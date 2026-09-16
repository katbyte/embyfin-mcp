//go:build integration

package integration

import (
	"slices"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/jf"
	"github.com/katbyte/go-kt/pointer"
)

// TestJFSessions makes the suite's own session controllable and then sends
// it the three kinds of command. Nothing is listening on the session's end,
// so what is asserted is that the server accepts each command for a session
// that advertises support, and reports the capabilities back.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestJFSessions(t *testing.T) {
	ctx := skipUnlessJellyfin(t)
	movie := jfMovie(t, alien)

	if _, err := jfc.PostCapabilities(ctx, jf.PostCapabilitiesOperationOptions{
		PlayableMediaTypes:   []jf.MediaType{jf.MediaTypeVideo},
		SupportedCommands:    []jf.GeneralCommandType{jf.GeneralCommandTypeDisplayMessage, jf.GeneralCommandTypePlay},
		SupportsMediaControl: new(true),
	}); err != nil {
		t.Fatal(err)
	}

	sessions := must(jfc.GetSessions(ctx, jf.GetSessionsOperationOptions{DeviceId: deviceID})).Model
	i := slices.IndexFunc(sessions, func(s jf.SessionInfoDto) bool {
		return s.DeviceId == deviceID && s.Capabilities != nil && pointer.From(s.Capabilities.SupportsMediaControl)
	})
	if i < 0 {
		t.Fatalf("no controllable session for device %q after PostCapabilities: %+v", deviceID, sessions)
	}
	own := sessions[i]
	// the session's own SupportsMediaControl flag stays false until a
	// websocket controller attaches; the capabilities are what round-trip
	if pointer.From(own.SupportsMediaControl) {
		t.Errorf("SupportsMediaControl is true for a session with no websocket: %+v", own)
	}
	if own.Id == "" || own.Client == "" || own.DeviceName == "" || own.LastActivityDate == "" || own.ServerId == "" {
		t.Errorf("session = %+v", own)
	}
	if !slices.Contains(own.PlayableMediaTypes, jf.MediaTypeVideo) || !slices.Contains(own.SupportedCommands, jf.GeneralCommandTypeDisplayMessage) {
		t.Errorf("capabilities did not round-trip: %+v", own)
	}

	// the full list has every session, ours included
	all := must(jfc.GetSessions(ctx, jf.GetSessionsOperationOptions{})).Model
	if !slices.ContainsFunc(all, func(s jf.SessionInfoDto) bool { return s.Id == own.Id }) {
		t.Errorf("GetSessions() lacks the session %s that GetSessions(DeviceId) found", own.Id)
	}

	if _, err := jfc.Play(ctx, own.Id, jf.PlayOperationOptions{PlayCommand: jf.PlayCommandPlayNow, ItemIds: []string{movie.Id}}); err != nil {
		t.Errorf("Play: %v", err)
	}
	if _, err := jfc.SendPlaystateCommand(ctx, own.Id, jf.PlaystateCommandPause, jf.SendPlaystateCommandOperationOptions{}); err != nil {
		t.Errorf("SendPlaystateCommand(Pause): %v", err)
	}
	if _, err := jfc.SendPlaystateCommand(ctx, own.Id, jf.PlaystateCommandSeek, jf.SendPlaystateCommandOperationOptions{SeekPositionTicks: 10_000_000}); err != nil {
		t.Errorf("SendPlaystateCommand(Seek): %v", err)
	}
	if _, err := jfc.SendMessageCommand(ctx, own.Id, jf.MessageCommand{Header: "SDK", Text: "hello from the integration suite", TimeoutMs: 1000}); err != nil {
		t.Errorf("SendMessageCommand: %v", err)
	}

	// the full-capabilities body form
	if _, err := jfc.PostFullCapabilities(ctx, jf.ClientCapabilitiesDto{
		PlayableMediaTypes: []jf.MediaType{jf.MediaTypeVideo}, SupportedCommands: []jf.GeneralCommandType{jf.GeneralCommandTypeDisplayMessage}, SupportsMediaControl: new(true),
	}, jf.PostFullCapabilitiesOperationOptions{}); err != nil {
		t.Errorf("PostFullCapabilities: %v", err)
	}
}
