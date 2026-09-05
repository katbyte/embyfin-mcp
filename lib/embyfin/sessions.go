package embyfin

import (
	"context"
	"net/url"
	"strconv"
	"strings"
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
	var sessions []Session
	if err := c.get(ctx, "/Sessions", nil, &sessions); err != nil {
		return nil, err
	}

	return sessions, nil
}

// Play queues items on a session's device. playCommand is PlayNow, PlayNext,
// or PlayLast.
func (c *Client) Play(ctx context.Context, sessionID string, itemIDs []string, playCommand string) error {
	q := url.Values{}
	q.Set("ItemIds", strings.Join(itemIDs, ","))
	q.Set("PlayCommand", playCommand)

	return c.post(ctx, "/Sessions/"+url.PathEscape(sessionID)+"/Playing", q, nil, nil)
}

// PlayCommand sends a playstate command: Pause, Unpause, Stop, PlayPause,
// Seek (with seekTicks), NextTrack, PreviousTrack.
func (c *Client) PlayCommand(ctx context.Context, sessionID, command string, seekTicks int64) error {
	q := url.Values{}
	if strings.EqualFold(command, "Seek") {
		q.Set("SeekPositionTicks", strconv.FormatInt(seekTicks, 10))
	}

	return c.post(ctx, "/Sessions/"+url.PathEscape(sessionID)+"/Playing/"+url.PathEscape(command), q, nil, nil)
}

// Message displays a text message on the session's client.
func (c *Client) Message(ctx context.Context, sessionID, header, text string, timeoutMs int) error {
	body := map[string]any{
		"Header": header,
		"Text":   text,
	}
	if timeoutMs > 0 {
		body["TimeoutMs"] = timeoutMs
	}

	return c.post(ctx, "/Sessions/"+url.PathEscape(sessionID)+"/Message", nil, body, nil)
}
