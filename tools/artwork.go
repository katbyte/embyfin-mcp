package tools

import (
	"context"
	"strconv"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerArtworkTools(server *mcp.Server, client *embyfin.Client) {
	type artworkIn struct {
		ID    string `json:"id"              jsonschema:"the library item id"`
		Type  string `json:"type,omitempty"  jsonschema:"image type: Primary (poster), Backdrop, Logo, Thumb; default Primary"`
		Limit int    `json:"limit,omitempty" jsonschema:"maximum remote candidates, default 10"`
	}
	type remoteImageOut struct {
		URL      string  `json:"url"                jsonschema:"pass to item_artwork_set"`
		Provider string  `json:"provider,omitempty"`
		Size     string  `json:"size,omitempty"`
		Language string  `json:"language,omitempty"`
		Rating   float64 `json:"rating,omitempty"`
		Votes    int     `json:"votes,omitempty"`
	}
	type artworkOut struct {
		Current    []embyfin.ImageInfo `json:"current"    jsonschema:"images the item has now"`
		Candidates []remoteImageOut    `json:"candidates" jsonschema:"remote provider images that could replace them"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "item_artwork",
		Description: "An item's current images plus remote provider candidates (posters, backdrops) that could replace them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in artworkIn) (*mcp.CallToolResult, artworkOut, error) {
		imgType := in.Type
		if imgType == "" {
			imgType = "Primary"
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 10
		}

		current, err := client.Images(ctx, in.ID)
		if err != nil {
			return nil, artworkOut{}, err
		}

		remote, _, err := client.RemoteImages(ctx, in.ID, imgType, limit)
		if err != nil {
			return nil, artworkOut{}, err
		}

		out := artworkOut{Current: current}
		for _, r := range remote {
			size := ""
			if r.Width > 0 {
				size = strconv.Itoa(r.Width) + "x" + strconv.Itoa(r.Height)
			}
			out.Candidates = append(out.Candidates, remoteImageOut{
				URL:      r.URL,
				Provider: r.ProviderName,
				Size:     size,
				Language: r.Language,
				Rating:   r.CommunityRating,
				Votes:    r.VoteCount,
			})
		}

		return nil, out, nil
	})

	type setIn struct {
		ID   string `json:"id"             jsonschema:"the library item id"`
		URL  string `json:"url"            jsonschema:"a candidate url from item_artwork"`
		Type string `json:"type,omitempty" jsonschema:"image type to set; default Primary"`
	}
	type setOut struct {
		Set string `json:"set"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "item_artwork_set",
		Description: "Apply a remote provider image (from item_artwork) as the item's poster/backdrop/etc. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setIn) (*mcp.CallToolResult, setOut, error) {
		imgType := in.Type
		if imgType == "" {
			imgType = "Primary"
		}

		if err := client.DownloadRemoteImage(ctx, in.ID, imgType, in.URL); err != nil {
			return nil, setOut{}, err
		}

		return nil, setOut{Set: imgType}, nil
	})
}
