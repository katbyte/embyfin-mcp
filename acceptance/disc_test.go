//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/go-kt/test/env"

	"github.com/katbyte/embyfin-mcp/lib/testenv"
)

// A disc flattened into a film's folder, which is what the audit is for: the
// server makes an item of each stream rather than one film.
//
// The fixtures carry a DVD's VOB left loose in a film's folder, and a Blu-ray
// kept whole as its BDMV tree. Both servers hold the kept one as one film at
// its folder - neither reaches past BDMV into the streams, so the "inside a
// disc structure" kind never arises from a disc laid out whole - and the
// audit leaves it alone. A Blu-ray's streams, flattened into Pi's folder, are
// staged here: found, matched one stream at a time to two films, and then
// remuxed into the one file they should have been, which the audit lets go of.
func TestAuditDiscFolders(t *testing.T) {
	if testenv.DataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}

	before := suite.Call(t, "audit_disc_folders", nil)
	folders := acc.Rows(t, before["folders"], "folders")
	if acc.Num(t, before["total_findings"], "total_findings") != 1 || len(folders) != 1 {
		t.Fatalf("before staging one: %v, want the loose DVD alone", before)
	}
	dvd := folders[0]
	entries := acc.Rows(t, dvd["entries"], "entries")
	if acc.Str(dvd["folder"]) != "/media/messy-movies/"+messyLooseDVD || acc.Str(dvd["kind"]) != "flattened dvd" || acc.Num(t, dvd["items"], "items") != 1 || len(entries) != 1 {
		t.Errorf("the loose DVD = %v", dvd)
	} else if e := entries[0]; acc.Str(e["file"]) != "VTS_01_1.VOB" || title(acc.Str(e["name"])) != "Coyote vs. Acme" || acc.Num(t, e["size"], "size") <= 0 || acc.Str(e["matched_to"]) != "" {
		t.Errorf("the loose DVD's entry = %v", e)
	}
	for _, f := range folders {
		if strings.Contains(acc.Str(f["folder"]), messyKeptBluRay) {
			t.Errorf("the Blu-ray kept whole was reported: %v", f)
		}
	}

	have := typeCount(t, "Messy Movies", "Movie")
	const name = "Pi (1998)"
	dir := filepath.Join(testenv.DataDir(), "messy-movies", name)
	server := "/media/messy-movies/" + name
	streams := []string{"00000.m2ts", "00001.m2ts"}
	env.Mkdir(t, testenv.DataDir(), dir)
	for _, s := range streams {
		env.WriteFile(t, filepath.Join(dir, s), fixtureVideo(t, "disc-src", s))
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		if err := scanUntil("Messy Movies", have); err != nil {
			t.Error(err)
		}
	})
	// both servers read the two streams as two films, which is the defect
	if err := scanUntil("Messy Movies", have+2); err != nil {
		t.Fatal(err)
	}

	pi := func() map[string]any {
		t.Helper()
		out := suite.Call(t, "audit_disc_folders", map[string]any{"library": "Messy Movies"})
		folders := acc.Rows(t, out["folders"], "folders")
		// most items first: the two streams, then the one VOB
		if len(folders) != 2 || acc.Num(t, out["total_findings"], "total_findings") != 2 || acc.Str(folders[0]["folder"]) != server {
			t.Fatalf("folders = %v, want the staged disc then the loose DVD", folders)
		}
		// the sweep reads the library, not just this folder
		if acc.Num(t, out["items_scanned"], "items_scanned") != have+2 {
			t.Errorf("items_scanned = %v, want the library's %d films", out["items_scanned"], have+2)
		}
		return folders[0]
	}
	group := pi()
	if acc.Str(group["kind"]) != "flattened blu-ray" || group["note"] != nil {
		t.Errorf("the staged disc = %v", group)
	}
	entries = acc.Rows(t, group["entries"], "entries")
	if len(entries) != 2 || acc.Num(t, group["items"], "items") != 2 {
		t.Fatalf("entries = %v", entries)
	}
	ids := make([]string, len(entries))
	for i, e := range entries {
		if acc.Str(e["file"]) != streams[i] || acc.Str(e["id"]) == "" || acc.Str(e["matched_to"]) != "" {
			t.Errorf("entry = %v", e)
		}
		ids[i] = acc.Str(e["id"])
	}
	// a limit caps the folders, most items first, and not the count
	capped := suite.Call(t, "audit_disc_folders", map[string]any{"library": "Messy Movies", "limit": 1})
	if first := acc.Rows(t, capped["folders"], "folders"); len(first) != 1 || acc.Str(first[0]["folder"]) != server || acc.Num(t, capped["total_findings"], "total_findings") != 2 {
		t.Errorf("limit 1 = %v, want the staged disc alone and a count of 2", capped)
	}
	// a stream is one film of two in the folder: deleting it would take that
	// stream and nothing else
	if folder, names := wouldRemove(t, suite.CallErr(t, "item_delete", map[string]any{"id": ids[0]})); folder != "" || !slices.Equal(names, []string{server + "/" + streams[0]}) {
		t.Errorf("the refusal for one stream would take %q %v, want that stream alone", folder, names)
	}

	// each stream matched on its own, as a scan with an nfo beside each would:
	// one to Pi and one to Arrival. One disc is one film, so the audit says at
	// least one of them is wrong
	nfos := map[string][]byte{"00000.nfo": movieNfo("Pi", 1998, "473", "tt0138704"), "00001.nfo": movieNfo("Arrival", 2016, "329865", "tt2543164")}
	for file, raw := range nfos {
		env.WriteFile(t, filepath.Join(dir, file), raw)
	}
	for _, id := range ids {
		suite.Call(t, "item_refresh", map[string]any{"id": id})
	}
	matched := func() []string {
		var out []string
		for _, e := range acc.Rows(t, pi()["entries"], "entries") {
			out = append(out, acc.Str(e["matched_to"]))
		}
		return out
	}
	if !acc.Eventually(func() bool {
		return slices.Equal(matched(), []string{"imdb:tt0138704 tmdb:473", "imdb:tt2543164 tmdb:329865"})
	}) {
		t.Errorf("the streams are matched to %v, want Pi's ids and Arrival's", matched())
	}
	if note := acc.Str(pi()["note"]); note != "matched to 2 different titles, so at least 1 of these are the wrong film" {
		t.Errorf("note = %q", note)
	}

	// remuxed into one file in the same folder: one film again, and nothing
	// for the audit
	for _, f := range []string{streams[0], streams[1], "00000.nfo", "00001.nfo"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	env.WriteFile(t, filepath.Join(dir, name+".mp4"), fixtureVideo(t, "messy-movies", messyArrival, messyArrival+".mp4"))
	if err := scanUntil("Messy Movies", have+1); err != nil {
		t.Fatal(err)
	}
	after := suite.Call(t, "audit_disc_folders", map[string]any{"library": "Messy Movies"})
	var left []string
	for _, f := range acc.Rows(t, after["folders"], "folders") {
		left = append(left, acc.Str(f["folder"]))
	}
	if want := []string{"/media/messy-movies/" + messyLooseDVD}; !slices.Equal(left, want) || acc.Num(t, after["total_findings"], "total_findings") != 1 {
		t.Errorf("after the remux audit_disc_folders = %v, want %v alone", left, want)
	}
	var remux []string
	for _, it := range acc.Rows(t, suite.Call(t, "library_items", map[string]any{"library": "Messy Movies", "limit": 50})["items"], "items") {
		if strings.HasPrefix(acc.Str(it["path"]), server+"/") {
			remux = append(remux, acc.Str(it["path"]))
		}
	}
	if !slices.Equal(remux, []string{server + "/" + name + ".mp4"}) {
		t.Errorf("the folder holds %v, want the remux alone", remux)
	}
}

// The loose VOB's shape as its stream states it. Both servers state a DVD
// stream's ratio as a decimal against 1 - this one, a 720x480 frame of
// square pixels, as 1.5:1 - which read as no ratio at all, so the copy's
// aspect came from the frame, and an anamorphic DVD stated that way got no
// display width. It is read as stated now; this one's shape is its frame's,
// so it is shown at its stored width and needs no display width.
func TestTheLooseVOBsStatedShape(t *testing.T) {
	vob := findItem(t, "Messy Movies", "Movie", "Coyote vs. Acme")
	got := suite.Call(t, "item_get", map[string]any{"id": vob})
	if ratio := acc.Str(got["aspect_ratio"]); !strings.Contains(ratio, ":") || got["display_width"] != nil || acc.Num(t, got["width"], "width") != 720 || acc.Num(t, got["height"], "height") != 480 {
		t.Errorf("item_get of the loose VOB = aspect %v, display width %v, %vx%v: want its stated ratio, and no display width for a frame of its own shape", got["aspect_ratio"], got["display_width"], got["width"], got["height"])
	}
	out := suite.Call(t, "quality_compare", map[string]any{"a": map[string]any{"item_id": vob}, "b": map[string]any{"item_id": vob}})
	a := acc.Object(t, out["a"], "a")
	if acc.Decimal(t, a["aspect"], "aspect") != 1.5 || acc.Str(a["aspect_from"]) != "stated" {
		t.Errorf("quality_compare reads the loose VOB's shape as %v from %v, want 1.5 as the stream states it", a["aspect"], a["aspect_from"])
	}
}

// Moon is a DVD kept whole: VIDEO_TS with its IFO, BUP and VOB files, and its
// nfo as VIDEO_TS/VIDEO_TS.nfo, the one place both servers read one beside a
// disc from. Both hold it as one film at its folder, matched by the nfo, and
// the disc audit leaves it alone as it does the Blu-ray kept whole. Jellyfin
// reads the disc's video, and judges it as the DVD it is; Emby never probes
// a disc, so it has no picture to judge and says so. (What deleting it would
// take is TestWhatADeleteWouldTake's.)
func TestADVDKeptWhole(t *testing.T) {
	moon := findItem(t, "Messy Movies", "Movie", "Moon")
	got := suite.Call(t, "item_get", map[string]any{"id": moon})
	ids := acc.Object(t, got["metadata_provider_ids"], "metadata_provider_ids")
	if acc.Str(got["path"]) != "/media/messy-movies/"+messyKeptDVD || acc.Num(t, got["year"], "year") != 2009 || acc.Str(ids["tmdb"]) != "17431" || acc.Str(ids["imdb"]) != "tt1182345" {
		t.Errorf("item_get Moon = %v at %v, %v: want one film at its folder with the nfo's ids", got["name"], got["path"], ids)
	}
	if found := acc.Rows(t, suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "17431", "type": "movie"})["items"], "items"); len(found) != 1 || acc.Str(found[0]["id"]) != moon {
		t.Errorf("tmdb 17431 finds %v, want Moon alone", found)
	}

	for _, f := range acc.Rows(t, suite.Call(t, "audit_disc_folders", map[string]any{"library": "Messy Movies"})["folders"], "folders") {
		if strings.Contains(acc.Str(f["folder"]), messyKeptDVD) {
			t.Errorf("the DVD kept whole was reported: %v", f)
		}
	}

	quality := suite.Call(t, "audit_quality", map[string]any{"library": "Messy Movies"})
	var finding string
	for _, f := range acc.Rows(t, quality["findings"], "findings") {
		if acc.Str(f["id"]) == moon {
			finding = acc.Str(f["detail"])
		}
	}
	unprobed := false
	for _, u := range acc.Rows(t, quality["unprobed"], "unprobed") {
		unprobed = unprobed || acc.Str(u["id"]) == moon
	}
	if isJellyfin() {
		if finding != "mpeg2video 720x480: 480p, below 720p; legacy codec mpeg2video" || unprobed || acc.Str(got["video_codec"]) != "mpeg2video" {
			t.Errorf("Jellyfin reads Moon as %v, and audit_quality says %q (unprobed %v): want the DVD's MPEG-2 judged", got["video_codec"], finding, unprobed)
		}
	} else if finding != "" || !unprobed || got["video_codec"] != nil {
		t.Errorf("Emby reads Moon as %v, and audit_quality says %q (unprobed %v): want it listed as never probed", got["video_codec"], finding, unprobed)
	}
}
