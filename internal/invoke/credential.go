package invoke

import (
	"context"
	"strings"
)

// Credential types. They match the config auth types (SPEC §3.6); oauth2
// resolves to "bearer" with a Token source.
const (
	credNone   = "none"
	credBearer = "bearer"
	credHeader = "header"
	credQuery  = "query"
	credBasic  = "basic"
)

// Credential is a resolved, protocol-neutral credential. invoke attaches it to
// the request a protocol encoded, only when the request origin matches the
// pinned endpoint (T8). A protocol package never sees it (T17).
type Credential struct {
	Type         string
	Header       string
	Param        string
	Secret       string
	Token        func(context.Context) (string, error)
	Unauthorized func()
	// Extra lists further values to redact that are not attached (for example
	// an OAuth client secret).
	Extra []string
}

// SecretValues lists every credential value that must be redacted wherever it
// appears (SPEC T11): the credential, the password part of a Basic credential
// on its own (an upstream may echo just that), and Extra.
func (c Credential) SecretValues() []string {
	var out []string
	add := func(s string) {
		if s != "" {
			out = append(out, s)
		}
	}
	if c.Type != credNone && c.Type != "" {
		add(c.Secret)
		if c.Type == credBasic {
			if _, pass, ok := strings.Cut(c.Secret, ":"); ok {
				add(pass)
			}
		}
	}
	for _, e := range c.Extra {
		add(e)
	}
	return out
}
