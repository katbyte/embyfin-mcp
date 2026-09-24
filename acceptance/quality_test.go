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

	// the audio bitrate arrives from the server too: the fixture's mono aac
	// against a 448k ac3 of the same channel count is the case a codec name
	// gets wrong, and it can only be called without the rate on both sides
	sound := call(t, "quality_compare", map[string]any{
		"a": map[string]any{"item_id": clean},
		"b": map[string]any{
			"width": 1280, "height": 720, "video_codec": "h264", "bitrate": 1000000,
			"audio": []map[string]any{{"language": "eng", "codec": "ac3", "channels": 1, "bitrate": 448000}},
		},
	})
	a := object(t, sound["a"], "a")
	if num(t, a["audio_bitrate"], "audio_bitrate") <= 0 {
		t.Errorf("the server's audio bitrate did not arrive: %v", a)
	}
	if note := str(object(t, sound["audio"], "audio")["parity"]); !strings.Contains(note, "not a quality claim") {
		t.Errorf("a low-rate aac against a 448k ac3 said nothing: %v", sound["audio"])
	}

	// the caller's own policy is the one applied, and echoed: a margin of 1
	// calls any difference better; one below 1 is refused
	policy := call(t, "quality_compare", map[string]any{
		"a":      map[string]any{"item_id": rip},
		"b":      map[string]any{"item_id": clean},
		"policy": map[string]any{"upgrade_margin": 1, "codec_efficiency": map[string]any{"h264": 1}},
	})
	if used := object(t, policy["policy_used"], "policy_used"); decimal(t, used["upgrade_margin"], "upgrade_margin") != 1 || str(policy["verdict"]) != "b_better" {
		t.Errorf("with a margin of 1: verdict %v, policy_used %v", policy["verdict"], used)
	}
	if msg := callErr(t, "quality_compare", map[string]any{
		"a": map[string]any{"item_id": rip}, "b": map[string]any{"item_id": clean},
		"policy": map[string]any{"upgrade_margin": 0.5},
	}); !strings.Contains(msg, "below 1") {
		t.Errorf("a margin below 1: %s", msg)
	}
	// every fact a side can be given by hand is taken: the same 720p picture
	// as the clean copy, described in full, is no better and no worse by
	// resolution
	described := call(t, "quality_compare", map[string]any{
		"a": map[string]any{"item_id": clean},
		"b": map[string]any{
			"width": 1280, "height": 720, "aspect_ratio": "16:9", "video_codec": "h264", "frame_rate": 5, "hdr": "SDR",
			"bitrate": 1000000, "size": 125000, "runtime_s": 1, "container": "mkv",
		},
	})
	if facts := object(t, described["b"], "b"); num(t, facts["resolution_class"], "resolution_class") != 720 || str(described["decided_by"]) == "resolution" {
		t.Errorf("a copy described in full = decided by %v, b %v", described["decided_by"], facts)
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
