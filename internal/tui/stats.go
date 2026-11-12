package tui

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/swayam5342/plugit/internal/store"
)

// StatsInput configures the stats screen (SPEC §2.3).
type StatsInput struct {
	Title    string
	Source   func(ctx context.Context, since time.Time) ([]store.ToolStats, error)
	Budgets  map[string]string // per API, e.g. "≈2.3k tokens · 8 tools"
	Interval time.Duration     // refresh period
	Now      func() time.Time
}

// StatsMsg delivers fresh numbers (or a read error).
type StatsMsg struct {
	Stats []store.ToolStats
	Err   error
}

type statsTick struct{}

// Windows the stats screen offers, by number key.
var statsWindows = []struct {
	label string
	d     time.Duration
}{
	{"last hour", time.Hour},
	{"last 24 hours", 24 * time.Hour},
	{"last 7 days", 7 * 24 * time.Hour},
	{"last 30 days", 30 * 24 * time.Hour},
}

var statsSorts = []string{"calls", "errors", "p95", "latest"}

// Stats is the per-API, per-tool summary of the call log.
type Stats struct {
	in     StatsInput
	stats  []store.ToolStats
	err    error
	window int
	sort   int
	offset int
	width  int
	height int
	th     theme
}

// NewStats builds the stats screen, showing the last 7 days.
func NewStats(in StatsInput) *Stats {
	if in.Interval <= 0 {
		in.Interval = 2 * time.Second
	}
	if in.Now == nil {
		in.Now = time.Now
	}
	return &Stats{in: in, window: 2, width: 100, height: 30, th: newTheme(true)}
}

// Init implements tea.Model.
func (s *Stats) Init() tea.Cmd { return tea.Batch(tea.RequestBackgroundColor, s.fetch()) }

func (s *Stats) fetch() tea.Cmd {
	src, since := s.in.Source, s.in.Now().Add(-statsWindows[s.window].d)
	return func() tea.Msg {
		if src == nil {
			return StatsMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		st, err := src(ctx, since)
		return StatsMsg{Stats: st, Err: err}
	}
}

// Update implements tea.Model.
func (s *Stats) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		s.th = themeFor(msg)
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case statsTick:
		return s, s.fetch()
	case StatsMsg:
		s.stats, s.err = msg.Stats, msg.Err
		return s, tea.Tick(s.in.Interval, func(time.Time) tea.Msg { return statsTick{} })
	case tea.KeyPressMsg:
		switch k := msg.String(); k {
		case "ctrl+c", "q", "esc":
			return s, tea.Quit
		case "1", "2", "3", "4":
			s.window = int(k[0] - '1')
			s.offset = 0
			return s, s.fetch()
		case "s":
			s.sort = (s.sort + 1) % len(statsSorts)
		case "down", "j":
			s.offset++
		case "up", "k":
			s.offset = max(s.offset-1, 0)
		case "home", "g":
			s.offset = 0
		}
	}
	return s, nil
}

type apiGroup struct {
	api    string
	tools  []store.ToolStats
	calls  int
	errors int
	p95    time.Duration
	last   time.Time
}

func (s *Stats) groups() []apiGroup {
	by := map[string]*apiGroup{}
	for _, t := range s.stats {
		g := by[t.API]
		if g == nil {
			g = &apiGroup{api: t.API}
			by[t.API] = g
		}
		g.tools = append(g.tools, t)
		g.calls += t.Calls
		g.errors += t.Errors
		g.p95 = max(g.p95, t.P95)
		if t.Last.After(g.last) {
			g.last = t.Last
		}
	}
	out := make([]apiGroup, 0, len(by))
	for _, g := range by {
		slices.SortFunc(g.tools, func(a, b store.ToolStats) int {
			return cmp.Or(s.compare(a.Calls, a.Errors, a.P95, a.Last, b.Calls, b.Errors, b.P95, b.Last), cmp.Compare(a.Tool, b.Tool))
		})
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b apiGroup) int {
		return cmp.Or(s.compare(a.calls, a.errors, a.p95, a.last, b.calls, b.errors, b.p95, b.last), cmp.Compare(a.api, b.api))
	})
	return out
}

// compare orders by the current sort key, biggest first.
func (s *Stats) compare(ac, ae int, ap time.Duration, al time.Time, bc, be int, bp time.Duration, bl time.Time) int {
	switch statsSorts[s.sort] {
	case "errors":
		return cmp.Or(cmp.Compare(be, ae), cmp.Compare(bc, ac))
	case "p95":
		return cmp.Or(cmp.Compare(bp, ap), cmp.Compare(bc, ac))
	case "latest":
		return cmp.Or(bl.Compare(al), cmp.Compare(bc, ac))
	}
	return cmp.Or(cmp.Compare(bc, ac), cmp.Compare(be, ae))
}

func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func errRate(errors, calls int) string {
	if calls == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", (errors*100+calls/2)/calls)
}

func (s *Stats) body(w int) []string {
	th := s.th
	now := s.in.Now()
	var lines []string
	for i, g := range s.groups() {
		if i > 0 {
			lines = append(lines, "")
		}
		head := th.Accent.Bold(true).Render(safe(g.api)) + th.Dim.Render(fmt.Sprintf("  %d calls · %s errors · p95 %s", g.calls, errRate(g.errors, g.calls), Millis(g.p95)))
		if b := s.in.Budgets[g.api]; b != "" {
			head += th.Dim.Render(" · " + b)
		}
		lines = append(lines, fit("  "+head, w))
		lines = append(lines, fit(th.Dim.Render(fmt.Sprintf("    %-30s %6s %7s %8s %8s %9s  %s", "tool", "calls", "errors", "p50", "p95", "bytes", "last")), w))
		for _, t := range g.tools {
			rate := th.Dim.Render(fmt.Sprintf("%7s", errRate(t.Errors, t.Calls)))
			if t.Errors > 0 {
				rate = th.Err.Render(fmt.Sprintf("%7s", errRate(t.Errors, t.Calls)))
			}
			line := fmt.Sprintf("    %-30s %6d ", truncate(safe(t.Tool), 30), t.Calls) + rate +
				fmt.Sprintf(" %8s %8s %9s  ", Millis(t.P50), Millis(t.P95), HumanBytes(t.Bytes)) + th.Dim.Render(ago(now, t.Last))
			lines = append(lines, fit(line, w))
		}
	}
	return lines
}

// View implements tea.Model.
func (s *Stats) View() tea.View {
	th := s.th
	w := max(s.width, 20)
	calls, errs := 0, 0
	for _, t := range s.stats {
		calls += t.Calls
		errs += t.Errors
	}
	title := th.Accent.Render("✦ ") + th.Title.Render("plugit") + th.Dim.Render(" · stats · ") + th.Title.Render(safe(s.in.Title))
	sub := fmt.Sprintf("%d calls · %d errors · sorted by %s", calls, errs, statsSorts[s.sort])
	if s.err != nil {
		sub = th.Err.Render(safe(s.err.Error()))
	}
	lines := th.box(w, th.Border, spread(title, th.Accent.Render(statsWindows[s.window].label), w-4), th.Dim.Render(sub))
	lines = append(lines, "")

	body := s.body(w)
	if len(body) == 0 {
		body = []string{fit(th.Dim.Render("  No calls in this window. Calls appear once an MCP client uses `plugit serve`."), w)}
	}
	h := max(s.height-len(lines)-1, 1)
	s.offset = max(min(s.offset, len(body)-h), 0)
	shown := body[s.offset:min(s.offset+h, len(body))]
	lines = append(lines, shown...)
	for range h - len(shown) {
		lines = append(lines, "")
	}
	lines = append(lines, "  "+th.hints(w-2, "1-4", "window", "s", "sort", "↑↓", "scroll", "q", "quit"))

	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}

// RunStats shows the stats screen until the user quits.
func RunStats(ctx context.Context, in StatsInput, r io.Reader, w io.Writer) error {
	_, err := tea.NewProgram(NewStats(in), tea.WithContext(ctx), tea.WithInput(r), tea.WithOutput(w)).Run()
	return err
}
