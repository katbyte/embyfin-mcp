//go:build integration

package acceptance

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/go-kt/test/env"

	"github.com/katbyte/embyfin-mcp/lib/testenv"
)

// The tools for writing into a library safely: reading destinations before
// writing them, and finding the damage shapes afterwards.

// plan_check answers what is at each destination now, which series it would
// join, and which entries collide with each other.
func TestPlanCheck(t *testing.T) {
	sev := findItem(t, "Shows", "Series", "Severance")
	held := acc.Rows(t, suite.Call(t, "library_episodes", map[string]any{"series_id": sev})["episodes"], "episodes")
	if len(held) == 0 {
		t.Fatal("Severance holds no episodes")
	}
	occupied := acc.Str(held[0]["path"])
	free := filepath.Join(filepath.Dir(occupied), "Severance S01E09 - Nothing Here.mp4")

	out := suite.Call(t, "plan_check", map[string]any{"entries": []map[string]any{
		{"path": occupied, "size": 12_000_000, "series": "Severance", "season": 1, "episode": 1},
		{"path": free, "series": "Severance", "season": 1, "episode": 9},
		{"path": free, "series": "Severance", "season": 1, "episode": 9},
		{"path": "/nowhere/at/all/Some Show S01E01.mkv"},
	}})
	entries := acc.Rows(t, out["entries"], "entries")
	if len(entries) != 4 {
		t.Fatalf("entries = %v", entries)
	}

	// the occupied one names what is there, from the server's own facts
	first := entries[0]
	if first["exists"] != true {
		t.Fatalf("a path the library holds read as free: %v", first)
	}
	current := acc.Object(t, first["current"], "current")
	if acc.Str(current["item_id"]) != acc.Str(held[0]["id"]) || acc.Num(t, current["runtime_s"], "runtime_s") <= 0 {
		t.Errorf("current = %v", current)
	}
	// the incoming size against what is there, to the hundredth, so a grow
	// or a shrink is visible before it happens rather than after
	size := acc.Num(t, held[0]["size"], "size")
	if want := math.Round(12_000_000/float64(size)*100) / 100; acc.Decimal(t, current["size_ratio"], "size_ratio") != want || acc.Num(t, current["size"], "size") != size {
		t.Errorf("current = %v, want %d bytes and a size ratio of %v", current, size, want)
	}
	if join := acc.Object(t, first["would_join"], "would_join"); acc.Str(join["series_id"]) != sev {
		t.Errorf("would_join = %v", join)
	}

	// the free path in the same folder: nothing there, same series
	second := entries[1]
	if second["exists"] != false || second["current"] != nil {
		t.Errorf("a free path = %v", second)
	}
	if join := acc.Object(t, second["would_join"], "would_join"); acc.Str(join["series_id"]) != sev {
		t.Errorf("a free path did not name its series: %v", join)
	}

	// two entries onto one path name each other
	if dups := acc.Strs(t, entries[2]["duplicate_of"], "duplicate_of"); len(dups) == 0 {
		t.Errorf("the colliding entries were not flagged: %v", entries[2])
	}
	if acc.Num(t, out["duplicates"], "duplicates") != 2 {
		t.Errorf("duplicates = %v, want 2", out["duplicates"])
	}

	// and a path under no series folder says so
	if outside := entries[3]; outside["would_join"] != nil || !strings.Contains(acc.Str(outside["note"]), "no series folder") {
		t.Errorf("a path outside the library = %v", outside)
	}
	if acc.Num(t, out["unplaced"], "unplaced") != 1 {
		t.Errorf("unplaced = %v, want 1", out["unplaced"])
	}
}

// A zero is plan_check's most important answer, and it gives it: an empty
// file about to replace one of Severance's episodes is a size ratio of 0, and
// Breaking Bad claimed for a path under Severance's folder a similarity of 0.
// Both were left out of the answer, which read as if no size or series had
// been given.
func TestPlanCheckGivesAZero(t *testing.T) {
	sev := findItem(t, "Shows", "Series", "Severance")
	held := acc.Rows(t, suite.Call(t, "library_episodes", map[string]any{"series_id": sev})["episodes"], "episodes")
	if len(held) == 0 {
		t.Fatal("Severance holds no episodes")
	}
	out := suite.Call(t, "plan_check", map[string]any{"entries": []map[string]any{
		{"path": acc.Str(held[0]["path"]), "size": 0, "series": "Breaking Bad", "season": 1, "episode": 1},
	}})
	row := acc.Rows(t, out["entries"], "entries")[0]
	current := acc.Object(t, row["current"], "current")
	if ratio, ok := current["size_ratio"]; !ok || acc.Decimal(t, ratio, "size_ratio") != 0 || acc.Num(t, current["size"], "size") <= 0 {
		t.Errorf("an empty file over %s: current = %v, want its size and a size ratio of 0", held[0]["path"], current)
	}
	join := acc.Object(t, row["would_join"], "would_join")
	if score, ok := join["claim_similarity"]; acc.Str(join["series_id"]) != sev || !ok || acc.Decimal(t, score, "claim_similarity") != 0 {
		t.Errorf("Breaking Bad claimed under Severance's folder: would_join = %v, want Severance at a similarity of 0", join)
	}
}

// The mistakes plan_check is there to catch before the write: a file headed
// for another show's folder, a smaller file about to replace a bigger one,
// two files claiming one episode, and a file headed for the folder of the
// copy of a show held twice. And a film's path, which Emby looks up by the
// path itself and Jellyfin cannot, so says it does not know.
func TestPlanCheckCatchesMistakes(t *testing.T) {
	ds9 := findItem(t, "Messy Shows", "Series", "Star Trek: Deep Space Nine")
	tng := findItem(t, "Messy Shows", "Series", "Star Trek The Next Generation")
	var tngFirst map[string]any
	for _, e := range acc.Rows(t, suite.Call(t, "library_episodes", map[string]any{"series_id": tng})["episodes"], "episodes") {
		if acc.Num(t, e["episode"], "episode") == 1 {
			tngFirst = e
		}
	}
	if tngFirst == nil {
		t.Fatal("Star Trek The Next Generation holds no S01E01")
	}
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	const knight = "A Knight of the Seven Kingdoms"
	var copyOf map[string]any
	for _, s := range acc.Rows(t, suite.Call(t, "audit_duplicates", map[string]any{"library": "Messy Shows"})["folder_groups"], "folder_groups") {
		for _, row := range acc.Rows(t, s["series"], "series") {
			if acc.Str(row["folder"]) != knight+" (2026)" {
				copyOf = row
			}
		}
	}
	if copyOf == nil {
		t.Fatal("audit_duplicates finds no copy of the show held twice among its folder groups")
	}

	size := acc.Num(t, tngFirst["size"], "size")
	wrongShow := "/media/messy-shows/Star Trek Deep Space Nine (1993)/Season 01/Star Trek The Next Generation S01E04.mp4"
	tngDir := filepath.Dir(acc.Str(tngFirst["path"]))
	film := "/media/movies/Arrival (2016)/Arrival (2016).mp4"
	freeFilm := "/media/movies/Arrival (2016)/Arrival (2016) - 1080p.mp4"
	out := suite.Call(t, "plan_check", map[string]any{"entries": []map[string]any{
		// the wrong Star Trek's folder
		{"path": wrongShow, "series": "Star Trek The Next Generation", "season": 1, "episode": 4},
		// half the size of the file it would replace
		{"path": acc.Str(tngFirst["path"]), "size": size / 2, "series": "Star Trek The Next Generation", "season": 1, "episode": 1},
		// two names for one episode
		{"path": filepath.Join(tngDir, "Star Trek The Next Generation S01E02.mp4"), "series": "Star Trek The Next Generation", "season": 1, "episode": 2},
		{"path": filepath.Join(tngDir, "Star.Trek.The.Next.Generation.S01E02.mkv"), "series": "Star Trek The Next Generation", "season": 1, "episode": 2},
		// the copy's folder, a space and a letter's case from the real one
		{"path": acc.Str(copyOf["path"]) + "/Season 01/" + knight + " S01E03.mp4", "series": knight, "season": 1, "episode": 3},
		// a film held, and a free path beside it
		{"path": film},
		{"path": freeFilm},
	}})
	entries := acc.Rows(t, out["entries"], "entries")
	if len(entries) != 7 {
		t.Fatalf("entries = %v", entries)
	}

	// under Deep Space Nine's folder, which the claim does not name: a low
	// similarity, and the show it would really join
	if join := acc.Object(t, entries[0]["would_join"], "would_join"); acc.Str(join["series_id"]) != ds9 || acc.Str(join["claimed_series"]) != "Star Trek The Next Generation" || acc.Decimal(t, join["claim_similarity"], "claim_similarity") >= 0.9 {
		t.Errorf("a file for the wrong show's folder = %v, want Deep Space Nine at a low similarity", join)
	}
	if entries[0]["exists"] != false {
		t.Errorf("the wrong show's free path = %v", entries[0])
	}

	// a smaller file over a bigger one
	if current := acc.Object(t, entries[1]["current"], "current"); acc.Str(current["item_id"]) != acc.Str(tngFirst["id"]) || acc.Decimal(t, current["size_ratio"], "size_ratio") != math.Round(float64(size/2)/float64(size)*100)/100 {
		t.Errorf("a smaller file = %v, want a size ratio below 1", current)
	}

	// the two names for S01E02 name each other, and only each other
	for i, other := range map[int]int{2: 3, 3: 2} {
		if dups := acc.Strs(t, entries[i]["duplicate_of"], "duplicate_of"); !slices.Equal(dups, []string{acc.Str(entries[other]["path"])}) {
			t.Errorf("entry %d duplicate_of = %v, want the other name for S01E02", i, dups)
		}
	}
	if acc.Num(t, out["duplicates"], "duplicates") != 2 {
		t.Errorf("duplicates = %v, want the two names for S01E02", out["duplicates"])
	}

	// the copy's folder joins the copy, which the claim scores as the show
	// itself: series_path is what tells them apart
	if join := acc.Object(t, entries[4]["would_join"], "would_join"); acc.Str(join["series_id"]) != acc.Str(copyOf["series_id"]) || acc.Str(join["series_path"]) != acc.Str(copyOf["path"]) {
		t.Errorf("a file for the copy's folder = %v, want the copy at %s", join, copyOf["path"])
	}

	// a film held, which both servers find: Emby by the path, Jellyfin by the
	// title the path names
	if current, ok := entries[5]["current"].(map[string]any); !ok || entries[5]["exists"] != true || acc.Str(current["item_id"]) != arrival {
		t.Errorf("a film's path = %v, want Arrival there", entries[5])
	}
	// and the free path beside it: the disk has nothing there on both
	// servers, but only Emby's library can be asked which item is there -
	// on Jellyfin an item whose file is gone could still be at it, so
	// whether a write replaces anything is not known
	free := entries[6]
	if isJellyfin() {
		if !acc.IsBool(free["checked"], false) || free["exists"] != nil || !acc.IsBool(free["on_disk"], false) || free["in_library"] != nil || !strings.Contains(acc.Str(free["note"]), "exists is true only when the server's disk has a file here, and null otherwise") {
			t.Errorf("a free film path on Jellyfin = %v, want nothing on the disk, the library not asked, exists not known", free)
		}
	} else if !acc.IsBool(free["checked"], true) || !acc.IsBool(free["exists"], false) || !acc.IsBool(free["on_disk"], false) || !acc.IsBool(free["in_library"], false) {
		t.Errorf("a free film path on Emby = %v, want free", free)
	}
	wantUnchecked := 0
	if isJellyfin() {
		wantUnchecked = 1
	}
	if acc.Num(t, out["unchecked"], "unchecked") != wantUnchecked || acc.Num(t, out["existing"], "existing") != 2 {
		t.Errorf("unchecked %v, existing %v, want %d and 2", out["unchecked"], out["existing"], wantUnchecked)
	}
}

// One title on two episode numbers: the fixtures carry Breaking Bad's
// second title on its third episode too, both TMDB's 49 minutes and seconds
// apart, as two episodes run. A second pair is staged in the messy Severance
// - Half Loop's own file again as S01E05 - so a limit has more than one
// group to cap, and that one the files tie together.
func TestAuditDuplicateEpisodes(t *testing.T) {
	if testenv.DataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	out := suite.Call(t, "audit_duplicate_episodes", map[string]any{"library": "Shows"})
	groups := acc.Rows(t, out["groups"], "groups")
	if len(groups) != 1 || acc.Num(t, out["total_findings"], "total_findings") != 1 {
		t.Fatalf("groups = %v", groups)
	}
	group := groups[0]
	if title(acc.Str(group["series"])) != "Breaking Bad" || !strings.Contains(acc.Str(group["title"]), "Cat") {
		t.Errorf("group = %v", group)
	}
	// within 15% of each other and not the same to the second or the byte:
	// nothing in the files ties them, so it is a lead, with no evidence
	if acc.Str(group["confidence"]) != "lead" || group["evidence"] != nil {
		t.Errorf("confidence = %v, evidence %v (runtime_gap %v)", group["confidence"], group["evidence"], group["runtime_gap"])
	}
	eps := acc.Rows(t, group["episodes"], "episodes")
	if len(eps) != 2 || acc.Num(t, eps[0]["episode"], "episode") != 2 || acc.Num(t, eps[1]["episode"], "episode") != 3 {
		t.Errorf("the group is not E02 and E03: %v", eps)
	}
	if acc.Str(eps[0]["path"]) == "" || acc.Num(t, eps[0]["size"], "size") <= 0 {
		t.Errorf("a row lacks what a caller decides on: %v", eps[0])
	}
	// the messy shows' titles are all their own
	if n := acc.Num(t, suite.Call(t, "audit_duplicate_episodes", nil)["total_findings"], "total_findings"); n != 1 {
		t.Errorf("across the server %d groups, want the Breaking Bad pair alone", n)
	}

	sev := findItem(t, "Messy Shows", "Series", "Severance")
	base := filepath.Join(testenv.DataDir(), "messy-shows", "Severance", "Season 01", "Severance S01E05")
	t.Cleanup(func() {
		_ = os.Remove(base + ".mp4")
		_ = os.Remove(base + ".nfo")
		scanUntilTrue(t, "Messy Shows", func() bool { there, _ := held(t, sev, 1, 5); return !there })
	})
	env.WriteFile(t, base+".mp4", fixtureVideo(t, "messy-shows", "Severance", "Season 01", "Severance S01E02.mp4"))
	env.WriteFile(t, base+".nfo", episodeNfo("Half Loop", 1, 5))
	scanUntilTrue(t, "Messy Shows", func() bool { there, _ := held(t, sev, 1, 5); return there })

	// two pairs across the server; a limit of one lists one and counts both
	whole := suite.Call(t, "audit_duplicate_episodes", nil)
	var series []string
	for _, g := range acc.Rows(t, whole["groups"], "groups") {
		series = append(series, title(acc.Str(g["series"])))
	}
	if !slices.Equal(acc.Sorted(series), []string{"Breaking Bad", "Severance"}) || acc.Num(t, whole["total_findings"], "total_findings") != 2 {
		t.Errorf("across the server with the pair staged = %v", series)
	}
	if capped := suite.Call(t, "audit_duplicate_episodes", map[string]any{"limit": 1}); len(acc.Rows(t, capped["groups"], "groups")) != 1 || acc.Num(t, capped["total_findings"], "total_findings") != 2 {
		t.Errorf("limit 1 = %v, want one group listed and both counted", capped)
	}
	// the staged pair is one file twice: the same runtime to the second,
	// where the season's other episodes run lengths of their own, so near
	// certain
	for _, g := range acc.Rows(t, whole["groups"], "groups") {
		if title(acc.Str(g["series"])) != "Severance" {
			continue
		}
		if acc.Str(g["confidence"]) != "near_certain" || !slices.Contains(acc.Strs(t, g["evidence"], "evidence"), "E02 and E05: the same runtime to the second") {
			t.Errorf("the staged Severance pair = %v, evidence %v, want near_certain by its runtime", g["confidence"], g["evidence"])
		}
	}
}

// A file named after a different episode than the one the server holds: the
// fixture names The Expanse S01E02's file "Dulcinea", which is S01E01. Every
// other path in the show library agrees with the server: the series
// folders, the season and episode numbers, the files that claim no title.
func TestAuditFilePathShows(t *testing.T) {
	out := suite.Call(t, "audit_file_path", map[string]any{"library": "Shows"})
	findings := acc.Rows(t, out["findings"], "findings")
	if len(findings) != 1 || acc.Num(t, out["total_findings"], "total_findings") != 1 {
		t.Fatalf("findings = %v", findings)
	}
	f := findings[0]
	if acc.Str(f["title_in_file"]) != "Dulcinea" || acc.Str(f["title_on_server"]) != "The Big Empty" || acc.Str(f["type"]) != "Episode" {
		t.Errorf("finding = %v", f)
	}
	if problems, _ := f["problems"].([]any); len(problems) != 1 || !strings.HasPrefix(acc.Str(problems[0]), "title:") {
		t.Errorf("problems = %v, want the title alone", f["problems"])
	}
	if acc.Num(t, f["episode"], "episode") != 2 || !strings.Contains(acc.Str(f["path"]), "Dulcinea") {
		t.Errorf("finding names the wrong episode: %v", f)
	}
	// the row says where TMDB puts the file's title, asked through the
	// recording: the first episode, so the file is numbered in another order
	if acc.Str(f["tmdb_episode"]) != "S01E01" || !strings.Contains(acc.Str(f["diagnosis"]), "another order") {
		t.Errorf("diagnosis = %v, want the file's title placed at TMDB's S01E01", f)
	}
	// the files whose names claim no title at all are counted, not reported:
	// the rest of the library's
	if acc.Num(t, out["unnamed"], "unnamed") != showEpisodes()-1 {
		t.Errorf("unnamed = %v, want the rest of the library's %d episodes", out["unnamed"], showEpisodes()-1)
	}
}

// Two folders for one show, a space and a letter's case apart: the messy
// A Knight of the Seven Kingdoms pair, and the only collision across every
// series the server holds. The two Severances are one show in two
// libraries, which is not this audit's business (audit_duplicates groups
// them by their ids). A second collision is staged - The Next Generation
// again under a name a dash apart - so a limit has more than one to cap.
func TestAuditDuplicateSeries(t *testing.T) {
	if testenv.DataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	out := suite.Call(t, "audit_duplicates", nil)
	groups := acc.Rows(t, out["folder_groups"], "folder_groups")
	if len(groups) != 1 || acc.Num(t, out["total_folder_groups"], "total_folder_groups") != 1 {
		t.Fatalf("folder_groups = %v, want the A Knight of the Seven Kingdoms pair alone", groups)
	}
	// counted among the findings beside the groups sharing an id
	if n, ids := acc.Num(t, out["total_findings"], "total_findings"), len(acc.RowsOfAny(out["groups"])); n != ids+1 {
		t.Errorf("total_findings = %d, want the %d groups sharing an id and the folder pair", n, ids)
	}
	var folders []string
	for _, s := range acc.Rows(t, groups[0]["series"], "series") {
		folders = append(folders, acc.Str(s["folder"]))
		if acc.Str(s["series_id"]) == "" || acc.Num(t, s["year"], "year") != 2026 || !strings.HasPrefix(acc.Str(s["path"]), "/media/messy-shows/") {
			t.Errorf("series = %v", s)
		}
	}
	if !slices.Equal(acc.Sorted(folders), []string{"A Knight of the Seven  kingdoms (2026)", "A Knight of the Seven Kingdoms (2026)"}) {
		t.Errorf("the pair = %v", folders)
	}
	if n := acc.Num(t, suite.Call(t, "audit_duplicates", map[string]any{"library": "Shows"})["total_folder_groups"], "total_folder_groups"); n != 0 {
		t.Errorf("Shows = %d folder groups", n)
	}
	// films alone: the folder rule is series', and reads nothing
	if out := suite.Call(t, "audit_duplicates", map[string]any{"library": "Messy Shows", "types": "Movie"}); acc.Num(t, out["total_folder_groups"], "total_folder_groups") != 0 || len(acc.Rows(t, out["folder_groups"], "folder_groups")) != 0 {
		t.Errorf("audit_duplicates over films = %v, want no folder groups", out)
	}

	have := typeCount(t, "Messy Shows", "Series")
	src := filepath.Join(testenv.DataDir(), "messy-shows", "Star Trek The Next Generation")
	dst := filepath.Join(testenv.DataDir(), "messy-shows", "Star Trek - The Next Generation")
	t.Cleanup(func() {
		_ = os.RemoveAll(dst)
		if err := scanUntil("Messy Shows", have); err != nil {
			t.Error(err)
		}
	})
	env.CopyTree(t, testenv.DataDir(), src, dst)
	if err := scanUntil("Messy Shows", have+1); err != nil {
		t.Fatal(err)
	}
	// one library at a time finds both in its own, and a limit caps the rows
	// and not the count
	whole := suite.Call(t, "audit_duplicates", map[string]any{"library": "Messy Shows"})
	var keys []string
	for _, g := range acc.Rows(t, whole["folder_groups"], "folder_groups") {
		keys = append(keys, acc.Str(g["key"]))
	}
	if acc.Num(t, whole["total_folder_groups"], "total_folder_groups") != 2 || !slices.ContainsFunc(keys, func(k string) bool { return strings.HasSuffix(k, "/star trek the next generation") }) {
		t.Errorf("Messy Shows with the second pair = %v", keys)
	}
	if capped := suite.Call(t, "audit_duplicates", map[string]any{"library": "Messy Shows", "limit": 1}); len(acc.Rows(t, capped["folder_groups"], "folder_groups")) != 1 || acc.Num(t, capped["total_folder_groups"], "total_folder_groups") != 2 || acc.Num(t, capped["total_findings"], "total_findings") != acc.Num(t, whole["total_findings"], "total_findings") {
		t.Errorf("limit 1 = %v, want one folder group listed and both counted", capped)
	}
}
