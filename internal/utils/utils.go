// Package utils holds small generic helpers shared across PlugIt's packages:
// JSON encoding and string and byte checks.
//
// It is a pure leaf: it imports only the standard library, never another
// plugit package, and does no network, filesystem, clock, env or randomness
// work. That lets the pure packages (model, naming, schema, lint, tokens,
// curate) and the protocol packages import it without breaking their rules.
package utils

import (
	"bytes"
	"encoding/json"
	"strings"
)

// MustJSON encodes v. Callers pass values that came from json.Unmarshal or
// were built from plain maps, slices and scalars, so encoding cannot fail; a
// permissive {} is the fallback rather than a panic. Encoding a map sorts its
// keys, so the output is deterministic.
func MustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// HasControl reports whether s contains an ASCII control character (below
// 0x20, or DEL).
func HasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// TrimBOM drops one leading UTF-8 byte order mark from data, if present.
func TrimBOM(data []byte) []byte {
	return bytes.TrimPrefix(data, utf8BOM)
}
