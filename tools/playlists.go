package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolveByType finds an item of the given type by name (case-insensitive) or
// id — used for playlists and collections.
func resolveByType(ctx context.Context, client *embyfin.Client, itemType, nameOrID string) (*embyfin.Item, error) {
	items, _, err := client.Search(ctx, embyfin.SearchOptions{IncludeItemTypes: itemType, Fields: embyfin.FieldsLean})
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(items))
	for i := range items {
		if strings.EqualFold(items[i].Name, nameOrID) || items[i].ID == nameOrID {
			return &items[i], nil
		}
		names = append(names, items[i].Name)
	}

	return nil, fmt.Errorf("no %s named %q (have: %s)", strings.ToLower(itemType), nameOrID, strings.Join(names, ", "))
}

func registerPlaylistTools(server *mcp.Server, client *embyfin.Client) {
	type playlistRow struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type listOut struct {
		Playlists []playlistRow `json:"playlists"`
	}
	addTool(server, &mcp.Tool{
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
		Playlist string `json:"playlist" jsonschema:"playlist name (case-insensitive) or id"`
	}
	type entryRow struct {
		itemSummary
		EntryID string `json:"entry_id" jsonschema:"pass to playlist_remove"`
	}
	type getOut struct {
		Name    string     `json:"name"`
		Entries []entryRow `json:"entries" jsonschema:"in playlist order"`
	}
	addTool(server, &mcp.Tool{
		Name:        "playlist_get",
		Description: "A playlist's entries in order.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		pl, err := resolveByType(ctx, client, "Playlist", in.Playlist)
		if err != nil {
			return nil, getOut{}, err
		}

		entries, _, err := client.PlaylistItems(ctx, pl.ID)
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
	}
	type createOut struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	addTool(server, &mcp.Tool{
		Name:        "playlist_create",
		Description: "Create a new playlist, optionally pre-filled with items. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, createOut, error) {
		id, err := client.CreatePlaylist(ctx, in.Name, in.ItemIDs, in.MediaType)
		if err != nil {
			return nil, createOut{}, err
		}

		return nil, createOut{ID: id, Name: in.Name}, nil
	})

	type addIn struct {
		Playlist string   `json:"playlist" jsonschema:"playlist name or id"`
		ItemIDs  []string `json:"item_ids" jsonschema:"library item ids to append"`
	}
	type addOut struct {
		Added int    `json:"added"`
		To    string `json:"to"`
	}
	addTool(server, &mcp.Tool{
		Name:        "playlist_add",
		Description: "Append items to a playlist. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, addOut, error) {
		pl, err := resolveByType(ctx, client, "Playlist", in.Playlist)
		if err != nil {
			return nil, addOut{}, err
		}

		if err := client.AddToPlaylist(ctx, pl.ID, in.ItemIDs); err != nil {
			return nil, addOut{}, err
		}

		return nil, addOut{Added: len(in.ItemIDs), To: pl.Name}, nil
	})

	type removeIn struct {
		Playlist string   `json:"playlist"  jsonschema:"playlist name or id"`
		EntryIDs []string `json:"entry_ids" jsonschema:"entry ids from playlist_get (not item ids)"`
	}
	type removeOut struct {
		Removed int    `json:"removed"`
		From    string `json:"from"`
	}
	addTool(server, &mcp.Tool{
		Name:        "playlist_remove",
		Description: "Remove entries from a playlist (the items stay in the library). Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in removeIn) (*mcp.CallToolResult, removeOut, error) {
		pl, err := resolveByType(ctx, client, "Playlist", in.Playlist)
		if err != nil {
			return nil, removeOut{}, err
		}

		if err := client.RemoveFromPlaylist(ctx, pl.ID, in.EntryIDs); err != nil {
			return nil, removeOut{}, err
		}

		return nil, removeOut{Removed: len(in.EntryIDs), From: pl.Name}, nil
	})
}
