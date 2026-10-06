package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
		for i := range items {
			it := &items[i]
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
		EntryID string `json:"entry_id" jsonschema:"what playlist_edit takes out or moves an entry by, with id as the item it holds"`
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
		Fingerprint string     `json:"fingerprint,omitempty" jsonschema:"the playlist's entries as they are now, their ids and items in order, named in one value: pass it to playlist_edit, which needs it to remove or move an entry of an item the playlist holds more than once and refuses a change when the playlist is no longer so. Left out when the whole playlist could not be read"`
		Note        string     `json:"note,omitempty"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "playlist_get",
		Description: "A playlist's entries in order, each with its entry id, and the playlist's fingerprint: what playlist_edit names an entry by. Every entry is listed, whoever can see it - on Jellyfin read in the view of an administrator who sees every library, and refused when there is none - or, with user, the entries that user sees; the fingerprint is always of every entry, and left out, saying why, when the whole playlist cannot be read.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		pl, err := client.ResolveByType(ctx, "Playlist", in.Playlist)
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
		Fingerprint string `json:"fingerprint" jsonschema:"the playlist's entries as they are now, their ids and items in order, named in one value: pass it to playlist_edit, which needs it to remove or move an entry of an item the playlist holds more than once and refuses a change when the playlist is no longer so"`
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

	type removeEntry struct {
		EntryID string `json:"entry_id" jsonschema:"an entry id from playlist_get"`
		ItemID  string `json:"item_id"  jsonschema:"the item that entry holds, by its id as playlist_get listed it: an entry holding another item now is refused"`
	}
	type editIn struct {
		Playlist      string        `json:"playlist"                 jsonschema:"playlist name or id"`
		Name          string        `json:"name,omitempty"           jsonschema:"rename the playlist"`
		AddItems      []string      `json:"add_items,omitempty"      jsonschema:"library item ids to append, in order, each of which the user acting on it must be able to see; a series, season, album or artist puts every item under it in, an entry each, and an item already there gets a second entry"`
		RemoveEntries []removeEntry `json:"remove_entries,omitempty" jsonschema:"entries to take out (the items stay in the library), each by its entry id and the item it holds, both from playlist_get"`
		AllCopies     bool          `json:"all_copies,omitempty"     jsonschema:"on Jellyfin, which names every copy of an item by one entry id and removes them together: true to remove every copy an entry id in remove_entries names; left out, such an entry is refused"`
		MoveEntryID   string        `json:"move_entry_id,omitempty"  jsonschema:"an entry id from playlist_get to move; a move goes in a call of its own, with no add_items or remove_entries, since those number the entries again"`
		MoveItemID    string        `json:"move_item_id,omitempty"   jsonschema:"the item that entry holds, by its id as playlist_get listed it: an entry holding another item now is refused"`
		Position      int           `json:"position,omitempty"       jsonschema:"where to move it, 1 for the top"`
		Fingerprint   string        `json:"fingerprint,omitempty"    jsonschema:"the playlist's fingerprint from playlist_get or the last change's answer: needed to remove or move an entry of an item the playlist holds more than once, and refused when the playlist has changed since"`
		User          string        `json:"user,omitempty"           jsonschema:"user name or id acting on the playlist; defaults to the first administrator"`
	}
	type removedEntry struct {
		memberRow
		Position int `json:"position" jsonschema:"where the entry was, 1 for the top"`
	}
	type editOut struct {
		Name         string         `json:"name"`
		Changed      []string       `json:"changed"`
		Added        int            `json:"added,omitempty"         jsonschema:"entries the playlist gained"`
		Removed      int            `json:"removed,omitempty"       jsonschema:"entries that left the playlist"`
		RemovedItems []removedEntry `json:"removed_items,omitempty" jsonschema:"the entries taken out: each item's id and name and where it was, to put back with add_items and a move"`
		Appeared     []memberRow    `json:"appeared,omitempty"      jsonschema:"entries that appeared in the playlist while a removal ran - someone else's add - which it was not asked about and left in"`
		Entries      []entryRow     `json:"entries"                 jsonschema:"in playlist order, after the change, with the entry ids to use next"`
		Fingerprint  string         `json:"fingerprint"             jsonschema:"the playlist's entries as they are now, their ids and items in order, named in one value: pass it to playlist_edit, which needs it to remove or move an entry of an item the playlist holds more than once and refuses a change when the playlist is no longer so"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "playlist_edit",
		Description: "Change a playlist: rename it, append items to it, take entries out of it (the items stay in the library), or move one of its entries to another position (1 is the top), naming the entry by its entry id and the item it holds, both from playlist_get. Entries are taken out first, then items appended, then the entry moved, then the playlist renamed; a move goes in a call of its own, with no add_items or remove_entries. " +
			"Appended items must each be ones the user acting on the playlist can see. A series, season, album or artist puts every item under it in, an entry each; an item already there gets a second entry. It checks a moment later that the entries stayed (a library scan saves a playlist as it found it, on Emby), sending what was lost once more, and answers how many the playlist gained. " +
			"An entry id names a place in the playlist, and the places are numbered again as it changes: Emby 4.11 numbers the entries 1 to n a moment after every add or removal, 4.10 when it refreshes the playlist (a library scan does), so read entry ids just before, from playlist_get or the last answer; an entry id the playlist does not hold, or one holding another item now, is refused with nothing removed or moved. An entry of an item the playlist holds more than once also needs the playlist's fingerprint, since an old id of one of its entries can name another, and a fingerprint given is refused when the playlist has changed since. On Jellyfin an item's entries share its id, so removing one entry of an item the playlist holds twice removes both: that is refused unless all_copies is given. Before a removal the playlist is read twice, a moment apart, and the removal refused when the two differ; a removal a library scan undoes by saving the playlist back as it was is sent once more, for the entries at the places asked for, and anything added meanwhile is left in and named in appeared. " +
			"Emby moves the entry in place. Jellyfin's move is refused to an API key, so there every entry from the lower of the two positions to the end is taken out and put back in the new order, which gives them new entry ids; if putting them back fails, the error names each item taken out, in order, to add again. The answer names each entry taken out, by its item's id and name and where it was, so it can be put back, and lists the entries as they are after, with the fingerprint.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		switch {
		case in.Name == "" && in.MoveEntryID == "" && len(in.AddItems) == 0 && len(in.RemoveEntries) == 0:
			return nil, editOut{}, errors.New("nothing to change: pass name, add_items, remove_entries, or move_entry_id and position")
		case in.MoveEntryID != "" && (len(in.AddItems) > 0 || len(in.RemoveEntries) > 0):
			return nil, editOut{}, errors.New("a move goes in a call of its own: add_items and remove_entries number the entries again, so the entry to move would be another by then; nothing was changed")
		case in.MoveEntryID != "" && in.Position < 1:
			return nil, editOut{}, errors.New("position is required with move_entry_id, 1 for the top")
		case in.MoveEntryID != "" && in.MoveItemID == "":
			return nil, editOut{}, errors.New("move_item_id is required with move_entry_id: the id of the item that entry holds, as playlist_get lists it")
		}
		for i, e := range in.RemoveEntries {
			if e.EntryID == "" || e.ItemID == "" {
				return nil, editOut{}, fmt.Errorf("remove_entries[%d]: each entry needs its entry_id and the item_id it holds, as playlist_get lists them; nothing was changed", i)
			}
		}
		pl, err := client.ResolveByType(ctx, "Playlist", in.Playlist)
		if err != nil {
			return nil, editOut{}, err
		}
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, editOut{}, err
		}
		// the appended items are checked before anything is changed, so a
		// refusal changes nothing
		if err := visibleToAll(ctx, client, user, in.AddItems); err != nil {
			return nil, editOut{}, err
		}

		out := editOut{Name: pl.Name, Changed: []string{}}
		failed := func(what string, err error) error {
			if len(out.Changed) > 0 {
				return fmt.Errorf("%s: %w; already done before it failed: %s", what, err, strings.Join(out.Changed, ", "))
			}

			return err
		}
		if len(in.RemoveEntries) > 0 {
			asked := embyfin.EntriesToRemove{Fingerprint: in.Fingerprint, AllCopies: in.AllCopies}
			for _, e := range in.RemoveEntries {
				asked.EntryIDs = append(asked.EntryIDs, e.EntryID)
				asked.ItemIDs = append(asked.ItemIDs, e.ItemID)
			}
			removal, rerr := client.RemoveFromPlaylist(ctx, pl.ID, asked)
			if rerr != nil {
				return nil, editOut{}, rerr
			}
			out.Removed, out.RemovedItems = len(removal.Removed), make([]removedEntry, 0, len(removal.Removed))
			gone := make([]memberRow, 0, len(removal.Removed))
			for i := range removal.Removed {
				e := &removal.Removed[i]
				gone = append(gone, memberRow{ID: e.Item.ID, Name: e.Item.Name})
				out.RemovedItems = append(out.RemovedItems, removedEntry{memberRow: gone[len(gone)-1], Position: e.Position})
			}
			if len(removal.Appeared) > 0 {
				out.Appeared = memberRows(removal.Appeared)
			}
			out.Changed = append(out.Changed, "took out "+membersSaid(gone))
		}
		if len(in.AddItems) > 0 {
			// counted by what the playlist holds before and after, not by
			// what was asked: the server may fold or drop an entry
			before, rerr := client.PlaylistHeld(ctx, pl.ID)
			if rerr != nil {
				return nil, editOut{}, failed("reading the playlist before adding to it", rerr)
			}
			if err := client.AddToPlaylist(ctx, pl.ID, in.AddItems, user.ID); err != nil {
				return nil, editOut{}, failed("adding to "+pl.Name, err)
			}
			after, rerr := client.PlaylistHeld(ctx, pl.ID)
			if rerr != nil {
				return nil, editOut{}, failed(fmt.Sprintf("added %s to %s, and it kept them, but reading the playlist back to count them failed", strings.Join(in.AddItems, ", "), pl.Name), rerr)
			}
			out.Added = max(len(after)-len(before), 0)
			out.Changed = append(out.Changed, fmt.Sprintf("added %d entries", out.Added))
		}
		if in.MoveEntryID != "" {
			if err := client.MovePlaylistEntry(ctx, pl.ID, user.ID, in.MoveEntryID, in.MoveItemID, in.Fingerprint, in.Position-1); err != nil {
				return nil, editOut{}, failed("moving entry "+in.MoveEntryID, err)
			}
			out.Changed = append(out.Changed, fmt.Sprintf("moved entry %s (item %s) to %d", in.MoveEntryID, in.MoveItemID, in.Position))
		}
		if in.Name != "" && in.Name != pl.Name {
			if err := client.RenamePlaylist(ctx, pl.ID, user.ID, in.Name); err != nil {
				return nil, editOut{}, failed("renaming to "+in.Name, err)
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
		pl, err := client.ResolveByType(ctx, "Playlist", in.Playlist)
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
			if _, lerr := client.ResolveByType(ctx, "Playlist", pl.ID); lerr != nil {
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
