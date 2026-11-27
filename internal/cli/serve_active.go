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

	"github.com/swayam5342/plugit/internal/invoke"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/curate"
	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/jsonpick"
	"github.com/swayam5342/plugit/internal/mcpserver"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/protocol"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
	"github.com/swayam5342/plugit/internal/redact"
	"github.com/swayam5342/plugit/internal/schema"
)

type active struct {
	cfg      *config.API
	toolset  string
	tools    []mcpserver.Tool
	cred     invoke.Credential
	endpoint *url.URL
	proto    protocol.Protocol
	red      *redact.Redactor
	doer     httpx.Doer
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

	baseURL, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("endpoint: %w", err)
	}
	cred, credHidden, err := buildCredential(cfg, d.resolver, d)
	if err != nil {
		return nil, err
	}
	fx, fixedHidden, err := buildFixedTarget(cfg, d.resolver)
	if err != nil {
		return nil, err
	}
	fx.userAgent = "plugit/" + d.info.withDefaults().Version
	hidden := append(credHidden, fixedHidden...)
	proto, err := resolveProto(m.Protocol, fx, baseURL)
	if err != nil {
		return nil, err
	}
	gql := m.Protocol == model.GraphQL
	docs, err := overrideDocs(m, tsName, sel.Tools, cfg.Name)
	if err != nil {
		return nil, err
	}
	tools := make([]mcpserver.Tool, 0, len(sel.Tools))
	for _, st := range sel.Tools {
		t := st.Tool
		if doc, ok := docs[t.Key]; ok && t.GraphQL != nil {
			t.GraphQL = overrideOp(*t.GraphQL, doc, m.GraphQL.OverrideSelections[tsName], t.Key)
		}
		mt := mcpserver.Tool{Tool: schema.Prepare(t, schema.Options{Fixed: hidden}), Override: st.Override}
		if r, err := policy.ParseRate(ts.Tools[string(t.Key)].RateLimit); err == nil {
			mt.Limit = policy.NewLimiter(r, nil)
		}
		// GraphQL response_fields are compiled into the document (the server
		// returns only those fields), so no picker filters the result.
		if p, err := jsonpick.Parse(ts.Tools[string(t.Key)].ResponseFields); err == nil && !gql {
			mt.Fields = p
		}
		tools = append(tools, mt)
	}
	if gql {
		if err := checkFixedVariables(tools, fx, logger); err != nil {
			return nil, err
		}
	}
	return &active{
		cfg:      cfg,
		toolset:  tsName,
		tools:    tools,
		cred:     cred,
		endpoint: baseURL,
		proto:    proto,
		red: redact.New(redact.Options{
			Secrets:           append(cred.SecretValues(), fx.secretValues()...),
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

// overrideDocs returns the toolset's compiled response_fields documents for
// the selected tools (GraphQL; nil for any other manifest). A selected tool
// whose response_fields didn't compile against the schema is a load error
// naming the toolset, the tool and the path (SPEC P7): the server would
// reject the document anyway.
func overrideDocs(m *model.Manifest, toolset string, sel []curate.Selected, api string) (map[model.OpKey]string, error) {
	if m.GraphQL == nil {
		return nil, nil
	}
	var problems []string
	for _, st := range sel {
		if msg, ok := m.GraphQL.OverrideErrors[toolset][string(st.Tool.Key)]; ok {
			problems = append(problems, fmt.Sprintf("%s: %s", st.Tool.Key, msg))
		}
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return nil, fmt.Errorf("toolset %q: response_fields that don't compile against the schema (%s); fix them with `plugit edit %s --toolset %s`",
			toolset, strings.Join(problems, "; "), api, toolset)
	}
	docs := map[model.OpKey]string{}
	for key, doc := range m.GraphQL.OverrideDocs[toolset] {
		docs[model.OpKey(key)] = doc
	}
	return docs, nil
}

// checkFixedVariables checks the GraphQL fixed variables against the
// selected tools: one whose variable is an input object or a list is refused
// (a fixed value is a single scalar), and a tool that doesn't declare some of
// them gets one warning naming them, since they aren't sent with it.
func checkFixedVariables(tools []mcpserver.Tool, fx resolvedFixed, logger *slog.Logger) error {
	for _, mt := range tools {
		var missing []string
		for _, it := range fx.items {
			if it.In == "header" {
				continue
			}
			i := slices.IndexFunc(mt.Tool.Params, func(p model.Param) bool {
				return p.Bind.In == model.InVariable && p.Bind.Upstream == it.Name
			})
			if i < 0 {
				missing = append(missing, it.Name)
				continue
			}
			if typ := graphql.SchemaType(mt.Tool.Params[i].Schema); typ == "object" || typ == "array" {
				return fmt.Errorf("fixed variable %q: tool %s declares it as an %s, and a fixed value can only set a scalar; remove it from fixed",
					it.Name, mt.Tool.Key, typ)
			}
		}
		if len(missing) > 0 {
			logger.Warn("tool does not declare these fixed variables, so they are not sent with it",
				"tool", string(mt.Tool.Key), "fixed_variables", strings.Join(missing, ", "))
		}
	}
	return nil
}

// parseOptions is the parse configuration plugit.yaml implies: the REST
// subset, and for GraphQL the selection caps and every toolset's
// response_fields, which the child compiles into override documents.
func parseOptions(cfg *config.API) ingest.Options {
	o := ingest.Options{Prefix: cfg.Prefix, AllowUnsafePaths: cfg.Network.AllowInsecure}
	if model.Protocol(cfg.Protocol) == model.GraphQL {
		g := cfg.GraphQLSettings()
		o.SelectionDepth, o.MaxFields = g.SelectionDepth, g.MaxFields
		o.ResponseFields = cfg.GraphQLResponseFields()
	}
	return o
}

// loadManifest returns the parsed spec snapshot, from manifest.json when it
// was built from this snapshot, config and plugit build. Otherwise it parses
// the snapshot once in the isolate child (with source.format, if recorded)
// and rewrites the cache (SPEC §3.6).
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
	key := cfg.CacheKey(sum, parserID(d.info))
	m, err := config.ReadManifest(cachePath, key)
	if err == nil {
		return checkProtocol(cfg, m)
	}
	if !errors.Is(err, os.ErrNotExist) {
		logger.Info("re-parsing the spec snapshot", "reason", err.Error())
	}
	m, err = d.parse(ctx, data, cfg.Source.Format, parseOptions(cfg))
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", snapPath, err)
	}
	if err := config.WriteManifest(cachePath, key, m); err != nil {
		logger.Warn("could not write the manifest cache; the next start will parse again", "error", err.Error())
	}
	return checkProtocol(cfg, m)
}

// checkProtocol rejects a config whose protocol disagrees with the manifest
// it is paired with (a hand edit, or a sync whose source changed format).
func checkProtocol(cfg *config.API, m *model.Manifest) (*model.Manifest, error) {
	want := cfg.Protocol
	if want == "" {
		want = string(model.REST)
	}
	got := string(m.Protocol)
	if got == "" {
		got = string(model.REST) // a manifest with no protocol predates the field; it is REST
	}
	if want != got {
		return nil, fmt.Errorf("config protocol %q does not match the %q manifest; re-run `plugit sync %s`", want, got, cfg.Name)
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

// overrideOp returns op serving the override document doc. Selection must
// describe the document served, so it is replaced by the override's
// selection, when the manifest records one (sels holds the toolset's
// selections by tool key). A cache written before selections were recorded
// has none; the default selection is kept then rather than cleared.
func overrideOp(op model.GraphQLOp, doc string, sels map[string][]string, key model.OpKey) *model.GraphQLOp {
	op.Document = doc
	if sel, ok := sels[string(key)]; ok {
		op.Selection = sel
	}
	return &op
}
