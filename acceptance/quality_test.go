//go:build integration

package acceptance

import (
	"fmt"
	"slices"
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

// The messy Blade Runner is held twice: a 1080p file at 24 frames a second,
// and what an AI upscaler makes of it - 2160p at 60, HEVC 10-bit, tagged as
// HDR10. The rows say what each is, read off the files, and quality_compare
// between the two real items says the bigger one is bigger and what it
// cannot say for it: that sixty frames on a film were interpolated, and that
// the HDR is a claim the source does not back.
func TestAnAIUpscaleBesideItsSource(t *testing.T) {
	id := itemsTitled(t, "Messy Movies", "Movie", "Blade Runner")[0]
	versions := map[int]map[string]any{}
	for _, v := range rows(t, call(t, "item_get", map[string]any{"id": id})["versions"], "versions") {
		versions[num(t, v["height"], "height")] = v
	}
	upscale, source := versions[2160], versions[1080]
	if upscale == nil || source == nil {
		t.Fatalf("the messy Blade Runner's versions = %v, want a 2160p one and a 1080p one", versions)
	}
	for _, c := range []struct {
		name  string
		v     map[string]any
		codec string
		fps   float64
		hdr   string
	}{{"the upscale", upscale, "hevc", 60, "hdr10"}, {"its source", source, "h264", 24, "sdr"}} {
		if str(c.v["video_codec"]) != c.codec || decimal(t, c.v["frame_rate"], "frame_rate") != c.fps || str(c.v["hdr"]) != c.hdr {
			t.Errorf("%s = %v at %v fps, %v; want %s at %v fps, %s", c.name, c.v["video_codec"], c.v["frame_rate"], c.v["hdr"], c.codec, c.fps, c.hdr)
		}
	}

	out := call(t, "quality_compare", map[string]any{"a": map[string]any{"item_id": str(upscale["id"])}, "b": map[string]any{"item_id": str(source["id"])}})
	a, b := object(t, out["a"], "a"), object(t, out["b"], "b")
	if num(t, a["resolution_class"], "resolution_class") != 2160 || num(t, b["resolution_class"], "resolution_class") != 1080 || decimal(t, a["frame_rate"], "frame_rate") != 60 || str(a["hdr"]) != "hdr10" {
		t.Fatalf("the sides read = %v against %v: want the upscale's facts against the source's", a, b)
	}
	if str(out["verdict"]) != "a_better" || str(out["decided_by"]) != "resolution" {
		t.Errorf("verdict = %v by %v, want the upscale bigger by its frame", out["verdict"], out["decided_by"])
	}
	caveats := strings.Join(strs(t, out["caveats"], "caveats"), " | ")
	for _, want := range []string{
		"one copy runs at 60 fps and the other at 24",
		"so the 60 fps copy was interpolated from a slower master",
		"the copies claim different HDR formats (hdr10 against sdr)",
	} {
		if !strings.Contains(caveats, want) {
			t.Errorf("caveats = %s, want %q", caveats, want)
		}
	}
	// and the one film in two files is one film: no caveat that it is two
	if strings.Contains(caveats, "may not be the same film") {
		t.Errorf("caveats = %s: the two files are one film", caveats)
	}
}

// The messy Severance's second episode is a DVD rip kept anamorphic: 720x480
// with the 16:9 it is shown at stated in its stream. Its row says so, and
// shows at 853 wide; quality_compare reads its shape as stated, so against
// the 16:9 episode beside it it is the same shape (by its frame, 720x480 is
// 3:2 and it was not), and against a 4:3 picture of the same frame size it
// is not.
func TestAnAnamorphicDVDRip(t *testing.T) {
	messy := findItem(t, "Messy Shows", "Series", "Severance")
	byEpisode := map[int]map[string]any{}
	for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": messy, "season": 1})["episodes"], "episodes") {
		byEpisode[num(t, e["episode"], "episode")] = e
	}
	rip := byEpisode[2]
	if rip == nil || num(t, rip["width"], "width") != 720 || num(t, rip["height"], "height") != 480 || str(rip["aspect_ratio"]) != "16:9" || num(t, rip["display_width"], "display_width") != 853 {
		t.Fatalf("S01E02 = %v, want 720x480 stated 16:9 and shown 853 wide", rip)
	}
	// a frame of its own shape shows at its own width
	if first := byEpisode[1]; first == nil || first["display_width"] != nil {
		t.Errorf("S01E01 = %v, want no display width for a 16:9 frame stated 16:9", first)
	}

	out := call(t, "quality_compare", map[string]any{"a": map[string]any{"item_id": str(rip["id"])}, "b": map[string]any{"item_id": str(byEpisode[1]["id"])}})
	a := object(t, out["a"], "a")
	if decimal(t, a["aspect"], "aspect") != 1.78 || str(a["aspect_from"]) != "stated" || num(t, a["resolution_class"], "resolution_class") != 480 {
		t.Errorf("the rip reads as %v from %v, class %v; want 1.78 as stated and 480", a["aspect"], a["aspect_from"], a["resolution_class"])
	}
	if caveats := strings.Join(texts(out["caveats"]), " | "); strings.Contains(caveats, "different shapes") {
		t.Errorf("the rip against a 16:9 episode = %s: the same shape once the stated ratio is read", caveats)
	}
	if str(out["verdict"]) != "a_better" || str(out["decided_by"]) != "resolution" {
		t.Errorf("480 lines against 360 = %v by %v", out["verdict"], out["decided_by"])
	}

	fourThree := call(t, "quality_compare", map[string]any{
		"a": map[string]any{"item_id": str(rip["id"])},
		"b": map[string]any{"width": 720, "height": 480, "aspect_ratio": "4:3", "video_codec": "h264", "bitrate": num(t, a["bitrate"], "bitrate")},
	})
	if caveats := strings.Join(texts(fourThree["caveats"]), " | "); !strings.Contains(caveats, "the frames are different shapes (1.78 against 1.33)") {
		t.Errorf("the rip against a 4:3 DVD of the same frame = %s, want the shapes told apart", caveats)
	}
	if str(fourThree["decided_by"]) == "resolution" {
		t.Errorf("one frame size against itself decided by resolution: %v", fourThree["reasons"])
	}
}

// texts reads a list of strings that may be absent.
func texts(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		out = append(out, str(e))
	}

	return out
}

// A picture's class is read by its width as much as its height: a scope
// encode is 1920x800 or 1280x536, a 4:3 one 960x720, and each is a 1080p or
// a 720p picture however few lines it has; a scope DVD cropped to 720x304 is
// 405 lines' worth, and below the audit's 720. The four frames, made outside
// the libraries, are staged as the messy Severance's second season, each
// with an nfo naming it the episode it stands in for, and taken away again.
func TestResolutionClassesByWidth(t *testing.T) {
	season := "messy-shows/Severance/Season 02/"
	frames := []struct {
		size, title string
		class       int
	}{{"1920x800", "Hello, Ms. Cobel", 1080}, {"1280x536", "Goodbye, Mrs. Selvig", 720}, {"960x720", "Who Is Alive?", 720}, {"720x304", "Woe's Hollow", 405}}
	files := map[string][]byte{}
	for i, f := range frames {
		base := fmt.Sprintf("%sSeverance S02E%02d", season, i+1)
		files[base+".mp4"] = fixture(t, "frames-src/"+f.size+".mp4")
		files[base+".nfo"] = episodeNfo(f.title, 2, i+1)
	}
	stage(t, plus(0, 0, len(frames)), files, "messy-shows/Severance/Season 02")

	messy := findItem(t, "Messy Shows", "Series", "Severance")
	staged := map[int]map[string]any{}
	for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": messy, "season": 2})["episodes"], "episodes") {
		staged[num(t, e["episode"], "episode")] = e
	}
	var flagged []string
	for _, f := range rows(t, call(t, "audit_quality", map[string]any{"library": "Messy Shows"})["findings"], "findings") {
		if strings.Contains(str(f["path"]), "/Season 02/") {
			flagged = append(flagged, str(f["name"])+": "+str(f["detail"]))
		}
	}
	if want := []string{"Severance S02E04 Woe's Hollow: h264 720x304: 405p, below 720p"}; !slices.Equal(flagged, want) {
		t.Errorf("audit_quality over the staged frames = %v, want %v", flagged, want)
	}
	for i, f := range frames {
		e := staged[i+1]
		if e == nil || fmt.Sprintf("%dx%d", num(t, e["width"], "width"), num(t, e["height"], "height")) != f.size {
			t.Errorf("S02E%02d = %v, want %s", i+1, e, f.size)
			continue
		}
		// read back from the item, the class is the width's where it is more
		b := object(t, call(t, "quality_compare", map[string]any{"a": map[string]any{"item_id": str(e["id"])}, "b": map[string]any{"item_id": str(e["id"])}})["a"], "a")
		if num(t, b["resolution_class"], "resolution_class") != f.class {
			t.Errorf("%s reads as class %v, want %d", f.size, b["resolution_class"], f.class)
		}
	}

	// a scope 720p against a 4:3 one: one class, and the shapes said to differ
	out := call(t, "quality_compare", map[string]any{"a": map[string]any{"item_id": str(staged[2]["id"])}, "b": map[string]any{"item_id": str(staged[3]["id"])}})
	if caveats := strings.Join(texts(out["caveats"]), " | "); !strings.Contains(caveats, "the frames are different shapes") || str(out["decided_by"]) == "resolution" {
		t.Errorf("1280x536 against 960x720 = %v by %v, caveats %s: want one class and the shapes told apart", out["verdict"], out["decided_by"], caveats)
	}
	// and the anamorphic DVD rip in season one against the 4:3 picture: its
	// stated 16:9 against 4:3, and 480 lines against 720
	var rip string
	for _, e := range rows(t, call(t, "library_episodes", map[string]any{"series_id": messy, "season": 1})["episodes"], "episodes") {
		if num(t, e["episode"], "episode") == 2 {
			rip = str(e["id"])
		}
	}
	out = call(t, "quality_compare", map[string]any{"a": map[string]any{"item_id": rip}, "b": map[string]any{"item_id": str(staged[3]["id"])}})
	if caveats := strings.Join(texts(out["caveats"]), " | "); !strings.Contains(caveats, "the frames are different shapes (1.78 against 1.33)") || str(out["verdict"]) != "b_better" {
		t.Errorf("the anamorphic rip against the 4:3 720p = %v, caveats %s", out["verdict"], caveats)
	}
}
