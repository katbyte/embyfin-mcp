//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

// playerDevice is the device this suite signs in as, so the session tools
// have something to drive: a client that reported it can be controlled. No
// player is listening on the other end, so the commands are accepted and
// go nowhere, which is all a throwaway server can prove.
const playerDevice = "acceptance-player"

// ensurePlayer logs in as the second user from a device and reports
// media-control capabilities, the way a real client does, and returns the
// session's device name. It goes straight to the HTTP API: this is the one
// thing a test needs that is not a tool's job.
func ensurePlayer(t *testing.T) string {
	t.Helper()

	device, _ := signInPlayer(t)

	return device
}

// signInPlayer is ensurePlayer, returning the session's access token as well,
// for a test that reports playback as the player.
func signInPlayer(t *testing.T) (string, string) {
	t.Helper()

	return playerDevice, signIn(t, os.Getenv("EMBYFIN_TEST_USER"), os.Getenv("EMBYFIN_TEST_PASSWORD"), playerDevice)
}

// signIn logs a user in from a device that says it can be controlled, and
// returns the session's access token.
func signIn(t *testing.T, user, password, device string) string {
	t.Helper()

	if !ready {
		t.Skip("EMBYFIN_BACKEND, EMBYFIN_SERVER and EMBYFIN_TOKEN are not set")
	}
	server := os.Getenv("EMBYFIN_SERVER")
	auth := fmt.Sprintf(`MediaBrowser Client="embyfin-mcp acceptance", Device=%q, DeviceId=%q, Version="0"`, device, device)
	body, _ := json.Marshal(map[string]string{"Username": user, "Pw": password})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server+"/Users/AuthenticateByName", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth)
	req.Header.Set("X-Emby-Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login as %s from %s: HTTP %d: %s", user, device, resp.StatusCode, raw)
	}
	var login struct {
		AccessToken string
	}
	if err := json.Unmarshal(raw, &login); err != nil || login.AccessToken == "" {
		t.Fatalf("login response: %s", raw)
	}

	q := url.Values{}
	q.Set("SupportsMediaControl", "true")
	q.Set("PlayableMediaTypes", "Video")
	q.Set("SupportedCommands", "DisplayMessage")
	if status, raw := apiAs(t, http.MethodPost, "/Sessions/Capabilities?"+q.Encode(), login.AccessToken, device, nil); status/100 != 2 {
		t.Fatalf("posting capabilities: HTTP %d: %s", status, raw)
	}

	return login.AccessToken
}

// signOut ends the session a token signed in, so a device a test made up is
// not left among the sessions for the tests after it to trip over.
func signOut(t *testing.T, token, device string) {
	t.Helper()

	if status, raw := apiAs(t, http.MethodPost, "/Sessions/Logout", token, device, nil); status/100 != 2 {
		t.Errorf("signing %s out: HTTP %d: %s", device, status, raw)
	}
}

// api calls the server's HTTP API directly with a token (the suite's API key
// when empty) and returns the status and body: for what a test must set up or
// put back that no tool does, such as a user's library access or a client
// reporting playback.
func api(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()

	return apiAs(t, method, path, token, playerDevice, body)
}

// apiAs is api as a particular device, for a token signed in from another
// one than the player.
func apiAs(t *testing.T, method, path, token, device string, body any) (int, []byte) {
	t.Helper()

	if !ready {
		t.Skip("EMBYFIN_BACKEND, EMBYFIN_SERVER and EMBYFIN_TOKEN are not set")
	}
	// the API key goes as the tools send it; a player token with the
	// player's device, so the server keeps it on that session
	auth := fmt.Sprintf(`MediaBrowser Client="embyfin-mcp acceptance", Device=%q, DeviceId=%q, Version="0", Token=%q`, device, device, token)
	if token == "" {
		token = os.Getenv("EMBYFIN_TOKEN")
		auth = fmt.Sprintf(`MediaBrowser Token=%q`, token)
	}
	var r io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(raw)
	}
	// without the test's cancellation, so a cleanup can put things back
	req, err := http.NewRequestWithContext(context.WithoutCancel(t.Context()), method, os.Getenv("EMBYFIN_SERVER")+path, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Token", token)
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, raw
}

// playSession is a play of one item a client has started and not stopped:
// what a session shows while something is on screen.
type playSession struct {
	t                   *testing.T
	token, device, item string
	playSessionID       string
}

// startPlaying starts a play of an item as alice's player, the way a client
// does, and stops it when the test ends if the test has not.
func startPlaying(t *testing.T, token, item string) *playSession {
	t.Helper()

	return startPlayingAs(t, token, playerDevice, os.Getenv("EMBYFIN_TEST_USER_ID"), item)
}

// startPlayingAs is startPlaying as another user's device.
func startPlayingAs(t *testing.T, token, device, userID, item string) *playSession {
	t.Helper()

	// Emby answers a report without the PlaySessionId PlaybackInfo hands out
	// with a 400
	status, raw := apiAs(t, http.MethodPost, "/Items/"+item+"/PlaybackInfo?UserId="+userID, token, device, map[string]any{})
	var info struct {
		PlaySessionID string `json:"PlaySessionId"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &info) != nil {
		t.Fatalf("PlaybackInfo: HTTP %d: %.200s", status, raw)
	}
	p := &playSession{t: t, token: token, device: device, item: item, playSessionID: info.PlaySessionID}
	p.report("/Sessions/Playing", 0, false)
	// a stop the test already sent is answered either way, which is no
	// failure of the test's
	t.Cleanup(func() {
		body := map[string]any{"ItemId": item, "PlaySessionId": p.playSessionID, "PositionTicks": 0, "PlayMethod": "DirectPlay"}
		_, _ = apiAs(t, http.MethodPost, "/Sessions/Playing/Stopped", token, device, body)
	})

	return p
}

// report tells the server where the play is, and whether it is paused.
func (p *playSession) report(path string, ticks int64, paused bool) {
	p.t.Helper()

	body := map[string]any{"ItemId": p.item, "PlaySessionId": p.playSessionID, "PositionTicks": ticks, "IsPaused": paused, "CanSeek": true, "PlayMethod": "DirectPlay"}
	if status, raw := apiAs(p.t, http.MethodPost, path, p.token, p.device, body); status/100 != 2 {
		p.t.Errorf("%s: HTTP %d: %s", path, status, raw)
	}
}

// stop ends the play at a position.
func (p *playSession) stop(ticks int64) {
	p.t.Helper()

	p.report("/Sessions/Playing/Progress", ticks, false)
	p.report("/Sessions/Playing/Stopped", ticks, false)
}

// sessionOn is the session_list row for a device, nil when it has none.
func sessionOn(t *testing.T, device string) map[string]any {
	t.Helper()

	for _, s := range rows(t, call(t, "session_list", nil)["sessions"], "sessions") {
		if str(s["device"]) == device {
			return s
		}
	}

	return nil
}

func TestSessions(t *testing.T) {
	device := ensurePlayer(t)

	out := call(t, "session_list", nil)
	var player map[string]any
	for _, s := range rows(t, out["sessions"], "sessions") {
		if str(s["device"]) == device {
			player = s
		}
		if str(s["id"]) == "" || str(s["device"]) == "" {
			t.Errorf("session row = %v", s)
		}
	}
	if player == nil {
		t.Fatalf("the player is not among the sessions: %v", out["sessions"])
	}
	if str(player["user"]) != "alice" || str(player["app"]) != "embyfin-mcp acceptance" {
		t.Errorf("the player's user and app = %v and %v, want alice and embyfin-mcp acceptance", player["user"], player["app"])
	}
	// nothing is playing on it, so nothing about a play is said
	if player["now_playing"] != nil || player["position"] != nil || player["paused"] != nil {
		t.Errorf("an idle player = %v", player)
	}

	// a message, to the device by name (a case-insensitive substring)
	msg := call(t, "session_message", map[string]any{"session": "ACCEPTANCE", "text": "dinner is ready", "header": "Kitchen", "timeout_ms": 1000})
	if str(msg["sent_to"]) != device {
		t.Errorf("session_message = %v", msg)
	}

	// play something on it: by session id, by device name, and in each mode
	dune := findItem(t, "Movies", "Movie", "Dune")
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	play := call(t, "session_play", map[string]any{"session": str(player["id"]), "item_ids": []any{dune}})
	if str(play["playing_on"]) != device {
		t.Errorf("session_play = %v", play)
	}
	for _, mode := range []string{"PlayNext", "PlayLast"} {
		if play := call(t, "session_play", map[string]any{"session": device, "item_ids": []any{dune}, "mode": mode}); str(play["playing_on"]) != device {
			t.Errorf("session_play %s = %v", mode, play)
		}
	}
	// several items at once, in order
	if play := call(t, "session_play", map[string]any{"session": device, "item_ids": []any{dune, arrival}}); str(play["playing_on"]) != device {
		t.Errorf("session_play of two items = %v", play)
	}

	// and drive it
	for _, cmd := range []string{"Pause", "Unpause", "PlayPause", "NextTrack", "PreviousTrack", "Stop"} {
		out := call(t, "session_command", map[string]any{"session": device, "command": cmd})
		if str(out["sent"]) != cmd+" → "+device {
			t.Errorf("session_command %s = %v", cmd, out)
		}
	}
	out = call(t, "session_command", map[string]any{"session": device, "command": "Seek", "seek_s": 60})
	if str(out["sent"]) != "Seek → "+device {
		t.Errorf("session_command Seek = %v", out)
	}
	// back to the start: a position of 0 is a position, and both servers take
	// it (no player is listening, so that the server took it is all a test
	// server can show)
	out = call(t, "session_command", map[string]any{"session": device, "command": "Seek", "seek_s": 0})
	if str(out["sent"]) != "Seek → "+device {
		t.Errorf("session_command Seek to 0 = %v", out)
	}

	if e := callErr(t, "session_message", map[string]any{"session": "no such device", "text": "x"}); !strings.Contains(e, "no such device") || !strings.Contains(e, device) {
		t.Errorf("an unknown session should list the real ones: %s", e)
	}
}

// What the session tools refuse, and why: a name that is no session's, or
// nobody's at all, and a mode or a command the servers do not have. Each is
// refused before anything reaches a device.
func TestSessionRefusals(t *testing.T) {
	device := ensurePlayer(t)
	dune := findItem(t, "Movies", "Movie", "Dune")

	// an empty name is a part of every device's name, which would drive
	// whichever device the server lists first
	for _, session := range []string{"", "   "} {
		if msg := callErr(t, "session_command", map[string]any{"session": session, "command": "Pause"}); !strings.Contains(msg, "session is required") {
			t.Errorf("session %q: %s", session, msg)
		}
	}

	// the servers type the mode and the command, and refuse one they do not
	// have rather than sending it
	if msg := callErr(t, "session_play", map[string]any{"session": device, "item_ids": []any{dune}, "mode": "PlaySometime"}); !strings.Contains(msg, "HTTP 400") {
		t.Errorf("an unknown mode: %s", msg)
	}
	if msg := callErr(t, "session_command", map[string]any{"session": device, "command": "Dance"}); !strings.Contains(msg, "HTTP 400") {
		t.Errorf("an unknown command: %s", msg)
	}

	// what is played is read first: both servers take a play of nothing, and
	// Jellyfin one of an id it does not hold, and answer as if the device
	// were playing it
	if msg := callErr(t, "session_play", map[string]any{"session": device, "item_ids": []any{}}); !strings.Contains(msg, "item_ids is required") {
		t.Errorf("nothing to play: %s", msg)
	}
	if msg := callErr(t, "session_play", map[string]any{"session": device, "item_ids": []any{dune, unknownID()}}); !strings.Contains(msg, "no item with id "+unknownID()) {
		t.Errorf("an unknown item among known ones: %s", msg)
	}
	// an id not in the server's shape at all: Emby numbers its items, and
	// asked for any other id says so; Jellyfin finds nothing by it
	want := "no item with id not-an-id"
	if !isJellyfin() {
		want = "Unrecognized Guid format"
	}
	if msg := callErr(t, "session_play", map[string]any{"session": device, "item_ids": []any{"not-an-id"}}); !strings.Contains(msg, want) {
		t.Errorf("a malformed item id: %s, want %q", msg, want)
	}
}

// A name that two devices answer to is refused naming both, rather than
// driving whichever the server lists first; the device's whole name, or its
// session id, picks one out.
func TestSessionNamedTwice(t *testing.T) {
	first := ensurePlayer(t)
	// a second player whose device name holds the first's
	second := first + "-two"
	token := signIn(t, os.Getenv("EMBYFIN_TEST_USER"), os.Getenv("EMBYFIN_TEST_PASSWORD"), second)
	t.Cleanup(func() { signOut(t, token, second) })
	if !eventually(func() bool { return sessionOn(t, second) != nil }) {
		t.Fatalf("the second player is not among the sessions: %v", call(t, "session_list", nil)["sessions"])
	}

	msg := callErr(t, "session_message", map[string]any{"session": "player", "text": "which one?"})
	for _, want := range []string{`"player" matches 2 sessions`, first + " (", second + " (", "name one by its id"} {
		if !strings.Contains(msg, want) {
			t.Errorf("a name two devices share = %s, want it saying %q", msg, want)
		}
	}
	// a whole name matched exactly beats one that is only a part of another
	if out := call(t, "session_message", map[string]any{"session": first, "text": "the first"}); str(out["sent_to"]) != first {
		t.Errorf("the first player by its whole name = %v", out)
	}
	if out := call(t, "session_message", map[string]any{"session": strings.ToUpper(second), "text": "the second"}); str(out["sent_to"]) != second {
		t.Errorf("the second player by its whole name = %v", out)
	}
	if out := call(t, "session_message", map[string]any{"session": str(sessionOn(t, second)["id"]), "text": "by id"}); str(out["sent_to"]) != second {
		t.Errorf("the second player by its id = %v", out)
	}
}

// While a client plays something, the session says what, where it is and
// whether it is paused, and the server counts it as an active session; once
// the client stops, neither does.
func TestSessionWhilePlaying(t *testing.T) {
	device, token := signInPlayer(t)
	dune := findItem(t, "Movies", "Movie", "Dune: Part Two")
	idle := num(t, call(t, "server_stats", nil)["active_sessions"], "active_sessions")

	p := startPlaying(t, token, dune)
	// half a second into the one-second file, paused there
	p.report("/Sessions/Playing/Progress", 5_000_000, true)
	var row map[string]any
	if !eventually(func() bool {
		row = sessionOn(t, device)
		return row != nil && boolOf(row["paused"])
	}) {
		t.Fatalf("the player never showed as paused: %v", row)
	}
	if str(row["now_playing"]) != "Dune: Part Two" || str(row["position"]) != "1s / 1s" {
		t.Errorf("the playing session = %v, want Dune: Part Two, paused at 1s / 1s", row)
	}
	if n := num(t, call(t, "server_stats", nil)["active_sessions"], "active_sessions"); n != idle+1 {
		t.Errorf("active_sessions while playing = %d, want %d", n, idle+1)
	}

	// playing again, then stopped
	p.report("/Sessions/Playing/Progress", 5_000_000, false)
	if !eventually(func() bool { row = sessionOn(t, device); return row != nil && !boolOf(row["paused"]) }) {
		t.Errorf("the player never showed as playing again: %v", row)
	}
	p.report("/Sessions/Playing/Stopped", 5_000_000, false)
	if !eventually(func() bool {
		row = sessionOn(t, device)
		return row != nil && row["now_playing"] == nil
	}) {
		t.Errorf("the player still shows a play after it stopped: %v", row)
	}
	if n := num(t, call(t, "server_stats", nil)["active_sessions"], "active_sessions"); n != idle {
		t.Errorf("active_sessions after the play stopped = %d, want %d", n, idle)
	}
	// the stop marked it watched for alice, which is not this test's to keep
	t.Cleanup(func() { _, _ = invoke("item_set_state", map[string]any{"id": dune, "user": "alice", "watched": false}) })

	// the device that played remembers who it played for
	var seen bool
	for _, d := range rows(t, call(t, "server_devices", nil)["devices"], "devices") {
		if str(d["name"]) == device {
			seen = true
			if str(d["last_user"]) != "alice" || !strings.HasPrefix(str(d["app"]), "embyfin-mcp acceptance") {
				t.Errorf("the player's device = %v, want alice's embyfin-mcp acceptance", d)
			}
		}
	}
	if !seen {
		t.Errorf("the player's device is not among %v", call(t, "server_devices", nil)["devices"])
	}
}
