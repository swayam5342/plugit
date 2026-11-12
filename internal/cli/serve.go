package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/curate"
	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/mcpserver"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/openapi"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/proxy"
	"github.com/swayam5342/plugit/internal/redact"
	"github.com/swayam5342/plugit/internal/schema"
	"github.com/swayam5342/plugit/internal/secrets"
	"github.com/swayam5342/plugit/internal/store"
)

const defaultToolset = "default"

type serveSettings struct {
	paths   config.Paths
	name    string
	toolset string // "" means default
}

func newServeCmd(g *globalFlags, info BuildInfo) *cobra.Command {
	var toolset string
	cmd := &cobra.Command{
		Use:   "serve <name>",
		Short: "Serve a saved API as MCP tools on stdio (run by the MCP client)",
		Long: `Serve a saved API's active toolset as MCP tools on stdio.

MCP clients run this command; stdout carries JSON-RPC and logs go to stderr.
Credentials come from the keyring or environment variables named in
plugit.yaml, never from the client config.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			return serveAPI(cmd.Context(), serveSettings{paths: p, name: args[0], toolset: toolset}, runDeps{
				transport: &mcp.StdioTransport{},
				stderr:    cmd.ErrOrStderr(),
				resolver:  secrets.NewResolver(),
				parse:     isolate.Parse,
				info:      info,
			})
		},
	}
	cmd.Flags().StringVar(&toolset, "toolset", defaultToolset, "toolset to serve")
	return cmd
}

// serveAPI loads a saved API and serves it. It returns before serving on any
// setup error. Nothing here writes to stdout: it carries JSON-RPC.
func serveAPI(ctx context.Context, s serveSettings, d runDeps) error {
	logger := slog.New(slog.NewTextHandler(d.stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	a, err := loadActive(ctx, s, d, logger)
	if err != nil {
		return err
	}
	listed := 0
	for _, t := range a.tools {
		if policy.Lists(t.Tool.Safety, a.cfg.PolicyMode()) && t.Override != policy.OverrideDeny {
			listed++
		}
	}
	// "selected" counts the toolset; "listed" is what the mode lets clients see.
	logger.Info("plugit serve serving",
		"api", a.cfg.Name, "toolset", a.toolset, "selected", len(a.tools), "listed", listed, "mode", a.cfg.Policy.Mode,
		"base_url", a.target.BaseURL.String()) // validated: no userinfo or query
	// Rate limits are per serve process: each MCP client has its own budget.
	var apiLimit *policy.Limiter
	if r, err := policy.ParseRate(a.cfg.Policy.RateLimit); err == nil {
		apiLimit = policy.NewLimiter(r, nil)
		logger.Info("rate limit", "rate_limit", r.String(), "scope", "this serve process")
	}
	callLog, closeLog := openCallLog(ctx, s.paths, a, logger)
	defer closeLog()
	return mcpserver.Serve(ctx, mcpserver.Config{
		Log:      callLog,
		Limit:    apiLimit,
		Name:     "plugit-" + a.cfg.Name,
		Version:  d.info.withDefaults().Version,
		Tools:    a.tools,
		Mode:     a.cfg.PolicyMode(),
		Target:   a.target,
		Doer:     a.doer,
		Redactor: a.red,
		Logger:   logger,
	}, d.transport)
}

// openCallLog opens the call log, prunes the API's calls older than
// log.retention and starts a background recorder. The log is best effort:
// if it can't be opened, serve still runs and says so on stderr.
func openCallLog(ctx context.Context, p config.Paths, a *active, logger *slog.Logger) (*mcpserver.CallLog, func()) {
	db, err := store.Open(ctx, p.DB())
	if err != nil {
		logger.Warn("call log unavailable; calls won't be recorded", "err", err)
		return nil, func() {}
	}
	if n, err := db.Prune(ctx, a.cfg.Name, time.Now().Add(-a.cfg.RetentionPeriod())); err != nil {
		logger.Warn("pruning the call log failed", "err", err)
	} else if n > 0 {
		logger.Info("pruned old calls from the call log", "removed", n, "retention", a.cfg.RetentionPeriod().String())
	}
	rec := store.NewRecorder(db, 256, func(err error) { logger.Warn("recording a call failed", "err", err) })
	return &mcpserver.CallLog{
		Record: rec.Record, API: a.cfg.Name, Toolset: a.toolset,
		Args: a.cfg.Log.Args, Preview: a.cfg.Log.ResponsePreview,
	}, func() {
		rec.Close()
		if n := rec.Dropped(); n > 0 {
			logger.Warn("calls not recorded because the call log fell behind", "dropped", n)
		}
		_ = db.Close()
	}
}

// active is a loaded API, ready to call: its active toolset with resolved
// credentials.
type active struct {
	cfg     *config.API
	toolset string
	tools   []mcpserver.Tool
	target  proxy.Target
	red     *redact.Redactor
	doer    httpx.Doer
}

// loadActive loads plugit.yaml, the manifest (from cache when fresh), the
// toolset and the credentials. Shared by serve and try.
func loadActive(ctx context.Context, s serveSettings, d runDeps, logger *slog.Logger) (*active, error) {
	cfg, err := config.LoadAPI(s.paths, s.name)
	if err != nil {
		return nil, err
	}
	tsName := cmp.Or(s.toolset, defaultToolset)
	ts, ok := cfg.Toolsets[tsName]
	if !ok {
		return nil, fmt.Errorf("API %s has no toolset %q (it has: %s)", cfg.Name, tsName,
			strings.Join(slices.Sorted(maps.Keys(cfg.Toolsets)), ", "))
	}

	m, err := loadManifest(ctx, s.paths, cfg, d, logger)
	if err != nil {
		return nil, err
	}
	sel, err := curate.Apply(m.Tools, ts, cfg.Prefix)
	if err != nil {
		return nil, fmt.Errorf("toolset %q: %w", tsName, err)
	}
	if len(sel.Missing) > 0 {
		keys := make([]string, 0, len(sel.Missing))
		for _, k := range sel.Missing {
			keys = append(keys, string(k))
		}
		logger.Warn("toolset lists operations the spec no longer has", "toolset", tsName, "missing", strings.Join(keys, ", "))
	}
	if len(sel.Tools) == 0 {
		return nil, fmt.Errorf("toolset %q selects no tools: add operations with `plugit edit %s`", tsName, cfg.Name)
	}

	baseURL, err := url.Parse(cfg.BaseURL) // validated by config.LoadAPI
	if err != nil {
		return nil, fmt.Errorf("base_url: %w", err)
	}
	target, hidden, err := buildTarget(cfg, baseURL, d.resolver, "plugit/"+d.info.withDefaults().Version)
	if err != nil {
		return nil, err
	}
	tools := make([]mcpserver.Tool, 0, len(sel.Tools))
	for _, st := range sel.Tools {
		mt := mcpserver.Tool{Tool: schema.Prepare(st.Tool, schema.Options{Fixed: hidden}), Override: st.Override}
		if r, err := policy.ParseRate(ts.Tools[string(st.Tool.Key)].RateLimit); err == nil {
			mt.Limit = policy.NewLimiter(r, nil)
		}
		tools = append(tools, mt)
	}
	return &active{
		cfg:     cfg,
		toolset: tsName,
		tools:   tools,
		target:  target,
		red: redact.New(redact.Options{
			Secrets:           target.SecretValues(),
			SecretHeaders:     hidden,
			SecretQueryParams: hidden,
		}),
		doer: httpx.NewClient(httpx.Options{
			AllowPrivate:     cfg.Network.AllowPrivate,
			Timeout:          cfg.Network.Timeout,
			MaxResponseBytes: cfg.Network.MaxResponseBytes,
		}),
	}, nil
}

// loadManifest returns the parsed spec snapshot, from manifest.json when it
// was built from this snapshot, prefix and plugit build. Otherwise it parses
// the snapshot once in the isolate child and rewrites the cache (SPEC §3.6).
func loadManifest(ctx context.Context, p config.Paths, cfg *config.API, d runDeps, logger *slog.Logger) (*model.Manifest, error) {
	snapPath, err := p.SpecSnapshot(cfg.Name)
	if err != nil {
		return nil, err
	}
	data, sum, err := config.ReadSnapshot(snapPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("API %s has no spec snapshot (%s); run `plugit sync %s`", cfg.Name, snapPath, cfg.Name)
	}
	if err != nil {
		return nil, err
	}
	cachePath, err := p.Manifest(cfg.Name)
	if err != nil {
		return nil, err
	}
	key := config.CacheKey{SpecSHA256: sum, Prefix: cfg.Prefix, Parser: parserID(d.info)}
	m, err := config.ReadManifest(cachePath, key)
	if err == nil {
		return m, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		logger.Info("re-parsing the spec snapshot", "reason", err.Error())
	}
	m, err = d.parse(ctx, data, openapi.Options{Prefix: cfg.Prefix})
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", snapPath, err)
	}
	if err := config.WriteManifest(cachePath, key, m); err != nil {
		logger.Warn("could not write the manifest cache; the next start will parse again", "error", err.Error())
	}
	return m, nil
}

// parserID identifies the plugit build that parses, so an upgrade
// invalidates the cache. Development builds share a version string, so the
// executable's size and modification time are included too.
func parserID(info BuildInfo) string {
	info = info.withDefaults()
	id := info.Version + "+" + info.Commit
	if exe, err := os.Executable(); err == nil {
		if fi, err := os.Stat(exe); err == nil {
			id += "+" + strconv.FormatInt(fi.Size(), 10) + "." + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
		}
	}
	return id
}

// buildTarget resolves the credential and fixed params. It returns the
// names to hide from the model and to redact by name.
func buildTarget(cfg *config.API, baseURL *url.URL, res *secrets.Resolver, userAgent string) (proxy.Target, []string, error) {
	tg := proxy.Target{BaseURL: baseURL, UserAgent: userAgent}
	var hidden []string

	a := cfg.Auth
	if a.Type != config.AuthNone {
		ref, err := secrets.ParseSecretRef(a.Secret)
		if err != nil {
			return tg, nil, fmt.Errorf("auth.secret: %w", err)
		}
		v, err := res.Resolve(ref)
		if err != nil {
			return tg, nil, fmt.Errorf("auth.secret: %w", err)
		}
		tg.Auth = proxy.Auth{Type: proxy.AuthType(a.Type), Header: a.Header, Param: a.Param, Secret: v}
		switch a.Type {
		case config.AuthHeader:
			hidden = append(hidden, a.Header)
		case config.AuthQuery:
			hidden = append(hidden, a.Param)
		}
	}
	for i, f := range cfg.Fixed {
		ref, err := secrets.ParseRef(f.Value)
		if err != nil {
			return tg, nil, fmt.Errorf("fixed[%d].value: %w", i, err)
		}
		v, err := res.Resolve(ref)
		if err != nil {
			return tg, nil, fmt.Errorf("fixed %s: %w", f.Name, err)
		}
		in := model.InQuery
		if f.In == "header" {
			in = model.InHeader
		}
		tg.Fixed = append(tg.Fixed, proxy.Fixed{Name: f.Name, In: in, Value: v, Secret: ref.IsSecret()})
		hidden = append(hidden, f.Name)
	}
	return tg, hidden, nil
}
