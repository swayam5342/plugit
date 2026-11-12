package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

func testManifest() *model.Manifest {
	return &model.Manifest{
		Title:       "Pets",
		Version:     "1",
		SpecVersion: "3.0.3",
		Servers:     []string{"https://pets.example.test"},
		Tools: []model.Tool{{
			Key: "GET /pets/{id}", ID: "abcd1234", Name: "getPet", Description: "Get a pet",
			Method: "GET", Path: "/pets/{id}", Safety: model.ReadOnly,
			Params: []model.Param{{Name: "id", UpstreamName: "id", In: model.InPath, Required: true, Schema: []byte(`{"type":"string"}`)}},
		}},
		Warnings: []string{"1 TRACE operation skipped"},
	}
}

func TestManifestCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	key := CacheKey{SpecSHA256: strings.Repeat("a", 64), Prefix: "pets", Parser: "v1.2.3"}
	want := testManifest()
	if err := WriteManifest(path, key, want); err != nil {
		t.Fatal(err)
	}
	assertOwnerOnlyFile(t, path)
	got, err := ReadManifest(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
	}
}

// Anything that would make the cache disagree with a fresh parse is a miss:
// a different spec, a different prefix (names change), or a different
// plugit build (the parser may have changed).
func TestManifestCacheMisses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	key := CacheKey{SpecSHA256: strings.Repeat("a", 64), Prefix: "pets", Parser: "v1.2.3"}
	if err := WriteManifest(path, key, testManifest()); err != nil {
		t.Fatal(err)
	}
	for name, k := range map[string]CacheKey{
		"spec changed":   {SpecSHA256: strings.Repeat("b", 64), Prefix: "pets", Parser: "v1.2.3"},
		"prefix changed": {SpecSHA256: key.SpecSHA256, Prefix: "", Parser: "v1.2.3"},
		"plugit changed": {SpecSHA256: key.SpecSHA256, Prefix: "pets", Parser: "v1.2.4"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadManifest(path, k); !errors.Is(err, ErrCacheMiss) {
				t.Errorf("err = %v, want ErrCacheMiss", err)
			}
		})
	}

	broken := map[string]string{
		"corrupt":         "{not json",
		"old format":      `{"format":0,"key":{},"manifest":{"tools":[]}}`,
		"no manifest":     `{"format":1,"key":{"spec_sha256":"` + key.SpecSHA256 + `","prefix":"pets","parser":"v1.2.3"}}`,
		"unknown enum":    `{"format":1,"key":{"spec_sha256":"` + key.SpecSHA256 + `","prefix":"pets","parser":"v1.2.3"},"manifest":{"tools":[{"safety":"harmless"}]}}`,
		"trailing data":   `{"format":1,"key":{},"manifest":{"tools":[]}} {}`,
		"top-level array": `[]`,
	}
	for name, content := range broken {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name+".json")
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadManifest(p, key); !errors.Is(err, ErrCacheMiss) {
				t.Errorf("err = %v, want ErrCacheMiss", err)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		if _, err := ReadManifest(filepath.Join(dir, "absent.json"), key); !errors.Is(err, ErrCacheMiss) {
			t.Errorf("err = %v, want ErrCacheMiss", err)
		}
	})
}

func TestReadSnapshot(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "spec.snapshot.json")
	data := []byte(`{"openapi":"3.0.3"}`)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, sum, err := ReadSnapshot(p)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)
	if string(got) != string(data) || sum != hex.EncodeToString(want[:]) {
		t.Errorf("got %q %s", got, sum)
	}

	if _, _, err := ReadSnapshot(filepath.Join(dir, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing snapshot: err = %v", err)
	}
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, make([]byte, MaxSnapshotBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadSnapshot(big); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("oversized snapshot: err = %v", err)
	}
}

func TestManifestPath(t *testing.T) {
	p := NewPaths(filepath.FromSlash("/root"))
	got, err := p.Manifest("pets")
	if err != nil || got != filepath.Join(filepath.FromSlash("/root"), "apis", "pets", "manifest.json") {
		t.Errorf("Manifest = %q, %v", got, err)
	}
	if _, err := p.Manifest("../x"); !errors.Is(err, ErrInvalidAPIName) {
		t.Errorf("err = %v", err)
	}
}
