package tools

import (
	"cmp"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// cannedTMDB answers the two questions audit_movie_ids asks: a film by
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

func TestAuditMovieIDs(t *testing.T) {
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

	out := mustCall(t, cs, "audit_movie_ids", map[string]any{})
	if number(t, out["items_scanned"], "items_scanned") != len(films) || out["next_start_index"] != nil {
		t.Fatalf("out = %v", out)
	}
	got := map[string]string{}
	for _, row := range objects(t, out["findings"], "findings") {
		got[text(row["id"])] = text(row["detail"])
	}
	for id, want := range map[string]string{
		"f2": "whose IMDb id is tt9000002, not the tt9000099 it holds",
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
	first := mustCall(t, cs, "audit_movie_ids", map[string]any{"max_lookups": 3})
	next := number(t, first["next_start_index"], "next_start_index")
	if next != 3 || number(t, first["total_findings"], "total_findings") != 2 {
		t.Fatalf("first page = %v", first)
	}
	rest := mustCall(t, cs, "audit_movie_ids", map[string]any{"start_index": next})
	if number(t, rest["total_findings"], "total_findings") != 2 || rest["next_start_index"] != nil {
		t.Errorf("the rest = %v", rest)
	}

	// no key, no audit: said plainly rather than an empty report
	keyless := session(t, f, Options{})
	if _, msg := callTool(t, keyless, "audit_movie_ids", map[string]any{}); !strings.Contains(msg, "EMBYFIN_TMDB_TOKEN") {
		t.Errorf("without a key = %q", msg)
	}
}
