package embyfin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Waiting for a change the server makes in the background - a refresh, a
// match - to land on an item.
//
// Both servers answer a refresh (and the refresh a match starts) once it is
// queued, and run it a moment later: a tenth of a second on an idle server,
// longer behind a scan. The sign that it has run is the item being saved,
// which moves its etag. Jellyfin's etag moves on every save. Emby's follows
// the time the item was last saved, which it keeps to the second, so a save
// in the same second as the one before it leaves the etag as it was: a
// refresh that ran a moment after an edit could not be told from one that
// had not run (seen on Emby 4.10, where the item's people were refilled and
// the etag held). So on Emby a change is asked for only once the item has
// gone a whole second unsaved, and the save it causes is then always in a
// later second than any before it. Both servers write an item's people apart
// from its record and after it - Jellyfin a few at a time for a second or so
// - so a save has landed once the etag has moved and the etag and the
// people have both held for a moment.

// saveState is what tells a read of an item taken after the server saved it
// from one taken before.
type saveState struct {
	etag   string
	people int
}

// savedItem reads an item with its etag and people.
func (c *Client) savedItem(ctx context.Context, id string) (*Item, saveState, error) {
	items, _, err := c.Search(ctx, SearchOptions{IDs: id, Fields: FieldsDetail + ",Etag", Limit: 2})
	if err != nil {
		return nil, saveState{}, err
	}
	if len(items) == 0 || items[0].ID != id {
		return nil, saveState{}, fmt.Errorf("no item with id %s", id)
	}

	return &items[0], saveState{etag: items[0].Etag, people: len(items[0].People)}, nil
}

// unsavedFor reads an item until it has gone saveGrain without being saved,
// on Emby, so that a save asked for next moves its etag (see above); on
// Jellyfin the first read will do. It answers the item and its state as of
// the last read, which is what the save is then told apart from. A server
// that keeps saving the item is waited on a few grains at most, and taken as
// it last read.
func (c *Client) unsavedFor(ctx context.Context, id string) (*Item, saveState, error) {
	it, state, err := c.savedItem(ctx, id)
	if err != nil || !c.isEmby() || state.etag == "" {
		return it, state, err
	}
	for range 4 {
		if err := sleep(ctx, c.saveGrain); err != nil {
			return nil, saveState{}, err
		}
		again, now, err := c.savedItem(ctx, id)
		if err != nil {
			return nil, saveState{}, err
		}
		if now.etag == state.etag {
			return again, now, nil
		}
		it, state = again, now
	}

	return it, state, nil
}

// savePolls is how many settle intervals awaitSave reads for: a minute at
// the default interval, for a refresh queued behind others to run.
const savePolls = 240

// awaitSave reads an item until it has been saved since the state given and
// has then held still for a second or so, for at most savePolls reads. It
// answers the item as last read and whether the save was seen; an item that
// stops being there is an error. A server that sends no etag cannot be
// waited on, and is read once.
func (c *Client) awaitSave(ctx context.Context, id string, before saveState) (*Item, bool, error) {
	const steady = 4
	moved, held := false, 0
	last := before
	var it *Item
	for range savePolls {
		var now saveState
		var err error
		if it, now, err = c.savedItem(ctx, id); err != nil || before.etag == "" {
			return it, false, err
		}
		if now.etag != before.etag {
			moved = true
		}
		if moved {
			if now == last {
				held++
			} else {
				held = 0
			}
			if held >= steady {
				return it, true, nil
			}
		}
		last = now
		if err := c.pause(ctx); err != nil {
			return nil, false, err
		}
	}

	return it, moved, nil
}

// sleep waits d, or until the context ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// serverTime is the server's clock, from the Date header it answers with, to
// the second: a time the server compares against its own records (when an
// item was last saved) is the server's, not this machine's, which a
// container's virtual machine or a remote host drifts from.
func (c *Client) serverTime(ctx context.Context) (time.Time, error) {
	var resp *http.Response
	if c.isEmby() {
		res, err := c.emby.GetSystemInfoPublic(ctx)
		if err != nil {
			return time.Time{}, err
		}
		resp = res.HttpResponse
	} else {
		res, err := c.jf.GetPublicSystemInfo(ctx)
		if err != nil {
			return time.Time{}, err
		}
		resp = res.HttpResponse
	}
	if resp == nil {
		return time.Time{}, errors.New("the server's answer carried no Date header")
	}

	return http.ParseTime(resp.Header.Get("Date"))
}

// savedSince says whether the server has saved an item at or after a time
// by its clock. Emby answers a filter it cannot parse with everything, so
// the answer has to be the item asked for.
func (c *Client) savedSince(ctx context.Context, id string, since time.Time) (bool, error) {
	items, _, err := c.Search(ctx, SearchOptions{IDs: id, SavedSince: since.UTC().Format(time.RFC3339), Fields: FieldsLean, Limit: 2})
	if err != nil {
		return false, err
	}

	return len(items) > 0 && items[0].ID == id, nil
}

// lastSavedWithin is when the server last saved an item, to the second by its
// own clock, if that was within the window before now, and the server's time
// now; the zero time when it was not. The servers say when an item was saved
// only by answering whether it was saved since a time, so the time is found
// by halving the window.
func (c *Client) lastSavedWithin(ctx context.Context, id string, window time.Duration) (last, now time.Time, err error) {
	if now, err = c.serverTime(ctx); err != nil {
		return time.Time{}, time.Time{}, err
	}
	lo := now.Add(-window)
	recent, err := c.savedSince(ctx, id, lo)
	if err != nil || !recent {
		return time.Time{}, now, err
	}
	// saved at or after lo, and not after hi: halve until they are a second
	// apart
	hi := now.Add(time.Second)
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / 2).Truncate(time.Second)
		if !mid.After(lo) {
			break
		}
		after, err := c.savedSince(ctx, id, mid)
		if err != nil {
			return time.Time{}, now, err
		}
		if after {
			lo = mid
		} else {
			hi = mid
		}
	}

	return lo, now, nil
}
