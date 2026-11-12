package policy

import (
	"errors"
	"strings"
	"testing"

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
