//go:build integration

package acceptance

import (
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

	if msg := callErr(t, "item_identify", map[string]any{"id": id, "kind": "podcast"}); msg == "" {
		t.Error("an unsupported kind was accepted")
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
	out := call(t, "item_artwork", map[string]any{"id": id, "limit": 3})
	current := rows(t, out["current"], "current")
	var primary bool
	for _, img := range current {
		if str(img["ImageType"]) == "Primary" {
			primary = true
		}
	}
	if !primary {
		t.Errorf("no primary image among %v", current)
	}
	cands := rows(t, out["candidates"], "candidates")
	if len(cands) == 0 || len(cands) > 3 {
		t.Fatalf("candidates = %v", cands)
	}
	for _, c := range cands {
		if !strings.HasPrefix(str(c["url"]), "http") || str(c["provider"]) == "" {
			t.Errorf("candidate = %v", c)
		}
	}

	// backdrops too
	out = call(t, "item_artwork", map[string]any{"id": id, "type": "Backdrop", "limit": 2})
	if n := len(rows(t, out["candidates"], "candidates")); n == 0 {
		t.Error("no backdrop candidates")
	}

	// apply the first poster candidate: the server downloads it through the
	// proxy (a placeholder image on replay) and keeps it
	set := call(t, "item_artwork_set", map[string]any{"id": id, "url": str(cands[0]["url"])})
	if str(set["set"]) != "Primary" {
		t.Errorf("item_artwork_set = %v", set)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
	after := call(t, "item_artwork", map[string]any{"id": id, "limit": 1})
	primary = false
	for _, img := range rows(t, after["current"], "current") {
		if str(img["ImageType"]) == "Primary" {
			primary = true
		}
	}
	if !primary {
		t.Error("no primary image after item_artwork_set")
	}
}

// No subtitle provider is configured on a fresh server, so the search
// answers with nothing or with the provider's refusal; either way the
// tools answer rather than hang, and a download of nothing is refused.
func TestSubtitles(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Dune")
	out, err := invoke("item_subtitle_search", map[string]any{"id": id, "language": "eng"})
	switch {
	case err != nil:
		if !strings.Contains(strings.ToLower(err.Error()), "subtitle") && !strings.Contains(err.Error(), "HTTP") {
			t.Errorf("subtitle search failed oddly: %v", err)
		}
	default:
		if _, ok := out["candidates"].([]any); !ok {
			t.Errorf("candidates = %v", out["candidates"])
		}
	}
	// a download of nothing: Emby refuses it, Jellyfin answers 204 and
	// downloads nothing, so only the call is asserted
	if _, err := invoke("item_subtitle_download", map[string]any{"id": id, "subtitle_id": "nope_nope"}); err != nil && !strings.Contains(err.Error(), "HTTP") {
		t.Errorf("subtitle download failed oddly: %v", err)
	}
}
