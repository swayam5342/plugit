package openapi

import (
	"strings"
	"testing"
)

func TestParseKeepsOnlyKnownSchemaTypes(t *testing.T) {
	src := `
openapi: 3.1.0
info: {title: T, version: '1'}
paths:
  /x:
    get:
      parameters:
        - {name: ok, in: query, schema: {type: integer, minimum: 1}}
        - {name: evil, in: query, schema: {type: "\e]0;owned\ahack", maxLength: 5}}
        - {name: mixed, in: query, schema: {type: [string, "\e[2Jnope", "null"]}}
        - {name: nested, in: query, schema: {type: array, items: {type: "<b>bold</b>"}}}
      responses: {'200': {description: ok}}
`
	tl := tool(t, parse(t, src, Options{}), "GET /x")
	assertJSON(t, "ok", param(t, tl, "ok").Schema, `{"type":"integer","minimum":1}`)
	assertJSON(t, "evil", param(t, tl, "evil").Schema, `{"maxLength":5}`)
	assertJSON(t, "mixed", param(t, tl, "mixed").Schema, `{"type":["string","null"]}`)
	assertJSON(t, "nested", param(t, tl, "nested").Schema, `{"type":"array","items":{}}`)
	for _, p := range tl.Params {
		if strings.ContainsAny(string(p.Schema), "\x1b\a") || strings.Contains(string(p.Schema), `\u001b`) {
			t.Errorf("%s: schema carries an escape sequence: %s", p.UpstreamName, p.Schema)
		}
	}
}
