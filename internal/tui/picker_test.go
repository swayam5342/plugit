package tui

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
)

func tl(method, path, name string, tags ...string) model.Tool {
	return model.Tool{
		Key: model.NewOpKey(method, path), Name: name, Description: method + " " + path + " does things.",
		Method: method, Path: path, Tags: tags, Safety: model.SafetyForMethod(method),
		Params: []model.Param{{Name: "id", UpstreamName: "id", In: model.InQuery, Schema: json.RawMessage(`{"type":"string"}`)}},
	}
}

func testTools() []model.Tool {
	return []model.Tool{
		tl("GET", "/pets", "pp_listPets", "pets"),
		tl("POST", "/pets", "pp_createPet", "pets"),
		tl("GET", "/pets/{id}", "pp_getPet", "pets"),
		tl("DELETE", "/pets/{id}", "pp_deletePet", "pets"),
		tl("GET", "/store/inventory", "pp_getInventory", "store"),
		tl("GET", "/health", "pp_health"),
	}
}

func newTest(selected ...string) *Picker {
	ts := config.Toolset{Tools: map[string]config.ToolOverride{}}
	for _, k := range selected {
		ts.Tools[k] = config.ToolOverride{}
	}
	p := NewPicker(PickerInput{API: "pets", Tools: testTools(), Prefix: "pp", Mode: policy.ReadOnly, Selected: ts})
	p.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return p
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

// press sends keys; a multi-character string that isn't a key name is typed.
func press(p *Picker, keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		switch k {
		case "space", "enter", "esc", "down", "up", "backspace", "ctrl+u":
			_, cmd = p.Update(key(k))
		default:
			for _, r := range k {
				_, cmd = p.Update(key(string(r)))
			}
		}
	}
	return cmd
}

func view(p *Picker) string { return p.View().Content }

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// moveTo puts the cursor on the row whose text contains s.
func moveTo(t *testing.T, p *Picker, s string) {
	t.Helper()
	press(p, "g")
	for range 80 {
		if strings.Contains(p.currentRowText(), s) {
			return
		}
		press(p, "down")
	}
	t.Fatalf("no row containing %q:\n%s", s, view(p))
}

func selectedKeys(ts config.Toolset) []string {
	var out []string
	for k := range ts.Tools {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestPickerShowsGroupsAndBudget(t *testing.T) {
	p := newTest("GET /pets", "GET /pets/{id}")
	v := view(p)
	for _, want := range []string{"pets", "store", "(untagged)", "pp_listPets", "/pets/{id}", "2 tools", "≈", "read-only"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

func TestPickerToggleAndSave(t *testing.T) {
	p := newTest("GET /pets")
	moveTo(t, p, "pp_getInventory")
	press(p, "space")
	if !strings.Contains(view(p), "2 tools") {
		t.Errorf("footer after toggle:\n%s", view(p))
	}
	moveTo(t, p, "pp_listPets")
	press(p, "space") // deselect
	cmd := press(p, "s")
	if !isQuit(cmd) {
		t.Fatal("s should save and quit")
	}
	ts, saved := p.Result()
	if !saved || !slices.Equal(selectedKeys(ts), []string{"GET /store/inventory"}) {
		t.Errorf("result = %v saved=%v", selectedKeys(ts), saved)
	}
}

func TestPickerGroupToggle(t *testing.T) {
	p := newTest()
	moveTo(t, p, "▾ pets")
	press(p, "space")
	ts, _ := p.Result()
	if got := selectedKeys(ts); len(got) != 4 {
		t.Errorf("group toggle selected %v", got)
	}
	press(p, "space")
	if ts, _ := p.Result(); len(ts.Tools) != 0 {
		t.Errorf("second group toggle left %v", selectedKeys(ts))
	}
	// Collapsing hides the group's tools.
	press(p, "enter")
	if strings.Contains(view(p), "pp_listPets") {
		t.Errorf("collapsed group still shows its tools:\n%s", view(p))
	}
}

func TestPickerFilter(t *testing.T) {
	p := newTest()
	press(p, "/", "invent", "enter")
	v := view(p)
	if !strings.Contains(v, "pp_getInventory") || strings.Contains(v, "pp_listPets") {
		t.Errorf("filtered view:\n%s", v)
	}
	// "a" selects everything the filter shows, and only that.
	press(p, "a")
	if ts, _ := p.Result(); !slices.Equal(selectedKeys(ts), []string{"GET /store/inventory"}) {
		t.Errorf("select visible = %v", selectedKeys(ts))
	}
	press(p, "esc") // clears the filter
	if !strings.Contains(view(p), "pp_listPets") {
		t.Errorf("filter not cleared:\n%s", view(p))
	}
}

func TestPickerRenameAndDescribe(t *testing.T) {
	p := newTest("GET /pets")
	moveTo(t, p, "pp_listPets")
	press(p, "r", "ctrl+u", "list_pets", "enter")
	if !strings.Contains(view(p), "pp_list_pets") {
		t.Errorf("renamed row should show the prefixed name:\n%s", view(p))
	}
	press(p, "e", "ctrl+u", "List every pet.", "enter")
	ts, _ := p.Result()
	o := ts.Tools["GET /pets"]
	if o.Name != "list_pets" || o.Description != "List every pet." {
		t.Errorf("override = %+v", o)
	}

	// Invalid names are refused and the editor stays open.
	press(p, "r", "ctrl+u", "bad name", "enter")
	if ts, _ := p.Result(); ts.Tools["GET /pets"].Name != "list_pets" {
		t.Errorf("invalid name accepted: %+v", ts.Tools["GET /pets"])
	}
	if !strings.Contains(view(p), "letters, digits") {
		t.Errorf("no validation message:\n%s", view(p))
	}
	press(p, "esc")

	// An empty name resets to the generated one.
	press(p, "r", "ctrl+u", "enter")
	if ts, _ := p.Result(); ts.Tools["GET /pets"].Name != "" {
		t.Errorf("reset failed: %+v", ts.Tools["GET /pets"])
	}
}

func TestPickerNameConflictBlocksSave(t *testing.T) {
	p := newTest("GET /pets", "GET /pets/{id}")
	moveTo(t, p, "pp_listPets")
	press(p, "r", "ctrl+u", "getPet", "enter")
	if isQuit(press(p, "s")) {
		t.Fatal("saved with a name conflict")
	}
	if !strings.Contains(view(p), "same name") {
		t.Errorf("conflict not shown:\n%s", view(p))
	}
}

func TestPickerPolicyCycle(t *testing.T) {
	p := newTest("POST /pets")
	moveTo(t, p, "pp_createPet")
	press(p, "p")
	if ts, _ := p.Result(); ts.Tools["POST /pets"].Policy != "confirm" {
		t.Errorf("policy = %q", ts.Tools["POST /pets"].Policy)
	}
	press(p, "p", "p", "p") // deny, allow, back to none
	if ts, _ := p.Result(); ts.Tools["POST /pets"].Policy != "" {
		t.Errorf("policy after full cycle = %q", ts.Tools["POST /pets"].Policy)
	}
}

// In read-only mode write tools can be saved but aren't offered, so they
// don't count toward the budget and are marked.
func TestPickerReadOnlyBudget(t *testing.T) {
	p := newTest("GET /pets")
	before := view(p)
	moveTo(t, p, "pp_createPet")
	press(p, "space")
	v := view(p)
	if !strings.Contains(v, "not offered") {
		t.Errorf("write tool not marked:\n%s", v)
	}
	if footer(before) == "" || tokensIn(footer(before)) != tokensIn(footer(v)) {
		t.Errorf("write tool changed the read-only budget: %q -> %q", footer(before), footer(v))
	}
}

func footer(v string) string {
	for _, l := range strings.Split(v, "\n") {
		if strings.Contains(l, " tools ·") || strings.Contains(l, " tool ·") {
			return l
		}
	}
	return ""
}

func tokensIn(f string) string {
	i := strings.Index(f, "≈")
	if i < 0 {
		return ""
	}
	return strings.Fields(f[i:])[0]
}

func TestPickerQuit(t *testing.T) {
	p := newTest("GET /pets")
	if !isQuit(press(p, "q")) {
		t.Fatal("q without changes should quit")
	}
	if _, saved := p.Result(); saved {
		t.Error("quit counted as save")
	}

	p = newTest("GET /pets")
	press(p, "space")
	if isQuit(press(p, "q")) {
		t.Fatal("q with changes should ask first")
	}
	if !strings.Contains(view(p), "Discard") {
		t.Errorf("no discard prompt:\n%s", view(p))
	}
	press(p, "n")
	if isQuit(press(p, "q")) || !isQuit(press(p, "y")) {
		t.Error("discard prompt didn't work")
	}
}

// Spec text is untrusted: nothing in it may reach the terminal as a
// control sequence, even if it got past parsing.
func TestPickerNeverRendersControlCharacters(t *testing.T) {
	evil := tl("GET", "/x", "pp_x", "tag\x1b[2J")
	evil.Description = "desc\x1b]0;title\x07\u202e"
	p := NewPicker(PickerInput{API: "x", Tools: []model.Tool{evil}, Mode: policy.ReadOnly})
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	for _, bad := range []string{"\x1b[2J", "\x1b]0;", "\x07", "\u202e"} {
		if strings.Contains(view(p), bad) {
			t.Errorf("view contains %q", bad)
		}
	}
}

// A small window still shows the cursor row and the footer.
func TestPickerScrolls(t *testing.T) {
	var many []model.Tool
	for i := range 60 {
		many = append(many, tl("GET", "/r"+string(rune('a'+i%26))+string(rune('a'+i/26)), "pp_r"+string(rune('a'+i%26))+string(rune('a'+i/26)), "t"))
	}
	p := NewPicker(PickerInput{API: "x", Tools: many, Mode: policy.ReadOnly})
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 15})
	for range 40 {
		press(p, "down")
	}
	v := view(p)
	if lines := strings.Count(v, "\n") + 1; lines > 15 {
		t.Errorf("view has %d lines for a 15-line window", lines)
	}
	if !strings.Contains(v, p.currentRowText()) || footer(v) == "" {
		t.Errorf("cursor row or footer missing:\n%s", v)
	}
}
