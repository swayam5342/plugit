package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"

	"github.com/spf13/cobra"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/curate"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/schema"
	"github.com/swayam5342/plugit/internal/tokens"
	"github.com/swayam5342/plugit/internal/tui"
)

func newEditCmd(g *globalFlags, info BuildInfo) *cobra.Command {
	var toolset string
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Reopen the tool picker for a saved API",
		Long: `Reopen the picker for one toolset of a saved API (default: "default"; a
new name creates that toolset). Other toolsets and settings are left as they
are. Edit plugit.yaml by hand for everything else.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			d := addDeps{paths: p, out: cmd.OutOrStdout(), stderr: cmd.ErrOrStderr(), parse: isolate.Parse, info: info}
			if terminalConfirm(cmd) != nil {
				d.pick = runPicker
			}
			return editAPI(cmd.Context(), p, args[0], toolset, d)
		},
	}
	cmd.Flags().StringVar(&toolset, "toolset", defaultToolset, "toolset to edit (a new name creates it)")
	return cmd
}

func editAPI(ctx context.Context, p config.Paths, name, toolset string, d addDeps) error {
	cfg, err := config.LoadAPI(p, name)
	if err != nil {
		return err
	}
	tsName := cmp.Or(toolset, defaultToolset)
	if err := config.ValidateAPIName(tsName); err != nil {
		return fmt.Errorf("toolset name: %w", err)
	}
	if d.pick == nil {
		return errors.New("the tool picker needs a terminal; edit plugit.yaml by hand instead")
	}
	logger := slog.New(slog.NewTextHandler(d.stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	m, err := loadManifest(ctx, p, cfg, runDeps{parse: d.parse, info: d.info}, logger)
	if err != nil {
		return err
	}
	tools := prepareAll(m.Tools, hiddenParams(cfg.Auth, cfg.Fixed))

	current := cfg.Toolsets[tsName]
	current.Tools = maps.Clone(current.Tools)
	if current.Tools == nil {
		current.Tools = map[string]config.ToolOverride{}
	}
	ts, err := d.pick(ctx, pickInput{api: name, tools: tools, prefix: cfg.Prefix, mode: cfg.PolicyMode(), selected: current,
		lint: pickerLint(*cfg, tsName, m.Tools)})
	if errors.Is(err, tui.ErrCancelled) {
		_, err := fmt.Fprintln(d.out, "Cancelled; nothing saved.")
		return err
	}
	if err != nil {
		return err
	}
	active, err := curate.Apply(tools, ts, cfg.Prefix)
	if err != nil {
		return err
	}
	cfg.Toolsets[tsName] = ts
	if err := config.SaveAPI(p, cfg); err != nil {
		return err
	}
	sel := make([]model.Tool, 0, len(active.Tools))
	for _, st := range active.Tools {
		sel = append(sel, st.Tool)
	}
	_, err = fmt.Fprintf(d.out, "Saved toolset %s of %s: %d tools, %s tokens. Restart your MCP client to pick up the change.\n",
		tsName, name, len(sel), tokens.Format(tokens.Total(sel)))
	return err
}

// hiddenParams are the parameter names plugit fills itself: the auth
// header or query parameter and every fixed parameter.
func hiddenParams(a config.Auth, fixed []config.Fixed) []string {
	var out []string
	switch a.Type {
	case config.AuthHeader:
		out = append(out, a.Header)
	case config.AuthQuery:
		out = append(out, a.Param)
	}
	for _, f := range fixed {
		out = append(out, f.Name)
	}
	return out
}

func prepareAll(tools []model.Tool, hidden []string) []model.Tool {
	out := make([]model.Tool, len(tools))
	for i, t := range tools {
		out[i] = schema.Prepare(t, schema.Options{Fixed: hidden})
	}
	return out
}
