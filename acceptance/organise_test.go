//go:build integration

package acceptance

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCollections(t *testing.T) {
	alien := findItem(t, "Movies", "Movie", "Alien")
	aliens := findItem(t, "Movies", "Movie", "Aliens")
	blade := findItem(t, "Movies", "Movie", "Blade Runner")

	out := call(t, "collection_create", map[string]any{"name": "Alien Saga", "item_ids": []any{alien}})
	if str(out["id"]) == "" || str(out["name"]) != "Alien Saga" {
		t.Fatalf("collection_create = %v", out)
	}
	id := str(out["id"])
	t.Cleanup(func() { _, _ = invoke("collection_delete", map[string]any{"collection": id}) })

	list := call(t, "collection_list", nil)
	var names []string
	for _, c := range rows(t, list["collections"], "collections") {
		names = append(names, str(c["name"]))
	}
	if !slices.Contains(names, "Alien Saga") {
		t.Errorf("collections = %v", names)
	}

	// add two, by collection name, case-insensitively
	add := call(t, "collection_add", map[string]any{"collection": "alien saga", "item_ids": []any{aliens, blade}})
	if num(t, add["added"], "added") != 2 || str(add["to"]) != "Alien Saga" {
		t.Errorf("collection_add = %v", add)
	}
	got := call(t, "collection_get", map[string]any{"collection": id})
	names = names[:0]
	for _, it := range rows(t, got["items"], "items") {
		names = append(names, str(it["name"]))
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"Alien", "Aliens", "Blade Runner"}) {
		t.Errorf("collection = %v", names)
	}

	// remove one
	rm := call(t, "collection_remove", map[string]any{"collection": "Alien Saga", "item_ids": []any{blade}})
	if num(t, rm["removed"], "removed") != 1 || str(rm["from"]) != "Alien Saga" {
		t.Errorf("collection_remove = %v", rm)
	}
	// the servers apply the change a moment after answering
	if n := collectionSize(t, "Alien Saga", 2); n != 2 {
		t.Errorf("after remove = %d items", n)
	}
	// the film is still in the library
	if findItem(t, "Movies", "Movie", "Blade Runner") != blade {
		t.Error("Blade Runner changed id")
	}

	if msg := callErr(t, "collection_get", map[string]any{"collection": "Nope"}); !strings.Contains(msg, "Nope") || !strings.Contains(msg, "Alien Saga") {
		t.Errorf("an unknown collection should list the real ones: %s", msg)
	}

	// rename it, with a sort name and a description
	edit := call(t, "collection_edit", map[string]any{"collection": "alien saga", "name": "Zzyzx Anthology", "sort_name": "Alien 0", "overview": "The Alien films."})
	if str(edit["name"]) != "Zzyzx Anthology" || !slices.Equal(strs(t, edit["changed"], "changed"), []string{"Name", "SortName", "Overview"}) {
		t.Errorf("collection_edit = %v", edit)
	}
	if stillListed(t, "collection_list", "collections", "Alien Saga") {
		t.Error("Alien Saga is still listed after the rename")
	}
	if got := call(t, "item_get", map[string]any{"id": id}); str(got["name"]) != "Zzyzx Anthology" || str(got["overview"]) != "The Alien films." {
		t.Errorf("the renamed collection = %v", got)
	}
	if n := collectionSize(t, "Zzyzx Anthology", 2); n != 2 {
		t.Errorf("the renamed collection holds %d items", n)
	}
	if msg := callErr(t, "collection_edit", map[string]any{"collection": "Zzyzx Anthology"}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("an edit without fields: %s", msg)
	}

	del := call(t, "collection_delete", map[string]any{"collection": "Zzyzx Anthology"})
	if str(del["deleted"]) != "Zzyzx Anthology" {
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
		for _, row := range rows(t, call(t, tool, nil)[field], field) {
			if str(row["name"]) == name {
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
		got := call(t, "playlist_get", map[string]any{"playlist": playlist})
		if entries = rows(t, got["entries"], "entries"); len(entries) == want {
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
		got := call(t, "collection_get", map[string]any{"collection": collection})
		if n = len(rows(t, got["items"], "items")); n == want {
			return n
		}
		time.Sleep(500 * time.Millisecond)
	}

	return n
}

func TestPlaylists(t *testing.T) {
	dune := findItem(t, "Movies", "Movie", "Dune")
	dune2 := findItem(t, "Movies", "Movie", "Dune: Part Two")
	arrival := findItem(t, "Movies", "Movie", "Arrival")

	out := call(t, "playlist_create", map[string]any{"name": "Villeneuve", "item_ids": []any{dune}, "media_type": "Video"})
	if str(out["id"]) == "" || str(out["name"]) != "Villeneuve" {
		t.Fatalf("playlist_create = %v", out)
	}
	t.Cleanup(func() { _, _ = invoke("playlist_delete", map[string]any{"playlist": str(out["id"])}) })

	list := call(t, "playlist_list", nil)
	var names []string
	for _, p := range rows(t, list["playlists"], "playlists") {
		names = append(names, str(p["name"]))
	}
	if !slices.Contains(names, "Villeneuve") {
		t.Errorf("playlists = %v", names)
	}

	add := call(t, "playlist_add", map[string]any{"playlist": "villeneuve", "item_ids": []any{dune2, arrival}})
	if num(t, add["added"], "added") != 2 || str(add["to"]) != "Villeneuve" {
		t.Errorf("playlist_add = %v", add)
	}
	entries := playlistEntries(t, "Villeneuve", 3)
	names = names[:0]
	var entryIDs []string
	for _, e := range entries {
		names = append(names, str(e["name"]))
		entryIDs = append(entryIDs, str(e["entry_id"]))
		if str(e["entry_id"]) == "" {
			t.Errorf("entry has no entry_id: %v", e)
		}
	}
	// in playlist order
	if !slices.Equal(names, []string{"Dune", "Dune: Part Two", "Arrival"}) {
		t.Fatalf("playlist = %v", names)
	}

	rm := call(t, "playlist_remove", map[string]any{"playlist": "Villeneuve", "entry_ids": []any{entryIDs[1]}})
	if num(t, rm["removed"], "removed") != 1 || str(rm["from"]) != "Villeneuve" {
		t.Errorf("playlist_remove = %v", rm)
	}
	names = names[:0]
	for _, e := range playlistEntries(t, str(out["id"]), 2) {
		names = append(names, str(e["name"]))
	}
	if !slices.Equal(names, []string{"Dune", "Arrival"}) {
		t.Errorf("after remove = %v", names)
	}
	// both servers ignore an entry they do not hold, so the tool checks
	if msg := callErr(t, "playlist_remove", map[string]any{"playlist": "Villeneuve", "entry_ids": []any{entryIDs[1]}}); !strings.Contains(msg, "no entry "+entryIDs[1]) {
		t.Errorf("removing an entry already removed: %s", msg)
	}

	if msg := callErr(t, "playlist_get", map[string]any{"playlist": "Nope"}); !strings.Contains(msg, "Nope") || !strings.Contains(msg, "Villeneuve") {
		t.Errorf("an unknown playlist should list the real ones: %s", msg)
	}

	// move Arrival to the top, and rename the playlist
	var arrivalEntry string
	for _, e := range playlistEntries(t, str(out["id"]), 2) {
		if str(e["name"]) == "Arrival" {
			arrivalEntry = str(e["entry_id"])
		}
	}
	edit := call(t, "playlist_edit", map[string]any{"playlist": "Villeneuve", "move_entry_id": arrivalEntry, "position": 1, "name": "Denis"})
	names = names[:0]
	for _, e := range rows(t, edit["entries"], "entries") {
		names = append(names, str(e["name"]))
	}
	if str(edit["name"]) != "Denis" || len(strs(t, edit["changed"], "changed")) != 2 || !slices.Equal(names, []string{"Arrival", "Dune"}) {
		t.Errorf("playlist_edit = %v (entries %v)", edit, names)
	}
	if stillListed(t, "playlist_list", "playlists", "Villeneuve") {
		t.Error("Villeneuve is still listed after the rename")
	}
	names = names[:0]
	for _, e := range playlistEntries(t, "Denis", 2) {
		names = append(names, str(e["name"]))
	}
	if !slices.Equal(names, []string{"Arrival", "Dune"}) {
		t.Errorf("after the move = %v", names)
	}
	if msg := callErr(t, "playlist_edit", map[string]any{"playlist": "Denis", "move_entry_id": arrivalEntry}); !strings.Contains(msg, "position") {
		t.Errorf("a move without a position: %s", msg)
	}

	del := call(t, "playlist_delete", map[string]any{"playlist": "denis"})
	if str(del["deleted"]) != "Denis" {
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
	out := call(t, "collection_create", map[string]any{"name": "Zzyzx Edges", "item_ids": []any{alien}})
	id := str(out["id"])
	deleteLater(t, "collection_delete", "collection", id)

	// an item it does not hold: refused, and nothing leaves
	if msg := callErr(t, "collection_remove", map[string]any{"collection": id, "item_ids": []any{alien, aliens}}); !strings.Contains(msg, "the collection does not hold item "+aliens) {
		t.Errorf("removing an item the collection does not hold: %s", msg)
	}
	if n := collectionSize(t, id, 1); n != 1 {
		t.Errorf("after the refused removal the collection holds %d items, want Alien still", n)
	}
	// an item it holds is counted, not added again
	add := call(t, "collection_add", map[string]any{"collection": id, "item_ids": []any{alien, aliens, aliens}})
	if num(t, add["added"], "added") != 1 || num(t, add["already_held"], "already_held") != 2 {
		t.Errorf("adding Alien again and Aliens twice = %v, want 1 added and 2 already held", add)
	}
	if n := collectionSize(t, id, 2); n != 2 {
		t.Errorf("the collection holds %d items, want Alien and Aliens", n)
	}
	if msg := callErr(t, "collection_add", map[string]any{"collection": "Zzyzx Nowhere", "item_ids": []any{alien}}); !strings.Contains(msg, `no collection named "Zzyzx Nowhere" (have: `) || !strings.Contains(msg, "Zzyzx Edges") {
		t.Errorf("adding to a collection nobody has: %s", msg)
	}

	// the sort name set is the one the server keeps, which no read tool
	// shows, so it is read off the item as the server's own editor does
	// (Jellyfin sorts by a form of it with its numbers padded out)
	call(t, "collection_edit", map[string]any{"collection": id, "sort_name": "Alien 0"})
	if got := fullItem(t, id); str(got["ForcedSortName"]) != "Alien 0" {
		t.Errorf("the collection's sort name = %v (sorted by %v), want Alien 0", got["ForcedSortName"], got["SortName"])
	}
	if got := call(t, "item_get", map[string]any{"id": id}); str(got["name"]) != "Zzyzx Edges" {
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
	out := call(t, "playlist_create", map[string]any{"name": "Zzyzx Edges", "item_ids": ids, "media_type": "Video"})
	id := str(out["id"])
	deleteLater(t, "playlist_delete", "playlist", id)
	entries := playlistEntries(t, id, 3)
	if len(entries) != 3 {
		t.Fatalf("the playlist = %v, want three entries", entries)
	}
	first := str(entries[0]["entry_id"])

	for want, args := range map[string]map[string]any{
		"nothing to change": {"playlist": id},
		"position 4 is outside the playlist's 3 entries":      {"playlist": id, "move_entry_id": first, "position": 4},
		"the playlist has no entry zzyzx (its entry ids are ": {"playlist": id, "move_entry_id": "zzyzx", "position": 1},
	} {
		if msg := callErr(t, "playlist_edit", args); !strings.Contains(msg, want) {
			t.Errorf("playlist_edit %v: %s, want %q", args, msg, want)
		}
	}
	if got := names(t, call(t, "playlist_get", map[string]any{"playlist": id})["entries"], "entries"); !slices.Equal(got, []string{"Alien", "Aliens", "Arrival"}) {
		t.Errorf("after the refused edits the playlist = %v", got)
	}

	// the first two in one removal
	rm := call(t, "playlist_remove", map[string]any{"playlist": id, "entry_ids": []any{first, str(entries[1]["entry_id"])}})
	if num(t, rm["removed"], "removed") != 2 || str(rm["from"]) != "Zzyzx Edges" {
		t.Errorf("playlist_remove of two = %v", rm)
	}
	var left []string
	for _, e := range playlistEntries(t, id, 1) {
		left = append(left, str(e["name"]))
	}
	if !slices.Equal(left, []string{"Arrival"}) {
		t.Errorf("after removing two the playlist = %v, want Arrival", left)
	}
}
