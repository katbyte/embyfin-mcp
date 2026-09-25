//go:build integration

package acceptance

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// What item_delete takes off the disk, for the shapes TestHowFarADeleteReaches
// does not stage: a film whose file or folder has already gone, two films
// sharing one folder, a film loose in a library's own folder, and what the
// refusal names for the lasting fixtures, none of which is ever confirmed.

// treeOf reads every folder and file under root, a folder ending in "/", so
// a test can hold the disk to what it was. skip leaves those paths, and
// whatever is under them, out.
func treeOf(t *testing.T, root string, skip ...string) map[string][]byte {
	t.Helper()

	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case slices.Contains(skip, path) && d.IsDir():
			return filepath.SkipDir
		case slices.Contains(skip, path):
		case d.IsDir():
			out[path+"/"] = nil
		default:
			raw, rerr := os.ReadFile(path) //nolint:gosec // a fixture under the test data dir
			if rerr != nil {
				return rerr
			}
			out[path] = raw
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return out
}

// sameTree fails for every path that changed, went or appeared between two
// reads of a tree.
func sameTree(t *testing.T, root string, before, after map[string][]byte) {
	t.Helper()

	for path, raw := range before {
		if now, ok := after[path]; !ok || !bytes.Equal(now, raw) {
			t.Errorf("%s changed or went", strings.TrimPrefix(path, root))
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s appeared", strings.TrimPrefix(path, root))
		}
	}
}

// wouldRemove reads what item_delete's refusal says it would take: the
// folder it would take whole and the names under it, or, when it keeps the
// folder, the files. The names are sorted.
func wouldRemove(t *testing.T, msg string) (folder string, names []string) {
	t.Helper()

	const whole, files = "it would remove the folder ", "it would remove "
	switch {
	case strings.Contains(msg, whole):
		rest := msg[strings.Index(msg, whole)+len(whole):]
		folder, rest, _ = strings.Cut(rest, " with everything in it (")
		names = strings.Split(strings.TrimSuffix(rest, ")"), ", ")
	case strings.Contains(msg, files):
		rest, _, _ := strings.Cut(msg[strings.Index(msg, files)+len(files):], ". Of those, ")
		names = strings.Split(rest, ", ")
	default:
		t.Fatalf("the refusal does not say what it would remove: %s", msg)
	}
	slices.Sort(names)

	return folder, names
}

// notOwn is what a refusal to delete says is not the item's own among what
// it would remove: the sidecars the server takes only because their names
// begin with the item's file name.
func notOwn(msg string) []string {
	_, rest, found := strings.Cut(msg, ". Of those, ")
	if !found {
		return nil
	}
	rest, _, _ = strings.Cut(rest, " are not this item's own")
	names := strings.Split(rest, ", ")
	slices.Sort(names)

	return names
}

// A film the disk has already lost part of: its file, or its whole folder.
// The item is still on the server until something deletes it, which is the
// clean-up a library that has had files moved out from under it needs.
// Primer, staged with its nfo and a poster.
func TestItemDelete(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	have := movieCount(t, "Messy Movies")
	const name = "Primer (2004)"
	dir := filepath.Join(dataDir(), "messy-movies", name)
	server := "/media/messy-movies/" + name
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		if err := scanUntil("Messy Movies", have); err != nil {
			t.Error(err)
		}
	})
	stage := func(t *testing.T) string {
		t.Helper()
		mediaMkdir(t, dir)
		mediaWrite(t, filepath.Join(dir, name+".mp4"), fixtureVideo(t, "messy-movies", messyArrival, messyArrival+".mp4"))
		mediaWrite(t, filepath.Join(dir, "movie.nfo"), movieNfo("Primer", 2004, "14337", "tt0390384"))
		mediaWrite(t, filepath.Join(dir, "poster.jpg"), fixtureVideo(t, "messy-movies", messyBladeRunner, "poster.jpg"))
		if err := scanUntil("Messy Movies", have+1); err != nil {
			t.Fatal(err)
		}
		id := findItem(t, "Messy Movies", "Movie", "Primer")
		// the nfo was read: this is Primer, not a copy of the file it was
		// made from
		if ids, _ := call(t, "item_get", map[string]any{"id": id})["metadata_provider_ids"].(map[string]any); str(ids["tmdb"]) != "14337" {
			t.Fatalf("the staged film holds ids %v, want Primer's tmdb 14337", ids)
		}
		return id
	}

	// the file gone, the folder left with its nfo and poster: the delete
	// takes what is left of the folder, and says so without the file
	t.Run("its file gone", func(t *testing.T) {
		id := stage(t)
		if err := os.Remove(filepath.Join(dir, name+".mp4")); err != nil {
			t.Fatal(err)
		}
		msg := callErr(t, "item_delete", map[string]any{"id": id})
		if folder, names := wouldRemove(t, msg); folder != server || !slices.Equal(names, []string{"movie.nfo", "poster.jpg"}) {
			t.Errorf("the refusal would take %s %v, want the folder with its nfo and poster: %s", folder, names, msg)
		}
		if _, err := os.Stat(filepath.Join(dir, "movie.nfo")); err != nil {
			t.Fatalf("the refused delete removed the nfo: %v", err)
		}

		out := call(t, "item_delete", map[string]any{"id": id, "confirm": true})
		if got, want := removedPaths(t, out), []string{server + "/", server + "/movie.nfo", server + "/poster.jpg"}; !slices.Equal(got, want) {
			t.Errorf("removed = %v, want %v", got, want)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			left, _ := os.ReadDir(dir)
			t.Errorf("the folder is still on disk, holding %v", left)
		}
		if msg := callErr(t, "item_get", map[string]any{"id": id}); !strings.Contains(msg, "no item") {
			t.Errorf("item_get after the delete: %s", msg)
		}
		if err := waitForItems("Messy Movies", have); err != nil {
			t.Error(err)
		}
	})

	// the whole folder gone: the delete takes the server's record and
	// nothing on disk, and says so before and after
	t.Run("its folder gone", func(t *testing.T) {
		id := stage(t)
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		// the server's view of the disk loses the folder a moment after its
		// files (seen on Emby: the empty folder listed for that moment, a
		// film alone in it, which the refusal rightly offered to take)
		const gone = "the server cannot find the folder holding the item: the delete removes its record, and nothing on disk"
		var msg string
		if !eventually(func() bool { msg = callErr(t, "item_delete", map[string]any{"id": id}); return strings.Contains(msg, gone) }) {
			t.Errorf("the refusal for a film whose folder is gone: %s", msg)
		}
		out := call(t, "item_delete", map[string]any{"id": id, "confirm": true})
		if got := removedPaths(t, out); len(got) != 0 || !strings.Contains(str(out["note"]), "nothing on disk") {
			t.Errorf("item_delete of a film with no folder = %v", out)
		}
		if err := waitForItems("Messy Movies", have); err != nil {
			t.Error(err)
		}
		// and the second finds nothing to delete
		if msg := callErr(t, "item_delete", map[string]any{"id": id, "confirm": true}); !strings.Contains(msg, "no item with id "+id) {
			t.Errorf("deleting the same item twice: %s", msg)
		}
	})
}

// movieNfo is a movie.nfo naming a film and, when given, its ids: the shape
// scripts/testenv.sh writes.
func movieNfo(title string, year int, tmdb, imdb string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<movie>\n  <title>%s</title>\n  <year>%d</year>\n", title, year)
	if tmdb != "" {
		fmt.Fprintf(&b, "  <tmdbid>%s</tmdbid>\n  <uniqueid type=\"tmdb\" default=\"true\">%s</uniqueid>\n", tmdb, tmdb)
	}
	if imdb != "" {
		fmt.Fprintf(&b, "  <imdbid>%s</imdbid>\n  <uniqueid type=\"imdb\">%s</uniqueid>\n", imdb, imdb)
	}
	b.WriteString("</movie>\n")

	return []byte(b.String())
}

// Two films in one folder: deleting one takes its own files and every other
// file whose name begins with its file's name, bar media files - which both
// servers do by the name alone. Named apart, that is its own nfo, subtitle
// and poster, and the other film's are left as they were; named so that one
// begins the other, the other film's nfo, subtitle and poster go too, and the
// refusal says so before anything is deleted. Blade and Blade II, staged in a
// folder named for neither.
func TestDeletingAFilmSharingItsFolder(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	const shared = "Blade Collection"
	dir := filepath.Join(dataDir(), "messy-movies", shared)
	server := "/media/messy-movies/" + shared
	type film struct {
		name, title, tmdb, imdb string
		year                    int
	}
	// stage lays the two films out, each with an nfo, a subtitle and a
	// poster named after its file, and scans them in; it answers their ids
	// by file name
	stage := func(t *testing.T, films []film, extra ...string) map[string]string {
		t.Helper()
		have := movieCount(t, "Messy Movies")
		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
			if err := scanUntil("Messy Movies", have); err != nil {
				t.Error(err)
			}
		})
		mediaMkdir(t, dir)
		video := fixtureVideo(t, "messy-movies", messyArrival, messyArrival+".mp4")
		srt := fixtureVideo(t, "movies", "The Thirteenth Floor (1999)", "The Thirteenth Floor (1999).eng.srt")
		for _, f := range films {
			mediaWrite(t, filepath.Join(dir, f.name+".mp4"), video)
			mediaWrite(t, filepath.Join(dir, f.name+".nfo"), movieNfo(f.title, f.year, f.tmdb, f.imdb))
			mediaWrite(t, filepath.Join(dir, f.name+".eng.srt"), srt)
			mediaWrite(t, filepath.Join(dir, f.name+"-poster.jpg"), fixtureVideo(t, "messy-movies", messyBladeRunner, "poster.jpg"))
		}
		for _, e := range extra {
			mediaWrite(t, filepath.Join(dir, e), video)
		}
		if err := scanUntil("Messy Movies", have+len(films)); err != nil {
			t.Fatal(err)
		}
		ids := map[string]string{}
		for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "query": "Blade", "limit": 50})["items"], "items") {
			if p := str(it["path"]); strings.HasPrefix(p, server+"/") {
				ids[strings.TrimSuffix(filepath.Base(p), ".mp4")] = str(it["id"])
			}
		}
		for _, f := range films {
			if ids[f.name] == "" || len(ids) != len(films) {
				t.Fatalf("the shared folder's films = %v, want %d apart", ids, len(films))
			}
		}
		return ids
	}
	// sidecars are the nfo, subtitle and poster named after a film's file
	sidecars := func(name string) []string {
		return []string{server + "/" + name + "-poster.jpg", server + "/" + name + ".eng.srt", server + "/" + name + ".nfo"}
	}
	// deletes takes a film, holding the refusal and the answer to what it
	// names, and the rest of the folder to what it was
	deletes := func(t *testing.T, id string, want, others []string) {
		t.Helper()
		want = sorted(want)
		msg := callErr(t, "item_delete", map[string]any{"id": id})
		if folder, names := wouldRemove(t, msg); folder != "" || !slices.Equal(names, want) {
			t.Errorf("the refusal would take %q %v, want %v", folder, names, want)
		}
		if got := notOwn(msg); !slices.Equal(got, sorted(others)) {
			t.Errorf("the refusal says %v are not the film's own, want %v: %s", got, sorted(others), msg)
		}
		var gone []string
		for _, p := range want {
			gone = append(gone, hostPath(p))
		}
		keep := treeOf(t, dir, gone...)
		out := call(t, "item_delete", map[string]any{"id": id, "confirm": true})
		if got := removedPaths(t, out); !slices.Equal(got, want) {
			t.Errorf("removed = %v, want %v", got, want)
		}
		sameTree(t, dataDir(), keep, treeOf(t, dir))
		if msg := callErr(t, "item_get", map[string]any{"id": id}); !strings.Contains(msg, "no item") {
			t.Errorf("item_get of the deleted film: %s", msg)
		}
	}

	t.Run("named apart", func(t *testing.T) {
		blade, sequel := "Blade (1998)", "Blade II (2002)"
		ids := stage(t, []film{{blade, "Blade", "36647", "tt0120611", 1998}, {sequel, "Blade II", "36586", "tt0187738", 2002}})
		deletes(t, ids[blade], append(sidecars(blade), server+"/"+blade+".mp4"), nil)
		if got := str(call(t, "item_get", map[string]any{"id": ids[sequel]})["name"]); got != "Blade II" {
			t.Errorf("the film left in the folder reads as %q", got)
		}

		// alone in the folder now, and scanned as such: the other film goes
		// with the folder, the way any film alone in its folder does
		if err := scanUntil("Messy Movies", movieCount(t, "Messy Movies")); err != nil {
			t.Fatal(err)
		}
		own := []string{sequel + "-poster.jpg", sequel + ".eng.srt", sequel + ".mp4", sequel + ".nfo"}
		if folder, got := wouldRemove(t, callErr(t, "item_delete", map[string]any{"id": ids[sequel]})); folder != server || !slices.Equal(got, sorted(own)) {
			t.Errorf("the refusal for the film left alone would take %q %v, want the folder with %v", folder, got, own)
		}
		out := call(t, "item_delete", map[string]any{"id": ids[sequel], "confirm": true})
		want := []string{server + "/"}
		for _, f := range own {
			want = append(want, server+"/"+f)
		}
		if got := removedPaths(t, out); !slices.Equal(got, sorted(want)) {
			t.Errorf("removed = %v, want %v", got, sorted(want))
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			left, _ := os.ReadDir(dir)
			t.Errorf("the emptied folder is still on disk, holding %v", left)
		}
	})

	// the names a library without years in them has: Blade's file name
	// begins Blade II's, so deleting Blade takes Blade II's nfo, subtitle
	// and poster, and neither Blade II's video nor Blade's own trailer, both
	// media files. Beyond those each server keeps its own list: Emby takes
	// an .ass subtitle and leaves an .idx, and Jellyfin the other way round
	t.Run("one name beginning the other", func(t *testing.T) {
		blade, sequel := "Blade", "Blade II"
		trailer := blade + "-trailer.mp4"
		ids := stage(t, []film{{blade, "Blade", "36647", "tt0120611", 1998}, {sequel, "Blade II", "36586", "tt0187738", 2002}}, trailer, sequel+".ass", sequel+".idx")
		taken, kept := sequel+".ass", sequel+".idx"
		if isJellyfin() {
			taken, kept = kept, taken
		}
		// and the refusal says Blade II's are not Blade's own
		deletes(t, ids[blade], append(append(sidecars(blade), server+"/"+blade+".mp4", server+"/"+taken), sidecars(sequel)...), append(sidecars(sequel), server+"/"+taken))
		for _, kept := range []string{sequel + ".mp4", trailer, kept} {
			if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
				t.Errorf("%s went with the delete: %v", kept, err)
			}
		}
		if got := str(call(t, "item_get", map[string]any{"id": ids[sequel]})["name"]); got != "Blade II" {
			t.Errorf("the film left in the folder reads as %q", got)
		}
	})
}

// A film loose in a library's own folder, with nothing else there: deleting
// it takes its files and never the library's folder, which a film alone in
// any other folder would take. Blade, staged loose in a library of its own.
func TestDeletingAFilmLooseInALibrarysFolder(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	const library, name = "Loose Films", "Blade (1998)"
	root := filepath.Join(dataDir(), "loose-films")
	server := "/media/loose-films"
	mediaMkdir(t, root)
	mediaWrite(t, filepath.Join(root, name+".mp4"), fixtureVideo(t, "messy-movies", messyArrival, messyArrival+".mp4"))
	mediaWrite(t, filepath.Join(root, name+".nfo"), movieNfo("Blade", 1998, "36647", "tt0120611"))
	mediaWrite(t, filepath.Join(root, name+".eng.srt"), fixtureVideo(t, "movies", "The Thirteenth Floor (1999)", "The Thirteenth Floor (1999).eng.srt"))
	t.Cleanup(func() {
		if _, err := invoke("library_delete", map[string]any{"library": library, "confirm": true}); err != nil {
			t.Errorf("removing the library: %v", err)
		}
		if err := waitForExpectedScan(); err != nil {
			t.Error(err)
		}
		_ = os.RemoveAll(root)
	})
	call(t, "library_create", map[string]any{"name": library, "type": "movies", "paths": []any{server}, "scan": true})
	if err := waitForItems(library, 1); err != nil {
		t.Fatal(err)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
	id := findItem(t, library, "Movie", "Blade")

	files := []string{server + "/" + name + ".eng.srt", server + "/" + name + ".mp4", server + "/" + name + ".nfo"}
	msg := callErr(t, "item_delete", map[string]any{"id": id})
	if folder, names := wouldRemove(t, msg); folder != "" || !slices.Equal(names, files) {
		t.Errorf("the refusal would take %q %v, want the film's own files %v", folder, names, files)
	}
	out := call(t, "item_delete", map[string]any{"id": id, "confirm": true})
	if got := removedPaths(t, out); !slices.Equal(got, files) {
		t.Errorf("removed = %v, want %v", got, files)
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		t.Errorf("the library's folder went with its one film: %v", err)
	}
	if left, _ := os.ReadDir(root); len(left) != 0 {
		t.Errorf("the library's folder still holds %v", left)
	}
}

// What a delete would take from the lasting fixtures, asked and never
// confirmed: a Blu-ray kept whole goes as its folder, streams and all; a film
// in two versions goes with both and its nfo and poster, whichever version is
// asked about; a film alone in its folder takes its subtitle with it; an
// episode, even one file holding two, takes its own file and nfo and nothing
// of its neighbours'. Nothing on disk changes for asking.
func TestWhatADeleteWouldTake(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	before := treeOf(t, dataDir())
	messy, clean := "/media/messy-movies/", "/media/movies/"

	would := func(t *testing.T, id, wantFolder string, want []string) {
		t.Helper()
		msg := callErr(t, "item_delete", map[string]any{"id": id})
		if !strings.Contains(msg, "nothing was deleted") {
			t.Errorf("the refusal does not say nothing was deleted: %s", msg)
		}
		if folder, names := wouldRemove(t, msg); folder != wantFolder || !slices.Equal(names, sorted(want)) {
			t.Errorf("item %s would take %q %v, want %q %v", id, folder, names, wantFolder, sorted(want))
		}
	}

	t.Run("a Blu-ray kept whole", func(t *testing.T) {
		would(t, findItem(t, "Messy Movies", "Movie", "Cube"), messy+messyKeptBluRay, []string{"BDMV", "BDMV/STREAM", "BDMV/STREAM/00000.m2ts", "BDMV/STREAM/00001.m2ts"})
	})
	t.Run("a film in two versions", func(t *testing.T) {
		var ids []string
		for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "query": "Blade Runner"})["items"], "items") {
			ids = append(ids, str(it["id"]))
		}
		// Jellyfin's one entry, Emby's two
		if want := map[bool]int{true: 1, false: 2}[versionsMerged()]; len(ids) != want {
			t.Fatalf("the messy Blade Runner is %d items, want %d", len(ids), want)
		}
		for _, id := range ids {
			would(t, id, messy+messyBladeRunner, []string{"Blade Runner (1982) - 1080p.mp4", "Blade Runner (1982) - 2160p.mp4", "movie.nfo", "poster.jpg"})
		}
	})
	t.Run("a film with a subtitle", func(t *testing.T) {
		const floor = "The Thirteenth Floor (1999)"
		would(t, findItem(t, "Movies", "Movie", "The Thirteenth Floor"), clean+floor, []string{floor + ".eng.srt", floor + ".mp4", "movie.nfo", "poster.jpg"})
	})

	episode := func(t *testing.T, series string, number int) string {
		t.Helper()
		for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": findItem(t, "Messy Shows", "Series", series), "season": 1})["episodes"], "episodes") {
			if num(t, e["episode"], "episode") == number {
				return str(e["id"])
			}
		}
		t.Fatalf("%s holds no S01E%02d", series, number)

		return ""
	}
	t.Run("a file holding two episodes", func(t *testing.T) {
		base := "/media/messy-shows/Andor (2022)/Season 01/Andor S01E02E03"
		would(t, episode(t, "Andor", 2), "", []string{base + ".mp4", base + ".nfo"})
	})
	t.Run("an episode beside another", func(t *testing.T) {
		base := "/media/messy-shows/Star Trek The Next Generation/Season 01/Star Trek The Next Generation S01E01"
		would(t, episode(t, "Star Trek The Next Generation", 1), "", []string{base + ".mp4", base + ".nfo"})
	})

	sameTree(t, dataDir(), before, treeOf(t, dataDir()))
}
