package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/jsonpick"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
)

// Init implements tea.Model.
// It asks the terminal for its background colour to pick the palette.
func (p *Picker) Init() tea.Cmd { return tea.RequestBackgroundColor }

// Update implements tea.Model.
func (p *Picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		p.th = themeFor(msg)
		return p, nil
	case tea.WindowSizeMsg:
		p.width, p.height = msg.Width, msg.Height
		p.clampScroll()
		return p, nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return p, tea.Quit
		}
		if p.detail != nil {
			return p, p.updateDetail(msg)
		}
		switch p.mode {
		case modeFilter, modeRename, modeDescribe, modeFields:
			return p, p.updateInput(msg)
		case modeConfirmQuit:
			switch msg.String() {
			case "y", "Y":
				return p, tea.Quit
			default:
				p.mode = modeList
				p.status = ""
			}
			return p, nil
		}
		return p, p.updateList(msg)
	}
	return p, nil
}

func (p *Picker) current() (row, bool) {
	if p.cursor >= 0 && p.cursor < len(p.rows) {
		return p.rows[p.cursor], true
	}
	return row{}, false
}

func (p *Picker) updateList(msg tea.KeyPressMsg) tea.Cmd {
	p.status = ""
	confirmFull := p.fullPending
	p.fullPending = false
	r, ok := p.current()
	switch msg.String() {
	case "m":
		p.cycleMode(confirmFull)
	case "down", "j":
		p.cursor++
	case "up", "k":
		p.cursor = max(p.cursor-1, 0)
	case "pgdown":
		p.cursor += p.listHeight()
	case "pgup":
		p.cursor = max(p.cursor-p.listHeight(), 0)
	case "home", "g":
		p.cursor = 0
	case "end", "G":
		p.cursor = max(len(p.rows)-1, 0)
	case "space", "x":
		if !ok {
			break
		}
		if r.kind == rowGroup {
			p.toggleGroup(r.group)
		} else {
			p.setSelected(p.tools[r.tool].Key, !p.selected[p.tools[r.tool].Key])
		}
	case "enter":
		if !ok {
			break
		}
		if r.kind == rowGroup {
			p.collapsed[r.group] = !p.collapsed[r.group]
			p.rebuild()
		} else {
			p.setSelected(p.tools[r.tool].Key, !p.selected[p.tools[r.tool].Key])
		}
	case "a", "n":
		for _, rr := range p.rows {
			if rr.kind == rowTool {
				p.setSelected(p.tools[rr.tool].Key, msg.String() == "a")
			}
		}
	case "/":
		p.startInput(modeFilter, "/ ", p.filter)
	case "esc":
		if p.filter != "" {
			p.filter = ""
			p.rebuild()
		}
	case "r", "e":
		if !ok || r.kind != rowTool {
			p.status = "move to a tool first"
			break
		}
		t := p.tools[r.tool]
		if msg.String() == "r" {
			p.startInput(modeRename, "name: ", p.baseName(t))
		} else {
			p.startInput(modeDescribe, "description: ", t.Description)
		}
	case "f":
		if !ok || r.kind != rowTool {
			p.status = "move to a tool first"
			break
		}
		p.startInput(modeFields, "response fields: ", strings.Join(p.over[p.tools[r.tool].Key].ResponseFields, ", "))
	case "p":
		if !ok || r.kind != rowTool {
			p.status = "move to a tool first"
			break
		}
		k := p.tools[r.tool].Key
		o := p.over[k]
		o.Policy = map[string]string{"": "confirm", "confirm": "deny", "deny": "allow", "allow": ""}[o.Policy]
		p.setOverride(k, o)
	case "s", "ctrl+s":
		return p.save()
	case "?":
		p.showHelp = !p.showHelp
	case "i":
		if !ok || r.kind != rowTool {
			p.status = "move to a tool first"
			break
		}
		p.openDetail(r.tool)
	case "q":
		if !p.dirty {
			return tea.Quit
		}
		p.mode = modeConfirmQuit
		p.status = ""
	}
	p.clampScroll()
	return nil
}

func (p *Picker) save() tea.Cmd {
	if p.conflict != "" {
		p.status = p.conflict
		return nil
	}
	if p.count == 0 {
		p.status = "select at least one tool"
		return nil
	}
	p.saved = true
	return tea.Quit
}

func (p *Picker) toggleGroup(gi int) {
	var visible []model.OpKey
	all := true
	for _, ti := range p.groups[gi].tools {
		if p.matches(p.tools[ti]) {
			k := p.tools[ti].Key
			visible = append(visible, k)
			all = all && p.selected[k]
		}
	}
	for _, k := range visible {
		p.setSelected(k, !all)
	}
}

func (p *Picker) setSelected(k model.OpKey, on bool) {
	if p.selected[k] == on {
		return
	}
	if on {
		p.selected[k] = true
	} else {
		delete(p.selected, k)
	}
	p.dirty = true
	p.recompute()
}

// setOverride stores an override; editing a tool selects it.
func (p *Picker) setOverride(k model.OpKey, o config.ToolOverride) {
	if o.IsZero() {
		delete(p.over, k)
	} else {
		p.over[k] = o
	}
	p.selected[k] = true
	p.dirty = true
	p.recompute()
}

func (p *Picker) startInput(m mode, prompt, value string) {
	p.mode = m
	p.input = textinput.New()
	p.inputLabel = prompt
	p.input.Prompt = ""
	p.input.CharLimit = naming.DefaultMaxDescription
	p.input.SetValue(value)
	p.input.CursorEnd()
	p.input.Focus()
}

func (p *Picker) updateInput(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		if p.mode == modeFilter {
			p.filter = ""
			p.rebuild()
		}
		p.mode = modeList
		p.status = ""
		return nil
	case "enter":
		return p.commitInput()
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.mode == modeFilter {
		p.filter = p.input.Value()
		p.rebuild()
	}
	return cmd
}

func (p *Picker) commitInput() tea.Cmd {
	v := strings.TrimSpace(p.input.Value())
	switch p.mode {
	case modeFilter:
		p.mode = modeList
		return nil
	case modeRename, modeDescribe, modeFields:
		r, ok := p.current()
		if !ok || r.kind != rowTool {
			p.mode = modeList
			return nil
		}
		k := p.tools[r.tool].Key
		o := p.over[k]
		switch p.mode {
		case modeRename:
			if v != "" && !toolNameRe.MatchString(v) {
				p.status = "names use 1-64 letters, digits, '_' or '-'"
				return nil
			}
			if p.in.Prefix != "" && len(p.in.Prefix)+1+len(v) > naming.MaxNameLen {
				p.status = fmt.Sprintf("too long with the %q prefix (max %d characters)", p.in.Prefix, naming.MaxNameLen)
				return nil
			}
			o.Name = v
		case modeFields:
			var fields []string
			for f := range strings.SplitSeq(v, ",") {
				if f = strings.TrimSpace(f); f != "" {
					fields = append(fields, f)
				}
			}
			if _, err := jsonpick.Parse(fields); err != nil {
				p.status = err.Error()
				return nil
			}
			o.ResponseFields = fields
		default:
			if v == p.tools[r.tool].Description {
				v = ""
			}
			o.Description = v
		}
		p.setOverride(k, o)
	}
	p.mode = modeList
	p.status = ""
	return nil
}
