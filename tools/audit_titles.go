package tools

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
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

func registerTitleAudits(r *registry) {
	client := r.client

	type dupTitlesIn struct {
		Library string `json:"library,omitempty" jsonschema:"one library by name or id"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum groups, default 50"`
	}
	type dupTitlesOut struct {
		Scanned int          `json:"items_scanned"`
		Found   int          `json:"total_groups"`
		Groups  []titleGroup `json:"groups"        jsonschema:"near-certain groups first, then leads; capped at limit"`
	}

	add(r, readTool, &mcp.Tool{
		Name: "audit_duplicate_titles",
		Description: "Find one episode's content filed under two episode numbers: a season holding the same episode title twice. " +
			"Neither other duplicate audit sees this - audit_duplicates matches provider ids, which differ because the server believes they are different episodes, and audit_multiple_versions finds several files under one item. " +
			"Runtimes within 5% make it near certain; matching titles alone are a lead, because a season can reuse a title and generic ones repeat by nature. It does not pick a winner: the larger file can be the worse copy.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dupTitlesIn) (*mcp.CallToolResult, dupTitlesOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		opts, err := sweepOptions(ctx, client, in.Library, typeEpisode, typeEpisode, "Path,MediaSources")
		if err != nil {
			return nil, dupTitlesOut{}, err
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
			return nil, dupTitlesOut{}, err
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

		return nil, out, nil
	})
}

// episodeTitleFromFile reads the title a file name claims, which is whatever
// follows the season and episode marker once the extension and the encode's
// words are off: "Show - 01x01 - The DVD.mkv" claims "The DVD". It returns ""
// for a name that claims nothing, which is most of a tidy library.
func episodeTitleFromFile(path string) string {
	name := filepath.Base(path)
	name = fileExtension.ReplaceAllString(name, "")

	var after string
	for _, re := range releaseMarkers {
		if m := re.FindStringIndex(name); m != nil {
			after = name[m[1]:]

			break
		}
	}
	if after == "" {
		return ""
	}

	// the same cleanup a release name gets: dots and underscores are spaces,
	// and the encode's words end a title
	after = strings.TrimLeft(after, " -_.")
	after = strings.NewReplacer(".", " ", "_", " ").Replace(after)
	words := strings.Fields(after)
	for i, w := range words {
		if releaseJunk[strings.ToLower(strings.Trim(w, "()[]-"))] {
			words = words[:i]

			break
		}
	}

	return strings.Trim(strings.Join(words, " "), " -_([{")
}

// mismatchRow is one reported file.
type mismatchRow struct {
	ID            string  `json:"id"`
	Series        string  `json:"series,omitempty"`
	Season        int     `json:"season"`
	Episode       int     `json:"episode"`
	TitleInFile   string  `json:"title_in_file"    jsonschema:"the title the file name claims"`
	TitleOnServer string  `json:"title_on_server"  jsonschema:"the title the server holds, from its metadata provider"`
	Score         float64 `json:"similarity"       jsonschema:"0 to 1: how close the two are once case, punctuation and accents are folded"`
	Path          string  `json:"path,omitempty"`
	// where the file's title sits in the provider's own list, which tells
	// a file numbered another way (a download numbered from TVDB in a
	// library matched to TMDB) from a file that is simply wrong
	TMDBEpisode string `json:"tmdb_episode,omitempty" jsonschema:"the episode TMDB gives the file's title to, as SxxEyy, when the series has a TMDB id and EMBYFIN_TMDB_TOKEN is set: a different number means the file is numbered in another order, not mislabelled"`
	Diagnosis   string `json:"diagnosis,omitempty"    jsonschema:"what the tmdb lookup made of it"`

	seriesID string
}

func registerTitleMismatchAudit(r *registry) {
	client := r.client

	type mismatchIn struct {
		Library string `json:"library,omitempty" jsonschema:"one library by name or id"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum findings, default 100"`
	}
	type mismatchOut struct {
		Scanned  int           `json:"items_scanned"`
		Found    int           `json:"total_findings"`
		Unnamed  int           `json:"unnamed"        jsonschema:"files whose name claims no title at all, so there was nothing to compare; not findings"`
		Findings []mismatchRow `json:"findings"       jsonschema:"least similar first; capped at limit"`
	}

	provider := tmdbFacts(r.opts)
	add(r, readTool, &mcp.Tool{
		Name: "audit_title_mismatch",
		Description: "Find episodes whose file name claims a different title from the one the server holds, with both strings side by side. " +
			"A file named after one episode sitting where the server holds a different one is the tell that a file from another series was written to this path, or of a file numbered in another provider's order: with EMBYFIN_TMDB_TOKEN set, each row says which episode TMDB gives the file's title to, so a download numbered from TVDB in a library matched to TMDB reads as that rather than as a mislabel. Files whose name claims no title are counted in unnamed, not reported.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mismatchIn) (*mcp.CallToolResult, mismatchOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 100
		}
		opts, err := sweepOptions(ctx, client, in.Library, typeEpisode, typeEpisode, "Path")
		if err != nil {
			return nil, mismatchOut{}, err
		}

		out := mismatchOut{Findings: []mismatchRow{}}
		var findings []mismatchRow
		if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
			for i := range items {
				it := &items[i]
				out.Scanned++
				if it.Path == "" || it.Name == "" {
					continue
				}
				claimed := episodeTitleFromFile(it.Path)
				if claimed == "" {
					out.Unnamed++

					continue
				}
				score, _ := titleScore(claimed, it.Name)
				if score >= seriesConfident {
					continue
				}
				findings = append(findings, mismatchRow{
					ID: it.ID, Series: it.SeriesName, Season: it.ParentIndexNumber, Episode: it.IndexNumber,
					TitleInFile: claimed, TitleOnServer: it.Name, Score: score, Path: it.Path, seriesID: it.SeriesID,
				})
			}

			return true
		}); err != nil {
			return nil, mismatchOut{}, err
		}

		// least alike first: a file that shares nothing with the title the
		// server holds is the one worth looking at
		slices.SortFunc(findings, func(a, b mismatchRow) int {
			if a.Score != b.Score {
				if a.Score < b.Score {
					return -1
				}

				return 1
			}

			return strings.Compare(a.Path, b.Path)
		})
		out.Found = len(findings)
		out.Findings = append(out.Findings, findings[:min(len(findings), limit)]...)
		if provider != nil {
			diagnoseByTMDB(ctx, client, provider, out.Findings)
		}

		return nil, out, nil
	})
}

// diagnoseByTMDB looks each reported file's title up in TMDB's list of the
// series' episodes, one read per series, and says on the row where TMDB
// puts that title: the same number as the file is a title the server has
// reworded, another number is a file numbered in a different order, and no
// number is a title TMDB has never heard of.
func diagnoseByTMDB(ctx context.Context, client *embyfin.Client, provider *tmdb.Facts, rows []mismatchRow) {
	guides := map[string][]tmdb.Episode{} // series item id -> TMDB's episodes, nil when it cannot be read
	for i := range rows {
		row := &rows[i]
		eps, ok := guides[row.seriesID]
		if !ok {
			eps = tmdbGuide(ctx, client, provider, row.seriesID)
			guides[row.seriesID] = eps
		}
		if eps == nil {
			continue
		}
		best, bestScore := tmdb.Episode{}, 0.0
		for _, e := range eps {
			if score, _ := titleScore(row.TitleInFile, e.Name); score > bestScore {
				best, bestScore = e, score
			}
		}
		switch {
		case bestScore < seriesConfident:
			row.Diagnosis = "no TMDB episode of this series has the file's title: the file is from another series, or named by hand"
		case best.Season == row.Season && best.Episode == row.Episode:
			row.TMDBEpisode = fmt.Sprintf("S%02dE%02d", best.Season, best.Episode)
			row.Diagnosis = "TMDB gives the file's title to this very episode: the server's title is a reworded one, not a different episode"
		default:
			row.TMDBEpisode = fmt.Sprintf("S%02dE%02d", best.Season, best.Episode)
			row.Diagnosis = fmt.Sprintf("the file's title is TMDB's %s, not S%02dE%02d: the file is numbered in another order (a download numbered from TVDB, most often), and its content is that episode", row.TMDBEpisode, row.Season, row.Episode)
		}
	}
}

// tmdbGuide is TMDB's episodes for a library series, or nil when the series
// has no TMDB id or TMDB cannot be asked.
func tmdbGuide(ctx context.Context, client *embyfin.Client, provider *tmdb.Facts, seriesID string) []tmdb.Episode {
	if seriesID == "" {
		return nil
	}
	series, err := client.ItemByID(ctx, seriesID)
	if err != nil {
		return nil
	}
	id := providerID(series, "tmdb")
	if id == "" {
		return nil
	}
	eps, err := provider.SeriesEpisodes(ctx, id)
	if err != nil {
		return nil
	}

	return eps
}
