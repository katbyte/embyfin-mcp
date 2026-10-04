package naming

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/mediapath"
)

var pathYearRe = regexp.MustCompile(`\((19|20)\d\d\)`)

// DiscStructures are the folders a server holds a whole disc as, which is the
// arrangement that works: an item inside one is the server reaching past the
// disc into its parts.
var DiscStructures = []string{"BDMV", "VIDEO_TS", "AUDIO_TS"}

// TitleFromPath reads the title a film's or a series' path claims and how
// close it is to the name held (see TitleAndYearFromPath).
func TitleFromPath(path, name string) (claimed string, score float64) {
	claimed, score, _ = TitleAndYearFromPath(path, name, NumbersOf(name))

	return claimed, score
}

// NamingSegment is the segment of a path that names a film or a series: the
// last, without its extension, or the folder above a disc's own files.
func NamingSegment(path string) string {
	// the path is the server's, so split on either separator: one on Windows
	// answers with backslashes, whatever this runs on
	base := FileExtension.ReplaceAllString(mediapath.Base(path), "")
	// a disc's own files name nothing, nor do the folders a disc keeps them
	// in: the folder above those is the one named for the film. Read as a
	// title, VTS_01_1.VOB was "VTS 01 1", and every loose DVD a mismatch.
	for (discFile.MatchString(base) || IsDiscFolder(base)) && mediapath.Dir(path) != "" {
		path = mediapath.Dir(path)
		base = mediapath.Base(path)
	}

	return base
}

// Reading is one way to read a scene name: the title before a year,
// and that year.
type Reading struct {
	Title string
	Year  int
}

// TwoReadings are the two ways a scene name opening on two years side by
// side reads when the earlier is no number of a title the item goes by
// (own): the title dated by the first ("Zzyzx Race" 2000), or the title
// through the first dated by the second ("Zzyzx Race 2000" 1975). The names
// alone can't tell which. ok is false for any other name, or one with a
// bracketed year.
func TwoReadings(path string, own map[int]bool) (first, second Reading, ok bool) {
	base := NamingSegment(path)
	if PathYear(path) != 0 || BracketedYear.MatchString(base) {
		return first, second, false
	}
	years := releaseYears(base, LatestReleaseYear())
	if len(years) < 2 || years[1].word != years[0].word+1 || own[YearAt(base, years[0].at)] {
		return first, second, false
	}
	title := func(s string) string {
		return strings.Trim(strings.TrimSpace(spaceRun.ReplaceAllString(strings.NewReplacer(".", " ", "_", " ").Replace(s), " ")), " -_([{")
	}
	first = Reading{title(base[:years[0].at]), YearAt(base, years[0].at)}
	second = Reading{title(base[:years[1].at]), YearAt(base, years[1].at)}

	return first, second, first.Title != ""
}

// NumbersOf are the numbers the titles carry, as years: those of every title
// an item goes by are its own, and date no file.
func NumbersOf(titles ...string) map[int]bool {
	out := map[int]bool{}
	for _, title := range titles {
		for _, n := range FormOf(title).Numbers {
			if y, err := strconv.Atoi(n); err == nil {
				out[y] = true
			}
		}
	}

	return out
}

// TitleAndYearFromPath reads the title a film's or a series' path claims,
// how close it is to the name held, and the year it was cut at, 0 for none:
// the last segment, cut at its (year) when it has one, else at a bare year a
// scene name gives ("Title.2011.1080p"), else at the encode's words. A title
// that is itself a year, or ends in one (2012, Blade Runner 2049), is tried
// uncut too, and the reading numbered as the name, then the closer, wins.
// own are the numbers of the titles the item goes by (NumbersOf): of two
// years side by side the earlier is the title's own only when it is one of
// them ("Blade.Runner.2049.2017"), and otherwise the first dates the file
// ("Zzyzx.1982.2021").
func TitleAndYearFromPath(path, name string, own map[int]bool) (claimed string, score float64, year int) {
	base := NamingSegment(path)
	// of two readings, the one numbered as the name is wins, then the closer:
	// "Blade.Runner.2049.2017" read up to its first year is "Blade Runner",
	// alike but numbered apart
	best, bestScore, bestNumbered, bestYear, pairedYear := "", -1.0, false, 0, 0
	try := func(candidate string, cutAt int) {
		candidate = strings.Trim(strings.TrimSpace(spaceRun.ReplaceAllString(strings.NewReplacer(".", " ", "_", " ").Replace(candidate), " ")), " -_([{")
		if candidate == "" {
			return
		}
		score, _ := Score(candidate, name)
		numbered := SameNumbering(candidate, name)
		if numbered != bestNumbered && numbered || numbered == bestNumbered && score > bestScore {
			best, bestScore, bestNumbered, bestYear = candidate, score, numbered, cutAt
		}
	}
	latest := LatestReleaseYear()
	if m := BracketedYear.FindStringSubmatchIndex(base); m != nil {
		// a bracketed year later than next year sets the title apart but
		// dates nothing ("Zzyzx (2049)")
		y := YearAt(base, m[2])
		if y > latest {
			y = 0
		}
		try(base[:m[0]], y)
		// "Franchise (1971) - Title": the title follows the year, and the
		// name without the year is the franchise's and the title's
		if rest := base[m[1]:]; strings.TrimSpace(rest) != "" {
			try(rest, y)
			try(base[:m[0]]+" "+rest, y)
		}
	} else {
		// cut at the year that dates the release: the first, or of two
		// side by side the later when the earlier is a number of a title
		// the item goes by ("Zzyzx.Race.2000.1975" of "Zzyzx Race 2000").
		// The release reader takes the later of any two, blind to the
		// titles; read so, "Zzyzx.1982.2021" of "Zzyzx" (1982) was
		// numbered apart and dated 2021
		if years := releaseYears(base, latest); len(years) > 0 {
			at := years[0].at
			for i, y := range years {
				if i+1 < len(years) && years[i+1].word == y.word+1 && own[YearAt(base, y.at)] {
					// the later dates the file whatever the reading: the
					// one before it is the title's own
					at = years[i+1].at
					pairedYear = YearAt(base, at)

					break
				}
			}
			try(base[:at], YearAt(base, at))
		}
		rel := ParseSegment(base)
		try(rel.Title, rel.Year)
	}
	// a title ending in a year the reading cut off ("Zzyzx Race 2000.mkv",
	// "Blade.Runner.2049.1080p"): read whole, up to the encode's words, it
	// is the name's, and the year its own
	if best == "" || !bestNumbered {
		words := strings.Fields(strings.NewReplacer(".", " ", "_", " ").Replace(base))
		try(strings.Join(words[:EncodeStarts(words)], " "), 0)
	}
	if best == "" {
		try(base, 0)
	}
	// a cut at a year is the release year only when the reading it leaves
	// is numbered as the name: a cut that took the title's own year off
	// ("Blade Runner" of "Blade Runner 2049") dated nothing
	if !bestNumbered {
		bestYear = 0
	}

	return best, bestScore, cmp.Or(pairedYear, bestYear)
}

// YearAt is the four-digit year a name holds at an index, 0 for none.
func YearAt(s string, at int) int {
	if at < 0 || at+4 > len(s) {
		return 0
	}
	year, err := strconv.Atoi(s[at : at+4])
	if err != nil {
		return 0
	}

	return year
}

// yearDigits is a run of four digits that could be a year.
var yearDigits = regexp.MustCompile(`(?:19|20)\d{2}`)

// discFile is a disc's own stream or title-set file, which names nothing:
// VTS_01_1.VOB, VIDEO_TS.IFO, 00000.m2ts.
var discFile = regexp.MustCompile(`(?i)^(vts_\d+_\d+|video_ts|\d{5})(\.(ifo|bup))?$`)

// IsDiscFolder is a folder a disc keeps its files in (see DiscStructures),
// or a Blu-ray's STREAM folder inside BDMV.
func IsDiscFolder(name string) bool {
	return strings.EqualFold(name, "STREAM") || slices.ContainsFunc(DiscStructures, func(s string) bool { return strings.EqualFold(name, s) })
}

// PathYear is the last (year) in a path, 0 for none: the item's own, as
// checkYearMismatch reads it. One later than next year is none - a title's
// own or a number ("Zzyzx (2049)") - and no year above it stands in: a
// collection's folder carries its first film's.
func PathYear(path string) int {
	years := pathYearRe.FindAllString(path, -1)
	if len(years) == 0 {
		return 0
	}
	year, err := strconv.Atoi(strings.Trim(years[len(years)-1], "()"))
	if err != nil || year > LatestReleaseYear() {
		return 0
	}

	return year
}

// EpisodeTitlesFromFile reads the titles a file name claims, which are
// whatever follows the season and episode marker once the extension and the
// encode's words are off: "Show - 01x01 - The DVD.mkv" claims "The DVD". The
// first is the one to show; the rest are other readings of the same name,
// any of which the server's title may be. story is a serial's story alone,
// when the name gives its part number, which counts only against a server
// title that gives no part number of its own. It returns nothing for a name
// that claims no title, which is most of a tidy library.
//
// Read as one string, four shapes of name were titles no episode has:
//   - a story's number in brackets after the marker, and the part of a
//     serial after its story: "02x18 (013) - The Story (3) - The Part" read
//     as "013) - The". The number is dropped, and the part and the story
//     with its part number are each a title it claims
//   - a run of episodes numbered the NxNN way, "02x47-48 - A & B", once
//     read as "48 - A & B": the second number is the run's, which the
//     marker now reads, not the title's
//   - a file holding a run, titled "A & B": each of A and B is a title it
//     claims, so it matches either episode's. Only a run: "Pride & Zzyzx"
//     is one title, and its "Pride" is not
//   - release words after the title that no encode mark follows, "Half Loop
//     PROPER": the title without them is a reading too, though not the one
//     shown, since titles end on such words as well ("The DVD")
func EpisodeTitlesFromFile(path string) (titles []string, story string) {
	name := FileExtension.ReplaceAllString(mediapath.Base(path), "")
	run := ParseSegment(name).EpisodeEnd > 0

	var after string
	for _, re := range releaseMarkers {
		m := re.FindStringIndex(name)
		if m == nil {
			continue
		}
		after = name[m[1]:]

		break
	}
	after = strings.TrimLeft(after, " -_.")
	after = strings.TrimLeft(storyNumber.ReplaceAllString(after, ""), " -_.")
	if after == "" {
		return nil, ""
	}

	// the same cleanup a release name gets: dots and underscores are spaces,
	// and the encode's words end a title
	words := strings.Fields(strings.NewReplacer(".", " ", "_", " ").Replace(after))
	words = words[:EncodeStarts(words)]
	title := strings.Trim(strings.Join(words, " "), " -_([{")
	if title == "" {
		return nil, ""
	}

	titles = []string{title}
	add := func(t string) {
		if t = strings.Trim(strings.TrimSpace(t), " -_([{"); t != "" && !slices.Contains(titles, t) {
			titles = append(titles, t)
		}
	}
	// the title without the release words it ends on is the one shown, the
	// first word kept and never an article alone; the whole is a reading too
	bare := len(words)
	for bare > 1 && EncodeWord(words[bare-1]) {
		bare--
	}
	if lone := strings.ToLower(words[0]); bare < len(words) && (bare > 1 || lone != "the" && lone != "a" && lone != "an") {
		titles = []string{strings.Trim(strings.Join(words[:bare], " "), " -_([{"), title}
	}
	for _, t := range slices.Clone(titles) {
		if m := serialPart.FindStringSubmatch(t); len(m) > 3 && !slices.Contains(titles, m[3]) {
			// the part is what a server calls the episode, most often
			titles = append([]string{m[3]}, titles...)
			add(m[1] + " (" + m[2] + ")")
			story = m[1]
		}
	}
	if run {
		for _, t := range slices.Clone(titles) {
			if parts := strings.Split(t, "&"); len(parts) == 2 {
				add(parts[0])
				add(parts[1])
			}
		}
	}

	return titles, story
}

var (
	// storyNumber is a story's or a production number in brackets, set
	// before an episode's title: "(013)"
	storyNumber = regexp.MustCompile(`^[(\[]\d{1,4}[)\]]`)
	// serialPart is a serial's story, its part number, and the part's own
	// title: "The Story (3) - The Part"
	serialPart = regexp.MustCompile(`^(.+?)\s*\((\d{1,2})\)\s*-\s*(.+)$`)
)

// encodeMarks are the words of an encode that no title ends on: a
// resolution, a codec, a source, a sound. The rest of the encode's words
// (releaseJunk) are words titles have too - The Web Planet, The DVD, Max -
// and only end a title when an encode mark follows them.
var encodeMarks = map[string]bool{}

func init() {
	for _, w := range []string{
		"480p", "540p", "576p", "720p", "1080p", "1080i", "2160p", "4k", "uhd",
		"hdtv", "pdtv", "webrip", "web-dl", "webdl", "bluray", "blu-ray", "brrip", "bdrip", "dvdrip", "hdrip", "remux",
		"x264", "x265", "h264", "h265", "hevc", "avc", "xvid", "divx", "av1", "vp9", "10bit", "8bit", "hi10p",
		"aac", "aac2", "ac3", "eac3", "ddp", "ddp5", "dd5", "dts", "dts-hd", "truehd", "atmos", "flac",
	} {
		encodeMarks[w] = true
	}
}

// EncodeStarts is where the encode's words begin in a file's title words, or
// len(words) when they do not: at the first encode mark, or at an earlier run
// of the encode's words that only such words follow, one of them a mark.
func EncodeStarts(words []string) int {
	mark := func(w string) bool {
		w = strings.ToLower(strings.Trim(w, "()[]-"))
		if encodeMarks[w] || encodeParts(w, encodeMarks) {
			return true
		}
		i := strings.LastIndex(w, "-")

		return i > 0 && encodeMarks[w[:i]]
	}
	first := slices.IndexFunc(words, mark)
	if first < 0 {
		return len(words)
	}
	start := first
	for start > 0 && EncodeWord(words[start-1]) {
		start--
	}

	return start
}
