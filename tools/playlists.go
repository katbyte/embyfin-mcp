package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolveByType finds an item of the given type by id or by name
// (case-insensitive) — used for playlists and collections. Names need not be
// unique, so a name several share is an error that lists their ids.
func resolveByType(ctx context.Context, client *embyfin.Client, itemType, nameOrID string) (*embyfin.Item, error) {
	items, _, err := client.Search(ctx, embyfin.SearchOptions{IncludeItemTypes: itemType, Fields: embyfin.FieldsLean})
	if err != nil {
		return nil, err
	}

	kind := strings.ToLower(itemType)
	if itemType == "BoxSet" {
		kind = "collection"
	}
	names := make([]string, 0, len(items))
	var named []*embyfin.Item
	for i := range items {
		if items[i].ID == nameOrID {
			return &items[i], nil
		}
		if strings.EqualFold(items[i].Name, nameOrID) {
			named = append(named, &items[i])
		}
		names = append(names, items[i].Name)
	}
	switch len(named) {
	case 1:
		return named[0], nil
	case 0:
		return nil, fmt.Errorf("no %s named %q (have: %s)", kind, nameOrID, strings.Join(names, ", "))
	}
	ids := make([]string, 0, len(named))
	for _, it := range named {
		ids = append(ids, it.ID)
	}

	return nil, fmt.Errorf("%d %ss are named %q (ids %s): pass an id", len(named), kind, nameOrID, strings.Join(ids, ", "))
}

func registerPlaylistTools(r *registry) {
	client := r.client
	type playlistRow struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type listOut struct {
		Playlists []playlistRow `json:"playlists"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "playlist_list",
		Description: "List all playlists.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, listOut, error) {
		items, _, err := client.Search(ctx, embyfin.SearchOptions{IncludeItemTypes: "Playlist", Fields: embyfin.FieldsLean})
		if err != nil {
			return nil, listOut{}, err
		}

		out := listOut{}
		for _, it := range items {
			out.Playlists = append(out.Playlists, playlistRow{ID: it.ID, Name: it.Name})
		}

		return nil, out, nil
	})

	type getIn struct {
		Playlist string `json:"playlist"       jsonschema:"playlist name (case-insensitive) or id"`
		User     string `json:"user,omitempty" jsonschema:"a user name or id, to list the playlist as that user sees it, leaving out entries of items they cannot see; left out, every entry"`
	}
	type entryRow struct {
		itemSummary
		EntryID string `json:"entry_id" jsonschema:"pass to playlist_remove or playlist_edit, with id as the item it holds"`
	}
	entryRows := func(entries []embyfin.Item) []entryRow {
		out := make([]entryRow, 0, len(entries))
		for i := range entries {
			out = append(out, entryRow{itemSummary: summarise(&entries[i]), EntryID: entries[i].PlaylistItemID})
		}
		return out
	}
	type getOut struct {
		Name        string     `json:"name"`
		Entries     []entryRow `json:"entries"               jsonschema:"in playlist order"`
		Fingerprint string     `json:"fingerprint,omitempty" jsonschema:"the playlist's entries as they are now, their ids and items in order, named in one value: pass it to playlist_remove or playlist_edit, which need it for an item the playlist holds more than once and refuse a change when the playlist is no longer so. Left out when the whole playlist could not be read"`
		Note        string     `json:"note,omitempty"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "playlist_get",
		Description: "A playlist's entries in order, each with its entry id, and the playlist's fingerprint: what playlist_remove and playlist_edit name an entry by. Every entry is listed, whoever can see it - on Jellyfin read in the view of an administrator who sees every library, and refused when there is none - or, with user, the entries that user sees; the fingerprint is always of every entry, and left out, saying why, when the whole playlist cannot be read.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		pl, err := resolveByType(ctx, client, "Playlist", in.Playlist)
		if err != nil {
			return nil, getOut{}, err
		}
		// the fingerprint is of every entry, which is what a change checks;
		// a user's own view needs no whole read, only the fingerprint does
		held, err := client.PlaylistHeld(ctx, pl.ID)
		if in.User == "" {
			if err != nil {
				return nil, getOut{}, err
			}
			return nil, getOut{Name: pl.Name, Entries: entryRows(held), Fingerprint: embyfin.PlaylistFingerprint(held)}, nil
		}
		out := getOut{Name: pl.Name}
		switch {
		case errors.Is(err, embyfin.ErrNoFullView):
			out.Note = "no fingerprint: " + err.Error() + ", and a change needs it only for an item the playlist holds more than once; the entries are the ones " + in.User + " sees"
		case err != nil:
			return nil, getOut{}, err
		default:
			out.Fingerprint = embyfin.PlaylistFingerprint(held)
		}
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, getOut{}, err
		}
		entries, _, err := client.PlaylistItems(ctx, pl.ID, user.ID)
		if err != nil {
			return nil, getOut{}, err
		}
		out.Entries = entryRows(entries)

		return nil, out, nil
	})

	type createIn struct {
		Name      string   `json:"name"                 jsonschema:"name for the new playlist"`
		ItemIDs   []string `json:"item_ids,omitempty"   jsonschema:"initial items, in order"`
		MediaType string   `json:"media_type,omitempty" jsonschema:"Video or Audio; defaults to server inference"`
		User      string   `json:"user,omitempty"       jsonschema:"the playlist's owner, by name or id; defaults to the first administrator"`
	}
	type createOut struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Entries     int    `json:"entries"     jsonschema:"entries the new playlist holds, read back"`
		Fingerprint string `json:"fingerprint" jsonschema:"the playlist's entries as they are now, their ids and items in order, named in one value: pass it to playlist_remove or playlist_edit, which need it for an item the playlist holds more than once and refuse a change when the playlist is no longer so"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "playlist_create",
		Description: "Create a new playlist for a user, optionally pre-filled with items, of which the user must be able to see every one. A series, season, album or artist given as an item puts every item under it in the playlist, an entry each, rather than itself. " +
			"On Emby the first playlist made on a server starts a library scan, which drops items whose files are gone and re-reads changed files. It checks a moment later that the playlist still holds what it was made with, sending what it lost once more, and answers how many entries it holds.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, createOut, error) {
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, createOut{}, err
		}

		if err := visibleToAll(ctx, client, user, in.ItemIDs); err != nil {
			return nil, createOut{}, err
		}

		id, err := client.CreatePlaylist(ctx, in.Name, in.ItemIDs, in.MediaType, user.ID)
		if err != nil {
			return nil, createOut{}, err
		}
		if len(in.ItemIDs) > 0 {
			if err := client.KeepPlaylistEntries(ctx, id, user.ID, in.ItemIDs); err != nil {
				return nil, createOut{}, fmt.Errorf("the playlist %s was made (id %s), but checking it kept its items failed: %w", in.Name, id, err)
			}
		}
		entries, err := client.PlaylistHeld(ctx, id)
		if err != nil {
			return nil, createOut{}, fmt.Errorf("the playlist %s was made (id %s), but reading it back failed: %w", in.Name, id, err)
		}

		return nil, createOut{ID: id, Name: in.Name, Entries: len(entries), Fingerprint: embyfin.PlaylistFingerprint(entries)}, nil
	})

	type addIn struct {
		Playlist string   `json:"playlist"       jsonschema:"playlist name or id"`
		ItemIDs  []string `json:"item_ids"       jsonschema:"library item ids to append"`
		User     string   `json:"user,omitempty" jsonschema:"user name or id acting on the playlist; defaults to the first administrator"`
	}
	type addOut struct {
		Added       int    `json:"added"`
		To          string `json:"to"`
		Fingerprint string `json:"fingerprint" jsonschema:"the playlist's entries as they are now, their ids and items in order, named in one value: pass it to playlist_remove or playlist_edit, which need it for an item the playlist holds more than once and refuse a change when the playlist is no longer so"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "playlist_add",
		Description: "Append items to a playlist, each of which the user acting on it must be able to see. A series, season, album or artist puts every item under it in, an entry each; an item already there gets a second entry. It checks a moment later that the entries stayed (a library scan saves a playlist as it found it, on Emby), sending what was lost once more, and answers how many the playlist gained.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, addOut, error) {
		pl, err := resolveByType(ctx, client, "Playlist", in.Playlist)
		if err != nil {
			return nil, addOut{}, err
		}
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, addOut{}, err
		}

		if err := visibleToAll(ctx, client, user, in.ItemIDs); err != nil {
			return nil, addOut{}, err
		}

		// counted by what the playlist holds before and after, not by what
		// was asked: the server may fold or drop an entry
		before, err := client.PlaylistHeld(ctx, pl.ID)
		if err != nil {
			return nil, addOut{}, err
		}
		if err := client.AddToPlaylist(ctx, pl.ID, in.ItemIDs, user.ID); err != nil {
			return nil, addOut{}, err
		}
		after, err := client.PlaylistHeld(ctx, pl.ID)
		if err != nil {
			return nil, addOut{}, fmt.Errorf("added %s to %s, and it kept them, but reading the playlist back to count them failed: %w", strings.Join(in.ItemIDs, ", "), pl.Name, err)
		}

		return nil, addOut{Added: max(len(after)-len(before), 0), To: pl.Name, Fingerprint: embyfin.PlaylistFingerprint(after)}, nil
	})

	type removeIn struct {
		Playlist    string   `json:"playlist"              jsonschema:"playlist name or id"`
		EntryIDs    []string `json:"entry_ids"             jsonschema:"entry ids from playlist_get (not item ids)"`
		ItemIDs     []string `json:"item_ids"              jsonschema:"the item each of entry_ids holds, by its id as playlist_get listed it, in the same order: an entry holding another item now is refused"`
		Fingerprint string   `json:"fingerprint,omitempty" jsonschema:"the playlist's fingerprint from playlist_get or the last change's answer: needed when an item named is in the playlist more than once, and refused when the playlist has changed since"`
		AllCopies   bool     `json:"all_copies,omitempty"  jsonschema:"on Jellyfin, which names every copy of an item by one entry id and removes them together: true to remove every copy an entry id names; left out, such an entry is refused"`
		User        string   `json:"user,omitempty"        jsonschema:"the user acting on the playlist, which the removal does not need: every entry is checked, whoever can see it"`
	}
	type removedEntry struct {
		memberRow
		Position int `json:"position" jsonschema:"where the entry was, 1 for the top"`
	}
	type removeOut struct {
		Removed     int            `json:"removed"            jsonschema:"entries that left the playlist"`
		From        string         `json:"from"`
		Items       []removedEntry `json:"items"              jsonschema:"the entries taken out: each item's id and name and where it was, to put back with playlist_add and playlist_edit"`
		Appeared    []memberRow    `json:"appeared,omitempty" jsonschema:"entries that appeared in the playlist while the removal ran - someone else's add - which it was not asked about and left in"`
		Entries     []entryRow     `json:"entries"            jsonschema:"the playlist's entries after, in order, with the entry ids to use next"`
		Fingerprint string         `json:"fingerprint"        jsonschema:"the playlist's entries as they are now, their ids and items in order, named in one value: pass it to playlist_remove or playlist_edit, which need it for an item the playlist holds more than once and refuse a change when the playlist is no longer so"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "playlist_remove",
		Description: "Remove entries from a playlist (the items stay in the library). Each entry is named by its entry id and the item it holds, both from playlist_get. An entry id names a place in the playlist, and the places are numbered again as it changes: Emby 4.11 numbers the entries 1 to n a moment after every add or removal, 4.10 when it refreshes the playlist (a library scan does). So an entry id read before a change can name another entry, and an entry id the playlist does not hold, or one holding another item now, is refused with nothing removed. An item the playlist holds more than once also needs the playlist's fingerprint from playlist_get, since an old id of one of its entries can name another; a fingerprint given is refused, with nothing removed, when the playlist has changed since. On Jellyfin an item's entries share its id, so removing one entry of an item the playlist holds twice removes both: that is refused unless all_copies is given. The playlist is read twice, a moment apart, before anything is sent, and refused when the two differ. " +
			"A removal a library scan undoes by saving the playlist back as it was is sent once more, for the entries at the places asked for; anything added meanwhile is left in, and named in appeared. The answer names each item taken out, by id and name, and where it was, so it can be put back, and lists the entries as they are after, with the fingerprint; anything else the playlist lost meanwhile is an error naming it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in removeIn) (*mcp.CallToolResult, removeOut, error) {
		if len(in.EntryIDs) == 0 {
			return nil, removeOut{}, errors.New("entry_ids is empty: nothing to remove")
		}
		if len(in.ItemIDs) != len(in.EntryIDs) {
			return nil, removeOut{}, fmt.Errorf("%d entry ids and %d item ids: give item_ids the item each entry holds, in the same order, as playlist_get lists them", len(in.EntryIDs), len(in.ItemIDs))
		}
		pl, err := resolveByType(ctx, client, "Playlist", in.Playlist)
		if err != nil {
			return nil, removeOut{}, err
		}
		// a user named must be one the server has; the removal needs none,
		// and every entry is checked whoever can see it
		if in.User != "" {
			if _, err := client.ResolveUser(ctx, in.User); err != nil {
				return nil, removeOut{}, err
			}
		}

		removal, err := client.RemoveFromPlaylist(ctx, pl.ID, embyfin.EntriesToRemove{EntryIDs: in.EntryIDs, ItemIDs: in.ItemIDs, Fingerprint: in.Fingerprint, AllCopies: in.AllCopies})
		if err != nil {
			return nil, removeOut{}, err
		}
		out := removeOut{Removed: len(removal.Removed), From: pl.Name, Items: make([]removedEntry, 0, len(removal.Removed))}
		gone := make([]memberRow, 0, len(removal.Removed))
		for _, e := range removal.Removed {
			gone = append(gone, memberRow{ID: e.Item.ID, Name: e.Item.Name})
			out.Items = append(out.Items, removedEntry{memberRow: gone[len(gone)-1], Position: e.Position})
		}
		if len(removal.Appeared) > 0 {
			out.Appeared = memberRows(removal.Appeared)
		}
		entries, err := client.PlaylistHeld(ctx, pl.ID)
		if err != nil {
			return nil, removeOut{}, fmt.Errorf("removed from %s: %s; done, but reading the playlist back failed, so its entries as they are now are not known: %w", pl.Name, membersSaid(gone), err)
		}
		out.Entries, out.Fingerprint = entryRows(entries), embyfin.PlaylistFingerprint(entries)

		return nil, out, nil
	})

	type editIn struct {
		Playlist    string `json:"playlist"                jsonschema:"playlist name or id"`
		Name        string `json:"name,omitempty"          jsonschema:"rename the playlist"`
		MoveEntryID string `json:"move_entry_id,omitempty" jsonschema:"an entry id from playlist_get to move"`
		MoveItemID  string `json:"move_item_id,omitempty"  jsonschema:"the item that entry holds, by its id as playlist_get listed it: an entry holding another item now is refused"`
		Fingerprint string `json:"fingerprint,omitempty"   jsonschema:"the playlist's fingerprint from playlist_get or the last change's answer: needed to move an entry of an item the playlist holds more than once, and refused when the playlist has changed since"`
		Position    int    `json:"position,omitempty"      jsonschema:"where to move it, 1 for the top"`
		User        string `json:"user,omitempty"          jsonschema:"user name or id acting on the playlist; defaults to the first administrator"`
	}
	type editOut struct {
		Name        string     `json:"name"`
		Changed     []string   `json:"changed"`
		Entries     []entryRow `json:"entries"     jsonschema:"in playlist order, after the change"`
		Fingerprint string     `json:"fingerprint" jsonschema:"the playlist's entries as they are now, their ids and items in order, named in one value: pass it to playlist_remove or playlist_edit, which need it for an item the playlist holds more than once and refuse a change when the playlist is no longer so"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "playlist_edit",
		Description: "Rename a playlist, or move one of its entries to another position (1 is the top), naming the entry by its entry id and the item it holds, both from playlist_get. playlist_add and playlist_remove change what it holds. An entry id names a place in the playlist, and the places are numbered again as it changes: Emby 4.11 numbers the entries 1 to n a moment after every add or removal, 4.10 when it refreshes the playlist (a library scan does), so read entry ids just before, from playlist_get or the last answer; an entry id the playlist does not hold, or one holding another item now, is refused with nothing moved. An entry of an item the playlist holds more than once also needs the playlist's fingerprint, and a fingerprint given is refused when the playlist has changed since. " +
			"Emby moves the entry in place. Jellyfin's move is refused to an API key, so there every entry from the lower of the two positions to the end is taken out and put back in the new order, which gives them new entry ids; if putting them back fails, the error names each item taken out, in order, to add again. The answer lists the entries as they are after.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		if in.Name == "" && in.MoveEntryID == "" {
			return nil, editOut{}, errors.New("nothing to change: pass name, or move_entry_id and position")
		}
		if in.MoveEntryID != "" && in.Position < 1 {
			return nil, editOut{}, errors.New("position is required with move_entry_id, 1 for the top")
		}
		if in.MoveEntryID != "" && in.MoveItemID == "" {
			return nil, editOut{}, errors.New("move_item_id is required with move_entry_id: the id of the item that entry holds, as playlist_get lists it")
		}
		pl, err := resolveByType(ctx, client, "Playlist", in.Playlist)
		if err != nil {
			return nil, editOut{}, err
		}
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, editOut{}, err
		}

		out := editOut{Name: pl.Name, Changed: []string{}}
		if in.MoveEntryID != "" {
			if err := client.MovePlaylistEntry(ctx, pl.ID, user.ID, in.MoveEntryID, in.MoveItemID, in.Fingerprint, in.Position-1); err != nil {
				return nil, editOut{}, err
			}
			out.Changed = append(out.Changed, fmt.Sprintf("moved entry %s (item %s) to %d", in.MoveEntryID, in.MoveItemID, in.Position))
		}
		if in.Name != "" && in.Name != pl.Name {
			if err := client.RenamePlaylist(ctx, pl.ID, user.ID, in.Name); err != nil {
				if len(out.Changed) > 0 {
					return nil, editOut{}, fmt.Errorf("renaming to %s: %w; already done before it failed: %s", in.Name, err, strings.Join(out.Changed, ", "))
				}
				return nil, editOut{}, err
			}
			out.Changed = append(out.Changed, "renamed "+pl.Name+" to "+in.Name)
			out.Name = in.Name
		}

		entries, err := client.PlaylistHeld(ctx, pl.ID)
		if err != nil {
			return nil, editOut{}, fmt.Errorf("%s: done, but reading the playlist back failed, so its entries as they are now are not known: %w", strings.Join(out.Changed, ", "), err)
		}
		out.Entries, out.Fingerprint = entryRows(entries), embyfin.PlaylistFingerprint(entries)

		return nil, out, nil
	})

	type deleteIn struct {
		Playlist string `json:"playlist" jsonschema:"playlist name or id"`
	}
	type deleteOut struct {
		Deleted string      `json:"deleted"`
		ID      string      `json:"id"`
		Held    []memberRow `json:"held"    jsonschema:"the playlist's entries in order, each item by id and name, to make it again with playlist_create"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "playlist_delete",
		Description: "Delete a playlist, which cannot be undone: the items stay in the library and only the list goes, with its order and name. held in the answer lists every entry in order, each item by id and name, to make it again with playlist_create, which gives it a new id and new entry ids; on Jellyfin, which lists a playlist only in a user's view, one that cannot be read whole - no administrator sees every library - is not deleted.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		pl, err := resolveByType(ctx, client, "Playlist", in.Playlist)
		if err != nil {
			return nil, deleteOut{}, err
		}
		// every entry, whoever can see it: the only record of what went
		entries, err := client.PlaylistHeld(ctx, pl.ID)
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("could not read everything %s holds, so it was not deleted: %w", pl.Name, err)
		}
		held := memberRows(entries)

		if err := client.DeleteItem(ctx, pl.ID); err != nil {
			still := "it is still there, read back"
			if _, lerr := resolveByType(ctx, client, "Playlist", pl.ID); lerr != nil {
				still = "read back, it is gone"
				if !strings.Contains(lerr.Error(), "no playlist named") {
					still = "reading whether it is still there failed: " + lerr.Error()
				}
			}
			return nil, deleteOut{}, fmt.Errorf("the delete of %s answered an error: %w; %s. It held: %s", pl.Name, err, still, membersSaid(held))
		}

		return nil, deleteOut{Deleted: pl.Name, ID: pl.ID, Held: held}, nil
	})
}

// visibleToAll checks that the user a playlist is changed for can see every
// item going into it. Neither server does: Emby keeps the item in the
// playlist where only an administrator sees it, and Jellyfin keeps it and
// shows it to the restricted user, so a playlist was a way round the
// libraries an account was given. item_set_state checks the same (visibleTo).
func visibleToAll(ctx context.Context, client *embyfin.Client, user *embyfin.User, ids []string) error {
	for _, id := range ids {
		_, seen, err := client.VisibleUserItem(ctx, user.ID, id)
		if err != nil {
			return err
		}
		if seen {
			continue
		}
		name := id
		if it, err := client.ItemByID(ctx, id); err != nil {
			name += " (its name could not be read: " + err.Error() + ")"
		} else {
			name = it.Name + " (" + id + ")"
		}

		return fmt.Errorf("%s cannot see %s: it is in a library they have no access to, or rated above what they may watch, and a playlist of theirs takes only what they can see", user.Name, name)
	}

	return nil
}
