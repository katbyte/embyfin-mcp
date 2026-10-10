package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
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

// sessionSource is the file a session is playing, as the server read it.
type sessionSource struct {
	Container  string       `json:"container,omitempty"`
	Bitrate    int64        `json:"bitrate,omitempty"     jsonschema:"bits per second: the video's, falling back to the whole file's"`
	Width      int          `json:"width,omitempty"`
	Height     int          `json:"height,omitempty"`
	VideoCodec string       `json:"video_codec,omitempty"`
	FrameRate  float64      `json:"frame_rate,omitempty"`
	HDR        string       `json:"hdr,omitempty"         jsonschema:"the dynamic range as the server read it: sdr, hdr10, hdr10plus, hlg, dovi"`
	Audio      []audioTrack `json:"audio,omitempty"       jsonschema:"every audio track the file has"`
	Versions   int          `json:"versions,omitempty"    jsonschema:"set when the item is held in several files: these are the best one's facts, which may not be the one playing"`
}

// sessionSourceOf reads the facts of the file behind what a session is
// playing. A session names its item with no files under it, so the item is
// read again for them; one the server no longer lists has none to give.
func sessionSourceOf(ctx context.Context, client *embyfin.Client, playing *embyfin.Item) (*sessionSource, error) {
	it := playing
	if len(it.MediaSources) == 0 {
		read, err := client.ItemByID(ctx, playing.ID)
		if err != nil {
			// the item went while it played: there is no file to read
			if _, gone := errors.AsType[*embyfin.NoItemError](err); gone {
				return nil, nil
			}

			return nil, fmt.Errorf("reading the file behind %s (id %s): %w", playing.Name, playing.ID, err)
		}
		it = read
	}
	if it.BestSource() == nil {
		return nil, nil
	}
	q := qualityOf(it)
	src := &sessionSource{Container: q.Container, Bitrate: q.Bitrate, Width: q.Width, Height: q.Height, VideoCodec: q.VideoCodec, FrameRate: q.FrameRate, HDR: q.HDR, Audio: q.Audio}
	if len(it.MediaSources) > 1 {
		src.Versions = len(it.MediaSources)
	}

	return src, nil
}

// transcodeSummary says on one line what a server is doing to a stream:
// what it re-encodes, into what, on what, and why. "" when it is doing
// nothing to it.
func transcodeSummary(t *embyfin.Transcoding) string {
	if t == nil {
		return ""
	}
	var parts []string
	video := "video copied"
	if !t.VideoDirect {
		video = "video to " + t.VideoCodec
		if t.Width > 0 && t.Height > 0 {
			video += fmt.Sprintf(" %dx%d", t.Width, t.Height)
		}
		switch {
		case t.EncoderHardware != nil && *t.EncoderHardware:
			video += " on hardware"
			if t.HardwareAcceleration != "" {
				video += " (" + t.HardwareAcceleration + ")"
			}
		case t.EncoderHardware != nil:
			video += " in software"
		case t.HardwareAcceleration != "" && !strings.EqualFold(t.HardwareAcceleration, "none"):
			video += " on hardware (" + t.HardwareAcceleration + ")"
		case strings.EqualFold(t.HardwareAcceleration, "none"):
			video += " in software"
		}
	}
	parts = append(parts, video)
	if t.AudioDirect {
		parts = append(parts, "audio copied")
	} else if t.AudioCodec != "" {
		parts = append(parts, "audio to "+t.AudioCodec)
	}
	out := strings.Join(parts, ", ")
	if len(t.Reasons) > 0 {
		out += ": " + strings.Join(t.Reasons, ", ")
	}

	return out
}

// playingOf is what a session is playing, where in it, and how far through
// in percent. A session playing nothing gives no name.
func playingOf(s *embyfin.Session) (name, position string, progress *float64) {
	it := s.NowPlayingItem
	if it == nil {
		return "", "", nil
	}

	// an episode's own title alone is one of a dozen "Pilot"s
	name = it.Name
	if it.Type == typeEpisode && it.SeriesName != "" {
		name = fmt.Sprintf("%s %s %s", it.SeriesName, episodeCode(it), it.Name)
	}
	pos := time.Duration(s.PlayState.PositionTicks * 100)
	total := time.Duration(it.RunTimeTicks * 100)
	position = fmt.Sprintf("%s / %s", pos.Round(time.Second), total.Round(time.Second))
	if total > 0 {
		progress = new(math.Round(float64(pos)/float64(total)*1000) / 10)
	}

	return name, position, progress
}

func registerSessionTools(r *registry) {
	client := r.client
	type sessionTranscode struct {
		Container       string   `json:"container,omitempty"`
		VideoCodec      string   `json:"video_codec,omitempty"`
		AudioCodec      string   `json:"audio_codec,omitempty"`
		VideoDirect     bool     `json:"video_direct"                    jsonschema:"the video is passed on as it is, not re-encoded"`
		AudioDirect     bool     `json:"audio_direct"                    jsonschema:"the audio is passed on as it is, not re-encoded"`
		Bitrate         int      `json:"bitrate,omitempty"               jsonschema:"bits per second, of the stream being made"`
		Width           int      `json:"width,omitempty"`
		Height          int      `json:"height,omitempty"`
		FrameRate       float64  `json:"frame_rate,omitempty"`
		AudioChannels   int      `json:"audio_channels,omitempty"`
		Completion      float64  `json:"completion_percent,omitempty"    jsonschema:"how much of the file the server has transcoded so far"`
		Reasons         []string `json:"reasons"                         jsonschema:"why the server is transcoding, in its own words: ContainerBitrateExceedsLimit, VideoCodecNotSupported, SubtitleCodecNotSupported..."`
		VideoDecoder    string   `json:"video_decoder,omitempty"         jsonschema:"Emby: what decodes the video"`
		VideoEncoder    string   `json:"video_encoder,omitempty"         jsonschema:"Emby: what encodes it"`
		DecoderHardware *bool    `json:"decoder_hardware,omitempty"      jsonschema:"Emby: whether the decoder is hardware (a GPU). Absent when the server does not say, as Jellyfin does not"`
		EncoderHardware *bool    `json:"encoder_hardware,omitempty"      jsonschema:"Emby: whether the encoder is hardware"`
		Hardware        string   `json:"hardware_acceleration,omitempty" jsonschema:"the kind of hardware acceleration in use, as the server names it (Jellyfin: none, nvenc, qsv, vaapi...); absent when it does not say"`
	}
	type sessionDetails struct {
		AppVersion    string            `json:"app_version,omitempty"`
		RemoteAddress string            `json:"remote_address,omitempty" jsonschema:"where the device connects from"`
		Source        *sessionSource    `json:"source,omitempty"         jsonschema:"the file being played, as the server read it"`
		Transcode     *sessionTranscode `json:"transcode,omitempty"      jsonschema:"what the server is turning the file into for this device; absent when the file goes out as it is"`
	}
	type sessionRow struct {
		ID           string          `json:"id"`
		User         string          `json:"user,omitempty"`
		Device       string          `json:"device"`
		App          string          `json:"app,omitempty"`
		LastActivity string          `json:"last_activity,omitempty"  jsonschema:"when the device last spoke to the server: a session long silent is one left open, not one in use"`
		NowPlaying   string          `json:"now_playing,omitempty"    jsonschema:"what is playing: an episode by its series and number (Breaking Bad S01E01 Pilot), anything else by its name"`
		NowPlayingID string          `json:"now_playing_id,omitempty" jsonschema:"the library item playing"`
		Position     string          `json:"position,omitempty"`
		Progress     *float64        `json:"progress,omitempty"       jsonschema:"how far through it is, in percent"`
		Paused       bool            `json:"paused,omitempty"`
		PlayMethod   string          `json:"play_method,omitempty"    jsonschema:"how what is playing reaches the device: DirectPlay (the file as it is), DirectStream (its streams repacked, none re-encoded) or Transcode (re-encoded by the server). Absent when the device has not said"`
		Transcoding  string          `json:"transcoding,omitempty"    jsonschema:"set while the server is making a stream for the device: what it re-encodes, into what, on what, and why. details has each part"`
		Details      *sessionDetails `json:"details,omitempty"        jsonschema:"with details: the device's app version and address, the file being played, and the stream being made from it"`
	}
	type sessionsIn struct {
		Details bool `json:"details,omitempty" jsonschema:"give each session's details: the file being played (container, codec, size, bitrate, range, audio tracks) and, when the server is transcoding, what into, why, how far it has got and whether hardware is doing it"`
	}
	type sessionsOut struct {
		Sessions []sessionRow `json:"sessions"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "session_list",
		Description: "Live sessions: which devices are connected, when each last spoke, what each is playing right now and how - played as the file is, or transcoded, and then into what and why. details adds the file's own facts and the whole of what the server says about the transcode, hardware included.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sessionsIn) (*mcp.CallToolResult, sessionsOut, error) {
		sessions, err := client.Sessions(ctx)
		if err != nil {
			return nil, sessionsOut{}, err
		}

		out := sessionsOut{}
		for i := range sessions {
			s := &sessions[i]
			row := sessionRow{ID: s.ID, User: s.UserName, Device: s.DeviceName, App: s.Client, LastActivity: s.LastActivityDate}
			if in.Details {
				row.Details = &sessionDetails{AppVersion: s.AppVersion, RemoteAddress: s.RemoteEndPoint}
			}
			if it := s.NowPlayingItem; it != nil {
				row.NowPlaying, row.Position, row.Progress = playingOf(s)
				row.NowPlayingID, row.Paused, row.PlayMethod = it.ID, s.PlayState.IsPaused, s.PlayState.PlayMethod
				row.Transcoding = transcodeSummary(s.Transcoding)
				if in.Details {
					if row.Details.Source, err = sessionSourceOf(ctx, client, it); err != nil {
						return nil, sessionsOut{}, err
					}
					if t := s.Transcoding; t != nil {
						row.Details.Transcode = &sessionTranscode{
							Container: t.Container, VideoCodec: t.VideoCodec, AudioCodec: t.AudioCodec, VideoDirect: t.VideoDirect, AudioDirect: t.AudioDirect,
							Bitrate: t.Bitrate, Width: t.Width, Height: t.Height, FrameRate: math.Round(t.Framerate*1000) / 1000, AudioChannels: t.AudioChannels,
							Completion: math.Round(t.Completion*10) / 10, Reasons: t.Reasons, VideoDecoder: t.VideoDecoder, VideoEncoder: t.VideoEncoder,
							DecoderHardware: t.DecoderHardware, EncoderHardware: t.EncoderHardware, Hardware: t.HardwareAcceleration,
						}
					}
				}
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
		User      string `json:"user,omitempty" jsonschema:"the session's user, whose watch state what plays is recorded to"`
		Was       string `json:"was,omitempty"  jsonschema:"what the device was playing before, and where in it; empty when nothing"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "session_play",
		Description: "Play items on a connected device ('play Dune on the living-room TV'). The device is someone's: with PlayNow whatever it was playing stops for this, and what plays is recorded as its session's user watching it - their progress, resume point and watched mark move as if they had played it. PlayNext and PlayLast queue the items after what is playing. " +
			"The answer names the session's user and what the device was playing, and where in it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in playIn) (*mcp.CallToolResult, playOut, error) {
		// both servers take a play of nothing, and Jellyfin one of an id it
		// does not hold, and answer as if the device were playing it
		if len(in.ItemIDs) == 0 {
			return nil, playOut{}, errors.New("item_ids is required: the library items to play")
		}
		for _, id := range in.ItemIDs {
			if _, err := client.ItemByID(ctx, id); err != nil {
				return nil, playOut{}, err
			}
		}
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

		return nil, playOut{PlayingOn: session.DeviceName, User: session.UserName, Was: nowPlaying(session)}, nil
	})

	type commandIn struct {
		Session string `json:"session"          jsonschema:"session id, or a device, app or user name from session_list (a part of one does when only one session has it)"`
		Command string `json:"command"          jsonschema:"Pause, Unpause, PlayPause, Stop, Seek, NextTrack, PreviousTrack"`
		SeekS   int    `json:"seek_s,omitempty" jsonschema:"target position for Seek, seconds from the start"`
	}
	type commandOut struct {
		Sent string `json:"sent"`
		User string `json:"user,omitempty" jsonschema:"the session's user"`
		Was  string `json:"was,omitempty"  jsonschema:"what the device was playing when the command was sent, and where in it; empty when nothing"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "session_command",
		Description: "Send a playback command (pause, stop, seek...) to a connected device. The device is someone's: the command changes what they are watching now, and a stop, a seek or a skip moves the progress and resume point the server records for the session's user. " +
			"The answer names the session's user and what the device was playing, and where in it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in commandIn) (*mcp.CallToolResult, commandOut, error) {
		session, err := resolveSession(ctx, client, in.Session)
		if err != nil {
			return nil, commandOut{}, err
		}

		seekTicks := int64(in.SeekS) * ticksPerSecond
		if err := client.PlayCommand(ctx, session.ID, in.Command, seekTicks); err != nil {
			return nil, commandOut{}, err
		}

		return nil, commandOut{Sent: in.Command + " → " + session.DeviceName, User: session.UserName, Was: nowPlaying(session)}, nil
	})

	type messageIn struct {
		Session   string `json:"session"              jsonschema:"session id, or a device, app or user name from session_list (a part of one does when only one session has it)"`
		Text      string `json:"text"                 jsonschema:"the message to display"`
		Header    string `json:"header,omitempty"     jsonschema:"message title, default 'Message'"`
		TimeoutMs int    `json:"timeout_ms,omitempty"`
	}
	type messageOut struct {
		SentTo string `json:"sent_to"`
		User   string `json:"user,omitempty" jsonschema:"the session's user, who sees it"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "session_message",
		Description: "Display a text message on a connected device's screen ('dinner is ready'), over whatever is on it, for its session's user to see. The answer names that user.",
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

		return nil, messageOut{SentTo: session.DeviceName, User: session.UserName}, nil
	})
}

// nowPlaying says what a session is playing and where in it, as it was read
// just before a change: "Dune at 1h2m3s of 2h35m0s, paused", "" for nothing.
func nowPlaying(s *embyfin.Session) string {
	if s.NowPlayingItem == nil {
		return ""
	}
	pos := time.Duration(s.PlayState.PositionTicks * 100).Round(time.Second)
	total := time.Duration(s.NowPlayingItem.RunTimeTicks * 100).Round(time.Second)
	out := fmt.Sprintf("%s at %s of %s", s.NowPlayingItem.Name, pos, total)
	if s.PlayState.IsPaused {
		out += ", paused"
	}

	return out
}
