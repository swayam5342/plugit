package config

import (
	"errors"
	"strings"
	"testing"
)

// Version 4 adds OAuth2 (SPEC M6 design note: OAuth2).
func TestOAuth2Auth(t *testing.T) {
	withAuth := func(auth string) string {
		return strings.Replace(minimalConfig, "toolsets:", auth+"\ntoolsets:", 1)
	}
	cc := withAuth(`auth: {type: oauth2, flow: client_credentials, token_url: "https://auth.example.test/oauth/token", client_id: my-client, secret: "keyring:plugit/pets-client", scopes: [read, write]}`)
	c, err := ParseAPI([]byte(cc))
	if err != nil {
		t.Fatal(err)
	}
	a := c.Auth
	if a.Type != AuthOAuth2 || a.Flow != "client_credentials" || a.TokenURL != "https://auth.example.test/oauth/token" || a.ClientID != "my-client" || len(a.Scopes) != 2 {
		t.Errorf("auth = %+v", a)
	}
	device := withAuth(`auth: {type: oauth2, flow: device_code, token_url: "https://auth.example.test/token", device_url: "https://auth.example.test/device", client_id: cli}`)
	if _, err := ParseAPI([]byte(device)); err != nil {
		t.Errorf("device flow without a client secret: %v", err)
	}
	local := withAuth(`auth: {type: oauth2, flow: device_code, token_url: "http://127.0.0.1:9000/token", device_url: "http://localhost:9000/device", client_id: cli}`)
	if _, err := ParseAPI([]byte(local)); err != nil {
		t.Errorf("loopback http endpoints: %v", err)
	}

	bad := map[string]string{
		"no flow":               `auth: {type: oauth2, token_url: "https://a.example.test/t", client_id: c, secret: "env:S"}`,
		"unknown flow":          `auth: {type: oauth2, flow: password, token_url: "https://a.example.test/t", client_id: c, secret: "env:S"}`,
		"no token_url":          `auth: {type: oauth2, flow: client_credentials, client_id: c, secret: "env:S"}`,
		"http token_url":        `auth: {type: oauth2, flow: client_credentials, token_url: "http://a.example.test/t", client_id: c, secret: "env:S"}`,
		"token_url query":       `auth: {type: oauth2, flow: client_credentials, token_url: "https://a.example.test/t?k=v", client_id: c, secret: "env:S"}`,
		"token_url userinfo":    `auth: {type: oauth2, flow: client_credentials, token_url: "https://u:p@a.example.test/t", client_id: c, secret: "env:S"}`,
		"no client_id":          `auth: {type: oauth2, flow: client_credentials, token_url: "https://a.example.test/t", secret: "env:S"}`,
		"cc without secret":     `auth: {type: oauth2, flow: client_credentials, token_url: "https://a.example.test/t", client_id: c}`,
		"literal secret":        `auth: {type: oauth2, flow: client_credentials, token_url: "https://a.example.test/t", client_id: c, secret: "hunter2"}`,
		"device no device_url":  `auth: {type: oauth2, flow: device_code, token_url: "https://a.example.test/t", client_id: c}`,
		"oauth with header":     `auth: {type: oauth2, flow: device_code, token_url: "https://a.example.test/t", device_url: "https://a.example.test/d", client_id: c, header: X-Key}`,
		"bad scope":             `auth: {type: oauth2, flow: device_code, token_url: "https://a.example.test/t", device_url: "https://a.example.test/d", client_id: c, scopes: ["a b"]}`,
		"oauth field on bearer": `auth: {type: bearer, secret: "env:S", token_url: "https://a.example.test/t"}`,
	}
	for name, auth := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAPI([]byte(withAuth(auth))); err == nil || !strings.Contains(err.Error(), "auth") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestCredentialsNeedHTTPS(t *testing.T) {
	const (
		bearer = "auth: {type: bearer, secret: \"env:PETS_TOKEN\"}\n"
		secret = "fixed: [{name: api_key, value: \"env:PETS_KEY\"}]\n"
		public = "fixed: [{name: version, value: \"literal:2\"}]\n"
	)
	with := func(baseURL, extra string) string {
		return strings.Replace(minimalConfig, "endpoint: https://pets.example.test/v1", "endpoint: "+baseURL, 1) + extra
	}
	if !strings.Contains(minimalConfig, "endpoint: https://pets.example.test/v1") {
		t.Fatal("minimalConfig changed: update this test")
	}
	cases := map[string]struct {
		yaml string
		ok   bool
	}{
		"https with auth":               {with("https://pets.example.test/v1", bearer), true},
		"http without credentials":      {with("http://pets.example.test/v1", ""), true},
		"http with a literal fixed":     {with("http://pets.example.test/v1", public), true},
		"http with auth":                {with("http://pets.example.test/v1", bearer), false},
		"http with a secret fixed":      {with("http://pets.example.test/v1", secret), false},
		"http to localhost with auth":   {with("http://localhost:8080/v1", bearer), true},
		"http to 127.0.0.1 with auth":   {with("http://127.0.0.1:8080/v1", bearer), true},
		"http to [::1] with auth":       {with("http://[::1]:8080/v1", bearer), true},
		"http, allow_private, auth":     {with("http://intranet.example.test/v1", bearer+"network: {allow_private: true}\n"), false},
		"http, allow_insecure, auth":    {with("http://intranet.example.test/v1", bearer+"network: {allow_insecure: true}\n"), true},
		"http, allow_insecure, fixed":   {with("http://pets.example.test/v1", secret+"network: {allow_insecure: true}\n"), true},
		"http to a lookalike with auth": {with("http://localhost.example.test/v1", bearer), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAPI([]byte(tc.yaml))
			switch {
			case tc.ok && err != nil:
				t.Errorf("unexpected error: %v", err)
			case !tc.ok && (!errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "https") || !strings.Contains(err.Error(), "allow_insecure")):
				t.Errorf("err = %v, want an ErrInvalidConfig about https that names allow_insecure", err)
			}
		})
	}
}

func TestAllowInsecure(t *testing.T) {
	c, err := ParseAPI([]byte(minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	if c.Network.AllowInsecure {
		t.Error("allow_insecure defaults to on")
	}
	if out, err := MarshalAPI(c); err != nil || strings.Contains(string(out), "allow_insecure") {
		t.Errorf("an off switch is written out: %s, %v", out, err)
	}
	on := strings.Replace(minimalConfig, "toolsets:", "network: {allow_insecure: true}\ntoolsets:", 1)
	c, err = ParseAPI([]byte(on))
	if err != nil || !c.Network.AllowInsecure {
		t.Fatalf("allow_insecure: true not read: %+v, %v", c, err)
	}
	if out, err := MarshalAPI(c); err != nil || !strings.Contains(string(out), "allow_insecure: true") {
		t.Errorf("marshal = %s, %v", out, err)
	}
}
