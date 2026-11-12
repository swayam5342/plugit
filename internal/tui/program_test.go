package tui

import (
	"bytes"
	"testing"
)

// --no-color forces the ASCII profile (text styles, no colours) on every
// screen; NO_COLOR is honoured by the colour profile detection itself.
func TestNoColor(t *testing.T) {
	t.Cleanup(func() { SetNoColor(false) })
	base := len(programOptions(t.Context(), &bytes.Buffer{}, &bytes.Buffer{}))
	SetNoColor(true)
	if !NoColor() || len(programOptions(t.Context(), &bytes.Buffer{}, &bytes.Buffer{})) != base+1 {
		t.Error("--no-color didn't add a colour profile")
	}
	SetNoColor(false)
	if NoColor() {
		t.Error("SetNoColor(false) ignored")
	}
}
