package cli

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
	"github.com/swayam5342/plugit/internal/secrets"
)

func writeSDL(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "schema.graphql")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func introspectionFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/specs/graphql/user.introspection.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func envResolver(env map[string]string) *secrets.Resolver {
	return &secrets.Resolver{LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }}
}

// pickFields is a fake picker selecting query user (with these
// response_fields) and query org.
func pickFields(got *pickInput, fields ...string) func(context.Context, pickInput) (pickResult, error) {
	return func(_ context.Context, in pickInput) (pickResult, error) {
		if got != nil {
			*got = in
		}
		return pickResult{toolset: config.Toolset{Tools: map[string]config.ToolOverride{
			"query user": {ResponseFields: fields}, "query org": {}}}}, nil
	}
}

// response_fields are compiled in the parse child when add or edit saves a
// GraphQL toolset: [id] becomes an override document selecting only id, an
// unknown path is an error naming the toolset, tool and path (nothing saved),
// and serve attaches no jsonpick picker to the GraphQL tool. add records the
// detected protocol and format, and the picker gets the GraphQL protocol.
func TestResponseFieldsCompiledInChild(t *testing.T) {
	p, d, _ := addEnv(t)
	schemaPath := writeSDL(t, gqlSDL)
	s := addSettingsFor(schemaPath, func(s *addSettings) { s.name, s.baseURL = "gh", "https://api.example.test/graphql" })

	d.pick = pickFields(nil, "id", "nosuch")
	err := addAPI(t.Context(), s, d)
	if err == nil {
		t.Fatal("an unknown response_fields path must fail add")
	}
	for _, want := range []string{"default", "query user", "nosuch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("add error should mention %q: %v", want, err)
		}
	}
	if cfgPath, _ := p.APIConfig("gh"); fileExists(cfgPath) {
		t.Error("a failed add saved plugit.yaml")
	}

	var in pickInput
	d.pick = pickFields(&in, "id")
	if err := addAPI(t.Context(), s, d); err != nil {
		t.Fatalf("add: %v", err)
	}
	if in.proto != model.GraphQL {
		t.Errorf("picker protocol = %q, want graphql", in.proto)
	}
	cfg, err := config.LoadAPI(p, "gh")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Protocol != string(model.GraphQL) || cfg.Source.Format != graphql.FormatSDL {
		t.Errorf("saved protocol/format = %q/%q", cfg.Protocol, cfg.Source.Format)
	}
	const onlyID = "query PlugIt_user($id: ID!, $tenant: String) { user(id: $id, tenant: $tenant) { id } }"
	snap, _ := p.SpecSnapshot("gh")
	_, sum, _ := config.ReadSnapshot(snap)
	cache, _ := p.Manifest("gh")
	m, err := config.ReadManifest(cache, cfg.CacheKey(sum, parserID(d.info)))
	if err != nil {
		t.Fatalf("manifest cache: %v", err)
	}
	if got := m.GraphQL.OverrideDocs["default"]["query user"]; got != onlyID {
		t.Errorf("OverrideDocs[default][query user] = %q\nwant %q", got, onlyID)
	}

	noParse := gqlDeps(func(context.Context, []byte, string, ingest.Options) (*model.Manifest, error) {
		t.Error("serve re-parsed right after add")
		return nil, errors.New("unexpected parse")
	})
	a, err := loadActive(t.Context(), serveSettings{paths: p, name: "gh"}, noParse, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range a.tools {
		if st.Fields != nil {
			t.Errorf("%s has a jsonpick picker", st.Tool.Key)
		}
		if st.Tool.Key == "query user" && st.Tool.GraphQL.Document != onlyID {
			t.Errorf("served document = %q", st.Tool.GraphQL.Document)
		}
	}

	// edit recompiles too: a bad path is refused and nothing changes; a good
	// one replaces the override.
	cfgPath, _ := p.APIConfig("gh")
	before, _ := os.ReadFile(cfgPath)
	d.pick = pickFields(nil, "nosuch")
	if err := editAPI(t.Context(), p, "gh", "default", d); err == nil || !strings.Contains(err.Error(), "nosuch") {
		t.Errorf("edit with an unknown path: err = %v", err)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Error("a failed edit changed plugit.yaml")
	}
	d.pick = pickFields(nil, "name")
	if err := editAPI(t.Context(), p, "gh", "default", d); err != nil {
		t.Fatalf("edit: %v", err)
	}
	a, err = loadActive(t.Context(), serveSettings{paths: p, name: "gh"}, noParse, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range a.tools {
		if st.Tool.Key == "query user" && !strings.HasSuffix(st.Tool.GraphQL.Document, "{ name } }") {
			t.Errorf("document after edit = %q", st.Tool.GraphQL.Document)
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// plugit add --introspect: the URL is the endpoint, pinned before anything
// is sent; the standard introspection query is POSTed there with the
// credential, the raw result becomes the snapshot and is parsed in the child
// as graphql-introspection.
func TestIntrospectBuildsManifest(t *testing.T) {
	fixture := introspectionFixture(t)
	up := newGraphQLUpstream(t, func(u *gqlUpstream) { u.introspection = fixture })
	p, d, out := addEnv(t)
	d.resolver = envResolver(map[string]string{"GH_TOKEN": "Bearer " + runSecret})
	endpoint := up.URL + "/graphql"
	s := addSettingsFor(endpoint, func(s *addSettings) {
		s.name, s.introspect, s.fromRun, s.allowPrivate = "gh", true, true, true
		s.headers = []string{"Authorization=env:GH_TOKEN"}
	})
	if err := addAPI(t.Context(), s, d); err != nil {
		t.Fatalf("add --introspect: %v", err)
	}
	if got, _ := up.lastAuth.Load().(string); got != "Bearer "+runSecret {
		t.Errorf("introspection request Authorization = %q", got)
	}
	if q := up.body(t).Query; !strings.Contains(q, "__schema") || !strings.Contains(q, "IntrospectionQuery") {
		t.Errorf("sent query = %q, want the standard introspection query", q)
	}
	cfg, err := config.LoadAPI(p, "gh")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Protocol != string(model.GraphQL) || cfg.Source.Format != graphql.FormatIntrospection ||
		cfg.Endpoint != endpoint || cfg.Source.URL != endpoint {
		t.Errorf("config = protocol %q format %q endpoint %q source %q", cfg.Protocol, cfg.Source.Format, cfg.Endpoint, cfg.Source.URL)
	}
	snap, _ := p.SpecSnapshot("gh")
	data, sum, err := config.ReadSnapshot(snap)
	if err != nil || string(data) != string(fixture) || cfg.Source.SHA256 != sum {
		t.Errorf("snapshot is not the raw introspection result (err %v)", err)
	}
	cache, _ := p.Manifest("gh")
	m, err := config.ReadManifest(cache, cfg.CacheKey(sum, parserID(d.info)))
	if err != nil || !slices.ContainsFunc(m.Tools, func(x model.Tool) bool { return x.Key == "query user" }) {
		t.Errorf("manifest: %v", err)
	}
	if strings.Contains(out.String(), runSecret) {
		t.Error("output leaks the credential")
	}
}

// T8 for --introspect: the bearer token goes only to the pinned origin. A
// redirect to a second origin is refused before that origin is contacted,
// and an --endpoint that differs from the introspected URL is refused before
// anything is sent.
func TestIntrospectSendsTokenOnlyToPinnedOrigin(t *testing.T) {
	fixture := introspectionFixture(t)
	other := newGraphQLUpstream(t, func(u *gqlUpstream) { u.introspection = fixture })
	pinned := newGraphQLUpstream(t, func(u *gqlUpstream) { u.redirectTo, u.redirectAll = other.URL+"/graphql", true })
	_, d, _ := addEnv(t)
	d.resolver = envResolver(map[string]string{"GH_TOKEN": "Bearer " + runSecret})
	settings := func(url string, mut func(*addSettings)) addSettings {
		return addSettingsFor(url, func(s *addSettings) {
			s.name, s.introspect, s.fromRun, s.allowPrivate = "gh", true, true, true
			s.headers = []string{"Authorization=env:GH_TOKEN"}
			if mut != nil {
				mut(s)
			}
		})
	}

	err := addAPI(t.Context(), settings(pinned.URL+"/graphql", nil), d)
	if !errors.Is(err, httpx.ErrRedirectBlocked) {
		t.Fatalf("a redirect to another origin must fail the introspection with ErrRedirectBlocked: %v", err)
	}
	if got, _ := pinned.lastAuth.Load().(string); got != "Bearer "+runSecret {
		t.Errorf("pinned origin got Authorization %q", got)
	}
	if n := other.hits.Load(); n != 0 {
		t.Errorf("the second origin was contacted %d times", n)
	}
	if strings.Contains(err.Error(), runSecret) {
		t.Errorf("error leaks the token: %v", err)
	}

	pinnedHits := pinned.hits.Load()
	err = addAPI(t.Context(), settings(pinned.URL+"/graphql", func(s *addSettings) { s.baseURL = other.URL + "/graphql" }), d)
	if err == nil || !strings.Contains(err.Error(), "--endpoint") {
		t.Errorf("--endpoint differing from the introspected URL must be refused: %v", err)
	}
	if pinned.hits.Load() != pinnedHits || other.hits.Load() != 0 {
		t.Error("a request was sent before the endpoint mismatch was refused")
	}
}
