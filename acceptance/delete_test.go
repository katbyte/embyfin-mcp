//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// item_delete removes the file from disk, so it gets a film of its own: a
// copy added to the messy library and scanned in first.
func TestItemDelete(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	have := movieCount(t, "Messy Movies")

	src := filepath.Join(dataDir(), "messy-movies", messyMononoke, messyMononoke+".mp4")
	dir := filepath.Join(dataDir(), "messy-movies", "Doomed (2001)")
	file := filepath.Join(dir, "Doomed (2001).mp4")
	mediaMkdir(t, dir)
	raw, err := os.ReadFile(src) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}
	mediaWrite(t, file, raw)
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		if _, err := invoke("library_scan", nil); err == nil {
			_ = waitForItems("Messy Movies", have)
		}
	})

	call(t, "library_scan", nil)
	if err := waitForItems("Messy Movies", have+1); err != nil {
		t.Fatal(err)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
	id := findItem(t, "Messy Movies", "Movie", "Doomed")

	if msg := callErr(t, "item_delete", map[string]any{"id": id}); !strings.Contains(msg, "confirm") {
		t.Errorf("delete without confirm: %s", msg)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("the refused delete removed the file")
	}

	out := call(t, "item_delete", map[string]any{"id": id, "confirm": true})
	if d := str(out["deleted"]); !strings.HasPrefix(d, "Doomed") || !strings.Contains(d, "Doomed (2001).mp4") {
		t.Errorf("item_delete = %v", out)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("the file is still on disk after item_delete: %v", err)
	}
	if err := waitForItems("Messy Movies", have); err != nil {
		t.Error(err)
	}
	if msg := callErr(t, "item_delete", map[string]any{"id": id, "confirm": true}); msg == "" {
		t.Error("deleting the same item twice succeeded")
	}
}
