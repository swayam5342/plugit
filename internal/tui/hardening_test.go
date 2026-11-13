package tui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/curate"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/store"
)

const osc = "\x1b]0;owned\a"

func assertNoOSC(t *testing.T, what, view string) {
	t.Helper()
	if strings.Contains(view, "\x1b]") || strings.Contains(view, "\a") {
		t.Errorf("%s carries a raw escape sequence: %q", what, view)
	}
}

func TestPickerEndOnEmptyList(t *testing.T) {
	for _, k := range []string{"G", "end"} {
		t.Run(k, func(t *testing.T) {
			p := newTest()
			press(p, "/", "zzzz", "enter")
			if k == "end" {
				p.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
			} else {
				press(p, k)
			}
			if !strings.Contains(p.View().Content, "No tools match") {
				t.Error("empty list not shown")
			}
			press(p, "down", "space", "i", "esc")
			if p.cursor != 0 {
				t.Errorf("cursor = %d, want 0", p.cursor)
			}
		})
	}
}

func TestParamTypeIsSanitized(t *testing.T) {
	for name, schema := range map[string]string{
		"string": `{"type":"\u001b]0;owned\u0007str"}`,
		"list":   `{"type":["string","\u001b]0;owned\u0007null"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			tool := tl("GET", "/pets", "pp_listPets", "pets")
			tool.Params[0].Schema = json.RawMessage(schema)
			if got := paramType(tool.Params[0]); strings.ContainsAny(got, "\x1b\a") {
				t.Errorf("paramType = %q", got)
			}
			p := NewPicker(PickerInput{API: "pets", Tools: []model.Tool{tool}, Mode: policy.ReadOnly})
			p.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
			press(p, "down", "i")
			assertNoOSC(t, "tool detail", p.View().Content)
			m := NewTryIt(TryInput{Tool: tool, Mode: policy.ReadOnly})
			assertNoOSC(t, "try it", m.View().Content)
		})
	}
}

func TestViewsSanitizeValidatedValues(t *testing.T) {
	tool := tl("GET"+osc, "/pets", "pp_listPets", "pets")
	ts := config.Toolset{Tools: map[string]config.ToolOverride{string(tool.Key): {Policy: "deny" + osc}}}
	p := NewPicker(PickerInput{API: "pets", Tools: []model.Tool{tool}, Mode: policy.ReadOnly, Selected: ts})
	p.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	press(p, "down")
	assertNoOSC(t, "picker", p.View().Content)
	press(p, "i")
	assertNoOSC(t, "tool detail", p.View().Content)

	assertNoOSC(t, "try it", NewTryIt(TryInput{Tool: tool, Mode: policy.ReadOnly}).View().Content)

	in := NewInspector(InspectorInput{Title: "pets"})
	in.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	in.Update(CallsMsg{Calls: []store.Call{{ID: 1, Tool: "pp_listPets", Op: "GET" + osc + " /pets", Outcome: store.Outcome("ok" + osc)}}})
	in.Update(key("enter"))
	assertNoOSC(t, "inspector", in.View().Content)

	s := NewSyncScreen(SyncInput{API: "pets"})
	s.in.Diff.Added = []curate.OpChange{{Key: model.OpKey("GET" + osc + " /pets"), New: &tool, Listed: []string{"default" + osc}}}
	s.in.Diff.Changed = s.in.Diff.Added
	s.in.Diff.Removed = s.in.Diff.Added
	s.in.Diff.Removed[0].Old = &tool
	assertNoOSC(t, "sync", s.View().Content)
}

func TestDetailShowsEditedMode(t *testing.T) {
	p := newTest()
	press(p, "down", "m", "i")
	head := strings.Join(strings.Split(p.View().Content, "\n")[:3], "\n")
	if !strings.Contains(head, string(policy.ConfirmWrites)) {
		t.Errorf("detail header should show confirm-writes:\n%s", head)
	}
}

func TestWrapLinesCountsCells(t *testing.T) {
	const text = "日本語のテキストです" // ten characters, two cells each
	lines := wrapLines(text, 8)
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > 8 {
			t.Errorf("line %q is %d cells wide, want at most 8", l, w)
		}
	}
	if got := strings.Join(lines, ""); got != text {
		t.Errorf("wrapping lost text: %q", got)
	}
	if got := wrapLines("ab\n\ncd", 10); len(got) != 3 || got[1] != "" {
		t.Errorf("blank lines not kept: %q", got)
	}
}

func TestInspectorWrapsLongDetails(t *testing.T) {
	long := strings.Repeat("x", 150) + "END-OF-ARGS"
	args, _ := json.Marshal(map[string]string{"q": long})
	in := NewInspector(InspectorInput{Title: "pets"})
	in.Update(tea.WindowSizeMsg{Width: 60, Height: 40})
	in.Update(CallsMsg{Calls: []store.Call{{ID: 1, Tool: "pp_listPets", Op: "GET /pets", Outcome: store.Failed, Args: args,
		Error: strings.Repeat("e", 150) + "END-OF-ERROR", Preview: strings.Repeat("p", 150) + "END-OF-PREVIEW"}}})
	in.Update(key("enter"))
	view := strings.Join(strings.Fields(ansi.Strip(in.View().Content)), "")
	view = strings.NewReplacer("│", "", "…", "").Replace(view)
	for _, want := range []string{"END-OF-ARGS", "END-OF-ERROR", "END-OF-PREVIEW"} {
		if !strings.Contains(view, want) {
			t.Errorf("inspector detail is missing %s", want)
		}
	}
	for _, l := range strings.Split(in.View().Content, "\n") {
		if w := ansi.StringWidth(l); w > 60 {
			t.Errorf("line is %d cells wide in a 60-cell window: %q", w, ansi.Strip(l))
		}
	}
}

func TestTryItQuitCancelsCall(t *testing.T) {
	started, done := make(chan struct{}), make(chan error, 1)
	m := NewTryIt(TryInput{Tool: tl("GET", "/pets", "pp_listPets", "pets"), Send: func(ctx context.Context, _ map[string]string) TryResult {
		close(started)
		<-ctx.Done()
		done <- ctx.Err()
		return TryResult{}
	}})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+s did not start a call")
	}
	go cmd()
	<-started
	m.Update(key("esc"))
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("call ended with %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the call was not cancelled when the screen quit")
	}
}
