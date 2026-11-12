//go:build !windows

package config

import (
	"os"
	"testing"
)

func assertOwnerOnlyFile(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("%s mode = %#o, want 0600", path, got)
	}
}

func assertOwnerOnlyDir(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("%s mode = %#o, want 0700", path, got)
	}
}
