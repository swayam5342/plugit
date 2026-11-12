package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
)

// TryInput configures the Try it screen (SPEC §2.3).
type TryInput struct {
	API     string
	Tool    model.Tool // prepared: hidden params are marked
	Mode    policy.Mode
	Confirm string // non-empty: why the call needs confirmation first
	// Send makes the call with the non-empty fields (name → text as typed)
	// and returns what to show. It is called off the UI goroutine.
	Send func(ctx context.Context, args map[string]string) TryResult
}

// TryResult is one call's outcome, already redacted.
type TryResult struct {
	Request  string // the outgoing request as printed by plugit try
	Status   int
	Response string // the tool result text
	IsError  bool
	Err      string // the call wasn't made or failed before a result
}

type tryResultMsg TryResult

// TryIt is a form generated from a tool's parameters that makes one call
// and shows the redacted request and the response.
type TryIt struct {
	in      TryInput
	params  []model.Param
	fields  []textinput.Model
	focus   int
	asking  bool
	sending bool
	result  *TryResult
	scroll  int
	width   int
	height  int
	th      theme
}

// NewTryIt builds the form. Hidden params (credentials, fixed values) get
// no field: plugit fills them in.
func NewTryIt(in TryInput) *TryIt {
	m := &TryIt{in: in, width: 100, height: 30, th: newTheme(true)}
	for _, p := range in.Tool.Params {
		if p.Hidden {
			continue
		}
		f := textinput.New()
		f.Prompt = ""
		f.Placeholder = paramType(p)
		f.CharLimit = 4096
		f.SetWidth(23) // the field box is 24 cells; the cursor takes one
		m.params = append(m.params, p)
		m.fields = append(m.fields, f)
	}
	m.setFocus(0)
	return m
}

func paramType(p model.Param) string {
	var s struct {
		Type any `json:"type"`
	}
	if json.Unmarshal(p.Schema, &s) == nil {
		switch t := s.Type.(type) {
		case string:
			return t
		case []any:
			parts := make([]string, 0, len(t))
			for _, x := range t {
				parts = append(parts, fmt.Sprint(x))
			}
			return strings.Join(parts, "|")
		}
	}
	return "any"
}

func (m *TryIt) setFocus(i int) {
	if len(m.fields) == 0 {
		return
	}
	m.focus = (i + len(m.fields)) % len(m.fields)
	for j := range m.fields {
		if j == m.focus {
			m.fields[j].Focus()
		} else {
			m.fields[j].Blur()
		}
	}
}

// Init implements tea.Model.
func (m *TryIt) Init() tea.Cmd { return tea.Batch(tea.RequestBackgroundColor, textinput.Blink) }

func (m *TryIt) send() tea.Cmd {
	m.sending, m.scroll = true, 0
	args := map[string]string{}
	for i, f := range m.fields {
		if v := f.Value(); strings.TrimSpace(v) != "" {
			args[m.params[i].Name] = v
		}
	}
	send := m.in.Send
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		return tryResultMsg(send(ctx, args))
	}
}

// Update implements tea.Model.
func (m *TryIt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.th = themeFor(msg)
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tryResultMsg:
		r := TryResult(msg)
		m.result, m.sending = &r, false
		return m, nil
	case tea.KeyPressMsg:
		if m.asking {
			m.asking = false
			if s := msg.String(); s == "y" || s == "Y" {
				return m, m.send()
			}
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "ctrl+s":
			return m, m.trySend()
		case "tab", "down":
			m.setFocus(m.focus + 1)
			return m, nil
		case "shift+tab", "up":
			m.setFocus(m.focus - 1)
			return m, nil
		case "enter":
			if len(m.fields) == 0 || m.focus == len(m.fields)-1 {
				return m, m.trySend()
			}
			m.setFocus(m.focus + 1)
			return m, nil
		case "pgdown", "ctrl+d":
			m.scroll += 5
			return m, nil
		case "pgup", "ctrl+u":
			m.scroll = max(m.scroll-5, 0)
			return m, nil
		}
	}
	if len(m.fields) > 0 {
		var cmd tea.Cmd
		m.fields[m.focus], cmd = m.fields[m.focus].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *TryIt) trySend() tea.Cmd {
	if m.sending || m.in.Send == nil {
		return nil
	}
	if m.in.Confirm != "" {
		m.asking = true
		return nil
	}
	return m.send()
}

// wrapLines splits text into sanitized lines of at most w cells.
func wrapLines(text string, w int) []string {
	var out []string
	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		r := []rune(safe(strings.ReplaceAll(line, "\t", "  ")))
		if len(r) == 0 {
			out = append(out, "")
		}
		for len(r) > 0 {
			n := min(len(r), max(w, 1))
			out = append(out, string(r[:n]))
			r = r[n:]
		}
	}
	return out
}

// View implements tea.Model.
func (m *TryIt) View() tea.View {
	th := m.th
	w := max(m.width, 20)
	t := m.in.Tool
	title := th.Accent.Render("✦ ") + th.Title.Render("plugit") + th.Dim.Render(" · try · ") + th.Title.Render(safe(t.Name))
	sub := th.method(t.Method).Render(t.Method) + " " + safe(t.Path) + th.Dim.Render("  "+safe(t.Description))
	lines := th.box(w, th.Border, spread(title, th.pill(m.in.Mode), w-4), sub)
	lines = append(lines, "")

	// The form.
	if len(m.fields) == 0 {
		lines = append(lines, th.Dim.Render("  No parameters. ctrl+s sends the call."))
	} else {
		lines = append(lines, th.Dim.Render("  Parameters"))
	}
	nameW := 0
	for _, p := range m.params {
		nameW = max(nameW, len([]rune(safe(p.Name)))+2)
	}
	for i, p := range m.params {
		bar, name := "    ", th.Bold
		if i == m.focus {
			bar, name = "  "+th.Accent.Render("› "), th.Accent.Bold(true)
		}
		label := safe(p.Name)
		if p.Required {
			label += " *"
		}
		meta := []string{paramType(p), p.In.String()}
		if p.Required {
			meta = append(meta, "required")
		}
		if p.Desc != "" {
			meta = append(meta, safe(p.Desc))
		}
		field := th.Border.Render("[") + padRight(m.fields[i].View(), 24) + th.Border.Render("]")
		lines = append(lines, fit(bar+name.Render(padRight(label, nameW))+" "+field+"  "+th.Dim.Render(strings.Join(meta, " · ")), w))
	}
	lines = append(lines, "")

	// Status.
	switch {
	case m.asking:
		lines = append(lines, fit("  "+th.Warn.Render(safe(m.in.Confirm)+". Send it? ")+th.Bold.Render("y")+th.Dim.Render("/N"), w))
	case m.sending:
		lines = append(lines, "  "+th.Dim.Render("Sending…"))
	case m.result != nil && m.result.Err != "":
		lines = append(lines, fit("  "+th.Err.Render("✗ "+safe(m.result.Err)), w))
	default:
		lines = append(lines, "")
	}

	// The call: request, then response, in what height is left.
	footer := "  " + th.hints(w-2, "tab", "next field", "ctrl+s", "send", "pgup/pgdn", "scroll", "esc", "quit")
	avail := m.height - len(lines) - 1
	if r := m.result; r != nil && r.Err == "" && avail >= 6 {
		inner := w - 4
		req := wrapLines(r.Request, inner)
		if len(req) == 0 {
			req = []string{"(no request sent)"}
		}
		reqH := min(len(req), max((avail-4)/3, 1))
		if len(req) > reqH {
			req = append(req[:reqH-1], th.Dim.Render(fmt.Sprintf("… %d more lines", len(req)-reqH+1)))
		}
		lines = append(lines, th.box(w, th.Border, append([]string{th.Dim.Render("Request")}, req...)...)...)

		head := th.Dim.Render("Response")
		if r.Status > 0 {
			st := th.OK
			if r.Status >= 400 || r.IsError {
				st = th.Err
			}
			head += "  " + st.Render(fmt.Sprintf("%d", r.Status))
		} else if r.IsError {
			head += "  " + th.Err.Render("error")
		}
		resp := wrapLines(r.Response, inner)
		respH := max(avail-(reqH+3)-3, 1)
		m.scroll = max(min(m.scroll, len(resp)-respH), 0)
		shown := resp[m.scroll:min(m.scroll+respH, len(resp))]
		if more := len(resp) - m.scroll - len(shown); more > 0 {
			head += th.Dim.Render(fmt.Sprintf("  (%d more lines: pgdn)", more))
		}
		lines = append(lines, th.box(w, th.Border, append([]string{head}, shown...)...)...)
	} else if r != nil && r.Err == "" {
		lines = append(lines, fit(th.Dim.Render(fmt.Sprintf("  Response %d (make the window taller to see it)", r.Status)), w))
	}
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines, footer)

	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}

// RunTryIt shows the Try it screen until the user quits.
func RunTryIt(ctx context.Context, in TryInput, r io.Reader, w io.Writer) error {
	_, err := tea.NewProgram(NewTryIt(in), tea.WithContext(ctx), tea.WithInput(r), tea.WithOutput(w)).Run()
	return err
}
