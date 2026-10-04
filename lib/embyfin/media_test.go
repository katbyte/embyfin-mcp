package embyfin

import "testing"

// The dynamic range a stream carries, read the servers' narrow way first,
// then their broad one, then the transfer function, and unknown when none of
// them says.
func TestMediaStreamHDR(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		st   MediaStream
		want string
	}{
		{"nothing probed", MediaStream{}, HDRUnknown},
		{"Jellyfin narrow", MediaStream{VideoRangeType: "DOVIWithHDR10", VideoRange: "HDR"}, HDRDOVI10},
		{"Jellyfin SDR", MediaStream{VideoRangeType: "SDR", VideoRange: "SDR"}, HDRSDR},
		{"Emby profile 8.1", MediaStream{ExtendedVideoType: "DolbyVision", ExtendedVideoSubType: "DoviProfile81", VideoRange: "HDR 10"}, HDRDOVI10},
		{"Emby profile unknown", MediaStream{ExtendedVideoType: "DolbyVision", ExtendedVideoSubType: "DoviProfile99"}, HDRDOVI},
		{"Emby HDR10+", MediaStream{ExtendedVideoType: "Hdr10Plus"}, HDR10Plus},
		{"Emby none, broad HDR 10", MediaStream{ExtendedVideoType: "None", VideoRange: "HDR 10"}, HDR10},
		{"broad HDR with a PQ transfer", MediaStream{VideoRange: "HDR", ColourTransfer: "smpte2084"}, HDR10},
		{"broad HDR with no transfer", MediaStream{VideoRange: "HDR"}, HDRAny},
		{"an HLG transfer alone", MediaStream{ColourTransfer: "arib-std-b67"}, HDRHLG},
		{"an SDR transfer alone", MediaStream{ColourTransfer: "bt709"}, HDRSDR},
		{"a transfer nobody knows", MediaStream{ColourTransfer: "zzyzx"}, HDRUnknown},
	} {
		if got := tc.st.HDR(); got != tc.want {
			t.Errorf("%s: HDR() = %q, want %q", tc.name, got, tc.want)
		}
	}
	for format, specific := range map[string]bool{HDR10: true, HDRDOVIEL: true, HDRSDR: true, HDRAny: false, HDRUnknown: false, "": false} {
		if SpecificHDR(format) != specific {
			t.Errorf("SpecificHDR(%q) = %v", format, !specific)
		}
	}
}

// Which of an item's files answers for it: the tallest for its facts, the one
// at its own path for a row naming that path, the one at a given path for
// what writing there would replace; and whether the server read any of them.
func TestItemSources(t *testing.T) {
	t.Parallel()

	small := MediaSource{Path: "/m/Zzyzx (2020)/Zzyzx (2020).mkv", MediaStreams: []MediaStream{{Type: "Audio", Codec: "aac"}, {Type: "Video", Codec: "h264", Width: 1280, Height: 720}}}
	big := MediaSource{Path: "/m/Zzyzx (2020)/Zzyzx (2020) - 2160p.mkv", MediaStreams: []MediaStream{{Type: "Video", Codec: "hevc", Width: 3840, Height: 2160}}}
	it := Item{Path: small.Path, MediaSources: []MediaSource{small, big}}

	if v := small.Video(); v == nil || v.Codec != "h264" {
		t.Errorf("Video() = %v, want the h264 stream", v)
	}
	if v := (&MediaSource{MediaStreams: []MediaStream{{Type: "Audio"}}}).Video(); v != nil {
		t.Errorf("Video() of a file with no video = %v", v)
	}
	if best := it.BestSource(); best == nil || best.Path != big.Path {
		t.Errorf("BestSource() = %v, want the 2160p file", best)
	}
	if own := it.OwnSource(); own == nil || own.Path != small.Path {
		t.Errorf("OwnSource() = %v, want the file at the item's path", own)
	}
	if at := it.SourceAt("/m/Zzyzx (2020)//Zzyzx (2020) - 2160p.mkv"); at == nil || at.Path != big.Path {
		t.Errorf("SourceAt() = %v, want the 2160p file, found by a path spelled with a doubled separator", at)
	}
	if at := it.SourceAt("/m/Other.mkv"); at != nil {
		t.Errorf("SourceAt() of a path the item does not hold = %v", at)
	}
	if (&Item{}).BestSource() != nil || (&Item{Path: "/m/a.mkv", MediaSources: []MediaSource{big}}).OwnSource() == nil {
		t.Error("an item with no files has no best file, and one with one file answers with it whatever its path says")
	}
	if (&Item{Path: "/m/a.mkv", MediaSources: []MediaSource{small, big}}).OwnSource() != nil {
		t.Error("an item with several files, none at its path, has no own file")
	}

	// probed is a stream the server read off the file, not a subtitle beside it
	if !it.Probed() {
		t.Error("an item with streams is not probed")
	}
	if (&Item{MediaSources: []MediaSource{{Size: 100, MediaStreams: []MediaStream{{Type: "Subtitle", IsExternal: true}}}}}).Probed() {
		t.Error("a size and an external subtitle count as probed")
	}
}

// The line a frame belongs on: its height, or its width scaled to 16:9 when
// that is more, so bars on either axis leave the class alone.
func TestResolutionClass(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		width, height, want int
	}{
		{1920, 1080, 1080},
		{1920, 816, 1080},  // letterboxed scope
		{1436, 1080, 1080}, // pillarboxed 4:3
		{960, 720, 720},
		{3840, 1920, 2160},
		{720, 400, 405},
		{0, 0, 0},
	} {
		if got := ResolutionClass(tc.width, tc.height); got != tc.want {
			t.Errorf("ResolutionClass(%d, %d) = %d, want %d", tc.width, tc.height, got, tc.want)
		}
	}
}
