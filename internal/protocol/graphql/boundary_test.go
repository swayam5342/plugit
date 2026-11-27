package graphql

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBoundaryNoNetworkImports enforces T17 structurally (SPEC §5.3) for the
// GraphQL protocol, as rest's boundary test does for REST: the package only
// ingests, Encodes and Decodes. It must never import the raw "net" dialer
// package, the one HTTP client in internal/httpx, or internal/secrets — invoke
// owns the dialer, the single httpx call and credential resolution. depguard
// and forbidigo guard this at lint time; this test guards it in the suite.
//
// It scans the package's own (non-test) source files for direct imports.
// net/http and net/url are allowed (the Protocol seam returns *http.Request).
func TestBoundaryNoNetworkImports(t *testing.T) {
	banned := map[string]string{
		"net": "a protocol must not dial; invoke owns the dialer in internal/httpx",
		"github.com/swayam5342/plugit/internal/httpx":   "a protocol must not call httpx; invoke makes the one call",
		"github.com/swayam5342/plugit/internal/secrets": "a protocol must not resolve a secret; invoke attaches credentials",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Clean(name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if why, bad := banned[path]; bad {
				t.Errorf("%s imports %q — %s (T17)", name, path, why)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no source files scanned")
	}
}
