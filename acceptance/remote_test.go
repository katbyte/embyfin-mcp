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
	"slices"
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

	server := os.Getenv("EMBYFIN_SERVER")
	auth := fmt.Sprintf(`MediaBrowser Client="embyfin-mcp acceptance", Device=%q, DeviceId=%q, Version="0"`, playerDevice, playerDevice)
	body, _ := json.Marshal(map[string]string{"Username": os.Getenv("EMBYFIN_TEST_USER"), "Pw": os.Getenv("EMBYFIN_TEST_PASSWORD")})
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
		t.Fatalf("login as the player: HTTP %d: %s", resp.StatusCode, raw)
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
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, server+"/Sessions/Capabilities?"+q.Encode(), http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Emby-Token", login.AccessToken)
	req.Header.Set("Authorization", auth+fmt.Sprintf(", Token=%q", login.AccessToken))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("posting capabilities: HTTP %d", resp.StatusCode)
	}

	return playerDevice, login.AccessToken
}

// api calls the server's HTTP API directly with a token (the suite's API key
// when empty) and returns the status and body: for what a test must set up or
// put back that no tool does, such as a user's library access or a client
// reporting playback.
func api(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()

	// the API key goes as the tools send it; a player token with the
	// player's device, so the server keeps it on that session
	auth := fmt.Sprintf(`MediaBrowser Client="embyfin-mcp acceptance", Device=%q, DeviceId=%q, Version="0", Token=%q`, playerDevice, playerDevice, token)
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
	if str(player["user"]) != "alice" {
		t.Errorf("the player's user = %v, want alice", player["user"])
	}
	if str(player["now_playing"]) != "" {
		t.Errorf("the player is playing %v", player["now_playing"])
	}

	// a message, to the device by name (a case-insensitive substring)
	msg := call(t, "session_message", map[string]any{"session": "ACCEPTANCE", "text": "dinner is ready", "header": "Kitchen", "timeout_ms": 1000})
	if str(msg["sent_to"]) != device {
		t.Errorf("session_message = %v", msg)
	}

	// play something on it
	dune := findItem(t, "Movies", "Movie", "Dune")
	play := call(t, "session_play", map[string]any{"session": str(player["id"]), "item_ids": []any{dune}})
	if str(play["playing_on"]) != device {
		t.Errorf("session_play = %v", play)
	}
	play = call(t, "session_play", map[string]any{"session": device, "item_ids": []any{dune}, "mode": "PlayNext"})
	if str(play["playing_on"]) != device {
		t.Errorf("session_play PlayNext = %v", play)
	}

	// and drive it
	for _, cmd := range []string{"Pause", "Unpause", "Stop"} {
		out := call(t, "session_command", map[string]any{"session": device, "command": cmd})
		if !strings.Contains(str(out["sent"]), cmd) || !strings.Contains(str(out["sent"]), device) {
			t.Errorf("session_command %s = %v", cmd, out)
		}
	}
	out = call(t, "session_command", map[string]any{"session": device, "command": "Seek", "seek_s": 60})
	if !strings.Contains(str(out["sent"]), "Seek") {
		t.Errorf("session_command Seek = %v", out)
	}

	if e := callErr(t, "session_message", map[string]any{"session": "no such device", "text": "x"}); !strings.Contains(e, "no such device") || !strings.Contains(e, device) {
		t.Errorf("an unknown session should list the real ones: %s", e)
	}
}

func TestSessionFamilyIsComplete(t *testing.T) {
	var got []string
	for _, name := range toolNames(t) {
		if strings.HasPrefix(name, "session_") {
			got = append(got, name)
		}
	}
	want := []string{"session_command", "session_list", "session_message", "session_play"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("session tools = %v, want %v", got, want)
	}
}
