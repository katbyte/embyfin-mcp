//go:build integration

package integration

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/jf"
	"github.com/katbyte/go-kt/pointer"
)

// jfFiles lists a library's items of one kind with their media sources,
// keyed by path below the library's folder ("Alien (1979)/Alien (1979).mp4",
// or the folder itself for a disc kept whole).
func jfFiles(t *testing.T, l libraryFixture, kind jf.BaseItemKind) map[string]*jf.BaseItemDto {
	t.Helper()

	res := must(jfc.GetItems(t.Context(), jf.GetItemsOperationOptions{
		ParentId: jfLibrary(t, l), Recursive: new(true), IncludeItemTypes: []jf.BaseItemKind{kind},
		Fields: []jf.ItemFields{jf.ItemFieldsPath, jf.ItemFieldsMediaSources, jf.ItemFieldsMediaStreams},
	})).Model
	out := map[string]*jf.BaseItemDto{}
	for i := range res.Items {
		out[strings.TrimPrefix(res.Items[i].Path, l.Folder+"/")] = &res.Items[i]
	}

	return out
}

// jfFile is the item at a path jfFiles keyed, which must be there.
func jfFile(t *testing.T, files map[string]*jf.BaseItemDto, path string) *jf.BaseItemDto {
	t.Helper()

	it, ok := files[path]
	if !ok {
		t.Fatalf("no item at %s among %v", path, slices.Sorted(maps.Keys(files)))
	}
	if len(it.MediaSources) == 0 {
		t.Fatalf("%s has no media source", path)
	}

	return it
}

// jfStreams is a media source's streams of one type.
func jfStreams(src *jf.MediaSourceInfo, typ jf.MediaStreamType) []*jf.MediaStream {
	var out []*jf.MediaStream
	for i := range src.MediaStreams {
		if s := &src.MediaStreams[i]; s.Type == typ {
			out = append(out, s)
		}
	}

	return out
}

// jfVideo checks a source's picture: the size, codec and rate ffmpeg wrote,
// and the range Jellyfin reads it as (SDR, which it names twice).
func jfVideo(t *testing.T, where string, src *jf.MediaSourceInfo, codec string, width, height int, fps float32, aspect string) {
	t.Helper()

	v := jfStreams(src, jf.MediaStreamTypeVideo)
	if len(v) != 1 {
		t.Errorf("%s: %d video streams, want 1: %+v", where, len(v), src)
		return
	}
	if v[0].Codec != codec || v[0].Width != width || v[0].Height != height || v[0].RealFrameRate != fps || v[0].AverageFrameRate != fps ||
		v[0].VideoRange != jf.VideoRangeSDR || v[0].VideoRangeType != jf.VideoRangeTypeSDR || v[0].AspectRatio != aspect {
		t.Errorf("%s: video %s %dx%d at %v/%v fps, range %q/%q, aspect %q; want %s %dx%d at %v fps, SDR, %s",
			where, v[0].Codec, v[0].Width, v[0].Height, v[0].RealFrameRate, v[0].AverageFrameRate, v[0].VideoRange, v[0].VideoRangeType, v[0].AspectRatio, codec, width, height, fps, aspect)
	}
}

// jfAudio checks a source's sound: the codec, the channels, and the language
// tag (ffmpeg's "und" for an untagged track, nothing for a track in a
// container with no language field at all).
func jfAudio(t *testing.T, where string, src *jf.MediaSourceInfo, codec, language string, channels int) {
	t.Helper()

	a := jfStreams(src, jf.MediaStreamTypeAudio)
	if len(a) != 1 || a[0].Codec != codec || a[0].Language != language || a[0].Channels != channels {
		t.Errorf("%s: audio %+v, want one %s stream in %q of %d channels", where, a, codec, language, channels)
	}
}

// TestJFMediaStreams reads what Jellyfin says of the files themselves, the
// fields the quality, language and disc audits read, as the SDK decodes them:
// the size, codec, frame rate and range of the picture, the codec and
// language of the sound, a subtitle beside a film, a film held as two files,
// a disc kept whole and one copied in loose, and a file holding two episodes.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestJFMediaStreams(t *testing.T) {
	skipUnlessJellyfin(t)

	clean := jfFiles(t, sdkMovies, jf.BaseItemKindMovie)
	messy := jfFiles(t, sdkMessyMovies, jf.BaseItemKindMovie)
	episodes := jfFiles(t, sdkMessyShows, jf.BaseItemKindEpisode)

	t.Run("CleanFilm", func(t *testing.T) {
		it := jfFile(t, clean, "Alien (1979)/Alien (1979).mp4")
		src := &it.MediaSources[0]
		if it.VideoType != jf.VideoTypeVideoFile || src.Container != "mp4" || src.Size == 0 || src.Bitrate == 0 || it.RunTimeTicks != 10_000_000 {
			t.Errorf("%s is a %s, its source %s, %d bytes at %d b/s, running %d ticks; want a video file, an mp4 of one second",
				alien, it.VideoType, src.Container, src.Size, src.Bitrate, it.RunTimeTicks)
		}
		jfVideo(t, alien, src, "h264", 1280, 720, 5, "16:9")
		jfAudio(t, alien, src, "aac", "und", 1)
		if s := jfStreams(src, jf.MediaStreamTypeSubtitle); len(s) != 0 {
			t.Errorf("%s has subtitles %+v, and no file beside it", alien, s)
		}
	})
	t.Run("Subtitle", func(t *testing.T) {
		// the .eng.srt beside the film is an external stream, listed first,
		// its language read from the name and kept as the three-letter code
		src := &jfFile(t, clean, "The Thirteenth Floor (1999)/The Thirteenth Floor (1999).mp4").MediaSources[0]
		s := jfStreams(src, jf.MediaStreamTypeSubtitle)
		if len(s) != 1 || s[0].Codec != "subrip" || s[0].Language != "eng" || !pointer.From(s[0].IsExternal) || s[0].Index != 0 ||
			!strings.HasSuffix(s[0].Path, ".eng.srt") || s[0].DisplayTitle != "English - SUBRIP - External" {
			t.Errorf("%s's subtitles = %+v, want the one external English srt", thirteenthFloor, s)
		}
	})
	t.Run("LegacyCodecAndLanguage", func(t *testing.T) {
		// MPEG-4 part 2 at 360p, the sound tagged Japanese
		src := &jfFile(t, messy, "Princess Mononoke (1997)/Princess Mononoke (1997).mp4").MediaSources[0]
		jfVideo(t, "the messy Princess Mononoke", src, "mpeg4", 640, 360, 5, "16:9")
		jfAudio(t, "the messy Princess Mononoke", src, "aac", "jpn", 1)
	})
	t.Run("TwoVersions", func(t *testing.T) {
		// Jellyfin folds the files of a film's folder into one film, at the
		// largest of them, with a source for each named after its suffix
		it := jfFile(t, messy, "Blade Runner (1982)/Blade Runner (1982) - 2160p.mp4")
		if _, ok := messy["Blade Runner (1982)/Blade Runner (1982) - 1080p.mp4"]; ok || it.Name != bladeRunner || len(it.MediaSources) != 2 {
			t.Fatalf("Blade Runner = %s with %d sources, and the 1080p file listed on its own %t; want one film of two sources", it.Name, len(it.MediaSources), ok)
		}
		byName := map[string]*jf.MediaSourceInfo{}
		for i := range it.MediaSources {
			byName[it.MediaSources[i].Name] = &it.MediaSources[i]
		}
		if byName["1080p"] == nil || byName["2160p"] == nil {
			t.Fatalf("Blade Runner's sources = %+v, want one named 1080p and one 2160p", it.MediaSources)
		}
		jfVideo(t, "Blade Runner's 2160p", byName["2160p"], "h264", 3840, 2160, 5, "16:9")
		jfVideo(t, "Blade Runner's 1080p", byName["1080p"], "h264", 1920, 1080, 5, "16:9")
		if !strings.HasSuffix(byName["1080p"].Path, " - 1080p.mp4") || !strings.HasSuffix(byName["2160p"].Path, " - 2160p.mp4") {
			t.Errorf("Blade Runner's sources = %+v, want one at each file", it.MediaSources)
		}
		// a film in two folders stays two films
		a := jfFile(t, messy, "Alien (1979)/Alien (1979).mp4")
		b := jfFile(t, messy, "Alien (1979) Directors Cut/Alien (1979) Directors Cut.mp4")
		if a.Id == b.Id || a.Name != alien || b.Name != alien {
			t.Errorf("the two Alien folders are %s and %s, want two films", a.Name, b.Name)
		}
	})
	t.Run("DiscKeptWhole", func(t *testing.T) {
		// a Blu-ray kept as its BDMV tree is one film at its folder, of the
		// BluRay type, which the scan does not probe: no container, no stream
		it := jfFile(t, messy, "Cube (1997)")
		if it.Name != "Cube (1997)" || it.VideoType != jf.VideoTypeBluRay || len(it.MediaSources) != 1 {
			t.Errorf("Cube = %s, a %s with %d sources; want one BluRay film", it.Name, it.VideoType, len(it.MediaSources))
		}
		if src := &it.MediaSources[0]; src.VideoType != jf.VideoTypeBluRay || src.Container != "" || src.Path != sdkMessyMovies.Folder+"/Cube (1997)" || len(src.MediaStreams) != 0 {
			t.Errorf("Cube's source = %+v, want the unprobed BluRay folder", src)
		}
	})
	t.Run("LooseDisc", func(t *testing.T) {
		// a DVD's VOB copied in on its own is a film of that one file: MPEG-2
		// at the DVD's own size and rate, the shape read as 1.5:1 since the
		// file says nothing of it, and stereo sound untagged altogether
		it := jfFile(t, messy, "Coyote vs. Acme (2026)/VTS_01_1.VOB")
		src := &it.MediaSources[0]
		if it.VideoType != jf.VideoTypeVideoFile || src.Container != "mpeg" {
			t.Errorf("the loose VOB is a %s in a %s source, want a video file in mpeg", it.VideoType, src.Container)
		}
		jfVideo(t, "the loose VOB", src, "mpeg2video", 720, 480, 25, "1.5:1")
		jfAudio(t, "the loose VOB", src, "mp2", "", 2)
	})
	t.Run("Episodes", func(t *testing.T) {
		// the file named for two episodes holds a run, which Jellyfin takes
		// from its nfo (ending at 2) over its name (ending at 3)
		const run = "Andor (2022)/Season 01/Andor S01E02E03.mp4"
		if two := jfFile(t, episodes, run); two.ParentIndexNumber != 1 || two.IndexNumber != 2 || two.IndexNumberEnd != 2 {
			t.Errorf("Andor S01E02E03 is S%02dE%02d-%02d, want S01E02-02 from its nfo", two.ParentIndexNumber, two.IndexNumber, two.IndexNumberEnd)
		}
		for path, it := range episodes {
			if it.IndexNumberEnd != 0 && path != run {
				t.Errorf("%s ends a run at %d, and holds one episode", path, it.IndexNumberEnd)
			}
		}
		// the one Severance episode made five seconds long
		if long := jfFile(t, episodes, "Severance/Season 01/Severance S01E03.mp4"); long.RunTimeTicks != 50_000_000 {
			t.Errorf("Severance S01E03 runs %d ticks, want five seconds", long.RunTimeTicks)
		}
		// the OVA's episodes are the smallest files of all
		jfVideo(t, ".hack//Liminality's first episode", &jfFile(t, episodes, "hack Liminality (2002)/Season 01/hack Liminality S01E01.mp4").MediaSources[0], "h264", 160, 90, 5, "16:9")
	})
}
