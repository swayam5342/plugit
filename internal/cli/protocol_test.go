package cli

import (
	"context"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol"
	"github.com/swayam5342/plugit/internal/protocol/graphql"
	"github.com/swayam5342/plugit/internal/protocol/rest"
)

func mustParseURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// A REST manifest serves through internal/protocol/rest: resolveProto builds a
// rest.REST bound to the serve's fixed params and pinned base URL.
func TestServeSelectsProtocolImpl(t *testing.T) {
	base := mustParseURL(t, "https://api.example.test")
	for _, name := range []model.Protocol{model.REST, ""} { // "" is a pre-field manifest: REST
		p, err := resolveProto(name, resolvedFixed{}, base)
		if err != nil {
			t.Fatalf("resolveProto(%q): %v", name, err)
		}
		if p.Name() != model.REST {
			t.Errorf("resolveProto(%q).Name() = %q, want %q", name, p.Name(), model.REST)
		}
		tl := model.Tool{Name: "getThing", HTTP: &model.HTTPOp{Method: "GET", Path: "/things"}}
		req, err := p.Encode(context.Background(), protocol.Call{Tool: tl, Endpoint: base})
		if err != nil {
			t.Fatalf("Encode via the selected proto: %v", err)
		}
		if got := req.URL.String(); got != "https://api.example.test/things" {
			t.Errorf("request URL = %q", got)
		}
	}
}

// resolveProto binds the REST target from the resolved fixed params: the
// config location string maps to the REST location, secrets and the user
// agent carry over.
func TestResolveProtoBindsRESTTarget(t *testing.T) {
	base := mustParseURL(t, "https://api.example.test")
	fx := resolvedFixed{userAgent: "plugit/test", items: []fixedItem{
		{Name: "tenant", In: "header", Value: "acme"},
		{Name: "sid", In: "cookie", Value: "test-token-123", Secret: true},
		{Name: "v", In: "", Value: "2"},
	}}
	p, err := resolveProto(model.REST, fx, base)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := p.(rest.REST)
	if !ok {
		t.Fatalf("resolveProto(REST) = %T, want rest.REST", p)
	}
	want := rest.Target{BaseURL: base, UserAgent: "plugit/test", Fixed: []rest.Fixed{
		{Name: "tenant", In: model.InHeader, Value: "acme"},
		{Name: "sid", In: model.InCookie, Value: "test-token-123", Secret: true},
		{Name: "v", In: model.InQuery, Value: "2"},
	}}
	if !reflect.DeepEqual(r.Target, want) {
		t.Errorf("REST target = %+v, want %+v", r.Target, want)
	}
}

// resolveProto builds GraphQL directly, mapping fixed[].in to GraphQL's two
// locations: "header" is a header, "variable" (or unset) is a variable;
// GraphQL has no query location.
func TestResolveProtoBindsGraphQLTarget(t *testing.T) {
	fx := resolvedFixed{userAgent: "plugit/test", items: []fixedItem{
		{Name: "X-Tenant", In: "header", Value: "acme"},
		{Name: "org", In: "variable", Value: "test-token-123", Secret: true},
		{Name: "first", In: "", Value: "10"},
	}}
	p, err := resolveProto(model.GraphQL, fx, mustParseURL(t, "https://api.example.test/graphql"))
	if err != nil {
		t.Fatal(err)
	}
	g, ok := p.(graphql.GraphQL)
	if !ok {
		t.Fatalf("resolveProto(GraphQL) = %T, want graphql.GraphQL", p)
	}
	want := graphql.Target{UserAgent: "plugit/test", Fixed: []graphql.Fixed{
		{Name: "X-Tenant", In: model.InHeader, Value: "acme"},
		{Name: "org", In: model.InVariable, Value: "test-token-123", Secret: true},
		{Name: "first", In: model.InVariable, Value: "10"},
	}}
	if !reflect.DeepEqual(g.Target, want) {
		t.Errorf("GraphQL target = %+v, want %+v", g.Target, want)
	}
}

// The redaction list for a serve's fixed params is protocol-neutral: every
// non-empty secret value, in config order, and nothing else.
func TestResolvedFixedSecretValues(t *testing.T) {
	fx := resolvedFixed{items: []fixedItem{
		{Name: "a", Value: "test-token-123", Secret: true},
		{Name: "b", Value: "public"},
		{Name: "c", Value: "", Secret: true},
		{Name: "d", Value: "test-token-456", Secret: true},
	}}
	got := fx.secretValues()
	want := []string{"test-token-123", "test-token-456"}
	if !slices.Equal(got, want) {
		t.Errorf("secretValues() = %q, want %q", got, want)
	}
	// It agrees with the REST target's own list, so REST redaction is unchanged.
	if rv := restTargetFrom(fx, nil).SecretValues(); !slices.Equal(got, rv) {
		t.Errorf("secretValues() = %q, but the REST target redacts %q", got, rv)
	}
}

// A manifest naming a protocol this build has no impl for is refused at
// startup with a "needs a newer plugit" message: never a silent fallback or a
// panic.
func TestServeUnknownProtocolRefused(t *testing.T) {
	for _, name := range []model.Protocol{model.GRPC, "bogus"} {
		_, err := resolveProto(name, resolvedFixed{}, mustParseURL(t, "https://api.example.test"))
		if err == nil {
			t.Fatalf("resolveProto(%q) must be refused at startup", name)
		}
		if !strings.Contains(err.Error(), string(name)) || !strings.Contains(err.Error(), newerPlugit) {
			t.Errorf("the refusal should name %q and say it needs a newer plugit: %v", name, err)
		}
	}
}

// newerPlugit is the phrase every refusal of an unbuilt protocol carries.
const newerPlugit = "may need a newer version of plugit"

// Every protocol in the P3 table is handled explicitly by BOTH switches in
// protocol.go: built or refused, never silently fallen through.
//
// How this bites: the test's own expectation table must list exactly the
// protocols model.Protocols() returns, so adding a row to the P3 table fails
// here until someone decides "built" or "refused" for it. A "built" protocol
// then fails unless resolveProto returns an impl with that Name() (the switch's
// default case returns the "newer plugit" error instead) AND describeFor
// returns a non-empty badge for a matching tool (its default returns ""). A
// "refused" protocol fails if either switch quietly starts handling it.
func TestProtocolSwitchesExhaustive(t *testing.T) {
	type expect struct {
		built bool
		tool  model.Tool // a tool of this protocol
	}
	cases := map[model.Protocol]expect{
		model.REST: {built: true, tool: model.Tool{Key: "GET /pets",
			HTTP: &model.HTTPOp{Method: "GET", Path: "/pets"}}},
		model.GraphQL: {built: true, tool: model.Tool{Key: "query user",
			GraphQL: &model.GraphQLOp{Operation: "query", Field: "user"}}},
		model.GRPC: {built: false, tool: model.Tool{Key: "grpc acme.v1.Issues/Get",
			GRPC: &model.GRPCOp{Service: "acme.v1.Issues", Method: "Get"}}},
	}

	known := model.Protocols()
	if len(known) == 0 {
		t.Fatal("model.Protocols() is empty")
	}
	for _, name := range known {
		if _, ok := cases[name]; !ok {
			t.Errorf("protocol %q is in the P3 table but this test doesn't say whether resolveProto/describeFor build or refuse it: add a case to both switches in protocol.go and to this table", name)
		}
	}
	for name := range cases {
		if !slices.Contains(known, name) {
			t.Errorf("this test lists %q but the P3 table doesn't define it", name)
		}
	}

	base := mustParseURL(t, "https://api.example.test")
	for _, name := range known {
		c, ok := cases[name]
		if !ok {
			continue
		}
		t.Run(string(name), func(t *testing.T) {
			p, err := resolveProto(name, resolvedFixed{}, base)
			badge, target := describeFor(name, c.tool)
			if c.built {
				if err != nil {
					t.Fatalf("resolveProto(%q) should build an impl: %v", name, err)
				}
				if p.Name() != name {
					t.Errorf("resolveProto(%q).Name() = %q", name, p.Name())
				}
				if badge == "" {
					t.Errorf("describeFor(%q) has no case for it: badge empty (target %q)", name, target)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), newerPlugit) {
				t.Errorf("resolveProto(%q) should refuse with %q, got %v, %v", name, newerPlugit, p, err)
			}
			if badge != "" || target != string(c.tool.Key) {
				t.Errorf("describeFor(%q) = %q %q, want the fallback %q %q", name, badge, target, "", c.tool.Key)
			}
		})
	}
}

// describeFor gives each built protocol its own badge and target; an empty
// name is a pre-field manifest (REST), and an unknown one falls back to the key.
func TestDescribeFor(t *testing.T) {
	restTool := model.Tool{Key: "GET /pets/{id}", HTTP: &model.HTTPOp{Method: "GET", Path: "/pets/{id}"}}
	gqlTool := model.Tool{Key: "mutation createUser", GraphQL: &model.GraphQLOp{Operation: "mutation", Field: "createUser"}}
	tests := []struct {
		name          model.Protocol
		tool          model.Tool
		badge, target string
	}{
		{model.REST, restTool, "GET", "/pets/{id}"},
		{"", restTool, "GET", "/pets/{id}"},
		{model.GraphQL, gqlTool, "mutation", "createUser"},
		{"bogus", gqlTool, "", "mutation createUser"},
	}
	for _, tt := range tests {
		t.Run(string(tt.name), func(t *testing.T) {
			b, tg := describeFor(tt.name, tt.tool)
			if b != tt.badge || tg != tt.target {
				t.Errorf("describeFor(%q) = %q %q, want %q %q", tt.name, b, tg, tt.badge, tt.target)
			}
		})
	}
}

// A config whose protocol disagrees with the manifest it is paired with (a hand
// edit, or a sync whose source changed format) is refused at load, naming both
// protocols and pointing at `plugit sync` — never silently reinterpreted.
func TestProtocolMustMatchManifest(t *testing.T) {
	cfg := &config.API{Name: "pets", Protocol: "graphql"}
	m := &model.Manifest{Protocol: model.REST}
	if _, err := checkProtocol(cfg, m); err == nil {
		t.Fatal("a config/manifest protocol mismatch must be refused")
	} else {
		for _, want := range []string{"graphql", string(model.REST), "plugit sync"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal should mention %q: %v", want, err)
			}
		}
	}
	// The matching case is accepted.
	if _, err := checkProtocol(&config.API{Name: "pets", Protocol: string(model.REST)}, &model.Manifest{Protocol: model.REST}); err != nil {
		t.Errorf("matching protocols should load: %v", err)
	}
}
