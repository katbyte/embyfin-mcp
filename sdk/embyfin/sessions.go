package embyfin

import (
	"context"
	"strings"

	"github.com/katbyte/embyfin-mcp/sdk/emby"
	"github.com/katbyte/embyfin-mcp/sdk/jf"
)

type PlayState struct {
	PositionTicks int64 `json:"PositionTicks,omitempty"`
	IsPaused      bool  `json:"IsPaused"`
	// PlayMethod is how what is playing reaches the device: DirectPlay (the
	// file as it is), DirectStream (its streams repacked, none re-encoded)
	// or Transcode
	PlayMethod string `json:"PlayMethod,omitempty"`
	// MediaSourceID is which of the item's files is playing, for an item
	// held in several versions
	MediaSourceID string `json:"MediaSourceId,omitempty"`
}

// Transcoding is what a server says about the stream it is making for a
// session: what it turns the file into, why, and what does the work.
type Transcoding struct {
	Container  string `json:"Container,omitempty"`
	VideoCodec string `json:"VideoCodec,omitempty"`
	AudioCodec string `json:"AudioCodec,omitempty"`
	// VideoDirect and AudioDirect say a stream is passed on as it is, not
	// re-encoded
	VideoDirect   bool    `json:"IsVideoDirect"`
	AudioDirect   bool    `json:"IsAudioDirect"`
	Bitrate       int     `json:"Bitrate,omitempty"`
	Width         int     `json:"Width,omitempty"`
	Height        int     `json:"Height,omitempty"`
	AudioChannels int     `json:"AudioChannels,omitempty"`
	Framerate     float64 `json:"Framerate,omitempty"`
	// Completion is how much of the file has been transcoded, in percent
	Completion float64  `json:"CompletionPercentage,omitempty"`
	Reasons    []string `json:"TranscodeReasons,omitempty"`
	// What does the work, as each server says it. Emby names the decoder
	// and the encoder and says of each whether it is hardware; Jellyfin
	// names the kind of hardware acceleration, "none" for software
	VideoDecoder         string `json:"VideoDecoder,omitempty"`
	VideoEncoder         string `json:"VideoEncoder,omitempty"`
	DecoderHardware      *bool  `json:"VideoDecoderIsHardware,omitempty"`
	EncoderHardware      *bool  `json:"VideoEncoderIsHardware,omitempty"`
	HardwareAcceleration string `json:"HardwareAccelerationType,omitempty"`
}

type Session struct {
	ID               string    `json:"Id"`
	UserName         string    `json:"UserName,omitempty"`
	Client           string    `json:"Client,omitempty"`
	AppVersion       string    `json:"ApplicationVersion,omitempty"`
	DeviceName       string    `json:"DeviceName,omitempty"`
	RemoteEndPoint   string    `json:"RemoteEndPoint,omitempty"`
	LastActivityDate string    `json:"LastActivityDate,omitempty"`
	NowPlayingItem   *Item     `json:"NowPlayingItem,omitempty"`
	PlayState        PlayState `json:"PlayState"`
	// Transcoding is set while the server is making a stream for the
	// session, and nil when the file goes out as it is
	Transcoding *Transcoding `json:"TranscodingInfo,omitempty"`
}

func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	if c.isEmby() {
		res, err := c.emby.GetSessions(ctx, emby.GetSessionsOperationOptions{})
		if err != nil {
			return nil, err
		}
		sessions := make([]Session, 0, len(res.Model))
		for i := range res.Model {
			sessions = append(sessions, sessionFromEmby(&res.Model[i]))
		}

		return sessions, nil
	}

	res, err := c.jf.GetSessions(ctx, jf.GetSessionsOperationOptions{})
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(res.Model))
	for i := range res.Model {
		sessions = append(sessions, sessionFromJF(&res.Model[i]))
	}

	return sessions, nil
}

// Play queues items on a session's device. playCommand is PlayNow, PlayNext,
// or PlayLast.
func (c *Client) Play(ctx context.Context, sessionID string, itemIDs []string, playCommand string) error {
	if c.isEmby() {
		// Emby's document types the item ids as integers
		ids := make([]int64, 0, len(itemIDs))
		for _, id := range itemIDs {
			n, err := embyID(id)
			if err != nil {
				return err
			}
			ids = append(ids, n)
		}
		_, err := c.emby.PostSessionsByIdPlaying(ctx, sessionID, emby.PlayRequest{}, emby.PostSessionsByIdPlayingOperationOptions{ItemIds: ids, PlayCommand: emby.PlayCommand(playCommand)})

		return err
	}

	_, err := c.jf.Play(ctx, sessionID, jf.PlayOperationOptions{ItemIds: itemIDs, PlayCommand: jf.PlayCommand(playCommand)})

	return err
}

// PlayCommand sends a playstate command: Pause, Unpause, Stop, PlayPause,
// Seek (with seekTicks), NextTrack, PreviousTrack.
func (c *Client) PlayCommand(ctx context.Context, sessionID, command string, seekTicks int64) error {
	seek := strings.EqualFold(command, "Seek")
	if c.isEmby() {
		// Emby's document takes the target position in the PlaystateRequest
		// body, whose number the typed model leaves out at 0, so a seek to
		// the start would send none; the query carries it too
		// (emby-playstate-seek-query), where Emby's own web client sends it
		// and 0 is sent
		var body emby.PlaystateRequest
		var options emby.PostSessionsByIdPlayingByCommandOperationOptions
		if seek {
			body.SeekPositionTicks = seekTicks
			options.SeekPositionTicks = &seekTicks
		}
		_, err := c.emby.PostSessionsByIdPlayingByCommand(ctx, sessionID, emby.PlaystateCommand(command), body, options)

		return err
	}

	var options jf.SendPlaystateCommandOperationOptions
	if seek {
		options.SeekPositionTicks = &seekTicks
	}
	_, err := c.jf.SendPlaystateCommand(ctx, sessionID, jf.PlaystateCommand(command), options)

	return err
}

// Message displays a text message on the session's client. Emby takes the
// message as query parameters, Jellyfin as a JSON body.
func (c *Client) Message(ctx context.Context, sessionID, header, text string, timeoutMs int) error {
	if c.isEmby() {
		_, err := c.emby.PostSessionsByIdMessage(ctx, sessionID, emby.PostSessionsByIdMessageOperationOptions{Text: text, Header: header, TimeoutMs: nz(int64(timeoutMs))})
		return err
	}

	_, err := c.jf.SendMessageCommand(ctx, sessionID, jf.MessageCommand{Header: header, Text: text, TimeoutMs: int64(timeoutMs)})

	return err
}
