// Package whitespace finds the spaces out of place in a name: doubled,
// leading, trailing and odd spaces, and a space before a colon or a file
// extension. For each name it says what is wrong (Problems), shows it with
// the offending spaces made visible (Visible) and puts it right (Fixed).
//
// Names come in kinds (Text), because what is wrong depends on what the text
// is: a file name has an extension a space can sit before, and a title in
// its own language keeps the spaces its typography sets - French puts one
// before a colon.
package whitespace

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The problems, named as Problems reports them, in the order it reports them.
const (
	OddSpace        = "odd_space"
	DoubleSpace     = "double_space"
	EdgeSpace       = "edge_space"
	BeforeExtension = "space_before_extension"
	BeforeColon     = "space_before_colon"
)

// ProblemOrder is every problem, in the order Problems lists them.
var ProblemOrder = []string{OddSpace, DoubleSpace, EdgeSpace, BeforeExtension, BeforeColon}

var (
	// run is two or more spaces in a row
	run = regexp.MustCompile(` {2,}`)
	// colon is a space before a colon or the look-alike U+A789 a renamer
	// writes for one, "Title ꞉ Subtitle" where the library has "Title꞉
	// Subtitle"
	colon = regexp.MustCompile(` +([:꞉])`)
)

// Odd is a space that is not the ordinary one: a tab, a line break, a
// non-breaking or typographic space. The ideographic space, U+3000, is not
// one: Japanese titles use it as written.
func Odd(r rune) bool {
	switch r {
	case '\t', '\n', '\r', '\v', '\f', 0x85, 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F:
		return true
	}

	return r >= 0x2000 && r <= 0x200A
}

// Typographic is a no-break or narrow space a language's own typography sets
// on purpose: French puts one before a colon ("Titre : Sous-titre").
func Typographic(r rune) bool {
	return r == 0xA0 || r == 0x202F || (r >= 0x2000 && r <= 0x200A)
}

// Any is an ordinary space or an odd one.
func Any(r rune) bool { return r == ' ' || Odd(r) }

// Text is what a text is, which decides what is wrong with its spaces.
type Text int

const (
	// Name is a name or a value: every problem counts
	Name Text = iota
	// File is a file name, read as a stem and an extension
	File
	// Foreign is an original title, written in its own language: the space
	// before a colon and the typographic spaces that language's typography
	// sets are left alone, and only the rest is a problem
	Foreign
)

// odd is whether a rune is a space out of place in this kind of text.
func (k Text) odd(r rune) bool {
	return Odd(r) && (k != Foreign || !Typographic(r))
}

// StartsWithSpace says whether a text begins with a space of any kind.
func StartsWithSpace(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)

	return Any(r)
}

// EndsWithSpace says whether a text ends with a space of any kind.
func EndsWithSpace(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)

	return Any(r)
}

// SplitExt parts a file name into its stem and its extension; a name whose
// last dot starts no plausible extension (longer than five letters, or with
// a space in it) is all stem.
func SplitExt(name string) (stem, ext string) {
	ext = path.Ext(name)
	if ext == "" || ext == name || len(ext) > 6 || strings.ContainsFunc(ext, Any) {
		return name, ""
	}

	return strings.TrimSuffix(name, ext), ext
}

// Problems is what is wrong with the spaces in one text, in ProblemOrder;
// nothing for a text whose spaces are all in place.
func Problems(name string, k Text) []string {
	var out []string
	if strings.ContainsFunc(name, k.odd) {
		out = append(out, OddSpace)
	}
	if strings.Contains(name, "  ") {
		out = append(out, DoubleSpace)
	}
	stem, ext := name, ""
	if k == File {
		stem, ext = SplitExt(name)
	}
	// an odd space at an end is at the end all the same
	if StartsWithSpace(name) || EndsWithSpace(name) {
		out = append(out, EdgeSpace)
	}
	if ext != "" && EndsWithSpace(stem) {
		out = append(out, BeforeExtension)
	}
	if k != Foreign && colon.MatchString(name) {
		out = append(out, BeforeColon)
	}

	return out
}

// Visible writes a text with the offending spaces made visible: ␣ for a
// space in a run, at an end, before a colon or before the extension, and
// [U+00A0] for a space that is not the ordinary one.
func Visible(name string, k Text) string {
	runes := []rune(name)
	extAt := len(runes)
	if k == File {
		if _, ext := SplitExt(name); ext != "" {
			extAt = len(runes) - utf8.RuneCountInString(ext)
		}
	}
	var b strings.Builder
	for i := 0; i < len(runes); {
		r := runes[i]
		if k.odd(r) {
			fmt.Fprintf(&b, "[U+%04X]", r)
			i++

			continue
		}
		if r != ' ' {
			b.WriteRune(r)
			i++

			continue
		}
		end := i
		for end < len(runes) && runes[end] == ' ' {
			end++
		}
		beforeColon := k != Foreign && end < len(runes) && (runes[end] == ':' || runes[end] == '꞉')
		mark := end-i > 1 || i == 0 || end == len(runes) || end == extAt || beforeColon
		for ; i < end; i++ {
			if mark {
				b.WriteRune('␣')
			} else {
				b.WriteRune(' ')
			}
		}
	}

	return b.String()
}

// Fixed is a text with its spaces put right: an odd space made an ordinary
// one, runs made one, the ends and the space before a colon or the extension
// dropped.
func Fixed(name string, k Text) string {
	s := strings.Map(func(r rune) rune {
		if k.odd(r) {
			return ' '
		}

		return r
	}, name)
	stem, ext := s, ""
	if k == File {
		stem, ext = SplitExt(s)
	}
	stem = run.ReplaceAllString(stem, " ")
	if k != Foreign {
		stem = colon.ReplaceAllString(stem, "$1")
	}

	return strings.TrimFunc(stem, Any) + ext
}

// DroppedAt is what a title holds where a name has two spaces in a row,
// when the words either side of the gap are in the title too: ":" for "Dune
// Part Two" written with two spaces against "Dune: Part Two", the asterisks
// of a censored word. "" when the two do not line up, or the title holds
// nothing there, or more than a few words - which is another name rather
// than a character a renamer dropped.
func DroppedAt(name, title string) string {
	gap := run.FindStringIndex(name)
	if gap == nil {
		return ""
	}
	before, after := strings.Fields(name[:gap[0]]), strings.Fields(name[gap[1]:])
	if len(before) == 0 || len(after) == 0 {
		return ""
	}
	last, next := before[len(before)-1], after[0]
	at := IndexWord(title, last)
	if at < 0 {
		return ""
	}
	rest := title[at+len(last):]
	end := IndexWord(rest, next)
	if end < 0 {
		return ""
	}
	between := strings.TrimFunc(rest[:end], Any)
	if between == "" || len(strings.Fields(between)) > 3 || utf8.RuneCountInString(between) > 24 {
		return ""
	}

	return between
}

// IndexWord is where word first stands in s as a word of its own, and not
// inside a longer one ("IV" in "Episode IV", not in "DIVE"); -1 when it does
// not.
func IndexWord(s, word string) int {
	for from := 0; from <= len(s)-len(word); {
		i := strings.Index(s[from:], word)
		if i < 0 {
			return -1
		}
		i += from
		before, _ := utf8.DecodeLastRuneInString(s[:i])
		after, _ := utf8.DecodeRuneInString(s[i+len(word):])
		if (i == 0 || !IsWordChar(before)) && (i+len(word) == len(s) || !IsWordChar(after)) {
			return i
		}
		from = i + 1
	}

	return -1
}

// IsWordChar is a letter or a digit, of any script.
func IsWordChar(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
