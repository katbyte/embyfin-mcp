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
// removes a library's items with it and has no route to that state, so there
// the tools are driven through their refusals and a folder holding nothing.
func TestOrphans(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	movies := movieCount(t, "Movies")

	// the fixtures leave nothing outside a library, so anything reported here
	// is something the sweep should not have counted
	if audit := call(t, "audit_orphans", nil); num(t, audit["total_orphans"], "total_orphans") != 0 || num(t, audit["items_scanned"], "items_scanned") == 0 {
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
	} {
		if msg := callErr(t, "item_orphans_delete", map[string]any{"folder": folder, "confirm": true}); !strings.Contains(msg, want) {
			t.Errorf("folder %s: %s", folder, msg)
		}
	}

	if !isJellyfin() {
		out := call(t, "item_orphans_delete", map[string]any{"folder": "/media/orphans-never", "confirm": true})
		if num(t, out["found"], "found") != 0 || num(t, out["deleted"], "deleted") != 0 || num(t, out["items_scanned"], "items_scanned") == 0 {
			t.Errorf("a folder holding nothing = %v", out)
		}

		return
	}

	// a library of two films, removed without the scan that drops its items
	raw, err := os.ReadFile(filepath.Join(dataDir(), "movies", "Arrival (2016)", "Arrival (2016).mp4")) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dataDir(), "orphans-live")
	for _, title := range []string{"Orphan One (2001)", "Orphan Two (2002)"} {
		mediaMkdir(t, filepath.Join(folder, title))
		mediaWrite(t, filepath.Join(folder, title, title+".mp4"), raw)
	}
	name := "Orphans Live"
	t.Cleanup(func() {
		_ = os.RemoveAll(folder)
		_, _ = invoke("library_delete", map[string]any{"library": name, "confirm": true})
		_, _ = invoke("item_orphans_delete", map[string]any{"folder": "/media/orphans-live", "confirm": true})
		_ = waitForScan()
	})
	call(t, "library_create", map[string]any{"name": name, "type": "movies", "paths": []any{"/media/orphans-live"}, "scan": true})
	for i := 0; movieCountOr0(name) < 2; i++ {
		if i == 60 {
			t.Fatalf("%s never held its two films", name)
		}
		time.Sleep(time.Second)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
	if status, body := api(t, http.MethodDelete, "/Library/VirtualFolders?name="+url.QueryEscape(name)+"&refreshLibrary=false", "", nil); status != http.StatusNoContent {
		t.Fatalf("removing the library: %d %s", status, body)
	}
	if err := os.RemoveAll(folder); err != nil {
		t.Fatal(err)
	}

	audit := call(t, "audit_orphans", nil)
	var group map[string]any
	for _, row := range rows(t, audit["folders"], "folders") {
		if str(row["folder"]) == "/media/orphans-live" {
			group = row
		}
	}
	if group == nil {
		t.Fatalf("the removed library's items were not reported: %v", audit)
	}
	if str(group["on_server"]) != "missing" || num(t, object(t, group["by_type"], "by_type")["Movie"], "Movie") != 2 {
		t.Errorf("group = %v", group)
	}
	found := num(t, group["items"], "items")

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

	out := call(t, "item_orphans_delete", map[string]any{"folder": "/media/orphans-live", "confirm": true})
	if num(t, out["deleted"], "deleted") != found || num(t, out["remaining"], "remaining") != 0 || out["failed"] != nil || out["stopped"] != nil {
		t.Errorf("delete = %v", out)
	}
	if audit := call(t, "audit_orphans", nil); num(t, audit["total_orphans"], "total_orphans") != 0 {
		t.Errorf("after the delete: %v", audit)
	}
	if got := movieCount(t, "Movies"); got != movies {
		t.Errorf("Movies holds %d films after the delete, %d before", got, movies)
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
