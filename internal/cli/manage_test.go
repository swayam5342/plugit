package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/clients"
	"github.com/swayam5342/plugit/internal/config"
)

func TestList(t *testing.T) {
	p := setupAPI(t, "https://pets.example.test")
	// A broken API is listed with its error, not skipped silently.
	dir, _ := p.APIDir("broken")
	if err := config.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	cfg, _ := p.APIConfig("broken")
	if err := config.WriteFileAtomic(cfg, []byte("version: 1\nbogus: true\n")); err != nil {
		t.Fatal(err)
	}
	// Stray entries that aren't APIs are ignored.
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := listAPIs(p, false, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"pets", "4", "read-only", "https://pets.example.test", "broken", "bogus"} {
		if !strings.Contains(s, want) {
			t.Errorf("list missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "notes.txt") {
		t.Errorf("stray file listed:\n%s", s)
	}

	out.Reset()
	if err := listAPIs(p, true, &out); err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Name     string         `json:"name"`
		BaseURL  string         `json:"base_url"`
		Mode     string         `json:"mode"`
		Toolsets map[string]int `json:"toolsets"`
		Error    string         `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if len(got) != 2 || got[1].Name != "pets" || got[1].Toolsets["default"] != 4 || got[0].Error == "" {
		t.Errorf("json = %+v", got)
	}

	// No APIs yet.
	out.Reset()
	if err := listAPIs(config.NewPaths(t.TempDir()), false, &out); err != nil || !strings.Contains(out.String(), "plugit add") {
		t.Errorf("empty list: %v %q", err, out.String())
	}
}

func TestRemove(t *testing.T) {
	p := setupAPI(t, "https://pets.example.test")
	d, cfgPath, out, _ := installEnv(t, p, "")
	d.confirm = yes
	if err := installAPI(t.Context(), installSettings{name: "pets", client: "claude-desktop"}, d); err != nil {
		t.Fatal(err)
	}
	out.Reset()

	if err := removeAPI(p, "pets", false, nil, d.env, out); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("remove without confirmation: %v", err)
	}
	if err := removeAPI(p, "pets", false, no, d.env, out); err == nil {
		t.Error("declined remove succeeded")
	}
	dir, _ := p.APIDir("pets")
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("declined remove deleted the API")
	}

	if err := removeAPI(p, "pets", false, yes, d.env, out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("API dir still there: %v", err)
	}
	// The client config still points at it: say so, with the fix.
	if !strings.Contains(out.String(), "plugit uninstall pets --client claude-desktop") {
		t.Errorf("no uninstall hint:\n%s", out)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Error("remove touched the client config")
	}
	if err := removeAPI(p, "pets", true, nil, d.env, out); err == nil {
		t.Error("removing a missing API succeeded")
	}
	if err := removeAPI(p, "../x", true, nil, d.env, out); err == nil {
		t.Error("invalid name accepted")
	}
}

func TestDoctor(t *testing.T) {
	up := newUpstream(t)
	p := setupAPI(t, up.URL) // allow_private: true, env:PETS_KEY
	d, _, _, _ := installEnv(t, p, "")
	d.confirm = yes
	if err := installAPI(t.Context(), installSettings{name: "pets", client: "claude-desktop"}, d); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	dd := doctorDeps{
		paths: p, out: &out, env: d.env,
		lookupEnv: func(string) (string, bool) { return "", false },
		exe:       func() (string, error) { return fakeExe, nil },
		lookPath:  d.lookPath,
		stat: func(path string) error {
			if path == fakeExe {
				return nil
			}
			_, err := os.Stat(path)
			return err
		},
		info: BuildInfo{Version: "v0.1.0"},
	}
	problems := runDoctor(dd)
	s := out.String()
	for _, want := range []string{
		"v0.1.0",
		"pets",
		"allow_private",                        // relaxed default
		"PETS_KEY is not set",                  // unresolvable credential
		"manifest",                             // cache not built yet (no serve has run)
		"Claude Desktop: \"pets\" serves pets", // installed client found
		"claude CLI not on PATH",
		"codex CLI not on PATH",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("doctor should mention %q:\n%s", want, s)
		}
	}
	// env: secrets come from the MCP client's env block, so a variable
	// missing from this shell is a warning, not a failure.
	if problems != 0 {
		t.Errorf("problems = %d, want 0 (warnings only)", problems)
	}
	if strings.Contains(s, runSecret) {
		t.Errorf("doctor printed a secret:\n%s", s)
	}

	out.Reset()
	dd.lookupEnv = func(k string) (string, bool) { return runSecret, k == "PETS_KEY" }
	runDoctor(dd)
	if strings.Contains(out.String(), runSecret) || !strings.Contains(out.String(), "env:PETS_KEY") {
		t.Errorf("credential line:\n%s", out.String())
	}

	// A client entry pointing at a plugit binary that no longer exists.
	out.Reset()
	dd.exe = func() (string, error) { return `C:\elsewhere\plugit.exe`, nil }
	dd.stat = func(string) error { return os.ErrNotExist }
	runDoctor(dd)
	if !strings.Contains(out.String(), "missing") {
		t.Errorf("stale client entry not reported:\n%s", out.String())
	}

	// An API whose config doesn't load is a failure.
	dir, _ := p.APIDir("broken")
	if err := config.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteFileAtomic(filepath.Join(dir, "plugit.yaml"), []byte("version: 1\nbogus: true\n")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if n := runDoctor(dd); n == 0 || !strings.Contains(out.String(), "bogus") {
		t.Errorf("broken config: problems=%d\n%s", n, out.String())
	}
}

func TestPlugitEntries(t *testing.T) {
	data := []byte(`{"mcpServers": {"a": {"command": "C:\\x\\plugit.exe", "args": ["serve", "pets"]}, "b": {"command": "npx"}, "c": {"command": "/bin/plugit", "args": ["run", "x"]}}}`)
	got, err := clients.PlugitEntries(data, clients.MCPServers)
	if err != nil || len(got) != 1 || got["a"].API != "pets" {
		t.Errorf("entries = %+v, %v", got, err)
	}
}
