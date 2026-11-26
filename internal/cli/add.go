package cli

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/curate"
	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/lint"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
	"github.com/swayam5342/plugit/internal/secrets"
	"github.com/swayam5342/plugit/internal/spec"
	"github.com/swayam5342/plugit/internal/tokens"
	"github.com/swayam5342/plugit/internal/tui"
)

type addSettings struct {
	name, spec           string
	baseURL              string
	prefix               string
	mode                 policy.Mode
	allowPrivate         bool
	allowInsecure        bool
	timeout              time.Duration
	headers              []string
	fixed                []string
	fromRun              bool
	tags, methods, paths []string
	maxTools             int
	yes                  bool
	format               string // --format: skip detection
	introspect           bool   // --introspect: spec is the GraphQL endpoint
}

type pickResult struct {
	toolset config.Toolset
	mode    policy.Mode
}

type pickInput struct {
	api      string
	tools    []model.Tool
	prefix   string
	mode     policy.Mode
	selected config.Toolset
	lint     func(config.Toolset) []lint.Finding
	proto    model.Protocol
}

type addDeps struct {
	paths   config.Paths
	out     io.Writer
	stderr  io.Writer
	confirm func(string) (bool, error)
	parse   parseFunc
	pick    func(context.Context, pickInput) (pickResult, error)
	info    BuildInfo
	// resolver resolves the credential for --introspect (nil: the real
	// environment and keyring).
	resolver *secrets.Resolver
}

func newAddCmd(g *globalFlags, info BuildInfo) *cobra.Command {
	var s addSettings
	var mode string
	cmd := &cobra.Command{
		Use:   "add <name> <spec-url|file|graphql-endpoint>",
		Short: "Save an API: fetch its spec, pin the base URL, pick tools",
		Long: `Fetch and parse a spec, confirm the base URL to pin, choose the tools in
the picker, and save apis/<name>/ (plugit.yaml, the spec snapshot and the
parsed manifest).

--from-run skips the picker and selects the way plugit run does, from
--mode, --tags, --methods and --paths, so a working plugit run command line
becomes a saved config by adding "add <name>" and --from-run.

Credentials are references only (--header Name=env:VAR, keyring:svc/key);
store a keyring value with plugit auth.

--format NAME skips format detection (openapi, postman, graphql-sdl,
graphql-introspection). --introspect treats the URL as a GraphQL endpoint:
it is pinned, and the standard introspection query is POSTed there (with
the credential) instead of reading a schema file.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			s.name, s.spec = args[0], args[1]
			if s.mode, err = policy.ParseMode(mode); err != nil {
				return err
			}
			d := addDeps{
				paths:    p,
				out:      cmd.OutOrStdout(),
				stderr:   cmd.ErrOrStderr(),
				confirm:  terminalConfirm(cmd),
				parse:    isolate.Parse,
				info:     info,
				resolver: secrets.NewResolver(),
			}
			if d.confirm != nil {
				d.pick = runPicker
			}
			return addAPI(cmd.Context(), s, d)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&s.baseURL, "endpoint", "", "API base URL to pin (default: the spec's server, after confirmation)")
	fl.StringVar(&s.prefix, "prefix", "", "tool name prefix, e.g. gh -> gh_listIssues")
	fl.StringVar(&mode, "mode", string(policy.ReadOnly), "policy mode: read-only, confirm-writes or full")
	fl.BoolVar(&s.allowPrivate, "allow-private", false, "allow loopback and private-network addresses")
	fl.BoolVar(&s.allowInsecure, "allow-insecure", false, "accept what plugit otherwise refuses: credentials over plain http to a remote host, and spec paths with spaces, '..' or backslashes")
	fl.DurationVar(&s.timeout, "timeout", httpx.DefaultTimeout, "per-request timeout")
	fl.StringArrayVar(&s.headers, "header", nil, "credential header Name=env:VAR or Name=keyring:service/key")
	fl.StringArrayVar(&s.fixed, "fixed", nil, "fixed hidden parameter name=literal:V, name=env:VAR or name=keyring:service/key")
	fl.BoolVar(&s.fromRun, "from-run", false, "select tools like plugit run (no picker)")
	fl.StringSliceVar(&s.tags, "tags", nil, "with --from-run: only operations with one of these tags")
	fl.StringSliceVar(&s.methods, "methods", nil, "with --from-run: only these HTTP methods")
	fl.StringSliceVar(&s.paths, "paths", nil, "with --from-run: only these path prefixes or globs")
	fl.IntVar(&s.maxTools, "max-tools", defaultMaxTools, "with --from-run: refuse more tools than this")
	fl.BoolVar(&s.yes, "yes", false, "pin the spec's server as the base URL without asking")
	fl.StringVar(&s.format, "format", "", "source format, skipping detection: openapi, postman, graphql-sdl or graphql-introspection")
	fl.BoolVar(&s.introspect, "introspect", false, "GraphQL: the URL is the endpoint; pin it and fetch the schema by introspection")
	return cmd
}

func addAPI(ctx context.Context, s addSettings, d addDeps) error {
	if err := config.ValidateAPIName(s.name); err != nil {
		return err
	}
	cfgPath, err := d.paths.APIConfig(s.name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(cfgPath); err == nil {
		return fmt.Errorf("API %s already exists (%s): change it with `plugit edit %s`, or `plugit remove %s` first", s.name, cfgPath, s.name, s.name)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if !s.fromRun && d.pick == nil {
		return errors.New("the tool picker needs a terminal: use --from-run with --tags, --methods or --paths to select tools without it")
	}
	auth, fixed, err := credentialConfig(s.headers, s.fixed)
	if err != nil {
		return err
	}

	src, err := loadAddSource(ctx, s, d, auth, fixed)
	if err != nil {
		return err
	}
	m := src.m
	baseURL := src.endpoint
	if baseURL == "" {
		if baseURL, err = pinBaseURL(s, m.Endpoints, d.confirm); err != nil {
			return err
		}
	}

	prepared := prepareAll(m.Tools, hiddenParams(auth, fixed))

	var ts config.Toolset
	mode := s.mode
	if s.fromRun {
		sel := selectTools(prepared, s.mode, s.tags, s.methods, s.paths)
		if err := checkToolCount(sel, s.maxTools); err != nil {
			return err
		}
		ts.Tools = map[string]config.ToolOverride{}
		for _, t := range sel {
			ts.Tools[string(t.Key)] = config.ToolOverride{}
		}
	} else {
		draft := config.API{Name: s.name, Endpoint: baseURL, Prefix: s.prefix, Auth: auth, Fixed: fixed, Policy: config.Policy{Mode: string(s.mode)}}
		res, err := d.pick(ctx, pickInput{api: s.name, tools: prepared, prefix: s.prefix, mode: s.mode, selected: initialSelection(prepared, s.mode),
			lint: pickerLint(draft, defaultToolset, m.Tools), proto: m.Protocol})
		if errors.Is(err, tui.ErrCancelled) {
			_, err := fmt.Fprintln(d.out, "Cancelled; nothing saved.")
			return err
		}
		if err != nil {
			return err
		}
		ts = res.toolset
		if res.mode != "" {
			mode = res.mode
		}
	}

	source := s.spec
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		if abs, err := filepath.Abs(source); err == nil {
			source = abs
		}
	} else {
		source = safeLocation(source)
	}
	cfg := &config.API{
		Version:  config.CurrentVersion,
		Name:     s.name,
		Protocol: string(cmp.Or(m.Protocol, model.REST)),
		Source:   config.Source{URL: source, Format: m.Format, SHA256: src.sha256},
		Endpoint: baseURL,
		Prefix:   s.prefix,
		Fixed:    fixed,
		Network:  config.Network{AllowPrivate: s.allowPrivate, AllowInsecure: s.allowInsecure, Timeout: s.timeout},
		Auth:     auth,
		Policy:   config.Policy{Mode: string(mode)},
		Toolsets: map[string]config.Toolset{defaultToolset: ts},
	}
	if cfg.Network.Timeout == 0 {
		cfg.Network.Timeout = config.DefaultTimeout
	}
	cfg.Network.MaxResponseBytes = config.DefaultMaxResponseBytes
	cfg.Log.Args = config.LogArgsRedacted
	active, err := curate.Apply(prepared, ts, s.prefix)
	if err != nil {
		return err
	}
	if len(active.Tools) == 0 {
		return errors.New("no tools selected; nothing saved")
	}
	if _, err := config.MarshalAPI(cfg); err != nil {
		return err
	}
	if m, err = compileResponseFields(ctx, d.parse, cfg, src.snapshot, m); err != nil {
		return err
	}

	if err := saveAPIFiles(d.paths, cfg, src.snapshot, m, parserID(d.info)); err != nil {
		return err
	}
	return printAddSummary(d.out, cfg, active)
}

// addSource is a parsed source for add: the snapshot to save and its
// sha256, the manifest, and the endpoint when it is already pinned
// (--introspect).
type addSource struct {
	snapshot []byte
	sha256   string
	m        *model.Manifest
	endpoint string
}

// loadAddSource loads and parses (in the isolate child) the source add
// saves. A spec file or URL is loaded with the spec loader and parsed with
// --format, or detected. With --introspect the URL is the GraphQL endpoint:
// it is pinned first, then the introspection query is sent there with the
// configured credential, and the raw result is parsed as
// graphql-introspection.
func loadAddSource(ctx context.Context, s addSettings, d addDeps, auth config.Auth, fixed []config.Fixed) (addSource, error) {
	opts := ingest.Options{Prefix: s.prefix, AllowUnsafePaths: s.allowInsecure}
	if !s.introspect {
		sp, err := spec.New(spec.Options{AllowPrivate: s.allowPrivate}).Load(ctx, s.spec)
		if err != nil {
			return addSource{}, err
		}
		m, err := d.parse(ctx, sp.Bytes, s.format, opts)
		if err != nil {
			return addSource{}, err
		}
		return addSource{snapshot: sp.Bytes, sha256: sp.SHA256, m: m}, nil
	}

	if s.format != "" && s.format != graphql.FormatIntrospection {
		return addSource{}, fmt.Errorf("--introspect reads an introspection result; --format %s doesn't apply", naming.CleanText(s.format, 40))
	}
	endpoint, err := introspectEndpoint(s.spec, s.baseURL)
	if err != nil {
		return addSource{}, err
	}
	draft := &config.API{Name: s.name, Endpoint: endpoint.String(), Auth: auth}
	for _, f := range fixed {
		if f.In == "header" { // only headers go with the introspection request
			draft.Fixed = append(draft.Fixed, f)
		}
	}
	if draft.Auth.Type != config.AuthNone || len(draft.Fixed) > 0 {
		if err := config.CheckCredentialTransport(endpoint.String(), s.allowInsecure); err != nil {
			return addSource{}, fmt.Errorf("the --introspect URL %w; to send it over http anyway, pass --allow-insecure", err)
		}
	}
	res := d.resolver
	if res == nil {
		res = secrets.NewResolver()
	}
	cred, _, err := buildCredential(draft, res, runDeps{})
	if err != nil {
		return addSource{}, err
	}
	fx, _, err := buildFixedTarget(draft, res)
	if err != nil {
		return addSource{}, err
	}
	fx.userAgent = "plugit/" + d.info.withDefaults().Version
	raw, err := introspect(ctx, endpoint, cred, fx, s.allowPrivate)
	if err != nil {
		return addSource{}, err
	}
	m, err := d.parse(ctx, raw, graphql.FormatIntrospection, opts)
	if err != nil {
		return addSource{}, fmt.Errorf("introspection result from %s: %w", endpoint, err)
	}
	sum := sha256.Sum256(raw)
	return addSource{snapshot: raw, sha256: hex.EncodeToString(sum[:]), m: m, endpoint: endpoint.String()}, nil
}

// compileResponseFields re-parses a GraphQL snapshot in the isolate child
// with cfg's toolset response_fields, so the saved manifest carries their
// compiled documents (and matches cfg's cache key). Any that don't compile
// is an error naming the toolset, tool and path. A manifest of any other
// protocol, or one with nothing to compile and no compiled documents left
// over, is returned as is.
func compileResponseFields(ctx context.Context, parse parseFunc, cfg *config.API, snapshot []byte, m *model.Manifest) (*model.Manifest, error) {
	if m.Protocol != model.GraphQL {
		return m, nil
	}
	stale := m.GraphQL != nil && (len(m.GraphQL.OverrideDocs) > 0 || len(m.GraphQL.OverrideErrors) > 0)
	if cfg.GraphQLResponseFields() == nil && !stale {
		return m, nil
	}
	m, err := parse(ctx, snapshot, cfg.Source.Format, parseOptions(cfg))
	if err != nil {
		return nil, err
	}
	if m.GraphQL != nil && len(m.GraphQL.OverrideErrors) > 0 {
		var problems []string
		for _, ts := range slices.Sorted(maps.Keys(m.GraphQL.OverrideErrors)) {
			byKey := m.GraphQL.OverrideErrors[ts]
			for _, key := range slices.Sorted(maps.Keys(byKey)) {
				problems = append(problems, fmt.Sprintf("toolset %q, tool %q: %s", ts, key, byKey[key]))
			}
		}
		return nil, fmt.Errorf("response_fields don't compile against the schema (%s); nothing saved", strings.Join(problems, "; "))
	}
	return m, nil
}

// pickerLint lints a picker selection as toolset ts of api. tools are the
// manifest's tools as parsed (lint prepares them itself).
func pickerLint(api config.API, ts string, tools []model.Tool) func(config.Toolset) []lint.Finding {
	hidden := hiddenParams(api.Auth, api.Fixed)
	return func(sel config.Toolset) []lint.Finding {
		a := api
		a.Toolsets = map[string]config.Toolset{ts: sel}
		return lint.Toolset(lint.Input{API: a, Tools: tools, Hidden: hidden}, ts)
	}
}

// saveAPIFiles writes the snapshot and manifest first and plugit.yaml last:
// its presence is what makes the API exist.
func saveAPIFiles(p config.Paths, cfg *config.API, snapshot []byte, m *model.Manifest, parser string) error {
	dir, err := p.APIDir(cfg.Name)
	if err != nil {
		return err
	}
	if err := config.EnsureDir(dir); err != nil {
		return err
	}
	snapPath, _ := p.SpecSnapshot(cfg.Name)
	if err := config.WriteFileAtomic(snapPath, snapshot); err != nil {
		return err
	}
	_, sum, err := config.ReadSnapshot(snapPath)
	if err != nil {
		return err
	}
	cachePath, _ := p.Manifest(cfg.Name)
	if err := config.WriteManifest(cachePath, cfg.CacheKey(sum, parser), m); err != nil {
		return err
	}
	return config.SaveAPI(p, cfg)
}

func printAddSummary(w io.Writer, cfg *config.API, active curate.Result) error {
	tools := make([]model.Tool, 0, len(active.Tools))
	writes := 0
	for _, st := range active.Tools {
		tools = append(tools, st.Tool)
		if st.Tool.Safety != model.ReadOnly {
			writes++
		}
	}
	note := ""
	if writes > 0 && cfg.PolicyMode() == policy.ReadOnly {
		note = fmt.Sprintf(" (%d write tools are saved but not offered in read-only mode)", writes)
	}
	next := "  plugit install " + cfg.Name + "                         add it to Claude, Codex, Cursor, VS Code...\n"
	if cfg.Auth.Type != config.AuthNone && strings.HasPrefix(cfg.Auth.Secret, "keyring:") {
		next = "  plugit auth " + cfg.Name + "                          store the credential\n" + next
	}
	_, err := fmt.Fprintf(w, "Saved %s: %d tools, %s tokens, base URL %s%s.\n\nNext:\n%s  plugit try %s <tool>                       one test call\n",
		cfg.Name, len(tools), tokens.Format(tokens.Total(tools)), cfg.Endpoint, note, next, cfg.Name)
	return err
}

// initialSelection preselects everything the mode lists when that fits the
// default budget, and nothing otherwise (a 1,000-op spec starts empty).
func initialSelection(tools []model.Tool, mode policy.Mode) config.Toolset {
	ts := config.Toolset{Tools: map[string]config.ToolOverride{}}
	var listable []model.Tool
	for _, t := range tools {
		if policy.Lists(t.Safety, mode) {
			listable = append(listable, t)
		}
	}
	if len(listable) > defaultMaxTools {
		return ts
	}
	for _, t := range listable {
		ts.Tools[string(t.Key)] = config.ToolOverride{}
	}
	return ts
}

// pinBaseURL picks the base URL to pin: --endpoint, or the spec's single
// server after the user confirms it (SPEC T3).
func pinBaseURL(s addSettings, servers []string, confirm func(string) (bool, error)) (string, error) {
	if s.baseURL != "" {
		u, err := validateBaseURL(s.baseURL, "--endpoint")
		if err != nil {
			return "", err
		}
		return u.String(), nil
	}
	u, err := chooseBaseURL("", servers, false)
	if err != nil {
		return "", err
	}
	if s.yes {
		return u.String(), nil
	}
	if confirm == nil {
		return "", fmt.Errorf("the spec's server is %s: confirm it with --yes, or pass --endpoint", u)
	}
	ok, err := confirm(fmt.Sprintf("Pin endpoint %s? Credentials will only ever be sent there.", u))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("endpoint not confirmed: pass --endpoint to pin a different one")
	}
	return u.String(), nil
}

// credentialConfig turns --header and --fixed flags into config: the first
// header becomes the auth credential (SPEC §9 item 4), any others fixed
// headers.
func credentialConfig(headerFlags, fixedFlags []string) (config.Auth, []config.Fixed, error) {
	auth := config.Auth{Type: config.AuthNone}
	headers, err := parseHeaderFlags(headerFlags)
	if err != nil {
		return auth, nil, err
	}
	fixedRefs, err := parseFixedFlags(fixedFlags)
	if err != nil {
		return auth, nil, err
	}
	var fixed []config.Fixed
	for i, h := range headers {
		if i == 0 {
			auth = config.Auth{Type: config.AuthHeader, Header: h.name, Secret: h.ref.String()}
			continue
		}
		fixed = append(fixed, config.Fixed{Name: h.name, In: "header", Value: h.ref.String()})
	}
	for _, f := range fixedRefs {
		fixed = append(fixed, config.Fixed{Name: f.name, Value: f.ref.String()})
	}
	return auth, fixed, nil
}
