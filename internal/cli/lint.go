package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/lint"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/openapi"
)

// exitCodeError makes Main exit with code instead of 1.
type exitCodeError struct {
	code int
	err  error
}

func (e exitCodeError) Error() string { return e.err.Error() }
func (e exitCodeError) Unwrap() error { return e.err }

// usageError marks a command-line mistake: exit code 2.
func usageError(err error) error { return exitCodeError{code: 2, err: err} }

type lintSettings struct {
	toolset string // "" lints every toolset
	asJSON  bool
	out     io.Writer
	stderr  io.Writer
	parse   func(ctx context.Context, data []byte, opts openapi.Options) (*model.Manifest, error)
	info    BuildInfo
}

func newLintCmd(g *globalFlags, info BuildInfo) *cobra.Command {
	var s lintSettings
	cmd := &cobra.Command{
		Use:   "lint <name>",
		Short: "Check a saved API's toolsets for quality and safety problems",
		Long: `Check a saved API's toolsets for quality and safety problems (rules
L001-L013): missing descriptions, name clashes, oversized schemas, text
aimed at the model (hidden characters, injected instructions, credentials),
policy overrides that do nothing, and credentials sent over plain http.

Exit codes: 0 clean or warnings only, 1 errors found, 2 usage error.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return usageError(err)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			s.out, s.stderr, s.parse, s.info = cmd.OutOrStdout(), cmd.ErrOrStderr(), isolate.Parse, info
			return lintAPI(cmd.Context(), p, args[0], s)
		},
	}
	cmd.Flags().StringVar(&s.toolset, "toolset", "", "lint only this toolset (default: all)")
	cmd.Flags().BoolVar(&s.asJSON, "json", false, "machine-readable output (for CI)")
	return cmd
}

type lintReport struct {
	API      string         `json:"api"`
	Findings []lint.Finding `json:"findings"`
	Summary  lint.Summary   `json:"summary"`
}

func lintAPI(ctx context.Context, p config.Paths, name string, s lintSettings) error {
	cfg, err := config.LoadAPI(p, name)
	if err != nil {
		return err
	}
	if s.toolset != "" {
		if _, ok := cfg.Toolsets[s.toolset]; !ok {
			return usageError(fmt.Errorf("API %s has no toolset %q (it has: %s)", name, s.toolset,
				strings.Join(slices.Sorted(maps.Keys(cfg.Toolsets)), ", ")))
		}
	}
	logger := slog.New(slog.NewTextHandler(s.stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	m, err := loadManifest(ctx, p, cfg, runDeps{parse: s.parse, info: s.info}, logger)
	if err != nil {
		return err
	}
	fs := lint.Run(lint.Input{API: *cfg, Tools: m.Tools, Hidden: hiddenParams(cfg.Auth, cfg.Fixed)})
	if s.toolset != "" {
		fs = slices.DeleteFunc(fs, func(f lint.Finding) bool { return f.Toolset != "" && f.Toolset != s.toolset })
	}
	sum := lint.Summarize(fs)

	if s.asJSON {
		enc := json.NewEncoder(s.out)
		enc.SetIndent("", "  ")
		if fs == nil {
			fs = []lint.Finding{}
		}
		if err := enc.Encode(lintReport{API: name, Findings: fs, Summary: sum}); err != nil {
			return err
		}
	} else if err := printFindings(s.out, name, fs, sum); err != nil {
		return err
	}
	if sum.Errors > 0 {
		return exitCodeError{code: 1, err: fmt.Errorf("lint found %s", plural(sum.Errors, "error"))}
	}
	return nil
}

func printFindings(w io.Writer, name string, fs []lint.Finding, sum lint.Summary) error {
	marks := map[lint.Severity]string{lint.Error: "✗", lint.Warn: "!", lint.Info: "·"}
	for _, f := range fs {
		where := ""
		if f.Toolset != "" {
			where = "[" + f.Toolset + "] "
		}
		if f.Tool != "" {
			where += fmt.Sprintf("%s (%s): ", safeName(f.Name), f.Tool)
		}
		if _, err := fmt.Fprintf(w, "%s %-5s %s  %s%s\n", marks[f.Severity], f.Severity, f.Rule, where, f.Message); err != nil {
			return err
		}
	}
	if len(fs) > 0 {
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "%s: %s, %s, %d info\n", name, plural(sum.Errors, "error"), plural(sum.Warnings, "warning"), sum.Infos)
	return err
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
