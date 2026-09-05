package tools

import (
	"context"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerShowTools(server *mcp.Server, client *embyfin.Client) {
	type seasonsIn struct {
		SeriesID string `json:"series_id" jsonschema:"the series item id (find it with library_search types=Series)"`
	}
	type seasonRow struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Season int    `json:"season,omitempty"`
	}
	type seasonsOut struct {
		Series  string      `json:"series"`
		Seasons []seasonRow `json:"seasons"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "show_seasons",
		Description: "List a series' seasons.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in seasonsIn) (*mcp.CallToolResult, seasonsOut, error) {
		series, err := client.ItemByID(ctx, in.SeriesID)
		if err != nil {
			return nil, seasonsOut{}, err
		}

		seasons, err := client.Seasons(ctx, in.SeriesID, "")
		if err != nil {
			return nil, seasonsOut{}, err
		}

		out := seasonsOut{Series: series.Name}
		for _, s := range seasons {
			out.Seasons = append(out.Seasons, seasonRow{ID: s.ID, Name: s.Name, Season: s.IndexNumber})
		}

		return nil, out, nil
	})

	type episodesIn struct {
		SeriesID string `json:"series_id"           jsonschema:"the series item id"`
		SeasonID string `json:"season_id,omitempty" jsonschema:"restrict to one season (id from show_seasons)"`
	}
	type episodesOut struct {
		Series   string        `json:"series"`
		Episodes []itemSummary `json:"episodes"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "show_episodes",
		Description: "List a series' episodes with quality facts, optionally scoped to one season.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in episodesIn) (*mcp.CallToolResult, episodesOut, error) {
		series, err := client.ItemByID(ctx, in.SeriesID)
		if err != nil {
			return nil, episodesOut{}, err
		}

		episodes, err := client.Episodes(ctx, in.SeriesID, in.SeasonID, "", false)
		if err != nil {
			return nil, episodesOut{}, err
		}

		return nil, episodesOut{Series: series.Name, Episodes: summariseAll(episodes)}, nil
	})

	type missingIn struct {
		SeriesID string `json:"series_id" jsonschema:"the series item id"`
	}
	type missingRow struct {
		Season  int    `json:"season"`
		Episode int    `json:"episode"`
		Name    string `json:"name"`
		AirDate string `json:"air_date,omitempty"`
	}
	type missingOut struct {
		Series  string       `json:"series"`
		Missing []missingRow `json:"missing" jsonschema:"episodes the metadata provider lists that the library lacks"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "show_missing",
		Description: "Episodes the metadata provider knows about that the library does not have. Requires the server's missing-episode display to be enabled for the library.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in missingIn) (*mcp.CallToolResult, missingOut, error) {
		series, err := client.ItemByID(ctx, in.SeriesID)
		if err != nil {
			return nil, missingOut{}, err
		}

		episodes, err := client.Episodes(ctx, in.SeriesID, "", "", true)
		if err != nil {
			return nil, missingOut{}, err
		}

		out := missingOut{Series: series.Name}
		for _, e := range episodes {
			out.Missing = append(out.Missing, missingRow{
				Season:  e.ParentIndexNumber,
				Episode: e.IndexNumber,
				Name:    e.Name,
				AirDate: e.PremiereDate,
			})
		}

		return nil, out, nil
	})
}
