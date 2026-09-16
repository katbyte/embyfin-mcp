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
