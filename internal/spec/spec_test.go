package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swayam5342/plugit/internal/httpx"
)

const petJSON = `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"paths":{}}`
const petYAML = "openapi: 3.0.3\ninfo:\n  title: t\n  version: '1'\npaths: {}\n"

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// testLoader can reach httptest servers on loopback.
func testLoader(opts Options) *Loader {
	opts.AllowPrivate = true
	return New(opts)
}

func TestLoadFileJSON(t *testing.T) {
	p := writeFile(t, "api.json", []byte(petJSON))
	s, err := New(Options{}).Load(t.Context(), p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(s.Bytes) != petJSON {
		t.Errorf("Bytes = %q", s.Bytes)
	}
	if s.Format != JSON {
		t.Errorf("Format = %v, want JSON", s.Format)
	}
	if s.SHA256 != sha([]byte(petJSON)) {
		t.Errorf("SHA256 = %s, want %s", s.SHA256, sha([]byte(petJSON)))
	}
	if s.Source != p {
		t.Errorf("Source = %q, want %q", s.Source, p)
	}
}

func TestLoadFileYAMLWithBOM(t *testing.T) {
	raw := append([]byte("\xEF\xBB\xBF"), petYAML...)
	p := writeFile(t, "api.yaml", raw)
	s, err := New(Options{}).Load(t.Context(), p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Format != YAML {
		t.Errorf("Format = %v, want YAML", s.Format)
	}
	if string(s.Bytes) != petYAML {
		t.Errorf("Bytes = %q, want BOM stripped", s.Bytes)
	}
	if s.SHA256 != sha(raw) {
		t.Error("SHA256 must cover the raw bytes as loaded")
	}
}

func TestLoadJSONWithLeadingWhitespace(t *testing.T) {
	p := writeFile(t, "api.json", []byte("\n\t  "+petJSON))
	s, err := New(Options{}).Load(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Format != JSON {
		t.Errorf("Format = %v, want JSON", s.Format)
	}
}

func TestLoadFileErrors(t *testing.T) {
	big := writeFile(t, "big.json", []byte(strings.Repeat(" ", 101)))
	empty := writeFile(t, "empty.json", []byte("  \n\t"))
	dir := t.TempDir()

	tests := []struct {
		name    string
		path    string
		wantErr error
	}{
		{"too large", big, ErrTooLarge},
		{"empty", empty, ErrEmpty},
		{"directory", dir, ErrNotRegular},
		{"missing", filepath.Join(dir, "nope.json"), fs.ErrNotExist},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Options{MaxBytes: 100}).Load(t.Context(), tc.path)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestLoadHugeFileDefaultLimit is the generated "huge.json" fixture (SPEC
// §6.3): one byte over the default 20 MiB cap.
func TestLoadHugeFileDefaultLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "huge.json")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(DefaultMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if _, err := New(Options{}).Load(t.Context(), p); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}

func TestLoadHugeURLDefaultLimit(t *testing.T) {
	chunk := []byte(strings.Repeat(" ", 1<<20))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for range 21 { // 21 MiB, no Content-Length
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	if _, err := testLoader(Options{}).Load(t.Context(), srv.URL); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}

func TestLoadExactlyMaxBytesIsAllowed(t *testing.T) {
	data := []byte(petJSON)
	p := writeFile(t, "api.json", data)
	if _, err := New(Options{MaxBytes: int64(len(data))}).Load(t.Context(), p); err != nil {
		t.Errorf("Load at exactly MaxBytes: %v", err)
	}
}

func TestLoadRejectsOtherSchemes(t *testing.T) {
	for _, loc := range []string{"file:///etc/passwd", "ftp://example.test/api.json", "gopher://x/", "FILE://C:/Windows/win.ini"} {
		_, err := New(Options{}).Load(t.Context(), loc)
		if !errors.Is(err, ErrUnsupportedScheme) {
			t.Errorf("Load(%q) = %v, want ErrUnsupportedScheme", loc, err)
		}
	}
}

func TestLoadURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte(petYAML))
	}))
	t.Cleanup(srv.Close)

	s, err := testLoader(Options{}).Load(t.Context(), srv.URL+"/openapi.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(s.Bytes) != petYAML || s.Format != YAML || s.SHA256 != sha([]byte(petYAML)) {
		t.Errorf("unexpected spec: format=%v sha=%s bytes=%q", s.Format, s.SHA256, s.Bytes)
	}
}

func TestLoadURLUppercaseScheme(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(petJSON))
	}))
	t.Cleanup(srv.Close)
	if _, err := testLoader(Options{}).Load(t.Context(), "HTTP://"+strings.TrimPrefix(srv.URL, "http://")); err != nil {
		t.Errorf("Load with HTTP:// = %v", err)
	}
}

func TestLoadURLErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/missing", func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) })
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 5000)))
	})
	mux.HandleFunc("/slow", func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Run("404", func(t *testing.T) {
		_, err := testLoader(Options{}).Load(t.Context(), srv.URL+"/missing")
		if !errors.Is(err, ErrHTTPStatus) || !strings.Contains(err.Error(), "404") {
			t.Errorf("err = %v, want ErrHTTPStatus mentioning 404", err)
		}
	})
	t.Run("too large", func(t *testing.T) {
		_, err := testLoader(Options{MaxBytes: 1000}).Load(t.Context(), srv.URL+"/big")
		if !errors.Is(err, ErrTooLarge) {
			t.Errorf("err = %v, want ErrTooLarge", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		start := time.Now()
		_, err := testLoader(Options{Timeout: 100 * time.Millisecond}).Load(t.Context(), srv.URL+"/slow")
		if err == nil {
			t.Fatal("expected a timeout error")
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("timeout took %v", time.Since(start))
		}
	})
}

func TestLoadURLErrorsHideQueryAndUserinfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	loc := strings.Replace(srv.URL, "http://", "http://user:test-password-123@", 1) + "/spec.json?token=test-token-123"
	_, err := testLoader(Options{}).Load(t.Context(), loc)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, secret := range []string{"test-token-123", "test-password-123"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks %q: %v", secret, err)
		}
	}
}

func TestLoadURLBlocksPrivateByDefault(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = w.Write([]byte(petJSON))
	}))
	t.Cleanup(srv.Close)

	_, err := New(Options{}).Load(t.Context(), srv.URL)
	if !errors.Is(err, httpx.ErrBlockedAddress) {
		t.Errorf("err = %v, want httpx.ErrBlockedAddress", err)
	}
	if hit {
		t.Error("server was reached despite allow_private=false")
	}
}
