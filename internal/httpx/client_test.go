package httpx

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var _ Doer = (*http.Client)(nil)

func TestSameOrigin(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"https://api.example.com/a", "https://api.example.com/b?x=1", true},
		{"https://api.example.com/", "https://api.example.com:443/", true},
		{"http://api.example.com/", "http://api.example.com:80/", true},
		{"https://API.Example.com/", "https://api.example.com/", true},
		{"https://api.example.com/", "http://api.example.com/", false},
		{"https://api.example.com/", "https://api.example.com:8443/", false},
		{"https://api.example.com/", "https://evil.example.com/", false},
		{"https://api.example.com/", "https://sub.api.example.com/", false},
		{"https://api.example.com/", "https://api.example.com.evil.test/", false},
		{"http://127.0.0.1:8080/", "http://127.0.0.1:8081/", false},
		{"http://[::1]:8080/", "http://[::1]:8080/x", true},
	}
	for _, tc := range tests {
		t.Run(tc.a+" vs "+tc.b, func(t *testing.T) {
			a, _ := url.Parse(tc.a)
			b, _ := url.Parse(tc.b)
			if got := SameOrigin(a, b); got != tc.want {
				t.Errorf("SameOrigin = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSameOriginNil(t *testing.T) {
	u, _ := url.Parse("https://a.test/")
	if SameOrigin(nil, u) || SameOrigin(u, nil) || SameOrigin(nil, nil) {
		t.Error("SameOrigin with nil must be false")
	}
}

func TestRedirectSameOriginFollowed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("final"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := NewClient(Options{AllowPrivate: true})
	resp, err := get(t, c, srv.URL+"/start")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "final" {
		t.Errorf("body = %q, want %q", body, "final")
	}
}

func TestRedirectCrossOriginBlocked(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("credential leaked to other origin")
		}
	}))
	t.Cleanup(other.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)

	c := NewClient(Options{AllowPrivate: true})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, origin.URL, nil)
	req.Header.Set("Authorization", "Bearer test-token-123")
	resp, err := c.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrRedirectBlocked) {
		t.Fatalf("err = %v, want ErrRedirectBlocked", err)
	}
	if otherHits.Load() != 0 {
		t.Errorf("other origin was hit %d times, want 0", otherHits.Load())
	}
}

func TestRedirectLimit(t *testing.T) {
	// /r/N redirects to /r/N-1; /r/0 answers 200. Following N redirects
	// starting from /r/N.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/r/"))
		if err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		if n == 0 {
			_, _ = w.Write([]byte("done"))
			return
		}
		http.Redirect(w, r, "/r/"+strconv.Itoa(n-1), http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Options{AllowPrivate: true})

	resp, err := get(t, c, srv.URL+"/r/5")
	if err != nil {
		t.Fatalf("5 redirects: err = %v, want nil", err)
	}
	_ = resp.Body.Close()

	resp, err = get(t, c, srv.URL+"/r/6")
	if err == nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrTooManyRedirects) {
		t.Errorf("6 redirects: err = %v, want ErrTooManyRedirects", err)
	}
}

func TestResponseCap(t *testing.T) {
	const capBytes = 1000
	tests := []struct {
		name          string
		size          int
		wantLen       int
		wantTruncated bool
	}{
		{"under cap", 999, 999, false},
		{"exactly cap", 1000, 1000, false},
		{"one over", 1001, 1000, true},
		{"far over", 5 << 20, 1000, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(bytes.Repeat([]byte("x"), tc.size))
			}))
			t.Cleanup(srv.Close)
			c := NewClient(Options{AllowPrivate: true, MaxResponseBytes: capBytes})

			resp, err := get(t, c, srv.URL)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			body, err := io.ReadAll(resp.Body)
			if tc.wantTruncated && !errors.Is(err, ErrResponseTruncated) {
				t.Errorf("ReadAll err = %v, want ErrResponseTruncated", err)
			}
			if !tc.wantTruncated && err != nil {
				t.Errorf("ReadAll err = %v, want nil", err)
			}
			if len(body) != tc.wantLen {
				t.Errorf("len(body) = %d, want %d", len(body), tc.wantLen)
			}
			// The end state is sticky.
			if n, err2 := resp.Body.Read(make([]byte, 8)); n != 0 || (err2 == nil) {
				t.Errorf("Read after end = %d, %v; want 0 and an error", n, err2)
			}
		})
	}
}

func TestResponseCapAppliesAfterDecompression(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(make([]byte, 10<<20)) // 10 MiB of zeros, ~10 KiB compressed
	_ = zw.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(gz.Bytes())
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Options{AllowPrivate: true, MaxResponseBytes: 4096})

	resp, err := get(t, c, srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if !errors.Is(err, ErrResponseTruncated) {
		t.Errorf("ReadAll err = %v, want ErrResponseTruncated", err)
	}
	if len(body) != 4096 {
		t.Errorf("len = %d, want 4096", len(body))
	}
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	c := NewClient(Options{AllowPrivate: true, Timeout: 100 * time.Millisecond})
	start := time.Now()
	_, err := get(t, c, srv.URL)
	var nerr net.Error
	if !errors.As(err, &nerr) || !nerr.Timeout() {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("timeout took %v", elapsed)
	}
}

func TestDefaults(t *testing.T) {
	c := NewClient(Options{})
	if c.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %v, want %v", c.Timeout, DefaultTimeout)
	}
	ct, ok := c.Transport.(*capTransport)
	if !ok {
		t.Fatalf("Transport is %T, want *capTransport", c.Transport)
	}
	if ct.max != DefaultMaxResponseBytes {
		t.Errorf("max response bytes = %d, want %d", ct.max, DefaultMaxResponseBytes)
	}
	tr, ok := ct.base.(*http.Transport)
	if !ok {
		t.Fatalf("base transport is %T, want *http.Transport", ct.base)
	}
	// Proxy env vars must be ignored: a proxy would be dialed instead of the
	// target, bypassing the dial-time SSRF guard.
	if tr.Proxy != nil {
		t.Error("Transport.Proxy is set; proxy env vars must be ignored")
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Error("TLS minimum version must be at least 1.2")
	}
}
