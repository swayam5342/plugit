package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/swayam5342/plugit/internal/clients"
	"github.com/swayam5342/plugit/internal/config"
)

const fakeExe = `C:\Tools\plugit.exe`

type fakeRun struct {
	calls [][]string
	out   string
	err   error
}

func (f *fakeRun) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return []byte(f.out), f.err
}

func installEnv(t *testing.T, p config.Paths, existing string) (installDeps, string, *bytes.Buffer, *fakeRun) {
	t.Helper()
	appData := t.TempDir()
	if err := os.MkdirAll(filepath.Join(appData, "Claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(appData, "Claude", "claude_desktop_config.json")
	if existing != "" {
		if err := os.WriteFile(cfgPath, []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := &bytes.Buffer{}
	fr := &fakeRun{}
	return installDeps{
		paths:    p,
		out:      out,
		env:      clients.Env{GOOS: "windows", Home: t.TempDir(), AppData: appData},
		exe:      func() (string, error) { return fakeExe, nil },
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		run:      fr.run,
		now:      func() time.Time { return time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC) },
	}, cfgPath, out, fr
}

func yes(string) (bool, error) { return true, nil }
func no(string) (bool, error)  { return false, nil }

func TestInstallClaudeDesktop(t *testing.T) {
	p := setupAPI(t, "https://pets.example.test")
	d, cfgPath, out, _ := installEnv(t, p, `{"mcpServers": {"other": {"command": "npx"}}, "preferences": {"x": 1}}`)
	d.confirm = yes
	if err := installAPI(t.Context(), installSettings{name: "pets", client: "claude-desktop"}, d); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cfgPath)
	s := string(data)
	for _, want := range []string{`"other"`, `"preferences"`, `"pets"`, `C:\\Tools\\plugit.exe`, `"serve"`} {
		if !strings.Contains(s, want) {
			t.Errorf("config missing %s:\n%s", want, s)
		}
	}
	// No secrets or secret references ever go into client configs.
	if strings.Contains(s, "PETS_KEY") || strings.Contains(s, "env") {
		t.Errorf("client config mentions credentials:\n%s", s)
	}
	if !strings.Contains(out.String(), "+ ") || !strings.Contains(out.String(), "plugit-backup") || !strings.Contains(out.String(), "Restart") {
		t.Errorf("output should show the diff, the backup and a restart hint:\n%s", out)
	}

	// Installing again changes nothing and says so.
	out.Reset()
	if err := installAPI(t.Context(), installSettings{name: "pets", client: "claude-desktop"}, d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already") {
		t.Errorf("second install output: %s", out)
	}

	// Uninstall removes only our entry.
	if err := uninstallAPI(t.Context(), installSettings{name: "pets", client: "claude-desktop"}, d); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(cfgPath)
	if strings.Contains(string(data), `"pets"`) || !strings.Contains(string(data), `"other"`) {
		t.Errorf("after uninstall:\n%s", data)
	}
}

func TestInstallOptions(t *testing.T) {
	p := setupAPI(t, "https://pets.example.test")
	d, cfgPath, _, _ := installEnv(t, p, "")
	d.confirm = yes
	d.configDir = `D:\custom cfg`
	if err := installAPI(t.Context(), installSettings{name: "pets", client: "claude-desktop", toolset: "denied", as: "my-pets"}, d); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cfgPath)
	for _, want := range []string{`"my-pets"`, `"--toolset"`, `"denied"`, `"--config-dir"`, `D:\\custom cfg`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %s:\n%s", want, data)
		}
	}
}

func TestInstallRefuses(t *testing.T) {
	p := setupAPI(t, "https://pets.example.test")
	cases := map[string]struct {
		s        installSettings
		existing string
		confirm  func(string) (bool, error)
		mut      func(*installDeps)
		want     string
	}{
		"not confirmed":        {installSettings{name: "pets", client: "claude-desktop"}, "", no, nil, "nothing changed"},
		"no terminal":          {installSettings{name: "pets", client: "claude-desktop"}, "", nil, nil, "--yes"},
		"unknown API":          {installSettings{name: "nope", client: "claude-desktop"}, "", yes, nil, "plugit add"},
		"unknown toolset":      {installSettings{name: "pets", client: "claude-desktop", toolset: "x"}, "", yes, nil, "toolset"},
		"unknown client":       {installSettings{name: "pets", client: "emacs"}, "", yes, nil, "emacs"},
		"bad --as":             {installSettings{name: "pets", client: "claude-desktop", as: "a b"}, "", yes, nil, "--as"},
		"foreign entry":        {installSettings{name: "pets", client: "claude-desktop"}, `{"mcpServers":{"pets":{"command":"npx"}}}`, yes, nil, "--as"},
		"broken client config": {installSettings{name: "pets", client: "claude-desktop"}, `{"mcpServers": `, yes, nil, "not a JSON object"},
		"temporary binary": {installSettings{name: "pets", client: "claude-desktop"}, "", yes, func(d *installDeps) {
			d.exe = func() (string, error) { return filepath.Join(os.TempDir(), "go-build123", "exe", "plugit.exe"), nil }
		}, "go run"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d, cfgPath, _, _ := installEnv(t, p, tc.existing)
			d.confirm = tc.confirm
			if tc.mut != nil {
				tc.mut(&d)
			}
			err := installAPI(t.Context(), tc.s, d)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
			if data, _ := os.ReadFile(cfgPath); string(data) != tc.existing {
				t.Errorf("config changed despite the error:\n%s", data)
			}
		})
	}
}

func TestInstallClaudeCode(t *testing.T) {
	p := setupAPI(t, "https://pets.example.test")

	t.Run("CLI found", func(t *testing.T) {
		d, _, out, fr := installEnv(t, p, "")
		d.confirm = yes
		d.lookPath = func(string) (string, error) { return `C:\bin\claude.exe`, nil }
		if err := installAPI(t.Context(), installSettings{name: "pets", client: "claude-code"}, d); err != nil {
			t.Fatal(err)
		}
		want := []string{`C:\bin\claude.exe`, "mcp", "add", "--scope", "user", "pets", "--", fakeExe, "serve", "pets"}
		if len(fr.calls) != 1 || !slices.Equal(fr.calls[0], want) {
			t.Errorf("calls = %q", fr.calls)
		}
		if !strings.Contains(out.String(), "claude mcp add") {
			t.Errorf("command not shown: %s", out)
		}
		if err := uninstallAPI(t.Context(), installSettings{name: "pets", client: "claude-code"}, d); err != nil {
			t.Fatal(err)
		}
		if len(fr.calls) != 2 || !slices.Equal(fr.calls[1][1:], []string{"mcp", "remove", "--scope", "user", "pets"}) {
			t.Errorf("calls = %q", fr.calls)
		}
	})
	t.Run("CLI missing", func(t *testing.T) {
		d, _, out, fr := installEnv(t, p, "")
		d.confirm = yes
		if err := installAPI(t.Context(), installSettings{name: "pets", client: "claude-code"}, d); err != nil {
			t.Fatal(err)
		}
		if len(fr.calls) != 0 || !strings.Contains(out.String(), "claude mcp add --scope user pets -- ") {
			t.Errorf("should print the command: calls=%q out=%s", fr.calls, out)
		}
	})
	t.Run("CLI fails", func(t *testing.T) {
		d, _, _, fr := installEnv(t, p, "")
		d.confirm = yes
		d.lookPath = func(string) (string, error) { return `C:\bin\claude.exe`, nil }
		fr.out, fr.err = "MCP server pets already exists", errors.New("exit status 1")
		err := installAPI(t.Context(), installSettings{name: "pets", client: "claude-code"}, d)
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("batch shim with metacharacters", func(t *testing.T) {
		d, _, _, fr := installEnv(t, p, "")
		d.confirm = yes
		d.lookPath = func(string) (string, error) { return `C:\npm\claude.cmd`, nil }
		d.exe = func() (string, error) { return `C:\R&D\plugit.exe`, nil }
		if err := installAPI(t.Context(), installSettings{name: "pets", client: "claude-code"}, d); err == nil || len(fr.calls) != 0 {
			t.Errorf("err = %v, calls = %q", err, fr.calls)
		}
	})
}
