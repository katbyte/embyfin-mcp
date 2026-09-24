//go:build integration

package acceptance

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Journeys through the files on disk: an episode imported into a gap, a copy
// upgraded in place, how far a delete reaches, and a show held twice put back
// together. Each lays out what it needs, checks every tool along the chain
// agrees with the one before, and takes it all away again.

// hostPath is where a path the server reads (/media/...) sits on this machine.
func hostPath(serverPath string) string {
	return filepath.Join(dataDir(), strings.TrimPrefix(serverPath, "/media/"))
}

// scanUntilTrue asks for a scan of one library until check holds, asking
// again whenever the scan goes idle short of it: the same patience as
// scanUntil, for a change a count of series or films cannot see.
func scanUntilTrue(t *testing.T, library string, check func() bool) {
	t.Helper()

	deadline := time.Now().Add(scanPatience)
	for {
		if err := waitForScan(); err != nil {
			t.Fatal(err)
		}
		if _, err := invoke("library_scan", map[string]any{"library": library}); err != nil {
			t.Fatal(err)
		}
		for range 22 {
			if check() {
				if err := waitForScan(); err != nil {
					t.Fatal(err)
				}
				return
			}
			time.Sleep(2 * time.Second)
		}
		if time.Now().After(deadline) {
			t.Fatalf("a scan of %s never brought about what the test waits for", library)
		}
	}
}

// episodeNfo is the sidecar a downloader writes beside an episode.
func episodeNfo(title string, season, episode int) []byte {
	return fmt.Appendf(nil, `<?xml version="1.0" encoding="utf-8"?>
<episodedetails>
  <title>%s</title>
  <season>%d</season>
  <episode>%d</episode>
</episodedetails>
`, title, season, episode)
}

// held answers whether a series holds one episode, and the id it holds it
// under.
func held(t *testing.T, seriesID string, season, episode int) (exists bool, id string) {
	t.Helper()

	out := call(t, "show_episodes_exist", map[string]any{"series_id": seriesID, "episodes": []map[string]any{{"season": season, "episode": episode}}})
	row := rows(t, out["episodes"], "episodes")[0]

	return boolOf(row["exists"]), str(row["id"])
}

// A missing episode imported the way a downloader does it: the release name
// resolved to the series, the gap confirmed three ways, the destination read
// before writing, the file and its nfo written, the library scanned. Every
// tool that called the episode missing then says it is there, and the path
// that was free names the item now at it.
//
// Star Trek The Next Generation holds episodes 1 and 3 of its first season
// and no ids, so the gap between its files is the fact every tool agrees on
// without asking a provider.
func TestImportingAMissingEpisode(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	const show = "Star Trek The Next Generation"
	tng := findItem(t, "Messy Shows", "Series", show)

	res := call(t, "show_resolve", map[string]any{"title": "Star.Trek.The.Next.Generation.S01E02.The.Naked.Now.DVDRip.x264-GROUP", "library": "Messy Shows"})
	cands := rows(t, res["candidates"], "candidates")
	if len(cands) == 0 || str(cands[0]["series_id"]) != tng || decimal(t, cands[0]["score"], "score") < 0.9 {
		t.Fatalf("the release name resolved to %v, want %s", cands, show)
	}
	if num(t, res["parsed_season"], "parsed_season") != 1 || num(t, res["parsed_episode"], "parsed_episode") != 2 {
		t.Errorf("parsed S%vE%v, want S01E02", res["parsed_season"], res["parsed_episode"])
	}

	// absent, three ways
	if there, _ := held(t, tng, 1, 2); there {
		t.Fatal("show_episodes_exist says the series already holds S01E02")
	}
	gapFor := func() string {
		for _, f := range rows(t, call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows"})["findings"], "findings") {
			if str(f["id"]) == tng {
				return str(f["detail"])
			}
		}
		return ""
	}
	if d := gapFor(); !strings.Contains(d, "S01E02") {
		t.Fatalf("audit_missing_episodes says %q of %s, want S01E02 among the gaps", d, show)
	}
	gaps := func() []string {
		var out []string
		for _, g := range rowsOf(call(t, "show_missing", map[string]any{"series_id": tng})["gaps_on_disk"]) {
			out = append(out, fmt.Sprintf("S%02dE%02d", numOr0(g["season"]), numOr0(g["episode"])))
		}
		return out
	}
	if g := gaps(); !slices.Equal(g, []string{"S01E02"}) {
		t.Fatalf("show_missing gaps_on_disk = %v, want [S01E02]", g)
	}

	// the destination, beside the episodes the series holds
	var first string
	for _, e := range rows(t, call(t, "show_episodes", map[string]any{"series_id": tng})["episodes"], "episodes") {
		if num(t, e["episode"], "episode") == 1 {
			first = str(e["path"])
		}
	}
	if first == "" {
		t.Fatal("the series holds no first episode to write beside")
	}
	dest := filepath.Join(filepath.Dir(first), show+" S01E02.mp4")
	plan := func() map[string]any {
		out := call(t, "plan_check", map[string]any{"entries": []map[string]any{{"path": dest, "series": show, "season": 1, "episode": 2}}})
		return rows(t, out["entries"], "entries")[0]
	}
	before := plan()
	if before["exists"] != false || before["current"] != nil {
		t.Fatalf("plan_check calls the free path taken: %v", before)
	}
	if join := object(t, before["would_join"], "would_join"); str(join["series_id"]) != tng || decimal(t, join["claim_similarity"], "claim_similarity") < 0.9 {
		t.Errorf("plan_check would_join = %v, want %s", join, show)
	}

	// write it, the way a downloader does: the file, then its nfo
	file := hostPath(dest)
	nfo := strings.TrimSuffix(file, ".mp4") + ".nfo"
	t.Cleanup(func() {
		_ = os.Remove(file)
		_ = os.Remove(nfo)
		scanUntilTrue(t, "Messy Shows", func() bool { there, _ := held(t, tng, 1, 2); return !there })
	})
	mediaWrite(t, file, fixtureVideo(t, "messy-shows", show, "Season 01", show+" S01E01.mp4"))
	mediaWrite(t, nfo, episodeNfo("The Naked Now", 1, 2))
	scanUntilTrue(t, "Messy Shows", func() bool { there, _ := held(t, tng, 1, 2); return there })

	there, id := held(t, tng, 1, 2)
	if !there || id == "" {
		t.Fatalf("after the scan show_episodes_exist says S01E02 is held %v, id %q", there, id)
	}
	if d := gapFor(); d != "" {
		t.Errorf("audit_missing_episodes still lists %s: %q", show, d)
	}
	if g := gaps(); len(g) != 0 {
		t.Errorf("show_missing still has gaps on disk: %v", g)
	}
	after := plan()
	if after["exists"] != true {
		t.Fatalf("plan_check calls the written path free: %v", after)
	}
	if current := object(t, after["current"], "current"); str(current["item_id"]) != id || num(t, current["episode"], "episode") != 2 {
		t.Errorf("plan_check current = %v, want the new episode %s", current, id)
	}
}

// A copy upgraded in place: quality_compare says the incoming file beats the
// one in the library, plan_check shows the path taken and by how much the
// file grows, the file is written over, and the library scanned. The one
// episode saved since then is the upgraded one, audit_quality has let go of
// it, and show_episodes reads the new picture.
//
// The episode is staged, 360p, beside the messy Severance's own, so the
// upgrade leaves nothing behind: an episode written over in place stays
// "replaced" on Emby for good, its file newer than the server's first sight
// of it.
func TestUpgradingACopyInPlace(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	messy := findItem(t, "Messy Shows", "Series", "Severance")
	clean := findItem(t, "Shows", "Series", "Severance")
	season := filepath.Join(dataDir(), "messy-shows", "Severance", "Season 01")
	file := filepath.Join(season, "Severance S01E04.mp4")
	nfo := filepath.Join(season, "Severance S01E04.nfo")
	t.Cleanup(func() {
		_ = os.Remove(file)
		_ = os.Remove(nfo)
		scanUntilTrue(t, "Messy Shows", func() bool { there, _ := held(t, messy, 1, 4); return !there })
	})
	mediaWrite(t, file, fixtureVideo(t, "messy-shows", "Severance", "Season 01", "Severance S01E01.mp4"))
	mediaWrite(t, nfo, episodeNfo("The You You Are", 1, 4))
	scanUntilTrue(t, "Messy Shows", func() bool { there, _ := held(t, messy, 1, 4); return there })
	_, rip := held(t, messy, 1, 4)

	// the incoming file is the clean library's 720p encode, byte for byte
	var better map[string]any
	for _, e := range rows(t, call(t, "show_episodes", map[string]any{"series_id": clean, "season": 1})["episodes"], "episodes") {
		if num(t, e["episode"], "episode") == 1 {
			better = e
		}
	}
	if better == nil {
		t.Fatal("the clean Severance holds no S01E01")
	}
	incoming := fixtureVideo(t, "shows", "Severance", "Season 01", "Severance S01E01.mp4")
	verdict := call(t, "quality_compare", map[string]any{"a": map[string]any{"item_id": rip}, "b": map[string]any{"item_id": str(better["id"])}})
	if str(verdict["verdict"]) != "b_better" || str(verdict["decided_by"]) != "resolution" {
		t.Fatalf("quality_compare = %v by %v, want the 720p copy better by resolution", verdict["verdict"], verdict["decided_by"])
	}

	// the path is taken, by a smaller file
	onDisk, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for _, e := range rows(t, call(t, "show_episodes", map[string]any{"series_id": messy, "season": 1})["episodes"], "episodes") {
		if str(e["id"]) == rip {
			path = str(e["path"])
		}
	}
	plan := call(t, "plan_check", map[string]any{"library": "Messy Shows", "entries": []map[string]any{{"path": path, "size": len(incoming), "series": "Severance", "season": 1, "episode": 4}}})
	row := rows(t, plan["entries"], "entries")[0]
	if row["exists"] != true || num(t, plan["existing"], "existing") != 1 {
		t.Fatalf("plan_check calls the staged episode's path free: %v", row)
	}
	current := object(t, row["current"], "current")
	want := math.Round(float64(len(incoming))/float64(onDisk.Size())*100) / 100
	if str(current["item_id"]) != rip || decimal(t, current["size_ratio"], "size_ratio") != want || want <= 1 {
		t.Errorf("plan_check current = %v, want item %s at a size ratio of %v", current, rip, want)
	}

	// written over in place: same path, same item, a newer file
	start := time.Now().Add(-2 * time.Second).UTC().Format(time.RFC3339)
	mediaWrite(t, file, incoming)
	if !isJellyfin() {
		// Emby judges a file by what it read at the last scan, and has not
		// read this one yet
		for _, r := range rowsOf(call(t, "audit_quality", map[string]any{"library": "Messy Shows"})["replaced"]) {
			if str(r["id"]) == rip {
				t.Errorf("audit_quality calls the file replaced before any scan read it: %v", r)
			}
		}
	}
	savedSince := func() []string {
		var ids []string
		for _, e := range rowsOf(call(t, "library_episodes", map[string]any{"library": "Messy Shows", "saved_since": start})["episodes"]) {
			ids = append(ids, str(e["id"]))
		}
		return ids
	}
	// the scan saves the item before it has probed the new file, so what
	// settles it is the picture the server reads
	height := func() int {
		for _, e := range rows(t, call(t, "show_episodes", map[string]any{"series_id": messy, "season": 1})["episodes"], "episodes") {
			if str(e["id"]) == rip {
				return numOr0(e["height"])
			}
		}
		return 0
	}
	scanUntilTrue(t, "Messy Shows", func() bool { return height() == 720 })

	if ids := savedSince(); !slices.Equal(ids, []string{rip}) {
		t.Errorf("library_episodes saved since the write = %v, want the upgraded episode alone (%s)", ids, rip)
	}
	quality := call(t, "audit_quality", map[string]any{"library": "Messy Shows"})
	for _, f := range rows(t, quality["findings"], "findings") {
		if str(f["id"]) == rip {
			t.Errorf("audit_quality still finds the upgraded episode: %v", f)
		}
	}
	var replaced map[string]any
	for _, r := range rows(t, quality["replaced"], "replaced") {
		if str(r["id"]) == rip {
			replaced = r
		}
	}
	if isJellyfin() {
		// Jellyfin does not say when a file was written, so it cannot tell
		if replaced != nil || !strings.Contains(str(quality["note"]), "Jellyfin does not say") {
			t.Errorf("audit_quality on Jellyfin: replaced %v, note %q", replaced, quality["note"])
		}
	} else if replaced == nil || num(t, replaced["size"], "size") != len(incoming) {
		// Emby read the new file's size and time, and the time says replaced
		t.Errorf("audit_quality replaced = %v, want the upgraded episode at %d bytes", quality["replaced"], len(incoming))
	}
	for _, e := range rows(t, call(t, "show_episodes", map[string]any{"series_id": messy, "season": 1})["episodes"], "episodes") {
		if str(e["id"]) == rip && (num(t, e["height"], "height") != 720 || num(t, e["size"], "size") != len(incoming)) {
			t.Errorf("show_episodes reads the upgraded episode as %vx%v, %v bytes", e["width"], e["height"], e["size"])
		}
	}
}

// removedPaths reads item_delete's removed list as paths, a folder ending in
// "/".
func removedPaths(t *testing.T, out map[string]any) []string {
	t.Helper()

	var got []string
	for _, r := range rows(t, out["removed"], "removed") {
		p := str(r["path"])
		if boolOf(r["folder"]) {
			p += "/"
		}
		got = append(got, p)
	}
	slices.Sort(got)

	return got
}

// How far item_delete reaches, which is the same on both servers (probed on
// Emby 4.10 and Jellyfin 12.1): a film alone in its folder takes the folder
// with it - every version, the nfo, the artwork - and a series takes its
// folder. Without confirm the tool refuses and says what would go; with it,
// the answer lists what went, and that is what left the disk.
func TestHowFarADeleteReaches(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}

	t.Run("a film in two versions", func(t *testing.T) {
		have := movieCount(t, "Messy Movies")
		const name = "Zzyzx Two Cuts (2003)"
		folder := filepath.Join(dataDir(), "messy-movies", name)
		t.Cleanup(func() {
			_ = os.RemoveAll(folder)
			if err := scanUntil("Messy Movies", have); err != nil {
				t.Error(err)
			}
		})
		mediaMkdir(t, folder)
		cuts := map[string]string{"1080p": "Blade Runner (1982) - 1080p.mp4", "2160p": "Blade Runner (1982) - 2160p.mp4"}
		for label, src := range cuts {
			mediaWrite(t, filepath.Join(folder, name+" - "+label+".mp4"), fixtureVideo(t, "messy-movies", messyBladeRunner, src))
		}
		mediaWrite(t, filepath.Join(folder, "poster.jpg"), fixtureVideo(t, "messy-movies", messyBladeRunner, "poster.jpg"))
		added := 2
		if versionsMerged() {
			added = 1
		}
		if err := scanUntil("Messy Movies", have+added); err != nil {
			t.Fatal(err)
		}
		// Emby's search answers with every Zzyzx film, so the folder decides
		var ids, paths []string
		for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "query": "Zzyzx Two Cuts"})["items"], "items") {
			if strings.Contains(str(it["path"]), "/"+name+"/") {
				ids = append(ids, str(it["id"]))
				paths = append(paths, str(it["path"]))
			}
		}
		if len(ids) != added {
			t.Fatalf("library_items holds %v for the two cuts, want %d entries", paths, added)
		}

		// both files are one film to both servers - Jellyfin's one entry, and
		// on Emby two entries it shows as one film's versions - so deleting
		// either takes the folder, both files and the poster with it
		server := "/media/messy-movies/" + name
		msg := callErr(t, "item_delete", map[string]any{"id": ids[0]})
		for _, want := range []string{"without confirm=true", "nothing was deleted", "would remove the folder " + server + " with everything in it", name + " - 1080p.mp4", name + " - 2160p.mp4", "poster.jpg"} {
			if !strings.Contains(msg, want) {
				t.Errorf("the refusal does not say %q: %s", want, msg)
			}
		}
		if _, err := os.Stat(filepath.Join(folder, name+" - 2160p.mp4")); err != nil {
			t.Fatalf("the refused delete removed a file: %v", err)
		}

		out := call(t, "item_delete", map[string]any{"id": ids[0], "confirm": true})
		if !strings.Contains(str(out["deleted"]), paths[0]) {
			t.Errorf("item_delete = %v, want the path it deleted, %s", out, paths[0])
		}
		got := removedPaths(t, out)
		for _, want := range []string{server + "/", server + "/" + name + " - 1080p.mp4", server + "/" + name + " - 2160p.mp4", server + "/poster.jpg"} {
			if !slices.Contains(got, want) {
				t.Errorf("removed = %v, want %s among them", got, want)
			}
		}
		for _, p := range got {
			if !strings.HasPrefix(p, server+"/") {
				t.Errorf("removed names %s, outside the film's folder", p)
			}
		}
		if _, err := os.Stat(folder); !os.IsNotExist(err) {
			left, _ := os.ReadDir(folder)
			t.Errorf("after the delete the folder is still there, holding %v", left)
		}
		// and once a scan has run, nothing of the film is left
		if err := scanUntil("Messy Movies", have); err != nil {
			t.Fatal(err)
		}
		for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "query": "Zzyzx Two Cuts"})["items"], "items") {
			if strings.Contains(str(it["path"]), "/"+name+"/") {
				t.Errorf("after a scan the library still holds %v", it["path"])
			}
		}
	})

	t.Run("a series", func(t *testing.T) {
		have := seriesCount(t, "Messy Shows")
		folder := filepath.Join(dataDir(), "messy-shows", "Zzyzx Doomed (2001)")
		t.Cleanup(func() {
			_ = os.RemoveAll(folder)
			if err := scanUntil("Messy Shows", have); err != nil {
				t.Error(err)
			}
		})
		ep := fixtureVideo(t, "messy-shows", "Severance", "Season 01", "Severance S01E01.mp4")
		mediaMkdir(t, filepath.Join(folder, "Season 01"))
		for _, n := range []int{1, 2} {
			mediaWrite(t, filepath.Join(folder, "Season 01", fmt.Sprintf("Zzyzx Doomed S01E%02d.mp4", n)), ep)
		}
		if err := scanUntil("Messy Shows", have+1); err != nil {
			t.Fatal(err)
		}
		id := findItem(t, "Messy Shows", "Series", "Zzyzx Doomed")
		episodes := rows(t, call(t, "show_episodes", map[string]any{"series_id": id})["episodes"], "episodes")
		if len(episodes) != 2 {
			t.Fatalf("the staged series holds %v, want two episodes", episodes)
		}

		server := "/media/messy-shows/Zzyzx Doomed (2001)"
		if msg := callErr(t, "item_delete", map[string]any{"id": id}); !strings.Contains(msg, "would remove the folder "+server+" with everything in it") || !strings.Contains(msg, "Season 01/Zzyzx Doomed S01E02.mp4") {
			t.Errorf("the refusal for a series: %s", msg)
		}

		// the answer names the series and its folder; every episode file in
		// the folder goes with it
		out := call(t, "item_delete", map[string]any{"id": id, "confirm": true})
		if d := str(out["deleted"]); !strings.HasPrefix(d, "Zzyzx Doomed") || !strings.Contains(d, server) {
			t.Errorf("item_delete of a series = %v", out)
		}
		got := removedPaths(t, out)
		for _, want := range []string{server + "/", server + "/Season 01/", server + "/Season 01/Zzyzx Doomed S01E01.mp4", server + "/Season 01/Zzyzx Doomed S01E02.mp4"} {
			if !slices.Contains(got, want) {
				t.Errorf("removed = %v, want %s among them", got, want)
			}
		}
		if _, err := os.Stat(folder); !os.IsNotExist(err) {
			entries, _ := os.ReadDir(filepath.Join(folder, "Season 01"))
			t.Errorf("the series folder is still on disk, holding %v", entries)
		}
		for _, e := range episodes {
			if _, err := invoke("item_get", map[string]any{"id": str(e["id"])}); err == nil {
				t.Errorf("episode %v outlived its series", e["title"])
			}
		}
	})
}

// One show held twice, put back together: audit_duplicate_series finds the
// Zzyzx Twins in two folders a space and a letter's case apart, plan_check
// places the second folder's episode under the first show, the file is moved
// there and the empty folder removed, and after a scan the server holds one
// show with both episodes and the audit has nothing to report. Neither show
// carries an id, so the folder names are the only thing that says they are
// one: the lookups that go by provider id cannot see the pair at all.
func TestAShowHeldTwicePutBackTogether(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	groups := func() []map[string]any {
		return rows(t, call(t, "audit_duplicate_series", map[string]any{"library": "Messy Shows"})["groups"], "groups")
	}
	found := groups()
	if len(found) != 1 {
		t.Fatalf("audit_duplicate_series = %v, want the Twins pair", found)
	}
	var keep, drop map[string]any
	for _, s := range rows(t, found[0]["series"], "series") {
		if str(s["folder"]) == "Zzyzx Twins (2005)" {
			keep = s
		} else {
			drop = s
		}
	}
	if keep == nil || drop == nil {
		t.Fatalf("the pair = %v", found[0])
	}
	if there, _ := held(t, str(keep["series_id"]), 1, 2); there {
		t.Fatal("the show to keep already holds S01E02")
	}
	// the lookups by id see two unrelated shows
	if out := call(t, "show_episodes_exist", map[string]any{"series_id": str(keep["series_id"]), "episodes": []map[string]any{{"season": 1, "episode": 2}}}); out["duplicate_entries"] != nil {
		t.Errorf("show_episodes_exist names duplicates of a show with no ids: %v", out["duplicate_entries"])
	}

	dest := "/media/messy-shows/Zzyzx Twins (2005)/Season 01/Zzyzx Twins S01E02.mp4"
	plan := call(t, "plan_check", map[string]any{"library": "Messy Shows", "entries": []map[string]any{{"path": dest, "series": "Zzyzx Twins (2005)", "season": 1, "episode": 2}}})
	row := rows(t, plan["entries"], "entries")[0]
	if join := object(t, row["would_join"], "would_join"); row["exists"] != false || str(join["series_id"]) != str(keep["series_id"]) || decimal(t, join["claim_similarity"], "claim_similarity") < 0.9 {
		t.Fatalf("plan_check = %v", row)
	}

	// what both folders hold now, to put back exactly
	folders := []string{hostPath(str(keep["path"])), hostPath(str(drop["path"]))}
	before := map[string][]byte{}
	for _, dir := range folders {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				raw, _ := os.ReadFile(path) //nolint:gosec // a fixture under the test data dir
				before[path] = raw
			}
			return nil
		})
	}
	have := seriesCount(t, "Messy Shows")
	t.Cleanup(func() {
		for _, dir := range folders {
			_ = os.RemoveAll(dir)
		}
		for path, raw := range before {
			mediaMkdir(t, filepath.Dir(path))
			mediaWrite(t, path, raw)
		}
		if err := scanUntil("Messy Shows", have); err != nil {
			t.Error(err)
		}
	})
	src := filepath.Join(folders[1], "Season 01", "Zzyzx Twins S01E02.mp4")
	if err := os.Rename(src, hostPath(dest)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(folders[1]); err != nil {
		t.Fatal(err)
	}
	if err := scanUntil("Messy Shows", have-1); err != nil {
		t.Fatal(err)
	}

	if got := groups(); len(got) != 0 {
		t.Errorf("after the merge audit_duplicate_series = %v", got)
	}
	var numbers []int
	for _, e := range rows(t, call(t, "show_episodes", map[string]any{"series_id": str(keep["series_id"])})["episodes"], "episodes") {
		numbers = append(numbers, num(t, e["episode"], "episode"))
	}
	slices.Sort(numbers)
	if !slices.Equal(numbers, []int{1, 2}) {
		t.Errorf("the show kept holds episodes %v, want 1 and 2", numbers)
	}
	if _, err := invoke("item_get", map[string]any{"id": str(drop["series_id"])}); err == nil {
		t.Error("the emptied show is still on the server")
	}
}
