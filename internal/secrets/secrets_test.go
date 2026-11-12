package secrets

import (
	"errors"
	"strings"
	"testing"
)

const fakeSecret = "test-token-123"

func TestParseRef(t *testing.T) {
	tests := []struct {
		in     string
		scheme Scheme
		a, b   string
	}{
		{"literal:2022-11-28", Literal, "2022-11-28", ""},
		{"literal:", Literal, "", ""},
		{"literal:a:b:c", Literal, "a:b:c", ""},
		{"env:GITHUB_TOKEN", Env, "GITHUB_TOKEN", ""},
		{"env:_x9", Env, "_x9", ""},
		{"keyring:plugit/github", Keyring, "plugit", "github"},
		{"keyring:plugit/github/extra", Keyring, "plugit", "github/extra"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			r, err := ParseRef(tc.in)
			if err != nil {
				t.Fatalf("ParseRef: %v", err)
			}
			if r.Scheme != tc.scheme {
				t.Errorf("scheme = %v, want %v", r.Scheme, tc.scheme)
			}
			switch r.Scheme {
			case Literal:
				if r.Literal != tc.a {
					t.Errorf("literal = %q, want %q", r.Literal, tc.a)
				}
			case Env:
				if r.Var != tc.a {
					t.Errorf("var = %q, want %q", r.Var, tc.a)
				}
			case Keyring:
				if r.Service != tc.a || r.Key != tc.b {
					t.Errorf("keyring = %q/%q, want %q/%q", r.Service, r.Key, tc.a, tc.b)
				}
			}
			if r.String() != tc.in {
				t.Errorf("String() = %q, want %q", r.String(), tc.in)
			}
		})
	}
}

func TestParseRefRejects(t *testing.T) {
	for _, in := range []string{
		"", "ghp_abc", fakeSecret, "Bearer xyz", // bare values: no scheme
		"env:", "env:1BAD", "env:A-B", "env:A B",
		"keyring:", "keyring:svc", "keyring:/key", "keyring:svc/",
		"file:/etc/passwd", "vault:x",
	} {
		t.Run(in, func(t *testing.T) {
			_, err := ParseRef(in)
			if err == nil {
				t.Fatalf("ParseRef(%q) succeeded", in)
			}
			if in != "" && strings.Contains(err.Error(), in) && !strings.Contains(in, ":") {
				t.Errorf("error echoes the bare value: %v", err)
			}
		})
	}
}

func TestParseRefBareValueErrorNeverEchoes(t *testing.T) {
	_, err := ParseRef(fakeSecret)
	if !errors.Is(err, ErrNoScheme) {
		t.Fatalf("err = %v, want ErrNoScheme", err)
	}
	if strings.Contains(err.Error(), fakeSecret) {
		t.Errorf("error leaks the pasted value: %v", err)
	}
}

func TestParseSecretRef(t *testing.T) {
	if _, err := ParseSecretRef("literal:" + fakeSecret); !errors.Is(err, ErrLiteralSecret) {
		t.Errorf("literal: err = %v, want ErrLiteralSecret", err)
	}
	if _, err := ParseSecretRef("literal:" + fakeSecret); err != nil && strings.Contains(err.Error(), fakeSecret) {
		t.Errorf("error leaks the literal: %v", err)
	}
	for _, ok := range []string{"env:TOKEN", "keyring:plugit/x"} {
		if _, err := ParseSecretRef(ok); err != nil {
			t.Errorf("ParseSecretRef(%q) = %v", ok, err)
		}
	}
}

func TestIsSecret(t *testing.T) {
	for in, want := range map[string]bool{"literal:v2": false, "env:X": true, "keyring:a/b": true} {
		r, _ := ParseRef(in)
		if r.IsSecret() != want {
			t.Errorf("%s IsSecret = %v, want %v", in, r.IsSecret(), want)
		}
	}
}

type fakeKeyring map[string]string

func (f fakeKeyring) Get(service, key string) (string, error) {
	v, ok := f[service+"/"+key]
	if !ok {
		return "", ErrKeyringItemNotFound
	}
	return v, nil
}

func resolver(env map[string]string, kr fakeKeyring) *Resolver {
	return &Resolver{
		LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		Keyring:   kr,
	}
}

func TestResolve(t *testing.T) {
	r := resolver(map[string]string{"TOKEN": fakeSecret}, fakeKeyring{"plugit/gh": "kr-" + fakeSecret})
	for in, want := range map[string]string{
		"literal:v2":        "v2",
		"env:TOKEN":         fakeSecret,
		"keyring:plugit/gh": "kr-" + fakeSecret,
	} {
		ref, _ := ParseRef(in)
		got, err := r.Resolve(ref)
		if err != nil || got != want {
			t.Errorf("Resolve(%s) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestResolveErrors(t *testing.T) {
	r := resolver(map[string]string{"EMPTY": ""}, fakeKeyring{"plugit/empty": ""})
	tests := []struct {
		in   string
		want error
	}{
		{"env:MISSING", ErrNotFound},
		{"env:EMPTY", ErrEmpty},
		{"keyring:plugit/missing", ErrNotFound},
		{"keyring:plugit/empty", ErrEmpty},
	}
	for _, tc := range tests {
		ref, _ := ParseRef(tc.in)
		_, err := r.Resolve(ref)
		if !errors.Is(err, tc.want) {
			t.Errorf("Resolve(%s) err = %v, want %v", tc.in, err, tc.want)
		}
		if err != nil && !strings.Contains(err.Error(), strings.SplitN(tc.in, ":", 2)[1]) {
			t.Errorf("Resolve(%s) error should name the reference: %v", tc.in, err)
		}
	}
}

type failingKeyring struct{}

func (failingKeyring) Get(string, string) (string, error) {
	// A backend error that (badly) includes the secret must not be surfaced.
	return "", errors.New("backend exploded near " + fakeSecret)
}

func TestResolveKeyringBackendErrorIsNotEchoed(t *testing.T) {
	r := &Resolver{LookupEnv: func(string) (string, bool) { return "", false }, Keyring: failingKeyring{}}
	ref, _ := ParseRef("keyring:plugit/x")
	_, err := r.Resolve(ref)
	if err == nil || !errors.Is(err, ErrKeyring) {
		t.Fatalf("err = %v, want ErrKeyring", err)
	}
	if strings.Contains(err.Error(), fakeSecret) {
		t.Errorf("keyring backend error leaked: %v", err)
	}
}
