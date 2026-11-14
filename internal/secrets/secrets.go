// Package secrets parses and resolves value references (SPEC §3.6, T10):
//
//	literal:VALUE        a non-secret value, used as-is
//	env:VAR              an environment variable
//	keyring:SERVICE/KEY  an OS keyring item
//
// Every reference needs an explicit scheme, so a secret can't be pasted in as
// a bare string by accident. Secrets themselves (auth credentials, and secrets
// on the command line, where other local users can read them) must be env: or
// keyring:. Errors name the reference, never the value.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	ErrNoScheme            = errors.New("value needs an explicit literal:, env: or keyring: prefix")
	ErrLiteralSecret       = errors.New("secrets must come from env: or keyring:, not a literal value")
	ErrInvalidRef          = errors.New("invalid reference")
	ErrNotFound            = errors.New("secret not found")
	ErrEmpty               = errors.New("secret is empty")
	ErrKeyring             = errors.New("keyring error")
	ErrKeyringItemNotFound = errors.New("keyring item not found")
)

type Scheme int

const (
	Literal Scheme = iota + 1
	Env
	Keyring
)

type Ref struct {
	Scheme  Scheme
	Literal string
	Var     string
	Service string
	Key     string
}

var envVarRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseRef parses a reference with an explicit scheme.
func ParseRef(s string) (Ref, error) {
	scheme, rest, ok := strings.Cut(s, ":")
	if !ok {
		return Ref{}, ErrNoScheme
	}
	switch scheme {
	case "literal":
		return Ref{Scheme: Literal, Literal: rest}, nil
	case "env":
		if !envVarRe.MatchString(rest) {
			return Ref{}, fmt.Errorf("%w: env:%s is not a valid variable name", ErrInvalidRef, rest)
		}
		return Ref{Scheme: Env, Var: rest}, nil
	case "keyring":
		service, key, ok := strings.Cut(rest, "/")
		if !ok || service == "" || key == "" {
			return Ref{}, fmt.Errorf("%w: keyring references look like keyring:service/key", ErrInvalidRef)
		}
		return Ref{Scheme: Keyring, Service: service, Key: key}, nil
	default:
		return Ref{}, ErrNoScheme
	}
}

// ParseSecretRef parses a reference that must hold a secret: env: or
// keyring: only.
func ParseSecretRef(s string) (Ref, error) {
	r, err := ParseRef(s)
	if err != nil {
		return Ref{}, err
	}
	if r.Scheme == Literal {
		return Ref{}, ErrLiteralSecret
	}
	return r, nil
}

// IsSecret reports whether the value is secret (env: or keyring:). Secret
// values are redacted wherever they appear (SPEC T11).
func (r Ref) IsSecret() bool { return r.Scheme == Env || r.Scheme == Keyring }

// String returns the reference in its parseable form. It never contains a
// resolved secret.
func (r Ref) String() string {
	switch r.Scheme {
	case Literal:
		return "literal:" + r.Literal
	case Env:
		return "env:" + r.Var
	case Keyring:
		return "keyring:" + r.Service + "/" + r.Key
	}
	return ""
}

type KeyringGetter interface {
	Get(service, key string) (string, error)
}

type Resolver struct {
	LookupEnv func(string) (string, bool)
	Keyring   KeyringGetter
}

// NewResolver resolves against the process environment and the OS keyring.
func NewResolver() *Resolver {
	return &Resolver{LookupEnv: os.LookupEnv, Keyring: OSKeyring{}}
}

// Resolve returns the value r refers to. Missing and empty secrets are errors.
func (res *Resolver) Resolve(r Ref) (string, error) {
	switch r.Scheme {
	case Literal:
		return r.Literal, nil
	case Env:
		v, ok := res.LookupEnv(r.Var)
		if !ok {
			return "", fmt.Errorf("%w: environment variable %s is not set", ErrNotFound, r.Var)
		}
		if v == "" {
			return "", fmt.Errorf("%w: environment variable %s is empty", ErrEmpty, r.Var)
		}
		return v, nil
	case Keyring:
		v, err := res.Keyring.Get(r.Service, r.Key)
		if errors.Is(err, ErrKeyringItemNotFound) {
			return "", fmt.Errorf("%w: keyring item %s/%s (run `plugit auth`)", ErrNotFound, r.Service, r.Key)
		}
		if err != nil {
			return "", fmt.Errorf("%w: reading keyring item %s/%s failed", ErrKeyring, r.Service, r.Key)
		}
		if v == "" {
			return "", fmt.Errorf("%w: keyring item %s/%s is empty", ErrEmpty, r.Service, r.Key)
		}
		return v, nil
	}
	return "", ErrInvalidRef
}
