package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// searches counts the calls that asked the server's search for a name, as
// opposed to reading the series index.
func searches(f *fakeServer) int {
	n := 0
	for _, r := range f.requests("/Items") {
		if strings.Contains(r.Query, "SearchTerm=") {
			n++
		}
	}

	return n
}

// C12: a batch of names costs one read of the library's series, not up to
// four searches a name. Across a folder spanning hundreds of shows that is
// hundreds of round trips - and the search they replace is the thing that
// kept failing to return the right show.
func TestNamesAreMatchedAgainstTheIndexNotTheSearch(t *testing.T) {
	t.Parallel()

	shows := showLibrary(6, 1, 1) // A..F, one episode each
	for _, s := range shows {
		s.name = "Show " + s.name
	}
	f := tvServer(t, shows...)
	cs := session(t, f, Options{})

	names := []string{"Show A", "Show.B", "show c", "Show D", "Show E", "Show F"}
	queries := make([]map[string]any, 0, len(names))
	for _, name := range names {
		queries = append(queries, map[string]any{"series": name, "episodes": []map[string]any{{"season": 1, "episode": 1}}})
	}
	out := mustCall(t, cs, "show_episodes_exist", map[string]any{"queries": queries})
	for _, g := range objects(t, out["results"], "results") {
		if text(g["error"]) != "" {
			t.Errorf("%v did not resolve: %v", g["series"], g["error"])
		}
	}
	if n := searches(f); n != 0 {
		t.Errorf("six names cost %d searches; the index should answer all of them", n)
	}

	// and a second call reads nothing again: the index is kept
	before := len(f.requests("/Items"))
	mustCall(t, cs, "show_episodes_exist", map[string]any{"series": "Show A", "episodes": []map[string]any{{"season": 1, "episode": 1}}})
	if n := len(f.requests("/Items")) - before; n != 0 {
		t.Errorf("a second call read the library %d more times", n)
	}
}

// the shapes the search could not find and the index can: every word of the
// library's title is there to meet, however the release spelled it.
func TestTheIndexFindsWhatTheSearchMissed(t *testing.T) {
	t.Parallel()

	shows := []*fakeSeries{
		{id: "swat", name: "S.W.A.T.", year: 2017, episodes: []ep{{season: 1, number: 1, name: "One", path: "/m/s.mkv"}}},
		{id: "pd", name: "Chicago P.D.", year: 2014, episodes: []ep{{season: 1, number: 1, name: "One", path: "/m/p.mkv"}}},
		{id: "fire", name: "Chicago Fire", year: 2012, episodes: []ep{{season: 1, number: 1, name: "One", path: "/m/f.mkv"}}},
		{id: "svu", name: "Law & Order: Special Victims Unit", year: 1999, episodes: []ep{{season: 1, number: 1, name: "One", path: "/m/v.mkv"}}},
		{id: "lao", name: "Law & Order", year: 1990, episodes: []ep{{season: 1, number: 1, name: "One", path: "/m/l.mkv"}}},
	}
	cs := session(t, tvServer(t, shows...), Options{})

	for name, want := range map[string]string{"S W A T": "swat", "Chicago PD": "pd", "Law And Order SVU": "svu"} {
		out := mustCall(t, cs, "show_episodes_exist", map[string]any{"series": name, "episodes": []map[string]any{{"season": 1, "episode": 1}}})
		if got := text(out["series_id"]); got != want {
			t.Errorf("%q resolved to %q, want %s", name, got, want)
		}
	}
}

// a fragment of a name shares no whole word with any title, so the index has
// nothing to offer and the server's substring search still gets its say.
func TestTheSearchStillAnswersWhatTheIndexCannot(t *testing.T) {
	t.Parallel()

	f := tvServer(t, severance())
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "show_episodes_exist", map[string]any{"series": "Sever", "episodes": []map[string]any{{"season": 1, "episode": 1}}})
	if text(out["series_id"]) != "sev" {
		t.Errorf("a fragment the search finds did not resolve: %v", out)
	}
	if searches(f) == 0 {
		t.Error("the index had nothing and the search was not asked")
	}
}

// the same show held twice is found by provider id in the index, not by a
// search per series asked about.
func TestDuplicatesComeFromTheIndex(t *testing.T) {
	t.Parallel()

	first := &fakeSeries{
		id: "a", name: "Some Procedural", year: 2000, ids: map[string]string{"Tmdb": "90001"},
		episodes: []ep{{season: 1, number: 1, name: "One", path: "/m/a.mkv"}},
	}
	second := &fakeSeries{
		id: "b", name: "Some Procedural", year: 2000, ids: map[string]string{"Tmdb": "90001"},
		episodes: []ep{{season: 2, number: 1, name: "Two", path: "/m/b.mkv"}},
	}
	f := tvServer(t, first, second, severance())
	cs := session(t, f, Options{})

	queries := make([]map[string]any, 0, 5)
	for range 5 {
		queries = append(queries, map[string]any{"series_id": "a", "episodes": []map[string]any{{"season": 1, "episode": 1}}})
	}
	out := mustCall(t, cs, "show_episodes_exist", map[string]any{"queries": queries})
	for _, g := range objects(t, out["results"], "results") {
		if others := texts(g["duplicate_entries"]); len(others) != 1 || others[0] != "b" {
			t.Errorf("duplicate_entries = %v", g["duplicate_entries"])
		}
	}
	if n := searches(f); n != 0 {
		t.Errorf("five lookups of the same series cost %d searches", n)
	}
}

// a write through this server may have added, renamed or removed a series,
// so the index is dropped rather than trusted; a read keeps it.
func TestAWriteDropsTheIndex(t *testing.T) {
	t.Parallel()

	r := &registry{server: mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)}
	stock := func() {
		r.seriesCache().mu.Lock()
		r.seriesCache().indexes[""] = &seriesIndex{read: time.Now()}
		r.seriesCache().mu.Unlock()
	}
	held := func() bool {
		r.seriesCache().mu.Lock()
		defer r.seriesCache().mu.Unlock()

		return len(r.seriesCache().indexes) > 0
	}
	type none struct{}
	ok := func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, none, error) {
		return nil, none{}, nil
	}
	add(r, readTool, &mcp.Tool{Name: "a_read"}, ok)
	add(r, writeTool, &mcp.Tool{Name: "a_write"}, ok)
	for _, p := range r.pending {
		p.register()
	}

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := r.server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	stock()
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "a_read", Arguments: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if !held() {
		t.Error("a read dropped the index")
	}
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "a_write", Arguments: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if held() {
		t.Error("a write left the index in place")
	}
}
