package tools

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerArtworkTools(r *registry) {
	client := r.client
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
	add(r, readTool, &mcp.Tool{
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
		Set     string `json:"set"`
		Removed string `json:"removed,omitempty" jsonschema:"the image file beside the media the server deleted in taking the new image (Emby does): gone from disk"`
		Note    string `json:"note,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "item_artwork_set",
		Description: "Apply a remote provider image (from item_artwork) as the item's poster/backdrop/etc. " +
			"Emby DELETES the image file it replaces when that file sits beside the media - a film's poster.jpg in its folder - and keeps the new image in its own metadata folder instead, so the folder loses its poster file; the answer names the file removed. Jellyfin keeps the file. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setIn) (*mcp.CallToolResult, setOut, error) {
		imgType := in.Type
		if imgType == "" {
			imgType = "Primary"
		}

		// the file the image is now, when it is one beside the media: what
		// the apply may take off the disk
		local := ""
		if it, err := client.ItemByID(ctx, in.ID); err == nil && onDisk(it.Path) {
			if current, ierr := client.Images(ctx, in.ID); ierr == nil {
				dir := parentDir(it.Path)
				if it.IsFolder {
					dir = it.Path
				}
				for _, img := range current {
					if strings.EqualFold(img.ImageType, imgType) && img.Path != "" && parentDir(img.Path) == trimSep(dir) {
						local = img.Path

						break
					}
				}
			}
		}

		if err := client.DownloadRemoteImage(ctx, in.ID, imgType, in.URL); err != nil {
			return nil, setOut{}, err
		}
		out := setOut{Set: imgType}
		if local != "" {
			// read back: Emby deletes it (seen on 4.10), Jellyfin keeps it
			entries, found, err := client.ListFolder(ctx, parentDir(local))
			if err == nil && (!found || !slices.ContainsFunc(entries, func(e embyfin.FolderEntry) bool { return trimSep(e.Path) == trimSep(local) })) {
				out.Removed = local
				out.Note = "the server deleted " + local + ", the image this one replaced, from beside the media, and keeps the new image in its own metadata folder: the folder no longer holds that file"
			}
		}

		return nil, out, nil
	})
}
