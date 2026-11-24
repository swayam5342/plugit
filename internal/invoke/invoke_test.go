package invoke

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/jsonpick"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/rest"
	"github.com/swayam5342/plugit/internal/proxy"
	"github.com/swayam5342/plugit/internal/redact"
)

var getThing = model.Tool{Key: "GET /things/{id}", Name: "getThing", HTTP: &model.HTTPOp{Method: "GET", Path: "/things/{id}"},
	Params: []model.Param{{Name: "id", Bind: model.Binding{Upstream: "id", In: model.InPath}, Required: true, Schema: []byte(`{"type":"string"}`)}}}

func deps(t *testing.T, srv *httptest.Server) (Deps, model.Tool) {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	tg := proxy.Target{BaseURL: u, Auth: proxy.Auth{Type: proxy.AuthBearer, Secret: "test-token-123"}}
	red := redact.New(redact.Options{Secrets: tg.SecretValues()})
	return Deps{Proto: rest.REST{Target: tg}, Doer: srv.Client(), Target: tg, Endpoint: u, Redactor: red}, getThing
}

func TestInvokeREST(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		// Echo the secret back to prove value-redaction covers the body.
		_, _ = w.Write([]byte(`{"id":"42","token":"test-token-123","extra":"x"}`))
	}))
	defer srv.Close()

	d, tool := deps(t, srv)
	res := Perform(context.Background(), d, tool, map[string]any{"id": "42"}, nil)

	if gotAuth != "Bearer test-token-123" {
		t.Errorf("invoke did not attach the credential (server saw %q)", gotAuth)
	}
	if res.IsError || res.Code != "HTTP 200" {
		t.Errorf("IsError=%v Code=%q", res.IsError, res.Code)
	}
	if strings.Contains(res.Text, "test-token-123") {
		t.Errorf("secret leaked into the result: %s", res.Text)
	}
	if !strings.Contains(res.Text, `"id": "42"`) {
		t.Errorf("response body missing from result: %s", res.Text)
	}
}

func TestInvokeResponseFieldsFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"42","extra":"drop-me"}`))
	}))
	defer srv.Close()
	pick, err := jsonpick.Parse([]string{"id"})
	if err != nil {
		t.Fatal(err)
	}
	d, tool := deps(t, srv)
	res := Perform(context.Background(), d, tool, map[string]any{"id": "42"}, pick)
	if strings.Contains(res.Text, "drop-me") {
		t.Errorf("response_fields did not trim 'extra': %s", res.Text)
	}
}

func TestInvokeBadArgsIsError(t *testing.T) {
	d := Deps{Proto: rest.REST{Target: proxy.Target{BaseURL: mustU("https://api.example.test")}}, Endpoint: mustU("https://api.example.test"), Redactor: redact.New(redact.Options{})}
	res := Perform(context.Background(), d, getThing, map[string]any{}, nil) // missing required path param
	if !res.IsError {
		t.Error("a missing path param should yield an isError result, not a panic")
	}
}

func mustU(s string) *url.URL { u, _ := url.Parse(s); return u }

type errDoer struct{}

func (errDoer) Do(*http.Request) (*http.Response, error) { return nil, errors.New("dial tcp: refused") }

func TestInvokeTransportErrorCode(t *testing.T) {
	d := Deps{Proto: rest.REST{Target: proxy.Target{BaseURL: mustU("https://api.example.test")}}, Doer: errDoer{}, Endpoint: mustU("https://api.example.test"), Redactor: redact.New(redact.Options{})}
	res := Perform(context.Background(), d, getThing, map[string]any{"id": "42"}, nil)
	if !res.IsError || res.Code != "transport error" {
		t.Errorf("IsError=%v Code=%q, want transport error", res.IsError, res.Code)
	}
}
