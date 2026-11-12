package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/secrets"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestRunSettingsDefaults(t *testing.T) {
	s, err := resolveRunSettings(runFlags{spec: "api.json"}, nil, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.mode != policy.ReadOnly || s.maxTools != 40 || s.timeout != 30*time.Second || s.allowPrivate {
		t.Errorf("defaults = %+v", s)
	}
}

func TestRunSettingsPrecedence(t *testing.T) {
	e := env(map[string]string{
		"PLUGIT_SPEC": "from-env.json", "PLUGIT_BASE_URL": "https://env.example.test",
		"PLUGIT_PREFIX": "envp", "PLUGIT_MODE": "full", "PLUGIT_MAX_TOOLS": "7",
		"PLUGIT_TIMEOUT": "5s", "PLUGIT_ALLOW_PRIVATE": "true", "PLUGIT_TAGS": "a,b",
	})
	s, err := resolveRunSettings(runFlags{}, nil, e)
	if err != nil {
		t.Fatal(err)
	}
	if s.spec != "from-env.json" || s.baseURL != "https://env.example.test" || s.prefix != "envp" ||
		s.mode != policy.Full || s.maxTools != 7 || s.timeout != 5*time.Second || !s.allowPrivate || !slices.Equal(s.tags, []string{"a", "b"}) {
		t.Errorf("env settings = %+v", s)
	}

	// Explicitly set flags beat env vars.
	f := runFlags{spec: "flag.json", baseURL: "https://flag.example.test", mode: "read-only", maxTools: 3, tags: []string{"c"}}
	s, err = resolveRunSettings(f, map[string]bool{"base-url": true, "mode": true, "max-tools": true, "tags": true}, e)
	if err != nil {
		t.Fatal(err)
	}
	if s.spec != "flag.json" || s.baseURL != "https://flag.example.test" || s.mode != policy.ReadOnly || s.maxTools != 3 || !slices.Equal(s.tags, []string{"c"}) {
		t.Errorf("flag settings = %+v", s)
	}
	if s.prefix != "envp" {
		t.Errorf("unset flags fall back to env, prefix = %q", s.prefix)
	}
}

func TestRunSettingsErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		f   runFlags
		set map[string]bool
		e   map[string]string
	}{
		"no spec":        {runFlags{}, nil, nil},
		"bad mode":       {runFlags{spec: "x", mode: "yolo"}, map[string]bool{"mode": true}, nil},
		"bad env mode":   {runFlags{spec: "x"}, nil, map[string]string{"PLUGIT_MODE": "readonly"}},
		"bad max tools":  {runFlags{spec: "x"}, nil, map[string]string{"PLUGIT_MAX_TOOLS": "lots"}},
		"bad timeout":    {runFlags{spec: "x"}, nil, map[string]string{"PLUGIT_TIMEOUT": "soon"}},
		"zero max tools": {runFlags{spec: "x", maxTools: 0}, map[string]bool{"max-tools": true}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := resolveRunSettings(tc.f, tc.set, env(tc.e)); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestParseCredentialFlags(t *testing.T) {
	h, err := parseHeaderFlags([]string{"X-Api-Key=env:PETSTORE_KEY", "Authorization=keyring:plugit/gh"})
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[0].name != "X-Api-Key" || h[0].ref.Var != "PETSTORE_KEY" || h[1].ref.Scheme != secrets.Keyring {
		t.Errorf("headers = %+v", h)
	}
	fx, err := parseFixedFlags([]string{"api_key=env:K", "version=literal:2022-11-28"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fx) != 2 || fx[1].ref.Literal != "2022-11-28" {
		t.Errorf("fixed = %+v", fx)
	}
}

func TestCredentialFlagsRejectLiteralSecrets(t *testing.T) {
	const pasted = "test-token-123"
	for _, bad := range []string{"X-Key=" + pasted, "X-Key=literal:" + pasted, "=env:K", "X-Key", "Bad Header=env:K", "X-Key=env:"} {
		_, err := parseHeaderFlags([]string{bad})
		if err == nil {
			t.Errorf("--header %q accepted", bad)
			continue
		}
		if strings.Contains(err.Error(), pasted) {
			t.Errorf("--header error echoes the pasted value: %v", err)
		}
	}
	for _, bad := range []string{"api_key=" + pasted, "=literal:x", "k"} {
		_, err := parseFixedFlags([]string{bad})
		if err == nil {
			t.Errorf("--fixed %q accepted", bad)
			continue
		}
		if strings.Contains(err.Error(), pasted) {
			t.Errorf("--fixed error echoes the pasted value: %v", err)
		}
	}
}

func TestChooseBaseURL(t *testing.T) {
	ok := []struct {
		flag        string
		servers     []string
		credentials bool
		want        string
	}{
		{"", []string{"https://api.example.test/v1"}, false, "https://api.example.test/v1"},
		{"https://pinned.example.test", []string{"https://api.example.test"}, true, "https://pinned.example.test"},
		{"https://pinned.example.test/", nil, false, "https://pinned.example.test/"},
	}
	for _, tc := range ok {
		u, err := chooseBaseURL(tc.flag, tc.servers, tc.credentials)
		if err != nil || u.String() != tc.want {
			t.Errorf("chooseBaseURL(%q, %v, %v) = %v, %v; want %s", tc.flag, tc.servers, tc.credentials, u, err, tc.want)
		}
	}
	bad := []struct {
		flag        string
		servers     []string
		credentials bool
	}{
		{"", nil, false},                                          // no server
		{"", []string{"/api/v3"}, false},                          // relative
		{"", []string{"https://a.test", "https://b.test"}, false}, // several: never guess
		{"", []string{"https://api.example.test"}, true},          // credentials need an explicit --base-url (T3)
		{"ftp://x.test", nil, false},
		{"https://user:pw@x.test", nil, false},
		{"https://x.test/?a=1", nil, false},
		{"x.test", nil, false},
	}
	for _, tc := range bad {
		if _, err := chooseBaseURL(tc.flag, tc.servers, tc.credentials); err == nil {
			t.Errorf("chooseBaseURL(%q, %v, %v) succeeded", tc.flag, tc.servers, tc.credentials)
		}
	}
}

func TestChooseBaseURLExplainsCredentialRule(t *testing.T) {
	_, err := chooseBaseURL("", []string{"https://attacker.example.test"}, true)
	if err == nil || !strings.Contains(err.Error(), "--base-url") || !strings.Contains(err.Error(), "attacker.example.test") {
		t.Errorf("err = %v", err)
	}
}

func mtool(key model.OpKey, tags ...string) model.Tool {
	return model.Tool{Key: key, Name: strings.NewReplacer(" ", "_", "/", "_", "{", "", "}", "").Replace(string(key)),
		Method: key.Method(), Path: key.Path(), Tags: tags, Safety: model.SafetyForMethod(key.Method())}
}

func keys(ts []model.Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, string(t.Key))
	}
	return out
}

func TestSelectTools(t *testing.T) {
	all := []model.Tool{
		mtool("GET /pets", "pet"), mtool("POST /pets", "pet"), mtool("GET /pets/{id}", "pet"),
		mtool("GET /store/inventory", "store"), mtool("DELETE /store/order/{id}", "Store"), mtool("GET /user/login", "user"),
	}
	tests := []struct {
		name                 string
		mode                 policy.Mode
		tags, methods, paths []string
		want                 []string
	}{
		{"read-only default", policy.ReadOnly, nil, nil, nil, []string{"GET /pets", "GET /pets/{id}", "GET /store/inventory", "GET /user/login"}},
		{"tags case-insensitive", policy.Full, []string{"store"}, nil, nil, []string{"GET /store/inventory", "DELETE /store/order/{id}"}},
		{"methods", policy.Full, nil, []string{"post", "DELETE"}, nil, []string{"POST /pets", "DELETE /store/order/{id}"}},
		{"path prefix", policy.ReadOnly, nil, nil, []string{"/pets"}, []string{"GET /pets", "GET /pets/{id}"}},
		{"path glob", policy.ReadOnly, nil, nil, []string{"/pets/*"}, []string{"GET /pets/{id}"}},
		{"mode still applies", policy.ReadOnly, nil, []string{"POST"}, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := keys(selectTools(all, tc.mode, tc.tags, tc.methods, tc.paths))
			if !slices.Equal(got, tc.want) {
				t.Errorf("selected %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMaxToolsGuard(t *testing.T) {
	var many []model.Tool
	for i := range 100 {
		tag := "repos"
		if i%4 == 0 {
			tag = "issues"
		}
		many = append(many, mtool(model.NewOpKey("GET", "/x/"+strings.Repeat("a", i+1)), tag))
	}
	err := checkToolCount(many, 40)
	if !errors.Is(err, errTooManyTools) {
		t.Fatalf("err = %v, want errTooManyTools", err)
	}
	for _, want := range []string{"100", "40", "--tags", "repos", "issues", "plugit add"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("hint %q missing from: %v", want, err)
		}
	}
	if err := checkToolCount(many[:40], 40); err != nil {
		t.Errorf("exactly at the limit: %v", err)
	}
	if err := checkToolCount(nil, 40); err == nil {
		t.Error("zero tools must be an error")
	}
}
