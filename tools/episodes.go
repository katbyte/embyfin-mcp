package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
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

// numberText is a season or episode number as an episode's code writes it,
// two digits, or ?? when the server holds none: an unnumbered episode is not
// a special and not episode 0.
func numberText(n *int) string {
	if n == nil {
		return "??"
	}

	return fmt.Sprintf("%02d", *n)
}

// episodeCode names an episode by its numbers, S01E02, with ?? for a number
// the server does not hold.
func episodeCode(it *embyfin.Item) string {
	return "S" + numberText(it.ParentIndexNumber) + "E" + numberText(it.IndexNumber)
}

// runCode is episodeCode with the last episode of a file holding several:
// S01E02E03.
func runCode(it *embyfin.Item) string {
	code := episodeCode(it)
	if it.IndexNumber != nil && it.IndexNumberEnd > *it.IndexNumber {
		code += fmt.Sprintf("E%02d", it.IndexNumberEnd)
	}

	return code
}

// numbered says whether the server holds both an episode's season and its
// number, which is what matching it to a season and episode asked after
// needs: one without either answers for no number at all.
func numbered(it *embyfin.Item) bool {
	return it.ParentIndexNumber != nil && it.IndexNumber != nil
}

// compareNumbers orders two season or episode numbers, one the server does
// not hold after every one it does.
func compareNumbers(a, b *int) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}

	return cmp.Compare(*a, *b)
}

// episodeSpan is the episode numbers a numbered file holds: its own, through
// its last when it holds several. An unnumbered one holds none.
func episodeSpan(it *embyfin.Item) []int {
	if it.IndexNumber == nil {
		return nil
	}
	first := *it.IndexNumber
	out := []int{}
	for n := first; n <= max(first, it.IndexNumberEnd); n++ {
		out = append(out, n)
	}

	return out
}

// episodeRow is one episode as a bulk read answers for it.
type episodeRow struct {
	ID         string `json:"id"`
	SeriesID   string `json:"series_id,omitempty"`
	Series     string `json:"series,omitempty"`
	Season     *int   `json:"season"                jsonschema:"the season number, 0 for the specials; null when the server holds no season number for the episode, which is not a special (Jellyfin holds a file named without SxxEyy that way, even in a Season 01 folder, and Emby one at the show's root)"`
	Episode    *int   `json:"episode"               jsonschema:"null when the server holds no episode number for it (a file named without one, or an extra Emby took for an episode)"`
	EpisodeEnd int    `json:"episode_end,omitempty" jsonschema:"the last episode number when one file holds several (S01E01E02); absent for the usual one-episode file"`
	Title      string `json:"title,omitempty"`
	Path       string `json:"path,omitempty"        jsonschema:"the file the row's facts are read from; absent when quality is off, and when the server knows of the episode but holds no file. An episode held in more than one file lists every one in versions"`
	Missing    bool   `json:"missing,omitempty"     jsonschema:"true when the server knows of the episode but holds no file for it"`
	// Jellyfin folds a second file of one episode in one folder into the
	// episode, and no item query lists it on its own: read by path alone,
	// that file is not in the library at all
	Versions []versionRow `json:"versions,omitempty" jsonschema:"every file the episode is held in, when more than one, each with its own id, path and facts: Jellyfin folds a second file of one episode in one folder into the episode as a version and lists it nowhere else, so a folder compared against path alone misses it. Deleting the episode deletes every one"`
	// A file written over an existing path keeps the item's id and its
	// date_created, so a library read sorted by what was added cannot see it.
	// file_modified is the only field that moves, and only Emby has it.
	DateCreated  string `json:"date_created,omitempty"  jsonschema:"when the item was added to the library; unchanged when a file is written over an existing path"`
	FileModified string `json:"file_modified,omitempty" jsonschema:"when the file itself last changed (Emby only; Jellyfin's item carries no such field). This is what moves when a download overwrites a path in place"`
	RuntimeS     int    `json:"runtime_s,omitempty"     jsonschema:"runtime in seconds"`

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
	if it.IndexNumber != nil && it.IndexNumberEnd > *it.IndexNumber {
		row.EpisodeEnd = it.IndexNumberEnd
	}
	if !quality {
		return row
	}
	row.Path = it.Path
	row.DateCreated, row.FileModified = it.DateCreated, it.DateModified
	row.qualityFacts = pathQuality(it)
	row.Versions = versionRows(it, keep)
	if keep != nil {
		if !keep["path"] {
			row.Path = ""
		}
		if !keep["date_created"] {
			row.DateCreated = ""
		}
		if !keep["file_modified"] {
			row.FileModified = ""
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
	Container string `json:"container,omitempty"`
	Size      int64  `json:"size,omitempty"      jsonschema:"file size in bytes"`
	Bitrate   int64  `json:"bitrate,omitempty"   jsonschema:"video bitrate in bits per second, falling back to the file's overall bitrate"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	// the stored frame is not always the shape the picture is shown at:
	// a DVD rip is 720x480 or 720x576 whether it is 4:3 or an anamorphic
	// 16:9, and only the ratio the file states tells which
	AspectRatio  string       `json:"aspect_ratio,omitempty"  jsonschema:"the shape the picture is shown at, as the file states it (16:9, 4:3, or 1.78:1 as a decimal against 1); absent when the file does not say, which on a DVD-sized frame means the shape is not known"`
	DisplayWidth int          `json:"display_width,omitempty" jsonschema:"the width the picture is shown at when that differs from width: an anamorphic 720x480 16:9 DVD shows at 853x480"`
	VideoCodec   string       `json:"video_codec,omitempty"`
	FrameRate    float64      `json:"frame_rate,omitempty"    jsonschema:"frames per second. A film or a scripted show at 50, 59.94 or 60 was most likely interpolated from a 24 or 25 master, which adds no picture; but sport, much broadcast TV (720p50 and 720p60) and some documentaries are shot at 50 or 60, so it is a lead to check against what the source is, not proof"`
	HDR          string       `json:"hdr,omitempty"           jsonschema:"the dynamic range as the server read it: sdr, hdr10, hdr10plus, hlg; Dolby Vision as dovi (no base layer another player can show, or one the server did not name), dovi_hdr10, dovi_hdr10plus, dovi_hlg or dovi_sdr (by the base layer a player without Dolby Vision falls back to), dovi_el (profile 7, an enhancement layer over an HDR10 base) or dovi_el_hdr10plus, dovi_invalid (Dolby Vision the server calls invalid); hdr when the server says HDR and no more; unknown when the server has not said. Present on every row with video: an absent field would read as SDR, and 'we did not look' is not a measurement"`
	Audio        []audioTrack `json:"audio,omitempty"         jsonschema:"one entry per audio track"`
	Subtitles    []string     `json:"subtitles,omitempty"     jsonschema:"one entry per subtitle track, by language as the server spells it (Emby en, Jellyfin eng), marked (external) for a file beside the video and (forced) for a track that shows only the lines in another language than the audio's"`
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

// factNames are the facts a caller can ask for by name, so a reconcile that
// compares on resolution and bitrate does not also pay for a subtitle list.
// On a series dubbed into thirty languages the subtitle and audio lists are
// most of the row, and the path is most of the rest; across thousands of
// episodes that is megabytes of answer nobody reads.
var factNames = []string{"path", "date_created", "file_modified", "runtime_s", "container", "size", "bitrate", "width", "height", "aspect_ratio", "display_width", "video_codec", "frame_rate", "hdr", "audio", "subtitles"}

// mediaFacts are the ones that need the server's media sources, which is the
// expensive half of an episode read: asked for none of them, we do not ask.
var mediaFacts = []string{"container", "size", "bitrate", "width", "height", "aspect_ratio", "display_width", "video_codec", "frame_rate", "hdr", "audio", "subtitles"}

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
	if !keep["aspect_ratio"] {
		q.AspectRatio = ""
	}
	if !keep["display_width"] {
		q.DisplayWidth = 0
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

// pathQuality reads the facts off the file an item's path names.
func pathQuality(it *embyfin.Item) qualityFacts {
	if src := it.OwnSource(); src != nil {
		return sourceQuality(src)
	}

	return qualityFacts{}
}

// versionRows lists every file an item is held in, each with its own facts
// and kept to the facts a caller asked for, when there is more than one:
// Jellyfin folds a second file of a film or an episode in one folder into
// the item as a version, and no item query lists that file on its own.
func versionRows(it *embyfin.Item, keep map[string]bool) []versionRow {
	if len(it.MediaSources) < 2 {
		return nil
	}
	out := make([]versionRow, 0, len(it.MediaSources))
	for i := range it.MediaSources {
		src := &it.MediaSources[i]
		v := versionRow{ID: src.ItemID, Label: src.Name, Path: src.Path, RuntimeS: int(src.RunTimeTicks / ticksPerSecond), qualityFacts: sourceQuality(src)}
		if keep != nil {
			if !keep["path"] {
				v.Path = ""
			}
			if !keep["runtime_s"] {
				v.RuntimeS = 0
			}
			v.keepOnly(keep)
		}
		out = append(out, v)
	}

	return out
}

// qualityOf reads the facts off the best file behind an item: a 4K copy
// beside a DVD rip is what the library can play, the same rule audit_quality
// judges by. An item the server holds no file for has none of them.
func qualityOf(it *embyfin.Item) qualityFacts {
	best := it.BestSource()
	if best == nil {
		return qualityFacts{}
	}

	return sourceQuality(best)
}

// qualityAt reads the facts off the file at a path rather than off the best
// of an item's versions: what writing to that path would replace. An item
// whose versions do not list the path falls back to its best file.
func qualityAt(it *embyfin.Item, path string) qualityFacts {
	if src := it.SourceAt(path); src != nil {
		return sourceQuality(src)
	}

	return qualityOf(it)
}

// sourceQuality reads the facts off one file.
func sourceQuality(best *embyfin.MediaSource) qualityFacts {
	q := qualityFacts{Container: best.Container, Size: best.Size, Bitrate: best.Bitrate}
	for _, st := range best.MediaStreams {
		switch st.Type {
		case "Video":
			if q.Width == 0 && q.Height == 0 && q.VideoCodec == "" {
				q.Width, q.Height, q.VideoCodec = st.Width, st.Height, st.Codec
				q.AspectRatio, q.DisplayWidth = st.AspectRatio, st.DisplayWidth()
				q.FrameRate = math.Round(float64(st.FrameRate)*1000) / 1000
				q.HDR = st.HDR()
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
			// a forced track carries only the lines in another language than
			// the audio's, which is not subtitles to follow by
			switch {
			case st.IsForced && st.IsExternal:
				lang += " (forced, external)"
			case st.IsForced:
				lang += " (forced)"
			case st.IsExternal:
				lang += " (external)"
			}
			q.Subtitles = append(q.Subtitles, lang)
		}
	}

	return q
}

// How many episodes a bulk read answers with by default, and the most it
// will: a sweep is read in pages rather than in one answer no client can
// hold, and the servers' own item query pages at a thousand.
const (
	episodePage    = 500
	episodePageMax = 1000
)

// episodeSweepSort is the order a bulk read answers in: by series, then by
// season and episode within it. It has to be total and stable, or a page
// boundary would drop or repeat rows between calls, so it ends in when each
// was added: a show held twice has two of every episode, alike in every key
// before that. Neither server sorts by anything that tells every item apart.
const episodeSweepSort = "SeriesSortName,ParentIndexNumber,IndexNumber,SortName,DateCreated"

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

	// the episode held more than once: two entries for one show (both
	// servers answer either with the other's episodes when they share their
	// ids), or one episode filed twice. The row speaks for the asked entry's
	// own copy, or the first, and the rest are named here rather than
	// dropped: which of two copies was read used to depend on the order the
	// server answered in, and the other went unseen
	OtherCopies []heldCopy `json:"other_copies,omitempty" jsonschema:"the episode's other files, when the library holds it more than once (a show split across two entries, or one episode filed twice): each by id and path, with its facts when quality was asked for, so which copy is the better is plain. The row itself answers for the asked entry's own copy, or the first"`
}

// heldCopy is another file an episode is held in.
type heldCopy struct {
	ID       string `json:"id"`
	SeriesID string `json:"series_id,omitempty" jsonschema:"the entry this copy belongs to"`
	Path     string `json:"path,omitempty"`
	// a version is no entry of its own: deleting the episode it is folded
	// into deletes it too
	VersionOf string `json:"version_of,omitempty" jsonschema:"set when this file is a version the server folds into the episode with this id (Jellyfin holds a second file of one episode in one folder that way): the two are one item, and deleting that item deletes both files"`
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
	Anime    []string         `json:"anime_entries,omitempty"     jsonschema:"other library entries sharing this show's provider id under another AniDB id, by id: an OVA, a film or a season AniDB counts as a show of its own - not this show, but an episode the provider numbers into it may be filed there, so an absence here is not proof: ask these too"`
	Warning  string           `json:"warning,omitempty"           jsonschema:"set when the library holds this show under more than one entry, or holds an anime entry sharing its provider id (anime_entries), and something was absent, or when whether it does could not be checked. The episodes may be split across entries, so an absence here is NOT proof the library lacks the episode - ask the other entry too"`
	Error    string           `json:"error,omitempty"             jsonschema:"why this series could not be answered for: nothing matched the name, or more than one thing did. Episodes is then empty and absent is 0 - which is NOT the same as the library holding none of them. The rest of the batch is answered regardless"`
}

// existsBatchMax is how many series one call will answer for. A batch is one
// server read per season per series, so an unbounded one is a request that
// never returns; a refusal that names the limit is a caller that pages.
const existsBatchMax = 50

// episodesHeld reads the episodes a series holds, keyed by season and
// episode number, every copy of each in the order the server gave them. A
// file holding several (S01E01E02) is keyed under each of them, because each
// of those episodes is one the library has. A file the server holds no
// season or episode number for is keyed under none, and comes back in
// unnumbered: it may be any of the episodes asked after. whole says the
// reads covered every season, and so every unnumbered file.
func episodesHeld(ctx context.Context, client *embyfin.Client, seriesID string, seasons []int, fields string) (held map[[2]int][]*embyfin.Item, unnumbered []*embyfin.Item, whole bool, err error) {
	// one read per season is cheaper than the whole series until enough
	// seasons are asked about that the whole series is the cheaper read
	queries := seasons
	if len(seasons) > 3 {
		queries = []int{0}
	}

	held = map[[2]int][]*embyfin.Item{}
	seen := map[string]bool{}
	for _, season := range queries {
		opts := embyfin.EpisodeOptions{Fields: fields + "," + embyfin.FieldVersionCount}
		if len(queries) > 1 || season > 0 {
			opts.Season = &season
		} else {
			whole = true
		}
		episodes, err := client.Episodes(ctx, seriesID, opts)
		if err != nil {
			return nil, nil, false, err
		}
		if err := client.WithVersionFiles(ctx, episodes); err != nil {
			return nil, nil, false, err
		}
		for i := range episodes {
			e := &episodes[i]
			if !numbered(e) {
				if e.HasFile() && !seen[e.ID] {
					unnumbered = append(unnumbered, e)
				}
				seen[e.ID] = true

				continue
			}
			for _, n := range episodeSpan(e) {
				key := [2]int{*e.ParentIndexNumber, n}
				held[key] = append(held[key], e)
			}
		}
	}

	return held, unnumbered, whole, nil
}

// unnumberedFiles are the files of a series the server holds no season or
// episode number for, read across the whole series.
func unnumberedFiles(ctx context.Context, client *embyfin.Client, seriesID string) ([]*embyfin.Item, error) {
	episodes, err := client.Episodes(ctx, seriesID, embyfin.EpisodeOptions{Fields: "Path"})
	if err != nil {
		return nil, err
	}
	var out []*embyfin.Item
	for i := range episodes {
		if !numbered(&episodes[i]) && episodes[i].HasFile() {
			out = append(out, &episodes[i])
		}
	}

	return out, nil
}

// unnumberedShown is how many of a show's unnumbered files an answer names:
// a daily show held without numbers is thousands of them, and the count says
// the rest.
const unnumberedShown = 10

// someOf is the first unnumberedShown paths, and how many more there are.
func someOf(paths []string) string {
	if len(paths) <= unnumberedShown {
		return strings.Join(paths, ", ")
	}

	return fmt.Sprintf("%s and %d more", strings.Join(paths[:unnumberedShown], ", "), len(paths)-unnumberedShown)
}

// pathsOf is the paths of items.
func pathsOf(items []*embyfin.Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Path)
	}

	return out
}

// unnumberedWarning is what an absence is worth beside files the server
// holds no numbers for: any of them may be the episode.
func unnumberedWarning(files []*embyfin.Item) string {
	return fmt.Sprintf("the show also holds %d file(s) the server has no season or episode number for (%s): any of them may be an episode read as absent here, so an absence is not proof the library lacks it", len(files), someOf(pathsOf(files)))
}

// joinWarnings puts two warnings in one field.
func joinWarnings(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}

	return a + "; " + b
}

// otherFiles are the copies an episode is held in beyond the one a row
// answers for: every other entry's episode, and every version folded into
// either, each file once.
func otherFiles(answer *embyfin.Item, others []*embyfin.Item, quality bool, keep map[string]bool) []heldCopy {
	var out []heldCopy
	add := func(c heldCopy, src *embyfin.MediaSource) {
		if quality {
			if src != nil {
				c.qualityFacts = sourceQuality(src)
			}
			if keep != nil {
				if !keep["path"] {
					c.Path = ""
				}
				c.keepOnly(keep)
			}
		}
		out = append(out, c)
	}
	folded := func(e *embyfin.Item) {
		if len(e.MediaSources) < 2 {
			return
		}
		own := e.OwnSource()
		for i := range e.MediaSources {
			if src := &e.MediaSources[i]; src != own {
				add(heldCopy{ID: src.ItemID, SeriesID: e.SeriesID, Path: src.Path, VersionOf: e.ID}, src)
			}
		}
	}
	folded(answer)
	for _, o := range others {
		add(heldCopy{ID: o.ID, SeriesID: o.SeriesID, Path: o.Path}, o.OwnSource())
		folded(o)
	}

	return out
}

// answeringCopy picks the copy of an episode a row answers for, and the
// others: one with a file before a record of one without, the asked entry's
// own before another entry's, and otherwise the first the server gave. A
// record is no copy, so it is not listed beside a file.
func answeringCopy(copies []*embyfin.Item, seriesID string) (answer *embyfin.Item, others []*embyfin.Item) {
	rank := func(e *embyfin.Item) int {
		r := 0
		if !e.HasFile() {
			r += 2
		}
		if e.SeriesID != "" && e.SeriesID != seriesID {
			r++
		}

		return r
	}
	best := 0
	for i := range copies {
		if rank(copies[i]) < rank(copies[best]) {
			best = i
		}
	}
	answer = copies[best]
	for i, e := range copies {
		if i != best && e.ID != answer.ID && (e.HasFile() || !answer.HasFile()) {
			others = append(others, e)
		}
	}

	return answer, others
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

	fields := "Path,DateCreated,DateModified"
	if quality && needsMediaSources(keep) {
		fields = "Path,MediaSources,DateCreated,DateModified"
	}
	held, unnumbered, whole, err := episodesHeld(ctx, r.client, series.ID, seasons, fields)
	if err != nil {
		return existsGroup{Series: series.Name, SeriesID: series.ID, Episodes: []existsRow{}, Error: err.Error()}, err
	}

	out := existsGroup{Series: series.Name, SeriesID: series.ID, Match: match, Episodes: []existsRow{}}
	for _, p := range q.Episodes {
		row := existsRow{Season: p.Season, Episode: p.Episode}
		if copies, ok := held[[2]int{p.Season, p.Episode}]; ok {
			e, others := answeringCopy(copies, series.ID)
			row.Known, row.ID, row.Title, row.Path = true, e.ID, e.Name, e.Path
			row.Exists = e.HasFile()
			if e.IndexNumberEnd > *e.IndexNumber {
				row.File = runCode(e)
			}
			if quality && row.Exists {
				row.RuntimeS = int(e.RunTimeTicks / ticksPerSecond)
				// the facts of the file the row names, not of the best of
				// its versions: the others are named beside it
				row.qualityFacts = pathQuality(e)
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
			if e.HasFile() {
				row.OtherCopies = otherFiles(e, others, quality, keep)
			}
		}
		if !row.Exists {
			out.Absent++
		}
		out.Episodes = append(out.Episodes, row)
	}

	// a file the server holds no numbers for may be any episode read as
	// absent: say so, reading the whole show for them when the reads above
	// covered only some of its seasons
	if out.Absent > 0 {
		if !whole {
			if unnumbered, err = unnumberedFiles(ctx, r.client, series.ID); err != nil {
				return existsGroup{Series: series.Name, SeriesID: series.ID, Episodes: []existsRow{}, Error: err.Error()}, err
			}
		}
		if len(unnumbered) > 0 {
			out.Warning = unnumberedWarning(unnumbered)
		}
	}

	// a show held twice is worth saying whether or not this call found a gap:
	// the episodes may be split across the entries, and where they are not,
	// the two entries are often the same episodes at different quality, so
	// the one asked may not be the one to compare against
	others, anime, oerr := r.otherEntriesFor(ctx, series)
	if oerr != nil {
		// not checked is not "held once": say so, or an absence reads as proof
		out.Warning = joinWarnings(fmt.Sprintf("whether the library holds %q under another entry could not be checked (the library's series could not be read: %v), so an absence above is not proof the library lacks the episode", series.Name, oerr), out.Warning)
	}
	where := func(list []embyfin.Item) string {
		at := make([]string, 0, len(list))
		for i := range list {
			it := &list[i]
			at = append(at, fmt.Sprintf("id %s at %s", it.ID, it.Path))
		}

		return strings.Join(at, "; ")
	}
	var warnings []string
	for i := range others {
		it := &others[i]
		out.Others = append(out.Others, it.ID)
	}
	if len(others) > 0 && out.Absent > 0 {
		warnings = append(warnings, fmt.Sprintf("the library holds %q under %d entries and the episodes are split across them (also %s): an absence above is not proof the library lacks the episode - ask the other entry as well, and audit_duplicates lists every show in this state",
			series.Name, len(others)+1, where(others)))
	}
	// an anime entry kept apart by its AniDB id is not this show, but the
	// provider's numbering may put one of this show's episodes in it
	for i := range anime {
		it := &anime[i]
		out.Anime = append(out.Anime, it.ID)
	}
	if len(anime) > 0 && out.Absent > 0 {
		entries := fmt.Sprintf("%d entries", len(anime))
		if len(anime) == 1 {
			entries = "1 entry"
		}
		warnings = append(warnings, fmt.Sprintf("the library also holds %s sharing %q's provider id under another AniDB id (%s): an OVA, a film or a season AniDB counts as a show of its own, and an episode the provider numbers into this show may be filed there, so an absence above is not proof - ask it as well",
			entries, series.Name, where(anime)))
	}
	if len(warnings) > 0 {
		// beside what was said already of the files with no numbers
		out.Warning = joinWarnings(strings.Join(warnings, "; and "), out.Warning)
	}

	return out, nil
}

// entriesWarning is what library_episodes says about the show it read: the
// show's other entries, which hold episodes this read of one entry does not,
// and, when one season was asked for, the files the show holds with no
// season number, which no season's list includes.
func entriesWarning(ctx context.Context, r *registry, series *embyfin.Item, season *int) (others []string, warning string, err error) {
	entries, _, oerr := r.otherEntriesFor(ctx, series)
	switch {
	case oerr != nil:
		warning = fmt.Sprintf("whether the library holds %q under another entry could not be checked (the library's series could not be read: %v), so these may not be all of its episodes", series.Name, oerr)
	case len(entries) > 0:
		where := make([]string, 0, len(entries))
		for i := range entries {
			it := &entries[i]
			others = append(others, it.ID)
			where = append(where, fmt.Sprintf("id %s at %s", it.ID, it.Path))
		}
		warning = fmt.Sprintf("the library holds %q under %d entries (also %s) and this is the asked entry's episodes alone: the show's others may be held under the rest, so read them too before calling an episode missing. show_episodes_exist and show_missing read every entry together", series.Name, len(entries)+1, strings.Join(where, "; "))
	}
	if season == nil {
		return others, warning, nil
	}
	unnumbered, err := unnumberedFiles(ctx, r.client, series.ID)
	if err != nil {
		return nil, "", err
	}
	if len(unnumbered) > 0 {
		warning = joinWarnings(warning, fmt.Sprintf("the show also holds %d file(s) the server has no season or episode number for (%s), which no season's list includes: read the show without season to see them", len(unnumbered), someOf(pathsOf(unnumbered))))
	}

	return others, warning, nil
}

func registerEpisodeTools(r *registry) {
	client := r.client

	type exportIn struct {
		Series     string   `json:"series,omitempty"      jsonschema:"the show by name or id - every episode of it; with season, that season only"`
		SeriesID   string   `json:"series_id,omitempty"   jsonschema:"the show by id alone"`
		Season     *int     `json:"season,omitempty"      jsonschema:"one season of the show, 0 for the specials; needs series or series_id"`
		Library    string   `json:"library,omitempty"     jsonschema:"every episode in one library, by name or id; with series, narrows a name lookup to that library. Default every library"`
		Quality    *bool    `json:"quality,omitempty"     jsonschema:"the facts and the path on each row; default true"`
		Fields     []string `json:"fields,omitempty"      jsonschema:"only these facts on each row: path, date_created, file_modified, runtime_s, container, size, bitrate, width, height, aspect_ratio, display_width, video_codec, frame_rate, hdr, audio, subtitles"`
		WithFile   *bool    `json:"with_file,omitempty"   jsonschema:"only episodes with a file; default true"`
		Limit      int      `json:"limit,omitempty"       jsonschema:"page size, default 500, max 1000"`
		SavedSince string   `json:"saved_since,omitempty" jsonschema:"only items the server last SAVED at or after this time: a date such as 2026-01-02, or a time such as 2026-01-02T15:04:05Z. The closest either server offers to 'what changed': a file written over an existing path is re-read and saved, but so is an item somebody edited, so it is a net rather than a measurement. Neither server can sort by it"`
		Offset     int      `json:"offset,omitempty"      jsonschema:"skip this many episodes, to page: the next page starts at offset + limit"`
	}
	type exportOut struct {
		Series   string           `json:"series,omitempty"    jsonschema:"the show, when the call was scoped to one"`
		SeriesID string           `json:"series_id,omitempty"`
		Match    *seriesCandidate `json:"matched,omitempty"   jsonschema:"how the show's name was matched, when it was given by name: the score, what matched, and the runner-up"`
		Total    int              `json:"total"               jsonschema:"episodes matching across every page: each episode once however many files it is held in on Jellyfin, which folds a second file of one episode in one folder into it (versions lists them); each file on Emby, which holds every file as an episode of its own"`
		Offset   int              `json:"offset"              jsonschema:"where this page starts"`
		Episodes []episodeRow     `json:"episodes"            jsonschema:"ordered by series, then season, then episode, so pages line up across calls"`
		// both servers answer show_episodes_exist and show_missing with
		// every entry sharing the show's ids, and an item read by its parent
		// with this entry's alone: half a split show read as the whole
		Others  []string `json:"duplicate_entries,omitempty" jsonschema:"other library entries for this same show, by id, when the call was scoped to one: this answer is the asked entry's episodes alone, and the show's other episodes may be held under these"`
		Warning string   `json:"warning,omitempty"           jsonschema:"set when the show is held under more than one entry, or whether it is could not be checked, and when a season was asked for and the show holds files the server has no season number for, which no season's list includes: this page is then not every episode of the show, or of the season"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "library_episodes",
		Description: "Every episode of one show - or one season of it (season) - by name or id, or every episode in a library, with each file's quality facts: resolution, codec, frame rate, HDR, bitrate, size, runtime and audio tracks. " +
			"A show's name is matched the way show_episodes_exist matches it: a guess or a tie is refused, naming the candidates. Paged by offset (the page walks the query, so a page whose rows were all filtered out still has pages after it); the bulk read for comparing a folder against the library. " +
			"An episode held in more than one file lists every file in versions (Jellyfin folds a second file of one episode in one folder into it; Emby lists each file as an episode of its own), and a row's facts are the file its path names. A show held under more than one entry is read for the asked entry alone, and duplicate_entries names the rest.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in exportIn) (*mcp.CallToolResult, exportOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = episodePage
		}
		limit = min(limit, episodePageMax)

		offset := max(in.Offset, 0)
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
			SavedSince:        in.SavedSince,
			SortOrder:         "Ascending",
			Limit:             limit,
			StartIndex:        offset,
			Fields:            "Path,MediaSources,DateCreated,DateModified",
		}
		if !quality || !needsMediaSources(keep) {
			// how many files an item is held in, so the paths of the ones
			// folded into it are read back even when no fact is asked for
			opts.Fields = "Path,DateCreated,DateModified," + embyfin.FieldVersionCount
		}

		var series *embyfin.Item
		var match *seriesCandidate
		switch {
		case in.Series != "" && in.SeriesID != "":
			return nil, exportOut{}, errors.New("give series or series_id, not both")
		case in.Series != "":
			if series, match, err = resolveSeriesRef(ctx, r, in.Series, in.Library); err != nil {
				return nil, exportOut{}, err
			}
			opts.ParentID = series.ID
		case in.SeriesID != "":
			if in.Library != "" {
				return nil, exportOut{}, errors.New("give library or series_id, not both: a series is already in one library")
			}
			if series, err = seriesByID(ctx, client, in.SeriesID); err != nil {
				return nil, exportOut{}, err
			}
			opts.ParentID = series.ID
		case in.Season != nil:
			return nil, exportOut{}, errors.New("season needs series or series_id: a season number means nothing across a library")
		default:
			folder, ferr := client.ResolveLibrary(ctx, in.Library)
			if ferr != nil {
				return nil, exportOut{}, ferr
			}
			if folder != nil {
				opts.ParentID = folder.ItemID
			}
		}

		items, total, serr := client.Search(ctx, opts)
		if serr != nil {
			return nil, exportOut{}, serr
		}
		if quality {
			if verr := client.WithVersionFiles(ctx, items); verr != nil {
				return nil, exportOut{}, verr
			}
		}

		out := exportOut{Total: total, Offset: offset, Episodes: []episodeRow{}}
		if series != nil {
			out.Series, out.SeriesID, out.Match = series.Name, series.ID, match
			if out.Others, out.Warning, err = entriesWarning(ctx, r, series, in.Season); err != nil {
				return nil, exportOut{}, err
			}
		}
		for i := range items {
			it := &items[i]
			if in.Season != nil && (it.ParentIndexNumber == nil || *it.ParentIndexNumber != *in.Season) {
				continue
			}
			if withFile && !it.HasFile() {
				continue
			}
			out.Episodes = append(out.Episodes, episodeFacts(it, quality, keep))
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
		Fields   []string      `json:"fields,omitempty"    jsonschema:"only these facts on each hit: path, runtime_s, container, size, bitrate, width, height, aspect_ratio, display_width, video_codec, frame_rate, hdr, audio, subtitles. Implies quality"`
	}
	type existsOut struct {
		Series   string           `json:"series,omitempty"            jsonschema:"the series asked after, for a single-series call"`
		SeriesID string           `json:"series_id,omitempty"`
		Episodes []existsRow      `json:"episodes,omitempty"          jsonschema:"one row per pair asked for, in the order asked; a single-series call answers here"`
		Absent   int              `json:"absent"                      jsonschema:"how many of the episodes asked after the library holds no file for, across the whole call"`
		Match    *seriesCandidate `json:"matched,omitempty"           jsonschema:"how the series name was matched, when one was given: the score, what matched, and the runner-up. Below about 0.9 is a guess a caller should stop on"`
		Others   []string         `json:"duplicate_entries,omitempty" jsonschema:"other library entries for this same show, by id: the episodes may be split across them"`
		Anime    []string         `json:"anime_entries,omitempty"     jsonschema:"other library entries sharing this show's provider id under another AniDB id, by id: an OVA, a film or a season AniDB counts as a show of its own - not this show, but an episode the provider numbers into it may be filed there"`
		Warning  string           `json:"warning,omitempty"           jsonschema:"set when the library holds this show under more than one entry, or an anime entry sharing its provider id, or when that could not be checked, so an absence is not proof the library lacks the episode"`
		Results  []existsGroup    `json:"results,omitempty"           jsonschema:"one group per entry in queries, in the order asked; a batch answers here"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "show_episodes_exist",
		Description: "Does the library hold these episodes? Give series_id or series with episodes, or up to 50 series in queries. A multi-episode file counts for each episode. quality or fields add the held copy's facts to hits. " +
			"A group with an error could not be looked up, which is not the same as absent. A name match below 0.9 in matched is a guess. duplicate_entries means the show is split across library entries, so an absence may be held by another; anime_entries are entries sharing its provider id under another AniDB id (an OVA, a film), not the show but where one of its episodes may be filed. An episode held in more than one file (two entries sharing the show's ids, which both servers answer as one show, or an episode filed twice) names the rest in other_copies.",
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
				out.Match, out.Others, out.Anime, out.Warning = group.Match, group.Others, group.Anime, group.Warning

				continue
			}
			out.Results = append(out.Results, group)
		}

		return nil, out, nil
	})
}
