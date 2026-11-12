// Package config owns PlugIt's on-disk layout: platform config paths, API
// directory names and owner-only, atomic file writes.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrInvalidAPIName is returned when an API name is not safe to use as a
// directory name.
var ErrInvalidAPIName = errors.New("invalid API name")

// apiNameRe keeps API names portable and free of path syntax: lowercase
// letters, digits, '-' and '_', starting with a letter or digit, at most 64
// characters.
var apiNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// windowsReserved are device names Windows refuses as file or directory
// names, regardless of extension.
var windowsReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidateAPIName reports whether name can be used as an API name. Names
// become directory names under apis/, so anything that could escape that
// directory or misbehave on some OS is rejected.
func ValidateAPIName(name string) error {
	if !apiNameRe.MatchString(name) || windowsReserved[strings.ToLower(name)] {
		return fmt.Errorf("%w %q: use 1-64 lowercase letters, digits, '-' or '_', starting with a letter or digit", ErrInvalidAPIName, name)
	}
	return nil
}

// RootDir returns the PlugIt config root. A non-empty override (the
// --config-dir flag) wins and is made absolute. Otherwise it is
// os.UserConfigDir()/plugit.
func RootDir(override string) (string, error) {
	if override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("resolving config dir %q: %w", override, err)
		}
		return abs, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating user config dir: %w", err)
	}
	return filepath.Join(base, "plugit"), nil
}

// Paths computes file locations under a config root. It does no I/O.
type Paths struct {
	root string
}

// NewPaths returns the layout rooted at root.
func NewPaths(root string) Paths {
	return Paths{root: root}
}

// Root is the config root directory.
func (p Paths) Root() string { return p.root }

// GlobalConfig is the path of the global defaults file.
func (p Paths) GlobalConfig() string { return filepath.Join(p.root, "config.yaml") }

// DB is the path of the sqlite call log.
func (p Paths) DB() string { return filepath.Join(p.root, "data", "plugit.db") }

// APIDir is the directory holding one API's config and spec snapshot.
func (p Paths) APIDir(name string) (string, error) {
	if err := ValidateAPIName(name); err != nil {
		return "", err
	}
	return filepath.Join(p.root, "apis", name), nil
}

// APIConfig is the path of an API's user-owned plugit.yaml.
func (p Paths) APIConfig(name string) (string, error) {
	return p.apiFile(name, "plugit.yaml")
}

// SpecSnapshot is the path of an API's normalized spec snapshot.
func (p Paths) SpecSnapshot(name string) (string, error) {
	return p.apiFile(name, "spec.snapshot.json")
}

func (p Paths) apiFile(name, file string) (string, error) {
	dir, err := p.APIDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, file), nil
}
