package protocol

import (
	"bytes"
	"mime"
	"strings"
	"unicode/utf8"
)

// IsBinary reports whether a response body is binary rather than text, for
// every protocol's Decode: its
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
