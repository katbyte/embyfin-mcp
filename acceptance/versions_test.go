//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Versions, as each server shows them: Jellyfin merges the files of one film
// or episode in one folder into one item when it scans; Emby stores each
// file as an item of its own and merges them only in what it shows people.
// These stage a version beside the fixtures' own and take it away again, so
// the counts other tests read are left as they were.

// messyMoviesShown is how many films the messy library shows people: Emby
// shows the two Blade Runner files, and the two Aliens sharing a TMDB id, as
// one film each (TestAuditMultipleVersions), where Jellyfin stores its merge.
// It is what the audits judging a film by all its files count.
func messyMoviesShown() int {
	if isJellyfin() {
		return messyMovies()
	}

	return messyMovies() - 2
}

// searchKey is a TMDB film search as the cassette keys it, for
// needsTMDBRecording: the & between the parameters as JSON escapes it, a
// backslash (rune 92) and u0026.
func searchKey(query string) string {
	return "GET api.themoviedb.org/3/search/movie?" + strings.ReplaceAll(query, "&", string(rune(92))+"u0026")
}

// stageFile writes a file under the media tree for the rest of the test.
func stageFile(t *testing.T, path string, data []byte) {
	t.Helper()

	mediaMkdir(t, filepath.Dir(path))
	mediaWrite(t, path, data)
	t.Cleanup(func() { _ = os.Remove(path) })
}

// versionCount is how many files item_get shows an item in, or -1 when it
// cannot be read (an item a scan is replacing).
func versionCount(id string) int {
	out, err := invoke("item_get", map[string]any{"id": id})
	if err != nil {
		return -1
	}
	versions, _ := out["versions"].([]any)

	return max(len(versions), 1)
}

// itemsTitled are the ids library_items lists under a title in a library:
// on Emby every version of a film is one, as it stores them.
func itemsTitled(t *testing.T, library, types, name string) []string {
	t.Helper()

	var ids []string
	for _, row := range rows(t, call(t, "library_items", map[string]any{"library": library, "types": types, "query": name, "limit": 50})["items"], "items") {
		if strings.EqualFold(title(str(row["name"])), name) {
			ids = append(ids, str(row["id"]))
		}
	}
	if len(ids) == 0 {
		t.Fatalf("nothing titled %q in %s", name, library)
	}

	return ids
}

// A 360p copy staged beside the messy Blade Runner's 1080p and 2160p files is
// a third version of it. The film is judged by its best file on both
// servers: on Emby, which stores the copy as an item of its own, audit_quality
// read it alone and reported a DVD-grade Blade Runner the library also holds
// in 4K.
func TestAVersionBesideABetterOne(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	blade := itemsTitled(t, "Messy Movies", "Movie", "Blade Runner")[0]
	before := call(t, "audit_quality", map[string]any{"library": "Messy Movies"})
	if slices.Contains(findings(t, before), "Blade Runner") {
		t.Fatalf("Blade Runner is reported before a third version is staged: %v", before["findings"])
	}

	t.Cleanup(func() { scanUntilTrue(t, "Messy Movies", func() bool { return versionCount(blade) == 2 }) })
	copied := filepath.Join(dataDir(), "messy-movies", messyBladeRunner, messyBladeRunner+" - 360p.mp4")
	stageFile(t, copied, fixtureVideo(t, "messy-movies", messyArrival, messyArrival+".mp4"))
	scanUntilTrue(t, "Messy Movies", func() bool { return versionCount(blade) == 3 })

	// item_get lists every version with its own facts
	var heights []int
	for _, v := range rows(t, call(t, "item_get", map[string]any{"id": blade})["versions"], "versions") {
		heights = append(heights, num(t, v["height"], "height"))
		if strings.HasSuffix(str(v["path"]), " - 360p.mp4") && str(v["label"]) != "360p" {
			t.Errorf("the staged version's label = %v", v)
		}
	}
	if slices.Sort(heights); !slices.Equal(heights, []int{360, 1080, 2160}) {
		t.Errorf("version heights = %v, want 360, 1080 and 2160", heights)
	}

	after := call(t, "audit_quality", map[string]any{"library": "Messy Movies"})
	if got := findings(t, after); slices.Contains(got, "Blade Runner") {
		t.Errorf("audit_quality reports Blade Runner by its 360p version, beside a 2160p one: %v", after["findings"])
	}
	for _, field := range []string{"items_scanned", "total_findings"} {
		if num(t, after[field], field) != num(t, before[field], field) {
			t.Errorf("%s = %v with a third version staged, %v before: a version is not an item", field, after[field], before[field])
		}
	}
	for _, list := range []string{"unprobed", "replaced"} {
		for _, row := range rows(t, after[list], list) {
			if strings.Contains(str(row["path"]), " - 360p.mp4") {
				t.Errorf("the staged version is listed %s: %v", list, row)
			}
		}
	}

	versions := call(t, "audit_multiple_versions", map[string]any{"library": "Messy Movies", "types": "Movie"})
	found := false
	for _, f := range rows(t, versions["findings"], "findings") {
		if title(str(f["name"])) != "Blade Runner" {
			continue
		}
		found = true
		if d := str(f["detail"]); !strings.HasPrefix(d, "3 versions: ") || !strings.Contains(d, messyBladeRunner+" - 360p.mp4") || f["warning"] != nil {
			t.Errorf("Blade Runner's versions = %v", f)
		}
	}
	if !found {
		t.Errorf("audit_multiple_versions lacks Blade Runner: %v", versions["findings"])
	}
}

// A second copy of the messy Despecialized Edition, its German audio beside
// an English subtitle file, is a version of it: any version counts, so the
// film can be watched in English, has English subtitles, and is one film with
// German audio, not two.
func TestALanguageInAnotherVersion(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	restoration := findItem(t, "Messy Movies", "Movie", despecialized)
	unwatchable := map[string]any{"language": "eng", "find": "unwatchable", "library": "Messy Movies"}
	if got := findings(t, call(t, "audit_language", unwatchable)); !slices.Equal(got, []string{despecialized}) {
		t.Fatalf("unwatchable in English before a version is staged = %v, want [%s]", got, despecialized)
	}

	t.Cleanup(func() { scanUntilTrue(t, "Messy Movies", func() bool { return versionCount(restoration) == 1 }) })
	dir := filepath.Join(dataDir(), "messy-movies", messyDespecialized)
	stageFile(t, filepath.Join(dir, messyDespecialized+" - Subtitled.mp4"), fixtureVideo(t, "messy-movies", messyDespecialized, messyDespecialized+".mp4"))
	stageFile(t, filepath.Join(dir, messyDespecialized+" - Subtitled.eng.srt"), []byte("1\n00:00:00,000 --> 00:00:00,900\nA line.\n"))
	scanUntilTrue(t, "Messy Movies", func() bool { return versionCount(restoration) == 2 })

	if got := findings(t, call(t, "audit_language", unwatchable)); len(got) != 0 {
		t.Errorf("unwatchable in English = %v, want none: a version has English subtitles", got)
	}
	subtitled := call(t, "audit_language", map[string]any{"language": "eng", "find": "subtitles", "library": "Messy Movies"})
	if got := findings(t, subtitled); !slices.Equal(got, []string{despecialized}) {
		t.Errorf("English subtitles = %v, want [%s]", got, despecialized)
	}
	if got := findings(t, call(t, "audit_language", map[string]any{"language": "deu", "library": "Messy Movies"})); !slices.Equal(got, []string{despecialized}) {
		t.Errorf("German audio = %v, want the Despecialized Edition once: two versions are one film", got)
	}
}

// A second copy of the messy Severance's first episode, its nfo naming it the
// same episode, is a version of that episode on both servers, and not the
// one season holding its title twice.
func TestAnEpisodeInTwoVersions(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	series := findItem(t, "Messy Shows", "Series", "Severance")
	// the most versions any first episode is shown in: which item a server
	// lists as the episode may change when a version joins it
	pilotVersions := func() int {
		out, err := invoke("library_episodes", map[string]any{"series_id": series, "season": 1})
		if err != nil {
			return -1
		}
		most := 0
		for _, row := range rowsOf(out["episodes"]) {
			if num(t, row["episode"], "episode") == 1 {
				most = max(most, versionCount(str(row["id"])))
			}
		}
		return most
	}
	if n := pilotVersions(); n != 1 {
		t.Fatalf("the first episode is in %d versions before one is staged", n)
	}

	t.Cleanup(func() { scanUntilTrue(t, "Messy Shows", func() bool { return pilotVersions() == 1 }) })
	season := filepath.Join(dataDir(), "messy-shows", "Severance", "Season 01")
	stageFile(t, filepath.Join(season, "Severance S01E01 - 720p.mp4"), fixtureVideo(t, "messy-shows", "Severance", "Season 01", "Severance S01E01.mp4"))
	stageFile(t, filepath.Join(season, "Severance S01E01 - 720p.nfo"), episodeNfo("Good News About Hell", 1, 1))
	scanUntilTrue(t, "Messy Shows", func() bool { return pilotVersions() == 2 })

	out := call(t, "audit_duplicate_episodes", map[string]any{"library": "Messy Shows"})
	for _, g := range rows(t, out["groups"], "groups") {
		if strings.EqualFold(str(g["title"]), "Good News About Hell") {
			t.Errorf("the episode's two versions are reported as its title filed twice: %v", g)
		}
	}
}

// A copy of another film matched to Interstellar's ids: a three-minute file
// in a folder named for The Thirteenth Floor, whose nfo carries Interstellar's
// title, year and ids. Emby shows it as a version of the messy Interstellar,
// sharing their ids; Jellyfin holds it apart, as a second entry. Either way a
// caller comparing the two to keep the better would delete a film, so every
// tool that shows them together says they are probably two films.
func TestAFilmMatchedToAnothersIDs(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	dir := filepath.Join(dataDir(), "messy-movies", "The Thirteenth Floor (1999)")
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		if err := scanUntil("Messy Movies", messyMovies()); err != nil {
			t.Error(err)
		}
	})
	stageFile(t, filepath.Join(dir, "The Thirteenth Floor (1999).mp4"), fixtureVideo(t, "anime-src", "special.mp4"))
	stageFile(t, filepath.Join(dir, "movie.nfo"), []byte(`<?xml version="1.0" encoding="utf-8"?>
<movie>
  <title>Interstellar</title>
  <year>2014</year>
  <tmdbid>157336</tmdbid>
  <uniqueid type="tmdb" default="true">157336</uniqueid>
  <imdbid>tt0816692</imdbid>
  <uniqueid type="imdb">tt0816692</uniqueid>
</movie>
`))
	if err := scanUntil("Messy Movies", messyMovies()+1); err != nil {
		t.Fatal(err)
	}
	var real, staged string
	for _, id := range itemsTitled(t, "Messy Movies", "Movie", "Interstellar") {
		if strings.Contains(str(call(t, "item_get", map[string]any{"id": id})["path"]), "The Thirteenth Floor") {
			staged = id
		} else {
			real = id
		}
	}
	if real == "" || staged == "" {
		t.Fatalf("the messy Interstellar %q and the staged film %q are not both listed", real, staged)
	}
	named := `"The Thirteenth Floor (1999).mp4" is named for "The Thirteenth Floor" (1999), not Interstellar (2014)`

	versions := call(t, "audit_multiple_versions", map[string]any{"library": "Messy Movies", "types": "Movie"})
	var merged map[string]any
	for _, f := range rows(t, versions["findings"], "findings") {
		if title(str(f["name"])) == "Interstellar" {
			merged = f
		}
	}
	var group []map[string]any
	for _, g := range call(t, "audit_duplicates", map[string]any{"library": "Messy Movies", "types": "Movie"})["groups"].([]any) {
		members := rows(t, g, "group")
		if title(str(members[0]["name"])) == "Interstellar" {
			group = members
		}
	}
	if !isJellyfin() {
		// shown as one film in two versions, whichever of the two is read
		for _, id := range []string{real, staged} {
			got := call(t, "item_get", map[string]any{"id": id})
			if w := str(got["warning"]); !strings.HasPrefix(w, "probably not one film") || !strings.Contains(w, named) || !strings.Contains(w, " 1s") || !strings.Contains(w, " 3 min") {
				t.Errorf("item_get %s warning = %q", id, w)
			}
			if n := len(rows(t, got["versions"], "versions")); n != 2 {
				t.Errorf("item_get %s lists %d versions, want the two files", id, n)
			}
		}
		if merged == nil || !strings.HasPrefix(str(merged["warning"]), "probably not one film") {
			t.Errorf("audit_multiple_versions Interstellar = %v", merged)
		}
		if group != nil {
			t.Errorf("audit_duplicates groups what Emby shows as one film: %v", group)
		}
	} else {
		// held apart: the staged entry's own file names another film
		if w := str(call(t, "item_get", map[string]any{"id": staged})["warning"]); !strings.HasPrefix(w, "may be a different film matched to this one's ids") || !strings.Contains(w, named) {
			t.Errorf("item_get of the staged entry warning = %q", w)
		}
		if got := call(t, "item_get", map[string]any{"id": real}); got["warning"] != nil || got["versions"] != nil {
			t.Errorf("item_get of the messy Interstellar = warning %v, versions %v", got["warning"], got["versions"])
		}
		if len(group) != 2 {
			t.Fatalf("audit_duplicates Interstellar = %v", group)
		}
		for _, m := range group {
			if w := str(m["warning"]); !strings.HasPrefix(w, "probably not copies of one film") || !strings.Contains(w, named) {
				t.Errorf("%v warning = %q", m["path"], w)
			}
		}
		if merged != nil {
			t.Errorf("audit_multiple_versions reports what Jellyfin holds apart: %v", merged)
		}
	}

	// compared, they are two films: a caveat, never the verdict
	compared := call(t, "quality_compare", map[string]any{"a": map[string]any{"item_id": real}, "b": map[string]any{"item_id": staged}})
	caveats := strings.Join(strs(t, compared["caveats"], "caveats"), " | ")
	if !strings.Contains(caveats, "these may not be the same film") || !strings.Contains(caveats, named) || !strings.Contains(caveats, "they run 1s and 3 min: a different cut, or a different film") {
		t.Errorf("quality_compare caveats = %q", caveats)
	}

	// and the staged file's path names another title and another year: TMDB
	// says which film the path names, beside the id the item carries
	needsTMDBRecording(t, searchKey("query=The+Thirteenth+Floor&year=1999"))
	paths := call(t, "audit_file_path", map[string]any{"ids": []any{staged}})
	row := rows(t, paths["findings"], "findings")
	if len(row) != 1 || num(t, paths["items_scanned"], "items_scanned") != 1 {
		t.Fatalf("audit_file_path of the staged film = %v", paths)
	}
	problems := strs(t, row[0]["problems"], "problems")
	if len(problems) != 2 || !strings.HasPrefix(problems[0], `title: the path is named "The Thirteenth Floor", the server holds "Interstellar"`) || problems[1] != "year: path says 1999, metadata says 2014" {
		t.Errorf("problems = %v", problems)
	}
	if str(row[0]["path_tmdb"]) != "1090 The Thirteenth Floor (1999)" || str(row[0]["item_tmdb"]) != "157336" ||
		!strings.Contains(str(row[0]["diagnosis"]), "the path names TMDB's film 1090, The Thirteenth Floor (1999); the item carries TMDB 157336") ||
		!strings.Contains(str(row[0]["diagnosis"]), "the file runs 3 min") {
		t.Errorf("the TMDB diagnosis = %v", row[0])
	}
}
