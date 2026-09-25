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
)

// Journeys against work that is still going on: a bulk edit that stops at a
// bad id, fixes made while a scan runs, and an edit made while the server's
// own refresh of the item is still to come.

// heldPaths is the path of every film a library holds, for a message.
func heldPaths(t *testing.T, library string) []string {
	t.Helper()

	var out []string
	for _, it := range rowsOf(call(t, "library_items", map[string]any{"library": library, "types": "Movie", "limit": 200})["items"]) {
		out = append(out, str(it["path"]))
	}

	return out
}

// missingID is an id shaped like the server's own that names no item.
func missingID() string {
	if isJellyfin() {
		return "0123456789abcdef0123456789abcdef"
	}

	return "999999999"
}

// A bulk edit that stops part way: the items before the bad id are changed,
// the ones after it are not, and the error says how many were done. Run
// again without the bad id it finishes the rest and changes the done ones no
// further: the tag is on each once.
func TestABulkEditStoppedPartWay(t *testing.T) {
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	dune := findItem(t, "Movies", "Movie", "Dune")
	for _, id := range []string{arrival, dune} {
		keepFiles(t, itemFolder(t, id))
	}
	putBack(t, "item_edit", map[string]any{"ids": []any{arrival, dune}, "remove_tags": []any{"zzyzx-bulk"}})
	tagged := func(id string) int {
		n := 0
		for _, tag := range strs(t, call(t, "item_get", map[string]any{"id": id})["tags"], "tags") {
			if tag == "zzyzx-bulk" {
				n++
			}
		}
		return n
	}

	bad := missingID()
	msg := callErr(t, "item_edit", map[string]any{"ids": []any{arrival, bad, dune}, "add_tags": []any{"zzyzx-bulk"}})
	if !strings.Contains(msg, bad+": ") || !strings.Contains(msg, "(1 items were updated before it)") {
		t.Errorf("the edit stopped at the bad id with: %s", msg)
	}
	if a, d := tagged(arrival), tagged(dune); a != 1 || d != 0 {
		t.Fatalf("after the stopped edit Arrival carries the tag %d times and Dune %d, want 1 and 0", a, d)
	}

	for range 2 {
		out := call(t, "item_edit", map[string]any{"ids": []any{arrival, dune}, "add_tags": []any{"zzyzx-bulk"}})
		if num(t, out["updated"], "updated") != 2 || !slices.Equal(strs(t, out["items"], "items"), []string{"Arrival", "Dune"}) {
			t.Errorf("the edit again = %v", out)
		}
		if a, d := tagged(arrival), tagged(dune); a != 1 || d != 1 {
			t.Errorf("after the edit again Arrival carries the tag %d times and Dune %d, want once each", a, d)
		}
	}
}

// Fixes made while a scan runs stand once it has finished: an edit, a match
// set by hand in a library with its fetchers off, a user's watch state, and
// a copy deleted - which the scan, having listed the folder before the delete,
// does not bring back.
func TestFixingWhileAScanRuns(t *testing.T) {
	arrival := findItem(t, "Messy Movies", "Movie", "Arrival")
	mononoke := findItem(t, "Messy Movies", "Movie", "Princess Mononoke")
	blade := findItem(t, "Movies", "Movie", "Blade Runner")
	for _, id := range []string{arrival, mononoke} {
		keepFiles(t, itemFolder(t, id))
		restoreLater(t, id)
	}
	unmarkLater(t, "alice", blade)
	call(t, "item_set_state", map[string]any{"id": blade, "user": "alice", "watched": false, "favourite": false})

	// a copy to delete, staged and scanned in first
	have := movieCount(t, "Messy Movies")
	const name = "The Thirteenth Floor (1999)"
	copied := filepath.Join(dataDir(), "messy-movies", name)
	copyFixture(t, filepath.Join(dataDir(), "movies", name), copied)
	t.Cleanup(func() {
		if err := os.RemoveAll(copied); err != nil {
			t.Error(err)
		}
		if err := scanUntil("Messy Movies", have); err != nil {
			t.Errorf("%v: it holds %v", err, heldPaths(t, "Messy Movies"))
		}
	})
	if err := scanUntil("Messy Movies", have+1); err != nil {
		t.Fatal(err)
	}
	var staged string
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "query": "Thirteenth", "limit": 50})["items"], "items") {
		if strings.Contains(str(it["path"]), "/messy-movies/"+name+"/") {
			staged = str(it["id"])
		}
	}
	if staged == "" {
		t.Fatal("the copy was not scanned in")
	}
	idx := candidateFor(t, map[string]any{"id": mononoke, "kind": "movie"}, "128", "tt0119698")

	slowScan(t)
	if out := call(t, "library_scan", nil); !boolOf(out["started"]) {
		t.Fatalf("library_scan = %v", out)
	}
	const overview = "Zzyzx: fixed while a scan ran."
	call(t, "item_edit", map[string]any{"ids": []any{arrival}, "overview": overview})
	if out := call(t, "item_identify_apply", map[string]any{"id": mononoke, "kind": "movie", "candidate": idx}); !strings.Contains(str(out["note"]), "metadata fetchers off") {
		t.Errorf("item_identify_apply = %v", out)
	}
	call(t, "item_set_state", map[string]any{"id": blade, "user": "alice", "watched": true, "favourite": true})
	call(t, "item_delete", map[string]any{"id": staged, "confirm": true})
	if idle, err := scanIdle(); err != nil || idle {
		t.Fatalf("the scan had finished before the last fix (%v): the fixes raced nothing", err)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}

	stand := func(when string) {
		t.Helper()
		if got := str(call(t, "item_get", map[string]any{"id": arrival})["overview"]); got != overview {
			t.Errorf("%s the messy Arrival's overview is %q", when, got)
		}
		if ids, _ := call(t, "item_get", map[string]any{"id": mononoke})["metadata_provider_ids"].(map[string]any); str(ids["tmdb"]) != "128" && str(ids["imdb"]) != "tt0119698" {
			t.Errorf("%s the messy Princess Mononoke holds %v, want its own tmdb or imdb id", when, ids)
		}
		if w, f := stateOf(t, blade, "alice"); !w || !f {
			t.Errorf("%s Blade Runner for alice: watched %v favourite %v", when, w, f)
		}
		if _, err := invoke("item_get", map[string]any{"id": staged}); err == nil {
			t.Errorf("%s the deleted copy is back", when)
		}
		if _, err := os.Stat(copied); !os.IsNotExist(err) {
			t.Errorf("%s the deleted copy's folder is on disk: %v", when, err)
		}
		if held := heldPaths(t, "Messy Movies"); slices.ContainsFunc(held, func(p string) bool { return strings.Contains(p, "/messy-movies/"+name+"/") }) {
			t.Errorf("%s the library holds the deleted copy again: %v", when, held)
		}
	}
	stand("once the scan finished")
	// and the next scan, the first to start after the delete
	call(t, "library_scan", nil)
	if err := waitForExpectedScan(); err != nil {
		t.Fatal(err)
	}
	stand("after another scan")
}

// An edit made straight after item_refresh answers stays. It did not:
// item_refresh answered once the server had queued the refresh, and a
// replace_all refresh that ran after the edit put the providers' metadata
// back over it, in about half of tries on Emby 4.10 and Jellyfin 12.1 with
// nothing between the calls. item_refresh now waits for the refresh to save
// the item, and says it saw it land.
func TestAnEditRightAfterARefreshStays(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Princess Mononoke")
	keepFiles(t, itemFolder(t, id))
	restoreLater(t, id)
	settled := func() {
		t.Helper()
		last, same := itemEtag(t, id), 0
		if !eventuallyWithin(time.Minute, func() bool {
			if now := itemEtag(t, id); now != last {
				last, same = now, 0
			} else {
				same++
			}
			return same >= 6
		}) {
			t.Fatal("the film never settled after the refresh")
		}
	}
	for i := range 6 {
		tag := fmt.Sprintf("zzyzx-after-refresh-%d", i)
		if out := call(t, "item_refresh", map[string]any{"id": id, "replace_all": true}); !boolOf(out["landed"]) {
			t.Errorf("try %d: item_refresh = %v, want the refresh seen to land", i, out)
		}
		call(t, "item_edit", map[string]any{"ids": []any{id}, "add_tags": []any{tag}})
		settled()
		if tags := strs(t, call(t, "item_get", map[string]any{"id": id})["tags"], "tags"); !slices.Contains(tags, tag) {
			t.Errorf("try %d: the edit made after item_refresh answered was undone by the refresh: tags %v", i, tags)
		}
	}
}
