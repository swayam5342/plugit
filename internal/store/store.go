// Package store is PlugIt's call log (SPEC §3.6 data/plugit.db, §3.7 step
// 9): one row of redacted metadata per tool call, kept for the API's
// log.retention, read by `plugit logs` and `plugit stats`. It is SQLite
// (modernc.org/sqlite: pure Go, no CGO) in WAL mode, so several `plugit
// serve` processes (one per MCP client) can write while a viewer reads.
//
// Callers redact before recording: nothing here knows about secrets.
package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/swayam5342/plugit/internal/config"
)

// SchemaVersion is the call log's schema version (PRAGMA user_version).
const SchemaVersion = 1

// Caps on stored text, so one huge call can't bloat the log.
const (
	MaxArgsBytes    = 16 << 10
	MaxPreviewBytes = 2 << 10
	MaxErrorBytes   = 1 << 10
)

// Outcome is how a call ended.
type Outcome string

// Outcomes.
const (
	OK        Outcome = "ok"         // 2xx/3xx response
	HTTPError Outcome = "http_error" // the API answered with status >= 400
	Denied    Outcome = "denied"     // policy refused it (deny or confirm)
	Invalid   Outcome = "invalid"    // arguments failed validation
	Failed    Outcome = "error"      // build, network, timeout or internal error
)

// Call is one logged tool call. Every text field is already redacted.
type Call struct {
	ID       int64           `json:"id"`
	Time     time.Time       `json:"time"`
	API      string          `json:"api"`
	Toolset  string          `json:"toolset"`
	Tool     string          `json:"tool"` // the MCP tool name
	Op       string          `json:"op"`   // "METHOD /path"
	Outcome  Outcome         `json:"outcome"`
	Status   int             `json:"status,omitempty"` // HTTP status, 0 if none
	Duration time.Duration   `json:"duration_ns"`
	Bytes    int             `json:"bytes,omitempty"`   // response body bytes
	Args     json.RawMessage `json:"args,omitempty"`    // nil when log.args is none
	Preview  string          `json:"preview,omitempty"` // only with log.response_preview
	Error    string          `json:"error,omitempty"`   // short reason when not OK
}

// DB is an open call log.
type DB struct {
	sql *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS calls (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	ts          INTEGER NOT NULL,
	api         TEXT    NOT NULL,
	toolset     TEXT    NOT NULL,
	tool        TEXT    NOT NULL,
	op          TEXT    NOT NULL,
	outcome     TEXT    NOT NULL,
	status      INTEGER NOT NULL DEFAULT 0,
	duration_us INTEGER NOT NULL,
	bytes       INTEGER NOT NULL DEFAULT 0,
	args        TEXT,
	preview     TEXT,
	error       TEXT
);
CREATE INDEX IF NOT EXISTS calls_api_ts ON calls(api, ts);
`

// Open opens (creating if needed) the call log at path. The directory is
// owner-only and the file is created 0600 before SQLite touches it; SQLite
// gives its -wal and -shm files the same permissions.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := config.EnsureDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("call log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- plugit's own data file
	if err != nil {
		return nil, fmt.Errorf("opening call log: %w", err)
	}
	_ = f.Close()

	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening call log: %w", err)
	}
	db := &DB{sql: sqlDB}
	if err := db.migrate(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) migrate(ctx context.Context) error {
	var v int
	if err := db.sql.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("reading call log version: %w", err)
	}
	switch {
	case v > SchemaVersion:
		return fmt.Errorf("the call log was written by a newer plugit (schema %d, this build knows %d); upgrade plugit", v, SchemaVersion)
	case v == SchemaVersion:
		return nil
	}
	if _, err := db.sql.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating call log: %w", err)
	}
	if _, err := db.sql.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return fmt.Errorf("creating call log: %w", err)
	}
	return nil
}

// Close closes the log.
func (db *DB) Close() error { return db.sql.Close() }

// Insert stores one call and returns its id. Oversized fields are capped.
func (db *DB) Insert(ctx context.Context, c Call) (int64, error) {
	args := c.Args
	if len(args) > MaxArgsBytes {
		args = json.RawMessage(`{"truncated":true}`)
	}
	res, err := db.sql.ExecContext(ctx,
		`INSERT INTO calls (ts, api, toolset, tool, op, outcome, status, duration_us, bytes, args, preview, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Time.UnixMicro(), c.API, c.Toolset, c.Tool, c.Op, string(c.Outcome), c.Status,
		c.Duration.Microseconds(), c.Bytes, nullable(string(args)),
		nullable(capText(c.Preview, MaxPreviewBytes)), nullable(capText(c.Error, MaxErrorBytes)))
	if err != nil {
		return 0, fmt.Errorf("recording call: %w", err)
	}
	return res.LastInsertId()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// capText cuts s to at most n bytes on a rune boundary.
func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// Prune deletes an API's calls older than before.
func (db *DB) Prune(ctx context.Context, api string, before time.Time) (int64, error) {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM calls WHERE api = ? AND ts < ?`, api, before.UnixMicro())
	if err != nil {
		return 0, fmt.Errorf("pruning call log: %w", err)
	}
	return res.RowsAffected()
}

// Filter selects calls.
type Filter struct {
	API     string    // "" for every API
	AfterID int64     // only calls with a larger id (for following)
	Since   time.Time // only calls at or after this time
	Limit   int       // at most this many (default 1000)
	Newest  bool      // take the newest Limit calls instead of the oldest
}

// Fixed queries: the filter only ever supplies bound parameters. An empty
// API or a zero Since matches everything.
const (
	filterSQL = ` WHERE id > ?1 AND (?2 = '' OR api = ?2) AND ts >= ?3`
	listCols  = `SELECT id, ts, api, toolset, tool, op, outcome, status, duration_us, bytes,
	        COALESCE(args, ''), COALESCE(preview, ''), COALESCE(error, '') FROM calls`
	listOldestSQL = listCols + filterSQL + ` ORDER BY id ASC LIMIT ?4`
	listNewestSQL = listCols + filterSQL + ` ORDER BY id DESC LIMIT ?4`
	statsSQL      = `SELECT api, tool, outcome, duration_us, bytes, ts FROM calls` + filterSQL
)

func (f Filter) params() []any {
	since := int64(0)
	if !f.Since.IsZero() {
		since = f.Since.UnixMicro()
	}
	return []any{f.AfterID, f.API, since}
}

// List returns calls in id order (oldest first).
func (db *DB) List(ctx context.Context, f Filter) ([]Call, error) {
	query := listOldestSQL
	if f.Newest {
		query = listNewestSQL
	}
	rows, err := db.sql.QueryContext(ctx, query, append(f.params(), cmp.Or(f.Limit, 1000))...)
	if err != nil {
		return nil, fmt.Errorf("reading call log: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Call
	for rows.Next() {
		var c Call
		var ts, us int64
		var outcome, argText string
		if err := rows.Scan(&c.ID, &ts, &c.API, &c.Toolset, &c.Tool, &c.Op, &outcome, &c.Status, &us, &c.Bytes,
			&argText, &c.Preview, &c.Error); err != nil {
			return nil, fmt.Errorf("reading call log: %w", err)
		}
		c.Time, c.Duration, c.Outcome = time.UnixMicro(ts).UTC(), time.Duration(us)*time.Microsecond, Outcome(outcome)
		if argText != "" {
			c.Args = json.RawMessage(argText)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading call log: %w", err)
	}
	if f.Newest {
		slices.Reverse(out)
	}
	return out, nil
}

// ToolStats summarizes one tool's calls.
type ToolStats struct {
	API      string        `json:"api"`
	Tool     string        `json:"tool"`
	Calls    int           `json:"calls"`
	Errors   int           `json:"errors"` // every outcome but ok
	P50      time.Duration `json:"p50_ns"`
	P95      time.Duration `json:"p95_ns"`
	Bytes    int64         `json:"bytes"`
	Last     time.Time     `json:"last"`
	Outcomes map[Outcome]int
}

// Stats summarizes calls per API and tool, most-called first. f.Limit and
// f.AfterID are ignored.
func (db *DB) Stats(ctx context.Context, f Filter) ([]ToolStats, error) {
	f.AfterID = 0
	rows, err := db.sql.QueryContext(ctx, statsSQL, f.params()...)
	if err != nil {
		return nil, fmt.Errorf("reading call log: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type key struct{ api, tool string }
	acc := map[key]*ToolStats{}
	lat := map[key][]time.Duration{}
	for rows.Next() {
		var k key
		var outcome string
		var us, bytes, ts int64
		if err := rows.Scan(&k.api, &k.tool, &outcome, &us, &bytes, &ts); err != nil {
			return nil, fmt.Errorf("reading call log: %w", err)
		}
		s := acc[k]
		if s == nil {
			s = &ToolStats{API: k.api, Tool: k.tool, Outcomes: map[Outcome]int{}}
			acc[k] = s
		}
		s.Calls++
		s.Outcomes[Outcome(outcome)]++
		if Outcome(outcome) != OK {
			s.Errors++
		}
		s.Bytes += bytes
		if t := time.UnixMicro(ts).UTC(); t.After(s.Last) {
			s.Last = t
		}
		lat[k] = append(lat[k], time.Duration(us)*time.Microsecond)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading call log: %w", err)
	}
	out := make([]ToolStats, 0, len(acc))
	for k, s := range acc {
		d := lat[k]
		slices.Sort(d)
		s.P50, s.P95 = percentile(d, 50), percentile(d, 95)
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b ToolStats) int {
		return cmp.Or(cmp.Compare(b.Calls, a.Calls), cmp.Compare(a.API, b.API), cmp.Compare(a.Tool, b.Tool))
	})
	return out, nil
}

// percentile is the nearest-rank percentile of sorted values.
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p*len(sorted) + 99) / 100 // ceil(p/100 * n)
	return sorted[max(rank, 1)-1]
}

// Recorder writes calls in the background so recording never blocks or
// fails a tool call (SPEC §3.7 step 9). When its buffer is full, calls are
// dropped and counted.
type Recorder struct {
	db      *DB
	ch      chan Call
	onErr   func(error)
	dropped atomic.Int64
	mu      sync.RWMutex // guards closed against Record racing Close
	closed  bool
	done    chan struct{}
}

// NewRecorder starts a recorder with room for buffer pending calls. onErr
// (may be nil) hears about write failures.
func NewRecorder(db *DB, buffer int, onErr func(error)) *Recorder {
	r := &Recorder{db: db, ch: make(chan Call, max(buffer, 1)), onErr: onErr, done: make(chan struct{})}
	go r.loop()
	return r
}

func (r *Recorder) loop() {
	defer close(r.done)
	for c := range r.ch {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := r.db.Insert(ctx, c)
		cancel()
		if err != nil && r.onErr != nil {
			r.onErr(err)
		}
	}
}

// Record queues c without blocking. After Close it does nothing.
func (r *Recorder) Record(c Call) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return
	}
	select {
	case r.ch <- c:
	default:
		r.dropped.Add(1)
	}
}

// Dropped is how many calls were not recorded because the buffer was full.
func (r *Recorder) Dropped() int64 { return r.dropped.Load() }

// Close stops accepting calls and waits for queued ones to be written.
func (r *Recorder) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	close(r.ch)
	r.mu.Unlock()
	<-r.done
}

// ErrNoLog means there is no call log yet (nothing has been served).
var ErrNoLog = errors.New("no calls recorded yet")

// OpenExisting opens the log for reading, or returns ErrNoLog if it
// doesn't exist yet. It never creates the file.
func OpenExisting(ctx context.Context, path string) (*DB, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoLog
	}
	return Open(ctx, path)
}
