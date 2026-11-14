// Package httpx provides PlugIt's hardened HTTP client (SPEC §5.3: T8, T9,
// T12). Every outbound request PlugIt makes goes through it.
//
//   - SSRF guard: the IP actually being connected is checked at dial time
//     (net.Dialer.Control), so DNS rebinding cannot slip past a hostname
//     check. Private ranges are blocked unless AllowPrivate is set; cloud
//     metadata, unspecified, multicast and reserved addresses are always
//     blocked.
//   - Redirects are followed only within the same origin, at most
//     MaxRedirects times.
//   - Response bodies are capped at MaxResponseBytes (after decompression).
//     Reading past the cap returns the capped bytes followed by
//     ErrResponseTruncated instead of io.EOF.
//   - A total timeout covers dial, headers and body.
//   - Proxy environment variables are ignored: dialing a proxy would check
//     the proxy's address instead of the target's.
package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

const (
	DefaultTimeout          = 30 * time.Second
	DefaultMaxResponseBytes = 1 << 20
	DefaultMaxRedirects     = 5
)

var (
	ErrBlockedAddress    = errors.New("blocked address")
	ErrRedirectBlocked   = errors.New("cross-origin redirect blocked")
	ErrTooManyRedirects  = errors.New("too many redirects")
	ErrResponseTruncated = errors.New("response truncated at size cap")
)

type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type Options struct {
	AllowPrivate     bool
	Timeout          time.Duration
	MaxResponseBytes int64
	MaxRedirects     int
	Resolver         Resolver
}

// NewClient returns a hardened *http.Client configured by opts.
func NewClient(opts Options) *http.Client {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.MaxResponseBytes <= 0 {
		opts.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if opts.MaxRedirects <= 0 {
		opts.MaxRedirects = DefaultMaxRedirects
	}
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}

	d := &guardedDialer{
		resolver:     opts.Resolver,
		allowPrivate: opts.AllowPrivate,
		dialer: &net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
			Control:   dialGuard(opts.AllowPrivate),
		},
	}
	tr := &http.Transport{
		Proxy:                  nil,
		DialContext:            d.DialContext,
		ForceAttemptHTTP2:      true,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  opts.Timeout,
		ExpectContinueTimeout:  1 * time.Second,
		IdleConnTimeout:        90 * time.Second,
		MaxIdleConns:           16,
		MaxResponseHeaderBytes: 1 << 20,
	}
	return &http.Client{
		Transport:     &capTransport{base: tr, max: opts.MaxResponseBytes},
		Timeout:       opts.Timeout,
		CheckRedirect: checkRedirect(opts.MaxRedirects),
	}
}

// checkRedirect allows at most maxRedirects redirects, all within the origin of the
// first request. Errors name origins only: full URLs may carry secrets.
func checkRedirect(maxRedirects int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return fmt.Errorf("%w: more than %d", ErrTooManyRedirects, maxRedirects)
		}
		if first := via[0].URL; !SameOrigin(first, req.URL) {
			return fmt.Errorf("%w: %s -> %s", ErrRedirectBlocked, Origin(first), Origin(req.URL))
		}
		return nil
	}
}

type guardedDialer struct {
	resolver     Resolver
	dialer       *net.Dialer
	allowPrivate bool
}

// DialContext dials addr ("host:port").
func (d *guardedDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return d.dialIP(ctx, network, ip, port)
	}

	ips, err := d.resolver.LookupNetIP(ctx, lookupNetwork(network), host)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolving %s: no addresses", host)
	}
	var errs []error
	for _, ip := range ips {
		conn, err := d.dialIP(ctx, network, ip, port)
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("dial %s: %w", host, errors.Join(errs...))
}

// dialIP checks ip before dialing it. The Control hook checks again on the
// address actually connected; the pre-check matters because some platforms
// rewrite addresses first (Windows turns a 0.0.0.0 target into loopback
// before Control runs, which would hide the always-blocked 0.0.0.0/8).
func (d *guardedDialer) dialIP(ctx context.Context, network string, ip netip.Addr, port string) (net.Conn, error) {
	if err := checkIP(ip, d.allowPrivate); err != nil {
		return nil, err
	}
	return d.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
}

func lookupNetwork(network string) string {
	switch network {
	case "tcp4":
		return "ip4"
	case "tcp6":
		return "ip6"
	default:
		return "ip"
	}
}
