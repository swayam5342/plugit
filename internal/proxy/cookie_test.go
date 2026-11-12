package proxy

import (
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/redact"
)

var getMe = model.Tool{Key: "GET /me", Name: "getMe", Method: "GET", Path: "/me", Params: []model.Param{
	param("theme", model.InCookie, false),
	param("lang", model.InCookie, false),
	param("q", model.InQuery, false),
}}

// Cookie parameters become one Cookie header that plugit builds itself
// (SPEC M6 design note: cookie parameters).
func TestCookieHeader(t *testing.T) {
	req := mustBuild(t, getMe, `{"theme":"dark","lang":"en","q":"x"}`, Target{})
	if got := req.Header.Values("Cookie"); len(got) != 1 || got[0] != "lang=en; theme=dark" {
		t.Errorf("Cookie = %q", got)
	}
	if req.URL.Query().Get("theme") != "" {
		t.Error("a cookie leaked into the query")
	}
	cookies := req.Cookies()
	if len(cookies) != 2 || cookies[0].Name != "lang" || cookies[1].Value != "dark" {
		t.Errorf("parsed cookies = %v", cookies)
	}
	// Numbers and booleans are fine; no cookie arguments, no header.
	if req := mustBuild(t, getMe, `{"theme":7,"lang":true}`, Target{}); req.Header.Get("Cookie") != "lang=true; theme=7" {
		t.Errorf("Cookie = %q", req.Header.Get("Cookie"))
	}
	if req := mustBuild(t, getMe, `{"q":"x"}`, Target{}); req.Header.Get("Cookie") != "" {
		t.Errorf("Cookie without cookie arguments: %q", req.Header.Get("Cookie"))
	}
}

// A value can't smuggle another cookie or break the header.
func TestCookieValuesRejected(t *testing.T) {
	for _, v := range []string{`"dark; admin=1"`, `"a,b"`, `"two words"`, `"tab\there"`, `"quo\"te"`, `"back\\slash"`, `"line\r\nX-Evil: 1"`, `["a","b"]`, `{"k":"v"}`} {
		if req, err := build(t, getMe, `{"theme":`+v+`}`, Target{}); err == nil {
			t.Errorf("theme=%s accepted: Cookie = %q", v, req.Header.Get("Cookie"))
		} else if !strings.Contains(err.Error(), "theme") {
			t.Errorf("theme=%s: error doesn't name the argument: %v", v, err)
		}
	}
}

// A cookie credential is a fixed param named like the declared cookie: it
// goes in the Cookie header, never in the input schema, and is redacted.
func TestCookieFixedSecret(t *testing.T) {
	tl := getMe
	tl.Params = append([]model.Param{{Name: "session", UpstreamName: "session", In: model.InCookie, Hidden: true}}, tl.Params...)
	tg := Target{Fixed: []Fixed{{Name: "session", Value: "test-session-secret-1", Secret: true}}}
	req := mustBuild(t, tl, `{"theme":"dark"}`, tg)
	if got := req.Header.Get("Cookie"); got != "session=test-session-secret-1; theme=dark" {
		t.Errorf("Cookie = %q", got)
	}
	red := redact.New(redact.Options{Secrets: tg.SecretValues()})
	shown := red.Header(req.Header).Get("Cookie")
	if strings.Contains(shown, "test-session-secret-1") || strings.Contains(shown, "dark") || !strings.Contains(shown, "session=") {
		t.Errorf("printed Cookie = %q", shown)
	}
	// The model can't set it: hidden params aren't arguments.
	if _, err := build(t, tl, `{"session":"forged"}`, tg); err == nil {
		t.Error("the model set the hidden session cookie")
	}
}

// A Cookie header parameter is still never a tool parameter.
func TestCookieHeaderParamStillForbidden(t *testing.T) {
	tl := model.Tool{Key: "GET /x", Name: "x", Method: "GET", Path: "/x", Params: []model.Param{param("Cookie", model.InHeader, false)}}
	req, err := build(t, tl, `{"Cookie":"admin=1"}`, Target{})
	if err == nil && req.Header.Get("Cookie") != "" {
		t.Errorf("raw Cookie header from the model: %q", req.Header.Get("Cookie"))
	}
}
