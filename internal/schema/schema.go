// Package schema turns a tool's raw parameters and body into the argument
// set the model sees, and builds the MCP input schema from it (SPEC §3.5
// rules 3, 6 and 7). It is pure.
//
//   - Forbidden headers (auth, cookies, hop-by-hop, Host, X-Forwarded-*,
//     Content-Length, Content-Type, ...) are removed: the model never sets
//     them.
//   - Fixed parameters stay in Params (so the proxy can inject them) but are
//     Hidden and never appear in the input schema.
//   - Object JSON bodies are flattened into InBody parameters. A field that
//     collides becomes body_<name>; if that collides too, the whole body is
//     nested under one InBodyRoot parameter instead. Fields are never
//     dropped.
//   - Model-facing names are sanitized to [A-Za-z0-9_.-] (<= 64 bytes) and
//     unique case-insensitively; UpstreamName keeps the original.
package schema

import (
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/swayam5342/plugit/internal/model"
)

// Options configures Prepare.
type Options struct {
	// Fixed lists upstream parameter names whose values come from config.
	// Header names match case-insensitively; other locations exactly.
	Fixed []string
}

const maxPropName = 64

// forbiddenHeaders are never model-settable (lowercase).
var forbiddenHeaders = map[string]bool{
	"authorization": true, "cookie": true, "host": true, "forwarded": true,
	"content-length": true, "content-type": true, "transfer-encoding": true,
	"connection": true, "keep-alive": true, "te": true, "trailer": true, "upgrade": true,
}

// IsForbiddenHeader reports whether a header may never be a tool parameter.
func IsForbiddenHeader(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return forbiddenHeaders[n] || strings.HasPrefix(n, "proxy-") || strings.HasPrefix(n, "x-forwarded-")
}

// Prepare returns a copy of t with its final parameter list. t is not
// modified.
func Prepare(t model.Tool, opts Options) model.Tool {
	out := t
	out.Params = nil
	out.Issues = slices.Clone(t.Issues)
	if t.Body != nil {
		b := *t.Body
		out.Body = &b
	}

	fx := fixedSet(opts.Fixed)
	ns := newNamespace()

	// Path params claim names first, then query, then header; output keeps
	// the spec's order.
	kept := make([]model.Param, 0, len(t.Params))
	for _, p := range t.Params {
		if p.In == model.InHeader && IsForbiddenHeader(p.UpstreamName) {
			continue
		}
		kept = append(kept, p)
	}
	order := make([]int, len(kept))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return kept[order[a]].In < kept[order[b]].In })
	for _, i := range order {
		p := &kept[i]
		if fx.has(p.UpstreamName, p.In) {
			p.Hidden, p.Name = true, p.UpstreamName
			continue
		}
		p.Hidden = false
		p.Name = ns.claim(sanitize(p.UpstreamName), p.In)
	}
	out.Params = kept

	if t.Body != nil {
		out.Params = append(out.Params, bodyParams(&out, fx, ns)...)
	}
	return out
}

func bodyParams(t *model.Tool, fx fixed, ns *namespace) []model.Param {
	sch := decode(t.Body.Schema)
	if props, required, ok := flattenable(sch); ok {
		trial := ns.clone()
		var fields []model.Param
		collided := false
		for _, k := range sortedKeys(props) {
			f := model.Param{
				UpstreamName: k,
				In:           model.InBody,
				Required:     t.Body.Required && required[k],
				Schema:       mustJSON(props[k]),
			}
			if fx.has(k, model.InBody) {
				f.Hidden, f.Name = true, k
				fields = append(fields, f)
				continue
			}
			name, ok := trial.claimBodyField(sanitize(k))
			if !ok {
				collided = true
				break
			}
			f.Name = name
			fields = append(fields, f)
		}
		if !collided {
			*ns = *trial
			return fields
		}
		t.Issues = append(t.Issues, model.Issue{
			Kind:   model.IssueBodyNested,
			Detail: "request body nested under one parameter: flattening its fields would collide with other parameter names",
		})
	}

	name := ns.claimExact("body", "body_payload")
	return []model.Param{{
		Name:     name,
		In:       model.InBodyRoot,
		Required: t.Body.Required,
		Schema:   t.Body.Schema,
	}}
}

// flattenable reports whether a body schema is a plain object whose fields
// can become top-level parameters without losing anything.
func flattenable(s map[string]any) (map[string]any, map[string]bool, bool) {
	if typ, has := s["type"]; has && typ != "object" {
		return nil, nil, false
	}
	for _, k := range []string{"allOf", "oneOf", "anyOf", "not", "patternProperties", "if"} {
		if _, has := s[k]; has {
			return nil, nil, false
		}
	}
	if ap, has := s["additionalProperties"]; has && ap != false {
		return nil, nil, false
	}
	props, _ := s["properties"].(map[string]any)
	if len(props) == 0 {
		return nil, nil, false
	}
	required := map[string]bool{}
	if list, ok := s["required"].([]any); ok {
		for _, r := range list {
			if rs, ok := r.(string); ok {
				required[rs] = true
			}
		}
	}
	return props, required, true
}

// InputSchema builds the MCP input schema from t's visible parameters.
// Unknown arguments are rejected (additionalProperties: false).
func InputSchema(t model.Tool) json.RawMessage {
	props := map[string]any{}
	var required []string
	for _, p := range t.Params {
		if p.Hidden {
			continue
		}
		s := decode(p.Schema)
		if p.Desc != "" {
			if _, has := s["description"]; !has {
				s["description"] = p.Desc
			}
		}
		props[p.Name] = s
		if p.Required {
			required = append(required, p.Name)
		}
	}
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return mustJSON(out)
}

// ---- names ----

type namespace struct {
	used map[string]model.Location // lowercase name -> location that claimed it
}

func newNamespace() *namespace { return &namespace{used: map[string]model.Location{}} }

func (n *namespace) clone() *namespace {
	c := newNamespace()
	maps.Copy(c.used, n.used)
	return c
}

func (n *namespace) free(name string) bool {
	_, taken := n.used[strings.ToLower(name)]
	return !taken
}

func (n *namespace) take(name string, in model.Location) string {
	n.used[strings.ToLower(name)] = in
	return name
}

// claim picks a name for a path/query/header param. A clash with another
// location gets a location prefix ("query_id"); anything still clashing
// gets a numeric suffix.
func (n *namespace) claim(base string, in model.Location) string {
	if n.free(base) {
		return n.take(base, in)
	}
	if owner := n.used[strings.ToLower(base)]; owner != in {
		base = sanitize(in.String() + "_" + base)
		if n.free(base) {
			return n.take(base, in)
		}
	}
	return n.take(n.suffixed(base), in)
}

// claimBodyField tries the field name, then body_<name>.
func (n *namespace) claimBodyField(base string) (string, bool) {
	for _, cand := range []string{base, sanitize("body_" + base)} {
		if n.free(cand) {
			return n.take(cand, model.InBody), true
		}
	}
	return "", false
}

// claimExact tries each name in turn, then suffixes the last one.
func (n *namespace) claimExact(names ...string) string {
	for _, c := range names {
		if n.free(c) {
			return n.take(c, model.InBodyRoot)
		}
	}
	return n.take(n.suffixed(names[len(names)-1]), model.InBodyRoot)
}

func (n *namespace) suffixed(base string) string {
	for i := 2; ; i++ {
		s := "_" + strconv.Itoa(i)
		cand := truncate(base, maxPropName-len(s)) + s
		if n.free(cand) {
			return cand
		}
	}
}

// sanitize maps a parameter name onto [A-Za-z0-9_.-], collapsing and
// trimming '_', capped at 64 bytes.
func sanitize(s string) string {
	var b strings.Builder
	last := false
	for _, r := range s {
		ok := r == '-' || r == '.' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9')
		if !ok {
			if !last {
				b.WriteByte('_')
			}
			last = true
			continue
		}
		b.WriteRune(r)
		last = false
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		out = "param"
	}
	return truncate(out, maxPropName)
}

func truncate(s string, n int) string {
	if len(s) > n {
		s = strings.TrimRight(s[:n], "_")
	}
	return s
}

// ---- fixed params ----

type fixed struct {
	exact map[string]bool
	lower map[string]bool
}

func fixedSet(names []string) fixed {
	f := fixed{exact: map[string]bool{}, lower: map[string]bool{}}
	for _, n := range names {
		f.exact[n] = true
		f.lower[strings.ToLower(n)] = true
	}
	return f
}

func (f fixed) has(name string, in model.Location) bool {
	if in == model.InHeader {
		return f.lower[strings.ToLower(name)]
	}
	return f.exact[name]
}

// ---- JSON helpers ----

func decode(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	if len(raw) == 0 {
		return m
	}
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

// mustJSON encodes v, which always came from json.Unmarshal, so encoding
// cannot fail; a permissive {} is the fallback rather than a panic.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
