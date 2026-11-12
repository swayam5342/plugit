package cli

import (
	"bytes"
	"encoding/base64"
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

func TestMainDispatchesParseChild(t *testing.T) {
	spec := "openapi: 3.0.3\ninfo: {title: T, version: '1'}\npaths:\n  /x:\n    get:\n      operationId: getX\n      responses: {'200': {description: ok}}\n"
	req := `{"spec":"` + base64.StdEncoding.EncodeToString([]byte(spec)) + `"}`
	var out, errb bytes.Buffer
	code := Main([]string{"__parse"}, strings.NewReader(req), &out, &errb, BuildInfo{})
	if code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, errb.String())
	}
	if !strings.Contains(out.String(), `"name":"getX"`) {
		t.Errorf("stdout = %q, want a manifest with getX", out.String())
	}
}

func TestMainExitCodes(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Main([]string{"version"}, strings.NewReader(""), &out, &errb, BuildInfo{Version: "v9"}); code != 0 || !strings.Contains(out.String(), "v9") {
		t.Errorf("version: code=%d out=%q", code, out.String())
	}
	out.Reset()
	if code := Main([]string{"no-such-command"}, strings.NewReader(""), &out, &errb, BuildInfo{}); code != 1 {
		t.Errorf("unknown command exit code = %d, want 1", code)
	}
	if !strings.HasPrefix(errb.String(), "plugit: ") {
		t.Errorf("stderr = %q, want a plugit: error line", errb.String())
	}
	if out.Len() != 0 {
		t.Errorf("errors must not go to stdout, got %q", out.String())
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
