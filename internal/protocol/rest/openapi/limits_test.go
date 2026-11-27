package openapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

func TestParseCircularSchemaIsDepthCapped(t *testing.T) {
	src := `
openapi: 3.0.3
info: {title: T, version: '1'}
paths:
  /nodes:
    post:
      requestBody:
        content:
          application/json:
            schema: {$ref: '#/components/schemas/Node'}
      responses: {'200': {description: ok}}
components:
  schemas:
    Node:
      type: object
      properties:
        value: {type: string}
        next: {$ref: '#/components/schemas/Node'}
`
	tl := tool(t, parse(t, src, Options{}), "POST /nodes")
	if !hasIssue(tl, model.IssueSchemaDegraded, "depth") {
		t.Errorf("missing depth issue: %+v", tl.Issues)
	}
	depth := jsonDepth(t, tl.HTTP.Body.Schema)
	if depth > 2*(MaxSchemaDepth+1)+1 {
		t.Errorf("schema JSON depth %d exceeds the cap", depth)
	}
}

func jsonDepth(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	var walk func(any) int
	walk = func(v any) int {
		best := 0
		switch x := v.(type) {
		case map[string]any:
			for _, c := range x {
				best = max(best, walk(c))
			}
			return best + 1
		case []any:
			for _, c := range x {
				best = max(best, walk(c))
			}
			return best + 1
		}
		return 0
	}
	return walk(v)
}

func TestParseWideSchemaHitsNodeBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString("openapi: 3.0.3\ninfo: {title: T, version: '1'}\npaths:\n  /x:\n    post:\n      requestBody:\n        content:\n          application/json:\n            schema: {$ref: '#/components/schemas/L0'}\n      responses: {'200': {description: ok}}\ncomponents:\n  schemas:\n")
	for i := range 8 {
		fmt.Fprintf(&b, "    L%d:\n      type: object\n      properties:\n", i)
		for j := range 6 {
			fmt.Fprintf(&b, "        p%d: {$ref: '#/components/schemas/L%d'}\n", j, i+1)
		}
	}
	b.WriteString("    L8: {type: string}\n")
	tl := tool(t, parse(t, b.String(), Options{}), "POST /x")
	if !hasIssue(tl, model.IssueSchemaDegraded, "too large") {
		t.Errorf("missing node budget issue: %+v", tl.Issues)
	}
	if len(tl.HTTP.Body.Schema) > 2_000_000 {
		t.Errorf("schema is %d bytes; budget did not bound it", len(tl.HTTP.Body.Schema))
	}
}

func TestParseNeverFetchesExternalRefs(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("type: string\ndescription: REMOTE-MARKER\n"))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.yaml")
	if err := os.WriteFile(secret, []byte("type: string\ndescription: FILE-MARKER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	src := `
openapi: 3.0.3
info: {title: T, version: '1'}
paths:
  /x:
    get:
      parameters:
        - {name: remote, in: query, schema: {$ref: '` + srv.URL + `/evil.yaml'}}
        - {name: rel, in: query, schema: {$ref: './secret.yaml'}}
        - {name: abs, in: query, schema: {$ref: 'file://` + filepath.ToSlash(secret) + `'}}
        - {name: up, in: query, schema: {$ref: '../../Windows/win.ini'}}
      responses: {'200': {description: ok}}
`
	m := parse(t, src, Options{})
	if hits.Load() != 0 {
		t.Errorf("remote $ref was fetched %d times", hits.Load())
	}
	out, _ := json.Marshal(m)
	for _, marker := range []string{"REMOTE-MARKER", "FILE-MARKER"} {
		if strings.Contains(string(out), marker) {
			t.Errorf("output contains %s: external content was resolved", marker)
		}
	}
	tl := tool(t, m, "GET /x")
	for _, name := range []string{"remote", "rel", "abs", "up"} {
		assertJSON(t, name, param(t, tl, name).Schema, `{}`)
	}
	if !hasIssue(tl, model.IssueSchemaDegraded, "external $ref") {
		t.Errorf("missing external ref issue: %+v", tl.Issues)
	}
	if !containsWarning(m, "external $ref") {
		t.Errorf("missing manifest warning about external refs: %v", m.Warnings)
	}
}

func refChain(n int) string {
	var b strings.Builder
	b.WriteString("openapi: 3.0.3\ninfo: {title: T, version: '1'}\npaths:\n  /x:\n    get:\n      parameters:\n        - {name: a, in: query, schema: {$ref: '#/components/schemas/S0'}}\n      responses: {'200': {description: ok}}\ncomponents:\n  schemas:\n")
	for i := range n {
		fmt.Fprintf(&b, "    S%d: {$ref: '#/components/schemas/S%d'}\n", i, i+1)
	}
	fmt.Fprintf(&b, "    S%d: {type: string}\n", n)
	return b.String()
}

func TestParseRefChainLimit(t *testing.T) {
	m := parse(t, refChain(MaxRefDepth-1), Options{})
	assertJSON(t, "chain within limit", param(t, tool(t, m, "GET /x"), "a").Schema, `{"type":"string"}`)

	if _, err := Parse([]byte(refChain(MaxRefDepth+5)), Options{}); !errors.Is(err, ErrRefTooDeep) {
		t.Errorf("deep chain: err = %v, want ErrRefTooDeep", err)
	}
}

func TestParsePureRefCycle(t *testing.T) {
	src := `
openapi: 3.0.3
info: {title: T, version: '1'}
paths:
  /x:
    get:
      parameters:
        - {name: a, in: query, schema: {$ref: '#/components/schemas/A'}}
      responses: {'200': {description: ok}}
components:
  schemas:
    A: {$ref: '#/components/schemas/B'}
    B: {$ref: '#/components/schemas/A'}
`
	if _, err := Parse([]byte(src), Options{}); !errors.Is(err, ErrRefCycle) {
		t.Errorf("err = %v, want ErrRefCycle", err)
	}
}

func TestParseAliasBomb(t *testing.T) {
	src := `openapi: 3.0.3
info: {title: t, version: '1'}
x-a: &a ["lol","lol","lol","lol","lol","lol","lol","lol","lol"]
x-b: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a]
x-c: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b]
x-d: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c]
x-e: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d]
x-f: &f [*e,*e,*e,*e,*e,*e,*e,*e,*e]
x-g: &g [*f,*f,*f,*f,*f,*f,*f,*f,*f]
x-h: &h [*g,*g,*g,*g,*g,*g,*g,*g,*g]
x-i: &i [*h,*h,*h,*h,*h,*h,*h,*h,*h]
paths:
  /x:
    get:
      parameters:
        - {name: a, in: query, schema: {type: string, enum: *i}}
      responses: {'200': {description: ok}}
`
	_, err := Parse([]byte(src), Options{})
	if err == nil || !strings.Contains(err.Error(), "alias") {
		t.Fatalf("err = %v, want an alias-expansion error", err)
	}
}

func TestParseSkipsInvalidPaths(t *testing.T) {
	bad := map[string]string{
		"control":    `"/a\x7fb"`,
		"newline":    `"/a\nb"`,
		"space":      `"/a b"`,
		"query":      `"/a?admin=1"`,
		"fragment":   `"/a#frag"`,
		"bidi":       `"/a\u202eb"`,
		"dotdot":     `"/api/../admin"`,
		"dot":        `"/a/./b"`,
		"dotdot end": `"/a/.."`,
		"no slash":   `"a/b"`,
		"backslash":  `"/a\\..\\b"`,
	}
	for name, path := range bad {
		t.Run(name, func(t *testing.T) {
			src := "openapi: 3.0.3\ninfo: {title: t, version: '1'}\npaths:\n  " + path + ":\n    get:\n      responses: {}\n  /ok:\n    get:\n      responses: {}\n"
			m := parse(t, src, Options{})
			if len(m.Tools) != 1 || m.Tools[0].HTTP.Path != "/ok" {
				t.Errorf("tools = %+v, want only /ok", m.Tools)
			}
			if !containsWarning(m, "skipped") {
				t.Errorf("missing warning: %v", m.Warnings)
			}
			for _, w := range m.Warnings {
				assertClean(t, w)
			}
		})
	}
	ok := "openapi: 3.0.3\ninfo: {title: t, version: '1'}\npaths:\n  /a/{b}/c.d~e-f_g%20h:\n    get:\n      responses: {}\n  /:\n    get:\n      responses: {}\n"
	if m := parse(t, ok, Options{}); len(m.Tools) != 2 {
		t.Errorf("valid paths rejected: %v", m.Warnings)
	}
}

func TestParseNeutralizesExternalRefObjects(t *testing.T) {
	src := `
openapi: 3.0.3
info: {title: t, version: '1'}
paths:
  /x:
    get:
      operationId: getX
      parameters:
        - name: q
          in: query
          schema: {type: string}
          examples:
            e: {$ref: 'other.yaml#/e'}
      callbacks:
        cb: {$ref: 'other.yaml#/cb'}
      responses:
        '200':
          description: ok
          headers:
            X-A: {$ref: 'other.yaml#/h'}
          links:
            l: {$ref: 'other.yaml#/l'}
`
	m := parse(t, src, Options{})
	tl := tool(t, m, "GET /x")
	if tl.Name != "getX" || len(tl.Params) != 1 {
		t.Errorf("tool = %+v", tl)
	}
	if !containsWarning(m, "4 external $ref(s) ignored") {
		t.Errorf("warnings = %v, want 4 external refs reported", m.Warnings)
	}
}

func TestParseRejectsNonObjectMediaTypes(t *testing.T) {
	for name, content := range map[string]string{
		"null next to ref": "          n:\n          a: {$ref: x}\n",
		"null":             "          application/json:\n",
		"ref only":         "          a: {$ref: '/comprnents/schemas/Tree'}\n",
		"local ref":        "          application/json: {$ref: '#/components/schemas/X'}\n",
		"scalar":           "          application/json: 5\n",
		"list":             "          application/json: [a]\n",
	} {
		t.Run(name, func(t *testing.T) {
			src := "openapi: 3.0.3\ninfo: {title: t, version: '1'}\npaths:\n  /e:\n    post:\n      requestBody:\n        content:\n" + content + "      responses: {}\n"
			if _, err := Parse([]byte(src), Options{}); !errors.Is(err, ErrInvalidSpec) {
				t.Errorf("err = %v, want ErrInvalidSpec", err)
			}
		})
	}
}

func TestParseMediaTypeCheckIgnoresNamesAndFreeForm(t *testing.T) {
	src := `
openapi: 3.0.3
info: {title: t, version: '1'}
paths:
  /e:
    post:
      requestBody:
        content:
          application/json:
            schema: {$ref: '#/components/schemas/content'}
            example: {content: {a: null, b: 1}}
      responses:
        '200':
          description: ok
          content:
            application/json: {}
components:
  schemas:
    content:
      type: object
      properties:
        content: {type: string}
      x-content: {content: {n: null}}
`
	tl := tool(t, parse(t, src, Options{}), "POST /e")
	if tl.HTTP.Body == nil {
		t.Fatal("body missing")
	}
}

// libopenapi v0.42.5 reads past the end of a Swagger 2.0 path item whose
// last value is a scalar containing "$ref", and panics on its own
// goroutine (v2/path_item.go:83; found by FuzzParse, seed 2e86b8e3bffd3ebb).
// Swagger 2.0 path items have no string fields but $ref, so such a value
// is invalid: reject it before libopenapi sees it.
func TestParseRejectsScalarV2PathItemValues(t *testing.T) {
	for name, src := range map[string]string{
		"fuzz seed": "swagger: \"2.0\"\npaths:\n  /:\n    put:\n      'arameters:\n        - in: body\n          schema: {$ref: '#User'}\nUser: \n properties:\n  kkkkme:",
		"plain":     "swagger: '2.0'\ninfo: {title: t, version: '1'}\npaths:\n  /x:\n    get: 'see $ref docs'\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(src), Options{}); !errors.Is(err, ErrInvalidSpec) {
				t.Errorf("err = %v, want ErrInvalidSpec", err)
			}
		})
	}
	ok := "swagger: '2.0'\ninfo: {title: t, version: '1'}\npaths:\n  /x:\n    get:\n      description: uses $ref internally\n      responses: {'200': {description: ok}}\n"
	if m := parse(t, ok, Options{}); len(m.Tools) != 1 {
		t.Errorf("valid spec rejected: %v", m.Warnings)
	}
}
