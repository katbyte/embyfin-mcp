package embyfin

import (
	"context"
	"net/url"
	"strings"
)

type createResponse struct {
	ID string `json:"Id"`
}

// CreatePlaylist makes a new playlist containing the given items and returns
// its id. mediaType is Video or Audio (empty lets the server infer).
func (c *Client) CreatePlaylist(ctx context.Context, name string, itemIDs []string, mediaType string) (string, error) {
	q := url.Values{}
	q.Set("Name", name)
	if len(itemIDs) > 0 {
		q.Set("Ids", strings.Join(itemIDs, ","))
	}
	if mediaType != "" {
		q.Set("MediaType", mediaType)
	}

	var resp createResponse
	if err := c.post(ctx, "/Playlists", q, nil, &resp); err != nil {
		return "", err
	}

	return resp.ID, nil
}

// PlaylistItems returns a playlist's entries in order. Each item carries
// PlaylistItemID, which is what removal requires.
func (c *Client) PlaylistItems(ctx context.Context, playlistID string) ([]Item, int, error) {
	q := url.Values{}
	q.Set("Fields", FieldsDefault)

	var resp itemsResponse
	if err := c.get(ctx, "/Playlists/"+url.PathEscape(playlistID)+"/Items", q, &resp); err != nil {
		return nil, 0, err
	}

	return resp.Items, resp.TotalRecordCount, nil
}

// AddToPlaylist appends items (by item id) to a playlist.
func (c *Client) AddToPlaylist(ctx context.Context, playlistID string, itemIDs []string) error {
	q := url.Values{}
	q.Set("Ids", strings.Join(itemIDs, ","))

	return c.post(ctx, "/Playlists/"+url.PathEscape(playlistID)+"/Items", q, nil, nil)
}

// RemoveFromPlaylist removes entries by their PlaylistItemID (not item id).
func (c *Client) RemoveFromPlaylist(ctx context.Context, playlistID string, entryIDs []string) error {
	q := url.Values{}
	q.Set("EntryIds", strings.Join(entryIDs, ","))

	return c.del(ctx, "/Playlists/"+url.PathEscape(playlistID)+"/Items", q)
}
