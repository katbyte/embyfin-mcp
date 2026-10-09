//go:build integration

package acceptance

import (
	"fmt"
	"strings"
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"

	"github.com/katbyte/embyfin-mcp/lib/mediapath"
	"github.com/katbyte/embyfin-mcp/lib/naming"
)

// runStyleFiles is one file of each way a name spells a run of two
// episodes, each its own pair of numbers so no two files are one episode's
// versions: the first episode of each, and the marker as the file writes it.
var runStyleFiles = []struct {
	first  int
	marker string
}{
	{1, "S02E01-E02"},
	{3, "S02E03E04"},
	{5, "S02E05-06"},
	{7, "S02E07+E08"},
	{9, "S02E09.S02E10"},
	{11, "02x11-12"},
	{13, "02x13+14"},
	{15, "02x15x16"},
	{17, "02x17-x18"},
	{19, "02x19 - 02x20"},
	{21, "02x21 02x22"},
	{23, "S02x23.S02x24"},
	{25, "S02E25 - E26"},
	{27, "S02xE27xE28"},
	{29, "02x29-02x30"},
	{31, "02x31 - x32"},
	{33, "S02E33-X34"},
	{35, "S02x35x36"},
	{37, "S02E37 - S02E38"},
	{39, "S02.E39-E40"},
	{41, "S02E41+42"},
}

// A file holding two episodes is named a dozen ways, and each server reads
// some of them as a run and the rest as the first episode alone, listing the
// second as missing. One file of each style, in a show of its own, says
// which this server reads; the table the tools answer from
// (naming.RunStyleRead) has to say the same, and audit_file_path has to say
// of each file the server read short that its name is why.
func TestRunStylesTheServerReads(t *testing.T) {
	const show = "Zzyzx Runs (2020)"
	video := fixture(t, "messy-shows/hack Liminality (2002)/Season 01/hack Liminality S01E03.mp4")
	files := map[string][]byte{"messy-shows/" + show + "/tvshow.nfo": showNfo("Zzyzx Runs", nil)}
	name := func(marker string) string { return "Zzyzx Runs - " + marker + " - Alpha.mp4" }
	for _, f := range runStyleFiles {
		files["messy-shows/"+show+"/Season 02/"+name(f.marker)] = video
	}
	stage(t, plus(0, 1, len(runStyleFiles)), files, "messy-shows/"+show)

	out := suite.Call(t, "library_episodes", map[string]any{"library": "Messy Shows", "series": "Zzyzx Runs", "fields": []string{"path"}})
	byFile := map[string]map[string]any{}
	for _, row := range acc.Rows(t, out["episodes"], "episodes") {
		byFile[mediapath.Base(acc.Str(row["path"]))] = row
	}
	if len(byFile) != len(runStyleFiles) {
		t.Fatalf("the server lists %d episodes of the staged show, want %d: %v", len(byFile), len(runStyleFiles), out["episodes"])
	}

	unread := map[string]bool{} // files the server read as their first episode alone
	for _, f := range runStyleFiles {
		row := byFile[name(f.marker)]
		if row == nil {
			t.Errorf("%s: not listed", f.marker)

			continue
		}
		rel := naming.ParseRelease(name(f.marker))
		if rel.Episode != f.first || rel.EpisodeEnd != f.first+1 {
			t.Fatalf("%s: the parser reads S%02dE%02d-%02d, want E%02d-E%02d", f.marker, rel.Season, rel.Episode, rel.EpisodeEnd, f.first, f.first+1)
		}
		episode, end := acc.NumOr0(row["episode"]), acc.NumOr0(row["episode_end"])
		observed := episode == f.first && end == f.first+1
		if !observed {
			unread[name(f.marker)] = true
		}
		read, known := naming.RunStyleRead(rel.RunStyle(), string(backend))
		switch {
		case !known:
			t.Errorf("%s (%s): %s reads it as a run: %v (episode %d, end %d); the table does not say for this server", f.marker, rel.RunStyle(), backend, observed, episode, end)
		case read != observed:
			t.Errorf("%s (%s): %s reads it as a run: %v (episode %d, end %d), the table says %v", f.marker, rel.RunStyle(), backend, observed, episode, end, read)
		default:
			t.Logf("%s (%s): read as a run %v", f.marker, rel.RunStyle(), observed)
		}
	}

	// the file path audit: every file the server read short is a row saying
	// the server does not read its style, and none the server read is one
	audit := suite.Call(t, "audit_file_path", map[string]any{"library": "Messy Shows", "checks": "episode", "limit": 500})
	said := map[string]string{}
	for _, row := range acc.Rows(t, audit["findings"], "findings") {
		if strings.Contains(acc.Str(row["path"]), "/"+show+"/") {
			said[mediapath.Base(acc.Str(row["path"]))] = strings.Join(acc.Texts(row["problems"]), " | ")
		}
	}
	for _, f := range runStyleFiles {
		problems, found := said[name(f.marker)]
		switch {
		case unread[name(f.marker)] && !found:
			t.Errorf("%s: the server holds E%02d alone and the audit has no row for it", f.marker, f.first)
		case unread[name(f.marker)] && !strings.Contains(problems, fmt.Sprintf("does not read %q", f.marker)):
			t.Errorf("%s: the audit says %q, want it to say the server does not read the name as a run", f.marker, problems)
		case !unread[name(f.marker)] && found:
			t.Errorf("%s: read as a run, and the audit still says %q", f.marker, problems)
		}
	}
}
