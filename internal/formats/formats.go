// Package formats assembles the ordered list of ingest.Format implementations.
// It sits above internal/ingest so ingest stays a pure interface package.
package formats

import (
	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
	"github.com/swayam5342/plugit/internal/protocol/rest"
)

// Ordered returns the formats in detection order, assembled here rather than
// registered from init(): JSON formats first, the more specific before the
// broader. The order is not the guard: each Detect is narrow (OpenAPI needs a
// top-level openapi or swagger key), and a pairwise test asserts no fixture
// is detected by two formats (SPEC P4).
func Ordered() []ingest.Format {
	return []ingest.Format{
		rest.PostmanFormat{},
		rest.OpenapiFormat{},
		graphql.IntrospectionFormat{},
		graphql.SDLFormat{},
	}
}
