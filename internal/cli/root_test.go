package cli

import (
	"bytes"
	"encoding/base64"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/tui"
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

func TestBuildInfoFill(t *testing.T) {
	mod := func(version string, settings ...debug.BuildSetting) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/swayam5342/plugit", Version: version}, Settings: settings}
	}
	tests := []struct {
		name string
		in   BuildInfo
		bi   *debug.BuildInfo
		want BuildInfo
	}{
		{
			name: "go install at a tag fills the version",
			bi:   mod("v0.2.0"),
			want: BuildInfo{Version: "v0.2.0"},
		},
		{
			name: "pseudo-version yields commit and date",
			bi:   mod("v0.2.1-0.20261117093015-abcdef123456"),
			want: BuildInfo{Version: "v0.2.1-0.20261117093015-abcdef123456", Commit: "abcdef1", Date: "2026-11-17T09:30:15Z"},
		},
		{
			name: "local checkout build uses vcs settings",
			bi: mod("v0.2.1-0.20261117093015-abcdef123456+dirty",
				debug.BuildSetting{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
				debug.BuildSetting{Key: "vcs.time", Value: "2026-11-17T10:00:00Z"}),
			want: BuildInfo{Version: "v0.2.1-0.20261117093015-abcdef123456+dirty", Commit: "0123456", Date: "2026-11-17T10:00:00Z"},
		},
		{
			name: "devel version is ignored",
			bi:   mod("(devel)"),
			want: BuildInfo{},
		},
		{
			name: "ldflags values win",
			in:   BuildInfo{Version: "0.2.0", Commit: "fedcba9", Date: "2026-11-12T00:00:00Z"},
			bi:   mod("v0.0.0-20260101000000-abcdef123456", debug.BuildSetting{Key: "vcs.revision", Value: "0123456789abcdef"}),
			want: BuildInfo{Version: "0.2.0", Commit: "fedcba9", Date: "2026-11-12T00:00:00Z"},
		},
		{
			name: "no build info",
			bi:   nil,
			want: BuildInfo{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Fill(tc.bi); got != tc.want {
				t.Errorf("Fill = %+v, want %+v", got, tc.want)
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

func TestNoColorFlag(t *testing.T) {
	t.Cleanup(func() { tui.SetNoColor(false) })
	var out, errOut bytes.Buffer
	if code := Main([]string{"--no-color", "version"}, strings.NewReader(""), &out, &errOut, BuildInfo{}); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !tui.NoColor() {
		t.Error("--no-color not applied")
	}
}
