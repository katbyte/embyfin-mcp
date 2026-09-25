//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A copy of the messy Princess Mononoke in a folder named for its original,
// Japanese title, its nfo giving that as the original title: the path names
// the film, which audit_file_path used to report as another. A year one off
// is still the film; two off is not, and TMDB, asked by the path's title and
// year, finds the film itself, so the item's year is the one to check.
func TestAFilmInItsOwnLanguagesFolder(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	dir := filepath.Join(dataDir(), "messy-movies", "もののけ姫 (1997)")
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		if err := scanUntil("Messy Movies", messyMovies()); err != nil {
			t.Error(err)
		}
	})
	stageFile(t, filepath.Join(dir, "もののけ姫 (1997).mp4"), fixtureVideo(t, "messy-movies", messyMononoke, messyMononoke+".mp4"))
	stageFile(t, filepath.Join(dir, "movie.nfo"), []byte(`<?xml version="1.0" encoding="utf-8"?>
<movie>
  <title>Princess Mononoke</title>
  <originaltitle>もののけ姫</originaltitle>
  <year>1997</year>
  <tmdbid>128</tmdbid>
  <uniqueid type="tmdb" default="true">128</uniqueid>
  <imdbid>tt0119698</imdbid>
  <uniqueid type="imdb">tt0119698</uniqueid>
</movie>
`))
	if err := scanUntil("Messy Movies", messyMovies()+1); err != nil {
		t.Fatal(err)
	}
	var staged string
	for _, id := range itemsTitled(t, "Messy Movies", "Movie", "Princess Mononoke") {
		if strings.Contains(str(call(t, "item_get", map[string]any{"id": id})["path"]), "もののけ姫") {
			staged = id
		}
	}
	if staged == "" {
		t.Fatal("the copy in the Japanese folder is not listed")
	}

	got := call(t, "item_get", map[string]any{"id": staged})
	if str(got["original_title"]) != "もののけ姫" || got["warning"] != nil {
		t.Errorf("item_get = original_title %v, warning %v, want its original title and no warning", got["original_title"], got["warning"])
	}
	check := func() map[string]any {
		t.Helper()
		out := call(t, "audit_file_path", map[string]any{"ids": []any{staged}})
		if n := num(t, out["items_scanned"], "items_scanned"); n != 1 {
			t.Fatalf("audit_file_path by id scanned %d items", n)
		}
		return out
	}
	if out := check(); num(t, out["total_findings"], "total_findings") != 0 {
		t.Errorf("a folder in the film's own language = %v", out["findings"])
	}

	t.Cleanup(func() { _, _ = invoke("item_edit", map[string]any{"ids": []any{staged}, "year": 1997}) })
	call(t, "item_edit", map[string]any{"ids": []any{staged}, "year": 1998})
	if out := check(); num(t, out["total_findings"], "total_findings") != 0 {
		t.Errorf("a year one off = %v", out["findings"])
	}
	if w := call(t, "item_get", map[string]any{"id": staged})["warning"]; w != nil {
		t.Errorf("item_get with the year one off warned: %v", w)
	}

	needsTMDBRecording(t, searchKey("query=%E3%82%82%E3%81%AE%E3%81%AE%E3%81%91%E5%A7%AB&year=1997"))
	call(t, "item_edit", map[string]any{"ids": []any{staged}, "year": 1999})
	row := rows(t, check()["findings"], "findings")
	if len(row) != 1 || !slices.Equal(strs(t, row[0]["problems"], "problems"), []string{"year: path says 1997, metadata says 1999"}) {
		t.Fatalf("a year two off = %v", row)
	}
	if str(row[0]["item_tmdb"]) != "128" || row[0]["path_tmdb"] != nil || !strings.Contains(str(row[0]["diagnosis"]), "TMDB's search finds this very film, TMDB 128, by the path's title and year") {
		t.Errorf("the TMDB diagnosis = %v", row[0])
	}
	if w := str(call(t, "item_get", map[string]any{"id": staged})["warning"]); !strings.Contains(w, `named for "もののけ姫" (1997), not Princess Mononoke (1999)`) {
		t.Errorf("item_get with the year two off = %q", w)
	}
}

// The messy Arrival renamed with a Cyrillic A (U+0410) where the Latin A belongs: it
// reads right, and a search for Arrival does not find it. audit_file_path
// names the letter and the plain spelling, and nothing else about it.
func TestALookalikeLetterInAName(t *testing.T) {
	arrival := findItem(t, "Messy Movies", "Movie", "Arrival")
	lookalike := "\u0410rrival"
	t.Cleanup(func() {
		if _, err := invoke("item_edit", map[string]any{"ids": []any{arrival}, "name": "Arrival"}); err != nil {
			t.Error(err)
		}
	})
	call(t, "item_edit", map[string]any{"ids": []any{arrival}, "name": lookalike})

	out := call(t, "audit_file_path", map[string]any{"ids": []any{arrival}})
	found := rows(t, out["findings"], "findings")
	if len(found) != 1 {
		t.Fatalf("audit_file_path = %v", found)
	}
	problems := strs(t, found[0]["problems"], "problems")
	if len(problems) != 1 || !strings.HasPrefix(problems[0], "lookalike: \"\u0410rrival\" is spelled with the Cyrillic \u0410 (U+0410) in place of the Latin A") || !strings.Contains(problems[0], `item_edit name "Arrival" puts it right`) {
		t.Errorf("problems = %v", problems)
	}

	// what the row is about: the plain title finds it no more, the
	// lookalike does
	search := func(query string) bool {
		for _, row := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "query": query})["items"], "items") {
			if str(row["id"]) == arrival {
				return true
			}
		}
		return false
	}
	if search("Arrival") || !search(lookalike) {
		t.Errorf("a search for Arrival finds the renamed film %v, for %s %v: want only the lookalike to", search("Arrival"), lookalike, search(lookalike))
	}

	call(t, "item_edit", map[string]any{"ids": []any{arrival}, "name": "Arrival"})
	if n := num(t, call(t, "audit_file_path", map[string]any{"ids": []any{arrival}})["total_findings"], "total_findings"); n != 0 {
		t.Errorf("renamed back, audit_file_path still finds %d", n)
	}
}
