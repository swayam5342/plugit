package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/jsonpick"
	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/redact"
)

// Result is a tool result, ready for MCP. Text is always redacted.
type Result struct {
	Text      string
	IsError   bool
	Status    int // HTTP status, 0 if no response
	Bytes     int // response body bytes read
	Truncated bool
}

// Call builds the request, sends it and shapes the response (SPEC §3.7
// steps 5-8). It never returns a Go error: every failure is an isError
// result whose text starts with the tool name. All text passes through red,
// so no resolved secret reaches the model, successful responses included.
//
// pick, when not nil, keeps only the tool's response_fields of a complete
// JSON success response (SPEC M6 design note: response field filtering).
func Call(ctx context.Context, doer httpx.Doer, t model.Tool, args map[string]any, tg Target, red *redact.Redactor, pick *jsonpick.Picker) Result {
	if tg.Auth.Token != nil {
		tok, err := tg.Auth.Token(ctx)
		if err != nil {
			return failure(t, red, "couldn't get an access token: "+err.Error())
		}
		tg.Auth.Secret = tok
		red = red.With(tok) // fetched while serving: unknown when red was built
	}
	req, err := Build(ctx, t, args, tg)
	if err != nil {
		return failure(t, red, "invalid arguments: "+err.Error())
	}
	resp, err := doer.Do(req)
	if err != nil {
		return failure(t, red, describeTransportError(ctx, err))
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	truncated := errors.Is(err, httpx.ErrResponseTruncated)
	if err != nil && !truncated {
		return failure(t, red, describeTransportError(ctx, fmt.Errorf("reading response: %w", err)))
	}

	res := Result{Status: resp.StatusCode, Bytes: len(body), Truncated: truncated, IsError: resp.StatusCode >= 400}
	if resp.StatusCode == http.StatusUnauthorized && tg.Auth.Unauthorized != nil {
		tg.Auth.Unauthorized()
	}
	if res.IsError || truncated {
		pick = nil // errors and cut-off bodies are shown whole
	}
	text := shape(resp, body, truncated, pick)
	if res.IsError {
		text = fmt.Sprintf("%s: upstream returned HTTP %d %s\n\n%s", t.Name, resp.StatusCode, http.StatusText(resp.StatusCode), text)
	}
	res.Text = red.String(text)
	return res
}

func failure(t model.Tool, red *redact.Redactor, msg string) Result {
	return Result{Text: red.String(t.Name + ": " + msg), IsError: true}
}

// describeTransportError explains a failed exchange without repeating the
// request URL, which may carry a credential in its query.
func describeTransportError(ctx context.Context, err error) string {
	var nerr net.Error
	switch {
	case errors.Is(err, httpx.ErrBlockedAddress):
		return "request blocked: the API address is in a private or reserved network range (" +
			innermost(err) + "); set network.allow_private to allow private hosts"
	case errors.Is(err, httpx.ErrRedirectBlocked):
		return "request blocked: the API redirected to a different origin, and credentials are only sent to the pinned base URL"
	case errors.Is(err, httpx.ErrTooManyRedirects):
		return "request failed: too many redirects"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded),
		errors.As(err, &nerr) && nerr.Timeout():
		return "request timed out"
	case errors.Is(err, context.Canceled):
		return "request canceled"
	}
	return "request failed: " + innermost(err)
}

// innermost strips *url.Error (whose message includes the full URL).
func innermost(err error) string {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		err = ue.Err
	}
	return err.Error()
}

func shape(resp *http.Response, body []byte, truncated bool, pick *jsonpick.Picker) string {
	ct := resp.Header.Get("Content-Type")
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP %d %s\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	if ct != "" {
		fmt.Fprintf(&b, "Content-Type: %s\n", ct)
	}
	if pick != nil {
		if filtered, ok := filterJSON(ct, body, pick); ok {
			body = filtered
			fmt.Fprintf(&b, "Filtered to fields: %s (the tool's response_fields; other fields were left out)\n", strings.Join(pick.Paths(), ", "))
		}
	}
	b.WriteString("\n")

	switch {
	case len(body) == 0:
		fmt.Fprintf(&b, "(empty response, status %d)", resp.StatusCode)
	case isBinary(ct, body):
		typ := ct
		if typ == "" {
			typ = "unknown"
		}
		fmt.Fprintf(&b, "[binary %d bytes, type %s]", len(body), typ)
	default:
		b.Write(prettyJSON(ct, body))
	}
	if truncated {
		fmt.Fprintf(&b, "\n[truncated: response exceeded %d bytes]", len(body))
	}
	return b.String()
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

// filterJSON applies pick to a JSON body. ok is false for anything that
// isn't complete JSON, which is then shown unfiltered.
func filterJSON(ct string, body []byte, pick *jsonpick.Picker) ([]byte, bool) {
	if isBinary(ct, body) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // keep numbers exactly as sent
	var v any
	if dec.Decode(&v) != nil || dec.More() {
		return nil, false
	}
	switch v.(type) {
	case map[string]any, []any:
	default:
		return nil, false
	}
	out, err := json.Marshal(pick.Apply(v))
	if err != nil {
		return nil, false
	}
	return out, true
}

// prettyJSON indents JSON bodies; anything else (or JSON cut off by the size
// cap) is returned unchanged.
func prettyJSON(ct string, body []byte) []byte {
	mt, _, _ := mime.ParseMediaType(ct)
	trimmed := bytes.TrimSpace(body)
	looksJSON := strings.HasSuffix(mt, "json") || (len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '['))
	if !looksJSON {
		return body
	}
	var out bytes.Buffer
	if err := json.Indent(&out, trimmed, "", "  "); err != nil {
		return body
	}
	return out.Bytes()
}
