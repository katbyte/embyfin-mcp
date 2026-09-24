package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolveSession finds the live session target names: by its id first, then
// by a device, app or user name matched whole (in any case), then by a part
// of such a name that only one session has. The session tools drive a real
// device, so a name more than one session answers to at the first of those
// that matches any is refused with the candidates, rather than settled by
// whichever the server happens to list first.
func resolveSession(ctx context.Context, client *embyfin.Client, target string) (*embyfin.Session, error) {
	// an empty name is a part of every device's, which would drive whichever
	// device the server lists first
	needle := strings.ToLower(strings.TrimSpace(target))
	if needle == "" {
		return nil, errors.New("session is required: a session id, or a device, app or user name, or part of one (session_list has them)")
	}
	sessions, err := client.Sessions(ctx)
	if err != nil {
		return nil, err
	}

	names := func(s *embyfin.Session) []string { return []string{s.DeviceName, s.Client, s.UserName} }
	for _, matches := range []func(s *embyfin.Session) bool{
		func(s *embyfin.Session) bool { return s.ID == strings.TrimSpace(target) },
		func(s *embyfin.Session) bool {
			return slices.ContainsFunc(names(s), func(n string) bool { return strings.EqualFold(n, needle) })
		},
		func(s *embyfin.Session) bool {
			return slices.ContainsFunc(names(s), func(n string) bool { return strings.Contains(strings.ToLower(n), needle) })
		},
	} {
		var found []string
		var match *embyfin.Session
		for i := range sessions {
			if s := &sessions[i]; matches(s) {
				match = s
				who := s.Client
				if s.UserName != "" {
					who += ", " + s.UserName
				}
				found = append(found, fmt.Sprintf("%s (%s) id %s", s.DeviceName, who, s.ID))
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return match, nil
		}

		return nil, fmt.Errorf("%q matches %d sessions: %s; name one by its id", target, len(found), strings.Join(found, ", "))
	}

	descs := make([]string, 0, len(sessions))
	for i := range sessions {
		descs = append(descs, sessions[i].DeviceName+" ("+sessions[i].Client+")")
	}

	return nil, fmt.Errorf("no session matching %q (have: %s)", target, strings.Join(descs, ", "))
}

func registerSessionTools(r *registry) {
	client := r.client
	type sessionRow struct {
		ID         string `json:"id"`
		User       string `json:"user,omitempty"`
		Device     string `json:"device"`
		App        string `json:"app,omitempty"`
		NowPlaying string `json:"now_playing,omitempty"`
		Position   string `json:"position,omitempty"`
		Paused     bool   `json:"paused,omitempty"`
	}
	type sessionsOut struct {
		Sessions []sessionRow `json:"sessions"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "session_list",
		Description: "Live sessions: which devices are connected and what each is playing right now.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, sessionsOut, error) {
		sessions, err := client.Sessions(ctx)
		if err != nil {
			return nil, sessionsOut{}, err
		}

		out := sessionsOut{}
		for _, s := range sessions {
			row := sessionRow{
				ID:     s.ID,
				User:   s.UserName,
				Device: s.DeviceName,
				App:    s.Client,
			}
			if s.NowPlayingItem != nil {
				row.NowPlaying = s.NowPlayingItem.Name
				row.Paused = s.PlayState.IsPaused
				pos := time.Duration(s.PlayState.PositionTicks * 100)
				total := time.Duration(s.NowPlayingItem.RunTimeTicks * 100)
				row.Position = fmt.Sprintf("%s / %s", pos.Round(time.Second), total.Round(time.Second))
			}
			out.Sessions = append(out.Sessions, row)
		}

		return nil, out, nil
	})

	type playIn struct {
		Session string   `json:"session"        jsonschema:"session id, or a device, app or user name from session_list (a part of one does when only one session has it)"`
		ItemIDs []string `json:"item_ids"       jsonschema:"library item id(s) to play"`
		Mode    string   `json:"mode,omitempty" jsonschema:"PlayNow (default), PlayNext, or PlayLast"`
	}
	type playOut struct {
		PlayingOn string `json:"playing_on"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "session_play",
		Description: "Play items on a connected device ('play Dune on the living-room TV'). Changes what the device is doing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in playIn) (*mcp.CallToolResult, playOut, error) {
		session, err := resolveSession(ctx, client, in.Session)
		if err != nil {
			return nil, playOut{}, err
		}

		mode := in.Mode
		if mode == "" {
			mode = "PlayNow"
		}

		if err := client.Play(ctx, session.ID, in.ItemIDs, mode); err != nil {
			return nil, playOut{}, err
		}

		return nil, playOut{PlayingOn: session.DeviceName}, nil
	})

	type commandIn struct {
		Session string `json:"session"          jsonschema:"session id, or a device, app or user name from session_list (a part of one does when only one session has it)"`
		Command string `json:"command"          jsonschema:"Pause, Unpause, PlayPause, Stop, Seek, NextTrack, PreviousTrack"`
		SeekS   int    `json:"seek_s,omitempty" jsonschema:"target position for Seek, seconds from the start"`
	}
	type commandOut struct {
		Sent string `json:"sent"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "session_command",
		Description: "Send a playback command (pause, stop, seek...) to a device. Changes what the device is doing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in commandIn) (*mcp.CallToolResult, commandOut, error) {
		session, err := resolveSession(ctx, client, in.Session)
		if err != nil {
			return nil, commandOut{}, err
		}

		seekTicks := int64(in.SeekS) * ticksPerSecond
		if err := client.PlayCommand(ctx, session.ID, in.Command, seekTicks); err != nil {
			return nil, commandOut{}, err
		}

		return nil, commandOut{Sent: in.Command + " → " + session.DeviceName}, nil
	})

	type messageIn struct {
		Session   string `json:"session"              jsonschema:"session id, or a device, app or user name from session_list (a part of one does when only one session has it)"`
		Text      string `json:"text"                 jsonschema:"the message to display"`
		Header    string `json:"header,omitempty"     jsonschema:"message title, default 'Message'"`
		TimeoutMs int    `json:"timeout_ms,omitempty"`
	}
	type messageOut struct {
		SentTo string `json:"sent_to"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "session_message",
		Description: "Display a text message on a device's screen ('dinner is ready').",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in messageIn) (*mcp.CallToolResult, messageOut, error) {
		session, err := resolveSession(ctx, client, in.Session)
		if err != nil {
			return nil, messageOut{}, err
		}

		header := in.Header
		if header == "" {
			header = "Message"
		}

		if err := client.Message(ctx, session.ID, header, in.Text, in.TimeoutMs); err != nil {
			return nil, messageOut{}, err
		}

		return nil, messageOut{SentTo: session.DeviceName}, nil
	})
}
