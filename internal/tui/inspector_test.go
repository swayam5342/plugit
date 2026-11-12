package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/swayam5342/plugit/internal/store"
)

var ti0 = time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)

func logCall(id int64, tool string, outcome store.Outcome, status int, ms int) store.Call {
	return store.Call{
		ID: id, Time: ti0.Add(time.Duration(id) * time.Second), API: "pets", Toolset: "default",
		Tool: tool, Op: "GET /" + tool, Outcome: outcome, Status: status,
		Duration: time.Duration(ms) * time.Millisecond, Bytes: 1200,
	}
}

func noSource(context.Context, int64) ([]store.Call, error) { return nil, nil }

func newInspectorTest() *Inspector {
	in := NewInspector(InspectorInput{Title: "pets", Source: noSource, Interval: time.Hour})
	in.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return in
}

func inspView(in *Inspector) string { return ansi.Strip(in.View().Content) }

func TestInspectorShowsCalls(t *testing.T) {
	in := newInspectorTest()
	if !strings.Contains(inspView(in), "Waiting for calls") {
		t.Errorf("empty view:\n%s", inspView(in))
	}
	_, cmd := in.Update(CallsMsg{Calls: []store.Call{
		logCall(1, "pp_list_pets", store.OK, 200, 42),
		logCall(2, "pp_get_pet", store.HTTPError, 404, 7),
	}})
	if cmd == nil {
		t.Error("no poll scheduled after new calls")
	}
	v := inspView(in)
	for _, want := range []string{"plugit", "logs", "pets", "pp_list_pets", "pp_get_pet", "404", "42ms", "2 calls", "1 error"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

// New calls keep the cursor on the newest one unless the user moved away.
func TestInspectorFollows(t *testing.T) {
	in := newInspectorTest()
	in.Update(CallsMsg{Calls: []store.Call{logCall(1, "a", store.OK, 200, 1), logCall(2, "b", store.OK, 200, 1)}})
	if in.current().Tool != "b" {
		t.Fatalf("cursor on %q, want the newest", in.current().Tool)
	}
	in.Update(key("up"))
	in.Update(CallsMsg{Calls: []store.Call{logCall(3, "c", store.OK, 200, 1)}})
	if in.current().Tool != "a" {
		t.Errorf("cursor moved to %q while the user was reading", in.current().Tool)
	}
	in.Update(key("G"))
	in.Update(CallsMsg{Calls: []store.Call{logCall(4, "d", store.OK, 200, 1)}})
	if in.current().Tool != "d" {
		t.Errorf("after G, cursor on %q", in.current().Tool)
	}
}

func TestInspectorExpand(t *testing.T) {
	in := newInspectorTest()
	c := logCall(1, "pp_get_pet", store.HTTPError, 404, 7)
	c.Args = json.RawMessage(`{"id":"42"}`)
	c.Error = "pp_get_pet: HTTP 404 Not Found"
	c.Preview = `{"message":"no such pet"}`
	in.Update(CallsMsg{Calls: []store.Call{c}})
	if strings.Contains(inspView(in), "no such pet") {
		t.Error("details shown before enter")
	}
	in.Update(key("enter"))
	v := inspView(in)
	for _, want := range []string{"args", `"id"`, "42", "HTTP 404", "no such pet", "GET /pp_get_pet"} {
		if !strings.Contains(v, want) {
			t.Errorf("details missing %q:\n%s", want, v)
		}
	}
}

// Previews and errors are upstream text: they must not reach the terminal
// as control sequences.
func TestInspectorSanitizes(t *testing.T) {
	in := newInspectorTest()
	c := logCall(1, "x", store.OK, 200, 1)
	c.Preview = "ok\x1b[2J\x1b]0;pwned\x07" + string(rune(0x202e))
	c.Error = "bad\x1b[31m"
	in.Update(CallsMsg{Calls: []store.Call{c}})
	in.Update(key("enter"))
	raw := in.View().Content
	for _, bad := range []string{"\x1b[2J", "\x1b]0;", "\x07", string(rune(0x202e))} {
		if strings.Contains(raw, bad) {
			t.Errorf("view contains %q", bad)
		}
	}
}

func TestInspectorFitsAndQuits(t *testing.T) {
	in := NewInspector(InspectorInput{Title: "pets", Source: noSource, Interval: time.Hour})
	in.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	var calls []store.Call
	for i := range 50 {
		calls = append(calls, logCall(int64(i+1), strings.Repeat("t", 30), store.OK, 200, i))
	}
	in.Update(CallsMsg{Calls: calls})
	in.Update(key("enter"))
	v := in.View().Content
	if n := strings.Count(v, "\n") + 1; n > 12 {
		t.Errorf("%d lines for a 12-line window", n)
	}
	for _, l := range strings.Split(v, "\n") {
		if w := ansi.StringWidth(l); w > 40 {
			t.Errorf("line is %d wide: %q", w, ansi.Strip(l))
		}
	}
	_, cmd := in.Update(key("q"))
	if !isQuit(cmd) {
		t.Error("q should quit")
	}
}

// A failing source is shown, and polling continues.
func TestInspectorSourceError(t *testing.T) {
	in := newInspectorTest()
	_, cmd := in.Update(CallsMsg{Err: context.DeadlineExceeded})
	if cmd == nil || !strings.Contains(inspView(in), "deadline") {
		t.Errorf("error not shown or polling stopped:\n%s", inspView(in))
	}
}
