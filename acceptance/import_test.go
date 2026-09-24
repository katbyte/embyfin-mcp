//go:build integration

package acceptance

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The tools for writing into a library safely: reading destinations before
// writing them, and finding the damage shapes afterwards.

// plan_check answers what is at each destination now, which series it would
// join, and which entries collide with each other.
func TestPlanCheck(t *testing.T) {
	sev := findItem(t, "Shows", "Series", "Severance")
	held := rows(t, call(t, "library_episodes", map[string]any{"series_id": sev})["episodes"], "episodes")
	if len(held) == 0 {
		t.Fatal("Severance holds no episodes")
	}
	occupied := str(held[0]["path"])
	free := filepath.Join(filepath.Dir(occupied), "Severance S01E09 - Nothing Here.mp4")

	out := call(t, "plan_check", map[string]any{"entries": []map[string]any{
		{"path": occupied, "size": 12_000_000, "series": "Severance", "season": 1, "episode": 1},
		{"path": free, "series": "Severance", "season": 1, "episode": 9},
		{"path": free, "series": "Severance", "season": 1, "episode": 9},
		{"path": "/nowhere/at/all/Some Show S01E01.mkv"},
	}})
	entries := rows(t, out["entries"], "entries")
	if len(entries) != 4 {
		t.Fatalf("entries = %v", entries)
	}

	// the occupied one names what is there, from the server's own facts
	first := entries[0]
	if first["exists"] != true {
		t.Fatalf("a path the library holds read as free: %v", first)
	}
	current := object(t, first["current"], "current")
	if str(current["item_id"]) != str(held[0]["id"]) || num(t, current["runtime_s"], "runtime_s") <= 0 {
		t.Errorf("current = %v", current)
	}
	// the incoming size against what is there, so a shrink is visible before
	// it happens rather than after
	if ratio, ok := current["size_ratio"].(float64); !ok || ratio <= 0 {
		t.Errorf("size_ratio = %v", current["size_ratio"])
	}
	if join := object(t, first["would_join"], "would_join"); str(join["series_id"]) != sev {
		t.Errorf("would_join = %v", join)
	}

	// the free path in the same folder: nothing there, same series
	second := entries[1]
	if second["exists"] != false || second["current"] != nil {
		t.Errorf("a free path = %v", second)
	}
	if join := object(t, second["would_join"], "would_join"); str(join["series_id"]) != sev {
		t.Errorf("a free path did not name its series: %v", join)
	}

	// two entries onto one path name each other
	if dups := strs(t, entries[2]["duplicate_of"], "duplicate_of"); len(dups) == 0 {
		t.Errorf("the colliding entries were not flagged: %v", entries[2])
	}
	if num(t, out["duplicates"], "duplicates") != 2 {
		t.Errorf("duplicates = %v, want 2", out["duplicates"])
	}

	// and a path under no series folder says so
	if outside := entries[3]; outside["would_join"] != nil || !strings.Contains(str(outside["note"]), "no series folder") {
		t.Errorf("a path outside the library = %v", outside)
	}
	if num(t, out["unplaced"], "unplaced") != 1 {
		t.Errorf("unplaced = %v, want 1", out["unplaced"])
	}
}

// One episode's content under two episode numbers: the fixtures carry the
// same Breaking Bad title on two episodes, at the same length.
func TestAuditDuplicateEpisodes(t *testing.T) {
	out := call(t, "audit_duplicate_episodes", map[string]any{"library": "Shows"})
	groups := rows(t, out["groups"], "groups")
	if len(groups) != 1 || num(t, out["total_findings"], "total_findings") != 1 {
		t.Fatalf("groups = %v", groups)
	}
	// the only such pair on the server: the messy shows' titles are all
	// their own, and a limit of one holds it
	if whole := call(t, "audit_duplicate_episodes", map[string]any{"limit": 1}); len(rows(t, whole["groups"], "groups")) != 1 || num(t, whole["total_findings"], "total_findings") != 1 {
		t.Errorf("across the server = %v", whole)
	}
	group := groups[0]
	if title(str(group["series"])) != "Breaking Bad" || !strings.Contains(str(group["title"]), "Cat") {
		t.Errorf("group = %v", group)
	}
	// both files are a second long, so the runtimes agree and it is not a
	// guess
	if str(group["confidence"]) != "near_certain" {
		t.Errorf("confidence = %v (runtime_gap %v)", group["confidence"], group["runtime_gap"])
	}
	eps := rows(t, group["episodes"], "episodes")
	if len(eps) != 2 || num(t, eps[0]["episode"], "episode") != 2 || num(t, eps[1]["episode"], "episode") != 3 {
		t.Errorf("the group is not E02 and E03: %v", eps)
	}
	if str(eps[0]["path"]) == "" || num(t, eps[0]["size"], "size") <= 0 {
		t.Errorf("a row lacks what a caller decides on: %v", eps[0])
	}
}

// A file named after a different episode than the one the server holds: the
// fixture names The Expanse S01E02's file "Dulcinea", which is S01E01. Every
// other path in the show library agrees with the server: the series
// folders, the season and episode numbers, the files that claim no title.
func TestAuditFilePathShows(t *testing.T) {
	out := call(t, "audit_file_path", map[string]any{"library": "Shows"})
	findings := rows(t, out["findings"], "findings")
	if len(findings) != 1 || num(t, out["total_findings"], "total_findings") != 1 {
		t.Fatalf("findings = %v", findings)
	}
	f := findings[0]
	if str(f["title_in_file"]) != "Dulcinea" || str(f["title_on_server"]) != "The Big Empty" || str(f["type"]) != "Episode" {
		t.Errorf("finding = %v", f)
	}
	if problems, _ := f["problems"].([]any); len(problems) != 1 || !strings.HasPrefix(str(problems[0]), "title:") {
		t.Errorf("problems = %v, want the title alone", f["problems"])
	}
	if num(t, f["episode"], "episode") != 2 || !strings.Contains(str(f["path"]), "Dulcinea") {
		t.Errorf("finding names the wrong episode: %v", f)
	}
	// with a TMDB token the row says where TMDB puts the file's title: the
	// first episode, so the file is numbered in another order
	if tmdbKey() != "" {
		if str(f["tmdb_episode"]) != "S01E01" || !strings.Contains(str(f["diagnosis"]), "another order") {
			t.Errorf("diagnosis = %v, want the file's title placed at TMDB's S01E01", f)
		}
	}
	// the files whose names claim no title at all are counted, not reported
	if num(t, out["unnamed"], "unnamed") < 8 {
		t.Errorf("unnamed = %v, want the rest of the library's episodes", out["unnamed"])
	}
}

// Two folders for one show, a space and a letter's case apart: the messy
// Zzyzx Twins pair, and the only collision across every series the server
// holds. The two Severances are one show in two libraries, which is not
// this audit's business (audit_duplicates groups them by their ids).
func TestAuditDuplicateSeries(t *testing.T) {
	out := call(t, "audit_duplicate_series", nil)
	if n := num(t, out["items_scanned"], "items_scanned"); n != 3+messySeries {
		t.Errorf("items_scanned = %d, want every series in the fixtures (%d)", n, 3+messySeries)
	}
	groups := rows(t, out["groups"], "groups")
	if len(groups) != 1 || num(t, out["total_findings"], "total_findings") != 1 {
		t.Fatalf("groups = %v, want the Twins pair alone", groups)
	}
	var folders []string
	for _, s := range rows(t, groups[0]["series"], "series") {
		folders = append(folders, str(s["folder"]))
		if str(s["series_id"]) == "" || num(t, s["year"], "year") != 2005 || !strings.HasPrefix(str(s["path"]), "/media/messy-shows/") {
			t.Errorf("series = %v", s)
		}
	}
	if !slices.Equal(sorted(folders), []string{"Zzyzx  twins (2005)", "Zzyzx Twins (2005)"}) {
		t.Errorf("the pair = %v", folders)
	}
	// one library at a time finds it in its own, and a limit caps the rows
	if n := num(t, call(t, "audit_duplicate_series", map[string]any{"library": "Messy Shows", "limit": 1})["total_findings"], "total_findings"); n != 1 {
		t.Errorf("Messy Shows = %d groups", n)
	}
	if n := num(t, call(t, "audit_duplicate_series", map[string]any{"library": "Shows"})["total_findings"], "total_findings"); n != 0 {
		t.Errorf("Shows = %d groups", n)
	}
}
