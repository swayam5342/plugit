// Package model is PlugIt's canonical intermediate representation of an API
// (SPEC §3.4). Ingesters produce a Manifest; curation, lint, token
// estimation and serving consume it. The package is pure: no I/O.
package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OpKey is a tool's stable identity across renames and syncs:
// "METHOD /path/template", e.g. "GET /repos/{owner}/{repo}/issues".
type OpKey string

// NewOpKey builds an OpKey with an upper-case method.
func NewOpKey(method, path string) OpKey {
	return OpKey(strings.ToUpper(method) + " " + path)
}

// Method returns the HTTP method part of a REST key, or "" for a non-REST
// key (callers use Tool.HTTP once the protocol is known).
func (k OpKey) Method() string {
	m, p, ok := strings.Cut(string(k), " ")
	if !ok || !isRESTForm(m, p) {
		return ""
	}
	return m
}

// Path returns the path-template part of a REST key, or "" for a non-REST key.
func (k OpKey) Path() string {
	m, p, ok := strings.Cut(string(k), " ")
	if !ok || !isRESTForm(m, p) {
		return ""
	}
	return p
}

// isRESTForm reports whether "<m> <p>" is a REST key: an upper-case method
// and a path starting with "/" (SPEC §8.1.4).
func isRESTForm(m, p string) bool {
	return m != "" && m == strings.ToUpper(m) && strings.HasPrefix(p, "/")
}

// Safety classifies what calling a tool can do.
type Safety int

// Safety levels, from least to most dangerous.
const (
	ReadOnly Safety = iota
	Write
	Destructive
)

// SafetyForMethod classifies an HTTP method. Unknown methods are treated as
// Destructive so that they fail closed under every policy mode.
func SafetyForMethod(method string) Safety {
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "OPTIONS":
		return ReadOnly
	case "POST", "PUT", "PATCH":
		return Write
	default:
		return Destructive
	}
}

var safetyNames = [...]string{ReadOnly: "read-only", Write: "write", Destructive: "destructive"}

// ParseSafety parses a Safety name as produced by Safety.String.
func ParseSafety(s string) (Safety, bool) {
	for i, n := range safetyNames {
		if n == s {
			return Safety(i), true
		}
	}
	return 0, false
}

func (s Safety) String() string {
	if s >= 0 && int(s) < len(safetyNames) {
		return safetyNames[s]
	}
	return fmt.Sprintf("Safety(%d)", int(s))
}

// MarshalJSON encodes s as its name.
func (s Safety) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// UnmarshalJSON decodes a name produced by MarshalJSON.
func (s *Safety) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err != nil {
		return err
	}
	for i, n := range safetyNames {
		if n == name {
			*s = Safety(i)
			return nil
		}
	}
	return fmt.Errorf("unknown safety %q", name)
}

// Location is where an argument is sent in the HTTP request.
type Location int

// Argument locations. The first six are REST; the rest belong to other
// protocols (SPEC §3.4 P3 Binding set).
const (
	InPath Location = iota
	InQuery
	InHeader
	InCookie
	InBody
	InBodyRoot
	InVariable // GraphQL
	InField    // gRPC
	InMetadata // gRPC
)

var locationNames = [...]string{
	InPath: "path", InQuery: "query", InHeader: "header", InCookie: "cookie", InBody: "body", InBodyRoot: "bodyRoot",
	InVariable: "variable", InField: "field", InMetadata: "metadata",
}

func (l Location) String() string {
	if l >= 0 && int(l) < len(locationNames) {
		return locationNames[l]
	}
	return fmt.Sprintf("Location(%d)", int(l))
}

// MarshalJSON encodes l as its name.
func (l Location) MarshalJSON() ([]byte, error) { return json.Marshal(l.String()) }

// UnmarshalJSON decodes a name produced by MarshalJSON.
func (l *Location) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err != nil {
		return err
	}
	loc, ok := ParseLocation(name)
	if !ok {
		return fmt.Errorf("unknown location %q", name)
	}
	*l = loc
	return nil
}

// ParseLocation parses a location name as produced by Location.String.
func ParseLocation(s string) (Location, bool) {
	for i, n := range locationNames {
		if n == s {
			return Location(i), true
		}
	}
	return 0, false
}

// Tool is one API operation exposed (or exposable) as an MCP tool. The
// fields above the line are shared by every protocol; exactly one of HTTP,
// GraphQL, GRPC is set, matching Manifest.Protocol (SPEC §3.4 P2).
type Tool struct {
	Key         OpKey    `json:"key"`
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Params      []Param  `json:"params,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
	Safety      Safety   `json:"safety"`
	Idempotent  bool     `json:"idempotent,omitempty"`
	Issues      []Issue  `json:"issues,omitempty"`

	HTTP    *HTTPOp    `json:"http,omitempty"`
	GraphQL *GraphQLOp `json:"graphql,omitempty"`
	GRPC    *GRPCOp    `json:"grpc,omitempty"`
}

// HTTPOp is everything REST-specific about a tool.
type HTTPOp struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   *Body  `json:"body,omitempty"`
}

// GraphQLOp is everything GraphQL-specific about a tool (M8).
type GraphQLOp struct {
	Operation string   `json:"operation"`
	Field     string   `json:"field"`
	Document  string   `json:"document"`
	Selection []string `json:"selection,omitempty"`
}

// GRPCOp is everything gRPC-specific about a tool (M11).
type GRPCOp struct {
	Service string `json:"service"`
	Method  string `json:"method"`
	Input   string `json:"input"`
	Output  string `json:"output"`
}

// Param is one argument of a tool.
type Param struct {
	Name     string          `json:"name"`
	Bind     Binding         `json:"bind"`
	Required bool            `json:"required,omitempty"`
	Schema   json.RawMessage `json:"schema"`
	Desc     string          `json:"desc,omitempty"`
	Hidden   bool            `json:"hidden,omitempty"`
}

// Binding is where a param's value goes on the wire; never guessed at call
// time (SPEC §3.4). In is a validated location from the per-protocol set.
type Binding struct {
	In       Location `json:"in"`
	Upstream string   `json:"upstream"`
}

// Body is a tool's request body.
type Body struct {
	ContentType string              `json:"contentType"`
	Schema      json.RawMessage     `json:"schema"`
	Required    bool                `json:"required,omitempty"`
	Encoding    map[string]Encoding `json:"encoding,omitempty"`
}

// Encoding is how one form field is serialized (an OpenAPI encoding
// object, with explode resolved). Fields without one use style form,
// explode true.
type Encoding struct {
	Style   string `json:"style,omitempty"`
	Explode bool   `json:"explode"`
}

// IssueKind classifies an ingest problem. Lint rules map these to findings.
type IssueKind string

// Ingest issue kinds.
const (
	IssueSchemaDegraded IssueKind = "schema-degraded"
	IssueBodyNested     IssueKind = "body-nested"
	IssueUnsupported    IssueKind = "unsupported"
	IssueHiddenText     IssueKind = "hidden-text"
	// IssueSelectionTruncated: a GraphQL default selection hit
	// graphql.max_fields (lint L020).
	IssueSelectionTruncated IssueKind = "selection-truncated"
)

// Issue is a problem found while ingesting one tool.
type Issue struct {
	Kind   IssueKind `json:"kind"`
	Detail string    `json:"detail"`
}

// Manifest is the result of ingesting one source (SPEC §3.4 P2).
type Manifest struct {
	Protocol    Protocol       `json:"protocol"`
	Format      string         `json:"format,omitempty"`
	Title       string         `json:"title"`
	Version     string         `json:"version"`
	SpecVersion string         `json:"specVersion"`
	Endpoints   []string       `json:"endpoints,omitempty"`
	Tools       []Tool         `json:"tools"`
	Warnings    []string       `json:"warnings,omitempty"`
	GraphQL     *GraphQLSchema `json:"graphql,omitempty"`
	GRPC        *GRPCSchema    `json:"grpc,omitempty"`
}

// GraphQLSchema is the derived GraphQL data serve and sync need, cached in
// manifest.json so serve never parses a schema (SPEC §3.6, no migration).
// NormalizedSDL is kept for sync re-validation in the child; OverrideDocs
// maps toolset -> tool key -> the operation document compiled from that
// toolset's response_fields.
type GraphQLSchema struct {
	NormalizedSDL string                       `json:"normalizedSDL,omitempty"`
	OverrideDocs  map[string]map[string]string `json:"overrideDocs,omitempty"`
}

// GRPCSchema holds the trimmed descriptor set gRPC tools need (M11); an
// empty placeholder until then.
type GRPCSchema struct{}
