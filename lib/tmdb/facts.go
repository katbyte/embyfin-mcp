package tmdb //nolint:revive // the package comment is in the generated doc.go, which lint skips

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/client"
)

// Facts is what the audits ask TMDB, through the generated Client, with each
// answer kept for the life of the process: a film's runtime and ids, a
// series' episodes, what another provider's id is at TMDB. A sweep over a
// library asks each question once.
type Facts struct {
	api *Client

	mu     sync.Mutex
	movies map[string]Movie     // tmdb movie id -> the film (ID 0 = unknown)
	guides map[string][]Episode // tmdb series id -> its episodes
	found  map[string]Found     // "<source>:<id>" -> what TMDB holds under it
}

// NewFacts answers from the TMDB API with a read access token or an API
// key. rt carries the requests, so a test can route them through a
// recording; nil is the default transport.
func NewFacts(token string, rt http.RoundTripper) (*Facts, error) {
	api, err := New(DefaultBaseURL, token)
	if err != nil {
		return nil, err
	}
	api.Client.HTTPClient = &http.Client{Timeout: 15 * time.Second, Transport: rt}

	return &Facts{api: api, movies: map[string]Movie{}, guides: map[string][]Episode{}, found: map[string]Found{}}, nil
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
	Runtime     int // minutes, 0 when TMDB has none
	IMDbID      string
}

// Year is the year TMDB dates the film to, 0 when it has no date.
func (m Movie) Year() int {
	year, _ := strconv.Atoi(m.ReleaseDate[:min(len(m.ReleaseDate), 4)])

	return year
}

// Movie returns TMDB's film for a movie id; its ID is 0 when TMDB does not
// know the id, or it is not one.
func (f *Facts) Movie(ctx context.Context, id string) (Movie, error) {
	f.mu.Lock()
	m, ok := f.movies[id]
	f.mu.Unlock()
	if ok {
		return m, nil
	}

	if n, err := strconv.Atoi(id); err == nil && n > 0 {
		res, err := f.api.MovieDetails(ctx, n, MovieDetailsOperationOptions{})
		switch {
		case client.IsNotFound(err):
		case err != nil:
			return Movie{}, explain("movie "+id, err)
		case res.Model != nil:
			d := res.Model
			m = Movie{ID: d.Id, Title: d.Title, ReleaseDate: d.ReleaseDate, Runtime: d.Runtime, IMDbID: d.ImdbId}
		}
	}

	f.mu.Lock()
	f.movies[id] = m
	f.mu.Unlock()

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
	f.mu.Lock()
	found, ok := f.found[memo]
	f.mu.Unlock()
	if ok {
		return found, nil
	}

	res, err := f.api.FindById(ctx, id, FindByIdOperationOptions{ExternalSource: source})
	switch {
	case client.IsNotFound(err):
	case err != nil:
		return Found{}, explain("find "+id, err)
	case res.Model != nil:
		found = Found{Movies: res.Model.MovieResults, Series: res.Model.TvResults, Episodes: res.Model.TvEpisodeResults}
	}

	f.mu.Lock()
	f.found[memo] = found
	f.mu.Unlock()

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
	AirDate string
}

// SeriesEpisodes lists every episode TMDB knows for a series id, season by
// season, skipping the specials (season 0, which has no run to be missing
// from). It returns nothing, and no error, for a series TMDB does not know.
//
// TMDB answers a series with its season numbers and an episode count each,
// and only the per-season read carries the episodes, so this costs one
// request plus one per season.
func (f *Facts) SeriesEpisodes(ctx context.Context, id string) ([]Episode, error) {
	f.mu.Lock()
	eps, ok := f.guides[id]
	f.mu.Unlock()
	if ok {
		return eps, nil
	}

	out := []Episode{}
	if n, err := strconv.Atoi(id); err == nil && n > 0 {
		series, err := f.api.TvSeriesDetails(ctx, n, TvSeriesDetailsOperationOptions{})
		switch {
		case client.IsNotFound(err):
		case err != nil:
			return nil, explain("tv "+id, err)
		case series.Model != nil:
			for _, s := range series.Model.Seasons {
				if s.SeasonNumber <= 0 {
					continue
				}
				season, err := f.api.TvSeasonDetails(ctx, n, s.SeasonNumber, TvSeasonDetailsOperationOptions{})
				if err != nil {
					return nil, explain(fmt.Sprintf("tv %s season %d", id, s.SeasonNumber), err)
				}
				if season.Model == nil {
					continue
				}
				for _, e := range season.Model.Episodes {
					// a season read answers with its own number, but a series
					// whose seasons TMDB has renumbered has answered with another
					number := e.SeasonNumber
					if number == 0 {
						number = s.SeasonNumber
					}
					out = append(out, Episode{Season: number, Episode: e.EpisodeNumber, Name: e.Name, AirDate: e.AirDate})
				}
			}
		}
	}

	// a series TMDB does not know is kept as nothing too, so a sweep over a
	// library of unmatched series asks once each
	f.mu.Lock()
	f.guides[id] = out
	f.mu.Unlock()

	return out, nil
}
