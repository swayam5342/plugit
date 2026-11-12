package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/swayam5342/plugit/internal/clients"
	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/secrets"
)

// ---- list ----

func newListCmd(g *globalFlags) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List saved APIs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			return listAPIs(p, asJSON, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

type apiSummary struct {
	Name     string         `json:"name"`
	BaseURL  string         `json:"base_url,omitempty"`
	Mode     string         `json:"mode,omitempty"`
	Toolsets map[string]int `json:"toolsets,omitempty"` // toolset -> listed keys
	Error    string         `json:"error,omitempty"`
}

func listAPIs(p config.Paths, asJSON bool, w io.Writer) error {
	names, err := p.APINames()
	if err != nil {
		return err
	}
	sums := make([]apiSummary, 0, len(names))
	for _, n := range names {
		s := apiSummary{Name: n}
		cfg, err := config.LoadAPI(p, n)
		if err != nil {
			s.Error = err.Error()
		} else {
			s.BaseURL, s.Mode, s.Toolsets = cfg.BaseURL, cfg.Policy.Mode, map[string]int{}
			for tn, ts := range cfg.Toolsets {
				s.Toolsets[tn] = len(ts.Tools)
			}
		}
		sums = append(sums, s)
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(sums)
	}
	if len(sums) == 0 {
		_, err := fmt.Fprintln(w, "No APIs saved yet. Add one with `plugit add <name> <spec-url|file>`.")
		return err
	}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tTOOLS\tMODE\tBASE URL")
	for _, s := range sums {
		if s.Error != "" {
			_, _ = fmt.Fprintf(tw, "%s\t-\t-\terror: %s\n", s.Name, firstLine(s.Error))
			continue
		}
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", s.Name, s.Toolsets[defaultToolset], s.Mode, s.BaseURL)
	}
	return tw.Flush()
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// ---- remove ----

func newRemoveCmd(g *globalFlags) *cobra.Command {
	var yesFlag bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Delete a saved API (its config, spec snapshot and manifest)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			return removeAPI(p, args[0], yesFlag, terminalConfirm(cmd), clients.DefaultEnv(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&yesFlag, "yes", false, "don't ask for confirmation")
	return cmd
}

func removeAPI(p config.Paths, name string, yesFlag bool, confirm func(string) (bool, error), env clients.Env, w io.Writer) error {
	dir, err := p.APIDir(name)
	if err != nil {
		return err
	}
	cfgPath, _ := p.APIConfig(name)
	if _, err := os.Stat(cfgPath); err != nil {
		return fmt.Errorf("%w: %q", config.ErrAPINotFound, name)
	}
	if err := confirmOrYes(installDeps{confirm: confirm}, yesFlag, fmt.Sprintf("Delete %s and everything in it?", dir)); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing %s: %w", dir, err)
	}
	if _, err := fmt.Fprintf(w, "Removed %s. Keyring items it used are left in place.\n", name); err != nil {
		return err
	}
	for _, info := range clients.All() {
		for key, e := range clientEntries(info, env) {
			if e.API == name {
				if _, err := fmt.Fprintf(w, "%s still has an entry %q for it: run `plugit uninstall %s --client %s", info.Name, key, name, info.ID); err != nil {
					return err
				}
				if key != name {
					_, _ = fmt.Fprintf(w, " --as %s", key)
				}
				_, _ = fmt.Fprintln(w, "`")
			}
		}
	}
	return nil
}

// clientEntries returns the plugit entries in a file client's config; CLI
// clients and unreadable configs have none.
func clientEntries(info clients.Info, env clients.Env) map[string]clients.PlugitEntry {
	if info.CLI != "" {
		return nil
	}
	path, err := clients.ConfigPath(info.ID, env)
	if err != nil {
		return nil
	}
	data, existed, err := clients.ReadConfig(path)
	if err != nil || !existed {
		return nil
	}
	entries, _ := clients.PlugitEntries(data, info.Format)
	return entries
}

// ---- doctor ----

type doctorDeps struct {
	paths     config.Paths
	out       io.Writer
	env       clients.Env
	lookupEnv func(string) (string, bool)
	keyring   secrets.KeyringGetter // nil: keyring items aren't checked
	exe       func() (string, error)
	lookPath  func(string) (string, error)
	stat      func(string) error
	info      BuildInfo
}

func newDoctorCmd(g *globalFlags, info BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check saved APIs, credentials and client installs; report relaxed security defaults",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			n := runDoctor(doctorDeps{
				paths: p, out: cmd.OutOrStdout(), env: clients.DefaultEnv(),
				lookupEnv: os.LookupEnv, keyring: secrets.OSKeyring{},
				exe: executablePath, lookPath: exec.LookPath,
				stat: func(path string) error { _, err := os.Stat(path); return err },
				info: info,
			})
			if n > 0 {
				return fmt.Errorf("%d problem(s) found", n)
			}
			return nil
		},
	}
}

type report struct {
	w        io.Writer
	problems int
}

func (r *report) ok(format string, a ...any)   { r.line("✓", format, a...) }
func (r *report) warn(format string, a ...any) { r.line("!", format, a...) }
func (r *report) info(format string, a ...any) { r.line("·", format, a...) }
func (r *report) fail(format string, a ...any) {
	r.problems++
	r.line("✗", format, a...)
}
func (r *report) line(mark, format string, a ...any) {
	_, _ = fmt.Fprintf(r.w, "  %s %s\n", mark, firstLine(fmt.Sprintf(format, a...)))
}

// runDoctor prints the checks and returns the number of failures. Secret
// values are never printed: only whether a reference resolves.
func runDoctor(d doctorDeps) int {
	r := &report{w: d.out}
	exe, err := d.exe()
	if err != nil {
		exe = "unknown"
	}
	_, _ = fmt.Fprintf(d.out, "plugit %s (%s)\nconfig: %s\n", d.info.withDefaults().Version, exe, d.paths.Root())

	names, err := d.paths.APINames()
	if err != nil {
		r.fail("listing APIs: %v", err)
	}
	if len(names) == 0 {
		r.info("no APIs saved yet")
	}
	for _, n := range names {
		_, _ = fmt.Fprintf(d.out, "API %s\n", n)
		doctorAPI(d, r, n)
	}

	_, _ = fmt.Fprintln(d.out, "clients")
	for _, info := range clients.All() {
		if info.CLI != "" {
			if _, err := d.lookPath(info.CLI); err != nil {
				r.info("%s: %s CLI not on PATH (install prints the command instead)", info.Name, info.CLI)
			} else {
				r.ok("%s: %s CLI found (check with `%s mcp list`)", info.Name, info.CLI, info.CLI)
			}
			continue
		}
		c := info.Name
		entries := clientEntries(info, d.env)
		if len(entries) == 0 {
			r.info("%s: no plugit entries", c)
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(entries)) {
			e := entries[key]
			switch {
			case d.stat(e.Command) != nil:
				r.fail("%s: %q runs %s, which is missing; reinstall with `plugit install %s --client %s`", c, key, e.Command, e.API, info.ID)
			case !slices.Contains(names, e.API):
				r.fail("%s: %q serves %s, which isn't saved", c, key, e.API)
			case e.Command != exe:
				r.warn("%s: %q runs a different plugit binary (%s)", c, key, e.Command)
			default:
				r.ok("%s: %q serves %s", c, key, e.API)
			}
		}
	}
	return r.problems
}

func doctorAPI(d doctorDeps, r *report, name string) {
	cfg, err := config.LoadAPI(d.paths, name)
	if err != nil {
		r.fail("plugit.yaml: %s", strings.ReplaceAll(err.Error(), "\n", "; "))
		return
	}
	r.ok("plugit.yaml valid, base URL %s, %d toolset(s)", cfg.BaseURL, len(cfg.Toolsets))

	snap, _ := d.paths.SpecSnapshot(name)
	if _, sum, err := config.ReadSnapshot(snap); err != nil {
		r.fail("spec snapshot: %v", err)
	} else {
		cache, _ := d.paths.Manifest(name)
		if _, err := config.ReadManifest(cache, config.CacheKey{SpecSHA256: sum, Prefix: cfg.Prefix, Parser: parserID(d.info)}); err != nil {
			r.warn("manifest cache missing or stale: the next serve parses the spec once (slower start)")
		} else {
			r.ok("manifest cache fresh")
		}
	}

	// Relaxed security defaults (SPEC §5.4).
	if cfg.Network.AllowPrivate {
		r.warn("network.allow_private is true: loopback and private-network addresses are reachable")
	}
	if cfg.PolicyMode() != policy.ReadOnly {
		r.warn("policy.mode is %s: write tools can be offered", cfg.Policy.Mode)
	}
	if cfg.Log.Args == config.LogArgsFull {
		r.warn("log.args is full: call arguments are logged unredacted")
	}
	if cfg.Log.ResponsePreview {
		r.warn("log.response_preview is true")
	}
	if strings.HasPrefix(cfg.BaseURL, "http://") {
		r.warn("base_url uses http: credentials and data travel unencrypted")
	}

	res := &secrets.Resolver{LookupEnv: d.lookupEnv, Keyring: d.keyring}
	check := func(field, value string) {
		ref, err := secrets.ParseRef(value)
		if err != nil || !ref.IsSecret() {
			return
		}
		if ref.Scheme == secrets.Keyring && d.keyring == nil {
			r.info("%s %s: keyring not checked", field, ref)
			return
		}
		if _, err := res.Resolve(ref); err != nil {
			if ref.Scheme == secrets.Env {
				r.warn("%s %s: %s %s in this shell; make sure the MCP client's env block sets it", field, ref, ref.Var, envMessage(err))
				return
			}
			r.fail("%s %s: %v; store it with `plugit auth %s`", field, ref, err, name)
			return
		}
		r.ok("%s %s resolves", field, ref)
	}
	if cfg.Auth.Type != config.AuthNone && cfg.Auth.Secret != "" {
		check("auth.secret", cfg.Auth.Secret)
	}
	if cfg.Auth.Type == config.AuthOAuth2 {
		r.info("OAuth2 %s via %s", cfg.Auth.Flow, hostOf(cfg.Auth.TokenURL))
		if cfg.Auth.Flow == config.FlowDeviceCode {
			if d.keyring == nil {
				r.info("%s: keyring not checked", refreshTokenRef(name))
			} else if _, err := (keyringStore{get: d.keyring, api: name}).Load(); err != nil {
				r.fail("no OAuth refresh token stored: sign in with `plugit auth %s`", name)
			} else {
				r.ok("OAuth refresh token stored in %s", refreshTokenRef(name))
			}
		}
	}
	for _, f := range cfg.Fixed {
		check("fixed "+f.Name, f.Value)
	}
}

func envMessage(err error) string {
	switch {
	case errors.Is(err, secrets.ErrEmpty):
		return "is empty"
	default:
		return "is not set"
	}
}
