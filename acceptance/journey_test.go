//go:build integration

package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	acc "github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/go-kt/test/env"

	"github.com/katbyte/embyfin-mcp/lib/testenv"
)

// Journeys: the tools chained the way a session uses them, against state
// the servers change underneath (a scan), across users, and repeated. Each
// puts back what it changes, and says so when it cannot: a change left
// behind fails the tests after it rather than this one.

// removeLibrary removes a library a test made, as retried calls a tool, and
// names it when it is left behind, which the tests that count the libraries
// then stop on (noLeftoverLibraries) rather than failing on the count.
func removeLibrary(t *testing.T, name string) {
	t.Helper()

	if err := suite.Retried("library_delete", map[string]any{"library": name, "confirm": true}); err != nil {
		t.Errorf("the library %s was left behind: removing it failed for a minute: %v", name, err)
	}
}

// removeLibraryIfThere removes a library a test made, as removeLibrary does,
// unless it is gone already: a test that deletes it itself, or stopped
// before making it. A read that can't say is reported. It says whether the
// library was there.
func removeLibraryIfThere(t *testing.T, name string) bool {
	t.Helper()

	if _, err := suite.Invoke("library_get", map[string]any{"library": name}); err != nil {
		if !strings.Contains(err.Error(), "no library named") {
			t.Errorf("reading whether the library %s is still there: %v", name, err)
		}

		return false
	}
	removeLibrary(t, name)

	return true
}

// settleScan waits for the scan a change started (suite.WaitForScan, or
// suite.WaitForExpectedScan) and reports a wait that fails: the tests after
// would start under the scan.
func settleScan(t *testing.T, wait func() error) {
	t.Helper()

	if err := wait(); err != nil {
		t.Errorf("waiting for the library scan: %v", err)
	}
}

// deleteLater removes what a test created once it ends.
func deleteLater(t *testing.T, tool, field, id string) {
	t.Helper()
	suite.PutBack(t, tool, map[string]any{field: id})
}

// unmarkLater clears a user's watched mark and favourite on items once the
// test ends.
func unmarkLater(t *testing.T, user string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		suite.PutBack(t, "item_set_state", map[string]any{"id": id, "user": user, "watched": false, "favourite": false})
	}
}

// clearable are the metadata fields an item read without them had empty:
// the servers leave a field out of an item they hold nothing for, and keep a
// field a post leaves out, so putting an item back sets them empty.
var clearable = map[string]any{
	"Overview": "", "OfficialRating": "", "CustomRating": "", "OriginalTitle": "", "ForcedSortName": "",
	"Taglines": []any{}, "Genres": []any{}, "GenreItems": []any{}, "Tags": []any{}, "TagItems": []any{},
	"Studios": []any{}, "People": []any{}, "ProviderIds": map[string]any{},
}

// restoreLater puts an item back as it is now once the test ends: the whole
// item, so what no tool puts back (people, studios, a rating, provider ids,
// the fields a refresh filled) goes back too, and a field it had empty is
// emptied again. A post the server refuses while a refresh of the item is
// still writing it (Emby answers a 500 then) is tried again. It returns the
// item as read.
func restoreLater(t *testing.T, id string) map[string]any {
	t.Helper()

	before := fullItem(t, id)
	t.Cleanup(func() {
		item := maps.Clone(before)
		for field, empty := range clearable {
			if _, had := item[field]; !had {
				item[field] = empty
			}
		}
		// people and studios by name: the ids they had may name nothing
		// once a refresh has replaced them, which Emby refuses (a foreign
		// key the database cannot follow)
		for _, field := range []string{"People", "Studios", "GenreItems", "TagItems"} {
			var named []any
			for _, e := range acc.RowsOf(item[field]) {
				e = maps.Clone(e)
				delete(e, "Id")
				named = append(named, e)
			}
			if named != nil {
				item[field] = named
			}
		}
		var status int
		var raw []byte
		for range 10 {
			if status, raw = postItem(t, id, item); status/100 == 2 {
				return
			}
			time.Sleep(time.Second)
		}
		t.Errorf("putting item %s back: HTTP %d: %.200s", id, status, raw)
	})

	return before
}

// keepFiles puts the folders' files back as they are now once the test ends:
// what a server wrote there since goes (Jellyfin saves an nfo, and artwork,
// beside every item it edits), and what it changed or removed is written
// back. Registered before restoreLater, it runs after it, so the nfo a
// restore writes is the one replaced.
func keepFiles(t *testing.T, dirs ...string) {
	t.Helper()

	files, folders := map[string][]byte{}, map[string]bool{}
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
			case d.IsDir():
				folders[path] = true
			default:
				raw, rerr := os.ReadFile(path) //nolint:gosec // a fixture under the test data dir
				if rerr != nil {
					t.Fatal(rerr)
				}
				files[path] = raw
			}
			return nil
		})
	}
	t.Cleanup(func() {
		for _, dir := range dirs {
			_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				switch {
				case err != nil:
				case d.IsDir() && !folders[path]:
					if rerr := os.RemoveAll(path); rerr != nil {
						t.Errorf("removing %s: %v", path, rerr)
					}
					return filepath.SkipDir
				case d.IsDir():
				default:
					if _, ours := files[path]; !ours {
						if rerr := os.Remove(path); rerr != nil {
							t.Errorf("removing %s: %v", path, rerr)
						}
					}
				}
				return nil
			})
		}
		for path, raw := range files {
			if now, err := os.ReadFile(path); err == nil && bytes.Equal(now, raw) { //nolint:gosec // same
				continue
			}
			env.Mkdir(t, testenv.DataDir(), filepath.Dir(path))
			env.WriteFile(t, path, raw)
		}
	})
}

// itemFolder is where an item's files sit on this machine.
func itemFolder(t *testing.T, id string) string {
	t.Helper()

	return filepath.Dir(hostPath(acc.Str(suite.Call(t, "item_get", map[string]any{"id": id})["path"])))
}

// itemEtag is an item's etag, which moves whenever the server saves the item.
func itemEtag(t *testing.T, id string) string {
	t.Helper()

	return acc.Str(fullItem(t, id)["Etag"])
}

// savedSince waits for the server to save an item after it read with the
// etag given: a refresh or a scan's re-read is queued, not done, when the
// call that asked for it answers, and a check made before it runs proves
// nothing about it.
func savedSince(t *testing.T, id, was string) bool {
	t.Helper()

	return acc.EventuallyWithin(time.Minute, func() bool { return itemEtag(t, id) != was })
}

// refreshed asks for a refresh of an item and says whether it ran:
// item_refresh waits for the refresh to save the item, and says whether it
// saw it do so (TestAnEditRightAfterARefreshStays holds it to that).
func refreshed(t *testing.T, id string) bool {
	t.Helper()

	return acc.BoolOf(suite.Call(t, "item_refresh", map[string]any{"id": id})["landed"])
}

// serverNow is the server's clock, from the Date header it answers with: a
// date the server wrote is compared against the server's time, not this
// machine's, which a container's virtual machine drifts from.
func serverNow(t *testing.T) time.Time {
	t.Helper()

	resp, err := http.Get(os.Getenv("EMBYFIN_SERVER") + "/System/Info/Public") //nolint:noctx // a test's one read
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	now, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		t.Fatalf("the server's Date header %q: %v", resp.Header.Get("Date"), err)
	}

	return now
}

// rescanOrReport asks for a scan until check holds, asking again whenever the
// scan goes idle short of it, as scanUntil does for a count; it reports a
// check that never holds rather than stopping the test, so it serves a
// clean-up as well.
func rescanOrReport(t *testing.T, library string, check func() bool) {
	t.Helper()

	if err := suite.ScanUntilTrue("", check); err != nil {
		t.Errorf("a scan for %s: %v", library, err)
	}
}

// slowScan lays forty short files in a folder of the messy film library, so
// the next scan has them to read and runs for seconds rather than the
// fraction of one a scan with nothing new takes: long enough for what a test
// does while one runs to land inside it. They go again once the test ends.
// It returns what gives the scan after that more to read: the files' times
// moved on, as a download writing them over would, and as many files again,
// since a server that read forty before the edits landed reads faster than
// that.
func slowScan(t *testing.T) (again func()) {
	t.Helper()

	const name = "The Lord of the Rings The Two Towers (2002)"
	dir := filepath.Join(testenv.DataDir(), "messy-movies", name)
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
		rescanOrReport(t, "Messy Movies", func() bool {
			out, err := suite.Invoke("library_items", map[string]any{"library": "Messy Movies", "query": "Two Towers", "limit": 100})
			return err == nil && !slices.ContainsFunc(acc.RowsOf(out["items"]), func(it map[string]any) bool {
				return strings.Contains(acc.Str(it["path"]), "/"+name+"/")
			})
		})
	})
	raw := fixtureVideo(t, "messy-movies", messyAlien, messyAlien+".mp4")
	env.Mkdir(t, testenv.DataDir(), dir)
	var files []string
	lay := func(n int) {
		for range n {
			f := filepath.Join(dir, fmt.Sprintf("%s - part%d.mp4", name, len(files)+1))
			env.WriteFile(t, f, raw)
			files = append(files, f)
		}
	}
	lay(40)

	return func() {
		now := time.Now()
		for _, f := range files {
			if err := os.Chtimes(f, now, now); err != nil {
				t.Fatal(err)
			}
		}
		lay(len(files))
	}
}

// entryNamed returns the entry id of the first entry of a playlist that is
// the named item, as playlist_get lists it now.
func entryNamed(t *testing.T, playlist, name string) string {
	t.Helper()

	for _, e := range acc.Rows(t, suite.Call(t, "playlist_get", map[string]any{"playlist": playlist})["entries"], "entries") {
		if acc.Str(e["name"]) == name {
			return acc.Str(e["entry_id"])
		}
	}
	t.Fatalf("%s is not in the playlist", name)

	return ""
}

// A library scan validates every playlist and collection and, on Emby, saves
// each as it found it: edits made while one runs must all be there once it
// has finished. The scan is given files to read so it runs for seconds, and
// is checked to be running still once the last edit has landed; one that
// finished first raced nothing, and the edits go again under a fresh scan.
func TestEditsDuringAScan(t *testing.T) {
	dune := findItem(t, "Movies", "Movie", "Dune")
	dune2 := findItem(t, "Movies", "Movie", "Dune: Part Two")
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	alien := findItem(t, "Movies", "Movie", "Alien")
	again := slowScan(t)
	t.Cleanup(func() {
		if err := suite.WaitForScan(); err != nil {
			t.Error(err)
		}
	})

	// one playlist a try, the last kept to check; one collection for every
	// try, put back to Alien alone between them (Emby cannot make a
	// collection again under a name it has deleted)
	var pl, col string
	var colAdd, colRemove map[string]any
	t.Cleanup(func() {
		if pl == "" {
			return
		}
		if err := suite.Retried("playlist_delete", map[string]any{"playlist": pl}); err != nil {
			t.Errorf("deleting the playlist: %v", err)
		}
	})
	for try := 1; ; try++ {
		if out := suite.Call(t, "library_scan", nil); !acc.BoolOf(out["started"]) {
			t.Fatalf("library_scan = %v", out)
		}

		pl = acc.Str(suite.Call(t, "playlist_create", map[string]any{"name": "Zzyzx Scan Race", "item_ids": []any{dune}, "media_type": "Video"})["id"])
		if add := suite.Call(t, "playlist_edit", map[string]any{"playlist": pl, "add_items": []any{dune2, arrival}}); acc.Num(t, add["added"], "added") != 2 {
			t.Errorf("playlist_edit add_items = %v", add)
		}
		// a scan can renumber Emby's entries between reading one and using
		// it, which the tool refuses; a caller reads the playlist again
		args := map[string]any{"playlist": pl, "move_entry_id": entryNamed(t, pl, "Arrival"), "move_item_id": arrival, "position": 1}
		_, err := suite.Invoke("playlist_edit", args)
		if err != nil && strings.Contains(err.Error(), "so nothing was changed") {
			args["move_entry_id"] = entryNamed(t, pl, "Arrival")
			_, err = suite.Invoke("playlist_edit", args)
		}
		if err != nil {
			t.Fatalf("playlist_edit: %v", err)
		}
		if rm := suite.Call(t, "playlist_edit", map[string]any{"playlist": pl, "remove_entries": []any{map[string]any{"entry_id": entryNamed(t, pl, "Dune: Part Two"), "item_id": dune2}}}); acc.Num(t, rm["removed"], "removed") != 1 {
			t.Errorf("playlist_edit remove_entries = %v", rm)
		}

		if col == "" {
			col = acc.Str(suite.Call(t, "collection_create", map[string]any{"name": "Zzyzx Scan Race", "item_ids": []any{alien}})["id"])
			deleteLater(t, "collection_delete", "collection", col)
		}
		if colAdd = suite.Call(t, "collection_edit", map[string]any{"collection": col, "add_items": []any{dune, arrival}}); acc.Num(t, colAdd["added"], "added") != 2 {
			t.Errorf("collection_edit add_items = %v", colAdd)
		}
		if colRemove = suite.Call(t, "collection_edit", map[string]any{"collection": col, "remove_items": []any{dune}}); acc.Num(t, colRemove["removed"], "removed") != 1 {
			t.Errorf("collection_edit remove_items = %v", colRemove)
		}

		if idle, err := suite.ScanIdle(); err == nil && !idle {
			break
		}
		if try == 4 {
			t.Fatal("the scan had finished before the last edit, four times, the last with 320 files to read: the edits raced nothing")
		}
		// the scan finished first: once it has, put the lists back as the
		// try found them and go again
		if err := suite.WaitForScan(); err != nil {
			t.Fatal(err)
		}
		if err := suite.Retried("playlist_delete", map[string]any{"playlist": pl}); err != nil {
			t.Fatalf("deleting the try's playlist: %v", err)
		}
		pl = ""
		suite.Call(t, "collection_edit", map[string]any{"collection": col, "remove_items": []any{arrival}})
		again()
	}

	if err := suite.WaitForScan(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range acc.Rows(t, suite.Call(t, "playlist_get", map[string]any{"playlist": pl})["entries"], "entries") {
		got = append(got, acc.Str(e["name"]))
	}
	if !slices.Equal(got, []string{"Arrival", "Dune"}) {
		t.Errorf("after the scan the playlist is %v, want [Arrival Dune]", got)
	}
	// the collection's changes were made while the scan ran, and both
	// answers say so
	const raced = "was running: it may put back or drop members once it finishes; check the collection afterwards"
	for tool, out := range map[string]map[string]any{"collection_edit add_items": colAdd, "collection_edit remove_items": colRemove} {
		if !strings.Contains(acc.Str(out["note"]), raced) {
			t.Errorf("%s during the scan: note %q, want it to say a scan was running", tool, out["note"])
		}
	}
	// Jellyfin's scan saves a collection as it read it, and has put an item
	// back after collection_edit remove_items said it was gone, and dropped one nobody
	// took out (seen on 12.1): allowed there because the answers said a scan
	// could; Emby's must hold
	var held []string
	if !acc.Eventually(func() bool {
		held = acc.Sorted(names(t, suite.Call(t, "collection_get", map[string]any{"collection": col})["items"], "items"))
		return slices.Equal(held, []string{"Alien", "Arrival"})
	}) {
		if isJellyfin() && strings.Contains(acc.Str(colAdd["note"]), raced) && strings.Contains(acc.Str(colRemove["note"]), raced) {
			t.Logf("after the scan Jellyfin's collection holds %v, not [Alien Arrival]: the scan saved it over the changes, as their answers said it may", held)
		} else {
			t.Errorf("after the scan the collection holds %v, want [Alien Arrival]", held)
		}
	}
}

// fullItem reads an item the way the servers' own editors do, for putting
// back a field no tool clears.
func fullItem(t *testing.T, id string) map[string]any {
	t.Helper()

	path := "/Items/" + id + "?userId=" + os.Getenv("EMBYFIN_TEST_ADMIN_ID")
	if !isJellyfin() {
		path = "/Users/" + os.Getenv("EMBYFIN_TEST_ADMIN_ID") + "/Items/" + id
	}
	status, raw := api(t, http.MethodGet, path, "", nil)
	var item map[string]any
	if status != http.StatusOK || json.Unmarshal(raw, &item) != nil {
		t.Fatalf("reading item %s: HTTP %d: %.200s", id, status, raw)
	}

	return item
}

// updateItem posts an item back with fields changed.
func updateItem(t *testing.T, id string, change map[string]any) {
	t.Helper()

	if status, raw := postItem(t, id, change); status/100 != 2 {
		t.Errorf("updating item %s: HTTP %d: %.200s", id, status, raw)
	}
}

// postItem posts an item back with fields changed and returns the answer.
func postItem(t *testing.T, id string, change map[string]any) (int, []byte) {
	t.Helper()

	item := fullItem(t, id)
	maps.Copy(item, change)

	return api(t, http.MethodPost, "/Items/"+id, "", item)
}

// movedBy checks audit_all's counts after a fix against the counts before
// it: the rows named move by exactly what they are given, and every other
// row, and the total, by nothing else. A fix that shifted another audit's
// count, or a total that did not follow its row, shows here.
func movedBy(t *testing.T, library string, before, want map[string]int) {
	t.Helper()

	after := auditCounts(t, map[string]any{"library": library})
	for audit, n := range after {
		if n != before[audit]+want[audit] {
			t.Errorf("audit_all %s: %d before the fix and %d after, want %+d", audit, before[audit], n, want[audit])
		}
	}
	if len(after) != len(before) {
		t.Errorf("audit_all rows before %v, after %v", before, after)
	}
}

// Every audit whose findings a tool can clear: audit, fix with the tool the
// finding points at, audit again, and put the defect back. Each fix takes
// its own item off the worklist and leaves the rest of it as it was, and in
// audit_all it moves that audit's row and the total by one and nothing else.
func TestAuditsAreFixable(t *testing.T) {
	arrival := findItem(t, "Messy Movies", "Movie", "Arrival")
	mononoke := findItem(t, "Messy Movies", "Movie", "Princess Mononoke")
	dune := findItem(t, "Messy Movies", "Movie", "Dune")
	messy := map[string]any{"library": "Messy Movies"}

	t.Run("missing overview", func(t *testing.T) {
		before := findings(t, missing(t, "overview", messy))
		if !slices.Contains(before, "Arrival") {
			t.Fatalf("audit_missing_metadata overview = %v, want Arrival among them", before)
		}
		counts := auditCounts(t, messy)
		keepFiles(t, itemFolder(t, arrival))
		restoreLater(t, arrival)

		suite.Call(t, "item_edit", map[string]any{"ids": []any{arrival}, "overview": "A linguist is recruited to talk to the visitors."})
		if got := findings(t, missing(t, "overview", messy)); !slices.Equal(got, acc.WithoutName(before, "Arrival")) {
			t.Errorf("after item_edit audit_missing_metadata overview = %v, want %v", got, acc.WithoutName(before, "Arrival"))
		}
		movedBy(t, "Messy Movies", counts, map[string]int{rowOverview: -1, "total_findings": -1})
	})

	t.Run("missing poster", func(t *testing.T) {
		before := findings(t, missing(t, "poster", messy))
		if !slices.Contains(before, "Arrival") {
			t.Fatalf("audit_missing_metadata poster = %v, want Arrival among them", before)
		}
		counts := auditCounts(t, messy)
		keepFiles(t, itemFolder(t, arrival))
		// the providers are off in the messy library, so the poster comes from
		// the clean copy's candidates
		clean := findItem(t, "Movies", "Movie", "Arrival")
		cands := acc.Rows(t, suite.Call(t, "item_artwork", map[string]any{"id": clean, "type": "Primary", "limit": 1})["candidates"], "candidates")
		if len(cands) == 0 {
			t.Fatal("no poster candidates for Arrival")
		}
		suite.Call(t, "item_artwork_set", map[string]any{"id": arrival, "url": acc.Str(cands[0]["url"])})
		t.Cleanup(func() {
			if status, raw := api(t, http.MethodDelete, "/Items/"+arrival+"/Images/Primary", "", nil); status/100 != 2 {
				t.Errorf("removing the poster: HTTP %d: %s", status, raw)
			}
		})
		if !acc.Eventually(func() bool { return !slices.Contains(findings(t, missing(t, "poster", messy)), "Arrival") }) {
			t.Fatal("after item_artwork_set Arrival still has no poster")
		}
		if got := findings(t, missing(t, "poster", messy)); !slices.Equal(got, acc.WithoutName(before, "Arrival")) {
			t.Errorf("after item_artwork_set audit_missing_metadata poster = %v, want %v", got, acc.WithoutName(before, "Arrival"))
		}
		movedBy(t, "Messy Movies", counts, map[string]int{rowPoster: -1, "total_findings": -1})
	})

	// the messy Dune's folder says 2021 and its metadata Lynch's 1984 film,
	// ids and all: the fix is the identity, not the year alone, which would
	// leave a 2021 film holding the 1984 film's ids
	t.Run("year mismatch", func(t *testing.T) {
		// Stargate, held as Stargate: Continuum, is dated wrong too, and
		// stays so
		years := map[string]any{"library": "Messy Movies", "checks": "year"}
		if got := findings(t, suite.Call(t, "audit_file_path", years)); !slices.Equal(got, []string{"Dune", "Stargate: Continuum"}) {
			t.Fatalf("audit_file_path = %v, want [Dune Stargate: Continuum]", got)
		}
		counts := auditCounts(t, messy)
		keepFiles(t, itemFolder(t, dune))
		restoreLater(t, dune)

		idx := -1
		for i, c := range acc.Rows(t, suite.Call(t, "item_identify", map[string]any{"id": dune, "kind": "movie", "year": 2021})["candidates"], "candidates") {
			if ids, _ := c["metadata_provider_ids"].(map[string]any); idx < 0 && acc.Str(ids["tmdb"]) == "438631" {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatal("the 2021 Dune is not a candidate")
		}
		// the fetchers are off, so the apply sets the ids and the answer says
		// the rest is item_edit's to set: the year is still the old film's
		out := suite.Call(t, "item_identify_apply", withCandidateIDs(t, map[string]any{"id": dune, "kind": "movie", "candidate": idx, "year": 2021}))
		if ids, _ := out["metadata_provider_ids"].(map[string]any); acc.Str(ids["tmdb"]) != "438631" || acc.Num(t, out["year"], "year") != 1984 || !strings.Contains(acc.Str(out["note"]), "set what is missing or wrong with item_edit") {
			t.Errorf("item_identify_apply = %v", out)
		}
		if got := findings(t, suite.Call(t, "audit_file_path", years)); !slices.Equal(got, []string{"Dune", "Stargate: Continuum"}) {
			t.Errorf("with the new ids alone audit_file_path = %v, want [Dune Stargate: Continuum] still", got)
		}
		suite.Call(t, "item_edit", map[string]any{"ids": []any{dune}, "year": 2021})
		if got := findings(t, suite.Call(t, "audit_file_path", years)); !slices.Equal(got, []string{"Stargate: Continuum"}) {
			t.Errorf("after the match and the year audit_file_path = %v, want Stargate's alone", got)
		}
		if got := suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "841", "type": "movie"}); acc.BoolOf(got["found"]) || len(acc.RowsOf(got["items"])) != 0 {
			t.Errorf("tmdb 841 still finds %v", got)
		}
		movedBy(t, "Messy Movies", counts, map[string]int{"audit_file_path": -1, "total_findings": -1})
	})

	t.Run("unmatched", func(t *testing.T) {
		if got := findings(t, missing(t, "provider_id", messy)); !slices.Equal(got, messyUnmatched) {
			t.Fatalf("audit_missing_metadata provider_id = %v, want %v", got, messyUnmatched)
		}
		counts := auditCounts(t, messy)
		keepFiles(t, itemFolder(t, mononoke))
		restoreLater(t, mononoke)
		// the messy library has its fetchers off, so the search runs without
		// the item and the match still sets its ids
		cands := acc.Rows(t, suite.Call(t, "item_identify", map[string]any{"id": mononoke, "kind": "movie"})["candidates"], "candidates")
		// by either id: without the item Jellyfin's search is answered by OMDb,
		// whose candidates carry the imdb id alone
		idx := -1
		for i, c := range cands {
			if ids, _ := c["metadata_provider_ids"].(map[string]any); idx < 0 && (acc.Str(ids["tmdb"]) == "128" || acc.Str(ids["imdb"]) == "tt0119698") {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatalf("no Princess Mononoke (tmdb 128, imdb tt0119698) among the candidates: %v", cands)
		}
		suite.Call(t, "item_identify_apply", withCandidateIDs(t, map[string]any{"id": mononoke, "kind": "movie", "candidate": idx}))
		// the two discs are still unmatched; the film is not
		discs := acc.WithoutName(messyUnmatched, "Princess Mononoke")
		if !acc.Eventually(func() bool {
			return slices.Equal(findings(t, missing(t, "provider_id", messy)), discs)
		}) {
			t.Errorf("after item_identify_apply the unmatched are %v, want %v", findings(t, missing(t, "provider_id", messy)), discs)
		}
		movedBy(t, "Messy Movies", counts, map[string]int{rowProvider: -1, "total_findings": -1})
	})

	// what TestAuditUnwatched does not see: audit_all counts the unwatched as
	// a row and leaves them out of its total, which is of defects
	t.Run("unwatched", func(t *testing.T) {
		movies := map[string]any{"library": "Movies"}
		before := findings(t, suite.Call(t, "audit_unwatched", movies))
		if !slices.Contains(before, "Princess Mononoke") {
			t.Fatalf("audit_unwatched = %v, want Princess Mononoke among them", before)
		}
		counts := auditCounts(t, movies)
		id := findItem(t, "Movies", "Movie", "Princess Mononoke")
		unmarkLater(t, "alice", id)
		suite.Call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "watched": true})
		if got := findings(t, suite.Call(t, "audit_unwatched", movies)); !slices.Equal(got, acc.WithoutName(before, "Princess Mononoke")) {
			t.Errorf("after item_set_state audit_unwatched = %v, want %v", got, acc.WithoutName(before, "Princess Mononoke"))
		}
		movedBy(t, "Movies", counts, map[string]int{"audit_unwatched": -1})
	})
}

// A client plays something: the session shows it, and the history, activity
// and watch-state tools all agree about it afterwards.
func TestPlaybackIsRecorded(t *testing.T) {
	device, token := signInPlayer(t)
	dune := findItem(t, "Movies", "Movie", "Dune")
	dune2 := findItem(t, "Movies", "Movie", "Dune: Part Two")
	aliceBefore := suite.Call(t, "user_stats", map[string]any{"user": "alice"})
	unmarkLater(t, "alice", dune)
	// a second early, for the Date header's whole seconds
	started := serverNow(t).Add(-time.Second)

	// a play session the way a client starts one: Emby answers a report
	// without the PlaySessionId PlaybackInfo hands out with a 400
	status, raw := api(t, http.MethodPost, "/Items/"+dune+"/PlaybackInfo?UserId="+os.Getenv("EMBYFIN_TEST_USER_ID"), token, map[string]any{})
	var info struct {
		PlaySessionID string `json:"PlaySessionId"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &info) != nil {
		t.Fatalf("PlaybackInfo: HTTP %d: %.200s", status, raw)
	}
	report := func(path string, ticks int64) {
		t.Helper()
		body := map[string]any{"ItemId": dune, "PlaySessionId": info.PlaySessionID, "PositionTicks": ticks, "CanSeek": true, "PlayMethod": "DirectPlay"}
		if status, raw := api(t, http.MethodPost, path, token, body); status/100 != 2 {
			t.Fatalf("%s: HTTP %d: %s", path, status, raw)
		}
	}
	report("/Sessions/Playing", 0)

	playing := false
	for range 20 {
		for _, s := range acc.Rows(t, suite.Call(t, "session_list", nil)["sessions"], "sessions") {
			if acc.Str(s["device"]) == device && acc.Str(s["now_playing"]) == "Dune" && acc.Str(s["user"]) == "alice" {
				playing = true
			}
		}
		if playing {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !playing {
		t.Errorf("session_list does not show alice playing Dune: %v", suite.Call(t, "session_list", nil)["sessions"])
	}

	// stopped a minute before the end of the film's 155 minutes, which is
	// past the point both servers count as finished (90% by default)
	end := int64(155*60-60) * 10_000_000
	report("/Sessions/Playing/Progress", end)
	report("/Sessions/Playing/Stopped", end)

	var played map[string]any
	for range 20 {
		for _, u := range acc.Rows(t, suite.Call(t, "item_last_watched", map[string]any{"id": dune})["users"], "users") {
			if acc.Str(u["user"]) == "alice" && acc.BoolOf(u["played"]) {
				played = u
			}
		}
		if played != nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if played == nil || acc.Num(t, played["play_count"], "play_count") < 1 {
		t.Errorf("item_last_watched does not show alice finishing Dune: %v", played)
	}
	if after := suite.Call(t, "user_stats", map[string]any{"user": "alice"}); acc.Num(t, after["movies_watched"], "movies_watched") != acc.Num(t, aliceBefore["movies_watched"], "movies_watched")+1 {
		t.Errorf("alice movies_watched %v, was %v", after["movies_watched"], aliceBefore["movies_watched"])
	}

	// the activity log: the playback is Dune's and alice's, and not Dune: Part
	// Two's, whose title contains Dune's. It is this run's, stopped since the
	// test began: a run before this one left a row for Dune too.
	var history []map[string]any
	var hist map[string]any
	this := func(row map[string]any) bool {
		at, err := time.Parse(time.RFC3339Nano, acc.Str(row["last_played"]))
		return err == nil && !at.Before(started) && acc.Str(row["name"]) == "Dune" && acc.Str(row["event"]) == "stop"
	}
	if !acc.Eventually(func() bool {
		hist = suite.Call(t, "user_history", map[string]any{"user": "alice", "days": 1})
		history = acc.Rows(t, hist["items"], "items")
		return len(history) > 0 && this(history[0])
	}) {
		t.Errorf("user_history alice has no stop of Dune since %s first: %v", started, history)
	}
	if acc.Num(t, hist["total"], "total") != len(history) || acc.Num(t, hist["offset"], "offset") != 0 {
		t.Errorf("user_history total %v offset %v for %d items", hist["total"], hist["offset"], len(history))
	}
	entries := summaries(t, suite.Call(t, "server_activity", map[string]any{"item": dune, "days": 1}))
	if !slices.ContainsFunc(entries, func(e string) bool { return strings.Contains(e, "alice") }) {
		t.Errorf("server_activity about Dune = %v", entries)
	}
	// and Dune's play is not Dune: Part Two's, whose title holds Dune's:
	// every entry about Part Two names Part Two, whoever played it when
	if other := summaries(t, suite.Call(t, "server_activity", map[string]any{"item": dune2, "days": 1})); slices.ContainsFunc(other, func(e string) bool { return !strings.Contains(e, "Dune: Part Two") }) {
		t.Errorf("server_activity about Dune: Part Two picked up Dune's playback: %v", other)
	}
	// and about alice: her play of Dune, by the item's id
	about := acc.Rows(t, suite.Call(t, "server_activity", map[string]any{"user": "alice", "days": 1})["entries"], "entries")
	if !slices.ContainsFunc(about, func(e map[string]any) bool { return acc.Str(e["item_id"]) == dune }) {
		t.Errorf("server_activity about alice has no entry about Dune: %v", about)
	}
	for _, e := range about {
		if !strings.Contains(acc.Str(e["summary"]), "alice") {
			t.Errorf("server_activity about alice holds another's entry: %v", e)
		}
	}
	activity := acc.Rows(t, suite.Call(t, "server_activity", map[string]any{"days": 1, "limit": 50})["entries"], "entries")
	if !slices.ContainsFunc(activity, func(e map[string]any) bool {
		return strings.Contains(acc.Str(e["summary"]), "alice") && strings.Contains(acc.Str(e["summary"]), "Dune")
	}) {
		t.Errorf("server_activity has no playback of Dune by alice: %v", activity)
	}
}

// audit_all's counts are the audits' own: each row equals what the audit it
// names reports for the same library. A row compared only where both say 0
// proves nothing, so every audit must also be compared somewhere it finds
// something, which the lasting defects in the messy libraries (and Breaking
// Bad's twice-titled episode in Shows) make possible.
func TestAuditAllMatchesEachAudit(t *testing.T) {
	found := map[string]bool{}
	for _, library := range []string{"Messy Movies", "Messy Shows", "Shows", ""} {
		args := map[string]any{}
		if library != "" {
			args["library"] = library
		}
		for _, row := range acc.Rows(t, suite.Call(t, "audit_all", args)["audits"], "audits") {
			name := rowKey(row)
			if s, _ := row["skipped"].(bool); s {
				continue
			}
			// audit_orphans takes no library and runs in audit_all only
			// without one; the rest take the same arguments, and what the
			// row says it was counted over
			own := rowArgs(row, args)
			if name == "audit_orphans" {
				own = nil
			}
			out := suite.Call(t, acc.Str(row["audit"]), own)
			// every audit names its count and its sweep the same way
			count, scanned := out["total_findings"], out["items_scanned"]
			if acc.Num(t, count, name) != acc.Num(t, row["findings"], "findings") || acc.Num(t, scanned, name) != acc.Num(t, row["items_scanned"], "items_scanned") {
				t.Errorf("%q %s: audit_all counted %v of %v, the audit %v of %v", library, name, row["findings"], row["items_scanned"], count, scanned)
			}
			found[name] = found[name] || acc.Num(t, row["findings"], "findings") > 0
		}
	}

	// the one with nothing to find in any fixture, and where it is compared
	// against something instead
	never := map[string]bool{
		// the fixtures leave nothing outside a library; TestOrphans compares
		// the row against a removed library's leftovers on Jellyfin, and
		// Emby 4.10 leaves none behind to compare
		"audit_orphans": true,
	}
	for name, nonZero := range found {
		if !nonZero && !never[name] {
			t.Errorf("%s found nothing in any library compared, so its row was never checked against a count", name)
		}
	}
}

// The lookups change nothing: a snapshot of what they could change - each
// item's metadata, its images and every user's watch state on it, the
// users and alice's counts, a playlist, a collection and the libraries - is
// the same after every read-only tool has run over them. The tools run are
// the ones the server marks read-only, so one added without a lookup here
// fails the test rather than going unchecked.
func TestLookupsChangeNothing(t *testing.T) {
	blade := findItem(t, "Movies", "Movie", "Blade Runner")
	mononoke := findItem(t, "Movies", "Movie", "Princess Mononoke")
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	messyMononoke := findItem(t, "Messy Movies", "Movie", "Princess Mononoke")
	messyArrival := findItem(t, "Messy Movies", "Movie", "Arrival")
	severance := findItem(t, "Shows", "Series", "Severance")
	pl := acc.Str(suite.Call(t, "playlist_create", map[string]any{"name": "Zzyzx Lookups", "item_ids": []any{blade, arrival}, "media_type": "Video"})["id"])
	deleteLater(t, "playlist_delete", "playlist", pl)
	col := acc.Str(suite.Call(t, "collection_create", map[string]any{"name": "Zzyzx Lookups", "item_ids": []any{blade, mononoke}})["id"])
	deleteLater(t, "collection_delete", "collection", col)

	snapshot := func() map[string]any {
		s := map[string]any{}
		for _, id := range []string{blade, mononoke, arrival, messyMononoke, messyArrival, severance} {
			s["item "+id] = suite.Call(t, "item_get", map[string]any{"id": id})
			s["watch state "+id] = suite.Call(t, "item_last_watched", map[string]any{"id": id})
			s["images "+id] = suite.Call(t, "item_artwork", map[string]any{"id": id, "limit": 1})["current"]
		}
		for _, u := range []string{"root", "alice"} {
			s["user "+u] = suite.Call(t, "user_get", map[string]any{"user": u})
		}
		s["alice's counts"] = suite.Call(t, "user_stats", map[string]any{"user": "alice"})
		s["playlist"] = suite.Call(t, "playlist_get", map[string]any{"playlist": pl})
		s["collection"] = suite.Call(t, "collection_get", map[string]any{"collection": col})
		s["playlists"] = suite.Call(t, "playlist_list", nil)
		s["collections"] = suite.Call(t, "collection_list", nil)
		s["libraries"] = suite.Call(t, "library_list", nil)
		s["filters"] = suite.Call(t, "library_filters", map[string]any{"library": "Movies"})
		return s
	}
	before := snapshot()

	lookups := map[string][]map[string]any{
		"audit_all":                {nil, {"library": "Messy Movies"}},
		"audit_anime_ids":          {{"library": "Messy Shows"}},
		"audit_disc_folders":       {{"library": "Messy Movies"}},
		"audit_duplicate_episodes": {{"library": "Messy Shows"}},
		"audit_duplicates":         {{"library": "Messy Movies"}, {"library": "Messy Shows"}},
		"audit_file_path":          {{"library": "Messy Movies"}},
		"audit_language":           {{"language": "jpn", "find": "no_audio", "library": "Messy Movies"}},
		"audit_missing_episodes":   {{"library": "Shows"}},
		"audit_missing_metadata":   {{"library": "Messy Movies"}, {"library": "Messy Movies", "problems": "provider_id", "missing": "tmdb"}},
		"audit_multiple_versions":  {{"library": "Messy Movies"}},
		"audit_orphans":            {nil},
		"audit_previews":           {nil, {"library": "Movies"}},
		"audit_provider":           {{"library": "Movies", "checks": "ids", "types": "Movie"}},
		"audit_quality":            {{"library": "Messy Movies"}},
		"audit_runtime":            {{"library": "Messy Shows"}},
		"audit_spelling":           {nil},
		"provider_cache_clear":     {nil},
		"audit_unwatched":          {{"types": "Movie,Series"}},
		"audit_whitespace":         {{"library": "Messy Shows"}},
		"collection_get":           {{"collection": col}},
		"collection_list":          {nil},
		"item_artwork":             {{"id": blade, "type": "Primary", "limit": 2}},
		"item_find_by_metadata_id": {{"metadata_provider": "tmdb", "id": "78", "type": "movie"}},
		"item_get":                 {{"id": blade}},
		"item_identify":            {{"id": blade, "kind": "movie"}, {"id": messyMononoke, "kind": "movie"}},
		"item_instant_mix":         {{"id": blade, "limit": 5}},
		"item_last_watched":        {{"id": mononoke}},
		"item_similar":             {{"id": blade, "limit": 5}},
		"item_subtitle_search":     {{"id": blade, "language": "eng"}},
		"library_episodes":         {{"series_id": severance}},
		// writes a file on this machine, and never over one: the server is
		// what must not change
		"library_export":      {{"path": filepath.Join(t.TempDir(), "movies.jsonl"), "library": "Movies"}},
		"library_filters":     {{"library": "Movies"}},
		"library_get":         {{"library": "Movies"}},
		"library_items":       {{"library": "Movies", "user": "alice", "watched": "unwatched"}, {"query": "Princess"}},
		"library_list":        {nil},
		"person_get":          {{"person": "Scott"}, {"person": "Ridley Scott"}},
		"plan_check":          {{"library": "Movies", "entries": []map[string]any{{"path": acc.Str(suite.Call(t, "item_get", map[string]any{"id": arrival})["path"])}}}},
		"playlist_get":        {{"playlist": pl}},
		"playlist_list":       {nil},
		"quality_compare":     {{"a": map[string]any{"item_id": arrival}, "b": map[string]any{"item_id": messyArrival}}},
		"server_activity":     {{"days": 1, "limit": 5}, {"item": mononoke, "days": 7}, {"user": "alice", "days": 1}},
		"server_devices":      {nil},
		"server_info":         {nil},
		"server_log":          {{"lines": 5}},
		"server_stats":        {nil},
		"session_list":        {nil},
		"show_episodes_exist": {{"series_id": severance, "episodes": []map[string]any{{"season": 1, "episode": 1}}}},
		"show_missing":        {{"series_id": severance}},
		"show_resolve":        {{"title": "Severance.S02E07.1080p.ATVP.WEB-DL.H264-GROUP", "library": "Shows"}},
		"show_seasons":        {{"series_id": severance}},
		"task_list":           {nil},
		"user_get":            {{"user": "alice"}},
		"user_history":        {{"user": "alice"}},
		"user_list":           {nil},
		"user_next_up":        {{"user": "alice"}},
		"user_stats":          {{"user": "alice"}},
	}
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var readOnly []string
	for _, tool := range res.Tools {
		if kindOf(t, tool) == "read" {
			readOnly = append(readOnly, tool.Name)
		}
	}
	for _, name := range readOnly {
		if _, ok := lookups[name]; !ok {
			t.Errorf("%s is read-only and has no lookup here: give it one", name)
		}
	}
	for name, calls := range lookups {
		if !slices.Contains(readOnly, name) {
			t.Errorf("%s has a lookup here, but the server does not mark it read-only", name)
			continue
		}
		for _, args := range calls {
			// Jellyfin's trickplay tiles are not read yet, and there the
			// audit says so (TestPreviewThumbnailsFoundAndMadeAgain)
			if isJellyfin() && name == "audit_previews" {
				if msg := suite.CallErr(t, name, args); !strings.Contains(msg, "read on Emby alone so far") {
					t.Errorf("audit_previews on Jellyfin said: %s", msg)
				}

				continue
			}
			suite.Call(t, name, args)
		}
	}

	after := snapshot()
	for k, v := range before {
		if !reflect.DeepEqual(v, after[k]) {
			b, _ := json.Marshal(v)
			a, _ := json.Marshal(after[k])
			t.Errorf("%s changed:\nbefore %s\nafter  %s", k, b, a)
		}
	}
}

// libraryIDs maps library names to the id a user's library access lists them
// by: Emby's Guid (its numeric id grants nothing), Jellyfin's ItemId.
func libraryIDs(t *testing.T) map[string]string {
	t.Helper()

	type folder struct {
		Name   string
		ItemID string `json:"ItemId"`
		GUID   string `json:"Guid"`
	}
	var folders []folder
	if isJellyfin() {
		status, raw := api(t, http.MethodGet, "/Library/VirtualFolders", "", nil)
		if status != http.StatusOK || json.Unmarshal(raw, &folders) != nil {
			t.Fatalf("listing libraries: HTTP %d: %.200s", status, raw)
		}
	} else {
		var page struct{ Items []folder }
		status, raw := api(t, http.MethodGet, "/Library/VirtualFolders/Query", "", nil)
		if status != http.StatusOK || json.Unmarshal(raw, &page) != nil {
			t.Fatalf("listing libraries: HTTP %d: %.200s", status, raw)
		}
		folders = page.Items
	}

	ids := map[string]string{}
	for _, f := range folders {
		ids[f.Name] = f.ItemID
		if f.GUID != "" {
			ids[f.Name] = f.GUID
		}
	}

	return ids
}

// restrictAlice lets alice see only the named libraries until the test ends.
// No tool changes a user's access, so it goes to the HTTP API.
func restrictAlice(t *testing.T, libraries ...string) {
	t.Helper()

	alice := os.Getenv("EMBYFIN_TEST_USER_ID")
	status, raw := api(t, http.MethodGet, "/Users/"+alice, "", nil)
	var user struct {
		Policy map[string]any
	}
	if status != http.StatusOK || json.Unmarshal(raw, &user) != nil || user.Policy == nil {
		t.Fatalf("reading alice: HTTP %d: %.200s", status, raw)
	}
	original, _ := json.Marshal(user.Policy)
	t.Cleanup(func() {
		var policy map[string]any
		_ = json.Unmarshal(original, &policy)
		if status, raw := api(t, http.MethodPost, "/Users/"+alice+"/Policy", "", policy); status/100 != 2 {
			t.Errorf("restoring alice's access: HTTP %d: %s", status, raw)
		}
	})

	ids := libraryIDs(t)
	enabled := []string{}
	for _, l := range libraries {
		enabled = append(enabled, ids[l])
	}
	user.Policy["EnableAllFolders"] = false
	user.Policy["EnabledFolders"] = enabled
	if status, raw := api(t, http.MethodPost, "/Users/"+alice+"/Policy", "", user.Policy); status/100 != 2 {
		t.Fatalf("restricting alice: HTTP %d: %s", status, raw)
	}
}

// A user who may see only Movies: user_get says so, and the tools that read
// a library in that user's view keep to it.
func TestUserLibraryAccess(t *testing.T) {
	restrictAlice(t, "Movies")

	got := suite.Call(t, "user_get", map[string]any{"user": "alice"})
	libs := acc.Strs(t, got["libraries"], "libraries")
	if acc.BoolOf(got["all_libraries"]) || !slices.Contains(libs, "Movies") || slices.Contains(libs, "Shows") || slices.Contains(libs, "Messy Movies") {
		t.Errorf("user_get alice: all_libraries %v, libraries %v, want Movies alone", got["all_libraries"], libs)
	}
	if n := acc.Num(t, suite.Call(t, "library_items", map[string]any{"library": "Movies", "user": "alice"})["total"], "total"); n != len(movies) {
		t.Errorf("alice sees %d films in Movies, want 8", n)
	}
	for tool, args := range map[string]map[string]any{
		"library_items": {"library": "Shows", "user": "alice"},
		"user_stats":    {"library": "Shows", "user": "alice"},
	} {
		if msg := suite.CallErr(t, tool, args); !strings.Contains(msg, "cannot see the Shows library") {
			t.Errorf("%s in a library alice cannot see: %s", tool, msg)
		}
	}
	// root still sees everything
	if root := suite.Call(t, "user_get", nil); !acc.BoolOf(root["all_libraries"]) {
		t.Errorf("root = %v", root)
	}
}

// Writes repeated: the second changes nothing, or says what it did.
func TestWritesTwice(t *testing.T) {
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	alien := findItem(t, "Movies", "Movie", "Alien")
	aliens := findItem(t, "Movies", "Movie", "Aliens")

	t.Run("watched and favourite", func(t *testing.T) {
		// Aliens: its tmdb id is its own, and Emby keeps watch state by
		// provider id, so marking a film with a copy elsewhere marks both
		before := suite.Call(t, "user_stats", map[string]any{"user": "alice"})
		unmarkLater(t, "alice", aliens)
		for range 2 {
			// both in one call
			if out := suite.Call(t, "item_set_state", map[string]any{"id": aliens, "user": "alice", "watched": true, "favourite": true}); !acc.BoolOf(out["watched"]) || !acc.BoolOf(out["favourite"]) || acc.Str(out["item"]) != "Aliens" {
				t.Errorf("item_set_state = %v", out)
			}
			after := suite.Call(t, "user_stats", map[string]any{"user": "alice"})
			if acc.Num(t, after["movies_watched"], "movies_watched") != acc.Num(t, before["movies_watched"], "movies_watched")+1 ||
				acc.Num(t, after["favourites"], "favourites") != acc.Num(t, before["favourites"], "favourites")+1 {
				t.Errorf("alice after marking Aliens: %v watched %v favourites, was %v and %v", after["movies_watched"], after["favourites"], before["movies_watched"], before["favourites"])
			}
		}
	})

	t.Run("collection add", func(t *testing.T) {
		col := acc.Str(suite.Call(t, "collection_create", map[string]any{"name": "Zzyzx Twice", "item_ids": []any{alien}})["id"])
		deleteLater(t, "collection_delete", "collection", col)
		if out := suite.Call(t, "collection_edit", map[string]any{"collection": col, "add_items": []any{alien, aliens}}); acc.Num(t, out["added"], "added") != 1 || acc.Num(t, out["already_held"], "already_held") != 1 {
			t.Errorf("adding a member and a new item = %v", out)
		}
		if out := suite.Call(t, "collection_edit", map[string]any{"collection": col, "add_items": []any{alien}}); acc.NumOr0(out["added"]) != 0 || acc.Num(t, out["already_held"], "already_held") != 1 {
			t.Errorf("adding a member again = %v", out)
		}
		if n := collectionSize(t, col, 2); n != 2 {
			t.Errorf("the collection holds %d, want 2", n)
		}
	})

	t.Run("playlist add", func(t *testing.T) {
		pl := acc.Str(suite.Call(t, "playlist_create", map[string]any{"name": "Zzyzx Twice", "item_ids": []any{alien}, "media_type": "Video"})["id"])
		deleteLater(t, "playlist_delete", "playlist", pl)
		// a playlist may hold an item twice
		if out := suite.Call(t, "playlist_edit", map[string]any{"playlist": pl, "add_items": []any{alien}}); acc.Num(t, out["added"], "added") != 1 {
			t.Errorf("adding it again = %v", out)
		}
		entries := playlistEntries(t, pl, 2)
		if len(entries) != 2 || acc.Str(entries[0]["name"]) != "Alien" || acc.Str(entries[1]["name"]) != "Alien" {
			t.Fatalf("the playlist = %v", entries)
		}
		// removing one entry: Emby numbers them, Jellyfin's is the item id, so
		// there it takes both
		want := 1
		if isJellyfin() {
			want = 2
		}
		// on Emby each entry has its own id, and an old id of one could name
		// the other after a change: the playlist's fingerprint is needed
		remove := map[string]any{"playlist": pl, "remove_entries": []any{map[string]any{"entry_id": acc.Str(entries[0]["entry_id"]), "item_id": alien}}}
		if !isJellyfin() {
			if msg := suite.CallErr(t, "playlist_edit", remove); !strings.Contains(msg, "more than once") || !strings.Contains(msg, "fingerprint") {
				t.Errorf("removing one of two entries of an item without the fingerprint: %s", msg)
			}
		}
		remove["fingerprint"] = acc.Str(suite.Call(t, "playlist_get", map[string]any{"playlist": pl})["fingerprint"])
		// on Jellyfin the entry id names both, which it removes together:
		// refused unless all_copies says to take both
		if isJellyfin() {
			if msg := suite.CallErr(t, "playlist_edit", remove); !strings.Contains(msg, "names all 2 entries of Alien") || !strings.Contains(msg, "pass all_copies") {
				t.Errorf("removing one entry of an item held twice on Jellyfin: %s", msg)
			}
			if n := len(playlistEntries(t, pl, 2)); n != 2 {
				t.Fatalf("the refused removal left %d entries, want both", n)
			}
			remove["all_copies"] = true
		}
		if out := suite.Call(t, "playlist_edit", remove); acc.Num(t, out["removed"], "removed") != want {
			t.Errorf("removing one of the two = %v, want %d removed", out, want)
		}
		if n := len(playlistEntries(t, pl, 2-want)); n != 2-want {
			t.Errorf("the playlist holds %d, want %d", n, 2-want)
		}
	})

	t.Run("metadata rename", func(t *testing.T) {
		keepFiles(t, itemFolder(t, arrival))
		suite.PutBack(t, "item_edit", map[string]any{"ids": []any{arrival}, "remove_tags": []any{"zzyzx-twice", "zzyzx-once"}})
		suite.Call(t, "item_edit", map[string]any{"ids": []any{arrival}, "add_tags": []any{"zzyzx-twice"}})
		rename := map[string]any{"field": "tags", "from": "zzyzx-twice", "to": "zzyzx-once", "library": "Movies"}
		if out := suite.Call(t, "metadata_rename", rename); acc.Num(t, out["updated"], "updated") != 1 {
			t.Errorf("the rename = %v", out)
		}
		if out := suite.Call(t, "metadata_rename", rename); acc.Num(t, out["updated"], "updated") != 0 {
			t.Errorf("the rename again = %v", out)
		}
		if tags := acc.Strs(t, suite.Call(t, "item_get", map[string]any{"id": arrival})["tags"], "tags"); !slices.Contains(tags, "zzyzx-once") || slices.Contains(tags, "zzyzx-twice") {
			t.Errorf("after the rename Arrival's tags are %v, want zzyzx-once and no zzyzx-twice", tags)
		}
	})
}

// Everything that resolves by name resolves by id to the same thing, and a
// name several share is refused with their ids.
func TestResolveByIDAndName(t *testing.T) {
	for _, l := range acc.Rows(t, suite.Call(t, "library_list", nil)["libraries"], "libraries") {
		byID := suite.Call(t, "library_get", map[string]any{"library": acc.Str(l["id"])})
		byName := suite.Call(t, "library_get", map[string]any{"library": acc.Str(l["name"])})
		if acc.Str(byID["name"]) != acc.Str(l["name"]) || acc.Str(byName["id"]) != acc.Str(l["id"]) {
			t.Errorf("library %v: by id %v, by name %v", l["name"], byID["name"], byName["id"])
		}
	}
	for _, u := range acc.Rows(t, suite.Call(t, "user_list", nil)["users"], "users") {
		byID := suite.Call(t, "user_get", map[string]any{"user": acc.Str(u["id"])})
		byName := suite.Call(t, "user_get", map[string]any{"user": acc.Str(u["name"])})
		if acc.Str(byID["name"]) != acc.Str(u["name"]) || acc.Str(byName["id"]) != acc.Str(u["id"]) {
			t.Errorf("user %v: by id %v, by name %v", u["name"], byID["name"], byName["id"])
		}
	}
	// a part of a name answers with the people it could mean (Emby lists
	// Adam Scott too), each by name and id
	var ridley string
	for _, p := range acc.Rows(t, suite.Call(t, "person_get", map[string]any{"person": "Scott"})["candidates"], "candidates") {
		if acc.Str(p["name"]) == "Ridley Scott" {
			ridley = acc.Str(p["id"])
		}
	}
	if ridley == "" {
		t.Fatal("person_get offered no Ridley Scott for Scott")
	}
	if byID := suite.Call(t, "person_get", map[string]any{"person": ridley}); acc.Str(byID["name"]) != "Ridley Scott" {
		t.Errorf("person_get by id = %v", byID["name"])
	}
	if byName := suite.Call(t, "person_get", map[string]any{"person": "Ridley Scott"}); acc.Str(byName["id"]) != ridley {
		t.Errorf("person_get by name = %v, want %s", byName["id"], ridley)
	}

	// the films named Dune, across libraries - the clean Dune and Dune: Part
	// Two and the messy Dune - each resolve to themselves
	dunes := acc.Rows(t, suite.Call(t, "library_items", map[string]any{"query": "Dune", "types": "Movie"})["items"], "items")
	if len(dunes) != 3 {
		t.Errorf("the films named Dune are %v, want the three", dunes)
	}
	for _, it := range dunes {
		got := suite.Call(t, "item_get", map[string]any{"id": acc.Str(it["id"])})
		if acc.Str(got["name"]) != acc.Str(it["name"]) || acc.Str(got["path"]) != acc.Str(it["path"]) {
			t.Errorf("item %v: item_get says %v at %v", it["id"], got["name"], got["path"])
		}
	}

	// two playlists may share a name: by name is then refused, with the ids
	var ids []string
	for range 2 {
		id := acc.Str(suite.Call(t, "playlist_create", map[string]any{"name": "Zzyzx Twin"})["id"])
		ids = append(ids, id)
		deleteLater(t, "playlist_delete", "playlist", id)
	}
	msg := suite.CallErr(t, "playlist_get", map[string]any{"playlist": "zzyzx twin"})
	if !strings.Contains(msg, "2 playlists are named") || !strings.Contains(msg, ids[0]) || !strings.Contains(msg, ids[1]) {
		t.Errorf("playlist_get by a shared name: %s", msg)
	}
	for _, id := range ids {
		if got := suite.Call(t, "playlist_get", map[string]any{"playlist": id}); acc.Str(got["name"]) != "Zzyzx Twin" {
			t.Errorf("playlist_get by id %s = %v", id, got["name"])
		}
	}

	// a collection is a folder named after it: the servers answer a second
	// create under the name with the first (Jellyfin replacing its items), so
	// the tool refuses one and the first keeps what it held
	alien := findItem(t, "Movies", "Movie", "Alien")
	aliens := findItem(t, "Movies", "Movie", "Aliens")
	first := acc.Str(suite.Call(t, "collection_create", map[string]any{"name": "Zzyzx Twin", "item_ids": []any{alien}})["id"])
	deleteLater(t, "collection_delete", "collection", first)
	if msg := suite.CallErr(t, "collection_create", map[string]any{"name": "zzyzx twin", "item_ids": []any{aliens}}); !strings.Contains(msg, "exists") || !strings.Contains(msg, first) {
		t.Errorf("a second collection named Zzyzx Twin: %s", msg)
	}
	if byName := suite.Call(t, "collection_get", map[string]any{"collection": "Zzyzx Twin"}); fmt.Sprint(names(t, byName["items"], "items")) != fmt.Sprint([]string{"Alien"}) {
		t.Errorf("collection_get by name = %v", byName["items"])
	}
	// a collection with nothing in it: Emby cannot make one, and the tool says
	// so; Jellyfin makes it, and it lists, holding nothing
	if !isJellyfin() {
		if msg := suite.CallErr(t, "collection_create", map[string]any{"name": "Zzyzx Empty"}); !strings.Contains(msg, "empty collection") {
			t.Errorf("an empty collection on Emby: %s", msg)
		}
		return
	}
	empty := acc.Str(suite.Call(t, "collection_create", map[string]any{"name": "Zzyzx Empty"})["id"])
	// deleted at once, while the refresh Jellyfin starts on a new collection
	// is still running: collection_delete answers only once it stays gone
	t.Cleanup(func() {
		if _, err := suite.Invoke("collection_delete", map[string]any{"collection": empty}); err != nil {
			t.Errorf("deleting the empty collection: %v", err)
		}
		if _, err := suite.Invoke("collection_get", map[string]any{"collection": empty}); err == nil {
			t.Error("the empty collection is back after collection_delete answered")
		}
	})
	if got := suite.Call(t, "collection_get", map[string]any{"collection": empty}); len(acc.Rows(t, got["items"], "items")) != 0 {
		t.Errorf("the empty collection holds %v", got["items"])
	}
	listed := false
	for _, c := range acc.Rows(t, suite.Call(t, "collection_list", nil)["collections"], "collections") {
		listed = listed || acc.Str(c["id"]) == empty
	}
	if !listed {
		t.Error("the empty collection is not listed")
	}
}

// plotOf reads the plot an nfo gives.
var plotOf = regexp.MustCompile(`(?s)<plot>(.*?)</plot>`)

// An edit is kept until someone replaces it: a refresh fills only what is
// missing, so an edit survives one, on a film the providers matched and on
// one built from its nfo alone. A film whose file is written over is read
// again whole by the next scan, and there the servers differ: Jellyfin saved
// the edit into the nfo beside the file and reads it back, and Emby, whose
// libraries here save no nfo, reads the nfo's own metadata back over the
// edit - an upgrade written in place loses what was typed on Emby unless the
// nfo carries it. Each step is waited on until the server has done it, so
// the check comes after the refresh or re-read rather than before it.
// replace_all puts the providers' metadata back, and is refused where there
// are no providers to ask.
func TestEditsSurviveARefreshAndAScan(t *testing.T) {
	for _, c := range []struct{ library, title string }{{"Movies", "Princess Mononoke"}, {"Messy Movies", "Interstellar"}} {
		t.Run(c.library, func(t *testing.T) {
			id := findItem(t, c.library, "Movie", c.title)
			folder := itemFolder(t, id)
			var plot string
			for _, nfo := range nfos(t, folder) {
				raw, _ := os.ReadFile(nfo) //nolint:gosec // a fixture under the test data dir
				if m := plotOf.FindSubmatch(raw); m != nil {
					plot = string(m[1])
				}
			}
			keepFiles(t, folder)
			restoreLater(t, id)

			const overview = "Zzyzx: an overview typed by hand."
			edit := func() {
				t.Helper()
				suite.Call(t, "item_edit", map[string]any{"ids": []any{id}, "overview": overview})
				suite.Call(t, "item_edit", map[string]any{"ids": []any{id}, "add_tags": []any{"zzyzx-kept"}, "add_genres": []any{"Zzyzx Kept"}})
			}
			edit()
			edited := func() bool {
				got := suite.Call(t, "item_get", map[string]any{"id": id})
				return acc.Str(got["overview"]) == overview && slices.Contains(acc.Strs(t, got["tags"], "tags"), "zzyzx-kept") && slices.Contains(acc.Strs(t, got["genres"], "genres"), "Zzyzx Kept")
			}
			if !edited() {
				t.Fatalf("the edit did not land: %v", suite.Call(t, "item_get", map[string]any{"id": id}))
			}

			if !refreshed(t, id) {
				t.Fatal("the refresh never ran")
			}
			if !acc.Holds(edited) {
				t.Errorf("item_refresh undid the edit: %v", suite.Call(t, "item_get", map[string]any{"id": id}))
			}

			// the file written again, as a download that replaces it would:
			// the scan re-reads it and saves the film
			file := hostPath(acc.Str(suite.Call(t, "item_get", map[string]any{"id": id})["path"]))
			now := time.Now()
			if err := os.Chtimes(file, now, now); err != nil {
				t.Fatal(err)
			}
			was := itemEtag(t, id)
			if err := suite.WaitForScan(); err != nil {
				t.Fatal(err)
			}
			suite.Call(t, "library_scan", nil)
			if !savedSince(t, id, was) {
				t.Fatal("the scan never re-read the film whose file changed")
			}
			if err := suite.WaitForScan(); err != nil {
				t.Fatal(err)
			}
			if isJellyfin() {
				if !acc.Holds(edited) {
					t.Errorf("a scan that re-read the film undid the edit: %v", suite.Call(t, "item_get", map[string]any{"id": id}))
				}
			} else {
				got := suite.Call(t, "item_get", map[string]any{"id": id})
				if acc.Str(got["overview"]) != plot || plot == "" || slices.Contains(acc.Strs(t, got["tags"], "tags"), "zzyzx-kept") {
					t.Errorf("after a scan re-read the film its overview is %q and tags %v, want the nfo's plot %q back over the edit", got["overview"], got["tags"], plot)
				}
				// typed again, for what follows
				edit()
				if !edited() {
					t.Fatalf("the edit did not land again: %v", suite.Call(t, "item_get", map[string]any{"id": id}))
				}
			}

			if c.library == "Messy Movies" {
				// Jellyfin would clear the film rather than re-read its nfo
				if msg := suite.CallErr(t, "item_refresh", map[string]any{"id": id, "replace_all": true}); !strings.Contains(msg, "metadata fetchers off") {
					t.Errorf("replace_all without fetchers: %s", msg)
				}
				if !edited() {
					t.Errorf("the refused replace_all changed the film: %v", suite.Call(t, "item_get", map[string]any{"id": id}))
				}
				return
			}
			// and item_refresh answers once the refresh has run: what it
			// replaced is gone at the first read after
			if out := suite.Call(t, "item_refresh", map[string]any{"id": id, "replace_all": true}); !acc.BoolOf(out["landed"]) {
				t.Fatalf("item_refresh replace_all = %v, want it seen to land", out)
			}
			if got := suite.Call(t, "item_get", map[string]any{"id": id}); acc.Str(got["overview"]) == overview || acc.Str(got["overview"]) == "" || slices.Contains(acc.Strs(t, got["tags"], "tags"), "zzyzx-kept") {
				t.Errorf("replace_all kept the edit: %v", got)
			}
		})
	}
}

// A client calls tools in parallel: edits of one item, adds to one playlist
// or collection, removals, and a move racing an add all land, none undoing
// another.
func TestParallelWrites(t *testing.T) {
	var films []any
	for _, name := range []string{"Aliens", "Blade Runner", "Dune", "Arrival", "The Thirteenth Floor"} {
		films = append(films, findItem(t, "Movies", "Movie", name))
	}
	alien := findItem(t, "Movies", "Movie", "Alien")

	t.Run("edits of one item", func(t *testing.T) {
		id := findItem(t, "Messy Movies", "Movie", "Arrival")
		keepFiles(t, itemFolder(t, id))
		restoreLater(t, id)
		var tags []any
		for i := range 6 {
			tags = append(tags, fmt.Sprintf("zzyzx-parallel-%d", i))
		}

		var wg sync.WaitGroup
		for _, tag := range tags {
			wg.Go(func() {
				if _, err := suite.Invoke("item_edit", map[string]any{"ids": []any{id}, "add_tags": []any{tag}}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Go(func() {
			if _, err := suite.Invoke("item_edit", map[string]any{"ids": []any{id}, "overview": "Zzyzx: edited alongside."}); err != nil {
				t.Error(err)
			}
		})
		wg.Wait()

		got := suite.Call(t, "item_get", map[string]any{"id": id})
		for _, tag := range tags {
			if !slices.Contains(acc.Strs(t, got["tags"], "tags"), tag.(string)) {
				t.Errorf("tag %s was lost: the tags are %v", tag, got["tags"])
			}
		}
		if acc.Str(got["overview"]) != "Zzyzx: edited alongside." {
			t.Errorf("the overview edit was lost: %v", got["overview"])
		}
	})

	t.Run("a playlist", func(t *testing.T) {
		pl := acc.Str(suite.Call(t, "playlist_create", map[string]any{"name": "Zzyzx Parallel", "item_ids": []any{alien}, "media_type": "Video"})["id"])
		deleteLater(t, "playlist_delete", "playlist", pl)

		var wg sync.WaitGroup
		for _, id := range films {
			wg.Go(func() {
				if _, err := suite.Invoke("playlist_edit", map[string]any{"playlist": pl, "add_items": []any{id}}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		entries := playlistEntries(t, pl, 6)
		if len(entries) != 6 {
			t.Fatalf("after five adds at once the playlist holds %v", names(t, suite.Call(t, "playlist_get", map[string]any{"playlist": pl})["entries"], "entries"))
		}

		// move the last to the top while another film is added
		last := entries[5]
		extra := findItem(t, "Movies", "Movie", "Dune: Part Two")
		wg.Go(func() {
			if _, err := suite.Invoke("playlist_edit", map[string]any{"playlist": pl, "move_entry_id": acc.Str(last["entry_id"]), "move_item_id": acc.Str(last["id"]), "position": 1}); err != nil {
				t.Errorf("the move: %v", err)
			}
		})
		wg.Go(func() {
			if _, err := suite.Invoke("playlist_edit", map[string]any{"playlist": pl, "add_items": []any{extra}}); err != nil {
				t.Errorf("the add: %v", err)
			}
		})
		wg.Wait()
		got := names(t, suite.Call(t, "playlist_get", map[string]any{"playlist": pl})["entries"], "entries")
		if len(got) != 7 || got[0] != acc.Str(last["name"]) || !slices.Contains(got, "Dune: Part Two") {
			t.Errorf("after a move and an add at once the playlist is %v, want %s first and Dune: Part Two in it", got, last["name"])
		}
	})

	t.Run("a collection", func(t *testing.T) {
		col := acc.Str(suite.Call(t, "collection_create", map[string]any{"name": "Zzyzx Parallel", "item_ids": []any{alien}})["id"])
		deleteLater(t, "collection_delete", "collection", col)

		var wg sync.WaitGroup
		for _, id := range films {
			wg.Go(func() {
				if _, err := suite.Invoke("collection_edit", map[string]any{"collection": col, "add_items": []any{id}}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if n := collectionSize(t, col, 6); n != 6 {
			t.Fatalf("after five adds at once the collection holds %d", n)
		}
		for _, id := range films[:3] {
			wg.Go(func() {
				if _, err := suite.Invoke("collection_edit", map[string]any{"collection": col, "remove_items": []any{id}}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if got := names(t, suite.Call(t, "collection_get", map[string]any{"collection": col})["items"], "items"); !slices.Equal(acc.Sorted(got), []string{"Alien", "Arrival", "The Thirteenth Floor"}) {
			t.Errorf("after three removals at once the collection holds %v", got)
		}
	})
}

// copyFixture copies a fixture folder under a new name, renaming the files
// named after it, so a scan picks up another copy of the film.
func copyFixture(t *testing.T, src, dst string) {
	t.Helper()

	copyRenamed(t, src, dst, filepath.Base(src), filepath.Base(dst))
}

// copyRenamed copies a folder and the folders in it, a film's extras among
// them, renaming what is named after the film.
func copyRenamed(t *testing.T, src, dst, from, to string) {
	t.Helper()

	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	env.Mkdir(t, testenv.DataDir(), dst)
	for _, e := range entries {
		name := strings.ReplaceAll(e.Name(), from, to)
		if e.IsDir() {
			copyRenamed(t, filepath.Join(src, e.Name()), filepath.Join(dst, name), from, to)
			continue
		}
		raw, err := os.ReadFile(filepath.Join(src, e.Name())) //nolint:gosec // a fixture under the test data dir
		if err != nil {
			t.Fatal(err)
		}
		env.WriteFile(t, filepath.Join(dst, name), raw)
	}
}

// Pruning a copy the duplicates audit found, and removing a whole library:
// the playlist, collection and favourites that held their items let go of
// them, and the audits and lookups count one fewer.
func TestDeletesLeaveNothingBehind(t *testing.T) {
	if testenv.DataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	dune := findItem(t, "Movies", "Movie", "Dune")
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	alienCopies := func() int {
		return len(acc.Rows(t, suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "348", "type": "movie"})["items"], "items"))
	}
	// the messy Aliens as the server shows them: Jellyfin as separate
	// entries, which audit_duplicates groups; Emby as one film's versions,
	// merging every copy that shares the TMDB id
	alienGroup := func() int {
		if !isJellyfin() {
			for _, f := range acc.RowsOf(suite.Call(t, "audit_multiple_versions", map[string]any{"library": "Messy Movies"})["findings"]) {
				if title(acc.Str(f["name"])) == "Alien" {
					n, _ := strconv.Atoi(strings.SplitN(acc.Str(f["detail"]), " ", 2)[0])
					return n
				}
			}
			return 0
		}
		groups, _ := suite.Call(t, "audit_duplicates", map[string]any{"library": "Messy Movies"})["groups"].([]any)
		for _, g := range groups {
			if group := acc.RowsOf(g); len(group) > 0 && acc.Str(group[0]["name"]) == "Alien" {
				return len(group)
			}
		}
		return 0
	}
	// the lists are named after the test holding them: Emby cannot recreate a
	// collection under the name of the first it created, once deleted
	held := func(name, id string) (playlist, collection, favourite bool) {
		for _, p := range acc.RowsOf(suite.Call(t, "playlist_list", nil)["playlists"]) {
			if acc.Str(p["name"]) == name {
				for _, e := range acc.RowsOf(suite.Call(t, "playlist_get", map[string]any{"playlist": acc.Str(p["id"])})["entries"]) {
					playlist = playlist || acc.Str(e["id"]) == id
				}
			}
		}
		for _, it := range acc.RowsOf(suite.Call(t, "collection_get", map[string]any{"collection": name})["items"]) {
			collection = collection || acc.Str(it["id"]) == id
		}
		for _, it := range acc.RowsOf(suite.Call(t, "library_items", map[string]any{"user": "alice", "watched": "favourite"})["items"]) {
			favourite = favourite || acc.Str(it["id"]) == id
		}
		return playlist, collection, favourite
	}
	// favourite too, for a film with no other copy: Emby marks every copy and
	// its favourites list then shows only some of them
	hold := func(t *testing.T, name, id string, favourite bool) {
		t.Helper()
		pl := acc.Str(suite.Call(t, "playlist_create", map[string]any{"name": name, "item_ids": []any{id, dune}, "media_type": "Video"})["id"])
		deleteLater(t, "playlist_delete", "playlist", pl)
		col := acc.Str(suite.Call(t, "collection_create", map[string]any{"name": name, "item_ids": []any{id, arrival}})["id"])
		deleteLater(t, "collection_delete", "collection", col)
		if favourite {
			suite.Call(t, "item_set_state", map[string]any{"id": id, "user": "alice", "favourite": true})
		}
		if p, c, f := held(name, id); !p || !c || f != favourite {
			t.Fatalf("the item is not held: playlist %v collection %v favourite %v", p, c, f)
		}
	}

	// the copy staged beside the two messy Aliens: on Emby the three are one
	// film's versions, and the server's view of the copy lists the other two
	// folders' files, so the delete must still reach no further than the
	// copy's own folder
	t.Run("a duplicate pruned", func(t *testing.T) {
		have := typeCount(t, "Messy Movies", "Movie")
		copies, group := alienCopies(), alienGroup()
		const copyName = "Alien (1979) Copy"
		dst := filepath.Join(testenv.DataDir(), "messy-movies", copyName)
		// every other copy's files, on disk, to hold them to after the delete
		kept := map[string][]byte{}
		for _, dir := range []string{filepath.Join(testenv.DataDir(), "messy-movies", messyAlien), filepath.Join(testenv.DataDir(), "messy-movies", messyAlienCut), filepath.Join(testenv.DataDir(), "movies", "Alien (1979)")} {
			_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					kept[path], _ = os.ReadFile(path) //nolint:gosec // a fixture under the test data dir
				}
				return nil
			})
		}
		copyFixture(t, filepath.Join(testenv.DataDir(), "messy-movies", messyAlien), dst)
		t.Cleanup(func() {
			if err := os.RemoveAll(dst); err != nil {
				t.Error(err)
			}
			if err := scanUntil("Messy Movies", have); err != nil {
				t.Error(err)
			}
		})
		if err := scanUntil("Messy Movies", have+1); err != nil {
			t.Fatal(err)
		}
		var id string
		for _, it := range acc.Rows(t, suite.Call(t, "library_items", map[string]any{"library": "Messy Movies", "query": "Alien", "limit": 50})["items"], "items") {
			if strings.Contains(acc.Str(it["path"]), "/"+copyName+"/") {
				id = acc.Str(it["id"])
			}
		}
		if id == "" {
			t.Fatal("the copy was not scanned in")
		}
		if alienCopies() != copies+1 || alienGroup() != group+1 {
			t.Fatalf("with the copy: %d copies in a group of %d, was %d in %d", alienCopies(), alienGroup(), copies, group)
		}
		hold(t, "Zzyzx Pruned", id, false)

		server := "/media/messy-movies/" + copyName
		msg := suite.CallErr(t, "item_delete", map[string]any{"id": id})
		if !strings.Contains(msg, "would remove the folder "+server+" with everything in it") || strings.Contains(msg, messyAlienCut) || strings.Contains(msg, "/media/movies/") {
			t.Errorf("the refusal names more than the copy's folder: %s", msg)
		}
		out := suite.Call(t, "item_delete", map[string]any{"id": id, "confirm": true})
		got := removedPaths(t, out)
		if !slices.Contains(got, server+"/") || !slices.Contains(got, server+"/"+copyName+".mp4") {
			t.Errorf("removed = %v, want the copy's folder and file", got)
		}
		for _, p := range got {
			if !strings.HasPrefix(p, server+"/") {
				t.Errorf("removed names %s, outside the copy's folder", p)
			}
		}
		// every other copy's every file is where it was, as it was
		for path, raw := range kept {
			if now, err := os.ReadFile(path); err != nil || !bytes.Equal(now, raw) { //nolint:gosec // same
				t.Errorf("%s went or changed with the copy's delete: %v", path, err)
			}
		}
		if err := waitForItems("Messy Movies", have); err != nil {
			t.Fatal(err)
		}
		if p, c, f := held("Zzyzx Pruned", id); p || c || f {
			t.Errorf("the deleted copy is still held: playlist %v collection %v favourite %v", p, c, f)
		}
		if alienCopies() != copies || alienGroup() != group {
			t.Errorf("after the delete: %d copies in a group of %d, want %d in %d", alienCopies(), alienGroup(), copies, group)
		}
		if msg := suite.CallErr(t, "item_get", map[string]any{"id": id}); !strings.Contains(msg, "no item") {
			t.Errorf("item_get of the deleted copy: %s", msg)
		}
		// and a scan finds nothing to bring back: the counts hold
		if err := scanUntil("Messy Movies", have); err != nil {
			t.Fatal(err)
		}
		if alienCopies() != copies || alienGroup() != group {
			t.Errorf("after a scan: %d copies in a group of %d, want %d in %d", alienCopies(), alienGroup(), copies, group)
		}
	})

	t.Run("a library removed", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(testenv.DataDir(), "movies", "Arrival (2016)", "Arrival (2016).mp4")) //nolint:gosec // a fixture under the test data dir
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(testenv.DataDir(), "ripple", "The Lord of the Rings The Return of the King (2003)")
		file := filepath.Join(dir, "The Lord of the Rings The Return of the King (2003).mp4")
		env.Mkdir(t, testenv.DataDir(), dir)
		env.WriteFile(t, file, raw)
		t.Cleanup(func() {
			if err := os.RemoveAll(filepath.Join(testenv.DataDir(), "ripple")); err != nil {
				t.Error(err)
			}
		})
		t.Cleanup(func() {
			// gone already when the test got as far as removing it
			if !slices.ContainsFunc(acc.Rows(t, suite.Call(t, "library_list", nil)["libraries"], "libraries"), func(l map[string]any) bool { return acc.Str(l["name"]) == "Ripple" }) {
				return
			}
			removeLibrary(t, "Ripple")
			if err := suite.WaitForScan(); err != nil {
				t.Error(err)
			}
		})
		suite.Call(t, "library_create", map[string]any{"name": "Ripple", "type": "movies", "paths": []any{"/media/ripple"}, "scan": true})
		var id string
		if !acc.Eventually(func() bool {
			out, err := suite.Invoke("library_items", map[string]any{"library": "Ripple"})
			if items := acc.RowsOf(out["items"]); err == nil && len(items) == 1 {
				id = acc.Str(items[0]["id"])
			}
			return id != ""
		}) {
			t.Fatal("the library never held its film")
		}
		if err := suite.WaitForScan(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			// the film goes with its library; one still there is unmarked
			if _, err := suite.Invoke("item_get", map[string]any{"id": id}); err != nil {
				return
			}
			if err := suite.Retried("item_set_state", map[string]any{"id": id, "user": "alice", "favourite": false}); err != nil {
				t.Errorf("unmarking the film: %v", err)
			}
		})
		hold(t, "Zzyzx Removed", id, true)
		favourites := acc.Num(t, suite.Call(t, "user_stats", map[string]any{"user": "alice"})["favourites"], "favourites")

		suite.Call(t, "library_delete", map[string]any{"library": "Ripple", "confirm": true})
		// the library goes; its folder and film stay on disk
		if _, err := os.Stat(file); err != nil {
			t.Errorf("removing the library took its film off the disk: %v", err)
		}
		// Jellyfin lets go of the items on the library scan the removal starts
		if !acc.Eventually(func() bool {
			p, c, f := held("Zzyzx Removed", id)
			_, err := suite.Invoke("item_get", map[string]any{"id": id})
			return !p && !c && !f && err != nil
		}) {
			p, c, f := held("Zzyzx Removed", id)
			t.Errorf("the removed library's film is still held: playlist %v collection %v favourite %v", p, c, f)
		}
		if n := acc.Num(t, suite.Call(t, "user_stats", map[string]any{"user": "alice"})["favourites"], "favourites"); n != favourites-1 {
			t.Errorf("alice has %d favourites, want %d", n, favourites-1)
		}
		if err := suite.WaitForScan(); err != nil {
			t.Fatal(err)
		}
		// and the scans since have not taken it either
		if _, err := os.Stat(file); err != nil {
			t.Errorf("after the removal's scan the film is off the disk: %v", err)
		}
	})
}

// auditCounts is audit_all's findings by audit, and its total under
// total_findings.
func auditCounts(t *testing.T, args map[string]any) map[string]int {
	t.Helper()

	out := suite.Call(t, "audit_all", args)
	counts := map[string]int{"total_findings": acc.Num(t, out["total_findings"], "total_findings")}
	for _, row := range acc.Rows(t, out["audits"], "audits") {
		counts[rowKey(row)] = acc.Num(t, row["findings"], "findings")
	}

	return counts
}

// plotless matches the plot an nfo gives, and its outline.
var plotless = regexp.MustCompile(`(?s)\s*<(?:plot|outline)>.*?</(?:plot|outline)>|\s*<(?:plot|outline)\s*/>`)

// nfos are the nfo files in a folder, failing when it holds none.
func nfos(t *testing.T, folder string) []string {
	t.Helper()

	found, err := filepath.Glob(filepath.Join(folder, "*.nfo"))
	if err != nil || len(found) == 0 {
		t.Fatalf("no nfo in %s: %v", folder, err)
	}

	return found
}

// The fixes the audits point to, where they can work and where they cannot.
// With the fetchers on, item_refresh fills a missing overview and
// item_identify re-matches a wrong edition, which also clears the duplicate
// the wrong match made. With them off, the tools say what they could not do
// rather than claiming it.
func TestAuditFixesWhereTheyPoint(t *testing.T) {
	// the film's nfo holds its plot, and on Emby, whose library saves no
	// nfo, it still does after the overview is cleared: a refresh re-reading
	// it would fill the overview with no provider asked. So the nfo loses its
	// plot for the test, and the overview can only come from TMDB.
	t.Run("a refresh fills a missing overview", func(t *testing.T) {
		arrival := findItem(t, "Movies", "Movie", "Arrival")
		folder := itemFolder(t, arrival)
		keepFiles(t, folder)
		restoreLater(t, arrival)
		updateItem(t, arrival, map[string]any{"Overview": ""})
		for _, nfo := range nfos(t, folder) {
			raw, err := os.ReadFile(nfo) //nolint:gosec // a fixture under the test data dir
			if err != nil {
				t.Fatal(err)
			}
			env.WriteFile(t, nfo, plotless.ReplaceAll(raw, nil))
		}
		if got := findings(t, missing(t, "overview", map[string]any{"library": "Movies"})); !slices.Equal(got, []string{"Arrival"}) {
			t.Fatalf("audit_missing_metadata overview = %v, want [Arrival]", got)
		}
		suite.Call(t, "item_refresh", map[string]any{"id": arrival})
		if !acc.Eventually(func() bool {
			return acc.Num(t, missing(t, "overview", map[string]any{"library": "Movies"})["total_findings"], "total_findings") == 0
		}) {
			t.Errorf("after item_refresh Arrival still has no overview: %v", suite.Call(t, "item_get", map[string]any{"id": arrival}))
		}
	})

	t.Run("identify re-matches a wrong edition", func(t *testing.T) {
		dune := findItem(t, "Movies", "Movie", "Dune")
		// the whole item goes back: the re-match replaces what the nfo gave
		// the film (its people, its genres) with TMDB's, and a later run's
		// item_get would read TMDB's cast where the nfo put the director
		folder := itemFolder(t, dune)
		keepFiles(t, folder)
		restoreLater(t, dune)
		// matched to David Lynch's film, which the messy Dune's nfo names too.
		// The nfo beside the file still names the 2021 film on Emby, which
		// saves none, and Emby's apply re-reads it: it goes for the test, so
		// what puts the film right is the match and not the nfo
		updateItem(t, dune, map[string]any{"ProviderIds": map[string]any{"Tmdb": "841", "Imdb": "tt0087182"}, "ProductionYear": 1984})
		for _, nfo := range nfos(t, folder) {
			if err := os.Remove(nfo); err != nil {
				t.Fatal(err)
			}
		}
		counts := auditCounts(t, nil)
		if got := findings(t, suite.Call(t, "audit_file_path", map[string]any{"library": "Movies"})); !slices.Equal(got, []string{"Dune"}) {
			t.Fatalf("audit_file_path = %v, want [Dune]", got)
		}
		if n := len(acc.Rows(t, suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "841", "type": "movie"})["items"], "items")); n != 2 {
			t.Fatalf("tmdb 841 matches %d films, want the clean and the messy Dune", n)
		}

		idx := -1
		for i, c := range acc.Rows(t, suite.Call(t, "item_identify", map[string]any{"id": dune, "kind": "movie", "year": 2021})["candidates"], "candidates") {
			if ids, _ := c["metadata_provider_ids"].(map[string]any); idx < 0 && acc.Str(ids["tmdb"]) == "438631" {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatal("the 2021 Dune is not a candidate")
		}
		applied := suite.Call(t, "item_identify_apply", withCandidateIDs(t, map[string]any{"id": dune, "kind": "movie", "candidate": idx, "year": 2021}))
		ids, _ := applied["metadata_provider_ids"].(map[string]any)
		if acc.Str(ids["tmdb"]) != "438631" || acc.Num(t, applied["year"], "year") != 2021 {
			t.Errorf("item_identify_apply = %v", applied)
		}
		// no nfo is left to warn of; on Emby the watch state follows the ids,
		// and the answer says so
		if note := beyondNfoUnseen(acc.Str(applied["note"])); strings.Contains(note, "nfo") || isJellyfin() != (note == "") {
			t.Errorf("item_identify_apply's note = %q", note)
		}

		if got := findings(t, suite.Call(t, "audit_file_path", map[string]any{"library": "Movies"})); len(got) != 0 {
			t.Errorf("after the re-match audit_file_path = %v", got)
		}
		if n := len(acc.Rows(t, suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "841", "type": "movie"})["items"], "items")); n != 1 {
			t.Errorf("after the re-match tmdb 841 matches %d films, want the messy Dune alone", n)
		}
		after := auditCounts(t, nil)
		for _, audit := range []string{"audit_file_path", "audit_duplicates"} {
			if after[audit] != counts[audit]-1 {
				t.Errorf("%s counted %d before the re-match and %d after, want one fewer", audit, counts[audit], after[audit])
			}
		}
	})

	t.Run("with the fetchers off", func(t *testing.T) {
		dune := findItem(t, "Messy Movies", "Movie", "Dune")
		keepFiles(t, itemFolder(t, dune))
		restoreLater(t, dune)

		idx := -1
		for i, c := range acc.Rows(t, suite.Call(t, "item_identify", map[string]any{"id": dune, "kind": "movie", "year": 2021})["candidates"], "candidates") {
			if ids, _ := c["metadata_provider_ids"].(map[string]any); idx < 0 && acc.Str(ids["tmdb"]) == "438631" {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatal("the 2021 Dune is not a candidate")
		}
		// the ids are set by a plain edit, on both servers: they change and
		// nothing is fetched, which the answer says
		out := suite.Call(t, "item_identify_apply", withCandidateIDs(t, map[string]any{"id": dune, "kind": "movie", "candidate": idx, "year": 2021}))
		note := acc.Str(out["note"])
		if ids, _ := out["metadata_provider_ids"].(map[string]any); acc.Str(ids["tmdb"]) != "438631" || !strings.Contains(note, "metadata fetchers off") {
			t.Errorf("item_identify_apply = %v", out)
		}
		if msg := suite.CallErr(t, "item_refresh", map[string]any{"id": dune, "replace_all": true}); !strings.Contains(msg, "metadata fetchers off") {
			t.Errorf("replace_all without fetchers: %s", msg)
		}
		tmdb := func() string {
			ids, _ := suite.Call(t, "item_get", map[string]any{"id": dune})["metadata_provider_ids"].(map[string]any)
			return acc.Str(ids["tmdb"])
		}
		if isJellyfin() {
			// Jellyfin saves the new ids to the nfo, so a refresh keeps them
			if strings.Contains(note, "does not save nfo") {
				t.Errorf("the note warns of an nfo on a library that saves them: %q", note)
			}
			was := itemEtag(t, dune)
			suite.Call(t, "item_refresh", map[string]any{"id": dune})
			if !savedSince(t, dune, was) {
				t.Fatal("the refresh never saved the film")
			}
			if !acc.Holds(func() bool { return tmdb() == "438631" }) {
				t.Errorf("a refresh after the apply left tmdb %s", tmdb())
			}
			return
		}
		// Emby's messy library saves no nfo, and the one beside the file names
		// Lynch's film: the note says so, and a refresh puts it back
		if !strings.Contains(note, "does not save nfo files") {
			t.Errorf("the note says nothing of the nfo a refresh reads: %q", note)
		}
		suite.Call(t, "item_refresh", map[string]any{"id": dune})
		if !acc.Eventually(func() bool { return tmdb() == "841" }) {
			t.Errorf("after a refresh the nfo's id did not come back: tmdb %s", tmdb())
		}
	})
}

// A genre put on a franchise shows at once in every tool that lists or
// filters by genre, follows a rename, and leaves them all once no item
// carries it.
func TestGenresFollowEdits(t *testing.T) {
	ids := []any{findItem(t, "Movies", "Movie", "Alien"), findItem(t, "Movies", "Movie", "Aliens")}
	suite.PutBack(t, "item_edit", map[string]any{"ids": ids, "remove_genres": []any{"Zzyzx Franchise", "Zzyzx Saga"}})
	carried := func(genre string, want ...string) {
		t.Helper()
		if n := valueCounts(t, suite.Call(t, "library_filters", map[string]any{"library": "Movies"})["genres"], "genres")[genre]; n != len(want) {
			t.Errorf("library_filters counts %s on %d films, want %d", genre, n, len(want))
		}
		if got := names(t, suite.Call(t, "library_items", map[string]any{"library": "Movies", "genres": []any{genre}})["items"], "items"); !slices.Equal(acc.Sorted(got), want) {
			t.Errorf("library_items genre %s = %v, want %v", genre, got, want)
		}
		if got := names(t, suite.Call(t, "library_items", map[string]any{"query": "Alien", "genres": []any{genre}})["items"], "items"); !slices.Equal(acc.Sorted(got), want) {
			t.Errorf("library_items query+genre %s = %v, want %v", genre, got, want)
		}
		for _, args := range []map[string]any{{"library": "Movies"}, nil} {
			if _, listed := valueCounts(t, suite.Call(t, "library_filters", args)["genres"], "genres")[genre]; listed != (len(want) > 0) {
				t.Errorf("library_filters %v lists %s: %v, want %v", args, genre, listed, len(want) > 0)
			}
		}
	}

	suite.Call(t, "item_edit", map[string]any{"ids": ids, "add_genres": []any{"Zzyzx Franchise"}})
	carried("Zzyzx Franchise", "Alien", "Aliens")

	suite.Call(t, "metadata_rename", map[string]any{"field": "genres", "from": "Zzyzx Franchise", "to": "Zzyzx Saga", "library": "Movies"})
	carried("Zzyzx Franchise")
	carried("Zzyzx Saga", "Alien", "Aliens")

	suite.Call(t, "metadata_rename", map[string]any{"field": "genres", "from": "Zzyzx Saga", "remove": true, "library": "Movies"})
	carried("Zzyzx Saga")
}

// Copies of one film share its provider ids, and Emby keeps watch state and
// favourites by those ids: marking one copy there marks them all, where
// Jellyfin marks the one. Either way the counts are by film: one film
// watched, one favourite, and no copy of it left unwatched.
func TestCopiesCountOnce(t *testing.T) {
	alien := findItem(t, "Movies", "Movie", "Alien")
	copies := acc.Rows(t, suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "348", "type": "movie"})["items"], "items")
	if len(copies) != 3 {
		t.Fatalf("tmdb 348 has %d copies, want 3", len(copies))
	}
	for _, c := range copies {
		unmarkLater(t, "alice", acc.Str(c["id"]))
	}
	unwatchedAliens := func(library string) int {
		return len(slices.DeleteFunc(findings(t, suite.Call(t, "audit_unwatched", map[string]any{"library": library})), func(n string) bool { return n != "Alien" }))
	}
	for library, want := range map[string]int{"Movies": 1, "Messy Movies": 2} {
		if n := unwatchedAliens(library); n != want {
			t.Fatalf("audit_unwatched lists %d Aliens in %s before, want %d", n, library, want)
		}
	}
	stats := suite.Call(t, "user_stats", map[string]any{"user": "alice"})

	suite.Call(t, "item_set_state", map[string]any{"id": alien, "user": "alice", "watched": true, "favourite": true})

	for _, c := range copies {
		played := false
		for _, u := range acc.Rows(t, suite.Call(t, "item_last_watched", map[string]any{"id": acc.Str(c["id"])})["users"], "users") {
			played = played || (acc.Str(u["user"]) == "alice" && acc.BoolOf(u["played"]))
		}
		if want := !isJellyfin() || acc.Str(c["id"]) == alien; played != want {
			t.Errorf("the copy at %s is played for alice: %v, want %v", c["path"], played, want)
		}
	}
	// the copies count once, watched and favourited
	after := suite.Call(t, "user_stats", map[string]any{"user": "alice"})
	for _, field := range []string{"movies_watched", "favourites"} {
		if acc.Num(t, after[field], field) != acc.Num(t, stats[field], field)+1 {
			t.Errorf("alice's %s went from %v to %v, want one more", field, stats[field], after[field])
		}
	}
	for _, library := range []string{"Movies", "Messy Movies"} {
		if n := unwatchedAliens(library); n != 0 {
			t.Errorf("audit_unwatched still lists %d Aliens in %s", n, library)
		}
	}
}

// playThrough reports a play of an item from its start to its end, the way
// a client does, as the player the token signs in: stopped at the runtime
// the server holds for it.
func playThrough(t *testing.T, token, id string) {
	t.Helper()

	playTo(t, token, id, int64(acc.Num(t, suite.Call(t, "item_get", map[string]any{"id": id})["runtime_s"], "runtime_s"))*10_000_000)
}

// playTo plays an item as alice's player and stops it at a position, in
// ticks.
func playTo(t *testing.T, token, id string, stop int64) {
	t.Helper()

	status, raw := api(t, http.MethodPost, "/Items/"+id+"/PlaybackInfo?UserId="+os.Getenv("EMBYFIN_TEST_USER_ID"), token, map[string]any{})
	var info struct {
		PlaySessionID string `json:"PlaySessionId"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &info) != nil {
		t.Fatalf("PlaybackInfo: HTTP %d: %.200s", status, raw)
	}
	for _, step := range []struct {
		path  string
		ticks int64
	}{{"/Sessions/Playing", 0}, {"/Sessions/Playing/Progress", stop}, {"/Sessions/Playing/Stopped", stop}} {
		body := map[string]any{"ItemId": id, "PlaySessionId": info.PlaySessionID, "PositionTicks": step.ticks, "CanSeek": true, "PlayMethod": "DirectPlay"}
		if status, raw := api(t, http.MethodPost, step.path, token, body); status/100 != 2 {
			t.Fatalf("%s: HTTP %d: %s", step.path, status, raw)
		}
	}
}

// Watching a series through: next up moves along an episode at a time, an
// episode started is in progress until it is played, and the last one
// finishes the series in the stats and takes it off next up.
func TestFinishingASeries(t *testing.T) {
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	byNumber := map[int]string{}
	for _, e := range acc.Rows(t, suite.Call(t, "library_episodes", map[string]any{"series_id": series})["episodes"], "episodes") {
		byNumber[acc.Num(t, e["episode"], "episode")] = acc.Str(e["id"])
	}
	e1, e2, e3 := byNumber[1], byNumber[2], byNumber[3]
	unmarkLater(t, "alice", e1, e2, e3)
	ids := func(tool, field string) []string {
		var out []string
		for _, it := range acc.Rows(t, suite.Call(t, tool, map[string]any{"user": "alice"})[field], field) {
			out = append(out, acc.Str(it["id"]))
		}
		return out
	}
	breakingBad := func() map[string]any {
		for _, s := range acc.Rows(t, suite.Call(t, "user_stats", map[string]any{"user": "alice"})["top_series"], "top_series") {
			if acc.Str(s["name"]) == "Breaking Bad" {
				return s
			}
		}
		return nil
	}

	suite.Call(t, "item_set_state", map[string]any{"id": e1, "user": "alice", "watched": true})
	if next := ids("user_next_up", "next_up"); !slices.Contains(next, e2) {
		t.Errorf("after the pilot next up is %v, want episode two", next)
	}
	// Emby lists the next episode as resumable at zero; nothing is in progress
	if in := ids("user_next_up", "in_progress"); len(in) != 0 {
		t.Errorf("after marking the pilot watched alice has %v in progress", in)
	}
	if bb := breakingBad(); bb == nil || acc.Num(t, bb["episodes_watched"], "episodes_watched") != 1 || acc.BoolOf(bb["finished"]) {
		t.Errorf("Breaking Bad in the stats = %v", bb)
	}
	if got := findings(t, suite.Call(t, "audit_unwatched", map[string]any{"library": "Shows", "types": "Series"})); slices.Contains(got, "Breaking Bad") {
		t.Errorf("audit_unwatched lists Breaking Bad after an episode was watched: %v", got)
	}

	suite.Call(t, "item_set_state", map[string]any{"id": e2, "user": "alice", "position_s": 1})
	if in := ids("user_next_up", "in_progress"); !slices.Equal(in, []string{e2}) {
		t.Errorf("with episode two started alice has %v in progress", in)
	}

	_, token := signInPlayer(t)
	playThrough(t, token, e2)
	if !acc.Eventually(func() bool {
		return slices.Contains(ids("user_next_up", "next_up"), e3) && len(ids("user_next_up", "in_progress")) == 0
	}) {
		t.Errorf("after episode two was played next up is %v and in progress %v", ids("user_next_up", "next_up"), ids("user_next_up", "in_progress"))
	}

	suite.Call(t, "item_set_state", map[string]any{"id": e3, "user": "alice", "watched": true})
	for _, row := range acc.Rows(t, suite.Call(t, "user_next_up", map[string]any{"user": "alice"})["next_up"], "next_up") {
		if acc.Str(row["series"]) == "Breaking Bad" {
			t.Errorf("after the last episode next up still has Breaking Bad: %v", row)
		}
	}
	if bb := breakingBad(); bb == nil || acc.Num(t, bb["episodes_watched"], "episodes_watched") != 3 || !acc.BoolOf(bb["finished"]) {
		t.Errorf("Breaking Bad in the stats after the last episode = %v", bb)
	}
	if n := acc.Num(t, suite.Call(t, "user_stats", map[string]any{"user": "alice"})["series_finished"], "series_finished"); n != 1 {
		t.Errorf("alice has finished %d series, want 1", n)
	}
}

// A user who may see only Movies, across the tools that act in their name or
// report their watching: what they cannot see is not listed, not counted,
// and not changed.
func TestRestrictedUserAcrossTools(t *testing.T) {
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	eps := acc.Rows(t, suite.Call(t, "library_episodes", map[string]any{"series_id": series})["episodes"], "episodes")
	pilot, second := acc.Str(eps[0]["id"]), acc.Str(eps[1]["id"])
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	unmarkLater(t, "alice", pilot, second, arrival)
	// watched and favourited while alice could see everything
	suite.Call(t, "item_set_state", map[string]any{"id": pilot, "user": "alice", "watched": true})
	suite.Call(t, "item_set_state", map[string]any{"id": pilot, "user": "alice", "favourite": true})
	suite.Call(t, "item_set_state", map[string]any{"id": arrival, "user": "alice", "position_s": 1})

	// what each tool says of her while she can see it all, so what goes
	// once she cannot is known to have been there
	nextUp := func() []string {
		var out []string
		for _, e := range acc.Rows(t, suite.Call(t, "user_next_up", map[string]any{"user": "alice"})["next_up"], "next_up") {
			out = append(out, acc.Str(e["id"]))
		}
		return out
	}
	// episodes too: with no library the default is films and series, which
	// would find no favourite episode whoever could see it
	favourites := func() []string {
		var out []string
		for _, it := range acc.Rows(t, suite.Call(t, "library_items", map[string]any{"user": "alice", "watched": "favourite", "types": "Movie,Series,Episode"})["items"], "items") {
			out = append(out, acc.Str(it["id"]))
		}
		return out
	}
	watchers := func() map[string]bool {
		out := map[string]bool{}
		for _, u := range acc.Rows(t, suite.Call(t, "item_last_watched", map[string]any{"id": pilot})["users"], "users") {
			out[acc.Str(u["user"])] = acc.BoolOf(u["played"])
		}
		return out
	}
	if got := nextUp(); !slices.Equal(got, []string{second}) {
		t.Fatalf("before the restriction alice's next up is %v, want episode two alone", got)
	}
	if got := favourites(); !slices.Equal(got, []string{pilot}) {
		t.Fatalf("before the restriction alice's favourites are %v, want the pilot alone", got)
	}
	if got := suite.Call(t, "user_stats", map[string]any{"user": "alice"}); acc.Num(t, got["episodes_watched"], "episodes_watched") != 1 || acc.Num(t, got["favourites"], "favourites") != 1 {
		t.Fatalf("before the restriction user_stats alice = %v, want one episode and one favourite", got)
	}
	if got := watchers(); !got["alice"] {
		t.Fatalf("before the restriction item_last_watched of the pilot = %v, want alice played", got)
	}

	restrictAlice(t, "Movies")

	next := suite.Call(t, "user_next_up", map[string]any{"user": "alice"})
	if n := len(acc.Rows(t, next["next_up"], "next_up")); n != 0 {
		t.Errorf("alice has %d episodes next up in a library she cannot see", n)
	}
	if got := names(t, next["in_progress"], "in_progress"); !slices.Equal(got, []string{"Arrival"}) {
		t.Errorf("user_next_up in_progress = %v, want [Arrival]", got)
	}
	if got := favourites(); len(got) != 0 {
		t.Errorf("alice's favourites = %v, want none she can see", got)
	}
	if got := suite.Call(t, "user_stats", map[string]any{"user": "alice"}); acc.Num(t, got["episodes_watched"], "episodes_watched") != 0 || acc.Num(t, got["favourites"], "favourites") != 0 {
		t.Errorf("user_stats alice counts what she cannot see: %v", got)
	}
	// root, who can still see the pilot, is reported; so is alice, who
	// watched it before she lost the library, marked as one who has - as
	// audit_unwatched below still counts her watch
	got := watchers()
	if _, ok := got["root"]; !ok {
		t.Errorf("item_last_watched of the pilot = %v, want root's row", got)
	}
	var aliceRow map[string]any
	for _, u := range acc.Rows(t, suite.Call(t, "item_last_watched", map[string]any{"id": pilot})["users"], "users") {
		if acc.Str(u["user"]) == "alice" {
			aliceRow = u
		}
	}
	if aliceRow == nil || !acc.BoolOf(aliceRow["played"]) || !acc.BoolOf(aliceRow["no_access"]) {
		t.Errorf("item_last_watched of the pilot for alice = %v, want her watch, marked no_access", aliceRow)
	}
	for _, args := range []map[string]any{
		{"id": second, "user": "alice", "watched": true},
		{"id": second, "user": "alice", "favourite": true},
		{"id": second, "user": "alice", "position_s": 1},
	} {
		if msg := suite.CallErr(t, "item_set_state", args); !strings.Contains(msg, "alice cannot see") {
			t.Errorf("item_set_state %v on an episode alice cannot see: %s", args, msg)
		}
	}
	// alice watched the pilot before she lost the library, and a series
	// someone has watched is not one nobody has
	if got := findings(t, suite.Call(t, "audit_unwatched", map[string]any{"library": "Shows", "types": "Series"})); slices.Contains(got, "Breaking Bad") {
		t.Errorf("audit_unwatched = %v: alice's watch of the pilot stopped counting when she lost the library", got)
	}
}
