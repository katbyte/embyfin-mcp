//go:build integration

package acceptance

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// showNfo is a tvshow.nfo naming a show's ids, which is all a messy show
// library knows of one: its metadata fetchers are off.
func showNfo(title string, ids map[string]string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n<tvshow>\n")
	fmt.Fprintf(&b, "  <title>%s</title>\n", title)
	for _, kind := range slices.Sorted(maps.Keys(ids)) {
		fmt.Fprintf(&b, "  <%sid>%s</%sid>\n  <uniqueid type=%q>%s</uniqueid>\n", kind, ids[kind], kind, kind, ids[kind])
	}
	b.WriteString("</tvshow>\n")

	return []byte(b.String())
}

// seriesCount is how many series a library holds right now.
func seriesCount(t *testing.T, library string) int {
	t.Helper()

	out := call(t, "library_get", map[string]any{"library": library})
	counts, _ := out["type_counts"].(map[string]any)
	n, _ := counts["Series"].(float64)

	return int(n)
}

// Anime against a small list in Anime-Lists' shape (testdata/anime-list.xml):
// a show whose specials are, by the list, an OVA of its own; a special
// matched as the whole show; and an OVA held on its own. The list is a file
// here, so the suite never reaches GitHub.
//
// The shows are staged and taken away again, like the disc audit's streams:
// the messy show library's count is read by tests that have nothing to do
// with anime.
func TestAuditAnimeIDs(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	special, err := os.ReadFile(filepath.Join(dataDir(), "anime-src", "special.mp4")) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}

	have := seriesCount(t, "Messy Shows")
	root := filepath.Join(dataDir(), "messy-shows")
	staged := []string{"Zzyzx Senki (2001)", "Zzyzx Tokubetsu-hen (2003)", "Zzyzx Gaiden (2002)"}
	t.Cleanup(func() {
		for _, folder := range staged {
			_ = os.RemoveAll(filepath.Join(root, folder))
		}
		if _, err := invoke("library_scan", nil); err == nil {
			_ = waitForItems("Messy Shows", have)
		}
	})
	stage := func(folder string, nfo []byte, files ...string) {
		dir := filepath.Join(root, folder)
		mediaMkdir(t, dir)
		mediaWrite(t, filepath.Join(dir, "tvshow.nfo"), nfo)
		for _, f := range files {
			mediaMkdir(t, filepath.Dir(filepath.Join(dir, f)))
			mediaWrite(t, filepath.Join(dir, f), special)
		}
	}
	// specials 3 and 4 of this show are, by the list's TVDB numbers, an OVA
	// with an AniDB entry of its own
	stage(staged[0], showNfo("Zzyzx Senki", map[string]string{"tvdb": "71001", "tmdb": "81001", "anidb": "9101"}),
		"Season 01/Zzyzx Senki S01E01.mp4", "Season 00/Zzyzx Senki S00E03.mp4", "Season 00/Zzyzx Senki S00E04.mp4")
	// a special of TMDB's tv 83001, matched as that whole show
	stage(staged[1], showNfo("Zzyzx Tokubetsu-hen", map[string]string{"tmdb": "83001", "anidb": "9105"}),
		"Season 01/Zzyzx Tokubetsu-hen S01E01.mp4")
	// an OVA TMDB and TVDB fold into another show, held on its own
	stage(staged[2], showNfo("Zzyzx Gaiden", map[string]string{"anidb": "9104"}),
		"Season 01/Zzyzx Gaiden S01E01.mp4")

	call(t, "library_scan", nil)
	if err := waitForItems("Messy Shows", have+3); err != nil {
		t.Fatal(err)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}

	out := call(t, "audit_anime_ids", map[string]any{"library": "Messy Shows"})
	if num(t, out["list_entries"], "list_entries") != 4 || num(t, out["series_scanned"], "series_scanned") != have+3 {
		t.Fatalf("out = %v", out)
	}

	named := func(field, prefix string) map[string]any {
		t.Helper()
		for _, row := range rows(t, out[field], field) {
			if strings.HasPrefix(str(row["series"])+str(row["name"]), prefix) {
				return row
			}
		}
		t.Fatalf("%s holds nothing for %s: %v", field, prefix, out[field])

		return nil
	}

	// the OVA among the show's specials, where the list's TVDB numbers put it
	split := named("split_out", "Zzyzx Senki")
	if str(split["anidb"]) != "AniDB 9102 Zzyzx Senki OVA" || str(split["where"]) != "TVDB specials 3–4" {
		t.Errorf("split_out = %v", split)
	}
	var eps []int
	for _, s := range rows(t, split["specials"], "specials") {
		eps = append(eps, num(t, s["episode"], "episode"))
	}
	if !slices.Equal(eps, []int{3, 4}) {
		t.Errorf("the OVA is specials %v, want 3 and 4", eps)
	}

	// the two that turn on the show's own AniDB id, read from its nfo
	if row := named("ids_disagree", "Zzyzx Tokubetsu-hen"); !strings.Contains(str(row["detail"]), "tv 83001") {
		t.Errorf("ids_disagree = %v", row)
	}
	if row := named("kept_separate", "Zzyzx Gaiden"); str(row["anidb"]) != "AniDB 9104 Zzyzx Gaiden" {
		t.Errorf("kept_separate = %v", row)
	}
}
