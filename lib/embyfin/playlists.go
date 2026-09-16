package embyfin

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
)

// CreatePlaylist makes a new playlist owned by userID containing the given
// items and returns its id. mediaType is Video or Audio (empty lets the
// server infer). Emby takes the fields as query parameters; Jellyfin wants a
// JSON body and answers 400 to the query form.
func (c *Client) CreatePlaylist(ctx context.Context, name string, itemIDs []string, mediaType, userID string) (string, error) {
	if c.isEmby() {
		res, err := c.emby.PostPlaylists(ctx, emby.PostPlaylistsOperationOptions{
			Name: name, Ids: strings.Join(itemIDs, ","), MediaType: mediaType, UserId: userID,
		})
		if err != nil {
			return "", err
		}

		return res.Model.Id, nil
	}

	res, err := c.jf.CreatePlaylist(ctx, jf.CreatePlaylistDto{
		Name: name, Ids: itemIDs, UserId: userID, MediaType: jf.MediaType(mediaType),
	})
	if err != nil {
		return "", err
	}

	return res.Model.Id, nil
}

// PlaylistItems returns a playlist's entries in order, as seen by userID.
// Each item carries PlaylistItemID, which is what removal requires.
func (c *Client) PlaylistItems(ctx context.Context, playlistID, userID string) ([]Item, int, error) {
	if c.isEmby() {
		res, err := c.emby.GetPlaylistsByIdItems(ctx, playlistID, emby.GetPlaylistsByIdItemsOperationOptions{UserId: userID, Fields: FieldsDefault})
		if err != nil {
			return nil, 0, err
		}

		return itemsFromEmby(res.Model.Items), res.Model.TotalRecordCount, nil
	}

	res, err := c.jf.GetPlaylistItems(ctx, playlistID, jf.GetPlaylistItemsOperationOptions{UserId: userID, Fields: list[jf.ItemFields](FieldsDefault)})
	if err != nil {
		return nil, 0, err
	}

	return itemsFromJF(res.Model.Items), res.Model.TotalRecordCount, nil
}

// AddToPlaylist appends items (by item id) to a playlist on behalf of
// userID, who must be allowed to edit it (its owner is), and checks they
// stay. A library scan validates every playlist, and on Emby one that is
// running when an add lands saves the playlist as it was before: the add is
// answered, logged and saved, then lost (the first playlist on a server
// queues such a scan itself). What has not stayed is sent once more before
// it is an error. Changes to one playlist from this process are made one at a
// time (see keyedLocks), so a check never sees another change's entries land.
func (c *Client) AddToPlaylist(ctx context.Context, playlistID string, itemIDs []string, userID string) error {
	unlock := c.items.lock(playlistID)
	defer unlock()

	before, _, err := c.PlaylistItems(ctx, playlistID, userID)
	if err != nil {
		return err
	}
	want := itemCounts(before)
	for _, id := range itemIDs {
		want[id]++
	}

	missing := itemIDs
	for range 2 {
		if err := c.addItems(ctx, playlistID, missing, userID); err != nil {
			return err
		}
		if missing, err = c.playlistMissing(ctx, playlistID, userID, want); err != nil || len(missing) == 0 {
			return err
		}
	}

	return fmt.Errorf("the server did not keep %s in the playlist", strings.Join(missing, ", "))
}

// playlistMissing waits for a playlist to hold at least want of each item,
// then looks again a moment later to see the entries stayed, and returns
// the item ids short of it (an id once per missing copy).
func (c *Client) playlistMissing(ctx context.Context, playlistID, userID string, want map[string]int) ([]string, error) {
	var missing []string
	held := false
	for range 20 {
		entries, _, err := c.PlaylistItems(ctx, playlistID, userID)
		if err != nil {
			return nil, err
		}
		have := itemCounts(entries)
		missing = missing[:0]
		for id, n := range want {
			for range n - have[id] {
				missing = append(missing, id)
			}
		}
		slices.Sort(missing)
		switch {
		case len(missing) > 0 && held:
			// it held, then a refresh put the playlist back
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

// itemCounts counts each item's entries in a playlist.
func itemCounts(entries []Item) map[string]int {
	n := make(map[string]int, len(entries))
	for i := range entries {
		n[entries[i].ID]++
	}

	return n
}

func (c *Client) addItems(ctx context.Context, playlistID string, itemIDs []string, userID string) error {
	if c.isEmby() {
		_, err := c.emby.PostPlaylistsByIdItems(ctx, playlistID, emby.PostPlaylistsByIdItemsOperationOptions{Ids: strings.Join(itemIDs, ","), UserId: userID})
		return err
	}

	_, err := c.jf.AddItemToPlaylist(ctx, playlistID, jf.AddItemToPlaylistOperationOptions{Ids: itemIDs, UserId: userID})

	return err
}

// RemoveFromPlaylist removes entries by their PlaylistItemID (not item id),
// as seen by userID, and returns how many entries left. Both servers answer
// the removal of an entry the playlist does not hold with a 204 and change
// nothing, so the entries are checked first and an unknown one is an error.
// Jellyfin's entry id is the item's id, so a playlist holding an item twice
// lists both entries under one id and removing it removes both; Emby numbers
// each entry.
func (c *Client) RemoveFromPlaylist(ctx context.Context, playlistID, userID string, entryIDs []string) (int, error) {
	unlock := c.items.lock(playlistID)
	defer unlock()

	entries, err := c.playlistEntries(ctx, playlistID, userID, entryIDs)
	if err != nil {
		return 0, err
	}
	removed := 0
	for i := range entries {
		if slices.Contains(entryIDs, entries[i].PlaylistItemID) {
			removed++
		}
	}

	return removed, c.removeEntries(ctx, playlistID, entryIDs)
}

func (c *Client) removeEntries(ctx context.Context, playlistID string, entryIDs []string) error {
	if c.isEmby() {
		_, err := c.emby.DeletePlaylistsByIdItems(ctx, playlistID, emby.DeletePlaylistsByIdItemsOperationOptions{EntryIds: strings.Join(entryIDs, ",")})
		return err
	}

	_, err := c.jf.RemoveItemFromPlaylist(ctx, playlistID, jf.RemoveItemFromPlaylistOperationOptions{EntryIds: entryIDs})

	return err
}

// playlistEntries reads a playlist's entries and checks it holds every one of
// entryIDs. Emby renumbers the entries 1..n whenever it refreshes the playlist
// (a library scan does), so an entry id read before that names another entry
// or none.
func (c *Client) playlistEntries(ctx context.Context, playlistID, userID string, entryIDs []string) ([]Item, error) {
	entries, _, err := c.PlaylistItems(ctx, playlistID, userID)
	if err != nil {
		return nil, err
	}
	have := make([]string, 0, len(entries))
	for _, e := range entries {
		have = append(have, e.PlaylistItemID)
	}
	for _, id := range entryIDs {
		if slices.Contains(have, id) {
			continue
		}
		if len(have) == 0 {
			return nil, fmt.Errorf("the playlist has no entry %s (it is empty)", id)
		}

		return nil, fmt.Errorf("the playlist has no entry %s (its entry ids are %s)", id, strings.Join(have, ", "))
	}

	return entries, nil
}

// RenamePlaylist renames a playlist the way any item is edited. Jellyfin's
// own playlist update checks access against the calling user, which an API
// key is not (it answers 400), so both servers take the item update.
func (c *Client) RenamePlaylist(ctx context.Context, playlistID, userID, name string) error {
	_, err := c.EditItem(ctx, userID, playlistID, func(full map[string]any) (bool, error) {
		full["Name"] = name
		return true, nil
	})

	return err
}

// MovePlaylistEntry moves an entry (a PlaylistItemID, not an item id) to a
// zero-based position in the playlist. Emby moves it in place. Jellyfin's
// move checks access against the calling user, which an API key is not (a
// 400), so there the entries are taken out and put back in the new order,
// which the key may do. An add made meanwhile from this process waits for the
// move (see keyedLocks): Emby would otherwise see the playlist change under
// the move, and Jellyfin's put-back would race it.
func (c *Client) MovePlaylistEntry(ctx context.Context, playlistID, userID, entryID string, newIndex int) error {
	unlock := c.items.lock(playlistID)
	defer unlock()

	entries, err := c.playlistEntries(ctx, playlistID, userID, []string{entryID})
	if err != nil {
		return err
	}
	if newIndex < 0 || newIndex >= len(entries) {
		return fmt.Errorf("position %d is outside the playlist's %d entries", newIndex, len(entries))
	}
	from := slices.IndexFunc(entries, func(e Item) bool { return e.PlaylistItemID == entryID })
	if from == newIndex {
		return nil
	}
	order := slices.Insert(slices.Delete(slices.Clone(entries), from, from+1), newIndex, entries[from])

	if c.isEmby() {
		return c.moveEmbyEntry(ctx, playlistID, userID, entries, order, from, newIndex)
	}

	entryIDs := make([]string, 0, len(entries))
	itemIDs := make([]string, 0, len(entries))
	for _, e := range order {
		entryIDs = append(entryIDs, e.PlaylistItemID)
		itemIDs = append(itemIDs, e.ID)
	}
	if err := c.removeEntries(ctx, playlistID, entryIDs); err != nil {
		return err
	}

	return c.addItems(ctx, playlistID, itemIDs, userID)
}

// moveEmbyEntry moves entries[from] to newIndex and waits to see order. Emby
// answers a move of an entry id it no longer holds with a 204, so a refresh
// that renumbers the playlist between reading the entries and the move leaves
// it as it was: the entry is then found again at its old position (a
// renumbering keeps the order) and moved once more. Anything else changing
// the playlist meanwhile is an error rather than a guess.
func (c *Client) moveEmbyEntry(ctx context.Context, playlistID, userID string, entries, order []Item, from, newIndex int) error {
	move := func(entryID string) error {
		id, err := strconv.ParseInt(entryID, 10, 64)
		if err != nil {
			return fmt.Errorf("entry id %q is not an Emby playlist entry id", entryID)
		}
		_, err = c.emby.PostPlaylistsByIdItemsByItemIdMoveByNewIndex(ctx, playlistID, id, newIndex)

		return err
	}
	entryID := entries[from].PlaylistItemID
	if err := move(entryID); err != nil {
		return err
	}

	retried := false
	for range 10 {
		now, _, err := c.PlaylistItems(ctx, playlistID, userID)
		if err != nil {
			return err
		}
		switch {
		case sameItems(now, order):
			return nil
		case !sameItems(now, entries):
			return fmt.Errorf("the playlist changed while entry %s was being moved", entryID)
		case !retried && now[from].PlaylistItemID != entryID:
			retried = true
			if err := move(now[from].PlaylistItemID); err != nil {
				return err
			}

			continue
		}

		if err := c.pause(ctx); err != nil {
			return err
		}
	}

	return fmt.Errorf("the server did not move entry %s", entryID)
}

// sameItems reports whether two entry lists hold the same items in the same
// order, whatever their entry ids.
func sameItems(a, b []Item) bool {
	return slices.EqualFunc(a, b, func(x, y Item) bool { return x.ID == y.ID })
}
