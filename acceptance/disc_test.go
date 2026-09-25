//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"slices"
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
// audit leaves it alone. A Blu-ray's streams, flattened into Pi's folder, are
// staged here: found, matched one stream at a time to two films, and then
// remuxed into the one file they should have been, which the audit lets go of.
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
	const name = "Pi (1998)"
	dir := filepath.Join(dataDir(), "messy-movies", name)
	server := "/media/messy-movies/" + name
	streams := []string{"00000.m2ts", "00001.m2ts"}
	mediaMkdir(t, dir)
	for _, s := range streams {
		mediaWrite(t, filepath.Join(dir, s), fixtureVideo(t, "disc-src", s))
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
		out := call(t, "audit_disc_folders", map[string]any{"library": "Messy Movies"})
		folders := rows(t, out["folders"], "folders")
		// most items first: the two streams, then the one VOB
		if len(folders) != 2 || num(t, out["total_findings"], "total_findings") != 2 || str(folders[0]["folder"]) != server {
			t.Fatalf("folders = %v, want the staged disc then the loose DVD", folders)
		}
		// the sweep reads the library, not just this folder
		if num(t, out["items_scanned"], "items_scanned") != have+2 {
			t.Errorf("items_scanned = %v, want the library's %d films", out["items_scanned"], have+2)
		}
		return folders[0]
	}
	group := pi()
	if str(group["kind"]) != "flattened blu-ray" || group["note"] != nil {
		t.Errorf("the staged disc = %v", group)
	}
	entries = rows(t, group["entries"], "entries")
	if len(entries) != 2 || num(t, group["items"], "items") != 2 {
		t.Fatalf("entries = %v", entries)
	}
	ids := make([]string, len(entries))
	for i, e := range entries {
		if str(e["file"]) != streams[i] || str(e["id"]) == "" || str(e["matched_to"]) != "" {
			t.Errorf("entry = %v", e)
		}
		ids[i] = str(e["id"])
	}
	// a limit caps the folders, most items first, and not the count
	capped := call(t, "audit_disc_folders", map[string]any{"library": "Messy Movies", "limit": 1})
	if first := rows(t, capped["folders"], "folders"); len(first) != 1 || str(first[0]["folder"]) != server || num(t, capped["total_findings"], "total_findings") != 2 {
		t.Errorf("limit 1 = %v, want the staged disc alone and a count of 2", capped)
	}
	// a stream is one film of two in the folder: deleting it would take that
	// stream and nothing else
	if folder, names := wouldRemove(t, callErr(t, "item_delete", map[string]any{"id": ids[0]})); folder != "" || !slices.Equal(names, []string{server + "/" + streams[0]}) {
		t.Errorf("the refusal for one stream would take %q %v, want that stream alone", folder, names)
	}

	// each stream matched on its own, as a scan with an nfo beside each would:
	// one to Pi and one to Arrival. One disc is one film, so the audit says at
	// least one of them is wrong
	nfos := map[string][]byte{"00000.nfo": movieNfo("Pi", 1998, "473", "tt0138704"), "00001.nfo": movieNfo("Arrival", 2016, "329865", "tt2543164")}
	for file, raw := range nfos {
		mediaWrite(t, filepath.Join(dir, file), raw)
	}
	for _, id := range ids {
		call(t, "item_refresh", map[string]any{"id": id})
	}
	matched := func() []string {
		var out []string
		for _, e := range rows(t, pi()["entries"], "entries") {
			out = append(out, str(e["matched_to"]))
		}
		return out
	}
	if !eventually(func() bool {
		return slices.Equal(matched(), []string{"imdb:tt0138704 tmdb:473", "imdb:tt2543164 tmdb:329865"})
	}) {
		t.Errorf("the streams are matched to %v, want Pi's ids and Arrival's", matched())
	}
	if note := str(pi()["note"]); note != "matched to 2 different titles, so at least 1 of these are the wrong film" {
		t.Errorf("note = %q", note)
	}

	// remuxed into one file in the same folder: one film again, and nothing
	// for the audit
	for _, f := range []string{streams[0], streams[1], "00000.nfo", "00001.nfo"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	mediaWrite(t, filepath.Join(dir, name+".mp4"), fixtureVideo(t, "messy-movies", messyArrival, messyArrival+".mp4"))
	if err := scanUntil("Messy Movies", have+1); err != nil {
		t.Fatal(err)
	}
	after := call(t, "audit_disc_folders", map[string]any{"library": "Messy Movies"})
	var left []string
	for _, f := range rows(t, after["folders"], "folders") {
		left = append(left, str(f["folder"]))
	}
	if want := []string{"/media/messy-movies/" + messyLooseDVD}; !slices.Equal(left, want) || num(t, after["total_findings"], "total_findings") != 1 {
		t.Errorf("after the remux audit_disc_folders = %v, want %v alone", left, want)
	}
	var remux []string
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "limit": 50})["items"], "items") {
		if strings.HasPrefix(str(it["path"]), server+"/") {
			remux = append(remux, str(it["path"]))
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
	got := call(t, "item_get", map[string]any{"id": vob})
	if ratio := str(got["aspect_ratio"]); !strings.Contains(ratio, ":") || got["display_width"] != nil || num(t, got["width"], "width") != 720 || num(t, got["height"], "height") != 480 {
		t.Errorf("item_get of the loose VOB = aspect %v, display width %v, %vx%v: want its stated ratio, and no display width for a frame of its own shape", got["aspect_ratio"], got["display_width"], got["width"], got["height"])
	}
	out := call(t, "quality_compare", map[string]any{"a": map[string]any{"item_id": vob}, "b": map[string]any{"item_id": vob}})
	a := object(t, out["a"], "a")
	if decimal(t, a["aspect"], "aspect") != 1.5 || str(a["aspect_from"]) != "stated" {
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
	got := call(t, "item_get", map[string]any{"id": moon})
	ids := object(t, got["metadata_provider_ids"], "metadata_provider_ids")
	if str(got["path"]) != "/media/messy-movies/"+messyKeptDVD || num(t, got["year"], "year") != 2009 || str(ids["tmdb"]) != "17431" || str(ids["imdb"]) != "tt1182345" {
		t.Errorf("item_get Moon = %v at %v, %v: want one film at its folder with the nfo's ids", got["name"], got["path"], ids)
	}
	if found := rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "17431"})["items"], "items"); len(found) != 1 || str(found[0]["id"]) != moon {
		t.Errorf("tmdb 17431 finds %v, want Moon alone", found)
	}

	for _, f := range rows(t, call(t, "audit_disc_folders", map[string]any{"library": "Messy Movies"})["folders"], "folders") {
		if strings.Contains(str(f["folder"]), messyKeptDVD) {
			t.Errorf("the DVD kept whole was reported: %v", f)
		}
	}

	quality := call(t, "audit_quality", map[string]any{"library": "Messy Movies"})
	var finding string
	for _, f := range rows(t, quality["findings"], "findings") {
		if str(f["id"]) == moon {
			finding = str(f["detail"])
		}
	}
	unprobed := false
	for _, u := range rows(t, quality["unprobed"], "unprobed") {
		unprobed = unprobed || str(u["id"]) == moon
	}
	if isJellyfin() {
		if finding != "mpeg2video 720x480: 480p, below 720p; legacy codec mpeg2video" || unprobed || str(got["video_codec"]) != "mpeg2video" {
			t.Errorf("Jellyfin reads Moon as %v, and audit_quality says %q (unprobed %v): want the DVD's MPEG-2 judged", got["video_codec"], finding, unprobed)
		}
	} else if finding != "" || !unprobed || got["video_codec"] != nil {
		t.Errorf("Emby reads Moon as %v, and audit_quality says %q (unprobed %v): want it listed as never probed", got["video_codec"], finding, unprobed)
	}
}
