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
	var known []missingRow
	for _, e := range episodes {
		if !e.HasFile() {
			known = append(known, missingRow{Season: e.ParentIndexNumber, Episode: e.IndexNumber, Name: e.Name, AirDate: e.PremiereDate})
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

	if len(known) > 0 {
		out.Supported, out.Source = true, sourceServer
		out.Missing = new(sortMissing(known))

		return out, nil
	}

	// the server knows of no episode it has no file for, which on Emby 4.10
	// and on stock Jellyfin it never does: read the run from the provider
	if guide == nil {
		out.Reason = "the server keeps no record of an episode it has no file for (stock Jellyfin needs the TheTVDB plugin for those, and Emby 4.10 no longer imports them), and no metadata provider is configured to be asked instead: set EMBYFIN_TMDB_KEY (or --tmdb-key) and restart."

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

// resolveSeries finds the series a tool was pointed at: by id, or by name
// when the caller has only that, optionally within one library. A name that
// matches more than one series is refused with the matches rather than one
// of them being picked - a library holding the same show twice is ordinary
// (a tidy copy and a messy one), and guessing between them would answer a
// question about the wrong files.
func resolveSeries(ctx context.Context, client *embyfin.Client, id, name, library string) (*embyfin.Item, error) {
	if id != "" {
		return client.ItemByID(ctx, id)
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("a series is required: give series_id, or series by name")
	}

	opts := embyfin.SearchOptions{SearchTerm: name, IncludeItemTypes: "Series", Fields: "Path,ProductionYear"}
	folder, err := resolveLibrary(ctx, client, library)
	if err != nil {
		return nil, err
	}
	if folder != nil {
		opts.ParentID = folder.ItemID
	}
	items, _, err := client.Search(ctx, opts)
	if err != nil {
		return nil, err
	}

	// an exact title beats the others the server's search turned up
	var exact []embyfin.Item
	for i := range items {
		if strings.EqualFold(items[i].Name, name) {
			exact = append(exact, items[i])
		}
	}
	if len(exact) == 0 {
		exact = items
	}
	switch len(exact) {
	case 0:
		return nil, fmt.Errorf("no series named %q", name)
	case 1:
		return &exact[0], nil
	}

	where := ""
	if folder != nil {
		where = " in " + folder.Name
	}
	names := make([]string, 0, len(exact))
	for i := range exact {
		names = append(names, fmt.Sprintf("%s (%d) id %s at %s", exact[i].Name, exact[i].ProductionYear, exact[i].ID, exact[i].Path))
	}

	return nil, fmt.Errorf("%q matches %d series%s: %s - give series_id, or narrow it with library", name, len(exact), where, strings.Join(names, "; "))
}

func registerShowTools(r *registry) {
	client := r.client

	var guide seriesGuide
	if r.opts.TMDBKey != "" {
		guide = tmdb.NewWithTransport(r.opts.TMDBKey, r.opts.ProviderTransport)
	}

	type seasonsIn struct {
		SeriesID string `json:"series_id" jsonschema:"the series item id (find it with library_search types=Series)"`
	}
	type seasonRow struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Season int    `json:"season,omitempty"`
	}
	type seasonsOut struct {
		Series  string      `json:"series"`
		Seasons []seasonRow `json:"seasons"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "show_seasons",
		Description: "List a series' seasons.",
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

	type episodesIn struct {
		SeriesID string `json:"series_id"           jsonschema:"the series item id"`
		SeasonID string `json:"season_id,omitempty" jsonschema:"restrict to one season (id from show_seasons)"`
		Season   int    `json:"season,omitempty"    jsonschema:"restrict to one season by number, when its id is not to hand"`
	}
	type episodesOut struct {
		Series   string       `json:"series"`
		SeriesID string       `json:"series_id"`
		Episodes []episodeRow `json:"episodes"  jsonschema:"in broadcast order, each with its quality facts"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "show_episodes",
		Description: "List one series' episodes with the quality facts on each row - width, height, video codec, bitrate, size, container and runtime - optionally scoped to one season. For every episode in a library at once, rather than a call per series, use library_episodes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in episodesIn) (*mcp.CallToolResult, episodesOut, error) {
		series, err := client.ItemByID(ctx, in.SeriesID)
		if err != nil {
			return nil, episodesOut{}, err
		}

		episodes, err := client.Episodes(ctx, in.SeriesID, embyfin.EpisodeOptions{SeasonID: in.SeasonID, Season: in.Season})
		if err != nil {
			return nil, episodesOut{}, err
		}
		if in.Season > 0 {
			// Emby answers the whole series for a season it cannot place
			episodes = slices.DeleteFunc(episodes, func(e embyfin.Item) bool { return e.ParentIndexNumber != in.Season })
		}

		return nil, episodesOut{Series: series.Name, SeriesID: series.ID, Episodes: episodeRows(episodes, true)}, nil
	})

	type missingIn struct {
		SeriesID string `json:"series_id"                 jsonschema:"the series item id"`
		Unaired  bool   `json:"include_unaired,omitempty" jsonschema:"also list episodes that have not been broadcast yet; off by default, so the answer is what could be had"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "show_missing",
		Description: "Episodes a series has no file for. The run comes from the server's own records when it keeps them (stock Jellyfin needs the TheTVDB plugin and Emby 4.10 no longer imports them), else from TMDB read by the series' metadata provider id when EMBYFIN_TMDB_KEY is set. " +
			"Read 'supported' before 'missing': when it is false the run could not be established at all and 'missing' is null, which is unknown rather than complete. 'gaps_on_disk' is given either way and is weaker: it can only see episodes skipped between the files, never ones after the last episode held.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in missingIn) (*mcp.CallToolResult, missingOut, error) {
		out, err := showMissing(ctx, client, guide, in.SeriesID, in.Unaired)

		return nil, out, err
	})
}
