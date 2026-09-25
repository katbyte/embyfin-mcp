package tools

import (
	"cmp"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// cannedTMDB answers the two questions audit_provider asks: a film by
// TMDB id, and what an IMDb id is.
func cannedTMDB(t *testing.T, films, found map[string]string) http.RoundTripper {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := strings.CutPrefix(r.URL.Path, "/3/movie/"); ok {
			body, known := films[id]
			if !known {
				http.NotFound(w, r)

				return
			}
			_, _ = w.Write([]byte(body))

			return
		}
		if id, ok := strings.CutPrefix(r.URL.Path, "/3/find/"); ok {
			_, _ = w.Write([]byte(cmp.Or(found[id], `{"movie_results":[],"tv_results":[],"tv_episode_results":[]}`)))

			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	return rewrite{target}
}

func TestAuditProviderIDs(t *testing.T) {
	t.Parallel()

	films := []map[string]any{
		{"Id": "f1", "Name": "Consistent", "Type": "Movie", "ProviderIds": map[string]string{"Tmdb": "9001", "Imdb": "tt9000001"}},
		{"Id": "f2", "Name": "Crossed", "Type": "Movie", "ProviderIds": map[string]string{"Tmdb": "9002", "Imdb": "tt9000099"}},
		{"Id": "f3", "Name": "Gone", "Type": "Movie", "ProviderIds": map[string]string{"Tmdb": "9003"}},
		{"Id": "f4", "Name": "Matched To A Series", "Type": "Movie", "ProviderIds": map[string]string{"Imdb": "tt9000004"}},
		{"Id": "f5", "Name": "Matched To An Episode", "Type": "Movie", "ProviderIds": map[string]string{"Imdb": "tt9000005"}},
		// an IMDb id TMDB cannot place is TMDB lacking the film as often as
		// the id being wrong
		{"Id": "f6", "Name": "Unplaceable", "Type": "Movie", "ProviderIds": map[string]string{"Imdb": "tt9000006"}},
		{"Id": "f7", "Name": "Only On IMDb", "Type": "Movie", "ProviderIds": map[string]string{"Imdb": "tt9000007"}},
		{"Id": "f8", "Name": "No Ids", "Type": "Movie"},
	}
	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": films[min(start, len(films)):], "TotalRecordCount": len(films)})
	})
	tmdb := cannedTMDB(t, map[string]string{
		"9001": `{"id":9001,"title":"Consistent","release_date":"2001-01-01","imdb_id":"tt9000001"}`,
		"9002": `{"id":9002,"title":"The Other Film","release_date":"2002-02-02","imdb_id":"tt9000002"}`,
	}, map[string]string{
		"tt9000004": `{"movie_results":[],"tv_results":[{"id":8004,"name":"Zzyzx Series","first_air_date":"2014-01-19"}],"tv_episode_results":[]}`,
		"tt9000005": `{"movie_results":[],"tv_results":[],"tv_episode_results":[{"id":1,"name":"Bargain Bin","show_id":8005,"season_number":1,"episode_number":21}]}`,
		"tt9000007": `{"movie_results":[{"id":9007,"title":"Only On IMDb","release_date":"2007-07-07"}],"tv_results":[],"tv_episode_results":[]}`,
	})
	cs := session(t, f, Options{TMDBKey: "k", ProviderTransport: tmdb})

	out := mustCall(t, cs, "audit_provider", map[string]any{})
	if number(t, out["items_scanned"], "items_scanned") != len(films) || out["next_offset"] != nil {
		t.Fatalf("out = %v", out)
	}
	got := map[string]string{}
	for _, row := range objects(t, out["findings"], "findings") {
		got[text(row["id"])] = strings.Join(texts(row["problems"]), " | ")
	}
	for id, want := range map[string]string{
		"f2": "ids: its TMDB id is 9002, The Other Film (2002), whose IMDb id is tt9000002, not the tt9000099 it holds",
		"f3": "TMDB has no film 9003",
		"f4": "is a series, not a film: Zzyzx Series, TMDB tv 8004",
		"f5": `is an episode, not a film: "Bargain Bin", S01E21 of TMDB tv 8005`,
	} {
		if !strings.Contains(got[id], want) {
			t.Errorf("%s = %q, want it to say %q", id, got[id], want)
		}
	}
	if len(got) != 4 || number(t, out["total_findings"], "total_findings") != 4 {
		t.Errorf("findings = %v", got)
	}

	// a capped call stops at the film it could not look up, and the next
	// call goes on from there; a film with no ids costs no lookup
	first := mustCall(t, cs, "audit_provider", map[string]any{"max_lookups": 3})
	next := number(t, first["next_offset"], "next_offset")
	if next != 3 || number(t, first["total_findings"], "total_findings") != 2 {
		t.Fatalf("first page = %v", first)
	}
	rest := mustCall(t, cs, "audit_provider", map[string]any{"offset": next})
	if number(t, rest["total_findings"], "total_findings") != 2 || rest["next_offset"] != nil {
		t.Errorf("the rest = %v", rest)
	}

	// no key, no audit: said plainly rather than an empty report
	keyless := session(t, f, Options{})
	if _, msg := callTool(t, keyless, "audit_provider", map[string]any{}); !strings.Contains(msg, "EMBYFIN_TMDB_TOKEN") {
		t.Errorf("without a key = %q", msg)
	}
}

// Two films of one sort name (the messy library's two Aliens; a remake) come
// back from a server's sort by name in either order, and paged by the
// server, the order could change between one call and the next: one film
// asked about twice and the other never. The films are put in an order of
// the tool's own, by sort name and then id, so a walk at one lookup a call
// asks about every film exactly once whatever order the server answers in.
func TestAuditProviderWalksEveryFilmOnce(t *testing.T) {
	t.Parallel()

	films := []map[string]any{
		{"Id": "31", "Name": "Alien", "SortName": "Alien", "Type": "Movie", "ProductionYear": 1979, "Path": "/m/Alien (1979)/Alien (1979).mp4", "ProviderIds": map[string]any{"Tmdb": "348"}, "RunTimeTicks": 60 * ticksPerSecond},
		{"Id": "25", "Name": "Alien", "SortName": "Alien", "Type": "Movie", "ProductionYear": 1979, "Path": "/m/Alien (1979) Directors Cut/Alien (1979) Directors Cut.mp4", "ProviderIds": map[string]any{"Tmdb": "348"}, "RunTimeTicks": 60 * ticksPerSecond},
		{"Id": "32", "Name": "Arrival", "SortName": "Arrival", "Type": "Movie", "ProductionYear": 2016, "Path": "/m/Arrival (2016)/Arrival (2016).mp4", "ProviderIds": map[string]any{"Tmdb": "329865"}, "RunTimeTicks": 60 * ticksPerSecond},
		{"Id": "28", "Name": "Princess Mononoke", "SortName": "Princess Mononoke", "Type": "Movie", "ProductionYear": 1997, "Path": "/m/Princess Mononoke (1997)/Princess Mononoke (1997).mp4", "RunTimeTicks": 60 * ticksPerSecond},
		{"Id": "29", "Name": "Blade Runner", "SortName": "Blade Runner", "Type": "Movie", "ProductionYear": 1982, "Path": "/m/Blade Runner (1982)/Blade Runner (1982).mp4", "ProviderIds": map[string]any{"Tmdb": "78"}, "RunTimeTicks": 60 * ticksPerSecond},
	}
	f, _ := zzyzxServer(t)
	var calls atomic.Int32
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		// the two Aliens swap places on every read, as a server's sort by
		// name is free to leave them
		rows := slices.Clone(films)
		if calls.Add(1)%2 == 0 {
			rows[0], rows[1] = rows[1], rows[0]
		}
		if ids := param(r.URL.Query(), "Ids"); ids != "" {
			rows = slices.DeleteFunc(rows, func(it map[string]any) bool { return !slices.Contains(strings.Split(ids, ","), text(it["Id"])) })
		}
		writeJSON(t, w, page(rows...))
	})
	// every film with an id runs a minute against TMDB's two hours: each one
	// asked about is a finding naming it
	cs := session(t, f, Options{TMDBKey: "k", ProviderTransport: tmdbTransport(t, map[string]int{"348": 117, "329865": 116, "78": 117})})

	seen := map[string]int{}
	scanned, offset := 0, 0
	for step := 0; ; step++ {
		if step > 10 {
			t.Fatal("the walk never finished")
		}
		out := mustCall(t, cs, "audit_provider", map[string]any{"library": "Zzyzx Films", "checks": "runtime", "max_lookups": 1, "offset": offset})
		scanned += number(t, out["items_scanned"], "items_scanned")
		for _, row := range objects(t, out["findings"], "findings") {
			seen[text(row["id"])]++
		}
		if out["next_offset"] == nil {
			break
		}
		offset = number(t, out["next_offset"], "next_offset")
	}
	for _, id := range []string{"31", "25", "32", "29"} {
		if seen[id] != 1 {
			t.Errorf("film %s was asked about %d times, want once: %v", id, seen[id], seen)
		}
	}
	if scanned != len(films) {
		t.Errorf("the walk scanned %d films, want every one of the %d once", scanned, len(films))
	}

	// a handful by id, without a sweep
	out := mustCall(t, cs, "audit_provider", map[string]any{"ids": []any{"32", "28"}, "checks": "runtime"})
	if number(t, out["items_scanned"], "items_scanned") != 2 || number(t, out["total_findings"], "total_findings") != 1 {
		t.Errorf("ids 32 and 28 = %v", out)
	}
	if msg := mustRefuse(t, cs, "audit_provider", map[string]any{"ids": []any{"32"}, "library": "Zzyzx Films"}); !strings.Contains(msg, "library or ids") {
		t.Errorf("library and ids = %q", msg)
	}
}
