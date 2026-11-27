package redact

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

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

// Call arguments are stored in the call log (SPEC §3.7 step 9): secret-
// looking names are masked, and resolved secrets are masked anywhere.
func TestArgs(t *testing.T) {
	r := New(Options{Secrets: []string{"s3cr3t-value"}})
	in := map[string]any{
		"q":       "cats",
		"limit":   float64(5),
		"api_key": "abc",
		"nested":  map[string]any{"password": "p", "name": "rex"},
		"list":    []any{map[string]any{"token": "t"}, "see s3cr3t-value here"},
		"note":    "contains s3cr3t-value",
	}
	got := r.Args(in)
	want := map[string]any{
		"q":       "cats",
		"limit":   float64(5),
		"api_key": Mask,
		"nested":  map[string]any{"password": Mask, "name": "rex"},
		"list":    []any{map[string]any{"token": Mask}, "see *** here"},
		"note":    "contains ***",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Args = %#v\nwant %#v", got, want)
	}
	if in["api_key"] != "abc" || in["nested"].(map[string]any)["password"] != "p" {
		t.Error("Args changed its input")
	}

	full := r.Values(in)
	if full["api_key"] != "abc" || full["note"] != "contains ***" {
		t.Errorf("Values = %#v", full)
	}
	if r.Args(nil) != nil {
		t.Error("Args(nil) should be nil")
	}
}

// With adds secrets learned later (an OAuth access token fetched while
// serving) without losing the original ones; the receiver is unchanged.
func TestWith(t *testing.T) {
	base := New(Options{Secrets: []string{"static-secret-1"}, SecretHeaders: []string{"X-Custom"}})
	r := base.With("dynamic-token-123", "")
	got := r.String("a static-secret-1 b dynamic-token-123 c")
	if strings.Contains(got, "static-secret-1") || strings.Contains(got, "dynamic-token-123") {
		t.Errorf("With lost a secret: %q", got)
	}
	if !r.IsSecretHeader("X-Custom") {
		t.Error("With lost the secret header names")
	}
	if strings.Contains(base.String("dynamic-token-123"), Mask) {
		t.Error("With changed the receiver")
	}
	if base.With() != base {
		t.Error("With() without secrets should return the receiver")
	}
}

// ContainsSecret detects a configured secret in every form String masks
// (raw, percent-encoded, base64, JSON-escaped), so a caller can refuse text
// instead of masking it (an introspection result that becomes spec text).
// Well-known token formats that were never configured don't count: a schema
// may document an example token.
func TestContainsSecret(t *testing.T) {
	const secret = `s3cr+t/=x y"q\z`
	r := New(Options{Secrets: []string{secret}})
	jsonForm, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	for name, form := range map[string]string{
		"raw":          secret,
		"query-escape": url.QueryEscape(secret),
		"path-escape":  url.PathEscape(secret),
		"base64-std":   base64.StdEncoding.EncodeToString([]byte(secret)),
		"base64-url":   base64.RawURLEncoding.EncodeToString([]byte(secret)),
		"json-escaped": strings.Trim(string(jsonForm), `"`),
	} {
		if !r.ContainsSecret(`{"description":"key ` + form + ` here"}`) {
			t.Errorf("ContainsSecret missed the %s form", name)
		}
	}
	for _, s := range []string{"", "ordinary text", "example " + fakeGitHub, "Bearer abcdefghijkl"} {
		if r.ContainsSecret(s) {
			t.Errorf("ContainsSecret(%q) = true, want false (no configured secret)", s)
		}
	}
	if New(Options{}).ContainsSecret(secret) {
		t.Error("a Redactor without secrets reported one")
	}
}

// A JSON document may spell a string with any escapes (PHP's json_encode
// writes "\/" for "/"; any character may be "\uXXXX"), and a JSON parser
// decodes them all, so ContainsSecret checks a JSON document's canonical
// spelling too: every escape spelling of every secret form is caught,
// in values and keys, and in a duplicate key's discarded value.
func TestContainsSecretAnyJSONEscape(t *testing.T) {
	const secret = "tok/en+123="
	r := New(Options{Secrets: []string{secret}})
	b64 := base64.StdEncoding.EncodeToString([]byte(secret))
	for name, doc := range map[string]string{
		"solidus escaped":       `{"d":"key tok\/en+123= here"}`,
		"fully u-escaped":       `{"d":"` + jsonU(secret) + `"}`,
		"mixed escapes":         `{"d":"t\u006fk\/en\u002B123\u003d"}`,
		"u-escaped query form":  `{"d":"` + jsonU(url.QueryEscape(secret)) + `"}`,
		"u-escaped base64 form": `["` + jsonU(b64) + `"]`,
		"in a key":              `{"tok\/en+123=":1}`,
		"duplicate key's first": `{"a":"tok\/en+123=","a":"x"}`,
		"deep and pretty": `{
  "a": [1, 2.50, true, null, {"b": "tok\/en+123="}]
}`,
	} {
		if !r.ContainsSecret(doc) {
			t.Errorf("ContainsSecret missed the %s spelling: %s", name, doc)
		}
	}
	for _, s := range []string{`{"d":"caf\u00e9 \/ \n"}`, `{"d":"tok\/en"}`, `not json \/`} {
		if r.ContainsSecret(s) {
			t.Errorf("ContainsSecret(%q) = true, want false", s)
		}
	}
}

// jsonU spells every rune of s (BMP) as a \uXXXX escape.
func jsonU(s string) string {
	var b strings.Builder
	for _, c := range s {
		fmt.Fprintf(&b, `\u%04x`, c)
	}
	return b.String()
}

// CanonicalJSON re-spells a JSON document: compact, every string with Go's
// escapes (no HTML escaping), numbers as written, key order and duplicate
// keys kept. Anything that isn't one valid JSON document is refused.
func TestCanonicalJSON(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"b":"\u00e9\/<&>","a":[1.50,true,null,{"x":"\"\\"}],"a":2}`, `{"b":"é/<&>","a":[1.50,true,null,{"x":"\"\\"}],"a":2}`},
		{" [ ] ", `[]`},
		{`{}`, `{}`},
		{`"t\u006fk"`, `"tok"`},
		{`-0.5e10`, `-0.5e10`},
		{"{\n \"a\" : { \"b\" : [ ] } , \"c\" : " + `"\n"` + " }", `{"a":{"b":[]},"c":"\n"}`},
	}
	for _, tc := range cases {
		got, ok := CanonicalJSON([]byte(tc.in))
		if !ok || string(got) != tc.want {
			t.Errorf("CanonicalJSON(%s) = %s, %v; want %s", tc.in, got, ok, tc.want)
		}
	}
	for _, bad := range []string{``, `{"a":1`, `{} {}`, `{"a":1,}`, `tok`} {
		if got, ok := CanonicalJSON([]byte(bad)); ok {
			t.Errorf("CanonicalJSON(%q) = %s, true; want refused", bad, got)
		}
	}
}

// JSON returns a body unchanged unless it is a JSON document whose canonical
// spelling carries something String masks; then it returns that spelling, so
// a secret hidden behind escapes is masked before the model (or a client that
// decodes the JSON) sees it.
func TestJSONCanonicalOnlyWhenMasked(t *testing.T) {
	const secret = "tok/en+123="
	r := New(Options{Secrets: []string{secret}})
	for _, body := range []string{
		`{"d":"` + jsonU(secret) + `"}`,
		`{"d":"tok\/en+123="}`,
		`{"d":"` + jsonU(fakeGitHub) + `"}`, // token-pattern backstop
	} {
		got := r.String(string(r.JSON([]byte(body))))
		if strings.Contains(got, secret) || strings.Contains(got, fakeGitHub) || !strings.Contains(got, Mask) {
			t.Errorf("JSON(%s) then String = %s; want the secret masked", body, got)
		}
	}
	for _, body := range []string{`{"d":"caf\u00e9 \/ \u003c"}`, `{"d":"plain"}`, `not json \u0074`, `{"d":"tok\/en+123=`} {
		if got := r.JSON([]byte(body)); string(got) != body {
			t.Errorf("JSON(%s) = %s; want it unchanged", body, got)
		}
	}
	if r.JSON(nil) != nil {
		t.Error("JSON(nil) != nil")
	}
}

// A body that is not one valid JSON document (cut at the response cap, or
// with trailing junk) still has each complete "..." string literal decoded
// and checked: a secret behind escapes inside one is re-spelled so String
// masks it, and ContainsSecret reports it. Bodies with nothing to mask are
// returned unchanged.
func TestJSONLenientOnInvalidBodies(t *testing.T) {
	const secret = "tok/en+123="
	r := New(Options{Secrets: []string{secret}})
	for name, body := range map[string]string{
		"truncated after the secret": `{"d":"tok\/en+123=","e":"more`,
		"trailing junk":              `{"d":"` + jsonU(secret) + `"} trailing`,
		"two documents":              `{} {"d":"tok\/en+123="}`,
		"in markup":                  `<p>"tok\/en+123="</p>`,
		// An odd number of quotes, or an escaped quote, before the secret
		// must not throw the literal pairing off.
		"odd quote before":     `x"y {"d":"tok\/en+123="}`,
		"escaped quote before": `"a\"{"d":"tok\/en+123="} more`,
		"stray escaped quote":  `a \" b "tok\/en+123="`,
		"token pattern":        `{"d":"` + jsonU(fakeGitHub) + `",`,
	} {
		t.Run(name, func(t *testing.T) {
			got := r.String(string(r.JSON([]byte(body))))
			if strings.Contains(got, secret) || strings.Contains(got, fakeGitHub) || !strings.Contains(got, Mask) {
				t.Errorf("JSON(%s) then String = %s; want the secret masked", body, got)
			}
			if name != "token pattern" && !r.ContainsSecret(body) {
				t.Errorf("ContainsSecret(%s) = false, want true", body)
			}
		})
	}
	for _, body := range []string{`{"d":"café","e":`, `"a\/b" "c`, `"\x"`, `{"d":"tok\/en+123=`} {
		if got := r.JSON([]byte(body)); string(got) != body {
			t.Errorf("JSON(%s) = %s; want it unchanged", body, got)
		}
		if r.ContainsSecret(body) {
			t.Errorf("ContainsSecret(%s) = true, want false", body)
		}
	}
}

// CanonicalJSON accepts exactly the valid JSON documents, and its output is
// valid JSON that decodes to the same value.
func FuzzCanonicalJSON(f *testing.F) {
	for _, s := range []string{`{"a":"t\u006fk\/x","a":[1.50,true,null]}`, `[]`, `"x"`, `{"a":1`, `{} {}`, "\"\xff\""} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		c, ok := CanonicalJSON(b)
		if ok != json.Valid(b) {
			t.Fatalf("CanonicalJSON ok = %v, json.Valid = %v", ok, !ok)
		}
		if !ok {
			return
		}
		decode := func(x []byte) any {
			d := json.NewDecoder(bytes.NewReader(x))
			d.UseNumber()
			var v any
			if err := d.Decode(&v); err != nil {
				t.Fatalf("decode %q: %v", x, err)
			}
			return v
		}
		if !json.Valid(c) || !reflect.DeepEqual(decode(b), decode(c)) {
			t.Fatalf("CanonicalJSON(%q) = %q: not the same value", b, c)
		}
		// The canonical spelling is a fixed point.
		if again, ok := CanonicalJSON(c); !ok || !bytes.Equal(again, c) {
			t.Fatalf("CanonicalJSON is not idempotent: %q -> %q", c, again)
		}
	})
}

// A fixed secret spelled with any mix of per-rune escapes (\uXXXX, "\/")
// inside a JSON string is always caught by ContainsSecret and masked by
// JSON then String, whether the document is valid or cut short, and
// whatever comes before it (an odd number of quotes, escaped quotes). The
// fuzz input picks which runes are escaped and the bytes around the document.
func FuzzEscapedSecretCaught(f *testing.F) {
	f.Add([]byte{0}, []byte(""), []byte(""))
	f.Add([]byte{0xff, 0x0f}, []byte(""), []byte(" junk"))
	f.Add([]byte{0xaa, 0x55, 0x33}, []byte(""), []byte(`,"x":"`))
	f.Add([]byte{0x0f}, []byte(`x"y `), []byte(""))
	f.Add([]byte{0xf0, 0x01}, []byte(`"a\"`), []byte(` "`))
	const secret = "tok/en+123=é"
	r := New(Options{Secrets: []string{secret}})
	f.Fuzz(func(t *testing.T, mask, prefix, tail []byte) {
		var lit strings.Builder
		for i, c := range secret {
			bit := len(mask) > 0 && mask[(i/8)%len(mask)]&(1<<(i%8)) != 0
			switch {
			case bit:
				fmt.Fprintf(&lit, `\u%04x`, c)
			case c == '/' && len(mask) > 1 && mask[1]&1 != 0:
				lit.WriteString(`\/`)
			default:
				lit.WriteRune(c)
			}
		}
		body := string(prefix) + `{"d":"` + lit.String() + `"}` + string(tail)
		if !r.ContainsSecret(body) {
			t.Fatalf("ContainsSecret(%q) = false", body)
		}
		if got := r.String(string(r.JSON([]byte(body)))); strings.Contains(got, secret) || !strings.Contains(got, Mask) {
			t.Fatalf("JSON then String(%q) = %q; want the secret masked", body, got)
		}
	})
}
