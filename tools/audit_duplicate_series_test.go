package tools

import (
	"slices"
	"testing"
)

// A folder name in another script keeps its letters. Folding them away made
// every Japanese folder under one parent the same name, and the audit
// reported unrelated shows as one show held twice.
func TestFolderKeyKeepsEveryScript(t *testing.T) {
	t.Parallel()

	for _, pair := range [][2]string{
		{"進撃の巨人", "鬼滅の刃"},
		{"星の森", "月の森"},
		{"Тихий дом", "Тихий сад"},
		{"Zzyzx Studio α", "Zzyzx Studio β"},
	} {
		a, b := folderKey(pair[0]), folderKey(pair[1])
		if a == "" || b == "" || a == b {
			t.Errorf("%q and %q are different folders: %q against %q", pair[0], pair[1], a, b)
		}
	}

	// and folds them the way it folds a Latin name: case, spacing and
	// punctuation, and an accent written as a separate mark
	for _, same := range [][]string{
		{"Тихий дом", "ТИХИЙ ДОМ", "Тихий  дом"},
		{"Zzyzx Studio α", "ZZYZX STUDIO Α"},
		{"星の森", "星の森 "},
		{"星の 森", "星の・森"},
		{"The Law According to Lidia Poët", "The Law According to Lidia Poe\u0308t"},
	} {
		for _, other := range same[1:] {
			if folderKey(other) != folderKey(same[0]) {
				t.Errorf("%q and %q are the same folder: %q against %q", other, same[0], folderKey(other), folderKey(same[0]))
			}
		}
	}

	// a mark belongs to the letter it sits on in a script that writes vowels
	// that way, so a word is not split at one
	if got := folderKey("नमस्ते दुनिया"); got != "नमस्ते दुनिया" {
		t.Errorf("folderKey split a word at its vowel signs: %q", got)
	}
}

// audit_duplicate_series against folders named in other scripts, and ones a
// server on Windows reports with backslashes: unrelated shows are not one
// show held twice, a real pair is still found, and a folder name that folds
// to nothing is left out rather than colliding with every other such name.
func TestAuditDuplicateSeriesAcrossScriptsAndSeparators(t *testing.T) {
	t.Parallel()

	shows := []*fakeSeries{
		{id: "a", name: "Zzyzx A", path: "/tv/進撃の巨人"},
		{id: "b", name: "Zzyzx B", path: "/tv/鬼滅の刃"},
		{id: "c", name: "Zzyzx C", path: "/tv/Тихий дом"},
		{id: "d", name: "Zzyzx D", path: "/tv/Тихий сад"},
		{id: "e", name: "Zzyzx E", path: "/tv/!!!"},
		{id: "f", name: "Zzyzx F", path: "/tv/???"},
		// a rename on a Windows server that changed only case and spacing
		{id: "w1", name: "Zzyzx Show", path: `D:\TV\Zzyzx Show`},
		{id: "w2", name: "Zzyzx Show", path: `D:\TV\zzyzx  show`},
	}
	out := mustCall(t, session(t, tvServer(t, shows...), Options{}), "audit_duplicate_series", map[string]any{})
	groups := objects(t, out["groups"], "groups")
	if len(groups) != 1 || number(t, out["total_findings"], "total_findings") != 1 {
		t.Fatalf("groups = %v, want only the Windows pair", groups)
	}
	series := objects(t, groups[0]["series"], "series")
	ids, folders := make([]string, 0, len(series)), make([]string, 0, len(series))
	for _, row := range series {
		ids = append(ids, text(row["series_id"]))
		folders = append(folders, text(row["folder"]))
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"w1", "w2"}) {
		t.Errorf("group holds %v, want the two Windows folders", ids)
	}
	// the folder is the last segment, not the whole path
	if !slices.Equal(folders, []string{"Zzyzx Show", "zzyzx  show"}) {
		t.Errorf("folders = %q", folders)
	}
}
