package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// missingRow is one episode a series does not hold.
type missingRow struct {
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
	Name    string `json:"name,omitempty"`
	AirDate string `json:"air_date,omitempty"`
}

// Where a missing-episode answer came from, which is what tells "complete"
// from "cannot know".
const (
	// sourceServer: the server's own records list episodes with no file.
	// Stock Jellyfin needs the TheTVDB plugin to keep them and Emby 4.10 has
	// dropped the import, so most servers have none.
	sourceServer = "server"
	// sourceTMDB: the run was read from TMDB, by the series' own metadata
	// provider id.
	sourceTMDB = "tmdb"
	// sourceNone: nothing could be asked, so the run is not known at all.
	sourceNone = "none"
)

// missingOut answers "what is this series missing". Supported is the field
// to read first: false means the run could not be established at all, and
// Missing is then null rather than an empty list, because an empty list reads
// as "nothing is missing" and a caller that believed it would delete files it
// should have kept.
type missingOut struct {
	Series     string        `json:"series"`
	Supported  bool          `json:"supported"                     jsonschema:"whether the full run could be established. When false, missing is null: the answer is unknown, NOT that the series is complete"`
	Source     string        `json:"source"                        jsonschema:"where the answer came from: server (the server's own records), tmdb (the run read from the metadata provider by the series' id), or none (nothing could be asked)"`
	Missing    *[]missingRow `json:"missing"                       jsonschema:"episodes the series has no file for, in order. Null when supported is false"`
	Reason     string        `json:"reason,omitempty"              jsonschema:"why the run could not be established, and what would make it knowable"`
	Gaps       []missingRow  `json:"gaps_on_disk,omitempty"        jsonschema:"a weaker fact, given whether or not the run is known: the episode numbers skipped between the ones on disk. Never the whole answer, because nothing after the last episode held shows up as a gap"`
	GapSeasons []int         `json:"season_gaps_on_disk,omitempty" jsonschema:"whole seasons skipped between the ones on disk"`
}

// seriesGuide reads a series' whole run from a metadata provider. Only TMDB
// implements it; the interface is what show_missing depends on so a test can
// answer for it.
type seriesGuide interface {
	SeriesEpisodes(ctx context.Context, id string) ([]tmdb.Episode, error)
	SeriesID(ctx context.Context, source, id string) (string, error)
}

// guideSeriesID is the provider id to read a series' run by: its own tmdb id,
// else the id TMDB knows it by from its tvdb or imdb one. It returns "" for a
// series carrying none of them, which is a series nobody has identified.
func guideSeriesID(ctx context.Context, guide seriesGuide, series *embyfin.Item) (string, error) {
	if id := providerID(series, "tmdb"); id != "" {
		return id, nil
	}
	// TMDB places a series known by another provider's id through /find
	for _, p := range []struct{ key, source string }{{"tvdb", "tvdb_id"}, {"imdb", "imdb_id"}} {
		id := providerID(series, p.key)
		if id == "" {
			continue
		}
		found, err := guide.SeriesID(ctx, p.source, id)
		if err != nil {
			return "", err
		}
		if found != "" {
			return found, nil
		}
	}

	return "", nil
}

// aired reports whether an episode has been broadcast by now. An episode with
// no air date at all has not: TMDB carries announced episodes before it knows
// when they run.
func aired(e tmdb.Episode, now time.Time) bool {
	if e.AirDate == "" {
		return false
	}
	day, err := time.Parse(time.DateOnly, e.AirDate)
	if err != nil {
		return true // an air date we cannot read is not evidence it is unaired
	}

	return !day.After(now)
}

// sortMissing orders a worklist the way a season reads.
func sortMissing(rows []missingRow) []missingRow {
	slices.SortFunc(rows, func(a, b missingRow) int {
		if c := cmp.Compare(a.Season, b.Season); c != 0 {
			return c
		}
		return cmp.Compare(a.Episode, b.Episode)
	})

	return rows
}

// showMissing answers what a series lacks, from the best source that can
// answer: the server's own records, else the metadata provider. When neither
// can, it says so rather than answering with an empty list - see
// missingOut.Supported.
func showMissing(ctx context.Context, client *embyfin.Client, guide seriesGuide, seriesID string, unaired bool) (missingOut, error) {
	series, err := client.ItemByID(ctx, seriesID)
	if err != nil {
		return missingOut{}, err
	}

	episodes, err := client.EpisodesAndRecords(ctx, seriesID)
	if err != nil {
		return missingOut{}, err
	}

	// Emby ignores the missing filter and answers every episode, so the two
	// are told apart here: a missing episode is a record with no file.
	out := missingOut{Series: series.Name, Source: sourceNone}
	onDisk := map[int][]int{}
	held := map[[2]int]bool{}
	var records []*embyfin.Item
	for i := range episodes {
		e := &episodes[i]
		if !e.HasFile() {
			records = append(records, e)
			continue
		}
		last := max(e.IndexNumberEnd, e.IndexNumber)
		for n := e.IndexNumber; n <= last; n++ {
			held[[2]int{e.ParentIndexNumber, n}] = true
			if n > 0 {
				onDisk[e.ParentIndexNumber] = append(onDisk[e.ParentIndexNumber], n)
			}
		}
	}

	// the gaps between the files are a weaker fact than the run, and are
	// given either way: they are what a caller has when the run is unknown,
	// and a cross-check when it is known
	for _, h := range gapsOnDisk(onDisk) {
		if h.Episode == 0 {
			out.GapSeasons = append(out.GapSeasons, h.Season)
			continue
		}
		out.Gaps = append(out.Gaps, missingRow{Season: h.Season, Episode: h.Episode})
	}

	// a server that keeps records of the run answers from them, by the same
	// rules the provider's run is read by: an episode another file already
	// holds (one file covering E01-E02 beside a record for E02) is not
	// missing, and one not yet broadcast - dated in the future, or not dated
	// at all (see recordAired) - is only listed when asked for
	if len(records) > 0 {
		now := time.Now()
		known := []missingRow{}
		for _, e := range records {
			if held[[2]int{e.ParentIndexNumber, e.IndexNumber}] {
				continue
			}
			if !unaired && !recordAired(e, now) {
				continue
			}
			known = append(known, missingRow{Season: e.ParentIndexNumber, Episode: e.IndexNumber, Name: e.Name, AirDate: e.PremiereDate})
		}
		out.Supported, out.Source = true, sourceServer
		out.Missing = new(sortMissing(known))

		return out, nil
	}

	// the server knows of no episode it has no file for, which on Emby 4.10
	// and on stock Jellyfin it never does: read the run from the provider
	if guide == nil {
		out.Reason = "the server keeps no record of an episode it has no file for (stock Jellyfin needs the TheTVDB plugin for those, and Emby 4.10 no longer imports them), and no metadata provider is configured to be asked instead: set EMBYFIN_TMDB_TOKEN (or --tmdb-token) and restart."

		return out, nil
	}

	return missingFromGuide(ctx, guide, series, out, held, unaired), nil
}

// guideRun reads a series' whole run from the metadata provider, or the
// reason it could not be read. A provider that cannot be reached leaves the
// run unknown; it does not make the question unanswerable, and "unknown, and
// here is why" is a better answer than a failed call.
func guideRun(ctx context.Context, guide seriesGuide, series *embyfin.Item) (run []tmdb.Episode, reason string) {
	id, err := guideSeriesID(ctx, guide, series)
	switch {
	case err != nil:
		return nil, "the metadata provider could not be asked: " + err.Error()
	case id == "":
		return nil, "the series carries no tmdb, tvdb or imdb id for a metadata provider to be asked by, and the server keeps no record of the run: identify it first with item_identify."
	}

	run, err = guide.SeriesEpisodes(ctx, id)
	switch {
	case err != nil:
		return nil, "the metadata provider could not be asked: " + err.Error()
	case len(run) == 0:
		return nil, fmt.Sprintf("TMDB lists no episodes for series %s, and the server keeps no record of the run.", id)
	}

	return run, ""
}

// missingFromGuide compares the provider's run against what the series holds.
// When the provider cannot answer, out comes back unsupported with the reason.
func missingFromGuide(ctx context.Context, guide seriesGuide, series *embyfin.Item, out missingOut, held map[[2]int]bool, unaired bool) missingOut {
	run, reason := guideRun(ctx, guide, series)
	if reason != "" {
		out.Reason = reason

		return out
	}

	now := time.Now()
	missing := []missingRow{}
	for _, e := range run {
		if held[[2]int{e.Season, e.Episode}] {
			continue
		}
		if !unaired && !aired(e, now) {
			continue
		}
		missing = append(missing, missingRow{Season: e.Season, Episode: e.Episode, Name: e.Name, AirDate: e.AirDate})
	}
	out.Supported, out.Source = true, sourceTMDB
	out.Missing = new(sortMissing(missing))

	return out
}

// seasonOf is the season an item belongs to, for the summaries that give one:
// a season's own number, or an episode's season. 0 is the specials, so it has
// to be said rather than left out, and a film or a series has no season at all
// - which is what nil says, rather than a 0 that would read as the specials.
func seasonOf(it *embyfin.Item) *int {
	switch it.Type {
	case "Season":
		return new(it.IndexNumber)
	case typeEpisode:
		return new(it.ParentIndexNumber)
	}

	return nil
}

// episodeOf is an item's own number, except on a season, whose number is its
// season's and is given as that instead of as an episode.
func episodeOf(it *embyfin.Item) int {
	if it.Type == "Season" {
		return 0
	}

	return it.IndexNumber
}

// resolveSeriesRef finds a series given one string that may be its id or its
// name, optionally within one library: the id of a series the library holds
// is that series, and anything else is a name, matched and refused as
// resolveSeriesMatch does. A name can be what another show's id is - Emby's
// ids are numbers, and 24 and 1923 are shows - so an id that a confident name
// match says is a different show is refused naming both rather than one
// picked. An id the library's index has not caught up with (a show added a
// moment ago) is still found when nothing is named it.
func resolveSeriesRef(ctx context.Context, r *registry, ref, library string) (*embyfin.Item, *seriesCandidate, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil, errors.New("a series is required: give it by name or id")
	}
	folder, err := resolveLibrary(ctx, r.client, library)
	if err != nil {
		return nil, nil, err
	}
	parent := ""
	if folder != nil {
		parent = folder.ItemID
	}
	idx, err := r.seriesCache().get(ctx, r.client, parent)
	if err != nil {
		return nil, nil, err
	}
	if i, ok := idx.byID[ref]; ok {
		byID := idx.items[i]
		if rows := idx.rank(parseRelease(ref)); len(rows) > 0 && rows[0].Score >= seriesConfident && rows[0].SeriesID != byID.ID {
			return nil, nil, fmt.Errorf("%q is the id of %s (%d) at %s, and also names %s (%d) id %s at %s: give series_id for the one by id, or more of the name",
				ref, byID.Name, byID.ProductionYear, byID.Path, rows[0].Name, rows[0].Year, rows[0].SeriesID, rows[0].Path)
		}

		return &byID, nil, nil
	}

	item, match, err := resolveSeriesMatch(ctx, r, "", ref, library)
	if err == nil {
		return item, match, nil
	}
	if folder == nil {
		if it, ierr := r.client.ItemByID(ctx, ref); ierr == nil && it.Type == "Series" {
			return it, nil, nil
		}
	}

	return nil, nil, err
}

// resolveSeriesMatch finds the series a tool was pointed at: by id, or by
// name when the caller has only that, optionally within one library. A name
// that matches more than one series is refused with the matches rather than
// one of them being picked - a library holding the same show twice is
// ordinary (a tidy copy and a messy one), and guessing between them would
// answer a question about the wrong files.
//
// It also says how sure it is.
// The score comes back on the way out, not only in the refusal: a caller
// deciding whether to keep a file needs to see a 0.92 for what it is, and the
// only way to see one used to be to make the query ambiguous on purpose.
func resolveSeriesMatch(ctx context.Context, r *registry, id, name, library string) (*embyfin.Item, *seriesCandidate, error) {
	client := r.client
	if id != "" {
		item, err := client.ItemByID(ctx, id)

		return item, nil, err
	}
	if strings.TrimSpace(name) == "" {
		return nil, nil, errors.New("a series is required: give series_id, or series by name")
	}

	folder, err := resolveLibrary(ctx, client, library)
	if err != nil {
		return nil, nil, err
	}
	parent := ""
	if folder != nil {
		parent = folder.ItemID
	}

	// the same matcher show_resolve answers with, because the two were asked
	// the same question and disagreed: "24 Hours in A and E" is how every
	// scene name spells "24 Hours in A&E", and a plain search for it matches
	// hundreds of series and commits to none. Apostrophes, colons and
	// ampersands are stripped by the naming conventions this is fed from, so
	// a name path that needs them punctuated right fails on most real input.
	rows, seen, err := r.matchSeries(ctx, parseRelease(name), parent)
	if err != nil {
		return nil, nil, err
	}

	byID := func(id string) (*embyfin.Item, error) {
		for i := range seen {
			if seen[i].ID == id {
				return &seen[i], nil
			}
		}

		return client.ItemByID(ctx, id)
	}
	where := ""
	if folder != nil {
		where = " in " + folder.Name
	}
	switch {
	case len(rows) == 0 && len(seen) == 1:
		// nothing scored, and the search found exactly one thing. That used to
		// be the answer, and it is the rule below broken at its weakest: the
		// servers' search matches inside words, so "Andor" in a library
		// without it finds Pandora, alone, and a lone hit whose title matched
		// nothing is not a match at all
		return nil, nil, fmt.Errorf("%q matches nothing%s: the search found only %s (%d) id %s at %s, and its title did not match the name - give series_id if it is the one you meant",
			name, where, seen[0].Name, seen[0].ProductionYear, seen[0].ID, seen[0].Path)
	case len(rows) == 0:
		return nil, nil, fmt.Errorf("no series named %q", name)
	}

	// one clear winner is the answer. A tie is not: two series really are
	// called Severance, and picking either would be a guess a caller cannot
	// see us make.
	//
	// Being the only candidate is NOT being a winner. "Sentai Daishikkaku"
	// turned up one series scoring 0.30 on the bare word "Sentai" - a
	// different show entirely - and answering with it silently was worse than
	// refusing, because the caller went on to ask what that series was
	// missing and believed the answer.
	if rows[0].Score >= seriesConfident && clearWinner(rows) {
		item, err := byID(rows[0].SeriesID)
		if err != nil {
			return nil, nil, err
		}
		match := rows[0]
		if len(rows) > 1 {
			match.RunnerUp = rows[1].Score
			match.RunnerUpName = rows[1].Name
		}

		return item, &match, nil
	}

	// a loose name can match most of a library, and the refusal is read by a
	// caller working through a batch: the first few are what it needs to
	// choose between, and the long tail is pure cost. A hundred matches
	// spelled out is an error dearer than the answer it replaced.
	shown := min(len(rows), ambiguousShown)
	names := make([]string, 0, shown)
	for _, row := range rows[:shown] {
		names = append(names, fmt.Sprintf("%s (%d) id %s scored %.2f at %s", row.Name, row.Year, row.SeriesID, row.Score, row.Path))
	}
	rest := ""
	if more := len(rows) - shown; more > 0 {
		rest = fmt.Sprintf(", showing %d (%d more not listed)", shown, more)
	}

	// several weak candidates are not an ambiguity: "narrow it with library"
	// told a caller that one of two good matches would do, when neither was
	// one ("Star Trek Picard" beside two other Star Treks)
	switch {
	case len(rows) == 1:
		return nil, nil, fmt.Errorf("%q matches nothing well enough to act on%s: the closest is %s, which is a guess rather than a match - give series_id if it is the one you meant", name, where, names[0])
	case rows[0].Score < seriesConfident:
		return nil, nil, fmt.Errorf("%q matches nothing well enough to act on%s: the closest are %s%s, guesses rather than matches - give series_id if one of them is the one you meant", name, where, strings.Join(names, "; "), rest)
	}

	return nil, nil, fmt.Errorf("%q matches %d series%s%s: %s - give series_id, or narrow it with library", name, len(rows), where, rest, strings.Join(names, "; "))
}

// What the name path will commit to on its own: a score show_resolve would
// call a match rather than a guess, and clear enough of the next candidate
// that choosing it is not picking one of two.
const (
	seriesConfident = 0.9
	seriesMargin    = 0.02
	// scoreTolerance is how far apart two scores can be and still be the
	// same number. Scores are hundredths, and the difference of two of them
	// is not: 0.95-0.93 comes out a hair under 0.02 and 0.92-0.90 a hair
	// over, so the same margin was decisive or not depending on the digits.
	scoreTolerance = 1e-9
)

// clearWinner says whether the best candidate stands clear of the next one
// by the margin. A single candidate does by definition; whether it scored
// enough to act on is the caller's other question.
func clearWinner(rows []seriesCandidate) bool {
	return len(rows) == 1 || rows[0].Score-rows[1].Score >= seriesMargin-scoreTolerance
}

// ambiguousShown is how many of the matches a refusal spells out.
const ambiguousShown = 5

func registerShowTools(r *registry) {
	client := r.client

	var guide seriesGuide
	if facts := tmdbFacts(r.opts); facts != nil {
		guide = facts
	}

	type seasonsIn struct {
		SeriesID string `json:"series_id" jsonschema:"the series item id (find it with library_items types=Series)"`
	}
	type seasonRow struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		// always given: 0 is the specials, and an omitted 0 left the one
		// season a caller most needs to tell apart with no number at all
		Season int `json:"season" jsonschema:"the season's number, 0 for the specials"`
	}
	type seasonsOut struct {
		Series  string      `json:"series"`
		Seasons []seasonRow `json:"seasons"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "show_seasons",
		Description: "List a series' seasons, each with its number (0 for the specials). A season's episodes come from library_episodes, with series and season.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in seasonsIn) (*mcp.CallToolResult, seasonsOut, error) {
		series, err := client.ItemByID(ctx, in.SeriesID)
		if err != nil {
			return nil, seasonsOut{}, err
		}

		seasons, err := client.Seasons(ctx, in.SeriesID, "")
		if err != nil {
			return nil, seasonsOut{}, err
		}

		out := seasonsOut{Series: series.Name}
		for _, s := range seasons {
			out.Seasons = append(out.Seasons, seasonRow{ID: s.ID, Name: s.Name, Season: s.IndexNumber})
		}

		return nil, out, nil
	})

	type missingIn struct {
		SeriesID string `json:"series_id"                 jsonschema:"the series item id"`
		Unaired  bool   `json:"include_unaired,omitempty" jsonschema:"also list episodes that have not been broadcast yet; off by default, so the answer is what could be had"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "show_missing",
		Description: "Episodes a series has no file for. The run comes from the server's own records when it keeps them (stock Jellyfin needs the TheTVDB plugin and Emby 4.10 no longer imports them), else from TMDB read by the series' metadata provider id when EMBYFIN_TMDB_TOKEN is set. " +
			"Read 'supported' before 'missing': when it is false the run could not be established at all and 'missing' is null, which is unknown rather than complete. 'gaps_on_disk' is given either way and is weaker: it can only see episodes skipped between the files, never ones after the last episode held.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in missingIn) (*mcp.CallToolResult, missingOut, error) {
		out, err := showMissing(ctx, client, guide, in.SeriesID, in.Unaired)

		return nil, out, err
	})
}
