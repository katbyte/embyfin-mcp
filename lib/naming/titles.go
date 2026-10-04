package naming

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// SeriesNameYear is the year a library writes into a series' own name to tell
// two of them apart: "Doctor Who (1963)".
var SeriesNameYear = regexp.MustCompile(`\s*[(\[]((?:19|20)\d{2})[)\]]\s*$`)

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
	AsName     = "name"
	AsOriginal = "original title"
	AsSort     = "sort name"
)

// KnownTitle is one title an item goes by, and which.
type KnownTitle struct {
	Title, As string
}

// KnownTitles are the titles an item goes by: its name, spelled plainly when
// it carries a letter that only looks Latin; its original title, which a
// folder in the film's own language names; and its sort name. Each comes
// without the year a server leaves on a film it could not match ("Cube
// (1997)"), because a path's title is read cut at its year.
func KnownTitles(name, original, sort string) []KnownTitle {
	_, plain := LookalikeLetters(name)
	var out []KnownTitle
	for _, k := range []KnownTitle{{plain, AsName}, {original, AsOriginal}, {sort, AsSort}} {
		title := strings.TrimSpace(SeriesNameYear.ReplaceAllString(k.Title, ""))
		if title == "" {
			title = strings.TrimSpace(k.Title)
		}
		if title != "" {
			out = append(out, KnownTitle{title, k.As})
		}
	}

	return out
}

// Form is a title read once for comparing: folded, its words as
// Words spells them, the numbers it carries, and whether it closes on a
// part 1 marked as one.
type Form struct {
	Folded   string   // Normalise
	Words    []string // Words
	Canon    string   // the words, one string
	Numbers  []string // each number word's digits, in order
	Numbered bool     // any word a number
	// PartWord says a part word ("Part", "Chapter", "Volume") or a bracketed
	// number numbers the title: never "only a number" then
	PartWord bool
	// PartOne says the title closes on a part 1 marked as one - after a part
	// word, or bracketed - which a title without it names as well ("Dune:
	// Part One", "Pilot (1)"); a closing "One" or "1" of the name itself
	// ("Air Force One", "Apollo 1") is not one
	PartOne bool
	// Qualifier is the country or the year a title closes on in brackets,
	// "US" of "The Zzyzx (US)": a qualifier on one side only is the same
	// title, and another on each may be another country's version of it
	Qualifier string
	// Raw is the title as written, its qualifier off: what a finding quotes
	Raw string
	// Maybe is the title without a closing country's code it kept as a word
	// because the title is that word too ("It (IT)"): either reading may be
	// the name
	Maybe *Form
}

// Confident is the score two titles have to reach to be one title to act
// on, as show_resolve calls a match rather than a guess.
const Confident = 0.9

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
// and is kept: maybe is then the title without it, for Judge to say
// it can't tell.
func qualifierOf(s string) (qualifier, rest, maybe string) {
	m := titleQualifier.FindStringSubmatchIndex(s)
	if len(m) < 4 {
		return "", s, ""
	}
	q := s[m[2]:m[3]]
	switch {
	case IsNumber(q):
		return q, s[:m[0]], ""
	case countryCodes[q] && Normalise(s[:m[0]]) == strings.ToLower(q):
		return "", s, s[:m[0]]
	case countryCodes[q]:
		return q, s[:m[0]], ""
	}

	return "", s, ""
}

// FormOf reads a title into a Form.
func FormOf(s string) Form {
	// a closing country or year tells two of a name apart - "The Zzyzx
	// (US)", "The Zzyzx (2005)" - and is no word of the title
	qualifier, s, maybe := qualifierOf(s)
	f := wordsOf(s)
	f.Qualifier = qualifier
	if maybe != "" {
		without := wordsOf(maybe)
		f.Maybe = &without
	}

	return f
}

// wordsOf reads a title, its qualifier already off, into a Form.
func wordsOf(s string) Form {
	words, marked := Words(s)
	f := Form{Folded: Normalise(s), Words: words, Canon: strings.Join(words, " "), Raw: s}
	for i, w := range words {
		if countsAsNumber(w) {
			f.Numbered = true
			f.Numbers = append(f.Numbers, strings.Join(digitRun.FindAllString(w, -1), " "))
		}
		if marked[i] {
			f.PartWord = true
		}
	}
	if n := len(words); n > 0 && words[n-1] == "1" && marked[n-1] {
		f.PartOne = true
	}

	return f
}

// Verdict is what two titles are to each other.
type Verdict int

const (
	// Different are two titles
	Different Verdict = iota
	// Same are one title, however written
	Same
	// NumberedApart are one title but for a number on one side and
	// not the other, or another on each: another part, another sequel, or a
	// number that is part of the name ("Air Force One")
	NumberedApart
	// CantTell part on a closing letter one has and the other lacks
	// ("Henry" and "Henry V", "X" and "10"): a numeral or a name
	CantTell
)

func (v Verdict) String() string {
	return [...]string{"different", "the same", "numbered apart", "can't tell"}[v]
}

// SameTitle says whether two titles are one title (see Judge).
func SameTitle(a, b string) bool {
	return Alike(FormOf(a), FormOf(b))
}

// Alike says whether two titles are one title (see Judge).
func Alike(a, b Form) bool {
	return Judge(a, b) == Same
}

// Judge says what two titles are to each other. The same title when
// alike enough to act on (Score, over the words as Words spells
// them, so "Part 2", "Pt. 2", "(2)" and a closing "II" read alike) or
// written another way (VariantOf), their numbers agreeing (NumbersAgree).
// Numbered apart when only their numbers part them: "Part 2" beside the
// title alone scored as the title with a word added, and a film's first part
// passed for its second. Can't tell when a closing letter parts them ("Henry"
// and "Henry V"). And a title that is nothing but its number is its number
// ("Ten" and "10" are two films).
func Judge(a, b Form) Verdict {
	v := judgeQualified(a, b)
	// a country's code kept as a word the title may have ("It (IT)"): when
	// the title without it is the other, either reading may be the name
	if v != Same && (a.Maybe != nil || b.Maybe != nil) {
		x, y := a, b
		if a.Maybe != nil {
			x = *a.Maybe
		}
		if b.Maybe != nil {
			y = *b.Maybe
		}
		if judgeQualified(x, y) == Same {
			return CantTell
		}
	}

	return v
}

// judgeQualified is Judge with the titles' qualifiers weighed: the
// same title's country or year set apart as Judge says.
func judgeQualified(a, b Form) Verdict {
	v := judgeWords(a, b)
	// the same title, qualified apart: another country each side may be the
	// other country's version of it, and another year each side another of
	// the name
	if v == Same && a.Qualifier != "" && b.Qualifier != "" && a.Qualifier != b.Qualifier {
		// a year one either side is the same title, as a path's year one
		// either side of the item's is the same film
		if IsNumber(a.Qualifier) && IsNumber(b.Qualifier) {
			if x, y := YearAt(a.Qualifier, 0), YearAt(b.Qualifier, 0); x-y > 1 || y-x > 1 {
				return NumberedApart
			}

			return Same
		}
		if !IsNumber(a.Qualifier) && !IsNumber(b.Qualifier) {
			return CantTell
		}
	}

	return v
}

// judgeWords is Judge over the titles' words, their qualifiers aside.
func judgeWords(a, b Form) Verdict {
	if letterApart(a.Words, b.Words) {
		return CantTell
	}
	if !NumbersAgree(a, b) {
		if len(a.Words) > 0 && slices.Equal(unnumbered(a.Words), unnumbered(b.Words)) {
			return NumberedApart
		}

		return Different
	}
	if onlyNumbers(a) && onlyNumbers(b) && a.Folded != b.Folded {
		return Different
	}
	// both ways round, so the verdict is the pair's and not the order's:
	// one title with a word added ("Plan" and "Plan A", a spin-off's short
	// title and its parent's name) scored alike read the one way only
	ab, _ := FoldedScore(a.Canon, b.Canon)
	ba, _ := FoldedScore(b.Canon, a.Canon)
	if min(ab, ba) >= Confident {
		return Same
	}
	if _, ok := VariantOf(a, b); ok {
		return Same
	}

	return Different
}

// WordsAdded says whether one title is the other with words added before or
// after it - an edition ("Alien Director's Cut"), a subtitle ("... The Final
// Cut", "Mononoke-hime - Princess Mononoke"), or another film - and which
// words, as the title writes them; longer is 1 when a is the longer, 2 when
// b is. A leading article is no word of the relation ("The Zzyzx Files" is
// "Zzyzx" with "Files" added). A title numbered apart from the other, or only
// a part or a number longer, is no such pair: that is Judge' to say.
func WordsAdded(a, b Form) (added string, longer int, ok bool) {
	x, y := leadArticleOff(foldedTokens(a.Raw)), leadArticleOff(foldedTokens(b.Raw))
	longer, long := 1, a.Raw
	if len(x) < len(y) {
		x, y, longer, long = y, x, 2, b.Raw
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
	if !slices.ContainsFunc(rest, func(t foldedToken) bool { _, n := NumberOf(t.word, true); return !n && PartWordOf(t.word) == "" }) {
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
		for w := range strings.FieldsSeq(Normalise(tok)) {
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
func onlyNumbers(f Form) bool {
	return f.Numbered && !f.PartWord && !slices.ContainsFunc(f.Words, func(w string) bool { return !countsAsNumber(w) })
}

// SameName says whether two names are one title written the same way, or
// another way that cannot be another title: equal once folded, an article
// apart, or Variant. No likeness score, which is what matched a
// spin-off's short title ("Zzyzx Street: SU") to its parent's name: another
// title a series goes by has to be the name, not a name like it.
func SameName(a, b string) bool {
	x, y := Normalise(a), Normalise(b)
	if x == "" || y == "" {
		return false
	}
	if x == y || WithoutThe(x) == WithoutThe(y) {
		return true
	}
	_, ok := Variant(a, b)

	return ok
}

// Numbering is the numbers a title carries, in order, as Words reads
// them: digits, a number in words, a part's number however it is written.
func Numbering(s string) []string {
	return FormOf(s).Numbers
}

// SameNumbering says whether two titles carry the same numbers (see
// NumbersAgree).
func SameNumbering(a, b string) bool {
	return NumbersAgree(FormOf(a), FormOf(b))
}

// NumbersAgree says whether two titles' numbers are the same numbers: a
// part, a sequel's number or a numeral on one and not the other, or another
// on each, is two titles - except a part 1 marked as one on one side alone,
// which is the first part of what the other names without one ("Dune" and
// "Dune: Part One", "It" and "It Chapter One", "Pilot" and "Pilot (1)").
func NumbersAgree(a, b Form) bool {
	x, y := a.Numbers, b.Numbers
	if slices.Equal(x, y) {
		return true
	}

	return a.PartOne && slices.Equal(x[:len(x)-1], y) || b.PartOne && slices.Equal(x, y[:len(y)-1])
}

// PartOneApart says whether one title is the other with a part 1 marked as
// one after it: "Dune" and "Dune: Part One", "It" and "It Chapter One".
func PartOneApart(a, b Form) bool {
	return a.PartOne && slices.Equal(WithoutPartOne(a.Words), b.Words) || b.PartOne && slices.Equal(WithoutPartOne(b.Words), a.Words)
}

// WithoutPartOne is a title's words but its closing part 1 and the part
// word before it.
func WithoutPartOne(words []string) []string {
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

// NumberWord is a number written as a word, in digits: "two" is "2". ok is
// false for any other word.
func NumberWord(w string) (digits string, ok bool) {
	digits, ok = numberWords[w]

	return digits, ok
}

// IsRomanPart says whether a word is made of the letters a part or a sequel
// is numbered by (I, V and X), which may be a numeral or a name.
func IsRomanPart(w string) bool {
	return romanPart.MatchString(w)
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

// Words are a title's words as two spellings of it have them in common:
// folded (Normalise), with an article dropped where a word follows it
// (the A of "Plan A" is the title's own), a number written in words written
// in digits, and a part's number read as the number however it is written.
// The word Part goes before its number - "Part 2", "Pt. II", "(Part Two)",
// "(2)" and a closing "II" are all "2" - and the other part words stay,
// spelled one way each: Vol and Volume, Ep and Episode, Ch and Chapter, and
// Book, are each their own ("Book One" is not "Volume One"). marked says, by
// word, which numbers a part word or a bracket marks as a part's.
func Words(s string) (words []string, marked []bool) {
	// a renamer drops the colon of a time, "12:00" filed as "1200": read
	// with the colons between digits dropped on both sides. (Not a point:
	// a name's points are its spaces, and "1.5" is read as "1 5" both ways)
	s = digitColon.ReplaceAllString(s, "$1$2")
	// a bracketed part is its part word and its number
	s = bracketedPart.ReplaceAllStringFunc(s, func(m string) string {
		sub := bracketedPart.FindStringSubmatch(m)

		return " " + cmp.Or(sub[1], "part") + " " + sub[2] + " "
	})
	all := strings.Fields(Normalise(s))
	for i, w := range all {
		afterPart := i > 0 && PartWordOf(all[i-1]) != "" && isPartNumber(w)
		switch {
		case (w == "a" || w == "an" || w == "the") && i < len(all)-1:
			continue
		case PartWordOf(w) != "" && i < len(all)-1 && isPartNumber(all[i+1]):
			// the word Part goes where its number follows; the others stay,
			// spelled one way
			if kept := PartWordOf(w); kept != "part" {
				words, marked = append(words, kept), append(marked, false)
			}

			continue
		}
		// a numeral after a part word - a lone I too - or closing the title
		// after a title word, as a sequel's number is ("Saga II"): not a
		// lone I, V or X closing one, which is a letter as often, nor a
		// numeral past what a sequel runs to ("Size XL" is no 40)
		if n, ok := NumberOf(w, afterPart || i > 0 && i == len(all)-1 && len(w) > 1); ok {
			w = n
		}
		words, marked = append(words, w), append(marked, afterPart)
	}

	return words, marked
}

// PartWordOf is the part word a word spells, one way for each - "part" for
// Part and Pt, "volume" for Volume and Vol, "episode" for Episode and Ep,
// "chapter" for Chapter and Ch, "book" - or "" for none.
func PartWordOf(w string) string {
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
	_, ok := NumberOf(w, true)

	return ok
}

// romanPart is a Roman numeral of the kind a part or a sequel is numbered by.
var romanPart = regexp.MustCompile(`^[ivx]+$`)

// romanMax is the highest numeral read as a part's or a sequel's number.
const romanMax = 20

// NumberOf is a word read as a number, in digits: digits as they are, a
// number in words, and - when roman is set - a Roman numeral a part or a
// sequel is numbered by, I to XX.
func NumberOf(w string, roman bool) (string, bool) {
	if d, ok := numberWords[w]; ok {
		return d, true
	}
	if IsNumber(w) {
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

// IsNumber says whether a word is digits and nothing else.
func IsNumber(w string) bool {
	return w != "" && !strings.ContainsFunc(w, func(r rune) bool { return !unicode.IsDigit(r) })
}

// Variant says whether two titles are the same title written another
// way, and how (see VariantOf).
func Variant(a, b string) (how string, ok bool) {
	return VariantOf(FormOf(a), FormOf(b))
}

// VariantOf says whether two titles are the same title written another way,
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
func VariantOf(fa, fb Form) (how string, ok bool) {
	wa, wb := fa.Words, fb.Words
	if len(wa) == 0 || len(wb) == 0 {
		return "", false
	}
	numbered := fa.Numbered || fb.Numbered
	if slices.Equal(wa, wb) {
		if onlyNumbers(fa) && onlyNumbers(fb) && fa.Folded != fb.Folded {
			return "", false
		}
		if fa.Folded == fb.Folded && (fa.Qualifier == "") != (fb.Qualifier == "") {
			return fmt.Sprintf("the same title with (%s) on one side alone", fa.Qualifier+fb.Qualifier), true
		}

		return "the same title with its articles or its numbers written otherwise", true
	}
	if PartOneApart(fa, fb) {
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

// LookalikeLetters finds the letters in a Latin title that belong to another
// script and only look Latin: a Cyrillic A (U+0410) spelling Arrival, which
// no search for Arrival finds. plain is the title with each put right. A title written in
// the other script is left alone - one with any letter of it that looks like
// no Latin letter uses the script for itself, and a Greek delta in a Latin
// title is a symbol, not a stand-in - and so is a title with no Latin letter
// at all.
func LookalikeLetters(title string) (found []string, plain string) {
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
