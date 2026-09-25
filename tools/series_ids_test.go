package tools

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// A series id that names something else is refused, saying what it names.
// show_missing given a film's id read the film's TMDB id as a series' and
// reported an unrelated show's gaps; the others answered with nothing, which
// reads as a series with no seasons or no episodes.
func TestSeriesIDsMustNameASeries(t *testing.T) {
	t.Parallel()

	library := []map[string]any{
		{"Id": "sev", "Name": "Severance", "Type": "Series", "ProductionYear": 2022, "Path": "/zz/shows/Severance", "ProviderIds": map[string]any{"Tmdb": "95396"}},
		{"Id": "e1", "Name": "Good News About Hell", "Type": "Episode", "SeriesId": "sev", "SeriesName": "Severance", "ParentIndexNumber": 1, "IndexNumber": 1, "Path": "/zz/shows/Severance/Season 01/Severance S01E01.mkv"},
		{"Id": "s1", "Name": "Season 1", "Type": "Season", "SeriesId": "sev", "SeriesName": "Severance", "IndexNumber": 1},
		{"Id": "12", "Name": "Arrival", "Type": "Movie", "ProductionYear": 2016, "Path": "/zz/films/Arrival (2016)/Arrival (2016).mkv", "ProviderIds": map[string]any{"Tmdb": "329865"}},
	}
	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		rows := slices.DeleteFunc(slices.Clone(library), func(it map[string]any) bool {
			if ids := param(q, "Ids"); ids != "" && !slices.Contains(strings.Split(ids, ","), text(it["Id"])) {
				return true
			}
			types := param(q, "IncludeItemTypes")
			return types != "" && !slices.Contains(strings.Split(types, ","), text(it["Type"]))
		})
		writeJSON(t, w, page(rows...))
	})
	f.mux.HandleFunc("GET /Shows/sev/Episodes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(library[1]))
	})
	cs := session(t, f, Options{TMDBKey: "k", ProviderTransport: tmdbTransport(t, nil)})

	film := "12 is a film, Arrival (2016), not a series"
	episode := "e1 is an episode, Severance S01E01 Good News About Hell, not a series: its series is Severance, id sev"
	season := "s1 is a season, Season 1 of Severance, not a series: its series' id is sev"
	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"show_seasons", map[string]any{"series_id": "12"}, film},
		{"show_seasons", map[string]any{"series_id": "e1"}, episode},
		{"show_missing", map[string]any{"series_id": "12"}, film},
		{"show_missing", map[string]any{"series_id": "e1"}, episode},
		{"show_missing", map[string]any{"series_id": "s1"}, season},
		{"library_episodes", map[string]any{"series_id": "12"}, film},
		{"library_episodes", map[string]any{"series_id": "e1"}, episode},
		{"show_episodes_exist", map[string]any{"series_id": "12", "episodes": []any{map[string]any{"season": 1, "episode": 1}}}, film},
		{"show_episodes_exist", map[string]any{"series_id": "e1", "episodes": []any{map[string]any{"season": 1, "episode": 1}}}, episode},
		// by name or id: an id of something else says what it is beside the
		// name that matched nothing
		{"library_episodes", map[string]any{"series": "12"}, "as an id, " + film},
	} {
		if msg := mustRefuse(t, cs, tc.tool, tc.args); !strings.Contains(msg, tc.want) {
			t.Errorf("%s %v = %q, want %q", tc.tool, tc.args, msg, tc.want)
		}
	}
	// a batch answers each query on its own row
	out := mustCall(t, cs, "show_episodes_exist", map[string]any{"queries": []any{
		map[string]any{"series_id": "12", "episodes": []any{map[string]any{"season": 1, "episode": 1}}},
	}})
	if rows := objects(t, out["results"], "results"); len(rows) != 1 || !strings.Contains(text(rows[0]["error"]), film) {
		t.Errorf("a batch with a film's id = %v", out["results"])
	}
	// and the series itself is still answered
	if out := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"}); out["series"] != "Severance" {
		t.Errorf("show_missing of the series = %v", out)
	}
}

// A match names another title when the first id the item held before and
// holds now changed, or it held none; ids corrected or added beside that one
// are the same title's. Princess Mononoke's nfo gave it a TVDB id TMDB's
// record does not, and matched to itself it read as another film, with a
// warning of an nfo and of watch state that followed nothing.
func TestOtherTitle(t *testing.T) {
	t.Parallel()

	item := func(ids map[string]string) *embyfin.Item { return &embyfin.Item{ProviderIDs: ids} }
	for _, c := range []struct {
		before, after map[string]string
		want          bool
	}{
		{map[string]string{"Tmdb": "128", "Tvdb": "306766"}, map[string]string{"Tmdb": "128", "Tvdb": "791"}, false},
		{map[string]string{"Tmdb": "128"}, map[string]string{"Tmdb": "128", "Imdb": "tt0119698"}, false},
		{map[string]string{"Tmdb": "841"}, map[string]string{"Tmdb": "438631"}, true},
		{map[string]string{"Imdb": "tt0903747"}, map[string]string{"Tmdb": "77", "Imdb": "tt0209144"}, true},
		// nothing held on both sides to tell by: taken as another title
		{map[string]string{"Imdb": "tt0903747"}, map[string]string{"Tmdb": "77"}, true},
		{nil, map[string]string{"Tmdb": "128"}, true},
	} {
		if got := otherTitle(item(c.before), item(c.after)); got != c.want {
			t.Errorf("otherTitle(%v, %v) = %v, want %v", c.before, c.after, got, c.want)
		}
	}
}
