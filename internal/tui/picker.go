// Package tui holds PlugIt's terminal screens (SPEC §2.3), built on Bubble
// Tea. Spec-derived text is untrusted: everything drawn is stripped of
// control and format characters first, so a spec can't drive the terminal.
package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/curate"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/tokens"
)

// ErrCancelled means the user left the picker without saving.
var ErrCancelled = errors.New("cancelled; nothing saved")

// Budget thresholds shown as warnings (lint L010).
const (
	warnTools  = 40
	warnTokens = 8000
)

const untagged = "(untagged)"

var toolNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// PickerInput is what the picker edits.
type PickerInput struct {
	API      string
	Tools    []model.Tool // named with the prefix, fixed params hidden
	Prefix   string
	Mode     policy.Mode
	Selected config.Toolset // initial selection and overrides
}

type rowKind int

const (
	rowGroup rowKind = iota
	rowTool
)

type row struct {
	kind  rowKind
	group int
	tool  int // index into Picker.tools
}

type group struct {
	name  string
	tools []int
}

type mode int

const (
	modeList mode = iota
	modeFilter
	modeRename
	modeDescribe
	modeConfirmQuit
)

// Picker is the tool picker: a Bubble Tea model.
type Picker struct {
	in       PickerInput
	tools    []model.Tool
	groups   []group
	selected map[model.OpKey]bool
	over     map[model.OpKey]config.ToolOverride
	missing  map[string]config.ToolOverride // listed keys the spec doesn't have; kept as they are

	collapsed map[int]bool
	filter    string
	rows      []row
	cursor    int
	offset    int
	width     int
	height    int

	mode   mode
	input  textinput.Model
	status string

	dirty bool
	saved bool

	// recomputed on every change
	conflict   string
	count      int // selected tools
	offered    int // selected and listed under the mode
	writes     int
	budget     int
	finalNames map[model.OpKey]string
	toolTokens map[model.OpKey]int
}

// NewPicker builds a picker.
func NewPicker(in PickerInput) *Picker {
	p := &Picker{
		in:        in,
		tools:     slices.Clone(in.Tools),
		selected:  map[model.OpKey]bool{},
		over:      map[model.OpKey]config.ToolOverride{},
		missing:   map[string]config.ToolOverride{},
		collapsed: map[int]bool{},
		width:     100,
		height:    30,
	}
	present := map[model.OpKey]bool{}
	for _, t := range p.tools {
		present[t.Key] = true
	}
	for k, o := range in.Selected.Tools {
		if !present[model.OpKey(k)] {
			p.missing[k] = o
			continue
		}
		p.selected[model.OpKey(k)] = true
		if o != (config.ToolOverride{}) {
			p.over[model.OpKey(k)] = o
		}
	}
	p.buildGroups()
	p.input = textinput.New()
	p.rebuild()
	p.recompute()
	return p
}

func (p *Picker) buildGroups() {
	byName := map[string][]int{}
	for i, t := range p.tools {
		g := untagged
		if len(t.Tags) > 0 && strings.TrimSpace(t.Tags[0]) != "" {
			g = t.Tags[0]
		}
		byName[g] = append(byName[g], i)
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for n := range byName {
			if !yield(n) {
				return
			}
		}
	})
	slices.SortStableFunc(names, func(a, b string) int {
		// "(untagged)" goes last.
		return cmp.Compare(boolInt(a == untagged), boolInt(b == untagged))
	})
	for _, n := range names {
		idx := byName[n]
		slices.SortFunc(idx, func(a, b int) int {
			return cmp.Or(cmp.Compare(p.tools[a].Path, p.tools[b].Path), cmp.Compare(methodOrder(p.tools[a].Method), methodOrder(p.tools[b].Method)))
		})
		p.groups = append(p.groups, group{name: n, tools: idx})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func methodOrder(m string) int {
	return slices.Index([]string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"}, m)
}

// matches reports whether t matches every term of the filter.
func (p *Picker) matches(t model.Tool) bool {
	if p.filter == "" {
		return true
	}
	hay := strings.ToLower(p.displayName(t) + " " + string(t.Key) + " " + strings.Join(t.Tags, " ") + " " + t.Description)
	for _, term := range strings.Fields(strings.ToLower(p.filter)) {
		if !strings.Contains(hay, term) {
			return false
		}
	}
	return true
}

func (p *Picker) rebuild() {
	var cur *row
	if p.cursor < len(p.rows) {
		r := p.rows[p.cursor]
		cur = &r
	}
	p.rows = p.rows[:0]
	for gi, g := range p.groups {
		var visible []int
		for _, ti := range g.tools {
			if p.matches(p.tools[ti]) {
				visible = append(visible, ti)
			}
		}
		if len(visible) == 0 {
			continue
		}
		p.rows = append(p.rows, row{kind: rowGroup, group: gi})
		if p.collapsed[gi] && p.filter == "" {
			continue
		}
		for _, ti := range visible {
			p.rows = append(p.rows, row{kind: rowTool, group: gi, tool: ti})
		}
	}
	// Keep the cursor on the same row when it is still visible.
	p.cursor = 0
	if cur != nil {
		for i, r := range p.rows {
			if r == *cur {
				p.cursor = i
				break
			}
		}
	}
	p.clampScroll()
}

func (p *Picker) listHeight() int {
	return max(p.height-7, 3)
}

func (p *Picker) clampScroll() {
	if p.cursor >= len(p.rows) {
		p.cursor = max(len(p.rows)-1, 0)
	}
	h := p.listHeight()
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+h {
		p.offset = p.cursor - h + 1
	}
	p.offset = max(min(p.offset, len(p.rows)-h), 0)
}

// toolset is the current selection as config.
func (p *Picker) toolset() config.Toolset {
	ts := config.Toolset{
		Tools:       map[string]config.ToolOverride{},
		IncludeTags: p.in.Selected.IncludeTags,
		Methods:     p.in.Selected.Methods,
	}
	for k, o := range p.missing {
		ts.Tools[k] = o
	}
	for k := range p.selected {
		ts.Tools[string(k)] = p.over[k]
	}
	return ts
}

func (p *Picker) recompute() {
	p.conflict = ""
	p.count, p.offered, p.writes, p.budget = 0, 0, 0, 0
	p.finalNames = map[model.OpKey]string{}
	if p.toolTokens == nil {
		p.toolTokens = map[model.OpKey]int{}
	}
	res, err := curate.Apply(p.tools, p.toolset(), p.in.Prefix)
	if err != nil {
		p.conflict = err.Error()
		return
	}
	for _, st := range res.Tools {
		t := st.Tool
		p.count++
		p.finalNames[t.Key] = t.Name
		n := tokens.Tool(t)
		p.toolTokens[t.Key] = n
		if t.Safety != model.ReadOnly {
			p.writes++
		}
		if policy.Lists(t.Safety, p.in.Mode) && st.Override != policy.OverrideDeny {
			p.offered++
			p.budget += n
		}
	}
}

func (p *Picker) displayName(t model.Tool) string {
	if n, ok := p.finalNames[t.Key]; ok {
		return n
	}
	if o := p.over[t.Key]; o.Name != "" {
		if p.in.Prefix != "" {
			return p.in.Prefix + "_" + o.Name
		}
		return o.Name
	}
	return t.Name
}

// baseName is the name without the prefix, as overrides store it.
func (p *Picker) baseName(t model.Tool) string {
	if o := p.over[t.Key]; o.Name != "" {
		return o.Name
	}
	if p.in.Prefix != "" {
		return strings.TrimPrefix(t.Name, p.in.Prefix+"_")
	}
	return t.Name
}

// Result returns the selection and whether the user saved it.
func (p *Picker) Result() (config.Toolset, bool) {
	return p.toolset(), p.saved
}

// Init implements tea.Model.
func (p *Picker) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (p *Picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = msg.Width, msg.Height
		p.clampScroll()
		return p, nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return p, tea.Quit
		}
		switch p.mode {
		case modeFilter, modeRename, modeDescribe:
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
	if p.cursor < len(p.rows) {
		return p.rows[p.cursor], true
	}
	return row{}, false
}

func (p *Picker) updateList(msg tea.KeyPressMsg) tea.Cmd {
	p.status = ""
	r, ok := p.current()
	switch msg.String() {
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
		p.cursor = len(p.rows) - 1
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
	case "q":
		if !p.dirty {
			return tea.Quit
		}
		p.mode = modeConfirmQuit
		p.status = "Discard your changes? [y/N]"
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
	if o == (config.ToolOverride{}) {
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
	p.input.Prompt = prompt
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
	case modeRename, modeDescribe:
		r, ok := p.current()
		if !ok || r.kind != rowTool {
			p.mode = modeList
			return nil
		}
		k := p.tools[r.tool].Key
		o := p.over[k]
		if p.mode == modeRename {
			if v != "" && !toolNameRe.MatchString(v) {
				p.status = "names use 1-64 letters, digits, '_' or '-'"
				return nil
			}
			if p.in.Prefix != "" && len(p.in.Prefix)+1+len(v) > naming.MaxNameLen {
				p.status = fmt.Sprintf("too long with the %q prefix (max %d characters)", p.in.Prefix, naming.MaxNameLen)
				return nil
			}
			o.Name = v
		} else {
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

// ---- rendering ----

var (
	styleTitle  = lipgloss.NewStyle().Bold(true)
	styleCursor = lipgloss.NewStyle().Reverse(true)
	styleDim    = lipgloss.NewStyle().Faint(true)
	styleWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleRead   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

// safe strips control and format characters from untrusted text.
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

func truncate(s string, n int) string {
	if n <= 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// rowText is a row as plain text.
func (p *Picker) rowText(r row) string {
	if r.kind == rowGroup {
		g := p.groups[r.group]
		sel := 0
		for _, ti := range g.tools {
			if p.selected[p.tools[ti].Key] {
				sel++
			}
		}
		arrow := "▾"
		if p.collapsed[r.group] && p.filter == "" {
			arrow = "▸"
		}
		return fmt.Sprintf("%s %s  (%d/%d)", arrow, safe(g.name), sel, len(g.tools))
	}
	t := p.tools[r.tool]
	box := "[ ]"
	if p.selected[t.Key] {
		box = "[x]"
	}
	badge := map[model.Safety]string{model.ReadOnly: "R", model.Write: "W", model.Destructive: "D"}[t.Safety]
	s := fmt.Sprintf("  %s %s %-7s %-34s %s", box, badge, t.Method, truncate(safe(t.Path), 34), safe(p.displayName(t)))
	if n, ok := p.toolTokens[t.Key]; ok && p.selected[t.Key] {
		s += "  " + tokens.Format(n)
	}
	if o := p.over[t.Key]; o.Policy != "" {
		s += "  [" + o.Policy + "]"
	}
	if !policy.Lists(t.Safety, p.in.Mode) {
		s += "  (not offered in " + string(p.in.Mode) + ")"
	}
	return s
}

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
	f := fmt.Sprintf("%d %s · %s tokens · %d write · mode %s", p.count, unit, tokens.Format(p.budget), p.writes, p.in.Mode)
	if off := p.count - p.offered; off > 0 {
		f += fmt.Sprintf(" · %d not offered", off)
	}
	return f
}

// View implements tea.Model.
func (p *Picker) View() tea.View {
	w := max(p.width, 20)
	var b strings.Builder
	title := fmt.Sprintf("plugit · %s · pick tools", safe(p.in.API))
	if p.filter != "" {
		title += "  filter: " + safe(p.filter)
	}
	b.WriteString(styleTitle.Render(truncate(title, w)) + "\n")

	h := p.listHeight()
	for i := p.offset; i < min(p.offset+h, len(p.rows)); i++ {
		line := truncate(p.rowText(p.rows[i]), w)
		switch {
		case i == p.cursor:
			line = styleCursor.Render(line)
		case p.rows[i].kind == rowTool && !policy.Lists(p.tools[p.rows[i].tool].Safety, p.in.Mode):
			line = styleDim.Render(line)
		case p.rows[i].kind == rowTool && p.tools[p.rows[i].tool].Safety == model.ReadOnly:
			line = styleRead.Render(line)
		}
		b.WriteString(line + "\n")
	}
	for i := len(p.rows) - p.offset; i < h; i++ {
		b.WriteString("\n")
	}

	// Detail of the tool under the cursor.
	detail := ""
	if r, ok := p.current(); ok && r.kind == rowTool {
		detail = safe(p.tools[r.tool].Description)
		if o := p.over[p.tools[r.tool].Key]; o.Description != "" {
			detail = safe(o.Description)
		}
	}
	b.WriteString(styleDim.Render(truncate(detail, w)) + "\n")

	switch {
	case p.mode == modeFilter || p.mode == modeRename || p.mode == modeDescribe:
		b.WriteString(p.input.View() + "\n")
	case p.status != "":
		b.WriteString(styleWarn.Render(truncate(safe(p.status), w)) + "\n")
	case p.conflict != "":
		b.WriteString(styleErr.Render(truncate(safe(p.conflict), w)) + "\n")
	default:
		b.WriteString("\n")
	}
	if p.mode != modeList && p.status != "" {
		b.WriteString(styleWarn.Render(truncate(safe(p.status), w)) + "\n")
	} else {
		b.WriteString("\n")
	}

	footer := p.footerText()
	if p.offered > warnTools || p.budget > warnTokens {
		footer = styleWarn.Render(footer + " · large toolset")
	}
	b.WriteString(truncate(footer, w+20) + "\n")
	b.WriteString(styleDim.Render(truncate("↑↓ move · space toggle · enter fold · / filter · a/n all/none · r rename · e describe · p policy · s save · q quit", w)))

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

// RunPicker runs the picker on a terminal and returns the saved selection,
// or ErrCancelled.
func RunPicker(ctx context.Context, in PickerInput, r io.Reader, w io.Writer) (config.Toolset, error) {
	p := NewPicker(in)
	if _, err := tea.NewProgram(p, tea.WithContext(ctx), tea.WithInput(r), tea.WithOutput(w)).Run(); err != nil {
		return config.Toolset{}, err
	}
	ts, saved := p.Result()
	if !saved {
		return config.Toolset{}, ErrCancelled
	}
	return ts, nil
}
