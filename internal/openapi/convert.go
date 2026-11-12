package openapi

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/pb33f/go-yaml"
	"github.com/pb33f/libopenapi/datamodel/high/base"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
)

// converter turns libopenapi schemas into plain JSON Schema for one tool. It
// shares a node budget across all of the tool's schemas.
type converter struct {
	is30     bool // OpenAPI 3.0 semantics (nullable, boolean exclusive bounds)
	maxDesc  int
	budget   int
	issues   []model.Issue
	issueSet map[string]bool
}

func newConverter(is30 bool, maxDesc int) *converter {
	return &converter{is30: is30, maxDesc: maxDesc, budget: MaxSchemaNodes, issueSet: map[string]bool{}}
}

func (c *converter) issue(kind model.IssueKind, format string, args ...any) {
	detail := naming.CleanText(fmt.Sprintf(format, args...), 300)
	if key := string(kind) + "\x00" + detail; !c.issueSet[key] {
		c.issueSet[key] = true
		c.issues = append(c.issues, model.Issue{Kind: kind, Detail: detail})
	}
}

// hidden records that cleaning removed hidden content from where (L007).
func (c *converter) hidden(where, s string) {
	if r := naming.Hidden(s); r != "" {
		c.issue(model.IssueHiddenText, "%s contained %s (removed)", where, r)
	}
}

// raw converts sp and encodes it. Encoding a map sorts keys, so output is
// deterministic.
func (c *converter) raw(sp *base.SchemaProxy) json.RawMessage {
	return mustJSON(c.proxy(sp, 0))
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func (c *converter) proxy(sp *base.SchemaProxy, depth int) map[string]any {
	if sp == nil {
		return map[string]any{}
	}
	if depth > MaxSchemaDepth {
		c.issue(model.IssueSchemaDegraded, "schema nested deeper than %d levels (depth cap); deeper parts accept anything", MaxSchemaDepth)
		return map[string]any{}
	}
	if c.budget <= 0 {
		c.issue(model.IssueSchemaDegraded, "schema too large (more than %d nodes); remaining parts accept anything", MaxSchemaNodes)
		return map[string]any{}
	}
	c.budget--

	if sp.IsReference() && !strings.HasPrefix(sp.GetReference(), "#") {
		c.issue(model.IssueSchemaDegraded, "external $ref not resolved: %s", truncateRef(sp.GetReference()))
		return map[string]any{}
	}
	s := sp.Schema()
	if s == nil {
		if sp.IsReference() {
			c.issue(model.IssueSchemaDegraded, "unresolvable $ref: %s", truncateRef(sp.GetReference()))
		} else {
			c.issue(model.IssueSchemaDegraded, "invalid schema")
		}
		return map[string]any{}
	}
	return c.schema(s, depth)
}

func (c *converter) schema(s *base.Schema, depth int) map[string]any {
	out := map[string]any{}
	if s.BooleanSchema != nil {
		if *s.BooleanSchema {
			return out
		}
		return map[string]any{"not": map[string]any{}}
	}

	types := append([]string(nil), s.Type...)
	if c.is30 && s.Nullable != nil && *s.Nullable && len(types) > 0 && !slices.Contains(types, "null") {
		types = append(types, "null")
	}
	switch len(types) {
	case 0:
	case 1:
		out["type"] = types[0]
	default:
		out["type"] = types
	}

	c.hidden("schema title", s.Title)
	c.hidden("schema description", s.Description)
	if t := naming.CleanText(s.Title, c.maxDesc); t != "" {
		out["title"] = t
	}
	if d := naming.CleanText(s.Description, c.maxDesc); d != "" {
		out["description"] = d
	}
	setStr(out, "format", s.Format)
	setStr(out, "pattern", s.Pattern)
	setPtr(out, "multipleOf", s.MultipleOf)
	setPtr(out, "minimum", s.Minimum)
	setPtr(out, "maximum", s.Maximum)
	c.exclusive(out, "exclusiveMinimum", "minimum", s.ExclusiveMinimum)
	c.exclusive(out, "exclusiveMaximum", "maximum", s.ExclusiveMaximum)
	setPtr(out, "minLength", s.MinLength)
	setPtr(out, "maxLength", s.MaxLength)
	setPtr(out, "minItems", s.MinItems)
	setPtr(out, "maxItems", s.MaxItems)
	setPtr(out, "uniqueItems", s.UniqueItems)
	setPtr(out, "minProperties", s.MinProperties)
	setPtr(out, "maxProperties", s.MaxProperties)

	setEnum(out, s.Enum)
	if v, ok := decodeNode(s.Const); ok {
		out["const"] = v
	}
	if v, ok := decodeNode(s.Default); ok {
		out["default"] = v
	}

	if s.Properties != nil && s.Properties.Len() > 0 {
		props := map[string]any{}
		for pair := s.Properties.First(); pair != nil; pair = pair.Next() {
			// readOnly properties are set by the server; never ask the model for them.
			if child := pair.Value().Schema(); child != nil && child.ReadOnly != nil && *child.ReadOnly {
				continue
			}
			props[pair.Key()] = c.proxy(pair.Value(), depth+1)
		}
		out["properties"] = props
		if req := filterRequired(s.Required, props); len(req) > 0 {
			out["required"] = req
		}
	} else if len(s.Required) > 0 {
		out["required"] = append([]string(nil), s.Required...)
	}

	if s.Items != nil {
		if s.Items.IsA() {
			out["items"] = c.proxy(s.Items.A, depth+1)
		} else {
			out["items"] = s.Items.B
		}
	}
	if s.AdditionalProperties != nil {
		if s.AdditionalProperties.IsA() {
			out["additionalProperties"] = c.proxy(s.AdditionalProperties.A, depth+1)
		} else {
			out["additionalProperties"] = s.AdditionalProperties.B
		}
	}
	for key, list := range map[string][]*base.SchemaProxy{"allOf": s.AllOf, "oneOf": s.OneOf, "anyOf": s.AnyOf} {
		if len(list) == 0 {
			continue
		}
		arr := make([]any, 0, len(list))
		for _, sp := range list {
			arr = append(arr, c.proxy(sp, depth+1))
		}
		out[key] = arr
	}
	if s.Not != nil {
		out["not"] = c.proxy(s.Not, depth+1)
	}

	var ignored []string
	for name, present := range map[string]bool{
		"if": s.If != nil, "then": s.Then != nil, "else": s.Else != nil,
		"patternProperties": s.PatternProperties != nil, "prefixItems": len(s.PrefixItems) > 0,
		"dependentSchemas": s.DependentSchemas != nil, "unevaluatedItems": s.UnevaluatedItems != nil,
		"unevaluatedProperties": s.UnevaluatedProperties != nil, "$dynamicRef": s.DynamicRef != "",
		"contains": s.Contains != nil, "propertyNames": s.PropertyNames != nil,
	} {
		if present {
			ignored = append(ignored, name)
		}
	}
	if len(ignored) > 0 {
		slices.Sort(ignored)
		c.issue(model.IssueSchemaDegraded, "unsupported JSON Schema keywords ignored: %s", strings.Join(ignored, ", "))
	}
	return out
}

// exclusive maps OpenAPI 3.0 boolean exclusive bounds onto JSON Schema's
// numeric form, and passes 3.1 numeric bounds through.
func (c *converter) exclusive(out map[string]any, key, plain string, dv *base.DynamicValue[bool, float64]) {
	if dv == nil {
		return
	}
	if dv.IsB() {
		out[key] = dv.B
		return
	}
	if dv.A {
		if v, ok := out[plain]; ok {
			out[key] = v
			delete(out, plain)
		}
	}
}

func filterRequired(req []string, props map[string]any) []string {
	var out []string
	for _, r := range req {
		if _, ok := props[r]; ok {
			out = append(out, r)
		}
	}
	return out
}

// decodeNode turns a YAML value (enum entry, default, const) into a
// JSON-encodable Go value.
func decodeNode(n *yaml.Node) (any, bool) {
	if n == nil {
		return nil, false
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, false
	}
	v = jsonable(v)
	if _, err := json.Marshal(v); err != nil {
		return nil, false
	}
	return v, true
}

// jsonable converts map[any]any (possible from YAML) into map[string]any.
func jsonable(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = jsonable(e)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[fmt.Sprint(k)] = jsonable(e)
		}
		return m
	case []any:
		for i, e := range x {
			x[i] = jsonable(e)
		}
		return x
	}
	return v
}

func setStr(out map[string]any, key, v string) {
	if v != "" {
		out[key] = v
	}
}

func setPtr[T any](out map[string]any, key string, v *T) {
	if v != nil {
		out[key] = *v
	}
}
