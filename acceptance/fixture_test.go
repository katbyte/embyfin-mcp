//go:build integration

package acceptance

import (
	"fmt"
	"slices"
	"testing"
)

// The catalogue the script laid out is what the server holds: every clean
// film and show with the ids, genre and director its nfo carried, which is
// what every other test here assumes.
func TestFixturesAreWhatTheScriptLaidOut(t *testing.T) {
	for _, m := range movies {
		id := findItem(t, "Movies", "Movie", m.Title)
		got := call(t, "item_get", map[string]any{"id": id})
		ids, _ := got["metadata_provider_ids"].(map[string]any)
		if num(t, got["year"], "year") != m.Year || str(ids["tmdb"]) != m.TMDB || str(ids["imdb"]) != m.IMDB {
			t.Errorf("%s = %v %v, want %d tmdb %s imdb %s", m.Title, got["year"], ids, m.Year, m.TMDB, m.IMDB)
		}
		if str(got["overview"]) == "" {
			t.Errorf("%s has no overview", m.Title)
		}
		// the nfo's genre alone: the providers add none to a film whose nfo
		// names one
		if genres := strs(t, got["genres"], "genres"); !slices.Equal(genres, []string{m.Genre}) {
			t.Errorf("%s genres = %v, want [%s]", m.Title, genres, m.Genre)
		}
		if !slices.ContainsFunc(rows(t, got["people"], "people"), func(p map[string]any) bool {
			return str(p["name"]) == m.Director && str(p["type"]) == "Director"
		}) {
			t.Errorf("%s's people = %v, want %s as the director", m.Title, got["people"], m.Director)
		}
	}

	for _, s := range shows {
		id := findItem(t, "Shows", "Series", s.Title)
		got := call(t, "item_get", map[string]any{"id": id})
		ids, _ := got["metadata_provider_ids"].(map[string]any)
		if num(t, got["year"], "year") != s.Year || str(ids["tmdb"]) != s.TMDB || str(ids["tvdb"]) != s.TVDB {
			t.Errorf("%s = %v %v, want %d tmdb %s tvdb %s", s.Title, got["year"], ids, s.Year, s.TMDB, s.TVDB)
		}
		eps := call(t, "library_episodes", map[string]any{"series_id": id})
		var have, want []string
		for _, e := range rows(t, eps["episodes"], "episodes") {
			have = append(have, fmt.Sprintf("S%02dE%02d", num(t, e["season"], "season"), num(t, e["episode"], "episode")))
		}
		for season, numbers := range s.Episodes {
			for _, n := range numbers {
				want = append(want, fmt.Sprintf("S%02dE%02d", season, n))
			}
		}
		// exactly the files laid out, no more
		slices.Sort(want)
		if !slices.Equal(have, want) {
			t.Errorf("%s holds %v, want %v", s.Title, have, want)
		}
	}
}
