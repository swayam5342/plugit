package cli

import (
	"context"
	"os"

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
