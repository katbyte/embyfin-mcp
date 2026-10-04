package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/naming"
)

// and so show_resolve finds a series by a title in any script, and a release
// name carrying one
func TestShowResolveReadsEveryScript(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t,
		&fakeSeries{id: "slovo", name: "Слово пацана. Кровь на асфальте", year: 2023},
		&fakeSeries{id: "titan", name: "進撃の巨人", year: 2013},
		&fakeSeries{id: "nisi", name: "Το Νησί", year: 2010},
	), Options{})

	for _, tc := range []struct{ release, want string }{
		{"進撃の巨人 S01E01 1080p WEB H264-GROUP", "titan"},
		{"Το.Νησί.S01E01.720p.HDTV.x264-GROUP", "nisi"},
		{"ΤΟ ΝΗΣΙ", "nisi"},
		{"Слово пацана Кровь на асфальте S01E03", "slovo"},
	} {
		out := mustCall(t, cs, "show_resolve", map[string]any{"title": tc.release})
		cands := objects(t, out["candidates"], "candidates")
		if len(cands) == 0 || cands[0]["series_id"] != tc.want {
			t.Errorf("%s resolved to %v, want %s", tc.release, cands, tc.want)
			continue
		}
		if got := score(t, cands[0]); got < seriesConfident {
			t.Errorf("%s matched at %v, too low to act on", tc.release, got)
		}
	}
}

// A show with a slash in its name resolves to itself, not to whatever the
// half after the slash names.
func TestAShowNamedWithASlashIsNotAPath(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t,
		&fakeSeries{id: "sv", name: "Sweet/Vicious", year: 2016, episodes: []ep{{season: 1, number: 1, name: "Pilot", path: "/media/shows/Sweet Vicious/S01E01.mkv"}}},
		&fakeSeries{id: "vicious", name: "Vicious", year: 2013, episodes: []ep{{season: 1, number: 1, name: "Anniversary", path: "/media/shows/Vicious/S01E01.mkv"}}},
		&fakeSeries{id: "nt", name: "Nip/Tuck", year: 2003, episodes: []ep{{season: 1, number: 1, name: "Pilot", path: "/media/shows/Nip Tuck/S01E01.mkv"}}},
	), Options{})

	for name, want := range map[string]string{"Sweet/Vicious": "sv", "Nip/Tuck": "nt", "Sweet/Vicious S01E01 1080p WEB H264-GROUP": "sv"} {
		out := mustCall(t, cs, "show_episodes_exist", map[string]any{"series": name, "episodes": []map[string]any{{"season": 1, "episode": 1}}})
		if got := text(out["series_id"]); got != want {
			t.Errorf("%q resolved to %q, want %s", name, got, want)
		}
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
	// the specials are season 0, and a name numbering one carries a season:
	// left out as a 0, S00E03 read as a name with no season in it at all
	out = mustCall(t, cs, "show_resolve", map[string]any{"title": "9-1-1.S00E03.1080p.WEB"})
	if number(t, out["parsed_season"], "parsed_season") != 0 || number(t, out["parsed_episode"], "parsed_episode") != 3 {
		t.Errorf("parsed S%vE%v, want S00E03", out["parsed_season"], out["parsed_episode"])
	}
	out = mustCall(t, cs, "show_resolve", map[string]any{"title": "9-1-1.S02E00.1080p.WEB"})
	if number(t, out["parsed_season"], "parsed_season") != 2 || number(t, out["parsed_episode"], "parsed_episode") != 0 {
		t.Errorf("parsed S%vE%v, want S02E00", out["parsed_season"], out["parsed_episode"])
	}
	out = mustCall(t, cs, "show_resolve", map[string]any{"title": "9-1-1"})
	if _, ok := out["parsed_season"]; ok || out["parsed_episode"] != nil {
		t.Errorf("a bare title parsed a season %v and an episode %v", out["parsed_season"], out["parsed_episode"])
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

// A name with no title in it is refused rather than searched for, because a
// search for nothing is a search for everything - and so is a name that is
// nothing but the season, episode, encode and group, which used to be
// searched for as if it were the title.
func TestShowResolveRefusesATitlelessName(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})
	for _, name := range []string{"...", "S01E01.1080p.WEB-DL-GROUP", "S01E01", "1080p.WEB-DL", "1080p.WEB-DL-GROUP", "Season 2 720p HDTV x264-NGP", "2160p.HDR.x265"} {
		if msg := mustRefuse(t, cs, "show_resolve", map[string]any{"title": name}); !strings.Contains(msg, "no title could be read out of") || !strings.Contains(msg, "all season, encode and group") {
			t.Errorf("%q said: %s", name, msg)
		}
	}
	// and a one-word title is a title, whatever it spells
	for _, name := range []string{"Max", "Max.S01E01.1080p.WEB-DL-GROUP", "Severance.S01E01.1080p.WEB-DL-GROUP"} {
		if rel := naming.ParseRelease(name); rel.Unread {
			t.Errorf("parseRelease(%q) read no title: %+v", name, rel)
		}
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
