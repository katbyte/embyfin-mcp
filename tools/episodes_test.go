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
	const limit = 5
	for offset, page := 0, 0; ; offset, page = offset+limit, page+1 {
		out := mustCall(t, cs, "library_episodes", map[string]any{"library": "Shows", "limit": limit, "offset": offset})
		total := number(t, out["total"], "total")
		if total != 24 {
			t.Fatalf("total = %d, want 24", total)
		}
		if got := number(t, out["offset"], "offset"); got != offset {
			t.Errorf("page %d starts at %d, want %d", page, got, offset)
		}
		for _, row := range objects(t, out["episodes"], "episodes") {
			seen = append(seen, fmt.Sprintf("%s S%02dE%02d", row["series"], number(t, row["season"], "season"), number(t, row["episode"], "episode")))
		}
		// the next page is offset + limit, until that is past the total
		if offset+limit >= total {
			break
		}
		if page > 10 {
			t.Fatal("the pages never ran out")
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
	// a page past the end is empty, and still says how many there are
	out := mustCall(t, cs, "library_episodes", map[string]any{"library": "Shows", "limit": limit, "offset": 30})
	if rows := objects(t, out["episodes"], "episodes"); len(rows) != 0 || number(t, out["total"], "total") != 24 || number(t, out["offset"], "offset") != 30 {
		t.Errorf("a page past the end = %v", out)
	}
	if _, ok := out["cursor"]; ok {
		t.Errorf("a page carries a cursor: %v", out["cursor"])
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
	if number(t, out["total"], "total") != 24 || number(t, out["offset"], "offset") != 0 {
		t.Errorf("one page of everything = total %v offset %v", out["total"], out["offset"])
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
	// the frame rate comes off the stream like the rest: it is the fact that
	// says whether a 2160p row is a remaster or something a machine made
	if fps := decimal(t, row["frame_rate"], "frame_rate"); fps != 23.976 {
		t.Errorf("frame_rate = %v, want 23.976", fps)
	}
	// bt709 is a statement that the file is SDR, and it is said rather than
	// left to an absent field a caller would have to read as SDR anyway
	if row["hdr"] != "sdr" {
		t.Errorf("hdr = %v on a bt709 file, want sdr", row["hdr"])
	}
	// audio is fields, not a sentence to parse back: the codec is the codec
	// whether or not the track carries a language, and the bitrate is there
	// at all, which is what says whether a newer codec is the better track
	tracks := objects(t, row["audio"], "audio")
	if len(tracks) != 1 {
		t.Fatalf("audio = %v", row["audio"])
	}
	if tracks[0]["codec"] != "aac" || tracks[0]["language"] != "eng" {
		t.Errorf("audio track = %v", tracks[0])
	}
	if number(t, tracks[0]["channels"], "channels") != 6 || number(t, tracks[0]["bitrate"], "bitrate") != 448000 {
		t.Errorf("audio track lacks channels or bitrate: %v", tracks[0])
	}
	if number(t, row["width"], "width") != 1920 || number(t, row["height"], "height") != 1080 {
		t.Errorf("resolution = %vx%v", row["width"], row["height"])
	}
	if row["path"] == "" || row["series"] != "Severance" || row["title"] != "Good News About Hell" {
		t.Errorf("row = %v", row)
	}

	// the same facts reach a single series' listing, which is where A5 was
	// found: series then episodes then a read per item
	show := mustCall(t, cs, "library_episodes", map[string]any{"series": "Severance"})
	first := objects(t, show["episodes"], "episodes")[0]
	if number(t, first["width"], "width") != 1920 || first["video_codec"] != "h264" {
		t.Errorf("a show's episode row lacks the quality facts: %v", first)
	}

	// asked for without them, the rows are the bare listing: no facts, and
	// no path either - the longest field on the row, and one a reconcile
	// working from season and episode numbers never reads. A sweep of a
	// library of hundreds of thousands of episodes is tens of megabytes of
	// paths nobody asked for.
	lean := mustCall(t, cs, "library_episodes", map[string]any{"library": "Shows", "quality": false})
	leanRow := objects(t, lean["episodes"], "episodes")[0]
	if leanRow["width"] != nil || leanRow["container"] != nil || leanRow["path"] != nil || leanRow["hdr"] != nil {
		t.Errorf("quality=false still carried the facts or the path: %v", leanRow)
	}
	// what is left still names the episode
	if leanRow["series"] != "Severance" || number(t, leanRow["season"], "season") != 1 || leanRow["id"] == nil {
		t.Errorf("quality=false dropped what names the episode: %v", leanRow)
	}
}

// C6 again, on the batch: an exists check is asked "do you have it" to decide
// keep-or-trash, and immediately after "is mine better" to decide
// trash-or-upgrade. Answering only the first is a second read per series,
// which for a folder spanning hundreds of shows is hundreds of round trips
// for facts the first read already had in its hand.
func TestShowEpisodesExistCarriesQualityOnHits(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = []ep{
		{season: 1, number: 1, name: "Good News About Hell", path: "/m/s01e01.mkv"},
		{season: 1, number: 3, name: "In Perpetuity", missing: true},
	}
	f := tvServer(t, s)
	cs := session(t, f, Options{})

	batch := []map[string]any{{"season": 1, "episode": 1}, {"season": 1, "episode": 2}, {"season": 1, "episode": 3}}
	out := mustCall(t, cs, "show_episodes_exist", map[string]any{"series_id": "sev", "episodes": batch, "quality": true})
	rows := objects(t, out["episodes"], "episodes")

	hit := rows[0]
	for _, field := range []string{"width", "height", "bitrate", "size", "runtime_s"} {
		if n := number(t, hit[field], field); n <= 0 {
			t.Errorf("a hit lacks %s: %v", field, hit)
		}
	}
	if hit["video_codec"] != "h264" || hit["container"] != "mkv" {
		t.Errorf("a hit lacks the codec or container: %v", hit)
	}

	// a miss has nothing to say about a file that is not there, and a batch
	// is mostly misses: the facts stay off both the unknown episode and the
	// record with no file
	for _, row := range []map[string]any{rows[1], rows[2]} {
		if row["width"] != nil || row["size"] != nil || row["container"] != nil || row["hdr"] != nil {
			t.Errorf("a miss carried quality facts: %v", row)
		}
	}

	// and they are off by default, so the answer stays small for a caller
	// that only asked whether the library holds the episode
	plain := mustCall(t, cs, "show_episodes_exist", map[string]any{"series_id": "sev", "episodes": batch})
	if row := objects(t, plain["episodes"], "episodes")[0]; row["width"] != nil || row["size"] != nil || row["hdr"] != nil {
		t.Errorf("quality was not asked for and came anyway: %v", row)
	}

	// the media sources are pulled only for the call that wanted them: they
	// are the expensive half of an episode read
	asked := f.requests("/Shows/sev/Episodes")
	if len(asked) != 2 {
		t.Fatalf("episode reads = %d, want one per call", len(asked))
	}
	if !strings.Contains(asked[0].Query, "MediaSources") {
		t.Errorf("quality=true did not ask for the media sources: %s", asked[0].Query)
	}
	if strings.Contains(asked[1].Query, "MediaSources") {
		t.Errorf("the plain call asked for the media sources anyway: %s", asked[1].Query)
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

	// a file holding two episodes holds both: S01E01E02 then E03 is no gap
	s.episodes[0].number2 = 2
	cs = session(t, tvServer(t, s), Options{})
	out = mustCall(t, cs, "audit_missing_episodes", map[string]any{"library": "Shows"})
	if rows := objects(t, out["findings"], "findings"); len(rows) != 0 {
		t.Errorf("findings = %v, want none: the double-episode file holds E02", rows)
	}
	s.episodes[0].number2 = 0

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
	s.episodes = append(s.episodes, ep{season: 1, number: 3, name: "In Perpetuity", path: "/media/shows/Severance/Season 01/S01E03.mkv", missing: true, premiere: "2022-02-25T00:00:00.0000000Z"})
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

// C7: a reconcile asks after one series per show it found in the folder, and
// a folder of thousands of releases spans hundreds of shows. One call per
// show is hundreds of round trips, so the batch takes series as readily as
// episodes - and the answer for one series cannot be allowed to depend on
// the other thirty-four resolving, or a single ambiguous name costs the
// whole page.
func TestShowEpisodesExistAnswersSeveralSeries(t *testing.T) {
	t.Parallel()

	shows := showLibrary(3, 1, 2) // A, B, C, one season, two episodes each
	cs := session(t, tvServer(t, shows...), Options{})

	out := mustCall(t, cs, "show_episodes_exist", map[string]any{
		"queries": []map[string]any{
			{"series_id": "sA", "episodes": []map[string]any{{"season": 1, "episode": 1}, {"season": 1, "episode": 9}}},
			{"series": "B", "episodes": []map[string]any{{"season": 1, "episode": 2}}},
			{"series": "no such show", "episodes": []map[string]any{{"season": 1, "episode": 1}}},
			{"series_id": "sC", "episodes": []map[string]any{{"season": 1, "episode": 1}}},
		},
	})

	groups := objects(t, out["results"], "results")
	if len(groups) != 4 {
		t.Fatalf("asked after 4 series, answered %d: %v", len(groups), groups)
	}
	// the groups come back in the order asked, which is what lets a caller
	// line them up with the releases it asked about
	for i, want := range []string{"A", "B", "no such show", "C"} {
		if got := text(groups[i]["series"]); got != want {
			t.Errorf("group %d is for %q, want %q", i, got, want)
		}
	}

	// the series that resolved are answered
	first := objects(t, groups[0]["episodes"], "episodes")
	if !boolean(t, first[0]["exists"], "exists") || boolean(t, first[1]["exists"], "exists") {
		t.Errorf("A: S01E01 and S01E09 = %v", first)
	}
	if !boolean(t, objects(t, groups[1]["episodes"], "episodes")[0]["exists"], "exists") {
		t.Errorf("B: S01E02 = %v", groups[1])
	}
	if !boolean(t, objects(t, groups[3]["episodes"], "episodes")[0]["exists"], "exists") {
		t.Errorf("C answered nothing after the failure above it: %v", groups[3])
	}

	// the one that did not resolve says so on its own group, and says it
	// rather than reporting the episodes absent - "we hold none of these" and
	// "we could not look" send a reconcile in opposite directions
	bad := groups[2]
	if text(bad["error"]) == "" {
		t.Errorf("an unresolvable series carried no error: %v", bad)
	}
	if rows := objects(t, bad["episodes"], "episodes"); len(rows) != 0 {
		t.Errorf("an unresolvable series answered for its episodes anyway: %v", rows)
	}
	if number(t, bad["absent"], "absent") != 0 {
		t.Errorf("an unresolvable series counted absences: %v", bad)
	}

	// absent totals the batch: one episode asked after that nothing holds
	if got := number(t, out["absent"], "absent"); got != 1 {
		t.Errorf("absent = %d across the batch, want 1", got)
	}

	// a batch answers in results, not in the single-series fields
	if out["episodes"] != nil || text(out["series"]) != "" {
		t.Errorf("a batch answered in the single-series shape too: %v", out)
	}
}

// The two shapes are alternatives, not a mixture: silently ignoring half of
// what was asked is worse than refusing it.
func TestShowEpisodesExistRefusesBothShapesAtOnce(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})

	msg := mustRefuse(t, cs, "show_episodes_exist", map[string]any{
		"series_id": "sev",
		"episodes":  []map[string]any{{"season": 1, "episode": 1}},
		"queries":   []map[string]any{{"series_id": "sev", "episodes": []map[string]any{{"season": 1, "episode": 1}}}},
	})
	if !strings.Contains(msg, "not both") {
		t.Errorf("mixing the shapes said: %s", msg)
	}

	// and a batch bigger than it will answer for names the limit, rather
	// than timing out somewhere in the middle of it
	big := make([]map[string]any, 0, 51)
	for range 51 {
		big = append(big, map[string]any{"series_id": "sev", "episodes": []map[string]any{{"season": 1, "episode": 1}}})
	}
	if msg := mustRefuse(t, cs, "show_episodes_exist", map[string]any{"queries": big}); !strings.Contains(msg, "50") {
		t.Errorf("an oversized batch said: %s", msg)
	}
}

// The name path and show_resolve were asked the same question and disagreed:
// resolve matched "24 Hours in A and E" to "24 Hours in A&E" at 1.0 while the
// name path matched it to hundreds of series and committed to none. Scene
// naming strips apostrophes, colons and ampersands, so a name path that needs
// them punctuated the library's way fails on most real input - and every one
// of these is a name that failed in the field.
func TestSeriesByNameFoldsPunctuationTheWayResolveDoes(t *testing.T) {
	t.Parallel()

	shows := []*fakeSeries{
		{id: "ae", name: "24 Hours in A&E", year: 2011},
		{id: "gath", name: "A Gatherer's Adventure in Isekai", year: 2024},
		{id: "swx", name: "AMERICA'S SWEETHEARTS: Dallas Cowboys Cheerleaders", year: 2024},
		{id: "davies", name: "Alan Davies: As Yet Untitled", year: 2015},
	}
	for _, s := range shows {
		s.episodes = []ep{{season: 1, number: 1, name: "One", path: "/m/" + s.id + "/s01e01.mkv"}}
	}
	cs := session(t, tvServer(t, shows...), Options{})

	for _, tc := range []struct{ ask, want string }{
		{"24 Hours in A and E", "ae"},
		{"A Gatherers Adventure in Isekai", "gath"},
		{"AMERICAS SWEETHEARTS Dallas Cowboys Cheerleaders", "swx"},
		{"Alan Davies As Yet Untitled", "davies"},
	} {
		out := mustCall(t, cs, "show_episodes_exist", map[string]any{
			"series": tc.ask, "episodes": []map[string]any{{"season": 1, "episode": 1}},
		})
		if got := text(out["series_id"]); got != tc.want {
			t.Errorf("%q resolved to %q, want %s", tc.ask, got, tc.want)
		}
	}
}

// A refusal is read by a caller working through a batch of shows, and it is
// competing for the same context the answers need. A loose name can match
// most of a library; spelling all of those out made the error dearer than the
// answer it replaced, and a batch of them worse than no batching at all.
func TestAnAmbiguousNameRefusesBriefly(t *testing.T) {
	t.Parallel()

	shows := make([]*fakeSeries, 0, 40)
	for i := range 40 {
		shows = append(shows, &fakeSeries{
			id: fmt.Sprintf("dup%02d", i), name: "Repeat", year: 1990 + i,
			episodes: []ep{{season: 1, number: 1, name: "One", path: fmt.Sprintf("/media/a/very/long/path/that/costs/bytes/Repeat (%d)/S01E01.mkv", 1990+i)}},
		})
	}
	cs := session(t, tvServer(t, shows...), Options{})

	msg := mustRefuse(t, cs, "show_episodes_exist", map[string]any{
		"series": "Repeat", "episodes": []map[string]any{{"season": 1, "episode": 1}},
	})

	// it still says how many there were, and still hands over enough to
	// choose between: the count is the fact, the long tail is not
	for _, want := range []string{"matches 40 series", "showing 5", "35 more not listed", "give series_id"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not carry %q: %s", want, msg)
		}
	}
	if n := strings.Count(msg, " id dup"); n != 5 {
		t.Errorf("the refusal lists %d series, want 5: %s", n, msg)
	}
	if len(msg) > 800 {
		t.Errorf("the refusal is %d bytes; 40 matches must not cost more than the answer would", len(msg))
	}
}

// C8: the facts are not one thing. A reconcile comparing resolution and
// bitrate across thousands of episodes pays, on every row, for a subtitle
// list thirty languages long and a path it addresses nothing by - which is
// most of the answer, and the answer is what runs out first.
func TestEpisodeFactsCanBeNarrowed(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = []ep{{season: 1, number: 1, name: "Good News About Hell", path: "/m/s01e01.mkv"}}
	f := tvServer(t, s)
	cs := session(t, f, Options{})

	want := []string{"width", "height", "video_codec", "bitrate", "size", "audio"}
	for _, tc := range []struct {
		tool string
		args map[string]any
		rows string
	}{
		{"show_episodes_exist", map[string]any{"series_id": "sev", "episodes": []map[string]any{{"season": 1, "episode": 1}}, "fields": want}, "episodes"},
		{"library_episodes", map[string]any{"library": "Shows", "fields": want}, "episodes"},
	} {
		out := mustCall(t, cs, tc.tool, tc.args)
		row := objects(t, out[tc.rows], tc.rows)[0]

		for _, field := range want {
			if row[field] == nil {
				t.Errorf("%s: asked for %s and did not get it: %v", tc.tool, field, row)
			}
		}
		// what was not asked for is not there - including the two that cost
		// the most
		for _, gone := range []string{"path", "subtitles", "container", "runtime_s"} {
			if row[gone] != nil {
				t.Errorf("%s: %s was not asked for: %v", tc.tool, gone, row)
			}
		}
		// what names the episode is never optional: a row nobody can line up
		// against a release is not a smaller answer, it is no answer
		if row["id"] == nil || number(t, row["season"], "season") != 1 || number(t, row["episode"], "episode") != 1 {
			t.Errorf("%s: narrowing dropped what identifies the row: %v", tc.tool, row)
		}
	}

	// asking for facts is asking for the facts: fields alone turns them on,
	// so a caller does not have to pass quality as well and wonder which wins
	only := mustCall(t, cs, "show_episodes_exist", map[string]any{
		"series_id": "sev", "episodes": []map[string]any{{"season": 1, "episode": 1}}, "fields": []string{"height"},
	})
	if row := objects(t, only["episodes"], "episodes")[0]; number(t, row["height"], "height") != 1080 {
		t.Errorf("fields alone did not turn the facts on: %v", row)
	}

	// a field nobody has is refused, rather than quietly answering without
	// it: a typo that drops the fact a decision turns on still looks like an
	// answer
	msg := mustRefuse(t, cs, "library_episodes", map[string]any{"library": "Shows", "fields": []string{"width", "heigth"}})
	if !strings.Contains(msg, "heigth") || !strings.Contains(msg, "video_codec") {
		t.Errorf("a misspelled field said: %s", msg)
	}

	// and asked for nothing off the file, the file is not read: the media
	// sources are the expensive half of the query
	f.reset()
	mustCall(t, cs, "library_episodes", map[string]any{"library": "Shows", "fields": []string{"runtime_s"}})
	for _, r := range f.requests("/Items") {
		if strings.Contains(r.Query, "MediaSources") {
			t.Errorf("nothing off the file was asked for, but the media sources were read: %s", r.Query)
		}
	}
}

// C9: a show held under two library entries is split by EPISODE, not
// duplicated - one entry with season 1 and another with the rest is a shape a
// folder rename leaves behind. Asked about the wrong half, an exists
// check answers known:false for an episode the library is holding, which is
// well-formed, confident and wrong. It has to say there is somewhere else to
// look.
func TestShowEpisodesExistWarnsWhenAShowIsSplit(t *testing.T) {
	t.Parallel()

	first := &fakeSeries{
		id: "split-a", name: "Some Procedural", year: 2000,
		ids:      map[string]string{"Tmdb": "90001"},
		episodes: []ep{{season: 1, number: 1, name: "Pilot", path: "/media/s/Some Procedural (2000)/S01E01.mkv"}},
	}
	rest := &fakeSeries{
		id: "split-b", name: "Some Procedural", year: 2000,
		ids:      map[string]string{"Tmdb": "90001"},
		episodes: []ep{{season: 4, number: 12, name: "Twelve", path: "/media/s/Some Procedural - 2000/S04E12.mkv"}},
	}
	cs := session(t, tvServer(t, first, rest, severance()), Options{})

	// the half that does not hold S04E12 still has to point at the half that does
	out := mustCall(t, cs, "show_episodes_exist", map[string]any{
		"series_id": "split-a",
		"episodes":  []map[string]any{{"season": 1, "episode": 1}, {"season": 4, "episode": 12}},
	})
	rows := objects(t, out["episodes"], "episodes")
	if !boolean(t, rows[0]["exists"], "exists") || boolean(t, rows[1]["exists"], "exists") {
		t.Fatalf("this entry holds S01E01 and not S04E12: %v", rows)
	}
	warning := text(out["warning"])
	if warning == "" {
		t.Fatal("an absence against a split show came back with nothing to say it might be elsewhere")
	}
	for _, want := range []string{"split-b", "not proof", "audit_duplicates"} {
		if !strings.Contains(warning, want) {
			t.Errorf("the warning does not carry %q: %s", want, warning)
		}
	}

	// the other entry comes back as an id, not only as prose: a caller
	// working through hundreds of shows has to act on it, not read it
	if others := texts(out["duplicate_entries"]); len(others) != 1 || others[0] != "split-b" {
		t.Errorf("duplicate_entries = %v, want [split-b]", out["duplicate_entries"])
	}

	// and they come back even when nothing is absent: two entries holding the
	// same episodes at different quality is the other half of this shape, and
	// the entry asked may not be the one worth comparing against
	held := mustCall(t, cs, "show_episodes_exist", map[string]any{
		"series_id": "split-a", "episodes": []map[string]any{{"season": 1, "episode": 1}},
	})
	if others := texts(held["duplicate_entries"]); len(others) != 1 {
		t.Errorf("a show held twice said nothing about it when nothing was absent: %v", held)
	}
	// the warning is about an absence, so it only speaks when there is one
	if text(held["warning"]) != "" {
		t.Errorf("a call with nothing absent warned anyway: %v", held["warning"])
	}

	// a show held once says nothing, however much is absent
	alone := mustCall(t, cs, "show_episodes_exist", map[string]any{
		"series": "Severance", "episodes": []map[string]any{{"season": 9, "episode": 9}},
	})
	if text(alone["warning"]) != "" {
		t.Errorf("a show held once warned about itself: %v", alone["warning"])
	}
}

// C10: being the only candidate is not being a match. "Sentai Daishikkaku"
// turned up exactly one series - a different show scoring 0.30 on the bare
// word "Sentai" - and the answer was handed over with nothing on it to say
// so. The caller went on to ask what that series was missing, and believed
// the answer.
// Several weak candidates are guesses, not an ambiguity: the refusal says
// nothing matched well enough and lists them, rather than "matches 2
// series ... narrow it with library", which read as two good matches of
// which either would do.
func TestSeriesByNameRefusesSeveralGuesses(t *testing.T) {
	t.Parallel()

	shows := []*fakeSeries{
		{id: "tng", name: "Star Trek: The Next Generation", year: 1987, episodes: []ep{{season: 1, number: 1, name: "One", path: "/m/tng.mkv"}}},
		{id: "ds9", name: "Star Trek: Deep Space Nine", year: 1993, episodes: []ep{{season: 1, number: 1, name: "One", path: "/m/ds9.mkv"}}},
	}
	cs := session(t, tvServer(t, shows...), Options{})

	msg := mustRefuse(t, cs, "show_episodes_exist", map[string]any{
		"series": "Star Trek Picard", "episodes": []map[string]any{{"season": 1, "episode": 1}},
	})
	for _, want := range []string{"nothing well enough", "the closest are", "Next Generation", "Deep Space Nine", "guesses"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not carry %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "narrow it with library") {
		t.Errorf("two guesses were refused as an ambiguity: %s", msg)
	}
}

func TestSeriesByNameRefusesALoneGuess(t *testing.T) {
	t.Parallel()

	shows := []*fakeSeries{
		{id: "jetman", name: "Chojin Sentai Jetman", year: 1991, episodes: []ep{{season: 1, number: 1, name: "One", path: "/m/j.mkv"}}},
		{id: "swat75", name: "S.W.A.T.", year: 1975, episodes: []ep{{season: 1, number: 1, name: "One", path: "/m/s.mkv"}}},
	}
	cs := session(t, tvServer(t, shows...), Options{})

	msg := mustRefuse(t, cs, "show_episodes_exist", map[string]any{
		"series": "Sentai Daishikkaku", "episodes": []map[string]any{{"season": 1, "episode": 1}},
	})
	for _, want := range []string{"nothing well enough", "Jetman", "guess"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not carry %q: %s", want, msg)
		}
	}

	// a release name spelling an initialism with spaces still finds the
	// series the library spells with points - the search has to be asked for
	// the closed-up spelling, because the servers fold the points but not
	// the spaces
	out := mustCall(t, cs, "show_episodes_exist", map[string]any{
		"series": "S W A T", "episodes": []map[string]any{{"season": 1, "episode": 1}},
	})
	if text(out["series_id"]) != "swat75" {
		t.Errorf("S W A T resolved to %v", out["series_id"])
	}
}

// C11: the score belongs on the way out, not only in the refusal. A caller
// deciding whether to keep a file has to be able to tell a 1.00 from a 0.92,
// and the only way to see one used to be to make the query ambiguous on
// purpose.
func TestShowEpisodesExistReportsHowItMatched(t *testing.T) {
	t.Parallel()

	shows := []*fakeSeries{
		{id: "svu", name: "Law & Order: Special Victims Unit", year: 1999, episodes: []ep{{season: 1, number: 1, name: "One", path: "/m/svu.mkv"}}},
		{id: "lao", name: "Law & Order", year: 1990, episodes: []ep{{season: 1, number: 1, name: "One", path: "/m/lao.mkv"}}},
	}
	cs := session(t, tvServer(t, shows...), Options{})

	// the abbreviation is the whole point of the name: it says which show
	out := mustCall(t, cs, "show_episodes_exist", map[string]any{
		"series": "Law And Order SVU", "episodes": []map[string]any{{"season": 1, "episode": 1}},
	})
	if text(out["series_id"]) != "svu" {
		t.Fatalf("Law And Order SVU resolved to %v, not Special Victims Unit", out["series_id"])
	}
	match, ok := out["matched"].(map[string]any)
	if !ok {
		t.Fatalf("the answer does not say how it matched: %v", out)
	}
	if score := decimal(t, match["score"], "score"); score < seriesConfident {
		t.Errorf("matched at %v but answered anyway", score)
	}
	if text(match["matched_on"]) == "" {
		t.Errorf("the match does not say what matched: %v", match)
	}
	// the runner-up is what tells a near-tie from a certainty
	if match["runner_up_score"] == nil {
		t.Errorf("no runner-up reported, so a caller cannot see how close it was: %v", match)
	}

	// asked by id there is nothing to match, and nothing is claimed
	byID := mustCall(t, cs, "show_episodes_exist", map[string]any{
		"series_id": "svu", "episodes": []map[string]any{{"season": 1, "episode": 1}},
	})
	if byID["matched"] != nil {
		t.Errorf("an id needs no matching: %v", byID["matched"])
	}
}

// Every tool that reports audio reports it the same way. item_get used to say
// "eng aac 6ch" while an episode row said {language, codec, channels,
// bitrate}, and two tools disagreeing on the shape of one fact is how a
// caller that read one gets the other wrong.
func TestItemGetReportsAudioTheWayEpisodeRowsDo(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = []ep{{season: 1, number: 1, name: "Good News About Hell", path: "/m/s01e01.mkv"}}
	cs := session(t, tvServer(t, s), Options{})

	item := mustCall(t, cs, "item_get", map[string]any{"id": "sev-1-1"})
	row := objects(t, mustCall(t, cs, "library_episodes", map[string]any{"series_id": "sev"})["episodes"], "episodes")[0]

	fromItem := objects(t, item["audio"], "audio")
	fromRow := objects(t, row["audio"], "audio")
	if len(fromItem) != 1 || len(fromRow) != 1 {
		t.Fatalf("item_get audio = %v, episode row audio = %v", item["audio"], row["audio"])
	}
	for _, field := range []string{"language", "codec", "channels", "bitrate"} {
		if fromItem[0][field] == nil {
			t.Errorf("item_get's track has no %s: %v", field, fromItem[0])
		}
		if fromItem[0][field] != fromRow[0][field] {
			t.Errorf("%s: item_get says %v, the episode row says %v", field, fromItem[0][field], fromRow[0][field])
		}
	}
}

// C14: a download written over an existing path keeps the item's id and its
// date_created, so every "what was added" view is blind to it. The file's own
// timestamp is the only thing that moves, so without it an overwritten
// episode stays invisible.
func TestEpisodeRowsCarryBothDates(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = []ep{{season: 1, number: 1, name: "Good News About Hell", path: "/m/s01e01.mkv"}}
	f := tvServer(t, s)
	cs := session(t, f, Options{})

	row := objects(t, mustCall(t, cs, "library_episodes", map[string]any{"library": "Shows"})["episodes"], "episodes")[0]
	if text(row["date_created"]) == "" || text(row["file_modified"]) == "" {
		t.Errorf("row carries %v and %v", row["date_created"], row["file_modified"])
	}

	// asked for on their own, they come without the rest: the cheap sweep for
	// "what changed" is the whole point
	narrow := mustCall(t, cs, "library_episodes", map[string]any{"library": "Shows", "fields": []string{"file_modified"}})
	lean := objects(t, narrow["episodes"], "episodes")[0]
	if text(lean["file_modified"]) == "" {
		t.Errorf("file_modified was asked for and is missing: %v", lean)
	}
	for _, gone := range []string{"path", "date_created", "size", "width", "hdr"} {
		if lean[gone] != nil {
			t.Errorf("%s was not asked for: %v", gone, lean)
		}
	}

	// the changed-since filter reaches the server rather than being applied
	// to a page after the fact
	f.reset()
	mustCall(t, cs, "library_episodes", map[string]any{"library": "Shows", "saved_since": "2026-09-17T00:00:00Z"})
	asked := f.requests("/Items")
	if len(asked) == 0 || !strings.Contains(asked[0].Query, "MinDateLastSaved=2026-09-17") {
		t.Errorf("saved_since did not reach the server: %v", asked)
	}
}

// The runtime multiple is worked out from the runtime, so asking for the one
// without the other still answers it. The runtime used to be dropped first,
// and a caller asking only for the multiple got none at all.
func TestTheRuntimeMultipleCanBeAskedForAlone(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = []ep{
		{season: 1, number: 1, name: "one", path: "/m/1.mkv", minutes: 50},
		{season: 1, number: 2, name: "two", path: "/m/2.mkv", minutes: 50},
		{season: 1, number: 3, name: "three and four", path: "/m/3.mkv", minutes: 100},
		{season: 1, number: 5, name: "five", path: "/m/5.mkv", minutes: 50},
	}
	cs := session(t, tvServer(t, s), Options{})

	out := mustCall(t, cs, "library_episodes", map[string]any{"series_id": "sev", "fields": []string{"runtime_multiple"}})
	rows := objects(t, out["episodes"], "episodes")
	if len(rows) != 4 {
		t.Fatalf("episodes = %v", rows)
	}
	if got := decimal(t, rows[2]["runtime_multiple"], "runtime_multiple"); got != 2 {
		t.Errorf("the double episode's multiple = %v, want 2: %v", got, rows[2])
	}
	// and what was not asked for is still left out
	if rows[2]["runtime_s"] != nil || rows[2]["path"] != nil {
		t.Errorf("fields not asked for came back: %v", rows[2])
	}
}

// library_items reads every item's files with it, so an uncapped limit was a
// whole library in one answer. It is capped the way library_episodes is.
func TestLibraryItemsCapsItsPage(t *testing.T) {
	t.Parallel()

	f := tvServer(t, severance())
	cs := session(t, f, Options{})

	mustCall(t, cs, "library_items", map[string]any{"library": "Shows", "limit": 50000})
	asked := f.requests("/Items")
	if len(asked) == 0 {
		t.Fatal("the server was not asked")
	}
	last := asked[len(asked)-1].Query
	if !strings.Contains(last, "Limit="+strconv.Itoa(episodePageMax)) || strings.Contains(last, "Limit=50000") {
		t.Errorf("a limit of 50000 reached the server as %s, want the %d cap", last, episodePageMax)
	}
}

// library_episodes reads one show as readily as a library: by name or by id,
// the whole of it or one season - the specials as season 0 - and a season the
// show does not have is no episodes, not the show's others. The show comes
// back named at the top, and a name says how it was matched.
func TestLibraryEpisodesOfOneShow(t *testing.T) {
	t.Parallel()

	sev := severance()
	sev.episodes = append(sev.episodes,
		ep{season: 2, number: 1, name: "Hello, Ms. Cobel", path: "/media/shows/Severance/Season 02/S02E01.mkv"},
		ep{season: 0, number: 1, name: "Welcome to Lumon", path: "/media/shows/Severance/Specials/S00E01.mkv"},
	)
	expanse := &fakeSeries{id: "exp", name: "The Expanse", year: 2015, episodes: []ep{{season: 1, number: 1, name: "Dulcinea", path: "/media/shows/The Expanse/S01E01.mkv"}}}
	cs := session(t, tvServer(t, sev, expanse), Options{})

	titles := func(out map[string]any) []string {
		rows := objects(t, out["episodes"], "episodes")
		got := make([]string, 0, len(rows))
		for _, row := range rows {
			got = append(got, text(row["title"]))
		}
		return got
	}
	whole := []string{"Welcome to Lumon", "Good News About Hell", "Half Loop", "Hello, Ms. Cobel"}
	for _, args := range []map[string]any{{"series": "Severance"}, {"series": "sev"}, {"series_id": "sev"}} {
		out := mustCall(t, cs, "library_episodes", args)
		if out["series"] != "Severance" || out["series_id"] != "sev" || number(t, out["total"], "total") != 4 || !slices.Equal(titles(out), whole) {
			t.Errorf("%v = %v %v, %v episodes %v, want Severance's four", args, out["series"], out["series_id"], out["total"], titles(out))
		}
		byName := args["series"] == "Severance"
		if match, ok := out["matched"].(map[string]any); byName != ok || (ok && (match["series_id"] != "sev" || number(t, match["score"], "score") != 1)) {
			t.Errorf("%v matched = %v, want the score for a name and nothing for an id", args, out["matched"])
		}
	}

	for season, want := range map[int][]string{2: {"Hello, Ms. Cobel"}, 0: {"Welcome to Lumon"}, 9: nil} {
		out := mustCall(t, cs, "library_episodes", map[string]any{"series": "Severance", "season": season})
		if got := titles(out); !slices.Equal(got, want) || number(t, out["total"], "total") != len(want) || out["series_id"] != "sev" {
			t.Errorf("season %d = %v (total %v), want %v", season, got, out["total"], want)
		}
	}

	// a library read is every show's, and names none at the top
	all := mustCall(t, cs, "library_episodes", map[string]any{})
	if all["series"] != nil || all["series_id"] != nil || number(t, all["total"], "total") != 5 {
		t.Errorf("a library read = %v episodes naming %v %v", all["total"], all["series"], all["series_id"])
	}

	for args, want := range map[string]map[string]any{
		"series or series_id, not both":    {"series": "Severance", "series_id": "sev"},
		"library or series_id, not both":   {"series_id": "sev", "library": "Shows"},
		"season needs series or series_id": {"season": 1},
		`no series named "Breaking Bad"`:   {"series": "Breaking Bad"},
	} {
		if msg := mustRefuse(t, cs, "library_episodes", want); !strings.Contains(msg, args) {
			t.Errorf("%v = %s, want %q", want, msg, args)
		}
	}
}

// A show's name is matched the way show_episodes_exist matches it: a name
// two shows answer to equally is refused naming both, a name that only
// shares words with shows is refused naming the guesses, and neither is
// read.
func TestLibraryEpisodesRefusesAGuessOrATie(t *testing.T) {
	t.Parallel()

	tidy, messy := severance(), severance()
	messy.id = "sev-messy"
	messy.episodes = []ep{{season: 1, number: 1, name: "Good News About Hell", path: "/media/messy-shows/Severance/S01E01.mkv"}}
	tng := &fakeSeries{id: "tng", name: "Star Trek: The Next Generation", year: 1987, episodes: []ep{{season: 1, number: 1, name: "Encounter at Farpoint", path: "/m/tng.mkv"}}}
	ds9 := &fakeSeries{id: "ds9", name: "Star Trek: Deep Space Nine", year: 1993, episodes: []ep{{season: 1, number: 1, name: "Emissary", path: "/m/ds9.mkv"}}}
	cs := session(t, tvServer(t, tidy, messy, tng, ds9), Options{})

	tie := mustRefuse(t, cs, "library_episodes", map[string]any{"series": "Severance"})
	for _, want := range []string{"matches 2 series", "id sev ", "id sev-messy", "give series_id"} {
		if !strings.Contains(tie, want) {
			t.Errorf("the tie's refusal lacks %q: %s", want, tie)
		}
	}
	guess := mustRefuse(t, cs, "library_episodes", map[string]any{"series": "Star Trek Picard"})
	for _, want := range []string{"nothing well enough", "the closest are", "The Next Generation", "Deep Space Nine"} {
		if !strings.Contains(guess, want) {
			t.Errorf("the guesses' refusal lacks %q: %s", want, guess)
		}
	}
	// and either copy by its id is read
	if out := mustCall(t, cs, "library_episodes", map[string]any{"series": "sev-messy"}); out["series_id"] != "sev-messy" || number(t, out["total"], "total") != 1 {
		t.Errorf("the messy copy by id = %v", out)
	}
}

// A name can be what another show's id is: Emby's ids are numbers, and 24 is
// a show. Asked for "24" where one show has that id and another that name,
// neither is read in the other's place.
func TestLibraryEpisodesRefusesAnIDThatIsAnotherShowsName(t *testing.T) {
	t.Parallel()

	sev := severance()
	sev.id = "24"
	for i := range sev.episodes {
		sev.episodes[i].path = "/media/shows/Severance/" + sev.episodes[i].name + ".mkv"
	}
	twentyFour := &fakeSeries{id: "2400", name: "24", year: 2001, episodes: []ep{{season: 1, number: 1, name: "12:00 a.m.-1:00 a.m.", path: "/media/shows/24/S01E01.mkv"}}}
	cs := session(t, tvServer(t, sev, twentyFour), Options{})

	msg := mustRefuse(t, cs, "library_episodes", map[string]any{"series": "24"})
	for _, want := range []string{"is the id of Severance", "also names 24", "2400", "give series_id"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal lacks %q: %s", want, msg)
		}
	}
	if out := mustCall(t, cs, "library_episodes", map[string]any{"series_id": "24"}); out["series"] != "Severance" {
		t.Errorf("series_id 24 = %v, want Severance", out["series"])
	}
}
