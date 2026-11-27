package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/swayam5342/plugit/internal/lint"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/tokens"
)

type seg struct {
	text  string
	style lipgloss.Style
}

func plain(ss []seg) string {
	var b strings.Builder
	for _, s := range ss {
		b.WriteString(s.text)
	}
	return b.String()
}

func styled(ss []seg) string {
	var b strings.Builder
	for _, s := range ss {
		b.WriteString(s.style.Render(s.text))
	}
	return b.String()
}

const pathWidth = 34

const (
	headerLines = 5
	footerLines = 6
)

func (p *Picker) helpLines() []string {
	if !p.showHelp {
		return nil
	}
	return p.th.keyGrid(max(p.width, 20),
		"↑↓ j k", "move", "space", "toggle tool or group", "enter", "fold group",
		"a / n", "all / none shown", "/", "filter", "esc", "clear filter",
		"r", "rename", "e", "edit description", "p", "tool policy", "m", "API mode",
		"i", "tool detail", "f", "response fields", "s", "save", "q", "quit", "?", "hide shortcuts")
}

func (p *Picker) listHeight() int {
	return max(p.height-headerLines-footerLines-len(p.helpLines()), 3)
}

// rowSegs is a row as styled pieces. Its plain text is what tests and the
// filter see; the colours only add emphasis.
func (p *Picker) rowSegs(r row, cursor bool) []seg {
	th, none := p.th, lipgloss.NewStyle()
	if r.kind == rowGroup {
		g := p.groups[r.group]
		sel := 0
		for _, ti := range g.tools {
			if p.selected[p.tools[ti].Key] {
				sel++
			}
		}
		arrow := "▾ "
		if p.collapsed[r.group] && p.filter == "" {
			arrow = "▸ "
		}
		count := th.Dim
		if sel > 0 {
			count = th.OK
		}
		return []seg{
			{arrow, th.Dim},
			{safe(g.name), th.Accent.Bold(true)},
			{fmt.Sprintf("  %d/%d", sel, len(g.tools)), count},
		}
	}

	t := p.tools[r.tool]
	offered := policy.Lists(t.Safety, p.policyMode)
	on := p.selected[t.Key]
	box := seg{"○ ", th.Dim}
	if on {
		box = seg{"● ", th.Accent}
	}
	badge, target := describe(p.in.Describe, t)
	method, name := th.method(badge), none
	if cursor {
		name = th.Bold
	}
	if !offered {
		method, name = th.Dim, th.Dim
	}
	ss := []seg{
		box,
		{fmt.Sprintf("%-7s ", safe(badge)), method},
		{fmt.Sprintf("%-*s ", pathWidth, truncate(safe(target), pathWidth)), th.Dim},
		{fmt.Sprintf("%-*s", p.nameWidth, safe(p.displayName(t))), name},
	}
	if n, ok := p.toolTokens[t.Key]; ok && on {
		ss = append(ss, seg{"  " + tokens.Format(n), th.Dim})
	}
	if o := p.over[t.Key]; o.Name != "" || o.Description != "" {
		ss = append(ss, seg{"  ✎", th.Accent})
	}
	if o := p.over[t.Key]; o.Policy != "" {
		ss = append(ss, seg{"  " + safe(o.Policy), th.policy(o.Policy)})
	}
	if n := len(p.over[t.Key].ResponseFields); n > 0 {
		unit := "fields"
		if n == 1 {
			unit = "field"
		}
		ss = append(ss, seg{fmt.Sprintf("  %d %s", n, unit), th.Accent})
	}
	if !offered {
		ss = append(ss, seg{"  not offered in " + string(p.policyMode), th.Warn})
	}
	if sev, ok := p.lintWorst[t.Key]; ok && on && sev > lint.Info {
		if sev == lint.Error {
			ss = append(ss, seg{"  ✗ lint", th.Err})
		} else {
			ss = append(ss, seg{"  ! lint", th.Warn})
		}
	}
	return ss
}

// rowText is a row as plain text.
func (p *Picker) rowText(r row) string { return plain(p.rowSegs(r, false)) }

func (p *Picker) currentRowText() string {
	if r, ok := p.current(); ok {
		return p.rowText(r)
	}
	return ""
}

func (p *Picker) footerText() string {
	unit := "tools"
	if p.count == 1 {
		unit = "tool"
	}
	f := fmt.Sprintf("%d %s · %s tokens · %d write", p.count, unit, tokens.Format(p.budget), p.writes)
	if off := p.count - p.offered; off > 0 {
		f += fmt.Sprintf(" · %d not offered", off)
	}
	switch p.lintErrors {
	case 0:
	case 1:
		f += " · 1 lint error"
	default:
		f += fmt.Sprintf(" · %d lint errors", p.lintErrors)
	}
	return f
}

// View implements tea.Model.
func (p *Picker) View() tea.View {
	if p.detail != nil {
		return p.detailView()
	}
	th := p.th
	w := max(p.width, 20)
	var lines []string

	title := th.Accent.Render("✦ ") + th.Title.Render("plugit") + th.Dim.Render(" · ") + th.Title.Render(safe(p.in.API))
	if p.dirty {
		title += th.Warn.Render("  ● unsaved")
	}
	sub := fmt.Sprintf("Pick the tools to expose · %d operations", len(p.tools))
	if p.in.Prefix != "" {
		sub += " · prefix " + safe(p.in.Prefix)
	}
	lines = append(lines, th.box(w, th.Border, spread(title, th.pill(p.policyMode), w-4), th.Dim.Render(sub))...)
	lines = append(lines, "")

	h := p.listHeight()
	for i := p.offset; i < min(p.offset+h, len(p.rows)); i++ {
		bar := "  "
		if i == p.cursor {
			bar = th.Accent.Render("› ")
		}
		if p.rows[i].kind == rowTool {
			bar = "  " + bar
		}
		lines = append(lines, fit(bar+styled(p.rowSegs(p.rows[i], i == p.cursor)), w))
	}
	shown := max(min(len(p.rows)-p.offset, h), 0)
	if len(p.rows) == 0 {
		lines = append(lines, fit(th.Dim.Render("  No tools match “"+safe(p.filter)+"”. esc clears the filter."), w))
		shown = 1
	}
	for range h - shown {
		lines = append(lines, "")
	}
	pos := ""
	if len(p.rows) > h {
		pos = th.Dim.Render(fmt.Sprintf("%d-%d of %d ", p.offset+1, min(p.offset+h, len(p.rows)), len(p.rows)))
	}
	lines = append(lines, spread("", pos, w))

	elbow := th.Dim.Render("  ⎿  ")
	detail := ""
	switch r, ok := p.current(); {
	case p.status != "":
		detail = th.Warn.Render(safe(p.status))
	case p.conflict != "":
		detail = th.Err.Render(safe(p.conflict))
	case ok && r.kind == rowTool:
		t := p.tools[r.tool]
		d := t.Description
		if o := p.over[t.Key]; o.Description != "" {
			d = o.Description
		}
		badge, target := describe(p.in.Describe, t)
		detail = th.method(badge).Render(safe(badge)) + " " + safe(target) + th.Dim.Render("  "+safe(d))
	case ok:
		detail = th.Dim.Render("space selects the whole group, enter folds it")
	}
	lines = append(lines, fit(elbow+detail, w))

	var input string
	border := th.Border
	switch p.mode {
	case modeFilter, modeRename, modeDescribe, modeFields:
		border = th.Focus
		input = th.Accent.Render(p.inputLabel) + p.input.View()
	case modeConfirmQuit:
		border = th.Warn
		input = th.Warn.Render("Discard your changes? ") + th.Dim.Render("y discards · any other key keeps editing")
	default:
		if p.filter != "" {
			input = th.Accent.Render("/ ") + safe(p.filter) + th.Dim.Render("   esc clears")
		} else {
			input = th.Dim.Render("> / filter · space toggle · m mode · i details · s save")
		}
	}
	lines = append(lines, th.box(w, border, input)...)

	large := p.offered > warnTools || p.budget > warnTokens
	stats := th.Dim
	if large {
		stats = th.Warn
	}
	left := "  " + stats.Render(p.footerText()) + "  " + th.gauge(p.budget, warnTokens, 10)
	if large {
		left += th.Warn.Render("  large toolset: models pick tools less reliably")
	}
	right := th.Dim.Render("? for shortcuts")
	if p.showHelp {
		right = th.Dim.Render("? to hide")
	}
	lines = append(lines, spread(left, right, w))
	lines = append(lines, p.helpLines()...)

	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}
