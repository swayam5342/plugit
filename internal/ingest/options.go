package ingest

import "log/slog"

// Options is the protocol-neutral parse configuration handed to a Format. Each
// Format reads the subset that applies to it and ignores the rest.
type Options struct {
	// REST subset.
	Prefix           string
	MaxDescription   int
	AllowUnsafePaths bool

	// GraphQL subset (zero for REST).
	SelectionDepth int
	MaxFields      int
	// ResponseFields is toolset -> tool key -> selected response fields.
	ResponseFields map[string]map[string][]string

	// Logger receives parse diagnostics; it is not carried to the isolate child.
	Logger *slog.Logger
}
