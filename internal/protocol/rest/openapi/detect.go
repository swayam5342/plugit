package openapi

import (
	"bytes"
	"encoding/json"
)

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// rootKeys are the top-level keys that mark an OpenAPI 3.x or Swagger 2.0
// document.
var rootKeys = [...]string{"openapi", "swagger"}

// IsOpenAPI reports whether data has a top-level "openapi" or "swagger" key,
// the field every OpenAPI 3.x and Swagger 2.0 document starts from. It only
// looks at keys, not values: Parse still decides whether the version is one
// PlugIt supports. A key nested deeper, in a description or in a GraphQL
// schema never counts.
//
// JSON (the first byte after a UTF-8 BOM and whitespace is '{') has its
// top-level keys streamed, with each value skipped unread. Anything else is
// scanned as YAML for a key at column 0, plain or quoted; blank lines, #
// comments, --- and % directives start with no such key, so they are passed
// over. It is linear in len(data) and never panics.
func IsOpenAPI(data []byte) bool {
	data = bytes.TrimPrefix(data, utf8BOM)
	if trimmed := bytes.TrimLeft(data, " \t\r\n"); len(trimmed) > 0 && trimmed[0] == '{' {
		return jsonHasRootKey(trimmed)
	}
	for len(data) > 0 {
		var line []byte
		line, data, _ = bytes.Cut(data, []byte{'\n'})
		if yamlRootKey(bytes.TrimSuffix(line, []byte{'\r'})) {
			return true
		}
	}
	return false
}

// jsonHasRootKey streams the keys of the top-level JSON object in data. It
// stops at the first root key or at the first syntax error.
func jsonHasRootKey(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return false
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		key, ok := tok.(string)
		if !ok {
			return false
		}
		for _, k := range rootKeys {
			if key == k {
				return true
			}
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return false
		}
	}
	return false
}

// yamlRootKey reports whether line is a column-0 YAML mapping entry whose key
// is openapi or swagger, optionally single- or double-quoted: the key, then
// optional blanks, then ':' at the end of the line or before a blank.
func yamlRootKey(line []byte) bool {
	for _, k := range rootKeys {
		for _, q := range [...]string{"", `"`, `'`} {
			rest, ok := bytes.CutPrefix(line, []byte(q+k+q))
			if !ok {
				continue
			}
			rest = bytes.TrimLeft(rest, " \t")
			if len(rest) == 0 || rest[0] != ':' {
				continue
			}
			if len(rest) == 1 || rest[1] == ' ' || rest[1] == '\t' {
				return true
			}
		}
	}
	return false
}
