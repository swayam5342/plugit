package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/lint"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/schema"
	"github.com/swayam5342/plugit/internal/tokens"
)

// detailState is the open Tool detail screen (SPEC §2.3).
type detailState struct {
	tool     int // index into Picker.tools
	offset   int
	findings []lint.Finding
}

// openDetail shows the tool under the cursor. Lint runs as if the tool
// were selected, so its findings show even before it is.
func (p *Picker) openDetail(ti int) {
	d := &detailState{tool: ti}
	if p.in.Lint != nil {
		ts := p.toolset()
		ts.Tools = maps.Clone(ts.Tools)
		key := string(p.tools[ti].Key)
		if _, ok := ts.Tools[key]; !ok {
			ts.Tools[key] = config.ToolOverride{}
		}
		for _, f := range p.in.Lint(ts) {
			if f.Tool == p.tools[ti].Key {
				d.findings = append(d.findings, f)
			}
		}
	}
	p.detail = d
}

func (p *Picker) updateDetail(msg tea.KeyPressMsg) tea.Cmd {
	d := p.detail
	switch msg.String() {
	case "esc", "q", "i", "backspace", "left":
		p.detail = nil
	case "down", "j":
		d.offset++
	case "up", "k":
		d.offset = max(d.offset-1, 0)
	case "pgdown", "space":
		d.offset += max(p.height-6, 1)
	case "pgup":
		d.offset = max(d.offset-max(p.height-6, 1), 0)
	case "home", "g":
		d.offset = 0
	case "end", "G":
		d.offset = 1 << 30 // clamped when drawn
	}
	return nil
}

// finalTool is the tool as the model would see it: overrides applied.
func (p *Picker) finalTool(ti int) model.Tool {
	t := p.tools[ti]
	t.Name = p.displayName(t)
	if o := p.over[t.Key]; o.Description != "" {
		t.Description = o.Description
	}
	return t
}

func (p *Picker) detailBody(w int) []string {
	th := p.th
	t := p.finalTool(p.detail.tool)
	var lines []string
	add := func(s string) { lines = append(lines, fit(s, w)) }
	section := func(s string) { add(""); add("  " + th.Accent.Bold(true).Render(s)) }

	for _, l := range wrapLines(t.Description, max(w-4, 10)) {
		add("  " + l)
	}
	meta := []string{t.Safety.String()}
	if len(t.Tags) > 0 {
		meta = append(meta, "tags: "+safe(strings.Join(t.Tags, ", ")))
	}
	if t.Deprecated {
		meta = append(meta, th.Warn.Render("deprecated"))
	}
	if o := p.over[t.Key]; o.Policy != "" {
		meta = append(meta, "policy: "+th.policy(o.Policy).Render(o.Policy))
	}
	add("  " + th.Dim.Render(strings.Join(meta, " · ")))

	section("Parameters")
	if len(t.Params) == 0 {
		add("    " + th.Dim.Render("none"))
	}
	nameW := 0
	for _, prm := range t.Params {
		nameW = max(nameW, len([]rune(safe(prm.Name))))
	}
	for _, prm := range t.Params {
		cols := []string{fmt.Sprintf("%-8s", prm.In), fmt.Sprintf("%-8s", paramType(prm))}
		if prm.Required {
			cols = append(cols, "required")
		}
		if prm.UpstreamName != "" && prm.UpstreamName != prm.Name {
			cols = append(cols, "sent as "+safe(prm.UpstreamName))
		}
		line := "    " + th.Bold.Render(padRight(safe(prm.Name), nameW)) + "  " + th.Dim.Render(strings.Join(cols, "  "))
		if prm.Hidden {
			line += "  " + th.Warn.Render("hidden: filled in by plugit, never shown to the model")
		} else if prm.Desc != "" {
			line += "  " + th.Dim.Render(safe(prm.Desc))
		}
		add(line)
	}
	if t.Body != nil {
		add("    " + th.Dim.Render("request body: "+safe(t.Body.ContentType)))
	}

	section("Token cost")
	add("    " + tokens.Format(tokens.Tool(t)) + " tokens " + th.Dim.Render("in the tool list the model sees (name, description and input schema)"))

	if p.in.Lint != nil {
		section("Lint")
		if len(p.detail.findings) == 0 {
			add("    " + th.OK.Render("✓ no findings"))
		}
		for _, f := range p.detail.findings {
			mark, st := "·", th.Dim
			switch f.Severity {
			case lint.Error:
				mark, st = "✗", th.Err
			case lint.Warn:
				mark, st = "!", th.Warn
			}
			add("    " + st.Render(mark+" "+f.Rule) + "  " + safe(f.Message))
		}
	}
	for _, is := range t.Issues {
		add("    " + th.Dim.Render("ingest: "+safe(string(is.Kind))+": "+safe(is.Detail)))
	}

	section("Input schema")
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, schema.InputSchema(t), "    ", "  "); err == nil {
		for _, l := range strings.Split("    "+pretty.String(), "\n") {
			add(th.Dim.Render(safe(l)))
		}
	}
	return lines
}

func (p *Picker) detailView() tea.View {
	th := p.th
	w := max(p.width, 20)
	t := p.finalTool(p.detail.tool)
	title := th.Accent.Render("✦ ") + th.Title.Render("plugit") + th.Dim.Render(" · tool · ") + th.Title.Render(safe(t.Name))
	sub := th.method(t.Method).Render(t.Method) + " " + safe(t.Path)
	lines := th.box(w, th.Border, spread(title, th.pill(p.in.Mode), w-4), sub)

	body := p.detailBody(w)
	h := max(p.height-len(lines)-1, 1)
	p.detail.offset = max(min(p.detail.offset, len(body)-h), 0)
	shown := body[p.detail.offset:min(p.detail.offset+h, len(body))]
	lines = append(lines, shown...)
	for range h - len(shown) {
		lines = append(lines, "")
	}
	pos := ""
	if len(body) > h {
		pos = th.Dim.Render(fmt.Sprintf("%d-%d of %d ", p.detail.offset+1, p.detail.offset+len(shown), len(body)))
	}
	lines = append(lines, spread("  "+th.hints(w/2, "↑↓", "scroll", "esc", "back"), pos, w))

	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}
