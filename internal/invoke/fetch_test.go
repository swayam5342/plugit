package invoke

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/httpx"
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
