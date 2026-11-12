package mcpserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPPath is where the Streamable HTTP endpoint lives.
const HTTPPath = "/mcp"

// Limits for the HTTP transport.
const (
	maxHTTPBody       = 4 << 20
	httpSessionIdle   = 30 * time.Minute
	httpHeaderTimeout = 10 * time.Second
	httpIdleTimeout   = 2 * time.Minute
)

// ErrNotLoopback means an HTTP listen address isn't on this machine only.
var ErrNotLoopback = errors.New("the HTTP transport only listens on loopback (127.0.0.1 or ::1)")

// ListenAddr normalizes an HTTP listen address and refuses anything but
// loopback (SPEC T15): ":8931" and "8931" mean 127.0.0.1:8931, and
// "localhost" is 127.0.0.1.
func ListenAddr(s string) (string, error) {
	if s == "" {
		return "", errors.New("empty listen address")
	}
	if _, err := strconv.Atoi(s); err == nil {
		s = ":" + s
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", fmt.Errorf("listen address %q: %w", s, err)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return "", fmt.Errorf("listen address %q: bad port", s)
	}
	switch host {
	case "", "localhost":
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("%w; got %q", ErrNotLoopback, host)
	}
	return net.JoinHostPort(ip.String(), port), nil
}

// HTTPHandler serves cfg's tools over Streamable HTTP at /mcp, guarded as
// SPEC T15 requires: every request needs "Authorization: Bearer <token>"
// (compared in constant time), a browser Origin must be a loopback origin,
// cross-site browser requests are refused, and the Host must be loopback
// (DNS rebinding; the SDK checks this too).
func HTTPHandler(cfg Config, token string, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	srv := New(cfg)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		Logger:              logger,
		SessionTimeout:      httpSessionIdle,
		MaxRequestBodyBytes: maxHTTPBody,
	})
	want := []byte("Bearer " + token)
	mux := http.NewServeMux()
	mux.Handle(HTTPPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case !loopbackHost(r.Host):
			http.Error(w, "forbidden: not a loopback host", http.StatusForbidden)
			return
		case r.Header.Get("Origin") != "" && !loopbackOrigin(r.Header.Get("Origin")):
			http.Error(w, "forbidden: origin not allowed", http.StatusForbidden)
			return
		case r.Header.Get("Sec-Fetch-Site") == "cross-site":
			http.Error(w, "forbidden: cross-site request", http.StatusForbidden)
			return
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	return mux
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func loopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	return loopbackHost(u.Host)
}

// ServeHTTP listens on addr (see ListenAddr) and serves until ctx is done.
// ready, if set, gets the endpoint URL once listening.
func ServeHTTP(ctx context.Context, cfg Config, addr, token string, ready func(endpoint string)) error {
	if token == "" {
		return errors.New("the HTTP transport needs a token")
	}
	addr, err := ListenAddr(addr)
	if err != nil {
		return err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	logger := cfg.Logger
	server := &http.Server{
		Handler:           HTTPHandler(cfg, token, logger),
		ReadHeaderTimeout: httpHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
		ErrorLog:          slog.NewLogLogger(cmpLogger(logger).Handler(), slog.LevelWarn),
	}
	go func() {
		<-ctx.Done()
		// ctx is done by now: shut down gracefully on a fresh deadline.
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if ready != nil {
		ready("http://" + ln.Addr().String() + HTTPPath)
	}
	if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func cmpLogger(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.New(slog.DiscardHandler)
	}
	return l
}
