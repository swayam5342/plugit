package cli

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/config"
)

type fakeKeyring struct {
	items map[string]string
	err   error
}

func (k *fakeKeyring) Set(service, key, value string) error {
	if k.err != nil {
		return k.err
	}
	if k.items == nil {
		k.items = map[string]string{}
	}
	k.items[service+"/"+key] = value
	return nil
}

// setupAuthAPI writes a pets config whose auth block is replaced by auth.
func setupAuthAPI(t *testing.T, auth string) config.Paths {
	t.Helper()
	p := setupAPI(t, "https://pets.example.test")
	cfg, _ := p.APIConfig("pets")
	y := strings.Replace(fmt.Sprintf(petsYAML, "https://pets.example.test"),
		"auth: {type: header, header: X-Api-Key, secret: env:PETS_KEY}", auth, 1)
	if err := config.WriteFileAtomic(cfg, []byte(y)); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAuthStoresKeyringSecret(t *testing.T) {
	p := setupAuthAPI(t, "auth: {type: bearer, secret: 'keyring:plugit/pets'}")
	kr := &fakeKeyring{}
	var out, errOut bytes.Buffer
	err := authAPI(t.Context(), p, "pets", authDeps{in: strings.NewReader(runSecret + "\r\n"), out: &out, errOut: &errOut, keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	if kr.items["plugit/pets"] != runSecret {
		t.Errorf("keyring = %v", kr.items)
	}
	if !strings.Contains(out.String(), "keyring:plugit/pets") {
		t.Errorf("confirmation should name the keyring item: %q", out.String())
	}
	if strings.Contains(out.String()+errOut.String(), runSecret) {
		t.Errorf("secret echoed: out=%q err=%q", out.String(), errOut.String())
	}
}

func TestAuthRejects(t *testing.T) {
	cases := map[string]struct {
		auth  string
		input string
		kr    *fakeKeyring
		want  string
	}{
		"env reference": {"auth: {type: bearer, secret: env:PETS_KEY}", runSecret + "\n", nil,
			"PETS_KEY"},
		"no auth configured": {"auth: {type: none}", runSecret + "\n", nil, "auth.type"},
		"empty input":        {"auth: {type: bearer, secret: 'keyring:plugit/pets'}", "\n", nil, "empty"},
		"no input":           {"auth: {type: bearer, secret: 'keyring:plugit/pets'}", "", nil, "empty"},
		"control characters": {"auth: {type: bearer, secret: 'keyring:plugit/pets'}", "abc\x00def\n", nil, "control"},
		"basic without colon": {"auth: {type: basic, secret: 'keyring:plugit/pets'}", runSecret + "\n", nil,
			"user:password"},
		"keyring failure": {"auth: {type: bearer, secret: 'keyring:plugit/pets'}", runSecret + "\n",
			&fakeKeyring{err: errors.New("backend says no")}, "keyring"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := setupAuthAPI(t, tc.auth)
			kr := tc.kr
			if kr == nil {
				kr = &fakeKeyring{}
			}
			var out, errOut bytes.Buffer
			err := authAPI(t.Context(), p, "pets", authDeps{in: strings.NewReader(tc.input), out: &out, errOut: &errOut, keyring: kr})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
			if len(kr.items) != 0 {
				t.Errorf("stored despite the error: %v", kr.items)
			}
			if strings.Contains(err.Error()+out.String()+errOut.String(), runSecret) {
				t.Errorf("secret echoed: %v", err)
			}
		})
	}
}

// Input is capped so a stray pipe can't exhaust memory.
func TestAuthInputCap(t *testing.T) {
	p := setupAuthAPI(t, "auth: {type: bearer, secret: 'keyring:plugit/pets'}")
	kr := &fakeKeyring{}
	err := authAPI(t.Context(), p, "pets", authDeps{in: strings.NewReader(strings.Repeat("a", maxSecretBytes+10)), out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, keyring: kr})
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("err = %v", err)
	}
}
