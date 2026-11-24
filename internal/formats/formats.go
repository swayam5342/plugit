// Package formats assembles the ordered list of ingest.Format implementations.
// It sits above internal/ingest so ingest stays a pure interface package.
package formats

import (
	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/rest/openapi"
)

// Ordered returns the formats in detection order: a more specific format
// (Postman) before a fallback (OpenAPI). A pairwise test asserts no fixture is
// detected by two of them.
func Ordered() []ingest.Format {
	return []ingest.Format{postmanFormat{}, openapiFormat{}}
}

func restOptions(o ingest.Options) openapi.Options {
	return openapi.Options{
		Prefix:           o.Prefix,
		MaxDescription:   o.MaxDescription,
		AllowUnsafePaths: o.AllowUnsafePaths,
		Logger:           o.Logger,
	}
}

type openapiFormat struct{}

func (openapiFormat) Name() string             { return "openapi" }
func (openapiFormat) Protocol() model.Protocol { return model.REST }
func (openapiFormat) Detect(data []byte) bool  { return !openapi.IsPostman(data) }
func (openapiFormat) Parse(data []byte, opts ingest.Options) (*model.Manifest, error) {
	return openapi.Parse(data, restOptions(opts))
}

type postmanFormat struct{}

func (postmanFormat) Name() string             { return "postman" }
func (postmanFormat) Protocol() model.Protocol { return model.REST }
func (postmanFormat) Detect(data []byte) bool  { return openapi.IsPostman(data) }
func (postmanFormat) Parse(data []byte, opts ingest.Options) (*model.Manifest, error) {
	return openapi.ParsePostman(data, restOptions(opts))
}
