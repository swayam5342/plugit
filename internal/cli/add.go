package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/curate"
	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/lint"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/openapi"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/spec"
	"github.com/swayam5342/plugit/internal/tokens"
	"github.com/swayam5342/plugit/internal/tui"
)

type addSettings struct {
	name, spec   string
	baseURL      string
	prefix       string
	mode         policy.Mode
	allowPrivate bool
	timeout      time.Duration
	headers      []string // Name=env:VAR|keyring:svc/key
	fixed        []string // name=literal:V|env:VAR|keyring:svc/key
	// fromRun selects tools the way `plugit run` does (mode + filters),
	// without the picker.
	fromRun              bool
	tags, methods, paths []string
	maxTools             int
	yes                  bool // accept the spec's server as the base URL
}

// pickInput is what the picker works on.
type pickInput struct {
	api      string
	tools    []model.Tool // named with the prefix, fixed params hidden
	prefix   string
	mode     policy.Mode
	selected config.Toolset // initial selection
	lint     func(config.Toolset) []lint.Finding
}

type addDeps struct {
	paths   config.Paths
	out     io.Writer
	stderr  io.Writer
	confirm func(string) (bool, error) // nil: no terminal
	parse   func(context.Context, []byte, openapi.Options) (*model.Manifest, error)
	// pick runs the interactive picker; nil means there's no terminal.
	pick func(context.Context, pickInput) (config.Toolset, error)
	info BuildInfo
}

func newAddCmd(g *globalFlags, info BuildInfo) *cobra.Command {
	var s addSettings
	var mode string
	cmd := &cobra.Command{
		Use:   "add <name> <spec-url|file>",
		Short: "Save an API: fetch its spec, pin the base URL, pick tools",
		Long: `Fetch and parse a spec, confirm the base URL to pin, choose the tools in
the picker, and save apis/<name>/ (plugit.yaml, the spec snapshot and the
parsed manifest).

--from-run skips the picker and selects the way plugit run does, from
--mode, --tags, --methods and --paths, so a working plugit run command line
becomes a saved config by adding "add <name>" and --from-run.

Credentials are references only (--header Name=env:VAR, keyring:svc/key);
store a keyring value with plugit auth.`,
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
				paths:   p,
				out:     cmd.OutOrStdout(),
				stderr:  cmd.ErrOrStderr(),
				confirm: terminalConfirm(cmd),
				parse:   isolate.Parse,
				info:    info,
			}
			if d.confirm != nil {
				d.pick = runPicker
			}
			return addAPI(cmd.Context(), s, d)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&s.baseURL, "base-url", "", "API base URL to pin (default: the spec's server, after confirmation)")
	fl.StringVar(&s.prefix, "prefix", "", "tool name prefix, e.g. gh -> gh_listIssues")
	fl.StringVar(&mode, "mode", string(policy.ReadOnly), "policy mode: read-only, confirm-writes or full")
	fl.BoolVar(&s.allowPrivate, "allow-private", false, "allow loopback and private-network addresses")
	fl.DurationVar(&s.timeout, "timeout", httpx.DefaultTimeout, "per-request timeout")
	fl.StringArrayVar(&s.headers, "header", nil, "credential header Name=env:VAR or Name=keyring:service/key")
	fl.StringArrayVar(&s.fixed, "fixed", nil, "fixed hidden parameter name=literal:V, name=env:VAR or name=keyring:service/key")
	fl.BoolVar(&s.fromRun, "from-run", false, "select tools like plugit run (no picker)")
	fl.StringSliceVar(&s.tags, "tags", nil, "with --from-run: only operations with one of these tags")
	fl.StringSliceVar(&s.methods, "methods", nil, "with --from-run: only these HTTP methods")
	fl.StringSliceVar(&s.paths, "paths", nil, "with --from-run: only these path prefixes or globs")
	fl.IntVar(&s.maxTools, "max-tools", defaultMaxTools, "with --from-run: refuse more tools than this")
	fl.BoolVar(&s.yes, "yes", false, "pin the spec's server as the base URL without asking")
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

	sp, err := spec.New(spec.Options{AllowPrivate: s.allowPrivate}).Load(ctx, s.spec)
	if err != nil {
		return err
	}
	m, err := d.parse(ctx, sp.Bytes, openapi.Options{Prefix: s.prefix})
	if err != nil {
		return err
	}
	baseURL, err := pinBaseURL(s, m.Servers, d.confirm)
	if err != nil {
		return err
	}

	prepared := prepareAll(m.Tools, hiddenParams(auth, fixed))

	var ts config.Toolset
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
		draft := config.API{Name: s.name, BaseURL: baseURL, Prefix: s.prefix, Auth: auth, Fixed: fixed, Policy: config.Policy{Mode: string(s.mode)}}
		ts, err = d.pick(ctx, pickInput{api: s.name, tools: prepared, prefix: s.prefix, mode: s.mode, selected: initialSelection(prepared, s.mode),
			lint: pickerLint(draft, defaultToolset, m.Tools)})
		if errors.Is(err, tui.ErrCancelled) {
			_, err := fmt.Fprintln(d.out, "Cancelled; nothing saved.")
			return err
		}
		if err != nil {
			return err
		}
	}

	source := s.spec
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		if abs, err := filepath.Abs(source); err == nil {
			source = abs
		}
	} else {
		source = safeLocation(source) // never store a query (it may hold a key)
	}
	cfg := &config.API{
		Version:  config.CurrentVersion,
		Name:     s.name,
		Source:   config.Source{URL: source, SHA256: sp.SHA256},
		BaseURL:  baseURL,
		Prefix:   s.prefix,
		Fixed:    fixed,
		Network:  config.Network{AllowPrivate: s.allowPrivate, Timeout: s.timeout},
		Auth:     auth,
		Policy:   config.Policy{Mode: string(s.mode)},
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

	if err := saveAPIFiles(d.paths, cfg, sp.Bytes, m, parserID(d.info)); err != nil {
		return err
	}
	return printAddSummary(d.out, cfg, active)
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
	if err := config.WriteManifest(cachePath, config.CacheKey{SpecSHA256: sum, Prefix: cfg.Prefix, Parser: parser}, m); err != nil {
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
	next := "  plugit install " + cfg.Name + " --client claude-desktop   (or cursor, claude-code)\n"
	if cfg.Auth.Type != config.AuthNone && strings.HasPrefix(cfg.Auth.Secret, "keyring:") {
		next = "  plugit auth " + cfg.Name + "                          store the credential\n" + next
	}
	_, err := fmt.Fprintf(w, "Saved %s: %d tools, %s tokens, base URL %s%s.\n\nNext:\n%s  plugit try %s <tool>                       one test call\n",
		cfg.Name, len(tools), tokens.Format(tokens.Total(tools)), cfg.BaseURL, note, next, cfg.Name)
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

// pinBaseURL picks the base URL to pin: --base-url, or the spec's single
// server after the user confirms it (SPEC T3).
func pinBaseURL(s addSettings, servers []string, confirm func(string) (bool, error)) (string, error) {
	if s.baseURL != "" {
		u, err := validateBaseURL(s.baseURL, "--base-url")
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
		return "", fmt.Errorf("the spec's server is %s: confirm it with --yes, or pass --base-url", u)
	}
	ok, err := confirm(fmt.Sprintf("Pin base URL %s? Credentials will only ever be sent there.", u))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("base URL not confirmed: pass --base-url to pin a different one")
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
