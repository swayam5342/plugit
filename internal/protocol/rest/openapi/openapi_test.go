package openapi

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/rest/postman"
)

// parse mimics the ingest dispatcher for tests: a Postman collection goes to
// postman.Parse, everything else to Parse.
func parse(t *testing.T, src string, opts Options) *model.Manifest {
	t.Helper()
	p := Parse
	if postman.Detect([]byte(src)) {
		p = postman.Parse
	}
	m, err := p([]byte(src), opts)
	if err != nil {
		t.Fatalf("parse: %v", err)
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
		if tl.Params[i].Bind.Upstream == name {
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
	if !reflect.DeepEqual(m.Endpoints, []string{"https://api.example.test/v1"}) {
		t.Errorf("Servers = %v", m.Endpoints)
	}

	var keys []model.OpKey
	for _, tl := range m.Tools {
		keys = append(keys, tl.Key)
	}
	want := []model.OpKey{"DELETE /pets/{petId}", "GET /pets", "GET /pets/{petId}", "POST /pets"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}

	get := tool(t, m, "GET /pets/{petId}")
	if get.Name != "getPet" || get.Description != "Get a pet" || get.HTTP.Method != http.MethodGet || get.HTTP.Path != "/pets/{petId}" {
		t.Errorf("getPet = %+v", get)
	}
	if get.Safety != model.ReadOnly || !reflect.DeepEqual(get.Tags, []string{"pets"}) || get.ID == "" {
		t.Errorf("getPet safety/tags/id = %v %v %q", get.Safety, get.Tags, get.ID)
	}
	pid := param(t, get, "petId")
	if pid.Bind.In != model.InPath || !pid.Required || pid.Desc != "The pet id" || pid.Name != "petId" {
		t.Errorf("petId = %+v", pid)
	}
	assertJSON(t, "petId schema", pid.Schema, `{"type":"string"}`)
	verbose := param(t, get, "verbose")
	assertJSON(t, "verbose schema", verbose.Schema, `{"type":"integer"}`)
	if verbose.Desc != "overridden" {
		t.Errorf("verbose desc = %q", verbose.Desc)
	}
	if param(t, get, "X-Trace").Bind.In != model.InHeader {
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
	if post.HTTP.Body == nil {
		t.Fatal("Body is nil")
	}
	if post.HTTP.Body.ContentType != "application/json" || !post.HTTP.Body.Required {
		t.Errorf("body = %q required=%v", post.HTTP.Body.ContentType, post.HTTP.Body.Required)
	}
	assertJSON(t, "body", post.HTTP.Body.Schema, `{
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
	if up.HTTP.Body != nil {
		t.Error("a body whose required field is a file should not be modelled")
	}
	if !hasIssue(up, model.IssueUnsupported, "file") {
		t.Errorf("missing file-field issue: %+v", up.Issues)
	}
	if hasIssue(up, model.IssueUnsupported, "cookie") {
		t.Errorf("cookie still flagged unsupported: %+v", up.Issues)
	}
	if p := param(t, up, "session"); p.Bind.In != model.InCookie {
		t.Errorf("session = %+v", p)
	}

	for _, tl := range m.Tools {
		if tl.HTTP.Method == http.MethodTrace {
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
	if p := param(t, me, "session_id"); p.Bind.In != model.InCookie || !p.Required || p.Desc != "The session" {
		t.Errorf("session_id = %+v", p)
	}
	param(t, me, "theme")
	for _, p := range me.Params {
		if strings.ContainsAny(p.Bind.Upstream, "; ") {
			t.Errorf("invalid cookie name kept: %q", p.Bind.Upstream)
		}
	}
	if !hasIssue(me, model.IssueUnsupported, "cookie name") {
		t.Errorf("invalid names not reported: %+v", me.Issues)
	}
}
