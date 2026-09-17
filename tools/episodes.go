package tools

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
// few hundred thousand episode files across thousands of series is not
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
	Path       string `json:"path,omitempty"        jsonschema:"the file backing it, with the quality facts; absent when quality is off, and when the server knows of the episode but holds no file"`
	Missing    bool   `json:"missing,omitempty"     jsonschema:"true when the server knows of the episode but holds no file for it"`
	RuntimeS   int    `json:"runtime_s,omitempty"   jsonschema:"runtime in seconds"`

	qualityFacts
}

// episodeFacts is the row for one episode. quality false leaves out
// everything a comparison does not need to place the file - the facts and
// the path, which is the longest field of all - and keeps what names the
// episode. That is most of the bytes when a page holds hundreds of rows,
// and all of them when a caller is reading a library of a quarter of a
// million episodes to reconcile it against a folder.
func episodeFacts(it *embyfin.Item, quality bool, keep map[string]bool) episodeRow {
	row := episodeRow{
		ID:       it.ID,
		SeriesID: it.SeriesID,
		Series:   it.SeriesName,
		Season:   it.ParentIndexNumber,
		Episode:  it.IndexNumber,
		Title:    it.Name,
		Missing:  !it.HasFile(),
		RuntimeS: int(it.RunTimeTicks / 10_000_000),
	}
	if it.IndexNumberEnd > it.IndexNumber {
		row.EpisodeEnd = it.IndexNumberEnd
	}
	if !quality {
		return row
	}
	row.Path = it.Path
	row.qualityFacts = qualityOf(it)
	if keep != nil {
		if !keep["path"] {
			row.Path = ""
		}
		if !keep["runtime_s"] {
			row.RuntimeS = 0
		}
		row.keepOnly(keep)
	}

	return row
}

// qualityFacts are the numbers that decide whether one copy of an episode
// beats another. Two tools answer with them - the bulk read, and the exists
// check asked trash-or-upgrade about its hits - so which file speaks for an
// item is decided here rather than twice.
type qualityFacts struct {
	Container  string       `json:"container,omitempty"`
	Size       int64        `json:"size,omitempty"        jsonschema:"file size in bytes"`
	Bitrate    int64        `json:"bitrate,omitempty"     jsonschema:"video bitrate in bits per second, falling back to the file's overall bitrate"`
	Width      int          `json:"width,omitempty"`
	Height     int          `json:"height,omitempty"`
	VideoCodec string       `json:"video_codec,omitempty"`
	FrameRate  float64      `json:"frame_rate,omitempty"  jsonschema:"frames per second. The one fact a release cannot inflate: a scripted show at 59.94 or 60 was interpolated from a 23.976 master, because no broadcast or disc master of one ships at 60p. Read it beside the resolution - 2160p at 23.976 is plausibly a remaster, 2160p at 59.94 is machine-made"`
	HDR        string       `json:"hdr,omitempty"         jsonschema:"the HDR format the file claims (pq/hlg), from its colour transfer. Claimed on a source that cannot have been HDR - an SD-era show - it is a claim about the encode, not the picture"`
	Audio      []audioTrack `json:"audio,omitempty"       jsonschema:"one entry per audio track"`
	Subtitles  []string     `json:"subtitles,omitempty"   jsonschema:"one entry per subtitle track, by language"`
}

// audioTrack is one audio track, in fields rather than in a sentence.
//
// It used to be the string "eng eac3 6ch", which every caller had to parse
// back, and parsing it is a trap: the second word is the codec only when the
// track has a language, and "a short lowercase token is a language" swallows
// aac and dts. Both of those were real caller bugs, an hour apart.
//
// The bitrate is the field the string never carried, and without it the codec
// name is the only thing to compare on - which is not a quality claim. E-AC-3
// is the more efficient codec, and an E-AC-3 track at 192k is still worse
// than the AC-3 disc track it would replace.
// audioTrackOf reads one audio stream. Every tool that reports audio reports
// it through here, so a track reads the same from item_get as from an
// episode row.
func audioTrackOf(st *embyfin.MediaStream) audioTrack {
	return audioTrack{
		Language: cmp.Or(st.Language, "und"),
		Codec:    st.Codec,
		Channels: st.Channels,
		Bitrate:  st.BitRate,
	}
}

type audioTrack struct {
	Language string `json:"language,omitempty" jsonschema:"und when untagged"`
	Codec    string `json:"codec,omitempty"`
	Channels int    `json:"channels,omitempty" jsonschema:"2 stereo, 6 for 5.1, 8 for 7.1"`
	Bitrate  int64  `json:"bitrate,omitempty"  jsonschema:"bits per second, when known"`
}

// hdrFormat names what a stream's colour transfer claims, and nothing when it
// claims nothing. The names are the transfer functions themselves rather than
// a marketing label: a file either carries one or it does not.
func hdrFormat(st *embyfin.MediaStream) string {
	switch strings.ToLower(st.ColourTransfer) {
	case "smpte2084", "smpte-st-2084", "pq":
		return "pq"
	case "arib-std-b67", "hlg":
		return "hlg"
	}

	return ""
}

// factNames are the facts a caller can ask for by name, so a reconcile that
// compares on resolution and bitrate does not also pay for a subtitle list.
// On a series dubbed into thirty languages the subtitle and audio lists are
// most of the row, and the path is most of the rest; across thousands of
// episodes that is megabytes of answer nobody reads.
var factNames = []string{"path", "runtime_s", "container", "size", "bitrate", "width", "height", "video_codec", "frame_rate", "hdr", "audio", "subtitles"}

// mediaFacts are the ones that need the server's media sources, which is the
// expensive half of an episode read: asked for none of them, we do not ask.
var mediaFacts = []string{"container", "size", "bitrate", "width", "height", "video_codec", "frame_rate", "hdr", "audio", "subtitles"}

// keptFacts reads the fields a caller asked for. Nil means all of them. An
// unknown name is refused rather than ignored: a typo that silently dropped
// the field a trash-or-upgrade decision turns on is the worst way to be
// wrong, because the answer still looks like an answer.
func keptFacts(fields []string) (map[string]bool, error) {
	if len(fields) == 0 {
		return nil, nil
	}

	keep := make(map[string]bool, len(fields))
	for _, f := range fields {
		name := strings.ToLower(strings.TrimSpace(f))
		if !slices.Contains(factNames, name) {
			return nil, fmt.Errorf("no such field %q: fields are %s", f, strings.Join(factNames, ", "))
		}
		keep[name] = true
	}

	return keep, nil
}

// needsMediaSources says whether anything asked for has to be read off the
// file rather than off the episode record.
func needsMediaSources(keep map[string]bool) bool {
	if keep == nil {
		return true
	}

	return slices.ContainsFunc(mediaFacts, func(f string) bool { return keep[f] })
}

// keepOnly drops every fact the caller did not ask for. A nil set keeps all
// of them.
func (q *qualityFacts) keepOnly(keep map[string]bool) {
	if keep == nil {
		return
	}
	if !keep["container"] {
		q.Container = ""
	}
	if !keep["size"] {
		q.Size = 0
	}
	if !keep["bitrate"] {
		q.Bitrate = 0
	}
	if !keep["width"] {
		q.Width = 0
	}
	if !keep["height"] {
		q.Height = 0
	}
	if !keep["video_codec"] {
		q.VideoCodec = ""
	}
	if !keep["frame_rate"] {
		q.FrameRate = 0
	}
	if !keep["hdr"] {
		q.HDR = ""
	}
	if !keep["audio"] {
		q.Audio = nil
	}
	if !keep["subtitles"] {
		q.Subtitles = nil
	}
}

// qualityOf reads the facts off the best file behind an item: a 4K copy
// beside a DVD rip is what the library can play, the same rule audit_quality
// judges by. An item the server holds no file for has none of them.
func qualityOf(it *embyfin.Item) qualityFacts {
	if len(it.MediaSources) == 0 {
		return qualityFacts{}
	}

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

	q := qualityFacts{Container: best.Container, Size: best.Size, Bitrate: best.Bitrate}
	for _, st := range best.MediaStreams {
		switch st.Type {
		case "Video":
			if q.Width == 0 && q.Height == 0 && q.VideoCodec == "" {
				q.Width, q.Height, q.VideoCodec = st.Width, st.Height, st.Codec
				q.FrameRate = math.Round(float64(st.FrameRate)*1000) / 1000
				q.HDR = hdrFormat(&st)
				if st.BitRate > 0 {
					q.Bitrate = st.BitRate
				}
			}
		case "Audio":
			q.Audio = append(q.Audio, audioTrackOf(&st))
		case "Subtitle":
			lang := st.Language
			if lang == "" {
				lang = "und"
			}
			if st.IsExternal {
				lang += " (external)"
			}
			q.Subtitles = append(q.Subtitles, lang)
		}
	}

	return q
}

func episodeRows(items []embyfin.Item, quality bool, keep map[string]bool) []episodeRow {
	out := make([]episodeRow, 0, len(items))
	for i := range items {
		out = append(out, episodeFacts(&items[i], quality, keep))
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

// Asking whether the library holds particular episodes.
//
// A client reconciling an outside folder asks this once per series it found
// there, and a folder of thousands of releases spans hundreds of series. So
// the tool takes a batch of series as readily as one, and a series that
// cannot be resolved fails on its own row rather than taking the other
// thirty-four down with it: half an answer for a reconcile is worth far
// more than an error.

// existsPair is one season and episode number asked after.
type existsPair struct {
	Season  int `json:"season"  jsonschema:"0 for specials"`
	Episode int `json:"episode"`
}

// existsQuery is one series' worth of a batch: which series, and which of
// its episodes to answer for.
type existsQuery struct {
	SeriesID string       `json:"series_id,omitempty" jsonschema:"series item id"`
	Series   string       `json:"series,omitempty"    jsonschema:"series name, when there is no id"`
	Library  string       `json:"library,omitempty"   jsonschema:"narrow a name lookup to one library"`
	Episodes []existsPair `json:"episodes"            jsonschema:"season and episode pairs"`
}

// existsRow is the answer for one episode asked after.
type existsRow struct {
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
	Exists  bool   `json:"exists"               jsonschema:"whether the library holds a file for it"`
	Known   bool   `json:"known"                jsonschema:"whether the server knows of the episode at all; known without exists is a record with no file"`
	ID      string `json:"id,omitempty"         jsonschema:"the episode item id, when the server knows of it"`
	Title   string `json:"title,omitempty"`
	Path    string `json:"path,omitempty"`
	File    string `json:"covered_by,omitempty" jsonschema:"set when the episode is covered by a file holding several, e.g. S01E01E02"`

	// only with quality, and only on a row the library holds a file for: a
	// miss has nothing to say about a file that is not there, and a batch is
	// mostly misses
	RuntimeS int `json:"runtime_s,omitempty" jsonschema:"runtime in seconds"`
	qualityFacts
}

// existsGroup is one series' answer within a batch.
type existsGroup struct {
	Series   string           `json:"series,omitempty"            jsonschema:"the series answered for; the name as asked when it could not be resolved"`
	SeriesID string           `json:"series_id,omitempty"`
	Episodes []existsRow      `json:"episodes"                    jsonschema:"one row per pair asked for, in the order asked"`
	Absent   int              `json:"absent"                      jsonschema:"how many of them the library holds no file for"`
	Match    *seriesCandidate `json:"matched,omitempty"           jsonschema:"how the series name was matched, when one was given: the score, what matched, and the runner-up. Below about 0.9 is a guess - a caller acting unattended should stop and ask. Absent when the series was given by id, which needs no matching"`
	Others   []string         `json:"duplicate_entries,omitempty" jsonschema:"other library entries for this same show, by id. The episodes are split across them, so an absence here is not proof the library lacks the episode: ask these too. audit_duplicates lists every show in this state"`
	Warning  string           `json:"warning,omitempty"           jsonschema:"set when the library holds this show under more than one entry and something was absent. The episodes are split across them, so an absence here is NOT proof the library lacks the episode - ask the other entry too. Only raised when something was absent"`
	Error    string           `json:"error,omitempty"             jsonschema:"why this series could not be answered for: nothing matched the name, or more than one thing did. Episodes is then empty and absent is 0 - which is NOT the same as the library holding none of them. The rest of the batch is answered regardless"`
}

// existsBatchMax is how many series one call will answer for. A batch is one
// server read per season per series, so an unbounded one is a request that
// never returns; a refusal that names the limit is a caller that pages.
const existsBatchMax = 50

// episodesHeld reads the episodes a series holds, keyed by season and
// episode number. A file holding several (S01E01E02) is keyed under each of
// them, because each of those episodes is one the library has.
func episodesHeld(ctx context.Context, client *embyfin.Client, seriesID string, seasons []int, fields string) (map[[2]int]*embyfin.Item, error) {
	// one read per season is cheaper than the whole series until enough
	// seasons are asked about that the whole series is the cheaper read
	queries := seasons
	if len(seasons) > 3 {
		queries = []int{0}
	}

	held := map[[2]int]*embyfin.Item{}
	for _, season := range queries {
		opts := embyfin.EpisodeOptions{Fields: fields}
		if len(queries) > 1 || season > 0 {
			opts.Season = season
		}
		episodes, err := client.Episodes(ctx, seriesID, opts)
		if err != nil {
			return nil, err
		}
		for i := range episodes {
			e := &episodes[i]
			last := max(e.IndexNumberEnd, e.IndexNumber)
			for n := e.IndexNumber; n <= last; n++ {
				held[[2]int{e.ParentIndexNumber, n}] = e
			}
		}
	}

	return held, nil
}

// existsAnswer answers one series' query. The error it returns is the
// caller's to decide about: a single-series call fails on it, a batch puts it
// on the row and carries on with the rest.
func existsAnswer(ctx context.Context, r *registry, q existsQuery, quality bool, keep map[string]bool) (existsGroup, error) {
	series, match, err := resolveSeriesMatch(ctx, r, q.SeriesID, q.Series, q.Library)
	if err != nil {
		return existsGroup{Series: cmp.Or(q.Series, q.SeriesID), Episodes: []existsRow{}, Error: err.Error()}, err
	}

	seasons := []int{}
	for _, p := range q.Episodes {
		if !slices.Contains(seasons, p.Season) {
			seasons = append(seasons, p.Season)
		}
	}

	fields := "Path"
	if quality && needsMediaSources(keep) {
		fields = "Path,MediaSources"
	}
	held, err := episodesHeld(ctx, r.client, series.ID, seasons, fields)
	if err != nil {
		return existsGroup{Series: series.Name, SeriesID: series.ID, Episodes: []existsRow{}, Error: err.Error()}, err
	}

	out := existsGroup{Series: series.Name, SeriesID: series.ID, Match: match, Episodes: []existsRow{}}
	for _, p := range q.Episodes {
		row := existsRow{Season: p.Season, Episode: p.Episode}
		if e, ok := held[[2]int{p.Season, p.Episode}]; ok {
			row.Known, row.ID, row.Title, row.Path = true, e.ID, e.Name, e.Path
			row.Exists = e.HasFile()
			if e.IndexNumberEnd > e.IndexNumber {
				row.File = fmt.Sprintf("S%02dE%02dE%02d", e.ParentIndexNumber, e.IndexNumber, e.IndexNumberEnd)
			}
			if quality && row.Exists {
				row.RuntimeS = int(e.RunTimeTicks / 10_000_000)
				row.qualityFacts = qualityOf(e)
				if keep != nil {
					if !keep["path"] {
						row.Path = ""
					}
					if !keep["runtime_s"] {
						row.RuntimeS = 0
					}
					row.keepOnly(keep)
				}
			}
		}
		if !row.Exists {
			out.Absent++
		}
		out.Episodes = append(out.Episodes, row)
	}

	// a show held twice is worth saying whether or not this call found a gap:
	// the episodes may be split across the entries, and where they are not,
	// the two entries are often the same episodes at different quality, so
	// the one asked may not be the one to compare against
	if others := r.otherEntriesFor(ctx, series); len(others) > 0 {
		where := make([]string, 0, len(others))
		for _, it := range others {
			out.Others = append(out.Others, it.ID)
			where = append(where, fmt.Sprintf("id %s at %s", it.ID, it.Path))
		}
		if out.Absent > 0 {
			out.Warning = fmt.Sprintf("the library holds %q under %d entries and the episodes are split across them (also %s): an absence above is not proof the library lacks the episode - ask the other entry as well, and audit_duplicates lists every show in this state",
				series.Name, len(others)+1, strings.Join(where, "; "))
		}
	}

	return out, nil
}

func registerEpisodeTools(r *registry) {
	client := r.client

	type exportIn struct {
		Library  string   `json:"library,omitempty"   jsonschema:"name or id; default every library"`
		SeriesID string   `json:"series_id,omitempty" jsonschema:"one series, in place of a library"`
		Season   int      `json:"season,omitempty"    jsonschema:"one season; needs series_id"`
		Quality  *bool    `json:"quality,omitempty"   jsonschema:"the facts and the path on each row; default true"`
		Fields   []string `json:"fields,omitempty"    jsonschema:"only these facts on each row: path, runtime_s, container, size, bitrate, width, height, video_codec, frame_rate, hdr, audio, subtitles"`
		WithFile *bool    `json:"with_file,omitempty" jsonschema:"only episodes with a file; default true"`
		Limit    int      `json:"limit,omitempty"     jsonschema:"page size, default 500, max 1000"`
		Cursor   string   `json:"cursor,omitempty"    jsonschema:"from the previous page"`
		Offset   int      `json:"offset,omitempty"    jsonschema:"start here, in place of a cursor"`
	}
	type exportOut struct {
		Total    int          `json:"total"            jsonschema:"episodes matching across every page"`
		Offset   int          `json:"offset"           jsonschema:"where this page starts"`
		Cursor   string       `json:"cursor,omitempty" jsonschema:"pass back as cursor to read the next page; absent when this was the last one"`
		Episodes []episodeRow `json:"episodes"         jsonschema:"ordered by series, then season, then episode, so pages line up across calls"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_episodes",
		Description: "Every episode in a library or one series, paged by cursor, with each file's quality facts: resolution, codec, frame rate, HDR, bitrate, size, runtime and audio tracks. The bulk read for comparing a folder against the library.",
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
		keep, err := keptFacts(in.Fields)
		if err != nil {
			return nil, exportOut{}, err
		}

		opts := embyfin.SearchOptions{
			IncludeItemTypes:  "Episode",
			ParentIndexNumber: in.Season,
			SortBy:            episodeSweepSort,
			SortOrder:         "Ascending",
			Limit:             limit,
			StartIndex:        offset,
			Fields:            "Path,MediaSources",
		}
		if !quality || !needsMediaSources(keep) {
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

		items, total, serr := client.Search(ctx, opts)
		if serr != nil {
			return nil, exportOut{}, serr
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
			out.Episodes = append(out.Episodes, episodeFacts(it, quality, keep))
		}
		// the cursor walks the query, not the rows kept: a page whose rows
		// were all filtered out still has pages after it
		if next := offset + len(items); len(items) > 0 && next < total {
			out.Cursor = encodeCursor(next)
		}

		return nil, out, nil
	})

	type existsIn struct {
		SeriesID string        `json:"series_id,omitempty" jsonschema:"series item id"`
		Series   string        `json:"series,omitempty"    jsonschema:"series name, when there is no id"`
		Library  string        `json:"library,omitempty"   jsonschema:"narrow a name lookup to one library"`
		Episodes []existsPair  `json:"episodes,omitempty"  jsonschema:"season and episode pairs, for one series"`
		Queries  []existsQuery `json:"queries,omitempty"   jsonschema:"up to 50 series, answered in order in results"`
		Quality  *bool         `json:"quality,omitempty"   jsonschema:"add the held copy's facts to each hit; default false"`
		Fields   []string      `json:"fields,omitempty"    jsonschema:"only these facts on each hit: path, runtime_s, container, size, bitrate, width, height, video_codec, frame_rate, hdr, audio, subtitles. Implies quality"`
	}
	type existsOut struct {
		Series   string           `json:"series,omitempty"            jsonschema:"the series asked after, for a single-series call"`
		SeriesID string           `json:"series_id,omitempty"`
		Episodes []existsRow      `json:"episodes,omitempty"          jsonschema:"one row per pair asked for, in the order asked; a single-series call answers here"`
		Absent   int              `json:"absent"                      jsonschema:"how many of the episodes asked after the library holds no file for, across the whole call"`
		Match    *seriesCandidate `json:"matched,omitempty"           jsonschema:"how the series name was matched, when one was given: the score, what matched, and the runner-up. Below about 0.9 is a guess a caller should stop on"`
		Others   []string         `json:"duplicate_entries,omitempty" jsonschema:"other library entries for this same show, by id: the episodes may be split across them"`
		Warning  string           `json:"warning,omitempty"           jsonschema:"set when the library holds this show under more than one entry, so an absence is not proof the library lacks the episode"`
		Results  []existsGroup    `json:"results,omitempty"           jsonschema:"one group per entry in queries, in the order asked; a batch answers here"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "show_episodes_exist",
		Description: "Does the library hold these episodes? Give series_id or series with episodes, or up to 50 series in queries. A multi-episode file counts for each episode. quality or fields add the held copy's facts to hits. " +
			"A group with an error could not be looked up, which is not the same as absent. A name match below 0.9 in matched is a guess. duplicate_entries means the show is split across library entries, so an absence may be held by another.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in existsIn) (*mcp.CallToolResult, existsOut, error) {
		keep, err := keptFacts(in.Fields)
		if err != nil {
			return nil, existsOut{}, err
		}
		// asking for particular facts is asking for the facts
		quality := len(in.Fields) > 0 || (in.Quality != nil && *in.Quality)

		batch := in.Queries
		single := len(batch) == 0
		switch {
		case single:
			batch = []existsQuery{{SeriesID: in.SeriesID, Series: in.Series, Library: in.Library, Episodes: in.Episodes}}
		case in.SeriesID != "" || in.Series != "" || len(in.Episodes) > 0:
			return nil, existsOut{}, errors.New("give queries, or series_id/series and episodes for one series, not both")
		case len(batch) > existsBatchMax:
			return nil, existsOut{}, fmt.Errorf("%d series in one call is more than the %d this answers for: ask in pages", len(batch), existsBatchMax)
		}

		// a bad pair is the caller's mistake rather than a fact about the
		// library, so it stops the call instead of riding along as a row
		for _, q := range batch {
			if len(q.Episodes) == 0 {
				return nil, existsOut{}, errors.New("at least one season and episode pair is required")
			}
			for _, p := range q.Episodes {
				if p.Episode <= 0 {
					return nil, existsOut{}, fmt.Errorf("episode must be 1 or more, got %d for season %d", p.Episode, p.Season)
				}
			}
		}

		out := existsOut{}
		for _, q := range batch {
			group, aerr := existsAnswer(ctx, r, q, quality, keep)
			if single && aerr != nil {
				return nil, existsOut{}, aerr
			}
			out.Absent += group.Absent
			if single {
				out.Series, out.SeriesID, out.Episodes = group.Series, group.SeriesID, group.Episodes
				out.Match, out.Others, out.Warning = group.Match, group.Others, group.Warning

				continue
			}
			out.Results = append(out.Results, group)
		}

		return nil, out, nil
	})
}
