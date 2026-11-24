// Package ingest turns source bytes into a model.Manifest by detecting the
// source format and routing to the matching Format (SPEC §3.5 P4). Detection
// runs in the isolate child, so every format gets child-process isolation
// for free (T2).
package ingest

import (
	"fmt"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/rest/openapi"
)

// Format turns source bytes of one format into a Manifest.
type Format interface {
	Name() string                                                     // "openapi", "postman", …
	Protocol() model.Protocol                                         // the protocol its manifests carry
	Detect(data []byte) bool                                          // cheap and deterministic
	Parse(data []byte, opts openapi.Options) (*model.Manifest, error) // runs in the isolate child
}

// Formats is the ordered registry. Order matters for detection: a more
// specific format (Postman) comes before a fallback (OpenAPI), and a
// pairwise test asserts no fixture is detected by two of them.
var Formats = []Format{postmanFormat{}, openapiFormat{}}

// Dispatch parses data with the format named by forceName, or, when it is
// empty, the first registered Format whose Detect matches.
func Dispatch(data []byte, forceName string, opts openapi.Options) (*model.Manifest, error) {
	if forceName != "" {
		for _, f := range Formats {
			if f.Name() == forceName {
				return f.Parse(data, opts)
			}
		}
		return nil, fmt.Errorf("%w: unknown format %q", openapi.ErrUnsupportedSpec, forceName)
	}
	for _, f := range Formats {
		if f.Detect(data) {
			return f.Parse(data, opts)
		}
	}
	return nil, fmt.Errorf("%w: unrecognized source format", openapi.ErrUnsupportedSpec)
}

type openapiFormat struct{}

func (openapiFormat) Name() string             { return "openapi" }
func (openapiFormat) Protocol() model.Protocol { return model.REST }
func (openapiFormat) Detect(data []byte) bool  { return !openapi.IsPostman(data) }
func (openapiFormat) Parse(data []byte, opts openapi.Options) (*model.Manifest, error) {
	return openapi.Parse(data, opts)
}

type postmanFormat struct{}

func (postmanFormat) Name() string             { return "postman" }
func (postmanFormat) Protocol() model.Protocol { return model.REST }
func (postmanFormat) Detect(data []byte) bool  { return openapi.IsPostman(data) }
func (postmanFormat) Parse(data []byte, opts openapi.Options) (*model.Manifest, error) {
	return openapi.ParsePostman(data, opts)
}
