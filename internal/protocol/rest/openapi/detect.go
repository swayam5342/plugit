package openapi

import (
	"bytes"
	"encoding/json"

	"github.com/swayam5342/plugit/internal/utils"
)

// rootKeys are the top-level keys that mark an OpenAPI 3.x or Swagger 2.0
// document.
var rootKeys = [...]string{"openapi", "swagger"}

// IsOpenAPI reports whether data has a top-level "openapi" or "swagger" key,
// the field every OpenAPI 3.x and Swagger 2.0 document starts from. Parse
// still decides whether the version is one PlugIt supports. A key nested
// deeper, in a description or in a GraphQL schema never counts.
//
// JSON (the first byte after a UTF-8 BOM and whitespace is '{') has its
// top-level keys streamed, with each value skipped unread.
//
// Anything else is scanned as YAML, line by line:
//   - The first significant line must be a column-0 block-mapping entry: a
//     plain key without blanks, ',' or '(' directly followed by ':', or a
//     quoted key, optional blanks, then ':'. Blank lines, #
//     comments, % directives and --- are skipped first. A GraphQL SDL
//     document starts with an SDL keyword or a """ description, never
//     followed by ':', so this rules out all of SDL, including SDL with an
//     openapi: line at column 0 further down.
//   - Some column-0 line must then be openapi: or swagger: (the key plain or
//     quoted) with a value starting with a digit or a quote, as a version does.
//
// Two valid but rare YAML spellings are not detected and need
// --format openapi: a flow-mapping root ({openapi: 3.0.0, ...}) and a first
// plain key with a blank before its ':' (openapi : 3.0.0).
//
// It is linear in len(data), does not allocate on the YAML path and never
// panics.
func IsOpenAPI(data []byte) bool {
	data = utils.TrimBOM(data)
	if trimmed := bytes.TrimLeft(data, " \t\r\n"); len(trimmed) > 0 && trimmed[0] == '{' {
		return jsonHasRootKey(trimmed)
	}
	seenEntry := false
	for len(data) > 0 {
		var line []byte
		line, data, _ = bytes.Cut(data, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if !seenEntry {
			if yamlSkippable(line) {
				continue
			}
			if !yamlMappingEntry(line) {
				return false
			}
			seenEntry = true
		}
		if yamlRootKey(line) {
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

// yamlSkippable reports whether line comes before a document's first
// mapping entry without being one: blank, a # comment, a % directive or a
// --- document marker.
func yamlSkippable(line []byte) bool {
	t := bytes.TrimLeft(line, " \t")
	switch {
	case len(t) == 0 || t[0] == '#':
		return true
	case line[0] == '%':
		return true
	case bytes.HasPrefix(line, []byte("---")):
		return len(line) == 3 || isBlank(line[3])
	}
	return false
}

// yamlMappingEntry reports whether line is a column-0 block-mapping entry:
// a quoted key ("""…""" is a GraphQL description, not a key) then optional
// blanks, or a plain key without blanks, ',' or '(', then ':' ending the
// line or followed by a blank. A plain key with a blank before the ':'
// ("info : x") is valid YAML but not accepted here.
func yamlMappingEntry(line []byte) bool {
	if len(line) == 0 {
		return false
	}
	switch c := line[0]; c {
	case '"', '\'':
		if bytes.HasPrefix(line, []byte(`"""`)) {
			return false
		}
		end := bytes.IndexByte(line[1:], c)
		if end < 0 {
			return false
		}
		return colonEntry(bytes.TrimLeft(line[end+2:], " \t"))
	case ' ', '\t', '[', ']', '{', '}', ',', '#', '&', '*', '!', '|', '>', '%', '@', '`':
		return false
	case '-', '?', ':':
		if len(line) == 1 || isBlank(line[1]) {
			return false
		}
	}
	for i := 0; i < len(line); i++ {
		// ',' and '(' are legal in a plain YAML key but never start an
		// OpenAPI document; ',' is GraphQL whitespace, so "type,Query,{a:"
		// is SDL.
		if isBlank(line[i]) || line[i] == ',' || line[i] == '(' {
			return false
		}
		if colonEntry(line[i:]) {
			return i > 0
		}
	}
	return false
}

// colonEntry reports whether rest is ':' at the end of a line or before a blank.
func colonEntry(rest []byte) bool {
	return len(rest) > 0 && rest[0] == ':' && (len(rest) == 1 || isBlank(rest[1]))
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

// yamlRootKey reports whether line is a column-0 YAML mapping entry whose key
// is openapi or swagger, optionally single- or double-quoted, and whose value
// starts with a digit or a quote: the key, optional blanks, ':', blanks, then
// the version.
func yamlRootKey(line []byte) bool {
	for _, k := range rootKeys {
		for _, q := range [...]string{"", `"`, `'`} {
			rest, ok := bytes.CutPrefix(line, []byte(q+k+q))
			if !ok {
				continue
			}
			rest = bytes.TrimLeft(rest, " \t")
			if len(rest) < 2 || rest[0] != ':' || !isBlank(rest[1]) {
				continue
			}
			v := bytes.TrimLeft(rest[1:], " \t")
			if len(v) > 0 && (v[0] >= '0' && v[0] <= '9' || v[0] == '"' || v[0] == '\'') {
				return true
			}
		}
	}
	return false
}
