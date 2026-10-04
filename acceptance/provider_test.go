//go:build integration

package acceptance

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	acc "github.com/katbyte/embyfin-mcp/lib/acceptance"

	"github.com/katbyte/embyfin-mcp/lib/testenv"
)

// The provider tools ask the server to ask TMDB, which the proxy answers
// from the cassettes. A library with its fetchers off answers nothing, so
// these run against the clean libraries.

func TestItemIdentify(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Dune")
	out := suite.Call(t, "item_identify", map[string]any{"id": id, "kind": "movie"})
	if acc.Str(out["item"]) != "Dune" {
		t.Errorf("item = %v", out["item"])
	}
	cands := acc.Rows(t, out["candidates"], "candidates")
	if len(cands) < 2 {
		t.Fatalf("candidates = %v", cands)
	}
	// the 2021 film first, with its ids, then the 1984 one somewhere
	first := cands[0]
	ids, _ := first["metadata_provider_ids"].(map[string]any)
	if acc.Str(first["name"]) != "Dune" || acc.Num(t, first["year"], "year") != 2021 || acc.Str(ids["tmdb"]) != "438631" || acc.Num(t, first["index"], "index") != 0 {
		t.Errorf("first candidate = %v", first)
	}
	if acc.Str(first["search_provider"]) == "" {
		t.Errorf("no search provider on %v", first)
	}
	var lynch bool
	for _, c := range cands {
		if acc.Num(t, c["year"], "year") == 1984 {
			lynch = true
		}
	}
	if !lynch {
		t.Errorf("the 1984 Dune is not a candidate: %v", cands)
	}

	// with a name and year override, and as a series
	out = suite.Call(t, "item_identify", map[string]any{"id": id, "kind": "movie", "name": "Dune", "year": 1984})
	if cands = acc.Rows(t, out["candidates"], "candidates"); len(cands) == 0 || acc.Num(t, cands[0]["year"], "year") != 1984 {
		t.Errorf("year override = %v", cands)
	}
	sev := findItem(t, "Shows", "Series", "Severance")
	out = suite.Call(t, "item_identify", map[string]any{"id": sev, "kind": "series"})
	if cands = acc.Rows(t, out["candidates"], "candidates"); len(cands) == 0 || acc.Str(cands[0]["name"]) != "Severance" {
		t.Errorf("series candidates = %v", cands)
	}

	if msg := suite.CallErr(t, "item_identify", map[string]any{"id": id, "kind": "podcast"}); !strings.Contains(msg, `unsupported identify kind "podcast"`) {
		t.Errorf("an unsupported kind: %s", msg)
	}
}

// item_identify_apply re-identifies Princess Mononoke as itself, which
// re-fetches its metadata over what an edit left: the plot edited away is
// replaced by the provider's, the ids are the same, and the item is still
// the film. (On a fresh server the film holds its nfo's plot, which the
// cleanup puts back: the apply takes TMDB's over it.)
func TestItemIdentifyApply(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Princess Mononoke")
	plot := acc.Str(suite.Call(t, "item_get", map[string]any{"id": id})["overview"])
	if plot == "" {
		t.Fatal("the clean Princess Mononoke has no plot")
	}
	// the apply replaces what the nfo gave the film - its plot, genres and
	// people - with TMDB's, and the nfo Jellyfin saves beside it: the whole
	// item and its folder go back as they were, or a later run's exact
	// genre checks read TMDB's
	keepFiles(t, itemFolder(t, id))
	restoreLater(t, id)
	const edited = "Not the plot of any film: an edit for the apply to replace."
	suite.Call(t, "item_edit", map[string]any{"ids": []any{id}, "overview": edited})

	out := suite.Call(t, "item_identify", map[string]any{"id": id, "kind": "movie", "year": 1997})
	cands := acc.Rows(t, out["candidates"], "candidates")
	idx := slices.IndexFunc(cands, func(c map[string]any) bool {
		ids, _ := c["metadata_provider_ids"].(map[string]any)
		return acc.Str(ids["tmdb"]) == "128"
	})
	if idx < 0 {
		t.Fatalf("Princess Mononoke (tmdb 128) is not among the candidates: %v", cands)
	}

	applied := suite.Call(t, "item_identify_apply", withCandidateIDs(t, map[string]any{"id": id, "kind": "movie", "candidate": idx, "year": 1997}))
	// the one thing to note is on Emby, whose match deletes the poster beside
	// the film (keepFiles puts it back), and where a library saves nfos -
	// Jellyfin's does - that the nfo written over in place is not seen
	full := acc.Str(applied["note"])
	noted := beyondNfoUnseen(full)
	if isJellyfin() && noted != "" || !isJellyfin() && (!strings.HasSuffix(noted, "/poster.jpg from beside the media") || !posterNamed(applied["removed_beside_media"])) {
		t.Errorf("applied = %v, want a note only on Emby, of the poster it deleted", applied)
	}
	saves := acc.BoolOf(suite.Call(t, "library_get", map[string]any{"library": "Movies"})["saves_nfo"])
	if said := full != noted; said != saves {
		t.Errorf("the match's note speaks of the nfo written over: %v, want %v (the library saves nfos: %v): %q", said, saves, saves, full)
	}
	if !strings.HasPrefix(acc.Str(applied["applied"]), "Princess Mononoke (1997)") {
		t.Errorf("applied = %v", applied)
	}
	ids, _ := applied["metadata_provider_ids"].(map[string]any)
	if acc.Str(ids["tmdb"]) != "128" || acc.Str(ids["imdb"]) != "tt0119698" {
		t.Errorf("applied ids = %v", ids)
	}
	if err := suite.WaitForScan(); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if !acc.Eventually(func() bool {
		got = suite.Call(t, "item_get", map[string]any{"id": id})
		return acc.Str(got["overview"]) != edited && strings.HasPrefix(acc.Str(got["overview"]), "Ashitaka, a prince")
	}) {
		t.Errorf("after the apply the plot is %q, want the provider's in place of the edit", got["overview"])
	}
	gotIDs, _ := got["metadata_provider_ids"].(map[string]any)
	if acc.Str(got["name"]) != "Princess Mononoke" || acc.Str(gotIDs["tmdb"]) != "128" || acc.Num(t, got["year"], "year") != 1997 {
		t.Errorf("after apply = %v", got)
	}

	// a candidate the search no longer offers, and a kind that is not the
	// film's: refused, nothing applied
	if msg := suite.CallErr(t, "item_identify_apply", map[string]any{"id": id, "kind": "movie", "candidate": 99, "year": 1997, "candidate_ids": map[string]any{"tmdb": "999999999"}}); !strings.Contains(msg, "no candidate the search offers now carries") || !strings.Contains(msg, "nothing was changed") {
		t.Errorf("a candidate no longer offered: %s", msg)
	}
	if msg := suite.CallErr(t, "item_identify_apply", map[string]any{"id": id, "kind": "series", "candidate": 0, "candidate_ids": map[string]any{"tmdb": "128"}}); !strings.Contains(msg, "is a Movie, and kind series identifies a Series") {
		t.Errorf("a kind that is not the film's: %s", msg)
	}
	// and the answer carries what the film was, to put it back by
	if was, _ := applied["was"].(map[string]any); acc.Str(was["name"]) != "Princess Mononoke" {
		t.Errorf("was = %v", applied["was"])
	}
}

// The clean Blade Runner's poster, replaced by a provider's and put back.
// On replay a provider's image is the proxy's 2x2 placeholder, recorded
// images being elided, so the size says which poster is in place. Emby
// deletes the poster.jpg beside the film in taking the new one, which it
// keeps in its own metadata folder, and the answer names the file; Jellyfin
// keeps the file.
func TestItemArtwork(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Blade Runner")
	primary := func(item string) (w, h int, ok bool) {
		for _, img := range acc.Rows(t, suite.Call(t, "item_artwork", map[string]any{"id": item, "limit": 1})["current"], "current") {
			if acc.Str(img["ImageType"]) == "Primary" {
				return acc.NumOr0(img["Width"]), acc.NumOr0(img["Height"]), true
			}
		}
		return 0, 0, false
	}
	// The fixture's poster.jpg, 200x300, is what the set has to replace for
	// the 2x2 after it to mean anything, and what this test puts back. A
	// test that re-identifies the film can leave the provider's in its place
	// (Emby takes poster.jpg off the disk for the provider's image:
	// TestThePosterBesideAFilm), so it is laid out again first: every
	// fixture poster is the one test pattern
	// (scripts/testenv.sh), and the messy copy's is never replaced.
	poster := filepath.Join(testenv.DataDir(), "movies", "Blade Runner (1982)", "poster.jpg")
	fixturePoster := fixture(t, "messy-movies/Blade Runner (1982)/poster.jpg")
	putBack := func() {
		acc.MediaWrite(t, poster, fixturePoster)
		if _, err := suite.Invoke("item_refresh", map[string]any{"id": id}); err != nil {
			t.Errorf("refreshing Blade Runner: %v", err)
		}
		if !acc.Eventually(func() bool { w, h, ok := primary(id); return ok && w == 200 && h == 300 }) {
			w, h, _ := primary(id)
			t.Errorf("Blade Runner's poster is %dx%d, want the fixture's 200x300", w, h)
		}
		settleScan(t, suite.WaitForScan)
	}
	if w, h, ok := primary(id); !ok || w != 200 || h != 300 {
		t.Logf("Blade Runner's poster is %dx%d (held: %v): putting the fixture's back first", w, h, ok)
		putBack()
	}
	if w, h, ok := primary(id); !ok || w != 200 || h != 300 {
		t.Fatalf("Blade Runner's poster is %dx%d (held: %v), want the fixture's 200x300", w, h, ok)
	}
	out := suite.Call(t, "item_artwork", map[string]any{"id": id, "type": "Primary", "limit": 3})
	cands := acc.Rows(t, out["candidates"], "candidates")
	if len(cands) == 0 || len(cands) > 3 {
		t.Fatalf("candidates = %v", cands)
	}
	for _, c := range cands {
		if !strings.HasPrefix(acc.Str(c["url"]), "http") || acc.Str(c["provider"]) == "" {
			t.Errorf("candidate = %v", c)
		}
	}

	t.Cleanup(putBack)
	// the nfo beside the film, which neither server writes again for a new
	// image unless the library saves artwork beside the media
	// (TestArtworkSavedBesideTheMedia)
	keepFiles(t, filepath.Dir(poster))
	nfo := filepath.Join(filepath.Dir(poster), "movie.nfo")
	nfoBefore, err := os.ReadFile(nfo) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	set := suite.Call(t, "item_artwork_set", map[string]any{"id": id, "url": acc.Str(cands[0]["url"]), "type": "Primary"})
	if nfoAfter, err := os.ReadFile(nfo); err != nil || !bytes.Equal(nfoAfter, nfoBefore) { //nolint:gosec // same
		t.Errorf("after item_artwork_set the nfo beside the film changed (%v)", err)
	}
	if acc.Str(set["set"]) != "Primary" {
		t.Errorf("item_artwork_set = %v", set)
	}
	_, statErr := os.Stat(poster)
	if isJellyfin() {
		if now, err := os.ReadFile(poster); statErr != nil || err != nil || !bytes.Equal(now, fixturePoster) || set["removed"] != nil || set["replaced"] != nil { //nolint:gosec // same
			t.Errorf("on Jellyfin the poster file beside the film: %v %v, and the answer = %v; want it kept as it was, and neither removed nor written over", statErr, err, set)
		}
	} else if !os.IsNotExist(statErr) || acc.Str(set["removed"]) != "/media/movies/Blade Runner (1982)/poster.jpg" || !strings.Contains(acc.Str(set["note"]), "deleted") {
		t.Errorf("on Emby the poster file beside the film: %v, and the answer = %v; want it gone and named", statErr, set)
	}
	if !testenv.Recording() && !acc.Eventually(func() bool { w, h, ok := primary(id); return ok && w == 2 && h == 2 }) {
		w, h, _ := primary(id)
		t.Errorf("after item_artwork_set the poster is %dx%d, want the replayed 2x2", w, h)
	}

	// and a type the item had none of: the messy copy, its fetchers off, has
	// only its poster.jpg, and takes the clean copy's backdrop
	backdrops := acc.Rows(t, suite.Call(t, "item_artwork", map[string]any{"id": id, "type": "Backdrop", "limit": 2})["candidates"], "candidates")
	if len(backdrops) == 0 {
		t.Fatal("no backdrop candidates")
	}
	var messy string
	for _, it := range acc.Rows(t, suite.Call(t, "library_items", map[string]any{"library": "Messy Movies", "query": "Blade Runner"})["items"], "items") {
		messy = acc.Str(it["id"]) // either of Emby's two will do
	}
	types := func() []string {
		var out []string
		for _, img := range acc.Rows(t, suite.Call(t, "item_artwork", map[string]any{"id": messy, "limit": 1})["current"], "current") {
			out = append(out, acc.Str(img["ImageType"]))
		}
		return out
	}
	if got := types(); slices.Contains(got, "Backdrop") {
		t.Fatalf("the messy Blade Runner already has a backdrop: %v", got)
	}
	t.Cleanup(func() {
		if status, raw := api(t, http.MethodDelete, "/Items/"+messy+"/Images/Backdrop/0", "", nil); status/100 != 2 {
			t.Errorf("removing the backdrop: HTTP %d: %s", status, raw)
		}
	})
	if set := suite.Call(t, "item_artwork_set", map[string]any{"id": messy, "url": acc.Str(backdrops[0]["url"]), "type": "Backdrop"}); acc.Str(set["set"]) != "Backdrop" {
		t.Errorf("item_artwork_set type Backdrop = %v", set)
	}
	if !acc.Eventually(func() bool { return slices.Contains(types(), "Backdrop") }) {
		t.Errorf("after item_artwork_set the messy copy's images are %v, want a Backdrop", types())
	}
}

// No subtitle provider is configured on a fresh server, so a search answers
// with nothing on both, in any language or none, and a download of nothing is
// refused.
func TestSubtitles(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Dune")
	for _, args := range []map[string]any{{"id": id, "language": "eng"}, {"id": id}} {
		if out := suite.Call(t, "item_subtitle_search", args); len(acc.Rows(t, out["candidates"], "candidates")) != 0 {
			t.Errorf("item_subtitle_search %v = %v, want nothing with no provider", args, out["candidates"])
		}
	}
	// a download of a subtitle no provider offered: Emby refuses it with a
	// 500, and Jellyfin answers 204 having downloaded nothing, which the tool
	// reads back for and refuses too
	msg := suite.CallErr(t, "item_subtitle_download", map[string]any{"id": id, "subtitle_id": "nope_nope"})
	want := "HTTP 500"
	if isJellyfin() {
		want = "no new subtitle file appeared beside Dune and no new subtitle reached it within ten seconds"
	}
	if !strings.Contains(msg, want) {
		t.Errorf("downloading a subtitle nobody offered: %s", msg)
	}
}
