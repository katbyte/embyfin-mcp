//go:build integration

package integration

// TMDB's read surface, swept the way the servers' are: every GET in its
// definitions called against the real API through the recording proxy, and
// its answer decoded into the generated model. That is what shows that the
// definitions, drawn from the examples in TMDB's own documentation, are right
// about what TMDB answers.
//
// TMDB is not a server the suite runs, so this needs no container and keeps
// its own cassettes: the answers are recorded once with a token
// (EMBYFIN_TEST_RECORD=1 and EMBYFIN_TMDB_TOKEN) into testdata/cassettes/tmdb,
// and replayed after with no token and no network.

import (
	"cmp"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/providerproxy"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
)

// The ids the sweep starts from, which TMDB's own documentation uses: Fight
// Club, Game of Thrones, Brad Pitt and the Star Wars collection.
const (
	tmdbFilm       = 550
	tmdbSeries     = 1399
	tmdbPerson     = 287
	tmdbCollection = 10
)

// tmdbCases are the GETs the sweep does not call, and why.
var tmdbCases = func() map[string]sweepCase {
	session := sweepCase{Skip: "needs a signed-in user's session, and answers with that user's account: nothing the suite may hold or publish"}
	cases := map[string]sweepCase{}
	for _, name := range []string{
		"AccountDetails", "AccountFavoriteTv", "AccountGetFavorites", "AccountLists", "AccountRatedMovies", "AccountRatedTv",
		"AccountRatedTvEpisodes", "AccountWatchlistMovies", "AccountWatchlistTv",
		"MovieAccountStates", "TvEpisodeAccountStates", "TvSeasonAccountStates", "TvSeriesAccountStates",
	} {
		cases[name] = session
	}
	// the suite rates nothing on TMDB, where a guest's rating counts toward
	// the film's, and a guest session that has rated nothing is not found
	for _, name := range []string{"GuestSessionRatedMovies", "GuestSessionRatedTv", "GuestSessionRatedTvEpisodes"} {
		cases[name] = sweepCase{Status: http.StatusNotFound, Why: "a guest session that has rated nothing answers 404"}
	}
	// a changes route lists the last day's, and the day these were recorded
	// the person and the episode had none
	cases["PersonChanges"] = sweepCase{Empty: "Brad Pitt had no change the day it was recorded"}
	cases["TvEpisodeChangesById"] = sweepCase{Empty: "the episode had no change the day it was recorded"}

	return cases
}()

func TestTMDBSweep(t *testing.T) {
	mode, token := providerproxy.Replay, "replay" // the proxy leaves api_key out of a match
	switch {
	case recording(), verifying():
		token = cmp.Or(os.Getenv("EMBYFIN_TMDB_TOKEN"), os.Getenv("EMBYFIN_TMDB_KEY"))
		if token == "" {
			t.Skip("recording TMDB needs EMBYFIN_TMDB_TOKEN")
		}
		mode = providerproxy.Record
		if verifying() {
			mode = providerproxy.Verify
		}
	}
	p, err := providerproxy.New(providerproxy.Options{
		Mode:        mode,
		CassetteDir: filepath.Join("testdata", "cassettes", "tmdb"),
		Addr:        "127.0.0.1:0",
		RedactQuery: []string{"api_key"},
		// a new request token is a credential for a moment, and the cassette
		// should not hold even that
		RedactBodyFields: []string{"request_token"},
		Logger:           log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		misses, drifts := p.Misses(), p.Drifts()
		_ = p.Close()
		if len(misses) > 0 {
			t.Errorf("%d request(s) had no recording; record them with EMBYFIN_TEST_RECORD=1 EMBYFIN_TMDB_TOKEN=...: %v", len(misses), misses)
		}
		for _, d := range drifts {
			t.Errorf("TMDB's answer changed shape since it was recorded: %s", d)
		}
	})

	c, err := tmdb.New(tmdb.DefaultBaseURL, token)
	if err != nil {
		t.Fatal(err)
	}
	c.Client.HTTPClient = &http.Client{Timeout: time.Minute, Transport: p.Transport()}

	fixtures := tmdbFixtures(t, c)
	sweep(t, "tmdb", c, fixtures, tmdbCases)
}

// tmdbFixtures resolves the ids no example in the document gives: the fixed
// ones above, and the rest read off TMDB's own answers about them.
func tmdbFixtures(t *testing.T, c *tmdb.Client) sweepFixtures {
	t.Helper()

	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	id := strconv.Itoa

	film, err := c.MovieDetails(ctx, tmdbFilm, tmdb.MovieDetailsOperationOptions{})
	must(err)
	credits, err := c.MovieCredits(ctx, tmdbFilm, tmdb.MovieCreditsOperationOptions{})
	must(err)
	reviews, err := c.MovieReviews(ctx, tmdbFilm, tmdb.MovieReviewsOperationOptions{})
	must(err)
	lists, err := c.MovieLists(ctx, tmdbFilm, tmdb.MovieListsOperationOptions{})
	must(err)
	keywords, err := c.MovieKeywords(ctx, tmdbFilm)
	must(err)
	series, err := c.TvSeriesDetails(ctx, tmdbSeries, tmdb.TvSeriesDetailsOperationOptions{})
	must(err)
	season, err := c.TvSeasonDetails(ctx, tmdbSeries, 1, tmdb.TvSeasonDetailsOperationOptions{})
	must(err)
	episode, err := c.TvEpisodeDetails(ctx, tmdbSeries, 1, 1, tmdb.TvEpisodeDetailsOperationOptions{})
	must(err)
	groups, err := c.TvSeriesEpisodeGroups(ctx, tmdbSeries)
	must(err)
	guest, err := c.AuthenticationCreateGuestSession(ctx)
	must(err)

	if len(film.Model.ProductionCompanies) == 0 || len(credits.Model.Cast) == 0 || len(reviews.Model.Results) == 0 || len(lists.Model.Results) == 0 ||
		len(keywords.Model.Keywords) == 0 || len(series.Model.Networks) == 0 || len(groups.Model.Results) == 0 {
		t.Fatal("TMDB's answers about the starting ids no longer hold the ids the sweep reads off them")
	}

	return sweepFixtures{
		path: map[string]string{
			"movie_id":            id(tmdbFilm),
			"series_id":           id(tmdbSeries),
			"season_number":       "1",
			"episode_number":      "1",
			"person_id":           id(tmdbPerson),
			"collection_id":       id(tmdbCollection),
			"company_id":          id(film.Model.ProductionCompanies[0].Id),
			"network_id":          id(series.Model.Networks[0].Id),
			"keyword_id":          id(keywords.Model.Keywords[0].Id),
			"credit_id":           credits.Model.Cast[0].CreditId,
			"review_id":           reviews.Model.Results[0].Id,
			"list_id":             id(lists.Model.Results[0].Id),
			"season_id":           id(season.Model.Id),
			"episode_id":          id(episode.Model.Id),
			"tv_episode_group_id": groups.Model.Results[0].Id,
			"guest_session_id":    guest.Model.GuestSessionId,
			"external_id":         film.Model.ImdbId,
			"time_window":         "day",
		},
		options: map[string]string{
			"query":           "star wars",
			"external_source": "imdb_id",
		},
	}
}
