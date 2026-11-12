package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/curate"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/store"
	"github.com/swayam5342/plugit/internal/tokens"
	"github.com/swayam5342/plugit/internal/tui"
)

type statsSettings struct {
	name   string        // "" for every API
	since  time.Duration // window (default 7 days)
	asJSON bool
	out    io.Writer
	info   BuildInfo
	// interactive opens the stats screen; nil prints a table.
	interactive func(ctx context.Context, in tui.StatsInput) error
}

func newStatsCmd(g *globalFlags, info BuildInfo) *cobra.Command {
	var s statsSettings
	var plain bool
	cmd := &cobra.Command{
		Use:   "stats [name]",
		Short: "Show call counts, error rates and latency per tool",
		Long: `Summarize the call log per API and tool: calls, error rate, p50/p95
latency, response bytes and the last call, plus each API's current token
budget. On a terminal this is a live screen (1-4 change the window, s the
sort); piped, or with --plain, it prints a table; --json is for scripts.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				s.name = args[0]
			}
			s.out, s.info = cmd.OutOrStdout(), info
			if out, ok := s.out.(*os.File); ok && !plain && !s.asJSON && term.IsTerminal(int(out.Fd())) { // #nosec G115 -- a file descriptor fits in int
				s.interactive = func(ctx context.Context, in tui.StatsInput) error { return tui.RunStats(ctx, in, os.Stdin, out) }
			}
			return showStats(cmd.Context(), p, s)
		},
	}
	f := cmd.Flags()
	f.DurationVar(&s.since, "since", 7*24*time.Hour, "window for --plain and --json output (e.g. 24h)")
	f.BoolVar(&s.asJSON, "json", false, "machine-readable output")
	f.BoolVar(&plain, "plain", false, "print a table even on a terminal")
	return cmd
}

func showStats(ctx context.Context, p config.Paths, s statsSettings) error {
	if s.name != "" {
		if err := config.ValidateAPIName(s.name); err != nil {
			return err
		}
	}
	open := store.OpenExisting
	if s.interactive != nil {
		open = store.Open // the screen waits for calls
	}
	db, err := open(ctx, p.DB())
	if errors.Is(err, store.ErrNoLog) {
		_, err := fmt.Fprintln(s.out, "No calls recorded yet. Calls appear once an MCP client uses `plugit serve`.")
		return err
	}
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if s.interactive != nil {
		budgets := map[string]string{}
		names, _ := p.APINames()
		for _, n := range names {
			if s.name == "" || n == s.name {
				if b := apiBudget(p, n, s.info); b != "" {
					budgets[n] = b
				}
			}
		}
		return s.interactive(ctx, tui.StatsInput{
			Title:   cmp.Or(s.name, "all APIs"),
			Budgets: budgets,
			Source: func(ctx context.Context, since time.Time) ([]store.ToolStats, error) {
				return db.Stats(ctx, store.Filter{API: s.name, Since: since})
			},
		})
	}

	window := cmp.Or(s.since, 7*24*time.Hour)
	since := time.Now().Add(-window)
	st, err := db.Stats(ctx, store.Filter{API: s.name, Since: since})
	if err != nil {
		return err
	}
	if s.asJSON {
		if st == nil {
			st = []store.ToolStats{}
		}
		enc := json.NewEncoder(s.out)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Since time.Time         `json:"since"`
			Tools []store.ToolStats `json:"tools"`
		}{since.UTC(), st})
	}
	if len(st) == 0 {
		_, err := fmt.Fprintf(s.out, "No calls recorded in the last %s.\n", window)
		return err
	}
	tw := tabwriter.NewWriter(s.out, 2, 4, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintln(tw, "API\tTOOL\tCALLS\tERRORS\tP50\tP95\tBYTES\tLAST\t")
	for _, t := range st {
		rate := "-"
		if t.Calls > 0 {
			rate = fmt.Sprintf("%d%%", (t.Errors*100+t.Calls/2)/t.Calls)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t\n", tui.Safe(t.API), tui.Safe(t.Tool), t.Calls, rate,
			tui.Millis(t.P50), tui.Millis(t.P95), tui.HumanBytes(t.Bytes), t.Last.Local().Format("2006-01-02 15:04"))
	}
	return tw.Flush()
}

// apiBudget is "≈2.3k tokens · 8 tools" for an API's default toolset, from
// the manifest cache only (stats never parses a spec). "" when unknown.
func apiBudget(p config.Paths, name string, info BuildInfo) string {
	cfg, err := config.LoadAPI(p, name)
	if err != nil {
		return ""
	}
	snap, _ := p.SpecSnapshot(name)
	_, sum, err := config.ReadSnapshot(snap)
	if err != nil {
		return ""
	}
	cache, _ := p.Manifest(name)
	m, err := config.ReadManifest(cache, config.CacheKey{SpecSHA256: sum, Prefix: cfg.Prefix, Parser: parserID(info)})
	if err != nil {
		return ""
	}
	res, err := curate.Apply(prepareAll(m.Tools, hiddenParams(cfg.Auth, cfg.Fixed)), cfg.Toolsets[defaultToolset], cfg.Prefix)
	if err != nil {
		return ""
	}
	n, budget := 0, 0
	for _, st := range res.Tools {
		if policy.Lists(st.Tool.Safety, cfg.PolicyMode()) && st.Override != policy.OverrideDeny {
			n++
			budget += tokens.Tool(st.Tool)
		}
	}
	unit := "tools"
	if n == 1 {
		unit = "tool"
	}
	return fmt.Sprintf("%s tokens · %d %s", tokens.Format(budget), n, unit)
}
