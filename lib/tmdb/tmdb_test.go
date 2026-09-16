package tmdb

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// rewrite is a transport that sends every request to a local server in place
// of api.themoviedb.org, keeping the path and query the client built.
type rewrite struct{ target *url.URL }

func (rw rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.URL.Scheme, clone.URL.Host = rw.target.Scheme, rw.target.Host

	return http.DefaultTransport.RoundTrip(clone)
}

// newTMDB serves a canned TMDB behind the rewrite, so the client's real URL
// building is exercised against a local server.
func newTMDB(t *testing.T, handle http.HandlerFunc) (client *Client, calls *int32) {
	t.Helper()

	calls = new(int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		handle(w, r)
	}))
	t.Cleanup(srv.Close)

	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return NewWithTransport("v3-key", rewrite{target}), calls
}

func TestMovieRuntimeSendsTheKeyAndMemoises(t *testing.T) {
	t.Parallel()

	c, calls := newTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/movie/348" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("api_key") != "v3-key" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":348,"runtime":117}`))
	})

	for range 3 {
		minutes, err := c.MovieRuntime(t.Context(), "348")
		if err != nil {
			t.Fatal(err)
		}
		if minutes != 117 {
			t.Errorf("runtime = %d, want 117", minutes)
		}
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("TMDB was asked %d times for the same film, want 1", n)
	}

	// an unknown film is 0, not an error, and is remembered too
	if minutes, err := c.MovieRuntime(t.Context(), "999999"); err != nil || minutes != 0 {
		t.Errorf("unknown film = %d, %v", minutes, err)
	}
	if _, err := c.MovieRuntime(t.Context(), "999999"); err != nil {
		t.Error(err)
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Errorf("TMDB was asked %d times in all, want 2", n)
	}
}

// A v4 read access token is a JWT and goes in the Authorization header
// rather than the query string.
func TestReadAccessTokenIsABearer(t *testing.T) {
	t.Parallel()

	var auth, key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, key = r.Header.Get("Authorization"), r.URL.Query().Get("api_key")
		_, _ = w.Write([]byte(`{"runtime":90}`))
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	c := NewWithTransport("eyJhbGciOiJIUzI1NiJ9.token", rewrite{target})

	if _, err := c.MovieRuntime(t.Context(), "1"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer eyJhbGciOiJIUzI1NiJ9.token" || key != "" {
		t.Errorf("auth = %q, api_key = %q", auth, key)
	}
}

func TestErrorsNameTheKey(t *testing.T) {
	t.Parallel()

	c, _ := newTMDB(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	})
	if _, err := c.MovieRuntime(t.Context(), "1"); err == nil || !contains(err.Error(), "EMBYFIN_TMDB_KEY") {
		t.Errorf("a 401 should say which key to check: %v", err)
	}

	c, _ = newTMDB(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	})
	if _, err := c.MovieRuntime(t.Context(), "1"); err == nil || !contains(err.Error(), "502") {
		t.Errorf("a 502 should be reported: %v", err)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
