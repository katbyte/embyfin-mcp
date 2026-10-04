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

	acc "github.com/katbyte/embyfin-mcp/lib/acceptance"

	"github.com/katbyte/embyfin-mcp/lib/testenv"
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

// Anime against a few of Anime-Lists' real entries (testdata/anime-list.xml):
// a show whose specials hold, by the list, a special with an AniDB entry of
// its own; a special held as a series and matched as the whole show; and an
// OVA held on its own. The list is a file here, so the suite never reaches
// GitHub.
//
// The OVA held on its own, .hack//Liminality, is a lasting fixture: TMDB and
// TVDB fold it into .hack//SIGN's specials, and the library holds .hack//SIGN
// too, with the special TVDB numbers 2 - by the list Liminality's first
// episode, held twice. The other two are Dragon Ball Z
// and The History of Trunks, one of its specials, staged and taken away
// again, since the messy show library's count is read by tests that have
// nothing to do with anime.
func TestAuditAnimeIDs(t *testing.T) {
	if testenv.DataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	have := typeCount(t, "Messy Shows", "Series")

	// the fixtures alone: .hack//Liminality kept separate, and its first
	// episode held again among .hack//SIGN's specials
	out := suite.Call(t, "audit_anime_ids", map[string]any{"library": "Messy Shows"})
	kept := acc.Rows(t, out["kept_separate"], "kept_separate")
	if acc.Num(t, out["list_entries"], "list_entries") != 5 || acc.Num(t, out["items_scanned"], "items_scanned") != have || len(kept) != 1 ||
		acc.Num(t, out["total_ids_disagree"], "total_ids_disagree") != 0 || acc.Num(t, out["total_split_out"], "total_split_out") != 1 {
		t.Fatalf("the fixtures = %v, want .hack//Liminality kept separate and held again in .hack//SIGN, nothing else", out)
	}
	sign := findItem(t, "Messy Shows", "Series", ".hack//SIGN")
	held := acc.Rows(t, out["split_out"], "split_out")[0]
	var heldEps []string
	for _, sp := range acc.Rows(t, held["specials"], "specials") {
		heldEps = append(heldEps, fmt.Sprintf("%d %s", acc.Num(t, sp["episode"], "episode"), acc.Str(sp["name"])))
	}
	if acc.Str(held["series_id"]) != sign || acc.Str(held["anidb"]) != "AniDB 222 .hack//Liminality" || acc.Str(held["where"]) != "TVDB specials from 2" ||
		acc.Str(held["held_also"]) != acc.Str(kept[0]["id"]) || !slices.Equal(heldEps, []string{"2 In the Case of Mai Minase"}) {
		t.Errorf("split_out = %v, want .hack//SIGN's special 2 as Liminality's, held also as the series", held)
	}
	if row := kept[0]; title(acc.Str(row["name"])) != ".hack//Liminality" || acc.Str(row["anidb"]) != "AniDB 222 .hack//Liminality" ||
		acc.Str(row["detail"]) != "an AniDB entry of its own; TMDB folds it into the specials of tv 8864, and TVDB into those of series 79099" {
		t.Errorf("kept_separate = %v", row)
	}
	if n := acc.Num(t, suite.Call(t, "audit_anime_ids", map[string]any{"library": "Shows"})["total_kept_separate"], "total_kept_separate"); n != 0 {
		t.Errorf("the clean shows hold %d anime kept separate", n)
	}

	special, err := os.ReadFile(filepath.Join(testenv.DataDir(), "anime-src", "special.mp4")) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(testenv.DataDir(), "messy-shows")
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
		acc.MediaMkdir(t, testenv.DataDir(), dir)
		acc.MediaWrite(t, filepath.Join(dir, "tvshow.nfo"), nfo)
		for _, f := range files {
			acc.MediaMkdir(t, testenv.DataDir(), filepath.Dir(filepath.Join(dir, f)))
			acc.MediaWrite(t, filepath.Join(dir, f), special)
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

	out = suite.Call(t, "audit_anime_ids", map[string]any{"library": "Messy Shows"})
	if acc.Num(t, out["list_entries"], "list_entries") != 5 || acc.Num(t, out["items_scanned"], "items_scanned") != have+2 {
		t.Fatalf("out = %v", out)
	}

	named := func(field, prefix string) map[string]any {
		t.Helper()
		for _, row := range acc.Rows(t, out[field], field) {
			if strings.HasPrefix(acc.Str(row["series"])+acc.Str(row["name"]), prefix) {
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
	if acc.Str(split["series"]) != "Dragon Ball Z" || acc.Str(split["held_also"]) != "" ||
		acc.Str(split["anidb"]) != "AniDB 2336 Dragon Ball Z Special: Tatta Hitori no Saishuu Kessen - Freezer ni Idonda Z Senshi Son Gokuu no Chichi" ||
		acc.Str(split["where"]) != "TVDB specials from 4" {
		t.Errorf("split_out = %v", split)
	}
	var eps []int
	for _, s := range acc.Rows(t, split["specials"], "specials") {
		eps = append(eps, acc.Num(t, s["episode"], "episode"))
	}
	if !slices.Equal(eps, []int{4}) {
		t.Errorf("Bardock is specials %v, want 4", eps)
	}
	if n := acc.Num(t, out["total_split_out"], "total_split_out"); n != 2 {
		t.Errorf("split_out = %v, want Bardock and .hack//SIGN's special", out["split_out"])
	}

	// the one that turns on the series' own AniDB id, read from its nfo; the
	// show itself, sharing tv 12971 with it, is not one
	if row := named("ids_disagree", "Dragon Ball Z: The History of Trunks"); acc.Str(row["anidb"]) != "AniDB 1474 Dragon Ball Z: Zetsubou e no Hankou!! Nokosareta Chousenshi - Gohan to Trunks" ||
		acc.Str(row["detail"]) != "its TMDB id is the whole show (tv 12971), but its AniDB id is one of that show's specials: one of the two is wrong" {
		t.Errorf("ids_disagree = %v", row)
	}
	if n := acc.Num(t, out["total_ids_disagree"], "total_ids_disagree"); n != 1 {
		t.Errorf("ids_disagree = %v, want The History of Trunks alone", out["ids_disagree"])
	}
	// and .hack//Liminality still kept separate, the one of its kind
	if n := acc.Num(t, out["total_kept_separate"], "total_kept_separate"); n != 1 {
		t.Errorf("kept_separate = %v", out["kept_separate"])
	}
	// a limit caps each list, not its count
	if capped := suite.Call(t, "audit_anime_ids", map[string]any{"library": "Messy Shows", "limit": 1}); len(acc.Rows(t, capped["split_out"], "split_out")) != 1 || acc.Num(t, capped["total_split_out"], "total_split_out") != 2 {
		t.Errorf("limit 1 = %v", capped)
	}

	// More of the show's specials, as a library holding them all would:
	// where the list gives an entry only its first special, the entry runs
	// over the specials held after it, and stops at a gap, at a special too
	// short to be one (an opening, a trailer), or where the next entry
	// begins. The History of Trunks' place, 11, is the next entry, and the
	// library holds that one as a series of its own as well.
	dbz, trunks := acc.Str(split["series_id"]), acc.Str(named("ids_disagree", "Dragon Ball Z: The History of Trunks")["id"])
	// the two share TMDB's tv 12971, and their AniDB ids say they are two
	// shows: neither is named as the other's second entry, where a shared
	// id alone used to join them, but each is named apart as an anime entry
	// an episode may be filed under, and an absence warns of it
	for id, other := range map[string]string{dbz: trunks, trunks: dbz} {
		out := suite.Call(t, "show_episodes_exist", map[string]any{"series_id": id, "episodes": []map[string]any{{"season": 1, "episode": 99}}})
		if slices.Contains(acc.Strs(t, acc.OrEmptyList(out["duplicate_entries"]), "duplicate_entries"), other) {
			t.Errorf("show_episodes_exist %s names %s as its other entry: %v", id, other, out["duplicate_entries"])
		}
		if !slices.Contains(acc.Strs(t, acc.OrEmptyList(out["anime_entries"]), "anime_entries"), other) || !strings.Contains(acc.Str(out["warning"]), "under another AniDB id (id "+other+" at ") {
			t.Errorf("show_episodes_exist %s, S01E99 absent: anime_entries %v, warning %q: want %s named apart", id, out["anime_entries"], out["warning"], other)
		}
	}
	season0 := filepath.Join(root, staged[0], "Season 00")
	short := fixture(t, "messy-shows/hack Liminality (2002)/Season 01/hack Liminality S01E03.mp4") // a second long
	// lays specials out, or takes them away, and scans until the library
	// holds that many episodes
	lay := func(files map[string][]byte, remove ...string) {
		t.Helper()
		want := typeCount(t, "Messy Shows", "Episode") + len(files) - len(remove)
		for _, f := range remove {
			if err := os.Remove(filepath.Join(season0, f)); err != nil {
				t.Fatal(err)
			}
		}
		for f, raw := range files {
			acc.MediaWrite(t, filepath.Join(season0, f), raw)
		}
		rescanUntil(t, "Dragon Ball Z's specials", func() bool { return typeCount(t, "Messy Shows", "Episode") == want })
	}
	splits := func() []string {
		t.Helper()
		out := suite.Call(t, "audit_anime_ids", map[string]any{"library": "Messy Shows"})
		var got []string
		for _, row := range acc.Rows(t, out["split_out"], "split_out") {
			// .hack//SIGN's special is the fixtures' own, read above
			if acc.Str(row["series_id"]) == sign {
				continue
			}
			var eps []string
			for _, s := range acc.Rows(t, row["specials"], "specials") {
				eps = append(eps, fmt.Sprint(acc.Num(t, s["episode"], "episode")))
			}
			entry, _, _ := strings.Cut(strings.TrimPrefix(acc.Str(row["anidb"]), "AniDB "), " ")
			line := fmt.Sprintf("%s %s: %s", entry, row["where"], strings.Join(eps, " "))
			switch held := acc.Str(row["held_also"]); {
			case held == trunks:
				line += ", held also"
			case held != "":
				line += ", held also as " + held
			}
			if acc.Str(row["series_id"]) != dbz {
				line += " in " + acc.Str(row["series"])
			}
			got = append(got, line)
		}
		if n := acc.Num(t, out["total_split_out"], "total_split_out"); n != len(got)+1 {
			t.Errorf("total_split_out = %d for %d rows and .hack//SIGN's", n, len(got))
		}

		return got
	}

	lay(map[string][]byte{
		"Dragon Ball Z S00E05.mp4": special,
		"Dragon Ball Z S00E06.mp4": short,
		"Dragon Ball Z S00E07.mp4": special,
		"Dragon Ball Z S00E11.mp4": special,
	})
	if got, want := splits(), []string{"2336 TVDB specials from 4: 4 5", "1474 TVDB specials from 11: 11, held also"}; !slices.Equal(got, want) {
		t.Errorf("with specials 4 to 7 and 11 held, the sixth a second long = %v, want %v", got, want)
	}
	// specials are no season to be missing episodes from: 4 to 7 and 11
	// leave 8 to 10 out, and that is not a gap
	if got := findings(t, suite.Call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows"})); slices.Contains(got, "Dragon Ball Z") {
		t.Errorf("series with gaps = %v, want Dragon Ball Z's specials left out of it", got)
	}

	lay(nil, "Dragon Ball Z S00E06.mp4")
	lay(map[string][]byte{
		"Dragon Ball Z S00E06.mp4": special,
		"Dragon Ball Z S00E08.mp4": special,
		"Dragon Ball Z S00E09.mp4": special,
		"Dragon Ball Z S00E10.mp4": special,
	})
	whole := []string{"2336 TVDB specials from 4: 4 5 6 7 8 9 10", "1474 TVDB specials from 11: 11, held also"}
	if got := splits(); !slices.Equal(got, whole) {
		t.Errorf("with specials 4 to 11 held = %v, want %v", got, whole)
	}

	// placed by TMDB's numbers where the show holds no TVDB id: the list
	// gives both entries the same places there
	t.Run("by TMDB", func(t *testing.T) {
		setIDs(t, dbz, map[string]any{"Tmdb": "12971", "AniDB": "1530"})
		if got, want := splits(), []string{"2336 TMDB specials from 4: 4 5 6 7 8 9 10", "1474 TMDB specials from 11: 11, held also"}; !slices.Equal(got, want) {
			t.Errorf("by TMDB = %v, want %v", got, want)
		}
	})
	// The History of Trunks holding the show's TVDB id rather than its TMDB
	// one disagrees with its AniDB id the same way, said of TVDB
	t.Run("by TVDB", func(t *testing.T) {
		setIDs(t, trunks, map[string]any{"Tvdb": "81472", "AniDB": "1474"})
		out := suite.Call(t, "audit_anime_ids", map[string]any{"library": "Messy Shows"})
		disagree := acc.Rows(t, out["ids_disagree"], "ids_disagree")
		if len(disagree) != 1 || acc.Str(disagree[0]["id"]) != trunks || acc.Str(disagree[0]["detail"]) != "its TVDB id is the whole show (series 81472), but its AniDB id is one of that show's specials: one of the two is wrong" {
			t.Errorf("ids_disagree = %v", disagree)
		}
	})
}
