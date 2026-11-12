package tui

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// ? shows the full key list, and the screen still fits the window.
func TestPickerShortcuts(t *testing.T) {
	p := newTest("GET /pets")
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	if strings.Contains(view(p), "tool policy") {
		t.Fatal("shortcuts shown before ?")
	}
	press(p, "?")
	v := view(p)
	if !strings.Contains(v, "tool policy") || !strings.Contains(v, "? to hide") {
		t.Errorf("shortcuts missing:\n%s", v)
	}
	if lines := strings.Count(v, "\n") + 1; lines > 24 {
		t.Errorf("view has %d lines for a 24-line window", lines)
	}
	press(p, "?")
	if strings.Contains(view(p), "tool policy") {
		t.Error("? didn't hide the shortcuts")
	}
}

// The palette follows the terminal background; switching it changes
// colours only, never the text.
func TestPickerTheme(t *testing.T) {
	p := newTest("GET /pets")
	if p.Init() == nil {
		t.Error("Init should ask for the background colour")
	}
	dark := p.View().Content
	p.Update(tea.BackgroundColorMsg{Color: color.White})
	light := p.View().Content
	if dark == light || ansi.Strip(dark) != ansi.Strip(light) {
		t.Error("light palette should change colours only")
	}
}

// Boxes are exactly as wide as asked, whatever is inside.
func TestBoxWidth(t *testing.T) {
	th := newTheme(true)
	for _, w := range []int{6, 20, 57} {
		for _, l := range th.box(w, th.Border, "short", strings.Repeat("long ", 30), th.Accent.Render("styled")) {
			if got := ansi.StringWidth(l); got != w {
				t.Errorf("box(%d) line is %d wide: %q", w, got, ansi.Strip(l))
			}
		}
	}
}
