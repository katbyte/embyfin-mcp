//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Films asked about at TMDB. The messy films' sidecars carry a TMDB and an
// IMDb id that agree, so nothing is reported; a staged film whose sidecar
// pairs Alien's TMDB id with Blade Runner's IMDb id is. Every lookup here is
// one audit_runtime already makes, so the recordings hold them.
//
// The film is staged and taken away again, like the disc audit's streams:
// the messy library's count is read by tests that have nothing to do with it.
func TestAuditMovieIDs(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}

	out := call(t, "audit_movie_ids", map[string]any{"library": "Messy Movies"})
	if n := num(t, out["total_findings"], "total_findings"); n != 0 || num(t, out["items_scanned"], "items_scanned") != messyMovies() {
		t.Errorf("the messy films' ids agree, and the audit says %v", out)
	}

	raw, err := os.ReadFile(filepath.Join(dataDir(), "movies", "Arrival (2016)", "Arrival (2016).mp4")) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	have := movieCount(t, "Messy Movies")
	dir := filepath.Join(dataDir(), "messy-movies", "Zzyzx Crossed (1979)")
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		if _, err := invoke("library_scan", nil); err == nil {
			_ = waitForItems("Messy Movies", have)
		}
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

	call(t, "library_scan", nil)
	if err := waitForItems("Messy Movies", have+1); err != nil {
		t.Fatal(err)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}

	out = call(t, "audit_movie_ids", map[string]any{"library": "Messy Movies"})
	findings := rows(t, out["findings"], "findings")
	if len(findings) != 1 || !strings.HasPrefix(str(findings[0]["name"]), "Zzyzx Crossed") {
		t.Fatalf("findings = %v", findings)
	}
	if detail := str(findings[0]["detail"]); !strings.Contains(detail, "whose IMDb id is tt0078748, not the tt0083658 it holds") {
		t.Errorf("detail = %q", detail)
	}
	if holds := str(findings[0]["holds"]); holds != "tmdb:348 imdb:tt0083658" {
		t.Errorf("holds = %q", holds)
	}
}
