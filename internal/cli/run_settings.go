package cli

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/secrets"
)

var errTooManyTools = errors.New("too many tools selected")

const defaultMaxTools = 40

type runSettings struct {
	spec, baseURL, prefix string
	headers, fixed        []string
	tags, methods, paths  []string
	maxTools              int
	timeout               time.Duration
	allowPrivate          bool
	allowInsecure         bool
	mode                  policy.Mode
	format                string // skip detection
	introspect            bool   // spec is a GraphQL endpoint
}

func resolveRunSettings(f runFlags, changed map[string]bool, lookup func(string) (string, bool)) (runSettings, error) {
	str := func(flagName, flagVal, envName, def string) string {
		if changed[flagName] || (flagName == "" && flagVal != "") {
			return flagVal
		}
		if v, ok := lookup(envName); ok && v != "" {
			return v
		}
		return def
	}
	list := func(flagName string, flagVal []string, envName string) []string {
		if changed[flagName] {
			return flagVal
		}
		if v, ok := lookup(envName); ok && v != "" {
			return splitList(v)
		}
		return nil
	}

	s := runSettings{
		spec:    str("", f.spec, "PLUGIT_SPEC", ""),
		baseURL: str("endpoint", f.baseURL, "PLUGIT_ENDPOINT", ""),
		prefix:  str("prefix", f.prefix, "PLUGIT_PREFIX", ""),
		headers: list("header", f.headers, "PLUGIT_HEADER"),
		fixed:   list("fixed", f.fixed, "PLUGIT_FIXED"),
		tags:    list("tags", f.tags, "PLUGIT_TAGS"),
		methods: list("methods", f.methods, "PLUGIT_METHODS"),
		paths:   list("paths", f.paths, "PLUGIT_PATHS"),
		format:  str("format", f.format, "PLUGIT_FORMAT", ""),
	}
	if s.spec == "" {
		return s, errors.New("no spec given: pass a file or URL, or set PLUGIT_SPEC")
	}

	mode, err := policy.ParseMode(str("mode", f.mode, "PLUGIT_MODE", string(policy.ReadOnly)))
	if err != nil {
		return s, err
	}
	s.mode = mode

	switch {
	case changed["max-tools"]:
		s.maxTools = f.maxTools
	default:
		s.maxTools = defaultMaxTools
		if v, ok := lookup("PLUGIT_MAX_TOOLS"); ok && v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return s, fmt.Errorf("PLUGIT_MAX_TOOLS: %q is not a number", v)
			}
			s.maxTools = n
		}
	}
	if s.maxTools < 1 {
		return s, errors.New("--max-tools must be at least 1")
	}

	switch {
	case changed["timeout"]:
		s.timeout = f.timeout
	default:
		s.timeout = httpx.DefaultTimeout
		if v, ok := lookup("PLUGIT_TIMEOUT"); ok && v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return s, fmt.Errorf("PLUGIT_TIMEOUT: %q is not a duration like 30s", v)
			}
			s.timeout = d
		}
	}
	if s.timeout <= 0 {
		return s, errors.New("--timeout must be positive")
	}

	switch {
	case changed["allow-private"]:
		s.allowPrivate = f.allowPrivate
	default:
		if v, ok := lookup("PLUGIT_ALLOW_PRIVATE"); ok && v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return s, fmt.Errorf("PLUGIT_ALLOW_PRIVATE: %q is not true or false", v)
			}
			s.allowPrivate = b
		}
	}

	switch {
	case changed["introspect"]:
		s.introspect = f.introspect
	default:
		if v, ok := lookup("PLUGIT_INTROSPECT"); ok && v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return s, fmt.Errorf("PLUGIT_INTROSPECT: %q is not true or false", v)
			}
			s.introspect = b
		}
	}

	switch {
	case changed["allow-insecure"]:
		s.allowInsecure = f.allowInsecure
	default:
		if v, ok := lookup("PLUGIT_ALLOW_INSECURE"); ok && v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return s, fmt.Errorf("PLUGIT_ALLOW_INSECURE: %q is not true or false", v)
			}
			s.allowInsecure = b
		}
	}
	return s, nil
}

func splitList(v string) []string {
	var out []string
	for p := range strings.SplitSeq(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type credFlag struct {
	name string
	ref  secrets.Ref
}

var headerNameRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// parseHeaderFlags parses --header Name=env:VAR|keyring:svc/key. Header
// values are credentials, and command lines are visible to other local
// users, so literal values are rejected (SPEC §2.4, T10).
func parseHeaderFlags(in []string) ([]credFlag, error) {
	out := make([]credFlag, 0, len(in))
	for _, h := range in {
		name, val, ok := strings.Cut(h, "=")
		if !ok || !headerNameRe.MatchString(name) {
			return nil, fmt.Errorf("--header: expected Name=env:VAR or Name=keyring:service/key, got an entry for %q", safeName(name))
		}
		ref, err := secrets.ParseSecretRef(val)
		if err != nil {
			return nil, fmt.Errorf("--header %s: %w", name, err)
		}
		out = append(out, credFlag{name: name, ref: ref})
	}
	return out, nil
}

// parseFixedFlags parses --fixed name=literal:V|env:VAR|keyring:svc/key.
func parseFixedFlags(in []string) ([]credFlag, error) {
	out := make([]credFlag, 0, len(in))
	for _, f := range in {
		name, val, ok := strings.Cut(f, "=")
		if !ok || name == "" || strings.ContainsAny(name, " \t\r\n") {
			return nil, fmt.Errorf("--fixed: expected name=literal:value, name=env:VAR or name=keyring:service/key, got an entry for %q", safeName(name))
		}
		ref, err := secrets.ParseRef(val)
		if err != nil {
			return nil, fmt.Errorf("--fixed %s: %w", name, err)
		}
		out = append(out, credFlag{name: name, ref: ref})
	}
	return out, nil
}

func safeName(s string) string {
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return s
}

// chooseBaseURL picks the pinned base URL. --endpoint always wins. Otherwise
// the spec must declare exactly one absolute server, and no credentials may
// be configured: a spec's servers[ ] is untrusted, so credentials only ever
// go to a URL the user typed (SPEC T3).
func chooseBaseURL(flag string, servers []string, credentials bool) (*url.URL, error) {
	if flag != "" {
		return validateBaseURL(flag, "--endpoint")
	}
	servers = collapseSchemeVariants(servers)
	switch {
	case len(servers) == 0:
		return nil, errors.New("the spec declares no server; pass --endpoint")
	case len(servers) > 1:
		return nil, fmt.Errorf("the spec declares %d servers (%s); pass --endpoint to choose one", len(servers), listServers(servers))
	}
	s := servers[0]
	u, err := validateBaseURL(s, "the spec's server")
	if err != nil {
		return nil, fmt.Errorf("%w; pass --endpoint", err)
	}
	if credentials {
		return nil, fmt.Errorf("credentials are configured, so pass --endpoint explicitly: the spec's server (%s) is not trusted to receive them", listServers(servers))
	}
	return u, nil
}

// collapseSchemeVariants drops exact duplicates and, when the remaining
// servers are the same host and path differing only in http vs https (a
// Swagger 2.0 `schemes: [https, http]`), keeps just the https one. That
// choice never sends anything to a new origin. Anything else is returned
// unchanged, so run still never guesses between real alternatives.
func collapseSchemeVariants(servers []string) []string {
	uniq := slices.Compact(slices.Sorted(slices.Values(servers)))
	if len(uniq) != 2 {
		return uniq
	}
	a, errA := url.Parse(uniq[0])
	b, errB := url.Parse(uniq[1])
	if errA != nil || errB != nil || a.Host != b.Host || a.Path != b.Path ||
		a.RawQuery != b.RawQuery || a.Fragment != b.Fragment || a.User.String() != b.User.String() {
		return uniq
	}
	switch {
	case a.Scheme == "http" && b.Scheme == "https":
		return uniq[1:]
	case a.Scheme == "https" && b.Scheme == "http":
		return uniq[:1]
	}
	return uniq
}

func validateBaseURL(raw, what string) (*url.URL, error) {
	shown := naming.CleanText(raw, 120)
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%s %q is not a valid URL", what, shown)
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("%s %q must be an absolute http(s) URL", what, shown)
	case u.Host == "":
		return nil, fmt.Errorf("%s %q has no host", what, shown)
	case u.User != nil:
		return nil, fmt.Errorf("%s must not contain credentials", what)
	case u.RawQuery != "" || u.Fragment != "":
		return nil, fmt.Errorf("%s %q must not have a query or fragment", what, shown)
	}
	return u, nil
}

func listServers(servers []string) string {
	shown := make([]string, 0, min(len(servers), 3))
	for _, s := range servers[:min(len(servers), 3)] {
		shown = append(shown, naming.CleanText(s, 80))
	}
	if len(servers) > 3 {
		shown = append(shown, "…")
	}
	return strings.Join(shown, ", ")
}

// selectTools keeps the tools the mode could run, narrowed by tags, methods
// and paths. A path filter with *, ? or [ is a glob, otherwise a prefix.
func selectTools(all []model.Tool, mode policy.Mode, tags, methods, paths []string) []model.Tool {
	var out []model.Tool
	for _, t := range all {
		if !policy.Lists(t.Safety, mode) {
			continue
		}
		if len(tags) > 0 && !slices.ContainsFunc(t.Tags, func(tag string) bool {
			return slices.ContainsFunc(tags, func(want string) bool { return strings.EqualFold(tag, want) })
		}) {
			continue
		}
		if len(methods) > 0 && !slices.ContainsFunc(methods, func(m string) bool { return strings.EqualFold(m, t.Key.Method()) }) {
			continue
		}
		if len(paths) > 0 && !slices.ContainsFunc(paths, func(p string) bool { return pathMatches(p, t.Key.Path()) }) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func pathMatches(pattern, p string) bool {
	if strings.ContainsAny(pattern, "*?[") {
		ok, err := path.Match(pattern, p)
		return err == nil && ok
	}
	return strings.HasPrefix(p, pattern)
}

// checkToolCount stops a huge spec from silently flooding the model's
// context (SPEC §2.4).
func checkToolCount(tools []model.Tool, maxTools int) error {
	if len(tools) == 0 {
		return errors.New("no tools selected: check --mode, --tags, --methods and --paths (read-only mode lists only GET, HEAD and OPTIONS operations)")
	}
	if len(tools) <= maxTools {
		return nil
	}
	counts := map[string]int{}
	for _, t := range tools {
		for _, tag := range t.Tags {
			counts[tag]++
		}
	}
	tags := make([]string, 0, len(counts))
	for tag := range counts {
		tags = append(tags, tag)
	}
	slices.SortFunc(tags, func(a, b string) int {
		return cmp.Or(cmp.Compare(counts[b], counts[a]), cmp.Compare(a, b))
	})
	var hint []string
	for _, tag := range tags[:min(len(tags), 5)] {
		hint = append(hint, fmt.Sprintf("%s (%d)", naming.CleanText(tag, 40), counts[tag]))
	}
	tagHint := ""
	if len(hint) > 0 {
		tagHint = " Most common tags: " + strings.Join(hint, ", ") + "."
	}
	return fmt.Errorf("%w: %d tools match, more than --max-tools %d. Narrow the selection with --tags, --methods or --paths, raise --max-tools, or curate a toolset with `plugit add`.%s",
		errTooManyTools, len(tools), maxTools, tagHint)
}
