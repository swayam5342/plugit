package cli

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol"
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

// A REST manifest serves through internal/protocol/rest: the registry resolves
// the manifest's protocol to a rest.REST bound to the serve's target.
func TestServeSelectsProtocolImpl(t *testing.T) {
	tg := rest.Target{BaseURL: mustParseURL(t, "https://api.example.test")}
	p, err := resolveProto(model.REST, tg)
	if err != nil {
		t.Fatalf("resolveProto(REST): %v", err)
	}
	if p.Name() != model.REST {
		t.Errorf("Name() = %q, want %q", p.Name(), model.REST)
	}
	tl := model.Tool{Name: "getThing", HTTP: &model.HTTPOp{Method: "GET", Path: "/things"}}
	req, err := p.Encode(context.Background(), protocol.Call{Tool: tl, Endpoint: tg.BaseURL})
	if err != nil {
		t.Fatalf("Encode via the selected proto: %v", err)
	}
	if got := req.URL.String(); got != "https://api.example.test/things" {
		t.Errorf("request URL = %q", got)
	}
}

// A manifest naming a protocol with no registered impl is refused at startup
// with a "needs a newer plugit" message — never a silent fallback or a panic.
func TestServeUnknownProtocolRefused(t *testing.T) {
	_, err := resolveProto(model.Protocol("grpc"), rest.Target{})
	if err == nil {
		t.Fatal("an unregistered protocol must be refused at startup")
	}
	if !strings.Contains(err.Error(), "grpc") {
		t.Errorf("the refusal should name the protocol: %v", err)
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
