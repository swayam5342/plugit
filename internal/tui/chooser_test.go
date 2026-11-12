package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func testChoices() []Choice {
	return []Choice{
		{Label: "Claude Desktop", Detail: `C:\Users\me\AppData\Roaming\Claude\claude_desktop_config.json`, Note: "detected"},
		{Label: "Claude Code", Detail: "claude mcp add", Note: "detected"},
		{Label: "OpenAI Codex", Detail: "codex mcp add"},
		{Label: "Cursor", Detail: `C:\Users\me\.cursor\mcp.json`},
	}
}

func newChooserTest() *Chooser {
	c := NewChooser("install pets", "Choose the MCP clients to add it to", testChoices())
	c.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return c
}

func pressC(c *Chooser, keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		_, cmd = c.Update(key(k))
	}
	return cmd
}

func chooserView(c *Chooser) string { return ansi.Strip(c.View().Content) }

func TestChooserShowsChoices(t *testing.T) {
	v := chooserView(newChooserTest())
	for _, want := range []string{"install pets", "Choose the MCP clients", "Claude Desktop", "OpenAI Codex", "detected", "codex mcp add", "space"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

func TestChooserSelect(t *testing.T) {
	c := newChooserTest()
	if !isQuit(pressC(c, "space", "down", "down", "space", "enter")) {
		t.Fatal("enter should finish")
	}
	if got, ok := c.Result(); !ok || !slices.Equal(got, []int{0, 2}) {
		t.Errorf("result = %v, %v", got, ok)
	}
}

// With nothing ticked, enter takes the row under the cursor.
func TestChooserEnterPicksCursor(t *testing.T) {
	c := newChooserTest()
	pressC(c, "down", "enter")
	if got, ok := c.Result(); !ok || !slices.Equal(got, []int{1}) {
		t.Errorf("result = %v, %v", got, ok)
	}
}

func TestChooserAllNone(t *testing.T) {
	c := newChooserTest()
	pressC(c, "a")
	if got := c.selectedIndexes(); len(got) != 4 {
		t.Errorf("a selected %v", got)
	}
	pressC(c, "n")
	if got := c.selectedIndexes(); len(got) != 0 {
		t.Errorf("n left %v", got)
	}
}

func TestChooserCancel(t *testing.T) {
	for _, k := range []string{"esc", "q"} {
		c := newChooserTest()
		pressC(c, "space")
		if !isQuit(pressC(c, k)) {
			t.Fatalf("%s should quit", k)
		}
		if _, ok := c.Result(); ok {
			t.Errorf("%s counted as a choice", k)
		}
	}
}

func TestChooserFitsWidth(t *testing.T) {
	c := NewChooser("install pets", "Choose", testChoices())
	c.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	for _, l := range strings.Split(c.View().Content, "\n") {
		if w := ansi.StringWidth(l); w > 40 {
			t.Errorf("line is %d wide: %q", w, ansi.Strip(l))
		}
	}
}
