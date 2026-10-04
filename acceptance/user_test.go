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

	acc "github.com/katbyte/embyfin-mcp/lib/acceptance"
)

func TestUserList(t *testing.T) {
	out := suite.Call(t, "user_list", nil)
	users := map[string]map[string]any{}
	for _, u := range acc.Rows(t, out["users"], "users") {
		users[acc.Str(u["name"])] = u
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
	if acc.Str(root["id"]) != os.Getenv("EMBYFIN_TEST_ADMIN_ID") || acc.Str(alice["id"]) != os.Getenv("EMBYFIN_TEST_USER_ID") {
		t.Errorf("root and alice are %v and %v, want the ids the server gave them", root["id"], alice["id"])
	}
}

// Watch state: mark played for alice, see it on the item and in her next
// up, then unmark it.
func TestWatchState(t *testing.T) {
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	eps := suite.Call(t, "library_episodes", map[string]any{"series_id": series})
	byNumber := map[int]string{}
	for _, e := range acc.Rows(t, eps["episodes"], "episodes") {
		byNumber[acc.Num(t, e["episode"], "episode")] = acc.Str(e["id"])
	}
	e1, e2, e3 := byNumber[1], byNumber[2], byNumber[3]
	if e1 == "" || e2 == "" || e3 == "" {
		t.Fatalf("episodes = %v", byNumber)
	}

	out := suite.Call(t, "item_set_state", map[string]any{"id": e1, "user": "alice", "watched": true})
	if acc.Str(out["user"]) != "alice" || acc.Str(out["item"]) != "Pilot" {
		t.Errorf("item_set_state = %v", out)
	}
	// only what was set is answered
	if w, _ := out["watched"].(bool); !w || out["favourite"] != nil || out["position_s"] != nil {
		t.Errorf("item_set_state = %v", out)
	}
	suite.PutBack(t, "item_set_state", map[string]any{"id": e1, "user": "alice", "watched": false})

	// per-user state on the item
	last := suite.Call(t, "item_last_watched", map[string]any{"id": e1})
	if acc.Str(last["item"]) != "Pilot" {
		t.Errorf("item_last_watched item = %v", last["item"])
	}
	state := map[string]map[string]any{}
	for _, u := range acc.Rows(t, last["users"], "users") {
		state[acc.Str(u["user"])] = u
	}
	if played, _ := state["alice"]["played"].(bool); !played {
		t.Errorf("alice's state = %v", state["alice"])
	}
	if played, _ := state["root"]["played"].(bool); played {
		t.Errorf("root's state = %v", state["root"])
	}
	// both servers count a mark as a play, and date it (Emby 4.10 does; an
	// older Emby did not), and root's untouched state has neither
	if n := acc.NumOr0(state["alice"]["play_count"]); n != 1 || acc.Str(state["alice"]["last_played"]) == "" {
		t.Errorf("alice's state after a mark = %v, want played once, with a date", state["alice"])
	}
	if state["root"]["play_count"] != nil || state["root"]["last_played"] != nil {
		t.Errorf("root's state = %v, want no play", state["root"])
	}

	// next up for alice is episode two
	next := suite.Call(t, "user_next_up", map[string]any{"user": "alice"})
	if acc.Str(next["user"]) != "alice" {
		t.Errorf("user_next_up user = %v", next["user"])
	}
	var nextIDs []string
	for _, it := range acc.Rows(t, next["next_up"], "next_up") {
		nextIDs = append(nextIDs, acc.Str(it["id"]))
	}
	if !slices.Contains(nextIDs, e2) || slices.Contains(nextIDs, e1) || slices.Contains(nextIDs, e3) {
		t.Errorf("alice's next up = %v, want episode two %s and not one or three", nextIDs, e2)
	}
	// and the pilot is not part way through: it was marked, not started
	for _, it := range acc.Rows(t, next["in_progress"], "in_progress") {
		if id := acc.Str(it["id"]); id == e1 || id == e2 {
			t.Errorf("a Breaking Bad episode is in progress after a mark: %v", it)
		}
	}
	// and root, who has marked nothing, has none of it next
	next = suite.Call(t, "user_next_up", nil)
	if acc.Str(next["user"]) != "root" {
		t.Errorf("the default user = %v", next["user"])
	}
	for _, it := range acc.Rows(t, next["next_up"], "next_up") {
		if acc.Str(it["series"]) == "Breaking Bad" {
			t.Errorf("root has Breaking Bad next: %v", it)
		}
	}

	// unmark
	out = suite.Call(t, "item_set_state", map[string]any{"id": e1, "user": "alice", "watched": false})
	if w, _ := out["watched"].(bool); w {
		t.Errorf("unmark = %v", out)
	}
	last = suite.Call(t, "item_last_watched", map[string]any{"id": e1})
	for _, u := range acc.Rows(t, last["users"], "users") {
		if played, _ := u["played"].(bool); acc.Str(u["user"]) == "alice" && played {
			t.Error("alice's episode is still played after unmarking")
		}
	}

	// a user named by id is the same user
	if out := suite.Call(t, "item_set_state", map[string]any{"id": e3, "user": os.Getenv("EMBYFIN_TEST_USER_ID"), "favourite": false}); acc.Str(out["user"]) != "alice" {
		t.Errorf("alice by id = %v", out)
	}

	if msg := suite.CallErr(t, "item_set_state", map[string]any{"id": e1, "user": "nobody", "watched": true}); !strings.Contains(msg, "nobody") {
		t.Errorf("an unknown user: %s", msg)
	}
	for want, args := range map[string]map[string]any{
		"nothing to set":                 {"id": e1, "user": "alice"},
		"contradict":                     {"id": e1, "user": "alice", "watched": true, "position_s": 1},
		"no item with id " + unknownID(): {"id": unknownID(), "user": "alice", "watched": true},
	} {
		if msg := suite.CallErr(t, "item_set_state", args); !strings.Contains(msg, want) {
			t.Errorf("item_set_state %v: %s", args, msg)
		}
	}
}

func TestFavourites(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Arrival")
	out := suite.Call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "favourite": true})
	if f, _ := out["favourite"].(bool); !f || acc.Str(out["user"]) != "alice" || acc.Str(out["item"]) != "Arrival" || out["watched"] != nil {
		t.Errorf("item_set_state = %v", out)
	}
	t.Cleanup(func() {
		suite.Undo(t, "item_set_state", map[string]any{"id": id, "user": "alice", "favourite": false})
	})

	// the favourites are library_items in the user's view. Emby keys watch
	// state by provider id, so favouriting the clean Arrival favourites the
	// messy copy with it; Jellyfin keys it by item
	favs := suite.Call(t, "library_items", map[string]any{"user": "alice", "watched": "favourite"})
	var paths []string
	for _, it := range acc.Rows(t, favs["items"], "items") {
		if acc.Str(it["name"]) != "Arrival" {
			t.Errorf("alice's favourites hold %v", it["name"])
		}
		paths = append(paths, acc.Str(it["path"]))
	}
	slices.Sort(paths)
	want := []string{"/media/messy-movies/Arrival (2016)/Arrival (2016).mp4", "/media/movies/Arrival (2016)/Arrival (2016).mp4"}
	if isJellyfin() {
		want = want[1:]
	}
	if !slices.Equal(paths, want) {
		t.Errorf("alice's favourites = %v, want %v", paths, want)
	}
	if acc.Num(t, favs["total"], "total") != len(paths) {
		t.Errorf("total %v for %d favourites", favs["total"], len(paths))
	}
	// root has none
	favs = suite.Call(t, "library_items", map[string]any{"watched": "favourite"})
	if n := len(acc.Rows(t, favs["items"], "items")); n != 0 {
		t.Errorf("root has %d favourites", n)
	}

	out = suite.Call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "favourite": false})
	if f, _ := out["favourite"].(bool); f {
		t.Errorf("unfavourite = %v", out)
	}
	favs = suite.Call(t, "library_items", map[string]any{"user": "alice", "watched": "favourite"})
	if n := len(acc.Rows(t, favs["items"], "items")); n != 0 {
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
			suite.Undo(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": false})
		}
	})
	var out map[string]any
	history := map[string]map[string]any{}
	// the servers write the log a moment after the play is reported, so each
	// play is waited for until its last event is the one just made
	since := func(id, event string, at time.Time) bool {
		out = suite.Call(t, "user_history", map[string]any{"user": "alice", "days": 7})
		clear(history)
		for _, it := range acc.Rows(t, out["items"], "items") {
			history[acc.Str(it["id"])] = it
		}
		row := history[id]
		return row != nil && acc.Str(row["event"]) == event && !stamp(t, row["last_played"]).Before(at)
	}
	// the server's clock and this one's may disagree by a moment
	began := time.Now().Add(-time.Minute)
	playThrough(t, token, aliens)
	if !acc.Eventually(func() bool { return since(aliens, "stop", began) }) {
		t.Fatalf("alice's history = %v, want Aliens played through a moment ago", out["items"])
	}
	// a second on, so the two plays' entries cannot be put in either order
	time.Sleep(1100 * time.Millisecond)
	playing := startPlaying(t, token, blade)
	if !acc.Eventually(func() bool { return since(blade, "start", began) }) {
		t.Fatalf("alice's history = %v, want Blade Runner begun a moment ago", out["items"])
	}
	if acc.Str(out["user"]) != "alice" {
		t.Errorf("user_history user = %v", out["user"])
	}
	// the last thing each did: Aliens played through, Blade Runner begun
	for id, want := range map[string]string{aliens: "stop", blade: "start"} {
		row := history[id]
		if acc.Str(row["event"]) != want || acc.Str(row["type"]) != "Movie" {
			t.Errorf("%v in alice's history = event %v, want %s", row["name"], row["event"], want)
		}
		if at := stamp(t, row["last_played"]); at.Before(began) || at.After(time.Now().Add(time.Minute)) {
			t.Errorf("%v was last played %v, want a moment ago", row["name"], at)
		}
	}
	// each item once, and a total that is every row
	total := acc.Num(t, out["total"], "total")
	if total != len(acc.Rows(t, out["items"], "items")) || acc.Num(t, out["offset"], "offset") != 0 || total != len(history) {
		t.Errorf("total %v offset %v for %d items (%d distinct)", out["total"], out["offset"], len(acc.Rows(t, out["items"], "items")), len(history))
	}
	// the whole week was read, which the answer says
	if complete, ok := out["complete"].(bool); !ok || !complete || out["note"] != nil {
		t.Errorf("complete = %v, note %v: a week of a test server's log is read whole", out["complete"], out["note"])
	}
	// most recent first: Blade Runner was begun after Aliens finished
	if first := acc.Rows(t, out["items"], "items")[0]; acc.Str(first["id"]) != blade {
		t.Errorf("the most recent in alice's history = %v, want Blade Runner", first["name"])
	}
	// a page of one is one, and the total is still the whole
	if page := suite.Call(t, "user_history", map[string]any{"user": "alice", "days": 7, "limit": 1}); len(acc.Rows(t, page["items"], "items")) != 1 || acc.Num(t, page["total"], "total") != total {
		t.Errorf("limit 1 = %v", page)
	}
	// a page past the end is empty, and says where it starts
	past := suite.Call(t, "user_history", map[string]any{"user": "alice", "days": 7, "offset": 1000})
	if n := len(acc.Rows(t, past["items"], "items")); n != 0 || acc.Num(t, past["offset"], "offset") != 1000 || acc.Num(t, past["total"], "total") != total {
		t.Errorf("past the end = %d items at offset %v of %v", n, past["offset"], past["total"])
	}
	// root has played nothing
	out = suite.Call(t, "user_history", nil)
	if acc.Str(out["user"]) != "root" || acc.Num(t, out["total"], "total") != 0 || len(acc.Rows(t, out["items"], "items")) != 0 {
		t.Errorf("the default user's history = %v", out)
	}
	if msg := suite.CallErr(t, "user_history", map[string]any{"user": "nobody"}); !strings.Contains(msg, "nobody") {
		t.Errorf("an unknown user: %s", msg)
	}
	playing.stop(0)

	// an item's history: Aliens has alice's playback, and Princess Mononoke,
	// marked watched and never played, has none
	hist := suite.Call(t, "server_activity", map[string]any{"item": aliens, "days": 7})
	if entries := summaries(t, hist); acc.Str(hist["item"]) != "Aliens" || !slices.ContainsFunc(entries, func(e string) bool { return strings.Contains(e, "alice") && strings.Contains(e, "Aliens") }) || hist["complete"] != true {
		t.Errorf("server_activity about Aliens = %v", hist)
	}
	// each entry about it names it by id, and the count is of them
	if entries := acc.Rows(t, hist["entries"], "entries"); len(entries) == 0 || acc.Num(t, hist["total"], "total") != len(entries) || slices.ContainsFunc(entries, func(e map[string]any) bool { return acc.Str(e["item_id"]) != aliens }) {
		t.Errorf("server_activity about Aliens = %v, want every entry carrying its id and total counting them", hist)
	}
	mononoke := findItem(t, "Movies", "Movie", "Princess Mononoke")
	suite.Call(t, "item_set_state", map[string]any{"id": mononoke, "user": "alice", "watched": true})
	t.Cleanup(func() {
		suite.Undo(t, "item_set_state", map[string]any{"id": mononoke, "user": "alice", "watched": false})
	})
	hist = suite.Call(t, "server_activity", map[string]any{"item": mononoke, "days": 7})
	if acc.Str(hist["item"]) != "Princess Mononoke" || len(acc.Rows(t, hist["entries"], "entries")) != 0 || acc.Num(t, hist["total"], "total") != 0 || hist["complete"] != true {
		t.Errorf("server_activity about Princess Mononoke = %v", hist)
	}
	if msg := suite.CallErr(t, "server_activity", map[string]any{"item": unknownID()}); !strings.Contains(msg, "no item with id "+unknownID()) {
		t.Errorf("an unknown item: %s", msg)
	}
	if msg := suite.CallErr(t, "server_activity", map[string]any{"user": "nobody"}); !strings.Contains(msg, "nobody") {
		t.Errorf("an unknown user: %s", msg)
	}
}

// summaries are the lines of a server_activity answer, as the log wrote them.
func summaries(t *testing.T, out map[string]any) []string {
	t.Helper()

	var lines []string
	for _, e := range acc.Rows(t, out["entries"], "entries") {
		lines = append(lines, acc.Str(e["summary"]))
	}

	return lines
}

// An item's history is its own: Dune: Part Two's title holds Dune's, so a
// play of it must not be put down to Dune.
func TestWatchHistoryOfATitleInsideAnother(t *testing.T) {
	_, token := signInPlayer(t)
	dune := findItem(t, "Movies", "Movie", "Dune")
	partTwo := findItem(t, "Movies", "Movie", "Dune: Part Two")
	t.Cleanup(func() {
		suite.Undo(t, "item_set_state", map[string]any{"id": partTwo, "user": "alice", "watched": false})
	})
	playThrough(t, token, partTwo)

	mentions := func(id string) bool {
		return slices.ContainsFunc(summaries(t, suite.Call(t, "server_activity", map[string]any{"item": id, "days": 1})), func(e string) bool {
			return strings.Contains(e, "Dune: Part Two")
		})
	}
	if !acc.Eventually(func() bool { return mentions(partTwo) }) {
		t.Fatalf("Dune: Part Two's history never showed its play: %v", suite.Call(t, "server_activity", map[string]any{"item": partTwo, "days": 1}))
	}
	if mentions(dune) {
		t.Errorf("Dune's history holds Dune: Part Two's play: %v", suite.Call(t, "server_activity", map[string]any{"item": dune, "days": 1})["entries"])
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
	if !acc.Eventually(func() bool {
		theirs = names(t, suite.Call(t, "user_history", map[string]any{"user": name, "days": 1})["items"], "items")
		return slices.Contains(theirs, "Arrival")
	}) {
		t.Fatalf("%s's history = %v, want the Arrival just played", name, theirs)
	}
	for _, it := range acc.Rows(t, suite.Call(t, "user_history", map[string]any{"user": "alice", "days": 1})["items"], "items") {
		if acc.Str(it["id"]) == arrival {
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
	noLeftoverLibraries(t)
	root := suite.Call(t, "user_get", nil)
	if acc.Str(root["name"]) != "root" || acc.Str(root["id"]) != os.Getenv("EMBYFIN_TEST_ADMIN_ID") || !acc.BoolOf(root["admin"]) || !acc.BoolOf(root["all_libraries"]) || !acc.BoolOf(root["has_password"]) || acc.BoolOf(root["disabled"]) {
		t.Errorf("user_get (the default) = %v", root)
	}
	// an administrator may delete and connect from anywhere; Jellyfin keeps
	// its accounts off the login screen from the start, Emby shows them
	if !acc.BoolOf(root["can_delete"]) || !acc.BoolOf(root["remote_access"]) || acc.BoolOf(root["hidden"]) != isJellyfin() {
		t.Errorf("root's permissions = can_delete %v remote_access %v hidden %v", root["can_delete"], root["remote_access"], root["hidden"])
	}
	// every library, and the folders the servers keep collections and
	// playlists in once there are any
	libs := slices.DeleteFunc(acc.Strs(t, root["libraries"], "libraries"), func(name string) bool { return serverFolders[name] != "" })
	if !slices.Equal(acc.Sorted(libs), []string{"Messy Movies", "Messy Shows", "Movies", "Music", "Shows"}) {
		t.Errorf("root's libraries = %v", root["libraries"])
	}
	// root signed in to set the server up
	if at := stamp(t, root["last_login"]); at.After(time.Now().Add(time.Minute)) {
		t.Errorf("root last logged in at %v", at)
	}

	alice := suite.Call(t, "user_get", map[string]any{"user": "ALICE"})
	if acc.Str(alice["name"]) != "alice" || acc.BoolOf(alice["admin"]) || acc.Str(alice["id"]) != os.Getenv("EMBYFIN_TEST_USER_ID") || acc.BoolOf(alice["can_delete"]) {
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
	if at := stamp(t, suite.Call(t, "user_get", map[string]any{"user": "alice"})["last_login"]); at.Before(began) {
		t.Errorf("alice last logged in at %v, before the sign-in a moment ago", at)
	}

	if msg := suite.CallErr(t, "user_get", map[string]any{"user": "nobody"}); !strings.Contains(msg, "nobody") {
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

	before := suite.Call(t, "user_get", map[string]any{"user": "alice"})
	hidden := !acc.BoolOf(before["hidden"])
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

	got := suite.Call(t, "user_get", map[string]any{"user": "alice"})
	if acc.BoolOf(got["hidden"]) != hidden || !acc.BoolOf(got["disabled"]) || !acc.BoolOf(got["can_delete"]) || acc.BoolOf(got["remote_access"]) {
		t.Errorf("alice's permissions = hidden %v disabled %v can_delete %v remote_access %v, want %v, true, true and false",
			got["hidden"], got["disabled"], got["can_delete"], got["remote_access"], hidden)
	}
	if acc.Str(got["audio_language"]) != "jpn" || acc.Str(got["subtitle_language"]) != "eng" || acc.Str(got["subtitle_mode"]) != "Always" || acc.BoolOf(got["play_default_audio_track"]) {
		t.Errorf("alice's playback = audio %v subtitles %v mode %v default track %v, want jpn, eng, Always and false",
			got["audio_language"], got["subtitle_language"], got["subtitle_mode"], got["play_default_audio_track"])
	}
	// and before, none of it: the defaults read as the defaults
	if before["audio_language"] != nil || before["subtitle_language"] != nil || acc.BoolOf(before["disabled"]) || !acc.BoolOf(before["play_default_audio_track"]) {
		t.Errorf("alice before = %v", before)
	}
}

// item_set_state with a position puts an item in progress; user_next_up
// lists it among in_progress with where it resumes, user_stats counts it;
// marking it unwatched clears it.
func TestProgress(t *testing.T) {
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	before := suite.Call(t, "user_stats", map[string]any{"user": "alice"})
	out := suite.Call(t, "item_set_state", map[string]any{"id": arrival, "user": "alice", "position_s": 2550})
	if acc.Str(out["item"]) != "Arrival" || acc.Str(out["user"]) != "alice" || acc.Num(t, out["position_s"], "position_s") != 2550 || out["watched"] != nil {
		t.Errorf("item_set_state = %v", out)
	}
	t.Cleanup(func() {
		suite.Undo(t, "item_set_state", map[string]any{"id": arrival, "user": "alice", "watched": false})
	})
	if after := suite.Call(t, "user_stats", map[string]any{"user": "alice"}); acc.Num(t, after["in_progress"], "in_progress") != acc.Num(t, before["in_progress"], "in_progress")+1 {
		t.Errorf("user_stats in_progress went from %v to %v, want one more", before["in_progress"], after["in_progress"])
	}

	inProgress := func() map[string]any {
		for _, it := range acc.Rows(t, suite.Call(t, "user_next_up", map[string]any{"user": "alice"})["in_progress"], "in_progress") {
			if acc.Str(it["id"]) == arrival {
				return it
			}
		}
		return nil
	}
	var row map[string]any
	if !acc.Eventually(func() bool { row = inProgress(); return row != nil }) {
		t.Fatal("Arrival is not in progress for alice")
	}
	// 2550 seconds of the film's 116 minutes is 36.6% of it, which the
	// server's percentage gives whole
	if acc.Num(t, row["position_s"], "position_s") != 2550 || acc.Num(t, row["percent"], "percent") != 36 || acc.Str(row["name"]) != "Arrival" || acc.Str(row["type"]) != "Movie" {
		t.Errorf("in progress row = %v", row)
	}
	// the resume point reads back in the same unit through item_last_watched
	resumed := false
	for _, u := range acc.Rows(t, suite.Call(t, "item_last_watched", map[string]any{"id": arrival})["users"], "users") {
		if acc.Str(u["user"]) == "alice" {
			resumed = acc.Num(t, u["resume_s"], "resume_s") == 2550
		}
	}
	if !resumed {
		t.Error("item_last_watched does not show alice's resume point at 2550 seconds")
	}

	// a new resume point with watched false keeps the point: unplayed is what
	// a resume point already is, and marking it so again would clear it
	out = suite.Call(t, "item_set_state", map[string]any{"id": arrival, "user": "alice", "position_s": 40, "watched": false})
	if acc.Num(t, out["position_s"], "position_s") != 40 || acc.BoolOf(out["watched"]) || out["watched"] == nil {
		t.Errorf("a position with watched false = %v", out)
	}
	if !acc.Eventually(func() bool {
		row = inProgress()
		return row != nil && acc.Num(t, row["position_s"], "position_s") == 40
	}) {
		t.Errorf("after moving the resume point to 40 seconds with watched false, alice's Arrival = %v", row)
	}

	// root has nothing in progress
	if n := len(acc.Rows(t, suite.Call(t, "user_next_up", nil)["in_progress"], "in_progress")); n != 0 {
		t.Errorf("root has %d in progress", n)
	}

	// marking it unwatched clears the resume point
	suite.Call(t, "item_set_state", map[string]any{"id": arrival, "user": "alice", "watched": false})
	if !acc.Holds(func() bool { return inProgress() == nil }) {
		t.Errorf("Arrival is still in progress after marking it unwatched: %v", inProgress())
	}

	if msg := suite.CallErr(t, "item_set_state", map[string]any{"id": arrival, "position_s": 0}); !strings.Contains(msg, "above zero") {
		t.Errorf("a zero position: %s", msg)
	}
	if n := acc.Num(t, suite.Call(t, "user_stats", map[string]any{"user": "alice"})["in_progress"], "in_progress"); n != acc.Num(t, before["in_progress"], "in_progress") {
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
	suite.PutBack(t, "item_set_state", map[string]any{"id": film, "user": "alice", "watched": false})
	_, token := signInPlayer(t)
	began := time.Now().Add(-time.Minute)
	playThrough(t, token, film)
	if !acc.Eventually(func() bool {
		for _, u := range acc.Rows(t, suite.Call(t, "item_last_watched", map[string]any{"id": film})["users"], "users") {
			if acc.Str(u["user"]) == "alice" && acc.BoolOf(u["played"]) {
				return true
			}
		}
		return false
	}) {
		t.Fatal("the play through never marked The Thirteenth Floor played for alice")
	}
	suite.Call(t, "item_set_state", map[string]any{"id": film, "user": "alice", "position_s": 1})

	var row map[string]any
	if !acc.Eventually(func() bool {
		row = nil
		for _, it := range acc.Rows(t, suite.Call(t, "user_next_up", map[string]any{"user": "alice"})["in_progress"], "in_progress") {
			if acc.Str(it["id"]) == film {
				row = it
			}
		}
		return row != nil
	}) {
		t.Fatal("The Thirteenth Floor is not in progress for alice")
	}
	if acc.Num(t, row["position_s"], "position_s") != 1 || acc.Str(row["name"]) != "The Thirteenth Floor" {
		t.Errorf("in progress row = %v", row)
	}
	// on both servers: Emby's resume list carries the date only when asked
	// for it, and the tool asks
	if at := stamp(t, row["last_played"]); at.Before(began) || at.After(time.Now().Add(time.Minute)) {
		t.Errorf("last played %v, want a moment ago", at)
	}
}

// A play stopped part way through a film long enough to have a resume point
// - Limitless runs its 106 minutes, and Jellyfin keeps none in a file under
// five - leaves it in progress where it stopped, and not played: on both
// servers, from the player's own report rather than a state set by hand.
func TestAPlayStoppedPartWay(t *testing.T) {
	film := findItem(t, "Movies", "Movie", "Limitless")
	suite.PutBack(t, "item_set_state", map[string]any{"id": film, "user": "alice", "watched": false})
	_, token := signInPlayer(t)
	playTo(t, token, film, 10*60*10_000_000) // ten minutes in

	var row map[string]any
	if !acc.Eventually(func() bool {
		row = nil
		for _, it := range acc.Rows(t, suite.Call(t, "user_next_up", map[string]any{"user": "alice"})["in_progress"], "in_progress") {
			if acc.Str(it["id"]) == film {
				row = it
			}
		}
		return row != nil
	}) {
		t.Fatal("Limitless, stopped ten minutes in, is not in progress for alice")
	}
	if acc.Num(t, row["position_s"], "position_s") != 600 || acc.Num(t, row["percent"], "percent") != 9 {
		t.Errorf("in progress row = %v, want ten minutes in, 9%% of 106", row)
	}
	for _, u := range acc.Rows(t, suite.Call(t, "item_last_watched", map[string]any{"id": film})["users"], "users") {
		if acc.Str(u["user"]) == "alice" && acc.BoolOf(u["played"]) {
			t.Errorf("alice's row = %v, want it not played", u)
		}
	}
}

func TestUserStats(t *testing.T) {
	mononoke := findItem(t, "Movies", "Movie", "Princess Mononoke")
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	eps := acc.Rows(t, suite.Call(t, "library_episodes", map[string]any{"series_id": series})["episodes"], "episodes")
	if len(eps) != 3 {
		t.Fatalf("Breaking Bad has %d episodes, want 3", len(eps))
	}
	watch := []string{mononoke, acc.Str(eps[0]["id"]), acc.Str(eps[1]["id"])}
	for _, id := range watch {
		suite.Call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": true})
	}
	// and one favourited, and one part way through, in the same sweep
	suite.Call(t, "item_set_state", map[string]any{"id": mononoke, "user": "alice", "favourite": true})
	third := acc.Str(eps[2]["id"])
	suite.Call(t, "item_set_state", map[string]any{"id": third, "user": "alice", "position_s": 1})
	t.Cleanup(func() {
		for _, id := range append(watch, third) {
			suite.Undo(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": false})
		}
		suite.Undo(t, "item_set_state", map[string]any{"id": mononoke, "user": "alice", "favourite": false})
	})

	out := suite.Call(t, "user_stats", map[string]any{"user": "alice", "library": "Movies"})
	if acc.Str(out["user"]) != "alice" || acc.Num(t, out["movies_watched"], "movies_watched") != 1 || acc.Num(t, out["episodes_watched"], "episodes_watched") != 0 {
		t.Errorf("alice in Movies = %v", out)
	}
	if acc.Num(t, out["favourites"], "favourites") != 1 || acc.Num(t, out["in_progress"], "in_progress") != 0 {
		t.Errorf("alice in Movies = %v, want 1 favourite and nothing in progress", out)
	}
	if out = suite.Call(t, "user_stats", map[string]any{"user": "alice", "library": "Shows"}); acc.Num(t, out["in_progress"], "in_progress") != 1 || acc.Num(t, out["favourites"], "favourites") != 0 {
		t.Errorf("alice in Shows = %v, want 1 in progress and no favourite", out)
	}

	out = suite.Call(t, "user_stats", map[string]any{"user": "alice"})
	if acc.Num(t, out["movies_watched"], "movies_watched") < 1 || acc.Num(t, out["episodes_watched"], "episodes_watched") < 2 || acc.Num(t, out["series_started"], "series_started") < 1 {
		t.Errorf("alice everywhere = %v", out)
	}
	var bb map[string]any
	for _, s := range acc.Rows(t, out["top_series"], "top_series") {
		if acc.Str(s["name"]) == "Breaking Bad" {
			bb = s
		}
	}
	// Breaking Bad has a third episode on disk
	if bb == nil || acc.Num(t, bb["episodes_watched"], "episodes_watched") != 2 || acc.BoolOf(bb["finished"]) {
		t.Errorf("Breaking Bad in top_series = %v", out["top_series"])
	}
	genres := valueCounts(t, out["top_genres"], "top_genres")
	if genres["Animation"] < 1 || genres["Drama"] < 1 {
		t.Errorf("top genres = %v, want Princess Mononoke's Animation and Breaking Bad's Drama", genres)
	}

	// root has watched nothing
	root := suite.Call(t, "user_stats", nil)
	if acc.Num(t, root["movies_watched"], "movies_watched") != 0 || acc.Num(t, root["series_started"], "series_started") != 0 || len(acc.Rows(t, root["top_series"], "top_series")) != 0 {
		t.Errorf("root's stats = %v", root)
	}
	if acc.Num(t, root["favourites"], "favourites") != 0 || acc.Num(t, root["in_progress"], "in_progress") != 0 {
		t.Errorf("root's stats = %v", root)
	}
	if msg := suite.CallErr(t, "user_stats", map[string]any{"user": "alice", "library": "Nope"}); !strings.Contains(msg, `no library named "Nope"`) {
		t.Errorf("an unknown library: %s", msg)
	}
}

// Hours are the runtime of what was watched, and the most played what was
// played, a mark counting as a play: .hack//Liminality's two three-minute
// episodes marked watched and its one-second last played through are six
// minutes, a tenth of an hour, and three episodes played once each.
func TestUserStatsHoursAndPlays(t *testing.T) {
	byNumber := map[int]string{}
	for _, it := range acc.Rows(t, suite.Call(t, "library_items", map[string]any{"library": "Messy Shows", "types": "Episode", "query": "In the Case of", "limit": 10})["items"], "items") {
		if acc.Str(it["series"]) == ".hack//Liminality" {
			byNumber[acc.Num(t, it["episode"], "episode")] = acc.Str(it["id"])
		}
	}
	if len(byNumber) != 3 {
		t.Fatalf(".hack//Liminality's episodes = %v", byNumber)
	}
	t.Cleanup(func() {
		for _, id := range byNumber {
			suite.Undo(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": false})
		}
	})
	stats := func() map[string]any {
		return suite.Call(t, "user_stats", map[string]any{"user": "alice", "library": "Messy Shows"})
	}
	if before := stats(); acc.Decimal(t, before["hours_watched"], "hours_watched") != 0 || len(acc.Rows(t, before["most_played"], "most_played")) != 0 {
		t.Fatalf("alice in Messy Shows before = %v, want nothing watched", before)
	}

	for _, n := range []int{1, 2} {
		suite.Call(t, "item_set_state", map[string]any{"id": byNumber[n], "user": "alice", "watched": true})
	}
	_, token := signInPlayer(t)
	playThrough(t, token, byNumber[3])

	var after map[string]any
	if !acc.Eventually(func() bool { after = stats(); return acc.Num(t, after["episodes_watched"], "episodes_watched") == 3 }) {
		t.Fatalf("alice in Messy Shows = %v, want three episodes watched", after)
	}
	// six minutes and a second
	if h := acc.Decimal(t, after["hours_watched"], "hours_watched"); h != 0.1 {
		t.Errorf("hours watched = %v, want 0.1", h)
	}
	if s := acc.Rows(t, after["top_series"], "top_series"); len(s) != 1 || acc.Str(s[0]["name"]) != ".hack//Liminality" || !acc.BoolOf(s[0]["finished"]) || acc.Num(t, after["series_finished"], "series_finished") != 1 {
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
	suite.PutBack(t, "item_set_state", map[string]any{"id": film, "user": "alice", "watched": false})
	_, token := signInPlayer(t)
	playThrough(t, token, film)
	var row map[string]any
	if !acc.Eventually(func() bool {
		row = nil
		for _, p := range acc.Rows(t, suite.Call(t, "user_stats", map[string]any{"user": "alice", "library": "Movies"})["most_played"], "most_played") {
			if acc.Str(p["name"]) == "The Thirteenth Floor" {
				row = p
			}
		}
		return row != nil
	}) {
		t.Fatal("The Thirteenth Floor, played through, is not among alice's most played")
	}
	if acc.Num(t, row["plays"], "plays") < 1 || acc.Str(row["type"]) != "Movie" {
		t.Errorf("most played row = %v", row)
	}
}

// assertMostPlayed checks user_stats counts each of .hack//Liminality's
// episodes played once.
func assertMostPlayed(t *testing.T, stats map[string]any) {
	t.Helper()

	plays := map[string]int{}
	for _, p := range acc.Rows(t, stats["most_played"], "most_played") {
		plays[acc.Str(p["name"])] = acc.Num(t, p["plays"], "plays")
		if acc.Str(p["type"]) != "Episode" {
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
