package invoke

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/jsonpick"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol"
	"github.com/swayam5342/plugit/internal/protocol/rest"
	"github.com/swayam5342/plugit/internal/redact"
)

var getThing = model.Tool{Key: "GET /things/{id}", Name: "getThing", HTTP: &model.HTTPOp{Method: "GET", Path: "/things/{id}"},
	Params: []model.Param{{Name: "id", Bind: model.Binding{Upstream: "id", In: model.InPath}, Required: true, Schema: []byte(`{"type":"string"}`)}}}

func deps(t *testing.T, srv *httptest.Server) (Deps, model.Tool) {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	cred := Credential{Type: "bearer", Secret: "test-token-123"}
	red := redact.New(redact.Options{Secrets: cred.SecretValues()})
	return Deps{Proto: rest.REST{Target: rest.Target{BaseURL: u}}, Doer: srv.Client(), Cred: cred, Endpoint: u, Redactor: red}, getThing
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
	d := Deps{Proto: rest.REST{Target: rest.Target{BaseURL: mustU("https://api.example.test")}}, Endpoint: mustU("https://api.example.test"), Redactor: redact.New(redact.Options{})}
	res := Perform(context.Background(), d, getThing, map[string]any{}, nil) // missing required path param
	if !res.IsError {
		t.Error("a missing path param should yield an isError result, not a panic")
	}
}

func mustU(s string) *url.URL { u, _ := url.Parse(s); return u }

type errDoer struct{}

func (errDoer) Do(*http.Request) (*http.Response, error) { return nil, errors.New("dial tcp: refused") }

func TestInvokeTransportErrorCode(t *testing.T) {
	d := Deps{Proto: rest.REST{Target: rest.Target{BaseURL: mustU("https://api.example.test")}}, Doer: errDoer{}, Endpoint: mustU("https://api.example.test"), Redactor: redact.New(redact.Options{})}
	res := Perform(context.Background(), d, getThing, map[string]any{"id": "42"}, nil)
	if !res.IsError || res.Code != "transport error" {
		t.Errorf("IsError=%v Code=%q, want transport error", res.IsError, res.Code)
	}
}

// offOriginProto encodes every request for a fixed URL regardless of the
// endpoint, as a buggy or hostile protocol might.
type offOriginProto struct {
	rest.REST
	to string
}

func (p offOriginProto) Encode(ctx context.Context, _ protocol.Call) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodGet, p.to, nil)
}

// recordingDoer records every request it is handed and answers 200 with an
// empty JSON object, so a request that slips past the origin guard succeeds
// instead of failing on DNS or TLS and masking the leak.
type recordingDoer struct{ reqs []*http.Request }

func (d *recordingDoer) Do(r *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, r)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
}

// T8: a credential is never sent to an origin other than the pinned endpoint,
// even if the protocol encoded a request for one. The doer must not be called.
func TestInvokeCredentialNotCrossOrigin(t *testing.T) {
	const secret = "test-token-123"
	cases := []struct {
		name string
		cred Credential
	}{
		{"bearer", Credential{Type: credBearer, Secret: secret}},
		{"header", Credential{Type: credHeader, Header: "X-Api-Key", Secret: secret}},
		{"query", Credential{Type: credQuery, Param: "api_key", Secret: secret}},
		{"basic", Credential{Type: credBasic, Secret: "user:" + secret}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := mustU("https://api.example.test")
			doer := &recordingDoer{}
			d := Deps{
				Proto:    offOriginProto{to: "https://evil.example.test/steal"},
				Doer:     doer,
				Cred:     tc.cred,
				Endpoint: endpoint,
				Redactor: redact.New(redact.Options{Secrets: tc.cred.SecretValues()}),
			}
			res := Perform(context.Background(), d, getThing, map[string]any{"id": "42"}, nil)

			for _, r := range doer.reqs {
				t.Errorf("credentialed request reached the doer: %s %s Authorization=%q X-Api-Key=%q api_key=%q",
					r.Method, r.URL.Host, r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"), r.URL.Query().Get("api_key"))
			}
			if !res.IsError {
				t.Errorf("IsError=false text=%q; a cross-origin request must be refused", res.Text)
			}
			if strings.Contains(res.Text, secret) {
				t.Errorf("secret leaked: %s", res.Text)
			}
		})
	}
}
