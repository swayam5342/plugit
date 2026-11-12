package clients

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/swayam5342/plugit/internal/config"
)

// MaxConfigBytes caps a client config plugit will read.
const MaxConfigBytes = 16 << 20

// ErrClientNotFound means the client's config directory doesn't exist, so
// the client is probably not installed.
var ErrClientNotFound = errors.New("client config directory not found")

// Client is an MCP client plugit can install into.
type Client string

// Supported clients.
const (
	ClaudeDesktop Client = "claude-desktop"
	ClaudeCode    Client = "claude-code"
	Cursor        Client = "cursor"
)

// ParseClient parses a --client value.
func ParseClient(s string) (Client, error) {
	switch c := Client(s); c {
	case ClaudeDesktop, ClaudeCode, Cursor:
		return c, nil
	case "vscode":
		return "", errors.New("--client vscode is not supported yet (planned for v0.3); use claude-desktop, claude-code or cursor")
	}
	return "", fmt.Errorf("unknown --client %q: use claude-desktop, claude-code or cursor", s)
}

// Env holds the locations ConfigPath depends on.
type Env struct {
	GOOS       string
	Home       string
	AppData    string // Windows %APPDATA%
	ConfigHome string // Linux $XDG_CONFIG_HOME, or ~/.config
}

// DefaultEnv describes the current user's environment.
func DefaultEnv() Env {
	home, _ := os.UserHomeDir()
	e := Env{GOOS: runtime.GOOS, Home: home, AppData: os.Getenv("APPDATA"), ConfigHome: os.Getenv("XDG_CONFIG_HOME")}
	if e.ConfigHome == "" && home != "" {
		e.ConfigHome = filepath.Join(home, ".config")
	}
	return e
}

// ConfigPath is the config file plugit edits for a file-based client.
func ConfigPath(c Client, env Env) (string, error) {
	switch c {
	case ClaudeDesktop:
		switch env.GOOS {
		case "windows":
			if env.AppData == "" {
				return "", errors.New("APPDATA is not set")
			}
			return filepath.Join(env.AppData, "Claude", "claude_desktop_config.json"), nil
		case "darwin":
			return homeJoin(env, "Library", "Application Support", "Claude", "claude_desktop_config.json")
		default:
			if env.ConfigHome == "" {
				return "", errors.New("no config directory (set XDG_CONFIG_HOME or HOME)")
			}
			return filepath.Join(env.ConfigHome, "Claude", "claude_desktop_config.json"), nil
		}
	case Cursor:
		return homeJoin(env, ".cursor", "mcp.json")
	}
	return "", fmt.Errorf("%s is configured through its CLI, not a file", c)
}

func homeJoin(env Env, parts ...string) (string, error) {
	if env.Home == "" {
		return "", errors.New("no home directory")
	}
	return filepath.Join(append([]string{env.Home}, parts...)...), nil
}

// ReadConfig reads a client config. A missing file is not an error.
func ReadConfig(path string) (data []byte, existed bool, err error) {
	f, err := os.Open(path) // #nosec G304 -- a fixed client config location
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	data, err = io.ReadAll(io.LimitReader(f, MaxConfigBytes+1))
	if err != nil {
		return nil, true, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(data) > MaxConfigBytes {
		return nil, true, fmt.Errorf("reading %s: file too large", path)
	}
	return data, true, nil
}

// WriteWithBackup copies the current file (if any) to a timestamped backup
// next to it, then writes data atomically. It returns the backup path, or
// "" when there was nothing to back up. Both files are owner-only.
func WriteWithBackup(path string, data []byte, now time.Time) (string, error) {
	dir := filepath.Dir(path)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrClientNotFound, dir)
	}
	old, existed, err := ReadConfig(path)
	if err != nil {
		return "", err
	}
	backup := ""
	if existed {
		base := path + ".plugit-backup-" + now.UTC().Format("20060102T150405")
		backup = base
		for n := 2; fileExists(backup); n++ {
			backup = base + "-" + strconv.Itoa(n)
		}
		if err := config.WriteFileAtomic(backup, old); err != nil {
			return "", fmt.Errorf("backing up %s: %w", path, err)
		}
	}
	if err := config.WriteFileAtomic(path, data); err != nil {
		return backup, err
	}
	return backup, nil
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// ClaudeCodeAdd is the `claude` argument list that registers e at user
// scope. Claude Code owns ~/.claude.json and rewrites it while running, so
// plugit never edits it (SPEC T13). The entry goes after "--" as plain
// arguments, so no JSON has to survive Windows command-line parsing.
func ClaudeCodeAdd(name string, e Entry) []string {
	return append([]string{"mcp", "add", "--scope", "user", name, "--", e.Command}, e.Args...)
}

// ClaudeCodeRemove is the `claude` argument list that removes name.
func ClaudeCodeRemove(name string) []string {
	return []string{"mcp", "remove", "--scope", "user", name}
}

// CheckShimArgs refuses arguments a Windows batch shim (claude.cmd, as npm
// installs it) would re-interpret: cmd.exe parses its command line again,
// so quotes and metacharacters could change what runs.
func CheckShimArgs(exe string, args []string) error {
	ext := strings.ToLower(filepath.Ext(exe))
	shim := ext == ".cmd" || ext == ".bat"
	for _, a := range args {
		if strings.ContainsAny(a, "\r\n\x00") {
			return errors.New("arguments contain line breaks")
		}
		if shim && strings.ContainsAny(a, "\"%^&|<>!") {
			return fmt.Errorf("%s is a batch shim and an argument contains characters cmd.exe would interpret (%q); run the printed command yourself", filepath.Base(exe), a)
		}
	}
	return nil
}
