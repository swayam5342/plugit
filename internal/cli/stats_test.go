package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/store"
	"github.com/swayam5342/plugit/internal/tui"
)

func TestStatsPlain(t *testing.T) {
	p := config.NewPaths(t.TempDir())
	bad := lcall("pets", "pp_get_pet", store.HTTPError, 404)
	seedLog(t, p, lcall("pets", "pp_list_pets", store.OK, 200), lcall("pets", "pp_list_pets", store.OK, 200), bad, lcall("other", "o_x", store.OK, 200))
	out := &bytes.Buffer{}
	if err := showStats(t.Context(), p, statsSettings{name: "pets", out: out, since: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"API", "TOOL", "CALLS", "pp_list_pets", "pp_get_pet", "100%", "42", "pets"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "o_x") {
		t.Errorf("other API shown:\n%s", s)
	}
}

func TestStatsJSON(t *testing.T) {
	p := config.NewPaths(t.TempDir())
	seedLog(t, p, lcall("pets", "a", store.OK, 200), lcall("gh", "b", store.Failed, 0))
	out := &bytes.Buffer{}
	if err := showStats(t.Context(), p, statsSettings{out: out, asJSON: true}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Since string            `json:"since"`
		Tools []store.ToolStats `json:"tools"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.Tools) != 2 || got.Since == "" {
		t.Errorf("json = %s (%v)", out, err)
	}
}

func TestStatsWindowExcludesOld(t *testing.T) {
	p := config.NewPaths(t.TempDir())
	old := lcall("pets", "ancient", store.OK, 200)
	old.Time = time.Now().Add(-48 * time.Hour)
	seedLog(t, p, old, lcall("pets", "recent", store.OK, 200))
	out := &bytes.Buffer{}
	if err := showStats(t.Context(), p, statsSettings{out: out, since: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); strings.Contains(s, "ancient") || !strings.Contains(s, "recent") {
		t.Errorf("window ignored:\n%s", s)
	}
}

func TestStatsEmptyAndInteractive(t *testing.T) {
	p := config.NewPaths(t.TempDir())
	out := &bytes.Buffer{}
	if err := showStats(t.Context(), p, statsSettings{out: out}); err != nil || !strings.Contains(out.String(), "No calls recorded yet") {
		t.Errorf("empty: %v %s", err, out)
	}

	seedLog(t, p, lcall("pets", "a", store.OK, 200))
	opened := false
	s := statsSettings{name: "pets", out: out, interactive: func(ctx context.Context, in tui.StatsInput) error {
		opened = true
		if in.Title != "pets" || in.Source == nil {
			t.Fatalf("input = %+v", in)
		}
		st, err := in.Source(ctx, time.Now().Add(-time.Hour))
		if err != nil || len(st) != 1 || st[0].Tool != "a" {
			t.Errorf("source = %+v, %v", st, err)
		}
		return nil
	}}
	if err := showStats(t.Context(), p, s); err != nil || !opened {
		t.Fatalf("err = %v, opened = %v", err, opened)
	}
}

// The token budget for a saved API comes from its manifest cache.
func TestStatsBudget(t *testing.T) {
	up := newUpstream(t)
	p := setupAPI(t, up.URL)
	if err := lintAPI(t.Context(), p, "pets", lintDeps(&bytes.Buffer{})); err != nil { // builds the manifest cache
		t.Fatal(err)
	}
	b := apiBudget(p, "pets", BuildInfo{})
	if !strings.Contains(b, "tokens") || !strings.Contains(b, "2 tools") {
		t.Errorf("budget = %q", b)
	}
	if apiBudget(p, "missing", BuildInfo{}) != "" {
		t.Error("budget for a missing API")
	}
}
