//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Journeys through folders moved on disk: a film's folder renamed, and a
// library's folder swapped for another holding the same films. Either way
// the servers see new items where the old ones were - new ids - and what
// follows a film across is what they keep by its provider ids.

// itemsUnder is the films a library holds under a folder, by path.
func itemsUnder(t *testing.T, library, folder string) map[string]map[string]any {
	t.Helper()

	out := map[string]map[string]any{}
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": library, "types": "Movie", "limit": 200})["items"], "items") {
		if strings.HasPrefix(str(it["path"]), folder+"/") {
			out[str(it["path"])] = it
		}
	}

	return out
}

// A film's folder renamed on disk, the way a tidy-up does it, and the library
// scanned: the film is a new item at the new path, and the old one is gone,
// so what named it by id lets go of it - the playlist and the collection.
// What the servers keep by provider id follows it: alice's watched mark and
// favourite on a film with ids come across, on one without them they are
// lost. Nothing is left behind as an orphan, and the library holds as many
// films as it did.
func TestRenamingAFilmsFolder(t *testing.T) {
	messy := filepath.Join(dataDir(), "messy-movies")
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	dune := findItem(t, "Movies", "Movie", "Dune")
	const (
		bare  = "The Lord of the Rings The Two Towers (2002)" // no nfo, no ids
		known = "The Thirteenth Floor (1999)"                 // the clean copy's nfo and ids
	)
	renamed := func(name string) string { return name + " [1080p]" }
	have := movieCount(t, "Messy Movies")
	t.Cleanup(func() {
		for _, name := range []string{bare, known, renamed(bare), renamed(known)} {
			if err := os.RemoveAll(filepath.Join(messy, name)); err != nil {
				t.Error(err)
			}
		}
		if err := scanUntil("Messy Movies", have); err != nil {
			t.Error(err)
		}
	})
	mediaMkdir(t, filepath.Join(messy, bare))
	mediaWrite(t, filepath.Join(messy, bare, bare+".mp4"), fixtureVideo(t, "movies", "Arrival (2016)", "Arrival (2016).mp4"))
	copyFixture(t, filepath.Join(dataDir(), "movies", known), filepath.Join(messy, known))
	if err := scanUntil("Messy Movies", have+2); err != nil {
		t.Fatal(err)
	}
	find := func(name string) (string, map[string]any) {
		for path, it := range itemsUnder(t, "Messy Movies", "/media/messy-movies/"+name) {
			if strings.HasPrefix(path, "/media/messy-movies/"+name+"/") {
				return str(it["id"]), it
			}
		}
		return "", nil
	}
	bareID, _ := find(bare)
	knownID, _ := find(known)
	if bareID == "" || knownID == "" {
		t.Fatalf("the staged films were not both scanned in: %q %q", bareID, knownID)
	}

	// alice has watched both and likes both; the lists hold both, beside a
	// film that stays
	unmarkLater(t, "alice", findItem(t, "Movies", "Movie", "The Thirteenth Floor"))
	for _, id := range []string{bareID, knownID} {
		call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": false, "favourite": false})
		call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": true, "favourite": true})
		if w, f := stateOf(t, id, "alice"); !w || !f {
			t.Fatalf("before the rename %s for alice: watched %v favourite %v", id, w, f)
		}
	}
	pl := str(call(t, "playlist_create", map[string]any{"name": "Zzyzx Renamed", "item_ids": []any{dune, bareID, knownID}, "media_type": "Video"})["id"])
	deleteLater(t, "playlist_delete", "playlist", pl)
	col := str(call(t, "collection_create", map[string]any{"name": "Zzyzx Renamed", "item_ids": []any{arrival, bareID, knownID}})["id"])
	deleteLater(t, "collection_delete", "collection", col)
	if n := collectionSize(t, col, 3); n != 3 {
		t.Fatalf("the collection holds %d, want 3", n)
	}

	// the folders renamed, and the file in the one with ids too
	for _, name := range []string{bare, known} {
		if err := os.Rename(filepath.Join(messy, name), filepath.Join(messy, renamed(name))); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(filepath.Join(messy, renamed(known), known+".mp4"), filepath.Join(messy, renamed(known), renamed(known)+".mp4")); err != nil {
		t.Fatal(err)
	}
	rescanOrReport(t, "Messy Movies", func() bool {
		a, _ := find(renamed(bare))
		b, _ := find(renamed(known))
		c, _ := find(bare)
		d, _ := find(known)
		return a != "" && b != "" && c == "" && d == ""
	})
	if t.Failed() {
		t.FailNow()
	}

	if n := movieCount(t, "Messy Movies"); n != have+2 {
		t.Errorf("after the rename the library holds %d films, want %d", n, have+2)
	}
	for _, old := range []string{bareID, knownID} {
		if _, err := invoke("item_get", map[string]any{"id": old}); err == nil {
			t.Errorf("the item at the old path, %s, is still there", old)
		}
	}
	newBare, bareRow := find(renamed(bare))
	newKnown, knownRow := find(renamed(known))
	// cleared at the end: Jellyfin names an item by its path, and would hand
	// what is left on these ids to the next run's films at the same paths
	unmarkLater(t, "alice", newBare, newKnown)
	if ids, _ := knownRow["metadata_provider_ids"].(map[string]any); str(ids["tmdb"]) != "1090" {
		t.Errorf("the renamed film with an nfo holds %v, want tmdb 1090", ids)
	}
	if ids, _ := bareRow["metadata_provider_ids"].(map[string]any); len(ids) != 0 {
		t.Errorf("the renamed film without one holds %v", ids)
	}
	if w, f := stateOf(t, newKnown, "alice"); !w || !f {
		t.Errorf("the film with ids, renamed, for alice: watched %v favourite %v, want both come across", w, f)
	}
	if w, f := stateOf(t, newBare, "alice"); w || f {
		t.Errorf("the film without ids, renamed, for alice: watched %v favourite %v, want both lost", w, f)
	}
	if got := names(t, call(t, "playlist_get", map[string]any{"playlist": pl})["entries"], "entries"); !slices.Equal(got, []string{"Dune"}) {
		t.Errorf("after the rename the playlist is %v, want [Dune]", got)
	}
	if got := names(t, call(t, "collection_get", map[string]any{"collection": col})["items"], "items"); !slices.Equal(got, []string{"Arrival"}) {
		t.Errorf("after the rename the collection holds %v, want [Arrival]", got)
	}
	if n := num(t, call(t, "audit_orphans", nil)["total_findings"], "total_findings"); n != 0 {
		t.Errorf("after the rename audit_orphans finds %d", n)
	}
}

// A library's folder swapped with library_edit for another holding the same
// films - the films moved on disk, the new folder added and the old one
// removed - and the library scanned: the films are new items at the new
// paths. alice's watch state follows the film with provider ids, and is lost
// on the one without, as with a folder renamed inside a library.
func TestSwappingALibrarysFolder(t *testing.T) {
	const library = "Zzyzx Swap"
	from, to := filepath.Join(dataDir(), "swap-a"), filepath.Join(dataDir(), "swap-b")
	const (
		bare  = "The Lord of the Rings The Fellowship of the Ring (2001)" // no nfo, no ids
		known = messyInterstellar                                         // its nfo and ids
	)
	mediaMkdir(t, filepath.Join(from, bare))
	mediaWrite(t, filepath.Join(from, bare, bare+".mp4"), fixtureVideo(t, "movies", "Arrival (2016)", "Arrival (2016).mp4"))
	copyFixture(t, filepath.Join(dataDir(), "messy-movies", known), filepath.Join(from, known))
	t.Cleanup(func() {
		for _, dir := range []string{from, to} {
			if err := os.RemoveAll(dir); err != nil {
				t.Error(err)
			}
		}
	})
	t.Cleanup(func() {
		if err := retried("library_delete", map[string]any{"library": library, "confirm": true}); err != nil {
			t.Errorf("removing the library: %v", err)
		}
		if err := waitForExpectedScan(); err != nil {
			t.Error(err)
		}
	})
	// Emby keeps state by provider id across every copy of a film, the
	// messy Interstellar's too, which is cleared at the end through it
	unmarkLater(t, "alice", findItem(t, "Messy Movies", "Movie", "Interstellar"))
	call(t, "library_create", map[string]any{"name": library, "type": "movies", "paths": []any{"/media/swap-a"}, "scan": true, "save_nfo": false})
	holding := func(folder string) (bareID, knownID string) {
		out, err := invoke("library_items", map[string]any{"library": library, "types": "Movie"})
		if err != nil {
			return "", ""
		}
		for _, it := range rowsOf(out["items"]) {
			switch path := str(it["path"]); {
			case strings.HasPrefix(path, folder+"/"+bare+"/"):
				bareID = str(it["id"])
			case strings.HasPrefix(path, folder+"/"+known+"/"):
				knownID = str(it["id"])
			}
		}
		return bareID, knownID
	}
	var bareID, knownID string
	if !eventuallyWithin(scanPatience, func() bool { bareID, knownID = holding("/media/swap-a"); return bareID != "" && knownID != "" }) {
		t.Fatal("the library never held its two films")
	}
	if err := waitForExpectedScan(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{bareID, knownID} {
		call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": false, "favourite": false})
		call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": true, "favourite": true})
	}

	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
	out := call(t, "library_edit", map[string]any{"library": library, "add_paths": []any{"/media/swap-b"}, "remove_paths": []any{"/media/swap-a"}})
	if locs := strs(t, out["locations"], "locations"); !slices.Equal(locs, []string{"/media/swap-b"}) {
		t.Fatalf("library_edit locations = %v", locs)
	}
	var newBare, newKnown string
	rescanOrReport(t, library, func() bool {
		newBare, newKnown = holding("/media/swap-b")
		oldBare, oldKnown := holding("/media/swap-a")
		return newBare != "" && newKnown != "" && oldBare == "" && oldKnown == ""
	})
	if t.Failed() {
		t.FailNow()
	}
	// cleared at the end, for the next run's films at the same paths
	unmarkLater(t, "alice", newBare, newKnown)
	if newBare == bareID || newKnown == knownID {
		t.Errorf("the films kept their ids across the swap: %s %s", newBare, newKnown)
	}
	if w, f := stateOf(t, newKnown, "alice"); !w || !f {
		t.Errorf("the film with ids, moved, for alice: watched %v favourite %v, want both come across", w, f)
	}
	if w, f := stateOf(t, newBare, "alice"); w || f {
		t.Errorf("the film without ids, moved, for alice: watched %v favourite %v, want both lost", w, f)
	}
}
