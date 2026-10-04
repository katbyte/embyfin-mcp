package naming

import (
	"strings"
	"testing"
)

// C7, guarding A6: the golden corpus. Real release names, in the shapes the
// folder that exposed this holds them - dots for spaces, a title made of
// hyphens and digits, "and" against "&", an apostrophe the scene drops, a
// year in brackets - against what each should be read as. A parser is only
// as good as the names it has met, so this list grows rather than changes.
func TestParseRelease(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		title   string
		year    int
		season  int
		episode int
		end     int
	}{
		// dotted, with the encode and group after the episode
		{"24.Hours.in.A.and.E.S36E03.1080p.HDTV.H264-DARKFLiX", "24 Hours in A and E", 0, 36, 3, 0},
		// a title made of hyphens and digits, spaces in the tail
		{"9-1-1-S09E16 HDTV X264-NGP", "9-1-1", 0, 9, 16, 0},
		// a season pack, with an apostrophe the release name drops
		{"A.Gatherers.Adventure.in.Isekai.S01.1080p.CR.WEB-DL", "A Gatherers Adventure in Isekai", 0, 1, 0, 0},
		// a year in brackets and the encode in brackets after it
		{"1600 Penn (2012) S01 (1080p AMZN WEB-DL x265 10bit)", "1600 Penn", 2012, 1, 0, 0},
		// a bare year between title and season
		{"Some.Show.2019.S02E05.1080p.WEB.H264-GROUP", "Some Show", 2019, 2, 5, 0},
		// one file holding two episodes
		{"The.Expanse.S01E01E02.1080p.BluRay.x265-RARBG", "The Expanse", 0, 1, 1, 2},
		// the old numbering
		{"Breaking.Bad.1x03.720p.HDTV.x264", "Breaking Bad", 0, 1, 3, 0},
		// no season at all: the encode's words end the title
		{"Chernobyl.2019.1080p.AMZN.WEB-DL.DDP5.1.H.264", "Chernobyl", 2019, 0, 0, 0},
		// a file name rather than a folder name
		{"Severance.S02E07.1080p.ATVP.WEB-DL.mkv", "Severance", 0, 2, 7, 0},
		// a plain title, which is what a person types
		{"Abbott Elementary", "Abbott Elementary", 0, 0, 0, 0},
		// a title ending in a year and the release year after it: the last
		// year dates the release, the one before it is the title's
		{"Blade.Runner.2049.2017.1080p.BluRay.x264-GRP", "Blade Runner 2049", 2017, 0, 0, 0},
		{"Zzyzx.1984.2020.1080p", "Zzyzx 1984", 2020, 0, 0, 0},
		// a year after the encode's first word is none of the release's,
		// and a year after an edition's words is a re-release's: the first
		// year the title leaves dates the release
		{"Zzyzx.Race.2000.1080p.x264-2023", "Zzyzx Race", 2000, 0, 0, 0},
		// (a year later than next year dates nothing, 2049 among them)
		{"Blade.Runner.2049.2160p.Remux-2023", "Blade Runner 2049", 0, 0, 0, 0},
		{"Zzyzx.Title.2010.1080p.2160p-2023-remux", "Zzyzx Title", 2010, 0, 0, 0},
		{"Zzyzx.2015.Extended.2016.Re-Edit", "Zzyzx", 2015, 0, 0, 0},
		{"Zzyzx.1982.Remastered.2021", "Zzyzx", 1982, 0, 0, 0},
		{"Blade.Runner.1982.The.Final.Cut.2007", "Blade Runner", 1982, 0, 0, 0},
		{"Zzyzx.1979.Directors.Cut.2003", "Zzyzx", 1979, 0, 0, 0},
		// an edition's words that would leave nothing, or an article, are
		// the title
		{"The.Final.Cut.2004", "The Final Cut", 2004, 0, 0, 0},
		{"A.Directors.Cut.2020", "A Directors Cut", 2020, 0, 0, 0},
		// an edition's words end the title, not the search for its year
		{"Zzyzx.Theatrical.Cut.1999", "Zzyzx", 1999, 0, 0, 0},
		{"Zzyzx.Directors.Cut.1999", "Zzyzx", 1999, 0, 0, 0},
		{"Zzyzx.Unrated.2009", "Zzyzx", 2009, 0, 0, 0},
		{"Zzyzx.Remastered.1982", "Zzyzx", 1982, 0, 0, 0},
		{"Zzyzx.Extended.2010", "Zzyzx", 2010, 0, 0, 0},
		// an encode word joined to another by a hyphen ends the title too
		{"Zzyzx Movie Remux-2160p", "Zzyzx Movie", 0, 0, 0, 0},
		{"Zzyzx.Movie.2010.WEBDL-1080p", "Zzyzx Movie", 2010, 0, 0, 0},
		// an edition's 3D after an encode word stays the title's
		{"Zzyzx.IMAX.3D.2010.1080p", "Zzyzx 3D", 2010, 0, 0, 0},
		// titles made with hyphens and pluses are left whole
		{"Spider-Man.2002.1080p.BluRay.x264", "Spider-Man", 2002, 0, 0, 0},
		{"X-Men.2000.1080p.WEB-DL", "X-Men", 2000, 0, 0, 0},
		{"Ant-Man.2015.2160p.WEB-DL.DDP5.1", "Ant-Man", 2015, 0, 0, 0},
		{"9-1-1.S01E01.1080p.WEB", "9-1-1", 0, 1, 1, 0},
		{"Paramount+ Zzyzx.S01E01.1080p.WEB", "Paramount+ Zzyzx", 0, 1, 1, 0},

		// and the same shows once FileBot has renamed them, where the answer
		// is spread across the path: the folder names the show and the year,
		// the file numbers the episode
		{"Severance - S01E01 - Good News About Hell.mkv", "Severance", 0, 1, 1, 0},
		{"Severance (2022) - S01E01 - Good News About Hell.mkv", "Severance", 2022, 1, 1, 0},
		{"Severance/Season 01/Severance - S01E01 - Good News About Hell.mkv", "Severance", 0, 1, 1, 0},
		{"TV/Severance (2022)/Season 02/Severance - S02E07 - Chikhai Bardo.mkv", "Severance", 2022, 2, 7, 0},
		{"Severance (2022)/Season 01/S01E01.mkv", "Severance", 2022, 1, 1, 0},
		// a season folder on its own still names the show and the season
		{"Severance/Season 01", "Severance", 0, 1, 0, 0},
		// FileBot keeps the ampersand and writes a colon as a hyphen
		{"24 Hours in A&E - S36E03 - Episode 3.mkv", "24 Hours in A&E", 0, 36, 3, 0},
		{"Law & Order- Special Victims Unit - S01E01 - Payback.mkv", "Law & Order- Special Victims Unit", 0, 1, 1, 0},
		{"9-1-1/Season 09/9-1-1 - S09E16 - Ashes, Ashes.mkv", "9-1-1", 0, 9, 16, 0},
		// and its spelling of a file holding two episodes
		{"The Expanse - S01E01-E02 - Dulcinea.mkv", "The Expanse", 0, 1, 1, 2},

		// a slash alone does not make a path: titles have them. Read as paths
		// these were "Vicious", "Tuck" and "20", and "Vicious" then matched a
		// different show at 1.0
		{"Sweet/Vicious", "Sweet/Vicious", 0, 0, 0, 0},
		{"Nip/Tuck", "Nip/Tuck", 0, 0, 0, 0},
		{"20/20", "20/20", 0, 0, 0, 0},
		{"Nip/Tuck (2003)", "Nip/Tuck", 2003, 0, 0, 0},
		{"Sweet/Vicious S01E01 1080p WEB H264-GROUP", "Sweet/Vicious", 0, 1, 1, 0},
		{"Nip/Tuck - S02E03 - Manny Skerritt", "Nip/Tuck", 0, 2, 3, 0},
		// and a path is still read as one, by what no title carries: a
		// season folder, a leading separator, a drive, a file extension, or a
		// show's folder named with its year
		{"Severance (2022)/Season 02/Severance - S02E07 - Chikhai Bardo.mkv", "Severance", 2022, 2, 7, 0},
		{"/tv/Nip Tuck (2003)/Season 01/Nip Tuck - S01E01.mkv", "Nip Tuck", 2003, 1, 1, 0},
		{`D:\TV\Severance (2022)\Season 01\Severance - S01E01 - Good News About Hell.mkv`, "Severance", 2022, 1, 1, 0},
		{"Severance (2022)/S01E01", "Severance", 2022, 1, 1, 0},
		{"Severance/Specials/Severance - S00E01 - Lumon Orientation.mkv", "Severance", 0, 0, 1, 0},
		{"Sweet Vicious/Sweet Vicious - S01E01.mkv", "Sweet Vicious", 0, 1, 1, 0},

		// every way a file says it holds a run of episodes, to its last
		{"The.Expanse.S01E01E02E03.1080p.BluRay.x265-RARBG", "The Expanse", 0, 1, 1, 3},
		{"The Expanse - S01E01-02-03 - Dulcinea.mkv", "The Expanse", 0, 1, 1, 3},
		{"The Expanse - S01E01-E02-E03 - Dulcinea.mkv", "The Expanse", 0, 1, 1, 3},
		{"The.Expanse.S01E01.S01E02.1080p.WEB.H264-GROUP", "The Expanse", 0, 1, 1, 2},
		// a run cannot cross into another season and still have one end
		{"The.Expanse.S01E10.S02E01.1080p.WEB.H264-GROUP", "The Expanse", 0, 1, 10, 0},
		// and the encode after a marker is still not an episode
		{"The.Expanse.S02E01-1080p.WEB.H264-GROUP", "The Expanse", 0, 2, 1, 0},

		// titles in other scripts are titles
		{"千と千尋の神隠し (2001)", "千と千尋の神隠し", 2001, 0, 0, 0},
		{"進撃の巨人 S01E01 1080p WEB H264-GROUP", "進撃の巨人", 0, 1, 1, 0},
		{"Слово.пацана.S01E03.1080p.WEB-DL.H264-GROUP", "Слово пацана", 0, 1, 3, 0},
		{"Το.Νησί.S01E01.720p.HDTV.x264-GROUP", "Το Νησί", 0, 1, 1, 0},
	} {
		got := ParseRelease(tc.name)
		if got.Title != tc.title || got.Year != tc.year || got.Season != tc.season || got.Episode != tc.episode || got.EpisodeEnd != tc.end {
			t.Errorf("parseRelease(%q) =\n  %+v\nwant title %q year %d S%02dE%02d end %d", tc.name, got, tc.title, tc.year, tc.season, tc.episode, tc.end)
		}
	}
}

// Two spellings of the same show fold together; two different shows do not.
func TestTitleScore(t *testing.T) {
	t.Parallel()

	same := [][2]string{
		{"24 Hours in A and E", "24 Hours in A&E"},
		{"A Gatherers Adventure in Isekai", "A Gatherer's Adventure in Isekai"},
		{"9-1-1", "9 1 1"},
		{"Marvels Agents of S H I E L D", "Marvel's Agents of S.H.I.E.L.D."},
		{"law and order svu", "Law & Order: SVU"},
		// FileBot writes a colon as a hyphen
		{"Law & Order- Special Victims Unit", "Law & Order: Special Victims Unit"},
	}
	for _, pair := range same {
		if score, how := Score(pair[0], pair[1]); score < 1 {
			t.Errorf("titleScore(%q, %q) = %.2f (%s), want a certain match", pair[0], pair[1], score, how)
		}
	}

	// a prefix is a candidate, not an answer
	if score, _ := Score("Doctor Who", "Doctor Who Confidential"); score >= 0.95 || score < 0.5 {
		t.Errorf("a prefix scored %.2f, want a candidate rather than a certainty", score)
	}
	// and unrelated titles do not match at all
	if score, _ := Score("Severance", "Succession"); score > 0 {
		t.Errorf("unrelated titles scored %.2f", score)
	}
	// an article is not a difference worth refusing over
	if score, _ := Score("Office", "The Office"); score < 0.9 {
		t.Errorf("an article cost %.2f", score)
	}
}

// A title in another script is a title. Folding to a-z and 0-9 left nothing of
// one, and nothing scores 0 even against itself: 千と千尋の神隠し could not be
// found by its own name, nor any Russian or Greek series by any name.
func TestTitleScoreReadsEveryScript(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ release, library string }{
		{"千と千尋の神隠し", "千と千尋の神隠し"},
		{"進撃の巨人", "進撃の巨人"},
		{"Слово пацана", "СЛОВО ПАЦАНА"},
		// Russian writes ё as е as often as not
		{"Елки", "Ёлки"},
		// Greek drops its accents in capitals, and ends a word in ς
		{"ΟΔΥΣΣΕΙΑ", "Οδύσσεια"},
		{"Ο ΘΕΟΣ", "Ο Θεός"},
		// an accent written as a mark of its own after its letter
		{"Pokemon", "Pokémon"},
	} {
		if score, how := Score(tc.release, tc.library); score != 1 {
			t.Errorf("%q against %q scored %v (%s), want 1", tc.release, tc.library, score, how)
		}
	}

	// and different titles in them are still different
	for _, tc := range []struct{ a, b string }{
		{"千と千尋の神隠し", "もののけ姫"},
		{"Слово пацана", "Мастер и Маргарита"},
		{"Το Νησί", "Η Ζωή Αλλιώς"},
	} {
		if score, _ := Score(tc.a, tc.b); score > 0 {
			t.Errorf("%q against %q scored %v", tc.a, tc.b, score)
		}
	}
	// a word shared is still a word in common
	if score, _ := Score("Слово пацана", "Слово пацана. Кровь на асфальте"); score <= 0 || score >= 1 {
		t.Errorf("a longer Russian title scored %v, want a candidate", score)
	}
}

// A year later than next year dates no release: it is the title's own, or
// a group's number. Read as of 2026, a 2049 or a 2077 is part of the title.
func TestParseReleaseTakesNoYearFromTheFuture(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, title string
		year        int
		season      int
		episode     int
	}{
		{"Blade.Runner.2049.2160p.Remux-2023", "Blade Runner 2049", 0, 0, 0},
		{"Zzyzx.2077.S01E01.1080p", "Zzyzx 2077", 0, 1, 1},
		{"Zzyzx.2027.1080p", "Zzyzx", 2027, 0, 0},
		{"Zzyzx.2028.1080p", "Zzyzx 2028", 0, 0, 0},
	} {
		got := ParseSegmentAsOf(tc.name, 2027)
		if got.Title != tc.title || got.Year != tc.year || got.Season != tc.season || got.Episode != tc.episode {
			t.Errorf("%s as of 2026 = %+v, want %q %d S%02dE%02d", tc.name, got, tc.title, tc.year, tc.season, tc.episode)
		}
	}
}

// The parser never hands back an empty title, whatever it is given: a caller
// that gets "" would search the whole library.
func TestParseReleaseAlwaysNamesSomething(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"S01E01", "1080p.WEB-DL", "...", "-", "2019"} {
		if got := ParseRelease(name); strings.TrimSpace(got.Title) == "" {
			t.Errorf("parseRelease(%q) read no title at all: %+v", name, got)
		}
	}
	// and the season still comes out of a bare marker
	if got := ParseRelease("S01E01"); got.Season != 1 || got.Episode != 1 {
		t.Errorf("parseRelease(\"S01E01\") = %+v", got)
	}
}

// The shapes release names arrive in, named one at a time so a failure says
// which rule broke. The names are made up; the shapes are the ones that broke
// the parser.
func TestParseReleaseShapes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		release       string
		title         string
		season, first int
		last          int
	}{
		{"site prefix, spaced dash", "www.example.org    -    The Long Drive 2016 S07E02 Episode Title 1080p WEB-DL DDP5 1 H 264-GROUP", "The Long Drive", 7, 2, 0},
		{"site prefix over a dotted name", "www.example.org    -    The.Blue.Yellow.Show.S02E19.Episode.Title.1080p.WEB.H264-GROUP", "The Blue Yellow Show", 2, 19, 0},
		{"a bare host with a spaced dash", "example.net - Some.Show.S01E01.720p.HDTV.x264-GROUP", "Some Show", 1, 1, 0},
		{"a title is not a host", "The.Night.Office.S02E03.RERIP.MULTI.1080p.WEB.H264-GROUP", "The Night Office", 2, 3, 0},
		{"hyphen-numeric title", "www.example.org    -    9-1-1 S09E12 Episode Title REPACK 1080p WEB-DL DD 5 1 H 264-GROUP", "9-1-1", 9, 12, 0},
		{"year in the title", "www.example.org    -    Some Remake 2024 S02E03 Episode Title 2160p WEB-DL DDP5 1 Atmos H 265-GROUP", "Some Remake", 2, 3, 0},
		{"a run of episodes, spelled out", "www.example.org    -    Some Cartoon S06E01-E02 576p WEB-DL AAC2 0 H 264 DUAL-GROUP", "Some Cartoon", 6, 1, 2},
		{"a run of episodes, bare", "Some.Drama.S02E01-05.2160p.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-GROUP", "Some Drama", 2, 1, 5},
		{"a season pack numbers no episode", "Some.Anime.S01.REPACK.1080p.BluRay.Dual-Audio.AAC2.0.x265-GROUP", "Some Anime", 1, 0, 0},
		{"a title whose first word names a codec", "Max.Headroom.S01E01.1080p.WEB.H264-GROUP", "Max Headroom", 1, 1, 0},
		{"a title whose first word names a service", "Stan.Against.Evil.S02E04.720p.HDTV.x264-GROUP", "Stan Against Evil", 2, 4, 0},
		{"junk still ends a title with no marker", "Web Therapy 1080p WEB-DL", "Web Therapy", 0, 0, 0},
		{"a single letter is junk only in front of a number", "Marvels Agents of S H I E L D H 264-GROUP", "Marvels Agents of S H I E L D", 0, 0, 0},
		{"and still is in front of one", "Some Show H 264-GROUP", "Some Show", 0, 0, 0},
	} {
		got := ParseRelease(tc.release)
		if !strings.EqualFold(Normalise(got.Title), Normalise(tc.title)) {
			t.Errorf("%s: read %q, want %q", tc.name, got.Title, tc.title)
		}
		if got.Season != tc.season || got.Episode != tc.first || got.EpisodeEnd != tc.last {
			t.Errorf("%s: read S%02dE%02d-%02d, want S%02dE%02d-%02d", tc.name, got.Season, got.Episode, got.EpisodeEnd, tc.season, tc.first, tc.last)
		}
	}
}

// Accents fold the way apostrophes and colons do, and for the same reason:
// scene naming drops them, and Emby's own search drops them too - a search for
// "90 Day Fiance" finds "90 Day Fiancé". A scorer that kept them would rank the
// very series the search just found at 0.5 and refuse to commit to it.
func TestTitleScoreFoldsAccents(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ release, library string }{
		{"90 Day Fiance", "90 Day Fiancé"},
		{"Pokemon", "Pokémon"},
		{"Amelie", "Amélie"},
		{"Los Companeros", "Los Compañeros"},
		{"Die Brucke", "Die Brücke"},
		{"Bjorn of the North", "Bjørn of the North"},
		{"Strasse", "Straße"},
	} {
		if score, _ := Score(tc.release, tc.library); score != 1 {
			t.Errorf("%q against %q scored %v, want 1: the accent is the only difference", tc.release, tc.library, score)
		}
	}

	// folding the marks off must not fold two different shows together
	if score, _ := Score("Fiance", "Finance"); score == 1 {
		t.Error("Fiance and Finance are not the same show")
	}
}

// A dotted acronym is a whole genre of title, and it broke worse than the
// apostrophes did. "Chicago P.D." against a search for "Chicago PD" did not
// just score low: the punctuation strip turned P.D. into two words, so it
// scored BELOW Chicago Fire, Hope, Justice and Med, which at least share a
// whole word. The right answer ranked under four wrong ones.
func TestTitleScoreFoldsDottedAcronyms(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ release, library string }{
		{"Chicago PD", "Chicago P.D."},
		{"CSI Miami", "C.S.I. Miami"},
		{"SWAT", "S.W.A.T."},
		{"Marvels Agents of SHIELD", "Marvel's Agents of S.H.I.E.L.D."},
		{"MASH", "M.A.S.H."},
		{"The IT Crowd", "The I.T. Crowd"},
	} {
		if score, _ := Score(tc.release, tc.library); score != 1 {
			t.Errorf("%q against %q scored %v, want 1: the points are the only difference", tc.release, tc.library, score)
		}
	}

	// and it has to beat the shows it was losing to
	pd, _ := Score("Chicago PD", "Chicago P.D.")
	for _, other := range []string{"Chicago Fire", "Chicago Med", "Chicago Justice", "Alaska PD"} {
		if score, _ := Score("Chicago PD", other); score >= pd {
			t.Errorf("Chicago PD scores %v against %q and %v against Chicago P.D.", score, other, pd)
		}
	}

	// a word that merely ends in a point is not an acronym
	if score, _ := Score("Dr Who", "Dr. Who"); score != 1 {
		t.Errorf("Dr. Who: %v", score)
	}

	// the release name spells the same acronym with its dots already turned
	// into spaces, and that has to land on the same word
	for _, spelling := range []string{"Marvels.Agents.of.S.H.I.E.L.D", "Marvels Agents of S H I E L D", "Marvels Agents of SHIELD"} {
		if score, _ := Score(ParseRelease(spelling).Title, "Marvel's Agents of S.H.I.E.L.D."); score != 1 {
			t.Errorf("%q scored %v", spelling, score)
		}
	}

	// single letters that are words in their own right are not an acronym:
	// the ampersand in "A&E" becomes "and", which breaks the run
	if score, _ := Score("24 Hours in A and E", "24 Hours in A&E"); score != 1 {
		t.Errorf("24 Hours in A&E: %v", score)
	}
}

// A prefix match is two different facts pointing opposite ways, and reading
// them as one is how a spin-off resolves to its parent. "Law and Order SVU"
// scored 0.91 against "Law & Order" - over the floor, acted on unattended -
// and the library was holding Special Victims Unit all along.
func TestTitleScoreReadsPrefixesDirectionally(t *testing.T) {
	t.Parallel()

	// the name carries words the library's title does not: those words are
	// what says which show it is, so this cannot be acted on
	for _, tc := range []struct{ release, library string }{
		{"Law and Order SVU", "Law & Order"},
		{"CSI Miami", "CSI"},
		{"Star Trek Deep Space Nine", "Star Trek"},
	} {
		score, how := Score(tc.release, tc.library)
		if score >= Confident {
			t.Errorf("%q against %q scored %v (%s): a spin-off must not resolve to its parent", tc.release, tc.library, score, how)
		}
	}

	// the other way round is the Doctor Who Confidential case, which is worth
	// something but still not a certainty
	score, _ := Score("Doctor Who", "Doctor Who Confidential")
	if score < 0.8 || score >= 1 {
		t.Errorf("Doctor Who against its spin-off scored %v", score)
	}
}

// An abbreviation is usually the only thing in a name saying WHICH show it
// is, so it has to be read rather than dropped.
func TestTitleScoreReadsAcronyms(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ release, library string }{
		{"Law and Order SVU", "Law & Order: Special Victims Unit"},
		{"CSI NY", "CSI: New York"},
		// and the same in reverse, when the name spells out what the library
		// abbreviates
		{"Law and Order Special Victims Unit", "Law & Order SVU"},
	} {
		score, how := Score(tc.release, tc.library)
		if score < Confident {
			t.Errorf("%q against %q scored %v (%s)", tc.release, tc.library, score, how)
		}
	}

	// the acronym has to be the right one
	if score, _ := Score("Law and Order SVU", "Law & Order: Criminal Intent"); score >= Confident {
		t.Errorf("SVU matched Criminal Intent at %v", score)
	}

	// and SVU must beat the parent it was losing to
	svu, _ := Score("Law and Order SVU", "Law & Order: Special Victims Unit")
	parent, _ := Score("Law and Order SVU", "Law & Order")
	if svu <= parent {
		t.Errorf("SVU scores %v and its parent %v", svu, parent)
	}
}
