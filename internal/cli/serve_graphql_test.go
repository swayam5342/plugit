package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
)

// gqlSDL is a small GraphQL schema for the cli tests: a query with a
// declared tenant variable, one without it, an input-object argument, and a
// mutation.
const gqlSDL = `"""A user account."""
type User { id: ID! name: String email: String org: Org }
type Org { id: ID! name: String! }
input UserFilter { name: String }
type Query {
  "Fetch one user."
  user(id: ID!, tenant: String): User
  org(id: ID!): Org
  users(filter: UserFilter, tenant: String): [User!]!
}
type Mutation { deleteUser(id: ID!): Boolean }
`

const gqlYAML = `version: 1
name: gh
protocol: graphql
source: {url: https://example.test/schema.graphql, format: graphql-sdl}
endpoint: %s
network: {allow_private: true}
auth: {type: bearer, secret: env:GH_TOKEN}
fixed:
  - {name: tenant, in: variable, value: literal:acme}
toolsets:
  default:
    tools:
      "query user": {response_fields: ["id", "name"]}
      "query org": {}
`

// setupGraphQLAPI writes apis/gh/{plugit.yaml,spec.snapshot.json} for a
// GraphQL API pinned to endpoint.
func setupGraphQLAPI(t *testing.T, endpoint string) config.Paths {
	t.Helper()
	p := config.NewPaths(t.TempDir())
	dir, _ := p.APIDir("gh")
	if err := config.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	cfg, _ := p.APIConfig("gh")
	if err := config.WriteFileAtomic(cfg, fmt.Appendf(nil, gqlYAML, endpoint)); err != nil {
		t.Fatal(err)
	}
	snap, _ := p.SpecSnapshot("gh")
	if err := config.WriteFileAtomic(snap, []byte(gqlSDL)); err != nil {
		t.Fatal(err)
	}
	return p
}

func rewriteGraphQLAPI(t *testing.T, p config.Paths, old, replacement string) {
	t.Helper()
	path, _ := p.APIConfig("gh")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), old) {
		t.Fatalf("the gh config has no %q to replace", old)
	}
	if err := config.WriteFileAtomic(path, []byte(strings.Replace(string(raw), old, replacement, 1))); err != nil {
		t.Fatal(err)
	}
}

func gqlDeps(parse parseFunc) runDeps {
	d := deps(nil, nil, map[string]string{"GH_TOKEN": runSecret})
	d.parse = parse
	return d
}

// gqlUpstream is a fake GraphQL server. It records the last request's
// Authorization header and body and answers with a fixed user. Options set
// before it starts: redirectTo receives {"id":"redirect"} calls (or every
// request, with redirectAll), and introspection answers a request whose query
// mentions __schema.
type gqlUpstream struct {
	*httptest.Server
	hits          atomic.Int32
	lastAuth      atomic.Value
	lastBody      atomic.Value
	redirectTo    string
	redirectAll   bool
	introspection []byte
}

func newGraphQLUpstream(t *testing.T, opts ...func(*gqlUpstream)) *gqlUpstream {
	t.Helper()
	u := &gqlUpstream{}
	for _, o := range opts {
		o(u)
	}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		u.lastAuth.Store(r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		u.lastBody.Store(string(body))
		if u.redirectTo != "" && (u.redirectAll || strings.Contains(string(body), `"id":"redirect"`)) {
			http.Redirect(w, r, u.redirectTo, http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if u.introspection != nil && strings.Contains(string(body), "__schema") {
			_, _ = w.Write(u.introspection)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"user":{"id":"1","name":"Ada"}}}`)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *gqlUpstream) body(t *testing.T) gqlWire {
	t.Helper()
	raw, _ := u.lastBody.Load().(string)
	var wb gqlWire
	if err := json.Unmarshal([]byte(raw), &wb); err != nil {
		t.Fatalf("upstream body is not a GraphQL request: %v\n%s", err, raw)
	}
	return wb
}

type gqlWire struct {
	Query     string                     `json:"query"`
	Variables map[string]json.RawMessage `json:"variables"`
}

const userOverrideDoc = "query PlugIt_user($id: ID!, $tenant: String) { user(id: $id, tenant: $tenant) { id name } }"

// loadActive on a GraphQL API builds the graphql impl, uses the toolset's
// compiled override document for a tool with response_fields and the default
// one otherwise, attaches no jsonpick picker to any GraphQL tool, warns once
// (on the log) for a tool that doesn't declare a fixed variable, and refuses
// a fixed variable whose declared type is an input object.
func TestServeSelectsGraphQLImpl(t *testing.T) {
	p := setupGraphQLAPI(t, "https://api.example.test/graphql")
	logBuf := &syncBuf{}
	logger := slog.New(slog.NewTextHandler(logBuf, nil))
	a, err := loadActive(t.Context(), serveSettings{paths: p, name: "gh"}, gqlDeps(isolate.Parse), logger)
	if err != nil {
		t.Fatalf("loadActive: %v", err)
	}
	if _, ok := a.proto.(graphql.GraphQL); !ok {
		t.Fatalf("proto = %T, want graphql.GraphQL", a.proto)
	}
	docs := map[model.OpKey]string{}
	for _, st := range a.tools {
		if st.Fields != nil {
			t.Errorf("%s has a jsonpick picker; GraphQL response_fields are compiled into the document", st.Tool.Key)
		}
		docs[st.Tool.Key] = st.Tool.GraphQL.Document
	}
	if docs["query user"] != userOverrideDoc {
		t.Errorf("query user document = %q\nwant %q", docs["query user"], userOverrideDoc)
	}
	if want := "query PlugIt_org($id: ID!) { org(id: $id) { id name } }"; docs["query org"] != want {
		t.Errorf("query org document = %q, want the default %q", docs["query org"], want)
	}
	log := logBuf.String()
	if n := strings.Count(log, "fixed variable"); n != 1 || !strings.Contains(log, "query org") || !strings.Contains(log, "tenant") {
		t.Errorf("want one warning naming query org and tenant, got %d:\n%s", n, log)
	}

	rewriteGraphQLAPI(t, p, `"query org": {}`, `"query users": {}`)
	rewriteGraphQLAPI(t, p, "{name: tenant, in: variable, value: literal:acme}", "{name: filter, in: variable, value: literal:x}")
	_, err = loadActive(t.Context(), serveSettings{paths: p, name: "gh"}, gqlDeps(isolate.Parse), logger)
	if err == nil || !strings.Contains(err.Error(), "filter") || !strings.Contains(err.Error(), "query users") {
		t.Errorf("an object-typed fixed variable must be refused, naming it and the tool: %v", err)
	}
}

// End to end over MCP: a GraphQL query tool sends its compiled document with
// the model's arguments and the fixed variable, returns the selected fields
// unfiltered (no jsonpick pass), and the credential reaches only the pinned
// origin: a redirect elsewhere is refused before the other origin sees it.
func TestServeGraphQLCall(t *testing.T) {
	other := newGraphQLUpstream(t)
	up := newGraphQLUpstream(t, func(u *gqlUpstream) { u.redirectTo = other.URL + "/steal" })
	p := setupGraphQLAPI(t, up.URL+"/graphql")
	cs, stderr := startServer(t, func(ctx context.Context, d runDeps) error {
		return serveAPI(ctx, serveSettings{paths: p, name: "gh"}, d)
	}, gqlDeps(isolate.Parse))

	if got := toolNames(t, cs); !slices.Equal(got, []string{"org", "user"}) {
		t.Fatalf("tools = %v", got)
	}
	res, _ := cs.ListTools(t.Context(), nil)
	for _, tl := range res.Tools {
		if strings.Contains(fmt.Sprint(tl.InputSchema), "tenant") {
			t.Errorf("%s exposes the fixed variable tenant", tl.Name)
		}
	}
	r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "user", Arguments: map[string]any{"id": "1"}})
	if err != nil || r.IsError {
		t.Fatalf("call: err=%v text=%s\n%s", err, resultText(r), stderr)
	}
	if !strings.Contains(resultText(r), `"name": "Ada"`) || strings.Contains(resultText(r), "Filtered to fields") {
		t.Errorf("result = %s", resultText(r))
	}
	wb := up.body(t)
	if wb.Query != userOverrideDoc {
		t.Errorf("sent document = %q\nwant %q", wb.Query, userOverrideDoc)
	}
	if string(wb.Variables["id"]) != `"1"` || string(wb.Variables["tenant"]) != `"acme"` {
		t.Errorf("variables = %v", wb.Variables)
	}
	if got, _ := up.lastAuth.Load().(string); got != "Bearer "+runSecret {
		t.Errorf("pinned origin got Authorization %q", got)
	}

	r, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "user", Arguments: map[string]any{"id": "redirect"}})
	if err != nil || !r.IsError {
		t.Fatalf("a cross-origin redirect must be an error: %v %s", err, resultText(r))
	}
	if other.hits.Load() != 0 {
		t.Errorf("the other origin was reached %d times", other.hits.Load())
	}
	if strings.Contains(resultText(r), runSecret) || strings.Contains(stderr.String(), runSecret) {
		t.Error("the credential leaked into the result or the log")
	}
}

// The manifest cache key covers GraphQL response_fields, so changing them
// re-compiles in the child on the next start; REST-style [*] markers are
// stripped first (org[*].name is org.name for GraphQL), and a REST config's
// key ignores response_fields entirely.
func TestResponseFieldsBustCacheKey(t *testing.T) {
	gql := func(fields ...string) *config.API {
		return &config.API{Protocol: "graphql", Toolsets: map[string]config.Toolset{
			"default": {Tools: map[string]config.ToolOverride{"query user": {ResponseFields: fields}}}}}
	}
	base := gql().CacheKey("sha", "parser")
	if base.ResponseFieldsSHA256 != "" {
		t.Errorf("no response_fields: key hash = %q, want empty", base.ResponseFieldsSHA256)
	}
	a, b := gql("id").CacheKey("sha", "parser"), gql("id", "name").CacheKey("sha", "parser")
	if a == b || a == base || a.ResponseFieldsSHA256 == "" {
		t.Errorf("response_fields must change the key: %+v %+v", a, b)
	}
	if x, y := gql("org[*].name").CacheKey("sha", "parser"), gql("org.name").CacheKey("sha", "parser"); x != y {
		t.Errorf("[*] must be stripped before hashing: %+v vs %+v", x, y)
	}
	rest := &config.API{Protocol: "rest", Toolsets: map[string]config.Toolset{
		"default": {Tools: map[string]config.ToolOverride{"GET /pets": {ResponseFields: []string{"id"}}}}}}
	if k := rest.CacheKey("sha", "parser"); k != (config.CacheKey{SpecSHA256: "sha", Parser: "parser"}) {
		t.Errorf("a REST key must not depend on response_fields: %+v", k)
	}

	p := setupGraphQLAPI(t, "https://api.example.test/graphql")
	var parses atomic.Int32
	load := func() *active {
		t.Helper()
		a, err := loadActive(t.Context(), serveSettings{paths: p, name: "gh"}, gqlDeps(countingParse(&parses)), slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	load()
	load()
	if parses.Load() != 1 {
		t.Fatalf("parses = %d after a cold and a warm start, want 1", parses.Load())
	}
	rewriteGraphQLAPI(t, p, `response_fields: ["id", "name"]`, `response_fields: ["org[*].name"]`)
	a2 := load()
	if parses.Load() != 2 {
		t.Errorf("changing response_fields didn't re-parse (parses = %d)", parses.Load())
	}
	for _, st := range a2.tools {
		if st.Tool.Key == "query user" && !strings.HasSuffix(st.Tool.GraphQL.Document, "{ org { name } } }") {
			t.Errorf("query user document = %q, want the new selection", st.Tool.GraphQL.Document)
		}
	}
}

// serve refuses to start when a selected tool's response_fields in the
// active toolset don't compile against the schema, naming the tool.
func TestServeRefusesBrokenResponseFields(t *testing.T) {
	p := setupGraphQLAPI(t, "https://api.example.test/graphql")
	rewriteGraphQLAPI(t, p, `response_fields: ["id", "name"]`, `response_fields: ["id", "nosuch"]`)
	_, err := loadActive(t.Context(), serveSettings{paths: p, name: "gh"}, gqlDeps(isolate.Parse), slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("serve must refuse a toolset whose response_fields don't compile")
	}
	for _, want := range []string{"query user", "nosuch", "default"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}
