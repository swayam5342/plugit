package clients

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseClient(t *testing.T) {
	for _, s := range []string{"claude-desktop", "claude-code", "cursor"} {
		if c, err := ParseClient(s); err != nil || string(c) != s {
			t.Errorf("ParseClient(%q) = %q, %v", s, c, err)
		}
	}
	if _, err := ParseClient("vscode"); err == nil || !strings.Contains(err.Error(), "not supported yet") {
		t.Errorf("vscode: %v", err)
	}
	if _, err := ParseClient("emacs"); err == nil || !strings.Contains(err.Error(), "claude-desktop") {
		t.Errorf("unknown client: %v", err)
	}
}

func TestConfigPath(t *testing.T) {
	cases := []struct {
		c    Client
		env  Env
		want string
	}{
		{ClaudeDesktop, Env{GOOS: "windows", Home: `C:\Users\me`, AppData: `C:\Users\me\AppData\Roaming`}, filepath.Join(`C:\Users\me\AppData\Roaming`, "Claude", "claude_desktop_config.json")},
		{ClaudeDesktop, Env{GOOS: "darwin", Home: "/Users/me"}, filepath.Join("/Users/me", "Library", "Application Support", "Claude", "claude_desktop_config.json")},
		{ClaudeDesktop, Env{GOOS: "linux", Home: "/home/me", ConfigHome: "/home/me/.config"}, filepath.Join("/home/me/.config", "Claude", "claude_desktop_config.json")},
		{Cursor, Env{GOOS: "windows", Home: `C:\Users\me`}, filepath.Join(`C:\Users\me`, ".cursor", "mcp.json")},
		{Cursor, Env{GOOS: "linux", Home: "/home/me"}, filepath.Join("/home/me", ".cursor", "mcp.json")},
	}
	for _, tc := range cases {
		got, err := ConfigPath(tc.c, tc.env)
		if err != nil || got != tc.want {
			t.Errorf("ConfigPath(%s, %s) = %q, %v; want %q", tc.c, tc.env.GOOS, got, err, tc.want)
		}
	}
	if _, err := ConfigPath(ClaudeCode, Env{GOOS: "linux", Home: "/h"}); err == nil {
		t.Error("claude-code has no file plugit edits")
	}
	if _, err := ConfigPath(ClaudeDesktop, Env{GOOS: "windows"}); err == nil {
		t.Error("missing AppData accepted")
	}
}

func TestWriteWithBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude_desktop_config.json")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 17, 55, 0, 0, time.UTC)
	backup, err := WriteWithBackup(path, []byte("new\n"), now)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(backup); string(b) != "old\n" {
		t.Errorf("backup = %q", b)
	}
	if !strings.HasPrefix(filepath.Base(backup), "claude_desktop_config.json.plugit-backup-20261112T175500") {
		t.Errorf("backup name = %s", backup)
	}
	if b, _ := os.ReadFile(path); string(b) != "new\n" {
		t.Errorf("config = %q", b)
	}

	// A second write in the same second keeps both backups.
	backup2, err := WriteWithBackup(path, []byte("newer\n"), now)
	if err != nil || backup2 == backup {
		t.Errorf("second backup = %q, %v", backup2, err)
	}

	// No existing file: nothing to back up, the file is created.
	fresh := filepath.Join(dir, "mcp.json")
	if b, err := WriteWithBackup(fresh, []byte("{}\n"), now); err != nil || b != "" {
		t.Errorf("fresh: backup=%q err=%v", b, err)
	}
	// The client's directory must exist: plugit doesn't create config dirs
	// for clients that aren't installed.
	if _, err := WriteWithBackup(filepath.Join(dir, "absent", "x.json"), []byte("{}"), now); !errors.Is(err, ErrClientNotFound) {
		t.Errorf("missing dir: err = %v", err)
	}
}

func TestReadConfig(t *testing.T) {
	dir := t.TempDir()
	if b, existed, err := ReadConfig(filepath.Join(dir, "absent.json")); err != nil || existed || b != nil {
		t.Errorf("absent: %q %v %v", b, existed, err)
	}
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, make([]byte, MaxConfigBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadConfig(big); err == nil {
		t.Error("oversized config accepted")
	}
}

func TestClaudeCodeCommands(t *testing.T) {
	e := Entry{Command: `C:\Program Files\plugit\plugit.exe`, Args: []string{"serve", "pets", "--toolset", "ro"}}
	add := ClaudeCodeAdd("pets", e)
	want := []string{"mcp", "add", "--scope", "user", "pets", "--", `C:\Program Files\plugit\plugit.exe`, "serve", "pets", "--toolset", "ro"}
	if !slices.Equal(add, want) {
		t.Errorf("add = %q", add)
	}
	if rm := ClaudeCodeRemove("pets"); !slices.Equal(rm, []string{"mcp", "remove", "--scope", "user", "pets"}) {
		t.Errorf("remove = %q", rm)
	}
}

// npm installs claude as a .cmd shim, and cmd.exe re-parses its arguments:
// refuse anything cmd would interpret rather than risk running it.
func TestCheckShimArgs(t *testing.T) {
	ok := []string{"mcp", "add", "pets", `C:\Program Files\plugit\plugit.exe`, "serve"}
	if err := CheckShimArgs(`C:\npm\claude.cmd`, ok); err != nil {
		t.Errorf("plain args refused: %v", err)
	}
	for _, bad := range []string{`C:\a&b\plugit.exe`, "100%", `say"hi"`, "a|b", "a^b", "a<b", "a>b", "wow!", "line\nbreak"} {
		if err := CheckShimArgs(`C:\npm\claude.CMD`, []string{bad}); err == nil {
			t.Errorf("%q accepted for a .cmd shim", bad)
		}
		if err := CheckShimArgs(`C:\bin\claude.exe`, []string{bad}); err != nil && !strings.Contains(bad, "\n") {
			t.Errorf("%q refused for a real executable: %v", bad, err)
		}
	}
}
