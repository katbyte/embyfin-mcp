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

// EpisodeOptions scopes an episode query. The zero value asks for every
// episode of the series with the default fields.
type EpisodeOptions struct {
	SeasonID string // one season, by item id
	Season   int    // one season, by number; 0 is every season
	UserID   string // user context, so the server includes watch state
	// Missing asks for only the episodes the library lacks. Jellyfin filters
	// on it; Emby 4.10 has no such parameter and answers every episode, so a
	// caller tells the missing ones by Path and IsMissing whichever server
	// answered.
	Missing    bool
	Fields     string // override FieldsDefault
	Limit      int
	StartIndex int
}

func (o EpisodeOptions) fields() string {
	if o.Fields != "" {
		return o.Fields
	}

	return FieldsDefault
}

// Episodes lists a series' episodes, scoped by opts.
func (c *Client) Episodes(ctx context.Context, seriesID string, opts EpisodeOptions) ([]Item, error) {
	if c.isEmby() {
		res, err := c.emby.GetShowsByIdEpisodes(ctx, seriesID, emby.GetShowsByIdEpisodesOperationOptions{
			UserId: opts.UserID, Fields: opts.fields(), SeasonId: opts.SeasonID, Season: opts.Season,
			Limit: opts.Limit, StartIndex: opts.StartIndex,
		})
		if err != nil {
			return nil, err
		}

		return itemsFromEmby(res.Model.Items), nil
	}

	var isMissing *bool
	if opts.Missing {
		isMissing = new(true)
	}
	res, err := c.jf.GetEpisodes(ctx, seriesID, jf.GetEpisodesOperationOptions{
		UserId: opts.UserID, Fields: list[jf.ItemFields](opts.fields()), SeasonId: opts.SeasonID, Season: opts.Season,
		IsMissing: isMissing, Limit: opts.Limit, StartIndex: opts.StartIndex,
	})
	if err != nil {
		return nil, err
	}

	return itemsFromJF(res.Model.Items), nil
}

// EpisodesAndRecords lists a series' episodes, including the records the
// server keeps for episodes it has no file for.
//
// The two servers differ on how to ask. Emby 4.10 has no missing filter and
// answers with everything it holds, records included, so one query does it.
// Jellyfin honours IsMissing: asked for the missing ones it answers with
// only those, and its default view may leave them out altogether, so both
// questions are put and the answers merged. Asking Jellyfin only for the
// missing ones is the trap: the caller then sees no episode with a file and
// concludes the library holds nothing.
func (c *Client) EpisodesAndRecords(ctx context.Context, seriesID string) ([]Item, error) {
	held, err := c.Episodes(ctx, seriesID, EpisodeOptions{})
	if err != nil {
		return nil, err
	}
	if c.isEmby() {
		return held, nil
	}

	records, err := c.Episodes(ctx, seriesID, EpisodeOptions{Missing: true})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(held))
	for i := range held {
		seen[held[i].ID] = true
	}
	for i := range records {
		if !seen[records[i].ID] {
			held = append(held, records[i])
		}
	}

	return held, nil
}
