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
	runtime map[string]int // tmdb movie id -> minutes (0 = unknown), memoised per process
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
	u := baseURL + p
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return err
	}

	// v4 tokens are JWTs sent as a bearer; v3 keys go in the query string
	if strings.HasPrefix(c.key, "eyJ") {
		req.Header.Set("Authorization", "Bearer "+c.key)
	} else {
		q := req.URL.Query()
		q.Set("api_key", c.key)
		req.URL.RawQuery = q.Encode()
	}
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
