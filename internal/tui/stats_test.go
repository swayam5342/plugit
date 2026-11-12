package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/swayam5342/plugit/internal/store"
)

var statsNow = time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)

func sampleStats() []store.ToolStats {
	return []store.ToolStats{
		{API: "pets", Tool: "pp_list_pets", Calls: 20, Errors: 4, P50: 100 * time.Millisecond, P95: 190 * time.Millisecond, Bytes: 24000, Last: statsNow.Add(-time.Minute)},
		{API: "pets", Tool: "pp_get_pet", Calls: 3, Errors: 0, P50: 5 * time.Millisecond, P95: 9 * time.Millisecond, Bytes: 900, Last: statsNow.Add(-time.Hour)},
		{API: "gh", Tool: "gh_list_issues", Calls: 7, Errors: 7, P50: 300 * time.Millisecond, P95: 900 * time.Millisecond, Bytes: 1, Last: statsNow.Add(-2 * time.Hour)},
	}
}

func newStatsTest(src func(context.Context, time.Time) ([]store.ToolStats, error)) *Stats {
	s := NewStats(StatsInput{Title: "all APIs", Source: src, Budgets: map[string]string{"pets": "≈2.3k tokens · 8 tools"},
		Interval: time.Hour, Now: func() time.Time { return statsNow }})
	s.Update(tea.WindowSizeMsg{Width: 110, Height: 30})
	return s
}

func statsView(s *Stats) string { return ansi.Strip(s.View().Content) }

func TestStatsShowsTotalsAndTools(t *testing.T) {
	s := newStatsTest(nil)
	s.Update(StatsMsg{Stats: sampleStats()})
	v := statsView(s)
	for _, want := range []string{"plugit", "stats", "pets", "gh", "pp_list_pets", "20%", "100ms", "190ms", "24.0 KB", "1m ago",
		"≈2.3k tokens", "30 calls", "11 errors", "last 7 days"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	// APIs ordered by calls, tools by calls within each.
	if strings.Index(v, "pp_list_pets") > strings.Index(v, "pp_get_pet") || strings.Index(v, "pets") > strings.Index(v, "gh_list_issues") {
		t.Errorf("order wrong:\n%s", v)
	}
}

// Number keys change the window and re-query with the new start time.
func TestStatsWindow(t *testing.T) {
	var since time.Time
	s := newStatsTest(func(_ context.Context, t time.Time) ([]store.ToolStats, error) { since = t; return nil, nil })
	_, cmd := s.Update(key("1"))
	if cmd == nil {
		t.Fatal("no refresh after changing the window")
	}
	cmd()
	if !since.Equal(statsNow.Add(-time.Hour)) || !strings.Contains(statsView(s), "last hour") {
		t.Errorf("since = %v\n%s", since, statsView(s))
	}
	s.Update(key("4"))
	if !strings.Contains(statsView(s), "last 30 days") {
		t.Errorf("view:\n%s", statsView(s))
	}
}

func TestStatsSort(t *testing.T) {
	s := newStatsTest(nil)
	s.Update(StatsMsg{Stats: sampleStats()})
	s.Update(key("s")) // by errors
	v := statsView(s)
	if !strings.Contains(v, "sorted by errors") || strings.Index(v, "gh_list_issues") > strings.Index(v, "pp_list_pets") {
		t.Errorf("not sorted by errors:\n%s", v)
	}
}

func TestStatsEmptyAndFits(t *testing.T) {
	s := newStatsTest(nil)
	s.Update(StatsMsg{})
	if !strings.Contains(statsView(s), "No calls") {
		t.Errorf("empty view:\n%s", statsView(s))
	}
	s.Update(StatsMsg{Stats: sampleStats()})
	s.Update(tea.WindowSizeMsg{Width: 44, Height: 8})
	v := s.View().Content
	if n := strings.Count(v, "\n") + 1; n > 8 {
		t.Errorf("%d lines for an 8-line window", n)
	}
	for _, l := range strings.Split(v, "\n") {
		if w := ansi.StringWidth(l); w > 44 {
			t.Errorf("line is %d wide: %q", w, ansi.Strip(l))
		}
	}
	if _, cmd := s.Update(key("q")); !isQuit(cmd) {
		t.Error("q should quit")
	}
}
