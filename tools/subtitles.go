package tools

import (
	"context"
	"fmt"
	"regexp"
	"slices"

	"github.com/katbyte/embyfin-mcp/lib/mediapath"
	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
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

// subtitleFile is a subtitle's file name, as a server writes one beside the
// media it is for.
var subtitleFile = regexp.MustCompile(`(?i)\.(srt|ass|ssa|vtt|sub|sup|smi|idx|ttml)$`)

// subtitlesBeside lists the subtitle files in a folder, by path.
func subtitlesBeside(ctx context.Context, client *embyfin.Client, dir string) ([]string, error) {
	entries, found, err := client.ListFolder(ctx, dir)
	if err != nil || !found {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir && subtitleFile.MatchString(e.Name) {
			out = append(out, e.Path)
		}
	}
	slices.Sort(out)

	return out, nil
}

// subtitleArrived reads an item and its folder back until the item carries
// more subtitle streams than it did before a download, or a subtitle file
// the folder did not hold is there, or subtitlePolls reads have found
// neither. It answers the files new beside the media, and whether a new
// stream was seen.
func subtitleArrived(ctx context.Context, r *registry, it *embyfin.Item, dir string, streams int, files []string) (written []string, arrived bool, err error) {
	for range subtitlePolls {
		if err := r.pause(ctx); err != nil {
			return nil, false, err
		}
		now, err := r.client.ItemByID(ctx, it.ID)
		if err != nil {
			return nil, false, err
		}
		if dir != "" {
			after, err := subtitlesBeside(ctx, r.client, dir)
			if err != nil {
				return nil, false, err
			}
			written = slices.DeleteFunc(after, func(p string) bool { return slices.Contains(files, p) })
		}
		if arrived = subtitleStreams(now) > streams; arrived || len(written) > 0 {
			return written, arrived, nil
		}
	}

	return written, false, nil
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
		// an id no item has: Emby answers the search with a bare 500 and
		// Jellyfin with a 404, neither of which says the id is wrong. A
		// version's own id is an item's, which the search answers for
		if _, _, err := client.ItemByIDOrVersion(ctx, in.ID); err != nil {
			return nil, searchOut{}, err
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
		Downloaded bool     `json:"downloaded"        jsonschema:"true once the new subtitle shows on the item or beside it"`
		Written    []string `json:"written,omitempty" jsonschema:"subtitle files beside the media that were not there before the download"`
		Note       string   `json:"note,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "item_subtitle_download",
		Description: "Download a chosen remote subtitle for an item: the server writes it as a file beside the media and then refreshes the item to find it - a refresh that reads the nfo beside the file again, which on Emby puts back the ids it names over a match made since. " +
			"A subtitle file of that language and format already there is written over without a word, and no tool puts it back. The call waits up to ten seconds for a new subtitle file beside the media or a new subtitle on the item, and answers the files written; when neither comes, the error lists the subtitle files beside the media, one of which may have been written over.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in downloadIn) (*mcp.CallToolResult, downloadOut, error) {
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, downloadOut{}, err
		}
		streams := subtitleStreams(it)
		dir := ""
		if mediapath.OnDisk(it.Path) && !it.IsFolder {
			dir = mediapath.Dir(it.Path)
		}
		var files []string
		if dir != "" {
			if files, err = subtitlesBeside(ctx, client, dir); err != nil {
				return nil, downloadOut{}, fmt.Errorf("could not read the subtitle files beside %s, so nothing was downloaded: %w", it.Name, err)
			}
		}

		if err := client.DownloadSubtitle(ctx, in.ID, in.SubtitleID); err != nil {
			return nil, downloadOut{}, err
		}

		// the server's answer is not the subtitle: Jellyfin answers an id
		// that names nothing with the same empty success as a real download,
		// so the item and its folder are read back for a subtitle they did
		// not have before
		written, arrived, err := subtitleArrived(ctx, r, it, dir, streams, files)
		if err != nil {
			return nil, downloadOut{}, fmt.Errorf("the download of %s was sent, and may have written a subtitle file beside %s, but reading it back failed: %w", in.SubtitleID, it.Name, err)
		}
		if !arrived && len(written) == 0 {
			there := "none"
			if len(files) > 0 {
				there = listed(files, 20)
			}
			return nil, downloadOut{}, fmt.Errorf("the server answered the download of %s, but no new subtitle file appeared beside %s and no new subtitle reached it within ten seconds. Either the id names nothing the server could download (Jellyfin answers one with the same success as a real download: pass an id item_subtitle_search offered for this item), or the download was written over a subtitle file of the same language and format already there - the subtitle files beside the media are: %s", in.SubtitleID, it.Name, there)
		}
		out := downloadOut{Downloaded: true, Written: written}
		if len(written) == 0 {
			out.Note = "a new subtitle shows on the item, and no new file appeared beside the media: the server may have written it over a subtitle file already there, of those: " + listed(files, 20)
		}

		return nil, out, nil
	})
}
