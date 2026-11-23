// Package rest is the REST Protocol: it encodes a call into an HTTP request
// (via internal/proxy, credential-free) and decodes the response into a
// neutral Result (SPEC §3.5 P4/§3.7 P5).
package rest

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol"
	"github.com/swayam5342/plugit/internal/proxy"
)

// REST serves REST tools. Target carries the fixed params and user agent;
// the credential is attached by invoke (AttachAuth), not here (T17).
type REST struct {
	Target proxy.Target
}

// Name reports the protocol.
func (REST) Name() model.Protocol { return model.REST }

// Encode routes the validated arguments into an *http.Request with no
// credential attached (SPEC §3.7 P5 step 6).
func (r REST) Encode(ctx context.Context, c protocol.Call) (*http.Request, error) {
	tg := r.Target
	tg.BaseURL = c.Endpoint
	return proxy.Encode(ctx, c.Tool, c.Args, tg)
}

// Describe returns the REST method and path for the TUI and `try`.
func (REST) Describe(t model.Tool) (badge, target string) {
	if t.HTTP != nil {
		return t.HTTP.Method, t.HTTP.Path
	}
	return "", string(t.Key)
}

// Decode shapes the response into a Result (SPEC §3.7 P5 step 9). The body is
// kept as raw JSON bytes so numbers, key order and escaping survive; response
// field filtering and redaction happen later in invoke.
func (REST) Decode(_ protocol.Call, resp *http.Response, body []byte, truncated bool) protocol.Result {
	ct := resp.Header.Get("Content-Type")
	res := protocol.Result{
		Code:    fmt.Sprintf("HTTP %d", resp.StatusCode),
		IsError: resp.StatusCode >= 400,
	}
	status := fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	if ct != "" {
		status += " " + ct
	}
	res.Status = status

	switch {
	case len(body) == 0:
		res.Text = fmt.Sprintf("(empty response, status %d)", resp.StatusCode)
	case isBinary(ct, body):
		typ := ct
		if typ == "" {
			typ = "unknown"
		}
		res.Binary = &protocol.BinaryInfo{Bytes: len(body), Type: typ}
		res.Text = fmt.Sprintf("[binary %d bytes, type %s]", len(body), typ)
	case !res.IsError && !truncated && looksJSON(ct, body):
		res.Value = append([]byte(nil), body...)
	default:
		res.Text = string(body)
	}
	return res
}

func isBinary(ct string, body []byte) bool {
	mt, _, _ := mime.ParseMediaType(ct)
	switch {
	case strings.HasPrefix(mt, "image/"), strings.HasPrefix(mt, "audio/"), strings.HasPrefix(mt, "video/"),
		strings.HasPrefix(mt, "font/"), mt == "application/octet-stream", mt == "application/pdf",
		mt == "application/zip", mt == "application/gzip":
		return true
	}
	return !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0
}

func looksJSON(ct string, body []byte) bool {
	mt, _, _ := mime.ParseMediaType(ct)
	if strings.HasSuffix(mt, "json") {
		return true
	}
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}
