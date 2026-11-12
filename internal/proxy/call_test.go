package proxy

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/redact"
)

const testSecret = "test-token-123"

var listThings = model.Tool{Key: "GET /things", Name: "listThings", Method: "GET", Path: "/things",
	Params: []model.Param{param("q", model.InQuery, false)}}

func server(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func call(t *testing.T, srvURL string, tl model.Tool, rawArgs string, tg Target, opts httpx.Options) Result {
	t.Helper()
	opts.AllowPrivate = true // httptest listens on loopback
	if tg.BaseURL == nil {
		tg.BaseURL = base(t, srvURL)
	}
	red := redact.New(redact.Options{Secrets: tg.SecretValues()})
	return Call(t.Context(), httpx.NewClient(opts), tl, args(t, rawArgs), tg, red)
}

func TestSecretValues(t *testing.T) {
	tg := Target{
		Auth: Auth{Type: AuthBasic, Secret: "alice:" + testSecret},
		Fixed: []Fixed{
			{Name: "tenant", Value: "t-secret-9", Secret: true},
			{Name: "version", Value: "v2"}, // literal: not a secret (SPEC T11)
		},
	}
	got := map[string]bool{}
	for _, s := range tg.SecretValues() {
		got[s] = true
	}
	for _, want := range []string{"alice:" + testSecret, testSecret, "t-secret-9"} {
		if !got[want] {
			t.Errorf("SecretValues() missing %q: %v", want, tg.SecretValues())
		}
	}
	for _, not := range []string{"v2", "alice", ""} {
		if got[not] {
			t.Errorf("SecretValues() must not include %q", not)
		}
	}
}

func TestCallJSONPrettyPrinted(t *testing.T) {
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":1}],"q":"` + r.URL.Query().Get("q") + `"}`))
	})
	res := call(t, s.URL, listThings, `{"q": "cats"}`, Target{}, httpx.Options{})
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Text)
	}
	for _, want := range []string{"HTTP 200 OK", "Content-Type: application/json", "{\n  \"items\": [\n", `"q": "cats"`} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("result missing %q:\n%s", want, res.Text)
		}
	}
	if res.Status != 200 {
		t.Errorf("Status = %d", res.Status)
	}
}

func TestCallHTTPErrorIsToolError(t *testing.T) {
	s := server(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	res := call(t, s.URL, listThings, `{}`, Target{}, httpx.Options{})
	if !res.IsError {
		t.Fatal("HTTP 404 must be an isError result")
	}
	if !strings.HasPrefix(res.Text, "listThings: ") || !strings.Contains(res.Text, "404") || !strings.Contains(res.Text, "Not Found") {
		t.Errorf("text = %q", res.Text)
	}
}

func TestCallEmptyAndBinaryAndText(t *testing.T) {
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
			res := call(t, server(t, tc.h).URL, listThings, `{}`, Target{}, httpx.Options{})
			if res.IsError || !strings.Contains(res.Text, tc.want) {
				t.Errorf("isError=%v text=%q, want it to contain %q", res.IsError, res.Text, tc.want)
			}
		})
	}
}

func TestCallTruncatedResponse(t *testing.T) {
	s := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"` + strings.Repeat("x", 5000) + `"}`))
	})
	res := call(t, s.URL, listThings, `{}`, Target{}, httpx.Options{MaxResponseBytes: 1000})
	if !res.Truncated || !strings.Contains(res.Text, "[truncated: response exceeded 1000 bytes]") {
		t.Errorf("truncated=%v text tail=%q", res.Truncated, tail(res.Text))
	}
	if res.IsError {
		t.Error("truncation alone is not an error")
	}
}

func tail(s string) string {
	if len(s) > 120 {
		return s[len(s)-120:]
	}
	return s
}

// TestCallEchoSecret is the echo-secret fixture (SPEC §6.3): the upstream
// returns the credential in every form it might, on success and on failure.
// None of them may reach the model.
func TestCallEchoSecret(t *testing.T) {
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
		for _, auth := range []Auth{
			{Type: AuthBearer, Secret: testSecret},
			{Type: AuthQuery, Param: "key", Secret: testSecret},
			{Type: AuthBasic, Secret: "alice:" + testSecret},
		} {
			t.Run(string(auth.Type)+"/"+http.StatusText(status), func(t *testing.T) {
				s := server(t, echo(status))
				res := call(t, s.URL, listThings, `{}`, Target{Auth: auth}, httpx.Options{})
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

func TestCallNetworkErrorsAreToolErrors(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		s := httptest.NewServer(http.NotFoundHandler())
		addr := s.URL
		s.Close()
		res := call(t, addr, listThings, `{"q": "x"}`, Target{Auth: Auth{Type: AuthQuery, Param: "key", Secret: testSecret}}, httpx.Options{})
		if !res.IsError || !strings.HasPrefix(res.Text, "listThings: ") {
			t.Errorf("isError=%v text=%q", res.IsError, res.Text)
		}
		if strings.Contains(res.Text, testSecret) || strings.Contains(res.Text, "key=") {
			t.Errorf("network error leaks the URL or credential: %q", res.Text)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		s := server(t, func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})
		defer close(release)
		res := call(t, s.URL, listThings, `{}`, Target{}, httpx.Options{Timeout: 150 * time.Millisecond})
		if !res.IsError || !strings.Contains(res.Text, "timed out") {
			t.Errorf("isError=%v text=%q", res.IsError, res.Text)
		}
	})
	t.Run("blocked by SSRF guard", func(t *testing.T) {
		s := server(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hi")) })
		red := redact.New(redact.Options{})
		res := Call(t.Context(), httpx.NewClient(httpx.Options{}), listThings, args(t, `{}`), Target{BaseURL: base(t, s.URL)}, red)
		if !res.IsError || !strings.Contains(res.Text, "blocked") || !strings.Contains(res.Text, "allow_private") {
			t.Errorf("isError=%v text=%q", res.IsError, res.Text)
		}
	})
	t.Run("cross-origin redirect", func(t *testing.T) {
		var hits atomic.Int32
		other := server(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
		s := server(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) })
		res := call(t, s.URL, listThings, `{}`, Target{Auth: Auth{Type: AuthBearer, Secret: testSecret}}, httpx.Options{})
		if !res.IsError || !strings.Contains(res.Text, "redirect") || hits.Load() != 0 {
			t.Errorf("isError=%v hits=%d text=%q", res.IsError, hits.Load(), res.Text)
		}
	})
}

func TestCallBadArgumentsAreToolErrors(t *testing.T) {
	var hits atomic.Int32
	s := server(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	tl := model.Tool{Name: "getFile", Method: "GET", Path: "/files/{name}", Params: []model.Param{param("name", model.InPath, true)}}
	res := call(t, s.URL, tl, `{"name": ".."}`, Target{}, httpx.Options{})
	if !res.IsError || !strings.HasPrefix(res.Text, "getFile: ") || hits.Load() != 0 {
		t.Errorf("isError=%v hits=%d text=%q", res.IsError, hits.Load(), res.Text)
	}
}
