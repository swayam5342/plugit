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
		Endpoints:   []string{"https://pets.example.test"},
		Tools: []model.Tool{{
			Key: "GET /pets/{id}", ID: "abcd1234", Name: "getPet", Description: "Get a pet",
			HTTP: &model.HTTPOp{Method: "GET", Path: "/pets/{id}"}, Safety: model.ReadOnly,
			Params: []model.Param{{Name: "id", Bind: model.Binding{Upstream: "id", In: model.InPath}, Required: true, Schema: []byte(`{"type":"string"}`)}},
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
		"switch changed": {SpecSHA256: key.SpecSHA256, Prefix: "pets", Parser: "v1.2.3", UnsafePaths: true},
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

// The GraphQL selection settings change the generated documents, so they
// are part of the cache key; an absent section and the explicit defaults
// give the same key.
func TestSelectionSettingsBustCacheKey(t *testing.T) {
	const sum = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	parse := func(extra string) *API {
		t.Helper()
		c, err := ParseAPI([]byte(gqlCacheConfig + extra))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	base := parse("").CacheKey(sum, "v1")
	if base.SelectionDepth != 2 || base.MaxFields != 100 {
		t.Errorf("graphql key = %+v, want the effective defaults", base)
	}
	if k := parse("graphql: {selection_depth: 2, max_fields: 100}\n").CacheKey(sum, "v1"); k != base {
		t.Errorf("explicit defaults key %+v != absent-section key %+v", k, base)
	}
	deeper := parse("graphql: {selection_depth: 3}\n").CacheKey(sum, "v1")
	if deeper == base {
		t.Fatal("selection_depth did not change the cache key")
	}
	if k := parse("graphql: {max_fields: 99}\n").CacheKey(sum, "v1"); k == base {
		t.Error("max_fields did not change the cache key")
	}

	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := WriteManifest(path, base, testManifest()); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(path, base); err != nil {
		t.Fatalf("same key: %v", err)
	}
	if _, err := ReadManifest(path, deeper); !errors.Is(err, ErrCacheMiss) {
		t.Errorf("changed selection_depth: err = %v, want ErrCacheMiss", err)
	}
}

const gqlCacheConfig = `version: 1
name: gh
protocol: graphql
source: {url: https://example.test/schema.graphql}
endpoint: https://api.example.test/graphql
prefix: gh
toolsets:
  default:
    tools:
      "query user": {}
`

// A REST key is what it was before the GraphQL fields existed, so a
// manifest.json written by an earlier build still matches (no spurious
// cache miss).
func TestRESTCacheKeyUnchanged(t *testing.T) {
	sum := strings.Repeat("a", 64)
	c, err := ParseAPI([]byte(strings.Replace(minimalConfig, "toolsets:", "prefix: pets\nnetwork: {allow_insecure: true}\ntoolsets:", 1)))
	if err != nil {
		t.Fatal(err)
	}
	key := c.CacheKey(sum, "v1.2.3")
	want := CacheKey{SpecSHA256: sum, Prefix: "pets", Parser: "v1.2.3", UnsafePaths: true}
	if key != want {
		t.Fatalf("REST key = %+v, want %+v", key, want)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	old := `{"format":1,"key":{"spec_sha256":"` + sum + `","prefix":"pets","parser":"v1.2.3","unsafe_paths":true},"manifest":{"tools":[]}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(path, key); err != nil {
		t.Errorf("pre-GraphQL REST manifest: %v", err)
	}
	if err := WriteManifest(path, key, testManifest()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "selection_depth") || strings.Contains(string(data), "max_fields") {
		t.Errorf("REST manifest key carries GraphQL fields: %s", data)
	}
}
