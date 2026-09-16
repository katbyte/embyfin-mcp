//go:build integration

package integration

import (
	"slices"
	"strconv"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/emby"
)

// TestEmbySessions makes the suite's own session controllable and then sends
// it the three kinds of command. Nothing is listening on the session's end,
// so what is asserted is that the server accepts each command for a session
// that advertises support, and reports the capabilities back.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestEmbySessions(t *testing.T) {
	ctx := skipUnlessEmby(t)
	movie := embyMovie(t, alien)
	itemID, err := strconv.Atoi(movie.Id)
	if err != nil {
		t.Fatalf("item id %q is not an integer: %v", movie.Id, err)
	}

	if _, err := embyc.PostSessionsCapabilities(ctx, emby.PostSessionsCapabilitiesOperationOptions{
		PlayableMediaTypes:   "Video",
		SupportedCommands:    "DisplayMessage,Play",
		SupportsMediaControl: new(true),
	}); err != nil {
		t.Fatal(err)
	}

	// the session's SupportsRemoteControl flag stays false until a websocket
	// controller attaches; the capabilities are what round-trip
	// the API key's session reports the key's device rather than the
	// DeviceId the client header names, so the suite's own session is the
	// one carrying the capabilities just posted
	sessions := must(embyc.GetSessions(ctx, emby.GetSessionsOperationOptions{})).Model
	i := slices.IndexFunc(sessions, func(s emby.SessionSessionInfo) bool {
		return slices.Contains(s.PlayableMediaTypes, "Video") && slices.Contains(s.SupportedCommands, "Play")
	})
	if i < 0 {
		t.Fatalf("no session carries the capabilities after PostSessionsCapabilities: %+v", sessions)
	}
	own := sessions[i]
	if own.Id == "" || own.Client == "" || own.DeviceName == "" || own.LastActivityDate == "" || own.ServerId == "" {
		t.Errorf("session = %+v", own)
	}
	if !slices.Contains(own.PlayableMediaTypes, "Video") || !slices.Contains(own.SupportedCommands, "DisplayMessage") {
		t.Errorf("capabilities did not round-trip: %+v", own)
	}

	byDevice := must(embyc.GetSessions(ctx, emby.GetSessionsOperationOptions{DeviceId: own.DeviceId})).Model
	if !slices.ContainsFunc(byDevice, func(s emby.SessionSessionInfo) bool { return s.Id == own.Id }) {
		t.Errorf("GetSessions(DeviceId=%s) lacks the session %s", own.DeviceId, own.Id)
	}

	if _, err := embyc.PostSessionsByIdPlaying(ctx, own.Id, emby.PlayRequest{}, emby.PostSessionsByIdPlayingOperationOptions{PlayCommand: "PlayNow", ItemIds: []int{itemID}}); err != nil {
		t.Errorf("play: %v", err)
	}
	if _, err := embyc.PostSessionsByIdPlayingByCommand(ctx, own.Id, "Pause", emby.PlaystateRequest{}); err != nil {
		t.Errorf("playstate Pause: %v", err)
	}
	if _, err := embyc.PostSessionsByIdPlayingByCommand(ctx, own.Id, "Seek", emby.PlaystateRequest{Command: "Seek", SeekPositionTicks: 10_000_000}); err != nil {
		t.Errorf("playstate Seek: %v", err)
	}
	if _, err := embyc.PostSessionsByIdMessage(ctx, own.Id, emby.PostSessionsByIdMessageOperationOptions{Header: "SDK", Text: "hello from the integration suite", TimeoutMs: 1000}); err != nil {
		t.Errorf("message: %v", err)
	}

	// the full-capabilities body form
	if _, err := embyc.PostSessionsCapabilitiesFull(ctx, emby.ClientCapabilities{
		PlayableMediaTypes: []string{"Video"}, SupportedCommands: []string{"DisplayMessage"}, SupportsMediaControl: new(true),
	}, emby.PostSessionsCapabilitiesFullOperationOptions{}); err != nil {
		t.Errorf("full capabilities: %v", err)
	}
}
