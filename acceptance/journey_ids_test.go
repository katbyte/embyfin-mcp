//go:build integration

package acceptance

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Journeys through a title's identity: an unmatched show identified end to
// end, and a film holding another film's id put right.

// An unmatched show identified: the audit finds it, item_identify offers the
// real show, item_identify_apply gives it that show's ids, the audit lets go
// of it, and the episode audit, asking TMDB by the new id, now knows its run.
// The messy show library has its fetchers off, so the match sets the ids and
// nothing else, which is all the run needs.
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
		// the files re-read, the episodes numbered as they were
		call(t, "item_refresh", map[string]any{"id": tng})
		if !eventually(func() bool { return slices.Equal(numbers(), []int{1, 3}) }) {
			t.Errorf("after putting the nfos back the show's episodes are %v", numbers())
		}
	})

	applied := call(t, "item_identify_apply", map[string]any{"id": tng, "kind": "series", "candidate": idx, "name": name, "replace_all_images": true})
	if ids, _ := applied["metadata_provider_ids"].(map[string]any); str(ids["tmdb"]) != "655" {
		t.Fatalf("item_identify_apply = %v, want tmdb 655", applied)
	}
	if !strings.Contains(str(applied["note"]), "metadata fetchers off") {
		t.Errorf("item_identify_apply says nothing of the fetchers being off: %v", applied)
	}
	// with the fetchers off the ids are set by a plain edit, which changes
	// them and nothing else: the episodes keep the numbers their files and
	// nfos give them (the server's own apply refreshed the show, and on
	// Jellyfin that numbered every episode 0)
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
	out := call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows", "provider": true})
	for _, u := range rows(t, out["unknown"], "unknown") {
		if str(u["id"]) == tng {
			t.Errorf("the matched show's run is still unknown: %v", u)
		}
	}
	var detail string
	for _, f := range rows(t, out["findings"], "findings") {
		if str(f["id"]) == tng {
			detail = str(f["detail"])
		}
	}
	// the gap between its files, and TMDB's run past them
	if !strings.Contains(detail, "between the episodes on disk: S01E02") || !strings.Contains(detail, "listed by TMDB without a file: S01E02, S01E04") {
		t.Errorf("the matched show's finding = %q, want its gap and TMDB's run", detail)
	}
}

// A film given another film's IMDb id, the way a hand edit or a bad match
// leaves it: audit_provider finds the two ids disagree, item_identify and
// item_identify_apply put the right film back, the audit comes back clean,
// and the IMDb id finds only the film it belongs to.
func TestAMismatchedIDPutRight(t *testing.T) {
	needsTMDBCassette(t)
	blade := findItem(t, "Movies", "Movie", "Blade Runner")
	before := fullItem(t, blade)
	// the apply replaces what the nfo gave the film (its genres, its people)
	// with TMDB's, so the whole item goes back as it was
	t.Cleanup(func() { updateItem(t, blade, before) })
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
	if ids, _ := call(t, "item_get", map[string]any{"id": blade})["metadata_provider_ids"].(map[string]any); str(ids["imdb"]) != "tt0083658" {
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
