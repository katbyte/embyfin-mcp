package naming

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/katbyte/go-kt/spelling"
)

// Turning a release name into a series the library holds.
//
// Files arrive named the way the scene names them - dots for spaces, the
// season and episode in the middle, and everything about the encode after
// it - and every client that has to match those against a library writes its
// own parser for them, badly. The library is the only side that knows both
// what the show is really called and what it is called elsewhere, so the
// matching belongs here.

// runStepSE is one more episode a SxxExx marker's run names, as the servers
// and the people naming files write one: E02 and xE02, -E02, -X02 and -02,
// +E02 and +02, " - E02", and the marker again (.S01E02, " - S01E02"). A bare number
// needs the hyphen or the plus, or "S02E01.1080p" would read as episodes 1
// to 108, and a bare number after a spaced dash is a title's ("S01E01 - 24
// Hours"), never a run's.
const runStepSE = `(?:[-+]x?e?|[. _x-]?e|\s+-\s+e|\s*\+\s*e|[. _-]?s\d{1,2}[. _x-]?e|\s+-\s+s\d{1,2}[. _x-]?e)\d{1,3}`

// runStepNx is one more episode a NNxNN marker's run names: x03, -03, +03,
// -x03, -e03, -01x03, " - x03", " - 01x03", and the marker again after a
// dot or a space (".S01x03", " 01x03"). The same rule for a bare number after
// a spaced dash.
const runStepNx = `(?:x|[-+]s?\d{1,2}x|[-+]x?e?|\s+-\s+(?:x|e|s?\d{1,2}x)|[. _]s?\d{1,2}x)\d{1,3}`

// releaseMarkers are the shapes that end a title and begin the rest of a
// release name, most specific first: the episode, then a season on its own.
// The two episode markers read a run of episodes after the first (see
// runStepSE and runStepNx), into their fourth group; runEnd reads it.
var releaseMarkers = []*regexp.Regexp{
	// SxxExx, S01.E01, S01xE01, and a run after it
	regexp.MustCompile(`(?i)(^|[^a-z0-9])s(\d{1,2})[. _x-]?e(\d{1,3})((?:` + runStepSE + `)*)([^a-z0-9]|$)`),
	// 1x02 and S01x02, and a run after it
	regexp.MustCompile(`(?i)(^|[^a-z0-9])s?(\d{1,2})x(\d{2})((?:` + runStepNx + `)*)([^a-z0-9]|$)`),
	regexp.MustCompile(`(?i)(^|[^a-z0-9])s(\d{1,2})([^a-z0-9]|$)`),
	regexp.MustCompile(`(?i)(^|[^a-z0-9])season[. _-]+(\d{1,2})([^a-z0-9]|$)`),
}

// runNumber is one number of a run as the markers admit them: the season
// again with its episode (S01E02, 01x02), or an episode on its own after an
// E, an x, a joiner or nothing.
var runNumber = regexp.MustCompile(`(?i)s(\d{1,2})[. _x-]?e(\d{1,3})|(\d{1,2})x(\d{1,3})|[ex]?(\d{1,3})`)

// runMax is the most episodes past the first a file is taken to hold:
// "1x02x264" is an encode, not 262 episodes.
const runMax = 20

// runEnd is the last episode of a run, or 0 when the marker numbered one
// episode. A repeat that names another season ends the run there: one file
// spanning two seasons has no single last episode to give.
func runEnd(season, first int, run string) int {
	last := 0
	for _, m := range runNumber.FindAllStringSubmatch(run, -1) {
		s, n := m[1]+m[3], m[2]+m[4]+m[5]
		if s != "" && atoi(s) != season {
			break
		}
		last = max(last, atoi(n))
	}
	if last <= first || last-first > runMax {
		return 0
	}

	return last
}

// releaseJunk are the words that only ever appear after a title: what the
// file was made from and with. A title that ends at one of these had no
// season marker to end at.
var releaseJunk = map[string]bool{}

func init() {
	for _, w := range []string{
		// resolution and shape
		"240p", "360p", "480p", "540p", "576p", "720p", "1080p", "1080i", "2160p", "4k", "uhd", "hd", "sd",
		// where it came from
		"hdtv", "pdtv", "web", "webrip", "web-dl", "webdl", "bluray", "blu-ray", "brrip", "bdrip",
		"dvdrip", "dvd", "hdrip", "remux", "amzn", "nf", "dsnp", "atvp", "hmax", "max", "cr",
		"crunchyroll", "itunes", "pcok", "stan", "ip", "all4", "uktv",
		// what it was encoded with
		"x264", "x265", "h264", "h265", "h", "hevc", "avc", "xvid", "divx", "av1", "vp9",
		"10bit", "8bit", "hi10p", "hdr", "hdr10", "dv", "sdr", "imax",
		// sound
		"aac", "aac2", "ac3", "eac3", "ddp", "ddp5", "dd5", "dts", "dts-hd", "truehd", "atmos", "flac", "opus", "mp3",
		// the rest
		"proper", "repack", "internal", "limited", "extended", "uncut", "complete", "multi",
		"dual", "dual-audio", "subbed", "dubbed", "subs", "batch",
	} {
		releaseJunk[w] = true
	}
}

// Release is what a release name says: the series it is for, and where in
// the run it sits when it says so.
type Release struct {
	Title      string
	Year       int
	Season     int
	Episode    int
	EpisodeEnd int
	// Run is the season and episode marker as the name writes it, the run
	// of episodes after the first included: "S01E01-E02", "02x47+48". "" for
	// a name with no episode marker. RunStyle reads its shape.
	Run string
	// HasSeason and HasEpisode say the name carried a season or an episode
	// number: 0 is a number too - S00 is the specials, E00 a pilot numbered
	// before the first - and read as "none" an S00E01 had no season at all
	HasSeason, HasEpisode bool
	// Unread is set when no title could be read out of the name - it is
	// all season, episode, encode and group - and Title is the name itself
	Unread bool
}

// sitePrefix is the index or tracker stamped on the front of a name before
// the show begins: "www.example.org    -    Some Show 2016 S07E02 ...". Folders
// fed from one source can carry it on most of their names, and left on it is
// four words of noise on the front of every title, so nothing matches.
//
// Two shapes, both anchored at the start and both needing a separator, so a
// title that merely contains dots ("The.Red.Green.Show") is never mistaken
// for a host: a www. host followed by anything, or a bare host whose top
// level is one anyone stamps names with, followed by a spaced dash.
var sitePrefix = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^[\[({\s]*www\.[a-z0-9][a-z0-9.-]*[a-z0-9][\])}\s]*[-–—_:|]+[\s.]*`),
	regexp.MustCompile(`(?i)^[\[({\s]*[a-z0-9][a-z0-9-]*\.(?:org|com|net|info|tv|me|cc|io|to|se|eu|is|xyz|club|party|site|link|online|pw|ws)[\])}\s]*\s+[-–—]+\s+`),
}

var (
	FileExtension = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|m4v|ts|m2ts|mts|vob|ssif|mov|wmv|mpg|mpeg|m2v|flv|webm|ogm|divx|iso|nfo)$`)
	BracketedYear = regexp.MustCompile(`[(\[]((?:19|20)\d{2})[)\]]`)
	bareYear      = regexp.MustCompile(`(^|[^a-z0-9])((?:19|20)\d{2})([^a-z0-9]|$)`)
	spaceRun      = regexp.MustCompile(`\s+`)
)

// ParseRelease reads a release name: the title, and the year, season and
// episode when the name carries them. A name that is already a plain title
// comes back as itself.
//
// A path is read segment by segment, because the parts of the answer are
// spread across them: "Severance (2022)/Season 01/Severance - S01E01 - ...".
// The title and year come from the last segment that names a show, and the
// season and episode from the last that numbers one, so a file named only
// "S01E01.mkv" still takes its title from the folder above it.
//
// A slash alone does not make a path, because titles have them: Sweet/Vicious,
// Nip/Tuck, 20/20. Read as a path those were "Vicious", "Tuck" and "20", and
// "Vicious" then matched a different show at 1.0. See LooksLikePath.
func ParseRelease(name string) Release {
	segments := []string{name}
	if LooksLikePath(name) {
		segments = splitPath(name)
	}

	var out Release
	for _, segment := range segments {
		seg := ParseSegment(segment)
		if seg.Title != "" {
			out.Title = seg.Title
		}
		if seg.HasSeason || seg.HasEpisode {
			out.Season, out.Episode, out.EpisodeEnd, out.Run = seg.Season, seg.Episode, seg.EpisodeEnd, seg.Run
			out.HasSeason, out.HasEpisode = seg.HasSeason, seg.HasEpisode
		}
		if seg.Year > 0 {
			out.Year = seg.Year
		}
	}
	if out.Title == "" {
		// a name that is all marker and encode still has to answer with
		// something a caller can see, rather than with nothing
		out.Unread = true
		out.Title = strings.TrimSpace(spaceRun.ReplaceAllString(strings.NewReplacer(".", " ", "_", " ").Replace(name), " "))
	}
	if out.Title == "" {
		out.Title = strings.TrimSpace(name)
	}

	return out
}

var (
	// seasonFolder is a folder that numbers a season and names nothing else
	seasonFolder = regexp.MustCompile(`(?i)^(?:season[. _-]*\d{1,3}|s\d{1,2}|specials)$`)
	// folderYear is how FileBot and Plex name a show's folder: the title and
	// the year in brackets, with nothing after it
	folderYear  = regexp.MustCompile(`[(\[](?:19|20)\d{2}[)\]]\s*$`)
	driveLetter = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
)

// LooksLikePath says whether a name with a slash in it is a path rather than a
// title that has one. A title is the safer reading when nothing says
// otherwise: a path read as a title scores low and is refused, while a title
// read as a path loses its first half and can match a different show outright.
//
// What does say otherwise is what no title carries: a backslash, a leading
// separator or drive letter, a file extension, a season folder, or a folder
// before the last that is named the way a show's folder is ("Severance
// (2022)/...") or numbers an episode (a release folder over its file).
func LooksLikePath(name string) bool {
	name = strings.TrimSpace(name)
	switch {
	case !strings.ContainsAny(name, `/\`):
		return false
	case strings.ContainsRune(name, '\\'), strings.HasPrefix(name, "/"), strings.HasPrefix(name, "./"),
		strings.HasPrefix(name, "../"), strings.HasPrefix(name, "~/"), driveLetter.MatchString(name):
		return true
	case FileExtension.MatchString(name):
		// a file's name cannot hold a slash, so everything before one is a folder
		return true
	}

	segments := splitPath(name)
	for i, seg := range segments {
		if seasonFolder.MatchString(strings.TrimSpace(seg)) {
			return true
		}
		if i == len(segments)-1 {
			continue
		}
		if folderYear.MatchString(seg) {
			return true
		}
		if p := ParseSegment(seg); p.HasSeason || p.HasEpisode {
			return true
		}
	}

	return false
}

func splitPath(name string) []string {
	return strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' })
}

// ParseSegment reads one path segment. Its title is empty when the segment
// holds no show name - a "Season 01" folder, or a bare "S01E01.mkv".
func ParseSegment(name string) Release {
	return ParseSegmentAsOf(name, LatestReleaseYear())
}

// ParseSegmentAsOf is ParseSegment with latest the latest year a release
// can be dated (see datingYear), for a reading as of a year.
func ParseSegmentAsOf(name string, latest int) Release {
	s := FileExtension.ReplaceAllString(strings.TrimSpace(name), "")
	for _, re := range sitePrefix {
		// only a pattern that matched ends the search, and only if it left
		// something behind: a name that is nothing but a host keeps its name
		if stripped := re.ReplaceAllString(s, ""); stripped != s && stripped != "" {
			s = stripped

			break
		}
	}

	var out Release
	cut := len(s)
	for i, re := range releaseMarkers {
		m := re.FindStringSubmatchIndex(s)
		if m == nil {
			continue
		}
		// the marker starts after whatever separator matched before it
		at := m[2*1+1]
		if at >= cut {
			continue
		}
		cut = at
		groups := re.FindStringSubmatch(s)
		switch i {
		case 0, 1: // SxxExx or 1x02, maybe a run of them: E01E02, E01-05, 1x02-03, S01E01.S01E02
			out.Season, out.Episode = atoi(groups[2]), atoi(groups[3])
			out.EpisodeEnd = runEnd(out.Season, out.Episode, groups[4])
			out.HasSeason, out.HasEpisode = true, true
			// the marker as written, from its first letter or digit to the
			// end of its run
			out.Run = s[at:m[2*4+1]]
		default: // Sxx or Season xx
			out.Season, out.HasSeason = atoi(groups[2]), true
		}
	}
	head := s[:cut]

	// the year, when the name gives one: parenthesised anywhere, or a bare
	// 19xx/20xx that is not the whole title (1600 Penn, 2012) - see
	// datingYear for which of several
	if m := BracketedYear.FindStringSubmatch(head); m != nil {
		out.Year = atoi(m[1])
		head = strings.Replace(head, m[0], " ", 1)
	} else if at := datingYear(head, latest); at > 0 {
		out.Year = atoi(head[at : at+4])
		head = head[:at] + " " + head[at+4:]
	}

	// dots and underscores stand in for spaces; hyphens do not, because a
	// title can be made of them (9-1-1)
	head = strings.NewReplacer(".", " ", "_", " ").Replace(head)
	head = spaceRun.ReplaceAllString(head, " ")

	// what is left may still run into the encode's words when the name had no
	// season marker at all. Only then: past a marker the head is the title,
	// and cutting it at a word that happens to also name a codec or a
	// streaming service is how "The Red Green Show" becomes "The Green Show".
	//
	// The first word is never junk either, whatever it spells. Shows are
	// called Max Headroom, Stan Against Evil, Web Therapy and Dual Survival,
	// and a rule that reads those as an encode leaves nothing to search for.
	words := strings.Fields(head)
	// two words or more and every one of them the encode's, the source's or
	// the sound's, the group riding on the last ("1080p.WEB-DL-GROUP"): a
	// name with no title in it. One word alone is a title, whatever it
	// spells: there are shows called Max
	if len(words) > 1 && !slices.ContainsFunc(words, func(w string) bool { return !EncodeWord(w) }) {
		return out
	}
	// an edition's words end a title with no season marker, and a
	// re-release's year with them: "Zzyzx.1982.Remastered.2021" is Zzyzx
	if e := editionAt(words); cut == len(s) && e > 0 {
		words = words[:e]
	}
	if cut == len(s) && len(words) > 1 {
		for i, w := range words[1:] {
			// the encode's words, and those joined by a hyphen or a plus
			// ("Remux-2160p", "HDR10+"), or with the group riding on one
			if !EncodeWord(w) {
				continue
			}
			// a single letter is junk only in front of a number: the h of
			// "H 264" ends a title, the H of "S H I E L D" is in one. Every
			// other junk word is junk wherever it stands.
			if word := strings.ToLower(strings.Trim(w, "()[]-")); len(word) == 1 && !startsWithDigit(words[min(i+2, len(words)-1)]) {
				continue
			}
			words = append(words[:i+1:i+1], editionsIn(words[i+1:])...)

			break
		}
	}
	out.Title = strings.Trim(strings.Join(words, " "), " -._([{")

	return out
}

// datingYear is where in a name the year that dates the release begins, -1
// for none: a bare 19xx or 20xx standing as a word of its own, never the
// name's first word (a title that is a year, 2012), never one after the
// encode's first word ("x264-2023"), and never one later than latest - next
// year: a later one is the title's own ("Blade Runner 2049") or a number.
// Of two years side by side the later dates it and the one before is the
// title's own ("Blade.Runner.2049.2017"); otherwise the first does, and a
// year after it - past an edition's words, a re-release's
// ("Remastered.2021") - dates nothing. Blind to the titles, as a release
// name is read: the path audit, which knows them, takes the later of two
// only when the earlier is a number of one (TitleAndYearFromPath).
func datingYear(s string, latest int) int {
	years := releaseYears(s, latest)
	for i, y := range years {
		if i+1 < len(years) && years[i+1].word == y.word+1 {
			return years[i+1].at
		}
	}
	if len(years) > 0 {
		return years[0].at
	}

	return -1
}

// LatestReleaseYear is the latest year a release can be dated: next year,
// by the clock.
func LatestReleaseYear() int {
	return time.Now().Year() + 1
}

// nameYear is a year standing as a word of a release name: where it begins,
// and which word it is.
type nameYear struct{ at, word int }

// releaseYears are the years of a name that may date its release: each a
// bare 19xx or 20xx word of its own, past the name's first word, before the
// encode's first word, and no later than latest (see datingYear). An
// edition's words end a title, not this search: "Zzyzx.Theatrical.Cut.1999"
// is dated 1999.
func releaseYears(s string, latest int) []nameYear {
	tokens := nameToken.FindAllStringIndex(s, -1)
	words := make([]string, 0, len(tokens))
	for _, t := range tokens {
		words = append(words, s[t[0]:t[1]])
	}
	stop := len(words)
	for i := 1; i < stop; i++ {
		// an edition's words - IMAX, 3D, Extended - name the release as
		// much as its encode, and come before its year as often as after
		if w := strings.ToLower(words[i]); EncodeWord(words[i]) && !editionish[w] {
			stop = i

			break
		}
	}
	var out []nameYear
	for i := 1; i < stop; i++ {
		if len(words[i]) == 4 && yearDigits.MatchString(words[i]) && IsNumber(words[i]) && YearAt(words[i], 0) <= latest {
			out = append(out, nameYear{at: tokens[i][0], word: i})
		}
	}

	return out
}

// editionish are the encode's words that name an edition, which a release
// year can follow.
var editionish = map[string]bool{"imax": true, "3d": true, "2d": true, "extended": true, "uncut": true, "unrated": true, "remastered": true}

// nameToken is a word of a release name, between its spaces, points and
// underscores.
var nameToken = regexp.MustCompile(`[^\s._]+`)

// editionAt is where an edition's words begin among a name's, -1 for none:
// Remastered, Re-Edit, Unrated, or a Director's, Directors, Final,
// Theatrical or Extended Cut - with the article before it ("Blade Runner
// The Final Cut"). Never one that would leave the title nothing but an
// article: "The Final Cut" and "A Directors Cut" are titles.
func editionAt(words []string) int {
	lower := func(i int) string { return strings.ToLower(strings.Trim(words[i], "()[]-,:")) }
	article := func(i int) bool { a := lower(i); return a == "the" || a == "a" || a == "an" }
	for i := 1; i < len(words); i++ {
		w := lower(i)
		cut := i+1 < len(words) && lower(i+1) == "cut" && (w == "directors" || w == "director's" || w == "final" || w == "theatrical" || w == "extended")
		if !cut && w != "remastered" && w != "re-edit" && w != "reedit" && w != "unrated" {
			continue
		}
		at := i
		if article(i - 1) {
			at = i - 1
		}
		// what the edition would leave: nothing, or only an article, and
		// the edition's words are the title's
		if at == 0 || at == 1 && article(0) {
			continue
		}

		return at
	}

	return -1
}

// editionWords are the words of an edition a title keeps, the encode's words
// around them or not: "Hubble IMAX 3D" is Hubble 3D.
var editionWords = map[string]bool{"3d": true, "2d": true}

// editionsIn are the edition's words among an encode's, up to the first
// word that is neither: what a title keeps of the words it was cut at.
func editionsIn(tail []string) []string {
	var out []string
	for _, w := range tail {
		switch word := strings.ToLower(strings.Trim(w, "()[]-")); {
		case editionWords[word]:
			out = append(out, w)
		case !EncodeWord(w):
			return out
		}
	}

	return out
}

// EncodeWord says whether a word of a release name is one of the encode's,
// the source's or the sound's, or one of them with the group after a hyphen
// (x264-NGP, WEB-DL-GROUP).
func EncodeWord(w string) bool {
	w = strings.ToLower(strings.Trim(w, "()[]-"))
	if releaseJunk[w] || encodeParts(w, releaseJunk) {
		return true
	}
	i := strings.LastIndex(w, "-")

	return i > 0 && releaseJunk[w[:i]]
}

// encodeParts says whether a word joined of parts by "-" or "+" is every
// part one of words: "Remux-2160p", "WEBDL-1080p", "HDR10+", "DTS-HD".
func encodeParts(w string, words map[string]bool) bool {
	parts := strings.FieldsFunc(w, func(r rune) bool { return r == '-' || r == '+' })

	return len(parts) > 0 && !slices.ContainsFunc(parts, func(p string) bool { return !words[p] })
}

func startsWithDigit(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)

	return n
}

// notAlphanumeric is everything in a title that is not a letter or a digit,
// in any script. It used to be everything outside a-z and 0-9, which folded
// every Japanese, Russian and Greek title to nothing - and nothing scores 0
// against itself, so 千と千尋の神隠し could not be resolved even by its own name.
var notAlphanumeric = regexp.MustCompile(`[^\p{L}\p{Nd}]+`)

// combiningMarks are accents written as a letter of their own after the one
// they sit on. They are dropped rather than turned into a space, or a title
// spelled with them would split in two where the accent was.
var combiningMarks = regexp.MustCompile(`\p{M}+`)

// foldScript folds the accents of other scripts the way FoldAccents folds
// Latin ones, for the same reason: they are dropped more often than not. Greek
// writes its accents only in lower case, so "ΟΔΥΣΣΕΙΑ" and "Οδύσσεια" are one
// title, and Russian writes ё as е as often as not.
var foldScript = map[rune]string{
	'ά': "α", 'έ': "ε", 'ή': "η", 'ί': "ι", 'ϊ': "ι", 'ΐ': "ι", 'ό': "ο", 'ύ': "υ", 'ϋ': "υ", 'ΰ': "υ", 'ώ': "ω",
	// the final sigma is the same letter as σ, written at the end of a word
	'ς': "σ",
	'ё': "е",
}

// initialism is a title's dotted acronym: the P.D. of "Chicago P.D.", the
// S.W.A.T., the S.H.I.E.L.D. Scene naming drops those points as reliably as
// it drops apostrophes, and the two spellings have to meet somewhere.
//
// They cannot meet at the punctuation strip that follows, because that turns
// "p.d." into "p d" and "pd" stays "pd": two words against one, which scores
// as a different show rather than a worse match. That is what made this one
// worse than the apostrophes - "Chicago P.D." did not merely rank low against
// a search for "Chicago PD", it ranked below Chicago Fire, Hope, Justice and
// Med, which share a word with it.
var initialism = regexp.MustCompile(`(?:\b[a-z]\.){2,}`)

// spacedInitialism is the same acronym after a release name has had its dots
// turned into spaces: "Marvels.Agents.of.S.H.I.E.L.D" arrives as single
// letters in a row. Both spellings have to land on the same word as the
// library's "S.H.I.E.L.D.", so a run of single letters closes up too.
var spacedInitialism = regexp.MustCompile(`\b[a-z](?: [a-z])+\b`)

// FoldAccents strips the diacritics off a title, by the table the spelling
// audit folds by (go-kt's spelling.FoldLetter). Scene naming drops them as reliably as it drops
// apostrophes - "90 Day Fiancé" arrives as "90 Day Fiance" - and the servers'
// own search folds them too, so a scorer that does not ranks the very series
// the search just found at 0.5 and refuses to commit to it.
func FoldAccents(s string) string {
	if isASCII(s) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 128 {
			b.WriteRune(r)

			continue
		}
		if folded := spelling.FoldLetter(r); folded != "" {
			b.WriteString(folded)

			continue
		}
		if folded, ok := foldScript[r]; ok {
			b.WriteString(folded)

			continue
		}
		b.WriteRune(r) // not a letter we know: leave it for notAlphanumeric
	}

	return b.String()
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 128 {
			return false
		}
	}

	return true
}

// Normalise folds a title to what two spellings of the same show have in
// common: case, the ampersand written out, apostrophes, and every other mark
// that a release name and a library differ on. Letters and digits of every
// script survive it; only Latin and Greek letters have accents to lose.
func Normalise(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "&", " and ")
	s = strings.NewReplacer("'", "", "’", "", "`", "").Replace(s)
	s = FoldAccents(s)
	s = initialism.ReplaceAllStringFunc(s, func(m string) string { return strings.ReplaceAll(m, ".", "") })
	s = combiningMarks.ReplaceAllString(s, "")
	s = notAlphanumeric.ReplaceAllString(s, " ")
	s = strings.TrimSpace(spaceRun.ReplaceAllString(s, " "))
	s = spacedInitialism.ReplaceAllStringFunc(s, func(m string) string { return strings.ReplaceAll(m, " ", "") })

	return s
}

// WithoutThe drops a leading article, which a library and a release name
// disagree about often enough to be worth a second look.
func WithoutThe(s string) string {
	for _, article := range []string{"the ", "a ", "an "} {
		if rest, ok := strings.CutPrefix(s, article); ok {
			return rest
		}
	}

	return s
}

// acronymScore matches a name that abbreviates what the library spells out,
// or the other way round: "Law and Order SVU" against "Law & Order: Special
// Victims Unit", "CSI NY" against "CSI: New York". The abbreviation is
// usually the only thing saying WHICH show it is, so reading it is the
// difference between the right series and its parent.
func acronymScore(q, c string) (score float64, matchedOn string) {
	qw, cw := strings.Fields(q), strings.Fields(c)

	shared := 0
	for shared < len(qw) && shared < len(cw) && qw[shared] == cw[shared] {
		shared++
	}
	if shared == 0 {
		return 0, ""
	}

	short, long := qw[shared:], cw[shared:]
	if len(short) > len(long) {
		short, long = long, short
	}
	// one word against the several it stands for
	if len(short) != 1 || len(long) < 2 {
		return 0, ""
	}

	var initials strings.Builder
	for _, w := range long {
		// the first letter, not the first byte: a Cyrillic word's first byte
		// is half of one
		first, _ := utf8.DecodeRuneInString(w)
		initials.WriteRune(first)
	}
	if initials.String() != short[0] {
		return 0, ""
	}

	// not quite an exact title, because the two sides do not literally agree
	return 0.95, "title with the acronym spelled out"
}

// dice is the overlap of two word lists: twice what they share over what
// they hold between them, so a long title and a short one that share
// everything the short one has still score below an exact match.
func dice(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	rest := slices.Clone(b)
	shared := 0
	for _, w := range a {
		if i := slices.Index(rest, w); i >= 0 {
			shared++
			rest = slices.Delete(rest, i, i+1)
		}
	}

	return 2 * float64(shared) / float64(len(a)+len(b))
}

// Score is how sure we are that two titles name the same show, and what
// made us think so. 1 is the same title once both are folded.
func Score(query, candidate string) (score float64, matchedOn string) {
	return FoldedScore(Normalise(query), Normalise(candidate))
}

// FoldedScore is Score over two titles already folded (Normalise),
// for a caller comparing one title against many to fold each once.
func FoldedScore(q, c string) (score float64, matchedOn string) {
	if q == "" || c == "" {
		return 0, ""
	}
	if q == c {
		return 1, "title"
	}
	if WithoutThe(q) == WithoutThe(c) {
		return 0.97, "title without the article"
	}

	if score, how := acronymScore(q, c); score > 0 {
		return score, how
	}

	// a prefix match is not one fact but two, and they point opposite ways.
	if strings.HasPrefix(c, q+" ") {
		// the library's title carries words the name does not: "Doctor Who"
		// against "Doctor Who Confidential". Right as far as it goes, and
		// wrong often enough not to be certain.
		return 0.80 + 0.15*float64(len(q))/float64(len(c)), "title prefix"
	}
	if strings.HasPrefix(q, c+" ") {
		// the NAME carries words the library's title does not, which is how
		// a spin-off matches its parent: "Law and Order SVU" against "Law &
		// Order", "CSI Miami" against "CSI". The extra words are the whole
		// point of the name - they are what says which show it is - so this
		// has to score below anything a caller would act on unattended.
		return math.Min(0.65, 0.55+0.1*float64(len(c))/float64(len(q))), "the library's title is a prefix of this name, which is how a spin-off matches its parent"
	}

	if d := dice(strings.Fields(q), strings.Fields(c)); d > 0 {
		return 0.75 * d, "words in common"
	}

	return 0, ""
}
