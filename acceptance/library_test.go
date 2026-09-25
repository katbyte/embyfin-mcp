//go:build integration

package acceptance

import (
	"cmp"
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
		if locs := strs(t, row["locations"], "locations"); !slices.Equal(locs, []string{l.Folder}) {
			t.Errorf("%s locations = %v, want [%s]", l.Name, locs, l.Folder)
		}
		if str(row["id"]) == "" {
			t.Errorf("%s has no id", l.Name)
		}
		// made without save_nfo, each keeps its server's own default:
		// Jellyfin runs every saver for a library that lists none, Emby none
		if saves, ok := row["saves_nfo"].(bool); !ok || saves != isJellyfin() {
			t.Errorf("%s saves_nfo = %v on %s", l.Name, row["saves_nfo"], backend)
		}
	}
	// the servers list a folder of their own for the collections once there
	// is one, and Emby one for the playlists too; nothing else is listed
	for name, row := range got {
		if slices.ContainsFunc(libraries, func(l libraryFixture) bool { return l.Name == name }) {
			continue
		}
		if kind := serverFolders[name]; kind == "" || str(row["collection_type"]) != kind {
			t.Errorf("library %s (%v) is none the harness made", name, row["collection_type"])
		}
	}
}

// serverFolders are the folders the servers list as libraries of their own
// once a collection (both) or a playlist (Emby) is made, by the kind they
// give each.
var serverFolders = map[string]string{"Collections": "boxsets", "Playlists": "playlists"}

func TestLibraryGet(t *testing.T) {
	// by name, case-insensitively
	out := call(t, "library_get", map[string]any{"library": "movies"})
	if str(out["name"]) != "Movies" || str(out["collection_type"]) != "movies" || !slices.Equal(strs(t, out["locations"], "locations"), []string{"/media/movies"}) {
		t.Errorf("library_get = %v", out)
	}
	// by id
	byID := call(t, "library_get", map[string]any{"library": str(out["id"])})
	if str(byID["name"]) != "Movies" || !reflect.DeepEqual(byID, out) {
		t.Errorf("by id = %v, by name %v", byID, out)
	}

	// each library counted by its own kinds, exactly: a film library its
	// films, a show library its series and episode files, the music its
	// artists, albums and songs
	for _, l := range libraries {
		want := map[string]any{"Movie": float64(l.Items())}
		switch l.Type {
		case "tvshows":
			want = map[string]any{"Series": float64(l.Items()), "Episode": float64(l.Episodes())}
		case "music":
			want = map[string]any{"MusicArtist": float64(artists()), "MusicAlbum": float64(musicAlbums()), "Audio": float64(songs())}
		}
		got := call(t, "library_get", map[string]any{"library": l.Name})
		if counts := object(t, got["type_counts"], "type_counts"); !reflect.DeepEqual(counts, want) {
			t.Errorf("%s type_counts = %v, want %v", l.Name, counts, want)
		}
		// the item count is every item under it but the folders: on Jellyfin
		// the counted kinds and the seasons; Emby keeps more items besides,
		// and is never below them
		sum := 0
		for _, n := range want {
			sum += int(n.(float64)) //nolint:forcetypeassert // built above
		}
		seasons := num(t, call(t, "library_items", map[string]any{"library": l.Name, "types": "Season", "limit": 1})["total"], "total")
		// Jellyfin keeps an artist with no folder of its own - one only the
		// tags name, Various Artists and The Pink Floyd - among its metadata
		// rather than under the library: counted among the library's kinds,
		// and not among its items
		if isJellyfin() && l.Type == "music" {
			sum -= 2
		}
		switch n := num(t, got["item_count"], "item_count"); {
		case isJellyfin() && n != sum+seasons:
			t.Errorf("%s item_count = %d, want its %d counted items and %d seasons", l.Name, n, sum, seasons)
		case n < sum+seasons:
			t.Errorf("%s item_count = %d, below its %d counted items and %d seasons", l.Name, n, sum, seasons)
		}
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
	if alien != 3 || aliens != 1 || num(t, out["total"], "total") != 4 {
		t.Errorf("Alien x%d Aliens x%d of %v, want 3 and 1 of 4: %v", alien, aliens, out["total"], items)
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
	if len(rows(t, out["items"], "items")) != len(shows) {
		t.Errorf("Shows lists %d series, want %d", len(rows(t, out["items"], "items")), len(shows))
	}

	// episodes carry their series and numbering
	out = call(t, "library_items", map[string]any{"library": "Shows", "types": "Episode", "query": "Half Loop"})
	eps := rows(t, out["items"], "items")
	if len(eps) != 1 || str(eps[0]["series"]) != "Severance" || num(t, eps[0]["season"], "season") != 1 || num(t, eps[0]["episode"], "episode") != 2 {
		t.Errorf("episode summary = %v", eps)
	}

	// years, with a sort that puts the newer first
	out = call(t, "library_items", map[string]any{"library": "Movies", "years": []any{1982, 1999}, "sort": "year", "desc": true})
	if got := names(t, out["items"], "items"); !slices.Equal(got, []string{"The Thirteenth Floor", "Blade Runner"}) {
		t.Errorf("1982 and 1999 newest first = %v, want The Thirteenth Floor and Blade Runner", got)
	}

	// a person, resolved by exact name the way person_get does, or by id
	out = call(t, "library_items", map[string]any{"library": "Movies", "person": "Ridley Scott"})
	if got := sorted(names(t, out["items"], "items")); !slices.Equal(got, []string{"Alien", "Blade Runner"}) {
		t.Errorf("Ridley Scott directed %v, want Alien and Blade Runner", got)
	}
	villeneuve := call(t, "person_get", map[string]any{"person": "Denis Villeneuve"})
	byName := call(t, "library_items", map[string]any{"person": "Denis Villeneuve", "types": "Movie", "limit": 50})
	byID := call(t, "library_items", map[string]any{"person": str(villeneuve["id"]), "types": "Movie", "limit": 50})
	// both Arrivals and both Dunes of his
	if a, b := sorted(names(t, byName["items"], "items")), sorted(names(t, byID["items"], "items")); !slices.Equal(a, []string{"Arrival", "Arrival", "Dune", "Dune: Part Two"}) || !slices.Equal(a, b) {
		t.Errorf("Denis Villeneuve by name = %v, by id %v", a, b)
	}
	if msg := callErr(t, "library_items", map[string]any{"person": "Nobody Atall"}); !strings.Contains(msg, "Nobody Atall") {
		t.Errorf("an unknown person: %s", msg)
	}
	// a part of a name is not resolved for a filter: the error points at person_get
	if msg := callErr(t, "library_items", map[string]any{"person": "Scott"}); !strings.Contains(msg, "person_get") {
		t.Errorf("a partial person name: %s", msg)
	}
}

// Every order library_items offers, each read against the facts it orders
// by rather than a list typed out here.
func TestLibraryItemsSorts(t *testing.T) {
	all := call(t, "library_items", map[string]any{"library": "Movies", "limit": 50})
	byName := names(t, all["items"], "items")
	if len(byName) != len(movies) {
		t.Fatalf("Movies = %v", byName)
	}

	// desc alone turns the default order, by name, round
	out := call(t, "library_items", map[string]any{"library": "Movies", "desc": true, "limit": 50})
	if got := names(t, out["items"], "items"); !slices.Equal(got, reversed(byName)) {
		t.Errorf("desc without a sort = %v, want %v", got, reversed(byName))
	}

	// premiered: TMDB's release dates, which for these films fall in the
	// order of their years
	out = call(t, "library_items", map[string]any{"library": "Movies", "sort": "premiered", "limit": 50})
	if got := names(t, out["items"], "items"); !slices.Equal(got, []string{"Alien", "Blade Runner", "Aliens", "Princess Mononoke", "The Thirteenth Floor", "Limitless", "Arrival", "Dune", "Dune: Part Two"}) {
		t.Errorf("by premiere = %v", got)
	}

	// rating: the providers' scores, highest first
	out = call(t, "library_items", map[string]any{"library": "Movies", "sort": "rating", "desc": true, "limit": 50})
	var ratings []float64
	for _, it := range rows(t, out["items"], "items") {
		ratings = append(ratings, decimal(t, call(t, "item_get", map[string]any{"id": str(it["id"])})["community_rating"], "community_rating"))
	}
	if len(ratings) != len(movies) || !slices.IsSortedFunc(ratings, func(a, b float64) int { return cmp.Compare(b, a) }) {
		t.Errorf("by rating, highest first = %v", ratings)
	}

	// runtime: the messy shows' episodes, which run three minutes, five
	// seconds and one - after Deep Space Nine's broken file, whose duration
	// claims twelve hours
	out = call(t, "library_items", map[string]any{"library": "Messy Shows", "types": "Episode", "sort": "runtime", "desc": true, "limit": 50})
	var runtimes []int
	for _, it := range rows(t, out["items"], "items") {
		runtimes = append(runtimes, num(t, it["runtime_s"], "runtime_s"))
	}
	if len(runtimes) != messyEpisodes() || !slices.Equal(runtimes[:4], []int{43200, 180, 180, 5}) || !slices.IsSortedFunc(runtimes, func(a, b int) int { return b - a }) {
		t.Errorf("episodes by runtime, longest first = %v", runtimes)
	}

	// random: the same films in some order, all of them
	out = call(t, "library_items", map[string]any{"library": "Movies", "sort": "random", "limit": 50})
	if got := sorted(names(t, out["items"], "items")); !slices.Equal(got, sorted(byName)) || num(t, out["total"], "total") != len(movies) {
		t.Errorf("at random = %v of %v, want every film once", got, out["total"])
	}

	// played: most recently played first, in a user's view. Played is
	// what finishing a film sets, and marking one watched sets it too
	first, second := findItem(t, "Movies", "Movie", "Princess Mononoke"), findItem(t, "Movies", "Movie", "Dune")
	t.Cleanup(func() {
		for _, id := range []string{first, second} {
			_, _ = invoke("item_set_state", map[string]any{"id": id, "user": "alice", "watched": false})
		}
	})
	call(t, "item_set_state", map[string]any{"id": first, "user": "alice", "watched": true})
	time.Sleep(1100 * time.Millisecond) // the stamps are to the second on Emby
	call(t, "item_set_state", map[string]any{"id": second, "user": "alice", "watched": true})
	out = call(t, "library_items", map[string]any{"library": "Movies", "user": "alice", "sort": "played", "desc": true, "limit": 2})
	if got := names(t, out["items"], "items"); !slices.Equal(got, []string{"Dune", "Princess Mononoke"}) {
		t.Errorf("alice's most recently played = %v, want Dune then Princess Mononoke", got)
	}
	if msg := callErr(t, "library_items", map[string]any{"library": "Movies", "user": "nobody", "sort": "played"}); !strings.Contains(msg, "nobody") {
		t.Errorf("played for an unknown user: %s", msg)
	}
}

// reversed is a copy of s back to front.
func reversed(s []string) []string {
	out := slices.Clone(s)
	slices.Reverse(out)

	return out
}

// An item part way through is in progress in the view of the user it is
// part way through for, and nobody else's.
func TestLibraryItemsInProgress(t *testing.T) {
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	call(t, "item_set_state", map[string]any{"id": arrival, "user": "alice", "position_s": 30})
	t.Cleanup(func() {
		_, _ = invoke("item_set_state", map[string]any{"id": arrival, "user": "alice", "watched": false})
	})

	out := call(t, "library_items", map[string]any{"library": "Movies", "user": "alice", "watched": "in_progress"})
	if got := rows(t, out["items"], "items"); len(got) != 1 || str(got[0]["id"]) != arrival {
		t.Errorf("alice's films in progress = %v, want Arrival", got)
	}
	if n := num(t, call(t, "library_items", map[string]any{"library": "Movies", "watched": "in_progress"})["total"], "total"); n != 0 {
		t.Errorf("root has %d films in progress, want none", n)
	}
}

func TestLibraryRecent(t *testing.T) {
	everything := rows(t, call(t, "library_recent", map[string]any{"limit": 100})["items"], "items")
	stamps := make([]time.Time, 0, len(everything))
	for _, it := range everything {
		stamps = append(stamps, stamp(t, it["added"]))
	}
	// newest first, across every row, the stamps read as times: as strings
	// the fractions of a second of different lengths order wrongly
	if !slices.IsSortedFunc(stamps, func(a, b time.Time) int { return b.Compare(a) }) {
		t.Errorf("not newest first: %v", stamps)
	}

	// a limit keeps the newest: the three it answers are as new as the
	// newest three of the whole list (films added in the same second may
	// come in either order, so it is their stamps that are compared)
	out := call(t, "library_recent", map[string]any{"library": "Movies", "limit": 3})
	items := rows(t, out["items"], "items")
	if len(items) != 3 {
		t.Fatalf("recent = %d items, want 3 (the limit)", len(items))
	}
	all := rows(t, call(t, "library_recent", map[string]any{"library": "Movies", "limit": 50})["items"], "items")
	if len(all) != len(movies) {
		t.Fatalf("Movies' recent additions = %d, want its %d films", len(all), len(movies))
	}
	for i := range items {
		if got, want := stamp(t, items[i]["added"]), stamp(t, all[i]["added"]); !got.Equal(want) {
			t.Errorf("limit 3's film %d was added %v, want the %v of the newest three", i+1, got, want)
		}
	}

	// the default types include episodes, so the show library has more
	// recent additions than series alone
	out = call(t, "library_recent", map[string]any{"library": "Shows", "limit": 50})
	var episodes, series int
	for _, it := range rows(t, out["items"], "items") {
		switch str(it["type"]) {
		case "Episode":
			episodes++
		case "Series":
			series++
		}
	}
	if episodes != showEpisodes() || series != len(shows) {
		t.Errorf("recent in Shows has %d episodes and %d series, want %d and %d", episodes, series, showEpisodes(), len(shows))
	}

	// days=0 is the default of 60, which the whole catalogue falls within;
	// across libraries
	out = call(t, "library_recent", map[string]any{"types": "Movie", "days": 0, "limit": 100})
	if got, want := len(rows(t, out["items"], "items")), len(movies)+messyMovies(); got != want {
		t.Errorf("recent movies across libraries = %d, want %d", got, want)
	}
	// music by its own kinds
	out = call(t, "library_recent", map[string]any{"library": "Music", "types": "MusicAlbum"})
	if got := sorted(names(t, out["items"], "items")); len(got) != musicAlbums() {
		t.Errorf("recent albums = %v, want the %d", got, musicAlbums())
	}
	if msg := callErr(t, "library_recent", map[string]any{"library": "Nope"}); !strings.Contains(msg, `no library named "Nope"`) {
		t.Errorf("an unknown library: %s", msg)
	}
}

// stamp reads a server timestamp.
func stamp(t *testing.T, v any) time.Time {
	t.Helper()

	at, err := time.Parse(time.RFC3339, str(v))
	if err != nil {
		t.Fatalf("%v is not a time: %v", v, err)
	}

	return at
}

// days counts back from now, and an item added before the cut is left out:
// a film dated a month back is recent for sixty days and not for seven.
func TestLibraryRecentDays(t *testing.T) {
	cube := findItem(t, "Messy Movies", "Movie", "Cube")
	was := fullItem(t, cube)["DateCreated"]
	t.Cleanup(func() { updateItem(t, cube, map[string]any{"DateCreated": was}) })
	updateItem(t, cube, map[string]any{"DateCreated": time.Now().AddDate(0, 0, -30).UTC().Format(time.RFC3339)})

	recent := func(days int) []string {
		var ids []string
		for _, it := range rows(t, call(t, "library_recent", map[string]any{"library": "Messy Movies", "days": days, "limit": 50})["items"], "items") {
			ids = append(ids, str(it["id"]))
		}
		return ids
	}
	if !eventually(func() bool { return !slices.Contains(recent(7), cube) }) {
		t.Errorf("a film added a month back is among the last week's: %v", recent(7))
	}
	if got := recent(60); !slices.Contains(got, cube) || len(got) != messyMovies() {
		t.Errorf("the last sixty days' = %d films, want all %d, Cube among them", len(got), messyMovies())
	}
}

func TestLibraryGenres(t *testing.T) {
	// the genres the films' nfos carry, exactly
	out := call(t, "library_genres", map[string]any{"library": "Movies"})
	var want []string
	for _, m := range movies {
		if !slices.Contains(want, m.Genre) {
			want = append(want, m.Genre)
		}
	}
	slices.Sort(want)
	if genres := strs(t, out["genres"], "genres"); !slices.Equal(genres, want) {
		t.Errorf("Movies genres = %v, want %v", genres, want)
	}
	// the messy shows carry only what their nfo says, the two spellings of
	// one genre as two
	out = call(t, "library_genres", map[string]any{"library": "Messy Shows", "types": "Series"})
	if genres := strs(t, out["genres"], "genres"); !slices.Equal(genres, []string{"Animation", "Comedy", "Drama", "Science Fiction", "Science-Fiction"}) {
		t.Errorf("Messy Shows genres = %v, want Animation, Comedy, Drama, Science Fiction and Science-Fiction", genres)
	}
	// across every library, the films and series: the music is left out
	// unless asked for by its kind
	out = call(t, "library_genres", nil)
	if genres := strs(t, out["genres"], "genres"); !slices.Equal(genres, []string{"Action", "Animation", "Comedy", "Drama", "Horror", "Mystery", "Science Fiction", "Science-Fiction", "Thriller"}) {
		t.Errorf("every library's genres = %v", genres)
	}
	var music []string
	for _, a := range albums {
		if !slices.Contains(music, a.Genre) {
			music = append(music, a.Genre)
		}
	}
	slices.Sort(music)
	if genres := strs(t, call(t, "library_genres", map[string]any{"types": "MusicAlbum"})["genres"], "genres"); !slices.Equal(genres, music) {
		t.Errorf("the albums' genres = %v, want %v", genres, music)
	}
	if msg := callErr(t, "library_genres", map[string]any{"library": "Nope"}); !strings.Contains(msg, `no library named "Nope"`) {
		t.Errorf("an unknown library: %s", msg)
	}
}

// library_filters with no library reads every library's films and series,
// and an unknown library is refused.
func TestLibraryFiltersEverywhere(t *testing.T) {
	out := call(t, "library_filters", nil)
	if n, want := num(t, out["items_scanned"], "items_scanned"), len(movies)+messyMovies()+len(shows)+messySeries; n != want {
		t.Errorf("every library's filters read %d items, want the %d films and series", n, want)
	}
	// Science Fiction on four clean films and The Expanse; on the messy Dune,
	// Interstellar and Blade Runner (once or twice, as the server stores
	// it); and on the messy Andor and Deep Space Nine
	want := 4 + 1 + 3 + 2
	if !versionsMerged() {
		want++
	}
	if n := valueCounts(t, out["genres"], "genres")["Science Fiction"]; n != want {
		t.Errorf("Science Fiction on %d items everywhere, want %d", n, want)
	}
	if msg := callErr(t, "library_filters", map[string]any{"library": "Nope"}); !strings.Contains(msg, `no library named "Nope"`) {
		t.Errorf("an unknown library: %s", msg)
	}
}

// A part of a name that matches nobody exactly answers with the people it
// could mean, and nothing else (TestPersonGet has Scott, whose word matches
// more than one).
func TestPersonGetCandidates(t *testing.T) {
	out := call(t, "person_get", map[string]any{"person": "Villeneuve"})
	people := rows(t, out["candidates"], "candidates")
	if len(people) != 1 || str(people[0]["name"]) != "Denis Villeneuve" || str(people[0]["id"]) == "" {
		t.Errorf("candidates = %v", people)
	}
	if out["name"] != nil || out["id"] != nil || len(rows(t, out["credits"], "credits")) != 0 {
		t.Errorf("a partial name answered with more than candidates: %v", out)
	}
}

// library_scan picks up a file added after the first scan: the one thing a
// scan test has to prove. A scan of every library does, and so does a scan
// of one - which leaves another library's new file where it is.
func TestLibraryScanPicksUpNewFiles(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	have, messyHave := movieCount(t, "Movies"), movieCount(t, "Messy Movies")

	// copies of an existing film under new titles, so the scanner has a real
	// container to probe
	raw := fixtureVideo(t, "movies", "Princess Mononoke (1997)", "Princess Mononoke (1997).mp4")
	stageFilm := func(library, title string) string {
		dir := filepath.Join(dataDir(), library, title)
		mediaMkdir(t, dir)
		mediaWrite(t, filepath.Join(dir, title+".mp4"), raw)
		return dir
	}
	collateral := stageFilm("movies", "Collateral (2004)")
	var eventHorizon, triangle string
	t.Cleanup(func() {
		for _, dir := range []string{collateral, eventHorizon, triangle} {
			if dir != "" {
				_ = os.RemoveAll(dir)
			}
		}
		// and wait for the scan to finish: it refreshes every playlist and
		// collection, which renumbers Emby's playlist entries under a later
		// test
		if err := scanUntil("Movies", have); err != nil {
			t.Errorf("putting Movies back: %v", err)
		}
		if n := typeCounts("Messy Movies")["Movie"]; n != messyHave {
			t.Errorf("Messy Movies holds %d films after the test, want %d", n, messyHave)
		}
	})

	out := call(t, "library_scan", nil)
	if !boolOf(out["started"]) || out["library"] != nil {
		t.Fatalf("library_scan = %v", out)
	}
	if err := waitForItems("Movies", have+1); err != nil {
		t.Fatal(err)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
	// the newest addition to the library is the one just scanned in
	newest := call(t, "library_items", map[string]any{"library": "Movies", "sort": "added", "desc": true, "limit": 1})
	if got := names(t, newest["items"], "items"); !slices.Equal(got, []string{"Collateral"}) || str(rows(t, newest["items"], "items")[0]["id"]) != findItem(t, "Movies", "Movie", "Collateral") {
		t.Errorf("the most recently added film = %v, want Collateral", got)
	}

	// a scan of one library, named by its id, picks up its new file and
	// leaves another library's alone
	eventHorizon = stageFilm("movies", "Event Horizon (1997)")
	triangle = stageFilm("messy-movies", "Triangle (2009)")
	id := str(call(t, "library_get", map[string]any{"library": "Movies"})["id"])
	out = call(t, "library_scan", map[string]any{"library": id})
	if !boolOf(out["started"]) || str(out["library"]) != "Movies" {
		t.Fatalf("library_scan %s = %v", id, out)
	}
	if err := waitForItems("Movies", have+2); err != nil {
		t.Fatal(err)
	}
	if !holds(func() bool { return movieCount(t, "Messy Movies") == messyHave }) {
		t.Errorf("a scan of Movies alone brought Triangle into Messy Movies: %d films", movieCount(t, "Messy Movies"))
	}
	// taken away before any scan of every library could find it
	_ = os.RemoveAll(triangle)

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
	raw := fixtureVideo(t, "movies", "Arrival (2016)", "Arrival (2016).mp4")
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
	if got := call(t, "library_get", map[string]any{"library": name}); num(t, object(t, got["type_counts"], "type_counts")["Movie"], "Movie") != 2 {
		t.Errorf("library_get = %v", got)
	}

	// on Jellyfin the rename starts a scan of every library, which gives the
	// library its new id; it runs to its end before the library goes, or
	// the delete cuts short the fetches it makes for the library's films
	// at a different point each time
	out = call(t, "library_edit", map[string]any{"library": name, "name": "Lifecycle Renamed"})
	name = "Lifecycle Renamed"
	if str(out["id"]) == "" || str(out["name"]) != name {
		t.Errorf("the rename = %v, want the library under its new name with its id", out)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
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

// library_create and library_delete: the harness already created five, so
// this creates a sixth over an existing folder and removes it again.
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
	// read by that id before any scan of its own: both servers count in it
	// what its folder already holds, the messy films Messy Movies scanned
	got := call(t, "library_get", map[string]any{"library": str(out["id"])})
	if str(got["name"]) != "Scratch" {
		t.Errorf("library_get %v = %v, want Scratch", out["id"], got)
	}
	if num(t, object(t, got["type_counts"], "type_counts")["Movie"], "Movie") != messyMovies() {
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
	if msg := callErr(t, "library_delete", map[string]any{"library": "", "confirm": true}); !strings.Contains(msg, "library is required") {
		t.Errorf("delete of no library: %s", msg)
	}
	del := call(t, "library_delete", map[string]any{"library": "scratch", "confirm": true})
	if str(del["deleted"]) != "Scratch" {
		t.Errorf("library_delete = %v", del)
	}
	if stillListed(t, "library_list", "libraries", "Scratch") {
		t.Error("Scratch is still listed after library_delete")
	}
	if msg := callErr(t, "library_delete", map[string]any{"library": "Scratch", "confirm": true}); !strings.Contains(msg, "Scratch") {
		t.Errorf("deleting a missing library: %s", msg)
	}
}

// Every kind of library the tool names can be made, and says it is that kind
// (on Jellyfin a mixed one has no kind to say); save_nfo false is kept on
// Jellyfin too,
// whose own default is on; and a folder the server cannot see is refused
// with nothing made.
func TestLibraryCreateKinds(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	mediaMkdir(t, filepath.Join(dataDir(), "kinds-empty"))
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(dataDir(), "kinds-empty")) })

	// Emby calls a mixed library mixed; Jellyfin gives it no kind at all
	mixed := "mixed"
	if isJellyfin() {
		mixed = ""
	}
	for kind, want := range map[string]string{"mixed": mixed, "homevideos": "homevideos", "musicvideos": "musicvideos", "books": "books"} {
		t.Run(kind, func(t *testing.T) {
			name := "Zzyzx " + kind
			t.Cleanup(func() {
				_, _ = invoke("library_delete", map[string]any{"library": name, "confirm": true})
				_ = waitForScan()
			})
			out := call(t, "library_create", map[string]any{"name": name, "type": kind, "paths": []any{"/media/kinds-empty"}, "save_nfo": false})
			if str(out["collection_type"]) != want || boolOf(out["saves_nfo"]) || str(out["id"]) == "" {
				t.Errorf("a %s library = %v, want kind %q saving no nfos", kind, out, want)
			}
			got := call(t, "library_get", map[string]any{"library": name})
			if str(got["collection_type"]) != want || boolOf(got["saves_nfo"]) || num(t, got["item_count"], "item_count") != 0 {
				t.Errorf("library_get %s = %v", name, got)
			}
			if del := call(t, "library_delete", map[string]any{"library": str(out["id"]), "confirm": true}); str(del["deleted"]) != name {
				t.Errorf("library_delete by id = %v", del)
			}
			if stillListed(t, "library_list", "libraries", name) {
				t.Errorf("%s is still listed after library_delete by id", name)
			}
		})
	}

	const missing = "Zzyzx Nowhere"
	msg := callErr(t, "library_create", map[string]any{"name": missing, "type": "movies", "paths": []any{"/media/does-not-exist"}})
	if !strings.Contains(msg, "HTTP 400") {
		t.Errorf("a folder that is not there: %s", msg)
	}
	if stillListed(t, "library_list", "libraries", missing) {
		t.Errorf("the refused create made %s", missing)
		_, _ = invoke("library_delete", map[string]any{"library": missing, "confirm": true})
	}
}

// library_edit checks what it can before changing anything, and when the
// server refuses a step after others landed, says which landed. A library is
// edited by its id as readily as by its name.
func TestLibraryEditRefusals(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	mediaMkdir(t, filepath.Join(dataDir(), "edit-empty"))
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(dataDir(), "edit-empty")) })
	const name = "Zzyzx Edits"
	t.Cleanup(func() {
		_, _ = invoke("library_delete", map[string]any{"library": name, "confirm": true})
		_ = waitForScan()
	})
	id := str(call(t, "library_create", map[string]any{"name": name, "type": "movies", "paths": []any{"/media/edit-empty"}, "save_nfo": false})["id"])

	// the same folder twice is refused before anything is asked of the server
	if msg := callErr(t, "library_edit", map[string]any{"library": id, "add_paths": []any{"/media/disc-src", "/media/disc-src"}}); !strings.Contains(msg, "/media/disc-src is named more than once") {
		t.Errorf("a folder added twice: %s", msg)
	}
	if msg := callErr(t, "library_edit", map[string]any{"library": id, "add_paths": []any{"/media/disc-src"}, "remove_paths": []any{"/media/disc-src"}}); !strings.Contains(msg, "named more than once") {
		t.Errorf("a folder added and removed: %s", msg)
	}
	if got := call(t, "library_get", map[string]any{"library": id}); !slices.Equal(strs(t, got["locations"], "locations"), []string{"/media/edit-empty"}) || boolOf(got["saves_nfo"]) {
		t.Errorf("the refused edits changed the library: %v", got)
	}

	// nfo saving lands first, then the server refuses a folder it cannot
	// see: the answer says what landed, and it has
	msg := callErr(t, "library_edit", map[string]any{"library": id, "save_nfo": true, "add_paths": []any{"/media/does-not-exist"}})
	if !strings.Contains(msg, "adding /media/does-not-exist") || !strings.Contains(msg, "already done before it failed: nfo saving on") {
		t.Errorf("a refusal after a change landed: %s", msg)
	}
	if got := call(t, "library_get", map[string]any{"library": id}); !boolOf(got["saves_nfo"]) || !slices.Equal(strs(t, got["locations"], "locations"), []string{"/media/edit-empty"}) {
		t.Errorf("after the half-done edit = %v, want nfo saving on and the one folder", got)
	}

	// by id: a folder added, and nfo saving off again
	out := call(t, "library_edit", map[string]any{"library": id, "add_paths": []any{"/media/disc-src"}, "save_nfo": false})
	if str(out["name"]) != name || !slices.Equal(sorted(strs(t, out["locations"], "locations")), []string{"/media/disc-src", "/media/edit-empty"}) || boolOf(out["saves_nfo"]) {
		t.Errorf("library_edit by id = %v", out)
	}
	if changed := strs(t, out["changed"], "changed"); !slices.Equal(changed, []string{"nfo saving off", "added /media/disc-src"}) {
		t.Errorf("changed = %v, want nfo saving off then the folder", changed)
	}
	// the new folder's two streams are films of the library once it is scanned
	call(t, "library_scan", map[string]any{"library": id})
	if !eventuallyWithin(scanPatience, func() bool { return typeCounts(name)["Movie"] == 2 }) {
		t.Errorf("%s never held the two streams: %v", name, typeCounts(name))
	}
	if err := waitForScan(); err != nil {
		t.Error(err)
	}
}

// save_nfo writes an edit back to an nfo beside the media file, and only
// while it is on; switching it leaves the library's other options as they
// were, and switching it back on writes again.
func TestLibraryNfoSaving(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	raw := fixtureVideo(t, "movies", "Arrival (2016)", "Arrival (2016).mp4")
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

	// and on again, the next edit is written
	if out := call(t, "library_edit", map[string]any{"library": name, "save_nfo": true}); !boolOf(out["saves_nfo"]) || !slices.Contains(strs(t, out["changed"], "changed"), "nfo saving on") {
		t.Errorf("library_edit save_nfo true = %v", out)
	}
	call(t, "item_edit", map[string]any{"ids": []any{id}, "overview": "Zzyzx: saved again."})
	if !eventually(func() bool { return saved("Zzyzx: saved again.") }) {
		t.Errorf("with save_nfo back on, the edit is in no nfo in %s", dir)
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

// library_items with a query and a sort: both servers answer a search in
// their own order of how well each item matches and ignore the sort, so
// "Dune, newest first" came back oldest first. The tool sorts the matches
// itself.
func TestLibraryItemsSortsASearch(t *testing.T) {
	out := call(t, "library_items", map[string]any{"library": "Movies", "query": "Dune", "sort": "year", "desc": true})
	if got := names(t, out["items"], "items"); !slices.Equal(got, []string{"Dune: Part Two", "Dune"}) {
		t.Errorf("Dune newest first = %v, want Dune: Part Two then Dune", got)
	}
	out = call(t, "library_items", map[string]any{"library": "Movies", "query": "Dune", "sort": "year"})
	if got := names(t, out["items"], "items"); !slices.Equal(got, []string{"Dune", "Dune: Part Two"}) {
		t.Errorf("Dune oldest first = %v, want Dune then Dune: Part Two", got)
	}
	out = call(t, "library_items", map[string]any{"query": "Alien", "types": "Movie", "sort": "name", "desc": true})
	if got := names(t, out["items"], "items"); len(got) == 0 || got[0] != "Aliens" {
		t.Errorf("Alien by name, last first = %v, want Aliens first", got)
	}
	// a page of the sorted matches, and the count of them all
	out = call(t, "library_items", map[string]any{"library": "Movies", "query": "Dune", "sort": "year", "desc": true, "limit": 1, "offset": 1})
	if got := names(t, out["items"], "items"); !slices.Equal(got, []string{"Dune"}) || num(t, out["total"], "total") != 2 {
		t.Errorf("the second of Dune newest first = %v of %v, want Dune of 2", got, out["total"])
	}
}
