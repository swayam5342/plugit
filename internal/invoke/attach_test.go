package invoke

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/rest"
)

// encodeAttach builds a credential-free request with rest.Encode (as a
// protocol does) and then runs invoke's attachAuth — the two halves the old
// proxy.Build combined. BaseURL defaults to a pinned origin.
func encodeAttach(t *testing.T, tl model.Tool, rawArgs string, tg rest.Target) *http.Request {
	t.Helper()
	if tg.BaseURL == nil {
		u, err := url.Parse("https://api.example.test/v1")
		if err != nil {
			t.Fatal(err)
		}
		tg.BaseURL = u
	}
	req, err := rest.Encode(t.Context(), tl, decodeArgs(t, rawArgs), tg)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := attachAuth(req, tg); err != nil {
		t.Fatalf("attachAuth: %v", err)
	}
	return req
}

func TestAttachAuth(t *testing.T) {
	tl := model.Tool{HTTP: &model.HTTPOp{Method: "GET", Path: "/me"}}
	const secret = "test-token-123"
	cases := map[rest.AuthType]func(t *testing.T, r *http.Request){
		rest.AuthBearer: func(t *testing.T, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+secret {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
		},
		rest.AuthHeader: func(t *testing.T, r *http.Request) {
			if r.Header.Get("X-Api-Key") != secret {
				t.Errorf("X-Api-Key = %q", r.Header.Get("X-Api-Key"))
			}
		},
		rest.AuthQuery: func(t *testing.T, r *http.Request) {
			if r.URL.Query().Get("key") != secret {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
		},
		rest.AuthBasic: func(t *testing.T, r *http.Request) {
			want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+secret))
			if r.Header.Get("Authorization") != want {
				t.Errorf("Authorization = %q, want %q", r.Header.Get("Authorization"), want)
			}
		},
		rest.AuthNone: func(t *testing.T, r *http.Request) {
			if r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
				t.Error("no credentials expected")
			}
		},
	}
	for typ, check := range cases {
		t.Run(string(typ), func(t *testing.T) {
			s := secret
			if typ == rest.AuthBasic {
				s = "alice:" + secret
			}
			req := encodeAttach(t, tl, `{}`, rest.Target{Auth: rest.Auth{Type: typ, Header: "X-Api-Key", Param: "key", Secret: s}})
			check(t, req)
		})
	}
}

func TestAttachAuthOverridesModelSuppliedHeaders(t *testing.T) {
	tl := model.Tool{HTTP: &model.HTTPOp{Method: "GET", Path: "/me"}, Params: []model.Param{paramIn("X-Api-Key", model.InHeader, false)}}
	req := encodeAttach(t, tl, `{"X-Api-Key": "attacker"}`, rest.Target{Auth: rest.Auth{Type: rest.AuthHeader, Header: "X-Api-Key", Secret: "test-token-123"}})
	if got := req.Header.Values("X-Api-Key"); !reflect.DeepEqual(got, []string{"test-token-123"}) {
		t.Errorf("X-Api-Key = %v", got)
	}
}
