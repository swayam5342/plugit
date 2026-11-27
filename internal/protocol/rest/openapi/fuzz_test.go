package openapi

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/pb33f/go-yaml"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/schema"
)

func addSeeds(f *testing.F) {
	f.Add([]byte(petstore30))
	f.Add([]byte(swagger2))
	f.Add([]byte(refChain(3)))
	f.Add(petsCollection(f))
	for _, dir := range []string{hostileDir, specsDir} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".yaml") || strings.HasSuffix(e.Name(), ".json") {
				if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil && len(b) < 64<<10 {
					f.Add(b)
				}
			}
		}
	}
}

// FuzzParse: arbitrary bytes must never panic the parser (Parse turns
// panics into ErrParserPanic, which this treats as a failure), and whatever
// parses must yield valid, clean tools.
func FuzzParse(f *testing.F) {
	addSeeds(f)
	// Regression inputs found by fuzzing, formerly the corpus files in
	// testdata/fuzz/FuzzParse (named by their hash below). Each literal is
	// the file's "go test fuzz v1" value verbatim.
	// 203f9c7d2d851b36
	f.Add([]byte("openapi: 3.0\no: e\n#\npaths:\n e:\n  t:\n      requestBody:\n       content:\n        a: {$ref: '/comprnents/schemas/Tree'}\n      responses: {'200': {description: ok}}\ncomponents:\n  schemas:\n    Tree:\n      type: object\n      properties:\n        children: {type: array, items: {$ref: '#/components/sc99hemas/Tree'}}\n        other: {$ref: '#/components/schemas/Forest'}\n    Forest:"))
	// 2e86b8e3bffd3ebb
	f.Add([]byte("swagger: \"2.0\"\npaths:\n  /:\n    put:\n      'arameters:\n        - in: body\n          schema: {$ref: '#User'}\nUser: \n properties:\n  kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkme:"))
	// 3f7289e623353678
	f.Add([]byte("swagger: \"2.0\"\npaths:\n  /\x7f:\n    put:"))
	// a01e7be48f5454ca
	f.Add([]byte("openapi: 3.0\no: {e}#\npaths:\n e:\n  t:\n      requestBody:\n       content:\n        n:\n        a: {$ref: '/components/schemas/Trema: {$e'}\n      responses: {'200': {description: ok}}\ncomponents:\n  schemas:\n    Tree:\n      type: object\n      properties:\n        children: {type: Qrray, items: {$ref: '#/coWponents/schemas/Tree'}}\n        other: {$ref: '#/components/schemas/Forest'}\n    Forest:\n      type: object\n      properties:\n        trees: {type: array, items: {$ref: '#/components/schemas/Tree'}}"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256<<10 {
			t.Skip()
		}
		type result struct {
			m   *model.Manifest
			err error
		}
		done := make(chan result, 1)
		go func() {
			m, err := Parse(data, Options{})
			done <- result{m, err}
		}()
		var r result
		select {
		case r = <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("Parse did not finish within 5s (hang or super-linear blowup)")
		}
		m, err := r.m, r.err
		if errors.Is(err, ErrParserPanic) {
			t.Fatalf("parser panicked: %v", err)
		}
		if err != nil {
			return
		}
		seen := map[string]bool{}
		for _, tl := range m.Tools {
			if !validToolName(tl.Name) || seen[strings.ToLower(tl.Name)] {
				t.Fatalf("invalid or duplicate tool name %q", tl.Name)
			}
			seen[strings.ToLower(tl.Name)] = true
			assertClean(t, tl.Description)
			p := schema.Prepare(tl, schema.Options{})
			var is map[string]any
			if err := json.Unmarshal(schema.InputSchema(p), &is); err != nil {
				t.Fatalf("%s: invalid input schema: %v", tl.Key, err)
			}
			for _, pr := range p.Params {
				assertClean(t, pr.Desc)
			}
		}
	})
}

func assertClean(t *testing.T, s string) {
	t.Helper()
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) || unicode.IsControl(r) {
			t.Fatalf("text contains invisible/control character U+%04X: %q", r, s)
		}
	}
}

// FuzzRefCheck: the $ref pre-pass must terminate without panicking on any
// YAML tree, including cyclic pointers and aliases.
func FuzzRefCheck(f *testing.F) {
	addSeeds(f)
	f.Add([]byte("a: &x {b: *x}\nc: {$ref: '#/a/b/b/b'}\n"))
	f.Add([]byte("$ref: '#'\n"))
	f.Add([]byte("x: {$ref: '#/x'}\n"))
	f.Add([]byte("- {$ref: '#/0'}\n- {$ref: '#/1'}\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		var root yaml.Node
		if err := yaml.Unmarshal(data, &root); err != nil || root.Kind == 0 {
			return
		}
		_, _ = checkRefs(&root)
	})
}
