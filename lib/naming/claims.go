package naming

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/katbyte/embyfin-mcp/lib/mediapath"
)

// Claim is what one segment of a path says the film is.
type Claim struct {
	Segment string  // the file's or folder's name
	Title   string  // the title it names
	Year    int     // the year it names
	Score   float64 // how close the title is to the nearest of the item's names
	// Whole is the segment's title read whole when words follow its (year),
	// "" when none do (see WholeTitle), and Before the words before the year
	Whole, Before string
}

// Terms are what TMDB is asked by for a claim's title, in turn. The title
// read whole comes first when words follow the year and the words before it
// are none of the item's titles - the franchise word of "Batman (2016)
// Unlimited", which finds whichever film the item is matched to - and the
// title read after it, asked only when the whole title finds nothing
// ("Mononoke-hime (1997) Remastered"). When the words before the year are
// one of the item's titles ("Alien (1979) Directors Cut" held as Alien), the
// words after it are an edition's, and the title read is asked alone.
func (c Claim) Terms(held []string) []string {
	if c.Whole == "" || slices.ContainsFunc(held, func(h string) bool { return Like(c.Before, h) }) {
		return []string{c.Title}
	}

	return []string{c.Whole, c.Title}
}

// After is the words after a claim's year, "" when none follow it but an
// encode's.
func (c Claim) After() string {
	if c.Whole == "" {
		return ""
	}

	return strings.TrimSpace(strings.TrimPrefix(c.Whole, c.Before))
}

// Named is the title a claim names, to quote: the title read whole when the
// words before the year are none the item goes by, as TMDB was asked by it.
func (c Claim) Named(held []string) string {
	if terms := c.Terms(held); len(terms) > 1 {
		return terms[0]
	}

	return cmp.Or(c.Title, c.Segment)
}

// WholeTitle is a segment's title read whole when words follow its (year):
// "Zzyzx (2016) Unlimited - Mechs" names "Zzyzx Unlimited - Mechs", where the
// title before the year alone is a word a dozen films begin with, and TMDB's
// search by it finds whichever film the item is matched to among them. ""
// when nothing follows the year but a renamer's or a release's own words: a
// tag in brackets or braces ("[Bluray-1080p]", "{edition-Directors Cut}"), a
// release group after a hyphen ("-GROUP"), and the encode's, source's and
// sound's words, read part by part across a hyphen or a plus ("Bluray-1080p",
// "Remux-2160p", "HDR10+", "IMAX", "Proper").
func WholeTitle(segment string) string {
	whole, _ := YearParts(segment)

	return whole
}

// YearParts is WholeTitle's whole title, and the words before the year.
func YearParts(segment string) (whole, before string) {
	base := FileExtension.ReplaceAllString(segment, "")
	m := BracketedYear.FindStringIndex(base)
	if m == nil {
		return "", ""
	}
	before = strings.Trim(strings.TrimSpace(strings.NewReplacer(".", " ", "_", " ").Replace(base[:m[0]])), " -_([{")
	after := releaseGroup.ReplaceAllString(bracketedTag.ReplaceAllString(base[m[1]:], " "), "")
	words := strings.Fields(strings.NewReplacer(".", " ", "_", " ").Replace(after))
	for i, w := range words {
		if ReleaseWord(w) {
			words = words[:i]

			break
		}
	}
	rest := strings.Trim(strings.Join(words, " "), " -_([{")
	if !strings.ContainsFunc(rest, unicode.IsLetter) {
		return "", before
	}

	return strings.TrimSpace(before + " " + rest), before
}

// bracketedTag is a tag a renamer writes in brackets after a film's year:
// "[1080p]", "{imdb-tt0078748}", "{edition-Director's Cut}".
var bracketedTag = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)

// releaseGroup is the group a release name ends with after a hyphen, the
// hyphen against it: "[x264]-FraMeSToR", "x264-GROUP". "- Aliens", the hyphen
// set apart, is a title.
var releaseGroup = regexp.MustCompile(`(?:^|\s)-[^\s-]+\s*$`)

// ReleaseWord says whether a word after a film's year is a release's rather
// than a title's: an encode's, a source's or a sound's word (releaseJunk), an
// HDR, IMAX, 3D, language or dub tag, a stacked file's part written without a
// space ("cd1", "part1", "disc2") or a disc's ("Disc 2"), or an extra's
// ("Sample", "Trailer"), read part by part across a hyphen or a plus -
// "Bluray-1080p", "WEBDL-1080p", "Remux-2160p", "DTS-HD", "HDR10+",
// "German-DL", "x264-GROUP". "Part 2", the number set apart, is a title's:
// it can name a sequel.
func ReleaseWord(w string) bool {
	parts := strings.FieldsFunc(strings.ToLower(strings.Trim(w, "()[]")), func(r rune) bool { return r == '-' || r == '+' })

	return slices.ContainsFunc(parts, func(p string) bool { return releaseJunk[p] || releaseTags[p] || stackedPart.MatchString(p) })
}

// releaseTags are a release's words releaseJunk leaves out, as they end no
// episode's title in a release name: picture, presentation, 3D, language and
// dub tags, a disc's number, and an extra's word.
var releaseTags = map[string]bool{
	"imax": true, "hdr10plus": true, "dovi": true, "hlg": true, "rerip": true, "hybrid": true,
	"3d": true, "hsbs": true, "sbs": true, "hou": true, "ou": true, "htab": true, "tab": true,
	"dl": true, "german": true, "ger": true, "french": true, "truefrench": true, "vff": true, "vfq": true, "vfi": true,
	"vostfr": true, "vost": true, "italian": true, "ita": true, "spanish": true, "castellano": true, "latino": true,
	"dutch": true, "nordic": true, "swedish": true, "danish": true, "norwegian": true, "finnish": true, "polish": true,
	"russian": true, "hindi": true, "japanese": true, "korean": true, "english": true, "eng": true, "dub": true, "sub": true,
	"disc": true, "disk": true, "cd": true,
	"sample": true, "trailer": true, "teaser": true, "featurette": true,
}

// stackedPart is a stacked file's part written without a space: "cd1",
// "pt2", "part1", "disc1", "disk2", "dvd1".
var stackedPart = regexp.MustCompile(`^(?:cd|pt|part|disc|disk|dvd)\d+$`)

// SegmentYear is the year a file or folder name gives, 0 for none: a
// (bracketed) one, else the last bare one.
func SegmentYear(name string) int {
	// four digits either way, as the patterns read them: a number
	digits := ""
	if m := BracketedYear.FindStringSubmatch(name); m != nil {
		digits = m[1]
	} else if years := bareYear.FindAllStringSubmatch(name, -1); len(years) > 0 {
		digits = years[len(years)-1][2]
	}
	if y, err := strconv.Atoi(digits); err == nil {
		return y
	}

	return 0
}

// ClaimOf reads what a film's path claims it is: the file's own name when it
// gives a year - "Title (Year)", or a scene name's bare year - else the folder
// holding it when that is named "Title (Year)". A disc's stream names nothing,
// so for one it is the folder named for the disc. A name with no year
// ("movie.mkv", a collection's folder of several) claims too little to hold
// against a film, and false comes back.
func ClaimOf(path string, held []string) (Claim, bool) {
	file := TitledPath(path)
	p := file
	year := SegmentYear(FileExtension.ReplaceAllString(mediapath.Base(p), ""))
	if year == 0 {
		p = mediapath.Dir(file)
		if m := BracketedYear.FindStringSubmatch(mediapath.Base(p)); p != "" && m != nil {
			// four digits, as the pattern reads them: a number
			if y, err := strconv.Atoi(m[1]); err == nil {
				year = y
			}
		}
	}
	if year == 0 {
		return Claim{}, false
	}
	claim := Claim{Segment: mediapath.Base(p), Year: year, Score: -1}
	claim.Whole, claim.Before = YearParts(mediapath.Base(p))
	for _, name := range held {
		if title, score := TitleFromPath(p, name); score > claim.Score {
			claim.Title, claim.Score = title, score
		}
	}

	return claim, true
}

// Like says whether two titles are one, as the path audit's title check
// judges it (SameTitle): the same title written another way, never one with
// a number, a part or words the other lacks.
func Like(a, b string) bool {
	return b != "" && SameTitle(a, b)
}

// TitledPath is the part of a path a film's or a series' title is read from,
// as TitleFromPath reads it: its file or folder, or the folder a disc's files
// sit in.
func TitledPath(path string) string {
	for {
		base := FileExtension.ReplaceAllString(mediapath.Base(path), "")
		if !discFile.MatchString(base) && !IsDiscFolder(base) || mediapath.Dir(path) == "" {
			return path
		}
		path = mediapath.Dir(path)
	}
}

// SearchTerms are what TMDB is asked by for the title a path claims (see
// Claim.terms), read the title given when the path claims no year.
func SearchTerms(path string, held []string, read string) []string {
	claim, ok := ClaimOf(path, held)
	if !ok {
		return []string{read}
	}
	claim.Title = cmp.Or(read, claim.Title)

	return claim.Terms(held)
}
