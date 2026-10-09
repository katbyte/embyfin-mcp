package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
)

// TMDB and TVDB number films and TV apart, so a film and a series holding
// the same number are two unrelated titles, not one held twice. An IMDb id
// names one title whatever it is, so the same one on a film and a series
// still groups them.
func TestDuplicatesKeepFilmsAndTVApart(t *testing.T) {
	t.Parallel()

	film := embyfin.Item{ID: "f", Name: "Zzyzx Film", Type: typeMovie, ProviderIDs: map[string]string{"Tmdb": "1399"}}
	series := embyfin.Item{ID: "s", Name: "Zzyzx Series", Type: "Series", ProviderIDs: map[string]string{"Tmdb": "1399"}}
	tvdbFilm := embyfin.Item{ID: "tf", Name: "Zzyzx Other Film", Type: typeMovie, ProviderIDs: map[string]string{"Tvdb": "81189"}}
	tvdbSeries := embyfin.Item{ID: "ts", Name: "Zzyzx Other Series", Type: "Series", ProviderIDs: map[string]string{"Tvdb": "81189"}}
	if groups := groupByProviderID([]embyfin.Item{film, series, tvdbFilm, tvdbSeries}); len(groups) != 0 {
		t.Errorf("a film and a series sharing a number were grouped: %v", groups)
	}

	// the same number on two films, or two series, is still a copy
	filmAgain := embyfin.Item{ID: "f2", Name: "Zzyzx Film", Type: typeMovie, ProviderIDs: map[string]string{"Tmdb": "1399"}}
	seriesAgain := embyfin.Item{ID: "s2", Name: "Zzyzx Series", Type: "Series", ProviderIDs: map[string]string{"Tmdb": "1399"}}
	groups := groupByProviderID([]embyfin.Item{film, series, filmAgain, seriesAgain})
	if len(groups) != 2 || len(groups[0]) != 2 || len(groups[1]) != 2 || groups[0][0].Type != groups[0][1].Type || groups[1][0].Type != groups[1][1].Type {
		t.Errorf("groups = %v, want the two films and the two series apart", groups)
	}

	// an imdb id is one title whatever the entry is
	imdbFilm := embyfin.Item{ID: "if", Name: "Zzyzx", Type: typeMovie, ProviderIDs: map[string]string{"Imdb": "tt0000001"}}
	imdbSeries := embyfin.Item{ID: "is", Name: "Zzyzx", Type: "Series", ProviderIDs: map[string]string{"Imdb": "tt0000001"}}
	if groups := groupByProviderID([]embyfin.Item{imdbFilm, imdbSeries}); len(groups) != 1 {
		t.Errorf("one imdb id on a film and a series = %v, want one group", groups)
	}
}

// An episode's id is shared loosely, so the first episode of two unrelated
// shows carrying one imdb id is not a copy. The same show held under two
// series entries - a folder renamed, the server building a second entry
// with the same name - is the case the audit exists for, and still groups.
func TestDuplicateEpisodesMustBeOfTheSameShow(t *testing.T) {
	t.Parallel()

	episode := func(id, seriesID, series string) embyfin.Item {
		return embyfin.Item{
			ID: id, Name: "Pilot", Type: typeEpisode, SeriesID: seriesID, SeriesName: series,
			ParentIndexNumber: new(1), IndexNumber: new(1), ProviderIDs: map[string]string{"Imdb": "tt0000002"},
		}
	}
	if groups := groupByProviderID([]embyfin.Item{episode("a", "s1", "Zzyzx Show"), episode("b", "s2", "Another Zzyzx Show")}); len(groups) != 0 {
		t.Errorf("two shows' first episodes sharing a loose imdb id were grouped: %v", groups)
	}

	// one show split across two entries: two series ids, one name, spelled
	// the way a rename that changed only case or spacing leaves it
	if groups := groupByProviderID([]embyfin.Item{episode("a", "s1", "Zzyzx Show"), episode("b", "s2", "Zzyzx  show")}); len(groups) != 1 || len(groups[0]) != 2 {
		t.Errorf("one show under two entries = %v, want its episode grouped", groups)
	}
}

// The runtime audit reports only a length no film or episode can have: under
// two minutes, or twelve hours or more. Nothing is judged against its season:
// a 5-minute file among 22-minute ones is no finding here (TMDB's length for
// that episode is audit_provider's), a season where every file is broken is
// reported file by file, and eleven hours is long, not broken.
func TestRuntimeAuditReportsOnlyLengthsNoFileCanHave(t *testing.T) {
	t.Parallel()

	s := &fakeSeries{id: "z", name: "Zzyzx Show", episodes: []ep{
		{season: 0, number: 1, name: "Special", path: "/m/s0e1.mkv", minutes: 100000},
		{season: 1, number: 1, name: "Fine", path: "/m/s1e1.mkv", minutes: 22},
		{season: 1, number: 2, name: "Broken Short Season", path: "/m/s1e2.mkv", minutes: 100000},
		// a season where every file is broken
		{season: 2, number: 1, name: "Broken A", path: "/m/s2e1.mkv", minutes: 90000},
		{season: 2, number: 2, name: "Broken B", path: "/m/s2e2.mkv", minutes: 90000},
		{season: 2, number: 3, name: "Broken C", path: "/m/s2e3.mkv", minutes: 90000},
		// short beside its season, and long, but either a length a file can have
		{season: 3, number: 1, name: "One", path: "/m/s3e1.mkv", minutes: 22},
		{season: 3, number: 2, name: "Two", path: "/m/s3e2.mkv", minutes: 22},
		{season: 3, number: 3, name: "Five Minutes", path: "/m/s3e3.mkv", minutes: 5},
		{season: 3, number: 4, name: "Eleven Hours", path: "/m/s3e4.mkv", minutes: 11*60 + 59},
		// and the two that cannot be episodes at all
		{season: 3, number: 5, name: "A Minute", path: "/m/s3e5.mkv", minutes: 1},
		{season: 3, number: 6, name: "Twelve Hours", path: "/m/s3e6.mkv", minutes: 12 * 60},
	}}
	film := &fakeSeries{id: "f", name: "Zzyzx Film", film: true, path: "/m/Zzyzx Film (2001)/Zzyzx Film (2001).mkv", year: 2001, filmSeconds: 90}
	// a whole DVD in one file, which the server times at a minute and a
	// half: a title of the disc, or its menu, and no length of the film's
	disc := &fakeSeries{id: "d", name: "Zzyzx Disc", film: true, path: "/m/Zzyzx Disc (1942)/Zzyzx Disc (1942) - DVD.ISO", year: 1942, filmSeconds: 88}
	f := tvServer(t, s, film, disc)
	// audit_all reads the library as an administrator is shown it (for
	// versions) and every account's watch state: one account, whose view is
	// the library's
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": []any{map[string]any{"Id": "u1", "Name": "Quux", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}}}, "TotalRecordCount": 1})
	})
	f.mux.HandleFunc("GET /Users/u1/Items", func(w http.ResponseWriter, r *http.Request) {
		view := r.Clone(r.Context())
		view.URL.Path = "/Items"
		f.mux.ServeHTTP(w, view)
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_runtime", map[string]any{})
	findings := objects(t, out["findings"], "findings")
	got := make([]string, 0, len(findings))
	for _, row := range findings {
		got = append(got, text(row["name"])+": "+text(row["detail"]))
	}
	want := []string{
		// the broken first, longest first, and a name settling a tie
		"Zzyzx Show S00E01 Special: 1666 h 40 min: not a runtime, the file's duration metadata is broken",
		"Zzyzx Show S01E02 Broken Short Season: 1666 h 40 min: not a runtime, the file's duration metadata is broken",
		"Zzyzx Show S02E01 Broken A: 1500 h 0 min: not a runtime, the file's duration metadata is broken",
		"Zzyzx Show S02E02 Broken B: 1500 h 0 min: not a runtime, the file's duration metadata is broken",
		"Zzyzx Show S02E03 Broken C: 1500 h 0 min: not a runtime, the file's duration metadata is broken",
		"Zzyzx Show S03E06 Twelve Hours: 12 h 0 min: not a runtime, the file's duration metadata is broken",
		// then the too short, shortest first
		"Zzyzx Show S03E05 A Minute: 60 s: too short to be the episode, an incomplete, sample or broken file",
		"Zzyzx Film: 90 s: too short to be the film, an incomplete, sample or broken file",
	}
	if !slices.Equal(got, want) {
		t.Errorf("findings =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if n := number(t, out["total_findings"], "total_findings"); n != len(want) {
		t.Errorf("total_findings = %d, want %d", n, len(want))
	}
	// the film and the disc image are scanned beside the twelve episodes,
	// and the disc image is counted and not judged
	if n := number(t, out["items_scanned"], "items_scanned"); n != 14 {
		t.Errorf("items_scanned = %d, want the twelve episodes, the film and the disc image", n)
	}
	if number(t, out["disc_images"], "disc_images") != 1 || number(t, out["unprobed"], "unprobed") != 0 {
		t.Errorf("disc_images = %v, unprobed = %v, want the one disc image and nothing unprobed", out["disc_images"], out["unprobed"])
	}

	// audit_all's row is the audit's own count
	all := mustCall(t, cs, "audit_all", map[string]any{})
	for _, row := range objects(t, all["audits"], "audits") {
		if text(row["audit"]) != "audit_runtime" {
			continue
		}
		if number(t, row["findings"], "findings") != len(want) {
			t.Errorf("audit_all's audit_runtime row = %v, want %d", row, len(want))
		}
		// and says the disc image it did not judge
		if !boolean(t, row["partial"], "partial") || !strings.Contains(text(row["note"]), "disc images (.iso), not judged either: 1") {
			t.Errorf("audit_all's audit_runtime row = %v, want it partial for the disc image", row)
		}
	}
}

// Every audit's schema states the defaults its sweep really has: a caller
// reading "Movie,Series" asks for episodes by hand that it already gets, or
// never learns that a series is left out.
func TestAuditSchemasStateTheirRealDefaults(t *testing.T) {
	t.Parallel()

	res, err := session(t, newFakeServer(t), Options{}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	type schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	schemas := map[string]schema{}
	for _, tool := range res.Tools {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var s schema
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		schemas[tool.Name] = s
	}

	for _, tc := range []struct{ tool, types, limit string }{
		{"audit_duplicates", "Movie,Series,Episode", "default 50"},
		{"audit_multiple_versions", "Movie,Episode", "default 100"},
		{"audit_missing_metadata", "Movie,Series", "default 100"},
	} {
		s, ok := schemas[tc.tool]
		if !ok {
			t.Errorf("%s is not registered", tc.tool)

			continue
		}
		types := s.Properties["types"].Description
		// the default named, and no other list of types beside it
		if !strings.Contains(types, "defaults to "+tc.types) || strings.Count(types, "Movie") != 1 {
			t.Errorf("%s: types says %q, want its default %s", tc.tool, types, tc.types)
		}
		if limit := s.Properties["limit"].Description; !strings.Contains(limit, tc.limit) {
			t.Errorf("%s: limit says %q, want %s", tc.tool, limit, tc.limit)
		}
	}
}

// A music library read as empty in audit_all: every audit swept films and
// series unless told otherwise, and audit_all told none of them, so an album
// with no cover and a genre spelled two ways counted 0 in the call that says
// where to start. In a music library the two audits that apply to music now
// count its albums, the rest say they have nothing there to read, and over
// every library the two count albums beside films and series, naming the
// types they read.
func TestAuditAllReadsAMusicLibrary(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	music := map[string]any{"Name": "Tunes", "CollectionType": "music", "ItemId": "lib-music", "Locations": []string{"/media/music"}}
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(music)) })
	albums := []map[string]any{
		{"Id": "a1", "Name": "Zzyzx Covered", "Type": "MusicAlbum", "Genres": []string{"Electronic"}, "ImageTags": map[string]string{"Primary": "x"}},
		{"Id": "a2", "Name": "Zzyzx Bare", "Type": "MusicAlbum", "Genres": []string{"Electronica"}},
		{"Id": "a3", "Name": "Zzyzx Third", "Type": "MusicAlbum", "Genres": []string{"Electronic"}, "ImageTags": map[string]string{"Primary": "y"}},
	}
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(param(r.URL.Query(), "IncludeItemTypes"), "MusicAlbum") {
			writeJSON(t, w, page())
			return
		}
		writeJSON(t, w, page(albums...))
	})
	adminView(t, f)
	cs := session(t, f, Options{})

	// audit_missing_metadata is a row a problem, named by problems
	rowName := func(row map[string]any) string {
		if p := text(row["problems"]); p != "" {
			return text(row["audit"]) + ":" + p
		}

		return text(row["audit"])
	}
	rows := map[string]map[string]any{}
	for _, row := range objects(t, mustCall(t, cs, "audit_all", map[string]any{"library": "Tunes"})["audits"], "audits") {
		rows[rowName(row)] = row
	}
	for audit, want := range map[string]int{"audit_missing_metadata:poster": 1, "audit_spelling": 1} {
		row := rows[audit]
		if row == nil || row["skipped"] != nil || number(t, row["findings"], "findings") != want || number(t, row["items_scanned"], "items_scanned") != 3 || text(row["types"]) != "MusicAlbum" {
			t.Errorf("%s over the music library = %v, want %d of the 3 albums, read as MusicAlbum", audit, row, want)
		}
	}
	// the spaces in names and folders are music's as much as anything's
	if row := rows["audit_whitespace"]; row == nil || row["skipped"] != nil || number(t, row["items_scanned"], "items_scanned") != 3 {
		t.Errorf("audit_whitespace over the music library = %v, want the 3 albums read", row)
	}
	for audit, row := range rows {
		if audit != "audit_missing_metadata:poster" && audit != "audit_spelling" && audit != "audit_whitespace" && row["skipped"] == nil {
			t.Errorf("%s ran over a music library: %v", audit, row)
		}
	}
	for _, problem := range []string{"provider_id", "overview"} {
		if row := rows["audit_missing_metadata:"+problem]; row == nil || row["skipped"] == nil {
			t.Errorf("audit_missing_metadata's %s row over the music library = %v, want it skipped with its problem named", problem, row)
		}
	}

	// over every library the two read albums beside films and series
	for _, row := range objects(t, mustCall(t, cs, "audit_all", map[string]any{})["audits"], "audits") {
		switch rowName(row) {
		case "audit_missing_metadata:poster", "audit_spelling":
			if types := text(row["types"]); !strings.HasSuffix(types, ",MusicAlbum") || number(t, row["findings"], "findings") != 1 {
				t.Errorf("over every library %v, want the album counted and MusicAlbum among its types", row)
			}
		case "audit_missing_metadata:overview":
			if row["types"] != nil {
				t.Errorf("an audit with nothing to say about music names types over every library: %v", row)
			}
		}
	}
}

// audit_all said less than it knew. Its missing-episodes row asks no
// provider, so it can see only gaps between files, and "series with episodes
// missing: 0" read as nothing missing; its runtime row counted files with no
// runtime as scanned; and over a library of home videos every row counted 0,
// which read as a clean library. Each now says what it left out.
func TestAuditAllSaysWhatItDidNotCheck(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	shows := map[string]any{"Name": "Shows", "CollectionType": "tvshows", "ItemId": "lib", "Locations": []string{"/media/shows"}}
	clips := map[string]any{"Name": "Clips", "CollectionType": "homevideos", "ItemId": "clips", "Locations": []string{"/media/clips"}}
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(shows, clips)) })
	series := map[string]any{"Id": "z", "Name": "Zzyzx Show", "Type": "Series", "Path": "/media/shows/Zzyzx Show"}
	episode := func(n int, minutes int) map[string]any {
		return map[string]any{
			"Id": fmt.Sprintf("z%d", n), "Name": fmt.Sprintf("E%d", n), "Type": "Episode", "SeriesId": "z", "SeriesName": "Zzyzx Show",
			"ParentIndexNumber": 1, "IndexNumber": n, "LocationType": "FileSystem", "Path": fmt.Sprintf("/media/shows/Zzyzx Show/S01E%02d.mkv", n),
			"RunTimeTicks": int64(minutes) * 60 * ticksPerSecond,
		}
	}
	// E02 is missing between the files, and E03 was never probed
	episodes := []map[string]any{episode(1, 30), episode(3, 0)}
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		switch types := param(r.URL.Query(), "IncludeItemTypes"); {
		case types == "Episode", types == "Movie,Episode":
			writeJSON(t, w, page(episodes...))
		case strings.Contains(types, "Series") && param(r.URL.Query(), "Ids") != "":
			writeJSON(t, w, page(series))
		default:
			writeJSON(t, w, page())
		}
	})
	adminView(t, f)
	cs := session(t, f, Options{})

	// over a library of home videos, every audit has a row, and every row
	// says it was not checked
	clipRows := objects(t, mustCall(t, cs, "audit_all", map[string]any{"library": "Clips"})["audits"], "audits")
	clipNames := make([]auditAllKey, 0, len(clipRows))
	for _, row := range clipRows {
		clipNames = append(clipNames, auditAllKey{audit: text(row["audit"]), problems: text(row["problems"])})
		want := "not checked for this library type: a homevideos library"
		if text(row["audit"]) == "audit_orphans" {
			want = "server-wide"
		}
		if !boolean(t, row["skipped"], "skipped") || !strings.Contains(text(row["note"]), want) {
			t.Errorf("over home videos %v, want it skipped: %q", row, want)
		}
	}
	if !slices.Equal(clipNames, auditAllRows()) {
		t.Errorf("over home videos the rows are %v, want one for every audit: %v", clipNames, auditAllRows())
	}

	// and the list those rows are made from is every row a real run makes,
	// in its order
	out := mustCall(t, cs, "audit_all", map[string]any{})
	if got := texts(out["libraries_not_checked"]); !slices.Equal(got, []string{"Clips (homevideos)"}) {
		t.Errorf("libraries_not_checked = %v", got)
	}
	names := func(out map[string]any) ([]auditAllKey, map[string]map[string]any) {
		audits := objects(t, out["audits"], "audits")
		names, byName := make([]auditAllKey, 0, len(audits)), map[string]map[string]any{}
		for _, row := range audits {
			byName[text(row["audit"])] = row
			names = append(names, auditAllKey{audit: text(row["audit"]), problems: text(row["problems"])})
		}

		return names, byName
	}
	got, rows := names(out)
	if !slices.Equal(got, auditAllRows()) {
		t.Errorf("audit_all over every library has rows %v, want auditAllRows %v", got, auditAllRows())
	}
	if showNames, _ := names(mustCall(t, cs, "audit_all", map[string]any{"library": "Shows"})); !slices.Equal(showNames, auditAllRows()) {
		t.Errorf("audit_all over one library has rows %v, want auditAllRows %v", showNames, auditAllRows())
	}
	if row := rows["audit_missing_episodes"]; number(t, row["findings"], "findings") != 1 || !boolean(t, row["partial"], "partial") || !strings.Contains(text(row["note"]), "runs not known: 1 of the 1 shows") {
		t.Errorf("audit_missing_episodes row = %v, want the gap counted and the run said to be unknown", row)
	}
	if row := rows["audit_runtime"]; number(t, row["items_scanned"], "items_scanned") != 2 || !boolean(t, row["partial"], "partial") || !strings.Contains(text(row["note"]), "files the server holds no runtime for (never probed), counted in items_scanned and not judged: 1") {
		t.Errorf("audit_runtime row = %v, want the unprobed file said", row)
	}
	if n := number(t, mustCall(t, cs, "audit_runtime", map[string]any{})["unprobed"], "unprobed"); n != 1 {
		t.Errorf("audit_runtime unprobed = %d, want 1", n)
	}
}

// A show's extras are not its episodes. Emby 4.10 reads a featurette in a
// season's Extras folder as an episode with no number, and the audits that
// judge episodes judged it: a 360p featurette was an episode worth replacing,
// a three-minute one a truncated episode, and two seasons' featurettes one
// episode filed twice. A show called Extras, its episodes in its own folder,
// is still judged.
func TestAuditsLeaveAShowsExtrasAlone(t *testing.T) {
	t.Parallel()

	show := &fakeSeries{id: "z", name: "Zzyzx Show", episodes: []ep{
		{season: 1, number: 1, name: "One", path: "/media/shows/Zzyzx Show/Season 01/Zzyzx Show S01E01.mkv"},
		{season: 1, number: 2, name: "Two", path: "/media/shows/Zzyzx Show/Season 01/Zzyzx Show S01E02.mkv"},
		{season: 1, number: 3, name: "Three", path: "/media/shows/Zzyzx Show/Season 01/Zzyzx Show S01E03.mkv"},
		// what Emby makes of each season's Extras folder
		{season: 1, name: "Featurette", path: "/media/shows/Zzyzx Show/Season 01/Extras/Featurette.mkv", minutes: 1, width: 640, height: 360},
		{season: 1, name: "Featurette", path: "/media/shows/Zzyzx Show/Season 02/extras/Featurette.mkv", minutes: 1, width: 640, height: 360},
	}}
	named := &fakeSeries{id: "x", name: "Extras", episodes: []ep{
		{season: 1, number: 1, name: "Cut Short", path: "/media/shows/Extras/Extras S01E01.mkv", minutes: 1, width: 640, height: 360},
		{season: 1, number: 2, name: "Two", path: "/media/shows/Extras/Extras S01E02.mkv"},
		{season: 1, number: 3, name: "Three", path: "/media/shows/Extras/Extras S01E03.mkv"},
		{season: 1, number: 4, name: "Four", path: "/media/shows/Extras/Extras S01E04.mkv"},
	}}
	f := tvServer(t, show, named)
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": []any{map[string]any{"Id": "u1", "Name": "Quux", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}}}, "TotalRecordCount": 1})
	})
	f.mux.HandleFunc("GET /Users/u1/Items", func(w http.ResponseWriter, r *http.Request) {
		view := r.Clone(r.Context())
		view.URL.Path = "/Items"
		f.mux.ServeHTTP(w, view)
	})
	cs := session(t, f, Options{})

	names := func(out map[string]any) []string {
		rows := objects(t, out["findings"], "findings")
		got := make([]string, 0, len(rows))
		for _, row := range rows {
			got = append(got, text(row["name"]))
		}

		return got
	}
	quality := mustCall(t, cs, "audit_quality", map[string]any{"types": "Episode"})
	if got := names(quality); !slices.Equal(got, []string{"Extras S01E01 Cut Short"}) || number(t, quality["items_scanned"], "items_scanned") != 7 {
		t.Errorf("audit_quality = %v of %v scanned, want the Extras episode alone of the seven episodes", got, quality["items_scanned"])
	}
	runtime := mustCall(t, cs, "audit_runtime", map[string]any{})
	if got := names(runtime); !slices.Equal(got, []string{"Extras S01E01 Cut Short"}) || number(t, runtime["items_scanned"], "items_scanned") != 7 {
		t.Errorf("audit_runtime = %v of %v scanned, want the Extras episode alone of the seven episodes", got, runtime["items_scanned"])
	}
	if dup := mustCall(t, cs, "audit_duplicate_episodes", map[string]any{}); number(t, dup["total_findings"], "total_findings") != 0 || number(t, dup["items_scanned"], "items_scanned") != 7 {
		t.Errorf("audit_duplicate_episodes = %v, want the two featurettes no episode filed twice", dup)
	}
}
