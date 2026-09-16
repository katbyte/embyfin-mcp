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
	if str(edit["name"]) != "Zzyzx Anthology" || !slices.Equal(strs(t, edit["updated_fields"], "updated_fields"), []string{"Name", "SortName", "Overview"}) {
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

func TestOrganiseFamiliesAreComplete(t *testing.T) {
	var got []string
	for _, name := range toolNames(t) {
		if strings.HasPrefix(name, "collection_") || strings.HasPrefix(name, "playlist_") {
			got = append(got, name)
		}
	}
	want := []string{
		"collection_add", "collection_create", "collection_delete", "collection_edit", "collection_get", "collection_list", "collection_remove",
		"playlist_add", "playlist_create", "playlist_delete", "playlist_edit", "playlist_get", "playlist_list", "playlist_remove",
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("organise tools = %v, want %v", got, want)
	}
}
