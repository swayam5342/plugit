package clients

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var entry = Entry{Command: `C:\Tools & More\plugit.exe`, Args: []string{"serve", "pets"}}

const desktopConfig = `{
  "mcpServers": {
    "other": {
      "command": "npx",
      "args": ["-y", "some-server"],
      "env": {"OTHER_TOKEN": "test-token-123"}
    }
  },
  "preferences": {
    "sidebarMode": "epitaxy",
    "big": 1e400,
    "bypass": {"25165159": false}
  },
  "coworkUserFilesPath": "C:\\Users\\me\\Claude"
}
`

// keysInOrder returns the top-level keys of a JSON object in file order.
func keysInOrder(t *testing.T, data []byte) []string {
	t.Helper()
	obj, err := parseObject(data)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range obj {
		out = append(out, m.Key)
	}
	return out
}

func TestInstallPreservesEverythingElse(t *testing.T) {
	out, err := InstallJSON([]byte(desktopConfig), MCPServers, "pets", entry)
	if err != nil {
		t.Fatal(err)
	}
	if got := keysInOrder(t, out); strings.Join(got, ",") != "mcpServers,preferences,coworkUserFilesPath" {
		t.Errorf("top-level order = %v", got)
	}
	s := string(out)
	// Values we don't own are kept verbatim, including numbers Go can't
	// represent and other servers' env blocks.
	for _, want := range []string{`1e400`, `"OTHER_TOKEN": "test-token-123"`, `"sidebarMode": "epitaxy"`, `"C:\\Users\\me\\Claude"`, `"25165159": false`} {
		if !strings.Contains(s, want) {
			t.Errorf("lost %s:\n%s", want, s)
		}
	}
	var parsed struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, s)
	}
	pets := parsed.MCPServers["pets"]
	if pets.Command != entry.Command || strings.Join(pets.Args, " ") != "serve pets" || pets.Env != nil {
		t.Errorf("entry = %+v", pets)
	}
	if _, ok := parsed.MCPServers["other"]; !ok {
		t.Error("other server removed")
	}
	// Our entry goes after the existing servers, and paths aren't
	// HTML-escaped (& stays &).
	if strings.Index(s, `"other"`) > strings.Index(s, `"pets"`) || !strings.Contains(s, `Tools & More`) {
		t.Errorf("entry placement or escaping:\n%s", s)
	}
	if !strings.HasSuffix(s, "}\n") {
		t.Error("missing trailing newline")
	}
}

func TestInstallIsIdempotentAndReplacesOwnEntry(t *testing.T) {
	once, err := InstallJSON([]byte(desktopConfig), MCPServers, "pets", entry)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := InstallJSON(once, MCPServers, "pets", entry)
	if err != nil || string(twice) != string(once) {
		t.Errorf("second install changed the file (err=%v)", err)
	}
	moved := Entry{Command: `D:\new\plugit.exe`, Args: []string{"serve", "pets", "--toolset", "readonly"}}
	out, err := InstallJSON(once, MCPServers, "pets", moved)
	if err != nil || !strings.Contains(string(out), `D:\\new\\plugit.exe`) || strings.Contains(string(out), `More`) {
		t.Errorf("own entry not replaced (err=%v):\n%s", err, out)
	}
}

func TestInstallCreatesConfig(t *testing.T) {
	for name, in := range map[string]string{"missing file": "", "whitespace": " \n", "empty object": "{}", "no servers": `{"theme":"dark"}`} {
		t.Run(name, func(t *testing.T) {
			out, err := InstallJSON([]byte(in), MCPServers, "pets", entry)
			if err != nil {
				t.Fatal(err)
			}
			var v struct {
				MCPServers map[string]any `json:"mcpServers"`
			}
			if err := json.Unmarshal(out, &v); err != nil || v.MCPServers["pets"] == nil {
				t.Errorf("err=%v out=%s", err, out)
			}
		})
	}
}

func TestInstallRefuses(t *testing.T) {
	foreign := `{"mcpServers": {"pets": {"command": "npx", "args": ["pets-server"]}}}`
	otherAPI := `{"mcpServers": {"pets": {"command": "C:\\plugit.exe", "args": ["serve", "github"]}}}`
	for name, tc := range map[string]struct {
		in   string
		want error
	}{
		"invalid JSON":          {`{"mcpServers": {`, ErrInvalidConfig},
		"comments":              {"// note\n{}", ErrInvalidConfig},
		"top-level array":       {`[]`, ErrInvalidConfig},
		"mcpServers not object": {`{"mcpServers": []}`, ErrInvalidConfig},
		"trailing data":         {`{} {}`, ErrInvalidConfig},
		"foreign entry":         {foreign, ErrNotOurs},
		"plugit for other API":  {otherAPI, ErrNotOurs},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := InstallJSON([]byte(tc.in), MCPServers, "pets", entry)
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestUninstall(t *testing.T) {
	installed, err := InstallJSON([]byte(desktopConfig), MCPServers, "pets", entry)
	if err != nil {
		t.Fatal(err)
	}
	out, removed, err := UninstallJSON(installed, MCPServers, "pets")
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if strings.Contains(string(out), `"pets"`) || !strings.Contains(string(out), `"other"`) || !strings.Contains(string(out), "1e400") {
		t.Errorf("uninstall result:\n%s", out)
	}
	if _, removed, err := UninstallJSON(out, MCPServers, "pets"); err != nil || removed {
		t.Errorf("second uninstall: removed=%v err=%v", removed, err)
	}
	// Someone else's entry of the same name is left alone.
	if _, _, err := UninstallJSON([]byte(`{"mcpServers": {"pets": {"command": "npx"}}}`), MCPServers, "pets"); !errors.Is(err, ErrNotOurs) {
		t.Errorf("foreign uninstall: err = %v", err)
	}
}

func TestIsPlugitEntry(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"command": "C:\\x\\plugit.exe", "args": ["serve", "pets"]}`:                               true,
		`{"command": "/usr/local/bin/plugit", "args": ["serve", "pets", "--toolset", "ro"]}`:        true,
		`{"command": "/usr/local/bin/PLUGIT.EXE", "args": ["--config-dir", "/c", "serve", "pets"]}`: true,
		`{"command": "plugit", "args": ["run", "https://x"]}`:                                       false,
		`{"command": "plugit", "args": ["serve", "github"]}`:                                        false,
		`{"command": "npx", "args": ["serve", "pets"]}`:                                             false,
		`"not an object"`: false,
	} {
		if got := isPlugitEntry(json.RawMessage(raw), "pets"); got != want {
			t.Errorf("isPlugitEntry(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestDiff(t *testing.T) {
	d := Diff([]byte("a\nb\nc\n"), []byte("a\nB\nc\nd\n"))
	for _, want := range []string{"- b", "+ B", "+ d"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff missing %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, "- a") || strings.Contains(d, "+ a") {
		t.Errorf("unchanged line reported:\n%s", d)
	}
	if Diff([]byte("x\n"), []byte("x\n")) != "" {
		t.Error("identical inputs produce a diff")
	}
}

// VS Code keeps servers under "servers" and wants an explicit stdio type.
func TestVSCodeFormat(t *testing.T) {
	in := `{"servers": {"other": {"type": "http", "url": "https://x.example.test"}}, "inputs": []}`
	out, err := InstallJSON([]byte(in), VSCodeServers, "pets", entry)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Servers map[string]map[string]any `json:"servers"`
		Inputs  []any                     `json:"inputs"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	pets := parsed.Servers["pets"]
	if pets["type"] != "stdio" || pets["command"] != entry.Command || parsed.Servers["other"] == nil || parsed.Inputs == nil {
		t.Errorf("unexpected result:\n%s", out)
	}
	if strings.Contains(string(out), "mcpServers") {
		t.Errorf("wrote mcpServers into a VS Code config:\n%s", out)
	}
	if got, err := PlugitEntries(out, VSCodeServers); err != nil || got["pets"].API != "pets" {
		t.Errorf("entries = %v, %v", got, err)
	}
	// The same file read as mcpServers has no plugit entries.
	if got, _ := PlugitEntries(out, MCPServers); len(got) != 0 {
		t.Errorf("found entries under the wrong key: %v", got)
	}
	back, removed, err := UninstallJSON(out, VSCodeServers, "pets")
	if err != nil || !removed || strings.Contains(string(back), `"pets"`) || !strings.Contains(string(back), `"other"`) {
		t.Errorf("uninstall = %v, %v:\n%s", removed, err, back)
	}
}
