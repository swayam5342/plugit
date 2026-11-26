package rest

import (
	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/rest/openapi"
)

// restOptions maps the protocol-neutral ingest options onto the OpenAPI
// parser's.
func restOptions(o ingest.Options) openapi.Options {
	return openapi.Options{
		Prefix:           o.Prefix,
		MaxDescription:   o.MaxDescription,
		AllowUnsafePaths: o.AllowUnsafePaths,
		Logger:           o.Logger,
	}
}

// OpenapiFormat is the openapi ingest.Format: an OpenAPI 3.x or Swagger 2.0
// document.
type OpenapiFormat struct{}

// Name returns "openapi".
func (OpenapiFormat) Name() string { return "openapi" }

// Protocol returns model.REST.
func (OpenapiFormat) Protocol() model.Protocol { return model.REST }

// Detect reports whether data is an OpenAPI or Swagger document that is not
// a Postman collection.
func (OpenapiFormat) Detect(data []byte) bool {
	return openapi.IsOpenAPI(data) && !openapi.IsPostman(data)
}

// Parse loads data as OpenAPI and builds its manifest.
func (OpenapiFormat) Parse(data []byte, opts ingest.Options) (*model.Manifest, error) {
	return openapi.Parse(data, restOptions(opts))
}

// PostmanFormat is the postman ingest.Format: a Postman collection.
type PostmanFormat struct{}

// Name returns "postman".
func (PostmanFormat) Name() string { return "postman" }

// Protocol returns model.REST.
func (PostmanFormat) Protocol() model.Protocol { return model.REST }

// Detect reports whether data is a Postman collection.
func (PostmanFormat) Detect(data []byte) bool { return openapi.IsPostman(data) }

// Parse loads data as a Postman collection and builds its manifest.
func (PostmanFormat) Parse(data []byte, opts ingest.Options) (*model.Manifest, error) {
	return openapi.ParsePostman(data, restOptions(opts))
}
