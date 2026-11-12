package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/isolate"
)

func lintDeps(out *bytes.Buffer) lintSettings {
	return lintSettings{out: out, stderr: &bytes.Buffer{}, parse: isolate.Parse}
}

func TestLintClean(t *testing.T) {
	p := setupAPI(t, "https://pets.example.test")
	out := &bytes.Buffer{}
	s := lintDeps(out)
	if err := lintAPI(t.Context(), p, "pets", s); err != nil {
		t.Fatalf("lint: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "pets") || !strings.Contains(out.String(), "0 errors") {
		t.Errorf("output:\n%s", out)
	}
}

// Credentials over plain http to a remote host are an error: exit code 1.
func TestLintErrors(t *testing.T) {
	p := setupAPI(t, "http://pets.example.test")
	out := &bytes.Buffer{}
	err := lintAPI(t.Context(), p, "pets", lintDeps(out))
	var ec exitCodeError
	if !errors.As(err, &ec) || ec.code != 1 {
		t.Fatalf("err = %v, want exit code 1", err)
	}
	if !strings.Contains(out.String(), "L011") || !strings.Contains(out.String(), "error") {
		t.Errorf("output:\n%s", out)
	}
}

func TestLintJSON(t *testing.T) {
	p := setupAPI(t, "http://pets.example.test")
	out := &bytes.Buffer{}
	s := lintDeps(out)
	s.asJSON = true
	_ = lintAPI(t.Context(), p, "pets", s)
	var got struct {
		API      string `json:"api"`
		Findings []struct {
			Rule     string `json:"rule"`
			Severity string `json:"severity"`
		} `json:"findings"`
		Summary struct {
			Errors int `json:"errors"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.API != "pets" || got.Summary.Errors == 0 || len(got.Findings) == 0 || got.Findings[0].Severity != "error" {
		t.Errorf("json = %+v", got)
	}
}

func TestLintToolset(t *testing.T) {
	p := setupAPI(t, "https://pets.example.test")
	out := &bytes.Buffer{}
	s := lintDeps(out)
	s.toolset = "nope"
	if err := lintAPI(t.Context(), p, "pets", s); err == nil || !strings.Contains(err.Error(), "toolset") {
		t.Errorf("unknown toolset: %v", err)
	}
	s.toolset = "denied"
	if err := lintAPI(t.Context(), p, "pets", s); err != nil {
		t.Errorf("denied: %v", err)
	}
}

// Usage errors exit with 2 (SPEC §4), so CI can tell them from findings.
func TestLintUsageExitCode(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Main([]string{"lint"}, strings.NewReader(""), &out, &errOut, BuildInfo{}); code != 2 {
		t.Errorf("exit code = %d, want 2 (%s)", code, errOut.String())
	}
	if code := Main([]string{"lint", "pets", "--bogus"}, strings.NewReader(""), &out, &errOut, BuildInfo{}); code != 2 {
		t.Errorf("unknown flag exit code = %d, want 2", code)
	}
}
