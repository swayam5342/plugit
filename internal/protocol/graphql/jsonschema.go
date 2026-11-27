package graphql

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/swayam5342/plugit/internal/naming"
)

// Limits on argument mapping. The input schema is untrusted (T18), so the
// walk must terminate on self-referential and very wide input objects.
const (
	// maxSchemaDepth is the number of nested schema levels (lists and named
	// types) mapped; deeper parts become {}. The argument itself is level 1.
	maxSchemaDepth = 12
	// maxSchemaNodes is the node budget for one tool, shared by all of its
	// arguments mapped through one argMapper (SPEC 3.5 rule 3); once spent,
	// the remaining parts become {}.
	maxSchemaNodes = 5000
	// maxDefaultLen caps the JSON encoding of an input field's default value
	// shown as "default"; a longer default is left out. Defaults repeat with
	// every use of their input type, so this bounds what they add.
	maxDefaultLen = 256
	// maxDegradedReasons caps the distinct reasons kept per argument.
	maxDegradedReasons = 4
)

// ArgSchema maps the GraphQL type t of one field argument to the JSON Schema
// the model sees for that tool parameter, with a node budget of its own. A
// tool builder mapping several arguments must use one newArgMapper per tool
// instead, so the budget is shared.
func ArgSchema(s *ast.Schema, def *ast.Value, t *ast.Type) (schema json.RawMessage, required, degraded bool) {
	return newArgMapper(s).arg(def, t)
}

// argMapper maps the arguments of one tool. Its node budget is shared by
// every arg call on it; the degraded reasons and the hidden-text findings
// are per call.
type argMapper struct {
	s          *ast.Schema
	nodes      int
	reasons    []string
	hiddenList []string
	hiddenSeen map[string]bool
}

// newArgMapper returns a mapper with a fresh maxSchemaNodes budget. Create
// one per tool.
func newArgMapper(s *ast.Schema) *argMapper {
	return &argMapper{s: s}
}

// arg maps the GraphQL type t of one field argument, whose default value is
// def, to a JSON Schema.
//
// required is true when t is non-null and def is nil: a non-null argument
// with a default may be omitted. Inside input objects the same rule decides
// each field's place in "required". degraded is true when any part of this
// argument's schema could not be mapped (a custom scalar, an unknown or
// non-input type, or the depth cap or the tool's node budget was hit) and was
// replaced by {}, which accepts anything.
//
// The output is deterministic: object keys are sorted, enum values keep
// schema order and "required" is sorted.
func (m *argMapper) arg(def *ast.Value, t *ast.Type) (schema json.RawMessage, required, degraded bool) {
	m.reasons = nil
	m.hiddenList, m.hiddenSeen = nil, nil
	out := m.mapType(t, 1)
	b, err := json.Marshal(out)
	if err != nil {
		// Only strings, numbers, slices and maps are built, so this cannot fail.
		b = []byte(`{}`)
		m.degrade("encoding failed")
	}
	return b, t != nil && t.NonNull && def == nil, len(m.reasons) > 0
}

// degradedReasons returns why the most recent arg call was degraded: each
// distinct reason ("custom scalar DateTime", "depth cap", "node budget",
// ...), in the order met, at most maxDegradedReasons. It is empty when the
// call was not degraded.
func (m *argMapper) degradedReasons() []string {
	return m.reasons
}

// hidden returns what the most recent arg call's description cleaning
// removed that a reader would never see (lint L007), one deduplicated
// "<where> contained <what> (removed)" detail per description.
func (m *argMapper) hidden() []string {
	return m.hiddenList
}

// Hidden-text finding wordings: a description is cleaned before the model
// sees it; a default value is part of what the API accepts and is left in
// (the same wording as REST's schema values).
const (
	hiddenRemoved = " contained %s (removed)"
	hiddenLeftIn  = " contains %s (left in: it is part of what the API accepts)"
)

// noteHidden records a hidden-text finding for the description s of where.
func (m *argMapper) noteHidden(where, s string) {
	m.noteHiddenAs(where, s, hiddenRemoved)
}

// noteHiddenAs records a hidden-text finding for s, worded by format
// (hiddenRemoved or hiddenLeftIn).
func (m *argMapper) noteHiddenAs(where, s, format string) {
	r := naming.Hidden(s)
	if r == "" {
		return
	}
	d := naming.CleanText(where+fmt.Sprintf(format, r), maxIssueLen)
	if m.hiddenSeen == nil {
		m.hiddenSeen = map[string]bool{}
	}
	if !m.hiddenSeen[d] {
		m.hiddenSeen[d] = true
		m.hiddenList = append(m.hiddenList, d)
	}
}

// degrade records why the call is degraded and returns the accept-anything
// schema.
func (m *argMapper) degrade(reason string) map[string]any {
	if !slices.Contains(m.reasons, reason) && len(m.reasons) < maxDegradedReasons {
		m.reasons = append(m.reasons, reason)
	}
	return map[string]any{}
}

func (m *argMapper) mapType(t *ast.Type, depth int) map[string]any {
	switch {
	case t == nil || m.s == nil:
		return m.degrade("no type")
	case depth > maxSchemaDepth:
		return m.degrade("depth cap")
	case m.nodes >= maxSchemaNodes:
		return m.degrade("node budget")
	}
	m.nodes++
	if t.Elem != nil {
		return map[string]any{"type": "array", "items": m.mapType(t.Elem, depth+1)}
	}
	d := m.s.Types[t.NamedType]
	if d == nil {
		return m.degrade("unknown type " + t.NamedType)
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
		return m.degrade("non-input type " + d.Name)
	}
}

func (m *argMapper) scalar(name string) map[string]any {
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
		return m.degrade("custom scalar " + name)
	}
}

func (m *argMapper) enum(d *ast.Definition) map[string]any {
	if len(d.EnumValues) == 0 {
		return m.degrade("enum " + d.Name + " without values")
	}
	values := make([]string, 0, len(d.EnumValues))
	for _, v := range d.EnumValues {
		values = append(values, v.Name)
	}
	out := map[string]any{"enum": values}
	m.noteHidden("enum "+d.Name+" description", d.Description)
	if desc := naming.CleanText(d.Description, 0); desc != "" {
		out["description"] = desc
	}
	return out
}

func (m *argMapper) inputObject(d *ast.Definition, depth int) map[string]any {
	props := make(map[string]any, len(d.Fields))
	var req []string
	for _, f := range d.Fields {
		ps := m.mapType(f.Type, depth+1)
		m.noteHidden("input "+d.Name+" field "+f.Name+" description", f.Description)
		if desc := naming.CleanText(f.Description, 0); desc != "" {
			ps["description"] = desc
		}
		if v, ok := defaultJSON(f.DefaultValue); ok {
			ps["default"] = v
			m.noteHiddenValue("input "+d.Name+" field "+f.Name+" default value", v)
		}
		props[f.Name] = ps
		if f.Type != nil && f.Type.NonNull && f.DefaultValue == nil {
			req = append(req, f.Name)
		}
	}
	// A GraphQL input object is closed: the server rejects any other field.
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(req) > 0 {
		// Field names are unique (the schema loader rejects duplicates).
		slices.Sort(req)
		out["required"] = req
	}
	return out
}

// noteHiddenValue records a hidden-text finding for every string in the
// JSON value v (a default). The value reaches the model unchanged, so the
// finding says it was left in.
func (m *argMapper) noteHiddenValue(where string, v any) {
	switch x := v.(type) {
	case string:
		m.noteHiddenAs(where, x, hiddenLeftIn)
	case []any:
		for _, e := range x {
			m.noteHiddenValue(where, e)
		}
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(x)) {
			m.noteHiddenValue(where, k)
			m.noteHiddenValue(where, x[k])
		}
	}
}

// defaultJSON converts a constant GraphQL default value to its JSON value:
// numbers as written, enum values as strings, lists and input objects as
// arrays and objects. It reports false for no default, for a value holding a
// variable, and for one whose JSON encoding is longer than maxDefaultLen.
func defaultJSON(v *ast.Value) (any, bool) {
	out, ok := defaultValueJSON(v)
	if !ok {
		return nil, false
	}
	b, err := json.Marshal(out)
	if err != nil || len(b) > maxDefaultLen {
		return nil, false
	}
	return out, true
}

func defaultValueJSON(v *ast.Value) (any, bool) {
	if v == nil {
		return nil, false
	}
	switch v.Kind {
	case ast.IntValue, ast.FloatValue:
		return json.Number(v.Raw), true
	case ast.StringValue, ast.BlockValue, ast.EnumValue:
		return v.Raw, true
	case ast.BooleanValue:
		return v.Raw == "true", true
	case ast.NullValue:
		return nil, true
	case ast.ListValue:
		out := make([]any, 0, len(v.Children))
		for _, c := range v.Children {
			e, ok := defaultValueJSON(c.Value)
			if !ok {
				return nil, false
			}
			out = append(out, e)
		}
		return out, true
	case ast.ObjectValue:
		out := make(map[string]any, len(v.Children))
		for _, c := range v.Children {
			e, ok := defaultValueJSON(c.Value)
			if !ok {
				return nil, false
			}
			out[c.Name] = e
		}
		return out, true
	default:
		return nil, false
	}
}
