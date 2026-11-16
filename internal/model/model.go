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

// Method returns the HTTP method part of k.
func (k OpKey) Method() string {
	m, _, _ := strings.Cut(string(k), " ")
	return m
}

// Path returns the path template part of k.
func (k OpKey) Path() string {
	_, p, _ := strings.Cut(string(k), " ")
	return p
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

// Argument locations.
const (
	InPath Location = iota
	InQuery
	InHeader
	InCookie
	InBody
	InBodyRoot
)

var locationNames = [...]string{
	InPath: "path", InQuery: "query", InHeader: "header", InCookie: "cookie", InBody: "body", InBodyRoot: "bodyRoot",
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

// Tool is one API operation exposed (or exposable) as an MCP tool.
type Tool struct {
	Key         OpKey    `json:"key"`
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Params      []Param  `json:"params,omitempty"`
	Body        *Body    `json:"body,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
	Safety      Safety   `json:"safety"`
	Issues      []Issue  `json:"issues,omitempty"`
}

// Param is one argument of a tool.
type Param struct {
	Name         string          `json:"name"`
	UpstreamName string          `json:"upstreamName"`
	In           Location        `json:"in"`
	Required     bool            `json:"required,omitempty"`
	Schema       json.RawMessage `json:"schema"`
	Desc         string          `json:"desc,omitempty"`
	Hidden       bool            `json:"hidden,omitempty"`
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
)

// Issue is a problem found while ingesting one tool.
type Issue struct {
	Kind   IssueKind `json:"kind"`
	Detail string    `json:"detail"`
}

// Manifest is the result of ingesting one spec.
type Manifest struct {
	Title       string   `json:"title"`
	Version     string   `json:"version"`
	SpecVersion string   `json:"specVersion"`
	Servers     []string `json:"servers,omitempty"`
	Tools       []Tool   `json:"tools"`
	Warnings    []string `json:"warnings,omitempty"`
}
