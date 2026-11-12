package tui

import (
	"context"
	"io"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

var noColor atomic.Bool

// SetNoColor turns colour off (or back on) for every screen: the global
// --no-color flag. NO_COLOR in the environment works without it.
func SetNoColor(on bool) { noColor.Store(on) }

// NoColor reports whether --no-color is in effect.
func NoColor() bool { return noColor.Load() }

// programOptions are the options every screen runs with.
func programOptions(ctx context.Context, r io.Reader, w io.Writer) []tea.ProgramOption {
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithInput(r), tea.WithOutput(w)}
	if NoColor() {
		// ASCII keeps bold and faint text but drops every colour.
		opts = append(opts, tea.WithColorProfile(colorprofile.ASCII))
	}
	return opts
}
