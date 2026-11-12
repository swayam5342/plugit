package tui

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/swayam5342/plugit/internal/curate"
	"github.com/swayam5342/plugit/internal/model"
)

// SyncInput configures the Sync diff screen (SPEC §2.3).
type SyncInput struct {
	API      string
	Diff     curate.SyncDiff
	BaseURL  string // pinned; sync never changes it
	Toolset  string // where chosen new operations go
	LintNote string // e.g. "2 lint errors in tools you serve", or ""
}

// SyncScreen shows what a new spec changes and lets the user pick which
// new operations to add. Overrides are always kept.
type SyncScreen struct {
	in       SyncInput
	selected map[int]bool
	cursor   int
	offset   int
	done, ok bool
	width    int
	height   int
	th       theme
}

// NewSyncScreen builds the screen with no new operations chosen.
func NewSyncScreen(in SyncInput) *SyncScreen {
	return &SyncScreen{in: in, selected: map[int]bool{}, width: 100, height: 30, th: newTheme(true)}
}

// Result returns the chosen new operations and whether to apply.
func (s *SyncScreen) Result() ([]model.OpKey, bool) {
	if !s.ok {
		return nil, false
	}
	var out []model.OpKey
	for i, a := range s.in.Diff.Added {
		if s.selected[i] {
			out = append(out, a.Key)
		}
	}
	return out, true
}

// Init implements tea.Model.
func (s *SyncScreen) Init() tea.Cmd { return tea.RequestBackgroundColor }

// Update implements tea.Model.
func (s *SyncScreen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	n := len(s.in.Diff.Added)
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		s.th = themeFor(msg)
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			s.done = true
			return s, tea.Quit
		case "enter":
			s.done, s.ok = true, true
			return s, tea.Quit
		case "down", "j":
			s.cursor = min(s.cursor+1, max(n-1, 0))
		case "up", "k":
			s.cursor = max(s.cursor-1, 0)
		case "space", "x":
			if n > 0 {
				s.selected[s.cursor] = !s.selected[s.cursor]
			}
		case "a", "n":
			for i := range n {
				s.selected[i] = msg.String() == "a"
			}
		case "pgdown":
			s.offset += 5
		case "pgup":
			s.offset = max(s.offset-5, 0)
		}
	}
	return s, nil
}

func opLine(t *model.Tool) string {
	if t == nil {
		return ""
	}
	return safe(string(t.Key))
}

// body is the scrollable part; cursorLine is the line of the current
// added operation (-1 if none).
func (s *SyncScreen) body(w int) (lines []string, cursorLine int) {
	th, d := s.th, s.in.Diff
	cursorLine = -1
	add := func(l string) { lines = append(lines, fit(l, w)) }

	if d.ServersChanged {
		before, after := strings.Join(d.ServersBefore, ", "), strings.Join(d.ServersAfter, ", ")
		add("  " + th.Warn.Render("! The spec's servers changed: ") + safe(cmp.Or(before, "(none)")) + th.Dim.Render(" → ") + safe(cmp.Or(after, "(none)")))
		add("    " + th.Dim.Render("base_url stays "+safe(s.in.BaseURL)+" (sync never changes it; edit plugit.yaml if you trust the new server)"))
		if !d.BaseURLListed {
			add("    " + th.Warn.Render("The new spec no longer lists your base_url."))
		}
		add("")
	}
	if s.in.LintNote != "" {
		add("  " + th.Err.Render("✗ "+safe(s.in.LintNote)))
		add("")
	}

	add("  " + th.Accent.Bold(true).Render(fmt.Sprintf("Added (%d)", len(d.Added))) + th.Dim.Render("  space adds one to toolset "+safe(s.in.Toolset)))
	for i, a := range d.Added {
		bar := "    "
		if i == s.cursor {
			bar = "  " + th.Accent.Render("› ")
			cursorLine = len(lines)
		}
		box := th.Dim.Render("○ ")
		if s.selected[i] {
			box = th.Accent.Render("● ")
		}
		desc := ""
		name := ""
		if a.New != nil {
			desc, name = safe(a.New.Description), safe(a.New.Name)
		}
		add(bar + box + th.method(a.Key.Method()).Render(fmt.Sprintf("%-7s", a.Key.Method())) + " " +
			fmt.Sprintf("%-32s", truncate(safe(a.Key.Path()), 32)) + " " + th.Bold.Render(name) + th.Dim.Render("  "+desc))
	}
	add("")

	add("  " + th.Accent.Bold(true).Render(fmt.Sprintf("Changed (%d)", len(d.Changed))))
	for _, c := range d.Changed {
		where := th.Dim.Render("  not in a toolset")
		if len(c.Listed) > 0 {
			where = th.Warn.Render("  served in " + strings.Join(c.Listed, ", "))
		}
		add("    " + opLine(c.New) + th.Dim.Render("  "+strings.Join(c.What, ", ")) + where)
		if c.New != nil && len(c.Listed) > 0 && slices.Contains(c.What, "description") {
			add("      " + th.Dim.Render("new description: ") + safe(c.New.Description))
		}
	}
	add("")

	add("  " + th.Accent.Bold(true).Render(fmt.Sprintf("Removed (%d)", len(d.Removed))))
	for _, r := range d.Removed {
		note := th.Dim.Render("  not in a toolset")
		if len(r.Listed) > 0 {
			note = th.Dim.Render("  listed in " + strings.Join(r.Listed, ", ") + "; its overrides are kept in plugit.yaml")
		}
		add("    " + opLine(r.Old) + note)
	}
	return lines, cursorLine
}

// View implements tea.Model.
func (s *SyncScreen) View() tea.View {
	th := s.th
	w := max(s.width, 20)
	d := s.in.Diff
	title := th.Accent.Render("✦ ") + th.Title.Render("plugit") + th.Dim.Render(" · sync · ") + th.Title.Render(safe(s.in.API))
	count := th.Dim.Render(fmt.Sprintf("+%d ~%d -%d", len(d.Added), len(d.Changed), len(d.Removed)))
	lines := th.box(w, th.Border, spread(title, count, w-4), th.Dim.Render("Review the new spec. Every override in plugit.yaml is kept."))

	body, cur := s.body(w)
	h := max(s.height-len(lines)-1, 1)
	if cur >= 0 {
		if cur < s.offset {
			s.offset = cur
		}
		if cur >= s.offset+h {
			s.offset = cur - h + 1
		}
	}
	s.offset = max(min(s.offset, len(body)-h), 0)
	shown := body[s.offset:min(s.offset+h, len(body))]
	lines = append(lines, shown...)
	for range h - len(shown) {
		lines = append(lines, "")
	}
	lines = append(lines, "  "+th.hints(w-2, "space", "toggle", "a/n", "all/none", "enter", "apply", "pgup/pgdn", "scroll", "q", "cancel"))

	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}

// RunSync shows the screen and returns the chosen new operations, or
// ErrCancelled.
func RunSync(ctx context.Context, in SyncInput, r io.Reader, w io.Writer) ([]model.OpKey, error) {
	s := NewSyncScreen(in)
	if _, err := tea.NewProgram(s, tea.WithContext(ctx), tea.WithInput(r), tea.WithOutput(w)).Run(); err != nil {
		return nil, err
	}
	add, ok := s.Result()
	if !ok {
		return nil, ErrCancelled
	}
	return add, nil
}
