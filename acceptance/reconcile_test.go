//go:build integration

package acceptance

import (
	"strings"
	"testing"
)

// The tools a folder reconcile leans on, against real servers. The unit tests
// run these against a canned server, which is exactly where they are weakest:
// every matching bug found so far came from how a real server's search
// behaves, not from the scoring.

// quality facts on the episodes an existence check finds, and only those.
func TestShowEpisodesExistQuality(t *testing.T) {
	sev := findItem(t, "Shows", "Series", "Severance")
	ask := []map[string]any{{"season": 1, "episode": 1}, {"season": 1, "episode": 9}}

	out := call(t, "show_episodes_exist", map[string]any{"series_id": sev, "episodes": ask, "quality": true})
	answers := rows(t, out["episodes"], "episodes")
	hit, miss := answers[0], answers[1]
	assertQualityFacts(t, hit)
	if fps, _ := hit["frame_rate"].(float64); fps < 4.9 || fps > 5.1 {
		t.Errorf("frame_rate = %v, want the fixture's 5", hit["frame_rate"])
	}
	if hit["hdr"] != nil {
		t.Errorf("hdr = %v on an SDR test pattern", hit["hdr"])
	}
	tracks := rows(t, hit["audio"], "audio")
	if len(tracks) != 1 || str(tracks[0]["codec"]) != "aac" || num(t, tracks[0]["channels"], "channels") != 1 {
		t.Errorf("audio = %v, want the fixture's mono aac", hit["audio"])
	}
	// a miss has no file to describe
	for _, field := range []string{"width", "size", "audio", "frame_rate"} {
		if miss[field] != nil {
			t.Errorf("a miss carried %s: %v", field, miss)
		}
	}

	// asked for particular facts, only those come back - and asking for them
	// is asking for the facts, without quality as well
	narrow := call(t, "show_episodes_exist", map[string]any{"series_id": sev, "episodes": ask, "fields": []string{"height", "frame_rate"}})
	row := rows(t, narrow["episodes"], "episodes")[0]
	if num(t, row["height"], "height") <= 0 || row["frame_rate"] == nil {
		t.Errorf("the fields asked for are missing: %v", row)
	}
	for _, gone := range []string{"width", "path", "audio", "size", "video_codec"} {
		if row[gone] != nil {
			t.Errorf("%s was not asked for: %v", gone, row)
		}
	}
	if row["id"] == nil || num(t, row["season"], "season") != 1 {
		t.Errorf("narrowing dropped what identifies the row: %v", row)
	}
	if msg := callErr(t, "show_episodes_exist", map[string]any{"series_id": sev, "episodes": ask, "fields": []string{"heigth"}}); !strings.Contains(msg, "heigth") {
		t.Errorf("a misspelled field: %s", msg)
	}
}

// several series in one call, each answered on its own.
func TestShowEpisodesExistBatch(t *testing.T) {
	sev := findItem(t, "Shows", "Series", "Severance")

	out := call(t, "show_episodes_exist", map[string]any{"queries": []map[string]any{
		{"series_id": sev, "episodes": []map[string]any{{"season": 1, "episode": 1}, {"season": 1, "episode": 9}}},
		{"series": "Breaking Bad", "episodes": []map[string]any{{"season": 1, "episode": 3}}},
		{"series": "no show by this name at all", "episodes": []map[string]any{{"season": 1, "episode": 1}}},
		{"series": "The Expanse", "episodes": []map[string]any{{"season": 1, "episode": 2}}},
	}})
	groups := rows(t, out["results"], "results")
	if len(groups) != 4 {
		t.Fatalf("asked after 4 series, answered %d: %v", len(groups), groups)
	}
	// in the order asked, so a caller can line them up with what it asked
	for i, want := range []string{"Severance", "Breaking Bad", "no show by this name at all", "The Expanse"} {
		if got := str(groups[i]["series"]); got != want {
			t.Errorf("group %d is %q, want %q", i, got, want)
		}
	}
	for _, i := range []int{1, 3} {
		if rows(t, groups[i]["episodes"], "episodes")[0]["exists"] != true {
			t.Errorf("%s: %v", groups[i]["series"], groups[i])
		}
	}
	// the one that did not resolve fails alone, and says so rather than
	// reporting its episodes absent
	bad := groups[2]
	if str(bad["error"]) == "" || len(rows(t, bad["episodes"], "episodes")) != 0 || num(t, bad["absent"], "absent") != 0 {
		t.Errorf("an unresolvable series = %v", bad)
	}
	if num(t, out["absent"], "absent") != 1 {
		t.Errorf("absent across the batch = %v, want 1", out["absent"])
	}

	if msg := callErr(t, "show_episodes_exist", map[string]any{
		"series_id": sev, "episodes": []map[string]any{{"season": 1, "episode": 1}},
		"queries": []map[string]any{{"series_id": sev, "episodes": []map[string]any{{"season": 1, "episode": 1}}}},
	}); !strings.Contains(msg, "not both") {
		t.Errorf("both shapes at once: %s", msg)
	}
	big := make([]map[string]any, 51)
	for i := range big {
		big[i] = map[string]any{"series_id": sev, "episodes": []map[string]any{{"season": 1, "episode": 1}}}
	}
	if msg := callErr(t, "show_episodes_exist", map[string]any{"queries": big}); !strings.Contains(msg, "50") {
		t.Errorf("an oversized batch: %s", msg)
	}
}

// how a name was matched comes back with the answer, and a name that matches
// nothing well enough is refused rather than answered about the nearest show.
func TestShowEpisodesExistMatching(t *testing.T) {
	sev := findItem(t, "Shows", "Series", "Severance")
	ep := []map[string]any{{"season": 1, "episode": 1}}

	out := call(t, "show_episodes_exist", map[string]any{"series": "Breaking Bad", "episodes": ep})
	match := object(t, out["matched"], "matched")
	if decimal(t, match["score"], "score") < 0.9 || str(match["matched_on"]) == "" {
		t.Errorf("matched = %v", match)
	}
	if byID := call(t, "show_episodes_exist", map[string]any{"series_id": sev, "episodes": ep}); byID["matched"] != nil {
		t.Errorf("an id needs no matching: %v", byID["matched"])
	}

	// punctuation and accents the release name has and the library does not
	for _, name := range []string{"Sévérance", "Breaking.Bad", "The Expanse."} {
		if _, err := invoke("show_episodes_exist", map[string]any{"series": name, "library": "Shows", "episodes": ep}); err != nil {
			t.Errorf("%q did not resolve: %v", name, err)
		}
	}

	// a spin-off does not resolve to its parent: the extra words are what say
	// which show it is, so the only candidate is a guess, not a match
	if msg := callErr(t, "show_episodes_exist", map[string]any{"series": "Breaking Bad Insider", "library": "Shows", "episodes": ep}); !strings.Contains(msg, "nothing well enough") {
		t.Errorf("a spin-off name was not refused: %s", msg)
	}
	// and a name sharing words with the only show that answers is not that show
	if msg := callErr(t, "show_episodes_exist", map[string]any{"series": "Star Trek Picard", "library": "Messy Shows", "episodes": ep}); !strings.Contains(msg, "nothing well enough") {
		t.Errorf("a name sharing two words with the only candidate was not refused: %s", msg)
	}
}

// the fixtures hold Severance twice, a tidy copy and a messy one: every answer
// about either names the other, and an absence says it may be there instead.
func TestShowEpisodesExistDuplicates(t *testing.T) {
	tidy := findItem(t, "Shows", "Series", "Severance")
	messy := findItem(t, "Messy Shows", "Series", "Severance")

	held := call(t, "show_episodes_exist", map[string]any{"series_id": tidy, "episodes": []map[string]any{{"season": 1, "episode": 1}}})
	if others := strs(t, held["duplicate_entries"], "duplicate_entries"); len(others) != 1 || others[0] != messy {
		t.Errorf("duplicate_entries = %v, want the messy copy %s", held["duplicate_entries"], messy)
	}
	// nothing absent, so nothing to warn about
	if str(held["warning"]) != "" {
		t.Errorf("warned with nothing absent: %v", held["warning"])
	}

	// the messy copy holds S01E03 and the tidy one does not
	absent := call(t, "show_episodes_exist", map[string]any{"series_id": tidy, "episodes": []map[string]any{{"season": 1, "episode": 3}}})
	if rows(t, absent["episodes"], "episodes")[0]["exists"] != false {
		t.Fatalf("the tidy copy holds S01E03? %v", absent)
	}
	if w := str(absent["warning"]); !strings.Contains(w, messy) || !strings.Contains(w, "not proof") {
		t.Errorf("an absence the other entry may hold was not warned about: %q", w)
	}

	// and a show held once says nothing
	bb := findItem(t, "Shows", "Series", "Breaking Bad")
	if once := call(t, "show_episodes_exist", map[string]any{"series_id": bb, "episodes": []map[string]any{{"season": 9, "episode": 9}}}); once["duplicate_entries"] != nil || str(once["warning"]) != "" {
		t.Errorf("a show held once: %v", once)
	}
}

// the bulk read's lean and narrowed shapes.
func TestLibraryEpisodesShapes(t *testing.T) {
	sev := findItem(t, "Shows", "Series", "Severance")

	lean := call(t, "library_episodes", map[string]any{"series_id": sev, "quality": false})
	for _, row := range rows(t, lean["episodes"], "episodes") {
		if row["path"] != nil || row["width"] != nil {
			t.Errorf("quality=false carried the path or the facts: %v", row)
		}
		if row["id"] == nil || str(row["series"]) == "" {
			t.Errorf("quality=false dropped what names the episode: %v", row)
		}
	}

	narrow := call(t, "library_episodes", map[string]any{"series_id": sev, "fields": []string{"frame_rate", "audio"}})
	for _, row := range rows(t, narrow["episodes"], "episodes") {
		if row["frame_rate"] == nil || len(rows(t, row["audio"], "audio")) != 1 {
			t.Errorf("the fields asked for are missing: %v", row)
		}
		if row["path"] != nil || row["width"] != nil || row["size"] != nil {
			t.Errorf("fields not asked for came back: %v", row)
		}
	}
}

// release names in the shapes that broke the parser, resolved against a real
// server's search rather than a canned one.
func TestShowResolveShapes(t *testing.T) {
	for _, tc := range []struct{ name, library, want string }{
		{"www.example.org    -    Severance.S01E01.1080p.WEB.H264-GROUP", "Shows", "Severance"},
		{"example.net - Breaking.Bad.S01E02.720p.HDTV.x264-GROUP", "Shows", "Breaking Bad"},
		{"Sévérance S02E01 1080p WEB-DL-GROUP", "Shows", "Severance"},
		{"The.Expanse.S01E01-02.1080p.BluRay.x264-GROUP", "Shows", "The Expanse"},
		{"Star Trek TNG S01E01 DVDRip", "Messy Shows", "Star Trek The Next Generation"},
	} {
		want := findItem(t, tc.library, "Series", tc.want)
		out := call(t, "show_resolve", map[string]any{"title": tc.name, "library": tc.library})
		cands := rows(t, out["candidates"], "candidates")
		if len(cands) == 0 {
			t.Errorf("%s resolved to nothing (parsed %q)", tc.name, out["parsed_title"])
			continue
		}
		if str(cands[0]["series_id"]) != want {
			t.Errorf("%s resolved to %v, want %s", tc.name, cands[0], tc.want)
		}
		if score, _ := cands[0]["score"].(float64); score < 0.9 {
			t.Errorf("%s matched %v at %v, too low to act on", tc.name, cands[0]["name"], score)
		}
	}

	// a run of episodes is read as a run
	out := call(t, "show_resolve", map[string]any{"title": "The.Expanse.S01E01-02.1080p.BluRay.x264-GROUP", "library": "Shows"})
	if num(t, out["parsed_episode"], "parsed_episode") != 1 || num(t, out["parsed_episode_end"], "parsed_episode_end") != 2 {
		t.Errorf("S01E01-02 parsed as %v-%v", out["parsed_episode"], out["parsed_episode_end"])
	}
}
