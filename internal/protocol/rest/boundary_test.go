package rest

import (
	"go/parser"
	"go/token"
	"io/fs"
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
// Note: this scans the (non-test) source files of rest and of every package
// below it (openapi, postman, restutil) for *direct* imports. net/http is
// allowed (the Protocol seam returns *http.Request); the raw "net" dialer
// package is not. When a grpc protocol lands behind the `grpc` build tag
// (SPEC §3.5 P8), it gets an explicit, tested exception here.
func TestBoundaryRESTNoNetworkImports(t *testing.T) {
	banned := map[string]string{
		"net": "a protocol must not dial; invoke owns the dialer in internal/httpx",
		"github.com/swayam5342/plugit/internal/httpx":   "a protocol must not call httpx; invoke makes the one call",
		"github.com/swayam5342/plugit/internal/secrets": "a protocol must not resolve a secret; invoke attaches credentials",
	}

	fset := token.NewFileSet()
	dirs := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, filepath.Clean(path), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		dirs[filepath.ToSlash(filepath.Dir(path))] = true
		for _, imp := range file.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if why, bad := banned[p]; bad {
				t.Errorf("%s imports %q — %s (T17)", path, p, why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk package tree: %v", err)
	}
	for _, want := range []string{".", "openapi", "postman", "restutil"} {
		if !dirs[want] {
			t.Errorf("package %s was not scanned", want)
		}
	}
}
