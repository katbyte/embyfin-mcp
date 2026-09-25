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
		// a 16:9 frame the file says is 16:9, and SDR
		if decimal(t, facts["aspect"], "aspect") != 1.78 || str(facts["aspect_from"]) != "stated" || str(facts["hdr"]) != "sdr" {
			t.Errorf("%s: aspect %v from %v, hdr %v", side, facts["aspect"], facts["aspect_from"], facts["hdr"])
		}
	}
	if margin := decimal(t, object(t, out["policy_used"], "policy_used")["upgrade_margin"], "upgrade_margin"); margin != 1.6 {
		t.Errorf("policy_used.upgrade_margin = %v, want the 1.6 default", margin)
	}
	// the clean copy's video rate, read off the server, is what the numbers
	// below are made against
	rate := num(t, sides["b"]["bitrate"], "bitrate")
	if rate <= 0 || str(sides["b"]["bitrate_from"]) != "the video stream, as the library read it" {
		t.Fatalf("the clean copy's bitrate = %v from %v", sides["b"]["bitrate"], sides["b"]["bitrate_from"])
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
	if msg := callErr(t, "quality_compare", map[string]any{
		"a": map[string]any{"item_id": rip}, "b": map[string]any{"item_id": clean},
		"policy": map[string]any{"upgrade_margin": 0.5},
	}); !strings.Contains(msg, "below 1") {
		t.Errorf("a margin below 1: %s", msg)
	}

	// a series holds episodes rather than a file, and is refused as one
	series := findItem(t, "Shows", "Series", "Severance")
	if msg := callErr(t, "quality_compare", map[string]any{
		"a": map[string]any{"item_id": series},
		"b": map[string]any{"item_id": clean},
	}); !strings.Contains(msg, `copy a: "Severance" is a series, which has no frame of its own`) {
		t.Errorf("a series as a copy said: %s", msg)
	}
	// and a side with neither an item nor a frame is no copy at all
	if msg := callErr(t, "quality_compare", map[string]any{
		"a": map[string]any{"item_id": clean},
		"b": map[string]any{"video_codec": "h264", "bitrate": 1000000},
	}); !strings.Contains(msg, "copy b: each copy needs either an item_id or a frame size") {
		t.Errorf("a copy with no frame said: %s", msg)
	}
}

// Two copies of one class are told apart by bitrate, the codec taken out of
// it, and called better only past the margin. The caller's own margin and
// codec prices are the ones used, and come back with the answer.
func TestQualityComparePolicy(t *testing.T) {
	clean := findItem(t, "Movies", "Movie", "Arrival")
	rate := num(t, object(t, call(t, "quality_compare", map[string]any{
		"a": map[string]any{"item_id": clean}, "b": map[string]any{"item_id": clean},
	})["a"], "a")["bitrate"], "bitrate")
	copyAt := func(codec string, bitrate int) map[string]any {
		return map[string]any{"width": 1280, "height": 720, "video_codec": codec, "bitrate": bitrate}
	}
	compare := func(b map[string]any, policy map[string]any) map[string]any {
		args := map[string]any{"a": map[string]any{"item_id": clean}, "b": b}
		if policy != nil {
			args["policy"] = policy
		}

		return call(t, "quality_compare", args)
	}
	verdict := func(out map[string]any) string {
		return str(out["verdict"]) + " by " + str(out["decided_by"])
	}

	// a fifth more of the same codec: the same copy under the 1.6 default,
	// and better once any difference counts
	fifth := copyAt("h264", rate*6/5)
	out := compare(fifth, nil)
	if verdict(out) != "comparable by nothing" || decimal(t, out["margin"], "margin") != 1.2 || str(out["bitrate_basis"]) != "video" || decimal(t, out["bitrate_ratio"], "bitrate_ratio") != 0.83 {
		t.Errorf("a fifth more = %s, margin %v, basis %v, ratio %v (%v)", verdict(out), out["margin"], out["bitrate_basis"], out["bitrate_ratio"], out["reasons"])
	}
	out = compare(fifth, map[string]any{"upgrade_margin": 1})
	if verdict(out) != "b_better by bitrate" || decimal(t, out["margin"], "margin") != 1.2 {
		t.Errorf("a fifth more with a margin of 1 = %s, margin %v", verdict(out), out["margin"])
	}
	if used := object(t, out["policy_used"], "policy_used"); decimal(t, used["upgrade_margin"], "upgrade_margin") != 1 {
		t.Errorf("policy_used = %v, want the margin of 1 given", used)
	}
	// twice the rate is better under the default, either way round
	if out := compare(copyAt("h264", rate*2), nil); verdict(out) != "b_better by bitrate" || decimal(t, out["margin"], "margin") != 2 {
		t.Errorf("twice the rate = %s, margin %v", verdict(out), out["margin"])
	}
	if out := compare(copyAt("h264", rate/2), nil); verdict(out) != "a_better by bitrate" || decimal(t, out["margin"], "margin") != 2 {
		t.Errorf("half the rate = %s, margin %v", verdict(out), out["margin"])
	}

	// the same rate in HEVC is worth 1.7 of it in h264 by default, which
	// clears the margin; priced at 1 by the caller, it is the same copy
	hevc := copyAt("hevc", rate)
	out = compare(hevc, nil)
	if effective := num(t, object(t, out["b"], "b")["effective_bitrate"], "effective_bitrate"); verdict(out) != "b_better by bitrate" || effective < rate*17/10-1 || effective > rate*17/10+1 {
		t.Errorf("HEVC at the same rate = %s, b %v", verdict(out), out["b"])
	}
	out = compare(hevc, map[string]any{"codec_efficiency": map[string]any{"HEVC": 1}})
	if verdict(out) != "comparable by nothing" {
		t.Errorf("HEVC priced as h264 = %s (%v)", verdict(out), out["reasons"])
	}
	prices := object(t, object(t, out["policy_used"], "policy_used")["codec_efficiency"], "codec_efficiency")
	if decimal(t, prices["hevc"], "hevc") != 1 || decimal(t, prices["av1"], "av1") != 2.2 || decimal(t, prices["h264"], "h264") != 1 {
		t.Errorf("codec_efficiency used = %v, want hevc at the 1 given and the rest their defaults", prices)
	}
}

// What the numbers cannot settle is said rather than guessed: no rate on a
// side, or rates that measure different things, is unknown; and a frame of
// another shape, another dynamic range, a big frame starved of bits, or
// whole files compared with their sound, is a caveat on the answer.
func TestQualityCompareCaveats(t *testing.T) {
	clean := findItem(t, "Movies", "Movie", "Arrival")
	caveats := func(out map[string]any) string {
		raw, _ := out["caveats"].([]any)
		var all []string
		for _, c := range raw {
			all = append(all, str(c))
		}

		return strings.Join(all, " | ")
	}
	given := func(extra map[string]any) map[string]any {
		out := map[string]any{"width": 1280, "height": 720, "video_codec": "h264"}
		for k, v := range extra {
			out[k] = v
		}

		return out
	}
	compare := func(a, b map[string]any) map[string]any {
		return call(t, "quality_compare", map[string]any{"a": a, "b": b})
	}
	item := map[string]any{"item_id": clean}
	rate := num(t, object(t, compare(item, item)["a"], "a")["bitrate"], "bitrate")

	// a frame and nothing to measure it by
	out := compare(item, given(nil))
	if str(out["verdict"]) != "unknown" || out["decided_by"] != nil || out["margin"] != nil ||
		!strings.Contains(strings.Join(strs(t, out["reasons"], "reasons"), " "), "one copy has no bitrate and no size and runtime to work one out from") {
		t.Errorf("a copy with no rate = %v", out)
	}
	// one side's video against the other's whole file, and no audio rates
	// to take out of it
	out = compare(given(map[string]any{"bitrate": 1000000}), given(map[string]any{"size": 250000, "runtime_s": 1}))
	if str(out["verdict"]) != "unknown" || out["bitrate_basis"] != nil || !strings.Contains(caveats(out), "the two measure different things, so bitrate cannot settle this") {
		t.Errorf("video against a whole file = %v", out)
	}
	// with every audio rate given, the whole file less its sound is its
	// video, and the two are on one footing
	out = compare(given(map[string]any{"bitrate": 1000000}), given(map[string]any{"size": 250000, "runtime_s": 1, "audio": []map[string]any{{"codec": "aac", "channels": 2, "bitrate": 128000}}}))
	if b := object(t, out["b"], "b"); str(out["bitrate_basis"]) != "video" || num(t, b["bitrate"], "bitrate") != 2000000-128000 || str(b["bitrate_from"]) != "the whole file less its audio tracks" || str(out["verdict"]) == "unknown" {
		t.Errorf("a whole file with its audio rates = %v, b %v", out["verdict"], b)
	}
	// both whole files: compared so, and the sound one of them does not
	// describe is said to be in the gap
	out = compare(given(map[string]any{"size": 250000, "runtime_s": 1}), given(map[string]any{"size": 500000, "runtime_s": 1}))
	if str(out["verdict"]) != "b_better" || str(out["decided_by"]) != "bitrate" || str(out["bitrate_basis"]) != "whole_file" || !strings.Contains(caveats(out), "one copy's audio was not given") {
		t.Errorf("two whole files = %v", out)
	}
	out = compare(
		given(map[string]any{"size": 250000, "runtime_s": 1, "audio": []map[string]any{{"language": "eng", "codec": "aac", "channels": 2}}}),
		given(map[string]any{"size": 500000, "runtime_s": 1, "audio": []map[string]any{{"language": "eng", "codec": "ac3", "channels": 6}, {"language": "jpn", "codec": "aac", "channels": 2}}}),
	)
	if !strings.Contains(caveats(out), "the copies carry different audio (1 tracks, best aac 2ch, against 2 tracks, best ac3 6ch)") {
		t.Errorf("two whole files with different sound = %v", caveats(out))
	}
	// the Japanese track only b has is reported beside the verdict, never
	// in it
	if audio := object(t, out["audio"], "audio"); !strings.Contains(str(audio["note"]), "only the b copy carries jpn") {
		t.Errorf("audio = %v", audio)
	}

	// another dynamic range: a claim about the encode, said as one
	out = compare(item, given(map[string]any{"bitrate": rate, "hdr": "HDR10"}))
	if !strings.Contains(caveats(out), "the copies claim different HDR formats (sdr against hdr10)") {
		t.Errorf("SDR against HDR10 = %v", caveats(out))
	}
	// a 4:3 frame is not a smaller 16:9 one: the same class by its height,
	// and a different shape
	out = compare(item, map[string]any{"width": 960, "height": 720, "aspect_ratio": "4:3", "video_codec": "h264", "bitrate": rate})
	if b := object(t, out["b"], "b"); num(t, b["resolution_class"], "resolution_class") != 720 || decimal(t, b["aspect"], "aspect") != 1.33 ||
		!strings.Contains(caveats(out), "the frames are different shapes (1.78 against 1.33)") {
		t.Errorf("4:3 against 16:9 = %v, b %v", caveats(out), b)
	}
	// a bigger frame on under half the bits a pixel still wins on class, and
	// the answer says what that hides
	out = compare(item, map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": rate / 5})
	if str(out["verdict"]) != "b_better" || str(out["decided_by"]) != "resolution" || !strings.Contains(caveats(out), "the larger frame is the more thinly encoded one") {
		t.Errorf("a starved 1080p = %v by %v: %v", out["verdict"], out["decided_by"], caveats(out))
	}
	// and two copies alike in every way carry no caveat at all
	if out := compare(item, map[string]any{"width": 1280, "height": 720, "aspect_ratio": "16:9", "video_codec": "h264", "frame_rate": 5, "hdr": "sdr", "bitrate": rate}); out["caveats"] != nil {
		t.Errorf("a copy alike in every way = %v", out["caveats"])
	}
}

// Every fact a side can be given by hand is taken, and comes back as the
// side's facts: the same 720p picture as the clean copy, described in full,
// is the same class, so bitrate decides it, and near enough the clean
// copy's rate it is the same copy.
func TestQualityCompareGivenInFull(t *testing.T) {
	clean := findItem(t, "Movies", "Movie", "Arrival")
	described := call(t, "quality_compare", map[string]any{
		"a": map[string]any{"item_id": clean},
		"b": map[string]any{
			"width": 1280, "height": 720, "aspect_ratio": "16:9", "video_codec": "h264", "frame_rate": 5, "hdr": "SDR",
			"bitrate": 1000000, "size": 125000, "runtime_s": 1, "container": "mkv",
			"audio": []map[string]any{{"language": "eng", "codec": "aac", "channels": 1, "bitrate": 64000}},
		},
	})
	if v, by := str(described["verdict"]), str(described["decided_by"]); v == "unknown" || (by != "bitrate" && by != "nothing") {
		t.Errorf("a copy described in full = %s decided by %q (%v)", v, by, described["reasons"])
	}
	b := object(t, described["b"], "b")
	for field, want := range map[string]any{
		"source": "given", "resolution_class": 720.0, "aspect": 1.78, "aspect_from": "stated", "video_codec": "h264", "frame_rate": 5.0,
		"hdr": "sdr", "size": 125000.0, "runtime_s": 1.0, "bitrate": 1000000.0, "bitrate_from": "the video stream, as given",
		"audio_tracks": 1.0, "audio_codec": "aac", "audio_channels": 1.0, "audio_bitrate": 64000.0,
	} {
		if b[field] != want {
			t.Errorf("b's %s = %v, want %v as given", field, b[field], want)
		}
	}
	if langs := strs(t, b["audio_languages"], "audio_languages"); len(langs) != 1 || langs[0] != "eng" {
		t.Errorf("b's audio_languages = %v", langs)
	}
}
