package cli

import (
	"context"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/swayam5342/plugit/internal/clients"
	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/tui"
)

// runPicker runs the terminal picker (SPEC §2.3). The picker draws on
// stdout: add and edit are interactive commands, never MCP servers.
func runPicker(ctx context.Context, in pickInput) (config.Toolset, error) {
	return tui.RunPicker(ctx, tui.PickerInput{
		API:      in.api,
		Tools:    in.tools,
		Prefix:   in.prefix,
		Mode:     in.mode,
		Selected: in.selected,
	}, os.Stdin, os.Stdout)
}

// chooseClients returns the interactive client chooser for install and
// uninstall, or nil when stdin isn't a terminal.
func chooseClients(cmd *cobra.Command) func([]clientOption) ([]clients.Client, error) {
	in, ok := cmd.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(int(in.Fd())) { // #nosec G115 -- a file descriptor fits in int
		return nil
	}
	verb := cmd.Name()
	return func(opts []clientOption) ([]clients.Client, error) {
		choices := make([]tui.Choice, len(opts))
		for i, o := range opts {
			choices[i] = tui.Choice{Label: o.Info.Name, Detail: o.Where}
			if o.Detected {
				choices[i].Note = "detected"
			}
		}
		idx, err := tui.RunChooser(cmd.Context(), "plugit "+verb, "Choose the MCP clients (space to tick, enter to confirm)", choices, in, os.Stdout)
		if err != nil {
			return nil, err
		}
		out := make([]clients.Client, len(idx))
		for i, j := range idx {
			out[i] = opts[j].Info.ID
		}
		return out, nil
	}
}
