//go:build integration

package acceptance

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
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
	if root == nil || alice == nil || len(users) != 2 {
		t.Fatalf("users = %v, want root and alice", out["users"])
	}
	if admin, _ := root["admin"].(bool); !admin {
		t.Error("root is not an admin")
	}
	if admin, _ := alice["admin"].(bool); admin {
		t.Error("alice is an admin")
	}
	if str(root["id"]) != os.Getenv("EMBYFIN_TEST_ADMIN_ID") || str(alice["id"]) != os.Getenv("EMBYFIN_TEST_USER_ID") {
		t.Errorf("root and alice are %v and %v, want the ids the server gave them", root["id"], alice["id"])
	}
}

// Watch state: mark played for alice, see it on the item and in her next
// up, then unmark it.
func TestWatchState(t *testing.T) {
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	eps := call(t, "library_episodes", map[string]any{"series_id": series})
	byNumber := map[int]string{}
	for _, e := range rows(t, eps["episodes"], "episodes") {
		byNumber[num(t, e["episode"], "episode")] = str(e["id"])
	}
	e1, e2, e3 := byNumber[1], byNumber[2], byNumber[3]
	if e1 == "" || e2 == "" || e3 == "" {
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
	// both servers count a mark as a play, and date it (Emby 4.10 does; an
	// older Emby did not), and root's untouched state has neither
	if n := numOr0(state["alice"]["play_count"]); n != 1 || str(state["alice"]["last_played"]) == "" {
		t.Errorf("alice's state after a mark = %v, want played once, with a date", state["alice"])
	}
	if state["root"]["play_count"] != nil || state["root"]["last_played"] != nil {
		t.Errorf("root's state = %v, want no play", state["root"])
	}

	// next up for alice is episode two
	next := call(t, "user_next_up", map[string]any{"user": "alice"})
	if str(next["user"]) != "alice" {
		t.Errorf("user_next_up user = %v", next["user"])
	}
	var nextIDs []string
	for _, it := range rows(t, next["next_up"], "next_up") {
		nextIDs = append(nextIDs, str(it["id"]))
	}
	if !slices.Contains(nextIDs, e2) || slices.Contains(nextIDs, e1) || slices.Contains(nextIDs, e3) {
		t.Errorf("alice's next up = %v, want episode two %s and not one or three", nextIDs, e2)
	}
	// and the pilot is not part way through: it was marked, not started
	for _, it := range rows(t, next["in_progress"], "in_progress") {
		if id := str(it["id"]); id == e1 || id == e2 {
			t.Errorf("a Breaking Bad episode is in progress after a mark: %v", it)
		}
	}
	// and root, who has marked nothing, has none of it next
	next = call(t, "user_next_up", nil)
	if str(next["user"]) != "root" {
		t.Errorf("the default user = %v", next["user"])
	}
	for _, it := range rows(t, next["next_up"], "next_up") {
		if str(it["series"]) == "Breaking Bad" {
			t.Errorf("root has Breaking Bad next: %v", it)
		}
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

	// a user named by id is the same user
	if out := call(t, "item_set_state", map[string]any{"id": e3, "user": os.Getenv("EMBYFIN_TEST_USER_ID"), "favourite": false}); str(out["user"]) != "alice" {
		t.Errorf("alice by id = %v", out)
	}

	if msg := callErr(t, "item_set_state", map[string]any{"id": e1, "user": "nobody", "watched": true}); !strings.Contains(msg, "nobody") {
		t.Errorf("an unknown user: %s", msg)
	}
	for want, args := range map[string]map[string]any{
		"nothing to set":                 {"id": e1, "user": "alice"},
		"contradict":                     {"id": e1, "user": "alice", "watched": true, "position_s": 1},
		"no item with id " + unknownID(): {"id": unknownID(), "user": "alice", "watched": true},
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

	// the favourites are library_items in the user's view. Emby keys watch
	// state by provider id, so favouriting the clean Arrival favourites the
	// messy copy with it; Jellyfin keys it by item
	favs := call(t, "library_items", map[string]any{"user": "alice", "watched": "favourite"})
	var paths []string
	for _, it := range rows(t, favs["items"], "items") {
		if str(it["name"]) != "Arrival" {
			t.Errorf("alice's favourites hold %v", it["name"])
		}
		paths = append(paths, str(it["path"]))
	}
	slices.Sort(paths)
	want := []string{"/media/messy-movies/Arrival (2016)/Arrival (2016).mp4", "/media/movies/Arrival (2016)/Arrival (2016).mp4"}
	if isJellyfin() {
		want = want[1:]
	}
	if !slices.Equal(paths, want) {
		t.Errorf("alice's favourites = %v, want %v", paths, want)
	}
	if num(t, favs["total"], "total") != len(paths) {
		t.Errorf("total %v for %d favourites", favs["total"], len(paths))
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

// History comes from the activity log's playback events: alice plays Aliens
// through here, and starts Blade Runner and leaves it playing, so her
// history holds both, each with the last thing it did - and root's, who has
// played nothing, holds neither. Marking played is not playback.
func TestHistory(t *testing.T) {
	_, token := signInPlayer(t)
	aliens := findItem(t, "Movies", "Movie", "Aliens")
	blade := findItem(t, "Movies", "Movie", "Blade Runner")
	t.Cleanup(func() {
		for _, id := range []string{aliens, blade} {
			_, _ = invoke("item_set_state", map[string]any{"id": id, "user": "alice", "watched": false})
		}
	})
	var out map[string]any
	history := map[string]map[string]any{}
	// the servers write the log a moment after the play is reported, so each
	// play is waited for until its last event is the one just made
	since := func(id, event string, at time.Time) bool {
		out = call(t, "user_history", map[string]any{"user": "alice", "days": 7})
		clear(history)
		for _, it := range rows(t, out["items"], "items") {
			history[str(it["id"])] = it
		}
		row := history[id]
		return row != nil && str(row["event"]) == event && !stamp(t, row["last_played"]).Before(at)
	}
	// the server's clock and this one's may disagree by a moment
	began := time.Now().Add(-time.Minute)
	playThrough(t, token, aliens)
	if !eventually(func() bool { return since(aliens, "stop", began) }) {
		t.Fatalf("alice's history = %v, want Aliens played through a moment ago", out["items"])
	}
	// a second on, so the two plays' entries cannot be put in either order
	time.Sleep(1100 * time.Millisecond)
	playing := startPlaying(t, token, blade)
	if !eventually(func() bool { return since(blade, "start", began) }) {
		t.Fatalf("alice's history = %v, want Blade Runner begun a moment ago", out["items"])
	}
	if str(out["user"]) != "alice" {
		t.Errorf("user_history user = %v", out["user"])
	}
	// the last thing each did: Aliens played through, Blade Runner begun
	for id, want := range map[string]string{aliens: "stop", blade: "start"} {
		row := history[id]
		if str(row["event"]) != want || str(row["type"]) != "Movie" {
			t.Errorf("%v in alice's history = event %v, want %s", row["name"], row["event"], want)
		}
		if at := stamp(t, row["last_played"]); at.Before(began) || at.After(time.Now().Add(time.Minute)) {
			t.Errorf("%v was last played %v, want a moment ago", row["name"], at)
		}
	}
	// each item once, and a total that is every row
	total := num(t, out["total"], "total")
	if total != len(rows(t, out["items"], "items")) || num(t, out["offset"], "offset") != 0 || total != len(history) {
		t.Errorf("total %v offset %v for %d items (%d distinct)", out["total"], out["offset"], len(rows(t, out["items"], "items")), len(history))
	}
	// the whole week was read, which the answer says
	if complete, ok := out["complete"].(bool); !ok || !complete || out["note"] != nil {
		t.Errorf("complete = %v, note %v: a week of a test server's log is read whole", out["complete"], out["note"])
	}
	// most recent first: Blade Runner was begun after Aliens finished
	if first := rows(t, out["items"], "items")[0]; str(first["id"]) != blade {
		t.Errorf("the most recent in alice's history = %v, want Blade Runner", first["name"])
	}
	// a page of one is one, and the total is still the whole
	if page := call(t, "user_history", map[string]any{"user": "alice", "days": 7, "limit": 1}); len(rows(t, page["items"], "items")) != 1 || num(t, page["total"], "total") != total {
		t.Errorf("limit 1 = %v", page)
	}
	// a page past the end is empty, and says where it starts
	past := call(t, "user_history", map[string]any{"user": "alice", "days": 7, "offset": 1000})
	if n := len(rows(t, past["items"], "items")); n != 0 || num(t, past["offset"], "offset") != 1000 || num(t, past["total"], "total") != total {
		t.Errorf("past the end = %d items at offset %v of %v", n, past["offset"], past["total"])
	}
	// root has played nothing
	out = call(t, "user_history", nil)
	if str(out["user"]) != "root" || num(t, out["total"], "total") != 0 || len(rows(t, out["items"], "items")) != 0 {
		t.Errorf("the default user's history = %v", out)
	}
	if msg := callErr(t, "user_history", map[string]any{"user": "nobody"}); !strings.Contains(msg, "nobody") {
		t.Errorf("an unknown user: %s", msg)
	}
	playing.stop(0)

	// an item's history: Aliens has alice's playback, and Princess Mononoke,
	// marked watched and never played, has none
	hist := call(t, "item_watch_history", map[string]any{"id": aliens, "days": 7})
	if entries := strs(t, hist["entries"], "entries"); str(hist["item"]) != "Aliens" || !slices.ContainsFunc(entries, func(e string) bool { return strings.Contains(e, "alice") && strings.Contains(e, "Aliens") }) || hist["complete"] != true {
		t.Errorf("item_watch_history Aliens = %v", hist)
	}
	mononoke := findItem(t, "Movies", "Movie", "Princess Mononoke")
	call(t, "item_set_state", map[string]any{"id": mononoke, "user": "alice", "watched": true})
	t.Cleanup(func() {
		_, _ = invoke("item_set_state", map[string]any{"id": mononoke, "user": "alice", "watched": false})
	})
	hist = call(t, "item_watch_history", map[string]any{"id": mononoke, "days": 7})
	if str(hist["item"]) != "Princess Mononoke" || len(strs(t, hist["entries"], "entries")) != 0 || hist["complete"] != true {
		t.Errorf("item_watch_history Princess Mononoke = %v", hist)
	}
	if msg := callErr(t, "item_watch_history", map[string]any{"id": unknownID()}); !strings.Contains(msg, "no item with id "+unknownID()) {
		t.Errorf("an unknown item: %s", msg)
	}
}

// An item's history is its own: Dune: Part Two's title holds Dune's, so a
// play of it must not be put down to Dune.
func TestWatchHistoryOfATitleInsideAnother(t *testing.T) {
	_, token := signInPlayer(t)
	dune := findItem(t, "Movies", "Movie", "Dune")
	partTwo := findItem(t, "Movies", "Movie", "Dune: Part Two")
	t.Cleanup(func() {
		_, _ = invoke("item_set_state", map[string]any{"id": partTwo, "user": "alice", "watched": false})
	})
	playThrough(t, token, partTwo)

	mentions := func(id string) bool {
		return slices.ContainsFunc(strs(t, call(t, "item_watch_history", map[string]any{"id": id, "days": 1})["entries"], "entries"), func(e string) bool {
			return strings.Contains(e, "Dune: Part Two")
		})
	}
	if !eventually(func() bool { return mentions(partTwo) }) {
		t.Fatalf("Dune: Part Two's history never showed its play: %v", call(t, "item_watch_history", map[string]any{"id": partTwo, "days": 1}))
	}
	if mentions(dune) {
		t.Errorf("Dune's history holds Dune: Part Two's play: %v", call(t, "item_watch_history", map[string]any{"id": dune, "days": 1})["entries"])
	}
}

// On Emby an activity entry names its user only in its text, "<user> is
// playing ...", so a user whose name is another's with a word after it is
// told apart by the longer name winning; on Jellyfin the entry carries the
// user's id. Either way, the history is the right user's.
func TestHistoryOfAUserNamedLikeAnother(t *testing.T) {
	const name, device = "alice zzyzx", "acceptance-zzyzx"
	id := createUser(t, name)
	token := signIn(t, name, os.Getenv("EMBYFIN_TEST_PASSWORD"), device)
	t.Cleanup(func() { signOut(t, token, device) })
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	p := startPlayingAs(t, token, device, id, arrival)
	p.stop(5_000_000)

	var theirs []string
	if !eventually(func() bool {
		theirs = names(t, call(t, "user_history", map[string]any{"user": name, "days": 1})["items"], "items")
		return slices.Contains(theirs, "Arrival")
	}) {
		t.Fatalf("%s's history = %v, want the Arrival just played", name, theirs)
	}
	for _, it := range rows(t, call(t, "user_history", map[string]any{"user": "alice", "days": 1})["items"], "items") {
		if str(it["id"]) == arrival {
			t.Errorf("alice's history holds the Arrival %s played: %v", name, it)
		}
	}
}

// createUser makes an account, with the suite's password, for the length of
// a test; no tool makes one.
func createUser(t *testing.T, name string) string {
	t.Helper()

	password := os.Getenv("EMBYFIN_TEST_PASSWORD")
	body := map[string]any{"Name": name, "Password": password}
	status, raw := api(t, http.MethodPost, "/Users/New", "", body)
	var user struct {
		ID string `json:"Id"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &user) != nil || user.ID == "" {
		t.Fatalf("creating %s: HTTP %d: %s", name, status, raw)
	}
	t.Cleanup(func() {
		if status, raw := api(t, http.MethodDelete, "/Users/"+user.ID, "", nil); status/100 != 2 {
			t.Errorf("removing %s: HTTP %d: %s", name, status, raw)
		}
	})
	// Emby takes the password on its own
	if !isJellyfin() {
		if status, raw := api(t, http.MethodPost, "/Users/"+user.ID+"/Password", "", map[string]any{"NewPw": password}); status/100 != 2 {
			t.Fatalf("setting %s's password: HTTP %d: %s", name, status, raw)
		}
	}

	return user.ID
}

func TestUserGet(t *testing.T) {
	root := call(t, "user_get", nil)
	if str(root["name"]) != "root" || str(root["id"]) != os.Getenv("EMBYFIN_TEST_ADMIN_ID") || !boolOf(root["admin"]) || !boolOf(root["all_libraries"]) || !boolOf(root["has_password"]) || boolOf(root["disabled"]) {
		t.Errorf("user_get (the default) = %v", root)
	}
	// an administrator may delete and connect from anywhere; Jellyfin keeps
	// its accounts off the login screen from the start, Emby shows them
	if !boolOf(root["can_delete"]) || !boolOf(root["remote_access"]) || boolOf(root["hidden"]) != isJellyfin() {
		t.Errorf("root's permissions = can_delete %v remote_access %v hidden %v", root["can_delete"], root["remote_access"], root["hidden"])
	}
	// every library, and the folders the servers keep collections and
	// playlists in once there are any
	libs := slices.DeleteFunc(strs(t, root["libraries"], "libraries"), func(name string) bool { return serverFolders[name] != "" })
	if !slices.Equal(sorted(libs), []string{"Messy Movies", "Messy Shows", "Movies", "Music", "Shows"}) {
		t.Errorf("root's libraries = %v", root["libraries"])
	}
	// root signed in to set the server up
	if at := stamp(t, root["last_login"]); at.After(time.Now().Add(time.Minute)) {
		t.Errorf("root last logged in at %v", at)
	}

	alice := call(t, "user_get", map[string]any{"user": "ALICE"})
	if str(alice["name"]) != "alice" || boolOf(alice["admin"]) || str(alice["id"]) != os.Getenv("EMBYFIN_TEST_USER_ID") || boolOf(alice["can_delete"]) {
		t.Errorf("user_get alice = %v", alice)
	}
	// what she has watched is user_stats, not the account
	for _, field := range []string{"movies_watched", "episodes_watched", "favourites", "in_progress"} {
		if _, ok := alice[field]; ok {
			t.Errorf("user_get carries %s: %v", field, alice)
		}
	}
	// a sign-in is when she last logged in
	began := time.Now().Add(-time.Minute)
	ensurePlayer(t)
	if at := stamp(t, call(t, "user_get", map[string]any{"user": "alice"})["last_login"]); at.Before(began) {
		t.Errorf("alice last logged in at %v, before the sign-in a moment ago", at)
	}

	if msg := callErr(t, "user_get", map[string]any{"user": "nobody"}); !strings.Contains(msg, "nobody") {
		t.Errorf("an unknown user: %s", msg)
	}
}

// What an account may do and how it wants playback read back as they are
// set: every field turned from its default by the HTTP API, since no tool
// changes an account, and put back after.
func TestUserGetReadsWhatIsSet(t *testing.T) {
	alice := os.Getenv("EMBYFIN_TEST_USER_ID")
	status, raw := api(t, http.MethodGet, "/Users/"+alice, "", nil)
	var user struct {
		Policy        map[string]any
		Configuration map[string]any
	}
	if status != http.StatusOK || json.Unmarshal(raw, &user) != nil || user.Policy == nil || user.Configuration == nil {
		t.Fatalf("reading alice: HTTP %d: %.200s", status, raw)
	}
	configPath := "/Users/" + alice + "/Configuration"
	if isJellyfin() {
		configPath = "/Users/Configuration?userId=" + alice
	}
	policy, _ := json.Marshal(user.Policy)
	config, _ := json.Marshal(user.Configuration)
	t.Cleanup(func() {
		var p, c map[string]any
		_ = json.Unmarshal(policy, &p)
		_ = json.Unmarshal(config, &c)
		if status, raw := api(t, http.MethodPost, "/Users/"+alice+"/Policy", "", p); status/100 != 2 {
			t.Errorf("putting alice's policy back: HTTP %d: %s", status, raw)
		}
		if status, raw := api(t, http.MethodPost, configPath, "", c); status/100 != 2 {
			t.Errorf("putting alice's playback settings back: HTTP %d: %s", status, raw)
		}
	})

	before := call(t, "user_get", map[string]any{"user": "alice"})
	hidden := !boolOf(before["hidden"])
	user.Policy["IsHidden"] = hidden
	user.Policy["IsDisabled"] = true
	user.Policy["EnableContentDeletion"] = true
	user.Policy["EnableRemoteAccess"] = false
	if status, raw := api(t, http.MethodPost, "/Users/"+alice+"/Policy", "", user.Policy); status/100 != 2 {
		t.Fatalf("changing alice's policy: HTTP %d: %s", status, raw)
	}
	user.Configuration["AudioLanguagePreference"] = "jpn"
	user.Configuration["SubtitleLanguagePreference"] = "eng"
	user.Configuration["SubtitleMode"] = "Always"
	user.Configuration["PlayDefaultAudioTrack"] = false
	if status, raw := api(t, http.MethodPost, configPath, "", user.Configuration); status/100 != 2 {
		t.Fatalf("changing alice's playback settings: HTTP %d: %s", status, raw)
	}

	got := call(t, "user_get", map[string]any{"user": "alice"})
	if boolOf(got["hidden"]) != hidden || !boolOf(got["disabled"]) || !boolOf(got["can_delete"]) || boolOf(got["remote_access"]) {
		t.Errorf("alice's permissions = hidden %v disabled %v can_delete %v remote_access %v, want %v, true, true and false",
			got["hidden"], got["disabled"], got["can_delete"], got["remote_access"], hidden)
	}
	if str(got["audio_language"]) != "jpn" || str(got["subtitle_language"]) != "eng" || str(got["subtitle_mode"]) != "Always" || boolOf(got["play_default_audio_track"]) {
		t.Errorf("alice's playback = audio %v subtitles %v mode %v default track %v, want jpn, eng, Always and false",
			got["audio_language"], got["subtitle_language"], got["subtitle_mode"], got["play_default_audio_track"])
	}
	// and before, none of it: the defaults read as the defaults
	if before["audio_language"] != nil || before["subtitle_language"] != nil || boolOf(before["disabled"]) || !boolOf(before["play_default_audio_track"]) {
		t.Errorf("alice before = %v", before)
	}
}

// boolOf pulls a JSON boolean out of a decoded field, false when absent.
func boolOf(v any) bool {
	b, _ := v.(bool)
	return b
}

// item_set_state with a position puts an item in progress; user_next_up
// lists it among in_progress with where it resumes, user_stats counts it;
// marking it unwatched clears it.
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

	inProgress := func() map[string]any {
		for _, it := range rows(t, call(t, "user_next_up", map[string]any{"user": "alice"})["in_progress"], "in_progress") {
			if str(it["id"]) == arrival {
				return it
			}
		}
		return nil
	}
	var row map[string]any
	if !eventually(func() bool { row = inProgress(); return row != nil }) {
		t.Fatal("Arrival is not in progress for alice")
	}
	// the file runs a second, so the position is past its end: the percent is
	// capped
	if num(t, row["position_s"], "position_s") != 2550 || num(t, row["percent"], "percent") != 100 || str(row["name"]) != "Arrival" || str(row["type"]) != "Movie" {
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

	// a new resume point with watched false keeps the point: unplayed is what
	// a resume point already is, and marking it so again would clear it
	out = call(t, "item_set_state", map[string]any{"id": arrival, "user": "alice", "position_s": 40, "watched": false})
	if num(t, out["position_s"], "position_s") != 40 || boolOf(out["watched"]) || out["watched"] == nil {
		t.Errorf("a position with watched false = %v", out)
	}
	if !eventually(func() bool { row = inProgress(); return row != nil && num(t, row["position_s"], "position_s") == 40 }) {
		t.Errorf("after moving the resume point to 40 seconds with watched false, alice's Arrival = %v", row)
	}

	// root has nothing in progress
	if n := len(rows(t, call(t, "user_next_up", nil)["in_progress"], "in_progress")); n != 0 {
		t.Errorf("root has %d in progress", n)
	}

	// marking it unwatched clears the resume point
	call(t, "item_set_state", map[string]any{"id": arrival, "user": "alice", "watched": false})
	if !holds(func() bool { return inProgress() == nil }) {
		t.Errorf("Arrival is still in progress after marking it unwatched: %v", inProgress())
	}

	if msg := callErr(t, "item_set_state", map[string]any{"id": arrival, "position_s": 0}); !strings.Contains(msg, "above zero") {
		t.Errorf("a zero position: %s", msg)
	}
	if n := num(t, call(t, "user_stats", map[string]any{"user": "alice"})["in_progress"], "in_progress"); n != num(t, before["in_progress"], "in_progress") {
		t.Errorf("alice has %d in progress after clearing, was %v", n, before["in_progress"])
	}
}

// An item in progress says when it was last played: The Thirteenth Floor
// played through a moment ago and then taken back to a resume point is in
// progress, played a moment ago. (A play stopped part way cannot make one on
// both servers here: Jellyfin keeps no resume point in a file shorter than
// five minutes, and the longest fixture runs three.)
func TestInProgressSaysWhenPlayed(t *testing.T) {
	film := findItem(t, "Movies", "Movie", "The Thirteenth Floor")
	t.Cleanup(func() { _, _ = invoke("item_set_state", map[string]any{"id": film, "user": "alice", "watched": false}) })
	_, token := signInPlayer(t)
	began := time.Now().Add(-time.Minute)
	playThrough(t, token, film)
	if !eventually(func() bool {
		for _, u := range rows(t, call(t, "item_last_watched", map[string]any{"id": film})["users"], "users") {
			if str(u["user"]) == "alice" && boolOf(u["played"]) {
				return true
			}
		}
		return false
	}) {
		t.Fatal("the play through never marked The Thirteenth Floor played for alice")
	}
	call(t, "item_set_state", map[string]any{"id": film, "user": "alice", "position_s": 1})

	var row map[string]any
	if !eventually(func() bool {
		row = nil
		for _, it := range rows(t, call(t, "user_next_up", map[string]any{"user": "alice"})["in_progress"], "in_progress") {
			if str(it["id"]) == film {
				row = it
			}
		}
		return row != nil
	}) {
		t.Fatal("The Thirteenth Floor is not in progress for alice")
	}
	if num(t, row["position_s"], "position_s") != 1 || str(row["name"]) != "The Thirteenth Floor" {
		t.Errorf("in progress row = %v", row)
	}
	// on both servers: Emby's resume list carries the date only when asked
	// for it, and the tool asks
	if at := stamp(t, row["last_played"]); at.Before(began) || at.After(time.Now().Add(time.Minute)) {
		t.Errorf("last played %v, want a moment ago", at)
	}
}

func TestUserStats(t *testing.T) {
	mononoke := findItem(t, "Movies", "Movie", "Princess Mononoke")
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	eps := rows(t, call(t, "library_episodes", map[string]any{"series_id": series})["episodes"], "episodes")
	if len(eps) != 3 {
		t.Fatalf("Breaking Bad has %d episodes, want 3", len(eps))
	}
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
	if msg := callErr(t, "user_stats", map[string]any{"user": "alice", "library": "Nope"}); !strings.Contains(msg, `no library named "Nope"`) {
		t.Errorf("an unknown library: %s", msg)
	}
}

// Hours are the runtime of what was watched, and the most played what was
// played, a mark counting as a play: .hack//Liminality's two three-minute
// episodes marked watched and its one-second last played through are six
// minutes, a tenth of an hour, and three episodes played once each.
func TestUserStatsHoursAndPlays(t *testing.T) {
	byNumber := map[int]string{}
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Shows", "types": "Episode", "query": "In the Case of", "limit": 10})["items"], "items") {
		if str(it["series"]) == ".hack//Liminality" {
			byNumber[num(t, it["episode"], "episode")] = str(it["id"])
		}
	}
	if len(byNumber) != 3 {
		t.Fatalf(".hack//Liminality's episodes = %v", byNumber)
	}
	t.Cleanup(func() {
		for _, id := range byNumber {
			_, _ = invoke("item_set_state", map[string]any{"id": id, "user": "alice", "watched": false})
		}
	})
	stats := func() map[string]any {
		return call(t, "user_stats", map[string]any{"user": "alice", "library": "Messy Shows"})
	}
	if before := stats(); decimal(t, before["hours_watched"], "hours_watched") != 0 || len(rows(t, before["most_played"], "most_played")) != 0 {
		t.Fatalf("alice in Messy Shows before = %v, want nothing watched", before)
	}

	for _, n := range []int{1, 2} {
		call(t, "item_set_state", map[string]any{"id": byNumber[n], "user": "alice", "watched": true})
	}
	_, token := signInPlayer(t)
	playThrough(t, token, byNumber[3])

	var after map[string]any
	if !eventually(func() bool { after = stats(); return num(t, after["episodes_watched"], "episodes_watched") == 3 }) {
		t.Fatalf("alice in Messy Shows = %v, want three episodes watched", after)
	}
	// six minutes and a second
	if h := decimal(t, after["hours_watched"], "hours_watched"); h != 0.1 {
		t.Errorf("hours watched = %v, want 0.1", h)
	}
	if s := rows(t, after["top_series"], "top_series"); len(s) != 1 || str(s[0]["name"]) != ".hack//Liminality" || !boolOf(s[0]["finished"]) || num(t, after["series_finished"], "series_finished") != 1 {
		t.Errorf("top series = %v, finished %v: want .hack//Liminality, finished", s, after["series_finished"])
	}
	// on both servers: Emby's lists carry the play count only when asked for
	// it, and the tool asks
	assertMostPlayed(t, after)
}

// A film played through is among the user's most played, on both servers:
// Emby's list of the user's items carried no play counts until asked for
// them, and nothing was ever most played there.
func TestMostPlayedFilm(t *testing.T) {
	film := findItem(t, "Movies", "Movie", "The Thirteenth Floor")
	t.Cleanup(func() { _, _ = invoke("item_set_state", map[string]any{"id": film, "user": "alice", "watched": false}) })
	_, token := signInPlayer(t)
	playThrough(t, token, film)
	var row map[string]any
	if !eventually(func() bool {
		row = nil
		for _, p := range rows(t, call(t, "user_stats", map[string]any{"user": "alice", "library": "Movies"})["most_played"], "most_played") {
			if str(p["name"]) == "The Thirteenth Floor" {
				row = p
			}
		}
		return row != nil
	}) {
		t.Fatal("The Thirteenth Floor, played through, is not among alice's most played")
	}
	if num(t, row["plays"], "plays") < 1 || str(row["type"]) != "Movie" {
		t.Errorf("most played row = %v", row)
	}
}

// assertMostPlayed checks user_stats counts each of .hack//Liminality's
// episodes played once.
func assertMostPlayed(t *testing.T, stats map[string]any) {
	t.Helper()

	plays := map[string]int{}
	for _, p := range rows(t, stats["most_played"], "most_played") {
		plays[str(p["name"])] = num(t, p["plays"], "plays")
		if str(p["type"]) != "Episode" {
			t.Errorf("most played = %v", p)
		}
	}
	want := map[string]int{
		".hack//Liminality: In the Case of Mai Minase":  1,
		".hack//Liminality: In the Case of Yuki Aihara": 1,
		".hack//Liminality: In the Case of Kyoko Tohno": 1,
	}
	if !reflect.DeepEqual(plays, want) {
		t.Errorf("most played = %v, want %v", plays, want)
	}
}
