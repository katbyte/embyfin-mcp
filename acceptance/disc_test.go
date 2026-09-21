//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A disc flattened into a film's folder, which is what the audit is for: the
// server makes an item of each stream rather than one film.
//
// The streams are staged here and taken away again rather than left in the
// fixtures, because films in a library are counted by tests that have nothing
// to do with discs.
func TestAuditDiscFolders(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}

	// nothing in the fixtures is a disc
	if before := call(t, "audit_disc_folders", nil); num(t, before["total_folders"], "total_folders") != 0 {
		t.Errorf("before staging one: %v", before)
	}

	have := movieCount(t, "Messy Movies")
	dir := filepath.Join(dataDir(), "messy-movies", "Disc Rip (1999)")
	mediaMkdir(t, dir)
	for _, name := range []string{"00000.m2ts", "00001.m2ts"} {
		raw, err := os.ReadFile(filepath.Join(dataDir(), "disc-src", name)) //nolint:gosec // a fixture under the test data dir
		if err != nil {
			t.Fatal(err)
		}
		mediaWrite(t, filepath.Join(dir, name), raw)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		if _, err := invoke("library_scan", nil); err == nil {
			_ = waitForItems("Messy Movies", have)
		}
	})

	call(t, "library_scan", nil)
	// both servers read the two streams as two films, which is the defect
	if err := waitForItems("Messy Movies", have+2); err != nil {
		t.Fatal(err)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}

	out := call(t, "audit_disc_folders", map[string]any{"library": "Messy Movies"})
	var group map[string]any
	for _, row := range rows(t, out["folders"], "folders") {
		if strings.HasSuffix(str(row["folder"]), "Disc Rip (1999)") {
			group = row
		}
	}
	if group == nil {
		t.Fatalf("the staged disc was not reported: %v", out)
	}
	if str(group["kind"]) != "flattened blu-ray" {
		t.Errorf("kind = %v", group["kind"])
	}
	entries := rows(t, group["entries"], "entries")
	if len(entries) != num(t, group["items"], "items") || len(entries) == 0 {
		t.Fatalf("entries = %v", entries)
	}
	for _, e := range entries {
		if !strings.HasSuffix(str(e["file"]), ".m2ts") || str(e["id"]) == "" {
			t.Errorf("entry = %v", e)
		}
	}
	// the sweep reads the library, not just this folder
	if num(t, out["items_scanned"], "items_scanned") < have {
		t.Errorf("items_scanned = %v, want at least the library's films", out["items_scanned"])
	}
}
