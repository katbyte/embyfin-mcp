package tools

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
)

// Every title a film or a series goes by, and the letters that only look
// like a title's.
//
// A path names a film by whatever title whoever placed it used: the one on
// the poster where they live, the original one, a sort name. The server
// holds one name, and the provider knows the rest. So a path is only said to
// name another film when its title is none of them - the item's name, its
// original title, its sort name, and, with a TMDB token, every alternative
// title TMDB lists for its id - and when it is not, TMDB is asked what the
// path's title and year do name, which is the evidence a caller acts on.

// How a title is known to an item.
const (
	titleAsName     = "name"
	titleAsOriginal = "original title"
	titleAsSort     = "sort name"
)

// knownTitle is one title an item goes by, and which.
type knownTitle struct {
	title, as string
}

// knownTitles are the titles an item goes by: its name, spelled plainly when
// it carries a letter that only looks Latin; its original title, which a
// folder in the film's own language names; and its sort name. Each comes
// without the year a server leaves on a film it could not match ("Cube
// (1997)"), because a path's title is read cut at its year.
func knownTitles(it *embyfin.Item) []knownTitle {
	_, plain := lookalikeLetters(it.Name)
	var out []knownTitle
	for _, k := range []knownTitle{{plain, titleAsName}, {it.OriginalTitle, titleAsOriginal}, {it.SortName, titleAsSort}} {
		title := strings.TrimSpace(seriesNameYear.ReplaceAllString(k.title, ""))
		if title == "" {
			title = strings.TrimSpace(k.title)
		}
		if title != "" {
			out = append(out, knownTitle{title, k.as})
		}
	}

	return out
}

// titleForm is a title read once for comparing: folded, its words as
// titleWords spells them, the numbers it carries, and whether it closes on a
// part 1 marked as one.
type titleForm struct {
	folded   string   // normaliseTitle
	words    []string // titleWords
	canon    string   // the words, one string
	numbers  []string // each number word's digits, in order
	numbered bool     // any word a number
	// partWord says a part word ("Part", "Chapter", "Volume") or a bracketed
	// number numbers the title: never "only a number" then
	partWord bool
	// partOne says the title closes on a part 1 marked as one - after a part
	// word, or bracketed - which a title without it names as well ("Dune:
	// Part One", "Pilot (1)"); a closing "One" or "1" of the name itself
	// ("Air Force One", "Apollo 1") is not one
	partOne bool
	// qualifier is the country or the year a title closes on in brackets,
	// "US" of "The Zzyzx (US)": a qualifier on one side only is the same
	// title, and another on each may be another country's version of it
	qualifier string
	// raw is the title as written, its qualifier off: what a finding quotes
	raw string
	// maybe is the title without a closing country's code it kept as a word
	// because the title is that word too ("It (IT)"): either reading may be
	// the name
	maybe *titleForm
}

// titleQualifier is a closing year or capitals in brackets: "(US)", "(UK)",
// "[2005]". The capitals qualify a title only when they are a country's
// (countryCodes): "(OVA)", "(TV)", "(DC)" are words the title has.
var titleQualifier = regexp.MustCompile(`\s*[(\[]\s*((?:19|20)\d{2}|[A-Z]{2,3})\s*[)\]]\s*$`)

// countryCodes are the countries a title closes on to tell one country's
// version of a show from another's: "The Office (US)", "(UK)", "(AU)".
var countryCodes = map[string]bool{
	"US": true, "USA": true, "UK": true, "GB": true, "AU": true, "NZ": true, "CA": true, "IE": true,
	"FR": true, "DE": true, "IT": true, "ES": true, "PT": true, "NL": true, "BE": true, "DK": true,
	"SE": true, "NO": true, "FI": true, "IS": true, "PL": true, "RU": true, "JP": true, "KR": true,
	"CN": true, "HK": true, "TW": true, "IN": true, "BR": true, "MX": true, "AR": true, "ZA": true,
}

// qualifierOf is a title's closing country or year in brackets, and the
// title without it; "" when it closes on neither. A country's code that is
// also the title's one word ("It (IT)", "No (NO)") may be part of the name
// and is kept: maybe is then the title without it, for judgeTitles to say
// it can't tell.
func qualifierOf(s string) (qualifier, rest, maybe string) {
	m := titleQualifier.FindStringSubmatchIndex(s)
	if len(m) < 4 {
		return "", s, ""
	}
	q := s[m[2]:m[3]]
	switch {
	case isNumber(q):
		return q, s[:m[0]], ""
	case countryCodes[q] && normaliseTitle(s[:m[0]]) == strings.ToLower(q):
		return "", s, s[:m[0]]
	case countryCodes[q]:
		return q, s[:m[0]], ""
	}

	return "", s, ""
}

// formOf reads a title into a titleForm.
func formOf(s string) titleForm {
	// a closing country or year tells two of a name apart - "The Zzyzx
	// (US)", "The Zzyzx (2005)" - and is no word of the title
	qualifier, s, maybe := qualifierOf(s)
	f := wordsOf(s)
	f.qualifier = qualifier
	if maybe != "" {
		without := wordsOf(maybe)
		f.maybe = &without
	}

	return f
}

// wordsOf reads a title, its qualifier already off, into a titleForm.
func wordsOf(s string) titleForm {
	words, marked := titleWords(s)
	f := titleForm{folded: normaliseTitle(s), words: words, canon: strings.Join(words, " "), raw: s}
	for i, w := range words {
		if countsAsNumber(w) {
			f.numbered = true
			f.numbers = append(f.numbers, strings.Join(digitRun.FindAllString(w, -1), " "))
		}
		if marked[i] {
			f.partWord = true
		}
	}
	if n := len(words); n > 0 && words[n-1] == "1" && marked[n-1] {
		f.partOne = true
	}

	return f
}

// titleVerdict is what two titles are to each other.
type titleVerdict int

const (
	// titlesDifferent are two titles
	titlesDifferent titleVerdict = iota
	// titlesSame are one title, however written
	titlesSame
	// titlesNumberedApart are one title but for a number on one side and
	// not the other, or another on each: another part, another sequel, or a
	// number that is part of the name ("Air Force One")
	titlesNumberedApart
	// titlesCantTell part on a closing letter one has and the other lacks
	// ("Henry" and "Henry V", "X" and "10"): a numeral or a name
	titlesCantTell
)

func (v titleVerdict) String() string {
	return [...]string{"different", "the same", "numbered apart", "can't tell"}[v]
}

// sameTitle says whether two titles are one title (see judgeTitles).
func sameTitle(a, b string) bool {
	return alike(formOf(a), formOf(b))
}

// alike says whether two titles are one title (see judgeTitles).
func alike(a, b titleForm) bool {
	return judgeTitles(a, b) == titlesSame
}

// judgeTitles says what two titles are to each other. The same title when
// alike enough to act on (titleScore, over the words as titleWords spells
// them, so "Part 2", "Pt. 2", "(2)" and a closing "II" read alike) or
// written another way (variantOf), their numbers agreeing (numbersAgree).
// Numbered apart when only their numbers part them: "Part 2" beside the
// title alone scored as the title with a word added, and a film's first part
// passed for its second. Can't tell when a closing letter parts them ("Henry"
// and "Henry V"). And a title that is nothing but its number is its number
// ("Ten" and "10" are two films).
func judgeTitles(a, b titleForm) titleVerdict {
	v := judgeQualified(a, b)
	// a country's code kept as a word the title may have ("It (IT)"): when
	// the title without it is the other, either reading may be the name
	if v != titlesSame && (a.maybe != nil || b.maybe != nil) {
		x, y := a, b
		if a.maybe != nil {
			x = *a.maybe
		}
		if b.maybe != nil {
			y = *b.maybe
		}
		if judgeQualified(x, y) == titlesSame {
			return titlesCantTell
		}
	}

	return v
}

// judgeQualified is judgeTitles with the titles' qualifiers weighed: the
// same title's country or year set apart as judgeTitles says.
func judgeQualified(a, b titleForm) titleVerdict {
	v := judgeWords(a, b)
	// the same title, qualified apart: another country each side may be the
	// other country's version of it, and another year each side another of
	// the name
	if v == titlesSame && a.qualifier != "" && b.qualifier != "" && a.qualifier != b.qualifier {
		// a year one either side is the same title, as a path's year one
		// either side of the item's is the same film
		if isNumber(a.qualifier) && isNumber(b.qualifier) {
			if x, y := yearAt(a.qualifier, 0), yearAt(b.qualifier, 0); x-y > 1 || y-x > 1 {
				return titlesNumberedApart
			}

			return titlesSame
		}
		if !isNumber(a.qualifier) && !isNumber(b.qualifier) {
			return titlesCantTell
		}
	}

	return v
}

// judgeWords is judgeTitles over the titles' words, their qualifiers aside.
func judgeWords(a, b titleForm) titleVerdict {
	if letterApart(a.words, b.words) {
		return titlesCantTell
	}
	if !numbersAgree(a, b) {
		if len(a.words) > 0 && slices.Equal(unnumbered(a.words), unnumbered(b.words)) {
			return titlesNumberedApart
		}

		return titlesDifferent
	}
	if onlyNumbers(a) && onlyNumbers(b) && a.folded != b.folded {
		return titlesDifferent
	}
	// both ways round, so the verdict is the pair's and not the order's:
	// one title with a word added ("Plan" and "Plan A", a spin-off's short
	// title and its parent's name) scored alike read the one way only
	ab, _ := foldedScore(a.canon, b.canon)
	ba, _ := foldedScore(b.canon, a.canon)
	if min(ab, ba) >= seriesConfident {
		return titlesSame
	}
	if _, ok := variantOf(a, b); ok {
		return titlesSame
	}

	return titlesDifferent
}

// wordsAdded says whether one title is the other with words added before or
// after it - an edition ("Alien Director's Cut"), a subtitle ("... The Final
// Cut", "Mononoke-hime - Princess Mononoke"), or another film - and which
// words, as the title writes them; longer is 1 when a is the longer, 2 when
// b is. A leading article is no word of the relation ("The Zzyzx Files" is
// "Zzyzx" with "Files" added). A title numbered apart from the other, or only
// a part or a number longer, is no such pair: that is judgeTitles' to say.
func wordsAdded(a, b titleForm) (added string, longer int, ok bool) {
	x, y := leadArticleOff(foldedTokens(a.raw)), leadArticleOff(foldedTokens(b.raw))
	longer, long := 1, a.raw
	if len(x) < len(y) {
		x, y, longer, long = y, x, 2, b.raw
	}
	if len(y) == 0 || len(x) == len(y) {
		return "", 0, false
	}
	words := func(ts []foldedToken) []string {
		out := make([]string, 0, len(ts))
		for _, t := range ts {
			out = append(out, t.word)
		}

		return out
	}
	var rest []foldedToken
	// an edition's word set inside the title ("Zzyzx 4K Nature")
	inside := slices.IndexFunc(x, func(t foldedToken) bool { return editionNumerals[t.word] })
	switch {
	case inside > 0 && len(x) == len(y)+1 && slices.Equal(words(slices.Delete(slices.Clone(x), inside, inside+1)), words(y)):
		rest = x[inside : inside+1]
	case slices.Equal(words(x[:len(y)]), words(y)):
		rest = x[len(y):]
	case slices.Equal(words(x[len(x)-len(y):]), words(y)):
		rest = x[:len(x)-len(y)]
	default:
		return "", 0, false
	}
	// words that are only a part's number or a number are a part or a
	// sequel, numbered apart, not an edition or a subtitle
	if !slices.ContainsFunc(rest, func(t foldedToken) bool { _, n := numberOf(t.word, true); return !n && partWordOf(t.word) == "" }) {
		return "", 0, false
	}
	tokens := strings.Fields(long)

	return strings.Trim(strings.Join(tokens[rest[0].token:rest[len(rest)-1].token+1], " "), " -:,;"), longer, true
}

// foldedToken is one folded word of a title and which of the title's own
// words, split at its spaces, it came from.
type foldedToken struct {
	word  string
	token int
}

// foldedTokens are a title's folded words, each with the word of the title
// as written that holds it: "Mononoke-hime" holds two.
func foldedTokens(raw string) []foldedToken {
	var out []foldedToken
	for i, tok := range strings.Fields(raw) {
		for w := range strings.FieldsSeq(normaliseTitle(tok)) {
			out = append(out, foldedToken{word: w, token: i})
		}
	}

	return out
}

// leadArticleOff is a title's folded words but a leading article.
func leadArticleOff(ts []foldedToken) []foldedToken {
	if len(ts) > 1 && (ts[0].word == "the" || ts[0].word == "a" || ts[0].word == "an") {
		return ts[1:]
	}

	return ts
}

// unnumbered is a title's words but its numbers and the part words before
// them.
func unnumbered(words []string) []string {
	out := make([]string, 0, len(words))
	for i, w := range words {
		if countsAsNumber(w) || keptPartWords[w] && i+1 < len(words) && countsAsNumber(words[i+1]) {
			continue
		}
		out = append(out, w)
	}

	return out
}

// onlyNumbers says whether every word of a title is a number, and no part
// word numbers it ("Part One" is a part, not the number one).
func onlyNumbers(f titleForm) bool {
	return f.numbered && !f.partWord && !slices.ContainsFunc(f.words, func(w string) bool { return !countsAsNumber(w) })
}

// sameName says whether two names are one title written the same way, or
// another way that cannot be another title: equal once folded, an article
// apart, or titleVariant. No likeness score, which is what matched a
// spin-off's short title ("Zzyzx Street: SU") to its parent's name: another
// title a series goes by has to be the name, not a name like it.
func sameName(a, b string) bool {
	x, y := normaliseTitle(a), normaliseTitle(b)
	if x == "" || y == "" {
		return false
	}
	if x == y || withoutThe(x) == withoutThe(y) {
		return true
	}
	_, ok := titleVariant(a, b)

	return ok
}

// numbering is the numbers a title carries, in order, as titleWords reads
// them: digits, a number in words, a part's number however it is written.
func numbering(s string) []string {
	return formOf(s).numbers
}

// sameNumbering says whether two titles carry the same numbers (see
// numbersAgree).
func sameNumbering(a, b string) bool {
	return numbersAgree(formOf(a), formOf(b))
}

// numbersAgree says whether two titles' numbers are the same numbers: a
// part, a sequel's number or a numeral on one and not the other, or another
// on each, is two titles - except a part 1 marked as one on one side alone,
// which is the first part of what the other names without one ("Dune" and
// "Dune: Part One", "It" and "It Chapter One", "Pilot" and "Pilot (1)").
func numbersAgree(a, b titleForm) bool {
	x, y := a.numbers, b.numbers
	if slices.Equal(x, y) {
		return true
	}

	return a.partOne && slices.Equal(x[:len(x)-1], y) || b.partOne && slices.Equal(x, y[:len(y)-1])
}

// partOneApart says whether one title is the other with a part 1 marked as
// one after it: "Dune" and "Dune: Part One", "It" and "It Chapter One".
func partOneApart(a, b titleForm) bool {
	return a.partOne && slices.Equal(withoutPartOne(a.words), b.words) || b.partOne && slices.Equal(withoutPartOne(b.words), a.words)
}

// withoutPartOne is a title's words but its closing part 1 and the part
// word before it.
func withoutPartOne(words []string) []string {
	n := len(words) - 1
	if n > 0 && keptPartWords[words[n-1]] {
		n--
	}

	return words[:max(n, 0)]
}

// closingLetters are the letters that close a title as a sequel's numeral
// as often as a name: "Henry V", "Malcolm X", "Zzyzx I".
var closingLetters = map[string]bool{"i": true, "v": true, "x": true}

// letterApart says whether two titles part on a closing letter that could
// be a numeral or a name: one has it and the other lacks it ("Henry" and
// "Henry V"), or the other closes on the number it would be ("Zzyzx X" and
// "Zzyzx 10").
func letterApart(x, y []string) bool {
	one := func(long, short []string) bool {
		if len(long) == 0 || len(short) == 0 || !closingLetters[long[len(long)-1]] {
			return false
		}
		last := long[len(long)-1]
		switch {
		case len(long) == len(short)+1:
			return slices.Equal(long[:len(short)], short)
		case len(long) == len(short):
			return slices.Equal(long[:len(long)-1], short[:len(short)-1]) && short[len(short)-1] == strconv.Itoa(romanValue(last))
		}

		return false
	}

	return one(x, y) || one(y, x)
}

// digitRun is a run of digits in a word: "7" of "se7en", "13" of "13th".
var digitRun = regexp.MustCompile(`\d+`)

// numberWords are the numbers a title writes as a word as often as in
// digits: "The Nine Lives of Fritz the Cat" is filed as "9 Lives of Fritz
// the Cat", "Dune: Part Two" as "Dune Part 2".
var numberWords = map[string]string{
	"zero": "0", "one": "1", "two": "2", "three": "3", "four": "4", "five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10",
	"eleven": "11", "twelve": "12", "thirteen": "13", "fourteen": "14", "fifteen": "15", "sixteen": "16", "seventeen": "17", "eighteen": "18", "nineteen": "19",
	"twenty": "20", "thirty": "30", "forty": "40", "fifty": "50", "sixty": "60", "seventy": "70", "eighty": "80", "ninety": "90", "hundred": "100",
}

// digitColon is a colon between two digits.
var digitColon = regexp.MustCompile(`(\d):(\d)`)

// romanNumeral is a word that reads as a Roman numeral: VIII and XIII are
// two films' numbers, not one letter spelled two ways.
var romanNumeral = regexp.MustCompile(`^m{0,3}(cm|cd|d?c{0,3})(xc|xl|l?x{0,3})(ix|iv|v?i{0,3})$`)

// bracketedPart is a part numbered in brackets, with or without a part
// word: "(1)", "(Part One)", "[Pt. II]". Two digits at most, so a bracketed
// year is none.
var bracketedPart = regexp.MustCompile(`(?i)[(\[]\s*(?:(part|pt|chapter|ch|volume|vol|book|episode|ep)\.?\s*)?(\d{1,2}|[ivx]+|one|two|three|four|five|six|seven|eight|nine|ten)\s*[)\]]`)

// titleWords are a title's words as two spellings of it have them in common:
// folded (normaliseTitle), with an article dropped where a word follows it
// (the A of "Plan A" is the title's own), a number written in words written
// in digits, and a part's number read as the number however it is written.
// The word Part goes before its number - "Part 2", "Pt. II", "(Part Two)",
// "(2)" and a closing "II" are all "2" - and the other part words stay,
// spelled one way each: Vol and Volume, Ep and Episode, Ch and Chapter, and
// Book, are each their own ("Book One" is not "Volume One"). marked says, by
// word, which numbers a part word or a bracket marks as a part's.
func titleWords(s string) (words []string, marked []bool) {
	// a renamer drops the colon of a time, "12:00" filed as "1200": read
	// with the colons between digits dropped on both sides. (Not a point:
	// a name's points are its spaces, and "1.5" is read as "1 5" both ways)
	s = digitColon.ReplaceAllString(s, "$1$2")
	// a bracketed part is its part word and its number
	s = bracketedPart.ReplaceAllStringFunc(s, func(m string) string {
		sub := bracketedPart.FindStringSubmatch(m)

		return " " + cmp.Or(sub[1], "part") + " " + sub[2] + " "
	})
	all := strings.Fields(normaliseTitle(s))
	for i, w := range all {
		afterPart := i > 0 && partWordOf(all[i-1]) != "" && isPartNumber(w)
		switch {
		case (w == "a" || w == "an" || w == "the") && i < len(all)-1:
			continue
		case partWordOf(w) != "" && i < len(all)-1 && isPartNumber(all[i+1]):
			// the word Part goes where its number follows; the others stay,
			// spelled one way
			if kept := partWordOf(w); kept != "part" {
				words, marked = append(words, kept), append(marked, false)
			}

			continue
		}
		// a numeral after a part word - a lone I too - or closing the title
		// after a title word, as a sequel's number is ("Saga II"): not a
		// lone I, V or X closing one, which is a letter as often, nor a
		// numeral past what a sequel runs to ("Size XL" is no 40)
		if n, ok := numberOf(w, afterPart || i > 0 && i == len(all)-1 && len(w) > 1); ok {
			w = n
		}
		words, marked = append(words, w), append(marked, afterPart)
	}

	return words, marked
}

// partWordOf is the part word a word spells, one way for each - "part" for
// Part and Pt, "volume" for Volume and Vol, "episode" for Episode and Ep,
// "chapter" for Chapter and Ch, "book" - or "" for none.
func partWordOf(w string) string {
	switch w {
	case "part", "pt":
		return "part"
	case "volume", "vol":
		return "volume"
	case "episode", "ep":
		return "episode"
	case "chapter", "ch":
		return "chapter"
	case "book":
		return "book"
	}

	return ""
}

// keptPartWords are the part words that stay among a title's words.
var keptPartWords = map[string]bool{"volume": true, "episode": true, "chapter": true, "book": true}

// isPartNumber says whether a word reads as a part's number after a part
// word.
func isPartNumber(w string) bool {
	_, ok := numberOf(w, true)

	return ok
}

// romanPart is a Roman numeral of the kind a part or a sequel is numbered by.
var romanPart = regexp.MustCompile(`^[ivx]+$`)

// romanMax is the highest numeral read as a part's or a sequel's number.
const romanMax = 20

// numberOf is a word read as a number, in digits: digits as they are, a
// number in words, and - when roman is set - a Roman numeral a part or a
// sequel is numbered by, I to XX.
func numberOf(w string, roman bool) (string, bool) {
	if d, ok := numberWords[w]; ok {
		return d, true
	}
	if isNumber(w) {
		return w, true
	}
	if roman && romanPart.MatchString(w) && romanNumeral.MatchString(w) {
		if v := romanValue(w); v <= romanMax {
			return strconv.Itoa(v), true
		}
	}

	return "", false
}

// romanValue is what a well-formed Roman numeral counts.
func romanValue(s string) int {
	value := map[rune]int{'i': 1, 'v': 5, 'x': 10, 'l': 50, 'c': 100, 'd': 500, 'm': 1000}
	total, prev := 0, 0
	for _, r := range slices.Backward([]rune(s)) {
		v := value[r]
		if v < prev {
			total -= v
		} else {
			total += v
			prev = v
		}
	}

	return total
}

func hasDigit(s string) bool {
	return strings.ContainsFunc(s, unicode.IsDigit)
}

// countsAsNumber says whether a word numbers a title: it holds a digit, and
// is not an edition's word that happens to ("3D", "4K").
func countsAsNumber(w string) bool {
	return hasDigit(w) && !editionNumerals[w]
}

// editionNumerals are the words an edition or a format is named by that hold
// a digit, which no part or sequel is numbered by.
var editionNumerals = map[string]bool{"3d": true, "2d": true, "4k": true, "8k": true}

// isNumber says whether a word is digits and nothing else.
func isNumber(w string) bool {
	return w != "" && !strings.ContainsFunc(w, func(r rune) bool { return !unicode.IsDigit(r) })
}

// titleVariant says whether two titles are the same title written another
// way, and how (see variantOf).
func titleVariant(a, b string) (how string, ok bool) {
	return variantOf(formOf(a), formOf(b))
}

// variantOf says whether two titles are the same title written another way,
// and how: letters spaced otherwise ("J.D's Revenge", read as "J Ds",
// against "J.D.'s Revenge"), an article swapped, added or dropped before a
// word ("Lassie - A New Beginning" against "Lassie: The New Beginning"), a
// number in digits against one in words ("9 Lives" against "The Nine
// Lives"), a part 1 marked as one on one side alone, or, in a title of three
// words or more, one vowel inside a word spelled another way ("Grey" against
// "Gray"). Each scored below the bar and was reported as the wrong match or
// another film.
//
// Kept narrow on purpose, because each looser reading made two films one: a
// word holding a number or a Roman numeral is never a spelling ("Death Race
// 2000" and "2050", "VIII" and "XIII"); a first letter changed is another
// word ("Bride" and "Pride"); a letter added or dropped is another title
// ("Alien" and "Aliens"); words run together are only compared so when no
// number is among them ("One Two" and "Twelve"); and a title that is nothing
// but its number is its number ("Ten" and "10" are two films).
func variantOf(fa, fb titleForm) (how string, ok bool) {
	wa, wb := fa.words, fb.words
	if len(wa) == 0 || len(wb) == 0 {
		return "", false
	}
	numbered := fa.numbered || fb.numbered
	if slices.Equal(wa, wb) {
		if onlyNumbers(fa) && onlyNumbers(fb) && fa.folded != fb.folded {
			return "", false
		}
		if fa.folded == fb.folded && (fa.qualifier == "") != (fb.qualifier == "") {
			return fmt.Sprintf("the same title with (%s) on one side alone", fa.qualifier+fb.qualifier), true
		}

		return "the same title with its articles or its numbers written otherwise", true
	}
	if partOneApart(fa, fb) {
		return "the same title with its part 1 named on one side alone", true
	}
	if !numbered && strings.Join(wa, "") == strings.Join(wb, "") {
		return "the same title with its words spaced otherwise", true
	}
	if len(wa) != len(wb) || len(wa) < 3 {
		return "", false
	}
	differ := -1
	for i := range wa {
		if wa[i] == wb[i] {
			continue
		}
		if differ >= 0 {
			return "", false
		}
		differ = i
	}
	x, y := []rune(wa[differ]), []rune(wb[differ])
	if hasDigit(wa[differ]) || hasDigit(wb[differ]) || romanNumeral.MatchString(wa[differ]) || romanNumeral.MatchString(wb[differ]) {
		return "", false
	}
	if len(x) != len(y) || len(x) < 4 {
		return "", false
	}
	changed := -1
	for i := range x {
		if x[i] == y[i] {
			continue
		}
		if changed >= 0 {
			return "", false
		}
		changed = i
	}
	if changed <= 0 || !vowel(x[changed]) || !vowel(y[changed]) {
		return "", false
	}

	return fmt.Sprintf("the same title with %q spelled %q", wb[differ], wa[differ]), true
}

// vowel says whether a letter is a Latin vowel, y included.
func vowel(r rune) bool {
	return strings.ContainsRune("aeiouy", r)
}

// lookalike is a letter of another script drawn like a Latin one.
type lookalike struct {
	latin  rune
	script string
}

// lookalikes are the Cyrillic and Greek letters drawn like a Latin letter,
// and the one they stand in for. The table is kept to the letters that are
// indistinguishable in print: a Greek delta is a letter no one mistakes for
// an A, and used as a symbol it is no stand-in for anything.
var lookalikes = map[rune]lookalike{
	'\u0430': {'a', "Cyrillic"}, '\u0435': {'e', "Cyrillic"}, '\u043E': {'o', "Cyrillic"}, '\u0440': {'p', "Cyrillic"},
	'\u0441': {'c', "Cyrillic"}, '\u0443': {'y', "Cyrillic"}, '\u0445': {'x', "Cyrillic"}, '\u0456': {'i', "Cyrillic"},
	'\u0458': {'j', "Cyrillic"}, '\u0455': {'s', "Cyrillic"},
	'\u0410': {'A', "Cyrillic"}, '\u0412': {'B', "Cyrillic"}, '\u0415': {'E', "Cyrillic"}, '\u041A': {'K', "Cyrillic"},
	'\u041C': {'M', "Cyrillic"}, '\u041D': {'H', "Cyrillic"}, '\u041E': {'O', "Cyrillic"}, '\u0420': {'P', "Cyrillic"},
	'\u0421': {'C', "Cyrillic"}, '\u0422': {'T', "Cyrillic"}, '\u0425': {'X', "Cyrillic"},
	'\u0391': {'A', "Greek"}, '\u0392': {'B', "Greek"}, '\u0395': {'E', "Greek"}, '\u0396': {'Z', "Greek"},
	'\u0397': {'H', "Greek"}, '\u0399': {'I', "Greek"}, '\u039A': {'K', "Greek"}, '\u039C': {'M', "Greek"},
	'\u039D': {'N', "Greek"}, '\u039F': {'O', "Greek"}, '\u03A1': {'P', "Greek"}, '\u03A4': {'T', "Greek"},
	'\u03A5': {'Y', "Greek"}, '\u03A7': {'X', "Greek"}, '\u03BF': {'o', "Greek"}, '\u03BD': {'v', "Greek"},
}

// scriptOf names a letter's script where it is one lookalikes draws from.
func scriptOf(r rune) string {
	switch {
	case unicode.Is(unicode.Cyrillic, r):
		return "Cyrillic"
	case unicode.Is(unicode.Greek, r):
		return "Greek"
	}

	return ""
}

// lookalikeLetters finds the letters in a Latin title that belong to another
// script and only look Latin: a Cyrillic A (U+0410) spelling Arrival, which
// no search for Arrival finds. plain is the title with each put right. A title written in
// the other script is left alone - one with any letter of it that looks like
// no Latin letter uses the script for itself, and a Greek delta in a Latin
// title is a symbol, not a stand-in - and so is a title with no Latin letter
// at all.
func lookalikeLetters(title string) (found []string, plain string) {
	latin, genuine := false, map[string]bool{}
	for _, r := range title {
		if _, ok := lookalikes[r]; ok {
			continue
		}
		switch {
		case unicode.Is(unicode.Latin, r):
			latin = true
		case scriptOf(r) != "" && unicode.IsLetter(r):
			genuine[scriptOf(r)] = true
		}
	}
	if !latin {
		return nil, title
	}

	var b strings.Builder
	for _, r := range title {
		l, ok := lookalikes[r]
		if !ok || genuine[l.script] {
			b.WriteRune(r)

			continue
		}
		found = append(found, fmt.Sprintf("the %s %c (U+%04X) in place of the Latin %c", l.script, r, r, l.latin))
		b.WriteRune(l.latin)
	}

	return found, b.String()
}

// providerTitles asks TMDB what else a film or a series is called, and what
// its search finds by a title and a year, each answer kept for the life of
// the process as tmdb.Facts keeps its own.
type providerTitles struct {
	api *tmdb.Client

	mu          sync.Mutex
	alternative map[string][]string   // "movie:ID" or "tv:ID" -> the titles TMDB lists for it
	translated  map[string][]string   // "movie:ID" or "tv:ID" -> its titles in TMDB's translations
	collected   map[string][]titleHit // movie ID -> the films of the TMDB collection it is in
	collections map[int][]titleHit    // collection ID -> its films
	searched    map[string][]titleHit // "movie:title:year" -> what the search found
	unanswered  map[string]bool       // "movie:title:year" -> the search failed, and has not answered since
}

// titleHit is one film or series TMDB's search found.
type titleHit struct {
	ID       int
	Title    string
	Original string
	Year     int
}

// newProviderTitles asks TMDB with the configured token, or is nil without
// one, its reads behind a breaker of their own (tmdb.Guarded).
func newProviderTitles(opts Options) *providerTitles {
	return newProviderTitlesVia(opts, tmdb.Guarded(opts.ProviderTransport))
}

// newProviderTitlesVia is newProviderTitles reading through rt: a breaker
// shared with the other TMDB reads of one sweep, so TMDB down is seen once.
func newProviderTitlesVia(opts Options, rt http.RoundTripper) *providerTitles {
	if opts.TMDBKey == "" {
		return nil
	}
	api, err := tmdb.New(tmdb.DefaultBaseURL, opts.TMDBKey)
	if err != nil {
		return nil
	}
	// a read TMDB fails in a way that passes (a reset, a timeout, a 5xx) is
	// tried again: one such failure used to be the end of TMDB for a sweep
	api.Client.HTTPClient = tmdb.HTTPClient(rt)

	return &providerTitles{api: api, alternative: map[string][]string{}, translated: map[string][]string{}, collected: map[string][]titleHit{}, collections: map[int][]titleHit{}, searched: map[string][]titleHit{}, unanswered: map[string]bool{}}
}

// tmdbError names the setting to check when TMDB refuses the credential.
func tmdbError(what string, err error) error {
	if client.StatusCode(err) == http.StatusUnauthorized {
		return fmt.Errorf("tmdb %s: HTTP 401 (check EMBYFIN_TMDB_TOKEN)", what)
	}

	return fmt.Errorf("tmdb %s: %w", what, err)
}

// tmdbKind is the list a library item's TMDB id is read in: movie for a
// film, tv for a series, "" for anything else.
func tmdbKind(itemType string) string {
	switch itemType {
	case typeMovie:
		return "movie"
	case "Series":
		return "tv"
	}

	return ""
}

// alternatives are the other titles TMDB lists for a film or a series: the
// ones it goes by in other countries and languages. An id TMDB does not know
// has none.
func (p *providerTitles) alternatives(ctx context.Context, kind, id string) ([]string, error) {
	memo := kind + ":" + id
	p.mu.Lock()
	titles, ok := p.alternative[memo]
	p.mu.Unlock()
	if ok {
		return titles, nil
	}

	// an id that is not a number, or is 0, is none TMDB has
	titles = []string{}
	if id == "" || strings.Trim(id, "0123456789") != "" {
		return titles, nil
	}
	n, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("tmdb %s %s: %w", kind, id, err)
	}
	if n <= 0 {
		return titles, nil
	}
	switch kind {
	case "movie":
		res, err := p.api.MovieAlternativeTitles(ctx, n, tmdb.MovieAlternativeTitlesOperationOptions{})
		switch {
		case client.IsNotFound(err):
		case err != nil:
			return nil, tmdbError("alternative titles of movie "+id, err)
		case res.Model != nil:
			for _, t := range res.Model.Titles {
				titles = append(titles, t.Title)
			}
		}
	case "tv":
		res, err := p.api.TvSeriesAlternativeTitles(ctx, n)
		switch {
		case client.IsNotFound(err):
		case err != nil:
			return nil, tmdbError("alternative titles of tv "+id, err)
		case res.Model != nil:
			for _, t := range res.Model.Results {
				titles = append(titles, t.Title)
			}
		}
	}

	p.mu.Lock()
	p.alternative[memo] = titles
	p.mu.Unlock()

	return titles, nil
}

// translations are a film's or a series' titles in the translations TMDB
// holds for it - its title in each language it is translated into, which
// alternatives often leave out ("La llegada" for Arrival). A translation
// giving no title of its own (the original's is used) adds none, and an id
// TMDB does not know has none.
func (p *providerTitles) translations(ctx context.Context, kind, id string) ([]string, error) {
	memo := kind + ":" + id
	p.mu.Lock()
	titles, ok := p.translated[memo]
	p.mu.Unlock()
	if ok {
		return titles, nil
	}

	// an id that is not a number, or is 0, is none TMDB has
	titles = []string{}
	if id == "" || strings.Trim(id, "0123456789") != "" {
		return titles, nil
	}
	n, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("tmdb translations of %s %s: %w", kind, id, err)
	}
	if n <= 0 {
		return titles, nil
	}
	switch kind {
	case "movie":
		res, err := p.api.MovieTranslations(ctx, n)
		switch {
		case client.IsNotFound(err):
		case err != nil:
			return nil, tmdbError("translations of movie "+id, err)
		case res.Model != nil:
			for _, t := range res.Model.Translations {
				if t.Data != nil && t.Data.Title != "" {
					titles = append(titles, t.Data.Title)
				}
			}
		}
	case "tv":
		res, err := p.api.TvSeriesTranslations(ctx, n)
		switch {
		case client.IsNotFound(err):
		case err != nil:
			return nil, tmdbError("translations of tv "+id, err)
		case res.Model != nil:
			for _, t := range res.Model.Translations {
				if t.Data != nil && t.Data.Name != "" {
					titles = append(titles, t.Data.Name)
				}
			}
		}
	}

	p.mu.Lock()
	p.translated[memo] = titles
	p.mu.Unlock()

	return titles, nil
}

// collectionParts are the films of the TMDB collection a film is in - Alien's
// holds Aliens and Alien³ - the film itself among them; none when TMDB puts it
// in no collection, or does not know the id.
func (p *providerTitles) collectionParts(ctx context.Context, id string) ([]titleHit, error) {
	p.mu.Lock()
	parts, ok := p.collected[id]
	p.mu.Unlock()
	if ok {
		return parts, nil
	}

	// an id that is not a number, or is 0, is none TMDB has
	parts = []titleHit{}
	if id == "" || strings.Trim(id, "0123456789") != "" {
		return parts, nil
	}
	n, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("tmdb movie %s: %w", id, err)
	}
	if n <= 0 {
		return parts, nil
	}
	res, err := p.api.MovieDetails(ctx, n, tmdb.MovieDetailsOperationOptions{})
	switch {
	case client.IsNotFound(err):
	case err != nil:
		return nil, tmdbError("movie "+id, err)
	case res.Model != nil && res.Model.BelongsToCollection != nil && res.Model.BelongsToCollection.Id > 0:
		if parts, err = p.collection(ctx, res.Model.BelongsToCollection.Id); err != nil {
			return nil, err
		}
	}

	p.mu.Lock()
	p.collected[id] = parts
	p.mu.Unlock()

	return parts, nil
}

// collection is the films of a TMDB collection, each with its title and year,
// asked once for every film of it.
func (p *providerTitles) collection(ctx context.Context, id int) ([]titleHit, error) {
	p.mu.Lock()
	parts, ok := p.collections[id]
	p.mu.Unlock()
	if ok {
		return parts, nil
	}
	parts, err := p.collectionOf(ctx, id)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.collections[id] = parts
	p.mu.Unlock()

	return parts, nil
}

// collectionOf asks TMDB for a collection's films.
func (p *providerTitles) collectionOf(ctx context.Context, id int) ([]titleHit, error) {
	parts := []titleHit{}
	res, err := p.api.CollectionDetails(ctx, id, tmdb.CollectionDetailsOperationOptions{})
	switch {
	case client.IsNotFound(err):
		return parts, nil
	case err != nil:
		return nil, tmdbError(fmt.Sprintf("collection %d", id), err)
	case res.Model == nil:
		return parts, nil
	}
	for _, part := range res.Model.Parts {
		year := 0
		if len(part.ReleaseDate) >= 4 {
			if y, err := strconv.Atoi(part.ReleaseDate[:4]); err == nil {
				year = y
			}
		}
		parts = append(parts, titleHit{ID: part.Id, Title: part.Title, Original: part.OriginalTitle, Year: year})
	}

	return parts, nil
}

// asked says whether search has already answered for a title and year,
// so asking again costs TMDB nothing.
func (p *providerTitles) asked(kind, title string, year int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.searched[searchMemo(kind, title, year)]

	return ok
}

// failedBefore says whether search failed for a title and year, TMDB not
// answering (its caller's own giving up aside), and has not answered since.
func (p *providerTitles) failedBefore(kind, title string, year int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.unanswered[searchMemo(kind, title, year)]
}

// unansweredSearch notes a search TMDB failed, unless its caller gave up.
func (p *providerTitles) unansweredSearch(ctx context.Context, memo string) {
	if ctx.Err() != nil {
		return
	}
	p.mu.Lock()
	p.unanswered[memo] = true
	p.mu.Unlock()
}

// searchMemo is search's key for an answer.
func searchMemo(kind, title string, year int) string {
	return fmt.Sprintf("%s:%s:%d", kind, strings.ToLower(title), year)
}

// search is what TMDB's search finds by a title, in the year given when
// there is one: films for kind movie, series for tv.
func (p *providerTitles) search(ctx context.Context, kind, title string, year int) ([]titleHit, error) {
	memo := searchMemo(kind, title, year)
	p.mu.Lock()
	hits, ok := p.searched[memo]
	p.mu.Unlock()
	if ok {
		return hits, nil
	}

	hits = []titleHit{}
	// a date with no year in it dates nothing
	yearOf := func(date string) int {
		if y, err := strconv.Atoi(date[:min(len(date), 4)]); err == nil {
			return y
		}

		return 0
	}
	switch kind {
	case "movie":
		opts := tmdb.SearchMovieOperationOptions{Query: title}
		if year > 0 {
			opts.Year = strconv.Itoa(year)
		}
		res, err := p.api.SearchMovie(ctx, opts)
		if err != nil {
			p.unansweredSearch(ctx, memo)

			return nil, tmdbError("search for the film "+title, err)
		}
		if res.Model != nil {
			for _, r := range res.Model.Results {
				hits = append(hits, titleHit{ID: r.Id, Title: r.Title, Original: r.OriginalTitle, Year: yearOf(r.ReleaseDate)})
			}
		}
	case "tv":
		opts := tmdb.SearchTvOperationOptions{Query: title}
		if year > 0 {
			opts.FirstAirDateYear = &year
		}
		res, err := p.api.SearchTv(ctx, opts)
		if err != nil {
			p.unansweredSearch(ctx, memo)

			return nil, tmdbError("search for the series "+title, err)
		}
		if res.Model != nil {
			for _, r := range res.Model.Results {
				hits = append(hits, titleHit{ID: r.Id, Title: r.Name, Original: r.OriginalName, Year: yearOf(r.FirstAirDate)})
			}
		}
	}

	p.mu.Lock()
	p.searched[memo] = hits
	delete(p.unanswered, memo)
	p.mu.Unlock()

	return hits, nil
}

// numberedAs says whether a search hit's title, or its original title,
// carries the numbers a title does (see sameNumbering).
func numberedAs(title string, h titleHit) bool {
	return sameNumbering(title, h.Title) || h.Original != "" && sameNumbering(title, h.Original)
}

// bestHit is the search hit whose title (or original title) is closest to
// the one asked after, numbered as it is and dated within a year of it when
// a year was asked, or false when none comes near enough to be the one.
func bestHit(hits []titleHit, title string, year int) (titleHit, bool) {
	best, bestScore := titleHit{}, -1.0
	for _, h := range hits {
		if year > 0 && h.Year > 0 && abs(h.Year-year) > 1 || !numberedAs(title, h) {
			continue
		}
		score, _ := titleScore(title, h.Title)
		if s, _ := titleScore(title, h.Original); s > score {
			score = s
		}
		if score > bestScore {
			best, bestScore = h, score
		}
	}

	return best, bestScore >= seriesConfident
}
