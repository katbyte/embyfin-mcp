//go:build integration

package acceptance

import (
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Journeys through a title's identity: an unmatched show identified end to
// end, a film holding another film's id put right, and a film holding a
// series' id put right in a library that fetches nothing.

// needsTMDBAnswers skips unless every TMDB request described can be answered:
// live while recording with a key, else from the cassette. Each request is
// the parts its key must hold, because the two servers ask the same question
// with their own query strings.
func needsTMDBAnswers(t *testing.T, requests ...[]string) {
	t.Helper()

	if recording() {
		if os.Getenv("EMBYFIN_TMDB_TOKEN") == "" && os.Getenv("EMBYFIN_TMDB_KEY") == "" {
			t.Skip("recording the TMDB lookups needs EMBYFIN_TMDB_TOKEN")
		}
		return
	}
	var cassette struct {
		Interactions []struct{ Key string } `json:"interactions"`
	}
	raw, err := os.ReadFile(filepath.Join(cassetteDir(), "api.themoviedb.org.json"))
	if err == nil {
		err = json.Unmarshal(raw, &cassette)
	}
	if err != nil {
		t.Skipf("no TMDB recordings to replay: %v", err)
	}
	for _, parts := range requests {
		if !slices.ContainsFunc(cassette.Interactions, func(i struct{ Key string }) bool {
			return !slices.ContainsFunc(parts, func(p string) bool { return !strings.Contains(i.Key, p) })
		}) {
			t.Skipf("no recorded TMDB answer holds %q; run make record with EMBYFIN_TMDB_TOKEN set", parts)
		}
	}
}

// providerIDs reads an item's provider ids off item_get.
func providerIDs(t *testing.T, id string) map[string]any {
	t.Helper()

	ids, _ := call(t, "item_get", map[string]any{"id": id})["metadata_provider_ids"].(map[string]any)

	return ids
}

// An unmatched show identified: the audit finds it, item_identify offers the
// real show, item_identify_apply gives it that show's ids, the audit lets go
// of it, and the episode audit, asking TMDB by the new id, now knows its run.
// The messy show library has its fetchers off, so the match sets the ids and
// nothing else, which is all the run needs, and a scan and a refresh after it
// leave them be.
func TestIdentifyingAnUnmatchedShow(t *testing.T) {
	needsTMDBRecording(t, "GET api.themoviedb.org/3/tv/655")
	const show = "Star Trek The Next Generation"
	tng := findItem(t, "Messy Shows", "Series", show)
	unmatched := func() []string {
		return findings(t, call(t, "audit_missing_metadata_provider", map[string]any{"library": "Messy Shows", "types": "Series"}))
	}
	if !slices.Contains(unmatched(), show) {
		t.Fatalf("audit_missing_metadata_provider = %v, want %s among them", unmatched(), show)
	}
	// with no id, the episode audit cannot ask anyone for its run
	missing := func() (unknown bool, detail string) {
		out := call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows", "provider": true})
		for _, u := range rows(t, out["unknown"], "unknown") {
			unknown = unknown || str(u["id"]) == tng
		}
		for _, f := range rows(t, out["findings"], "findings") {
			if str(f["id"]) == tng {
				detail = str(f["detail"])
			}
		}
		return unknown, detail
	}
	if unknown, detail := missing(); !unknown || strings.Contains(detail, "TMDB") {
		t.Fatalf("before the match the show's run is unknown %v, its finding %q", unknown, detail)
	}

	// the title as the providers spell it, the same for the search and the
	// apply so the candidates line up
	const name = "Star Trek: The Next Generation"
	cands := rows(t, call(t, "item_identify", map[string]any{"id": tng, "kind": "series", "name": name})["candidates"], "candidates")
	idx := -1
	for i, c := range cands {
		if ids, _ := c["metadata_provider_ids"].(map[string]any); idx < 0 && str(ids["tmdb"]) == "655" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no Star Trek: The Next Generation (tmdb 655) among the candidates: %v", cands)
	}

	// what the show's folder holds besides its videos, to put back: the
	// servers write nfos of their own for a library that saves them
	numbers := func() []int {
		var out []int
		for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": tng})["episodes"], "episodes") {
			out = append(out, numOr0(e["episode"]))
		}
		slices.Sort(out)
		return out
	}
	folder := filepath.Join(dataDir(), "messy-shows", show)
	sidecars := map[string][]byte{}
	_ = filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".nfo") {
			raw, _ := os.ReadFile(path) //nolint:gosec // a fixture under the test data dir
			sidecars[path] = raw
		}
		return nil
	})
	t.Cleanup(func() {
		updateItem(t, tng, map[string]any{"ProviderIds": map[string]any{}})
		_ = filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
			if _, ours := sidecars[path]; err == nil && strings.HasSuffix(path, ".nfo") && !ours {
				_ = os.Remove(path)
			}
			return nil
		})
		for path, raw := range sidecars {
			mediaWrite(t, path, raw)
		}
		// the files re-read, the episodes numbered as they were, and the
		// show unmatched again
		call(t, "item_refresh", map[string]any{"id": tng})
		if !eventually(func() bool { return slices.Equal(numbers(), []int{1, 3}) }) {
			t.Errorf("after putting the nfos back the show's episodes are %v", numbers())
		}
		if !eventually(func() bool { return slices.Contains(unmatched(), show) }) {
			t.Errorf("after clearing its ids the show is not unmatched again: %v", providerIDs(t, tng))
		}
	})

	// the name, year and overview as they are, which the match must not touch
	identity := func() [3]any {
		item := fullItem(t, tng)
		return [3]any{item["Name"], item["ProductionYear"], item["Overview"]}
	}
	before := identity()
	applied := call(t, "item_identify_apply", map[string]any{"id": tng, "kind": "series", "candidate": idx, "name": name, "replace_all_images": true})
	if ids, _ := applied["metadata_provider_ids"].(map[string]any); str(ids["tmdb"]) != "655" {
		t.Fatalf("item_identify_apply = %v, want tmdb 655", applied)
	}
	if !strings.Contains(str(applied["note"]), "metadata fetchers off") {
		t.Errorf("item_identify_apply says nothing of the fetchers being off: %v", applied)
	}
	// with the fetchers off the ids are set by a plain edit, which changes
	// them and nothing else: the name, year and overview stay as they were,
	// and the episodes keep the numbers their files and nfos give them (the
	// server's own apply refreshed the show, and on Jellyfin that numbered
	// every episode 0)
	if after := identity(); after != before {
		t.Errorf("the match changed the show's name, year or overview: %v, was %v", after, before)
	}
	keeps := func() bool { return slices.Equal(numbers(), []int{1, 3}) }
	if !holds(keeps) {
		t.Errorf("after the match the show's episodes are numbered %v, want [1 3]", numbers())
	}
	// and the nfos beside the files still number them, whoever wrote them
	// since (Jellyfin saves its own over them)
	for path := range sidecars {
		if raw, err := os.ReadFile(path); err != nil || !strings.Contains(string(raw), "<episode>") { //nolint:gosec // a fixture under the test data dir
			t.Errorf("%s no longer numbers its episode: %v %.300s", filepath.Base(path), err, raw)
		}
	}

	if got := unmatched(); slices.Contains(got, show) {
		t.Errorf("after the match audit_missing_metadata_provider still lists it: %v", got)
	}
	// the gap between its files, and TMDB's run past them
	checkRun := func(when string) {
		t.Helper()
		unknown, detail := missing()
		if unknown || !strings.Contains(detail, "between the episodes on disk: S01E02") || !strings.Contains(detail, "listed by TMDB without a file: S01E02, S01E04") {
			t.Errorf("%s the show's run is unknown %v, its finding %q, want its gap and TMDB's run", when, unknown, detail)
		}
	}
	checkRun("after the match")

	// and it stays matched: a scan of its library and a refresh of the show
	// leave the id where the match put it, and its run known
	tmdb := func() string { return str(providerIDs(t, tng)["tmdb"]) }
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
	call(t, "library_scan", map[string]any{"library": "Messy Shows"})
	if err := waitForExpectedScan(); err != nil {
		t.Fatal(err)
	}
	if got := tmdb(); got != "655" {
		t.Errorf("after a scan the show's tmdb id is %q, want 655", got)
	}
	call(t, "item_refresh", map[string]any{"id": tng})
	if !holds(func() bool { return tmdb() == "655" }) {
		t.Errorf("after a refresh the show's tmdb id is %q, want 655", tmdb())
	}
	checkRun("after a scan and a refresh")
}

// A film given another film's IMDb id, the way a hand edit or a bad match
// leaves it: audit_provider finds the two ids disagree, item_identify and
// item_identify_apply put the right film back, the audit comes back clean,
// and the IMDb id finds only the film it belongs to. The film's nfo, which
// names the right ids, is taken away for the test: Emby reads it again on the
// refresh the apply starts, and would put the right id back on its own.
func TestAMismatchedIDPutRight(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	needsTMDBAnswers(t, []string{"/3/movie/78?"}, []string{"/3/search/movie?", "query=Blade+Runner", "year=1982"})
	blade := findItem(t, "Movies", "Movie", "Blade Runner")
	before := fullItem(t, blade)
	nfo := filepath.Join(dataDir(), "movies", "Blade Runner (1982)", "movie.nfo")
	sidecar, err := os.ReadFile(nfo) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	// the apply replaces what the nfo gave the film (its genres, its people)
	// with TMDB's, so the whole item goes back as it was, and the nfo with it
	t.Cleanup(func() {
		mediaWrite(t, nfo, sidecar)
		updateItem(t, blade, before)
	})
	if err := os.Remove(nfo); err != nil {
		t.Fatal(err)
	}
	ids := maps.Clone(object(t, before["ProviderIds"], "ProviderIds"))
	const alien = "tt0078748"
	for k := range ids {
		if strings.EqualFold(k, "imdb") {
			ids[k] = alien
		}
	}
	updateItem(t, blade, map[string]any{"ProviderIds": ids})

	crossed := func() []map[string]any {
		return rows(t, call(t, "audit_provider", map[string]any{"library": "Movies", "checks": "ids", "types": "Movie"})["findings"], "findings")
	}
	found := crossed()
	if len(found) != 1 || str(found[0]["id"]) != blade {
		t.Fatalf("audit_provider ids = %v, want Blade Runner alone", found)
	}
	if ps := strs(t, found[0]["problems"], "problems"); len(ps) != 1 || !strings.Contains(ps[0], "whose IMDb id is tt0083658, not the "+alien+" it holds") {
		t.Errorf("problems = %v", ps)
	}
	holders := func() []string {
		var out []string
		for _, it := range rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "imdb", "id": alien})["items"], "items") {
			out = append(out, str(it["name"]))
		}
		slices.Sort(out)
		return out
	}
	if got := holders(); !slices.Contains(got, "Blade Runner") {
		t.Fatalf("imdb %s finds %v, want Blade Runner among them", alien, got)
	}

	idx := -1
	for i, c := range rows(t, call(t, "item_identify", map[string]any{"id": blade, "kind": "movie"})["candidates"], "candidates") {
		if ids, _ := c["metadata_provider_ids"].(map[string]any); idx < 0 && str(ids["tmdb"]) == "78" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("Blade Runner (tmdb 78) is not a candidate")
	}
	// the refresh the apply starts puts TMDB's IMDb id back. Jellyfin has run
	// it by the time it answers; Emby answers first and runs it a moment
	// later, and the tool waits for it, so its answer is the item as the
	// refresh left it on both
	applied := call(t, "item_identify_apply", map[string]any{"id": blade, "kind": "movie", "candidate": idx})
	if got, _ := applied["metadata_provider_ids"].(map[string]any); str(got["tmdb"]) != "78" || str(got["imdb"]) != "tt0083658" {
		t.Errorf("item_identify_apply = %v, want Blade Runner's own TMDB and IMDb ids", applied)
	}
	if ids := providerIDs(t, blade); str(ids["imdb"]) != "tt0083658" {
		t.Errorf("after the apply Blade Runner holds %v", ids)
	}

	if got := crossed(); len(got) != 0 {
		t.Errorf("after the apply audit_provider ids = %v", got)
	}
	// Alien, three times: the clean copy and the two messy ones
	if got := holders(); !slices.Equal(got, []string{"Alien", "Alien", "Alien"}) {
		t.Errorf("imdb %s finds %v, want the three Aliens alone", alien, got)
	}
}

// A film holding a series' id, put right where the library fetches nothing:
// audit_provider finds Memento carries Breaking Bad's IMDb id, item_identify
// offers Memento, and item_identify_apply sets its ids by a plain edit - the
// TMDB id the candidate carries and the IMDb id TMDB's record of the film
// adds, so the series' id is replaced rather than left - warning that the
// nfo beside the file may still name the old one. The audit comes back
// clean. A refresh then reads the nfo again: Emby's library saves no nfo,
// and the series' id is back, as warned; Jellyfin's wrote the new ids into
// it, and they hold. Once the nfo is put right too, the ids hold through a
// refresh on both.
func TestAFilmWithASeriesIDPutRight(t *testing.T) {
	const series, film = "tt0903747", "tt0209144"
	needsTMDBAnswers(t, []string{"/3/find/" + series}, []string{"/3/search/movie?", "query=Memento", "year=2000"}, []string{"/3/movie/77"}, []string{"/3/movie/77?", "append_to_response"})
	memento := findItem(t, "Messy Movies", "Movie", "Memento")
	nfo := filepath.Join(dataDir(), "messy-movies", messyCrossed, "movie.nfo")
	sidecar, err := os.ReadFile(nfo) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	ids := func() map[string]any { return providerIDs(t, memento) }
	holding := func(tmdb, imdb string) func() bool {
		return func() bool { got := ids(); return str(got["tmdb"]) == tmdb && str(got["imdb"]) == imdb }
	}
	if !holding("", series)() {
		t.Fatalf("Memento holds %v, want the series' IMDb id alone", ids())
	}
	original := fullItem(t, memento)["ProviderIds"]
	t.Cleanup(func() {
		mediaWrite(t, nfo, sidecar)
		updateItem(t, memento, map[string]any{"ProviderIds": original})
		if !eventually(holding("", series)) {
			t.Errorf("Memento was not put back to the series' id alone: %v", ids())
		}
	})

	problem := func() string {
		for _, f := range rows(t, call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "ids", "types": "Movie"})["findings"], "findings") {
			if str(f["id"]) == memento {
				return strings.Join(strs(t, f["problems"], "problems"), "; ")
			}
		}
		return ""
	}
	if p := problem(); !strings.Contains(p, "its IMDb id "+series+" is a series, not a film") {
		t.Fatalf("audit_provider says %q of Memento, want its series' id found", p)
	}

	idx := -1
	for i, c := range rows(t, call(t, "item_identify", map[string]any{"id": memento, "kind": "movie"})["candidates"], "candidates") {
		if cids, _ := c["metadata_provider_ids"].(map[string]any); idx < 0 && str(cids["tmdb"]) == "77" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("Memento (tmdb 77) is not a candidate")
	}
	// the candidate is a TMDB search result, which carries TMDB's id alone;
	// the server's provider, asked by that id, gives Memento's IMDb id, and
	// the edit sets both in place of the ids the film held
	applied := call(t, "item_identify_apply", map[string]any{"id": memento, "kind": "movie", "candidate": idx})
	if got, _ := applied["metadata_provider_ids"].(map[string]any); str(got["tmdb"]) != "77" || str(got["imdb"]) != film {
		t.Errorf("item_identify_apply = %v, want Memento's TMDB and IMDb ids", applied)
	}
	// and the note says the nfo beside the file may bring the old id back:
	// Emby's messy library saves no nfo, and a refresh reads it again;
	// Jellyfin's wrote the new ids into it and kept the old one beside them
	note := str(applied["note"])
	nfoSaid := "does not save nfo files, so a refresh reads the nfo beside the file (movie.nfo)"
	if isJellyfin() {
		nfoSaid = "wrote the new ids into the nfo beside the file (movie.nfo), but keeps any id there it had no new value for"
	}
	if !strings.Contains(note, "metadata fetchers off") || !strings.Contains(note, nfoSaid) {
		t.Errorf("the apply's note = %q, want it to say %q", note, nfoSaid)
	}
	if !holding("77", film)() {
		t.Errorf("after the apply Memento holds %v", ids())
	}
	if p := problem(); p != "" {
		t.Errorf("after the apply audit_provider still says %q", p)
	}
	// the new IMDb id written into the nfo where the library saves them
	nfoNamesFilm := func() bool {
		raw, err := os.ReadFile(nfo) //nolint:gosec // a fixture under the test data dir
		return err == nil && strings.Contains(string(raw), ">"+film+"<")
	}
	if isJellyfin() && !eventually(nfoNamesFilm) || !isJellyfin() && nfoNamesFilm() {
		t.Errorf("after the apply the nfo beside Memento names the film's own IMDb id: %v, want it to only where the library saves nfos", nfoNamesFilm())
	}

	// a refresh reads the nfo: Emby's still names the series, as warned, and
	// Jellyfin's the film
	call(t, "item_refresh", map[string]any{"id": memento})
	if isJellyfin() {
		if !holds(holding("77", film)) {
			t.Errorf("after a refresh Memento holds %v, want the ids the apply wrote into the nfo", ids())
		}
	} else if !eventually(func() bool { return str(ids()["imdb"]) == series }) {
		t.Errorf("after a refresh Memento holds %v, want the nfo's %s back as the note warned", ids(), series)
	}

	// the nfo put right as well - Memento's own IMDb id, and its TMDB id
	// added - and the ids hold through a refresh
	right := strings.ReplaceAll(string(sidecar), series, film)
	right = strings.Replace(right, "</movie>", "  <tmdbid>77</tmdbid>\n  <uniqueid type=\"tmdb\" default=\"true\">77</uniqueid>\n</movie>", 1)
	mediaWrite(t, nfo, []byte(right))
	updateItem(t, memento, map[string]any{"ProviderIds": map[string]any{"Tmdb": "77", "Imdb": film}})
	call(t, "item_refresh", map[string]any{"id": memento})
	if !holds(holding("77", film)) {
		t.Errorf("with the nfo put right a refresh leaves Memento holding %v", ids())
	}
	if p := problem(); p != "" {
		t.Errorf("with the nfo put right audit_provider says %q", p)
	}
}
