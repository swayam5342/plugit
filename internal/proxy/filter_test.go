package proxy

import (
	"net/http"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/jsonpick"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/redact"
)

var listRepos = model.Tool{Key: "GET /repos", Name: "listRepos", Method: "GET", Path: "/repos"}

func pickCall(t *testing.T, h http.HandlerFunc, fields ...string) Result {
	t.Helper()
	s := server(t, h)
	p, err := jsonpick.Parse(fields)
	if err != nil {
		t.Fatal(err)
	}
	return Call(t.Context(), httpx.NewClient(httpx.Options{AllowPrivate: true}), listRepos, nil,
		Target{BaseURL: base(t, s.URL)}, redact.New(redact.Options{}), p)
}

func jsonReply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// response_fields keeps only the selected parts and says so (SPEC M6
// design note: response field filtering).
func TestCallFiltersResponse(t *testing.T) {
	r := pickCall(t, jsonReply(200, `[{"id":1,"name":"a","owner":{"login":"me","avatar":"x"},"body":"`+strings.Repeat("x", 500)+`"}]`), "id", "owner.login")
	if r.IsError {
		t.Fatal(r.Text)
	}
	if strings.Contains(r.Text, "avatar") || strings.Contains(r.Text, "xxxx") || !strings.Contains(r.Text, `"login": "me"`) || !strings.Contains(r.Text, `"id": 1`) {
		t.Errorf("not filtered:\n%s", r.Text)
	}
	if !strings.Contains(r.Text, "fields: id, owner.login") {
		t.Errorf("no note that the response is a projection:\n%s", r.Text)
	}
}

// Errors are never filtered: the model needs the whole explanation.
func TestCallDoesntFilterErrors(t *testing.T) {
	r := pickCall(t, jsonReply(422, `{"message":"bad","errors":[{"field":"name"}]}`), "id")
	if !r.IsError || !strings.Contains(r.Text, "errors") || strings.Contains(r.Text, "fields:") {
		t.Errorf("error response filtered:\n%s", r.Text)
	}
}

func TestCallFilterPassthrough(t *testing.T) {
	text := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("plain words"))
	}
	if r := pickCall(t, text, "id"); !strings.Contains(r.Text, "plain words") || strings.Contains(r.Text, "fields:") {
		t.Errorf("non-JSON: %s", r.Text)
	}
	if r := pickCall(t, jsonReply(200, `{"id":1,`), "id"); !strings.Contains(r.Text, `{"id":1,`) {
		t.Errorf("invalid JSON should pass through: %s", r.Text)
	}
	if r := pickCall(t, jsonReply(200, `{"id":1,"x":2}`)); !strings.Contains(r.Text, `"x": 2`) {
		t.Errorf("no fields means no filtering: %s", r.Text)
	}
}
