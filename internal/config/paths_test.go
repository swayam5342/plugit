package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootDirOverride(t *testing.T) {
	dir := t.TempDir()
	got, err := RootDir(dir)
	if err != nil {
		t.Fatalf("RootDir: %v", err)
	}
	if got != dir {
		t.Errorf("RootDir(%q) = %q, want the override unchanged", dir, got)
	}
}

func TestRootDirRelativeOverrideIsMadeAbsolute(t *testing.T) {
	got, err := RootDir("relative-dir")
	if err != nil {
		t.Fatalf("RootDir: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("RootDir(relative) = %q, want an absolute path", got)
	}
	if filepath.Base(got) != "relative-dir" {
		t.Errorf("RootDir(relative) = %q, want it to end in relative-dir", got)
	}
}

func TestRootDirDefaultUsesUserConfigDir(t *testing.T) {
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no user config dir on this system: %v", err)
	}
	got, err := RootDir("")
	if err != nil {
		t.Fatalf("RootDir: %v", err)
	}
	if want := filepath.Join(base, "plugit"); got != want {
		t.Errorf("RootDir(\"\") = %q, want %q", got, want)
	}
}

func TestPathsLayout(t *testing.T) {
	root := filepath.Join("x", "plugit")
	p := NewPaths(root)

	if got, want := p.GlobalConfig(), filepath.Join(root, "config.yaml"); got != want {
		t.Errorf("GlobalConfig = %q, want %q", got, want)
	}
	if got, want := p.DB(), filepath.Join(root, "data", "plugit.db"); got != want {
		t.Errorf("DB = %q, want %q", got, want)
	}

	cfg, err := p.APIConfig("github")
	if err != nil {
		t.Fatalf("APIConfig: %v", err)
	}
	if want := filepath.Join(root, "apis", "github", "plugit.yaml"); cfg != want {
		t.Errorf("APIConfig = %q, want %q", cfg, want)
	}
	snap, err := p.SpecSnapshot("github")
	if err != nil {
		t.Fatalf("SpecSnapshot: %v", err)
	}
	if want := filepath.Join(root, "apis", "github", "spec.snapshot.json"); snap != want {
		t.Errorf("SpecSnapshot = %q, want %q", snap, want)
	}
}

func TestValidateAPIName(t *testing.T) {
	valid := []string{"github", "a", "pet-store", "stripe_v2", "x1", strings.Repeat("a", 64)}
	for _, name := range valid {
		t.Run("valid/"+name, func(t *testing.T) {
			if err := ValidateAPIName(name); err != nil {
				t.Errorf("ValidateAPIName(%q) = %v, want nil", name, err)
			}
		})
	}

	invalid := []string{
		"", ".", "..", "../etc", `..\Windows`, "a/b", `a\b`, "C:", "c:foo",
		"GitHub", "-leading", "_leading", "has space", "dot.name", "nul\x00",
		strings.Repeat("a", 65), "con", "NUL", "aux", "com1", "lpt9",
	}
	for _, name := range invalid {
		t.Run("invalid/"+name, func(t *testing.T) {
			err := ValidateAPIName(name)
			if !errors.Is(err, ErrInvalidAPIName) {
				t.Errorf("ValidateAPIName(%q) = %v, want ErrInvalidAPIName", name, err)
			}
		})
	}
}

func TestAPIPathsRejectInvalidNames(t *testing.T) {
	p := NewPaths(t.TempDir())
	if _, err := p.APIDir("../escape"); !errors.Is(err, ErrInvalidAPIName) {
		t.Errorf("APIDir(../escape) = %v, want ErrInvalidAPIName", err)
	}
	if _, err := p.APIConfig("../escape"); !errors.Is(err, ErrInvalidAPIName) {
		t.Errorf("APIConfig(../escape) = %v, want ErrInvalidAPIName", err)
	}
	if _, err := p.SpecSnapshot("../escape"); !errors.Is(err, ErrInvalidAPIName) {
		t.Errorf("SpecSnapshot(../escape) = %v, want ErrInvalidAPIName", err)
	}
}
