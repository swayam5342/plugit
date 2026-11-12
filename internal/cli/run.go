package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/mcpserver"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/openapi"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/proxy"
	"github.com/swayam5342/plugit/internal/redact"
	"github.com/swayam5342/plugit/internal/schema"
	"github.com/swayam5342/plugit/internal/secrets"
	"github.com/swayam5342/plugit/internal/spec"
)

var errTooManyTools = errors.New("too many tools selected")

const defaultMaxTools = 40

// runFlags are the raw `plugit run` flags.
type runFlags struct {
	spec, baseURL, prefix, mode string
	headers, fixed              []string
	tags, methods, paths        []string
	maxTools                    int
	timeout                     time.Duration
	allowPrivate                bool
}

// runSettings are the effective settings: flags, then PLUGIT_* env vars,
// then defaults (SPEC §2.4).
type runSettings struct {
	spec, baseURL, prefix string
	headers, fixed        []string
	tags, methods, paths  []string
	maxTools              int
	timeout               time.Duration
	allowPrivate          bool
	mode                  policy.Mode
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
		baseURL: str("base-url", f.baseURL, "PLUGIT_BASE_URL", ""),
		prefix:  str("prefix", f.prefix, "PLUGIT_PREFIX", ""),
		headers: list("header", f.headers, "PLUGIT_HEADER"),
		fixed:   list("fixed", f.fixed, "PLUGIT_FIXED"),
		tags:    list("tags", f.tags, "PLUGIT_TAGS"),
		methods: list("methods", f.methods, "PLUGIT_METHODS"),
		paths:   list("paths", f.paths, "PLUGIT_PATHS"),
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

// credFlag is a parsed --header or --fixed entry. Its value is a reference,
// never a resolved secret.
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

// chooseBaseURL picks the pinned base URL. --base-url always wins. Otherwise
// the spec must declare exactly one absolute server, and no credentials may
// be configured: a spec's servers[] is untrusted, so credentials only ever
// go to a URL the user typed (SPEC T3).
func chooseBaseURL(flag string, servers []string, credentials bool) (*url.URL, error) {
	if flag != "" {
		return validateBaseURL(flag, "--base-url")
	}
	servers = collapseSchemeVariants(servers)
	switch {
	case len(servers) == 0:
		return nil, errors.New("the spec declares no server; pass --base-url")
	case len(servers) > 1:
		return nil, fmt.Errorf("the spec declares %d servers (%s); pass --base-url to choose one", len(servers), listServers(servers))
	}
	s := servers[0]
	u, err := validateBaseURL(s, "the spec's server")
	if err != nil {
		return nil, fmt.Errorf("%w; pass --base-url", err)
	}
	if credentials {
		return nil, fmt.Errorf("credentials are configured, so pass --base-url explicitly: the spec's server (%s) is not trusted to receive them", listServers(servers))
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
		if len(methods) > 0 && !slices.ContainsFunc(methods, func(m string) bool { return strings.EqualFold(m, t.Method) }) {
			continue
		}
		if len(paths) > 0 && !slices.ContainsFunc(paths, func(p string) bool { return pathMatches(p, t.Path) }) {
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

// ---- the command ----

type runDeps struct {
	transport mcp.Transport
	stderr    io.Writer
	resolver  *secrets.Resolver
	keyring   keyringSetter // stores a rotated OAuth refresh token; nil: not stored
	parse     func(ctx context.Context, data []byte, opts openapi.Options) (*model.Manifest, error)
	info      BuildInfo
}

func newRunCmd(info BuildInfo) *cobra.Command {
	var f runFlags
	cmd := &cobra.Command{
		Use:   "run <spec-url|file>",
		Short: "Serve a spec as MCP tools on stdio, with no setup (read-only by default)",
		Long: `Serve a spec's operations as MCP tools on stdio, without saving anything.

Settings come from flags, then PLUGIT_* environment variables (PLUGIT_SPEC,
PLUGIT_BASE_URL, PLUGIT_PREFIX, PLUGIT_MODE, PLUGIT_TAGS, PLUGIT_METHODS,
PLUGIT_PATHS, PLUGIT_MAX_TOOLS, PLUGIT_TIMEOUT, PLUGIT_ALLOW_PRIVATE,
PLUGIT_HEADER, PLUGIT_FIXED), then defaults.

Secrets in --header and --fixed must be env: or keyring: references; put the
real value in the MCP client's env block. When any credential is configured,
--base-url is required: a spec's servers entry is never trusted with it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				f.spec = args[0]
			}
			changed := map[string]bool{}
			for _, n := range []string{"base-url", "prefix", "mode", "header", "fixed", "tags", "methods", "paths", "max-tools", "timeout", "allow-private"} {
				changed[n] = cmd.Flags().Changed(n)
			}
			s, err := resolveRunSettings(f, changed, os.LookupEnv)
			if err != nil {
				return err
			}
			return runServe(cmd.Context(), s, runDeps{
				transport: &mcp.StdioTransport{},
				stderr:    cmd.ErrOrStderr(),
				resolver:  secrets.NewResolver(),
				keyring:   secrets.OSKeyring{},
				parse:     isolate.Parse,
				info:      info,
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.baseURL, "base-url", "", "pinned API base URL (required when the spec has no single absolute server, or when credentials are set)")
	fl.StringVar(&f.prefix, "prefix", "", "tool name prefix, e.g. pets -> pets_listPets")
	fl.StringVar(&f.mode, "mode", string(policy.ReadOnly), "policy mode: read-only, confirm-writes or full")
	fl.StringArrayVar(&f.headers, "header", nil, "credential header Name=env:VAR or Name=keyring:service/key (repeatable)")
	fl.StringArrayVar(&f.fixed, "fixed", nil, "fixed hidden parameter name=literal:V, name=env:VAR or name=keyring:service/key (repeatable)")
	fl.StringSliceVar(&f.tags, "tags", nil, "only operations with one of these tags")
	fl.StringSliceVar(&f.methods, "methods", nil, "only these HTTP methods")
	fl.StringSliceVar(&f.paths, "paths", nil, "only these path prefixes or globs, e.g. /pets or /pets/*")
	fl.IntVar(&f.maxTools, "max-tools", defaultMaxTools, "refuse to start if more tools than this are selected")
	fl.DurationVar(&f.timeout, "timeout", httpx.DefaultTimeout, "per-request timeout")
	fl.BoolVar(&f.allowPrivate, "allow-private", false, "allow loopback and private-network addresses (spec fetch and API calls)")
	return cmd
}

// runServe loads, selects and serves. It returns before serving on any
// setup error. Nothing here writes to stdout: it carries JSON-RPC.
func runServe(ctx context.Context, s runSettings, d runDeps) error {
	headers, err := parseHeaderFlags(s.headers)
	if err != nil {
		return err
	}
	fixedFlags, err := parseFixedFlags(s.fixed)
	if err != nil {
		return err
	}
	credentials := len(headers) > 0 || slices.ContainsFunc(fixedFlags, func(c credFlag) bool { return c.ref.IsSecret() })

	sp, err := spec.New(spec.Options{AllowPrivate: s.allowPrivate}).Load(ctx, s.spec)
	if err != nil {
		return err
	}
	m, err := d.parse(ctx, sp.Bytes, openapi.Options{Prefix: s.prefix})
	if err != nil {
		return err
	}
	baseURL, err := chooseBaseURL(s.baseURL, m.Servers, credentials)
	if err != nil {
		return err
	}
	selected := selectTools(m.Tools, s.mode, s.tags, s.methods, s.paths)
	if err := checkToolCount(selected, s.maxTools); err != nil {
		return err
	}

	// Resolve credentials only once everything else checks out.
	var fixed []proxy.Fixed
	var hidden []string
	for _, h := range headers {
		v, err := d.resolver.Resolve(h.ref)
		if err != nil {
			return fmt.Errorf("--header %s: %w", h.name, err)
		}
		fixed = append(fixed, proxy.Fixed{Name: h.name, In: model.InHeader, Value: v, Secret: true})
		hidden = append(hidden, h.name)
	}
	for _, f := range fixedFlags {
		v, err := d.resolver.Resolve(f.ref)
		if err != nil {
			return fmt.Errorf("--fixed %s: %w", f.name, err)
		}
		fixed = append(fixed, proxy.Fixed{Name: f.name, In: model.InQuery, Value: v, Secret: f.ref.IsSecret()})
		hidden = append(hidden, f.name)
	}

	tools := make([]mcpserver.Tool, 0, len(selected))
	for _, t := range selected {
		tools = append(tools, mcpserver.Tool{Tool: schema.Prepare(t, schema.Options{Fixed: hidden})})
	}

	target := proxy.Target{BaseURL: baseURL, Fixed: fixed, UserAgent: "plugit/" + d.info.withDefaults().Version}
	red := redact.New(redact.Options{
		Secrets:           target.SecretValues(),
		SecretHeaders:     hidden,
		SecretQueryParams: hidden,
	})
	logger := slog.New(slog.NewTextHandler(d.stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("plugit run serving",
		"tools", len(tools), "mode", string(s.mode), "base_url", baseURL.String(), // validated: no userinfo or query
		"spec", red.String(safeLocation(s.spec)), "warnings", len(m.Warnings))

	return mcpserver.Serve(ctx, mcpserver.Config{
		Name:     "plugit-run",
		Version:  d.info.withDefaults().Version,
		Tools:    tools,
		Mode:     s.mode,
		Target:   target,
		Doer:     httpx.NewClient(httpx.Options{AllowPrivate: s.allowPrivate, Timeout: s.timeout}),
		Redactor: red,
		Logger:   logger,
	}, d.transport)
}

// safeLocation drops userinfo, query and fragment from URL locations.
func safeLocation(loc string) string {
	u, err := url.Parse(loc)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return loc
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}
