package policy

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/swayam5342/plugit/internal/model"
)

func TestCheckMatrix(t *testing.T) {
	const A, C, D = Allow, Confirm, Deny
	// want[mode][safety] for overrides: none, allow, confirm, deny.
	want := map[Mode]map[model.Safety][4]Decision{
		ReadOnly: {
			model.ReadOnly:    {A, A, C, D},
			model.Write:       {D, D, D, D},
			model.Destructive: {D, D, D, D},
		},
		ConfirmWrites: {
			model.ReadOnly:    {A, A, C, D},
			model.Write:       {C, C, C, D}, // allow can't lift a write above the mode's ceiling
			model.Destructive: {C, C, C, D},
		},
		Full: {
			model.ReadOnly:    {A, A, C, D},
			model.Write:       {A, A, C, D},
			model.Destructive: {A, A, C, D},
		},
	}
	overrides := [4]Override{None, OverrideAllow, OverrideConfirm, OverrideDeny}
	for mode, bySafety := range want {
		for safety, decisions := range bySafety {
			for i, ov := range overrides {
				got := Check(safety, mode, ov)
				if got.Decision != decisions[i] {
					t.Errorf("Check(%v, %s, %q) = %v, want %v", safety, mode, ov, got.Decision, decisions[i])
				}
				if got.Decision != Allow && got.Reason == "" {
					t.Errorf("Check(%v, %s, %q): %v without a reason", safety, mode, ov, got.Decision)
				}
			}
		}
	}
}

func TestDenyReasonsExplainTheFix(t *testing.T) {
	r := Check(model.Write, ReadOnly, None)
	if !strings.Contains(r.Reason, "read-only") {
		t.Errorf("reason = %q, want it to mention read-only mode", r.Reason)
	}
	r = Check(model.ReadOnly, Full, OverrideDeny)
	if !strings.Contains(r.Reason, "deny") {
		t.Errorf("reason = %q, want it to mention the per-tool deny", r.Reason)
	}
}

func TestUnknownModeFailsClosed(t *testing.T) {
	if got := Check(model.ReadOnly, Mode("yolo"), None); got.Decision != Deny {
		t.Errorf("unknown mode: %v, want Deny", got.Decision)
	}
	if got := Check(model.ReadOnly, Full, Override("maybe")); got.Decision != Deny {
		t.Errorf("unknown override: %v, want Deny", got.Decision)
	}
}

func TestParse(t *testing.T) {
	for _, s := range []string{"read-only", "confirm-writes", "full"} {
		m, err := ParseMode(s)
		if err != nil || string(m) != s {
			t.Errorf("ParseMode(%q) = %q, %v", s, m, err)
		}
	}
	if _, err := ParseMode("readonly"); !errors.Is(err, ErrInvalid) {
		t.Errorf("ParseMode(readonly) err = %v, want ErrInvalid", err)
	}
	for _, s := range []string{"", "allow", "confirm", "deny"} {
		o, err := ParseOverride(s)
		if err != nil || string(o) != s {
			t.Errorf("ParseOverride(%q) = %q, %v", s, o, err)
		}
	}
	if _, err := ParseOverride("block"); !errors.Is(err, ErrInvalid) {
		t.Errorf("ParseOverride(block) err = %v, want ErrInvalid", err)
	}
}

func TestAllowsListing(t *testing.T) {
	// plugit run lists only what the mode could ever allow or confirm.
	if Lists(model.Write, ReadOnly) || Lists(model.Destructive, ReadOnly) {
		t.Error("read-only mode must not list write tools")
	}
	if !Lists(model.ReadOnly, ReadOnly) || !Lists(model.Write, ConfirmWrites) || !Lists(model.Destructive, Full) {
		t.Error("tools the mode can allow or confirm must be listed")
	}
}

func TestParseRate(t *testing.T) {
	good := map[string]Rate{
		"60/min": {N: 60, Per: time.Minute},
		"5/sec":  {N: 5, Per: time.Second},
		"1/hour": {N: 1, Per: time.Hour},
	}
	for in, want := range good {
		if got, err := ParseRate(in); err != nil || got != want {
			t.Errorf("ParseRate(%q) = %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0/min", "00/sec", "-1/min", "10/day", "10 per min", "1e3/sec", "1/min/x", "9999999999/sec"} {
		if _, err := ParseRate(bad); err == nil {
			t.Errorf("ParseRate(%q) accepted", bad)
		}
	}
	if s := (Rate{N: 60, Per: time.Minute}).String(); s != "60/min" {
		t.Errorf("String = %q", s)
	}
}

// The limiter allows a burst of N, then refills N per period evenly.
func TestLimiter(t *testing.T) {
	clock := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	l := NewLimiter(Rate{N: 3, Per: time.Minute}, now)
	for i := range 3 {
		if ok, _ := l.Allow(); !ok {
			t.Fatalf("call %d refused within the burst", i+1)
		}
	}
	ok, wait := l.Allow()
	if ok || wait != 20*time.Second {
		t.Fatalf("4th call: ok=%v wait=%v, want refused for 20s", ok, wait)
	}
	clock = clock.Add(19 * time.Second)
	if ok, _ := l.Allow(); ok {
		t.Error("allowed before a token refilled")
	}
	clock = clock.Add(time.Second)
	if ok, _ := l.Allow(); !ok {
		t.Error("refused after a token refilled")
	}
	clock = clock.Add(time.Hour)
	n := 0
	for {
		if ok, _ := l.Allow(); !ok {
			break
		}
		n++
	}
	if n != 3 {
		t.Errorf("after a long idle, %d calls allowed; the burst is 3", n)
	}
	var nilLimiter *Limiter
	if ok, _ := nilLimiter.Allow(); !ok {
		t.Error("a nil limiter must allow everything")
	}
}
