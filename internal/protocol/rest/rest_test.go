package rest

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

var getThing = model.Tool{Key: "GET /things/{id}", Name: "getThing", HTTP: &model.HTTPOp{Method: "GET", Path: "/things/{id}"},
	Params: []model.Param{{Name: "id", Bind: model.Binding{Upstream: "id", In: model.InPath}, Required: true, Schema: []byte(`{"type":"string"}`)}}}

func TestRESTEncodeNoCredentials(t *testing.T) {
	r := REST{Target: Target{Auth: Auth{Type: AuthBearer, Secret: "test-token-123"}}}
	req, err := r.Encode(context.Background(), protocol.Call{
		Tool: getThing, Args: map[string]any{"id": "42"}, Endpoint: mustURL(t, "https://api.example.test"),
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Encode attached a credential (%q); that is invoke's job, not the protocol's", got)
	}
	if req.URL.String() != "https://api.example.test/things/42" {
		t.Errorf("URL = %q", req.URL.String())
	}
}

func TestRESTDecodeCodeAndValue(t *testing.T) {
	r := REST{}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}}
	body := []byte(`{"id":"42","n":9007199254740993}`)
	res := r.Decode(protocol.Call{Tool: getThing}, resp, body, false)
	if res.IsError || res.Code != "HTTP 200" {
		t.Errorf("ok decode: IsError=%v Code=%q", res.IsError, res.Code)
	}
	if string(res.Value) != string(body) {
		t.Errorf("Value = %s, want the raw body unchanged (numbers preserved)", res.Value)
	}

	eresp := &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{"Content-Type": {"application/json"}}}
	eres := r.Decode(protocol.Call{Tool: getThing}, eresp, []byte(`{"error":"nope"}`), false)
	if !eres.IsError || eres.Code != "HTTP 404" {
		t.Errorf("error decode: IsError=%v Code=%q", eres.IsError, eres.Code)
	}
}

func TestRESTDescribe(t *testing.T) {
	badge, target := REST{}.Describe(getThing)
	if badge != "GET" || target != "/things/{id}" {
		t.Errorf("Describe = %q %q", badge, target)
	}
}
