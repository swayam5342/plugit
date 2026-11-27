package config

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
)

const graphqlConfig = `version: 1
name: gh
protocol: graphql
source: {url: https://example.test/schema.graphql}
endpoint: https://api.example.test/graphql
toolsets:
  default:
    tools:
      "query user": {}
`

// The graphql: section is optional; absent or zero fields mean the SPEC
// §3.6 defaults, and an absent section stays absent through MarshalAPI.
func TestGraphQLSectionParsed(t *testing.T) {
	c := mustParse(t, graphqlConfig)
	if c.GraphQL != nil {
		t.Errorf("absent section filled in: %+v", c.GraphQL)
	}
	if got := c.GraphQLSettings(); got != (GraphQLConfig{SelectionDepth: 2, MaxFields: 100}) {
		t.Errorf("defaults = %+v, want depth 2, max_fields 100", got)
	}

	c = mustParse(t, graphqlConfig+"graphql: {selection_depth: 4, max_fields: 50}\n")
	if got := c.GraphQLSettings(); got != (GraphQLConfig{SelectionDepth: 4, MaxFields: 50}) {
		t.Errorf("explicit = %+v", got)
	}
	c = mustParse(t, graphqlConfig+"graphql: {selection_depth: 3}\n")
	if got := c.GraphQLSettings(); got != (GraphQLConfig{SelectionDepth: 3, MaxFields: 100}) {
		t.Errorf("partial = %+v", got)
	}
	for _, edge := range []string{"{selection_depth: 1, max_fields: 1}", "{selection_depth: 12, max_fields: 1000}"} {
		mustParse(t, graphqlConfig+"graphql: "+edge+"\n")
	}

	// Round trips: a GraphQL config keeps its section, a REST one never
	// grows a graphql: key.
	c = mustParse(t, graphqlConfig+"graphql: {selection_depth: 4, max_fields: 50}\n")
	out, err := MarshalAPI(c)
	if err != nil {
		t.Fatal(err)
	}
	back := mustParse(t, string(out))
	if back.GraphQLSettings() != c.GraphQLSettings() || back.Protocol != "graphql" {
		t.Errorf("round trip lost the section:\n%s", out)
	}
	out, err = MarshalAPI(mustParse(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "graphql") {
		t.Errorf("REST config grew a graphql key:\n%s", out)
	}

	for name, sec := range map[string]string{
		"depth too deep":      "{selection_depth: 13}",
		"negative depth":      "{selection_depth: -1}",
		"too many fields":     "{max_fields: 1001}",
		"negative fields":     "{max_fields: -5}",
		"unknown graphql key": "{selection_dept: 3}",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAPI([]byte(graphqlConfig + "graphql: " + sec + "\n"))
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("err = %v, want ErrInvalidConfig", err)
			}
			want := "graphql."
			if name == "unknown graphql key" {
				want = "selection_dept"
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q should name %q", err, want)
			}
		})
	}
}

// Only the section matching protocol may appear (SPEC §3.6).
func TestSectionGatedByProtocol(t *testing.T) {
	_, err := ParseAPI([]byte(minimalConfig + "graphql: {selection_depth: 2}\n"))
	if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "graphql: only allowed with protocol: graphql") {
		t.Errorf("rest + graphql: err = %v", err)
	}
	explicit := strings.Replace(minimalConfig, "version: 1\n", "version: 1\nprotocol: rest\n", 1)
	if _, err := ParseAPI([]byte(explicit + "graphql: {}\n")); err == nil || !strings.Contains(err.Error(), "graphql:") {
		t.Errorf("explicit rest + empty graphql: err = %v", err)
	}
}

// REST-shaped settings are refused under protocol: graphql (P3 table).
func TestGraphQLConfigRejectsRESTShape(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"methods filter":  {graphqlConfig + "  reads:\n    methods: [GET]\n", "toolsets.reads.methods"},
		"fixed in cookie": {graphqlConfig + "fixed: [{name: session, in: cookie, value: 'literal:1'}]\n", "fixed[0].in"},
		"REST tool key":   {strings.Replace(graphqlConfig, `"query user"`, `"GET /user"`, 1), "GET /user"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAPI([]byte(tc.yaml))
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
	// The GraphQL fixed locations are accepted.
	mustParse(t, graphqlConfig+"fixed: [{name: org, in: variable, value: 'literal:1'}, {name: X-Org, in: header, value: 'literal:1'}]\n")
}

// source.format, when set, names a registered format of the protocol.
func TestSourceFormatMustMatchProtocol(t *testing.T) {
	withFormat := func(base, f string) string {
		return strings.Replace(base, "source: {url: ", "source: {format: "+f+", url: ", 1)
	}
	for _, ok := range []string{withFormat(minimalConfig, "openapi"), withFormat(minimalConfig, "postman"),
		withFormat(graphqlConfig, "graphql-sdl"), withFormat(graphqlConfig, "graphql-introspection")} {
		mustParse(t, ok)
	}
	for name, src := range map[string]string{
		"graphql format under rest": withFormat(minimalConfig, "graphql-sdl"),
		"rest format under graphql": withFormat(graphqlConfig, "openapi"),
		"unknown format":            withFormat(minimalConfig, "wsdl"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAPI([]byte(src))
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "source.format") {
				t.Errorf("err = %v, want one naming source.format", err)
			}
		})
	}
}

// knownAuthTypes is exactly the set of auth types the P3 table allows under
// any protocol, and the AuthType constants: a type added to one but not the
// other would read as "needs a newer plugit" or slip past validation.
func TestKnownAuthTypesMatchTable(t *testing.T) {
	table := map[string]bool{}
	for _, p := range model.Protocols() {
		for _, at := range model.AuthTypes(p) {
			table[at] = true
		}
	}
	known := map[string]bool{}
	for _, at := range knownAuthTypes {
		known[string(at)] = true
	}
	if !maps.Equal(table, known) {
		t.Errorf("knownAuthTypes = %v, but the P3 table allows %v", known, table)
	}
	consts := []AuthType{AuthNone, AuthBearer, AuthHeader, AuthQuery, AuthBasic, AuthOAuth2}
	if !slices.Equal(consts, knownAuthTypes) {
		t.Errorf("knownAuthTypes = %v, want the AuthType constants %v", knownAuthTypes, consts)
	}
	// The newer-plugit message lists every known type.
	err := checkAuthType(model.REST, "mtls")
	for _, at := range knownAuthTypes {
		if err == nil || !strings.Contains(err.Error(), string(at)) {
			t.Errorf("message %v does not list %s", err, at)
		}
	}
}

// A null graphql: section under protocol rest decodes to no section, so it
// is accepted and has no effect.
func TestNullGraphQLSectionUnderREST(t *testing.T) {
	c := mustParse(t, minimalConfig+"graphql: null\n")
	if c.GraphQL != nil {
		t.Errorf("GraphQL = %+v, want nil", c.GraphQL)
	}
}

// A graphql range error says that 0 (or leaving it out) means the default.
func TestGraphQLRangeMessage(t *testing.T) {
	for sec, want := range map[string]string{
		"{selection_depth: 13}": "graphql.selection_depth: must be between 0 and 12 (0 or unset means the default, 2)",
		"{max_fields: 1001}":    "graphql.max_fields: must be between 0 and 1000 (0 or unset means the default, 100)",
	} {
		_, err := ParseAPI([]byte(graphqlConfig + "graphql: " + sec + "\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to contain %q", sec, err, want)
		}
	}
}

// An auth.type no protocol knows is a newer plugit's (SPEC §8.1.2 rule 4).
func TestAuthTypeUnknownValue(t *testing.T) {
	for _, base := range []string{minimalConfig, graphqlConfig} {
		_, err := ParseAPI([]byte(base + "auth: {type: mtls, secret: env:X}\n"))
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "auth.type") ||
			!strings.Contains(err.Error(), "needs a newer plugit") {
			t.Errorf("err = %v", err)
		}
	}
}

// A known auth.type the protocol's P3 row forbids gets its own message. No
// M8 config reaches it (grpc has no serving implementation), so the helper
// is called directly.
func TestAuthTypeNotAllowedForProtocol(t *testing.T) {
	// The message leaves the field name to the caller, which prefixes it.
	err := checkAuthType(model.GRPC, "query")
	if err == nil || err.Error() != `"query" isn't available for protocol "grpc"` {
		t.Errorf("err = %v", err)
	}
	if err := checkAuthType(model.GRPC, "bearer"); err != nil {
		t.Errorf("bearer for grpc: %v", err)
	}
	for _, at := range []string{"none", "bearer", "header", "query", "basic", "oauth2"} {
		if err := checkAuthType(model.GraphQL, at); err != nil {
			t.Errorf("%s for graphql: %v", at, err)
		}
	}
	if err := checkAuthType(model.REST, "mtls"); err == nil || !strings.Contains(err.Error(), "needs a newer plugit") {
		t.Errorf("unknown type: %v", err)
	}
}
