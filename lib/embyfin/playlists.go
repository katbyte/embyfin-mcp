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
		if res.Model == nil {
			return "", fmt.Errorf("the server answered the creation of playlist %q with nothing, not its id", name)
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
		page := orEmpty(res.Model)

		return itemsFromEmby(page.Items), page.TotalRecordCount, nil
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
//
// Both servers put a folder (a series, a season, an album) in a playlist as
// the items beneath it rather than as itself, so what is checked is what the
// add puts in (see playlistAdds): the folder's own id never shows, and a
// check for it would send the folder again, putting every item in twice. What
// is sent again is only what is missing, item by item, never the folder.
func (c *Client) AddToPlaylist(ctx context.Context, playlistID string, itemIDs []string, userID string) error {
	unlock := c.items.lock(playlistID)
	defer unlock()

	before, _, err := c.PlaylistItems(ctx, playlistID, userID)
	if err != nil {
		return err
	}
	adds, err := c.playlistAdds(ctx, playlistID, userID, itemIDs)
	if err != nil {
		return err
	}
	want := itemCounts(before)
	for _, id := range adds {
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

// playlistAdds is what adding itemIDs to a playlist puts in it, an item id per
// entry. Both servers expand what they are handed (Playlist.GetPlaylistItems):
// an artist or a music genre becomes its songs, any other folder the items
// anywhere beneath it that are not folders, of the playlist's media type, and
// anything else is itself. The folders are told apart with one read of the
// items asked for; an id that read does not answer is taken as itself.
//
// A record the server keeps of an item it has no file for (a missing
// episode) is left out of what is expected, as is anything of another media
// type: whether a server puts those in is its own affair, and expecting less
// than it adds only checks less, where expecting more would send items it
// never meant to add. A folder holding nothing the playlist can take is an
// error before anything is sent.
func (c *Client) playlistAdds(ctx context.Context, playlistID, userID string, itemIDs []string) ([]string, error) {
	asked := slices.Compact(slices.Sorted(slices.Values(itemIDs)))
	// capped, as ItemByID's is, for Emby answering the whole library to an
	// id it cannot parse; only the ids asked for are read off the answer
	items, _, err := c.Search(ctx, SearchOptions{IDs: strings.Join(asked, ","), Fields: "Path", Limit: len(asked) + 1})
	if err != nil {
		return nil, err
	}
	expanded := map[string]*Item{}
	for i := range items {
		it := &items[i]
		if slices.Contains(asked, it.ID) && (it.IsFolder || it.Type == "MusicArtist" || it.Type == "MusicGenre") {
			expanded[it.ID] = it
		}
	}
	if len(expanded) == 0 {
		return itemIDs, nil
	}

	mediaType := ""
	adds := make([]string, 0, len(itemIDs))
	for _, id := range itemIDs {
		it := expanded[id]
		var under SearchOptions
		switch {
		case it == nil:
			adds = append(adds, id)
			continue
		case it.Type == "MusicArtist":
			under = SearchOptions{ArtistIDs: id, IncludeItemTypes: "Audio"}
		case it.Type == "MusicGenre":
			under = SearchOptions{Genres: []string{it.Name}, IncludeItemTypes: "Audio"}
		default:
			if mediaType == "" {
				if mediaType, err = c.playlistMediaType(ctx, userID, playlistID); err != nil {
					return nil, err
				}
			}
			under = SearchOptions{ParentID: id, Filters: "IsNotFolder", MediaTypes: mediaType}
		}
		// in the user's view, which is what the server expands in
		under.UserID, under.Fields = userID, "Path"
		var leaves []string
		if err := c.SearchAll(ctx, under, func(page []Item) bool {
			for i := range page {
				if !page[i].IsMissing {
					leaves = append(leaves, page[i].ID)
				}
			}
			return true
		}); err != nil {
			return nil, err
		}
		if len(leaves) == 0 {
			return nil, fmt.Errorf("%s %q (%s) holds nothing the playlist can take", it.Type, it.Name, id)
		}
		adds = append(adds, leaves...)
	}

	return adds, nil
}

// playlistMediaType is the media type a folder added to the playlist is
// narrowed to: the playlist's own, Audio or Video, or both when it names
// neither (both servers take the two from a folder at most).
func (c *Client) playlistMediaType(ctx context.Context, userID, playlistID string) (string, error) {
	full, err := c.FullItem(ctx, userID, playlistID)
	if err != nil {
		return "", err
	}
	if t, ok := full["MediaType"].(string); ok && (t == "Audio" || t == "Video") {
		return t, nil
	}

	return "Audio,Video", nil
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
//
// Like an add, a removal that lands while a library scan is saving the
// playlist as it found it is answered and then lost, so what the playlist
// should hold afterwards is checked, by item rather than by entry id (Emby
// renumbers the entries when it refreshes the playlist), and the removal is
// sent once more for the entries that came back before it is an error.
func (c *Client) RemoveFromPlaylist(ctx context.Context, playlistID, userID string, entryIDs []string) (int, error) {
	unlock := c.items.lock(playlistID)
	defer unlock()

	entries, err := c.playlistEntries(ctx, playlistID, userID, entryIDs)
	if err != nil {
		return 0, err
	}
	want := itemCounts(entries)
	removed := 0
	for i := range entries {
		if slices.Contains(entryIDs, entries[i].PlaylistItemID) {
			removed++
			want[entries[i].ID]--
		}
	}

	for range 2 {
		if err := c.removeEntries(ctx, playlistID, entryIDs); err != nil {
			return 0, err
		}
		extra, err := c.playlistExtra(ctx, playlistID, userID, want)
		if err != nil || len(extra) == 0 {
			return removed, err
		}
		// the entries holding the items that came back, by their ids now
		entryIDs = entryIDs[:0]
		for _, e := range extra {
			entryIDs = append(entryIDs, e.PlaylistItemID)
		}
	}

	return removed, fmt.Errorf("the server did not remove %d entries from the playlist", len(entryIDs))
}

// playlistExtra waits for a playlist to hold no more of each item than want,
// then looks again a moment later to see that it stayed so, and returns the
// entries above want (the ones a refresh put back).
func (c *Client) playlistExtra(ctx context.Context, playlistID, userID string, want map[string]int) ([]Item, error) {
	var extra []Item
	held := false
	for range 20 {
		entries, _, err := c.PlaylistItems(ctx, playlistID, userID)
		if err != nil {
			return nil, err
		}
		have := map[string]int{}
		extra = extra[:0]
		for i := range entries {
			have[entries[i].ID]++
			if have[entries[i].ID] > want[entries[i].ID] {
				extra = append(extra, entries[i])
			}
		}
		switch {
		case len(extra) > 0 && held:
			return extra, nil
		case len(extra) == 0 && held:
			return nil, nil
		case len(extra) == 0:
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

	return extra, nil
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
// 400), so there the entries from the lower of the two positions on (from
// higher still when an item among them has a copy above, see stretchStart)
// are taken out and put back in the new order, which the key may do; the put-back
// is checked and re-sent as an add is, since a scan's re-read of the
// playlist file can land between the two and put the old entries back. An
// add made meanwhile from this process waits for the move (see keyedLocks):
// Emby would otherwise see the playlist change under the move, and
// Jellyfin's put-back would race it.
func (c *Client) MovePlaylistEntry(ctx context.Context, playlistID, userID, entryID string, newIndex int) error {
	unlock := c.items.lock(playlistID)
	defer unlock()

	entries, err := c.playlistEntries(ctx, playlistID, userID, []string{entryID})
	if err != nil {
		return err
	}
	if newIndex < 0 || newIndex >= len(entries) {
		// counted from 1, as the caller asked for it
		return fmt.Errorf("position %d is outside the playlist's %d entries", newIndex+1, len(entries))
	}
	from := slices.IndexFunc(entries, func(e Item) bool { return e.PlaylistItemID == entryID })
	if from == newIndex {
		return nil
	}
	order := slices.Insert(slices.Delete(slices.Clone(entries), from, from+1), newIndex, entries[from])

	if c.isEmby() {
		return c.moveEmbyEntry(ctx, playlistID, userID, entries, order, from, newIndex)
	}

	// everything before the stretch that moves stays where it is: from the
	// lower of the two positions, or from the first copy of an item in it
	lo := stretchStart(entries, min(from, newIndex))
	itemIDs := make([]string, 0, len(order)-lo)
	for _, e := range order[lo:] {
		itemIDs = append(itemIDs, e.ID)
	}
	want := itemCounts(order)
	now := entries
	for attempt := range 2 {
		if attempt > 0 {
			if now, _, err = c.PlaylistItems(ctx, playlistID, userID); err != nil {
				return err
			}
		}
		switch {
		case sameItems(now, order):
			return nil
		case !tailReshuffled(now, entries, lo):
			return fmt.Errorf("the playlist changed while entry %s was being moved", entryID)
		}
		// the entry ids are read afresh: a put-back the server undid may
		// have renumbered them, or a refresh may have restored the old tail
		// beside the new one, and everything from lo on goes out again
		if err := c.removeEntries(ctx, playlistID, entryIDsOf(now[lo:])); err != nil {
			return err
		}
		if err := c.addItems(ctx, playlistID, itemIDs, userID); err != nil {
			return err
		}
		missing, missErr := c.playlistMissing(ctx, playlistID, userID, want)
		if missErr != nil {
			return missErr
		}
		if len(missing) > 0 {
			return fmt.Errorf("the server did not keep %s in the playlist after moving entry %s", strings.Join(missing, ", "), entryID)
		}
	}
	// the last put-back is read back like the others
	if now, _, err = c.PlaylistItems(ctx, playlistID, userID); err != nil {
		return err
	}
	if sameItems(now, order) {
		return nil
	}

	return fmt.Errorf("the server did not move entry %s", entryID)
}

// moveEmbyEntry moves entries[from] to newIndex and waits to see order. Emby
// answers a move of an entry id it no longer holds with a 204, so a refresh
// that renumbers the playlist between reading the entries and the move leaves
// it as it was: the entry is then found again at its old position (a
// renumbering keeps the order) and moved once more. A scan's refresh can
// also write the playlist back as it found it after a move it accepted, so
// an order that never changes is sent once more too. Anything else changing
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
	for range 3 {
		if err := move(entryID); err != nil {
			return err
		}
	wait:
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
			case now[from].PlaylistItemID != entryID:
				// renumbered under the move: the id sent no longer exists
				entryID = now[from].PlaylistItemID

				break wait
			}
			if err := c.pause(ctx); err != nil {
				return err
			}
		}
	}

	return fmt.Errorf("the server did not move entry %s", entryID)
}

// stretchStart is where the stretch of a Jellyfin playlist taken out and put
// back for a move begins, given the lower of the move's two positions.
// Jellyfin names an entry by its item's id and takes every copy of the item
// out at once, so a copy above the stretch would go with it and never come
// back: the stretch starts at the first copy of any item in it instead, and
// again at the first copy of anything that brings in, until no item in the
// stretch has a copy above it.
func stretchStart(entries []Item, lo int) int {
	for {
		in := itemCounts(entries[lo:])
		first := slices.IndexFunc(entries[:lo], func(e Item) bool { return in[e.ID] > 0 })
		if first < 0 {
			return lo
		}
		lo = first
	}
}

// entryIDsOf is the entry ids of entries, each once: on Jellyfin the copies
// of an item share one.
func entryIDsOf(entries []Item) []string {
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if !slices.Contains(ids, e.PlaylistItemID) {
			ids = append(ids, e.PlaylistItemID)
		}
	}

	return ids
}

// tailReshuffled says whether now is entries with only the tail from lo on
// changed, and changed only among the tail's own items: what a scan's
// refresh leaves when it writes the playlist back around a move (the old
// tail restored, beside or instead of the new one). Anything before lo
// changed, or an item that was never in the tail, is someone else's edit.
func tailReshuffled(now, entries []Item, lo int) bool {
	if len(now) < lo || !sameItems(now[:lo], entries[:lo]) {
		return false
	}
	tail := itemCounts(entries[lo:])
	for _, e := range now[lo:] {
		if tail[e.ID] == 0 {
			return false
		}
	}

	return true
}

// sameItems reports whether two entry lists hold the same items in the same
// order, whatever their entry ids.
func sameItems(a, b []Item) bool {
	return slices.EqualFunc(a, b, func(x, y Item) bool { return x.ID == y.ID })
}
