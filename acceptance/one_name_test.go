//go:build integration

package acceptance

import (
	"slices"
	"strings"
	"testing"
)

// Limitless is a film of 2011 in the clean movie library and a series of
// 2015 in the clean show library: one name, two titles. A lookup of a show by
// that name is the series, never the film; a search by it is both, each as
// what it is; and each is found by its own ids and not by the other's.
func TestOneNameForAFilmAndAShow(t *testing.T) {
	film := findItem(t, "Movies", "Movie", "Limitless")
	series := findItem(t, "Shows", "Series", "Limitless")

	// the show tools by name: the series, matched whole, and the film is not
	// even a candidate
	resolved := rows(t, call(t, "show_resolve", map[string]any{"title": "Limitless"})["candidates"], "candidates")
	if len(resolved) != 1 || str(resolved[0]["series_id"]) != series || decimal(t, resolved[0]["score"], "score") != 1 {
		t.Errorf("show_resolve Limitless = %v, want the series alone", resolved)
	}
	eps := call(t, "library_episodes", map[string]any{"series": "Limitless"})
	if str(eps["series_id"]) != series || !slices.Equal(episodeKeys(t, eps), []string{"Limitless S01E01"}) {
		t.Errorf("library_episodes Limitless = %v %v, want the series' pilot", eps["series_id"], episodeKeys(t, eps))
	}
	exists := call(t, "show_episodes_exist", map[string]any{"series": "Limitless", "episodes": []map[string]any{{"season": 1, "episode": 1}}})
	if str(exists["series_id"]) != series || num(t, exists["absent"], "absent") != 0 {
		t.Errorf("show_episodes_exist Limitless = %v", exists)
	}
	// and a film's id is refused as a series by every show tool
	if msg := callErr(t, "show_seasons", map[string]any{"series_id": film}); !strings.Contains(msg, film+" is a film, Limitless (2011), not a series") {
		t.Errorf("show_seasons of the film = %s", msg)
	}

	// a search is both, each as what it is
	got := map[string]string{}
	for _, it := range rows(t, call(t, "library_items", map[string]any{"query": "Limitless", "limit": 50})["items"], "items") {
		got[str(it["type"])+" "+str(it["path"])] = str(it["id"])
	}
	want := map[string]string{"Movie /media/movies/Limitless (2011)/Limitless (2011).mp4": film, "Series /media/shows/Limitless": series}
	if len(got) != len(want) {
		t.Errorf("library_items Limitless = %v, want the film and the series", got)
	}
	for k, id := range want {
		if got[k] != id {
			t.Errorf("library_items Limitless holds %v, want %s as %s", got, id, k)
		}
	}

	// each by its own ids
	for _, tc := range []struct{ provider, id, want string }{
		{"tmdb", "51876", film}, {"imdb", "tt1219289", film},
		{"tmdb", "62687", series}, {"imdb", "tt4422836", series}, {"tvdb", "295743", series},
	} {
		var ids []string
		for _, it := range rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": tc.provider, "id": tc.id})["items"], "items") {
			ids = append(ids, str(it["id"]))
		}
		if !slices.Equal(ids, []string{tc.want}) {
			t.Errorf("%s %s = %v, want %s alone", tc.provider, tc.id, ids, tc.want)
		}
	}

	// and one name is no duplicate: the two share no id
	groups, _ := call(t, "audit_duplicates", nil)["groups"].([]any)
	for _, g := range groups {
		for _, it := range rows(t, g, "group") {
			if str(it["name"]) == "Limitless" {
				t.Errorf("audit_duplicates groups Limitless: %v", g)
			}
		}
	}
}
