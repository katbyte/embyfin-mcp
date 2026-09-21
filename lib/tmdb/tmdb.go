// Package tmdb is a minimal client for The Movie Database API, used by audits
// that need the metadata provider's own facts (such as a film's runtime) which
// the media server does not retain once it has probed the file.
package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const baseURL = "https://api.themoviedb.org/3"

// Client talks to TMDB with either a v3 API key or a v4 read access token.
type Client struct {
	key  string
	http *http.Client

	mu     sync.Mutex
	movies map[string]Movie     // tmdb movie id -> the film (ID 0 = unknown), memoised per process
	guides map[string][]Episode // tmdb series id -> its episodes, memoised per process
	found  map[string]Found     // "<source>:<id>" -> what TMDB holds under it, memoised per process
}

// New returns a client; key may be a v3 API key or a v4 read access token (JWT).
func New(key string) *Client {
	return NewWithTransport(key, nil)
}

// NewWithTransport is New with the transport the requests go through, so a
// test can route them via a record/replay proxy; nil is the default
// transport.
func NewWithTransport(key string, rt http.RoundTripper) *Client {
	return &Client{
		key:    key,
		http:   &http.Client{Timeout: 15 * time.Second, Transport: rt},
		movies: map[string]Movie{},
		guides: map[string][]Episode{},
		found:  map[string]Found{},
	}
}

// Movie is what TMDB holds for a film: enough to tell whether a library's
// ids for it agree, and how long it runs.
type Movie struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	ReleaseDate string `json:"release_date"`
	Runtime     int    `json:"runtime"` // minutes, 0 when TMDB has none
	IMDbID      string `json:"imdb_id"`
}

// Year is the year TMDB dates the film to, 0 when it has no date.
func (m Movie) Year() int {
	year, _ := strconv.Atoi(m.ReleaseDate[:min(len(m.ReleaseDate), 4)])

	return year
}

// Movie returns TMDB's film for a movie id; its ID is 0 when TMDB does not
// know the id.
func (c *Client) Movie(ctx context.Context, id string) (Movie, error) {
	c.mu.Lock()
	m, ok := c.movies[id]
	c.mu.Unlock()
	if ok {
		return m, nil
	}

	if err := c.get(ctx, "/movie/"+url.PathEscape(id), &m); err != nil {
		return Movie{}, err
	}

	c.mu.Lock()
	c.movies[id] = m
	c.mu.Unlock()

	return m, nil
}

// MovieRuntime returns TMDB's runtime in minutes for a movie id, or 0 when
// TMDB does not know the film or has no runtime for it.
func (c *Client) MovieRuntime(ctx context.Context, id string) (int, error) {
	m, err := c.Movie(ctx, id)

	return m.Runtime, err
}

func (c *Client) get(ctx context.Context, p string, into any) error {
	return c.getQuery(ctx, p, nil, into)
}

func (c *Client) getQuery(ctx context.Context, p string, q url.Values, into any) error {
	u := baseURL + p
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return err
	}

	if q == nil {
		q = url.Values{}
	}
	// v4 tokens are JWTs sent as a bearer; v3 keys go in the query string
	if strings.HasPrefix(c.key, "eyJ") {
		req.Header.Set("Authorization", "Bearer "+c.key)
	} else {
		q.Set("api_key", c.key)
	}
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tmdb GET %s: %w", p, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil // unknown id: leave the zero value
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("tmdb GET %s: HTTP 401 (check EMBYFIN_TMDB_TOKEN)", p)
	case resp.StatusCode >= 300:
		return fmt.Errorf("tmdb GET %s: HTTP %d", p, resp.StatusCode)
	}

	return json.NewDecoder(resp.Body).Decode(into)
}

// Episode is one episode as TMDB lists it, which is what an episode guide
// needs: where it sits and what it is called.
type Episode struct {
	Season  int    `json:"season_number"`
	Episode int    `json:"episode_number"`
	Name    string `json:"name"`
	AirDate string `json:"air_date"`
}

// SeriesEpisodes lists every episode TMDB knows for a series id, season by
// season, skipping the specials (season 0, which has no run to be missing
// from). It returns nothing, and no error, for a series TMDB does not know.
//
// TMDB answers a series with its season numbers and an episode count each,
// and only the per-season read carries the episodes, so this costs one
// request plus one per season. The answer is memoised per process.
func (c *Client) SeriesEpisodes(ctx context.Context, id string) ([]Episode, error) {
	c.mu.Lock()
	eps, ok := c.guides[id]
	c.mu.Unlock()
	if ok {
		return eps, nil
	}

	var series struct {
		ID      int `json:"id"`
		Seasons []struct {
			Number int `json:"season_number"`
		} `json:"seasons"`
	}
	if err := c.get(ctx, "/tv/"+url.PathEscape(id), &series); err != nil {
		return nil, err
	}

	out := []Episode{}
	for _, s := range series.Seasons {
		if s.Number <= 0 {
			continue
		}
		var season struct {
			Episodes []Episode `json:"episodes"`
		}
		if err := c.get(ctx, fmt.Sprintf("/tv/%s/season/%d", url.PathEscape(id), s.Number), &season); err != nil {
			return nil, err
		}
		for _, e := range season.Episodes {
			// a season read answers with its own number, but a series whose
			// seasons TMDB has renumbered has answered with another
			if e.Season == 0 {
				e.Season = s.Number
			}
			out = append(out, e)
		}
	}

	// a series TMDB does not know is memoised as nothing too, so a sweep
	// over a library of unmatched series asks once each
	c.mu.Lock()
	c.guides[id] = out
	c.mu.Unlock()

	return out, nil
}

// Found is what TMDB holds under another provider's id: the films, series
// and episodes it names. An IMDb id is any of the three, which is how a film
// matched to a series' or an episode's id shows itself.
type Found struct {
	Movies []struct {
		ID          int    `json:"id"`
		Title       string `json:"title"`
		ReleaseDate string `json:"release_date"`
	} `json:"movie_results"`
	Series []struct {
		ID           int    `json:"id"`
		Name         string `json:"name"`
		FirstAirDate string `json:"first_air_date"`
	} `json:"tv_results"`
	Episodes []struct {
		ID      int    `json:"id"`
		Name    string `json:"name"`
		ShowID  int    `json:"show_id"`
		Season  int    `json:"season_number"`
		Episode int    `json:"episode_number"`
	} `json:"tv_episode_results"`
}

// Find returns what TMDB holds under another provider's id: source is a
// TMDB external source (imdb_id, tvdb_id). Nothing found is an empty Found,
// not an error.
func (c *Client) Find(ctx context.Context, source, id string) (Found, error) {
	memo := source + ":" + id
	c.mu.Lock()
	f, ok := c.found[memo]
	c.mu.Unlock()
	if ok {
		return f, nil
	}

	if err := c.getQuery(ctx, "/find/"+url.PathEscape(id), url.Values{"external_source": {source}}, &f); err != nil {
		return Found{}, err
	}

	c.mu.Lock()
	c.found[memo] = f
	c.mu.Unlock()

	return f, nil
}

// SeriesID is the TMDB series id for a series known by another provider's
// id: source is a TMDB external source (tvdb_id, imdb_id). It returns "" for
// an id TMDB cannot place.
func (c *Client) SeriesID(ctx context.Context, source, id string) (string, error) {
	f, err := c.Find(ctx, source, id)
	if err != nil || len(f.Series) == 0 {
		return "", err
	}

	return strconv.Itoa(f.Series[0].ID), nil
}
