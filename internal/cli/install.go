package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/swayam5342/plugit/internal/clients"
	"github.com/swayam5342/plugit/internal/config"
)

var serverKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type installSettings struct {
	name    string
	client  string
	toolset string // "" means default
	as      string // server key in the client config; "" means name
	yes     bool
}

type installDeps struct {
	paths     config.Paths
	configDir string // the --config-dir flag, passed on to serve when set
	out       io.Writer
	// confirm asks a yes/no question; nil means no terminal.
	confirm  func(question string) (bool, error)
	env      clients.Env
	exe      func() (string, error)
	lookPath func(string) (string, error)
	run      func(ctx context.Context, name string, args ...string) ([]byte, error)
	now      func() time.Time
	// choose lets the user pick clients when --client is omitted; nil
	// means no terminal.
	choose func([]clientOption) ([]clients.Client, error)
}

// clientOption is a client offered by the chooser.
type clientOption struct {
	Info     clients.Info
	Detected bool   // looks installed on this machine
	Where    string // its config file, or the CLI that owns its config
}

func newInstallCmd(g *globalFlags) *cobra.Command {
	return installCommand(g, "install <name>", "Add a saved API to an MCP client's config (shows a diff, backs up, asks first)", installAPI)
}

func newUninstallCmd(g *globalFlags) *cobra.Command {
	return installCommand(g, "uninstall <name>", "Remove a saved API from an MCP client's config", uninstallAPI)
}

func installCommand(g *globalFlags, use, short string, fn func(context.Context, installSettings, installDeps) error) *cobra.Command {
	var s installSettings
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: short + `.

Clients: ` + clients.IDs() + `.
Without --client you pick from a list (detected clients are marked); pass
several as a comma-separated list. JSON configs are edited in place: only
plugit's own entry changes, after a timestamped backup. Claude Code and
Codex own their configs, so plugit runs their CLI (claude mcp add, codex mcp
add) instead. Entries contain the plugit command only: credentials stay in
the keyring or environment.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			s.name = args[0]
			return fn(cmd.Context(), s, installDeps{
				paths:     p,
				configDir: g.configDir,
				out:       cmd.OutOrStdout(),
				confirm:   terminalConfirm(cmd),
				env:       clients.DefaultEnv(),
				exe:       executablePath,
				lookPath:  exec.LookPath,
				run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					return exec.CommandContext(ctx, name, args...).CombinedOutput() // #nosec G204 -- the claude CLI with arguments checked by CheckShimArgs
				},
				now:    time.Now,
				choose: chooseClients(cmd),
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&s.client, "client", "", "client(s), comma-separated: "+clients.IDs()+" (default: choose)")
	f.StringVar(&s.toolset, "toolset", "", "toolset to serve (default: default)")
	f.StringVar(&s.as, "as", "", "server name in the client config (default: the API name)")
	f.BoolVar(&s.yes, "yes", false, "don't ask for confirmation")
	return cmd
}

func executablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// entryFor validates the API and builds the client entry.
func entryFor(s installSettings, d installDeps) (key string, e clients.Entry, err error) {
	cfg, err := config.LoadAPI(d.paths, s.name)
	if err != nil {
		return "", e, err
	}
	if s.toolset != "" {
		if _, ok := cfg.Toolsets[s.toolset]; !ok {
			return "", e, fmt.Errorf("API %s has no toolset %q (it has: %s)", s.name, s.toolset,
				strings.Join(slices.Sorted(maps.Keys(cfg.Toolsets)), ", "))
		}
	}
	key = s.as
	if key == "" {
		key = s.name
	}
	if !serverKeyRe.MatchString(key) {
		return "", e, fmt.Errorf("--as %q: use 1-64 letters, digits, '-' or '_'", safeName(key))
	}
	exe, err := d.exe()
	if err != nil {
		return "", e, fmt.Errorf("locating the plugit executable: %w", err)
	}
	if strings.Contains(filepath.ToSlash(exe), "/go-build") {
		return "", e, errors.New("plugit is running from a temporary `go run` build; build or install plugit and run install from that binary")
	}
	e = clients.Entry{Command: exe, Args: []string{"serve", s.name}}
	if s.toolset != "" && s.toolset != defaultToolset {
		e.Args = append(e.Args, "--toolset", s.toolset)
	}
	if d.configDir != "" {
		abs, err := filepath.Abs(d.configDir)
		if err != nil {
			return "", e, err
		}
		e.Args = append(e.Args, "--config-dir", abs)
	}
	return key, e, nil
}

// resolveClients parses --client, or asks when it's empty.
func resolveClients(s installSettings, d installDeps) ([]clients.Info, error) {
	var ids []clients.Client
	if strings.TrimSpace(s.client) == "" {
		if d.choose == nil {
			return nil, fmt.Errorf("not a terminal: pass --client (%s)", clients.IDs())
		}
		chosen, err := d.choose(clientOptions(d))
		if err != nil {
			return nil, err
		}
		if len(chosen) == 0 {
			return nil, errors.New("no client chosen; nothing changed")
		}
		ids = chosen
	} else {
		for part := range strings.SplitSeq(s.client, ",") {
			c, err := clients.ParseClient(part)
			if err != nil {
				return nil, err
			}
			if !slices.Contains(ids, c) {
				ids = append(ids, c)
			}
		}
	}
	out := make([]clients.Info, len(ids))
	for i, c := range ids {
		out[i] = clients.Lookup(c)
	}
	return out, nil
}

// clientOptions lists every client and whether it looks installed: its
// CLI is on PATH, or its config directory exists.
func clientOptions(d installDeps) []clientOption {
	var out []clientOption
	for _, info := range clients.All() {
		o := clientOption{Info: info}
		if info.CLI != "" {
			_, err := d.lookPath(info.CLI)
			o.Detected, o.Where = err == nil, info.CLI+" mcp add"
		} else if path, err := clients.ConfigPath(info.ID, d.env); err == nil {
			fi, err := os.Stat(filepath.Dir(path))
			o.Detected, o.Where = err == nil && fi.IsDir(), path
		}
		out = append(out, o)
	}
	return out
}

func installAPI(ctx context.Context, s installSettings, d installDeps) error {
	key, e, err := entryFor(s, d)
	if err != nil {
		return err
	}
	infos, err := resolveClients(s, d)
	if err != nil {
		return err
	}
	return eachClient(d, infos, func(info clients.Info) error {
		if info.CLI != "" {
			return runClientCLI(ctx, info, s, d, info.AddArgs(key, e), "add "+key)
		}
		return editClientFile(info, s, d, func(data []byte) ([]byte, bool, error) {
			out, err := clients.InstallJSON(data, info.Format, key, e)
			if errors.Is(err, clients.ErrNotOurs) {
				return nil, false, fmt.Errorf("%w; pick another name with --as", err)
			}
			return out, err == nil, err
		}, fmt.Sprintf("%s is already installed in %s as %q.", s.name, info.Name, key))
	})
}

func uninstallAPI(ctx context.Context, s installSettings, d installDeps) error {
	key := s.as
	if key == "" {
		key = s.name
	}
	if !serverKeyRe.MatchString(key) {
		return fmt.Errorf("--as %q: use 1-64 letters, digits, '-' or '_'", safeName(key))
	}
	infos, err := resolveClients(s, d)
	if err != nil {
		return err
	}
	return eachClient(d, infos, func(info clients.Info) error {
		if info.CLI != "" {
			return runClientCLI(ctx, info, s, d, info.RemoveArgs(key), "remove "+key)
		}
		return editClientFile(info, s, d, func(data []byte) ([]byte, bool, error) {
			return clients.UninstallJSON(data, info.Format, key)
		}, fmt.Sprintf("%s has no plugit entry named %q.", info.Name, key))
	})
}

// eachClient runs fn per client, with a heading when there are several.
// It stops at the first failure.
func eachClient(d installDeps, infos []clients.Info, fn func(clients.Info) error) error {
	for i, info := range infos {
		if len(infos) > 1 {
			sep := ""
			if i > 0 {
				sep = "\n"
			}
			if _, err := fmt.Fprintf(d.out, "%s== %s ==\n", sep, info.Name); err != nil {
				return err
			}
		}
		if err := fn(info); err != nil {
			if len(infos) > 1 {
				return fmt.Errorf("%s: %w", info.Name, err)
			}
			return err
		}
	}
	return nil
}

// editClientFile reads a client config, applies edit, shows the diff, asks,
// then backs up and writes.
func editClientFile(info clients.Info, s installSettings, d installDeps, edit func([]byte) ([]byte, bool, error), unchanged string) error {
	path, err := clients.ConfigPath(info.ID, d.env)
	if err != nil {
		return err
	}
	before, _, err := clients.ReadConfig(path)
	if err != nil {
		return err
	}
	after, changed, err := edit(before)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if !changed || string(after) == string(before) {
		_, err := fmt.Fprintln(d.out, unchanged)
		return err
	}
	if _, err := fmt.Fprintf(d.out, "%s\n%s", path, clients.Diff(before, after)); err != nil {
		return err
	}
	if err := confirmOrYes(d, s.yes, "Write this change?"); err != nil {
		return err
	}
	backup, err := clients.WriteWithBackup(path, after, d.now())
	if errors.Is(err, clients.ErrClientNotFound) {
		return fmt.Errorf("%w: is %s installed?", err, info.Name)
	}
	if err != nil {
		return err
	}
	if backup != "" {
		if _, err := fmt.Fprintf(d.out, "Backed up the previous config to %s\n", backup); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(d.out, "Updated %s. Restart %s to pick up the change.\n", path, info.Name)
	return err
}

// runClientCLI runs a client's own CLI (claude, codex) to change its
// config, or prints the command when the CLI isn't on PATH.
func runClientCLI(ctx context.Context, info clients.Info, s installSettings, d installDeps, args []string, what string) error {
	shown := info.CLI + " " + shellJoin(args)
	exe, err := d.lookPath(info.CLI)
	if err != nil {
		_, err := fmt.Fprintf(d.out, "The %s CLI isn't on PATH. Run this to %s in %s:\n\n  %s\n", info.CLI, what, info.Name, shown)
		return err
	}
	if err := clients.CheckShimArgs(exe, args); err != nil {
		_, _ = fmt.Fprintf(d.out, "Run this yourself:\n\n  %s\n", shown)
		return err
	}
	if _, err := fmt.Fprintf(d.out, "Will run:\n\n  %s\n\n", shown); err != nil {
		return err
	}
	if err := confirmOrYes(d, s.yes, "Run it?"); err != nil {
		return err
	}
	out, err := d.run(ctx, exe, args...)
	if err != nil {
		return fmt.Errorf("%s %s failed: %w: %s", info.CLI, args[1], err, strings.TrimSpace(string(out)))
	}
	_, err = fmt.Fprintf(d.out, "%s\n", strings.TrimSpace(string(out)))
	return err
}

func confirmOrYes(d installDeps, yes bool, q string) error {
	if yes {
		return nil
	}
	if d.confirm == nil {
		return errors.New("not a terminal: pass --yes to confirm")
	}
	ok, err := d.confirm(q)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("not confirmed; nothing changed")
	}
	return nil
}

// shellJoin quotes arguments for display (POSIX-style, good enough for
// copy-paste in PowerShell too).
func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t'\"\\$&|;<>()*?!`") {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			out[i] = a
		}
	}
	return strings.Join(out, " ")
}
