package openapi

import (
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

func TestSwagger2ParamTypesAreFiltered(t *testing.T) {
	src := `
swagger: "2.0"
info: {title: T, version: '1'}
host: api.example.test
paths:
  /x:
    get:
      parameters:
        - {name: ok, in: query, type: integer}
        - {name: evil, in: query, type: "\e]0;owned\ahack"}
        - {name: list, in: query, type: array, items: {type: "<b>bold</b>"}}
        - {name: blob, in: query, type: file}
      responses: {'200': {description: ok}}
`
	tl := tool(t, parse(t, src, Options{}), "GET /x")
	assertJSON(t, "ok", param(t, tl, "ok").Schema, `{"type":"integer"}`)
	assertJSON(t, "evil", param(t, tl, "evil").Schema, `{}`)
	assertJSON(t, "list", param(t, tl, "list").Schema, `{"type":"array","items":{}}`)
	assertJSON(t, "blob", param(t, tl, "blob").Schema, `{"type":"string","format":"binary"}`)
}

func TestFileFieldsStayOutOfFormBodies(t *testing.T) {
	v2 := `
swagger: "2.0"
info: {title: T, version: '1'}
host: api.example.test
paths:
  /upload:
    post:
      consumes: [multipart/form-data]
      parameters:
        - {name: note, in: formData, type: string}
        - {name: doc, in: formData, type: file}
      responses: {'200': {description: ok}}
`
	v3 := `
openapi: 3.0.3
info: {title: T, version: '1'}
paths:
  /upload:
    post:
      requestBody:
        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                note: {type: string}
                doc: {type: file}
      responses: {'200': {description: ok}}
`
	for name, src := range map[string]string{"swagger 2.0": v2, "openapi 3": v3} {
		t.Run(name, func(t *testing.T) {
			tl := tool(t, parse(t, src, Options{}), "POST /upload")
			if tl.HTTP.Body == nil {
				t.Fatal("no body")
			}
			assertJSON(t, "body", tl.HTTP.Body.Schema, `{"type":"object","properties":{"note":{"type":"string"}}}`)
			if !hasIssue(tl, model.IssueUnsupported, `"doc"`) {
				t.Errorf("the dropped file field isn't reported: %+v", tl.Issues)
			}
		})
	}
}

func TestPathsCannotLeaveTheBasePath(t *testing.T) {
	oas := `
openapi: 3.0.3
info: {title: T, version: '1'}
paths:
  /ok/{id}: {get: {responses: {'200': {description: ok}}}}
  /v1/%2e%2e/secret: {get: {responses: {'200': {description: ok}}}}
  /v1/%2E%2E%2Fadmin: {get: {responses: {'200': {description: ok}}}}
  /v1/.%2e/x: {get: {responses: {'200': {description: ok}}}}
  /v1/a%5c..%5cb: {get: {responses: {'200': {description: ok}}}}
  /v1/a%0d%0ab: {get: {responses: {'200': {description: ok}}}}
`
	m := parse(t, oas, Options{})
	if len(m.Tools) != 1 || m.Tools[0].Key != "GET /ok/{id}" {
		t.Errorf("openapi tools = %v", toolKeys(m))
	}
	if n := countWarnings(m, "skipped"); n != 5 {
		t.Errorf("openapi: %d paths reported as skipped, want 5: %q", n, m.Warnings)
	}

	postman := `{"info":{"name":"x","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},"item":[
{"name":"ok","request":{"method":"GET","url":"https://api.example.test/v1/pets/:id"}},
{"name":"up","request":{"method":"GET","url":"https://api.example.test/v1/../../admin"}},
{"name":"enc","request":{"method":"GET","url":"https://api.example.test/v1/%2e%2e/secret"}},
{"name":"esc","request":{"method":"GET","url":"https://api.example.test/v1/x\u001b]0;owned\u0007y"}},
{"name":"bidi","request":{"method":"GET","url":"https://api.example.test/v1/x\u202ey"}}]}`
	m = parse(t, postman, Options{})
	if len(m.Tools) != 1 || m.Tools[0].Key != "GET /v1/pets/{id}" {
		t.Errorf("postman tools = %v", toolKeys(m))
	}
	if n := countWarnings(m, "skipped"); n != 4 {
		t.Errorf("postman: %d requests reported as skipped, want 4: %q", n, m.Warnings)
	}
	for _, w := range m.Warnings {
		if strings.ContainsAny(w, "\x1b\a") {
			t.Errorf("warning carries a control character: %q", w)
		}
	}
}

func toolKeys(m *model.Manifest) []model.OpKey {
	keys := make([]model.OpKey, len(m.Tools))
	for i, tl := range m.Tools {
		keys[i] = tl.Key
	}
	return keys
}

func countWarnings(m *model.Manifest, substr string) int {
	n := 0
	for _, w := range m.Warnings {
		if strings.Contains(w, substr) {
			n++
		}
	}
	return n
}

func TestHiddenTextInSchemaValuesIsReported(t *testing.T) {
	src := `
openapi: 3.1.0
info: {title: T, version: '1'}
paths:
  /x:
    post:
      parameters:
        - {name: clean, in: query, schema: {type: string, enum: [a, b], default: a, pattern: "^[ab]$", format: letter}}
        - {name: e, in: query, schema: {type: string, enum: ["a\u200bb"]}}
        - {name: d, in: query, schema: {type: string, default: "x\u202ey"}}
        - {name: c, in: query, schema: {const: "x\u2066y"}}
        - {name: p, in: query, schema: {type: string, pattern: "x\u200dy"}}
        - {name: f, in: query, schema: {type: string, format: "x\u200by"}}
      requestBody:
        content:
          application/json:
            schema: {type: object, properties: {"na\u200bme": {type: string}}}
      responses: {'200': {description: ok}}
`
	tl := tool(t, parse(t, src, Options{}), "POST /x")
	for _, where := range []string{"enum", "default", "const", "pattern", "format", "property name"} {
		if !hasIssue(tl, model.IssueHiddenText, where) {
			t.Errorf("hidden text in a schema %s isn't reported: %+v", where, tl.Issues)
		}
	}
	n := 0
	for _, is := range tl.Issues {
		if is.Kind == model.IssueHiddenText {
			n++
		}
	}
	if n != 6 {
		t.Errorf("%d hidden-text issues, want 6 (none for the clean parameter): %+v", n, tl.Issues)
	}
}

func TestUnsafePathsNeedTheSwitch(t *testing.T) {
	postman := `{"info":{"name":"x","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},"item":[
{"name":"ok","request":{"method":"GET","url":"https://api.example.test/v1/pets/:id"}},
{"name":"up","request":{"method":"GET","url":"https://api.example.test/v1/../admin"}},
{"name":"space","request":{"method":"GET","url":"https://api.example.test/my folder/items"}},
{"name":"esc","request":{"method":"GET","url":"https://api.example.test/v1/x\u001b]0;owned\u0007y"}},
{"name":"bidi","request":{"method":"GET","url":"https://api.example.test/v1/x\u202ey"}}]}`
	oas := `
openapi: 3.0.3
info: {title: T, version: '1'}
paths:
  /ok: {get: {responses: {'200': {description: ok}}}}
  /v1/%2e%2e/secret: {get: {responses: {'200': {description: ok}}}}
  /v1/a%5cb: {get: {responses: {'200': {description: ok}}}}
  /v1/a%0d%0ab: {get: {responses: {'200': {description: ok}}}}
  /v1/a?b=c: {get: {responses: {'200': {description: ok}}}}
`
	for _, tc := range []struct {
		name, src string
		off, on   int
	}{{"postman", postman, 1, 3}, {"openapi", oas, 1, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			m := parse(t, tc.src, Options{})
			if len(m.Tools) != tc.off {
				t.Errorf("switch off: tools = %v, want %d", toolKeys(m), tc.off)
			}
			if n := countWarnings(m, "allow_insecure"); n != tc.on-tc.off {
				t.Errorf("switch off: %d warnings name the switch, want %d: %q", n, tc.on-tc.off, m.Warnings)
			}
			m = parse(t, tc.src, Options{AllowUnsafePaths: true})
			if len(m.Tools) != tc.on {
				t.Errorf("switch on: tools = %v, want %d", toolKeys(m), tc.on)
			}
			for _, tl := range m.Tools {
				if strings.ContainsAny(string(tl.Key), "\x1b\a?") || strings.ContainsRune(string(tl.Key), 0x202e) {
					t.Errorf("switch on: %q was let through", tl.Key)
				}
			}
			if n := countWarnings(m, "skipped"); n != 2 {
				t.Errorf("switch on: %d paths still skipped, want 2: %q", n, m.Warnings)
			}
		})
	}
}
