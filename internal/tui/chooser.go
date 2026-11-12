package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Choice is one row of a Chooser.
type Choice struct {
	Label  string
	Detail string // shown faint after the label, e.g. a config path
	Note   string // a short highlighted tag, e.g. "detected"
}

// Chooser is a small inline multi-select (used to pick MCP clients).
type Chooser struct {
	title, subtitle string
	choices         []Choice
	selected        map[int]bool
	cursor          int
	width           int
	done, ok        bool
	th              theme
}

// NewChooser builds a chooser with nothing selected.
func NewChooser(title, subtitle string, choices []Choice) *Chooser {
	return &Chooser{title: title, subtitle: subtitle, choices: choices, selected: map[int]bool{}, width: 80, th: newTheme(true)}
}

// Result returns the chosen indexes in order, and false if the user
// cancelled.
func (c *Chooser) Result() ([]int, bool) {
	if !c.ok {
		return nil, false
	}
	return c.selectedIndexes(), true
}

func (c *Chooser) selectedIndexes() []int {
	var out []int
	for i := range c.choices {
		if c.selected[i] {
			out = append(out, i)
		}
	}
	return out
}

// Init implements tea.Model.
func (c *Chooser) Init() tea.Cmd { return tea.RequestBackgroundColor }

// Update implements tea.Model.
func (c *Chooser) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		c.th = themeFor(msg)
	case tea.WindowSizeMsg:
		c.width = msg.Width
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			c.done = true
			return c, tea.Quit
		case "down", "j", "tab":
			c.cursor = min(c.cursor+1, len(c.choices)-1)
		case "up", "k", "shift+tab":
			c.cursor = max(c.cursor-1, 0)
		case "space", "x":
			c.selected[c.cursor] = !c.selected[c.cursor]
		case "a", "n":
			for i := range c.choices {
				c.selected[i] = msg.String() == "a"
			}
		case "enter":
			if len(c.selectedIndexes()) == 0 && len(c.choices) > 0 {
				c.selected[c.cursor] = true
			}
			c.done, c.ok = true, true
			return c, tea.Quit
		}
	}
	return c, nil
}

// View implements tea.Model.
func (c *Chooser) View() tea.View {
	th := c.th
	w := max(c.width, 20)
	title := th.Accent.Render("✦ ") + th.Title.Render(safe(c.title))
	if c.done {
		// The last frame stays on screen: leave a one-line summary.
		var names []string
		for _, i := range c.selectedIndexes() {
			names = append(names, safe(c.choices[i].Label))
		}
		res := th.Dim.Render("cancelled")
		if c.ok {
			res = strings.Join(names, ", ")
		}
		return tea.NewView(fit(title, w) + "\n" + fit(th.Dim.Render("  ⎿  ")+res, w) + "\n")
	}

	var content []string
	content = append(content, title)
	if c.subtitle != "" {
		content = append(content, th.Dim.Render(safe(c.subtitle)))
	}
	lines := th.box(w, th.Border, content...)
	lines = append(lines, "")

	labelW, noteW := 0, 0
	for _, ch := range c.choices {
		labelW = max(labelW, ansi.StringWidth(safe(ch.Label)))
		noteW = max(noteW, ansi.StringWidth(safe(ch.Note)))
	}
	for i, ch := range c.choices {
		bar, label := "  ", padRight(safe(ch.Label), labelW)
		if i == c.cursor {
			bar, label = th.Accent.Render("› "), th.Bold.Render(label)
		}
		box := th.Dim.Render("○ ")
		if c.selected[i] {
			box = th.Accent.Render("● ")
		}
		line := "  " + bar + box + label + "  " + padRight(th.OK.Render(safe(ch.Note)), noteW)
		if ch.Detail != "" {
			line += "  " + th.Dim.Render(safe(ch.Detail))
		}
		lines = append(lines, fit(line, w))
	}
	lines = append(lines, "")
	n := len(c.selectedIndexes())
	left := "  " + th.Dim.Render(fmt.Sprintf("%d selected", n))
	lines = append(lines, fit(left+"   "+th.hints(w-ansi.StringWidth(left)-3, "space", "toggle", "a/n", "all/none", "enter", "confirm", "esc", "cancel"), w))
	return tea.NewView(strings.Join(lines, "\n"))
}

// RunChooser shows a chooser on a terminal and returns the chosen indexes,
// or ErrCancelled.
func RunChooser(ctx context.Context, title, subtitle string, choices []Choice, r io.Reader, w io.Writer) ([]int, error) {
	c := NewChooser(title, subtitle, choices)
	if _, err := tea.NewProgram(c, programOptions(ctx, r, w)...).Run(); err != nil {
		return nil, err
	}
	idx, ok := c.Result()
	if !ok {
		return nil, ErrCancelled
	}
	return idx, nil
}
