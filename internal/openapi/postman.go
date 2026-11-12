package openapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
)

// Limits on a Postman collection (SPEC M6 design note: Postman import).
const (
	maxPostmanDepth  = 16   // folder nesting
	maxPostmanItems  = 2000 // requests imported
	maxInferDepth    = 12   // schema inferred from an example body
	postmanSchemaURL = "schema.getpostman.com/json/collection/v2"
)

// pmText is Postman's description: a string or {"content": "..."}.
type pmText string

func (t *pmText) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*t = pmText(s)
		return nil
	}
	var o struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(b, &o) == nil {
		*t = pmText(o.Content)
	}
	return nil
}

type pmKV struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Description pmText `json:"description"`
	Disabled    bool   `json:"disabled"`
	Type        string `json:"type"`
}

// pmURL is a request URL: a string (v2.0) or an object (v2.1).
type pmURL struct {
	Raw      string `json:"raw"`
	Query    []pmKV `json:"query"`
	Variable []pmKV `json:"variable"`
}

func (u *pmURL) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		u.Raw = s
		return nil
	}
	type plain pmURL
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*u = pmURL(p)
	return nil
}

type pmBody struct {
	Mode       string `json:"mode"`
	Raw        string `json:"raw"`
	URLEncoded []pmKV `json:"urlencoded"`
	FormData   []pmKV `json:"formdata"`
	Options    struct {
		Raw struct {
			Language string `json:"language"`
		} `json:"raw"`
	} `json:"options"`
}

type pmRequest struct {
	Method      string          `json:"method"`
	URL         pmURL           `json:"url"`
	Header      []pmKV          `json:"header"`
	Body        *pmBody         `json:"body"`
	Description pmText          `json:"description"`
	Auth        json.RawMessage `json:"auth"`
}

type pmItem struct {
	Name        string     `json:"name"`
	Description pmText     `json:"description"`
	Item        []pmItem   `json:"item"`
	Request     *pmRequest `json:"request"`
}

type pmCollection struct {
	Info struct {
		Name   string `json:"name"`
		Schema string `json:"schema"`
	} `json:"info"`
	Item     []pmItem        `json:"item"`
	Variable []pmKV          `json:"variable"`
	Auth     json.RawMessage `json:"auth"`
}

// isPostman reports whether data looks like a Postman v2 collection.
func isPostman(data []byte) bool {
	if !bytes.Contains(data, []byte(postmanSchemaURL)) {
		return false
	}
	var head struct {
		Info struct {
			Schema string `json:"schema"`
		} `json:"info"`
	}
	return json.Unmarshal(data, &head) == nil && strings.Contains(head.Info.Schema, postmanSchemaURL)
}

// postmanMethods are the methods plugit serves.
var postmanMethods = []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"}

// parsePostman converts a Postman v2.0/v2.1 collection into a manifest.
// Requests become tools, top-level folders become tags, and the
// collection's base URL variables are offered as servers (the user still
// pins base_url, T3). Collection auth is ignored: plugit's auth applies.
func parsePostman(data []byte, opts Options) (*model.Manifest, error) {
	var col pmCollection
	if err := json.Unmarshal(data, &col); err != nil {
		return nil, fmt.Errorf("parsing Postman collection: %w", err)
	}
	b := &builder{opts: opts}
	switch {
	case strings.Contains(col.Info.Schema, "v2.1"):
		b.m.SpecVersion = "postman-2.1"
	case strings.Contains(col.Info.Schema, "v2.0"):
		b.m.SpecVersion = "postman-2.0"
	default:
		return nil, fmt.Errorf("parsing Postman collection: %w (schema %q)", ErrUnsupportedSpec, naming.CleanText(col.Info.Schema, 100))
	}
	b.m.Title = naming.CleanText(col.Info.Name, 200)

	// Variables that hold an absolute URL are base URLs.
	vars := map[string]string{}
	for _, v := range col.Variable {
		if u, err := url.Parse(v.Value); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			vars[v.Key] = strings.TrimRight(v.Value, "/")
			b.addServer(vars[v.Key])
		}
	}
	p := &pmWalk{b: b, vars: vars}
	if len(col.Auth) > 0 && string(col.Auth) != "null" {
		p.authSeen = true
	}
	p.walk(col.Item, "", 1)
	if p.authSeen {
		b.warn("the collection's auth settings are ignored: set the credential with plugit add --header and plugit auth")
	}
	if p.tooDeep {
		b.warn(fmt.Sprintf("folders nested more than %d deep were skipped", maxPostmanDepth))
	}
	if p.skipped > 0 {
		b.warn(fmt.Sprintf("only the first %d requests were imported (%d more skipped)", maxPostmanItems, p.skipped))
	}
	return b.finish(), nil
}

func (b *builder) addServer(s string) {
	if !slices.Contains(b.m.Servers, s) {
		b.m.Servers = append(b.m.Servers, s)
	}
}

type pmWalk struct {
	b        *builder
	vars     map[string]string
	count    int
	skipped  int
	tooDeep  bool
	authSeen bool
}

func (p *pmWalk) walk(items []pmItem, tag string, depth int) {
	if depth > maxPostmanDepth {
		p.tooDeep = true
		return
	}
	for _, it := range items {
		switch {
		case it.Request != nil:
			if p.count >= maxPostmanItems {
				p.skipped++
				continue
			}
			p.count++
			p.request(it, tag)
		case it.Item != nil:
			t := tag
			if t == "" {
				t = it.Name // the top-level folder groups everything inside it
			}
			p.walk(it.Item, t, depth+1)
		}
	}
}

// request converts one Postman request.
func (p *pmWalk) request(it pmItem, tag string) {
	b := p.b
	r := it.Request
	method := strings.ToUpper(strings.TrimSpace(r.Method))
	if method == "" {
		method = "GET"
	}
	if !slices.Contains(postmanMethods, method) {
		b.warn(fmt.Sprintf("request %q uses method %s, which plugit doesn't serve; skipped", naming.CleanText(it.Name, 80), naming.CleanText(method, 20)))
		return
	}
	if len(r.Auth) > 0 && string(r.Auth) != "null" {
		p.authSeen = true
	}

	path, query, pathVars, ok := p.splitURL(r.URL.Raw)
	if !ok {
		b.warn(fmt.Sprintf("request %q has no usable URL; skipped", naming.CleanText(it.Name, 80)))
		return
	}
	var tags []string
	if tag != "" {
		tags = []string{tag}
	}
	desc := string(r.Description)
	if desc == "" {
		desc = string(it.Description)
	}
	// In Postman the description is the useful text; the name is a label.
	summary := ""
	if strings.TrimSpace(desc) == "" {
		summary = it.Name
	}
	t := b.newTool(method, path, summary, desc, tags, false)
	c := newConverter(true, b.opts.MaxDescription)
	c.hidden("summary", it.Name)
	c.hidden("description", desc)

	varDesc := map[string]string{}
	for _, v := range r.URL.Variable {
		varDesc[v.Key] = string(v.Description)
	}
	str := json.RawMessage(`{"type":"string"}`)
	for _, name := range pathVars {
		b.addParam(t, c, name, "path", true, varDesc[name], str)
	}

	seen := map[string]bool{}
	queryParams := r.URL.Query
	if queryParams == nil {
		queryParams = query
	}
	for _, q := range queryParams {
		if q.Disabled || q.Key == "" || seen[q.Key] {
			continue
		}
		seen[q.Key] = true
		b.addParam(t, c, q.Key, "query", false, string(q.Description), str)
	}
	for _, h := range r.Header {
		switch strings.ToLower(h.Key) {
		case "", "accept", "content-type", "user-agent", "content-length":
			continue // plugit sets these itself
		}
		if h.Disabled {
			continue
		}
		b.addParam(t, c, h.Key, "header", false, string(h.Description), str)
	}

	if r.Body != nil {
		p.body(t, c, r.Body)
	}
	t.Issues = c.issues
	b.addOp(t, it.Name)
}

// splitURL resolves base URL variables, records the server, and returns
// the path template, the query in the raw URL, and the path parameters.
func (p *pmWalk) splitURL(raw string) (path string, query []pmKV, pathVars []string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil, nil, false
	}
	rest, q, _ := strings.Cut(raw, "?")
	if vals, err := url.ParseQuery(q); err == nil {
		for _, k := range sortedQueryKeys(q) {
			query = append(query, pmKV{Key: k, Value: vals.Get(k)})
		}
	}
	resolved := rest
	for k, v := range p.vars {
		resolved = strings.ReplaceAll(resolved, "{{"+k+"}}", v)
	}
	switch {
	case p.matchesServer(resolved) != "":
		resolved = strings.TrimPrefix(resolved, p.matchesServer(resolved))
	case strings.HasPrefix(resolved, "http://") || strings.HasPrefix(resolved, "https://"):
		u, err := url.Parse(resolved)
		if err != nil || u.Host == "" {
			return "", nil, nil, false
		}
		p.b.addServer(u.Scheme + "://" + u.Host)
		resolved = u.EscapedPath()
		if unescaped, err := url.PathUnescape(resolved); err == nil {
			resolved = unescaped
		}
	case strings.HasPrefix(resolved, "{{"):
		// An unknown host variable: drop it, keep the path.
		if i := strings.Index(resolved, "}}"); i >= 0 {
			resolved = resolved[i+2:]
		}
	}
	var segs []string
	for seg := range strings.SplitSeq(resolved, "/") {
		switch {
		case seg == "":
			continue
		case strings.HasPrefix(seg, ":") && len(seg) > 1:
			name := seg[1:]
			pathVars = append(pathVars, name)
			seg = "{" + name + "}"
		case strings.HasPrefix(seg, "{{") && strings.HasSuffix(seg, "}}") && len(seg) > 4:
			name := seg[2 : len(seg)-2]
			pathVars = append(pathVars, name)
			seg = "{" + name + "}"
		}
		segs = append(segs, seg)
	}
	return "/" + strings.Join(segs, "/"), query, pathVars, true
}

func (p *pmWalk) matchesServer(u string) string {
	best := ""
	for _, s := range p.b.m.Servers {
		if (u == s || strings.HasPrefix(u, s+"/")) && len(s) > len(best) {
			best = s
		}
	}
	return best
}

func sortedQueryKeys(q string) []string {
	var keys []string
	for part := range strings.SplitSeq(q, "&") {
		k, _, _ := strings.Cut(part, "=")
		if k, err := url.QueryUnescape(k); err == nil && k != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

// body converts a Postman request body.
func (p *pmWalk) body(t *model.Tool, c *converter, body *pmBody) {
	switch body.Mode {
	case "raw":
		var v any
		dec := json.NewDecoder(strings.NewReader(body.Raw))
		dec.UseNumber()
		if dec.Decode(&v) != nil {
			if body.Options.Raw.Language == "json" {
				c.issue(model.IssueUnsupported, "the example JSON body doesn't parse, so its fields are unknown")
			} else if strings.TrimSpace(body.Raw) != "" {
				c.issue(model.IssueUnsupported, "raw %s request body not supported", naming.CleanText(cmpOr(body.Options.Raw.Language, "text"), 20))
			}
			return
		}
		t.Body = &model.Body{ContentType: "application/json", Schema: mustJSON(inferSchema(v, 1))}
	case "urlencoded", "formdata":
		props := map[string]any{}
		for _, f := range slices.Concat(body.URLEncoded, body.FormData) {
			if f.Disabled || f.Key == "" {
				continue
			}
			prop := map[string]any{"type": "string"}
			if f.Type == "file" {
				prop["format"] = "binary"
			}
			if d := naming.CleanText(string(f.Description), p.b.opts.MaxDescription); d != "" {
				prop["description"] = d
			}
			props[f.Key] = prop
		}
		ct := ctForm
		if body.Mode == "formdata" {
			ct = ctMultipart
		}
		t.Body = formBody(c, ct, mustJSON(map[string]any{"type": "object", "properties": props}), nil, false)
	case "":
	default:
		c.issue(model.IssueUnsupported, "%s request body not supported", naming.CleanText(body.Mode, 20))
	}
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// inferSchema builds a JSON Schema from an example value. Nothing is
// required: an example shows what can be sent, not what must be.
func inferSchema(v any, depth int) map[string]any {
	if depth > maxInferDepth {
		return map[string]any{}
	}
	switch x := v.(type) {
	case bool:
		return map[string]any{"type": "boolean"}
	case json.Number:
		if strings.ContainsAny(x.String(), ".eE") {
			return map[string]any{"type": "number"}
		}
		return map[string]any{"type": "integer"}
	case string:
		return map[string]any{"type": "string"}
	case []any:
		s := map[string]any{"type": "array"}
		if len(x) > 0 {
			s["items"] = inferSchema(x[0], depth+1)
		}
		return s
	case map[string]any:
		props := make(map[string]any, len(x))
		for k, e := range x {
			props[k] = inferSchema(e, depth+1)
		}
		return map[string]any{"type": "object", "properties": props}
	}
	return map[string]any{}
}
