package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/naming"
	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/katbyte/embyfin-mcp/sdk/tmdb"
)

// ids names the members of each group, for a test to compare.
func groupIDs(groups [][]embyfin.Item) [][]string {
	out := make([][]string, 0, len(groups))
	for _, g := range groups {
		var ids []string
		for i := range g {
			it := &g[i]
			ids = append(ids, it.ID)
		}
		out = append(out, ids)
	}

	return out
}

// Entries whose AniDB ids differ are two works, whatever id they share: a
// special held as its own series carries its parent's TMDB or TVDB id, and
// grouped with the parent unmarked it read as a second copy of it, which
// invites deleting a series of other episodes. Kept apart silently instead,
// a copy of the show beside the special was never listed, and an entry with
// no AniDB id joined one side or the other by the order the ids came in. So
// they are one group, every member saying which AniDB id it is and that only
// entries of one can be copies; a placeholder AniDB id splits nothing, and a
// placeholder provider id joins nothing.
func TestAniDBIdsSayWhichEntriesAreWorksApart(t *testing.T) {
	t.Parallel()

	series := func(id string, ids map[string]string) embyfin.Item {
		return embyfin.Item{ID: id, Name: "Dragon Ball Z", Type: "Series", ProviderIDs: ids}
	}
	trunks := series("trunks", map[string]string{"Tmdb": "12971", "AniDB": "1474"})
	dbz := series("dbz", map[string]string{"Tmdb": "12971", "Tvdb": "81472", "AniDB": "1530"})
	dbzAgain := series("dbz2", map[string]string{"Tmdb": "12971", "AniDB": "1530"})
	unmatched := series("plain", map[string]string{"Tvdb": "81472"})
	placeholder := series("zero", map[string]string{"Tmdb": "12971", "AniDB": "0"})

	for _, tc := range []struct {
		name     string
		items    []embyfin.Item
		want     []string
		warnings []string // a prefix of each member's warning, "" for none
	}{
		{"a special beside its show", []embyfin.Item{dbz, trunks}, []string{"dbz", "trunks"}, []string{
			"AniDB ids differ: this entry is AniDB 1530, and the group holds AniDB 1474 too",
			"AniDB ids differ: this entry is AniDB 1474, and the group holds AniDB 1530 too",
		}},
		{"two copies of the show beside the special", []embyfin.Item{trunks, dbz, dbzAgain}, []string{"trunks", "dbz", "dbz2"}, []string{
			"AniDB ids differ: this entry is AniDB 1474", "AniDB ids differ: this entry is AniDB 1530", "AniDB ids differ: this entry is AniDB 1530",
		}},
		{"a copy with no AniDB id", []embyfin.Item{trunks, dbz, unmatched}, []string{"trunks", "dbz", "plain"}, []string{
			"AniDB ids differ", "AniDB ids differ", "carries no AniDB id, in a group holding AniDB 1474 and 1530",
		}},
		{"a placeholder AniDB id", []embyfin.Item{dbz, placeholder}, []string{"dbz", "zero"}, []string{"", ""}},
		{"one AniDB id", []embyfin.Item{dbz, dbzAgain}, []string{"dbz", "dbz2"}, []string{"", ""}},
	} {
		for range 10 { // map order must not matter
			groups := groupByProviderID(tc.items)
			if got := groupIDs(groups); len(got) != 1 || !slices.Equal(got[0], tc.want) {
				t.Errorf("%s: groups = %v, want %v", tc.name, got, tc.want)

				break
			}
			warnings := duplicateWarnings(t.Context(), nil, groups[0])
			for i, want := range tc.warnings {
				if (want == "") != (warnings[i] == "") || !strings.HasPrefix(warnings[i], want) {
					t.Errorf("%s: %s's warning = %q, want %q", tc.name, groups[0][i].ID, warnings[i], want)
				}
			}
		}
	}

	// one of two entries of one AniDB id matched wrong - its folder names
	// another show, dated years off - is said beside the split, which alone
	// reads as though the two may be copies
	right := series("right", map[string]string{"Tmdb": "12971", "AniDB": "1530"})
	right.ProductionYear, right.Path = 1989, "/tv/Dragon Ball Z (1989)"
	wrong := series("wrong", map[string]string{"Tmdb": "12971", "AniDB": "1530"})
	wrong.ProductionYear, wrong.Path = 1989, "/tv/Zzyzx Show (2004)"
	groups := groupByProviderID([]embyfin.Item{right, wrong, trunks})
	if len(groups) != 1 {
		t.Fatalf("groups = %v, want one", groupIDs(groups))
	}
	among := `. Among the entries of AniDB 1530: probably not copies of one series: "Zzyzx Show (2004)" is named for "Zzyzx Show" (2004), not Dragon Ball Z (1989)`
	for i, warning := range duplicateWarnings(t.Context(), nil, groups[0]) {
		id := groups[0][i].ID
		if said := strings.Contains(warning, among); said != (id != "trunks") || !strings.HasPrefix(warning, "AniDB ids differ") {
			t.Errorf("%s's warning = %q, want the AniDB split, and what is wrong among AniDB 1530's entries: %v", id, warning, id != "trunks")
		}
	}

	// an entry with no AniDB id, its folder naming another show and year,
	// is held against the entries of each AniDB id, where it said only that
	// it carries none
	plain := series("plain", map[string]string{"Tmdb": "12971"})
	plain.ProductionYear, plain.Path = 1989, "/tv/Zzyzx Show (2004)"
	groups = groupByProviderID([]embyfin.Item{right, trunks, plain})
	if len(groups) != 1 {
		t.Fatalf("groups = %v, want one", groupIDs(groups))
	}
	for i, warning := range duplicateWarnings(t.Context(), nil, groups[0]) {
		if groups[0][i].ID != "plain" {
			continue
		}
		for _, id := range []string{"1474", "1530"} {
			if want := ". Against the entries of AniDB " + id + `: probably not copies of one series: "Zzyzx Show (2004)" is named for "Zzyzx Show" (2004)`; !strings.Contains(warning, want) || !strings.HasPrefix(warning, "carries no AniDB id") {
				t.Errorf("the entry with no AniDB id warns %q, want %q", warning, want)
			}
		}
	}

	// a placeholder a template leaves in an nfo names no title
	a := embyfin.Item{ID: "a", Name: "Alien", Type: typeMovie, ProviderIDs: map[string]string{"Tmdb": "0", "Imdb": "tt0000000"}}
	b := embyfin.Item{ID: "b", Name: "Arrival", Type: typeMovie, ProviderIDs: map[string]string{"Tmdb": "0", "Imdb": "tt0000000"}}
	if got := groupByProviderID([]embyfin.Item{a, b}); len(got) != 0 {
		t.Errorf("two films sharing placeholder ids = %v", groupIDs(got))
	}
}

// A series' folder named by a title TMDB lists for the series - an anime's
// romanised title beside its English one - is the series: two copies of one
// show, one folder romanised, were said to be one matched wrong. Without a
// token the folder is none of the titles the entries go by, and still said.
func TestSeriesFoldersReadByTMDBsTitles(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/tv/91001/alternative_titles":
			writeJSON(t, w, map[string]any{"id": 91001, "results": []map[string]any{{"iso_3166_1": "JP", "title": "Kyojin no Zzyzx", "type": "romaji"}}})
		case "/3/search/tv":
			writeJSON(t, w, map[string]any{"results": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	withTMDB := &titleCheck{titles: newProviderTitles(Options{TMDBKey: "k", ProviderTransport: rewrite{target}})}
	copies := []embyfin.Item{
		{Type: "Series", Name: "Zzyzx Titans", ProductionYear: 2013, Path: "/tv/Zzyzx Titans (2013)", ProviderIDs: map[string]string{"Tmdb": "91001"}},
		{Type: "Series", Name: "Zzyzx Titans", ProductionYear: 2013, Path: "/tv/Kyojin no Zzyzx (2013)", ProviderIDs: map[string]string{"Tmdb": "91001"}},
	}
	if w := duplicateWarning(t.Context(), withTMDB, copies); w != "" {
		t.Errorf("a romanised folder TMDB lists = %q", w)
	}
	// without one it is only a title the entry does not go by, which may be
	// a translation: "may", saying TMDB was not asked
	if w := duplicateWarning(t.Context(), nil, copies); !strings.HasPrefix(w, "may not be copies of one series") || !strings.Contains(w, "TMDB was not asked") {
		t.Errorf("without a token = %q", w)
	}
}

// A group sharing an id whose members are dated years apart, or a series
// whose folder names another year, is probably a remake or a reboot matched
// to the original's id rather than copies: the warning says so of series as
// well as films. A show split across two folders by a rename is copies, and
// a group of episodes has no year to hold against another.
func TestDuplicateWarningSeriesAndYears(t *testing.T) {
	t.Parallel()

	reboot := []embyfin.Item{
		{Type: "Series", Name: "Zzyzx Show", ProductionYear: 2004, Path: "/tv/Zzyzx Show (1978)"},
		{Type: "Series", Name: "Zzyzx Show", ProductionYear: 2004, Path: "/tv/Zzyzx Show (2004)"},
	}
	if w := duplicateWarning(t.Context(), nil, reboot); !strings.HasPrefix(w, "probably not copies of one series") || !strings.Contains(w, `"Zzyzx Show (1978)"`) {
		t.Errorf("a reboot on the original's id = %q", w)
	}

	split := []embyfin.Item{
		{Type: "Series", Name: "The Wire", ProductionYear: 2002, Path: "/tv/The Wire"},
		{Type: "Series", Name: "The Wire", ProductionYear: 2002, Path: "/tv/The Wire (2002)"},
	}
	if w := duplicateWarning(t.Context(), nil, split); w != "" {
		t.Errorf("one show split by a folder rename = %q", w)
	}

	// films whose paths name no year, dated apart by the entries themselves
	dated := []embyfin.Item{
		{Type: typeMovie, Name: "Alien", ProductionYear: 1979, Path: "/m/a/alien.mkv"},
		{Type: typeMovie, Name: "Alien", ProductionYear: 1986, Path: "/m/b/alien.mkv"},
	}
	if w := duplicateWarning(t.Context(), nil, dated); !strings.HasPrefix(w, "probably not copies of one film: the entries are dated 1979 and 1986") {
		t.Errorf("films dated seven years apart = %q", w)
	}
	// a year either side of new year is one film
	if w := duplicateWarning(t.Context(), nil, []embyfin.Item{dated[0], {Type: typeMovie, Name: "Alien", ProductionYear: 1980, Path: "/m/c/alien.mkv"}}); w != "" {
		t.Errorf("films a year apart = %q", w)
	}

	episodes := []embyfin.Item{
		{Type: typeEpisode, Name: "Pilot", ProductionYear: 2008, Path: "/tv/Show/Season 01/Show S01E01.mkv"},
		{Type: typeEpisode, Name: "Pilot", ProductionYear: 2011, Path: "/tv/Show (2008)/Season 01/Show S01E01.mkv"},
	}
	if w := duplicateWarning(t.Context(), nil, episodes); w != "" {
		t.Errorf("one episode aired in two years = %q", w)
	}
}

// Entries sharing an id whose runtimes are far apart are not copies, though
// every path names the one film: two films sharing an IMDb id ran 4 minutes
// and 64 and were grouped as copies, which is a wrong id on one of them. More
// than twice the length is no cut of one film; more than 15% apart - once a
// PAL copy's 4% is allowed for - is two cuts at most, or a wrong id. Runtimes
// closer, a PAL copy's included, say nothing, and a series has no runtime of
// its own to hold against another.
func TestDuplicateWarningRuntimes(t *testing.T) {
	t.Parallel()

	film := func(minutes float64) embyfin.Item {
		return embyfin.Item{Type: typeMovie, Name: "Alien", ProductionYear: 1979, Path: "/m/Alien (1979)/Alien (1979).mkv", RunTimeTicks: int64(minutes * ticksPerMinute)}
	}
	for _, tc := range []struct {
		runtimes []float64
		want     string
	}{
		{[]float64{4, 64}, "probably not copies of one film: the entries run 4 min and 64 min, more than twice as long, which no two cuts of one film are: one file is cut short or a sample, or one of the ids is wrong - compare the files before keeping either"},
		{[]float64{64, 4, 63}, "probably not copies of one film: the entries run 4 min and 64 min, more than twice as long, which no two cuts of one film are: one file is cut short or a sample, or one of the ids is wrong - compare the files before keeping either"},
		{[]float64{100, 125}, "not copies of one cut: the entries run 100 min and 125 min, more than 15% apart - two cuts of one film (a theatrical and an extended one), a file cut short, or a wrong id on one: compare them before keeping one over the other"},
		{[]float64{100, 100 / palSpeedup}, ""},
		{[]float64{100, 118}, ""},
		{[]float64{117, 116}, ""},
		{[]float64{0, 64}, ""},
	} {
		var group []embyfin.Item
		for _, m := range tc.runtimes {
			group = append(group, film(m))
		}
		if got := duplicateWarning(t.Context(), nil, group); got != tc.want {
			t.Errorf("films of %v minutes = %q, want %q", tc.runtimes, got, tc.want)
		}
	}
	// a file naming a title the entry does not go by, more than twice as
	// long or as short: the lengths say it, and "probably"
	cut := []embyfin.Item{film(117), {Type: typeMovie, Name: "Alien", ProductionYear: 1979, Path: "/m/Zzyzx Qux (1979)/Zzyzx Qux (1979).mkv", RunTimeTicks: 4 * ticksPerMinute}}
	if got := duplicateWarning(t.Context(), nil, cut); !strings.HasPrefix(got, "probably not copies of one film: the entries run 4 min and 117 min, more than twice as long, which no two cuts of one film are: one file is cut short or a sample") ||
		!strings.Contains(got, `The paths say more: "Zzyzx Qux (1979).mkv" is named for "Zzyzx Qux" (1979), not Alien (1979)`) {
		t.Errorf("a 4-minute file naming another title beside a 117-minute one = %q", got)
	}
	// episodes too, in their own words: a file holding two episodes shares
	// the first one's id, and runs twice as long
	eps := []embyfin.Item{{Type: typeEpisode, Name: "Pilot", RunTimeTicks: 22 * ticksPerMinute}, {Type: typeEpisode, Name: "Pilot", RunTimeTicks: 3 * ticksPerMinute}}
	if got := duplicateWarning(t.Context(), nil, eps); got != "probably not copies of one episode: the entries run 3 min and 22 min, more than twice as long: one file is cut short or a sample, or holds more than one episode, or one of the ids is wrong - compare the files before keeping either" {
		t.Errorf("one episode's id on 22 minutes and 3 = %q", got)
	}
	double := []embyfin.Item{
		{Type: typeEpisode, Name: "Pilot", IndexNumber: new(1), Path: "/tv/Zzyzx/Season 01/Zzyzx S01E01.mkv", RunTimeTicks: 22 * ticksPerMinute},
		{Type: typeEpisode, Name: "Pilot", IndexNumber: new(1), IndexNumberEnd: 2, Path: "/tv/Zzyzx/Season 01/Zzyzx S01E01E02.mkv", RunTimeTicks: 44 * ticksPerMinute},
	}
	if got := duplicateWarning(t.Context(), nil, double); !strings.Contains(got, "Zzyzx S01E01E02.mkv is held as episodes 1 to 2, and may hold them all") {
		t.Errorf("a file holding two episodes beside the first = %q", got)
	}
	extended := []embyfin.Item{{Type: typeEpisode, Name: "Pilot", RunTimeTicks: 44 * ticksPerMinute}, {Type: typeEpisode, Name: "Pilot", RunTimeTicks: 58 * ticksPerMinute}}
	if got := duplicateWarning(t.Context(), nil, extended); !strings.HasPrefix(got, "not copies of one cut: the entries run 44 min and 58 min, more than 15% apart - an extended cut, a file cut short or holding more") {
		t.Errorf("an extended episode = %q", got)
	}
	// a film and a series sharing an IMDb id, which names one title
	mixed := []embyfin.Item{film(113), {Type: "Series", Name: "Breaking Bad", Path: "/tv/Breaking Bad", RunTimeTicks: 47 * ticksPerMinute}}
	if got := duplicateWarning(t.Context(), nil, mixed); got != "probably not copies: a film and a series share one IMDb id, which names one title, so one of the ids is wrong: identify the wrong one (item_identify) rather than keep either" {
		t.Errorf("a film and a series on one IMDb id = %q", got)
	}
	// a series' runtime is its episodes', and says nothing of which show
	shows := []embyfin.Item{{Type: "Series", Name: "Zzyzx", Path: "/tv/Zzyzx", RunTimeTicks: 22 * ticksPerMinute}, {Type: "Series", Name: "Zzyzx", Path: "/tv/Zzyzx", RunTimeTicks: 60 * ticksPerMinute}}
	if got := duplicateWarning(t.Context(), nil, shows); got != "" {
		t.Errorf("two series of different episode lengths = %q", got)
	}
}

// audit_multiple_versions and audit_duplicates hold a file's title against
// the item's with its numbers in it: a film's second part, or its sequel,
// merged in as a version or matched to its id is warned of. Without a token
// it is a title the film does not go by, which may be one it goes by that
// no list holds: "may".
func TestVersionWarningKeepsNumbersApart(t *testing.T) {
	t.Parallel()

	// every file dated the film's year, so the number is all that differs
	for _, tc := range []struct{ name, other string }{
		{"Dune: Part One", "Dune Part Two (2021)"},
		{"Dune", "Dune 2 (2021)"},
		{"Blade Runner", "Blade Runner 2049 (2021)"},
	} {
		own := "/m/" + tc.name + " (2021)/" + tc.name + " (2021).mkv"
		it := &embyfin.Item{
			Type: typeMovie, Name: tc.name, ProductionYear: 2021, Path: own,
			MediaSources: []embyfin.MediaSource{{Path: own}, {Path: "/m/" + tc.other + "/" + tc.other + ".mkv"}},
		}
		if w := versionWarning(t.Context(), nil, it); !strings.HasPrefix(w, "may not be one film") || !strings.Contains(w, tc.other) {
			t.Errorf("%q with a version named %q = %q", tc.name, tc.other, w)
		}
	}
}

// The version warning reads a file as audit_file_path reads a path. A file
// named for a title TMDB lists for the film - its title in another country -
// or one TMDB's search finds the film itself by is the film, and so is a
// file named "Franchise (Year) - Title", whose title follows the year; each
// was warned of as another film merged in. Without a token those TMDB knows
// are warned of still, as audit_file_path reports them; a sequel merged in
// is warned of either way; and a TMDB that cannot answer leaves the warning
// standing and says why.
func TestVersionWarningReadsTitlesAsTheFilePathAuditDoes(t *testing.T) {
	t.Parallel()

	withTMDB := &titleCheck{titles: newProviderTitles(Options{TMDBKey: "k", ProviderTransport: titlesTMDB(t)})}
	film := func(name string, year int, tmdbID string, versions ...string) *embyfin.Item {
		it := &embyfin.Item{Type: typeMovie, Name: name, ProductionYear: year, ProviderIDs: map[string]string{"Tmdb": tmdbID}}
		for _, v := range versions {
			it.MediaSources = append(it.MediaSources, embyfin.MediaSource{Path: v})
		}
		it.Path = versions[0]
		return it
	}
	mononoke := film("Princess Mononoke", 1997, "128", "/m/Princess Mononoke (1997)/Princess Mononoke (1997).mkv", "/m/Mononoke-hime (1997)/Mononoke-hime (1997).mkv")
	arrival := film("Arrival", 2016, "329865", "/m/Arrival (2016)/Arrival (2016).mkv", "/m/La llegada (2016)/La llegada (2016).mkv")
	franchise := film("Alien", 1979, "348", "/m/Alien (1979)/Alien (1979).mkv", "/m/Alien Collection/Alien Collection (1979) - Alien.avi")
	sequel := film("Alien", 1979, "348", "/m/Alien (1979)/Alien (1979).mkv", "/m/Aliens (1986)/Aliens (1986).mkv")

	for _, tc := range []struct {
		name  string
		it    *embyfin.Item
		check *titleCheck
		warns bool
	}{
		{"a title TMDB lists for the film", mononoke, withTMDB, false},
		{"a title TMDB lists, with no token", mononoke, nil, true},
		// TMDB's search answering with the film, but under no title like the
		// file's, settles nothing: it matches loosely
		{"a title TMDB's search answers the film for", arrival, withTMDB, true},
		{"a title TMDB's search answers, with no token", arrival, nil, true},
		{"the title after the year", franchise, nil, false},
		{"a sequel merged in", sequel, withTMDB, true},
		{"a sequel merged in, with no token", sequel, nil, true},
	} {
		if w := versionWarning(t.Context(), tc.check, tc.it); (w != "") != tc.warns {
			t.Errorf("%s: warning %q, want one: %v", tc.name, w, tc.warns)
		}
	}

	// the file path audit reads the same file the same way
	if row, _ := checkPath(&embyfin.Item{ID: "x", Type: typeMovie, Name: "Alien", ProductionYear: 1979, Path: "/m/Alien Collection/Alien Collection (1979) - Alien.avi"}, map[string]bool{"title": true, "year": true}, embyfin.Emby); len(row.Problems) != 0 {
		t.Errorf("audit_file_path on the franchise's file = %v", row.Problems)
	}

	// TMDB not answering: the warning stands, and says so
	down := &titleCheck{titles: newProviderTitles(Options{TMDBKey: "k", ProviderTransport: failingTransport{}})}
	if w := versionWarning(t.Context(), down, mononoke); !strings.HasPrefix(w, "may not be one film") || !strings.Contains(w, "TMDB could not be asked") {
		t.Errorf("with TMDB down = %q", w)
	}
}

// franchiseTMDB is a canned TMDB for a title shaped "Franchise (Year)
// Subtitle": searched by the franchise word alone, it answers with the film
// the item is matched to among others; by the whole title, with the film the
// file is. Nothing in it is a real film.
func franchiseTMDB(t *testing.T) http.RoundTripper {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p, q := r.URL.Path, r.URL.Query(); {
		case strings.HasSuffix(p, "/alternative_titles"):
			writeJSON(t, w, map[string]any{"titles": []any{}})
		case p == "/3/search/movie" && q.Get("query") == "Zzyzx" && q.Get("year") == "2016":
			writeJSON(t, w, map[string]any{"results": []map[string]any{
				{"id": 90001, "title": "Zzyzx v Quux: Dawn", "original_title": "Zzyzx v Quux: Dawn", "release_date": "2016-03-23"},
				{"id": 90003, "title": "Zzyzx", "original_title": "Zzyzx", "release_date": "2016-01-01"},
			}})
		case p == "/3/search/movie" && q.Get("query") == "Zzyzx Unlimited - Mechs" && q.Get("year") == "2016":
			writeJSON(t, w, map[string]any{"results": []map[string]any{
				{"id": 90002, "title": "Zzyzx Unlimited: Mechs", "original_title": "Zzyzx Unlimited: Mechs", "release_date": "2016-05-10"},
			}})
		case p == "/3/search/movie":
			writeJSON(t, w, map[string]any{"results": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	return rewrite{target}
}

// A file named "Franchise (Year) Subtitle" is read by its whole title. Read
// by the franchise word alone, TMDB's search for it answered with the film
// the item is matched to - one of a dozen films the word begins - and the
// file of another film merged in was cleared as a copy: in the version
// warning, in item_get and audit_duplicates, and in audit_file_path. Asked by
// the whole title, TMDB names the film the file is, and the warning stands
// with a token as without one; the file of the film itself, read whole, is
// its own title and no warning at all.
func TestAFranchiseTitleIsReadWhole(t *testing.T) {
	t.Parallel()

	if got := naming.WholeTitle("Zzyzx (2016) Unlimited - Mechs.mkv"); got != "Zzyzx Unlimited - Mechs" {
		t.Errorf("the whole title = %q", got)
	}
	for _, name := range []string{"Zzyzx (2016).mkv", "Zzyzx (2016) - 1080p.mkv", "Zzyzx (2016) 360p.mkv", "Zzyzx (2016) [Bluray-1080p].mkv", "Zzyzx (2016) {imdb-tt0000001}.mkv", "Zzyzx 2016.mkv"} {
		if got := naming.WholeTitle(name); got != "" {
			t.Errorf("wholeTitle(%q) = %q, want nothing past the year to read", name, got)
		}
	}

	withTMDB := &titleCheck{titles: newProviderTitles(Options{TMDBKey: "k", ProviderTransport: franchiseTMDB(t)})}
	versions := func(name string, id string) *embyfin.Item {
		own := "/zz/films/" + name + " (2016)/" + name + " (2016).mkv"
		return &embyfin.Item{
			Type: typeMovie, Name: name, ProductionYear: 2016, ProviderIDs: map[string]string{"Tmdb": id}, Path: own,
			MediaSources: []embyfin.MediaSource{{Path: own}, {Path: "/zz/films/Zzyzx (2016) Unlimited - Mechs/Zzyzx (2016) Unlimited - Mechs.mkv"}},
		}
	}
	// quoted as TMDB is asked by it, whole; "probably" when TMDB gives the
	// title to another film, "may" without a token to ask
	for check, want := range map[*titleCheck]string{withTMDB: "probably not one film", nil: "may not be one film"} {
		if w := versionWarning(t.Context(), check, versions("Zzyzx v Quux: Dawn", "90001")); !strings.HasPrefix(w, want) || !strings.Contains(w, `named for "Zzyzx Unlimited - Mechs" (2016)`) {
			t.Errorf("token %v: another film merged in = %q", check != nil, w)
		}
		if w := versionWarning(t.Context(), check, versions("Zzyzx Unlimited: Mechs", "90002")); w != "" {
			t.Errorf("token %v: the film's own file = %q", check != nil, w)
		}
	}

	// audit_file_path reads it the same way
	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(map[string]any{
			"Id": "b", "Name": "Zzyzx v Quux: Dawn", "Type": "Movie", "ProductionYear": 2016, "ProviderIds": map[string]any{"Tmdb": "90001"},
			"Path": "/zz/films/Zzyzx (2016) Unlimited - Mechs/Zzyzx (2016) Unlimited - Mechs.mkv", "RunTimeTicks": 78 * ticksPerMinute,
		}))
	})
	for _, opts := range []Options{{TMDBKey: "k", ProviderTransport: franchiseTMDB(t)}, {}} {
		rows := objects(t, mustCall(t, session(t, f, opts), "audit_file_path", map[string]any{"library": "Zzyzx Films"})["findings"], "findings")
		if len(rows) != 1 || !strings.HasPrefix(texts(rows[0]["problems"])[0], `title: the path is named "Zzyzx Unlimited - Mechs", `) || text(rows[0]["title_in_file"]) != "Zzyzx Unlimited - Mechs" {
			t.Fatalf("token %v: findings = %v", opts.TMDBKey != "", rows)
		}
		if opts.TMDBKey != "" && text(rows[0]["path_tmdb"]) != "90002 Zzyzx Unlimited: Mechs (2016)" {
			t.Errorf("with a token the row = %v, want the film TMDB finds by the whole title", rows[0])
		}
	}
}

// failingTransport is a provider that cannot be reached.
type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

// Folders whose names differ by a number are two shows: a year, a sequel's
// numeral. Only the spelling around the numbers is folded.
func TestAuditDuplicateSeriesKeepsNumbersApart(t *testing.T) {
	t.Parallel()

	shows := []*fakeSeries{
		{id: "a", name: "Zzyzx Show", path: "/tv/Zzyzx Show (1978)"},
		{id: "b", name: "Zzyzx Show", path: "/tv/Zzyzx Show (2004)"},
		{id: "c", name: "Zzyzx Show", path: "/tv/Zzyzx Show 2"},
		{id: "d", name: "Zzyzx Show", path: "/tv/Zzyzx Show"},
		{id: "e", name: "Zzyzx Show", path: "/tv/Zzyzx Show Part 1"},
		{id: "f", name: "Zzyzx Show", path: "/tv/Zzyzx Show Part 2"},
	}
	f := tvServer(t, shows...)
	adminView(t, f)
	out := mustCall(t, session(t, f, Options{}), "audit_duplicates", map[string]any{})
	if n := number(t, out["total_findings"], "total_findings"); n != 0 || len(objects(t, out["folder_groups"], "folder_groups")) != 0 {
		t.Errorf("shows a number apart grouped: %v", out["folder_groups"])
	}
}

// editionTMDB is a canned TMDB for files named "Title (Year) Words", the
// words after the year an edition's label or the rest of another film's
// title. Alien and Princess Mononoke answer as TMDB does; films 90020
// (Zzyzx, 2021), 90021 (Zzyzx: Part Two, 2024), 90030 (Zzyzx, 1982), 90040
// (Zzyzx, 2016, titled La Zzyzx in its Spanish translation), 90050 (Zzyzx,
// 1979, in a collection with 90051, Quux Returns, whose runtime TMDB fails to
// answer, and 90052, Zzyzx Resurrection), 90060 (Zzyzx, 2016) beside 90061
// (Zzyzx: Ultimate Edition), and
// 90070 (Quux, listed as "Zzyzx 2" too), 90053 (in 90050's collection),
// 90092 (Zzyzx, whose details TMDB fails to answer) beside 90093 (Zzyzx Quux
// Rising) are none real. asked counts the asks made: a search by its query
// and year, anything else by its path.
func editionTMDB(t *testing.T) (transport http.RoundTripper, asked func(query string) int) {
	t.Helper()

	var mu sync.Mutex
	searches := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit := func(id int, title, date string) map[string]any {
			return map[string]any{"id": id, "title": title, "original_title": title, "release_date": date}
		}
		key := r.URL.Path
		if key == "/3/search/movie" {
			key = r.URL.Query().Get("query") + "|" + r.URL.Query().Get("year")
		}
		mu.Lock()
		searches[key]++
		mu.Unlock()
		switch p := r.URL.Path; {
		case p == "/3/movie/128/alternative_titles":
			writeJSON(t, w, map[string]any{"id": 128, "titles": []map[string]any{{"iso_3166_1": "JP", "title": "Mononoke-hime", "type": "romaji"}}})
		case p == "/3/movie/90070/alternative_titles":
			writeJSON(t, w, map[string]any{"id": 90070, "titles": []map[string]any{{"iso_3166_1": "DE", "title": "Zzyzx 2"}}})
		case p == "/3/movie/90050":
			writeJSON(t, w, map[string]any{"id": 90050, "title": "Zzyzx", "runtime": 117, "belongs_to_collection": map[string]any{"id": 95000, "name": "Zzyzx Collection"}})
		case p == "/3/collection/95000":
			writeJSON(t, w, map[string]any{"id": 95000, "name": "Zzyzx Collection", "parts": []map[string]any{
				hit(90050, "Zzyzx", "1979-05-25"), hit(90051, "Quux Returns", "1986-07-18"), hit(90052, "Zzyzx Resurrection", "1997-11-12"),
			}})
		case p == "/3/movie/90053":
			writeJSON(t, w, map[string]any{"id": 90053, "title": "Zzyzx Again", "belongs_to_collection": map[string]any{"id": 95000, "name": "Zzyzx Collection"}})
		case p == "/3/movie/90051", p == "/3/movie/90092":
			http.Error(w, "down", http.StatusInternalServerError)
		case strings.HasSuffix(p, "/alternative_titles"):
			writeJSON(t, w, map[string]any{"titles": []any{}})
		case p == "/3/movie/90040/translations":
			writeJSON(t, w, map[string]any{"id": 90040, "translations": []map[string]any{
				{"iso_639_1": "es", "iso_3166_1": "ES", "english_name": "Spanish", "data": map[string]any{"title": "La Zzyzx"}},
				{"iso_639_1": "fr", "iso_3166_1": "FR", "english_name": "French", "data": map[string]any{"title": ""}},
			}})
		case strings.HasSuffix(p, "/translations"):
			writeJSON(t, w, map[string]any{"translations": []any{}})
		case p == "/3/search/movie":
			var results []map[string]any
			switch key {
			case "Alien|1979":
				results = append(results, hit(348, "Alien", "1979-05-25"))
			case "Princess Mononoke|1997", "もののけ姫|1997":
				results = append(results, map[string]any{"id": 128, "title": "Princess Mononoke", "original_title": "もののけ姫", "release_date": "1997-07-12"})
			case "Zzyzx Part Two|":
				results = append(results, hit(90021, "Zzyzx: Part Two", "2024-02-27"), hit(90020, "Zzyzx", "2021-09-15"))
			case "Zzyzx Final Cut|":
				results = append(results, hit(90030, "Zzyzx", "1982-06-25"))
			case "Zzyzx|1990":
				results = append(results, hit(90081, "Zzyzx", "1990-04-20"))
			case "Zzyzx Quux Rising|":
				results = append(results, hit(90093, "Zzyzx Quux Rising", "2014-06-06"))
			case "Zzyzx in the Air|":
				results = append(results, hit(90091, "Zzyzx in the Air", "2009-12-04"))
			case "Zzyzx Resurrection|":
				results = append(results, hit(90052, "Zzyzx Resurrection", "1997-11-12"))
			case "Zzyzx Ultimate Edition|", "Zzyzx Ultimate Edition|2016":
				results = append(results, hit(90061, "Zzyzx: Ultimate Edition", "2016-06-28"), hit(90060, "Zzyzx", "2016-03-23"))
			case "La Zzyzx|2016", "Quux Zzyzx|2016":
				// the film itself, under its own title
				results = append(results, hit(90040, "Zzyzx", "2016-11-10"))
			}
			writeJSON(t, w, map[string]any{"results": results})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	return rewrite{target}, func(query string) int {
		mu.Lock()
		defer mu.Unlock()

		return searches[query]
	}
}

// filePathRows is audit_file_path's findings over one library of films.
func filePathRows(t *testing.T, opts Options, films ...map[string]any) []map[string]any {
	t.Helper()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(films...))
	})

	return objects(t, mustCall(t, session(t, f, opts), "audit_file_path", map[string]any{"library": "Zzyzx Films"})["findings"], "findings")
}

// versionsOfFilm is a film held in the files given, the first its own.
func versionsOfFilm(name string, year int, tmdbID string, paths ...string) *embyfin.Item {
	it := &embyfin.Item{Type: typeMovie, Name: name, ProductionYear: year, ProviderIDs: map[string]string{"Tmdb": tmdbID}, Path: paths[0]}
	for _, p := range paths {
		it.MediaSources = append(it.MediaSources, embyfin.MediaSource{Path: p})
	}

	return it
}

// Words after a file's year that follow one of the film's own titles are an
// edition's, and the path is searched by the title before the year. Read as
// a franchise's subtitle, "Alien (1979) Directors Cut" held as Alien dated
// 1986 was searched as "Alien Directors Cut", found nothing, and was said to
// be named by hand - where the search by "Alien" finds this very film, and
// the year the item holds is the one to check. A translated copy with an
// edition word ("Mononoke-hime (1997) Remastered", "- Criterion"), its title
// one TMDB lists for the film, was a finding the same way: the whole title
// is asked first when the words before the year are none the film goes by,
// and the words before the year when that finds nothing.
func TestAnEditionAfterTheYearIsNotTheTitle(t *testing.T) {
	t.Parallel()

	film := func(id, name, original string, year int, tmdbID, file string) map[string]any {
		return map[string]any{
			"Id": id, "Name": name, "OriginalTitle": original, "Type": "Movie", "ProductionYear": year, "ProviderIds": map[string]any{"Tmdb": tmdbID},
			"Path": "/zz/films/" + file + "/" + file + ".mkv", "RunTimeTicks": 117 * ticksPerMinute,
		}
	}
	films := []map[string]any{
		film("alien", "Alien", "", 1986, "348", "Alien (1979) Directors Cut"),
		film("remastered", "Princess Mononoke", "もののけ姫", 1997, "128", "Mononoke-hime (1997) Remastered"),
		film("criterion", "Princess Mononoke", "もののけ姫", 1997, "128", "Mononoke-hime (1997) - Criterion"),
	}
	transport, _ := editionTMDB(t)
	rows := filePathRows(t, Options{TMDBKey: "k", ProviderTransport: transport}, films...)
	if len(rows) != 1 || text(rows[0]["id"]) != "alien" {
		t.Fatalf("with a token, findings = %v, want Alien's year alone", rows)
	}
	if problems := texts(rows[0]["problems"]); len(problems) != 1 || !strings.HasPrefix(problems[0], "year:") {
		t.Errorf("Alien's problems = %v, want its year alone", problems)
	}
	if want := "TMDB's search finds this very film, TMDB 348, by the path's title and year: the path names it, and the year the item holds is the one to check"; text(rows[0]["diagnosis"]) != want {
		t.Errorf("Alien's diagnosis = %q, want %q", rows[0]["diagnosis"], want)
	}

	// without a token the translated copies are titles the film does not go
	// by, quoted whole: which words are an edition's is not known
	rows = filePathRows(t, Options{}, films...)
	named := map[string]string{}
	for _, r := range rows {
		named[text(r["id"])] = text(r["title_in_file"])
	}
	if want := map[string]string{"alien": "", "remastered": "Mononoke-hime Remastered", "criterion": "Mononoke-hime Criterion"}; !maps.Equal(named, want) {
		t.Errorf("without a token, titles read = %v, want %v", named, want)
	}

	// the version warning reads them alike
	check := &titleCheck{titles: newProviderTitles(Options{TMDBKey: "k", ProviderTransport: transport})}
	for _, it := range []*embyfin.Item{
		versionsOfFilm("Alien", 1979, "348", "/m/Alien (1979)/Alien (1979).mkv", "/m/Alien (1979) Directors Cut/Alien (1979) Directors Cut.mkv"),
		versionsOfFilm("Princess Mononoke", 1997, "128", "/m/Princess Mononoke (1997)/Princess Mononoke (1997).mkv", "/m/Mononoke-hime (1997) Remastered/Mononoke-hime (1997) Remastered.mkv"),
	} {
		if w := versionWarning(t.Context(), check, it); w != "" {
			t.Errorf("%s with an edition's file = %q", it.Name, w)
		}
	}
}

// A file whose words before its year are the film's, and whose words after
// are more, is asked of TMDB by its whole title in no year - the year filter
// hides "Zzyzx: Part Two" (2024) when asked for 2021 - and by the films of
// the film's own TMDB collection, and is another film only when TMDB names
// one of another id by it. "Zzyzx (2021) Part Two" merged into Zzyzx was
// cleared by its first word; so was "Zzyzx (1979) - Quux Returns", whose
// whole title TMDB's search finds nothing by, though Quux Returns is the next
// film of Zzyzx's collection. An entry of its own whose title begins with the
// film's is probably another film when it carries a number the film's lacks
// ("Zzyzx: Part Two") or sits in the film's collection ("Zzyzx
// Resurrection"). A file whose words after its year are an edition's alone
// ("Zzyzx (2016) Ultimate Edition", "Zzyzx (1982) - Final Cut") is asked
// nothing and stays quiet: the film's own title, year and an edition's words
// can only be the film, or an edition TMDB lists apart. Without a token none can be
// told apart and none is warned; with TMDB down the file is said to be
// unchecked, where it came out clean. Each whole title is searched once.
func TestAFileNamedForAnotherFilmAfterTheYear(t *testing.T) {
	t.Parallel()

	transport, asked := editionTMDB(t)
	check := &titleCheck{titles: newProviderTitles(Options{TMDBKey: "k", ProviderTransport: transport})}
	partTwo := versionsOfFilm("Zzyzx", 2021, "90020", "/zz/Zzyzx (2021)/Zzyzx (2021).mkv", "/zz/Zzyzx (2021) Part Two/Zzyzx (2021) Part Two.mkv")
	collected := versionsOfFilm("Zzyzx", 1979, "90050", "/zz/Zzyzx (1979)/Zzyzx (1979).mkv", "/zz/Zzyzx (1979) - Quux Returns/Zzyzx (1979) - Quux Returns.mkv")
	ultimate := versionsOfFilm("Zzyzx", 2016, "90060", "/zz/Zzyzx (2016)/Zzyzx (2016).mkv", "/zz/Zzyzx (2016) Ultimate Edition/Zzyzx (2016) Ultimate Edition.mkv")
	resurrection := versionsOfFilm("Zzyzx", 1979, "90050", "/zz/Zzyzx (1979)/Zzyzx (1979).mkv", "/zz/Zzyzx (1979) Resurrection/Zzyzx (1979) Resurrection.mkv")
	inTheAir := versionsOfFilm("Zzyzx", 2009, "90090", "/zz/Zzyzx (2009)/Zzyzx (2009).mkv", "/zz/Zzyzx (2009) in the Air/Zzyzx (2009) in the Air.mkv")
	finalCut := versionsOfFilm("Zzyzx", 1982, "90030", "/zz/Zzyzx (1982)/Zzyzx (1982).mkv", "/zz/Zzyzx (1982) - Final Cut/Zzyzx (1982) - Final Cut.mkv")

	for _, tc := range []struct {
		it   *embyfin.Item
		want string
	}{
		{partTwo, `probably not one film: "Zzyzx (2021) Part Two.mkv" names "Zzyzx Part Two": TMDB lists 90021 Zzyzx: Part Two (2024), numbered Two, as a film of its own, not Zzyzx. `},
		{ultimate, ""},
		{collected, `probably not one film: "Zzyzx (1979) - Quux Returns.mkv" names "Quux Returns", TMDB's film 90051 Quux Returns (1986), another film of the series in TMDB's collection of Zzyzx. `},
		{resurrection, `probably not one film: "Zzyzx (1979) Resurrection.mkv" names "Zzyzx Resurrection", TMDB's film 90052 Zzyzx Resurrection (1997), another film of the series in TMDB's collection of Zzyzx. `},
		// its title and words of a title past it: another film, not an edition
		{inTheAir, `probably not one film: "Zzyzx (2009) in the Air.mkv" names "Zzyzx in the Air", TMDB's film 90091 Zzyzx in the Air (2009), not Zzyzx. `},
		{finalCut, ""},
	} {
		for range 2 {
			if w := versionWarning(t.Context(), check, tc.it); !strings.HasPrefix(w, tc.want) || (tc.want == "") != (w == "") {
				t.Errorf("%s = %q, want %q", tc.it.MediaSources[1].Path, w, tc.want)
			}
		}
		if w := versionWarning(t.Context(), nil, tc.it); w != "" {
			t.Errorf("without a token, %s = %q", tc.it.MediaSources[1].Path, w)
		}
	}
	if n := asked("Zzyzx Part Two|"); n != 1 {
		t.Errorf("the whole title was searched %d times, want once", n)
	}

	// TMDB not answering leaves the file unchecked, and says so
	down := &titleCheck{titles: newProviderTitles(Options{TMDBKey: "k", ProviderTransport: failingTransport{}})}
	want := `may not be one film: "Zzyzx (2021) Part Two.mkv" names "Zzyzx Part Two" read whole, and TMDB could not be asked whether that is another film. Compare the files before keeping one over another (TMDB could not be asked: `
	if w := versionWarning(t.Context(), down, partTwo); !strings.HasPrefix(w, want) {
		t.Errorf("with TMDB down = %q, want %q", w, want)
	}

	// audit_duplicates says it of two entries sharing the id
	pair := func(it *embyfin.Item) []embyfin.Item {
		out := make([]embyfin.Item, 0, len(it.MediaSources))
		for _, src := range it.MediaSources {
			out = append(out, embyfin.Item{Type: typeMovie, Name: it.Name, ProductionYear: it.ProductionYear, ProviderIDs: it.ProviderIDs, Path: src.Path})
		}
		return out
	}
	for it, want := range map[*embyfin.Item]string{
		collected: `probably not copies of one film: "Zzyzx (1979) - Quux Returns.mkv" names "Quux Returns", TMDB's film 90051`,
		partTwo:   `probably not copies of one film: "Zzyzx (2021) Part Two.mkv" names "Zzyzx Part Two": TMDB lists 90021 Zzyzx: Part Two (2024), numbered Two`,
	} {
		if w := duplicateWarning(t.Context(), check, pair(it)); !strings.HasPrefix(w, want) {
			t.Errorf("two entries, one %s = %q, want %q", it.MediaSources[1].Path, w, want)
		}
	}
	if w := duplicateWarning(t.Context(), down, pair(partTwo)); !strings.HasPrefix(w, `may not be copies of one film: "Zzyzx (2021) Part Two.mkv" names "Zzyzx Part Two" read whole, and TMDB could not be asked`) {
		t.Errorf("two entries with TMDB down = %q", w)
	}

	// audit_file_path reads the files alike, with the same asking
	film := func(id, file string, year int, tmdbID string) map[string]any {
		return map[string]any{
			"Id": id, "Name": "Zzyzx", "Type": "Movie", "ProductionYear": year, "ProviderIds": map[string]any{"Tmdb": tmdbID},
			"Path": "/zz/films/" + file + "/" + file + ".mkv", "RunTimeTicks": 117 * ticksPerMinute,
		}
	}
	films := []map[string]any{
		film("two", "Zzyzx (2021) Part Two", 2021, "90020"),
		film("collected", "Zzyzx (1979) - Quux Returns", 1979, "90050"),
		film("cut", "Zzyzx (1982) - Final Cut", 1982, "90030"),
	}
	rows := filePathRows(t, Options{TMDBKey: "k", ProviderTransport: transport}, films...)
	byID := map[string]map[string]any{}
	for _, r := range rows {
		byID[text(r["id"])] = r
	}
	if len(rows) != 2 || byID["cut"] != nil {
		t.Fatalf("with a token, findings = %v, want the part two and the collection's film", rows)
	}
	if r := byID["collected"]; texts(r["problems"])[0] != `title: "Zzyzx (1979) - Quux Returns.mkv" names "Quux Returns", TMDB's film 90051 Quux Returns (1986), another film of the series in TMDB's collection of Zzyzx: another film matched to this one's ids` ||
		text(r["path_tmdb"]) != "90051 Quux Returns (1986)" || text(r["item_tmdb"]) != "90050" ||
		!strings.Contains(text(r["diagnosis"]), "TMDB could not be asked how long its 90051 runs") {
		t.Errorf("the collection's film = %v", r)
	}
	if r := byID["two"]; texts(r["problems"])[0] != `title: "Zzyzx (2021) Part Two.mkv" names "Zzyzx Part Two": TMDB lists 90021 Zzyzx: Part Two (2024), numbered Two, as a film of its own, not Zzyzx: another film matched to this one's ids` || text(r["path_tmdb"]) != "90021 Zzyzx: Part Two (2024)" {
		t.Errorf("the part two = %v", r)
	}
	if rows := filePathRows(t, Options{}, films...); len(rows) != 0 {
		t.Errorf("without a token, findings = %v", rows)
	}
}

// The words before a file's year, asked after its whole title, are held to
// the same title, not the prefix rule's likeness: a film matched wrong to
// Quux, which TMDB lists as "Zzyzx 2" too, was cleared by a file named
// "Zzyzx (2019) Remastered", "Zzyzx" and "Zzyzx 2" scoring 0.91.
func TestTheWordsBeforeTheYearAreHeldToTheSameTitle(t *testing.T) {
	t.Parallel()

	transport, _ := editionTMDB(t)
	check := &titleCheck{titles: newProviderTitles(Options{TMDBKey: "k", ProviderTransport: transport})}
	wrong := versionsOfFilm("Quux", 2019, "90070", "/zz/Quux (2019)/Quux (2019).mkv", "/zz/Zzyzx (2019) Remastered/Zzyzx (2019) Remastered.mkv")
	if w := versionWarning(t.Context(), check, wrong); !strings.HasPrefix(w, `may not be one film: "Zzyzx (2019) Remastered.mkv" is named for "Zzyzx Remastered" (2019), not Quux (2019)`) {
		t.Errorf("a film matched wrong to one TMDB lists as Zzyzx 2 = %q", w)
	}
	rows := filePathRows(t, Options{TMDBKey: "k", ProviderTransport: transport}, map[string]any{
		"Id": "wrong", "Name": "Quux", "Type": "Movie", "ProductionYear": 2019, "ProviderIds": map[string]any{"Tmdb": "90070"},
		"Path": wrong.MediaSources[1].Path, "RunTimeTicks": 117 * ticksPerMinute,
	})
	if len(rows) != 1 || !strings.HasPrefix(texts(rows[0]["problems"])[0], `title: the path is named "Zzyzx Remastered"`) {
		t.Errorf("audit_file_path = %v", rows)
	}
}

// A file named by the film's title in one of TMDB's translations is the film
// when TMDB's search finds the film by it: "La Zzyzx" finds Zzyzx, whose
// Spanish translation is titled so, and TMDB lists it among no alternative
// titles. It was warned of as probably another film merged in. A title the
// search finds the film by that is in no translation is still in doubt -
// "may", saying what was asked - and so is any without a token.
func TestATranslatedTitleIsTheFilm(t *testing.T) {
	t.Parallel()

	transport, _ := editionTMDB(t)
	check := &titleCheck{titles: newProviderTitles(Options{TMDBKey: "k", ProviderTransport: transport})}
	translated := versionsOfFilm("Zzyzx", 2016, "90040", "/zz/Zzyzx (2016)/Zzyzx (2016).mkv", "/zz/La Zzyzx (2016)/La Zzyzx (2016).mkv")
	unknown := versionsOfFilm("Zzyzx", 2016, "90040", "/zz/Zzyzx (2016)/Zzyzx (2016).mkv", "/zz/Quux Zzyzx (2016)/Quux Zzyzx (2016).mkv")
	if w := versionWarning(t.Context(), check, translated); w != "" {
		t.Errorf("a title in TMDB's translations = %q", w)
	}
	if w := versionWarning(t.Context(), check, unknown); !strings.HasPrefix(w, "may not be one film") || !strings.Contains(w, "among the titles TMDB lists for it") {
		t.Errorf("a title in no translation = %q", w)
	}
	if w := versionWarning(t.Context(), nil, translated); !strings.HasPrefix(w, "may not be one film") || !strings.Contains(w, "TMDB was not asked") {
		t.Errorf("without a token = %q", w)
	}

	// audit_file_path reads the path alike
	film := func(id, folder string) map[string]any {
		return map[string]any{
			"Id": id, "Name": "Zzyzx", "Type": "Movie", "ProductionYear": 2016, "ProviderIds": map[string]any{"Tmdb": "90040"},
			"Path": "/zz/films/" + folder + " (2016)/" + folder + " (2016).mkv", "RunTimeTicks": 116 * ticksPerMinute,
		}
	}
	rows := filePathRows(t, Options{TMDBKey: "k", ProviderTransport: transport}, film("translated", "La Zzyzx"), film("unknown", "Quux Zzyzx"))
	if len(rows) != 1 || text(rows[0]["id"]) != "unknown" || !strings.Contains(text(rows[0]["diagnosis"]), "answers with this very film, TMDB 90040 Zzyzx (2016), but under no title like the path's") {
		t.Errorf("findings = %v, want the title in no translation alone, found as the film under another title", rows)
	}
}

// recorded is the body of a TMDB answer the acceptance suite recorded, by
// its cassette key.
func recorded(t *testing.T, key string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "acceptance", "testdata", "cassettes", "jellyfin", "api.themoviedb.org.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cassette struct {
		Interactions []struct {
			Key  string `json:"key"`
			Body string `json:"body"`
		} `json:"interactions"`
	}
	if err := json.Unmarshal(raw, &cassette); err != nil {
		t.Fatal(err)
	}
	for _, i := range cassette.Interactions {
		if i.Key == key {
			return i.Body
		}
	}
	t.Fatalf("no recording of %s", key)

	return ""
}

// A collection's parts are films, and TMDB answers each one's title and
// original title; the document declared a series' name and original_name,
// and every part decoded untitled. Decoded from the recorded answer for
// Alien's collection, every part carries the title and year TMDB gave it.
func TestACollectionsPartsDecodeWithTheirTitles(t *testing.T) {
	t.Parallel()

	collection := recorded(t, "GET api.themoviedb.org/3/collection/8091")
	movie := recorded(t, "GET api.themoviedb.org/3/movie/348")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{"/3/movie/348": movie, "/3/collection/8091": collection}[r.URL.Path]
		if body == "" {
			http.NotFound(w, r)

			return
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	titles := newProviderTitles(Options{TMDBKey: "k", ProviderTransport: rewrite{target}})
	parts, err := titles.CollectionParts(t.Context(), "348")
	if err != nil {
		t.Fatal(err)
	}

	var want struct {
		Parts []struct {
			ID            int    `json:"id"`
			Title         string `json:"title"`
			OriginalTitle string `json:"original_title"`
			ReleaseDate   string `json:"release_date"`
		} `json:"parts"`
	}
	if err := json.Unmarshal([]byte(collection), &want); err != nil {
		t.Fatal(err)
	}
	if len(want.Parts) < 2 || len(parts) != len(want.Parts) {
		t.Fatalf("parts = %v, want the %d recorded", parts, len(want.Parts))
	}
	for i, w := range want.Parts {
		year, err := strconv.Atoi(w.ReleaseDate[:4])
		if err != nil {
			t.Fatal(err)
		}
		if got := parts[i]; w.Title == "" || got != (tmdb.Hit{ID: w.ID, Title: w.Title, Original: w.OriginalTitle, Year: year}) {
			t.Errorf("part %d = %+v, want %d %q (%q, %d)", i, got, w.ID, w.Title, w.OriginalTitle, year)
		}
	}
	if !slices.ContainsFunc(parts, func(h tmdb.Hit) bool { return h.ID == 679 && h.Title == "Aliens" }) {
		t.Errorf("parts = %v, want Aliens among them", parts)
	}
}

// A film's title that is the item's and a number alone - "Part Two", "2",
// "II", "Chapter Two", "Vol. 3" - is a numbered film of its own; a number
// among other words ("The Two Towers", "Six Feet Under", "Two: The Return")
// or a year ("2049") is a title's, and was read as the film's number. The
// item's title and an edition's words alone is an edition's; any other words
// ("in the Air" past Up) are another film's.
func TestAnEntrysNumberPastTheFilmsTitle(t *testing.T) {
	t.Parallel()

	zzyzx := &embyfin.Item{Name: "Zzyzx"}
	for title, want := range map[string]string{
		"Zzyzx: Part Two": "Two", "Zzyzx 2": "2", "Zzyzx II": "II", "Zzyzx: Chapter Two": "Two", "Zzyzx Part I": "I", "Zzyzx: Chapter One": "One", "Zzyzx Vol. 3": "3", "Zzyzx Part 13": "13", "Zzyzx 13": "",
		"Zzyzx 2049": "", "Zzyzx: The Two Towers": "", "Zzyzx Six Feet Under": "", "Zzyzx Two: The Return": "", "Zzyzx Ten Commandments": "", "Zzyzx One": "", "Zzyzx X": "",
		"Zzyzx: Ultimate Edition": "", "Zzyzx: Director's Cut": "", "Zzyzx v Quux": "", "Zzyzx X Quux": "", "Quux 2": "",
	} {
		if got := entryNumber(zzyzx, tmdb.Hit{Title: title}); got != want {
			t.Errorf("entryNumber(Zzyzx, %q) = %q, want %q", title, got, want)
		}
	}
	if got := entryNumber(&embyfin.Item{Name: "Zzyzx: Part One"}, tmdb.Hit{Title: "Zzyzx: Part One Extended"}); got != "" {
		t.Errorf("a number the item's title carries too = %q", got)
	}
	for title, want := range map[string]bool{
		"Zzyzx: Ultimate Edition": true, "Zzyzx: The Director's Cut": true, "Zzyzx: 25th Anniversary Edition": true, "Zzyzx: The Final Cut": true, "Zzyzx Redux": true, "Zzyzx (IMAX)": true,
		"Zzyzx in the Air": false, "Zzyzx: Part Two": false, "Zzyzx Returns": false, "Zzyzx: Special Forces": false,
	} {
		rest, _ := titleRest(zzyzx, tmdb.Hit{Title: title})
		if got := editionRest(rest); got != want {
			t.Errorf("editionRest(Zzyzx, %q) = %v, want %v", title, got, want)
		}
	}
}

// The file names a library holds, and what the whole-title reading takes
// from each: the year, the title read whole ("" when nothing follows the year
// but a renamer's or a release's words), and whether TMDB is asked about it -
// only when the words before the year are the film's own title and real
// words follow. Radarr's and TRaSH's names read "Alien Bluray-1080p" and
// "Alien FraMeSToR" as titles, and every film of a library so named was
// searched for, its collection read, and warned of when TMDB was down.
func TestTheWholeTitleAFileNameReads(t *testing.T) {
	t.Parallel()

	alien := &embyfin.Item{Type: typeMovie, Name: "Alien", ProductionYear: 1979}
	for _, tc := range []struct {
		file  string
		it    *embyfin.Item
		year  int
		whole string
		asked bool
	}{
		// a renamer's and a release's own words
		{"Alien (1979).mkv", alien, 1979, "", false},
		{"Alien (1979) Bluray-1080p.mkv", alien, 1979, "", false},
		{"Alien (1979) {imdb-tt0078748} {edition-Directors Cut} [Bluray-1080p][DTS-HD MA 5.1][x264]-FraMeSToR.mkv", alien, 1979, "", false},
		{"Alien.1979.1080p.BluRay.x264-GROUP.mkv", alien, 1979, "", false},
		{"Alien (1979) {edition-Director's Cut}.mkv", alien, 1979, "", false},
		{"Alien (1979) [Bluray-1080p].mkv", alien, 1979, "", false},
		{"Alien (1979) HDR10+ DV.mkv", alien, 1979, "", false},
		{"Alien (1979) IMAX.mkv", alien, 1979, "", false},
		{"Alien (1979) Remux-2160p.mkv", alien, 1979, "", false},
		{"Alien (1979) WEBDL-1080p Proper.mkv", alien, 1979, "", false},
		{"Alien (1979) REPACK Bluray-1080p.mkv", alien, 1979, "", false},
		{"Alien (1979) - 1080p.mkv", alien, 1979, "", false},
		{"Alien (1979) Bluray-1080p-GROUP.mkv", alien, 1979, "", false},
		// language, dub and 3D tags, a stacked file's part, a disc's, and
		// an extra's word
		{"Alien (1979) German-DL 1080p.mkv", alien, 1979, "", false},
		{"Alien (1979) VOSTFR.mkv", alien, 1979, "", false},
		{"Alien (1979) MULTi.mkv", alien, 1979, "", false},
		{"Alien (1979) iTALiAN.mkv", alien, 1979, "", false},
		{"Alien (1979) FRENCH.mkv", alien, 1979, "", false},
		{"Alien (1979) TRUEFRENCH.mkv", alien, 1979, "", false},
		{"Alien (1979) SUBBED.mkv", alien, 1979, "", false},
		{"Alien (1979) DUBBED.mkv", alien, 1979, "", false},
		{"Alien (1979) 3D HSBS.mkv", alien, 1979, "", false},
		{"Alien (1979) HOU.mkv", alien, 1979, "", false},
		{"Alien (1979) SBS.mkv", alien, 1979, "", false},
		{"Alien (1979) OU.mkv", alien, 1979, "", false},
		{"Alien (1979) cd1.avi", alien, 1979, "", false},
		{"Alien (1979) cd2.avi", alien, 1979, "", false},
		{"Alien (1979) pt1.avi", alien, 1979, "", false},
		{"Alien (1979) part1.avi", alien, 1979, "", false},
		{"Alien (1979) disc1.mkv", alien, 1979, "", false},
		{"Alien (1979) disk1.mkv", alien, 1979, "", false},
		{"Alien (1979) dvd1.vob", alien, 1979, "", false},
		{"Alien (1979) Disc 2.mkv", alien, 1979, "", false},
		{"Alien (1979) Sample.mkv", alien, 1979, "", false},
		{"Alien (1979) - Trailer.mkv", alien, 1979, "", false},
		// an edition's words alone after the year: read whole, and asked
		// nothing, as they can only be the film or an edition of it
		{"Alien (1979) Directors Cut.mkv", alien, 1979, "Alien Directors Cut", false},
		{"Alien (1979) Directors Cut Bluray-1080p-GROUP.mkv", alien, 1979, "Alien Directors Cut", false},
		{"Alien (1979) - Ultimate Edition.mkv", alien, 1979, "Alien Ultimate Edition", false},
		// words of a title after the year: asked about when the words
		// before it are the film's - "Part 2", the number set apart, can
		// name a sequel
		{"Alien (1979) - Aliens.mkv", alien, 1979, "Alien Aliens", true},
		{"Alien (1979) Part 2.mkv", alien, 1979, "Alien Part 2", true},
		{"Dune (2021) Part Two.mkv", &embyfin.Item{Type: typeMovie, Name: "Dune", ProductionYear: 2021}, 2021, "Dune Part Two", true},
		// the words before the year none of the film's titles: read by the
		// path's title check instead
		{"Mononoke-hime (1997) Remastered.mkv", &embyfin.Item{Type: typeMovie, Name: "Princess Mononoke", ProductionYear: 1997}, 1997, "Mononoke-hime Remastered", false},
		{"Mononoke-hime (1997) - Criterion.mkv", &embyfin.Item{Type: typeMovie, Name: "Princess Mononoke", ProductionYear: 1997}, 1997, "Mononoke-hime Criterion", false},
		{"Zzyzx (2016) Unlimited - Mechs.mkv", &embyfin.Item{Type: typeMovie, Name: "Zzyzx v Quux: Dawn", ProductionYear: 2016}, 2016, "Zzyzx Unlimited - Mechs", false},
		{"Alien Collection (1979) - Alien.avi", alien, 1979, "Alien Collection Alien", false},
	} {
		if got := naming.SegmentYear(naming.FileExtension.ReplaceAllString(tc.file, "")); got != tc.year {
			t.Errorf("%s: year %d, want %d", tc.file, got, tc.year)
		}
		if got := naming.WholeTitle(tc.file); got != tc.whole {
			t.Errorf("%s: whole title %q, want %q", tc.file, got, tc.whole)
		}
		if _, asked := wholeClaim(tc.it, "/m/"+tc.file); asked != tc.asked {
			t.Errorf("%s held as %s: asked %v, want %v", tc.file, tc.it.Name, asked, tc.asked)
		}
	}
}

// item_get's warning and audit_file_path's row read one file the same way:
// neither says another film where the other says this very film, and a doubt
// in one is a doubt in the other. A file whose title is the film's and whose
// year is not was "probably a different film" to item_get, while
// audit_file_path's search found this very film by the file's title and year
// and said the item's year was the one to check.
func TestItemGetAndTheFilePathAuditAgree(t *testing.T) {
	t.Parallel()

	transport, _ := editionTMDB(t)
	type fixture struct {
		id, name string
		year     int
		tmdb     string
		file     string
		row      string // what the row's diagnosis or problem says, "" for no row
		warning  string // how the warning begins, "" for none
		says     string // what else the warning says
	}
	for _, token := range []bool{true, false} {
		fixtures := []fixture{
			{"year", "Alien", 1986, "348", "Alien (1979)", "the year the item holds is the one to check", "the year the film holds is the one to check, not its file", "TMDB's search finds this very film, TMDB 348, by the path's title and year"},
			// another film of exactly the film's name: the row can't tell
			// which the file is with no runtime to go by, and the warning
			// doubts
			{"other", "Zzyzx", 2000, "90080", "Zzyzx (1990)", "a film of the same name as the item's TMDB 90080: can't tell which the file is", "may be a different film", "TMDB gives the path's title and year to its film 90081 Zzyzx (1990), another film of the same name"},
			{"none", "Quux", 2005, "90082", "Quux (1995)", "TMDB's search finds no film by the path's title and year", "may be a different film", "another film, or the item's year is wrong"},
			{"two", "Zzyzx", 2021, "90020", "Zzyzx (2021) Part Two", "numbered Two, as a film of its own", "probably a different film", "numbered Two"},
			{"edition", "Zzyzx", 2016, "90060", "Zzyzx (2016) Ultimate Edition", "", "", ""},
			{"titled", "Zzyzx", 2016, "90060", "Zzyzx Ultimate Edition (2016)", "which TMDB lists as an entry of its own titled the film's and an edition's words", "may be a different film", "an edition of this film, or another film"},
			{"cut", "Zzyzx", 1982, "90030", "Zzyzx (1982) - Final Cut", "", "", ""},
		}
		opts, check := Options{}, (*titleCheck)(nil)
		if token {
			opts = Options{TMDBKey: "k", ProviderTransport: transport}
			check = &titleCheck{titles: newProviderTitles(opts)}
		} else {
			// without a token: a year apart is a doubt either way, and a
			// title after the year is not asked about
			fixtures = []fixture{
				{"year", "Alien", 1986, "348", "Alien (1979)", "year: path says 1979, metadata says 1986", "may be a different film", "another film, or the item's year is wrong"},
				{"two", "Zzyzx", 2021, "90020", "Zzyzx (2021) Part Two", "", "", ""},
			}
		}
		films := make([]map[string]any, 0, len(fixtures))
		for _, f := range fixtures {
			films = append(films, map[string]any{
				"Id": f.id, "Name": f.name, "Type": "Movie", "ProductionYear": f.year, "ProviderIds": map[string]any{"Tmdb": f.tmdb},
				"Path": "/zz/films/" + f.file + "/" + f.file + ".mkv", "RunTimeTicks": 117 * ticksPerMinute,
			})
		}
		rows := map[string]string{}
		for _, r := range filePathRows(t, opts, films...) {
			rows[text(r["id"])] = strings.Join(append(texts(r["problems"]), text(r["diagnosis"])), " | ")
		}
		for _, f := range fixtures {
			row, got := rows[f.id], versionWarning(t.Context(), check, versionsOfFilm(f.name, f.year, f.tmdb, "/zz/films/"+f.file+"/"+f.file+".mkv"))
			if (f.row == "") != (row == "") || !strings.Contains(row, f.row) {
				t.Errorf("token %v, %s: audit_file_path's row %q, want %q", token, f.file, row, f.row)
			}
			if (f.warning == "") != (got == "") || !strings.HasPrefix(got, f.warning) || !strings.Contains(got, f.says) {
				t.Errorf("token %v, %s: item_get's warning %q, want %q ... %q", token, f.file, got, f.warning, f.says)
			}
			// the two never disagree about another film
			if another := strings.Contains(row, "another film matched to this one's ids") || strings.Contains(row, "matched to another film than the one on disk"); another != strings.HasPrefix(got, "probably") {
				t.Errorf("token %v, %s: the row %q and the warning %q disagree", token, f.file, row, got)
			}
			// nor does the row's own title problem, beside a diagnosis that
			// allows an edition
			if f.id == "titled" && !strings.Contains(row, `the path's title is the item's with "Ultimate Edition" added: an edition, a subtitle, or another film`) {
				t.Errorf("token %v, %s: the title problem beside an edition's diagnosis = %q", token, f.file, row)
			}
		}
	}
}

// filePathOut is audit_file_path's whole answer over one library of films.
func filePathOut(t *testing.T, opts Options, films ...map[string]any) map[string]any {
	t.Helper()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(films...))
	})

	return mustCall(t, session(t, f, opts), "audit_file_path", map[string]any{"library": "Zzyzx Films"})
}

// A film found by its title read whole is kept though TMDB fails to say
// whether it is of this one's series, and the failure is said beside it, where
// the finding was dropped with it. TMDB failing for every file leaves them
// unasked, and the answer says how many; a collection is read once for every
// film of it; and TMDB failing three times in a row stops the asking at once,
// rather than every file waiting out its own timeout.
func TestTheWholeTitleAskingSaysWhatItCouldNotAsk(t *testing.T) {
	t.Parallel()

	transport, asked := editionTMDB(t)
	film := func(id, file string, year int, tmdbID string) map[string]any {
		return map[string]any{
			"Id": id, "Name": "Zzyzx", "Type": "Movie", "ProductionYear": year, "ProviderIds": map[string]any{"Tmdb": tmdbID},
			"Path": "/zz/films/" + file + "/" + file + ".mkv", "RunTimeTicks": 117 * ticksPerMinute,
		}
	}
	rising := film("rising", "Zzyzx (2012) Quux Rising", 2012, "90092")
	rows := objects(t, filePathOut(t, Options{TMDBKey: "k", ProviderTransport: transport}, rising)["findings"], "findings")
	if len(rows) != 1 || !strings.HasPrefix(texts(rows[0]["problems"])[0], `title: "Zzyzx (2012) Quux Rising.mkv" names "Zzyzx Quux Rising", TMDB's film 90093 Zzyzx Quux Rising (2014), not Zzyzx: another film matched`) ||
		!strings.Contains(text(rows[0]["diagnosis"]), "TMDB could not be asked whether it is a film of this one's series") {
		t.Errorf("a finding beside a failed collection lookup = %v", rows)
	}
	check := &titleCheck{titles: newProviderTitles(Options{TMDBKey: "k", ProviderTransport: transport})}
	if w := versionWarning(t.Context(), check, versionsOfFilm("Zzyzx", 2012, "90092", "/zz/Zzyzx (2012)/Zzyzx (2012).mkv", "/zz/films/Zzyzx (2012) Quux Rising/Zzyzx (2012) Quux Rising.mkv")); !strings.HasPrefix(w, "probably not one film") || !strings.Contains(w, "(TMDB could not be asked: ") {
		t.Errorf("the version warning beside a failed collection lookup = %q", w)
	}

	out := filePathOut(t, Options{TMDBKey: "k", ProviderTransport: failingTransport{}}, film("two", "Zzyzx (2021) Part Two", 2021, "90020"), film("cut", "Zzyzx (1982) - Final Cut", 1982, "90030"))
	if n := number(t, out["total_findings"], "total_findings"); n != 0 || !strings.Contains(text(out["note"]), "TMDB could not be asked about 1 of the 1 films read this call") ||
		!strings.Contains(text(out["note"]), "1 of the 1 films whose file names more than the film after its year are ones TMDB failed on, and every other one is asked; calling again retries them") {
		t.Errorf("with TMDB down = %v", out)
	}

	titles := newProviderTitles(Options{TMDBKey: "k", ProviderTransport: transport})
	for _, id := range []string{"90050", "90053"} {
		parts, err := titles.CollectionParts(t.Context(), id)
		if err != nil || len(parts) != 3 {
			t.Fatalf("collectionParts(%s) = %v, %v", id, parts, err)
		}
	}
	if n := asked("/3/collection/95000"); n != 1 {
		t.Errorf("the collection was read %d times, want once", n)
	}

	calls := &countingTransport{}
	broken := newProviderTitles(Options{TMDBKey: "k", ProviderTransport: calls})
	var last error
	for i := range 5 {
		_, last = broken.Search(t.Context(), "movie", fmt.Sprintf("Zzyzx %d", i), 0)
	}
	// the breaker (tmdb.Breaker) counts reads, each tried triesPerRead
	// times before it fails: three failed reads, and the rest are not sent
	if n, want := int(calls.n.Load()), breakerReads*triesPerRead(); n != want || last == nil || !strings.Contains(last.Error(), "TMDB not asked") {
		t.Errorf("after TMDB failed, %d asks reached it and the last said %v; want %d and the breaker", n, last, want)
	}
}

// breakerReads is how many reads in a row TMDB fails before the breaker
// every TMDB read goes through (tmdb.Breaker) stops the asking.
const breakerReads = 3

// countingTransport is a provider that cannot be reached, counting the asks.
type countingTransport struct{ n atomic.Int32 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.n.Add(1)

	return nil, errors.New("connection refused")
}

// Each call asks TMDB about films it has not asked before, up to its limit,
// films TMDB failed to answer before after those never asked, and says how
// many are left; an answer an earlier call got is read again for nothing.
// The limit took the same first films in sweep order every call, and the
// answers were kept, so the films past it were never asked. And with TMDB
// failing one film in a hundred, a call asked the failed ones first, three in
// a row tripped the breaker, and the films past the limit were never reached
// while those kept failing; the note counted neither. Here TMDB always fails
// the same ten of a thousand films: two calls reach every other one, the
// notes count what is left, and four calls stay there.
func TestTheWholeTitleAskingGoesOnWhereItStopped(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	answered := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/search/movie" {
			http.NotFound(w, r)

			return
		}
		query := r.URL.Query().Get("query")
		var n int
		if _, err := fmt.Sscanf(query, "Zzyzx Quux %d", &n); err != nil {
			t.Errorf("an ask for %q", query)
		}
		if n%100 == 0 {
			http.Error(w, "down", http.StatusInternalServerError)

			return
		}
		mu.Lock()
		answered[query] = true
		mu.Unlock()
		writeJSON(t, w, map[string]any{"results": []any{}})
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	films := make([]map[string]any, 0, 2*wholeLimit)
	for i := range 2 * wholeLimit {
		file := fmt.Sprintf("Zzyzx (2000) Quux %d", i)
		films = append(films, map[string]any{
			"Id": fmt.Sprintf("f%d", i), "Name": "Zzyzx", "Type": "Movie", "ProductionYear": 2000, "ProviderIds": map[string]any{"Tmdb": "90100"},
			"Path": "/zz/films/" + file + "/" + file + ".mkv", "RunTimeTicks": 117 * ticksPerMinute,
		})
	}
	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(films...))
	})
	cs := session(t, f, Options{TMDBKey: "k", ProviderTransport: rewrite{target}})
	reached := func() int {
		mu.Lock()
		defer mu.Unlock()

		return len(answered)
	}

	for call, want := range []struct {
		reached     int
		failed, all string
	}{
		{wholeLimit - 5, "TMDB could not be asked about 5 of the 500 films read this call", "call again to continue: 505 of the 1000 films whose file names more than the film after its year are still to ask (one call asks TMDB about at most 500 not asked before), 5 of them ones TMDB failed on, which calling again retries"},
		// every film asked: what is left TMDB failed on, and calling again
		// only retries it, which the note does not call going on
		{2*wholeLimit - 10, "TMDB could not be asked about 5 of the 995 films read this call", "10 of the 1000 films whose file names more than the film after its year are ones TMDB failed on, and every other one is asked; calling again retries them"},
		{2*wholeLimit - 10, "TMDB could not be asked about 10 of the 1000 films read this call", "10 of the 1000 films whose file names more than the film after its year are ones TMDB failed on, and every other one is asked; calling again retries them"},
		{2*wholeLimit - 10, "TMDB could not be asked about 10 of the 1000 films read this call", "10 of the 1000 films whose file names more than the film after its year are ones TMDB failed on, and every other one is asked; calling again retries them"},
	} {
		note := text(mustCall(t, cs, "audit_file_path", map[string]any{"library": "Zzyzx Films"})["note"])
		if n := reached(); n != want.reached || !strings.Contains(note, want.failed) || !strings.Contains(note, want.all) || call > 0 && strings.Contains(note, "call again to continue") {
			t.Errorf("call %d reached %d films, want %d; note %q, want %q and %q", call+1, n, want.reached, note, want.failed, want.all)
		}
	}
}

// An ask the caller gave up on says nothing of TMDB: cancelled asks tripped
// the breaker, and the next caller's asks all failed at once.
func TestACancelledAskDoesNotTripTheBreaker(t *testing.T) {
	t.Parallel()

	calls := &countingTransport{}
	titles := newProviderTitles(Options{TMDBKey: "k", ProviderTransport: calls})
	gone, cancel := context.WithCancel(t.Context())
	cancel()
	for i := range 2 * breakerReads {
		if _, err := titles.Search(gone, "movie", fmt.Sprintf("Zzyzx %d", i), 0); err == nil {
			t.Fatal("a cancelled ask answered")
		}
	}
	_, err := titles.Search(t.Context(), "movie", "Zzyzx live", 0)
	if err == nil || strings.Contains(err.Error(), "TMDB not asked") || calls.n.Load() == 0 {
		t.Errorf("a live ask after cancelled ones = %v, with %d reaching TMDB; want TMDB asked", err, calls.n.Load())
	}
}
