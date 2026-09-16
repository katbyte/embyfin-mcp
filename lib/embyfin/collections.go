package embyfin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	apiclient "github.com/katbyte/embyfin-mcp/lib/client"

	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
)

// CreateCollection makes a new collection (boxset) containing the given items
// and returns its id. A name is a collection's folder on both servers, so
// creating one under a name that exists answers with that collection (and
// Jellyfin replaces what it holds). Emby cannot create one empty (a 500), so
// there it needs an item. Emby also answers a 500 (a FOREIGN KEY constraint)
// to a collection named like one deleted while a metadata refresh of it was
// still queued (behind a library scan, say): the refresh runs after the
// delete and leaves the name unusable until Emby restarts. The error says so.
func (c *Client) CreateCollection(ctx context.Context, name string, itemIDs []string) (string, error) {
	if c.isEmby() {
		if len(itemIDs) == 0 {
			return "", errors.New("emby cannot create an empty collection: pass at least one item")
		}
		res, err := c.emby.PostCollections(ctx, emby.PostCollectionsOperationOptions{Name: name, Ids: strings.Join(itemIDs, ",")})
		if apiclient.StatusCode(err) == http.StatusInternalServerError {
			return "", fmt.Errorf("%w (Emby answers so for the name of a collection deleted while a refresh of it was still queued, until Emby restarts, and for a write that lands while a member is being refreshed; another name works)", err)
		}
		if err != nil {
			return "", err
		}

		return res.Model.Id, nil
	}

	res, err := c.jf.CreateCollection(ctx, jf.CreateCollectionOperationOptions{Name: name, Ids: itemIDs})
	if err != nil {
		return "", err
	}

	return res.Model.Id, nil
}

// AddToCollection adds items to a collection. Jellyfin saves a collection's
// members as a list it reads and writes back, so two adds at once lose one
// (a journey saw five parallel adds keep four); changes to one collection
// from this process are made one at a time (see keyedLocks).
func (c *Client) AddToCollection(ctx context.Context, collectionID string, itemIDs []string) error {
	unlock := c.items.lock(collectionID)
	defer unlock()

	if c.isEmby() {
		_, err := c.emby.PostCollectionsByIdItems(ctx, collectionID, emby.PostCollectionsByIdItemsOperationOptions{Ids: strings.Join(itemIDs, ",")})
		return err
	}

	_, err := c.jf.AddToCollection(ctx, collectionID, jf.AddToCollectionOperationOptions{Ids: itemIDs})

	return err
}

// RemoveFromCollection removes items from a collection and waits until they
// have left it. Both servers apply a removal a moment after answering, and
// Jellyfin answers one it cannot match to a member with a 204 and removes
// nothing, so an item the collection does not hold is an error, and a removal
// that has not landed after a few seconds is sent once more before it is one.
func (c *Client) RemoveFromCollection(ctx context.Context, collectionID string, itemIDs []string) error {
	unlock := c.items.lock(collectionID)
	defer unlock()

	held, err := c.CollectionMembers(ctx, collectionID)
	if err != nil {
		return err
	}
	for _, id := range itemIDs {
		if !slices.Contains(held, id) {
			return fmt.Errorf("the collection does not hold item %s", id)
		}
	}

	var left []string
	for range 2 {
		if err := c.removeMembers(ctx, collectionID, itemIDs); err != nil {
			return err
		}
		for range 20 {
			if held, err = c.CollectionMembers(ctx, collectionID); err != nil {
				return err
			}
			left = slices.DeleteFunc(slices.Clone(itemIDs), func(id string) bool { return !slices.Contains(held, id) })
			if len(left) == 0 {
				return nil
			}
			if err := c.pause(ctx); err != nil {
				return err
			}
		}
	}

	return fmt.Errorf("the server did not remove %s from the collection", strings.Join(left, ", "))
}

// CollectionMembers lists the ids of the items a collection holds.
func (c *Client) CollectionMembers(ctx context.Context, collectionID string) ([]string, error) {
	items, _, err := c.Search(ctx, SearchOptions{ParentID: collectionID, Fields: FieldsLean})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(items))
	for i := range items {
		ids = append(ids, items[i].ID)
	}

	return ids, nil
}

func (c *Client) removeMembers(ctx context.Context, collectionID string, itemIDs []string) error {
	if c.isEmby() {
		_, err := c.emby.DeleteCollectionsByIdItems(ctx, collectionID, emby.DeleteCollectionsByIdItemsOperationOptions{Ids: strings.Join(itemIDs, ",")})
		return err
	}

	_, err := c.jf.RemoveFromCollection(ctx, collectionID, jf.RemoveFromCollectionOperationOptions{Ids: itemIDs})

	return err
}
