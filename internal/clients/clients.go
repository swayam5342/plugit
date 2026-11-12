package clients

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	Codex         Client = "codex"
	Cursor        Client = "cursor"
	VSCode        Client = "vscode"
	Windsurf      Client = "windsurf"
	Gemini        Client = "gemini"
	LMStudio      Client = "lm-studio"
)

// Info describes how plugit installs into a client. A client is either
// configured through its own CLI (CLI set) or through a JSON file plugit
// edits in place (Format set).
type Info struct {
	ID     Client
	Name   string // for people: "Claude Desktop"
	CLI    string // executable that owns the client's config
	Format Format // where servers live in the client's JSON file
}

var all = []Info{
	{ID: ClaudeDesktop, Name: "Claude Desktop", Format: MCPServers},
	{ID: ClaudeCode, Name: "Claude Code", CLI: "claude"},
	{ID: Codex, Name: "OpenAI Codex", CLI: "codex"},
	{ID: Cursor, Name: "Cursor", Format: MCPServers},
	{ID: VSCode, Name: "VS Code (GitHub Copilot)", Format: VSCodeServers},
	{ID: Windsurf, Name: "Windsurf", Format: MCPServers},
	{ID: Gemini, Name: "Gemini CLI", Format: MCPServers},
	{ID: LMStudio, Name: "LM Studio", Format: MCPServers},
}

var aliases = map[string]Client{
	"copilot":      VSCode,
	"vs-code":      VSCode,
	"gemini-cli":   Gemini,
	"openai-codex": Codex,
	"codex-cli":    Codex,
	"lmstudio":     LMStudio,
}

// All lists the supported clients in display order.
func All() []Info { return slices.Clone(all) }

// Lookup returns a client's details; c must come from ParseClient or All.
func Lookup(c Client) Info {
	for _, i := range all {
		if i.ID == c {
			return i
		}
	}
	return Info{ID: c, Name: string(c)}
}

// IDs lists the client IDs, for help text.
func IDs() string {
	ids := make([]string, len(all))
	for i, c := range all {
		ids[i] = string(c.ID)
	}
	return strings.Join(ids, ", ")
}

// ParseClient parses a --client value. Case doesn't matter and a few
// common names (copilot, gemini-cli, …) are accepted.
func ParseClient(s string) (Client, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if c, ok := aliases[s]; ok {
		return c, nil
	}
	for _, i := range all {
		if string(i.ID) == s {
			return i.ID, nil
		}
	}
	switch s {
	case "chatgpt", "openai":
		return "", errors.New("ChatGPT connects only to remote MCP servers and plugit serves over stdio; use --client codex for OpenAI's Codex")
	}
	return "", fmt.Errorf("unknown --client %q: use one of %s", s, IDs())
}

// AddArgs is the argument list for a CLI client that registers e as name
// at user scope. The entry goes after "--" as plain arguments, so no JSON
// has to survive Windows command-line parsing.
func (i Info) AddArgs(name string, e Entry) []string {
	args := []string{"mcp", "add"}
	if i.ID == ClaudeCode {
		args = append(args, "--scope", "user")
	}
	return append(append(args, name, "--", e.Command), e.Args...)
}

// RemoveArgs is the argument list for a CLI client that removes name.
func (i Info) RemoveArgs(name string) []string {
	args := []string{"mcp", "remove"}
	if i.ID == ClaudeCode {
		args = append(args, "--scope", "user")
	}
	return append(args, name)
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
		return appConfig(env, "Claude", "claude_desktop_config.json")
	case VSCode:
		return appConfig(env, "Code", "User", "mcp.json")
	case Cursor:
		return homeJoin(env, ".cursor", "mcp.json")
	case Windsurf:
		return homeJoin(env, ".codeium", "windsurf", "mcp_config.json")
	case Gemini:
		return homeJoin(env, ".gemini", "settings.json")
	case LMStudio:
		return homeJoin(env, ".lmstudio", "mcp.json")
	}
	return "", fmt.Errorf("%s is configured through its CLI, not a file", Lookup(c).Name)
}

// appConfig is a desktop app's file under the OS's per-user application
// config directory.
func appConfig(env Env, parts ...string) (string, error) {
	switch env.GOOS {
	case "windows":
		if env.AppData == "" {
			return "", errors.New("APPDATA is not set")
		}
		return filepath.Join(append([]string{env.AppData}, parts...)...), nil
	case "darwin":
		return homeJoin(env, append([]string{"Library", "Application Support"}, parts...)...)
	default:
		if env.ConfigHome == "" {
			return "", errors.New("no config directory (set XDG_CONFIG_HOME or HOME)")
		}
		return filepath.Join(append([]string{env.ConfigHome}, parts...)...), nil
	}
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

// CheckShimArgs refuses arguments a Windows batch shim (claude.cmd or codex.cmd,
// as npm installs them) would re-interpret: cmd.exe parses its command line again,
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
