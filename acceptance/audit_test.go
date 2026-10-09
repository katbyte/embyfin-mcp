//go:build integration

package acceptance

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"

	"github.com/katbyte/embyfin-mcp/lib/testenv"
	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
)

// findings returns the titles in an audit's worklist, sorted.
func findings(t *testing.T, out map[string]any) []string {
	t.Helper()

	var names []string
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		names = append(names, title(acc.Str(f["name"])))
	}
	slices.Sort(names)

	return names
}

// audit_missing_metadata is one audit of three problems, and in audit_all a
// row a problem: these name its rows the way rowKey does.
const (
	rowProvider = "audit_missing_metadata:provider_id"
	rowPoster   = "audit_missing_metadata:poster"
	rowOverview = "audit_missing_metadata:overview"
)

// rowKey names one of audit_all's rows: the audit, and for
// audit_missing_metadata the problem the row counts after a colon.
func rowKey(row map[string]any) string {
	if p := acc.Str(row["problems"]); p != "" {
		return acc.Str(row["audit"]) + ":" + p
	}

	return acc.Str(row["audit"])
}

// rowArgs are the arguments that ask an audit for the count one of
// audit_all's rows holds: the ones given, and what the row says it was
// counted over - its types (albums beside films and series, across the
// server), its places (audit_whitespace's, without the file names) and its
// problem (audit_missing_metadata's).
func rowArgs(row, args map[string]any) map[string]any {
	own := map[string]any{}
	maps.Copy(own, args)
	for _, field := range []string{"types", "where", "problems"} {
		if v := acc.Str(row[field]); v != "" {
			own[field] = v
		}
	}

	return own
}

// missing calls audit_missing_metadata for one of its problems, the way the
// three audits it replaced were called.
func missing(t *testing.T, problem string, args map[string]any) map[string]any {
	t.Helper()

	own := map[string]any{"problems": problem}
	maps.Copy(own, args)

	return suite.Call(t, "audit_missing_metadata", own)
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
		// every clean film and series is matched, has a plot and carries a
		// poster.jpg
		"audit_missing_metadata": {},
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
		"audit_missing_episodes":   {seriesOnly: true},
		"audit_whitespace":         {},
	} {
		for lib, n := range map[string]int{"Movies": want.movies, "Shows": want.shows} {
			out := suite.Call(t, audit, map[string]any{"library": lib})
			if got := acc.Num(t, out["total_findings"], "total_findings"); got != n {
				t.Errorf("%s on %s found %d, want %d: %v", audit, lib, got, n, out)
			}
			scanned := acc.Num(t, out["items_scanned"], "items_scanned")
			switch {
			case lib == "Movies" && want.seriesOnly && scanned != 0:
				t.Errorf("%s swept %d items of a film library", audit, scanned)
			case (lib == "Shows" || !want.seriesOnly) && scanned == 0:
				t.Errorf("%s on %s scanned nothing", audit, lib)
			}
		}
	}
	// every film and episode runs TMDB's length for it, a length a film or
	// an episode can have: the runtime audit names none of them
	for _, lib := range []string{"Movies", "Shows"} {
		if got, want := findingIDs(t, suite.Call(t, "audit_runtime", map[string]any{"library": lib, "limit": 1000})), brokenRuntimes(t, lib); len(got) != 0 || len(want) != 0 {
			t.Errorf("audit_runtime on %s named %v, and the server's runtimes say %v: want none", lib, got, want)
		}
	}
	// and the two the show library carries are the ones named
	if f := acc.Rows(t, suite.Call(t, "audit_file_path", map[string]any{"library": "Shows"})["findings"], "findings"); len(f) != 1 || acc.Str(f[0]["title_in_file"]) != "Dulcinea" {
		t.Errorf("the path finding in Shows = %v, want The Expanse's file named Dulcinea", f)
	}
	if g := acc.Rows(t, suite.Call(t, "audit_duplicate_episodes", map[string]any{"library": "Shows"})["groups"], "groups"); len(g) != 1 || acc.Str(g[0]["series"]) != "Breaking Bad" {
		t.Errorf("the repeated title in Shows = %v, want Breaking Bad's", g)
	}
	// nothing kept apart that the anime list folds into another show
	if out := suite.Call(t, "audit_anime_ids", map[string]any{"library": "Shows"}); acc.Num(t, out["items_scanned"], "items_scanned") != len(shows) ||
		acc.Num(t, out["total_ids_disagree"], "total_ids_disagree")+acc.Num(t, out["total_split_out"], "total_split_out")+acc.Num(t, out["total_kept_separate"], "total_kept_separate") != 0 {
		t.Errorf("audit_anime_ids on Shows = %v", out)
	}
}

// unmatchedShows are the messy series no nfo names: Star Trek The Next
// Generation has none, the A Knight of the Seven Kingdoms pair have none, and
// Andor's and Deep Space Nine's name no id. Both servers keep the double space
// in the second Knight folder's name. Sorted, as findings are.
var unmatchedShows = []string{"A Knight of the Seven  kingdoms", "A Knight of the Seven Kingdoms", "Andor", "Star Trek The Next Generation", "Star Trek: Deep Space Nine"}

func TestAuditMissingMetadataProvider(t *testing.T) {
	out := missing(t, "provider_id", map[string]any{"library": "Messy Movies"})
	if got := findings(t, out); !slices.Equal(got, messyUnmatched) {
		t.Errorf("unmatched movies = %v, want %v", got, messyUnmatched)
	}
	if got := acc.Num(t, out["items_scanned"], "items_scanned"); got != messyMovies() {
		t.Errorf("scanned %d, want %d", got, messyMovies())
	}
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		if acc.Str(f["id"]) == "" || !strings.HasPrefix(acc.Str(f["path"]), "/media/messy-movies/") || acc.Str(f["detail"]) != "no provider id" || !slices.Equal(acc.Strs(t, f["problems"], "problems"), []string{"provider_id"}) {
			t.Errorf("finding = %v", f)
		}
	}

	out = missing(t, "provider_id", map[string]any{"library": "Messy Shows", "types": "Series"})
	if got := findings(t, out); !slices.Equal(got, unmatchedShows) {
		t.Errorf("unmatched shows = %v, want %v", got, unmatchedShows)
	}

	// across every library the count is the sum
	everywhere := len(messyUnmatched) + len(unmatchedShows)
	out = missing(t, "provider_id", nil)
	if n := acc.Num(t, out["total_findings"], "total_findings"); n != everywhere {
		t.Errorf("unmatched everywhere = %d, want %d", n, everywhere)
	}
	// and a limit caps the worklist, not the count
	out = missing(t, "provider_id", map[string]any{"limit": 1})
	if n := len(acc.Rows(t, out["findings"], "findings")); n != 1 || acc.Num(t, out["total_findings"], "total_findings") != everywhere {
		t.Errorf("limit 1 = %d findings of %v", n, out["total_findings"])
	}

	// missing names the providers to look for. The messy films' sidecars
	// carry TMDB and IMDB ids and never a TVDB one, so every film lacks
	// TVDB, and each lists the ids it does have
	out = missing(t, "provider_id", map[string]any{"library": "Messy Movies", "missing": "tvdb"})
	if n := acc.Num(t, out["total_findings"], "total_findings"); n != messyMovies() {
		t.Errorf("missing tvdb = %d, want every messy film (%d): %v", n, messyMovies(), out["findings"])
	}
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		name := title(acc.Str(f["name"]))
		detail, unmatched := acc.Str(f["detail"]), slices.Contains(messyUnmatched, name)
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
	out = missing(t, "provider_id", map[string]any{"library": "Messy Movies", "missing": "tmdb"})
	if got, want := findings(t, out), acc.Sorted(append([]string{"Memento"}, messyUnmatched...)); !slices.Equal(got, want) {
		t.Errorf("missing tmdb = %v, want %v", got, want)
	}
	// two providers named is an item holding neither: Memento's IMDb id
	// takes it off the list, and the films matched nowhere stay
	out = missing(t, "provider_id", map[string]any{"library": "Messy Movies", "missing": "tmdb, IMDB"})
	if got := findings(t, out); !slices.Equal(got, messyUnmatched) {
		t.Errorf("missing tmdb and imdb = %v, want %v", got, messyUnmatched)
	}
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		if acc.Str(f["detail"]) != "no tmdb/imdb id" {
			t.Errorf("a film matched nowhere = %v", f)
		}
	}
	// the anime providers: only .hack//Liminality carries an AniDB id, and
	// nothing a MyAnimeList one, so every other series is listed with the
	// ids it does have, in the order they are named - the Asterix series the
	// film's two
	has := map[string]string{
		"Severance":                       "tmdb:95396 imdb:tt11280740 tvdb:371980",
		"The Wire":                        "tmdb:1438 imdb:tt0306414 tvdb:79126",
		"Red Dwarf":                       "tmdb:326 imdb:tt0094535 tvdb:71326",
		"Asterix & Obelix: The Big Fight": "tmdb:11625 imdb:tt0096842",
	}
	out = missing(t, "provider_id", map[string]any{"library": "Messy Shows", "missing": "anidb"})
	if got, want := findings(t, out), acc.Sorted(append([]string{"Severance", "The Wire", "The Wire", "Red Dwarf", "Asterix & Obelix: The Big Fight"}, unmatchedShows...)); !slices.Equal(got, want) {
		t.Errorf("missing anidb = %v, want %v", got, want)
	}
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		want := "no anidb id"
		if ids := has[acc.Str(f["name"])]; ids != "" {
			want += "; has " + ids
		}
		if acc.Str(f["detail"]) != want {
			t.Errorf("%v = %q, want %q", f["name"], f["detail"], want)
		}
	}
	out = missing(t, "provider_id", map[string]any{"library": "Messy Shows", "missing": "myanimelist"})
	if n := acc.Num(t, out["total_findings"], "total_findings"); n != messySeries {
		t.Errorf("missing myanimelist = %d, want every messy series (%d): %v", n, messySeries, out["findings"])
	}
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		if acc.Str(f["name"]) == ".hack//Liminality" && acc.Str(f["detail"]) != "no myanimelist id; has anidb:222" {
			t.Errorf(".hack//Liminality = %v", f)
		}
	}
	// the messy episodes' sidecars name no ids at all, so each is an episode
	// matched nowhere; the clean show library's were all matched
	out = missing(t, "provider_id", map[string]any{"library": "Messy Shows", "types": "Episode"})
	if n, scanned := acc.Num(t, out["total_findings"], "total_findings"), acc.Num(t, out["items_scanned"], "items_scanned"); n != messyEpisodes() || scanned != messyEpisodes() {
		t.Errorf("messy episodes matched nowhere = %d of %d, want all %d", n, scanned, messyEpisodes())
	}
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		if acc.Str(f["detail"]) != "no provider id" || !strings.HasPrefix(acc.Str(f["path"]), "/media/messy-shows/") {
			t.Errorf("episode finding = %v", f)
		}
	}
	if out := missing(t, "provider_id", map[string]any{"library": "Shows", "types": "Series,Episode"}); acc.Num(t, out["total_findings"], "total_findings") != 0 || acc.Num(t, out["items_scanned"], "items_scanned") != len(shows)+showEpisodes() {
		t.Errorf("the clean show library = %v, want its %d series and %d episodes all matched", out, len(shows), showEpisodes())
	}
	// and a misspelling is refused rather than flagging every item
	if msg := suite.CallErr(t, "audit_missing_metadata", map[string]any{"missing": "tmbd"}); !strings.Contains(msg, "tmdb, imdb, tvdb") {
		t.Errorf("a misspelled provider = %s", msg)
	}
	if msg := suite.CallErr(t, "audit_missing_metadata", map[string]any{"problems": "plot"}); !strings.Contains(msg, `unknown problem "plot"; choose from: provider_id, poster, overview`) {
		t.Errorf("a misspelled problem = %s", msg)
	}

	// ignore leaves a library out by its folder, before its items are
	// counted: without the messy show library its unmatched shows go, and
	// its series come off the count
	all := missing(t, "provider_id", nil)
	out = missing(t, "provider_id", map[string]any{"ignore": []any{"messy shows"}})
	if got := findings(t, out); !slices.Equal(got, messyUnmatched) {
		t.Errorf("ignoring Messy Shows = %v, want %v", got, messyUnmatched)
	}
	if got, want := acc.Num(t, out["items_scanned"], "items_scanned"), acc.Num(t, all["items_scanned"], "items_scanned")-messySeries; got != want {
		t.Errorf("ignoring Messy Shows scanned %d, want %d", got, want)
	}
	if msg := suite.CallErr(t, "audit_missing_metadata", map[string]any{"ignore": []any{"No Such Library"}}); !strings.Contains(msg, "no library named") {
		t.Errorf("an unknown library = %s", msg)
	}
}

// Asked for nothing in particular, audit_missing_metadata looks for all
// three problems at once: an item is one finding naming each it has, the
// count is of items, and by_problem counts each problem. The messy films
// matched nowhere have all three, Arrival a poster and a plot to find, and
// Moon a poster alone.
func TestAuditMissingMetadataProblems(t *testing.T) {
	out := suite.Call(t, "audit_missing_metadata", map[string]any{"library": "Messy Movies"})
	if got := findings(t, out); !slices.Equal(got, messyPosterless) || acc.Num(t, out["total_findings"], "total_findings") != len(messyPosterless) {
		t.Errorf("items missing anything = %v (%v), want %v", got, out["total_findings"], messyPosterless)
	}
	byProblem, _ := out["by_problem"].(map[string]any)
	for problem, want := range map[string]int{"provider_id": len(messyUnmatched), "poster": len(messyPosterless), "overview": len(messyBare)} {
		if acc.Num(t, byProblem[problem], problem) != want {
			t.Errorf("by_problem %s = %v, want %d", problem, byProblem[problem], want)
		}
	}
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		name, problems := title(acc.Str(f["name"])), acc.Strs(t, f["problems"], "problems")
		want := []string{"poster"}
		switch {
		case slices.Contains(messyUnmatched, name):
			want = []string{"provider_id", "poster", "overview"}
		case name == "Arrival":
			want = []string{"poster", "overview"}
		}
		if !slices.Equal(problems, want) {
			t.Errorf("%s has problems %v, want %v", name, problems, want)
		}
		if strings.Count(acc.Str(f["detail"]), ";")+1 != len(want) {
			t.Errorf("%s's detail %q does not say each of its %d problems", name, f["detail"], len(want))
		}
	}
	// two named, in either order, are the two in the audit's own order
	two := suite.Call(t, "audit_missing_metadata", map[string]any{"library": "Messy Movies", "problems": "overview, provider_id"})
	if got := findings(t, two); !slices.Equal(got, messyBare) {
		t.Errorf("items missing a plot or an id = %v, want %v", got, messyBare)
	}
	for _, f := range acc.Rows(t, two["findings"], "findings") {
		if problems := acc.Strs(t, f["problems"], "problems"); problems[len(problems)-1] != "overview" || len(problems) > 2 {
			t.Errorf("%v has problems %v, want provider_id then overview", f["name"], problems)
		}
	}
}

// The films with no plot and no poster: Arrival's nfo has ids and nothing
// else, and the three no nfo names at all.
var messyBare = acc.Sorted(append([]string{"Arrival"}, messyUnmatched...))

// The films with no poster: those, and Moon, the DVD kept whole, whose nfo
// has its plot and nothing beside it is a poster.
var messyPosterless = acc.Sorted(append([]string{"Moon"}, messyBare...))

func TestAuditMissingOverview(t *testing.T) {
	out := missing(t, "overview", map[string]any{"library": "Messy Movies"})
	if got := findings(t, out); !slices.Equal(got, messyBare) {
		t.Errorf("no overview = %v, want %v", got, messyBare)
	}
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		if acc.Str(f["detail"]) != "no overview" || acc.Str(f["id"]) == "" {
			t.Errorf("finding = %v", f)
		}
	}
	// the messy series with no tvshow.nfo to give them a plot
	out = missing(t, "overview", map[string]any{"library": "Messy Shows", "types": "Series"})
	if got, want := findings(t, out), []string{"A Knight of the Seven  kingdoms", "A Knight of the Seven Kingdoms", "Star Trek The Next Generation"}; !slices.Equal(got, want) {
		t.Errorf("series with no overview = %v, want %v", got, want)
	}
	// and the episodes with no nfo beside them: the Knight pair, named by
	// the server after their files, and on Emby the featurette it takes for
	// an episode. Every other messy episode's nfo has a plot
	want := []string{"A Knight of the Seven Kingdoms S01E01", "A Knight of the Seven Kingdoms S01E02"}
	if !isJellyfin() {
		want = append(want, "Featurette")
	}
	out = missing(t, "overview", map[string]any{"library": "Messy Shows", "types": "Episode"})
	if got := findings(t, out); !slices.Equal(got, want) || acc.Num(t, out["items_scanned"], "items_scanned") != messyEpisodes() {
		t.Errorf("episodes with no overview = %v of %v scanned, want %v of %d", got, out["items_scanned"], want, messyEpisodes())
	}

	// an overview of nothing but spaces is no overview: Jellyfin keeps one
	// as it was sent, Emby keeps none, and the audit reads both alike
	dune := findItem(t, "Messy Movies", "Movie", "Dune")
	plot := acc.Str(suite.Call(t, "item_get", map[string]any{"id": dune})["overview"])
	if plot == "" {
		t.Fatal("the messy Dune has no plot to take away")
	}
	t.Cleanup(func() { updateItem(t, dune, map[string]any{"Overview": plot}) })
	updateItem(t, dune, map[string]any{"Overview": "   "})
	if got := findings(t, missing(t, "overview", map[string]any{"library": "Messy Movies"})); !slices.Equal(got, acc.Sorted(append([]string{"Dune"}, messyBare...))) {
		t.Errorf("with Dune's plot all spaces = %v, want Dune beside %v", got, messyBare)
	}
}

func TestAuditMissingPoster(t *testing.T) {
	out := missing(t, "poster", map[string]any{"library": "Messy Movies"})
	if got := findings(t, out); !slices.Equal(got, messyPosterless) {
		t.Errorf("no poster = %v, want %v", got, messyPosterless)
	}
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		if acc.Str(f["detail"]) != "no primary image" {
			t.Errorf("finding = %v", f)
		}
	}
	// no messy series has a poster.jpg, and with the fetchers off nothing
	// gave it one
	out = missing(t, "poster", map[string]any{"library": "Messy Shows"})
	if got, want := findings(t, out), acc.Sorted(append([]string{".hack//Liminality", ".hack//SIGN", "Severance", "The Wire", "The Wire", "Red Dwarf", "Asterix & Obelix: The Big Fight"}, unmatchedShows...)); !slices.Equal(got, want) || len(want) != messySeries {
		t.Errorf("series with no poster = %v, want every one of the %d: %v", got, messySeries, want)
	}
	// nor any messy episode an image, where the clean show library's were
	// all given one
	out = missing(t, "poster", map[string]any{"library": "Messy Shows", "types": "Episode"})
	if n := acc.Num(t, out["total_findings"], "total_findings"); n != messyEpisodes() || acc.Num(t, out["items_scanned"], "items_scanned") != messyEpisodes() {
		t.Errorf("messy episodes with no image = %d of %v, want all %d", n, out["items_scanned"], messyEpisodes())
	}
	out = missing(t, "poster", map[string]any{"library": "Shows", "types": "Series,Episode"})
	if n := acc.Num(t, out["total_findings"], "total_findings"); n != 0 || acc.Num(t, out["items_scanned"], "items_scanned") != len(shows)+showEpisodes() {
		t.Errorf("the clean show library = %d missing of %v, want none of %d", n, out["items_scanned"], len(shows)+showEpisodes())
	}
}

// The messy Dune's folder says (2021) and its nfo 1984, and Stargate's file
// is held as Stargate: Continuum of 2008; every other messy film's folder
// names it as the server does, edition words after the year included
// ("Alien (1979) Directors Cut" is Alien). The loose DVD's file is
// VTS_01_1.VOB, which names no film: a disc's files are read by the folder
// above them, and that names the film the server holds.
func TestAuditFilePath(t *testing.T) {
	out := suite.Call(t, "audit_file_path", map[string]any{"library": "Messy Movies"})
	if got := findings(t, out); !slices.Equal(got, []string{"Dune", "Stargate: Continuum"}) {
		t.Fatalf("file path = %v, want [Dune Stargate: Continuum]", got)
	}
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		problems := acc.Strs(t, f["problems"], "problems")
		switch title(acc.Str(f["name"])) {
		case "Dune":
			if len(problems) != 1 || !strings.Contains(problems[0], "year: path says 2021, metadata says 1984") || acc.Str(f["type"]) != "Movie" {
				t.Errorf("Dune = %v", f)
			}
		case "Stargate: Continuum":
			if len(problems) != 2 || !strings.HasPrefix(problems[0], `title: the path is named "Stargate"`) || problems[1] != "year: path says 1994, metadata says 2008" {
				t.Errorf("Stargate = %v", f)
			}
		}
	}
	byCheck, _ := out["by_check"].(map[string]any)
	// Stargate's row fails both checks, and counts under each
	if acc.Num(t, byCheck["year"], "year") != 2 || acc.Num(t, byCheck["title"], "title") != 1 || len(byCheck) != 2 {
		t.Errorf("by_check = %v", byCheck)
	}
	// the year check alone, and the title check alone
	if got := findings(t, suite.Call(t, "audit_file_path", map[string]any{"library": "Messy Movies", "checks": "year"})); !slices.Equal(got, []string{"Dune", "Stargate: Continuum"}) {
		t.Errorf("checks=year = %v", got)
	}
	if got := findings(t, suite.Call(t, "audit_file_path", map[string]any{"library": "Messy Movies", "checks": "title"})); !slices.Equal(got, []string{"Stargate: Continuum"}) {
		t.Errorf("checks=title = %v", got)
	}
	// types narrows what is swept: the films alone are the whole library
	if n := acc.Num(t, suite.Call(t, "audit_file_path", map[string]any{"library": "Messy Movies", "types": "Series"})["items_scanned"], "items_scanned"); n != 0 {
		t.Errorf("types=Series swept %d items of a film library", n)
	}
	// a check it does not have is refused rather than running none
	if msg := suite.CallErr(t, "audit_file_path", map[string]any{"checks": "year,runtime"}); !strings.Contains(msg, `checks must be among series, season, episode, title, year, lookalike, not "runtime"`) {
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
	out := suite.Call(t, "audit_file_path", map[string]any{"library": "Messy Shows"})
	got := map[string][]string{}
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		if acc.Str(f["series"]) != "Andor" || acc.Str(f["type"]) != "Episode" {
			t.Errorf("a finding outside Andor: %v", f)
			continue
		}
		got[filepath.Base(acc.Str(f["path"]))] = acc.Strs(t, f["problems"], "problems")
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
	byCheck := acc.Object(t, out["by_check"], "by_check")
	episodes := 1
	if isJellyfin() {
		episodes = 2
	}
	if acc.Num(t, byCheck["series"], "series") != 1 || acc.Num(t, byCheck["season"], "season") != 1 || acc.Num(t, byCheck["episode"], "episode") != episodes {
		t.Errorf("by_check = %v", byCheck)
	}
	// one check at a time finds its own rows and no others
	for check, n := range map[string]int{"series": 1, "season": 1, "episode": episodes} {
		if got := acc.Num(t, suite.Call(t, "audit_file_path", map[string]any{"library": "Messy Shows", "checks": check})["total_findings"], "total_findings"); got != n {
			t.Errorf("checks=%s found %d, want %d", check, got, n)
		}
	}
	// the plainest first: a number or the series disagreeing, then titles,
	// then years. Across every library that is Andor's rows, then The
	// Expanse's file named for another episode and Stargate held as another
	// film, then the messy Dune's year, and a limit keeps the first of them
	all := suite.Call(t, "audit_file_path", nil)
	var order []string
	for _, f := range acc.Rows(t, all["findings"], "findings") {
		check, _, _ := strings.Cut(acc.Strs(t, f["problems"], "problems")[0], ":")
		order = append(order, check)
	}
	numbers := len(got)
	if len(order) != numbers+3 || !slices.Equal(order[numbers:], []string{"title", "title", "year"}) {
		t.Errorf("every library's rows lead with %v, want Andor's %d, then two titles, then a year", order, numbers)
	}
	for _, check := range order[:min(numbers, len(order))] {
		if check != "series" && check != "season" && check != "episode" {
			t.Errorf("rows lead with %v, want Andor's numbers and series first", order)
		}
	}
	capped := suite.Call(t, "audit_file_path", map[string]any{"limit": 1})
	found := acc.Rows(t, capped["findings"], "findings")
	if len(found) != 1 || acc.Num(t, capped["total_findings"], "total_findings") != numbers+3 {
		t.Fatalf("limit 1 = %v of %v, want one row of %d", found, capped["total_findings"], numbers+3)
	}
	if acc.Str(found[0]["series"]) != "Andor" || !slices.Contains([]string{"series", "season", "episode"}, strings.SplitN(acc.Strs(t, found[0]["problems"], "problems")[0], ":", 2)[0]) {
		t.Errorf("the row a limit of 1 keeps = %v, want one of Andor's numbers or its series", found[0])
	}
	// the episode held as a run is held whole: E03 is covered, not missing
	andor := findItem(t, "Messy Shows", "Series", "Andor")
	e03s := acc.Rows(t, suite.Call(t, "show_episodes_exist", map[string]any{"series_id": andor, "episodes": []map[string]any{{"season": 1, "episode": 3}}})["episodes"], "episodes")
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
	out := suite.Call(t, "audit_duplicates", map[string]any{"library": "Messy Movies"})
	groups, _ := out["groups"].([]any)
	if !isJellyfin() {
		if acc.Num(t, out["total_findings"], "total_findings") != 0 || len(groups) != 0 {
			t.Errorf("duplicates in the messy library on Emby = %v, want none", out)
		}
		// what Emby shows: the eleven folders' films, both Aliens as one
		// and both Blade Runner files as one
		if n := acc.Num(t, out["items_scanned"], "items_scanned"); n != messyMovies()-2 {
			t.Errorf("items_scanned = %d, want the %d films Emby shows", n, messyMovies()-2)
		}
	} else {
		if acc.Num(t, out["total_findings"], "total_findings") != 1 || len(groups) != 1 {
			t.Fatalf("duplicates = %v", out)
		}
		group := acc.Rows(t, groups[0], "group")
		var plain, cut bool
		for _, it := range group {
			if acc.Str(it["name"]) != "Alien" {
				t.Errorf("a duplicate of %v", it["name"])
			}
			switch {
			case strings.Contains(acc.Str(it["path"]), messyAlien+"/"):
				plain = true
			case strings.Contains(acc.Str(it["path"]), messyAlienCut+"/"):
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
	out = suite.Call(t, "audit_duplicates", map[string]any{"types": "Movie", "limit": 1})
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
	if n := acc.Num(t, out["total_findings"], "total_findings"); n != 3 {
		t.Errorf("total_findings = %d, want Alien, Arrival and Blade Runner", n)
	}
	// the clean libraries alone have none
	out = suite.Call(t, "audit_duplicates", map[string]any{"library": "Movies"})
	if acc.Num(t, out["total_findings"], "total_findings") != 0 {
		t.Errorf("Movies has duplicates: %v", out)
	}

	// across every kind, the series held twice, and an IMDb id joining the
	// film that holds Breaking Bad's to Breaking Bad: an IMDb id names one
	// title whatever its kind, where a TMDB or TVDB number is a film's in one
	// list and a series' in another, and only joins its own kind
	var got []string
	all, _ := suite.Call(t, "audit_duplicates", nil)["groups"].([]any)
	for _, g := range all {
		var members []string
		for _, it := range acc.Rows(t, g, "group") {
			members = append(members, acc.Str(it["type"])+" "+title(acc.Str(it["name"])))
		}
		got = append(got, strings.Join(acc.Sorted(members), " + "))
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
		// one show split by a folder rename
		"Series The Wire + Series The Wire",
	}
	if !slices.Equal(acc.Sorted(got), want) {
		t.Errorf("duplicates across every kind = %v, want %v", acc.Sorted(got), want)
	}
}

// On Emby the items an administrator's view leaves out are placed with the
// item they are versions of by the key Emby merges them by, rather than each
// read on its own: the view names no version, and only the single read of an
// item does, which on a library of 20,244 films held 901 in two files was 901
// reads. Every placing agrees with what those single reads say, film for film
// and episode for episode:
//
//   - a TMDB id shared across folders merges (the two Aliens);
//   - an IMDb id merges where neither film has a TMDB id (Memento, and a copy
//     holding its IMDb id alone);
//   - an IMDb id shared under a TMDB id of its own does not (The Thirteenth
//     Floor, holding Interstellar's IMDb id beside TMDB 1090);
//   - a second file in a film's folder merges, ids or none (Arrival, Dune and
//     Memento held twice, and the unmatched Princess Mononoke);
//   - a site shared merges nothing: Princess Mononoke's second file and the
//     unmatched もののけ姫 given one Facebook account are placed by their key,
//     not the account;
//   - an episode's number under two folders of one show merges (The Wire's
//     second episode), as does a second file of one (Severance's first);
//     two episodes of different numbers sharing a TVDB id do not.
//
// Which item of a group the view lists follows the order asked for, so groups
// are matched by their files, not their ids.
func TestEmbyVersionsArePlacedAsItsSingleReadsSay(t *testing.T) {
	if isJellyfin() {
		t.Skip("Jellyfin stores its versions: nothing is placed")
	}
	if testenv.DataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	client, err := embyfin.New(backend, os.Getenv("EMBYFIN_SERVER"), os.Getenv("EMBYFIN_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	movies, shows := filepath.Join(testenv.DataDir(), "messy-movies"), filepath.Join(testenv.DataDir(), "messy-shows")
	movieNfo := func(title string, year int, tmdb, imdb string) []byte {
		ids := ""
		if tmdb != "" {
			ids += fmt.Sprintf("  <tmdbid>%s</tmdbid>\n  <uniqueid type=\"tmdb\" default=\"true\">%s</uniqueid>\n", tmdb, tmdb)
		}
		if imdb != "" {
			ids += fmt.Sprintf("  <imdbid>%s</imdbid>\n  <uniqueid type=\"imdb\">%s</uniqueid>\n", imdb, imdb)
		}
		return fmt.Appendf(nil, "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<movie>\n  <title>%s</title>\n  <year>%d</year>\n%s  <lockdata>true</lockdata>\n</movie>\n", title, year, ids)
	}
	episodeWith := func(season, episode int, tvdb string) []byte {
		ids := ""
		if tvdb != "" {
			ids = fmt.Sprintf("  <uniqueid type=\"tvdb\" default=\"true\">%s</uniqueid>\n", tvdb)
		}
		return fmt.Appendf(nil, "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<episodedetails>\n  <season>%d</season>\n  <episode>%d</episode>\n%s  <lockdata>true</lockdata>\n</episodedetails>\n", season, episode, ids)
	}
	video := fixtureVideo(t, "messy-movies", messyArrival, messyArrival+".mp4")
	messyShows := acc.Str(suite.Call(t, "library_get", map[string]any{"library": "Messy Shows"})["id"])
	severance := len(itemsTitledEpisodes(t.Context(), t, client, messyShows, "Severance"))
	// the scans back to the fixtures run after the staged files go
	t.Cleanup(func() {
		for _, dir := range []string{"The Thirteenth Floor (1999)", "Memento (2000) Extended", "もののけ姫 (1997)"} {
			if err := os.RemoveAll(filepath.Join(movies, dir)); err != nil {
				t.Error(err)
			}
		}
		if err := scanUntil("Messy Movies", messyMovies()); err != nil {
			t.Error(err)
		}
		// the test's own context is over by now
		scanUntilTrue(t, "Messy Shows", func() bool {
			return len(itemsTitledEpisodes(context.Background(), t, client, messyShows, "Severance")) == severance
		})
	})
	for _, film := range []string{messyArrival, messyDune, messyCrossed, "Princess Mononoke (1997)"} {
		stageFile(t, filepath.Join(movies, film, film+" - 720p.mp4"), video)
	}
	for _, f := range []struct {
		dir, title string
		year       int
		tmdb, imdb string
	}{
		{"The Thirteenth Floor (1999)", "The Thirteenth Floor", 1999, "1090", "tt0816692"},
		{"Memento (2000) Extended", "Memento", 2000, "", "tt0903747"},
		{"もののけ姫 (1997)", "もののけ姫", 1997, "", ""},
	} {
		stageFile(t, filepath.Join(movies, f.dir, f.dir+".mp4"), video)
		stageFile(t, filepath.Join(movies, f.dir, "movie.nfo"), movieNfo(f.title, f.year, f.tmdb, f.imdb))
	}
	season := filepath.Join(shows, "Severance", "Season 01")
	stageFile(t, filepath.Join(season, "Severance S01E01 - 720p.mp4"), video)
	stageFile(t, filepath.Join(season, "Severance S01E01 - 720p.nfo"), episodeWith(1, 1, ""))
	for _, n := range []int{8, 9} {
		name := fmt.Sprintf("Severance S01E%02d", n)
		stageFile(t, filepath.Join(season, name+".mp4"), video)
		stageFile(t, filepath.Join(season, name+".nfo"), episodeWith(1, n, "99999901"))
	}
	if err := scanUntil("Messy Movies", messyMovies()+7); err != nil {
		t.Fatal(err)
	}
	scanUntilTrue(t, "Messy Shows", func() bool {
		return len(itemsTitledEpisodes(t.Context(), t, client, messyShows, "Severance")) == severance+3
	})

	// a Facebook account on Princess Mononoke's second file and on もののけ姫,
	// which a match on every provider id took for one film
	admin, err := client.ResolveUser(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	library := func(name string) string {
		return acc.Str(suite.Call(t, "library_get", map[string]any{"library": name})["id"])
	}
	stored, _, err := client.Search(t.Context(), embyfin.SearchOptions{ParentID: library("Messy Movies"), IncludeItemTypes: "Movie", Fields: "Path", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	shared := 0
	for _, it := range stored {
		if strings.Contains(it.Path, "Princess Mononoke (1997) - 720p") || strings.Contains(it.Path, "/もののけ姫 (1997)/") {
			if _, err := client.EditItem(t.Context(), admin.ID, it.ID, func(full map[string]any) (bool, error) {
				full["ProviderIds"] = map[string]any{"Facebook": "zzyzx"}
				full["LockData"] = true
				return true, nil
			}); err != nil {
				t.Fatal(err)
			}
			shared++
		}
	}
	if shared != 2 {
		t.Fatalf("gave %d items the Facebook account, want 2", shared)
	}

	for _, c := range []struct {
		library, types string
		groups         int
	}{
		// Alien, Blade Runner, Arrival, Dune, Memento (with its copy and
		// its second file) and Princess Mononoke
		{"Messy Movies", "Movie", 6},
		// The Wire's second episode and Severance's first
		{"Messy Shows", "Episode", 2},
	} {
		listed, _, err := client.Search(t.Context(), embyfin.SearchOptions{ParentID: library(c.library), IncludeItemTypes: c.types, Fields: "Path", UserID: admin.ID, Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, it := range listed {
			single, err := client.UserItem(t.Context(), admin.ID, it.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(single.MediaSources) < 2 {
				continue
			}
			var files []string
			for _, src := range single.MediaSources {
				files = append(files, filepath.Base(src.Path))
			}
			slices.Sort(files)
			want = append(want, strings.Join(files, " + "))
		}
		slices.Sort(want)
		if len(want) != c.groups {
			t.Fatalf("%s: %d read as held in several files, want %d: %v", c.library, len(want), c.groups, want)
		}

		out := suite.Call(t, "audit_multiple_versions", map[string]any{"library": c.library, "types": c.types})
		found := acc.Rows(t, out["findings"], "findings")
		got := make([]string, 0, len(found))
		for _, f := range found {
			_, list, _ := strings.Cut(acc.Str(f["detail"]), ": ")
			files := strings.Split(list, ", ")
			slices.Sort(files)
			got = append(got, strings.Join(files, " + "))
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: in versions by the audit: %v; by their own reads: %v", c.library, got, want)
		}
		// placed by the key, and the sample of those placings agreeing
		if note := acc.Str(out["note"]); !strings.Contains(note, "were placed by the key Emby merges versions by") || !strings.Contains(note, "all agreeing") {
			t.Errorf("%s: note = %q, want the placings by key, checked", c.library, note)
		}
	}
}

// itemsTitledEpisodes are the paths of a series' episode files in a library.
func itemsTitledEpisodes(ctx context.Context, t *testing.T, client *embyfin.Client, library, series string) []string {
	t.Helper()

	items, _, err := client.Search(ctx, embyfin.SearchOptions{ParentID: library, IncludeItemTypes: "Episode", Fields: "Path", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, it := range items {
		if strings.Contains(it.Path, "/"+series+"/") {
			paths = append(paths, it.Path)
		}
	}

	return paths
}

// Emby also shows the two messy Aliens, in two folders and sharing a TMDB id,
// as one. Jellyfin stores its merge (a sweep holds one Blade Runner with two
// files); Emby stores each file as an item and merges them only in what it
// shows people, which is what the audit reads there.
func TestAuditMultipleVersions(t *testing.T) {
	out := suite.Call(t, "audit_multiple_versions", map[string]any{"library": "Messy Movies"})
	want := []string{"Blade Runner"}
	shown := messyMovies()
	if !isJellyfin() {
		want, shown = []string{"Alien", "Blade Runner"}, messyMovies()-2
	}
	if got := acc.Num(t, out["items_scanned"], "items_scanned"); got != shown {
		t.Errorf("scanned %d, want the %d films the server shows", got, shown)
	}
	if got := findings(t, out); !slices.Equal(got, want) {
		t.Errorf("multiple versions = %v, want %v", got, want)
	}
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		d := acc.Str(f["detail"])
		switch title(acc.Str(f["name"])) {
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
	if got := suite.Call(t, "audit_multiple_versions", map[string]any{"library": "Messy Movies", "types": "Movie", "limit": 1}); len(acc.Rows(t, got["findings"], "findings")) != 1 || acc.Num(t, got["total_findings"], "total_findings") != len(want) {
		t.Errorf("types Movie limit 1 = %v", got)
	}
	// and episodes alone find none: no episode is held in two versions
	if n := acc.Num(t, suite.Call(t, "audit_multiple_versions", map[string]any{"library": "Messy Movies", "types": "Episode"})["items_scanned"], "items_scanned"); n != 0 {
		t.Errorf("types Episode swept %d items of a film library", n)
	}
}

// audit_runtime names only a length no film or episode can have: under two
// minutes, or twelve hours or more. The fixtures run seconds, so it names most
// of them, which is what they are; .hack//Liminality's three-minute episodes
// are the ones it has to leave alone, and Deep Space Nine's season 3 file,
// claiming twelve hours for a second of video, comes first as the broken
// duration it is. The expected findings are worked out from the server's own
// runtimes (brokenRuntimes), not from a count written down.
func TestAuditRuntime(t *testing.T) {
	out := suite.Call(t, "audit_runtime", map[string]any{"library": "Messy Shows", "limit": 1000})
	if got := acc.Num(t, out["items_scanned"], "items_scanned"); got != messyEpisodeFiles {
		t.Errorf("scanned %d episodes, want the %d episode files, the featurette Emby takes for one left out", got, messyEpisodeFiles)
	}
	if got, want := findingIDs(t, out), brokenRuntimes(t, "Messy Shows"); !slices.Equal(got, want) || acc.Num(t, out["total_findings"], "total_findings") != len(want) {
		t.Errorf("findings %v (total %v), want %v", got, out["total_findings"], want)
	}
	found := acc.Rows(t, out["findings"], "findings")
	if len(found) == 0 {
		t.Fatal("no findings")
	}
	if f := found[0]; acc.Str(f["name"]) != "Star Trek: Deep Space Nine S03E01 The Search (1)" || acc.Str(f["detail"]) != "12 h 0 min: not a runtime, the file's duration metadata is broken" || !strings.HasSuffix(acc.Str(f["path"]), "/Season 03/Star Trek Deep Space Nine S03E01.mkv") {
		t.Errorf("the broken duration = %v", f)
	}
	details := map[string]string{}
	for _, f := range found {
		details[acc.Str(f["name"])] = acc.Str(f["detail"])
	}
	if d := details[".hack//Liminality S01E03 In the Case of Kyoko Tohno"]; d != "1 s: too short to be the episode, an incomplete, sample or broken file" {
		t.Errorf("the one-second episode = %q", d)
	}
	for _, name := range []string{".hack//Liminality S01E01 In the Case of Mai Minase", ".hack//Liminality S01E02 In the Case of Yuki Aihara"} {
		if d, ok := details[name]; ok {
			t.Errorf("a three-minute episode was named: %s: %s", name, d)
		}
	}
	// the broken file's own row says what the file claims: twelve hours
	ds9 := findItem(t, "Messy Shows", "Series", "Star Trek: Deep Space Nine")
	for _, e := range acc.Rows(t, suite.Call(t, "library_episodes", map[string]any{"series_id": ds9, "season": 3})["episodes"], "episodes") {
		if acc.Num(t, e["runtime_s"], "runtime_s") != 43200 {
			t.Errorf("the broken file's row = %v runtime_s, want 43200", e["runtime_s"])
		}
	}
	// and the films, judged the same way
	if got, want := findingIDs(t, suite.Call(t, "audit_runtime", map[string]any{"library": "Messy Movies", "limit": 1000})), brokenRuntimes(t, "Messy Movies"); !slices.Equal(got, want) {
		t.Errorf("Messy Movies: named %v, want %v", got, want)
	}
}

// brokenRuntimes is what audit_runtime should name in a library, sorted by
// id: every film and episode with a file whose runtime the server holds is
// under two minutes or twelve hours or more, the extras a server takes for
// episodes left out. It is read from the server's own runtimes, item by item:
// on Emby every version of a film is an item of its own, judged on its own,
// where Jellyfin folds a second file into one item, judged once.
func brokenRuntimes(t *testing.T, library string) []string {
	t.Helper()

	extras := []string{"extras", "trailers", "featurettes", "behind the scenes", "deleted scenes", "interviews", "scenes", "samples", "shorts", "clips", "other", "backdrops"}
	var ids []string
	for _, it := range acc.Rows(t, suite.Call(t, "library_items", map[string]any{"library": library, "types": "Movie,Episode", "limit": 1000})["items"], "items") {
		files := []map[string]any{it}
		if versions := acc.RowsOf(it["versions"]); !isJellyfin() && len(versions) > 0 {
			files = versions
		}
		for _, file := range files {
			path, seconds := acc.Str(file["path"]), acc.NumOr0(file["runtime_s"])
			if path == "" || seconds <= 0 || acc.Str(it["type"]) == "Episode" && slices.Contains(extras, strings.ToLower(filepath.Base(filepath.Dir(path)))) {
				continue
			}
			if seconds < 2*60 || seconds >= 12*60*60 {
				id := acc.Str(file["id"])
				if id == "" {
					id = acc.Str(it["id"])
				}
				ids = append(ids, id)
			}
		}
	}

	return acc.Sorted(ids)
}

// findingIDs is the ids an audit named, sorted.
func findingIDs(t *testing.T, out map[string]any) []string {
	t.Helper()

	var ids []string
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		ids = append(ids, acc.Str(f["id"]))
	}

	return acc.Sorted(ids)
}

// Movie runtimes come from TMDB, through the provider proxy. The messy
// films run TMDB's lengths but two: Arrival, cut short at 40 minutes of
// TMDB's 116, a copy no length a film cannot have gives away, which only a
// provider's fact for the film can; and Stargate's 121 minutes, held as
// Stargate: Continuum, which TMDB says runs 98.
func TestAuditProviderRuntime(t *testing.T) {
	needsTMDBCassette(t)
	out := suite.Call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "runtime"})
	got := findings(t, out)
	// the films passed over are passed over for what they hold: Princess
	// Mononoke and the loose and Blu-ray discs no id at all, Memento an IMDb
	// id and no TMDB one to read a runtime by, and the Despecialized Edition
	// a TMDB id TMDB has no film for. The DVD kept whole runs as long as the
	// server read it: a second on Jellyfin, which reads the disc's one-second
	// title, and not at all on Emby, which does not, so there it has no
	// runtime to hold to TMDB's
	want := []string{"Arrival", "Stargate: Continuum"}
	if isJellyfin() {
		want = []string{"Arrival", "Moon", "Stargate: Continuum"}
	}
	if !slices.Equal(got, want) {
		t.Errorf("runtime off = %v, want %v", got, want)
	}
	if got := acc.Num(t, out["items_scanned"], "items_scanned"); got != messyMovies() {
		t.Errorf("scanned %d, want %d", got, messyMovies())
	}
	for _, f := range acc.Rows(t, out["findings"], "findings") {
		if title(acc.Str(f["name"])) == "Arrival" && !slices.Equal(acc.Strs(t, f["problems"], "problems"), []string{"runtime: file 40 min, TMDB says 116 min (65% off)"}) {
			t.Errorf("Arrival's problems = %v", f["problems"])
		}
		if title(acc.Str(f["name"])) == "Stargate: Continuum" && !slices.Equal(acc.Strs(t, f["problems"], "problems"), []string{"runtime: file 121 min, TMDB says 98 min (23% off)"}) {
			t.Errorf("Stargate's problems = %v", f["problems"])
		}
	}

	// paging: one lookup per call, continued from next_offset, finds what
	// the whole sweep found
	var paged []string
	for offset, calls := any(nil), 0; ; calls++ {
		if calls > messyMovies() {
			t.Fatalf("still paging after %d calls", calls)
		}
		args := map[string]any{"library": "Messy Movies", "checks": "runtime", "max_lookups": 1}
		if offset != nil {
			args["offset"] = offset
		}
		page := suite.Call(t, "audit_provider", args)
		paged = append(paged, findings(t, page)...)
		next, ok := page["next_offset"]
		if !ok {
			break
		}
		offset = next
	}
	if slices.Sort(paged); !slices.Equal(paged, want) {
		t.Errorf("one lookup a call found %v, want %v", paged, want)
	}
	// tolerance: Arrival is 65% off, which a tolerance of 65 lets by and one
	// of 64 does not
	tolerant := func(pct int) []string {
		return findings(t, suite.Call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "runtime", "tolerance_percent": pct}))
	}
	if got := tolerant(65); slices.Contains(got, "Arrival") {
		t.Errorf("tolerance 65 found %v, want Arrival let by", got)
	}
	if got := tolerant(64); !slices.Contains(got, "Arrival") {
		t.Errorf("tolerance 64 found %v, want Arrival", got)
	}
	// only TMDB yet, said plainly
	if msg := suite.CallErr(t, "audit_provider", map[string]any{"provider": "tvdb"}); !strings.Contains(msg, "provider must be tmdb") {
		t.Errorf("another provider: %s", msg)
	}
}

// Every clean film runs TMDB's length for it, a still frame joined end to
// end so a two-hour film costs a few megabytes: Limitless's 106 minutes read
// back to the second, and the runtime check leaves every film alone.
func TestAFilmRunningItsRealLength(t *testing.T) {
	needsTMDBCassette(t)
	limitless := findItem(t, "Movies", "Movie", "Limitless")
	if got := suite.Call(t, "item_get", map[string]any{"id": limitless}); acc.Num(t, got["runtime_s"], "runtime_s") != 106*60 || acc.Num(t, got["height"], "height") != 720 {
		t.Errorf("Limitless = %vs at %vp, want its 106 minutes at 720p", got["runtime_s"], got["height"])
	}
	out := suite.Call(t, "audit_provider", map[string]any{"library": "Movies", "checks": "runtime"})
	if got := findings(t, out); len(got) != 0 || acc.Num(t, out["items_scanned"], "items_scanned") != len(movies) || acc.Num(t, out["runtime_not_judged"], "runtime_not_judged") != 0 {
		t.Errorf("runtime off in Movies = %v of %v (not judged %v), want every film judged and none off", got, out["items_scanned"], out["runtime_not_judged"])
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

	if testenv.Recording() {
		if os.Getenv("EMBYFIN_TMDB_TOKEN") == "" && os.Getenv("EMBYFIN_TMDB_KEY") == "" {
			t.Skip("recording the TMDB lookups needs EMBYFIN_TMDB_TOKEN")
		}
		return
	}
	raw, err := os.ReadFile(filepath.Join(testenv.CassetteDir(string(backend)), "api.themoviedb.org.json"))
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
	out := suite.Call(t, "audit_all", map[string]any{"library": "Messy Movies"})
	counts := map[string]int{}
	for _, row := range acc.Rows(t, out["audits"], "audits") {
		counts[rowKey(row)] = acc.Num(t, row["findings"], "findings")
	}
	want := map[string]int{
		rowProvider: len(messyUnmatched),
		rowPoster:   len(messyPosterless),
		rowOverview: len(messyBare),
		// Dune's year, and Stargate held as Stargate: Continuum
		"audit_file_path": 2,
		// Blade Runner's two files; on Emby the two Aliens too (below)
		"audit_multiple_versions":  1,
		"audit_duplicates":         1,
		"audit_duplicate_episodes": 0,
		"audit_disc_folders":       1,
		"audit_runtime":            len(brokenRuntimes(t, "Messy Movies")),
		// every film but the Blade Runner cuts and the discs kept whole,
		// which Emby never probes and so does not judge; Jellyfin reads the
		// DVD, and it is a 480p MPEG-2 one
		"audit_quality":          11,
		"audit_missing_episodes": 0,
		// the Despecialized Edition's Science-Fiction
		"audit_spelling":   1,
		"audit_whitespace": 0,
	}
	if !isJellyfin() {
		// Emby shows the two messy Aliens, sharing a TMDB id, as one film's
		// versions as well, which leaves no duplicates in the library
		want["audit_multiple_versions"], want["audit_duplicates"] = 2, 0
		want["audit_quality"] = 9 // and judges them as one film, and the DVD not at all
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
		for _, row := range acc.Rows(t, out["audits"], "audits") {
			if s, _ := row["skipped"].(bool); s {
				names = append(names, rowKey(row))
				if acc.Str(row["note"]) == "" {
					t.Errorf("%s is skipped without a note", row["audit"])
				}
			}
		}
		slices.Sort(names)

		return names
	}
	if got, want := skippedIn(out), []string{"audit_anime_ids", "audit_language", "audit_orphans", "audit_previews", "audit_provider"}; !slices.Equal(got, want) {
		t.Errorf("skipped for one library = %v, want %v", got, want)
	}
	// nothing failed, and every row that ran says how long it took
	if out["failed"] != nil {
		t.Errorf("failed = %v", out["failed"])
	}
	for _, row := range acc.Rows(t, out["audits"], "audits") {
		if _, timed := row["took_s"].(float64); timed == (row["skipped"] == true) || row["failed"] != nil {
			t.Errorf("row %v: a row that ran is timed, a skipped one is not, and none failed", row)
		}
	}
	if len(counts) != len(want)+6 {
		t.Errorf("audits = %v, want %d rows", counts, len(want)+6)
	}
	// the total is the defects: what nobody has watched is a row, not a fault
	total := 0
	for _, n := range want {
		total += n
	}
	if acc.Num(t, out["total_findings"], "total_findings") != total {
		t.Errorf("total_findings = %v, want %d", out["total_findings"], total)
	}
	// across the server, the orphans check runs and the four it does not
	// run itself are all that is skipped: the three that need more than the
	// server, and the one that asks it about every video
	whole := suite.Call(t, "audit_all", nil)
	if got, want := skippedIn(whole), []string{"audit_anime_ids", "audit_language", "audit_previews", "audit_provider"}; !slices.Equal(got, want) {
		t.Errorf("skipped across the server = %v, want %v", got, want)
	}

	// the clean libraries are clean, but for the show library's file named
	// for another episode and its title held twice, and the runtimes of the
	// fixtures, seconds long, which no film or episode runs
	// (TestAuditsLeaveTheCleanLibrariesAlone)
	clean := suite.Call(t, "audit_all", map[string]any{"library": "Movies"})
	if n, want := acc.Num(t, clean["total_findings"], "total_findings"), len(brokenRuntimes(t, "Movies")); n != want {
		t.Errorf("Movies total_findings = %d, want the %d runtimes: %v", n, want, clean["audits"])
	}
	if n, want := acc.Num(t, suite.Call(t, "audit_all", map[string]any{"library": "Shows"})["total_findings"], "total_findings"), 2+len(brokenRuntimes(t, "Shows")); n != want {
		t.Errorf("Shows total_findings = %d, want %d: The Expanse's path, Breaking Bad's repeated title and the runtimes", n, want)
	}
	// and each clean row is the audit's own count, which TestAuditAllMatchesEachAudit
	// checks for the other libraries
	for _, row := range acc.Rows(t, clean["audits"], "audits") {
		name := rowKey(row)
		if s, _ := row["skipped"].(bool); s {
			continue
		}
		own := suite.Call(t, acc.Str(row["audit"]), rowArgs(row, map[string]any{"library": "Movies"}))
		if acc.Num(t, own["total_findings"], name) != acc.Num(t, row["findings"], "findings") || acc.Num(t, own["items_scanned"], name) != acc.Num(t, row["items_scanned"], "items_scanned") {
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
		covers = musicAlbums()
	}
	music := suite.Call(t, "audit_all", map[string]any{"library": "Music"})
	ran := map[string]bool{}
	for _, row := range acc.Rows(t, music["audits"], "audits") {
		name := rowKey(row)
		if s, _ := row["skipped"].(bool); s {
			if acc.Str(row["note"]) == "" {
				t.Errorf("Music %s is skipped without a note", name)
			}
			continue
		}
		ran[name] = true
		// the spaces in the artists', albums' and tracks' names and folders,
		// of which there are none out of place
		if name == "audit_whitespace" {
			own := suite.Call(t, name, map[string]any{"library": "Music"})
			if items := artists() + musicAlbums() + songs(); acc.Num(t, row["findings"], "findings") != 0 || acc.Num(t, row["items_scanned"], "items_scanned") != items ||
				acc.Num(t, own["total_findings"], name) != 0 || acc.Num(t, own["items_scanned"], name) != items {
				t.Errorf("Music %s = %v and on its own %v, want the %d artists, albums and songs read and nothing found", name, row, own, items)
			}

			continue
		}
		// the spellings: the genre, and in the tracks' tags an album and an
		// artist each written two ways
		want, applies := map[string]int{rowPoster: covers, "audit_spelling": 3}[name]
		switch {
		case !applies:
			t.Errorf("Music %s ran: %v", name, row)
		case acc.Num(t, row["findings"], "findings") != want || acc.Num(t, row["items_scanned"], "items_scanned") != musicAlbums() || acc.Str(row["types"]) != "MusicAlbum":
			t.Errorf("Music %s = %v, want %d of the %d albums, read as MusicAlbum", name, row, want, musicAlbums())
		}
		// and it is the audit's own count, asked for the albums
		own := suite.Call(t, acc.Str(row["audit"]), rowArgs(row, map[string]any{"library": "Music"}))
		if acc.Num(t, own["total_findings"], name) != acc.Num(t, row["findings"], "findings") || acc.Num(t, own["items_scanned"], name) != acc.Num(t, row["items_scanned"], "items_scanned") {
			t.Errorf("Music %s: audit_all counted %v of %v, the audit %v of %v", name, row["findings"], row["items_scanned"], own["total_findings"], own["items_scanned"])
		}
	}
	if !ran[rowPoster] || !ran["audit_spelling"] || !ran["audit_whitespace"] || len(ran) != 3 {
		t.Errorf("over Music audit_all ran %v, want the covers, the spellings and the spaces", ran)
	}
	if acc.Num(t, music["total_findings"], "total_findings") != covers+3 {
		t.Errorf("Music total_findings = %v, want the %d albums with no cover and the three spellings", music["total_findings"], covers)
	}
	// and across the server they count the albums beside the films and series
	for _, row := range acc.Rows(t, whole["audits"], "audits") {
		switch rowKey(row) {
		case rowPoster, "audit_spelling":
			if !strings.HasSuffix(acc.Str(row["types"]), ",MusicAlbum") {
				t.Errorf("across the server %v, want the albums read too", row)
			}
		}
	}
	// and the messy shows' defects are each counted
	shows := map[string]int{}
	for _, row := range acc.Rows(t, suite.Call(t, "audit_all", map[string]any{"library": "Messy Shows"})["audits"], "audits") {
		shows[rowKey(row)] = acc.Num(t, row["findings"], "findings")
	}
	pathRows := 3 // Andor's series, episode and season rows
	if isJellyfin() {
		pathRows = 4 // and the run Jellyfin reads from the nfo
	}
	// The Wire's second episode, one copy in each of its folders, is one
	// episode's two versions to Emby, which shows them so
	twice := 0
	if !isJellyfin() {
		twice = 1
	}
	for audit, n := range map[string]int{
		rowProvider:                len(unmatchedShows),
		rowPoster:                  messySeries,
		rowOverview:                3,
		"audit_file_path":          pathRows,
		"audit_multiple_versions":  twice,
		"audit_duplicates":         2, // The Wire's two entries sharing ids, and the Knight pair's folders
		"audit_duplicate_episodes": 0,
		"audit_disc_folders":       0,
		"audit_runtime":            len(brokenRuntimes(t, "Messy Shows")),
		"audit_quality":            messyEpisodesJudged() - 1,
		"audit_missing_episodes":   3, // Andor, Deep Space Nine and The Next Generation
		"audit_spelling":           1,
		"audit_whitespace":         2, // the Knight pair's renamed folder, and the show named from it
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
	for _, name := range suite.ToolNames(t) {
		if strings.HasPrefix(name, "audit_") {
			got = append(got, name)
		}
	}
	want := []string{
		"audit_all", "audit_anime_ids", "audit_disc_folders", "audit_duplicate_episodes", "audit_duplicates", "audit_file_path", "audit_language",
		"audit_missing_episodes", "audit_missing_metadata", "audit_multiple_versions", "audit_orphans", "audit_previews", "audit_provider", "audit_quality", "audit_runtime", "audit_spelling",
		"audit_unwatched", "audit_whitespace",
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("audit tools = %v, want %v", got, want)
	}
}

// audit_language against files whose streams the servers probed: the messy
// Princess Mononoke carries Japanese audio and then English, the messy
// Despecialized Edition German alone, tagged ger, The Thirteenth Floor an
// English subtitle beside it, and every other fixture an audio track with no
// language tag - which is what most real rips carry too.
func TestAuditLanguage(t *testing.T) {
	names := func(out map[string]any) []string {
		var got []string
		for _, f := range acc.Rows(t, out["findings"], "findings") {
			got = append(got, title(acc.Str(f["name"])))
		}

		return got
	}
	// the films the servers hold no audio track for: the Blu-ray kept whole,
	// which neither probes, and on Emby the DVD kept whole, which it does not
	// probe either
	noTrack := 2
	if isJellyfin() {
		noTrack = 1
	}

	// any track counts: Princess Mononoke's English is its second, and it has
	// Japanese audio and English audio alike
	for _, language := range []string{"ja", "eng"} {
		out := suite.Call(t, "audit_language", map[string]any{"language": language, "library": "Messy Movies"})
		f := acc.Rows(t, out["findings"], "findings")
		if got := names(out); !slices.Equal(got, []string{"Princess Mononoke"}) || len(f) != 1 || acc.Str(f[0]["detail"]) != "audio: jpn, eng; subtitles: none" {
			t.Errorf("%s audio = %v, want Princess Mononoke's two tracks", language, f)
		}
	}
	// German is ger in one file and deu in the next, and the audit reads the
	// two as one language whichever is asked for
	for _, language := range []string{"deu", "ger", "de"} {
		out := suite.Call(t, "audit_language", map[string]any{"language": language, "library": "Messy Movies"})
		f := acc.Rows(t, out["findings"], "findings")
		if len(f) != 1 || title(acc.Str(f[0]["name"])) != despecialized || acc.Str(f[0]["detail"]) != "audio: deu; subtitles: none" {
			t.Errorf("%s audio = %v, want the Despecialized Edition's German track", language, f)
		}
	}
	// and that film is the one the messy library cannot be watched in, or
	// heard, in English: its one track is tagged, and tagged something else.
	// Princess Mononoke's English is no longer "no audio" for being second;
	// the rest carry untagged audio, which may be English, and the discs kept
	// whole no audio track the server knows of
	for _, find := range []string{"no_audio", "unwatchable"} {
		out := suite.Call(t, "audit_language", map[string]any{"language": "eng", "find": find, "library": "Messy Movies"})
		scanned := acc.Num(t, out["items_scanned"], "items_scanned")
		f := acc.Rows(t, out["findings"], "findings")
		if len(f) != 1 || title(acc.Str(f[0]["name"])) != despecialized || acc.Str(f[0]["detail"]) != "audio: deu; subtitles: none" || acc.Num(t, out["total_findings"], "total_findings") != 1 {
			t.Errorf("%s in English = %v, want the Despecialized Edition alone", find, f)
		}
		// the films as people are shown them: Emby's versions judged together
		if scanned != messyMoviesShown() || acc.Num(t, out["untagged"], "untagged") != scanned-2-noTrack || acc.Num(t, out["no_audio_track"], "no_audio_track") != noTrack {
			t.Errorf("%s in English: untagged %v and no_audio_track %v of %d scanned, want all but the two tagged and the %d with none, and %d", find, out["untagged"], out["no_audio_track"], scanned, noTrack, noTrack)
		}
	}
	// episodes are swept too, and every messy one is untagged: nothing to
	// report, and each counted
	out := suite.Call(t, "audit_language", map[string]any{"language": "eng", "find": "unwatchable", "library": "Messy Shows"})
	if acc.Num(t, out["total_findings"], "total_findings") != 0 || acc.Num(t, out["items_scanned"], "items_scanned") != messyEpisodesShown() || acc.Num(t, out["untagged"], "untagged") != messyEpisodesShown() {
		t.Errorf("the messy episodes = %v, want all %d scanned and untagged", out, messyEpisodesShown())
	}
	// a series is not a file: asked for series alone, there is nothing to read
	if out := suite.Call(t, "audit_language", map[string]any{"language": "eng", "library": "Messy Shows", "types": "Series"}); acc.Num(t, out["total_findings"], "total_findings")+acc.Num(t, out["untagged"], "untagged")+acc.Num(t, out["no_audio_track"], "no_audio_track") != 0 {
		t.Errorf("types=Series = %v, want nothing judged", out)
	}

	// lacking Japanese: the German film, whose one track is tagged and not
	// Japanese; the rest carry untagged audio, which may be Japanese, and the
	// discs kept whole none the server knows of, each counted apart rather
	// than judged
	lacking := suite.Call(t, "audit_language", map[string]any{"language": "jpn", "find": "no_audio", "library": "Messy Movies"})
	scanned := acc.Num(t, lacking["items_scanned"], "items_scanned")
	if got := names(lacking); !slices.Equal(got, []string{despecialized}) || scanned != messyMoviesShown() {
		t.Errorf("of %d scanned, lacking japanese: %v, want the Despecialized Edition", scanned, got)
	}
	if n, none := acc.Num(t, lacking["untagged"], "untagged"), acc.Num(t, lacking["no_audio_track"], "no_audio_track"); none != noTrack || n != scanned-2-noTrack {
		t.Errorf("untagged = %d and no_audio_track %d of %d scanned, want all but the two tagged and the %d with no track, and those", n, none, scanned, noTrack)
	}

	// the subtitle is read off the file beside the film, language from its name
	subtitled := suite.Call(t, "audit_language", map[string]any{"language": "eng", "find": "subtitles", "library": "Movies"})
	if got := names(subtitled); !slices.Equal(got, []string{"The Thirteenth Floor"}) {
		t.Errorf("english subtitles = %v, want [The Thirteenth Floor]", got)
	}
	// the films alone are the whole library (a limit is read where there
	// are two to cap, TestAuditLanguageStaged)
	if films := suite.Call(t, "audit_language", map[string]any{"language": "eng", "find": "subtitles", "library": "Movies", "types": "Movie"}); !slices.Equal(names(films), []string{"The Thirteenth Floor"}) || acc.Num(t, films["items_scanned"], "items_scanned") != len(movies) {
		t.Errorf("types Movie = %v of %v", names(films), films["items_scanned"])
	}

	// and it is what makes that film watchable in English when nothing else
	// in the library can be judged
	unwatchable := suite.Call(t, "audit_language", map[string]any{"language": "eng", "find": "unwatchable", "library": "Movies"})
	scanned = acc.Num(t, unwatchable["items_scanned"], "items_scanned")
	if n := acc.Num(t, unwatchable["total_findings"], "total_findings"); n != 0 {
		t.Errorf("unwatchable in english = %v", unwatchable["findings"])
	}
	if n := acc.Num(t, unwatchable["untagged"], "untagged"); n != scanned-1 {
		t.Errorf("untagged = %d of %d scanned, want all but The Thirteenth Floor", n, scanned)
	}

	if msg := suite.CallErr(t, "audit_language", map[string]any{"language": "eng", "find": "sideways"}); !strings.Contains(msg, "find must be") {
		t.Errorf("a bad find: %s", msg)
	}
	// no language, or the tag that means none, is refused rather than
	// answered: every item would lack it
	for language, want := range map[string]string{"": "language is required", " ": "language is required", "und": "und means no language", "UND": "und means no language"} {
		if msg := suite.CallErr(t, "audit_language", map[string]any{"language": language}); !strings.Contains(msg, want) {
			t.Errorf("language %q: %s", language, msg)
		}
	}
}
