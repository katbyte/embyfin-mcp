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
	for _, audit := range []string{"audit_missing_metadata_provider", "audit_missing_overview", "audit_year_mismatch", "audit_multiple_versions"} {
		for _, lib := range []string{"Movies", "Shows"} {
			if audit == "audit_multiple_versions" && lib == "Shows" {
				continue // the show library has no versions and, with the provider on, virtual episodes
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

func TestAuditMissingMetadataProvider(t *testing.T) {
	out := call(t, "audit_missing_metadata_provider", map[string]any{"library": "Messy Movies"})
	if got := findings(t, out); !slices.Equal(got, []string{"Princess Mononoke"}) {
		t.Errorf("unmatched movies = %v, want [Princess Mononoke]", got)
	}
	if got := num(t, out["items_scanned"], "items_scanned"); got != messyMovies() {
		t.Errorf("scanned %d, want %d", got, messyMovies())
	}
	f := rows(t, out["findings"], "findings")[0]
	if str(f["id"]) == "" || !strings.Contains(str(f["path"]), messyMononoke) || str(f["detail"]) == "" {
		t.Errorf("finding = %v", f)
	}

	out = call(t, "audit_missing_metadata_provider", map[string]any{"library": "Messy Shows", "types": "Series"})
	if got := findings(t, out); !slices.Equal(got, []string{"Star Trek The Next Generation"}) {
		t.Errorf("unmatched shows = %v, want [Star Trek The Next Generation]", got)
	}

	// across every library the count is the sum
	out = call(t, "audit_missing_metadata_provider", nil)
	if n := num(t, out["total_findings"], "total_findings"); n != 2 {
		t.Errorf("unmatched everywhere = %d, want 2", n)
	}
	// and a limit caps the worklist, not the count
	out = call(t, "audit_missing_metadata_provider", map[string]any{"limit": 1})
	if n := len(rows(t, out["findings"], "findings")); n != 1 || num(t, out["total_findings"], "total_findings") != 2 {
		t.Errorf("limit 1 = %d findings of %v", n, out["total_findings"])
	}
}

func TestAuditMissingOverview(t *testing.T) {
	out := call(t, "audit_missing_overview", map[string]any{"library": "Messy Movies"})
	if got := findings(t, out); !slices.Equal(got, []string{"Arrival", "Princess Mononoke"}) {
		t.Errorf("no overview = %v, want [Arrival Princess Mononoke]", got)
	}
}

func TestAuditMissingPoster(t *testing.T) {
	out := call(t, "audit_missing_poster", map[string]any{"library": "Messy Movies"})
	if got := findings(t, out); !slices.Equal(got, []string{"Arrival", "Princess Mononoke"}) {
		t.Errorf("no poster = %v, want [Arrival Princess Mononoke]", got)
	}
}

func TestAuditYearMismatch(t *testing.T) {
	out := call(t, "audit_year_mismatch", map[string]any{"library": "Messy Movies"})
	if got := findings(t, out); !slices.Equal(got, []string{"Dune"}) {
		t.Errorf("year mismatch = %v, want [Dune]", got)
	}
	f := rows(t, out["findings"], "findings")[0]
	if !strings.Contains(str(f["detail"]), "2021") || !strings.Contains(str(f["detail"]), "1984") || num(t, f["year"], "year") != 1984 {
		t.Errorf("finding = %v", f)
	}
}

func TestAuditDuplicates(t *testing.T) {
	// within the messy library: Alien twice, and on Emby the two Blade
	// Runner files as two entries too
	out := call(t, "audit_duplicates", map[string]any{"library": "Messy Movies"})
	groups, _ := out["duplicate_groups"].([]any)
	wantGroups := 1
	if !versionsMerged() {
		wantGroups = 2
	}
	if num(t, out["total_groups"], "total_groups") != wantGroups || len(groups) != wantGroups {
		t.Fatalf("duplicates = %v", out)
	}
	group := rows(t, groups[0], "group") // Alien sorts first
	if len(group) != 2 {
		t.Fatalf("group = %v", group)
	}
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
	if !plain || !cut {
		t.Errorf("the group is not the two messy Aliens: %v", group)
	}

	// across libraries the clean Alien joins the group, and a limit caps the
	// groups returned but not the count
	out = call(t, "audit_duplicates", map[string]any{"types": "Movie", "limit": 1})
	groups, _ = out["duplicate_groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("limit 1 returned %d groups", len(groups))
	}
	var alien []map[string]any
	for _, g := range groups {
		for _, it := range rows(t, g, "group") {
			if str(it["name"]) == "Alien" {
				alien = append(alien, it)
			}
		}
	}
	// Blade Runner is also in both libraries, so which group comes first is
	// alphabetical: Alien
	if len(alien) != 3 {
		t.Errorf("Alien group across libraries has %d copies, want 3", len(alien))
	}
	if n := num(t, out["total_groups"], "total_groups"); n < 2 {
		t.Errorf("total_groups = %d, want Alien and Blade Runner at least", n)
	}
	// the clean libraries alone have none
	out = call(t, "audit_duplicates", map[string]any{"library": "Movies"})
	if num(t, out["total_groups"], "total_groups") != 0 {
		t.Errorf("Movies has duplicates: %v", out)
	}
}

func TestAuditMultipleVersions(t *testing.T) {
	out := call(t, "audit_multiple_versions", map[string]any{"library": "Messy Movies"})
	if got := num(t, out["items_scanned"], "items_scanned"); got != messyMovies() {
		t.Errorf("scanned %d, want %d", got, messyMovies())
	}
	if !versionsMerged() {
		// Emby keeps the two files as two films: audit_duplicates' business
		if got := findings(t, out); len(got) != 0 {
			t.Errorf("multiple versions on Emby = %v, want none", got)
		}
		return
	}
	if got := findings(t, out); !slices.Equal(got, []string{"Blade Runner"}) {
		t.Errorf("multiple versions = %v, want [Blade Runner]", got)
	}
	f := rows(t, out["findings"], "findings")[0]
	if d := str(f["detail"]); !strings.Contains(d, "2 versions") || !strings.Contains(d, "1080p") || !strings.Contains(d, "2160p") {
		t.Errorf("detail = %q", d)
	}
}

func TestAuditRuntimeEpisodes(t *testing.T) {
	// the messy Severance has a five-second third episode among one-second
	// ones: 0 minutes against a 0 minute median is under the floor, so the
	// fixture cannot trip the audit at one-second scale. What it can prove is
	// the sweep: every episode counted, nothing false
	out := call(t, "audit_runtime", map[string]any{"library": "Messy Shows"})
	if got := num(t, out["items_scanned"], "items_scanned"); got != 5 {
		t.Errorf("scanned %d episodes, want 5", got)
	}
	if n := num(t, out["total_findings"], "total_findings"); n != 0 {
		t.Errorf("findings = %v", out["findings"])
	}
	if _, ok := out["next_start_index"]; ok {
		t.Error("episode mode paged")
	}
	if msg := callErr(t, "audit_runtime", map[string]any{"types": "Album"}); !strings.Contains(msg, "Episode or Movie") {
		t.Errorf("an unsupported type: %s", msg)
	}
}

// Movie runtimes come from TMDB, through the provider proxy: Interstellar's
// nfo says 169 minutes and the file runs a second, but the audit asks TMDB,
// not the nfo, so every one-second film in the messy library is off.
func TestAuditRuntimeMovies(t *testing.T) {
	needsTMDBCassette(t)
	out := call(t, "audit_runtime", map[string]any{"library": "Messy Movies", "types": "Movie"})
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
		if d := str(f["detail"]); !strings.Contains(d, "TMDB says") {
			t.Errorf("detail = %q", d)
		}
	}

	// paging: two lookups per call, then continue from next_start_index
	out = call(t, "audit_runtime", map[string]any{"library": "Messy Movies", "types": "Movie", "max_lookups": 2})
	if len(rows(t, out["findings"], "findings")) != 2 {
		t.Errorf("max_lookups 2 = %v", out["findings"])
	}
	next, ok := out["next_start_index"]
	if !ok {
		t.Fatal("no next_start_index after a partial sweep")
	}
	rest := call(t, "audit_runtime", map[string]any{"library": "Messy Movies", "types": "Movie", "start_index": next})
	if n, want := num(t, rest["total_findings"], "total_findings"), len(want)-2; n != want {
		t.Errorf("the rest = %d findings, want %d", n, want)
	}
	// tolerance: a one-second file is always more than 99% off, so a
	// tolerance of 100 finds nothing
	out = call(t, "audit_runtime", map[string]any{"library": "Messy Movies", "types": "Movie", "tolerance_percent": 100})
	if n := num(t, out["total_findings"], "total_findings"); n != 0 {
		t.Errorf("tolerance 100 found %d", n)
	}
}

// needsTMDBCassette skips the movie runtime audit when it cannot run: a
// recording needs a real TMDB key, and a replay needs the recording to have
// been made (make record with EMBYFIN_TMDB_KEY set).
func needsTMDBCassette(t *testing.T) {
	t.Helper()

	if recording() {
		if os.Getenv("EMBYFIN_TMDB_KEY") == "" {
			t.Skip("recording the TMDB runtime lookups needs EMBYFIN_TMDB_KEY")
		}
		return
	}
	raw, err := os.ReadFile(filepath.Join(cassetteDir(), "api.themoviedb.org.json"))
	if err != nil || !strings.Contains(string(raw), `"key": "GET api.themoviedb.org/3/movie/348"`) {
		t.Skip("the TMDB runtime lookups have not been recorded; run make record with EMBYFIN_TMDB_KEY set")
	}
}

// audit_all is the overview: one row per audit with the counts the audits
// themselves report, across the whole server.
func TestAuditAll(t *testing.T) {
	out := call(t, "audit_all", map[string]any{"library": "Messy Movies"})
	counts := map[string]int{}
	for _, row := range rows(t, out["audits"], "audits") {
		counts[str(row["audit"])] = num(t, row["findings"], "findings")
	}
	want := map[string]int{
		"audit_missing_metadata_provider": 1,
		"audit_missing_poster":            2,
		"audit_missing_overview":          2,
		"audit_year_mismatch":             1,
		"audit_multiple_versions":         1,
		"audit_duplicates":                1,
		"audit_runtime":                   0,
		"audit_quality":                   6,
		"audit_missing_episodes":          0,
		"audit_spelling":                  0,
	}
	if !versionsMerged() {
		want["audit_multiple_versions"], want["audit_duplicates"] = 0, 2
	}
	for audit, n := range want {
		if counts[audit] != n {
			t.Errorf("%s = %d, want %d", audit, counts[audit], n)
		}
	}
	if len(counts) != len(want) {
		t.Errorf("audits = %v, want %d rows", counts, len(want))
	}
	total := 0
	for _, n := range want {
		total += n
	}
	if num(t, out["total_findings"], "total_findings") != total {
		t.Errorf("total_findings = %v, want %d", out["total_findings"], total)
	}

	// the clean libraries are clean
	clean := call(t, "audit_all", map[string]any{"library": "Movies"})
	if n := num(t, clean["total_findings"], "total_findings"); n != 0 {
		t.Errorf("Movies total_findings = %d: %v", n, clean["audits"])
	}
	// and the messy shows' gap is counted
	shows := call(t, "audit_all", map[string]any{"library": "Messy Shows"})
	for _, row := range rows(t, shows["audits"], "audits") {
		if str(row["audit"]) == "audit_missing_episodes" && num(t, row["findings"], "findings") != 1 {
			t.Errorf("Messy Shows audit_missing_episodes = %v, want Star Trek The Next Generation", row)
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
		"audit_all", "audit_duplicates", "audit_language", "audit_missing_episodes", "audit_missing_metadata_provider", "audit_missing_overview",
		"audit_missing_poster", "audit_multiple_versions", "audit_quality", "audit_runtime", "audit_spelling", "audit_unwatched", "audit_year_mismatch",
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
	// Japanese: an untagged track may be it
	lacking := call(t, "audit_language", map[string]any{"language": "jpn", "find": "no_audio", "library": "Messy Movies"})
	scanned := num(t, lacking["items_scanned"], "items_scanned")
	if n := num(t, lacking["total_findings"], "total_findings"); n != 0 {
		t.Errorf("untagged audio was reported as lacking japanese: %v", lacking["findings"])
	}
	if n := num(t, lacking["untagged"], "untagged"); n != scanned-1 {
		t.Errorf("untagged = %d of %d scanned, want all but Princess Mononoke", n, scanned)
	}

	// the subtitle is read off the file beside the film, language from its name
	subtitled := call(t, "audit_language", map[string]any{"language": "eng", "find": "subtitles", "library": "Movies"})
	if got := names(subtitled); !slices.Equal(got, []string{"The Thirteenth Floor"}) {
		t.Errorf("english subtitles = %v, want [The Thirteenth Floor]", got)
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
