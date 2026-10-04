package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/naming"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
)

// The title rules, every pair any review has raised, with what each pair
// is. The rules swung between rounds - a fix for one pair quietly undid an
// earlier one - so every change to how titles are compared has to pass the
// whole table, the title check and the episode lookup alike.
var titleRules = []struct {
	a, b string
	want naming.Verdict
}{
	// written another way: spacing, articles, numbers in words, a vowel
	{"Zzyzx Q.X's Return", "Zzyzx Q.X.'s Return", naming.Same},
	{"Zzyzx - A New Dawn", "Zzyzx: The New Dawn", naming.Same},
	{"The Half Loop", "Half Loop", naming.Same},
	{"9 Zzyzx Lives", "The Nine Zzyzx Lives", naming.Same},
	{"Zzyzx in the Grey Coat", "Zzyzx in the Gray Coat", naming.Same},
	{"Grey Zzyzx Coat", "Gray Zzyzx Coat", naming.Same},
	{"12 Zzyzx Men", "Twelve Zzyzx Men", naming.Same},
	{"1200 A.M.-100 A.M.", "12:00 A.M.-1:00 A.M.", naming.Same},
	{"Zzyzx Special Victims Unit", "Zzyzx SVU", naming.Same},
	// a part written another way is the same part: Part and Pt, Vol and
	// Volume, Ep and Episode, Ch and Chapter, a bracketed number, a closing
	// numeral, a number in words
	{"Dune: Part Two", "Dune Part 2", naming.Same},
	{"Zzyzx Saga II", "Zzyzx Saga 2", naming.Same},
	{"Zzyzx Saga: Part Two", "Zzyzx Saga Pt. 2", naming.Same},
	{"Zzyzx Search (1)", "Zzyzx Search, Part 1", naming.Same},
	{"Zzyzx Search (1)", "Zzyzx Search (Part One)", naming.Same},
	{"Zzyzx Search (1)", "Zzyzx Search Pt. I", naming.Same},
	{"The Zzyzx of Both Worlds (1)", "The Zzyzx of Both Worlds, Part I", naming.Same},
	{"Part One", "Part 1", naming.Same},
	{"Part I", "Part 1", naming.Same},
	{"Part Two", "Part II", naming.Same},
	{"Chapter One", "Chapter 1", naming.Same},
	{"Zzyzx Chapter I", "Zzyzx Chapter 1", naming.Same},
	{"Episode 1", "Episode One", naming.Same},
	{"Book 1", "Book One", naming.Same},
	{"Zzyzx Bill: Vol. 1", "Zzyzx Bill Volume 1", naming.Same},
	{"Zzyzx Ep. 3", "Zzyzx Episode 3", naming.Same},
	{"Zzyzx Ch. 2", "Zzyzx Chapter 2", naming.Same},
	// a marked part 1 on one side alone is the first part of what the
	// other names without one
	{"Dune", "Dune: Part One", naming.Same},
	{"Zzyzx Dune", "Zzyzx Dune: Part One", naming.Same},
	{"Zzyzx It", "Zzyzx It Chapter One", naming.Same},
	{"Pilot (1)", "Pilot", naming.Same},
	{"Zzyzx Search", "Zzyzx Search (1)", naming.Same},
	{"Zzyzx Hallows", "Zzyzx Hallows Part 1", naming.Same},
	// but different part words are different: a book is not a volume
	{"Zzyzx Book One", "Zzyzx Volume One", naming.Different},
	{"Zzyzx Part 1", "Zzyzx Chapter 1", naming.Different},
	// a number on one side and not the other, or another on each, is
	// another title: a part, a sequel, or a 1 that is part of the name
	{"Dune", "Dune Part Two", naming.NumberedApart},
	{"Zzyzx and the Deathly Hallows", "Zzyzx and the Deathly Hallows: Part 2", naming.NumberedApart},
	{"Zzyzx Games Mockingjay", "Zzyzx Games Mockingjay - Part 2", naming.NumberedApart},
	{"Zzyzx Saga", "Zzyzx Saga II", naming.NumberedApart},
	{"Zzyzx Search (1)", "Zzyzx Search, Part 2", naming.NumberedApart},
	{"Zzyzx Search", "Zzyzx Search (2)", naming.NumberedApart},
	{"Zzyzx Race 2000", "Zzyzx Race 2050", naming.NumberedApart},
	{"Blade Runner 2049", "Blade Runner 2048", naming.NumberedApart},
	{"Zzyzx Fantasy VIII", "Zzyzx Fantasy XIII", naming.NumberedApart},
	{"Zzyzx the 13th Part VIII", "Zzyzx the 13th Part XIII", naming.NumberedApart},
	{"Zzyzx One Two", "Zzyzx Twelve", naming.NumberedApart},
	{"Zzyzx Force One", "Zzyzx Force", naming.NumberedApart},
	{"Zzyzx Player One", "Zzyzx Player", naming.NumberedApart},
	{"Zzyzx Rogue One", "Zzyzx Rogue", naming.NumberedApart},
	{"Zzyzx Apollo 1", "Zzyzx Apollo", naming.NumberedApart},
	{"Zzyzx Number One", "Zzyzx Number", naming.NumberedApart},
	{"Zzyzx X", "Zzyzx 10", naming.CantTell},
	// a closing letter one side has and the other lacks: a numeral or a
	// name, and can't tell which
	{"Zzyzx Henry", "Zzyzx Henry V", naming.CantTell},
	{"Zzyzx Malcolm X", "Zzyzx Malcolm", naming.CantTell},
	// round 5: written another way
	{"Zzyzx Nightwatch", "Zzyzx Night Watch", naming.Same},
	{"Zzyzx's Eleven", "Zzyzx's 11", naming.Same},
	{"Zzyzx Wars Episode 1 The Menace", "Zzyzx Wars: Episode I - The Menace", naming.Same},
	{"Summer of 84", "Summer of '84", naming.Same},
	{"Zzyzx 9", "Zzyzx Nine", naming.Same},
	{"#1 Zzyzx Fan", "1 Zzyzx Fan", naming.Same},
	{"Plugh - The Return", "Plugh: The Return", naming.Same},
	{"Zzyzx Vol 1", "Zzyzx Volume 1", naming.Same},
	{"Zzyzx Book 1", "Zzyzx Book I", naming.Same},
	{"Zzyzx (Part 1)", "Zzyzx", naming.Same},
	{"Zzyzx [Part 1]", "Zzyzx", naming.Same},
	{"Chapter One: Zzyzx", "Chapter 1: Zzyzx", naming.Same},
	// a country or a year on one side only is the same title; another
	// country on each side may be the other country's version
	{"The Zzyzx (US)", "The Zzyzx", naming.Same},
	{"Zzyzx Shameless (US)", "Zzyzx Shameless", naming.Same},
	{"Zzyzx of Cards (US)", "Zzyzx of Cards", naming.Same},
	{"The Zzyzx (UK)", "The Zzyzx (2001)", naming.Same},
	{"The Zzyzx (2005)", "The Zzyzx", naming.Same},
	{"The Zzyzx (US)", "The Zzyzx (UK)", naming.CantTell},
	{"The Zzyzx (GB)", "The Zzyzx", naming.Same},
	{"Zzyzx (JP)", "Zzyzx (KR)", naming.CantTell},
	// a year each side, one apart, is the same title as a path's year one
	// either side of the item's is; two apart, numbered apart
	{"Zzyzx (2003)", "Zzyzx (2004)", naming.Same},
	{"Zzyzx (2003)", "Zzyzx (2005)", naming.NumberedApart},
	// capitals in brackets that are no country are words of the title: an
	// OVA, a TV cut, a director's cut
	{"Zzyzx (OVA)", "Zzyzx", naming.Different},
	{"Zzyzx (ONA)", "Zzyzx", naming.Different},
	{"Zzyzx (TV)", "Zzyzx", naming.Different},
	{"Zzyzx (DC)", "Zzyzx", naming.Different},
	{"The Zzyzx Files", "Zzyzx", naming.Different},
	// a 3D or a 4K is an edition's word, not a number
	{"Zzyzx 3D", "Zzyzx", naming.Different},
	{"Zzyzx (3D)", "Zzyzx", naming.Different},
	{"Zzyzx 4K", "Zzyzx", naming.Different},
	{"Zzyzx 4K Nature", "Zzyzx Nature", naming.Different},
	// a country's code that is also the title's one word may be part of
	// the name: can't tell
	{"It (IT)", "It", naming.CantTell},
	{"No (NO)", "No", naming.CantTell},
	{"In (IN)", "In", naming.CantTell},
	{"Zzyzx (IT)", "Zzyzx", naming.Same},
	// round 5: numbered apart
	{"Zzyzx Rocky IV", "Zzyzx Rocky IX", naming.NumberedApart},
	{"Zzyzx Trek II", "Zzyzx Trek VI", naming.NumberedApart},
	{"Zzyzx Story", "Zzyzx Story 2", naming.NumberedApart},
	{"2014", "2015", naming.NumberedApart},
	{"1001 Zzyzx Nights", "1002 Zzyzx Nights", naming.NumberedApart},
	{"Zzyzx Pelham 123", "Zzyzx Pelham One Two Three", naming.NumberedApart},
	{"Zzyzx Taken 1", "Zzyzx Taken", naming.NumberedApart},
	{"Zzyzx Super 8", "Zzyzx Super", naming.NumberedApart},
	{"Zzyzx Part 1", "Zzyzx Part 2", naming.NumberedApart},
	{"Zzyzx (1)", "Zzyzx (2)", naming.NumberedApart},
	// round 5: other titles, words added among them
	{"Look Who's Zzyzx", "Look Who's Zzyzx Too", naming.Different},
	{"Look Who's Zzyzx Two", "Look Who's Zzyzx Too", naming.Different},
	{"Pride & Zzyzx", "Pride", naming.Different},
	{"Meet Zzyzx Li", "Meet Zzyzx", naming.Different},
	{"Zzyzx Episode 1", "Zzyzx Part 1", naming.Different},
	{"Mister Zzyzx", "Mr. Zzyzx", naming.Different},
	{"2 Zzyzx 2 Furious", "Too Zzyzx Too Furious", naming.Different},
	{"Se7en", "Seven", naming.Different},
	{"Alien", "Alien Directors Cut", naming.Different},
	{"Zzyzx Runner", "Zzyzx Runner The Final Cut", naming.Different},
	{"Mononoke-hime - Zzyzx Mononoke", "Zzyzx Mononoke", naming.Different},
	{"Pilot (Superfan Cut)", "Pilot", naming.Different},
	{"Rose Remastered", "Rose", naming.Different},
	{"Zzyzx Wars", "Zzyzx Wars Episode IV A New Hope", naming.Different},
	// round 5: can't tell
	{"Zzyzx Rocky I", "Zzyzx Rocky", naming.CantTell},
	{"Zzyzx I", "Zzyzx 1", naming.CantTell},
	{"Zzyzx V", "Zzyzx 5", naming.CantTell},
	// other titles
	{"Aliens", "Alien", naming.Different},
	{"Zzyzx Cars", "Zzyzx Bars", naming.Different},
	{"Bride of Zzyzx", "Pride of Zzyzx", naming.Different},
	{"Zzyzx Plan A", "Zzyzx Plan", naming.Different},
	{"Ten", "10", naming.Different},
	{"Zzyzx Size XL", "Zzyzx Size 40", naming.Different},
}

// The title check and the episode lookup say the same of every pair, both
// ways round: the lookup finds an episode title exactly where the check says
// the two are the same title.
func TestTitleRules(t *testing.T) {
	t.Parallel()

	for _, tc := range titleRules {
		for _, pair := range [][2]string{{tc.a, tc.b}, {tc.b, tc.a}} {
			if got := naming.Judge(naming.FormOf(pair[0]), naming.FormOf(pair[1])); got != tc.want {
				t.Errorf("%q against %q: %v, want %v", pair[0], pair[1], got, tc.want)
			}
			if got := naming.SameTitle(pair[0], pair[1]); got != (tc.want == naming.Same) {
				t.Errorf("sameTitle(%q, %q) = %v, want %v", pair[0], pair[1], got, tc.want == naming.Same)
			}
			ep, score, _ := indexEpisodes([]tmdb.Episode{{Season: 1, Episode: 1, Name: pair[1]}}).best(pair[0])
			if found := score >= seriesConfident && ep.Name == pair[1]; found != (tc.want == naming.Same) {
				t.Errorf("the lookup of %q among %q: found %v at %v, want %v", pair[0], pair[1], found, score, tc.want == naming.Same)
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
		if got := naming.SameName(tc.a, tc.b); got != tc.same {
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
		row, _ := checkPath(&embyfin.Item{Type: tc.typ, Name: tc.name, ProductionYear: tc.year, Path: tc.path}, want, embyfin.Emby)
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
	if row, _ := checkPath(&embyfin.Item{Type: typeMovie, Name: "Seven Zzyzx", OriginalTitle: "Shichinin no Zzyzx", ProductionYear: 1954, Path: "/m/Shichinin.no.Zzyzx.1956.1080p.mkv"}, want, embyfin.Emby); !slices.Contains(row.Problems, "year: path says 1956, metadata says 1954") {
		t.Errorf("a scene name dated by its original title = %v, want the year row", row.Problems)
	}
	// an original or sort title that is the plain title does not settle
	// which part a folder a year off holds: it names the film or its series
	// alike
	for _, it := range []*embyfin.Item{
		{Type: typeMovie, Name: "Zzyzx Hallows: Part 1", OriginalTitle: "Zzyzx Hallows", ProductionYear: 2010, Path: "/m/Zzyzx Hallows (2011)"},
		{Type: typeMovie, Name: "Zzyzx Hallows: Part 1", SortName: "Zzyzx Hallows", ProductionYear: 2010, Path: "/m/Zzyzx Hallows (2011)"},
	} {
		if row, _ := checkPath(it, want, embyfin.Emby); !row.partOneYearOff || len(row.Problems) == 0 || !strings.Contains(row.Problems[0], "can't tell") {
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
		if row, _ := checkPath(it, want, embyfin.Emby); slices.ContainsFunc(row.Problems, func(p string) bool { return strings.HasPrefix(p, "year:") }) {
			t.Errorf("%q (%d) at %s: %v, want no year row", it.Name, it.ProductionYear, it.Path, row.Problems)
		}
	}
	// a country on one side alone says it matched by that, not by articles
	// or numbers written otherwise
	for _, tc := range []struct{ name, path, want string }{
		{"The Zzyzx (US)", "/tv/The Zzyzx", "name: the same title with (US) on one side alone"},
		{"The Zzyzx", "/tv/The Zzyzx (UK)", "name: the same title with (UK) on one side alone"},
	} {
		row, _ := checkPath(&embyfin.Item{Type: "Series", Name: tc.name, ProductionYear: 2005, Path: tc.path}, want, embyfin.Emby)
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
		row, _ := checkPath(it, map[string]bool{"title": true}, embyfin.Emby)
		got := strings.Join(row.Problems, " | ")
		switch {
		case tc.says == "" && got != "":
			t.Errorf("%q at %s: %v, want no finding", tc.name, tc.path, row.Problems)
		case tc.says != "" && !strings.Contains(got, tc.says):
			t.Errorf("%q at %s: %q, want it to say %q", tc.name, tc.path, got, tc.says)
		}
	}
}
