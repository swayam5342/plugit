package graphql

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	"github.com/swayam5342/plugit/internal/utils"
)

// Format names (SPEC §3.5 P4).
const (
	FormatSDL           = "graphql-sdl"
	FormatIntrospection = "graphql-introspection"
)

// ErrUnsupportedSpec wraps every LoadSchema failure: the input is not a GraphQL
// schema PlugIt can load.
var ErrUnsupportedSpec = errors.New("unsupported spec: need a GraphQL SDL schema or introspection result")

// sdlKeywords are the tokens a type-system document can start with.
var sdlKeywords = [...]string{"type", "schema", "interface", "enum", "input", "scalar", "union", "directive", "extend"}

// DetectSDL reports whether data is a GraphQL SDL document. The cheap token
// check runs first; only input that passes it is fully parsed.
func DetectSDL(data []byte) (ok bool) {
	if !sdlKeywordFirst(data) {
		return false
	}
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	_, err := parser.ParseSchema(&ast.Source{Name: "schema.graphql", Input: string(data)})
	return err == nil
}

// sdlKeywordFirst reports whether the first token after an optional BOM,
// whitespace, commas and # comments is an SDL keyword or a """ description.
// It does not allocate.
func sdlKeywordFirst(data []byte) bool {
	data = utils.TrimBOM(data)
	for i := 0; i < len(data); {
		switch data[i] {
		case ' ', '\t', '\n', '\r', ',':
			i++
		case '#':
			for i < len(data) && data[i] != '\n' && data[i] != '\r' {
				i++
			}
		default:
			rest := data[i:]
			if len(rest) >= 3 && rest[0] == '"' && rest[1] == '"' && rest[2] == '"' {
				return true
			}
			j := 0
			for j < len(rest) && isNameByte(rest[j], j > 0) {
				j++
			}
			for _, kw := range sdlKeywords {
				if string(rest[:j]) == kw {
					return true
				}
			}
			return false
		}
	}
	return false
}

func isNameByte(c byte, notFirst bool) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (notFirst && c >= '0' && c <= '9')
}

// isName reports whether s matches the GraphQL Name grammar.
func isName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isNameByte(s[i], i > 0) {
			return false
		}
	}
	return true
}

// DetectIntrospection reports whether data is a JSON introspection result:
// an object with a top-level "__schema" object, or one under "data". An
// object that also has a top-level "openapi" or "swagger" key is OpenAPI's,
// so no fixture is detected by both formats (SPEC P4).
func DetectIntrospection(data []byte) bool {
	trimmed := trimJSONStart(data)
	if len(trimmed) == 0 || trimmed[0] != '{' || !bytes.Contains(trimmed, []byte(`"__schema"`)) {
		return false
	}
	raw, openapiKey, err := decodeIntrospection(trimmed)
	return err == nil && raw != nil && !openapiKey
}

// trimJSONStart drops a leading UTF-8 BOM (which encoding/json rejects; PowerShell
// 5.1 writes one) and leading whitespace. Detection and loading share it so a
// file that is detected also loads.
func trimJSONStart(data []byte) []byte {
	return bytes.TrimLeft(utils.TrimBOM(data), " \t\r\n")
}

// introspectionRoot returns the raw "__schema" object of an introspection result.
func introspectionRoot(data []byte) (json.RawMessage, error) {
	raw, _, err := decodeIntrospection(data)
	return raw, err
}

// decodeIntrospection returns the raw "__schema" object of an introspection
// result and whether the object also has a top-level openapi or swagger key.
func decodeIntrospection(data []byte) (raw json.RawMessage, openapiKey bool, err error) {
	data = trimJSONStart(data)
	var env struct {
		Data *struct {
			Schema json.RawMessage `json:"__schema"`
		} `json:"data"`
		Schema  json.RawMessage `json:"__schema"`
		OpenAPI json.RawMessage `json:"openapi"`
		Swagger json.RawMessage `json:"swagger"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, false, fmt.Errorf("decoding introspection result: %w", err)
	}
	raw = env.Schema
	if env.Data != nil && len(env.Data.Schema) > 0 {
		raw = env.Data.Schema
	}
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false, errors.New(`no "__schema" object at the top level or under "data"`)
	}
	return raw, env.OpenAPI != nil || env.Swagger != nil, nil
}

// LoadSchema loads data, in the named format (FormatSDL or
// FormatIntrospection), into a validated schema. Every error wraps
// ErrUnsupportedSpec, and a gqlparser panic is recovered into one.
func LoadSchema(data []byte, format string) (s *ast.Schema, err error) {
	defer func() {
		if r := recover(); r != nil {
			s, err = nil, fmt.Errorf("%w: GraphQL parser panic: %v", ErrUnsupportedSpec, r)
		}
	}()
	src := &ast.Source{Name: "schema.graphql"}
	switch format {
	case FormatSDL:
		src.Input = string(data)
	case FormatIntrospection:
		sdl, cerr := introspectionToSDL(data)
		if cerr != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnsupportedSpec, cerr)
		}
		src.Name, src.Input = "introspection.graphql", sdl
	default:
		return nil, fmt.Errorf("%w: unknown GraphQL format %q", ErrUnsupportedSpec, format)
	}
	s, gerr := gqlparser.LoadSchema(src)
	if gerr != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupportedSpec, gerr)
	}
	if s.Query == nil {
		// The GraphQL spec requires a query root; without one there is nothing to serve.
		return nil, fmt.Errorf("%w: schema has no query root type", ErrUnsupportedSpec)
	}
	return s, nil
}
