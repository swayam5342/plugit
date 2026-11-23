package model

import (
	"slices"
	"strings"
)

// Protocol is an API protocol PlugIt speaks, stored and compared as its
// string name so it survives JSON/YAML round-trips (SPEC §3.4 P2).
type Protocol string

// Protocols. "a2a" is reserved in the OpKey grammar but not a value here.
const (
	REST    Protocol = "rest"
	GraphQL Protocol = "graphql"
	GRPC    Protocol = "grpc"
)

// protoRules is the pure P3 table (SPEC §3.4): one row per protocol drives
// key, binding, fixed-location, auth-type and toolset-filter validation, so
// no other package switches on protocol for these rules.
type protoRules struct {
	keyWords      []string // reserved first words of a non-REST OpKey; nil means REST "METHOD /path"
	bindings      []string // allowed Binding.In values
	fixedIn       []string // allowed fixed[].in values
	authTypes     []string // allowed auth.type values
	methodsFilter bool     // whether toolsets may use methods:
	section       string   // the protocol's config section name ("" for REST)
}

var protoTable = map[Protocol]protoRules{
	REST: {
		keyWords:      nil,
		bindings:      []string{"path", "query", "header", "cookie", "body", "bodyRoot"},
		fixedIn:       []string{"query", "header", "cookie"},
		authTypes:     []string{"none", "bearer", "header", "query", "basic", "oauth2"},
		methodsFilter: true,
		section:       "",
	},
	GraphQL: {
		keyWords:      []string{"query", "mutation"},
		bindings:      []string{"variable"},
		fixedIn:       []string{"variable", "header"},
		authTypes:     []string{"none", "bearer", "header", "query", "basic", "oauth2"},
		methodsFilter: false,
		section:       "graphql",
	},
	GRPC: {
		keyWords:      []string{"grpc"},
		bindings:      []string{"field", "metadata"},
		fixedIn:       []string{"field", "metadata"},
		authTypes:     []string{"none", "bearer", "header", "basic", "oauth2"},
		methodsFilter: false,
		section:       "grpc",
	},
}

// KnownProtocol reports whether p is a protocol the P3 table defines.
func KnownProtocol(p Protocol) bool {
	_, ok := protoTable[p]
	return ok
}

// KeyValid reports whether k is a well-formed OpKey for protocol p (SPEC
// §8.1.4 grammar). REST is "METHOD /path"; every other protocol is
// "<word> <rest>" where <word> is one of that protocol's reserved words.
func KeyValid(p Protocol, k OpKey) bool {
	r, ok := protoTable[p]
	if !ok {
		return false
	}
	word, rest, found := strings.Cut(string(k), " ")
	if !found || word == "" || rest == "" {
		return false
	}
	if p == REST {
		return word == strings.ToUpper(word) && strings.HasPrefix(rest, "/")
	}
	return slices.Contains(r.keyWords, word)
}

// BindingAllowed reports whether in is a valid Binding.In location for p.
func BindingAllowed(p Protocol, in string) bool { return slices.Contains(protoTable[p].bindings, in) }

// FixedInAllowed reports whether in is a valid fixed[].in location for p.
func FixedInAllowed(p Protocol, in string) bool { return slices.Contains(protoTable[p].fixedIn, in) }

// AuthTypeAllowed reports whether t is an auth.type p accepts.
func AuthTypeAllowed(p Protocol, t string) bool { return slices.Contains(protoTable[p].authTypes, t) }

// MethodsFilterAllowed reports whether toolsets for p may use the methods: filter.
func MethodsFilterAllowed(p Protocol) bool { return protoTable[p].methodsFilter }

// ConfigSection is the plugit.yaml section name for p ("" for REST).
func ConfigSection(p Protocol) string { return protoTable[p].section }

// IdempotentForMethod reports whether an HTTP method is idempotent (SPEC
// §3.5 P6): GET, HEAD, OPTIONS, PUT, DELETE.
func IdempotentForMethod(method string) bool {
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "OPTIONS", "PUT", "DELETE":
		return true
	default:
		return false
	}
}
