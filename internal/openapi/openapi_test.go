package openapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

func parse(t *testing.T, src string, opts Options) *model.Manifest {
	t.Helper()
	m, err := Parse([]byte(src), opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return m
}

func tool(t *testing.T, m *model.Manifest, key model.OpKey) *model.Tool {
	t.Helper()
	for i := range m.Tools {
		if m.Tools[i].Key == key {
			return &m.Tools[i]
		}
	}
	keys := make([]string, len(m.Tools))
	for i, tl := range m.Tools {
		keys[i] = string(tl.Key)
	}
	t.Fatalf("tool %q not found; have %v", key, keys)
	return nil
}

func param(t *testing.T, tl *model.Tool, name string) *model.Param {
	t.Helper()
	for i := range tl.Params {
		if tl.Params[i].UpstreamName == name {
			return &tl.Params[i]
		}
	}
	t.Fatalf("%s: param %q not found", tl.Key, name)
	return nil
}

func assertJSON(t *testing.T, what string, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: invalid JSON %s: %v", what, got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: bad want JSON: %v", what, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got %s\nwant %s", what, got, want)
	}
}

func hasIssue(tl *model.Tool, kind model.IssueKind, substr string) bool {
	for _, is := range tl.Issues {
		if is.Kind == kind && strings.Contains(is.Detail, substr) {
			return true
		}
	}
	return false
}

const petstore30 = `
openapi: 3.0.3
info: {title: Petstore, version: 1.2.3}
servers:
  - url: https://{env}.example.test/v1
    variables:
      env: {default: api}
paths:
  /pets/{petId}:
    parameters:
      - {name: petId, in: path, required: true, description: The pet id, schema: {type: string}}
      - {name: verbose, in: query, schema: {type: boolean}}
    get:
      operationId: getPet
      summary: Get a pet
      tags: [pets]
      parameters:
        - {name: verbose, in: query, description: overridden, schema: {type: integer}}
        - {name: X-Trace, in: header, schema: {type: string}}
      responses: {'200': {description: ok}}
    delete:
      deprecated: true
      responses: {'204': {description: gone}}
  /pets:
    get:
      description: List all pets.
      parameters:
        - name: limit
          in: query
          schema: {type: integer, minimum: 1, maximum: 100, default: 20}
        - name: status
          in: query
          schema: {type: string, nullable: true, enum: [available, sold]}
      responses: {'200': {description: ok}}
    post:
      operationId: createPet
      requestBody:
        required: true
        content:
          application/json; charset=utf-8:
            schema: {$ref: '#/components/schemas/NewPet'}
      responses: {'201': {description: created}}
components:
  schemas:
    NewPet:
      type: object
      required: [name, id]
      properties:
        id: {type: integer, readOnly: true}
        name: {type: string, description: "The pet's <b>name</b>"}
        tags:
          type: array
          items: {type: string}
`

func TestParse30Basics(t *testing.T) {
	m := parse(t, petstore30, Options{})

	if m.Title != "Petstore" || m.Version != "1.2.3" || m.SpecVersion != "3.0.3" {
		t.Errorf("manifest header = %q %q %q", m.Title, m.Version, m.SpecVersion)
	}
	if !reflect.DeepEqual(m.Servers, []string{"https://api.example.test/v1"}) {
		t.Errorf("Servers = %v", m.Servers)
	}

	// Sorted by OpKey.
	var keys []model.OpKey
	for _, tl := range m.Tools {
		keys = append(keys, tl.Key)
	}
	want := []model.OpKey{"DELETE /pets/{petId}", "GET /pets", "GET /pets/{petId}", "POST /pets"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}

	get := tool(t, m, "GET /pets/{petId}")
	if get.Name != "getPet" || get.Description != "Get a pet" || get.Method != http.MethodGet || get.Path != "/pets/{petId}" {
		t.Errorf("getPet = %+v", get)
	}
	if get.Safety != model.ReadOnly || !reflect.DeepEqual(get.Tags, []string{"pets"}) || get.ID == "" {
		t.Errorf("getPet safety/tags/id = %v %v %q", get.Safety, get.Tags, get.ID)
	}
	pid := param(t, get, "petId")
	if pid.In != model.InPath || !pid.Required || pid.Desc != "The pet id" || pid.Name != "petId" {
		t.Errorf("petId = %+v", pid)
	}
	assertJSON(t, "petId schema", pid.Schema, `{"type":"string"}`)
	// Operation-level parameter overrides the path-level one with the same name and location.
	verbose := param(t, get, "verbose")
	assertJSON(t, "verbose schema", verbose.Schema, `{"type":"integer"}`)
	if verbose.Desc != "overridden" {
		t.Errorf("verbose desc = %q", verbose.Desc)
	}
	if param(t, get, "X-Trace").In != model.InHeader {
		t.Error("X-Trace should be a header param")
	}
	if len(get.Params) != 3 {
		t.Errorf("getPet has %d params, want 3", len(get.Params))
	}

	del := tool(t, m, "DELETE /pets/{petId}")
	if del.Name != "delete_pets_petId" || !del.Deprecated || del.Safety != model.Destructive {
		t.Errorf("delete = name %q deprecated %v safety %v", del.Name, del.Deprecated, del.Safety)
	}
	if del.Description != "DELETE /pets/{petId}" {
		t.Errorf("delete description = %q", del.Description)
	}
	// Path-level params are inherited.
	param(t, del, "petId")

	list := tool(t, m, "GET /pets")
	if list.Description != "List all pets." {
		t.Errorf("list description = %q", list.Description)
	}
	assertJSON(t, "limit", param(t, list, "limit").Schema, `{"type":"integer","minimum":1,"maximum":100,"default":20}`)
	assertJSON(t, "status (3.0 nullable)", param(t, list, "status").Schema, `{"type":["string","null"],"enum":["available","sold"]}`)
}

func TestParse30RequestBody(t *testing.T) {
	m := parse(t, petstore30, Options{})
	post := tool(t, m, "POST /pets")
	if post.Safety != model.Write {
		t.Errorf("safety = %v", post.Safety)
	}
	if post.Body == nil {
		t.Fatal("Body is nil")
	}
	if post.Body.ContentType != "application/json" || !post.Body.Required {
		t.Errorf("body = %q required=%v", post.Body.ContentType, post.Body.Required)
	}
	// readOnly "id" is dropped from the request schema and from required.
	assertJSON(t, "body", post.Body.Schema, `{
		"type":"object",
		"required":["name"],
		"properties":{
			"name":{"type":"string","description":"The pet's name"},
			"tags":{"type":"array","items":{"type":"string"}}
		}}`)
}

func TestParsePrefixAndDescriptionCap(t *testing.T) {
	m := parse(t, petstore30, Options{Prefix: "pets", MaxDescription: 5})
	get := tool(t, m, "GET /pets/{petId}")
	if get.Name != "pets_getPet" {
		t.Errorf("name = %q, want pets_getPet", get.Name)
	}
	if get.Description != "Get…" {
		t.Errorf("description = %q, want capped", get.Description)
	}
}

func TestParseDeterministic(t *testing.T) {
	a, err := json.Marshal(parse(t, petstore30, Options{}))
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		b, _ := json.Marshal(parse(t, petstore30, Options{}))
		if string(a) != string(b) {
			t.Fatalf("non-deterministic output:\n%s\n%s", a, b)
		}
	}
}

func TestParse31(t *testing.T) {
	src := `
openapi: 3.1.0
info: {title: T, version: '1'}
paths:
  /things:
    get:
      operationId: list
      parameters:
        - name: q
          in: query
          schema: {type: [string, "null"], exclusiveMinimum: 3}
        - name: n
          in: query
          content:
            application/json:
              schema: {type: integer, const: 4}
      responses: {'200': {description: ok}}
`
	m := parse(t, src, Options{})
	tl := tool(t, m, "GET /things")
	assertJSON(t, "q", param(t, tl, "q").Schema, `{"type":["string","null"],"exclusiveMinimum":3}`)
	assertJSON(t, "n (content)", param(t, tl, "n").Schema, `{"type":"integer","const":4}`)
	if m.SpecVersion != "3.1.0" {
		t.Errorf("SpecVersion = %q", m.SpecVersion)
	}
}

func TestParse30ExclusiveBoolean(t *testing.T) {
	src := `
openapi: 3.0.0
info: {title: T, version: '1'}
paths:
  /x:
    get:
      parameters:
        - {name: a, in: query, schema: {type: number, minimum: 0, exclusiveMinimum: true, maximum: 9}}
      responses: {'200': {description: ok}}
`
	tl := tool(t, parse(t, src, Options{}), "GET /x")
	assertJSON(t, "a", param(t, tl, "a").Schema, `{"type":"number","exclusiveMinimum":0,"maximum":9}`)
}

func TestParseUnsupportedFeatures(t *testing.T) {
	src := `
openapi: 3.0.3
info: {title: T, version: '1'}
paths:
  /upload:
    post:
      parameters:
        - {name: session, in: cookie, schema: {type: string}}
        - {name: q, in: query, schema: {type: string}}
      requestBody:
        content:
          multipart/form-data:
            schema:
              type: object
              required: [file]
              properties:
                file: {type: string, format: binary}
      responses: {'200': {description: ok}}
  /echo:
    trace:
      responses: {'200': {description: ok}}
    get:
      responses: {'200': {description: ok}}
`
	m := parse(t, src, Options{})
	up := tool(t, m, "POST /upload")
	if up.Body != nil {
		t.Error("a body whose required field is a file should not be modelled")
	}
	if !hasIssue(up, model.IssueUnsupported, "file") {
		t.Errorf("missing file-field issue: %+v", up.Issues)
	}
	// Cookie parameters are supported (SPEC M6 design note).
	if hasIssue(up, model.IssueUnsupported, "cookie") {
		t.Errorf("cookie still flagged unsupported: %+v", up.Issues)
	}
	if p := param(t, up, "session"); p.In != model.InCookie {
		t.Errorf("session = %+v", p)
	}

	for _, tl := range m.Tools {
		if tl.Method == http.MethodTrace {
			t.Errorf("TRACE operation was not skipped: %s", tl.Key)
		}
	}
	if !containsWarning(m, "TRACE /echo") {
		t.Errorf("missing TRACE warning: %v", m.Warnings)
	}
	tool(t, m, "GET /echo")
}

func containsWarning(m *model.Manifest, substr string) bool {
	for _, w := range m.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

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
	depth := jsonDepth(t, tl.Body.Schema)
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
	// Each level references the next level 6 times: 6^8 paths without a budget.
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
	if len(tl.Body.Schema) > 2_000_000 {
		t.Errorf("schema is %d bytes; budget did not bound it", len(tl.Body.Schema))
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
	// Relative refs would resolve against the working directory.
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
			if len(m.Tools) != 1 || m.Tools[0].Path != "/ok" {
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
	// Each of these external refs panics libopenapi in-process if it reaches it
	// (pb33f/libopenapi#705). They must be dropped, with the operation kept.
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
		// pb33f/libopenapi#705: this exact shape panics on a libopenapi goroutine.
		"null next to ref": "          n:\n          a: {$ref: x}\n",
		"null":             "          application/json:\n",
		// Also panics libopenapi (same site); Media Type Objects can't be $refs in 3.0/3.1.
		"ref only":  "          a: {$ref: '/comprnents/schemas/Tree'}\n",
		"local ref": "          application/json: {$ref: '#/components/schemas/X'}\n",
		"scalar":    "          application/json: 5\n",
		"list":      "          application/json: [a]\n",
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
	// A property, a component and an example value may all be called
	// "content" without being a content map.
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
	if tl.Body == nil {
		t.Fatal("body missing")
	}
}

func TestParsePoisonedDescriptions(t *testing.T) {
	src := "openapi: 3.0.3\ninfo: {title: T, version: '1'}\npaths:\n  /x:\n    get:\n" +
		"      summary: \"List things\\u202e now\\u200b\"\n" +
		"      parameters:\n        - {name: a, in: query, description: \"Hidden\\u2066 text\", schema: {type: string, description: \"<script>x</script>ok\\u202d\"}}\n" +
		"      responses: {'200': {description: ok}}\n"
	tl := tool(t, parse(t, src, Options{}), "GET /x")
	if tl.Description != "List things now" {
		t.Errorf("description = %q", tl.Description)
	}
	p := param(t, tl, "a")
	if p.Desc != "Hidden text" {
		t.Errorf("param desc = %q", p.Desc)
	}
	assertJSON(t, "schema description", p.Schema, `{"type":"string","description":"xok"}`)
}

const swagger2 = `
swagger: "2.0"
info: {title: Legacy, version: "0.9"}
host: legacy.example.test
basePath: /api
schemes: [https, http]
consumes: [application/json]
paths:
  /users/{id}:
    get:
      operationId: getUser
      parameters:
        - {name: id, in: path, required: true, type: integer, format: int64, minimum: 1}
        - {name: fields, in: query, type: array, items: {type: string, enum: [a, b]}}
      responses: {'200': {description: ok}}
    put:
      parameters:
        - {name: id, in: path, required: true, type: integer}
        - name: user
          in: body
          required: true
          schema: {$ref: '#/definitions/User'}
      responses: {'200': {description: ok}}
  /avatar:
    post:
      consumes: [multipart/form-data]
      parameters:
        - {name: file, in: formData, type: file}
      responses: {'200': {description: ok}}
definitions:
  User:
    type: object
    properties:
      name: {type: string}
`

func TestParseSwagger2(t *testing.T) {
	m := parse(t, swagger2, Options{})
	if m.SpecVersion != "2.0" || m.Title != "Legacy" {
		t.Errorf("header = %q %q", m.SpecVersion, m.Title)
	}
	if !reflect.DeepEqual(m.Servers, []string{"https://legacy.example.test/api", "http://legacy.example.test/api"}) {
		t.Errorf("Servers = %v", m.Servers)
	}

	get := tool(t, m, "GET /users/{id}")
	if get.Name != "getUser" {
		t.Errorf("name = %q", get.Name)
	}
	assertJSON(t, "id", param(t, get, "id").Schema, `{"type":"integer","format":"int64","minimum":1}`)
	assertJSON(t, "fields", param(t, get, "fields").Schema, `{"type":"array","items":{"type":"string","enum":["a","b"]}}`)

	put := tool(t, m, "PUT /users/{id}")
	if put.Body == nil || put.Body.ContentType != "application/json" || !put.Body.Required {
		t.Fatalf("body = %+v", put.Body)
	}
	assertJSON(t, "body", put.Body.Schema, `{"type":"object","properties":{"name":{"type":"string"}}}`)
	if len(put.Params) != 1 {
		t.Errorf("body param must not appear in Params: %+v", put.Params)
	}

	av := tool(t, m, "POST /avatar")
	if !hasIssue(av, model.IssueUnsupported, "file") || av.Body != nil {
		t.Errorf("file-only formData: body %+v, issues %+v", av.Body, av.Issues)
	}
}

func TestParseRejectsNonSpecs(t *testing.T) {
	for name, src := range map[string]string{
		"asyncapi":     "asyncapi: 2.6.0\ninfo: {title: t, version: '1'}\nchannels: {}\n",
		"not yaml":     "{{{{ nope",
		"plain object": `{"hello": "world"}`,
		"openapi 4":    "openapi: 4.0.0\ninfo: {title: t, version: '1'}\npaths: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(src), Options{}); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestParseWritesNothingToStdout(t *testing.T) {
	// libopenapi's default logger writes to stdout, which is reserved for
	// JSON-RPC in serve/run. Parse must never let it.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	_, _ = Parse([]byte(refChain(3)+"    Broken: {$ref: '#/components/schemas/Missing'}\n"), Options{})
	_, _ = Parse([]byte(swagger2), Options{})
	_, _ = Parse([]byte("openapi: 3.0.3\ninfo: {title: t, version: '1'}\npaths:\n  /x: {get: {parameters: [{$ref: '#/nope'}], responses: {}}}\n"), Options{})

	_ = w.Close()
	os.Stdout = orig
	out, _ := io.ReadAll(r)
	if len(out) != 0 {
		t.Errorf("Parse wrote to stdout: %q", out)
	}
}

// Form and multipart bodies (SPEC M6 design note): JSON is preferred, the
// encoding of each field is kept, binary fields are dropped with an issue.
func TestParseFormBodies(t *testing.T) {
	src := `
openapi: 3.0.3
info: {title: T, version: '1'}
paths:
  /customers:
    post:
      requestBody:
        required: true
        content:
          application/x-www-form-urlencoded:
            schema:
              type: object
              required: [email]
              properties:
                email: {type: string}
                metadata: {type: object, additionalProperties: {type: string}}
                tags: {type: array, items: {type: string}}
            encoding:
              metadata: {style: deepObject, explode: true}
              tags: {style: form, explode: false}
      responses: {'200': {description: ok}}
  /notes:
    post:
      requestBody:
        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                title: {type: string}
                attachment: {type: string, format: binary}
      responses: {'200': {description: ok}}
  /both:
    post:
      requestBody:
        content:
          application/x-www-form-urlencoded:
            schema: {type: object, properties: {a: {type: string}}}
          application/json:
            schema: {type: object, properties: {a: {type: string}}}
      responses: {'200': {description: ok}}
  /empty:
    get:
      requestBody:
        content:
          application/x-www-form-urlencoded:
            schema: {type: object, properties: {}, additionalProperties: false}
      responses: {'200': {description: ok}}
  /scalar:
    post:
      requestBody:
        content:
          application/x-www-form-urlencoded:
            schema: {type: string}
      responses: {'200': {description: ok}}
`
	m := parse(t, src, Options{})
	c := tool(t, m, "POST /customers")
	if c.Body == nil || c.Body.ContentType != "application/x-www-form-urlencoded" || !c.Body.Required {
		t.Fatalf("form body = %+v", c.Body)
	}
	want := map[string]model.Encoding{"metadata": {Style: "deepObject", Explode: true}, "tags": {Style: "form", Explode: false}}
	if !reflect.DeepEqual(c.Body.Encoding, want) {
		t.Errorf("encoding = %+v", c.Body.Encoding)
	}
	if hasIssue(c, model.IssueUnsupported, "") {
		t.Errorf("form body flagged unsupported: %+v", c.Issues)
	}

	n := tool(t, m, "POST /notes")
	if n.Body == nil || n.Body.ContentType != "multipart/form-data" {
		t.Fatalf("multipart body = %+v", n.Body)
	}
	if strings.Contains(string(n.Body.Schema), "attachment") || !strings.Contains(string(n.Body.Schema), "title") {
		t.Errorf("binary field kept: %s", n.Body.Schema)
	}
	if !hasIssue(n, model.IssueUnsupported, "attachment") {
		t.Errorf("dropped field not reported: %+v", n.Issues)
	}

	if b := tool(t, m, "POST /both"); b.Body == nil || b.Body.ContentType != "application/json" {
		t.Errorf("JSON should win: %+v", b.Body)
	}
	// Stripe declares an empty form body on GET and DELETE: nothing to send,
	// so no body and no "body" parameter.
	if e := tool(t, m, "GET /empty"); e.Body != nil || len(e.Issues) != 0 {
		t.Errorf("empty form body: %+v, %+v", e.Body, e.Issues)
	}
	if sc := tool(t, m, "POST /scalar"); sc.Body != nil || !hasIssue(sc, model.IssueUnsupported, "object") {
		t.Errorf("non-object form body: %+v, %+v", sc.Body, sc.Issues)
	}
}

func TestParseSwagger2FormData(t *testing.T) {
	src := `
swagger: '2.0'
info: {title: T, version: '1'}
host: api.example.test
paths:
  /pets/{id}:
    post:
      consumes: [application/x-www-form-urlencoded]
      parameters:
        - {name: id, in: path, required: true, type: integer}
        - {name: name, in: formData, required: true, type: string, description: New name}
        - {name: status, in: formData, type: string, enum: [available, sold]}
        - {name: tags, in: formData, type: array, items: {type: string}, collectionFormat: multi}
      responses: {'200': {description: ok}}
  /pets/{id}/photo:
    post:
      consumes: [multipart/form-data]
      parameters:
        - {name: id, in: path, required: true, type: integer}
        - {name: caption, in: formData, type: string}
        - {name: file, in: formData, type: file}
      responses: {'200': {description: ok}}
`
	m := parse(t, src, Options{})
	up := tool(t, m, "POST /pets/{id}")
	if up.Body == nil || up.Body.ContentType != "application/x-www-form-urlencoded" || !up.Body.Required {
		t.Fatalf("body = %+v", up.Body)
	}
	assertJSON(t, "form schema", up.Body.Schema, `{"type":"object","required":["name"],"properties":{"name":{"type":"string","description":"New name"},"status":{"type":"string","enum":["available","sold"]},"tags":{"type":"array","items":{"type":"string"}}}}`)
	if len(up.Params) != 1 || up.Params[0].Name != "id" {
		t.Errorf("params = %+v", up.Params)
	}
	ph := tool(t, m, "POST /pets/{id}/photo")
	if ph.Body == nil || ph.Body.ContentType != "multipart/form-data" || strings.Contains(string(ph.Body.Schema), `"file"`) {
		t.Errorf("multipart body = %+v", ph.Body)
	}
	if !hasIssue(ph, model.IssueUnsupported, "file") {
		t.Errorf("file field not reported: %+v", ph.Issues)
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
	// A $ref path item and ordinary operations are fine.
	ok := "swagger: '2.0'\ninfo: {title: t, version: '1'}\npaths:\n  /x:\n    get:\n      description: uses $ref internally\n      responses: {'200': {description: ok}}\n"
	if m := parse(t, ok, Options{}); len(m.Tools) != 1 {
		t.Errorf("valid spec rejected: %v", m.Warnings)
	}
}

// Cookie names must be RFC 6265 tokens: anything else could break the
// Cookie header, so it's dropped with an issue.
func TestParseCookieParams(t *testing.T) {
	src := `
openapi: 3.0.3
info: {title: T, version: '1'}
paths:
  /me:
    get:
      parameters:
        - {name: session_id, in: cookie, required: true, schema: {type: string}, description: The session}
        - {name: theme, in: cookie, schema: {type: string, enum: [light, dark]}}
        - {name: "bad;name", in: cookie, schema: {type: string}}
        - {name: "two words", in: cookie, schema: {type: string}}
      responses: {'200': {description: ok}}
`
	m := parse(t, src, Options{})
	me := tool(t, m, "GET /me")
	if p := param(t, me, "session_id"); p.In != model.InCookie || !p.Required || p.Desc != "The session" {
		t.Errorf("session_id = %+v", p)
	}
	param(t, me, "theme")
	for _, p := range me.Params {
		if strings.ContainsAny(p.UpstreamName, "; ") {
			t.Errorf("invalid cookie name kept: %q", p.UpstreamName)
		}
	}
	if !hasIssue(me, model.IssueUnsupported, "cookie name") {
		t.Errorf("invalid names not reported: %+v", me.Issues)
	}
}
