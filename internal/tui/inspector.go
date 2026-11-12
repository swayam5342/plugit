package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/swayam5342/plugit/internal/store"
)

// maxInspectorCalls caps how many calls the inspector keeps in memory.
const maxInspectorCalls = 2000

// InspectorInput configures the call inspector (SPEC §2.3).
type InspectorInput struct {
	Title    string // the API name, or "all APIs"
	AllAPIs  bool   // show the API column
	Source   func(ctx context.Context, afterID int64) ([]store.Call, error)
	Interval time.Duration // how often to poll Source
}

// CallsMsg delivers calls newer than the last ones seen (or a read error).
type CallsMsg struct {
	Calls []store.Call
	Err   error
}

type pollMsg struct{}

// Inspector is a live tail of the call log: a Bubble Tea model.
type Inspector struct {
	in       InspectorInput
	calls    []store.Call
	lastID   int64
	cursor   int
	follow   bool // cursor stays on the newest call
	expanded bool
	err      error
	offset   int
	width    int
	height   int
	th       theme
}

// NewInspector builds an inspector.
func NewInspector(in InspectorInput) *Inspector {
	if in.Interval <= 0 {
		in.Interval = 500 * time.Millisecond
	}
	return &Inspector{in: in, follow: true, width: 100, height: 30, th: newTheme(true)}
}

// Init implements tea.Model: ask for the background colour and read the log.
func (m *Inspector) Init() tea.Cmd { return tea.Batch(tea.RequestBackgroundColor, m.fetch()) }

func (m *Inspector) fetch() tea.Cmd {
	after, src := m.lastID, m.in.Source
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		calls, err := src(ctx, after)
		return CallsMsg{Calls: calls, Err: err}
	}
}

func (m *Inspector) poll() tea.Cmd {
	return tea.Tick(m.in.Interval, func(time.Time) tea.Msg { return pollMsg{} })
}

func (m *Inspector) current() store.Call {
	if m.cursor < len(m.calls) {
		return m.calls[m.cursor]
	}
	return store.Call{}
}

// Update implements tea.Model.
func (m *Inspector) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.th = themeFor(msg)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clamp()
	case pollMsg:
		return m, m.fetch()
	case CallsMsg:
		m.err = msg.Err
		for _, c := range msg.Calls {
			if c.ID > m.lastID {
				m.calls = append(m.calls, c)
				m.lastID = c.ID
			}
		}
		if drop := len(m.calls) - maxInspectorCalls; drop > 0 {
			m.calls = slices.Delete(m.calls, 0, drop)
			m.cursor = max(m.cursor-drop, 0)
		}
		if m.follow {
			m.cursor = max(len(m.calls)-1, 0)
		}
		m.clamp()
		return m, m.poll()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			if msg.String() == "esc" && m.expanded {
				m.expanded = false
				break
			}
			return m, tea.Quit
		case "up", "k":
			m.cursor = max(m.cursor-1, 0)
		case "down", "j":
			m.cursor = min(m.cursor+1, max(len(m.calls)-1, 0))
		case "pgup":
			m.cursor = max(m.cursor-m.listHeight(), 0)
		case "pgdown":
			m.cursor = min(m.cursor+m.listHeight(), max(len(m.calls)-1, 0))
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			m.cursor = max(len(m.calls)-1, 0)
		case "enter", "space":
			m.expanded = !m.expanded
		}
		m.follow = m.cursor >= len(m.calls)-1
		m.clamp()
	}
	return m, nil
}

// Layout: header box (3) + blank, list, detail panel, status bar.
const inspectorChrome = 6

func (m *Inspector) detailLines() []string {
	if !m.expanded || len(m.calls) == 0 {
		return nil
	}
	return m.detail(max(m.width, 20))
}

func (m *Inspector) listHeight() int {
	return max(m.height-inspectorChrome-len(m.detailLines()), 1)
}

func (m *Inspector) clamp() {
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = max(min(m.offset, len(m.calls)-h), 0)
}

func (m *Inspector) outcomeStyle(o store.Outcome) lipgloss.Style {
	switch o {
	case store.OK:
		return m.th.OK
	case store.Denied, store.Invalid:
		return m.th.Warn
	}
	return m.th.Err
}

// HumanBytes formats a byte count for people.
func HumanBytes(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.1f KB", float64(n)/1000)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/1000/1000)
}

// Millis formats a latency.
func Millis(d time.Duration) string {
	if d < 10*time.Millisecond {
		return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

func (m *Inspector) row(c store.Call, cursor bool) string {
	th := m.th
	bar := "  "
	name := th.Bold
	if cursor {
		bar, name = th.Accent.Render("› "), th.Accent.Bold(true)
	}
	status := "   "
	if c.Status > 0 {
		status = fmt.Sprintf("%3d", c.Status)
	}
	line := bar + th.Dim.Render(c.Time.Local().Format("15:04:05")) + "  "
	if m.in.AllAPIs {
		line += th.Dim.Render(fmt.Sprintf("%-12s ", truncate(safe(c.API), 12)))
	}
	line += name.Render(fmt.Sprintf("%-28s", truncate(safe(c.Tool), 28))) + " " +
		m.outcomeStyle(c.Outcome).Render(fmt.Sprintf("%-10s %s", c.Outcome, status)) + "  " +
		th.Dim.Render(fmt.Sprintf("%7s  %8s", Millis(c.Duration), HumanBytes(int64(c.Bytes))))
	return line
}

// detail is the expanded view of the call under the cursor.
func (m *Inspector) detail(w int) []string {
	th := m.th
	c := m.current()
	label := func(s string) string { return th.Dim.Render(fmt.Sprintf("%-8s ", s)) }
	content := []string{
		th.method(strings.SplitN(c.Op, " ", 2)[0]).Render(safe(c.Op)) + th.Dim.Render(fmt.Sprintf("  ·  %s/%s  ·  %s", safe(c.API), safe(c.Toolset), c.Time.Local().Format("2006-01-02 15:04:05"))),
	}
	args := "(not logged: log.args is none)"
	if len(c.Args) > 0 {
		args = oneLineJSON(c.Args)
	}
	content = append(content, label("args")+safe(args))
	if c.Error != "" {
		content = append(content, label("error")+th.Err.Render(safe(c.Error)))
	}
	preview := th.Dim.Render("(off: set log.response_preview: true to keep one)")
	if c.Preview != "" {
		preview = safe(strings.Join(strings.Fields(c.Preview), " "))
	}
	content = append(content, label("preview")+preview)
	return m.th.box(w, m.th.Border, content...)
}

func oneLineJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}

// View implements tea.Model.
func (m *Inspector) View() tea.View {
	th := m.th
	w := max(m.width, 20)
	var lines []string

	live := th.OK.Render("● live")
	if m.err != nil {
		live = th.Err.Render("● " + truncate(safe(m.err.Error()), 60))
	}
	title := th.Accent.Render("✦ ") + th.Title.Render("plugit") + th.Dim.Render(" · logs · ") + th.Title.Render(safe(m.in.Title))
	lines = append(lines, th.box(w, th.Border, spread(title, live, w-4))...)

	h := m.listHeight()
	if len(m.calls) == 0 {
		lines = append(lines, fit(th.Dim.Render("  Waiting for calls… they appear here as an MCP client uses the tools."), w))
		h--
	}
	for i := m.offset; i < min(m.offset+h, len(m.calls)); i++ {
		lines = append(lines, fit(m.row(m.calls[i], i == m.cursor), w))
	}
	for range h - max(min(len(m.calls)-m.offset, h), 0) {
		lines = append(lines, "")
	}
	lines = append(lines, m.detailLines()...)

	errs := 0
	for _, c := range m.calls {
		if c.Outcome != store.OK {
			errs++
		}
	}
	errWord := "errors"
	if errs == 1 {
		errWord = "error"
	}
	stats := th.Dim.Render(fmt.Sprintf("  %d calls · %d %s", len(m.calls), errs, errWord))
	keys := th.hints(w/2, "↑↓", "move", "enter", "details", "G", "newest", "q", "quit")
	lines = append(lines, spread(stats, keys, w))

	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}

// RunInspector shows the inspector until the user quits.
func RunInspector(ctx context.Context, in InspectorInput, r io.Reader, w io.Writer) error {
	_, err := tea.NewProgram(NewInspector(in), tea.WithContext(ctx), tea.WithInput(r), tea.WithOutput(w)).Run()
	return err
}
