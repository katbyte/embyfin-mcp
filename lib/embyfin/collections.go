package embyfin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

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
		if res.Model == nil {
			return "", fmt.Errorf("the server answered the creation of collection %q with nothing, not its id", name)
		}

		return res.Model.Id, nil
	}

	res, err := c.jf.CreateCollection(ctx, jf.CreateCollectionOperationOptions{Name: name, Ids: itemIDs})
	if err != nil {
		return "", err
	}

	return res.Model.Id, nil
}

// AddToCollection adds items to a collection and waits until it holds them.
// Jellyfin saves a collection's members as a list it reads and writes back,
// so two adds at once lose one (a journey saw five parallel adds keep four);
// changes to one collection from this process are made one at a time (see
// keyedLocks), and an add that a scan's refresh of the collection wrote over
// is sent once more before it is an error, as a removal is.
func (c *Client) AddToCollection(ctx context.Context, collectionID string, itemIDs []string) error {
	unlock := c.items.lock(collectionID)
	defer unlock()

	missing := itemIDs
	for range 2 {
		if err := c.addMembers(ctx, collectionID, missing); err != nil {
			return err
		}
		var err error
		if missing, err = c.collectionMissing(ctx, collectionID, itemIDs); err != nil || len(missing) == 0 {
			return err
		}
	}

	return fmt.Errorf("the server did not keep %s in the collection", strings.Join(missing, ", "))
}

// collectionMissing waits for a collection to hold every one of itemIDs,
// then looks again a moment later to see they stayed, and returns the ones
// it does not hold.
func (c *Client) collectionMissing(ctx context.Context, collectionID string, itemIDs []string) ([]string, error) {
	var missing []string
	held := false
	for range 20 {
		members, err := c.CollectionMembers(ctx, collectionID)
		if err != nil {
			return nil, err
		}
		missing = slices.DeleteFunc(slices.Clone(itemIDs), func(id string) bool { return slices.Contains(members, id) })
		switch {
		case len(missing) > 0 && held:
			// it held, then a refresh put the collection back
			return missing, nil
		case len(missing) == 0 && held:
			return nil, nil
		case len(missing) == 0:
			held = true
			for range 2 {
				if err := c.pause(ctx); err != nil {
					return nil, err
				}
			}

			continue
		}
		if err := c.pause(ctx); err != nil {
			return nil, err
		}
	}

	return missing, nil
}

func (c *Client) addMembers(ctx context.Context, collectionID string, itemIDs []string) error {
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

// collectionDeleteTries is how many deletes DeleteCollection sends before it
// gives up on a collection that keeps coming back, and collectionGoneChecks
// how many settle intervals it must then stay gone for: Jellyfin put one back
// half a second after the delete answered, under test.
const (
	collectionDeleteTries = 3
	collectionGoneChecks  = 12
)

// collectionRefreshPolls is how many settle intervals after a collection was
// last saved a refresh of it may still be running on Jellyfin, to save it
// back when it ends: 75 seconds at the default interval. Jellyfin refreshes a
// collection it has made or whose members changed; the refresh takes about a
// second when the provider answers, and a collection made under a name TMDB
// could not be asked about was saved back sixty seconds after it was made,
// when the refresh gave up (seen on Jellyfin 12.1).
const collectionRefreshPolls = 300

// providerSlowPolls is how many settle intervals the provider a collection's
// refresh asks may take to answer before it is taken to be failing: ten
// seconds at the default interval. It answers a search for a collection in
// a moment when it is well (seen on Jellyfin 12.1: the same request the
// refresh makes, answered in milliseconds), and in a minute when it fails.
const providerSlowPolls = 40

// CollectionDelete is what DeleteCollection saw: how long it watched for the
// collection to stay gone, why, and how often it came back.
type CollectionDelete struct {
	// Watched is how long the collection was watched after the delete
	Watched time.Duration
	// Recent is whether the collection was saved recently enough for a
	// refresh of it to be running, and SavedAgo how long before the delete
	// that was, to the second
	Recent   bool
	SavedAgo time.Duration
	// ProviderSlow is whether the provider the refresh asks was failing or
	// slow, asked the same question just before the delete, for a recent
	// collection: then the watch runs to the end of the window
	ProviderSlow bool
	// Back is how many times it came back and was deleted again
	Back int
}

// DeleteCollection deletes a collection and reads back that it is gone and
// stays gone. Jellyfin refreshes a collection it has just made (and one whose
// members changed), and a delete that lands while that refresh runs is undone
// when the refresh saves the collection again: the delete answers 204 and the
// collection is back when the refresh ends - half a second later, or a
// minute. A refresh only queued when the collection goes is dropped, and one
// can only be running for a while after the change that started it, which
// saved the collection. So on Jellyfin, for a collection saved within the
// last collectionRefreshPolls intervals (the server says when it last saved
// an item only to the second, found by halving: lastSavedWithin), the
// provider its refresh asks is asked the same question first: answering in a
// moment, a refresh running now ends in a moment too, and the watch is a
// few times that; slow or failing, the collection is watched until the
// window has passed since the save. Any other gets a few seconds. It is
// deleted again whenever it comes back, and an error says so when it keeps
// coming back. Emby has not been seen to bring one back, and gets the short
// watch.
func (c *Client) DeleteCollection(ctx context.Context, id string) (CollectionDelete, error) {
	var out CollectionDelete
	watch := collectionGoneChecks * c.settle
	if !c.isEmby() {
		window := collectionRefreshPolls * c.settle
		last, now, err := c.lastSavedWithin(ctx, id, window)
		if err != nil {
			return out, err
		}
		if !last.IsZero() {
			out.Recent, out.SavedAgo = true, now.Sub(last)
			took, ok := c.collectionProviderAnswers(ctx, id)
			out.ProviderSlow = !ok
			if ok {
				watch += 3 * took
			} else {
				watch = max(watch, window-out.SavedAgo)
			}
		}
	}

	start := time.Now()
	for try := range collectionDeleteTries {
		if err := c.DeleteItem(ctx, id); err != nil {
			held, herr := c.holds(ctx, id)
			if herr != nil || held {
				return out, err
			}
			// already gone: a delete that raced another
		}
		if try > 0 {
			out.Back++
		}
		back := false
		// the first delete is watched for the whole window, a delete again
		// for the rest of it or a few seconds, whichever is longer - and
		// never for fewer than collectionGoneChecks reads, which a slow
		// server's answers would otherwise eat the few seconds with
		until := time.Now().Add(max(time.Until(start.Add(watch)), collectionGoneChecks*c.settle))
		for checks := 0; checks < collectionGoneChecks || time.Now().Before(until); checks++ {
			if err := c.pause(ctx); err != nil {
				return out, err
			}
			held, err := c.holds(ctx, id)
			if err != nil {
				return out, err
			}
			if held {
				back = true

				break
			}
		}
		if !back {
			out.Watched = time.Since(start)

			return out, nil
		}
	}

	return out, fmt.Errorf("collection %s came back after each of %d deletes: the server saves it again from a refresh still running on it; try again in a minute", id, collectionDeleteTries)
}

// collectionProviderAnswers asks the provider a Jellyfin collection's
// refresh asks - a search for a collection of its name - and says how long it
// took, and whether it answered within providerSlowPolls intervals.
func (c *Client) collectionProviderAnswers(ctx context.Context, id string) (time.Duration, bool) {
	col, err := c.ItemByID(ctx, id)
	if err != nil {
		return 0, false
	}
	asked, cancel := context.WithTimeout(ctx, providerSlowPolls*c.settle)
	defer cancel()
	start := time.Now()
	_, err = c.jf.GetBoxSetRemoteSearchResults(asked, jf.BoxSetInfoRemoteSearchQuery{SearchInfo: &jf.BoxSetInfo{Name: col.Name}})
	took := time.Since(start)

	return took, err == nil && took < providerSlowPolls*c.settle
}

// holds says whether the server has an item of this id. Emby answers an Ids
// filter it cannot parse with the whole library, so the answer has to be the
// item asked for.
func (c *Client) holds(ctx context.Context, id string) (bool, error) {
	items, _, err := c.Search(ctx, SearchOptions{IDs: id, Fields: FieldsLean, Limit: 2})
	if err != nil {
		return false, err
	}

	return len(items) > 0 && items[0].ID == id, nil
}
