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
	Note          string        `json:"note,omitempty" jsonschema:"on Jellyfin, that replaced files cannot be told apart; and when the library was seen to change while it was read, that items added or removed meanwhile may be missing, or listed though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. It says nothing of a change when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read. On Emby, an audit of what people are shown also says how the items shown only as versions of others were placed: by the key Emby merges them by, with a sample checked against a read of each, or by a read of each"`
	// changed is what the read said of the library changing, apart from the
	// rest of the note: what audit_all reports
	changed string
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

// probed says whether the server has read the file: a stream it found in
// it. A size is not a probe: Jellyfin gives a file it could not read (the
// first few kilobytes of an episode, cut short in the copying) its size and
// no streams, and Emby gives it neither (seen on Jellyfin 12.1 and Emby
// 4.10). Nor is a subtitle file beside it, which a server lists as a stream
// of the item without opening the video.
func probed(it *embyfin.Item) bool {
	for i := range it.MediaSources {
		for _, st := range it.MediaSources[i].MediaStreams {
			if !st.IsExternal {
				return true
			}
		}
	}

	return false
}

// extraFolders are the folders both servers keep what goes with a film or a
// show in - its trailers, featurettes and deleted scenes - by the names they
// read them by.
var extraFolders = []string{"extras", "trailers", "featurettes", "behind the scenes", "deleted scenes", "interviews", "scenes", "samples", "shorts", "clips", "other", "backdrops"}

// extraEpisode says whether an episode is a file from an extras folder that the
// server took for an episode. Emby 4.10 reads a season's Extras folder so:
// the featurette in a season's Extras became an episode with no number, and
// the quality audit reported it as an episode worth replacing. The folder
// has to be the file's own and not named for the show, or a show called
// Extras, its episodes held in its own folder, would be passed over.
func extraEpisode(it *embyfin.Item) bool {
	if it.Type != typeEpisode || it.Path == "" {
		return false
	}
	folder := baseName(parentDir(it.Path))

	return slices.Contains(extraFolders, strings.ToLower(folder)) && folderKey(folder) != folderKey(it.SeriesName)
}

// episodeOrItemName names an item the way a worklist wants it: an episode
// by its series and number, anything else by its name.
func episodeOrItemName(it *embyfin.Item) string {
	if it.Type == typeEpisode && it.SeriesName != "" {
		return fmt.Sprintf("%s %s %s", it.SeriesName, episodeCode(it), it.Name)
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
	// judged as people are shown it, every version together, so a DVD rip
	// beside a 4K copy is not a worklist entry on Emby either, where each
	// version is stored as an item of its own; the facts that cannot be
	// trusted are the files', so they are listed file by file
	groups, read, placing, err := shownGroups(ctx, client, opts)
	if err != nil {
		return qualityOut{}, err
	}
	out.Note, out.changed = joinWarnings(read, placing), read
	for g := range groups {
		shown := &groups[g].Item
		if extraEpisode(shown) {
			continue // not an episode to replace, whatever its size
		}
		out.Scanned++
		for i := range groups[g].stored {
			it := &groups[g].stored[i]
			if !it.HasFile() {
				continue
			}
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
			case it.DateModified != "" && it.DateCreated != "" && it.DateModified > it.DateCreated:
				row.Detail = fmt.Sprintf("the file was written on %s, after the server first saw it on %s: its facts are the earlier file's until a scan re-reads it", dateOf(it.DateModified), dateOf(it.DateCreated))
				replaced = append(replaced, row)
			}
		}
		detail, height, bad := checkQuality(shown, in)
		if !bad {
			continue
		}
		findings = append(findings, scored{height: height, finding: auditFinding{ID: shown.ID, Name: episodeOrItemName(shown), Year: shown.ProductionYear, Path: shown.Path, Detail: detail}})
	}

	for _, list := range []*[]unprobedRow{&unprobed, &replaced} {
		slices.SortFunc(*list, func(a, b unprobedRow) int { return strings.Compare(a.Path, b.Path) })
	}
	out.TotalUnprobed, out.TotalReplaced = len(unprobed), len(replaced)
	out.Unprobed = append(out.Unprobed, unprobed[:min(len(unprobed), in.Limit)]...)
	out.Replaced = append(out.Replaced, replaced[:min(len(replaced), in.Limit)]...)
	if client.Backend() == embyfin.Jellyfin {
		out.Note = joinWarnings("Jellyfin does not say when a file was last written, so replaced files cannot be told apart here; compare sizes against the files", out.Note)
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

// joinedTitles is what to say of the gaps a file holding two episodes may
// fill, "" when nothing: a file titled "A & B" and named by the first of its
// numbers alone holds the next one too, and read by its number every second
// episode of a show so named was missing. It is said, not settled: the file's
// title is what the server holds, which may or may not be the file's.
func joinedTitles(gaps []gap, titles map[[2]int]string) string {
	var hints []string
	for _, h := range gaps {
		if h.Episode <= 1 {
			continue
		}
		if title := titles[[2]int{h.Season, h.Episode - 1}]; strings.Contains(title, "&") {
			hints = append(hints, fmt.Sprintf("S%02dE%02d after S%02dE%02d %q", h.Season, h.Episode, h.Season, h.Episode-1, title))
		}
	}
	if len(hints) == 0 {
		return ""
	}

	return "a file whose title joins two titles with '&' just before a missing number probably holds both episodes, named by its first number alone: " + firstFew(hints)
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

// unknownRun is a series whose whole run could not be read, and why.
type unknownRun struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// missingFinding is a show with episodes missing, and whether what it lists
// is all the show lacks.
type missingFinding struct {
	auditFinding
	RunKnown bool `json:"run_known" jsonschema:"whether this show's whole run was read, from the server's own records or, with provider, from TMDB. False means the detail is only the numbers skipped between its files: it may lack more after its last one"`
}

// otherOrder is a show holding episode numbers the run it was compared with
// has no episode for.
type otherOrder struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	NotInRun string `json:"not_in_run" jsonschema:"the episode numbers the show's files hold that TMDB's aired order has no episode for, the first few and a count"`
}

// missingEpisodesOut is the missing-episode sweep's worklist, with the fields
// that say how much of it could be known. Without them a library whose
// server keeps no record of a series' run reads as a library with nothing
// missing.
type missingEpisodesOut struct {
	Scanned      int              `json:"items_scanned"                      jsonschema:"episode files read"`
	Series       int              `json:"series"                             jsonschema:"shows judged: with provider, the shows asked about in this call. A show held under two entries sharing its ids is one"`
	Found        int              `json:"total_findings"`
	Findings     []missingFinding `json:"findings"                           jsonschema:"capped at limit; total_findings is the real count"`
	RunsKnown    bool             `json:"runs_known"                         jsonschema:"true only when every show judged had its whole run read (from the server's own records or, with provider, from TMDB), so a show not listed lacks no aired episode in that run's numbering. False when any show was judged only by the numbers skipped between its files - total_unknown says how many: a show not listed is then NOT known to be complete"`
	TotalUnknown int              `json:"total_unknown"                      jsonschema:"shows judged whose whole run could not be read, so only the gaps between their files were seen"`
	Unknown      []unknownRun     `json:"unknown,omitempty"                  jsonschema:"provider: the shows whose run could not be read, with why (no id a provider knows it by, ids that name different titles there - a film's ids on a show - or the provider could not be asked); capped at limit"`
	Order        string           `json:"order,omitempty"                    jsonschema:"provider: how the runs read from TMDB number their episodes. Files are compared with them number by number, so a show whose files are numbered another way (TVDB's order, the one Sonarr names files by, or a DVD's) can read as missing episodes it holds under other numbers: numbered_otherwise lists the shows whose files hold numbers the run has no episode for"`
	TotalOther   int              `json:"total_numbered_otherwise,omitempty" jsonschema:"provider: shows whose files hold episode numbers TMDB's aired order has no episode for"`
	OtherOrder   []otherOrder     `json:"numbered_otherwise,omitempty"       jsonschema:"provider: shows whose files hold episode numbers TMDB's aired order has no episode for - a sign the files are numbered in another order, or that TMDB does not list those episodes yet - so what is listed missing for them may be held under other numbers; capped at limit"`
	Note         string           `json:"note,omitempty"                     jsonschema:"what runs_known false leaves unsaid; and when the library was seen to change while it was read, that episodes added or removed meanwhile may be missing, or counted though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. It says nothing of a change when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read"`
	NextOffset   int              `json:"next_offset,omitempty"              jsonschema:"provider: pass back as offset to go on; absent when every series was asked about"`
	// changed is what the reads said of the library changing, apart from
	// the rest of the note: what audit_all reports
	changed string
}

// firstFew spells a list of episode codes for a finding: the first dozen and
// a count of the rest.
func firstFew(codes []string) string {
	const shown = 12
	if len(codes) > shown {
		return fmt.Sprintf("%s and %d more", strings.Join(codes[:shown], ", "), len(codes)-shown)
	}

	return strings.Join(codes, ", ")
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

	return firstFew(missing)
}

// notInRun is every episode number a series' files hold that the provider's
// run has no episode for, in order: a season it does not list, or an episode
// past the end of one. Held numbers the run does not have are the sign that
// the files are numbered another way than the run - TVDB's order, which
// Sonarr names files by, against TMDB's aired order - and then a comparison
// number by number is not to be trusted. The specials are no part of a run.
func notInRun(run []tmdb.Episode, held map[[2]int]bool) []missingRow {
	listed := make(map[[2]int]bool, len(run))
	for _, e := range run {
		listed[[2]int{e.Season, e.Episode}] = true
	}
	var out []missingRow
	for k := range held {
		if k[0] > 0 && k[1] > 0 && !listed[k] {
			out = append(out, missingRow{Season: k[0], Episode: k[1]})
		}
	}

	return sortMissing(out)
}

// codesOf spells rows S01E02-style.
func codesOf(rows []missingRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("S%02dE%02d", r.Season, r.Episode))
	}

	return out
}

// recordAired says whether an episode the server keeps a record of, with no
// file, has aired: by the rule the provider's run is read by (aired), so an
// episode announced for next month, or announced with no date at all, is
// not reported missing by one source and left out by the other. A date that
// is not a day and time is an error: read as either, it would say an
// episode is missing, or leave one out, on a guess.
func recordAired(it *embyfin.Item, now time.Time) (bool, error) {
	day, _, _ := strings.Cut(it.PremiereDate, "T")
	if day != "" {
		if _, err := time.Parse(time.DateOnly, day); err != nil {
			return false, fmt.Errorf("the server dates %s (id %s) %q, which can't be read as a day", it.Name, it.ID, it.PremiereDate)
		}
	}

	return aired(tmdb.Episode{AirDate: day}, now), nil
}

// tmdbAiredOrder is how a run read from TMDB numbers its episodes.
const tmdbAiredOrder = "TMDB aired order"

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
		held     map[[2]int]bool   // every number a file covers, specials included
		titles   map[[2]int]string // the title of the file at each number it starts at
		records  bool              // the server keeps records of episodes it has no file for
		provider []string          // aired episodes the server lists without a file
		guide    string            // what the metadata provider lists without a file
		known    bool              // the whole run was read, from the records or the provider
		offRun   []missingRow      // held numbers the provider's run has no episode for
		// files the server holds no season or episode number for: they can
		// be any of the episodes that read as missing
		unnumbered []string
	}
	bySeries := map[string]*series{}
	answer := missingEpisodesOut{Findings: []missingFinding{}}
	now := time.Now()
	// the episodes and the server's records of those it has no file for, in
	// one sweep. Jellyfin keeps such records only with the TheTVDB plugin,
	// and whether its item query lists them as show_missing's episode read
	// does has not been seen on a server keeping them
	var reads []embyfin.ReadResult
	var dateErr error // a record's date that can't be read, which stops the sweep
	swept, err := client.ReadAll(ctx, opts, embyfin.ToAnswer, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			if it.SeriesID == "" {
				continue
			}
			s := bySeries[it.SeriesID]
			if s == nil {
				s = &series{name: it.SeriesName, onDisk: map[int][]int{}, held: map[[2]int]bool{}, titles: map[[2]int]string{}}
				bySeries[it.SeriesID] = s
			}
			if !it.HasFile() {
				// a record of an episode still to come is a run the server
				// knows, not an episode missing from it
				s.records = true
				on, derr := recordAired(it, now)
				if derr != nil {
					dateErr = derr

					return false
				}
				if on {
					s.provider = append(s.provider, episodeCode(it))
				}

				continue
			}
			answer.Scanned++
			if !numbered(it) {
				s.unnumbered = append(s.unnumbered, it.Path)

				continue
			}
			s.titles[[2]int{*it.ParentIndexNumber, *it.IndexNumber}] = it.Name
			// a file holding S01E01E02 is both, or E02 would be reported missing
			for _, n := range episodeSpan(it) {
				if n <= 0 {
					continue
				}
				s.onDisk[*it.ParentIndexNumber] = append(s.onDisk[*it.ParentIndexNumber], n)
				s.held[[2]int{*it.ParentIndexNumber, n}] = true
			}
		}
		return true
	})
	if err != nil {
		return missingEpisodesOut{}, err
	}
	if dateErr != nil {
		return missingEpisodesOut{}, dateErr
	}
	reads = append(reads, swept)

	// the series themselves, for the ids a provider knows them by and for the
	// ids that make two entries one show
	ids := make([]string, 0, len(bySeries))
	for id := range bySeries {
		ids = append(ids, id)
	}
	items := map[string]*embyfin.Item{}
	for chunk := range slices.Chunk(ids, 100) {
		shows, err := client.ReadAll(ctx, embyfin.SearchOptions{IDs: strings.Join(chunk, ","), IncludeItemTypes: "Series", Fields: "ProviderIds,Path"}, embyfin.ToAnswer, func(rows []embyfin.Item) bool {
			for i := range rows {
				items[rows[i].ID] = &rows[i]
			}

			return true
		})
		if err != nil {
			return missingEpisodesOut{}, err
		}
		reads = append(reads, shows)
	}

	// one show split across two entries - a folder renamed and the old one
	// left behind, both carrying the show's ids - is one show: judged alone,
	// each entry was missing what the other holds, where both servers answer
	// either entry with the other's episodes too (show_missing reads it so).
	// Entries sharing a tmdb, tvdb or imdb id are judged together, named by
	// the first, and the finding names the rest
	shows := sameShows(ids, func(id string) *embyfin.Item { return items[id] })
	for _, show := range shows {
		lead := bySeries[show[0]]
		for _, id := range show[1:] {
			s := bySeries[id]
			for season, eps := range s.onDisk {
				lead.onDisk[season] = append(lead.onDisk[season], eps...)
			}
			for k := range s.held {
				lead.held[k] = true
			}
			for k, title := range s.titles {
				lead.titles[k] = cmp.Or(lead.titles[k], title)
			}
			lead.records = lead.records || s.records
			lead.unnumbered = append(lead.unnumbered, s.unnumbered...)
			for _, e := range s.provider {
				if !slices.Contains(lead.provider, e) {
					lead.provider = append(lead.provider, e)
				}
			}
		}
		lead.known = lead.records
	}

	// by name, then by id: two series of one name (one show split across two
	// entries is the usual reason) otherwise swap places with the map's
	// order from one call to the next, and paged by offset one of them is
	// asked about twice and the other never
	slices.SortFunc(shows, func(a, b []string) int {
		return cmp.Or(strings.Compare(bySeries[a[0]].name, bySeries[b[0]].name), strings.Compare(a[0], b[0]))
	})

	if in.Provider {
		// the window is the shows asked about in this call
		from := min(max(in.Offset, 0), len(shows))
		to := min(from+maxLookups, len(shows))
		if to < len(shows) {
			answer.NextOffset = to
		}
		answer.Unknown = []unknownRun{}
		for _, show := range shows[from:to] {
			id := show[0]
			s := bySeries[id]
			item := items[id]
			if item == nil {
				item = &embyfin.Item{ID: id, Name: s.name}
			}
			run, reason := guideRun(ctx, guide, item)
			if reason != "" {
				// the server's own records still say the run
				if !s.known && len(answer.Unknown) < limit {
					answer.Unknown = append(answer.Unknown, unknownRun{ID: id, Name: s.name, Reason: reason})
				}

				continue
			}
			s.known = true
			answer.Order = tmdbAiredOrder
			s.guide = guideMissing(run, s.held)
			s.offRun = notInRun(run, s.held)
		}
		shows = shows[from:to]
	}

	answer.Series = len(shows)
	for _, show := range shows {
		id := show[0]
		s := bySeries[id]
		if !s.known {
			answer.TotalUnknown++
		}
		offRun := ""
		if len(s.offRun) > 0 {
			offRun = firstFew(codesOf(s.offRun))
			answer.TotalOther++
			if len(answer.OtherOrder) < limit {
				answer.OtherOrder = append(answer.OtherOrder, otherOrder{ID: id, Name: s.name, NotInRun: offRun})
			}
		}
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
		if len(answer.Findings) >= limit {
			continue
		}
		f := missingFinding{ID: id, Name: s.name, Detail: strings.Join(parts, "; "), RunKnown: s.known}
		var warnings []string
		if len(show) > 1 {
			also := make([]string, 0, len(show)-1)
			for _, other := range show[1:] {
				where := ""
				if it := items[other]; it != nil && it.Path != "" {
					where = " at " + it.Path
				}
				also = append(also, "id "+other+where)
			}
			warnings = append(warnings, fmt.Sprintf("the library holds %q under %d entries sharing its ids (also %s), judged here as one show: what is listed is missing from all of them. audit_duplicates lists every show in this state", s.name, len(show), strings.Join(also, "; ")))
		}
		if offRun != "" {
			warnings = append(warnings, fmt.Sprintf("the files hold %s, which TMDB's aired order has no episode for: they may be numbered in another order (TVDB's, a DVD's), and what is listed by TMDB may be held under other numbers", offRun))
		}
		warnings = append(warnings, joinedTitles(gapsOnDisk(s.onDisk), s.titles))
		if len(s.unnumbered) > 0 {
			warnings = append(warnings, fmt.Sprintf("the show also holds %d file(s) the server has no season or episode number for (%s): any of them may be an episode listed as missing", len(s.unnumbered), someOf(s.unnumbered)))
		}
		f.Warning = joinNotes(warnings...)
		answer.Findings = append(answer.Findings, f)
	}

	answer.RunsKnown = answer.TotalUnknown == 0
	switch {
	case answer.TotalUnknown == 0:
	case !in.Provider && answer.TotalUnknown == answer.Series:
		answer.Note = "the server keeps no record of an episode it has no file for (stock Jellyfin needs the TheTVDB plugin for those, and Emby 4.10 no longer imports them), so these findings are only what the files themselves show: the numbers skipped between them. A series not listed here is not known to be complete - provider: true reads every series' run from the metadata provider, and show_missing one series'."
	case !in.Provider:
		answer.Note = fmt.Sprintf("the server keeps a record of the run of %d of the %d shows; the other %d are judged only by the numbers skipped between their files (their findings say run_known false), so a show not listed here is not known to be complete - provider: true reads every show's run from the metadata provider, and show_missing one show's.",
			answer.Series-answer.TotalUnknown, answer.Series, answer.TotalUnknown)
	default:
		answer.Note = fmt.Sprintf("the run of %d of the %d shows asked about could not be read (unknown says why): they are judged only by the numbers skipped between their files, so one not listed here is not known to be complete.", answer.TotalUnknown, answer.Series)
	}
	answer.changed = changedNote(reads...)
	answer.Note = joinWarnings(answer.Note, answer.changed)

	return answer, nil
}

// sameShows groups series ids into shows: entries sharing a tmdb, tvdb or
// imdb id are one show, held under more than one entry - unless their AniDB
// ids differ (see sameAnime): a group holds at most one AniDB id, so an
// anime entry kept apart is not joined to its parent by the parent's id it
// carries. Each group is in the order its entries sort by (name, then id),
// so the first names it, and an entry that cannot be read (itemOf answers
// nil) is a show of its own.
func sameShows(ids []string, itemOf func(string) *embyfin.Item) [][]string {
	sorted := slices.Clone(ids)
	slices.SortFunc(sorted, func(a, b string) int {
		var na, nb string
		if it := itemOf(a); it != nil {
			na = it.Name
		}
		if it := itemOf(b); it != nil {
			nb = it.Name
		}

		return cmp.Or(strings.Compare(na, nb), strings.Compare(a, b))
	})
	root := map[string]string{}
	var find func(string) string
	find = func(id string) string {
		if r, ok := root[id]; ok && r != id {
			root[id] = find(r)

			return root[id]
		}

		return id
	}
	holders := map[string][]string{} // "<provider>:<id>" -> the entries carrying it
	anidb := map[string]string{}     // a group's root -> the AniDB id its entries carry
	for _, id := range sorted {
		root[id] = id
		it := itemOf(id)
		if it == nil {
			continue
		}
		anidb[id] = providerID(it, "anidb")
		for _, p := range []string{"tmdb", "tvdb", "imdb"} {
			v := providerID(it, p)
			if v == "" {
				continue
			}
			key := p + ":" + v
			for _, other := range holders[key] {
				// the earlier entry stays the root, so a show is named by
				// the first of its entries
				a, b := find(other), find(id)
				if a == b || anidb[a] != "" && anidb[b] != "" && anidb[a] != anidb[b] {
					continue
				}
				root[b] = a
				anidb[a] = cmp.Or(anidb[a], anidb[b])
			}
			holders[key] = append(holders[key], id)
		}
	}
	byRoot := map[string][]string{}
	var roots []string
	for _, id := range sorted {
		r := find(id)
		if byRoot[r] == nil {
			roots = append(roots, r)
		}
		byRoot[r] = append(byRoot[r], id)
	}
	out := make([][]string, 0, len(roots))
	for _, r := range roots {
		out = append(out, byRoot[r])
	}

	return out
}

type unwatchedIn struct {
	Library   string `json:"library,omitempty"    jsonschema:"restrict to one library by name or id"`
	Types     string `json:"types,omitempty"      jsonschema:"Movie, Series or both, comma-separated; default Movie. A series counts as watched when anyone has watched any of its episodes"`
	AddedDays int    `json:"added_days,omitempty" jsonschema:"only items added at least this many days ago, so what just arrived is left out"`
	Limit     int    `json:"limit,omitempty"      jsonschema:"maximum findings to return, default 100"`
}

type unwatchedOut struct {
	auditOut
	Users        []string       `json:"users"                   jsonschema:"whose watch state was read: every account on the server, in its own view and in every library it can no longer see"`
	TotalStarted int            `json:"total_started"           jsonschema:"items someone has started and nobody has finished: not among the findings"`
	Started      []auditFinding `json:"started"                 jsonschema:"films (or series) someone has started and nobody has finished - part way through, or begun and stopped in its first minutes - with who and how far: not candidates to archive, and not among the findings; oldest additions first, capped at limit"`
	Limited      []string       `json:"views_limited,omitempty" jsonschema:"accounts whose own view hides more than whole libraries - a parental rating, blocked or required tags, unrated items, folders inside a library (Emby) - and what: what they played of what their view hides is not read, so an item listed may have been watched by them"`
}

func registerMediaAudits(r *registry) {
	client := r.client
	var guide seriesGuide
	if facts := tmdbFacts(r.opts, r.opts.ProviderTransport); facts != nil {
		guide = facts
	}

	add(r, readTool, &mcp.Tool{
		Name: "audit_quality",
		Description: "Find the films and episodes worth replacing with a better copy: video below a resolution (default 720 lines, so 480p and 576p rips), in a legacy codec (MPEG-2, XviD and DivX, WMV, VC-1...), or below a bitrate when one is given. A show's extras (a featurette in a season's Extras folder, which Emby 4.10 holds as an episode) are not episodes and are left out. An item is judged by its best file, every version the server shows it in together (Emby stores each version as an item of its own and merges them only in what it shows people, so on Emby this reads the library as the first administrator is shown it), so a 4K version beside a DVD rip is not reported. Lowest resolution first. " +
			"Two more lists say which facts cannot be trusted: files the server holds no media facts for (never probed, so resolution, codec, bitrate and audio all read as nothing rather than as a measurement, and they are not judged) and, on Emby, files written over after the server first saw them, whose facts may still be the old file's until a scan re-reads them. Each of those rows carries the size the server believes, so a caller with the file in front of it can tell a re-read from a stale one; a scan of the library re-probes both.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in qualityIn) (*mcp.CallToolResult, qualityOut, error) {
		out, err := auditQuality(ctx, client, in)
		return nil, out, err
	})

	add(r, readTool, &mcp.Tool{
		Name: "audit_missing_episodes",
		Description: "Find the series with episodes missing: the episode numbers a season skips between the ones on disk (E01 and E03 but no E02), whole seasons skipped between the ones on disk, and, when the server records them, the episodes its metadata provider lists that have aired and have no file (stock Jellyfin needs the TheTVDB plugin for those, and Emby 4.10 does not record them). " +
			"With provider true, each series' whole run is read from the configured metadata providers instead (TMDB, with EMBYFIN_TMDB_TOKEN set), one request a series and paged, so what a series lacks after its last file is seen too. " +
			"A show held under two entries sharing its ids (a folder renamed and the old one left behind) is judged as one show, named by the first entry with the rest in its warning; two entries whose AniDB ids differ are two shows, whatever else they share. " +
			"TMDB's runs are in its aired order and files are compared with them number by number: a show whose files hold numbers that order has no episode for (numbered the TVDB way, as Sonarr names them, or a DVD's) is listed in numbered_otherwise and warned on, because what TMDB lists as missing may be held under other numbers. " +
			"A gap just after a file titled 'A & B' (two segments named by the first number alone) is warned on as probably held by that file. " +
			"Read 'runs_known': it is true only when every show's whole run was read; when it is false, total_unknown shows were judged only by the gaps between their files, so a show not listed is not known to be complete, and each finding says run_known for its own show.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in episodesIn) (*mcp.CallToolResult, missingEpisodesOut, error) {
		out, err := auditMissingEpisodes(ctx, client, guide, in)
		return nil, out, err
	})

	add(r, readTool, &mcp.Tool{
		Name: "audit_unwatched",
		Description: "Find what nobody has watched: the films (or series) no account on the server has played or started, oldest additions first, optionally only those added more than some days ago. A copy of a film watched anywhere on the server counts for every copy, and so does a play by an account that has since lost access to the library. " +
			"What someone has started and nobody finished - part way through, or begun and stopped in its first minutes - is not a finding: it is listed apart in started, with who and how far. A series counts as started when any episode is. " +
			"An account whose view is limited within a library is read as it sees the library: neither server lists what a parental rating, a tag block or an allowed-tags list, blocked unrated items or (on Emby) an excluded folder hides from it, so what it played of those is not counted; views_limited names every such account and what hides. What to archive or delete to free space, or what to recommend.",
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
	libraries, err := client.VirtualFolders(ctx)
	if err != nil {
		return unwatchedOut{}, err
	}

	// what anyone has watched, by title (a copy watched in another library
	// counts for this one's) and across the server: films played, and the
	// series of any episode played. An account's own view leaves out a
	// library it has lost access to, on both servers, and what it watched
	// there is still watched: both list it when a folder of the library is
	// named as the parent (see hiddenParents)
	watched := titles{}
	series := map[string]bool{}
	// and what anyone has started and nobody finished: a film stopped at
	// four fifths was listed "never watched" beside the ones nobody opened,
	// on the list of what to archive or delete
	started := starts{}
	startedSeries := map[string][]string{} // series id -> who started an episode of it
	out := unwatchedOut{
		Users:    []string{},
		Findings: []auditFinding{},
		Started:  []auditFinding{},
	}
	var reads []embyfin.ReadResult
	playedTypes := []string{}
	if types[typeMovie] {
		playedTypes = append(playedTypes, typeMovie)
	}
	if types["Series"] {
		playedTypes = append(playedTypes, typeEpisode)
	}
	for i := range users {
		u := &users[i]
		out.Users = append(out.Users, u.Name)
		if why := viewLimits(u); why != "" {
			out.Limited = append(out.Limited, u.Name+": "+why)
		}
		played := func(items []embyfin.Item) bool {
			for i := range items {
				if items[i].Type == typeEpisode {
					series[items[i].SeriesID] = true
					continue
				}
				watched.add(&items[i])
			}
			return true
		}
		begun := func(it *embyfin.Item) {
			who := u.Name
			if _, percent := progressOf(it); percent > 0 {
				who = fmt.Sprintf("%s (%d%%)", u.Name, percent)
			}
			if it.Type == typeEpisode {
				who = fmt.Sprintf("%s (%s)", u.Name, strings.TrimPrefix(episodeOrItemName(it), it.SeriesName+" "))
				if !slices.Contains(startedSeries[it.SeriesID], who) {
					startedSeries[it.SeriesID] = append(startedSeries[it.SeriesID], who)
				}

				return
			}
			started.add(it, who)
		}
		parents := []string{""} // the account's own view
		for i := range libraries {
			if u.CanSee(&libraries[i]) {
				continue
			}
			hidden, herr := hiddenParents(ctx, client, &libraries[i])
			if herr != nil {
				return unwatchedOut{}, herr
			}
			parents = append(parents, hidden...)
		}
		for _, parent := range parents {
			seen, perr := client.ReadAll(ctx, embyfin.SearchOptions{
				IncludeItemTypes: strings.Join(playedTypes, ","), Filters: "IsPlayed", UserID: u.ID, EnableUserData: true, Fields: "Path,ProviderIds", ParentID: parent,
			}, embyfin.ToAnswer, played)
			if perr != nil {
				return unwatchedOut{}, perr
			}
			reads = append(reads, seen)
			resumed, serr := startedIn(ctx, client, u.ID, strings.Join(playedTypes, ","), parent, begun)
			if serr != nil {
				return unwatchedOut{}, serr
			}
			reads = append(reads, resumed)
		}
	}
	delete(series, "")
	delete(startedSeries, "")
	seriesIDs := slices.Collect(maps.Keys(series))
	for id := range startedSeries {
		if !series[id] {
			seriesIDs = append(seriesIDs, id)
		}
	}
	for ids := range slices.Chunk(seriesIDs, 100) {
		shows, serr := client.ReadAll(ctx, embyfin.SearchOptions{IDs: strings.Join(ids, ","), IncludeItemTypes: "Series", Fields: "Path,ProviderIds"}, embyfin.ToAnswer, func(items []embyfin.Item) bool {
			for i := range items {
				if series[items[i].ID] {
					watched.add(&items[i])
				}
				for _, who := range startedSeries[items[i].ID] {
					started.add(&items[i], who)
				}
			}
			return true
		})
		if serr != nil {
			return unwatchedOut{}, serr
		}
		reads = append(reads, shows)
	}

	kinds := make([]string, 0, len(types))
	for t := range types {
		kinds = append(kinds, t)
	}
	type dated struct {
		row   auditFinding
		added string
	}
	var findings, begun []dated
	var dateErr error // an added date that can't be read, which stops the sweep
	// every copy as the server stores it, each with its own file: watching
	// is counted by title, so the copies of a film are all watched or all
	// not, and what to archive is each file
	swept, err := client.ReadAll(ctx, embyfin.SearchOptions{IncludeItemTypes: strings.Join(kinds, ","), ParentID: parent, Fields: embyfin.FieldsLean}, embyfin.ToAnswer, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			out.Scanned++
			if watched.has(it) {
				continue
			}
			if in.AddedDays > 0 {
				after, derr := afterCutoff(it, daysCutoff(in.AddedDays))
				if derr != nil {
					dateErr = derr

					return false
				}
				if after {
					continue
				}
			}
			// by the day added, then by name, so films added together keep
			// one order and a limit the same ones; an item with no date
			// ("on an unknown date") sorts after every dated one
			row := dated{row: auditFinding{ID: it.ID, Name: it.Name, Year: it.ProductionYear, Path: it.Path}, added: dateOf(it.DateCreated)}
			if who := started.of(it); len(who) > 0 {
				row.row.Detail = "started and never finished, by " + strings.Join(who, ", ") + "; added " + dateOf(it.DateCreated)
				begun = append(begun, row)

				continue
			}
			row.row.Detail = "never watched, added " + dateOf(it.DateCreated)
			findings = append(findings, row)
		}
		return true
	})
	if err != nil {
		return unwatchedOut{}, err
	}
	if dateErr != nil {
		return unwatchedOut{}, dateErr
	}
	out.Note = changedNote(append(reads, swept)...)

	// the oldest additions first
	for _, list := range [][]dated{findings, begun} {
		slices.SortStableFunc(list, func(a, b dated) int {
			return cmp.Or(strings.Compare(a.added, b.added), strings.Compare(a.row.Name, b.row.Name), strings.Compare(a.row.ID, b.row.ID))
		})
	}
	out.Found, out.TotalStarted = len(findings), len(begun)
	for _, f := range findings[:min(len(findings), limit)] {
		out.Findings = append(out.Findings, f.row)
	}
	for _, f := range begun[:min(len(begun), limit)] {
		out.Started = append(out.Started, f.row)
	}

	return out, nil
}

// starts is who has started a title, by each key it is known by (see
// titleKeys): a copy started anywhere is started for every copy, as a copy
// watched anywhere is watched.
type starts map[string][]string

func (s starts) add(it *embyfin.Item, who string) {
	for _, k := range titleKeys(it) {
		if !slices.Contains(s[k], who) {
			s[k] = append(s[k], who)
		}
	}
}

// of is everyone who started the item's title, in order.
func (s starts) of(it *embyfin.Item) []string {
	var out []string
	for _, k := range titleKeys(it) {
		for _, who := range s[k] {
			if !slices.Contains(out, who) {
				out = append(out, who)
			}
		}
	}
	slices.Sort(out)

	return out
}

// begunPage is how many rows one read of an account's begun-but-unplayed
// items asks for.
const begunPage = 1000

// startedIn reads what an account has started and not finished, in its own
// view or under parent, and hands each to cb: everything part way through
// (a resume point), and everything it began and stopped where no resume
// point is kept - both servers count a play and date it from its start, and
// drop the resume point of one stopped in its first minutes, so a film begun
// and abandoned has a play count and a last played date and no position.
//
// Neither server filters on a play count, so the unplayed are read by when
// they were last played, most recent first, and the read stops at the first
// never begun: past it, none were. A page that shows the order was not kept
// (one begun after one never begun) reads on to the end instead.
//
// resumed is what the read of the part-watched saw of the library changing.
func startedIn(ctx context.Context, client *embyfin.Client, userID, types, parent string, cb func(*embyfin.Item)) (resumed embyfin.ReadResult, err error) {
	base := embyfin.SearchOptions{IncludeItemTypes: types, UserID: userID, EnableUserData: true, Fields: "Path,ProviderIds", ParentID: parent}
	resumable := base
	resumable.Filters = "IsResumable"
	resumed, err = client.ReadAll(ctx, resumable, embyfin.ToAnswer, func(items []embyfin.Item) bool {
		for i := range items {
			if wasBegun(&items[i]) {
				cb(&items[i])
			}
		}
		return true
	})
	if err != nil {
		return resumed, err
	}

	opts := base
	opts.Filters, opts.SortBy, opts.SortOrder, opts.Limit = "IsUnplayed", "DatePlayed,SortName", sortDescending, begunPage
	ordered := true
	for start := 0; ; start += begunPage {
		opts.StartIndex = start
		items, total, err := client.Search(ctx, opts)
		if err != nil {
			return resumed, err
		}
		ended := false
		for i := range items {
			if !wasBegun(&items[i]) {
				ended = true

				continue
			}
			if ended {
				ordered = false
			}
			cb(&items[i])
		}
		if ended && ordered || len(items) < begunPage || start+len(items) >= total {
			return resumed, nil
		}
	}
}

// wasBegun says whether an account has ever started an item: a play counted,
// a date it was last played, or a resume point.
func wasBegun(it *embyfin.Item) bool {
	ud := it.UserData

	return ud != nil && (ud.PlayCount > 0 || ud.LastPlayedDate != "" || ud.PlaybackPositionTicks > 0)
}

// viewLimits says what an account's own view hides beyond whole libraries,
// "" when nothing: what it played of those is not read by a sweep of its
// view, and neither server lists it any other way.
func viewLimits(u *embyfin.User) string {
	p := u.Policy
	var why []string
	if p.MaxParentalRating != nil {
		why = append(why, "a parental rating limit")
	}
	if len(p.BlockUnratedItems) > 0 {
		why = append(why, "unrated "+strings.Join(p.BlockUnratedItems, ", ")+" blocked")
	}
	if len(p.BlockedTags) > 0 {
		why = append(why, "items tagged "+strings.Join(p.BlockedTags, ", ")+" blocked")
	}
	if len(p.AllowedTags) > 0 {
		why = append(why, "only items tagged "+strings.Join(p.AllowedTags, ", ")+" shown")
	}
	if len(p.BlockedFolders) > 0 {
		why = append(why, fmt.Sprintf("%d folders blocked by id", len(p.BlockedFolders)))
	}

	return strings.Join(why, "; ")
}

// hiddenParents are the parents to name to read what an account played in a
// library it cannot see, which its own view leaves out on both servers:
// Jellyfin lists it under the library itself; Emby leaves the library out
// even named, and lists it under the folders the library is built from.
func hiddenParents(ctx context.Context, client *embyfin.Client, folder *embyfin.VirtualFolder) ([]string, error) {
	if client.Backend() != embyfin.Emby {
		if folder.ItemID == "" {
			return nil, nil
		}

		return []string{folder.ItemID}, nil
	}
	var out []string
	for _, loc := range folder.Locations {
		items, _, err := client.Search(ctx, embyfin.SearchOptions{Path: loc, Fields: "Path", Limit: 1})
		if err != nil {
			return nil, err
		}
		// a path Emby could not match drops the filter, so the answer has to
		// be the folder asked for
		if len(items) == 1 && trimSep(items[0].Path) == trimSep(loc) {
			out = append(out, items[0].ID)
		}
	}

	return out, nil
}

// watchedOutOfView reads an item in the view of an account that cannot see
// its library, for what the account watched of it before it lost access.
// Emby's single read answers for an item the account may not see, with the
// play count and last played date its lists leave out; Jellyfin's is a 404,
// and it lists the item with the library named as the parent, as
// audit_unwatched reads it (hiddenParents). nil when neither has it.
func watchedOutOfView(ctx context.Context, client *embyfin.Client, user *embyfin.User, folder *embyfin.VirtualFolder, itemID string) (*embyfin.Item, error) {
	if client.Backend() == embyfin.Emby {
		return client.UserItem(ctx, user.ID, itemID)
	}
	parents, err := hiddenParents(ctx, client, folder)
	if err != nil {
		return nil, err
	}
	for _, parent := range parents {
		items, _, err := client.Search(ctx, embyfin.SearchOptions{IDs: itemID, UserID: user.ID, EnableUserData: true, ParentID: parent, Fields: "Path", Limit: 2})
		if err != nil {
			return nil, err
		}
		if len(items) > 0 && items[0].ID == itemID {
			return &items[0], nil
		}
	}

	return nil, nil //nolint:nilnil // nothing to read: the caller reports no row
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
