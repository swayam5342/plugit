package openapi

import (
	"slices"
	"strings"

	v2 "github.com/pb33f/libopenapi/datamodel/high/v2"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/protocol/rest/restutil"
	"github.com/swayam5342/plugit/internal/utils"
)

func (b *builder) fromV2(doc *v2.Swagger) {
	if doc.Info != nil {
		b.M.Title = naming.CleanText(doc.Info.Title, 200)
		b.M.Version = naming.CleanText(doc.Info.Version, 100)
	}
	b.M.Endpoints = v2Servers(doc)
	if doc.Paths == nil || doc.Paths.PathItems == nil {
		return
	}
	for pair := doc.Paths.PathItems.First(); pair != nil; pair = pair.Next() {
		path, item := pair.Key(), pair.Value()
		if item == nil || !b.ValidPath(path) {
			continue
		}
		for _, mo := range []struct {
			method string
			op     *v2.Operation
		}{
			{"GET", item.Get}, {"PUT", item.Put}, {"POST", item.Post}, {"DELETE", item.Delete},
			{"OPTIONS", item.Options}, {"HEAD", item.Head}, {"PATCH", item.Patch},
		} {
			if mo.op != nil {
				b.v2Op(mo.method, path, item, mo.op, doc.Consumes)
			}
		}
	}
}

func v2Servers(doc *v2.Swagger) []string {
	if doc.Host == "" {
		if doc.BasePath != "" {
			return []string{doc.BasePath}
		}
		return nil
	}
	schemes := doc.Schemes
	if len(schemes) == 0 {
		schemes = []string{"https"}
	}
	out := make([]string, 0, len(schemes))
	for _, s := range schemes {
		out = append(out, strings.ToLower(s)+"://"+doc.Host+doc.BasePath)
	}
	return out
}

func (b *builder) v2Op(method, path string, item *v2.PathItem, op *v2.Operation, globalConsumes []string) {
	t := b.NewTool(method, path, op.Summary, op.Description, op.Tags, op.Deprecated)
	c := newConverter(false, b.Opts.MaxDescription)
	c.Hidden("summary", op.Summary)
	c.Hidden("description", op.Description)
	consumes := op.Consumes
	if len(consumes) == 0 {
		consumes = globalConsumes
	}

	form := map[string]any{}
	var formRequired []any
	formAnyRequired := false
	for _, p := range mergeParams(item.Parameters, op.Parameters, func(p *v2.Parameter) (string, string) { return p.In, p.Name }) {
		switch strings.ToLower(p.In) {
		case "body":
			ct, ok := pickJSON(consumes)
			if !ok {
				c.Issue(model.IssueUnsupported, "request body content type(s) not supported yet: %s", strings.Join(consumes, ", "))
				continue
			}
			t.HTTP.Body = &model.Body{ContentType: ct, Schema: c.raw(p.Schema), Required: p.Required != nil && *p.Required}
		case "formdata":
			prop := v2ParamSchema(c, p)
			if d := naming.CleanText(p.Description, b.Opts.MaxDescription); d != "" {
				prop["description"] = d
			}
			form[p.Name] = prop
			if p.Required != nil && *p.Required {
				formRequired = append(formRequired, p.Name)
				formAnyRequired = true
			}
		default:
			b.AddParam(t, c.Collector, p.Name, p.In, p.Required != nil && *p.Required, p.Description, utils.MustJSON(v2ParamSchema(c, p)))
		}
	}
	if len(form) > 0 && t.HTTP.Body == nil {
		ct := restutil.CTForm
		if slices.ContainsFunc(consumes, func(x string) bool { return normalizeMediaType(x) == restutil.CTMultipart }) &&
			!slices.ContainsFunc(consumes, func(x string) bool { return normalizeMediaType(x) == restutil.CTForm }) {
			ct = restutil.CTMultipart
		}
		obj := map[string]any{"type": "object", "properties": form}
		if len(formRequired) > 0 {
			obj["required"] = formRequired
		}
		t.HTTP.Body = restutil.FormBody(c.Collector, ct, utils.MustJSON(obj), nil, formAnyRequired)
	}
	t.Issues = c.Issues()
	b.AddOp(t, op.OperationId)
}

func v2ParamSchema(c *converter, p *v2.Parameter) map[string]any {
	if p.Type == "file" && !strings.EqualFold(p.In, "formData") {
		c.Issue(model.IssueUnsupported, "file parameter %q not supported yet", p.Name)
	}
	out := map[string]any{}
	types, isFile := knownTypes([]string{p.Type})
	if len(types) == 1 {
		out["type"] = types[0]
	}
	setStr(out, "format", p.Format)
	setStr(out, "pattern", p.Pattern)
	if isFile {
		markFile(out)
	}
	c.hiddenValue("schema format", p.Format)
	c.hiddenValue("schema pattern", p.Pattern)
	setPtr(out, "minimum", p.Minimum)
	setPtr(out, "maximum", p.Maximum)
	if p.ExclusiveMinimum != nil && *p.ExclusiveMinimum && p.Minimum != nil {
		out["exclusiveMinimum"] = *p.Minimum
		delete(out, "minimum")
	}
	if p.ExclusiveMaximum != nil && *p.ExclusiveMaximum && p.Maximum != nil {
		out["exclusiveMaximum"] = *p.Maximum
		delete(out, "maximum")
	}
	setPtr(out, "minLength", p.MinLength)
	setPtr(out, "maxLength", p.MaxLength)
	setPtr(out, "minItems", p.MinItems)
	setPtr(out, "maxItems", p.MaxItems)
	setPtr(out, "uniqueItems", p.UniqueItems)
	setPtr(out, "multipleOf", p.MultipleOf)
	setEnum(out, p.Enum)
	c.hiddenValue("schema enum value", out["enum"])
	if v, ok := decodeNode(p.Default); ok {
		out["default"] = v
		c.hiddenValue("schema default value", v)
	}
	if p.Items != nil {
		out["items"] = v2Items(c, p.Items, 1)
	}
	return out
}

// v2Items converts Swagger 2.0 array items. Numeric constraints are dropped:
// libopenapi exposes them as plain ints, so "absent" and "0" look the same,
// and dropping a constraint only makes the schema more permissive.
func v2Items(c *converter, it *v2.Items, depth int) map[string]any {
	out := map[string]any{}
	if depth > MaxSchemaDepth {
		return out
	}
	if types, _ := knownTypes([]string{it.Type}); len(types) == 1 {
		out["type"] = types[0]
	}
	setStr(out, "format", it.Format)
	setStr(out, "pattern", it.Pattern)
	c.hiddenValue("schema format", it.Format)
	c.hiddenValue("schema pattern", it.Pattern)
	setEnum(out, it.Enum)
	c.hiddenValue("schema enum value", out["enum"])
	if it.Items != nil {
		out["items"] = v2Items(c, it.Items, depth+1)
	}
	return out
}
