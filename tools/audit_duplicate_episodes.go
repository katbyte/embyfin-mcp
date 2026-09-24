package tools

import (
	"context"
	"math"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// One episode's content filed under two episode numbers.
//
// Neither existing audit sees this. audit_duplicates matches provider ids, and
// the two entries carry different ones because the server thinks they are
// different episodes. audit_multiple_versions finds several files merged under
// ONE item, which is the opposite arrangement. What is left is the plainest
// evidence there is: the same season holding the same episode title twice.
//
// A library can carry such a pair for years, the two files running within
// seconds of each other, with nothing reporting it.

// titleRow is one member of a repeated-title group.
type titleRow struct {
	ID       string       `json:"id"`
	Episode  int          `json:"episode"`
	Title    string       `json:"title"`
	Path     string       `json:"path,omitempty"`
	RuntimeS int          `json:"runtime_s,omitempty"`
	Size     int64        `json:"size,omitempty"      jsonschema:"file size in bytes"`
	Bitrate  int64        `json:"bitrate,omitempty"`
	Height   int          `json:"height,omitempty"`
	Audio    []audioTrack `json:"audio,omitempty"`
}

// titleGroup is one season's repeated title.
type titleGroup struct {
	Series     string     `json:"series"`
	SeriesID   string     `json:"series_id"`
	Season     int        `json:"season"`
	Title      string     `json:"title"`
	Episodes   []titleRow `json:"episodes"    jsonschema:"the entries carrying that title, by episode number"`
	RuntimeGap float64    `json:"runtime_gap" jsonschema:"how far apart the runtimes are, as a fraction of the longest: 0.01 is the same content twice, 0.5 is two different episodes that happen to share a title"`
	Confidence string     `json:"confidence"  jsonschema:"near_certain when the runtimes agree within 5%, lead when they do not: a season can legitimately reuse a title, and generic titles like 'Episode 3' repeat by nature"`
}

type dupTitlesIn struct {
	Library string `json:"library,omitempty" jsonschema:"one library by name or id"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum groups, default 50"`
}

type dupTitlesOut struct {
	Scanned int          `json:"items_scanned"`
	Found   int          `json:"total_findings"`
	Groups  []titleGroup `json:"groups"         jsonschema:"near-certain groups first, then leads; capped at limit"`
}

func registerDuplicateEpisodesAudit(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_duplicate_episodes",
		Description: "Find one episode's content filed under two episode numbers: a season holding the same episode title twice. " +
			"Neither other duplicate audit sees this - audit_duplicates matches provider ids, which differ because the server believes they are different episodes, and audit_multiple_versions finds several files under one item. " +
			"Runtimes within 5% make it near certain; matching titles alone are a lead, because a season can reuse a title and generic ones repeat by nature. It does not pick a winner: the larger file can be the worse copy.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dupTitlesIn) (*mcp.CallToolResult, dupTitlesOut, error) {
		out, err := auditDuplicateEpisodes(ctx, client, in)

		return nil, out, err
	})
}

func auditDuplicateEpisodes(ctx context.Context, client *embyfin.Client, in dupTitlesIn) (dupTitlesOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	opts, err := sweepOptions(ctx, client, in.Library, typeEpisode, typeEpisode, "Path,MediaSources")
	if err != nil {
		return dupTitlesOut{}, err
	}

	type key struct {
		series, title string
		season        int
	}
	seasons := map[key][]embyfin.Item{}
	out := dupTitlesOut{Groups: []titleGroup{}}
	if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			out.Scanned++
			title := strings.TrimSpace(strings.ToLower(it.Name))
			if it.SeriesID == "" || title == "" || !it.HasFile() {
				continue
			}
			k := key{series: it.SeriesID, season: it.ParentIndexNumber, title: title}
			seasons[k] = append(seasons[k], *it)
		}

		return true
	}); err != nil {
		return dupTitlesOut{}, err
	}

	var groups []titleGroup
	for k, items := range seasons {
		if len(items) < 2 {
			continue
		}
		slices.SortFunc(items, func(a, b embyfin.Item) int { return a.IndexNumber - b.IndexNumber })
		group := titleGroup{
			Series: items[0].SeriesName, SeriesID: k.series, Season: k.season, Title: items[0].Name,
		}
		shortest, longest := 0, 0
		for i := range items {
			q := qualityOf(&items[i])
			runtime := int(items[i].RunTimeTicks / ticksPerSecond)
			group.Episodes = append(group.Episodes, titleRow{
				ID: items[i].ID, Episode: items[i].IndexNumber, Title: items[i].Name, Path: items[i].Path,
				RuntimeS: runtime, Size: q.Size, Bitrate: q.Bitrate, Height: q.Height, Audio: q.Audio,
			})
			if runtime > 0 && (shortest == 0 || runtime < shortest) {
				shortest = runtime
			}
			longest = max(longest, runtime)
		}
		// the runtimes are what carry the confidence: the same content
		// twice runs the same length, two episodes sharing a title do not
		group.Confidence = "lead"
		if longest > 0 && shortest > 0 {
			group.RuntimeGap = math.Round(float64(longest-shortest)/float64(longest)*100) / 100
			if group.RuntimeGap <= 0.05 {
				group.Confidence = "near_certain"
			}
		}
		groups = append(groups, group)
	}

	slices.SortFunc(groups, func(a, b titleGroup) int {
		if (a.Confidence == "near_certain") != (b.Confidence == "near_certain") {
			if a.Confidence == "near_certain" {
				return -1
			}

			return 1
		}
		if c := strings.Compare(a.Series, b.Series); c != 0 {
			return c
		}

		return strings.Compare(a.Title, b.Title)
	})
	out.Found = len(groups)
	out.Groups = append(out.Groups, groups[:min(len(groups), limit)]...)

	return out, nil
}
