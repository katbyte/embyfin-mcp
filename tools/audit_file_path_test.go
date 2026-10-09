package tools

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/mediapath"
	"github.com/katbyte/embyfin-mcp/lib/naming"
	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/katbyte/embyfin-mcp/sdk/tmdb"
)

// audit_file_path over episodes: a number the file and the server read
// differently, a run of episodes one side claims and the other does not, a
// file named for another series, a file named for another episode, and a
// bare file that claims nothing.
func TestAuditFilePathEpisodes(t *testing.T) {
	t.Parallel()

	s := &fakeSeries{id: "s1", name: "Zzyzx Show", year: 2020, episodes: []ep{
		{season: 1, number: 1, name: "Pilot", path: "/media/shows/Zzyzx Show/Season 01/Zzyzx Show S01E01 - Pilot.mkv"},
		{season: 1, number: 2, name: "Second", path: "/media/shows/Zzyzx Show/Season 01/Zzyzx Show S01E03 - Second.mkv"},
		{season: 1, number: 4, name: "Double", path: "/media/shows/Zzyzx Show/Season 01/Zzyzx Show S01E04E05 - Double.mkv"},
		{season: 1, number: 6, number2: 7, name: "Run", path: "/media/shows/Zzyzx Show/Season 01/Zzyzx Show S01E06 - Run.mkv"},
		{season: 2, number: 1, name: "Wrong Season", path: "/media/shows/Zzyzx Show/Season 02/Zzyzx Show S01E01 - Wrong Season.mkv"},
		{season: 1, number: 8, name: "Other", path: "/media/shows/Zzyzx Show/Season 01/Other.Show.S01E08.Other.mkv"},
		{season: 1, number: 9, name: "Real Title", path: "/media/shows/Zzyzx Show/Season 01/Zzyzx Show S01E09 - Some Other Title.mkv"},
		{season: 1, number: 10, name: "Bare", path: "/media/shows/Zzyzx Show/Season 01/S01E10.mkv"},
	}}
	cs := session(t, tvServer(t, s), Options{})

	out := mustCall(t, cs, "audit_file_path", map[string]any{"library": "Shows"})
	if number(t, out["items_scanned"], "items_scanned") != 9 || number(t, out["total_findings"], "total_findings") != 6 || number(t, out["unnamed"], "unnamed") != 1 {
		t.Fatalf("out = %v", out)
	}
	rows := objects(t, out["findings"], "findings")
	problems := map[string]string{}
	for _, r := range rows {
		problems[text(r["path"])] = strings.Join(texts(r["problems"]), " | ")
	}
	for path, want := range map[string]string{
		"S01E03 - Second":       "episode: the file says E03, the server holds E02",
		"S01E04E05 - Double":    "episode: the file holds E04-E05, the server holds E04 alone",
		"S01E06 - Run":          "episode: the server holds E06-E07, the file claims E06",
		"S01E01 - Wrong Season": "season: the file says season 1, the server holds season 2",
		"Other.Show.S01E08":     `series: the file is named for "Other Show", the server holds it under "Zzyzx Show"`,
		"Some Other Title":      `title: the file is named "Some Other Title", the server holds "Real Title"`,
	} {
		var got string
		for p, v := range problems {
			if strings.Contains(p, path) {
				got = v
			}
		}
		if !strings.Contains(got, want) {
			t.Errorf("%s: problems = %q, want %q", path, got, want)
		}
	}
	// the numbers first, the title last
	if last := rows[len(rows)-1]; text(last["title_in_file"]) != "Some Other Title" || text(last["title_on_server"]) != "Real Title" {
		t.Errorf("the title mismatch is not last: %v", last)
	}
	byCheck := object(t, out["by_check"], "by_check")
	if number(t, byCheck["episode"], "episode") != 3 || number(t, byCheck["season"], "season") != 1 || number(t, byCheck["series"], "series") != 1 || number(t, byCheck["title"], "title") != 1 || byCheck["year"] != nil {
		t.Errorf("by_check = %v", byCheck)
	}

	// one check alone
	only := mustCall(t, cs, "audit_file_path", map[string]any{"library": "Shows", "checks": "title"})
	if number(t, only["total_findings"], "total_findings") != 1 {
		t.Errorf("checks=title found %v", only["findings"])
	}
	if msg := mustRefuse(t, cs, "audit_file_path", map[string]any{"checks": "colour"}); !strings.Contains(msg, "checks must be among") {
		t.Errorf("an unknown check: %s", msg)
	}
}

// audit_file_path over films: the year in the folder against the metadata,
// the title before the year against the name, and the shapes that are not
// mismatches: an edition after the year, a title that ends in a year, a
// scene name, a title that is a year.
func TestAuditFilePathFilms(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Name":"Films","ItemId":"lib","CollectionType":"movies","Locations":["/m"]}],"TotalRecordCount":1}`)
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"TotalRecordCount": 8, "Items": []map[string]any{
			{"Id": "1", "Name": "Dune", "Type": "Movie", "ProductionYear": 1984, "Path": "/m/Dune (2021)/Dune (2021).mkv"},
			{"Id": "2", "Name": "Alien", "Type": "Movie", "ProductionYear": 1979, "Path": "/m/Alien (1979) Directors Cut/Alien (1979) Directors Cut.mkv"},
			{"Id": "3", "Name": "Blade Runner 2049", "Type": "Movie", "ProductionYear": 2017, "Path": "/m/Blade Runner 2049 (2017)/Blade Runner 2049 (2017).mkv"},
			{"Id": "4", "Name": "Blade Runner 2049", "Type": "Movie", "ProductionYear": 2017, "Path": "/m/Blade.Runner.2049.2017.1080p.BluRay.x264-GRP.mkv"},
			{"Id": "5", "Name": "2012", "Type": "Movie", "ProductionYear": 2009, "Path": "/m/2012 (2009)/2012 (2009).mkv"},
			{"Id": "6", "Name": "Zzyzx Title", "Type": "Movie", "ProductionYear": 2001, "Path": "/m/Zzyzx Title/Zzyzx Title.mkv"},
			{"Id": "7", "Name": "Another Picture", "Type": "Movie", "ProductionYear": 2001, "Path": "/m/Some Film (2001)/Some Film (2001).mkv"},
			{"Id": "8", "Name": "New Name", "Type": "Movie", "ProductionYear": 2010, "Path": "/m/Old Name (1990)/Old Name (1990).mkv"},
		}})
	})
	out := mustCall(t, session(t, f, Options{}), "audit_file_path", map[string]any{"library": "Films"})
	if number(t, out["items_scanned"], "items_scanned") != 8 || number(t, out["total_findings"], "total_findings") != 3 {
		t.Fatalf("out = %v", out)
	}
	rows := objects(t, out["findings"], "findings")
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, text(r["id"]))
	}
	// the titles first, the year alone last
	if ids[2] != "1" || !slices.Contains(ids[:2], "7") || !slices.Contains(ids[:2], "8") {
		t.Errorf("findings = %v, want 7 and 8 then Dune", ids)
	}
	for _, r := range rows {
		ps := texts(r["problems"])
		switch text(r["id"]) {
		case "1":
			if len(ps) != 1 || ps[0] != "year: path says 2021, metadata says 1984" {
				t.Errorf("Dune = %v", ps)
			}
		case "7":
			if len(ps) != 1 || !strings.HasPrefix(ps[0], `title: the path is named "Some Film", the server holds "Another Picture"`) || text(r["title_in_file"]) != "Some Film" {
				t.Errorf("Some Film = %v", r)
			}
		case "8":
			if len(ps) != 2 || !strings.HasPrefix(ps[0], "title:") || !strings.HasPrefix(ps[1], "year: path says 1990, metadata says 2010") {
				t.Errorf("Old Name = %v", ps)
			}
		}
	}
	byCheck := object(t, out["by_check"], "by_check")
	if number(t, byCheck["title"], "title") != 2 || number(t, byCheck["year"], "year") != 2 {
		t.Errorf("by_check = %v", byCheck)
	}
}

// audit_quality says how many files it could not judge: a file the server
// never probed has no picture, and left out in silence it reads as fine.
func TestAuditQualityCountsUnprobed(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Name":"Films","ItemId":"lib","CollectionType":"movies","Locations":["/m"]}],"TotalRecordCount":1}`)
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"TotalRecordCount": 3, "Items": []map[string]any{
			{
				"Id": "1", "Name": "Zzyzx Rip", "Type": "Movie", "Path": "/m/a.mkv", "LocationType": "FileSystem",
				"MediaSources": []map[string]any{{"Size": 5, "MediaStreams": []map[string]any{{"Type": "Video", "Codec": "h264", "Width": 720, "Height": 480}}}},
			},
			{
				"Id": "2", "Name": "Zzyzx Fine", "Type": "Movie", "Path": "/m/b.mkv", "LocationType": "FileSystem",
				"MediaSources": []map[string]any{{"Size": 5, "MediaStreams": []map[string]any{{"Type": "Video", "Codec": "h264", "Width": 1920, "Height": 1080}}}},
			},
			{"Id": "3", "Name": "Zzyzx Unprobed", "Type": "Movie", "Path": "/m/c.mkv", "LocationType": "FileSystem", "MediaSources": []map[string]any{{"Size": 0}}},
		}})
	})
	adminView(t, f)
	out := mustCall(t, session(t, f, Options{}), "audit_quality", map[string]any{"library": "Films"})
	if number(t, out["items_scanned"], "items_scanned") != 3 || number(t, out["total_findings"], "total_findings") != 1 || number(t, out["total_unprobed"], "total_unprobed") != 1 || len(objects(t, out["unprobed"], "unprobed")) != 1 {
		t.Errorf("out = %v", out)
	}
	if rows := objects(t, out["findings"], "findings"); len(rows) != 1 || text(rows[0]["name"]) != "Zzyzx Rip" {
		t.Errorf("findings = %v", rows)
	}
}

// A size is not a probe. Jellyfin gives a file it could not read - cut short
// in the copying - its size and no streams, and taking the size for a probe
// listed that file nowhere: not judged, and not among the files it could not
// judge either. Nor is a subtitle file beside it, which a server lists as a
// stream without opening the video.
func TestAuditQualityTakesNoSizeForAProbe(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Name":"Films","ItemId":"lib","CollectionType":"movies","Locations":["/m"]}],"TotalRecordCount":1}`)
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"TotalRecordCount": 3, "Items": []map[string]any{
			{
				"Id": "1", "Name": "Zzyzx Fine", "Type": "Movie", "Path": "/m/a.mkv", "LocationType": "FileSystem",
				"MediaSources": []map[string]any{{"Size": 5, "MediaStreams": []map[string]any{{"Type": "Audio", "Codec": "aac"}}}},
			},
			{"Id": "2", "Name": "Zzyzx Cut", "Type": "Movie", "Path": "/m/b.mkv", "LocationType": "FileSystem", "MediaSources": []map[string]any{{"Size": 4096, "MediaStreams": []map[string]any{}}}},
			{
				"Id": "3", "Name": "Zzyzx Subtitled", "Type": "Movie", "Path": "/m/c.mkv", "LocationType": "FileSystem",
				"MediaSources": []map[string]any{{"Size": 4096, "MediaStreams": []map[string]any{{"Type": "Subtitle", "Codec": "srt", "IsExternal": true}}}},
			},
		}})
	})
	adminView(t, f)
	out := mustCall(t, session(t, f, Options{}), "audit_quality", map[string]any{"library": "Films"})
	rows := objects(t, out["unprobed"], "unprobed")
	unprobed := make([]string, 0, len(rows))
	for _, row := range rows {
		unprobed = append(unprobed, text(row["name"]))
	}
	if !slices.Equal(unprobed, []string{"Zzyzx Cut", "Zzyzx Subtitled"}) || number(t, out["total_unprobed"], "total_unprobed") != 2 {
		t.Errorf("unprobed = %v (%v), want the cut file and the subtitled one, a size and an outside subtitle being no probe", unprobed, out["total_unprobed"])
	}
}

// A file name says which series it is from only when it separates a title
// from an episode marker. A name with no marker - an episode number and a
// title, a bare "Episode 1", a fansub's absolute number - is not a claim
// about the series, and reading its words as one reported every such file
// as from another show. A marked name for another show still is.
func TestAuditFilePathJudgesTheSeriesOnlyByAMarkedName(t *testing.T) {
	t.Parallel()

	s := &fakeSeries{id: "s1", name: "Zzyzx Show", episodes: []ep{
		{season: 1, number: 1, name: "Pilot", path: "/media/shows/Zzyzx Show/Season 01/01 - Pilot.mkv"},
		{season: 1, number: 2, name: "Second", path: "/media/shows/Zzyzx Show/Season 01/Episode 2.mkv"},
		{season: 1, number: 3, name: "Third", path: "/media/shows/Zzyzx Show/Season 01/[Grp] Zzyzx Show - 03 [1080p].mkv"},
		{season: 1, number: 4, name: "Cancer Man", path: "/media/shows/Zzyzx Show/Season 01/Breaking Bad S01E04.mkv"},
	}}
	out := mustCall(t, session(t, tvServer(t, s), Options{}), "audit_file_path", map[string]any{"library": "Shows", "checks": "series"})
	rows := objects(t, out["findings"], "findings")
	if len(rows) != 1 || !strings.Contains(text(rows[0]["path"]), "Breaking Bad S01E04") {
		t.Fatalf("findings = %v, want only the file named for another series", rows)
	}
	if ps := texts(rows[0]["problems"]); len(ps) != 1 || !strings.Contains(ps[0], `the file is named for "Breaking Bad", the server holds it under "Zzyzx Show"`) {
		t.Errorf("problems = %v", ps)
	}
}

// A server on Windows answers with backslashes, whatever the machine this
// runs on splits paths by: the file name is read the same either way, or
// every film and episode on it reports a folder path as its title.
func TestAuditFilePathReadsWindowsServerPaths(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Name":"Media","ItemId":"lib","Locations":["D:\\Media"]}],"TotalRecordCount":1}`)
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"TotalRecordCount": 4, "Items": []map[string]any{
			{"Id": "1", "Name": "Heat", "Type": "Movie", "ProductionYear": 1995, "Path": `D:\Movies\Heat (1995)\Heat (1995).mkv`},
			{"Id": "2", "Name": "Zzyzx Show", "Type": "Series", "ProductionYear": 2020, "Path": `D:\TV\Zzyzx Show (2020)`},
			{
				"Id": "3", "Name": "Pilot", "Type": "Episode", "SeriesName": "Zzyzx Show", "SeriesId": "2", "ParentIndexNumber": 1, "IndexNumber": 1,
				"Path": `D:\TV\Zzyzx Show (2020)\Season 01\Zzyzx Show S01E01 - Pilot.mkv`,
			},
			// and the real mismatch still shows through
			{
				"Id": "4", "Name": "Second", "Type": "Episode", "SeriesName": "Zzyzx Show", "SeriesId": "2", "ParentIndexNumber": 1, "IndexNumber": 2,
				"Path": `D:\TV\Zzyzx Show (2020)\Season 01\Zzyzx Show S01E03 - Second.mkv`,
			},
		}})
	})
	out := mustCall(t, session(t, f, Options{}), "audit_file_path", map[string]any{"library": "Media"})
	rows := objects(t, out["findings"], "findings")
	if len(rows) != 1 || text(rows[0]["id"]) != "4" {
		t.Fatalf("findings = %v, want only the episode numbered differently", rows)
	}
	if ps := texts(rows[0]["problems"]); len(ps) != 1 || !strings.HasPrefix(ps[0], "episode: the file says E03, the server holds E02") {
		t.Errorf("problems = %v", ps)
	}
}

// A disc's own files name nothing, so a film held as one is named by the
// folder above them: a loose DVD's VTS_01_1.VOB read as the title "VTS 01 1
// VOB" and every one was a mismatch.
func TestTitleFromPathReadsADiscByItsFolder(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/m/Zzyzx Road (1999)/VTS_01_1.VOB",
		"/m/Zzyzx Road (1999)/VIDEO_TS/VTS_01_1.VOB",
		"/m/Zzyzx Road (1999)/00000.m2ts",
		"/m/Zzyzx Road (1999)/BDMV/STREAM/00001.m2ts",
		`D:\Films\Zzyzx Road (1999)\VIDEO_TS\VTS_02_1.vob`,
	} {
		if claimed, score := naming.TitleFromPath(path, "Zzyzx Road"); claimed != "Zzyzx Road" || score < seriesConfident {
			t.Errorf("%s claims %q (%.2f), want the folder's Zzyzx Road", path, claimed, score)
		}
	}
	// a film named like a number is still its own title
	if claimed, _ := naming.TitleFromPath("/m/1917 (2019)/1917 (2019).mkv", "1917"); claimed != "1917" {
		t.Errorf("1917 claims %q", claimed)
	}
}

// A server names a film it could not match after its folder, year and all,
// and the path is read cut at its year: the year comes off both, or a short
// title ("Cube") scored under the bar against its own folder and a film was
// reported for a title nothing gets wrong.
func TestAuditFilePathReadsAnUnmatchedFilmNamedAfterItsFolder(t *testing.T) {
	t.Parallel()

	want := map[string]bool{"title": true}
	for _, it := range []embyfin.Item{
		{Type: typeMovie, Name: "Cube (1997)", Path: "/m/Cube (1997)"},
		{Type: typeMovie, Name: "Cube (1997)", Path: "/m/Cube (1997)/Cube (1997).mkv"},
		{Type: typeMovie, Name: "Cube", ProductionYear: 1997, Path: "/m/Cube (1997)/Cube (1997).mkv"},
		// an acronym with a number after its last point reads the same
		// with its dots as with the spaces the path is read with: it was
		// "alike but numbered apart" from its own folder
		{Type: typeMovie, Name: "Zzyzx Invasion - Q.R.S.1 (2017)", Path: "/m/Zzyzx Invasion - Q.R.S.1 (2017)/Zzyzx Invasion - Q.R.S.1 (2017).mp4"},
		{Type: typeMovie, Name: "Zzyzx Invasion - Q.R.S.1", ProductionYear: 2017, Path: "/m/Zzyzx Invasion - Q.R.S.1 (2017)/Zzyzx Invasion - Q.R.S.1 (2017).mp4"},
	} {
		if row, _ := checkPath(&it, want, embyfin.Emby); len(row.Problems) != 0 {
			t.Errorf("%s at %s: %v", it.Name, it.Path, row.Problems)
		}
	}
	// a film that really is another stays a finding
	if row, _ := checkPath(new(embyfin.Item{Type: typeMovie, Name: "Hypercube (2002)", Path: "/m/Cube (1997)/Cube (1997).mkv"}), want, embyfin.Emby); len(row.Problems) != 1 {
		t.Errorf("Hypercube in Cube's folder: %v", row.Problems)
	}
}

// TMDB's diagnosis reads a series' specials too: its run leaves season 0
// out, so a file named after a special was "no TMDB episode of this series
// has the file's title". And a series TMDB could not be asked about, or
// carries no TMDB id, says so on its rows: they used to come back with no
// tmdb_episode and no word why, which read as TMDB having nothing to say.
func TestAuditFilePathDiagnosisReadsSpecialsAndSaysWhatItCouldNotAsk(t *testing.T) {
	t.Parallel()

	s := severance()
	s.ids = map[string]string{"Tmdb": guideTMDBID}
	s.episodes = []ep{
		{season: 0, number: 1, name: "Zzyzx Featurette", path: "/s/Severance S00E01 - Inside The Office.mkv"},
		{season: 1, number: 1, name: "Good News About Hell", path: "/s/Severance S01E01 - Half Loop.mkv"},
	}
	unidentified := &fakeSeries{id: "z1", name: "Zzyzx Show", episodes: []ep{
		{season: 1, number: 1, name: "Pilot", path: "/z/Zzyzx Show S01E01 - Not The Pilot.mkv"},
	}}
	run := map[int][]string{0: {"Inside The Office"}, 1: {"Good News About Hell", "Half Loop"}}
	cs := session(t, tvServer(t, s, unidentified), Options{TMDBKey: "k", ProviderTransport: guideServer(t, run, aired2022)})

	byPath := func(out map[string]any) map[string]map[string]any {
		rows := map[string]map[string]any{}
		for _, r := range objects(t, out["findings"], "findings") {
			rows[mediapath.Base(text(r["path"]))] = r
		}

		return rows
	}
	out := mustCall(t, cs, "audit_file_path", map[string]any{"library": "Shows"})
	rows := byPath(out)
	if r := rows["Severance S00E01 - Inside The Office.mkv"]; text(r["tmdb_episode"]) != "S00E01" || !strings.Contains(text(r["diagnosis"]), "this very episode") {
		t.Errorf("a file named after its special = %v, want TMDB's S00E01", r)
	}
	if r := rows["Severance S01E01 - Half Loop.mkv"]; text(r["tmdb_episode"]) != "S01E02" {
		t.Errorf("a file named after another episode = %v", r)
	}
	if r := rows["Zzyzx Show S01E01 - Not The Pilot.mkv"]; r["tmdb_episode"] != nil || text(r["diagnosis"]) != "TMDB was not asked: the series carries no TMDB id" {
		t.Errorf("a series with no TMDB id = %v", r)
	}
	if out["note"] != nil {
		t.Errorf("note = %v, want none when TMDB answered", out["note"])
	}

	// TMDB down: the rows and the note say so
	down := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("down")), Header: http.Header{}, Request: r}, nil
	})
	out = mustCall(t, session(t, tvServer(t, s), Options{TMDBKey: "k", ProviderTransport: down}), "audit_file_path", map[string]any{"library": "Shows"})
	for name, r := range byPath(out) {
		if r["tmdb_episode"] != nil || !strings.HasPrefix(text(r["diagnosis"]), "TMDB could not be asked for the series' episodes") || !strings.Contains(text(r["diagnosis"]), "502") {
			t.Errorf("%s with TMDB down = %v", name, r)
		}
	}
	if note := text(out["note"]); !strings.Contains(note, "TMDB could not be asked about 2 rows") {
		t.Errorf("note with TMDB down = %q", note)
	}
}

// A title written another way is the same title, and no finding: a real
// library's folders spelled initials with the apostrophe glued to them, an
// article swapped, a number in digits, or a word in its other spelling, and
// each was reported as the wrong match or another film. A letter added or
// dropped is still another title, and so is a title that runs on past the
// item's.
func TestAuditFilePathTakesATitleWrittenAnotherWay(t *testing.T) {
	t.Parallel()

	want := map[string]bool{"title": true}
	for _, tc := range []struct {
		name, path string
		finding    bool
	}{
		// a number in digits
		{"Dune: Part Two", "/m/Dune Part 2 (2024)/Dune Part 2 (2024).mkv", false},
		// initials, the apostrophe glued to the last: "Q.X's" reads "Q Xs"
		{"Zzyzx Q.X.'s Return", "/m/Zzyzx Q.X's Return (1976)", false},
		// an article swapped in the middle, a dash for a colon
		{"Zzyzx: The New Dawn", "/m/Zzyzx - A New Dawn (1994)", false},
		// one letter of one word in a title of three words and more
		{"Zzyzx in the Gray Coat", "/m/Zzyzx in the Grey Coat (1968)", false},
		// still other titles: a letter added, a title running on, a letter
		// changed in a short title
		{"Alien", "/m/Aliens (1986)/Aliens (1986).mkv", true},
		{"Dune", "/m/Dune Part Two (2024)/Dune Part Two (2024).mkv", true},
		{"Zzyzx Cars", "/m/Zzyzx Bars (2006)", true},
		// a number is never a spelling: in digits, or a Roman numeral
		{"Zzyzx Race 2000", "/m/Zzyzx Race 2050 (2017)", true},
		{"Blade Runner 2049", "/m/Blade Runner 2048 (2017)", true},
		{"Zzyzx Fantasy VIII", "/m/Zzyzx Fantasy XIII (1999)", true},
		{"Zzyzx the 13th Part VIII", "/m/Zzyzx the 13th Part XIII (1989)", true},
		// a first letter changed is another word
		{"Bride of Zzyzx", "/m/Pride of Zzyzx (1998)", true},
		// words run together with numbers among them, and a title that is
		// only its number
		{"Zzyzx One Two", "/m/Zzyzx Twelve (2001)", true},
		{"10", "/m/Ten (2002)", true},
		// a trailing A is the title's own
		{"Zzyzx Plan", "/m/Zzyzx Plan A (2020)", true},
		// a part or a number on one side and not the other is another film,
		// however alike the rest and however near the year: the first
		// film's folder holding its second part, a year on
		{"Zzyzx and the Deathly Hallows: Part 2", "/m/Zzyzx and the Deathly Hallows (2010)/Zzyzx and the Deathly Hallows (2010).mkv", true},
		{"Zzyzx Games Mockingjay - Part 2", "/m/Zzyzx Games Mockingjay (2014)", true},
		{"Zzyzx Saga II", "/m/Zzyzx Saga (2001)", true},
		// (but a part 1 on one side alone is the first part: the same film)
		{"Zzyzx Hallows", "/m/Zzyzx Hallows Part 1 (2010)", false},
		// and the same part written another way is the same film
		{"Zzyzx Hallows: Part One", "/m/Zzyzx Hallows - Part 1 (2010)", false},
		{"Zzyzx Saga 2", "/m/Zzyzx Saga II (2002)", false},
		{"Zzyzx Saga: Part Two", "/m/Zzyzx Saga Pt. 2 (2002)", false},
		// a part 1 on one side and no part on the other is the same film
		{"Dune: Part One", "/m/Dune (2021)/Dune (2021).mkv", false},
		{"Zzyzx It Chapter One", "/m/Zzyzx It (2017)", false},
		// a closing letter the other side lacks is a numeral or a name:
		// alike, and not cleared
		{"Zzyzx Henry V", "/m/Zzyzx Henry (1989)", true},
		{"Zzyzx Malcolm X", "/m/Zzyzx Malcolm (1992)", true},
	} {
		row, _ := checkPath(&embyfin.Item{Type: typeMovie, Name: tc.name, Path: tc.path}, want, embyfin.Emby)
		if got := len(row.Problems) > 0; got != tc.finding {
			t.Errorf("%q at %s: problems %v, want a finding %v", tc.name, tc.path, row.Problems, tc.finding)
		}
		if !tc.finding && !strings.HasPrefix(row.TitleMatched, "name: the same title with") {
			t.Errorf("%q at %s: title_matched = %q, want it said how the title is written", tc.name, tc.path, row.TitleMatched)
		}
		// a closing letter apart says it can't tell
		if strings.HasSuffix(tc.name, " V") || strings.HasSuffix(tc.name, " X") {
			if len(row.Problems) == 0 || !strings.Contains(row.Problems[0], "can't tell") {
				t.Errorf("%q at %s: problems %v, want it to say it can't tell", tc.name, tc.path, row.Problems)
			}
		}
	}

	// an episode's file named with an article the title does not have
	if row, _ := checkPath(&embyfin.Item{Type: typeEpisode, Name: "Half Loop", SeriesName: "Severance", ParentIndexNumber: new(1), IndexNumber: new(2), Path: "/s/Severance S01E02 - The Half Loop.mkv"}, want, embyfin.Emby); len(row.Problems) != 0 {
		t.Errorf("an episode's file with an article added = %v", row.Problems)
	}
}

// One failure asking TMDB about one row used to switch TMDB off for every
// row after it. Each read is tried again, and a row TMDB still will not
// answer for says so on itself; the rest are asked as ever.
func TestAuditFilePathAsksTMDBRowByRow(t *testing.T) {
	t.Parallel()

	var resets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case p == "/3/movie/128/alternative_titles":
			// the connection reset once, then an answer
			if resets.Add(1) == 1 {
				conn, _, err := http.NewResponseController(w).Hijack()
				if err != nil {
					t.Error(err)

					return
				}
				if err := conn.Close(); err != nil {
					t.Error(err)
				}

				return
			}
			writeJSON(t, w, map[string]any{"id": 128, "titles": []map[string]any{{"title": "Mononoke-hime"}}})
		case strings.HasSuffix(p, "/alternative_titles"):
			writeJSON(t, w, map[string]any{"titles": []any{}})
		case p == "/3/search/movie" && r.URL.Query().Get("query") == "La llegada":
			// the search for one film's title fails on every try
			http.Error(w, "down", http.StatusBadGateway)
		case p == "/3/search/movie" && r.URL.Query().Get("query") == "Princess Mononoke":
			writeJSON(t, w, map[string]any{"results": []map[string]any{{"id": 128, "title": "Princess Mononoke", "release_date": "1997-07-12"}}})
		case p == "/3/search/movie":
			writeJSON(t, w, map[string]any{"results": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	films := []map[string]any{
		{"Id": "1", "Name": "Princess Mononoke", "Type": "Movie", "ProductionYear": 1997, "Path": "/zz/films/Mononoke-hime (1997)/Mononoke-hime (1997).mp4", "ProviderIds": map[string]any{"Tmdb": "128"}},
		{"Id": "2", "Name": "Arrival", "Type": "Movie", "ProductionYear": 2016, "Path": "/zz/films/La llegada (2016)/La llegada (2016).mp4", "ProviderIds": map[string]any{"Tmdb": "329865"}},
	}
	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(films...)) })
	out := mustCall(t, session(t, f, Options{TMDBKey: "k", ProviderTransport: rewrite{target}}), "audit_file_path", map[string]any{"library": "Zzyzx Films"})

	// Mononoke's folder is a title TMDB lists for it, read on the second try;
	// Arrival's search never answered, which its row says, and nothing else
	rows := objects(t, out["findings"], "findings")
	if len(rows) != 1 || text(rows[0]["id"]) != "2" || !strings.HasPrefix(text(rows[0]["diagnosis"]), "TMDB could not be asked what the path's title and year name") || !strings.Contains(text(rows[0]["diagnosis"]), "502") {
		t.Errorf("findings = %v, want Arrival's alone, saying TMDB could not be asked", rows)
	}
	if note := text(out["note"]); !strings.Contains(note, "TMDB could not be asked about 1 rows") || strings.Contains(note, "taken to be down") {
		t.Errorf("note = %q", note)
	}
	if n := resets.Load(); n != 2 {
		t.Errorf("Mononoke's titles were asked for %d times, want 2 (the reset, then the answer)", n)
	}
}

// With TMDB failing every read, the breaker every read of the sweep goes
// through trips after three of them - rows that made no read (a year alone,
// an answer kept from before) neither reset nor trip it - and every row after
// is said not to have been put to TMDB, the episodes' guide reads included,
// rather than each asked four times over: with a TMDB that hangs, hours.
func TestAuditFilePathStopsAskingATMDBThatIsDown(t *testing.T) {
	t.Parallel()

	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	films := make([]map[string]any, 0, 16)
	for n := range 8 {
		films = append(films,
			// a folder naming another title: TMDB is asked what it lists
			map[string]any{"Id": fmt.Sprintf("t%d", n), "Name": fmt.Sprintf("Zzyzx Title %d", n), "Type": "Movie", "ProductionYear": 2001, "Path": fmt.Sprintf("/zz/films/Quux Plugh %d (2001)/x.mkv", n), "ProviderIds": map[string]any{"Tmdb": strconv.Itoa(9000 + n)}},
			// a year alone: no TMDB read of its title before the search
			map[string]any{"Id": fmt.Sprintf("y%d", n), "Name": fmt.Sprintf("Zzyzx Year %d", n), "Type": "Movie", "ProductionYear": 1990, "Path": fmt.Sprintf("/zz/films/Zzyzx Year %d (1995)/x.mkv", n)})
	}
	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(films...)) })
	out := mustCall(t, session(t, f, Options{TMDBKey: "k", ProviderTransport: rewrite{target}}), "audit_file_path", map[string]any{"library": "Zzyzx Films"})

	// three reads failed through every try, and nothing asked after
	if n := int(reads.Load()); n != 3*tmdb.TriesPerRead() {
		t.Errorf("TMDB was asked %d times, want 3 reads of %d tries each", n, tmdb.TriesPerRead())
	}
	notAsked := 0
	for _, r := range objects(t, out["findings"], "findings") {
		if strings.Contains(text(r["diagnosis"]), "TMDB not asked: taken to be down after 3 reads in a row failed") {
			notAsked++
		}
	}
	if note := text(out["note"]); notAsked == 0 || !strings.Contains(note, fmt.Sprintf("%d rows were not put to TMDB", notAsked)) || !strings.Contains(note, "TMDB could not be asked about 3 rows") {
		t.Errorf("note = %q with %d rows not asked", note, notAsked)
	}
}

// A film of the item's own name - a remake, another film titled alike - is
// what TMDB's search finds by the name and another year, and "matched to
// another film than the one on disk" was said of a file whose runtime is the
// item's film's. The runtime settles which film the file is, and the row
// stays whichever it is: a file of the item's film sits in a folder naming
// another film, and the folder is what is wrong. Only the exact same name is
// one name: a sequel's ("Zzyzx Story 2") is another film's, and a file of the
// first film in its folder is a finding like any other.
func TestAuditFilePathTellsAFilmOfTheSameNameByItsRuntime(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query") + "|" + r.URL.Query().Get("year")
		switch p := r.URL.Path; {
		// part 1 known by its plain title too, as TMDB lists Dune: Part One
		// as "Dune": no answer to which part a folder a year off holds
		case p == "/3/movie/90041/alternative_titles":
			writeJSON(t, w, map[string]any{"titles": []map[string]any{{"title": "Zzyzx Hallows"}}})
		case p == "/3/movie/90071/alternative_titles":
			writeJSON(t, w, map[string]any{"titles": []map[string]any{{"title": "Dune"}}})
		case p == "/3/search/movie" && q == "Dune|2022":
			writeJSON(t, w, map[string]any{"results": []map[string]any{
				{"id": 90071, "title": "Dune: Part One", "original_title": "Dune: Part One", "release_date": "2021-09-15"},
			}})
		case strings.HasSuffix(p, "/alternative_titles"):
			writeJSON(t, w, map[string]any{"titles": []any{}})
		case p == "/3/search/movie" && q == "Alien|1992":
			writeJSON(t, w, map[string]any{"results": []map[string]any{{"id": 90001, "title": "Alien", "original_title": "Alien", "release_date": "1992-05-01"}}})
		case p == "/3/search/movie" && q == "Zzyzx Story 2|1999":
			writeJSON(t, w, map[string]any{"results": []map[string]any{{"id": 90003, "title": "Zzyzx Story 2", "original_title": "Zzyzx Story 2", "release_date": "1999-11-19"}}})
		// part 1 in a folder dated a year on: the part the folder's year names
		case p == "/3/search/movie" && q == "Zzyzx Hallows|2011":
			writeJSON(t, w, map[string]any{"results": []map[string]any{
				{"id": 90042, "title": "Zzyzx Hallows: Part 2", "original_title": "Zzyzx Hallows: Part 2", "release_date": "2011-07-07"},
				{"id": 90041, "title": "Zzyzx Hallows: Part 1", "original_title": "Zzyzx Hallows: Part 1", "release_date": "2010-11-17"},
			}})
		case p == "/3/search/movie" && q == "Zzyzx Bill|2004":
			writeJSON(t, w, map[string]any{"results": []map[string]any{
				{"id": 90052, "title": "Zzyzx Bill: Vol. 2", "original_title": "Zzyzx Bill: Vol. 2", "release_date": "2004-04-16"},
				{"id": 90051, "title": "Zzyzx Bill: Vol. 1", "original_title": "Zzyzx Bill: Vol. 1", "release_date": "2003-10-10"},
			}})
		case p == "/3/search/movie" && q == "Zzyzx Mockingjay|2015":
			writeJSON(t, w, map[string]any{"results": []map[string]any{
				{"id": 90061, "title": "Zzyzx Mockingjay - Part 1", "original_title": "Zzyzx Mockingjay - Part 1", "release_date": "2014-11-21"},
			}})
		case p == "/3/movie/90041":
			writeJSON(t, w, map[string]any{"id": 90041, "runtime": 160})
		case p == "/3/movie/90042":
			writeJSON(t, w, map[string]any{"id": 90042, "runtime": 130})
		case p == "/3/movie/90051":
			writeJSON(t, w, map[string]any{"id": 90051, "runtime": 111})
		case p == "/3/movie/90052":
			writeJSON(t, w, map[string]any{"id": 90052, "runtime": 137})
		case p == "/3/search/movie" && q == "Zzyzx Games Mockingjay|2014":
			// the item alone, numbered 2, which the path is not
			writeJSON(t, w, map[string]any{"results": []map[string]any{
				{"id": 90032, "title": "Zzyzx Games Mockingjay - Part 2", "original_title": "Zzyzx Games Mockingjay - Part 2", "release_date": "2015-11-20"},
			}})
		case p == "/3/search/movie" && q == "Zzyzx Dune|2021":
			writeJSON(t, w, map[string]any{"results": []map[string]any{
				{"id": 90021, "title": "Zzyzx Dune: Part One", "original_title": "Zzyzx Dune: Part One", "release_date": "2021-10-22"},
			}})
		case p == "/3/search/movie" && q == "Zzyzx and the Deathly Hallows|2010":
			// the item's own id among what a search by the first part's
			// title finds, dated within a year
			writeJSON(t, w, map[string]any{"results": []map[string]any{
				{"id": 90012, "title": "Zzyzx and the Deathly Hallows: Part 2", "original_title": "Zzyzx and the Deathly Hallows: Part 2", "release_date": "2011-07-07"},
				{"id": 90011, "title": "Zzyzx and the Deathly Hallows: Part 1", "original_title": "Zzyzx and the Deathly Hallows: Part 1", "release_date": "2010-11-17"},
			}})
		case p == "/3/search/movie":
			writeJSON(t, w, map[string]any{"results": []any{}})
		case p == "/3/movie/348":
			writeJSON(t, w, map[string]any{"id": 348, "runtime": 117})
		case p == "/3/movie/90001":
			writeJSON(t, w, map[string]any{"id": 90001, "runtime": 90})
		case p == "/3/movie/90002":
			writeJSON(t, w, map[string]any{"id": 90002, "runtime": 81})
		case p == "/3/movie/90003":
			writeJSON(t, w, map[string]any{"id": 90003, "runtime": 92})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	alien := func(id string, minutes int) map[string]any {
		return map[string]any{
			"Id": id, "Name": "Alien", "Type": "Movie", "ProductionYear": 1979, "ProviderIds": map[string]any{"Tmdb": "348"},
			"Path": "/zz/films/Alien (1992) " + id + "/Alien (1992).mp4", "RunTimeTicks": int64(minutes) * 60 * ticksPerSecond,
		}
	}
	films := []map[string]any{alien("item", 117), alien("other", 90), alien("unprobed", 0), {
		"Id": "sequel", "Name": "Zzyzx Story", "Type": "Movie", "ProductionYear": 1995, "ProviderIds": map[string]any{"Tmdb": "90002"},
		"Path": "/zz/films/Zzyzx Story 2 (1999)/Zzyzx Story 2 (1999).mp4", "RunTimeTicks": int64(81) * 60 * ticksPerSecond,
	}, {
		"Id": "part2", "Name": "Zzyzx and the Deathly Hallows: Part 2", "Type": "Movie", "ProductionYear": 2011, "ProviderIds": map[string]any{"Tmdb": "90012"},
		"Path": "/zz/films/Zzyzx and the Deathly Hallows (2010)/Zzyzx and the Deathly Hallows (2010).mp4",
	}, {
		"Id": "jay2", "Name": "Zzyzx Games Mockingjay - Part 2", "Type": "Movie", "ProductionYear": 2015, "ProviderIds": map[string]any{"Tmdb": "90032"},
		"Path": "/zz/films/Zzyzx Games Mockingjay (2014)/Zzyzx Games Mockingjay (2014).mp4",
	}, {
		"Id": "partone", "Name": "Zzyzx Dune: Part One", "Type": "Movie", "ProductionYear": 2021, "ProviderIds": map[string]any{"Tmdb": "90021"},
		"Path": "/zz/films/Zzyzx Dune (2021)/Zzyzx Dune (2021).mp4",
	}, {
		"Id": "hallows1", "Name": "Zzyzx Hallows: Part 1", "Type": "Movie", "ProductionYear": 2010, "ProviderIds": map[string]any{"Tmdb": "90041"},
		"Path": "/zz/films/Zzyzx Hallows (2011)/Zzyzx Hallows (2011).mp4", "RunTimeTicks": int64(130) * 60 * ticksPerSecond,
	}, {
		"Id": "bill1", "Name": "Zzyzx Bill: Vol. 1", "Type": "Movie", "ProductionYear": 2003, "ProviderIds": map[string]any{"Tmdb": "90051"},
		"Path": "/zz/films/Zzyzx Bill (2004)/Zzyzx Bill (2004).mp4", "RunTimeTicks": int64(111) * 60 * ticksPerSecond,
	}, {
		"Id": "jay1", "Name": "Zzyzx Mockingjay - Part 1", "Type": "Movie", "ProductionYear": 2014, "ProviderIds": map[string]any{"Tmdb": "90061"},
		"Path": "/zz/films/Zzyzx Mockingjay (2015)/Zzyzx Mockingjay (2015).mp4",
	}, {
		"Id": "dune1", "Name": "Dune: Part One", "Type": "Movie", "ProductionYear": 2021, "ProviderIds": map[string]any{"Tmdb": "90071"},
		"Path": "/zz/films/Dune (2022)/Dune (2022).mp4",
	}}
	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(films...)) })
	out := mustCall(t, session(t, f, Options{TMDBKey: "k", ProviderTransport: rewrite{target}}), "audit_file_path", map[string]any{"library": "Zzyzx Films"})

	rows := map[string]map[string]any{}
	for _, r := range objects(t, out["findings"], "findings") {
		rows[text(r["id"])] = r
	}
	if r := rows["item"]; r == nil || !strings.Contains(text(r["diagnosis"]), "another film of the item's name; the file's runtime points to the item's TMDB 348: the folder is likely what is wrong") {
		t.Errorf("a file whose runtime is the item's film's = %v, want the row kept saying the folder is wrong", r)
	}
	// the first film in its sequel's folder, running as the first film: the
	// match holds and the folder is wrong, said the one way
	if r := rows["sequel"]; r == nil || !strings.Contains(text(r["diagnosis"]), "the path names TMDB's film 90003, Zzyzx Story 2 (1999); the file's runtime points to the item's TMDB 90002: the folder is likely what is wrong") ||
		strings.Contains(text(r["diagnosis"]), "matched to another film") {
		t.Errorf("the first film in its sequel's folder = %v, want the folder said to be wrong", r)
	}
	// the second part in the first's folder: TMDB's search by the first's
	// title finding the item's own id a year on does not make the path name it
	if r := rows["part2"]; r == nil || !slices.ContainsFunc(texts(r["problems"]), func(p string) bool { return strings.HasPrefix(p, "title:") }) || strings.Contains(text(r["title_matched"]), "search finds it by") ||
		!strings.Contains(text(r["diagnosis"]), "the path names TMDB's film 90011, Zzyzx and the Deathly Hallows: Part 1 (2010)") {
		t.Errorf("the second part in the first part's folder = %v, want its title finding kept, naming the first part", r)
	}
	// the search finding the item alone, numbered otherwise than the path:
	// it found the very film, and says so
	if r := rows["jay2"]; r == nil || !strings.Contains(text(r["diagnosis"]), "TMDB's search finds this very film, TMDB 90032 Zzyzx Games Mockingjay - Part 2 (2015), numbered 2, which the path is not") ||
		strings.Contains(text(r["diagnosis"]), "finds no film") {
		t.Errorf("the item alone found, numbered apart = %v", r)
	}
	// a part 1 the folder does not name is the same film: no row at all
	if r := rows["partone"]; r != nil {
		t.Errorf("a folder naming the film without its part 1 = %v, want no finding", r)
	}
	// part 1 in a folder dated a year on: the part the folder's year names,
	// and the file's runtime says which it is
	if r := rows["hallows1"]; r == nil || !strings.Contains(text(r["diagnosis"]), "the path names TMDB's film 90042, Zzyzx Hallows: Part 2 (2011)") ||
		!strings.Contains(text(r["diagnosis"]), "it is matched to another film than the one on disk") || !strings.Contains(text(r["diagnosis"]), "the file's runtime is the path's film's") {
		t.Errorf("part 1 matched to a folder dated with part 2, running as part 2 = %v", r)
	}
	if r := rows["bill1"]; r == nil || !strings.Contains(text(r["diagnosis"]), "the path names TMDB's film 90052, Zzyzx Bill: Vol. 2 (2004); the file's runtime points to the item's TMDB 90051: the folder is likely what is wrong") {
		t.Errorf("part 1 in a folder dated with part 2, running as part 1 = %v", r)
	}
	if r := rows["jay1"]; r == nil || text(r["diagnosis"]) != "TMDB's search finds no other part of it dated 2015: can't tell whether the file is part 1 or another part filed under it" {
		t.Errorf("part 1 in a folder a year off, no other part found = %v", r)
	}
	// and a plain alternative title of part 1 settles nothing: the row stays
	if r := rows["dune1"]; r == nil || text(r["diagnosis"]) != "TMDB's search finds no other part of it dated 2022: can't tell whether the file is part 1 or another part filed under it" {
		t.Errorf("part 1 known as the plain title too, a year off = %v", r)
	}
	if r := rows["other"]; r == nil || !strings.Contains(text(r["diagnosis"]), "it is matched to another film than the one on disk") || !strings.Contains(text(r["diagnosis"]), "the file's runtime is the path's film's") {
		t.Errorf("a file whose runtime is the other film's = %v", r)
	}
	if r := rows["unprobed"]; r == nil || !strings.Contains(text(r["diagnosis"]), "a film of the same name as the item's TMDB 348: can't tell which the file is") || !strings.Contains(text(r["diagnosis"]), "never probed") {
		t.Errorf("a file with no runtime to go by = %v", r)
	}
}

// An episode file's title read as one string was a title no episode has in
// three shapes of name: a story's number in brackets and a serial's part
// after its story ("02x18 (013) - Story (3) - Part" read as "013) - The",
// cut at a word the encode's list also holds), a run numbered the NxNN way
// with titles ("02x47-48 - A & B" read as "48 - A & B"), and a time whose
// colons a renamer dropped. Each reading of the name is a title it claims.
func TestEpisodeTitlesFromFileReadEveryShape(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		path  string
		want  []string
		story string
	}{
		{"/s/Zzyzx - 02x18 (013) - The Web Zone (3) - The Last Hour.avi", []string{"The Last Hour", "The Web Zone (3) - The Last Hour", "The Web Zone (3)"}, "The Web Zone"},
		{"/s/Zzyzx - 02x47-48 - Alpha & Beta.mkv", []string{"Alpha & Beta", "Alpha", "Beta"}, ""},
		// one title with an ampersand in it, in a file of one episode
		{"/s/Zzyzx - 02x47 - Pride & Plugh.mkv", []string{"Pride & Plugh"}, ""},
		{"/s/Zzyzx - 01x01 - The DVD.mkv", []string{"The DVD"}, ""},
		{"/s/Zzyzx.S01E02.The.Web.Planet.1080p.WEB.H264-GRP.mkv", []string{"The Web Planet"}, ""},
		{"/s/Zzyzx.S01E03.Max.Power.PROPER.720p.HDTV.x264-GRP.mkv", []string{"Max Power"}, ""},
		// release words the title ends on with no encode mark after them end
		// it, and the whole is a reading too
		{"/s/Zzyzx.S01E04.Half.Loop.PROPER.mkv", []string{"Half Loop", "Half Loop PROPER"}, ""},
		{"/s/Zzyzx S01E04 Half Loop [WEB].mkv", []string{"Half Loop", "Half Loop [WEB]"}, ""},
		{"/s/Zzyzx.S01E04.Half.Loop.iNTERNAL.MULTi.mkv", []string{"Half Loop", "Half Loop iNTERNAL MULTi"}, ""},
		{"/s/Zzyzx.S01E04.Half.Loop.REPACK.DVD.mkv", []string{"Half Loop", "Half Loop REPACK DVD"}, ""},
		{"/s/Zzyzx - 01x05 - Max.mkv", []string{"Max"}, ""},
		{"/s/S01E10.mkv", nil, ""},
	} {
		got, story := naming.EpisodeTitlesFromFile(tc.path)
		if !slices.Equal(got, tc.want) || story != tc.story {
			t.Errorf("%s = %q story %q, want %q story %q", tc.path, got, story, tc.want, tc.story)
		}
	}

	want := map[string]bool{"title": true}
	for _, tc := range []struct {
		name, path string
		finding    bool
	}{
		// the server calls it by its story and part number, or by the part
		{"The Search (1)", "/s/Star Trek Deep Space Nine - 03x01 (447) - The Search (1) - Operation Zzyzx.mkv", false},
		{"Operation Zzyzx", "/s/Star Trek Deep Space Nine - 03x01 (447) - The Search (1) - Operation Zzyzx.mkv", false},
		// either title of a file holding two
		{"Alpha", "/s/Zzyzx - 02x47-48 - Alpha & Beta.mkv", false},
		{"Beta", "/s/Zzyzx - 02x47-48 - Alpha & Beta.mkv", false},
		// a time whose colons were dropped
		{"12:00 A.M.-1:00 A.M.", "/s/Zzyzx - 01x01 - 1200 A.M.-100 A.M..avi", false},
		// the story alone, where the server gives no part number
		{"The Search", "/s/Star Trek Deep Space Nine - 03x01 (447) - The Search (1) - Operation Zzyzx.mkv", false},
		// a release word the title ends on
		{"Half Loop", "/s/Zzyzx.S01E04.Half.Loop.PROPER.mkv", false},
		// and a title that is none of them is still a finding: another
		// title, part 1 filed where the server holds part 2, the first half
		// of one title with an ampersand in it
		{"Gamma", "/s/Zzyzx - 02x47-48 - Alpha & Beta.mkv", true},
		{"The Search (2)", "/s/Star Trek Deep Space Nine - 03x02 (448) - The Search (1) - Operation Zzyzx.mkv", true},
		{"Pride", "/s/Zzyzx - 02x47 - Pride & Plugh.mkv", true},
		// part 1 filed where the server holds part 2, however the server
		// writes its part: in words, abbreviated, bracketed, a numeral
		{"The Zzyzx Search Part 2", "/s/Zzyzx - 03x02 - The Zzyzx Search (1) - Operation Plugh.mkv", true},
		{"The Zzyzx Search, Part 2", "/s/Zzyzx - 03x02 - The Zzyzx Search (1) - Operation Plugh.mkv", true},
		{"The Zzyzx Search (Part 2)", "/s/Zzyzx - 03x02 - The Zzyzx Search (1) - Operation Plugh.mkv", true},
		{"The Zzyzx Search, Pt. 2", "/s/Zzyzx - 03x02 - The Zzyzx Search (1) - Operation Plugh.mkv", true},
		{"The Zzyzx Search II", "/s/Zzyzx - 03x02 - The Zzyzx Search (1) - Operation Plugh.mkv", true},
		{"The Zzyzx Search: Part Two", "/s/Zzyzx - 03x02 - The Zzyzx Search (1) - Operation Plugh.mkv", true},
		// and part 1 is part 1 however it is written; the story alone is the
		// story when the server gives no part at all
		{"The Zzyzx Search, Part 1", "/s/Zzyzx - 03x01 - The Zzyzx Search (1) - Operation Plugh.mkv", false},
		{"The Zzyzx Search (Part One)", "/s/Zzyzx - 03x01 - The Zzyzx Search (1) - Operation Plugh.mkv", false},
		{"The Zzyzx Search Pt. I", "/s/Zzyzx - 03x01 - The Zzyzx Search (1) - Operation Plugh.mkv", false},
		{"The Zzyzx Search", "/s/Zzyzx - 03x01 - The Zzyzx Search (1) - Operation Plugh.mkv", false},
		// a "(1)" against the bare title, either way round, is part 1
		{"Pilot", "/s/Zzyzx - 01x01 - Pilot (1).mkv", false},
		{"Pilot (1)", "/s/Zzyzx - 01x01 - Pilot.mkv", false},
	} {
		it := &embyfin.Item{Type: typeEpisode, Name: tc.name, SeriesName: "Zzyzx", Path: tc.path}
		file := naming.ParseSegment(mediapath.Base(tc.path))
		it.ParentIndexNumber, it.IndexNumber = new(file.Season), new(file.Episode)
		if strings.HasPrefix(mediapath.Base(tc.path), "Star Trek") {
			it.SeriesName = "Star Trek Deep Space Nine"
		}
		row, _ := checkPath(it, want, embyfin.Emby)
		if got := len(row.Problems) > 0; got != tc.finding {
			t.Errorf("%q at %s: problems %v, want a finding %v", tc.name, tc.path, row.Problems, tc.finding)
		}
	}
}

// One long-running show whose file titles all differ from the server's was a
// row for every episode, burying the few rows that said something of their
// own. Most of a show's files disagreeing the one way is one row for the
// show, listing the files it stands for; a show with a few odd files keeps
// their rows; a file named for another show stays a row of its own; a show
// whose files are all named for one other series is one row that says
// either may be wrong; and a show filed under its original title is no
// finding at all.
func TestAuditFilePathRollsUpAShowThatDisagreesOneWay(t *testing.T) {
	t.Parallel()

	var dated, odd, other, mixed, original []ep
	for n := 1; n <= 6; n++ {
		dated = append(dated, ep{season: 1, number: n, name: fmt.Sprintf("Zzyzx Name %d", n), path: fmt.Sprintf("/media/shows/Zzyzx Dated/Season 01/Zzyzx Dated S01E%02d - 2019-01-%02d.mkv", n, n)})
		title := fmt.Sprintf("Zzyzx Odd %d", n)
		if n <= 2 {
			title = fmt.Sprintf("Zzyzx Wrong %d", n)
		}
		odd = append(odd, ep{season: 1, number: n, name: fmt.Sprintf("Zzyzx Odd %d", n), path: fmt.Sprintf("/media/shows/Zzyzx Odd/Season 01/Zzyzx Odd S01E%02d - %s.mkv", n, title)})
		other = append(other, ep{season: 1, number: n, name: fmt.Sprintf("Zzyzx Other %d", n), path: fmt.Sprintf("/media/shows/Zzyzx Other/Season 01/Quux Plugh S01E%02d.mkv", n)})
		mixed = append(mixed, ep{season: 1, number: n, name: fmt.Sprintf("Zzyzx Name %d", n), path: fmt.Sprintf("/media/shows/Zzyzx Mixed/Season 01/Zzyzx Mixed S01E%02d - 2019-01-%02d.mkv", n, n)})
		original = append(original, ep{season: 1, number: n, name: fmt.Sprintf("Zzyzx Giant %d", n), path: fmt.Sprintf("/media/shows/Kyojin no Zzyzx/Season 01/Kyojin no Zzyzx S01E%02d - Zzyzx Giant %d.mkv", n, n)})
	}
	// two files of another show among a show's dated ones
	for n := 7; n <= 8; n++ {
		mixed = append(mixed, ep{season: 1, number: n, name: fmt.Sprintf("Zzyzx Name %d", n), path: fmt.Sprintf("/media/shows/Zzyzx Mixed/Season 01/Quux Plugh S01E%02d - Quux %d.mkv", n, n)})
	}
	shows := func() []*fakeSeries {
		return []*fakeSeries{
			{id: "d", name: "Zzyzx Dated", episodes: dated},
			{id: "o", name: "Zzyzx Odd", episodes: odd},
			{id: "x", name: "Zzyzx Other", episodes: other, ids: map[string]string{"Tmdb": "9100"}},
			{id: "m", name: "Zzyzx Mixed", episodes: mixed},
			{id: "g", name: "Zzyzx Giant", original: "Kyojin no Zzyzx", episodes: original, path: "/media/shows/Kyojin no Zzyzx"},
		}
	}
	bySeries := func(out map[string]any) map[string][]map[string]any {
		rows := map[string][]map[string]any{}
		for _, r := range objects(t, out["findings"], "findings") {
			rows[text(r["series"])] = append(rows[text(r["series"])], r)
		}

		return rows
	}
	datedFiles := func(show string, n int) []string {
		var files []string
		for i := 1; i <= n; i++ {
			files = append(files, fmt.Sprintf("%s S01E%02d - 2019-01-%02d.mkv", show, i, i))
		}

		return files
	}

	out := mustCall(t, session(t, tvServer(t, shows()...), Options{}), "audit_file_path", map[string]any{"library": "Shows"})
	rows := bySeries(out)
	if r := rows["Zzyzx Dated"]; len(r) != 1 || text(r[0]["type"]) != "Series" || text(r[0]["id"]) != "d" || number(t, r[0]["episodes"], "episodes") != 6 ||
		texts(r[0]["problems"])[0] != `title: 6 of the 6 episode files named with a title are named otherwise than the server holds them (the first, "2019-01-01" where the server holds "Zzyzx Name 1"): compare the files, listed in files` ||
		text(r[0]["path"]) != "/media/shows/Zzyzx Dated/Season 01" || !slices.Equal(texts(r[0]["files"]), datedFiles("Zzyzx Dated", 6)) || r[0]["diagnosis"] != nil {
		t.Errorf("a show whose titles all differ = %v, want one row for the show listing its files", r)
	}
	if r := rows["Zzyzx Odd"]; len(r) != 2 || text(r[0]["type"]) != "Episode" || text(r[1]["type"]) != "Episode" {
		t.Errorf("a show with two odd titles = %v, want their two rows", r)
	}
	if r := rows["Zzyzx Other"]; len(r) != 1 || text(r[0]["type"]) != "Series" || number(t, r[0]["episodes"], "episodes") != 6 ||
		texts(r[0]["problems"])[0] != `series: 6 of the 6 files named for a series are named for "Quux Plugh", none of the titles "Zzyzx Other" goes by (the first, Quux Plugh S01E01.mkv): the files may be another show's, or the show's match may be wrong - compare them before changing either` {
		t.Errorf("a show whose files are all named for another series = %v, want one row for the show", r)
	}
	// the other show's two files are rows of their own, not the show's row's
	if r := rows["Zzyzx Mixed"]; len(r) != 3 {
		t.Errorf("a dated show holding two other show's files = %v, want the show's row and the two files'", r)
	} else {
		var show map[string]any
		var loose []string
		for _, row := range r {
			if text(row["type"]) == "Series" {
				show = row
			} else if slices.ContainsFunc(texts(row["problems"]), func(p string) bool { return strings.HasPrefix(p, "series:") }) {
				loose = append(loose, mediapath.Base(text(row["path"])))
			}
		}
		if show == nil || number(t, show["episodes"], "episodes") != 6 || !slices.Equal(texts(show["files"]), datedFiles("Zzyzx Mixed", 6)) ||
			!strings.HasPrefix(texts(show["problems"])[0], "title: 6 of the 8 episode files named with a title") {
			t.Errorf("the dated show's row = %v, want the six dated files alone", show)
		}
		slices.Sort(loose)
		if !slices.Equal(loose, []string{"Quux Plugh S01E07 - Quux 7.mkv", "Quux Plugh S01E08 - Quux 8.mkv"}) {
			t.Errorf("the other show's files = %v, want each its own row", loose)
		}
	}
	if r := rows["Zzyzx Giant"]; len(r) != 0 {
		t.Errorf("a show filed under its original title = %v, want no finding", r)
	}
	if n := number(t, out["total_findings"], "total_findings"); n != 7 {
		t.Errorf("total_findings = %d, want the three show rows and the four files", n)
	}

	// TMDB refusing the read of the titles it lists for the series: the
	// show's row says it could not be asked, and the note counts it
	refused := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("no")), Header: http.Header{}, Request: r}, nil
	})
	out = mustCall(t, session(t, tvServer(t, shows()...), Options{TMDBKey: "k", ProviderTransport: refused}), "audit_file_path", map[string]any{"library": "Shows"})
	if r := bySeries(out)["Zzyzx Other"]; len(r) != 1 || !strings.HasPrefix(text(r[0]["diagnosis"]), "TMDB could not be asked which titles TMDB lists for the series") || !strings.Contains(text(r[0]["diagnosis"]), "401") {
		t.Errorf("the show's row with TMDB refusing = %v", r)
	}
	if note := text(out["note"]); !strings.HasPrefix(note, "TMDB could not be asked about 1 rows") {
		t.Errorf("note = %q, want the show's row counted", note)
	}
}

// A file title is looked up among a series' TMDB episode titles by an index,
// not compared with each of them: a show of thousands of episodes named by
// date was minutes of work. The index finds a title wherever comparing every
// one by naming.SameTitle does, and nowhere else.
func TestEpisodeTitlesFindWhatComparingEveryOneFinds(t *testing.T) {
	t.Parallel()

	names := []string{"Alpha", "The Alpha", "Alpha Beta", "Zzyzx Special Victims Unit", "A Zzyzx Night", "Zzyzx Night (2)", "Gamma: Part Two", "Delta", "The Delta Force", "2019-01-01", "Grey Zzyzx Coat", "Zzyzx Q.X.'s Return"}
	var eps []tmdb.Episode
	for i, n := range names {
		eps = append(eps, tmdb.Episode{Season: 1, Episode: i + 1, Name: n})
	}
	x := indexEpisodes(eps)
	for _, q := range []string{"alpha", "The Alpha", "Alpha Beta Gamma", "Zzyzx SVU", "Zzyzx Night", "zzyzx night 2", "Gamma Part 2", "Delta Force", "The Delta", "2019 01 01", "Epsilon", "", "Gray Zzyzx Coat", "Zzyzx Q.X's Return"} {
		want := slices.ContainsFunc(eps, func(e tmdb.Episode) bool { return naming.SameTitle(q, e.Name) })
		got, score, _ := x.best(q)
		if found := score >= seriesConfident; found != want || found && !naming.SameTitle(q, got.Name) {
			t.Errorf("%q: the index finds %q at %v, where comparing every one finds one: %v", q, got.Name, score, want)
		}
	}
	// a title numbered apart is never the file's: the story alone is not
	// its second part, and a second part is not the first
	if _, score, _ := indexEpisodes([]tmdb.Episode{{Season: 1, Episode: 2, Name: "Zzyzx Night (2)"}}).best("Zzyzx Night"); score >= seriesConfident {
		t.Errorf("the story alone is taken for its second part at %v", score)
	}
	if got, score, _ := x.best("Gamma Part 3"); score >= seriesConfident {
		t.Errorf("a third part is taken for %q at %v", got.Name, score)
	}
	// and a story TMDB gives with two part numbers is either
	if _, _, parts := indexEpisodes([]tmdb.Episode{{Season: 3, Episode: 1, Name: "Zzyzx Search (1)"}, {Season: 3, Episode: 2, Name: "Zzyzx Search, Part 2"}}).best("Zzyzx Search"); joinCodes(parts) != "S03E01 and S03E02" {
		t.Errorf("a story in two parts = %v, want both named", parts)
	}

	// and a long show named by date takes no time at all
	eps = eps[:0]
	for n := range 4000 {
		eps = append(eps, tmdb.Episode{Season: 1, Episode: n + 1, Name: fmt.Sprintf("Zzyzx Chapter Title %d", n)})
	}
	x = indexEpisodes(eps)
	start := time.Now()
	for n := range 4000 {
		if got, score, _ := x.best(fmt.Sprintf("2019-%02d-%02d %d", n%12+1, n%28+1, n)); score >= seriesConfident {
			t.Fatalf("a date matched %q", got.Name)
		}
		if took := time.Since(start); took > 10*time.Second {
			t.Fatalf("%d dated titles against 4,000 episodes took %v", n+1, took)
		}
	}
}

// TMDB spelling an episode's title otherwise than the file - "Part I" for
// "(1)", "12" for "Twelve" - is still its title, so the file is numbered in
// another order, not another show's. And a story TMDB gives only with part
// numbers is said to be one of those parts, not placed at either.
func TestAuditFilePathPlacesATitleTMDBSpellsOtherwise(t *testing.T) {
	t.Parallel()

	guide := []string{"Zzyzx Opening", "12 Zzyzx Men", "The Zzyzx of Both Worlds, Part I", "The Zzyzx Search (1)", "The Zzyzx Search (2)"}
	s := &fakeSeries{id: "g", name: "Zzyzx Guide", ids: map[string]string{"Tmdb": guideTMDBID}, episodes: []ep{
		{season: 1, number: 1, name: guide[0], path: "/media/shows/Zzyzx Guide/Season 01/Zzyzx Guide S01E01 - Twelve Zzyzx Men.mkv"},
		{season: 1, number: 2, name: guide[1], path: "/media/shows/Zzyzx Guide/Season 01/Zzyzx Guide S01E02 - The Zzyzx of Both Worlds (1).mkv"},
		{season: 1, number: 3, name: guide[2], path: "/media/shows/Zzyzx Guide/Season 01/Zzyzx Guide S01E03 - The Zzyzx Search.mkv"},
	}}
	out := mustCall(t, session(t, tvServer(t, s), Options{TMDBKey: "k", ProviderTransport: guideServer(t, map[int][]string{1: guide}, aired2022)}), "audit_file_path", map[string]any{"library": "Shows"})
	rows := map[int]map[string]any{}
	for _, r := range objects(t, out["findings"], "findings") {
		rows[number(t, r["episode"], "episode")] = r
	}
	if r := rows[1]; text(r["tmdb_episode"]) != "S01E02" || !strings.Contains(text(r["diagnosis"]), "numbered in another order") {
		t.Errorf("a title TMDB writes in digits = %v, want it placed at S01E02", r)
	}
	if r := rows[2]; text(r["tmdb_episode"]) != "S01E03" || !strings.Contains(text(r["diagnosis"]), "numbered in another order") {
		t.Errorf("a part TMDB writes as Part I = %v, want it placed at S01E03", r)
	}
	if r := rows[3]; r["tmdb_episode"] != nil || text(r["diagnosis"]) != "TMDB gives this title, with a part number, to S01E04 and S01E05: can't tell which the file is" {
		t.Errorf("a story TMDB gives only with part numbers = %v", r)
	}
}

// A spin-off's short alternative title ("Zzyzx Street: SU") scored as its
// parent's name with a word added, above the bar, so files named for the
// parent show read as named for the spin-off once TMDB was asked: three
// findings without a token, none with one, and a file with a title lost its
// "another series" too. Another title the series goes by must be the file's
// series name written the same way, or another way that cannot be another
// title.
func TestAuditFilePathKeepsASpinOffsFilesNamedForItsParent(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/3/tv/9200/alternative_titles" {
			_, _ = io.WriteString(w, `{"id":9200,"results":[{"iso_3166_1":"US","title":"Zzyzx Street: SU"}]}`)

			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	show := func() *fakeSeries {
		s := &fakeSeries{id: "su", name: "Zzyzx Street: Special Unit", ids: map[string]string{"Tmdb": "9200"}, path: "/media/shows/Zzyzx Street Special Unit"}
		for n := 1; n <= 3; n++ {
			s.episodes = append(s.episodes, ep{season: 1, number: n, name: fmt.Sprintf("Zzyzx Unit %d", n), path: fmt.Sprintf("/media/shows/Zzyzx Street Special Unit/Season 01/Zzyzx Street - S01E%02d.mkv", n)})
		}
		s.episodes = append(s.episodes, ep{season: 1, number: 4, name: "Zzyzx Unit 4", path: "/media/shows/Zzyzx Street Special Unit/Season 01/Zzyzx Street - S01E04 - Zzyzx Other 4.mkv"})

		return s
	}
	for _, token := range []bool{false, true} {
		opts := Options{}
		if token {
			opts = Options{TMDBKey: "k", ProviderTransport: rewrite{target}}
		}
		out := mustCall(t, session(t, tvServer(t, show()), opts), "audit_file_path", map[string]any{"library": "Shows"})
		rows := objects(t, out["findings"], "findings")
		if len(rows) != 4 {
			t.Fatalf("token %v: findings = %v, want the four files named for another series", token, rows)
		}
		for _, r := range rows {
			problems := texts(r["problems"])
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, `series: the file is named for "Zzyzx Street"`) }) {
				t.Errorf("token %v: %s = %v, want it named for another series", token, mediapath.Base(text(r["path"])), problems)
			}
			if strings.Contains(text(r["path"]), "S01E04") && !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, "title:") }) {
				t.Errorf("token %v: the file with a title = %v, want its title problem kept beside", token, problems)
			}
		}
	}
}

// What TMDB says of a show's episode titles is asked before the show's rows
// are rolled up: a file whose title TMDB gives to another number is a file
// numbered in another order, and stays a row of its own. A show where that
// leaves too few files disagreeing the one way keeps every row.
func TestAuditFilePathKeepsAFileNumberedInAnotherOrderOutOfTheShowsRow(t *testing.T) {
	t.Parallel()

	guide := []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot", "Golf", "Hotel", "India", "Juliet", "Kilo", "Lima"}
	show := func(id, name string, files int) *fakeSeries {
		s := &fakeSeries{id: id, name: name, ids: map[string]string{"Tmdb": guideTMDBID}}
		for n := 1; n <= files; n++ {
			// the first six files are named for the episode six on; the
			// rest by date, a title TMDB gives no episode
			title := fmt.Sprintf("2019-01-%02d", n)
			if n <= 6 {
				title = guide[n+5]
			}
			s.episodes = append(s.episodes, ep{season: 1, number: n, name: guide[n-1], path: fmt.Sprintf("/media/shows/%s/Season 01/%s S01E%02d - %s.mkv", name, name, n, title)})
		}

		return s
	}
	cs := session(t, tvServer(t, show("a", "Zzyzx Twelve", 12), show("b", "Zzyzx Ten", 10)), Options{TMDBKey: "k", ProviderTransport: guideServer(t, map[int][]string{1: guide}, aired2022)})
	out := mustCall(t, cs, "audit_file_path", map[string]any{"library": "Shows"})

	type tally struct{ reordered, unknown, shows int }
	got := map[string]*tally{}
	for _, r := range objects(t, out["findings"], "findings") {
		c := got[text(r["series"])]
		if c == nil {
			c = &tally{}
			got[text(r["series"])] = c
		}
		switch {
		case text(r["type"]) == "Series":
			c.shows++
			if number(t, r["episodes"], "episodes") != 6 || text(r["diagnosis"]) != "TMDB's titles match none of these files' titles as written" {
				t.Errorf("the show's row = %v, want the six dated files, none of their titles TMDB's", r)
			}
			for _, f := range texts(r["files"]) {
				if !strings.Contains(f, " - 2019-01-") {
					t.Errorf("the show's row stands for %s, a file TMDB places", f)
				}
			}
		case strings.Contains(text(r["diagnosis"]), "numbered in another order"):
			c.reordered++
		case strings.HasPrefix(text(r["diagnosis"]), "no TMDB episode"):
			c.unknown++
		default:
			t.Errorf("unexpected row %v", r)
		}
	}
	if c := got["Zzyzx Twelve"]; c == nil || *c != (tally{reordered: 6, shows: 1}) {
		t.Errorf("twelve files, six numbered in another order = %+v, want six rows of their own and one for the show", c)
	}
	if c := got["Zzyzx Ten"]; c == nil || *c != (tally{reordered: 6, unknown: 4}) {
		t.Errorf("ten files, six numbered in another order = %+v, want every row, four too few to roll up", c)
	}
}

// triesPerRead is how many times one TMDB read is sent before it fails
// (tmdb.TriesPerRead), for the tests counting what reached TMDB.
func triesPerRead() int {
	return tmdb.TriesPerRead()
}
