//go:build integration

package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Journeys: the tools chained the way a session uses them, against state
// the servers change underneath (a scan), across users, and repeated. Each
// puts back what it changes.

// deleteLater removes what a test created once it ends, retrying a delete the
// server refuses while a refresh holds the item (both servers answer a 500
// then), and reports one that never succeeds rather than leaving it for the
// tests after.
func deleteLater(t *testing.T, tool, field, id string) {
	t.Helper()

	t.Cleanup(func() {
		var err error
		for range 10 {
			if _, err = invoke(tool, map[string]any{field: id}); err == nil {
				return
			}
			time.Sleep(time.Second)
		}
		t.Errorf("cleanup %s %s: %v", tool, id, err)
	})
}

// entryNamed returns the entry id of the first entry of a playlist that is
// the named item, as playlist_get lists it now.
func entryNamed(t *testing.T, playlist, name string) string {
	t.Helper()

	for _, e := range rows(t, call(t, "playlist_get", map[string]any{"playlist": playlist})["entries"], "entries") {
		if str(e["name"]) == name {
			return str(e["entry_id"])
		}
	}
	t.Fatalf("%s is not in the playlist", name)

	return ""
}

// A library scan validates every playlist and collection and, on Emby, saves
// each as it found it: edits made while one runs must all be there once it
// has finished.
func TestEditsDuringAScan(t *testing.T) {
	dune := findItem(t, "Movies", "Movie", "Dune")
	dune2 := findItem(t, "Movies", "Movie", "Dune: Part Two")
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	alien := findItem(t, "Movies", "Movie", "Alien")

	if out := call(t, "library_scan", nil); !boolOf(out["started"]) {
		t.Fatalf("library_scan = %v", out)
	}
	t.Cleanup(func() { _ = waitForScan() })

	pl := str(call(t, "playlist_create", map[string]any{"name": "Zzyzx Scan Race", "item_ids": []any{dune}, "media_type": "Video"})["id"])
	deleteLater(t, "playlist_delete", "playlist", pl)
	if add := call(t, "playlist_add", map[string]any{"playlist": pl, "item_ids": []any{dune2, arrival}}); num(t, add["added"], "added") != 2 {
		t.Errorf("playlist_add = %v", add)
	}
	// a scan can renumber Emby's entries between reading one and using it,
	// which the tool reports; a caller reads the playlist again
	move := func() map[string]any {
		args := map[string]any{"playlist": pl, "move_entry_id": entryNamed(t, pl, "Arrival"), "position": 1}
		out, err := invoke("playlist_edit", args)
		if err != nil && strings.Contains(err.Error(), "no entry") {
			args["move_entry_id"] = entryNamed(t, pl, "Arrival")
			out, err = invoke("playlist_edit", args)
		}
		if err != nil {
			t.Fatalf("playlist_edit: %v", err)
		}
		return out
	}
	move()
	if rm := call(t, "playlist_remove", map[string]any{"playlist": pl, "entry_ids": []any{entryNamed(t, pl, "Dune: Part Two")}}); num(t, rm["removed"], "removed") != 1 {
		t.Errorf("playlist_remove = %v", rm)
	}

	col := str(call(t, "collection_create", map[string]any{"name": "Zzyzx Scan Race", "item_ids": []any{alien}})["id"])
	deleteLater(t, "collection_delete", "collection", col)
	if add := call(t, "collection_add", map[string]any{"collection": col, "item_ids": []any{dune, arrival}}); num(t, add["added"], "added") != 2 {
		t.Errorf("collection_add = %v", add)
	}
	call(t, "collection_remove", map[string]any{"collection": col, "item_ids": []any{dune}})

	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range rows(t, call(t, "playlist_get", map[string]any{"playlist": pl})["entries"], "entries") {
		got = append(got, str(e["name"]))
	}
	if !slices.Equal(got, []string{"Arrival", "Dune"}) {
		t.Errorf("after the scan the playlist is %v, want [Arrival Dune]", got)
	}
	if got := names(t, call(t, "collection_get", map[string]any{"collection": col})["items"], "items"); !slices.Equal(sorted(got), []string{"Alien", "Arrival"}) {
		t.Errorf("after the scan the collection holds %v, want [Alien Arrival]", got)
	}
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)

	return s
}

// fullItem reads an item the way the servers' own editors do, for putting
// back a field no tool clears.
func fullItem(t *testing.T, id string) map[string]any {
	t.Helper()

	path := "/Items/" + id + "?userId=" + os.Getenv("EMBYFIN_TEST_ADMIN_ID")
	if !isJellyfin() {
		path = "/Users/" + os.Getenv("EMBYFIN_TEST_ADMIN_ID") + "/Items/" + id
	}
	status, raw := api(t, http.MethodGet, path, "", nil)
	var item map[string]any
	if status != http.StatusOK || json.Unmarshal(raw, &item) != nil {
		t.Fatalf("reading item %s: HTTP %d: %.200s", id, status, raw)
	}

	return item
}

// updateItem posts an item back with fields changed.
func updateItem(t *testing.T, id string, change map[string]any) {
	t.Helper()

	item := fullItem(t, id)
	for k, v := range change {
		item[k] = v
	}
	if status, raw := api(t, http.MethodPost, "/Items/"+id, "", item); status/100 != 2 {
		t.Errorf("updating item %s: HTTP %d: %.200s", id, status, raw)
	}
}

// Every audit whose findings a tool can clear: audit, fix with the tool the
// finding points at, audit again, and put the defect back.
func TestAuditsAreFixable(t *testing.T) {
	arrival := findItem(t, "Messy Movies", "Movie", "Arrival")
	mononoke := findItem(t, "Messy Movies", "Movie", "Princess Mononoke")
	dune := findItem(t, "Messy Movies", "Movie", "Dune")
	messy := map[string]any{"library": "Messy Movies"}

	t.Run("missing overview", func(t *testing.T) {
		if got := findings(t, call(t, "audit_missing_overview", messy)); !slices.Contains(got, "Arrival") {
			t.Fatalf("audit_missing_overview = %v, want Arrival among them", got)
		}
		call(t, "item_edit", map[string]any{"id": arrival, "overview": "A linguist is recruited to talk to the visitors."})
		t.Cleanup(func() { updateItem(t, arrival, map[string]any{"Overview": ""}) })
		if got := findings(t, call(t, "audit_missing_overview", messy)); slices.Contains(got, "Arrival") {
			t.Errorf("after item_edit audit_missing_overview = %v", got)
		}
	})

	t.Run("missing poster", func(t *testing.T) {
		if got := findings(t, call(t, "audit_missing_poster", messy)); !slices.Contains(got, "Arrival") {
			t.Fatalf("audit_missing_poster = %v, want Arrival among them", got)
		}
		// the providers are off in the messy library, so the poster comes from
		// the clean copy's candidates
		clean := findItem(t, "Movies", "Movie", "Arrival")
		cands := rows(t, call(t, "item_artwork", map[string]any{"id": clean, "type": "Primary", "limit": 1})["candidates"], "candidates")
		if len(cands) == 0 {
			t.Fatal("no poster candidates for Arrival")
		}
		call(t, "item_artwork_set", map[string]any{"id": arrival, "url": str(cands[0]["url"])})
		t.Cleanup(func() {
			if status, raw := api(t, http.MethodDelete, "/Items/"+arrival+"/Images/Primary", "", nil); status/100 != 2 {
				t.Errorf("removing the poster: HTTP %d: %s", status, raw)
			}
		})
		ok := false
		for range 20 {
			if ok = !slices.Contains(findings(t, call(t, "audit_missing_poster", messy)), "Arrival"); ok {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !ok {
			t.Error("after item_artwork_set Arrival still has no poster")
		}
	})

	t.Run("year mismatch", func(t *testing.T) {
		if got := findings(t, call(t, "audit_year_mismatch", messy)); !slices.Equal(got, []string{"Dune"}) {
			t.Fatalf("audit_year_mismatch = %v, want [Dune]", got)
		}
		call(t, "item_edit", map[string]any{"id": dune, "year": 2021})
		t.Cleanup(func() { _, _ = invoke("item_edit", map[string]any{"id": dune, "year": 1984}) })
		if n := num(t, call(t, "audit_year_mismatch", messy)["total_findings"], "total_findings"); n != 0 {
			t.Errorf("after item_edit audit_year_mismatch found %d", n)
		}
	})

	t.Run("unmatched", func(t *testing.T) {
		if got := findings(t, call(t, "audit_missing_metadata_provider", messy)); !slices.Equal(got, []string{"Princess Mononoke"}) {
			t.Fatalf("audit_missing_metadata_provider = %v", got)
		}
		// the messy library has its fetchers off, so the search runs without
		// the item and the match still sets its ids
		cands := rows(t, call(t, "item_identify", map[string]any{"id": mononoke, "kind": "movie"})["candidates"], "candidates")
		// by either id: without the item Jellyfin's search is answered by OMDb,
		// whose candidates carry the imdb id alone
		idx := -1
		for i, c := range cands {
			if ids, _ := c["metadata_provider_ids"].(map[string]any); idx < 0 && (str(ids["tmdb"]) == "128" || str(ids["imdb"]) == "tt0119698") {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatalf("no Princess Mononoke (tmdb 128, imdb tt0119698) among the candidates: %v", cands)
		}
		call(t, "item_identify_apply", map[string]any{"id": mononoke, "kind": "movie", "candidate": idx})
		t.Cleanup(func() { updateItem(t, mononoke, map[string]any{"ProviderIds": map[string]any{}, "Overview": ""}) })
		ok := false
		for range 20 {
			if ok = num(t, call(t, "audit_missing_metadata_provider", messy)["total_findings"], "total_findings") == 0; ok {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !ok {
			t.Error("after item_identify_apply Princess Mononoke is still unmatched")
		}
	})

	t.Run("unwatched", func(t *testing.T) {
		movies := map[string]any{"library": "Movies"}
		if got := findings(t, call(t, "audit_unwatched", movies)); !slices.Contains(got, "Princess Mononoke") {
			t.Fatalf("audit_unwatched = %v, want Princess Mononoke among them", got)
		}
		id := findItem(t, "Movies", "Movie", "Princess Mononoke")
		call(t, "item_set_watched", map[string]any{"id": id, "user": "alice", "watched": true})
		t.Cleanup(func() { _, _ = invoke("item_set_watched", map[string]any{"id": id, "user": "alice", "watched": false}) })
		if got := findings(t, call(t, "audit_unwatched", movies)); slices.Contains(got, "Princess Mononoke") {
			t.Errorf("after item_set_watched audit_unwatched = %v", got)
		}
	})
}

// A client plays something: the session shows it, and the history, activity
// and watch-state tools all agree about it afterwards.
func TestPlaybackIsRecorded(t *testing.T) {
	device, token := signInPlayer(t)
	dune := findItem(t, "Movies", "Movie", "Dune")
	dune2 := findItem(t, "Movies", "Movie", "Dune: Part Two")
	aliceBefore := call(t, "user_get", map[string]any{"user": "alice"})
	t.Cleanup(func() {
		_, _ = invoke("item_set_watched", map[string]any{"id": dune, "user": "alice", "watched": false})
	})

	// a play session the way a client starts one: Emby answers a report
	// without the PlaySessionId PlaybackInfo hands out with a 400
	status, raw := api(t, http.MethodPost, "/Items/"+dune+"/PlaybackInfo?UserId="+os.Getenv("EMBYFIN_TEST_USER_ID"), token, map[string]any{})
	var info struct {
		PlaySessionID string `json:"PlaySessionId"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &info) != nil {
		t.Fatalf("PlaybackInfo: HTTP %d: %.200s", status, raw)
	}
	report := func(path string, ticks int64) {
		t.Helper()
		body := map[string]any{"ItemId": dune, "PlaySessionId": info.PlaySessionID, "PositionTicks": ticks, "CanSeek": true, "PlayMethod": "DirectPlay"}
		if status, raw := api(t, http.MethodPost, path, token, body); status/100 != 2 {
			t.Fatalf("%s: HTTP %d: %s", path, status, raw)
		}
	}
	report("/Sessions/Playing", 0)

	playing := false
	for range 20 {
		for _, s := range rows(t, call(t, "session_list", nil)["sessions"], "sessions") {
			if str(s["device"]) == device && str(s["now_playing"]) == "Dune" && str(s["user"]) == "alice" {
				playing = true
			}
		}
		if playing {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !playing {
		t.Errorf("session_list does not show alice playing Dune: %v", call(t, "session_list", nil)["sessions"])
	}

	// stopped half way through the one-second file, which is past the point
	// both servers count as finished for something that short
	report("/Sessions/Playing/Progress", 5_000_000)
	report("/Sessions/Playing/Stopped", 5_000_000)

	var played map[string]any
	for range 20 {
		for _, u := range rows(t, call(t, "item_last_watched", map[string]any{"id": dune})["users"], "users") {
			if str(u["user"]) == "alice" && boolOf(u["played"]) {
				played = u
			}
		}
		if played != nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if played == nil || num(t, played["play_count"], "play_count") < 1 {
		t.Errorf("item_last_watched does not show alice finishing Dune: %v", played)
	}
	if after := call(t, "user_get", map[string]any{"user": "alice"}); num(t, after["movies_watched"], "movies_watched") != num(t, aliceBefore["movies_watched"], "movies_watched")+1 {
		t.Errorf("alice movies_watched %v, was %v", after["movies_watched"], aliceBefore["movies_watched"])
	}

	// the activity log: the playback is Dune's and alice's, and not Dune: Part
	// Two's, whose title contains Dune's
	var history []map[string]any
	for range 20 {
		if history = rows(t, call(t, "user_history", map[string]any{"user": "alice", "days": 1})["watched"], "watched"); len(history) > 0 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if len(history) == 0 || str(history[0]["name"]) != "Dune" || str(history[0]["event"]) == "" {
		t.Errorf("user_history alice = %v", history)
	}
	entries := strs(t, call(t, "item_watch_history", map[string]any{"id": dune, "days": 1})["entries"], "entries")
	if !slices.ContainsFunc(entries, func(e string) bool { return strings.Contains(e, "alice") }) {
		t.Errorf("item_watch_history Dune = %v", entries)
	}
	if other := strs(t, call(t, "item_watch_history", map[string]any{"id": dune2, "days": 1})["entries"], "entries"); slices.ContainsFunc(other, func(e string) bool { return strings.Contains(e, "alice") }) {
		t.Errorf("item_watch_history Dune: Part Two picked up Dune's playback: %v", other)
	}
	activity := rows(t, call(t, "server_activity", map[string]any{"days": 1, "limit": 50})["entries"], "entries")
	if !slices.ContainsFunc(activity, func(e map[string]any) bool {
		return strings.Contains(str(e["summary"]), "alice") && strings.Contains(str(e["summary"]), "Dune")
	}) {
		t.Errorf("server_activity has no playback of Dune by alice: %v", activity)
	}
}

// audit_all's counts are the audits' own: each row equals what the audit it
// names reports for the same library.
func TestAuditAllMatchesEachAudit(t *testing.T) {
	for _, library := range []string{"Messy Movies", "Messy Shows", ""} {
		args := map[string]any{}
		if library != "" {
			args["library"] = library
		}
		for _, row := range rows(t, call(t, "audit_all", args)["audits"], "audits") {
			name := str(row["audit"])
			out := call(t, name, args)
			count := out["total_findings"]
			if name == "audit_duplicates" {
				count = out["total_groups"]
			}
			if num(t, count, name) != num(t, row["findings"], "findings") || num(t, out["items_scanned"], name) != num(t, row["items_scanned"], "items_scanned") {
				t.Errorf("%q %s: audit_all counted %v of %v, the audit %v of %v", library, name, row["findings"], row["items_scanned"], count, out["items_scanned"])
			}
		}
	}
}

// The lookups change nothing: a snapshot of the items, users and lists they
// touch is the same after every read-only lookup has run over them.
func TestLookupsChangeNothing(t *testing.T) {
	blade := findItem(t, "Movies", "Movie", "Blade Runner")
	mononoke := findItem(t, "Movies", "Movie", "Princess Mononoke")
	messyMononoke := findItem(t, "Messy Movies", "Movie", "Princess Mononoke")
	messyArrival := findItem(t, "Messy Movies", "Movie", "Arrival")
	severance := findItem(t, "Shows", "Series", "Severance")

	snapshot := func() map[string]any {
		s := map[string]any{}
		for _, id := range []string{blade, mononoke, messyMononoke, messyArrival, severance} {
			s["item "+id] = call(t, "item_get", map[string]any{"id": id})
		}
		for _, u := range []string{"root", "alice"} {
			s["user "+u] = call(t, "user_get", map[string]any{"user": u})
		}
		s["playlists"] = call(t, "playlist_list", nil)
		s["collections"] = call(t, "collection_list", nil)
		s["libraries"] = call(t, "library_list", nil)
		s["filters"] = call(t, "library_filters", map[string]any{"library": "Movies"})
		return s
	}
	before := snapshot()

	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"item_identify", map[string]any{"id": blade, "kind": "movie"}},
		{"item_identify", map[string]any{"id": messyMononoke, "kind": "movie"}},
		{"item_artwork", map[string]any{"id": blade, "type": "Primary", "limit": 2}},
		{"item_similar", map[string]any{"id": blade, "limit": 5}},
		{"item_instant_mix", map[string]any{"id": blade, "limit": 5}},
		{"item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "78"}},
		{"item_last_watched", map[string]any{"id": mononoke}},
		{"item_watch_history", map[string]any{"id": mononoke, "days": 7}},
		{"library_items", map[string]any{"library": "Movies", "user": "alice", "watched": "unwatched"}},
		{"library_search", map[string]any{"query": "Princess"}},
		{"library_people", map[string]any{"name": "Scott"}},
		{"person_get", map[string]any{"person": "Ridley Scott"}},
		{"show_missing", map[string]any{"series_id": severance}},
		{"show_episodes", map[string]any{"series_id": severance}},
		{"audit_all", nil},
		{"audit_unwatched", map[string]any{"types": "Movie,Series"}},
		{"user_stats", map[string]any{"user": "alice"}},
		{"user_next_up", map[string]any{"user": "alice"}},
		{"user_in_progress", map[string]any{"user": "alice"}},
	} {
		call(t, c.tool, c.args)
	}

	after := snapshot()
	for k, v := range before {
		if !reflect.DeepEqual(v, after[k]) {
			b, _ := json.Marshal(v)
			a, _ := json.Marshal(after[k])
			t.Errorf("%s changed:\nbefore %s\nafter  %s", k, b, a)
		}
	}
}

// libraryIDs maps library names to the id a user's library access lists them
// by: Emby's Guid (its numeric id grants nothing), Jellyfin's ItemId.
func libraryIDs(t *testing.T) map[string]string {
	t.Helper()

	type folder struct {
		Name   string
		ItemID string `json:"ItemId"`
		GUID   string `json:"Guid"`
	}
	var folders []folder
	if isJellyfin() {
		status, raw := api(t, http.MethodGet, "/Library/VirtualFolders", "", nil)
		if status != http.StatusOK || json.Unmarshal(raw, &folders) != nil {
			t.Fatalf("listing libraries: HTTP %d: %.200s", status, raw)
		}
	} else {
		var page struct{ Items []folder }
		status, raw := api(t, http.MethodGet, "/Library/VirtualFolders/Query", "", nil)
		if status != http.StatusOK || json.Unmarshal(raw, &page) != nil {
			t.Fatalf("listing libraries: HTTP %d: %.200s", status, raw)
		}
		folders = page.Items
	}

	ids := map[string]string{}
	for _, f := range folders {
		ids[f.Name] = f.ItemID
		if f.GUID != "" {
			ids[f.Name] = f.GUID
		}
	}

	return ids
}

// restrictAlice lets alice see only the named libraries until the test ends.
// No tool changes a user's access, so it goes to the HTTP API.
func restrictAlice(t *testing.T, libraries ...string) {
	t.Helper()

	alice := os.Getenv("EMBYFIN_TEST_USER_ID")
	status, raw := api(t, http.MethodGet, "/Users/"+alice, "", nil)
	var user struct {
		Policy map[string]any
	}
	if status != http.StatusOK || json.Unmarshal(raw, &user) != nil || user.Policy == nil {
		t.Fatalf("reading alice: HTTP %d: %.200s", status, raw)
	}
	original, _ := json.Marshal(user.Policy)
	t.Cleanup(func() {
		var policy map[string]any
		_ = json.Unmarshal(original, &policy)
		if status, raw := api(t, http.MethodPost, "/Users/"+alice+"/Policy", "", policy); status/100 != 2 {
			t.Errorf("restoring alice's access: HTTP %d: %s", status, raw)
		}
	})

	ids := libraryIDs(t)
	enabled := []string{}
	for _, l := range libraries {
		enabled = append(enabled, ids[l])
	}
	user.Policy["EnableAllFolders"] = false
	user.Policy["EnabledFolders"] = enabled
	if status, raw := api(t, http.MethodPost, "/Users/"+alice+"/Policy", "", user.Policy); status/100 != 2 {
		t.Fatalf("restricting alice: HTTP %d: %s", status, raw)
	}
}

// A user who may see only Movies: user_get says so, and the tools that read
// a library in that user's view keep to it.
func TestUserLibraryAccess(t *testing.T) {
	restrictAlice(t, "Movies")

	got := call(t, "user_get", map[string]any{"user": "alice"})
	libs := strs(t, got["libraries"], "libraries")
	if boolOf(got["all_libraries"]) || !slices.Contains(libs, "Movies") || slices.Contains(libs, "Shows") || slices.Contains(libs, "Messy Movies") {
		t.Errorf("user_get alice: all_libraries %v, libraries %v, want Movies alone", got["all_libraries"], libs)
	}
	if n := num(t, call(t, "library_items", map[string]any{"library": "Movies", "user": "alice"})["total"], "total"); n != 8 {
		t.Errorf("alice sees %d films in Movies, want 8", n)
	}
	for tool, args := range map[string]map[string]any{
		"library_items": {"library": "Shows", "user": "alice"},
		"user_stats":    {"library": "Shows", "user": "alice"},
	} {
		if msg := callErr(t, tool, args); !strings.Contains(msg, "cannot see the Shows library") {
			t.Errorf("%s in a library alice cannot see: %s", tool, msg)
		}
	}
	// root still sees everything
	if root := call(t, "user_get", nil); !boolOf(root["all_libraries"]) {
		t.Errorf("root = %v", root)
	}
}

// Writes repeated: the second changes nothing, or says what it did.
func TestWritesTwice(t *testing.T) {
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	alien := findItem(t, "Movies", "Movie", "Alien")
	aliens := findItem(t, "Movies", "Movie", "Aliens")

	t.Run("watched and favourite", func(t *testing.T) {
		// Aliens: its tmdb id is its own, and Emby keeps watch state by
		// provider id, so marking a film with a copy elsewhere marks both
		before := call(t, "user_get", map[string]any{"user": "alice"})
		t.Cleanup(func() {
			_, _ = invoke("item_set_watched", map[string]any{"id": aliens, "user": "alice", "watched": false})
			_, _ = invoke("item_set_favourite", map[string]any{"id": aliens, "user": "alice", "favourite": false})
		})
		for range 2 {
			if out := call(t, "item_set_watched", map[string]any{"id": aliens, "user": "alice", "watched": true}); !boolOf(out["watched"]) {
				t.Errorf("item_set_watched = %v", out)
			}
			if out := call(t, "item_set_favourite", map[string]any{"id": aliens, "user": "alice", "favourite": true}); !boolOf(out["favourite"]) {
				t.Errorf("item_set_favourite = %v", out)
			}
			after := call(t, "user_get", map[string]any{"user": "alice"})
			if num(t, after["movies_watched"], "movies_watched") != num(t, before["movies_watched"], "movies_watched")+1 ||
				num(t, after["favourites"], "favourites") != num(t, before["favourites"], "favourites")+1 {
				t.Errorf("alice after marking Aliens: %v watched %v favourites, was %v and %v", after["movies_watched"], after["favourites"], before["movies_watched"], before["favourites"])
			}
		}
	})

	t.Run("collection add", func(t *testing.T) {
		col := str(call(t, "collection_create", map[string]any{"name": "Zzyzx Twice", "item_ids": []any{alien}})["id"])
		deleteLater(t, "collection_delete", "collection", col)
		if out := call(t, "collection_add", map[string]any{"collection": col, "item_ids": []any{alien, aliens}}); num(t, out["added"], "added") != 1 || num(t, out["already_held"], "already_held") != 1 {
			t.Errorf("adding a member and a new item = %v", out)
		}
		if out := call(t, "collection_add", map[string]any{"collection": col, "item_ids": []any{alien}}); num(t, out["added"], "added") != 0 || num(t, out["already_held"], "already_held") != 1 {
			t.Errorf("adding a member again = %v", out)
		}
		if n := collectionSize(t, col, 2); n != 2 {
			t.Errorf("the collection holds %d, want 2", n)
		}
	})

	t.Run("playlist add", func(t *testing.T) {
		pl := str(call(t, "playlist_create", map[string]any{"name": "Zzyzx Twice", "item_ids": []any{alien}, "media_type": "Video"})["id"])
		deleteLater(t, "playlist_delete", "playlist", pl)
		// a playlist may hold an item twice
		if out := call(t, "playlist_add", map[string]any{"playlist": pl, "item_ids": []any{alien}}); num(t, out["added"], "added") != 1 {
			t.Errorf("adding it again = %v", out)
		}
		entries := playlistEntries(t, pl, 2)
		if len(entries) != 2 || str(entries[0]["name"]) != "Alien" || str(entries[1]["name"]) != "Alien" {
			t.Fatalf("the playlist = %v", entries)
		}
		// removing one entry: Emby numbers them, Jellyfin's is the item id, so
		// there it takes both
		want := 1
		if isJellyfin() {
			want = 2
		}
		if out := call(t, "playlist_remove", map[string]any{"playlist": pl, "entry_ids": []any{str(entries[0]["entry_id"])}}); num(t, out["removed"], "removed") != want {
			t.Errorf("removing one of the two = %v, want %d removed", out, want)
		}
		if n := len(playlistEntries(t, pl, 2-want)); n != 2-want {
			t.Errorf("the playlist holds %d, want %d", n, 2-want)
		}
	})

	t.Run("metadata rename", func(t *testing.T) {
		call(t, "item_batch_edit", map[string]any{"ids": []any{arrival}, "add_tags": []any{"zzyzx-twice"}})
		t.Cleanup(func() {
			_, _ = invoke("item_batch_edit", map[string]any{"ids": []any{arrival}, "remove_tags": []any{"zzyzx-twice", "zzyzx-once"}})
		})
		rename := map[string]any{"field": "tags", "from": "zzyzx-twice", "to": "zzyzx-once", "library": "Movies"}
		if out := call(t, "metadata_rename", rename); num(t, out["items_updated"], "items_updated") != 1 {
			t.Errorf("the rename = %v", out)
		}
		if out := call(t, "metadata_rename", rename); num(t, out["items_updated"], "items_updated") != 0 {
			t.Errorf("the rename again = %v", out)
		}
	})
}

// Everything that resolves by name resolves by id to the same thing, and a
// name several share is refused with their ids.
func TestResolveByIDAndName(t *testing.T) {
	for _, l := range rows(t, call(t, "library_list", nil)["libraries"], "libraries") {
		byID := call(t, "library_get", map[string]any{"library": str(l["id"])})
		byName := call(t, "library_get", map[string]any{"library": str(l["name"])})
		if str(byID["name"]) != str(l["name"]) || str(byName["id"]) != str(l["id"]) {
			t.Errorf("library %v: by id %v, by name %v", l["name"], byID["name"], byName["id"])
		}
	}
	for _, u := range rows(t, call(t, "user_list", nil)["users"], "users") {
		byID := call(t, "user_get", map[string]any{"user": str(u["id"])})
		byName := call(t, "user_get", map[string]any{"user": str(u["name"])})
		if str(byID["name"]) != str(u["name"]) || str(byName["id"]) != str(u["id"]) {
			t.Errorf("user %v: by id %v, by name %v", u["name"], byID["name"], byName["id"])
		}
	}
	// a people search matches on a word (Emby lists Adam Scott too)
	var ridley string
	for _, p := range rows(t, call(t, "library_people", map[string]any{"name": "Ridley Scott"})["people"], "people") {
		if str(p["name"]) == "Ridley Scott" {
			ridley = str(p["id"])
		}
	}
	if ridley == "" {
		t.Fatal("library_people found no Ridley Scott")
	}
	if byID := call(t, "person_get", map[string]any{"person": ridley}); str(byID["name"]) != "Ridley Scott" {
		t.Errorf("person_get by id = %v", byID["name"])
	}
	if byName := call(t, "person_get", map[string]any{"person": "Ridley Scott"}); str(byName["id"]) != ridley {
		t.Errorf("person_get by name = %v, want %s", byName["id"], ridley)
	}

	// the films named Dune, across libraries, each resolve to themselves
	for _, it := range rows(t, call(t, "library_search", map[string]any{"query": "Dune", "types": "Movie"})["items"], "items") {
		got := call(t, "item_get", map[string]any{"id": str(it["id"])})
		if str(got["name"]) != str(it["name"]) || str(got["path"]) != str(it["path"]) {
			t.Errorf("item %v: item_get says %v at %v", it["id"], got["name"], got["path"])
		}
	}

	// two playlists may share a name: by name is then refused, with the ids
	var ids []string
	for range 2 {
		id := str(call(t, "playlist_create", map[string]any{"name": "Zzyzx Twin"})["id"])
		ids = append(ids, id)
		deleteLater(t, "playlist_delete", "playlist", id)
	}
	msg := callErr(t, "playlist_get", map[string]any{"playlist": "zzyzx twin"})
	if !strings.Contains(msg, "2 playlists are named") || !strings.Contains(msg, ids[0]) || !strings.Contains(msg, ids[1]) {
		t.Errorf("playlist_get by a shared name: %s", msg)
	}
	for _, id := range ids {
		if got := call(t, "playlist_get", map[string]any{"playlist": id}); str(got["name"]) != "Zzyzx Twin" {
			t.Errorf("playlist_get by id %s = %v", id, got["name"])
		}
	}

	// a collection is a folder named after it: the servers answer a second
	// create under the name with the first (Jellyfin replacing its items), so
	// the tool refuses one and the first keeps what it held
	alien := findItem(t, "Movies", "Movie", "Alien")
	aliens := findItem(t, "Movies", "Movie", "Aliens")
	first := str(call(t, "collection_create", map[string]any{"name": "Zzyzx Twin", "item_ids": []any{alien}})["id"])
	deleteLater(t, "collection_delete", "collection", first)
	if msg := callErr(t, "collection_create", map[string]any{"name": "zzyzx twin", "item_ids": []any{aliens}}); !strings.Contains(msg, "exists") || !strings.Contains(msg, first) {
		t.Errorf("a second collection named Zzyzx Twin: %s", msg)
	}
	if byName := call(t, "collection_get", map[string]any{"collection": "Zzyzx Twin"}); fmt.Sprint(names(t, byName["items"], "items")) != fmt.Sprint([]string{"Alien"}) {
		t.Errorf("collection_get by name = %v", byName["items"])
	}
	if !isJellyfin() {
		if msg := callErr(t, "collection_create", map[string]any{"name": "Zzyzx Empty"}); !strings.Contains(msg, "empty collection") {
			t.Errorf("an empty collection on Emby: %s", msg)
		}
	}
}

// holds reports whether check stays true for a few seconds: a change the
// server applies in the background (a refresh) that would undo something
// shows within that.
func holds(check func() bool) bool {
	for range 10 {
		if !check() {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}

	return true
}

// eventually reports whether check comes true within about twenty seconds.
func eventually(check func() bool) bool {
	for range 40 {
		if check() {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}

	return false
}

// An edit is kept until someone replaces it: a refresh fills only what is
// missing and a scan re-reads only what changed on disk, so an edit survives
// both, on a film the providers matched and on one built from its nfo alone.
// replace_all puts the providers' metadata back, and is refused where there
// are no providers to ask.
func TestEditsSurviveARefreshAndAScan(t *testing.T) {
	for _, c := range []struct{ library, title string }{{"Movies", "Princess Mononoke"}, {"Messy Movies", "Interstellar"}} {
		t.Run(c.library, func(t *testing.T) {
			id := findItem(t, c.library, "Movie", c.title)
			before := call(t, "item_get", map[string]any{"id": id})
			t.Cleanup(func() {
				_, _ = invoke("item_batch_edit", map[string]any{"ids": []any{id}, "genres": before["genres"], "tags": before["tags"]})
				updateItem(t, id, map[string]any{"Overview": before["overview"]})
			})

			const overview = "Zzyzx: an overview typed by hand."
			call(t, "item_edit", map[string]any{"id": id, "overview": overview})
			call(t, "item_batch_edit", map[string]any{"ids": []any{id}, "add_tags": []any{"zzyzx-kept"}, "add_genres": []any{"Zzyzx Kept"}})
			edited := func() bool {
				got := call(t, "item_get", map[string]any{"id": id})
				return str(got["overview"]) == overview && slices.Contains(strs(t, got["tags"], "tags"), "zzyzx-kept") && slices.Contains(strs(t, got["genres"], "genres"), "Zzyzx Kept")
			}
			if !edited() {
				t.Fatalf("the edit did not land: %v", call(t, "item_get", map[string]any{"id": id}))
			}

			call(t, "item_refresh", map[string]any{"id": id})
			if !holds(edited) {
				t.Errorf("item_refresh undid the edit: %v", call(t, "item_get", map[string]any{"id": id}))
			}
			call(t, "library_scan", nil)
			if err := waitForScan(); err != nil {
				t.Fatal(err)
			}
			if !holds(edited) {
				t.Errorf("a library scan undid the edit: %v", call(t, "item_get", map[string]any{"id": id}))
			}

			if c.library == "Messy Movies" {
				// Jellyfin would clear the film rather than re-read its nfo
				if msg := callErr(t, "item_refresh", map[string]any{"id": id, "replace_all": true}); !strings.Contains(msg, "metadata fetchers off") {
					t.Errorf("replace_all without fetchers: %s", msg)
				}
				if !edited() {
					t.Errorf("the refused replace_all changed the film: %v", call(t, "item_get", map[string]any{"id": id}))
				}
				return
			}
			call(t, "item_refresh", map[string]any{"id": id, "replace_all": true})
			if !eventually(func() bool {
				got := call(t, "item_get", map[string]any{"id": id})
				return str(got["overview"]) != overview && str(got["overview"]) != "" && !slices.Contains(strs(t, got["tags"], "tags"), "zzyzx-kept")
			}) {
				t.Errorf("replace_all kept the edit: %v", call(t, "item_get", map[string]any{"id": id}))
			}
		})
	}
}

// A client calls tools in parallel: edits of one item, adds to one playlist
// or collection, removals, and a move racing an add all land, none undoing
// another.
func TestParallelWrites(t *testing.T) {
	var films []any
	for _, name := range []string{"Aliens", "Blade Runner", "Dune", "Arrival", "The Thirteenth Floor"} {
		films = append(films, findItem(t, "Movies", "Movie", name))
	}
	alien := findItem(t, "Movies", "Movie", "Alien")

	t.Run("edits of one item", func(t *testing.T) {
		id := findItem(t, "Messy Movies", "Movie", "Arrival")
		before := call(t, "item_get", map[string]any{"id": id})
		var tags []any
		for i := range 6 {
			tags = append(tags, fmt.Sprintf("zzyzx-parallel-%d", i))
		}
		t.Cleanup(func() {
			_, _ = invoke("item_batch_edit", map[string]any{"ids": []any{id}, "remove_tags": tags})
			updateItem(t, id, map[string]any{"Overview": before["overview"]})
		})

		var wg sync.WaitGroup
		for _, tag := range tags {
			wg.Go(func() {
				if _, err := invoke("item_batch_edit", map[string]any{"ids": []any{id}, "add_tags": []any{tag}}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Go(func() {
			if _, err := invoke("item_edit", map[string]any{"id": id, "overview": "Zzyzx: edited alongside."}); err != nil {
				t.Error(err)
			}
		})
		wg.Wait()

		got := call(t, "item_get", map[string]any{"id": id})
		for _, tag := range tags {
			if !slices.Contains(strs(t, got["tags"], "tags"), tag.(string)) {
				t.Errorf("tag %s was lost: the tags are %v", tag, got["tags"])
			}
		}
		if str(got["overview"]) != "Zzyzx: edited alongside." {
			t.Errorf("the overview edit was lost: %v", got["overview"])
		}
	})

	t.Run("a playlist", func(t *testing.T) {
		pl := str(call(t, "playlist_create", map[string]any{"name": "Zzyzx Parallel", "item_ids": []any{alien}, "media_type": "Video"})["id"])
		deleteLater(t, "playlist_delete", "playlist", pl)

		var wg sync.WaitGroup
		for _, id := range films {
			wg.Go(func() {
				if _, err := invoke("playlist_add", map[string]any{"playlist": pl, "item_ids": []any{id}}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		entries := playlistEntries(t, pl, 6)
		if len(entries) != 6 {
			t.Fatalf("after five adds at once the playlist holds %v", names(t, call(t, "playlist_get", map[string]any{"playlist": pl})["entries"], "entries"))
		}

		// move the last to the top while another film is added
		last := entries[5]
		extra := findItem(t, "Movies", "Movie", "Dune: Part Two")
		wg.Go(func() {
			if _, err := invoke("playlist_edit", map[string]any{"playlist": pl, "move_entry_id": str(last["entry_id"]), "position": 1}); err != nil {
				t.Errorf("the move: %v", err)
			}
		})
		wg.Go(func() {
			if _, err := invoke("playlist_add", map[string]any{"playlist": pl, "item_ids": []any{extra}}); err != nil {
				t.Errorf("the add: %v", err)
			}
		})
		wg.Wait()
		got := names(t, call(t, "playlist_get", map[string]any{"playlist": pl})["entries"], "entries")
		if len(got) != 7 || got[0] != str(last["name"]) || !slices.Contains(got, "Dune: Part Two") {
			t.Errorf("after a move and an add at once the playlist is %v, want %s first and Dune: Part Two in it", got, last["name"])
		}
	})

	t.Run("a collection", func(t *testing.T) {
		col := str(call(t, "collection_create", map[string]any{"name": "Zzyzx Parallel", "item_ids": []any{alien}})["id"])
		deleteLater(t, "collection_delete", "collection", col)

		var wg sync.WaitGroup
		for _, id := range films {
			wg.Go(func() {
				if _, err := invoke("collection_add", map[string]any{"collection": col, "item_ids": []any{id}}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if n := collectionSize(t, col, 6); n != 6 {
			t.Fatalf("after five adds at once the collection holds %d", n)
		}
		for _, id := range films[:3] {
			wg.Go(func() {
				if _, err := invoke("collection_remove", map[string]any{"collection": col, "item_ids": []any{id}}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if got := names(t, call(t, "collection_get", map[string]any{"collection": col})["items"], "items"); !slices.Equal(sorted(got), []string{"Alien", "Arrival", "The Thirteenth Floor"}) {
			t.Errorf("after three removals at once the collection holds %v", got)
		}
	})
}

// copyFixture copies a fixture folder under a new name, renaming the files
// named after it, so a scan picks up another copy of the film.
func copyFixture(t *testing.T, src, dst string) {
	t.Helper()

	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o777); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(src, e.Name())) //nolint:gosec // a fixture under the test data dir
		if err != nil {
			t.Fatal(err)
		}
		name := strings.ReplaceAll(e.Name(), filepath.Base(src), filepath.Base(dst))
		if err := os.WriteFile(filepath.Join(dst, name), raw, 0o666); err != nil { //nolint:gosec // same
			t.Fatal(err)
		}
	}
}

// Pruning a copy the duplicates audit found, and removing a whole library:
// the playlist, collection and favourites that held their items let go of
// them, and the audits and lookups count one fewer.
func TestDeletesLeaveNothingBehind(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	dune := findItem(t, "Movies", "Movie", "Dune")
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	alienCopies := func() int {
		return len(rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "348"})["items"], "items"))
	}
	alienGroup := func() int {
		groups, _ := call(t, "audit_duplicates", map[string]any{"library": "Messy Movies"})["duplicate_groups"].([]any)
		for _, g := range groups {
			if group := rowsOf(g); len(group) > 0 && str(group[0]["name"]) == "Alien" {
				return len(group)
			}
		}
		return 0
	}
	// the lists are named after the test holding them: Emby cannot recreate a
	// collection under the name of the first it created, once deleted
	held := func(name, id string) (playlist, collection, favourite bool) {
		for _, p := range rowsOf(call(t, "playlist_list", nil)["playlists"]) {
			if str(p["name"]) == name {
				for _, e := range rowsOf(call(t, "playlist_get", map[string]any{"playlist": str(p["id"])})["entries"]) {
					playlist = playlist || str(e["id"]) == id
				}
			}
		}
		for _, it := range rowsOf(call(t, "collection_get", map[string]any{"collection": name})["items"]) {
			collection = collection || str(it["id"]) == id
		}
		for _, it := range rowsOf(call(t, "user_favourites", map[string]any{"user": "alice"})["favourites"]) {
			favourite = favourite || str(it["id"]) == id
		}
		return playlist, collection, favourite
	}
	// favourite too, for a film with no other copy: Emby marks every copy and
	// its favourites list then shows only some of them
	hold := func(t *testing.T, name, id string, favourite bool) {
		t.Helper()
		pl := str(call(t, "playlist_create", map[string]any{"name": name, "item_ids": []any{id, dune}, "media_type": "Video"})["id"])
		deleteLater(t, "playlist_delete", "playlist", pl)
		col := str(call(t, "collection_create", map[string]any{"name": name, "item_ids": []any{id, arrival}})["id"])
		deleteLater(t, "collection_delete", "collection", col)
		if favourite {
			call(t, "item_set_favourite", map[string]any{"id": id, "user": "alice", "favourite": true})
		}
		if p, c, f := held(name, id); !p || !c || f != favourite {
			t.Fatalf("the item is not held: playlist %v collection %v favourite %v", p, c, f)
		}
	}

	t.Run("a duplicate pruned", func(t *testing.T) {
		have := movieCount(t, "Messy Movies")
		copies, group := alienCopies(), alienGroup()
		dst := filepath.Join(dataDir(), "messy-movies", "Alien (1979) Zzyzx Copy")
		copyFixture(t, filepath.Join(dataDir(), "messy-movies", messyAlien), dst)
		t.Cleanup(func() { _ = os.RemoveAll(dst) })
		call(t, "library_scan", nil)
		if err := waitForItems("Messy Movies", have+1); err != nil {
			t.Fatal(err)
		}
		if err := waitForScan(); err != nil {
			t.Fatal(err)
		}
		var id string
		for _, it := range rows(t, call(t, "library_search", map[string]any{"library": "Messy Movies", "query": "Alien", "limit": 50})["items"], "items") {
			if strings.Contains(str(it["path"]), "Zzyzx Copy") {
				id = str(it["id"])
			}
		}
		if id == "" {
			t.Fatal("the copy was not scanned in")
		}
		if alienCopies() != copies+1 || alienGroup() != group+1 {
			t.Fatalf("with the copy: %d copies in a group of %d, was %d in %d", alienCopies(), alienGroup(), copies, group)
		}
		hold(t, "Zzyzx Pruned", id, false)

		call(t, "item_delete", map[string]any{"id": id, "confirm": true})
		if err := waitForItems("Messy Movies", have); err != nil {
			t.Fatal(err)
		}
		if p, c, f := held("Zzyzx Pruned", id); p || c || f {
			t.Errorf("the deleted copy is still held: playlist %v collection %v favourite %v", p, c, f)
		}
		if alienCopies() != copies || alienGroup() != group {
			t.Errorf("after the delete: %d copies in a group of %d, want %d in %d", alienCopies(), alienGroup(), copies, group)
		}
		if msg := callErr(t, "item_get", map[string]any{"id": id}); !strings.Contains(msg, "no item") {
			t.Errorf("item_get of the deleted copy: %s", msg)
		}
	})

	t.Run("a library removed", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(dataDir(), "movies", "Arrival (2016)", "Arrival (2016).mp4")) //nolint:gosec // a fixture under the test data dir
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(dataDir(), "ripple", "Zzyzx Three (2003)")
		if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // the container reads it as another user
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Zzyzx Three (2003).mp4"), raw, 0o666); err != nil { //nolint:gosec // same
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(dataDir(), "ripple")) })
		t.Cleanup(func() {
			_, _ = invoke("library_delete", map[string]any{"library": "Ripple", "confirm": true})
			_ = waitForScan()
		})
		call(t, "library_create", map[string]any{"name": "Ripple", "type": "movies", "paths": []any{"/media/ripple"}, "scan": true})
		var id string
		if !eventually(func() bool {
			out, err := invoke("library_items", map[string]any{"library": "Ripple"})
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
		t.Cleanup(func() {
			_, _ = invoke("item_set_favourite", map[string]any{"id": id, "user": "alice", "favourite": false})
		})
		hold(t, "Zzyzx Removed", id, true)
		favourites := num(t, call(t, "user_get", map[string]any{"user": "alice"})["favourites"], "favourites")

		call(t, "library_delete", map[string]any{"library": "Ripple", "confirm": true})
		// Jellyfin lets go of the items on the library scan the removal starts
		if !eventually(func() bool {
			p, c, f := held("Zzyzx Removed", id)
			_, err := invoke("item_get", map[string]any{"id": id})
			return !p && !c && !f && err != nil
		}) {
			p, c, f := held("Zzyzx Removed", id)
			t.Errorf("the removed library's film is still held: playlist %v collection %v favourite %v", p, c, f)
		}
		if n := num(t, call(t, "user_get", map[string]any{"user": "alice"})["favourites"], "favourites"); n != favourites-1 {
			t.Errorf("alice has %d favourites, want %d", n, favourites-1)
		}
		if err := waitForScan(); err != nil {
			t.Fatal(err)
		}
	})
}

// auditCounts is audit_all's findings by audit.
func auditCounts(t *testing.T, args map[string]any) map[string]int {
	t.Helper()

	counts := map[string]int{}
	for _, row := range rows(t, call(t, "audit_all", args)["audits"], "audits") {
		counts[str(row["audit"])] = num(t, row["findings"], "findings")
	}

	return counts
}

// The fixes the audits point to, where they can work and where they cannot.
// With the fetchers on, item_refresh fills a missing overview and
// item_identify re-matches a wrong edition, which also clears the duplicate
// the wrong match made. With them off, the tools say what they could not do
// rather than claiming it.
func TestAuditFixesWhereTheyPoint(t *testing.T) {
	t.Run("a refresh fills a missing overview", func(t *testing.T) {
		arrival := findItem(t, "Movies", "Movie", "Arrival")
		before := fullItem(t, arrival)
		t.Cleanup(func() { updateItem(t, arrival, map[string]any{"Overview": before["Overview"]}) })
		updateItem(t, arrival, map[string]any{"Overview": ""})
		if got := findings(t, call(t, "audit_missing_overview", map[string]any{"library": "Movies"})); !slices.Equal(got, []string{"Arrival"}) {
			t.Fatalf("audit_missing_overview = %v, want [Arrival]", got)
		}
		call(t, "item_refresh", map[string]any{"id": arrival})
		if !eventually(func() bool {
			return num(t, call(t, "audit_missing_overview", map[string]any{"library": "Movies"})["total_findings"], "total_findings") == 0
		}) {
			t.Errorf("after item_refresh Arrival still has no overview: %v", call(t, "item_get", map[string]any{"id": arrival}))
		}
	})

	t.Run("identify re-matches a wrong edition", func(t *testing.T) {
		dune := findItem(t, "Movies", "Movie", "Dune")
		before := fullItem(t, dune)
		t.Cleanup(func() {
			updateItem(t, dune, map[string]any{"ProviderIds": before["ProviderIds"], "ProductionYear": before["ProductionYear"], "Overview": before["Overview"], "PremiereDate": before["PremiereDate"]})
		})
		// matched to David Lynch's film, which the messy Dune's nfo names too
		updateItem(t, dune, map[string]any{"ProviderIds": map[string]any{"Tmdb": "841", "Imdb": "tt0087182"}, "ProductionYear": 1984})
		counts := auditCounts(t, nil)
		if got := findings(t, call(t, "audit_year_mismatch", map[string]any{"library": "Movies"})); !slices.Equal(got, []string{"Dune"}) {
			t.Fatalf("audit_year_mismatch = %v, want [Dune]", got)
		}
		if n := len(rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "841"})["items"], "items")); n != 2 {
			t.Fatalf("tmdb 841 matches %d films, want the clean and the messy Dune", n)
		}

		idx := -1
		for i, c := range rows(t, call(t, "item_identify", map[string]any{"id": dune, "kind": "movie", "year": 2021})["candidates"], "candidates") {
			if ids, _ := c["metadata_provider_ids"].(map[string]any); idx < 0 && str(ids["tmdb"]) == "438631" {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatal("the 2021 Dune is not a candidate")
		}
		applied := call(t, "item_identify_apply", map[string]any{"id": dune, "kind": "movie", "candidate": idx, "year": 2021})
		ids, _ := applied["metadata_provider_ids"].(map[string]any)
		if str(ids["tmdb"]) != "438631" || num(t, applied["year"], "year") != 2021 || str(applied["note"]) != "" {
			t.Errorf("item_identify_apply = %v", applied)
		}

		if got := findings(t, call(t, "audit_year_mismatch", map[string]any{"library": "Movies"})); len(got) != 0 {
			t.Errorf("after the re-match audit_year_mismatch = %v", got)
		}
		if n := len(rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "841"})["items"], "items")); n != 1 {
			t.Errorf("after the re-match tmdb 841 matches %d films, want the messy Dune alone", n)
		}
		after := auditCounts(t, nil)
		for _, audit := range []string{"audit_year_mismatch", "audit_duplicates"} {
			if after[audit] != counts[audit]-1 {
				t.Errorf("%s counted %d before the re-match and %d after, want one fewer", audit, counts[audit], after[audit])
			}
		}
	})

	t.Run("with the fetchers off", func(t *testing.T) {
		dune := findItem(t, "Messy Movies", "Movie", "Dune")
		before := fullItem(t, dune)
		t.Cleanup(func() { updateItem(t, dune, before) })

		idx := -1
		for i, c := range rows(t, call(t, "item_identify", map[string]any{"id": dune, "kind": "movie", "year": 2021})["candidates"], "candidates") {
			if ids, _ := c["metadata_provider_ids"].(map[string]any); idx < 0 && str(ids["tmdb"]) == "438631" {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatal("the 2021 Dune is not a candidate")
		}
		args := map[string]any{"id": dune, "kind": "movie", "candidate": idx, "year": 2021}
		if isJellyfin() {
			// the ids change and nothing is fetched, which the answer says
			out := call(t, "item_identify_apply", args)
			if ids, _ := out["metadata_provider_ids"].(map[string]any); str(ids["tmdb"]) != "438631" || !strings.Contains(str(out["note"]), "metadata fetchers off") {
				t.Errorf("item_identify_apply = %v", out)
			}
		} else if msg := callErr(t, "item_identify_apply", args); !strings.Contains(msg, "tmdb id is 841, not 438631") || !strings.Contains(msg, "nfo") {
			// the nfo names Lynch's film, and Emby keeps what it says
			t.Errorf("item_identify_apply over an nfo: %s", msg)
		}
		if msg := callErr(t, "item_refresh", map[string]any{"id": dune, "replace_all": true}); !strings.Contains(msg, "metadata fetchers off") {
			t.Errorf("replace_all without fetchers: %s", msg)
		}
	})
}

// A genre put on a franchise shows at once in every tool that lists or
// filters by genre, follows a rename, and leaves them all once no item
// carries it.
func TestGenresFollowEdits(t *testing.T) {
	ids := []any{findItem(t, "Movies", "Movie", "Alien"), findItem(t, "Movies", "Movie", "Aliens")}
	t.Cleanup(func() {
		_, _ = invoke("item_batch_edit", map[string]any{"ids": ids, "remove_genres": []any{"Zzyzx Franchise", "Zzyzx Saga"}})
	})
	carried := func(genre string, want ...string) {
		t.Helper()
		if n := valueCounts(t, call(t, "library_filters", map[string]any{"library": "Movies"})["genres"], "genres")[genre]; n != len(want) {
			t.Errorf("library_filters counts %s on %d films, want %d", genre, n, len(want))
		}
		if got := names(t, call(t, "library_items", map[string]any{"library": "Movies", "genres": []any{genre}})["items"], "items"); !slices.Equal(sorted(got), want) {
			t.Errorf("library_items genre %s = %v, want %v", genre, got, want)
		}
		if got := names(t, call(t, "library_search", map[string]any{"query": "Alien", "genre": genre})["items"], "items"); !slices.Equal(sorted(got), want) {
			t.Errorf("library_search genre %s = %v, want %v", genre, got, want)
		}
		for _, args := range []map[string]any{{"library": "Movies"}, nil} {
			if listed := slices.Contains(strs(t, call(t, "library_genres", args)["genres"], "genres"), genre); listed != (len(want) > 0) {
				t.Errorf("library_genres %v lists %s: %v, want %v", args, genre, listed, len(want) > 0)
			}
		}
	}

	call(t, "item_batch_edit", map[string]any{"ids": ids, "add_genres": []any{"Zzyzx Franchise"}})
	carried("Zzyzx Franchise", "Alien", "Aliens")

	call(t, "metadata_rename", map[string]any{"field": "genres", "from": "Zzyzx Franchise", "to": "Zzyzx Saga", "library": "Movies"})
	carried("Zzyzx Franchise")
	carried("Zzyzx Saga", "Alien", "Aliens")

	call(t, "metadata_rename", map[string]any{"field": "genres", "from": "Zzyzx Saga", "remove": true, "library": "Movies"})
	carried("Zzyzx Saga")
}

// Copies of one film share its provider ids, and Emby keeps watch state and
// favourites by those ids: marking one copy there marks them all, where
// Jellyfin marks the one. Either way the counts are by film: one film
// watched, one favourite, and no copy of it left unwatched.
func TestCopiesCountOnce(t *testing.T) {
	alien := findItem(t, "Movies", "Movie", "Alien")
	copies := rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "348"})["items"], "items")
	if len(copies) != 3 {
		t.Fatalf("tmdb 348 has %d copies, want 3", len(copies))
	}
	t.Cleanup(func() {
		for _, c := range copies {
			_, _ = invoke("item_set_watched", map[string]any{"id": str(c["id"]), "user": "alice", "watched": false})
			_, _ = invoke("item_set_favourite", map[string]any{"id": str(c["id"]), "user": "alice", "favourite": false})
		}
	})
	unwatchedAliens := func(library string) int {
		return len(slices.DeleteFunc(findings(t, call(t, "audit_unwatched", map[string]any{"library": library})), func(n string) bool { return n != "Alien" }))
	}
	if n := unwatchedAliens("Messy Movies"); n != 2 {
		t.Fatalf("audit_unwatched lists %d messy Aliens before, want 2", n)
	}
	user := call(t, "user_get", map[string]any{"user": "alice"})
	stats := call(t, "user_stats", map[string]any{"user": "alice"})

	call(t, "item_set_watched", map[string]any{"id": alien, "user": "alice", "watched": true})
	call(t, "item_set_favourite", map[string]any{"id": alien, "user": "alice", "favourite": true})

	for _, c := range copies {
		played := false
		for _, u := range rows(t, call(t, "item_last_watched", map[string]any{"id": str(c["id"])})["users"], "users") {
			played = played || (str(u["user"]) == "alice" && boolOf(u["played"]))
		}
		if want := !isJellyfin() || str(c["id"]) == alien; played != want {
			t.Errorf("the copy at %s is played for alice: %v, want %v", c["path"], played, want)
		}
	}
	after := call(t, "user_get", map[string]any{"user": "alice"})
	for _, field := range []string{"movies_watched", "favourites"} {
		if num(t, after[field], field) != num(t, user[field], field)+1 {
			t.Errorf("alice's %s went from %v to %v, want one more", field, user[field], after[field])
		}
	}
	if got := call(t, "user_stats", map[string]any{"user": "alice"}); num(t, got["movies_watched"], "movies_watched") != num(t, stats["movies_watched"], "movies_watched")+1 {
		t.Errorf("user_stats movies_watched went from %v to %v, want one more", stats["movies_watched"], got["movies_watched"])
	}
	for _, library := range []string{"Movies", "Messy Movies"} {
		if n := unwatchedAliens(library); n != 0 {
			t.Errorf("audit_unwatched still lists %d Aliens in %s", n, library)
		}
	}
}

// playThrough reports a play of an item from its start to past its end, the
// way a client does, as the player the token signs in.
func playThrough(t *testing.T, token, id string) {
	t.Helper()

	status, raw := api(t, http.MethodPost, "/Items/"+id+"/PlaybackInfo?UserId="+os.Getenv("EMBYFIN_TEST_USER_ID"), token, map[string]any{})
	var info struct {
		PlaySessionID string `json:"PlaySessionId"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &info) != nil {
		t.Fatalf("PlaybackInfo: HTTP %d: %.200s", status, raw)
	}
	for _, step := range []struct {
		path  string
		ticks int64
	}{{"/Sessions/Playing", 0}, {"/Sessions/Playing/Progress", 5_000_000}, {"/Sessions/Playing/Stopped", 5_000_000}} {
		body := map[string]any{"ItemId": id, "PlaySessionId": info.PlaySessionID, "PositionTicks": step.ticks, "CanSeek": true, "PlayMethod": "DirectPlay"}
		if status, raw := api(t, http.MethodPost, step.path, token, body); status/100 != 2 {
			t.Fatalf("%s: HTTP %d: %s", step.path, status, raw)
		}
	}
}

// Watching a series through: next up moves along an episode at a time, an
// episode started is in progress until it is played, and the last one
// finishes the series in the stats and takes it off next up.
func TestFinishingASeries(t *testing.T) {
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	byNumber := map[int]string{}
	for _, e := range rows(t, call(t, "show_episodes", map[string]any{"series_id": series})["episodes"], "episodes") {
		byNumber[num(t, e["episode"], "episode")] = str(e["id"])
	}
	e1, e2, e3 := byNumber[1], byNumber[2], byNumber[3]
	t.Cleanup(func() {
		for _, id := range []string{e1, e2, e3} {
			_, _ = invoke("item_set_watched", map[string]any{"id": id, "user": "alice", "watched": false})
		}
	})
	ids := func(tool, field string) []string {
		var out []string
		for _, it := range rows(t, call(t, tool, map[string]any{"user": "alice"})[field], field) {
			out = append(out, str(it["id"]))
		}
		return out
	}
	breakingBad := func() map[string]any {
		for _, s := range rows(t, call(t, "user_stats", map[string]any{"user": "alice"})["top_series"], "top_series") {
			if str(s["name"]) == "Breaking Bad" {
				return s
			}
		}
		return nil
	}

	call(t, "item_set_watched", map[string]any{"id": e1, "user": "alice", "watched": true})
	if next := ids("user_next_up", "next_up"); !slices.Contains(next, e2) {
		t.Errorf("after the pilot next up is %v, want episode two", next)
	}
	// Emby lists the next episode as resumable at zero; nothing is in progress
	if in := ids("user_in_progress", "items"); len(in) != 0 {
		t.Errorf("after marking the pilot watched alice has %v in progress", in)
	}
	if bb := breakingBad(); bb == nil || num(t, bb["episodes_watched"], "episodes_watched") != 1 || boolOf(bb["finished"]) {
		t.Errorf("Breaking Bad in the stats = %v", bb)
	}
	if got := findings(t, call(t, "audit_unwatched", map[string]any{"library": "Shows", "types": "Series"})); slices.Contains(got, "Breaking Bad") {
		t.Errorf("audit_unwatched lists Breaking Bad after an episode was watched: %v", got)
	}

	call(t, "item_set_progress", map[string]any{"id": e2, "user": "alice", "position_minutes": 0.01})
	if in := ids("user_in_progress", "items"); !slices.Equal(in, []string{e2}) {
		t.Errorf("with episode two started alice has %v in progress", in)
	}

	_, token := signInPlayer(t)
	playThrough(t, token, e2)
	if !eventually(func() bool {
		return slices.Contains(ids("user_next_up", "next_up"), e3) && len(ids("user_in_progress", "items")) == 0
	}) {
		t.Errorf("after episode two was played next up is %v and in progress %v", ids("user_next_up", "next_up"), ids("user_in_progress", "items"))
	}

	call(t, "item_set_watched", map[string]any{"id": e3, "user": "alice", "watched": true})
	if next := ids("user_next_up", "next_up"); slices.ContainsFunc(next, func(id string) bool { return id == e1 || id == e2 || id == e3 }) {
		t.Errorf("after the last episode next up is %v", next)
	}
	if bb := breakingBad(); bb == nil || num(t, bb["episodes_watched"], "episodes_watched") != 3 || !boolOf(bb["finished"]) {
		t.Errorf("Breaking Bad in the stats after the last episode = %v", bb)
	}
	if n := num(t, call(t, "user_stats", map[string]any{"user": "alice"})["series_finished"], "series_finished"); n != 1 {
		t.Errorf("alice has finished %d series, want 1", n)
	}
}

// A user who may see only Movies, across the tools that act in their name or
// report their watching: what they cannot see is not listed, not counted,
// and not changed.
func TestRestrictedUserAcrossTools(t *testing.T) {
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	eps := rows(t, call(t, "show_episodes", map[string]any{"series_id": series})["episodes"], "episodes")
	pilot, second := str(eps[0]["id"]), str(eps[1]["id"])
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	t.Cleanup(func() {
		for _, id := range []string{pilot, second, arrival} {
			_, _ = invoke("item_set_watched", map[string]any{"id": id, "user": "alice", "watched": false})
			_, _ = invoke("item_set_favourite", map[string]any{"id": id, "user": "alice", "favourite": false})
		}
	})
	// watched and favourited while alice could see everything
	call(t, "item_set_watched", map[string]any{"id": pilot, "user": "alice", "watched": true})
	call(t, "item_set_favourite", map[string]any{"id": pilot, "user": "alice", "favourite": true})
	call(t, "item_set_progress", map[string]any{"id": arrival, "user": "alice", "position_minutes": 0.01})

	restrictAlice(t, "Movies")

	next := call(t, "user_next_up", map[string]any{"user": "alice"})
	if n := len(rows(t, next["next_up"], "next_up")); n != 0 {
		t.Errorf("alice has %d episodes next up in a library she cannot see", n)
	}
	for tool, field := range map[string]string{"user_in_progress": "items", "user_next_up": "resume"} {
		if got := names(t, call(t, tool, map[string]any{"user": "alice"})[field], field); !slices.Equal(got, []string{"Arrival"}) {
			t.Errorf("%s %s = %v, want [Arrival]", tool, field, got)
		}
	}
	if got := names(t, call(t, "user_favourites", map[string]any{"user": "alice"})["favourites"], "favourites"); len(got) != 0 {
		t.Errorf("alice's favourites = %v, want none she can see", got)
	}
	if got := call(t, "user_get", map[string]any{"user": "alice"}); num(t, got["episodes_watched"], "episodes_watched") != 0 || num(t, got["favourites"], "favourites") != 0 {
		t.Errorf("user_get alice counts what she cannot see: %v", got)
	}
	for _, u := range rows(t, call(t, "item_last_watched", map[string]any{"id": pilot})["users"], "users") {
		if str(u["user"]) == "alice" {
			t.Errorf("item_last_watched reports alice on an episode she cannot see: %v", u)
		}
	}
	for tool, args := range map[string]map[string]any{
		"item_set_watched":   {"id": second, "user": "alice", "watched": true},
		"item_set_favourite": {"id": second, "user": "alice", "favourite": true},
		"item_set_progress":  {"id": second, "user": "alice", "position_minutes": 0.01},
	} {
		if msg := callErr(t, tool, args); !strings.Contains(msg, "alice cannot see") {
			t.Errorf("%s on an episode alice cannot see: %s", tool, msg)
		}
	}
	// alice's watch of the pilot is not hers to count while she cannot see it
	if got := findings(t, call(t, "audit_unwatched", map[string]any{"library": "Shows", "types": "Series"})); !slices.Contains(got, "Breaking Bad") {
		t.Errorf("audit_unwatched = %v, want Breaking Bad among them", got)
	}
}
