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

// plain is a text with every space that is odd in this kind of text made an
// ordinary one. An odd space doubles, ends a name and stands before a colon
// or an extension as an ordinary one does, so those are looked for in the
// text read this way: two no-break spaces are a double space, and one before
// a colon is a space before a colon.
func (k Text) plain(name string) string {
	return strings.Map(func(r rune) rune {
		if k.odd(r) {
			return ' '
		}

		return r
	}, name)
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
// a space in it) is all stem. The name is read without the spaces after it,
// which are no part of either: "Chapter 1 .mp3 " is "Chapter 1 " and ".mp3",
// so a space after the extension hides none before it.
func SplitExt(name string) (stem, ext string) {
	name = strings.TrimRightFunc(name, Any)
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
	// an odd space is out of place where an ordinary one would be, too
	plain := k.plain(name)
	if strings.Contains(plain, "  ") {
		out = append(out, DoubleSpace)
	}
	// an odd space at an end is at the end all the same
	if StartsWithSpace(name) || EndsWithSpace(name) {
		out = append(out, EdgeSpace)
	}
	if k == File {
		if stem, ext := SplitExt(plain); ext != "" && strings.HasSuffix(stem, " ") {
			out = append(out, BeforeExtension)
		}
	}
	if k != Foreign && colon.MatchString(plain) {
		out = append(out, BeforeColon)
	}

	return out
}

// Visible writes a text with the offending spaces made visible: ␣ for an
// ordinary space in a run, at an end, before a colon or before the
// extension, and [U+00A0] for a space that is not the ordinary one, wherever
// it is. An odd space makes a run with the spaces beside it, so the ordinary
// one next to it is marked too.
func Visible(name string, k Text) string {
	runes, plain := []rune(name), []rune(k.plain(name))
	extAt := -1
	if k == File {
		if stem, ext := SplitExt(string(plain)); ext != "" {
			extAt = utf8.RuneCountInString(stem)
		}
	}
	var b strings.Builder
	for i := 0; i < len(runes); {
		if plain[i] != ' ' {
			b.WriteRune(runes[i])
			i++

			continue
		}
		end := i
		for end < len(plain) && plain[end] == ' ' {
			end++
		}
		beforeColon := k != Foreign && end < len(plain) && (plain[end] == ':' || plain[end] == '꞉')
		mark := end-i > 1 || i == 0 || end == len(plain) || end == extAt || beforeColon
		for ; i < end; i++ {
			switch {
			case k.odd(runes[i]):
				fmt.Fprintf(&b, "[U+%04X]", runes[i])
			case mark:
				b.WriteRune('␣')
			default:
				b.WriteRune(' ')
			}
		}
	}

	return b.String()
}

// Fixed is a text with its spaces put right: an odd space made an ordinary
// one, runs made one, the ends and the space before a colon or the extension
// dropped. It is "" when nothing is left of the name but spaces, or its
// extension: there is no name to suggest, and ".mkv" for " .mkv" would be a
// file the next reader takes for a hidden one.
func Fixed(name string, k Text) string {
	stem, ext := k.plain(name), ""
	if k == File {
		stem, ext = SplitExt(stem)
	}
	stem = run.ReplaceAllString(stem, " ")
	if k != Foreign {
		stem = colon.ReplaceAllString(stem, "$1")
	}
	stem = strings.TrimFunc(stem, Any)
	if stem == "" {
		return ""
	}

	return stem + ext
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
