package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/invoke"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/mcpserver"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
	"github.com/swayam5342/plugit/internal/redact"
	"github.com/swayam5342/plugit/internal/schema"
	"github.com/swayam5342/plugit/internal/secrets"
	"github.com/swayam5342/plugit/internal/spec"
)

type runFlags struct {
	spec, baseURL, prefix, mode string
	headers, fixed              []string
	tags, methods, paths        []string
	maxTools                    int
	timeout                     time.Duration
	allowPrivate, allowInsecure bool
	format                      string
	introspect                  bool
}

// parseFunc parses untrusted source bytes in the isolate child (isolate.Parse;
// tests substitute counting or failing fakes). format names the format to
// use, skipping detection; "" detects.
type parseFunc func(ctx context.Context, data []byte, format string, opts ingest.Options) (*model.Manifest, error)

type runDeps struct {
	transport mcp.Transport
	stderr    io.Writer
	resolver  *secrets.Resolver
	keyring   keyringSetter
	parse     parseFunc
	info      BuildInfo
}

func newRunCmd(info BuildInfo) *cobra.Command {
	var f runFlags
	cmd := &cobra.Command{
		Use:   "run <spec-url|file|graphql-endpoint>",
		Short: "Serve a spec as MCP tools on stdio, with no setup (read-only by default)",
		Long: `Serve a spec's operations as MCP tools on stdio, without saving anything.

Settings come from flags, then PLUGIT_* environment variables (PLUGIT_SPEC,
PLUGIT_ENDPOINT, PLUGIT_PREFIX, PLUGIT_MODE, PLUGIT_TAGS, PLUGIT_METHODS,
PLUGIT_PATHS, PLUGIT_MAX_TOOLS, PLUGIT_TIMEOUT, PLUGIT_ALLOW_PRIVATE,
PLUGIT_ALLOW_INSECURE, PLUGIT_FORMAT, PLUGIT_INTROSPECT,
PLUGIT_HEADER, PLUGIT_FIXED), then defaults.

--introspect treats the URL as a GraphQL endpoint: the standard
introspection query is POSTed there instead of reading a schema file. For
GraphQL the first --header is the credential, sent only to the endpoint.

Secrets in --header and --fixed must be env: or keyring: references; put the
real value in the MCP client's env block. When any credential is configured,
--endpoint is required: a spec's servers entry is never trusted with it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				f.spec = args[0]
			}
			changed := map[string]bool{}
			for _, n := range []string{"endpoint", "prefix", "mode", "header", "fixed", "tags", "methods", "paths", "max-tools", "timeout", "allow-private", "allow-insecure", "format", "introspect"} {
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
	fl.StringVar(&f.baseURL, "endpoint", "", "pinned API base URL (required when the spec has no single absolute server, or when credentials are set)")
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
	fl.StringVar(&f.format, "format", "", "source format, skipping detection: openapi, postman, graphql-sdl or graphql-introspection")
	fl.BoolVar(&f.introspect, "introspect", false, "GraphQL: the URL is the endpoint; fetch the schema there by introspection")
	fl.BoolVar(&f.allowInsecure, "allow-insecure", false, "accept what plugit otherwise refuses: credentials over plain http to a remote host, and spec paths with spaces, '..' or backslashes")
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
	userAgent := "plugit/" + d.info.withDefaults().Version
	opts := ingest.Options{Prefix: s.prefix, AllowUnsafePaths: s.allowInsecure}
	checkTransport := func(u *url.URL) error {
		if !credentials {
			return nil
		}
		if err := config.CheckCredentialTransport(u.String(), s.allowInsecure); err != nil {
			return fmt.Errorf("--endpoint %w; to send it over http anyway, pass --allow-insecure", err)
		}
		return nil
	}

	var (
		m       *model.Manifest
		baseURL *url.URL
		creds   runCredentials
	)
	if s.introspect {
		// The URL is the GraphQL endpoint: pin it, then send the introspection
		// query there with the credentials.
		if s.format != "" && s.format != graphql.FormatIntrospection {
			return fmt.Errorf("--introspect reads an introspection result; --format %s doesn't apply", naming.CleanText(s.format, 40))
		}
		if baseURL, err = introspectEndpoint(s.spec, s.baseURL); err != nil {
			return err
		}
		if err := checkTransport(baseURL); err != nil {
			return err
		}
		if creds, err = resolveRunCredentials(headers, fixedFlags, d.resolver, true); err != nil {
			return err
		}
		creds.fixed.userAgent = userAgent
		raw, err := introspect(ctx, baseURL, creds.cred, creds.fixed, s.allowPrivate)
		if err != nil {
			return err
		}
		if m, err = d.parse(ctx, raw, graphql.FormatIntrospection, opts); err != nil {
			return fmt.Errorf("introspection result from %s: %w", baseURL, err)
		}
	} else {
		sp, err := spec.New(spec.Options{AllowPrivate: s.allowPrivate}).Load(ctx, s.spec)
		if err != nil {
			return err
		}
		if m, err = d.parse(ctx, sp.Bytes, s.format, opts); err != nil {
			return err
		}
		if baseURL, err = chooseBaseURL(s.baseURL, m.Endpoints, credentials); err != nil {
			return err
		}
		if err := checkTransport(baseURL); err != nil {
			return err
		}
	}
	selected := selectTools(m.Tools, s.mode, s.tags, s.methods, s.paths)
	if err := checkToolCount(selected, s.maxTools); err != nil {
		return err
	}
	if !s.introspect {
		if creds, err = resolveRunCredentials(headers, fixedFlags, d.resolver, m.Protocol == model.GraphQL); err != nil {
			return err
		}
		creds.fixed.userAgent = userAgent
	}

	tools := make([]mcpserver.Tool, 0, len(selected))
	for _, t := range selected {
		tools = append(tools, mcpserver.Tool{Tool: schema.Prepare(t, schema.Options{Fixed: creds.hidden})})
	}
	logger := slog.New(slog.NewTextHandler(d.stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if m.Protocol == model.GraphQL {
		if err := checkFixedVariables(tools, creds.fixed, logger); err != nil {
			return err
		}
	}

	proto, err := resolveProto(m.Protocol, creds.fixed, baseURL)
	if err != nil {
		return err
	}
	red := redact.New(redact.Options{
		Secrets:           append(creds.cred.SecretValues(), creds.fixed.secretValues()...),
		SecretHeaders:     creds.hidden,
		SecretQueryParams: creds.hidden,
	})
	logger.Info("plugit run serving",
		"tools", len(tools), "mode", string(s.mode), "endpoint", baseURL.String(),
		"spec", red.String(safeLocation(s.spec)), "warnings", len(m.Warnings))

	return mcpserver.Serve(ctx, mcpserver.Config{
		Name:     "plugit-run",
		Version:  d.info.withDefaults().Version,
		Tools:    tools,
		Mode:     s.mode,
		Cred:     creds.cred,
		Endpoint: baseURL,
		Proto:    proto,
		Doer:     httpx.NewClient(httpx.Options{AllowPrivate: s.allowPrivate, Timeout: s.timeout}),
		Redactor: red,
		Logger:   logger,
	}, d.transport)
}

// runCredentials are run's resolved --header and --fixed values: the
// credential (GraphQL only), the fixed params, and the names to hide from
// the model and redact by name.
type runCredentials struct {
	cred   invoke.Credential
	fixed  resolvedFixed
	hidden []string
}

// resolveRunCredentials resolves --header and --fixed. For REST every
// header is a fixed header and --fixed goes to the query string, as before.
// For GraphQL the first header is the credential, as plugit add makes it:
// invoke attaches it only to the pinned endpoint's origin (T8), and an
// Authorization header (the bearer form SPEC 2.4 documents) can't be a
// GraphQL fixed header; --fixed values are variables.
func resolveRunCredentials(headers, fixedFlags []credFlag, res *secrets.Resolver, gql bool) (runCredentials, error) {
	var rc runCredentials
	for i, h := range headers {
		v, err := res.Resolve(h.ref)
		if err != nil {
			return rc, fmt.Errorf("--header %s: %w", h.name, err)
		}
		if gql && i == 0 {
			rc.cred = invoke.Credential{Type: string(config.AuthHeader), Header: h.name, Secret: v}
		} else {
			rc.fixed.items = append(rc.fixed.items, fixedItem{Name: h.name, In: "header", Value: v, Secret: true})
		}
		rc.hidden = append(rc.hidden, h.name)
	}
	for _, f := range fixedFlags {
		v, err := res.Resolve(f.ref)
		if err != nil {
			return rc, fmt.Errorf("--fixed %s: %w", f.name, err)
		}
		rc.fixed.items = append(rc.fixed.items, fixedItem{Name: f.name, In: "query", Value: v, Secret: f.ref.IsSecret()})
		rc.hidden = append(rc.hidden, f.name)
	}
	return rc, nil
}

// safeLocation drops userinfo, query and fragment from URL locations.
func safeLocation(loc string) string {
	u, err := url.Parse(loc)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return loc
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}
