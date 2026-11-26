// Package lint checks an API's toolsets for quality and safety problems
// (SPEC 4, rules L001-L020). It is pure: it looks only at the parsed
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

const (
	maxDescription = naming.DefaultMaxDescription
	maxDepth       = 8
	maxProperties  = 50
	maxParams      = 15
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
	Toolset  string      `json:"toolset,omitempty"`
	Tool     model.OpKey `json:"tool,omitempty"`
	Name     string      `json:"name,omitempty"`
	Message  string      `json:"message"`
}

// Input is what lint looks at.
type Input struct {
	API    config.API
	Tools  []model.Tool
	Hidden []string
	// Warnings are the manifest's warnings. Run reads the GraphQL ones for
	// operations that became no tool (L013 for a subscription).
	Warnings []string
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

// Run lints the API and every toolset, most severe findings first. Ignored
// findings are left out (see RunCounting).
func Run(in Input) []Finding {
	fs, _ := RunCounting(in)
	return fs
}

// RunCounting is Run, and also says how many findings the config ignores.
func RunCounting(in Input) (fs []Finding, ignored int) {
	fs = apiFindings(in.API)
	for _, f := range warningFindings(in.Warnings) {
		if ignoredByAPI(in.API, f) {
			ignored++
			continue
		}
		fs = append(fs, f)
	}
	for _, name := range slices.Sorted(maps.Keys(in.API.Toolsets)) {
		kept, n := toolset(in, name)
		fs, ignored = append(fs, kept...), ignored+n
	}
	sortFindings(fs)
	return fs, ignored
}

// Toolset lints one toolset.
func Toolset(in Input, name string) []Finding {
	fs, _ := toolset(in, name)
	return fs
}

// ignored reports whether the config accepts f: lint.ignore hides a rule's
// warnings and infos everywhere, a tool's lint_ignore hides the rule for
// that tool, errors included.
func ignored(a config.API, ts config.Toolset, f Finding) bool {
	if ignoredByAPI(a, f) {
		return true
	}
	return f.Tool != "" && slices.Contains(ts.Tools[string(f.Tool)].LintIgnore, f.Rule)
}

func ignoredByAPI(a config.API, f Finding) bool {
	return f.Severity < Error && slices.Contains(a.Lint.Ignore, f.Rule)
}

// subscriptionWarning matches the manifest warning the GraphQL builder
// writes for a Subscription field, which becomes no tool.
var subscriptionWarning = regexp.MustCompile(`^subscription "([_A-Za-z][_0-9A-Za-z]*)": subscriptions aren't supported yet \(L013\)$`)

// warningFindings are the API-wide findings read from manifest warnings:
// L013 for each GraphQL subscription that has no tool.
func warningFindings(warnings []string) []Finding {
	var out []Finding
	for _, w := range warnings {
		if m := subscriptionWarning.FindStringSubmatch(w); m != nil {
			out = append(out, Finding{Rule: "L013", Severity: Info,
				Message: fmt.Sprintf("subscription %q has no tool: subscriptions aren't supported yet", m[1])})
		}
	}
	return out
}

// writeName returns the name and path writeLike checks: a GraphQL tool's root
// field (its safety comes from the operation type, so a write-like name only
// matters on a query), else the tool name without the API prefix and the
// REST path.
func writeName(t model.Tool, prefix string) (name, path string) {
	if t.GraphQL != nil {
		return t.GraphQL.Field, ""
	}
	return strings.TrimPrefix(t.Name, prefix+"_"), t.Key.Path()
}

// operation names what a tool calls in a message: the HTTP method for REST,
// the operation key ("query resetCache") otherwise.
func operation(t model.Tool) string {
	if m := t.Key.Method(); m != "" {
		return m
	}
	return string(t.Key)
}

func toolset(in Input, name string) (kept []Finding, n int) {
	ts := in.API.Toolsets[name]
	sel := curate.Select(in.Tools, ts, in.API.Prefix)
	mode := in.API.PolicyMode()
	l := &linter{toolset: name}

	seen := map[string]model.Tool{}
	squashed := map[string]model.Tool{}
	var unconfirmed []string
	offered, budget := 0, 0
	for _, st := range sel.Tools {
		t := schema.Prepare(st.Tool, schema.Options{Fixed: in.Hidden})
		l.tool = t

		lower := strings.ToLower(t.Name)
		if other, dup := seen[lower]; dup {
			l.add("L003", Error, "name %q clashes with %s (%q); names are compared ignoring case", t.Name, other.Key, other.Name)
		} else {
			seen[lower] = t
		}
		if len(t.Name) > naming.MaxNameLen {
			l.add("L003", Error, "name %q is longer than %d characters with the prefix", t.Name, naming.MaxNameLen)
		}
		if other, dup := squashed[squash(t.Name)]; dup && !strings.EqualFold(other.Name, t.Name) {
			l.add("L019", Warn, "name %q differs from %s (%q) only by case or separators; models mix such names up", t.Name, other.Key, other.Name)
		} else if !dup {
			squashed[squash(t.Name)] = t
		}

		if t.Description == t.Key.Method()+" "+t.Key.Path() {
			l.add("L001", Warn, "no description in the spec; add one with `plugit edit`")
		}
		if o := ts.Tools[string(t.Key)]; utf8.RuneCountInString(o.Description) > maxDescription {
			l.add("L002", Warn, "description override is %d characters; it is cut to %d", utf8.RuneCountInString(o.Description), maxDescription)
		}

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
			case model.IssueSelectionTruncated:
				l.add("L020", Warn, "%s; raise graphql.max_fields or list the fields you need in response_fields", is.Detail)
			}
		}

		if t.Deprecated {
			l.add("L008", Info, "the spec marks this operation deprecated")
		}
		if st.Override == policy.OverrideAllow && t.Safety == model.Destructive && mode != policy.Full {
			l.add("L009", Error, "destructive tool set to allow has no effect under policy mode %s (it stays %s); remove the override or use mode full", mode, policy.Check(t.Safety, mode, policy.None).Decision)
		}
		if w := writeLike(writeName(t, in.API.Prefix)); w != "" && t.Safety == model.ReadOnly && st.Override == policy.None {
			l.add("L015", Warn, "%s is treated as read-only but looks like it changes something (%q); if it does, set its policy to confirm or deny", operation(t), w)
		}
		if mode == policy.Full && t.Safety == model.Destructive && st.Override != policy.OverrideConfirm && st.Override != policy.OverrideDeny {
			unconfirmed = append(unconfirmed, t.Name)
		}

		if policy.Lists(t.Safety, mode) && st.Override != policy.OverrideDeny {
			offered++
			budget += tokens.Tool(t)
		}
	}

	if offered > BudgetTools || budget > BudgetTokens {
		l.tool = model.Tool{}
		l.add("L010", Warn, "%d tools, %s tokens offered (thresholds: %d tools, %s tokens); models pick tools less reliably from long lists", offered, tokens.Format(budget), BudgetTools, tokens.Format(BudgetTokens))
	}
	if n := len(unconfirmed); n > 0 {
		l.tool = model.Tool{}
		shown := unconfirmed[:min(n, 3)]
		more := ""
		if n > len(shown) {
			more = fmt.Sprintf(" and %d more", n-len(shown))
		}
		l.add("L018", Warn, "%d destructive tool(s) run without confirmation under policy mode full (%s%s); set their policy to confirm", n, strings.Join(shown, ", "), more)
	}
	for _, f := range l.out {
		if ignored(in.API, ts, f) {
			n++
			continue
		}
		kept = append(kept, f)
	}
	return kept, n
}

// apiFindings are the API-wide rules.
func apiFindings(a config.API) []Finding {
	var out []Finding
	if u, err := url.Parse(a.Endpoint); err == nil && u.Scheme == "http" && !loopback(u.Hostname()) && sendsSecrets(a) {
		f := Finding{Rule: "L011", Severity: Error,
			Message: fmt.Sprintf("base_url %s is plain http to a remote host, and credentials are sent: use https", u.Redacted())}
		if a.Network.AllowInsecure {
			f.Severity = Warn
			f.Message = fmt.Sprintf("base_url %s is plain http to a remote host, and credentials are sent unencrypted (network.allow_insecure is on): use https if the API offers it", u.Redacted())
		}
		out = append(out, f)
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
