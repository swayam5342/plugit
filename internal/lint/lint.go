// Package lint checks an API's toolsets for quality and safety problems
// (SPEC §4, rules L001-L013). It is pure: it looks only at the parsed
// manifest and the config it is given.
package lint

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/curate"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/schema"
	"github.com/swayam5342/plugit/internal/tokens"
)

// Budget thresholds for L010 (also shown by the picker).
const (
	BudgetTools  = 40
	BudgetTokens = 8000
)

// Limits for the other rules.
const (
	maxDescription = naming.DefaultMaxDescription // L002
	maxDepth       = 8                            // L005
	maxProperties  = 50                           // L005
	maxParams      = 15                           // L006
)

// Severity is how bad a finding is.
type Severity int

// Severities, least severe first.
const (
	Info Severity = iota
	Warn
	Error
)

var severityNames = [...]string{Info: "info", Warn: "warn", Error: "error"}

func (s Severity) String() string {
	if s >= 0 && int(s) < len(severityNames) {
		return severityNames[s]
	}
	return fmt.Sprintf("Severity(%d)", int(s))
}

// MarshalJSON writes the severity by name.
func (s Severity) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// Finding is one problem.
type Finding struct {
	Rule     string      `json:"rule"`
	Severity Severity    `json:"severity"`
	Toolset  string      `json:"toolset,omitempty"` // "" for API-wide findings
	Tool     model.OpKey `json:"tool,omitempty"`
	Name     string      `json:"name,omitempty"` // the tool's final name
	Message  string      `json:"message"`
}

// Input is what lint looks at.
type Input struct {
	API    config.API
	Tools  []model.Tool // the manifest's tools, named with the prefix
	Hidden []string     // credential and fixed params, hidden from the model
}

// Summary counts findings by severity.
type Summary struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Infos    int `json:"infos"`
}

// Summarize counts fs.
func Summarize(fs []Finding) Summary {
	var s Summary
	for _, f := range fs {
		switch f.Severity {
		case Error:
			s.Errors++
		case Warn:
			s.Warnings++
		default:
			s.Infos++
		}
	}
	return s
}

// HasErrors reports whether any finding is an error.
func HasErrors(fs []Finding) bool { return Summarize(fs).Errors > 0 }

// Run lints the API and every toolset, most severe findings first.
func Run(in Input) []Finding {
	fs := apiFindings(in.API)
	for _, name := range slices.Sorted(maps.Keys(in.API.Toolsets)) {
		fs = append(fs, Toolset(in, name)...)
	}
	sortFindings(fs)
	return fs
}

// Toolset lints one toolset.
func Toolset(in Input, name string) []Finding {
	ts := in.API.Toolsets[name]
	sel := curate.Select(in.Tools, ts, in.API.Prefix)
	mode := in.API.PolicyMode()
	l := &linter{toolset: name}

	seen := map[string]model.Tool{}
	offered, budget := 0, 0
	for _, st := range sel.Tools {
		t := schema.Prepare(st.Tool, schema.Options{Fixed: in.Hidden})
		l.tool = t

		// L003: names after overrides.
		lower := strings.ToLower(t.Name)
		if other, dup := seen[lower]; dup {
			l.add("L003", Error, "name %q clashes with %s (%q); names are compared ignoring case", t.Name, other.Key, other.Name)
		} else {
			seen[lower] = t
		}
		if len(t.Name) > naming.MaxNameLen {
			l.add("L003", Error, "name %q is longer than %d characters with the prefix", t.Name, naming.MaxNameLen)
		}

		// L001, L002: descriptions.
		if t.Description == t.Method+" "+t.Path {
			l.add("L001", Warn, "no description in the spec; add one with `plugit edit`")
		}
		if o := ts.Tools[string(t.Key)]; utf8.RuneCountInString(o.Description) > maxDescription {
			l.add("L002", Warn, "description override is %d characters; it is cut to %d", utf8.RuneCountInString(o.Description), maxDescription)
		}

		// L004-L007 on what the model sees.
		var visible []model.Param
		for _, p := range t.Params {
			if !p.Hidden {
				visible = append(visible, p)
			}
		}
		if len(visible) > maxParams {
			l.add("L006", Warn, "%d parameters (more than %d makes the tool hard to call correctly)", len(visible), maxParams)
		}
		l.text("description", t.Description)
		for _, p := range visible {
			l.param(p)
		}
		for _, is := range t.Issues {
			switch is.Kind {
			case model.IssueHiddenText:
				l.add("L007", Error, "hidden text: %s", is.Detail)
			case model.IssueSchemaDegraded, model.IssueBodyNested:
				l.add("L012", Warn, "%s", is.Detail)
			case model.IssueUnsupported:
				l.add("L013", Info, "%s", is.Detail)
			}
		}

		// L008, L009: lifecycle and policy.
		if t.Deprecated {
			l.add("L008", Info, "the spec marks this operation deprecated")
		}
		if st.Override == policy.OverrideAllow && t.Safety == model.Destructive && mode != policy.Full {
			l.add("L009", Error, "destructive tool set to allow has no effect under policy mode %s (it stays %s); remove the override or use mode full", mode, policy.Check(t.Safety, mode, policy.None).Decision)
		}

		if policy.Lists(t.Safety, mode) && st.Override != policy.OverrideDeny {
			offered++
			budget += tokens.Tool(t)
		}
	}

	// L010: the whole toolset.
	if offered > BudgetTools || budget > BudgetTokens {
		l.tool = model.Tool{}
		l.add("L010", Warn, "%d tools, %s tokens offered (thresholds: %d tools, %s tokens); models pick tools less reliably from long lists", offered, tokens.Format(budget), BudgetTools, tokens.Format(BudgetTokens))
	}
	return l.out
}

// apiFindings are the API-wide rules.
func apiFindings(a config.API) []Finding {
	var out []Finding
	// L011: credentials over plain http to a remote host.
	if u, err := url.Parse(a.BaseURL); err == nil && u.Scheme == "http" && !loopback(u.Hostname()) && sendsSecrets(a) {
		out = append(out, Finding{Rule: "L011", Severity: Error,
			Message: fmt.Sprintf("base_url %s is plain http to a remote host, and credentials are sent: use https", u.Redacted())})
	}
	return out
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sendsSecrets(a config.API) bool {
	if a.Auth.Type != "" && a.Auth.Type != config.AuthNone {
		return true
	}
	for _, f := range a.Fixed {
		if strings.HasPrefix(f.Value, "env:") || strings.HasPrefix(f.Value, "keyring:") {
			return true
		}
	}
	return false
}

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

// walk visits a schema: it reports every title and description to text,
// and returns the nesting depth and the most properties on one object.
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

// ---- L007: text aimed at the model ----

var (
	// Instructions to the model rather than documentation for a caller.
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
	// Docs wording that is also how injected instructions read. Real specs
	// use it ("you must be an admin"), so it is a warning (decided
	// 2026-11-12 after measuring the GitHub spec).
	weakInstruction = regexp.MustCompile(`(?i)\byou\s+must\b`)
	link            = regexp.MustCompile(`(?i)\b(https?|ftp|file)://[^\s"'<>)\]]+`)

	// Things that look like credentials. Matches are never echoed.
	credentials = []*regexp.Regexp{
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                             // AWS access key
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`),                   // GitHub token
		regexp.MustCompile(`\b(sk|pk|rk)_(live|test)_[A-Za-z0-9]{16,}\b`),      // Stripe-style key
		regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),                 // Slack token
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.`), // JWT
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|secret|password|passwd)\s*[:=]\s*["']?[A-Za-z0-9_\-+/]{12,}`),
	}
)

// text checks one piece of text the model will read.
func (l *linter) text(where, s string) {
	if s == "" {
		return
	}
	if m := injection.FindString(s); m != "" {
		l.add("L007", Error, "%s reads like an instruction to the model: %q", where, quote(m))
	} else if m := weakInstruction.FindString(s); m != "" {
		l.add("L007", Warn, "%s is phrased as an instruction (%q); check it's meant for the caller", where, quote(m))
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

func sortFindings(fs []Finding) {
	slices.SortStableFunc(fs, func(a, b Finding) int {
		return cmp.Or(
			cmp.Compare(b.Severity, a.Severity),
			cmp.Compare(a.Toolset, b.Toolset),
			cmp.Compare(a.Tool, b.Tool),
			cmp.Compare(a.Rule, b.Rule),
			cmp.Compare(a.Message, b.Message),
		)
	})
}
