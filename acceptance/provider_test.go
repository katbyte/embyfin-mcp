//go:build integration

package acceptance

import (
	"net/http"
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

// item_identify_apply re-identifies Princess Mononoke as itself: the ids are the same
// afterwards and the metadata is re-fetched, which is what a real
// re-identification does with a different candidate.
func TestItemIdentifyApply(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Princess Mononoke")
	out := call(t, "item_identify", map[string]any{"id": id, "kind": "movie", "year": 1997})
	cands := rows(t, out["candidates"], "candidates")
	if len(cands) == 0 {
		t.Fatalf("no candidates for Princess Mononoke")
	}
	var idx int
	for i, c := range cands {
		ids, _ := c["metadata_provider_ids"].(map[string]any)
		if str(ids["tmdb"]) == "128" {
			idx = i
		}
	}

	applied := call(t, "item_identify_apply", map[string]any{"id": id, "kind": "movie", "candidate": idx, "year": 1997})
	if !strings.HasPrefix(str(applied["applied"]), "Princess Mononoke (1997)") {
		t.Errorf("applied = %v", applied)
	}
	ids, _ := applied["metadata_provider_ids"].(map[string]any)
	if str(ids["tmdb"]) != "128" {
		t.Errorf("applied ids = %v", ids)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
	got := call(t, "item_get", map[string]any{"id": id})
	gotIDs, _ := got["metadata_provider_ids"].(map[string]any)
	if str(got["name"]) != "Princess Mononoke" || str(gotIDs["tmdb"]) != "128" {
		t.Errorf("after apply = %v", got)
	}

	if msg := callErr(t, "item_identify_apply", map[string]any{"id": id, "kind": "movie", "candidate": 99, "year": 1997}); !strings.Contains(msg, "out of range") {
		t.Errorf("a bad candidate: %s", msg)
	}
}

func TestItemArtwork(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Blade Runner")
	// the Primary image it holds: on a fresh server the fixture's 200x300
	// poster.jpg
	primary := func(item string) (w, h int, ok bool) {
		for _, img := range rows(t, call(t, "item_artwork", map[string]any{"id": item, "limit": 1})["current"], "current") {
			if str(img["ImageType"]) == "Primary" {
				return numOr0(img["Width"]), numOr0(img["Height"]), true
			}
		}
		return 0, 0, false
	}
	if _, _, ok := primary(id); !ok {
		t.Fatal("Blade Runner holds no poster")
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

	// apply the first poster candidate: the server downloads it through the
	// proxy and keeps it in place of the poster.jpg. On replay the image is
	// the proxy's 2x2 placeholder, recorded images being elided, so the size
	// says the new one is in place
	set := call(t, "item_artwork_set", map[string]any{"id": id, "url": str(cands[0]["url"]), "type": "Primary"})
	if str(set["set"]) != "Primary" {
		t.Errorf("item_artwork_set = %v", set)
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
