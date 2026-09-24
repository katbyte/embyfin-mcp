package tools

import (
	"net/http"
	"strings"
	"testing"
)

// The class a frame belongs on. Both of these were got wrong in the field, in
// opposite directions, by taking one side of the frame as the answer.
func TestResolutionClass(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		width, height int
		want          int
	}{
		{"plain 1080p", 1920, 1080, 1080},
		{"plain 2160p", 3840, 2160, 2160},
		{"cinematic 4K is not 1440p", 3840, 1920, 2160},
		{"4:3 720p is not 480p", 960, 720, 720},
		{"4:3 Columbo", 976, 720, 720},
		{"DVD", 720, 480, 480},
		{"PAL DVD", 720, 576, 576},

		// bars sit on one axis and leave the other alone, which is the whole
		// reason the class is the larger of the two readings
		{"2.35:1 letterboxed in 1080p", 1920, 816, 1080},
		{"the same film cropped to its picture", 1920, 816, 1080},
		{"4:3 pillarboxed into a 1080p frame", 1920, 1080, 1080},
		{"the same episode cropped to its picture", 1436, 1080, 1080},

		{"nothing known", 0, 0, 0},
	} {
		if got := resolutionClass(tc.width, tc.height); got != tc.want {
			t.Errorf("%s (%dx%d) classed %d, want %d", tc.name, tc.width, tc.height, got, tc.want)
		}
	}
}

// The pairs below are real: two copies of one episode, and which one an
// operator who looked at both said was better. They are the cases a
// comparison has to get right, including the one that caught a caller out -
// a lower bitrate in a better codec.
func TestQualityCompare(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})

	for _, tc := range []struct {
		name            string
		a, b            map[string]any
		verdict, decide string
		margin          float64
	}{
		{
			// the one that caught a caller out: fewer bits, better codec
			name:    "hevc at 12.2 beats h264 at 15.8 in the same class",
			a:       map[string]any{"width": 3840, "height": 2160, "video_codec": "hevc", "bitrate": 12200000},
			b:       map[string]any{"width": 3840, "height": 2160, "video_codec": "h264", "bitrate": 15800000},
			verdict: "comparable", decide: "nothing", margin: 1.31,
		},
		{
			name:    "a starved hevc loses to a well-fed h264",
			a:       map[string]any{"width": 1920, "height": 1080, "video_codec": "hevc", "bitrate": 1500000},
			b:       map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 8600000},
			verdict: "b_better", decide: "bitrate", margin: 3.37,
		},
		{
			name:    "4:3 720p, a third more bitrate, still the same copy",
			a:       map[string]any{"width": 976, "height": 720, "video_codec": "h264", "bitrate": 4500000},
			b:       map[string]any{"width": 976, "height": 720, "video_codec": "h264", "bitrate": 6100000},
			verdict: "comparable", decide: "nothing", margin: 1.36,
		},
		{
			name:    "a class apart is a class apart",
			a:       map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 5500000},
			b:       map[string]any{"width": 3840, "height": 2160, "video_codec": "hevc", "bitrate": 9000000},
			verdict: "b_better", decide: "resolution", margin: 2,
		},
		{
			name:    "a DVD rip against a 1080p WEB-DL",
			a:       map[string]any{"width": 720, "height": 400, "video_codec": "h264", "bitrate": 1100000},
			b:       map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 5000000},
			verdict: "b_better", decide: "resolution", margin: 2.67,
		},
		{
			name:    "cinematic 4K against full-frame 4K is not a class gap",
			a:       map[string]any{"width": 3840, "height": 1920, "video_codec": "hevc", "bitrate": 14000000},
			b:       map[string]any{"width": 3840, "height": 2160, "video_codec": "hevc", "bitrate": 15000000},
			verdict: "comparable", decide: "nothing", margin: 1.07,
		},
	} {
		out := mustCall(t, cs, "quality_compare", map[string]any{"a": tc.a, "b": tc.b})
		if got := text(out["verdict"]); got != tc.verdict {
			t.Errorf("%s: verdict %q, want %q (reasons: %v)", tc.name, got, tc.verdict, out["reasons"])
		}
		if got := text(out["decided_by"]); got != tc.decide {
			t.Errorf("%s: decided by %q, want %q", tc.name, got, tc.decide)
		}
		if got := decimal(t, out["margin"], "margin"); got != tc.margin {
			t.Errorf("%s: margin %v, want %v - how much is half the answer", tc.name, got, tc.margin)
		}
		if len(texts(out["reasons"])) == 0 {
			t.Errorf("%s: answered with no working", tc.name)
		}
	}
}

// A verdict nobody can check is worth no more than the guess it replaced: the
// derived numbers, the ratio and every constant used come back with it.
func TestQualityCompareShowsItsWorking(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})

	out := mustCall(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"width": 3840, "height": 2160, "video_codec": "hevc", "bitrate": 12200000},
		"b": map[string]any{"width": 3840, "height": 2160, "video_codec": "h264", "bitrate": 15800000},
	})

	a, ok := out["a"].(map[string]any)
	if !ok {
		t.Fatalf("no a side: %v", out)
	}
	// 12.2 Mbps of hevc is 20.7 Mbps of h264 at the default 1.7
	if got := number(t, a["effective_bitrate"], "effective_bitrate"); got != 20740000 {
		t.Errorf("hevc was not normalised: %v", a)
	}
	if number(t, a["resolution_class"], "resolution_class") != 2160 || a["bits_per_pixel"] == nil {
		t.Errorf("the derived numbers are not on the answer: %v", a)
	}
	if ratio := decimal(t, out["bitrate_ratio"], "bitrate_ratio"); ratio != 1.31 {
		t.Errorf("bitrate_ratio = %v, want 1.31", ratio)
	}
	// the working names the codec normalisation, so a caller can disagree
	// with that step rather than with the verdict
	if !strings.Contains(strings.Join(texts(out["reasons"]), " | "), "hevc") {
		t.Errorf("the working does not mention the codec: %v", out["reasons"])
	}

	policy, ok := out["policy_used"].(map[string]any)
	if !ok {
		t.Fatalf("the constants used are not reported: %v", out)
	}
	if margin := decimal(t, policy["upgrade_margin"], "upgrade_margin"); margin != 1.6 {
		t.Errorf("upgrade_margin = %v, want the 1.6 default", margin)
	}
	if eff, ok := policy["codec_efficiency"].(map[string]any); !ok || eff["hevc"] != 1.7 {
		t.Errorf("codec_efficiency not reported: %v", policy["codec_efficiency"])
	}

	// and the arithmetic is the caller's to change: the same two files, a
	// margin they chose, the opposite verdict
	strict := mustCall(t, cs, "quality_compare", map[string]any{
		"a":      map[string]any{"width": 3840, "height": 2160, "video_codec": "hevc", "bitrate": 12200000},
		"b":      map[string]any{"width": 3840, "height": 2160, "video_codec": "h264", "bitrate": 15800000},
		"policy": map[string]any{"upgrade_margin": 1.2},
	})
	if text(strict["verdict"]) != "a_better" {
		t.Errorf("a margin of 1.2 did not change the verdict: %v", strict["verdict"])
	}

	// a codec the caller prices differently is priced differently
	flat := mustCall(t, cs, "quality_compare", map[string]any{
		"a":      map[string]any{"width": 3840, "height": 2160, "video_codec": "hevc", "bitrate": 12200000},
		"b":      map[string]any{"width": 3840, "height": 2160, "video_codec": "h264", "bitrate": 15800000},
		"policy": map[string]any{"codec_efficiency": map[string]any{"hevc": 1.0}},
	})
	if text(flat["verdict"]) != "comparable" || text(flat["decided_by"]) != "nothing" {
		t.Errorf("hevc priced at 1.0 should make b the better copy by 1.30, under the margin: %v", flat)
	}
}

// Black bars cost frame and hold no picture, so a bigger frame is not always
// more picture. The class formula survives bars on either axis by
// construction; what it cannot see is two copies boxed DIFFERENTLY, and it
// has to say so rather than quietly compare them.
func TestQualityCompareFlagsFramesOfDifferentShapes(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})

	// the same 4:3 episode: one pillarboxed into a 16:9 frame, one cropped to
	// its picture. Same class, and the boxed one must not win on frame size
	out := mustCall(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 6000000},
		"b": map[string]any{"width": 1436, "height": 1080, "video_codec": "h264", "bitrate": 6000000},
	})
	if text(out["verdict"]) != "comparable" {
		t.Errorf("a pillarboxed copy beat the same picture cropped: %v", out)
	}
	caveats := texts(out["caveats"])
	if len(caveats) == 0 || !strings.Contains(strings.Join(caveats, " "), "shapes") {
		t.Errorf("different frame shapes went unremarked: %v", out)
	}

	// and a big frame starved of bitrate is flagged even though the class
	// still decides it
	starved := mustCall(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"width": 3840, "height": 2160, "video_codec": "h264", "bitrate": 3000000},
		"b": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 12000000},
	})
	if text(starved["verdict"]) != "a_better" || text(starved["decided_by"]) != "resolution" {
		t.Errorf("the class should still decide: %v", starved)
	}
	if !strings.Contains(strings.Join(texts(starved["caveats"]), " "), "thinly encoded") {
		t.Errorf("a starved 2160p went unremarked: %v", starved["caveats"])
	}
}

// Audio is a dimension of its own. Replacing a dual-audio copy with an
// English-only one at the same bitrate is an unrecoverable loss, and no
// number of megabits makes it up - so it is reported beside the verdict and
// never folded into it.
func TestQualityCompareKeepsAudioSeparate(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})

	out := mustCall(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 6000000, "audio": []map[string]any{{"language": "jpn", "codec": "aac", "channels": 2}, {"language": "eng", "codec": "aac", "channels": 2}}},
		"b": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 6000000, "audio": []map[string]any{{"language": "eng", "codec": "aac", "channels": 2}}},
	})

	// the video is the same copy, and the verdict says only that
	if text(out["verdict"]) != "comparable" || text(out["decided_by"]) != "nothing" {
		t.Errorf("audio moved the video verdict: %v", out)
	}
	audio, ok := out["audio"].(map[string]any)
	if !ok {
		t.Fatalf("no audio dimension: %v", out)
	}
	if only := texts(audio["only_a"]); len(only) != 1 || only[0] != "jpn" {
		t.Errorf("only_a = %v, want jpn", audio["only_a"])
	}
	if !strings.Contains(text(audio["note"]), "loses") {
		t.Errorf("the note does not say what replacing a with b costs: %v", audio["note"])
	}
}

// A comparison that cannot be made must say so rather than answer
// "comparable", which reads as "either will do" and is how the wrong copy
// gets deleted.
func TestQualityCompareRefusesWhatItCannotAnswer(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})

	noBitrate := mustCall(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264"},
		"b": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 6000000},
	})
	if text(noBitrate["verdict"]) != "unknown" {
		t.Errorf("same class, one bitrate missing: %v", noBitrate)
	}

	// but size and runtime are a bitrate - the whole file's, so it is
	// compared with another whole file's
	derived := mustCall(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "size": 2000000000, "runtime_s": 2400},
		"b": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "size": 300000000, "runtime_s": 2400},
	})
	if text(derived["verdict"]) != "a_better" {
		t.Errorf("a bitrate worked out from size and runtime was not used: %v", derived)
	}
	if !strings.Contains(strings.Join(texts(derived["reasons"]), " "), "worked out") {
		t.Errorf("the working does not say the bitrate was derived: %v", derived["reasons"])
	}

	if msg := mustRefuse(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"video_codec": "h264", "bitrate": 6000000},
		"b": map[string]any{"width": 1920, "height": 1080},
	}); !strings.Contains(msg, "frame size") {
		t.Errorf("a copy with no frame size said: %s", msg)
	}
}

// One side is usually the library's own copy, and it has to be read the same
// way the other tools read it or the two sides are not comparable.
func TestQualityCompareReadsALibraryItem(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})

	out := mustCall(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"item_id": "sev-1-1"},
		"b": map[string]any{"width": 3840, "height": 2160, "video_codec": "hevc", "bitrate": 14000000},
	})

	a, ok := out["a"].(map[string]any)
	if !ok {
		t.Fatalf("no a side: %v", out)
	}
	// the fixture's episodes are 1920x1080 h264 at 4 Mbps
	if number(t, a["width"], "width") != 1920 || a["video_codec"] != "h264" {
		t.Errorf("the item was not read: %v", a)
	}
	if !strings.Contains(text(a["source"]), "sev-1-1") {
		t.Errorf("the answer does not say which item it read: %v", a["source"])
	}
	if text(out["verdict"]) != "b_better" || text(out["decided_by"]) != "resolution" {
		t.Errorf("1080p against 2160p: %v", out)
	}
}

// The one claim a release cannot inflate. Width, codec and an HDR flag can
// all be asserted by an encoder; 60 frames a second on a 24fps master cannot
// be anything but interpolation, because no broadcast or disc master of a
// scripted show ships at 60p.
//
// AI-upscaled 60fps video named as an ordinary release is a real shape, and
// it wins on every raw number against the genuine copy it would replace -
// including "2160p HDR" over a 480i source, which is invention rather than
// upscaling.
func TestQualityCompareFlagsInterpolatedFrameRates(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})

	// the upscale is bigger, better-fed and newer-codec'd on every number,
	// and the answer has to say what it is
	out := mustCall(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"width": 3832, "height": 2160, "video_codec": "hevc", "bitrate": 14000000, "frame_rate": 60},
		"b": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 8000000, "frame_rate": 23.976},
	})
	caveats := strings.Join(texts(out["caveats"]), " | ")
	if !strings.Contains(caveats, "interpolated") {
		t.Errorf("a 60fps copy against a 23.976 one was not called out: %v", out["caveats"])
	}
	if !strings.Contains(caveats, "holds no frames the other does not") {
		t.Errorf("the caveat does not say why the bigger frame is not more picture: %v", out["caveats"])
	}
	// the facts are on both sides for a caller to act on
	a, ok := out["a"].(map[string]any)
	if !ok || decimal(t, a["frame_rate"], "frame_rate") != 60 {
		t.Errorf("the frame rate is not on the answer: %v", out["a"])
	}

	// two copies of the same honest rate say nothing
	same := mustCall(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"width": 3840, "height": 2160, "video_codec": "hevc", "bitrate": 14000000, "frame_rate": 23.976},
		"b": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 8000000, "frame_rate": 23.976},
	})
	if strings.Contains(strings.Join(texts(same["caveats"]), " "), "interpolated") {
		t.Errorf("two 23.976 copies were called interpolated: %v", same["caveats"])
	}

	// and 50/60 is not suspicious in itself, only against a slower master:
	// sport and video-shot studio work really are 50p
	for _, fps := range []float64{23.976, 24, 25, 29.97, 30} {
		if interpolated(fps) {
			t.Errorf("%v fps read as interpolated", fps)
		}
	}
	for _, fps := range []float64{50, 59.94, 60, 120} {
		if !interpolated(fps) {
			t.Errorf("%v fps did not read as interpolated", fps)
		}
	}
}

// A codec name is not a quality claim. An "EAC3 over AC3 at equal channels"
// rule fired on 198 files whose rates ran from 128k to 640k - bimodal, not
// clustered - so half of them were a real upgrade and half were a downgrade,
// and nothing on the row could tell them apart because the bitrate was not
// there.
func TestQualityCompareWeighsAudioByBitrateNotCodecName(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})

	video := func(audio []map[string]any) map[string]any {
		return map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 6000000, "audio": audio}
	}

	// the newer codec at a low rate: 192k of eac3 is worth about 288k of
	// ac3, which is under the 448k it would replace
	out := mustCall(t, cs, "quality_compare", map[string]any{
		"a": video([]map[string]any{{"language": "eng", "codec": "eac3", "channels": 6, "bitrate": 192000}}),
		"b": video([]map[string]any{{"language": "eng", "codec": "ac3", "channels": 6, "bitrate": 448000}}),
	})
	audio, ok := out["audio"].(map[string]any)
	if !ok {
		t.Fatalf("no audio dimension: %v", out)
	}
	parity := text(audio["parity"])
	if parity == "" {
		t.Fatalf("a 192k eac3 against a 448k ac3 said nothing: %v", audio)
	}
	for _, want := range []string{"192k", "448k", "not a quality claim"} {
		if !strings.Contains(parity, want) {
			t.Errorf("the parity note does not carry %q: %s", want, parity)
		}
	}
	// and it stays a note: the video verdict is untouched by sound
	if text(out["verdict"]) != "comparable" {
		t.Errorf("audio moved the video verdict: %v", out["verdict"])
	}

	// the same codecs at a rate where efficiency does cover the gap: 640k of
	// eac3 is worth about 960k of ac3, so nothing is flagged
	fine := mustCall(t, cs, "quality_compare", map[string]any{
		"a": video([]map[string]any{{"language": "eng", "codec": "eac3", "channels": 6, "bitrate": 640000}}),
		"b": video([]map[string]any{{"language": "eng", "codec": "ac3", "channels": 6, "bitrate": 448000}}),
	})
	if note := text(object(t, fine["audio"], "audio")["parity"]); note != "" {
		t.Errorf("a 640k eac3 was called out against a 448k ac3: %s", note)
	}

	// no bitrate, no claim: the old string carried none, and a rule that
	// guessed from the codec name is what this replaces
	blind := mustCall(t, cs, "quality_compare", map[string]any{
		"a": video([]map[string]any{{"language": "eng", "codec": "eac3", "channels": 6}}),
		"b": video([]map[string]any{{"language": "eng", "codec": "ac3", "channels": 6}}),
	})
	if note := text(object(t, blind["audio"], "audio")["parity"]); note != "" {
		t.Errorf("with no bitrates it claimed something anyway: %s", note)
	}

	// different channel counts are a different question and not this one
	channels := mustCall(t, cs, "quality_compare", map[string]any{
		"a": video([]map[string]any{{"language": "eng", "codec": "eac3", "channels": 2, "bitrate": 192000}}),
		"b": video([]map[string]any{{"language": "eng", "codec": "ac3", "channels": 6, "bitrate": 448000}}),
	})
	if note := text(object(t, channels["audio"], "audio")["parity"]); note != "" {
		t.Errorf("stereo against 5.1 was answered on codec efficiency: %s", note)
	}
}

// An unknown dynamic range is not a claim, so it cannot disagree with one:
// only two copies that both say what they are can differ.
func TestQualityCompareHDRCaveatNeedsTwoClaims(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})
	differs := func(a, b string) bool {
		copyOf := func(hdr string) map[string]any {
			return map[string]any{"width": 3840, "height": 2160, "video_codec": "hevc", "bitrate": 12000000, "hdr": hdr}
		}
		out := mustCall(t, cs, "quality_compare", map[string]any{"a": copyOf(a), "b": copyOf(b)})

		return strings.Contains(strings.Join(texts(out["caveats"]), " "), "HDR formats")
	}
	if !differs("hdr10", "sdr") {
		t.Error("an HDR10 copy against an SDR one went unremarked")
	}
	if differs("unknown", "sdr") || differs("hdr10", "unknown") {
		t.Error("an unknown dynamic range was read as a claim")
	}
	// and a claim is a claim however it is capitalised
	if differs("SDR", "sdr") || differs("HDR10", "hdr10") || differs("Unknown", "hdr10") {
		t.Error("two spellings of one dynamic range were read as two formats")
	}
}

// libraryEpisode is a canned server holding one episode whose video stream
// runs at 8.5 Mbps beside 0.5 Mbps of audio: a library item's bitrate is its
// video stream's, not the whole file's.
func libraryEpisode(t *testing.T) *fakeServer {
	t.Helper()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": []map[string]any{{
			"Id": "ep1", "Name": "Pilot", "Type": "Episode", "LocationType": "FileSystem", "Path": "/media/shows/Some Show/S01E01.mkv",
			"RunTimeTicks": int64(2400) * 10_000_000,
			"MediaSources": []map[string]any{{
				"Path": "/media/shows/Some Show/S01E01.mkv", "Container": "mkv", "Size": int64(2_700_000_000), "Bitrate": 9_000_000,
				"MediaStreams": []map[string]any{
					{"Type": "Video", "Codec": "h264", "Width": 1920, "Height": 1080, "BitRate": 8_500_000},
					{"Type": "Audio", "Codec": "eac3", "Language": "eng", "Channels": 6, "BitRate": 500_000},
				},
			}},
		}}, "TotalRecordCount": 1})
	})

	return f
}

// A bitrate worked out from size and runtime is the whole file's, audio and
// all; a library item's is its video stream's. Compared as one number, a
// 1080p file of 8 Mbps of video and 6 of TrueHD - 14 all told - was called
// 1.65x better than an item of 8.5 Mbps of video: the same picture, a verdict
// that was the audio's. Like is compared with like, the answer says which,
// and where it cannot be made like it says that instead of a verdict.
func TestQualityCompareMeasuresLikeWithLike(t *testing.T) {
	t.Parallel()

	cs := session(t, libraryEpisode(t), Options{})
	// 14 Mbps across 40 minutes
	file := func(audio ...map[string]any) map[string]any {
		return map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "size": 4_200_000_000, "runtime_s": 2400, "audio": audio}
	}

	// the audio's rates are known, so they come out of the whole file
	out := mustCall(t, cs, "quality_compare", map[string]any{
		"a": file(map[string]any{"language": "eng", "codec": "truehd", "channels": 8, "bitrate": 6_000_000}),
		"b": map[string]any{"item_id": "ep1"},
	})
	if text(out["verdict"]) != "comparable" || text(out["bitrate_basis"]) != "video" {
		t.Errorf("8 Mbps of video against 8.5 = %v on %v: %v", out["verdict"], out["bitrate_basis"], out["reasons"])
	}
	a := object(t, out["a"], "a")
	if number(t, a["bitrate"], "bitrate") != 8_000_000 || !strings.Contains(text(a["bitrate_from"]), "less its audio") {
		t.Errorf("a's video rate = %v from %q", a["bitrate"], a["bitrate_from"])
	}
	if b := object(t, out["b"], "b"); number(t, b["bitrate"], "bitrate") != 8_500_000 || !strings.Contains(text(b["bitrate_from"]), "video stream") {
		t.Errorf("b's video rate = %v from %q", b["bitrate"], b["bitrate_from"])
	}

	// with nothing to take the audio out with and only a video rate on the
	// other side, there is no like to compare: a caveat, and no verdict
	blind := mustCall(t, cs, "quality_compare", map[string]any{
		"a": file(),
		"b": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 8_500_000},
	})
	if text(blind["verdict"]) != "unknown" || blind["bitrate_basis"] != nil {
		t.Errorf("a whole file against a video stream = %v on %v", blind["verdict"], blind["bitrate_basis"])
	}
	if caveats := strings.Join(texts(blind["caveats"]), " | "); !strings.Contains(caveats, "measure different things") {
		t.Errorf("the caveat does not say why: %v", blind["caveats"])
	}

	// a library item has a whole-file rate too, so against one the two are
	// compared as whole files - and the audio nobody gave is said to be in it
	items := mustCall(t, cs, "quality_compare", map[string]any{"a": file(), "b": map[string]any{"item_id": "ep1"}})
	if text(items["bitrate_basis"]) != "whole_file" {
		t.Errorf("a whole file against an item = %v on %v", items["verdict"], items["bitrate_basis"])
	}
	if caveats := strings.Join(texts(items["caveats"]), " | "); !strings.Contains(caveats, "sound rather than picture") {
		t.Errorf("whole files with unknown audio went uncaveated: %v", items["caveats"])
	}

	// two whole files are like, and are said to be
	whole := mustCall(t, cs, "quality_compare", map[string]any{
		"a": file(),
		"b": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "size": 1_200_000_000, "runtime_s": 2400},
	})
	if text(whole["verdict"]) != "a_better" || text(whole["bitrate_basis"]) != "whole_file" {
		t.Errorf("two whole files = %v on %v", whole["verdict"], whole["bitrate_basis"])
	}
}

// An unknown frame rate is not a slower master. Its 0 read as one, and every
// 50p copy compared against a file whose rate was not given was called
// interpolated.
func TestQualityCompareDoesNotReadAnUnknownFrameRate(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})

	out := mustCall(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 8000000},
		"b": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 8000000, "frame_rate": 50},
	})
	if strings.Contains(strings.Join(texts(out["caveats"]), " "), "interpolated") {
		t.Errorf("an unknown frame rate against 50p was called interpolated: %v", out["caveats"])
	}
}

// A margin under 1 calls the worse copy better - identical copies measure
// 1.00x, which clears it - so it is refused. 1 is allowed, and still leaves
// two identical copies the same.
func TestQualityCompareRefusesAMarginBelowOne(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})
	same := map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 8000000}

	if msg := mustRefuse(t, cs, "quality_compare", map[string]any{"a": same, "b": same, "policy": map[string]any{"upgrade_margin": 0.5}}); !strings.Contains(msg, "below 1") {
		t.Errorf("a margin of 0.5 said: %s", msg)
	}
	out := mustCall(t, cs, "quality_compare", map[string]any{"a": same, "b": same, "policy": map[string]any{"upgrade_margin": 1}})
	if text(out["verdict"]) != "comparable" {
		t.Errorf("identical copies at a margin of 1 = %v", out["verdict"])
	}
}

// The working prints the ratio it decided on. At two significant figures
// 1.65 printed as 1.6, and "1.6x, over the 1.6x margin" reads as a bug in the
// verdict rather than in the printing.
func TestQualityCompareShowsTheRatioItDecidedOn(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})

	out := mustCall(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 8250000},
		"b": map[string]any{"width": 1920, "height": 1080, "video_codec": "h264", "bitrate": 5000000},
	})
	if decimal(t, out["margin"], "margin") != 1.65 {
		t.Fatalf("margin = %v", out["margin"])
	}
	if reasons := strings.Join(texts(out["reasons"]), " | "); !strings.Contains(reasons, "1.65x, over the 1.60x margin") {
		t.Errorf("the working does not show the ratio it decided on: %s", reasons)
	}
}
