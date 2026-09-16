//go:build integration

package acceptance

import (
	"slices"
	"strings"
	"testing"
)

func TestShowSeasons(t *testing.T) {
	id := findItem(t, "Shows", "Series", "Severance")
	out := call(t, "show_seasons", map[string]any{"series_id": id})
	if str(out["series"]) != "Severance" {
		t.Errorf("series = %v", out["series"])
	}
	var numbers []int
	for _, s := range rows(t, out["seasons"], "seasons") {
		numbers = append(numbers, num(t, s["season"], "season"))
		if str(s["id"]) == "" || str(s["name"]) == "" {
			t.Errorf("season row = %v", s)
		}
	}
	slices.Sort(numbers)
	if !slices.Equal(numbers, []int{1, 2}) {
		t.Errorf("seasons = %v, want [1 2]", numbers)
	}
	if msg := callErr(t, "show_seasons", map[string]any{"series_id": "00000000000000000000000000000000"}); !strings.Contains(msg, "no item") {
		t.Errorf("an unknown series: %s", msg)
	}
}

func TestShowEpisodes(t *testing.T) {
	id := findItem(t, "Shows", "Series", "Breaking Bad")
	out := call(t, "show_episodes", map[string]any{"series_id": id})
	if str(out["series"]) != "Breaking Bad" {
		t.Errorf("series = %v", out["series"])
	}
	eps := rows(t, out["episodes"], "episodes")
	var numbers []int
	for _, e := range eps {
		numbers = append(numbers, num(t, e["episode"], "episode"))
		if str(e["series"]) != "Breaking Bad" || num(t, e["season"], "season") != 1 || str(e["video"]) == "" {
			t.Errorf("episode row = %v", e)
		}
	}
	slices.Sort(numbers)
	if !slices.Equal(numbers, []int{1, 2, 3}) {
		t.Errorf("episodes = %v, want [1 2 3]", numbers)
	}
	// the nfo named them
	var pilot bool
	for _, e := range eps {
		if str(e["name"]) == "Pilot" {
			pilot = true
		}
	}
	if !pilot {
		t.Errorf("no episode named Pilot among %v", eps)
	}

	// scoped to one season of a two-season show
	sev := findItem(t, "Shows", "Series", "Severance")
	seasons := call(t, "show_seasons", map[string]any{"series_id": sev})
	var s2 string
	for _, s := range rows(t, seasons["seasons"], "seasons") {
		if num(t, s["season"], "season") == 2 {
			s2 = str(s["id"])
		}
	}
	out = call(t, "show_episodes", map[string]any{"series_id": sev, "season_id": s2})
	for _, e := range rows(t, out["episodes"], "episodes") {
		if num(t, e["season"], "season") != 2 {
			t.Errorf("season 2 listing has %v", e)
		}
	}
	if n := len(rows(t, out["episodes"], "episodes")); n != 2 {
		t.Errorf("season 2 has %d episodes, want 2", n)
	}
}

// Severance season one has nine episodes and the library holds two. Neither
// server records the other seven out of the box - stock Jellyfin needs the
// TheTVDB plugin and Emby 4.10 has dropped the import - so what is proved
// is that nothing on disk is reported missing and the shape is right.
func TestShowMissing(t *testing.T) {
	id := findItem(t, "Shows", "Series", "Severance")
	out := call(t, "show_missing", map[string]any{"series_id": id})
	if str(out["series"]) != "Severance" {
		t.Errorf("series = %v", out["series"])
	}
	missing := rows(t, out["missing"], "missing")
	for _, m := range missing {
		if num(t, m["season"], "season") == 0 || num(t, m["episode"], "episode") == 0 || str(m["name"]) == "" {
			t.Errorf("missing row = %v", m)
		}
		if num(t, m["season"], "season") == 1 && num(t, m["episode"], "episode") <= 2 {
			t.Errorf("an episode on disk reported missing: %v", m)
		}
	}
	// a show with the provider off knows nothing beyond its files
	tng := findItem(t, "Messy Shows", "Series", "Star Trek The Next Generation")
	out = call(t, "show_missing", map[string]any{"series_id": tng})
	if n := len(rows(t, out["missing"], "missing")); n != 0 {
		t.Errorf("Star Trek The Next Generation has %d missing episodes without a provider", n)
	}
}

func TestShowFamilyIsComplete(t *testing.T) {
	var got []string
	for _, name := range toolNames(t) {
		if strings.HasPrefix(name, "show_") {
			got = append(got, name)
		}
	}
	want := []string{"show_episodes", "show_missing", "show_seasons"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("show tools = %v, want %v", got, want)
	}
}
