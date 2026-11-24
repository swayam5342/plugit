package invoke

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/swayam5342/plugit/internal/httpx"
)

// errBuild marks a request that cannot be built or credentialed.
var errBuild = errors.New("cannot build request")

// attachAuth attaches the resolved credential, only when the request origin
// matches the pinned base URL (T8). It is invoke's step (SPEC §3.7): a protocol
// package never attaches a credential or calls httpx (T17).
func attachAuth(req *http.Request, a Credential, endpoint *url.URL) error {
	if a.Type == "" || a.Type == credNone {
		return nil
	}
	if !httpx.SameOrigin(req.URL, endpoint) {
		return fmt.Errorf("%w: request origin differs from the pinned base URL", errBuild)
	}
	if hasControl(a.Secret) {
		return fmt.Errorf("%w: the credential contains control characters", errBuild)
	}
	switch a.Type {
	case credBearer:
		req.Header.Set("Authorization", "Bearer "+a.Secret)
	case credHeader:
		if a.Header == "" {
			return fmt.Errorf("%w: auth.header is required for header auth", errBuild)
		}
		req.Header.Del(a.Header)
		req.Header.Set(a.Header, a.Secret)
	case credQuery:
		if a.Param == "" {
			return fmt.Errorf("%w: auth.param is required for query auth", errBuild)
		}
		q := req.URL.Query()
		q.Set(a.Param, a.Secret)
		req.URL.RawQuery = q.Encode()
	case credBasic:
		if !strings.Contains(a.Secret, ":") {
			return fmt.Errorf("%w: basic auth secrets must look like user:password", errBuild)
		}
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(a.Secret)))
	default:
		return fmt.Errorf("%w: unknown auth type %q", errBuild, a.Type)
	}
	return nil
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
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
