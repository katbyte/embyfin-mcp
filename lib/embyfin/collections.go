package embyfin

import (
	"context"
	"net/url"
	"strings"
)

// CreateCollection makes a new collection (boxset) containing the given items
// and returns its id.
func (c *Client) CreateCollection(ctx context.Context, name string, itemIDs []string) (string, error) {
	q := url.Values{}
	q.Set("Name", name)
	if len(itemIDs) > 0 {
		q.Set("Ids", strings.Join(itemIDs, ","))
	}

	var resp createResponse
	if err := c.post(ctx, "/Collections", q, nil, &resp); err != nil {
		return "", err
	}

	return resp.ID, nil
}

// AddToCollection adds items to a collection.
func (c *Client) AddToCollection(ctx context.Context, collectionID string, itemIDs []string) error {
	q := url.Values{}
	q.Set("Ids", strings.Join(itemIDs, ","))

	return c.post(ctx, "/Collections/"+url.PathEscape(collectionID)+"/Items", q, nil, nil)
}

// RemoveFromCollection removes items from a collection.
func (c *Client) RemoveFromCollection(ctx context.Context, collectionID string, itemIDs []string) error {
	q := url.Values{}
	q.Set("Ids", strings.Join(itemIDs, ","))

	return c.del(ctx, "/Collections/"+url.PathEscape(collectionID)+"/Items", q)
}
