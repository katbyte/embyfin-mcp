package tools

import (
	"context"
	"fmt"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// subtitlePolls is how many times item_subtitle_download reads the item back
// for the subtitle it asked for, a settle interval apart: ten seconds. The
// server saves the file and then queues a refresh of the item that finds it,
// so the subtitle shows a moment after the call returns rather than with it.
const subtitlePolls = 40

// subtitleStreams counts the subtitle streams an item's files carry, embedded
// and beside the file alike.
func subtitleStreams(it *embyfin.Item) int {
	n := 0
	for _, src := range it.MediaSources {
		for _, s := range src.MediaStreams {
			if s.Type == "Subtitle" {
				n++
			}
		}
	}

	return n
}

// subtitleArrived reads an item back until it carries more subtitle streams
// than it did before a download, or subtitlePolls reads have found none.
func subtitleArrived(ctx context.Context, r *registry, itemID string, before int) (bool, error) {
	for range subtitlePolls {
		if err := r.pause(ctx); err != nil {
			return false, err
		}
		it, err := r.client.ItemByID(ctx, itemID)
		if err != nil {
			return false, err
		}
		if subtitleStreams(it) > before {
			return true, nil
		}
	}

	return false, nil
}

func registerSubtitleTools(r *registry) {
	client := r.client
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
	add(r, readTool, &mcp.Tool{
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
		Downloaded bool `json:"downloaded" jsonschema:"true once the new subtitle shows on the item"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_subtitle_download",
		Description: "Download a chosen remote subtitle for an item, and wait (up to ten seconds) for it to show on the item. Changes server state (writes a subtitle file).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in downloadIn) (*mcp.CallToolResult, downloadOut, error) {
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, downloadOut{}, err
		}
		before := subtitleStreams(it)

		if err := client.DownloadSubtitle(ctx, in.ID, in.SubtitleID); err != nil {
			return nil, downloadOut{}, err
		}

		// the server's answer is not the subtitle: Jellyfin answers an id
		// that names nothing with the same empty success as a real download,
		// so the item is read back for a subtitle it did not have before
		arrived, err := subtitleArrived(ctx, r, in.ID, before)
		if err != nil {
			return nil, downloadOut{}, err
		}
		if !arrived {
			return nil, downloadOut{}, fmt.Errorf("the server answered the download of %s, but no new subtitle reached %s within ten seconds: pass an id item_subtitle_search offered for this item (a download that replaced a subtitle file of the same language and format adds none)", in.SubtitleID, it.Name)
		}

		return nil, downloadOut{Downloaded: true}, nil
	})
}
