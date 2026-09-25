//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Break, audit, fix, audit clean: each test here puts a defect into the
// fixtures the way a library comes by one, sees the audit find it and
// audit_all count it, puts it right the way the finding says to, and sees
// both let go - then leaves the fixtures as it found them.

// A film's or a series' title and year against its path. A title edited
// away from its folder is a finding and edited back is not; a year a year
// out is a film released over New Year and two out is a wrong edition; and a
// collection folder's year above a film's own is not the film's.
func TestAuditFilePathTitlesAndYears(t *testing.T) {
	interstellar := findItem(t, "Messy Movies", "Movie", "Interstellar")
	titles := map[string]any{"library": "Messy Movies", "checks": "title"}
	before := auditRow(t, "Messy Movies", "audit_file_path")

	rename(t, interstellar, "Arrival")
	out := call(t, "audit_file_path", titles)
	found := rows(t, out["findings"], "findings")
	if len(found) != 1 || num(t, out["total_findings"], "total_findings") != 1 {
		t.Fatalf("with Interstellar held as Arrival = %v", found)
	}
	f := found[0]
	// the tools are given a TMDB token here, and TMDB says the folder names
	// the film the item is matched to: it is the name that is wrong
	if ps := strs(t, f["problems"], "problems"); len(ps) != 1 || ps[0] != `title: the path is named "Interstellar", the server holds "Arrival": the path names the item's own title, and the name is none TMDB gives it` ||
		str(f["id"]) != interstellar || str(f["title_in_file"]) != "Interstellar" || str(f["title_on_server"]) != "Arrival" || str(f["type"]) != "Movie" ||
		str(f["item_tmdb"]) != "157336" || !strings.Contains(str(f["diagnosis"]), "the path is right and the name is what is wrong") {
		t.Errorf("the title row = %v", f)
	}
	if score, _ := f["similarity"].(float64); score >= 0.5 {
		t.Errorf("similarity of Interstellar and Arrival = %v, want it low", score)
	}
	if n := auditRow(t, "Messy Movies", "audit_file_path"); n != before+1 {
		t.Errorf("audit_all's row = %d with the title wrong, want %d", n, before+1)
	}
	call(t, "item_edit", map[string]any{"ids": []any{interstellar}, "name": "Interstellar"})
	if n := num(t, call(t, "audit_file_path", titles)["total_findings"], "total_findings"); n != 0 {
		t.Errorf("with the title put back the title check finds %d", n)
	}
	if n := auditRow(t, "Messy Movies", "audit_file_path"); n != before {
		t.Errorf("audit_all's row = %d with the title put back, want %d", n, before)
	}

	// a year either side of the folder's is a release over New Year, and two
	// is not
	years := map[string]any{"library": "Messy Movies", "checks": "year"}
	t.Cleanup(func() { _, _ = invoke("item_edit", map[string]any{"ids": []any{interstellar}, "year": 2014}) })
	for year, flagged := range map[int]bool{2013: false, 2015: false, 2016: true, 2012: true} {
		call(t, "item_edit", map[string]any{"ids": []any{interstellar}, "year": year})
		got := findings(t, call(t, "audit_file_path", years))
		want := []string{"Dune"}
		if flagged {
			want = []string{"Dune", "Interstellar"}
		}
		if !slices.Equal(got, want) {
			t.Errorf("with Interstellar's year %d the year check finds %v, want %v", year, got, want)
		}
	}
	call(t, "item_edit", map[string]any{"ids": []any{interstellar}, "year": 2016})
	for _, f := range rows(t, call(t, "audit_file_path", years)["findings"], "findings") {
		if str(f["id"]) == interstellar && !slices.Equal(strs(t, f["problems"], "problems"), []string{"year: path says 2014, metadata says 2016"}) {
			t.Errorf("Interstellar's year row = %v", f)
		}
	}
	call(t, "item_edit", map[string]any{"ids": []any{interstellar}, "year": 2014})
	if got := findings(t, call(t, "audit_file_path", years)); !slices.Equal(got, []string{"Dune"}) {
		t.Errorf("with the year put back the year check finds %v", got)
	}

	// a series is read by its folder the same way
	andor := findItem(t, "Messy Shows", "Series", "Andor")
	series := map[string]any{"library": "Messy Shows", "types": "Series"}
	if n := num(t, call(t, "audit_file_path", series)["total_findings"], "total_findings"); n != 0 {
		t.Fatalf("the messy series' folders already disagree with them: %d", n)
	}
	t.Cleanup(func() { _, _ = invoke("item_edit", map[string]any{"ids": []any{andor}, "year": 2022}) })
	call(t, "item_edit", map[string]any{"ids": []any{andor}, "year": 2020})
	rename(t, andor, "Severance")
	out = call(t, "audit_file_path", series)
	if found := rows(t, out["findings"], "findings"); len(found) != 1 || str(found[0]["id"]) != andor ||
		!slices.Equal(strs(t, found[0]["problems"], "problems"), []string{
			`title: the path is named "Andor", the server holds "Severance", and the path's title is none of the titles it goes by: the wrong match, or another film`,
			"year: path says 2022, metadata says 2020",
		}) {
		t.Errorf("Andor held as Severance of 2020 = %v", found)
	}
	// one row failing two checks counts under both
	if byCheck := object(t, out["by_check"], "by_check"); num(t, byCheck["title"], "title") != 1 || num(t, byCheck["year"], "year") != 1 || num(t, out["total_findings"], "total_findings") != 1 {
		t.Errorf("by_check = %v of %v rows", byCheck, out["total_findings"])
	}
	call(t, "item_edit", map[string]any{"ids": []any{andor}, "name": "Andor", "year": 2022})
	if n := num(t, call(t, "audit_file_path", series)["total_findings"], "total_findings"); n != 0 {
		t.Errorf("with Andor put back its folder check finds %d", n)
	}

	// a film filed inside a collection's folder: the collection's year is
	// the first film's, and the film's own is the last in the path
	nfo := []byte("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<movie>\n  <title>Dune</title>\n  <year>2021</year>\n</movie>\n")
	stage(t, plus(1, 0, 0), map[string][]byte{
		"messy-movies/Dune Collection (1984)/Dune (2021)/Dune (2021).mp4": fixture(t, "messy-movies/Dune (2021)/Dune (2021).mp4"),
		"messy-movies/Dune Collection (1984)/Dune (2021)/movie.nfo":       nfo,
	}, "messy-movies/Dune Collection (1984)")
	out = call(t, "audit_file_path", map[string]any{"library": "Messy Movies"})
	for _, f := range rows(t, out["findings"], "findings") {
		if strings.Contains(str(f["path"]), "Dune Collection") {
			t.Errorf("the film in a collection's folder was reported: %v", f)
		}
	}
	if n := num(t, out["total_findings"], "total_findings"); n != 1 || num(t, out["items_scanned"], "items_scanned") != messyMovies()+1 {
		t.Errorf("with the collection staged = %d rows of %v, want the messy Dune's year alone of %d", n, out["items_scanned"], messyMovies()+1)
	}
}

// A file renamed to the number the server holds it at: Andor's S01E04 file
// holds episode 5, and named for 5 the episode row lets go.
func TestAuditFilePathFixedByARename(t *testing.T) {
	season := filepath.Join("messy-shows", "Andor (2022)", "Season 01")
	episodes := map[string]any{"library": "Messy Shows", "checks": "episode"}
	has := func(file string) bool {
		for _, f := range rows(t, call(t, "audit_file_path", episodes)["findings"], "findings") {
			if filepath.Base(str(f["path"])) == file {
				return true
			}
		}

		return false
	}
	if !has("Andor S01E04.mp4") {
		t.Fatal("Andor's S01E04 file is not reported as held at another number")
	}
	before := auditRow(t, "Messy Shows", "audit_file_path")

	moved := map[string][]byte{}
	for _, ext := range []string{".mp4", ".nfo"} {
		moved[ext] = fixture(t, filepath.Join(season, "Andor S01E04"+ext))
	}
	t.Cleanup(func() {
		for ext, raw := range moved {
			mediaWrite(t, filepath.Join(dataDir(), season, "Andor S01E04"+ext), raw)
			_ = os.Remove(filepath.Join(dataDir(), season, "Andor S01E05"+ext))
		}
		rescanUntil(t, "Andor's S01E04 file back", func() bool { return has("Andor S01E04.mp4") })
	})
	for ext, raw := range moved {
		mediaWrite(t, filepath.Join(dataDir(), season, "Andor S01E05"+ext), raw)
		if err := os.Remove(filepath.Join(dataDir(), season, "Andor S01E04"+ext)); err != nil {
			t.Fatal(err)
		}
	}
	rescanUntil(t, "the renamed file in place", func() bool { return !has("Andor S01E04.mp4") && !has("Andor S01E05.mp4") })
	if n := auditRow(t, "Messy Shows", "audit_file_path"); n != before-1 {
		t.Errorf("audit_all's row = %d with the file renamed, want %d", n, before-1)
	}
}

// With the TMDB token, a row whose file names an episode title says where
// TMDB puts that title. Two of the messy Severance's files are named here as
// a downloader names them: one for its own episode, which the server holds
// under another title, and one for an episode of another show.
func TestAuditFilePathAgainstTMDB(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	needsTMDBRecording(t, "GET api.themoviedb.org/3/tv/95396")
	season := filepath.Join("messy-shows", "Severance", "Season 01")
	titles := map[string]any{"library": "Messy Shows", "checks": "title"}
	byFile := func() map[string]map[string]any {
		got := map[string]map[string]any{}
		for _, f := range rows(t, call(t, "audit_file_path", titles)["findings"], "findings") {
			got[filepath.Base(str(f["path"]))] = f
		}

		return got
	}
	if got := byFile(); len(got) != 0 {
		t.Fatalf("the messy shows already have title rows: %v", got)
	}
	before := auditRow(t, "Messy Shows", "audit_file_path")
	unnamed := num(t, call(t, "audit_file_path", titles)["unnamed"], "unnamed")

	renames := map[string]string{
		"Severance S01E03": "Severance S01E03 - In Perpetuity",
		"Severance S01E02": "Severance S01E02 - Kassa",
	}
	held := map[string][]byte{}
	for from := range renames {
		for _, ext := range []string{".mp4", ".nfo"} {
			held[from+ext] = fixture(t, filepath.Join(season, from+ext))
		}
	}
	t.Cleanup(func() {
		for from, to := range renames {
			for _, ext := range []string{".mp4", ".nfo"} {
				mediaWrite(t, filepath.Join(dataDir(), season, from+ext), held[from+ext])
				_ = os.Remove(filepath.Join(dataDir(), season, to+ext))
			}
		}
		rescanUntil(t, "Severance's files named as they were", func() bool { return len(byFile()) == 0 })
	})
	for from, to := range renames {
		for _, ext := range []string{".mp4", ".nfo"} {
			mediaWrite(t, filepath.Join(dataDir(), season, to+ext), held[from+ext])
			if err := os.Remove(filepath.Join(dataDir(), season, from+ext)); err != nil {
				t.Fatal(err)
			}
		}
	}
	// the third episode's file is named for itself, so the server's title
	// has to be something else for there to be a row
	rescanUntil(t, "the renamed files", func() bool { _, ok := byFile()["Severance S01E02 - Kassa.mp4"]; return ok })
	sev := findItem(t, "Messy Shows", "Series", "Severance")
	rename(t, episodeID(t, sev, 1, 3), "Hello, Ms. Cobel")

	out := call(t, "audit_file_path", titles)
	got := byFile()
	if len(got) != 2 || num(t, out["total_findings"], "total_findings") != 2 {
		t.Fatalf("title rows = %v, want the two renamed files", got)
	}
	// named for its own episode: TMDB gives the file's title to the very
	// number the server holds, so the server's title is the odd one
	if f := got["Severance S01E03 - In Perpetuity.mp4"]; str(f["title_in_file"]) != "In Perpetuity" || str(f["title_on_server"]) != "Hello, Ms. Cobel" ||
		str(f["tmdb_episode"]) != "S01E03" || str(f["diagnosis"]) != "TMDB gives the file's title to this very episode: the server's title is a reworded one, not a different episode" {
		t.Errorf("the file named for its own episode = %v", f)
	}
	// named for another show's episode: TMDB has no episode of that title
	if f := got["Severance S01E02 - Kassa.mp4"]; str(f["title_in_file"]) != "Kassa" || str(f["title_on_server"]) != "Half Loop" ||
		str(f["tmdb_episode"]) != "" || str(f["diagnosis"]) != "no TMDB episode of this series has the file's title: the file is from another series, or named by hand" {
		t.Errorf("the file named for another show's episode = %v", f)
	}
	// the two files now claim a title, so two fewer claim none
	if n := num(t, out["unnamed"], "unnamed"); n != unnamed-2 {
		t.Errorf("unnamed = %d, want %d", n, unnamed-2)
	}
	if n := auditRow(t, "Messy Shows", "audit_file_path"); n != before+2 {
		t.Errorf("audit_all's row = %d, want %d", n, before+2)
	}
}

// A run of episodes the nfo claims and the file name does not: the nfo
// beside Andor S01E09 ends the run at 10. Jellyfin reads the run from the
// nfo, so it holds more than the file's name says; Emby takes the file's
// name, and the two agree.
func TestAuditFilePathRunInTheNfo(t *testing.T) {
	nfo := []byte("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<episodedetails>\n  <season>1</season>\n  <episode>9</episode>\n  <episodenumberend>10</episodenumberend>\n</episodedetails>\n")
	stage(t, plus(0, 0, 1), map[string][]byte{
		"messy-shows/Andor (2022)/Season 01/Andor S01E09.mp4": fixture(t, "messy-shows/Andor (2022)/Season 01/Andor S01E01.mp4"),
		"messy-shows/Andor (2022)/Season 01/Andor S01E09.nfo": nfo,
	})
	var row map[string]any
	for _, f := range rows(t, call(t, "audit_file_path", map[string]any{"library": "Messy Shows", "checks": "episode"})["findings"], "findings") {
		if filepath.Base(str(f["path"])) == "Andor S01E09.mp4" {
			row = f
		}
	}
	if !isJellyfin() {
		if row != nil {
			t.Errorf("Emby takes the run from the file name, so the two agree: %v", row)
		}

		return
	}
	if row == nil || !slices.Equal(strs(t, row["problems"], "problems"), []string{"episode: the server holds E09-E10, the file claims E09: the metadata says a run the file name does not"}) {
		t.Errorf("the run the nfo claims = %v", row)
	}
}

// Entries sharing an id are copies of one title only where the id means
// one title. A TMDB or TVDB number is a film's in one list and a series' in
// another; an episode's ids are shared loosely enough that the series and
// number go into the key; and a copy matched by one id joins a copy matched
// by another through a third that holds both.
func TestAuditDuplicatesJoinOnlyOneTitle(t *testing.T) {
	groups := func(args map[string]any) []string {
		var got []string
		all, _ := call(t, "audit_duplicates", args)["groups"].([]any)
		for _, g := range all {
			var members []string
			for _, it := range rows(t, g, "group") {
				members = append(members, str(it["type"])+" "+title(str(it["name"])))
			}
			got = append(got, strings.Join(sorted(members), " + "))
		}

		return sorted(got)
	}
	base := groups(nil)
	baseRow := auditRow(t, "", "audit_duplicates")
	interstellar := findItem(t, "Messy Movies", "Movie", "Interstellar")

	t.Run("a series' numbers on a film", func(t *testing.T) {
		// Breaking Bad's TMDB and TVDB numbers, which as a film's are some
		// other film's or none
		setIDs(t, interstellar, map[string]any{"Tmdb": "1396", "Tvdb": "81189", "Imdb": "tt0816692"})
		if got := groups(nil); !slices.Equal(got, base) {
			t.Errorf("with a series' numbers on a film = %v, want %v", got, base)
		}
	})

	t.Run("a chain of two ids", func(t *testing.T) {
		// Interstellar by The Thirteenth Floor's TMDB number, Andor by its
		// IMDb id, and the clean Thirteenth Floor holding both: one group of
		// three, though the two ends share nothing
		andor := findItem(t, "Messy Shows", "Series", "Andor")
		setIDs(t, interstellar, map[string]any{"Tmdb": "1090"})
		setIDs(t, andor, map[string]any{"Imdb": "tt0139809"})
		got := groups(nil)
		chain := "Movie Interstellar + Movie The Thirteenth Floor + Series Andor"
		if !slices.Contains(got, chain) || len(got) != len(base)+1 {
			t.Errorf("groups = %v, want %q beside %v", got, chain, base)
		}
		if n := auditRow(t, "", "audit_duplicates"); n != baseRow+1 {
			t.Errorf("audit_all's row = %d, want %d", n, baseRow+1)
		}
	})

	messy := findItem(t, "Messy Shows", "Series", "Severance")
	clean := findItem(t, "Shows", "Series", "Severance")
	episodes := map[string]any{"types": "Episode"}
	if got := groups(episodes); len(got) != 0 {
		t.Fatalf("episode groups before any are made = %v", got)
	}

	t.Run("one episode held twice", func(t *testing.T) {
		// the messy Severance's first episode given the clean one's ids: one
		// series name, one number, so one episode held twice
		ids, ok := fullItem(t, episodeID(t, clean, 1, 1))["ProviderIds"].(map[string]any)
		if !ok || len(ids) == 0 {
			t.Fatalf("the clean Severance S01E01 holds no ids: %v", ids)
		}
		setIDs(t, episodeID(t, messy, 1, 1), ids)
		if got := groups(episodes); !slices.Equal(got, []string{"Episode Good News About Hell + Episode Good News About Hell"}) {
			t.Errorf("episode groups = %v, want Severance S01E01 twice", got)
		}
		if n := auditRow(t, "", "audit_duplicates"); n != baseRow+1 {
			t.Errorf("audit_all's row = %d, want %d", n, baseRow+1)
		}
	})

	t.Run("one id on different episodes", func(t *testing.T) {
		// the series' IMDb id copied onto three episodes of two shows, the
		// way a scraper leaves it: different episodes, so no group
		for _, id := range []string{episodeID(t, messy, 1, 2), episodeID(t, messy, 1, 3), episodeID(t, findItem(t, "Messy Shows", "Series", "Andor"), 1, 1)} {
			setIDs(t, id, map[string]any{"Imdb": "tt11280740"})
		}
		if got := groups(episodes); len(got) != 0 {
			t.Errorf("episodes sharing one loose id = %v, want no group", got)
		}
		if got := groups(nil); !slices.Equal(got, base) {
			t.Errorf("every group = %v, want %v", got, base)
		}
	})
}

// One title twice in a season is a lead when the runtimes disagree: the
// messy Severance's third episode renamed for its second, 5 seconds against
// 1. Case does not make it another title, and a title in another season is
// not the same episode.
func TestAuditDuplicateEpisodesLeadAndCase(t *testing.T) {
	sev := findItem(t, "Messy Shows", "Series", "Severance")
	e03 := episodeID(t, sev, 1, 3)
	messy := map[string]any{"library": "Messy Shows"}
	if n := num(t, call(t, "audit_duplicate_episodes", messy)["total_findings"], "total_findings"); n != 0 {
		t.Fatalf("the messy shows already repeat a title: %d", n)
	}
	before := auditRow(t, "Messy Shows", "audit_duplicate_episodes")

	for _, name := range []string{"Half Loop", "HALF LOOP"} {
		rename(t, e03, name)
		out := call(t, "audit_duplicate_episodes", messy)
		groups := rows(t, out["groups"], "groups")
		if len(groups) != 1 || num(t, out["total_findings"], "total_findings") != 1 {
			t.Fatalf("with S01E03 named %q = %v", name, groups)
		}
		g := groups[0]
		eps := rows(t, g["episodes"], "episodes")
		if str(g["series"]) != "Severance" || num(t, g["season"], "season") != 1 || str(g["confidence"]) != "lead" || decimal(t, g["runtime_gap"], "runtime_gap") != 0.8 ||
			len(eps) != 2 || num(t, eps[0]["episode"], "episode") != 2 || num(t, eps[0]["runtime_s"], "runtime_s") != 1 || num(t, eps[1]["episode"], "episode") != 3 || num(t, eps[1]["runtime_s"], "runtime_s") != 5 {
			t.Errorf("with S01E03 named %q the group = %v", name, g)
		}
		if n := auditRow(t, "Messy Shows", "audit_duplicate_episodes"); n != before+1 {
			t.Errorf("audit_all's row = %d, want %d", n, before+1)
		}
	}

	// Deep Space Nine's third season opener given its first season's title:
	// another season, another episode
	ds9 := findItem(t, "Messy Shows", "Series", "Star Trek: Deep Space Nine")
	rename(t, episodeID(t, ds9, 3, 1), "Emissary")
	if n := num(t, call(t, "audit_duplicate_episodes", messy)["total_findings"], "total_findings"); n != 1 {
		t.Errorf("a title repeated across seasons was grouped: %d groups", n)
	}

	call(t, "item_edit", map[string]any{"ids": []any{e03}, "name": "In Perpetuity"})
	if n := num(t, call(t, "audit_duplicate_episodes", messy)["total_findings"], "total_findings"); n != 0 {
		t.Errorf("with the title put back %d groups remain", n)
	}
	if n := auditRow(t, "Messy Shows", "audit_duplicate_episodes"); n != before {
		t.Errorf("audit_all's row = %d with the title put back, want %d", n, before)
	}
}

// A show's folder renamed by an accent or by punctuation leaves a second
// series behind, the way the Knight pair's space and case do.
func TestAuditDuplicateSeriesSpellings(t *testing.T) {
	before := num(t, call(t, "audit_duplicate_series", map[string]any{"library": "Messy Shows"})["total_findings"], "total_findings")
	stage(t, plus(0, 2, 2), map[string][]byte{
		"messy-shows/Andór (2022)/Season 01/Andor S01E01.mp4":                                            fixture(t, "messy-shows/Andor (2022)/Season 01/Andor S01E01.mp4"),
		"messy-shows/Star Trek - The Next Generation/Season 01/Star Trek The Next Generation S01E02.mp4": fixture(t, "messy-shows/Star Trek The Next Generation/Season 01/Star Trek The Next Generation S01E01.mp4"),
	}, "messy-shows/Andór (2022)", "messy-shows/Star Trek - The Next Generation")

	out := call(t, "audit_duplicate_series", map[string]any{"library": "Messy Shows"})
	pairs := map[string][]string{}
	for _, g := range rows(t, out["groups"], "groups") {
		var folders []string
		for _, s := range rows(t, g["series"], "series") {
			folders = append(folders, str(s["folder"]))
		}
		pairs[str(g["key"])] = sorted(folders)
	}
	for _, want := range [][]string{
		{"Andor (2022)", "Andór (2022)"},
		{"Star Trek - The Next Generation", "Star Trek The Next Generation"},
	} {
		found := false
		for _, pair := range pairs {
			found = found || slices.Equal(pair, want)
		}
		if !found {
			t.Errorf("no group of %v: %v", want, pairs)
		}
	}
	if n := num(t, out["total_findings"], "total_findings"); n != before+2 {
		t.Errorf("groups = %d, want the %d there were and the two staged", n, before)
	}
}

// Runtimes against their season: specials have no season to be held to,
// a file holding two episodes is held to twice the median, and the worst
// comes first under a limit - after the one broken duration the fixtures
// carry, Deep Space Nine's twelve-hour claim, which ranks above any
// percentage. Putting a whole copy over the one cut short clears it.
func TestAuditRuntimeStaged(t *testing.T) {
	src := "messy-shows/hack Liminality (2002)/Season 01/"
	long, short := fixture(t, src+"hack Liminality S01E01.mp4"), fixture(t, src+"hack Liminality S01E03.mp4")
	g := "messy-shows/hack Liminality (2002)/"
	before := auditRow(t, "Messy Shows", "audit_runtime")
	stage(t, plus(0, 0, 7), map[string][]byte{
		// three specials, the last cut to a second: nothing to hold them to
		g + "Season 00/hack Liminality S00E01.mp4": long,
		g + "Season 00/hack Liminality S00E02.mp4": long,
		g + "Season 00/hack Liminality S00E03.mp4": short,
		// two episodes in one file of 200 seconds, where two run six minutes
		g + "Season 01/hack Liminality S01E04E05.mp4": fixture(t, "anime-src/special.mp4"),
		// a second season shaped like the first
		g + "Season 02/hack Liminality S02E01.mp4": long,
		g + "Season 02/hack Liminality S02E02.mp4": long,
		g + "Season 02/hack Liminality S02E03.mp4": short,
	}, g+"Season 00", g+"Season 02")

	byFile := func(out map[string]any) []string {
		var got []string
		for _, f := range rows(t, out["findings"], "findings") {
			got = append(got, filepath.Base(str(f["path"]))+": "+str(f["detail"]))
		}

		return got
	}
	out := call(t, "audit_runtime", map[string]any{"library": "Messy Shows"})
	want := []string{
		"Star Trek Deep Space Nine S03E01.mkv: 720 min: not a runtime, the file's duration metadata is broken",
		"hack Liminality S01E03.mp4: 0 min, season median 3 min (100% off)",
		"hack Liminality S02E03.mp4: 0 min, season median 3 min (100% off)",
		"hack Liminality S01E04E05.mp4: 3 min for 2 episodes, season median 3 min each, 6 expected (50% off)",
	}
	if got := byFile(out); !slices.Equal(got, want) || num(t, out["total_findings"], "total_findings") != 4 {
		t.Errorf("runtimes off = %v, want %v", got, want)
	}
	if n := auditRow(t, "Messy Shows", "audit_runtime"); n != before+2 {
		t.Errorf("audit_all's row = %d, want %d", n, before+2)
	}
	// the worst first, and a limit keeps it
	capped := call(t, "audit_runtime", map[string]any{"library": "Messy Shows", "limit": 1})
	if got := byFile(capped); !slices.Equal(got, want[:1]) || num(t, capped["total_findings"], "total_findings") != 4 {
		t.Errorf("limit 1 = %v of %v, want %v", got, capped["total_findings"], want[:1])
	}
	// a tolerance past the run's 50% forgives it, and nothing short of 100
	// forgives the files a second long; no tolerance forgives a duration
	// that is no runtime at all
	if got := byFile(call(t, "audit_runtime", map[string]any{"library": "Messy Shows", "tolerance_percent": 60})); !slices.Equal(got, want[:3]) {
		t.Errorf("tolerance 60 = %v, want %v", got, want[:3])
	}

	// the whole episode written over the one cut short
	mediaWrite(t, filepath.Join(dataDir(), g+"Season 02/hack Liminality S02E03.mp4"), long)
	rescanUntil(t, "the whole copy read", func() bool {
		return len(byFile(call(t, "audit_runtime", map[string]any{"library": "Messy Shows"}))) == 3
	})
	if got := byFile(call(t, "audit_runtime", map[string]any{"library": "Messy Shows"})); !slices.Equal(got, []string{want[0], want[1], want[3]}) {
		t.Errorf("after the fix = %v", got)
	}
	if n := auditRow(t, "Messy Shows", "audit_runtime"); n != before+1 {
		t.Errorf("audit_all's row = %d after the fix, want %d", n, before+1)
	}

	// a file written over after the server first saw it is one the quality
	// audit cannot vouch for: Emby says when a file was last written, so it
	// lists both written here, and a limit caps that list; Jellyfin does not
	// say, and the answer says so instead
	mediaWrite(t, filepath.Join(dataDir(), g+"Season 02/hack Liminality S02E01.mp4"), long)
	quality := map[string]any{"library": "Messy Shows", "limit": 1}
	if isJellyfin() {
		out := call(t, "audit_quality", quality)
		if num(t, out["total_replaced"], "total_replaced") != 0 || !strings.Contains(str(out["note"]), "Jellyfin does not say when a file was last written") {
			t.Errorf("Jellyfin's replaced files = %v, note %q", out["replaced"], out["note"])
		}

		return
	}
	rescanUntil(t, "both files written over", func() bool {
		return num(t, call(t, "audit_quality", quality)["total_replaced"], "total_replaced") == 2
	})
	out = call(t, "audit_quality", quality)
	replaced := rows(t, out["replaced"], "replaced")
	if len(replaced) != 1 || !strings.HasSuffix(str(replaced[0]["path"]), "Season 02/hack Liminality S02E01.mp4") || !strings.Contains(str(replaced[0]["detail"]), "after the server first saw it") || out["note"] != nil {
		t.Errorf("limit 1 replaced = %v, want the first of the two by path", replaced)
	}
}

// Two files of one episode are one episode in two versions, where a server
// shows them that way: both do, for files named alike but for a suffix.
// Emby stores each file as an item and merges them in what it shows people;
// Jellyfin stores its merge.
func TestAuditMultipleVersionsOfAnEpisode(t *testing.T) {
	g := "messy-shows/hack Liminality (2002)/Season 02/"
	long := fixture(t, "messy-shows/hack Liminality (2002)/Season 01/hack Liminality S01E01.mp4")
	stored := 2
	if versionsMerged() {
		stored = 1
	}
	stage(t, plus(0, 0, stored), map[string][]byte{
		g + "hack Liminality S02E01.mp4":       long,
		g + "hack Liminality S02E01 - 90p.mp4": long,
	}, "messy-shows/hack Liminality (2002)/Season 02")

	// beside the one Emby already shows in two versions: The Wire's second
	// episode, a copy in each of the show's folders
	twice := 0
	if !isJellyfin() {
		twice = 1
	}
	out := call(t, "audit_multiple_versions", map[string]any{"library": "Messy Shows"})
	var found []map[string]any
	for _, f := range rows(t, out["findings"], "findings") {
		if strings.Contains(str(f["path"]), "/hack Liminality (2002)/") {
			found = append(found, f)
		}
	}
	if len(found) != 1 || num(t, out["total_findings"], "total_findings") != twice+1 {
		t.Fatalf("episodes in versions = %v of %v, want the staged one", found, out["total_findings"])
	}
	if d := str(found[0]["detail"]); !strings.HasPrefix(d, "2 versions: ") || !strings.Contains(d, "hack Liminality S02E01 - 90p.mp4") || !strings.Contains(d, "hack Liminality S02E01.mp4") {
		t.Errorf("detail = %q, want the two files", d)
	}
	// films alone find none of it
	if n := num(t, call(t, "audit_multiple_versions", map[string]any{"library": "Messy Shows", "types": "Movie"})["items_scanned"], "items_scanned"); n != 0 {
		t.Errorf("types Movie swept %d items of a show library", n)
	}
	if n := auditRow(t, "Messy Shows", "audit_multiple_versions"); n != twice+1 {
		t.Errorf("audit_all's row = %d, want %d", n, twice+1)
	}
}

// Things shaped like a disc that are not a film's disc: a camcorder's card,
// which keeps its clips in a BDMV folder of its own under PRIVATE/AVCHD, and
// a camcorder clip copied out loose, numbered like a stream but .mts. Both
// servers hold each as an item, and neither is a disc to remux. The card's
// item is never probed, so it joins the kept Blu-ray among the files the
// quality audit cannot judge, and a limit caps that list too.
func TestAuditDiscFoldersLeaveHomeVideoAlone(t *testing.T) {
	stream := fixture(t, "disc-src/00000.m2ts")
	before := call(t, "audit_disc_folders", nil)
	stage(t, plus(2, 0, 0), map[string][]byte{
		"messy-movies/Disc Rip (1999)/PRIVATE/AVCHD/BDMV/STREAM/00000.MTS": stream,
		"messy-movies/Home Video (2010)/00000.mts":                         stream,
	}, "messy-movies/Disc Rip (1999)", "messy-movies/Home Video (2010)")

	out := call(t, "audit_disc_folders", nil)
	folders := rows(t, out["folders"], "folders")
	if len(folders) != 1 || str(folders[0]["folder"]) != "/media/messy-movies/"+messyLooseDVD || num(t, out["total_findings"], "total_findings") != 1 {
		t.Errorf("disc folders with home video staged = %v, want the loose DVD alone", folders)
	}
	if num(t, out["items_scanned"], "items_scanned") != num(t, before["items_scanned"], "items_scanned")+2 {
		t.Errorf("items_scanned = %v, want the two staged items swept too (%v before)", out["items_scanned"], before["items_scanned"])
	}
	if n := auditRow(t, "Messy Movies", "audit_disc_folders"); n != 1 {
		t.Errorf("audit_all's row = %d, want the loose DVD alone", n)
	}

	// beside the discs kept whole the server never probed: the Blu-ray, and
	// on Emby the DVD
	want := []string{"/media/messy-movies/" + messyKeptBluRay, "/media/messy-movies/Disc Rip (1999)/PRIVATE/AVCHD"}
	if !isJellyfin() {
		want = append(want, "/media/messy-movies/"+messyKeptDVD)
	}
	quality := call(t, "audit_quality", map[string]any{"library": "Messy Movies", "limit": 1})
	unprobed := rows(t, quality["unprobed"], "unprobed")
	if len(unprobed) != 1 || num(t, quality["total_unprobed"], "total_unprobed") != len(want) {
		t.Errorf("limit 1 unprobed = %v of %v, want one of the %d", unprobed, quality["total_unprobed"], len(want))
	}
	all := call(t, "audit_quality", map[string]any{"library": "Messy Movies"})
	var paths []string
	for _, u := range rows(t, all["unprobed"], "unprobed") {
		paths = append(paths, str(u["path"]))
	}
	// by path, so a limit keeps the same one each call
	if !slices.Equal(paths, want) {
		t.Errorf("unprobed = %v, want %v", paths, want)
	}
}

// Subtitles read off files beside a film: one with no language in its name
// names none, so a film with it cannot be said to lack English; one named
// .eng is English, and the film it sits beside can be watched in English.
func TestAuditLanguageStaged(t *testing.T) {
	folder := filepath.Join(dataDir(), "messy-movies", messyDespecialized)
	restoration := findItem(t, "Messy Movies", "Movie", despecialized)
	subtitles := func() int {
		return len(strs(t, call(t, "item_get", map[string]any{"id": restoration})["subtitles"], "subtitles"))
	}
	line := []byte("1\n00:00:00,000 --> 00:00:00,900\nA line.\n")
	written := []string{}
	t.Cleanup(func() {
		for _, f := range written {
			_ = os.Remove(f)
		}
		rescanUntil(t, "the Despecialized Edition without subtitles", func() bool {
			out, err := invoke("item_get", map[string]any{"id": restoration})
			subs, _ := out["subtitles"].([]any)
			return err == nil && len(subs) == 0
		})
	})
	add := func(name string, want int) {
		t.Helper()
		path := filepath.Join(folder, name)
		written = append(written, path)
		mediaWrite(t, path, line)
		rescanUntil(t, name+" read", func() bool {
			out, err := invoke("item_get", map[string]any{"id": restoration})
			subs, _ := out["subtitles"].([]any)
			return err == nil && len(subs) == want
		})
	}
	unwatchable := map[string]any{"language": "eng", "find": "unwatchable", "library": "Messy Movies"}
	start := call(t, "audit_language", unwatchable)
	if got := names(t, start["findings"], "findings"); !slices.Equal(got, []string{despecialized}) {
		t.Fatalf("unwatchable in English = %v, want the Despecialized Edition, its one track German", got)
	}

	add(messyDespecialized+".srt", 1)
	out := call(t, "audit_language", unwatchable)
	if n := num(t, out["total_findings"], "total_findings"); n != 0 || num(t, out["untagged"], "untagged") != num(t, start["untagged"], "untagged")+1 {
		t.Errorf("with an untagged subtitle = %v, want the Despecialized Edition counted untagged rather than reported", out)
	}
	if got := names(t, call(t, "audit_language", map[string]any{"language": "eng", "find": "subtitles"})["findings"], "findings"); !slices.Equal(got, []string{"The Thirteenth Floor"}) {
		t.Errorf("English subtitles with an untagged one staged = %v", got)
	}

	add(messyDespecialized+".eng.srt", 2)
	english := map[string]any{"language": "eng", "find": "subtitles"}
	out = call(t, "audit_language", english)
	if got := names(t, out["findings"], "findings"); !slices.Equal(got, []string{despecialized, "The Thirteenth Floor"}) || num(t, out["total_findings"], "total_findings") != 2 {
		t.Errorf("English subtitles = %v", got)
	}
	for _, f := range rows(t, out["findings"], "findings") {
		if title(str(f["name"])) == despecialized && str(f["detail"]) != "audio: deu; subtitles: eng, untagged" {
			t.Errorf("detail = %q", f["detail"])
		}
	}
	// a limit caps the rows, name order keeps the first, and the count is
	// the whole
	capped := call(t, "audit_language", map[string]any{"language": "en", "find": "subtitles", "limit": 1})
	if got := names(t, capped["findings"], "findings"); !slices.Equal(got, []string{despecialized}) || num(t, capped["total_findings"], "total_findings") != 2 {
		t.Errorf("limit 1 = %v of %v", got, capped["total_findings"])
	}
	if n := num(t, call(t, "audit_language", unwatchable)["total_findings"], "total_findings"); n != 0 {
		t.Errorf("with English subtitles the Despecialized Edition is still unwatchable in English: %d", n)
	}
	if subtitles() != 2 {
		t.Errorf("item_get's subtitles = %d, want both files", subtitles())
	}
}

// A film holding links to its pages and no id is still matched nowhere, and
// the finding says why it may look matched; identified, it lets go.
func TestAuditMissingProviderLinksOnly(t *testing.T) {
	mononoke := findItem(t, "Messy Movies", "Movie", "Princess Mononoke")
	messy := map[string]any{"library": "Messy Movies"}
	before := auditRow(t, "Messy Movies", "audit_missing_metadata_provider")
	setIDs(t, mononoke, map[string]any{"Official Website": "https://movies.disney.com/princess-mononoke", "Wikipedia": "Princess_Mononoke"})
	t.Cleanup(func() { updateItem(t, mononoke, map[string]any{"Overview": ""}) })

	detail := ""
	for _, f := range rows(t, call(t, "audit_missing_metadata_provider", messy)["findings"], "findings") {
		if str(f["id"]) == mononoke {
			detail = str(f["detail"])
		}
	}
	if detail != "no provider id, only links to its pages" {
		t.Errorf("a film with links alone = %q", detail)
	}
	if n := auditRow(t, "Messy Movies", "audit_missing_metadata_provider"); n != before {
		t.Errorf("audit_all's row = %d, want %d: links are not a match", n, before)
	}

	// by either id: without the item Jellyfin's search is answered by OMDb,
	// whose candidates carry the imdb id alone
	cands := rows(t, call(t, "item_identify", map[string]any{"id": mononoke, "kind": "movie"})["candidates"], "candidates")
	idx := slices.IndexFunc(cands, func(c map[string]any) bool {
		ids, _ := c["metadata_provider_ids"].(map[string]any)
		return str(ids["tmdb"]) == "128" || str(ids["imdb"]) == "tt0119698"
	})
	if idx < 0 {
		t.Fatalf("no Princess Mononoke among the candidates: %v", cands)
	}
	call(t, "item_identify_apply", map[string]any{"id": mononoke, "kind": "movie", "candidate": idx})
	if !eventually(func() bool {
		return !slices.Contains(findings(t, call(t, "audit_missing_metadata_provider", messy)), "Princess Mononoke")
	}) {
		t.Errorf("identified, Princess Mononoke is still matched nowhere")
	}
	if n := auditRow(t, "Messy Movies", "audit_missing_metadata_provider"); n != before-1 {
		t.Errorf("audit_all's row = %d identified, want %d", n, before-1)
	}
}

// The gaps between the files, and what TMDB's run adds to them: specials
// are no season to have gaps in, a series is found at TMDB by its TVDB or
// IMDb id when it holds no TMDB one, and a TMDB id TMDB has nothing for
// leaves the run unknown rather than complete.
func TestAuditMissingEpisodesStaged(t *testing.T) {
	sev := "messy-shows/Severance/Season 00/"
	one := fixture(t, "messy-shows/Severance/Season 01/Severance S01E01.mp4")
	stage(t, plus(0, 0, 2), map[string][]byte{
		sev + "Severance S00E01.mp4": one,
		sev + "Severance S00E04.mp4": one,
	}, "messy-shows/Severance/Season 00")

	out := call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows"})
	if got := findings(t, out); !slices.Equal(got, []string{"Andor", "Star Trek The Next Generation", "Star Trek: Deep Space Nine"}) {
		t.Errorf("with specials 1 and 4 staged = %v, want no gap among them", got)
	}
	if n := num(t, out["items_scanned"], "items_scanned"); n != messyEpisodes()+2 {
		t.Errorf("scanned %d, want the specials counted too (%d)", n, messyEpisodes()+2)
	}

	needsTMDBRecording(t, "GET api.themoviedb.org/3/tv/95396")
	// what TMDB lists past Severance's three files, sixteen aired episodes
	// of its first two seasons, the first twelve by name; its empty third
	// season adds nothing, and the specials it skips are no part of a run
	run := "listed by TMDB without a file: S01E04, S01E05, S01E06, S01E07, S01E08, S01E09, S02E01, S02E02, S02E03, S02E04, S02E05, S02E06 and 4 more"
	severance := func() (string, string) {
		out := call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows", "provider": true})
		detail := ""
		for _, f := range rows(t, out["findings"], "findings") {
			if str(f["name"]) == "Severance" {
				detail = str(f["detail"])
			}
		}
		reason := ""
		for _, u := range rows(t, out["unknown"], "unknown") {
			if str(u["name"]) == "Andor" {
				reason = str(u["reason"])
			}
		}

		return detail, reason
	}
	if d, _ := severance(); d != run {
		t.Errorf("Severance by its TMDB id = %q, want %q", d, run)
	}
	series := findItem(t, "Messy Shows", "Series", "Severance")
	for _, by := range []struct{ provider, id string }{{"Tvdb", "371980"}, {"Imdb", "tt11280740"}} {
		t.Run("by "+by.provider, func(t *testing.T) {
			needsTMDBRecording(t, "GET api.themoviedb.org/3/find/"+by.id+"?external_source="+strings.ToLower(by.provider)+"_id")
			setIDs(t, series, map[string]any{by.provider: by.id})
			if d, _ := severance(); d != run {
				t.Errorf("Severance by its %s id alone = %q, want %q", by.provider, d, run)
			}
		})
	}

	t.Run("a TMDB id with nothing behind it", func(t *testing.T) {
		needsTMDBRecording(t, "GET api.themoviedb.org/3/tv/99999999")
		setIDs(t, findItem(t, "Messy Shows", "Series", "Andor"), map[string]any{"Tmdb": "99999999"})
		if _, reason := severance(); reason != "TMDB lists no episodes for series 99999999, and the server keeps no record of the run." {
			t.Errorf("Andor with a TMDB id TMDB has nothing for = %q", reason)
		}
	})
}
