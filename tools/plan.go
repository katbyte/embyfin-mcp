package tools

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Reading the destinations before anything is written to them.
//
// A bulk import that does not ask what is already at its paths can overwrite
// one series' episodes in place with files of another series of the same
// name, and a rename pass can write one episode over another. None of it is
// visible afterwards: an overwritten path keeps the item's id and its
// date_created, so every "what was added" view is blind to it, and the
// originals are gone.
//
// This asks first. It writes nothing; it says what is at each path now,
// which series each path would join, and which entries collide with each
// other.

// planBatchMax is how many destinations one call reads. The work is bounded
// by the number of series they fall under rather than by the count, but an
// unbounded batch is still a request that never returns.
const planBatchMax = 500

// planEntry is one file a caller intends to write.
type planEntry struct {
	Path    string `json:"path"              jsonschema:"the full destination path the file would be written to"`
	Size    int64  `json:"size,omitempty"    jsonschema:"the incoming file's size in bytes, to compare against what is there"`
	Series  string `json:"series,omitempty"  jsonschema:"the series the caller believes this is, checked against the series whose folder the path falls under"`
	Season  int    `json:"season,omitempty"`
	Episode int    `json:"episode,omitempty"`
}

// planCurrent is what the library holds at that path now.
type planCurrent struct {
	ItemID       string  `json:"item_id"`
	Name         string  `json:"name,omitempty"`
	Series       string  `json:"series,omitempty"`
	Season       int     `json:"season,omitempty"`
	Episode      int     `json:"episode,omitempty"`
	Size         int64   `json:"size,omitempty"          jsonschema:"file size in bytes"`
	SizeRatio    float64 `json:"size_ratio,omitempty"    jsonschema:"the incoming size over this one, when a size was given: below 1 means the write would replace a bigger file with a smaller"`
	RuntimeS     int     `json:"runtime_s,omitempty"`
	Height       int     `json:"height,omitempty"`
	VideoCodec   string  `json:"video_codec,omitempty"`
	Bitrate      int64   `json:"bitrate,omitempty"`
	DateCreated  string  `json:"date_created,omitempty"`
	FileModified string  `json:"file_modified,omitempty" jsonschema:"Emby only"`
}

// planJoin is the series a path would become an episode of.
type planJoin struct {
	SeriesID   string  `json:"series_id"`
	SeriesName string  `json:"series_name"`
	SeriesYear int     `json:"series_year,omitempty"`
	SeriesPath string  `json:"series_path"`
	Claimed    string  `json:"claimed_series,omitempty"   jsonschema:"the series the caller said this was"`
	ClaimScore float64 `json:"claim_similarity,omitempty" jsonschema:"0 to 1 between the claimed series and the one whose folder this path falls under. Low means the path is under a different show's folder than the caller thinks - which is how one series' episodes get written over another's of the same name"`
}

// planRow is the answer for one destination.
type planRow struct {
	Path      string       `json:"path"`
	Exists    bool         `json:"exists"                 jsonschema:"the library already holds a file at exactly this path: writing there REPLACES it, and the item keeps its id and date_created so nothing afterwards will show what happened"`
	Current   *planCurrent `json:"current"                jsonschema:"what is there now, or null when nothing is"`
	WouldJoin *planJoin    `json:"would_join"             jsonschema:"the series whose folder this path falls under, or null when no series folder holds it"`
	Duplicate []string     `json:"duplicate_of,omitempty" jsonschema:"other paths in this same batch that would land on the same file, or claim the same season and episode. Two entries written to one path leave only the last of them"`
	Note      string       `json:"note,omitempty"         jsonschema:"what could not be established, and why"`
}

func registerPlanTools(r *registry) {
	client := r.client

	type planIn struct {
		Entries []planEntry `json:"entries"           jsonschema:"the destinations to check, at most 500 per call"`
		Library string      `json:"library,omitempty" jsonschema:"restrict the series lookup to one library by name or id"`
	}
	type planOut struct {
		Entries    []planRow `json:"entries"    jsonschema:"one row per entry, in the order given"`
		Existing   int       `json:"existing"   jsonschema:"how many destinations already hold a file"`
		Duplicates int       `json:"duplicates" jsonschema:"how many entries collide with another entry in the same batch"`
		Unplaced   int       `json:"unplaced"   jsonschema:"how many paths fall under no series folder this server knows"`
	}

	add(r, readTool, &mcp.Tool{
		Name: "plan_check",
		Description: "Before writing files into the library: what is at each destination path now, which series each path would join, and which entries collide with each other. Reads only, writes nothing. " +
			"exists true means writing there replaces a file, and the item keeps its id and date_created, so no later 'what was added' read can show it happened. " +
			"claim_similarity low means the path falls under a different show's folder than the caller thinks. At most 500 paths a call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in planIn) (*mcp.CallToolResult, planOut, error) {
		if len(in.Entries) == 0 {
			return nil, planOut{}, errors.New("at least one entry is required")
		}
		if len(in.Entries) > planBatchMax {
			return nil, planOut{}, fmt.Errorf("%d entries is more than the %d this reads in one call: ask in pages", len(in.Entries), planBatchMax)
		}

		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, planOut{}, err
		}
		parent := ""
		if folder != nil {
			parent = folder.ItemID
		}
		index, err := r.seriesCache().get(ctx, r.client, parent)
		if err != nil {
			return nil, planOut{}, err
		}

		// which series' folder each path falls under: the longest folder that
		// is a prefix of it, so a show inside another show's folder still
		// lands on the right one
		joins := make([]*embyfin.Item, len(in.Entries))
		bySeries := map[string][]int{}
		for i, entry := range in.Entries {
			path := filepath.Clean(entry.Path)
			best := -1
			for j := range index.items {
				folder := index.items[j].Path
				if folder == "" || !strings.HasPrefix(path, strings.TrimSuffix(folder, "/")+"/") {
					continue
				}
				if best < 0 || len(folder) > len(index.items[best].Path) {
					best = j
				}
			}
			if best >= 0 {
				joins[i] = &index.items[best]
				bySeries[index.items[best].ID] = append(bySeries[index.items[best].ID], i)
			}
		}

		// one read per series the batch touches, rather than one per file
		held := map[string]*embyfin.Item{}
		for seriesID := range bySeries {
			episodes, eerr := client.Episodes(ctx, seriesID, embyfin.EpisodeOptions{Fields: "Path,MediaSources,DateCreated,DateModified"})
			if eerr != nil {
				return nil, planOut{}, eerr
			}
			for i := range episodes {
				if path := episodes[i].Path; path != "" {
					held[filepath.Clean(path)] = &episodes[i]
				}
			}
		}

		// a path under no series folder is still worth answering for, and
		// Emby can find an item by its path directly
		for i, entry := range in.Entries {
			path := filepath.Clean(entry.Path)
			if joins[i] != nil || held[path] != nil {
				continue
			}
			items, _, serr := client.Search(ctx, embyfin.SearchOptions{Path: entry.Path, Fields: "Path,MediaSources,DateCreated,DateModified", Limit: 1})
			if serr != nil {
				return nil, planOut{}, serr
			}
			// the returned item has to BE at that path: Jellyfin has no path
			// filter at all, and a server that ignores one would otherwise
			// have us report the wrong file as the destination's contents
			for j := range items {
				if filepath.Clean(items[j].Path) == path {
					held[path] = &items[j]

					break
				}
			}
		}

		// entries that would land on each other: the same path twice, or the
		// same episode claimed twice
		samePath := map[string][]int{}
		sameEpisode := map[string][]int{}
		for i, entry := range in.Entries {
			samePath[filepath.Clean(entry.Path)] = append(samePath[filepath.Clean(entry.Path)], i)
			if entry.Series != "" && entry.Episode > 0 {
				key := fmt.Sprintf("%s|s%02de%02d", strings.ToLower(entry.Series), entry.Season, entry.Episode)
				sameEpisode[key] = append(sameEpisode[key], i)
			}
		}

		out := planOut{Entries: make([]planRow, 0, len(in.Entries))}
		for i, entry := range in.Entries {
			path := filepath.Clean(entry.Path)
			row := planRow{Path: entry.Path}

			if item := held[path]; item != nil {
				q := qualityOf(item)
				row.Exists = true
				current := planCurrent{
					ItemID: item.ID, Name: item.Name, Series: item.SeriesName,
					Season: item.ParentIndexNumber, Episode: item.IndexNumber,
					Size: q.Size, RuntimeS: int(item.RunTimeTicks / ticksPerSecond), Height: q.Height,
					VideoCodec: q.VideoCodec, Bitrate: q.Bitrate,
					DateCreated: item.DateCreated, FileModified: item.DateModified,
				}
				if entry.Size > 0 && q.Size > 0 {
					current.SizeRatio = float64(entry.Size) / float64(q.Size)
					current.SizeRatio = float64(int(current.SizeRatio*100+0.5)) / 100
				}
				row.Current = &current
				out.Existing++
			}

			if series := joins[i]; series != nil {
				join := planJoin{
					SeriesID: series.ID, SeriesName: series.Name,
					SeriesYear: series.ProductionYear, SeriesPath: series.Path,
				}
				if entry.Series != "" {
					join.Claimed = entry.Series
					join.ClaimScore, _ = titleScore(entry.Series, series.Name)
				}
				row.WouldJoin = &join
			} else {
				out.Unplaced++
				row.Note = "no series folder on this server holds this path: the file would land outside the library, or in a folder the server has not scanned"
			}

			for _, other := range append(slices.Clone(samePath[path]), sameEpisode[fmt.Sprintf("%s|s%02de%02d", strings.ToLower(entry.Series), entry.Season, entry.Episode)]...) {
				if other != i && !slices.Contains(row.Duplicate, in.Entries[other].Path) {
					row.Duplicate = append(row.Duplicate, in.Entries[other].Path)
				}
			}
			if len(row.Duplicate) > 0 {
				out.Duplicates++
			}

			out.Entries = append(out.Entries, row)
		}

		return nil, out, nil
	})
}
