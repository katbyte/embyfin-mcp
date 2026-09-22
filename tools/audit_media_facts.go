package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit_media_facts: the files the server holds no facts about, or facts
// from before the file was last written.
//
// Every quality question - resolution, codec, bitrate, runtime, audio - is
// answered from what the server read off the file when it probed it. A file
// it never probed (an import cut short, a scan that stopped) answers all of
// them with nothing, which a caller reads as "a 0x0 file", and a file
// written over in place keeps the facts of the file that was there before
// until a scan re-reads it, which a caller reads as the old copy. Neither
// server records when it last probed a file, so the second can only be
// suspected: Emby gives the file's own modified time, and a file modified
// after the item was made was replaced; whether the server has re-read it
// since is for the caller with the file in front of it to settle, which is
// why each row carries the size the server believes.
func registerMediaFactsAudit(r *registry) {
	client := r.client

	type factsIn struct {
		Library string `json:"library,omitempty" jsonschema:"one library by name or id; default every library"`
		Types   string `json:"types,omitempty"   jsonschema:"Movie, Episode or both; default both"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum rows in each list, default 100"`
	}
	type factsRow struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Path         string `json:"path,omitempty"`
		Size         int64  `json:"size,omitempty"          jsonschema:"the file size the server believes, in bytes; compare it with the file's to tell whether the server has re-read a replaced file"`
		DateCreated  string `json:"date_created,omitempty"  jsonschema:"when the server first saw the file"`
		FileModified string `json:"file_modified,omitempty" jsonschema:"when the file was last written, as the server read it (Emby only)"`
		Detail       string `json:"detail"`
	}
	type factsOut struct {
		Scanned       int        `json:"items_scanned"`
		TotalUnprobed int        `json:"total_unprobed"`
		TotalReplaced int        `json:"total_replaced"`
		Unprobed      []factsRow `json:"unprobed"       jsonschema:"files the server holds no media facts for: never probed, so every quality question about them is unanswered; capped at limit"`
		Replaced      []factsRow `json:"replaced"       jsonschema:"files written after the server first saw them (Emby says when a file was last written; Jellyfin does not): the facts may be the old file's until a scan re-reads it, which the size tells. Capped at limit"`
		Note          string     `json:"note,omitempty"`
	}

	add(r, readTool, &mcp.Tool{
		Name: "audit_media_facts",
		Description: "Find files the server holds no media facts for (never probed, so resolution, codec, bitrate and audio all read as nothing rather than as a measurement) and, on Emby, files written over after the server first saw them, whose facts may still be the old file's until a scan re-reads them. " +
			"Each row carries the size the server believes, so a caller with the file in front of it can tell a re-read from a stale one. A scan of the library re-probes both.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in factsIn) (*mcp.CallToolResult, factsOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 100
		}
		opts, err := sweepOptions(ctx, client, in.Library, in.Types, "Movie,Episode", "Path,MediaSources,DateCreated,DateModified")
		if err != nil {
			return nil, factsOut{}, err
		}

		out := factsOut{Unprobed: []factsRow{}, Replaced: []factsRow{}}
		var unprobed, replaced []factsRow
		if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
			for i := range items {
				it := &items[i]
				if !it.HasFile() {
					continue
				}
				out.Scanned++
				row := factsRow{ID: it.ID, Name: episodeOrItemName(it), Path: it.Path, DateCreated: it.DateCreated, FileModified: it.DateModified}
				if len(it.MediaSources) > 0 {
					row.Size = it.MediaSources[0].Size
				}
				switch {
				case !probed(it):
					row.Detail = "no media facts: the server has never probed the file, so nothing about its picture or sound is known"
					unprobed = append(unprobed, row)
				case it.DateModified != "" && it.DateCreated != "" && it.DateModified > it.DateCreated:
					row.Detail = fmt.Sprintf("the file was written on %s, after the server first saw it on %s: its facts are the earlier file's until a scan re-reads it", dateOf(it.DateModified), dateOf(it.DateCreated))
					replaced = append(replaced, row)
				}
			}

			return true
		}); err != nil {
			return nil, factsOut{}, err
		}

		for _, list := range []*[]factsRow{&unprobed, &replaced} {
			slices.SortFunc(*list, func(a, b factsRow) int { return strings.Compare(a.Path, b.Path) })
		}
		out.TotalUnprobed, out.TotalReplaced = len(unprobed), len(replaced)
		out.Unprobed = append(out.Unprobed, unprobed[:min(len(unprobed), limit)]...)
		out.Replaced = append(out.Replaced, replaced[:min(len(replaced), limit)]...)
		if client.Backend() == embyfin.Jellyfin {
			out.Note = "Jellyfin does not say when a file was last written, so replaced files cannot be told apart here; compare sizes against the files"
		}

		return nil, out, nil
	})
}

// probed says whether the server has read anything off the file: a media
// source with a size, or a stream. A record with neither is a file it has
// not looked at, however it got there.
func probed(it *embyfin.Item) bool {
	for i := range it.MediaSources {
		if it.MediaSources[i].Size > 0 || len(it.MediaSources[i].MediaStreams) > 0 {
			return true
		}
	}

	return false
}

// episodeOrItemName names an item the way a worklist wants it: an episode
// by its series and number, anything else by its name.
func episodeOrItemName(it *embyfin.Item) string {
	if it.Type == typeEpisode && it.SeriesName != "" {
		return fmt.Sprintf("%s S%02dE%02d %s", it.SeriesName, it.ParentIndexNumber, it.IndexNumber, it.Name)
	}

	return it.Name
}
