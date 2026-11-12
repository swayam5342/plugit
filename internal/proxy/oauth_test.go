package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/redact"
)

const accessToken = "test-access-token-dyn-42"

// An OAuth token source supplies the bearer token per call. A token fetched
// while serving is redacted by value like any other secret, and a 401
// drops it so the next call fetches a new one.
func TestCallWithTokenSource(t *testing.T) {
	var invalidated atomic.Int32
	echo := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"expired token ` + strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") + `"}`))
	}
	s := server(t, echo)
	tg := Target{BaseURL: base(t, s.URL), Auth: Auth{Type: AuthBearer,
		Token:        func(context.Context) (string, error) { return accessToken, nil },
		Unauthorized: func() { invalidated.Add(1) },
	}}
	r := Call(t.Context(), httpx.NewClient(httpx.Options{AllowPrivate: true}), listRepos, nil, tg, redact.New(redact.Options{}), nil)
	if !r.IsError || r.Status != 401 {
		t.Fatalf("result = %+v", r)
	}
	if strings.Contains(r.Text, accessToken) || !strings.Contains(r.Text, redact.Mask) {
		t.Errorf("dynamic token not redacted:\n%s", r.Text)
	}
	if invalidated.Load() != 1 {
		t.Error("401 didn't invalidate the token")
	}
}

func TestCallTokenSourceFails(t *testing.T) {
	var hits atomic.Int32
	s := server(t, func(_ http.ResponseWriter, _ *http.Request) { hits.Add(1) })
	tg := Target{BaseURL: base(t, s.URL), Auth: Auth{Type: AuthBearer,
		Token: func(context.Context) (string, error) {
			return "", errors.New("OAuth token request failed: invalid_client")
		}}}
	r := Call(t.Context(), httpx.NewClient(httpx.Options{AllowPrivate: true}), listRepos, nil, tg, redact.New(redact.Options{}), nil)
	if !r.IsError || !strings.Contains(r.Text, "invalid_client") || hits.Load() != 0 {
		t.Errorf("result = %+v, hits = %d", r, hits.Load())
	}
}
