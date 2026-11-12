package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
)

func tryTool() model.Tool {
	return model.Tool{
		Key: "GET /pets/{id}", Name: "pp_get_pet", Description: "Returns a single pet.", Method: "GET", Path: "/pets/{id}",
		Params: []model.Param{
			{Name: "id", UpstreamName: "id", In: model.InPath, Required: true, Schema: json.RawMessage(`{"type":"string"}`), Desc: "The pet's ID"},
			{Name: "verbose", UpstreamName: "verbose", In: model.InQuery, Schema: json.RawMessage(`{"type":"boolean"}`)},
			{Name: "X-Api-Key", UpstreamName: "X-Api-Key", In: model.InHeader, Hidden: true, Schema: json.RawMessage(`{}`)},
		},
	}
}

type sent struct {
	args map[string]string
	n    int
}

func newTryTest(confirm string, res TryResult) (*TryIt, *sent) {
	s := &sent{}
	m := NewTryIt(TryInput{
		API: "pets", Tool: tryTool(), Mode: policy.ReadOnly, Confirm: confirm,
		Send: func(_ context.Context, args map[string]string) TryResult {
			s.n++
			s.args = args
			return res
		},
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m, s
}

func tryView(m *TryIt) string { return ansi.Strip(m.View().Content) }

// run feeds a command's message back, as Bubble Tea would.
func run(m *TryIt, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		if _, ok := msg.(tea.BatchMsg); ok || msg == nil {
			return
		}
		_, cmd = m.Update(msg)
	}
}

func typeInto(m *TryIt, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func ctrlS() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl} }

func TestTryItForm(t *testing.T) {
	m, _ := newTryTest("", TryResult{})
	v := tryView(m)
	for _, want := range []string{"try", "pp_get_pet", "GET /pets/{id}", "Returns a single pet.", "id", "required", "path", "The pet's ID", "verbose", "boolean", "query", "read-only"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "X-Api-Key") {
		t.Errorf("hidden credential param shown:\n%s", v)
	}
}

func TestTryItSends(t *testing.T) {
	m, s := newTryTest("", TryResult{Request: "→ GET https://pets.example.test/pets/42\n  X-Api-Key: ***", Status: 200, Response: `{"id":42,"name":"rex"}`})
	typeInto(m, "42")
	m.Update(key("down")) // to verbose, left empty
	_, cmd := m.Update(ctrlS())
	run(m, cmd)
	if s.n != 1 || s.args["id"] != "42" || len(s.args) != 1 {
		t.Fatalf("sent %d times with %v (empty fields must be left out)", s.n, s.args)
	}
	v := tryView(m)
	for _, want := range []string{"Request", "GET https://pets.example.test/pets/42", "X-Api-Key: ***", "Response", "200", "rex"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

// Tools whose policy needs confirmation ask before sending.
func TestTryItConfirms(t *testing.T) {
	m, s := newTryTest("this is a write tool", TryResult{Status: 201, Response: "created"})
	typeInto(m, "1")
	m.Update(ctrlS())
	if s.n != 0 || !strings.Contains(tryView(m), "Send it?") {
		t.Fatalf("sent without asking (n=%d):\n%s", s.n, tryView(m))
	}
	m.Update(key("n"))
	if s.n != 0 || strings.Contains(tryView(m), "Send it?") {
		t.Fatal("n should cancel")
	}
	m.Update(ctrlS())
	_, cmd := m.Update(key("y"))
	run(m, cmd)
	if s.n != 1 || !strings.Contains(tryView(m), "created") {
		t.Errorf("y should send (n=%d):\n%s", s.n, tryView(m))
	}
}

func TestTryItShowsErrors(t *testing.T) {
	m, _ := newTryTest("", TryResult{Err: "pp_get_pet: missing required argument \"id\""})
	_, cmd := m.Update(ctrlS())
	run(m, cmd)
	if !strings.Contains(tryView(m), "missing required argument") {
		t.Errorf("error not shown:\n%s", tryView(m))
	}
}

// Responses are upstream data: no control sequences reach the terminal.
func TestTryItSanitizes(t *testing.T) {
	m, _ := newTryTest("", TryResult{Status: 200, Response: "line1\n\x1b[2Jpwned\x1b]0;title\x07" + string(rune(0x202e))})
	_, cmd := m.Update(ctrlS())
	run(m, cmd)
	raw := m.View().Content
	for _, bad := range []string{"\x1b[2J", "\x1b]0;", "\x07", string(rune(0x202e))} {
		if strings.Contains(raw, bad) {
			t.Errorf("view contains %q", bad)
		}
	}
	if !strings.Contains(tryView(m), "pwned") {
		t.Error("text around the escapes should still show")
	}
}

func TestTryItFitsAndQuits(t *testing.T) {
	long := strings.Repeat(`{"field":"value"},`, 200)
	m, _ := newTryTest("", TryResult{Request: strings.Repeat("→ GET /x\n", 20), Status: 200, Response: long})
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 20})
	_, cmd := m.Update(ctrlS())
	run(m, cmd)
	v := m.View().Content
	if n := strings.Count(v, "\n") + 1; n > 20 {
		t.Errorf("%d lines for a 20-line window", n)
	}
	for _, l := range strings.Split(v, "\n") {
		if w := ansi.StringWidth(l); w > 50 {
			t.Errorf("line is %d wide: %q", w, ansi.Strip(l))
		}
	}
	if _, cmd := m.Update(key("esc")); !isQuit(cmd) {
		t.Error("esc should quit")
	}
}
