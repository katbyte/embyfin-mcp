package embyfin

import (
	"context"

	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
)

// Seasons lists a series' seasons. userID is optional but lets the server
// include watch state.
func (c *Client) Seasons(ctx context.Context, seriesID, userID string) ([]Item, error) {
	if c.isEmby() {
		res, err := c.emby.GetShowsByIdSeasons(ctx, seriesID, emby.GetShowsByIdSeasonsOperationOptions{UserId: userID, Fields: FieldsDefault})
		if err != nil {
			return nil, err
		}

		return itemsFromEmby(res.Model.Items), nil
	}

	res, err := c.jf.GetSeasons(ctx, seriesID, jf.GetSeasonsOperationOptions{UserId: userID, Fields: list[jf.ItemFields](FieldsDefault)})
	if err != nil {
		return nil, err
	}

	return itemsFromJF(res.Model.Items), nil
}

// Episodes lists a series' episodes, optionally scoped to a season. When
// missing is true, only virtual episodes the library lacks are asked for
// (Emby ignores the filter and answers every episode; the tools tell the
// missing ones by IsMissing).
func (c *Client) Episodes(ctx context.Context, seriesID, seasonID, userID string, missing bool) ([]Item, error) {
	var isMissing *bool
	if missing {
		isMissing = new(true)
	}
	if c.isEmby() {
		// Emby 4.10 has no IsMissing on the episode query (it answered every
		// episode whatever was asked before that); the missing ones are the
		// virtual ones
		res, err := c.emby.GetShowsByIdEpisodes(ctx, seriesID, emby.GetShowsByIdEpisodesOperationOptions{
			UserId: userID, Fields: FieldsDefault, SeasonId: seasonID,
		})
		if err != nil {
			return nil, err
		}

		return itemsFromEmby(res.Model.Items), nil
	}

	res, err := c.jf.GetEpisodes(ctx, seriesID, jf.GetEpisodesOperationOptions{
		UserId: userID, Fields: list[jf.ItemFields](FieldsDefault), SeasonId: seasonID, IsMissing: isMissing,
	})
	if err != nil {
		return nil, err
	}

	return itemsFromJF(res.Model.Items), nil
}
