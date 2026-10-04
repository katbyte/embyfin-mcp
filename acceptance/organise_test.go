//go:build integration

package acceptance

import (
	"slices"
	"strings"
	"testing"
	"time"

	acc "github.com/katbyte/embyfin-mcp/lib/acceptance"
)

func TestCollections(t *testing.T) {
	alien := findItem(t, "Movies", "Movie", "Alien")
	aliens := findItem(t, "Movies", "Movie", "Aliens")
	blade := findItem(t, "Movies", "Movie", "Blade Runner")

	out := suite.Call(t, "collection_create", map[string]any{"name": "Alien Saga", "item_ids": []any{alien}})
	if acc.Str(out["id"]) == "" || acc.Str(out["name"]) != "Alien Saga" {
		t.Fatalf("collection_create = %v", out)
	}
	id := acc.Str(out["id"])
	// the test deletes it itself; this is for one that stops short
	suite.DeleteLaterIfThere(t, "collection_delete", map[string]any{"collection": id}, "no collection named")

	list := suite.Call(t, "collection_list", nil)
	var names []string
	for _, c := range acc.Rows(t, list["collections"], "collections") {
		names = append(names, acc.Str(c["name"]))
	}
	if !slices.Contains(names, "Alien Saga") {
		t.Errorf("collections = %v", names)
	}

	// add two, by collection name, case-insensitively
	add := suite.Call(t, "collection_edit", map[string]any{"collection": "alien saga", "add_items": []any{aliens, blade}})
	if acc.Num(t, add["added"], "added") != 2 || acc.Str(add["name"]) != "Alien Saga" {
		t.Errorf("collection_edit add_items = %v", add)
	}
	got := suite.Call(t, "collection_get", map[string]any{"collection": id})
	names = names[:0]
	for _, it := range acc.Rows(t, got["items"], "items") {
		names = append(names, acc.Str(it["name"]))
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"Alien", "Aliens", "Blade Runner"}) {
		t.Errorf("collection = %v", names)
	}

	// remove one
	rm := suite.Call(t, "collection_edit", map[string]any{"collection": "Alien Saga", "remove_items": []any{blade}})
	if acc.Num(t, rm["removed"], "removed") != 1 || acc.Str(rm["name"]) != "Alien Saga" || len(acc.Rows(t, rm["removed_items"], "removed_items")) != 1 {
		t.Errorf("collection_edit remove_items = %v", rm)
	}
	// the servers apply the change a moment after answering
	if n := collectionSize(t, "Alien Saga", 2); n != 2 {
		t.Errorf("after remove = %d items", n)
	}
	// the film is still in the library
	if findItem(t, "Movies", "Movie", "Blade Runner") != blade {
		t.Error("Blade Runner changed id")
	}

	if msg := suite.CallErr(t, "collection_get", map[string]any{"collection": "Nope"}); !strings.Contains(msg, "Nope") || !strings.Contains(msg, "Alien Saga") {
		t.Errorf("an unknown collection should list the real ones: %s", msg)
	}

	// rename it, with a sort name and a description
	edit := suite.Call(t, "collection_edit", map[string]any{"collection": "alien saga", "name": "Zzyzx Anthology", "sort_name": "Alien 0", "overview": "The Alien films."})
	if acc.Str(edit["name"]) != "Zzyzx Anthology" || !slices.Equal(acc.Strs(t, edit["changed"], "changed"), []string{"Name", "SortName", "Overview"}) {
		t.Errorf("collection_edit = %v", edit)
	}
	if stillListed(t, "collection_list", "collections", "Alien Saga") {
		t.Error("Alien Saga is still listed after the rename")
	}
	if got := suite.Call(t, "item_get", map[string]any{"id": id}); acc.Str(got["name"]) != "Zzyzx Anthology" || acc.Str(got["overview"]) != "The Alien films." {
		t.Errorf("the renamed collection = %v", got)
	}
	if n := collectionSize(t, "Zzyzx Anthology", 2); n != 2 {
		t.Errorf("the renamed collection holds %d items", n)
	}
	if msg := suite.CallErr(t, "collection_edit", map[string]any{"collection": "Zzyzx Anthology"}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("an edit without fields: %s", msg)
	}

	del := suite.Call(t, "collection_delete", map[string]any{"collection": "Zzyzx Anthology"})
	if acc.Str(del["deleted"]) != "Zzyzx Anthology" {
		t.Errorf("collection_delete = %v", del)
	}
	if stillListed(t, "collection_list", "collections", "Zzyzx Anthology") {
		t.Error("Zzyzx Anthology is still listed after collection_delete")
	}
	// and the films are untouched
	if findItem(t, "Movies", "Movie", "Alien") != alien {
		t.Error("Alien changed id")
	}
}

// stillListed polls a listing tool until name is gone from its rows or a
// few seconds pass: both servers apply a deletion a moment after answering.
func stillListed(t *testing.T, tool, field, name string) bool {
	t.Helper()

	for range 20 {
		listed := false
		for _, row := range acc.Rows(t, suite.Call(t, tool, nil)[field], field) {
			if acc.Str(row["name"]) == name {
				listed = true
			}
		}
		if !listed {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}

	return true
}

// playlistEntries polls a playlist until it holds want entries or a few
// seconds pass, and returns what it holds: a removal lands a moment after it
// is answered.
func playlistEntries(t *testing.T, playlist string, want int) []map[string]any {
	t.Helper()

	var entries []map[string]any
	for range 20 {
		got := suite.Call(t, "playlist_get", map[string]any{"playlist": playlist})
		if entries = acc.Rows(t, got["entries"], "entries"); len(entries) == want {
			return entries
		}
		time.Sleep(500 * time.Millisecond)
	}

	return entries
}

// collectionSize polls a collection's contents until it holds want items or
// a few seconds pass, and returns what it holds.
func collectionSize(t *testing.T, collection string, want int) int {
	t.Helper()

	n := -1
	for range 20 {
		got := suite.Call(t, "collection_get", map[string]any{"collection": collection})
		if n = len(acc.Rows(t, got["items"], "items")); n == want {
			return n
		}
		time.Sleep(500 * time.Millisecond)
	}

	return n
}

func TestPlaylists(t *testing.T) {
	// the names of a playlist's entries in an answer, in order
	entryNames := func(v any) []string {
		var got []string
		for _, e := range acc.Rows(t, v, "entries") {
			got = append(got, acc.Str(e["name"]))
		}
		return got
	}
	dune := findItem(t, "Movies", "Movie", "Dune")
	dune2 := findItem(t, "Movies", "Movie", "Dune: Part Two")
	arrival := findItem(t, "Movies", "Movie", "Arrival")

	out := suite.Call(t, "playlist_create", map[string]any{"name": "Villeneuve", "item_ids": []any{dune}, "media_type": "Video"})
	if acc.Str(out["id"]) == "" || acc.Str(out["name"]) != "Villeneuve" {
		t.Fatalf("playlist_create = %v", out)
	}
	// the test deletes it itself; this is for one that stops short
	suite.DeleteLaterIfThere(t, "playlist_delete", map[string]any{"playlist": acc.Str(out["id"])}, "no playlist named")

	list := suite.Call(t, "playlist_list", nil)
	var names []string
	for _, p := range acc.Rows(t, list["playlists"], "playlists") {
		names = append(names, acc.Str(p["name"]))
	}
	if !slices.Contains(names, "Villeneuve") {
		t.Errorf("playlists = %v", names)
	}

	add := suite.Call(t, "playlist_edit", map[string]any{"playlist": "villeneuve", "add_items": []any{dune2, arrival}})
	if acc.Num(t, add["added"], "added") != 2 || acc.Str(add["name"]) != "Villeneuve" {
		t.Errorf("playlist_edit add_items = %v", add)
	}
	entries := playlistEntries(t, "Villeneuve", 3)
	names = names[:0]
	var entryIDs []string
	for _, e := range entries {
		names = append(names, acc.Str(e["name"]))
		entryIDs = append(entryIDs, acc.Str(e["entry_id"]))
		if acc.Str(e["entry_id"]) == "" {
			t.Errorf("entry has no entry_id: %v", e)
		}
	}
	// in playlist order
	if !slices.Equal(names, []string{"Dune", "Dune: Part Two", "Arrival"}) {
		t.Fatalf("playlist = %v", names)
	}

	rm := suite.Call(t, "playlist_edit", map[string]any{"playlist": "Villeneuve", "remove_entries": []any{map[string]any{"entry_id": entryIDs[1], "item_id": dune2}}})
	if acc.Num(t, rm["removed"], "removed") != 1 || acc.Str(rm["name"]) != "Villeneuve" {
		t.Errorf("playlist_edit remove_entries = %v", rm)
	}
	if got := entryNames(rm["entries"]); !slices.Equal(got, []string{"Dune", "Arrival"}) {
		t.Errorf("playlist_edit answered the playlist as %v after, want Dune and Arrival", got)
	}
	names = names[:0]
	for _, e := range playlistEntries(t, acc.Str(out["id"]), 2) {
		names = append(names, acc.Str(e["name"]))
	}
	if !slices.Equal(names, []string{"Dune", "Arrival"}) {
		t.Errorf("after remove = %v", names)
	}
	// the same removal again: Emby 4.10 and Jellyfin hold no such entry and
	// would answer its removal doing nothing; Emby 4.11 has numbered the
	// entries again, so the id names Arrival's entry, which it would remove.
	// Each entry is named with its item, and this one is refused
	if msg := suite.CallErr(t, "playlist_edit", map[string]any{"playlist": "Villeneuve", "remove_entries": []any{map[string]any{"entry_id": entryIDs[1], "item_id": dune2}}}); !strings.Contains(msg, "the playlist does not hold item "+dune2+", so nothing was changed") {
		t.Errorf("removing an entry already removed: %s", msg)
	}
	if got := entryNames(suite.Call(t, "playlist_get", map[string]any{"playlist": acc.Str(out["id"])})["entries"]); !slices.Equal(got, []string{"Dune", "Arrival"}) {
		t.Errorf("after the refused removal the playlist = %v, want Dune and Arrival still", got)
	}

	if msg := suite.CallErr(t, "playlist_get", map[string]any{"playlist": "Nope"}); !strings.Contains(msg, "Nope") || !strings.Contains(msg, "Villeneuve") {
		t.Errorf("an unknown playlist should list the real ones: %s", msg)
	}

	// move Arrival to the top, and rename the playlist
	var arrivalEntry string
	for _, e := range playlistEntries(t, acc.Str(out["id"]), 2) {
		if acc.Str(e["name"]) == "Arrival" {
			arrivalEntry = acc.Str(e["entry_id"])
		}
	}
	edit := suite.Call(t, "playlist_edit", map[string]any{"playlist": "Villeneuve", "move_entry_id": arrivalEntry, "move_item_id": arrival, "position": 1, "name": "Denis"})
	names = names[:0]
	for _, e := range acc.Rows(t, edit["entries"], "entries") {
		names = append(names, acc.Str(e["name"]))
	}
	if acc.Str(edit["name"]) != "Denis" || len(acc.Strs(t, edit["changed"], "changed")) != 2 || !slices.Equal(names, []string{"Arrival", "Dune"}) {
		t.Errorf("playlist_edit = %v (entries %v)", edit, names)
	}
	if stillListed(t, "playlist_list", "playlists", "Villeneuve") {
		t.Error("Villeneuve is still listed after the rename")
	}
	names = names[:0]
	for _, e := range playlistEntries(t, "Denis", 2) {
		names = append(names, acc.Str(e["name"]))
	}
	if !slices.Equal(names, []string{"Arrival", "Dune"}) {
		t.Errorf("after the move = %v", names)
	}
	if msg := suite.CallErr(t, "playlist_edit", map[string]any{"playlist": "Denis", "move_entry_id": arrivalEntry}); !strings.Contains(msg, "position") {
		t.Errorf("a move without a position: %s", msg)
	}

	del := suite.Call(t, "playlist_delete", map[string]any{"playlist": "denis"})
	if acc.Str(del["deleted"]) != "Denis" {
		t.Errorf("playlist_delete = %v", del)
	}
	if stillListed(t, "playlist_list", "playlists", "Denis") {
		t.Error("Denis is still listed after playlist_delete")
	}
}

// What a collection refuses, and what it reads back: an item it does not
// hold cannot be removed, an item it holds is not added twice, a collection
// nobody has is refused naming those there are, and a sort name set is the
// sort name the server keeps.
func TestCollectionEdges(t *testing.T) {
	alien := findItem(t, "Movies", "Movie", "Alien")
	aliens := findItem(t, "Movies", "Movie", "Aliens")
	// a name the provider cassettes know: Jellyfin looks a new collection's
	// name up at TMDB
	out := suite.Call(t, "collection_create", map[string]any{"name": "Zzyzx Edges", "item_ids": []any{alien}})
	id := acc.Str(out["id"])
	deleteLater(t, "collection_delete", "collection", id)

	// an item it does not hold: refused, and nothing leaves
	if msg := suite.CallErr(t, "collection_edit", map[string]any{"collection": id, "remove_items": []any{alien, aliens}}); !strings.Contains(msg, "the collection does not hold item "+aliens) {
		t.Errorf("removing an item the collection does not hold: %s", msg)
	}
	if n := collectionSize(t, id, 1); n != 1 {
		t.Errorf("after the refused removal the collection holds %d items, want Alien still", n)
	}
	// an item it holds is counted, not added again
	add := suite.Call(t, "collection_edit", map[string]any{"collection": id, "add_items": []any{alien, aliens, aliens}})
	if acc.Num(t, add["added"], "added") != 1 || acc.Num(t, add["already_held"], "already_held") != 2 {
		t.Errorf("adding Alien again and Aliens twice = %v, want 1 added and 2 already held", add)
	}
	if n := collectionSize(t, id, 2); n != 2 {
		t.Errorf("the collection holds %d items, want Alien and Aliens", n)
	}
	if msg := suite.CallErr(t, "collection_edit", map[string]any{"collection": "Zzyzx Nowhere", "add_items": []any{alien}}); !strings.Contains(msg, `no collection named "Zzyzx Nowhere" (have: `) || !strings.Contains(msg, "Zzyzx Edges") {
		t.Errorf("adding to a collection nobody has: %s", msg)
	}

	// the sort name set is the one the server keeps, which no read tool
	// shows, so it is read off the item as the server's own editor does
	// (Jellyfin sorts by a form of it with its numbers padded out)
	suite.Call(t, "collection_edit", map[string]any{"collection": id, "sort_name": "Alien 0"})
	if got := fullItem(t, id); acc.Str(got["ForcedSortName"]) != "Alien 0" {
		t.Errorf("the collection's sort name = %v (sorted by %v), want Alien 0", got["ForcedSortName"], got["SortName"])
	}
	if got := suite.Call(t, "item_get", map[string]any{"id": id}); acc.Str(got["name"]) != "Zzyzx Edges" {
		t.Errorf("a sort name renamed the collection: %v", got["name"])
	}
}

// What a playlist refuses: an edit of nothing, a move past its end or of an
// entry it does not hold; and several entries go in one removal.
func TestPlaylistEdges(t *testing.T) {
	var ids []any
	for _, title := range []string{"Alien", "Aliens", "Arrival"} {
		ids = append(ids, findItem(t, "Movies", "Movie", title))
	}
	out := suite.Call(t, "playlist_create", map[string]any{"name": "Zzyzx Edges", "item_ids": ids, "media_type": "Video"})
	id := acc.Str(out["id"])
	deleteLater(t, "playlist_delete", "playlist", id)
	entries := playlistEntries(t, id, 3)
	if len(entries) != 3 {
		t.Fatalf("the playlist = %v, want three entries", entries)
	}
	first := acc.Str(entries[0]["entry_id"])
	alien, aliens := acc.Str(ids[0]), acc.Str(ids[1])

	for want, args := range map[string]map[string]any{
		"nothing to change":                                   {"playlist": id},
		"move_item_id is required":                            {"playlist": id, "move_entry_id": first, "position": 1},
		"position 4 is outside the playlist's 3 entries":      {"playlist": id, "move_entry_id": first, "move_item_id": alien, "position": 4},
		"the playlist has no entry zzyzx (its entry ids are ": {"playlist": id, "move_entry_id": "zzyzx", "move_item_id": alien, "position": 1},
		// an entry named with an item it does not hold
		"entry " + first + " holds Alien (" + alien + ") now, not item " + aliens + "; Aliens (" + aliens + ") is entry " + acc.Str(entries[1]["entry_id"]) + " now, so nothing was changed": {"playlist": id, "move_entry_id": first, "move_item_id": aliens, "position": 3},
	} {
		if msg := suite.CallErr(t, "playlist_edit", args); !strings.Contains(msg, want) {
			t.Errorf("playlist_edit %v: %s, want %q", args, msg, want)
		}
	}
	if got := names(t, suite.Call(t, "playlist_get", map[string]any{"playlist": id})["entries"], "entries"); !slices.Equal(got, []string{"Alien", "Aliens", "Arrival"}) {
		t.Errorf("after the refused edits the playlist = %v", got)
	}

	// the first two in one removal
	if msg := suite.CallErr(t, "playlist_edit", map[string]any{"playlist": id, "remove_entries": []any{map[string]any{"entry_id": first, "item_id": aliens}, map[string]any{"entry_id": acc.Str(entries[1]["entry_id"]), "item_id": alien}}}); !strings.Contains(msg, "entry "+first+" holds Alien ("+alien+") now, not item "+aliens) {
		t.Errorf("removing entries named with each other's items: %s", msg)
	}
	if got := names(t, suite.Call(t, "playlist_get", map[string]any{"playlist": id})["entries"], "entries"); !slices.Equal(got, []string{"Alien", "Aliens", "Arrival"}) {
		t.Errorf("after the refused removal the playlist = %v", got)
	}
	rm := suite.Call(t, "playlist_edit", map[string]any{"playlist": id, "remove_entries": []any{map[string]any{"entry_id": first, "item_id": alien}, map[string]any{"entry_id": acc.Str(entries[1]["entry_id"]), "item_id": aliens}}})
	if acc.Num(t, rm["removed"], "removed") != 2 || acc.Str(rm["name"]) != "Zzyzx Edges" {
		t.Errorf("playlist_edit remove_entries of two = %v", rm)
	}
	var left []string
	for _, e := range playlistEntries(t, id, 1) {
		left = append(left, acc.Str(e["name"]))
	}
	if !slices.Equal(left, []string{"Arrival"}) {
		t.Errorf("after removing two the playlist = %v, want Arrival", left)
	}
}
