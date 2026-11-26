package graphql

import (
	"encoding/json"
	"math"
	"slices"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/swayam5342/plugit/internal/naming"
)

// Limits on one ArgSchema call. The input schema is untrusted (T18), so the
// walk must terminate on self-referential and very wide input objects.
const (
	// maxSchemaDepth is the number of nested schema levels (lists and named
	// types) mapped; deeper parts become {}. The argument itself is level 1.
	maxSchemaDepth = 12
	// maxSchemaNodes is the node budget for one argument; once spent, the
	// remaining parts become {}.
	maxSchemaNodes = 5000
)

// ArgSchema maps the GraphQL type t of one field argument to the JSON Schema
// the model sees for that tool parameter. def is the argument's default value.
//
// required is true when t is non-null and def is nil: a non-null argument
// with a default may be omitted. Inside input objects the same rule decides
// each field's place in "required". degraded is true when any part of the
// schema could not be mapped (a custom scalar, an unknown or non-input type,
// or the depth cap or node budget was hit) and was replaced by {}, which
// accepts anything.
//
// The output is deterministic: object keys are sorted, enum values keep
// schema order and "required" is sorted.
func ArgSchema(s *ast.Schema, def *ast.Value, t *ast.Type) (schema json.RawMessage, required, degraded bool) {
	m := &mapper{s: s}
	out := m.mapType(t, 1)
	b, err := json.Marshal(out)
	if err != nil {
		// Only strings, numbers, slices and maps are built, so this cannot fail.
		b, m.degraded = []byte(`{}`), true
	}
	return b, t != nil && t.NonNull && def == nil, m.degraded
}

type mapper struct {
	s        *ast.Schema
	nodes    int
	degraded bool
}

// degrade marks the call degraded and returns the accept-anything schema.
func (m *mapper) degrade() map[string]any {
	m.degraded = true
	return map[string]any{}
}

func (m *mapper) mapType(t *ast.Type, depth int) map[string]any {
	if t == nil || m.s == nil || depth > maxSchemaDepth || m.nodes >= maxSchemaNodes {
		return m.degrade()
	}
	m.nodes++
	if t.Elem != nil {
		return map[string]any{"type": "array", "items": m.mapType(t.Elem, depth+1)}
	}
	d := m.s.Types[t.NamedType]
	if d == nil {
		return m.degrade()
	}
	switch d.Kind {
	case ast.Scalar:
		return m.scalar(d.Name)
	case ast.Enum:
		return m.enum(d)
	case ast.InputObject:
		return m.inputObject(d, depth)
	default:
		// Objects, interfaces and unions are not valid input types.
		return m.degrade()
	}
}

func (m *mapper) scalar(name string) map[string]any {
	switch name {
	case "Int":
		// GraphQL Int is a signed 32-bit integer.
		return map[string]any{"type": "integer", "minimum": int64(math.MinInt32), "maximum": int64(math.MaxInt32)}
	case "Float":
		return map[string]any{"type": "number"}
	case "String", "ID":
		return map[string]any{"type": "string"}
	case "Boolean":
		return map[string]any{"type": "boolean"}
	default:
		// A custom scalar's wire format is unknown.
		return m.degrade()
	}
}

func (m *mapper) enum(d *ast.Definition) map[string]any {
	if len(d.EnumValues) == 0 {
		return m.degrade()
	}
	values := make([]string, 0, len(d.EnumValues))
	for _, v := range d.EnumValues {
		values = append(values, v.Name)
	}
	out := map[string]any{"enum": values}
	if desc := naming.CleanText(d.Description, 0); desc != "" {
		out["description"] = desc
	}
	return out
}

func (m *mapper) inputObject(d *ast.Definition, depth int) map[string]any {
	props := make(map[string]any, len(d.Fields))
	var req []string
	for _, f := range d.Fields {
		ps := m.mapType(f.Type, depth+1)
		if desc := naming.CleanText(f.Description, 0); desc != "" {
			ps["description"] = desc
		}
		props[f.Name] = ps
		if f.Type != nil && f.Type.NonNull && f.DefaultValue == nil {
			req = append(req, f.Name)
		}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(req) > 0 {
		slices.Sort(req)
		out["required"] = slices.Compact(req)
	}
	return out
}
