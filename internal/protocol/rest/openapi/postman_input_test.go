package openapi

import (
	"os"
	"path/filepath"
	"testing"
)

// petsCollection reads the shared Postman fixture (the postman package's
// tests use it too): FuzzParse seeds the OpenAPI parser with it, and the
// OpenAPI parser must reject it.
func petsCollection(tb testing.TB) []byte {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join(specsDir, "postman", "pets.postman.json"))
	if err != nil {
		tb.Fatalf("reading the pets collection: %v", err)
	}
	return b
}

func TestOpenAPIParseRejectsPostman(t *testing.T) {
	// The Postman sniff moved to the ingest dispatcher, so the OpenAPI parser
	// no longer special-cases a collection: it just fails to build a document.
	if _, err := Parse(petsCollection(t), Options{}); err == nil {
		t.Error("Parse on a Postman collection should error now that the sniff moved to ingest")
	}
}
