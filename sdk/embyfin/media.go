package embyfin

import (
	"math"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/mediapath"
)

// What a file says of itself, read off the streams the server probed: the
// picture's dynamic range and class, which stream is the video, whether the
// server read the file at all, and which of an item's files answers for it.

// The dynamic range a file carries, or that nobody has established.
const (
	HDRUnknown = "unknown"
	HDRSDR     = "sdr"
	// HDRAny is HDR of a kind the server did not narrow down: a claim of
	// HDR, and no more
	HDRAny          = "hdr"
	HDR10           = "hdr10"
	HDR10Plus       = "hdr10plus"
	HDRHLG          = "hlg"
	HDRDOVI         = "dovi"
	HDRDOVI10       = "dovi_hdr10"
	HDRDOVI10Plus   = "dovi_hdr10plus"
	HDRDOVIHLG      = "dovi_hlg"
	HDRDOVISDR      = "dovi_sdr"
	HDRDOVIEL       = "dovi_el"
	HDRDOVIEL10Plus = "dovi_el_hdr10plus"
	HDRDOVIInvalid  = "dovi_invalid"
)

// jellyfinRanges are Jellyfin's narrow readings (VideoRangeType), by the
// names the hdr field gives them.
var jellyfinRanges = map[string]string{
	"sdr": HDRSDR, "hdr10": HDR10, "hdr10plus": HDR10Plus, "hlg": HDRHLG,
	"dovi": HDRDOVI, "doviwithhdr10": HDRDOVI10, "doviwithhdr10plus": HDRDOVI10Plus, "doviwithhlg": HDRDOVIHLG,
	"doviwithsdr": HDRDOVISDR, "doviwithel": HDRDOVIEL, "doviwithelhdr10plus": HDRDOVIEL10Plus, "doviinvalid": HDRDOVIInvalid,
}

// embyRanges are Emby's narrow readings (ExtendedVideoType) other than Dolby
// Vision, which embyDolbyVision reads by its profile.
var embyRanges = map[string]string{
	"none": "", "hdr10": HDR10, "hdr10plus": HDR10Plus, "hyperloggamma": HDRHLG,
}

// embyDolbyVision are Emby's Dolby Vision profiles (ExtendedVideoSubType,
// DoviProfile and the profile's two digits: 81 is 8.1) by the base layer a
// player without Dolby Vision is left with: 8.1 an HDR10 one, 8.4 HLG, 8.2
// and 9.2 SDR, 5.0 none, and 7.6 and 6.1 an enhancement layer over HDR10.
// A profile not named here is Dolby Vision whose fallback is not known.
var embyDolbyVision = map[string]string{
	"doviprofile81": HDRDOVI10, "doviprofile84": HDRDOVIHLG, "doviprofile82": HDRDOVISDR, "doviprofile92": HDRDOVISDR,
	"doviprofile42": HDRDOVISDR, "doviprofile50": HDRDOVI, "doviprofile76": HDRDOVIEL, "doviprofile61": HDRDOVIEL,
}

// sdrTransfers are the transfer functions of a picture that is not HDR:
// BT.709 and the older broadcast curves, sRGB, and BT.2020's own SDR ones.
var sdrTransfers = []string{
	"bt709", "bt470m", "bt470bg", "smpte170m", "smpte240m", "linear", "log100", "log316",
	"iec61966-2-4", "bt1361e", "iec61966-2-1", "bt2020-10", "bt2020-12", "gamma22", "gamma28",
}

// HDR reads the servers' narrow readings first (Jellyfin's
// VideoRangeType, Emby's ExtendedVideoType and the Dolby Vision profile),
// then the broad one both answer (VideoRange), then the colour transfer, and
// says "unknown" when none of them settles it.
//
// This field used to be absent for three different reasons - the file is SDR,
// the server never probed it, or the server does not expose what it found -
// and a caller could not tell them apart. One did not: reading an absent
// field as "no HDR10, therefore Dolby Vision" put files in the wrong
// bucket. Two files of one release, one answering "pq" and the next
// answering nothing, is inconsistent metadata rather than two formats, and
// only an explicit unknown can say so. Nor is a Dolby Vision file HDR10
// because its base layer is: Jellyfin's DOVIWithEL read as HDR10, and so
// did every Dolby Vision file on Emby, whose broad reading says only "HDR 10".
func (s *MediaStream) HDR() string {
	if format, ok := jellyfinRanges[strings.ToLower(s.VideoRangeType)]; ok {
		return format
	}
	switch kind := strings.ToLower(s.ExtendedVideoType); kind {
	case "dolbyvision":
		if format, ok := embyDolbyVision[strings.ToLower(s.ExtendedVideoSubType)]; ok {
			return format
		}

		return HDRDOVI
	default:
		if format := embyRanges[kind]; format != "" {
			return format
		}
	}

	// then the broad reading both servers answer: Jellyfin says SDR or HDR,
	// and Emby 4.10 the same or, for a file tagged with HDR10's colours,
	// "HDR 10"
	switch strings.ToLower(s.VideoRange) {
	case "sdr":
		return HDRSDR
	case "hdr 10", "hdr10":
		return HDR10
	case "hdr 10+", "hdr10+":
		return HDR10Plus
	case "hlg":
		return HDRHLG
	case "dolby vision":
		return HDRDOVI
	case "hdr":
		if format := hdrFromTransfer(s.ColourTransfer); format != "" {
			return format
		}

		// HDR, and nothing to say which: not HDR10 for want of saying
		return HDRAny
	}

	// and last the transfer function, which a file can carry without the
	// server having formed an opinion about it
	if format := hdrFromTransfer(s.ColourTransfer); format != "" {
		return format
	}
	if slices.Contains(sdrTransfers, strings.ToLower(s.ColourTransfer)) {
		// an SDR transfer is itself a statement
		return HDRSDR
	}

	return HDRUnknown
}

// SpecificHDR says whether a format names one kind of picture, which two
// copies can disagree on: unknown names nothing, and hdr names only that it
// is HDR of some kind.
func SpecificHDR(format string) bool {
	format = strings.ToLower(format)

	return format != "" && format != HDRUnknown && format != HDRAny
}

func hdrFromTransfer(transfer string) string {
	switch strings.ToLower(transfer) {
	case "smpte2084", "smpte-st-2084", "pq":
		return HDR10
	case "arib-std-b67", "hlg":
		return HDRHLG
	}

	return ""
}

// Video is a file's primary video stream, or nil.
func (s *MediaSource) Video() *MediaStream {
	for i := range s.MediaStreams {
		if s.MediaStreams[i].Type == "Video" {
			return &s.MediaStreams[i]
		}
	}

	return nil
}

// Probed says whether the server has read the file: a stream it found in
// it. A size is not a probe: Jellyfin gives a file it could not read (the
// first few kilobytes of an episode, cut short in the copying) its size and
// no streams, and Emby gives it neither (seen on Jellyfin 12.1 and Emby
// 4.10). Nor is a subtitle file beside it, which a server lists as a stream
// of the item without opening the video.
func (i *Item) Probed() bool {
	for j := range i.MediaSources {
		for _, st := range i.MediaSources[j].MediaStreams {
			if !st.IsExternal {
				return true
			}
		}
	}

	return false
}

// BestSource is the file that speaks for an item: the tallest of its
// versions, or nil when it has none.
func (i *Item) BestSource() *MediaSource {
	if len(i.MediaSources) == 0 {
		return nil
	}

	best := &i.MediaSources[0]
	bestHeight := -1
	for j := range i.MediaSources {
		h := 0
		if v := i.MediaSources[j].Video(); v != nil {
			h = v.Height
		}
		if h > bestHeight {
			best, bestHeight = &i.MediaSources[j], h
		}
	}

	return best
}

// SourceAt is the version of an item held at one path, or nil when none of
// them is. A server that finds two files of one episode in a folder merges
// them into one item, so a path can be the second version of something whose
// own path is the first.
func (i *Item) SourceAt(path string) *MediaSource {
	for j := range i.MediaSources {
		if src := &i.MediaSources[j]; src.Path != "" && mediapath.Same(src.Path, path) {
			return src
		}
	}

	return nil
}

// OwnSource is the file an item's own path names, which is the file a row
// naming that path must answer for: a row whose path is one file and whose
// facts are another's (a 2160p version's height beside a 1080p path) reads
// as the path being 2160p. An item with one file answers with it whatever
// its path says (a disc kept whole is held at its folder); one with several,
// none of them at its path, with none.
func (i *Item) OwnSource() *MediaSource {
	if src := i.SourceAt(i.Path); src != nil {
		return src
	}
	if len(i.MediaSources) == 1 {
		return &i.MediaSources[0]
	}

	return nil
}

// ResolutionClass is the line a copy belongs on, in 16:9-equivalent lines:
// 1080 for a 1080p frame, and 405 for a scope DVD cropped to 720x400, which
// is what that frame honestly holds. Frames do not all land on the standard
// lines, so this does not pretend they do - sameClass decides what counts as
// the same (the tools' sameClass).
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
func ResolutionClass(width, height int) int {
	if width <= 0 && height <= 0 {
		return 0
	}

	return max(height, int(math.Round(float64(width)*9/16)))
}
