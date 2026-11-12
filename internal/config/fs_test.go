package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugit.yaml")

	if err := WriteFileAtomic(path, []byte("version: 1\n")); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "version: 1\n" {
		t.Errorf("content = %q, want %q", got, "version: 1\n")
	}
	assertOwnerOnlyFile(t, path)
	assertNoTempFiles(t, dir, "plugit.yaml")
}

func TestWriteFileAtomicReplacesAndTightensExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugit.yaml")
	if err := os.WriteFile(path, []byte("old content that is longer"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteFileAtomic(path, []byte("new")); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
	assertOwnerOnlyFile(t, path)
	assertNoTempFiles(t, dir, "plugit.yaml")
}

func TestWriteFileAtomicMissingDirFailsCleanly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing", "plugit.yaml")

	if err := WriteFileAtomic(path, []byte("x")); err == nil {
		t.Fatal("expected an error when the parent directory does not exist")
	}
	assertNoTempFiles(t, dir, "")
}

func TestWriteFileAtomicTargetIsDirectoryFailsCleanly(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "occupied")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	// Put a file inside so the directory is non-empty and cannot be replaced.
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteFileAtomic(target, []byte("x")); err == nil {
		t.Fatal("expected an error when the target is a directory")
	}
	assertNoTempFiles(t, dir, "occupied")
}

func TestEnsureDirCreatesNestedOwnerOnlyDirs(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "plugit")
	leaf := filepath.Join(root, "apis", "github")

	if err := EnsureDir(leaf); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	for _, d := range []string{root, filepath.Join(root, "apis"), leaf} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatalf("Stat(%s): %v", d, err)
		}
		if !fi.IsDir() {
			t.Fatalf("%s is not a directory", d)
		}
		assertOwnerOnlyDir(t, d)
	}
}

func TestEnsureDirTightensExistingLeaf(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugit")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	assertOwnerOnlyDir(t, dir)
}

func TestEnsureDirIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	for i := range 2 {
		if err := EnsureDir(dir); err != nil {
			t.Fatalf("EnsureDir call %d: %v", i+1, err)
		}
	}
}

func TestEnsureDirRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(path); err == nil {
		t.Fatal("expected an error when the path is an existing file")
	}
}

// assertNoTempFiles fails if dir contains anything other than keep (when non-empty).
func assertNoTempFiles(t *testing.T, dir, keep string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != keep {
			t.Errorf("unexpected leftover entry %q in %s", e.Name(), dir)
		}
	}
}
