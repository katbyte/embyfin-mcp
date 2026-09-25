//go:build integration

package integration

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/go-kt/pointer"
)

// embyFiles lists a library's items of one kind with their media sources,
// keyed by path below the library's folder ("Alien (1979)/Alien (1979).mp4",
// or the folder itself for a disc kept whole).
func embyFiles(t *testing.T, l libraryFixture, kind string) map[string]*emby.BaseItemDto {
	t.Helper()

	res := must(embyc.GetItems(t.Context(), emby.GetItemsOperationOptions{
		ParentId: embyLibrary(t, l), Recursive: new(true), IncludeItemTypes: kind, Fields: "Path,MediaSources,MediaStreams",
	})).Model
	out := map[string]*emby.BaseItemDto{}
	for i := range res.Items {
		out[strings.TrimPrefix(res.Items[i].Path, l.Folder+"/")] = &res.Items[i]
	}

	return out
}

// embyFile is the item at a path embyFiles keyed, which must be there.
func embyFile(t *testing.T, files map[string]*emby.BaseItemDto, path string) *emby.BaseItemDto {
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

// embyStreams is an item's first media source's streams of one type.
func embyStreams(it *emby.BaseItemDto, typ emby.MediaStreamType) []*emby.MediaStream {
	var out []*emby.MediaStream
	for i := range it.MediaSources[0].MediaStreams {
		if s := &it.MediaSources[0].MediaStreams[i]; s.Type == typ {
			out = append(out, s)
		}
	}

	return out
}

// embyVideo checks an item's picture: the size, codec and rate ffmpeg wrote,
// and the range Emby reads it as.
func embyVideo(t *testing.T, where string, it *emby.BaseItemDto, codec string, width, height int, fps float32, aspect string) {
	t.Helper()

	v := embyStreams(it, "Video")
	if len(v) != 1 {
		t.Errorf("%s: %d video streams, want 1: %+v", where, len(v), it.MediaSources)
		return
	}
	if v[0].Codec != codec || v[0].Width != width || v[0].Height != height || v[0].RealFrameRate != fps || v[0].AverageFrameRate != fps ||
		v[0].VideoRange != "SDR" || v[0].AspectRatio != aspect {
		t.Errorf("%s: video %s %dx%d at %v/%v fps, range %q, aspect %q; want %s %dx%d at %v fps, SDR, %s",
			where, v[0].Codec, v[0].Width, v[0].Height, v[0].RealFrameRate, v[0].AverageFrameRate, v[0].VideoRange, v[0].AspectRatio, codec, width, height, fps, aspect)
	}
}

// embyAudio checks an item's sound: the codec, and the language tag (ffmpeg's
// "und" for an untagged track, nothing for a track in a container with no
// language field at all).
func embyAudio(t *testing.T, where string, it *emby.BaseItemDto, codec, language string) {
	t.Helper()

	a := embyStreams(it, "Audio")
	if len(a) != 1 || a[0].Codec != codec || a[0].Language != language || a[0].Channels == 0 {
		t.Errorf("%s: audio %+v, want one %s stream in %q", where, a, codec, language)
	}
}

// TestEmbyMediaStreams reads what Emby says of the files themselves, the
// fields the quality, language and disc audits read, as the SDK decodes them:
// the size, codec, frame rate and range of the picture, the codec and
// language of the sound, a subtitle beside a film, a film held as two files,
// a disc kept whole and one copied in loose, and a file holding two episodes.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyMediaStreams(t *testing.T) {
	ctx := skipUnlessEmby(t)

	clean := embyFiles(t, sdkMovies, "Movie")
	messy := embyFiles(t, sdkMessyMovies, "Movie")
	episodes := embyFiles(t, sdkMessyShows, "Episode")

	t.Run("CleanFilm", func(t *testing.T) {
		it := embyFile(t, clean, "Alien (1979)/Alien (1979).mp4")
		if src := it.MediaSources[0]; src.Container != "mp4" || src.Size == 0 || src.Bitrate == 0 || it.RunTimeTicks != 10_000_000 {
			t.Errorf("the source is %s, %d bytes at %d b/s, running %d ticks; want an mp4 of one second", src.Container, src.Size, src.Bitrate, it.RunTimeTicks)
		}
		embyVideo(t, alien, it, "h264", 1280, 720, 5, "16:9")
		embyAudio(t, alien, it, "aac", "und")
		if s := embyStreams(it, "Subtitle"); len(s) != 0 {
			t.Errorf("%s has subtitles %+v, and no file beside it", alien, s)
		}
	})
	t.Run("Subtitle", func(t *testing.T) {
		// the .eng.srt beside the film is an external stream, listed last, its
		// language read from the name and given as the two-letter code
		it := embyFile(t, clean, "The Thirteenth Floor (1999)/The Thirteenth Floor (1999).mp4")
		s := embyStreams(it, "Subtitle")
		if len(s) != 1 || s[0].Codec != "srt" || s[0].Language != "en" || !pointer.From(s[0].IsExternal) || s[0].Index != 2 ||
			!strings.HasSuffix(s[0].Path, ".eng.srt") || s[0].DisplayTitle != "English (SRT)" {
			t.Errorf("%s's subtitles = %+v, want the one external English srt", thirteenthFloor, s)
		}
	})
	t.Run("LegacyCodecAndLanguage", func(t *testing.T) {
		// MPEG-4 part 2 at 360p, the sound Japanese and then English
		it := embyFile(t, messy, "Princess Mononoke (1997)/Princess Mononoke (1997).mp4")
		embyVideo(t, "the messy Princess Mononoke", it, "mpeg4", 640, 360, 5, "16:9")
		if a := embyStreams(it, "Audio"); len(a) != 2 || a[0].Language != "jpn" || a[1].Language != "eng" || a[0].Codec != "aac" || a[1].Codec != "aac" {
			t.Errorf("the messy Princess Mononoke's audio = %+v, want a Japanese aac track and then an English one", a)
		}
		// and German, as a file tags it: ger, the code the audits read as deu
		sw := embyFile(t, messy, "Star Wars Episode IV - A New Hope Despecialized Edition (1977)/Star Wars Episode IV - A New Hope Despecialized Edition (1977).mp4")
		embyAudio(t, "the Despecialized Edition", sw, "aac", "ger")
	})
	t.Run("TwoVersions", func(t *testing.T) {
		// Emby lists each file of a film's folder as a film of its own
		hd := embyFile(t, messy, "Blade Runner (1982)/Blade Runner (1982) - 1080p.mp4")
		uhd := embyFile(t, messy, "Blade Runner (1982)/Blade Runner (1982) - 2160p.mp4")
		embyVideo(t, "Blade Runner's 1080p", hd, "h264", 1920, 1080, 24, "16:9")
		// the upscale: HEVC 10-bit at 60 frames a second, which Emby reads as
		// HDR10 from its colour tags and names "HDR 10"
		if v := embyStreams(uhd, "Video"); len(v) != 1 || v[0].Codec != "hevc" || v[0].Width != 3840 || v[0].Height != 2160 || v[0].AverageFrameRate != 60 || v[0].BitDepth != 10 ||
			v[0].VideoRange != "HDR 10" || v[0].ExtendedVideoType != "Hdr10" || v[0].ColorTransfer != "smpte2084" || v[0].ColorPrimaries != "bt2020" {
			t.Errorf("Blade Runner's 2160p = %+v, want 10-bit HEVC at 60 fps, HDR10 by its colour tags", v)
		}
		if hd.Id == uhd.Id || hd.Name != bladeRunner || uhd.Name != bladeRunner {
			t.Errorf("the two files are %s (%s) and %s (%s), want two films named %s", hd.Name, hd.Id, uhd.Name, uhd.Id, bladeRunner)
		}
		// and folds them into one in a user's read of either, a source each
		for _, id := range []string{hd.Id, uhd.Id} {
			user := must(embyc.GetUsersByUserIdItemsById(ctx, adminID, id)).Model
			var heights []int
			for i := range user.MediaSources {
				for j := range user.MediaSources[i].MediaStreams {
					if s := &user.MediaSources[i].MediaStreams[j]; s.Type == "Video" {
						heights = append(heights, s.Height)
					}
				}
			}
			slices.Sort(heights)
			if len(user.MediaSources) != 2 || !slices.Equal(heights, []int{1080, 2160}) {
				t.Errorf("a user's read of %s lists %d sources of heights %v, want the 1080p and the 2160p", id, len(user.MediaSources), heights)
			}
		}
		// a film in two folders stays two films
		a := embyFile(t, messy, "Alien (1979)/Alien (1979).mp4")
		b := embyFile(t, messy, "Alien (1979) Directors Cut/Alien (1979) Directors Cut.mp4")
		if a.Id == b.Id || a.Name != alien || b.Name != alien {
			t.Errorf("the two Alien folders are %s and %s, want two films", a.Name, b.Name)
		}
	})
	t.Run("DiscKeptWhole", func(t *testing.T) {
		// a Blu-ray kept as its BDMV tree is one film at its folder, which the
		// scan does not probe: no stream is known
		it := embyFile(t, messy, "Cube (1997)")
		if src := it.MediaSources[0]; it.Name != "Cube" || len(it.MediaSources) != 1 || src.Container != "bluray" || src.Path != sdkMessyMovies.Folder+"/Cube (1997)" {
			t.Errorf("Cube = %s, sources %+v; want one bluray source at its folder", it.Name, it.MediaSources)
		}
		if n := len(it.MediaSources[0].MediaStreams); n != 0 {
			t.Errorf("Cube lists %d streams; the disc was never probed until now", n)
		}
		// and a DVD kept as its VIDEO_TS tree the same, named by its nfo
		moon := embyFile(t, messy, "Moon (2009)")
		if src := moon.MediaSources[0]; moon.Name != "Moon" || len(moon.MediaSources) != 1 || src.Container != "dvd" || src.Path != sdkMessyMovies.Folder+"/Moon (2009)" || len(src.MediaStreams) != 0 {
			t.Errorf("Moon = %s, sources %+v; want one unprobed dvd source at its folder", moon.Name, moon.MediaSources)
		}
	})
	t.Run("LooseDisc", func(t *testing.T) {
		// a DVD's VOB copied in on its own is a film of that one file: MPEG-2
		// at the DVD's own size and rate, the shape read as 1.5:1 since the
		// file says nothing of it, and the sound untagged altogether
		it := embyFile(t, messy, "Coyote vs. Acme (2026)/VTS_01_1.VOB")
		if it.MediaSources[0].Container != "mpeg" {
			t.Errorf("the loose VOB is a %s source, want mpeg", it.MediaSources[0].Container)
		}
		embyVideo(t, "the loose VOB", it, "mpeg2video", 720, 480, 25, "1.5:1")
		embyAudio(t, "the loose VOB", it, "mp2", "")
	})
	t.Run("Episodes", func(t *testing.T) {
		// the file named for two episodes holds a run, read from its name
		// (its nfo, which ends the run at 2, Emby does not read for that)
		const run = "Andor (2022)/Season 01/Andor S01E02E03.mp4"
		if two := embyFile(t, episodes, run); two.ParentIndexNumber != 1 || two.IndexNumber != 2 || two.IndexNumberEnd != 3 {
			t.Errorf("Andor S01E02E03 is S%02dE%02d-%02d, want S01E02-03", two.ParentIndexNumber, two.IndexNumber, two.IndexNumberEnd)
		}
		for path, it := range episodes {
			if it.IndexNumberEnd != 0 && path != run {
				t.Errorf("%s ends a run at %d, and holds one episode", path, it.IndexNumberEnd)
			}
		}
		// the one Severance episode made five seconds long
		if long := embyFile(t, episodes, "Severance/Season 01/Severance S01E03.mp4"); long.RunTimeTicks != 50_000_000 {
			t.Errorf("Severance S01E03 runs %d ticks, want five seconds", long.RunTimeTicks)
		}
		// the OVA's episodes are the smallest files of all
		embyVideo(t, ".hack//Liminality's first episode", embyFile(t, episodes, "hack Liminality (2002)/Season 01/hack Liminality S01E01.mp4"), "h264", 160, 90, 5, "16:9")
		// a DVD rip kept anamorphic: 720x480 stated 16:9
		rip := embyFile(t, episodes, "Severance/Season 01/Severance S01E02.mp4")
		embyVideo(t, "the anamorphic Severance S01E02", rip, "h264", 720, 480, 5, "16:9")
		if v := embyStreams(rip, "Video"); len(v) != 1 || !pointer.From(v[0].IsAnamorphic) {
			t.Errorf("the anamorphic Severance S01E02 = %+v, want it read as anamorphic", v)
		}
		// a second of video whose duration claims twelve hours
		if broken := embyFile(t, episodes, "Star Trek Deep Space Nine (1993)/Season 03/Star Trek Deep Space Nine S03E01.mkv"); broken.RunTimeTicks < 12*60*60*10_000_000 {
			t.Errorf("Deep Space Nine S03E01 runs %d ticks, want the twelve hours its duration claims", broken.RunTimeTicks)
		}
		// and the featurette in a season's Extras folder, which Emby takes for
		// an episode of the show with no number
		if extra := embyFile(t, episodes, "Severance/Season 01/Extras/Featurette.mp4"); extra.SeriesName != severance || extra.IndexNumber != 0 {
			t.Errorf("the season's featurette = %s of %s, number %d; want an episode of %s with none", extra.Name, extra.SeriesName, extra.IndexNumber, severance)
		}
	})
}
