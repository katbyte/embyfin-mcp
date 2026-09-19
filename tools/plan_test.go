package tools

import (
	"strings"
	"testing"
)

// C18: the call to make before writing. A bulk import that does not read the
// destinations can overwrite one series' episodes with files from another of
// the same name, and nothing afterwards can show it: an overwritten path
// keeps the item's id and its date_created.
func TestPlanCheck(t *testing.T) {
	t.Parallel()

	old := &fakeSeries{
		id: "ex94", name: "Example High", year: 1994,
		path: "/media/shows/Example High (1994)",
		episodes: []ep{
			{season: 3, number: 8, name: "Episode 60", path: "/media/shows/Example High (1994)/Season 03/Example High - 03x08 - Episode 60.mkv", minutes: 45},
		},
	}
	other := &fakeSeries{
		id: "sev", name: "Severance", year: 2022, path: "/media/shows/Severance",
		episodes: []ep{{season: 1, number: 1, name: "One", path: "/media/shows/Severance/S01E01.mkv"}},
	}
	cs := session(t, tvServer(t, old, other), Options{})

	out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{
		// straight onto an existing file, from a different show entirely
		{
			"path": "/media/shows/Example High (1994)/Season 03/Example High - 03x08 - Episode 60.mkv",
			"size": 7_000_000_000, "series": "Example High 2022", "season": 3, "episode": 8,
		},
		// a free path inside a series folder
		{"path": "/media/shows/Example High (1994)/Season 03/Example High - 03x09 - Episode 61.mkv", "series": "Example High"},
		// two entries that would land on one file
		{"path": "/media/shows/Severance/S01E07.mkv", "series": "Severance", "season": 1, "episode": 7},
		{"path": "/media/shows/Severance/S01E07.mkv", "series": "Severance", "season": 1, "episode": 7},
		// nowhere the server knows
		{"path": "/staging/incoming/Some Show S01E01.mkv"},
	}})

	rows := objects(t, out["entries"], "entries")
	if len(rows) != 5 {
		t.Fatalf("entries = %v", rows)
	}

	// the destination that already holds a file says so, with what is there
	overwrite := rows[0]
	if !boolean(t, overwrite["exists"], "exists") {
		t.Fatalf("an occupied destination read as free: %v", overwrite)
	}
	current := object(t, overwrite["current"], "current")
	if text(current["item_id"]) != "ex94-3-8" || number(t, current["runtime_s"], "runtime_s") != 45*60 {
		t.Errorf("current = %v", current)
	}
	// the incoming file is bigger, and the ratio says so rather than leaving
	// the caller to divide
	if ratio, ok := current["size_ratio"].(float64); !ok || ratio <= 1 {
		t.Errorf("size_ratio = %v", current["size_ratio"])
	}
	if number(t, out["existing"], "existing") != 1 {
		t.Errorf("existing = %v, want 1", out["existing"])
	}

	// and the folder it would join is the 1994 series, not the 2022 one the
	// caller named: that gap is the whole problem
	join := object(t, overwrite["would_join"], "would_join")
	if text(join["series_id"]) != "ex94" || number(t, join["series_year"], "series_year") != 1994 {
		t.Errorf("would_join = %v", join)
	}
	if score := decimal(t, join["claim_similarity"], "claim_similarity"); score >= seriesConfident {
		t.Errorf("claiming the 2022 series scored %v against the 1994 folder", score)
	}

	// a free path inside a known series: no file, and the series is named
	free := rows[1]
	if boolean(t, free["exists"], "exists") || free["current"] != nil {
		t.Errorf("a free path = %v", free)
	}
	if text(object(t, free["would_join"], "would_join")["series_id"]) != "ex94" {
		t.Errorf("a free path did not name its series: %v", free)
	}

	// two entries onto one path name each other
	for _, i := range []int{2, 3} {
		if dups := texts(rows[i]["duplicate_of"]); len(dups) != 1 || !strings.Contains(dups[0], "S01E07") {
			t.Errorf("entry %d does not name the entry it collides with: %v", i, rows[i])
		}
	}
	if number(t, out["duplicates"], "duplicates") != 2 {
		t.Errorf("duplicates = %v, want 2", out["duplicates"])
	}

	// a path under no series folder says so rather than reading as free
	outside := rows[4]
	if outside["would_join"] != nil || !strings.Contains(text(outside["note"]), "no series folder") {
		t.Errorf("a path outside the library = %v", outside)
	}
	if number(t, out["unplaced"], "unplaced") != 1 {
		t.Errorf("unplaced = %v, want 1", out["unplaced"])
	}

	// and the batch is bounded
	big := make([]map[string]any, 501)
	for i := range big {
		big[i] = map[string]any{"path": "/media/shows/Severance/x.mkv"}
	}
	if msg := mustRefuse(t, cs, "plan_check", map[string]any{"entries": big}); !strings.Contains(msg, "500") {
		t.Errorf("an oversized batch: %s", msg)
	}
	if msg := mustRefuse(t, cs, "plan_check", map[string]any{"entries": []map[string]any{}}); !strings.Contains(msg, "at least one") {
		t.Errorf("an empty batch: %s", msg)
	}
}
