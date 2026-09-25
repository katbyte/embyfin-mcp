//go:build integration

package acceptance

import (
	"fmt"
	"strings"
	"testing"
)

// What goes with a film or a show and is not a copy of it: the messy
// Interstellar's trailer beside it (-trailer) and its Trailers and Extras
// folders, and a featurette in the messy Severance's season-one Extras
// folder. Each is a 360p file, so an audit that took one for a film or an
// episode would have something to say about it.
//
// Neither server holds the film's extras as films or as versions of it. The
// season's featurette is where they part: Jellyfin keeps it as the season's
// extra, and Emby 4.10 reads it as an episode of the show with no number -
// which the audits that judge episodes leave alone.
func TestExtrasAreNeitherCopiesNorEpisodes(t *testing.T) {
	isExtra := func(path string) bool {
		return strings.Contains(path, "/Extras/") || strings.Contains(path, "/Trailers/") || strings.Contains(path, "-trailer")
	}

	t.Run("a film's", func(t *testing.T) {
		interstellar := findItem(t, "Messy Movies", "Movie", "Interstellar")
		got := call(t, "item_get", map[string]any{"id": interstellar})
		if str(got["path"]) != "/media/messy-movies/"+messyInterstellar+"/"+messyInterstellar+".mp4" || got["versions"] != nil {
			t.Errorf("Interstellar = %v in versions %v, want its own file alone", got["path"], got["versions"])
		}
		films := rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "types": "Movie", "limit": 100})["items"], "items")
		if len(films) != messyMovies() {
			t.Errorf("the messy library lists %d films, want %d", len(films), messyMovies())
		}
		for _, f := range films {
			if isExtra(str(f["path"])) {
				t.Errorf("an extra listed as a film: %v", f)
			}
		}
		for _, audit := range []string{"audit_multiple_versions", "audit_quality", "audit_duplicates", "audit_disc_folders"} {
			if out := fmt.Sprint(call(t, audit, map[string]any{"library": "Messy Movies"})); isExtra(out) || strings.Contains(out, "Trailer") {
				t.Errorf("%s names an extra: %s", audit, out)
			}
		}
	})

	t.Run("a season's", func(t *testing.T) {
		const featurette = "/media/messy-shows/Severance/Season 01/Extras/Featurette.mp4"
		var asEpisode map[string]any
		for _, e := range rows(t, call(t, "library_episodes", map[string]any{"library": "Messy Shows", "limit": 200})["episodes"], "episodes") {
			if str(e["path"]) == featurette {
				asEpisode = e
			}
		}
		switch {
		case isJellyfin() && asEpisode != nil:
			t.Errorf("Jellyfin holds the season's featurette as an episode: %v", asEpisode)
		case !isJellyfin() && (asEpisode == nil || str(asEpisode["series"]) != "Severance" || numOr0(asEpisode["episode"]) != 0 || numOr0(asEpisode["season"]) != 0):
			t.Errorf("Emby holds the season's featurette as %v, want an episode of Severance with no number, in no season", asEpisode)
		}
		for _, audit := range []string{"audit_quality", "audit_runtime", "audit_duplicate_episodes"} {
			out := call(t, audit, map[string]any{"library": "Messy Shows"})
			if strings.Contains(fmt.Sprint(out), "Featurette") {
				t.Errorf("%s names the featurette: %v", audit, out)
			}
			// what each sweeps: the episode files, and on Emby, which shows
			// The Wire's two copies of one episode as one, one fewer where
			// the audit reads the library as people are shown it
			want := messyEpisodeFiles
			if audit != "audit_runtime" {
				want = messyEpisodesJudged()
			}
			if n := num(t, out["items_scanned"], "items_scanned"); n != want {
				t.Errorf("%s scanned %d, want %d and no extra", audit, n, want)
			}
		}
	})
}
