package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
	"github.com/swayam5342/plugit/internal/secrets"
	"github.com/swayam5342/plugit/internal/store"
)

const petsYAML = `version: 1
name: pets
source: {url: https://example.test/openapi.yaml}
endpoint: %s
prefix: pp
network: {allow_private: true}
auth: {type: header, header: X-Api-Key, secret: env:PETS_KEY}
policy: {rate_limit: 10/min}
toolsets:
  default:
    tools:
      "GET /pets": {name: list_pets, description: List all pets.}
      "GET /pets/{id}": {}
      "POST /pets": {}
      "GET /gone": {}
  denied:
    tools:
      "GET /pets": {policy: deny}
      "GET /pets/{id}": {}
  nothing:
    tools:
      "GET /gone": {}
`

// setupAPI writes apis/pets/{plugit.yaml,spec.snapshot.json}. The spec's
// own server is an attacker host: serve must only ever use endpoint.
func setupAPI(t *testing.T, baseURL string) config.Paths {
	t.Helper()
	p := config.NewPaths(t.TempDir())
	dir, _ := p.APIDir("pets")
	if err := config.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	cfg, _ := p.APIConfig("pets")
	if err := config.WriteFileAtomic(cfg, fmt.Appendf(nil, petsYAML, baseURL)); err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, p, fmt.Sprintf(petSpec, "https://attacker.example.test"))
	return p
}

func rewriteAPI(t *testing.T, p config.Paths, old, replacement string) {
	t.Helper()
	path, _ := p.APIConfig("pets")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), old) {
		t.Fatalf("the pets config has no %q to replace", old)
	}
	if err := config.WriteFileAtomic(path, []byte(strings.Replace(string(raw), old, replacement, 1))); err != nil {
		t.Fatal(err)
	}
}

func setupInsecureAPI(t *testing.T, baseURL string) config.Paths {
	t.Helper()
	p := setupAPI(t, "https://placeholder.example.test")
	rewriteAPI(t, p, "network: {allow_private: true}", "network: {allow_private: true, allow_insecure: true}")
	rewriteAPI(t, p, "https://placeholder.example.test", baseURL)
	return p
}

func writeSnapshot(t *testing.T, p config.Paths, content string) {
	t.Helper()
	snap, _ := p.SpecSnapshot("pets")
	if err := config.WriteFileAtomic(snap, []byte(content)); err != nil {
		t.Fatal(err)
	}
}

// countingParse wraps the real child-process parser and counts calls.
func countingParse(n *atomic.Int32) parseFunc {
	return func(ctx context.Context, data []byte, format string, opts ingest.Options) (*model.Manifest, error) {
		n.Add(1)
		return isolate.Parse(ctx, data, format, opts)
	}
}

func serveDeps(parse parseFunc) runDeps {
	d := deps(nil, nil, map[string]string{"PETS_KEY": runSecret})
	d.parse = parse
	return d
}

func startServe(t *testing.T, s serveSettings, d runDeps) (*mcp.ClientSession, *syncBuf) {
	t.Helper()
	return startServer(t, func(ctx context.Context, d runDeps) error { return serveAPI(ctx, s, d) }, d)
}

func TestServeEndToEnd(t *testing.T) {
	up := newUpstream(t)
	p := setupAPI(t, up.URL)
	var parses atomic.Int32
	cs, stderr := startServe(t, serveSettings{paths: p, name: "pets"}, serveDeps(countingParse(&parses)))

	if got := toolNames(t, cs); !slices.Equal(got, []string{"pp_getPet", "pp_list_pets"}) {
		t.Fatalf("tools = %v", got)
	}
	res, _ := cs.ListTools(t.Context(), nil)
	for _, tl := range res.Tools {
		if tl.Name == "pp_list_pets" && tl.Description != "List all pets." {
			t.Errorf("description override lost: %q", tl.Description)
		}
		if strings.Contains(fmt.Sprint(tl.InputSchema), "X-Api-Key") {
			t.Errorf("%s exposes the auth header", tl.Name)
		}
	}

	r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "pp_list_pets", Arguments: map[string]any{"limit": 2}})
	if err != nil || r.IsError {
		t.Fatalf("call: err=%v text=%s", err, resultText(r))
	}
	if up.lastURL.Load() != "/pets?limit=2" || up.lastKey.Load() != runSecret {
		t.Errorf("upstream got url=%v key-sent=%v", up.lastURL.Load(), up.lastKey.Load() == runSecret)
	}
	if strings.Contains(resultText(r), runSecret) {
		t.Errorf("echoed credential not redacted: %s", resultText(r))
	}

	if parses.Load() != 1 {
		t.Errorf("parsed %d times, want 1 (no cache yet)", parses.Load())
	}
	mpath, _ := p.Manifest("pets")
	if _, err := os.Stat(mpath); err != nil {
		t.Errorf("manifest cache not written: %v", err)
	}
	log := stderr.String()
	for _, want := range []string{"plugit serve", "GET /gone", "rate_limit"} {
		if !strings.Contains(log, want) {
			t.Errorf("stderr should mention %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, runSecret) {
		t.Errorf("stderr leaks the credential:\n%s", log)
	}
}

// A warm start loads manifest.json and never starts the parse child
// (SPEC §3.6: serve must start in under 100 ms).
func TestServeUsesManifestCache(t *testing.T) {
	up := newUpstream(t)
	p := setupAPI(t, up.URL)
	var parses atomic.Int32
	cs, _ := startServe(t, serveSettings{paths: p, name: "pets"}, serveDeps(countingParse(&parses)))
	toolNames(t, cs)
	if parses.Load() != 1 {
		t.Fatalf("cold start parsed %d times", parses.Load())
	}

	noParse := func(context.Context, []byte, string, ingest.Options) (*model.Manifest, error) {
		t.Error("warm start re-parsed the spec")
		return nil, errors.New("unexpected parse")
	}
	cs, _ = startServe(t, serveSettings{paths: p, name: "pets"}, serveDeps(noParse))
	if got := toolNames(t, cs); len(got) != 2 {
		t.Errorf("warm start tools = %v", got)
	}

	writeSnapshot(t, p, strings.Replace(fmt.Sprintf(petSpec, "https://attacker.example.test"), "operationId: getPet", "operationId: fetchPet", 1))
	cs, _ = startServe(t, serveSettings{paths: p, name: "pets"}, serveDeps(countingParse(&parses)))
	if got := toolNames(t, cs); !slices.Contains(got, "pp_fetchPet") {
		t.Errorf("stale cache served: %v", got)
	}
	if parses.Load() != 2 {
		t.Errorf("parses = %d, want 2", parses.Load())
	}
	cs, _ = startServe(t, serveSettings{paths: p, name: "pets"}, serveDeps(noParse))
	toolNames(t, cs)
}

// A corrupt cache is derived data: re-parse instead of failing.
func TestServeRecoversFromCorruptCache(t *testing.T) {
	up := newUpstream(t)
	p := setupAPI(t, up.URL)
	mpath, _ := p.Manifest("pets")
	if err := config.WriteFileAtomic(mpath, []byte("{garbage")); err != nil {
		t.Fatal(err)
	}
	var parses atomic.Int32
	cs, _ := startServe(t, serveSettings{paths: p, name: "pets"}, serveDeps(countingParse(&parses)))
	if got := toolNames(t, cs); len(got) != 2 || parses.Load() != 1 {
		t.Errorf("tools = %v, parses = %d", got, parses.Load())
	}
}

func TestServeToolset(t *testing.T) {
	up := newUpstream(t)
	p := setupAPI(t, up.URL)
	cs, _ := startServe(t, serveSettings{paths: p, name: "pets", toolset: "denied"}, serveDeps(isolate.Parse))
	if got := toolNames(t, cs); !slices.Equal(got, []string{"pp_getPet"}) {
		t.Errorf("tools = %v", got)
	}
}

func TestServeSetupFailures(t *testing.T) {
	up := newUpstream(t)
	cases := map[string]struct {
		mut  func(t *testing.T, p config.Paths, s *serveSettings, d *runDeps)
		want []string
		is   error
	}{
		"unknown API": {func(_ *testing.T, _ config.Paths, s *serveSettings, _ *runDeps) { s.name = "nope" },
			[]string{"nope", "plugit add"}, config.ErrAPINotFound},
		"unknown toolset": {func(_ *testing.T, _ config.Paths, s *serveSettings, _ *runDeps) { s.toolset = "nope" },
			[]string{"nope", "default, denied, nothing"}, nil},
		"toolset selects nothing": {func(_ *testing.T, _ config.Paths, s *serveSettings, _ *runDeps) { s.toolset = "nothing" },
			[]string{"nothing", "no tools"}, nil},
		"missing snapshot": {func(t *testing.T, p config.Paths, _ *serveSettings, _ *runDeps) {
			snap, _ := p.SpecSnapshot("pets")
			if err := os.Remove(snap); err != nil {
				t.Fatal(err)
			}
		}, []string{"spec.snapshot.json"}, nil},
		"missing secret": {func(_ *testing.T, _ config.Paths, _ *serveSettings, d *runDeps) {
			d.resolver = &secrets.Resolver{LookupEnv: func(string) (string, bool) { return "", false }}
		}, []string{"auth.secret", "PETS_KEY"}, secrets.ErrNotFound},
		"invalid config": {func(t *testing.T, p config.Paths, _ *serveSettings, _ *runDeps) {
			cfg, _ := p.APIConfig("pets")
			if err := config.WriteFileAtomic(cfg, []byte("version: 1\nname: pets\nbogus: 1\n")); err != nil {
				t.Fatal(err)
			}
		}, []string{"bogus"}, config.ErrInvalidConfig},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := setupAPI(t, up.URL)
			s := serveSettings{paths: p, name: "pets"}
			d := serveDeps(isolate.Parse)
			st, _ := mcp.NewInMemoryTransports()
			d.transport, d.stderr = st, &syncBuf{}
			tc.mut(t, p, &s, &d)
			err := serveAPI(t.Context(), s, d)
			if err == nil {
				t.Fatal("expected a setup error")
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("err = %v, want %v", err, tc.is)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q should mention %q", err, want)
				}
			}
			if strings.Contains(err.Error(), runSecret) {
				t.Errorf("error leaks the secret: %v", err)
			}
		})
	}
}

// A fixed param declared with in: cookie must resolve to model.InCookie,
// not be silently collapsed to the query string, where a secret would leak
// into access/proxy logs. Config accepts cookie via the P3 table.
func TestBuildTargetFixedCookieLocation(t *testing.T) {
	cfg := &config.API{Name: "pets", Protocol: "rest",
		Auth: config.Auth{Type: config.AuthNone},
		Fixed: []config.Fixed{
			{Name: "sid", In: "cookie", Value: "env:PETS_SID"},
		}}
	d := runDeps{resolver: &secrets.Resolver{LookupEnv: func(k string) (string, bool) {
		if k == "PETS_SID" {
			return "s3cr3t", true
		}
		return "", false
	}}}
	u, _ := url.Parse("https://api.example.test")
	fx, _, err := buildFixedTarget(cfg, d.resolver)
	if err != nil {
		t.Fatalf("buildFixedTarget: %v", err)
	}
	fx.userAgent = "ua"
	tg := restTargetFrom(fx, u)
	if len(tg.Fixed) != 1 || tg.Fixed[0].In != model.InCookie {
		t.Errorf("fixed cookie location = %+v, want In=InCookie", tg.Fixed)
	}
}

// serve records each call (redacted) in the call log and prunes calls past
// log.retention when it starts.
func TestServeRecordsCalls(t *testing.T) {
	up := newUpstream(t)
	p := setupAPI(t, up.URL)
	db, err := store.Open(t.Context(), p.DB())
	if err != nil {
		t.Fatal(err)
	}
	old := store.Call{Time: time.Now().Add(-60 * 24 * time.Hour), API: "pets", Toolset: "default", Tool: "old", Op: "GET /old", Outcome: store.OK}
	if _, err := db.Insert(t.Context(), old); err != nil {
		t.Fatal(err)
	}

	cs, _ := startServe(t, serveSettings{paths: p, name: "pets"}, serveDeps(isolate.Parse))
	if r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "pp_list_pets", Arguments: map[string]any{"limit": 2}}); err != nil || r.IsError {
		t.Fatalf("call: %v %s", err, resultText(r))
	}

	var got []store.Call
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		got, err = db.List(t.Context(), store.Filter{API: "pets"})
		if err == nil && len(got) == 1 && got[0].Tool == "pp_list_pets" {
			break
		}
	}
	if len(got) != 1 || got[0].Tool != "pp_list_pets" || got[0].Outcome != store.OK || string(got[0].Args) != `{"limit":2}` {
		t.Fatalf("call log = %+v", got)
	}
	_ = db.Close()
	data, _ := os.ReadFile(p.DB())
	if strings.Contains(string(data), runSecret) {
		t.Error("call log file contains the secret")
	}
}

// policy.rate_limit and per-tool rate_limit are enforced by serve.
func TestServeRateLimits(t *testing.T) {
	up := newUpstream(t)
	p := setupAPI(t, up.URL)
	cfgPath, _ := p.APIConfig("pets")
	y := strings.Replace(fmt.Sprintf(petsYAML, up.URL), `"GET /pets/{id}": {}`, `"GET /pets/{id}": {rate_limit: 1/min}`, 1)
	if err := config.WriteFileAtomic(cfgPath, []byte(y)); err != nil {
		t.Fatal(err)
	}
	cs, stderr := startServe(t, serveSettings{paths: p, name: "pets"}, serveDeps(isolate.Parse))
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := call("pp_getPet", map[string]any{"id": "1"}); r.IsError {
		t.Fatalf("first call: %s", resultText(r))
	}
	if r := call("pp_getPet", map[string]any{"id": "2"}); !r.IsError || !strings.Contains(resultText(r), "1/min for this tool") {
		t.Errorf("per-tool limit not enforced: %s", resultText(r))
	}
	for i := range 9 {
		if r := call("pp_list_pets", map[string]any{}); r.IsError {
			t.Fatalf("list call %d: %s", i+1, resultText(r))
		}
	}
	if r := call("pp_list_pets", map[string]any{}); !r.IsError || !strings.Contains(resultText(r), "10/min for this API") {
		t.Errorf("API limit not enforced: %s", resultText(r))
	}
	if strings.Contains(stderr.String(), "not enforced") {
		t.Errorf("stale warning:\n%s", stderr)
	}
}

// serve --http: the Streamable HTTP transport on loopback with a bearer
// token kept in an owner-only file, reused across restarts (SPEC T15).
func TestServeHTTP(t *testing.T) {
	up := newUpstream(t)
	p := setupAPI(t, up.URL)
	stderr := &syncBuf{}
	start := func(newToken bool) (string, context.CancelFunc, chan error) {
		ctx, cancel := context.WithCancel(t.Context())
		urls := make(chan string, 1)
		done := make(chan error, 1)
		d := serveDeps(isolate.Parse)
		d.stderr = stderr
		go func() {
			done <- serveAPI(ctx, serveSettings{paths: p, name: "pets", http: "127.0.0.1:0", newToken: newToken,
				ready: func(u string) { urls <- u }}, d)
		}()
		select {
		case u := <-urls:
			return u, cancel, done
		case err := <-done:
			cancel()
			t.Fatalf("serve --http: %v\n%s", err, stderr)
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("serve --http didn't start")
		}
		return "", cancel, done
	}

	endpoint, stop, done := start(false)
	tokenPath, _ := p.HTTPToken("pets")
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(raw))
	if len(token) < 40 {
		t.Fatalf("token too short: %d characters", len(token))
	}
	if fi, _ := os.Stat(tokenPath); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v", fi.Mode().Perm())
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	tr := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &http.Client{Transport: bearerRT{token}}}
	cs, err := client.Connect(t.Context(), tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "pp_list_pets", Arguments: map[string]any{}}); err != nil || r.IsError {
		t.Fatalf("call over HTTP: %v %s", err, resultText(r))
	}
	_ = cs.Close()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %v %v", resp, err)
	}
	stop()
	if err := <-done; err != nil {
		t.Errorf("serve returned %v", err)
	}

	_, stop, done = start(false)
	if again, _ := os.ReadFile(tokenPath); strings.TrimSpace(string(again)) != token {
		t.Error("token changed on restart")
	}
	stop()
	if err := <-done; err != nil {
		t.Errorf("serve returned %v", err)
	}
	_, stop, done = start(true)
	if rotated, _ := os.ReadFile(tokenPath); strings.TrimSpace(string(rotated)) == token {
		t.Error("--new-token kept the old token")
	}
	stop()
	if err := <-done; err != nil {
		t.Errorf("serve returned %v", err)
	}

	if log := stderr.String(); strings.Contains(log, token) || !strings.Contains(log, tokenPath) {
		t.Errorf("stderr must name the token file and never show the token:\n%s", log)
	}
}

func TestServeHTTPRefusesNonLoopback(t *testing.T) {
	p := setupAPI(t, "https://pets.example.test")
	d := serveDeps(isolate.Parse)
	d.stderr = &bytes.Buffer{}
	err := serveAPI(t.Context(), serveSettings{paths: p, name: "pets", http: "0.0.0.0:8931"}, d)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Errorf("err = %v", err)
	}
}

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// A configured credential resolves to a protocol-neutral invoke.Credential,
// and its hidden name is reported; oauth2 resolves to a bearer token source
// whose client secret is redacted.
func TestBuildCredential(t *testing.T) {
	d := runDeps{resolver: &secrets.Resolver{LookupEnv: func(k string) (string, bool) {
		if k == "PETS_KEY" {
			return "test-token-123", true
		}
		return "", false
	}}}
	cfg := &config.API{Name: "pets", Protocol: "rest",
		Auth: config.Auth{Type: config.AuthHeader, Header: "X-Api-Key", Secret: "env:PETS_KEY"}}
	cred, hidden, err := buildCredential(cfg, d.resolver, d)
	if err != nil {
		t.Fatalf("buildCredential: %v", err)
	}
	if cred.Type != "header" || cred.Header != "X-Api-Key" || cred.Secret != "test-token-123" {
		t.Errorf("cred = %+v", cred)
	}
	if len(hidden) != 1 || hidden[0] != "X-Api-Key" {
		t.Errorf("hidden = %v", hidden)
	}
}

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
