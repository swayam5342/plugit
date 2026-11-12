package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/store"
	"github.com/swayam5342/plugit/internal/tui"
)

type callSource = func(ctx context.Context, afterID int64) ([]store.Call, error)

type logsSettings struct {
	name     string // "" for every API
	follow   bool
	asJSON   bool
	limit    int           // newest calls to show first (default 50)
	interval time.Duration // follow poll interval (default 500ms)
	out      io.Writer
	// interactive opens the live inspector; nil prints instead.
	interactive func(ctx context.Context, title string, allAPIs bool, src callSource) error
}

func newLogsCmd(g *globalFlags) *cobra.Command {
	var s logsSettings
	var plain bool
	cmd := &cobra.Command{
		Use:   "logs [name]",
		Short: "Show recorded tool calls (live inspector on a terminal)",
		Long: `Show the calls MCP clients made through plugit serve: time, tool, outcome,
HTTP status, latency and size. On a terminal this opens a live inspector
(enter shows a call's redacted arguments, error and response preview).
Piped, or with --plain, it prints lines; --follow keeps printing new calls.

Arguments are logged per log.args (redacted by default) and previews only
with log.response_preview: true. Secrets are masked before anything is
stored.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				s.name = args[0]
			}
			s.out = cmd.OutOrStdout()
			if out, ok := s.out.(*os.File); ok && !plain && !s.asJSON && term.IsTerminal(int(out.Fd())) { // #nosec G115 -- a file descriptor fits in int
				s.interactive = func(ctx context.Context, title string, all bool, src callSource) error {
					return tui.RunInspector(ctx, tui.InspectorInput{Title: title, AllAPIs: all, Source: src}, os.Stdin, out)
				}
			}
			return showLogs(cmd.Context(), p, s)
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&s.follow, "follow", "f", false, "keep printing new calls (plain output)")
	f.BoolVar(&s.asJSON, "json", false, "one JSON object per call")
	f.IntVarP(&s.limit, "limit", "n", 50, "how many recent calls to show")
	f.BoolVar(&plain, "plain", false, "print lines even on a terminal")
	return cmd
}

func showLogs(ctx context.Context, p config.Paths, s logsSettings) error {
	if s.name != "" {
		if err := config.ValidateAPIName(s.name); err != nil {
			return err
		}
	}
	filter := store.Filter{API: s.name}

	if s.interactive != nil {
		// The inspector waits for calls, so create the log if needed.
		db, err := store.Open(ctx, p.DB())
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		title := cmp.Or(s.name, "all APIs")
		return s.interactive(ctx, title, s.name == "", func(ctx context.Context, after int64) ([]store.Call, error) {
			f := filter
			f.AfterID = after
			if after == 0 {
				f.Limit, f.Newest = 500, true
			}
			return db.List(ctx, f)
		})
	}

	open := store.OpenExisting
	if s.follow {
		open = store.Open
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

	f := filter
	f.Limit, f.Newest = cmp.Or(s.limit, 50), true
	calls, err := db.List(ctx, f)
	if err != nil {
		return err
	}
	if len(calls) == 0 && !s.follow && !s.asJSON {
		_, err := fmt.Fprintln(s.out, "No calls recorded yet. Calls appear once an MCP client uses `plugit serve`.")
		return err
	}
	last := int64(0)
	emit := func(cs []store.Call) error {
		for _, c := range cs {
			if err := printCall(s.out, c, s.asJSON, s.name == ""); err != nil {
				return err
			}
			last = max(last, c.ID)
		}
		return nil
	}
	if err := emit(calls); err != nil {
		return err
	}
	if !s.follow {
		return nil
	}
	tick := time.NewTicker(cmp.Or(s.interval, 500*time.Millisecond))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		f := filter
		f.AfterID = last
		cs, err := db.List(ctx, f)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if err := emit(cs); err != nil {
			return err
		}
	}
}

// printCall writes one call. Text from the upstream (errors) is stripped
// of control characters before it reaches the terminal.
func printCall(w io.Writer, c store.Call, asJSON, showAPI bool) error {
	if asJSON {
		b, err := json.Marshal(c)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s\n", b)
		return err
	}
	status := "-"
	if c.Status > 0 {
		status = fmt.Sprint(c.Status)
	}
	api := ""
	if showAPI {
		api = tui.Safe(c.API) + "  "
	}
	line := fmt.Sprintf("%s  %s%-28s %-10s %3s %8s %9s", c.Time.Local().Format("2006-01-02 15:04:05"), api,
		tui.Safe(c.Tool), c.Outcome, status, tui.Millis(c.Duration), tui.HumanBytes(int64(c.Bytes)))
	if c.Error != "" {
		line += "  " + tui.Safe(c.Error)
	}
	_, err := fmt.Fprintln(w, line)
	return err
}
