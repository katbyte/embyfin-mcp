package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// A value in another script keeps its letters. Folding them away made
// "Zzyzx Studio α" and "Zzyzx Studio β" one spelling of one studio, with a
// merge advised, and left a value written wholly in another script out of
// the audit altogether.
func TestNormKeepsEveryScript(t *testing.T) {
	t.Parallel()

	for _, pair := range [][2]string{
		{"進撃の巨人", "鬼滅の刃"},
		{"Zzyzx Studio α", "Zzyzx Studio β"},
		{"Тихий дом", "Тихий сад"},
	} {
		a, b := norm(pair[0]), norm(pair[1])
		if a == "" || b == "" || a == b {
			t.Errorf("%q and %q are different values: %q against %q", pair[0], pair[1], a, b)
		}
	}
	for in, want := range map[string]string{
		"ТИХИЙ ДОМ":         "тихий дом",
		"Zzyzx Studio Α":    "zzyzx studio α",
		"星の森\u3000特集":       "星の森 特集", // an ideographic space is a space
		"Amélie":            "amelie",
		"Ame\u0301lie":      "amelie", // the accent written as a separate mark
		"  Sci-Fi / Drama ": "sci fi drama",
	} {
		if got := norm(in); got != want {
			t.Errorf("norm(%q) = %q, want %q", in, got, want)
		}
	}

	// a length is counted in letters: two ideographs are too short to call
	// a typo apart, however many bytes they take
	if typoApart(norm("星光"), norm("月光")) || truncationOf(norm("東星"), norm("東星 映画")) {
		t.Error("two-letter values were judged as long ones")
	}
}

// The report over values in other scripts: two spellings of one tag in
// Cyrillic are one group, and two studios a Greek letter apart are never
// one spelling to merge.
func TestSpellingReportAcrossScripts(t *testing.T) {
	t.Parallel()

	counts := newSpellingCounts(vocabFields)
	for _, v := range []struct{ tag, studio string }{
		{"Тихий дом", "Zzyzx Studio α"},
		{"ТИХИЙ ДОМ", "Zzyzx Studio β"},
		{"Тихий дом", "進撃の巨人"},
	} {
		counts.add(&embyfin.Item{Tags: []string{v.tag}, Studios: []embyfin.NameRef{{Name: v.studio}}})
	}

	tags := counts.report(fieldTags)
	if len(tags) != 1 || tags[0].Kind != "spelling" || tags[0].Keep != "Тихий дом" || len(tags[0].Spellings) != 2 {
		t.Errorf("tags = %+v, want the two spellings of one Cyrillic tag", tags)
	}
	for _, g := range counts.report(fieldStudios) {
		if g.Kind == "spelling" {
			t.Errorf("two studios a Greek letter apart were called one spelling: %+v", g)
		}
	}
	if n := len(counts[fieldStudios]); n != 3 {
		t.Errorf("studios counted = %d, want all three, the Japanese one included", n)
	}
}

// Values a word apart are two things, and so are names a mark apart. "teenage
// life" and "teenage love" were a near group, two edits in words too short
// to tell a slip from another word; "Discovery" and "discovery+" one value
// spelled two ways, and "Idea(L)" and "Ideal" too, because the fold drops
// every mark. A plus, a bang or a bracket makes another name - a streaming
// brand, a company - where case, spacing and a separator do not.
func TestSpellingKeepsWordsAndMarksApart(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"teenage life", "teenage love", false},
		{"teenage life", "teenage lift", false},
		{"coming of age", "coming of rage", false},
		{"martial arts film", "martial arts flim", true},
		{"science fiction", "science fictoin", true},
		{"superhero", "superheros", true},
		{"sciencefiction", "science fiction", true},
		{"time travel", "time travell", true},
		{"cyberpunk noir", "cyberpunk nr", true}, // letters dropped from a word's middle
		{"film nior", "film noir", true},         // two letters swapped in a short word
		{"kids flim", "kids film", true},
		{"road tirp", "road trip", true},
		{"teenage love", "teenage lve", true},
		{"coming of age", "coming of ace", false}, // a short word changed: another word
	} {
		if got := typoApart(norm(tc.a), norm(tc.b)); got != tc.want {
			t.Errorf("typoApart(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}

	counts := newSpellingCounts([]string{fieldStudios, fieldTags})
	for _, s := range []string{"Discovery", "DISCOVERY", "discovery+", "Discovery+ Originals", "Idea(L)", "Ideal", "Warner Bros.", "Warner Bros", "Warner Bros. (US)", "Warner Bros. (US", "Yahoo!", "Yahoo"} {
		counts.addValue(fieldStudios, s)
	}
	for _, s := range []string{"teenage life", "teenage love", "Action & Adventure", "Action Adventure"} {
		counts.addValue(fieldTags, s)
	}
	groups := counts.report(fieldStudios)
	studios := make([][]string, 0, len(groups))
	for _, g := range groups {
		var names []string
		for _, sp := range g.Spellings {
			names = append(names, sp.Value)
		}
		slices.Sort(names)
		studios = append(studios, append([]string{g.Kind}, names...))
	}
	// values only a mark apart are a near group to check, never one
	// spelling; the brand and its own longer name are a pair as any two
	// studios are, and the brand and the channel's name are not
	slices.SortFunc(studios, func(a, b []string) int { return strings.Compare(strings.Join(a, "|"), strings.Join(b, "|")) })
	want := [][]string{
		{"contains", "Discovery+ Originals", "discovery+"},
		{"near", "DISCOVERY", "Discovery", "discovery+"},
		{"near", "Idea(L)", "Ideal"},
		{"near", "Warner Bros. (US", "Warner Bros. (US)"},
		{"near", "Yahoo", "Yahoo!"},
		{"spelling", "DISCOVERY", "Discovery"},
		{"spelling", "Warner Bros", "Warner Bros."},
	}
	if !slices.EqualFunc(studios, want, slices.Equal) {
		t.Errorf("studio groups = %v, want %v", studios, want)
	}
	for _, g := range counts.report(fieldStudios) {
		if g.Kind == "near" && !strings.Contains(g.Note, "differ only by a mark") {
			t.Errorf("a group a mark apart says nothing of it: %+v", g)
		}
	}
	var tags []string
	for _, g := range counts.report(fieldTags) {
		for _, sp := range g.Spellings {
			tags = append(tags, g.Kind+" "+sp.Value)
		}
	}
	slices.Sort(tags)
	// an ampersand is no brand: the one genre with it and without
	if want := []string{"spelling Action & Adventure", "spelling Action Adventure"}; !slices.Equal(tags, want) {
		t.Errorf("tag groups = %v, want %v", tags, want)
	}
}

// A studio and a longer name it begins are a pair of their own. Joined into
// clusters through the shorter name, "Warner Bros." made one group of Warner
// Bros. Pictures and Warner Bros. Television, and metadata_rename would have
// merged two companies into the most used on every item. Each pair is still
// reported, as something to check, and says when the shorter name begins
// others too.
func TestSpellingContainsPairsOnly(t *testing.T) {
	t.Parallel()

	counts := newSpellingCounts([]string{fieldStudios})
	for studio, n := range map[string]int{
		"Warner Bros.": 3, "Warner Bros. Pictures": 2, "Warner Bros. Television": 1,
		"Paramount": 2, "Paramount Television": 1, "Paramount Animation": 1,
		"Sony Pictures": 2, "Sony Pictures Classics": 1, "Sony Pictures Television": 1,
		"Legendary Pictures": 1, "Legendary": 1,
	} {
		for range n {
			counts.addValue(fieldStudios, studio)
		}
	}

	pairs := map[[2]string]vocabGroup{}
	for _, g := range counts.report(fieldStudios) {
		if g.Kind != "contains" || len(g.Spellings) != 2 {
			t.Errorf("a group that is not a pair of a name and a longer one = %+v", g)

			continue
		}
		names := [2]string{g.Spellings[0].Value, g.Spellings[1].Value}
		if len(names[0]) > len(names[1]) {
			names[0], names[1] = names[1], names[0]
		}
		pairs[names] = g
	}
	for _, want := range [][2]string{
		{"Warner Bros.", "Warner Bros. Pictures"},
		{"Warner Bros.", "Warner Bros. Television"},
		{"Paramount", "Paramount Television"},
		{"Paramount", "Paramount Animation"},
		{"Sony Pictures", "Sony Pictures Classics"},
		{"Sony Pictures", "Sony Pictures Television"},
		{"Legendary", "Legendary Pictures"},
	} {
		g, ok := pairs[want]
		if !ok {
			t.Errorf("no pair of %q and %q in %v", want[0], want[1], pairs)

			continue
		}
		// a name beginning two others may be their parent company
		if many := want[0] != "Legendary"; many != (g.Note != "") {
			t.Errorf("%v note = %q", want, g.Note)
		}
	}
	if len(pairs) != 7 {
		t.Errorf("%d pairs, want 7: %v", len(pairs), pairs)
	}
	if g := pairs[[2]string{"Warner Bros.", "Warner Bros. Pictures"}]; g.Keep != "Warner Bros." ||
		g.Note != `"Warner Bros." also begins "Warner Bros. Television": it may name their parent company rather than "Warner Bros. Pictures" cut short` {
		t.Errorf("Warner Bros. and its pictures = %+v", g)
	}
}
