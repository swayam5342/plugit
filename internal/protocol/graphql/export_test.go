package graphql

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// Test helpers shared with the external graphql_test package (github_test.go),
// which runs ingest through the isolate child and so cannot be package graphql
// (isolate imports this package).

// RaceEnabled reports whether the test binary was built with -race.
const RaceEnabled = raceEnabled

// MaxWarnings is the manifest warning cap.
const MaxWarnings = maxWarnings

// SpecsDir holds the pinned public specs and SOURCES.md.
const SpecsDir = "../../../testdata/specs"

// GitHubSDLFile is the pinned GitHub GraphQL SDL (gzip).
const GitHubSDLFile = "github.graphql.gz"

// GitHubSDLSHA256 is the SHA-256 of the uncompressed pinned GitHub SDL, as
// recorded in SOURCES.md.
const GitHubSDLSHA256 = "4dea7bd74e69637bd55795157eef5bfd89af3a32a6f05e8ac69004f223896415"

// GitHubSDL returns the uncompressed pinned GitHub SDL, failing the test if
// its SHA-256 is not GitHubSDLSHA256 or is not listed in SOURCES.md.
func GitHubSDL(t testing.TB) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(SpecsDir, GitHubSDLFile))
	if err != nil {
		t.Fatalf("reading %s: %v", GitHubSDLFile, err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: %v", GitHubSDLFile, err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("%s: %v", GitHubSDLFile, err)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != GitHubSDLSHA256 {
		t.Fatalf("%s: SHA-256 %s, want %s", GitHubSDLFile, got, GitHubSDLSHA256)
	}
	sources, err := os.ReadFile(filepath.Join(SpecsDir, "SOURCES.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(sources, []byte("`"+GitHubSDLFile+"`")) || !bytes.Contains(sources, []byte(got)) {
		t.Fatalf("%s and its SHA-256 %s are not listed in SOURCES.md", GitHubSDLFile, got)
	}
	return data
}
