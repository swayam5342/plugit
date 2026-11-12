package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/store"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func seedLog(t *testing.T, p config.Paths, calls ...store.Call) *store.DB {
	t.Helper()
	db, err := store.Open(t.Context(), p.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, c := range calls {
		if _, err := db.Insert(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func lcall(api, tool string, outcome store.Outcome, status int) store.Call {
	return store.Call{Time: time.Now(), API: api, Toolset: "default", Tool: tool, Op: "GET /x",
		Outcome: outcome, Status: status, Duration: 42 * time.Millisecond, Bytes: 1200}
}

func TestLogsPlain(t *testing.T) {
	p := config.NewPaths(t.TempDir())
	bad := lcall("pets", "pp_get_pet", store.HTTPError, 404)
	bad.Error = "pp_get_pet: HTTP 404\x1b[2J"
	seedLog(t, p, lcall("pets", "pp_list_pets", store.OK, 200), bad, lcall("other", "o_x", store.OK, 200))
	out := &bytes.Buffer{}
	if err := showLogs(t.Context(), p, logsSettings{name: "pets", out: out}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"pp_list_pets", "pp_get_pet", "http_error", "404", "42ms", "1.2 KB", "HTTP 404"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "o_x") || strings.Contains(s, "\x1b") {
		t.Errorf("other API shown or escape not stripped:\n%q", s)
	}
}

func TestLogsJSON(t *testing.T) {
	p := config.NewPaths(t.TempDir())
	seedLog(t, p, lcall("pets", "a", store.OK, 200), lcall("pets", "b", store.Denied, 0))
	out := &bytes.Buffer{}
	if err := showLogs(t.Context(), p, logsSettings{out: out, asJSON: true}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	var c store.Call
	if err := json.Unmarshal([]byte(lines[1]), &c); err != nil || c.Tool != "b" || c.Outcome != store.Denied {
		t.Errorf("line = %s (%v)", lines[1], err)
	}
}

func TestLogsLimit(t *testing.T) {
	p := config.NewPaths(t.TempDir())
	seedLog(t, p, lcall("pets", "first", store.OK, 200), lcall("pets", "second", store.OK, 200), lcall("pets", "third", store.OK, 200))
	out := &bytes.Buffer{}
	if err := showLogs(t.Context(), p, logsSettings{out: out, limit: 2}); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); strings.Contains(s, "first") || !strings.Contains(s, "third") {
		t.Errorf("limit should keep the newest:\n%s", s)
	}
}

// --follow keeps printing new calls until cancelled.
func TestLogsFollow(t *testing.T) {
	p := config.NewPaths(t.TempDir())
	db := seedLog(t, p, lcall("pets", "before", store.OK, 200))
	ctx, cancel := context.WithCancel(t.Context())
	out := &lockedBuf{}
	done := make(chan error, 1)
	go func() {
		done <- showLogs(ctx, p, logsSettings{out: out, follow: true, interval: 10 * time.Millisecond})
	}()
	if _, err := db.Insert(t.Context(), lcall("pets", "after", store.OK, 200)); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && !strings.Contains(out.String(), "after"); {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("follow returned %v", err)
	}
	if s := out.String(); !strings.Contains(s, "before") || !strings.Contains(s, "after") {
		t.Errorf("output:\n%s", s)
	}
}

func TestLogsEmpty(t *testing.T) {
	p := config.NewPaths(t.TempDir())
	out := &bytes.Buffer{}
	if err := showLogs(t.Context(), p, logsSettings{out: out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No calls recorded yet") {
		t.Errorf("output: %s", out)
	}
	if err := showLogs(t.Context(), p, logsSettings{name: "../etc", out: out}); err == nil {
		t.Error("bad API name accepted")
	}
}

// On a terminal, logs opens the live inspector instead of printing.
func TestLogsInteractive(t *testing.T) {
	p := config.NewPaths(t.TempDir())
	seedLog(t, p, lcall("pets", "a", store.OK, 200))
	opened := false
	s := logsSettings{name: "pets", out: &bytes.Buffer{}, interactive: func(ctx context.Context, title string, all bool, src func(context.Context, int64) ([]store.Call, error)) error {
		opened = true
		calls, err := src(ctx, 0)
		if err != nil || len(calls) != 1 || title != "pets" || all {
			t.Errorf("source = %v, %v; title %q all %v", calls, err, title, all)
		}
		return nil
	}}
	if err := showLogs(t.Context(), p, s); err != nil || !opened {
		t.Errorf("err = %v, opened = %v", err, opened)
	}
}
