package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/lint"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
)

// fakeLint reports one error for GET /pets/{id} when it's selected, and
// records the toolsets it was asked about.
type fakeLint struct{ asked []config.Toolset }

func (f *fakeLint) run(ts config.Toolset) []lint.Finding {
	f.asked = append(f.asked, ts)
	if _, ok := ts.Tools["GET /pets/{id}"]; !ok {
		return nil
	}
	return []lint.Finding{
		{Rule: "L007", Severity: lint.Error, Toolset: "default", Tool: "GET /pets/{id}", Name: "pp_getPet", Message: "description reads like an instruction to the model"},
		{Rule: "L001", Severity: lint.Warn, Toolset: "default", Tool: "GET /pets/{id}", Name: "pp_getPet", Message: "no description in the spec"},
	}
}

func newLintPicker(selected ...string) (*Picker, *fakeLint) {
	f := &fakeLint{}
	ts := config.Toolset{Tools: map[string]config.ToolOverride{}}
	for _, k := range selected {
		ts.Tools[k] = config.ToolOverride{}
	}
	tools := testTools()
	tools[2].Params = append(tools[2].Params,
		model.Param{Name: "X-Api-Key", UpstreamName: "X-Api-Key", In: model.InHeader, Hidden: true, Schema: json.RawMessage(`{}`)})
	p := NewPicker(PickerInput{API: "pets", Tools: tools, Prefix: "pp", Mode: policy.ReadOnly, Selected: ts, Lint: f.run})
	p.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	return p, f
}

// The footer counts lint errors and rows carry a badge (SPEC §2.2, §2.3).
func TestPickerLintBadges(t *testing.T) {
	p, _ := newLintPicker("GET /pets")
	if footer(view(p)) == "" || strings.Contains(footer(view(p)), "lint") {
		t.Errorf("clean selection shouldn't mention lint: %q", footer(view(p)))
	}
	moveTo(t, p, "pp_getPet")
	press(p, "space")
	if f := footer(view(p)); !strings.Contains(f, "1 lint error") {
		t.Errorf("footer = %q", f)
	}
	if !strings.Contains(p.currentRowText(), "✗") {
		t.Errorf("row has no lint badge: %q", p.currentRowText())
	}
}

func TestToolDetail(t *testing.T) {
	p, f := newLintPicker("GET /pets")
	moveTo(t, p, "pp_getPet") // not selected: lint is still run for it
	press(p, "i")
	v := view(p)
	for _, want := range []string{
		"tool", "pp_getPet", "GET", "/pets/{id}", "GET /pets/{id} does things.", "pets",
		"Parameters", "id", "query", "string",
		"X-Api-Key", "hidden",
		"≈", "tokens",
		"Lint", "L007", "instruction", "L001",
		"Input schema", `"properties"`, `"id"`,
	} {
		if !strings.Contains(v, want) {
			t.Errorf("detail missing %q:\n%s", want, v)
		}
	}
	// The hidden header is listed but never in the schema the model sees.
	if i := strings.Index(v, "Input schema"); i < 0 || strings.Contains(v[i:], "X-Api-Key") {
		t.Errorf("hidden param in the input schema:\n%s", v)
	}
	last := f.asked[len(f.asked)-1]
	if _, ok := last.Tools["GET /pets/{id}"]; !ok {
		t.Error("lint wasn't run with the tool selected")
	}
	if p.selected["GET /pets/{id}"] {
		t.Error("opening the detail selected the tool")
	}

	press(p, "esc")
	if strings.Contains(view(p), "Input schema") {
		t.Error("esc didn't close the detail")
	}
}

func TestToolDetailWithoutLint(t *testing.T) {
	p := newTest("GET /pets")
	moveTo(t, p, "pp_listPets")
	press(p, "i")
	v := view(p)
	if !strings.Contains(v, "Input schema") || strings.Contains(v, "L007") {
		t.Errorf("detail:\n%s", v)
	}
	if _, cmd := p.Update(key("q")); isQuit(cmd) {
		t.Error("q in the detail should go back, not quit")
	}
	if strings.Contains(view(p), "Input schema") {
		t.Error("q didn't close the detail")
	}
}

// The detail scrolls and fits small windows; spec text is sanitized.
func TestToolDetailFitsAndSanitizes(t *testing.T) {
	evil := tl("GET", "/x", "pp_x", "t")
	evil.Description = "desc\x1b[2J" + string(rune(0x202e))
	p := NewPicker(PickerInput{API: "x", Tools: []model.Tool{evil}, Mode: policy.ReadOnly})
	p.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	moveTo(t, p, "pp_x")
	press(p, "i")
	for range 30 {
		press(p, "down")
	}
	raw := p.View().Content
	if strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, string(rune(0x202e))) {
		t.Error("control characters reached the view")
	}
	if n := strings.Count(raw, "\n") + 1; n > 12 {
		t.Errorf("%d lines for a 12-line window", n)
	}
	for _, l := range strings.Split(raw, "\n") {
		if w := ansi.StringWidth(l); w > 40 {
			t.Errorf("line is %d wide: %q", w, ansi.Strip(l))
		}
	}
}
