//go:build integration

package acceptance

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The duplicate audits claim only what they can show: a pair of episodes is
// near certain on what their files say, entries whose AniDB ids differ are
// never copies of one show, a studio is never merged with another company
// through a name both begin with, and a disc's pieces matched to one film by
// different ids are one film.

// Episodes staged in .hack//Liminality, each under the title of one it holds
// already, judged by what the files say against the rest of the season. Its
// first two episodes run three minutes each, and its third a second.
//   - E04, the 200-second special, titled as E02: a tenth longer, and
//     nothing in the files ties them - a lead
//   - E05, E01's own file, titled as E01: the same runtime, but E02 runs it
//     too, so that says nothing; the same size to the byte, which no other
//     episode is - near certain
//   - E06, the special again, titled as E03: two hundred times the length -
//     far apart, a copy cut short or two episodes, said rather than dropped
//   - S02E01 to S02E03, the film Limitless at its real length three times
//     under one title, beside S02E04 and S02E05, the special and E01's file
//     under two others: the same runtime, which nothing under another title
//     runs, in a season whose other lengths differ - near certain, each copy
//     tied to the others rather than their sharing it counted against them
func TestAuditDuplicateEpisodesByTheFiles(t *testing.T) {
	show := "messy-shows/hack Liminality (2002)/"
	special := fixture(t, "anime-src/special.mp4")                     // 200 seconds
	first := fixture(t, show+"Season 01/hack Liminality S01E01.mp4")   // 180 seconds
	film := fixture(t, "movies/Limitless (2011)/Limitless (2011).mp4") // 106 minutes
	stage(t, plus(0, 0, 8), map[string][]byte{
		show + "Season 01/hack Liminality S01E04.mp4": special,
		show + "Season 01/hack Liminality S01E04.nfo": episodeNfo("In the Case of Yuki Aihara", 1, 4),
		show + "Season 01/hack Liminality S01E05.mp4": first,
		show + "Season 01/hack Liminality S01E05.nfo": episodeNfo("In the Case of Mai Minase", 1, 5),
		show + "Season 01/hack Liminality S01E06.mp4": special,
		show + "Season 01/hack Liminality S01E06.nfo": episodeNfo("In the Case of Kyoko Tohno", 1, 6),
		show + "Season 02/hack Liminality S02E01.mp4": film,
		show + "Season 02/hack Liminality S02E01.nfo": episodeNfo("In the Case of Mai Minase", 2, 1),
		show + "Season 02/hack Liminality S02E02.mp4": film,
		show + "Season 02/hack Liminality S02E02.nfo": episodeNfo("In the Case of Mai Minase", 2, 2),
		show + "Season 02/hack Liminality S02E03.mp4": film,
		show + "Season 02/hack Liminality S02E03.nfo": episodeNfo("In the Case of Mai Minase", 2, 3),
		show + "Season 02/hack Liminality S02E04.mp4": special,
		show + "Season 02/hack Liminality S02E04.nfo": episodeNfo("In the Case of Yuki Aihara", 2, 4),
		show + "Season 02/hack Liminality S02E05.mp4": first,
		show + "Season 02/hack Liminality S02E05.nfo": episodeNfo("In the Case of Kyoko Tohno", 2, 5),
	}, show+"Season 02")
	// the scan lists a file before it reads it: the claim is about what the
	// files say, so every staged episode's size and streams are waited for
	readOffTheirFiles(t, ".hack//Liminality", [][2]int{{1, 4}, {1, 5}, {1, 6}, {2, 1}, {2, 2}, {2, 3}, {2, 4}, {2, 5}})

	type group struct {
		season            int
		title, confidence string
	}
	groups := map[group]map[string]any{}
	for _, g := range rows(t, call(t, "audit_duplicate_episodes", map[string]any{"library": "Messy Shows"})["groups"], "groups") {
		if str(g["series"]) == ".hack//Liminality" {
			groups[group{num(t, g["season"], "season"), str(g["title"]), str(g["confidence"])}] = g
		}
	}
	episodes := func(g map[string]any) []int {
		var out []int
		for _, e := range rows(t, g["episodes"], "episodes") {
			out = append(out, num(t, e["episode"], "episode"))
		}
		return out
	}
	evidence := func(g map[string]any) []string {
		if g["evidence"] == nil {
			return nil
		}
		return strs(t, g["evidence"], "evidence")
	}
	for key, want := range map[group]struct {
		episodes []int
		evidence []string
	}{
		{1, "In the Case of Yuki Aihara", "lead"}:        {[]int{2, 4}, nil},
		{1, "In the Case of Mai Minase", "near_certain"}: {[]int{1, 5}, []string{"E01 and E05: the same size to the byte"}},
		{1, "In the Case of Kyoko Tohno", "far_apart"}:   {[]int{3, 6}, nil},
		{2, "In the Case of Mai Minase", "near_certain"}: {[]int{1, 2, 3}, []string{"E01 and E02: the same runtime to the second", "E01 and E03: the same runtime to the second", "E02 and E03: the same runtime to the second"}},
	} {
		g, ok := groups[key]
		if !ok || !slices.Equal(episodes(g), want.episodes) || !slices.Equal(evidence(g), want.evidence) {
			t.Errorf("%+v = %v, want episodes %v with evidence %v; the groups: %v", key, g, want.episodes, want.evidence, groups)
		}
	}
	if len(groups) != 4 {
		t.Errorf(".hack//Liminality's groups = %v, want the four above", groups)
	}
	if lead := groups[group{1, "In the Case of Yuki Aihara", "lead"}]; lead != nil && decimal(t, lead["runtime_gap"], "runtime_gap") != 0.1 {
		t.Errorf("the lead's runtime_gap = %v, want 0.1", lead["runtime_gap"])
	}
}

// readOffTheirFiles waits until the server has read each of a series'
// episodes off its file - its size and its streams - which a scan does after
// it lists the file, so an audit of what the files say reads them all.
func readOffTheirFiles(t *testing.T, series string, episodes [][2]int) {
	t.Helper()

	id := findItem(t, "Messy Shows", "Series", series)
	var ids []string
	for _, e := range episodes {
		ids = append(ids, episodeID(t, id, e[0], e[1]))
	}
	read := func(item map[string]any) bool {
		sources, _ := item["MediaSources"].([]any)
		if len(sources) == 0 {
			return false
		}
		source, _ := sources[0].(map[string]any)
		size, _ := source["Size"].(float64)
		streams, _ := source["MediaStreams"].([]any)

		return size > 0 && len(streams) > 0
	}
	if !eventuallyWithin(scanPatience, func() bool {
		return !slices.ContainsFunc(ids, func(id string) bool { return !read(fullItem(t, id)) })
	}) {
		t.Fatalf("%s's staged episodes %v were never read off their files", series, episodes)
	}
}

// Dragon Ball Z held in two folders, and The History of Trunks - one of its
// specials, held as a series of its own - carrying the show's TMDB id beside
// an AniDB id of its own. The three share an id and are one group, and each
// says so: the two folders of the show are AniDB 1530, the special 1474, and
// only entries of one AniDB id can be copies. Grouped unmarked, the special
// read as a copy of the show, which invites deleting it.
func TestAuditDuplicatesSaysWhichAniDBEntriesDiffer(t *testing.T) {
	special := fixture(t, "anime-src/special.mp4")
	dbz := showNfo("Dragon Ball Z", map[string]string{"tvdb": "81472", "tmdb": "12971", "anidb": "1530"})
	folders := []string{"messy-shows/Dragon Ball Z (1989)", "messy-shows/Dragon Ball Z", "messy-shows/Dragon Ball Z The History of Trunks (1993)"}
	stage(t, plus(0, 3, 3), map[string][]byte{
		folders[0] + "/tvshow.nfo":                                               dbz,
		folders[0] + "/Season 01/Dragon Ball Z S01E01.mp4":                       special,
		folders[1] + "/tvshow.nfo":                                               dbz,
		folders[1] + "/Season 01/Dragon Ball Z S01E02.mp4":                       special,
		folders[2] + "/tvshow.nfo":                                               showNfo("Dragon Ball Z: The History of Trunks", map[string]string{"tmdb": "12971", "anidb": "1474"}),
		folders[2] + "/Season 01/Dragon Ball Z The History of Trunks S01E01.mp4": special,
	}, folders...)

	warnings := map[string]string{}
	groups := 0
	all, _ := call(t, "audit_duplicates", map[string]any{"library": "Messy Shows", "types": "Series"})["groups"].([]any)
	for _, g := range all {
		members := rows(t, g, "group")
		if !slices.ContainsFunc(members, func(it map[string]any) bool { return strings.Contains(str(it["path"]), "Dragon Ball Z") }) {
			continue
		}
		groups++
		for _, it := range members {
			warnings[strings.TrimPrefix(str(it["path"]), "/media/messy-shows/")] = str(it["warning"])
		}
	}
	if groups != 1 || len(warnings) != 3 {
		t.Fatalf("Dragon Ball Z's groups = %d holding %v, want one of the three", groups, warnings)
	}
	for folder, want := range map[string]string{
		"Dragon Ball Z (1989)":                       "AniDB ids differ: this entry is AniDB 1530, and the group holds AniDB 1474 too",
		"Dragon Ball Z":                              "AniDB ids differ: this entry is AniDB 1530, and the group holds AniDB 1474 too",
		"Dragon Ball Z The History of Trunks (1993)": "AniDB ids differ: this entry is AniDB 1474, and the group holds AniDB 1530 too",
	} {
		if !strings.HasPrefix(warnings[folder], want) {
			t.Errorf("%s warns %q, want %q", folder, warnings[folder], want)
		}
	}
}

// Three studios whose names begin alike - one company's short name, its film
// studio and its television studio. The short name is paired with each, as
// something to check, and the film and television studios are never one
// group to merge.
func TestAuditSpellingKeepsCompaniesApart(t *testing.T) {
	studios := map[string]string{"Memento": "Paramount", "Interstellar": "Paramount Pictures", "Arrival": "Paramount Television"}
	for film, studio := range studios {
		id := findItem(t, "Messy Movies", "Movie", film)
		putBack(t, "item_edit", map[string]any{"ids": []any{id}, "remove_studios": []any{studio}})
		call(t, "item_edit", map[string]any{"ids": []any{id}, "add_studios": []any{studio}})
	}

	var pairs [][]string
	for _, g := range rows(t, call(t, "audit_spelling", map[string]any{"library": "Messy Movies", "field": "studios"})["groups"], "groups") {
		var names []string
		for _, s := range rows(t, g["spellings"], "spellings") {
			names = append(names, str(s["value"]))
		}
		slices.Sort(names)
		if str(g["kind"]) != "contains" || !strings.Contains(str(g["note"]), "may name their parent company") {
			t.Errorf("group %v, want a pair to check with a note", g)
		}
		pairs = append(pairs, names)
	}
	slices.SortFunc(pairs, func(a, b []string) int { return strings.Compare(strings.Join(a, "|"), strings.Join(b, "|")) })
	if want := [][]string{{"Paramount", "Paramount Pictures"}, {"Paramount", "Paramount Television"}}; !slices.EqualFunc(pairs, want, slices.Equal) {
		t.Errorf("studio groups = %v, want %v and never the film and television studios together", pairs, want)
	}
}

// A disc's pieces matched to one film by different ids are one film: the
// feature by its TMDB and IMDb ids and a copy of it by the TMDB id alone
// were counted as two titles beside the piece matched to another film, and
// the note said at least two of three were the wrong film.
func TestAuditDiscFoldersCountsOneFilmOnce(t *testing.T) {
	const name = "Pi (1998)"
	dir := filepath.Join(dataDir(), "messy-movies", name)
	streams := map[string][]byte{
		"00000.m2ts": fixtureVideo(t, "disc-src", "00000.m2ts"),
		"00001.m2ts": fixtureVideo(t, "disc-src", "00001.m2ts"),
		"00002.m2ts": fixtureVideo(t, "disc-src", "00000.m2ts"),
	}
	files := map[string][]byte{}
	for s, raw := range streams {
		files["messy-movies/"+name+"/"+s] = raw
	}
	stage(t, plus(3, 0, 0), files, "messy-movies/"+name)

	var ids []string
	for _, e := range rows(t, discFolder(t, "/media/messy-movies/"+name)["entries"], "entries") {
		ids = append(ids, str(e["id"]))
	}
	if len(ids) != 3 {
		t.Fatalf("the staged disc's entries = %v", ids)
	}
	nfos := map[string][]byte{"00000.nfo": movieNfo("Pi", 1998, "473", "tt0138704"), "00001.nfo": movieNfo("Arrival", 2016, "329865", "tt2543164"), "00002.nfo": movieNfo("Pi", 1998, "473", "")}
	for file, raw := range nfos {
		mediaWrite(t, filepath.Join(dir, file), raw)
	}
	// the staged folder's removal takes them when the test ends
	for _, id := range ids {
		call(t, "item_refresh", map[string]any{"id": id})
	}
	matched := func() []string {
		var out []string
		for _, e := range rows(t, discFolder(t, "/media/messy-movies/"+name)["entries"], "entries") {
			out = append(out, str(e["matched_to"]))
		}
		return out
	}
	if !eventually(func() bool {
		return slices.Equal(matched(), []string{"imdb:tt0138704 tmdb:473", "imdb:tt2543164 tmdb:329865", "tmdb:473"})
	}) {
		t.Fatalf("the streams are matched to %v, want Pi's ids, Arrival's and Pi's TMDB id alone", matched())
	}
	if note := str(discFolder(t, "/media/messy-movies/"+name)["note"]); note != "matched to 2 different titles, so at least 1 of these are the wrong film" {
		t.Errorf("note = %q, want Pi counted once", note)
	}
}

// discFolder is audit_disc_folders' row for one folder of the messy films.
func discFolder(t *testing.T, folder string) map[string]any {
	t.Helper()

	for _, f := range rows(t, call(t, "audit_disc_folders", map[string]any{"library": "Messy Movies"})["folders"], "folders") {
		if str(f["folder"]) == folder {
			return f
		}
	}
	t.Fatalf("audit_disc_folders has no row for %s", folder)

	return nil
}

// Interstellar held twice more, each copy named as a real library names one
// and carrying the film's ids: by a title TMDB lists for it in another
// country (recorded), and "Franchise (Year) - Title", the title after the
// year. Both are the film, which audit_file_path says, and the version and
// duplicate warnings read the files the same way: no copy is warned of as
// another film. Jellyfin holds each as an entry of its own, which
// audit_duplicates groups; Emby shows them as the messy Interstellar's
// versions, which audit_multiple_versions lists.
func TestCopiesNamedAsTheFileAuditReadsThem(t *testing.T) {
	video := fixture(t, "messy-movies/"+messyInterstellar+"/"+messyInterstellar+".mp4")
	nfo := movieNfo("Interstellar", 2014, "157336", "tt0816692")
	alt := "messy-movies/Csillagok Között (2014)"
	saga := "messy-movies/Interstellar Collection"
	stage(t, plus(2, 0, 0), map[string][]byte{
		alt + "/Csillagok Között (2014).mp4":                        video,
		alt + "/Csillagok Között (2014).nfo":                        nfo,
		saga + "/Interstellar Collection (2014) - Interstellar.mp4": video,
		saga + "/Interstellar Collection (2014) - Interstellar.nfo": nfo,
	}, alt, saga)

	copied := func(path string) bool {
		return strings.Contains(path, "Csillagok") || strings.Contains(path, "Interstellar Collection")
	}
	if found := rows(t, call(t, "audit_file_path", map[string]any{"library": "Messy Movies", "checks": "title,year"})["findings"], "findings"); slices.ContainsFunc(found, func(f map[string]any) bool { return copied(str(f["path"])) }) {
		t.Fatalf("audit_file_path reports a copy: %v", found)
	}

	var interstellar []map[string]any
	if isJellyfin() {
		all, _ := call(t, "audit_duplicates", map[string]any{"library": "Messy Movies"})["groups"].([]any)
		for _, g := range all {
			if members := rows(t, g, "group"); title(str(members[0]["name"])) == "Interstellar" {
				interstellar = members
			}
		}
		if len(interstellar) != 3 {
			t.Fatalf("Interstellar's group = %v, want the film and its two copies", interstellar)
		}
		// and each copy's own read says the same
		for _, m := range interstellar {
			if w := str(call(t, "item_get", map[string]any{"id": str(m["id"])})["warning"]); w != "" {
				t.Errorf("item_get %s warns: %s", m["path"], w)
			}
		}
	} else {
		for _, f := range rows(t, call(t, "audit_multiple_versions", map[string]any{"library": "Messy Movies"})["findings"], "findings") {
			if title(str(f["name"])) == "Interstellar" {
				interstellar = append(interstellar, f)
			}
		}
		if len(interstellar) != 1 || !strings.HasPrefix(str(interstellar[0]["detail"]), "3 versions: ") {
			t.Fatalf("Interstellar's versions = %v, want the film and its two copies", interstellar)
		}
		if w := str(call(t, "item_get", map[string]any{"id": str(interstellar[0]["id"])})["warning"]); w != "" {
			t.Errorf("item_get of Interstellar warns: %s", w)
		}
	}
	for _, m := range interstellar {
		if w := str(m["warning"]); w != "" {
			t.Errorf("%v warns: %s", m["path"], w)
		}
	}
}

// Alien and Dune held once more, each carrying the film's ids in a folder
// whose words before its year are the film's and whose words after name
// another film: "Alien (1979) - Aliens", the next film of Alien's TMDB
// collection (recorded), though TMDB's search for the whole title finds
// nothing; and "Dune (2021) Part Two", which TMDB's search in no year gives to
// Dune: Part Two (recorded), numbered as a film of its own. Each was clean to
// every audit; now audit_file_path has a row for each, and each copy's own
// read warns of probably another film - on Emby the Alien copy as one of the
// messy Alien's versions. The messy Alien's Directors Cut, its label naming
// no film, stays quiet.
func TestAFileNamingAnotherFilmAfterItsYear(t *testing.T) {
	alien, dune := "messy-movies/Alien (1979) - Aliens", "messy-movies/Dune (2021) Part Two"
	stage(t, plus(2, 0, 0), map[string][]byte{
		alien + "/Alien (1979) - Aliens.mp4": fixture(t, "messy-movies/"+messyAlien+"/"+messyAlien+".mp4"),
		alien + "/Alien (1979) - Aliens.nfo": movieNfo("Alien", 1979, "348", "tt0078748"),
		dune + "/Dune (2021) Part Two.mp4":   fixture(t, "messy-movies/"+messyDune+"/"+messyDune+".mp4"),
		dune + "/Dune (2021) Part Two.nfo":   movieNfo("Dune", 2021, "438631", "tt1160419"),
	}, alien, dune)

	want := map[string]struct{ names, pathTMDB, itemTMDB string }{
		"Alien (1979) - Aliens": {`"Alien (1979) - Aliens.mp4" names "Aliens", TMDB's film 679 Aliens (1986), another film of the series in TMDB's collection of Alien`, "679 Aliens (1986)", "348"},
		"Dune (2021) Part Two":  {`"Dune (2021) Part Two.mp4" names "Dune Part Two": TMDB lists 693134 Dune: Part Two (2024), numbered Two, as a film of its own, not Dune`, "693134 Dune: Part Two (2024)", "438631"},
	}
	staged := map[string]string{}
	for _, f := range rows(t, call(t, "audit_file_path", map[string]any{"library": "Messy Movies", "checks": "title,year"})["findings"], "findings") {
		path := str(f["path"])
		if strings.Contains(path, messyAlienCut) {
			t.Errorf("the Directors Cut was reported: %v", f)
		}
		for folder, w := range want {
			if !strings.Contains(path, "/"+folder+"/") {
				continue
			}
			staged[folder] = str(f["id"])
			if ps := strs(t, f["problems"], "problems"); len(ps) != 1 || ps[0] != "title: "+w.names+": another film matched to this one's ids" || str(f["path_tmdb"]) != w.pathTMDB || str(f["item_tmdb"]) != w.itemTMDB {
				t.Errorf("audit_file_path's row for %s = %v", folder, f)
			}
		}
	}
	for folder, w := range want {
		id := staged[folder]
		if id == "" {
			t.Errorf("audit_file_path has no row for %s", folder)

			continue
		}
		if got := str(call(t, "item_get", map[string]any{"id": id})["warning"]); !strings.HasPrefix(got, "probably") || !strings.Contains(got, w.names) {
			t.Errorf("item_get of %s warns %q, want probably another film: %s", folder, got, w.names)
		}
	}
	if isJellyfin() {
		return
	}
	var versions []map[string]any
	for _, f := range rows(t, call(t, "audit_multiple_versions", map[string]any{"library": "Messy Movies"})["findings"], "findings") {
		if strings.Contains(str(f["detail"]), "Alien (1979) - Aliens.mp4") {
			versions = append(versions, f)
		}
	}
	if len(versions) != 1 || !strings.HasPrefix(str(versions[0]["warning"]), "probably not one film: "+want["Alien (1979) - Aliens"].names) {
		t.Errorf("the messy Alien's versions = %v, want the copy warned of as Aliens", versions)
	}
}

// item_get and audit_file_path read the messy films' files the same way: a
// row saying the file is another film than the item is matched to is an
// item_get warning of probably another film, and one saying the year the
// item holds is the one to check is a warning saying so, never "probably".
// The messy Dune - its folder 2021, its nfo Lynch's 1984 film - is such a
// row. Items shown in more than one file are left out: their warning speaks
// for every file at once.
func TestItemGetAndTheFilePathAuditAgree(t *testing.T) {
	found := rows(t, call(t, "audit_file_path", map[string]any{"library": "Messy Movies", "checks": "title,year"})["findings"], "findings")
	checked := 0
	for _, f := range found {
		got := call(t, "item_get", map[string]any{"id": str(f["id"])})
		if got["versions"] != nil && len(rows(t, got["versions"], "versions")) > 1 {
			continue
		}
		checked++
		said := strings.Join(append(strs(t, f["problems"], "problems"), str(f["diagnosis"])), " | ")
		warning := str(got["warning"])
		another := strings.Contains(said, "matched to another film than the one on disk") || strings.Contains(said, "another film matched to this one's ids")
		if another != strings.HasPrefix(warning, "probably") {
			t.Errorf("%s: audit_file_path says %q, item_get warns %q", f["path"], said, warning)
		}
		if strings.Contains(said, "the year the item holds is the one to check") && !strings.Contains(warning, "the year the item holds is the one to check") {
			t.Errorf("%s: audit_file_path says the item's year is the one to check, item_get warns %q", f["path"], warning)
		}
	}
	if checked == 0 {
		t.Fatalf("no messy film was read by both: %v", found)
	}
}

// Entries sharing an id whose runtimes say they are no copies: the clean
// Limitless runs its real 106 minutes, and a second-long copy staged in the
// messy films with its ids is no cut of it - one of the ids is wrong. And the
// messy Memento, holding Breaking Bad's IMDb id, is a film sharing one id
// with a series.
func TestAuditDuplicatesByRuntimeAndKind(t *testing.T) {
	folder := "messy-movies/Limitless (2011)"
	stage(t, plus(1, 0, 0), map[string][]byte{
		folder + "/Limitless (2011).mp4": fixture(t, "messy-movies/"+messyArrival+"/"+messyArrival+".mp4"),
		folder + "/Limitless (2011).nfo": movieNfo("Limitless", 2011, "51876", "tt1219289"),
	}, folder)

	warnings := map[string]string{}
	all, _ := call(t, "audit_duplicates", nil)["groups"].([]any)
	for _, g := range all {
		var names []string
		warning := ""
		for _, m := range rows(t, g, "group") {
			names = append(names, str(m["type"])+" "+title(str(m["name"])))
			if w := str(m["warning"]); w != "" {
				warning = w
			}
		}
		warnings[strings.Join(sorted(names), " + ")] = warning
	}
	if w, ok := warnings["Movie Limitless + Movie Limitless"]; !ok || w != "probably not copies of one film: the entries run 1s and 106 min, more than twice as long, which no two cuts of one film are: one file is cut short or a sample, or one of the ids is wrong - compare the files before keeping either" {
		t.Errorf("the two Limitless = %q (grouped %v)", w, ok)
	}
	if w := warnings["Movie Memento + Series Breaking Bad"]; w != "probably not copies: a film and a series share one IMDb id, which names one title, so one of the ids is wrong: identify the wrong one (item_identify) rather than keep either" {
		t.Errorf("Memento and Breaking Bad = %q", w)
	}
	// and copies that are copies carry none
	if w, ok := warnings["Movie Arrival + Movie Arrival"]; !ok || w != "" {
		t.Errorf("the two Arrivals = %q (grouped %v)", w, ok)
	}
}
