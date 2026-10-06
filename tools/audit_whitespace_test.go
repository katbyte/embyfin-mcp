package tools

import (
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
)

// Every problem on its own, as a name, a file name and an original title
// read it: what is wrong, the text made visible, and the text put right.
func TestWhitespaceProblems(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		text     string
		kind     spaceText
		problems []string
		visible  string
		fixed    string
	}{
		{"Zzyzx  Film", spaceName, []string{"double_space"}, "Zzyzx␣␣Film", "Zzyzx Film"},
		{"Zzyzx   Film", spaceName, []string{"double_space"}, "Zzyzx␣␣␣Film", "Zzyzx Film"},
		{" Zzyzx Film", spaceName, []string{"edge_space"}, "␣Zzyzx Film", "Zzyzx Film"},
		{"Zzyzx Film ", spaceName, []string{"edge_space"}, "Zzyzx Film␣", "Zzyzx Film"},
		{"Zzyzx : Film", spaceName, []string{"space_before_colon"}, "Zzyzx␣: Film", "Zzyzx: Film"},
		// the look-alike colon a renamer writes where a file name cannot hold
		// one, and its own spelling is no problem
		{"Zzyzx ꞉ Film", spaceName, []string{"space_before_colon"}, "Zzyzx␣꞉ Film", "Zzyzx꞉ Film"},
		{"Zzyzx꞉ Film", spaceName, nil, "Zzyzx꞉ Film", "Zzyzx꞉ Film"},
		// the spaces that are not the ordinary one, each named
		{"Zzyzx\u00a0Film", spaceName, []string{"odd_space"}, "Zzyzx[U+00A0]Film", "Zzyzx Film"},
		{"Zzyzx\tFilm", spaceName, []string{"odd_space"}, "Zzyzx[U+0009]Film", "Zzyzx Film"},
		{"Zzyzx\nFilm", spaceName, []string{"odd_space"}, "Zzyzx[U+000A]Film", "Zzyzx Film"},
		{"Zzyzx\u2009Film", spaceName, []string{"odd_space"}, "Zzyzx[U+2009]Film", "Zzyzx Film"},
		{"Zzyzx\u202fFilm", spaceName, []string{"odd_space"}, "Zzyzx[U+202F]Film", "Zzyzx Film"},
		{"Zzyzx\u205fFilm", spaceName, []string{"odd_space"}, "Zzyzx[U+205F]Film", "Zzyzx Film"},
		{"Zzyzx Film\u00a0", spaceName, []string{"odd_space", "edge_space"}, "Zzyzx Film[U+00A0]", "Zzyzx Film"},
		// the ideographic space is how Japanese titles are written
		{"進撃\u3000巨人", spaceName, nil, "進撃\u3000巨人", "進撃\u3000巨人"},
		{"\u3000進撃の巨人\u3000", spaceName, nil, "\u3000進撃の巨人\u3000", "\u3000進撃の巨人\u3000"},
		// a file name is a stem and an extension
		{"Zzyzx Film (2001) .mkv", spaceFile, []string{"space_before_extension"}, "Zzyzx Film (2001)␣.mkv", "Zzyzx Film (2001).mkv"},
		{"Zzyzx  Film (2001).mkv", spaceFile, []string{"double_space"}, "Zzyzx␣␣Film (2001).mkv", "Zzyzx Film (2001).mkv"},
		{"Zzyzx Film\u00a0.mkv", spaceFile, []string{"odd_space", "space_before_extension"}, "Zzyzx Film[U+00A0].mkv", "Zzyzx Film.mkv"},
		{" Zzyzx Film.mkv", spaceFile, []string{"edge_space"}, "␣Zzyzx Film.mkv", "Zzyzx Film.mkv"},
		{"Zzyzx Film (2001).mkv", spaceFile, nil, "Zzyzx Film (2001).mkv", "Zzyzx Film (2001).mkv"},
		// a name whose last dot starts no extension is all stem
		{"Zzyzx Vs. The World ", spaceFile, []string{"edge_space"}, "Zzyzx Vs. The World␣", "Zzyzx Vs. The World"},
		// an original title keeps its language's typography: French sets a
		// space, often a narrow no-break one, before a colon
		{"Zzyzx : Le Film", spaceForeign, nil, "Zzyzx : Le Film", "Zzyzx : Le Film"},
		{"Zzyzx\u202f: Le Film", spaceForeign, nil, "Zzyzx\u202f: Le Film", "Zzyzx\u202f: Le Film"},
		{"Zzyzx\u00a0: Le Film", spaceForeign, nil, "Zzyzx\u00a0: Le Film", "Zzyzx\u00a0: Le Film"},
		// and the rest is still out of place there
		{"Zzyzx  : Le Film", spaceForeign, []string{"double_space"}, "Zzyzx␣␣: Le Film", "Zzyzx : Le Film"},
		{"Zzyzx\t: Le Film", spaceForeign, []string{"odd_space"}, "Zzyzx[U+0009]: Le Film", "Zzyzx : Le Film"},
		{"Zzyzx : Le Film ", spaceForeign, []string{"edge_space"}, "Zzyzx : Le Film␣", "Zzyzx : Le Film"},
	} {
		if got := whitespaceProblems(tc.text, tc.kind); !slices.Equal(got, tc.problems) {
			t.Errorf("whitespaceProblems(%q, %d) = %v, want %v", tc.text, tc.kind, got, tc.problems)
		}
		if got := whitespaceVisible(tc.text, tc.kind); got != tc.visible {
			t.Errorf("whitespaceVisible(%q, %d) = %q, want %q", tc.text, tc.kind, got, tc.visible)
		}
		if got := whitespaceFixed(tc.text, tc.kind); got != tc.fixed {
			t.Errorf("whitespaceFixed(%q, %d) = %q, want %q", tc.text, tc.kind, got, tc.fixed)
		}
		// what is put right has nothing left to report
		if left := whitespaceProblems(whitespaceFixed(tc.text, tc.kind), tc.kind); len(left) != 0 {
			t.Errorf("%q put right as %q still has %v", tc.text, whitespaceFixed(tc.text, tc.kind), left)
		}
	}
}

// What a title holds where a name has two spaces: the colon a renamer
// dropped, or a word spelled in asterisks. Nothing when the words either
// side are not in the title, or the title holds another name there.
func TestDroppedAt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, title, want string }{
		{"Dune  Part Two (2024)", "Dune: Part Two", ":"},
		{"Zzyzx's  Is Here", "Zzyzx's ****** Is Here", "******"},
		{"Zzyzx  Film", "Zzyzx  Film", ""},
		{"Zzyzx  Film", "Something Else", ""},
		{"Zzyzx  Film", "Zzyzx and a whole other long title before the Film", ""},
		{"Zzyzx Film", "Zzyzx: Film", ""},
		{"  Zzyzx", "Zzyzx", ""},
	} {
		if got := droppedAt(tc.name, tc.title); got != tc.want {
			t.Errorf("droppedAt(%q, %q) = %q, want %q", tc.name, tc.title, got, tc.want)
		}
	}
}

// Which sort names were set, as each server says it: Emby lists a sort name
// as it stands and locks one an edit set; Jellyfin lists its own reading of
// the sort name and the one set apart, empty when none was.
func TestSortNameSet(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		backend embyfin.Backend
		it      embyfin.Item
		sort    string
		set     bool
	}{
		{embyfin.Emby, embyfin.Item{SortName: "Zzyzx  Words", Settings: &embyfin.ItemSettings{LockedFields: []string{"Overview", "SortName"}}}, "Zzyzx  Words", true},
		{embyfin.Emby, embyfin.Item{SortName: "Zzyzx Words"}, "Zzyzx Words", false},
		{embyfin.Jellyfin, embyfin.Item{SortName: "zzyzx  words", Settings: &embyfin.ItemSettings{ForcedSortName: "Zzyzx  Words"}}, "Zzyzx  Words", true},
		{embyfin.Jellyfin, embyfin.Item{SortName: "asterix  obelix: big fight"}, "", false},
	} {
		if sort, set := sortNameSet(&tc.it, tc.backend); sort != tc.sort || set != tc.set {
			t.Errorf("%s %+v = %q %v, want %q %v", tc.backend, tc.it, sort, set, tc.sort, tc.set)
		}
	}
}

// whitespaceLibrary is a canned server holding one library, at a folder with
// two spaces of its own, and the items given; answered in pages, and by id
// for the versions of an item held in several files.
func whitespaceLibrary(t *testing.T, jellyfin bool, items []map[string]any) *fakeServer {
	t.Helper()

	f := newFakeServer(t)
	f.jellyfin = jellyfin
	library := map[string]any{"Name": "Zzyzx", "CollectionType": "mixed", "ItemId": "lib", "Locations": []string{"/zz/my  lib"}}
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(library)) })
	f.mux.HandleFunc("GET /Library/VirtualFolders", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, []any{library}) })
	// Jellyfin's single read, where a sort name is as set
	f.mux.HandleFunc("GET /Users", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{{"Id": "u1", "Name": "Quux", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}}})
	})
	f.mux.HandleFunc("GET /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		for _, it := range items {
			if it["Id"] == r.PathValue("id") {
				writeJSON(t, w, it)

				return
			}
		}
		http.NotFound(w, r)
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var rows []map[string]any
		for _, it := range items {
			if ids := param(q, "Ids"); ids != "" && !slices.Contains(strings.Split(ids, ","), text(it["Id"])) {
				continue
			}
			if types := param(q, "IncludeItemTypes"); types != "" && !slices.Contains(strings.Split(types, ","), text(it["Type"])) {
				continue
			}
			// the items crediting a person
			if person := param(q, "PersonIds"); person != "" && !slices.ContainsFunc(objectsOf(it["People"]), func(p map[string]any) bool { return p["Id"] == person }) {
				continue
			}
			rows = append(rows, it)
		}
		start := startIndex(t, q)
		writeJSON(t, w, map[string]any{"Items": rows[min(start, len(rows)):], "TotalRecordCount": len(rows)})
	})

	return f
}

// objectsOf is a fake item's list of objects, nil when it has none.
func objectsOf(v any) []map[string]any {
	list, ok := v.([]map[string]any)
	if !ok {
		return nil
	}

	return list
}

// whitespaceItems is a library with a space out of place in every place the
// audit reads, and some that are not out of place at all. A film held in two
// files is listed as Jellyfin lists it, one item naming its second file only
// when asked by id, or as Emby does, each file an item of its own.
func whitespaceItems(jellyfin bool) []map[string]any {
	show := "/zz/my  lib/Zzyzx Show\u00a0"
	film := "/zz/my  lib/Zzyzx  Film (2001)"
	versions := []map[string]any{
		{
			"Id": "m4", "Type": "Movie", "Name": "Zzyzx Versions", "MediaSourceCount": 2, "Path": "/zz/my  lib/Zzyzx Versions (2003)/Zzyzx Versions (2003).mkv",
			"MediaSources": []map[string]any{{"Path": "/zz/my  lib/Zzyzx Versions (2003)/Zzyzx Versions (2003).mkv"}, {"Path": "/zz/my  lib/Zzyzx Versions (2003)/Zzyzx Versions (2003) - 720p .mkv"}},
		},
	}
	// sort names set by hand. Emby lists one as it stands, and locks one an
	// edit set; Jellyfin lists its own reading of it - lowercased, articles
	// and dashes dropped, numbers padded - and the one set apart. So
	// Jellyfin's listing has two spaces in a sort name set with none, and in
	// one it made from a name with none. One set with the name's own words
	// and two spaces is judged all the same
	sorted := map[string]any{"Id": "m2", "SortName": "Zzyzx  Sorted", "LockedFields": []string{"SortName"}}
	words := map[string]any{"Id": "m8", "Type": "Movie", "Name": "Zzyzx Words", "SortName": "Zzyzx  Words", "LockedFields": []string{"SortName"}, "Path": "/zz/my  lib/Zzyzx Words (2007)/Zzyzx Words (2007).mkv"}
	dashed := map[string]any{"Id": "m6", "Type": "Movie", "Name": "Zzyzx Dashed", "SortName": "Zzyzx 2 - A Film", "LockedFields": []string{"SortName"}, "Path": "/zz/my  lib/Zzyzx Dashed (2005)/Zzyzx Dashed (2005).mkv"}
	// one Emby made from the name, with a space the name has not
	extra := map[string]any{"Id": "m9", "Type": "Movie", "Name": "Zzyzx Extra", "SortName": "Zzyzx Extra ", "Path": "/zz/my  lib/Zzyzx Extra (2008)/Zzyzx Extra (2008).mkv"}
	asterix := map[string]any{"Id": "s2", "Type": "Series", "Name": "Asterix & Obelix: The Big Fight", "SortName": "Asterix & Obelix: The Big Fight", "Path": "/zz/my  lib/Asterix & Obelix - The Big Fight (2025)"}
	if jellyfin {
		sorted = map[string]any{"Id": "m2", "SortName": "zzyzx  sorted", "ForcedSortName": "Zzyzx  Sorted"}
		words["SortName"], words["ForcedSortName"] = "zzyzx  words", "Zzyzx  Words"
		dashed["SortName"], dashed["ForcedSortName"] = "zzyzx 0000000002  film", "Zzyzx 2 - A Film"
		extra["SortName"] = "zzyzx extra "
		asterix["SortName"] = "asterix  obelix: big fight"
		versions = append(versions, map[string]any{"Id": "m7", "Type": "Movie", "Name": "Zzyzx Made", "SortName": "made  zzyzx", "Path": "/zz/my  lib/Zzyzx Made (2006)/Zzyzx Made (2006).mkv"})
	} else {
		versions = []map[string]any{
			{"Id": "m4", "Type": "Movie", "Name": "Zzyzx Versions", "Path": "/zz/my  lib/Zzyzx Versions (2003)/Zzyzx Versions (2003).mkv"},
			{"Id": "m4v", "Type": "Movie", "Name": "Zzyzx Versions", "Path": "/zz/my  lib/Zzyzx Versions (2003)/Zzyzx Versions (2003) - 720p .mkv"},
		}
	}
	other := map[string]any{
		"Type": "Movie", "Name": "Zzyzx Other", "OriginalTitle": "Zzyzx\tOther", "Path": "/zz/my  lib/Zzyzx  Film (2001)/Zzyzx Other (2002).mkv",
		"Studios": []map[string]any{{"Name": "Zzyzx  Pictures"}}, "People": []map[string]any{{"Id": "p1", "Name": "Zzyzx  Person", "Type": "Actor"}},
	}
	maps.Copy(other, sorted)

	return append(versions, []map[string]any{
		// an episode met before its series: the folder is titled for the
		// series once the series comes by
		{"Id": "e1", "Type": "Episode", "Name": "Pilot ", "SortName": "Pilot ", "SeriesName": "Zzyzx Show", "ParentIndexNumber": 1, "IndexNumber": 1, "Path": show + "/Season 01/Zzyzx Show S01E01 .mkv"},
		{"Id": "e2", "Type": "Episode", "Name": "Title : Subtitle", "SortName": "Title : Subtitle", "SeriesName": "Zzyzx Show", "ParentIndexNumber": 1, "IndexNumber": 2, "Path": show + "/Season 01/Zzyzx Show S01E02.mkv"},
		{"Id": "se1", "Type": "Season", "Name": "Season 1", "SortName": "0001", "Path": show + "/Season 01"},
		{"Id": "s1", "Type": "Series", "Name": "Zzyzx Show", "SortName": "zzyzx show", "Path": show},
		// a French original title; a sort name the server made from the name
		{
			"Id": "m1", "Type": "Movie", "Name": "Zzyzx  Film", "SortName": "Zzyzx  Film", "OriginalTitle": "Zzyzx : Le Film", "Path": film + "/Zzyzx  Film (2001) .mkv",
			"Genres": []string{"Science  Fiction"}, "TagItems": []map[string]any{{"Name": "Zzyzx\u00a0Tag"}}, "Tags": []string{"Zzyzx\u00a0Tag"}, "Studios": []map[string]any{{"Name": "Zzyzx  Pictures"}},
			"People": []map[string]any{{"Id": "p1", "Name": "Zzyzx  Person", "Type": "Director"}, {"Id": "p1", "Name": "Zzyzx  Person", "Type": "Writer"}},
		},
		// a sort name set by hand, and an original title with a tab in it,
		// in the same folder as the film before it
		other,
		dashed,
		words,
		extra,
		// the ideographic space, as Japanese titles are written
		{"Id": "m3", "Type": "Movie", "Name": "進撃\u3000巨人", "Path": "/zz/my  lib/進撃\u3000巨人/進撃\u3000巨人.mkv"},
		// what Jellyfin makes of a name as its sort name
		asterix,
		// a folder that lost its colon
		{"Id": "m5", "Type": "Movie", "Name": "Dune: Part Two", "Path": "/zz/my  lib/Dune  Part Two (2024)/Dune Part Two (2024).mkv"},
		// music: a track's album folder, and an artist with no folder
		{"Id": "a1", "Type": "Audio", "Name": "Zzyzx Song", "Path": "/zz/my  lib/Zzyzx Artist/Zzyzx  Album/01 - Zzyzx Song.mp3"},
		{"Id": "ar1", "Type": "MusicArtist", "Name": "Zzyzx Artist "},
		// the people the server lists: one credited, one whose name is
		// clean, and one no item of the library credits
		{"Id": "p1", "Type": "Person", "Name": "Zzyzx  Person"},
		{"Id": "p2", "Type": "Person", "Name": "Zzyzx Clean"},
		{"Id": "p3", "Type": "Person", "Name": "Zzyzx  Nobody"},
	}...)
}

// audit_whitespace through the tool: every place a name is read, each
// problem once, a shared value or folder once with how many carry it, and
// nothing reported for the library's own folder, the ideographic space, an
// original title's French colon or a sort name the server made.
func TestAuditWhitespace(t *testing.T) {
	t.Parallel()

	for _, jellyfin := range []bool{false, true} {
		f := whitespaceLibrary(t, jellyfin, whitespaceItems(jellyfin))
		items, version, found, edges, sorts := 16, "m4v", 21, 4, 3
		if jellyfin {
			version, found, edges, sorts = "m4", 20, 3, 2
		}
		out := mustCall(t, session(t, f, Options{}), "audit_whitespace", map[string]any{})
		if number(t, out["items_scanned"], "items_scanned") != items || number(t, out["people_scanned"], "people_scanned") != 3 || number(t, out["total_findings"], "total_findings") != found {
			t.Fatalf("jellyfin %v: out = %v", jellyfin, out)
		}
		counts, where := object(t, out["counts"], "counts"), object(t, out["by_where"], "by_where")
		for k, want := range map[string]int{"odd_space": 3, "double_space": 10, "edge_space": edges, "space_before_extension": 3, "space_before_colon": 1} {
			if got := number(t, counts[k], k); got != want {
				t.Errorf("counts.%s = %d, want %d", k, got, want)
			}
		}
		for k, want := range map[string]int{"name": 4, "sort_name": sorts, "original_title": 1, "genre": 1, "tag": 1, "studio": 1, "person": 1, "folder": 5, "file": 4} {
			if got := number(t, where[k], k); got != want {
				t.Errorf("by_where.%s = %d, want %d", k, got, want)
			}
		}

		rows := objects(t, out["findings"], "findings")
		// by where, then by problem
		var order []string
		for _, r := range rows {
			order = append(order, text(r["where"]))
		}
		if !slices.IsSortedFunc(order, func(a, b string) int {
			return slices.Index(whitespaceWhereOrder, a) - slices.Index(whitespaceWhereOrder, b)
		}) {
			t.Errorf("rows are not by where: %v", order)
		}
		find := func(where, problem, path string) map[string]any {
			t.Helper()
			for _, r := range rows {
				if text(r["where"]) == where && text(r["problem"]) == problem && (path == "" || text(r["path"]) == path) {
					return r
				}
			}
			t.Errorf("jellyfin %v: no %s %s row at %q in %v", jellyfin, where, problem, path, rows)

			return map[string]any{}
		}
		check := func(row map[string]any, want map[string]any) {
			t.Helper()
			for k, v := range want {
				if n, isInt := v.(int); isInt {
					if row[k] == nil || number(t, row[k], k) != n {
						t.Errorf("%s = %v, want %d, in %v", k, row[k], n, row)
					}
				} else if text(row[k]) != v {
					t.Errorf("%s = %q, want %q, in %v", k, text(row[k]), v, row)
				}
			}
		}

		check(find("name", "edge_space", "/zz/my  lib/Zzyzx Show\u00a0/Season 01/Zzyzx Show S01E01 .mkv"), map[string]any{"text": "Pilot␣", "suggest": "Pilot", "id": "e1", "fix": whitespaceFixes["name"]})
		check(find("name", "space_before_colon", ""), map[string]any{"text": "Title␣: Subtitle", "suggest": "Title: Subtitle", "id": "e2"})
		check(find("name", "double_space", ""), map[string]any{"text": "Zzyzx␣␣Film", "suggest": "Zzyzx Film", "id": "m1"})
		check(find("name", "edge_space", ""), map[string]any{})
		sorted := map[string]map[string]any{}
		for _, r := range rows {
			if text(r["where"]) == "sort_name" {
				sorted[text(r["id"])] = r
			}
		}
		check(sorted["m2"], map[string]any{"problem": "double_space", "text": "Zzyzx␣␣Sorted", "value": "Zzyzx  Sorted", "suggest": "Zzyzx Sorted", "fix": whitespaceFixes["sort_name"]})
		// set with the name's own words, and still judged
		check(sorted["m8"], map[string]any{"problem": "double_space", "text": "Zzyzx␣␣Words"})
		// one Emby made from the name, with a space the name has not; on
		// Jellyfin, where none was set, its own reading is not judged
		if m9, judged := sorted["m9"]; judged == jellyfin {
			t.Errorf("jellyfin %v: the sort name made from the name, with a space it has not = %v", jellyfin, m9)
		} else if !jellyfin {
			check(m9, map[string]any{"problem": "edge_space", "text": "Zzyzx Extra␣"})
		}
		check(find("original_title", "odd_space", ""), map[string]any{"text": "Zzyzx[U+0009]Other", "suggest": "Zzyzx Other", "id": "m2", "fix": whitespaceFixes["original_title"]})
		check(find("genre", "double_space", ""), map[string]any{"text": "Science␣␣Fiction", "value": "Science  Fiction", "suggest": "Science Fiction", "items": 1, "fix": whitespaceFixes["genre"]})
		check(find("tag", "odd_space", ""), map[string]any{"text": "Zzyzx[U+00A0]Tag", "suggest": "Zzyzx Tag", "items": 1})
		check(find("studio", "double_space", ""), map[string]any{"text": "Zzyzx␣␣Pictures", "items": 2, "fix": whitespaceFixes["studio"]})
		// credited twice on one film and once on another: two items
		person := find("person", "double_space", "")
		check(person, map[string]any{"text": "Zzyzx␣␣Person", "value": "Zzyzx  Person", "items": 2, "id": "p1", "title": "Zzyzx  Film"})
		if jellyfin != strings.HasPrefix(text(person["fix"]), "not item_edit") {
			t.Errorf("jellyfin %v: a person's fix = %q", jellyfin, person["fix"])
		}

		// the show's folder: every item in it counted, titled for the series
		show := "/zz/my  lib/Zzyzx Show\u00a0"
		check(find("folder", "odd_space", show), map[string]any{"text": "Zzyzx Show[U+00A0]", "value": "Zzyzx Show\u00a0", "suggest": "Zzyzx Show", "items": 4, "id": "s1", "title": "Zzyzx Show", "fix": renamedOnDisk})
		check(find("folder", "edge_space", show), map[string]any{"items": 4, "id": "s1"})
		// two films' folder, titled for the first met
		check(find("folder", "double_space", "/zz/my  lib/Zzyzx  Film (2001)"), map[string]any{"text": "Zzyzx␣␣Film (2001)", "value": "Zzyzx  Film (2001)", "suggest": "Zzyzx Film (2001)", "items": 2, "id": "m1", "dropped": ""})
		// the colon a renamer dropped
		check(find("folder", "double_space", "/zz/my  lib/Dune  Part Two (2024)"), map[string]any{"title": "Dune: Part Two", "dropped": ":", "suggest": "Dune Part Two (2024)"})
		check(find("folder", "double_space", "/zz/my  lib/Zzyzx Artist/Zzyzx  Album"), map[string]any{"items": 1, "id": "a1"})

		check(find("file", "space_before_extension", "/zz/my  lib/Zzyzx Show\u00a0/Season 01/Zzyzx Show S01E01 .mkv"), map[string]any{"text": "Zzyzx Show S01E01␣.mkv", "suggest": "Zzyzx Show S01E01.mkv", "title": "Zzyzx Show S01E01 Pilot ", "fix": renamedOnDisk})
		check(find("file", "double_space", "/zz/my  lib/Zzyzx  Film (2001)/Zzyzx  Film (2001) .mkv"), map[string]any{"text": "Zzyzx␣␣Film (2001)␣.mkv", "suggest": "Zzyzx Film (2001).mkv"})
		check(find("file", "space_before_extension", "/zz/my  lib/Zzyzx  Film (2001)/Zzyzx  Film (2001) .mkv"), map[string]any{"id": "m1"})
		// the second file of a film in two, named by its own path
		check(find("file", "space_before_extension", "/zz/my  lib/Zzyzx Versions (2003)/Zzyzx Versions (2003) - 720p .mkv"), map[string]any{"id": version, "text": "Zzyzx Versions (2003) - 720p␣.mkv"})

		for _, r := range rows {
			switch {
			case strings.Contains(text(r["text"]), "my␣␣lib") || text(r["path"]) == "/zz/my  lib":
				t.Errorf("the library's own folder was reported: %v", r)
			case strings.Contains(text(r["text"]), "進撃"):
				t.Errorf("the ideographic space was reported: %v", r)
			case text(r["id"]) == "s2" || text(r["id"]) == "m6" || text(r["id"]) == "m7":
				t.Errorf("a sort name the server made, or one set with no fault, was reported: %v", r)
			case text(r["id"]) == "p3":
				t.Errorf("a person no item of the library credits was reported: %v", r)
			case text(r["where"]) == "original_title" && text(r["id"]) == "m1":
				t.Errorf("a French original title's colon was reported: %v", r)
			}
		}

		// file names alone, and a limit that caps the rows and not the count
		files := mustCall(t, session(t, f, Options{}), "audit_whitespace", map[string]any{"where": "file", "limit": 2})
		if number(t, files["total_findings"], "total_findings") != 4 || len(objects(t, files["findings"], "findings")) != 2 {
			t.Errorf("where=file limit 2 = %v", files)
		}
		for _, r := range objects(t, files["findings"], "findings") {
			if text(r["where"]) != "file" {
				t.Errorf("where=file gave %v", r)
			}
		}
		if refusal := mustRefuse(t, session(t, f, Options{}), "audit_whitespace", map[string]any{"where": "title"}); !strings.Contains(refusal, "where must be among") {
			t.Errorf("an unknown where = %q", refusal)
		}

		// audit_all counts every place but the file names, and says how
		// many of those there are
		row, err := whitespaceAllRow(t.Context(), f.client(t), "")
		if err != nil {
			t.Fatal(err)
		}
		if row.Findings != found-4 || row.Scanned != items || !strings.Contains(row.Note, "3 files with one in their name (4 rows") || row.Where != "name,sort_name,original_title,genre,tag,studio,person,folder" {
			t.Errorf("audit_all's row = %+v, want %d findings and the 3 files, 4 rows, in the note", row, found-4)
		}
	}
}

// A person's fix on each server, as the live suites found them: Emby renames
// a person in place, Jellyfin takes a renamed one off every item.
func TestPersonFix(t *testing.T) {
	t.Parallel()

	if fix := personFix(embyfin.Emby); !strings.HasPrefix(fix, "item_edit name=") {
		t.Errorf("Emby = %q", fix)
	}
	if fix := personFix(embyfin.Jellyfin); !strings.HasPrefix(fix, "not item_edit") {
		t.Errorf("Jellyfin = %q", fix)
	}
}
