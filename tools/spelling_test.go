package tools

import (
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
