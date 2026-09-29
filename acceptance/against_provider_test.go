//go:build integration

package acceptance

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Films' ids asked about at TMDB. Most of the messy films' sidecars carry a
// TMDB and an IMDb id that agree, so nothing is reported for them; two carry
// ids that do not hold up - Memento an IMDb id that is Breaking Bad, a
// series, and the Despecialized Edition of Star Wars a TMDB id TMDB has no
// film for - and a staged film whose sidecar pairs The Machinist's TMDB id
// with Blade Runner's IMDb id is a third.
//
// The film is staged and taken away again, like the disc audit's streams:
// the messy library's count is read by tests that have nothing to do with it.
func TestAuditProviderIDs(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	needsTMDBRecording(t, "GET api.themoviedb.org/3/find/tt0903747?external_source=imdb_id")

	problems := func(out map[string]any) map[string]string {
		got := map[string]string{}
		for _, f := range rows(t, out["findings"], "findings") {
			ps := strs(t, f["problems"], "problems")
			if len(ps) != 1 {
				t.Errorf("%v has %d problems: %v", f["name"], len(ps), ps)
				continue
			}
			got[title(str(f["name"]))] = ps[0] + " | holds " + str(f["holds"])
		}
		return got
	}
	lasting := map[string]string{
		"Memento": "ids: its IMDb id tt0903747 is a series, not a film: Breaking Bad, TMDB tv 1396 | holds imdb:tt0903747",
		"Star Wars: Episode IV - A New Hope (Despecialized Edition)": "ids: TMDB has no film 99999999: the id is wrong, or the film was taken down | holds tmdb:99999999",
	}
	out := call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "ids", "types": "Movie"})
	if got := problems(out); !reflect.DeepEqual(got, lasting) || num(t, out["items_scanned"], "items_scanned") != messyMovies() {
		t.Errorf("the messy films' ids = %v (scanned %v), want %v", got, out["items_scanned"], lasting)
	}
	if byCheck := object(t, out["by_check"], "by_check"); num(t, byCheck["ids"], "ids") != 2 || byCheck["runtime"] != nil {
		t.Errorf("by_check = %v", byCheck)
	}
	// films and episodes are the kinds checked, said plainly
	if msg := callErr(t, "audit_provider", map[string]any{"library": "Messy Shows", "types": "Series"}); !strings.Contains(msg, "types must be among Movie, Episode") {
		t.Errorf("types=Series: %s", msg)
	}

	// its file is a copy cut to five seconds, the messy Severance's third
	// episode, so its runtime is off TMDB's for it as well as its ids
	raw, err := os.ReadFile(filepath.Join(dataDir(), "messy-shows", "Severance", "Season 01", "Severance S01E03.mp4")) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	have := movieCount(t, "Messy Movies")
	dir := filepath.Join(dataDir(), "messy-movies", "The Machinist (2004)")
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		// the messy films' count is read by every test after this one, so a
		// staged film left behind fails here rather than somewhere else
		if err := scanUntil("Messy Movies", have); err != nil {
			t.Error(err)
		}
	})
	mediaMkdir(t, dir)
	mediaWrite(t, filepath.Join(dir, "The Machinist (2004).mp4"), raw)
	mediaWrite(t, filepath.Join(dir, "movie.nfo"), []byte(`<?xml version="1.0" encoding="utf-8"?>
<movie>
  <title>The Machinist</title>
  <year>2004</year>
  <tmdbid>4553</tmdbid>
  <uniqueid type="tmdb" default="true">4553</uniqueid>
  <imdbid>tt0083658</imdbid>
  <uniqueid type="imdb">tt0083658</uniqueid>
</movie>
`))

	if err := scanUntil("Messy Movies", have+1); err != nil {
		t.Fatal(err)
	}

	out = call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "ids"})
	got := problems(out)
	crossed := "ids: its TMDB id is 4553, The Machinist (2004), whose IMDb id is tt0361862, not the tt0083658 it holds: one of the two is wrong"
	want := map[string]string{"The Machinist": crossed + " | holds tmdb:4553 imdb:tt0083658"}
	maps.Copy(want, lasting)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with the crossed film staged = %v, want %v", got, want)
	}

	// both checks, which is what a sweep runs unless told otherwise: the
	// staged film fails both, on one row counted under each, and the films
	// TestAuditProviderRuntime finds off TMDB's runtimes fail the runtime
	// check: Arrival, cut short, Stargate held as Stargate: Continuum, and on
	// Jellyfin the DVD kept whole, read as the second its title runs. Emby
	// never reads the disc, so it has no runtime to hold to TMDB's
	runtimeOff := 2
	if isJellyfin() {
		runtimeOff = 3
	}
	out = call(t, "audit_provider", map[string]any{"library": "Messy Movies"})
	byCheck := object(t, out["by_check"], "by_check")
	total := num(t, out["total_findings"], "total_findings")
	if num(t, byCheck["ids"], "ids") != 3 || num(t, byCheck["runtime"], "runtime") != runtimeOff+1 || total != runtimeOff+3 || len(byCheck) != 2 {
		t.Errorf("both checks: by_check %v of %d findings, want ids 3 and runtime %d of %d", byCheck, total, runtimeOff+1, runtimeOff+3)
	}
	var machinist []string
	for _, f := range rows(t, out["findings"], "findings") {
		if title(str(f["name"])) == "The Machinist" {
			machinist = strs(t, f["problems"], "problems")
		}
	}
	if len(machinist) != 2 || machinist[0] != crossed || !strings.HasPrefix(machinist[1], "runtime: file 0 min, TMDB says ") {
		t.Errorf("the staged film's problems = %v, want its ids then its runtime", machinist)
	}
	if n := num(t, out["items_scanned"], "items_scanned"); n != messyMovies()+1 {
		t.Errorf("scanned %d, want %d", n, messyMovies()+1)
	}
	// a limit caps the rows and not the counts
	capped := call(t, "audit_provider", map[string]any{"library": "Messy Movies", "limit": 1})
	if len(rows(t, capped["findings"], "findings")) != 1 || num(t, capped["total_findings"], "total_findings") != total || !reflect.DeepEqual(capped["by_check"], out["by_check"]) {
		t.Errorf("limit 1 = %v", capped)
	}
	// every library's films: the clean films' ids hold, and so do their
	// runtimes, each file running TMDB's length for its film
	whole := call(t, "audit_provider", map[string]any{"types": "Movie"})
	if byCheck := object(t, whole["by_check"], "by_check"); num(t, byCheck["ids"], "ids") != 3 || num(t, byCheck["runtime"], "runtime") != runtimeOff+1 ||
		num(t, whole["items_scanned"], "items_scanned") != len(movies)+messyMovies()+1 {
		t.Errorf("every library: by_check %v of %v scanned, want ids 3 and runtime %d of %d", byCheck, whole["items_scanned"], runtimeOff+1, len(movies)+messyMovies()+1)
	}
	if msg := callErr(t, "audit_provider", map[string]any{"checks": "ids,year"}); !strings.Contains(msg, `checks must be among ids, runtime, not "year"`) {
		t.Errorf("an unknown check: %s", msg)
	}

	machinistID := findItem(t, "Messy Movies", "Movie", "The Machinist")
	ids := map[string]any{"library": "Messy Movies", "checks": "ids"}
	// an IMDb id that is an episode of a series: Breaking Bad's first
	t.Run("an episode's IMDb id", func(t *testing.T) {
		needsTMDBRecording(t, "GET api.themoviedb.org/3/find/tt0959621?external_source=imdb_id")
		setIDs(t, machinistID, map[string]any{"Imdb": "tt0959621"})
		if got := problems(call(t, "audit_provider", ids))["The Machinist"]; got != `ids: its IMDb id tt0959621 is an episode, not a film: "Pilot", S01E01 of TMDB tv 1396 | holds imdb:tt0959621` {
			t.Errorf("a film holding an episode's IMDb id = %q", got)
		}
	})
	// the film's own ids, one at a time, hold up: an IMDb id TMDB places as
	// a film, and a TMDB id with no IMDb id to set against it
	t.Run("an IMDb id alone", func(t *testing.T) {
		needsTMDBRecording(t, "GET api.themoviedb.org/3/find/tt0361862?external_source=imdb_id")
		setIDs(t, machinistID, map[string]any{"Imdb": "tt0361862"})
		if got := problems(call(t, "audit_provider", ids)); !reflect.DeepEqual(got, lasting) {
			t.Errorf("a film holding its own IMDb id alone = %v, want %v", got, lasting)
		}
	})
	t.Run("a TMDB id alone", func(t *testing.T) {
		setIDs(t, machinistID, map[string]any{"Tmdb": "4553"})
		if got := problems(call(t, "audit_provider", ids)); !reflect.DeepEqual(got, lasting) {
			t.Errorf("a film holding its own TMDB id alone = %v, want %v", got, lasting)
		}
	})
}

// An episode is judged against TMDB's own length for it, read once for its
// series from TMDB's seasons. Breaking Bad's three episodes in Shows run
// TMDB's 59, 49 and 49 minutes and hold, the third under the second's title
// all the same. The messy Severance's third is cut to five seconds of TMDB's
// 60 minutes, and a fourth is staged beside it, cut the same way and held
// under the second's title: its finding names TMDB's fourth, so an episode
// numbered another way than TMDB's reads as that.
func TestAuditProviderEpisodes(t *testing.T) {
	episodeIDs := func(series string) []any {
		var ids []any
		for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": series, "season": 1})["episodes"], "episodes") {
			ids = append(ids, str(e["id"]))
		}
		return ids
	}
	judged := func(ids []any) (map[string]string, map[string]any) {
		out := call(t, "audit_provider", map[string]any{"ids": ids, "checks": "runtime"})
		got := map[string]string{}
		for _, f := range rows(t, out["findings"], "findings") {
			got[str(f["name"])] = str(f["holds"]) + ": " + strings.Join(strs(t, f["problems"], "problems"), " | ")
		}
		return got, out
	}

	ids := episodeIDs(findItem(t, "Shows", "Series", "Breaking Bad"))
	if got, out := judged(ids); len(got) != 0 || num(t, out["items_scanned"], "items_scanned") != 3 || num(t, out["runtime_not_judged"], "runtime_not_judged") != 0 {
		t.Errorf("Breaking Bad's episodes = %v (scanned %v, not judged %v), want all three judged and none off", got, out["items_scanned"], out["runtime_not_judged"])
	}

	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	season := "messy-shows/Severance/Season 01/"
	stage(t, plus(0, 0, 1), map[string][]byte{
		season + "Severance S01E04.mp4": fixture(t, season+"Severance S01E03.mp4"),
		season + "Severance S01E04.nfo": []byte(`<?xml version="1.0" encoding="utf-8"?>
<episodedetails>
  <title>Half Loop</title>
  <season>1</season>
  <episode>4</episode>
</episodedetails>
`),
	})
	got, out := judged(episodeIDs(findItem(t, "Messy Shows", "Series", "Severance")))
	want := map[string]string{
		"Severance S01E03 In Perpetuity": `tmdb tv 95396: runtime: file 0 min, TMDB says 60 min for S01E03 "In Perpetuity" (100% off)`,
		"Severance S01E04 Half Loop":     `tmdb tv 95396: runtime: file 0 min, TMDB says 50 min for S01E04 "The You You Are" (100% off)`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the messy Severance's first season = %v (scanned %v, not judged %v), want %v", got, out["items_scanned"], out["runtime_not_judged"], want)
	}
	// ids alone judge no episode
	if msg := callErr(t, "audit_provider", map[string]any{"ids": ids, "types": "Episode", "checks": "ids"}); !strings.Contains(msg, "episodes are checked by runtime only") {
		t.Errorf("episodes by ids alone = %s", msg)
	}
}
