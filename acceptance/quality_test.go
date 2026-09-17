//go:build integration

package acceptance

import (
	"strings"
	"testing"
)

// quality_compare against two copies of one film read off the server rather
// than given: the clean 720p Arrival and the 360p rip in the messy library.
// The facts have to arrive through the same fields from both backends - frame
// rate and the audio track included - or the comparison is comparing guesses.
func TestQualityCompare(t *testing.T) {
	clean := findItem(t, "Movies", "Movie", "Arrival")
	rip := findItem(t, "Messy Movies", "Movie", "Arrival")

	out := call(t, "quality_compare", map[string]any{
		"a": map[string]any{"item_id": rip},
		"b": map[string]any{"item_id": clean},
	})
	if str(out["verdict"]) != "b_better" || str(out["decided_by"]) != "resolution" {
		t.Errorf("the rip against the clean copy: verdict %v decided by %v (%v)", out["verdict"], out["decided_by"], out["reasons"])
	}
	if margin := decimal(t, out["margin"], "margin"); margin != 2 {
		t.Errorf("margin = %v, want 2: 720 lines against 360", margin)
	}

	sides := map[string]map[string]any{"a": object(t, out["a"], "a"), "b": object(t, out["b"], "b")}
	for side, class := range map[string]int{"a": 360, "b": 720} {
		facts := sides[side]
		if got := num(t, facts["resolution_class"], "resolution_class"); got != class {
			t.Errorf("%s: resolution_class = %d, want %d: %v", side, got, class, facts)
		}
		// the fixtures are encoded at 5 frames a second, so a 5 here is the
		// server's own reading reaching the tool, not a default
		if fps := decimal(t, facts["frame_rate"], "frame_rate"); fps < 4.9 || fps > 5.1 {
			t.Errorf("%s: frame_rate = %v, want the fixture's 5", side, fps)
		}
		if str(facts["audio_codec"]) != "aac" || num(t, facts["audio_channels"], "audio_channels") != 1 {
			t.Errorf("%s: best audio track = %v %v, want the fixture's mono aac", side, facts["audio_codec"], facts["audio_channels"])
		}
		if !strings.Contains(str(facts["source"]), "item") {
			t.Errorf("%s: source = %v, want the item it was read from", side, facts["source"])
		}
	}
	if margin := decimal(t, object(t, out["policy_used"], "policy_used")["upgrade_margin"], "upgrade_margin"); margin != 1.6 {
		t.Errorf("policy_used.upgrade_margin = %v, want the 1.6 default", margin)
	}

	// a side given as numbers against one read off the server: a 60fps copy
	// is bigger on every number and the answer still says what it is
	given := call(t, "quality_compare", map[string]any{
		"a": map[string]any{"item_id": clean},
		"b": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 5000000, "frame_rate": 60},
	})
	if str(given["verdict"]) != "b_better" {
		t.Errorf("720p against 1080p: %v", given["verdict"])
	}
	if !strings.Contains(strings.Join(strs(t, given["caveats"], "caveats"), " "), "interpolated") {
		t.Errorf("60fps against the fixture's 5 went unremarked: %v", given["caveats"])
	}

	// a series holds episodes rather than a file, and is refused as one
	series := findItem(t, "Shows", "Series", "Severance")
	if msg := callErr(t, "quality_compare", map[string]any{
		"a": map[string]any{"item_id": series},
		"b": map[string]any{"item_id": clean},
	}); !strings.Contains(msg, "compare") {
		t.Errorf("a series as a copy said: %s", msg)
	}
}
