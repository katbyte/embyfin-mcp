//go:build integration

package acceptance

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	acc "github.com/katbyte/embyfin-mcp/lib/acceptance"

	"github.com/katbyte/embyfin-mcp/lib/testenv"
	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
)

// What a removed library leaves behind, and removing it.
//
// Jellyfin keeps a library's items when the library is removed without the
// scan that would drop them: the state a renamed library folder leaves on a
// real server, the items outside every library and their folder gone. Emby
// 4.10 has no route to that state: it drops a library's items with the
// library, and a folder's items with the folder the moment it leaves a
// library, scan or no scan - which the Emby branch shows, and then drives
// the tools through their refusals and a folder holding nothing. The request
// item_orphans_delete sends is run against both servers on its own
// (TestDeleteItemsInOneRequest), since no orphan on Emby can reach it.

// removeLibraryKeepingItems removes a Jellyfin library without the scan that
// drops its items, which leaves them as orphans. No tool does that - the tool
// asks for the scan - so it goes to the HTTP API.
func removeLibraryKeepingItems(t *testing.T, name string) {
	t.Helper()

	if status, body := api(t, http.MethodDelete, "/Library/VirtualFolders?name="+url.QueryEscape(name)+"&refreshLibrary=false", "", nil); status != http.StatusNoContent {
		t.Fatalf("removing the library: %d %s", status, body)
	}
}

// lostSight waits for the server to stop seeing a folder taken off the
// disk, which it can go on seeing for a moment through the bind mount.
func lostSight(t *testing.T, folder string) {
	t.Helper()

	if !acc.Eventually(func() bool {
		_, err := suite.Invoke("item_orphans_delete", map[string]any{"folder": folder})
		return err == nil
	}) {
		t.Fatalf("the server can still see %s after it was removed", folder)
	}
}

// orphanCount is how many items audit_orphans finds across the server.
func orphanCount(t *testing.T) int {
	t.Helper()

	return acc.Num(t, suite.Call(t, "audit_orphans", nil)["total_findings"], "total_findings")
}

// A library of two folders, one taken off the disk with the library gone and
// one still there: The Matrix and The Matrix Reloaded in the one removed,
// The Matrix Revolutions in the one kept. The removed folder is named so that
// it begins the name of a folder a live library reads (/media/movie beside
// the Movies library's /media/movies), the shape a renamed folder leaves, and
// nothing of the live library may be touched.
func TestOrphans(t *testing.T) {
	if testenv.DataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	movies := typeCount(t, "Movies", "Movie")

	// the fixtures leave nothing outside a library, so anything reported here
	// is something the sweep should not have counted
	if audit := suite.Call(t, "audit_orphans", nil); acc.Num(t, audit["total_findings"], "total_findings") != 0 || acc.Num(t, audit["items_scanned"], "items_scanned") == 0 {
		t.Errorf("before anything was removed: %v", audit)
	}

	// a folder on disk that no library reads: what is under it is real, so
	// the delete refuses it
	present := filepath.Join(testenv.DataDir(), "orphans-present")
	acc.MediaMkdir(t, testenv.DataDir(), present)
	t.Cleanup(func() { _ = os.RemoveAll(present) })
	for folder, want := range map[string]string{
		"/media/movies":                "inside the Movies library",
		"/media/movies/Arrival (2016)": "inside the Movies library",
		"/media":                       "holds the",
		"/media/orphans-present":       "can still see",
		"/media/movie/../movies":       "steps through",
		"/":                            "top of a filesystem",
		"movies":                       "full path",
	} {
		if msg := suite.CallErr(t, "item_orphans_delete", map[string]any{"folder": folder, "confirm": true}); !strings.Contains(msg, want) {
			t.Errorf("folder %s: %s", folder, msg)
		}
	}
	if msg := suite.CallErr(t, "item_orphans_delete", map[string]any{"folder": "/media/orphans-never", "limit": 10001}); !strings.Contains(msg, "more than the 10000") {
		t.Errorf("a limit over the most one call deletes: %s", msg)
	}

	// a library of two folders, the removed one holding two films and the
	// kept one a third
	raw := fixtureVideo(t, "movies", "Arrival (2016)", "Arrival (2016).mp4")
	removed := filepath.Join(testenv.DataDir(), "movie")
	kept := filepath.Join(testenv.DataDir(), "orphans-kept")
	films := map[string]string{"The Matrix (1999)": removed, "The Matrix Reloaded (2003)": removed, "The Matrix Revolutions (2003)": kept}
	for title, dir := range films {
		acc.MediaMkdir(t, testenv.DataDir(), filepath.Join(dir, title))
		acc.MediaWrite(t, filepath.Join(dir, title, title+".mp4"), raw)
	}
	const name, again = "Orphans Live", "Orphans Kept"
	t.Cleanup(func() {
		_ = os.RemoveAll(removed)
		_ = os.RemoveAll(kept)
		for _, library := range []string{name, again} {
			if _, err := suite.Invoke("library_get", map[string]any{"library": library}); err == nil {
				removeLibrary(t, library)
			}
		}
		if err := suite.WaitForExpectedScan(isJellyfin()); err != nil {
			t.Error(err)
		}
		// whatever the test left outside a library, cleared, and checked
		for _, folder := range []string{"/media/movie", "/media/orphans-kept"} {
			if _, err := suite.Invoke("item_orphans_delete", map[string]any{"folder": folder, "confirm": true}); err != nil && !strings.Contains(err.Error(), "can still see") {
				t.Errorf("clearing %s: %v", folder, err)
			}
		}
		if out, err := suite.Invoke("audit_orphans", nil); err != nil || acc.NumOr0(out["total_findings"]) != 0 {
			t.Errorf("the test left orphans behind: %v %v", out, err)
		}
	})
	suite.Call(t, "library_create", map[string]any{"name": name, "type": "movies", "paths": []any{"/media/movie", "/media/orphans-kept"}, "scan": true})
	if !acc.EventuallyWithin(time.Minute, func() bool { return suite.TypeCounts(name)["Movie"] == 3 }) {
		t.Fatalf("%s never held its three films", name)
	}
	if err := suite.WaitForScan(); err != nil {
		t.Fatal(err)
	}
	// the live library beside it, to hold to what it was
	live := acc.TreeOf(t, filepath.Join(testenv.DataDir(), "movies"))

	if !isJellyfin() {
		// one folder taken out of the library, with no scan asked for, and
		// taken off the disk: Emby has already let go of its films
		suite.Call(t, "library_edit", map[string]any{"library": name, "remove_paths": []any{"/media/movie"}})
		if err := os.RemoveAll(removed); err != nil {
			t.Fatal(err)
		}
		lostSight(t, "/media/movie")
		if got := suite.TypeCounts(name)["Movie"]; got != 1 {
			t.Errorf("%s holds %d films once a folder is out, want the other folder's one", name, got)
		}
		if audit := suite.Call(t, "audit_orphans", nil); acc.Num(t, audit["total_findings"], "total_findings") != 0 {
			t.Errorf("Emby left the removed folder's films behind: %v", audit)
		}
		out := suite.Call(t, "item_orphans_delete", map[string]any{"folder": "/media/movie", "confirm": true})
		if acc.Num(t, out["found"], "found") != 0 || acc.Num(t, out["deleted"], "deleted") != 0 || acc.Num(t, out["items_scanned"], "items_scanned") == 0 {
			t.Errorf("a folder holding nothing = %v", out)
		}
		if got := typeCount(t, "Movies", "Movie"); got != movies {
			t.Errorf("Movies holds %d films, %d before", got, movies)
		}
		acc.SameTree(t, testenv.DataDir(), live, acc.TreeOf(t, filepath.Join(testenv.DataDir(), "movies")))
		return
	}

	// one of the films held where removing it has to reach: a collection, a
	// playlist and alice's favourites, each with a film that stays
	matrix := findItem(t, name, "Movie", "The Matrix")
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	col := acc.Str(suite.Call(t, "collection_create", map[string]any{"name": "Zzyzx Orphans", "item_ids": []any{matrix, arrival}})["id"])
	deleteLater(t, "collection_delete", "collection", col)
	pl := acc.Str(suite.Call(t, "playlist_create", map[string]any{"name": "Zzyzx Orphans", "item_ids": []any{matrix, arrival}, "media_type": "Video"})["id"])
	deleteLater(t, "playlist_delete", "playlist", pl)
	suite.Call(t, "item_set_state", map[string]any{"id": matrix, "user": "alice", "favourite": true})
	t.Cleanup(func() {
		// the orphans' delete below takes it, and its state with it
		suite.UndoIfThere(t, matrix, "item_set_state", map[string]any{"id": matrix, "user": "alice", "favourite": false})
	})
	favourites := acc.Num(t, suite.Call(t, "user_stats", map[string]any{"user": "alice"})["favourites"], "favourites")
	holding := func() (collection, playlist []string, favourite bool) {
		collection = acc.Sorted(names(t, suite.Call(t, "collection_get", map[string]any{"collection": col})["items"], "items"))
		playlist = acc.Sorted(names(t, suite.Call(t, "playlist_get", map[string]any{"playlist": pl})["entries"], "entries"))
		for _, it := range acc.RowsOf(suite.Call(t, "library_items", map[string]any{"user": "alice", "watched": "favourite"})["items"]) {
			favourite = favourite || acc.Str(it["id"]) == matrix
		}
		return collection, playlist, favourite
	}
	if c, p, f := holding(); !slices.Equal(c, []string{"Arrival", "The Matrix"}) || !slices.Equal(p, []string{"Arrival", "The Matrix"}) || !f {
		t.Fatalf("The Matrix is not held: collection %v playlist %v favourite %v", c, p, f)
	}

	// the library removed without the scan that drops its items, and its
	// first folder with it. Not while a scan runs: Jellyfin refreshes a new
	// collection with one, and a scan that finds the library gone drops its
	// items as surely as the one the removal would have asked for
	if err := suite.WaitForScan(); err != nil {
		t.Fatal(err)
	}
	removeLibraryKeepingItems(t, name)
	if err := os.RemoveAll(removed); err != nil {
		t.Fatal(err)
	}
	lostSight(t, "/media/movie")
	stats := suite.Call(t, "server_stats", nil)
	posters := auditCounts(t, nil)[rowPoster]

	audit := suite.Call(t, "audit_orphans", nil)
	byFolder := map[string]map[string]any{}
	for _, row := range acc.Rows(t, audit["folders"], "folders") {
		byFolder[acc.Str(row["folder"])] = row
	}
	// each folder the library read is itself an item, and is left behind too
	gone, still := byFolder["/media/movie"], byFolder["/media/orphans-kept"]
	if gone == nil || still == nil || len(byFolder) != 2 {
		t.Fatalf("the removed library's items were not reported where they lie: %v", audit["folders"])
	}
	if types := acc.Object(t, gone["by_type"], "by_type"); acc.Str(gone["on_server"]) != "missing" || acc.Num(t, types["Movie"], "Movie") != 2 || acc.Num(t, types["Folder"], "Folder") != 1 {
		t.Errorf("the folder taken away = %v", gone)
	}
	// the other folder is still on disk: its film is real, outside every library
	if types := acc.Object(t, still["by_type"], "by_type"); acc.Str(still["on_server"]) != "present" || acc.Num(t, types["Movie"], "Movie") != 1 || acc.Num(t, types["Folder"], "Folder") != 1 {
		t.Errorf("the folder still on disk = %v", still)
	}
	var keptFilm string
	for _, e := range acc.Rows(t, still["examples"], "examples") {
		if acc.Str(e["type"]) == "Movie" {
			keptFilm = acc.Str(e["id"])
		}
	}
	found := acc.Num(t, gone["items"], "items")
	total := acc.Num(t, audit["total_findings"], "total_findings")
	// a limit caps the folders, most items first, and not the count
	capped := suite.Call(t, "audit_orphans", map[string]any{"limit": 1})
	if first := acc.Rows(t, capped["folders"], "folders"); len(first) != 1 || acc.Str(first[0]["folder"]) != "/media/movie" || acc.Num(t, capped["total_findings"], "total_findings") != total {
		t.Errorf("limit 1 = %v, want the removed folder's %d items first and the count of %d", capped, found, total)
	}
	// audit_all's row is the audit's own count, here where it is not 0
	for _, row := range acc.Rows(t, suite.Call(t, "audit_all", nil)["audits"], "audits") {
		if acc.Str(row["audit"]) == "audit_orphans" && (acc.Num(t, row["findings"], "findings") != total || acc.Num(t, row["items_scanned"], "items_scanned") != acc.Num(t, audit["items_scanned"], "items_scanned")) {
			t.Errorf("audit_all's orphans row = %v, the audit %d of %v", row, total, audit["items_scanned"])
		}
	}
	if msg := suite.CallErr(t, "item_orphans_delete", map[string]any{"folder": "/media/orphans-kept", "confirm": true}); !strings.Contains(msg, "can still see") {
		t.Errorf("deleting leftovers the server can still see: %s", msg)
	}

	preview := func() map[string]any {
		return suite.Call(t, "item_orphans_delete", map[string]any{"folder": "/media/movie"})
	}
	first := preview()
	if acc.Num(t, first["found"], "found") != found || acc.Num(t, first["deleted"], "deleted") != 0 || acc.Num(t, first["remaining"], "remaining") != found {
		t.Errorf("preview = %v", first)
	}
	var paths []string
	for _, row := range acc.Rows(t, first["examples"], "examples") {
		paths = append(paths, acc.Str(row["path"]))
	}
	if !slices.Contains(paths, "/media/movie/The Matrix (1999)/The Matrix (1999).mp4") || slices.ContainsFunc(paths, func(p string) bool { return strings.HasPrefix(p, "/media/movies/") }) {
		t.Errorf("examples = %v, want the removed folder's films and nothing of the live library's", paths)
	}

	// a batch at a time, deepest first: the two films go before the folder
	// that held them, and audit_orphans agrees with what is left after each
	left := found
	for _, waiting := range []map[string]int{{"Movie": 1, "Folder": 1}, {"Folder": 1}, {}} {
		out := suite.Call(t, "item_orphans_delete", map[string]any{"folder": "/media/movie", "confirm": true, "limit": 1})
		if acc.Num(t, out["found"], "found") != left || acc.Num(t, out["deleted"], "deleted") != 1 || acc.Num(t, out["remaining"], "remaining") != left-1 || out["failed"] != nil || out["stopped"] != nil {
			t.Fatalf("a batch of one with %d left = %v", left, out)
		}
		left--
		if n := orphanCount(t); n != total-(found-left) {
			t.Errorf("audit_orphans counts %d with %d of the folder's left, want %d", n, left, total-(found-left))
		}
		byType := map[string]int{}
		for kind, n := range acc.Object(t, preview()["by_type"], "by_type") {
			byType[kind] = acc.Num(t, n, kind)
		}
		if fmt.Sprint(byType) != fmt.Sprint(waiting) {
			t.Errorf("with %d left the folder still holds %v, want %v", left, byType, waiting)
		}
	}
	if out := suite.Call(t, "item_orphans_delete", map[string]any{"folder": "/media/movie", "confirm": true}); acc.Num(t, out["found"], "found") != 0 || acc.Num(t, out["deleted"], "deleted") != 0 {
		t.Errorf("after the last batch = %v", out)
	}

	// the live library whose folder the removed one's name begins is as it
	// was, on the server and on disk
	if got := typeCount(t, "Movies", "Movie"); got != movies {
		t.Errorf("Movies holds %d films after the delete, %d before", got, movies)
	}
	acc.SameTree(t, testenv.DataDir(), live, acc.TreeOf(t, filepath.Join(testenv.DataDir(), "movies")))

	// what held The Matrix let it go, and is still there with the film that
	// stays
	if c, p, f := holding(); !slices.Equal(c, []string{"Arrival"}) || !slices.Equal(p, []string{"Arrival"}) || f {
		t.Errorf("after the delete: collection %v playlist %v favourite %v, want Arrival alone and no favourite", c, p, f)
	}
	if n := acc.Num(t, suite.Call(t, "user_stats", map[string]any{"user": "alice"})["favourites"], "favourites"); n != favourites-1 {
		t.Errorf("alice has %d favourites, %d before", n, favourites)
	}
	// and the server's counts drop by the two films, as does the poster
	// audit's, neither film having one
	if n := acc.Num(t, suite.Call(t, "server_stats", nil)["movies"], "movies"); n != acc.Num(t, stats["movies"], "movies")-2 {
		t.Errorf("server_stats counts %d films, %v before the delete", n, stats["movies"])
	}
	if n := auditCounts(t, nil)[rowPoster]; n != posters-2 {
		t.Errorf("audit_all counts %d films without a poster, %d before the delete", n, posters)
	}

	// the film still on disk is left for someone to add to a library again,
	// which takes it back in
	if n := orphanCount(t); n != 2 {
		t.Errorf("after the delete audit_orphans counts %d, want the kept folder and its film", n)
	}
	suite.Call(t, "library_create", map[string]any{"name": again, "type": "movies", "paths": []any{"/media/orphans-kept"}, "scan": true})
	if err := suite.WaitForExpectedScan(isJellyfin()); err != nil {
		t.Fatal(err)
	}
	if !acc.EventuallyWithin(time.Minute, func() bool { return suite.TypeCounts(again)["Movie"] == 1 }) {
		t.Fatalf("%s never held the kept film", again)
	}
	// as the same item: Jellyfin makes an item's id from its path, so the
	// orphan is taken back in rather than held twice
	if got := findItem(t, again, "Movie", "The Matrix Revolutions"); got != keptFilm {
		t.Errorf("the kept film is %s in the library it was added to, %s while outside every library", got, keptFilm)
	}
	if n := orphanCount(t); n != 0 {
		t.Errorf("with the kept folder in a library again audit_orphans counts %d", n)
	}
}

// More orphans than one request deletes: a show of sixty episodes, left
// behind by its library. A call with a limit deletes that many, the episodes
// first, over two requests; the show, its season and the library's folder
// wait for the next call with the last episodes, which clears them. DuckTales
// (1987), whose first season runs to sixty-five.
func TestOrphansManyAtOnce(t *testing.T) {
	if testenv.DataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	if !isJellyfin() {
		// the Emby branch of TestOrphans shows Emby leaves nothing behind
		suite.Call(t, "audit_orphans", nil)
		t.Skip("Emby 4.10 drops a library's items with it, so there are no orphans to make")
	}
	const name, folder, count = "Orphans Show", "/media/orphans-show", 60
	root := filepath.Join(testenv.DataDir(), "orphans-show")
	season := filepath.Join(root, "DuckTales (1987)", "Season 01")
	acc.MediaMkdir(t, testenv.DataDir(), season)
	ep := fixtureVideo(t, "messy-shows", "Severance", "Season 01", "Severance S01E01.mp4")
	for n := 1; n <= count; n++ {
		acc.MediaWrite(t, filepath.Join(season, fmt.Sprintf("DuckTales S01E%02d.mp4", n)), ep)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(root)
		if removeLibraryIfThere(t, name) {
			settleScan(t, func() error { return suite.WaitForExpectedScan(isJellyfin()) })
		}
		if _, err := suite.Invoke("item_orphans_delete", map[string]any{"folder": folder, "confirm": true}); err != nil {
			t.Errorf("clearing %s: %v", folder, err)
		}
		if out, err := suite.Invoke("audit_orphans", nil); err != nil || acc.NumOr0(out["total_findings"]) != 0 {
			t.Errorf("the test left orphans behind: %v %v", out, err)
		}
	})
	before := suite.Call(t, "server_stats", nil)
	suite.Call(t, "library_create", map[string]any{"name": name, "type": "tvshows", "paths": []any{folder}, "scan": true})
	if !acc.EventuallyWithin(2*time.Minute, func() bool { return suite.TypeCounts(name)["Episode"] == count }) {
		t.Fatalf("%s never held its %d episodes", name, count)
	}
	if err := suite.WaitForScan(); err != nil {
		t.Fatal(err)
	}
	removeLibraryKeepingItems(t, name)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	// the server still counts what no library lists
	left := suite.Call(t, "server_stats", nil)
	if acc.Num(t, left["series"], "series") != acc.Num(t, before["series"], "series")+1 || acc.Num(t, left["episodes"], "episodes") != acc.Num(t, before["episodes"], "episodes")+count {
		t.Errorf("with the show left behind server_stats counts %v series and %v episodes, %v and %v before it was staged", left["series"], left["episodes"], before["series"], before["episodes"])
	}

	byType := func(v any) map[string]int {
		out := map[string]int{}
		for kind, n := range acc.Object(t, v, "by_type") {
			out[kind] = acc.Num(t, n, kind)
		}
		return out
	}
	audit := suite.Call(t, "audit_orphans", nil)
	groups := acc.Rows(t, audit["folders"], "folders")
	if len(groups) != 1 || acc.Str(groups[0]["folder"]) != folder || acc.Str(groups[0]["on_server"]) != "missing" {
		t.Fatalf("audit_orphans = %v, want the show's folder alone", groups)
	}
	if got, want := byType(groups[0]["by_type"]), map[string]int{"Episode": count, "Season": 1, "Series": 1, "Folder": 1}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the show's folder holds %v, want %v", got, want)
	}
	all := count + 3
	if acc.Num(t, audit["total_findings"], "total_findings") != all || acc.Num(t, groups[0]["items"], "items") != all {
		t.Errorf("audit_orphans counts %v, want %d", audit["total_findings"], all)
	}

	// fifty-five in one call: more than one request's fifty, all episodes
	out := suite.Call(t, "item_orphans_delete", map[string]any{"folder": folder, "confirm": true, "limit": 55})
	if acc.Num(t, out["found"], "found") != all || acc.Num(t, out["deleted"], "deleted") != 55 || acc.Num(t, out["remaining"], "remaining") != all-55 || out["failed"] != nil || out["stopped"] != nil {
		t.Fatalf("a limit of 55 = %v", out)
	}
	if got, want := byType(suite.Call(t, "item_orphans_delete", map[string]any{"folder": folder})["by_type"]), map[string]int{"Episode": count - 55, "Season": 1, "Series": 1, "Folder": 1}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("after 55 the folder still holds %v, want %v", got, want)
	}
	if n := orphanCount(t); n != all-55 {
		t.Errorf("audit_orphans counts %d after 55, want %d", n, all-55)
	}

	// and the next call clears the rest, the folder last
	out = suite.Call(t, "item_orphans_delete", map[string]any{"folder": folder, "confirm": true})
	if acc.Num(t, out["found"], "found") != all-55 || acc.Num(t, out["deleted"], "deleted") != all-55 || acc.Num(t, out["remaining"], "remaining") != 0 || out["failed"] != nil {
		t.Errorf("the rest = %v", out)
	}
	if n := orphanCount(t); n != 0 {
		t.Errorf("audit_orphans counts %d after the rest", n)
	}
	after := suite.Call(t, "server_stats", nil)
	for _, kind := range []string{"series", "episodes"} {
		if acc.Num(t, after[kind], kind) != acc.Num(t, before[kind], kind) {
			t.Errorf("server_stats counts %v %s, %v before the show was staged", after[kind], kind, before[kind])
		}
	}
}

// DeleteItems, the one request item_orphans_delete sends for each batch,
// against the server itself: Emby 4.10 leaves no orphan for the tool to send
// it for, and on Jellyfin the tool only names ids it has just read. Two films
// of a library of their own, one whose file has already gone with no scan
// since, and between them an id the server never had. Emby deletes both and
// passes over the unknown id; Jellyfin deletes up to the unknown id and
// refuses the rest, which is what item_orphans_delete's one-at-a-time
// fallback is for. And a folder's item on Emby takes its film with it.
func TestDeleteItemsInOneRequest(t *testing.T) {
	if testenv.DataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	const name = "Bulk Delete"
	root := filepath.Join(testenv.DataDir(), "bulk-delete")
	raw := fixtureVideo(t, "movies", "Arrival (2016)", "Arrival (2016).mp4")
	films := []string{"The Matrix (1999)", "The Matrix Reloaded (2003)"}
	for _, f := range films {
		acc.MediaMkdir(t, testenv.DataDir(), filepath.Join(root, f))
		acc.MediaWrite(t, filepath.Join(root, f, f+".mp4"), raw)
	}
	t.Cleanup(func() {
		removeLibrary(t, name)
		if err := suite.WaitForExpectedScan(isJellyfin()); err != nil {
			t.Error(err)
		}
		_ = os.RemoveAll(root)
	})
	suite.Call(t, "library_create", map[string]any{"name": name, "type": "movies", "paths": []any{"/media/bulk-delete"}, "scan": true})
	if !acc.EventuallyWithin(time.Minute, func() bool { return suite.TypeCounts(name)["Movie"] == 2 }) {
		t.Fatalf("%s never held its two films", name)
	}
	if err := suite.WaitForScan(); err != nil {
		t.Fatal(err)
	}
	matrix := findItem(t, name, "Movie", "The Matrix")
	reloaded := findItem(t, name, "Movie", "The Matrix Reloaded")
	gone := func(id string) bool {
		_, err := suite.Invoke("item_get", map[string]any{"id": id})
		return err != nil && strings.Contains(err.Error(), "no item")
	}

	client, err := embyfin.New(backend, os.Getenv("EMBYFIN_SERVER"), os.Getenv("EMBYFIN_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, films[0], films[0]+".mp4")); err != nil {
		t.Fatal(err)
	}

	if !isJellyfin() {
		// Emby's ids are numbers, and one it never gave is passed over; the
		// second film is named by its folder's item, which takes the film
		var folder string
		for _, it := range acc.Rows(t, suite.Call(t, "library_items", map[string]any{"library": name, "types": "Folder", "limit": 50})["items"], "items") {
			if acc.Str(it["path"]) == "/media/bulk-delete/"+films[1] {
				folder = acc.Str(it["id"])
			}
		}
		if folder == "" {
			t.Fatalf("no folder item for %s", films[1])
		}
		if err := client.DeleteItems(ctx, []string{matrix, "99999999", folder}); err != nil {
			t.Fatalf("DeleteItems with an unknown id among them: %v", err)
		}
		if !acc.Eventually(func() bool { return gone(matrix) && gone(reloaded) && gone(folder) }) {
			t.Errorf("after DeleteItems The Matrix is gone %v, Reloaded %v, its folder %v", gone(matrix), gone(reloaded), gone(folder))
		}
		for _, f := range films {
			if _, err := os.Stat(filepath.Join(root, f)); !os.IsNotExist(err) {
				t.Errorf("%s's folder is still on disk: %v", f, err)
			}
		}
		// a request naming nothing the server has is not an error either
		if err := client.DeleteItems(ctx, []string{"99999998", "99999997"}); err != nil {
			t.Errorf("DeleteItems of unknown ids alone: %v", err)
		}
		return
	}

	// Jellyfin's are GUIDs: it deletes The Matrix, stops at the id it cannot
	// find, and leaves Reloaded, file and all
	const unknown = "0123456789abcdef0123456789abcdef"
	if err := client.DeleteItems(ctx, []string{matrix, unknown, reloaded}); err == nil {
		t.Fatal("DeleteItems with an unknown id among them succeeded")
	}
	if !gone(matrix) || gone(reloaded) {
		t.Errorf("after the refused request The Matrix is gone %v, Reloaded %v: want the first deleted and the last left", gone(matrix), gone(reloaded))
	}
	if _, err := os.Stat(filepath.Join(root, films[1], films[1]+".mp4")); err != nil {
		t.Errorf("Reloaded's file went with the refused request: %v", err)
	}
	if err := client.DeleteItems(ctx, []string{reloaded}); err != nil {
		t.Fatalf("DeleteItems of the one left: %v", err)
	}
	if !gone(reloaded) {
		t.Error("Reloaded is still on the server")
	}
	if _, err := os.Stat(filepath.Join(root, films[1])); !os.IsNotExist(err) {
		t.Errorf("Reloaded's folder is still on disk: %v", err)
	}
}
