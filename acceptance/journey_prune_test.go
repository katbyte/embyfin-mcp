//go:build integration

package acceptance

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Journeys that prune: one copy of a film held twice, an item whose files are
// already gone, and one season of a show. Each checks the delete reached what
// it should and nothing else - on disk, on the server, and in what users and
// lists held - and takes what it staged away again.

// filesUnder reads every file under the folders, to hold them to later.
func filesUnder(t *testing.T, dirs ...string) map[string][]byte {
	t.Helper()

	out := map[string][]byte{}
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				raw, rerr := os.ReadFile(path) //nolint:gosec // a fixture under the test data dir
				if rerr != nil {
					t.Fatal(rerr)
				}
				out[path] = raw
			}
			return nil
		})
	}
	if len(out) == 0 {
		t.Fatalf("nothing on disk under %v", dirs)
	}

	return out
}

// stillOnDisk checks every file read by filesUnder is where it was, as it was.
func stillOnDisk(t *testing.T, files map[string][]byte, after string) {
	t.Helper()

	for path, raw := range files {
		if now, err := os.ReadFile(path); err != nil || !bytes.Equal(now, raw) { //nolint:gosec // a fixture under the test data dir
			t.Errorf("%s went or changed with %s: %v", strings.TrimPrefix(path, dataDir()), after, err)
		}
	}
}

// One film held twice, pruned to one: the audit finds the pair,
// quality_compare says which copy is worse, and item_delete takes it - its
// folder and nothing else. The copy kept is left whole on disk and on the
// server, alice's watch state and favourite on the film stay, her counts do
// not move, and the collection and playlist that held the pruned copy let go
// of it rather than taking the kept one in its place.
//
// The pair is the clean Aliens staged twice in the messy library, a 720p copy
// and a 360p one sharing its TMDB id. Emby shows the two as one film, and
// shows it by the copy it scanned first; Jellyfin shows both. Emby is where
// a delete could reach too far - the item read in a user's view lists every
// copy's file as its versions - so the worse copy is deleted both ways there:
// once as the copy Emby does not show, once as the one it does.
func TestPruningOneCopyOfAFilm(t *testing.T) {
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	dune := findItem(t, "Movies", "Movie", "Dune")
	messy := filepath.Join(dataDir(), "messy-movies")
	const (
		better = "Aliens (1986) 720p"
		worse  = "Aliens (1986) 360p"
	)

	for _, c := range []struct {
		name string
		// worseFirst scans the worse copy in before the better, which makes
		// it the one Emby shows for the film
		worseFirst bool
		list       string
	}{
		{"the copy Emby folds into the other", false, "Zzyzx Pruned Second"},
		{"the copy Emby shows for both", true, "Zzyzx Pruned First"},
	} {
		t.Run(c.name, func(t *testing.T) {
			have := movieCount(t, "Messy Movies")
			t.Cleanup(func() {
				for _, dir := range []string{better, worse} {
					if err := os.RemoveAll(filepath.Join(messy, dir)); err != nil {
						t.Error(err)
					}
				}
				if err := scanUntil("Messy Movies", have); err != nil {
					t.Error(err)
				}
			})
			// the better copy is the clean one as it is; the worse carries
			// the same nfo over a 360p rip
			stageBetter := func() {
				copyFixture(t, filepath.Join(dataDir(), "movies", "Aliens (1986)"), filepath.Join(messy, better))
			}
			stageWorse := func() {
				dir := filepath.Join(messy, worse)
				mediaMkdir(t, dir)
				mediaWrite(t, filepath.Join(dir, worse+".mp4"), fixtureVideo(t, "messy-movies", messyAlien, messyAlien+".mp4"))
				mediaWrite(t, filepath.Join(dir, "movie.nfo"), fixtureVideo(t, "movies", "Aliens (1986)", "movie.nfo"))
			}
			first, second := stageBetter, stageWorse
			if c.worseFirst {
				first, second = stageWorse, stageBetter
			}
			first()
			if err := scanUntil("Messy Movies", have+1); err != nil {
				t.Fatal(err)
			}
			second()
			if err := scanUntil("Messy Movies", have+2); err != nil {
				t.Fatal(err)
			}
			var keep, prune string
			for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "query": "Aliens", "limit": 50})["items"], "items") {
				switch {
				case strings.Contains(str(it["path"]), "/"+better+"/"):
					keep = str(it["id"])
				case strings.Contains(str(it["path"]), "/"+worse+"/"):
					prune = str(it["id"])
				}
			}
			if keep == "" || prune == "" {
				t.Fatalf("the two copies were not both scanned in: keep %q, prune %q", keep, prune)
			}
			if !isJellyfin() {
				shown := rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "query": "Aliens", "user": "alice"})["items"], "items")
				want := keep
				if c.worseFirst {
					want = prune
				}
				if len(shown) != 1 || str(shown[0]["id"]) != want {
					t.Fatalf("Emby shows the pair as %v, want the one film by %s", shown, want)
				}
			}

			// the audit finds the pair
			paired := func() bool {
				if !isJellyfin() {
					for _, f := range rowsOf(call(t, "audit_multiple_versions", map[string]any{"library": "Messy Movies"})["findings"]) {
						if title(str(f["name"])) == "Aliens" {
							return strings.HasPrefix(str(f["detail"]), "2 versions: ")
						}
					}
					return false
				}
				groups, _ := call(t, "audit_duplicates", map[string]any{"library": "Messy Movies"})["groups"].([]any)
				for _, g := range groups {
					if group := rowsOf(g); len(group) > 0 && title(str(group[0]["name"])) == "Aliens" {
						return len(group) == 2
					}
				}
				return false
			}
			if !paired() {
				t.Fatal("the audit does not find the two copies")
			}
			// and quality_compare says which to keep
			if v := call(t, "quality_compare", map[string]any{"a": map[string]any{"item_id": prune}, "b": map[string]any{"item_id": keep}}); str(v["verdict"]) != "b_better" || str(v["decided_by"]) != "resolution" {
				t.Fatalf("quality_compare = %v by %v, want the 720p copy better by resolution", v["verdict"], v["decided_by"])
			}

			// alice has watched the film and made it a favourite, on both
			// copies (Emby marks every copy of a film at once); the state a
			// run before this one left on the same paths is cleared first
			unmarkLater(t, "alice", keep)
			for _, id := range []string{prune, keep} {
				call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": false, "favourite": false})
			}
			stats := call(t, "user_stats", map[string]any{"user": "alice"})
			for _, id := range []string{prune, keep} {
				call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": true, "favourite": true})
			}
			marked := call(t, "user_stats", map[string]any{"user": "alice"})
			for _, field := range []string{"movies_watched", "favourites"} {
				if num(t, marked[field], field) != num(t, stats[field], field)+1 {
					t.Fatalf("alice's %s went from %v to %v marking the film, want one more", field, stats[field], marked[field])
				}
			}
			// the pruned copy in a collection and a playlist, each beside
			// another film
			pl := str(call(t, "playlist_create", map[string]any{"name": c.list, "item_ids": []any{prune, dune}, "media_type": "Video"})["id"])
			deleteLater(t, "playlist_delete", "playlist", pl)
			col := str(call(t, "collection_create", map[string]any{"name": c.list, "item_ids": []any{prune, arrival}})["id"])
			deleteLater(t, "collection_delete", "collection", col)
			if n := collectionSize(t, col, 2); n != 2 {
				t.Fatalf("the collection holds %d, want 2", n)
			}

			// the refusal names the worse copy's folder, and nothing of the
			// better's or the clean library's
			server := "/media/messy-movies/" + worse
			msg := callErr(t, "item_delete", map[string]any{"id": prune})
			if !strings.Contains(msg, "would remove the folder "+server+" with everything in it") || strings.Contains(msg, better) || strings.Contains(msg, "/media/movies/") {
				t.Errorf("the refusal names more than the worse copy's folder: %s", msg)
			}
			kept := filesUnder(t, filepath.Join(messy, better), filepath.Join(dataDir(), "movies", "Aliens (1986)"))
			out := call(t, "item_delete", map[string]any{"id": prune, "confirm": true})
			got := removedPaths(t, out)
			if !slices.Contains(got, server+"/") || !slices.Contains(got, server+"/"+worse+".mp4") {
				t.Errorf("removed = %v, want the worse copy's folder and file", got)
			}
			for _, p := range got {
				if !strings.HasPrefix(p, server+"/") {
					t.Errorf("removed names %s, outside the worse copy's folder", p)
				}
			}
			stillOnDisk(t, kept, "the worse copy's delete")
			if _, err := os.Stat(filepath.Join(messy, worse)); !os.IsNotExist(err) {
				t.Errorf("the worse copy's folder is still on disk: %v", err)
			}
			if err := waitForItems("Messy Movies", have+1); err != nil {
				t.Fatal(err)
			}

			// the film is alice's as it was, on the copy kept
			if msg := callErr(t, "item_get", map[string]any{"id": prune}); !strings.Contains(msg, "no item") {
				t.Errorf("item_get of the pruned copy: %s", msg)
			}
			played := false
			for _, u := range rows(t, call(t, "item_last_watched", map[string]any{"id": keep})["users"], "users") {
				played = played || (str(u["user"]) == "alice" && boolOf(u["played"]))
			}
			if !played {
				t.Error("after the delete alice has not watched the copy kept")
			}
			favourite := false
			for _, it := range rows(t, call(t, "library_items", map[string]any{"user": "alice", "watched": "favourite", "limit": 100})["items"], "items") {
				favourite = favourite || str(it["id"]) == keep
			}
			if !favourite {
				t.Error("after the delete the copy kept is not among alice's favourites")
			}
			after := call(t, "user_stats", map[string]any{"user": "alice"})
			for _, field := range []string{"movies_watched", "favourites"} {
				if num(t, after[field], field) != num(t, marked[field], field) {
					t.Errorf("alice's %s went from %v to %v with the delete, want it unchanged", field, marked[field], after[field])
				}
			}
			// the lists let go of the copy, and do not take the kept one
			if got := names(t, call(t, "playlist_get", map[string]any{"playlist": pl})["entries"], "entries"); !slices.Equal(got, []string{"Dune"}) {
				t.Errorf("after the delete the playlist is %v, want [Dune]", got)
			}
			if got := names(t, call(t, "collection_get", map[string]any{"collection": col})["items"], "items"); !slices.Equal(got, []string{"Arrival"}) {
				t.Errorf("after the delete the collection holds %v, want [Arrival]", got)
			}
			if paired() {
				t.Error("after the delete the audit still finds the pair")
			}

			// and a scan brings nothing back
			if err := scanUntil("Messy Movies", have+1); err != nil {
				t.Fatal(err)
			}
			var copies []string
			for _, it := range rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "679"})["items"], "items") {
				copies = append(copies, hostPath(str(it["path"])))
			}
			slices.Sort(copies)
			if want := []string{filepath.Join(messy, better, better+".mp4"), filepath.Join(dataDir(), "movies", "Aliens (1986)", "Aliens (1986).mp4")}; !slices.Equal(copies, sorted(want)) {
				t.Errorf("after a scan tmdb 679 is held at %v, want the copy kept and the clean one", copies)
			}
		})
	}
}

// An item whose files someone already took off the disk: the server still
// holds it, the delete says it can find no folder and will remove the record
// alone, and with confirm it does - nothing on disk goes, and nothing else in
// the library.
func TestDeletingWhatIsAlreadyGone(t *testing.T) {
	messy := filepath.Join(dataDir(), "messy-movies")
	const name = "The Lord of the Rings The Two Towers (2002)"
	dir := filepath.Join(messy, name)
	have := movieCount(t, "Messy Movies")
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
		if err := scanUntil("Messy Movies", have); err != nil {
			t.Error(err)
		}
	})
	mediaMkdir(t, dir)
	mediaWrite(t, filepath.Join(dir, name+".mp4"), fixtureVideo(t, "movies", "Arrival (2016)", "Arrival (2016).mp4"))
	if err := scanUntil("Messy Movies", have+1); err != nil {
		t.Fatal(err)
	}
	var id string
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "limit": 50})["items"], "items") {
		if strings.Contains(str(it["path"]), "/"+name+"/") {
			id = str(it["id"])
		}
	}
	if id == "" {
		t.Fatal("the staged film was not scanned in")
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	rest := filesUnder(t, messy)
	const gone = "the server cannot find the folder holding the item: the delete removes its record, and nothing on disk"
	if msg := callErr(t, "item_delete", map[string]any{"id": id}); !strings.Contains(msg, "nothing was deleted") || !strings.Contains(msg, gone) {
		t.Errorf("the refusal: %s", msg)
	}
	out := call(t, "item_delete", map[string]any{"id": id, "confirm": true})
	if got := removedPaths(t, out); len(got) != 0 || !strings.HasPrefix(str(out["note"]), gone) || !strings.Contains(str(out["deleted"]), name) {
		t.Errorf("item_delete = %v", out)
	}
	if msg := callErr(t, "item_get", map[string]any{"id": id}); !strings.Contains(msg, "no item") {
		t.Errorf("item_get after the delete: %s", msg)
	}
	stillOnDisk(t, rest, "the record's delete")
	if n := movieCount(t, "Messy Movies"); n != have {
		t.Errorf("the library holds %d films, want the %d it held before the staging", n, have)
	}
}

// One season of a show deleted: its folder goes with every episode and nfo
// in it, and the show, its other season and their files stay. The show is
// the messy Deep Space Nine - seasons 1 and 3, no ids - copied into a
// library of its own, so nothing a delete could reach is a fixture.
func TestDeletingASeason(t *testing.T) {
	root := filepath.Join(dataDir(), "seasons")
	const show = "Star Trek Deep Space Nine (1993)"
	copyTree(t, filepath.Join(dataDir(), "messy-shows", show), filepath.Join(root, show))
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	const library = "Zzyzx Seasons"
	t.Cleanup(func() {
		if err := retried("library_delete", map[string]any{"library": library, "confirm": true}); err != nil {
			t.Errorf("removing the library: %v", err)
		}
		if err := waitForExpectedScan(); err != nil {
			t.Error(err)
		}
	})
	// no nfos saved, so what is on disk is what was staged
	call(t, "library_create", map[string]any{"name": library, "type": "tvshows", "paths": []any{"/media/seasons"}, "scan": true, "save_nfo": false})
	var series string
	episodes := func() []string {
		var out []string
		for _, e := range rowsOf(call(t, "library_episodes", map[string]any{"series_id": series})["episodes"]) {
			out = append(out, str(e["path"]))
		}
		slices.Sort(out)
		return out
	}
	if !eventuallyWithin(scanPatience, func() bool {
		out, err := invoke("library_items", map[string]any{"library": library, "types": "Series"})
		if items := rowsOf(out["items"]); err == nil && len(items) == 1 {
			series = str(items[0]["id"])
		}
		return series != "" && len(episodes()) == 2
	}) {
		t.Fatalf("the library never held the show and its two episodes: series %q", series)
	}
	if err := waitForExpectedScan(); err != nil {
		t.Fatal(err)
	}
	seasons := map[int]string{}
	for _, s := range rows(t, call(t, "show_seasons", map[string]any{"series_id": series})["seasons"], "seasons") {
		seasons[num(t, s["season"], "season")] = str(s["id"])
	}
	if len(seasons) != 2 || seasons[1] == "" || seasons[3] == "" {
		t.Fatalf("show_seasons = %v, want seasons 1 and 3", seasons)
	}

	server := "/media/seasons/" + show + "/Season 03"
	if msg := callErr(t, "item_delete", map[string]any{"id": seasons[3]}); !strings.Contains(msg, "would remove the folder "+server+" with everything in it") || strings.Contains(msg, "Season 01") {
		t.Errorf("the refusal for a season: %s", msg)
	}
	kept := filesUnder(t, filepath.Join(root, show, "Season 01"))
	kept[filepath.Join(root, show, "tvshow.nfo")] = fixtureVideo(t, "seasons", show, "tvshow.nfo")
	out := call(t, "item_delete", map[string]any{"id": seasons[3], "confirm": true})
	got := removedPaths(t, out)
	for _, want := range []string{server + "/", server + "/Star Trek Deep Space Nine S03E01.mp4", server + "/Star Trek Deep Space Nine S03E01.nfo"} {
		if !slices.Contains(got, want) {
			t.Errorf("removed = %v, want %s among them", got, want)
		}
	}
	for _, p := range got {
		if !strings.HasPrefix(p, server+"/") {
			t.Errorf("removed names %s, outside the season's folder", p)
		}
	}
	stillOnDisk(t, kept, "the season's delete")

	// the show holds the season left, now and after a scan
	want := []string{"/media/seasons/" + show + "/Season 01/Star Trek Deep Space Nine S01E01.mp4"}
	for _, when := range []string{"after the delete", "after a scan"} {
		if when == "after a scan" {
			scanUntilTrue(t, library, func() bool { return slices.Equal(episodes(), want) })
		}
		if got := episodes(); !slices.Equal(got, want) {
			t.Errorf("%s the show holds %v, want %v", when, got, want)
		}
		if got := call(t, "item_get", map[string]any{"id": series}); str(got["name"]) != "Star Trek: Deep Space Nine" {
			t.Errorf("%s the show is %v", when, got["name"])
		}
		var left []int
		for _, s := range rows(t, call(t, "show_seasons", map[string]any{"series_id": series})["seasons"], "seasons") {
			left = append(left, num(t, s["season"], "season"))
		}
		if !slices.Equal(left, []int{1}) {
			t.Errorf("%s the show's seasons are %v, want [1]", when, left)
		}
	}
}
