package tools

import (
	"context"
	"fmt"
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
		// an id no item has: Emby answers the image reads with a bare 500 and
		// Jellyfin with a 404, neither of which says the id is wrong. A
		// version's own id is an item's, whose images the server answers
		if _, err := itemOrVersion(ctx, client, in.ID); err != nil {
			return nil, artworkOut{}, err
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
		Set      string `json:"set"`
		Image    string `json:"image,omitempty"    jsonschema:"where the server keeps the new image, as it lists it now"`
		Removed  string `json:"removed,omitempty"  jsonschema:"the image file beside the media the server deleted in taking the new image: gone from disk"`
		Replaced string `json:"replaced,omitempty" jsonschema:"the image file beside the media the server wrote the new image over"`
		Written  string `json:"written,omitempty"  jsonschema:"a file beside the media the server wrote the new image to that was not there before"`
		Note     string `json:"note,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "item_artwork_set",
		Description: "Apply a remote image as the item's poster/backdrop/etc. The server downloads whatever URL it is given - a candidate from item_artwork or any other - and the image it replaces is gone. " +
			"When that image is a file beside the media (a film's poster.jpg in its folder), Emby DELETES it and keeps the new image in its own metadata folder; with the library's option to save artwork beside the media on, Emby writes the new image over the file instead, and Jellyfin deletes it, writes the new image as folder.jpg and writes the item's nfo beside the media again. Otherwise Jellyfin keeps the file. No tool puts a deleted or written-over file back. " +
			"The answer names the file removed, written over or written, and where the server keeps the new image.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setIn) (*mcp.CallToolResult, setOut, error) {
		imgType := in.Type
		if imgType == "" {
			imgType = "Primary"
		}

		// what is beside the media now, and the image of this type: what the
		// apply may delete, write over, or write beside it
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, setOut{}, err
		}
		dir := ""
		if onDisk(it.Path) {
			dir = parentDir(it.Path)
			if it.IsFolder {
				dir = trimSep(it.Path)
			}
		}
		before, err := client.Images(ctx, in.ID)
		if err != nil {
			return nil, setOut{}, fmt.Errorf("could not read %s's images, so nothing was changed: %w", it.Name, err)
		}
		old := imageOfType(before, imgType)
		var had []embyfin.FolderEntry
		if dir != "" {
			entries, found, lerr := client.ListFolder(ctx, dir)
			if lerr != nil {
				return nil, setOut{}, fmt.Errorf("could not read the folder beside %s's media, so nothing was changed: %w", it.Name, lerr)
			}
			if found {
				had = entries
			}
		}

		if err := client.DownloadRemoteImage(ctx, in.ID, imgType, in.URL); err != nil {
			return nil, setOut{}, err
		}
		out := setOut{Set: imgType}
		applied := func(what string, err error) error {
			return fmt.Errorf("the %s image was set, but %s failed, so what happened to the files beside the media is not known: %w", imgType, what, err)
		}
		after, err := client.Images(ctx, in.ID)
		if err != nil {
			return nil, setOut{}, applied("reading the images back", err)
		}
		if now := imageOfType(after, imgType); now != nil {
			out.Image = now.Path
		}
		if dir == "" {
			return nil, out, nil
		}
		entries, found, err := client.ListFolder(ctx, dir)
		if err != nil {
			return nil, setOut{}, applied("reading the folder back", err)
		}
		if !found {
			entries = nil
		}
		there := func(list []embyfin.FolderEntry, path string) bool {
			return slices.ContainsFunc(list, func(e embyfin.FolderEntry) bool { return trimSep(e.Path) == trimSep(path) })
		}
		var notes []string
		// the file it replaced, when that was one beside the media
		if old != nil && old.Path != "" && parentDir(old.Path) == dir {
			now := imageOfType(after, imgType)
			switch {
			case !there(entries, old.Path):
				out.Removed = old.Path
				notes = append(notes, "the server deleted "+old.Path+", the image this one replaced, from beside the media")
			case now != nil && trimSep(now.Path) == trimSep(old.Path):
				// the set was answered, and the image is the file the old
				// one was: Emby, saving artwork beside the media, wrote
				// over it (seen on 4.10)
				out.Replaced = old.Path
				notes = append(notes, "the server lists the new image as "+old.Path+", the file the image it replaced was: it wrote the new image over it")
			}
		}
		// a file the new image went to that was not there before
		if out.Image != "" && parentDir(out.Image) == dir && !there(had, out.Image) {
			out.Written = out.Image
			notes = append(notes, "the server wrote the new image to "+out.Image+", beside the media")
		}
		if out.Removed != "" && out.Image != "" && parentDir(out.Image) != dir {
			notes = append(notes, "it keeps the new image in its own metadata folder: the folder no longer holds that file")
		}
		out.Note = strings.Join(notes, "; ")

		return nil, out, nil
	})
}

// imageOfType is an item's first image of a type, nil when it has none.
func imageOfType(images []embyfin.ImageInfo, imgType string) *embyfin.ImageInfo {
	for i := range images {
		if strings.EqualFold(images[i].ImageType, imgType) {
			return &images[i]
		}
	}

	return nil
}
