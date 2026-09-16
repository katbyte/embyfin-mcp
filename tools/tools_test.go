package tools

import (
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newTestClient(t *testing.T) *embyfin.Client {
	t.Helper()

	c, err := embyfin.New(embyfin.Emby, "http://127.0.0.1:1", "test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func register(t *testing.T, opts Options) []string {
	t.Helper()

	names, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestRegisterAllKinds(t *testing.T) {
	t.Parallel()

	all := register(t, Options{EnableDelete: true})
	dflt := register(t, Options{})
	ro := register(t, Options{ReadOnly: true})

	if len(all) <= len(dflt) || len(dflt) <= len(ro) || len(ro) == 0 {
		t.Fatalf("counts all=%d default=%d read-only=%d", len(all), len(dflt), len(ro))
	}
	for _, name := range []string{"item_delete", "library_delete"} {
		if slices.Contains(dflt, name) {
			t.Errorf("%s registered without --enable-delete", name)
		}
		if !slices.Contains(all, name) {
			t.Errorf("%s missing with --enable-delete", name)
		}
	}
	for _, name := range ro {
		for _, suffix := range []string{"_set", "_delete", "_scan", "_edit", "_apply", "_run", "_create", "_add", "_remove", "_download", "_play", "_command", "_message", "_refresh", "_set_watched", "_set_favourite", "_set_progress", "_rename"} {
			if strings.HasSuffix(name, suffix) {
				t.Errorf("%s registered under --read-only", name)
			}
		}
	}
	for _, name := range EssentialTools {
		if !slices.Contains(dflt, name) {
			t.Errorf("essential tool %s does not exist", name)
		}
	}
	if !slices.IsSorted(dflt) {
		t.Error("registered names not sorted")
	}
}

func TestRegisterAllFilters(t *testing.T) {
	t.Parallel()

	got := register(t, Options{Allow: []string{"essential"}})
	want := slices.Clone(EssentialTools)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("essential = %v", got)
	}

	got = register(t, Options{Allow: []string{"library_*,user_list"}, Deny: []string{"*_scan"}})
	for _, name := range got {
		if !strings.HasPrefix(name, "library_") && name != "user_list" {
			t.Errorf("unexpected %s", name)
		}
		if name == "library_scan" {
			t.Error("denied tool registered")
		}
	}
	if !slices.Contains(got, "library_list") || !slices.Contains(got, "user_list") {
		t.Errorf("allow list not honoured: %v", got)
	}

	if _, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), Options{Allow: []string{"bogus_*"}}); err == nil {
		t.Error("unknown allow pattern accepted")
	}
	if _, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), Options{Deny: []string{"nope"}}); err == nil {
		t.Error("unknown deny pattern accepted")
	}
}

// Every tool belongs to exactly one toolset, every toolset names only real
// tools, and core comes along with whatever else is asked for.
func TestToolsetsPartition(t *testing.T) {
	t.Parallel()

	all := register(t, Options{EnableDelete: true})
	seen := map[string]string{}
	for set, members := range Toolsets {
		for _, m := range members {
			if !slices.Contains(all, m) {
				t.Errorf("toolset %s names %s, which is not a tool", set, m)
			}
			if prev, dup := seen[m]; dup {
				t.Errorf("%s is in both %s and %s", m, prev, set)
			}
			seen[m] = set
		}
	}
	for _, name := range all {
		if seen[name] == "" {
			t.Errorf("%s belongs to no toolset", name)
		}
	}

	got := register(t, Options{Toolsets: []string{"organise"}})
	for _, core := range Toolsets["core"] {
		if !slices.Contains(got, core) {
			t.Errorf("core tool %s missing when only organise was asked for", core)
		}
	}
	for _, name := range got {
		if seen[name] != "core" && seen[name] != "organise" {
			t.Errorf("%s (%s) registered for --toolsets organise", name, seen[name])
		}
	}

	// a resource family is every tool with that prefix, plus core
	got = register(t, Options{Toolsets: []string{"playlist"}})
	for _, name := range got {
		if !strings.HasPrefix(name, "playlist_") && seen[name] != "core" {
			t.Errorf("%s registered for the playlist family", name)
		}
	}
	if !slices.Contains(got, "playlist_list") {
		t.Errorf("the playlist family lacks playlist_list: %v", got)
	}

	// all is everything the kind gates allow
	if got = register(t, Options{Toolsets: []string{"all"}}); len(got) != len(register(t, Options{})) {
		t.Errorf("all registered %d tools, want %d", len(got), len(register(t, Options{})))
	}

	if _, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), Options{Toolsets: []string{"nope"}}); err == nil {
		t.Error("unknown toolset accepted")
	} else if !strings.Contains(err.Error(), "curation") || !strings.Contains(err.Error(), "playlist") {
		t.Errorf("the error should name the sets and families: %v", err)
	}
}

// A session reads the tools it loaded and follows where they point, so a tool
// names only tools that load with it: its own set's, or core's, which comes
// with every set. A core tool loads on its own by default, so it names only
// core tools.
func TestToolsetsNameOnlyWhatTheyLoad(t *testing.T) {
	t.Parallel()

	all := register(t, Options{EnableDelete: true})
	set := map[string]string{}
	for name, members := range Toolsets {
		for _, m := range members {
			set[m] = name
		}
	}
	res, err := session(t, newFakeServer(t), Options{EnableDelete: true}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	word := regexp.MustCompile(`[a-z]+(?:_[a-z]+)+`)
	for _, tool := range res.Tools {
		// the description and the input and output schemas' own descriptions
		raw, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range word.FindAllString(string(raw), -1) {
			if ref == tool.Name || !slices.Contains(all, ref) {
				continue
			}
			if set[ref] != "core" && set[ref] != set[tool.Name] {
				t.Errorf("%s (%s) names %s, which only %s loads", tool.Name, set[tool.Name], ref, set[ref])
			}
		}
	}
}

// Describe reports the same selection RegisterAll makes, with its kinds and
// sets, and needs no server.
func TestDescribe(t *testing.T) {
	t.Parallel()

	list, err := Describe(Options{Toolsets: []string{"curation"}, EnableDelete: true})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list))
	kinds := map[string]string{}
	for _, ti := range list {
		names = append(names, ti.Name)
		kinds[ti.Name] = ti.Kind
		if ti.Toolset != "core" && ti.Toolset != "curation" {
			t.Errorf("%s reported in %s", ti.Name, ti.Toolset)
		}
		if ti.Description == "" {
			t.Errorf("%s has no description", ti.Name)
		}
	}
	want := register(t, Options{Toolsets: []string{"curation"}, EnableDelete: true})
	if !slices.Equal(names, want) {
		t.Errorf("Describe = %v\nRegisterAll = %v", names, want)
	}
	if kinds["audit_all"] != "read" || kinds["item_edit"] != "write" {
		t.Errorf("kinds = %v", kinds)
	}
	if _, err := Describe(Options{Allow: []string{"nope"}}); err == nil {
		t.Error("Describe accepted a pattern that matches nothing")
	}

	if fam := FamilyNames(); !slices.Contains(fam, "audit") || !slices.Contains(fam, "item") {
		t.Errorf("families = %v", fam)
	}
	if sets := ToolsetNames(); !slices.Contains(sets, "core") || !slices.IsSorted(sets) {
		t.Errorf("toolset names = %v", sets)
	}
}

func TestMatchPattern(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"item_get", "item_get", true},
		{"item_get", "item_gets", false},
		{"item_*", "item_get", true},
		{"item_*", "library_items", false},
		{"*_delete", "item_delete", true},
		{"*", "anything", true},
	} {
		if got := matchPattern(tc.pattern, tc.name); got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v", tc.pattern, tc.name, got)
		}
	}
}

// A nil slice in a result must serialise as [], so a client can tell "none"
// from "not fetched".
func TestEmptyNilSlices(t *testing.T) {
	t.Parallel()

	type inner struct{ Tags []string }
	type out struct {
		Items  []inner
		Ptr    *inner
		Names  []string
		Nested [][]string
		Keep   []string
	}
	v := out{Items: []inner{{}}, Ptr: &inner{}, Keep: []string{"x"}}
	emptyNilSlices(reflect.ValueOf(&v).Elem())

	if v.Names == nil || v.Nested == nil || v.Items[0].Tags == nil || v.Ptr.Tags == nil {
		t.Errorf("nil slices survived: %+v", v)
	}
	if len(v.Keep) != 1 {
		t.Error("a populated slice was touched")
	}
}

func TestCutoffs(t *testing.T) {
	t.Parallel()

	if d := time.Since(daysCutoff(0)); d < 59*24*time.Hour || d > 61*24*time.Hour {
		t.Errorf("default cutoff is %v ago, want ~60 days", d)
	}
	if d := time.Since(daysCutoff(7)); d < 6*24*time.Hour || d > 8*24*time.Hour {
		t.Errorf("cutoff(7) is %v ago", d)
	}
	cut := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !afterCutoff("2026-01-02T00:00:00Z", cut) || afterCutoff("2025-12-31T00:00:00Z", cut) {
		t.Error("afterCutoff compares the wrong way")
	}
	if afterCutoff("", cut) || afterCutoff("yesterday", cut) {
		t.Error("an unparseable stamp counts as after the cutoff")
	}
}

func TestAuditChecks(t *testing.T) {
	t.Parallel()

	item := func(path string, year int, ids map[string]string) *embyfin.Item {
		return &embyfin.Item{Path: path, ProductionYear: year, ProviderIDs: ids}
	}

	unmatched := auditCheckByName("audit_missing_metadata_provider")
	if _, bad := unmatched.check(item("/m/Princess Mononoke (1997)", 1997, nil)); !bad {
		t.Error("unmatched: no ids not flagged")
	}
	if _, bad := unmatched.check(item("/m/Princess Mononoke (1997)", 1997, map[string]string{"Tmdb": "128"})); bad {
		t.Error("unmatched: a tmdb id flagged")
	}
	if _, bad := unmatched.check(item("/m/Princess Mononoke (1997)", 1997, map[string]string{"Tmdb": "", "Imdb": "tt1"})); bad {
		t.Error("unmatched: an imdb id flagged")
	}

	for _, tc := range []struct {
		path string
		year int
		bad  bool
	}{
		{"/m/Dune (2021)/Dune (2021).mp4", 1984, true},
		{"/m/Dune (2021)/Dune (2021).mp4", 2021, false},
		{"/m/Dune (2021)/Dune (2021).mp4", 2022, false},       // a year out is a release-date quibble
		{"/m/Dune/Dune.mp4", 1984, false},                     // no year in the path
		{"/m/Dune (2021)/x.mp4", 0, false},                    // no metadata year
		{"/m/2001 A Space Odyssey (1968)/x.mp4", 1968, false}, // the title's number is not in parentheses
	} {
		if _, bad := checkYearMismatch(item(tc.path, tc.year, nil)); bad != tc.bad {
			t.Errorf("year mismatch %q/%d = %v, want %v", tc.path, tc.year, bad, tc.bad)
		}
	}

	overview := auditCheckByName("audit_missing_overview")
	if _, bad := overview.check(&embyfin.Item{Overview: "  "}); !bad {
		t.Error("overview: blank not flagged")
	}
	poster := auditCheckByName("audit_missing_poster")
	if _, bad := poster.check(&embyfin.Item{ImageTags: map[string]string{"Primary": "abc"}}); bad {
		t.Error("poster: a primary image flagged")
	}
	versions := auditCheckByName("audit_multiple_versions")
	detail, bad := versions.check(&embyfin.Item{MediaSources: []embyfin.MediaSource{{Path: "/m/a - 1080p.mp4"}, {Path: "/m/a - 2160p.mp4"}}})
	if !bad || !strings.Contains(detail, "2 versions") || !strings.Contains(detail, "a - 2160p.mp4") {
		t.Errorf("versions: %q %v", detail, bad)
	}
	if _, bad := versions.check(&embyfin.Item{MediaSources: []embyfin.MediaSource{{Path: "/m/a.mp4"}}}); bad {
		t.Error("versions: one file flagged")
	}
	if auditCheckByName("nope") != nil {
		t.Error("an unknown audit was found")
	}
}

func TestRuntimeOff(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		actual, expected, tol int
		wantPct               int
		wantOff               bool
	}{
		{100, 100, 20, 0, false},
		{101, 100, 20, 0, false}, // under the two-minute floor
		{110, 100, 20, 10, false},
		{130, 100, 20, 30, true},
		{70, 100, 20, 30, true},
		{1, 169, 20, 99, true},
		{50, 0, 20, 0, false}, // nothing to compare to
	} {
		pct, off := runtimeOff(tc.actual, tc.expected, tc.tol)
		if pct != tc.wantPct || off != tc.wantOff {
			t.Errorf("runtimeOff(%d, %d, %d) = %d, %v; want %d, %v", tc.actual, tc.expected, tc.tol, pct, off, tc.wantPct, tc.wantOff)
		}
	}
}

func TestProviderIDAndSearchTypes(t *testing.T) {
	t.Parallel()

	it := &embyfin.Item{ProviderIDs: map[string]string{"Tmdb": "1", "IMDB": "tt2"}}
	if providerID(it, "tmdb") != "1" || providerID(it, "imdb") != "tt2" || providerID(it, "tvdb") != "" {
		t.Errorf("providerID is not case-insensitive: %v", it.ProviderIDs)
	}

	for folder, want := range map[*embyfin.VirtualFolder]string{
		nil:                         searchTypesAll,
		{CollectionType: "movies"}:  typeMovie,
		{CollectionType: "tvshows"}: "Series",
		{CollectionType: "music"}:   "MusicAlbum",
		{CollectionType: ""}:        searchTypesAll,
	} {
		if got := defaultSearchTypes(folder); got != want {
			t.Errorf("defaultSearchTypes(%v) = %q, want %q", folder, got, want)
		}
	}
}
