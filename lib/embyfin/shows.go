package embyfin

import (
	"context"
	"net/url"
)

// Seasons lists a series' seasons. userID is optional but lets the server
// include watch state.
func (c *Client) Seasons(ctx context.Context, seriesID, userID string) ([]Item, error) {
	q := url.Values{}
	q.Set("Fields", FieldsDefault)
	if userID != "" {
		q.Set("UserId", userID)
	}

	var resp itemsResponse
	if err := c.get(ctx, "/Shows/"+url.PathEscape(seriesID)+"/Seasons", q, &resp); err != nil {
		return nil, err
	}

	return resp.Items, nil
}

// Episodes lists a series' episodes, optionally scoped to a season. When
// missing is true, only virtual episodes the library lacks are returned.
func (c *Client) Episodes(ctx context.Context, seriesID, seasonID, userID string, missing bool) ([]Item, error) {
	q := url.Values{}
	q.Set("Fields", FieldsDefault)
	if seasonID != "" {
		q.Set("SeasonId", seasonID)
	}
	if userID != "" {
		q.Set("UserId", userID)
	}
	if missing {
		q.Set("IsMissing", "true")
	}

	var resp itemsResponse
	if err := c.get(ctx, "/Shows/"+url.PathEscape(seriesID)+"/Episodes", q, &resp); err != nil {
		return nil, err
	}

	return resp.Items, nil
}
