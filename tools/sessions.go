package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolveSession finds a live session by id, device name, or client name
// (case-insensitive substring).
func resolveSession(ctx context.Context, client *embyfin.Client, target string) (*embyfin.Session, error) {
	sessions, err := client.Sessions(ctx)
	if err != nil {
		return nil, err
	}

	needle := strings.ToLower(target)
	descs := make([]string, 0, len(sessions))
	for i := range sessions {
		s := &sessions[i]
		if s.ID == target ||
			strings.Contains(strings.ToLower(s.DeviceName), needle) ||
			strings.Contains(strings.ToLower(s.Client), needle) {
			return s, nil
		}
		descs = append(descs, s.DeviceName+" ("+s.Client+")")
	}

	return nil, fmt.Errorf("no session matching %q (have: %s)", target, strings.Join(descs, ", "))
}

func registerSessionTools(server *mcp.Server, client *embyfin.Client) {
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
	mcp.AddTool(server, &mcp.Tool{
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
		Session string   `json:"session"        jsonschema:"session id, device name, or app name from session_list"`
		ItemIDs []string `json:"item_ids"       jsonschema:"library item id(s) to play"`
		Mode    string   `json:"mode,omitempty" jsonschema:"PlayNow (default), PlayNext, or PlayLast"`
	}
	type playOut struct {
		PlayingOn string `json:"playing_on"`
	}
	mcp.AddTool(server, &mcp.Tool{
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
		Session     string `json:"session"                jsonschema:"session id, device name, or app name from session_list"`
		Command     string `json:"command"                jsonschema:"Pause, Unpause, PlayPause, Stop, Seek, NextTrack, PreviousTrack"`
		SeekMinutes int    `json:"seek_minutes,omitempty" jsonschema:"target position for Seek, minutes from the start"`
	}
	type commandOut struct {
		Sent string `json:"sent"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_command",
		Description: "Send a playback command (pause, stop, seek...) to a device. Changes what the device is doing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in commandIn) (*mcp.CallToolResult, commandOut, error) {
		session, err := resolveSession(ctx, client, in.Session)
		if err != nil {
			return nil, commandOut{}, err
		}

		seekTicks := int64(in.SeekMinutes) * int64(time.Minute/100)
		if err := client.PlayCommand(ctx, session.ID, in.Command, seekTicks); err != nil {
			return nil, commandOut{}, err
		}

		return nil, commandOut{Sent: in.Command + " → " + session.DeviceName}, nil
	})

	type messageIn struct {
		Session   string `json:"session"              jsonschema:"session id, device name, or app name from session_list"`
		Text      string `json:"text"                 jsonschema:"the message to display"`
		Header    string `json:"header,omitempty"     jsonschema:"message title, default 'Message'"`
		TimeoutMs int    `json:"timeout_ms,omitempty"`
	}
	type messageOut struct {
		SentTo string `json:"sent_to"`
	}
	mcp.AddTool(server, &mcp.Tool{
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
