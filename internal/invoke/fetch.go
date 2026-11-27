package invoke

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/redact"
)

// ErrSecretInResponse means a Fetch response body carried a secret in play,
// so it was refused rather than returned (it would become spec text).
var ErrSecretInResponse = errors.New("the result contains the API credential or a secret fixed value")

// Fetch sends one request that isn't a tool call (plugit add --introspect)
// through the same credential step as Perform: the credential is attached
// only when req is for d.Endpoint's origin (T8), then d.Doer sends it (the
// httpx guards and response cap apply) and the body is read. A body over the
// client's cap is returned truncated, not as an error. Errors describe the
// failure without the request URL or any credential, and keep the httpx
// sentinel errors for errors.Is. A 401 calls cred.Unauthorized, as Perform
// does, so a stale OAuth token is dropped.
//
// The body is refused with ErrSecretInResponse when it contains a secret in
// play, in any form d.Redactor would mask (raw, percent-encoded, base64,
// JSON-escaped): the credential (an OAuth access token included, which only
// Fetch knows) and every secret d.Redactor holds (the caller passes the
// resolved secret fixed values there). The caller turns the body into spec
// text, which reaches disk, the TUI and the model, so it is refused rather
// than masked (SPEC T11). d.Proto and d.Timeout are not used.
func Fetch(ctx context.Context, d Deps, req *http.Request) (body []byte, truncated bool, status int, err error) {
	cred := d.Cred
	if cred.Token != nil {
		tok, err := cred.Token(ctx)
		if err != nil {
			return nil, false, 0, errors.New("couldn't get an access token")
		}
		cred.Secret = tok
	}
	if err := attachAuth(req, cred, d.Endpoint); err != nil {
		return nil, false, 0, fetchError{msg: describeTransportError(ctx, err), err: err}
	}
	resp, err := d.Doer.Do(req)
	if err != nil {
		return nil, false, 0, fetchError{msg: describeTransportError(ctx, err), err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err = io.ReadAll(resp.Body)
	truncated = errors.Is(err, httpx.ErrResponseTruncated)
	if err != nil && !truncated {
		err = fmt.Errorf("reading response: %w", err)
		return nil, false, resp.StatusCode, fetchError{msg: describeTransportError(ctx, err), err: err}
	}
	if resp.StatusCode == http.StatusUnauthorized && cred.Unauthorized != nil {
		cred.Unauthorized()
	}
	red := d.Redactor
	if red == nil {
		red = redact.New(redact.Options{})
	}
	if red.With(cred.SecretValues()...).ContainsSecret(string(body)) {
		return nil, false, resp.StatusCode, ErrSecretInResponse
	}
	return body, truncated, resp.StatusCode, nil
}

// fetchError is a described transport failure: its text never repeats the
// URL (which may carry a credential), and it still unwraps to the cause.
type fetchError struct {
	msg string
	err error
}

func (e fetchError) Error() string { return e.msg }
func (e fetchError) Unwrap() error { return e.err }
