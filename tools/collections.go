package tools

import (
	"context"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerCollectionTools(server *mcp.Server, client *embyfin.Client) {
	type collectionRow struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type listOut struct {
		Collections []collectionRow `json:"collections"`
	}
	mcp.AddTool(server, &mcp.Tool{
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
	mcp.AddTool(server, &mcp.Tool{
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
	mcp.AddTool(server, &mcp.Tool{
		Name:        "collection_create",
		Description: "Create a new collection (boxset), optionally pre-filled with items. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, createOut, error) {
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
		Added int    `json:"added"`
		To    string `json:"to"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "collection_add",
		Description: "Add items to a collection. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, addOut, error) {
		col, err := resolveByType(ctx, client, "BoxSet", in.Collection)
		if err != nil {
			return nil, addOut{}, err
		}

		if err := client.AddToCollection(ctx, col.ID, in.ItemIDs); err != nil {
			return nil, addOut{}, err
		}

		return nil, addOut{Added: len(in.ItemIDs), To: col.Name}, nil
	})

	type removeIn struct {
		Collection string   `json:"collection" jsonschema:"collection name or id"`
		ItemIDs    []string `json:"item_ids"   jsonschema:"library item ids to remove (items stay in the library)"`
	}
	type removeOut struct {
		Removed int    `json:"removed"`
		From    string `json:"from"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "collection_remove",
		Description: "Remove items from a collection (the items stay in the library). Changes server state.",
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
}
