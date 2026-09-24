package embyfin

import (
	"context"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
)

type PlayState struct {
	PositionTicks int64 `json:"PositionTicks,omitempty"`
	IsPaused      bool  `json:"IsPaused"`
}

type Session struct {
	ID               string    `json:"Id"`
	UserName         string    `json:"UserName,omitempty"`
	Client           string    `json:"Client,omitempty"`
	DeviceName       string    `json:"DeviceName,omitempty"`
	LastActivityDate string    `json:"LastActivityDate,omitempty"`
	NowPlayingItem   *Item     `json:"NowPlayingItem,omitempty"`
	PlayState        PlayState `json:"PlayState"`
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
