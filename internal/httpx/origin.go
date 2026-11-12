package httpx

import (
	"net"
	"net/url"
	"strings"
)

// Origin returns u's origin as scheme://host:port, lowercased, with the
// scheme's default port filled in. It returns "" when u has no host.
func Origin(u *url.URL) string {
	if u == nil || u.Hostname() == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// SameOrigin reports whether a and b have exactly the same scheme, host and
// port. Credentials are attached only when this holds against the pinned
// base_url (SPEC §5.3, T8).
func SameOrigin(a, b *url.URL) bool {
	oa, ob := Origin(a), Origin(b)
	return oa != "" && oa == ob
}
