package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The audits over what is on disk rather than what the metadata says: the
// quality of the files, the episodes a season is missing between the ones it
// has, and what nobody has watched.

// legacyCodecs are the video codecs worth replacing: what DVD rips and early
// downloads were made with, by the names ffprobe gives them.
var legacyCodecs = []string{"mpeg1video", "mpeg2video", "mpeg4", "msmpeg4v1", "msmpeg4v2", "msmpeg4v3", "wmv1", "wmv2", "wmv3", "vc1", "h263", "flv1", "vp6", "vp6f", "theora", "rv30", "rv40", "cinepak", "indeo5"}

// qualityIn is audit_quality's input.
type qualityIn struct {
	Library    string `json:"library,omitempty"       jsonschema:"restrict to one library by name or id"`
	Types      string `json:"types,omitempty"         jsonschema:"comma-separated item types; default Movie,Episode"`
	MinHeight  int    `json:"min_height,omitempty"    jsonschema:"flag a picture below this class, default 720 (so 480p and 576p rips); a widescreen 1280x536 is 720p, judged by its width"`
	MinBitrate int64  `json:"min_bitrate,omitempty"   jsonschema:"also flag video below this bitrate, in bits per second like every bitrate a tool answers with; off unless given"`
	Codecs     *bool  `json:"legacy_codecs,omitempty" jsonschema:"flag legacy video codecs (MPEG-2, MPEG-4 part 2 such as XviD and DivX, WMV, VC-1, RealVideo...); default true"`
	Limit      int    `json:"limit,omitempty"         jsonschema:"maximum rows in each list, default 100"`
}

// videoOf is a file's primary video stream, or nil.
func videoOf(src *embyfin.MediaSource) *embyfin.MediaStream {
	for i := range src.MediaStreams {
		if src.MediaStreams[i].Type == "Video" {
			return &src.MediaStreams[i]
		}
	}

	return nil
}

// checkQuality judges an item by its best file: a 4K copy beside a 480p one is
// not a worklist entry. It returns the finding and the best picture's class
// (resolutionClass: a 2.39:1 encode at 1280x536 is 720p, not 536p), which
// orders the worklist worst first.
func checkQuality(it *embyfin.Item, in qualityIn) (detail string, height int, bad bool) {
	var best *embyfin.MediaStream
	bitrate := int64(0)
	for i := range it.MediaSources {
		v := videoOf(&it.MediaSources[i])
		if v == nil {
			continue
		}
		if best == nil || resolutionClass(v.Width, v.Height) > resolutionClass(best.Width, best.Height) {
			best, bitrate = v, v.BitRate
			if bitrate == 0 {
				bitrate = it.MediaSources[i].Bitrate
			}
		}
	}
	if best == nil {
		return "", 0, false // no video to judge: audio, or never probed
	}

	var problems []string
	class := resolutionClass(best.Width, best.Height)
	if in.MinHeight > 0 && class > 0 && class < in.MinHeight {
		problems = append(problems, fmt.Sprintf("%dp, below %dp", class, in.MinHeight))
	}
	if (in.Codecs == nil || *in.Codecs) && slices.Contains(legacyCodecs, strings.ToLower(best.Codec)) {
		problems = append(problems, "legacy codec "+best.Codec)
	}
	if in.MinBitrate > 0 && bitrate > 0 && bitrate < in.MinBitrate {
		problems = append(problems, fmt.Sprintf("%d kbps, below %d kbps", bitrate/1000, in.MinBitrate/1000))
	}
	if len(problems) == 0 {
		return "", class, false
	}

	return fmt.Sprintf("%s %dx%d: %s", best.Codec, best.Width, best.Height, strings.Join(problems, "; ")), class, true
}

// qualityDefaults fills audit_quality's defaults: 720 lines, legacy codecs
// flagged, no bitrate floor.
func qualityDefaults(in qualityIn) qualityIn {
	if in.MinHeight <= 0 {
		in.MinHeight = 720
	}
	if in.Limit <= 0 {
		in.Limit = 100
	}

	return in
}

// qualityOut is audit_quality's answer: the files worth replacing, and the
// files whose facts cannot be trusted to judge.
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
type qualityOut struct {
	auditOut
	TotalUnprobed int           `json:"total_unprobed"`
	TotalReplaced int           `json:"total_replaced"`
	Unprobed      []unprobedRow `json:"unprobed"       jsonschema:"files the server holds no media facts for: never probed, so nothing about their picture or sound could be judged and they are not among the findings; capped at limit"`
	Replaced      []unprobedRow `json:"replaced"       jsonschema:"files written after the server first saw them (Emby says when a file was last written; Jellyfin does not): the facts may be the old file's until a scan re-reads it, which the size tells. Judged as they stand; capped at limit"`
	Note          string        `json:"note,omitempty"`
}

// unprobedRow is a file whose facts are not the file's own.
type unprobedRow struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path,omitempty"`
	Size         int64  `json:"size,omitempty"          jsonschema:"the file size the server believes, in bytes; compare it with the file's to tell whether the server has re-read a replaced file"`
	DateCreated  string `json:"date_created,omitempty"  jsonschema:"when the server first saw the file"`
	FileModified string `json:"file_modified,omitempty" jsonschema:"when the file was last written, as the server read it (Emby only)"`
	Detail       string `json:"detail"`
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

func auditQuality(ctx context.Context, client *embyfin.Client, in qualityIn) (qualityOut, error) {
	in = qualityDefaults(in)
	opts, err := sweepOptions(ctx, client, in.Library, in.Types, "Movie,Episode", "Path,ProductionYear,MediaSources,DateCreated,DateModified")
	if err != nil {
		return qualityOut{}, err
	}

	type scored struct {
		finding auditFinding
		height  int
	}
	var findings []scored
	var unprobed, replaced []unprobedRow
	out := qualityOut{Findings: []auditFinding{}, Unprobed: []unprobedRow{}, Replaced: []unprobedRow{}}
	if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			out.Scanned++
			if it.HasFile() {
				row := unprobedRow{ID: it.ID, Name: episodeOrItemName(it), Path: it.Path, DateCreated: it.DateCreated, FileModified: it.DateModified}
				if len(it.MediaSources) > 0 {
					row.Size = it.MediaSources[0].Size
				}
				switch {
				case !probed(it):
					// no picture to judge, and left out in silence it would
					// read as fine
					row.Detail = "no media facts: the server has never probed the file, so nothing about its picture or sound is known"
					unprobed = append(unprobed, row)

					continue
				case it.DateModified != "" && it.DateCreated != "" && it.DateModified > it.DateCreated:
					row.Detail = fmt.Sprintf("the file was written on %s, after the server first saw it on %s: its facts are the earlier file's until a scan re-reads it", dateOf(it.DateModified), dateOf(it.DateCreated))
					replaced = append(replaced, row)
				}
			}
			detail, height, bad := checkQuality(it, in)
			if !bad {
				continue
			}
			findings = append(findings, scored{height: height, finding: auditFinding{ID: it.ID, Name: episodeOrItemName(it), Year: it.ProductionYear, Path: it.Path, Detail: detail}})
		}

		return true
	}); err != nil {
		return qualityOut{}, err
	}

	for _, list := range []*[]unprobedRow{&unprobed, &replaced} {
		slices.SortFunc(*list, func(a, b unprobedRow) int { return strings.Compare(a.Path, b.Path) })
	}
	out.TotalUnprobed, out.TotalReplaced = len(unprobed), len(replaced)
	out.Unprobed = append(out.Unprobed, unprobed[:min(len(unprobed), in.Limit)]...)
	out.Replaced = append(out.Replaced, replaced[:min(len(replaced), in.Limit)]...)
	if client.Backend() == embyfin.Jellyfin {
		out.Note = "Jellyfin does not say when a file was last written, so replaced files cannot be told apart here; compare sizes against the files"
	}

	// the lowest resolution first, so a capped worklist starts with the worst
	slices.SortStableFunc(findings, func(a, b scored) int {
		if c := cmp.Compare(a.height, b.height); c != 0 {
			return c
		}
		return strings.Compare(a.finding.Name, b.finding.Name)
	})
	out.Found = len(findings)
	for _, f := range findings[:min(len(findings), in.Limit)] {
		out.Findings = append(out.Findings, f.finding)
	}

	return out, nil
}

// gap is one thing a series' files leave out: a whole season when Episode is
// zero, otherwise one episode of a season.
type gap struct {
	Season  int
	Episode int
}

// gapsOnDisk lists what a series' files leave out: the episode numbers
// missing between the lowest and highest a season holds, and the seasons
// missing between the lowest and highest the series holds. Specials (season
// 0) have no order to have gaps in. Nothing past the last episode on disk is
// knowable from the files, which is why the gaps are never the whole answer
// to what a series is missing: only a metadata provider knows the rest.
func gapsOnDisk(episodes map[int][]int) []gap {
	seasons := make([]int, 0, len(episodes))
	for s := range episodes {
		if s > 0 {
			seasons = append(seasons, s)
		}
	}
	slices.Sort(seasons)

	var gaps []gap
	for i, s := range seasons {
		if i > 0 {
			for missing := seasons[i-1] + 1; missing < s; missing++ {
				gaps = append(gaps, gap{Season: missing})
			}
		}
		eps := slices.Clone(episodes[s])
		slices.Sort(eps)
		eps = slices.Compact(eps)
		for j := 1; j < len(eps); j++ {
			for missing := eps[j-1] + 1; missing < eps[j]; missing++ {
				gaps = append(gaps, gap{Season: s, Episode: missing})
			}
		}
	}

	return gaps
}

// seasonGaps spells gapsOnDisk the way an audit's detail line reads.
func seasonGaps(episodes map[int][]int) []string {
	holes := gapsOnDisk(episodes)
	if len(holes) == 0 {
		return nil
	}
	out := make([]string, 0, len(holes))
	for _, h := range holes {
		if h.Episode == 0 {
			out = append(out, fmt.Sprintf("season %d", h.Season))
			continue
		}
		out = append(out, fmt.Sprintf("S%02dE%02d", h.Season, h.Episode))
	}

	return out
}

type episodesIn struct {
	Library    string `json:"library,omitempty"     jsonschema:"restrict to one library by name or id"`
	Limit      int    `json:"limit,omitempty"       jsonschema:"maximum series to return, default 100"`
	Provider   bool   `json:"provider,omitempty"    jsonschema:"also read each series' whole run from the configured metadata providers (TMDB, by the series' tmdb id or the id TMDB knows it by from its tvdb or imdb one) and report the aired episodes it lists that have no file, which the gaps between files cannot see. One request a series, so paged: max_lookups and offset, and the findings are the series asked about in this call"`
	MaxLookups int    `json:"max_lookups,omitempty" jsonschema:"provider: series to ask about in this call, default 250"`
	Offset     int    `json:"offset,omitempty"      jsonschema:"provider: series to skip, from a previous call's next_offset"`
}

// unknownRun is a series the provider could not say the run of.
type unknownRun struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// missingEpisodesOut is the missing-episode sweep's worklist, with the field
// that says how much of it could be known. Without it a library whose server
// keeps no record of a series' run reads as a library with nothing missing.
type missingEpisodesOut struct {
	auditOut
	RunsKnown    bool         `json:"runs_known"              jsonschema:"whether any series' full run could be read, from the server's own records or, with provider, from the metadata provider. False means the findings are only the episode numbers skipped between the files on disk: a series absent from them is NOT known to be complete"`
	Note         string       `json:"note,omitempty"`
	TotalUnknown int          `json:"total_unknown,omitempty" jsonschema:"provider: series whose run could not be read"`
	Unknown      []unknownRun `json:"unknown,omitempty"       jsonschema:"provider: series whose run could not be read, with why (no id a provider knows it by, or the provider could not be asked); capped at limit"`
	NextOffset   int          `json:"next_offset,omitempty"   jsonschema:"provider: pass back as offset to go on; absent when every series was asked about"`
}

// guideMissing is what the provider's run lists past what a series holds,
// aired episodes only, spelled for a finding: the first few and a count.
func guideMissing(run []tmdb.Episode, held map[[2]int]bool) string {
	now := time.Now()
	var missing []string
	for _, e := range run {
		if held[[2]int{e.Season, e.Episode}] || !aired(e, now) {
			continue
		}
		missing = append(missing, fmt.Sprintf("S%02dE%02d", e.Season, e.Episode))
	}
	if len(missing) == 0 {
		return ""
	}
	const shown = 12
	if len(missing) > shown {
		return fmt.Sprintf("%s and %d more", strings.Join(missing[:shown], ", "), len(missing)-shown)
	}

	return strings.Join(missing, ", ")
}

// recordAired says whether an episode the server keeps a record of, with no
// file, has aired: by the rule the provider's run is read by (aired), so an
// episode announced for next month, or announced with no date at all, is
// not reported missing by one source and left out by the other.
func recordAired(it *embyfin.Item, now time.Time) bool {
	day, _, _ := strings.Cut(it.PremiereDate, "T")

	return aired(tmdb.Episode{AirDate: day}, now)
}

func auditMissingEpisodes(ctx context.Context, client *embyfin.Client, guide seriesGuide, in episodesIn) (missingEpisodesOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	if in.Provider && guide == nil {
		return missingEpisodesOut{}, errors.New("reading the runs from the metadata provider needs a TMDB token: set EMBYFIN_TMDB_TOKEN (or --tmdb-token) and restart")
	}
	maxLookups := in.MaxLookups
	if maxLookups <= 0 {
		maxLookups = defaultTMDBLookups
	}
	opts, err := sweepOptions(ctx, client, in.Library, "", "Episode", "Path,PremiereDate")
	if err != nil {
		return missingEpisodesOut{}, err
	}

	type series struct {
		name     string
		onDisk   map[int][]int
		held     map[[2]int]bool // every number a file covers, specials included
		records  bool            // the server keeps records of episodes it has no file for
		provider []string        // aired episodes the server lists without a file
		guide    string          // what the metadata provider lists without a file
	}
	bySeries := map[string]*series{}
	out := auditOut{Findings: []auditFinding{}}
	now := time.Now()
	if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			if it.SeriesID == "" {
				continue
			}
			s := bySeries[it.SeriesID]
			if s == nil {
				s = &series{name: it.SeriesName, onDisk: map[int][]int{}, held: map[[2]int]bool{}}
				bySeries[it.SeriesID] = s
			}
			if !it.HasFile() {
				// a record of an episode still to come is a run the server
				// knows, not an episode missing from it
				s.records = true
				if recordAired(it, now) {
					s.provider = append(s.provider, fmt.Sprintf("S%02dE%02d", it.ParentIndexNumber, it.IndexNumber))
				}

				continue
			}
			out.Scanned++
			// a file holding S01E01E02 is both, or E02 would be reported missing
			for n := it.IndexNumber; n > 0 && n <= max(it.IndexNumber, it.IndexNumberEnd); n++ {
				s.onDisk[it.ParentIndexNumber] = append(s.onDisk[it.ParentIndexNumber], n)
				s.held[[2]int{it.ParentIndexNumber, n}] = true
			}
		}
		return true
	}); err != nil {
		return missingEpisodesOut{}, err
	}

	runs := false
	for _, s := range bySeries {
		if s.records {
			runs = true
			break
		}
	}

	// by name, then by id: two series of one name (one show split across two
	// entries is the usual reason) otherwise swap places with the map's
	// order from one call to the next, and paged by offset one of them is
	// asked about twice and the other never
	ids := make([]string, 0, len(bySeries))
	for id := range bySeries {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		return cmp.Or(strings.Compare(bySeries[a].name, bySeries[b].name), strings.Compare(a, b))
	})

	answer := missingEpisodesOut{auditOut: out}
	if in.Provider {
		// the series themselves, for the ids a provider knows them by; the
		// window is the series asked about in this call
		from := min(max(in.Offset, 0), len(ids))
		to := min(from+maxLookups, len(ids))
		if to < len(ids) {
			answer.NextOffset = to
		}
		window := ids[from:to]
		items := map[string]*embyfin.Item{}
		for chunk := range slices.Chunk(window, 100) {
			if err := client.SearchAll(ctx, embyfin.SearchOptions{IDs: strings.Join(chunk, ","), IncludeItemTypes: "Series", Fields: "ProviderIds"}, func(rows []embyfin.Item) bool {
				for i := range rows {
					items[rows[i].ID] = &rows[i]
				}

				return true
			}); err != nil {
				return missingEpisodesOut{}, err
			}
		}
		answer.Unknown = []unknownRun{}
		for _, id := range window {
			s := bySeries[id]
			item := items[id]
			if item == nil {
				item = &embyfin.Item{ID: id, Name: s.name}
			}
			run, reason := guideRun(ctx, guide, item)
			if reason != "" {
				answer.TotalUnknown++
				if len(answer.Unknown) < limit {
					answer.Unknown = append(answer.Unknown, unknownRun{ID: id, Name: s.name, Reason: reason})
				}

				continue
			}
			runs = true
			s.guide = guideMissing(run, s.held)
		}
		ids = window
	}

	for _, id := range ids {
		s := bySeries[id]
		gaps := seasonGaps(s.onDisk)
		var parts []string
		if len(gaps) > 0 {
			parts = append(parts, "missing between the episodes on disk: "+strings.Join(gaps, ", "))
		}
		if len(s.provider) > 0 {
			slices.Sort(s.provider)
			parts = append(parts, "listed by the server's own records without a file: "+strings.Join(s.provider, ", "))
		}
		if s.guide != "" {
			parts = append(parts, "listed by TMDB without a file: "+s.guide)
		}
		if len(parts) == 0 {
			continue
		}
		answer.Found++
		if len(answer.Findings) < limit {
			answer.Findings = append(answer.Findings, auditFinding{ID: id, Name: s.name, Detail: strings.Join(parts, "; ")})
		}
	}

	answer.RunsKnown = runs
	if !runs {
		answer.Note = "the server keeps no record of an episode it has no file for (stock Jellyfin needs the TheTVDB plugin for those, and Emby 4.10 no longer imports them), so these findings are only what the files themselves show: the numbers skipped between them. A series not listed here is not known to be complete - provider: true reads every series' run from the metadata provider, and show_missing one series'."
	}

	return answer, nil
}

type unwatchedIn struct {
	Library   string `json:"library,omitempty"    jsonschema:"restrict to one library by name or id"`
	Types     string `json:"types,omitempty"      jsonschema:"Movie, Series or both, comma-separated; default Movie. A series counts as watched when anyone has watched any of its episodes"`
	AddedDays int    `json:"added_days,omitempty" jsonschema:"only items added at least this many days ago, so what just arrived is left out"`
	Limit     int    `json:"limit,omitempty"      jsonschema:"maximum findings to return, default 100"`
}

type unwatchedOut struct {
	auditOut
	Users []string `json:"users" jsonschema:"whose watch state was read: every account on the server, each in its own view, so what an account cannot see it has not watched"`
}

func registerMediaAudits(r *registry) {
	client := r.client
	var guide seriesGuide
	if facts := tmdbFacts(r.opts); facts != nil {
		guide = facts
	}

	add(r, readTool, &mcp.Tool{
		Name: "audit_quality",
		Description: "Find the films and episodes worth replacing with a better copy: video below a resolution (default 720 lines, so 480p and 576p rips), in a legacy codec (MPEG-2, XviD and DivX, WMV, VC-1...), or below a bitrate when one is given. An item is judged by its best file, so a 4K version beside a DVD rip is not reported. Lowest resolution first. " +
			"Two more lists say which facts cannot be trusted: files the server holds no media facts for (never probed, so resolution, codec, bitrate and audio all read as nothing rather than as a measurement, and they are not judged) and, on Emby, files written over after the server first saw them, whose facts may still be the old file's until a scan re-reads them. Each of those rows carries the size the server believes, so a caller with the file in front of it can tell a re-read from a stale one; a scan of the library re-probes both.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in qualityIn) (*mcp.CallToolResult, qualityOut, error) {
		out, err := auditQuality(ctx, client, in)
		return nil, out, err
	})

	add(r, readTool, &mcp.Tool{
		Name: "audit_missing_episodes",
		Description: "Find the series with episodes missing: the episode numbers a season skips between the ones on disk (E01 and E03 but no E02), whole seasons skipped between the ones on disk, and, when the server records them, the episodes its metadata provider lists that have aired and have no file (stock Jellyfin needs the TheTVDB plugin for those, and Emby 4.10 does not record them). " +
			"With provider true, each series' whole run is read from the configured metadata providers instead (TMDB, with EMBYFIN_TMDB_TOKEN set), one request a series and paged, so what a series lacks after its last file is seen too. " +
			"Read 'runs_known': when it is false this sweep can only see gaps between files, so a series it does not list is not known to be complete.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in episodesIn) (*mcp.CallToolResult, missingEpisodesOut, error) {
		out, err := auditMissingEpisodes(ctx, client, guide, in)
		return nil, out, err
	})

	add(r, readTool, &mcp.Tool{
		Name:        "audit_unwatched",
		Description: "Find what nobody has watched: the films (or series) no account on the server has played, oldest additions first, optionally only those added more than some days ago. A copy of a film watched anywhere on the server counts for every copy. What to archive or delete to free space, or what to recommend.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in unwatchedIn) (*mcp.CallToolResult, unwatchedOut, error) {
		out, err := auditUnwatched(ctx, client, in)

		return nil, out, err
	})
}

func auditUnwatched(ctx context.Context, client *embyfin.Client, in unwatchedIn) (unwatchedOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	types := map[string]bool{}
	for t := range strings.SplitSeq(cmp.Or(in.Types, typeMovie), ",") {
		switch t = strings.TrimSpace(t); t {
		case typeMovie, "Series":
			types[t] = true
		default:
			return unwatchedOut{}, fmt.Errorf("types must be Movie, Series or both, not %q", t)
		}
	}
	folder, err := resolveLibrary(ctx, client, in.Library)
	if err != nil {
		return unwatchedOut{}, err
	}
	parent := ""
	if folder != nil {
		parent = folder.ItemID
	}
	users, err := client.Users(ctx)
	if err != nil {
		return unwatchedOut{}, err
	}

	// what anyone has watched, by title (a copy watched in another library
	// counts for this one's) and across the server: films played, and the
	// series of any episode played. Not scoped to the library: Jellyfin
	// lists what a user played in a library they cannot see when the
	// library is named as the parent, where Emby leaves it out
	watched := titles{}
	series := map[string]bool{}
	out := unwatchedOut{
		Users:    []string{},
		Findings: []auditFinding{},
	}
	playedTypes := []string{}
	if types[typeMovie] {
		playedTypes = append(playedTypes, typeMovie)
	}
	if types["Series"] {
		playedTypes = append(playedTypes, "Episode")
	}
	for _, u := range users {
		out.Users = append(out.Users, u.Name)
		if err := client.SearchAll(ctx, embyfin.SearchOptions{
			IncludeItemTypes: strings.Join(playedTypes, ","), Filters: "IsPlayed", UserID: u.ID, EnableUserData: true, Fields: "Path,ProviderIds",
		}, func(items []embyfin.Item) bool {
			for i := range items {
				if items[i].Type == "Episode" {
					series[items[i].SeriesID] = true
					continue
				}
				watched.add(&items[i])
			}
			return true
		}); err != nil {
			return unwatchedOut{}, err
		}
	}
	delete(series, "")
	for ids := range slices.Chunk(slices.Collect(maps.Keys(series)), 100) {
		if err := client.SearchAll(ctx, embyfin.SearchOptions{IDs: strings.Join(ids, ","), IncludeItemTypes: "Series", Fields: "Path,ProviderIds"}, func(items []embyfin.Item) bool {
			for i := range items {
				watched.add(&items[i])
			}
			return true
		}); err != nil {
			return unwatchedOut{}, err
		}
	}

	kinds := make([]string, 0, len(types))
	for t := range types {
		kinds = append(kinds, t)
	}
	var findings []auditFinding
	if err := client.SearchAll(ctx, embyfin.SearchOptions{IncludeItemTypes: strings.Join(kinds, ","), ParentID: parent, Fields: embyfin.FieldsLean}, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			out.Scanned++
			if watched.has(it) {
				continue
			}
			if in.AddedDays > 0 && afterCutoff(it.DateCreated, daysCutoff(in.AddedDays)) {
				continue
			}
			findings = append(findings, auditFinding{ID: it.ID, Name: it.Name, Year: it.ProductionYear, Path: it.Path, Detail: "never watched, added " + dateOf(it.DateCreated)})
		}
		return true
	}); err != nil {
		return unwatchedOut{}, err
	}

	slices.SortStableFunc(findings, func(a, b auditFinding) int {
		if c := strings.Compare(a.Detail, b.Detail); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	out.Found = len(findings)
	out.Findings = append(out.Findings, findings[:min(len(findings), limit)]...)

	return out, nil
}

// dateOf is the date part of a server timestamp.
func dateOf(stamp string) string {
	if d, _, ok := strings.Cut(stamp, "T"); ok {
		return d
	}
	if stamp == "" {
		return "on an unknown date"
	}

	return stamp
}
