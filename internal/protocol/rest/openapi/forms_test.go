package openapi

import (
	"reflect"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

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
	if c.HTTP.Body == nil || c.HTTP.Body.ContentType != "application/x-www-form-urlencoded" || !c.HTTP.Body.Required {
		t.Fatalf("form body = %+v", c.HTTP.Body)
	}
	want := map[string]model.Encoding{"metadata": {Style: "deepObject", Explode: true}, "tags": {Style: "form", Explode: false}}
	if !reflect.DeepEqual(c.HTTP.Body.Encoding, want) {
		t.Errorf("encoding = %+v", c.HTTP.Body.Encoding)
	}
	if hasIssue(c, model.IssueUnsupported, "") {
		t.Errorf("form body flagged unsupported: %+v", c.Issues)
	}

	n := tool(t, m, "POST /notes")
	if n.HTTP.Body == nil || n.HTTP.Body.ContentType != "multipart/form-data" {
		t.Fatalf("multipart body = %+v", n.HTTP.Body)
	}
	if strings.Contains(string(n.HTTP.Body.Schema), "attachment") || !strings.Contains(string(n.HTTP.Body.Schema), "title") {
		t.Errorf("binary field kept: %s", n.HTTP.Body.Schema)
	}
	if !hasIssue(n, model.IssueUnsupported, "attachment") {
		t.Errorf("dropped field not reported: %+v", n.Issues)
	}

	if b := tool(t, m, "POST /both"); b.HTTP.Body == nil || b.HTTP.Body.ContentType != "application/json" {
		t.Errorf("JSON should win: %+v", b.HTTP.Body)
	}
	if e := tool(t, m, "GET /empty"); e.HTTP.Body != nil || len(e.Issues) != 0 {
		t.Errorf("empty form body: %+v, %+v", e.HTTP.Body, e.Issues)
	}
	if sc := tool(t, m, "POST /scalar"); sc.HTTP.Body != nil || !hasIssue(sc, model.IssueUnsupported, "object") {
		t.Errorf("non-object form body: %+v, %+v", sc.HTTP.Body, sc.Issues)
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
	if up.HTTP.Body == nil || up.HTTP.Body.ContentType != "application/x-www-form-urlencoded" || !up.HTTP.Body.Required {
		t.Fatalf("body = %+v", up.HTTP.Body)
	}
	assertJSON(t, "form schema", up.HTTP.Body.Schema, `{"type":"object","required":["name"],"properties":{"name":{"type":"string","description":"New name"},"status":{"type":"string","enum":["available","sold"]},"tags":{"type":"array","items":{"type":"string"}}}}`)
	if len(up.Params) != 1 || up.Params[0].Name != "id" {
		t.Errorf("params = %+v", up.Params)
	}
	ph := tool(t, m, "POST /pets/{id}/photo")
	if ph.HTTP.Body == nil || ph.HTTP.Body.ContentType != "multipart/form-data" || strings.Contains(string(ph.HTTP.Body.Schema), `"file"`) {
		t.Errorf("multipart body = %+v", ph.HTTP.Body)
	}
	if !hasIssue(ph, model.IssueUnsupported, "file") {
		t.Errorf("file field not reported: %+v", ph.Issues)
	}
}
