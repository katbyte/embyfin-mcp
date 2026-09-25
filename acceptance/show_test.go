//go:build integration

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A series' seasons by number, each with an id that reads back as that
// season of that series.
func TestShowSeasons(t *testing.T) {
	id := findItem(t, "Shows", "Series", "Severance")
	out := call(t, "show_seasons", map[string]any{"series_id": id})
	if str(out["series"]) != "Severance" {
		t.Errorf("series = %v", out["series"])
	}
	var numbers []int
	for _, s := range rows(t, out["seasons"], "seasons") {
		number := num(t, s["season"], "season")
		numbers = append(numbers, number)
		if want := fmt.Sprintf("Season %d", number); str(s["name"]) != want {
			t.Errorf("season %d is named %v, want %s", number, s["name"], want)
		}
		season := call(t, "item_get", map[string]any{"id": str(s["id"])})
		if str(season["type"]) != "Season" || str(season["series"]) != "Severance" || num(t, season["season"], "season") != number {
			t.Errorf("season %d's id %v reads as %v", number, s["id"], season)
		}
	}
	slices.Sort(numbers)
	if !slices.Equal(numbers, []int{1, 2}) {
		t.Errorf("seasons = %v, want [1 2]", numbers)
	}
	for _, bad := range []string{"00000000000000000000000000000000", unknownID()} {
		if msg := callErr(t, "show_seasons", map[string]any{"series_id": bad}); !strings.Contains(msg, "no item with id "+bad) {
			t.Errorf("an unknown series %s: %s", bad, msg)
		}
	}
}

// A show's episodes through library_episodes: by name or by id, the whole
// show or one season of it, each row carrying the quality facts as numbers,
// which is what deciding "is my copy better than the library's" reads (A5).
func TestLibraryEpisodesOfAShow(t *testing.T) {
	bb := findItem(t, "Shows", "Series", "Breaking Bad")
	numbers := func(out map[string]any) []int {
		var got []int
		for _, e := range rows(t, out["episodes"], "episodes") {
			got = append(got, num(t, e["episode"], "episode"))
		}
		return got
	}

	// by name: the show is named at the top, with how the name matched
	out := call(t, "library_episodes", map[string]any{"series": "Breaking Bad"})
	if str(out["series"]) != "Breaking Bad" || str(out["series_id"]) != bb {
		t.Errorf("series = %v %v, want Breaking Bad %s", out["series"], out["series_id"], bb)
	}
	if m := object(t, out["matched"], "matched"); str(m["series_id"]) != bb || decimal(t, m["score"], "score") != 1 {
		t.Errorf("matched = %v", m)
	}
	eps := rows(t, out["episodes"], "episodes")
	for _, e := range eps {
		if str(e["series"]) != "Breaking Bad" || num(t, e["season"], "season") != 1 {
			t.Errorf("episode row = %v", e)
		}
		assertQualityFacts(t, e)
	}
	if got := numbers(out); !slices.Equal(got, []int{1, 2, 3}) || num(t, out["total"], "total") != 3 {
		t.Errorf("episodes = %v of %v, want [1 2 3]", got, out["total"])
	}
	// the nfo named them
	if !slices.ContainsFunc(eps, func(e map[string]any) bool { return str(e["title"]) == "Pilot" }) {
		t.Errorf("no episode titled Pilot among %v", eps)
	}
	// by id, the same show, and no matching to report
	byID := call(t, "library_episodes", map[string]any{"series": bb})
	if str(byID["series"]) != "Breaking Bad" || byID["matched"] != nil || !slices.Equal(numbers(byID), []int{1, 2, 3}) {
		t.Errorf("by id = %v %v %v", byID["series"], byID["matched"], numbers(byID))
	}

	// Severance is held twice, a tidy copy and a messy one: its name alone is
	// refused naming both, and a library says which
	msg := callErr(t, "library_episodes", map[string]any{"series": "Severance"})
	if !strings.Contains(msg, "matches 2 series") || strings.Count(msg, "Severance (2022)") != 2 || !strings.Contains(msg, "give series_id") {
		t.Errorf("Severance by name alone = %s", msg)
	}
	sev := findItem(t, "Shows", "Series", "Severance")
	for season, want := range map[int][]int{1: {1, 2}, 2: {1, 2}, 0: nil, 9: nil} {
		out = call(t, "library_episodes", map[string]any{"series": "Severance", "library": "Shows", "season": season})
		for _, e := range rows(t, out["episodes"], "episodes") {
			if num(t, e["season"], "season") != season {
				t.Errorf("season %d listing has %v", season, e)
			}
		}
		if got := numbers(out); !slices.Equal(got, want) || num(t, out["total"], "total") != len(want) || str(out["series_id"]) != sev {
			t.Errorf("season %d of the tidy Severance = %v of %v (%v), want %v", season, got, out["total"], out["series_id"], want)
		}
	}

	// the tidy Severance's id is no series in Messy Shows: a library narrows
	// a name, and an id it does not hold there is read as a name nothing has
	if msg := callErr(t, "library_episodes", map[string]any{"series": sev, "library": "Messy Shows"}); !strings.Contains(msg, fmt.Sprintf("no series named %q", sev)) {
		t.Errorf("a series id with a library it is not in: %s", msg)
	}
	// a name that is only a guess is refused naming the guess
	msg = callErr(t, "library_episodes", map[string]any{"series": "Breaking Bad Insider", "library": "Shows"})
	for _, want := range []string{"matches nothing well enough to act on in Shows", "the closest is Breaking Bad (2008) id " + bb, "a guess rather than a match"} {
		if !strings.Contains(msg, want) {
			t.Errorf("a spin-off's name = %s, want it saying %q", msg, want)
		}
	}

	// the refusals: the show twice over, a season with no show, a fact that
	// is no fact, and an id nothing has
	for want, args := range map[string]map[string]any{
		"give series or series_id, not both":                              {"series": "Breaking Bad", "series_id": bb},
		"season needs series or series_id":                                {"season": 1},
		`no such field "heigth"`:                                          {"series_id": bb, "fields": []string{"heigth"}},
		"no item with id " + unknownID():                                  {"series_id": unknownID()},
		`no library named "Nope" (have: `:                                 {"library": "Nope"},
		"give library or series_id, not both: a series is already in one": {"library": "Shows", "series_id": bb},
	} {
		if msg := callErr(t, "library_episodes", args); !strings.Contains(msg, want) {
			t.Errorf("library_episodes %v: %s, want %q", args, msg, want)
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
}

// A3 and C4: every episode in a library in one paged read, with the quality
// facts, and page boundaries that neither drop a row nor repeat one.
func TestLibraryEpisodes(t *testing.T) {
	out := call(t, "library_episodes", map[string]any{"library": "Shows"})
	total := num(t, out["total"], "total")
	if total != showEpisodes() {
		t.Fatalf("total = %d, want the Shows library's %d episode files", total, showEpisodes())
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
	if len(seen) != showEpisodes() {
		t.Errorf("paging returned %d episodes, want the library's %d: %v", len(seen), showEpisodes(), seen)
	}
	// past the end is an empty page, not an error
	if past := call(t, "library_episodes", map[string]any{"library": "Shows", "offset": total}); len(rows(t, past["episodes"], "episodes")) != 0 || num(t, past["offset"], "offset") != total {
		t.Errorf("past the end = %v", past)
	}

	// with no library, every library's: the clean shows' and the messy ones'.
	// A limit past the most one page holds is not refused; the page is capped
	// at a thousand, which the whole server here does not reach
	everything := call(t, "library_episodes", map[string]any{"quality": false, "limit": 5000})
	if n := num(t, everything["total"], "total"); n != showEpisodes()+messyEpisodes() || len(rows(t, everything["episodes"], "episodes")) != n {
		t.Errorf("every library's episodes = %d rows of %d, want %d", len(rows(t, everything["episodes"], "episodes")), n, showEpisodes()+messyEpisodes())
	}

	// one series on its own, and one season of it, counted
	sev := findItem(t, "Shows", "Series", "Severance")
	out = call(t, "library_episodes", map[string]any{"series_id": sev})
	if n := len(rows(t, out["episodes"], "episodes")); n != 4 {
		t.Errorf("Severance has %d episode files, want 4", n)
	}
	out = call(t, "library_episodes", map[string]any{"series_id": sev, "season": 2, "quality": false})
	var season2 []int
	for _, row := range rows(t, out["episodes"], "episodes") {
		if num(t, row["season"], "season") != 2 {
			t.Errorf("season 2 read has %v", row)
		}
		if row["width"] != nil {
			t.Errorf("quality=false still carried the facts: %v", row)
		}
		season2 = append(season2, num(t, row["episode"], "episode"))
	}
	if !slices.Equal(season2, []int{1, 2}) {
		t.Errorf("season 2 read = episodes %v, want [1 2]", season2)
	}
	// with_file false keeps episodes the server knows of and holds no file
	// for; neither server keeps such a record here, so it is the same read
	withRecords := call(t, "library_episodes", map[string]any{"series_id": sev, "with_file": false, "quality": false})
	if a, b := episodeKeys(t, withRecords), episodeKeys(t, call(t, "library_episodes", map[string]any{"series_id": sev, "quality": false})); !slices.Equal(a, b) || len(a) != 4 {
		t.Errorf("with_file false = %v, with files only %v", a, b)
	}
}

// episodeKeys names each row of a library_episodes answer, in order.
func episodeKeys(t *testing.T, out map[string]any) []string {
	t.Helper()

	var keys []string
	for _, row := range rows(t, out["episodes"], "episodes") {
		keys = append(keys, fmt.Sprintf("%s S%02dE%02d", str(row["series"]), num(t, row["season"], "season"), num(t, row["episode"], "episode")))
	}

	return keys
}

// A season read whole sets each file's runtime against the season's median:
// the messy Severance's third episode runs five seconds to its season's one,
// and .hack//Liminality's last a second to its season's three minutes.
func TestLibraryEpisodesRuntimeMultiples(t *testing.T) {
	sev := findItem(t, "Messy Shows", "Series", "Severance")
	var hack string
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Shows", "limit": 50})["items"], "items") {
		if str(it["name"]) == ".hack//Liminality" {
			hack = str(it["id"])
		}
	}
	for _, c := range []struct {
		series    string
		median    int
		multiples []float64
	}{
		{sev, 1, []float64{1, 1, 5}},
		{hack, 180, []float64{1, 1, 0.01}},
	} {
		// season one: on Emby the messy Severance's season Extras featurette
		// is an episode too, of no season it can be held to
		out := call(t, "library_episodes", map[string]any{"series_id": c.series, "season": 1, "fields": []string{"runtime_s", "runtime_multiple"}})
		var got []float64
		for _, row := range rows(t, out["episodes"], "episodes") {
			got = append(got, decimal(t, row["runtime_multiple"], "runtime_multiple"))
			if m := num(t, row["season_median_runtime_s"], "season_median_runtime_s"); m != c.median {
				t.Errorf("%v S01E%02d's season median = %d, want %d", out["series"], num(t, row["episode"], "episode"), m, c.median)
			}
			// narrowed to these two, nothing else comes back
			if row["path"] != nil || row["width"] != nil {
				t.Errorf("fields not asked for came back: %v", row)
			}
		}
		if !slices.Equal(got, c.multiples) {
			t.Errorf("%v's runtime multiples = %v, want %v", out["series"], got, c.multiples)
		}
	}
	// a season's median needs three files, and a page of a library read holds
	// part of a season at best, so a library read answers none
	for _, row := range rows(t, call(t, "library_episodes", map[string]any{"library": "Messy Shows", "fields": []string{"runtime_multiple"}})["episodes"], "episodes") {
		if row["runtime_multiple"] != nil {
			t.Errorf("a library read set a runtime multiple: %v", row)
		}
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
	if answers[0]["exists"] != true || answers[0]["known"] != true || str(answers[0]["title"]) != "Good News About Hell" {
		t.Errorf("S01E01 is on disk: %v", answers[0])
	}
	if answers[1]["exists"] != false || answers[1]["known"] != false || answers[1]["id"] != nil {
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
	if got := rows(t, byName["episodes"], "episodes"); len(got) != 1 || got[0]["exists"] != true {
		t.Errorf("by name = %v", byName)
	}

	// a hit asked for its facts sets its runtime against its season's: the
	// messy Severance's third episode runs five times the other two
	messy := findItem(t, "Messy Shows", "Series", "Severance")
	out = call(t, "show_episodes_exist", map[string]any{"series_id": messy, "fields": []string{"runtime_s", "runtime_multiple"}, "episodes": []map[string]any{{"season": 1, "episode": 3}, {"season": 1, "episode": 4}}})
	got := rows(t, out["episodes"], "episodes")
	if len(got) != 2 || num(t, got[0]["runtime_s"], "runtime_s") != 5 || decimal(t, got[0]["runtime_multiple"], "runtime_multiple") != 5 || num(t, got[0]["season_median_runtime_s"], "season_median_runtime_s") != 1 {
		t.Errorf("the messy S01E03 = %v, want 5s, five times its season's 1s", got)
	}
	// a miss has no file to have a runtime
	if len(got) == 2 && (got[1]["exists"] != false || got[1]["runtime_multiple"] != nil) {
		t.Errorf("the missing S01E04 = %v", got[1])
	}

	for want, args := range map[string]map[string]any{
		"at least one": {"series_id": sev, "episodes": []map[string]any{}},
		"episode must be 1 or more, got 0 for season 1":   {"series_id": sev, "episodes": []map[string]any{{"season": 1, "episode": 1}, {"season": 1, "episode": 0}}},
		"episode must be 1 or more, got -2 for season 2":  {"series_id": sev, "episodes": []map[string]any{{"season": 2, "episode": -2}}},
		"no item with id " + unknownID():                  {"series_id": unknownID(), "episodes": []map[string]any{{"season": 1, "episode": 1}}},
		"a series is required: give series_id, or series": {"episodes": []map[string]any{{"season": 1, "episode": 1}}},
	} {
		if msg := callErr(t, "show_episodes_exist", args); !strings.Contains(msg, want) {
			t.Errorf("show_episodes_exist %v: %s, want %q", args, msg, want)
		}
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
	for want, args := range map[string]map[string]any{
		"a title is required": {"title": "   "},
		// nothing left once the punctuation goes
		`no title could be read out of "..."`: {"title": "..."},
		`no library named "Nope" (have: `:     {"title": "Severance", "library": "Nope"},
	} {
		if msg := callErr(t, "show_resolve", args); !strings.Contains(msg, want) {
			t.Errorf("show_resolve %v: %s, want %q", args, msg, want)
		}
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
	// and a year the name does carry is read out of it, and weighed the same
	for name, want := range map[string]struct {
		year  int
		score float64
	}{"The Expanse (2015)": {2015, 1}, "The.Expanse.1999.S01E01.720p": {1999, 0.75}} {
		out := call(t, "show_resolve", map[string]any{"title": name, "library": "Shows"})
		cands := rows(t, out["candidates"], "candidates")
		if str(out["parsed_title"]) != "The Expanse" || num(t, out["parsed_year"], "parsed_year") != want.year || len(cands) != 1 || str(cands[0]["series_id"]) != expanse || decimal(t, cands[0]["score"], "score") != want.score {
			t.Errorf("%s = parsed %q in %v, candidates %v; want The Expanse in %d scoring %v", name, out["parsed_title"], out["parsed_year"], cands, want.year, want.score)
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
	if got := missingKeys(t, out); !slices.Equal(got, want) {
		t.Errorf("missing = %v, want %v", got, want)
	}
	if first := rows(t, out["missing"], "missing"); len(first) == 0 || str(first[0]["name"]) != "In Perpetuity" || str(first[0]["air_date"]) != "2022-02-24" {
		t.Errorf("the first missing episode = %v, want TMDB's S01E03 In Perpetuity, aired 2022-02-24", first)
	}
	// nothing is skipped between the files it holds
	if out["gaps_on_disk"] != nil || out["season_gaps_on_disk"] != nil {
		t.Errorf("gaps on disk = %v and seasons %v, want none", out["gaps_on_disk"], out["season_gaps_on_disk"])
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

	// Star Trek: Deep Space Nine holds an episode of seasons 1 and 3 and
	// skips the whole second season between them
	ds9 := findItem(t, "Messy Shows", "Series", "Star Trek: Deep Space Nine")
	out = call(t, "show_missing", map[string]any{"series_id": ds9})
	if seasons := out["season_gaps_on_disk"]; fmt.Sprint(seasons) != "[2]" || out["gaps_on_disk"] != nil {
		t.Errorf("season_gaps_on_disk = %v and gaps_on_disk %v, want season 2 alone", seasons, out["gaps_on_disk"])
	}

	if msg := callErr(t, "show_missing", map[string]any{"series_id": unknownID()}); !strings.Contains(msg, "no item with id "+unknownID()) {
		t.Errorf("an unknown series: %s", msg)
	}
}

// missingKeys names each episode a show_missing answer lists, in order.
func missingKeys(t *testing.T, out map[string]any) []string {
	t.Helper()

	var got []string
	for _, m := range rows(t, out["missing"], "missing") {
		got = append(got, fmt.Sprintf("S%02dE%02d", num(t, m["season"], "season"), num(t, m["episode"], "episode")))
	}

	return got
}

// stagedShows is the TV library TestStagedShows lays out and takes away
// again: shapes the lasting fixtures do not hold, in a library of their own
// so no other library's counts move.
const stagedShows = "Staged Shows"

// Shapes a series takes that the lasting fixtures do not: specials, a file
// holding two episodes, more seasons than one call reads one at a time, and
// a show TMDB is asked after by its TVDB or IMDb id because the show carries
// no TMDB one. Star Trek: Deep Space Nine is laid out with a special, an
// episode of each of its first four seasons and a two-episode file; The
// Expanse twice, a copy known by its TVDB id alone and one by its IMDb id
// alone, each holding the two episodes the tidy copy does.
func TestStagedShows(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	root := filepath.Join(dataDir(), "staged-shows")
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	lay := func(path string, raw []byte) {
		mediaMkdir(t, filepath.Dir(filepath.Join(root, path)))
		mediaWrite(t, filepath.Join(root, path), raw)
	}
	special := fixtureVideo(t, "anime-src", "special.mp4")
	episode := fixtureVideo(t, "messy-shows", "Star Trek Deep Space Nine (1993)", "Season 01", "Star Trek Deep Space Nine S01E01.mp4")
	ds9 := "Star Trek Deep Space Nine (1993)"
	lay(ds9+"/tvshow.nfo", showNfo("Star Trek: Deep Space Nine", nil))
	lay(ds9+"/Season 00/Star Trek Deep Space Nine S00E01.mp4", special)
	lay(ds9+"/Season 01/Star Trek Deep Space Nine S01E01.mp4", episode)
	lay(ds9+"/Season 01/Star Trek Deep Space Nine S01E02E03.mp4", episode)
	for season := 2; season <= 4; season++ {
		lay(fmt.Sprintf("%s/Season %02d/Star Trek Deep Space Nine S%02dE01.mp4", ds9, season, season), episode)
	}
	for folder, ids := range map[string]map[string]string{
		"The Expanse (2015)":      {"tvdb": "280619"},
		"The Expanse (2015) IMDb": {"imdb": "tt3230854"},
	} {
		lay(folder+"/tvshow.nfo", showNfo("The Expanse", ids))
		// the tidy copy's season as it is, files and nfos
		entries, err := os.ReadDir(filepath.Join(dataDir(), "shows", "The Expanse", "Season 01"))
		if err != nil || len(entries) != 4 {
			t.Fatalf("the tidy The Expanse's season one = %v, %v", entries, err)
		}
		for _, e := range entries {
			lay(folder+"/Season 01/"+e.Name(), fixtureVideo(t, "shows", "The Expanse", "Season 01", e.Name()))
		}
	}

	t.Cleanup(func() {
		if _, err := invoke("library_delete", map[string]any{"library": stagedShows, "confirm": true}); err != nil {
			t.Errorf("removing %s: %v", stagedShows, err)
		}
		if err := waitForExpectedScan(); err != nil {
			t.Error(err)
		}
	})
	call(t, "library_create", map[string]any{"name": stagedShows, "type": "tvshows", "paths": []any{"/media/staged-shows"}, "scan": true})
	// three series and the ten episode files between them
	if !eventuallyWithin(scanPatience, func() bool {
		counts := typeCounts(stagedShows)
		return counts["Series"] == 3 && counts["Episode"] == 10
	}) {
		t.Fatalf("%s never held its three series and ten episodes: %v", stagedShows, typeCounts(stagedShows))
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
	series := map[string]string{}
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": stagedShows})["items"], "items") {
		series[str(it["path"])] = str(it["id"])
	}
	ds9ID := series["/media/staged-shows/"+ds9]
	tvdbOnly, imdbOnly := series["/media/staged-shows/The Expanse (2015)"], series["/media/staged-shows/The Expanse (2015) IMDb"]
	if ds9ID == "" || tvdbOnly == "" || imdbOnly == "" {
		t.Fatalf("the staged series = %v", series)
	}

	t.Run("specials", func(t *testing.T) {
		// season 0 is a season like the others, and says its number
		var numbers []int
		for _, s := range rows(t, call(t, "show_seasons", map[string]any{"series_id": ds9ID})["seasons"], "seasons") {
			numbers = append(numbers, num(t, s["season"], "season"))
		}
		slices.Sort(numbers)
		if !slices.Equal(numbers, []int{0, 1, 2, 3, 4}) {
			t.Errorf("the staged Deep Space Nine's seasons = %v, want the specials and 1 to 4", numbers)
		}
		out := call(t, "library_episodes", map[string]any{"series_id": ds9ID, "season": 0})
		if got := episodeKeys(t, out); !slices.Equal(got, []string{"Star Trek: Deep Space Nine S00E01"}) {
			t.Errorf("the specials = %v", got)
		}
		exists := call(t, "show_episodes_exist", map[string]any{"series_id": ds9ID, "episodes": []map[string]any{{"season": 0, "episode": 1}, {"season": 0, "episode": 2}}})
		if got := rows(t, exists["episodes"], "episodes"); len(got) != 2 || got[0]["exists"] != true || got[1]["exists"] != false {
			t.Errorf("specials 1 and 2 = %v, want the first held", got)
		}
	})

	t.Run("a file holding two episodes", func(t *testing.T) {
		out := call(t, "library_episodes", map[string]any{"series_id": ds9ID, "season": 1, "quality": false})
		var run map[string]any
		for _, row := range rows(t, out["episodes"], "episodes") {
			if num(t, row["episode"], "episode") == 2 {
				run = row
			}
		}
		if run == nil || num(t, run["episode_end"], "episode_end") != 3 {
			t.Errorf("S01E02E03 = %v, want episode 2 ending at 3", run)
		}
		exists := call(t, "show_episodes_exist", map[string]any{"series_id": ds9ID, "episodes": []map[string]any{{"season": 1, "episode": 1}, {"season": 1, "episode": 3}}})
		got := rows(t, exists["episodes"], "episodes")
		if len(got) != 2 || got[0]["covered_by"] != nil || got[1]["exists"] != true || str(got[1]["covered_by"]) != "S01E02E03" {
			t.Errorf("S01E01 and S01E03 = %v, want E03 held by the S01E02E03 file", got)
		}
	})

	t.Run("more seasons than are read one at a time", func(t *testing.T) {
		// five seasons asked after is past the three read one at a time, so
		// the series is read whole; the answer is the same either way
		ask := []map[string]any{{"season": 4, "episode": 1}, {"season": 0, "episode": 1}, {"season": 2, "episode": 1}, {"season": 5, "episode": 1}, {"season": 1, "episode": 3}, {"season": 3, "episode": 2}}
		out := call(t, "show_episodes_exist", map[string]any{"series_id": ds9ID, "episodes": ask})
		var got []string
		for _, row := range rows(t, out["episodes"], "episodes") {
			got = append(got, fmt.Sprintf("S%02dE%02d %v", num(t, row["season"], "season"), num(t, row["episode"], "episode"), row["exists"]))
		}
		want := []string{"S04E01 true", "S00E01 true", "S02E01 true", "S05E01 false", "S01E03 true", "S03E02 false"}
		if !slices.Equal(got, want) || num(t, out["absent"], "absent") != 2 {
			t.Errorf("six episodes across five seasons = %v (absent %v), want %v", got, out["absent"], want)
		}
	})

	t.Run("a run found by another provider's id", func(t *testing.T) {
		// TMDB is asked which of its shows the TVDB or IMDb id is, and the
		// run it names is The Expanse's: the copy holding the same two
		// episodes as the tidy one is missing what the tidy one is
		needsTMDBRecording(t, "GET api.themoviedb.org/3/find/280619?external_source=tvdb_id")
		needsTMDBRecording(t, "GET api.themoviedb.org/3/find/tt3230854?external_source=imdb_id")
		tidy := call(t, "show_missing", map[string]any{"series_id": findItem(t, "Shows", "Series", "The Expanse")})
		want := missingKeys(t, tidy)
		if len(want) == 0 || want[0] != "S01E03" {
			t.Fatalf("the tidy The Expanse is missing %v, want the run from S01E03", want)
		}
		for name, id := range map[string]string{"TVDB": tvdbOnly, "IMDb": imdbOnly} {
			out := call(t, "show_missing", map[string]any{"series_id": id})
			if out["supported"] != true || str(out["source"]) != "tmdb" || str(out["reason"]) != "" {
				t.Errorf("the copy known by its %s id = supported %v source %v reason %q", name, out["supported"], out["source"], out["reason"])
				continue
			}
			if got := missingKeys(t, out); !slices.Equal(got, want) {
				t.Errorf("the copy known by its %s id is missing %v, want the tidy copy's %v", name, got, want)
			}
		}
	})
}

// typeCounts is a library's type_counts right now, empty when it cannot be
// read.
func typeCounts(library string) map[string]int {
	out, err := invoke("library_get", map[string]any{"library": library})
	counts := map[string]int{}
	if err != nil {
		return counts
	}
	raw, _ := out["type_counts"].(map[string]any)
	for k, v := range raw {
		counts[k] = numOr0(v)
	}

	return counts
}

// The clean The Expanse holds the first of TMDB's specials, in Season 00,
// beside its first two episodes. Season 0 is a season like the others to the
// tools that list them, and no part of a run to the ones that look for what
// is missing: the specials have no order for a gap to be in, and TMDB lists
// dozens the library is not missing in any sense a caller acts on.
func TestSpecialsInAShow(t *testing.T) {
	expanse := findItem(t, "Shows", "Series", "The Expanse")
	names := map[int]string{}
	for _, s := range rows(t, call(t, "show_seasons", map[string]any{"series_id": expanse})["seasons"], "seasons") {
		names[num(t, s["season"], "season")] = str(s["name"])
	}
	// Emby names a season as TMDB does, Jellyfin by its number
	first := "Season 1"
	if !isJellyfin() {
		first = "Leviathan Wakes"
	}
	if len(names) != 2 || names[0] != "Specials" || names[1] != first {
		t.Errorf("The Expanse's seasons = %v, want Specials and %s", names, first)
	}
	specials := call(t, "library_episodes", map[string]any{"series_id": expanse, "season": 0})
	eps := rows(t, specials["episodes"], "episodes")
	if len(eps) != 1 || num(t, eps[0]["season"], "season") != 0 || num(t, eps[0]["episode"], "episode") != 1 || str(eps[0]["title"]) != "Inside The Expanse: Episode 1" {
		t.Errorf("the specials = %v, want TMDB's first", eps)
	}
	exists := rows(t, call(t, "show_episodes_exist", map[string]any{"series_id": expanse, "episodes": []map[string]any{{"season": 0, "episode": 1}, {"season": 0, "episode": 2}}})["episodes"], "episodes")
	if len(exists) != 2 || exists[0]["exists"] != true || exists[1]["exists"] != false {
		t.Errorf("specials 1 and 2 = %v, want the first held", exists)
	}

	// nothing on disk is a gap, and nothing TMDB lists among the specials is
	// missing
	plain := call(t, "audit_missing_episodes", map[string]any{"library": "Shows"})
	if num(t, plain["total_findings"], "total_findings") != 0 || num(t, plain["items_scanned"], "items_scanned") != showEpisodes() {
		t.Errorf("from the files = %v, want none of the %d episodes a gap", plain, showEpisodes())
	}
	needsTMDBRecording(t, "GET api.themoviedb.org/3/tv/63639")
	missing := call(t, "show_missing", map[string]any{"series_id": expanse})
	if got := missingKeys(t, missing); len(got) == 0 || got[0] != "S01E03" || slices.ContainsFunc(got, func(k string) bool { return strings.HasPrefix(k, "S00") }) {
		t.Errorf("show_missing The Expanse = %v, want the run from S01E03 and no special", got)
	}
	for _, f := range rows(t, call(t, "audit_missing_episodes", map[string]any{"library": "Shows", "provider": true})["findings"], "findings") {
		if strings.Contains(str(f["detail"]), "S00") {
			t.Errorf("audit_missing_episodes lists a special: %v", f)
		}
	}
}
