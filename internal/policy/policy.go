// Package policy decides whether a tool call may run (SPEC §3.7 step 2,
// T5). It is pure.
//
// The API's mode is a ceiling. A per-tool override can always tighten a
// decision (confirm, deny) but can never lift a tool above what the mode
// allows: under read-only, writes are denied even with "allow"; under
// confirm-writes they still need confirmation.
package policy

import (
	"errors"
	"fmt"

	"github.com/swayam5342/plugit/internal/model"
)

// ErrInvalid is returned for an unknown mode or override.
var ErrInvalid = errors.New("invalid policy value")

// Mode is an API-wide policy.
type Mode string

// Policy modes.
const (
	ReadOnly      Mode = "read-only"      // only read-only tools run (the default)
	ConfirmWrites Mode = "confirm-writes" // write and destructive tools need confirmation
	Full          Mode = "full"           // everything runs unless overridden
)

// Override is a per-tool policy.
type Override string

// Per-tool overrides.
const (
	None            Override = ""
	OverrideAllow   Override = "allow"
	OverrideConfirm Override = "confirm"
	OverrideDeny    Override = "deny"
)

// Decision is the outcome of Check.
type Decision int

// Decisions, from least to most restrictive.
const (
	Allow Decision = iota + 1
	Confirm
	Deny
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Confirm:
		return "confirm"
	case Deny:
		return "deny"
	}
	return fmt.Sprintf("Decision(%d)", int(d))
}

// Result is a decision and, unless it is Allow, why.
type Result struct {
	Decision Decision
	Reason   string
}

// ParseMode parses a mode name.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ReadOnly, ConfirmWrites, Full:
		return m, nil
	}
	return "", fmt.Errorf("%w: mode %q (use read-only, confirm-writes or full)", ErrInvalid, s)
}

// ParseOverride parses a per-tool override ("" means none).
func ParseOverride(s string) (Override, error) {
	switch o := Override(s); o {
	case None, OverrideAllow, OverrideConfirm, OverrideDeny:
		return o, nil
	}
	return "", fmt.Errorf("%w: policy %q (use allow, confirm or deny)", ErrInvalid, s)
}

// Check decides a call to a tool with the given safety. Unknown modes and
// overrides fail closed.
func Check(safety model.Safety, mode Mode, override Override) Result {
	ceiling, reason := modeCeiling(safety, mode)
	if ceiling == Deny {
		return Result{Deny, reason}
	}
	switch override {
	case None, OverrideAllow:
		// "allow" can't exceed the ceiling, so it changes nothing here.
	case OverrideConfirm:
		if ceiling < Confirm {
			ceiling, reason = Confirm, "this tool is set to require confirmation"
		}
	case OverrideDeny:
		return Result{Deny, "this tool is set to deny; change it with `plugit edit`"}
	default:
		return Result{Deny, fmt.Sprintf("unknown per-tool policy %q", override)}
	}
	if ceiling == Allow {
		return Result{Decision: Allow}
	}
	return Result{ceiling, reason}
}

func modeCeiling(safety model.Safety, mode Mode) (Decision, string) {
	switch mode {
	case ReadOnly:
		if safety == model.ReadOnly {
			return Allow, ""
		}
		return Deny, fmt.Sprintf("this is a %s tool and the API is in read-only mode; switch policy.mode to confirm-writes or full to use it", safety)
	case ConfirmWrites:
		if safety == model.ReadOnly {
			return Allow, ""
		}
		return Confirm, fmt.Sprintf("this is a %s tool and the API is in confirm-writes mode", safety)
	case Full:
		return Allow, ""
	}
	return Deny, fmt.Sprintf("unknown policy mode %q", mode)
}

// Lists reports whether a tool should be offered to the model at all under
// mode: it is listed when the mode could allow or confirm it.
func Lists(safety model.Safety, mode Mode) bool {
	d, _ := modeCeiling(safety, mode)
	return d != Deny
}
