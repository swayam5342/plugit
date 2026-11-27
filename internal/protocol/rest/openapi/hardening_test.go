package openapi

import (
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
