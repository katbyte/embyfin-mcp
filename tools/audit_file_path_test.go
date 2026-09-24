package tools

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
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
	out := mustCall(t, session(t, f, Options{}), "audit_quality", map[string]any{"library": "Films"})
	if number(t, out["items_scanned"], "items_scanned") != 3 || number(t, out["total_findings"], "total_findings") != 1 || number(t, out["total_unprobed"], "total_unprobed") != 1 || len(objects(t, out["unprobed"], "unprobed")) != 1 {
		t.Errorf("out = %v", out)
	}
	if rows := objects(t, out["findings"], "findings"); len(rows) != 1 || text(rows[0]["name"]) != "Zzyzx Rip" {
		t.Errorf("findings = %v", rows)
	}
}
