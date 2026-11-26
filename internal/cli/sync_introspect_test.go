package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
)

// withQueryField returns the introspection fixture with one more Query
// field, name: String.
func withQueryField(t *testing.T, fixture []byte, name string) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(fixture, &doc); err != nil {
		t.Fatal(err)
	}
	types := doc["data"].(map[string]any)["__schema"].(map[string]any)["types"].([]any)
	for _, ty := range types {
		tm := ty.(map[string]any)
		if tm["name"] != "Query" {
			continue
		}
		tm["fields"] = append(tm["fields"].([]any), map[string]any{
			"name": name, "description": nil, "args": []any{}, "isDeprecated": false, "deprecationReason": nil,
			"type": map[string]any{"kind": "SCALAR", "name": "String", "ofType": nil},
		})
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// introspectionServer is a fake GraphQL endpoint for the sync tests: it
// answers only introspection POSTs (anything else is 405, as real GraphQL
// servers answer a plain GET), serves the current schema, can redirect to
// another origin, and records each request's Authorization header.
type introspectionServer struct {
	*httptest.Server
	schema   atomic.Value // []byte
	redirect atomic.Value // string: redirect target, "" for none
	hits     atomic.Int32
	auths    sync.Map
}

func newIntrospectionServer(t *testing.T, schema []byte) *introspectionServer {
	t.Helper()
	s := &introspectionServer{}
	s.schema.Store(schema)
	s.redirect.Store("")
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.auths.Store(r.Header.Get("Authorization"), true)
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || !strings.Contains(string(body), "__schema") {
			http.Error(w, "GraphQL needs a POST with a query", http.StatusMethodNotAllowed)
			return
		}
		if to := s.redirect.Load().(string); to != "" {
			http.Redirect(w, r, to, http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.schema.Load().([]byte))
	}))
	t.Cleanup(s.Close)
	return s
}

// onlyToken fails the test if any request reached the server without
// exactly the bearer test token.
func (s *introspectionServer) onlyToken(t *testing.T) {
	t.Helper()
	s.auths.Range(func(k, _ any) bool {
		if k != "Bearer "+runSecret {
			t.Errorf("the endpoint got a request with Authorization %q", k)
		}
		return true
	})
}

// addIntrospected adds API gh with --introspect at endpoint and a bearer
// token, and returns sync settings that resolve the same token.
func addIntrospected(t *testing.T, endpoint string) (config.Paths, syncSettings, *bytes.Buffer) {
	t.Helper()
	p, d, _ := addEnv(t)
	env := map[string]string{"GH_TOKEN": "Bearer " + runSecret}
	d.resolver = envResolver(env)
	if err := addAPI(t.Context(), addSettingsFor(endpoint, func(s *addSettings) {
		s.name, s.introspect, s.fromRun, s.allowPrivate = "gh", true, true, true
		s.headers = []string{"Authorization=env:GH_TOKEN"}
	}), d); err != nil {
		t.Fatalf("add --introspect: %v", err)
	}
	out := &bytes.Buffer{}
	s := syncDeps(out)
	s.resolver = envResolver(env)
	s.info = d.info
	s.yes, s.addNew = true, true
	return p, s, out
}

func hasTool(t *testing.T, p config.Paths, info BuildInfo, key model.OpKey) bool {
	t.Helper()
	cfg, err := config.LoadAPI(p, "gh")
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := p.SpecSnapshot("gh")
	_, sum, _ := config.ReadSnapshot(snap)
	cache, _ := p.Manifest("gh")
	m, err := config.ReadManifest(cache, cfg.CacheKey(sum, parserID(info)))
	if err != nil {
		t.Fatalf("manifest cache: %v", err)
	}
	return slices.ContainsFunc(m.Tools, func(x model.Tool) bool { return x.Key == key })
}

// sync of an API added with --introspect re-runs the introspection against
// the pinned endpoint (source.url is that endpoint), with the credential
// attached only there, instead of downloading the endpoint as a spec file.
// A redirect to another origin fails before that origin is contacted; a
// changed schema is picked up with --yes. sync --spec keeps the saved
// (forced) format: an introspection file works, an SDL file is refused
// with an error that names the saved format.
func TestSyncReintrospects(t *testing.T) {
	fixture := introspectionFixture(t)
	other := newGraphQLUpstream(t, func(u *gqlUpstream) { u.introspection = fixture })
	pinned := newIntrospectionServer(t, fixture)
	endpoint := pinned.URL + "/graphql"
	p, s, out := addIntrospected(t, endpoint)

	pinned.redirect.Store(other.URL + "/graphql")
	if err := syncAPI(t.Context(), p, "gh", s); !errors.Is(err, httpx.ErrRedirectBlocked) {
		t.Fatalf("sync through a cross-origin redirect: err = %v, want ErrRedirectBlocked", err)
	}
	if other.hits.Load() != 0 {
		t.Errorf("the other origin was contacted %d times", other.hits.Load())
	}
	pinned.redirect.Store("")

	pinned.schema.Store(withQueryField(t, fixture, "ping"))
	if err := syncAPI(t.Context(), p, "gh", s); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "query ping") || !strings.Contains(out.String(), "1 added") {
		t.Errorf("sync output:\n%s", out)
	}
	cfg, err := config.LoadAPI(p, "gh")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source.URL != endpoint || cfg.Source.Format != graphql.FormatIntrospection {
		t.Errorf("source after sync = %+v", cfg.Source)
	}
	if _, ok := cfg.Toolsets["default"].Tools["query ping"]; !ok {
		t.Error("--add-new didn't add query ping")
	}
	if !hasTool(t, p, s.info, "query ping") {
		t.Error("the manifest cache has no query ping after sync")
	}
	pinned.onlyToken(t)
	if pinned.hits.Load() < 3 || other.hits.Load() != 0 {
		t.Errorf("pinned hits = %d, other hits = %d", pinned.hits.Load(), other.hits.Load())
	}

	// --spec reads a file instead of the endpoint, in the saved format.
	before := pinned.hits.Load()
	s.spec = writeSDL(t, gqlSDL)
	err = syncAPI(t.Context(), p, "gh", s)
	if err == nil {
		t.Fatal("sync --spec with an SDL file must fail: the API's saved format is graphql-introspection")
	}
	for _, want := range []string{graphql.FormatIntrospection, "add"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
	introFile := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(introFile, withQueryField(t, withQueryField(t, fixture, "ping"), "pong"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.spec = introFile
	out.Reset()
	if err := syncAPI(t.Context(), p, "gh", s); err != nil {
		t.Fatalf("sync --spec with an introspection file: %v\n%s", err, out)
	}
	if !hasTool(t, p, s.info, "query pong") {
		t.Error("sync --spec didn't pick up query pong")
	}
	if pinned.hits.Load() != before {
		t.Error("sync --spec still introspected the endpoint")
	}
}

// An endpoint whose path carries a percent-escape (/api%2Fv1/graphql) is
// saved as source.url exactly, so sync still recognizes the API as
// introspected and asks the endpoint again with the token, rather than
// falling back to a credential-less GET of a rewritten URL.
func TestSyncReintrospectsEscapedPath(t *testing.T) {
	fixture := introspectionFixture(t)
	pinned := newIntrospectionServer(t, fixture)
	endpoint := pinned.URL + "/api%2Fv1/graphql"
	p, s, out := addIntrospected(t, endpoint)
	cfg, err := config.LoadAPI(p, "gh")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != endpoint || cfg.Source.URL != endpoint {
		t.Errorf("endpoint %q, source.url %q, want both %q", cfg.Endpoint, cfg.Source.URL, endpoint)
	}
	pinned.schema.Store(withQueryField(t, fixture, "ping"))
	if err := syncAPI(t.Context(), p, "gh", s); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	if !hasTool(t, p, s.info, "query ping") {
		t.Error("sync didn't re-introspect the escaped endpoint")
	}
	pinned.onlyToken(t)
}
