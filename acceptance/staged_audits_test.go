//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Audits against a shape staged for them and taken away again: a file cut
// short, which the server cannot read, and a show's folder copied under its
// name with the year added. And the one audit held to the clean fixtures.

// fixtureVideo reads one of the files scripts/testenv.sh made.
func fixtureVideo(t *testing.T, parts ...string) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(append([]string{dataDir()}, parts...)...)) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

// The fixtures were probed at their scan and never rewritten, and are all
// 720p, so the audit proves its sweep: every one of Shows' episodes
// counted, nothing reported.
func TestAuditQualityTrustsTheFixtures(t *testing.T) {
	out := call(t, "audit_quality", map[string]any{"library": "Shows"})
	if n := num(t, out["items_scanned"], "items_scanned"); n != showEpisodes() {
		t.Errorf("items_scanned = %d, want Shows' %d episodes", n, showEpisodes())
	}
	if n := num(t, out["total_findings"], "total_findings"); n != 0 {
		t.Errorf("%d files reported: %v", n, out["findings"])
	}
	if n := num(t, out["total_unprobed"], "total_unprobed"); n != 0 {
		t.Errorf("%d files reported unprobed: %v", n, out["unprobed"])
	}
	if n := num(t, out["total_replaced"], "total_replaced"); n != 0 {
		t.Errorf("%d files reported replaced: %v", n, out["replaced"])
	}
}

// A file cut short in the copying - the first few kilobytes of an episode -
// is an episode the server holds and cannot read: audit_quality lists it
// among the files it could not judge, and not among its findings, since
// nothing about its picture is known. Staged in the messy Severance as its
// sixth episode. Jellyfin gives the cut file its size and no streams, and
// Emby neither, so a size is not taken for a probe.
func TestAuditQualityCannotReadATruncatedFile(t *testing.T) {
	sev := findItem(t, "Messy Shows", "Series", "Severance")
	file := filepath.Join(dataDir(), "messy-shows", "Severance", "Season 01", "Severance S01E06.mp4")
	t.Cleanup(func() {
		_ = os.Remove(file)
		scanUntilTrue(t, "Messy Shows", func() bool { there, _ := held(t, sev, 1, 6); return !there })
	})
	audit := func() (unprobed map[string]any, total int, found bool) {
		out := call(t, "audit_quality", map[string]any{"library": "Messy Shows"})
		_, id := held(t, sev, 1, 6)
		for _, r := range rows(t, out["unprobed"], "unprobed") {
			if id != "" && str(r["id"]) == id {
				unprobed = r
			}
		}
		for _, f := range rows(t, out["findings"], "findings") {
			found = found || id != "" && str(f["id"]) == id
		}
		return unprobed, num(t, out["total_unprobed"], "total_unprobed"), found
	}
	_, before, _ := audit()

	whole := fixtureVideo(t, "messy-shows", "Severance", "Season 01", "Severance S01E01.mp4")
	mediaWrite(t, file, whole[:4096])
	scanUntilTrue(t, "Messy Shows", func() bool { there, _ := held(t, sev, 1, 6); return there })

	row, total, found := audit()
	if row == nil || total != before+1 {
		t.Fatalf("audit_quality lists %v unprobed (%d, %d before), want the cut file among them", row, total, before)
	}
	if !strings.HasSuffix(str(row["path"]), "/Severance S01E06.mp4") || !strings.Contains(str(row["detail"]), "never probed") {
		t.Errorf("the cut file's row = %v", row)
	}
	if found {
		t.Error("audit_quality judges a file it could not read")
	}
}

// A show's folder copied under its name with the year added, the rename a
// tidy-up leaves half done: two folders the name-folding audit keeps apart,
// since a year is more than spelling, and the ids audit puts together, since
// both carry Severance's ids. And both servers list both folders' episodes
// under either entry.
func TestAFolderRenamedWithAYear(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	have := seriesCount(t, "Messy Shows")
	src := filepath.Join(dataDir(), "messy-shows", "Severance")
	dst := filepath.Join(dataDir(), "messy-shows", "Severance (2022)")
	t.Cleanup(func() {
		_ = os.RemoveAll(dst)
		if err := scanUntil("Messy Shows", have); err != nil {
			t.Error(err)
		}
	})
	copyTree(t, src, dst)
	if err := scanUntil("Messy Shows", have+1); err != nil {
		t.Fatal(err)
	}

	for _, g := range rows(t, call(t, "audit_duplicate_series", map[string]any{"library": "Messy Shows"})["groups"], "groups") {
		for _, s := range rows(t, g["series"], "series") {
			if strings.HasPrefix(str(s["folder"]), "Severance") {
				t.Errorf("audit_duplicate_series groups the renamed folder: %v", g)
			}
		}
	}

	var paths, ids []string
	groups, _ := call(t, "audit_duplicates", map[string]any{"library": "Messy Shows"})["groups"].([]any)
	for _, g := range groups {
		// The Wire, split the same way, is the other series group
		group := rowsOf(g)
		if len(group) == 0 || str(group[0]["type"]) != "Series" || str(group[0]["name"]) != "Severance" {
			continue
		}
		for _, s := range group {
			paths = append(paths, str(s["path"]))
			ids = append(ids, str(s["id"]))
		}
	}
	if want := []string{"/media/messy-shows/Severance", "/media/messy-shows/Severance (2022)"}; !slices.Equal(sorted(paths), want) {
		t.Fatalf("audit_duplicates groups the series at %v, want %v", paths, want)
	}

	// what each entry holds: both servers key a show's episodes by the
	// show's ids, so either entry lists both folders' six - a lookup by one
	// entry can answer with the other's file, and show_episodes_exist names
	// the other entry for it. Emby lists each folder's season featurette
	// among them, which it takes for an episode
	want := 6
	if !isJellyfin() {
		want = 8
	}
	for i, id := range ids {
		if n := len(rows(t, call(t, "library_episodes", map[string]any{"series_id": id})["episodes"], "episodes")); n != want {
			t.Errorf("library_episodes lists %d episodes for the series at %s, want both folders' %d", n, paths[i], want)
		}
		out := call(t, "show_episodes_exist", map[string]any{"series_id": id, "episodes": []map[string]any{{"season": 1, "episode": 1}}})
		if other := ids[1-i]; !slices.Contains(strs(t, out["duplicate_entries"], "duplicate_entries"), other) {
			t.Errorf("show_episodes_exist for the series at %s names %v as its other entries, want %s among them", paths[i], out["duplicate_entries"], other)
		}
	}
}
