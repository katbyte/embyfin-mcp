package tools

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
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
// nothing to offer and the server's substring search still gets its say - but
// only a say: what it finds is named for the caller to choose, because a
// title that matched nothing is not a match however few others there are.
func TestTheSearchStillAnswersWhatTheIndexCannot(t *testing.T) {
	t.Parallel()

	f := tvServer(t, severance())
	cs := session(t, f, Options{})

	msg := mustRefuse(t, cs, "show_episodes_exist", map[string]any{"series": "Sever", "episodes": []map[string]any{{"season": 1, "episode": 1}}})
	for _, want := range []string{"Severance", "id sev", "did not match", "give series_id"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not carry %q: %s", want, msg)
		}
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

// A write that only queues its work - a scan, a refresh, a task, an identify
// - lands after it returns. An index read in the seconds after it is the
// library as it was, and trusting that for the whole TTL hid a folder just
// scanned from plan_check for five minutes. So an index read soon after a
// write is trusted only briefly, until the write has had time to land.
func TestAnIndexReadJustAfterAWriteIsNotTrustedForLong(t *testing.T) {
	t.Parallel()

	f := tvServer(t, severance())
	client, err := embyfin.New(embyfin.Emby, f.srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cache := &seriesCache{indexes: map[string]*seriesIndex{}, now: func() time.Time { return now }}
	reads := func() int { return len(f.requests("/Items")) }
	get := func() {
		t.Helper()
		if _, err := cache.get(t.Context(), client, ""); err != nil {
			t.Fatal(err)
		}
	}

	// with no write, one read is trusted for the TTL
	get()
	before := reads()
	now = now.Add(4 * time.Minute)
	get()
	if reads() != before {
		t.Fatal("an index read with no write since was read again inside its TTL")
	}

	// a write drops it, and the read after it is the library mid-change
	cache.invalidate()
	now = now.Add(2 * time.Second)
	get()
	afterWrite := reads()
	if afterWrite == before {
		t.Fatal("a write did not drop the index")
	}
	// a batch of names moments later is still one read
	now = now.Add(time.Second)
	get()
	if reads() != afterWrite {
		t.Error("an index read a second ago was read again: a batch would cost a read a name")
	}
	// but a call half a minute later reads what the scan has done since,
	// where it used to trust the pre-scan index for five minutes
	now = now.Add(30 * time.Second)
	get()
	settling := reads()
	if settling == afterWrite {
		t.Fatal("an index read just after a write was trusted half a minute later")
	}

	// once the writes have had time to land, the index is trusted again,
	// and the one read then is kept for the TTL
	now = now.Add(seriesIndexSettle)
	get()
	settled := reads()
	if settled == settling {
		t.Error("the last index read while settling was trusted past it")
	}
	now = now.Add(4 * time.Minute)
	get()
	if reads() != settled {
		t.Error("an index read after the writes settled was not kept for the TTL")
	}
}

// "Andor" in a library without it found Pandora - the servers' search matches
// inside words - and Pandora, being the only thing found, was the answer. A
// title that matched nothing is not a match however few others there are.
func TestALoneSearchHitWhoseTitleMatchedNothingIsRefused(t *testing.T) {
	t.Parallel()

	f := tvServer(t, &fakeSeries{
		id: "pandora", name: "Pandora", year: 2019,
		episodes: []ep{{season: 1, number: 1, name: "Pilot", path: "/media/shows/Pandora/S01E01.mkv"}},
	})
	cs := session(t, f, Options{})

	msg := mustRefuse(t, cs, "show_episodes_exist", map[string]any{"series": "Andor", "episodes": []map[string]any{{"season": 1, "episode": 1}}})
	for _, want := range []string{`"Andor" matches nothing`, "Pandora", "id pandora", "did not match"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not carry %q: %s", want, msg)
		}
	}
	// a batch carries it on the row rather than answering for Pandora
	out := mustCall(t, cs, "show_episodes_exist", map[string]any{"queries": []map[string]any{
		{"series": "Andor", "episodes": []map[string]any{{"season": 1, "episode": 1}}},
	}})
	if g := objects(t, out["results"], "results")[0]; text(g["series_id"]) != "" || !strings.Contains(text(g["error"]), "did not match") {
		t.Errorf("a batch answered for the lone hit: %v", g)
	}
}

// Scores are hundredths and their differences are not: 0.95-0.93 came out a
// hair under the 0.02 margin and 0.92-0.90 a hair over, so the same gap was a
// clear winner or a tie depending on the digits.
func TestAClearWinnerDoesNotTurnOnFloatingPointDust(t *testing.T) {
	t.Parallel()

	rows := func(a, b float64) []seriesCandidate {
		return []seriesCandidate{{Name: "first", Score: a}, {Name: "second", Score: b}}
	}
	for _, pair := range [][2]float64{{0.95, 0.93}, {0.92, 0.90}, {1, 0.98}, {0.97, 0.95}, {0.93, 0.91}} {
		if !clearWinner(rows(pair[0], pair[1])) {
			t.Errorf("%.2f against %.2f is the margin exactly, and was not a clear winner", pair[0], pair[1])
		}
	}
	for _, pair := range [][2]float64{{0.95, 0.94}, {0.92, 0.91}, {1, 1}} {
		if clearWinner(rows(pair[0], pair[1])) {
			t.Errorf("%.2f against %.2f is inside the margin, and was a clear winner", pair[0], pair[1])
		}
	}
	if !clearWinner(rows(0.95, 0)[:1]) {
		t.Error("a single candidate is clear of nothing")
	}
}

// A series found by the fallback search is asked, like any other, whether the
// library holds it twice - which is a question about its provider ids. The
// search did not ask for them, so the answer was always no.
func TestTheFallbackSearchAsksForProviderIDs(t *testing.T) {
	t.Parallel()

	f := tvServer(t, severance())
	cs := session(t, f, Options{})

	// a fragment the index cannot place, so the search is asked
	mustCall(t, cs, "show_resolve", map[string]any{"title": "Sever"})
	asked := 0
	for _, r := range f.requests("/Items") {
		if !strings.Contains(r.Query, "SearchTerm=") {
			continue
		}
		asked++
		if !strings.Contains(r.Query, "ProviderIds") {
			t.Errorf("the fallback search did not ask for the provider ids: %s", r.Query)
		}
	}
	if asked == 0 {
		t.Fatal("the fallback search was never asked")
	}
}

// Whether a show is held under two entries is read off the library's series.
// When those cannot be read, the answer used to be silence, and silence reads
// as "held once" - which makes an absence look like proof.
func TestAnIndexThatCannotBeReadIsSaidNotHidden(t *testing.T) {
	t.Parallel()

	s := severance()
	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		// the series itself answers by id; the library's series do not
		if param(r.URL.Query(), "Ids") == s.id {
			writeJSON(t, w, map[string]any{"Items": []wireItem{s.item()}, "TotalRecordCount": 1})
			return
		}
		http.Error(w, "the database is locked", http.StatusInternalServerError)
	})
	f.mux.HandleFunc("GET /Shows/{id}/Episodes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": s.items(), "TotalRecordCount": len(s.episodes)})
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "show_episodes_exist", map[string]any{
		"series_id": "sev", "episodes": []map[string]any{{"season": 1, "episode": 9}},
	})
	if number(t, out["absent"], "absent") != 1 {
		t.Fatalf("absent = %v", out["absent"])
	}
	warning := text(out["warning"])
	for _, want := range []string{"could not be checked", "not proof"} {
		if !strings.Contains(warning, want) {
			t.Errorf("the warning does not carry %q: %q", want, warning)
		}
	}
}
