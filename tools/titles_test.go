package tools

import (
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// What a film's path names, against every title the item goes by. A folder
// in the film's own language, a year either side of the item's, a disc's
// streams under a folder named for the film, a file named for nothing: none
// is another film. A title the item goes by under no name, or a year two
// off, is.
func TestOtherFilm(t *testing.T) {
	t.Parallel()

	mononoke := embyfin.Item{Type: typeMovie, Name: "Princess Mononoke", OriginalTitle: "もののけ姫", ProductionYear: 1997}
	interstellar := embyfin.Item{Type: typeMovie, Name: "Interstellar", ProductionYear: 2014}
	for _, tc := range []struct {
		name  string
		it    embyfin.Item
		path  string
		other bool
		says  string
	}{
		{"the folder names the original title", mononoke, "/m/もののけ姫 (1997)/もののけ姫 (1997).mkv", false, ""},
		{"the name", mononoke, "/m/Princess Mononoke (1997)/Princess Mononoke (1997).mkv", false, ""},
		{"a year one after", mononoke, "/m/Princess Mononoke (1998)/Princess Mononoke (1998).mkv", false, ""},
		{"a year one before", mononoke, "/m/Princess Mononoke (1996)/Princess Mononoke (1996).mkv", false, ""},
		{"a year two off", mononoke, "/m/Princess Mononoke (1999)/Princess Mononoke (1999).mkv", true, `named for "Princess Mononoke" (1999), not Princess Mononoke (1997)`},
		{"another title", interstellar, "/m/The Thirteenth Floor (1999)/The Thirteenth Floor (1999).mp4", true, `"The Thirteenth Floor (1999).mp4" is named for "The Thirteenth Floor" (1999), not Interstellar (2014)`},
		{"another title, the year alike", interstellar, "/m/Arrival (2014)/Arrival (2014).mp4", true, `named for "Arrival" (2014)`},
		{"a scene name", interstellar, "/m/Interstellar.2014.1080p.BluRay.x264-GRP.mkv", false, ""},
		{"a version's label after the year", interstellar, "/m/Interstellar (2014)/Interstellar (2014) - 2160p.mkv", false, ""},
		{"a file named for nothing, under the film's folder", interstellar, "/m/Interstellar (2014)/movie.mkv", false, ""},
		{"a file named for nothing, under another film's folder", interstellar, "/m/Arrival (2016)/movie.mkv", true, `"Arrival (2016)" is named for "Arrival" (2016)`},
		{"a file named for nothing, in no film's folder", interstellar, "/m/Films/movie.mkv", false, ""},
		{"a disc's streams", embyfin.Item{Type: typeMovie, Name: "Coyote vs. Acme", ProductionYear: 2026}, "/m/Coyote vs. Acme (2026)/VIDEO_TS/VTS_01_1.VOB", false, ""},
		{"a Blu-ray kept whole", embyfin.Item{Type: typeMovie, Name: "Cube", ProductionYear: 1997}, "/m/Cube (1997)/BDMV/STREAM/00001.m2ts", false, ""},
		{"a film the server could not match, named after its folder", embyfin.Item{Type: typeMovie, Name: "Cube (1997)"}, "/m/Cube (1997)", false, ""},
		{"the sort name", embyfin.Item{Type: typeMovie, Name: "Dune: Part Two", SortName: "Dune 2", ProductionYear: 2024}, "/m/Dune 2 (2024)/Dune 2 (2024).mkv", false, ""},
		{"an episode is never judged here", embyfin.Item{Type: typeEpisode, Name: "Pilot", ProductionYear: 2008}, "/s/Severance (2022)/Season 01/Severance (2022) S01E01.mkv", false, ""},
	} {
		why, other := otherFilm(&tc.it, tc.path)
		if other != tc.other || !strings.Contains(why, tc.says) {
			t.Errorf("%s: other %v %q, want %v %q", tc.name, other, why, tc.other, tc.says)
		}
	}
}

// A runtime far apart is a tell, not a finding: more than a tenth of the
// longer and more than a minute.
func TestRuntimesApart(t *testing.T) {
	t.Parallel()

	minutes := func(m int64) int64 { return m * 60 * ticksPerSecond }
	for _, tc := range []struct {
		a, b  int64
		apart bool
	}{
		{minutes(96), minutes(81), true},
		{minutes(117), minutes(117), false},
		{minutes(100), minutes(105), false}, // an encode a few minutes apart
		{ticksPerSecond, 200 * ticksPerSecond, true},
		{ticksPerSecond, 50 * ticksPerSecond, false}, // under a minute apart
		{0, minutes(100), false},                     // unknown is not apart
	} {
		if got := runtimesApart(tc.a, tc.b); got != tc.apart {
			t.Errorf("%d against %d = %v, want %v", tc.a, tc.b, got, tc.apart)
		}
	}
}

// A film shown in two files, one of them named for another film, is two
// films on one id, said plainly with the runtimes; two versions of one film
// are nothing to warn about, and a runtime far apart alone is not either (a
// director's cut runs longer).
func TestVersionWarning(t *testing.T) {
	t.Parallel()

	source := func(path string, seconds int64) embyfin.MediaSource {
		return embyfin.MediaSource{Path: path, RunTimeTicks: seconds * ticksPerSecond}
	}
	merged := embyfin.Item{Type: typeMovie, Name: "Interstellar", ProductionYear: 2014, MediaSources: []embyfin.MediaSource{
		source("/m/Interstellar (2014)/Interstellar (2014).mp4", 169*60),
		source("/m/The Thirteenth Floor (1999)/The Thirteenth Floor (1999).mp4", 100*60),
	}}
	w := versionWarning(&merged)
	for _, want := range []string{"probably not one film", `"The Thirteenth Floor (1999).mp4" is named for "The Thirteenth Floor" (1999), not Interstellar (2014)`, "run 169 min and 100 min", "item_identify"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning = %q, want %q in it", w, want)
		}
	}

	versions := embyfin.Item{Type: typeMovie, Name: "Blade Runner", ProductionYear: 1982, MediaSources: []embyfin.MediaSource{
		source("/m/Blade Runner (1982)/Blade Runner (1982) - 1080p.mp4", 117*60),
		source("/m/Blade Runner (1982)/Blade Runner (1982) - 2160p.mp4", 117*60),
	}}
	if w := versionWarning(&versions); w != "" {
		t.Errorf("two versions of one film = %q", w)
	}
	cut := embyfin.Item{Type: typeMovie, Name: "Alien", ProductionYear: 1979, MediaSources: []embyfin.MediaSource{
		source("/m/Alien (1979)/Alien (1979).mp4", 117*60),
		source("/m/Alien (1979) Directors Cut/Alien (1979) Directors Cut.mp4", 136*60),
	}}
	if w := versionWarning(&cut); w != "" {
		t.Errorf("a director's cut beside the film = %q", w)
	}

	// one file, as Jellyfin holds a film matched to another's ids
	if w := versionWarning(new(embyfin.Item{Type: typeMovie, Name: "Interstellar", ProductionYear: 2014, Path: "/m/The Thirteenth Floor (1999)/The Thirteenth Floor (1999).mp4"})); !strings.HasPrefix(w, "may be a different film matched to this one's ids") {
		t.Errorf("one file named for another film = %q", w)
	}
}

// Entries sharing an id whose files name different films are different
// films, not copies: every member of the group says so.
func TestDuplicateWarning(t *testing.T) {
	t.Parallel()

	group := []embyfin.Item{
		{Type: typeMovie, Name: "Interstellar", ProductionYear: 2014, Path: "/m/Interstellar (2014)/Interstellar (2014).mp4", RunTimeTicks: 169 * 60 * ticksPerSecond},
		{Type: typeMovie, Name: "Interstellar", ProductionYear: 2014, Path: "/m/The Thirteenth Floor (1999)/The Thirteenth Floor (1999).mp4", RunTimeTicks: 100 * 60 * ticksPerSecond},
	}
	w := duplicateWarning(group)
	for _, want := range []string{"probably not copies of one film", "The Thirteenth Floor", "the entries run 169 min and 100 min"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning = %q, want %q in it", w, want)
		}
	}
	copies := []embyfin.Item{
		{Type: typeMovie, Name: "Alien", ProductionYear: 1979, Path: "/m/Alien (1979)/Alien (1979).mp4"},
		{Type: typeMovie, Name: "Alien", ProductionYear: 1979, Path: "/m/Alien (1979) Directors Cut/Alien (1979) Directors Cut.mp4"},
	}
	if w := duplicateWarning(copies); w != "" {
		t.Errorf("two copies of one film = %q", w)
	}
}

// Two items compared may be two films: a caveat, never a verdict.
func TestNotOneFilm(t *testing.T) {
	t.Parallel()

	film := func(name string, year int, path string, minutes int64) *embyfin.Item {
		return &embyfin.Item{Type: typeMovie, Name: name, ProductionYear: year, Path: path, RunTimeTicks: minutes * 60 * ticksPerSecond}
	}
	clean := film("Arrival", 2016, "/m/Arrival (2016)/Arrival (2016).mp4", 116)
	for _, tc := range []struct {
		name string
		a, b *embyfin.Item
		want string // "" is no caveat
	}{
		{"two copies of one film", clean, film("Arrival", 2016, "/r/Arrival (2016)/Arrival (2016).mp4", 116), ""},
		{"a side given as numbers", clean, nil, ""},
		{"a file named for another film", clean, film("Arrival", 2016, "/r/Memento (2000)/Memento (2000).mp4", 113), "these may not be the same film: \"Memento (2000).mp4\" is named for \"Memento\""},
		{"two films of one name", film("Dune", 1984, "/m/Dune (1984)/Dune (1984).mp4", 0), film("Dune", 2021, "/m/Dune (2021)/Dune (2021).mp4", 0), "dated 1984 and 2021"},
		{"two titles", clean, film("Alien", 2016, "/m/Alien (2016)/Alien (2016).mp4", 116), `the items are "Arrival" and "Alien"`},
		{"runtimes far apart", clean, film("Arrival", 2016, "/r/Arrival (2016)/Arrival (2016).mp4", 150), "they run 116 min and 150 min: a different cut, or a different film"},
	} {
		c := notOneFilm(tc.a, tc.b)
		if (tc.want == "" && c != "") || !strings.Contains(c, tc.want) {
			t.Errorf("%s = %q, want %q", tc.name, c, tc.want)
		}
	}
}

// A letter of another script that only looks Latin, inside a Latin title, is
// named with the plain spelling; a title written in that script, or a Greek
// letter used as a symbol, is left alone.
func TestLookalikeLetters(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		title, plain string
		letters      []string
	}{
		{"\u0410rrival", "Arrival", []string{"the Cyrillic \u0410 (U+0410) in place of the Latin A"}},
		{"S\u0435verance", "Severance", []string{"the Cyrillic \u0435 (U+0435) in place of the Latin e"}},
		{"\u0421ub\u0435", "Cube", []string{"the Cyrillic \u0421 (U+0421) in place of the Latin C", "the Cyrillic \u0435 (U+0435) in place of the Latin e"}},
		{"\u0391lien", "Alien", []string{"the Greek \u0391 (U+0391) in place of the Latin A"}},
		// Latin letters only
		{"Arrival", "Arrival", nil},
		// no Latin letter at all: written in that script
		{"もののけ姫", "もののけ姫", nil},
		{"\u0414\u044E\u043D\u0430", "\u0414\u044E\u043D\u0430", nil},
		// Dune's title in Russian, and a title in both scripts whose Cyrillic
		// is its own: its De (U+0414) and Yu (U+044E) look like no Latin letter
		{"\u0414\u044E\u043D\u0430 (Dune)", "\u0414\u044E\u043D\u0430 (Dune)", nil},
		// a Greek letter used as a symbol makes the Greek its own
		{"Arrival \u0394", "Arrival \u0394", nil},
	} {
		letters, plain := lookalikeLetters(tc.title)
		if plain != tc.plain || strings.Join(letters, "|") != strings.Join(tc.letters, "|") {
			t.Errorf("%q = %q %v, want %q %v", tc.title, plain, letters, tc.plain, tc.letters)
		}
	}
}

// The titles an item goes by, each saying which, the year a server leaves on
// an unmatched film's name taken off, and a lookalike name spelled plainly.
func TestKnownTitles(t *testing.T) {
	t.Parallel()

	got := knownTitles(&embyfin.Item{Name: "\u0421ube (1997)", OriginalTitle: "Cube", SortName: "Cube"})
	want := []knownTitle{{"Cube", titleAsName}, {"Cube", titleAsOriginal}, {"Cube", titleAsSort}}
	if len(got) != len(want) {
		t.Fatalf("known titles = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("title %d = %v, want %v", i, got[i], want[i])
		}
	}
	if got := knownTitles(&embyfin.Item{Name: "Alien"}); len(got) != 1 {
		t.Errorf("an item with a name alone = %v", got)
	}
}

// titlesTMDB is a canned TMDB for audit_file_path: one film's alternative
// titles, what the search finds by a few titles and years, and runtimes.
func titlesTMDB(t *testing.T) http.RoundTripper {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer := func(body string) { _, _ = io.WriteString(w, body) }
		switch p := r.URL.Path; {
		case p == "/3/movie/128/alternative_titles":
			answer(`{"id":128,"titles":[{"iso_3166_1":"JP","title":"Mononoke-hime","type":"romaji"}]}`)
		case strings.HasSuffix(p, "/alternative_titles"):
			answer(`{"titles":[]}`)
		case p == "/3/search/movie":
			switch r.URL.Query().Get("query") + "|" + r.URL.Query().Get("year") {
			case "The Thirteenth Floor|1999":
				answer(`{"results":[{"id":1090,"title":"The Thirteenth Floor","original_title":"The Thirteenth Floor","release_date":"1999-04-16"}]}`)
			case "La llegada|2016": // Arrival's Spanish title: found as the film itself
				answer(`{"results":[{"id":329865,"title":"Arrival","original_title":"Arrival","release_date":"2016-11-10"}]}`)
			case "Dune|2021":
				answer(`{"results":[{"id":438631,"title":"Dune","original_title":"Dune","release_date":"2021-09-15"}]}`)
			case "Interstellar|2014":
				answer(`{"results":[{"id":157336,"title":"Interstellar","original_title":"Interstellar","release_date":"2014-11-05"}]}`)
			case "もののけ姫|1997", "Princess Mononoke|1997":
				answer(`{"results":[{"id":128,"title":"Princess Mononoke","original_title":"もののけ姫","release_date":"1997-07-12"}]}`)
			default:
				answer(`{"results":[]}`)
			}
		case strings.HasPrefix(p, "/3/movie/"):
			runtimes := map[int]int{1090: 100, 157336: 169, 438631: 155, 841: 137}
			id, _ := strconv.Atoi(strings.TrimPrefix(p, "/3/movie/"))
			if runtimes[id] == 0 {
				http.NotFound(w, r)
				return
			}
			writeJSON(t, w, map[string]any{"id": id, "runtime": runtimes[id]})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	return rewrite{target}
}

// A film's path against every title it goes by, and TMDB asked what a path
// naming none of them does name: a folder in the film's own language, one
// named by a title TMDB lists for the film or finds it by, and one dated a
// year off are the film; one naming another film says which, beside the id
// the item carries, with the file's runtime against both; one naming nothing
// TMDB knows says so, and that a file never probed cannot settle it. A name
// with a letter that only looks Latin is its own row. And a film renamed by
// hand to another film's title, its folder and its id still its own: TMDB
// finds the film itself by the path, and the name is what is wrong.
func TestAuditFilePathTitles(t *testing.T) {
	t.Parallel()

	film := func(id, name string, year int, tmdb, path string, minutes int) map[string]any {
		it := map[string]any{"Id": id, "Name": name, "Type": "Movie", "ProductionYear": year, "Path": path, "RunTimeTicks": int64(minutes) * 60 * ticksPerSecond}
		if tmdb != "" {
			it["ProviderIds"] = map[string]any{"Tmdb": tmdb}
		}
		return it
	}
	mononoke := film("1", "Princess Mononoke", 1997, "128", "/m/もののけ姫 (1997)/もののけ姫 (1997).mp4", 134)
	mononoke["OriginalTitle"] = "もののけ姫"
	// the same folder under a year edited wrong: TMDB finds the film itself
	// by the path, so it is the item's year to check
	misdated := maps.Clone(mononoke)
	misdated["Id"], misdated["ProductionYear"] = "9", 1999
	films := []map[string]any{
		mononoke,
		misdated,
		film("2", "Princess Mononoke", 1997, "128", "/m/Mononoke-hime (1997)/Mononoke-hime (1997).mp4", 134),
		film("3", "Interstellar", 2014, "157336", "/m/The Thirteenth Floor (1999)/The Thirteenth Floor (1999).mp4", 100),
		film("4", "Memento", 2000, "77", "/m/Coyote vs. Acme (2026)/Coyote vs. Acme (2026).iso", 0),
		film("5", "Arrival", 2016, "329865", "/m/La llegada (2016)/La llegada (2016).mp4", 116),
		film("6", "Dune", 1984, "841", "/m/Dune (2021)/Dune (2021).mp4", 137),
		film("7", "\u0421ube", 1997, "431", "/m/Cube (1997)/Cube (1997).mp4", 90),
		film("8", "Blade Runner", 1982, "78", "/m/Blade Runner (1983)/Blade Runner (1983).mp4", 117),
		film("10", "Arrival", 2014, "157336", "/m/Interstellar (2014)/Interstellar (2014).mp4", 169),
		// and one renamed whose folder is a title TMDB lists for its id
		film("11", "Zzyzx Wrong", 1997, "128", "/m/Mononoke-hime (1997)/Mononoke-hime (1997).mp4", 134),
	}
	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		rows := films
		if ids := param(r.URL.Query(), "Ids"); ids != "" {
			rows = slices.DeleteFunc(slices.Clone(films), func(it map[string]any) bool { return !slices.Contains(strings.Split(ids, ","), text(it["Id"])) })
		}
		writeJSON(t, w, page(rows...))
	})

	cs := session(t, f, Options{TMDBKey: "k", ProviderTransport: titlesTMDB(t)})
	out := mustCall(t, cs, "audit_file_path", map[string]any{"library": "Zzyzx Films"})
	rows := map[string]map[string]any{}
	for _, r := range objects(t, out["findings"], "findings") {
		rows[text(r["id"])] = r
	}
	if len(rows) != 7 || rows["3"] == nil || rows["4"] == nil || rows["6"] == nil || rows["7"] == nil || rows["9"] == nil || rows["10"] == nil || rows["11"] == nil {
		t.Fatalf("findings = %v, want Interstellar, Memento, Dune, the lookalike Cube, the misdated Mononoke and the two renamed", slices.Collect(maps.Keys(rows)))
	}
	if r := rows["11"]; text(r["item_tmdb"]) != "128" || !strings.Contains(text(r["diagnosis"]), `the path's title is one TMDB lists for the item's TMDB 128, and the name the server holds, "Zzyzx Wrong", is none TMDB gives it`) {
		t.Errorf("a film renamed, in a folder named by a title TMDB lists for it = %v", r)
	}
	if r := rows["10"]; !slices.Equal(texts(r["problems"]), []string{`title: the path is named "Interstellar", the server holds "Arrival": the path names the item's own title, and the name is none TMDB gives it`}) || text(r["item_tmdb"]) != "157336" || r["path_tmdb"] != nil ||
		!strings.Contains(text(r["diagnosis"]), `TMDB's search finds this very film, TMDB 157336 Interstellar (2014), by the path's title and year, and the name the server holds, "Arrival", is none TMDB gives it: the path is right and the name is what is wrong`) {
		t.Errorf("a film renamed to another's title = %v", r)
	}
	if r := rows["9"]; !slices.Equal(texts(r["problems"]), []string{"year: path says 1997, metadata says 1999"}) || text(r["item_tmdb"]) != "128" || r["path_tmdb"] != nil ||
		!strings.Contains(text(r["diagnosis"]), "TMDB's search finds this very film, TMDB 128, by the path's title and year: the path names it, and the year the item holds is the one to check") {
		t.Errorf("a year edited wrong = %v", r)
	}
	if r := rows["3"]; text(r["path_tmdb"]) != "1090 The Thirteenth Floor (1999)" || text(r["item_tmdb"]) != "157336" ||
		!strings.Contains(text(r["diagnosis"]), "the path names TMDB's film 1090, The Thirteenth Floor (1999); the item carries TMDB 157336") ||
		!strings.Contains(text(r["diagnosis"]), "the file runs 100 min, and TMDB's 1090 runs 100 and its 157336 169: the file's runtime is the path's film's") {
		t.Errorf("a folder naming another film = %v", r)
	}
	if r := rows["4"]; r["path_tmdb"] != nil || !strings.Contains(text(r["diagnosis"]), "TMDB's search finds no film by the path's title and year") || !strings.Contains(text(r["diagnosis"]), "never probed") {
		t.Errorf("a folder naming nothing TMDB knows, over a file never probed = %v", r)
	}
	if r := rows["6"]; text(r["path_tmdb"]) != "438631 Dune (2021)" || !strings.Contains(text(r["diagnosis"]), "the file's runtime is the item's film's") || !slices.Equal(texts(r["problems"]), []string{"year: path says 2021, metadata says 1984"}) {
		t.Errorf("a folder dated for another film of the name = %v", r)
	}
	if r := rows["7"]; len(texts(r["problems"])) != 1 || !strings.HasPrefix(texts(r["problems"])[0], "lookalike: \"\u0421ube\" is spelled with the Cyrillic \u0421 (U+0421) in place of the Latin C") || !strings.Contains(texts(r["problems"])[0], `item_edit name "Cube"`) {
		t.Errorf("a lookalike name = %v", r)
	}
	if n := number(t, out["total_findings"], "total_findings"); n != 7 {
		t.Errorf("total_findings = %d, want 7", n)
	}

	// without a token, a title TMDB knows the film by is a finding like any
	// other: the lookups are what clear them
	plain := mustCall(t, session(t, f, Options{}), "audit_file_path", map[string]any{"library": "Zzyzx Films"})
	plainRows := objects(t, plain["findings"], "findings")
	ids := make([]string, 0, len(plainRows))
	for _, r := range plainRows {
		ids = append(ids, text(r["id"]))
		if r["diagnosis"] != nil || r["path_tmdb"] != nil {
			t.Errorf("a diagnosis with no TMDB token: %v", r)
		}
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"10", "11", "2", "3", "4", "5", "6", "7", "9"}) {
		t.Errorf("without TMDB = %v, want the two named by titles TMDB knows as well", ids)
	}

	// a handful by id, without a sweep
	f.reset()
	out = mustCall(t, cs, "audit_file_path", map[string]any{"ids": []any{"3", "8"}})
	if n := number(t, out["items_scanned"], "items_scanned"); n != 2 || len(objects(t, out["findings"], "findings")) != 1 {
		t.Errorf("ids 3 and 8 = %v", out)
	}
	if q := lastQuery(t, f, "/Items"); q.Get("Ids") != "3,8" || q.Has("ParentId") {
		t.Errorf("the lookup by id = %v", q)
	}
	if msg := mustRefuse(t, cs, "audit_file_path", map[string]any{"ids": []any{"3"}, "library": "Zzyzx Films"}); !strings.Contains(msg, "library or ids") {
		t.Errorf("library and ids = %q", msg)
	}
}

// Jellyfin holds a film's versions as one item whose path is one of them, and
// says how many it holds; the others' files are read and checked too, each
// on a row of its own. Emby answers each version as an item of its own, so
// its sweep reads every file already.
func TestAuditFilePathReadsEveryJellyfinVersion(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.jellyfin = true
	f.mux.HandleFunc("GET /Library/VirtualFolders", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{{"Name": "Films", "CollectionType": "movies", "ItemId": "lib", "Locations": []string{"/m"}}})
	})
	dir := "/m/Blade Runner (1982)/"
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		it := map[string]any{"Id": "br", "Name": "Blade Runner", "Type": "Movie", "ProductionYear": 1982, "Path": dir + "Blade Runner (1982) - 2160p.mp4", "MediaSourceCount": 2}
		if param(r.URL.Query(), "Ids") == "br" {
			if !strings.Contains(param(r.URL.Query(), "Fields"), "MediaSources") {
				t.Errorf("the versions were read without their files: %v", r.URL.Query())
			}
			it["MediaSources"] = []map[string]any{
				{"Id": "br", "Path": dir + "Blade Runner (1982) - 2160p.mp4"},
				{"Id": "br2", "Path": dir + "Blade Runner (1992).mp4"},
			}
		}
		writeJSON(t, w, page(it))
	})
	out := mustCall(t, session(t, f, Options{}), "audit_file_path", map[string]any{"library": "Films"})
	rows := objects(t, out["findings"], "findings")
	if len(rows) != 1 || rows[0]["id"] != "br" || text(rows[0]["path"]) != dir+"Blade Runner (1992).mp4" || !slices.Equal(texts(rows[0]["problems"]), []string{"year: path says 1992, metadata says 1982"}) {
		t.Errorf("findings = %v, want the second version's file alone", rows)
	}
	first, err := url.ParseQuery(f.requests("/Items")[0].Query)
	if err != nil {
		t.Fatal(err)
	}
	if q := param(first, "fields"); !strings.Contains(q, "MediaSourceCount") {
		t.Errorf("the sweep did not ask how many versions each item holds: %s", q)
	}
}
