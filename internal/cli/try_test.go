package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/isolate"
	"github.com/swayam5342/plugit/internal/tui"
)

// setupTryAPI writes the pets config pointed at baseURL, with policy
// replaced by the given block.
func setupTryAPI(t *testing.T, baseURL, policyBlock string) config.Paths {
	t.Helper()
	p := setupAPI(t, baseURL)
	if policyBlock != "" {
		cfg, _ := p.APIConfig("pets")
		y := strings.Replace(fmt.Sprintf(petsYAML, baseURL), "policy: {rate_limit: 10/min}", policyBlock, 1)
		y = strings.Replace(y, `"POST /pets": {}`, `"POST /pets": {policy: confirm}`, 1)
		if err := config.WriteFileAtomic(cfg, []byte(y)); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func runTry(t *testing.T, p config.Paths, toolset, tool string, f tryFlags, confirm func(string) (bool, error)) (string, error) {
	t.Helper()
	var out bytes.Buffer
	d := serveDeps(isolate.Parse)
	d.stderr = &bytes.Buffer{}
	err := tryTool(t.Context(), serveSettings{paths: p, name: "pets", toolset: toolset}, tool, f, tryDeps{runDeps: d, out: &out, confirm: confirm})
	all := out.String() + d.stderr.(*bytes.Buffer).String()
	if err != nil {
		all += err.Error()
	}
	if strings.Contains(all, runSecret) {
		t.Errorf("try leaked the secret:\n%s", all)
	}
	return out.String(), err
}

func TestTryPrintsRequestAndResponse(t *testing.T) {
	up := newUpstream(t)
	p := setupTryAPI(t, up.URL, "")
	out, err := runTry(t, p, "", "pp_getPet", tryFlags{args: []string{"id=7"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GET " + up.URL + "/pets/7", "X-Api-Key: ***", "HTTP 200", `"path": "/pets/7"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output should contain %q:\n%s", want, out)
		}
	}
	if up.lastKey.Load() != runSecret {
		t.Error("credential not sent")
	}
}

func TestTryArgumentTypes(t *testing.T) {
	up := newUpstream(t)
	p := setupTryAPI(t, up.URL, "")
	// limit is an integer in the spec, so "2" is sent as a number.
	if _, err := runTry(t, p, "", "pp_list_pets", tryFlags{args: []string{"limit=2"}}, nil); err != nil {
		t.Fatal(err)
	}
	if up.lastURL.Load() != "/pets?limit=2" {
		t.Errorf("url = %v", up.lastURL.Load())
	}
	// The operation key works as well as the tool name. id is a string, so
	// "007" stays "007".
	if _, err := runTry(t, p, "", "GET /pets/{id}", tryFlags{args: []string{"id=007"}}, nil); err != nil {
		t.Fatal(err)
	}
	if up.lastURL.Load() != "/pets/007" {
		t.Errorf("url = %v", up.lastURL.Load())
	}

	hits := up.hits.Load()
	for name, args := range map[string][]string{
		"invalid integer":  {"limit=abc"},
		"unknown argument": {"nope=1"},
		"missing =":        {"limit"},
		"repeated":         {"limit=1", "limit=2"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runTry(t, p, "", "pp_list_pets", tryFlags{args: args}, nil); err == nil {
				t.Error("expected an error")
			}
		})
	}
	if up.hits.Load() != hits {
		t.Error("invalid arguments reached the upstream")
	}
}

func TestTryPolicy(t *testing.T) {
	up := newUpstream(t)

	t.Run("read-only refuses writes even with --yes", func(t *testing.T) {
		p := setupTryAPI(t, up.URL, "")
		_, err := runTry(t, p, "", "pp_createPet", tryFlags{yes: true}, nil)
		if err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("deny can't be confirmed", func(t *testing.T) {
		p := setupTryAPI(t, up.URL, "")
		_, err := runTry(t, p, "denied", "pp_listPets", tryFlags{yes: true}, func(string) (bool, error) { return true, nil })
		if err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("err = %v", err)
		}
	})

	p := setupTryAPI(t, up.URL, "policy: {mode: confirm-writes}")
	hits := up.hits.Load()
	t.Run("confirm without a terminal or --yes", func(t *testing.T) {
		_, err := runTry(t, p, "", "pp_createPet", tryFlags{}, nil)
		if err == nil || !strings.Contains(err.Error(), "--yes") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("confirm declined", func(t *testing.T) {
		var asked string
		_, err := runTry(t, p, "", "pp_createPet", tryFlags{}, func(q string) (bool, error) { asked = q; return false, nil })
		if err == nil || !strings.Contains(asked, "pp_createPet") {
			t.Errorf("err = %v, prompt = %q", err, asked)
		}
	})
	if up.hits.Load() != hits {
		t.Fatal("an unconfirmed call reached the upstream")
	}
	t.Run("confirmed", func(t *testing.T) {
		if _, err := runTry(t, p, "", "pp_createPet", tryFlags{}, func(string) (bool, error) { return true, nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := runTry(t, p, "", "pp_createPet", tryFlags{yes: true}, nil); err != nil {
			t.Fatal(err)
		}
		if up.hits.Load() != hits+2 {
			t.Errorf("hits = %d, want %d", up.hits.Load(), hits+2)
		}
	})
}

func TestTryUnknownTool(t *testing.T) {
	up := newUpstream(t)
	p := setupTryAPI(t, up.URL, "")
	_, err := runTry(t, p, "", "nope", tryFlags{}, nil)
	if err == nil || !strings.Contains(err.Error(), "pp_getPet") {
		t.Errorf("err = %v, want a list of available tools", err)
	}
}

// An upstream error is shown and reported through the exit status.
func TestTryUpstreamError(t *testing.T) {
	up := newUpstream(t)
	p := setupTryAPI(t, up.URL, "")
	// The fake upstream answers 200 for everything; a traversal attempt is
	// refused before any request is made.
	out, err := runTry(t, p, "", "pp_getPet", tryFlags{args: []string{"id=.."}}, nil)
	if err == nil || !strings.Contains(out, "pp_getPet") {
		t.Errorf("out = %q err = %v", out, err)
	}
}

func runTryForm(t *testing.T, p config.Paths, toolset, tool string, f tryFlags, form func(context.Context, tui.TryInput) error) error {
	t.Helper()
	d := serveDeps(isolate.Parse)
	d.stderr = &bytes.Buffer{}
	return tryTool(t.Context(), serveSettings{paths: p, name: "pets", toolset: toolset}, tool, f, tryDeps{runDeps: d, out: &bytes.Buffer{}, form: form})
}

// On a terminal without --arg, try opens the Try it form; its Send makes
// the same redacted call as the command line.
func TestTryForm(t *testing.T) {
	up := newUpstream(t)
	p := setupTryAPI(t, up.URL, "")
	opened := false
	err := runTryForm(t, p, "", "pp_getPet", tryFlags{}, func(ctx context.Context, in tui.TryInput) error {
		opened = true
		if in.Tool.Name != "pp_getPet" || in.API != "pets" || in.Confirm != "" {
			t.Errorf("input = %+v", in)
		}
		for _, prm := range in.Tool.Params {
			if prm.Name == "X-Api-Key" && !prm.Hidden {
				t.Error("the credential header is a visible field")
			}
		}
		r := in.Send(ctx, map[string]string{"id": "7"})
		all := r.Request + r.Response + r.Err
		if r.Err != "" || r.Status != 200 || !strings.Contains(r.Request, "GET "+up.URL+"/pets/7") ||
			!strings.Contains(r.Request, "X-Api-Key: ***") || !strings.Contains(r.Response, `"path": "/pets/7"`) || strings.Contains(all, runSecret) {
			t.Errorf("result = %+v", r)
		}
		if r := in.Send(ctx, map[string]string{"nope": "x"}); r.Err == "" || !strings.Contains(r.Err, "nope") {
			t.Errorf("unknown argument: %+v", r)
		}
		return nil
	})
	if err != nil || !opened {
		t.Fatalf("err = %v, opened = %v", err, opened)
	}
}

func TestTryFormPolicy(t *testing.T) {
	up := newUpstream(t)
	never := func(context.Context, tui.TryInput) error { t.Error("form opened for a refused tool"); return nil }
	p := setupTryAPI(t, up.URL, "")
	if err := runTryForm(t, p, "", "pp_createPet", tryFlags{}, never); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only: %v", err)
	}
	if err := runTryForm(t, p, "denied", "pp_listPets", tryFlags{}, never); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Errorf("deny: %v", err)
	}

	p = setupTryAPI(t, up.URL, "policy: {mode: confirm-writes}")
	var confirm string
	if err := runTryForm(t, p, "", "pp_createPet", tryFlags{}, func(_ context.Context, in tui.TryInput) error { confirm = in.Confirm; return nil }); err != nil || confirm == "" {
		t.Errorf("confirm-writes: err=%v confirm=%q", err, confirm)
	}
	if err := runTryForm(t, p, "", "pp_createPet", tryFlags{yes: true}, func(_ context.Context, in tui.TryInput) error { confirm = in.Confirm; return nil }); err != nil || confirm != "" {
		t.Errorf("--yes should pre-confirm: err=%v confirm=%q", err, confirm)
	}
}
