// Package openapi turns an OpenAPI 3.0/3.1 or Swagger 2.0 document into a
// model.Manifest (SPEC §3.4, §3.5).
//
// The spec is untrusted input (SPEC §5.3, T1/T2/T4). Defenses, each tested in
// this package and in testdata/hostile:
//
//   - only local "#/..." $refs resolve; libopenapi's file and remote lookups
//     are switched off, and a pre-pass over the parsed node tree (refcheck.go)
//     rejects ref chains deeper than MaxRefDepth and pure ref cycles without
//     relying on those switches;
//   - YAML alias expansion is limited (libopenapi's limit, never relaxed);
//   - schema conversion is capped at MaxSchemaDepth levels and
//     MaxSchemaNodes nodes per tool, degrading to {} with an Issue;
//   - all spec text that reaches the model is cleaned (naming.CleanText);
//   - libopenapi never logs to stdout, and its panics become errors.
package openapi

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel"
	"github.com/pb33f/libopenapi/utils"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/naming"
)

// Limits on untrusted specs.
const (
	MaxSchemaDepth = 12   // nesting levels kept when converting a schema
	MaxSchemaNodes = 5000 // schema nodes converted per tool
	MaxRefDepth    = 32   // $ref -> $ref hops
	maxWarnings    = 25
)

// Sentinel errors callers can match with errors.Is.
var (
	ErrUnsupportedSpec = errors.New("unsupported spec: need OpenAPI 3.0, 3.1 or Swagger 2.0")
	ErrRefTooDeep      = errors.New("$ref chain too deep")
	ErrRefCycle        = errors.New("$ref cycle")
	ErrParserPanic     = errors.New("spec parser failed")
	ErrInvalidSpec     = errors.New("invalid spec")
)

// Options configures Parse.
type Options struct {
	// Prefix is prepended to every tool name ("pets" -> "pets_listPets").
	Prefix string
	// MaxDescription caps descriptions in runes (naming.DefaultMaxDescription if 0).
	MaxDescription int
	// Logger receives libopenapi's diagnostics. nil discards them. Never
	// pass a logger that writes to stdout.
	Logger *slog.Logger
}

// Parse builds a Manifest from spec bytes (JSON or YAML).
func Parse(data []byte, opts Options) (m *model.Manifest, err error) {
	defer func() {
		if r := recover(); r != nil {
			m, err = nil, fmt.Errorf("%w: %v", ErrParserPanic, r)
		}
	}()
	if opts.MaxDescription <= 0 {
		opts.MaxDescription = naming.DefaultMaxDescription
	}
	if isPostman(data) {
		return parsePostman(data, opts)
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	cfg := datamodel.NewDocumentConfiguration()
	cfg.Logger = logger // the default logs to stdout
	cfg.AllowFileReferences = false
	cfg.AllowRemoteReferences = false
	cfg.SkipExternalRefResolution = true
	cfg.AllowExcessiveAliases = false
	cfg.BasePath = ""
	cfg.LocalFS = nil
	cfg.RemoteFS = nil

	doc, err := libopenapi.NewDocumentWithConfiguration(data, cfg)
	if err != nil {
		return nil, fmt.Errorf("parsing spec: %w", err)
	}
	defer doc.Release()

	info := doc.GetSpecInfo()
	if info == nil || info.RootNode == nil {
		return nil, fmt.Errorf("parsing spec: %w", ErrUnsupportedSpec)
	}
	b := &builder{opts: opts}
	switch {
	case info.SpecType == utils.OpenApi3 && (strings.HasPrefix(info.Version, "3.0") || strings.HasPrefix(info.Version, "3.1")):
		b.is30 = strings.HasPrefix(info.Version, "3.0")
	case info.SpecType == utils.OpenApi2 && info.Version == "2.0":
	default:
		return nil, fmt.Errorf("parsing spec: %w (got %q %q)", ErrUnsupportedSpec, info.SpecType, info.Version)
	}

	if info.SpecType == utils.OpenApi2 {
		if err := checkV2PathItems(info.RootNode); err != nil {
			return nil, fmt.Errorf("parsing spec: %w", err)
		}
	}
	external, err := checkRefs(info.RootNode)
	if err != nil {
		return nil, fmt.Errorf("parsing spec: %w", err)
	}
	if external > 0 {
		b.warn(fmt.Sprintf("%d external $ref(s) ignored: only local #/ references are resolved", external))
	}

	if info.SpecType == utils.OpenApi2 {
		v2, err := doc.BuildV2Model()
		if v2 == nil {
			return nil, fmt.Errorf("building Swagger 2.0 model: %w", errOrUnknown(err))
		}
		b.warnErr(err)
		b.fromV2(&v2.Model)
	} else {
		v3, err := doc.BuildV3Model()
		if v3 == nil {
			return nil, fmt.Errorf("building OpenAPI model: %w", errOrUnknown(err))
		}
		b.warnErr(err)
		b.fromV3(&v3.Model)
	}
	b.m.SpecVersion = info.Version
	return b.finish(), nil
}

func errOrUnknown(err error) error {
	if err == nil {
		return errors.New("unknown error")
	}
	return err
}
