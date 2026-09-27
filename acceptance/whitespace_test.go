//go:build integration

package acceptance

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// wsRow is one audit_whitespace row as a test compares it.
type wsRow struct{ where, problem, text string }

// wsRows are an audit_whitespace answer's rows, and the answer.
func wsRows(t *testing.T, args map[string]any) ([]map[string]any, map[string]any) {
	t.Helper()

	out := call(t, "audit_whitespace", args)
	found := rows(t, out["findings"], "findings")
	if n := num(t, out["total_findings"], "total_findings"); n != len(found) {
		t.Fatalf("audit_whitespace %v lists %d of %d rows: raise the limit", args, len(found), n)
	}

	return found, out
}

// credited are the names an item credits, as item_get lists them.
func credited(t *testing.T, id string) []string {
	t.Helper()

	var names []string
	// no one credited is no people at all in the answer
	for _, p := range rowsOf(call(t, "item_get", map[string]any{"id": id})["people"]) {
		names = append(names, str(p["name"]))
	}

	return names
}

// wsFind is the row of one place, problem and text, nil when there is none.
func wsFind(found []map[string]any, want wsRow) map[string]any {
	for _, r := range found {
		if str(r["where"]) == want.where && str(r["problem"]) == want.problem && strings.EqualFold(str(r["text"]), want.text) {
			return r
		}
	}

	return nil
}

// The fixtures carry one space out of place of their own: the Knight pair's
// renamed folder has two in a row, and the show built from it is named with
// them. Everything else the libraries hold is clean - including the sort
// names Jellyfin makes from names with an ampersand or a dash, which hold
// two spaces where it drops one ("asterix  obelix: big fight"), and which
// are the server's reading of the name rather than a sort name anyone typed.
func TestAuditWhitespaceLeavesTheFixturesAlone(t *testing.T) {
	for _, library := range []string{"Movies", "Shows", "Music", "Messy Movies"} {
		if found, out := wsRows(t, map[string]any{"library": library}); len(found) != 0 || num(t, out["items_scanned"], "items_scanned") == 0 {
			t.Errorf("%s = %v, want its items read and nothing out of place", library, out)
		}
	}
	found, _ := wsRows(t, map[string]any{"library": "Messy Shows"})
	folder := wsFind(found, wsRow{"folder", "double_space", "A Knight of the Seven␣␣kingdoms (2026)"})
	name := wsFind(found, wsRow{"name", "double_space", "A Knight of the Seven␣␣kingdoms"})
	if len(found) != 2 || folder == nil || name == nil {
		t.Fatalf("Messy Shows = %v, want the Knight pair's folder and its name", found)
	}
	// the series, its season and its episode sit in the folder
	if num(t, folder["items"], "items") != 3 || str(folder["title"]) != "A Knight of the Seven  kingdoms" || str(folder["path"]) != "/media/messy-shows/A Knight of the Seven  kingdoms (2026)" ||
		str(folder["suggest"]) != "A Knight of the Seven kingdoms (2026)" || !strings.HasPrefix(str(folder["fix"]), "rename it on disk") {
		t.Errorf("the Knight folder's row = %v", folder)
	}
	if str(name["suggest"]) != "A Knight of the Seven kingdoms" || !strings.HasPrefix(str(name["fix"]), "item_edit name=") {
		t.Errorf("the Knight name's row = %v", name)
	}
}

// Folders and files laid out with spaces out of place, and names, sort
// names and genres edited to carry them: each is reported where it is, once
// for what many items share, beside the item's own title, and put right by
// the fix the row names. The staged files are copies of the fixtures under
// new names: two spaces where a renamer dropped a colon and a dash, a space
// before the extension, a non-breaking space, a trailing space on a folder,
// and an nfo naming a director and a studio with two spaces in them.
func TestAuditWhitespaceFindsWhatIsLaidOut(t *testing.T) {
	video := fixtureVideo(t, "messy-movies", messyArrival, messyArrival+".mp4")
	starWars := "messy-movies/Star Wars Episode IV  A New Hope (1977)"
	nfo := func(title string, year int, extra string) []byte {
		return fmt.Appendf(nil, "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<movie>\n  <title>%s</title>\n  <year>%d</year>\n%s</movie>\n", title, year, extra)
	}
	stage(t, plus(5, 0, 2), map[string][]byte{
		"messy-movies/The Thirteenth  Floor (1999)/The Thirteenth  Floor (1999).mp4": video,
		"messy-movies/Aliens (1986)/Aliens (1986) .mp4":                              video,
		"messy-movies/Aliens (1986)/Aliens (1986) .nfo":                              nfo("Aliens", 1986, "  <director>James  Cameron</director>\n  <studio>Legendary  Pictures</studio>\n"),
		"messy-movies/Dune\u00a0Part Two (2024)/Dune\u00a0Part Two (2024).mp4":       video,
		"messy-movies/Limitless (2011) /Limitless (2011).mp4":                        video,
		starWars + "/Star Wars Episode IV  A New Hope (1977).mp4":                    video,
		starWars + "/Star Wars Episode IV  A New Hope (1977).nfo":                    nfo("Star Wars: Episode IV - A New Hope", 1977, ""),
		"messy-shows/Severance/Season 01/Severance S01E07 .mp4":                      video,
		"messy-shows/Severance/Season 02 /Severance S02E01.mp4":                      video,
	}, "messy-movies/The Thirteenth  Floor (1999)", "messy-movies/Aliens (1986)", "messy-movies/Dune\u00a0Part Two (2024)", "messy-movies/Limitless (2011) ", starWars, "messy-shows/Severance/Season 02 ")

	movies := map[string]any{"library": "Messy Movies", "limit": 200}
	found, out := wsRows(t, movies)
	must := func(want wsRow) map[string]any {
		t.Helper()
		r := wsFind(found, want)
		if r == nil {
			t.Fatalf("no %s %s row for %q in %v", want.where, want.problem, want.text, found)
		}
		return r
	}

	// a name read from its folder keeps the folder's spaces; Jellyfin keeps
	// an unmatched film's year in its name, and the trailing space after it,
	// where Emby trims both
	year := func(s string) string {
		if isJellyfin() {
			return s + " (1999)"
		}
		return s
	}
	must(wsRow{"name", "double_space", year("The Thirteenth␣␣Floor")})
	dune := "Dune[U+00A0]Part Two"
	if isJellyfin() {
		dune += " (2024)"
	}
	must(wsRow{"name", "odd_space", dune})
	if limitless := wsFind(found, wsRow{"name", "edge_space", "Limitless (2011)␣"}); isJellyfin() != (limitless != nil) {
		t.Errorf("Limitless' name with its folder's trailing space = %v on %s", limitless, backend)
	}

	// every folder once, beside the film it was named for
	thirteenth := must(wsRow{"folder", "double_space", "The Thirteenth␣␣Floor (1999)"})
	if num(t, thirteenth["items"], "items") != 1 || str(thirteenth["path"]) != "/media/messy-movies/The Thirteenth  Floor (1999)" || str(thirteenth["dropped"]) != "" {
		t.Errorf("the Thirteenth Floor folder = %v", thirteenth)
	}
	must(wsRow{"folder", "odd_space", "Dune[U+00A0]Part Two (2024)"})
	must(wsRow{"folder", "edge_space", "Limitless (2011)␣"})
	// the gap where a renamer dropped the title's dash, said beside it
	sw := must(wsRow{"folder", "double_space", "Star Wars Episode IV␣␣A New Hope (1977)"})
	if str(sw["title"]) != "Star Wars: Episode IV - A New Hope" || str(sw["dropped"]) != "-" || str(sw["suggest"]) != "Star Wars Episode IV A New Hope (1977)" {
		t.Errorf("the Star Wars folder = %v, want its title beside it and the dash it lost", sw)
	}
	// and a file's name, one row a problem
	must(wsRow{"file", "double_space", "The Thirteenth␣␣Floor (1999).mp4"})
	aliens := must(wsRow{"file", "space_before_extension", "Aliens (1986)␣.mp4"})
	if str(aliens["suggest"]) != "Aliens (1986).mp4" || str(aliens["title"]) != "Aliens" || str(aliens["path"]) != "/media/messy-movies/Aliens (1986)/Aliens (1986) .mp4" {
		t.Errorf("the Aliens file = %v", aliens)
	}
	must(wsRow{"file", "odd_space", "Dune[U+00A0]Part Two (2024).mp4"})
	if r := must(wsRow{"file", "double_space", "Star Wars Episode IV␣␣A New Hope (1977).mp4"}); str(r["dropped"]) != "-" {
		t.Errorf("the Star Wars file = %v", r)
	}

	// the nfo's director and studio, each once with the film carrying it
	studio := must(wsRow{"studio", "double_space", "Legendary␣␣Pictures"})
	if num(t, studio["items"], "items") != 1 || str(studio["suggest"]) != "Legendary Pictures" || !strings.HasPrefix(str(studio["fix"]), "metadata_rename field=studios") {
		t.Errorf("the studio = %v", studio)
	}
	person := must(wsRow{"person", "double_space", "James␣␣Cameron"})
	if num(t, person["items"], "items") != 1 || str(person["title"]) != "Aliens" || str(person["id"]) == "" {
		t.Errorf("the director = %v", person)
	}
	if fix := str(person["fix"]); isJellyfin() != strings.HasPrefix(fix, "not item_edit") {
		t.Errorf("a person's fix on %s = %q", backend, fix)
	}
	byWhere := object(t, out["by_where"], "by_where")
	if files := num(t, byWhere["file"], "by_where.file"); files != 4 {
		t.Errorf("file rows = %d, want the four staged film files: %v", files, found)
	}

	// file names alone
	files, _ := wsRows(t, map[string]any{"library": "Messy Movies", "where": "file"})
	if len(files) != 4 {
		t.Errorf("where=file = %v", files)
	}
	for _, r := range files {
		if str(r["where"]) != "file" {
			t.Errorf("where=file gave %v", r)
		}
	}
	// audit_all counts every place but the files, and says how many those are
	for _, row := range rows(t, call(t, "audit_all", map[string]any{"library": "Messy Movies"})["audits"], "audits") {
		if str(row["audit"]) != "audit_whitespace" {
			continue
		}
		if num(t, row["findings"], "findings") != len(found)-4 || !strings.Contains(str(row["note"]), "4 files with one in their name (4 rows") {
			t.Errorf("audit_all's row = %v, want %d and the 4 files in its note", row, len(found)-4)
		}
	}

	// the show: an episode named for its file, its file, and a season folder
	// with a trailing space, which holds the season and its episode
	shows, _ := wsRows(t, map[string]any{"library": "Messy Shows"})
	for _, want := range []wsRow{
		{"name", "edge_space", "Severance S01E07␣"},
		{"file", "space_before_extension", "Severance S01E07␣.mp4"},
	} {
		if wsFind(shows, want) == nil {
			t.Errorf("no %v in %v", want, shows)
		}
	}
	if season := wsFind(shows, wsRow{"folder", "edge_space", "Season 02␣"}); season == nil || num(t, season["items"], "items") != 2 || str(season["title"]) != "Season 2" {
		t.Errorf("the season folder = %v, want its season and episode in it, titled for the season", season)
	}

	// a name and a sort name edited to carry two spaces, then put right by
	// the fix each row names
	sw2 := findItem(t, "Messy Movies", "Movie", "Star Wars: Episode IV - A New Hope")
	call(t, "item_edit", map[string]any{"ids": []any{sw2}, "name": "Star Wars  Episode IV - A New Hope", "sort_name": "Star Wars 4  A New Hope", "add_genres": []any{"Science  Fiction"}})
	found, _ = wsRows(t, movies)
	named := wsFind(found, wsRow{"name", "double_space", "Star Wars␣␣Episode IV - A New Hope"})
	sorted := wsFind(found, wsRow{"sort_name", "double_space", "Star Wars 4␣␣A New Hope"})
	genre := wsFind(found, wsRow{"genre", "double_space", "Science␣␣Fiction"})
	if named == nil || sorted == nil || genre == nil || str(named["id"]) != sw2 || str(sorted["id"]) != sw2 {
		t.Fatalf("after the edit: name %v, sort name %v, genre %v", named, sorted, genre)
	}
	call(t, "item_edit", map[string]any{"ids": []any{sw2}, "name": str(named["suggest"]), "sort_name": str(sorted["suggest"])})
	call(t, "metadata_rename", map[string]any{"field": "genres", "from": "Science  Fiction", "to": str(genre["suggest"]), "library": "Messy Movies"})
	found, _ = wsRows(t, movies)
	for _, gone := range []wsRow{
		{"name", "double_space", "Star Wars␣␣Episode IV - A New Hope"},
		{"sort_name", "double_space", "Star Wars 4␣␣A New Hope"},
		{"genre", "double_space", "Science␣␣Fiction"},
	} {
		if r := wsFind(found, gone); r != nil {
			t.Errorf("put right by its fix, still %v", r)
		}
	}
	if got := call(t, "item_get", map[string]any{"id": sw2}); str(got["name"]) != "Star Wars Episode IV - A New Hope" || !slices.Contains(strs(t, got["genres"], "genres"), "Science Fiction") {
		t.Errorf("after the fixes the film is %v with %v", got["name"], got["genres"])
	}
}

// A person's name is put right differently on each server, and the row's
// fix says how. Emby renames a person in place and every item keeps them.
// Jellyfin credits a person by name: renamed, they drop off every item
// crediting them until the old name is back. There the fix says to put the
// name right in the nfo and refresh the item, then check the credit: with
// TMDB answering about the person (recorded here) the refresh takes the new
// name, and without it the old credit was seen to stay.
func TestAuditWhitespacePersonFix(t *testing.T) {
	folder := "messy-movies/Aliens (1986)"
	nfo := func(director string) []byte {
		return fmt.Appendf(nil, "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<movie>\n  <title>Aliens</title>\n  <year>1986</year>\n  <director>%s</director>\n</movie>\n", director)
	}
	stage(t, plus(1, 0, 0), map[string][]byte{
		folder + "/Aliens (1986).mp4": fixtureVideo(t, "messy-movies", messyArrival, messyArrival+".mp4"),
		folder + "/Aliens (1986).nfo": nfo("James  Cameron"),
	}, folder)
	aliens := findItem(t, "Messy Movies", "Movie", "Aliens")
	movies := map[string]any{"library": "Messy Movies", "where": "person"}
	found, _ := wsRows(t, movies)
	row := wsFind(found, wsRow{"person", "double_space", "James␣␣Cameron"})
	if row == nil || len(found) != 1 || !slices.Equal(credited(t, aliens), []string{"James  Cameron"}) {
		t.Fatalf("people = %v, want the nfo's director, credited on the film", found)
	}
	person := str(row["id"])

	if !isJellyfin() {
		putBack(t, "item_edit", map[string]any{"ids": []any{person}, "name": "James  Cameron"})
		call(t, "item_edit", map[string]any{"ids": []any{person}, "name": str(row["suggest"])})
		if !eventually(func() bool { return slices.Equal(credited(t, aliens), []string{"James Cameron"}) }) {
			t.Errorf("after renaming the person the film credits %v, want James Cameron", credited(t, aliens))
		}
		if found, _ := wsRows(t, movies); len(found) != 0 {
			t.Errorf("after the fix: %v", found)
		}

		return
	}

	// what the fix warns of: renamed, the person is off the film
	call(t, "item_edit", map[string]any{"ids": []any{person}, "name": str(row["suggest"])})
	lost := credited(t, aliens)
	call(t, "item_edit", map[string]any{"ids": []any{person}, "name": "James  Cameron"})
	if len(lost) != 0 {
		t.Errorf("renamed on Jellyfin the person is still credited as %v: the fix's warning is stale", lost)
	}
	if !eventually(func() bool { return slices.Equal(credited(t, aliens), []string{"James  Cameron"}) }) {
		t.Errorf("with the name put back the film credits %v", credited(t, aliens))
	}
	// and what it says to do: the name put right in the nfo, the film
	// refreshed, the credit checked
	mediaWrite(t, hostPath("/media/"+folder+"/Aliens (1986).nfo"), nfo("James Cameron"))
	if out := call(t, "item_refresh", map[string]any{"id": aliens}); !boolOf(out["landed"]) {
		t.Fatalf("the refresh never landed: %v", out)
	}
	if !eventually(func() bool { return slices.Equal(credited(t, aliens), []string{"James Cameron"}) }) {
		t.Errorf("refreshed with the nfo put right the film credits %v, want James Cameron", credited(t, aliens))
	}
	if found, _ := wsRows(t, movies); len(found) != 0 {
		t.Errorf("after the fix: %v", found)
	}
}
