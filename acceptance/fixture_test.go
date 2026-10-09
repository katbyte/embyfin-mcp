//go:build integration

package acceptance

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"
)

// The catalogue the script laid out is what the server holds: every clean
// film and show with the ids, genre and director its nfo carried, which is
// what every other test here assumes.
func TestFixturesAreWhatTheScriptLaidOut(t *testing.T) {
	for _, m := range movies {
		id := findItem(t, "Movies", "Movie", m.Title)
		got := suite.Call(t, "item_get", map[string]any{"id": id})
		ids, _ := got["metadata_provider_ids"].(map[string]any)
		if acc.Num(t, got["year"], "year") != m.Year || acc.Str(ids["tmdb"]) != m.TMDB || acc.Str(ids["imdb"]) != m.IMDB {
			t.Errorf("%s = %v %v, want %d tmdb %s imdb %s", m.Title, got["year"], ids, m.Year, m.TMDB, m.IMDB)
		}
		if acc.Str(got["overview"]) == "" {
			t.Errorf("%s has no overview", m.Title)
		}
		// the nfo's genre alone: the providers add none to a film whose nfo
		// names one
		if genres := acc.Strs(t, got["genres"], "genres"); !slices.Equal(genres, []string{m.Genre}) {
			t.Errorf("%s genres = %v, want [%s]", m.Title, genres, m.Genre)
		}
		if !slices.ContainsFunc(acc.Rows(t, got["people"], "people"), func(p map[string]any) bool {
			return acc.Str(p["name"]) == m.Director && acc.Str(p["type"]) == "Director"
		}) {
			t.Errorf("%s's people = %v, want %s as the director", m.Title, got["people"], m.Director)
		}
	}

	for _, s := range shows {
		id := findItem(t, "Shows", "Series", s.Title)
		got := suite.Call(t, "item_get", map[string]any{"id": id})
		ids, _ := got["metadata_provider_ids"].(map[string]any)
		if acc.Num(t, got["year"], "year") != s.Year || acc.Str(ids["tmdb"]) != s.TMDB || acc.Str(ids["tvdb"]) != s.TVDB {
			t.Errorf("%s = %v %v, want %d tmdb %s tvdb %s", s.Title, got["year"], ids, s.Year, s.TMDB, s.TVDB)
		}
		eps := suite.Call(t, "library_episodes", map[string]any{"series_id": id})
		var have, want []string
		for _, e := range acc.Rows(t, eps["episodes"], "episodes") {
			have = append(have, fmt.Sprintf("S%02dE%02d", acc.Num(t, e["season"], "season"), acc.Num(t, e["episode"], "episode")))
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

	// the messy series, each at its own folder: the ids its nfo named and the
	// episode files in that folder. Both servers answer a series' episodes
	// with those of every entry sharing its ids, so a folder's own are read
	// by path
	for _, s := range messyShows {
		path := "/media/messy-shows/" + s.Folder
		var got map[string]any
		for _, it := range acc.Rows(t, suite.Call(t, "library_items", map[string]any{"library": "Messy Shows", "query": s.Title, "limit": 50})["items"], "items") {
			if acc.Str(it["path"]) == path {
				got = suite.Call(t, "item_get", map[string]any{"id": acc.Str(it["id"])})
			}
		}
		if got == nil {
			t.Errorf("no series at %s", path)
			continue
		}
		ids, _ := got["metadata_provider_ids"].(map[string]any)
		if acc.Str(got["name"]) != s.Title || acc.Str(ids["tmdb"]) != s.TMDB || acc.Str(ids["tvdb"]) != s.TVDB || acc.Str(ids["imdb"]) != s.IMDB {
			t.Errorf("%s = %v %v, want %s tmdb %s tvdb %s imdb %s", path, got["name"], ids, s.Title, s.TMDB, s.TVDB, s.IMDB)
		}
		var have, want []string
		for _, e := range acc.Rows(t, suite.Call(t, "library_episodes", map[string]any{"series_id": acc.Str(got["id"])})["episodes"], "episodes") {
			if strings.HasPrefix(acc.Str(e["path"]), path+"/") {
				have = append(have, fmt.Sprintf("S%02dE%02d", acc.Num(t, e["season"], "season"), acc.Num(t, e["episode"], "episode")))
			}
		}
		for season, numbers := range s.Episodes {
			for _, n := range numbers {
				want = append(want, fmt.Sprintf("S%02dE%02d", season, n))
			}
		}
		slices.Sort(have)
		slices.Sort(want)
		if !slices.Equal(have, want) {
			t.Errorf("%s holds %v, want %v", path, have, want)
		}
	}
}
