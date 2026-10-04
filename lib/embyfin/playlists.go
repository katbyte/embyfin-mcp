package embyfin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// PlaylistMembers lists the ids of the items a playlist holds, one per
// entry, every entry (see PlaylistHeld), read with the fewest fields: what
// finding the lists an item is in needs, across every playlist on a server.
func (c *Client) PlaylistMembers(ctx context.Context, playlistID string) ([]string, error) {
	items, err := c.readPlaylist(ctx, playlistID, FieldsLean)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(items))
	for i := range items {
		ids = append(ids, items[i].ID)
	}

	return ids, nil
}

// PlaylistHeld is every entry a playlist holds, in order. A read in a
// user's view leaves out the entries of items that user cannot see (seen on
// Emby 4.10 and Jellyfin 12.1: a playlist's episode, gone from the view of
// an administrator not given its show), so Emby's is read with no user,
// which lists them all, and Jellyfin's - which refuses a read with none - in
// the view of an account that sees everything (FullViewerID); with none, the
// read is ErrNoFullView.
func (c *Client) PlaylistHeld(ctx context.Context, playlistID string) ([]Item, error) {
	return c.readPlaylist(ctx, playlistID, FieldsDefault)
}

// readPlaylist is PlaylistHeld with the fields given.
func (c *Client) readPlaylist(ctx context.Context, playlistID, fields string) ([]Item, error) {
	if c.isEmby() {
		res, err := c.emby.GetPlaylistsByIdItems(ctx, playlistID, emby.GetPlaylistsByIdItemsOperationOptions{Fields: fields})
		if err != nil {
			return nil, err
		}

		return itemsFromEmby(orEmpty(res.Model).Items), nil
	}
	for _, fresh := range []bool{false, true} {
		viewer, err := c.FullViewerID(ctx, fresh)
		if err != nil {
			return nil, fmt.Errorf("can't read every entry of the playlist: %w", err)
		}
		res, err := c.jf.GetPlaylistItems(ctx, playlistID, jf.GetPlaylistItemsOperationOptions{UserId: viewer, Fields: list[jf.ItemFields](fields)})
		if err == nil || fresh {
			if err != nil {
				return nil, err
			}
			return itemsFromJF(res.Model.Items), nil
		}
		// the account kept may have gone or been narrowed: choose again
	}

	return nil, errors.New("unreachable")
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

	before, err := c.PlaylistHeld(ctx, playlistID)
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
		if missing, err = c.playlistMissing(ctx, playlistID, want); err != nil || len(missing) == 0 {
			return err
		}
	}

	return fmt.Errorf("the server did not keep %s in the playlist", strings.Join(missing, ", "))
}

// KeepPlaylistEntries checks a playlist just made holds what it was made
// with - a folder's items in the folder's place (see playlistAdds) - and
// still does a moment later: the library scan the first playlist on an Emby
// server queues saves it as the scan found it. What it lost is sent once
// more before it is an error.
func (c *Client) KeepPlaylistEntries(ctx context.Context, playlistID, userID string, itemIDs []string) error {
	unlock := c.items.lock(playlistID)
	defer unlock()

	adds, err := c.playlistAdds(ctx, playlistID, userID, itemIDs)
	if err != nil {
		return err
	}
	want := map[string]int{}
	for _, id := range adds {
		want[id]++
	}
	missing, err := c.playlistMissing(ctx, playlistID, want)
	if err != nil || len(missing) == 0 {
		return err
	}
	if err := c.addItems(ctx, playlistID, missing, userID); err != nil {
		return err
	}
	if missing, err = c.playlistMissing(ctx, playlistID, want); err != nil || len(missing) == 0 {
		return err
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
// error before anything is sent, as is one seen to change while it is read.
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
		read, err := c.ReadAll(ctx, under, ToAct, func(page []Item) bool {
			for i := range page {
				if !page[i].IsMissing {
					leaves = append(leaves, page[i].ID)
				}
			}
			return true
		})
		if err != nil {
			return nil, err
		}
		// what it holds is what the add is checked against: read while it
		// changed, that could send an item again that is gone, or pass an
		// add short of what the server put in
		if note := read.Changed(); note != "" {
			return nil, fmt.Errorf("%s %q (%s) changed while what it holds was read, so what the playlist should hold after the add cannot be told, and nothing was sent (%s): ask again", it.Type, it.Name, id, note)
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
func (c *Client) playlistMissing(ctx context.Context, playlistID string, want map[string]int) ([]string, error) {
	var missing []string
	held := false
	for range 20 {
		entries, err := c.PlaylistHeld(ctx, playlistID)
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

// RemovedEntry is an entry a removal took out: the item it held and where it
// was in the playlist, 1 for the top.
type RemovedEntry struct {
	Item     Item
	Position int
}

// PlaylistRemoval is what a removal did: the entries it took out, and the
// entries that appeared in the playlist while it ran, which it was not asked
// about and left in (someone else's add).
type PlaylistRemoval struct {
	Removed  []RemovedEntry
	Appeared []Item
}

// PlaylistFingerprint names a playlist's entries as they were read: each
// entry's id and the item it holds, in order. Any change to the playlist -
// an entry added, removed or moved, the entries numbered again - gives
// another.
func PlaylistFingerprint(entries []Item) string {
	h := sha256.New()
	for i := range entries {
		h.Write([]byte(entries[i].PlaylistItemID + "\x00" + entries[i].ID + "\x00"))
	}

	return hex.EncodeToString(h.Sum(nil))[:16]
}

// EntriesToRemove names the entries RemoveFromPlaylist takes out: each by its
// entry id and the item it holds (ItemIDs, in the same order), with the
// playlist's fingerprint as read, which an item held more than once needs.
type EntriesToRemove struct {
	EntryIDs, ItemIDs []string
	Fingerprint       string
	// AllCopies lets an entry id that names several entries take them all:
	// Jellyfin names every copy of an item by the item's id, and removes
	// them together
	AllCopies bool
}

// RemoveFromPlaylist removes entries by their PlaylistItemID (not item id),
// each named with the item it holds, and returns the entries it took out. An entry id names a place in
// the playlist that moves: Emby 4.11 numbers the entries 1 to n again a
// moment after every add or removal (seen on 4.11.0.4), and 4.10 when it
// refreshes the playlist (a library scan does), so an id read before a change
// can name another entry or none. Both servers answer the removal of an
// entry the playlist does not hold with a 204 and change nothing (4.11
// removes whatever entry now has the id), so each entry is checked first to
// be there and to hold the item given, and anything else is an error with
// nothing sent; so is an item named that the playlist holds more than once
// without the fingerprint of the playlist as read, which an old id of one of
// its entries could otherwise pass for another's (see playlistEntries).
// Jellyfin's entry id is the item's id, so a playlist holding an item twice
// lists both entries under one id and removing it removes both: that is an
// error unless AllCopies says to. Emby numbers each entry.
//
// Like an add, a removal that lands while a library scan is saving the
// playlist as it found it is answered and then lost. What the playlist
// should hold afterwards is checked, by item rather than by entry id, and
// when it holds exactly what it held before - the scan put it back - the
// removal is sent once more, for the entries at the places asked for; an
// item asked for back in any other way is an error rather than a guess at
// which of its entries to take. An item that appeared meanwhile and was not
// asked for is never removed: it is returned, for the answer to say. One that
// left besides those asked for - an id numbered again between the check and
// the removal names another entry - is an error naming it.
func (c *Client) RemoveFromPlaylist(ctx context.Context, playlistID string, asked EntriesToRemove) (PlaylistRemoval, error) {
	unlock := c.items.lock(playlistID)
	defer unlock()

	var out PlaylistRemoval
	entryIDs := asked.EntryIDs
	entries, err := c.playlistEntries(ctx, playlistID, entryIDs, asked.ItemIDs, asked.Fingerprint)
	if err != nil {
		return out, err
	}
	for _, id := range entryIDs {
		named := slices.DeleteFunc(slices.Clone(entries), func(e Item) bool { return e.PlaylistItemID != id })
		if len(named) > 1 && !asked.AllCopies {
			return out, fmt.Errorf("entry %s names all %d entries of %s (%s) in the playlist - Jellyfin names every copy of an item by the item's id, and removes them together - so removing it removes all %d: pass all_copies to do that. Nothing was changed", id, len(named), named[0].Name, named[0].ID, len(named))
		}
	}
	want := itemCounts(entries)
	askedFor := map[string]int{}
	var at []int
	gone := make([]Item, 0, len(entryIDs))
	for i := range entries {
		if !slices.Contains(entryIDs, entries[i].PlaylistItemID) {
			continue
		}
		out.Removed = append(out.Removed, RemovedEntry{Item: entries[i], Position: i + 1})
		gone = append(gone, entries[i])
		at = append(at, i)
		want[entries[i].ID]--
		askedFor[entries[i].ID]++
	}

	send := slices.Clone(entryIDs)
	var back []Item
	for attempt := range 2 {
		if err := c.removeEntries(ctx, playlistID, send); err != nil {
			if attempt > 0 {
				return PlaylistRemoval{}, fmt.Errorf("the removal of %s was answered, then the playlist held %s again (a library scan saves a playlist as it found it), and sending the removal once more failed: %w", namedEntries(gone), namedEntries(back), err)
			}
			return PlaylistRemoval{}, err
		}
		left, err := c.playlistOff(ctx, playlistID, want, askedFor)
		if err != nil {
			return PlaylistRemoval{}, fmt.Errorf("the removal of %s was sent, but reading the playlist back failed, so whether it landed is not known: %w", namedEntries(gone), err)
		}
		out.Appeared = left.appeared
		if len(left.short) > 0 {
			still := ""
			if len(left.back) > 0 {
				still = fmt.Sprintf(", and it still holds %s, which it was asked to take out", namedEntries(left.back))
			}
			return PlaylistRemoval{}, fmt.Errorf("after the removal of %s the playlist lost %s too, which it was not asked to take out%s: an entry id sent just as the entries were numbered again names another entry (Emby does it a moment after an add or a removal), or someone else changed the playlist. Put back what should be there with playlist_edit add_items; the playlist holds now, in order: %s", namedEntries(gone), namedIDs(entries, left.short), still, namedEntries(left.now))
		}
		if back = left.back; len(back) == 0 {
			return out, nil
		}
		if !sameItems(left.now, entries) {
			return PlaylistRemoval{}, fmt.Errorf("the removal of %s was answered, then the playlist held %s again, and not as it was before the removal, so which of its entries were the ones asked for cannot be told: it was not sent again. The playlist holds now, in order: %s", namedEntries(gone), namedEntries(back), namedEntries(left.now))
		}
		// the scan put the playlist back as it was: the entries asked for
		// are the ones at the same places, by their ids now
		send = send[:0]
		for _, i := range at {
			if !slices.Contains(send, left.now[i].PlaylistItemID) {
				send = append(send, left.now[i].PlaylistItemID)
			}
		}
	}

	return PlaylistRemoval{}, fmt.Errorf("the server did not keep the removal of %s: the playlist held %s again after it was sent twice (a library scan saves a playlist as it found it)", namedEntries(gone), namedEntries(back))
}

// playlistLeft is a playlist as a removal left it: what it holds, the
// entries of items asked for above what it should hold (the ones a refresh
// put back), the entries of other items above it (someone else's add), and
// the item ids below it (an id once per copy short).
type playlistLeft struct {
	now, back, appeared []Item
	short               []string
}

// playlistOff waits for a playlist to hold no more of each item asked for
// than want and no less of any, then looks again a moment later to see that
// it stayed so, and returns it as it is then. Entries of items not asked for
// beyond want do not hold the wait up: they are someone else's.
func (c *Client) playlistOff(ctx context.Context, playlistID string, want, asked map[string]int) (playlistLeft, error) {
	var left playlistLeft
	held := false
	for range 20 {
		now, err := c.PlaylistHeld(ctx, playlistID)
		if err != nil {
			return playlistLeft{}, err
		}
		left = playlistLeft{now: now}
		have := map[string]int{}
		for i := range now {
			have[now[i].ID]++
			if have[now[i].ID] <= want[now[i].ID] {
				continue
			}
			if asked[now[i].ID] > 0 {
				left.back = append(left.back, now[i])
			} else {
				left.appeared = append(left.appeared, now[i])
			}
		}
		for id, n := range want {
			for range n - have[id] {
				left.short = append(left.short, id)
			}
		}
		slices.Sort(left.short)
		off := len(left.back) > 0 || len(left.short) > 0
		switch {
		case held:
			return left, nil
		case !off:
			held = true
			for range 2 {
				if err := c.pause(ctx); err != nil {
					return playlistLeft{}, err
				}
			}

			continue
		}
		if err := c.pause(ctx); err != nil {
			return playlistLeft{}, err
		}
	}

	return left, nil
}

func (c *Client) removeEntries(ctx context.Context, playlistID string, entryIDs []string) error {
	if c.isEmby() {
		_, err := c.emby.DeletePlaylistsByIdItems(ctx, playlistID, emby.DeletePlaylistsByIdItemsOperationOptions{EntryIds: strings.Join(entryIDs, ",")})
		return err
	}

	_, err := c.jf.RemoveItemFromPlaylist(ctx, playlistID, jf.RemoveItemFromPlaylistOperationOptions{EntryIds: entryIDs})

	return err
}

// playlistEntries reads a playlist's entries and checks it holds each of
// entryIDs, holding the item at the same place in itemIDs. Emby 4.11 numbers
// the entries 1 to n again a moment after every add or removal, and 4.10
// whenever it refreshes the playlist (a library scan does), so an entry id
// read before names another entry or none, and both servers answer a change
// to an entry they do not hold with a 204, changing nothing.
//
// An entry holding the item asked for can still be another entry of that
// item, when the playlist holds it more than once: the second Dune moved up
// to the number the first one had. So an item held more than once needs the
// fingerprint of the playlist as it was read (PlaylistFingerprint), and a
// fingerprint given that is not the playlist's now is an error: it changed
// since.
//
// The playlist is read twice, a moment apart, and two reads that differ are
// an error: Emby 4.11 numbers the entries again a fraction of a second after
// a change, and a check made in that moment would pass entries whose ids are
// about to move under the change it lets through.
func (c *Client) playlistEntries(ctx context.Context, playlistID string, entryIDs, itemIDs []string, fingerprint string) ([]Item, error) {
	if len(entryIDs) == 0 {
		return nil, errors.New("no entry was named")
	}
	if len(itemIDs) != len(entryIDs) {
		return nil, fmt.Errorf("%d entry ids were given with %d item ids: each entry is named with the item it holds, so nothing was changed", len(entryIDs), len(itemIDs))
	}
	first, err := c.PlaylistHeld(ctx, playlistID)
	if err != nil {
		return nil, err
	}
	for range 2 {
		if err := c.pause(ctx); err != nil {
			return nil, err
		}
	}
	entries, err := c.PlaylistHeld(ctx, playlistID)
	if err != nil {
		return nil, err
	}
	if PlaylistFingerprint(first) != PlaylistFingerprint(entries) {
		return nil, fmt.Errorf("the playlist changed while it was read, a moment apart - its entries numbered again, or someone changing it - so nothing was changed: read it again with playlist_get. It holds now, in order: %s", entriesSaid(entries))
	}
	if now := PlaylistFingerprint(entries); fingerprint != "" && fingerprint != now {
		return nil, fmt.Errorf("the playlist changed since it was read (its fingerprint was %s and is %s now), so nothing was changed: read it again with playlist_get. It holds now, in order: %s", fingerprint, now, entriesSaid(entries))
	}
	have := make([]string, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		have = append(have, e.PlaylistItemID)
	}
	for i, id := range entryIDs {
		at := slices.Index(have, id)
		// the entry ids the item's copies go by: one on Jellyfin, which names
		// every copy by the item's id and removes them together
		var copies []string
		if at >= 0 {
			copies = entryIDsOf(slices.DeleteFunc(slices.Clone(entries), func(e Item) bool { return e.ID != itemIDs[i] }))
		}
		switch {
		case at >= 0 && entries[at].ID == itemIDs[i] && len(copies) > 1 && fingerprint == "":
			return nil, fmt.Errorf("the playlist holds %s (%s) more than once (entries %s), and an entry id read before a change can name another of its entries: pass the playlist's fingerprint from playlist_get, or from the answer of the last change, with the entry. Nothing was changed", entries[at].Name, itemIDs[i], strings.Join(copies, ", "))
		case at >= 0 && entries[at].ID == itemIDs[i]:
			continue
		case at >= 0:
			return nil, fmt.Errorf("entry %s holds %s (%s) now, not item %s%s, so nothing was changed: an entry id names a place in the playlist, which Emby numbers 1 to n again a moment after an add or a removal, so read the entries again with playlist_get", id, entries[at].Name, entries[at].ID, itemIDs[i], heldAt(entries, itemIDs[i]))
		case len(have) == 0:
			return nil, fmt.Errorf("the playlist has no entry %s (it is empty), so nothing was changed", id)
		}

		return nil, fmt.Errorf("the playlist has no entry %s (its entry ids are %s)%s, so nothing was changed", id, strings.Join(have, ", "), heldAt(entries, itemIDs[i]))
	}

	return entries, nil
}

// entriesSaid names a playlist's entries in order, each with its entry id.
func entriesSaid(entries []Item) string {
	if len(entries) == 0 {
		return "nothing"
	}
	names := make([]string, 0, min(len(entries), 30))
	for i := range entries[:min(len(entries), 30)] {
		names = append(names, fmt.Sprintf("entry %s %s (%s)", entries[i].PlaylistItemID, entries[i].Name, entries[i].ID))
	}
	if len(entries) > len(names) {
		names = append(names, fmt.Sprintf("and %d more", len(entries)-len(names)))
	}

	return strings.Join(names, ", ")
}

// heldAt says which entries hold an item now, for an error about an entry id
// that does not name it.
func heldAt(entries []Item, itemID string) string {
	var ids []string
	name := ""
	for i := range entries {
		if entries[i].ID == itemID && !slices.Contains(ids, entries[i].PlaylistItemID) {
			ids = append(ids, entries[i].PlaylistItemID)
			name = entries[i].Name
		}
	}
	switch len(ids) {
	case 0:
		return "; the playlist does not hold item " + itemID
	case 1:
		return fmt.Sprintf("; %s (%s) is entry %s now", name, itemID, ids[0])
	}

	return fmt.Sprintf("; %s (%s) is entries %s now", name, itemID, strings.Join(ids, ", "))
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

// MovePlaylistEntry moves an entry (a PlaylistItemID, not an item id), named
// with the item it holds, to a zero-based position in the playlist; an entry
// not there or holding another item is an error with nothing sent (see
// playlistEntries). Emby moves it in place. Jellyfin's
// move checks access against the calling user, which an API key is not (a
// 400), so there the entries from the lower of the two positions on (from
// higher still when an item among them has a copy above, see stretchStart)
// are taken out and put back in the new order, which the key may do; the put-back
// is checked and re-sent as an add is, since a scan's re-read of the
// playlist file can land between the two and put the old entries back. An
// add made meanwhile from this process waits for the move (see keyedLocks):
// Emby would otherwise see the playlist change under the move, and
// Jellyfin's put-back would race it.
func (c *Client) MovePlaylistEntry(ctx context.Context, playlistID, userID, entryID, itemID, fingerprint string, newIndex int) error {
	unlock := c.items.lock(playlistID)
	defer unlock()

	entries, err := c.playlistEntries(ctx, playlistID, []string{entryID}, []string{itemID}, fingerprint)
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
		return c.moveEmbyEntry(ctx, playlistID, entries, order, from, newIndex)
	}

	// everything before the stretch that moves stays where it is: from the
	// lower of the two positions, or from the first copy of an item in it
	lo := stretchStart(entries, min(from, newIndex))
	itemIDs := make([]string, 0, len(order)-lo)
	for i := lo; i < len(order); i++ {
		itemIDs = append(itemIDs, order[i].ID)
	}
	want := itemCounts(order)
	now := entries
	for attempt := range 2 {
		if attempt > 0 {
			if now, err = c.PlaylistHeld(ctx, playlistID); err != nil {
				return err
			}
		}
		switch {
		case sameItems(now, order):
			return nil
		case !tailReshuffled(now, entries, lo):
			// only after a put-back: the first pass reads the entries as checked
			return fmt.Errorf("the playlist changed while entry %s was being moved, beyond the entries %d to %d it took out and put back, so it was left as it is: it held, in order, %s, and holds now %s; check it with playlist_get", entryID, lo+1, len(entries), namedEntries(entries), namedEntries(now))
		}
		// the entry ids are read afresh: a put-back the server undid may
		// have renumbered them, or a refresh may have restored the old tail
		// beside the new one, and everything from lo on goes out again
		if err := c.removeEntries(ctx, playlistID, entryIDsOf(now[lo:])); err != nil {
			return fmt.Errorf("moving entry %s takes entries %d to %d out and puts them back in the new order, and taking them out failed, so some may be gone: %s: %w", entryID, lo+1, len(entries), namedEntries(order[lo:]), err)
		}
		if err := c.addItems(ctx, playlistID, itemIDs, userID); err != nil {
			return fmt.Errorf("moving entry %s took entries %d to %d out, and putting them back failed, so they are gone from the playlist until added again (playlist_edit add_items, in this order): %s: %w", entryID, lo+1, len(entries), namedEntries(order[lo:]), err)
		}
		missing, missErr := c.playlistMissing(ctx, playlistID, want)
		if missErr != nil {
			return fmt.Errorf("moving entry %s took entries %d to %d out and put them back, but reading the playlist back failed, so whether they all stayed is not known (they were, in this order: %s): %w", entryID, lo+1, len(entries), namedEntries(order[lo:]), missErr)
		}
		if len(missing) > 0 {
			return fmt.Errorf("the server did not keep %s in the playlist after moving entry %s: add them again with playlist_edit add_items", namedIDs(order, missing), entryID)
		}
	}
	// the last put-back is read back like the others
	if now, err = c.PlaylistHeld(ctx, playlistID); err != nil {
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
func (c *Client) moveEmbyEntry(ctx context.Context, playlistID string, entries, order []Item, from, newIndex int) error {
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
			now, err := c.PlaylistHeld(ctx, playlistID)
			if err != nil {
				return err
			}
			switch {
			case sameItems(now, order):
				return nil
			case !sameItems(now, entries):
				return fmt.Errorf("the playlist changed while entry %s was being moved, so the move was not sent again: it held, in order, %s, and holds now %s. Someone else changed it, or its entries were numbered again as the move was sent (Emby numbers them 1 to n a moment after an add or a removal) and the move took another entry; check it with playlist_get", entryID, namedEntries(entries), namedEntries(now))
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
	for i := range entries {
		e := &entries[i]
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
	for i := lo; i < len(now); i++ {
		if tail[now[i].ID] == 0 {
			return false
		}
	}

	return true
}

// namedEntries names a stretch of entries in order, each item by its name and
// id, which is what a caller needs to put them back.
func namedEntries(entries []Item) string {
	names := make([]string, 0, len(entries))
	for i := range entries {
		names = append(names, fmt.Sprintf("%s (%s)", entries[i].Name, entries[i].ID))
	}

	return strings.Join(names, ", ")
}

// namedIDs names item ids by the entries holding them, an id once per time
// it is given.
func namedIDs(entries []Item, ids []string) string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		name := id
		if i := slices.IndexFunc(entries, func(e Item) bool { return e.ID == id }); i >= 0 {
			name = fmt.Sprintf("%s (%s)", entries[i].Name, id)
		}
		names = append(names, name)
	}

	return strings.Join(names, ", ")
}

// sameItems reports whether two entry lists hold the same items in the same
// order, whatever their entry ids.
func sameItems(a, b []Item) bool {
	return slices.EqualFunc(a, b, func(x, y Item) bool { return x.ID == y.ID })
}
