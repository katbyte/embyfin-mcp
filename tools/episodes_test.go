package tools

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The bulk episode read and the existence check, against the canned Emby in
// shows_test.go.

// showLibrary builds a show library: series named A, B, C... each holding
// seasons of episodes, which is enough shape to page over.
func showLibrary(series, seasons, episodes int) []*fakeSeries {
	out := make([]*fakeSeries, 0, series)
	for s := range series {
		name := string(rune('A' + s))
		show := &fakeSeries{id: "s" + name, name: name, year: 2000 + s, ids: map[string]string{"Tmdb": strconv.Itoa(100 + s)}}
		for season := 1; season <= seasons; season++ {
			for e := 1; e <= episodes; e++ {
				show.episodes = append(show.episodes, ep{
					season: season, number: e,
					name: fmt.Sprintf("%s S%02dE%02d", name, season, e),
					path: fmt.Sprintf("/media/shows/%s/Season %02d/S%02dE%02d.mkv", name, season, season, e),
				})
			}
		}
		out = append(out, show)
	}

	return out
}

// C4, guarding A3: the sweep pages the whole library, and the page
// boundaries neither drop a row nor repeat one - the failure that silently
// corrupts a reconcile, because a dropped episode reads as one to import and
// a repeated one as a duplicate to delete.
func TestLibraryEpisodesPages(t *testing.T) {
	t.Parallel()

	shows := showLibrary(4, 2, 3) // 24 episodes
	cs := session(t, tvServer(t, shows...), Options{})

	seen := []string{}
	cursor := ""
	for page := 0; ; page++ {
		args := map[string]any{"library": "Shows", "limit": 5}
		if cursor != "" {
			args["cursor"] = cursor
		}
		out := mustCall(t, cs, "library_episodes", args)
		if got := number(t, out["total"], "total"); got != 24 {
			t.Fatalf("total = %d, want 24", got)
		}
		if got := number(t, out["offset"], "offset"); got != page*5 {
			t.Errorf("page %d starts at %d", page, got)
		}
		for _, row := range objects(t, out["episodes"], "episodes") {
			seen = append(seen, fmt.Sprintf("%s S%02dE%02d", row["series"], number(t, row["season"], "season"), number(t, row["episode"], "episode")))
		}
		next := text(out["cursor"])
		if next == "" {
			break
		}
		cursor = next
		if page > 10 {
			t.Fatal("the cursor never ran out")
		}
	}

	if len(seen) != 24 {
		t.Fatalf("the sweep returned %d episodes, want 24: %v", len(seen), seen)
	}
	sorted := slices.Clone(seen)
	slices.Sort(sorted)
	if n := len(slices.Compact(sorted)); n != 24 {
		t.Errorf("the pages repeat rows: %d distinct of %d", n, len(seen))
	}
	if !slices.IsSorted(seen) {
		t.Errorf("the pages are not in one order, so a boundary can move between calls: %v", seen)
	}
	// a cursor from somewhere else is refused rather than read as an offset
	if msg := mustRefuse(t, cs, "library_episodes", map[string]any{"cursor": "not-a-cursor"}); !strings.Contains(msg, "cursor") {
		t.Errorf("a bad cursor said: %s", msg)
	}
}

// C8, guarding A3: the sweep costs one query per page and nothing per item.
// Without this the bulk path can quietly regress to a fetch per episode,
// which is the cost the whole tool exists to avoid.
func TestLibraryEpisodesCostsOneQueryPerPage(t *testing.T) {
	t.Parallel()

	f := tvServer(t, showLibrary(4, 2, 3)...)
	cs := session(t, f, Options{})

	before := len(f.requests("/Items"))
	out := mustCall(t, cs, "library_episodes", map[string]any{"library": "Shows", "limit": 1000})
	if n := len(objects(t, out["episodes"], "episodes")); n != 24 {
		t.Fatalf("one page returned %d episodes, want all 24", n)
	}
	if out["cursor"] != nil {
		t.Errorf("a finished sweep still points at a next page: %v", out["cursor"])
	}
	if n := len(f.requests("/Items")) - before; n != 1 {
		t.Errorf("24 episodes in one page cost %d item queries, want 1", n)
	}
	// and nothing was asked of the per-series route
	if n := len(f.requests("/Shows/sA/Episodes")); n != 0 {
		t.Errorf("the bulk read fell back to the per-series route %d times", n)
	}
}

// C6, guarding A5: every quality fact a "is my copy better than the
// library's" comparison needs is on the row, as a number rather than a
// string to parse back, and without a second read per item.
func TestLibraryEpisodesCarryQualityFacts(t *testing.T) {
	t.Parallel()

	f := tvServer(t, severance())
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "library_episodes", map[string]any{"library": "Shows"})
	rows := objects(t, out["episodes"], "episodes")
	if len(rows) != 2 {
		t.Fatalf("episodes = %v", rows)
	}
	row := rows[0]
	for _, field := range []string{"width", "height", "bitrate", "size", "runtime_s"} {
		if n := number(t, row[field], field); n <= 0 {
			t.Errorf("%s = %v, want a number a comparison can use", field, row[field])
		}
	}
	if row["video_codec"] != "h264" || row["container"] != "mkv" {
		t.Errorf("codec = %v, container = %v", row["video_codec"], row["container"])
	}
	if number(t, row["width"], "width") != 1920 || number(t, row["height"], "height") != 1080 {
		t.Errorf("resolution = %vx%v", row["width"], row["height"])
	}
	if row["path"] == "" || row["series"] != "Severance" || row["title"] != "Good News About Hell" {
		t.Errorf("row = %v", row)
	}

	// the same facts reach a single series' listing, which is where A5 was
	// found: series then episodes then a read per item
	show := mustCall(t, cs, "show_episodes", map[string]any{"series_id": "sev"})
	first := objects(t, show["episodes"], "episodes")[0]
	if number(t, first["width"], "width") != 1920 || first["video_codec"] != "h264" {
		t.Errorf("show_episodes row lacks the quality facts: %v", first)
	}

	// asked for without them, the rows are the bare listing
	lean := mustCall(t, cs, "library_episodes", map[string]any{"library": "Shows", "quality": false})
	if row := objects(t, lean["episodes"], "episodes")[0]; row["width"] != nil || row["container"] != nil {
		t.Errorf("quality=false still carried the facts: %v", row)
	}
}

// Episodes the server knows of but holds no file for are left out of a bulk
// read by default, because it answers "what is on disk".
func TestLibraryEpisodesSkipsRecordsWithNoFile(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = append(s.episodes, ep{season: 1, number: 3, name: "In Perpetuity", missing: true})
	cs := session(t, tvServer(t, s), Options{})

	out := mustCall(t, cs, "library_episodes", map[string]any{"library": "Shows"})
	if n := len(objects(t, out["episodes"], "episodes")); n != 2 {
		t.Errorf("a record with no file was exported: %v", out["episodes"])
	}

	out = mustCall(t, cs, "library_episodes", map[string]any{"library": "Shows", "with_file": false})
	rows := objects(t, out["episodes"], "episodes")
	if len(rows) != 3 {
		t.Fatalf("with_file=false returned %d rows, want 3", len(rows))
	}
	if !boolean(t, rows[2]["missing"], "missing") {
		t.Errorf("the record with no file is not flagged: %v", rows[2])
	}
}

// A series can be read on its own, and a season of it; a season without a
// series is refused, because a season number means nothing across a library.
func TestLibraryEpisodesScopes(t *testing.T) {
	t.Parallel()

	shows := showLibrary(3, 2, 2)
	cs := session(t, tvServer(t, shows...), Options{})

	out := mustCall(t, cs, "library_episodes", map[string]any{"series_id": "sB"})
	rows := objects(t, out["episodes"], "episodes")
	if len(rows) != 4 {
		t.Fatalf("one series returned %d episodes, want 4", len(rows))
	}
	for _, row := range rows {
		if row["series"] != "B" {
			t.Errorf("a row from another series: %v", row)
		}
	}

	out = mustCall(t, cs, "library_episodes", map[string]any{"series_id": "sB", "season": 2})
	for _, row := range objects(t, out["episodes"], "episodes") {
		if number(t, row["season"], "season") != 2 {
			t.Errorf("season 2 listing has %v", row)
		}
	}

	if msg := mustRefuse(t, cs, "library_episodes", map[string]any{"season": 2}); !strings.Contains(msg, "series_id") {
		t.Errorf("a season without a series said: %s", msg)
	}
	if msg := mustRefuse(t, cs, "library_episodes", map[string]any{"library": "Shows", "series_id": "sB"}); !strings.Contains(msg, "not both") {
		t.Errorf("a library and a series together said: %s", msg)
	}
}

// C5, guarding A4: a batch of season and episode numbers is answered without
// listing the series, and a file holding several episodes counts for each of
// them.
func TestShowEpisodesExist(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = []ep{
		{season: 1, number: 1, name: "Good News About Hell", path: "/m/s01e01.mkv"},
		// one file holding two episodes, as a double-length opener is named
		{season: 2, number: 1, number2: 2, name: "Hello, Ms. Cobel", path: "/m/s02e01e02.mkv"},
		{season: 2, number: 5, name: "Trojan's Horse", missing: true},
	}
	f := tvServer(t, s)
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "show_episodes_exist", map[string]any{
		"series_id": "sev",
		"episodes":  []map[string]any{{"season": 1, "episode": 1}, {"season": 1, "episode": 2}, {"season": 2, "episode": 2}, {"season": 2, "episode": 5}},
	})
	rows := objects(t, out["episodes"], "episodes")
	if len(rows) != 4 {
		t.Fatalf("asked about 4 episodes, answered %d", len(rows))
	}
	for i, want := range []bool{true, false, true, false} {
		if got := boolean(t, rows[i]["exists"], "exists"); got != want {
			t.Errorf("%v: exists = %v, want %v", rows[i], got, want)
		}
	}
	if number(t, out["absent"], "absent") != 2 {
		t.Errorf("absent = %v, want 2", out["absent"])
	}
	// the second half of the double file says which file covers it
	if rows[2]["covered_by"] != "S02E01E02" {
		t.Errorf("S02E02 is covered by %v, want the double file", rows[2]["covered_by"])
	}
	// a record with no file is known but does not exist
	if !boolean(t, rows[3]["known"], "known") || boolean(t, rows[3]["exists"], "exists") {
		t.Errorf("a record with no file = %v", rows[3])
	}
	// an episode nothing knows of is neither
	if boolean(t, rows[1]["known"], "known") || rows[1]["id"] != nil {
		t.Errorf("an unknown episode = %v", rows[1])
	}

	// the seasons asked about are the seasons read, not the whole series
	for _, r := range f.requests("/Shows/sev/Episodes") {
		if !strings.Contains(r.Query, "Season=") {
			t.Errorf("the whole series was pulled to answer two seasons: %s", r.Query)
		}
	}

	// and it can be asked by name
	byName := mustCall(t, cs, "show_episodes_exist", map[string]any{"series": "Severance", "episodes": []map[string]any{{"season": 1, "episode": 1}}})
	if text(byName["series_id"]) != "sev" {
		t.Errorf("by name = %v", byName)
	}
	if msg := mustRefuse(t, cs, "show_episodes_exist", map[string]any{"series_id": "sev", "episodes": []map[string]any{}}); !strings.Contains(msg, "at least one") {
		t.Errorf("an empty batch said: %s", msg)
	}
	if msg := mustRefuse(t, cs, "show_episodes_exist", map[string]any{"series_id": "sev", "episodes": []map[string]any{{"season": 1, "episode": 0}}}); !strings.Contains(msg, "episode must be") {
		t.Errorf("episode 0 said: %s", msg)
	}
}

// A2 again, across the sweep rather than one series: audit_missing_episodes
// can only see the gaps between files when the server keeps no record of a
// series' run, and has to say so - otherwise a library it reports nothing
// about reads as a library with nothing missing.
func TestAuditMissingEpisodesSaysHowMuchItKnows(t *testing.T) {
	t.Parallel()

	// an Emby 4.10: every episode it lists has a file behind it
	s := severance()
	s.episodes = []ep{
		{season: 1, number: 1, name: "one", path: "/m/s01e01.mkv"},
		{season: 1, number: 3, name: "three", path: "/m/s01e03.mkv"},
	}
	cs := session(t, tvServer(t, s), Options{})

	out := mustCall(t, cs, "audit_missing_episodes", map[string]any{"library": "Shows"})
	if boolean(t, out["runs_known"], "runs_known") {
		t.Error("runs_known is true where the server records no run at all")
	}
	if note := text(out["note"]); !strings.Contains(note, "not known to be complete") {
		t.Errorf("the note does not warn that absence is not completeness: %q", note)
	}
	// it still finds the gap it can see
	rows := objects(t, out["findings"], "findings")
	if len(rows) != 1 || !strings.Contains(text(rows[0]["detail"]), "S01E02") {
		t.Errorf("findings = %v, want the gap between the two files", rows)
	}

	// a server that does keep the run says so, and drops the warning
	s.episodes = append(s.episodes, ep{season: 1, number: 4, name: "four", missing: true})
	cs = session(t, tvServer(t, s), Options{})
	out = mustCall(t, cs, "audit_missing_episodes", map[string]any{"library": "Shows"})
	if !boolean(t, out["runs_known"], "runs_known") {
		t.Error("runs_known is false where the server listed an episode with no file")
	}
	if note := text(out["note"]); note != "" {
		t.Errorf("a known run still carries the warning: %q", note)
	}
}

// A record for an episode the library lacks is told apart by LocationType
// Virtual, not by an empty path: neither server's item carries an IsMissing
// field, and a virtual record that arrives with a path anyway must not count
// as a file the library holds. Counting one would drop the episode from what
// show_missing reports, which is the silent under-report - the library looks
// more complete than it is.
func TestAVirtualRecordIsNotAFile(t *testing.T) {
	t.Parallel()

	s := severance()
	// the awkward one: marked virtual, and carrying a path regardless
	s.episodes = append(s.episodes, ep{season: 1, number: 3, name: "In Perpetuity", path: "/media/shows/Severance/Season 01/S01E03.mkv", missing: true})
	cs := session(t, tvServer(t, s), Options{})

	// it is missing, from the server's own records
	out := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"})
	if out["source"] != sourceServer || !boolean(t, out["supported"], "supported") {
		t.Fatalf("supported = %v, source = %v", out["supported"], out["source"])
	}
	if got := codes(t, out["missing"], "missing"); !slices.Equal(got, []string{"S01E03"}) {
		t.Errorf("missing = %v, want S01E03: a virtual record with a path was counted as held", got)
	}

	// it is not a file the bulk read exports
	bulk := mustCall(t, cs, "library_episodes", map[string]any{"library": "Shows"})
	for _, row := range objects(t, bulk["episodes"], "episodes") {
		if number(t, row["episode"], "episode") == 3 {
			t.Errorf("a virtual record was exported as a file: %v", row)
		}
	}

	// and the library does not hold it
	exists := mustCall(t, cs, "show_episodes_exist", map[string]any{
		"series_id": "sev", "episodes": []map[string]any{{"season": 1, "episode": 3}},
	})
	row := objects(t, exists["episodes"], "episodes")[0]
	if boolean(t, row["exists"], "exists") || !boolean(t, row["known"], "known") {
		t.Errorf("a virtual record with a path = %v, want known but not held", row)
	}
}

// What the existence check costs. It reads a season at a time while that is
// cheaper than the series, and gives up and reads the series once enough
// seasons are asked about - the point of the tool being that "does S09E11
// exist" should not pull a 240-episode show. The threshold is a judgement,
// so it is pinned here rather than left to drift.
func TestShowEpisodesExistReadsOnlyWhatItNeeds(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = nil
	for season := 1; season <= 12; season++ {
		for e := 1; e <= 20; e++ {
			s.episodes = append(s.episodes, ep{
				season: season, number: e,
				name: fmt.Sprintf("S%02dE%02d", season, e),
				path: fmt.Sprintf("/m/s%02de%02d.mkv", season, e),
			})
		}
	}

	for _, tc := range []struct {
		seasons []int
		queries int
		scoped  bool
	}{
		{[]int{9}, 1, true},              // the question the tool exists for
		{[]int{1, 9}, 2, true},           // two seasons, two scoped reads
		{[]int{1, 2, 3}, 3, true},        // still cheaper than 240 episodes
		{[]int{1, 2, 3, 4, 5}, 1, false}, // past that, one read of the series
	} {
		f := tvServer(t, s)
		cs := session(t, f, Options{})
		pairs := make([]map[string]any, 0, len(tc.seasons))
		for _, season := range tc.seasons {
			pairs = append(pairs, map[string]any{"season": season, "episode": 11})
		}

		out := mustCall(t, cs, "show_episodes_exist", map[string]any{"series_id": "sev", "episodes": pairs})
		if n := len(objects(t, out["episodes"], "episodes")); n != len(tc.seasons) {
			t.Errorf("%v: answered %d of %d", tc.seasons, n, len(tc.seasons))
		}
		if number(t, out["absent"], "absent") != 0 {
			t.Errorf("%v: episode 11 is on disk in every season, absent = %v", tc.seasons, out["absent"])
		}

		reads := f.requests("/Shows/sev/Episodes")
		if len(reads) != tc.queries {
			t.Errorf("%v seasons cost %d reads, want %d", tc.seasons, len(reads), tc.queries)
		}
		for _, r := range reads {
			if scoped := strings.Contains(r.Query, "Season="); scoped != tc.scoped {
				t.Errorf("%v: read scoped=%v, want %v (%s)", tc.seasons, scoped, tc.scoped, r.Query)
			}
		}
	}
}

// A library holding the same show twice is ordinary - a tidy copy and a
// messy one - and the fixtures do. A name that matches both is refused with
// both, rather than one being picked and the answer quietly describing the
// wrong files; a library narrows it when the caller has no id.
func TestShowEpisodesExistRefusesAnAmbiguousName(t *testing.T) {
	t.Parallel()

	tidy := severance()
	messy := severance()
	messy.id, messy.year = "sev-messy", 2022
	messy.episodes = []ep{{season: 1, number: 1, name: "Good News About Hell", path: "/media/messy-shows/Severance/S01E01.mkv"}}
	for i := range tidy.episodes {
		tidy.episodes[i].path = "/media/shows/Severance/" + tidy.episodes[i].name + ".mkv"
	}
	cs := session(t, tvServer(t, tidy, messy), Options{})

	ask := map[string]any{"series": "Severance", "episodes": []map[string]any{{"season": 1, "episode": 2}}}
	msg := mustRefuse(t, cs, "show_episodes_exist", ask)
	for _, want := range []string{"matches 2 series", "sev", "sev-messy", "2022", "give series_id", "library"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not carry %q, so a caller cannot tell them apart: %s", want, msg)
		}
	}

	// an id is unambiguous, and so is a name once a library is named
	byID := mustCall(t, cs, "show_episodes_exist", map[string]any{"series_id": "sev-messy", "episodes": []map[string]any{{"season": 1, "episode": 2}}})
	if boolean(t, objects(t, byID["episodes"], "episodes")[0]["exists"], "exists") {
		t.Error("the messy copy holds only S01E01, so S01E02 is not there")
	}
}
