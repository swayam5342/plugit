package rest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRESTNoNetworkImports enforces T17 structurally (SPEC §5.3): a protocol
// package only Encodes/Decodes. It must never import a network dialer, the one
// HTTP client in internal/httpx, or internal/secrets — invoke owns the dialer,
// the single httpx call and credential resolution. depguard and forbidigo guard
// this at lint time; this test guards it in the suite so a bad import is caught
// even where the linter is not run.
//
// Note: this scans the package's own (non-test) source files for *direct*
// imports. net/http is allowed (the Protocol seam returns *http.Request); the
// raw "net" dialer package is not. When a grpc protocol lands behind the `grpc`
// build tag (SPEC §3.5 P8), it gets an explicit, tested exception here.
func TestBoundaryRESTNoNetworkImports(t *testing.T) {
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
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Clean(name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if why, bad := banned[path]; bad {
				t.Errorf("%s imports %q — %s (T17)", name, path, why)
			}
		}
	}
}
