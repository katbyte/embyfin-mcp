//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// findings returns the titles in an audit's worklist, sorted.
func findings(t *testing.T, out map[string]any) []string {
	t.Helper()

	var names []string
	for _, f := range rows(t, out["findings"], "findings") {
		names = append(names, title(str(f["name"])))
	}
	slices.Sort(names)

	return names
}

var yearSuffix = regexp.MustCompile(`\s*\((19|20)\d\d\)$`)

// title strips the "(1997)" a server keeps on the name of a film it could
// not match: Emby parses the year out of a bare folder name, Jellyfin
// leaves it in, and the tests do not care which.
func title(name string) string {
	return yearSuffix.ReplaceAllString(name, "")
}

// The clean libraries are clean: every audit that sweeps the library leaves
// them alone, but for the two defects the show library carries on purpose.
// Neither server records an episode it has no file for, so the show
// library's episodes are its files and nothing else (TestLibraryExport).
func TestAuditsLeaveTheCleanLibrariesAlone(t *testing.T) {
	type expect struct {
		// what the audit finds in each library
		movies, shows int
		// the audits of series and episodes have nothing to sweep in a film
		// library, so it scanning nothing there is right rather than a sweep
		// that failed
		seriesOnly bool
	}
	for audit, want := range map[string]expect{
		"audit_missing_metadata_provider": {},
		"audit_missing_overview":          {},
		// the clean films and series all carry a poster.jpg
		"audit_missing_poster": {},
		// the show library's episodes are a file each
		"audit_multiple_versions": {},
		// The Expanse S01E02's file is named for S01E01, which
		// TestAuditFilePathShows reads
		"audit_file_path":    {shows: 1},
		"audit_duplicates":   {},
		"audit_disc_folders": {},
		"audit_quality":      {},
		"audit_spelling":     {},
		// Breaking Bad holds one title on two episodes, which
		// TestAuditDuplicateEpisodes reads
		"audit_duplicate_episodes": {shows: 1, seriesOnly: true},
		"audit_duplicate_series":   {seriesOnly: true},
		"audit_runtime":            {seriesOnly: true},
		"audit_missing_episodes":   {seriesOnly: true},
	} {
		for lib, n := range map[string]int{"Movies": want.movies, "Shows": want.shows} {
			out := call(t, audit, map[string]any{"library": lib})
			if got := num(t, out["total_findings"], "total_findings"); got != n {
				t.Errorf("%s on %s found %d, want %d: %v", audit, lib, got, n, out)
			}
			scanned := num(t, out["items_scanned"], "items_scanned")
			switch {
			case lib == "Movies" && want.seriesOnly && scanned != 0:
				t.Errorf("%s swept %d items of a film library", audit, scanned)
			case (lib == "Shows" || !want.seriesOnly) && scanned == 0:
				t.Errorf("%s on %s scanned nothing", audit, lib)
			}
		}
	}
	// and the two the show library carries are the ones named
	if f := rows(t, call(t, "audit_file_path", map[string]any{"library": "Shows"})["findings"], "findings"); len(f) != 1 || str(f[0]["title_in_file"]) != "Dulcinea" {
		t.Errorf("the path finding in Shows = %v, want The Expanse's file named Dulcinea", f)
	}
	if g := rows(t, call(t, "audit_duplicate_episodes", map[string]any{"library": "Shows"})["groups"], "groups"); len(g) != 1 || str(g[0]["series"]) != "Breaking Bad" {
		t.Errorf("the repeated title in Shows = %v, want Breaking Bad's", g)
	}
	// nothing kept apart that the anime list folds into another show
	if out := call(t, "audit_anime_ids", map[string]any{"library": "Shows"}); num(t, out["items_scanned"], "items_scanned") != 3 ||
		num(t, out["total_ids_disagree"], "total_ids_disagree")+num(t, out["total_split_out"], "total_split_out")+num(t, out["total_kept_separate"], "total_kept_separate") != 0 {
		t.Errorf("audit_anime_ids on Shows = %v", out)
	}
}

// unmatchedShows are the messy series no nfo names: Star Trek The Next
// Generation has none, the A Knight of the Seven Kingdoms pair have none, and
// Andor's and Deep Space Nine's name no id. Both servers keep the double space
// in the second Knight folder's name. Sorted, as findings are.
var unmatchedShows = []string{"A Knight of the Seven  kingdoms", "A Knight of the Seven Kingdoms", "Andor", "Star Trek The Next Generation", "Star Trek: Deep Space Nine"}

func TestAuditMissingMetadataProvider(t *testing.T) {
	out := call(t, "audit_missing_metadata_provider", map[string]any{"library": "Messy Movies"})
	if got := findings(t, out); !slices.Equal(got, messyUnmatched) {
		t.Errorf("unmatched movies = %v, want %v", got, messyUnmatched)
	}
	if got := num(t, out["items_scanned"], "items_scanned"); got != messyMovies() {
		t.Errorf("scanned %d, want %d", got, messyMovies())
	}
	for _, f := range rows(t, out["findings"], "findings") {
		if str(f["id"]) == "" || !strings.HasPrefix(str(f["path"]), "/media/messy-movies/") || str(f["detail"]) != "no provider id" {
			t.Errorf("finding = %v", f)
		}
	}

	out = call(t, "audit_missing_metadata_provider", map[string]any{"library": "Messy Shows", "types": "Series"})
	if got := findings(t, out); !slices.Equal(got, unmatchedShows) {
		t.Errorf("unmatched shows = %v, want %v", got, unmatchedShows)
	}

	// across every library the count is the sum
	everywhere := len(messyUnmatched) + len(unmatchedShows)
	out = call(t, "audit_missing_metadata_provider", nil)
	if n := num(t, out["total_findings"], "total_findings"); n != everywhere {
		t.Errorf("unmatched everywhere = %d, want %d", n, everywhere)
	}
	// and a limit caps the worklist, not the count
	out = call(t, "audit_missing_metadata_provider", map[string]any{"limit": 1})
	if n := len(rows(t, out["findings"], "findings")); n != 1 || num(t, out["total_findings"], "total_findings") != everywhere {
		t.Errorf("limit 1 = %d findings of %v", n, out["total_findings"])
	}

	// missing names the providers to look for. The messy films' sidecars
	// carry TMDB and IMDB ids and never a TVDB one, so every film lacks
	// TVDB, and each lists the ids it does have
	out = call(t, "audit_missing_metadata_provider", map[string]any{"library": "Messy Movies", "missing": "tvdb"})
	if n := num(t, out["total_findings"], "total_findings"); n != messyMovies() {
		t.Errorf("missing tvdb = %d, want every messy film (%d): %v", n, messyMovies(), out["findings"])
	}
	for _, f := range rows(t, out["findings"], "findings") {
		name := title(str(f["name"]))
		detail, unmatched := str(f["detail"]), slices.Contains(messyUnmatched, name)
		switch {
		case unmatched && detail != "no tvdb id":
			t.Errorf("a film matched nowhere has no ids to list: %v", f)
		case name == "Memento" && detail != "no tvdb id; has imdb:tt0903747":
			t.Errorf("the film holding an IMDb id alone does not list it: %v", f)
		case !unmatched && name != "Memento" && !strings.HasPrefix(detail, "no tvdb id; has tmdb:"):
			t.Errorf("a film matched on TMDB does not say so: %v", f)
		}
	}
	// TMDB alone finds the films matched nowhere, and the one matched on its
	// IMDb id alone
	out = call(t, "audit_missing_metadata_provider", map[string]any{"library": "Messy Movies", "missing": "tmdb"})
	if got, want := findings(t, out), sorted(append([]string{"Memento"}, messyUnmatched...)); !slices.Equal(got, want) {
		t.Errorf("missing tmdb = %v, want %v", got, want)
	}
	// two providers named is an item holding neither: Memento's IMDb id
	// takes it off the list, and the films matched nowhere stay
	out = call(t, "audit_missing_metadata_provider", map[string]any{"library": "Messy Movies", "missing": "tmdb, IMDB"})
	if got := findings(t, out); !slices.Equal(got, messyUnmatched) {
		t.Errorf("missing tmdb and imdb = %v, want %v", got, messyUnmatched)
	}
	for _, f := range rows(t, out["findings"], "findings") {
		if str(f["detail"]) != "no tmdb/imdb id" {
			t.Errorf("a film matched nowhere = %v", f)
		}
	}
	// the anime providers: only .hack//Liminality carries an AniDB id, and
	// nothing a MyAnimeList one, so every other series is listed with the
	// ids it does have - Severance's three, in the order they are named
	out = call(t, "audit_missing_metadata_provider", map[string]any{"library": "Messy Shows", "missing": "anidb"})
	if got, want := findings(t, out), sorted(append([]string{"Severance"}, unmatchedShows...)); !slices.Equal(got, want) {
		t.Errorf("missing anidb = %v, want %v", got, want)
	}
	for _, f := range rows(t, out["findings"], "findings") {
		want := "no anidb id"
		if str(f["name"]) == "Severance" {
			want = "no anidb id; has tmdb:95396 imdb:tt11280740 tvdb:371980"
		}
		if str(f["detail"]) != want {
			t.Errorf("%v = %q, want %q", f["name"], f["detail"], want)
		}
	}
	out = call(t, "audit_missing_metadata_provider", map[string]any{"library": "Messy Shows", "missing": "myanimelist"})
	if n := num(t, out["total_findings"], "total_findings"); n != messySeries {
		t.Errorf("missing myanimelist = %d, want every messy series (%d): %v", n, messySeries, out["findings"])
	}
	for _, f := range rows(t, out["findings"], "findings") {
		if str(f["name"]) == ".hack//Liminality" && str(f["detail"]) != "no myanimelist id; has anidb:222" {
			t.Errorf(".hack//Liminality = %v", f)
		}
	}
	// the messy episodes' sidecars name no ids at all, so each is an episode
	// matched nowhere; the clean show library's were all matched
	out = call(t, "audit_missing_metadata_provider", map[string]any{"library": "Messy Shows", "types": "Episode"})
	if n, scanned := num(t, out["total_findings"], "total_findings"), num(t, out["items_scanned"], "items_scanned"); n != messyEpisodes || scanned != messyEpisodes {
		t.Errorf("messy episodes matched nowhere = %d of %d, want all %d", n, scanned, messyEpisodes)
	}
	for _, f := range rows(t, out["findings"], "findings") {
		if str(f["detail"]) != "no provider id" || !strings.HasPrefix(str(f["path"]), "/media/messy-shows/") {
			t.Errorf("episode finding = %v", f)
		}
	}
	if out := call(t, "audit_missing_metadata_provider", map[string]any{"library": "Shows", "types": "Series,Episode"}); num(t, out["total_findings"], "total_findings") != 0 || num(t, out["items_scanned"], "items_scanned") != 3+9 {
		t.Errorf("the clean show library = %v, want its 3 series and 9 episodes all matched", out)
	}
	// and a misspelling is refused rather than flagging every item
	if msg := callErr(t, "audit_missing_metadata_provider", map[string]any{"missing": "tmbd"}); !strings.Contains(msg, "tmdb, imdb, tvdb") {
		t.Errorf("a misspelled provider = %s", msg)
	}

	// ignore leaves a library out by its folder, before its items are
	// counted: without the messy show library its unmatched shows go, and
	// its series come off the count
	all := call(t, "audit_missing_metadata_provider", nil)
	out = call(t, "audit_missing_metadata_provider", map[string]any{"ignore": []any{"messy shows"}})
	if got := findings(t, out); !slices.Equal(got, messyUnmatched) {
		t.Errorf("ignoring Messy Shows = %v, want %v", got, messyUnmatched)
	}
	if got, want := num(t, out["items_scanned"], "items_scanned"), num(t, all["items_scanned"], "items_scanned")-messySeries; got != want {
		t.Errorf("ignoring Messy Shows scanned %d, want %d", got, want)
	}
	if msg := callErr(t, "audit_missing_metadata_provider", map[string]any{"ignore": []any{"No Such Library"}}); !strings.Contains(msg, "no library named") {
		t.Errorf("an unknown library = %s", msg)
	}
}

// The films with no plot and no poster: Arrival's nfo has ids and nothing
// else, and the three no nfo names at all.
var messyBare = sorted(append([]string{"Arrival"}, messyUnmatched...))

func TestAuditMissingOverview(t *testing.T) {
	out := call(t, "audit_missing_overview", map[string]any{"library": "Messy Movies"})
	if got := findings(t, out); !slices.Equal(got, messyBare) {
		t.Errorf("no overview = %v, want %v", got, messyBare)
	}
	for _, f := range rows(t, out["findings"], "findings") {
		if str(f["detail"]) != "no overview" || str(f["id"]) == "" {
			t.Errorf("finding = %v", f)
		}
	}
	// the messy series with no tvshow.nfo to give them a plot
	out = call(t, "audit_missing_overview", map[string]any{"library": "Messy Shows", "types": "Series"})
	if got, want := findings(t, out), []string{"A Knight of the Seven  kingdoms", "A Knight of the Seven Kingdoms", "Star Trek The Next Generation"}; !slices.Equal(got, want) {
		t.Errorf("series with no overview = %v, want %v", got, want)
	}
	// and the episodes with no nfo beside them: the Knight pair, named by
	// the server after their files. Every other messy episode's nfo has a
	// plot
	out = call(t, "audit_missing_overview", map[string]any{"library": "Messy Shows", "types": "Episode"})
	if got, want := findings(t, out), []string{"A Knight of the Seven Kingdoms S01E01", "A Knight of the Seven Kingdoms S01E02"}; !slices.Equal(got, want) || num(t, out["items_scanned"], "items_scanned") != messyEpisodes {
		t.Errorf("episodes with no overview = %v of %v scanned, want %v of %d", got, out["items_scanned"], want, messyEpisodes)
	}

	// an overview of nothing but spaces is no overview: Jellyfin keeps one
	// as it was sent, Emby keeps none, and the audit reads both alike
	dune := findItem(t, "Messy Movies", "Movie", "Dune")
	plot := str(call(t, "item_get", map[string]any{"id": dune})["overview"])
	if plot == "" {
		t.Fatal("the messy Dune has no plot to take away")
	}
	t.Cleanup(func() { updateItem(t, dune, map[string]any{"Overview": plot}) })
	updateItem(t, dune, map[string]any{"Overview": "   "})
	if got := findings(t, call(t, "audit_missing_overview", map[string]any{"library": "Messy Movies"})); !slices.Equal(got, sorted(append([]string{"Dune"}, messyBare...))) {
		t.Errorf("with Dune's plot all spaces = %v, want Dune beside %v", got, messyBare)
	}
}

func TestAuditMissingPoster(t *testing.T) {
	out := call(t, "audit_missing_poster", map[string]any{"library": "Messy Movies"})
	if got := findings(t, out); !slices.Equal(got, messyBare) {
		t.Errorf("no poster = %v, want %v", got, messyBare)
	}
	for _, f := range rows(t, out["findings"], "findings") {
		if str(f["detail"]) != "no primary image" {
			t.Errorf("finding = %v", f)
		}
	}
	// no messy series has a poster.jpg, and with the fetchers off nothing
	// gave it one
	out = call(t, "audit_missing_poster", map[string]any{"library": "Messy Shows"})
	if got, want := findings(t, out), sorted(append([]string{".hack//Liminality", "Severance"}, unmatchedShows...)); !slices.Equal(got, want) {
		t.Errorf("series with no poster = %v, want %v", got, want)
	}
	// nor any messy episode an image, where the clean show library's were
	// all given one
	out = call(t, "audit_missing_poster", map[string]any{"library": "Messy Shows", "types": "Episode"})
	if n := num(t, out["total_findings"], "total_findings"); n != messyEpisodes || num(t, out["items_scanned"], "items_scanned") != messyEpisodes {
		t.Errorf("messy episodes with no image = %d of %v, want all %d", n, out["items_scanned"], messyEpisodes)
	}
	out = call(t, "audit_missing_poster", map[string]any{"library": "Shows", "types": "Series,Episode"})
	if n := num(t, out["total_findings"], "total_findings"); n != 0 || num(t, out["items_scanned"], "items_scanned") != 3+9 {
		t.Errorf("the clean show library = %d missing of %v, want none of 12", n, out["items_scanned"])
	}
}

// The messy Dune's folder says (2021) and its nfo 1984; every other messy
// film's folder names it as the server does, edition words after the year
// included ("Alien (1979) Directors Cut" is Alien). The loose DVD's file is
// VTS_01_1.VOB, which names no film: a disc's files are read by the folder
// above them, and that names the film the server holds.
func TestAuditFilePath(t *testing.T) {
	out := call(t, "audit_file_path", map[string]any{"library": "Messy Movies"})
	if got := findings(t, out); !slices.Equal(got, []string{"Dune"}) {
		t.Fatalf("file path = %v, want [Dune]", got)
	}
	f := rows(t, out["findings"], "findings")[0]
	if problems := strs(t, f["problems"], "problems"); len(problems) != 1 || !strings.Contains(problems[0], "year: path says 2021, metadata says 1984") || str(f["type"]) != "Movie" {
		t.Errorf("Dune = %v", f)
	}
	byCheck, _ := out["by_check"].(map[string]any)
	if num(t, byCheck["year"], "year") != 1 || len(byCheck) != 1 {
		t.Errorf("by_check = %v", byCheck)
	}
	// the year check alone, and the title check alone
	if got := findings(t, call(t, "audit_file_path", map[string]any{"library": "Messy Movies", "checks": "year"})); !slices.Equal(got, []string{"Dune"}) {
		t.Errorf("checks=year = %v", got)
	}
	if n := num(t, call(t, "audit_file_path", map[string]any{"library": "Messy Movies", "checks": "title"})["total_findings"], "total_findings"); n != 0 {
		t.Errorf("checks=title found %d", n)
	}
	// types narrows what is swept: the films alone are the whole library
	if n := num(t, call(t, "audit_file_path", map[string]any{"library": "Messy Movies", "types": "Series"})["items_scanned"], "items_scanned"); n != 0 {
		t.Errorf("types=Series swept %d items of a film library", n)
	}
	// a check it does not have is refused rather than running none
	if msg := callErr(t, "audit_file_path", map[string]any{"checks": "year,runtime"}); !strings.Contains(msg, `checks must be among series, season, episode, title, year, lookalike, not "runtime"`) {
		t.Errorf("an unknown check: %s", msg)
	}
}

// Andor's files and nfos disagree the ways a bulk import leaves them, and
// each is the check that says so. A file named without an episode marker
// (07 - Announcement) claims no series, and is left alone.
//
// The run is where the servers differ: the nfo beside Andor S01E02E03.mp4
// ends the run at episode 2, which Jellyfin reads over the
// file name, so it holds E02 alone and the file's E03 reads as missing; Emby
// takes the run from the file name and holds E02-E03, which the file agrees
// with.
func TestAuditFilePathSeriesSeasonAndEpisode(t *testing.T) {
	out := call(t, "audit_file_path", map[string]any{"library": "Messy Shows"})
	got := map[string][]string{}
	for _, f := range rows(t, out["findings"], "findings") {
		if str(f["series"]) != "Andor" || str(f["type"]) != "Episode" {
			t.Errorf("a finding outside Andor: %v", f)
			continue
		}
		got[filepath.Base(str(f["path"]))] = strs(t, f["problems"], "problems")
	}
	want := map[string]string{
		"Breaking Bad S01E06.mp4": `series: the file is named for "Breaking Bad", the server holds it under "Andor"`,
		"Andor S01E04.mp4":        "episode: the file says E04, the server holds E05",
		"Andor S02E08.mp4":        "season: the file says season 2, the server holds season 1",
	}
	if isJellyfin() {
		want["Andor S01E02E03.mp4"] = "episode: the file holds E02-E03, the server holds E02 alone"
	}
	for file, problem := range want {
		if ps := got[file]; len(ps) != 1 || !strings.HasPrefix(ps[0], problem) {
			t.Errorf("%s = %v, want %q", file, ps, problem)
		}
	}
	for file := range got {
		if _, ok := want[file]; !ok {
			t.Errorf("%s was reported: %v", file, got[file])
		}
	}
	byCheck := object(t, out["by_check"], "by_check")
	episodes := 1
	if isJellyfin() {
		episodes = 2
	}
	if num(t, byCheck["series"], "series") != 1 || num(t, byCheck["season"], "season") != 1 || num(t, byCheck["episode"], "episode") != episodes {
		t.Errorf("by_check = %v", byCheck)
	}
	// one check at a time finds its own rows and no others
	for check, n := range map[string]int{"series": 1, "season": 1, "episode": episodes} {
		if got := num(t, call(t, "audit_file_path", map[string]any{"library": "Messy Shows", "checks": check})["total_findings"], "total_findings"); got != n {
			t.Errorf("checks=%s found %d, want %d", check, got, n)
		}
	}
	// the plainest first: a number or the series disagreeing, then titles,
	// then years. Across every library that is Andor's rows, then The
	// Expanse's file named for another episode, then the messy Dune's year,
	// and a limit keeps the first of them
	all := call(t, "audit_file_path", nil)
	var order []string
	for _, f := range rows(t, all["findings"], "findings") {
		check, _, _ := strings.Cut(strs(t, f["problems"], "problems")[0], ":")
		order = append(order, check)
	}
	numbers := len(got)
	if len(order) != numbers+2 || !slices.Equal(order[numbers:], []string{"title", "year"}) {
		t.Errorf("every library's rows lead with %v, want Andor's %d, then a title, then a year", order, numbers)
	}
	for _, check := range order[:min(numbers, len(order))] {
		if check != "series" && check != "season" && check != "episode" {
			t.Errorf("rows lead with %v, want Andor's numbers and series first", order)
		}
	}
	capped := call(t, "audit_file_path", map[string]any{"limit": 1})
	found := rows(t, capped["findings"], "findings")
	if len(found) != 1 || num(t, capped["total_findings"], "total_findings") != numbers+2 {
		t.Fatalf("limit 1 = %v of %v, want one row of %d", found, capped["total_findings"], numbers+2)
	}
	if str(found[0]["series"]) != "Andor" || !slices.Contains([]string{"series", "season", "episode"}, strings.SplitN(strs(t, found[0]["problems"], "problems")[0], ":", 2)[0]) {
		t.Errorf("the row a limit of 1 keeps = %v, want one of Andor's numbers or its series", found[0])
	}
	// the episode held as a run is held whole: E03 is covered, not missing
	andor := findItem(t, "Messy Shows", "Series", "Andor")
	e03s := rows(t, call(t, "show_episodes_exist", map[string]any{"series_id": andor, "episodes": []map[string]any{{"season": 1, "episode": 3}}})["episodes"], "episodes")
	if len(e03s) != 1 {
		t.Fatalf("show_episodes_exist answered %v for one pair", e03s)
	}
	e03 := e03s[0]
	if e03["exists"] != !isJellyfin() {
		t.Errorf("S01E03 of Andor = %v, want held only where the server reads the run from the file (Emby)", e03)
	}
}

// Separate entries for one title, as the servers show them: two files a
// server shows as one film's versions are audit_multiple_versions', not two
// entries. Jellyfin holds the two messy Aliens (two folders, one TMDB id) as
// two films; Emby shows them as one film's versions, so in the messy library
// it has no duplicates at all.
func TestAuditDuplicates(t *testing.T) {
	out := call(t, "audit_duplicates", map[string]any{"library": "Messy Movies"})
	groups, _ := out["groups"].([]any)
	if !isJellyfin() {
		if num(t, out["total_findings"], "total_findings") != 0 || len(groups) != 0 {
			t.Errorf("duplicates in the messy library on Emby = %v, want none", out)
		}
		// what Emby shows: the eleven folders' films, both Aliens as one
		// and both Blade Runner files as one
		if n := num(t, out["items_scanned"], "items_scanned"); n != messyMovies()-2 {
			t.Errorf("items_scanned = %d, want the %d films Emby shows", n, messyMovies()-2)
		}
	} else {
		if num(t, out["total_findings"], "total_findings") != 1 || len(groups) != 1 {
			t.Fatalf("duplicates = %v", out)
		}
		group := rows(t, groups[0], "group")
		var plain, cut bool
		for _, it := range group {
			if str(it["name"]) != "Alien" {
				t.Errorf("a duplicate of %v", it["name"])
			}
			switch {
			case strings.Contains(str(it["path"]), messyAlien+"/"):
				plain = true
			case strings.Contains(str(it["path"]), messyAlienCut+"/"):
				cut = true
			}
		}
		if len(group) != 2 || !plain || !cut {
			t.Errorf("the group is not the two messy Aliens: %v", group)
		}
	}

	// across libraries the clean copies join, and a limit caps the groups
	// returned but not the count: Alien, Arrival and Blade Runner are each in
	// both film libraries, and Alien sorts first
	out = call(t, "audit_duplicates", map[string]any{"types": "Movie", "limit": 1})
	groups, _ = out["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("limit 1 returned %d groups", len(groups))
	}
	aliens := 3 // the clean one and the two messy entries
	if !isJellyfin() {
		aliens = 2 // the clean one and the messy one Emby shows
	}
	if names := names(t, groups[0], "group"); len(names) != aliens || names[0] != "Alien" {
		t.Errorf("the first group across libraries = %v, want %d Aliens", names, aliens)
	}
	if n := num(t, out["total_findings"], "total_findings"); n != 3 {
		t.Errorf("total_findings = %d, want Alien, Arrival and Blade Runner", n)
	}
	// the clean libraries alone have none
	out = call(t, "audit_duplicates", map[string]any{"library": "Movies"})
	if num(t, out["total_findings"], "total_findings") != 0 {
		t.Errorf("Movies has duplicates: %v", out)
	}

	// across every kind, the series held twice, and an IMDb id joining the
	// film that holds Breaking Bad's to Breaking Bad: an IMDb id names one
	// title whatever its kind, where a TMDB or TVDB number is a film's in one
	// list and a series' in another, and only joins its own kind
	var got []string
	all, _ := call(t, "audit_duplicates", nil)["groups"].([]any)
	for _, g := range all {
		var members []string
		for _, it := range rows(t, g, "group") {
			members = append(members, str(it["type"])+" "+title(str(it["name"])))
		}
		got = append(got, strings.Join(sorted(members), " + "))
	}
	alien := "Movie Alien + Movie Alien + Movie Alien"
	if !isJellyfin() {
		alien = "Movie Alien + Movie Alien"
	}
	want := []string{
		alien,
		"Movie Arrival + Movie Arrival",
		"Movie Blade Runner + Movie Blade Runner",
		"Movie Memento + Series Breaking Bad",
		"Series Severance + Series Severance",
	}
	if !slices.Equal(sorted(got), want) {
		t.Errorf("duplicates across every kind = %v, want %v", sorted(got), want)
	}
}

// The films a server shows as one title in several versions. Both show the
// two Blade Runner files, named as versions in one folder, as one film;
// Emby also shows the two messy Aliens, in two folders and sharing a TMDB id,
// as one. Jellyfin stores its merge (a sweep holds one Blade Runner with two
// files); Emby stores each file as an item and merges them only in what it
// shows people, which is what the audit reads there.
func TestAuditMultipleVersions(t *testing.T) {
	out := call(t, "audit_multiple_versions", map[string]any{"library": "Messy Movies"})
	want := []string{"Blade Runner"}
	shown := messyMovies()
	if !isJellyfin() {
		want, shown = []string{"Alien", "Blade Runner"}, messyMovies()-2
	}
	if got := num(t, out["items_scanned"], "items_scanned"); got != shown {
		t.Errorf("scanned %d, want the %d films the server shows", got, shown)
	}
	if got := findings(t, out); !slices.Equal(got, want) {
		t.Errorf("multiple versions = %v, want %v", got, want)
	}
	for _, f := range rows(t, out["findings"], "findings") {
		d := str(f["detail"])
		switch title(str(f["name"])) {
		case "Blade Runner":
			if !strings.HasPrefix(d, "2 versions: ") || !strings.Contains(d, "Blade Runner (1982) - 1080p.mp4") || !strings.Contains(d, "Blade Runner (1982) - 2160p.mp4") {
				t.Errorf("Blade Runner's detail = %q", d)
			}
		case "Alien":
			if !strings.HasPrefix(d, "2 versions: ") || !strings.Contains(d, "Alien (1979).mp4") || !strings.Contains(d, "Alien (1979) Directors Cut.mp4") {
				t.Errorf("Alien's detail = %q", d)
			}
		}
	}
	// films alone, capped: the first
	if got := call(t, "audit_multiple_versions", map[string]any{"library": "Messy Movies", "types": "Movie", "limit": 1}); len(rows(t, got["findings"], "findings")) != 1 || num(t, got["total_findings"], "total_findings") != len(want) {
		t.Errorf("types Movie limit 1 = %v", got)
	}
	// and episodes alone find none: no episode is held in two versions
	if n := num(t, call(t, "audit_multiple_versions", map[string]any{"library": "Messy Movies", "types": "Episode"})["items_scanned"], "items_scanned"); n != 0 {
		t.Errorf("types Episode swept %d items of a film library", n)
	}
}

// .hack//Liminality's episodes run three minutes but the last, cut to a second.
// The messy Severance's five-second third episode among one-second ones is
// not a finding: 0 minutes against a 0 minute median is under the two-minute
// floor, which is what the floor is for.
func TestAuditRuntimeEpisodes(t *testing.T) {
	out := call(t, "audit_runtime", map[string]any{"library": "Messy Shows"})
	if got := num(t, out["items_scanned"], "items_scanned"); got != messyEpisodes {
		t.Errorf("scanned %d episodes, want %d", got, messyEpisodes)
	}
	found := rows(t, out["findings"], "findings")
	if len(found) != 1 || num(t, out["total_findings"], "total_findings") != 1 {
		t.Fatalf("findings = %v, want the one cut short", found)
	}
	if f := found[0]; str(f["name"]) != ".hack//Liminality S01E03 In the Case of Kyoko Tohno" || str(f["detail"]) != "0 min, season median 3 min (100% off)" || !strings.HasSuffix(str(f["path"]), "/hack Liminality S01E03.mp4") {
		t.Errorf("finding = %v", f)
	}
	// a tolerance of 100% forgives a file that runs none of its median
	if n := num(t, call(t, "audit_runtime", map[string]any{"library": "Messy Shows", "tolerance_percent": 100})["total_findings"], "total_findings"); n != 0 {
		t.Errorf("tolerance 100 found %d", n)
	}
	// and the clean shows, their episodes all a second, have nothing off
	if n := num(t, call(t, "audit_runtime", map[string]any{"library": "Shows", "limit": 1})["total_findings"], "total_findings"); n != 0 {
		t.Errorf("Shows has %d runtimes off", n)
	}
}

// Movie runtimes come from TMDB, through the provider proxy: Interstellar's
// nfo says 169 minutes and the file runs a second, but the audit asks TMDB,
// not the nfo, so every one-second film in the messy library is off.
func TestAuditProviderRuntime(t *testing.T) {
	needsTMDBCassette(t)
	out := call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "runtime"})
	got := findings(t, out)
	// every file is a second long, and five films are passed over for three
	// reasons: Princess Mononoke and the two discs hold no id at all, Memento
	// holds an IMDb id and no TMDB one to read a runtime by, and TMDB has no
	// film for the Despecialized Edition's id, so no runtime to hold it to
	want := []string{"Alien", "Alien", "Arrival", "Blade Runner", "Dune", "Interstellar"}
	if !versionsMerged() {
		want = []string{"Alien", "Alien", "Arrival", "Blade Runner", "Blade Runner", "Dune", "Interstellar"}
	}
	if !slices.Equal(got, want) {
		t.Errorf("runtime off = %v, want %v", got, want)
	}
	if got := num(t, out["items_scanned"], "items_scanned"); got != messyMovies() {
		t.Errorf("scanned %d, want %d", got, messyMovies())
	}
	for _, f := range rows(t, out["findings"], "findings") {
		if ps, _ := f["problems"].([]any); len(ps) != 1 || !strings.Contains(str(ps[0]), "runtime: file") || !strings.Contains(str(ps[0]), "TMDB says") {
			t.Errorf("problems = %v", f["problems"])
		}
	}

	// paging: two lookups per call, then continue from next_offset
	out = call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "runtime", "max_lookups": 2})
	if len(rows(t, out["findings"], "findings")) != 2 {
		t.Errorf("max_lookups 2 = %v", out["findings"])
	}
	next, ok := out["next_offset"]
	if !ok {
		t.Fatal("no next_offset after a partial sweep")
	}
	rest := call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "runtime", "offset": next})
	if n, want := num(t, rest["total_findings"], "total_findings"), len(want)-2; n != want {
		t.Errorf("the rest = %d findings, want %d", n, want)
	}
	// tolerance: a one-second file is always more than 99% off, so a
	// tolerance of 100 finds nothing
	out = call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "runtime", "tolerance_percent": 100})
	if n := num(t, out["total_findings"], "total_findings"); n != 0 {
		t.Errorf("tolerance 100 found %d", n)
	}
	// only TMDB yet, said plainly
	if msg := callErr(t, "audit_provider", map[string]any{"provider": "tvdb"}); !strings.Contains(msg, "provider must be tmdb") {
		t.Errorf("another provider: %s", msg)
	}
}

// needsTMDBCassette skips a TMDB-backed test when it cannot run: a
// recording needs a real TMDB key, and a replay needs the recording to have
// been made (make record with EMBYFIN_TMDB_TOKEN set).
func needsTMDBCassette(t *testing.T) {
	t.Helper()

	needsTMDBRecording(t, "GET api.themoviedb.org/3/movie/348")
}

// needsTMDBRecording skips unless the TMDB request named can be answered:
// live while recording with a key, else from the cassette.
func needsTMDBRecording(t *testing.T, key string) {
	t.Helper()

	if recording() {
		if os.Getenv("EMBYFIN_TMDB_TOKEN") == "" && os.Getenv("EMBYFIN_TMDB_KEY") == "" {
			t.Skip("recording the TMDB lookups needs EMBYFIN_TMDB_TOKEN")
		}
		return
	}
	raw, err := os.ReadFile(filepath.Join(cassetteDir(), "api.themoviedb.org.json"))
	if err != nil || !strings.Contains(string(raw), `"key": "`+key+`"`) {
		t.Skipf("%s has not been recorded; run make record with EMBYFIN_TMDB_TOKEN set", key)
	}
}

// audit_all is the overview: one row per audit with the counts the audits
// themselves report, across the whole server. Every audit has a row: the
// ones that need more than the server (a language, TMDB, the Anime-Lists
// file) and the server-wide orphans check when a library is given are
// rows marked skipped, with why.
func TestAuditAll(t *testing.T) {
	out := call(t, "audit_all", map[string]any{"library": "Messy Movies"})
	counts := map[string]int{}
	for _, row := range rows(t, out["audits"], "audits") {
		counts[str(row["audit"])] = num(t, row["findings"], "findings")
	}
	want := map[string]int{
		"audit_missing_metadata_provider": len(messyUnmatched),
		"audit_missing_poster":            len(messyBare),
		"audit_missing_overview":          len(messyBare),
		// Dune's year
		"audit_file_path": 1,
		// Blade Runner's two files; on Emby the two Aliens too (below)
		"audit_multiple_versions":  1,
		"audit_duplicates":         1,
		"audit_duplicate_episodes": 0,
		"audit_duplicate_series":   0,
		"audit_disc_folders":       1,
		"audit_runtime":            0,
		// every film but the Blade Runner cuts and the Blu-ray kept whole,
		// which the server never probed and so is not judged
		"audit_quality":          9,
		"audit_missing_episodes": 0,
		// the Despecialized Edition's Science-Fiction
		"audit_spelling": 1,
	}
	if !isJellyfin() {
		// Emby shows the two messy Aliens, sharing a TMDB id, as one film's
		// versions as well, which leaves no duplicates in the library
		want["audit_multiple_versions"], want["audit_duplicates"] = 2, 0
		want["audit_quality"] = 8 // and judges them as one film
	}
	for audit, n := range want {
		if counts[audit] != n {
			t.Errorf("%s = %d, want %d", audit, counts[audit], n)
		}
	}
	// what nobody has watched depends on what the other tests played, so
	// its row is checked for being there rather than for its count
	if _, ok := counts["audit_unwatched"]; !ok {
		t.Error("no audit_unwatched row")
	}
	// exactly these are skipped, and nothing else: a skipped row reads 0,
	// so an audit marked skipped by mistake would pass for one finding
	// nothing
	skippedIn := func(out map[string]any) []string {
		var names []string
		for _, row := range rows(t, out["audits"], "audits") {
			if s, _ := row["skipped"].(bool); s {
				names = append(names, str(row["audit"]))
				if str(row["note"]) == "" {
					t.Errorf("%s is skipped without a note", row["audit"])
				}
			}
		}
		slices.Sort(names)

		return names
	}
	if got, want := skippedIn(out), []string{"audit_anime_ids", "audit_language", "audit_orphans", "audit_provider"}; !slices.Equal(got, want) {
		t.Errorf("skipped for one library = %v, want %v", got, want)
	}
	if len(counts) != len(want)+5 {
		t.Errorf("audits = %v, want %d rows", counts, len(want)+5)
	}
	// the total is the defects: what nobody has watched is a row, not a fault
	total := 0
	for _, n := range want {
		total += n
	}
	if num(t, out["total_findings"], "total_findings") != total {
		t.Errorf("total_findings = %v, want %d", out["total_findings"], total)
	}
	// across the server, the orphans check runs and the three that need
	// more than the server are all that is skipped
	whole := call(t, "audit_all", nil)
	if got, want := skippedIn(whole), []string{"audit_anime_ids", "audit_language", "audit_provider"}; !slices.Equal(got, want) {
		t.Errorf("skipped across the server = %v, want %v", got, want)
	}

	// the clean libraries are clean, but for the show library's file named
	// for another episode and its title held twice (TestAuditsLeaveTheCleanLibrariesAlone)
	clean := call(t, "audit_all", map[string]any{"library": "Movies"})
	if n := num(t, clean["total_findings"], "total_findings"); n != 0 {
		t.Errorf("Movies total_findings = %d: %v", n, clean["audits"])
	}
	if n := num(t, call(t, "audit_all", map[string]any{"library": "Shows"})["total_findings"], "total_findings"); n != 2 {
		t.Errorf("Shows total_findings = %d, want The Expanse's path and Breaking Bad's repeated title", n)
	}
	// and each clean row is the audit's own count, which TestAuditAllMatchesEachAudit
	// checks for the other libraries
	for _, row := range rows(t, clean["audits"], "audits") {
		name := str(row["audit"])
		if s, _ := row["skipped"].(bool); s {
			continue
		}
		own := call(t, name, map[string]any{"library": "Movies"})
		if num(t, own["total_findings"], name) != num(t, row["findings"], "findings") || num(t, own["items_scanned"], name) != num(t, row["items_scanned"], "items_scanned") {
			t.Errorf("Movies %s: audit_all counted %v of %v, the audit %v of %v", name, row["findings"], row["items_scanned"], own["total_findings"], own["items_scanned"])
		}
	}

	// A music library is read for what applies to music: its albums' covers
	// - Thundercolor has none, and on Emby none of the albums has one, as
	// they have no folder of their own (TestAuditMusicMissingPoster) - and
	// the spellings of their genres, SirensCeol's Electronica beside the
	// Electronic of the rest (TestAuditMusicSpelling). Every audit of films,
	// series or episodes is a row saying it has nothing there to read, not a
	// 0 that reads as a clean library.
	covers := 1
	if !isJellyfin() {
		covers = len(albums)
	}
	music := call(t, "audit_all", map[string]any{"library": "Music"})
	ran := map[string]bool{}
	for _, row := range rows(t, music["audits"], "audits") {
		name := str(row["audit"])
		if s, _ := row["skipped"].(bool); s {
			if str(row["note"]) == "" {
				t.Errorf("Music %s is skipped without a note", name)
			}
			continue
		}
		ran[name] = true
		want, applies := map[string]int{"audit_missing_poster": covers, "audit_spelling": 1}[name]
		switch {
		case !applies:
			t.Errorf("Music %s ran: %v", name, row)
		case num(t, row["findings"], "findings") != want || num(t, row["items_scanned"], "items_scanned") != len(albums) || str(row["types"]) != "MusicAlbum":
			t.Errorf("Music %s = %v, want %d of the %d albums, read as MusicAlbum", name, row, want, len(albums))
		}
		// and it is the audit's own count, asked for the albums
		own := call(t, name, map[string]any{"library": "Music", "types": str(row["types"])})
		if num(t, own["total_findings"], name) != num(t, row["findings"], "findings") || num(t, own["items_scanned"], name) != num(t, row["items_scanned"], "items_scanned") {
			t.Errorf("Music %s: audit_all counted %v of %v, the audit %v of %v", name, row["findings"], row["items_scanned"], own["total_findings"], own["items_scanned"])
		}
	}
	if !ran["audit_missing_poster"] || !ran["audit_spelling"] || len(ran) != 2 {
		t.Errorf("over Music audit_all ran %v, want the covers and the spellings", ran)
	}
	if num(t, music["total_findings"], "total_findings") != covers+1 {
		t.Errorf("Music total_findings = %v, want the %d albums with no cover and the one spelling", music["total_findings"], covers)
	}
	// and across the server they count the albums beside the films and series
	for _, row := range rows(t, whole["audits"], "audits") {
		switch str(row["audit"]) {
		case "audit_missing_poster", "audit_spelling":
			if !strings.HasSuffix(str(row["types"]), ",MusicAlbum") {
				t.Errorf("across the server %v, want the albums read too", row)
			}
		}
	}
	// and the messy shows' defects are each counted
	shows := map[string]int{}
	for _, row := range rows(t, call(t, "audit_all", map[string]any{"library": "Messy Shows"})["audits"], "audits") {
		shows[str(row["audit"])] = num(t, row["findings"], "findings")
	}
	pathRows := 3 // Andor's series, episode and season rows
	if isJellyfin() {
		pathRows = 4 // and the run Jellyfin reads from the nfo
	}
	for audit, n := range map[string]int{
		"audit_missing_metadata_provider": len(unmatchedShows),
		"audit_missing_poster":            messySeries,
		"audit_missing_overview":          3,
		"audit_file_path":                 pathRows,
		"audit_multiple_versions":         0,
		"audit_duplicates":                0,
		"audit_duplicate_episodes":        0,
		"audit_duplicate_series":          1,
		"audit_disc_folders":              0,
		"audit_runtime":                   1,
		"audit_quality":                   messyEpisodes,
		"audit_missing_episodes":          3, // Andor, Deep Space Nine and The Next Generation
		"audit_spelling":                  1,
	} {
		n0, ok := shows[audit]
		if !ok || n0 != n {
			t.Errorf("Messy Shows %s = %d (a row: %v), want %d", audit, n0, ok, n)
		}
	}
}

// The audit family is complete: nothing may be added without a test here.
func TestAuditFamilyIsComplete(t *testing.T) {
	var got []string
	for _, name := range toolNames(t) {
		if strings.HasPrefix(name, "audit_") {
			got = append(got, name)
		}
	}
	want := []string{
		"audit_all", "audit_anime_ids", "audit_disc_folders", "audit_duplicate_episodes", "audit_duplicate_series", "audit_duplicates", "audit_file_path", "audit_language",
		"audit_missing_episodes", "audit_missing_metadata_provider", "audit_missing_overview",
		"audit_missing_poster", "audit_multiple_versions", "audit_orphans", "audit_provider", "audit_quality", "audit_runtime", "audit_spelling",
		"audit_unwatched",
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("audit tools = %v, want %v", got, want)
	}
}

// audit_language against files whose streams the servers probed: the messy
// Princess Mononoke carries Japanese audio, The Thirteenth Floor an English
// subtitle beside it, and every other fixture an audio track with no language
// tag - which is what most real rips carry too.
func TestAuditLanguage(t *testing.T) {
	names := func(out map[string]any) []string {
		var got []string
		for _, f := range rows(t, out["findings"], "findings") {
			got = append(got, title(str(f["name"])))
		}

		return got
	}

	japanese := call(t, "audit_language", map[string]any{"language": "ja", "library": "Messy Movies"})
	if got := names(japanese); !slices.Equal(got, []string{"Princess Mononoke"}) {
		t.Errorf("japanese audio = %v, want [Princess Mononoke]", got)
	}
	if f := rows(t, japanese["findings"], "findings"); len(f) == 1 && str(f[0]["detail"]) != "audio: jpn; subtitles: none" {
		t.Errorf("detail = %q, want the Japanese track and no subtitles", f[0]["detail"])
	}
	// and that film is the one the messy library cannot be watched in, or
	// heard, in English: its one track is tagged, and tagged something else.
	// The rest carry untagged audio, which may be English, and the Blu-ray
	// kept whole no audio track the server knows of
	for _, find := range []string{"no_audio", "unwatchable"} {
		out := call(t, "audit_language", map[string]any{"language": "eng", "find": find, "library": "Messy Movies"})
		scanned := num(t, out["items_scanned"], "items_scanned")
		f := rows(t, out["findings"], "findings")
		if len(f) != 1 || title(str(f[0]["name"])) != "Princess Mononoke" || str(f[0]["detail"]) != "audio: jpn; subtitles: none" || num(t, out["total_findings"], "total_findings") != 1 {
			t.Errorf("%s in English = %v, want Princess Mononoke alone", find, f)
		}
		// the films as people are shown them: Emby's versions judged together
		if scanned != messyMoviesShown() || num(t, out["untagged"], "untagged") != scanned-2 || num(t, out["no_audio_track"], "no_audio_track") != 1 {
			t.Errorf("%s in English: untagged %v and no_audio_track %v of %d scanned, want all but two, and one", find, out["untagged"], out["no_audio_track"], scanned)
		}
	}
	// episodes are swept too, and every messy one is untagged: nothing to
	// report, and each counted
	out := call(t, "audit_language", map[string]any{"language": "eng", "find": "unwatchable", "library": "Messy Shows"})
	if num(t, out["total_findings"], "total_findings") != 0 || num(t, out["items_scanned"], "items_scanned") != messyEpisodes || num(t, out["untagged"], "untagged") != messyEpisodes {
		t.Errorf("the messy episodes = %v, want all %d scanned and untagged", out, messyEpisodes)
	}
	// a series is not a file: asked for series alone, there is nothing to read
	if out := call(t, "audit_language", map[string]any{"language": "eng", "library": "Messy Shows", "types": "Series"}); num(t, out["total_findings"], "total_findings")+num(t, out["untagged"], "untagged")+num(t, out["no_audio_track"], "no_audio_track") != 0 {
		t.Errorf("types=Series = %v, want nothing judged", out)
	}

	// the rest carry untagged audio, so nothing is reported as lacking
	// Japanese: an untagged track may be it. The Blu-ray kept whole has no
	// audio track the server knows of - it never probed the disc - which is
	// counted apart rather than judged
	lacking := call(t, "audit_language", map[string]any{"language": "jpn", "find": "no_audio", "library": "Messy Movies"})
	scanned := num(t, lacking["items_scanned"], "items_scanned")
	if n := num(t, lacking["total_findings"], "total_findings"); n != 0 || scanned != messyMoviesShown() {
		t.Errorf("of %d scanned, lacking japanese: %v", scanned, lacking["findings"])
	}
	if n, none := num(t, lacking["untagged"], "untagged"), num(t, lacking["no_audio_track"], "no_audio_track"); none != 1 || n != scanned-2 {
		t.Errorf("untagged = %d and no_audio_track %d of %d scanned, want all but Princess Mononoke and the kept Blu-ray, and that one", n, none, scanned)
	}

	// the subtitle is read off the file beside the film, language from its name
	subtitled := call(t, "audit_language", map[string]any{"language": "eng", "find": "subtitles", "library": "Movies"})
	if got := names(subtitled); !slices.Equal(got, []string{"The Thirteenth Floor"}) {
		t.Errorf("english subtitles = %v, want [The Thirteenth Floor]", got)
	}
	// the films alone are the whole library (a limit is read where there
	// are two to cap, TestAuditLanguageStaged)
	if films := call(t, "audit_language", map[string]any{"language": "eng", "find": "subtitles", "library": "Movies", "types": "Movie"}); !slices.Equal(names(films), []string{"The Thirteenth Floor"}) || num(t, films["items_scanned"], "items_scanned") != 8 {
		t.Errorf("types Movie = %v of %v", names(films), films["items_scanned"])
	}

	// and it is what makes that film watchable in English when nothing else
	// in the library can be judged
	unwatchable := call(t, "audit_language", map[string]any{"language": "eng", "find": "unwatchable", "library": "Movies"})
	scanned = num(t, unwatchable["items_scanned"], "items_scanned")
	if n := num(t, unwatchable["total_findings"], "total_findings"); n != 0 {
		t.Errorf("unwatchable in english = %v", unwatchable["findings"])
	}
	if n := num(t, unwatchable["untagged"], "untagged"); n != scanned-1 {
		t.Errorf("untagged = %d of %d scanned, want all but The Thirteenth Floor", n, scanned)
	}

	if msg := callErr(t, "audit_language", map[string]any{"language": "eng", "find": "sideways"}); !strings.Contains(msg, "find must be") {
		t.Errorf("a bad find: %s", msg)
	}
	// no language, or the tag that means none, is refused rather than
	// answered: every item would lack it
	for language, want := range map[string]string{"": "language is required", " ": "language is required", "und": "und means no language", "UND": "und means no language"} {
		if msg := callErr(t, "audit_language", map[string]any{"language": language}); !strings.Contains(msg, want) {
			t.Errorf("language %q: %s", language, msg)
		}
	}
}
