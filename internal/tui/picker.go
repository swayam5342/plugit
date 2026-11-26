// Package tui holds PlugIt's terminal screens (SPEC §2.3), built on Bubble
// Tea. Spec-derived text is untrusted: everything drawn is stripped of
// control and format characters first, so a spec can't drive the terminal.
package tui

import (
	"cmp"
	"context"
	"errors"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/curate"
	"github.com/swayam5342/plugit/internal/lint"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/tokens"
)

// ErrCancelled means the user left the picker without saving.
var ErrCancelled = errors.New("cancelled; nothing saved")

const (
	warnTools  = 40
	warnTokens = 8000
)

const untagged = "(untagged)"

var toolNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// PickerInput is what the picker edits.
type PickerInput struct {
	API      string
	Tools    []model.Tool
	Prefix   string
	Mode     policy.Mode
	Selected config.Toolset
	Lint     func(config.Toolset) []lint.Finding
	// Describe returns a tool's per-protocol badge and target for display
	// (SPEC §2.3): REST "GET /pets/{id}", GraphQL "query user", gRPC
	// "acme.v1.Issues/Get". The cli layer injects it (cli.describeFor).
	// When nil, the badge/target fall back to the REST method and path.
	Describe func(model.Tool) (badge, target string)
}

// describe is the per-protocol badge and target for a tool (SPEC §2.3),
// routed through the injected Describe. With none set it falls back to the
// REST method and path from the OpKey, so REST renders identically whether
// or not the caller wired the seam.
func describe(fn func(model.Tool) (string, string), t model.Tool) (badge, target string) {
	if fn != nil {
		return fn(t)
	}
	return t.Key.Method(), t.Key.Path()
}

type rowKind int

const (
	rowGroup rowKind = iota
	rowTool
)

type row struct {
	kind  rowKind
	group int
	tool  int
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
	modeFields
	modeConfirmQuit
)

// Picker is the tool picker: a Bubble Tea model.
type Picker struct {
	in       PickerInput
	tools    []model.Tool
	groups   []group
	selected map[model.OpKey]bool
	over     map[model.OpKey]config.ToolOverride
	missing  map[string]config.ToolOverride

	collapsed map[int]bool
	filter    string
	rows      []row
	cursor    int
	offset    int
	width     int
	height    int

	mode       mode
	input      textinput.Model
	inputLabel string
	status     string

	dirty bool
	saved bool

	policyMode  policy.Mode
	fullPending bool

	conflict   string
	count      int
	offered    int
	writes     int
	budget     int
	finalNames map[model.OpKey]string
	toolTokens map[model.OpKey]int
	nameWidth  int

	th       theme
	showHelp bool

	detail     *detailState
	lintErrors int
	lintWorst  map[model.OpKey]lint.Severity
}

// NewPicker builds a picker.
func NewPicker(in PickerInput) *Picker {
	p := &Picker{
		in:         in,
		tools:      slices.Clone(in.Tools),
		selected:   map[model.OpKey]bool{},
		over:       map[model.OpKey]config.ToolOverride{},
		missing:    map[string]config.ToolOverride{},
		collapsed:  map[int]bool{},
		width:      100,
		th:         newTheme(true),
		height:     30,
		policyMode: in.Mode,
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
		if !o.IsZero() {
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
	names := slices.Sorted(maps.Keys(byName))
	slices.SortStableFunc(names, func(a, b string) int {
		return cmp.Compare(boolInt(a == untagged), boolInt(b == untagged))
	})
	for _, n := range names {
		idx := byName[n]
		slices.SortFunc(idx, func(a, b int) int {
			return cmp.Or(cmp.Compare(p.tools[a].Key.Path(), p.tools[b].Key.Path()), cmp.Compare(methodOrder(p.tools[a].Key.Method()), methodOrder(p.tools[b].Key.Method())))
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
	defer p.measureNames()
	p.relint()
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
		if policy.Lists(t.Safety, p.policyMode) && st.Override != policy.OverrideDeny {
			p.offered++
			p.budget += n
		}
	}
}

// relint lints the current selection, when the picker has a linter.
func (p *Picker) relint() {
	p.lintErrors, p.lintWorst = 0, map[model.OpKey]lint.Severity{}
	if p.in.Lint == nil {
		return
	}
	for _, f := range p.in.Lint(p.toolset()) {
		if f.Severity == lint.Error {
			p.lintErrors++
		}
		if w, ok := p.lintWorst[f.Tool]; !ok || f.Severity > w {
			p.lintWorst[f.Tool] = f.Severity
		}
	}
}

// measureNames sizes the name column to the longest name, up to a limit.
func (p *Picker) measureNames() {
	p.nameWidth = 0
	for _, t := range p.tools {
		p.nameWidth = max(p.nameWidth, ansi.StringWidth(safe(p.displayName(t))))
	}
	p.nameWidth = min(p.nameWidth, 40)
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

// Mode returns the API's policy mode as set in the picker.
func (p *Picker) Mode() policy.Mode { return p.policyMode }

// nextMode is the mode m moves to: read-only, confirm-writes, full, and round.
func nextMode(m policy.Mode) policy.Mode {
	switch m {
	case policy.ReadOnly:
		return policy.ConfirmWrites
	case policy.ConfirmWrites:
		return policy.Full
	}
	return policy.ReadOnly
}

// cycleMode moves to the next policy mode. Loosening to full takes a second
// press, so writes never start running unasked because of one stray key.
func (p *Picker) cycleMode(confirmed bool) {
	next := nextMode(p.policyMode)
	if next == policy.Full && !confirmed {
		p.fullPending = true
		p.status = "full runs write tools without asking: press m again to confirm, any other key to cancel"
		return
	}
	p.policyMode = next
	p.dirty = true
	p.recompute()
	p.status = "policy mode: " + string(next) + " (saved with s)"
}

// RunPicker runs the picker on a terminal and returns the saved selection
// and policy mode, or ErrCancelled.
func RunPicker(ctx context.Context, in PickerInput, r io.Reader, w io.Writer) (config.Toolset, policy.Mode, error) {
	p := NewPicker(in)
	if _, err := tea.NewProgram(p, programOptions(ctx, r, w)...).Run(); err != nil {
		return config.Toolset{}, "", err
	}
	ts, saved := p.Result()
	if !saved {
		return config.Toolset{}, "", ErrCancelled
	}
	return ts, p.Mode(), nil
}
