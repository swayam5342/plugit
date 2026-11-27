package invoke

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/jsonpick"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/rest"
	"github.com/swayam5342/plugit/internal/redact"
	"github.com/swayam5342/plugit/internal/schema"
)

// These tests exercise the whole invoke pipeline (Encode → attachAuth →
// httpx → Decode → shape + redact) against httptest upstreams. They replace
// the old internal/proxy.Call integration suite, which Task 10 removed.

const testSecret = "test-token-123"

var listThings = model.Tool{Key: "GET /things", Name: "listThings", HTTP: &model.HTTPOp{Method: "GET", Path: "/things"},
	Params: []model.Param{paramIn("q", model.InQuery, false)}}

var listRepos = model.Tool{Key: "GET /repos", Name: "listRepos", HTTP: &model.HTTPOp{Method: "GET", Path: "/repos"}}

func paramIn(name string, in model.Location, required bool) model.Param {
	return model.Param{Name: name, Bind: model.Binding{Upstream: name, In: in}, Required: required, Schema: json.RawMessage(`{}`)}
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func decodeArgs(t *testing.T, raw string) map[string]any {
	t.Helper()
	a, err := schema.DecodeArgs(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// perform runs the pipeline the way serve/try do: one hardened client, the
// target's secrets registered for redaction, Perform around rest.REST.
func perform(t *testing.T, srvURL string, tl model.Tool, rawArgs string, tg rest.Target, opts httpx.Options, pick *jsonpick.Picker) Result {
	t.Helper()
	opts.AllowPrivate = true
	if tg.BaseURL == nil {
		u, err := url.Parse(srvURL)
		if err != nil {
			t.Fatal(err)
		}
		tg.BaseURL = u
	}
	red := redact.New(redact.Options{Secrets: tg.SecretValues()})
	d := Deps{
		Proto:    rest.REST{Target: tg},
		Doer:     httpx.NewClient(opts),
		Target:   tg,
		Endpoint: tg.BaseURL,
		Redactor: red,
		Timeout:  opts.Timeout,
	}
	return Perform(t.Context(), d, tl, decodeArgs(t, rawArgs), pick)
}

func tail(s string) string {
	if len(s) > 120 {
		return s[len(s)-120:]
	}
	return s
}

func TestPerformJSONPrettyPrinted(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":1}],"q":"` + r.URL.Query().Get("q") + `"}`))
	})
	res := perform(t, s.URL, listThings, `{"q": "cats"}`, rest.Target{}, httpx.Options{}, nil)
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Text)
	}
	for _, want := range []string{"HTTP 200 OK", "application/json", "{\n  \"items\": [\n", `"q": "cats"`} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("result missing %q:\n%s", want, res.Text)
		}
	}
	if res.Status != 200 || res.Code != "HTTP 200" {
		t.Errorf("Status = %d, Code = %q", res.Status, res.Code)
	}
}

func TestPerformHTTPErrorIsToolError(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	res := perform(t, s.URL, listThings, `{}`, rest.Target{}, httpx.Options{}, nil)
	if !res.IsError {
		t.Fatal("HTTP 404 must be an isError result")
	}
	if !strings.HasPrefix(res.Text, "listThings: ") || !strings.Contains(res.Text, "404") || !strings.Contains(res.Text, "Not Found") {
		t.Errorf("text = %q", res.Text)
	}
	if res.Code != "HTTP 404" {
		t.Errorf("Code = %q, want HTTP 404", res.Code)
	}
}

func TestPerformEmptyAndBinaryAndText(t *testing.T) {
	cases := map[string]struct {
		h    http.HandlerFunc
		want string
	}{
		"empty 204": {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }, "(empty response, status 204)"},
		"png": {func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n"))
		}, "[binary 8 bytes, type image/png]"},
		"invalid utf-8 text": {func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte{0xff, 0xfe, 'a'})
		}, "[binary 3 bytes, type text/plain]"},
		"plain text": {func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("hello world"))
		}, "hello world"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := perform(t, serve(t, tc.h).URL, listThings, `{}`, rest.Target{}, httpx.Options{}, nil)
			if res.IsError || !strings.Contains(res.Text, tc.want) {
				t.Errorf("isError=%v text=%q, want it to contain %q", res.IsError, res.Text, tc.want)
			}
		})
	}
}

func TestPerformTruncatedResponse(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"` + strings.Repeat("x", 5000) + `"}`))
	})
	res := perform(t, s.URL, listThings, `{}`, rest.Target{}, httpx.Options{MaxResponseBytes: 1000}, nil)
	if !res.Truncated || !strings.Contains(res.Text, "[truncated: response exceeded 1000 bytes]") {
		t.Errorf("truncated=%v text tail=%q", res.Truncated, tail(res.Text))
	}
	if res.IsError {
		t.Error("truncation alone is not an error")
	}
}

// TestPerformEchoSecret is the echo-secret fixture (SPEC §6.3): the upstream
// returns the credential in every form it might, on success and on failure.
// None of them may reach the model.
func TestPerformEchoSecret(t *testing.T) {
	echo := func(status int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Echo", r.Header.Get("Authorization"))
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"bad key","auth":"` + r.Header.Get("Authorization") +
				`","key":"` + r.URL.Query().Get("key") +
				`","raw_query":"` + r.URL.RawQuery +
				`","b64":"` + base64.StdEncoding.EncodeToString([]byte(testSecret)) + `"}`))
		}
	}
	forms := []string{testSecret, url.QueryEscape(testSecret), base64.StdEncoding.EncodeToString([]byte(testSecret)),
		base64.StdEncoding.EncodeToString([]byte("alice:" + testSecret))}

	for _, status := range []int{200, 401} {
		for _, auth := range []rest.Auth{
			{Type: rest.AuthBearer, Secret: testSecret},
			{Type: rest.AuthQuery, Param: "key", Secret: testSecret},
			{Type: rest.AuthBasic, Secret: "alice:" + testSecret},
		} {
			t.Run(string(auth.Type)+"/"+http.StatusText(status), func(t *testing.T) {
				s := serve(t, echo(status))
				res := perform(t, s.URL, listThings, `{}`, rest.Target{Auth: auth}, httpx.Options{}, nil)
				for _, f := range forms {
					if strings.Contains(res.Text, f) {
						t.Errorf("result leaks %q:\n%s", f, res.Text)
					}
				}
				if !strings.Contains(res.Text, redact.Mask) {
					t.Errorf("expected masked output:\n%s", res.Text)
				}
				if res.IsError != (status >= 400) {
					t.Errorf("isError = %v for status %d", res.IsError, status)
				}
			})
		}
	}
}

func TestPerformNetworkErrorsAreToolErrors(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		s := httptest.NewServer(http.NotFoundHandler())
		addr := s.URL
		s.Close()
		res := perform(t, addr, listThings, `{"q": "x"}`, rest.Target{Auth: rest.Auth{Type: rest.AuthQuery, Param: "key", Secret: testSecret}}, httpx.Options{}, nil)
		if !res.IsError || !strings.HasPrefix(res.Text, "listThings: ") {
			t.Errorf("isError=%v text=%q", res.IsError, res.Text)
		}
		if strings.Contains(res.Text, testSecret) || strings.Contains(res.Text, "key=") {
			t.Errorf("network error leaks the URL or credential: %q", res.Text)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		s := serve(t, func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})
		defer close(release)
		res := perform(t, s.URL, listThings, `{}`, rest.Target{}, httpx.Options{Timeout: 150 * time.Millisecond}, nil)
		if !res.IsError || !strings.Contains(res.Text, "timed out") {
			t.Errorf("isError=%v text=%q", res.IsError, res.Text)
		}
	})
	t.Run("blocked by SSRF guard", func(t *testing.T) {
		s := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hi")) })
		u, _ := url.Parse(s.URL)
		red := redact.New(redact.Options{})
		d := Deps{Proto: rest.REST{Target: rest.Target{BaseURL: u}}, Doer: httpx.NewClient(httpx.Options{}), Target: rest.Target{BaseURL: u}, Endpoint: u, Redactor: red}
		res := Perform(t.Context(), d, listThings, decodeArgs(t, `{}`), nil)
		if !res.IsError || !strings.Contains(res.Text, "blocked") || !strings.Contains(res.Text, "allow_private") {
			t.Errorf("isError=%v text=%q", res.IsError, res.Text)
		}
	})
	t.Run("cross-origin redirect", func(t *testing.T) {
		var hits atomic.Int32
		other := serve(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
		s := serve(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) })
		res := perform(t, s.URL, listThings, `{}`, rest.Target{Auth: rest.Auth{Type: rest.AuthBearer, Secret: testSecret}}, httpx.Options{}, nil)
		if !res.IsError || !strings.Contains(res.Text, "redirect") || hits.Load() != 0 {
			t.Errorf("isError=%v hits=%d text=%q", res.IsError, hits.Load(), res.Text)
		}
	})
}

func TestPerformBadArgumentsAreToolErrors(t *testing.T) {
	var hits atomic.Int32
	s := serve(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	tl := model.Tool{Name: "getFile", HTTP: &model.HTTPOp{Method: "GET", Path: "/files/{name}"}, Params: []model.Param{paramIn("name", model.InPath, true)}}
	res := perform(t, s.URL, tl, `{"name": ".."}`, rest.Target{}, httpx.Options{}, nil)
	if !res.IsError || !strings.HasPrefix(res.Text, "getFile: ") || hits.Load() != 0 {
		t.Errorf("isError=%v hits=%d text=%q", res.IsError, hits.Load(), res.Text)
	}
}

func jsonReply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func pick(t *testing.T, fields ...string) *jsonpick.Picker {
	t.Helper()
	p, err := jsonpick.Parse(fields)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// response_fields keeps only the selected parts and says so (SPEC M6
// design note: response field filtering).
func TestPerformFilterBehaviors(t *testing.T) {
	t.Run("filters", func(t *testing.T) {
		s := serve(t, jsonReply(200, `[{"id":1,"name":"a","owner":{"login":"me","avatar":"x"},"body":"`+strings.Repeat("x", 500)+`"}]`))
		r := perform(t, s.URL, listRepos, `{}`, rest.Target{}, httpx.Options{}, pick(t, "id", "owner.login"))
		if r.IsError {
			t.Fatal(r.Text)
		}
		if strings.Contains(r.Text, "avatar") || strings.Contains(r.Text, "xxxx") || !strings.Contains(r.Text, `"login": "me"`) || !strings.Contains(r.Text, `"id": 1`) {
			t.Errorf("not filtered:\n%s", r.Text)
		}
		if !strings.Contains(r.Text, "fields: id, owner.login") {
			t.Errorf("no note that the response is a projection:\n%s", r.Text)
		}
	})
	t.Run("errors are not filtered", func(t *testing.T) {
		s := serve(t, jsonReply(422, `{"message":"bad","errors":[{"field":"name"}]}`))
		r := perform(t, s.URL, listRepos, `{}`, rest.Target{}, httpx.Options{}, pick(t, "id"))
		if !r.IsError || !strings.Contains(r.Text, "errors") || strings.Contains(r.Text, "fields:") {
			t.Errorf("error response filtered:\n%s", r.Text)
		}
	})
	t.Run("passthrough", func(t *testing.T) {
		text := func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("plain words"))
		}
		if r := perform(t, serve(t, text).URL, listRepos, `{}`, rest.Target{}, httpx.Options{}, pick(t, "id")); !strings.Contains(r.Text, "plain words") || strings.Contains(r.Text, "fields:") {
			t.Errorf("non-JSON: %s", r.Text)
		}
		if r := perform(t, serve(t, jsonReply(200, `{"id":1,`)).URL, listRepos, `{}`, rest.Target{}, httpx.Options{}, pick(t, "id")); !strings.Contains(r.Text, `{"id":1,`) {
			t.Errorf("invalid JSON should pass through: %s", r.Text)
		}
		if r := perform(t, serve(t, jsonReply(200, `{"id":1,"x":2}`)).URL, listRepos, `{}`, rest.Target{}, httpx.Options{}, nil); !strings.Contains(r.Text, `"x": 2`) {
			t.Errorf("no fields means no filtering: %s", r.Text)
		}
	})
}

const accessToken = "test-access-token-dyn-42"

// An OAuth token source supplies the bearer token per call. A token fetched
// while serving is redacted by value like any other secret, and a 401
// drops it so the next call fetches a new one.
func TestPerformWithTokenSource(t *testing.T) {
	var invalidated atomic.Int32
	echo := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"expired token ` + strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") + `"}`))
	}
	s := serve(t, echo)
	u, _ := url.Parse(s.URL)
	tg := rest.Target{BaseURL: u, Auth: rest.Auth{Type: rest.AuthBearer,
		Token:        func(context.Context) (string, error) { return accessToken, nil },
		Unauthorized: func() { invalidated.Add(1) },
	}}
	res := perform(t, s.URL, listRepos, `{}`, tg, httpx.Options{}, nil)
	if !res.IsError || res.Status != 401 {
		t.Fatalf("result = %+v", res)
	}
	if strings.Contains(res.Text, accessToken) || !strings.Contains(res.Text, redact.Mask) {
		t.Errorf("dynamic token not redacted:\n%s", res.Text)
	}
	if invalidated.Load() != 1 {
		t.Error("401 didn't invalidate the token")
	}
}

func TestPerformTokenSourceFails(t *testing.T) {
	var hits atomic.Int32
	s := serve(t, func(_ http.ResponseWriter, _ *http.Request) { hits.Add(1) })
	u, _ := url.Parse(s.URL)
	tg := rest.Target{BaseURL: u, Auth: rest.Auth{Type: rest.AuthBearer,
		Token: func(context.Context) (string, error) {
			return "", errors.New("OAuth token request failed: invalid_client")
		}}}
	res := perform(t, s.URL, listRepos, `{}`, tg, httpx.Options{}, nil)
	if !res.IsError || !strings.Contains(res.Text, "invalid_client") || hits.Load() != 0 {
		t.Errorf("result = %+v, hits = %d", res, hits.Load())
	}
}
