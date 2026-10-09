//go:build integration

package acceptance

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	acc "github.com/katbyte/embyfin-mcp/lib/acceptance"
	"github.com/katbyte/embyfin-mcp/lib/testenv"
)

// previewOptions turns a library's preview thumbnails on or off the way the
// server's own settings page does - extraction and an interval, both, with
// the files saved beside the media - which no tool here sets.
func previewOptions(t *testing.T, library string, on bool) {
	t.Helper()

	status, raw := api(t, http.MethodGet, "/Library/VirtualFolders", "", nil)
	var folders []map[string]any
	if status != http.StatusOK || json.Unmarshal(raw, &folders) != nil {
		t.Fatalf("reading the libraries: HTTP %d: %.200s", status, raw)
	}
	for _, folder := range folders {
		if acc.Str(folder["Name"]) != library {
			continue
		}
		options, _ := folder["LibraryOptions"].(map[string]any)
		if options == nil {
			t.Fatalf("the library %s has no options: %v", library, folder)
		}
		options["EnableChapterImageExtraction"], options["ThumbnailImagesIntervalSeconds"], options["SaveLocalThumbnailSets"] = on, -1, on
		// a library scan makes none: the test says when they are made
		options["ExtractChapterImagesDuringLibraryScan"] = false
		if on {
			options["ThumbnailImagesIntervalSeconds"] = 10
		}
		if status, raw := api(t, http.MethodPost, "/Library/VirtualFolders/LibraryOptions", "", map[string]any{"Id": folder["ItemId"], "LibraryOptions": options}); status/100 != 2 {
			t.Fatalf("setting the options of %s: HTTP %d: %.200s", library, status, raw)
		}

		return
	}
	t.Fatalf("no library %s among %d", library, len(folders))
}

// Preview thumbnails from none to all, and every way one goes wrong on the
// way: the frames a player shows over the seek bar, which Emby keeps in one
// BIF file a video.
//
// Three films in a library of their own. While the library makes none, none
// is missing. Turned on, all three are, and Emby's own task makes them. A
// file deleted behind its back the task then leaves missing when it runs
// again (seen on 4.10.1), which is what the tool is for; asked for by id, the film gets
// its file beside it and nothing else in its folder is touched. A file cut
// off in its index, cut off in its frames, overwritten with something else,
// emptied, or swapped for another film's is each found and named. The library
// is then worked through from the first film to the last until the audit
// finds nothing. Last, what a refresh does besides: an overview the item had
// empty comes back from its nfo, and the answer says so; and a video Emby
// cannot read is said to have had none made.
//
// Jellyfin keeps trickplay tiles, which neither tool reads, and both say so.
func TestPreviewThumbnailsFoundAndMadeAgain(t *testing.T) {
	if isJellyfin() {
		for tool, args := range map[string]map[string]any{
			"audit_previews":           nil,
			"item_previews_regenerate": {"library": "Movies"},
		} {
			if msg := suite.CallErr(t, tool, args); !strings.Contains(msg, "read on Emby alone so far: Jellyfin keeps trickplay tiles") {
				t.Errorf("%s on Jellyfin said: %s", tool, msg)
			}
		}

		return
	}

	const library = "Zzyzx Previews"
	// in the order of their paths, which is the order they are read in
	films := []string{"Brüno (2009)", "Dune Part Two (2024)", "Limitless (2011)"}
	dir := filepath.Join(testenv.DataDir(), "previews")
	for _, film := range films {
		copyFixture(t, filepath.Join(testenv.DataDir(), "movies", film), filepath.Join(dir, film))
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() {
		removeLibraryIfThere(t, library)
		if err := suite.WaitForExpectedScan(isJellyfin()); err != nil {
			t.Error(err)
		}
	})
	// its fetchers off: what a refresh fills, it fills from the nfo beside the film
	suite.Call(t, "library_create", map[string]any{"name": library, "type": "movies", "paths": []any{"/media/previews"}, "scan": true, "save_nfo": false, "providers": false})
	if !acc.EventuallyWithin(acc.ScanPatience, func() bool { return typeCount(t, library, "Movie") == len(films) }) {
		t.Fatal("the library never held its films")
	}
	if err := suite.WaitForExpectedScan(isJellyfin()); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for path, it := range itemsUnder(t, library, "/media/previews") {
		ids[filepath.Base(filepath.Dir(path))] = acc.Str(it["id"])
	}
	if len(ids) != len(films) {
		t.Fatalf("the films in the library are %v", ids)
	}
	file := func(film string) string { return filepath.Join(dir, film, film+"-320-10.bif") }
	audit := func(args map[string]any) (out map[string]any, found []map[string]any) {
		t.Helper()
		out = suite.Call(t, "audit_previews", args)

		return out, acc.Rows(t, out["findings"], "findings")
	}
	// problems is what the audit of the whole library finds, a film and its
	// problem a row
	problems := func() []string {
		t.Helper()
		out, found := audit(map[string]any{"library": library})
		if acc.Num(t, out["items_scanned"], "items_scanned") != len(films) || acc.Num(t, out["total_findings"], "total_findings") != len(found) || acc.Num(t, out["length_not_judged"], "length_not_judged") != 0 {
			t.Errorf("the audit of the library = %v", out)
		}
		var rows []string
		for _, f := range found {
			rows = append(rows, filepath.Base(filepath.Dir(acc.Str(f["path"])))+": "+acc.Str(f["problem"]))
		}

		return rows
	}
	regenerate := func(args map[string]any) (out map[string]any, rows []map[string]any) {
		t.Helper()
		out = suite.Call(t, "item_previews_regenerate", args)

		return out, acc.Rows(t, out["videos"], "videos")
	}

	// a library that makes none: nothing is missing, and nothing is asked for
	out, found := audit(map[string]any{"library": library})
	off := acc.Rows(t, out["libraries_off"], "libraries_off")
	if acc.Num(t, out["items_scanned"], "items_scanned") != 0 || len(found) != 0 || len(off) != 1 || acc.Str(off[0]["library"]) != library || acc.Num(t, off[0]["videos"], "videos") != len(films) ||
		!strings.HasPrefix(acc.Str(off[0]["why"]), "the library's options make no preview thumbnails") {
		t.Fatalf("with previews off the audit = %v", out)
	}
	out, rows := regenerate(map[string]any{"library": library})
	if o := acc.Object(t, out["library_off"], "library_off"); len(rows) != 0 || acc.Num(t, out["made"], "made") != 0 || acc.Str(o["library"]) != library {
		t.Fatalf("with previews off item_previews_regenerate = %v", out)
	}

	// turned on, every film is missing its own
	previewOptions(t, library, true)
	if got, want := problems(), []string{films[0] + ": missing", films[1] + ": missing", films[2] + ": missing"}; !slices.Equal(got, want) {
		t.Fatalf("with previews on the audit finds %v, want %v", got, want)
	}

	// Emby's own task makes a film's thumbnails
	const task = "Video preview thumbnail extraction"
	runTask := func() {
		t.Helper()
		was, _ := taskRun(t, task)
		suite.Call(t, "task_run", map[string]any{"task": task})
		if !acc.EventuallyWithin(acc.ScanPatience, func() bool {
			at, row := taskRun(t, task)

			return at.After(was) && acc.Str(row["state"]) == "Idle"
		}) {
			t.Fatalf("the %s task never ran", task)
		}
	}
	runTask()
	if got := problems(); len(got) != 0 {
		t.Fatalf("after Emby's own task the audit still finds %v", got)
	}
	// and passes over a film it made them for before: a file deleted behind
	// its back stays missing (seen on 4.10.1)
	bruno, dune, limitless := films[0], films[1], films[2]
	if err := os.Remove(file(bruno)); err != nil {
		t.Fatal(err)
	}
	if got, want := problems(), []string{bruno + ": missing"}; !slices.Equal(got, want) {
		t.Fatalf("with a film's file deleted the audit finds %v, want %v", got, want)
	}
	runTask()
	if got, want := problems(), []string{bruno + ": missing"}; !slices.Equal(got, want) {
		t.Fatalf("after Emby's own task ran again the audit finds %v, want %v: on 4.10.1 the task passes over a film it made thumbnails for before", got, want)
	}

	// the film by id: its file is made beside it, and nothing else there
	// is touched
	before := acc.TreeOf(t, filepath.Join(dir, bruno))
	out, rows = regenerate(map[string]any{"ids": []any{ids[bruno]}})
	if len(rows) != 1 || acc.Num(t, out["made"], "made") != 1 || acc.Num(t, out["videos_checked"], "videos_checked") != 1 || out["stopped"] != nil || out["note"] != nil {
		t.Fatalf("item_previews_regenerate for one film = %v", out)
	}
	if r := rows[0]; acc.Str(r["id"]) != ids[bruno] || acc.Str(r["result"]) != "made" || acc.Str(r["was"]) != "missing: the server holds no preview thumbnails for it" ||
		acc.Num(t, r["thumbnails"], "thumbnails") < 400 || acc.Str(r["library"]) != library || r["also_changed"] != nil {
		t.Errorf("the film's row = %v, want it made, with eighty minutes of thumbnails and nothing else changed", r)
	}
	if made, err := os.Stat(file(bruno)); err != nil || made.Size() < 10_000 {
		t.Fatalf("the film's thumbnails are not beside it as %s: %v", filepath.Base(file(bruno)), err)
	}
	acc.SameTree(t, filepath.Join(dir, bruno), before, acc.TreeOf(t, filepath.Join(dir, bruno), file(bruno)))
	if got := problems(); len(got) != 0 {
		t.Errorf("made, the audit still finds %v", got)
	}
	// and asked for again, a sound file is left alone
	if out, rows = regenerate(map[string]any{"ids": []any{ids[bruno]}}); len(rows) != 1 || acc.Str(rows[0]["result"]) != "skipped" || acc.Str(rows[0]["was"]) != "sound" || acc.Num(t, out["skipped"], "skipped") != 1 {
		t.Errorf("item_previews_regenerate for a film whose thumbnails are sound = %v", out)
	}

	// every way a file goes wrong, on the last film's
	sound, err := os.ReadFile(file(limitless))
	if err != nil {
		t.Fatal(err)
	}
	other, err := os.ReadFile(file(dune))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		what, problem, detail string
		bytes                 []byte
	}{
		{"cut off in its index", "damaged", "the file is cut off: its header counts ", sound[:1000]},
		{"cut off in its frames", "damaged", "the file is cut off or added to: its index ends the last thumbnail at byte ", sound[:len(sound)-900]},
		{"overwritten with something else", "damaged", "the file is not a set of thumbnails: it does not begin as one", slices.Repeat([]byte("not a bif "), 2000)},
		{"emptied", "damaged", "the file is empty", nil},
		// a film an hour longer
		{"another film's", "length", "thumbnails 10 s apart reach 2 h 4", other},
	} {
		acc.MediaWrite(t, file(limitless), c.bytes)
		_, found := audit(map[string]any{"ids": []any{ids[limitless]}})
		if len(found) != 1 || acc.Str(found[0]["problem"]) != c.problem || !strings.Contains(acc.Str(found[0]["detail"]), c.detail) || acc.Str(found[0]["id"]) != ids[limitless] {
			t.Errorf("a file %s is found as %v, want %s: %s", c.what, found, c.problem, c.detail)
		}
	}
	// and the first two films' deleted, for two to make
	for _, film := range []string{bruno, dune} {
		if err := os.Remove(file(film)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := problems(), []string{bruno + ": missing", dune + ": missing", limitless + ": length"}; !slices.Equal(got, want) {
		t.Fatalf("the audit finds %v, want %v", got, want)
	}

	// the library worked through, a film a call: the first is made, and the
	// answer says where to go on
	out, rows = regenerate(map[string]any{"library": library, "limit": 1})
	if len(rows) != 1 || acc.Str(rows[0]["id"]) != ids[bruno] || acc.Str(rows[0]["result"]) != "made" || acc.Num(t, out["next_offset"], "next_offset") != 1 ||
		acc.Str(out["stopped"]) != "the limit of 1 videos was reached" || acc.Num(t, out["videos_checked"], "videos_checked") != 1 {
		t.Fatalf("the first film of the library = %v", out)
	}
	// from there the second, and the last is passed over: its thumbnails are
	// sound, and whether they or the runtime are wrong is not told from here
	out, rows = regenerate(map[string]any{"library": library, "limit": 1, "offset": 1})
	if len(rows) != 2 || acc.Str(rows[0]["id"]) != ids[dune] || acc.Str(rows[0]["result"]) != "made" || out["next_offset"] != nil || out["stopped"] != nil ||
		acc.Num(t, out["made"], "made") != 1 || acc.Num(t, out["skipped"], "skipped") != 1 {
		t.Fatalf("the second film of the library = %v", out)
	}
	if r := rows[1]; acc.Str(r["id"]) != ids[limitless] || acc.Str(r["result"]) != "skipped" || !strings.HasPrefix(acc.Str(r["was"]), "length: ") || !strings.Contains(acc.Str(r["detail"]), "pass length true") {
		t.Errorf("the film with another film's thumbnails = %v, want it passed over and length named", r)
	}
	if got, want := problems(), []string{limitless + ": length"}; !slices.Equal(got, want) {
		t.Fatalf("the audit finds %v, want %v", got, want)
	}
	// asked for, they are made again from the film there now
	out, rows = regenerate(map[string]any{"library": library, "length": true})
	if len(rows) != 1 || acc.Str(rows[0]["id"]) != ids[limitless] || acc.Str(rows[0]["result"]) != "made" || rows[0]["detail"] != nil || !strings.HasPrefix(acc.Str(rows[0]["was"]), "length: ") ||
		out["next_offset"] != nil || acc.Num(t, out["videos_checked"], "videos_checked") != len(films) {
		t.Fatalf("with length, the last film of the library = %v", out)
	}
	if got := problems(); len(got) != 0 {
		t.Fatalf("worked through, the audit still finds %v", got)
	}
	// the file made over another film's is this film's own
	if now, err := os.ReadFile(file(limitless)); err != nil || len(now) != len(sound) {
		t.Errorf("the last film's thumbnails are %d bytes, %v: want the %d they were before another film's were put in their place", len(now), err, len(sound))
	}
	if out, rows = regenerate(map[string]any{"library": library}); len(rows) != 0 || acc.Num(t, out["made"], "made") != 0 || acc.Num(t, out["videos_checked"], "videos_checked") != len(films) || out["next_offset"] != nil {
		t.Errorf("with nothing left to make, item_previews_regenerate = %v", out)
	}

	// what the refresh does besides: an overview the item has empty comes
	// back from the nfo beside the film, and the answer names it
	updateItem(t, ids[limitless], map[string]any{"Overview": ""})
	if !acc.Eventually(func() bool { return acc.Str(fullItem(t, ids[limitless])["Overview"]) == "" }) {
		t.Fatal("the film's overview was not emptied")
	}
	if err := os.Remove(file(limitless)); err != nil {
		t.Fatal(err)
	}
	out, rows = regenerate(map[string]any{"ids": []any{ids[limitless]}})
	if len(rows) != 1 || acc.Str(rows[0]["result"]) != "made" || !slices.Equal(acc.Strs(t, rows[0]["also_changed"], "also_changed"), []string{"Overview"}) {
		t.Errorf("for a film with an empty overview item_previews_regenerate = %v, want it made and the overview named as changed", out)
	}
	if got := acc.Str(fullItem(t, ids[limitless])["Overview"]); got == "" {
		t.Error("the overview named as changed reads empty")
	}

	// a video Emby cannot read: the refresh runs, and none is made
	const cut = "Arrival (2016)"
	whole := fixtureVideo(t, "movies", cut, cut+".mp4")
	acc.MediaMkdir(t, testenv.DataDir(), filepath.Join(dir, cut))
	acc.MediaWrite(t, filepath.Join(dir, cut, cut+".mp4"), whole[:4096])
	var cutID string
	scanUntilTrue(t, library, func() bool {
		for path, it := range itemsUnder(t, library, "/media/previews/"+cut) {
			if strings.HasSuffix(path, cut+".mp4") {
				cutID = acc.Str(it["id"])
			}
		}

		return cutID != ""
	})
	out, rows = regenerate(map[string]any{"ids": []any{cutID}})
	if len(rows) != 1 || acc.Str(rows[0]["result"]) != "not_made" || acc.Num(t, out["not_made"], "not_made") != 1 || acc.Num(t, out["made"], "made") != 0 ||
		acc.Str(rows[0]["detail"]) != "Emby ran the refresh and the thumbnails are still missing: the server holds no preview thumbnails for it" {
		t.Errorf("for a video Emby cannot read item_previews_regenerate = %v", out)
	}
	if _, err := os.Stat(file(cut)); !os.IsNotExist(err) {
		t.Errorf("a file was made for the video Emby cannot read: %v", err)
	}
}
