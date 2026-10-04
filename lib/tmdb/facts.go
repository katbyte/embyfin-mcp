package tmdb //nolint:revive // the package comment is in the generated doc.go, which lint skips

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/client"
)

// Facts is what the audits ask TMDB, through the generated Client, with each
// answer kept for factsTTL: a film's runtime and ids, a series' episodes,
// what another provider's id is at TMDB, and the titles a film or a series
// goes by and its search finds (titles.go). A sweep over a library asks each
// question once.
type Facts struct {
	api *Client
	// now is the clock, for a test to move; nil is the wall clock
	now func() time.Time

	mu       sync.Mutex
	movies   map[string]kept[Movie]     // tmdb movie id -> the film (ID 0 = unknown)
	guides   map[string]kept[guide]     // tmdb series id -> its seasons and episodes
	specials map[string]kept[[]Episode] // tmdb series id -> its season 0
	found    map[string]kept[Found]     // "<source>:<id>" -> what TMDB holds under it
	// the titles (titles.go)
	alternatives map[string]kept[[]string] // "movie:ID" or "tv:ID" -> the titles TMDB lists for it
	translations map[string]kept[[]string] // "movie:ID" or "tv:ID" -> its titles in TMDB's translations
	collected    map[string]kept[[]Hit]    // movie ID -> the films of the TMDB collection it is in
	collections  map[string]kept[[]Hit]    // collection ID -> its films
	searched     map[string]kept[[]Hit]    // "movie:title:year" -> what the search found
	unanswered   map[string]bool           // "movie:title:year" -> the search failed, and has not answered since
}

// factsTTL is how long an answer is kept. TMDB changes under a running
// session: an episode is listed once it is announced, a season is added, a
// wrong id is put right. Kept for the life of the process, a session that ran
// all day answered show_missing from its first read of a series and never
// saw an episode TMDB listed after it.
const factsTTL = time.Hour

// kept is an answer and when it was read.
type kept[T any] struct {
	value T
	at    time.Time
}

// guide is a series as TMDB lists it: the numbers of its seasons, specials
// included, and the episodes of every season but the specials.
type guide struct {
	seasons []int
	run     []Episode
}

// NewFacts answers from the TMDB API with a read access token or an API
// key. rt carries the requests, so a test can route them through a
// recording; nil is the default transport. A read TMDB fails in a way that
// passes is tried again (see Retrying).
func NewFacts(token string, rt http.RoundTripper) (*Facts, error) {
	api, err := New(DefaultBaseURL, token)
	if err != nil {
		return nil, err
	}
	api.Client.HTTPClient = HTTPClient(rt)

	return &Facts{
		api: api, movies: map[string]kept[Movie]{}, guides: map[string]kept[guide]{}, specials: map[string]kept[[]Episode]{}, found: map[string]kept[Found]{},
		alternatives: map[string]kept[[]string]{}, translations: map[string]kept[[]string]{},
		collected: map[string]kept[[]Hit]{}, collections: map[string]kept[[]Hit]{}, searched: map[string]kept[[]Hit]{}, unanswered: map[string]bool{},
	}, nil
}

func (f *Facts) clock() time.Time {
	if f.now != nil {
		return f.now()
	}

	return time.Now()
}

// recall is the answer kept for key, when there is one young enough to trust.
func recall[T any](f *Facts, memo map[string]kept[T], key string) (T, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	k, ok := memo[key]
	if !ok || f.clock().Sub(k.at) >= factsTTL {
		var zero T

		return zero, false
	}

	return k.value, true
}

// Clear forgets every answer kept, so the next read of each asks TMDB
// again, and says how many there were.
func (f *Facts) Clear() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	// the counts are taken before the maps are emptied: deferred calls run
	// after the return value is worked out
	defer func() {
		clear(f.movies)
		clear(f.guides)
		clear(f.specials)
		clear(f.found)
		clear(f.alternatives)
		clear(f.translations)
		clear(f.collected)
		clear(f.collections)
		clear(f.searched)
		clear(f.unanswered)
	}()

	return len(f.movies) + len(f.guides) + len(f.specials) + len(f.found) +
		len(f.alternatives) + len(f.translations) + len(f.collected) + len(f.collections) + len(f.searched) + len(f.unanswered)
}

// keep stores an answer as read now.
func keep[T any](f *Facts, memo map[string]kept[T], key string, value T) {
	f.mu.Lock()
	defer f.mu.Unlock()

	memo[key] = kept[T]{value: value, at: f.clock()}
}

// explain names the setting to check when TMDB refuses the credential.
func explain(what string, err error) error {
	if client.StatusCode(err) == http.StatusUnauthorized {
		return fmt.Errorf("tmdb %s: HTTP 401 (check EMBYFIN_TMDB_TOKEN)", what)
	}

	return fmt.Errorf("tmdb %s: %w", what, err)
}

// Movie is what TMDB holds for a film: enough to tell whether a library's
// ids for it agree, and how long it runs.
type Movie struct {
	ID          int
	Title       string
	ReleaseDate string
	Year        int // the year of ReleaseDate, 0 when TMDB has no date
	Runtime     int // minutes, 0 when TMDB has none
	IMDbID      string
}

// DateYear is the year of a date TMDB answers as a day (2006-01-02), 0 for
// none. A date written any other way is an error naming what carried it:
// read as no date, or as the wrong year, it would date a film or an episode
// wrongly without a word.
func DateYear(what, date string) (int, error) {
	if date == "" {
		return 0, nil
	}
	day, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return 0, fmt.Errorf("tmdb %s: a date that can't be read (%q)", what, date)
	}

	return day.Year(), nil
}

// Movie returns TMDB's film for a movie id; its ID is 0 when TMDB does not
// know the id, or it is not one.
func (f *Facts) Movie(ctx context.Context, id string) (Movie, error) {
	if m, ok := recall(f, f.movies, id); ok {
		return m, nil
	}

	var m Movie
	if n, err := strconv.Atoi(id); err == nil && n > 0 {
		res, err := f.api.MovieDetails(ctx, n, MovieDetailsOperationOptions{})
		switch {
		case client.IsNotFound(err):
		case err != nil:
			return Movie{}, explain("movie "+id, err)
		case res.Model != nil:
			d := res.Model
			year, err := DateYear("movie "+id, d.ReleaseDate)
			if err != nil {
				return Movie{}, err
			}
			m = Movie{ID: d.Id, Title: d.Title, ReleaseDate: d.ReleaseDate, Year: year, Runtime: d.Runtime, IMDbID: d.ImdbId}
		}
	}
	keep(f, f.movies, id, m)

	return m, nil
}

// MovieRuntime returns TMDB's runtime in minutes for a movie id, or 0 when
// TMDB does not know the film or has no runtime for it.
func (f *Facts) MovieRuntime(ctx context.Context, id string) (int, error) {
	m, err := f.Movie(ctx, id)

	return m.Runtime, err
}

// Found is what TMDB holds under another provider's id: the films, series
// and episodes it names. An IMDb id is any of the three, which is how a film
// matched to a series' or an episode's id shows itself.
type Found struct {
	Movies   []FindByIdResponseMovieResults
	Series   []FindByIdResponseTvResults
	Episodes []FindByIdResponseTvEpisodeResults
}

// Find returns what TMDB holds under another provider's id: source is a
// TMDB external source (imdb_id, tvdb_id). Nothing found is an empty Found,
// not an error.
func (f *Facts) Find(ctx context.Context, source, id string) (Found, error) {
	memo := source + ":" + id
	if found, ok := recall(f, f.found, memo); ok {
		return found, nil
	}

	var found Found
	res, err := f.api.FindById(ctx, id, FindByIdOperationOptions{ExternalSource: source})
	switch {
	case client.IsNotFound(err):
	case err != nil:
		return Found{}, explain("find "+id, err)
	case res.Model != nil:
		found = Found{Movies: res.Model.MovieResults, Series: res.Model.TvResults, Episodes: res.Model.TvEpisodeResults}
	}
	keep(f, f.found, memo, found)

	return found, nil
}

// SeriesID is the TMDB series id for a series known by another provider's
// id: source is a TMDB external source (tvdb_id, imdb_id). It returns "" for
// an id TMDB cannot place.
func (f *Facts) SeriesID(ctx context.Context, source, id string) (string, error) {
	found, err := f.Find(ctx, source, id)
	if err != nil || len(found.Series) == 0 {
		return "", err
	}

	return strconv.Itoa(found.Series[0].Id), nil
}

// Episode is one episode as TMDB lists it, which is what an episode guide
// needs: where it sits and what it is called.
type Episode struct {
	Season  int
	Episode int
	Name    string
	AirDate string // a day (2006-01-02), checked when TMDB's answer is read, or empty
	Runtime int    // minutes, the episode's own; 0 when TMDB has none
}

// SeriesEpisodes lists every episode TMDB knows for a series id, season by
// season, in TMDB's aired order, skipping the specials (season 0, which has
// no run to be missing from: SeriesSpecials reads it). It returns nothing,
// and no error, for a series TMDB does not know.
//
// TMDB answers a series with its season numbers and an episode count each,
// and only the per-season read carries the episodes, so this costs one
// request plus one per season.
func (f *Facts) SeriesEpisodes(ctx context.Context, id string) ([]Episode, error) {
	g, err := f.series(ctx, id)

	return g.run, err
}

// SeriesSpecials lists the specials TMDB knows for a series id: its season 0.
// It returns nothing, and no error, for a series TMDB does not know or that
// has no specials. It costs one request more than SeriesEpisodes, and none
// when TMDB lists no season 0.
func (f *Facts) SeriesSpecials(ctx context.Context, id string) ([]Episode, error) {
	g, err := f.series(ctx, id)
	if err != nil || !slices.Contains(g.seasons, 0) {
		return nil, err
	}
	if eps, ok := recall(f, f.specials, id); ok {
		return eps, nil
	}
	// series answered a season list, so id is a TMDB number
	n, err := strconv.Atoi(id)
	if err != nil {
		return nil, err
	}
	eps, err := f.season(ctx, id, n, 0)
	if err != nil {
		return nil, err
	}
	keep(f, f.specials, id, eps)

	return eps, nil
}

// series reads a series' season numbers and the episodes of all but its
// specials.
func (f *Facts) series(ctx context.Context, id string) (guide, error) {
	if g, ok := recall(f, f.guides, id); ok {
		return g, nil
	}

	g := guide{run: []Episode{}}
	if n, err := strconv.Atoi(id); err == nil && n > 0 {
		series, err := f.api.TvSeriesDetails(ctx, n, TvSeriesDetailsOperationOptions{})
		switch {
		case client.IsNotFound(err):
		case err != nil:
			return guide{}, explain("tv "+id, err)
		case series.Model != nil:
			for _, s := range series.Model.Seasons {
				g.seasons = append(g.seasons, s.SeasonNumber)
				if s.SeasonNumber <= 0 {
					continue
				}
				eps, err := f.season(ctx, id, n, s.SeasonNumber)
				if err != nil {
					return guide{}, err
				}
				g.run = append(g.run, eps...)
			}
		}
	}

	// a series TMDB does not know is kept as nothing too, so a sweep over a
	// library of unmatched series asks once each
	keep(f, f.guides, id, g)

	return g, nil
}

// season reads one season's episodes: none for a season TMDB lists and then
// has no record for.
func (f *Facts) season(ctx context.Context, id string, n, number int) ([]Episode, error) {
	season, err := f.api.TvSeasonDetails(ctx, n, number, TvSeasonDetailsOperationOptions{})
	switch {
	case client.IsNotFound(err):
		return []Episode{}, nil
	case err != nil:
		return nil, explain(fmt.Sprintf("tv %s season %d", id, number), err)
	case season.Model == nil:
		return []Episode{}, nil
	}
	out := make([]Episode, 0, len(season.Model.Episodes))
	for _, e := range season.Model.Episodes {
		// a season read answers with its own number, but a series whose
		// seasons TMDB has renumbered has answered with another
		at := e.SeasonNumber
		if at == 0 {
			at = number
		}
		if _, err := DateYear(fmt.Sprintf("tv %s season %d episode %d", id, at, e.EpisodeNumber), e.AirDate); err != nil {
			return nil, err
		}
		out = append(out, Episode{Season: at, Episode: e.EpisodeNumber, Name: e.Name, AirDate: e.AirDate, Runtime: e.Runtime})
	}

	return out, nil
}
