package embyfin

import (
	"net/http"
	"testing"
)

// The stored frame is not the picture's shape: a DVD rip is 720x480 or
// 720x576 whether it is 4:3 or an anamorphic 16:9, and only the ratio the
// file states tells which. DisplayWidth is what that ratio makes of the
// height, and nothing when the frame already has that shape.
func TestDisplayWidth(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		ratio         string
		width, height int
		want          int
	}{
		{"16:9", 720, 480, 853},     // anamorphic NTSC DVD
		{"4:3", 720, 576, 768},      // PAL DVD, 4:3
		{"16:9", 1280, 720, 0},      // the frame already is 16:9
		{"16:9", 1920, 1080, 0},     // within rounding of it
		{"", 720, 480, 0},           // the file says nothing
		{"widescreen", 720, 480, 0}, // not a ratio
		{"16:9", 720, 0, 0},         // no height to scale
		// stated as a decimal against 1, as both servers state a VOB's
		{"1.78:1", 720, 480, 854},    // an anamorphic 16:9 DVD
		{"1.33:1", 720, 576, 766},    // a 4:3 PAL DVD
		{"1.5:1", 720, 480, 0},       // the frame's own shape
		{"2.35:1", 1920, 817, 0},     // a scope encode, cropped to its shape
		{"2.39:1", 1440, 1080, 2581}, // anamorphic scope
	} {
		s := MediaStream{AspectRatio: tc.ratio, Width: tc.width, Height: tc.height}
		if got := s.DisplayWidth(); got != tc.want {
			t.Errorf("DisplayWidth(%q %dx%d) = %d, want %d", tc.ratio, tc.width, tc.height, got, tc.want)
		}
	}
	if w, h, ok := ParseAspect("16:9"); !ok || w != 16 || h != 9 {
		t.Errorf("ParseAspect = %v %v %v", w, h, ok)
	}
	if w, h, ok := ParseAspect("1.5:1"); !ok || w != 1.5 || h != 1 {
		t.Errorf("ParseAspect(1.5:1) = %v %v %v", w, h, ok)
	}
	for _, bad := range []string{"0:9", "1.5:0", "-1:1", "NaN:1", "Inf:1", "1.5", ":", "a:b"} {
		if _, _, ok := ParseAspect(bad); ok {
			t.Errorf("ParseAspect(%q) parsed", bad)
		}
	}
}

// Both servers state the ratio on the video stream, and the account's
// playback preferences on its configuration; both reach the neutral types.
func TestAspectRatioAndPreferencesAreMapped(t *testing.T) {
	t.Parallel()

	item := `{"Items":[{"Id":"1","Name":"Zzyzx","Type":"Movie","MediaSources":[{"Container":"mkv","Size":10,"MediaStreams":[{"Type":"Video","Codec":"mpeg2video","Width":720,"Height":480,"AspectRatio":"16:9"}]}]}],"TotalRecordCount":1}`
	users := `[{"Id":"u1","Name":"root","Policy":{"IsAdministrator":true},"Configuration":{"AudioLanguagePreference":"eng","SubtitleLanguagePreference":"fre","SubtitleMode":"Smart","PlayDefaultAudioTrack":false}}]`
	for _, backend := range []Backend{Emby, Jellyfin} {
		routes := map[string]route{"GET /Items": ok(item), "GET /Users": ok(users), "GET /Users/Query": ok(`{"Items":` + users + `,"TotalRecordCount":1}`)}
		c, _ := newFake(t, backend, routes)
		items, _, err := c.Search(t.Context(), SearchOptions{})
		if err != nil || len(items) != 1 {
			t.Fatalf("%s: Search = %v, %v", backend, items, err)
		}
		if st := items[0].MediaSources[0].MediaStreams[0]; st.AspectRatio != "16:9" || st.DisplayWidth() != 853 {
			t.Errorf("%s: stream = %+v, want the stated 16:9 and a display width of 853", backend, st)
		}
		got, err := c.Users(t.Context())
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: Users = %v, %v", backend, got, err)
		}
		if p := got[0].Preferences; p.AudioLanguage != "eng" || p.SubtitleLanguage != "fre" || p.SubtitleMode != "Smart" || p.PlayDefaultAudioTrack {
			t.Errorf("%s: preferences = %+v", backend, p)
		}
	}
	_ = http.StatusOK
}
