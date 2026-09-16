//go:build integration

package acceptance

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestUserList(t *testing.T) {
	out := call(t, "user_list", nil)
	users := map[string]map[string]any{}
	for _, u := range rows(t, out["users"], "users") {
		users[str(u["name"])] = u
	}
	root, alice := users["root"], users["alice"]
	if root == nil || alice == nil {
		t.Fatalf("users = %v, want root and alice", out["users"])
	}
	if admin, _ := root["admin"].(bool); !admin {
		t.Error("root is not an admin")
	}
	if admin, _ := alice["admin"].(bool); admin {
		t.Error("alice is an admin")
	}
	if str(root["id"]) == "" || str(alice["id"]) == "" {
		t.Error("a user has no id")
	}
}

// Watch state: mark played for alice, see it on the item and in her next
// up, then unmark it.
func TestWatchState(t *testing.T) {
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	eps := call(t, "show_episodes", map[string]any{"series_id": series})
	byNumber := map[int]string{}
	for _, e := range rows(t, eps["episodes"], "episodes") {
		byNumber[num(t, e["episode"], "episode")] = str(e["id"])
	}
	e1, e2 := byNumber[1], byNumber[2]
	if e1 == "" || e2 == "" {
		t.Fatalf("episodes = %v", byNumber)
	}

	out := call(t, "item_set_watched", map[string]any{"id": e1, "user": "alice", "watched": true})
	if str(out["user"]) != "alice" || str(out["item"]) != e1 {
		t.Errorf("item_set_watched = %v", out)
	}
	if w, _ := out["watched"].(bool); !w {
		t.Errorf("item_set_watched = %v", out)
	}
	t.Cleanup(func() { _, _ = invoke("item_set_watched", map[string]any{"id": e1, "user": "alice", "watched": false}) })

	// per-user state on the item
	last := call(t, "item_last_watched", map[string]any{"id": e1})
	if str(last["item"]) != "Pilot" {
		t.Errorf("item_last_watched item = %v", last["item"])
	}
	state := map[string]map[string]any{}
	for _, u := range rows(t, last["users"], "users") {
		state[str(u["user"])] = u
	}
	if played, _ := state["alice"]["played"].(bool); !played {
		t.Errorf("alice's state = %v", state["alice"])
	}
	if played, _ := state["root"]["played"].(bool); played {
		t.Errorf("root's state = %v", state["root"])
	}
	// Jellyfin counts a mark as a play, Emby does not
	if isJellyfin() && num(t, state["alice"]["play_count"], "play_count") < 1 {
		t.Errorf("alice's play count = %v", state["alice"])
	}

	// next up for alice is episode two
	next := call(t, "user_next_up", map[string]any{"user": "alice"})
	if str(next["user"]) != "alice" {
		t.Errorf("user_next_up user = %v", next["user"])
	}
	var found bool
	for _, it := range rows(t, next["next_up"], "next_up") {
		if str(it["id"]) == e2 {
			found = true
		}
	}
	if !found {
		t.Errorf("episode two is not next up for alice: %v", next["next_up"])
	}
	if _, ok := next["resume"].([]any); !ok {
		t.Errorf("resume = %v", next["resume"])
	}
	// and root, who has watched nothing, has nothing next
	next = call(t, "user_next_up", nil)
	if str(next["user"]) != "root" {
		t.Errorf("the default user = %v", next["user"])
	}

	// unmark
	out = call(t, "item_set_watched", map[string]any{"id": e1, "user": "alice", "watched": false})
	if w, _ := out["watched"].(bool); w {
		t.Errorf("unmark = %v", out)
	}
	last = call(t, "item_last_watched", map[string]any{"id": e1})
	for _, u := range rows(t, last["users"], "users") {
		if played, _ := u["played"].(bool); str(u["user"]) == "alice" && played {
			t.Error("alice's episode is still played after unmarking")
		}
	}

	if msg := callErr(t, "item_set_watched", map[string]any{"id": e1, "user": "nobody", "watched": true}); !strings.Contains(msg, "nobody") {
		t.Errorf("an unknown user: %s", msg)
	}
}

func TestFavourites(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Arrival")
	out := call(t, "item_set_favourite", map[string]any{"id": id, "user": "alice", "favourite": true})
	if f, _ := out["favourite"].(bool); !f || str(out["user"]) != "alice" {
		t.Errorf("item_set_favourite = %v", out)
	}
	t.Cleanup(func() {
		_, _ = invoke("item_set_favourite", map[string]any{"id": id, "user": "alice", "favourite": false})
	})

	favs := call(t, "user_favourites", map[string]any{"user": "alice"})
	if str(favs["user"]) != "alice" {
		t.Errorf("user_favourites user = %v", favs["user"])
	}
	// Emby keys watch state by provider id, so favouriting the clean
	// Arrival favourites the messy copy with it; Jellyfin keys it by item
	names := []string{}
	for _, it := range rows(t, favs["favourites"], "favourites") {
		names = append(names, str(it["name"]))
	}
	if len(names) == 0 || slices.ContainsFunc(names, func(n string) bool { return n != "Arrival" }) {
		t.Errorf("alice's favourites = %v, want Arrival", names)
	}
	if isJellyfin() && len(names) != 1 {
		t.Errorf("alice's favourites = %v, want [Arrival]", names)
	}
	// root has none
	favs = call(t, "user_favourites", nil)
	if n := len(rows(t, favs["favourites"], "favourites")); n != 0 {
		t.Errorf("root has %d favourites", n)
	}

	out = call(t, "item_set_favourite", map[string]any{"id": id, "user": "alice", "favourite": false})
	if f, _ := out["favourite"].(bool); f {
		t.Errorf("unfavourite = %v", out)
	}
	favs = call(t, "user_favourites", map[string]any{"user": "alice"})
	if n := len(rows(t, favs["favourites"], "favourites")); n != 0 {
		t.Errorf("alice still has %d favourites", n)
	}
}

// History comes from the activity log's playback events. Nothing in this
// suite plays anything, so the histories are empty; what is asserted is the
// shape and the user resolution, and that marking played is not playback.
func TestHistory(t *testing.T) {
	out := call(t, "user_history", map[string]any{"user": "alice", "days": 7})
	if str(out["user"]) != "alice" {
		t.Errorf("user_history user = %v", out["user"])
	}
	if _, ok := out["watched"].([]any); !ok {
		t.Errorf("watched = %v", out["watched"])
	}
	out = call(t, "user_history", nil)
	if str(out["user"]) != "root" {
		t.Errorf("the default user = %v", out["user"])
	}
	if msg := callErr(t, "user_history", map[string]any{"user": "nobody"}); !strings.Contains(msg, "nobody") {
		t.Errorf("an unknown user: %s", msg)
	}

	id := findItem(t, "Movies", "Movie", "Princess Mononoke")
	hist := call(t, "item_watch_history", map[string]any{"id": id, "days": 7})
	if str(hist["item"]) != "Princess Mononoke" {
		t.Errorf("item_watch_history item = %v", hist["item"])
	}
	if _, ok := hist["entries"].([]any); !ok {
		t.Errorf("entries = %v", hist["entries"])
	}
}

func TestUserGet(t *testing.T) {
	root := call(t, "user_get", nil)
	if str(root["name"]) != "root" || !boolOf(root["admin"]) || !boolOf(root["all_libraries"]) || !boolOf(root["has_password"]) || boolOf(root["disabled"]) {
		t.Errorf("user_get (the default) = %v", root)
	}
	if libs := strs(t, root["libraries"], "libraries"); !slices.Contains(libs, "Movies") || !slices.Contains(libs, "Messy Shows") {
		t.Errorf("root's libraries = %v", libs)
	}

	alice := call(t, "user_get", map[string]any{"user": "ALICE"})
	if str(alice["name"]) != "alice" || boolOf(alice["admin"]) || str(alice["id"]) == "" {
		t.Errorf("user_get alice = %v", alice)
	}
	movies, favourites := num(t, alice["movies_watched"], "movies_watched"), num(t, alice["favourites"], "favourites")

	mononoke := findItem(t, "Movies", "Movie", "Princess Mononoke")
	call(t, "item_set_watched", map[string]any{"id": mononoke, "user": "alice", "watched": true})
	call(t, "item_set_favourite", map[string]any{"id": mononoke, "user": "alice", "favourite": true})
	t.Cleanup(func() {
		_, _ = invoke("item_set_watched", map[string]any{"id": mononoke, "user": "alice", "watched": false})
		_, _ = invoke("item_set_favourite", map[string]any{"id": mononoke, "user": "alice", "favourite": false})
	})
	alice = call(t, "user_get", map[string]any{"user": "alice"})
	if num(t, alice["movies_watched"], "movies_watched") != movies+1 || num(t, alice["favourites"], "favourites") != favourites+1 {
		t.Errorf("after watching and favouriting Princess Mononoke = %v", alice)
	}

	if msg := callErr(t, "user_get", map[string]any{"user": "nobody"}); !strings.Contains(msg, "nobody") {
		t.Errorf("an unknown user: %s", msg)
	}
}

// boolOf pulls a JSON boolean out of a decoded field, false when absent.
func boolOf(v any) bool {
	b, _ := v.(bool)
	return b
}

// item_set_progress puts an item in progress; user_in_progress lists it
// with its position; marking it unwatched clears it.
func TestProgress(t *testing.T) {
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	out := call(t, "item_set_progress", map[string]any{"id": arrival, "user": "alice", "position_minutes": 42.5})
	if str(out["item"]) != "Arrival" || str(out["user"]) != "alice" || out["position_minutes"] != 42.5 {
		t.Errorf("item_set_progress = %v", out)
	}
	t.Cleanup(func() {
		_, _ = invoke("item_set_watched", map[string]any{"id": arrival, "user": "alice", "watched": false})
	})

	var row map[string]any
	for range 10 {
		for _, it := range rows(t, call(t, "user_in_progress", map[string]any{"user": "alice"})["items"], "items") {
			if str(it["id"]) == arrival {
				row = it
			}
		}
		if row != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if row == nil {
		t.Fatal("Arrival is not in progress for alice")
	}
	// the file runs a second, so the position is past its end: the percent is capped
	if row["position_minutes"] != 42.5 || num(t, row["percent"], "percent") != 100 {
		t.Errorf("in progress row = %v", row)
	}
	// root has nothing in progress
	if n := len(rows(t, call(t, "user_in_progress", nil)["items"], "items")); n != 0 {
		t.Errorf("root has %d in progress", n)
	}

	// marking it unwatched clears the resume point
	call(t, "item_set_watched", map[string]any{"id": arrival, "user": "alice", "watched": false})
	for _, it := range rows(t, call(t, "user_in_progress", map[string]any{"user": "alice"})["items"], "items") {
		if str(it["id"]) == arrival {
			t.Errorf("Arrival is still in progress after marking it unwatched: %v", it)
		}
	}

	if msg := callErr(t, "item_set_progress", map[string]any{"id": arrival, "position_minutes": 0}); !strings.Contains(msg, "above zero") {
		t.Errorf("a zero position: %s", msg)
	}
}

func TestUserStats(t *testing.T) {
	mononoke := findItem(t, "Movies", "Movie", "Princess Mononoke")
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	eps := rows(t, call(t, "show_episodes", map[string]any{"series_id": series})["episodes"], "episodes")
	watch := []string{mononoke, str(eps[0]["id"]), str(eps[1]["id"])}
	for _, id := range watch {
		call(t, "item_set_watched", map[string]any{"id": id, "user": "alice", "watched": true})
	}
	t.Cleanup(func() {
		for _, id := range watch {
			_, _ = invoke("item_set_watched", map[string]any{"id": id, "user": "alice", "watched": false})
		}
	})

	out := call(t, "user_stats", map[string]any{"user": "alice", "library": "Movies"})
	if str(out["user"]) != "alice" || num(t, out["movies_watched"], "movies_watched") != 1 || num(t, out["episodes_watched"], "episodes_watched") != 0 {
		t.Errorf("alice in Movies = %v", out)
	}

	out = call(t, "user_stats", map[string]any{"user": "alice"})
	if num(t, out["movies_watched"], "movies_watched") < 1 || num(t, out["episodes_watched"], "episodes_watched") < 2 || num(t, out["series_started"], "series_started") < 1 {
		t.Errorf("alice everywhere = %v", out)
	}
	var bb map[string]any
	for _, s := range rows(t, out["top_series"], "top_series") {
		if str(s["name"]) == "Breaking Bad" {
			bb = s
		}
	}
	// Breaking Bad has a third episode on disk
	if bb == nil || num(t, bb["episodes_watched"], "episodes_watched") != 2 || boolOf(bb["finished"]) {
		t.Errorf("Breaking Bad in top_series = %v", out["top_series"])
	}
	genres := valueCounts(t, out["top_genres"], "top_genres")
	if genres["Animation"] < 1 || genres["Drama"] < 1 {
		t.Errorf("top genres = %v, want Princess Mononoke's Animation and Breaking Bad's Drama", genres)
	}

	// root has watched nothing
	root := call(t, "user_stats", nil)
	if num(t, root["movies_watched"], "movies_watched") != 0 || num(t, root["series_started"], "series_started") != 0 || len(rows(t, root["top_series"], "top_series")) != 0 {
		t.Errorf("root's stats = %v", root)
	}
}

func TestUserFamilyIsComplete(t *testing.T) {
	var got []string
	for _, name := range toolNames(t) {
		if strings.HasPrefix(name, "user_") {
			got = append(got, name)
		}
	}
	want := []string{"user_favourites", "user_get", "user_history", "user_in_progress", "user_list", "user_next_up", "user_stats"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("user tools = %v, want %v", got, want)
	}
}
