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
		User     string `json:"user,omitempty" jsonschema:"user name or id whose view to use; defaults to the first administrator"`
	}
	type entryRow struct {
		itemSummary
		EntryID string `json:"entry_id" jsonschema:"pass to playlist_remove or playlist_edit"`
	}
	type getOut struct {
		Name    string     `json:"name"`
		Entries []entryRow `json:"entries" jsonschema:"in playlist order"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "playlist_get",
		Description: "A playlist's entries in order.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		pl, err := resolveByType(ctx, client, "Playlist", in.Playlist)
		if err != nil {
			return nil, getOut{}, err
		}
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, getOut{}, err
		}

		entries, _, err := client.PlaylistItems(ctx, pl.ID, user.ID)
		if err != nil {
			return nil, getOut{}, err
		}

		out := getOut{Name: pl.Name}
		for i := range entries {
			out.Entries = append(out.Entries, entryRow{
				itemSummary: summarise(&entries[i]),
				EntryID:     entries[i].PlaylistItemID,
			})
		}

		return nil, out, nil
	})

	type createIn struct {
		Name      string   `json:"name"                 jsonschema:"name for the new playlist"`
		ItemIDs   []string `json:"item_ids,omitempty"   jsonschema:"initial items, in order"`
		MediaType string   `json:"media_type,omitempty" jsonschema:"Video or Audio; defaults to server inference"`
		User      string   `json:"user,omitempty"       jsonschema:"the playlist's owner, by name or id; defaults to the first administrator"`
	}
	type createOut struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "playlist_create",
		Description: "Create a new playlist for a user, optionally pre-filled with items. Changes server state.",
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

		return nil, createOut{ID: id, Name: in.Name}, nil
	})

	type addIn struct {
		Playlist string   `json:"playlist"       jsonschema:"playlist name or id"`
		ItemIDs  []string `json:"item_ids"       jsonschema:"library item ids to append"`
		User     string   `json:"user,omitempty" jsonschema:"user name or id acting on the playlist; defaults to the first administrator"`
	}
	type addOut struct {
		Added int    `json:"added"`
		To    string `json:"to"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "playlist_add",
		Description: "Append items to a playlist. Changes server state.",
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
		before, _, err := client.PlaylistItems(ctx, pl.ID, user.ID)
		if err != nil {
			return nil, addOut{}, err
		}
		if err := client.AddToPlaylist(ctx, pl.ID, in.ItemIDs, user.ID); err != nil {
			return nil, addOut{}, err
		}
		after, _, err := client.PlaylistItems(ctx, pl.ID, user.ID)
		if err != nil {
			return nil, addOut{}, err
		}

		return nil, addOut{Added: max(len(after)-len(before), 0), To: pl.Name}, nil
	})

	type removeIn struct {
		Playlist string   `json:"playlist"       jsonschema:"playlist name or id"`
		EntryIDs []string `json:"entry_ids"      jsonschema:"entry ids from playlist_get (not item ids)"`
		User     string   `json:"user,omitempty" jsonschema:"user name or id acting on the playlist; defaults to the first administrator"`
	}
	type removeOut struct {
		Removed int    `json:"removed" jsonschema:"entries that left the playlist"`
		From    string `json:"from"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "playlist_remove",
		Description: "Remove entries from a playlist (the items stay in the library). An entry id the playlist does not hold is an error: Emby renumbers the entries whenever it refreshes the playlist (a library scan does), so read them from playlist_get just before. On Jellyfin an item's entries share its id, so removing one entry of an item the playlist holds twice removes both. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in removeIn) (*mcp.CallToolResult, removeOut, error) {
		pl, err := resolveByType(ctx, client, "Playlist", in.Playlist)
		if err != nil {
			return nil, removeOut{}, err
		}
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, removeOut{}, err
		}

		removed, err := client.RemoveFromPlaylist(ctx, pl.ID, user.ID, in.EntryIDs)
		if err != nil {
			return nil, removeOut{}, err
		}

		return nil, removeOut{Removed: removed, From: pl.Name}, nil
	})

	type editIn struct {
		Playlist    string `json:"playlist"                jsonschema:"playlist name or id"`
		Name        string `json:"name,omitempty"          jsonschema:"rename the playlist"`
		MoveEntryID string `json:"move_entry_id,omitempty" jsonschema:"an entry id from playlist_get to move"`
		Position    int    `json:"position,omitempty"      jsonschema:"where to move it, 1 for the top"`
		User        string `json:"user,omitempty"          jsonschema:"user name or id acting on the playlist; defaults to the first administrator"`
	}
	type editOut struct {
		Name    string     `json:"name"`
		Changed []string   `json:"changed"`
		Entries []entryRow `json:"entries" jsonschema:"in playlist order, after the change"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "playlist_edit",
		Description: "Rename a playlist, or move one of its entries to another position (1 is the top). playlist_add and playlist_remove change what it holds. Emby renumbers the entries whenever it refreshes the playlist (a library scan does), so read entry ids from playlist_get just before and from this answer after; an entry id the playlist does not hold is an error. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		if in.Name == "" && in.MoveEntryID == "" {
			return nil, editOut{}, errors.New("nothing to change: pass name, or move_entry_id and position")
		}
		if in.MoveEntryID != "" && in.Position < 1 {
			return nil, editOut{}, errors.New("position is required with move_entry_id, 1 for the top")
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
			if err := client.MovePlaylistEntry(ctx, pl.ID, user.ID, in.MoveEntryID, in.Position-1); err != nil {
				return nil, editOut{}, err
			}
			out.Changed = append(out.Changed, fmt.Sprintf("moved %s to %d", in.MoveEntryID, in.Position))
		}
		if in.Name != "" && in.Name != pl.Name {
			if err := client.RenamePlaylist(ctx, pl.ID, user.ID, in.Name); err != nil {
				return nil, editOut{}, err
			}
			out.Changed = append(out.Changed, "renamed "+pl.Name+" to "+in.Name)
			out.Name = in.Name
		}

		entries, _, err := client.PlaylistItems(ctx, pl.ID, user.ID)
		if err != nil {
			return nil, editOut{}, err
		}
		out.Entries = make([]entryRow, 0, len(entries))
		for i := range entries {
			out.Entries = append(out.Entries, entryRow{itemSummary: summarise(&entries[i]), EntryID: entries[i].PlaylistItemID})
		}

		return nil, out, nil
	})

	type deleteIn struct {
		Playlist string `json:"playlist" jsonschema:"playlist name or id"`
	}
	type deleteOut struct {
		Deleted string `json:"deleted"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "playlist_delete",
		Description: "Delete a playlist. The items stay in the library; only the list goes. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		pl, err := resolveByType(ctx, client, "Playlist", in.Playlist)
		if err != nil {
			return nil, deleteOut{}, err
		}

		if err := client.DeleteItem(ctx, pl.ID); err != nil {
			return nil, deleteOut{}, err
		}

		return nil, deleteOut{Deleted: pl.Name}, nil
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
		if it, err := client.ItemByID(ctx, id); err == nil {
			name = it.Name + " (" + id + ")"
		}

		return fmt.Errorf("%s cannot see %s: it is in a library they have no access to, or rated above what they may watch, and a playlist of theirs takes only what they can see", user.Name, name)
	}

	return nil
}
