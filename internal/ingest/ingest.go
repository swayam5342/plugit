// Package ingest turns source bytes into a model.Manifest by detecting the
// source format and routing to the matching Format (SPEC §3.5 P4). Detection
// runs in the isolate child, so every format gets child-process isolation
// for free (T2). This package holds only the interface, the neutral Options
// and Dispatch; the concrete format list lives in internal/formats.
package ingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/utils"
)

// ErrUnsupportedFormat means no Format matched the source or the forced
// format name is unknown.
var ErrUnsupportedFormat = errors.New("unsupported spec format")

// Format turns source bytes of one format into a Manifest.
type Format interface {
	Name() string                                             // "openapi", "postman", …
	Protocol() model.Protocol                                 // the protocol its manifests carry
	Detect(data []byte) bool                                  // cheap and deterministic
	Parse(data []byte, opts Options) (*model.Manifest, error) // runs in the isolate child
}

// Dispatch parses data with the format named by forceName, or, when it is
// empty, the first Format in formats whose Detect matches. Order matters: a
// more specific format comes before a fallback. The manifest's Format is
// set to the name of the format that parsed it (SPEC 3.4: which ingester
// produced it), so it is always a name --format and source.format accept.
func Dispatch(formats []Format, data []byte, forceName string, opts Options) (*model.Manifest, error) {
	if forceName != "" {
		for _, f := range formats {
			if f.Name() == forceName {
				return parseWith(f, data, opts)
			}
		}
		return nil, fmt.Errorf("%w: unknown format %q", ErrUnsupportedFormat, forceName)
	}
	for _, f := range formats {
		if f.Detect(data) {
			return parseWith(f, data, opts)
		}
	}
	if reason := jsonSyntaxError(data); reason != nil {
		return nil, fmt.Errorf("%w: no format recognized the source (it is not valid JSON: %w)", ErrUnsupportedFormat, reason)
	}
	return nil, fmt.Errorf("%w: no format recognized the source", ErrUnsupportedFormat)
}

// jsonSyntaxError returns the JSON syntax error of data when it looks like a
// JSON object (its first byte after a BOM and whitespace is '{') but does not
// decode: a syntax error before a format's marker key hides the key from
// detection, and the error says why. It returns nil for anything else.
func jsonSyntaxError(data []byte) error {
	data = bytes.TrimLeft(utils.TrimBOM(data), " \t\r\n")
	if len(data) == 0 || data[0] != '{' {
		return nil
	}
	var v struct{}
	var syn *json.SyntaxError
	if err := json.Unmarshal(data, &v); errors.As(err, &syn) {
		return syn
	}
	return nil
}

func parseWith(f Format, data []byte, opts Options) (*model.Manifest, error) {
	m, err := f.Parse(data, opts)
	if err != nil {
		return nil, err
	}
	if m != nil {
		m.Format = f.Name()
	}
	return m, nil
}
