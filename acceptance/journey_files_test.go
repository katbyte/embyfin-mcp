//go:build integration

package acceptance

import (
	"bytes"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Journeys through the files on disk: an episode imported into a gap, a copy
// upgraded in place, how far a delete reaches, a show held twice put back
// together, and episodes imported into a show whose run TMDB knows. Each lays
// out what it needs, checks every tool along the chain agrees with the one
// before, and takes it all away again.

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
	for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": tng})["episodes"], "episodes") {
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
	// and it is the episode the nfo written beside it names
	if name := str(call(t, "item_get", map[string]any{"id": id})["name"]); name != "The Naked Now" {
		t.Errorf("the imported episode reads as %q, want its nfo's The Naked Now", name)
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

// A copy upgraded in place: audit_quality lists the rip, plan_check shows
// the path taken and by how much the file grows, the file is written over,
// and the library scanned. The one episode saved since then is the upgraded
// one, audit_quality has let go of it, and library_episodes reads the new
// picture. The item is the same one throughout, so what people did with it -
// watched it, put it in a playlist - is still there. (Which copy is better is
// quality_compare's, and TestQualityCompare's.)
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

	// the rip is on the quality audit's worklist, for being 360p
	listed := func() (detail string, replaced map[string]any, note string) {
		out := call(t, "audit_quality", map[string]any{"library": "Messy Shows"})
		for _, f := range rows(t, out["findings"], "findings") {
			if str(f["id"]) == rip {
				detail = str(f["detail"])
			}
		}
		for _, r := range rows(t, out["replaced"], "replaced") {
			if str(r["id"]) == rip {
				replaced = r
			}
		}
		return detail, replaced, str(out["note"])
	}
	if d, _, _ := listed(); d != "h264 640x360: 360p, below 720p" {
		t.Fatalf("audit_quality says %q of the rip, want it listed as 360p", d)
	}

	// alice has watched it and it sits in a playlist, which an upgrade in
	// place keeps: the item is the same one
	call(t, "item_set_state", map[string]any{"id": rip, "user": "alice", "watched": true})
	t.Cleanup(func() { _, _ = invoke("item_set_state", map[string]any{"id": rip, "user": "alice", "watched": false}) })
	pl := str(call(t, "playlist_create", map[string]any{"name": "Zzyzx Upgrade", "item_ids": []any{rip}, "media_type": "Video"})["id"])
	deleteLater(t, "playlist_delete", "playlist", pl)
	alicePlayed := func() bool {
		for _, u := range rows(t, call(t, "item_last_watched", map[string]any{"id": rip})["users"], "users") {
			if str(u["user"]) == "alice" {
				return boolOf(u["played"])
			}
		}
		return false
	}
	inPlaylist := func() bool {
		for _, e := range rows(t, call(t, "playlist_get", map[string]any{"playlist": pl})["entries"], "entries") {
			if str(e["id"]) == rip {
				return true
			}
		}
		return false
	}
	if !alicePlayed() || !inPlaylist() {
		t.Fatalf("before the upgrade: alice played it %v, in the playlist %v", alicePlayed(), inPlaylist())
	}
	stats := call(t, "user_stats", map[string]any{"user": "alice"})

	// the incoming file is the clean library's 720p encode, byte for byte
	incoming := fixtureVideo(t, "shows", "Severance", "Season 01", "Severance S01E01.mp4")

	// the path is taken, by a smaller file
	onDisk, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": messy, "season": 1})["episodes"], "episodes") {
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
		for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": messy, "season": 1})["episodes"], "episodes") {
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
	detail, replaced, note := listed()
	if detail != "" {
		t.Errorf("audit_quality still finds the upgraded episode: %s", detail)
	}
	if isJellyfin() {
		// Jellyfin does not say when a file was written, so it cannot tell
		if replaced != nil || !strings.Contains(note, "Jellyfin does not say") {
			t.Errorf("audit_quality on Jellyfin: replaced %v, note %q", replaced, note)
		}
	} else if replaced == nil || num(t, replaced["size"], "size") != len(incoming) {
		// Emby read the new file's size and time, and the time says replaced
		t.Errorf("audit_quality replaced = %v, want the upgraded episode at %d bytes", replaced, len(incoming))
	}
	for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": messy, "season": 1})["episodes"], "episodes") {
		if str(e["id"]) == rip && (num(t, e["height"], "height") != 720 || num(t, e["size"], "size") != len(incoming)) {
			t.Errorf("library_episodes reads the upgraded episode as %vx%v, %v bytes", e["width"], e["height"], e["size"])
		}
	}

	// the same item, still watched by alice, still in the playlist, and her
	// counts as they were
	if _, id := held(t, messy, 1, 4); id != rip {
		t.Errorf("S01E04 is item %s after the upgrade, was %s", id, rip)
	}
	if !alicePlayed() {
		t.Error("item_last_watched no longer has alice playing the upgraded episode")
	}
	if !inPlaylist() {
		t.Error("the playlist lost the upgraded episode")
	}
	if now := call(t, "user_stats", map[string]any{"user": "alice"}); !reflect.DeepEqual(now, stats) {
		t.Errorf("alice's user_stats changed with the upgrade:\nbefore %v\nafter  %v", stats, now)
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
// with it - every version, the nfo, the artwork, the subtitle, the trailer -
// a series or a season takes its folder, and an episode takes its own file
// and the ones named after it. Without confirm the tool refuses and says what
// would go; with it, the answer lists what went, and that is what left the
// disk.
func TestHowFarADeleteReaches(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}

	t.Run("a film in two versions", func(t *testing.T) {
		have := movieCount(t, "Messy Movies")
		const name = "Dune (1984)"
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
		// everything a film's folder gathers besides the film: its poster, an
		// nfo (with no ids, which would join it to the messy Dune), a subtitle
		// and a trailer
		mediaWrite(t, filepath.Join(folder, "poster.jpg"), fixtureVideo(t, "messy-movies", messyBladeRunner, "poster.jpg"))
		mediaWrite(t, filepath.Join(folder, "movie.nfo"), movieNfo("Dune", 1984, "", ""))
		mediaWrite(t, filepath.Join(folder, name+".eng.srt"), fixtureVideo(t, "movies", "The Thirteenth Floor (1999)", "The Thirteenth Floor (1999).eng.srt"))
		mediaWrite(t, filepath.Join(folder, name+"-trailer.mp4"), fixtureVideo(t, "messy-movies", messyArrival, messyArrival+".mp4"))
		sidecars := []string{"poster.jpg", "movie.nfo", name + ".eng.srt", name + "-trailer.mp4"}
		added := 2
		if versionsMerged() {
			added = 1
		}
		if err := scanUntil("Messy Movies", have+added); err != nil {
			t.Fatal(err)
		}
		// the search answers with the messy Dune as well, the film its nfo
		// says is this one in a folder saying 2021, so the folder decides
		var ids, paths []string
		for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "query": "Dune"})["items"], "items") {
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
		for _, want := range []string{"without confirm=true", "nothing was deleted"} {
			if !strings.Contains(msg, want) {
				t.Errorf("the refusal does not say %q: %s", want, msg)
			}
		}
		if whole, names := wouldRemove(t, msg); whole != server || !slices.Equal(names, sorted(append([]string{name + " - 1080p.mp4", name + " - 2160p.mp4"}, sidecars...))) {
			t.Errorf("the refusal would take %q %v, want the folder with both cuts and %v", whole, names, sidecars)
		}
		if _, err := os.Stat(filepath.Join(folder, name+" - 2160p.mp4")); err != nil {
			t.Fatalf("the refused delete removed a file: %v", err)
		}

		out := call(t, "item_delete", map[string]any{"id": ids[0], "confirm": true})
		if !strings.Contains(str(out["deleted"]), paths[0]) {
			t.Errorf("item_delete = %v, want the path it deleted, %s", out, paths[0])
		}
		got := removedPaths(t, out)
		want := []string{server + "/", server + "/" + name + " - 1080p.mp4", server + "/" + name + " - 2160p.mp4"}
		for _, s := range sidecars {
			want = append(want, server+"/"+s)
		}
		if !slices.Equal(got, sorted(want)) {
			t.Errorf("removed = %v, want %v", got, sorted(want))
		}
		if _, err := os.Stat(folder); !os.IsNotExist(err) {
			left, _ := os.ReadDir(folder)
			t.Errorf("after the delete the folder is still there, holding %v", left)
		}
		// and once a scan has run, nothing of the film is left
		if err := scanUntil("Messy Movies", have); err != nil {
			t.Fatal(err)
		}
		for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Movies", "query": "Dune"})["items"], "items") {
			if strings.Contains(str(it["path"]), "/"+name+"/") {
				t.Errorf("after a scan the library still holds %v", it["path"])
			}
		}
	})

	// a series staged beside the lasting ones: deleting it takes its folder,
	// and every other series in the library - its item, its folder, every
	// file in it - is left as it was
	t.Run("a series", func(t *testing.T) {
		have := seriesCount(t, "Messy Shows")
		root := filepath.Join(dataDir(), "messy-shows")
		folder := filepath.Join(root, "DuckTales (1987)")
		t.Cleanup(func() {
			_ = os.RemoveAll(folder)
			if err := scanUntil("Messy Shows", have); err != nil {
				t.Error(err)
			}
		})
		// what the rest of the library holds, on disk and on the server, to
		// hold it to after the delete
		onDisk := func() map[string][]byte {
			out := map[string][]byte{}
			_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				switch {
				case err != nil:
				case path == folder && d.IsDir():
					return filepath.SkipDir
				case d.IsDir():
					out[path+"/"] = nil
				default:
					out[path], _ = os.ReadFile(path) //nolint:gosec // a fixture under the test data dir
				}
				return nil
			})
			return out
		}
		series := func() []string {
			var out []string
			for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy Shows", "types": "Series", "limit": 50})["items"], "items") {
				n := len(rows(t, call(t, "library_episodes", map[string]any{"series_id": str(it["id"])})["episodes"], "episodes"))
				out = append(out, fmt.Sprintf("%s %s at %s, %d episodes", str(it["id"]), str(it["name"]), str(it["path"]), n))
			}
			slices.Sort(out)
			return out
		}
		keptOnDisk, keptSeries := onDisk(), series()
		if len(keptSeries) != have {
			t.Fatalf("Messy Shows lists %d series, counts %d", len(keptSeries), have)
		}

		files := []string{"DuckTales S01E01 - Don't Give Up the Ship (1).mp4", "DuckTales S01E02 - Wronguay in Ronguay (2).mp4"}
		ep := fixtureVideo(t, "messy-shows", "Severance", "Season 01", "Severance S01E01.mp4")
		mediaMkdir(t, filepath.Join(folder, "Season 01"))
		for _, f := range files {
			mediaWrite(t, filepath.Join(folder, "Season 01", f), ep)
		}
		if err := scanUntil("Messy Shows", have+1); err != nil {
			t.Fatal(err)
		}
		id := findItem(t, "Messy Shows", "Series", "DuckTales")
		episodes := rows(t, call(t, "library_episodes", map[string]any{"series_id": id})["episodes"], "episodes")
		if len(episodes) != 2 {
			t.Fatalf("the staged series holds %v, want two episodes", episodes)
		}

		server := "/media/messy-shows/DuckTales (1987)"
		if msg := callErr(t, "item_delete", map[string]any{"id": id}); !strings.Contains(msg, "would remove the folder "+server+" with everything in it") || !strings.Contains(msg, "Season 01/"+files[1]) {
			t.Errorf("the refusal for a series: %s", msg)
		}

		// the answer names the series and its folder; every episode file in
		// the folder goes with it
		out := call(t, "item_delete", map[string]any{"id": id, "confirm": true})
		if d := str(out["deleted"]); !strings.HasPrefix(d, "DuckTales") || !strings.Contains(d, server) {
			t.Errorf("item_delete of a series = %v", out)
		}
		got := removedPaths(t, out)
		for _, want := range []string{server + "/", server + "/Season 01/", server + "/Season 01/" + files[0], server + "/Season 01/" + files[1]} {
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

		// and the rest of the library is untouched: nothing removed outside
		// the series' folder, every other folder and file as it was, and the
		// server holding every other series as it did
		for _, p := range got {
			if !strings.HasPrefix(p, server+"/") {
				t.Errorf("removed names %s, outside the series' folder", p)
			}
		}
		now := onDisk()
		for path, raw := range keptOnDisk {
			if on, ok := now[path]; !ok || !bytes.Equal(on, raw) {
				t.Errorf("%s changed or went with the delete", strings.TrimPrefix(path, root))
			}
		}
		for path := range now {
			if _, ok := keptOnDisk[path]; !ok {
				t.Errorf("%s appeared with the delete", strings.TrimPrefix(path, root))
			}
		}
		if err := scanUntil("Messy Shows", have); err != nil {
			t.Fatal(err)
		}
		if after := series(); !slices.Equal(after, keptSeries) {
			t.Errorf("after the delete Messy Shows holds %v, want %v", after, keptSeries)
		}
	})

	// a second season staged in the messy Severance: deleting it takes its
	// folder and leaves the series, its first season and every file of it
	t.Run("a season", func(t *testing.T) {
		sev := findItem(t, "Messy Shows", "Series", "Severance")
		show := filepath.Join(dataDir(), "messy-shows", "Severance")
		folder := filepath.Join(show, "Season 02")
		t.Cleanup(func() {
			_ = os.RemoveAll(folder)
			scanUntilTrue(t, "Messy Shows", func() bool { there, _ := held(t, sev, 2, 1); return !there })
		})
		keep := treeOf(t, show)
		first := len(rows(t, call(t, "library_episodes", map[string]any{"series_id": sev, "season": 1})["episodes"], "episodes"))
		ep := fixtureVideo(t, "messy-shows", "Severance", "Season 01", "Severance S01E01.mp4")
		mediaMkdir(t, folder)
		for n, title := range map[int]string{1: "Hello, Ms. Cobel", 2: "Goodbye, Mrs. Selvig"} {
			base := filepath.Join(folder, fmt.Sprintf("Severance S02E%02d", n))
			mediaWrite(t, base+".mp4", ep)
			mediaWrite(t, base+".nfo", episodeNfo(title, 2, n))
		}
		scanUntilTrue(t, "Messy Shows", func() bool {
			one, _ := held(t, sev, 2, 1)
			two, _ := held(t, sev, 2, 2)
			return one && two
		})
		var id string
		for _, s := range rows(t, call(t, "show_seasons", map[string]any{"series_id": sev})["seasons"], "seasons") {
			if num(t, s["season"], "season") == 2 {
				id = str(s["id"])
			}
		}
		episodes := rows(t, call(t, "library_episodes", map[string]any{"series_id": sev, "season": 2})["episodes"], "episodes")
		if id == "" || len(episodes) != 2 {
			t.Fatalf("the staged season is %q holding %v", id, episodes)
		}

		server := "/media/messy-shows/Severance/Season 02"
		files := []string{"Severance S02E01.mp4", "Severance S02E01.nfo", "Severance S02E02.mp4", "Severance S02E02.nfo"}
		if folder, names := wouldRemove(t, callErr(t, "item_delete", map[string]any{"id": id})); folder != server || !slices.Equal(names, files) {
			t.Errorf("the refusal for a season would take %q %v, want its folder with %v", folder, names, files)
		}
		out := call(t, "item_delete", map[string]any{"id": id, "confirm": true})
		want := []string{server + "/"}
		for _, f := range files {
			want = append(want, server+"/"+f)
		}
		if got := removedPaths(t, out); !slices.Equal(got, want) {
			t.Errorf("removed = %v, want %v", got, want)
		}
		sameTree(t, dataDir(), keep, treeOf(t, show))
		for _, e := range episodes {
			if _, err := invoke("item_get", map[string]any{"id": str(e["id"])}); err == nil {
				t.Errorf("episode %v outlived its season", e["title"])
			}
		}
		var seasons []int
		for _, s := range rows(t, call(t, "show_seasons", map[string]any{"series_id": sev})["seasons"], "seasons") {
			seasons = append(seasons, num(t, s["season"], "season"))
		}
		if !slices.Equal(seasons, []int{1}) {
			t.Errorf("after the delete the series has seasons %v, want [1]", seasons)
		}
		if n := len(rows(t, call(t, "library_episodes", map[string]any{"series_id": sev, "season": 1})["episodes"], "episodes")); n != first {
			t.Errorf("the first season holds %d episodes after the delete, %d before", n, first)
		}
	})

	// one episode held twice, the way a second download of it lands: the
	// messy Severance's Half Loop again as S01E05, with an nfo of its own and
	// a subtitle. audit_duplicate_episodes finds the pair, and deleting the
	// copy takes its three files and nothing of the episode it copies
	t.Run("an episode held twice", func(t *testing.T) {
		sev := findItem(t, "Messy Shows", "Series", "Severance")
		season := filepath.Join(dataDir(), "messy-shows", "Severance", "Season 01")
		base := filepath.Join(season, "Severance S01E05")
		copies := []string{base + ".mp4", base + ".nfo", base + ".eng.srt"}
		t.Cleanup(func() {
			for _, f := range copies {
				_ = os.Remove(f)
			}
			scanUntilTrue(t, "Messy Shows", func() bool { there, _ := held(t, sev, 1, 5); return !there })
		})
		mediaWrite(t, copies[0], fixtureVideo(t, "messy-shows", "Severance", "Season 01", "Severance S01E02.mp4"))
		mediaWrite(t, copies[1], episodeNfo("Half Loop", 1, 5))
		mediaWrite(t, copies[2], fixtureVideo(t, "movies", "The Thirteenth Floor (1999)", "The Thirteenth Floor (1999).eng.srt"))
		scanUntilTrue(t, "Messy Shows", func() bool { there, _ := held(t, sev, 1, 5); return there })
		_, original := held(t, sev, 1, 2)
		_, copied := held(t, sev, 1, 5)

		pair := func() []int {
			for _, g := range rows(t, call(t, "audit_duplicate_episodes", map[string]any{"library": "Messy Shows"})["groups"], "groups") {
				if title(str(g["series"])) != "Severance" {
					continue
				}
				var numbers []int
				for _, e := range rows(t, g["episodes"], "episodes") {
					numbers = append(numbers, num(t, e["episode"], "episode"))
				}
				return numbers
			}
			return nil
		}
		if got := pair(); !slices.Equal(got, []int{2, 5}) {
			t.Fatalf("audit_duplicate_episodes pairs the messy Severance's %v, want [2 5]", got)
		}

		server := "/media/messy-shows/Severance/Season 01/Severance S01E05"
		own := []string{server + ".eng.srt", server + ".mp4", server + ".nfo"}
		if folder, names := wouldRemove(t, callErr(t, "item_delete", map[string]any{"id": copied})); folder != "" || !slices.Equal(names, own) {
			t.Errorf("the refusal for the copy would take %q %v, want its own files %v", folder, names, own)
		}
		var gone []string
		for _, p := range own {
			gone = append(gone, hostPath(p))
		}
		keep := treeOf(t, season, gone...)
		out := call(t, "item_delete", map[string]any{"id": copied, "confirm": true})
		if got := removedPaths(t, out); !slices.Equal(got, own) {
			t.Errorf("removed = %v, want %v", got, own)
		}
		sameTree(t, dataDir(), keep, treeOf(t, season))

		if got := pair(); got != nil {
			t.Errorf("after the delete audit_duplicate_episodes still pairs %v", got)
		}
		if there, _ := held(t, sev, 1, 5); there {
			t.Error("show_episodes_exist still holds S01E05")
		}
		if there, id := held(t, sev, 1, 2); !there || id != original {
			t.Errorf("S01E02 is held %v as %q, want the original %s", there, id, original)
		}
	})
}

// One show held twice, put back together with the tools:
// audit_duplicate_series finds A Knight of the Seven Kingdoms in two folders
// a space and a letter's case apart, and every tool that takes a show by name
// refuses it as a tie between the two. plan_check places the second folder's
// episode under the first show, the file is moved there, and item_delete
// removes the emptied show, naming its own folder and nothing of the one kept
// a space and a letter's case away. After a scan the name finds one show
// holding both episodes, and the audit has nothing to report. Neither show
// carries an id, so the folder names are the only thing that says they are
// one: the lookups that go by provider id cannot see the pair at all.
func TestAShowHeldTwicePutBackTogether(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	const name = "A Knight of the Seven Kingdoms"
	groups := func() []map[string]any {
		return rows(t, call(t, "audit_duplicate_series", map[string]any{"library": "Messy Shows"})["groups"], "groups")
	}
	found := groups()
	if len(found) != 1 {
		t.Fatalf("audit_duplicate_series = %v, want the %s pair", found, name)
	}
	var keep, drop map[string]any
	for _, s := range rows(t, found[0]["series"], "series") {
		if str(s["folder"]) == name+" (2026)" {
			keep = s
		} else {
			drop = s
		}
	}
	if keep == nil || drop == nil {
		t.Fatalf("the pair = %v", found[0])
	}
	keepID, dropID := str(keep["series_id"]), str(drop["series_id"])
	if there, _ := held(t, keepID, 1, 2); there {
		t.Fatal("the show to keep already holds S01E02")
	}
	// the lookups by id see two unrelated shows
	if out := call(t, "show_episodes_exist", map[string]any{"series_id": keepID, "episodes": []map[string]any{{"season": 1, "episode": 2}}}); out["duplicate_entries"] != nil {
		t.Errorf("show_episodes_exist names duplicates of a show with no ids: %v", out["duplicate_entries"])
	}
	// and the name is a tie: show_resolve scores both alike, and a tool
	// taking the show by name refuses to pick one, naming both
	cands := rows(t, call(t, "show_resolve", map[string]any{"title": name, "library": "Messy Shows"})["candidates"], "candidates")
	if len(cands) < 2 || decimal(t, cands[0]["score"], "score") != decimal(t, cands[1]["score"], "score") ||
		!slices.Equal(sorted([]string{str(cands[0]["series_id"]), str(cands[1]["series_id"])}), sorted([]string{keepID, dropID})) {
		t.Errorf("show_resolve = %v, want the pair first at one score", cands)
	}
	byName := map[string]map[string]any{
		"show_episodes_exist": {"series": name, "library": "Messy Shows", "episodes": []map[string]any{{"season": 1, "episode": 2}}},
		"library_episodes":    {"series": name, "library": "Messy Shows"},
	}
	for tool, args := range byName {
		if msg := callErr(t, tool, args); !strings.Contains(msg, "id "+keepID) || !strings.Contains(msg, "id "+dropID) || !strings.Contains(msg, "give series_id") {
			t.Errorf("%s by name: %s", tool, msg)
		}
	}

	// alice has watched the second folder's episode
	var watched string
	for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": dropID})["episodes"], "episodes") {
		if num(t, e["episode"], "episode") == 2 {
			watched = str(e["id"])
		}
	}
	if watched == "" {
		t.Fatal("the second folder holds no S01E02")
	}
	call(t, "item_set_state", map[string]any{"id": watched, "user": "alice", "watched": true})
	t.Cleanup(func() {
		_, _ = invoke("item_set_state", map[string]any{"id": watched, "user": "alice", "watched": false})
	})

	dest := "/media/messy-shows/" + name + " (2026)/Season 01/" + name + " S01E02.mp4"
	plan := call(t, "plan_check", map[string]any{"library": "Messy Shows", "entries": []map[string]any{{"path": dest, "series": name + " (2026)", "season": 1, "episode": 2}}})
	row := rows(t, plan["entries"], "entries")[0]
	if join := object(t, row["would_join"], "would_join"); row["exists"] != false || str(join["series_id"]) != keepID || decimal(t, join["claim_similarity"], "claim_similarity") < 0.9 {
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
		// alice's watched state went with the item the delete took, and Emby
		// hands it back to the episode the restored file makes (it keys the
		// state by more than the id), so it is cleared there as well
		out, err := invoke("library_items", map[string]any{"library": "Messy Shows", "types": "Series", "limit": 50})
		if err != nil {
			t.Error(err)
		}
		for _, s := range rowsOf(out["items"]) {
			if str(s["path"]) != str(drop["path"]) {
				continue
			}
			eps, _ := invoke("library_episodes", map[string]any{"series_id": str(s["id"])})
			for _, e := range rowsOf(eps["episodes"]) {
				if _, err := invoke("item_set_state", map[string]any{"id": str(e["id"]), "user": "alice", "watched": false}); err != nil {
					t.Errorf("clearing alice's watched state on the restored %v: %v", e["path"], err)
				}
			}
		}
	})
	src := filepath.Join(folders[1], "Season 01", name+" S01E02.mp4")
	if err := os.Rename(src, hostPath(dest)); err != nil {
		t.Fatal(err)
	}

	// the emptied show goes with the tool: its own folder, and nothing of
	// the kept one, which holds the file just moved
	msg := callErr(t, "item_delete", map[string]any{"id": dropID})
	if folder, names := wouldRemove(t, msg); folder != str(drop["path"]) || !slices.Equal(names, []string{"Season 01"}) || strings.Contains(msg, str(keep["path"])) {
		t.Errorf("the refusal for the emptied show would take %q %v: %s", folder, names, msg)
	}
	kept := treeOf(t, folders[0])
	out := call(t, "item_delete", map[string]any{"id": dropID, "confirm": true})
	if got, want := removedPaths(t, out), []string{str(drop["path"]) + "/", str(drop["path"]) + "/Season 01/"}; !slices.Equal(got, want) {
		t.Errorf("removed = %v, want %v", got, want)
	}
	if _, err := os.Stat(folders[1]); !os.IsNotExist(err) {
		t.Errorf("the emptied show's folder is still on disk: %v", err)
	}
	sameTree(t, dataDir(), kept, treeOf(t, folders[0]))
	if err := scanUntil("Messy Shows", have-1); err != nil {
		t.Fatal(err)
	}

	if got := groups(); len(got) != 0 {
		t.Errorf("after the merge audit_duplicate_series = %v", got)
	}
	var numbers []int
	for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": keepID})["episodes"], "episodes") {
		numbers = append(numbers, num(t, e["episode"], "episode"))
	}
	slices.Sort(numbers)
	if !slices.Equal(numbers, []int{1, 2}) {
		t.Errorf("the show kept holds episodes %v, want 1 and 2", numbers)
	}
	if _, err := invoke("item_get", map[string]any{"id": dropID}); err == nil {
		t.Error("the emptied show is still on the server")
	}
	// the name finds the one show now, holding the moved episode
	res := call(t, "show_episodes_exist", byName["show_episodes_exist"])
	ep := rows(t, res["episodes"], "episodes")[0]
	if !boolOf(ep["exists"]) || str(res["series_id"]) != keepID {
		t.Errorf("show_episodes_exist by name after the merge = %v", res)
	}

	// the moved file is a new item in the kept show, and what alice had
	// watched was the item the delete took: her watched state does not
	// follow the file on either server
	moved := str(ep["id"])
	if moved == watched {
		t.Errorf("the moved episode kept its id %s", moved)
	}
	for _, u := range rows(t, call(t, "item_last_watched", map[string]any{"id": moved})["users"], "users") {
		if str(u["user"]) == "alice" && boolOf(u["played"]) {
			t.Errorf("alice's watched state followed the moved file: %v", u)
		}
	}
}

// An episode imported into a show whose run TMDB knows: what TMDB lists and
// the library lacks starts one episode later, in audit_missing_episodes and
// in show_missing alike. Then one file holding the next two episodes, which
// the library holds as both. The messy Severance, which names its TMDB id in
// its nfo and holds the first three episodes of its first season.
func TestAnImportLeavesTheProvidersList(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	needsTMDBRecording(t, "GET api.themoviedb.org/3/tv/95396")
	sev := findItem(t, "Messy Shows", "Series", "Severance")
	season := filepath.Join(dataDir(), "messy-shows", "Severance", "Season 01")
	staged := []string{"Severance S01E04.mp4", "Severance S01E04.nfo", "Severance S01E05E06.mp4", "Severance S01E05E06.nfo"}
	t.Cleanup(func() {
		for _, f := range staged {
			_ = os.Remove(filepath.Join(season, f))
		}
		scanUntilTrue(t, "Messy Shows", func() bool {
			four, _ := held(t, sev, 1, 4)
			five, _ := held(t, sev, 1, 5)
			return !four && !five
		})
	})

	// the first of what TMDB lists without a file, by the audit and by
	// show_missing
	const listedBy = "listed by TMDB without a file: "
	firstListed := func() string {
		for _, f := range rows(t, call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows", "provider": true})["findings"], "findings") {
			if d := str(f["detail"]); str(f["id"]) == sev && strings.Contains(d, listedBy) {
				first, _, _ := strings.Cut(d[strings.Index(d, listedBy)+len(listedBy):], ",")
				return first
			}
		}
		return ""
	}
	firstMissing := func() string {
		out := call(t, "show_missing", map[string]any{"series_id": sev})
		missing := rows(t, out["missing"], "missing")
		if str(out["source"]) != "tmdb" || len(missing) == 0 {
			t.Fatalf("show_missing = %v, want TMDB's run", out)
		}
		return fmt.Sprintf("S%02dE%02d", num(t, missing[0]["season"], "season"), num(t, missing[0]["episode"], "episode"))
	}
	if l, m := firstListed(), firstMissing(); l != "S01E04" || m != "S01E04" {
		t.Fatalf("before the import TMDB's list starts at %s in the audit and %s in show_missing, want S01E04", l, m)
	}

	ep := fixtureVideo(t, "messy-shows", "Severance", "Season 01", "Severance S01E01.mp4")
	mediaWrite(t, filepath.Join(season, staged[0]), ep)
	mediaWrite(t, filepath.Join(season, staged[1]), episodeNfo("The You You Are", 1, 4))
	scanUntilTrue(t, "Messy Shows", func() bool { there, _ := held(t, sev, 1, 4); return there })
	if l, m := firstListed(), firstMissing(); l != "S01E05" || m != "S01E05" {
		t.Errorf("after importing S01E04 TMDB's list starts at %s in the audit and %s in show_missing, want S01E05", l, m)
	}

	// one file for the next two, its nfo ending the run at the second: no
	// title, which neither server needs to number it
	mediaWrite(t, filepath.Join(season, staged[2]), ep)
	mediaWrite(t, filepath.Join(season, staged[3]), []byte(`<?xml version="1.0" encoding="utf-8"?>
<episodedetails>
  <season>1</season>
  <episode>5</episode>
  <episodenumberend>6</episodenumberend>
</episodedetails>
`))
	both := func() []map[string]any {
		out := call(t, "show_episodes_exist", map[string]any{"series_id": sev, "episodes": []map[string]any{{"season": 1, "episode": 5}, {"season": 1, "episode": 6}}})
		return rows(t, out["episodes"], "episodes")
	}
	scanUntilTrue(t, "Messy Shows", func() bool {
		rows := both()
		return boolOf(rows[0]["exists"]) && boolOf(rows[1]["exists"])
	})
	for _, row := range both() {
		if str(row["covered_by"]) != "S01E05E06" || !strings.HasSuffix(str(row["path"]), "/"+staged[2]) {
			t.Errorf("episode %v is held %v by %v at %v, want the two-episode file", row["episode"], row["exists"], row["covered_by"], row["path"])
		}
	}
	if l, m := firstListed(), firstMissing(); l != "S01E07" || m != "S01E07" {
		t.Errorf("after importing S01E05E06 TMDB's list starts at %s in the audit and %s in show_missing, want S01E07", l, m)
	}
}
