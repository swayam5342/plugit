package mcpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/policy"
	"github.com/swayam5342/plugit/internal/proxy"
	"github.com/swayam5342/plugit/internal/store"
)

type recorded struct {
	mu    sync.Mutex
	calls []store.Call
}

func (r *recorded) record(c store.Call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *recorded) last(t *testing.T) store.Call {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		t.Fatal("no call recorded")
	}
	return r.calls[len(r.calls)-1]
}

func callLog(rec *recorded, args config.LogArgs, preview bool) *CallLog {
	return &CallLog{Record: rec.record, API: "pets", Toolset: "default", Args: args, Preview: preview}
}

// Every call is recorded with redacted metadata (SPEC §3.7 step 9, M4 exit).
func TestCallsAreRecorded(t *testing.T) {
	rec := &recorded{}
	r := setup(t, Config{Tools: served(listPets), Mode: policy.ReadOnly, Log: callLog(rec, config.LogArgsRedacted, false)}, nil)
	if _, err := r.call(t, "listPets", map[string]any{"limit": 5}); err != nil {
		t.Fatal(err)
	}
	c := rec.last(t)
	if c.API != "pets" || c.Toolset != "default" || c.Tool != "listPets" || c.Op != "GET /pets" ||
		c.Outcome != store.OK || c.Status != 200 || c.Bytes == 0 || c.Time.IsZero() || c.Duration < 0 {
		t.Errorf("record = %+v", c)
	}
	if string(c.Args) != `{"limit":5}` || c.Preview != "" || c.Error != "" {
		t.Errorf("args=%s preview=%q error=%q", c.Args, c.Preview, c.Error)
	}
}

// The secret the upstream echoes back never reaches the call log: not in
// the error, the preview or the arguments.
func TestRecordsAreRedacted(t *testing.T) {
	echo := func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad token ` + req.Header.Get("Authorization") + `"}`))
	}
	withSecretArg := tool("GET /pets", "listPets", qparam("limit", false, `{"type":"integer"}`), qparam("note", false, `{"type":"string"}`), qparam("api_key", false, `{"type":"string"}`))
	rec := &recorded{}
	r := setup(t, Config{Tools: served(withSecretArg), Mode: policy.ReadOnly, Log: callLog(rec, config.LogArgsRedacted, true),
		Target: proxy.Target{Auth: proxy.Auth{Type: proxy.AuthBearer, Secret: secret}}}, echo)
	if _, err := r.call(t, "listPets", map[string]any{"note": "my " + secret, "api_key": "xyz-not-a-real-key"}); err != nil {
		t.Fatal(err)
	}
	c := rec.last(t)
	if c.Outcome != store.HTTPError || c.Status != 401 || c.Error == "" || c.Preview == "" {
		t.Errorf("record = %+v", c)
	}
	all, _ := json.Marshal(c)
	for _, leak := range []string{secret, "xyz-not-a-real-key"} {
		if strings.Contains(string(all), leak) {
			t.Errorf("record leaks %q: %s", leak, all)
		}
	}
}

func TestRecordOutcomes(t *testing.T) {
	rec := &recorded{}
	r := setup(t, Config{Tools: served(listPets, createPet), Mode: policy.ConfirmWrites, Log: callLog(rec, config.LogArgsNone, false)}, nil)

	if _, err := r.call(t, "createPet", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if c := rec.last(t); c.Outcome != store.Denied || c.Tool != "createPet" || c.Error == "" {
		t.Errorf("confirm: %+v", c)
	}
	if _, err := r.call(t, "listPets", map[string]any{"limit": 1000}); err != nil {
		t.Fatal(err)
	}
	if c := rec.last(t); c.Outcome != store.Invalid || c.Args != nil {
		t.Errorf("invalid (log.args none): %+v", c)
	}
}

func TestRecordPanicAndFullArgs(t *testing.T) {
	rec := &recorded{}
	r := setup(t, Config{Tools: served(listPets), Mode: policy.ReadOnly, Doer: &panicDoer{}, Log: callLog(rec, config.LogArgsFull, false)}, nil)
	if _, err := r.call(t, "listPets", map[string]any{"limit": 2}); err != nil {
		t.Fatal(err)
	}
	if c := rec.last(t); c.Outcome != store.Failed || string(c.Args) != `{"limit":2}` {
		t.Errorf("panic: %+v", c)
	}
}

// Tools that never reach a handler (unlisted) aren't recorded, and a nil
// CallLog records nothing.
func TestNoCallLog(t *testing.T) {
	r := setup(t, Config{Tools: served(listPets), Mode: policy.ReadOnly}, nil)
	if _, err := r.call(t, "listPets", map[string]any{}); err != nil {
		t.Fatal(err)
	}
}
