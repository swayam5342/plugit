// Package ingest turns source bytes into a model.Manifest by detecting the
// source format and routing to the matching Format (SPEC §3.5 P4). Detection
// runs in the isolate child, so every format gets child-process isolation
// for free (T2). This package holds only the interface, the neutral Options
// and Dispatch; the concrete format list lives in internal/formats.
package ingest

import (
	"errors"
	"fmt"

	"github.com/swayam5342/plugit/internal/model"
)

// ErrUnsupportedFormat means no Format matched the source or the forced
// format name is unknown.
var ErrUnsupportedFormat = errors.New("unsupported spec: unrecognized or unknown source format")

// Format turns source bytes of one format into a Manifest.
type Format interface {
	Name() string                                             // "openapi", "postman", …
	Protocol() model.Protocol                                 // the protocol its manifests carry
	Detect(data []byte) bool                                  // cheap and deterministic
	Parse(data []byte, opts Options) (*model.Manifest, error) // runs in the isolate child
}

// Dispatch parses data with the format named by forceName, or, when it is
// empty, the first Format in formats whose Detect matches. Order matters: a
// more specific format comes before a fallback.
func Dispatch(formats []Format, data []byte, forceName string, opts Options) (*model.Manifest, error) {
	if forceName != "" {
		for _, f := range formats {
			if f.Name() == forceName {
				return f.Parse(data, opts)
			}
		}
		return nil, fmt.Errorf("%w: unknown format %q", ErrUnsupportedFormat, forceName)
	}
	for _, f := range formats {
		if f.Detect(data) {
			return f.Parse(data, opts)
		}
	}
	return nil, fmt.Errorf("%w: unrecognized source format", ErrUnsupportedFormat)
}
