package invoke

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/swayam5342/plugit/internal/model"
	"github.com/swayam5342/plugit/internal/protocol/rest"
)

const attachSecret = "test-token-123"

var pinned = mustU("https://api.example.test/v1")

// encodeAttach builds a credential-free request with rest.Encode (as a
// protocol does) and then runs invoke's attachAuth against the pinned endpoint.
func encodeAttach(t *testing.T, tl model.Tool, rawArgs string, cred Credential) *http.Request {
	t.Helper()
	req, err := rest.Encode(t.Context(), tl, decodeArgs(t, rawArgs), rest.Target{BaseURL: pinned})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := attachAuth(req, cred, pinned); err != nil {
		t.Fatalf("attachAuth: %v", err)
	}
	return req
}

var meTool = model.Tool{HTTP: &model.HTTPOp{Method: "GET", Path: "/me"}}

func TestAttachBearerSameOrigin(t *testing.T) {
	req := encodeAttach(t, meTool, `{}`, Credential{Type: "bearer", Secret: attachSecret})
	if got := req.Header.Get("Authorization"); got != "Bearer "+attachSecret {
		t.Errorf("Authorization = %q", got)
	}
}

func TestAttachHeader(t *testing.T) {
	req := encodeAttach(t, meTool, `{}`, Credential{Type: "header", Header: "X-Api-Key", Secret: attachSecret})
	if got := req.Header.Get("X-Api-Key"); got != attachSecret {
		t.Errorf("X-Api-Key = %q", got)
	}
}

func TestAttachQueryParam(t *testing.T) {
	req := encodeAttach(t, meTool, `{}`, Credential{Type: "query", Param: "key", Secret: attachSecret})
	if got := req.URL.Query().Get("key"); got != attachSecret {
		t.Errorf("query = %s", req.URL.RawQuery)
	}
}

func TestAttachBasic(t *testing.T) {
	req := encodeAttach(t, meTool, `{}`, Credential{Type: "basic", Secret: "alice:" + attachSecret})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+attachSecret))
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
}

func TestAttachNone(t *testing.T) {
	for _, typ := range []string{"", "none"} {
		req := encodeAttach(t, meTool, `{}`, Credential{Type: typ, Secret: attachSecret})
		if req.Header.Get("Authorization") != "" || req.URL.RawQuery != "" {
			t.Errorf("type %q: no credentials expected", typ)
		}
	}
}

// T8: the credential is attached only when the request origin matches the
// pinned endpoint exactly (scheme, host and port).
func TestAttachSkippedCrossOrigin(t *testing.T) {
	for _, target := range []string{
		"https://evil.example.test/me",
		"http://api.example.test/me",
		"https://api.example.test:8443/me",
	} {
		t.Run(target, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = attachAuth(req, Credential{Type: "bearer", Secret: attachSecret}, pinned)
			if !errors.Is(err, errBuild) {
				t.Errorf("err = %v, want errBuild", err)
			}
			if req.Header.Get("Authorization") != "" || req.URL.RawQuery != "" {
				t.Error("credential was attached to a cross-origin request")
			}
		})
	}
}

// A nil pinned endpoint (an mcpserver.Config with neither Endpoint nor
// Target.BaseURL) fails closed: httpx.Origin(nil) matches no request.
func TestAttachNilEndpointFailsClosed(t *testing.T) {
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.example.test/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := attachAuth(req, Credential{Type: "bearer", Secret: attachSecret}, nil); !errors.Is(err, errBuild) {
		t.Errorf("err = %v, want errBuild", err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("credential was attached with no pinned endpoint")
	}
}

func TestAttachRejectsControlChars(t *testing.T) {
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.example.test/me", nil)
	if err := attachAuth(req, Credential{Type: "bearer", Secret: "a" + string([]byte{13, 10}) + "X-Evil: 1"}, pinned); !errors.Is(err, errBuild) {
		t.Errorf("err = %v, want errBuild", err)
	}
}

func TestAttachAuthOverridesModelSuppliedHeaders(t *testing.T) {
	tl := model.Tool{HTTP: &model.HTTPOp{Method: "GET", Path: "/me"}, Params: []model.Param{paramIn("X-Api-Key", model.InHeader, false)}}
	req := encodeAttach(t, tl, `{"X-Api-Key": "attacker"}`, Credential{Type: "header", Header: "X-Api-Key", Secret: attachSecret})
	if got := req.Header.Values("X-Api-Key"); !reflect.DeepEqual(got, []string{attachSecret}) {
		t.Errorf("X-Api-Key = %v", got)
	}
}

func TestAttachQueryOverridesModelSuppliedParam(t *testing.T) {
	tl := model.Tool{HTTP: &model.HTTPOp{Method: "GET", Path: "/me"}, Params: []model.Param{paramIn("key", model.InQuery, false)}}
	req := encodeAttach(t, tl, `{"key": "attacker"}`, Credential{Type: "query", Param: "key", Secret: attachSecret})
	u, _ := url.ParseQuery(req.URL.RawQuery)
	if !reflect.DeepEqual(u["key"], []string{attachSecret}) {
		t.Errorf("key = %v", u["key"])
	}
}
