package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/swayam5342/plugit/internal/model"
)

// MaxManifestBytes caps manifest.json (the same cap as the parse child's
// output).
const MaxManifestBytes = 256 << 20

// MaxSnapshotBytes caps spec.snapshot.json (the spec fetch limit).
const MaxSnapshotBytes = 20 << 20

// manifestFormat is the manifest.json layout version.
const manifestFormat = 1

// ErrCacheMiss means manifest.json is missing, unreadable or out of date.
// The cache is derived data, so the caller re-parses the snapshot.
var ErrCacheMiss = errors.New("manifest cache miss")

// CacheKey is everything a parsed manifest depends on. A cache whose key
// differs in any field is stale.
type CacheKey struct {
	SpecSHA256 string `json:"spec_sha256"` // sha256 of spec.snapshot.json
	Prefix     string `json:"prefix"`      // tool names depend on it
	Parser     string `json:"parser"`      // plugit build that parsed it
}

type manifestFile struct {
	Format   int             `json:"format"`
	Key      CacheKey        `json:"key"`
	Manifest *model.Manifest `json:"manifest"`
}

// Manifest is the path of an API's parsed-manifest cache.
func (p Paths) Manifest(name string) (string, error) {
	return p.apiFile(name, "manifest.json")
}

// WriteManifest writes the manifest cache atomically and owner-only.
func WriteManifest(path string, key CacheKey, m *model.Manifest) error {
	data, err := json.Marshal(manifestFile{Format: manifestFormat, Key: key, Manifest: m})
	if err != nil {
		return fmt.Errorf("encoding manifest cache: %w", err)
	}
	return WriteFileAtomic(path, data)
}

// ReadManifest returns the cached manifest if it was built for key. Every
// failure, including a missing file, wraps ErrCacheMiss.
func ReadManifest(path string, key CacheKey) (*model.Manifest, error) {
	data, err := readCapped(path, MaxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCacheMiss, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var f manifestFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrCacheMiss, path, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %s: trailing data", ErrCacheMiss, path)
	}
	switch {
	case f.Format != manifestFormat:
		return nil, fmt.Errorf("%w: %s: format %d", ErrCacheMiss, path, f.Format)
	case f.Key != key:
		return nil, fmt.Errorf("%w: %s is out of date", ErrCacheMiss, path)
	case f.Manifest == nil:
		return nil, fmt.Errorf("%w: %s has no manifest", ErrCacheMiss, path)
	}
	return f.Manifest, nil
}

// ReadSnapshot reads spec.snapshot.json and returns it with its sha256.
func ReadSnapshot(path string) (data []byte, sha256Hex string, err error) {
	data, err = readCapped(path, MaxSnapshotBytes)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}
