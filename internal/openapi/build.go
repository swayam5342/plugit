package openapi

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/pb33f/go-yaml"
	v2 "github.com/pb33f/libopenapi/datamodel/high/v2"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
)

type builder struct {
	opts       Options
	is30       bool
	m          model.Manifest
	ops        []*model.Tool
	opIDs      map[model.OpKey]string
	extraWarns int
}

func (b *builder) warn(msg string) {
	if len(b.m.Warnings) >= maxWarnings {
		b.extraWarns++
		return
	}
	b.m.Warnings = append(b.m.Warnings, naming.CleanText(msg, 300))
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
			b.warn(e.Error())
		}
	}
}

func (b *builder) finish() *model.Manifest {
	if b.extraWarns > 0 {
		b.m.Warnings = append(b.m.Warnings, fmt.Sprintf("... and %d more warnings", b.extraWarns))
	}
	cands := make([]naming.Candidate, 0, len(b.ops))
	for _, t := range b.ops {
		cands = append(cands, naming.Candidate{Key: t.Key, OperationID: b.opIDs[t.Key]})
	}
	names := naming.Assign(cands, b.opts.Prefix)

	b.m.Tools = make([]model.Tool, 0, len(b.ops))
	for _, t := range b.ops {
		t.Name = names[t.Key]
		t.ID = naming.ToolID(t.Key)
		b.m.Tools = append(b.m.Tools, *t)
	}
	slices.SortFunc(b.m.Tools, func(x, y model.Tool) int { return cmp.Compare(x.Key, y.Key) })
	return &b.m
}

// addOp registers a tool, ignoring duplicate keys (first wins).
func (b *builder) addOp(t *model.Tool, operationID string) {
	if b.opIDs == nil {
		b.opIDs = map[model.OpKey]string{}
	}
	if _, dup := b.opIDs[t.Key]; dup {
		b.warn(fmt.Sprintf("duplicate operation %s ignored", t.Key))
		return
	}
	b.opIDs[t.Key] = operationID
	b.ops = append(b.ops, t)
}

func (b *builder) newTool(method, path, summary, description string, tags []string, deprecated bool) *model.Tool {
	return &model.Tool{
		Key:         model.NewOpKey(method, path),
		Method:      strings.ToUpper(method),
		Path:        path,
		Description: naming.Describe(summary, description, method, path, b.opts.MaxDescription),
		Tags:        cleanTags(tags),
		Deprecated:  deprecated,
		Safety:      model.SafetyForMethod(method),
	}
}

// maxTagLen caps a tag in runes.
const maxTagLen = 64

// cleanTags cleans tags like every other spec string: they reach the user's
// terminal (picker, hints), where an escape sequence could rewrite the
// screen. Empty and duplicate tags are dropped.
func cleanTags(tags []string) []string {
	var out []string
	for _, tag := range tags {
		if c := naming.CleanText(tag, maxTagLen); c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// validPath rejects path templates that could smuggle a query or fragment,
// climb out of the pinned base path, or carry invisible text into tool keys
// and descriptions. Path templates come from the untrusted spec.
func (b *builder) validPath(path string) bool {
	reason := ""
	switch {
	case !strings.HasPrefix(path, "/"):
		reason = "it must start with '/'"
	case strings.ContainsAny(path, `?#\`):
		reason = `it contains '?', '#' or '\'`
	case strings.IndexFunc(path, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r)
	}) >= 0:
		reason = "it contains whitespace, control or invisible characters"
	default:
		for seg := range strings.SplitSeq(path, "/") {
			if seg == "." || seg == ".." {
				reason = "it contains a '.' or '..' segment"
				break
			}
		}
	}
	if reason != "" {
		b.warn(fmt.Sprintf("path %+q skipped: %s", truncateRef(path), reason))
		return false
	}
	return true
}

// ---- OpenAPI 3.x ----

func (b *builder) fromV3(doc *v3.Document) {
	if doc.Info != nil {
		b.m.Title = naming.CleanText(doc.Info.Title, 200)
		b.m.Version = naming.CleanText(doc.Info.Version, 100)
	}
	for _, s := range doc.Servers {
		if s != nil && s.URL != "" {
			b.m.Servers = append(b.m.Servers, expandServer(s))
		}
	}
	if doc.Paths == nil || doc.Paths.PathItems == nil {
		return
	}
	for pair := doc.Paths.PathItems.First(); pair != nil; pair = pair.Next() {
		path, item := pair.Key(), pair.Value()
		if item == nil || !b.validPath(path) {
			continue
		}
		if item.Trace != nil {
			b.warn(fmt.Sprintf("TRACE %s skipped: TRACE echoes request credentials back", path))
		}
		if item.Query != nil || (item.AdditionalOperations != nil && item.AdditionalOperations.Len() > 0) {
			b.warn(fmt.Sprintf("non-standard methods on %s skipped", path))
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
	t := b.newTool(method, path, op.Summary, op.Description, op.Tags, op.Deprecated != nil && *op.Deprecated)
	c := newConverter(b.is30, b.opts.MaxDescription)

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
		b.addParam(t, c, p.Name, p.In, p.Required != nil && *p.Required, p.Description, schema)
	}

	if rb := op.RequestBody; rb != nil && rb.Content != nil && rb.Content.Len() > 0 {
		var all []string
		found := false
		for pair := rb.Content.First(); pair != nil; pair = pair.Next() {
			ct := normalizeMediaType(pair.Key())
			all = append(all, ct)
			if !found && isJSON(ct) && pair.Value() != nil {
				found = true
				t.Body = &model.Body{ContentType: "application/json", Schema: c.raw(pair.Value().Schema), Required: rb.Required != nil && *rb.Required}
			}
		}
		if !found {
			c.issue(model.IssueUnsupported, "request body content type(s) not supported yet: %s", strings.Join(all, ", "))
		}
	}
	t.Issues = c.issues
	b.addOp(t, op.OperationId)
}

// ---- Swagger 2.0 ----

func (b *builder) fromV2(doc *v2.Swagger) {
	if doc.Info != nil {
		b.m.Title = naming.CleanText(doc.Info.Title, 200)
		b.m.Version = naming.CleanText(doc.Info.Version, 100)
	}
	b.m.Servers = v2Servers(doc)
	if doc.Paths == nil || doc.Paths.PathItems == nil {
		return
	}
	for pair := doc.Paths.PathItems.First(); pair != nil; pair = pair.Next() {
		path, item := pair.Key(), pair.Value()
		if item == nil || !b.validPath(path) {
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
	t := b.newTool(method, path, op.Summary, op.Description, op.Tags, op.Deprecated)
	c := newConverter(false, b.opts.MaxDescription)
	consumes := op.Consumes
	if len(consumes) == 0 {
		consumes = globalConsumes
	}

	for _, p := range mergeParams(item.Parameters, op.Parameters, func(p *v2.Parameter) (string, string) { return p.In, p.Name }) {
		switch strings.ToLower(p.In) {
		case "body":
			ct, ok := pickJSON(consumes)
			if !ok {
				c.issue(model.IssueUnsupported, "request body content type(s) not supported yet: %s", strings.Join(consumes, ", "))
				continue
			}
			t.Body = &model.Body{ContentType: ct, Schema: c.raw(p.Schema), Required: p.Required != nil && *p.Required}
		case "formdata":
			c.issue(model.IssueUnsupported, "formData parameter %q not supported yet", p.Name)
		default:
			b.addParam(t, c, p.Name, p.In, p.Required != nil && *p.Required, p.Description, mustJSON(v2ParamSchema(c, p)))
		}
	}
	t.Issues = c.issues
	b.addOp(t, op.OperationId)
}

func v2ParamSchema(c *converter, p *v2.Parameter) map[string]any {
	if p.Type == "file" {
		c.issue(model.IssueUnsupported, "file parameter %q not supported yet", p.Name)
	}
	out := map[string]any{}
	setStr(out, "type", p.Type)
	setStr(out, "format", p.Format)
	setStr(out, "pattern", p.Pattern)
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
	if v, ok := decodeNode(p.Default); ok {
		out["default"] = v
	}
	if p.Items != nil {
		out["items"] = v2Items(p.Items, 1)
	}
	return out
}

// v2Items converts Swagger 2.0 array items. Numeric constraints are dropped:
// libopenapi exposes them as plain ints, so "absent" and "0" look the same,
// and dropping a constraint only makes the schema more permissive.
func v2Items(it *v2.Items, depth int) map[string]any {
	out := map[string]any{}
	if depth > MaxSchemaDepth {
		return out
	}
	setStr(out, "type", it.Type)
	setStr(out, "format", it.Format)
	setStr(out, "pattern", it.Pattern)
	setEnum(out, it.Enum)
	if it.Items != nil {
		out["items"] = v2Items(it.Items, depth+1)
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

// ---- shared ----

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

func (b *builder) addParam(t *model.Tool, c *converter, name, in string, required bool, desc string, schema json.RawMessage) {
	if name == "" {
		c.issue(model.IssueSchemaDegraded, "parameter without a name (or an unresolvable parameter $ref) ignored")
		return
	}
	var loc model.Location
	switch strings.ToLower(in) {
	case "path":
		loc, required = model.InPath, true // path params are always required
	case "query":
		loc = model.InQuery
	case "header":
		loc = model.InHeader
	case "cookie":
		c.issue(model.IssueUnsupported, "cookie parameter %q not supported yet", name)
		return
	default:
		c.issue(model.IssueUnsupported, "parameter %q in %q not supported", name, in)
		return
	}
	t.Params = append(t.Params, model.Param{
		Name:         name,
		UpstreamName: name,
		In:           loc,
		Required:     required,
		Schema:       schema,
		Desc:         naming.CleanText(desc, b.opts.MaxDescription),
	})
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
