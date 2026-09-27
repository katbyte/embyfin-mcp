//go:build integration

package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Journeys through what a write reaches past the item it names: a watched
// mark given to a series or a collection, a library renamed under an account
// given it alone, a collection made under a name another collection was
// first made with, and the poster beside a film when the server fetches or is
// given another.

// watchedEpisodes is the ids of a series' episodes a user has watched, read
// from the server's list in the user's view: straight after a series is
// marked, Jellyfin's single-item read still answers each episode's old state.
func watchedEpisodes(t *testing.T, user, series string) []string {
	t.Helper()

	var ids []string
	for _, it := range rows(t, call(t, "library_items", map[string]any{"user": user, "watched": "watched", "types": "Episode", "library": "Shows", "limit": 200})["items"], "items") {
		if str(it["series"]) == series {
			ids = append(ids, str(it["id"]))
		}
	}
	slices.Sort(ids)

	return ids
}

// A watched mark on a series goes on every episode under it, on both
// servers, and on a collection on every film it holds; unwatched clears
// them, play counts and dates too. item_set_state says how many items the
// mark reached - the series and its episodes as the user sees them - and
// what each was before, which no tool puts back. A film's or an episode's
// mark reaches its other copies on Emby, which the answer names, and not on
// Jellyfin.
func TestAWatchedMarkOnASeries(t *testing.T) {
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	var episodes []string
	for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": series})["episodes"], "episodes") {
		episodes = append(episodes, str(e["id"]))
	}
	slices.Sort(episodes)
	if len(episodes) != 3 {
		t.Fatalf("Breaking Bad holds %d episodes, want the fixture's 3", len(episodes))
	}
	unmarkLater(t, "alice", append([]string{series}, episodes...)...)
	call(t, "item_set_state", map[string]any{"id": series, "user": "alice", "watched": false})
	if got := watchedEpisodes(t, "alice", "Breaking Bad"); len(got) != 0 {
		t.Fatalf("before the mark alice has watched %v", got)
	}

	out := call(t, "item_set_state", map[string]any{"id": series, "user": "alice", "watched": true})
	if num(t, out["reaches"], "reaches") != 1+len(episodes) || num(t, out["items_changed"], "items_changed") < len(episodes) {
		t.Errorf("watched on the series = %v, want the series and its %d episodes reached, each episode changed", out, len(episodes))
	}
	if got := watchedEpisodes(t, "alice", "Breaking Bad"); !slices.Equal(got, episodes) {
		t.Errorf("after the series was marked alice has watched %v, want every episode %v", got, episodes)
	}

	// unwatched takes the plays away for good: was says what they were
	out = call(t, "item_set_state", map[string]any{"id": series, "user": "alice", "watched": false})
	was := rows(t, out["was"], "was")
	played := 0
	for _, row := range was[1:] {
		if boolOf(row["played"]) && num(t, row["play_count"], "play_count") >= 1 && str(row["last_played"]) != "" {
			played++
		}
	}
	if len(was) != 1+len(episodes) || played != len(episodes) {
		t.Errorf("unwatched on the series: was = %v, want each episode played, with its plays and date", was)
	}
	if note := str(out["note"]); !strings.Contains(note, fmt.Sprintf("the play counts, last played dates and resume points of the %d items under Breaking Bad are cleared for good: no tool restores them", len(episodes))) {
		t.Errorf("unwatched on the series: note = %q, want it to say the plays are gone for good", note)
	}
	if got := watchedEpisodes(t, "alice", "Breaking Bad"); len(got) != 0 {
		t.Errorf("after unwatched on the series alice has watched %v", got)
	}

	// a collection: every film in it
	arrival, alien := findItem(t, "Movies", "Movie", "Arrival"), findItem(t, "Movies", "Movie", "Alien")
	unmarkLater(t, "alice", arrival, alien)
	col := str(call(t, "collection_create", map[string]any{"name": "Zzyzx Counted", "item_ids": []any{arrival, alien}})["id"])
	deleteLater(t, "collection_delete", "collection", col)
	out = call(t, "item_set_state", map[string]any{"id": col, "user": "alice", "watched": true})
	if num(t, out["reaches"], "reaches") != 3 || num(t, out["items_changed"], "items_changed") < 2 {
		t.Errorf("watched on a collection = %v, want the collection and its two films", out)
	}
	for _, id := range []string{arrival, alien} {
		if w, _ := stateOf(t, id, "alice"); !w {
			t.Errorf("%s is not watched for alice after its collection was marked", id)
		}
	}
	if msg := callErr(t, "item_set_state", map[string]any{"id": series, "user": "alice", "position_s": 60}); !strings.Contains(msg, "is a Series, which holds items") {
		t.Errorf("a resume point on a series: %s", msg)
	}

	// a film's other copies carry its state on Emby, which keeps it by
	// provider id, and the answer names the ones that changed: the clean
	// Alien marked, and the messy library's two copies of it (its TMDB id)
	// with it. Read one by one: Emby's list in alice's view shows the two as
	// one film's versions, one row with one of their states. Jellyfin keeps
	// each copy's own, and names none; its list, which it answers afresh,
	// is read there
	clean := findItem(t, "Movies", "Movie", "Alien")
	var messy []string
	for _, it := range rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "348", "type": "movie"})["items"], "items") {
		if id := str(it["id"]); id != clean {
			messy = append(messy, id)
		}
	}
	slices.Sort(messy)
	if len(messy) != 2 {
		t.Fatalf("Alien is held %d times besides the clean copy, want the messy library's 2", len(messy))
	}
	unmarkLater(t, "alice", append([]string{clean}, messy...)...)
	for _, id := range append([]string{clean}, messy...) {
		call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": false})
	}
	out = call(t, "item_set_state", map[string]any{"id": clean, "user": "alice", "watched": true})
	var copies []string
	for _, c := range rowsOf(out["copies_changed"]) {
		copies = append(copies, str(c["id"]))
	}
	slices.Sort(copies)
	var marked []string
	if isJellyfin() {
		for _, it := range rows(t, call(t, "library_items", map[string]any{"user": "alice", "watched": "watched", "types": "Movie", "library": "Messy Movies", "limit": 100})["items"], "items") {
			if slices.Contains(messy, str(it["id"])) {
				marked = append(marked, str(it["id"]))
			}
		}
	} else {
		for _, id := range messy {
			if w, _ := stateOf(t, id, "alice"); w {
				marked = append(marked, id)
			}
		}
	}
	slices.Sort(marked)
	if isJellyfin() && (len(marked) != 0 || len(copies) != 0) || !isJellyfin() && (!slices.Equal(marked, messy) || !slices.Equal(copies, messy)) {
		t.Errorf("after the clean Alien was marked alice has watched the messy copies %v and the answer names %v; want both %v on Emby and none on Jellyfin", marked, copies, messy)
	}

	// one copy of an episode held twice (The Wire's second, in each of the
	// show's two folders): on Emby, which keeps an episode's state by its
	// series' ids and its number, a mark on one marks the other, and the
	// answer names it; on Jellyfin the other is left as it was
	var twice []string
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Shows", "types": "Episode", "query": "The Detail", "limit": 10})["items"], "items") {
		twice = append(twice, str(it["id"]))
	}
	if len(twice) != 2 {
		t.Fatalf("The Wire's second episode is held %d times, want the fixture's 2", len(twice))
	}
	unmarkLater(t, "alice", twice...)
	for _, id := range twice {
		call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": false})
	}
	out = call(t, "item_set_state", map[string]any{"id": twice[0], "user": "alice", "watched": true})
	other := false
	if isJellyfin() {
		for _, it := range rows(t, call(t, "library_items", map[string]any{"user": "alice", "watched": "watched", "types": "Episode", "library": "Messy Shows", "limit": 100})["items"], "items") {
			other = other || str(it["id"]) == twice[1]
		}
	} else {
		other, _ = stateOf(t, twice[1], "alice")
	}
	named := rowsOf(out["copies_changed"])
	switch {
	case num(t, out["reaches"], "reaches") != 1:
		t.Errorf("a mark on one copy of an episode reached %v items, want the one", out["reaches"])
	case isJellyfin() && (other || len(named) != 0):
		t.Errorf("on Jellyfin a mark on one copy of an episode: the other copy watched %v, the answer names %v; want neither", other, named)
	case !isJellyfin() && (!other || len(named) != 1 || str(named[0]["id"]) != twice[1]):
		t.Errorf("on Emby a mark on one copy of an episode: the other copy watched %v, the answer names %v; want it marked and named", other, named)
	}
}

// aliceSees is how many films alice sees in a library: none when she may not
// see it at all.
func aliceSees(t *testing.T, library string) int {
	t.Helper()

	out, err := invoke("library_items", map[string]any{"library": library, "user": "alice", "types": "Movie"})
	if err != nil {
		return 0
	}

	return len(rowsOf(out["items"]))
}

// A library renamed under an account given it alone: Emby keeps the
// library's id, and the account keeps it; Jellyfin gives it a new id with
// the scan the rename starts, and the account, given the old id, sees
// nothing of it after - which library_edit's answer says, naming the
// account and the old id.
func TestRenamingALibraryGivenToOneAccount(t *testing.T) {
	const name, renamed = "Zzyzx Access", "Zzyzx Access Renamed"
	dir := filepath.Join(dataDir(), "access")
	copyFixture(t, filepath.Join(dataDir(), "movies", "Arrival (2016)"), filepath.Join(dir, "Arrival (2016)"))
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() {
		for _, n := range []string{renamed, name} {
			if _, err := invoke("library_get", map[string]any{"library": n}); err == nil {
				removeLibrary(t, n)
			}
		}
		if err := waitForExpectedScan(); err != nil {
			t.Error(err)
		}
	})
	call(t, "library_create", map[string]any{"name": name, "type": "movies", "paths": []any{"/media/access"}, "scan": true, "save_nfo": false})
	if !eventuallyWithin(scanPatience, func() bool { return movieCount(t, name) == 1 }) {
		t.Fatal("the library never held its film")
	}
	if err := waitForExpectedScan(); err != nil {
		t.Fatal(err)
	}
	restrictAlice(t, name)
	if n := aliceSees(t, name); n != 1 {
		t.Fatalf("alice, given the library, sees %d films in it", n)
	}
	was := str(call(t, "library_get", map[string]any{"library": name})["id"])

	out := call(t, "library_edit", map[string]any{"library": name, "name": renamed})
	if err := waitForExpectedScan(); err != nil {
		t.Fatal(err)
	}
	now := str(call(t, "library_get", map[string]any{"library": renamed})["id"])
	lost := strs(t, orEmptyList(out["access_lost"]), "access_lost")
	if isJellyfin() {
		if now == was || str(out["was_id"]) != was || !slices.Equal(lost, []string{"alice"}) {
			t.Errorf("the rename on Jellyfin = %v (id %s, was %s), want a new id, the old one said, and alice named", out, now, was)
		}
		if n := aliceSees(t, renamed); n != 0 {
			t.Errorf("alice sees %d films in the renamed library, want none: she was given the old id", n)
		}
		return
	}
	if now != was || out["was_id"] != nil || len(lost) != 0 {
		t.Errorf("the rename on Emby = %v (id %s, was %s), want the id kept and nobody losing it", out, now, was)
	}
	if n := aliceSees(t, renamed); n != 1 {
		t.Errorf("alice sees %d films in the renamed library, want the one", n)
	}
}

// A collection renamed keeps the folder named after the name it was first
// made with, and a new collection made under that first name reaches it:
// Emby added the items to it, and Jellyfin replaced what it held with them
// and took its first name back (both seen live). collection_create refuses
// the name - before anything is sent where the server lists the folder
// (Jellyfin), and on Emby by taking out again what the server put in - and
// the renamed collection is left as it was.
func TestACollectionUnderAnotherCollectionsFirstName(t *testing.T) {
	arrival, alien := findItem(t, "Movies", "Movie", "Arrival"), findItem(t, "Movies", "Movie", "Alien")
	col := str(call(t, "collection_create", map[string]any{"name": "Zzyzx Twin", "item_ids": []any{arrival}})["id"])
	deleteLater(t, "collection_delete", "collection", col)
	call(t, "collection_edit", map[string]any{"collection": col, "name": "Zzyzx Twice"})

	msg := callErr(t, "collection_create", map[string]any{"name": "Zzyzx Twin", "item_ids": []any{alien}})
	want := `is kept in the folder "Zzyzx Twin [boxset]"`
	if !isJellyfin() {
		want = "no new collection was made"
	}
	if !strings.Contains(msg, want) {
		t.Errorf("a collection under the renamed one's first name: %s", msg)
	}
	for _, c := range rows(t, call(t, "collection_list", nil)["collections"], "collections") {
		if str(c["name"]) == "Zzyzx Twin" {
			t.Errorf("a collection is named Zzyzx Twin after the refusal: %v", c)
			deleteLater(t, "collection_delete", "collection", str(c["id"]))
		}
	}
	got := call(t, "collection_get", map[string]any{"collection": col})
	if members := names(t, got["items"], "items"); str(got["name"]) != "Zzyzx Twice" || !slices.Equal(members, []string{"Arrival"}) {
		t.Errorf("the renamed collection after the refusal is %v holding %v, want Zzyzx Twice holding Arrival", got["name"], members)
	}
}

// rereadLater refreshes an item once the test ends, after the files keepFiles
// puts back (registered before it, it runs after it): the server then shows
// the images beside the media again, not the ones a test left it holding.
func rereadLater(t *testing.T, id string) {
	t.Helper()

	t.Cleanup(func() {
		if _, err := invoke("item_refresh", map[string]any{"id": id}); err != nil {
			t.Errorf("refreshing %s to read its files back: %v", id, err)
		}
	})
}

// posterNow is the bytes of the poster.jpg beside the clean Blade Runner, nil
// when there is none.
func posterNow(t *testing.T, poster string) []byte {
	t.Helper()

	raw, err := os.ReadFile(poster) //nolint:gosec // a fixture under the test data dir
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

// What the poster.jpg beside a film goes through when the server fetches
// images: a refresh with replace_all, and a match (item_identify_apply), each
// take it off the disk on Emby in favour of the provider's image, and leave
// it on Jellyfin; a plain refresh leaves it on both. Emby's refresh and
// match descriptions say so.
func TestThePosterBesideAFilm(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Blade Runner")
	folder := itemFolder(t, id)
	rereadLater(t, id)
	keepFiles(t, folder)
	restoreLater(t, id)
	poster := filepath.Join(folder, "poster.jpg")
	fixturePoster := fixture(t, "messy-movies/Blade Runner (1982)/poster.jpg")
	layBack := func() {
		t.Helper()
		mediaWrite(t, poster, fixturePoster)
		if !refreshed(t, id) {
			t.Fatal("the refresh that reads the poster back never ran")
		}
	}
	layBack()

	if !refreshed(t, id) || !bytes.Equal(posterNow(t, poster), fixturePoster) {
		t.Error("a plain refresh changed the poster beside the film")
	}

	nfoBefore, err := os.ReadFile(filepath.Join(folder, "movie.nfo")) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	out := call(t, "item_refresh", map[string]any{"id": id, "replace_all": true})
	if !boolOf(out["landed"]) {
		t.Fatalf("the replace_all refresh never landed: %v", out)
	}
	// the answer names the poster the refresh deleted, where it did, the
	// folder it listed, and in a library that saves nfos that the nfo
	// written over in place is not seen
	if said := posterNamed(out["removed_beside_media"]); said != !isJellyfin() {
		t.Errorf("the replace_all refresh's answer names the poster as removed: %v, want it named only on Emby (removed %v)", said, out["removed_beside_media"])
	}
	if read := str(out["folder_read"]); !strings.HasSuffix(read, "/Blade Runner (1982)") {
		t.Errorf("the refresh listed %q, want the film's folder", read)
	}
	saves := boolOf(call(t, "library_get", map[string]any{"library": "Movies"})["saves_nfo"])
	if said := strings.Contains(str(out["note"]), "saves nfos, so the server may have written the nfo beside the media over"); said != saves {
		t.Errorf("the refresh's note speaks of the nfo written over: %v, want %v (the library saves nfos: %v): %q", said, saves, saves, out["note"])
	}
	nfoAfter, err := os.ReadFile(filepath.Join(folder, "movie.nfo")) //nolint:gosec // same
	// nor does it write the nfo beside the film again, though Jellyfin's
	// library saves nfos for an edit (Emby's saves none)
	if err != nil || !bytes.Equal(nfoBefore, nfoAfter) {
		t.Errorf("a replace_all refresh wrote the nfo beside the film again (%v)", err)
	}
	if kept := posterNow(t, poster) != nil; kept != isJellyfin() {
		t.Errorf("after a replace_all refresh the poster beside the film is kept: %v (want kept only on Jellyfin)", kept)
	}
	// the refresh read the same nfo, so the film is who it was
	if boolOf(out["identity_changed"]) {
		t.Errorf("a refresh of a film its nfo names changed its identity: %v", out)
	}

	layBack()
	idx := candidateFor(t, map[string]any{"id": id, "kind": "movie"}, "78")
	out = call(t, "item_identify_apply", withCandidateIDs(t, map[string]any{"id": id, "kind": "movie", "candidate": idx}))
	if kept := posterNow(t, poster) != nil; kept != isJellyfin() {
		t.Errorf("after a match the poster beside the film is kept: %v (want kept only on Jellyfin)", kept)
	}
	if said := posterNamed(out["removed_beside_media"]); said != !isJellyfin() {
		t.Errorf("the match's answer names the poster as removed: %v, want it named only on Emby (removed %v)", said, out["removed_beside_media"])
	}
}

// nfoUnseen matches the sentence a change's note says in a library that
// saves nfos: the nfo written over in place is not seen.
var nfoUnseen = regexp.MustCompile(`(^|; |\. )the [^.;]* library saves nfos, so the server may have written the nfo beside the media over, which a listing of the folder does not show`)

// beyondNfoUnseen is a note with that sentence taken out.
func beyondNfoUnseen(note string) string {
	return strings.TrimPrefix(strings.TrimPrefix(nfoUnseen.ReplaceAllString(note, ""), ". "), "; ")
}

// posterNamed says whether a list of paths from an answer names a poster.jpg.
func posterNamed(v any) bool {
	for _, p := range rowsOfStrings(v) {
		if strings.HasSuffix(p, "/poster.jpg") {
			return true
		}
	}

	return false
}

// rowsOfStrings is a decoded list of strings, empty when it was left out.
func rowsOfStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, str(s))
	}

	return out
}

// A collection first made as "Zzyzx AC/DC" is kept by Jellyfin in the folder
// "Zzyzx AC DC [boxset]" (a character a file name cannot hold made a space),
// and after a rename a create by the first name came to that folder: the
// first-name check compared the names as given and missed it, and Jellyfin
// replaced what the collection held with the new items; the put-back, never
// having read what it held, took everything out and answered it put back,
// empty. The create is refused - before anything is sent on Jellyfin, where
// the folder is listed, and on Emby by taking out again what the server put
// in - and the renamed collection holds what it held.
func TestACollectionNamedWithASlash(t *testing.T) {
	arrival, alien, blade := findItem(t, "Movies", "Movie", "Arrival"), findItem(t, "Movies", "Movie", "Alien"), findItem(t, "Movies", "Movie", "Blade Runner")
	col := str(call(t, "collection_create", map[string]any{"name": "Zzyzx AC/DC", "item_ids": []any{arrival, alien}})["id"])
	deleteLater(t, "collection_delete", "collection", col)
	call(t, "collection_edit", map[string]any{"collection": col, "name": "Zzyzx Rock"})

	msg := callErr(t, "collection_create", map[string]any{"name": "Zzyzx AC/DC", "item_ids": []any{blade}})
	want := `is kept in the folder "Zzyzx AC DC [boxset]"`
	if !isJellyfin() {
		want = "no new collection was made"
	}
	if !strings.Contains(msg, want) {
		t.Errorf("a collection under the renamed one's first name, which holds a slash: %s", msg)
	}
	for _, c := range rows(t, call(t, "collection_list", nil)["collections"], "collections") {
		if str(c["name"]) == "Zzyzx AC/DC" {
			t.Errorf("a collection is named Zzyzx AC/DC after the refusal: %v", c)
			deleteLater(t, "collection_delete", "collection", str(c["id"]))
		}
	}
	got := call(t, "collection_get", map[string]any{"collection": col})
	members := names(t, got["items"], "items")
	slices.Sort(members)
	if str(got["name"]) != "Zzyzx Rock" || !slices.Equal(members, []string{"Alien", "Arrival"}) {
		t.Errorf("the renamed collection after the refusal is %v holding %v, want Zzyzx Rock holding Alien and Arrival", got["name"], members)
	}
}

// A collection holding a series holds the series, not its seasons and
// episodes: Jellyfin read a collection's children recursively, and a
// collection of Breaking Bad and Alien answered as six items; an episode's
// delete said it would leave the collection, and the collection's delete
// listed the season and episodes as what it held, to make it again by. A
// collection is not deleted by item_delete, which takes media off the disk.
func TestACollectionOfASeries(t *testing.T) {
	series, alien := findItem(t, "Shows", "Series", "Breaking Bad"), findItem(t, "Movies", "Movie", "Alien")
	out := call(t, "collection_create", map[string]any{"name": "Zzyzx Mixed", "item_ids": []any{series, alien}})
	col := str(out["id"])
	deleted := false
	t.Cleanup(func() {
		if !deleted {
			if _, err := invoke("collection_delete", map[string]any{"collection": col}); err != nil {
				t.Errorf("deleting the collection: %v", err)
			}
		}
	})
	if n := num(t, out["items"], "items"); n != 2 {
		t.Errorf("collection_create answered %d items, want the series and the film", n)
	}
	got := names(t, call(t, "collection_get", map[string]any{"collection": col})["items"], "items")
	slices.Sort(got)
	if !slices.Equal(got, []string{"Alien", "Breaking Bad"}) {
		t.Errorf("collection_get = %v, want Alien and Breaking Bad", got)
	}

	episode := str(rows(t, call(t, "library_episodes", map[string]any{"series_id": series})["episodes"], "episodes")[0]["id"])
	if msg := callErr(t, "item_delete", map[string]any{"id": episode}); !strings.Contains(msg, "No playlist or collection holds it") || strings.Contains(msg, "Zzyzx Mixed") {
		t.Errorf("an episode's delete, whose series a collection holds: %s", msg)
	}
	if msg := callErr(t, "item_delete", map[string]any{"id": col, "confirm": true}); !strings.Contains(msg, "which item_delete does not take: collection_delete deletes a collection") {
		t.Errorf("item_delete of a collection: %s", msg)
	}

	del := call(t, "collection_delete", map[string]any{"collection": col})
	deleted = true
	held := names(t, del["held"], "held")
	slices.Sort(held)
	if !slices.Equal(held, []string{"Alien", "Breaking Bad"}) {
		t.Errorf("collection_delete says it held %v, want Alien and Breaking Bad", held)
	}
}

// Emby shows the messy library's two copies of Alien - two folders, one TMDB
// id - as one film's versions. Deleting the plain copy said it would leave
// the playlist holding the Director's Cut, which stays, with its place in
// the list: the lists named are the ones holding what the delete takes. A
// playlist is not deleted by item_delete.
func TestDeletingOneCopyNamesOnlyItsLists(t *testing.T) {
	var plain, cut string
	for _, it := range rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "348", "type": "movie"})["items"], "items") {
		switch p := str(it["path"]); {
		case strings.Contains(p, "/messy-movies/") && strings.Contains(p, "Directors Cut"):
			cut = str(it["id"])
		case strings.Contains(p, "/messy-movies/"):
			plain = str(it["id"])
		}
	}
	if plain == "" || cut == "" {
		t.Fatalf("the messy library's Aliens: plain %q, cut %q", plain, cut)
	}
	pl := str(call(t, "playlist_create", map[string]any{"name": "Zzyzx Cut", "item_ids": []any{cut}, "media_type": "Video"})["id"])
	deleteLater(t, "playlist_delete", "playlist", pl)

	if msg := callErr(t, "item_delete", map[string]any{"id": plain}); !strings.Contains(msg, "No playlist or collection holds it") || strings.Contains(msg, "Zzyzx Cut") {
		t.Errorf("the plain copy's delete, while a playlist holds the cut: %s", msg)
	}
	if msg := callErr(t, "item_delete", map[string]any{"id": pl, "confirm": true}); !strings.Contains(msg, "which item_delete does not take: playlist_delete deletes a playlist") {
		t.Errorf("item_delete of a playlist: %s", msg)
	}
	if got := names(t, call(t, "playlist_get", map[string]any{"playlist": pl})["entries"], "entries"); len(got) != 1 {
		t.Errorf("the playlist after the refusals holds %v, want the cut", got)
	}
}

// libraryOptionsSet posts a library's options back with one changed, and
// puts them back as they were when the test ends: what no tool sets.
func libraryOptionsSet(t *testing.T, library, option string, value any) {
	t.Helper()

	opts := libraryOptions(t, library)
	was, err := json.Marshal(opts)
	if err != nil {
		t.Fatal(err)
	}
	// both servers take the library's item id here
	id := str(call(t, "library_get", map[string]any{"library": library})["id"])
	t.Cleanup(func() {
		var back map[string]any
		if err := json.Unmarshal(was, &back); err != nil {
			t.Error(err)
			return
		}
		if status, raw := api(t, http.MethodPost, "/Library/VirtualFolders/LibraryOptions", "", map[string]any{"Id": id, "LibraryOptions": back}); status/100 != 2 {
			t.Errorf("putting %s's options back: HTTP %d: %s", library, status, raw)
		}
	})
	opts[option] = value
	if status, raw := api(t, http.MethodPost, "/Library/VirtualFolders/LibraryOptions", "", map[string]any{"Id": id, "LibraryOptions": opts}); status/100 != 2 {
		t.Fatalf("setting %s's %s: HTTP %d: %s", library, option, status, raw)
	}
}

// With the library saving artwork beside its media, item_artwork_set does
// not keep the new poster in the server's own folder: Emby writes it over
// the poster.jpg there, and Jellyfin deletes poster.jpg and writes the new
// image as folder.jpg. The answer names each file.
func TestArtworkSavedBesideTheMedia(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Blade Runner")
	folder := itemFolder(t, id)
	rereadLater(t, id)
	keepFiles(t, folder)
	restoreLater(t, id)
	poster := filepath.Join(folder, "poster.jpg")
	fixturePoster := fixture(t, "messy-movies/Blade Runner (1982)/poster.jpg")
	mediaWrite(t, poster, fixturePoster)
	if !refreshed(t, id) {
		t.Fatal("the refresh that reads the poster never ran")
	}
	libraryOptionsSet(t, "Movies", "SaveLocalMetadata", true)

	cands := rows(t, call(t, "item_artwork", map[string]any{"id": id, "type": "Primary", "limit": 1})["candidates"], "candidates")
	if len(cands) == 0 {
		t.Fatal("no poster candidates")
	}
	nfo := filepath.Join(folder, "movie.nfo")
	nfoBefore, err := os.ReadFile(nfo) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	out := call(t, "item_artwork_set", map[string]any{"id": id, "url": str(cands[0]["url"]), "type": "Primary"})
	server := "/media/movies/Blade Runner (1982)"
	// Jellyfin writes the film's nfo again with the image; Emby leaves it
	nfoAfter, err := os.ReadFile(nfo) //nolint:gosec // same
	if err != nil || bytes.Equal(nfoAfter, nfoBefore) == isJellyfin() {
		t.Errorf("after item_artwork_set saving artwork beside the media the nfo changed: %v (%v); want it written again on Jellyfin alone", !bytes.Equal(nfoAfter, nfoBefore), err)
	}
	if isJellyfin() {
		if str(out["removed"]) != server+"/poster.jpg" || str(out["written"]) != server+"/folder.jpg" {
			t.Errorf("item_artwork_set saving artwork beside the media on Jellyfin = %v, want poster.jpg removed and folder.jpg written", out)
		}
		if posterNow(t, poster) != nil {
			t.Error("poster.jpg is still beside the film")
		}
		if _, err := os.Stat(filepath.Join(folder, "folder.jpg")); err != nil {
			t.Errorf("folder.jpg is not beside the film: %v", err)
		}
		return
	}
	if str(out["replaced"]) != server+"/poster.jpg" || out["removed"] != nil {
		t.Errorf("item_artwork_set saving artwork beside the media on Emby = %v, want poster.jpg written over", out)
	}
	if now := posterNow(t, poster); now == nil || bytes.Equal(now, fixturePoster) {
		t.Error("poster.jpg beside the film was not written over")
	}
}

// nfoSays reports whether an nfo file in a folder holds the text given.
func nfoSays(t *testing.T, folder, text string) bool {
	t.Helper()

	entries, err := os.ReadDir(folder)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(strings.ToLower(e.Name()), ".nfo") {
			raw, rerr := os.ReadFile(filepath.Join(folder, e.Name())) //nolint:gosec // a fixture under the test data dir
			if rerr == nil && bytes.Contains(raw, []byte(text)) {
				return true
			}
		}
	}

	return false
}

// An edit in a library that saves nfos writes a film's nfo beside it, and
// item_edit names the items it expects that for. It named a song too, whose
// edit writes none (seen on Jellyfin 12.1): the answer names films, series,
// seasons and episodes only, and the nfo it expects is written.
func TestTheNfoAnEditWrites(t *testing.T) {
	film := findItem(t, "Movies", "Movie", "Arrival")
	songs := rows(t, call(t, "library_items", map[string]any{"library": "Music", "types": "Audio", "limit": 1})["items"], "items")
	if len(songs) == 0 {
		t.Fatal("the Music library holds no song")
	}
	song := str(songs[0]["id"])
	filmFolder, songFolder := itemFolder(t, film), itemFolder(t, song)
	keepFiles(t, filmFolder, songFolder)
	restoreLater(t, film)
	restoreLater(t, song)

	for _, tc := range []struct {
		id, library, folder string
		kindWrites          bool
	}{
		{film, "Movies", filmFolder, true},
		{song, "Music", songFolder, false},
	} {
		saves := boolOf(call(t, "library_get", map[string]any{"library": tc.library})["saves_nfo"])
		out := call(t, "item_edit", map[string]any{"ids": []any{tc.id}, "add_tags": []any{"Zzyzx Nfo"}})
		expected := slices.Contains(rowsOfStrings(out["nfo_expected"]), tc.id)
		if want := saves && tc.kindWrites; expected != want {
			t.Errorf("%s (library saves nfos: %v): nfo_expected names it: %v, want %v", tc.library, saves, expected, want)
		}
		switch {
		case expected && !eventually(func() bool { return nfoSays(t, tc.folder, "Zzyzx Nfo") }):
			t.Errorf("the %s item's nfo was expected and no nfo in %s holds the edit", tc.library, tc.folder)
		case !expected && !holds(func() bool { return !nfoSays(t, tc.folder, "Zzyzx Nfo") }):
			t.Errorf("the %s item's nfo was not expected and an nfo in %s holds the edit", tc.library, tc.folder)
		}
	}
}

// setPolicy changes an account's policy for the rest of a test, and puts it
// back when the test ends.
func setPolicy(t *testing.T, userID string, change func(policy map[string]any)) {
	t.Helper()

	status, raw := api(t, http.MethodGet, "/Users/"+userID, "", nil)
	var user struct{ Policy map[string]any }
	if status != http.StatusOK || json.Unmarshal(raw, &user) != nil || user.Policy == nil {
		t.Fatalf("reading account %s: HTTP %d: %.200s", userID, status, raw)
	}
	original, err := json.Marshal(user.Policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var policy map[string]any
		if err := json.Unmarshal(original, &policy); err != nil {
			t.Error(err)
			return
		}
		if status, raw := api(t, http.MethodPost, "/Users/"+userID+"/Policy", "", policy); status/100 != 2 {
			t.Errorf("putting account %s's policy back: HTTP %d: %s", userID, status, raw)
		}
	})
	change(user.Policy)
	if status, raw := api(t, http.MethodPost, "/Users/"+userID+"/Policy", "", user.Policy); status/100 != 2 {
		t.Fatalf("changing account %s's policy: HTTP %d: %s", userID, status, raw)
	}
}

// A collection's members and a playlist's entries were read in the first
// administrator's view, which leaves out what that account cannot see: with
// root not given Shows, a collection of Breaking Bad and Alien read as Alien
// alone, and its delete would have said it held Alien - the only record of
// what went. The lists are read whole: on Emby with no user, which lists
// everything; on Jellyfin, which lists them only in a user's view, in the
// view of an administrator who sees every library, and with none there a
// list that cannot be read whole is refused rather than read short - and the
// deletes that need it refused with it.
func TestListsReadWholeWhenTheAdminIsNarrowed(t *testing.T) {
	series, alien, arrival := findItem(t, "Shows", "Series", "Breaking Bad"), findItem(t, "Movies", "Movie", "Alien"), findItem(t, "Movies", "Movie", "Arrival")
	pilot := str(rows(t, call(t, "library_episodes", map[string]any{"series_id": series})["episodes"], "episodes")[0]["id"])
	col := str(call(t, "collection_create", map[string]any{"name": "Zzyzx Narrowed", "item_ids": []any{series, alien}})["id"])
	deleteLater(t, "collection_delete", "collection", col)
	pl := str(call(t, "playlist_create", map[string]any{"name": "Zzyzx Narrowed", "item_ids": []any{arrival, pilot}, "media_type": "Video"})["id"])
	deleteLater(t, "playlist_delete", "playlist", pl)

	ids := libraryIDs(t)
	setPolicy(t, os.Getenv("EMBYFIN_TEST_ADMIN_ID"), func(p map[string]any) {
		p["EnableAllFolders"] = false
		p["EnabledFolders"] = []string{ids["Movies"], ids["Music"]}
	})
	whole := func(when string) {
		t.Helper()
		members := names(t, call(t, "collection_get", map[string]any{"collection": col})["items"], "items")
		slices.Sort(members)
		if !slices.Equal(members, []string{"Alien", "Breaking Bad"}) {
			t.Errorf("%s the collection reads as %v, want Alien and Breaking Bad", when, members)
		}
		if entries := names(t, call(t, "playlist_get", map[string]any{"playlist": pl})["entries"], "entries"); len(entries) != 2 {
			t.Errorf("%s the playlist reads as %v, want Arrival and the pilot", when, entries)
		}
	}
	if !isJellyfin() {
		whole("with root not given Shows")
	} else {
		for tool, args := range map[string]map[string]any{
			"collection_get":  {"collection": col},
			"playlist_get":    {"playlist": pl},
			"playlist_delete": {"playlist": pl},
			"item_delete":     {"id": alien},
		} {
			if msg := callErr(t, tool, args); !strings.Contains(msg, "no administrator sees every library") {
				t.Errorf("%s with no administrator seeing every library: %s", tool, msg)
			}
		}
		if !slices.ContainsFunc(rows(t, call(t, "playlist_list", nil)["playlists"], "playlists"), func(p map[string]any) bool { return str(p["id"]) == pl }) {
			t.Error("the playlist was deleted, though what it held could not be read whole")
		}
	}

	// alice made an administrator who sees every library: Jellyfin reads the
	// lists in her view
	setPolicy(t, os.Getenv("EMBYFIN_TEST_USER_ID"), func(p map[string]any) {
		p["IsAdministrator"] = true
		p["EnableAllFolders"] = true
	})
	whole("with alice an administrator who sees every library")
}

// A watched mark on Emby's Messy Movies changed all the films it stores -
// thirteen, where its view shows eleven rows, a film's copies one row - and
// the films elsewhere sharing their ids, the clean library's Alien among
// them, while the answer said it reached the rows. The answer says how many
// items are stored, and names the copies elsewhere. Jellyfin shows every
// copy as its own row and keeps each one's state, and says neither.
func TestAWatchedMarkOnALibraryCountsWhatIsStored(t *testing.T) {
	lib := str(call(t, "library_get", map[string]any{"library": "Messy Movies"})["id"])
	clean := findItem(t, "Movies", "Movie", "Alien")
	// the library put back by its watched mark alone: Emby refuses a
	// favourite on a library with a 500
	putBack(t, "item_set_state", map[string]any{"id": lib, "user": "alice", "watched": false})
	unmarkLater(t, "alice", clean)
	call(t, "item_set_state", map[string]any{"id": lib, "user": "alice", "watched": false})

	out := call(t, "item_set_state", map[string]any{"id": lib, "user": "alice", "watched": true})
	var elsewhere []string
	for _, c := range rowsOf(out["copies_changed"]) {
		elsewhere = append(elsewhere, str(c["id"]))
	}
	unmarkLater(t, "alice", elsewhere...)
	cleanWatched, _ := stateOf(t, clean, "alice")
	if isJellyfin() {
		if out["stored_under"] != nil || len(elsewhere) != 0 || cleanWatched {
			// (Jellyfin shows every copy as its own row and keeps each one's state)
			t.Errorf("a mark on Jellyfin's Messy Movies = stored %v, elsewhere %v, the clean Alien watched %v; want none of them", out["stored_under"], elsewhere, cleanWatched)
		}
		return
	}
	if n := num(t, out["stored_under"], "stored_under"); n != messyMovies() || !strings.Contains(str(out["note"]), fmt.Sprintf("the server stores %d items under Messy Movies", messyMovies())) {
		t.Errorf("a mark on Emby's Messy Movies = stored %d, note %q; want the %d films it stores said", n, out["note"], messyMovies())
	}
	if !slices.Contains(elsewhere, clean) || !cleanWatched {
		t.Errorf("copies_changed = %v and the clean Alien watched %v: want the clean Alien, which shares the messy Aliens' id, read before and after and marked", elsewhere, cleanWatched)
	}
}

// A watched mark on a series in the name of a user blocked from one of its
// episodes by a tag reaches that episode too (seen on Emby 4.10 and 4.11 and
// Jellyfin 12.1, both ways), where the answer had said a mark in her name
// leaves what she cannot see; a mark on its season reaches it on Emby and
// not on Jellyfin. On Emby, which answers the hidden episode in her name, it
// is read before and after and answered as changed, with what it was in
// was; Jellyfin answers it with a 404, and the answer says where its mark
// can reach what her view leaves out and where it did not.
func TestAMarkReachesWhatTheUserCannotSee(t *testing.T) {
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	byNumber := map[int]string{}
	for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": series})["episodes"], "episodes") {
		byNumber[num(t, e["episode"], "episode")] = str(e["id"])
	}
	hidden, seen := byNumber[1], byNumber[2]
	if hidden == "" || seen == "" {
		t.Fatalf("Breaking Bad's episodes by number = %v, want the fixture's first and second", byNumber)
	}
	// cleaned up last, once the block is lifted and she sees it again
	unmarkLater(t, "alice", hidden, seen, byNumber[3])
	call(t, "item_set_state", map[string]any{"id": hidden, "user": "alice", "watched": true})
	call(t, "item_edit", map[string]any{"ids": []any{hidden}, "add_tags": []any{"zzyzx-hidden"}})
	putBack(t, "item_edit", map[string]any{"ids": []any{hidden}, "remove_tags": []any{"zzyzx-hidden"}})
	alice := os.Getenv("EMBYFIN_TEST_USER_ID")
	setPolicy(t, alice, func(p map[string]any) { p["BlockedTags"] = []any{"zzyzx-hidden"} })
	var view []string
	for _, e := range rows(t, call(t, "library_items", map[string]any{"library": "Shows", "types": "Episode", "user": "alice", "limit": 100})["items"], "items") {
		view = append(view, str(e["id"]))
	}
	if slices.Contains(view, hidden) || !slices.Contains(view, seen) {
		t.Fatalf("alice's view of Shows' episodes = %v, want the second episode (%s) and not the first (%s), blocked by its tag", view, seen, hidden)
	}

	out := call(t, "item_set_state", map[string]any{"id": series, "user": "alice", "watched": false})
	note := str(out["note"])
	changed := map[string]bool{}
	for _, c := range rowsOf(out["copies_changed"]) {
		changed[str(c["id"])] = boolOf(c["played"])
	}
	wasPlayed, wasListed := false, false
	for _, w := range rowsOf(out["was"]) {
		if str(w["id"]) == hidden {
			wasListed, wasPlayed = true, boolOf(w["played"])
		}
	}
	if strings.Contains(note, "reaches all") || strings.Contains(note, "is not marked") {
		t.Errorf("clearing Breaking Bad for alice, an episode hidden from her: note %q", note)
	}
	if after, named := changed[hidden]; isJellyfin() && (!strings.Contains(note, "Jellyfin's mark in their name can reach items their view leaves out") || named || wasListed) {
		t.Errorf("on Jellyfin clearing Breaking Bad for alice: note %q, copies_changed %v, the hidden episode in was %v; want the note to say the mark reaches what her view leaves out, and nothing read of it", note, changed, wasListed)
	} else if !isJellyfin() && (!strings.Contains(note, "items it leaves out") || !strings.Contains(note, "were read before and after") || !named || after || !wasListed || !wasPlayed) {
		t.Errorf("on Emby clearing Breaking Bad for alice: note %q, copies_changed %v, the hidden episode in was %v (played %v); want it read before as watched and after as not", note, changed, wasListed, wasPlayed)
	}

	// its first season marked in her name: Emby reaches the hidden episode
	// and reads it back watched; Jellyfin leaves it, and says so
	season := ""
	for _, s := range rows(t, call(t, "show_seasons", map[string]any{"series_id": series})["seasons"], "seasons") {
		if num(t, s["season"], "season") == 1 {
			season = str(s["id"])
		}
	}
	if season == "" {
		t.Fatal("Breaking Bad has no first season")
	}
	out = call(t, "item_set_state", map[string]any{"id": season, "user": "alice", "watched": true})
	named, played := false, false
	for _, c := range rowsOf(out["copies_changed"]) {
		if str(c["id"]) == hidden {
			named, played = true, boolOf(c["played"])
		}
	}
	if isJellyfin() && (named || !strings.Contains(str(out["note"]), "not under a season")) || !isJellyfin() && (!named || !played) {
		t.Errorf("marking Breaking Bad's first season for alice on %s: note %q, the hidden episode in copies_changed %v (played %v)", backend, out["note"], named, played)
	}

	// the series' unwatch cleared the episode on both, and the season's mark
	// set it again on Emby alone, which the answers said
	setPolicy(t, alice, func(p map[string]any) { p["BlockedTags"] = []any{} })
	if watched, _ := stateOf(t, hidden, "alice"); watched == isJellyfin() {
		t.Errorf("after Breaking Bad was cleared and its first season marked for alice on %s, its first episode, hidden from her then, is watched %v: want watched on Emby, whose season mark reaches it, and not on Jellyfin, whose series unwatch reached it and season mark did not", backend, watched)
	}
}
