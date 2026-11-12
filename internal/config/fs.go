package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// EnsureDir creates dir and any missing parents. Every directory it creates,
// and dir itself even if it already existed, is restricted to the current
// user (0700, or an owner-only protected DACL on Windows). Existing parents
// are left untouched.
func EnsureDir(dir string) error {
	dir = filepath.Clean(dir)

	// Collect missing components from dir upwards, then create them top-down
	// so each new directory is secured before anything is placed inside it.
	var missing []string
	for d := dir; ; {
		fi, err := os.Stat(d)
		if err == nil {
			if !fi.IsDir() {
				return fmt.Errorf("ensuring dir %s: %s exists and is not a directory", dir, d)
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("ensuring dir %s: %w", dir, err)
		}
		missing = append(missing, d)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}

	for _, d := range slices.Backward(missing) {
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("creating dir %s: %w", d, err)
		}
		if err := restrictToOwner(d, true); err != nil {
			return fmt.Errorf("securing dir %s: %w", d, err)
		}
	}
	if len(missing) == 0 {
		if err := restrictToOwner(dir, true); err != nil {
			return fmt.Errorf("securing dir %s: %w", dir, err)
		}
	}
	return nil
}

// WriteFileAtomic writes data to path so that readers see either the old
// file or the complete new one, never a partial write. The result is readable
// and writable only by the current user. The parent directory must exist.
func WriteFileAtomic(path string, data []byte) (err error) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	// CreateTemp opens with O_EXCL and mode 0600.
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("writing %s: creating temp file: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	// Secure before writing so the data never sits under a wider ACL.
	if err = restrictToOwner(tmpName, false); err != nil {
		return fmt.Errorf("writing %s: securing temp file: %w", path, err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("writing %s: sync: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: close: %w", path, err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("writing %s: rename: %w", path, err)
	}
	return nil
}
