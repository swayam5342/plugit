package redact

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Fake tokens are assembled at runtime so secret scanners don't flag the
// test source. None of these are real credentials.
var (
	fakeGitHub    = "ghp_" + strings.Repeat("A1b2", 9)
	fakeGitHubPAT = "github_pat_" + strings.Repeat("Ab3_", 10)
	fakeStripe    = "sk_" + "live_" + strings.Repeat("Z9y8", 6)
	fakeSlack     = "xoxb-" + strings.Repeat("1234-", 3) + "abcdefABCDEF"
	fakeAWS       = "AKIA" + strings.Repeat("Q", 16)
	fakeGoogle    = "AIza" + strings.Repeat("x", 35)
	fakeOpenAI    = "sk-" + "proj-" + strings.Repeat("abcDEF123_", 3)
	fakeJWT       = "eyJhbGciOiJIUzI1NiJ9" + "." + "eyJzdWIiOiJ0ZXN0In0" + "." + "c2lnbmF0dXJlLXRlc3Q"
	fakePEM       = "-----BEGIN RSA PRIVATE KEY-----\nMIIBfakefakefake\nAAAA\n-----END RSA PRIVATE KEY-----"
)

func TestStringValueForms(t *testing.T) {
	const secret = `s3cr+t/=x y"q\z`
	r := New(Options{Secrets: []string{secret}})

	jsonForm, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	forms := map[string]string{
		"raw":           secret,
		"query-escape":  url.QueryEscape(secret),
		"path-escape":   url.PathEscape(secret),
		"base64-std":    base64.StdEncoding.EncodeToString([]byte(secret)),
		"base64-url":    base64.URLEncoding.EncodeToString([]byte(secret)),
		"base64-raw":    base64.RawStdEncoding.EncodeToString([]byte(secret)),
		"base64-rawurl": base64.RawURLEncoding.EncodeToString([]byte(secret)),
		"json-escaped":  strings.Trim(string(jsonForm), `"`),
	}
	for name, form := range forms {
		t.Run(name, func(t *testing.T) {
			in := "error: invalid key [" + form + "] rejected"
			got := r.String(in)
			if strings.Contains(got, form) {
				t.Errorf("String(%q) = %q, still contains the %s form", in, got, name)
			}
			if want := "error: invalid key [" + Mask + "] rejected"; got != want {
				t.Errorf("String(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

func TestStringBasicAuthCredential(t *testing.T) {
	cred := "alice:test-password-123"
	r := New(Options{Secrets: []string{cred}})
	header := "Basic " + base64.StdEncoding.EncodeToString([]byte(cred))
	if got, want := r.String(header), "Basic "+Mask; got != want {
		t.Errorf("String(%q) = %q, want %q", header, got, want)
	}
}

func TestStringLongestSecretWins(t *testing.T) {
	r := New(Options{Secrets: []string{"test-token", "test-token-123"}})
	got := r.String("a test-token-123 b test-token c")
	if want := "a " + Mask + " b " + Mask + " c"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStringIgnoresEmptySecrets(t *testing.T) {
	r := New(Options{Secrets: []string{"", "test-token-123"}})
	in := "nothing secret here"
	if got := r.String(in); got != in {
		t.Errorf("String(%q) = %q, want unchanged", in, got)
	}
}

func TestStringTokenPatterns(t *testing.T) {
	r := New(Options{})
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"github classic", "token " + fakeGitHub + " end", "token " + Mask + " end"},
		{"github fine-grained", "x=" + fakeGitHubPAT, "x=" + Mask},
		{"stripe", `{"key":"` + fakeStripe + `"}`, `{"key":"` + Mask + `"}`},
		{"slack", fakeSlack, Mask},
		{"aws access key id", "id " + fakeAWS + ".", "id " + Mask + "."},
		{"google api key", "k=" + fakeGoogle, "k=" + Mask},
		{"openai style", "use " + fakeOpenAI, "use " + Mask},
		{"jwt", "jwt " + fakeJWT + " ok", "jwt " + Mask + " ok"},
		{"bearer in text", "sent Authorization: Bearer abc.DEF-ghi_123~xyz", "sent Authorization: Bearer " + Mask},
		{"bearer lowercase", "bearer abcdefgh12345678", "bearer " + Mask},
		{"private key block", "key:\n" + fakePEM + "\nafter", "key:\n" + Mask + "\nafter"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.String(tc.in); got != tc.want {
				t.Errorf("String(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStringLeavesOrdinaryTextAlone(t *testing.T) {
	r := New(Options{})
	for _, in := range []string{
		"skeleton key ring",
		"GET /repos/{owner}/{repo}/issues returned 404",
		`{"id": 12345, "name": "eyJ", "state": "open"}`,
		"the bearer of bad news",
		"AKIA is a prefix",
	} {
		if got := r.String(in); got != in {
			t.Errorf("String(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestBytes(t *testing.T) {
	r := New(Options{Secrets: []string{"test-token-123"}})
	got := r.Bytes([]byte(`{"echo":"test-token-123"}`))
	if want := `{"echo":"` + Mask + `"}`; string(got) != want {
		t.Errorf("Bytes = %q, want %q", got, want)
	}
}

func TestHeader(t *testing.T) {
	r := New(Options{
		SecretHeaders: []string{"X-Tenant-Signature-Custom"},
		Secrets:       []string{"test-token-123"},
	})
	in := http.Header{}
	in.Set("Authorization", "Bearer test-token-123")
	in.Set("Proxy-Authorization", "Basic Zm9vOmJhcg==")
	in.Set("X-Api-Key", "anything")
	in.Set("X-Auth-Token", "anything")
	in.Set("X-Tenant-Signature-Custom", "anything")
	in.Set("Set-Cookie", "session=abc123; Path=/; HttpOnly")
	in.Add("Cookie", "session=abc123; theme=dark")
	in.Set("Accept", "application/json")
	in.Set("X-Echo", "you sent test-token-123")

	got := r.Header(in)

	wantMasked := []string{"Authorization", "Proxy-Authorization", "X-Api-Key", "X-Auth-Token", "X-Tenant-Signature-Custom", "Set-Cookie"}
	for _, name := range wantMasked {
		if v := got.Get(name); v != Mask {
			t.Errorf("%s = %q, want %q", name, v, Mask)
		}
	}
	if v, want := got.Get("Cookie"), "session="+Mask+"; theme="+Mask; v != want {
		t.Errorf("Cookie = %q, want %q", v, want)
	}
	if v := got.Get("Accept"); v != "application/json" {
		t.Errorf("Accept = %q, want unchanged", v)
	}
	if v, want := got.Get("X-Echo"), "you sent "+Mask; v != want {
		t.Errorf("X-Echo = %q, want %q", v, want)
	}
	if in.Get("Authorization") != "Bearer test-token-123" {
		t.Error("Header mutated its input")
	}
}

func TestHeaderNilIsNil(t *testing.T) {
	if got := New(Options{}).Header(nil); got != nil {
		t.Errorf("Header(nil) = %v, want nil", got)
	}
}

func TestURL(t *testing.T) {
	r := New(Options{
		SecretQueryParams: []string{"tenant_secret"},
		Secrets:           []string{"test-token-123"},
	})
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "default sensitive names, order preserved",
			in:   "https://api.example.com/v1/items?limit=10&api_key=abc&access_token=def&state=open",
			want: "https://api.example.com/v1/items?limit=10&api_key=" + Mask + "&access_token=" + Mask + "&state=open",
		},
		{
			name: "configured name",
			in:   "https://api.example.com/x?tenant_secret=zzz",
			want: "https://api.example.com/x?tenant_secret=" + Mask,
		},
		{
			name: "google style key param",
			in:   "https://api.example.com/x?key=zzz&q=cats",
			want: "https://api.example.com/x?key=" + Mask + "&q=cats",
		},
		{
			name: "secret value in path and non-sensitive param",
			in:   "https://api.example.com/users/test-token-123?q=test-token-123",
			want: "https://api.example.com/users/" + Mask + "?q=" + Mask,
		},
		{
			name: "userinfo password",
			in:   "https://bob:hunter2pass@api.example.com/x",
			want: "https://bob:" + Mask + "@api.example.com/x",
		},
		{
			name: "no query",
			in:   "https://api.example.com/x",
			want: "https://api.example.com/x",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got := r.URL(u); got != tc.want {
				t.Errorf("URL(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestURLNil(t *testing.T) {
	if got := New(Options{}).URL(nil); got != "" {
		t.Errorf("URL(nil) = %q, want empty", got)
	}
}

func TestIsSecretName(t *testing.T) {
	r := New(Options{SecretHeaders: []string{"X-Custom"}})
	secret := []string{"Authorization", "authorization", "X-API-Key", "api_key", "apikey", "Access-Token", "client_secret", "password", "X-Session-Id", "x-custom", "X-Amz-Security-Token", "X-Signature"}
	for _, n := range secret {
		if !r.IsSecretHeader(n) {
			t.Errorf("IsSecretHeader(%q) = false, want true", n)
		}
	}
	plain := []string{"Accept", "Content-Type", "X-Request-Id", "User-Agent", "X-GitHub-Api-Version"}
	for _, n := range plain {
		if r.IsSecretHeader(n) {
			t.Errorf("IsSecretHeader(%q) = true, want false", n)
		}
	}
}

func FuzzStringNeverLeaksSecret(f *testing.F) {
	f.Add("test-token-123", "prefix ", " suffix")
	f.Add("a", "aaaa", "a")
	f.Add("s3cr+t/=x y", "?k=", "&x=1")
	f.Fuzz(func(t *testing.T, secret, before, after string) {
		// The mask itself contains '*', so a secret made of '*' would always
		// "appear" in the output. Skip those.
		if secret == "" || strings.Contains(secret, "*") {
			t.Skip()
		}
		r := New(Options{Secrets: []string{secret}})
		for _, form := range []string{secret, url.QueryEscape(secret), base64.StdEncoding.EncodeToString([]byte(secret))} {
			got := r.String(before + form + after)
			if strings.Contains(got, secret) {
				t.Fatalf("String leaked secret %q: in=%q out=%q", secret, before+form+after, got)
			}
		}
	})
}
