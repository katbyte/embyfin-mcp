//go:build integration

package acceptance

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// What a removed library leaves behind, and removing it.
//
// Jellyfin keeps a library's items when the library is removed without the
// scan that would drop them: the state a renamed library folder leaves on a
// real server, the items outside every library and their folder gone. Emby
// 4.10 has no route to that state: it drops a library's items with the
// library, and a folder's items with the folder the moment it leaves a
// library, scan or no scan - which the Emby branch shows, and then drives
// the tools through their refusals and a folder holding nothing.
func TestOrphans(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	movies := movieCount(t, "Movies")

	// the fixtures leave nothing outside a library, so anything reported here
	// is something the sweep should not have counted
	if audit := call(t, "audit_orphans", nil); num(t, audit["total_findings"], "total_findings") != 0 || num(t, audit["items_scanned"], "items_scanned") == 0 {
		t.Errorf("before anything was removed: %v", audit)
	}

	// a folder on disk that no library reads: what is under it is real, so
	// the delete refuses it
	present := filepath.Join(dataDir(), "orphans-present")
	mediaMkdir(t, present)
	t.Cleanup(func() { _ = os.RemoveAll(present) })
	for folder, want := range map[string]string{
		"/media/movies":                 "inside the Movies library",
		"/media/movies/Arrival (2016)":  "inside the Movies library",
		"/media":                        "holds the",
		"/media/orphans-present":        "can still see",
		"/media/orphans-live/../movies": "steps through",
		"/":                             "top of a filesystem",
		"movies":                        "full path",
	} {
		if msg := callErr(t, "item_orphans_delete", map[string]any{"folder": folder, "confirm": true}); !strings.Contains(msg, want) {
			t.Errorf("folder %s: %s", folder, msg)
		}
	}
	if msg := callErr(t, "item_orphans_delete", map[string]any{"folder": "/media/orphans-never", "limit": 10001}); !strings.Contains(msg, "more than the 10000") {
		t.Errorf("a limit over the most one call deletes: %s", msg)
	}

	// a library of two folders, a film in each
	raw, err := os.ReadFile(filepath.Join(dataDir(), "movies", "Arrival (2016)", "Arrival (2016).mp4")) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dataDir(), "orphans-live")
	kept := filepath.Join(dataDir(), "orphans-kept")
	films := map[string]string{"Orphan One (2001)": folder, "Orphan Two (2002)": folder, "Orphan Kept (2003)": kept}
	for title, dir := range films {
		mediaMkdir(t, filepath.Join(dir, title))
		mediaWrite(t, filepath.Join(dir, title, title+".mp4"), raw)
	}
	name := "Orphans Live"
	t.Cleanup(func() {
		_ = os.RemoveAll(folder)
		_ = os.RemoveAll(kept)
		_, _ = invoke("library_delete", map[string]any{"library": name, "confirm": true})
		_, _ = invoke("item_orphans_delete", map[string]any{"folder": "/media/orphans-live", "confirm": true})
		_, _ = invoke("item_orphans_delete", map[string]any{"folder": "/media/orphans-kept", "confirm": true})
		_ = waitForScan()
	})
	call(t, "library_create", map[string]any{"name": name, "type": "movies", "paths": []any{"/media/orphans-live", "/media/orphans-kept"}, "scan": true})
	for i := 0; movieCountOr0(name) < 3; i++ {
		if i == 60 {
			t.Fatalf("%s never held its three films", name)
		}
		time.Sleep(time.Second)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}

	if !isJellyfin() {
		// one folder taken out of the library, with no scan asked for, and
		// taken off the disk: Emby has already let go of its films
		call(t, "library_edit", map[string]any{"library": name, "remove_paths": []any{"/media/orphans-live"}})
		if err := os.RemoveAll(folder); err != nil {
			t.Fatal(err)
		}
		if got := movieCountOr0(name); got != 1 {
			t.Errorf("%s holds %d films once a folder is out, want the other folder's one", name, got)
		}
		if audit := call(t, "audit_orphans", nil); num(t, audit["total_findings"], "total_findings") != 0 {
			t.Errorf("Emby left the removed folder's films behind: %v", audit)
		}
		out := call(t, "item_orphans_delete", map[string]any{"folder": "/media/orphans-live", "confirm": true})
		if num(t, out["found"], "found") != 0 || num(t, out["deleted"], "deleted") != 0 || num(t, out["items_scanned"], "items_scanned") == 0 {
			t.Errorf("a folder holding nothing = %v", out)
		}
		return
	}

	// the library removed without the scan that drops its items, and its
	// first folder with it
	if status, body := api(t, http.MethodDelete, "/Library/VirtualFolders?name="+url.QueryEscape(name)+"&refreshLibrary=false", "", nil); status != http.StatusNoContent {
		t.Fatalf("removing the library: %d %s", status, body)
	}
	if err := os.RemoveAll(folder); err != nil {
		t.Fatal(err)
	}

	audit := call(t, "audit_orphans", nil)
	byFolder := map[string]map[string]any{}
	for _, row := range rows(t, audit["folders"], "folders") {
		byFolder[str(row["folder"])] = row
	}
	// each folder the library read is itself an item, and is left behind too
	gone, still := byFolder["/media/orphans-live"], byFolder["/media/orphans-kept"]
	if gone == nil || still == nil || len(byFolder) != 2 {
		t.Fatalf("the removed library's items were not reported where they lie: %v", audit["folders"])
	}
	if types := object(t, gone["by_type"], "by_type"); str(gone["on_server"]) != "missing" || num(t, types["Movie"], "Movie") != 2 || num(t, types["Folder"], "Folder") != 1 {
		t.Errorf("the folder taken away = %v", gone)
	}
	// the other folder is still on disk: its film is real, outside every library
	if types := object(t, still["by_type"], "by_type"); str(still["on_server"]) != "present" || num(t, types["Movie"], "Movie") != 1 || num(t, types["Folder"], "Folder") != 1 {
		t.Errorf("the folder still on disk = %v", still)
	}
	found := num(t, gone["items"], "items")
	total := num(t, audit["total_findings"], "total_findings")
	// a limit caps the folders, not the count
	if capped := call(t, "audit_orphans", map[string]any{"limit": 1}); len(rows(t, capped["folders"], "folders")) != 1 || num(t, capped["total_findings"], "total_findings") != total {
		t.Errorf("limit 1 = %v", capped)
	}
	// audit_all's row is the audit's own count, here where it is not 0
	for _, row := range rows(t, call(t, "audit_all", nil)["audits"], "audits") {
		if str(row["audit"]) == "audit_orphans" && (num(t, row["findings"], "findings") != total || num(t, row["items_scanned"], "items_scanned") != num(t, audit["items_scanned"], "items_scanned")) {
			t.Errorf("audit_all's orphans row = %v, the audit %d of %v", row, total, audit["items_scanned"])
		}
	}
	if msg := callErr(t, "item_orphans_delete", map[string]any{"folder": "/media/orphans-kept", "confirm": true}); !strings.Contains(msg, "can still see") {
		t.Errorf("deleting leftovers the server can still see: %s", msg)
	}

	preview := call(t, "item_orphans_delete", map[string]any{"folder": "/media/orphans-live"})
	if num(t, preview["found"], "found") != found || num(t, preview["deleted"], "deleted") != 0 || num(t, preview["remaining"], "remaining") != found {
		t.Errorf("preview = %v", preview)
	}
	var paths []string
	for _, row := range rows(t, preview["examples"], "examples") {
		paths = append(paths, str(row["path"]))
	}
	if !slices.Contains(paths, "/media/orphans-live/Orphan One (2001)/Orphan One (2001).mp4") {
		t.Errorf("examples = %v", paths)
	}

	// a batch at a time: each call deletes one, the rest wait for the next,
	// and audit_orphans agrees with what is left after each
	left := found
	for left > 0 {
		out := call(t, "item_orphans_delete", map[string]any{"folder": "/media/orphans-live", "confirm": true, "limit": 1})
		if num(t, out["found"], "found") != left || num(t, out["deleted"], "deleted") != 1 || num(t, out["remaining"], "remaining") != left-1 || out["failed"] != nil || out["stopped"] != nil {
			t.Fatalf("a batch of one with %d left = %v", left, out)
		}
		left--
		if n := num(t, call(t, "audit_orphans", nil)["total_findings"], "total_findings"); n != total-(found-left) {
			t.Errorf("audit_orphans counts %d with %d of the folder's left, want %d", n, left, total-(found-left))
		}
	}
	if out := call(t, "item_orphans_delete", map[string]any{"folder": "/media/orphans-live", "confirm": true}); num(t, out["found"], "found") != 0 || num(t, out["deleted"], "deleted") != 0 {
		t.Errorf("after the last batch = %v", out)
	}
	if got := movieCount(t, "Movies"); got != movies {
		t.Errorf("Movies holds %d films after the delete, %d before", got, movies)
	}
	// the film still on disk is left for someone to add to a library again
	if audit := call(t, "audit_orphans", nil); num(t, audit["total_findings"], "total_findings") != 2 {
		t.Errorf("after the delete: %v, want the kept folder and its film", audit)
	}
	// until its folder goes too, and the rest are cleared in one call
	if err := os.RemoveAll(kept); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if !eventually(func() bool {
		var err error
		out, err = invoke("item_orphans_delete", map[string]any{"folder": "/media/orphans-kept", "confirm": true})
		return err == nil
	}) {
		t.Fatalf("the kept folder's leftovers were never deletable once it was gone")
	}
	if num(t, out["found"], "found") != 2 || num(t, out["deleted"], "deleted") != 2 || num(t, out["remaining"], "remaining") != 0 {
		t.Errorf("the rest in one call = %v", out)
	}
	if audit := call(t, "audit_orphans", nil); num(t, audit["total_findings"], "total_findings") != 0 {
		t.Errorf("after both deletes: %v", audit)
	}
}

// movieCountOr0 is movieCount for a library that may not be listed yet.
func movieCountOr0(library string) int {
	out, err := invoke("library_get", map[string]any{"library": library})
	if err != nil {
		return 0
	}
	counts, _ := out["type_counts"].(map[string]any)
	n, _ := counts["Movie"].(float64)

	return int(n)
}
