package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	tests := []struct {
		name string
		info BuildInfo
		want string
	}{
		{
			name: "injected values",
			info: BuildInfo{Version: "v1.2.3", Commit: "abc1234", Date: "2026-11-12T00:00:00Z"},
			want: "plugit v1.2.3 (commit abc1234, built 2026-11-12T00:00:00Z)\n",
		},
		{
			name: "empty values fall back to dev defaults",
			info: BuildInfo{},
			want: "plugit dev (commit none, built unknown)\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			cmd := NewRootCmd(tc.info)
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"version"})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if got := out.String(); got != tc.want {
				t.Errorf("version output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRootHelpListsVersion(t *testing.T) {
	var out bytes.Buffer
	cmd := NewRootCmd(BuildInfo{})
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "version") {
		t.Errorf("help output does not list the version command:\n%s", out.String())
	}
}

func TestUnknownCommandFails(t *testing.T) {
	var out bytes.Buffer
	cmd := NewRootCmd(BuildInfo{})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"no-such-command"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for an unknown command")
	}
}
