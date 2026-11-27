package restutil

import (
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

// The parsers' tests (openapi, postman) cover the Builder end to end; these
// pin the shared pieces' own contracts.

func TestCollectorZeroValueDedupes(t *testing.T) {
	var c Collector
	if c.Issues() != nil {
		t.Fatal("a fresh Collector has issues")
	}
	c.Issue(model.IssueUnsupported, "field %q", "a")
	c.Issue(model.IssueUnsupported, "field %q", "a")
	c.Issue(model.IssueSchemaDegraded, "field %q", "a")
	c.Hidden("summary", "clean")
	c.Hidden("summary", "x\u200by")
	got := c.Issues()
	if len(got) != 3 || got[0].Kind != model.IssueUnsupported || got[1].Kind != model.IssueSchemaDegraded || got[2].Kind != model.IssueHiddenText {
		t.Errorf("issues = %+v, want unsupported, degraded, hidden-text in order", got)
	}
}

func TestBuilderValidPath(t *testing.T) {
	for _, tc := range []struct {
		path         string
		strict, open bool // accepted with AllowUnsafePaths off, on
	}{
		{"/pets/{id}", true, true},
		{"pets", false, false},
		{"/a?b", false, false},
		{"/a%0d%0ab", false, false},
		{"/v1/../admin", false, true},
		{"/v1/%2e%2e/x", false, true},
		{"/my folder", false, true},
	} {
		for _, allow := range []bool{false, true} {
			b := &Builder{Opts: Options{AllowUnsafePaths: allow}}
			want := tc.strict
			if allow {
				want = tc.open
			}
			if got := b.ValidPath(tc.path); got != want {
				t.Errorf("ValidPath(%q) with AllowUnsafePaths=%v = %v, want %v", tc.path, allow, got, want)
			}
			// A refused path is reported with one warning; an accepted one with none.
			if warned := len(b.M.Warnings) == 1; warned != !want {
				t.Errorf("ValidPath(%q) with AllowUnsafePaths=%v: warnings %q, want a warning only when refused", tc.path, allow, b.M.Warnings)
			}
		}
	}
}

func TestValidCookieName(t *testing.T) {
	for name, want := range map[string]bool{
		"session": true, "a.b-c_d": true, "": false, "a b": false, "a;b": false, "a=b": false,
		strings.Repeat("a", 128): true, strings.Repeat("a", 129): false,
	} {
		if got := ValidCookieName(name); got != want {
			t.Errorf("ValidCookieName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("#/a"); got != "#/a" {
		t.Errorf("short = %q", got)
	}
	long := strings.Repeat("x", 130)
	if got := Truncate(long); got != strings.Repeat("x", 120)+"…" {
		t.Errorf("long = %q", got)
	}
}
