package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerCollectionTools(r *registry) {
	client := r.client
	type collectionRow struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type listOut struct {
		Collections []collectionRow `json:"collections"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "collection_list",
		Description: "List all collections (boxsets).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, listOut, error) {
		items, _, err := client.Search(ctx, embyfin.SearchOptions{IncludeItemTypes: "BoxSet", Fields: embyfin.FieldsLean})
		if err != nil {
			return nil, listOut{}, err
		}

		out := listOut{}
		for _, it := range items {
			out.Collections = append(out.Collections, collectionRow{ID: it.ID, Name: it.Name})
		}

		return nil, out, nil
	})

	type getIn struct {
		Collection string `json:"collection" jsonschema:"collection name (case-insensitive) or id"`
	}
	type getOut struct {
		Name  string        `json:"name"`
		Items []itemSummary `json:"items"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "collection_get",
		Description: "A collection's contents.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		col, err := resolveByType(ctx, client, "BoxSet", in.Collection)
		if err != nil {
			return nil, getOut{}, err
		}

		items, _, err := client.Search(ctx, embyfin.SearchOptions{ParentID: col.ID})
		if err != nil {
			return nil, getOut{}, err
		}

		return nil, getOut{Name: col.Name, Items: summariseAll(items)}, nil
	})

	type createIn struct {
		Name    string   `json:"name"               jsonschema:"name for the new collection"`
		ItemIDs []string `json:"item_ids,omitempty" jsonschema:"initial items"`
	}
	type createOut struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "collection_create",
		Description: "Create a new collection (boxset) holding the given items (Emby needs at least one). A name another collection has is refused: collection_add adds to that one. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, createOut, error) {
		// a collection is a folder named after it on both servers, so a
		// second one under a name answers with the first, and Jellyfin
		// replaces what it holds with the new items
		// the name is taken by one collection, or by several already, which
		// resolveByType reports as an ambiguity rather than a hit
		existing, err := resolveByType(ctx, client, "BoxSet", in.Name)
		switch {
		case err == nil:
			return nil, createOut{}, fmt.Errorf("a collection named %q exists (id %s): add to it with collection_add", existing.Name, existing.ID)
		case strings.Contains(err.Error(), "are named"):
			return nil, createOut{}, fmt.Errorf("the name is taken: %w", err)
		}
		id, err := client.CreateCollection(ctx, in.Name, in.ItemIDs)
		if err != nil {
			return nil, createOut{}, err
		}

		return nil, createOut{ID: id, Name: in.Name}, nil
	})

	type addIn struct {
		Collection string   `json:"collection" jsonschema:"collection name or id"`
		ItemIDs    []string `json:"item_ids"   jsonschema:"library item ids to add"`
	}
	type addOut struct {
		Added       int    `json:"added"`
		AlreadyHeld int    `json:"already_held,omitempty" jsonschema:"items asked for that the collection already held, which a collection cannot hold twice"`
		To          string `json:"to"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "collection_add",
		Description: "Add items to a collection. An item it already holds is left as it is and counted as already_held. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, addOut, error) {
		col, err := resolveByType(ctx, client, "BoxSet", in.Collection)
		if err != nil {
			return nil, addOut{}, err
		}

		// both servers answer an add of a member with a success and change
		// nothing, so count what is new rather than what was asked for
		held, err := client.CollectionMembers(ctx, col.ID)
		if err != nil {
			return nil, addOut{}, err
		}
		var fresh []string
		for _, id := range in.ItemIDs {
			if !slices.Contains(held, id) && !slices.Contains(fresh, id) {
				fresh = append(fresh, id)
			}
		}
		out := addOut{Added: len(fresh), AlreadyHeld: len(in.ItemIDs) - len(fresh), To: col.Name}
		if len(fresh) == 0 {
			return nil, out, nil
		}
		if err := client.AddToCollection(ctx, col.ID, fresh); err != nil {
			return nil, addOut{}, err
		}

		return nil, out, nil
	})

	type removeIn struct {
		Collection string   `json:"collection" jsonschema:"collection name or id"`
		ItemIDs    []string `json:"item_ids"   jsonschema:"library item ids to remove (items stay in the library)"`
	}
	type removeOut struct {
		Removed int    `json:"removed"`
		From    string `json:"from"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "collection_remove",
		Description: "Remove items from a collection (the items stay in the library). It answers once they have left, and an item the collection does not hold is an error. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in removeIn) (*mcp.CallToolResult, removeOut, error) {
		col, err := resolveByType(ctx, client, "BoxSet", in.Collection)
		if err != nil {
			return nil, removeOut{}, err
		}

		if err := client.RemoveFromCollection(ctx, col.ID, in.ItemIDs); err != nil {
			return nil, removeOut{}, err
		}

		return nil, removeOut{Removed: len(in.ItemIDs), From: col.Name}, nil
	})

	type editIn struct {
		Collection string `json:"collection"          jsonschema:"collection name or id"`
		Name       string `json:"name,omitempty"      jsonschema:"rename the collection"`
		SortName   string `json:"sort_name,omitempty" jsonschema:"the name it sorts by, e.g. Alien 1 to keep a saga together"`
		Overview   string `json:"overview,omitempty"  jsonschema:"the collection's description"`
	}
	type editOut struct {
		Name    string   `json:"name"`
		Updated []string `json:"updated_fields"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "collection_edit",
		Description: "Rename a collection, or set the name it sorts by or its description. Only the fields given change; collection_add and collection_remove change what it holds. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		if in.Name == "" && in.SortName == "" && in.Overview == "" {
			return nil, editOut{}, errors.New("nothing to change: pass name, sort_name or overview")
		}
		col, err := resolveByType(ctx, client, "BoxSet", in.Collection)
		if err != nil {
			return nil, editOut{}, err
		}
		admin, err := client.ResolveUser(ctx, "")
		if err != nil {
			return nil, editOut{}, err
		}
		out := editOut{Name: col.Name, Updated: []string{}}
		if _, err := client.EditItem(ctx, admin.ID, col.ID, func(full map[string]any) (bool, error) {
			if in.Name != "" {
				full["Name"], out.Name = in.Name, in.Name
				out.Updated = append(out.Updated, "Name")
			}
			if in.SortName != "" {
				full["SortName"], full["ForcedSortName"] = in.SortName, in.SortName
				out.Updated = append(out.Updated, "SortName")
			}
			if in.Overview != "" {
				full["Overview"] = in.Overview
				out.Updated = append(out.Updated, "Overview")
			}
			return true, nil
		}); err != nil {
			return nil, editOut{}, err
		}

		return nil, out, nil
	})

	type deleteIn struct {
		Collection string `json:"collection" jsonschema:"collection name or id"`
	}
	type deleteOut struct {
		Deleted string `json:"deleted"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "collection_delete",
		Description: "Delete a collection. The items stay in the library; only the grouping goes. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		col, err := resolveByType(ctx, client, "BoxSet", in.Collection)
		if err != nil {
			return nil, deleteOut{}, err
		}

		if err := client.DeleteItem(ctx, col.ID); err != nil {
			return nil, deleteOut{}, err
		}

		return nil, deleteOut{Deleted: col.Name}, nil
	})
}
