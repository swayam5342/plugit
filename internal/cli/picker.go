package cli

import (
	"context"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/swayam5342/plugit/internal/clients"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/tui"
)

// protoDescribe returns the per-protocol badge/target function the TUI uses
// (SPEC §2.3), bound to the API's protocol through describeFor. An empty name
// is a pre-field manifest and means REST.
func protoDescribe(name model.Protocol) func(model.Tool) (string, string) {
	return func(t model.Tool) (string, string) { return describeFor(name, t) }
}

// runPicker runs the terminal picker (SPEC §2.3). The picker draws on
// stdout: add and edit are interactive commands, never MCP servers.
func runPicker(ctx context.Context, in pickInput) (pickResult, error) {
	ts, mode, err := tui.RunPicker(ctx, tui.PickerInput{
		API:      in.api,
		Tools:    in.tools,
		Prefix:   in.prefix,
		Mode:     in.mode,
		Selected: in.selected,
		Lint:     in.lint,
		Describe: protoDescribe(in.proto),
	}, os.Stdin, os.Stdout)
	return pickResult{toolset: ts, mode: mode}, err
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
