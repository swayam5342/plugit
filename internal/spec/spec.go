// Package spec loads raw API spec bytes from a file or an http(s) URL, with
// size and time limits (SPEC §5.3, T2). It does not parse the spec; see
// package openapi.
package spec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/swayam5342/plugit/internal/httpx"
)

// Limits applied when the corresponding Options field is zero.
const (
	DefaultMaxBytes = 20 << 20 // 20 MiB
	DefaultTimeout  = 15 * time.Second
)

// Sentinel errors callers can match with errors.Is.
var (
	ErrTooLarge          = errors.New("spec exceeds size limit")
	ErrEmpty             = errors.New("spec is empty")
	ErrNotRegular        = errors.New("spec path is not a regular file")
	ErrUnsupportedScheme = errors.New("unsupported spec location scheme (use a file path or an http(s) URL)")
	ErrHTTPStatus        = errors.New("unexpected HTTP status fetching spec")
)

// Format is the serialization of a spec document.
type Format int

// Spec formats.
const (
	JSON Format = iota + 1
	YAML
)

func (f Format) String() string {
	switch f {
	case JSON:
		return "json"
	case YAML:
		return "yaml"
	}
	return "unknown"
}

// Spec is a loaded spec document.
type Spec struct {
	Bytes  []byte // document bytes, UTF-8 BOM removed
	SHA256 string // hex sha256 of the raw bytes as loaded
	Format Format
	Source string // the location it was loaded from
}

// Options configures a Loader. The zero value is the secure default.
type Options struct {
	// AllowPrivate lets URL fetches reach loopback and private networks.
	AllowPrivate bool
	MaxBytes     int64
	Timeout      time.Duration
}

// Loader loads specs. Create it with New.
type Loader struct {
	client   httpx.Doer
	maxBytes int64
	timeout  time.Duration
}

// New returns a Loader whose URL fetches go through PlugIt's hardened client.
func New(opts Options) *Loader {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	return &Loader{
		client: httpx.NewClient(httpx.Options{
			AllowPrivate: opts.AllowPrivate,
			Timeout:      opts.Timeout,
			// One extra byte so "too large" is distinguishable from "exactly at the limit".
			MaxResponseBytes: opts.MaxBytes + 1,
		}),
		maxBytes: opts.MaxBytes,
		timeout:  opts.Timeout,
	}
}

// Load reads the spec at location: an http:// or https:// URL, or a local
// file path. Other URL schemes, including file://, are rejected.
func (l *Loader) Load(ctx context.Context, location string) (*Spec, error) {
	var (
		raw []byte
		err error
	)
	if scheme, _, ok := strings.Cut(location, "://"); ok {
		switch strings.ToLower(scheme) {
		case "http", "https":
			raw, err = l.fetch(ctx, location)
		default:
			return nil, fmt.Errorf("loading spec: %w: %q", ErrUnsupportedScheme, scheme)
		}
	} else {
		raw, err = l.readFile(location)
	}
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256(raw)
	body := bytes.TrimPrefix(raw, []byte("\xEF\xBB\xBF"))
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("loading spec %s: %w", location, ErrEmpty)
	}
	format := YAML
	if trimmed[0] == '{' || trimmed[0] == '[' {
		format = JSON
	}
	return &Spec{Bytes: body, SHA256: hex.EncodeToString(sum[:]), Format: format, Source: location}, nil
}

func (l *Loader) readFile(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- the user names the spec file on the command line
	if err != nil {
		return nil, fmt.Errorf("loading spec: %w", err)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("loading spec %s: %w", path, err)
	}
	// Refuse directories, FIFOs and devices: a FIFO can block forever and a
	// device such as /dev/zero never ends.
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("loading spec %s: %w", path, ErrNotRegular)
	}
	if fi.Size() > l.maxBytes {
		return nil, fmt.Errorf("loading spec %s: %w (%d > %d bytes)", path, ErrTooLarge, fi.Size(), l.maxBytes)
	}
	return l.readLimited(f, path)
}

func (l *Loader) fetch(ctx context.Context, location string) ([]byte, error) {
	u, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("loading spec: invalid URL: %w", err)
	}
	safe := safeURL(u)

	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("loading spec %s: %w", safe, err)
	}
	req.Header.Set("Accept", "application/json, application/yaml, text/yaml, */*;q=0.5")
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loading spec %s: %w", safe, unwrapURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("loading spec %s: %w: %s", safe, ErrHTTPStatus, resp.Status)
	}
	return l.readLimited(resp.Body, safe)
}

func (l *Loader) readLimited(r io.Reader, name string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, l.maxBytes+1))
	if err != nil && !errors.Is(err, httpx.ErrResponseTruncated) {
		return nil, fmt.Errorf("loading spec %s: %w", name, err)
	}
	if errors.Is(err, httpx.ErrResponseTruncated) || int64(len(data)) > l.maxBytes {
		return nil, fmt.Errorf("loading spec %s: %w (limit %d bytes)", name, ErrTooLarge, l.maxBytes)
	}
	return data, nil
}

// safeURL drops userinfo, query and fragment, which may carry credentials.
func safeURL(u *url.URL) string {
	c := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}
	return c.String()
}

// unwrapURLError strips *url.Error, whose message repeats the full URL
// (query and userinfo included).
func unwrapURLError(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err
	}
	return err
}
