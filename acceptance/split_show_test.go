//go:build integration

package acceptance

import (
	"maps"
	"slices"
	"strings"
	"testing"

	acc "github.com/katbyte/embyfin-mcp/lib/acceptance"
)

// The Wire, split by a folder rename: "The Wire" with no year and "The Wire
// (2002)", both tvshow.nfo files carrying the show's ids, the first two
// episodes in the old folder and the second and third in the new one, the
// second at 360p in the old and 720p in the new.
//
// What a caller asking about the show has to be told: that it is one show
// held twice, and which files it is held in. Both servers keep two series,
// and both answer either one with the other's episodes too when asked for a
// series' seasons or episodes - they merge entries that share their ids - so
// each half holds all three episodes as far as a question by id goes.
func TestAShowSplitByAFolderRename(t *testing.T) {
	halves := map[string]string{} // folder -> series id
	for _, it := range acc.Rows(t, suite.Call(t, "library_items", map[string]any{"library": "Messy Shows", "query": "The Wire", "limit": 50})["items"], "items") {
		if acc.Str(it["name"]) == "The Wire" {
			halves[acc.Str(it["path"])] = acc.Str(it["id"])
		}
	}
	old, renamed := halves["/media/messy-shows/The Wire"], halves["/media/messy-shows/The Wire (2002)"]
	if len(halves) != 2 || old == "" || renamed == "" {
		t.Fatalf("The Wire's entries = %v, want one for each folder", halves)
	}

	t.Run("the folder audit keeps them apart", func(t *testing.T) {
		// the folders differ by the year, which a folder's name is compared
		// with, so this is not the rename that audit catches (a space, a
		// letter's case): only the Knight pair is grouped
		out := suite.Call(t, "audit_duplicates", map[string]any{"library": "Messy Shows"})
		for _, g := range acc.Rows(t, out["folder_groups"], "folder_groups") {
			if strings.Contains(acc.Str(g["key"]), "wire") {
				t.Errorf("audit_duplicates' folder rule groups The Wire: %v", g)
			}
		}
		if acc.Num(t, out["total_folder_groups"], "total_folder_groups") != 1 {
			t.Errorf("audit_duplicates folder_groups = %v, want the Knight pair alone", out["folder_groups"])
		}
	})

	t.Run("the id audit joins them", func(t *testing.T) {
		var wire [][]string
		groups, _ := suite.Call(t, "audit_duplicates", map[string]any{"library": "Messy Shows"})["groups"].([]any)
		for _, g := range groups {
			var paths []string
			for _, it := range acc.Rows(t, g, "group") {
				if acc.Str(it["name"]) == "The Wire" && acc.Str(it["type"]) == "Series" {
					paths = append(paths, acc.Str(it["path"]))
				}
			}
			if len(paths) > 0 {
				wire = append(wire, acc.Sorted(paths))
			}
		}
		// the episodes carry no ids of their own, so the second one held
		// twice is no group here: the series is
		if want := []string{"/media/messy-shows/The Wire", "/media/messy-shows/The Wire (2002)"}; len(wire) != 1 || !slices.Equal(wire[0], want) {
			t.Errorf("The Wire's groups = %v, want one of the two series", wire)
		}
	})

	t.Run("either half answers for the whole show", func(t *testing.T) {
		ask := []map[string]any{{"season": 1, "episode": 1}, {"season": 1, "episode": 2}, {"season": 1, "episode": 3}, {"season": 1, "episode": 4}}
		for half, id := range map[string]string{"the old folder": old, "the renamed one": renamed} {
			other := map[string]string{old: renamed, renamed: old}[id]
			out := suite.Call(t, "show_episodes_exist", map[string]any{"series_id": id, "episodes": ask, "quality": true})
			got := acc.Rows(t, out["episodes"], "episodes")
			if len(got) != 4 || acc.Num(t, out["absent"], "absent") != 1 || got[3]["exists"] != false {
				t.Errorf("%s: %v, absent %v: want E01 to E03 held and E04 absent", half, got, out["absent"])
				continue
			}
			if dup := acc.Strs(t, out["duplicate_entries"], "duplicate_entries"); !slices.Equal(dup, []string{other}) {
				t.Errorf("%s: duplicate_entries = %v, want the other half %s", half, dup, other)
			}
			// E01 is the old folder's alone and E03 the renamed one's
			for i, folder := range map[int]string{0: "/The Wire/", 2: "/The Wire (2002)/"} {
				if !strings.Contains(acc.Str(got[i]["path"]), folder) || got[i]["other_copies"] != nil {
					t.Errorf("%s: S01E%02d = %v, want the one file in %s", half, i+1, got[i], folder)
				}
			}
			// E02 is held twice: the asked half's own copy answers, and the
			// other half's is named beside it, each at its height
			own, theirs := 360, 720
			if id == renamed {
				own, theirs = 720, 360
			}
			copies := acc.Rows(t, got[1]["other_copies"], "other_copies")
			if acc.Num(t, got[1]["height"], "height") != own || len(copies) != 1 || acc.Str(copies[0]["series_id"]) != other || acc.Num(t, copies[0]["height"], "height") != theirs {
				t.Errorf("%s: S01E02 = %vp, other copies %v: want its own %dp copy and the other half's %dp one", half, got[1]["height"], copies, own, theirs)
			}
		}
	})

	t.Run("by name it is two series", func(t *testing.T) {
		cands := acc.Rows(t, suite.Call(t, "show_resolve", map[string]any{"title": "The Wire", "library": "Messy Shows"})["candidates"], "candidates")
		if len(cands) < 2 || acc.Str(cands[0]["name"]) != "The Wire" || acc.Str(cands[1]["name"]) != "The Wire" || acc.Decimal(t, cands[0]["score"], "score") != 1 || acc.Decimal(t, cands[1]["score"], "score") != 1 {
			t.Fatalf("show_resolve The Wire = %v, want both halves first, each a whole match", cands)
		}
		// a tie is refused naming both, however narrowed
		msg := suite.CallErr(t, "show_episodes_exist", map[string]any{"series": "The Wire", "library": "Messy Shows", "episodes": []map[string]any{{"season": 1, "episode": 2}}})
		if !strings.Contains(msg, "id "+old+" scored 1.00 at /media/messy-shows/The Wire") || !strings.Contains(msg, "id "+renamed+" scored 1.00 at /media/messy-shows/The Wire (2002)") || !strings.Contains(msg, "give series_id") {
			t.Errorf("The Wire by name = %s, want both halves named", msg)
		}
	})

	t.Run("what the show is missing", func(t *testing.T) {
		needsTMDBRecording(t, "GET api.themoviedb.org/3/tv/1438")
		// both servers answer either half with all three episodes, so the
		// run from TMDB is missing the fourth on
		for _, id := range []string{old, renamed} {
			out := suite.Call(t, "show_missing", map[string]any{"series_id": id})
			if got := missingKeys(t, out); out["supported"] != true || len(got) == 0 || got[0] != "S01E04" || out["gaps_on_disk"] != nil {
				t.Errorf("show_missing %s = %v from %v, want the run from S01E04", id, got, out["source"])
			}
			// in TMDB's aired order, which the files are numbered by
			if acc.Str(out["order"]) != "TMDB aired order" || out["held_not_in_run"] != nil || out["warning"] != nil {
				t.Errorf("show_missing %s: order %v, held_not_in_run %v, warning %v", id, out["order"], out["held_not_in_run"], out["warning"])
			}
		}
		// and the sweep reads the two entries as one show: judged apart, each
		// was missing the episodes the other holds
		out := suite.Call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows", "provider": true})
		var wire []map[string]any
		for _, f := range acc.Rows(t, out["findings"], "findings") {
			if acc.Str(f["name"]) == "The Wire" {
				wire = append(wire, f)
			}
		}
		if len(wire) != 1 {
			t.Fatalf("The Wire's findings = %v, want one", wire)
		}
		f := wire[0]
		other := map[string]string{old: renamed, renamed: old}[acc.Str(f["id"])]
		if !strings.HasPrefix(acc.Str(f["detail"]), "listed by TMDB without a file: S01E04, S01E05") || other == "" ||
			!strings.Contains(acc.Str(f["warning"]), `holds "The Wire" under 2 entries sharing its ids (also id `+other+" at ") {
			t.Errorf("The Wire = %v, want the run from S01E04 and the other half named", f)
		}
	})

	t.Run("the copy held twice, as each server shows it", func(t *testing.T) {
		// Emby shows the two copies of the second episode as one episode's
		// versions, and judges its quality by the 720p one; Jellyfin keeps
		// them as two episodes of two series, and the 360p one is a rip
		// worth replacing
		versions := suite.Call(t, "audit_multiple_versions", map[string]any{"library": "Messy Shows"})
		var shownTwice []string
		for _, f := range acc.Rows(t, versions["findings"], "findings") {
			shownTwice = append(shownTwice, acc.Str(f["name"])+": "+acc.Str(f["detail"]))
		}
		want := []string{}
		if !isJellyfin() {
			want = []string{"The Detail: 2 versions: The Wire S01E02.mp4, The Wire S01E02.mp4"}
		}
		if !slices.Equal(shownTwice, want) {
			t.Errorf("episodes in two versions = %v, want %v", shownTwice, want)
		}
		rip := false
		for _, f := range acc.Rows(t, suite.Call(t, "audit_quality", map[string]any{"library": "Messy Shows"})["findings"], "findings") {
			if acc.Str(f["path"]) == "/media/messy-shows/The Wire/Season 01/The Wire S01E02.mp4" {
				rip = true
			}
		}
		if rip != isJellyfin() {
			t.Errorf("the 360p copy on the quality worklist = %v, want it there only where the server keeps it apart (Jellyfin)", rip)
		}
	})

	t.Run("each half lists both folders' seasons", func(t *testing.T) {
		for _, id := range []string{old, renamed} {
			var numbers []int
			folders := map[string]string{} // entry -> the season's folder
			for _, s := range acc.Rows(t, suite.Call(t, "show_seasons", map[string]any{"series_id": id})["seasons"], "seasons") {
				numbers = append(numbers, acc.Num(t, s["season"], "season"))
				folders[acc.Str(s["series_id"])] = acc.Str(s["path"])
			}
			if !slices.Equal(numbers, []int{1, 1}) {
				t.Errorf("show_seasons %s = %v, want season 1 once from each folder", id, numbers)
			}
			// which season 1 is whose: without the entry and the folder the
			// two read as one season listed twice
			if want := map[string]string{old: "/media/messy-shows/The Wire/Season 01", renamed: "/media/messy-shows/The Wire (2002)/Season 01"}; !maps.Equal(folders, want) {
				t.Errorf("show_seasons %s by entry = %v, want %v", id, folders, want)
			}
		}
	})
}
