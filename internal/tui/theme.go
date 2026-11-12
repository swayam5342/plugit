package tui

import (
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/swayam5342/plugit/internal/policy"
)

// theme is the palette and the drawing helpers built on it. Colours are
// true colour, picked for a dark or a light background (the screens ask
// the terminal which it has); Bubble Tea downsamples them for 256- and
// 16-colour terminals and drops them for NO_COLOR.
type theme struct {
	accent, green, yellow, red, blue, cyan, faint, border, onPill color.Color

	Accent, Dim, Bold, OK, Warn, Err, Title, Border, Focus lipgloss.Style
}

func newTheme(dark bool) theme {
	ld := lipgloss.LightDark(dark)
	hex := func(light, dark string) color.Color { return ld(lipgloss.Color(light), lipgloss.Color(dark)) }
	t := theme{
		accent: hex("#C4552F", "#E8875B"),
		green:  hex("#2E7D32", "#8CC98F"),
		yellow: hex("#946200", "#E5C07B"),
		red:    hex("#C62828", "#EF7A7A"),
		blue:   hex("#1F5FAD", "#7AB4F5"),
		cyan:   hex("#00727F", "#6FCBD6"),
		faint:  hex("#6E6E6E", "#8C8C8C"),
		border: hex("#B4B4B4", "#555555"),
		onPill: hex("#FFFFFF", "#1A1A1A"),
	}
	st := lipgloss.NewStyle
	t.Accent = st().Foreground(t.accent)
	t.Dim = st().Foreground(t.faint)
	t.Bold = st().Bold(true)
	t.OK = st().Foreground(t.green)
	t.Warn = st().Foreground(t.yellow)
	t.Err = st().Foreground(t.red)
	t.Title = st().Bold(true)
	t.Border = st().Foreground(t.border)
	t.Focus = st().Foreground(t.accent)
	return t
}

// themeFor reacts to the terminal's answer to tea.RequestBackgroundColor.
func themeFor(msg tea.BackgroundColorMsg) theme { return newTheme(msg.IsDark()) }

// method colours an HTTP method by what it does: reads green, writes blue
// and yellow, deletes red.
func (t theme) method(m string) lipgloss.Style {
	s := lipgloss.NewStyle().Bold(true)
	switch m {
	case "GET":
		return s.Foreground(t.green)
	case "HEAD", "OPTIONS":
		return s.Foreground(t.cyan)
	case "POST":
		return s.Foreground(t.blue)
	case "PUT", "PATCH":
		return s.Foreground(t.yellow)
	case "DELETE":
		return s.Foreground(t.red)
	}
	return s
}

// pill is the policy mode as a coloured label.
func (t theme) pill(m policy.Mode) string {
	bg := t.green
	switch m {
	case policy.ConfirmWrites:
		bg = t.yellow
	case policy.Full:
		bg = t.red
	}
	return lipgloss.NewStyle().Bold(true).Foreground(t.onPill).Background(bg).Padding(0, 1).Render(string(m))
}

// policy colours a per-tool policy override.
func (t theme) policy(p string) lipgloss.Style {
	switch p {
	case "deny":
		return t.Err
	case "allow":
		return t.OK
	}
	return t.Warn
}

// box draws content in a rounded frame exactly w cells wide. Each content
// line is cut or padded to fit.
func (t theme) box(w int, border lipgloss.Style, content ...string) []string {
	w = max(w, 6)
	inner := w - 4
	out := make([]string, 0, len(content)+2)
	out = append(out, border.Render("╭"+strings.Repeat("─", w-2)+"╮"))
	for _, c := range content {
		out = append(out, border.Render("│")+" "+padRight(fit(c, inner), inner)+" "+border.Render("│"))
	}
	return append(out, border.Render("╰"+strings.Repeat("─", w-2)+"╯"))
}

// gauge is a thin bar showing v against limit: green, then yellow past two
// thirds, then red past the limit.
func (t theme) gauge(v, limit, width int) string {
	if width <= 0 || limit <= 0 {
		return ""
	}
	filled := min(v*width/limit, width)
	if v > 0 && filled == 0 {
		filled = 1
	}
	st := t.OK
	switch {
	case v > limit:
		st = t.Err
	case v*3 > limit*2:
		st = t.Warn
	}
	return st.Render(strings.Repeat("━", filled)) + t.Border.Render(strings.Repeat("━", width-filled))
}

// keyGrid lays key/description pairs out in columns, as many as fit.
func (t theme) keyGrid(w int, pairs ...string) []string {
	var items []string
	colW := 0
	for i := 0; i+1 < len(pairs); i += 2 {
		item := t.Bold.Render(pairs[i]) + " " + t.Dim.Render(pairs[i+1])
		items = append(items, item)
		colW = max(colW, ansi.StringWidth(item)+3)
	}
	cols := max((w-2)/max(colW, 1), 1)
	var lines []string
	var b strings.Builder
	n := 0
	for i, item := range items {
		if n%cols == cols-1 || i == len(items)-1 {
			b.WriteString(item)
		} else {
			b.WriteString(padRight(item, colW))
		}
		n++
		if n%cols == 0 {
			lines = append(lines, fit("  "+b.String(), w))
			b.Reset()
		}
	}
	if b.Len() > 0 {
		lines = append(lines, fit("  "+b.String(), w))
	}
	return lines
}

// hints renders key/description pairs on one line, dropping the ones that
// don't fit.
func (t theme) hints(w int, pairs ...string) string {
	var b strings.Builder
	used := 0
	for i := 0; i+1 < len(pairs); i += 2 {
		item := t.Bold.Render(pairs[i]) + " " + t.Dim.Render(pairs[i+1])
		sep := ""
		if used > 0 {
			sep = t.Dim.Render(" · ")
		}
		iw := ansi.StringWidth(sep + item)
		if used+iw > w {
			break
		}
		b.WriteString(sep)
		b.WriteString(item)
		used += iw
	}
	return b.String()
}

// fit cuts a styled line to w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// padRight pads a styled string with spaces to w cells.
func padRight(s string, w int) string {
	if n := w - ansi.StringWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// spread puts left and right on one line of width w, right-aligned. When
// they don't both fit, left wins and right is dropped.
func spread(left, right string, w int) string {
	lw, rw := ansi.StringWidth(left), ansi.StringWidth(right)
	if lw+1+rw > w {
		return fit(left, w)
	}
	return left + strings.Repeat(" ", w-lw-rw) + right
}
