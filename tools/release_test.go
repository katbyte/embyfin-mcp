package tools

import (
	"fmt"
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
	} {
		got := parseRelease(tc.name)
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
		if score, how := titleScore(pair[0], pair[1]); score < 1 {
			t.Errorf("titleScore(%q, %q) = %.2f (%s), want a certain match", pair[0], pair[1], score, how)
		}
	}

	// a prefix is a candidate, not an answer
	if score, _ := titleScore("Doctor Who", "Doctor Who Confidential"); score >= 0.95 || score < 0.5 {
		t.Errorf("a prefix scored %.2f, want a candidate rather than a certainty", score)
	}
	// and unrelated titles do not match at all
	if score, _ := titleScore("Severance", "Succession"); score > 0 {
		t.Errorf("unrelated titles scored %.2f", score)
	}
	// an article is not a difference worth refusing over
	if score, _ := titleScore("Office", "The Office"); score < 0.9 {
		t.Errorf("an article cost %.2f", score)
	}
}

// show_resolve turns the corpus into library series, scores the right one
// first, and hands back what it read so a caller can see what it understood.
func TestShowResolve(t *testing.T) {
	t.Parallel()

	shows := []*fakeSeries{
		{id: "ae", name: "24 Hours in A&E", year: 2011},
		{id: "911", name: "9-1-1", year: 2018},
		{id: "911ls", name: "9-1-1: Lone Star", year: 2020},
		{id: "gath", name: "A Gatherer's Adventure in Isekai", year: 2024},
		{id: "penn", name: "1600 Penn", year: 2012},
		{id: "office", name: "The Office", year: 2005},
	}
	cs := session(t, tvServer(t, shows...), Options{})

	for _, tc := range []struct {
		release string
		want    string
	}{
		{"24.Hours.in.A.and.E.S36E03.1080p.HDTV.H264-DARKFLiX", "ae"},
		{"9-1-1-S09E16 HDTV X264-NGP", "911"},
		{"A.Gatherers.Adventure.in.Isekai.S01.1080p.CR.WEB-DL", "gath"},
		{"1600 Penn (2012) S01 (1080p AMZN WEB-DL x265 10bit)", "penn"},
		{"Office.S03E01.720p.HDTV.x264", "office"},
	} {
		out := mustCall(t, cs, "show_resolve", map[string]any{"title": tc.release})
		cands := objects(t, out["candidates"], "candidates")
		if len(cands) == 0 {
			t.Errorf("%s resolved to nothing (parsed %q)", tc.release, out["parsed_title"])
			continue
		}
		if cands[0]["series_id"] != tc.want {
			t.Errorf("%s resolved to %v (%v), want %s", tc.release, cands[0]["series_id"], cands[0]["name"], tc.want)
		}
		if got := score(t, cands[0]); got < 0.9 {
			t.Errorf("%s matched %v at %v, too low to act on", tc.release, cands[0]["name"], got)
		}
		if cands[0]["matched_on"] == "" {
			t.Errorf("%s does not say what matched: %v", tc.release, cands[0])
		}
	}

	// the season and episode come back too, so a caller can go straight on to
	// asking whether the library holds them
	out := mustCall(t, cs, "show_resolve", map[string]any{"title": "9-1-1-S09E16 HDTV X264-NGP"})
	if number(t, out["parsed_season"], "parsed_season") != 9 || number(t, out["parsed_episode"], "parsed_episode") != 16 {
		t.Errorf("parsed S%vE%v, want S09E16", out["parsed_season"], out["parsed_episode"])
	}

	// the near-namesake is offered, below the real one, rather than hidden
	out = mustCall(t, cs, "show_resolve", map[string]any{"title": "9-1-1.S09E16"})
	cands := objects(t, out["candidates"], "candidates")
	if len(cands) < 2 || cands[1]["series_id"] != "911ls" {
		t.Errorf("Lone Star is not the runner-up: %v", cands)
	}
	if first, second := score(t, cands[0]), score(t, cands[1]); first <= second {
		t.Errorf("the namesake scored %v against %v", second, first)
	}

	// a title the library does not hold comes back empty rather than with a
	// bad guess dressed up as an answer
	out = mustCall(t, cs, "show_resolve", map[string]any{"title": "Nothing.We.Hold.S01E01.1080p"})
	for _, c := range objects(t, out["candidates"], "candidates") {
		if got := score(t, c); got >= 0.9 {
			t.Errorf("an unheld title matched %v at %v", c["name"], got)
		}
	}

	if msg := mustRefuse(t, cs, "show_resolve", map[string]any{"title": "   "}); !strings.Contains(msg, "title") {
		t.Errorf("an empty title said: %s", msg)
	}
}

// A year the caller knows tells two shows of the same name apart.
func TestShowResolveUsesTheYear(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t,
		&fakeSeries{id: "old", name: "Battlestar Galactica", year: 1978},
		&fakeSeries{id: "new", name: "Battlestar Galactica", year: 2004},
	), Options{})

	out := mustCall(t, cs, "show_resolve", map[string]any{"title": "Battlestar.Galactica.2004.S01E01.1080p.BluRay.x264"})
	cands := objects(t, out["candidates"], "candidates")
	if len(cands) == 0 || cands[0]["series_id"] != "new" {
		t.Fatalf("the 2004 series was not preferred: %v", cands)
	}
	if first, second := score(t, cands[0]), score(t, cands[1]); first <= second {
		t.Errorf("the year made no difference: %v against %v", first, second)
	}

	// and the caller can give the year when the name does not carry one
	out = mustCall(t, cs, "show_resolve", map[string]any{"title": "Battlestar Galactica", "year": 1978})
	if cands = objects(t, out["candidates"], "candidates"); cands[0]["series_id"] != "old" {
		t.Errorf("an explicit year was ignored: %v", cands)
	}
}

// The parser never hands back an empty title, whatever it is given: a caller
// that gets "" would search the whole library.
func TestParseReleaseAlwaysNamesSomething(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"S01E01", "1080p.WEB-DL", "...", "-", "2019"} {
		if got := parseRelease(name); strings.TrimSpace(got.Title) == "" {
			t.Errorf("parseRelease(%q) read no title at all: %+v", name, got)
		}
	}
	// and the season still comes out of a bare marker
	if got := parseRelease("S01E01"); got.Season != 1 || got.Episode != 1 {
		t.Errorf("parseRelease(\"S01E01\") = %+v", got)
	}
}

// A name with no title in it is refused rather than searched for, because a
// search for nothing is a search for everything.
func TestShowResolveRefusesATitlelessName(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})
	if msg := mustRefuse(t, cs, "show_resolve", map[string]any{"title": "..."}); !strings.Contains(msg, "no title") {
		t.Errorf("a titleless name said: %s", msg)
	}
}

// fmt is used by the corpus table's failure messages.
var _ = fmt.Sprintf

// The contract this tool answers under, written down because a caller acting
// on more than it says would do real damage: show_resolve reads a NAME. It
// says which series a name is for, never that a file is what its name claims.
//
// Executables padded to a plausible size and named as clean releases are a
// real shape in download folders. They parse as perfectly good episode names -
// the title is cut at the season marker long before the extension matters -
// and a caller that treated "it resolved" as "it is an episode" would move
// malware into the library it was meant to fill.
func TestShowResolveReadsANameNotAFile(t *testing.T) {
	t.Parallel()

	shows := []*fakeSeries{{id: "911", name: "9-1-1", year: 2018}}
	cs := session(t, tvServer(t, shows...), Options{})

	media := mustCall(t, cs, "show_resolve", map[string]any{"title": "9-1-1 S10E01 1080p WEB H264-GROUP.mkv"})
	for _, name := range []string{
		"9-1-1 S10E01 1080p WEB H264-GROUP.exe",
		"9-1-1 S10E01 1080p WEB H264-GROUP.scr",
	} {
		out := mustCall(t, cs, "show_resolve", map[string]any{"title": name})
		cands := objects(t, out["candidates"], "candidates")
		if len(cands) == 0 || cands[0]["series_id"] != "911" {
			t.Fatalf("%s resolved to %v", name, cands)
		}
		// identical to the .mkv, and that is the point: the answer is about
		// the name, so nothing in it can be read as evidence about the bytes
		if score(t, cands[0]) != score(t, objects(t, media["candidates"], "candidates")[0]) {
			t.Errorf("%s scored differently from the same name on a .mkv, which would read as a judgement about the file", name)
		}
		if number(t, out["parsed_season"], "parsed_season") != 10 || number(t, out["parsed_episode"], "parsed_episode") != 1 {
			t.Errorf("%s parsed S%vE%v", name, out["parsed_season"], out["parsed_episode"])
		}
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
		got := parseRelease(tc.release)
		if !strings.EqualFold(normaliseTitle(got.Title), normaliseTitle(tc.title)) {
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
		if score, _ := titleScore(tc.release, tc.library); score != 1 {
			t.Errorf("%q against %q scored %v, want 1: the accent is the only difference", tc.release, tc.library, score)
		}
	}

	// folding the marks off must not fold two different shows together
	if score, _ := titleScore("Fiance", "Finance"); score == 1 {
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
		if score, _ := titleScore(tc.release, tc.library); score != 1 {
			t.Errorf("%q against %q scored %v, want 1: the points are the only difference", tc.release, tc.library, score)
		}
	}

	// and it has to beat the shows it was losing to
	pd, _ := titleScore("Chicago PD", "Chicago P.D.")
	for _, other := range []string{"Chicago Fire", "Chicago Med", "Chicago Justice", "Alaska PD"} {
		if score, _ := titleScore("Chicago PD", other); score >= pd {
			t.Errorf("Chicago PD scores %v against %q and %v against Chicago P.D.", score, other, pd)
		}
	}

	// a word that merely ends in a point is not an acronym
	if score, _ := titleScore("Dr Who", "Dr. Who"); score != 1 {
		t.Errorf("Dr. Who: %v", score)
	}

	// the release name spells the same acronym with its dots already turned
	// into spaces, and that has to land on the same word
	for _, spelling := range []string{"Marvels.Agents.of.S.H.I.E.L.D", "Marvels Agents of S H I E L D", "Marvels Agents of SHIELD"} {
		if score, _ := titleScore(parseRelease(spelling).Title, "Marvel's Agents of S.H.I.E.L.D."); score != 1 {
			t.Errorf("%q scored %v", spelling, score)
		}
	}

	// single letters that are words in their own right are not an acronym:
	// the ampersand in "A&E" becomes "and", which breaks the run
	if score, _ := titleScore("24 Hours in A and E", "24 Hours in A&E"); score != 1 {
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
		score, how := titleScore(tc.release, tc.library)
		if score >= seriesConfident {
			t.Errorf("%q against %q scored %v (%s): a spin-off must not resolve to its parent", tc.release, tc.library, score, how)
		}
	}

	// the other way round is the Doctor Who Confidential case, which is worth
	// something but still not a certainty
	score, _ := titleScore("Doctor Who", "Doctor Who Confidential")
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
		score, how := titleScore(tc.release, tc.library)
		if score < seriesConfident {
			t.Errorf("%q against %q scored %v (%s)", tc.release, tc.library, score, how)
		}
	}

	// the acronym has to be the right one
	if score, _ := titleScore("Law and Order SVU", "Law & Order: Criminal Intent"); score >= seriesConfident {
		t.Errorf("SVU matched Criminal Intent at %v", score)
	}

	// and SVU must beat the parent it was losing to
	svu, _ := titleScore("Law and Order SVU", "Law & Order: Special Victims Unit")
	parent, _ := titleScore("Law and Order SVU", "Law & Order")
	if svu <= parent {
		t.Errorf("SVU scores %v and its parent %v", svu, parent)
	}
}
