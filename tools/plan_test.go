package tools

import (
	"strings"
	"testing"
)

// C18: the call to make before writing. A bulk import that does not read the
// destinations can overwrite one series' episodes with files from another of
// the same name, and nothing afterwards can show it: an overwritten path
// keeps the item's id and its date_created.
func TestPlanCheck(t *testing.T) {
	t.Parallel()

	old := &fakeSeries{
		id: "ex94", name: "Example High", year: 1994,
		path: "/media/shows/Example High (1994)",
		episodes: []ep{
			{season: 3, number: 8, name: "Episode 60", path: "/media/shows/Example High (1994)/Season 03/Example High - 03x08 - Episode 60.mkv", minutes: 45},
		},
	}
	other := &fakeSeries{
		id: "sev", name: "Severance", year: 2022, path: "/media/shows/Severance",
		episodes: []ep{{season: 1, number: 1, name: "One", path: "/media/shows/Severance/S01E01.mkv"}},
	}
	cs := session(t, tvServer(t, old, other), Options{})

	out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{
		// straight onto an existing file, from a different show entirely
		{
			"path": "/media/shows/Example High (1994)/Season 03/Example High - 03x08 - Episode 60.mkv",
			"size": 7_000_000_000, "series": "Example High 2022", "season": 3, "episode": 8,
		},
		// a free path inside a series folder
		{"path": "/media/shows/Example High (1994)/Season 03/Example High - 03x09 - Episode 61.mkv", "series": "Example High"},
		// two entries that would land on one file
		{"path": "/media/shows/Severance/S01E07.mkv", "series": "Severance", "season": 1, "episode": 7},
		{"path": "/media/shows/Severance/S01E07.mkv", "series": "Severance", "season": 1, "episode": 7},
		// nowhere the server knows
		{"path": "/staging/incoming/Some Show S01E01.mkv"},
	}})

	rows := objects(t, out["entries"], "entries")
	if len(rows) != 5 {
		t.Fatalf("entries = %v", rows)
	}

	// the destination that already holds a file says so, with what is there
	overwrite := rows[0]
	if !boolean(t, overwrite["exists"], "exists") {
		t.Fatalf("an occupied destination read as free: %v", overwrite)
	}
	current := object(t, overwrite["current"], "current")
	if text(current["item_id"]) != "ex94-3-8" || number(t, current["runtime_s"], "runtime_s") != 45*60 {
		t.Errorf("current = %v", current)
	}
	// the incoming file is bigger, and the ratio says so rather than leaving
	// the caller to divide
	if ratio, ok := current["size_ratio"].(float64); !ok || ratio <= 1 {
		t.Errorf("size_ratio = %v", current["size_ratio"])
	}
	if number(t, out["existing"], "existing") != 1 {
		t.Errorf("existing = %v, want 1", out["existing"])
	}

	// and the folder it would join is the 1994 series, not the 2022 one the
	// caller named: that gap is the whole problem
	join := object(t, overwrite["would_join"], "would_join")
	if text(join["series_id"]) != "ex94" || number(t, join["series_year"], "series_year") != 1994 {
		t.Errorf("would_join = %v", join)
	}
	if score := decimal(t, join["claim_similarity"], "claim_similarity"); score >= seriesConfident {
		t.Errorf("claiming the 2022 series scored %v against the 1994 folder", score)
	}

	// a free path inside a known series: no file, and the series is named
	free := rows[1]
	if boolean(t, free["exists"], "exists") || free["current"] != nil {
		t.Errorf("a free path = %v", free)
	}
	if text(object(t, free["would_join"], "would_join")["series_id"]) != "ex94" {
		t.Errorf("a free path did not name its series: %v", free)
	}

	// two entries onto one path name each other
	for _, i := range []int{2, 3} {
		if dups := texts(rows[i]["duplicate_of"]); len(dups) != 1 || !strings.Contains(dups[0], "S01E07") {
			t.Errorf("entry %d does not name the entry it collides with: %v", i, rows[i])
		}
	}
	if number(t, out["duplicates"], "duplicates") != 2 {
		t.Errorf("duplicates = %v, want 2", out["duplicates"])
	}

	// a path under no series folder says so rather than reading as free
	outside := rows[4]
	if outside["would_join"] != nil || !strings.Contains(text(outside["note"]), "no series folder") {
		t.Errorf("a path outside the library = %v", outside)
	}
	if number(t, out["unplaced"], "unplaced") != 1 {
		t.Errorf("unplaced = %v, want 1", out["unplaced"])
	}

	// and the batch is bounded
	big := make([]map[string]any, 501)
	for i := range big {
		big[i] = map[string]any{"path": "/media/shows/Severance/x.mkv"}
	}
	if msg := mustRefuse(t, cs, "plan_check", map[string]any{"entries": big}); !strings.Contains(msg, "500") {
		t.Errorf("an oversized batch: %s", msg)
	}
	if msg := mustRefuse(t, cs, "plan_check", map[string]any{"entries": []map[string]any{}}); !strings.Contains(msg, "at least one") {
		t.Errorf("an empty batch: %s", msg)
	}
}

// A server that finds two files of one episode in a folder merges them into
// one item, and only the first is the item's own path. The second was read as
// free - a write there would have replaced it silently - and what was said to
// be at a path was the tallest version, not the file there.
func TestPlanCheckKnowsEveryVersionOfAnEpisode(t *testing.T) {
	t.Parallel()

	for _, jellyfin := range []bool{false, true} {
		s := severance()
		s.episodes[0].alt = "/media/shows/Severance/Season 01/S01E01 - 720p.mkv"
		cs := session(t, tvServerFor(t, jellyfin, s), Options{})

		out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{
			{"path": "/media/shows/Severance/Season 01/S01E01 - 720p.mkv", "size": 700 << 20},
		}})
		row := objects(t, out["entries"], "entries")[0]
		if !boolean(t, row["checked"], "checked") || !boolean(t, row["exists"], "exists") {
			t.Fatalf("jellyfin %v: the second version's path read as free: %v", jellyfin, row)
		}
		// the 720p file at that path, not the 1080p one beside it
		current := object(t, row["current"], "current")
		if number(t, current["height"], "height") != 720 || number(t, current["size"], "size") != 350<<20 {
			t.Errorf("jellyfin %v: current describes another version: %v", jellyfin, current)
		}
		if ratio := decimal(t, current["size_ratio"], "size_ratio"); ratio != 2 {
			t.Errorf("jellyfin %v: size_ratio = %v, want 2 against the file at the path", jellyfin, ratio)
		}
	}
}

// Jellyfin cannot be asked what is at a path, so a path under no series folder
// - a film - read exists: false on every one, with nothing to say it had not
// been checked. It is asked by the title the path names instead, and where
// that cannot settle it the row says so rather than answering false.
func TestPlanCheckOnJellyfinSaysWhatItCouldNotCheck(t *testing.T) {
	t.Parallel()

	film := &fakeSeries{id: "alien", name: "Alien", year: 1979, film: true, path: "/media/films/Alien (1979)/Alien (1979).mkv"}
	for _, jellyfin := range []bool{false, true} {
		cs := session(t, tvServerFor(t, jellyfin, severance(), film), Options{})

		out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{
			{"path": "/media/films/Alien (1979)/Alien (1979).mkv"},
			{"path": "/media/films/Aliens (1986)/Aliens (1986).mkv"},
			{"path": "/staging/incoming/Some Film (2020).mkv"},
		}})
		rows := objects(t, out["entries"], "entries")

		// the film is found, on both servers, and it has no season
		held := rows[0]
		if !boolean(t, held["checked"], "checked") || !boolean(t, held["exists"], "exists") {
			t.Errorf("jellyfin %v: a film's own path = %v", jellyfin, held)
		} else if current := object(t, held["current"], "current"); text(current["item_id"]) != "alien" || current["season"] != nil {
			t.Errorf("jellyfin %v: current = %v", jellyfin, current)
		}

		// outside every library folder, nothing can be there on either
		outside := rows[2]
		if !boolean(t, outside["checked"], "checked") || boolean(t, outside["exists"], "exists") {
			t.Errorf("jellyfin %v: a path outside the library = %v", jellyfin, outside)
		}

		// inside the library, where only a path lookup could settle it
		free := rows[1]
		if !jellyfin {
			// Emby was asked by the path itself, and it is free
			if !boolean(t, free["checked"], "checked") || boolean(t, free["exists"], "exists") || number(t, out["unchecked"], "unchecked") != 0 {
				t.Errorf("Emby: a free path = %v, unchecked %v", free, out["unchecked"])
			}

			continue
		}
		if boolean(t, free["checked"], "checked") || free["exists"] != nil {
			t.Errorf("Jellyfin: a path it could not look up answered as though it had: %v", free)
		}
		if note := text(free["note"]); !strings.Contains(note, "not known") || !strings.Contains(note, "Jellyfin") {
			t.Errorf("Jellyfin: the note does not say it could not tell: %q", note)
		}
		if number(t, out["unchecked"], "unchecked") != 1 {
			t.Errorf("Jellyfin: unchecked = %v, want 1", out["unchecked"])
		}
	}
}

// The claim is scored the way a name is resolved: its title and its year
// against the series' own. Scoring bare titles called "Severance (2022)" a
// different show from Severance under its own folder, and put a claim of
// plain "Doctor Who" under a series the library names "Doctor Who (1963)"
// no higher than a guess.
func TestPlanCheckScoresAClaimByTitleAndYear(t *testing.T) {
	t.Parallel()

	sev := &fakeSeries{id: "sev", name: "Severance", year: 2022, path: "/media/shows/Severance (2022)"}
	who := &fakeSeries{id: "who63", name: "Doctor Who (1963)", year: 1963, path: "/media/shows/Doctor Who (1963)"}
	cs := session(t, tvServer(t, sev, who), Options{})

	out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{
		{"path": "/media/shows/Severance (2022)/Season 01/S01E01.mkv", "series": "Severance (2022)"},
		{"path": "/media/shows/Doctor Who (1963)/Season 01/S01E01.mkv", "series": "Doctor Who"},
		{"path": "/media/shows/Doctor Who (1963)/Season 01/S01E02.mkv", "series": "Doctor Who (2005)"},
	}})
	rows := objects(t, out["entries"], "entries")
	claim := func(i int) float64 {
		return decimal(t, object(t, rows[i]["would_join"], "would_join")["claim_similarity"], "claim_similarity")
	}
	if got := claim(0); got < 0.95 {
		t.Errorf("Severance (2022) under Severance's own folder scored %v", got)
	}
	if got := claim(1); got < 0.95 {
		t.Errorf("Doctor Who under Doctor Who (1963) scored %v", got)
	}
	if got := claim(2); got >= seriesConfident {
		t.Errorf("Doctor Who (2005) under the 1963 series scored %v: the year is what says it is the wrong show", got)
	}
}

// A special is season 0, and says so: an omitted 0 left the one season a
// caller most needs to tell apart with no number at all.
func TestPlanCheckSaysASpecialIsSeasonZero(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = append(s.episodes, ep{season: 0, number: 1, name: "Lumon Orientation", path: "/media/shows/Severance/Specials/S00E01.mkv"})
	cs := session(t, tvServer(t, s), Options{})

	out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{{"path": "/media/shows/Severance/Specials/S00E01.mkv"}}})
	current := object(t, objects(t, out["entries"], "entries")[0]["current"], "current")
	if season, ok := current["season"]; !ok || number(t, season, "season") != 0 {
		t.Errorf("a special's current = %v, want season 0", current)
	}
}

// A zero is an answer, and the most important one: an empty incoming file
// over a whole one is a size_ratio of 0, and a claim of another show
// altogether a claim_similarity of 0. Both were left out of the answer as if
// no size or series had been given.
func TestPlanCheckGivesAZero(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes[0].alt = "/media/shows/Severance/Season 01/S01E01 - 720p.mkv"
	cs := session(t, tvServer(t, s), Options{})

	out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{
		{"path": "/media/shows/Severance/Season 01/S01E01 - 720p.mkv", "size": 0, "series": "Zzyzx Qwerty"},
		{"path": "/media/shows/Severance/Season 01/S01E01 - 720p.mkv"},
	}})
	rows := objects(t, out["entries"], "entries")
	current := object(t, rows[0]["current"], "current")
	if ratio, ok := current["size_ratio"]; !ok || decimal(t, ratio, "size_ratio") != 0 {
		t.Errorf("an empty file over a whole one: current = %v, want size_ratio 0", current)
	}
	join := object(t, rows[0]["would_join"], "would_join")
	if score, ok := join["claim_similarity"]; !ok || decimal(t, score, "claim_similarity") != 0 {
		t.Errorf("another show claimed: would_join = %v, want claim_similarity 0", join)
	}
	// and with no size or series given there is nothing to compare, and
	// nothing is said
	if _, ok := object(t, rows[1]["current"], "current")["size_ratio"]; ok {
		t.Errorf("no size given, yet size_ratio: %v", rows[1]["current"])
	}
	if _, ok := object(t, rows[1]["would_join"], "would_join")["claim_similarity"]; ok {
		t.Errorf("no series claimed, yet claim_similarity: %v", rows[1]["would_join"])
	}
}
