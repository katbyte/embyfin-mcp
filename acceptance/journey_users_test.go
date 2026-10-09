//go:build integration

package acceptance

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"
)

// Journeys in another user's name: a restricted user's own playlists, what a
// playlist holds when an album or a season is added to it, and a parental
// rating that hides a film from someone who had already watched it.

// playlistNames reads a playlist's entries by name in one user's view (root's
// when user is empty).
func playlistNames(t *testing.T, playlist, user string) []string {
	t.Helper()

	args := map[string]any{"playlist": playlist}
	if user != "" {
		args["user"] = user
	}

	return names(t, suite.Call(t, "playlist_get", args)["entries"], "entries")
}

// A user who may see films and music keeps playlists of her own: made,
// added to, reordered, renamed and trimmed in her name, each change visible
// to her and to root. What she cannot see her playlists cannot take.
func TestARestrictedUsersPlaylists(t *testing.T) {
	dune := findItem(t, "Movies", "Movie", "Dune")
	dune2 := findItem(t, "Movies", "Movie", "Dune: Part Two")
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	pilot := acc.Str(acc.Rows(t, suite.Call(t, "library_episodes", map[string]any{"series_id": series, "season": 1})["episodes"], "episodes")[0]["id"])
	restrictAlice(t, "Movies", "Music")

	pl := acc.Str(suite.Call(t, "playlist_create", map[string]any{"name": "Zzyzx Alice Films", "user": "alice", "item_ids": []any{dune}, "media_type": "Video"})["id"])
	deleteLater(t, "playlist_delete", "playlist", pl)
	listed := false
	for _, p := range acc.Rows(t, suite.Call(t, "playlist_list", nil)["playlists"], "playlists") {
		listed = listed || acc.Str(p["id"]) == pl
	}
	if !listed {
		t.Error("playlist_list does not list alice's playlist")
	}
	for _, user := range []string{"alice", ""} {
		if got := playlistNames(t, pl, user); !slices.Equal(got, []string{"Dune"}) {
			t.Errorf("alice's new playlist in %q's view = %v, want [Dune]", user, got)
		}
	}

	if out := suite.Call(t, "playlist_edit", map[string]any{"playlist": pl, "user": "alice", "add_items": []any{dune2, arrival}}); acc.Num(t, out["added"], "added") != 2 {
		t.Errorf("playlist_edit add_items as alice = %v", out)
	}
	var entry string
	for _, e := range acc.Rows(t, suite.Call(t, "playlist_get", map[string]any{"playlist": pl, "user": "alice"})["entries"], "entries") {
		if acc.Str(e["name"]) == "Arrival" {
			entry = acc.Str(e["entry_id"])
		}
	}
	edit := suite.Call(t, "playlist_edit", map[string]any{"playlist": pl, "user": "alice", "move_entry_id": entry, "move_item_id": arrival, "position": 1, "name": "Zzyzx Alice Renamed"})
	if got := names(t, edit["entries"], "entries"); acc.Str(edit["name"]) != "Zzyzx Alice Renamed" || !slices.Equal(got, []string{"Arrival", "Dune", "Dune: Part Two"}) {
		t.Errorf("playlist_edit as alice = %v, entries %v", edit["name"], got)
	}
	for _, e := range acc.Rows(t, suite.Call(t, "playlist_get", map[string]any{"playlist": pl, "user": "alice"})["entries"], "entries") {
		if acc.Str(e["name"]) == "Dune: Part Two" {
			entry = acc.Str(e["entry_id"])
		}
	}
	if out := suite.Call(t, "playlist_edit", map[string]any{"playlist": pl, "user": "alice", "remove_entries": []any{map[string]any{"entry_id": entry, "item_id": dune2}}}); acc.Num(t, out["removed"], "removed") != 1 {
		t.Errorf("playlist_edit remove_entries as alice = %v", out)
	}
	for _, user := range []string{"alice", ""} {
		if got := playlistNames(t, pl, user); !slices.Equal(got, []string{"Arrival", "Dune"}) {
			t.Errorf("after alice's edits, %q's view = %v, want [Arrival Dune]", user, got)
		}
	}

	// an episode from Shows, which alice cannot see: a playlist of hers takes
	// only what she can see, on both servers (left to themselves, Emby kept
	// it where only root saw it and Jellyfin showed it to her), and nothing
	// is added to it
	if msg := suite.CallErr(t, "playlist_edit", map[string]any{"playlist": pl, "user": "alice", "add_items": []any{pilot}}); !strings.Contains(msg, "alice cannot see Pilot ("+pilot+")") || !strings.Contains(msg, "takes only what they can see") {
		t.Errorf("adding what alice cannot see: %s", msg)
	}
	for _, user := range []string{"alice", ""} {
		if got := playlistNames(t, pl, user); !slices.Equal(got, []string{"Arrival", "Dune"}) {
			t.Errorf("after the refused add, %q's view = %v, want [Arrival Dune]", user, got)
		}
	}
	// and a playlist of hers cannot be made with it either
	if msg := suite.CallErr(t, "playlist_create", map[string]any{"name": "Zzyzx Alice Hidden", "user": "alice", "item_ids": []any{pilot}, "media_type": "Video"}); !strings.Contains(msg, "alice cannot see Pilot") {
		t.Errorf("creating alice's playlist with what she cannot see: %s", msg)
	}
	for _, p := range acc.Rows(t, suite.Call(t, "playlist_list", nil)["playlists"], "playlists") {
		if acc.Str(p["name"]) == "Zzyzx Alice Hidden" {
			t.Errorf("the refused playlist was made: %v", p)
			deleteLater(t, "playlist_delete", "playlist", acc.Str(p["id"]))
		}
	}
}

// Audio playlists hold tracks, and an album or a season added to a playlist
// adds what it holds, each once.
func TestPlaylistsOfTracksAndSeasons(t *testing.T) {
	belgrade := findItem(t, "Music", "Audio", "Belgrade")
	valkyrie := findItem(t, "Music", "Audio", "Valkyrie")
	polygon := findItem(t, "Music", "MusicAlbum", "Polygon")

	pl := acc.Str(suite.Call(t, "playlist_create", map[string]any{"name": "Zzyzx Tracks", "user": "alice", "item_ids": []any{belgrade, valkyrie}, "media_type": "Audio"})["id"])
	deleteLater(t, "playlist_delete", "playlist", pl)
	got := suite.Call(t, "playlist_get", map[string]any{"playlist": pl, "user": "alice"})["entries"]
	if n := names(t, got, "entries"); !slices.Equal(n, []string{"Belgrade", "Valkyrie"}) {
		t.Errorf("the audio playlist = %v", n)
	}
	for _, e := range acc.Rows(t, got, "entries") {
		if acc.Str(e["type"]) != "Audio" {
			t.Errorf("an audio playlist holds a %v", e["type"])
		}
	}

	// the album is four tracks, two of them already there: the playlist
	// holds each of the album's once more
	out := suite.Call(t, "playlist_edit", map[string]any{"playlist": pl, "user": "alice", "add_items": []any{polygon}})
	want := []string{"Belgrade", "Valkyrie", "Belgrade", "Valkyrie", "Solid Gold", "Private Dancer"}
	if acc.Num(t, out["added"], "added") != 4 {
		t.Errorf("adding the album = %v, want its 4 tracks added", out)
	}
	if got := playlistNames(t, pl, "alice"); !slices.Equal(got, want) {
		t.Errorf("after adding the album the playlist = %v, want %v", got, want)
	}

	// a season is its episodes, in order
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	var season string
	for _, s := range acc.Rows(t, suite.Call(t, "show_seasons", map[string]any{"series_id": series})["seasons"], "seasons") {
		if acc.Num(t, s["season"], "season") == 1 {
			season = acc.Str(s["id"])
		}
	}
	video := acc.Str(suite.Call(t, "playlist_create", map[string]any{"name": "Zzyzx Season", "media_type": "Video"})["id"])
	deleteLater(t, "playlist_delete", "playlist", video)
	if out := suite.Call(t, "playlist_edit", map[string]any{"playlist": video, "add_items": []any{season}}); acc.Num(t, out["added"], "added") != 3 {
		t.Errorf("adding the season = %v, want its 3 episodes", out)
	}
	if got := playlistNames(t, video, ""); !slices.Equal(got, []string{"Pilot", "Cat's in the Bag...", "Cat's in the Bag..."}) {
		t.Errorf("the season's playlist = %v", got)
	}
}

// ratingValue is the number a server ranks a parental rating by, which is
// what a user's limit is set in: the servers number their scales differently.
func ratingValue(t *testing.T, name string) int {
	t.Helper()

	status, raw := api(t, http.MethodGet, "/Localization/ParentalRatings", "", nil)
	var ratings []struct {
		Name  string
		Value *int
	}
	if status != http.StatusOK || json.Unmarshal(raw, &ratings) != nil {
		t.Fatalf("reading the parental ratings: HTTP %d: %.200s", status, raw)
	}
	for _, r := range ratings {
		if r.Name == name && r.Value != nil {
			return *r.Value
		}
	}
	t.Fatalf("the server has no rating %s", name)

	return 0
}

// limitAlice lets alice watch nothing rated above the named rating until the
// test ends. No tool sets a user's policy, so it goes to the HTTP API.
func limitAlice(t *testing.T, rating string) int {
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
			t.Errorf("lifting alice's limit: HTTP %d: %s", status, raw)
		}
	})

	value := ratingValue(t, rating)
	user.Policy["MaxParentalRating"] = value
	if status, raw := api(t, http.MethodPost, "/Users/"+alice+"/Policy", "", user.Policy); status/100 != 2 {
		t.Fatalf("limiting alice: HTTP %d: %s", status, raw)
	}

	return value
}

// A film rated above what a user may watch is gone from everything in her
// name: user_get says where her limit is, library_items leaves the film out,
// item_set_state will not touch it, a playlist of hers will not take it,
// user_stats no longer counts her having watched it and item_last_watched no
// longer names her. Dune: Part Two, because it has no copy elsewhere for
// Emby's by-title watch state to reach.
func TestAParentalRatingLimit(t *testing.T) {
	film := findItem(t, "Movies", "Movie", "Dune: Part Two")
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	rating := acc.Str(suite.Call(t, "item_get", map[string]any{"id": film})["official_rating"])
	// put back through the HTTP API: item_edit takes an empty rating for no
	// edit at all, so a film that had none would have stayed R
	t.Cleanup(func() {
		updateItem(t, film, map[string]any{"OfficialRating": rating})
		if out, err := suite.Invoke("item_get", map[string]any{"id": film}); err != nil || acc.Str(out["official_rating"]) != rating {
			t.Errorf("the film's rating was not put back to %q: %v %v", rating, out["official_rating"], err)
		}
	})
	if rating == "R" {
		t.Fatal("the film is already rated R")
	}
	if got := suite.Call(t, "user_get", map[string]any{"user": "alice"}); got["max_parental_rating"] != nil {
		t.Fatalf("alice is limited before the test limits her: %v", got)
	}

	// watched while nothing stopped her; cleaned up after the limit is lifted
	suite.Call(t, "item_set_state", map[string]any{"id": film, "user": "alice", "watched": true})
	suite.PutBack(t, "item_set_state", map[string]any{"id": film, "user": "alice", "watched": false})
	watched := acc.Num(t, suite.Call(t, "user_stats", map[string]any{"user": "alice"})["movies_watched"], "movies_watched")
	aliceWatched := func() (listed, played bool) {
		for _, u := range acc.Rows(t, suite.Call(t, "item_last_watched", map[string]any{"id": film})["users"], "users") {
			if acc.Str(u["user"]) == "alice" {
				return true, acc.BoolOf(u["played"])
			}
		}
		return false, false
	}
	if listed, played := aliceWatched(); !listed || !played {
		t.Fatalf("before the limit item_last_watched has alice listed %v, played %v", listed, played)
	}

	if out := suite.Call(t, "item_edit", map[string]any{"ids": []any{film}, "official_rating": "R"}); !slices.Equal(acc.Strs(t, out["changed"], "changed"), []string{"OfficialRating"}) {
		t.Errorf("item_edit official_rating = %v", out)
	}
	// what the limit hides is every film rated above it, by the number the
	// server ranks each rating by; a film with no rating is not hidden
	limit := ratingValue(t, "PG-13")
	all := acc.Num(t, suite.Call(t, "library_items", map[string]any{"library": "Movies", "limit": 50})["total"], "total")
	hidden := 0
	for _, r := range acc.Rows(t, suite.Call(t, "library_filters", map[string]any{"library": "Movies"})["official_ratings"], "official_ratings") {
		if ratingValue(t, acc.Str(r["value"])) > limit {
			hidden += acc.Num(t, r["items"], "items")
		}
	}
	if hidden == 0 || hidden >= all {
		t.Fatalf("%d of the %d films are rated above PG-13, want some and not all", hidden, all)
	}
	if got := limitAlice(t, "PG-13"); got != limit {
		t.Fatalf("alice was limited at %d, PG-13 is %d", got, limit)
	}

	if got := suite.Call(t, "user_get", map[string]any{"user": "alice"}); acc.Num(t, got["max_parental_rating"], "max_parental_rating") != limit {
		t.Errorf("user_get alice max_parental_rating = %v, want %d (PG-13 on this server)", got["max_parental_rating"], limit)
	}
	films := suite.Call(t, "library_items", map[string]any{"library": "Movies", "user": "alice", "limit": 50})
	if got := names(t, films["items"], "items"); slices.Contains(got, "Dune: Part Two") || acc.Num(t, films["total"], "total") != all-hidden {
		t.Errorf("alice sees %v, want the %d films not rated above PG-13, and not Dune: Part Two", got, all-hidden)
	}
	if msg := suite.CallErr(t, "item_set_state", map[string]any{"id": film, "user": "alice", "favourite": true}); !strings.Contains(msg, "rated above what they may watch") {
		t.Errorf("item_set_state on a film above alice's limit: %s", msg)
	}
	// a playlist of hers takes what she may watch and refuses the rest
	pl := acc.Str(suite.Call(t, "playlist_create", map[string]any{"name": "Zzyzx Alice Limited", "user": "alice", "item_ids": []any{arrival}, "media_type": "Video"})["id"])
	deleteLater(t, "playlist_delete", "playlist", pl)
	if msg := suite.CallErr(t, "playlist_edit", map[string]any{"playlist": pl, "user": "alice", "add_items": []any{film}}); !strings.Contains(msg, "alice cannot see Dune: Part Two") {
		t.Errorf("adding a film above alice's limit to her playlist: %s", msg)
	}
	if got := playlistNames(t, pl, ""); !slices.Equal(got, []string{"Arrival"}) {
		t.Errorf("after the refused add alice's playlist holds %v, want [Arrival]", got)
	}
	if n := acc.Num(t, suite.Call(t, "user_stats", map[string]any{"user": "alice"})["movies_watched"], "movies_watched"); n != watched-1 {
		t.Errorf("alice's movies_watched = %d under the limit, %d before it: the hidden film still counts", n, watched)
	}
	if listed, played := aliceWatched(); listed {
		t.Errorf("item_last_watched reports alice (played %v) on a film above her limit", played)
	}

	// a collection of Arrival and the hidden film, cleared in her name: Emby
	// leaves the hidden film watched, and reads it before and after to say
	// so; Jellyfin clears it, which it will not answer in her name, and the
	// answer says the mark reaches what her view leaves out (both seen on
	// the servers: Emby 4.10, Jellyfin 12.1). Neither says the mark reached
	// only what she sees, nor all that is stored
	coll := acc.Str(suite.Call(t, "collection_create", map[string]any{"name": "Zzyzx Alice Limited Films", "item_ids": []any{arrival, film}})["id"])
	deleteLater(t, "collection_delete", "collection", coll)
	cleared := suite.Call(t, "item_set_state", map[string]any{"id": coll, "user": "alice", "watched": false})
	note := acc.Str(cleared["note"])
	var changed []string
	for _, c := range acc.RowsOf(cleared["copies_changed"]) {
		changed = append(changed, acc.Str(c["id"]))
	}
	if strings.Contains(note, "reaches all") || strings.Contains(note, "is not marked") || slices.Contains(changed, film) {
		t.Errorf("clearing a collection for alice, one of its two films hidden from her: note %q, copies_changed %v", note, changed)
	}
	if isJellyfin() && !strings.Contains(note, "the server stores 2 items under Zzyzx Alice Limited Films, and alice's view shows 1: Jellyfin's mark in their name can reach items their view leaves out") ||
		!isJellyfin() && !strings.Contains(note, "the server stores 2 items under Zzyzx Alice Limited Films, and alice's view shows 1 rows: the 1 items it leaves out") {
		t.Errorf("clearing a collection for alice on %s: note %q", backend, note)
	}
	setPolicy(t, os.Getenv("EMBYFIN_TEST_USER_ID"), func(p map[string]any) { p["MaxParentalRating"] = nil })
	if watched, _ := stateOf(t, film, "alice"); watched == isJellyfin() {
		t.Errorf("after the collection was cleared for alice on %s, Dune: Part Two, hidden from her, is watched %v", backend, watched)
	}
}
