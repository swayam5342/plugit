package postman

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/protocol/rest/restutil"
	"github.com/swayam5342/plugit/internal/utils"
)

type pmWalk struct {
	b        *restutil.Builder
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
				t = it.Name
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
		b.Warn(fmt.Sprintf("request %q uses method %s, which plugit doesn't serve; skipped", naming.CleanText(it.Name, 80), naming.CleanText(method, 20)))
		return
	}
	if len(r.Auth) > 0 && string(r.Auth) != "null" {
		p.authSeen = true
	}

	path, query, pathVars, ok := p.splitURL(r.URL.Raw)
	if !ok {
		b.Warn(fmt.Sprintf("request %q has no usable URL; skipped", naming.CleanText(it.Name, 80)))
		return
	}
	if !b.ValidPath(path) {
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
	summary := ""
	if strings.TrimSpace(desc) == "" {
		summary = it.Name
	}
	t := b.NewTool(method, path, summary, desc, tags, false)
	c := &restutil.Collector{}
	c.Hidden("summary", it.Name)
	c.Hidden("description", desc)

	varDesc := map[string]string{}
	for _, v := range r.URL.Variable {
		varDesc[v.Key] = string(v.Description)
	}
	str := json.RawMessage(`{"type":"string"}`)
	for _, name := range pathVars {
		b.AddParam(t, c, name, "path", true, varDesc[name], str)
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
		b.AddParam(t, c, q.Key, "query", false, string(q.Description), str)
	}
	for _, h := range r.Header {
		switch strings.ToLower(h.Key) {
		case "", "accept", "content-type", "user-agent", "content-length":
			continue
		}
		if h.Disabled {
			continue
		}
		b.AddParam(t, c, h.Key, "header", false, string(h.Description), str)
	}

	if r.Body != nil {
		p.body(t, c, r.Body)
	}
	t.Issues = c.Issues()
	b.AddOp(t, it.Name)
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
		addServer(p.b, u.Scheme+"://"+u.Host)
		resolved = u.EscapedPath()
		if unescaped, err := url.PathUnescape(resolved); err == nil {
			resolved = unescaped
		}
	case strings.HasPrefix(resolved, "{{"):
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
	for _, s := range p.b.M.Endpoints {
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
func (p *pmWalk) body(t *model.Tool, c *restutil.Collector, body *pmBody) {
	switch body.Mode {
	case "raw":
		var v any
		dec := json.NewDecoder(strings.NewReader(body.Raw))
		dec.UseNumber()
		if dec.Decode(&v) != nil {
			if body.Options.Raw.Language == "json" {
				c.Issue(model.IssueUnsupported, "the example JSON body doesn't parse, so its fields are unknown")
			} else if strings.TrimSpace(body.Raw) != "" {
				c.Issue(model.IssueUnsupported, "raw %s request body not supported", naming.CleanText(cmp.Or(body.Options.Raw.Language, "text"), 20))
			}
			return
		}
		t.HTTP.Body = &model.Body{ContentType: "application/json", Schema: utils.MustJSON(inferSchema(v, 1))}
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
			if d := naming.CleanText(string(f.Description), p.b.Opts.MaxDescription); d != "" {
				prop["description"] = d
			}
			props[f.Key] = prop
		}
		ct := restutil.CTForm
		if body.Mode == "formdata" {
			ct = restutil.CTMultipart
		}
		t.HTTP.Body = restutil.FormBody(c, ct, utils.MustJSON(map[string]any{"type": "object", "properties": props}), nil, false)
	case "":
	default:
		c.Issue(model.IssueUnsupported, "%s request body not supported", naming.CleanText(body.Mode, 20))
	}
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
