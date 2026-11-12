package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/curate"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/lint"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/openapi"
	"github.com/swayam5342/plugit/internal/spec"
	"github.com/swayam5342/plugit/internal/tui"
)

type syncSettings struct {
	spec    string // new spec location; "" re-fetches source.url
	toolset string // where new operations go (default: default)
	yes     bool   // apply without the review screen
	addNew  bool   // with --yes: add every new operation to the toolset
	out     io.Writer
	stderr  io.Writer
	parse   func(context.Context, []byte, openapi.Options) (*model.Manifest, error)
	info    BuildInfo
	// review shows the Sync diff screen and returns the new operations to
	// add; nil means no terminal.
	review func(ctx context.Context, in tui.SyncInput) ([]model.OpKey, error)
}

func newSyncCmd(g *globalFlags, info BuildInfo) *cobra.Command {
	var s syncSettings
	cmd := &cobra.Command{
		Use:   "sync <name>",
		Short: "Re-fetch an API's spec, review what changed, and adopt it (overrides are kept)",
		Long: `Re-fetch a saved API's spec (from source.url, or --spec) and compare it
with the one plugit has: operations added, removed and changed, and any
change to the spec's servers. Lint runs on the new spec, so poisoned text
arriving through an update is shown before it's adopted.

Adopting the new spec never removes or edits a toolset entry: every rename,
description, policy and rate limit stays, including those for operations
the spec dropped (they're reported as missing until you remove them with
plugit edit). The pinned base_url is never changed, whatever the new spec's
servers say (SPEC T3).

On a terminal a review screen lets you pick which new operations to add.
Otherwise pass --yes (and --add-new to add every new operation).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			s.out, s.stderr, s.parse, s.info = cmd.OutOrStdout(), cmd.ErrOrStderr(), isolate.Parse, info
			if in, ok := cmd.InOrStdin().(*os.File); ok && !s.yes && term.IsTerminal(int(in.Fd())) { // #nosec G115 -- a file descriptor fits in int
				if out, ok := s.out.(*os.File); ok && term.IsTerminal(int(out.Fd())) { // #nosec G115 -- a file descriptor fits in int
					s.review = func(ctx context.Context, si tui.SyncInput) ([]model.OpKey, error) {
						return tui.RunSync(ctx, si, in, out)
					}
				}
			}
			return syncAPI(cmd.Context(), p, args[0], s)
		},
	}
	f := cmd.Flags()
	f.StringVar(&s.spec, "spec", "", "load the spec from here instead of source.url (and remember it)")
	f.StringVar(&s.toolset, "toolset", defaultToolset, "toolset that new operations are added to")
	f.BoolVar(&s.yes, "yes", false, "apply without the review screen")
	f.BoolVar(&s.addNew, "add-new", false, "with --yes, add every new operation to the toolset")
	return cmd
}

func syncAPI(ctx context.Context, p config.Paths, name string, s syncSettings) error {
	cfg, err := config.LoadAPI(p, name)
	if err != nil {
		return err
	}
	tsName := cmp.Or(s.toolset, defaultToolset)
	if err := config.ValidateAPIName(tsName); err != nil {
		return fmt.Errorf("toolset name: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(s.stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	old, err := loadManifest(ctx, p, cfg, runDeps{parse: s.parse, info: s.info}, logger)
	if err != nil {
		return err
	}

	location := cmp.Or(s.spec, cfg.Source.URL)
	sp, err := spec.New(spec.Options{AllowPrivate: cfg.Network.AllowPrivate}).Load(ctx, location)
	if err != nil {
		return err
	}
	if sp.SHA256 == cfg.Source.SHA256 {
		_, err := fmt.Fprintf(s.out, "%s is up to date: the spec hasn't changed.\n", name)
		return err
	}
	updated, err := s.parse(ctx, sp.Bytes, openapi.Options{Prefix: cfg.Prefix})
	if err != nil {
		return err
	}
	diff := curate.DiffManifests(old, updated, *cfg)

	// Lint the new spec against the current toolsets: an update is where
	// poisoned descriptions would arrive (T4).
	findings := lint.Run(lint.Input{API: *cfg, Tools: updated.Tools, Hidden: hiddenParams(cfg.Auth, cfg.Fixed)})
	lintNote := ""
	if n := lint.Summarize(findings).Errors; n > 0 {
		lintNote = fmt.Sprintf("%s in the new spec for tools you serve (run plugit lint %s after syncing to see them all)", plural(n, "lint error"), name)
	}

	var add []model.OpKey
	switch {
	case diff.Empty():
		// Nothing to review: just record the new snapshot.
	case s.review != nil:
		add, err = s.review(ctx, tui.SyncInput{API: name, Diff: diff, BaseURL: cfg.BaseURL, Toolset: tsName, LintNote: lintNote})
		if errors.Is(err, tui.ErrCancelled) {
			_, err := fmt.Fprintln(s.out, "Cancelled; nothing changed.")
			return err
		}
		if err != nil {
			return err
		}
	default:
		if err := printSyncDiff(s.out, cfg.BaseURL, diff, lintNote, findings); err != nil {
			return err
		}
		if !s.yes {
			return errors.New("review the changes above, then run again with --yes to adopt them (add --add-new to add the new operations); nothing changed")
		}
		if s.addNew {
			for _, a := range diff.Added {
				add = append(add, a.Key)
			}
		}
	}

	cfg.Source.SHA256 = sp.SHA256
	if s.spec != "" {
		cfg.Source.URL = sourceLocation(s.spec)
	}
	if len(add) > 0 {
		curate.AddToToolset(cfg, tsName, add)
	}
	if err := saveAPIFiles(p, cfg, sp.Bytes, updated, parserID(s.info)); err != nil {
		return err
	}
	msg := fmt.Sprintf("Synced %s: %d added, %d changed, %d removed; overrides kept.", name, len(diff.Added), len(diff.Changed), len(diff.Removed))
	if len(add) > 0 {
		msg += fmt.Sprintf(" Added %s to toolset %s.", plural(len(add), "operation"), tsName)
	}
	_, err = fmt.Fprintf(s.out, "%s Restart your MCP clients to pick up the change.\n", msg)
	return err
}

// sourceLocation is how a spec location is stored: files as absolute
// paths, URLs without their query (it may carry a key).
func sourceLocation(loc string) string {
	if !strings.HasPrefix(loc, "http://") && !strings.HasPrefix(loc, "https://") {
		if abs, err := filepath.Abs(loc); err == nil {
			return abs
		}
		return loc
	}
	return safeLocation(loc)
}

// printSyncDiff writes the diff as text. Spec text is stripped of control
// characters.
func printSyncDiff(w io.Writer, baseURL string, d curate.SyncDiff, lintNote string, findings []lint.Finding) error {
	var b strings.Builder
	if d.ServersChanged {
		fmt.Fprintf(&b, "! The spec's servers changed: %s -> %s\n  base_url stays %s (sync never changes it; edit plugit.yaml if you trust the new server)\n",
			tui.Safe(cmp.Or(strings.Join(d.ServersBefore, ", "), "(none)")), tui.Safe(cmp.Or(strings.Join(d.ServersAfter, ", "), "(none)")), baseURL)
		if !d.BaseURLListed {
			b.WriteString("  The new spec no longer lists your base_url.\n")
		}
	}
	served := func(c curate.OpChange) string {
		if len(c.Listed) == 0 {
			return ""
		}
		return "  (in " + strings.Join(c.Listed, ", ") + ")"
	}
	for _, a := range d.Added {
		fmt.Fprintf(&b, "+ %s  %s\n", a.Key, tui.Safe(a.New.Name))
	}
	for _, c := range d.Changed {
		fmt.Fprintf(&b, "~ %s  %s%s\n", c.Key, strings.Join(c.What, ", "), served(c))
		if len(c.Listed) > 0 && c.New != nil {
			for _, what := range c.What {
				if what == "description" {
					fmt.Fprintf(&b, "    new description: %s\n", tui.Safe(c.New.Description))
				}
			}
		}
	}
	for _, r := range d.Removed {
		note := ""
		if len(r.Listed) > 0 {
			note = "  (listed in " + strings.Join(r.Listed, ", ") + "; overrides kept)"
		}
		fmt.Fprintf(&b, "- %s%s\n", r.Key, note)
	}
	if lintNote != "" {
		fmt.Fprintf(&b, "\n✗ %s\n", lintNote)
		n := 0
		for _, f := range findings {
			if f.Severity == lint.Error && n < 5 {
				fmt.Fprintf(&b, "  %s %s: %s\n", f.Rule, f.Tool, tui.Safe(f.Message))
				n++
			}
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
