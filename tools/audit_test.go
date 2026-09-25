package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
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
			ParentIndexNumber: 1, IndexNumber: 1, ProviderIDs: map[string]string{"Imdb": "tt0000002"},
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

// A duration too long to be an episode is broken metadata wherever it is: a
// season of two, a season where every file is broken, and the specials all
// hold one, and none of them has a median to compare against. The median
// comparison itself still needs three episodes.
func TestRuntimeAuditReportsBrokenDurationsInAnySeason(t *testing.T) {
	t.Parallel()

	s := &fakeSeries{id: "z", name: "Zzyzx Show", episodes: []ep{
		{season: 0, number: 1, name: "Special", path: "/m/s0e1.mkv", minutes: 100000},
		// a season of two, one of them broken
		{season: 1, number: 1, name: "Fine", path: "/m/s1e1.mkv", minutes: 22},
		{season: 1, number: 2, name: "Broken Short Season", path: "/m/s1e2.mkv", minutes: 100000},
		// a season where every file is broken
		{season: 2, number: 1, name: "Broken A", path: "/m/s2e1.mkv", minutes: 90000},
		{season: 2, number: 2, name: "Broken B", path: "/m/s2e2.mkv", minutes: 90000},
		{season: 2, number: 3, name: "Broken C", path: "/m/s2e3.mkv", minutes: 90000},
		// an ordinary season with one truncated file, judged by its median
		{season: 3, number: 1, name: "One", path: "/m/s3e1.mkv", minutes: 22},
		{season: 3, number: 2, name: "Two", path: "/m/s3e2.mkv", minutes: 22},
		{season: 3, number: 3, name: "Three", path: "/m/s3e3.mkv", minutes: 22},
		{season: 3, number: 4, name: "Truncated", path: "/m/s3e4.mkv", minutes: 5},
	}}
	f := tvServer(t, s)
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
	details := map[string]string{}
	for _, row := range objects(t, out["findings"], "findings") {
		details[text(row["name"])] = text(row["detail"])
	}
	for _, name := range []string{"Special", "Broken Short Season", "Broken A", "Broken B", "Broken C"} {
		found := false
		for n, detail := range details {
			if strings.HasSuffix(n, " "+name) && strings.Contains(detail, "duration metadata is broken") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: a broken duration was not reported: %v", name, details)
		}
	}
	if detail := details["Zzyzx Show S03E04 Truncated"]; !strings.Contains(detail, "season median 22 min") {
		t.Errorf("the truncated file = %q", detail)
	}
	if n := number(t, out["total_findings"], "total_findings"); n != 6 {
		t.Errorf("total_findings = %d, want the five broken and the truncated one: %v", n, details)
	}

	// audit_all's row is the audit's own count
	all := mustCall(t, cs, "audit_all", map[string]any{"library": "Shows"})
	for _, row := range objects(t, all["audits"], "audits") {
		if text(row["audit"]) == "audit_runtime" && number(t, row["findings"], "findings") != 6 {
			t.Errorf("audit_all's audit_runtime row = %v, want 6", row)
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
		{"audit_missing_poster", "Movie,Series", "default 100"},
		{"audit_missing_overview", "Movie,Series", "default 100"},
		{"audit_missing_metadata_provider", "Movie,Series", "default 100"},
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

	rows := map[string]map[string]any{}
	for _, row := range objects(t, mustCall(t, cs, "audit_all", map[string]any{"library": "Tunes"})["audits"], "audits") {
		rows[text(row["audit"])] = row
	}
	for audit, want := range map[string]int{"audit_missing_poster": 1, "audit_spelling": 1} {
		row := rows[audit]
		if row == nil || row["skipped"] != nil || number(t, row["findings"], "findings") != want || number(t, row["items_scanned"], "items_scanned") != 3 || text(row["types"]) != "MusicAlbum" {
			t.Errorf("%s over the music library = %v, want %d of the 3 albums, read as MusicAlbum", audit, row, want)
		}
	}
	for audit, row := range rows {
		if audit != "audit_missing_poster" && audit != "audit_spelling" && row["skipped"] == nil {
			t.Errorf("%s ran over a music library: %v", audit, row)
		}
	}

	// over every library the two read albums beside films and series
	for _, row := range objects(t, mustCall(t, cs, "audit_all", map[string]any{})["audits"], "audits") {
		switch text(row["audit"]) {
		case "audit_missing_poster", "audit_spelling":
			if types := text(row["types"]); !strings.HasSuffix(types, ",MusicAlbum") || number(t, row["findings"], "findings") != 1 {
				t.Errorf("over every library %v, want the album counted and MusicAlbum among its types", row)
			}
		case "audit_missing_overview":
			if row["types"] != nil {
				t.Errorf("an audit with nothing to say about music names types over every library: %v", row)
			}
		}
	}
}
