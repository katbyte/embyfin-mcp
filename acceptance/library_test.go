//go:build integration

package acceptance

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLibraryList(t *testing.T) {
	out := call(t, "library_list", nil)
	got := map[string]map[string]any{}
	for _, row := range rows(t, out["libraries"], "libraries") {
		got[str(row["name"])] = row
	}
	for _, l := range libraries {
		row, ok := got[l.Name]
		if !ok {
			t.Errorf("library %s missing from %v", l.Name, out["libraries"])
			continue
		}
		if str(row["collection_type"]) != l.Type {
			t.Errorf("%s type = %v, want %s", l.Name, row["collection_type"], l.Type)
		}
		if locs := strs(t, row["locations"], "locations"); !slices.Contains(locs, l.Folder) {
			t.Errorf("%s locations = %v, want %s", l.Name, locs, l.Folder)
		}
		if str(row["id"]) == "" {
			t.Errorf("%s has no id", l.Name)
		}
	}
}

func TestLibraryGet(t *testing.T) {
	// by name, case-insensitively
	out := call(t, "library_get", map[string]any{"library": "movies"})
	if str(out["name"]) != "Movies" || str(out["collection_type"]) != "movies" {
		t.Errorf("library_get = %v", out)
	}
	if got := num(t, out["item_count"], "item_count"); got < 8 {
		t.Errorf("Movies item_count = %d, want at least 8", got)
	}
	counts, _ := out["type_counts"].(map[string]any)
	if num(t, counts["Movie"], "type_counts.Movie") != 8 {
		t.Errorf("type_counts = %v", counts)
	}

	// by id
	byID := call(t, "library_get", map[string]any{"library": str(out["id"])})
	if str(byID["name"]) != "Movies" {
		t.Errorf("by id = %v", byID)
	}

	shows := call(t, "library_get", map[string]any{"library": "Shows"})
	counts, _ = shows["type_counts"].(map[string]any)
	if num(t, counts["Series"], "Series") != 3 || num(t, counts["Episode"], "Episode") < 9 {
		t.Errorf("Shows type_counts = %v", counts)
	}

	if msg := callErr(t, "library_get", map[string]any{"library": "Nope"}); !strings.Contains(msg, "Nope") || !strings.Contains(msg, "Movies") {
		t.Errorf("an unknown library should list the real ones: %s", msg)
	}
	if msg := callErr(t, "library_get", nil); !strings.Contains(msg, "required") {
		t.Errorf("no library: %s", msg)
	}
}

func TestLibrarySearch(t *testing.T) {
	// a title, across every library: the clean Alien and the two messy ones
	out := call(t, "library_items", map[string]any{"query": "Alien", "types": "Movie", "limit": 20})
	items := rows(t, out["items"], "items")
	var alien, aliens int
	for _, it := range items {
		switch str(it["name"]) {
		case "Alien":
			alien++
		case "Aliens":
			aliens++
		}
		if str(it["type"]) != "Movie" {
			t.Errorf("types=Movie returned a %v", it["type"])
		}
	}
	if alien != 3 || aliens != 1 {
		t.Errorf("Alien x%d Aliens x%d, want 3 and 1: %v", alien, aliens, items)
	}
	if num(t, out["total"], "total") < 4 {
		t.Errorf("total = %v", out["total"])
	}

	// restricted to one library, the type defaults to the library's kind
	out = call(t, "library_items", map[string]any{"query": "Alien", "library": "Movies"})
	items = rows(t, out["items"], "items")
	if len(items) != 2 {
		t.Fatalf("Movies has %d Alien matches, want Alien and Aliens: %v", len(items), items)
	}
	for _, it := range items {
		if !strings.HasPrefix(str(it["path"]), "/media/movies/") {
			t.Errorf("a result from outside Movies: %v", it["path"])
		}
		// the summary carries the quality facts the scan probed
		if str(it["video_codec"]) == "" || str(it["container"]) == "" || num(t, it["year"], "year") == 0 {
			t.Errorf("summary lacks quality facts: %v", it)
		}
		ids, _ := it["metadata_provider_ids"].(map[string]any)
		if str(ids["tmdb"]) == "" {
			t.Errorf("no tmdb id on %v", it["name"])
		}
	}

	// a TV library defaults to series
	out = call(t, "library_items", map[string]any{"library": "Shows"})
	for _, it := range rows(t, out["items"], "items") {
		if str(it["type"]) != "Series" {
			t.Errorf("Shows search returned a %v", it["type"])
		}
	}
	if len(rows(t, out["items"], "items")) != 3 {
		t.Errorf("Shows lists %d series, want 3", len(rows(t, out["items"], "items")))
	}

	// episodes carry their series and numbering
	out = call(t, "library_items", map[string]any{"library": "Shows", "types": "Episode", "query": "Half Loop"})
	eps := rows(t, out["items"], "items")
	if len(eps) != 1 || str(eps[0]["series"]) != "Severance" || num(t, eps[0]["season"], "season") != 1 || num(t, eps[0]["episode"], "episode") != 2 {
		t.Errorf("episode summary = %v", eps)
	}

	// filters: year, genre and a sort
	out = call(t, "library_items", map[string]any{"library": "Movies", "years": []any{1982, 1999}})
	if got := rows(t, out["items"], "items"); len(got) != 2 {
		t.Errorf("1982 and 1999 have %d films, want Blade Runner and The Thirteenth Floor: %v", len(got), got)
	}
	out = call(t, "library_items", map[string]any{"library": "Movies", "genres": []any{"Science Fiction"}, "sort": "name"})
	names := []string{}
	for _, it := range rows(t, out["items"], "items") {
		names = append(names, str(it["name"]))
	}
	if !slices.Equal(names, []string{"Blade Runner", "Dune", "Dune: Part Two", "The Thirteenth Floor"}) {
		t.Errorf("Science Fiction sorted = %v", names)
	}
	// a limit pages, and the total says what was left out
	out = call(t, "library_items", map[string]any{"library": "Movies", "limit": 3})
	if len(rows(t, out["items"], "items")) != 3 || num(t, out["total"], "total") != 8 || num(t, out["offset"], "offset") != 0 {
		t.Errorf("limit 3 = %d items of %v from %v", len(rows(t, out["items"], "items")), out["total"], out["offset"])
	}

	// a person, resolved by exact name the way person_get does
	out = call(t, "library_items", map[string]any{"library": "Movies", "person": "Ridley Scott"})
	names = names[:0]
	for _, it := range rows(t, out["items"], "items") {
		names = append(names, str(it["name"]))
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"Alien", "Blade Runner"}) {
		t.Errorf("Ridley Scott directed %v, want Alien and Blade Runner", names)
	}
	if msg := callErr(t, "library_items", map[string]any{"person": "Nobody Atall"}); !strings.Contains(msg, "Nobody Atall") {
		t.Errorf("an unknown person: %s", msg)
	}
	// a part of a name is not resolved for a filter: the error points at person_get
	if msg := callErr(t, "library_items", map[string]any{"person": "Scott"}); !strings.Contains(msg, "person_get") {
		t.Errorf("a partial person name: %s", msg)
	}
	if msg := callErr(t, "library_items", map[string]any{"library": "Nope"}); !strings.Contains(msg, "Nope") {
		t.Errorf("an unknown library: %s", msg)
	}
}

func TestLibraryRecent(t *testing.T) {
	out := call(t, "library_recent", map[string]any{"library": "Movies", "days": 1, "limit": 3})
	items := rows(t, out["items"], "items")
	if len(items) != 3 {
		t.Fatalf("recent = %d items, want 3 (the limit)", len(items))
	}
	for _, it := range items {
		if str(it["added"]) == "" {
			t.Errorf("no added date on %v", it["name"])
		}
	}
	if str(items[0]["added"]) < str(items[2]["added"]) {
		t.Error("not newest first")
	}

	// the default types include episodes, so the show library has more
	// recent additions than series alone
	out = call(t, "library_recent", map[string]any{"library": "Shows", "limit": 50})
	var episodes int
	for _, it := range rows(t, out["items"], "items") {
		if str(it["type"]) == "Episode" {
			episodes++
		}
	}
	if episodes < 9 {
		t.Errorf("recent in Shows has %d episodes, want at least 9", episodes)
	}

	// everything is older than a moment ago... except that days=0 is the
	// default of 60, so ask across libraries and expect the whole catalogue
	out = call(t, "library_recent", map[string]any{"types": "Movie", "limit": 100})
	if got, want := len(rows(t, out["items"], "items")), 8+messyMovies(); got != want {
		t.Errorf("recent movies across libraries = %d, want %d", got, want)
	}
}

func TestLibraryGenres(t *testing.T) {
	out := call(t, "library_genres", map[string]any{"library": "Movies"})
	genres := strs(t, out["genres"], "genres")
	for _, want := range []string{"Horror", "Science Fiction", "Animation"} {
		if !slices.Contains(genres, want) {
			t.Errorf("genre %s missing from %v", want, genres)
		}
	}
	// the messy shows carry only what their nfo says, the two spellings of
	// one genre as two
	out = call(t, "library_genres", map[string]any{"library": "Messy Shows", "types": "Series"})
	if genres = strs(t, out["genres"], "genres"); !slices.Equal(sorted(genres), []string{"Drama", "Science Fiction", "Science-Fiction"}) {
		t.Errorf("Messy Shows genres = %v, want Drama, Science Fiction and Science-Fiction", genres)
	}
	// across every library
	out = call(t, "library_genres", nil)
	if genres = strs(t, out["genres"], "genres"); len(genres) < 4 {
		t.Errorf("all genres = %v", genres)
	}
}

// A part of a name that matches nobody exactly answers with the people it
// could mean, and nothing else; a name nobody has is an error.
func TestPersonGetCandidates(t *testing.T) {
	out := call(t, "person_get", map[string]any{"person": "Villeneuve"})
	people := rows(t, out["candidates"], "candidates")
	if len(people) != 1 || str(people[0]["name"]) != "Denis Villeneuve" || str(people[0]["id"]) == "" {
		t.Errorf("candidates = %v", people)
	}
	if out["name"] != nil || out["id"] != nil || len(rows(t, out["credits"], "credits")) != 0 {
		t.Errorf("a partial name answered with more than candidates: %v", out)
	}
	// a word matches on it (Emby lists Adam Scott too), Ridley among them
	out = call(t, "person_get", map[string]any{"person": "Scott"})
	var ridley bool
	for _, p := range rows(t, out["candidates"], "candidates") {
		ridley = ridley || str(p["name"]) == "Ridley Scott"
	}
	if !ridley {
		t.Errorf("Scott's candidates = %v, want Ridley Scott among them", out["candidates"])
	}
	if msg := callErr(t, "person_get", map[string]any{"person": "Nobody Atall"}); !strings.Contains(msg, "Nobody Atall") {
		t.Errorf("an unknown person: %s", msg)
	}
}

// library_scan picks up a file added after the first scan: the one thing a
// scan test has to prove.
func TestLibraryScanPicksUpNewFiles(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	have := movieCount(t, "Movies")

	// a copy of an existing film under a new title, so the scanner has a
	// real container to probe
	src := filepath.Join(dataDir(), "movies", "Princess Mononoke (1997)", "Princess Mononoke (1997).mp4")
	dir := filepath.Join(dataDir(), "movies", "Collateral (2004)")
	mediaMkdir(t, dir)
	raw, err := os.ReadFile(src) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	mediaWrite(t, filepath.Join(dir, "Collateral (2004).mp4"), raw)
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		// and wait for the scan to finish: it refreshes every playlist and
		// collection, which renumbers Emby's playlist entries under a later
		// test
		if _, err := invoke("library_scan", nil); err == nil {
			_ = waitForItems("Movies", have)
			_ = waitForScan()
		}
	})

	out := call(t, "library_scan", nil)
	if b, _ := out["started"].(bool); !b {
		t.Fatalf("library_scan = %v", out)
	}
	if err := waitForItems("Movies", have+1); err != nil {
		t.Fatal(err)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
	id := findItem(t, "Movies", "Movie", "Collateral")
	if id == "" {
		t.Error("the new film has no id")
	}

	// and a scan of the one library alone picks up another
	dir2 := filepath.Join(dataDir(), "movies", "Event Horizon (1997)")
	mediaMkdir(t, dir2)
	mediaWrite(t, filepath.Join(dir2, "Event Horizon (1997).mp4"), raw)
	t.Cleanup(func() { _ = os.RemoveAll(dir2) })
	out = call(t, "library_scan", map[string]any{"library": "movies"})
	if b, _ := out["started"].(bool); !b || str(out["library"]) != "Movies" {
		t.Fatalf("library_scan Movies = %v", out)
	}
	if err := waitForItems("Movies", have+2); err != nil {
		t.Fatal(err)
	}
	if msg := callErr(t, "library_scan", map[string]any{"library": "Nope"}); !strings.Contains(msg, "Nope") {
		t.Errorf("scanning an unknown library: %s", msg)
	}
}

// A library through its whole life, checking what it holds at each step:
// created over one folder and scanned, moved to another folder and scanned
// again, renamed, deleted. Its folders are its own, laid out here, and its
// fetchers are off, so nothing else is disturbed and no provider is asked.
func TestLibraryLifecycle(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	raw, err := os.ReadFile(filepath.Join(dataDir(), "movies", "Arrival (2016)", "Arrival (2016).mp4")) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	lay := func(folder string, titles ...string) {
		for _, title := range titles {
			dir := filepath.Join(dataDir(), folder, title)
			mediaMkdir(t, dir)
			mediaWrite(t, filepath.Join(dir, title+".mp4"), raw)
		}
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(dataDir(), folder)) })
	}
	lay("lifecycle-a", "The Lord of the Rings The Fellowship of the Ring (2001)")
	lay("lifecycle-b", "The Lord of the Rings The Two Towers (2002)", "The Lord of the Rings The Return of the King (2003)")

	name := "Lifecycle"
	t.Cleanup(func() {
		_, _ = invoke("library_delete", map[string]any{"library": name, "confirm": true})
		_ = waitForScan() // Jellyfin's removal starts a library scan
	})
	call(t, "library_create", map[string]any{"name": name, "type": "movies", "paths": []any{"/media/lifecycle-a"}, "scan": true})

	holds := func(want ...string) {
		t.Helper()
		// what a library holds settles when the scan the last change asked
		// for has run; on Jellyfin only the full library scan drops an item
		// under a folder taken out, and it takes as long as it takes
		if err := waitForExpectedScan(); err != nil {
			t.Fatal(err)
		}
		var got []string
		var lastErr error
		for range 60 {
			out, err := invoke("library_items", map[string]any{"library": name, "types": "Movie"})
			if lastErr = err; err == nil {
				got = got[:0]
				for _, it := range rowsOf(out["items"]) {
					got = append(got, title(str(it["name"])))
				}
				slices.Sort(got)
				if slices.Equal(got, want) {
					return
				}
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("%s holds %v, want %v (last error: %v)", name, got, want, lastErr)
	}
	holds("The Lord of the Rings The Fellowship of the Ring")

	out := call(t, "library_edit", map[string]any{"library": name, "add_paths": []any{"/media/lifecycle-b"}, "remove_paths": []any{"/media/lifecycle-a"}})
	if locs := strs(t, out["locations"], "locations"); !slices.Equal(locs, []string{"/media/lifecycle-b"}) {
		t.Errorf("locations = %v", locs)
	}
	if scan := call(t, "library_scan", map[string]any{"library": name}); !boolOf(scan["started"]) {
		t.Errorf("library_scan = %v", scan)
	}
	holds("The Lord of the Rings The Return of the King", "The Lord of the Rings The Two Towers")
	if got := call(t, "library_get", map[string]any{"library": name}); num(t, got["item_count"], "item_count") < 2 {
		t.Errorf("library_get = %v", got)
	}

	call(t, "library_edit", map[string]any{"library": name, "name": "Lifecycle Renamed"})
	name = "Lifecycle Renamed"
	if del := call(t, "library_delete", map[string]any{"library": name, "confirm": true}); str(del["deleted"]) != name {
		t.Errorf("library_delete = %v", del)
	}
	if stillListed(t, "library_list", "libraries", name) {
		t.Errorf("%s is still listed after library_delete", name)
	}
	if msg := callErr(t, "library_items", map[string]any{"library": name}); !strings.Contains(msg, name) {
		t.Errorf("reading a deleted library: %s", msg)
	}
}

// library_create and library_delete: the harness already created four, so
// this creates a fifth over an existing folder and removes it again.
func TestLibraryCreateAndDelete(t *testing.T) {
	out := call(t, "library_create", map[string]any{"name": "Scratch", "type": "movies", "paths": []any{"/media/messy-movies"}})
	if str(out["name"]) != "Scratch" || str(out["collection_type"]) != "movies" {
		t.Errorf("library_create = %v", out)
	}
	// without save_nfo the servers keep their own: Jellyfin runs every saver
	// for a library that lists none, Emby lists none and saves none
	if boolOf(out["saves_nfo"]) != isJellyfin() {
		t.Errorf("library_create saves_nfo = %v on %s", out["saves_nfo"], backend)
	}
	// both servers give the library its id at once, before any scan
	if str(out["id"]) == "" {
		t.Errorf("library_create returned no id: %v", out)
	}
	t.Cleanup(func() {
		_, _ = invoke("library_delete", map[string]any{"library": "Scratch", "confirm": true})
		_ = waitForScan() // Jellyfin's removal starts a library scan
	})

	list := call(t, "library_list", nil)
	var found bool
	for _, row := range rows(t, list["libraries"], "libraries") {
		if str(row["name"]) == "Scratch" {
			found = true
		}
	}
	if !found {
		t.Fatal("Scratch is not listed after library_create")
	}
	// read by that id before any scan of its own: Jellyfin counts in it what
	// its folder already holds, the messy films Messy Movies scanned
	got := call(t, "library_get", map[string]any{"library": "Scratch"})
	if str(got["id"]) != str(out["id"]) {
		t.Errorf("library_get Scratch = %v, want its id %v", got, out["id"])
	}
	if isJellyfin() && num(t, object(t, got["type_counts"], "type_counts")["Movie"], "Movie") != messyMovies() {
		t.Errorf("library_get Scratch counts %v, want the %d messy films", got["type_counts"], messyMovies())
	}

	// a name another library holds but for its case is refused before
	// anything is made: Jellyfin would have made it SCRATCH2 and Emby a second
	// library its names cannot tell apart
	if msg := callErr(t, "library_create", map[string]any{"name": "SCRATCH", "type": "movies", "paths": []any{"/media/disc-src"}}); !strings.Contains(msg, `a library named "Scratch" already exists`) {
		t.Errorf("a name another library holds but for its case: %s", msg)
	}
	for _, row := range rows(t, call(t, "library_list", nil)["libraries"], "libraries") {
		if name := str(row["name"]); strings.EqualFold(name, "scratch") && name != "Scratch" || strings.HasPrefix(name, "SCRATCH") {
			t.Errorf("the refused create made %s", name)
			_, _ = invoke("library_delete", map[string]any{"library": str(row["id"]), "confirm": true})
		}
	}

	if msg := callErr(t, "library_create", map[string]any{"name": "", "paths": []any{"/x"}}); !strings.Contains(msg, "required") {
		t.Errorf("an empty name: %s", msg)
	}
	if msg := callErr(t, "library_delete", map[string]any{"library": "Scratch"}); !strings.Contains(msg, "confirm") {
		t.Errorf("delete without confirm: %s", msg)
	}
	del := call(t, "library_delete", map[string]any{"library": "scratch", "confirm": true})
	if str(del["deleted"]) != "Scratch" {
		t.Errorf("library_delete = %v", del)
	}
	list = call(t, "library_list", nil)
	for _, row := range rows(t, list["libraries"], "libraries") {
		if str(row["name"]) == "Scratch" {
			t.Error("Scratch is still listed after library_delete")
		}
	}
	if msg := callErr(t, "library_delete", map[string]any{"library": "Scratch", "confirm": true}); !strings.Contains(msg, "Scratch") {
		t.Errorf("deleting a missing library: %s", msg)
	}
}

// save_nfo writes an edit back to an nfo beside the media file, and only
// while it is on; switching it leaves the library's other options as they
// were.
func TestLibraryNfoSaving(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	raw, err := os.ReadFile(filepath.Join(dataDir(), "movies", "Arrival (2016)", "Arrival (2016).mp4")) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(dataDir(), "nfo-saving", "Triangle (2009)")
	mediaMkdir(t, dir)
	mediaWrite(t, filepath.Join(dir, "Triangle (2009).mp4"), raw)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(dataDir(), "nfo-saving")) })
	const name = "Nfo Saving"
	t.Cleanup(func() {
		_, _ = invoke("library_delete", map[string]any{"library": name, "confirm": true})
		_ = waitForScan() // Jellyfin's removal starts a library scan
	})

	if out := call(t, "library_create", map[string]any{"name": name, "type": "movies", "paths": []any{"/media/nfo-saving"}, "scan": true, "save_nfo": true}); !boolOf(out["saves_nfo"]) {
		t.Errorf("library_create save_nfo = %v", out)
	}
	var id string
	if !eventually(func() bool {
		out, err := invoke("library_items", map[string]any{"library": name})
		if items := rowsOf(out["items"]); err == nil && len(items) == 1 {
			id = str(items[0]["id"])
		}
		return id != ""
	}) {
		t.Fatal("the library never held its film")
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}

	// Emby names the nfo after the media file, Jellyfin movie.nfo
	saved := func(text string) bool {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if raw, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil && strings.HasSuffix(e.Name(), ".nfo") && strings.Contains(string(raw), text) { //nolint:gosec // same
				return true
			}
		}
		return false
	}
	call(t, "item_edit", map[string]any{"ids": []any{id}, "overview": "Zzyzx: saved to the nfo."})
	if !eventually(func() bool { return saved("Zzyzx: saved to the nfo.") }) {
		t.Errorf("with save_nfo on, the edit is in no nfo in %s", dir)
	}

	before := libraryOptions(t, name)
	out := call(t, "library_edit", map[string]any{"library": name, "save_nfo": false})
	if boolOf(out["saves_nfo"]) || !slices.Contains(strs(t, out["changed"], "changed"), "nfo saving off") {
		t.Errorf("library_edit save_nfo false = %v", out)
	}
	after := libraryOptions(t, name)
	for k, v := range before {
		if k != "MetadataSavers" && !reflect.DeepEqual(v, after[k]) {
			t.Errorf("switching nfo saving changed the library's %s from %v to %v", k, v, after[k])
		}
	}
	if boolOf(call(t, "library_get", map[string]any{"library": name})["saves_nfo"]) {
		t.Error("library_get says the library still saves nfos")
	}
	call(t, "item_edit", map[string]any{"ids": []any{id}, "overview": "Zzyzx: kept out of the nfo."})
	if !holds(func() bool { return !saved("Zzyzx: kept out of the nfo.") }) {
		t.Error("with save_nfo off, the edit was written to an nfo")
	}

	if out := call(t, "library_edit", map[string]any{"library": name, "save_nfo": true}); !boolOf(out["saves_nfo"]) {
		t.Errorf("library_edit save_nfo true = %v", out)
	}
}

// libraryOptions reads a library's options as the server lists them.
func libraryOptions(t *testing.T, name string) map[string]any {
	t.Helper()

	path := "/Library/VirtualFolders"
	if !isJellyfin() {
		path = "/Library/VirtualFolders/Query"
	}
	status, raw := api(t, http.MethodGet, path, "", nil)
	var folders []struct {
		Name           string
		LibraryOptions map[string]any
	}
	var err error
	if isJellyfin() {
		err = json.Unmarshal(raw, &folders)
	} else {
		var page struct {
			Items []struct {
				Name           string
				LibraryOptions map[string]any
			}
		}
		err = json.Unmarshal(raw, &page)
		folders = page.Items
	}
	if status != http.StatusOK || err != nil {
		t.Fatalf("listing libraries: HTTP %d: %v", status, err)
	}
	for _, f := range folders {
		if f.Name == name {
			return f.LibraryOptions
		}
	}
	t.Fatalf("no library %s", name)

	return nil
}
