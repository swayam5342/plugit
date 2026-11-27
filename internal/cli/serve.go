package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/invoke"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/mcpserver"
	"github.com/swayam5342/plugit/internal/oauth"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/redact"
	"github.com/swayam5342/plugit/internal/secrets"
	"github.com/swayam5342/plugit/internal/store"
)

const defaultToolset = "default"

type serveSettings struct {
	paths    config.Paths
	name     string
	toolset  string
	http     string
	newToken bool
	ready    func(string)
}

func newServeCmd(g *globalFlags, info BuildInfo) *cobra.Command {
	var toolset, httpAddr string
	var newToken bool
	cmd := &cobra.Command{
		Use:   "serve <name>",
		Short: "Serve a saved API as MCP tools on stdio (run by the MCP client)",
		Long: `Serve a saved API's active toolset as MCP tools on stdio.

MCP clients run this command; stdout carries JSON-RPC and logs go to stderr.
Credentials come from the keyring or environment variables named in
plugit.yaml, never from the client config.

--http ADDR serves Streamable HTTP instead, on loopback only (":8931" is
127.0.0.1:8931), at http://ADDR/mcp. Every request needs the header
"Authorization: Bearer <token>"; the token is generated once and kept in an
owner-only file (its path is printed, the token never is); --new-token
replaces it. Browser requests from non-loopback origins are refused.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			return serveAPI(cmd.Context(), serveSettings{paths: p, name: args[0], toolset: toolset, http: httpAddr, newToken: newToken}, runDeps{
				transport: &mcp.StdioTransport{},
				stderr:    cmd.ErrOrStderr(),
				resolver:  secrets.NewResolver(),
				keyring:   secrets.OSKeyring{},
				parse:     isolate.Parse,
				info:      info,
			})
		},
	}
	cmd.Flags().StringVar(&toolset, "toolset", defaultToolset, "toolset to serve")
	cmd.Flags().StringVar(&httpAddr, "http", "", "serve Streamable HTTP on this loopback address (e.g. :8931) instead of stdio")
	cmd.Flags().BoolVar(&newToken, "new-token", false, "with --http, replace the stored token")
	return cmd
}

// serveAPI loads a saved API and serves it. It returns before serving on any
// setup error. Nothing here writes to stdout: it carries JSON-RPC.
func serveAPI(ctx context.Context, s serveSettings, d runDeps) error {
	if s.http != "" {
		if _, err := mcpserver.ListenAddr(s.http); err != nil {
			return err
		}
	}
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
	logger.Info("plugit serve serving",
		"api", a.cfg.Name, "toolset", a.toolset, "selected", len(a.tools), "listed", listed, "mode", a.cfg.Policy.Mode,
		"endpoint", a.endpoint.String())
	var apiLimit *policy.Limiter
	if r, err := policy.ParseRate(a.cfg.Policy.RateLimit); err == nil {
		apiLimit = policy.NewLimiter(r, nil)
		logger.Info("rate limit", "rate_limit", r.String(), "scope", "this serve process")
	}
	callLog, closeLog := openCallLog(ctx, s.paths, a, logger)
	defer closeLog()
	cfg := mcpserver.Config{
		Log:      callLog,
		Limit:    apiLimit,
		Name:     "plugit-" + a.cfg.Name,
		Version:  d.info.withDefaults().Version,
		Tools:    a.tools,
		Mode:     a.cfg.PolicyMode(),
		Cred:     a.cred,
		Endpoint: a.endpoint,
		Proto:    a.proto,
		Doer:     a.doer,
		Redactor: a.red,
		Logger:   logger,
	}
	if s.http == "" {
		return mcpserver.Serve(ctx, cfg, d.transport)
	}
	addr, err := mcpserver.ListenAddr(s.http)
	if err != nil {
		return err
	}
	token, tokenPath, err := httpToken(s.paths, a.cfg.Name, s.newToken)
	if err != nil {
		return err
	}
	return mcpserver.ServeHTTP(ctx, cfg, addr, token, func(endpoint string) {
		logger.Info("plugit serve listening (Streamable HTTP)", "url", endpoint, "token_file", tokenPath,
			"header", "Authorization: Bearer <contents of token_file>")
		if s.ready != nil {
			s.ready(endpoint)
		}
	})
}

// httpToken returns the API's HTTP transport token, creating it (32
// random bytes, base64url) on first use or with rotate. The file is
// owner-only and written atomically.
func httpToken(p config.Paths, name string, rotate bool) (token, path string, err error) {
	path, err = p.HTTPToken(name)
	if err != nil {
		return "", "", err
	}
	if !rotate {
		if b, err := os.ReadFile(path); err == nil { // #nosec G304 -- plugit's own token file
			if t := strings.TrimSpace(string(b)); len(t) >= 32 && len(t) <= 256 {
				return t, path, nil
			}
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	if err := config.EnsureDir(filepath.Dir(path)); err != nil {
		return "", "", err
	}
	if err := config.WriteFileAtomic(path, []byte(token+"\n")); err != nil {
		return "", "", err
	}
	return token, path, nil
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

// resolvedFixed is the protocol-neutral result of resolving the configured
// fixed params: values are resolved, locations are the config strings
// ("query", "header", "cookie"), and a protocol maps them to its own Target.
type resolvedFixed struct {
	items     []fixedItem
	userAgent string
}

// fixedItem is one resolved fixed parameter. In is the P3 location string.
type fixedItem struct {
	Name, In, Value string
	Secret          bool
}

// secretValues lists the resolved fixed values that must be redacted wherever
// they appear (SPEC T11): every non-empty secret value, in config order,
// each followed by its bare token when it is written "<scheme> <token>"
// (redact.SchemeToken), as for a header credential. It is protocol-neutral,
// so redaction doesn't depend on which target was built.
func (fx resolvedFixed) secretValues() []string {
	var out []string
	for _, it := range fx.items {
		if it.Secret && it.Value != "" {
			out = append(out, it.Value)
			if tok := redact.SchemeToken(it.Value); tok != "" {
				out = append(out, tok)
			}
		}
	}
	return out
}

// buildCredential resolves the configured credential. It returns the names to
// hide from the model and to redact by name.
func buildCredential(cfg *config.API, res *secrets.Resolver, d runDeps) (invoke.Credential, []string, error) {
	var cred invoke.Credential
	var hidden []string
	a := cfg.Auth
	switch a.Type {
	case config.AuthNone:
	case config.AuthOAuth2:
		oc, err := oauthConfig(cfg, res)
		if err != nil {
			return cred, nil, err
		}
		store := keyringStore{get: res.Keyring, set: d.keyring, api: cfg.Name}
		src := oauth.NewSource(oc, oauthDoer(cfg), store, nil)
		cred = invoke.Credential{Type: "bearer", Token: src.Token, Unauthorized: src.Invalidate, Extra: []string{oc.ClientSecret}}
	default:
		ref, err := secrets.ParseSecretRef(a.Secret)
		if err != nil {
			return cred, nil, fmt.Errorf("auth.secret: %w", err)
		}
		v, err := res.Resolve(ref)
		if err != nil {
			return cred, nil, fmt.Errorf("auth.secret: %w", err)
		}
		cred = invoke.Credential{Type: string(a.Type), Header: a.Header, Param: a.Param, Secret: v}
		switch a.Type {
		case config.AuthHeader:
			hidden = append(hidden, a.Header)
		case config.AuthQuery:
			hidden = append(hidden, a.Param)
		}
	}
	return cred, hidden, nil
}

// buildFixedTarget resolves the configured fixed params. It returns the names
// to hide from the model and to redact by name.
func buildFixedTarget(cfg *config.API, res *secrets.Resolver) (resolvedFixed, []string, error) {
	var fx resolvedFixed
	var hidden []string
	for i, f := range cfg.Fixed {
		ref, err := secrets.ParseRef(f.Value)
		if err != nil {
			return fx, nil, fmt.Errorf("fixed[%d].value: %w", i, err)
		}
		v, err := res.Resolve(ref)
		if err != nil {
			return fx, nil, fmt.Errorf("fixed %s: %w", f.Name, err)
		}
		fx.items = append(fx.items, fixedItem{Name: f.Name, In: f.In, Value: v, Secret: ref.IsSecret()})
		hidden = append(hidden, f.Name)
	}
	return fx, hidden, nil
}
