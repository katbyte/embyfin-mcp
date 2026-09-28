//go:build integration

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// What the read tools answer about the files behind an item, its numbers and
// its people, where they used to answer with confidence and be wrong.

// filesIn is every file a list of rows names, each with the height read off
// it: a row's own path, and every file in its versions. A row's facts must be
// its own path's file, not the tallest of its versions.
func filesIn(t *testing.T, rows []map[string]any) map[string]int {
	t.Helper()

	got := map[string]int{}
	for _, r := range rows {
		versions := rowsOf(r["versions"])
		for _, v := range versions {
			got[str(v["path"])] = numOr0(v["height"])
		}
		if len(versions) > 0 && numOr0(r["height"]) != got[str(r["path"])] {
			t.Errorf("%s reads %v high beside a %dp file at its path: its facts are another file's", r["id"], r["height"], got[str(r["path"])])
		}
		if len(versions) == 0 {
			got[str(r["path"])] = numOr0(r["height"])
		}
	}

	return got
}

// only keeps the files under a folder.
func only(files map[string]int, under string) map[string]int {
	out := map[string]int{}
	for path, h := range files {
		if strings.HasPrefix(path, under) {
			out[path] = h
		}
	}

	return out
}

// The messy Blade Runner's two files, 1080p and 2160p in one folder: Jellyfin
// folds them into one film, and no item query lists the second file on its
// own; Emby lists each as a film. Every bulk read names both files, each with
// its own facts, on both servers: a caller comparing the folder against the
// library read the one Jellyfin folded in as not in the library.
func TestAFilmInTwoFilesNamesBoth(t *testing.T) {
	dir := "/media/messy-movies/" + messyBladeRunner + "/"
	want := map[string]int{dir + messyBladeRunner + " - 1080p.mp4": 1080, dir + messyBladeRunner + " - 2160p.mp4": 2160}
	entries := 2
	if isJellyfin() {
		entries = 1
	}

	items := rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "types": "Movie", "query": "Blade Runner"})["items"], "items")
	if got := filesIn(t, items); len(items) != entries || !mapsEqual(got, want) {
		t.Errorf("library_items = %d entries holding %v, want %d holding %v", len(items), got, entries, want)
	}
	found := rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "78", "type": "movie"})["items"], "items")
	if got := only(filesIn(t, found), dir); !mapsEqual(got, want) {
		t.Errorf("item_find_by_metadata_id tmdb 78 holds %v in the messy folder, want %v", got, want)
	}
	// the export, which is what a caller compares a folder with
	path := filepath.Join(t.TempDir(), "films.jsonl")
	call(t, "library_export", map[string]any{"path": path, "library": "Messy Movies", "types": "Movie"})
	if got := only(filesIn(t, jsonLines(t, path)), dir); !mapsEqual(got, want) {
		t.Errorf("library_export holds %v, want %v", got, want)
	}
	// and one read of an entry answers for the file at its path, on both
	// servers, with every file beside it
	var versionIDs []string
	for _, it := range items {
		got := call(t, "item_get", map[string]any{"id": str(it["id"])})
		if num(t, got["height"], "height") != want[str(got["path"])] || !mapsEqual(filesIn(t, []map[string]any{got}), want) {
			t.Errorf("item_get %s = %v high at %v, versions %v", it["id"], got["height"], got["path"], got["versions"])
		}
		for _, v := range rowsOf(got["versions"]) {
			if !slices.Contains(versionIDs, str(v["id"])) {
				versionIDs = append(versionIDs, str(v["id"]))
			}
		}
	}
	// every version's own id is an item's to the tools that read one, as it
	// is to the server: on Jellyfin the folded file's id is found by no item
	// query, and was refused as an id nothing has (on Emby, which shows the
	// two as one film in a user's view, each entry lists both)
	if len(versionIDs) != 2 {
		t.Fatalf("version ids = %v, want one for each file", versionIDs)
	}
	for _, id := range versionIDs {
		got := call(t, "item_get", map[string]any{"id": id})
		if h, ok := want[str(got["path"])]; !ok || num(t, got["height"], "height") != h {
			t.Errorf("item_get of version %s = %v high at %v, want the facts of the file at its path", id, got["height"], got["path"])
		}
		for _, tool := range []string{"item_artwork", "item_subtitle_search", "item_similar"} {
			if _, err := invoke(tool, map[string]any{"id": id}); err != nil {
				t.Errorf("%s of version %s: %v", tool, id, err)
			}
		}
	}
}

// mapsEqual says whether two maps hold the same keys and values.
func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}

	return true
}

// A second file of the messy Severance's first episode, a 720p copy beside
// its 360p one and named as the same episode: Jellyfin folds it into the
// episode, Emby holds it as an episode of its own. library_episodes and
// show_episodes_exist name both files, each with its own height, and on
// Jellyfin say the copy is folded into the episode, whose delete takes both.
func TestAnEpisodeInTwoFilesNamesBoth(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	series := findItem(t, "Messy Shows", "Series", "Severance")
	season := "/media/messy-shows/Severance/Season 01/"
	pilot := func() map[string]int {
		out, err := invoke("library_episodes", map[string]any{"series_id": series, "season": 1})
		if err != nil {
			return nil
		}
		var eps []map[string]any
		for _, row := range rowsOf(out["episodes"]) {
			if numOr0(row["episode"]) == 1 {
				eps = append(eps, row)
			}
		}

		return filesIn(t, eps)
	}
	want := map[string]int{season + "Severance S01E01.mp4": 360, season + "Severance S01E01 - 720p.mp4": 720}

	t.Cleanup(func() { scanUntilTrue(t, "Messy Shows", func() bool { return len(pilot()) == 1 }) })
	stageFile(t, filepath.Join(dataDir(), "messy-shows", "Severance", "Season 01", "Severance S01E01 - 720p.mp4"), fixtureVideo(t, "shows", "Severance", "Season 01", "Severance S01E01.mp4"))
	stageFile(t, filepath.Join(dataDir(), "messy-shows", "Severance", "Season 01", "Severance S01E01 - 720p.nfo"), episodeNfo("Good News About Hell", 1, 1))
	scanUntilTrue(t, "Messy Shows", func() bool { return len(pilot()) == 2 })

	if got := pilot(); !mapsEqual(got, want) {
		t.Errorf("library_episodes holds S01E01 in %v, want %v", got, want)
	}
	for _, quality := range []bool{false, true} {
		out := call(t, "show_episodes_exist", map[string]any{"series_id": series, "episodes": []map[string]any{{"season": 1, "episode": 1}}, "quality": quality})
		row := rows(t, out["episodes"], "episodes")[0]
		copies := rows(t, row["other_copies"], "other_copies")
		got := map[string]int{str(row["path"]): numOr0(row["height"])}
		for _, c := range copies {
			got[str(c["path"])] = numOr0(c["height"])
			// Jellyfin holds the second file as a version of the row's episode
			if folded := str(c["version_of"]); isJellyfin() != (folded == str(row["id"])) {
				t.Errorf("quality %v: the other copy %v, version_of %q beside episode %v", quality, c["path"], folded, row["id"])
			}
		}
		if !boolOf(row["exists"]) || len(copies) != 1 || !slices.Equal(sortedKeys(got), sortedKeys(want)) {
			t.Errorf("quality %v: S01E01 held in %v, want %v", quality, got, want)
		}
		if quality && !mapsEqual(got, want) {
			t.Errorf("S01E01's files read %v high, want %v", got, want)
		}
		// the other copy's id is an item's, folded version or not
		for _, c := range copies {
			if one, err := invoke("item_get", map[string]any{"id": str(c["id"])}); err != nil || str(one["path"]) != str(c["path"]) {
				t.Errorf("item_get of the other copy %v = %v, %v", c["id"], one["path"], err)
			}
		}
	}
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)

	return out
}

// Two files of the messy Severance named without SxxEyy: one in its Season
// 01 folder, one at the show's root. Jellyfin holds both with no season and
// no episode number, and the root one in a "Season Unknown"; Emby holds the
// root one with neither, and the other in season 1 with no episode number.
// None of them is a special, which season 0 would say, and an episode read as
// absent may be any of them.
func TestEpisodesWithNoNumbers(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	series := findItem(t, "Messy Shows", "Series", "Severance")
	root := "/media/messy-shows/Severance/"
	inSeason, atRoot := root+"Season 01/Severance - Unnumbered.mp4", root+"Severance Behind the Scenes.mp4"
	byPath := func() map[string]map[string]any {
		out, err := invoke("library_episodes", map[string]any{"series_id": series})
		if err != nil {
			return nil
		}
		got := map[string]map[string]any{}
		for _, row := range rowsOf(out["episodes"]) {
			got[str(row["path"])] = row
		}

		return got
	}
	held := len(byPath())

	t.Cleanup(func() { scanUntilTrue(t, "Messy Shows", func() bool { return len(byPath()) == held }) })
	video := fixtureVideo(t, "messy-shows", "Severance", "Season 01", "Severance S01E01.mp4")
	stageFile(t, filepath.Join(dataDir(), "messy-shows", "Severance", "Season 01", "Severance - Unnumbered.mp4"), video)
	stageFile(t, filepath.Join(dataDir(), "messy-shows", "Severance", "Severance Behind the Scenes.mp4"), video)
	scanUntilTrue(t, "Messy Shows", func() bool { return len(byPath()) == held+2 })

	rows := byPath()
	for path, season := range map[string]any{inSeason: 1.0, atRoot: nil} {
		if isJellyfin() {
			season = nil
		}
		row := rows[path]
		if row == nil || row["episode"] != nil || row["season"] != season {
			t.Errorf("%s = %v, want season %v and no episode number", path, row, season)
		}
	}

	// a season the server holds no number for is not the specials
	var unknown int
	for _, s := range rowsOf(call(t, "show_seasons", map[string]any{"series_id": series})["seasons"]) {
		if s["season"] == nil {
			unknown++
		} else if num(t, s["season"], "season") == 0 {
			t.Errorf("the messy Severance has a season 0: %v", s)
		}
	}
	if want := map[bool]int{true: 1, false: 0}[isJellyfin()]; unknown != want {
		t.Errorf("%d seasons with no number, want %d", unknown, want)
	}

	// an absence beside them is not proof, and they are named
	out := call(t, "show_episodes_exist", map[string]any{"series_id": series, "episodes": []map[string]any{{"season": 1, "episode": 9}}})
	if w := str(out["warning"]); num(t, out["absent"], "absent") != 1 || !strings.Contains(w, inSeason) || !strings.Contains(w, atRoot) {
		t.Errorf("S01E09 = %v, want absent and a warning naming both files", out)
	}
	missing := strs(t, call(t, "show_missing", map[string]any{"series_id": series})["unnumbered_files"], "unnumbered_files")
	if !slices.Contains(missing, inSeason) || !slices.Contains(missing, atRoot) {
		t.Errorf("show_missing unnumbered_files = %v, want both", missing)
	}
	// and a season asked for says the show holds files no season lists
	if w := str(call(t, "library_episodes", map[string]any{"series_id": series, "season": 1})["warning"]); !strings.Contains(w, atRoot) {
		t.Errorf("season 1's warning = %q, want the root file named", w)
	}
}

// item_get keeps every director and writer however long the cast: Jellyfin
// lists Breaking Bad's pilot's twenty-one actors and guest stars before its
// director and writer, and the first fifteen people were all cast.
func TestItemGetKeepsTheCrew(t *testing.T) {
	bb := findItem(t, "Shows", "Series", "Breaking Bad")
	var pilot string
	for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": bb})["episodes"], "episodes") {
		if numOr0(e["season"]) == 1 && numOr0(e["episode"]) == 1 {
			pilot = str(e["id"])
		}
	}
	people := rows(t, call(t, "item_get", map[string]any{"id": pilot})["people"], "people")
	for _, credit := range []string{"Director", "Writer"} {
		if !slices.ContainsFunc(people, func(p map[string]any) bool { return str(p["name"]) == "Vince Gilligan" && str(p["type"]) == credit }) {
			t.Errorf("the pilot's people lack Vince Gilligan as %s: %v", credit, people)
		}
	}
	if len(people) != 15 {
		t.Errorf("the pilot lists %d people, want the 15 the cut keeps", len(people))
	}
}

// A TV director is credited on the episodes he directed, not on the series:
// person_get read films and series alone and answered him with no credits.
func TestPersonGetOfATVDirector(t *testing.T) {
	out := call(t, "person_get", map[string]any{"person": "Adam Bernstein"})
	if len(rowsOf(out["credits"])) != 0 {
		t.Errorf("Adam Bernstein's film and series credits = %v, want none", out["credits"])
	}
	shows := rows(t, out["episode_credits"], "episode_credits")
	if len(shows) != 1 || str(shows[0]["series"]) != "Breaking Bad" {
		t.Fatalf("episode credits = %v, want Breaking Bad's", shows)
	}
	episodes := rows(t, shows[0]["episodes"], "episodes")
	numbers := make([]int, 0, len(episodes))
	for _, e := range episodes {
		if str(e["credit"]) != "Director" || num(t, e["season"], "season") != 1 {
			t.Errorf("a credit = %v, want a season 1 episode he directed", e)
		}
		numbers = append(numbers, num(t, e["episode"], "episode"))
	}
	if !slices.Equal(numbers, []int{2, 3}) {
		t.Errorf("episodes directed = %v, want 2 and 3", numbers)
	}
}

// A parental limit of the lowest rating is a limit: Jellyfin scores G at 0,
// and user_get dropped a 0 as no limit at all, reading a child's account as
// unrestricted.
func TestAParentalLimitAtTheBottom(t *testing.T) {
	if got := call(t, "user_get", map[string]any{"user": "alice"}); got["max_parental_rating"] != nil {
		t.Fatalf("alice is limited before the test limits her: %v", got)
	}
	limit := limitAlice(t, "G")
	got := call(t, "user_get", map[string]any{"user": "alice"})
	if got["max_parental_rating"] == nil || num(t, got["max_parental_rating"], "max_parental_rating") != limit {
		t.Errorf("alice limited at G (%d) reads %v", limit, got["max_parental_rating"])
	}
	if isJellyfin() && limit != 0 {
		t.Errorf("Jellyfin scores G %d, want 0: the test no longer reaches the bottom of the scale", limit)
	}
}

// library_episodes of one half of a show split across two entries answers
// for that half, and says the show has another: show_episodes_exist and
// show_missing read both together, and a caller reading this half alone
// took the other half's episodes for missing.
func TestLibraryEpisodesOfASplitShow(t *testing.T) {
	halves := map[string]string{}
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Shows", "query": "The Wire", "limit": 50})["items"], "items") {
		if str(it["name"]) == "The Wire" {
			halves[str(it["path"])] = str(it["id"])
		}
	}
	old, renamed := halves["/media/messy-shows/The Wire"], halves["/media/messy-shows/The Wire (2002)"]
	if old == "" || renamed == "" {
		t.Fatalf("The Wire's entries = %v", halves)
	}
	for id, other := range map[string]string{old: renamed, renamed: old} {
		out := call(t, "library_episodes", map[string]any{"series_id": id})
		if dup := strs(t, out["duplicate_entries"], "duplicate_entries"); !slices.Equal(dup, []string{other}) || !strings.Contains(str(out["warning"]), "under 2 entries") {
			t.Errorf("library_episodes of %s = duplicate_entries %v, warning %q; want the other half named", id, dup, out["warning"])
		}
	}
	// a show held once says nothing of the kind
	if out := call(t, "library_episodes", map[string]any{"series_id": findItem(t, "Shows", "Series", "Breaking Bad")}); out["duplicate_entries"] != nil || out["warning"] != nil {
		t.Errorf("Breaking Bad = %v", out)
	}
}

// A forced subtitle track shows only the lines in another language than the
// audio's: an English forced track beside the German audio of the messy
// Despecialized Edition is no English subtitles to watch it by.
func TestAForcedSubtitleIsNotSubtitles(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	film := findItem(t, "Messy Movies", "Movie", despecialized)
	subs := func() []string {
		out, err := invoke("item_get", map[string]any{"id": film})
		if err != nil {
			return nil
		}
		var s []string
		for _, v := range rowsOfAny(out["subtitles"]) {
			s = append(s, str(v))
		}
		return s
	}
	t.Cleanup(func() { scanUntilTrue(t, "Messy Movies", func() bool { return len(subs()) == 0 }) })
	stageFile(t, filepath.Join(dataDir(), "messy-movies", messyDespecialized, messyDespecialized+".eng.forced.srt"), []byte("1\n00:00:00,000 --> 00:00:00,900\nA sign.\n"))
	scanUntilTrue(t, "Messy Movies", func() bool { return len(subs()) == 1 })

	want := "en (forced, external)"
	if isJellyfin() {
		want = "eng (forced, external)"
	}
	if got := subs(); !slices.Equal(got, []string{want}) {
		t.Errorf("subtitles = %v, want [%s]", got, want)
	}
	if got := findings(t, call(t, "audit_language", map[string]any{"language": "eng", "find": "subtitles", "library": "Messy Movies"})); slices.Contains(got, despecialized) {
		t.Errorf("English subtitles = %v: a forced track counted as them", got)
	}
	if got := findings(t, call(t, "audit_language", map[string]any{"language": "eng", "find": "unwatchable", "library": "Messy Movies"})); !slices.Contains(got, despecialized) {
		t.Errorf("unwatchable in English = %v, want %s still: a forced track is no subtitles to follow it by", got, despecialized)
	}
}

// library_recent says when its limit cut the period: nine films were added
// to Movies today, and the newest alone is not all of them.
func TestLibraryRecentSaysWhenThereIsMore(t *testing.T) {
	out := call(t, "library_recent", map[string]any{"library": "Movies", "limit": 1})
	if len(rows(t, out["items"], "items")) != 1 || !boolOf(out["more"]) {
		t.Errorf("a limit of 1 = %v, want one item and more", out)
	}
	if out = call(t, "library_recent", map[string]any{"library": "Movies", "limit": 50}); len(rows(t, out["items"], "items")) != len(movies) || boolOf(out["more"]) {
		t.Errorf("a limit past the period = %d items, more %v; want all %d and no more", len(rowsOf(out["items"])), out["more"], len(movies))
	}
}

// An episode playing is named by its show and number: its own title alone,
// "Pilot", is Breaking Bad's and Limitless's both.
func TestSessionPlayingAnEpisode(t *testing.T) {
	device, token := signInPlayer(t)
	bb := findItem(t, "Shows", "Series", "Breaking Bad")
	var pilot string
	for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": bb, "season": 1})["episodes"], "episodes") {
		if numOr0(e["episode"]) == 1 {
			pilot = str(e["id"])
		}
	}
	// a play begun and stopped can leave the pilot marked or counted as
	// played for alice, which the audits of what nobody has watched would
	// read after this
	t.Cleanup(func() {
		if _, err := invoke("item_set_state", map[string]any{"id": pilot, "user": "alice", "watched": false}); err != nil {
			t.Errorf("putting the pilot back to unwatched for alice: %v", err)
		}
	})
	p := startPlaying(t, token, pilot)
	var row map[string]any
	if !eventually(func() bool { row = sessionOn(t, device); return row != nil && row["now_playing"] != nil }) {
		t.Fatalf("the player never showed the pilot playing: %v", row)
	}
	if str(row["now_playing"]) != "Breaking Bad S01E01 Pilot" || str(row["now_playing_id"]) != pilot {
		t.Errorf("playing = %v, want Breaking Bad S01E01 Pilot", row)
	}
	p.stop(5_000_000)
}

// An id no item has is refused as that by the reads that look the item's
// images and subtitles up, where Emby answers them with a bare 500 and
// Jellyfin with a 404.
func TestItemReadsOfAnUnknownID(t *testing.T) {
	for _, tool := range []string{"item_artwork", "item_subtitle_search"} {
		if msg := callErr(t, tool, map[string]any{"id": unknownID()}); !strings.Contains(msg, "no item with id "+unknownID()) {
			t.Errorf("%s of an unknown id: %s", tool, msg)
		}
	}
}

// A read of every match in pages of two, each request re-reading the two
// before it, reads what one read of the whole library does, each film once,
// on both servers. And a film scanned in part way through it fails nothing:
// every film there from the start is read once, and the new one is read or
// named as added. Triangle is dated 2015 on disk, so it sorts among the first
// on a server that dates an item by its file, behind where the read has got
// to - the case a read by plain offset got wrong.
func TestReadAllOnTheServer(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	client, err := embyfin.New(backend, os.Getenv("EMBYFIN_SERVER"), os.Getenv("EMBYFIN_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	opts := embyfin.SearchOptions{ParentID: str(call(t, "library_get", map[string]any{"library": "Messy Movies"})["id"]), IncludeItemTypes: "Movie", Fields: "Path"}
	ids := func(pageSize int, during func(page int)) ([]string, embyfin.ReadResult) {
		t.Helper()

		o := opts
		o.PageSize = pageSize
		var got []string
		pages := 0
		result, err := client.ReadAll(t.Context(), o, embyfin.ToAnswer, func(items []embyfin.Item) bool {
			for _, it := range items {
				got = append(got, it.ID)
			}
			pages++
			if during != nil {
				during(pages)
			}
			return true
		})
		if err != nil {
			t.Fatalf("a read in pages of %d: %v", pageSize, err)
		}

		return got, result
	}

	whole, _ := ids(0, nil)
	if len(whole) != messyMovies() {
		t.Fatalf("one read holds %d films, want %d", len(whole), messyMovies())
	}
	paged, result := ids(2, nil)
	slices.Sort(whole)
	sorted := slices.Clone(paged)
	slices.Sort(sorted)
	if !slices.Equal(sorted, whole) || result.Read != len(whole) || result.Changed() != "" {
		t.Errorf("in pages of two = %v (%+v, %q), want each of %v once", paged, result, result.Changed(), whole)
	}

	// a film scanned in after the first page
	dir := filepath.Join(dataDir(), "messy-movies", "Triangle (2009)")
	file := filepath.Join(dir, "Triangle (2009).mp4")
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
		if err := scanUntil("Messy Movies", messyMovies()); err != nil {
			t.Error(err)
		}
	})
	grown, result := ids(2, func(page int) {
		if page != 1 {
			return
		}
		stageFile(t, file, fixtureVideo(t, "messy-movies", messyArrival, messyArrival+".mp4"))
		old := time.Date(2015, 6, 1, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(file, old, old); err != nil {
			t.Fatal(err)
		}
		if err := scanUntil("Messy Movies", messyMovies()+1); err != nil {
			t.Fatal(err)
		}
	})
	counts := map[string]int{}
	for _, id := range grown {
		counts[id]++
	}
	for _, id := range whole {
		if counts[id] != 1 {
			t.Errorf("film %s read %d times while the library grew, want once", id, counts[id])
		}
	}
	t.Logf("a film scanned in mid-read: %d read of %d there at the start; %q", len(grown), len(whole), result.Changed())
	if added := len(grown) - len(whole); added != 0 && added != 1 {
		t.Errorf("with a film scanned in mid-read: %d read of %d, want the new one read at most once", len(grown), len(whole))
	}
	// the count moved between two requests, which is seen whether the new
	// film landed behind the read or ahead of it
	if want := fmt.Sprintf("the library changed while it was read: items added or removed during it may be missing, or listed though gone (%d matched when the read began, %d when it ended, and %d were read)", len(whole), len(whole)+1, len(grown)); result.Changed() != want {
		t.Errorf("with a film scanned in mid-read: %q, want %q", result.Changed(), want)
	}
}

// One film taken out and another put in, both behind a read and between the
// same two of its requests, leave the count and the last films read where
// they were: reading every film again at the end finds the one put in and
// not the one taken out, on both servers. (What the server saved during the
// read did not name it on Jellyfin, which put Triangle back at a path it had
// just held without saving it anew.) The Thirteenth Floor is staged first, dated 2014 on disk so
// it sorts first and is in the first page (its sidecar is the one the
// versions test stages, whose lookups are recorded); after that page it goes,
// and Triangle comes in dated 2015, sorting before every fixture: behind
// where the read has got to.
func TestReadAllSeesOneOutAndOneInBehindIt(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	client, err := embyfin.New(backend, os.Getenv("EMBYFIN_SERVER"), os.Getenv("EMBYFIN_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	library := str(call(t, "library_get", map[string]any{"library": "Messy Movies"})["id"])
	out := filepath.Join(dataDir(), "messy-movies", "The Thirteenth Floor (1999)")
	in := filepath.Join(dataDir(), "messy-movies", "Triangle (2009)")
	t.Cleanup(func() {
		for _, dir := range []string{out, in} {
			if err := os.RemoveAll(dir); err != nil {
				t.Error(err)
			}
		}
		if err := scanUntil("Messy Movies", messyMovies()); err != nil {
			t.Error(err)
		}
	})
	dated := func(file string, year int) {
		at := time.Date(year, 6, 1, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(file, at, at); err != nil {
			t.Fatal(err)
		}
	}
	stageFile(t, filepath.Join(out, "The Thirteenth Floor (1999).mp4"), fixtureVideo(t, "anime-src", "special.mp4"))
	stageFile(t, filepath.Join(out, "movie.nfo"), []byte(`<?xml version="1.0" encoding="utf-8"?>
<movie>
  <title>Interstellar</title>
  <year>2014</year>
  <tmdbid>157336</tmdbid>
  <uniqueid type="tmdb" default="true">157336</uniqueid>
  <imdbid>tt0816692</imdbid>
  <uniqueid type="imdb">tt0816692</uniqueid>
</movie>
`))
	dated(filepath.Join(out, "The Thirteenth Floor (1999).mp4"), 2014)
	if err := scanUntil("Messy Movies", messyMovies()+1); err != nil {
		t.Fatal(err)
	}
	// which of the two the library holds, by the folder each is in
	holds := func() (hasOut, hasIn bool) {
		items, _, serr := client.Search(t.Context(), embyfin.SearchOptions{ParentID: library, IncludeItemTypes: "Movie", Fields: "Path", Limit: 1000})
		if serr != nil {
			t.Fatal(serr)
		}
		for _, it := range items {
			hasOut = hasOut || strings.Contains(it.Path, "/The Thirteenth Floor (1999)/")
			hasIn = hasIn || strings.Contains(it.Path, "/Triangle (2009)/")
		}
		return hasOut, hasIn
	}

	var read []string
	pages := 0
	result, err := client.ReadAll(t.Context(), embyfin.SearchOptions{ParentID: library, IncludeItemTypes: "Movie", Fields: "Path", PageSize: 2}, embyfin.ToAnswer, func(items []embyfin.Item) bool {
		for _, it := range items {
			read = append(read, it.Path)
		}
		pages++
		if pages == 1 {
			if err := os.RemoveAll(out); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(in, "Triangle (2009).mp4")
			stageFile(t, file, fixtureVideo(t, "messy-movies", messyArrival, messyArrival+".mp4"))
			dated(file, 2015)
			scanUntilTrue(t, "Messy Movies", func() bool {
				hasOut, hasIn := holds()
				return !hasOut && hasIn
			})
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	readOut, readIn := false, false
	for _, path := range read {
		readOut = readOut || strings.Contains(path, "/The Thirteenth Floor (1999)/")
		readIn = readIn || strings.Contains(path, "/Triangle (2009)/")
	}
	t.Logf("one film out and one in behind the read: %d read; %q", len(read), result.Changed())
	if !readOut || readIn {
		t.Errorf("read the film taken out %v and the one put in %v: want the first page to hold the one taken out, and the one put in to land behind the read", readOut, readIn)
	}
	n := messyMovies() + 1
	if want := fmt.Sprintf("the library changed while it was read: items added or removed during it may be missing, or listed though gone (%d matched when the read began, %d when it ended, and %d were read, with 1 there at the end not among them, and 1 of them gone by the end)", n, n, n); result.Changed() != want {
		t.Errorf("with one film out and one in behind the read: %q, want %q", result.Changed(), want)
	}
}

// A title search that matches more than a few items is counted and paged
// whole, on both servers. Emby counts 0 to a search sent with a limit, so
// total read 0 and a sorted search said "the server says 0 match but 8
// different items were read"; Jellyfin counts at most three times the limit
// and lists nothing past it, so a caller paging by three found the tenth
// match on never.
func TestATitleSearchIsCountedAndPagedWhole(t *testing.T) {
	const query, types = "the", "Movie,Series,Episode,Audio,MusicAlbum"
	first := call(t, "library_items", map[string]any{"query": query, "types": types, "limit": 3})
	if first["total"] == nil {
		t.Fatalf("a search for %q has no total: %v", query, first)
	}
	total := num(t, first["total"], "total")
	if total <= 9 {
		t.Fatalf("a search for %q matches %d items, too few to page past three times three", query, total)
	}
	seen := map[string]int{}
	for offset := 0; ; offset += 3 {
		out := call(t, "library_items", map[string]any{"query": query, "types": types, "limit": 3, "offset": offset})
		if note := str(out["note"]); note != "" || num(t, out["total"], "total") != total {
			t.Errorf("from %d: total %v, note %q; want %d and none", offset, out["total"], note, total)
		}
		page := rows(t, out["items"], "items")
		for _, it := range page {
			seen[str(it["id"])]++
		}
		if len(page) < 3 {
			break
		}
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("%s listed %d times across the pages", id, n)
		}
	}
	if len(seen) != total {
		t.Errorf("paging by three listed %d different items of the %d counted", len(seen), total)
	}
	// and sorted, the same matches, counted the same, with nothing to say
	sorted := call(t, "library_items", map[string]any{"query": query, "types": types, "sort": "name", "limit": 100})
	if num(t, sorted["total"], "total") != total || len(rows(t, sorted["items"], "items")) != total || str(sorted["note"]) != "" {
		t.Errorf("sorted by name: total %v, %d items, note %q; want %d, all of them, none", sorted["total"], len(rows(t, sorted["items"], "items")), str(sorted["note"]), total)
	}
}

// An edit of a special keeps it season 0. The season number was held as a
// plain number that left out a 0 when the item went back to the server, and
// Jellyfin cleared it: tagging The Expanse's first special made it an episode
// of no season.
func TestAnEditKeepsASpecialsSeason(t *testing.T) {
	expanse := findItem(t, "Shows", "Series", "The Expanse")
	var special map[string]any
	for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": expanse, "season": 0})["episodes"], "episodes") {
		if numOr0(e["episode"]) == 1 {
			special = e
		}
	}
	if special == nil || special["season"] == nil || num(t, special["season"], "season") != 0 {
		t.Fatalf("The Expanse's first special = %v, want it in season 0", special)
	}
	id := str(special["id"])
	season := func() any { return call(t, "item_get", map[string]any{"id": id})["season"] }
	t.Cleanup(func() {
		if _, err := invoke("item_edit", map[string]any{"ids": []any{id}, "remove_tags": []any{"zzyzx-special"}}); err != nil {
			t.Errorf("taking the tag back off: %v", err)
		}
	})
	call(t, "item_edit", map[string]any{"ids": []any{id}, "add_tags": []any{"zzyzx-special"}})
	if got := season(); got == nil || num(t, got, "season") != 0 {
		t.Errorf("after an edit the special's season = %v, want 0", got)
	}
	call(t, "item_edit", map[string]any{"ids": []any{id}, "remove_tags": []any{"zzyzx-special"}})
	if got := season(); got == nil || num(t, got, "season") != 0 {
		t.Errorf("after a second edit the special's season = %v, want 0", got)
	}
	still := false
	for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": expanse, "season": 0})["episodes"], "episodes") {
		still = still || str(e["id"]) == id
	}
	if !still {
		t.Error("after the edits the special is gone from season 0")
	}
}

// rowsOfAny is a JSON list's values, none for a field that is not one.
func rowsOfAny(v any) []any {
	if list, ok := v.([]any); ok {
		return list
	}

	return nil
}
