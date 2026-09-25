//go:build integration

package acceptance

import (
	"strings"
	"testing"
)

// Every tool taking a series id refuses the id of a film or an episode, and
// says what it is. show_missing given Arrival's id read its TMDB id as a
// series' and reported the gaps of whatever show TMDB numbers 329865; the
// others answered with nothing, which reads as a series with no seasons or no
// episodes.
func TestSeriesIDsNameASeries(t *testing.T) {
	film := findItem(t, "Movies", "Movie", "Arrival")
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	pilot := str(rows(t, call(t, "library_episodes", map[string]any{"series_id": series, "season": 1})["episodes"], "episodes")[0]["id"])

	isFilm := film + " is a film, Arrival (2016), not a series"
	isEpisode := pilot + " is an episode, Breaking Bad S01E01 Pilot, not a series: its series is Breaking Bad, id " + series
	one := []any{map[string]any{"season": 1, "episode": 1}}
	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"show_seasons", map[string]any{"series_id": film}, isFilm},
		{"show_seasons", map[string]any{"series_id": pilot}, isEpisode},
		{"show_missing", map[string]any{"series_id": film}, isFilm},
		{"show_missing", map[string]any{"series_id": pilot}, isEpisode},
		{"show_episodes_exist", map[string]any{"series_id": film, "episodes": one}, isFilm},
		{"show_episodes_exist", map[string]any{"series_id": pilot, "episodes": one}, isEpisode},
		{"library_episodes", map[string]any{"series_id": film}, isFilm},
		{"library_episodes", map[string]any{"series_id": pilot}, isEpisode},
	} {
		if msg := callErr(t, tc.tool, tc.args); !strings.Contains(msg, tc.want) {
			t.Errorf("%s %v = %q, want %q", tc.tool, tc.args, msg, tc.want)
		}
	}
	// a batch answers the film's row with the refusal and goes on
	out := call(t, "show_episodes_exist", map[string]any{"queries": []any{
		map[string]any{"series_id": film, "episodes": one},
		map[string]any{"series_id": series, "episodes": one},
	}})
	results := rows(t, out["results"], "results")
	if len(results) != 2 || !strings.Contains(str(results[0]["error"]), isFilm) || results[1]["error"] != nil {
		t.Errorf("a batch with a film's id = %v", results)
	}
	// and the series' own id is answered as ever
	if got := call(t, "show_seasons", map[string]any{"series_id": series}); str(got["series"]) != "Breaking Bad" {
		t.Errorf("show_seasons of the series = %v", got)
	}
}
