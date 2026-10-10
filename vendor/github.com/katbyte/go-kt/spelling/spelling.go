// Package spelling finds one name spelled two ways.
//
// A library's genres, tags, studios and the names in a music collection's
// tags arrive as people and taggers typed them: "Sci-Fi" and "sci fi",
// "Amélie" and "Amelie", "Warner Bros." and "Warner Bros. Pictures",
// "Noir" and "Nior". Key folds the spellings that mean the same thing onto
// one string, and TypoApart, TruncationOf and InitialsOf say when two keys
// that still differ are a slip of the keyboard, a name cut short or a first
// name reduced to its initial rather than two names. Detection is all this
// does; which spelling to keep is the caller's to decide.
//
// The detectors were found in real libraries first (abs-mcp's and
// embyfin-mcp's spelling audits), and each rule says what it was for.
package spelling

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Key lowercases a value, folds accented letters to plain ones, turns the
// separators people type interchangeably (_ - . /) into spaces, and drops the
// rest of the punctuation: "Sci-Fi" and "sci fi" meet at "sci fi". A letter
// or digit of any other script is kept as it is, lowercased, so "進撃の巨人"
// and "鬼滅の刃" stay two values rather than both folding to nothing.
func Key(s string) string {
	var b strings.Builder
	latin := false // the last rune written was plain ASCII
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == ' ':
			b.WriteRune(r)
			latin = r != ' '
		case r == '_' || r == '-' || r == '.' || r == '/':
			b.WriteRune(' ')
			latin = false
		case r > unicode.MaxASCII && unicode.IsSpace(r):
			// an ideographic or a non-breaking space is still a space
			b.WriteRune(' ')
			latin = false
		case r > unicode.MaxASCII:
			// Amélie is Amelie, not Amlie; 進撃の巨人 is itself, not nothing
			if spelling, word := WordRune(r, latin); word && spelling != "" {
				b.WriteString(spelling)
				latin = spelling[0] < utf8.RuneSelf
			}
		}
	}

	return strings.Join(strings.Fields(b.String()), " ")
}

// WordRune is how a fold for comparing names reads a rune past ASCII. An
// accented Latin letter is its plain spelling (FoldLetter), and any other
// letter or digit is itself, whatever its script: dropping those made every
// name written in Japanese, Greek or Cyrillic fold to nothing, so unrelated
// ones all met. A combining mark belongs to the letter before it: on a plain
// Latin letter (afterLatin) it is an accent written apart (e and U+0301 for
// é), folded away as the composed letter's is, and in a script that writes
// its vowels as marks it is part of the word and kept. word is false for
// punctuation, symbols and spaces.
func WordRune(r rune, afterLatin bool) (spelling string, word bool) {
	if folded := FoldLetter(r); folded != "" {
		return folded, true
	}
	switch {
	case unicode.IsLetter(r), unicode.IsDigit(r):
		return string(r), true
	case unicode.IsMark(r) && afterLatin:
		return "", true
	case unicode.IsMark(r):
		return string(r), true
	}

	return "", false
}

// FoldLetter is the plain-ASCII spelling of an accented Latin letter, or ""
// for a rune that is not one: é is e, ß is ss, þ is th.
func FoldLetter(r rune) string {
	for ascii, accented := range foldTable {
		if strings.ContainsRune(accented, r) {
			return ascii
		}
	}

	return ""
}

// foldTable is the accented Latin letters by their plain spelling, lowercase;
// a value is lowercased before it is folded.
var foldTable = map[string]string{
	"a": "àáâãäåāąă", "ae": "æ", "c": "çćčċ", "d": "ďđð", "e": "èéêëēęěė", "g": "ğģ",
	"i": "ìíîïīıį", "l": "łļľ", "n": "ñńňņ", "o": "òóôõöøōőœ", "r": "řŗ", "s": "šşśș", "ss": "ß",
	"t": "ťţț", "th": "þ", "u": "ùúûüūůűų", "y": "ýÿ", "z": "žźż",
}

// Marks are the marks in a value that make another name of it, in the order
// they come: a plus, a bang, a bracket - "discovery+" is a streaming brand
// and "Discovery" a channel, "Idea(L)" a company and "Ideal" another. Case,
// spacing, accents, separators, an apostrophe or an ampersand make no other
// name ("Action & Adventure" is "Action Adventure"), and Key folds them;
// two values of one Key and different Marks are two names.
func Marks(v string) string {
	var b strings.Builder
	for _, r := range v {
		if strings.ContainsRune("+!()[]{}#@*$%=~|<>^", r) {
			b.WriteRune(r)
		}
	}

	return b.String()
}

// Letters is how long a value is, in letters rather than bytes: an ideograph
// takes three bytes, and counted that way a two-letter name passed for a
// six-letter one and was judged a typo apart from every other two-letter
// name sharing one of its letters.
func Letters(s string) int { return utf8.RuneCountInString(s) }

// TruncationOf reports whether short is long cut off ("warner bros" in
// "warner bros pictures"): at least six letters, and whole words. Both are
// keys (Key).
func TruncationOf(short, long string) bool {
	if short == long || Letters(strings.ReplaceAll(short, " ", "")) < 6 {
		return false
	}

	return strings.HasPrefix(long, short+" ")
}

// InitialsOf reports whether two keys (Key) are one person's name, one of
// them with the first name reduced to its initial, middle names and initials
// aside: "c z dunn" is "christian dunn", and "j k rowling" is "joanne
// rowling". The last name has to be the same and at least three letters, and
// the reduced first name a single letter: "joe hill" is not "joey w hill",
// those are two people, and "chris dunn" is a shortened name, not an initial.
// Either may be the one with the initial.
func InitialsOf(short, long string) bool {
	sw, lw := strings.Fields(short), strings.Fields(long)
	if len(sw) < 2 || len(lw) < 2 || short == long {
		return false
	}

	last := sw[len(sw)-1]
	if Letters(last) < 3 || last != lw[len(lw)-1] {
		return false
	}

	a, b := sw[0], lw[0]
	if Letters(a) > Letters(b) {
		a, b = b, a
	}

	return Letters(a) == 1 && Letters(b) > 1 && strings.HasPrefix(b, a)
}

// TypoApart reports whether two keys (Key) differ by a slip of the keyboard:
// one edit for anything six letters or longer, two for twelve or longer when
// they start with the same word - and, between values of as many words,
// every word that differs is a slip itself (SlipsInWords).
func TypoApart(a, b string) bool {
	if a == b || Letters(a) < 6 || Letters(b) < 6 {
		return false
	}
	switch Distance(a, b, 2) {
	case 0, 1:
	case 2:
		if Letters(a) < 12 || Letters(b) < 12 || strings.Fields(a)[0] != strings.Fields(b)[0] {
			return false
		}
	default:
		return false
	}

	return SlipsInWords(a, b)
}

// SlipsInWords says whether two values of as many words differ only by slips
// within words: each word that differs is five letters or more and one edit
// from the other, or a shorter one with two letters swapped ("Nior" for
// "Noir", "Flim" for "Film") or letters dropped from its middle ("Nr"). A
// shorter word otherwise an edit or two apart is as often another word as a
// slip - "teenage life" and "teenage love", "coming of age" and "coming of
// rage" - and the values two things. Values of more or fewer words differ by
// a space added or dropped, which is a slip.
func SlipsInWords(a, b string) bool {
	wa, wb := strings.Fields(a), strings.Fields(b)
	if len(wa) != len(wb) {
		return true
	}
	for i := range wa {
		switch {
		case wa[i] == wb[i]:
		case Letters(wa[i]) >= 5 && Letters(wb[i]) >= 5:
			if Distance(wa[i], wb[i], 1) > 1 {
				return false
			}
		case !Swapped(wa[i], wb[i]) && !DroppedFromMiddle(wa[i], wb[i]) && !DroppedFromMiddle(wb[i], wa[i]):
			return false
		}
	}

	return true
}

// Swapped says whether two words are one with two letters next to each other
// swapped: "nior" and "noir".
func Swapped(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) != len(rb) || len(ra) < 3 {
		return false
	}
	at := -1
	for i := range ra {
		if ra[i] != rb[i] {
			at = i

			break
		}
	}

	return at >= 0 && at+1 < len(ra) && ra[at] == rb[at+1] && ra[at+1] == rb[at] && string(ra[at+2:]) == string(rb[at+2:])
}

// DroppedFromMiddle says whether short is long with letters dropped from
// between its first and its last: "nr" from "noir". A letter added or lost
// at either end ("age", "rage") makes another word as often as not, and so
// does one changed ("life", "lift").
func DroppedFromMiddle(short, long string) bool {
	s, l := []rune(short), []rune(long)
	if len(s) < 2 || len(s) >= len(l) || s[0] != l[0] || s[len(s)-1] != l[len(l)-1] {
		return false
	}
	at := 0
	for _, r := range l {
		if at < len(s) && r == s[at] {
			at++
		}
	}

	return at == len(s)
}

// Distance is the Damerau-Levenshtein distance (optimal string alignment, so
// a transposition is one edit), capped: anything past limit comes back as
// limit+1, and strings whose lengths differ by more than limit are not
// walked.
func Distance(a, b string, limit int) int {
	ra, rb := []rune(a), []rune(b)
	if d := len(ra) - len(rb); d > limit || -d > limit {
		return limit + 1
	}
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		best := cur[0]
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
			best = min(best, cur[j])
		}
		if best > limit {
			return limit + 1
		}
		prev2, prev, cur = prev, cur, prev2
	}
	if prev[len(rb)] > limit {
		return limit + 1
	}

	return prev[len(rb)]
}
