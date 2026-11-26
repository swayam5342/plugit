// Package utils holds small generic helpers shared across PlugIt's packages:
// JSON encoding, string and byte checks, map keys.
//
// It is a pure leaf: it imports only the standard library, never another
// plugit package, and does no network, filesystem, clock, env or randomness
// work. That lets the pure packages (model, naming, schema, lint, tokens,
// curate) and the protocol packages import it without breaking their rules.
package utils

import (
	"bytes"
	"encoding/json"
	"mime"
	"slices"
	"strings"
	"unicode/utf8"
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

// SortedKeys returns the keys of m in ascending order.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
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

// IsBinary reports whether a response body is binary rather than text: its
// media type (from the Content-Type value ct) is an image, audio, video, font
// or archive type, or the body is not valid UTF-8 or contains a NUL byte.
func IsBinary(ct string, body []byte) bool {
	mt, _, _ := mime.ParseMediaType(ct)
	switch {
	case strings.HasPrefix(mt, "image/"), strings.HasPrefix(mt, "audio/"), strings.HasPrefix(mt, "video/"),
		strings.HasPrefix(mt, "font/"), mt == "application/octet-stream", mt == "application/pdf",
		mt == "application/zip", mt == "application/gzip":
		return true
	}
	return !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0
}
