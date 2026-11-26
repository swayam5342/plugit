package openapi

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/pb33f/go-yaml"
	v2 "github.com/pb33f/libopenapi/datamodel/high/v2"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/protocol/rest/restutil"
	"github.com/swayam5342/plugit/internal/utils"
)

type builder struct {
	restutil.Builder
	is30 bool
}

// warnErr records non-fatal libopenapi errors (missing components, ...).
func (b *builder) warnErr(err error) {
	if err == nil {
		return
	}
	var errs []error
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		errs = j.Unwrap()
	} else {
		errs = []error{err}
	}
	for _, e := range errs {
		if e != nil {
			b.Warn(e.Error())
		}
	}
}

func (b *builder) fromV3(doc *v3.Document) {
	if doc.Info != nil {
		b.M.Title = naming.CleanText(doc.Info.Title, 200)
		b.M.Version = naming.CleanText(doc.Info.Version, 100)
	}
	for _, s := range doc.Servers {
		if s != nil && s.URL != "" {
			b.M.Endpoints = append(b.M.Endpoints, expandServer(s))
		}
	}
	if doc.Paths == nil || doc.Paths.PathItems == nil {
		return
	}
	for pair := doc.Paths.PathItems.First(); pair != nil; pair = pair.Next() {
		path, item := pair.Key(), pair.Value()
		if item == nil || !b.ValidPath(path) {
			continue
		}
		if item.Trace != nil {
			b.Warn(fmt.Sprintf("TRACE %s skipped: TRACE echoes request credentials back", path))
		}
		if item.Query != nil || (item.AdditionalOperations != nil && item.AdditionalOperations.Len() > 0) {
			b.Warn(fmt.Sprintf("non-standard methods on %s skipped", path))
		}
		for _, mo := range []struct {
			method string
			op     *v3.Operation
		}{
			{"GET", item.Get}, {"PUT", item.Put}, {"POST", item.Post}, {"DELETE", item.Delete},
			{"OPTIONS", item.Options}, {"HEAD", item.Head}, {"PATCH", item.Patch},
		} {
			if mo.op != nil {
				b.v3Op(mo.method, path, item, mo.op)
			}
		}
	}
}

func expandServer(s *v3.Server) string {
	u := s.URL
	if s.Variables != nil {
		for pair := s.Variables.First(); pair != nil; pair = pair.Next() {
			if v := pair.Value(); v != nil {
				u = strings.ReplaceAll(u, "{"+pair.Key()+"}", v.Default)
			}
		}
	}
	return u
}

func (b *builder) v3Op(method, path string, item *v3.PathItem, op *v3.Operation) {
	t := b.NewTool(method, path, op.Summary, op.Description, op.Tags, op.Deprecated != nil && *op.Deprecated)
	c := newConverter(b.is30, b.Opts.MaxDescription)
	c.Hidden("summary", op.Summary)
	c.Hidden("description", op.Description)

	for _, p := range mergeParams(item.Parameters, op.Parameters, func(p *v3.Parameter) (string, string) { return p.In, p.Name }) {
		var schema json.RawMessage
		switch {
		case p.Schema != nil:
			schema = c.raw(p.Schema)
		case p.Content != nil && p.Content.Len() > 0:
			schema = c.raw(p.Content.First().Value().Schema)
		default:
			schema = json.RawMessage(`{}`)
		}
		b.AddParam(t, c.Collector, p.Name, p.In, p.Required != nil && *p.Required, p.Description, schema)
	}

	if rb := op.RequestBody; rb != nil && rb.Content != nil && rb.Content.Len() > 0 {
		var all []string
		var jsonMT, formMT, multiMT *v3.MediaType
		for pair := rb.Content.First(); pair != nil; pair = pair.Next() {
			ct := normalizeMediaType(pair.Key())
			all = append(all, ct)
			switch mt := pair.Value(); {
			case mt == nil:
			case isJSON(ct) && jsonMT == nil:
				jsonMT = mt
			case ct == restutil.CTForm && formMT == nil:
				formMT = mt
			case ct == restutil.CTMultipart && multiMT == nil:
				multiMT = mt
			}
		}
		required := rb.Required != nil && *rb.Required
		switch {
		case jsonMT != nil:
			t.HTTP.Body = &model.Body{ContentType: "application/json", Schema: c.raw(jsonMT.Schema), Required: required}
		case formMT != nil:
			t.HTTP.Body = restutil.FormBody(c.Collector, restutil.CTForm, c.raw(formMT.Schema), v3Encodings(formMT), required)
		case multiMT != nil:
			t.HTTP.Body = restutil.FormBody(c.Collector, restutil.CTMultipart, c.raw(multiMT.Schema), v3Encodings(multiMT), required)
		default:
			c.Issue(model.IssueUnsupported, "request body content type(s) not supported yet: %s", strings.Join(all, ", "))
		}
	}
	t.Issues = c.Issues()
	b.AddOp(t, op.OperationId)
}

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

// v3Encodings reads a media type's per-field encoding, resolving explode:
// it defaults to true for style form and false otherwise (OpenAPI 3).
func v3Encodings(mt *v3.MediaType) map[string]model.Encoding {
	if mt.Encoding == nil || mt.Encoding.Len() == 0 {
		return nil
	}
	out := map[string]model.Encoding{}
	for pair := mt.Encoding.First(); pair != nil; pair = pair.Next() {
		e := pair.Value()
		if e == nil {
			continue
		}
		style := e.Style
		if style == "" {
			style = "form"
		}
		if style != "form" && style != "deepObject" {
			continue
		}
		explode := style == "form"
		if e.Explode != nil {
			explode = *e.Explode
		}
		out[pair.Key()] = model.Encoding{Style: style, Explode: explode}
	}
	return out
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

func setEnum(out map[string]any, nodes []*yaml.Node) {
	if len(nodes) == 0 {
		return
	}
	vals := make([]any, 0, len(nodes))
	for _, n := range nodes {
		if v, ok := decodeNode(n); ok {
			vals = append(vals, v)
		}
	}
	out["enum"] = vals
}

// mergeParams applies operation-level parameters over path-level ones with
// the same location and name, keeping first-seen order.
func mergeParams[P any](pathLevel, opLevel []*P, id func(*P) (string, string)) []*P {
	var out []*P
	pos := map[string]int{}
	for _, list := range [][]*P{pathLevel, opLevel} {
		for _, p := range list {
			if p == nil {
				continue
			}
			in, name := id(p)
			in = strings.ToLower(in)
			if in == "header" {
				name = strings.ToLower(name)
			}
			key := in + "\x00" + name
			if i, ok := pos[key]; ok {
				out[i] = p
				continue
			}
			pos[key] = len(out)
			out = append(out, p)
		}
	}
	return out
}

func normalizeMediaType(ct string) string {
	ct, _, _ = strings.Cut(ct, ";")
	return strings.ToLower(strings.TrimSpace(ct))
}

func isJSON(ct string) bool {
	return ct == "application/json" || (strings.HasPrefix(ct, "application/") && strings.HasSuffix(ct, "+json"))
}

func pickJSON(consumes []string) (string, bool) {
	if len(consumes) == 0 {
		return "application/json", true
	}
	for _, c := range consumes {
		if ct := normalizeMediaType(c); isJSON(ct) {
			return "application/json", true
		}
	}
	return "", false
}
