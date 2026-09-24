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

	out := call(t, "item_set_state", map[string]any{"id": e1, "user": "alice", "watched": true})
	if str(out["user"]) != "alice" || str(out["item"]) != "Pilot" {
		t.Errorf("item_set_state = %v", out)
	}
	// only what was set is answered
	if w, _ := out["watched"].(bool); !w || out["favourite"] != nil || out["position_s"] != nil {
		t.Errorf("item_set_state = %v", out)
	}
	t.Cleanup(func() { _, _ = invoke("item_set_state", map[string]any{"id": e1, "user": "alice", "watched": false}) })

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
	// a limit caps the list
	if capped := call(t, "user_next_up", map[string]any{"user": "alice", "limit": 1}); len(rows(t, capped["next_up"], "next_up")) != 1 {
		t.Errorf("limit 1 = %v", capped["next_up"])
	}
	// and root, who has watched nothing, has nothing next
	next = call(t, "user_next_up", nil)
	if str(next["user"]) != "root" || len(rows(t, next["next_up"], "next_up")) != 0 {
		t.Errorf("the default user = %v, next up %v", next["user"], next["next_up"])
	}

	// unmark
	out = call(t, "item_set_state", map[string]any{"id": e1, "user": "alice", "watched": false})
	if w, _ := out["watched"].(bool); w {
		t.Errorf("unmark = %v", out)
	}
	last = call(t, "item_last_watched", map[string]any{"id": e1})
	for _, u := range rows(t, last["users"], "users") {
		if played, _ := u["played"].(bool); str(u["user"]) == "alice" && played {
			t.Error("alice's episode is still played after unmarking")
		}
	}

	if msg := callErr(t, "item_set_state", map[string]any{"id": e1, "user": "nobody", "watched": true}); !strings.Contains(msg, "nobody") {
		t.Errorf("an unknown user: %s", msg)
	}
	for want, args := range map[string]map[string]any{
		"nothing to set": {"id": e1, "user": "alice"},
		"contradict":     {"id": e1, "user": "alice", "watched": true, "position_s": 1},
	} {
		if msg := callErr(t, "item_set_state", args); !strings.Contains(msg, want) {
			t.Errorf("item_set_state %v: %s", args, msg)
		}
	}
}

func TestFavourites(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Arrival")
	out := call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "favourite": true})
	if f, _ := out["favourite"].(bool); !f || str(out["user"]) != "alice" || str(out["item"]) != "Arrival" || out["watched"] != nil {
		t.Errorf("item_set_state = %v", out)
	}
	t.Cleanup(func() {
		_, _ = invoke("item_set_state", map[string]any{"id": id, "user": "alice", "favourite": false})
	})

	// the favourites are library_items in the user's view
	favs := call(t, "library_items", map[string]any{"user": "alice", "watched": "favourite"})
	// Emby keys watch state by provider id, so favouriting the clean
	// Arrival favourites the messy copy with it; Jellyfin keys it by item
	names := []string{}
	for _, it := range rows(t, favs["items"], "items") {
		names = append(names, str(it["name"]))
	}
	if len(names) == 0 || slices.ContainsFunc(names, func(n string) bool { return n != "Arrival" }) {
		t.Errorf("alice's favourites = %v, want Arrival", names)
	}
	if isJellyfin() && len(names) != 1 {
		t.Errorf("alice's favourites = %v, want [Arrival]", names)
	}
	if num(t, favs["total"], "total") != len(names) {
		t.Errorf("total %v for %d favourites", favs["total"], len(names))
	}
	// root has none
	favs = call(t, "library_items", map[string]any{"watched": "favourite"})
	if n := len(rows(t, favs["items"], "items")); n != 0 {
		t.Errorf("root has %d favourites", n)
	}

	out = call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "favourite": false})
	if f, _ := out["favourite"].(bool); f {
		t.Errorf("unfavourite = %v", out)
	}
	favs = call(t, "library_items", map[string]any{"user": "alice", "watched": "favourite"})
	if n := len(rows(t, favs["items"], "items")); n != 0 {
		t.Errorf("alice still has %d favourites", n)
	}
}

// History comes from the activity log's playback events. Only the player the
// journeys sign in as alice plays anything, so root's history is empty and
// alice's holds what those played; what is asserted here is the shape, the
// paging, the user resolution, and that marking played is not playback.
func TestHistory(t *testing.T) {
	out := call(t, "user_history", map[string]any{"user": "alice", "days": 7})
	if str(out["user"]) != "alice" {
		t.Errorf("user_history user = %v", out["user"])
	}
	if _, ok := out["items"].([]any); !ok {
		t.Errorf("items = %v", out["items"])
	}
	total := num(t, out["total"], "total")
	if total < len(rows(t, out["items"], "items")) || num(t, out["offset"], "offset") != 0 {
		t.Errorf("total %v offset %v for %d items", out["total"], out["offset"], len(rows(t, out["items"], "items")))
	}
	// the whole week was read, which the answer says
	if complete, ok := out["complete"].(bool); !ok || !complete || out["note"] != nil {
		t.Errorf("complete = %v, note %v: a week of a test server's log is read whole", out["complete"], out["note"])
	}
	// a page of one is one, and the total is still the whole
	if page := call(t, "user_history", map[string]any{"user": "alice", "days": 7, "limit": 1}); len(rows(t, page["items"], "items")) != min(total, 1) || num(t, page["total"], "total") != total {
		t.Errorf("limit 1 = %v", page)
	}
	// a page past the end is empty, and says where it starts
	out = call(t, "user_history", map[string]any{"user": "alice", "days": 7, "offset": 1000})
	if n := len(rows(t, out["items"], "items")); n != 0 || num(t, out["offset"], "offset") != 1000 {
		t.Errorf("past the end = %d items at offset %v", n, out["offset"])
	}
	// root has played nothing
	out = call(t, "user_history", nil)
	if str(out["user"]) != "root" || num(t, out["total"], "total") != 0 || len(rows(t, out["items"], "items")) != 0 {
		t.Errorf("the default user's history = %v", out)
	}
	if msg := callErr(t, "user_history", map[string]any{"user": "nobody"}); !strings.Contains(msg, "nobody") {
		t.Errorf("an unknown user: %s", msg)
	}

	// Princess Mononoke has been marked watched by other tests and never
	// played, and a mark is not a play
	id := findItem(t, "Movies", "Movie", "Princess Mononoke")
	hist := call(t, "item_watch_history", map[string]any{"id": id, "days": 7})
	if str(hist["item"]) != "Princess Mononoke" || len(strs(t, hist["entries"], "entries")) != 0 || hist["complete"] != true {
		t.Errorf("item_watch_history Princess Mononoke = %v", hist)
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
	// what she has watched is user_stats, not the account
	for _, field := range []string{"movies_watched", "episodes_watched", "favourites", "in_progress"} {
		if _, ok := alice[field]; ok {
			t.Errorf("user_get carries %s: %v", field, alice)
		}
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

// item_set_state with a position puts an item in progress; user_in_progress
// lists it with its position, user_stats counts it; marking it unwatched
// clears it.
func TestProgress(t *testing.T) {
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	before := call(t, "user_stats", map[string]any{"user": "alice"})
	out := call(t, "item_set_state", map[string]any{"id": arrival, "user": "alice", "position_s": 2550})
	if str(out["item"]) != "Arrival" || str(out["user"]) != "alice" || num(t, out["position_s"], "position_s") != 2550 || out["watched"] != nil {
		t.Errorf("item_set_state = %v", out)
	}
	t.Cleanup(func() {
		_, _ = invoke("item_set_state", map[string]any{"id": arrival, "user": "alice", "watched": false})
	})
	if after := call(t, "user_stats", map[string]any{"user": "alice"}); num(t, after["in_progress"], "in_progress") != num(t, before["in_progress"], "in_progress")+1 {
		t.Errorf("user_stats in_progress went from %v to %v, want one more", before["in_progress"], after["in_progress"])
	}

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
	if num(t, row["position_s"], "position_s") != 2550 || num(t, row["percent"], "percent") != 100 {
		t.Errorf("in progress row = %v", row)
	}
	// the resume point reads back in the same unit through item_last_watched
	resumed := false
	for _, u := range rows(t, call(t, "item_last_watched", map[string]any{"id": arrival})["users"], "users") {
		if str(u["user"]) == "alice" {
			resumed = num(t, u["resume_s"], "resume_s") == 2550
		}
	}
	if !resumed {
		t.Error("item_last_watched does not show alice's resume point at 2550 seconds")
	}
	// a limit caps the list
	if capped := call(t, "user_in_progress", map[string]any{"user": "alice", "limit": 1}); len(rows(t, capped["items"], "items")) != 1 {
		t.Errorf("limit 1 = %v", capped["items"])
	}
	// root has nothing in progress
	if n := len(rows(t, call(t, "user_in_progress", nil)["items"], "items")); n != 0 {
		t.Errorf("root has %d in progress", n)
	}

	// marking it unwatched clears the resume point
	call(t, "item_set_state", map[string]any{"id": arrival, "user": "alice", "watched": false})
	for _, it := range rows(t, call(t, "user_in_progress", map[string]any{"user": "alice"})["items"], "items") {
		if str(it["id"]) == arrival {
			t.Errorf("Arrival is still in progress after marking it unwatched: %v", it)
		}
	}

	if msg := callErr(t, "item_set_state", map[string]any{"id": arrival, "position_s": 0}); !strings.Contains(msg, "above zero") {
		t.Errorf("a zero position: %s", msg)
	}
	if n := num(t, call(t, "user_stats", map[string]any{"user": "alice"})["in_progress"], "in_progress"); n != num(t, before["in_progress"], "in_progress") {
		t.Errorf("alice has %d in progress after clearing, was %v", n, before["in_progress"])
	}
}

func TestUserStats(t *testing.T) {
	mononoke := findItem(t, "Movies", "Movie", "Princess Mononoke")
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	eps := rows(t, call(t, "show_episodes", map[string]any{"series_id": series})["episodes"], "episodes")
	watch := []string{mononoke, str(eps[0]["id"]), str(eps[1]["id"])}
	for _, id := range watch {
		call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": true})
	}
	// and one favourited, and one part way through, in the same sweep
	call(t, "item_set_state", map[string]any{"id": mononoke, "user": "alice", "favourite": true})
	third := str(eps[2]["id"])
	call(t, "item_set_state", map[string]any{"id": third, "user": "alice", "position_s": 1})
	t.Cleanup(func() {
		for _, id := range append(watch, third) {
			_, _ = invoke("item_set_state", map[string]any{"id": id, "user": "alice", "watched": false})
		}
		_, _ = invoke("item_set_state", map[string]any{"id": mononoke, "user": "alice", "favourite": false})
	})

	out := call(t, "user_stats", map[string]any{"user": "alice", "library": "Movies"})
	if str(out["user"]) != "alice" || num(t, out["movies_watched"], "movies_watched") != 1 || num(t, out["episodes_watched"], "episodes_watched") != 0 {
		t.Errorf("alice in Movies = %v", out)
	}
	if num(t, out["favourites"], "favourites") != 1 || num(t, out["in_progress"], "in_progress") != 0 {
		t.Errorf("alice in Movies = %v, want 1 favourite and nothing in progress", out)
	}
	if out = call(t, "user_stats", map[string]any{"user": "alice", "library": "Shows"}); num(t, out["in_progress"], "in_progress") != 1 || num(t, out["favourites"], "favourites") != 0 {
		t.Errorf("alice in Shows = %v, want 1 in progress and no favourite", out)
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
	if num(t, root["favourites"], "favourites") != 0 || num(t, root["in_progress"], "in_progress") != 0 {
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
	want := []string{"user_get", "user_history", "user_in_progress", "user_list", "user_next_up", "user_stats"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("user tools = %v, want %v", got, want)
	}
}
