package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
	"github.com/swayam5342/plugit/internal/secrets"
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
	b, err := os.ReadFile("../protocol/graphql/testdata/user.introspection.json")
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
