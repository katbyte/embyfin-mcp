package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Comparing two copies of the same thing.
//
// Whoever is holding a download beside a library file has to decide which of
// them to keep, and the decision is arithmetic: a resolution class, a bitrate
// the codec has been taken out of, and a margin below which two copies are
// the same copy. Left to the caller, those constants get invented per
// session - one run calls a file worse than another, the next run calls it
// better, and nothing in either answer says why. So the comparison lives
// here, deterministic and tested, and it shows its working: the derived
// numbers, the ratio, and the constants it used, so a caller can disagree
// with one step rather than with the verdict.
//
// What does NOT live here is the consequence. Whether a 1.4x edge is worth
// acting on, and what to do about it, depends on what the two sides are worth
// to whoever owns them: a copy that cannot be replaced and one that can be
// fetched again have a different answer to the same ratio than two copies of
// equal standing. The verdict is guidance, not a ruling.

// resolutionClass is the line a copy belongs on, in 16:9-equivalent lines:
// 1080 for a 1080p frame, and 405 for a scope DVD cropped to 720x400, which
// is what that frame honestly holds. Frames do not all land on the standard
// lines, so this does not pretend they do - sameClass decides what counts as
// the same.
//
// max(height, width scaled to 16:9) rather than either on its own, because
// both are wrong alone and in opposite directions. Height alone reads a
// 3840x1920 cinematic 4K as 1440p. Width alone reads a 4:3 960x720 as 480p.
//
// Taking the larger of the two is also what makes this survive black bars,
// which is the case that catches people out: bars sit on the axis they sit
// on and leave the other one alone. A 2.35:1 film letterboxed into 1920x1080
// has a 1920x816 picture and still classes 1080 on its width; a 4:3 episode
// pillarboxed into 1920x1080 has a 1436x1080 picture and still classes 1080
// on its height. A copy of that same episode cropped to 1436x1080 classes
// 1080 too, which is the point: they hold the same picture and neither is
// "bigger".
//
// This is why nothing here compares raw pixel counts. 1920x1080 with bars
// baked in has more pixels than 1436x1080 without them and not one more pixel
// of picture.
func resolutionClass(width, height int) int {
	if width <= 0 && height <= 0 {
		return 0
	}

	return max(height, int(math.Round(float64(width)*9/16)))
}

// classTolerance is how close two classes have to be to be the same class.
// Frames do not land on the standard lines: a scope DVD cropped to its
// picture is 720x400, which reads as 405, and a caller should not be told
// that is a different class from 480 lines of the same DVD. Snapping to the
// nearest standard line was the obvious alternative and it is worse - it
// invents a certainty the frame does not have, and gets 405 wrong whichever
// line it picks.
const classTolerance = 0.1

// sameClass says whether two frames belong on the same line, within the
// tolerance. Ordering is not enough on its own: 1436x1080 and 1920x1080 hold
// the same picture and neither is bigger.
func sameClass(a, b int) bool {
	if a == b {
		return true
	}
	hi, lo := max(a, b), min(a, b)
	if lo <= 0 {
		return false
	}

	return float64(hi-lo)/float64(hi) <= classTolerance
}

// codecEfficiency is how many bits of h264 one bit of each codec is worth, so
// two copies in different codecs can be compared at all. h264 is 1 by
// definition and everything else is relative to it.
//
// These are the published rules of thumb, not measurements of anyone's files:
// HEVC is generally cited at a 40-50% bitrate saving over H.264 for equal
// quality, AV1 at roughly 30% over HEVC, VP9 between the two, and the codecs
// that predate H.264 well behind it. Real savings depend on the encoder, its
// settings and the content, so any of these can be overridden per call, and
// whatever was used comes back in the answer.
var codecEfficiency = map[string]float64{
	"h264": 1, "avc": 1, "x264": 1,
	"hevc": 1.7, "h265": 1.7, "x265": 1.7,
	"av1": 2.2,
	"vp9": 1.5, "vp8": 0.9,
	"mpeg4": 0.6, "msmpeg4": 0.6, "divx": 0.6, "xvid": 0.6,
	"mpeg2video": 0.45, "mpeg2": 0.45, "mpeg1video": 0.35,
	"vc1": 0.9, "wmv3": 0.8, "wmv2": 0.6, "theora": 0.6,
}

// defaultUpgradeMargin is how much better one copy has to measure before it
// is called better rather than the same. Two encodes of one source differ by
// a few percent on bitrate without differing in what you see, so a margin
// near 1 would report noise as findings; 1.6 is roughly the gap between
// neighbouring quality tiers of the same release.
const defaultUpgradeMargin = 1.6

// aspectTolerance is how far two frames' shapes may differ before they are
// not the same shape. 16:9 is 1.778 and 1.85:1 flat is 1.85, which are
// different framings of the same film; 1.778 against 1.33 is not.
const aspectTolerance = 0.06

// comparePolicy is the arithmetic a comparison is made under, all of it
// overridable and all of it reported back.
type comparePolicy struct {
	CodecEfficiency map[string]float64 `json:"codec_efficiency,omitempty" jsonschema:"how many bits of h264 one bit of each codec is worth. Anything not named here is taken as h264"`
	UpgradeMargin   float64            `json:"upgrade_margin,omitempty"   jsonschema:"how many times the effective bitrate one copy needs over the other before it is called better rather than comparable"`
}

// resolve fills a caller's policy in with the defaults it did not override.
func (p comparePolicy) resolve() comparePolicy {
	out := comparePolicy{CodecEfficiency: map[string]float64{}, UpgradeMargin: p.UpgradeMargin}
	maps.Copy(out.CodecEfficiency, codecEfficiency)
	for codec, factor := range p.CodecEfficiency {
		out.CodecEfficiency[strings.ToLower(strings.TrimSpace(codec))] = factor
	}
	if out.UpgradeMargin <= 0 {
		out.UpgradeMargin = defaultUpgradeMargin
	}

	return out
}

// efficiency is what one bit of a codec is worth, defaulting to h264's 1 for
// a codec nobody has priced.
func (p comparePolicy) efficiency(codec string) float64 {
	if f, ok := p.CodecEfficiency[strings.ToLower(strings.TrimSpace(codec))]; ok {
		return f
	}

	return 1
}

// copyIn is one of the two copies being compared: a library item by id, or
// the numbers off a file the library has never seen.
type copyIn struct {
	ItemID     string       `json:"item_id,omitempty"     jsonschema:"a library item to read the facts off, in place of giving them. The other side is usually a file outside the library, given as numbers"`
	Width      int          `json:"width,omitempty"`
	Height     int          `json:"height,omitempty"`
	VideoCodec string       `json:"video_codec,omitempty" jsonschema:"h264, hevc, av1, mpeg2video..."`
	FrameRate  float64      `json:"frame_rate,omitempty"  jsonschema:"frames per second. Worth giving: a scripted show at 59.94 or 60 was interpolated from a 23.976 master, and no resolution makes up for that"`
	HDR        string       `json:"hdr,omitempty"         jsonschema:"the HDR format the file claims (pq/hlg), if any"`
	Bitrate    int64        `json:"bitrate,omitempty"     jsonschema:"bits per second. Worked out from size and runtime_s when not given"`
	Size       int64        `json:"size,omitempty"        jsonschema:"file size in bytes"`
	RuntimeS   int          `json:"runtime_s,omitempty"   jsonschema:"runtime in seconds"`
	Audio      []audioTrack `json:"audio,omitempty"       jsonschema:"one entry per audio track, as show_episodes_exist reports them: {language, codec, channels, bitrate}. Reported on its own, never folded into the video verdict"`
	Container  string       `json:"container,omitempty"`
}

// copyFacts is one side as the comparison reads it: what it was given, and
// everything derived from it.
type copyFacts struct {
	Source          string   `json:"source"                      jsonschema:"where these facts came from: the library item, or given by the caller"`
	Width           int      `json:"width,omitempty"`
	Height          int      `json:"height,omitempty"`
	ResolutionClass int      `json:"resolution_class,omitempty"  jsonschema:"480, 720, 1080, 2160: the line this frame belongs on, from max(height, width scaled to 16:9) so black bars on either axis do not move it"`
	Aspect          float64  `json:"aspect,omitempty"            jsonschema:"the encoded frame's shape, width over height"`
	VideoCodec      string   `json:"video_codec,omitempty"`
	FrameRate       float64  `json:"frame_rate,omitempty"        jsonschema:"frames per second"`
	HDR             string   `json:"hdr,omitempty"               jsonschema:"the HDR format the file claims, if any"`
	Bitrate         int64    `json:"bitrate,omitempty"           jsonschema:"bits per second as given or read"`
	BitrateFrom     string   `json:"bitrate_from,omitempty"      jsonschema:"set when the bitrate was worked out rather than given"`
	Effective       int64    `json:"effective_bitrate,omitempty" jsonschema:"the bitrate in h264-equivalent bits per second: what this codec's bits are worth against the other side's"`
	BitsPerPixel    float64  `json:"bits_per_pixel,omitempty"    jsonschema:"effective bitrate over the frame's pixels: how thinly the frame is encoded, which is what says whether a big frame is actually carrying detail"`
	Size            int64    `json:"size,omitempty"`
	RuntimeS        int      `json:"runtime_s,omitempty"`
	AudioTracks     int      `json:"audio_tracks,omitempty"`
	AudioLanguages  []string `json:"audio_languages,omitempty"`
	AudioCodec      string   `json:"audio_codec,omitempty"       jsonschema:"the codec of the best track: most channels, then highest bitrate"`
	AudioChannels   int      `json:"audio_channels,omitempty"`
	AudioBitrate    int64    `json:"audio_bitrate,omitempty"     jsonschema:"that track's bits per second, when known"`
}

// audioNote is the audio dimension, reported beside the video verdict and
// never folded into it: a second language is not worth some number of
// megabits, and pretending it is would make the verdict unpredictable. A
// caller that cares weighs it itself.
type audioNote struct {
	OnlyA  []string `json:"only_a,omitempty" jsonschema:"audio languages only the a copy has"`
	OnlyB  []string `json:"only_b,omitempty" jsonschema:"audio languages only the b copy has"`
	Note   string   `json:"note,omitempty"   jsonschema:"what that means for a caller that is about to replace one with the other"`
	Parity string   `json:"parity,omitempty" jsonschema:"set when the two copies' best tracks are in different codecs and the numbers say the newer codec is NOT the better track. A codec name is not a quality claim: E-AC-3 is the more efficient codec and an E-AC-3 track at 192k still loses to a 640k AC-3"`
}

// compareOut is the answer: the verdict, how much, and the working.
type compareOut struct {
	Verdict         string        `json:"verdict"                    jsonschema:"a_better, b_better, comparable, or unknown when the facts do not settle it. Guidance, not a ruling: it says which copy measures better and by how much, never what to do about it"`
	Margin          float64       `json:"margin,omitempty"           jsonschema:"how much better, on whatever decided it: the ratio of the better copy to the worse, 1 being identical"`
	DecidedBy       string        `json:"decided_by,omitempty"       jsonschema:"resolution, bitrate, or nothing when the two measure the same"`
	Reasons         []string      `json:"reasons"                    jsonschema:"the working, in order: what was derived, what was compared, and what settled it"`
	Caveats         []string      `json:"caveats,omitempty"          jsonschema:"what would change the answer and cannot be seen from the numbers given. A verdict with caveats is worth less than one without"`
	A               copyFacts     `json:"a"`
	B               copyFacts     `json:"b"`
	ResolutionRatio float64       `json:"resolution_ratio,omitempty" jsonschema:"a's resolution class over b's"`
	BitrateRatio    float64       `json:"bitrate_ratio,omitempty"    jsonschema:"a's effective bitrate over b's, the codecs taken out of both"`
	Audio           audioNote     `json:"audio,omitzero"             jsonschema:"the audio dimension, reported separately and never folded into the verdict"`
	Policy          comparePolicy `json:"policy_used"                jsonschema:"every constant this answer was made with, defaults included, so the same call can be made again or argued with"`
}

func registerQualityTools(r *registry) {
	client := r.client

	type compareIn struct {
		A      copyIn        `json:"a"               jsonschema:"one copy: a library item id, or the numbers off a file"`
		B      copyIn        `json:"b"               jsonschema:"the other copy, the same way"`
		Policy comparePolicy `json:"policy,omitzero" jsonschema:"override the arithmetic: the codec efficiencies, the margin, or both. Whatever is left out keeps its default, and the whole resolved policy comes back in policy_used"`
	}

	add(r, readTool, &mcp.Tool{
		Name: "quality_compare",
		Description: "Which of two copies of the same episode or film is the better one, by how much, and why. " +
			"Give each side as a library item id or as the numbers off a file (width, height, video_codec, bitrate or size+runtime_s), and it answers with the resolution class, the bitrate with the codec taken out of it, the ratio between them, and the working. " +
			"It compares; it does not decide what to do - whether a margin is worth acting on depends on what the two copies are worth to their owner, which this cannot know. " +
			"The arithmetic (codec efficiencies, the margin below which two copies are called the same) is overridable per call and always reported back.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in compareIn) (*mcp.CallToolResult, compareOut, error) {
		policy := in.Policy.resolve()

		a, reasonsA, err := readCopy(ctx, client, in.A, "a", policy)
		if err != nil {
			return nil, compareOut{}, err
		}
		b, reasonsB, err := readCopy(ctx, client, in.B, "b", policy)
		if err != nil {
			return nil, compareOut{}, err
		}

		out := compareOut{A: a, B: b, Policy: policy, Reasons: append(reasonsA, reasonsB...)}
		out.Audio = compareAudio(a, b)
		out.Verdict, out.Margin, out.DecidedBy = decide(&out)

		return nil, out, nil
	})
}

// readCopy turns one side into facts, reading them off the library when it
// was given an id, and says what it derived on the way.
func readCopy(ctx context.Context, client *embyfin.Client, in copyIn, side string, policy comparePolicy) (copyFacts, []string, error) {
	facts := copyFacts{
		Source:     "given",
		Width:      in.Width,
		Height:     in.Height,
		VideoCodec: in.VideoCodec,
		FrameRate:  in.FrameRate,
		HDR:        in.HDR,
		Bitrate:    in.Bitrate,
		Size:       in.Size,
		RuntimeS:   in.RuntimeS,
	}
	audio := in.Audio

	if in.ItemID == "" && in.Width <= 0 && in.Height <= 0 {
		return copyFacts{}, nil, fmt.Errorf("copy %s: %w", side, errNoCopy)
	}

	if in.ItemID != "" {
		item, err := client.ItemByID(ctx, in.ItemID)
		if err != nil {
			return copyFacts{}, nil, fmt.Errorf("copy %s: %w", side, err)
		}
		// the same facts every other tool answers with, so the two sides of a
		// comparison are read the same way
		q := qualityOf(item)
		facts = copyFacts{
			Source:     fmt.Sprintf("%s (item %s)", item.Name, item.ID),
			Width:      q.Width,
			Height:     q.Height,
			VideoCodec: q.VideoCodec,
			FrameRate:  q.FrameRate,
			HDR:        q.HDR,
			Bitrate:    q.Bitrate,
			Size:       q.Size,
			RuntimeS:   int(item.RunTimeTicks / 10_000_000),
		}
		audio = q.Audio
		if !item.HasFile() {
			return copyFacts{}, nil, fmt.Errorf("copy %s: %q is a record with no file, so there is nothing to compare", side, item.Name)
		}
		if facts.Width <= 0 && facts.Height <= 0 {
			// a series or a season holds episodes rather than a file: the
			// id is one level up from the thing being compared
			return copyFacts{}, nil, fmt.Errorf("copy %s: %q is a %s, which has no frame of its own - compare the episodes (show_episodes lists them with their ids)", side, item.Name, strings.ToLower(item.Type))
		}
	}

	var reasons []string
	if facts.Bitrate <= 0 && facts.Size > 0 && facts.RuntimeS > 0 {
		facts.Bitrate = facts.Size * 8 / int64(facts.RuntimeS)
		facts.BitrateFrom = "size and runtime"
		reasons = append(reasons, fmt.Sprintf("%s: no bitrate given, worked out %s from %s over %ds", side, mbps(facts.Bitrate), bytes(facts.Size), facts.RuntimeS))
	}

	facts.ResolutionClass = resolutionClass(facts.Width, facts.Height)
	if facts.Height > 0 {
		facts.Aspect = math.Round(float64(facts.Width)/float64(facts.Height)*100) / 100
	}
	if facts.Bitrate > 0 {
		factor := policy.efficiency(facts.VideoCodec)
		facts.Effective = int64(float64(facts.Bitrate) * factor)
		if factor != 1 {
			reasons = append(reasons, fmt.Sprintf("%s: %s is %s, worth %s of h264 at x%.2g", side, facts.VideoCodec, mbps(facts.Bitrate), mbps(facts.Effective), factor))
		}
		if px := facts.Width * facts.Height; px > 0 {
			facts.BitsPerPixel = math.Round(float64(facts.Effective)/float64(px)*100) / 100
		}
	}

	facts.AudioTracks = len(audio)
	facts.AudioLanguages = audioLanguages(audio)
	if best := bestTrack(audio); best != nil {
		facts.AudioCodec, facts.AudioChannels, facts.AudioBitrate = best.Codec, best.Channels, best.Bitrate
	}

	return facts, reasons, nil
}

// decide weighs the two sides and says which is better, by how much, and on
// what. It appends the working to the answer as it goes.
func decide(out *compareOut) (verdict string, margin float64, decidedBy string) {
	a, b := &out.A, &out.B

	if a.ResolutionClass == 0 || b.ResolutionClass == 0 {
		out.Reasons = append(out.Reasons, "one copy has no frame size, so there is nothing to place it against")

		return "unknown", 0, ""
	}

	out.ResolutionRatio = math.Round(float64(a.ResolutionClass)/float64(b.ResolutionClass)*100) / 100
	if a.Effective > 0 && b.Effective > 0 {
		out.BitrateRatio = math.Round(float64(a.Effective)/float64(b.Effective)*100) / 100
	}

	// a frame rate no camera or telecine produces says the file was made by
	// something rather than shot. This is the one claim a release cannot
	// inflate - width, codec and HDR can all be asserted by an encoder, and
	// 60fps on a 24fps master cannot be anything but interpolation - so it
	// outranks the frame size rather than sitting beside it.
	if interpolated(a.FrameRate) != interpolated(b.FrameRate) {
		synthetic := a
		if interpolated(b.FrameRate) {
			synthetic = b
		}
		out.Caveats = append(out.Caveats, fmt.Sprintf(
			"one copy runs at %.3g fps and the other at %.3g: no broadcast or disc master of a scripted show ships at 60p, so the %.3g fps copy was interpolated from a slower master. Whatever it measures, it holds no frames the other does not - and an upscale of the same master carries no detail either. Treat it as worse at equal resolution, and do not let a bigger frame on it read as a better copy",
			a.FrameRate, b.FrameRate, synthetic.FrameRate))
	}

	// an HDR claim on a copy whose partner is SD-era is a claim about the
	// encode, not the picture
	if a.HDR != "" && b.HDR != "" && a.HDR != b.HDR {
		out.Caveats = append(out.Caveats, fmt.Sprintf("the copies claim different HDR formats (%s against %s)", a.HDR, b.HDR))
	}

	// two frames of different shapes are not two sizes of the same picture:
	// one of them is carrying bars, or they are different cuts
	if a.Aspect > 0 && b.Aspect > 0 && math.Abs(a.Aspect-b.Aspect) > aspectTolerance {
		out.Caveats = append(out.Caveats, fmt.Sprintf(
			"the frames are different shapes (%.2f against %.2f): one copy may have black bars baked in, or they are different cuts. Bars cost frame but hold no picture, so neither the frame sizes nor the bits per pixel below are comparing like with like",
			a.Aspect, b.Aspect))
	}

	if !sameClass(a.ResolutionClass, b.ResolutionClass) {
		hi, lo, better := a, b, "a_better"
		if b.ResolutionClass > a.ResolutionClass {
			hi, lo, better = b, a, "b_better"
		}
		margin = math.Round(float64(hi.ResolutionClass)/float64(lo.ResolutionClass)*100) / 100
		out.Reasons = append(out.Reasons, fmt.Sprintf("%dp against %dp: a whole class apart, %.2gx the lines", hi.ResolutionClass, lo.ResolutionClass, margin))

		// a bigger frame encoded thinly enough is the same picture softened:
		// worth saying, not worth overruling the class on
		if hi.BitsPerPixel > 0 && lo.BitsPerPixel > 0 && hi.BitsPerPixel < lo.BitsPerPixel/2 {
			out.Caveats = append(out.Caveats, fmt.Sprintf(
				"the larger frame is the more thinly encoded one (%.2f bits per pixel against %.2f): a %dp encode starved of bitrate can look worse than a well-fed %dp one, and the class alone does not see that",
				hi.BitsPerPixel, lo.BitsPerPixel, hi.ResolutionClass, lo.ResolutionClass))
		}

		return better, margin, "resolution"
	}

	out.Reasons = append(out.Reasons, fmt.Sprintf("%dp against %dp: the same class, so it comes down to bitrate", a.ResolutionClass, b.ResolutionClass))
	if a.Effective <= 0 || b.Effective <= 0 {
		out.Reasons = append(out.Reasons, "one copy has no bitrate and no size and runtime to work one out from, so the two cannot be told apart")

		return "unknown", 0, ""
	}

	hi, lo, better := a, b, "a_better"
	if b.Effective > a.Effective {
		hi, lo, better = b, a, "b_better"
	}
	margin = math.Round(float64(hi.Effective)/float64(lo.Effective)*100) / 100
	if margin < out.Policy.UpgradeMargin {
		out.Reasons = append(out.Reasons, fmt.Sprintf("%s against %s effective: %.2gx, under the %.2gx margin, so they are the same copy for this purpose",
			mbps(hi.Effective), mbps(lo.Effective), margin, out.Policy.UpgradeMargin))

		return "comparable", margin, "nothing"
	}
	out.Reasons = append(out.Reasons, fmt.Sprintf("%s against %s effective: %.2gx, over the %.2gx margin",
		mbps(hi.Effective), mbps(lo.Effective), margin, out.Policy.UpgradeMargin))

	return better, margin, "bitrate"
}

// interpolated says whether a frame rate is one no scripted master is shot or
// mastered at. 23.976, 24 and 25 are film and PAL; 29.97 and 30 are NTSC
// video; 50 and 60 are sport, soaps and video game capture - and on a drama
// they are what a frame interpolator leaves behind.
func interpolated(fps float64) bool {
	return fps >= 47
}

// compareAudio reports what each side has that the other does not. It never
// feeds the verdict: replacing a copy that carries a second language with one
// that does not is a loss no bitrate makes up for, and only the caller knows
// whether it matters here.
func compareAudio(a, b copyFacts) audioNote {
	note := audioNote{}
	note.Parity = audioParity(
		&audioTrack{Codec: a.AudioCodec, Channels: a.AudioChannels, Bitrate: a.AudioBitrate},
		&audioTrack{Codec: b.AudioCodec, Channels: b.AudioChannels, Bitrate: b.AudioBitrate},
	)
	for _, lang := range a.AudioLanguages {
		if !slices.Contains(b.AudioLanguages, lang) {
			note.OnlyA = append(note.OnlyA, lang)
		}
	}
	for _, lang := range b.AudioLanguages {
		if !slices.Contains(a.AudioLanguages, lang) {
			note.OnlyB = append(note.OnlyB, lang)
		}
	}
	switch {
	case len(note.OnlyA) > 0 && len(note.OnlyB) > 0:
		note.Note = "each copy carries audio the other does not: neither replaces the other without losing something"
	case len(note.OnlyA) > 0:
		note.Note = "only the a copy carries " + strings.Join(note.OnlyA, ", ") + ": replacing it with b loses that audio, whatever the video says"
	case len(note.OnlyB) > 0:
		note.Note = "only the b copy carries " + strings.Join(note.OnlyB, ", ") + ": replacing it with a loses that audio, whatever the video says"
	}

	return note
}

// audioLanguages is the languages a track list names, in order and without
// repeats.
func audioLanguages(tracks []audioTrack) []string {
	out := []string{}
	for _, track := range tracks {
		lang := cmp.Or(track.Language, "und")
		if !slices.Contains(out, lang) {
			out = append(out, lang)
		}
	}

	return out
}

// bestTrack is the track a copy would be judged on: the most channels, and
// the highest bitrate among those.
func bestTrack(tracks []audioTrack) *audioTrack {
	var best *audioTrack
	for i := range tracks {
		t := &tracks[i]
		switch {
		case best == nil, t.Channels > best.Channels:
			best = t
		case t.Channels == best.Channels && t.Bitrate > best.Bitrate:
			best = t
		}
	}

	return best
}

// audioEfficiency is how many bits of AC-3 one bit of each codec is worth, on
// the same published rules-of-thumb footing as the video table: E-AC-3 is
// generally cited around 1.5x AC-3 for equal quality, AAC and Opus better
// again, and the lossless formats are not on this scale at all.
//
// It exists to answer one question a codec name cannot: whether a newer codec
// at a lower rate actually beats an older one. AC-3 tops out at 640 kbps, so
// an E-AC-3 track near that rate wins outright - and an E-AC-3 track at 192k
// does not, however modern its name.
var audioEfficiency = map[string]float64{
	"ac3": 1, "eac3": 1.5, "aac": 1.6, "opus": 2, "vorbis": 1.4, "mp3": 0.8,
	"dts": 0.9, "truehd": 4, "flac": 4, "alac": 4, "dtshd": 4, "pcm": 4,
}

// audioParity says whether one track plainly beats another on sound, and why,
// or "" when the numbers do not settle it. It is a caveat, never a verdict:
// channel layout, mastering and what the listener plays it on all matter, and
// none of them are in these fields.
func audioParity(a, b *audioTrack) string {
	if a == nil || b == nil || a.Bitrate <= 0 || b.Bitrate <= 0 {
		return ""
	}
	if a.Channels != b.Channels || strings.EqualFold(a.Codec, b.Codec) {
		return ""
	}

	ea, eb := audioEfficiencyOf(a.Codec), audioEfficiencyOf(b.Codec)
	newer, older := a, b
	if eb > ea {
		newer, older = b, a
	}
	if audioEfficiencyOf(newer.Codec) <= audioEfficiencyOf(older.Codec) {
		return ""
	}

	// the newer codec's bits are worth more, but not without limit
	effective := float64(newer.Bitrate) * audioEfficiencyOf(newer.Codec) / audioEfficiencyOf(older.Codec)
	if effective >= float64(older.Bitrate) {
		return ""
	}

	return fmt.Sprintf(
		"on sound the newer codec is not the better track here: %s at %dk against %s at %dk, %d channels each. %s is worth about %.2gx %s, which makes that %dk worth roughly %.0fk of %s - still under the other copy. A codec name is not a quality claim",
		newer.Codec, newer.Bitrate/1000, older.Codec, older.Bitrate/1000, newer.Channels,
		newer.Codec, audioEfficiencyOf(newer.Codec)/audioEfficiencyOf(older.Codec), older.Codec,
		newer.Bitrate/1000, effective/1000, older.Codec)
}

func audioEfficiencyOf(codec string) float64 {
	if f, ok := audioEfficiency[strings.ToLower(strings.TrimSpace(codec))]; ok {
		return f
	}

	return 1
}

func mbps(bits int64) string {
	return fmt.Sprintf("%.3g Mbps", float64(bits)/1e6)
}

func bytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.3g GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.3g MB", float64(b)/(1<<20))
	}

	return fmt.Sprintf("%d B", b)
}

var errNoCopy = errors.New("each copy needs either an item_id or a frame size: give width and height, or the item to read them off")
