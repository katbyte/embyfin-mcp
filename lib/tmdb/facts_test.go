package tmdb

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
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
func newTMDB(t *testing.T, handle http.HandlerFunc) (facts *Facts, calls *int32) {
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
	facts, err = NewFacts("v3-key", rewrite{target})
	if err != nil {
		t.Fatal(err)
	}

	return facts, calls
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
	c, err := NewFacts("eyJhbGciOiJIUzI1NiJ9.token", rewrite{target})
	if err != nil {
		t.Fatal(err)
	}

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
	if _, err := c.MovieRuntime(t.Context(), "1"); err == nil || !contains(err.Error(), "EMBYFIN_TMDB_TOKEN") {
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

// tvHandler serves a canned TV series: its season list, and the episodes of
// each season, as TMDB lays them out.
func tvHandler(t *testing.T, id string, seasons map[int]int) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "v3-key" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/3/tv/"+id:
			numbers := make([]int, 0, len(seasons))
			for n := range seasons {
				numbers = append(numbers, n)
			}
			slices.Sort(numbers)
			rows := make([]string, 0, len(numbers))
			for _, n := range numbers {
				rows = append(rows, fmt.Sprintf(`{"season_number":%d,"episode_count":%d}`, n, seasons[n]))
			}
			_, _ = fmt.Fprintf(w, `{"id":%s,"name":"Canned","seasons":[%s]}`, id, strings.Join(rows, ","))
		case strings.HasPrefix(r.URL.Path, "/3/tv/"+id+"/season/"):
			n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/3/tv/"+id+"/season/"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			eps := make([]string, 0, seasons[n])
			for i := 1; i <= seasons[n]; i++ {
				eps = append(eps, fmt.Sprintf(`{"season_number":%d,"episode_number":%d,"name":"S%dE%d","air_date":"2022-01-%02d"}`, n, i, n, i, i))
			}
			_, _ = fmt.Fprintf(w, `{"season_number":%d,"episodes":[%s]}`, n, strings.Join(eps, ","))
		default:
			http.NotFound(w, r)
		}
	}
}

// SeriesEpisodes reads the whole run season by season, leaves the specials
// out, and asks TMDB once per series however often it is called.
func TestSeriesEpisodes(t *testing.T) {
	t.Parallel()

	c, calls := newTMDB(t, tvHandler(t, "95396", map[int]int{0: 2, 1: 3, 2: 2}))

	run, err := c.SeriesEpisodes(t.Context(), "95396")
	if err != nil {
		t.Fatal(err)
	}
	want := []Episode{
		{Season: 1, Episode: 1, Name: "S1E1", AirDate: "2022-01-01"},
		{Season: 1, Episode: 2, Name: "S1E2", AirDate: "2022-01-02"},
		{Season: 1, Episode: 3, Name: "S1E3", AirDate: "2022-01-03"},
		{Season: 2, Episode: 1, Name: "S2E1", AirDate: "2022-01-01"},
		{Season: 2, Episode: 2, Name: "S2E2", AirDate: "2022-01-02"},
	}
	if !slices.Equal(run, want) {
		t.Errorf("run = %v, want %v", run, want)
	}
	// the series plus one read per season, and season 0 never asked for
	if n := atomic.LoadInt32(calls); n != 3 {
		t.Errorf("TMDB was asked %d times, want 3 (the series and seasons 1 and 2)", n)
	}

	if _, err := c.SeriesEpisodes(t.Context(), "95396"); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(calls); n != 3 {
		t.Errorf("a second read asked TMDB again: %d calls", n)
	}
}

// A series TMDB does not know is nothing rather than an error, and is
// remembered as nothing so a sweep asks once.
func TestSeriesEpisodesUnknown(t *testing.T) {
	t.Parallel()

	c, calls := newTMDB(t, tvHandler(t, "95396", map[int]int{1: 1}))

	for range 2 {
		run, err := c.SeriesEpisodes(t.Context(), "404404")
		if err != nil {
			t.Fatal(err)
		}
		if len(run) != 0 {
			t.Errorf("unknown series = %v", run)
		}
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("an unknown series was asked for %d times, want 1", n)
	}
}

// SeriesID places a series known by another provider's id, and remembers
// both an answer and the lack of one.
func TestSeriesID(t *testing.T) {
	t.Parallel()

	c, calls := newTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/find/371980" {
			_, _ = w.Write([]byte(`{"tv_results":[]}`))
			return
		}
		if src := r.URL.Query().Get("external_source"); src != "tvdb_id" {
			http.Error(w, "external_source = "+src, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"movie_results":[],"tv_results":[{"id":95396,"name":"Severance"}]}`))
	})

	for range 2 {
		id, err := c.SeriesID(t.Context(), "tvdb_id", "371980")
		if err != nil {
			t.Fatal(err)
		}
		if id != "95396" {
			t.Errorf("series id = %q, want 95396", id)
		}
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("TMDB was asked %d times for the same id, want 1", n)
	}

	// an id TMDB cannot place is "" rather than an error
	id, err := c.SeriesID(t.Context(), "imdb_id", "tt0000000")
	if err != nil || id != "" {
		t.Errorf("unplaceable id = %q, %v", id, err)
	}
}

// A film's facts come in the one request the runtime already makes, so the
// two share it.
func TestMovie(t *testing.T) {
	t.Parallel()

	c, calls := newTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/movie/348" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"id":348,"title":"Alien","release_date":"1979-05-25","runtime":117,"imdb_id":"tt0078748"}`))
	})

	m, err := c.Movie(t.Context(), "348")
	if err != nil || m.ID != 348 || m.Title != "Alien" || m.Year() != 1979 || m.IMDbID != "tt0078748" {
		t.Fatalf("movie = %+v, %v", m, err)
	}
	if minutes, err := c.MovieRuntime(t.Context(), "348"); err != nil || minutes != 117 {
		t.Errorf("runtime = %d, %v", minutes, err)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("TMDB was asked %d times for one film, want 1", n)
	}

	// an id TMDB does not know is a film with no id, not an error
	if m, err := c.Movie(t.Context(), "999999"); err != nil || m.ID != 0 || m.Year() != 0 {
		t.Errorf("unknown film = %+v, %v", m, err)
	}
}

// An IMDb id can name a film, a series or an episode, and TMDB says which.
func TestFind(t *testing.T) {
	t.Parallel()

	c, calls := newTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		if src := r.URL.Query().Get("external_source"); src != "imdb_id" {
			http.Error(w, "external_source = "+src, http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/3/find/tt9000001":
			_, _ = w.Write([]byte(`{"movie_results":[{"id":9001,"title":"Zzyzx","release_date":"2001-01-01"}],"tv_results":[],"tv_episode_results":[]}`))
		case "/3/find/tt9000002":
			_, _ = w.Write([]byte(`{"movie_results":[],"tv_results":[{"id":9002,"name":"Zzyzx Show","first_air_date":"2014-01-19"}],"tv_episode_results":[]}`))
		case "/3/find/tt9000003":
			_, _ = w.Write([]byte(`{"movie_results":[],"tv_results":[],"tv_episode_results":[{"id":99,"name":"Episode 21","show_id":9003,"season_number":1,"episode_number":21}]}`))
		default:
			_, _ = w.Write([]byte(`{"movie_results":[],"tv_results":[],"tv_episode_results":[]}`))
		}
	})
	ctx := t.Context()

	if f, err := c.Find(ctx, "imdb_id", "tt9000001"); err != nil || len(f.Movies) != 1 || f.Movies[0].Id != 9001 || len(f.Series) != 0 {
		t.Errorf("a film = %+v, %v", f, err)
	}
	if f, err := c.Find(ctx, "imdb_id", "tt9000002"); err != nil || len(f.Series) != 1 || f.Series[0].Name != "Zzyzx Show" {
		t.Errorf("a series = %+v, %v", f, err)
	}
	if f, err := c.Find(ctx, "imdb_id", "tt9000003"); err != nil || len(f.Episodes) != 1 || f.Episodes[0].ShowId != 9003 || f.Episodes[0].EpisodeNumber != 21 {
		t.Errorf("an episode = %+v, %v", f, err)
	}
	if f, err := c.Find(ctx, "imdb_id", "tt0000000"); err != nil || len(f.Movies)+len(f.Series)+len(f.Episodes) != 0 {
		t.Errorf("nothing = %+v, %v", f, err)
	}
	// SeriesID answers from the same lookup
	if id, err := c.SeriesID(ctx, "imdb_id", "tt9000002"); err != nil || id != "9002" {
		t.Errorf("series id = %q, %v", id, err)
	}
	if n := atomic.LoadInt32(calls); n != 4 {
		t.Errorf("TMDB was asked %d times, want once per id", n)
	}
}
