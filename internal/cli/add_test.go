package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
)

func addEnv(t *testing.T) (config.Paths, addDeps, *bytes.Buffer) {
	t.Helper()
	p := config.NewPaths(t.TempDir())
	out := &bytes.Buffer{}
	return p, addDeps{
		paths:   p,
		out:     out,
		stderr:  &bytes.Buffer{},
		parse:   isolate.Parse,
		info:    BuildInfo{Version: "v0.0.0-test"},
		confirm: yes,
	}, out
}

func addSettingsFor(spec string, mut func(*addSettings)) addSettings {
	s := addSettings{name: "pets", spec: spec, mode: policy.ReadOnly, maxTools: defaultMaxTools}
	if mut != nil {
		mut(&s)
	}
	return s
}

// pickKeys is a fake picker that selects the given operation keys.
func pickKeys(got *pickInput, keys ...string) func(context.Context, pickInput) (pickResult, error) {
	return func(_ context.Context, in pickInput) (pickResult, error) {
		if got != nil {
			*got = in
		}
		ts := config.Toolset{Tools: map[string]config.ToolOverride{}}
		for _, k := range keys {
			ts.Tools[k] = config.ToolOverride{}
		}
		return pickResult{toolset: ts}, nil
	}
}

// The mode set in the picker (m) is the one saved.
func TestAddSavesPickerMode(t *testing.T) {
	up := newUpstream(t)
	p, d, _ := addEnv(t)
	var in pickInput
	pick := pickKeys(&in, "GET /pets", "POST /pets")
	d.pick = func(ctx context.Context, got pickInput) (pickResult, error) {
		r, err := pick(ctx, got)
		r.mode = policy.ConfirmWrites
		return r, err
	}
	d.confirm = func(string) (bool, error) { return true, nil }
	specPath := writeSpec(t, fmt.Sprintf(petSpec, up.URL))
	if err := addAPI(t.Context(), addSettingsFor(specPath, nil), d); err != nil {
		t.Fatal(err)
	}
	if in.mode != policy.ReadOnly {
		t.Errorf("picker started in %q, want the --mode default read-only", in.mode)
	}
	cfg, err := config.LoadAPI(p, "pets")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Policy.Mode != string(policy.ConfirmWrites) {
		t.Errorf("saved policy.mode = %q, want confirm-writes", cfg.Policy.Mode)
	}
}

func TestAddWithPicker(t *testing.T) {
	up := newUpstream(t)
	p, d, out := addEnv(t)
	var in pickInput
	d.pick = pickKeys(&in, "GET /pets", "GET /pets/{id}")
	specPath := writeSpec(t, fmt.Sprintf(petSpec, up.URL))
	var asked []string
	d.confirm = func(q string) (bool, error) { asked = append(asked, q); return true, nil }
	if err := addAPI(t.Context(), addSettingsFor(specPath, func(s *addSettings) { s.prefix = "pp" }), d); err != nil {
		t.Fatal(err)
	}

	if len(asked) != 1 || !strings.Contains(asked[0], up.URL) {
		t.Errorf("questions = %q", asked)
	}
	if o := out.String(); !strings.Contains(o, "plugit install pets ") || strings.Contains(o, "--client claude-desktop") {
		t.Errorf("summary:\n%s", o)
	}
	if len(in.tools) != 3 || !slices.ContainsFunc(in.tools, func(t model.Tool) bool { return t.Name == "pp_createPet" }) {
		t.Errorf("picker input tools = %v", len(in.tools))
	}

	cfg, err := config.LoadAPI(p, "pets")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != up.URL || cfg.Prefix != "pp" || cfg.PolicyMode() != policy.ReadOnly || cfg.Source.URL != specPath {
		t.Errorf("config = %+v", cfg)
	}
	if len(cfg.Source.SHA256) != 64 {
		t.Errorf("source sha256 = %q", cfg.Source.SHA256)
	}
	if ts := cfg.Toolsets["default"]; len(ts.Tools) != 2 {
		t.Errorf("default toolset = %+v", ts)
	}
	for _, f := range []func(string) (string, error){p.SpecSnapshot, p.Manifest} {
		path, _ := f("pets")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing %s", path)
		}
	}
	for _, want := range []string{"2 tools", "≈", "plugit install pets "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("summary should mention %q:\n%s", want, out)
		}
	}

	cs, _ := startServe(t, serveSettings{paths: p, name: "pets"}, serveDeps(func(context.Context, []byte, string, ingest.Options) (*model.Manifest, error) {
		t.Error("serve re-parsed right after add")
		return nil, fmt.Errorf("unexpected")
	}))
	if got := toolNames(t, cs); !slices.Equal(got, []string{"pp_getPet", "pp_listPets"}) {
		t.Errorf("served tools = %v", got)
	}
}

func TestAddFromRun(t *testing.T) {
	up := newUpstream(t)
	p, d, _ := addEnv(t)
	specPath := writeSpec(t, fmt.Sprintf(petSpec, "https://attacker.example.test"))
	err := addAPI(t.Context(), addSettingsFor(specPath, func(s *addSettings) {
		s.fromRun = true
		s.baseURL = up.URL
		s.allowPrivate = true
		s.headers = []string{"X-Api-Key=env:PETS_KEY"}
		s.fixed = []string{"tenant=literal:acme", "sig=keyring:plugit/pets-sig"}
		s.paths = []string{"/pets/*"}
	}), d)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadAPI(p, "pets")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Type != config.AuthHeader || cfg.Auth.Header != "X-Api-Key" || cfg.Auth.Secret != "env:PETS_KEY" {
		t.Errorf("auth = %+v", cfg.Auth)
	}
	if len(cfg.Fixed) != 2 || cfg.Fixed[0].Value != "literal:acme" || cfg.Fixed[1].Value != "keyring:plugit/pets-sig" {
		t.Errorf("fixed = %+v", cfg.Fixed)
	}
	if !cfg.Network.AllowPrivate || cfg.Endpoint != up.URL {
		t.Errorf("network/base = %+v %s", cfg.Network, cfg.Endpoint)
	}
	if ts := cfg.Toolsets["default"]; len(ts.Tools) != 1 || !ts.Tools["GET /pets/{id}"].IsZero() {
		t.Errorf("toolset = %+v", ts)
	}
	raw, _ := os.ReadFile(must(p.APIConfig("pets")))
	if strings.Contains(string(raw), runSecret) || !strings.Contains(string(raw), "env:PETS_KEY") {
		t.Errorf("plugit.yaml:\n%s", raw)
	}
}

// add prints the manifest's warnings (operations that became no tool, and
// the like) to stderr for every protocol, since no other command shows
// them; stdout, which carries the summary, doesn't change.
func TestAddPrintsManifestWarnings(t *testing.T) {
	restSpec := fmt.Sprintf(petSpec, "https://api.example.test") + `    trace:
      operationId: tracePets
      responses: {'200': {description: ok}}
`
	cases := []struct {
		name, spec, want string
	}{
		{"rest", writeSpec(t, restSpec), "warning: TRACE /pets/{id} skipped"},
		{"graphql", writeSDL(t, gqlSDL+"type Subscription { onEvent: String }"), `warning: subscription "onEvent": subscriptions aren't supported yet`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, d, out := addEnv(t)
			if err := addAPI(t.Context(), addSettingsFor(tc.spec, func(s *addSettings) {
				s.fromRun, s.baseURL = true, "https://api.example.test/graphql"
			}), d); err != nil {
				t.Fatal(err)
			}
			if stderr := d.stderr.(*bytes.Buffer).String(); !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want a line starting %q", stderr, tc.want)
			}
			if strings.Contains(out.String(), "warning") {
				t.Errorf("stdout carries the warnings: %s", out)
			}
		})
	}
}

func must(s string, err error) string {
	if err != nil {
		panic(err)
	}
	return s
}

func TestAddRefuses(t *testing.T) {
	up := newUpstream(t)
	good := fmt.Sprintf(petSpec, up.URL)
	twoServers := strings.Replace(good, "servers:\n  - url: "+up.URL, "servers:\n  - url: https://a.example.test\n  - url: https://b.example.test", 1)
	cases := map[string]struct {
		spec string
		mut  func(*addSettings, *addDeps)
		want string
	}{
		"invalid name":                        {good, func(s *addSettings, _ *addDeps) { s.name = "Bad Name" }, "invalid API name"},
		"base URL declined":                   {good, func(_ *addSettings, d *addDeps) { d.confirm = no }, "--endpoint"},
		"no terminal to confirm the base URL": {good, func(_ *addSettings, d *addDeps) { d.confirm = nil }, "--endpoint"},
		"several servers":                     {twoServers, nil, "--endpoint"},
		"no picker without a terminal":        {good, func(_ *addSettings, d *addDeps) { d.pick = nil }, "--from-run"},
		"picker selects nothing":              {good, func(_ *addSettings, d *addDeps) { d.pick = pickKeys(nil) }, "no tools"},
		"literal secret header": {good, func(s *addSettings, _ *addDeps) {
			s.fromRun, s.headers = true, []string{"X-Api-Key=literal:" + runSecret}
		}, "env: or keyring:"},
		"too many for --from-run": {good, func(s *addSettings, _ *addDeps) { s.fromRun, s.maxTools = true, 1 }, "--max-tools"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p, d, _ := addEnv(t)
			d.pick = pickKeys(nil, "GET /pets")
			s := addSettingsFor(writeSpec(t, tc.spec), nil)
			if tc.mut != nil {
				tc.mut(&s, &d)
			}
			err := addAPI(t.Context(), s, d)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
			if strings.Contains(err.Error(), runSecret) {
				t.Errorf("error leaks the secret: %v", err)
			}
			if dir, err := p.APIDir("pets"); err == nil {
				if _, err := os.Stat(dir); err == nil {
					t.Errorf("%s created despite the error", dir)
				}
			}
		})
	}

	t.Run("already exists", func(t *testing.T) {
		p := setupAPI(t, up.URL)
		_, d, _ := addEnv(t)
		d.paths = p
		d.pick = pickKeys(nil, "GET /pets")
		err := addAPI(t.Context(), addSettingsFor(writeSpec(t, good), nil), d)
		if err == nil || !strings.Contains(err.Error(), "plugit edit") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestAddAllowInsecure(t *testing.T) {
	specPath := writeSpec(t, fmt.Sprintf(petSpec, "https://attacker.example.test"))
	settings := func(insecure bool) addSettings {
		return addSettingsFor(specPath, func(s *addSettings) {
			s.fromRun = true
			s.baseURL = "http://pets.example.test"
			s.headers = []string{"X-Api-Key=env:PETS_KEY"}
			s.allowInsecure = insecure
		})
	}
	p, d, _ := addEnv(t)
	if err := addAPI(t.Context(), settings(false), d); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("plain http with a credential was saved without the switch: %v", err)
	}
	if err := addAPI(t.Context(), settings(true), d); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadAPI(p, "pets")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Network.AllowInsecure || cfg.Network.AllowPrivate {
		t.Errorf("network = %+v, want only allow_insecure", cfg.Network)
	}
}

const swaggerSpec = `swagger: '2.0'
info: {title: Pets, version: '1'}
host: %s
schemes: [http]
paths:
  /pets:
    get:
      operationId: listPets
      responses: {'200': {description: ok}}
%s`

// Regression: a Swagger 2.0 spec is added with source.format openapi (the
// ingest format that parsed it; "swagger" is no format name, so plugit.yaml
// would be invalid and every forced re-parse would fail). sync and the
// stale-cache re-parse then force openapi and work.
func TestAddSwagger2(t *testing.T) {
	up := newUpstream(t)
	host := strings.TrimPrefix(up.URL, "http://")
	p, d, _ := addEnv(t)
	specPath := writeSpec(t, fmt.Sprintf(swaggerSpec, host, ""))
	if err := addAPI(t.Context(), addSettingsFor(specPath, func(s *addSettings) {
		s.fromRun, s.baseURL, s.allowPrivate = true, up.URL, true
	}), d); err != nil {
		t.Fatalf("add a Swagger 2.0 spec: %v", err)
	}
	cfg, err := config.LoadAPI(p, "pets")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source.Format != "openapi" || cfg.Protocol != string(model.REST) {
		t.Errorf("saved protocol/format = %q/%q, want rest/openapi", cfg.Protocol, cfg.Source.Format)
	}

	s := syncDeps(&bytes.Buffer{})
	s.info, s.yes, s.addNew = d.info, true, true
	s.spec = writeSpec(t, fmt.Sprintf(swaggerSpec, host, "  /owners:\n    get:\n      operationId: listOwners\n      responses: {'200': {description: ok}}\n"))
	if err := syncAPI(t.Context(), p, "pets", s); err != nil {
		t.Fatalf("sync a Swagger 2.0 spec: %v", err)
	}

	cache, _ := p.Manifest("pets")
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	var parses atomic.Int32
	cs, _ := startServe(t, serveSettings{paths: p, name: "pets"}, serveDeps(countingParse(&parses)))
	if got := toolNames(t, cs); !slices.Equal(got, []string{"listOwners", "listPets"}) {
		t.Errorf("served tools after a stale-cache re-parse = %v", got)
	}
	if parses.Load() != 1 {
		t.Errorf("parses = %d, want 1 (the stale-cache re-parse)", parses.Load())
	}
}
