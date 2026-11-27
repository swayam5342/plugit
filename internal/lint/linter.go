package lint

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/swayam5342/plugit/internal/model"
)

type linter struct {
	toolset string
	tool    model.Tool
	out     []Finding
	seen    map[string]bool
}

func (l *linter) add(rule string, sev Severity, format string, a ...any) {
	f := Finding{Rule: rule, Severity: sev, Toolset: l.toolset, Tool: l.tool.Key, Name: l.tool.Name, Message: fmt.Sprintf(format, a...)}
	key := rule + "\x00" + string(f.Tool) + "\x00" + f.Message
	if l.seen == nil {
		l.seen = map[string]bool{}
	}
	if !l.seen[key] {
		l.seen[key] = true
		l.out = append(l.out, f)
	}
}

func (l *linter) param(p model.Param) {
	where := fmt.Sprintf("parameter %q", p.Name)
	l.text(where+" description", p.Desc)
	if p.Bind.In != model.InPath && credentialName.MatchString(strings.Join(words(p.Bind.Upstream), "_")) {
		l.add("L016", Warn, "%s looks like a credential the model would have to supply; make it a fixed value so it stays hidden", where)
	}
	switch riskyParam(p.Bind.Upstream) {
	case "command":
		l.add("L017", Warn, "%s looks like a command or query the API will run: whatever the model writes there is executed upstream", where)
	case "target":
		l.add("L017", Info, "%s looks like a URL, host or file the API will reach for: the model chooses where", where)
	}
	var s any
	if json.Unmarshal(p.Schema, &s) != nil {
		return
	}
	if m, ok := s.(map[string]any); ok && len(m) > 0 && !typed(m) {
		l.add("L004", Warn, "%s has no type", where)
	}
	depth, wide := walk(s, 1, func(field, text string) { l.text(where+" schema "+field, text) })
	if depth > maxDepth {
		l.add("L005", Warn, "%s schema is %d levels deep (more than %d)", where, depth, maxDepth)
	}
	if wide > maxProperties {
		l.add("L005", Warn, "%s schema has an object with %d properties (more than %d)", where, wide, maxProperties)
	}
}

func typed(m map[string]any) bool {
	for _, k := range []string{"type", "anyOf", "oneOf", "allOf", "enum", "const", "$ref", "not"} {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

// walk visits a schema: it reports every title, description and enum, const
// or default string to text, and returns the nesting depth and the most
// properties on one object.
func walk(s any, depth int, text func(field, s string)) (maxD, wide int) {
	m, ok := s.(map[string]any)
	if !ok {
		return depth, 0
	}
	maxD = depth
	for _, f := range []string{"title", "description"} {
		if v, ok := m[f].(string); ok {
			text(f, v)
		}
	}
	for _, f := range []string{"enum", "const", "default"} {
		if v, ok := m[f]; ok {
			valueStrings(v, 1, func(s string) { text(f+" value", s) })
		}
	}
	child := func(c any, d int) {
		cd, cw := walk(c, d, text)
		maxD, wide = max(maxD, cd), max(wide, cw)
	}
	if props, ok := m["properties"].(map[string]any); ok {
		wide = len(props)
		for _, k := range slices.Sorted(maps.Keys(props)) {
			child(props[k], depth+1)
		}
	}
	for _, k := range []string{"items", "additionalProperties"} {
		if c, ok := m[k]; ok {
			child(c, depth+1)
		}
	}
	for _, k := range []string{"anyOf", "oneOf", "allOf"} {
		if list, ok := m[k].([]any); ok {
			for _, c := range list {
				child(c, depth)
			}
		}
	}
	return maxD, wide
}

func valueStrings(v any, depth int, fn func(string)) {
	if depth > maxDepth {
		return
	}
	switch x := v.(type) {
	case string:
		fn(x)
	case []any:
		for _, e := range x {
			valueStrings(e, depth+1, fn)
		}
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(x)) {
			fn(k)
			valueStrings(x[k], depth+1, fn)
		}
	}
}

var (
	injection = regexp.MustCompile(`(?i)\b(` +
		`(ignore|disregard|forget|override)\s+(all\s+|any\s+|the\s+|your\s+)*(previous|prior|above|earlier|preceding|system)\b` +
		`|exfiltrat\w*` +
		`|(send|upload|post|forward|copy|leak)\s+(all\s+|the\s+|any\s+)*(user'?s?|their|conversation|chat|session|history|secrets?|credentials?|tokens?|files)\b[^.]{0,40}?\bto\s+(https?://|\S+@)` +
		`|(^|[\s"'(\[])(system|assistant|developer)\s*:` +
		`|new\s+instructions?\b` +
		`|do\s+not\s+(tell|inform|mention|reveal|show)\b[^.]{0,30}\buser\b` +
		`|without\s+(telling|asking|informing)\s+the\s+user` +
		`|(act|behave)\s+as\s+(an?\s+)?(admin|administrator|root|developer|system)` +
		`|developer\s+mode` +
		`|</?\s*(system|instructions?|prompt)\s*>` +
		`)`)
	weakInstruction = regexp.MustCompile(`(?i)\byou\s+must\b`)
	link            = regexp.MustCompile(`(?i)\b(https?|ftp|file)://[^\s"'<>)\]]+`)

	credentials = []*regexp.Regexp{
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`),
		regexp.MustCompile(`\b(sk|pk|rk)_(live|test)_[A-Za-z0-9]{16,}\b`),
		regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.`),
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|secret|password|passwd)\s*[:=]\s*["']?[A-Za-z0-9_\-+/]{12,}`),
	}
)

// text checks one piece of text the model will read.
func (l *linter) text(where, s string) {
	if s == "" {
		return
	}
	if m, how := instruction(s); m != "" {
		if how != "" {
			how = " (" + how + ")"
		}
		l.add("L007", Error, "%s reads like an instruction to the model%s: %q", where, how, quote(m))
	} else if m := weakInstruction.FindString(s); m != "" {
		l.add("L007", Warn, "%s is phrased as an instruction (%q); check it's meant for the caller", where, quote(m))
	}
	if m := localSecrets.FindString(s); m != "" {
		l.add("L007", Warn, "%s tells the reader to use a local secret file (%q)", where, quote(m))
	}
	if w := mixedScript(s); w != "" {
		l.add("L007", Warn, "%s mixes alphabets in one word (%q): lookalike letters can hide a word from review", where, quote(w))
	}
	for _, dec := range encodedText(s) {
		if m, _ := instruction(dec); m != "" {
			l.add("L007", Error, "%s hides an instruction to the model in encoded text: %q", where, quote(m))
		} else {
			l.add("L014", Warn, "%s contains base64 or hex that decodes to readable text (%q); a model can decode it", where, quote(dec))
		}
	}
	if m := link.FindString(s); m != "" {
		host := "a link"
		if u, err := url.Parse(m); err == nil && u.Host != "" {
			host = u.Host
		}
		l.add("L007", Warn, "%s embeds a URL (%s); a model may follow it", where, quote(host))
	}
	for _, re := range credentials {
		if re.MatchString(s) {
			l.add("L007", Error, "%s contains what looks like a credential", where)
			break
		}
	}
}

func quote(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 40 {
		s = string(r[:39]) + "…"
	}
	return s
}
