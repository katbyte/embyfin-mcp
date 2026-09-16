//go:build integration

package acceptance

import (
	"fmt"
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

// The episode rows carry the quality facts as numbers, which is what deciding
// "is my copy better than the library's" reads (A5).
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
		if str(e["series"]) != "Breaking Bad" || num(t, e["season"], "season") != 1 {
			t.Errorf("episode row = %v", e)
		}
		assertQualityFacts(t, e)
	}
	slices.Sort(numbers)
	if !slices.Equal(numbers, []int{1, 2, 3}) {
		t.Errorf("episodes = %v, want [1 2 3]", numbers)
	}
	// the nfo named them
	var pilot bool
	for _, e := range eps {
		if str(e["title"]) == "Pilot" {
			pilot = true
		}
	}
	if !pilot {
		t.Errorf("no episode titled Pilot among %v", eps)
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
	// and by season number, for a caller that has no season id
	out = call(t, "show_episodes", map[string]any{"series_id": sev, "season": 1})
	for _, e := range rows(t, out["episodes"], "episodes") {
		if num(t, e["season"], "season") != 1 {
			t.Errorf("season 1 listing has %v", e)
		}
	}
}

// assertQualityFacts checks an episode row carries what the scan probed: the
// numbers a comparison needs, not a string to parse back (A5, C6).
func assertQualityFacts(t *testing.T, row map[string]any) {
	t.Helper()

	if str(row["path"]) == "" {
		return // a record with no file has nothing to have probed
	}
	for _, field := range []string{"width", "height", "size", "runtime_s"} {
		if n := num(t, row[field], field); n <= 0 {
			t.Errorf("%s = %v on %v", field, row[field], row["title"])
		}
	}
	if c := str(row["video_codec"]); c != "h264" {
		t.Errorf("video_codec = %q on %v", c, row["title"])
	}
	if c := str(row["container"]); c != "mp4" && c != "mov,mp4,m4a,3gp,3g2,mj2" {
		t.Errorf("container = %q on %v", c, row["title"])
	}
	if w, h := num(t, row["width"], "width"), num(t, row["height"], "height"); w*h == 0 {
		t.Errorf("resolution = %dx%d on %v", w, h, row["title"])
	}
}

// A3 and C4: every episode in a library in one paged read, with the quality
// facts, and page boundaries that neither drop a row nor repeat one.
func TestLibraryEpisodes(t *testing.T) {
	out := call(t, "library_episodes", map[string]any{"library": "Shows"})
	total := num(t, out["total"], "total")
	if total < 9 {
		t.Fatalf("total = %d, want the Shows library's nine episode files", total)
	}
	for _, row := range rows(t, out["episodes"], "episodes") {
		if str(row["series"]) == "" || str(row["path"]) == "" {
			t.Errorf("row = %v", row)
		}
		assertQualityFacts(t, row)
	}

	// paged two at a time, the whole library comes back once each
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 20; page++ {
		args := map[string]any{"library": "Shows", "limit": 2}
		if cursor != "" {
			args["cursor"] = cursor
		}
		page := call(t, "library_episodes", args)
		for _, row := range rows(t, page["episodes"], "episodes") {
			key := fmt.Sprintf("%s S%02dE%02d", str(row["series"]), num(t, row["season"], "season"), num(t, row["episode"], "episode"))
			if seen[key] {
				t.Errorf("%s came back on two pages", key)
			}
			seen[key] = true
		}
		if cursor = str(page["cursor"]); cursor == "" {
			break
		}
	}
	if len(seen) != 9 {
		t.Errorf("paging returned %d episodes, want the library's 9: %v", len(seen), seen)
	}

	// one series on its own, and the refusals
	sev := findItem(t, "Shows", "Series", "Severance")
	out = call(t, "library_episodes", map[string]any{"series_id": sev})
	if n := len(rows(t, out["episodes"], "episodes")); n != 4 {
		t.Errorf("Severance has %d episode files, want 4", n)
	}
	out = call(t, "library_episodes", map[string]any{"series_id": sev, "season": 2, "quality": false})
	for _, row := range rows(t, out["episodes"], "episodes") {
		if num(t, row["season"], "season") != 2 {
			t.Errorf("season 2 read has %v", row)
		}
		if row["width"] != nil {
			t.Errorf("quality=false still carried the facts: %v", row)
		}
	}
	if msg := callErr(t, "library_episodes", map[string]any{"library": "Shows", "series_id": sev}); !strings.Contains(msg, "not both") {
		t.Errorf("a library and a series together: %s", msg)
	}
	if msg := callErr(t, "library_episodes", map[string]any{"cursor": "nonsense"}); !strings.Contains(msg, "cursor") {
		t.Errorf("a bad cursor: %s", msg)
	}
}

// A4 and C5: a batch of season and episode numbers answered without listing
// the series.
func TestShowEpisodesExist(t *testing.T) {
	sev := findItem(t, "Shows", "Series", "Severance")
	out := call(t, "show_episodes_exist", map[string]any{
		"series_id": sev,
		"episodes":  []map[string]any{{"season": 1, "episode": 1}, {"season": 1, "episode": 9}},
	})
	if str(out["series"]) != "Severance" || str(out["series_id"]) != sev {
		t.Errorf("out = %v", out)
	}
	answers := rows(t, out["episodes"], "episodes")
	if len(answers) != 2 {
		t.Fatalf("asked about 2 episodes, answered %v", answers)
	}
	if answers[0]["exists"] != true {
		t.Errorf("S01E01 is on disk: %v", answers[0])
	}
	if answers[1]["exists"] != false {
		t.Errorf("S01E09 is not in the fixture: %v", answers[1])
	}
	if num(t, out["absent"], "absent") != 1 {
		t.Errorf("absent = %v, want 1", out["absent"])
	}

	// by name rather than by id. The fixtures hold Severance twice - a tidy
	// copy in Shows and a messy one in Messy Shows - so the bare name is
	// ambiguous and has to be refused with both, and a library narrows it.
	msg := callErr(t, "show_episodes_exist", map[string]any{"series": "Severance", "episodes": []map[string]any{{"season": 2, "episode": 1}}})
	for _, want := range []string{"matches 2 series", "give series_id", "library"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not carry %q: %s", want, msg)
		}
	}
	byName := call(t, "show_episodes_exist", map[string]any{"series": "Severance", "library": "Shows", "episodes": []map[string]any{{"season": 2, "episode": 1}}})
	if str(byName["series_id"]) != sev {
		t.Errorf("narrowed by library = %v, want the Shows copy", byName)
	}
	if rows(t, byName["episodes"], "episodes")[0]["exists"] != true {
		t.Errorf("by name = %v", byName)
	}
	if msg := callErr(t, "show_episodes_exist", map[string]any{"series_id": sev, "episodes": []map[string]any{}}); !strings.Contains(msg, "at least one") {
		t.Errorf("an empty batch: %s", msg)
	}
}

// A6 and C7: release names resolve to the library's series.
func TestShowResolve(t *testing.T) {
	sev := findItem(t, "Shows", "Series", "Severance")
	for _, name := range []string{
		"Severance.S02E07.1080p.ATVP.WEB-DL.H264-GROUP",
		"Severance (2022) S01 [1080p WEB-DL x265]",
		"severance",
	} {
		out := call(t, "show_resolve", map[string]any{"title": name, "library": "Shows"})
		cands := rows(t, out["candidates"], "candidates")
		if len(cands) == 0 {
			t.Errorf("%s resolved to nothing (parsed %q)", name, out["parsed_title"])
			continue
		}
		if str(cands[0]["series_id"]) != sev {
			t.Errorf("%s resolved to %v, want Severance", name, cands[0])
		}
		if score, _ := cands[0]["score"].(float64); score < 0.9 {
			t.Errorf("%s matched at %v, too low to act on", name, score)
		}
	}

	out := call(t, "show_resolve", map[string]any{"title": "Severance.S02E07.1080p.ATVP.WEB-DL.H264-GROUP", "library": "Shows"})
	if str(out["parsed_title"]) != "Severance" || num(t, out["parsed_season"], "parsed_season") != 2 || num(t, out["parsed_episode"], "parsed_episode") != 7 {
		t.Errorf("parsed = %v", out)
	}
	if msg := callErr(t, "show_resolve", map[string]any{"title": "   "}); !strings.Contains(msg, "title") {
		t.Errorf("an empty title: %s", msg)
	}
}

// A1 and A2, and C1 and C3: what a series is missing, and - when that cannot
// be established - an answer that says so instead of an empty list.
//
// The fixture libraries run without a TMDB key in replay unless one is set,
// and neither server records the provider's run out of the box, so what is
// pinned unconditionally is the contract: supported is never true with a null
// list, and never false with a list that reads as complete.
func TestShowMissing(t *testing.T) {
	id := findItem(t, "Shows", "Series", "Severance")
	out := call(t, "show_missing", map[string]any{"series_id": id})
	if str(out["series"]) != "Severance" {
		t.Errorf("series = %v", out["series"])
	}

	supported, ok := out["supported"].(bool)
	if !ok {
		t.Fatalf("supported is %T, want a bool: the field that tells complete from unknown", out["supported"])
	}
	missing, present := out["missing"]
	if !present {
		t.Fatal("the answer has no missing field")
	}
	switch {
	case supported:
		if missing == nil {
			t.Error("supported with a null list: an answer that is both given and not")
		}
		for _, m := range rows(t, missing, "missing") {
			if num(t, m["season"], "season") == 0 || num(t, m["episode"], "episode") == 0 {
				t.Errorf("missing row = %v", m)
			}
			// nothing on disk may be reported missing
			if num(t, m["season"], "season") == 1 && num(t, m["episode"], "episode") <= 2 {
				t.Errorf("an episode on disk reported missing: %v", m)
			}
		}
		if str(out["source"]) == "none" {
			t.Errorf("supported from no source at all: %v", out)
		}
	default:
		if missing != nil {
			t.Errorf("unsupported with a list: %v - an empty list reads as complete", missing)
		}
		if str(out["reason"]) == "" {
			t.Error("unsupported without a reason: nothing tells a caller what would make it knowable")
		}
		if str(out["source"]) != "none" {
			t.Errorf("source = %v with nothing asked", out["source"])
		}
	}

	// the gaps between the files are given either way, and are never the
	// whole answer: Severance holds S01E01 and S01E02 with no gap between
	if gaps := out["gaps_on_disk"]; gaps != nil {
		for _, g := range rows(t, gaps, "gaps_on_disk") {
			if num(t, g["season"], "season") == 1 && num(t, g["episode"], "episode") <= 2 {
				t.Errorf("an episode on disk reported as a gap: %v", g)
			}
		}
	}

	// Star Trek The Next Generation holds E01 and E03 of season one with no
	// provider at all, so the gap between its files is the fact that survives
	tng := findItem(t, "Messy Shows", "Series", "Star Trek The Next Generation")
	out = call(t, "show_missing", map[string]any{"series_id": tng})
	if out["supported"] == true && out["missing"] == nil {
		t.Error("supported with a null list")
	}
	var found bool
	for _, g := range rows(t, out["gaps_on_disk"], "gaps_on_disk") {
		if num(t, g["season"], "season") == 1 && num(t, g["episode"], "episode") == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("S01E02 is skipped between the files on disk, and is not reported: %v", out)
	}
}

func TestShowFamilyIsComplete(t *testing.T) {
	var got []string
	for _, name := range toolNames(t) {
		if strings.HasPrefix(name, "show_") {
			got = append(got, name)
		}
	}
	want := []string{"show_episodes", "show_episodes_exist", "show_missing", "show_resolve", "show_seasons"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("show tools = %v, want %v", got, want)
	}
}
