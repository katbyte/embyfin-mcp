package tools

import (
	"context"
	"fmt"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerIdentifyTools(server *mcp.Server, client *embyfin.Client) {
	type identifyIn struct {
		ID   string `json:"id"             jsonschema:"the library item id to identify"`
		Kind string `json:"kind"           jsonschema:"movie or series"`
		Name string `json:"name,omitempty" jsonschema:"override the search title; defaults to the item's current name"`
		Year int    `json:"year,omitempty" jsonschema:"override the search year"`
	}
	type candidate struct {
		Index               int               `json:"index"                           jsonschema:"pass to item_identify_apply as candidate"`
		Name                string            `json:"name"`
		Year                int               `json:"year,omitempty"`
		MetadataProviderIDs map[string]string `json:"metadata_provider_ids,omitempty"`
		Provider            string            `json:"search_provider,omitempty"`
		Overview            string            `json:"overview,omitempty"`
	}
	type identifyOut struct {
		Item       string      `json:"item"`
		Candidates []candidate `json:"candidates" jsonschema:"suggestions only; nothing is changed until item_identify_apply"`
	}
	addTool(server, &mcp.Tool{
		Name:        "item_identify",
		Description: "Ask the metadata providers for candidate matches for an item (suggestions only — verify year and runtime before applying with item_identify_apply).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in identifyIn) (*mcp.CallToolResult, identifyOut, error) {
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, identifyOut{}, err
		}

		results, err := client.RemoteSearch(ctx, in.Kind, in.ID, in.Name, in.Year)
		if err != nil {
			return nil, identifyOut{}, err
		}

		out := identifyOut{Item: it.Name, Candidates: []candidate{}}
		for i, r := range results {
			overview := r.Overview
			if len(overview) > 200 {
				overview = overview[:200] + "..."
			}
			out.Candidates = append(out.Candidates, candidate{
				Index:               i,
				Name:                r.Name,
				Year:                r.ProductionYear,
				MetadataProviderIDs: r.ProviderIDs,
				Provider:            r.SearchProviderName,
				Overview:            overview,
			})
		}

		return nil, out, nil
	})

	type applyIn struct {
		ID               string `json:"id"                           jsonschema:"the library item id"`
		Kind             string `json:"kind"                         jsonschema:"movie or series"`
		Candidate        int    `json:"candidate"                    jsonschema:"index from item_identify's candidates"`
		Name             string `json:"name,omitempty"               jsonschema:"must match the name/year overrides used in item_identify so indexes line up"`
		Year             int    `json:"year,omitempty"`
		ReplaceAllImages bool   `json:"replace_all_images,omitempty"`
	}
	type applyOut struct {
		Applied             string            `json:"applied"`
		MetadataProviderIDs map[string]string `json:"metadata_provider_ids,omitempty"`
	}
	addTool(server, &mcp.Tool{
		Name:        "item_identify_apply",
		Description: "Apply a candidate from item_identify: rewrites the item's identity and re-fetches its metadata and images. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in applyIn) (*mcp.CallToolResult, applyOut, error) {
		results, err := client.RemoteSearch(ctx, in.Kind, in.ID, in.Name, in.Year)
		if err != nil {
			return nil, applyOut{}, err
		}

		if in.Candidate < 0 || in.Candidate >= len(results) {
			return nil, applyOut{}, fmt.Errorf("candidate %d out of range (search returned %d results)", in.Candidate, len(results))
		}
		chosen := results[in.Candidate]

		if err := client.ApplyRemoteSearchResult(ctx, in.ID, chosen, in.ReplaceAllImages); err != nil {
			return nil, applyOut{}, err
		}

		return nil, applyOut{
			Applied:             fmt.Sprintf("%s (%d)", chosen.Name, chosen.ProductionYear),
			MetadataProviderIDs: chosen.ProviderIDs,
		}, nil
	})
}
