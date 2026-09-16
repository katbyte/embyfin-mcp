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

	mu      sync.Mutex
	runtime map[string]int       // tmdb movie id -> minutes (0 = unknown), memoised per process
	guides  map[string][]Episode // tmdb series id -> its episodes, memoised per process
	series  map[string]string    // "<source>:<id>" -> tmdb series id ("" = TMDB cannot place it)
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
		key:     key,
		http:    &http.Client{Timeout: 15 * time.Second, Transport: rt},
		runtime: map[string]int{},
		guides:  map[string][]Episode{},
		series:  map[string]string{},
	}
}

// MovieRuntime returns TMDB's runtime in minutes for a movie id, or 0 when
// TMDB does not know the film or has no runtime for it.
func (c *Client) MovieRuntime(ctx context.Context, id string) (int, error) {
	c.mu.Lock()
	minutes, ok := c.runtime[id]
	c.mu.Unlock()
	if ok {
		return minutes, nil
	}

	var out struct {
		Runtime int `json:"runtime"`
	}
	if err := c.get(ctx, "/movie/"+url.PathEscape(id), &out); err != nil {
		return 0, err
	}

	c.mu.Lock()
	c.runtime[id] = out.Runtime
	c.mu.Unlock()

	return out.Runtime, nil
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
		return fmt.Errorf("tmdb GET %s: HTTP 401 (check EMBYFIN_TMDB_KEY)", p)
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

// SeriesID is the TMDB series id for a series known by another provider's
// id: source is a TMDB external source (tvdb_id, imdb_id). It returns "" for
// an id TMDB cannot place.
func (c *Client) SeriesID(ctx context.Context, source, id string) (string, error) {
	memo := source + ":" + id
	c.mu.Lock()
	found, ok := c.series[memo]
	c.mu.Unlock()
	if ok {
		return found, nil
	}

	var out struct {
		TV []struct {
			ID int `json:"id"`
		} `json:"tv_results"`
	}
	if err := c.getQuery(ctx, "/find/"+url.PathEscape(id), url.Values{"external_source": {source}}, &out); err != nil {
		return "", err
	}
	found = ""
	if len(out.TV) > 0 {
		found = strconv.Itoa(out.TV[0].ID)
	}

	c.mu.Lock()
	c.series[memo] = found
	c.mu.Unlock()

	return found, nil
}
