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
// ids that do not hold up - Crossed Wires an IMDb id that is Breaking Bad, a
// series, and Taken Down a TMDB id TMDB has no film for - and a staged film
// whose sidecar pairs Alien's TMDB id with Blade Runner's IMDb id is a third.
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
		"Zzyzx Crossed Wires": "ids: its IMDb id tt0903747 is a series, not a film: Breaking Bad, TMDB tv 1396 | holds imdb:tt0903747",
		"Zzyzx Taken Down":    "ids: TMDB has no film 99999999: the id is wrong, or the film was taken down | holds tmdb:99999999",
	}
	out := call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "ids", "types": "Movie"})
	if got := problems(out); !reflect.DeepEqual(got, lasting) || num(t, out["items_scanned"], "items_scanned") != messyMovies() {
		t.Errorf("the messy films' ids = %v (scanned %v), want %v", got, out["items_scanned"], lasting)
	}
	if byCheck := object(t, out["by_check"], "by_check"); num(t, byCheck["ids"], "ids") != 2 || byCheck["runtime"] != nil {
		t.Errorf("by_check = %v", byCheck)
	}
	// films are the only kind checked yet, said plainly
	if msg := callErr(t, "audit_provider", map[string]any{"library": "Messy Shows", "types": "Series"}); !strings.Contains(msg, "types must be Movie") {
		t.Errorf("types=Series: %s", msg)
	}

	raw, err := os.ReadFile(filepath.Join(dataDir(), "movies", "Arrival (2016)", "Arrival (2016).mp4")) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	have := movieCount(t, "Messy Movies")
	dir := filepath.Join(dataDir(), "messy-movies", "Zzyzx Crossed (1979)")
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		_ = scanUntil("Messy Movies", have)
	})
	mediaMkdir(t, dir)
	mediaWrite(t, filepath.Join(dir, "Zzyzx Crossed (1979).mp4"), raw)
	mediaWrite(t, filepath.Join(dir, "movie.nfo"), []byte(`<?xml version="1.0" encoding="utf-8"?>
<movie>
  <title>Zzyzx Crossed</title>
  <year>1979</year>
  <tmdbid>348</tmdbid>
  <uniqueid type="tmdb" default="true">348</uniqueid>
  <imdbid>tt0083658</imdbid>
  <uniqueid type="imdb">tt0083658</uniqueid>
</movie>
`))

	if err := scanUntil("Messy Movies", have+1); err != nil {
		t.Fatal(err)
	}

	out = call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "ids"})
	got := problems(out)
	want := map[string]string{"Zzyzx Crossed": "ids: its TMDB id is 348, Alien (1979), whose IMDb id is tt0078748, not the tt0083658 it holds: one of the two is wrong | holds tmdb:348 imdb:tt0083658"}
	maps.Copy(want, lasting)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with the crossed film staged = %v, want %v", got, want)
	}
}
