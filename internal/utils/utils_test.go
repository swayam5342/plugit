package utils

import (
	"go/parser"
	"go/token"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMustJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want string
	}{
		{"sorted map", map[string]any{"b": 1, "a": []any{"x", true}}, `{"a":["x",true],"b":1}`},
		{"nil", nil, `null`},
		{"unencodable falls back to {}", math.NaN(), `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(MustJSON(tc.in)); got != tc.want {
				t.Errorf("MustJSON = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSortedKeys(t *testing.T) {
	if got := SortedKeys(map[string]any{"b": 1, "a": 2, "c": nil}); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("SortedKeys = %v", got)
	}
	if got := SortedKeys(map[string]int{}); got == nil || len(got) != 0 {
		t.Errorf("SortedKeys(empty) = %#v, want an empty non-nil slice", got)
	}
}

func TestHasControl(t *testing.T) {
	for in, want := range map[string]bool{
		"plain text": false, "": false, "tab\there": true, "cr\rlf\n": true,
		"del\x7f": true, "nul\x00": true, "unicode é ✓": false, "bidi \u202e": false,
	} {
		if got := HasControl(in); got != want {
			t.Errorf("HasControl(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTrimBOM(t *testing.T) {
	for in, want := range map[string]string{
		"\xef\xbb\xbf{}":            "{}",
		"{}":                        "{}",
		"\xef\xbb\xbf\xef\xbb\xbfx": "\xef\xbb\xbfx",
		"":                          "",
	} {
		if got := string(TrimBOM([]byte(in))); got != want {
			t.Errorf("TrimBOM(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsBinary(t *testing.T) {
	for _, tc := range []struct {
		ct, body string
		want     bool
	}{
		{"application/json", `{"a":1}`, false},
		{"text/plain; charset=utf-8", "héllo", false},
		{"image/png", "PNG", true},
		{"application/octet-stream", "abc", true},
		{"application/pdf; x=y", "", true},
		{"text/plain", "nul\x00byte", true},
		{"text/plain", "\xff\xfe", true},
		{"", "plain", false},
	} {
		if got := IsBinary(tc.ct, []byte(tc.body)); got != tc.want {
			t.Errorf("IsBinary(%q, %q) = %v, want %v", tc.ct, tc.body, got, tc.want)
		}
	}
}

// TestLeafImports keeps utils a pure leaf: standard library only, so every
// package (pure and protocol alike) may import it.
func TestLeafImports(t *testing.T) {
	banned := []string{"github.com/", "net", "os", "time", "math/rand", "crypto/rand", "syscall"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, b := range banned {
				if path == b || strings.HasPrefix(path, b+"/") || (strings.HasSuffix(b, "/") && strings.HasPrefix(path, b)) {
					t.Errorf("%s imports %q: utils must stay a standard-library-only, side-effect-free leaf", name, path)
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no source files scanned")
	}
}
