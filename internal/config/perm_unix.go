//go:build !windows

package config

import "os"

// restrictToOwner sets 0700 on directories and 0600 on files. An explicit
// chmod is used so the result does not depend on the process umask or on an
// existing file's mode.
func restrictToOwner(path string, isDir bool) error {
	mode := os.FileMode(0o600)
	if isDir {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}
