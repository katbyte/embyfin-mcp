package naming

import (
	"strings"
	"testing"
)

// A letter of another script that only looks Latin, inside a Latin title, is
// named with the plain spelling; a title written in that script, or a Greek
// letter used as a symbol, is left alone.
func TestLookalikeLetters(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		title, plain string
		letters      []string
	}{
		{"\u0410rrival", "Arrival", []string{"the Cyrillic \u0410 (U+0410) in place of the Latin A"}},
		{"S\u0435verance", "Severance", []string{"the Cyrillic \u0435 (U+0435) in place of the Latin e"}},
		{"\u0421ub\u0435", "Cube", []string{"the Cyrillic \u0421 (U+0421) in place of the Latin C", "the Cyrillic \u0435 (U+0435) in place of the Latin e"}},
		{"\u0391lien", "Alien", []string{"the Greek \u0391 (U+0391) in place of the Latin A"}},
		// Latin letters only
		{"Arrival", "Arrival", nil},
		// no Latin letter at all: written in that script
		{"もののけ姫", "もののけ姫", nil},
		{"\u0414\u044E\u043D\u0430", "\u0414\u044E\u043D\u0430", nil},
		// Dune's title in Russian, and a title in both scripts whose Cyrillic
		// is its own: its De (U+0414) and Yu (U+044E) look like no Latin letter
		{"\u0414\u044E\u043D\u0430 (Dune)", "\u0414\u044E\u043D\u0430 (Dune)", nil},
		// a Greek letter used as a symbol makes the Greek its own
		{"Arrival \u0394", "Arrival \u0394", nil},
	} {
		letters, plain := LookalikeLetters(tc.title)
		if plain != tc.plain || strings.Join(letters, "|") != strings.Join(tc.letters, "|") {
			t.Errorf("%q = %q %v, want %q %v", tc.title, plain, letters, tc.plain, tc.letters)
		}
	}
}

// The titles an item goes by, each saying which, the year a server leaves on
// an unmatched film's name taken off, and a lookalike name spelled plainly.
func TestKnownTitles(t *testing.T) {
	t.Parallel()

	got := KnownTitles("\u0421ube (1997)", "Cube", "Cube")
	want := []KnownTitle{{"Cube", AsName}, {"Cube", AsOriginal}, {"Cube", AsSort}}
	if len(got) != len(want) {
		t.Fatalf("known titles = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("title %d = %v, want %v", i, got[i], want[i])
		}
	}
	if got := KnownTitles("Alien", "", ""); len(got) != 1 {
		t.Errorf("an item with a name alone = %v", got)
	}
}
