package tools

import (
	"context"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerSubtitleTools(server *mcp.Server, client *embyfin.Client) {
	type searchIn struct {
		ID       string `json:"id"                 jsonschema:"the library item id"`
		Language string `json:"language,omitempty" jsonschema:"three-letter language code, default eng"`
	}
	type subOut struct {
		ID        string  `json:"id"                  jsonschema:"pass to item_subtitle_download"`
		Name      string  `json:"name,omitempty"`
		Provider  string  `json:"provider,omitempty"`
		Format    string  `json:"format,omitempty"`
		Downloads int     `json:"downloads,omitempty"`
		Rating    float64 `json:"rating,omitempty"`
	}
	type searchOut struct {
		Candidates []subOut `json:"candidates"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "item_subtitle_search",
		Description: "Search remote subtitle providers for an item in a given language.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
		lang := in.Language
		if lang == "" {
			lang = "eng"
		}

		subs, err := client.SearchSubtitles(ctx, in.ID, lang)
		if err != nil {
			return nil, searchOut{}, err
		}

		out := searchOut{}
		for _, s := range subs {
			out.Candidates = append(out.Candidates, subOut{
				ID:        s.ID,
				Name:      s.Name,
				Provider:  s.ProviderName,
				Format:    s.Format,
				Downloads: s.DownloadCount,
				Rating:    s.CommunityRating,
			})
		}

		return nil, out, nil
	})

	type downloadIn struct {
		ID         string `json:"id"          jsonschema:"the library item id"`
		SubtitleID string `json:"subtitle_id" jsonschema:"a candidate id from item_subtitle_search"`
	}
	type downloadOut struct {
		Downloaded bool `json:"downloaded"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "item_subtitle_download",
		Description: "Download a chosen remote subtitle for an item. Changes server state (writes a subtitle file).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in downloadIn) (*mcp.CallToolResult, downloadOut, error) {
		if err := client.DownloadSubtitle(ctx, in.ID, in.SubtitleID); err != nil {
			return nil, downloadOut{}, err
		}

		return nil, downloadOut{Downloaded: true}, nil
	})
}
