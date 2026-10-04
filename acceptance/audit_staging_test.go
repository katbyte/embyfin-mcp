//go:build integration

package acceptance

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	acc "github.com/katbyte/embyfin-mcp/lib/acceptance"

	"github.com/katbyte/embyfin-mcp/lib/testenv"
)

// The audits' staged tests break something the way a library comes to be
// broken - a file laid beside the fixtures, a title or an id edited away
// from what the path says - audit it, put it right, and audit again. These
// are what they share. Everything staged is taken away again when the test
// ends, because the fixtures' counts are read by tests that have nothing to
// do with it.

// holdings is what the two messy libraries hold: every staging changes one
// of these, and the test is not over until they are back.
type holdings struct{ films, series, episodes int }

// typeCount is how many items of one type a library holds right now.
func typeCount(t *testing.T, library, kind string) int {
	t.Helper()

	counts, _ := suite.Call(t, "library_get", map[string]any{"library": library})["type_counts"].(map[string]any)
	n, _ := counts[kind].(float64)

	return int(n)
}

// messyHoldings is what the messy libraries hold right now.
func messyHoldings(t *testing.T) holdings {
	t.Helper()

	return holdings{
		films:    typeCount(t, "Messy Movies", "Movie"),
		series:   typeCount(t, "Messy Shows", "Series"),
		episodes: typeCount(t, "Messy Shows", "Episode"),
	}
}

// fixture reads a file scripts/testenv.sh laid out, by its path under the
// media tree. It is often a test's first step, so it skips as call does
// when there is no container to lay anything out for.
func fixture(t *testing.T, rel string) []byte {
	t.Helper()

	if !suite.Ready {
		t.Skip("EMBYFIN_BACKEND, EMBYFIN_SERVER and EMBYFIN_TOKEN are not set")
	}
	raw, err := os.ReadFile(filepath.Join(testenv.DataDir(), rel)) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

// rescanUntil asks for a scan of every library until check holds
// (suite.ScanUntilTrue), and fails the test naming what never came about.
func rescanUntil(t *testing.T, what string, check func() bool) {
	t.Helper()

	if err := suite.ScanUntilTrue("", check); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// stage writes files under the media tree, by their paths under it, scans
// until the messy libraries hold what they should with them there, and when
// the test ends takes them away again - and the folders named, which a
// server may have written into - scanning until the libraries hold what
// they held before.
func stage(t *testing.T, want func(before holdings) holdings, files map[string][]byte, folders ...string) {
	t.Helper()

	before := messyHoldings(t)
	t.Cleanup(func() {
		for rel := range files {
			_ = os.Remove(filepath.Join(testenv.DataDir(), rel))
		}
		for _, folder := range folders {
			_ = os.RemoveAll(filepath.Join(testenv.DataDir(), folder))
		}
		rescanUntil(t, "the messy libraries back as they were", func() bool { return messyHoldings(t) == before })
	})
	// in a stable order, so a failure reads the same each run
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		path := filepath.Join(testenv.DataDir(), rel)
		acc.MediaMkdir(t, testenv.DataDir(), filepath.Dir(path))
		acc.MediaWrite(t, path, files[rel])
	}
	after := want(before)
	rescanUntil(t, "the staged files in the libraries", func() bool { return messyHoldings(t) == after })
}

// plus is a holdings change: so many more films, series and episodes.
func plus(films, series, episodes int) func(holdings) holdings {
	return func(h holdings) holdings {
		return holdings{h.films + films, h.series + series, h.episodes + episodes}
	}
}

// setIDs replaces an item's provider ids until the test ends, as a match by
// hand leaves them, and puts the ones it held back.
func setIDs(t *testing.T, id string, ids map[string]any) {
	t.Helper()

	held := fullItem(t, id)["ProviderIds"]
	if held == nil {
		held = map[string]any{}
	}
	t.Cleanup(func() { updateItem(t, id, map[string]any{"ProviderIds": held}) })
	updateItem(t, id, map[string]any{"ProviderIds": ids})
}

// rename gives an item another title until the test ends.
func rename(t *testing.T, id, name string) {
	t.Helper()

	held := acc.Str(suite.Call(t, "item_get", map[string]any{"id": id})["name"])
	t.Cleanup(func() {
		if _, err := suite.Invoke("item_edit", map[string]any{"ids": []any{id}, "name": held}); err != nil {
			t.Errorf("putting back %q: %v", held, err)
		}
	})
	suite.Call(t, "item_edit", map[string]any{"ids": []any{id}, "name": name})
}

// episodeID is the id of the one episode a series holds at a number.
func episodeID(t *testing.T, seriesID string, season, episode int) string {
	t.Helper()

	out := suite.Call(t, "show_episodes_exist", map[string]any{"series_id": seriesID, "episodes": []map[string]any{{"season": season, "episode": episode}}})
	row := acc.Rows(t, out["episodes"], "episodes")[0]
	if !acc.BoolOf(row["exists"]) || acc.Str(row["id"]) == "" {
		t.Fatalf("series %s holds no S%02dE%02d: %v", seriesID, season, episode, row)
	}

	return acc.Str(row["id"])
}

// auditRow is one audit's count in audit_all over a library, the row named
// as rowKey names it.
func auditRow(t *testing.T, library, audit string) int {
	t.Helper()

	for _, row := range acc.Rows(t, suite.Call(t, "audit_all", map[string]any{"library": library})["audits"], "audits") {
		if rowKey(row) == audit {
			return acc.Num(t, row["findings"], "findings")
		}
	}
	t.Fatalf("audit_all over %s has no %s row", library, audit)

	return 0
}
