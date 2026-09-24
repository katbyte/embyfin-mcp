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

// The clean libraries are clean: every audit leaves them alone.
func TestAuditsLeaveTheCleanLibrariesAlone(t *testing.T) {
	for _, audit := range []string{"audit_missing_metadata_provider", "audit_missing_overview", "audit_file_path", "audit_multiple_versions"} {
		for _, lib := range []string{"Movies", "Shows"} {
			if audit == "audit_multiple_versions" && lib == "Shows" {
				continue // the show library has no versions and, with the provider on, virtual episodes
			}
			if audit == "audit_file_path" && lib == "Shows" {
				continue // the show library holds the one file named after another episode, which TestAuditFilePathShows reads
			}
			out := call(t, audit, map[string]any{"library": lib})
			if n := num(t, out["total_findings"], "total_findings"); n != 0 {
				t.Errorf("%s on %s found %d: %v", audit, lib, n, out["findings"])
			}
			if num(t, out["items_scanned"], "items_scanned") == 0 {
				t.Errorf("%s on %s scanned nothing", audit, lib)
			}
		}
	}
	// the clean films all carry a poster.jpg
	out := call(t, "audit_missing_poster", map[string]any{"library": "Movies"})
	if n := num(t, out["total_findings"], "total_findings"); n != 0 {
		t.Errorf("audit_missing_poster on Movies found %d: %v", n, out["findings"])
	}
}

// unmatchedShows are the messy series no nfo names: Star Trek has none, the
// Twins pair have none, and Zzyzx Paths' names no id. Jellyfin keeps the
// double space in the second Twins folder's name.
var unmatchedShows = []string{"Star Trek The Next Generation", "Zzyzx  twins", "Zzyzx Paths", "Zzyzx Twins"}

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
		case name == "Zzyzx Crossed Wires" && detail != "no tvdb id; has imdb:tt0903747":
			t.Errorf("the film holding an IMDb id alone does not list it: %v", f)
		case !unmatched && name != "Zzyzx Crossed Wires" && !strings.HasPrefix(detail, "no tvdb id; has tmdb:"):
			t.Errorf("a film matched on TMDB does not say so: %v", f)
		}
	}
	// TMDB alone finds the films matched nowhere, and the one matched on its
	// IMDb id alone
	out = call(t, "audit_missing_metadata_provider", map[string]any{"library": "Messy Movies", "missing": "tmdb"})
	if got, want := findings(t, out), sorted(append([]string{"Zzyzx Crossed Wires"}, messyUnmatched...)); !slices.Equal(got, want) {
		t.Errorf("missing tmdb = %v, want %v", got, want)
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
	// the messy series with no tvshow.nfo to give them a plot
	out = call(t, "audit_missing_overview", map[string]any{"library": "Messy Shows", "types": "Series"})
	if got, want := findings(t, out), []string{"Star Trek The Next Generation", "Zzyzx  twins", "Zzyzx Twins"}; !slices.Equal(got, want) {
		t.Errorf("series with no overview = %v, want %v", got, want)
	}
}

func TestAuditMissingPoster(t *testing.T) {
	out := call(t, "audit_missing_poster", map[string]any{"library": "Messy Movies"})
	if got := findings(t, out); !slices.Equal(got, messyBare) {
		t.Errorf("no poster = %v, want %v", got, messyBare)
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
		t.Errorf("file path = %v, want [Dune]", got)
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
}

// Zzyzx Paths' files and nfos disagree the ways a bulk import leaves them,
// and each is the check that says so. A file named without an episode marker
// (07 - Night Shift) claims no series, and is left alone.
//
// The run is where the servers differ: the nfo beside Zzyzx Paths
// S01E02E03.mp4 ends the run at episode 2, which Jellyfin reads over the
// file name, so it holds E02 alone and the file's E03 reads as missing; Emby
// takes the run from the file name and holds E02-E03, which the file agrees
// with.
func TestAuditFilePathSeriesSeasonAndEpisode(t *testing.T) {
	out := call(t, "audit_file_path", map[string]any{"library": "Messy Shows"})
	got := map[string][]string{}
	for _, f := range rows(t, out["findings"], "findings") {
		if str(f["series"]) != "Zzyzx Paths" || str(f["type"]) != "Episode" {
			t.Errorf("a finding outside Zzyzx Paths: %v", f)
			continue
		}
		got[filepath.Base(str(f["path"]))] = strs(t, f["problems"], "problems")
	}
	want := map[string]string{
		"Breaking Bad S01E06.mp4": `series: the file is named for "Breaking Bad", the server holds it under "Zzyzx Paths"`,
		"Zzyzx Paths S01E04.mp4":  "episode: the file says E04, the server holds E05",
		"Zzyzx Paths S02E08.mp4":  "season: the file says season 2, the server holds season 1",
	}
	if isJellyfin() {
		want["Zzyzx Paths S01E02E03.mp4"] = "episode: the file holds E02-E03, the server holds E02 alone"
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
	// a limit caps the rows, not the count, and keeps the plainest first: a
	// number or a series disagreeing
	capped := call(t, "audit_file_path", map[string]any{"library": "Messy Shows", "types": "Episode", "limit": 1})
	if found := rows(t, capped["findings"], "findings"); len(found) != 1 || num(t, capped["total_findings"], "total_findings") != len(got) {
		t.Errorf("limit 1 = %v of %v", found, capped["total_findings"])
	}
	// the episode held as a run is held whole: E03 is covered, not missing
	paths := findItem(t, "Messy Shows", "Series", "Zzyzx Paths")
	e03 := rows(t, call(t, "show_episodes_exist", map[string]any{"series_id": paths, "episodes": []map[string]any{{"season": 1, "episode": 3}}})["episodes"], "episodes")[0]
	if e03["exists"] != !isJellyfin() {
		t.Errorf("S01E03 of Zzyzx Paths = %v, want held only where the server reads the run from the file (Emby)", e03)
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
		"Movie Zzyzx Crossed Wires + Series Breaking Bad",
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

// Zzyzx Gaiden's episodes run three minutes but the last, cut to a second.
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
	if f := found[0]; str(f["name"]) != "Zzyzx Gaiden S01E03 Cut Short" || str(f["detail"]) != "0 min, season median 3 min (100% off)" || !strings.HasSuffix(str(f["path"]), "Zzyzx Gaiden S01E03.mp4") {
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
	// Princess Mononoke has no tmdb id and is skipped; every other file is a
	// second long
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
	skipped := map[string]bool{}
	for _, row := range rows(t, out["audits"], "audits") {
		name := str(row["audit"])
		counts[name] = num(t, row["findings"], "findings")
		if s, _ := row["skipped"].(bool); s {
			skipped[name] = true
			if str(row["note"]) == "" {
				t.Errorf("%s is skipped without a note", name)
			}
		}
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
		// Taken Down's Science-Fiction
		"audit_spelling": 1,
	}
	if !isJellyfin() {
		// Emby shows the two messy Aliens, sharing a TMDB id, as one film's
		// versions as well, which leaves no duplicates in the library
		want["audit_multiple_versions"], want["audit_duplicates"] = 2, 0
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
	for _, audit := range []string{"audit_orphans", "audit_language", "audit_provider", "audit_anime_ids"} {
		if !skipped[audit] {
			t.Errorf("%s is not marked skipped for one library: %v", audit, out["audits"])
		}
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
	// across the server, the orphans check runs
	whole := call(t, "audit_all", nil)
	for _, row := range rows(t, whole["audits"], "audits") {
		if s, _ := row["skipped"].(bool); str(row["audit"]) == "audit_orphans" && s {
			t.Errorf("audit_orphans skipped with no library given: %v", row)
		}
	}

	// the clean libraries are clean
	clean := call(t, "audit_all", map[string]any{"library": "Movies"})
	if n := num(t, clean["total_findings"], "total_findings"); n != 0 {
		t.Errorf("Movies total_findings = %d: %v", n, clean["audits"])
	}
	// and the messy shows' defects are each counted
	shows := map[string]int{}
	for _, row := range rows(t, call(t, "audit_all", map[string]any{"library": "Messy Shows"})["audits"], "audits") {
		shows[str(row["audit"])] = num(t, row["findings"], "findings")
	}
	pathRows := 3 // Zzyzx Paths' series, episode and season rows
	if isJellyfin() {
		pathRows = 4 // and the run Jellyfin reads from the nfo
	}
	for audit, n := range map[string]int{
		"audit_missing_metadata_provider": len(unmatchedShows),
		"audit_missing_poster":            messySeries,
		"audit_missing_overview":          3,
		"audit_file_path":                 pathRows,
		"audit_duplicates":                0,
		"audit_duplicate_series":          1,
		"audit_runtime":                   1,
		"audit_quality":                   messyEpisodes,
		"audit_missing_episodes":          2,
		"audit_spelling":                  1,
	} {
		if shows[audit] != n {
			t.Errorf("Messy Shows %s = %d, want %d", audit, shows[audit], n)
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
	if f := rows(t, japanese["findings"], "findings"); len(f) == 1 && !strings.Contains(str(f[0]["detail"]), "audio: jpn") {
		t.Errorf("detail = %v", f[0]["detail"])
	}

	// the rest carry untagged audio, so nothing is reported as lacking
	// Japanese: an untagged track may be it. The Blu-ray kept whole has no
	// audio track the server knows of - it never probed the disc - which is
	// counted apart rather than judged
	lacking := call(t, "audit_language", map[string]any{"language": "jpn", "find": "no_audio", "library": "Messy Movies"})
	scanned := num(t, lacking["items_scanned"], "items_scanned")
	if n := num(t, lacking["total_findings"], "total_findings"); n != 0 || scanned != messyMovies() {
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
	// the films alone are the whole library, and one finding is the limit
	if got := names(call(t, "audit_language", map[string]any{"language": "eng", "find": "subtitles", "library": "Movies", "types": "Movie", "limit": 1})); !slices.Equal(got, []string{"The Thirteenth Floor"}) {
		t.Errorf("types Movie limit 1 = %v", got)
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
}
