//go:build integration

package acceptance

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Journeys through an identity changed: what a user's watch state does when
// a film takes another film's id, a film whose nfo names who it is, and a
// series re-matched with the server's own fetchers on.

// stateOf is a user's watched mark and favourite on an item.
func stateOf(t *testing.T, id, user string) (watched, favourite bool) {
	t.Helper()

	for _, u := range rows(t, call(t, "item_last_watched", map[string]any{"id": id})["users"], "users") {
		if str(u["user"]) == user {
			watched = boolOf(u["played"])
		}
	}
	for _, it := range rows(t, call(t, "library_items", map[string]any{"user": user, "watched": "favourite", "types": "Movie,Series,Episode", "limit": 100})["items"], "items") {
		favourite = favourite || str(it["id"]) == id
	}

	return watched, favourite
}

// candidateFor finds the first item_identify candidate carrying one of the
// ids given, by any provider: a search Jellyfin runs without the item is
// answered by OMDb, whose candidates carry the IMDb id alone.
func candidateFor(t *testing.T, args map[string]any, ids ...string) int {
	t.Helper()

	cands := rows(t, call(t, "item_identify", args)["candidates"], "candidates")
	for i, c := range cands {
		held, _ := c["metadata_provider_ids"].(map[string]any)
		for _, v := range held {
			if slices.Contains(ids, str(v)) {
				return i
			}
		}
	}
	t.Fatalf("no candidate carries any of %v: %v", ids, cands)

	return -1
}

// A film re-identified carries the watch state its new id carries, on Emby:
// Emby keeps a user's watched mark and favourite by the film's provider ids,
// so alice's watch of the messy Dune, held as Lynch's film, is not on it
// once it holds the 2021 film's ids - it reads the clean Dune's state there,
// favourite and unwatched - and comes back with the old ids, which kept it.
// Her counts, which are by film, lose the watch and see one favourite film
// where there were two. item_identify_apply's answer says so on Emby.
func TestReidentifyingMovesWatchState(t *testing.T) {
	messy := findItem(t, "Messy Movies", "Movie", "Dune")
	clean := findItem(t, "Movies", "Movie", "Dune")
	keepFiles(t, itemFolder(t, messy))
	// cleared once the old ids are back, not before: both servers hold
	// state under the ids an item had, and the old ids bring theirs back
	unmarkLater(t, "alice", messy, clean)
	before := restoreLater(t, messy)
	for _, id := range []string{messy, clean} {
		call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": false, "favourite": false})
	}
	stats := call(t, "user_stats", map[string]any{"user": "alice"})

	// alice watched Lynch's film and likes it, and likes the 2021 one unseen
	call(t, "item_set_state", map[string]any{"id": messy, "user": "alice", "watched": true, "favourite": true})
	call(t, "item_set_state", map[string]any{"id": clean, "user": "alice", "favourite": true})
	if w, f := stateOf(t, messy, "alice"); !w || !f {
		t.Fatalf("the messy Dune for alice: watched %v favourite %v, want both", w, f)
	}
	if w, f := stateOf(t, clean, "alice"); w || !f {
		t.Fatalf("the clean Dune for alice: watched %v favourite %v, want a favourite unwatched", w, f)
	}
	marked := call(t, "user_stats", map[string]any{"user": "alice"})
	if num(t, marked["movies_watched"], "movies_watched") != num(t, stats["movies_watched"], "movies_watched")+1 || num(t, marked["favourites"], "favourites") != num(t, stats["favourites"], "favourites")+2 {
		t.Fatalf("alice's counts went from %v to %v, want one more watched and two more favourites", stats, marked)
	}

	idx := candidateFor(t, map[string]any{"id": messy, "kind": "movie", "year": 2021}, "438631")
	out := call(t, "item_identify_apply", map[string]any{"id": messy, "kind": "movie", "candidate": idx, "year": 2021})
	if ids, _ := out["metadata_provider_ids"].(map[string]any); str(ids["tmdb"]) != "438631" {
		t.Fatalf("item_identify_apply = %v", out)
	}
	// the answer says the watch state moved with the ids, where it does
	const moved = "watched mark and favourite for the title it was matched to"
	if says := strings.Contains(str(out["note"]), moved); says == isJellyfin() {
		t.Errorf("item_identify_apply's note = %q: on Emby it should say the watch state follows the ids, and on Jellyfin not", out["note"])
	}

	after := call(t, "user_stats", map[string]any{"user": "alice"})
	watched, favourite := stateOf(t, messy, "alice")
	if isJellyfin() {
		// Jellyfin holds state under an item's ids as well, but answers from
		// a copy of each item's state it keeps in memory, which a match does
		// not clear: straight after, it has shown the item's own state on
		// one run and the clean Dune's on the next. Not pinned.
		t.Logf("after the match the messy Dune for alice: watched %v favourite %v; counts %v, were %v", watched, favourite, after, marked)
		return
	}
	// with the id: the 2021 film's state, which alice left a favourite and
	// unwatched, and her watch of Lynch's film counted nowhere
	if watched || !favourite {
		t.Errorf("after the match the messy Dune for alice: watched %v favourite %v, want the clean Dune's: a favourite, unwatched", watched, favourite)
	}
	if num(t, after["movies_watched"], "movies_watched") != num(t, marked["movies_watched"], "movies_watched")-1 || num(t, after["favourites"], "favourites") != num(t, marked["favourites"], "favourites")-1 {
		t.Errorf("after the match alice's counts are %v, were %v: want one watched and one favourite fewer", after, marked)
	}
	// the old ids back, and the old state with them: it was kept, not lost
	updateItem(t, messy, map[string]any{"ProviderIds": before["ProviderIds"]})
	if w, f := stateOf(t, messy, "alice"); !w || !f {
		t.Errorf("with Lynch's ids back the messy Dune for alice: watched %v favourite %v, want both again", w, f)
	}
}

// retag rewrites the provider ids an nfo names.
func retag(raw []byte, from, to map[string]string) []byte {
	s := string(raw)
	for k, v := range from {
		s = strings.ReplaceAll(s, v, to[k])
	}

	return []byte(s)
}

// A film matched while the nfo beside it names another film: the clean Dune,
// whose nfo names the 2021 film, given Lynch's film as its match. Both
// servers take the match and leave the nfo as it was - Emby's library saves
// no nfo, and Jellyfin 12.1 writes none for a match, though it saves one for
// an edit. A refresh then differs: Jellyfin keeps the match, and Emby reads
// the nfo again and puts the 2021 film back, as the apply's answer warns (and
// on Emby it warns that the watch state follows the ids). On Emby the match
// holds once the nfo names Lynch's film too. The 2021 film's match undoes it
// on both.
func TestMatchingAgainstAnNfo(t *testing.T) {
	dune := findItem(t, "Movies", "Movie", "Dune")
	folder := itemFolder(t, dune)
	keepFiles(t, folder)
	restoreLater(t, dune)
	nfo := filepath.Join(folder, "movie.nfo")
	original, err := os.ReadFile(nfo) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(original), ">438631<") {
		t.Fatalf("the clean Dune's nfo does not name the 2021 film: %s", original)
	}
	tmdb := func() string {
		got, _ := call(t, "item_get", map[string]any{"id": dune})["metadata_provider_ids"].(map[string]any)
		return str(got["tmdb"])
	}
	orig := call(t, "item_get", map[string]any{"id": dune})
	lynch := candidateFor(t, map[string]any{"id": dune, "kind": "movie", "year": 1984}, "841")
	matchLynch := func() {
		t.Helper()
		out := call(t, "item_identify_apply", map[string]any{"id": dune, "kind": "movie", "candidate": lynch, "year": 1984})
		if got, _ := out["metadata_provider_ids"].(map[string]any); str(got["tmdb"]) != "841" || num(t, out["year"], "year") != 1984 {
			t.Errorf("applying Lynch's film = %v", out)
		}
		// Emby's answer warns of the nfo the next refresh reads, and of the
		// watch state that follows the ids; Jellyfin's, whose match holds
		// and whose watch state stays, says nothing
		note := str(out["note"])
		if isJellyfin() && note != "" {
			t.Errorf("applying Lynch's film on Jellyfin: note %q", note)
		}
		if !isJellyfin() && (!strings.Contains(note, "Emby reads the nfo beside the file (movie.nfo) again at the next refresh") || !strings.Contains(note, "watched mark and favourite")) {
			t.Errorf("applying Lynch's film on Emby: note %q, want the nfo and the watch state warned of", note)
		}
		if got := tmdb(); got != "841" {
			t.Errorf("after the apply the film holds tmdb %s", got)
		}
	}

	matchLynch()
	if raw, err := os.ReadFile(nfo); err != nil || !bytes.Equal(raw, original) { //nolint:gosec // same
		t.Errorf("the apply changed the nfo: %v %.400s", err, raw)
	}
	if !refreshed(t, dune) {
		t.Fatal("the refresh never ran")
	}
	if isJellyfin() {
		if got := tmdb(); got != "841" {
			t.Errorf("after a refresh the film holds tmdb %s, want Lynch's 841 kept over the nfo", got)
		}
	} else {
		if got := tmdb(); got != "438631" {
			t.Errorf("after a refresh the film holds tmdb %s, want the nfo's 438631 back, as warned", got)
		}
		// the nfo made to agree, the match holds
		mediaWrite(t, nfo, retag(original, map[string]string{"tmdb": "438631", "imdb": "tt1160419"}, map[string]string{"tmdb": "841", "imdb": "tt0087182"}))
		matchLynch()
		if !refreshed(t, dune) {
			t.Fatal("the refresh never ran")
		}
		if got := tmdb(); got != "841" {
			t.Errorf("with the nfo naming Lynch's film, after a refresh the film holds tmdb %s", got)
		}
		mediaWrite(t, nfo, original)
	}

	// undone by the tools: the 2021 film's match
	back := candidateFor(t, map[string]any{"id": dune, "kind": "movie", "year": 2021}, "438631")
	call(t, "item_identify_apply", map[string]any{"id": dune, "kind": "movie", "candidate": back, "year": 2021})
	// the film as it was: its name, year, file and ids (the apply adds the
	// provider's other links beside them on Emby, and the tvdb id it finds)
	now := call(t, "item_get", map[string]any{"id": dune})
	for _, field := range []string{"name", "year", "path"} {
		if fmt.Sprint(now[field]) != fmt.Sprint(orig[field]) {
			t.Errorf("after the undo the film's %s is %v, was %v", field, now[field], orig[field])
		}
	}
	was, _ := orig["metadata_provider_ids"].(map[string]any)
	is, _ := now["metadata_provider_ids"].(map[string]any)
	for _, provider := range []string{"tmdb", "imdb"} {
		if str(is[provider]) != str(was[provider]) {
			t.Errorf("after the undo the film's %s id is %v, was %v", provider, is[provider], was[provider])
		}
	}
}

// A series re-matched with the fetchers on - the server's own apply, which
// refreshes the series and every episode under it: The Expanse given
// Breaking Bad's ids, identified and put right. Its episodes and seasons are
// what they were, file for file, and the tools that read its run from the
// provider read the right one again. With the fetchers off Jellyfin's apply
// once numbered every episode 0; with them on, nothing moves. The apply
// replaces the images too, and the series keeps a poster.
func TestReidentifyingASeries(t *testing.T) {
	needsTMDBCassette(t)
	id := findItem(t, "Shows", "Series", "The Expanse")
	keepFiles(t, filepath.Join(dataDir(), "shows", "The Expanse"))
	before := restoreLater(t, id)

	episodes := func() []string {
		var out []string
		for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": id})["episodes"], "episodes") {
			out = append(out, fmt.Sprintf("S%02dE%02d %s at %s", numOr0(e["season"]), numOr0(e["episode"]), str(e["title"]), str(e["path"])))
		}
		slices.Sort(out)
		return out
	}
	seasons := func() []string {
		var out []string
		for _, s := range rows(t, call(t, "show_seasons", map[string]any{"series_id": id})["seasons"], "seasons") {
			out = append(out, fmt.Sprintf("%d %s %s", numOr0(s["season"]), str(s["id"]), str(s["name"])))
		}
		slices.Sort(out)
		return out
	}
	missing := func() string {
		out := call(t, "show_missing", map[string]any{"series_id": id})
		return fmt.Sprint(out["source"], out["missing"])
	}
	expanse := func(tool string, args map[string]any) string {
		for _, f := range rowsOf(call(t, tool, args)["findings"]) {
			if str(f["id"]) == id {
				return str(f["detail"])
			}
		}
		return ""
	}
	images := func() []string {
		var out []string
		for _, img := range rows(t, call(t, "item_artwork", map[string]any{"id": id, "limit": 1})["current"], "current") {
			out = append(out, str(img["ImageType"]))
		}
		slices.Sort(out)
		return out
	}
	wantEpisodes, wantSeasons, wantMissing := episodes(), seasons(), missing()
	wantGaps := expanse("audit_missing_episodes", map[string]any{"library": "Shows", "provider": true})
	wantPaths := expanse("audit_file_path", map[string]any{"library": "Shows"})
	wantImages := images()
	if len(wantEpisodes) != 3 || !strings.HasPrefix(wantMissing, "tmdb") || !slices.Contains(wantImages, "Primary") {
		t.Fatalf("The Expanse before: episodes %v, missing %s, images %v", wantEpisodes, wantMissing, wantImages)
	}

	wrong := maps.Clone(object(t, before["ProviderIds"], "ProviderIds"))
	for k := range wrong {
		delete(wrong, k)
	}
	wrong["Tmdb"], wrong["Tvdb"], wrong["Imdb"] = "1396", "81189", "tt0903747"
	updateItem(t, id, map[string]any{"ProviderIds": wrong})

	idx := candidateFor(t, map[string]any{"id": id, "kind": "series", "name": "The Expanse"}, "63639")
	out := call(t, "item_identify_apply", map[string]any{"id": id, "kind": "series", "candidate": idx, "name": "The Expanse", "replace_all_images": true})
	if ids, _ := out["metadata_provider_ids"].(map[string]any); str(ids["tmdb"]) != "63639" {
		t.Fatalf("item_identify_apply = %v", out)
	}
	// from Breaking Bad's ids to The Expanse's: Emby's answer warns of the
	// show's nfo, read again at a refresh, and of the watch state that
	// follows the ids; Jellyfin's says nothing
	if note := str(out["note"]); isJellyfin() != (note == "") || !isJellyfin() && !strings.Contains(note, "(tvshow.nfo)") {
		t.Errorf("item_identify_apply's note = %q", note)
	}

	// the refresh reaches the episodes after the series: what they hold is
	// checked once it matches, and for a few seconds more
	same := func() bool { return slices.Equal(episodes(), wantEpisodes) && slices.Equal(seasons(), wantSeasons) }
	if !eventually(same) || !holds(same) {
		t.Errorf("after the match the episodes are %v and seasons %v, want %v and %v", episodes(), seasons(), wantEpisodes, wantSeasons)
	}
	if got := missing(); got != wantMissing {
		t.Errorf("after the match show_missing reads %s, want %s", got, wantMissing)
	}
	if got := expanse("audit_missing_episodes", map[string]any{"library": "Shows", "provider": true}); got != wantGaps {
		t.Errorf("after the match audit_missing_episodes says %q, want %q", got, wantGaps)
	}
	if got := expanse("audit_file_path", map[string]any{"library": "Shows"}); got != wantPaths {
		t.Errorf("after the match audit_file_path says %q, want %q", got, wantPaths)
	}
	if got := images(); !slices.Contains(got, "Primary") {
		t.Errorf("after the match with its images replaced the series has %v, want a poster still (had %v)", got, wantImages)
	}
}
