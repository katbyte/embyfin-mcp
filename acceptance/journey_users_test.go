//go:build integration

package acceptance

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
)

// Journeys in another user's name: a restricted user's own playlists, and a
// parental rating that hides a film from someone who had already watched it.

// playlistNames reads a playlist's entries by name in one user's view (root's
// when user is empty).
func playlistNames(t *testing.T, playlist, user string) []string {
	t.Helper()

	args := map[string]any{"playlist": playlist}
	if user != "" {
		args["user"] = user
	}

	return names(t, call(t, "playlist_get", args)["entries"], "entries")
}

// A user who may see films and music keeps playlists of her own: made,
// added to, reordered, renamed and trimmed in her name, each change visible
// to her and to root. What she cannot see her playlists cannot take.
func TestARestrictedUsersPlaylists(t *testing.T) {
	dune := findItem(t, "Movies", "Movie", "Dune")
	dune2 := findItem(t, "Movies", "Movie", "Dune: Part Two")
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	pilot := str(rows(t, call(t, "library_episodes", map[string]any{"series_id": series, "season": 1})["episodes"], "episodes")[0]["id"])
	restrictAlice(t, "Movies", "Music")

	pl := str(call(t, "playlist_create", map[string]any{"name": "Zzyzx Alice Films", "user": "alice", "item_ids": []any{dune}, "media_type": "Video"})["id"])
	deleteLater(t, "playlist_delete", "playlist", pl)
	listed := false
	for _, p := range rows(t, call(t, "playlist_list", nil)["playlists"], "playlists") {
		listed = listed || str(p["id"]) == pl
	}
	if !listed {
		t.Error("playlist_list does not list alice's playlist")
	}
	for _, user := range []string{"alice", ""} {
		if got := playlistNames(t, pl, user); !slices.Equal(got, []string{"Dune"}) {
			t.Errorf("alice's new playlist in %q's view = %v, want [Dune]", user, got)
		}
	}

	if out := call(t, "playlist_add", map[string]any{"playlist": pl, "user": "alice", "item_ids": []any{dune2, arrival}}); num(t, out["added"], "added") != 2 {
		t.Errorf("playlist_add as alice = %v", out)
	}
	var entry string
	for _, e := range rows(t, call(t, "playlist_get", map[string]any{"playlist": pl, "user": "alice"})["entries"], "entries") {
		if str(e["name"]) == "Arrival" {
			entry = str(e["entry_id"])
		}
	}
	edit := call(t, "playlist_edit", map[string]any{"playlist": pl, "user": "alice", "move_entry_id": entry, "position": 1, "name": "Zzyzx Alice Renamed"})
	if got := names(t, edit["entries"], "entries"); str(edit["name"]) != "Zzyzx Alice Renamed" || !slices.Equal(got, []string{"Arrival", "Dune", "Dune: Part Two"}) {
		t.Errorf("playlist_edit as alice = %v, entries %v", edit["name"], got)
	}
	for _, e := range rows(t, call(t, "playlist_get", map[string]any{"playlist": pl, "user": "alice"})["entries"], "entries") {
		if str(e["name"]) == "Dune: Part Two" {
			entry = str(e["entry_id"])
		}
	}
	if out := call(t, "playlist_remove", map[string]any{"playlist": pl, "user": "alice", "entry_ids": []any{entry}}); num(t, out["removed"], "removed") != 1 {
		t.Errorf("playlist_remove as alice = %v", out)
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
	if msg := callErr(t, "playlist_add", map[string]any{"playlist": pl, "user": "alice", "item_ids": []any{pilot}}); !strings.Contains(msg, "alice cannot see Pilot ("+pilot+")") || !strings.Contains(msg, "takes only what they can see") {
		t.Errorf("adding what alice cannot see: %s", msg)
	}
	for _, user := range []string{"alice", ""} {
		if got := playlistNames(t, pl, user); !slices.Equal(got, []string{"Arrival", "Dune"}) {
			t.Errorf("after the refused add, %q's view = %v, want [Arrival Dune]", user, got)
		}
	}
	// and a playlist of hers cannot be made with it either
	if msg := callErr(t, "playlist_create", map[string]any{"name": "Zzyzx Alice Hidden", "user": "alice", "item_ids": []any{pilot}, "media_type": "Video"}); !strings.Contains(msg, "alice cannot see Pilot") {
		t.Errorf("creating alice's playlist with what she cannot see: %s", msg)
	}
	for _, p := range rows(t, call(t, "playlist_list", nil)["playlists"], "playlists") {
		if str(p["name"]) == "Zzyzx Alice Hidden" {
			t.Errorf("the refused playlist was made: %v", p)
			deleteLater(t, "playlist_delete", "playlist", str(p["id"]))
		}
	}
}

// Audio playlists hold tracks, and an album or a season added to a playlist
// adds what it holds, each once.
func TestPlaylistsOfTracksAndSeasons(t *testing.T) {
	belgrade := findItem(t, "Music", "Audio", "Belgrade")
	valkyrie := findItem(t, "Music", "Audio", "Valkyrie")
	polygon := findItem(t, "Music", "MusicAlbum", "Polygon")

	pl := str(call(t, "playlist_create", map[string]any{"name": "Zzyzx Tracks", "user": "alice", "item_ids": []any{belgrade, valkyrie}, "media_type": "Audio"})["id"])
	deleteLater(t, "playlist_delete", "playlist", pl)
	got := call(t, "playlist_get", map[string]any{"playlist": pl, "user": "alice"})["entries"]
	if n := names(t, got, "entries"); !slices.Equal(n, []string{"Belgrade", "Valkyrie"}) {
		t.Errorf("the audio playlist = %v", n)
	}
	for _, e := range rows(t, got, "entries") {
		if str(e["type"]) != "Audio" {
			t.Errorf("an audio playlist holds a %v", e["type"])
		}
	}

	// the album is four tracks, two of them already there: the playlist
	// holds each of the album's once more
	out := call(t, "playlist_add", map[string]any{"playlist": pl, "user": "alice", "item_ids": []any{polygon}})
	want := []string{"Belgrade", "Valkyrie", "Belgrade", "Valkyrie", "Solid Gold", "Private Dancer"}
	if num(t, out["added"], "added") != 4 {
		t.Errorf("adding the album = %v, want its 4 tracks added", out)
	}
	if got := playlistNames(t, pl, "alice"); !slices.Equal(got, want) {
		t.Errorf("after adding the album the playlist = %v, want %v", got, want)
	}

	// a season is its episodes, in order
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	var season string
	for _, s := range rows(t, call(t, "show_seasons", map[string]any{"series_id": series})["seasons"], "seasons") {
		if num(t, s["season"], "season") == 1 {
			season = str(s["id"])
		}
	}
	video := str(call(t, "playlist_create", map[string]any{"name": "Zzyzx Season", "media_type": "Video"})["id"])
	deleteLater(t, "playlist_delete", "playlist", video)
	if out := call(t, "playlist_add", map[string]any{"playlist": video, "item_ids": []any{season}}); num(t, out["added"], "added") != 3 {
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
// item_set_state will not touch it, and user_stats no longer counts her
// having watched it. Dune: Part Two, because it has no copy elsewhere for
// Emby's by-title watch state to reach.
func TestAParentalRatingLimit(t *testing.T) {
	film := findItem(t, "Movies", "Movie", "Dune: Part Two")
	before := call(t, "item_get", map[string]any{"id": film})
	t.Cleanup(func() {
		_, _ = invoke("item_edit", map[string]any{"ids": []any{film}, "official_rating": str(before["official_rating"])})
	})
	if str(before["official_rating"]) == "R" {
		t.Fatalf("the film is already rated R: %v", before)
	}
	if got := call(t, "user_get", map[string]any{"user": "alice"}); got["max_parental_rating"] != nil {
		t.Fatalf("alice is limited before the test limits her: %v", got)
	}

	// watched while nothing stopped her; cleaned up after the limit is lifted
	call(t, "item_set_state", map[string]any{"id": film, "user": "alice", "watched": true})
	t.Cleanup(func() { _, _ = invoke("item_set_state", map[string]any{"id": film, "user": "alice", "watched": false}) })
	watched := num(t, call(t, "user_stats", map[string]any{"user": "alice"})["movies_watched"], "movies_watched")

	if out := call(t, "item_edit", map[string]any{"ids": []any{film}, "official_rating": "R"}); !slices.Equal(strs(t, out["changed"], "changed"), []string{"OfficialRating"}) {
		t.Errorf("item_edit official_rating = %v", out)
	}
	visible := 0
	for _, r := range rows(t, call(t, "library_filters", map[string]any{"library": "Movies"})["official_ratings"], "official_ratings") {
		if str(r["value"]) != "R" {
			visible += num(t, r["items"], "items")
		}
	}
	limit := limitAlice(t, "PG-13")

	if got := call(t, "user_get", map[string]any{"user": "alice"}); num(t, got["max_parental_rating"], "max_parental_rating") != limit {
		t.Errorf("user_get alice max_parental_rating = %v, want %d (PG-13 on this server)", got["max_parental_rating"], limit)
	}
	films := call(t, "library_items", map[string]any{"library": "Movies", "user": "alice", "limit": 50})
	if got := names(t, films["items"], "items"); slices.Contains(got, "Dune: Part Two") || num(t, films["total"], "total") != visible {
		t.Errorf("alice sees %v, want the %d films rated under R and not Dune: Part Two", got, visible)
	}
	if msg := callErr(t, "item_set_state", map[string]any{"id": film, "user": "alice", "favourite": true}); !strings.Contains(msg, "rated above what they may watch") {
		t.Errorf("item_set_state on a film above alice's limit: %s", msg)
	}
	if n := num(t, call(t, "user_stats", map[string]any{"user": "alice"})["movies_watched"], "movies_watched"); n != watched-1 {
		t.Errorf("alice's movies_watched = %d under the limit, %d before it: the hidden film still counts", n, watched)
	}
	for _, u := range rows(t, call(t, "item_last_watched", map[string]any{"id": film})["users"], "users") {
		if str(u["user"]) == "alice" {
			t.Errorf("item_last_watched reports alice on a film above her limit: %v", u)
		}
	}
}
