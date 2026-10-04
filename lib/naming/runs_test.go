package naming

import (
	"slices"
	"testing"
)

// Every way a name spells a run of episodes reads as the run, the marker is
// kept as written, and its style is the shape the servers are judged by.
func TestParseReleaseReadsEveryRunStyle(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		season      int
		first, last int
		run, style  string
	}{
		{"Zzyzx - S02E01-E02 - Alpha.mkv", 2, 1, 2, "S02E01-E02", "SNNENN-ENN"},
		{"Zzyzx.S02E03E04.1080p.WEB-DL.mkv", 2, 3, 4, "S02E03E04", "SNNENNENN"},
		{"Zzyzx - S02E05-06 - Alpha.mkv", 2, 5, 6, "S02E05-06", "SNNENN-NN"},
		{"Zzyzx - S02E07+E08 - Alpha.mkv", 2, 7, 8, "S02E07+E08", "SNNENN+ENN"},
		{"Zzyzx - S02E07+08 - Alpha.mkv", 2, 7, 8, "S02E07+08", "SNNENN+NN"},
		{"Zzyzx.S02E09.S02E10.1080p.mkv", 2, 9, 10, "S02E09.S02E10", "SNNENN.SNNENN"},
		{"Zzyzx - 02x11-12 - Alpha.mkv", 2, 11, 12, "02x11-12", "NNxNN-NN"},
		{"Zzyzx - 02x13+14 - Alpha.mkv", 2, 13, 14, "02x13+14", "NNxNN+NN"},
		{"Zzyzx - 02x15x16 - Alpha.mkv", 2, 15, 16, "02x15x16", "NNxNNxNN"},
		{"Zzyzx - 02x17-x18 - Alpha.mkv", 2, 17, 18, "02x17-x18", "NNxNN-xNN"},
		{"Zzyzx - 02x19 - 02x20 - Alpha.mkv", 2, 19, 20, "02x19 - 02x20", "NNxNN - NNxNN"},
		{"Zzyzx - 02x21 02x22 - Alpha.mkv", 2, 21, 22, "02x21 02x22", "NNxNN NNxNN"}, //nolint:dupword // the marker twice
		{"Zzyzx - S02x23.S02x24 - Alpha.mkv", 2, 23, 24, "S02x23.S02x24", "SNNxNN.SNNxNN"},
		{"Zzyzx - S02E25 - E26 - Alpha.mkv", 2, 25, 26, "S02E25 - E26", "SNNENN - ENN"},
		{"Zzyzx - S02xE27xE28 - Alpha.mkv", 2, 27, 28, "S02xE27xE28", "SNNxENNxENN"},
		{"Zzyzx - 02x29-02x30 - Alpha.mkv", 2, 29, 30, "02x29-02x30", "NNxNN-NNxNN"},
		{"Zzyzx - 02x31 - x32 - Alpha.mkv", 2, 31, 32, "02x31 - x32", "NNxNN - xNN"},
		{"Zzyzx - S02E33-X34 - Alpha.mkv", 2, 33, 34, "S02E33-X34", "SNNENN-xNN"},
		{"Zzyzx - S02x35x36 - Alpha.mkv", 2, 35, 36, "S02x35x36", "SNNxNNxNN"},
		{"Zzyzx - S02E37 - S02E38 - Alpha.mkv", 2, 37, 38, "S02E37 - S02E38", "SNNENN - SNNENN"},
		{"Zzyzx - S02.E39-E40 - Alpha.mkv", 2, 39, 40, "S02.E39-E40", "SNN.ENN-ENN"},
		// a range: everything from the first to the last
		{"Zzyzx - 01x02 - 01x05 - Alpha.mkv", 1, 2, 5, "01x02 - 01x05", "NNxNN - NNxNN"},
		{"Zzyzx - S01E01-E02-E03 - Alpha.mkv", 1, 1, 3, "S01E01-E02-E03", "SNNENN-ENN"},
		// one episode: no run, no style
		{"Zzyzx - S02E01 - Alpha.mkv", 2, 1, 0, "S02E01", ""},
		{"Zzyzx - 02x01 - Alpha.mkv", 2, 1, 0, "02x01", ""},
		{"Zzyzx - S02x01 - Alpha.mkv", 2, 1, 0, "S02x01", ""},
	} {
		got := ParseRelease(tc.name)
		if got.Season != tc.season || got.Episode != tc.first || got.EpisodeEnd != tc.last {
			t.Errorf("%s: read S%02dE%02d-%02d, want S%02dE%02d-%02d", tc.name, got.Season, got.Episode, got.EpisodeEnd, tc.season, tc.first, tc.last)
		}
		if got.Run != tc.run || got.RunStyle() != tc.style {
			t.Errorf("%s: run %q style %q, want %q %q", tc.name, got.Run, got.RunStyle(), tc.run, tc.style)
		}
		if got.Title != "Zzyzx" {
			t.Errorf("%s: title %q, want Zzyzx", tc.name, got.Title)
		}
	}
}

// What is not a run: a number after a spaced dash is a title's, an encode
// stuck to the marker is not 262 episodes, a run into another season has no
// last episode, and a resolution after the hyphen is what it always was.
func TestParseReleaseReadsNoRunWhereThereIsNone(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		first       int
		title, rest string
	}{
		{"Zzyzx - S01E01 - 24 Hours.mkv", 1, "Zzyzx", "S01E01"},
		{"Zzyzx - 01x01 - 24 Hours.mkv", 1, "Zzyzx", "01x01"},
		{"Zzyzx.1x02x264-GRP.mkv", 2, "Zzyzx", "1x02x264"},
		{"Zzyzx.S01E10.S02E01.1080p.mkv", 10, "Zzyzx", "S01E10.S02E01"},
		{"Zzyzx.S02E01-1080p.WEB.H264-GROUP.mkv", 1, "Zzyzx", "S02E01-"},
		{"Zzyzx.S02E01-720p.mkv", 1, "Zzyzx", "S02E01-"},
	} {
		got := ParseRelease(tc.name)
		if got.Episode != tc.first || got.EpisodeEnd != 0 || got.Title != tc.title {
			t.Errorf("%s: read %q S%02dE%02d-%02d, want %q E%02d and no run", tc.name, got.Title, got.Season, got.Episode, got.EpisodeEnd, tc.title, tc.first)
		}
		if got.RunStyle() != "" {
			t.Errorf("%s: style %q, want none", tc.name, got.RunStyle())
		}
	}
}

// The table says, for every style a name can spell, whether each server
// reads it, or that it has not been checked; and every style the table
// names is one the parser gives.
func TestRunStylesRead(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		style, server string
		read, known   bool
	}{
		{"SNNENN-ENN", ServerEmby, true, true},
		{"SNNENN-ENN", ServerJellyfin, true, true},
		{"NNxNN+NN", ServerJellyfin, false, true},
		{"SNNxENNxENN", ServerEmby, true, true},
		{"SNNxENNxENN", ServerJellyfin, false, true},
		{"", ServerEmby, false, false},
		{"NNxNN-NN", "plex", false, false},
	} {
		read, known := RunStyleRead(tc.style, tc.server)
		if read != tc.read || known != tc.known {
			t.Errorf("%s on %s = read %v known %v, want %v %v", tc.style, tc.server, read, known, tc.read, tc.known)
		}
	}

	styles := RunStyles()
	for _, name := range []string{"S02E01-E02", "S02E03E04", "S02E05-06", "S02E07+E08", "S02E07+08", "S02E09.S02E10", "02x11-12", "02x13+14", "02x15x16", "02x17-x18", "02x19 - 02x20", "02x21 02x22", "S02x23.S02x24", "S02E25 - E26", "S02xE27xE28", "02x29-02x30", "02x31 - x32", "S02E33-X34", "S02x35x36", "S02E37 - S02E38", "S02.E39-E40"} {
		if style := ParseRelease("Zzyzx - " + name + " - Alpha.mkv").RunStyle(); !slices.Contains(styles, style) {
			t.Errorf("%s reads as %q, which the table does not name", name, style)
		}
	}
}
