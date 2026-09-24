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

// Anime against a few of Anime-Lists' real entries (testdata/anime-list.xml):
// a show whose specials hold, by the list, a special with an AniDB entry of
// its own; a special held as a series and matched as the whole show; and an
// OVA held on its own. The list is a file here, so the suite never reaches
// GitHub.
//
// The OVA held on its own, .hack//Liminality, is a lasting fixture: TMDB and
// TVDB fold it into .hack//SIGN's specials. The other two are Dragon Ball Z
// and The History of Trunks, one of its specials, staged and taken away
// again, since the messy show library's count is read by tests that have
// nothing to do with anime.
func TestAuditAnimeIDs(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	have := seriesCount(t, "Messy Shows")

	// the fixtures alone: .hack//Liminality, and nothing else to say
	out := call(t, "audit_anime_ids", map[string]any{"library": "Messy Shows"})
	kept := rows(t, out["kept_separate"], "kept_separate")
	if num(t, out["list_entries"], "list_entries") != 4 || num(t, out["items_scanned"], "items_scanned") != have || len(kept) != 1 ||
		num(t, out["total_ids_disagree"], "total_ids_disagree") != 0 || num(t, out["total_split_out"], "total_split_out") != 0 {
		t.Fatalf("the fixtures = %v, want .hack//Liminality kept separate and nothing else", out)
	}
	if row := kept[0]; title(str(row["name"])) != ".hack//Liminality" || str(row["anidb"]) != "AniDB 222 .hack//Liminality" ||
		str(row["detail"]) != "an AniDB entry of its own; TMDB folds it into the specials of tv 8864, and TVDB into those of series 79099" {
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
	staged := []string{"Dragon Ball Z (1989)", "Dragon Ball Z The History of Trunks (1993)"}
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
	// the show, by its own ids: its AniDB entry (1530) numbers it straight
	// through, so it gives the show no specials of its own. Special 4 of it
	// is, by the list's TVDB numbers, Bardock - The Father of Goku, a TV
	// special with an AniDB entry of its own (2336). The file is numbered
	// TVDB's way, as a library named by Sonarr is; TMDB lists the same
	// special first today
	stage(staged[0], showNfo("Dragon Ball Z", map[string]string{"tvdb": "81472", "tmdb": "12971", "anidb": "1530"}),
		"Season 01/Dragon Ball Z S01E01 - The New Threat.mp4", "Season 00/Dragon Ball Z S00E04 - Bardock - The Father of Goku.mp4")
	// another special of the show (AniDB 1474), held as a series of its own
	// and matched as the whole show: TMDB's tv 12971, the id it shares with
	// the show
	stage(staged[1], showNfo("Dragon Ball Z: The History of Trunks", map[string]string{"tmdb": "12971", "anidb": "1474"}),
		"Season 01/Dragon Ball Z The History of Trunks S01E01.mp4")
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

	// the special among the show's specials, where the list's TVDB numbers
	// put it: from 4, up to where The History of Trunks begins (11), and the
	// show holds 4 alone
	split := named("split_out", "Dragon Ball Z")
	if str(split["series"]) != "Dragon Ball Z" || str(split["held_also"]) != "" ||
		str(split["anidb"]) != "AniDB 2336 Dragon Ball Z Special: Tatta Hitori no Saishuu Kessen - Freezer ni Idonda Z Senshi Son Gokuu no Chichi" ||
		str(split["where"]) != "TVDB specials from 4" {
		t.Errorf("split_out = %v", split)
	}
	var eps []int
	for _, s := range rows(t, split["specials"], "specials") {
		eps = append(eps, num(t, s["episode"], "episode"))
	}
	if !slices.Equal(eps, []int{4}) {
		t.Errorf("Bardock is specials %v, want 4", eps)
	}
	if n := num(t, out["total_split_out"], "total_split_out"); n != 1 {
		t.Errorf("split_out = %v, want Bardock alone", out["split_out"])
	}

	// the one that turns on the series' own AniDB id, read from its nfo; the
	// show itself, sharing tv 12971 with it, is not one
	if row := named("ids_disagree", "Dragon Ball Z: The History of Trunks"); str(row["anidb"]) != "AniDB 1474 Dragon Ball Z: Zetsubou e no Hankou!! Nokosareta Chousenshi - Gohan to Trunks" ||
		str(row["detail"]) != "its TMDB id is the whole show (tv 12971), but its AniDB id is one of that show's specials: one of the two is wrong" {
		t.Errorf("ids_disagree = %v", row)
	}
	if n := num(t, out["total_ids_disagree"], "total_ids_disagree"); n != 1 {
		t.Errorf("ids_disagree = %v, want The History of Trunks alone", out["ids_disagree"])
	}
	// and .hack//Liminality still kept separate, the one of its kind
	if n := num(t, out["total_kept_separate"], "total_kept_separate"); n != 1 {
		t.Errorf("kept_separate = %v", out["kept_separate"])
	}
	// a limit caps each list, not its count
	if capped := call(t, "audit_anime_ids", map[string]any{"library": "Messy Shows", "limit": 1}); len(rows(t, capped["split_out"], "split_out")) != 1 || num(t, capped["total_split_out"], "total_split_out") != 1 {
		t.Errorf("limit 1 = %v", capped)
	}
}
