package tools

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// What a caller can name, and what it comes back as.
func TestParseProviders(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   string
		want []string
	}{
		// none named: the audit's default, any id at all
		{"", nil},
		{" , ", nil},
		{"tmdb", []string{"tmdb"}},
		{" TMDB , imdb ", []string{"tmdb", "imdb"}},
		// the order a finding names them in, whatever order they were asked in
		{"myanimelist,tvdb,tmdb", []string{"tmdb", "tvdb", "myanimelist"}},
		{"imdb,imdb", []string{"imdb"}},
	} {
		got, err := parseProviders(tc.in)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("parseProviders(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}

	// a misspelling would otherwise flag every item in the library
	if _, err := parseProviders("tmdb,tmbd"); err == nil || !strings.Contains(err.Error(), `"tmbd"`) || !strings.Contains(err.Error(), "tmdb, imdb, tvdb, anidb, myanimelist") {
		t.Errorf("a misspelled provider = %v", err)
	}
}

// With nothing named, an id from any provider is a match.
func TestNoProviderID(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		ids map[string]string
		bad bool
	}{
		{nil, true},
		{map[string]string{}, true},
		// an empty id is no id
		{map[string]string{"Tmdb": ""}, true},
		{map[string]string{"Tmdb": "", "Imdb": "tt1"}, false},
		// an anime matched only where anime is matched
		{map[string]string{"AniDb": "3001", "MyAnimeList": "4001"}, false},
		// an id from a provider nobody listed is still an id
		{map[string]string{"Wikidata": "Q1"}, false},
		// a link to one of its pages is not: a show can carry these with
		// every id taken away
		{map[string]string{"Facebook": "SomeShow", "Official Website": "https://example.com", "X (Twitter)": "someshow"}, true},
		{map[string]string{"Facebook": "SomeShow", "Tvdb": "2002"}, false},
	} {
		detail, bad := noProviderID(&embyfin.Item{ProviderIDs: tc.ids})
		if bad != tc.bad {
			t.Errorf("%v = %q, %v; want %v", tc.ids, detail, bad, tc.bad)
		}
	}
	// and a finding says why an item with keys is one
	if detail, _ := noProviderID(&embyfin.Item{ProviderIDs: map[string]string{"Facebook": "SomeShow"}}); detail != "no provider id, only links to its pages" {
		t.Errorf("links only = %q", detail)
	}
	if detail, _ := noProviderID(&embyfin.Item{}); detail != "no provider id" {
		t.Errorf("nothing = %q", detail)
	}
}

func TestMissingProviders(t *testing.T) {
	t.Parallel()

	item := func(ids map[string]string) *embyfin.Item { return &embyfin.Item{ProviderIDs: ids} }
	for _, tc := range []struct {
		missing []string
		ids     map[string]string
		detail  string // "" is not a finding
	}{
		// matched on TVDB alone, which the default does not report
		{[]string{"tmdb"}, map[string]string{"Tvdb": "2002"}, "no tmdb id; has tvdb:2002"},
		{[]string{"tmdb"}, map[string]string{"IMDB": "tt1003", "Tvdb": "2003"}, "no tmdb id; has imdb:tt1003 tvdb:2003"},
		{[]string{"tmdb"}, map[string]string{"Tmdb": "1001"}, ""},
		{[]string{"tmdb"}, nil, "no tmdb id"},
		// an empty id is no id
		{[]string{"tmdb"}, map[string]string{"Tmdb": "", "Imdb": "tt1005"}, "no tmdb id; has imdb:tt1005"},
		// two named: an item with either one is fine
		{[]string{"tmdb", "imdb"}, map[string]string{"Tvdb": "2002"}, "no tmdb/imdb id; has tvdb:2002"},
		{[]string{"tmdb", "imdb"}, map[string]string{"Imdb": "tt1003", "Tvdb": "2003"}, ""},
		// an anime matched on AniDB and MyAnimeList says so
		{[]string{"tmdb"}, map[string]string{"AniDb": "3001", "MyAnimeList": "4001"}, "no tmdb id; has anidb:3001 myanimelist:4001"},
		// ids beyond the ones missing can name are not what finds an item
		{[]string{"tmdb"}, map[string]string{"Zap2It": "EP1", "Tvdb": "2002"}, "no tmdb id; has tvdb:2002"},
	} {
		detail, bad := missingProviders(tc.missing)(item(tc.ids))
		if bad != (tc.detail != "") || detail != tc.detail {
			t.Errorf("missing %v on %v = %q, %v; want %q", tc.missing, tc.ids, detail, bad, tc.detail)
		}
	}
}

// The option through the tool: by default any id is a match, and missing
// drills down, so missing=tmdb finds the shows matched everywhere but TMDB.
func TestAuditMissingMetadataProviderMissing(t *testing.T) {
	t.Parallel()

	rows := []map[string]any{
		{"Id": "a", "Name": "Matched Everywhere", "Type": "Series", "ProviderIds": map[string]string{"Tmdb": "1001", "Imdb": "tt1001", "Tvdb": "2001"}},
		{"Id": "b", "Name": "Only On TVDB", "Type": "Series", "ProviderIds": map[string]string{"Tvdb": "2002"}},
		{"Id": "c", "Name": "TVDB And IMDB", "Type": "Series", "ProviderIds": map[string]string{"Imdb": "tt1003", "Tvdb": "2003"}},
		{"Id": "d", "Name": "Never Matched", "Type": "Movie"},
		{"Id": "e", "Name": "An Anime", "Type": "Series", "ProviderIds": map[string]string{"AniDb": "3001", "MyAnimeList": "4001"}},
	}
	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": rows[min(start, len(rows)):], "TotalRecordCount": len(rows)})
	})
	cs := session(t, f, Options{})

	found := func(args map[string]any) map[string]string {
		t.Helper()
		out := mustCall(t, cs, "audit_missing_metadata_provider", args)
		got := map[string]string{}
		for _, f := range objects(t, out["findings"], "findings") {
			got[text(f["id"])] = text(f["detail"])
		}
		if number(t, out["total_findings"], "total_findings") != len(got) || number(t, out["items_scanned"], "items_scanned") != len(rows) {
			t.Errorf("%v: out = %v", args, out)
		}

		return got
	}

	for args, want := range map[string]map[string]string{
		// by default only the item matched nowhere: the anime is matched
		"": {"d": "no provider id"},
		// the three the default used to mean, which the anime has none of
		"tvdb,imdb,tmdb": {
			"d": "no tmdb/imdb/tvdb id",
			"e": "no tmdb/imdb/tvdb id; has anidb:3001 myanimelist:4001",
		},
		"tmdb": {
			"b": "no tmdb id; has tvdb:2002",
			"c": "no tmdb id; has imdb:tt1003 tvdb:2003",
			"d": "no tmdb id",
			"e": "no tmdb id; has anidb:3001 myanimelist:4001",
		},
		"TMDB, imdb": {
			"b": "no tmdb/imdb id; has tvdb:2002",
			"d": "no tmdb/imdb id",
			"e": "no tmdb/imdb id; has anidb:3001 myanimelist:4001",
		},
	} {
		if got := found(map[string]any{"missing": args}); !maps.Equal(got, want) {
			t.Errorf("missing %q = %v, want %v", args, got, want)
		}
	}

	if _, msg := callTool(t, cs, "audit_missing_metadata_provider", map[string]any{"missing": "tmbd"}); !strings.Contains(msg, `unknown provider "tmbd"`) {
		t.Errorf("a misspelled provider was not refused: %q", msg)
	}
}

// ignore leaves out a library whose items can never carry an id, by its
// folder, before the items are counted.
func TestAuditMissingMetadataProviderIgnore(t *testing.T) {
	t.Parallel()

	libraries := []map[string]any{
		{"Name": "TV", "CollectionType": "tvshows", "ItemId": "lib1", "Locations": []string{"/m/tv"}},
		{"Name": "YouTube", "CollectionType": "tvshows", "ItemId": "lib2", "Locations": []string{"/m/youtube/"}},
	}
	rows := []map[string]any{
		{"Id": "a", "Name": "Unmatched Show", "Type": "Series", "Path": "/m/tv/Unmatched Show"},
		{"Id": "b", "Name": "A Channel", "Type": "Series", "Path": "/m/youtube/A Channel"},
		{"Id": "c", "Name": "Another Channel", "Type": "Series", "Path": "/m/youtube/Another Channel", "ProviderIds": map[string]string{"Tvdb": "2002"}},
		// a folder whose name only starts like the ignored one is not in it
		{"Id": "d", "Name": "Not A Channel", "Type": "Series", "Path": "/m/youtube-archive/Not A Channel"},
	}
	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": libraries, "TotalRecordCount": len(libraries)})
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": rows[min(start, len(rows)):], "TotalRecordCount": len(rows)})
	})
	cs := session(t, f, Options{})

	for _, args := range []map[string]any{
		{"ignore": []any{"youtube"}}, // by name, in any case
		{"ignore": []any{"lib2"}},    // by id
		{"ignore": []any{"YouTube"}, "missing": "tmdb"},
	} {
		out := mustCall(t, cs, "audit_missing_metadata_provider", args)
		var ids []string
		for _, f := range objects(t, out["findings"], "findings") {
			ids = append(ids, text(f["id"]))
		}
		if !slices.Equal(ids, []string{"a", "d"}) || number(t, out["items_scanned"], "items_scanned") != 2 {
			t.Errorf("%v = %v, want a and d found, and the two channels not even counted", args, out)
		}
	}

	// a library that does not exist is refused, rather than leaving out
	// nothing and reporting what was meant to be left out
	if _, msg := callTool(t, cs, "audit_missing_metadata_provider", map[string]any{"ignore": []any{"YuoTube"}}); !strings.Contains(msg, `no library named "YuoTube"`) {
		t.Errorf("an unknown library was not refused: %q", msg)
	}
}
