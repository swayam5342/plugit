package invoke

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/redact"
)

// Fetch is the credentialed one-off request seam (plugit add --introspect):
// the credential is attached only to a request for the pinned endpoint's
// origin (T8), and a request for any other origin never reaches the doer.
func TestFetchAttachesCredentialOnlyToPinnedOrigin(t *testing.T) {
	const secret = "test-token-123"
	cred := Credential{Type: credHeader, Header: "Authorization", Secret: "Bearer " + secret}

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(srv.Close)
	d := Deps{Doer: httpx.NewClient(httpx.Options{AllowPrivate: true}), Cred: cred, Endpoint: mustU(srv.URL + "/graphql")}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/graphql", strings.NewReader(`{}`))
	body, truncated, status, err := Fetch(t.Context(), d, req)
	if err != nil || truncated || status != http.StatusOK || string(body) != `{"data":{}}` {
		t.Fatalf("Fetch = %q, %v, %d, %v", body, truncated, status, err)
	}
	if got != "Bearer "+secret {
		t.Errorf("pinned origin got Authorization %q", got)
	}

	doer := &recordingDoer{}
	d = Deps{Doer: doer, Cred: cred, Endpoint: mustU("https://api.example.test/graphql")}
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, "https://evil.example.test/graphql", nil)
	if _, _, _, err := Fetch(t.Context(), d, req); err == nil {
		t.Error("a credentialed request for another origin must be refused")
	} else if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaks the secret: %v", err)
	}
	if len(doer.reqs) != 0 {
		t.Errorf("the cross-origin request reached the doer")
	}
}

// A body over the client's cap comes back truncated, not as an error, so the
// caller decides; a transport failure is a described error.
func TestFetchTruncatedAndTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	t.Cleanup(srv.Close)
	d := Deps{Doer: httpx.NewClient(httpx.Options{AllowPrivate: true, MaxResponseBytes: 10}), Endpoint: mustU(srv.URL)}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, nil)
	body, truncated, _, err := Fetch(t.Context(), d, req)
	if err != nil || !truncated || len(body) > 10 {
		t.Errorf("Fetch = %d bytes, truncated=%v, err=%v; want a truncated body", len(body), truncated, err)
	}

	d = Deps{Doer: httpx.NewClient(httpx.Options{}), Endpoint: mustU(srv.URL)} // private address refused
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, nil)
	_, _, _, err = Fetch(t.Context(), d, req)
	if !errors.Is(err, httpx.ErrBlockedAddress) {
		t.Errorf("err = %v, want the SSRF guard's ErrBlockedAddress", err)
	}
}

// T11 for a response that becomes spec text (an introspection result): a
// body carrying a secret in play (the credential, the bare token of a
// "Bearer <token>" header credential, an OAuth access token only Fetch
// knows, or a secret in d.Redactor such as a resolved fixed value), in any
// encoded form, is refused with ErrSecretInResponse. Nothing of the body
// comes back and the error never repeats the secret.
func TestFetchRefusesBodyCarryingSecret(t *testing.T) {
	const tok = "tok/en+123=" // percent-encoding changes it
	const fixedSecret = "fixed-secret-456"
	header := Credential{Type: credHeader, Header: "Authorization", Secret: "Bearer " + tok}
	oauth := Credential{Type: credBearer, Token: func(context.Context) (string, error) { return tok, nil }}
	cases := []struct {
		name string
		cred Credential
		red  *redact.Redactor
		echo string
		want bool // refused
	}{
		{"raw header credential", header, nil, "Bearer " + tok, true},
		{"bare token of a header credential", header, nil, tok, true},
		{"percent-encoded token", header, nil, url.QueryEscape(tok), true},
		{"base64 token", header, nil, base64.StdEncoding.EncodeToString([]byte(tok)), true},
		{"OAuth access token", oauth, nil, tok, true},
		{"fixed secret in d.Redactor", Credential{}, redact.New(redact.Options{Secrets: []string{fixedSecret}}), fixedSecret, true},
		{"no secret", header, redact.New(redact.Options{Secrets: []string{fixedSecret}}), "an ordinary description", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"data":{"__schema":{"types":[{"name":"Query","description":"` + tc.echo + `"}]}}}`))
			}))
			t.Cleanup(srv.Close)
			d := Deps{Doer: httpx.NewClient(httpx.Options{AllowPrivate: true}), Cred: tc.cred, Endpoint: mustU(srv.URL), Redactor: tc.red}
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, nil)
			body, _, _, err := Fetch(t.Context(), d, req)
			if !tc.want {
				if err != nil || !strings.Contains(string(body), tc.echo) {
					t.Fatalf("Fetch = %q, %v; want the body", body, err)
				}
				return
			}
			if !errors.Is(err, ErrSecretInResponse) {
				t.Fatalf("err = %v, want ErrSecretInResponse", err)
			}
			if body != nil {
				t.Errorf("a refused body came back: %q", body)
			}
			for _, s := range []string{tok, fixedSecret, tc.echo} {
				if strings.Contains(err.Error(), s) {
					t.Errorf("error repeats a secret: %v", err)
				}
			}
		})
	}
}

// A 401 drops the credential's cached token, as Perform does, so a stale
// OAuth access token isn't reused after a failed introspection.
func TestFetchUnauthorizedInvalidatesToken(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusOK, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{}`))
		}))
		var invalidated atomic.Int32
		cred := Credential{Type: credBearer,
			Token:        func(context.Context) (string, error) { return "test-token-123", nil },
			Unauthorized: func() { invalidated.Add(1) }}
		d := Deps{Doer: httpx.NewClient(httpx.Options{AllowPrivate: true}), Cred: cred, Endpoint: mustU(srv.URL)}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, nil)
		_, _, got, err := Fetch(t.Context(), d, req)
		srv.Close()
		if err != nil || got != status {
			t.Fatalf("Fetch = %d, %v", got, err)
		}
		want := int32(0)
		if status == http.StatusUnauthorized {
			want = 1
		}
		if invalidated.Load() != want {
			t.Errorf("HTTP %d: Unauthorized called %d times, want %d", status, invalidated.Load(), want)
		}
	}
}

// SecretValues also lists the bare token of a header credential written as
// "<scheme> <token>" ("Bearer x", "token x"): an upstream may echo just the
// token.
func TestSecretValuesBareTokenOfSchemeHeader(t *testing.T) {
	cases := []struct {
		cred Credential
		want []string
	}{
		{Credential{Type: credHeader, Header: "Authorization", Secret: "Bearer test-token-123"}, []string{"Bearer test-token-123", "test-token-123"}},
		{Credential{Type: credHeader, Header: "Authorization", Secret: "token test-token-123"}, []string{"token test-token-123", "test-token-123"}},
		{Credential{Type: credHeader, Header: "X-Api-Key", Secret: "test-key-123"}, []string{"test-key-123"}},
		{Credential{Type: credHeader, Header: "Authorization", Secret: "Bearer "}, []string{"Bearer "}},
		{Credential{Type: credBearer, Secret: "test-token-123"}, []string{"test-token-123"}},
	}
	for _, tc := range cases {
		if got := tc.cred.SecretValues(); !slices.Equal(got, tc.want) {
			t.Errorf("SecretValues(%q) = %q, want %q", tc.cred.Secret, got, tc.want)
		}
	}
}
