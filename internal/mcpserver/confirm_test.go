package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/proxy"
	"github.com/swayam5342/plugit/internal/store"
)

// createPetBody takes a name and a secret-looking field, to check what the
// confirmation shows.
var createPetBody = tool("POST /pets", "createPet",
	model.Param{Name: "name", UpstreamName: "name", In: model.InBody, Schema: json.RawMessage(`{"type":"string"}`)},
	model.Param{Name: "api_token", UpstreamName: "api_token", In: model.InBody, Schema: json.RawMessage(`{"type":"string"}`)},
)

// elicitor answers elicitation requests like a user would.
type elicitor struct {
	asked   atomic.Int32
	message atomic.Value
	answer  func() (*mcp.ElicitResult, error)
}

func (e *elicitor) opts() *mcp.ClientOptions {
	return &mcp.ClientOptions{ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		e.asked.Add(1)
		e.message.Store(req.Params.Message)
		return e.answer()
	}}
}

func accept(confirm any) func() (*mcp.ElicitResult, error) {
	return func() (*mcp.ElicitResult, error) {
		content := map[string]any{}
		if confirm != nil {
			content["confirm"] = confirm
		}
		return &mcp.ElicitResult{Action: "accept", Content: content}, nil
	}
}

func confirmRig(t *testing.T, e *elicitor, rec *recorded) *rig {
	t.Helper()
	cfg := Config{Tools: served(createPetBody), Mode: policy.ConfirmWrites,
		Target: proxy.Target{Auth: proxy.Auth{Type: proxy.AuthBearer, Secret: secret}}}
	if rec != nil {
		cfg.Log = callLog(rec, "redacted", false)
	}
	return setupClient(t, cfg, nil, e.opts())
}

// confirm-writes asks the user through the client (MCP elicitation) and
// only calls the API when they accept (SPEC §3.7 step 2).
func TestConfirmViaElicitation(t *testing.T) {
	e := &elicitor{answer: accept(true)}
	r := confirmRig(t, e, nil)
	res, err := r.call(t, "createPet", map[string]any{"name": "rex", "api_token": "tok-not-real-1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || r.hits.Load() != 1 || e.asked.Load() != 1 {
		t.Fatalf("isError=%v hits=%d asked=%d text=%s", res.IsError, r.hits.Load(), e.asked.Load(), text(res))
	}
	msg, _ := e.message.Load().(string)
	for _, want := range []string{"createPet", "POST /pets", "rex"} {
		if !strings.Contains(msg, want) {
			t.Errorf("confirmation message missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "tok-not-real-1") || strings.Contains(msg, secret) {
		t.Errorf("confirmation message shows a secret:\n%s", msg)
	}
}

// A clients that drops the field (accept, no content) counts as yes.
func TestConfirmAcceptWithoutContent(t *testing.T) {
	e := &elicitor{answer: accept(nil)}
	r := confirmRig(t, e, nil)
	if res, _ := r.call(t, "createPet", map[string]any{"name": "rex"}); res.IsError || r.hits.Load() != 1 {
		t.Errorf("isError=%v hits=%d", res.IsError, r.hits.Load())
	}
}

func TestConfirmRefusals(t *testing.T) {
	cases := map[string]struct {
		answer func() (*mcp.ElicitResult, error)
		want   string
	}{
		"declined":  {func() (*mcp.ElicitResult, error) { return &mcp.ElicitResult{Action: "decline"}, nil }, "declined"},
		"cancelled": {func() (*mcp.ElicitResult, error) { return &mcp.ElicitResult{Action: "cancel"}, nil }, "cancelled"},
		"unticked":  {accept(false), "declined"},
		"failed":    {func() (*mcp.ElicitResult, error) { return nil, errors.New("ui crashed") }, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &elicitor{answer: tc.answer}
			rec := &recorded{}
			r := confirmRig(t, e, rec)
			res, err := r.call(t, "createPet", map[string]any{"name": "rex"})
			if r.hits.Load() != 0 {
				t.Fatalf("the API was called (hits=%d)", r.hits.Load())
			}
			if tc.want == "" { // the client failed to answer: a protocol error
				if err == nil && !res.IsError {
					t.Errorf("call succeeded: %s", text(res))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError || !strings.Contains(text(res), tc.want) {
				t.Errorf("isError=%v text=%s", res.IsError, text(res))
			}
			if c := rec.last(t); c.Outcome != store.Denied {
				t.Errorf("recorded %+v", c)
			}
		})
	}
}

// Invalid arguments are refused before the user is bothered.
func TestConfirmValidatesFirst(t *testing.T) {
	e := &elicitor{answer: accept(true)}
	r := confirmRig(t, e, nil)
	res, _ := r.call(t, "createPet", map[string]any{"bogus": 1})
	if !res.IsError || e.asked.Load() != 0 || r.hits.Load() != 0 {
		t.Errorf("isError=%v asked=%d hits=%d", res.IsError, e.asked.Load(), r.hits.Load())
	}
}

// Read-only tools never ask, even in confirm-writes mode.
func TestReadsDontAsk(t *testing.T) {
	e := &elicitor{answer: accept(true)}
	r := setupClient(t, Config{Tools: served(listPets), Mode: policy.ConfirmWrites}, nil, e.opts())
	if res, _ := r.call(t, "listPets", map[string]any{}); res.IsError || e.asked.Load() != 0 {
		t.Errorf("isError=%v asked=%d", res.IsError, e.asked.Load())
	}
}

// The answer is bound to the arguments it was given for, and expires.
func TestConfirmStateBinding(t *testing.T) {
	h := &handler{tool: Tool{Tool: createPetBody}, key: []byte("0123456789abcdef0123456789abcdef")}
	args := map[string]any{"name": "rex"}
	st := h.state(now(), args)
	if err := h.checkState(st, args); err != nil {
		t.Fatalf("valid state refused: %v", err)
	}
	if err := h.checkState(st, map[string]any{"name": "everything"}); err == nil {
		t.Error("state accepted for different arguments")
	}
	other := &handler{tool: Tool{Tool: listPets}, key: h.key}
	if err := other.checkState(st, args); err == nil {
		t.Error("state accepted for a different tool")
	}
	for _, bad := range []string{"", "x", "1.abc", st + "x"} {
		if err := h.checkState(bad, args); err == nil {
			t.Errorf("state %q accepted", bad)
		}
	}
	old := h.state(now().Add(-ConfirmTimeout-time.Minute), args)
	if err := h.checkState(old, args); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("stale state: %v", err)
	}
}

// Clients on protocol versions before multi round-trip requests get a
// plain elicitation/create request (the SDK converts) and the same result.
func TestConfirmOlderProtocol(t *testing.T) {
	e := &elicitor{answer: accept(true)}
	cfg := Config{Tools: served(createPetBody), Mode: policy.ConfirmWrites}
	r := setupClientVersion(t, cfg, e.opts(), "2025-06-18")
	res, err := r.call(t, "createPet", map[string]any{"name": "rex"})
	if err != nil || res.IsError || r.hits.Load() != 1 || e.asked.Load() != 1 {
		t.Fatalf("err=%v isError=%v hits=%d asked=%d text=%s", err, res != nil && res.IsError, r.hits.Load(), e.asked.Load(), text(res))
	}
}
