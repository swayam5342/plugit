package graphql

import (
	"github.com/swayam5342/plugit/internal/ingest"
	"github.com/swayam5342/plugit/internal/model"
)

// SDLFormat is the graphql-sdl ingest.Format: a GraphQL schema in SDL.
type SDLFormat struct{}

// Name returns FormatSDL.
func (SDLFormat) Name() string { return FormatSDL }

// Protocol returns model.GraphQL.
func (SDLFormat) Protocol() model.Protocol { return model.GraphQL }

// Detect reports whether data is an SDL document (see DetectSDL).
func (SDLFormat) Detect(data []byte) bool { return DetectSDL(data) }

// Parse loads data as SDL and builds its manifest.
func (SDLFormat) Parse(data []byte, opts ingest.Options) (*model.Manifest, error) {
	return parse(data, FormatSDL, opts)
}

// IntrospectionFormat is the graphql-introspection ingest.Format: a JSON
// introspection result.
type IntrospectionFormat struct{}

// Name returns FormatIntrospection.
func (IntrospectionFormat) Name() string { return FormatIntrospection }

// Protocol returns model.GraphQL.
func (IntrospectionFormat) Protocol() model.Protocol { return model.GraphQL }

// Detect reports whether data is an introspection result (see DetectIntrospection).
func (IntrospectionFormat) Detect(data []byte) bool { return DetectIntrospection(data) }

// Parse loads data as an introspection result and builds its manifest.
func (IntrospectionFormat) Parse(data []byte, opts ingest.Options) (*model.Manifest, error) {
	return parse(data, FormatIntrospection, opts)
}

func parse(data []byte, format string, opts ingest.Options) (*model.Manifest, error) {
	s, err := LoadSchema(data, format)
	if err != nil {
		return nil, err
	}
	return Build(s, format, opts)
}
