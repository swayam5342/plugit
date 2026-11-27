package openapi

import (
	"reflect"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

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
	if !reflect.DeepEqual(m.Endpoints, []string{"https://legacy.example.test/api", "http://legacy.example.test/api"}) {
		t.Errorf("Servers = %v", m.Endpoints)
	}

	get := tool(t, m, "GET /users/{id}")
	if get.Name != "getUser" {
		t.Errorf("name = %q", get.Name)
	}
	assertJSON(t, "id", param(t, get, "id").Schema, `{"type":"integer","format":"int64","minimum":1}`)
	assertJSON(t, "fields", param(t, get, "fields").Schema, `{"type":"array","items":{"type":"string","enum":["a","b"]}}`)

	put := tool(t, m, "PUT /users/{id}")
	if put.HTTP.Body == nil || put.HTTP.Body.ContentType != "application/json" || !put.HTTP.Body.Required {
		t.Fatalf("body = %+v", put.HTTP.Body)
	}
	assertJSON(t, "body", put.HTTP.Body.Schema, `{"type":"object","properties":{"name":{"type":"string"}}}`)
	if len(put.Params) != 1 {
		t.Errorf("body param must not appear in Params: %+v", put.Params)
	}

	av := tool(t, m, "POST /avatar")
	if !hasIssue(av, model.IssueUnsupported, "file") || av.HTTP.Body != nil {
		t.Errorf("file-only formData: body %+v, issues %+v", av.HTTP.Body, av.Issues)
	}
}
