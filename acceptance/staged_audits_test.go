//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stageShows lays series folders under the messy show library, each with
// the episode files given, from one fixture video, and takes them away again
// after the test, since other tests read that library's count. It returns
// once the server holds them.
func stageShows(t *testing.T, shows map[string]map[string][]byte) {
	t.Helper()

	have := seriesCount(t, "Messy Shows")
	root := filepath.Join(dataDir(), "messy-shows")
	t.Cleanup(func() {
		for folder := range shows {
			_ = os.RemoveAll(filepath.Join(root, folder))
		}
		_ = scanUntil("Messy Shows", have)
	})
	for folder, files := range shows {
		dir := filepath.Join(root, folder)
		mediaMkdir(t, dir)
		for name, data := range files {
			mediaMkdir(t, filepath.Dir(filepath.Join(dir, name)))
			mediaWrite(t, filepath.Join(dir, name), data)
		}
	}
	if err := scanUntil("Messy Shows", have+len(shows)); err != nil {
		t.Fatal(err)
	}
}

// fixtureVideo reads one of the videos scripts/testenv.sh made.
func fixtureVideo(t *testing.T, parts ...string) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(append([]string{dataDir()}, parts...)...)) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

// Two folders for one show, differing only in spacing and case: the sweep
// in TestAuditDuplicateSeries proves nothing is reported that does
// not collide; this proves the collision is.
func TestAuditDuplicateSeriesFindsAPair(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	short := fixtureVideo(t, "movies", "Arrival (2016)", "Arrival (2016).mp4")
	stageShows(t, map[string]map[string][]byte{
		"Zzyzx Twins (2005)":  {"Season 01/Zzyzx Twins S01E01.mp4": short},
		"Zzyzx  twins (2005)": {"Season 01/Zzyzx Twins S01E02.mp4": short},
	})

	out := call(t, "audit_duplicate_series", map[string]any{"library": "Messy Shows"})
	groups := rows(t, out["groups"], "groups")
	if len(groups) != 1 {
		t.Fatalf("groups = %v, want the one pair", out)
	}
	series := rows(t, groups[0]["series"], "series")
	if len(series) != 2 || !strings.Contains(str(series[0]["folder"])+str(series[1]["folder"]), "Zzyzx  twins") {
		t.Errorf("group = %v, want both folders", groups[0])
	}
}

// A season whose episodes run three minutes, with one that runs a second:
// the runtime audit on the fixtures alone can only prove its sweep
// (TestAuditRuntimeEpisodes), because their episodes are all a second long.
func TestAuditRuntimeFindsAShortEpisode(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	long := fixtureVideo(t, "anime-src", "special.mp4")
	short := fixtureVideo(t, "movies", "Arrival (2016)", "Arrival (2016).mp4")
	stageShows(t, map[string]map[string][]byte{
		"Zzyzx Runtime (2004)": {
			"Season 01/Zzyzx Runtime S01E01.mp4": long,
			"Season 01/Zzyzx Runtime S01E02.mp4": long,
			"Season 01/Zzyzx Runtime S01E03.mp4": long,
			"Season 01/Zzyzx Runtime S01E04.mp4": short,
		},
	})

	out := call(t, "audit_runtime", map[string]any{"library": "Messy Shows"})
	var found []string
	for _, f := range rows(t, out["findings"], "findings") {
		found = append(found, str(f["name"])+": "+str(f["detail"]))
	}
	if len(found) != 1 || !strings.Contains(found[0], "Zzyzx Runtime S01E04") {
		t.Errorf("findings = %v, want the one short episode", found)
	}
}

// library_export writes what library_episodes answers, every row in one
// file, and nothing to the server.
func TestLibraryExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shows.jsonl")
	out := call(t, "library_export", map[string]any{"path": path, "library": "Shows"})
	paged := call(t, "library_episodes", map[string]any{"library": "Shows", "limit": 1000})
	if rows, total := num(t, out["rows"], "rows"), num(t, paged["total"], "total"); rows != total || rows == 0 {
		t.Errorf("exported %d rows, library_episodes counts %d", rows, total)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // a path this test chose
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != num(t, out["rows"], "rows") || !strings.Contains(lines[0], `"width"`) || !strings.Contains(lines[0], `"series"`) {
		t.Errorf("the file holds %d lines; first: %s", len(lines), lines[0])
	}
	if msg := callErr(t, "library_export", map[string]any{"path": path, "library": "Shows"}); !strings.Contains(msg, "exists") {
		t.Errorf("writing over the file = %q", msg)
	}
}

// The fixtures were probed at their scan and never rewritten, so the audit
// proves its sweep: every file counted, nothing reported.
func TestAuditQualityTrustsTheFixtures(t *testing.T) {
	out := call(t, "audit_quality", map[string]any{"library": "Shows"})
	if n := num(t, out["items_scanned"], "items_scanned"); n < 5 {
		t.Errorf("items_scanned = %d, want every episode", n)
	}
	if n := num(t, out["total_unprobed"], "total_unprobed"); n != 0 {
		t.Errorf("%d files reported unprobed: %v", n, out["unprobed"])
	}
	if n := num(t, out["total_replaced"], "total_replaced"); n != 0 {
		t.Errorf("%d files reported replaced: %v", n, out["replaced"])
	}
}
