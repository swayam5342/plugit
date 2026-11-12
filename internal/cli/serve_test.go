package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/openapi"
	"github.com/swayam5342/plugit/internal/secrets"
)

const petsYAML = `version: 1
name: pets
source: {url: https://example.test/openapi.yaml}
base_url: %s
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
// own server is an attacker host: serve must only ever use base_url.
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

func writeSnapshot(t *testing.T, p config.Paths, content string) {
	t.Helper()
	snap, _ := p.SpecSnapshot("pets")
	if err := config.WriteFileAtomic(snap, []byte(content)); err != nil {
		t.Fatal(err)
	}
}

// countingParse wraps the real child-process parser and counts calls.
func countingParse(n *atomic.Int32) func(context.Context, []byte, openapi.Options) (*model.Manifest, error) {
	return func(ctx context.Context, data []byte, opts openapi.Options) (*model.Manifest, error) {
		n.Add(1)
		return isolate.Parse(ctx, data, opts)
	}
}

func serveDeps(parse func(context.Context, []byte, openapi.Options) (*model.Manifest, error)) runDeps {
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

	// Read-only mode (the default): POST /pets is listed in the toolset but
	// not offered. Overrides and the prefix apply.
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

	noParse := func(context.Context, []byte, openapi.Options) (*model.Manifest, error) {
		t.Error("warm start re-parsed the spec")
		return nil, errors.New("unexpected parse")
	}
	cs, _ = startServe(t, serveSettings{paths: p, name: "pets"}, serveDeps(noParse))
	if got := toolNames(t, cs); len(got) != 2 {
		t.Errorf("warm start tools = %v", got)
	}

	// A changed snapshot invalidates the cache: re-parse once and rewrite it.
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
	// A denied tool is not listed at all.
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
