//go:build integration

package acceptance

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The provider tools ask the server to ask TMDB, which the proxy answers
// from the cassettes. A library with its fetchers off answers nothing, so
// these run against the clean libraries.

func TestItemIdentify(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Dune")
	out := call(t, "item_identify", map[string]any{"id": id, "kind": "movie"})
	if str(out["item"]) != "Dune" {
		t.Errorf("item = %v", out["item"])
	}
	cands := rows(t, out["candidates"], "candidates")
	if len(cands) < 2 {
		t.Fatalf("candidates = %v", cands)
	}
	// the 2021 film first, with its ids, then the 1984 one somewhere
	first := cands[0]
	ids, _ := first["metadata_provider_ids"].(map[string]any)
	if str(first["name"]) != "Dune" || num(t, first["year"], "year") != 2021 || str(ids["tmdb"]) != "438631" || num(t, first["index"], "index") != 0 {
		t.Errorf("first candidate = %v", first)
	}
	if str(first["search_provider"]) == "" {
		t.Errorf("no search provider on %v", first)
	}
	var lynch bool
	for _, c := range cands {
		if num(t, c["year"], "year") == 1984 {
			lynch = true
		}
	}
	if !lynch {
		t.Errorf("the 1984 Dune is not a candidate: %v", cands)
	}

	// with a name and year override, and as a series
	out = call(t, "item_identify", map[string]any{"id": id, "kind": "movie", "name": "Dune", "year": 1984})
	if cands = rows(t, out["candidates"], "candidates"); len(cands) == 0 || num(t, cands[0]["year"], "year") != 1984 {
		t.Errorf("year override = %v", cands)
	}
	sev := findItem(t, "Shows", "Series", "Severance")
	out = call(t, "item_identify", map[string]any{"id": sev, "kind": "series"})
	if cands = rows(t, out["candidates"], "candidates"); len(cands) == 0 || str(cands[0]["name"]) != "Severance" {
		t.Errorf("series candidates = %v", cands)
	}

	if msg := callErr(t, "item_identify", map[string]any{"id": id, "kind": "podcast"}); !strings.Contains(msg, `unsupported identify kind "podcast"`) {
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
	plot := str(call(t, "item_get", map[string]any{"id": id})["overview"])
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
	call(t, "item_edit", map[string]any{"ids": []any{id}, "overview": edited})

	out := call(t, "item_identify", map[string]any{"id": id, "kind": "movie", "year": 1997})
	cands := rows(t, out["candidates"], "candidates")
	idx := slices.IndexFunc(cands, func(c map[string]any) bool {
		ids, _ := c["metadata_provider_ids"].(map[string]any)
		return str(ids["tmdb"]) == "128"
	})
	if idx < 0 {
		t.Fatalf("Princess Mononoke (tmdb 128) is not among the candidates: %v", cands)
	}

	applied := call(t, "item_identify_apply", map[string]any{"id": id, "kind": "movie", "candidate": idx, "year": 1997})
	if !strings.HasPrefix(str(applied["applied"]), "Princess Mononoke (1997)") || applied["note"] != nil {
		t.Errorf("applied = %v", applied)
	}
	ids, _ := applied["metadata_provider_ids"].(map[string]any)
	if str(ids["tmdb"]) != "128" || str(ids["imdb"]) != "tt0119698" {
		t.Errorf("applied ids = %v", ids)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if !eventually(func() bool {
		got = call(t, "item_get", map[string]any{"id": id})
		return str(got["overview"]) != edited && strings.HasPrefix(str(got["overview"]), "Ashitaka, a prince")
	}) {
		t.Errorf("after the apply the plot is %q, want the provider's in place of the edit", got["overview"])
	}
	gotIDs, _ := got["metadata_provider_ids"].(map[string]any)
	if str(got["name"]) != "Princess Mononoke" || str(gotIDs["tmdb"]) != "128" || num(t, got["year"], "year") != 1997 {
		t.Errorf("after apply = %v", got)
	}

	if msg := callErr(t, "item_identify_apply", map[string]any{"id": id, "kind": "movie", "candidate": 99, "year": 1997}); !strings.Contains(msg, "candidate 99 out of range") {
		t.Errorf("a bad candidate: %s", msg)
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
		for _, img := range rows(t, call(t, "item_artwork", map[string]any{"id": item, "limit": 1})["current"], "current") {
			if str(img["ImageType"]) == "Primary" {
				return numOr0(img["Width"]), numOr0(img["Height"]), true
			}
		}
		return 0, 0, false
	}
	// The fixture's poster.jpg, 200x300, is what the set has to replace for
	// the 2x2 after it to mean anything, and what this test puts back. A
	// test that re-identifies the film can leave the provider's in its place
	// (Emby writes a downloaded poster over poster.jpg), so it is laid out
	// again first: every fixture poster is the one test pattern
	// (scripts/testenv.sh), and the messy copy's is never replaced.
	poster := filepath.Join(dataDir(), "movies", "Blade Runner (1982)", "poster.jpg")
	fixturePoster := fixture(t, "messy-movies/Blade Runner (1982)/poster.jpg")
	putBack := func() {
		mediaWrite(t, poster, fixturePoster)
		if _, err := invoke("item_refresh", map[string]any{"id": id}); err != nil {
			t.Errorf("refreshing Blade Runner: %v", err)
		}
		if !eventually(func() bool { w, h, ok := primary(id); return ok && w == 200 && h == 300 }) {
			w, h, _ := primary(id)
			t.Errorf("Blade Runner's poster is %dx%d, want the fixture's 200x300", w, h)
		}
		_ = waitForScan()
	}
	if w, h, ok := primary(id); !ok || w != 200 || h != 300 {
		t.Logf("Blade Runner's poster is %dx%d (held: %v): putting the fixture's back first", w, h, ok)
		putBack()
	}
	if w, h, ok := primary(id); !ok || w != 200 || h != 300 {
		t.Fatalf("Blade Runner's poster is %dx%d (held: %v), want the fixture's 200x300", w, h, ok)
	}
	out := call(t, "item_artwork", map[string]any{"id": id, "type": "Primary", "limit": 3})
	cands := rows(t, out["candidates"], "candidates")
	if len(cands) == 0 || len(cands) > 3 {
		t.Fatalf("candidates = %v", cands)
	}
	for _, c := range cands {
		if !strings.HasPrefix(str(c["url"]), "http") || str(c["provider"]) == "" {
			t.Errorf("candidate = %v", c)
		}
	}

	t.Cleanup(putBack)
	set := call(t, "item_artwork_set", map[string]any{"id": id, "url": str(cands[0]["url"]), "type": "Primary"})
	if str(set["set"]) != "Primary" {
		t.Errorf("item_artwork_set = %v", set)
	}
	_, statErr := os.Stat(poster)
	if isJellyfin() {
		if statErr != nil || set["removed"] != nil {
			t.Errorf("on Jellyfin the poster file beside the film: %v, and the answer says %v removed; want it kept", statErr, set["removed"])
		}
	} else if !os.IsNotExist(statErr) || str(set["removed"]) != "/media/movies/Blade Runner (1982)/poster.jpg" || !strings.Contains(str(set["note"]), "deleted") {
		t.Errorf("on Emby the poster file beside the film: %v, and the answer = %v; want it gone and named", statErr, set)
	}
	if !recording() && !eventually(func() bool { w, h, ok := primary(id); return ok && w == 2 && h == 2 }) {
		w, h, _ := primary(id)
		t.Errorf("after item_artwork_set the poster is %dx%d, want the replayed 2x2", w, h)
	}

	// and a type the item had none of: the messy copy, its fetchers off, has
	// only its poster.jpg, and takes the clean copy's backdrop
	backdrops := rows(t, call(t, "item_artwork", map[string]any{"id": id, "type": "Backdrop", "limit": 2})["candidates"], "candidates")
	if len(backdrops) == 0 {
		t.Fatal("no backdrop candidates")
	}
	var messy string
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "query": "Blade Runner"})["items"], "items") {
		messy = str(it["id"]) // either of Emby's two will do
	}
	types := func() []string {
		var out []string
		for _, img := range rows(t, call(t, "item_artwork", map[string]any{"id": messy, "limit": 1})["current"], "current") {
			out = append(out, str(img["ImageType"]))
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
	if set := call(t, "item_artwork_set", map[string]any{"id": messy, "url": str(backdrops[0]["url"]), "type": "Backdrop"}); str(set["set"]) != "Backdrop" {
		t.Errorf("item_artwork_set type Backdrop = %v", set)
	}
	if !eventually(func() bool { return slices.Contains(types(), "Backdrop") }) {
		t.Errorf("after item_artwork_set the messy copy's images are %v, want a Backdrop", types())
	}
}

// No subtitle provider is configured on a fresh server, so a search answers
// with nothing on both, in any language or none, and a download of nothing is
// refused.
func TestSubtitles(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Dune")
	for _, args := range []map[string]any{{"id": id, "language": "eng"}, {"id": id}} {
		if out := call(t, "item_subtitle_search", args); len(rows(t, out["candidates"], "candidates")) != 0 {
			t.Errorf("item_subtitle_search %v = %v, want nothing with no provider", args, out["candidates"])
		}
	}
	// a download of a subtitle no provider offered: Emby refuses it with a
	// 500, and Jellyfin answers 204 having downloaded nothing, which the tool
	// reads back for and refuses too
	msg := callErr(t, "item_subtitle_download", map[string]any{"id": id, "subtitle_id": "nope_nope"})
	want := "HTTP 500"
	if isJellyfin() {
		want = "no new subtitle reached Dune within ten seconds"
	}
	if !strings.Contains(msg, want) {
		t.Errorf("downloading a subtitle nobody offered: %s", msg)
	}
}
