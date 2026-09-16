package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerIdentifyTools(r *registry) {
	client := r.client
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
		MetadataProviderIDs map[string]string `json:"metadata_provider_ids,omitempty" jsonschema:"keyed tmdb, imdb, tvdb"`
		Provider            string            `json:"search_provider,omitempty"`
		Overview            string            `json:"overview,omitempty"`
	}
	type identifyOut struct {
		Item       string      `json:"item"`
		Candidates []candidate `json:"candidates" jsonschema:"suggestions only; nothing is changed until item_identify_apply"`
	}
	add(r, readTool, &mcp.Tool{
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
				MetadataProviderIDs: providerKeys(r.ProviderIDs),
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
		Applied             string            `json:"applied"                         jsonschema:"the candidate applied"`
		Name                string            `json:"name"                            jsonschema:"the item as it is now"`
		Year                int               `json:"year,omitempty"`
		Overview            string            `json:"overview,omitempty"`
		MetadataProviderIDs map[string]string `json:"metadata_provider_ids,omitempty" jsonschema:"the item's ids now, keyed tmdb, imdb, tvdb"`
		Note                string            `json:"note,omitempty"                  jsonschema:"what the change could not do"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_identify_apply",
		Description: "Apply a candidate from item_identify: rewrites the item's identity and re-fetches its metadata and images, then reads the item back and reports it as it now is. An identity the server did not take is an error (Emby keeps the one an nfo beside the file names). In a library with its metadata fetchers off only the ids change and nothing is fetched: check the year and overview reported, and set them with item_edit. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in applyIn) (*mcp.CallToolResult, applyOut, error) {
		results, err := client.RemoteSearch(ctx, in.Kind, in.ID, in.Name, in.Year)
		if err != nil {
			return nil, applyOut{}, err
		}

		if in.Candidate < 0 || in.Candidate >= len(results) {
			return nil, applyOut{}, fmt.Errorf("candidate %d out of range (search returned %d results)", in.Candidate, len(results))
		}
		chosen := results[in.Candidate]

		it, err := client.ApplyRemoteSearchResult(ctx, in.ID, chosen, in.ReplaceAllImages)
		if err != nil {
			return nil, applyOut{}, err
		}

		overview := it.Overview
		if len(overview) > 200 {
			overview = overview[:200] + "..."
		}
		out := applyOut{
			Applied: fmt.Sprintf("%s (%d)", chosen.Name, chosen.ProductionYear),
			Name:    it.Name, Year: it.ProductionYear, Overview: overview,
			MetadataProviderIDs: providerKeys(it.ProviderIDs),
		}
		folder, err := libraryOf(ctx, client, it)
		if err != nil {
			return nil, applyOut{}, err
		}
		if folder != nil && folder.FetchersOff(it.Type) {
			out.Note = fmt.Sprintf("the %s library has its metadata fetchers off, so only the ids changed and nothing was fetched: set what is missing or wrong with item_edit", folder.Name)
		}

		return nil, out, nil
	})
}
