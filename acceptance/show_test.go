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

	// paged two at a time by offset, the whole library comes back once each;
	// the next page starts at offset + limit
	seen := map[string]bool{}
	for offset := 0; offset < total; offset += 2 {
		page := call(t, "library_episodes", map[string]any{"library": "Shows", "limit": 2, "offset": offset})
		if num(t, page["offset"], "offset") != offset || num(t, page["total"], "total") != total {
			t.Errorf("page at %d says offset %v of %v", offset, page["offset"], page["total"])
		}
		for _, row := range rows(t, page["episodes"], "episodes") {
			key := fmt.Sprintf("%s S%02dE%02d", str(row["series"]), num(t, row["season"], "season"), num(t, row["episode"], "episode"))
			if seen[key] {
				t.Errorf("%s came back on two pages", key)
			}
			seen[key] = true
		}
	}
	if len(seen) != 9 {
		t.Errorf("paging returned %d episodes, want the library's 9: %v", len(seen), seen)
	}
	// past the end is an empty page, not an error
	if past := call(t, "library_episodes", map[string]any{"library": "Shows", "offset": total}); len(rows(t, past["episodes"], "episodes")) != 0 || num(t, past["offset"], "offset") != total {
		t.Errorf("past the end = %v", past)
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

	// a year the name does not carry: the right one is a match on the title
	// and the year, a wrong one marks the match down
	expanse := findItem(t, "Shows", "Series", "The Expanse")
	for year, want := range map[int]struct {
		score float64
		on    string
	}{2015: {1, "and year"}, 1999: {0.75, "but a different year"}} {
		out := call(t, "show_resolve", map[string]any{"title": "The Expanse", "library": "Shows", "year": year})
		cands := rows(t, out["candidates"], "candidates")
		if num(t, out["parsed_year"], "parsed_year") != year || len(cands) == 0 || str(cands[0]["series_id"]) != expanse {
			t.Errorf("The Expanse in %d resolved to %v (parsed year %v)", year, cands, out["parsed_year"])
			continue
		}
		if decimal(t, cands[0]["score"], "score") != want.score || !strings.Contains(str(cands[0]["matched_on"]), want.on) {
			t.Errorf("The Expanse in %d scored %v on %q, want %v on %q", year, cands[0]["score"], cands[0]["matched_on"], want.score, want.on)
		}
	}

	// across libraries the name is both copies of Severance; a limit keeps
	// the first
	both := rows(t, call(t, "show_resolve", map[string]any{"title": "Severance"})["candidates"], "candidates")
	if len(both) != 2 {
		t.Fatalf("Severance across libraries = %v, want the tidy and the messy copy", both)
	}
	one := rows(t, call(t, "show_resolve", map[string]any{"title": "Severance", "limit": 1})["candidates"], "candidates")
	if len(one) != 1 || str(one[0]["series_id"]) != str(both[0]["series_id"]) {
		t.Errorf("limit 1 = %v, want the first of %v", one, both)
	}
}

// A1 and A2, and C1 and C3: what a series is missing, and - when that cannot
// be established - an answer that says so instead of an empty list.
//
// Neither server records a series' run out of the box (Emby 4.10 dropped the
// import, stock Jellyfin needs the TheTVDB plugin), so the run comes from
// TMDB, which the suite always has a key and recordings for. The clean
// Severance holds the first two episodes of each of its two seasons; TMDB, as
// recorded, lists nine and ten, and an empty third season.
func TestShowMissing(t *testing.T) {
	needsTMDBRecording(t, "GET api.themoviedb.org/3/tv/95396/season/2")
	id := findItem(t, "Shows", "Series", "Severance")
	out := call(t, "show_missing", map[string]any{"series_id": id})
	if str(out["series"]) != "Severance" || out["supported"] != true || str(out["source"]) != "tmdb" || str(out["reason"]) != "" {
		t.Fatalf("show_missing Severance = series %v supported %v source %v reason %q", out["series"], out["supported"], out["source"], out["reason"])
	}
	var want []string
	for _, season := range []struct{ number, from, to int }{{1, 3, 9}, {2, 3, 10}} {
		for e := season.from; e <= season.to; e++ {
			want = append(want, fmt.Sprintf("S%02dE%02d", season.number, e))
		}
	}
	missing := func(out map[string]any) []string {
		var got []string
		for _, m := range rows(t, out["missing"], "missing") {
			got = append(got, fmt.Sprintf("S%02dE%02d", num(t, m["season"], "season"), num(t, m["episode"], "episode")))
		}
		return got
	}
	if got := missing(out); !slices.Equal(got, want) {
		t.Errorf("missing = %v, want %v", got, want)
	}
	if first := rows(t, out["missing"], "missing")[0]; str(first["name"]) != "In Perpetuity" || str(first["air_date"]) != "2022-02-24" {
		t.Errorf("the first missing episode = %v, want TMDB's S01E03 In Perpetuity, aired 2022-02-24", first)
	}
	// nothing is skipped between the files it holds
	if out["gaps_on_disk"] != nil || out["season_gaps_on_disk"] != nil {
		t.Errorf("gaps on disk = %v and seasons %v, want none", out["gaps_on_disk"], out["season_gaps_on_disk"])
	}
	// every episode TMDB lists has aired, so asking for the unaired as well
	// adds nothing: the empty third season has no episode to add
	if got := missing(call(t, "show_missing", map[string]any{"series_id": id, "include_unaired": true})); !slices.Equal(got, want) {
		t.Errorf("with include_unaired, missing = %v, want %v", got, want)
	}

	// Star Trek The Next Generation holds E01 and E03 of season one and no
	// ids at all: nothing can be asked, which the answer says rather than
	// answering with an empty list, and the gap between its files is the fact
	// that survives
	tng := findItem(t, "Messy Shows", "Series", "Star Trek The Next Generation")
	out = call(t, "show_missing", map[string]any{"series_id": tng})
	if out["supported"] != false || out["missing"] != nil || str(out["source"]) != "none" || !strings.Contains(str(out["reason"]), "identify it first") {
		t.Errorf("an unidentified series = supported %v missing %v source %v reason %q", out["supported"], out["missing"], out["source"], out["reason"])
	}
	gaps := rows(t, out["gaps_on_disk"], "gaps_on_disk")
	if len(gaps) != 1 || num(t, gaps[0]["season"], "season") != 1 || num(t, gaps[0]["episode"], "episode") != 2 {
		t.Errorf("gaps_on_disk = %v, want S01E02 alone", gaps)
	}

	// Zzyzx Paths skips its whole second season
	paths := findItem(t, "Messy Shows", "Series", "Zzyzx Paths")
	out = call(t, "show_missing", map[string]any{"series_id": paths})
	if seasons := out["season_gaps_on_disk"]; fmt.Sprint(seasons) != "[2]" {
		t.Errorf("season_gaps_on_disk = %v, want [2]", seasons)
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
