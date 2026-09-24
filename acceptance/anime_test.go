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
// The OVA held on its own, Zzyzx Gaiden, is a lasting fixture; the other two
// are staged and taken away again, since the messy show library's count is
// read by tests that have nothing to do with anime.
func TestAuditAnimeIDs(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	have := seriesCount(t, "Messy Shows")

	// the fixtures alone: Gaiden, and nothing else to say
	out := call(t, "audit_anime_ids", map[string]any{"library": "Messy Shows"})
	kept := rows(t, out["kept_separate"], "kept_separate")
	if num(t, out["list_entries"], "list_entries") != 4 || num(t, out["items_scanned"], "items_scanned") != have || len(kept) != 1 ||
		num(t, out["total_ids_disagree"], "total_ids_disagree") != 0 || num(t, out["total_split_out"], "total_split_out") != 0 {
		t.Fatalf("the fixtures = %v, want Gaiden kept separate and nothing else", out)
	}
	if row := kept[0]; title(str(row["name"])) != "Zzyzx Gaiden" || str(row["anidb"]) != "AniDB 9104 Zzyzx Gaiden" ||
		str(row["detail"]) != "an AniDB entry of its own; TMDB folds it into the specials of tv 82001, and TVDB into those of series 72001" {
		t.Errorf("kept_separate = %v", row)
	}
	if n := num(t, call(t, "audit_anime_ids", map[string]any{"library": "Shows"})["total_kept_separate"], "total_kept_separate"); n != 0 {
		t.Errorf("the clean shows hold %d anime kept separate", n)
	}

	special, err := os.ReadFile(filepath.Join(dataDir(), "anime-src", "special.mp4")) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dataDir(), "messy-shows")
	staged := []string{"Zzyzx Senki (2001)", "Zzyzx Tokubetsu-hen (2003)"}
	t.Cleanup(func() {
		for _, folder := range staged {
			_ = os.RemoveAll(filepath.Join(root, folder))
		}
		if err := scanUntil("Messy Shows", have); err != nil {
			t.Error(err)
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
	if err := scanUntil("Messy Shows", have+2); err != nil {
		t.Fatal(err)
	}

	out = call(t, "audit_anime_ids", map[string]any{"library": "Messy Shows"})
	if num(t, out["list_entries"], "list_entries") != 4 || num(t, out["items_scanned"], "items_scanned") != have+2 {
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

	// the one that turns on the show's own AniDB id, read from its nfo
	if row := named("ids_disagree", "Zzyzx Tokubetsu-hen"); !strings.Contains(str(row["detail"]), "tv 83001") {
		t.Errorf("ids_disagree = %v", row)
	}
	// and Gaiden still kept separate, the one of its kind
	if n := num(t, out["total_kept_separate"], "total_kept_separate"); n != 1 {
		t.Errorf("kept_separate = %v", out["kept_separate"])
	}
	// a limit caps each list, not its count
	if capped := call(t, "audit_anime_ids", map[string]any{"library": "Messy Shows", "limit": 1}); len(rows(t, capped["split_out"], "split_out")) != 1 || num(t, capped["total_split_out"], "total_split_out") != 1 {
		t.Errorf("limit 1 = %v", capped)
	}
}
