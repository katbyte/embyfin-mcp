//go:build integration

package acceptance

import (
	"strings"
	"testing"
)

// A collection deleted straight after it was made. Jellyfin refreshes a
// collection it has just made, and saves it back when that refresh ends -
// a second later when the provider answers, a minute when it fails - so a
// delete that lands first is undone: the collection is listed again. So on
// Jellyfin collection_delete asks the provider what the refresh asks, and
// watches a moment when it answers at once (here, replayed, it does) and
// until 75 seconds after the change when it does not, deleting the
// collection again if it comes back, and says why it watched; the
// collection is then gone for good. Emby has not been seen to bring one
// back, and gets a few seconds' watch.
func TestDeletingACollectionJustMade(t *testing.T) {
	alien := findItem(t, "Movies", "Movie", "Alien")
	id := str(call(t, "collection_create", map[string]any{"name": "Zzyzx Just Made", "item_ids": []any{alien}})["id"])
	t.Cleanup(func() {
		if _, err := invoke("collection_get", map[string]any{"collection": id}); err == nil {
			_, _ = invoke("collection_delete", map[string]any{"collection": id})
		}
	})

	out := call(t, "collection_delete", map[string]any{"collection": id})
	watched, note := num(t, out["watched_s"], "watched_s"), str(out["note"])
	if str(out["deleted"]) != "Zzyzx Just Made" {
		t.Errorf("collection_delete = %v", out)
	}
	if isJellyfin() {
		// the provider answers at once, and the watch is a few seconds
		if watched > 15 || !strings.Contains(note, "was changed") || !strings.Contains(note, "s before the delete") || !strings.Contains(note, "answered at once") {
			t.Errorf("collection_delete on Jellyfin watched %d s with note %q, want a short watch, the provider having answered at once, and why", watched, note)
		}
	} else if watched > 10 || note != "" {
		t.Errorf("collection_delete on Emby watched %d s with note %q, want a few seconds and nothing to say", watched, note)
	}

	// and it stays gone
	gone := func() bool {
		for _, c := range rowsOf(call(t, "collection_list", nil)["collections"]) {
			if str(c["id"]) == id {
				return false
			}
		}
		return true
	}
	if !holds(gone) {
		t.Error("the collection is listed again after collection_delete answered")
	}
}
