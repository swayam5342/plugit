package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)

func open(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "plugit.db")
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func call(api, tool string, at time.Time, ms int, outcome Outcome) Call {
	return Call{
		Time: at, API: api, Toolset: "default", Tool: tool, Op: "GET /" + tool,
		Outcome: outcome, Status: 200, Duration: time.Duration(ms) * time.Millisecond, Bytes: 100,
	}
}

func TestOpenCreatesPrivateFile(t *testing.T) {
	_, path := open(t)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("db mode = %v, want 0600", fi.Mode().Perm())
		}
		if di, _ := os.Stat(filepath.Dir(path)); di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
		}
	}
}

func TestInsertAndList(t *testing.T) {
	db, _ := open(t)
	ctx := t.Context()
	c := call("pets", "list_pets", t0, 42, OK)
	c.Args = json.RawMessage(`{"limit":5}`)
	c.Preview = "[{\"id\":1}]"
	id, err := db.Insert(ctx, c)
	if err != nil || id == 0 {
		t.Fatalf("Insert = %d, %v", id, err)
	}
	if _, err := db.Insert(ctx, call("other", "x", t0.Add(time.Second), 7, HTTPError)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Insert(ctx, call("pets", "get_pet", t0.Add(2*time.Second), 9, Denied)); err != nil {
		t.Fatal(err)
	}

	got, err := db.List(ctx, Filter{API: "pets"})
	if err != nil || len(got) != 2 {
		t.Fatalf("List = %+v, %v", got, err)
	}
	first := got[0]
	if first.ID != id || first.Tool != "list_pets" || !first.Time.Equal(t0) || first.Duration != 42*time.Millisecond ||
		string(first.Args) != `{"limit":5}` || first.Preview != c.Preview || first.Outcome != OK || first.Op != "GET /list_pets" {
		t.Errorf("round trip = %+v", first)
	}
	if after, _ := db.List(ctx, Filter{AfterID: id}); len(after) != 2 || after[0].API != "other" {
		t.Errorf("AfterID = %+v", after)
	}
	if lim, _ := db.List(ctx, Filter{Limit: 1, Newest: true}); len(lim) != 1 || lim[0].Tool != "get_pet" {
		t.Errorf("newest = %+v", lim)
	}
}

// Stored text is capped, so a huge argument or response can't bloat the log.
func TestInsertCapsSizes(t *testing.T) {
	db, _ := open(t)
	c := call("pets", "x", t0, 1, OK)
	c.Args = json.RawMessage(`{"q":"` + strings.Repeat("a", MaxArgsBytes) + `"}`)
	c.Preview = strings.Repeat("b", MaxPreviewBytes*2)
	c.Error = strings.Repeat("c", MaxErrorBytes*2)
	if _, err := db.Insert(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	got, _ := db.List(t.Context(), Filter{})
	if string(got[0].Args) != `{"truncated":true}` || len(got[0].Preview) > MaxPreviewBytes || len(got[0].Error) > MaxErrorBytes {
		t.Errorf("not capped: args %d, preview %d, error %d", len(got[0].Args), len(got[0].Preview), len(got[0].Error))
	}
}

func TestPrune(t *testing.T) {
	db, _ := open(t)
	ctx := t.Context()
	for i := range 5 {
		if _, err := db.Insert(ctx, call("pets", "x", t0.Add(time.Duration(i)*24*time.Hour), 1, OK)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Insert(ctx, call("other", "x", t0, 1, OK)); err != nil {
		t.Fatal(err)
	}
	n, err := db.Prune(ctx, "pets", t0.Add(3*24*time.Hour))
	if err != nil || n != 3 {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	if left, _ := db.List(ctx, Filter{}); len(left) != 3 {
		t.Errorf("left = %d rows, want 3 (2 pets + 1 other)", len(left))
	}
}

func TestStats(t *testing.T) {
	db, _ := open(t)
	ctx := t.Context()
	for i := 1; i <= 20; i++ {
		o := OK
		if i%5 == 0 {
			o = HTTPError
		}
		if _, err := db.Insert(ctx, call("pets", "list_pets", t0.Add(time.Duration(i)*time.Minute), i*10, o)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Insert(ctx, call("pets", "get_pet", t0, 5, OK)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Insert(ctx, call("other", "z", t0, 5, OK)); err != nil {
		t.Fatal(err)
	}
	st, err := db.Stats(ctx, Filter{API: "pets"})
	if err != nil || len(st) != 2 {
		t.Fatalf("Stats = %+v, %v", st, err)
	}
	lp := st[0] // most calls first
	if lp.Tool != "list_pets" || lp.Calls != 20 || lp.Errors != 4 || lp.Bytes != 2000 {
		t.Errorf("list_pets = %+v", lp)
	}
	if lp.P50 != 100*time.Millisecond || lp.P95 != 190*time.Millisecond {
		t.Errorf("p50 = %v, p95 = %v", lp.P50, lp.P95)
	}
	if !lp.Last.Equal(t0.Add(20 * time.Minute)) {
		t.Errorf("last = %v", lp.Last)
	}
	since, _ := db.Stats(ctx, Filter{API: "pets", Since: t0.Add(16 * time.Minute)})
	if len(since) != 1 || since[0].Calls != 5 {
		t.Errorf("since = %+v", since)
	}
}

// Several plugit serve processes (one per client) share the file.
func TestConcurrentWriters(t *testing.T) {
	_, path := open(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4*50)
	for range 4 {
		db, err := Open(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		wg.Go(func() {
			for range 50 {
				if _, err := db.Insert(t.Context(), call("pets", "x", t0, 1, OK)); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	db, _ := Open(t.Context(), path)
	defer func() { _ = db.Close() }()
	if got, _ := db.List(t.Context(), Filter{Limit: 1000}); len(got) != 200 {
		t.Errorf("rows = %d, want 200", len(got))
	}
}

func TestNewerSchemaRefused(t *testing.T) {
	db, path := open(t)
	if _, err := db.sql.ExecContext(t.Context(), "PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(t.Context(), path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("err = %v", err)
	}
}

// Recording never blocks a tool call: a full buffer drops, Close drains.
func TestRecorder(t *testing.T) {
	db, _ := open(t)
	var failures []error
	rec := NewRecorder(db, 8, func(err error) { failures = append(failures, err) })
	start := time.Now()
	for range 2000 {
		rec.Record(call("pets", "x", t0, 1, OK))
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Record blocked for %v", d)
	}
	rec.Close()
	rec.Record(call("pets", "late", t0, 1, OK)) // after Close: ignored, no panic
	got, _ := db.List(t.Context(), Filter{Limit: 5000})
	if int64(len(got))+rec.Dropped() != 2000 {
		t.Errorf("stored %d + dropped %d != 2000", len(got), rec.Dropped())
	}
	if len(got) == 0 || len(failures) != 0 {
		t.Errorf("stored %d, failures %v", len(got), failures)
	}
}
