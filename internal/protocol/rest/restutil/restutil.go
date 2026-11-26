// Package restutil holds the manifest-building helpers shared by the REST
// ingest parsers, openapi and postman: the Builder that collects tools and
// warnings, the per-tool issue Collector, path validation, parameter and
// form-body modelling (SPEC §3.4, §3.5).
//
// Everything here handles untrusted spec text (SPEC §5.3): text that reaches
// the model or the terminal is cleaned with naming.CleanText, and path
// templates that could leave the pinned base path are skipped.
package restutil

import (
	"errors"
	"log/slog"
	"regexp"
)

// maxWarnings caps the warnings kept on a manifest; Finish counts the rest.
const maxWarnings = 25

// ErrUnsupportedSpec is returned (wrapped) when the input is not a spec
// version a REST parser supports. openapi re-exports it as
// openapi.ErrUnsupportedSpec, so errors.Is matches either name.
var ErrUnsupportedSpec = errors.New("unsupported spec: need OpenAPI 3.0, 3.1 or Swagger 2.0")

// Options configures the REST parsers (openapi.Parse, postman.Parse).
type Options struct {
	Prefix           string
	MaxDescription   int
	AllowUnsafePaths bool
	Logger           *slog.Logger
}

var cookieNameRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$")

// ValidCookieName reports whether name is a valid cookie name: an RFC 6265
// token of 1 to 128 characters.
func ValidCookieName(name string) bool {
	return cookieNameRe.MatchString(name)
}

// Truncate caps spec text quoted in a warning, issue or error (a $ref, a
// path, a key) at 120 bytes, marking the cut with an ellipsis.
func Truncate(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
