//go:build integration

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A disc flattened into a film's folder, which is what the audit is for: the
// server makes an item of each stream rather than one film.
//
// The fixtures carry a DVD's VOB left loose in a film's folder, and a Blu-ray
// kept whole as its BDMV tree. Both servers hold the kept one as one film at
// its folder - neither reaches past BDMV into the streams, so the "inside a
// disc structure" kind never arises from a disc laid out whole - and the
// audit leaves it alone. The Blu-ray's streams, flattened, are staged here and
// taken away again, being two more films for every count.
func TestAuditDiscFolders(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}

	before := call(t, "audit_disc_folders", nil)
	folders := rows(t, before["folders"], "folders")
	if num(t, before["total_findings"], "total_findings") != 1 || len(folders) != 1 {
		t.Fatalf("before staging one: %v, want the loose DVD alone", before)
	}
	dvd := folders[0]
	entries := rows(t, dvd["entries"], "entries")
	if str(dvd["folder"]) != "/media/messy-movies/"+messyLooseDVD || str(dvd["kind"]) != "flattened dvd" || num(t, dvd["items"], "items") != 1 || len(entries) != 1 {
		t.Errorf("the loose DVD = %v", dvd)
	} else if e := entries[0]; str(e["file"]) != "VTS_01_1.VOB" || title(str(e["name"])) != "Coyote vs. Acme" || num(t, e["size"], "size") <= 0 || str(e["matched_to"]) != "" {
		t.Errorf("the loose DVD's entry = %v", e)
	}
	for _, f := range folders {
		if strings.Contains(str(f["folder"]), messyKeptBluRay) {
			t.Errorf("the Blu-ray kept whole was reported: %v", f)
		}
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
	folders = rows(t, out["folders"], "folders")
	// most items first: the two streams, then the one VOB
	if len(folders) != 2 || num(t, out["total_findings"], "total_findings") != 2 || !strings.HasSuffix(str(folders[0]["folder"]), "Disc Rip (1999)") {
		t.Fatalf("folders = %v, want the staged disc then the loose DVD", folders)
	}
	group := folders[0]
	if str(group["kind"]) != "flattened blu-ray" {
		t.Errorf("kind = %v", group["kind"])
	}
	entries = rows(t, group["entries"], "entries")
	if len(entries) != 2 || num(t, group["items"], "items") != 2 {
		t.Fatalf("entries = %v", entries)
	}
	for i, e := range entries {
		if str(e["file"]) != fmt.Sprintf("0000%d.m2ts", i) || str(e["id"]) == "" {
			t.Errorf("entry = %v", e)
		}
	}
	// a limit caps the folders, not the count
	if capped := call(t, "audit_disc_folders", map[string]any{"library": "Messy Movies", "limit": 1}); len(rows(t, capped["folders"], "folders")) != 1 || num(t, capped["total_findings"], "total_findings") != 2 {
		t.Errorf("limit 1 = %v", capped)
	}
	// the sweep reads the library, not just this folder
	if num(t, out["items_scanned"], "items_scanned") != have+2 {
		t.Errorf("items_scanned = %v, want the library's %d films", out["items_scanned"], have+2)
	}
}
