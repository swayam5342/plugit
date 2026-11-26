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

const manifestFormat = 1

// ErrCacheMiss means manifest.json is missing, unreadable or out of date.
var ErrCacheMiss = errors.New("manifest cache miss")

// CacheKey is everything a parsed manifest depends on. A cache whose key
// differs in any field is stale.
type CacheKey struct {
	SpecSHA256  string `json:"spec_sha256"`
	Prefix      string `json:"prefix"`
	Parser      string `json:"parser"`
	UnsafePaths bool   `json:"unsafe_paths,omitempty"`

	// GraphQL selection settings (zero for every other protocol, so a REST
	// key is unchanged from before they existed).
	SelectionDepth int `json:"selection_depth,omitempty"`
	MaxFields      int `json:"max_fields,omitempty"`
}

// CacheKey is the manifest cache key for c's spec (sha256 specSHA256)
// parsed by plugit build parser. It holds every config value that changes
// the generated tools; for GraphQL that includes the effective selection
// settings, so an absent graphql: section and the explicit defaults match.
func (c *API) CacheKey(specSHA256, parser string) CacheKey {
	k := CacheKey{SpecSHA256: specSHA256, Prefix: c.Prefix, Parser: parser, UnsafePaths: c.Network.AllowInsecure}
	if model.Protocol(c.Protocol) == model.GraphQL {
		g := c.GraphQLSettings()
		k.SelectionDepth, k.MaxFields = g.SelectionDepth, g.MaxFields
	}
	return k
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
