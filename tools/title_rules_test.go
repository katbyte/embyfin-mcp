package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
)

// The title rules, every pair any review has raised, with what each pair
// is. The rules swung between rounds - a fix for one pair quietly undid an
// earlier one - so every change to how titles are compared has to pass the
// whole table, the title check and the episode lookup alike.
var titleRules = []struct {
	a, b string
	want titleVerdict
}{
	// written another way: spacing, articles, numbers in words, a vowel
	{"Zzyzx Q.X's Return", "Zzyzx Q.X.'s Return", titlesSame},
	{"Zzyzx - A New Dawn", "Zzyzx: The New Dawn", titlesSame},
	{"The Half Loop", "Half Loop", titlesSame},
	{"9 Zzyzx Lives", "The Nine Zzyzx Lives", titlesSame},
	{"Zzyzx in the Grey Coat", "Zzyzx in the Gray Coat", titlesSame},
	{"Grey Zzyzx Coat", "Gray Zzyzx Coat", titlesSame},
	{"12 Zzyzx Men", "Twelve Zzyzx Men", titlesSame},
	{"1200 A.M.-100 A.M.", "12:00 A.M.-1:00 A.M.", titlesSame},
	{"Zzyzx Special Victims Unit", "Zzyzx SVU", titlesSame},
	// a part written another way is the same part: Part and Pt, Vol and
	// Volume, Ep and Episode, Ch and Chapter, a bracketed number, a closing
	// numeral, a number in words
	{"Dune: Part Two", "Dune Part 2", titlesSame},
	{"Zzyzx Saga II", "Zzyzx Saga 2", titlesSame},
	{"Zzyzx Saga: Part Two", "Zzyzx Saga Pt. 2", titlesSame},
	{"Zzyzx Search (1)", "Zzyzx Search, Part 1", titlesSame},
	{"Zzyzx Search (1)", "Zzyzx Search (Part One)", titlesSame},
	{"Zzyzx Search (1)", "Zzyzx Search Pt. I", titlesSame},
	{"The Zzyzx of Both Worlds (1)", "The Zzyzx of Both Worlds, Part I", titlesSame},
	{"Part One", "Part 1", titlesSame},
	{"Part I", "Part 1", titlesSame},
	{"Part Two", "Part II", titlesSame},
	{"Chapter One", "Chapter 1", titlesSame},
	{"Zzyzx Chapter I", "Zzyzx Chapter 1", titlesSame},
	{"Episode 1", "Episode One", titlesSame},
	{"Book 1", "Book One", titlesSame},
	{"Zzyzx Bill: Vol. 1", "Zzyzx Bill Volume 1", titlesSame},
	{"Zzyzx Ep. 3", "Zzyzx Episode 3", titlesSame},
	{"Zzyzx Ch. 2", "Zzyzx Chapter 2", titlesSame},
	// a marked part 1 on one side alone is the first part of what the
	// other names without one
	{"Dune", "Dune: Part One", titlesSame},
	{"Zzyzx Dune", "Zzyzx Dune: Part One", titlesSame},
	{"Zzyzx It", "Zzyzx It Chapter One", titlesSame},
	{"Pilot (1)", "Pilot", titlesSame},
	{"Zzyzx Search", "Zzyzx Search (1)", titlesSame},
	{"Zzyzx Hallows", "Zzyzx Hallows Part 1", titlesSame},
	// but different part words are different: a book is not a volume
	{"Zzyzx Book One", "Zzyzx Volume One", titlesDifferent},
	{"Zzyzx Part 1", "Zzyzx Chapter 1", titlesDifferent},
	// a number on one side and not the other, or another on each, is
	// another title: a part, a sequel, or a 1 that is part of the name
	{"Dune", "Dune Part Two", titlesNumberedApart},
	{"Zzyzx and the Deathly Hallows", "Zzyzx and the Deathly Hallows: Part 2", titlesNumberedApart},
	{"Zzyzx Games Mockingjay", "Zzyzx Games Mockingjay - Part 2", titlesNumberedApart},
	{"Zzyzx Saga", "Zzyzx Saga II", titlesNumberedApart},
	{"Zzyzx Search (1)", "Zzyzx Search, Part 2", titlesNumberedApart},
	{"Zzyzx Search", "Zzyzx Search (2)", titlesNumberedApart},
	{"Zzyzx Race 2000", "Zzyzx Race 2050", titlesNumberedApart},
	{"Blade Runner 2049", "Blade Runner 2048", titlesNumberedApart},
	{"Zzyzx Fantasy VIII", "Zzyzx Fantasy XIII", titlesNumberedApart},
	{"Zzyzx the 13th Part VIII", "Zzyzx the 13th Part XIII", titlesNumberedApart},
	{"Zzyzx One Two", "Zzyzx Twelve", titlesNumberedApart},
	{"Zzyzx Force One", "Zzyzx Force", titlesNumberedApart},
	{"Zzyzx Player One", "Zzyzx Player", titlesNumberedApart},
	{"Zzyzx Rogue One", "Zzyzx Rogue", titlesNumberedApart},
	{"Zzyzx Apollo 1", "Zzyzx Apollo", titlesNumberedApart},
	{"Zzyzx Number One", "Zzyzx Number", titlesNumberedApart},
	{"Zzyzx X", "Zzyzx 10", titlesCantTell},
	// a closing letter one side has and the other lacks: a numeral or a
	// name, and can't tell which
	{"Zzyzx Henry", "Zzyzx Henry V", titlesCantTell},
	{"Zzyzx Malcolm X", "Zzyzx Malcolm", titlesCantTell},
	// round 5: written another way
	{"Zzyzx Nightwatch", "Zzyzx Night Watch", titlesSame},
	{"Zzyzx's Eleven", "Zzyzx's 11", titlesSame},
	{"Zzyzx Wars Episode 1 The Menace", "Zzyzx Wars: Episode I - The Menace", titlesSame},
	{"Summer of 84", "Summer of '84", titlesSame},
	{"Zzyzx 9", "Zzyzx Nine", titlesSame},
	{"#1 Zzyzx Fan", "1 Zzyzx Fan", titlesSame},
	{"Plugh - The Return", "Plugh: The Return", titlesSame},
	{"Zzyzx Vol 1", "Zzyzx Volume 1", titlesSame},
	{"Zzyzx Book 1", "Zzyzx Book I", titlesSame},
	{"Zzyzx (Part 1)", "Zzyzx", titlesSame},
	{"Zzyzx [Part 1]", "Zzyzx", titlesSame},
	{"Chapter One: Zzyzx", "Chapter 1: Zzyzx", titlesSame},
	// a country or a year on one side only is the same title; another
	// country on each side may be the other country's version
	{"The Zzyzx (US)", "The Zzyzx", titlesSame},
	{"Zzyzx Shameless (US)", "Zzyzx Shameless", titlesSame},
	{"Zzyzx of Cards (US)", "Zzyzx of Cards", titlesSame},
	{"The Zzyzx (UK)", "The Zzyzx (2001)", titlesSame},
	{"The Zzyzx (2005)", "The Zzyzx", titlesSame},
	{"The Zzyzx (US)", "The Zzyzx (UK)", titlesCantTell},
	{"The Zzyzx (GB)", "The Zzyzx", titlesSame},
	{"Zzyzx (JP)", "Zzyzx (KR)", titlesCantTell},
	// a year each side, one apart, is the same title as a path's year one
	// either side of the item's is; two apart, numbered apart
	{"Zzyzx (2003)", "Zzyzx (2004)", titlesSame},
	{"Zzyzx (2003)", "Zzyzx (2005)", titlesNumberedApart},
	// capitals in brackets that are no country are words of the title: an
	// OVA, a TV cut, a director's cut
	{"Zzyzx (OVA)", "Zzyzx", titlesDifferent},
	{"Zzyzx (ONA)", "Zzyzx", titlesDifferent},
	{"Zzyzx (TV)", "Zzyzx", titlesDifferent},
	{"Zzyzx (DC)", "Zzyzx", titlesDifferent},
	{"The Zzyzx Files", "Zzyzx", titlesDifferent},
	// a 3D or a 4K is an edition's word, not a number
	{"Zzyzx 3D", "Zzyzx", titlesDifferent},
	{"Zzyzx (3D)", "Zzyzx", titlesDifferent},
	{"Zzyzx 4K", "Zzyzx", titlesDifferent},
	{"Zzyzx 4K Nature", "Zzyzx Nature", titlesDifferent},
	// a country's code that is also the title's one word may be part of
	// the name: can't tell
	{"It (IT)", "It", titlesCantTell},
	{"No (NO)", "No", titlesCantTell},
	{"In (IN)", "In", titlesCantTell},
	{"Zzyzx (IT)", "Zzyzx", titlesSame},
	// round 5: numbered apart
	{"Zzyzx Rocky IV", "Zzyzx Rocky IX", titlesNumberedApart},
	{"Zzyzx Trek II", "Zzyzx Trek VI", titlesNumberedApart},
	{"Zzyzx Story", "Zzyzx Story 2", titlesNumberedApart},
	{"2014", "2015", titlesNumberedApart},
	{"1001 Zzyzx Nights", "1002 Zzyzx Nights", titlesNumberedApart},
	{"Zzyzx Pelham 123", "Zzyzx Pelham One Two Three", titlesNumberedApart},
	{"Zzyzx Taken 1", "Zzyzx Taken", titlesNumberedApart},
	{"Zzyzx Super 8", "Zzyzx Super", titlesNumberedApart},
	{"Zzyzx Part 1", "Zzyzx Part 2", titlesNumberedApart},
	{"Zzyzx (1)", "Zzyzx (2)", titlesNumberedApart},
	// round 5: other titles, words added among them
	{"Look Who's Zzyzx", "Look Who's Zzyzx Too", titlesDifferent},
	{"Look Who's Zzyzx Two", "Look Who's Zzyzx Too", titlesDifferent},
	{"Pride & Zzyzx", "Pride", titlesDifferent},
	{"Meet Zzyzx Li", "Meet Zzyzx", titlesDifferent},
	{"Zzyzx Episode 1", "Zzyzx Part 1", titlesDifferent},
	{"Mister Zzyzx", "Mr. Zzyzx", titlesDifferent},
	{"2 Zzyzx 2 Furious", "Too Zzyzx Too Furious", titlesDifferent},
	{"Se7en", "Seven", titlesDifferent},
	{"Alien", "Alien Directors Cut", titlesDifferent},
	{"Zzyzx Runner", "Zzyzx Runner The Final Cut", titlesDifferent},
	{"Mononoke-hime - Zzyzx Mononoke", "Zzyzx Mononoke", titlesDifferent},
	{"Pilot (Superfan Cut)", "Pilot", titlesDifferent},
	{"Rose Remastered", "Rose", titlesDifferent},
	{"Zzyzx Wars", "Zzyzx Wars Episode IV A New Hope", titlesDifferent},
	// round 5: can't tell
	{"Zzyzx Rocky I", "Zzyzx Rocky", titlesCantTell},
	{"Zzyzx I", "Zzyzx 1", titlesCantTell},
	{"Zzyzx V", "Zzyzx 5", titlesCantTell},
	// other titles
	{"Aliens", "Alien", titlesDifferent},
	{"Zzyzx Cars", "Zzyzx Bars", titlesDifferent},
	{"Bride of Zzyzx", "Pride of Zzyzx", titlesDifferent},
	{"Zzyzx Plan A", "Zzyzx Plan", titlesDifferent},
	{"Ten", "10", titlesDifferent},
	{"Zzyzx Size XL", "Zzyzx Size 40", titlesDifferent},
}

// The title check and the episode lookup say the same of every pair, both
// ways round: the lookup finds an episode title exactly where the check says
// the two are the same title.
func TestTitleRules(t *testing.T) {
	t.Parallel()

	for _, tc := range titleRules {
		for _, pair := range [][2]string{{tc.a, tc.b}, {tc.b, tc.a}} {
			if got := judgeTitles(formOf(pair[0]), formOf(pair[1])); got != tc.want {
				t.Errorf("%q against %q: %v, want %v", pair[0], pair[1], got, tc.want)
			}
			if got := sameTitle(pair[0], pair[1]); got != (tc.want == titlesSame) {
				t.Errorf("sameTitle(%q, %q) = %v, want %v", pair[0], pair[1], got, tc.want == titlesSame)
			}
			ep, score, _ := indexEpisodes([]tmdb.Episode{{Season: 1, Episode: 1, Name: pair[1]}}).best(pair[0])
			if found := score >= seriesConfident && ep.Name == pair[1]; found != (tc.want == titlesSame) {
				t.Errorf("the lookup of %q among %q: found %v at %v, want %v", pair[0], pair[1], found, score, tc.want == titlesSame)
			}
		}
	}
}

// A series goes by another name only when it is that name, however written:
// never a name with words added, which is how a spin-off's short title reads.
func TestSeriesNameRules(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"Zzyzx Street", "Zzyzx Street: SU", false},
		{"Zzyzx Street", "The Zzyzx Street", true},
		{"Zzyzx Street", "zzyzx  street", true},
		{"Zzyzx Street", "Zzyzx Streets", false},
		{"Kyojin no Zzyzx", "Kyojin no Zzyzx", true},
	} {
		if got := sameName(tc.a, tc.b); got != tc.same {
			t.Errorf("sameName(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.same)
		}
	}
}

// What a film's or a series' folder says against the item, the year with it.
// A part 1 on one side alone is the first part only when the folder's year -
// bracketed, or bare as a scene name gives it - is the item's, or it gives
// none: a year on, it may be the second part filed under the first, and the
// row says it can't tell. A country on one side only is the same title. One
// title with words added is said as that, a series never called a film.
func TestFilmFolderRules(t *testing.T) {
	t.Parallel()

	want := map[string]bool{"title": true, "year": true}
	for _, tc := range []struct {
		typ, name string
		year      int
		path      string
		says      string // what the title problem says; "" for no finding
		yearSays  string // what the year problem says; "" for none
	}{
		{typeMovie, "Zzyzx Hallows: Part 1", 2010, "/m/Zzyzx Hallows (2011)/Zzyzx Hallows (2011).mkv", "can't tell", ""},
		{typeMovie, "Zzyzx Hallows: Part 1", 2010, "/m/Zzyzx.Hallows.2011.1080p.mkv", "can't tell", ""},
		{typeMovie, "Zzyzx Mockingjay - Part 1", 2014, "/m/Zzyzx Mockingjay (2015)", "can't tell", ""},
		{typeMovie, "Zzyzx Bill: Vol. 1", 2003, "/m/Zzyzx Bill (2004)", "can't tell", ""},
		{typeMovie, "Zzyzx Hallows: Part 1", 2010, "/m/Zzyzx Hallows (2010)", "", ""},
		{typeMovie, "Zzyzx Hallows: Part 1", 2010, "/m/Zzyzx.Hallows.2010.1080p.mkv", "", ""},
		{typeMovie, "Zzyzx Hallows: Part 1", 2010, "/m/Zzyzx Hallows", "", ""},
		{typeMovie, "Dune: Part One", 2021, "/m/Dune (2021)/Dune (2021).mkv", "", ""},
		{typeMovie, "Zzyzx and the Deathly Hallows: Part 2", 2011, "/m/Zzyzx and the Deathly Hallows (2010)", "numbered apart", ""},
		{typeMovie, "Zzyzx Force One", 1997, "/m/Zzyzx Force (1997)", "numbered apart", ""},
		{typeMovie, "Zzyzx Henry V", 1989, "/m/Zzyzx Henry (1989)", "can't tell", ""},
		{typeMovie, "Zzyzx Size XL", 2001, "/m/Zzyzx Size 40 (2001)", "none of the titles", ""},
		// the year a scene name gives, bare, and a year that is the title's own
		{typeMovie, "Zzyzx Title", 2010, "/m/Zzyzx.Title.2013.1080p.mkv", "", "path says 2013, metadata says 2010"},
		{typeMovie, "Zzyzx Race 2000", 1975, "/m/Zzyzx Race 2000.mkv", "", ""},
		// a title ending in a year, the encode's words after it and no
		// release year: read whole, the year is the title's, no release year
		{typeMovie, "Blade Runner 2049", 2017, "/m/Blade.Runner.2049.1080p.mkv", "", ""},
		{typeMovie, "Blade Runner 2049", 2017, "/m/Blade.Runner.2049.1080p.BluRay.x264-GRP.mkv", "", ""},
		{typeMovie, "Zzyzx 1984", 1956, "/m/Zzyzx.1984.1080p.mkv", "", ""},
		{typeMovie, "Zzyzx Race 2000", 1975, "/m/Zzyzx.Race.2000.720p.mkv", "", ""},
		{typeMovie, "Zzyzx Race 2000", 1975, "/m/Zzyzx Race 2000 - 1080p.mkv", "", ""},
		{typeMovie, "Blade Runner 2049", 2017, "/m/Blade Runner 2049 IMAX HDR10+ DV 2160p.mkv", "", ""},
		// the shapes Radarr and the TRaSH naming write
		{typeMovie, "Zzyzx Title", 2010, "/m/Zzyzx Title (2010) Bluray-1080p.mkv", "", ""},
		{typeMovie, "Zzyzx Title", 2010, "/m/Zzyzx Title (2010) {imdb-tt0000001} {edition-Director's Cut} [Bluray-1080p][DTS 5.1][x264]-GROUP.mkv", "", ""},
		{typeMovie, "Zzyzx Title", 2010, "/m/Zzyzx Title (2010) Remux-2160p.mkv", "", ""},
		{typeMovie, "Zzyzx Title", 2010, "/m/Zzyzx Title (2010) WEBDL-1080p.mkv", "", ""},
		{typeMovie, "Zzyzx Title", 2010, "/m/Zzyzx.Title.2010.IMAX.HDR10+.DV.2160p.mkv", "", ""},
		{typeMovie, "Zzyzx Title", 2010, "/m/Zzyzx.Title.2010.Remux-2160p.mkv", "", ""},
		// capitals in brackets that are no country are words the title has
		{"Series", "Zzyzx (OVA)", 2001, "/tv/Zzyzx", `the item's title is the path's with "(OVA)" added: an edition, a subtitle, or another series - can't tell from the names`, ""},
		{"Series", "Zzyzx", 2001, "/tv/Zzyzx (TV)", `the path's title is the item's with "(TV)" added`, ""},
		{typeMovie, "Zzyzx", 2003, "/m/Zzyzx (DC) (2003)", `the path's title is the item's with "(DC)" added`, ""},
		// a leading article does not hide words added
		{typeMovie, "The Zzyzx Files", 1998, "/m/Zzyzx (1998)", `the item's title is the path's with "Files" added`, ""},
		// a year after the encode's words or an edition's is not the
		// release's; two years side by side, the later is
		{typeMovie, "Zzyzx Race 2000", 1975, "/m/Zzyzx.Race.2000.1080p.x264-2023.mkv", "", ""},
		{typeMovie, "Blade Runner 2049", 2017, "/m/Blade.Runner.2049.2160p.Remux-2023.mkv", "", ""},
		{typeMovie, "Zzyzx Title", 2010, "/m/Zzyzx.Title.2010.1080p.2160p-2023-remux.mkv", "", ""},
		{typeMovie, "Zzyzx", 2015, "/m/Zzyzx.2015.Extended.2016.Re-Edit.mkv", "", ""},
		{typeMovie, "Zzyzx", 1982, "/m/Zzyzx.1982.Remastered.2021.mkv", "", ""},
		{typeMovie, "Blade Runner", 1982, "/m/Blade.Runner.1982.The.Final.Cut.2007.mkv", "", ""},
		{typeMovie, "Zzyzx", 1979, "/m/Zzyzx.1979.Directors.Cut.2003.mkv", "", ""},
		{typeMovie, "Zzyzx", 1979, "/m/Zzyzx.1981.Directors.Cut.2003.mkv", "", "path says 1981, metadata says 1979"},
		{typeMovie, "Blade Runner 2049", 2017, "/m/Blade.Runner.2049.2017.1080p.mkv", "", ""},
		{typeMovie, "Zzyzx 1984", 2020, "/m/Zzyzx.1984.2020.1080p.mkv", "", ""},
		// a film titled with an edition's words; an edition before the year
		// hides no year; two years side by side, the later dates the file
		{typeMovie, "The Final Cut", 2004, "/m/The.Final.Cut.2004.1080p.mkv", "", ""},
		{typeMovie, "Pink Floyd The Final Cut", 1983, "/m/Pink.Floyd.The.Final.Cut.1983.mkv", "", ""},
		{typeMovie, "Zzyzx", 1999, "/m/Zzyzx.Theatrical.Cut.2004.1080p.mkv", "", "path says 2004, metadata says 1999"},
		// of two years side by side, the earlier is the title's own only
		// when it is a number of a title the item goes by; otherwise the
		// first dates the file
		// and when neither reading is the item's year, the row gives both
		{typeMovie, "Zzyzx Race", 2008, "/m/Zzyzx.Race.2000.1975.1080p.mkv", "", `the path reads as "Zzyzx Race" dated 2000, or "Zzyzx Race 2000" dated 1975, can't tell which; neither is 2008`},
		{typeMovie, "Zzyzx Race 2000", 1975, "/m/Zzyzx.Race.2000.1975.1080p.mkv", "", ""},
		{typeMovie, "Zzyzx Woman 1984", 2020, "/m/Zzyzx.Woman.1984.2020.1080p.mkv", "", ""},
		{typeMovie, "Zzyzx", 1982, "/m/Zzyzx.1982.2021.1080p.mkv", "", ""},
		// and when the other reading is dated the item's year, the title or
		// the year disagrees
		{typeMovie, "Zzyzx", 2021, "/m/Zzyzx.1982.2021.1080p.mkv", "", `the path reads as "Zzyzx" dated 1982, or "Zzyzx 1982" dated 2021: the title or the year disagrees, can't tell which`},
		{typeMovie, "Zzyzx", 2020, "/m/Zzyzx.1982.2021.1080p.mkv", "", `the path reads as "Zzyzx" dated 1982, or "Zzyzx 1982" dated 2021: the title or the year disagrees, can't tell which`},
		{typeMovie, "Zzyzx Race", 1975, "/m/Zzyzx.Race.2000.1975.1080p.mkv", "", `the path reads as "Zzyzx Race" dated 2000, or "Zzyzx Race 2000" dated 1975: the title or the year disagrees, can't tell which`},
		{typeMovie, "Zzyzx", 1990, "/m/Zzyzx.1982.2021.1080p.mkv", "", `the path reads as "Zzyzx" dated 1982, or "Zzyzx 1982" dated 2021, can't tell which; neither is 1990`},
		{typeMovie, "Zzyzx", 2010, "/m/Zzyzx.2010.2011.mkv", "", ""},
		{typeMovie, "Zzyzx Night", 1999, "/m/Zzyzx.Night.1999.2021.Remastered.mkv", "", ""},
		// a bracketed year later than next year dates nothing, and no year
		// above it stands in
		{typeMovie, "Zzyzx", 2010, "/m/Zzyzx (2049)", "", ""},
		{typeMovie, "Zzyzx", 2010, "/m/Zzyzx Saga (1982)/Zzyzx (2049)", "", ""},
		{typeMovie, "Zzyzx", 2010, "/m/Zzyzx (2027)", "", "path says 2027, metadata says 2010"},
		// an edition's word inside a title, added
		{typeMovie, "Zzyzx Nature", 2016, "/m/Zzyzx 4K Nature (2016)", `the path's title is the item's with "4K" added`, ""},
		// a 3D is an edition's word, added
		{typeMovie, "Zzyzx", 2019, "/m/Zzyzx 3D (2019)", `the path's title is the item's with "3D" added`, ""},
		{typeMovie, "Zzyzx", 2019, "/m/Zzyzx (3D) (2019)", `the path's title is the item's with "(3D)" added`, ""},
		// one title with words added, either way round
		{typeMovie, "Alien", 1979, "/m/Alien Director's Cut (1979)", `the path's title is the item's with "Director's Cut" added: an edition, a subtitle, or another film - can't tell from the names`, ""},
		{typeMovie, "Alien Directors Cut", 1979, "/m/Alien (1979)", `the item's title is the path's with "Directors Cut" added: an edition, a subtitle, or another film - can't tell from the names`, ""},
		{typeMovie, "Zzyzx Runner", 1982, "/m/Zzyzx Runner The Final Cut (1982)", `with "The Final Cut" added`, ""},
		{typeMovie, "Zzyzx Mononoke", 1997, "/m/Mononoke-hime - Zzyzx Mononoke (1997)", `with "Mononoke-hime" added`, ""},
		{typeMovie, "Zzyzx Wars", 1977, "/m/Zzyzx Wars Episode IV A New Hope (1977)", `with "Episode IV A New Hope" added`, ""},
		// a series: a country on one side only, another on each, and words
		// added, never called a film
		{"Series", "The Zzyzx (US)", 2005, "/tv/The Zzyzx", "", ""},
		{"Series", "Zzyzx Shameless (US)", 2011, "/tv/Zzyzx Shameless", "", ""},
		{"Series", "The Zzyzx (UK)", 2001, "/tv/The Zzyzx (2001)", "", ""},
		{"Series", "The Zzyzx (US)", 2005, "/tv/The Zzyzx (UK)", "can't tell", ""},
		{"Series", "Zzyzx of Cards", 2013, "/tv/Zzyzx of Cards Revisited", `the path's title is the item's with "Revisited" added: an edition, a subtitle, or another series - can't tell from the names`, ""},
		{"Series", "Zzyzx Other", 2013, "/tv/Quux Plugh", "the wrong match, or another series", ""},
	} {
		row, _ := checkPath(&embyfin.Item{Type: tc.typ, Name: tc.name, ProductionYear: tc.year, Path: tc.path}, want)
		var title, year string
		for _, p := range row.Problems {
			switch {
			case strings.HasPrefix(p, "title:"):
				title = p
			case strings.HasPrefix(p, "year:"):
				year = p
			}
		}
		switch {
		case tc.says == "" && title != "":
			t.Errorf("%q (%d) at %s: %q, want no title finding", tc.name, tc.year, tc.path, title)
		case tc.says != "" && !strings.Contains(title, tc.says):
			t.Errorf("%q (%d) at %s: %q, want it to say %q", tc.name, tc.year, tc.path, title, tc.says)
		}
		switch {
		case tc.yearSays == "" && year != "":
			t.Errorf("%q (%d) at %s: %q, want no year finding", tc.name, tc.year, tc.path, year)
		case tc.yearSays != "" && !strings.Contains(year, tc.yearSays):
			t.Errorf("%q (%d) at %s: %q, want it to say %q", tc.name, tc.year, tc.path, year, tc.yearSays)
		}
		if tc.typ == "Series" && strings.Contains(strings.Join(row.Problems, " "), "film") {
			t.Errorf("%q at %s calls a series a film: %v", tc.name, tc.path, row.Problems)
		}
		if tc.says == "can't tell" && strings.Contains(tc.name, "Part 1") && !row.partOneYearOff {
			t.Errorf("%q (%d) at %s: not marked for TMDB to say which part", tc.name, tc.year, tc.path)
		}
	}
	// a scene name's bare year is read against every title the item goes
	// by: its original title here, where its name holds a number in words
	if row, _ := checkPath(&embyfin.Item{Type: typeMovie, Name: "Seven Zzyzx", OriginalTitle: "Shichinin no Zzyzx", ProductionYear: 1954, Path: "/m/Shichinin.no.Zzyzx.1956.1080p.mkv"}, want); !slices.Contains(row.Problems, "year: path says 1956, metadata says 1954") {
		t.Errorf("a scene name dated by its original title = %v, want the year row", row.Problems)
	}
	// an original or sort title that is the plain title does not settle
	// which part a folder a year off holds: it names the film or its series
	// alike
	for _, it := range []*embyfin.Item{
		{Type: typeMovie, Name: "Zzyzx Hallows: Part 1", OriginalTitle: "Zzyzx Hallows", ProductionYear: 2010, Path: "/m/Zzyzx Hallows (2011)"},
		{Type: typeMovie, Name: "Zzyzx Hallows: Part 1", SortName: "Zzyzx Hallows", ProductionYear: 2010, Path: "/m/Zzyzx Hallows (2011)"},
	} {
		if row, _ := checkPath(it, want); !row.partOneYearOff || len(row.Problems) == 0 || !strings.Contains(row.Problems[0], "can't tell") {
			t.Errorf("part 1 with original %q, sort %q, a year off = %v (marked %v), want it can't tell", it.OriginalTitle, it.SortName, row.Problems, row.partOneYearOff)
		}
	}
	// and never a year that is a number of a title the item goes by: the
	// original title's reading cut at the name's own year dates nothing
	for _, it := range []*embyfin.Item{
		{Type: typeMovie, Name: "Seven Zzyzx", OriginalTitle: "Shichinin no Zzyzx", ProductionYear: 1954, Path: "/m/Shichinin.no.Zzyzx.1954.1080p.mkv"},
		{Type: typeMovie, Name: "Zzyzx 1984", OriginalTitle: "Zzyzx", ProductionYear: 1956, Path: "/m/Zzyzx.1984.1080p.mkv"},
		{Type: typeMovie, Name: "Zzyzx 2012", SortName: "Zzyzx", ProductionYear: 2009, Path: "/m/Zzyzx.2012.1080p.mkv"},
	} {
		if row, _ := checkPath(it, want); slices.ContainsFunc(row.Problems, func(p string) bool { return strings.HasPrefix(p, "year:") }) {
			t.Errorf("%q (%d) at %s: %v, want no year row", it.Name, it.ProductionYear, it.Path, row.Problems)
		}
	}
	// a country on one side alone says it matched by that, not by articles
	// or numbers written otherwise
	for _, tc := range []struct{ name, path, want string }{
		{"The Zzyzx (US)", "/tv/The Zzyzx", "name: the same title with (US) on one side alone"},
		{"The Zzyzx", "/tv/The Zzyzx (UK)", "name: the same title with (UK) on one side alone"},
	} {
		row, _ := checkPath(&embyfin.Item{Type: "Series", Name: tc.name, ProductionYear: 2005, Path: tc.path}, want)
		if len(row.Problems) != 0 || row.TitleMatched != tc.want {
			t.Errorf("%q at %s: problems %v, title_matched %q, want %q", tc.name, tc.path, row.Problems, row.TitleMatched, tc.want)
		}
	}
	// and the episodes' own checks read a part word's numeral the same way,
	// and say a title with words added as that
	for _, tc := range []struct{ name, path, says string }{
		{"Chapter I", "/s/Zzyzx - 01x01 - Chapter 1.mkv", ""},
		{"Part One", "/s/Zzyzx - 01x01 - Part 1.mkv", ""},
		{"Episode One", "/s/Zzyzx - 01x01 - Episode 1.mkv", ""},
		{"Pilot", "/s/Zzyzx - 01x01 - Pilot (Superfan Cut).mkv", `the file's title is the server's with "(Superfan Cut)" added: an edition, a subtitle, or another episode - can't tell from the names`},
		{"Rose Remastered", "/s/Zzyzx - 01x01 - Rose.mkv", `the server's title is the file's with "Remastered" added`},
	} {
		it := &embyfin.Item{Type: typeEpisode, Name: tc.name, SeriesName: "Zzyzx", ParentIndexNumber: new(1), IndexNumber: new(1), Path: tc.path}
		row, _ := checkPath(it, map[string]bool{"title": true})
		got := strings.Join(row.Problems, " | ")
		switch {
		case tc.says == "" && got != "":
			t.Errorf("%q at %s: %v, want no finding", tc.name, tc.path, row.Problems)
		case tc.says != "" && !strings.Contains(got, tc.says):
			t.Errorf("%q at %s: %q, want it to say %q", tc.name, tc.path, got, tc.says)
		}
	}
}
