package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Reading episodes in bulk, and asking after one.
//
// A client comparing an outside folder against the library needs every
// episode it holds and the facts that decide whether a copy is worth
// replacing, and it needs them without a call per series: a library of a
// quarter of a million episode files across nine thousand series is not
// readable one series at a time. So the quality facts are spelled out as
// numbers on the row itself - width, height, bitrate, size - rather than
// left to a second read per item or to a string a client would have to
// parse back.

// episodeRow is one episode as a bulk read answers for it.
type episodeRow struct {
	ID         string `json:"id"`
	SeriesID   string `json:"series_id,omitempty"`
	Series     string `json:"series,omitempty"`
	Season     int    `json:"season"`
	Episode    int    `json:"episode"`
	EpisodeEnd int    `json:"episode_end,omitempty" jsonschema:"the last episode number when one file holds several (S01E01E02); absent for the usual one-episode file"`
	Title      string `json:"title,omitempty"`
	Path       string `json:"path,omitempty"        jsonschema:"the file backing it; absent when the server knows of the episode but holds no file"`
	Missing    bool   `json:"missing,omitempty"     jsonschema:"true when the server knows of the episode but holds no file for it"`

	Container  string   `json:"container,omitempty"`
	Size       int64    `json:"size,omitempty"        jsonschema:"file size in bytes"`
	Bitrate    int64    `json:"bitrate,omitempty"     jsonschema:"video bitrate in bits per second, falling back to the file's overall bitrate"`
	Width      int      `json:"width,omitempty"`
	Height     int      `json:"height,omitempty"`
	VideoCodec string   `json:"video_codec,omitempty"`
	RuntimeS   int      `json:"runtime_s,omitempty"   jsonschema:"runtime in seconds"`
	Audio      []string `json:"audio,omitempty"       jsonschema:"one entry per audio track: language, codec and channels"`
	Subtitles  []string `json:"subtitles,omitempty"   jsonschema:"one entry per subtitle track, by language"`
}

// episodeFacts is the row for one episode. quality false leaves out
// everything a comparison does not need to place the file, which is most of
// the bytes when a page holds hundreds of rows.
func episodeFacts(it *embyfin.Item, quality bool) episodeRow {
	row := episodeRow{
		ID:       it.ID,
		SeriesID: it.SeriesID,
		Series:   it.SeriesName,
		Season:   it.ParentIndexNumber,
		Episode:  it.IndexNumber,
		Title:    it.Name,
		Path:     it.Path,
		Missing:  !it.HasFile(),
		RuntimeS: int(it.RunTimeTicks / 10_000_000),
	}
	if it.IndexNumberEnd > it.IndexNumber {
		row.EpisodeEnd = it.IndexNumberEnd
	}
	if !quality || len(it.MediaSources) == 0 {
		return row
	}

	// the best file decides the facts: a 4K copy beside a DVD rip is what the
	// library can play, the same rule audit_quality judges by
	best := &it.MediaSources[0]
	bestHeight := -1
	for i := range it.MediaSources {
		h := 0
		if v := videoOf(&it.MediaSources[i]); v != nil {
			h = v.Height
		}
		if h > bestHeight {
			best, bestHeight = &it.MediaSources[i], h
		}
	}
	row.Container, row.Size, row.Bitrate = best.Container, best.Size, best.Bitrate
	for _, st := range best.MediaStreams {
		switch st.Type {
		case "Video":
			if row.Width == 0 && row.Height == 0 && row.VideoCodec == "" {
				row.Width, row.Height, row.VideoCodec = st.Width, st.Height, st.Codec
				if st.BitRate > 0 {
					row.Bitrate = st.BitRate
				}
			}
		case "Audio":
			desc := st.Codec
			if st.Language != "" {
				desc = st.Language + " " + desc
			}
			if st.Channels > 0 {
				desc = fmt.Sprintf("%s %dch", desc, st.Channels)
			}
			row.Audio = append(row.Audio, desc)
		case "Subtitle":
			lang := st.Language
			if lang == "" {
				lang = "und"
			}
			if st.IsExternal {
				lang += " (external)"
			}
			row.Subtitles = append(row.Subtitles, lang)
		}
	}

	return row
}

func episodeRows(items []embyfin.Item, quality bool) []episodeRow {
	out := make([]episodeRow, 0, len(items))
	for i := range items {
		out = append(out, episodeFacts(&items[i], quality))
	}

	return out
}

// How many episodes a bulk read answers with by default, and the most it
// will: a sweep is read in pages rather than in one answer no client can
// hold, and the servers' own item query pages at a thousand.
const (
	episodePage    = 500
	episodePageMax = 1000
)

// episodeCursor is where the next page starts, handed back opaque so paging
// stays the server's business rather than arithmetic the caller repeats.
type episodeCursor struct {
	Offset int `json:"o"`
}

func encodeCursor(offset int) string {
	b, err := json.Marshal(episodeCursor{Offset: offset})
	if err != nil { // a struct of one int cannot fail to marshal
		return ""
	}

	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (int, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return 0, fmt.Errorf("cursor %q is not one this tool handed out: %w", s, err)
	}
	var c episodeCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return 0, fmt.Errorf("cursor %q is not one this tool handed out: %w", s, err)
	}
	if c.Offset < 0 {
		return 0, fmt.Errorf("cursor %q points before the first episode", s)
	}

	return c.Offset, nil
}

// episodeSweepSort is the order a bulk read answers in: by series, then by
// season and episode within it. It has to be total and stable, or a page
// boundary would drop or repeat rows between calls.
const episodeSweepSort = "SeriesSortName,ParentIndexNumber,IndexNumber,SortName"

func registerEpisodeTools(r *registry) {
	client := r.client

	type exportIn struct {
		Library  string `json:"library,omitempty"   jsonschema:"library name or id; default every library"`
		SeriesID string `json:"series_id,omitempty" jsonschema:"restrict to one series by item id, in place of a library"`
		Season   int    `json:"season,omitempty"    jsonschema:"restrict to one season number; needs series_id"`
		Quality  *bool  `json:"quality,omitempty"   jsonschema:"include the quality facts (width, height, video_codec, bitrate, size, container, runtime, audio, subtitles); default true. False gives a much smaller answer when only the episode list is wanted"`
		WithFile *bool  `json:"with_file,omitempty" jsonschema:"only episodes the library holds a file for; default true. False also lists the ones the server knows of but has no file for"`
		Limit    int    `json:"limit,omitempty"     jsonschema:"page size, default 500, maximum 1000"`
		Cursor   string `json:"cursor,omitempty"    jsonschema:"the cursor the previous call handed back, to read the next page"`
		Offset   int    `json:"offset,omitempty"    jsonschema:"skip this many episodes, in place of a cursor"`
	}
	type exportOut struct {
		Total    int          `json:"total"            jsonschema:"episodes matching across every page"`
		Offset   int          `json:"offset"           jsonschema:"where this page starts"`
		Cursor   string       `json:"cursor,omitempty" jsonschema:"pass back as cursor to read the next page; absent when this was the last one"`
		Episodes []episodeRow `json:"episodes"         jsonschema:"ordered by series, then season, then episode, so pages line up across calls"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "library_episodes",
		Description: "Every episode in a library, paged, with the quality facts on each row: series, season, episode, title, path, container, size, bitrate, width, height, video codec and runtime. " +
			"This is the bulk read for comparing a whole outside folder against the library, or for finding what to upgrade; show_episodes answers one series at a time, which is a call per series. Page with the cursor it hands back. Default page 500.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in exportIn) (*mcp.CallToolResult, exportOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = episodePage
		}
		limit = min(limit, episodePageMax)

		offset := max(in.Offset, 0)
		if in.Cursor != "" {
			at, err := decodeCursor(in.Cursor)
			if err != nil {
				return nil, exportOut{}, err
			}
			offset = at
		}
		quality := in.Quality == nil || *in.Quality
		withFile := in.WithFile == nil || *in.WithFile

		opts := embyfin.SearchOptions{
			IncludeItemTypes:  "Episode",
			ParentIndexNumber: in.Season,
			SortBy:            episodeSweepSort,
			SortOrder:         "Ascending",
			Limit:             limit,
			StartIndex:        offset,
			Fields:            "Path,MediaSources",
		}
		if !quality {
			opts.Fields = "Path"
		}

		switch {
		case in.SeriesID != "":
			if in.Library != "" {
				return nil, exportOut{}, errors.New("give library or series_id, not both: a series is already in one library")
			}
			series, err := client.ItemByID(ctx, in.SeriesID)
			if err != nil {
				return nil, exportOut{}, err
			}
			opts.ParentID = series.ID
		case in.Season > 0:
			return nil, exportOut{}, errors.New("season needs series_id: a season number means nothing across a library")
		default:
			folder, err := resolveLibrary(ctx, client, in.Library)
			if err != nil {
				return nil, exportOut{}, err
			}
			if folder != nil {
				opts.ParentID = folder.ItemID
			}
		}

		items, total, err := client.Search(ctx, opts)
		if err != nil {
			return nil, exportOut{}, err
		}

		out := exportOut{Total: total, Offset: offset, Episodes: []episodeRow{}}
		for i := range items {
			it := &items[i]
			if in.Season > 0 && it.ParentIndexNumber != in.Season {
				continue
			}
			if withFile && !it.HasFile() {
				continue
			}
			out.Episodes = append(out.Episodes, episodeFacts(it, quality))
		}
		// the cursor walks the query, not the rows kept: a page whose rows
		// were all filtered out still has pages after it
		if next := offset + len(items); len(items) > 0 && next < total {
			out.Cursor = encodeCursor(next)
		}

		return nil, out, nil
	})

	type pair struct {
		Season  int `json:"season"  jsonschema:"season number (0 for specials)"`
		Episode int `json:"episode" jsonschema:"episode number within the season"`
	}
	type existsIn struct {
		SeriesID string `json:"series_id,omitempty" jsonschema:"the series item id; give this or series"`
		Series   string `json:"series,omitempty"    jsonschema:"the series by name, when its id is not to hand; a name matching more than one series is refused with the matches. show_resolve turns a release name into an id"`
		Library  string `json:"library,omitempty"   jsonschema:"restrict the name lookup to one library by name or id, when the same show is held in more than one"`
		Episodes []pair `json:"episodes"            jsonschema:"the season and episode numbers to check, one entry each"`
	}
	type existsRow struct {
		Season  int    `json:"season"`
		Episode int    `json:"episode"`
		Exists  bool   `json:"exists"               jsonschema:"whether the library holds a file for it"`
		Known   bool   `json:"known"                jsonschema:"whether the server knows of the episode at all; known without exists is a record with no file"`
		ID      string `json:"id,omitempty"         jsonschema:"the episode item id, when the server knows of it"`
		Title   string `json:"title,omitempty"`
		Path    string `json:"path,omitempty"`
		File    string `json:"covered_by,omitempty" jsonschema:"set when the episode is covered by a file holding several, e.g. S01E01E02"`
	}
	type existsOut struct {
		Series   string      `json:"series"`
		SeriesID string      `json:"series_id"`
		Episodes []existsRow `json:"episodes"  jsonschema:"one row per pair asked for, in the order asked"`
		Absent   int         `json:"absent"    jsonschema:"how many of them the library holds no file for"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "show_episodes_exist",
		Description: "Does the library hold these episodes? Answers a batch of season and episode numbers for one series without listing the whole series, which for a long-running show is hundreds of rows to ask one question. " +
			"A file holding several episodes (S01E01E02) counts for each of them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in existsIn) (*mcp.CallToolResult, existsOut, error) {
		if len(in.Episodes) == 0 {
			return nil, existsOut{}, errors.New("at least one season and episode pair is required")
		}
		series, err := resolveSeries(ctx, client, in.SeriesID, in.Series, in.Library)
		if err != nil {
			return nil, existsOut{}, err
		}

		seasons := []int{}
		for _, p := range in.Episodes {
			if p.Episode <= 0 {
				return nil, existsOut{}, fmt.Errorf("episode must be 1 or more, got %d for season %d", p.Episode, p.Season)
			}
			if !slices.Contains(seasons, p.Season) {
				seasons = append(seasons, p.Season)
			}
		}

		// one read per season is cheaper than the whole series until enough
		// seasons are asked about that the whole series is the cheaper read
		queries := seasons
		if len(seasons) > 3 {
			queries = []int{0}
		}
		held := map[[2]int]*embyfin.Item{}
		for _, season := range queries {
			opts := embyfin.EpisodeOptions{Fields: "Path"}
			if len(queries) > 1 || season > 0 {
				opts.Season = season
			}
			episodes, eerr := client.Episodes(ctx, series.ID, opts)
			if eerr != nil {
				return nil, existsOut{}, eerr
			}
			for i := range episodes {
				e := &episodes[i]
				last := max(e.IndexNumberEnd, e.IndexNumber)
				for n := e.IndexNumber; n <= last; n++ {
					held[[2]int{e.ParentIndexNumber, n}] = e
				}
			}
		}

		out := existsOut{Series: series.Name, SeriesID: series.ID, Episodes: []existsRow{}}
		for _, p := range in.Episodes {
			row := existsRow{Season: p.Season, Episode: p.Episode}
			if e, ok := held[[2]int{p.Season, p.Episode}]; ok {
				row.Known, row.ID, row.Title, row.Path = true, e.ID, e.Name, e.Path
				row.Exists = e.HasFile()
				if e.IndexNumberEnd > e.IndexNumber {
					row.File = fmt.Sprintf("S%02dE%02dE%02d", e.ParentIndexNumber, e.IndexNumber, e.IndexNumberEnd)
				}
			}
			if !row.Exists {
				out.Absent++
			}
			out.Episodes = append(out.Episodes, row)
		}

		return nil, out, nil
	})
}
