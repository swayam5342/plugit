package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/openapi"
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
func pickKeys(got *pickInput, keys ...string) func(context.Context, pickInput) (config.Toolset, error) {
	return func(_ context.Context, in pickInput) (config.Toolset, error) {
		if got != nil {
			*got = in
		}
		ts := config.Toolset{Tools: map[string]config.ToolOverride{}}
		for _, k := range keys {
			ts.Tools[k] = config.ToolOverride{}
		}
		return ts, nil
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

	// The spec's single server is offered for confirmation before pinning (T3).
	if len(asked) != 1 || !strings.Contains(asked[0], up.URL) {
		t.Errorf("questions = %q", asked)
	}
	// The picker sees every operation, named with the prefix.
	if len(in.tools) != 3 || !slices.ContainsFunc(in.tools, func(t model.Tool) bool { return t.Name == "pp_createPet" }) {
		t.Errorf("picker input tools = %v", len(in.tools))
	}

	cfg, err := config.LoadAPI(p, "pets")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != up.URL || cfg.Prefix != "pp" || cfg.PolicyMode() != policy.ReadOnly || cfg.Source.URL != specPath {
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
	for _, want := range []string{"2 tools", "≈", "plugit install pets --client"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("summary should mention %q:\n%s", want, out)
		}
	}

	// The saved API serves straight from the cache.
	cs, _ := startServe(t, serveSettings{paths: p, name: "pets"}, serveDeps(func(context.Context, []byte, openapi.Options) (*model.Manifest, error) {
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
	if !cfg.Network.AllowPrivate || cfg.BaseURL != up.URL {
		t.Errorf("network/base = %+v %s", cfg.Network, cfg.BaseURL)
	}
	// Same selection rules as run: the mode and the filters.
	if ts := cfg.Toolsets["default"]; len(ts.Tools) != 1 || !ts.Tools["GET /pets/{id}"].IsZero() {
		t.Errorf("toolset = %+v", ts)
	}
	// The yaml never contains a resolved secret, only references.
	raw, _ := os.ReadFile(must(p.APIConfig("pets")))
	if strings.Contains(string(raw), runSecret) || !strings.Contains(string(raw), "env:PETS_KEY") {
		t.Errorf("plugit.yaml:\n%s", raw)
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
		"base URL declined":                   {good, func(_ *addSettings, d *addDeps) { d.confirm = no }, "--base-url"},
		"no terminal to confirm the base URL": {good, func(_ *addSettings, d *addDeps) { d.confirm = nil }, "--base-url"},
		"several servers":                     {twoServers, nil, "--base-url"},
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
