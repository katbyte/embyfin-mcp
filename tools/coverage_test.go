package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// The tools the live suite exercises but nothing pinned against the canned
// server: what each asks the server for, and the names it answers under. A
// change to a route or an output field shows up here rather than in a live
// run that needs a container.

// libraryState is the libraries a canned server lists, which the library
// tools change.
type libraryState struct {
	mu      sync.Mutex
	folders []map[string]any
}

func (s *libraryState) list() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.folders)
}

// zzyzxServer is the canned Emby most tools start from: one movie library
// and one administrator, since nearly every tool resolves a library or a
// user before it does anything.
func zzyzxServer(t *testing.T) (*fakeServer, *libraryState) {
	t.Helper()

	f := newFakeServer(t)
	libs := &libraryState{folders: []map[string]any{{
		"Name": "Zzyzx Films", "CollectionType": "movies", "ItemId": "lib9", "Locations": []string{"/zz/films"},
		"LibraryOptions": map[string]any{"MetadataSavers": []string{}},
	}}}
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		folders := libs.list()
		writeJSON(t, w, map[string]any{"Items": folders, "TotalRecordCount": len(folders)})
	})
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": []map[string]any{
			{"Id": "u1", "Name": "Quux", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}},
			{"Id": "u2", "Name": "Plugh", "Policy": map[string]any{"IsAdministrator": false, "EnableAllFolders": true}},
		}, "TotalRecordCount": 2})
	})

	return f, libs
}

// lastQuery is the query string of the latest request to a path, parsed.
func lastQuery(t *testing.T, f *fakeServer, path string) url.Values {
	t.Helper()

	reqs := f.requests(path)
	if len(reqs) == 0 {
		t.Fatalf("nothing asked %s", path)
	}
	q, err := url.ParseQuery(reqs[len(reqs)-1].Query)
	if err != nil {
		t.Fatal(err)
	}

	return q
}

// readBody decodes a request's JSON body inside a handler.
func readBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()

	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Errorf("decoding %s %s: %v", r.Method, r.URL.Path, err)
	}

	return m
}

// film is a movie as Emby lists it.
func film(id, name string, year int) map[string]any {
	return map[string]any{"Id": id, "Name": name, "Type": "Movie", "ProductionYear": year, "Path": "/zz/films/" + name + ".mkv"}
}

// page wraps items the way every list route answers.
func page(items ...map[string]any) map[string]any {
	return map[string]any{"Items": items, "TotalRecordCount": len(items)}
}

// library_items with a query passes the search and every filter through as
// the server's own query, scopes to the library named, picks the types from
// the library's kind, leaves the order to the server when none is asked for,
// and resolves a person's name to an id before searching.
func TestLibraryItemsSearchBuildsTheQuery(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Persons", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("SearchTerm") == "Nobody" {
			writeJSON(t, w, page())
			return
		}
		writeJSON(t, w, page(map[string]any{"Id": "p7", "Name": "Zed Zzyzx", "Type": "Person"}))
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		it := film("9", "Zzyzx", 2001)
		it["ProviderIds"] = map[string]string{"Tmdb": "77"}
		it["RunTimeTicks"] = 60 * 600_000_000
		writeJSON(t, w, map[string]any{"Items": []map[string]any{it}, "TotalRecordCount": 3})
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "library_items", map[string]any{"query": "zzy", "library": "zzyzx films", "genres": []string{"Drama"}, "years": []int{2001}, "sort": "added", "desc": true})
	q := lastQuery(t, f, "/Items")
	for k, want := range map[string]string{
		"SearchTerm": "zzy", "IncludeItemTypes": "Movie", "ParentId": "lib9", "Genres": "Drama", "Years": "2001",
		"SortBy": "DateCreated,SortName", "SortOrder": "Descending", "Limit": "25", "Recursive": "true",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if got := number(t, out["total"], "total"); got != 3 {
		t.Errorf("total = %d, want the server's count, 3", got)
	}
	if number(t, out["offset"], "offset") != 0 {
		t.Errorf("offset = %v", out["offset"])
	}
	items := objects(t, out["items"], "items")
	if len(items) != 1 || items[0]["id"] != "9" || items[0]["name"] != "Zzyzx" || number(t, items[0]["year"], "year") != 2001 {
		t.Errorf("items = %v", items)
	}
	if ids := object(t, items[0]["metadata_provider_ids"], "metadata_provider_ids"); ids["tmdb"] != "77" {
		t.Errorf("provider ids are not lowercased: %v", ids)
	}
	if number(t, items[0]["runtime_s"], "runtime_s") != 3600 {
		t.Errorf("runtime_s = %v", items[0]["runtime_s"])
	}

	// a query with no sort is the server's relevance order: no SortBy at all
	f.reset()
	mustCall(t, cs, "library_items", map[string]any{"query": "zzy"})
	if q = lastQuery(t, f, "/Items"); q.Get("SearchTerm") != "zzy" || q.Has("SortBy") || q.Has("SortOrder") {
		t.Errorf("relevance query = %v", q)
	}
	// and no query is by name
	f.reset()
	mustCall(t, cs, "library_items", map[string]any{})
	if q = lastQuery(t, f, "/Items"); q.Has("SearchTerm") || q.Get("SortBy") != "SortName" || q.Get("SortOrder") != "Ascending" {
		t.Errorf("browse query = %v", q)
	}

	// every library: films and series, no parent, and a person by id
	f.reset()
	mustCall(t, cs, "library_items", map[string]any{"query": "x", "person": "zed zzyzx", "limit": 2, "offset": 4, "sort": "name"})
	q = lastQuery(t, f, "/Items")
	if q.Get("IncludeItemTypes") != "Movie,Series" || q.Has("ParentId") || q.Get("PersonIds") != "p7" || q.Get("Limit") != "2" || q.Get("StartIndex") != "4" || q.Get("SortOrder") != "Ascending" {
		t.Errorf("unscoped query = %v", q)
	}
	if pq := lastQuery(t, f, "/Persons"); pq.Get("SearchTerm") != "zed zzyzx" || pq.Get("Limit") != "50" {
		t.Errorf("person lookup = %v", pq)
	}

	if msg := mustRefuse(t, cs, "library_items", map[string]any{"library": "Nope"}); !strings.Contains(msg, `no library named "Nope"`) || !strings.Contains(msg, "Zzyzx Films") {
		t.Errorf("unknown library: %s", msg)
	}
	// a part of a name matches nobody exactly, and points at person_get
	if msg := mustRefuse(t, cs, "library_items", map[string]any{"person": "Zed"}); !strings.Contains(msg, `no person named "Zed"`) || !strings.Contains(msg, "person_get") {
		t.Errorf("a partial person: %s", msg)
	}
	if msg := mustRefuse(t, cs, "library_items", map[string]any{"person": "Nobody"}); !strings.Contains(msg, `no person named "Nobody"`) {
		t.Errorf("unknown person: %s", msg)
	}
	if msg := mustRefuse(t, cs, "library_items", map[string]any{"sort": "colour"}); !strings.Contains(msg, `unknown sort "colour"`) {
		t.Errorf("unknown sort: %s", msg)
	}
}

// library_get counts the library's items with its folders and collections
// left out, then each primary type, reporting only the types it holds.
func TestLibraryGetCounts(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		n := 0
		switch {
		case q.Has("ExcludeItemTypes"):
			n = 42
		case q.Get("IncludeItemTypes") == "Movie":
			n = 40
		case q.Get("IncludeItemTypes") == "Episode":
			n = 2
		}
		writeJSON(t, w, map[string]any{"Items": []any{}, "TotalRecordCount": n})
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "library_get", map[string]any{"library": "lib9"})
	if out["id"] != "lib9" || out["name"] != "Zzyzx Films" || out["collection_type"] != "movies" || boolean(t, out["saves_nfo"], "saves_nfo") {
		t.Errorf("library facts = %v", out)
	}
	if got := number(t, out["item_count"], "item_count"); got != 42 {
		t.Errorf("item_count = %d, want 42", got)
	}
	counts := object(t, out["type_counts"], "type_counts")
	if number(t, counts["Movie"], "Movie") != 40 || number(t, counts["Episode"], "Episode") != 2 {
		t.Errorf("type_counts = %v", counts)
	}
	if _, ok := counts["Series"]; ok {
		t.Error("a type with no items is listed")
	}
	for _, r := range f.requests("/Items") {
		q, _ := url.ParseQuery(r.Query)
		if q.Get("ParentId") != "lib9" || q.Get("Limit") != "1" {
			t.Errorf("a count asked for more than the total: %s", r.Query)
		}
		if q.Has("ExcludeItemTypes") && q.Get("ExcludeItemTypes") != containerTypes {
			t.Errorf("ExcludeItemTypes = %s", q.Get("ExcludeItemTypes"))
		}
	}

	if msg := mustRefuse(t, cs, "library_get", map[string]any{"library": ""}); !strings.Contains(msg, "library name is required") {
		t.Errorf("no library: %s", msg)
	}
}

// item_find_by_metadata_id asks Emby by provider id, trimmed and lowercased,
// and answers found with every copy.
func TestItemFindByMetadataID(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("AnyProviderIdEquals") != "tmdb.77" {
			writeJSON(t, w, page())
			return
		}
		a, b := film("9", "Zzyzx", 2001), film("10", "Zzyzx", 2001)
		b["Path"] = "/zz/more/Zzyzx.mkv"
		writeJSON(t, w, page(a, b))
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "item_find_by_metadata_id", map[string]any{"metadata_provider": " TMDB ", "id": " 77 "})
	if !boolean(t, out["found"], "found") {
		t.Error("not found")
	}
	if items := objects(t, out["items"], "items"); len(items) != 2 || items[0]["id"] != "9" || items[1]["path"] != "/zz/more/Zzyzx.mkv" {
		t.Errorf("items = %v", items)
	}
	if q := lastQuery(t, f, "/Items"); q.Get("Recursive") != "true" {
		t.Errorf("query = %v", q)
	}

	out = mustCall(t, cs, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tvdb", "id": "1"})
	if boolean(t, out["found"], "found") {
		t.Error("found what the server did not have")
	}
	if items, ok := out["items"].([]any); ok && len(items) > 0 {
		t.Errorf("items = %v", items)
	}
}

// library_recent asks newest first and keeps only what was added inside the
// window, which the server cannot filter on.
func TestLibraryRecentKeepsTheWindow(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		fresh, stale := film("9", "Zzyzx", 2001), film("8", "Xyzzy", 1999)
		fresh["DateCreated"] = time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
		stale["DateCreated"] = time.Now().Add(-100 * 24 * time.Hour).UTC().Format(time.RFC3339)
		writeJSON(t, w, page(fresh, stale))
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "library_recent", map[string]any{"library": "Zzyzx Films"})
	q := lastQuery(t, f, "/Items")
	if q.Get("SortBy") != "DateCreated" || q.Get("SortOrder") != "Descending" || q.Get("IncludeItemTypes") != "Movie,Series,Episode" || q.Get("Limit") != "25" || q.Get("ParentId") != "lib9" {
		t.Errorf("query = %v", q)
	}
	items := objects(t, out["items"], "items")
	if len(items) != 1 || items[0]["id"] != "9" || text(items[0]["added"]) == "" {
		t.Errorf("items = %v, want the one inside 60 days", items)
	}

	out = mustCall(t, cs, "library_recent", map[string]any{"days": 200, "types": "Movie", "limit": 5})
	if q = lastQuery(t, f, "/Items"); q.Get("IncludeItemTypes") != "Movie" || q.Get("Limit") != "5" || q.Has("ParentId") {
		t.Errorf("query = %v", q)
	}
	if items = objects(t, out["items"], "items"); len(items) != 2 {
		t.Errorf("items = %v, want both inside 200 days", items)
	}

	// a page is capped where the bulk reads cap theirs
	mustCall(t, cs, "library_recent", map[string]any{"limit": 50000})
	if q = lastQuery(t, f, "/Items"); q.Get("Limit") != "1000" {
		t.Errorf("limit 50000 asked for %s, want the cap of 1000", q.Get("Limit"))
	}
}

// library_filters counts every value the items carry, most used first,
// reading Emby's tags from TagItems, with the years oldest first.
func TestLibraryFiltersCounts(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		a, b, c := film("1", "A", 1999), film("2", "B", 2010), film("3", "C", 1999)
		a["Genres"], a["TagItems"], a["Studios"], a["OfficialRating"] = []string{"Drama", "Crime"}, []map[string]any{{"Name": "heist"}}, []map[string]any{{"Name": "Zzyzx Pictures"}}, "R"
		b["Genres"], b["Studios"], b["OfficialRating"] = []string{"Drama"}, []map[string]any{{"Name": "Zzyzx Pictures"}}, "PG"
		c["Genres"] = []string{"Drama"}
		writeJSON(t, w, page(a, b, c))
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "library_filters", map[string]any{"library": "Zzyzx Films"})
	if q := lastQuery(t, f, "/Items"); q.Get("IncludeItemTypes") != "Movie,Series" || q.Get("ParentId") != "lib9" || q.Get("Fields") != embyfin.FieldsVocabulary {
		t.Errorf("query = %v", q)
	}
	if number(t, out["items_scanned"], "items_scanned") != 3 {
		t.Errorf("items_scanned = %v", out["items_scanned"])
	}
	counts := func(field string) string {
		rows := make([]string, 0, len(objects(t, out[field], field)))
		for _, r := range objects(t, out[field], field) {
			rows = append(rows, fmt.Sprintf("%s=%d", text(r["value"]), number(t, r["items"], "items")))
		}
		return strings.Join(rows, ",")
	}
	if got := counts("genres"); got != "Drama=3,Crime=1" {
		t.Errorf("genres = %s", got)
	}
	if got := counts("tags"); got != "heist=1" {
		t.Errorf("tags = %s", got)
	}
	if got := counts("studios"); got != "Zzyzx Pictures=2" {
		t.Errorf("studios = %s", got)
	}
	if got := counts("official_ratings"); got != "PG=1,R=1" {
		t.Errorf("official_ratings = %s", got)
	}
	years := objects(t, out["years"], "years")
	if len(years) != 2 || number(t, years[0]["year"], "year") != 1999 || number(t, years[0]["items"], "items") != 2 || number(t, years[1]["year"], "year") != 2010 {
		t.Errorf("years = %v", years)
	}
}

// library_items with watched=favourite reads the user's own view with the
// favourite filter, and finds the user by name.
func TestLibraryItemsFavourites(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Users/{user}/Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(film("9", "Zzyzx", 2001)))
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "library_items", map[string]any{"watched": "favourite", "user": "plugh"})
	q := lastQuery(t, f, "/Users/u2/Items")
	if q.Get("Filters") != "IsFavorite" || q.Get("EnableUserData") != "true" || q.Get("Limit") != "25" || q.Get("Recursive") != "true" {
		t.Errorf("query = %v", q)
	}
	if number(t, out["total"], "total") != 1 {
		t.Errorf("total = %v", out["total"])
	}
	if favs := objects(t, out["items"], "items"); len(favs) != 1 || favs[0]["name"] != "Zzyzx" {
		t.Errorf("items = %v", favs)
	}

	// no user named is the first administrator
	mustCall(t, cs, "library_items", map[string]any{"watched": "favourite", "limit": 3})
	if q = lastQuery(t, f, "/Users/u1/Items"); q.Get("Limit") != "3" || q.Get("Filters") != "IsFavorite" {
		t.Errorf("query = %v", q)
	}
	if msg := mustRefuse(t, cs, "library_items", map[string]any{"watched": "favourite", "user": "nobody"}); !strings.Contains(msg, `no user named "nobody"`) || !strings.Contains(msg, "Quux") {
		t.Errorf("unknown user: %s", msg)
	}
	if msg := mustRefuse(t, cs, "library_items", map[string]any{"watched": "loved"}); !strings.Contains(msg, `unknown watched "loved"`) {
		t.Errorf("unknown watched: %s", msg)
	}
}

// user_history reads the user's playback events out of the activity log by
// the name prefix Emby writes, keeps the latest event per item, and reads
// the items back in one query.
func TestUserHistory(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /System/ActivityLog/Entries", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": []map[string]any{
			{"Name": "Quux has finished playing Zzyzx", "Type": "playback.stop", "Date": "2026-09-20T10:00:00Z", "ItemId": "9"},
			{"Name": "Quux has started playing Zzyzx", "Type": "playback.start", "Date": "2026-09-20T09:00:00Z", "ItemId": "9"},
			{"Name": "Plugh has finished playing Xyzzy", "Type": "playback.stop", "Date": "2026-09-19T10:00:00Z", "ItemId": "8"},
			{"Name": "Quux has started playing Xyzzy", "Type": "playback.start", "Date": "2026-09-18T10:00:00Z", "ItemId": "8"},
			{"Name": "Quux logged in", "Type": "login", "Date": "2026-09-17T10:00:00Z"},
		}, "TotalRecordCount": 5})
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		var items []map[string]any
		for id := range strings.SplitSeq(r.URL.Query().Get("Ids"), ",") {
			switch id {
			case "9":
				items = append(items, film("9", "Zzyzx", 2001))
			case "8":
				items = append(items, film("8", "Xyzzy", 1999))
			}
		}
		writeJSON(t, w, page(items...))
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "user_history", map[string]any{})
	if q := lastQuery(t, f, "/Items"); q.Get("Ids") != "9,8" || q.Get("Limit") != "2" {
		t.Errorf("items query = %v", q)
	}
	if out["user"] != "Quux" || number(t, out["total"], "total") != 2 || number(t, out["offset"], "offset") != 0 {
		t.Errorf("user, total, offset = %v %v %v", out["user"], out["total"], out["offset"])
	}
	items := objects(t, out["items"], "items")
	if len(items) != 2 || items[0]["id"] != "9" || items[0]["event"] != "stop" || items[0]["last_played"] != "2026-09-20T10:00:00Z" || items[1]["id"] != "8" || items[1]["event"] != "start" {
		t.Errorf("items = %v, want each item once at its latest event", items)
	}

	// a page is cut from the distinct items, and the total is all of them
	out = mustCall(t, cs, "user_history", map[string]any{"limit": 1})
	if items = objects(t, out["items"], "items"); len(items) != 1 || items[0]["id"] != "9" || number(t, out["total"], "total") != 2 {
		t.Errorf("page 1 = %v", out)
	}
	out = mustCall(t, cs, "user_history", map[string]any{"limit": 1, "offset": 1})
	if items = objects(t, out["items"], "items"); len(items) != 1 || items[0]["id"] != "8" || number(t, out["offset"], "offset") != 1 {
		t.Errorf("page 2 = %v", out)
	}
	if q := lastQuery(t, f, "/Items"); q.Get("Ids") != "8" {
		t.Errorf("page 2 read %v, want the second item alone", q)
	}
	out = mustCall(t, cs, "user_history", map[string]any{"offset": 5})
	if items = objects(t, out["items"], "items"); len(items) != 0 || number(t, out["total"], "total") != 2 {
		t.Errorf("a page past the end = %v", out)
	}
	if items = objects(t, mustCall(t, cs, "user_history", map[string]any{"user": "Plugh"})["items"], "items"); len(items) != 1 || items[0]["id"] != "8" || items[0]["event"] != "stop" {
		t.Errorf("Plugh's history = %v", items)
	}
}

// itemsByID answers an /Items lookup by id with the films asked for, however
// the server spells the ids: Emby joins them, Jellyfin repeats the parameter.
func itemsByID(t *testing.T, films map[string]string) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		asked := q["ids"]
		if ids := q.Get("Ids"); ids != "" {
			asked = strings.Split(ids, ",")
		}
		var items []map[string]any
		for _, id := range asked {
			if name, ok := films[id]; ok {
				items = append(items, film(id, name, 2001))
			}
		}
		writeJSON(t, w, page(items...))
	}
}

// user_history puts each play down to the user who played it. Emby's entries
// say who only in their text, so a name that starts another's ("Bob" and
// "Bob Smith") goes to the longest name that fits; Jellyfin's carry the
// user's id, which decides whatever the text says, and an entry with the
// empty id falls back to the text.
func TestUserHistoryPutsEachPlayDownToWhoPlayedIt(t *testing.T) {
	t.Parallel()

	for _, jellyfin := range []bool{false, true} {
		f := newFakeServer(t)
		f.jellyfin = jellyfin
		users := []map[string]any{
			{"Id": "0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a01", "Name": "Bob", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}},
			{"Id": "0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a02", "Name": "Bob Smith", "Policy": map[string]any{"EnableAllFolders": true}},
		}
		entries := []map[string]any{
			{"Name": "Bob Smith has finished playing Zzyzx", "Type": "playback.stop", "Date": "2026-09-20T10:00:00Z", "ItemId": "9", "UserId": "7"},
			{"Name": "Bob has finished playing Xyzzy", "Type": "playback.stop", "Date": "2026-09-19T10:00:00Z", "ItemId": "8", "UserId": "6"},
		}
		if jellyfin {
			entries = []map[string]any{
				// a server writing its log in another language: the id decides
				{"Name": "Lecture de Zzyzx par Bob Smith", "Type": "VideoPlaybackStopped", "Date": "2026-09-20T10:00:00Z", "ItemId": "9", "UserId": "0a0a0a0a-0a0a-0a0a-0a0a-0a0a0a0a0a02"},
				{"Name": "Bob is playing Xyzzy", "Type": "VideoPlayback", "Date": "2026-09-19T10:00:00Z", "ItemId": "8", "UserId": "00000000-0000-0000-0000-000000000000"},
			}
			f.mux.HandleFunc("GET /Users", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, users) })
		} else {
			f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, map[string]any{"Items": users, "TotalRecordCount": len(users)})
			})
		}
		f.mux.HandleFunc("GET /System/ActivityLog/Entries", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, map[string]any{"Items": entries, "TotalRecordCount": len(entries)})
		})
		f.mux.HandleFunc("GET /Items", itemsByID(t, map[string]string{"9": "Zzyzx", "8": "Xyzzy"}))
		cs := session(t, f, Options{})

		for user, want := range map[string]string{"Bob": "8", "Bob Smith": "9"} {
			out := mustCall(t, cs, "user_history", map[string]any{"user": user})
			items := objects(t, out["items"], "items")
			if len(items) != 1 || items[0]["id"] != want {
				t.Errorf("jellyfin %v: %s's history = %v, want only item %s", jellyfin, user, items, want)
			}
		}
	}
}

// activityLog is a canned activity log of n entries, newest first, answered
// a page at a time the way both servers page it. entry says what the i-th
// newest is, and entryDate when it was written.
func activityLog(t *testing.T, newest time.Time, n int, entry func(i int) map[string]any) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		start, _ := strconv.Atoi(q.Get("StartIndex") + q.Get("startIndex"))
		limit, _ := strconv.Atoi(q.Get("Limit") + q.Get("limit"))
		items := []map[string]any{}
		for i := start; i < n && i < start+limit; i++ {
			e := entry(i)
			e["Date"] = entryDate(newest, i)
			items = append(items, e)
		}
		writeJSON(t, w, map[string]any{"Items": items, "TotalRecordCount": n})
	}
}

// entryDate is when an activityLog's i-th newest entry was written: a minute
// before the one after it.
func entryDate(newest time.Time, i int) string {
	return newest.Add(-time.Duration(i) * time.Minute).UTC().Format(time.RFC3339)
}

// The history tools read the whole period, however many pages of the log it
// takes: one page of a thousand entries is a few days of a busy server, and
// a film played before them was reported as never played.
func TestTheHistoryToolsReadTheWholePeriod(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /System/ActivityLog/Entries", activityLog(t, time.Now().Add(-time.Hour), 2500, func(i int) map[string]any {
		if i == 2499 { // the oldest in the period
			return map[string]any{"Name": "Quux has finished playing Zzyzx", "Type": "playback.stop", "ItemId": "9"}
		}
		return map[string]any{"Name": "Plugh has finished playing Xyzzy", "Type": "playback.stop", "ItemId": "8"}
	}))
	f.mux.HandleFunc("GET /Items", itemsByID(t, map[string]string{"9": "Zzyzx", "8": "Xyzzy"}))
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "item_watch_history", map[string]any{"id": "9"})
	if got := texts(out["entries"]); len(got) != 1 || !strings.HasSuffix(got[0], "Quux has finished playing Zzyzx") {
		t.Errorf("entries = %v, want the play at the far end of the period", got)
	}
	if !boolean(t, out["complete"], "complete") || out["note"] != nil {
		t.Errorf("complete = %v, note = %v, want the whole period read", out["complete"], out["note"])
	}
	reads := f.requests("/System/ActivityLog/Entries")
	starts := make([]string, 0, len(reads))
	for _, r := range reads {
		q, _ := url.ParseQuery(r.Query)
		starts = append(starts, q.Get("StartIndex"))
	}
	if !slices.Equal(starts, []string{"", "1000", "2000"}) {
		t.Errorf("pages read from %v, want 0, 1000 and 2000", starts)
	}

	out = mustCall(t, cs, "user_history", map[string]any{"user": "Quux"})
	if items := objects(t, out["items"], "items"); len(items) != 1 || items[0]["id"] != "9" || !boolean(t, out["complete"], "complete") {
		t.Errorf("Quux's history = %v", out)
	}
}

// user_history reads a page of its items back a batch at a time: a large
// limit named every id in one request, which a server refuses past a few
// hundred ids.
func TestUserHistoryReadsItsItemsInBatches(t *testing.T) {
	t.Parallel()

	const played = 250
	f, _ := zzyzxServer(t)
	names := map[string]string{}
	for i := range played {
		names[strconv.Itoa(100+i)] = "Zzyzx " + strconv.Itoa(i)
	}
	f.mux.HandleFunc("GET /System/ActivityLog/Entries", activityLog(t, time.Now().Add(-time.Hour), played, func(i int) map[string]any {
		return map[string]any{"Name": "Quux has finished playing Zzyzx", "Type": "playback.stop", "ItemId": strconv.Itoa(100 + i)}
	}))
	f.mux.HandleFunc("GET /Items", itemsByID(t, names))
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "user_history", map[string]any{"user": "Quux", "limit": played})
	if got := len(objects(t, out["items"], "items")); got != played {
		t.Errorf("history has %d items, want all %d", got, played)
	}
	for _, r := range f.requests("/Items") {
		q, _ := url.ParseQuery(r.Query)
		if n := len(strings.Split(q.Get("Ids"), ",")); n > idsPerRequest {
			t.Errorf("one request named %d items, want at most %d", n, idsPerRequest)
		}
	}
}

// A period holding more activity than one call reads is read to the ceiling
// and no further, and the answer says so and how far back it got rather than
// reading as a period with nothing older in it.
func TestTheHistoryToolsSayWhenThePeriodIsCutShort(t *testing.T) {
	t.Parallel()

	const held = activityScanMax + 500
	f, _ := zzyzxServer(t)
	newest := time.Now().Add(-time.Hour)
	f.mux.HandleFunc("GET /System/ActivityLog/Entries", activityLog(t, newest, held, func(i int) map[string]any {
		if i == held-100 { // past the ceiling
			return map[string]any{"Name": "Quux has finished playing Zzyzx", "Type": "playback.stop", "ItemId": "9"}
		}
		return map[string]any{"Name": "Plugh has finished playing Xyzzy", "Type": "playback.stop", "ItemId": "8"}
	}))
	f.mux.HandleFunc("GET /Items", itemsByID(t, map[string]string{"9": "Zzyzx", "8": "Xyzzy"}))
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "item_watch_history", map[string]any{"id": "9"})
	if got := texts(out["entries"]); len(got) != 0 {
		t.Errorf("entries = %v, want nothing from past the ceiling", got)
	}
	if reqs := f.requests("/System/ActivityLog/Entries"); len(reqs) != activityScanMax/activityPage {
		t.Errorf("%d pages read, want %d", len(reqs), activityScanMax/activityPage)
	}
	oldestRead := entryDate(newest, activityScanMax-1)
	note := text(out["note"])
	if boolean(t, out["complete"], "complete") || !strings.Contains(note, fmt.Sprintf("holds %d entries", held)) || !strings.Contains(note, "newest 20000") || !strings.Contains(note, oldestRead) {
		t.Errorf("complete = %v, note = %q, want it cut short at the entry of %s", out["complete"], note, oldestRead)
	}

	out = mustCall(t, cs, "user_history", map[string]any{"user": "Quux"})
	if items := objects(t, out["items"], "items"); len(items) != 0 || boolean(t, out["complete"], "complete") || !strings.Contains(text(out["note"]), oldestRead) {
		t.Errorf("Quux's history = %v, want nothing, and the read said to be cut short", out)
	}
}

// user_next_up asks Emby's legacy next-up and its resume list, and drops
// the resume rows Emby pads with never-started episodes.
func TestUserNextUp(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Shows/NextUp", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(map[string]any{"Id": "e2", "Name": "Two", "Type": "Episode", "SeriesName": "Zzyzx Files", "ParentIndexNumber": 1, "IndexNumber": 2}))
	})
	f.mux.HandleFunc("GET /Users/{user}/Items/Resume", func(w http.ResponseWriter, _ *http.Request) {
		started, padded := film("9", "Zzyzx", 2001), film("8", "Xyzzy", 1999)
		started["UserData"] = map[string]any{"PlaybackPositionTicks": 300_000_000}
		padded["UserData"] = map[string]any{"PlaybackPositionTicks": 0}
		writeJSON(t, w, page(started, padded))
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "user_next_up", map[string]any{})
	if q := lastQuery(t, f, "/Shows/NextUp"); q.Get("UserId") != "u1" || q.Get("LegacyNextUp") != "true" || q.Get("Limit") != "15" {
		t.Errorf("next up query = %v", q)
	}
	if q := lastQuery(t, f, "/Users/u1/Items/Resume"); q.Get("Recursive") != "true" || q.Get("MediaTypes") != "Video" || q.Get("EnableUserData") != "true" || q.Get("Limit") != "15" {
		t.Errorf("resume query = %v", q)
	}
	if out["user"] != "Quux" {
		t.Errorf("user = %v", out["user"])
	}
	next := objects(t, out["next_up"], "next_up")
	if len(next) != 1 || next[0]["series"] != "Zzyzx Files" || number(t, next[0]["season"], "season") != 1 || number(t, next[0]["episode"], "episode") != 2 {
		t.Errorf("next_up = %v", next)
	}
	if resume := objects(t, out["resume"], "resume"); len(resume) != 1 || resume[0]["id"] != "9" {
		t.Errorf("resume = %v, want only the item with a position", resume)
	}
}

// user_stats walks the user's view once, unfiltered, and sorts each item by
// its watch state: played is watched, a resume point is in progress, a
// favourite is a favourite, and an item with no state is nothing. It counts a
// film once however many copies share a provider id, and reads the series
// watched back for their genres and whether any episode is left.
func TestUserStats(t *testing.T) {
	t.Parallel()

	const hour = 36_000_000_000
	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Users/{user}/Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("IncludeItemTypes") == "Series" {
			writeJSON(t, w, page(map[string]any{"Id": "s1", "Name": "Zzyzx Files", "Type": "Series", "Genres": []string{"Mystery"}, "UserData": map[string]any{"UnplayedItemCount": 0}}))
			return
		}
		a, dup := film("9", "Zzyzx", 2001), film("10", "Zzyzx", 2001)
		a["ProviderIds"], a["Genres"], a["RunTimeTicks"], a["UserData"] = map[string]string{"Tmdb": "77"}, []string{"Drama"}, 2*hour, map[string]any{"Played": true, "PlayCount": 2, "IsFavorite": true}
		dup["ProviderIds"], dup["RunTimeTicks"], dup["UserData"] = map[string]string{"Tmdb": "77"}, 2*hour, map[string]any{"Played": true, "IsFavorite": true}
		e1 := map[string]any{"Id": "e1", "Name": "Pilot", "Type": "Episode", "SeriesId": "s1", "SeriesName": "Zzyzx Files", "RunTimeTicks": hour, "UserData": map[string]any{"Played": true, "PlayCount": 1}}
		e2 := map[string]any{"Id": "e2", "Name": "Two", "Type": "Episode", "SeriesId": "s1", "SeriesName": "Zzyzx Files", "RunTimeTicks": hour, "UserData": map[string]any{"Played": true}}
		// part way through, favourited but unwatched, and never touched
		halfway, loved, untouched := film("11", "Xyzzy", 1999), film("12", "Plugh", 1998), film("13", "Plover", 1997)
		halfway["RunTimeTicks"], halfway["UserData"] = 2*hour, map[string]any{"Played": false, "PlaybackPositionTicks": hour}
		loved["RunTimeTicks"], loved["UserData"] = 2*hour, map[string]any{"Played": false, "IsFavorite": true}
		untouched["RunTimeTicks"] = 2 * hour
		writeJSON(t, w, page(a, dup, e1, e2, halfway, loved, untouched))
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "user_stats", map[string]any{})
	reqs := f.requests("/Users/u1/Items")
	if len(reqs) != 2 {
		t.Fatalf("requests = %v, want one sweep then the series", reqs)
	}
	if q, _ := url.ParseQuery(reqs[0].Query); q.Has("Filters") || q.Get("EnableUserData") != "true" || q.Get("IncludeItemTypes") != "Movie,Episode" || q.Get("Fields") != "Path,Genres,ProviderIds" {
		t.Errorf("sweep query = %v, want every item with its state, unfiltered", q)
	}
	if q, _ := url.ParseQuery(reqs[1].Query); q.Get("Ids") != "s1" || q.Get("IncludeItemTypes") != "Series" {
		t.Errorf("series query = %v", q)
	}
	if out["user"] != "Quux" || number(t, out["movies_watched"], "movies_watched") != 1 || number(t, out["episodes_watched"], "episodes_watched") != 2 {
		t.Errorf("counts = %v", out)
	}
	if number(t, out["in_progress"], "in_progress") != 1 || number(t, out["favourites"], "favourites") != 2 {
		t.Errorf("in_progress = %v favourites = %v, want 1 and 2 (the copy once)", out["in_progress"], out["favourites"])
	}
	if got := decimal(t, out["hours_watched"], "hours_watched"); got != 4 {
		t.Errorf("hours_watched = %v, want 4 (the copy once, and nothing unwatched)", got)
	}
	if number(t, out["series_started"], "series_started") != 1 || number(t, out["series_finished"], "series_finished") != 1 {
		t.Errorf("series = %v", out)
	}
	genres := objects(t, out["top_genres"], "top_genres")
	if len(genres) != 2 || genres[0]["value"] != "Drama" || genres[1]["value"] != "Mystery" {
		t.Errorf("top_genres = %v", genres)
	}
	series := objects(t, out["top_series"], "top_series")
	if len(series) != 1 || series[0]["name"] != "Zzyzx Files" || number(t, series[0]["episodes_watched"], "episodes_watched") != 2 || !boolean(t, series[0]["finished"], "finished") {
		t.Errorf("top_series = %v", series)
	}
	plays := objects(t, out["most_played"], "most_played")
	if len(plays) != 2 || plays[0]["name"] != "Zzyzx" || number(t, plays[0]["plays"], "plays") != 2 || plays[1]["name"] != "Zzyzx Files: Pilot" || plays[1]["type"] != "Episode" {
		t.Errorf("most_played = %v", plays)
	}
}

// user_stats reads the series a user has started back a hundred at a time:
// Jellyfin takes each id as a parameter of its own, and a few hundred in one
// request run past the length a server or proxy accepts.
func TestUserStatsReadsTheSeriesInBatches(t *testing.T) {
	t.Parallel()

	const started = 250
	f := newFakeServer(t)
	f.jellyfin = true
	f.mux.HandleFunc("GET /Users", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{{"Id": "u1", "Name": "Quux", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}}})
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var items []map[string]any
		if slices.Equal(q["includeItemTypes"], []string{"Series"}) {
			for _, id := range q["ids"] {
				items = append(items, map[string]any{"Id": id, "Name": "Zzyzx " + id, "Type": "Series", "UserData": map[string]any{"UnplayedItemCount": 1}})
			}
			writeJSON(t, w, page(items...))
			return
		}
		for i := range started {
			id := strconv.Itoa(i)
			items = append(items, map[string]any{"Id": "e" + id, "Name": "Pilot", "Type": "Episode", "SeriesId": "s" + id, "SeriesName": "Zzyzx s" + id, "UserData": map[string]any{"Played": true}})
		}
		writeJSON(t, w, page(items...))
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "user_stats", map[string]any{})
	if got := number(t, out["series_started"], "series_started"); got != started {
		t.Errorf("series_started = %d, want %d", got, started)
	}
	if got := len(objects(t, out["top_series"], "top_series")); got != 10 {
		t.Errorf("top_series has %d rows, want 10", got)
	}
	asked := map[string]bool{}
	batches := 0
	for _, r := range f.requests("/Items") {
		q, _ := url.ParseQuery(r.Query)
		if !slices.Equal(q["includeItemTypes"], []string{"Series"}) {
			continue
		}
		batches++
		if len(q["ids"]) > idsPerRequest {
			t.Errorf("one request named %d series, want at most %d", len(q["ids"]), idsPerRequest)
		}
		for _, id := range q["ids"] {
			asked[id] = true
		}
	}
	if batches != 3 || len(asked) != started {
		t.Errorf("%d requests asked for %d series, want 3 asking for all %d", batches, len(asked), started)
	}
}

// item_set_state sets any of a user's three states on an item in one call,
// favourite then position then watched, for a user who can see it, and
// answers with the item's name and what was set. It refuses a call that sets
// nothing, a position at or below zero, and a position on an item marked
// watched in the same call.
func TestItemSetState(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("Ids") != "9" {
			writeJSON(t, w, page())
			return
		}
		writeJSON(t, w, page(film("9", "Zzyzx", 2001)))
	})
	f.mux.HandleFunc("GET /Users/{user}/Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(film("9", "Zzyzx", 2001)))
	})
	f.mux.HandleFunc("GET /Users/{user}/Items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, film("9", "Zzyzx", 2001))
	})
	var mu sync.Mutex
	var order []string
	var progress map[string]any
	record := func(what string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			order = append(order, what)
			mu.Unlock()
			_, _ = io.WriteString(w, `{}`)
		}
	}
	f.mux.HandleFunc("POST /Users/{user}/FavoriteItems/{id}", record("favourite"))
	f.mux.HandleFunc("DELETE /Users/{user}/FavoriteItems/{id}", record("unfavourite"))
	f.mux.HandleFunc("POST /Users/{user}/PlayedItems/{id}", record("played"))
	f.mux.HandleFunc("DELETE /Users/{user}/PlayedItems/{id}", record("unplayed"))
	f.mux.HandleFunc("POST /Users/{user}/Items/{id}/UserData", func(w http.ResponseWriter, r *http.Request) {
		progress = readBody(t, r)
		mu.Lock()
		order = append(order, "position")
		mu.Unlock()
		_, _ = io.WriteString(w, `{}`)
	})
	cs := session(t, f, Options{})

	for _, tc := range []struct{ args, want string }{
		{`{"id":"9"}`, "nothing to set"},
		{`{"id":"9","position_s":0}`, "position_s must be above zero"},
		{`{"id":"9","position_s":-5}`, "position_s must be above zero"},
		{`{"id":"9","position_s":60,"watched":true}`, "contradict"},
	} {
		var args map[string]any
		_ = json.Unmarshal([]byte(tc.args), &args)
		if msg := mustRefuse(t, cs, "item_set_state", args); !strings.Contains(msg, tc.want) {
			t.Errorf("item_set_state %s = %q, want %q", tc.args, msg, tc.want)
		}
	}
	if n := len(f.requests("/Users/u1/PlayedItems/9")) + len(f.requests("/Users/u1/FavoriteItems/9")) + len(f.requests("/Users/u1/Items/9/UserData")); n != 0 {
		t.Errorf("a refused call sent %d changes", n)
	}

	// all three at once, for a named user: the favourite, then the position,
	// which already marks the item not yet watched, so watched false sends
	// nothing more (an unplayed mark would clear the point just set)
	out := mustCall(t, cs, "item_set_state", map[string]any{"id": "9", "user": "plugh", "favourite": true, "position_s": 90, "watched": false})
	if out["item"] != "Zzyzx" || out["user"] != "Plugh" || !boolean(t, out["favourite"], "favourite") || boolean(t, out["watched"], "watched") || number(t, out["position_s"], "position_s") != 90 {
		t.Errorf("set = %v", out)
	}
	mu.Lock()
	if !slices.Equal(order, []string{"favourite", "position"}) {
		t.Errorf("changes sent = %v, want the favourite then the position", order)
	}
	if played, ok := progress["Played"].(bool); number(t, progress["PlaybackPositionTicks"], "PlaybackPositionTicks") != 900_000_000 || !ok || played {
		t.Errorf("position body = %v, want the ticks and Played false", progress)
	}
	order = nil
	mu.Unlock()
	for path, want := range map[string]int{"/Users/u2/FavoriteItems/9": 1, "/Users/u2/Items/9/UserData": 1, "/Users/u2/PlayedItems/9": 0} {
		if got := len(f.requests(path)); got != want {
			t.Errorf("%s was asked %d times for Plugh, want %d", path, got, want)
		}
	}

	// one at a time answers only what was set, and watched false on its own
	// does clear the point
	out = mustCall(t, cs, "item_set_state", map[string]any{"id": "9", "watched": true})
	if _, ok := out["favourite"]; ok || out["user"] != "Quux" || !boolean(t, out["watched"], "watched") {
		t.Errorf("watched alone = %v", out)
	}
	out = mustCall(t, cs, "item_set_state", map[string]any{"id": "9", "favourite": false})
	if _, ok := out["watched"]; ok || boolean(t, out["favourite"], "favourite") {
		t.Errorf("favourite alone = %v", out)
	}
	mustCall(t, cs, "item_set_state", map[string]any{"id": "9", "watched": false})
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(order, []string{"played", "unfavourite", "unplayed"}) {
		t.Errorf("changes sent = %v", order)
	}
}

// collectionState is a canned Emby's collections: the boxset list, each
// one's members, and the item edit and delete routes the tools use on them.
type collectionState struct {
	mu      sync.Mutex
	members map[string][]string // collection id -> item ids
	names   map[string]string
	edited  map[string]any // the last item body posted
	deleted []string
}

func newCollectionServer(t *testing.T) (*fakeServer, *collectionState) {
	t.Helper()

	f, _ := zzyzxServer(t)
	s := &collectionState{members: map[string][]string{}, names: map[string]string{}}
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		q := r.URL.Query()
		var items []map[string]any
		switch {
		case q.Get("ParentId") != "":
			for _, id := range s.members[q.Get("ParentId")] {
				items = append(items, film(id, "Film "+id, 2000))
			}
		case q.Get("IncludeItemTypes") == "BoxSet":
			for id, name := range s.names {
				items = append(items, map[string]any{"Id": id, "Name": name, "Type": "BoxSet"})
			}
		}
		writeJSON(t, w, page(items...))
	})
	f.mux.HandleFunc("POST /Collections", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		q := r.URL.Query()
		s.names["c1"] = q.Get("Name")
		s.members["c1"] = strings.Split(q.Get("Ids"), ",")
		writeJSON(t, w, map[string]any{"Id": "c1", "Name": q.Get("Name")})
	})
	f.mux.HandleFunc("POST /Collections/{id}/Items", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := r.PathValue("id")
		s.members[id] = append(s.members[id], strings.Split(r.URL.Query().Get("Ids"), ",")...)
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("DELETE /Collections/{id}/Items", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := r.PathValue("id")
		gone := strings.Split(r.URL.Query().Get("Ids"), ",")
		s.members[id] = slices.DeleteFunc(s.members[id], func(m string) bool { return slices.Contains(gone, m) })
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("GET /Users/{user}/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := r.PathValue("id")
		writeJSON(t, w, map[string]any{"Id": id, "Name": s.names[id], "Type": "BoxSet"})
	})
	f.mux.HandleFunc("POST /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.edited = body
		s.names[r.PathValue("id")] = text(body["Name"])
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("DELETE /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := r.PathValue("id")
		s.deleted = append(s.deleted, id)
		delete(s.names, id)
		w.WriteHeader(http.StatusNoContent)
	})

	return f, s
}

// The collection family against Emby's routes: create refuses a taken name
// and an empty set, add counts only what is new, remove refuses an item not
// held, edit posts the item back with the sort name in both fields, and
// delete goes straight to the item delete route with no confirm.
func TestCollectionFamily(t *testing.T) {
	t.Parallel()

	f, s := newCollectionServer(t)
	cs := session(t, f, Options{EnableDelete: true})

	if cols := objects(t, mustCall(t, cs, "collection_list", map[string]any{})["collections"], "collections"); len(cols) != 0 {
		t.Errorf("collections before any = %v", cols)
	}
	if msg := mustRefuse(t, cs, "collection_create", map[string]any{"name": "Zzyzx Saga"}); !strings.Contains(msg, "at least one item") {
		t.Errorf("empty create: %s", msg)
	}
	out := mustCall(t, cs, "collection_create", map[string]any{"name": "Zzyzx Saga", "item_ids": []string{"9"}})
	if out["id"] != "c1" || out["name"] != "Zzyzx Saga" {
		t.Errorf("create = %v", out)
	}
	if q := lastQuery(t, f, "/Collections"); q.Get("Name") != "Zzyzx Saga" || q.Get("Ids") != "9" {
		t.Errorf("create query = %v", q)
	}
	if msg := mustRefuse(t, cs, "collection_create", map[string]any{"name": "zzyzx saga", "item_ids": []string{"11"}}); !strings.Contains(msg, "id c1") || !strings.Contains(msg, "collection_add") {
		t.Errorf("a taken name: %s", msg)
	}
	cols := objects(t, mustCall(t, cs, "collection_list", map[string]any{})["collections"], "collections")
	if len(cols) != 1 || cols[0]["id"] != "c1" || cols[0]["name"] != "Zzyzx Saga" {
		t.Errorf("collections = %v", cols)
	}

	out = mustCall(t, cs, "collection_get", map[string]any{"collection": "c1"})
	if out["name"] != "Zzyzx Saga" {
		t.Errorf("get = %v", out)
	}
	if items := objects(t, out["items"], "items"); len(items) != 1 || items[0]["id"] != "9" {
		t.Errorf("items = %v", items)
	}
	if q := lastQuery(t, f, "/Items"); q.Get("ParentId") != "c1" {
		t.Errorf("get query = %v", q)
	}

	f.reset()
	out = mustCall(t, cs, "collection_add", map[string]any{"collection": "Zzyzx Saga", "item_ids": []string{"9", "11", "11"}})
	if number(t, out["added"], "added") != 1 || number(t, out["already_held"], "already_held") != 2 || out["to"] != "Zzyzx Saga" {
		t.Errorf("add = %v", out)
	}
	if q := lastQuery(t, f, "/Collections/c1/Items"); q.Get("Ids") != "11" {
		t.Errorf("only the new item should be posted: %v", q)
	}
	f.reset()
	if out = mustCall(t, cs, "collection_add", map[string]any{"collection": "c1", "item_ids": []string{"9"}}); number(t, out["added"], "added") != 0 {
		t.Errorf("re-add = %v", out)
	}
	if reqs := f.requests("/Collections/c1/Items"); len(reqs) != 0 {
		t.Errorf("nothing new was still posted: %v", reqs)
	}

	// removed is what left: the members are read before and after
	f.reset()
	out = mustCall(t, cs, "collection_remove", map[string]any{"collection": "c1", "item_ids": []string{"11"}})
	if number(t, out["removed"], "removed") != 1 || out["from"] != "Zzyzx Saga" {
		t.Errorf("remove = %v", out)
	}
	if reqs := f.requests("/Collections/c1/Items"); len(reqs) != 1 || reqs[0].Method != http.MethodDelete || reqs[0].Query != "Ids=11" {
		t.Errorf("remove requests = %v", reqs)
	}
	var memberReads int
	for _, r := range f.requests("/Items") {
		if strings.Contains(r.Query, "ParentId=c1") {
			memberReads++
		}
	}
	if memberReads < 2 {
		t.Errorf("the members were read %d times around the remove, want before and after", memberReads)
	}
	if msg := mustRefuse(t, cs, "collection_remove", map[string]any{"collection": "c1", "item_ids": []string{"11"}}); !strings.Contains(msg, "does not hold item 11") {
		t.Errorf("remove of a stranger: %s", msg)
	}

	if msg := mustRefuse(t, cs, "collection_edit", map[string]any{"collection": "c1"}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("empty edit: %s", msg)
	}
	out = mustCall(t, cs, "collection_edit", map[string]any{"collection": "c1", "name": "Zzyzx Cycle", "sort_name": "Zzyzx 1", "overview": "All of them."})
	if out["name"] != "Zzyzx Cycle" || strings.Join(texts(out["changed"]), ",") != "Name,SortName,Overview" {
		t.Errorf("edit = %v", out)
	}
	s.mu.Lock()
	edited := s.edited
	s.mu.Unlock()
	if edited["Name"] != "Zzyzx Cycle" || edited["SortName"] != "Zzyzx 1" || edited["ForcedSortName"] != "Zzyzx 1" || edited["Overview"] != "All of them." {
		t.Errorf("posted item = %v", edited)
	}

	out = mustCall(t, cs, "collection_delete", map[string]any{"collection": "Zzyzx Cycle"})
	if out["deleted"] != "Zzyzx Cycle" {
		t.Errorf("delete = %v", out)
	}
	s.mu.Lock()
	deleted := s.deleted
	s.mu.Unlock()
	if !slices.Equal(deleted, []string{"c1"}) {
		t.Errorf("deleted = %v", deleted)
	}
	if msg := mustRefuse(t, cs, "collection_get", map[string]any{"collection": "Zzyzx Cycle"}); !strings.Contains(msg, "no collection named") {
		t.Errorf("after delete: %s", msg)
	}
}

// playlistState is a canned Emby's playlists: numbered entries per list,
// the way Emby keeps them.
type playlistState struct {
	mu      sync.Mutex
	names   map[string]string
	entries map[string][]playlistEntry
	next    int
	edited  map[string]any
	deleted []string
}

type playlistEntry struct{ item, entry string }

func newPlaylistServer(t *testing.T) (*fakeServer, *playlistState) {
	t.Helper()

	f, _ := zzyzxServer(t)
	s := &playlistState{names: map[string]string{}, entries: map[string][]playlistEntry{}}
	add := func(id string, items []string) {
		for _, it := range items {
			if it == "" {
				continue
			}
			s.next++
			s.entries[id] = append(s.entries[id], playlistEntry{it, strconv.Itoa(s.next)})
		}
	}
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var items []map[string]any
		if r.URL.Query().Get("IncludeItemTypes") == "Playlist" {
			for id, name := range s.names {
				items = append(items, map[string]any{"Id": id, "Name": name, "Type": "Playlist"})
			}
		}
		writeJSON(t, w, page(items...))
	})
	f.mux.HandleFunc("POST /Playlists", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		q := r.URL.Query()
		s.names["pl1"] = q.Get("Name")
		add("pl1", strings.Split(q.Get("Ids"), ","))
		writeJSON(t, w, map[string]any{"Id": "pl1", "Name": q.Get("Name")})
	})
	f.mux.HandleFunc("GET /Playlists/{id}/Items", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		items := make([]map[string]any, 0, len(s.entries[r.PathValue("id")]))
		for _, e := range s.entries[r.PathValue("id")] {
			it := film(e.item, "Film "+e.item, 2000)
			it["PlaylistItemId"] = e.entry
			items = append(items, it)
		}
		writeJSON(t, w, page(items...))
	})
	f.mux.HandleFunc("POST /Playlists/{id}/Items", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		add(r.PathValue("id"), strings.Split(r.URL.Query().Get("Ids"), ","))
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("DELETE /Playlists/{id}/Items", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := r.PathValue("id")
		gone := strings.Split(r.URL.Query().Get("EntryIds"), ",")
		s.entries[id] = slices.DeleteFunc(s.entries[id], func(e playlistEntry) bool { return slices.Contains(gone, e.entry) })
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("POST /Playlists/{id}/Items/{entry}/Move/{index}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := r.PathValue("id")
		from := slices.IndexFunc(s.entries[id], func(e playlistEntry) bool { return e.entry == r.PathValue("entry") })
		to, _ := strconv.Atoi(r.PathValue("index"))
		if from >= 0 {
			moved := s.entries[id][from]
			s.entries[id] = slices.Insert(slices.Delete(s.entries[id], from, from+1), to, moved)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// what a user sees: every item but 66, which is in a library Plugh (u2)
	// was not given
	f.mux.HandleFunc("GET /Users/{user}/Items", func(w http.ResponseWriter, r *http.Request) {
		var items []map[string]any
		for id := range strings.SplitSeq(r.URL.Query().Get("Ids"), ",") {
			if id != "" && (id != "66" || r.PathValue("user") != "u2") {
				items = append(items, film(id, "Film "+id, 2000))
			}
		}
		writeJSON(t, w, page(items...))
	})
	f.mux.HandleFunc("GET /Users/{user}/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := r.PathValue("id")
		writeJSON(t, w, map[string]any{"Id": id, "Name": s.names[id], "Type": "Playlist"})
	})
	f.mux.HandleFunc("POST /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.edited = body
		s.names[r.PathValue("id")] = text(body["Name"])
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("DELETE /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := r.PathValue("id")
		s.deleted = append(s.deleted, id)
		delete(s.names, id)
		w.WriteHeader(http.StatusNoContent)
	})

	return f, s
}

// The playlist family against Emby's routes: create takes the fields as a
// query for the owner, get lists entries in order with their entry ids, add
// appends and checks the entries stayed, edit moves an entry by its id or
// renames through the item update, remove takes entry ids and refuses one
// the playlist lacks, and delete goes to the item delete route with no
// confirm.
func TestPlaylistFamily(t *testing.T) {
	t.Parallel()

	f, s := newPlaylistServer(t)
	cs := session(t, f, Options{EnableDelete: true})

	out := mustCall(t, cs, "playlist_create", map[string]any{"name": "Zzyzx Night", "item_ids": []string{"9", "11"}, "media_type": "Video"})
	if out["id"] != "pl1" || out["name"] != "Zzyzx Night" {
		t.Errorf("create = %v", out)
	}
	if q := lastQuery(t, f, "/Playlists"); q.Get("Name") != "Zzyzx Night" || q.Get("Ids") != "9,11" || q.Get("MediaType") != "Video" || q.Get("UserId") != "u1" {
		t.Errorf("create query = %v", q)
	}
	if lists := objects(t, mustCall(t, cs, "playlist_list", map[string]any{})["playlists"], "playlists"); len(lists) != 1 || lists[0]["id"] != "pl1" {
		t.Errorf("playlists = %v", lists)
	}

	out = mustCall(t, cs, "playlist_get", map[string]any{"playlist": "zzyzx night", "user": "Plugh"})
	if q := lastQuery(t, f, "/Playlists/pl1/Items"); q.Get("UserId") != "u2" {
		t.Errorf("get query = %v", q)
	}
	entries := objects(t, out["entries"], "entries")
	if out["name"] != "Zzyzx Night" || len(entries) != 2 || entries[0]["id"] != "9" || entries[0]["entry_id"] != "1" || entries[1]["entry_id"] != "2" {
		t.Errorf("get = %v", out)
	}

	// a playlist takes only what its user can see: neither server checks,
	// so a restricted account's playlist was a way into a library it was
	// not given
	f.reset()
	for name, args := range map[string]map[string]any{
		"playlist_add":    {"playlist": "pl1", "item_ids": []string{"13", "66"}, "user": "Plugh"},
		"playlist_create": {"name": "Zzyzx Late", "item_ids": []string{"66"}, "user": "Plugh"},
	} {
		if msg := mustRefuse(t, cs, name, args); !strings.Contains(msg, "Plugh cannot see 66") {
			t.Errorf("%s of an item Plugh cannot see = %q", name, msg)
		}
	}
	for _, r := range f.requests("/Playlists/pl1/Items") {
		if r.Method == http.MethodPost {
			t.Errorf("an item was added for a user who cannot see it: %v", r)
		}
	}
	if reqs := f.requests("/Playlists"); len(reqs) != 0 {
		t.Errorf("a playlist was made of an item its user cannot see: %v", reqs)
	}

	// added is what the playlist gained: the entries are read before and after
	f.reset()
	out = mustCall(t, cs, "playlist_add", map[string]any{"playlist": "pl1", "item_ids": []string{"13"}})
	if number(t, out["added"], "added") != 1 || out["to"] != "Zzyzx Night" {
		t.Errorf("add = %v", out)
	}
	var posted []request
	readBefore, readAfter := false, false
	for _, r := range f.requests("/Playlists/pl1/Items") {
		switch {
		case r.Method == http.MethodPost:
			posted = append(posted, r)
		case len(posted) == 0:
			readBefore = true
		default:
			readAfter = true
		}
	}
	if len(posted) != 1 || !strings.Contains(posted[0].Query, "Ids=13") || !strings.Contains(posted[0].Query, "UserId=u1") {
		t.Errorf("add requests = %v", posted)
	}
	if !readBefore || !readAfter {
		t.Errorf("the entries were read before the add: %v, after: %v; want both", readBefore, readAfter)
	}

	if msg := mustRefuse(t, cs, "playlist_edit", map[string]any{"playlist": "pl1"}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("empty edit: %s", msg)
	}
	if msg := mustRefuse(t, cs, "playlist_edit", map[string]any{"playlist": "pl1", "move_entry_id": "3"}); !strings.Contains(msg, "position is required") {
		t.Errorf("move without position: %s", msg)
	}
	out = mustCall(t, cs, "playlist_edit", map[string]any{"playlist": "pl1", "move_entry_id": "3", "position": 1})
	if strings.Join(texts(out["changed"]), ",") != "moved 3 to 1" {
		t.Errorf("move = %v", out)
	}
	if reqs := f.requests("/Playlists/pl1/Items/3/Move/0"); len(reqs) != 1 || reqs[0].Method != http.MethodPost {
		t.Errorf("move requests = %v", reqs)
	}
	order := make([]string, 0, len(objects(t, out["entries"], "entries")))
	for _, e := range objects(t, out["entries"], "entries") {
		order = append(order, text(e["id"]))
	}
	if !slices.Equal(order, []string{"13", "9", "11"}) {
		t.Errorf("entries after the move = %v", order)
	}
	out = mustCall(t, cs, "playlist_edit", map[string]any{"playlist": "pl1", "name": "Zzyzx Day"})
	if out["name"] != "Zzyzx Day" || strings.Join(texts(out["changed"]), ",") != "renamed Zzyzx Night to Zzyzx Day" {
		t.Errorf("rename = %v", out)
	}
	s.mu.Lock()
	edited := s.edited
	s.mu.Unlock()
	if edited["Name"] != "Zzyzx Day" || edited["Id"] != "pl1" {
		t.Errorf("posted item = %v", edited)
	}

	out = mustCall(t, cs, "playlist_remove", map[string]any{"playlist": "Zzyzx Day", "entry_ids": []string{"2"}})
	if number(t, out["removed"], "removed") != 1 || out["from"] != "Zzyzx Day" {
		t.Errorf("remove = %v", out)
	}
	var deletes []request
	for _, r := range f.requests("/Playlists/pl1/Items") {
		if r.Method == http.MethodDelete {
			deletes = append(deletes, r)
		}
	}
	if len(deletes) != 1 || deletes[0].Query != "EntryIds=2" {
		t.Errorf("remove requests = %v", deletes)
	}
	if msg := mustRefuse(t, cs, "playlist_remove", map[string]any{"playlist": "pl1", "entry_ids": []string{"2"}}); !strings.Contains(msg, "no entry 2") || !strings.Contains(msg, "entry ids are 3, 1") {
		t.Errorf("remove of a gone entry: %s", msg)
	}

	out = mustCall(t, cs, "playlist_delete", map[string]any{"playlist": "pl1"})
	if out["deleted"] != "Zzyzx Day" {
		t.Errorf("delete = %v", out)
	}
	s.mu.Lock()
	deleted := s.deleted
	s.mu.Unlock()
	if !slices.Equal(deleted, []string{"pl1"}) {
		t.Errorf("deleted = %v", deleted)
	}
}

// The session family: list reads what each device plays and where it is,
// and play, command and message find the device by name and post Emby's
// query or body form of each.
func TestSessionFamily(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Sessions", func(w http.ResponseWriter, _ *http.Request) {
		playing := film("9", "Zzyzx", 2001)
		playing["RunTimeTicks"] = 1_200_000_000
		writeJSON(t, w, []map[string]any{
			{"Id": "s1", "UserName": "Quux", "Client": "Zzyzx Player", "DeviceName": "Lounge TV", "NowPlayingItem": playing, "PlayState": map[string]any{"PositionTicks": 600_000_000, "IsPaused": true}},
			{"Id": "s2", "Client": "Web", "DeviceName": "Laptop"},
		})
	})
	var seek map[string]any
	var mu sync.Mutex
	f.mux.HandleFunc("POST /Sessions/{id}/Playing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	f.mux.HandleFunc("POST /Sessions/{id}/Playing/{command}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seek = readBody(t, r)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("POST /Sessions/{id}/Message", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	cs := session(t, f, Options{})

	rows := objects(t, mustCall(t, cs, "session_list", map[string]any{})["sessions"], "sessions")
	if len(rows) != 2 {
		t.Fatalf("sessions = %v", rows)
	}
	if rows[0]["id"] != "s1" || rows[0]["user"] != "Quux" || rows[0]["device"] != "Lounge TV" || rows[0]["app"] != "Zzyzx Player" || rows[0]["now_playing"] != "Zzyzx" || rows[0]["position"] != "1m0s / 2m0s" || !boolean(t, rows[0]["paused"], "paused") {
		t.Errorf("playing row = %v", rows[0])
	}
	if _, ok := rows[1]["now_playing"]; ok || rows[1]["device"] != "Laptop" {
		t.Errorf("idle row = %v", rows[1])
	}

	out := mustCall(t, cs, "session_play", map[string]any{"session": "lounge", "item_ids": []string{"9", "11"}})
	if out["playing_on"] != "Lounge TV" {
		t.Errorf("play = %v", out)
	}
	if q := lastQuery(t, f, "/Sessions/s1/Playing"); q.Get("ItemIds") != "9,11" || q.Get("PlayCommand") != "PlayNow" {
		t.Errorf("play query = %v", q)
	}
	if msg := mustRefuse(t, cs, "session_play", map[string]any{"session": "s1", "item_ids": []string{"abc"}}); !strings.Contains(msg, "not an Emby id") {
		t.Errorf("a non-numeric id on Emby: %s", msg)
	}

	out = mustCall(t, cs, "session_command", map[string]any{"session": "Zzyzx Player", "command": "Seek", "seek_s": 90})
	if out["sent"] != "Seek → Lounge TV" {
		t.Errorf("command = %v", out)
	}
	if reqs := f.requests("/Sessions/s1/Playing/Seek"); len(reqs) != 1 {
		t.Errorf("seek requests = %v", reqs)
	}
	mu.Lock()
	if number(t, seek["SeekPositionTicks"], "SeekPositionTicks") != 900_000_000 {
		t.Errorf("seek body = %v", seek)
	}
	mu.Unlock()

	out = mustCall(t, cs, "session_message", map[string]any{"session": "s2", "text": "Dinner", "timeout_ms": 5000})
	if out["sent_to"] != "Laptop" {
		t.Errorf("message = %v", out)
	}
	if q := lastQuery(t, f, "/Sessions/s2/Message"); q.Get("Text") != "Dinner" || q.Get("Header") != "Message" || q.Get("TimeoutMs") != "5000" {
		t.Errorf("message query = %v", q)
	}
	if msg := mustRefuse(t, cs, "session_message", map[string]any{"session": "kitchen", "text": "x"}); !strings.Contains(msg, `no session matching "kitchen"`) || !strings.Contains(msg, "Lounge TV (Zzyzx Player)") {
		t.Errorf("unknown session: %s", msg)
	}
}

// The server family: stats joins counts, playing sessions and users;
// activity asks since the cutoff and joins the overview onto the name;
// devices join app and version; log picks the newest file and tails it;
// tasks list the last result and run by name.
func TestServerFamily(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items/Counts", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"MovieCount": 40, "SeriesCount": 3, "EpisodeCount": 50, "BoxSetCount": 2})
	})
	f.mux.HandleFunc("GET /Sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{{"Id": "s1", "DeviceName": "Lounge TV", "NowPlayingItem": film("9", "Zzyzx", 2001)}, {"Id": "s2", "DeviceName": "Laptop"}})
	})
	f.mux.HandleFunc("GET /System/ActivityLog/Entries", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": []map[string]any{
			{"Name": "Quux has finished playing Zzyzx", "Type": "playback.stop", "Date": "2026-09-20T10:00:00Z", "Severity": "Info", "ShortOverview": "on Lounge TV", "ItemId": "9"},
			{"Name": "Scan failed", "Type": "TaskFailed", "Date": "2026-09-19T10:00:00Z", "Severity": "Error"},
		}, "TotalRecordCount": 7})
	})
	f.mux.HandleFunc("GET /Devices", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(map[string]any{"Id": "d1", "Name": "Lounge TV", "AppName": "Zzyzx Player", "AppVersion": "1.2", "LastUserName": "Quux", "DateLastActivity": "2026-09-20T10:00:00Z"}))
	})
	f.mux.HandleFunc("GET /System/Logs/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(
			map[string]any{"Name": "old.txt", "Size": 10, "DateModified": "2026-01-01T00:00:00Z"},
			map[string]any{"Name": "embyserver.txt", "Size": 20, "DateModified": "2026-09-20T00:00:00Z"},
		))
	})
	f.mux.HandleFunc("GET /System/Logs/{name}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.PathValue("name")+" one\ntwo\nthree\nfour\n") //nolint:gosec // a canned test server echoing a path it was handed
	})
	f.mux.HandleFunc("GET /ScheduledTasks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{
			{"Id": "t1", "Name": "Scan media library", "Category": "Library", "State": "Idle", "LastExecutionResult": map[string]any{"Status": "Completed", "EndTimeUtc": "2026-09-20T01:00:00Z"}},
			{"Id": "t2", "Name": "Clean cache", "Category": "Maintenance", "State": "Running"},
		})
	})
	f.mux.HandleFunc("POST /ScheduledTasks/Running/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "server_stats", map[string]any{})
	for k, want := range map[string]int{"movies": 40, "series": 3, "episodes": 50, "collections": 2, "active_sessions": 1, "users": 2} {
		if got := number(t, out[k], k); got != want {
			t.Errorf("%s = %d, want %d", k, got, want)
		}
	}
	if _, ok := out["albums"]; ok {
		t.Error("a zero music count is listed")
	}

	out = mustCall(t, cs, "server_activity", map[string]any{"days": 7, "limit": 5})
	q := lastQuery(t, f, "/System/ActivityLog/Entries")
	since, err := time.Parse(time.RFC3339, q.Get("MinDate"))
	if err != nil || time.Since(since) < 6*24*time.Hour || time.Since(since) > 8*24*time.Hour || q.Get("Limit") != "5" {
		t.Errorf("activity query = %v (%v)", q, err)
	}
	if number(t, out["total"], "total") != 7 || number(t, out["offset"], "offset") != 0 {
		t.Errorf("total = %v offset = %v, want the server's count and the page start", out["total"], out["offset"])
	}
	entries := objects(t, out["entries"], "entries")
	if len(entries) != 2 || entries[0]["summary"] != "Quux has finished playing Zzyzx — on Lounge TV" || entries[0]["type"] != "playback.stop" || entries[0]["date"] != "2026-09-20T10:00:00Z" || entries[1]["severity"] != "Error" || entries[1]["summary"] != "Scan failed" {
		t.Errorf("entries = %v", entries)
	}
	// the next page is asked of the server by offset
	out = mustCall(t, cs, "server_activity", map[string]any{"limit": 5, "offset": 5})
	if q = lastQuery(t, f, "/System/ActivityLog/Entries"); q.Get("StartIndex") != "5" || q.Get("Limit") != "5" {
		t.Errorf("page 2 query = %v", q)
	}
	if number(t, out["offset"], "offset") != 5 || number(t, out["total"], "total") != 7 {
		t.Errorf("page 2 = %v", out)
	}

	devices := objects(t, mustCall(t, cs, "server_devices", map[string]any{})["devices"], "devices")
	if len(devices) != 1 || devices[0]["name"] != "Lounge TV" || devices[0]["app"] != "Zzyzx Player 1.2" || devices[0]["last_user"] != "Quux" || devices[0]["last_activity"] != "2026-09-20T10:00:00Z" {
		t.Errorf("devices = %v", devices)
	}

	files := objects(t, mustCall(t, cs, "server_logs", map[string]any{})["files"], "files")
	if len(files) != 2 || files[1]["name"] != "embyserver.txt" || number(t, files[1]["size"], "size") != 20 || files[1]["modified"] != "2026-09-20T00:00:00Z" {
		t.Errorf("files = %v", files)
	}
	out = mustCall(t, cs, "server_log", map[string]any{"lines": 2})
	if out["name"] != "embyserver.txt" || out["tail"] != "three\nfour" {
		t.Errorf("log = %v, want the newest file's last two lines", out)
	}
	out = mustCall(t, cs, "server_log", map[string]any{"name": "old.txt"})
	if out["name"] != "old.txt" || !strings.HasPrefix(text(out["tail"]), "old.txt one\n") {
		t.Errorf("named log = %v", out)
	}

	tasks := objects(t, mustCall(t, cs, "task_list", map[string]any{})["tasks"], "tasks")
	if len(tasks) != 2 || tasks[0]["name"] != "Scan media library" || tasks[0]["category"] != "Library" || tasks[0]["state"] != "Idle" || tasks[0]["last_status"] != "Completed" || tasks[0]["last_run"] != "2026-09-20T01:00:00Z" {
		t.Errorf("tasks = %v", tasks)
	}
	if _, ok := tasks[1]["last_status"]; ok || tasks[1]["state"] != "Running" {
		t.Errorf("a task never run = %v", tasks[1])
	}
	out = mustCall(t, cs, "task_run", map[string]any{"task": "scan MEDIA library"})
	if out["started"] != "Scan media library" {
		t.Errorf("run = %v", out)
	}
	if reqs := f.requests("/ScheduledTasks/Running/t1"); len(reqs) != 1 || reqs[0].Method != http.MethodPost {
		t.Errorf("run requests = %v", reqs)
	}
	if msg := mustRefuse(t, cs, "task_run", map[string]any{"task": "Backup"}); !strings.Contains(msg, `no task named "Backup"`) || !strings.Contains(msg, "Clean cache") {
		t.Errorf("unknown task: %s", msg)
	}
}

// show_seasons names the series and lists its seasons by number, the
// specials as 0 rather than without one.
func TestShowSeasons(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("Ids") != "s1" {
			writeJSON(t, w, page())
			return
		}
		writeJSON(t, w, page(map[string]any{"Id": "s1", "Name": "Zzyzx Files", "Type": "Series"}))
	})
	f.mux.HandleFunc("GET /Shows/{id}/Seasons", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(
			map[string]any{"Id": "se1", "Name": "Season 1", "Type": "Season", "IndexNumber": 1},
			map[string]any{"Id": "se0", "Name": "Specials", "Type": "Season", "IndexNumber": 0},
		))
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "show_seasons", map[string]any{"series_id": "s1"})
	if out["series"] != "Zzyzx Files" {
		t.Errorf("series = %v", out["series"])
	}
	seasons := objects(t, out["seasons"], "seasons")
	if len(seasons) != 2 || seasons[0]["id"] != "se1" || seasons[0]["name"] != "Season 1" || number(t, seasons[0]["season"], "season") != 1 {
		t.Errorf("seasons = %v", seasons)
	}
	if n, ok := seasons[1]["season"]; !ok || number(t, n, "season") != 0 {
		t.Errorf("the specials do not say they are season 0: %v", seasons[1])
	}
	if len(f.requests("/Shows/s1/Seasons")) != 1 {
		t.Errorf("seasons requests = %v", f.seen)
	}
	if msg := mustRefuse(t, cs, "show_seasons", map[string]any{"series_id": "nope"}); !strings.Contains(msg, "no item with id nope") {
		t.Errorf("unknown series: %s", msg)
	}
}

// person_get: a person is found by exact name among the search's answers,
// read for their own facts, and credited once per role on each item, with a
// role that only repeats the job blanked; a part of a name answers with the
// people it could mean, and a name nobody has is an error.
func TestPersonGet(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Persons", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("SearchTerm") == "Nobody" {
			writeJSON(t, w, page())
			return
		}
		writeJSON(t, w, page(map[string]any{"Id": "p7", "Name": "Zed Zzyzx", "Type": "Person"}, map[string]any{"Id": "p8", "Name": "Zed Zzyzx Jr", "Type": "Person"}))
	})
	f.mux.HandleFunc("GET /Persons/{name}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"Id": "p7", "Name": r.PathValue("name"), "Type": "Person", "ProviderIds": map[string]string{"Tmdb": "500"}, "Overview": "Actor.", "PremiereDate": "1970-01-01T00:00:00Z"})
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("PersonIds") != "p7" {
			writeJSON(t, w, page())
			return
		}
		a, b := film("9", "Zzyzx", 2001), film("8", "Xyzzy", 1999)
		a["People"] = []map[string]any{
			{"Name": "Zed Zzyzx", "Id": "p7", "Role": "Actor", "Type": "Actor"},
			{"Name": "Zed Zzyzx", "Id": "p7", "Role": "Himself", "Type": "Director"},
			{"Name": "Someone Else", "Id": "p9", "Role": "Lead", "Type": "Actor"},
		}
		b["People"] = []map[string]any{{"Name": "zed zzyzx", "Role": "Lead", "Type": "Actor"}}
		writeJSON(t, w, page(b, a))
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "person_get", map[string]any{"person": "zed zzyzx"})
	if out["name"] != "Zed Zzyzx" || out["id"] != "p7" || out["overview"] != "Actor." || out["born"] != "1970-01-01T00:00:00Z" {
		t.Errorf("person = %v", out)
	}
	if ids := object(t, out["metadata_provider_ids"], "metadata_provider_ids"); ids["tmdb"] != "500" {
		t.Errorf("provider ids = %v", ids)
	}
	if q := lastQuery(t, f, "/Persons"); q.Get("SearchTerm") != "zed zzyzx" || q.Get("Limit") != "50" {
		t.Errorf("person search = %v", q)
	}
	if _, ok := out["candidates"]; ok {
		t.Errorf("an exact match lists candidates: %v", out["candidates"])
	}
	if len(f.requests("/Persons/Zed Zzyzx")) != 1 {
		t.Errorf("the person was not read by name: %v", f.seen)
	}
	q := lastQuery(t, f, "/Items")
	if q.Get("IncludeItemTypes") != "Movie,Series" || q.Get("SortBy") != "ProductionYear,SortName" || !strings.Contains(q.Get("Fields"), "People") {
		t.Errorf("credits query = %v", q)
	}
	credits := objects(t, out["credits"], "credits")
	if len(credits) != 3 {
		t.Fatalf("credits = %v", credits)
	}
	if credits[0]["id"] != "8" || credits[0]["credit"] != "Actor" || credits[0]["role"] != "Lead" {
		t.Errorf("a credit matched by name = %v", credits[0])
	}
	if credits[1]["credit"] != "Actor" || credits[1]["role"] != nil {
		t.Errorf("a role repeating the job should be blank: %v", credits[1])
	}
	if credits[2]["credit"] != "Director" || credits[2]["role"] != "Himself" {
		t.Errorf("second credit on one item = %v", credits[2])
	}

	// a part of a name is the people it could mean, and nothing else
	f.reset()
	out = mustCall(t, cs, "person_get", map[string]any{"person": "Zed"})
	cands := objects(t, out["candidates"], "candidates")
	if len(cands) != 2 || cands[0]["name"] != "Zed Zzyzx" || cands[0]["id"] != "p7" || cands[1]["name"] != "Zed Zzyzx Jr" || cands[1]["id"] != "p8" {
		t.Errorf("candidates = %v", cands)
	}
	// no person, so no name or id; credits is the empty list every tool
	// answers rather than null
	for _, k := range []string{"name", "id", "overview"} {
		if _, ok := out[k]; ok {
			t.Errorf("a near miss carries %s: %v", k, out[k])
		}
	}
	if len(objects(t, out["credits"], "credits")) != 0 {
		t.Errorf("a near miss carries credits: %v", out["credits"])
	}
	if q := lastQuery(t, f, "/Persons"); q.Get("SearchTerm") != "Zed" {
		t.Errorf("people query = %v", q)
	}
	if n := len(f.requests("/Items")); n != 0 {
		t.Errorf("a near miss read %d item pages; nothing to credit yet", n)
	}
	if msg := mustRefuse(t, cs, "person_get", map[string]any{"person": "Nobody"}); !strings.Contains(msg, `no person named "Nobody"`) {
		t.Errorf("unknown person: %s", msg)
	}
	if msg := mustRefuse(t, cs, "person_get", map[string]any{"person": " "}); !strings.Contains(msg, "person is required") {
		t.Errorf("no person: %s", msg)
	}
}

// item_similar asks in the user's context (Emby needs one) and
// item_instant_mix without; both answer summaries.
func TestItemSimilarAndInstantMix(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items/{id}/Similar", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(film("8", "Xyzzy", 1999)))
	})
	f.mux.HandleFunc("GET /Items/{id}/InstantMix", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(map[string]any{"Id": "a1", "Name": "Zzyzx Song", "Type": "Audio"}))
	})
	cs := session(t, f, Options{})

	items := objects(t, mustCall(t, cs, "item_similar", map[string]any{"id": "9", "user": "Plugh", "limit": 3})["items"], "items")
	if len(items) != 1 || items[0]["id"] != "8" || items[0]["name"] != "Xyzzy" {
		t.Errorf("similar = %v", items)
	}
	if q := lastQuery(t, f, "/Items/9/Similar"); q.Get("UserId") != "u2" || q.Get("Limit") != "3" {
		t.Errorf("similar query = %v", q)
	}
	mustCall(t, cs, "item_similar", map[string]any{"id": "9"})
	if q := lastQuery(t, f, "/Items/9/Similar"); q.Get("UserId") != "u1" || q.Get("Limit") != "10" {
		t.Errorf("similar defaults = %v", q)
	}

	items = objects(t, mustCall(t, cs, "item_instant_mix", map[string]any{"id": "a9"})["items"], "items")
	if len(items) != 1 || items[0]["id"] != "a1" || items[0]["type"] != "Audio" {
		t.Errorf("mix = %v", items)
	}
	if q := lastQuery(t, f, "/Items/a9/InstantMix"); q.Get("Limit") != "30" {
		t.Errorf("mix query = %v", q)
	}
}

// item_watch_history keeps the activity entries that name the item by id,
// or by title when they carry no id, and drops another item's entry whose
// title happens to contain this one's.
func TestItemWatchHistory(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("Ids") != "9" {
			writeJSON(t, w, page())
			return
		}
		writeJSON(t, w, page(film("9", "Zzyzx", 2001)))
	})
	f.mux.HandleFunc("GET /System/ActivityLog/Entries", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": []map[string]any{
			{"Name": "Quux has finished playing Zzyzx", "Type": "playback.stop", "Date": "2026-09-20T10:00:00Z", "ItemId": "9"},
			{"Name": "Plugh has started playing Zzyzx: Part Two", "Type": "playback.start", "Date": "2026-09-19T10:00:00Z", "ItemId": "10"},
			{"Name": "Zzyzx was added", "Type": "library.new", "Date": "2026-09-18T10:00:00Z"},
			{"Name": "Quux logged in", "Type": "login", "Date": "2026-09-17T10:00:00Z"},
		}, "TotalRecordCount": 4})
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "item_watch_history", map[string]any{"id": "9", "days": 30})
	if out["item"] != "Zzyzx" {
		t.Errorf("item = %v", out["item"])
	}
	if got := texts(out["entries"]); !slices.Equal(got, []string{"2026-09-20T10:00:00Z Quux has finished playing Zzyzx", "2026-09-18T10:00:00Z Zzyzx was added"}) {
		t.Errorf("entries = %v", got)
	}
	q := lastQuery(t, f, "/System/ActivityLog/Entries")
	if since, err := time.Parse(time.RFC3339, q.Get("MinDate")); err != nil || time.Since(since) < 29*24*time.Hour || time.Since(since) > 31*24*time.Hour {
		t.Errorf("MinDate = %q", q.Get("MinDate"))
	}
	if q.Get("Limit") != "1000" {
		t.Errorf("Limit = %q", q.Get("Limit"))
	}
}

// item_artwork lists what the item has and the provider candidates, sized;
// item_artwork_set posts the chosen url for the type.
func TestItemArtwork(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items/{id}/Images", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{{"ImageType": "Primary", "Width": 1000, "Height": 1500, "Size": 12345}})
	})
	f.mux.HandleFunc("GET /Items/{id}/RemoteImages", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Images": []map[string]any{
			{"ProviderName": "Zzyzx DB", "Url": "https://zz.example/p.jpg", "Type": "Primary", "Width": 2000, "Height": 3000, "Language": "en", "CommunityRating": 5.5, "VoteCount": 12},
			{"ProviderName": "Zzyzx DB", "Url": "https://zz.example/q.jpg", "Type": "Primary"},
		}, "TotalRecordCount": 2})
	})
	f.mux.HandleFunc("POST /Items/{id}/RemoteImages/Download", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "item_artwork", map[string]any{"id": "9", "type": "Backdrop", "limit": 4})
	if q := lastQuery(t, f, "/Items/9/RemoteImages"); q.Get("Type") != "Backdrop" || q.Get("Limit") != "4" {
		t.Errorf("remote query = %v", q)
	}
	current := objects(t, out["current"], "current")
	if len(current) != 1 || current[0]["ImageType"] != "Primary" || number(t, current[0]["Width"], "Width") != 1000 {
		t.Errorf("current = %v", current)
	}
	cands := objects(t, out["candidates"], "candidates")
	if len(cands) != 2 || cands[0]["url"] != "https://zz.example/p.jpg" || cands[0]["provider"] != "Zzyzx DB" || cands[0]["size"] != "2000x3000" || cands[0]["language"] != "en" || decimal(t, cands[0]["rating"], "rating") != 5.5 || number(t, cands[0]["votes"], "votes") != 12 {
		t.Errorf("candidates = %v", cands)
	}
	if _, ok := cands[1]["size"]; ok {
		t.Errorf("a candidate without dimensions has a size: %v", cands[1])
	}
	mustCall(t, cs, "item_artwork", map[string]any{"id": "9"})
	if q := lastQuery(t, f, "/Items/9/RemoteImages"); q.Get("Type") != "Primary" || q.Get("Limit") != "10" {
		t.Errorf("remote defaults = %v", q)
	}

	out = mustCall(t, cs, "item_artwork_set", map[string]any{"id": "9", "url": "https://zz.example/p.jpg", "type": "Backdrop"})
	if out["set"] != "Backdrop" {
		t.Errorf("set = %v", out)
	}
	if q := lastQuery(t, f, "/Items/9/RemoteImages/Download"); q.Get("Type") != "Backdrop" || q.Get("ImageUrl") != "https://zz.example/p.jpg" {
		t.Errorf("download query = %v", q)
	}
	if out = mustCall(t, cs, "item_artwork_set", map[string]any{"id": "9", "url": "https://zz.example/q.jpg"}); out["set"] != "Primary" {
		t.Errorf("default type = %v", out)
	}
}

// item_subtitle_search asks by three-letter language, eng by default, and
// item_subtitle_download posts the chosen candidate.
func TestItemSubtitles(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items/{id}/RemoteSearch/Subtitles/{lang}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []map[string]any{{"Id": "sub1", "Name": "Zzyzx." + r.PathValue("lang") + ".srt", "ProviderName": "Zzyzx Subs", "Format": "srt", "DownloadCount": 40, "CommunityRating": 4.5}})
	})
	// the server's refresh adds a downloaded subtitle to the item a few reads
	// after the download answers; an id that names nothing is answered the
	// same, and adds nothing
	var (
		mu        sync.Mutex
		subtitles []map[string]any
		due       int
	)
	f.mux.HandleFunc("POST /Items/{id}/RemoteSearch/Subtitles/{sub}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.PathValue("sub") == "sub1" {
			due = 3
		}
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if due > 0 {
			due--
			if due == 0 {
				subtitles = append(subtitles, map[string]any{"Type": "Subtitle", "Codec": "srt", "Language": "eng", "IsExternal": true})
			}
		}
		it := film("9", "Zzyzx", 2001)
		it["MediaSources"] = []map[string]any{{"Path": "/zz/films/Zzyzx.mkv", "MediaStreams": append([]map[string]any{
			{"Type": "Video", "Codec": "h264"}, {"Type": "Subtitle", "Codec": "subrip", "Language": "fre"},
		}, subtitles...)}}
		writeJSON(t, w, page(it))
	})
	r := &registry{client: f.client(t), settle: time.Millisecond}
	registerSubtitleTools(r)
	cs := hostRegistry(t, r)

	cands := objects(t, mustCall(t, cs, "item_subtitle_search", map[string]any{"id": "9"})["candidates"], "candidates")
	if len(cands) != 1 || cands[0]["id"] != "sub1" || cands[0]["name"] != "Zzyzx.eng.srt" || cands[0]["provider"] != "Zzyzx Subs" || cands[0]["format"] != "srt" || number(t, cands[0]["downloads"], "downloads") != 40 || decimal(t, cands[0]["rating"], "rating") != 4.5 {
		t.Errorf("candidates = %v", cands)
	}
	mustCall(t, cs, "item_subtitle_search", map[string]any{"id": "9", "language": "fre"})
	if len(f.requests("/Items/9/RemoteSearch/Subtitles/fre")) != 1 {
		t.Errorf("the language was not asked for: %v", f.seen)
	}

	// a made-up id is answered with the same empty success, and nothing comes
	msg := mustRefuse(t, cs, "item_subtitle_download", map[string]any{"id": "9", "subtitle_id": "zzyzx-nothing"})
	if !strings.Contains(msg, "the server answered the download of zzyzx-nothing, but no new subtitle reached Zzyzx") {
		t.Errorf("a download that brought nothing = %q", msg)
	}
	if reqs := f.requests("/Items"); len(reqs) != 1+subtitlePolls {
		t.Errorf("the item was read %d times, want once before and %d times after", len(reqs), subtitlePolls)
	}

	out := mustCall(t, cs, "item_subtitle_download", map[string]any{"id": "9", "subtitle_id": "sub1"})
	if !boolean(t, out["downloaded"], "downloaded") {
		t.Errorf("download = %v", out)
	}
	if reqs := f.requests("/Items/9/RemoteSearch/Subtitles/sub1"); len(reqs) != 1 || reqs[0].Method != http.MethodPost {
		t.Errorf("download requests = %v", reqs)
	}
}

// library_create posts the folders with every fetcher off when providers
// are not asked for, and answers with the library as the server then lists
// it; library_edit makes each change through its own route, in order, and
// answers with the library as it is after.
func TestLibraryCreateAndEdit(t *testing.T) {
	t.Parallel()

	f, libs := zzyzxServer(t)
	var created, renamed, added, removed, options map[string]any
	f.mux.HandleFunc("POST /Library/VirtualFolders", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		libs.mu.Lock()
		defer libs.mu.Unlock()
		created = body
		libs.folders = append(libs.folders, map[string]any{
			"Name": body["Name"], "CollectionType": body["CollectionType"], "ItemId": "lib10", "Locations": body["Paths"],
			"LibraryOptions": map[string]any{"MetadataSavers": []string{}},
		})
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("POST /Library/VirtualFolders/Name", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		libs.mu.Lock()
		defer libs.mu.Unlock()
		renamed = body
		libs.folders[0]["Name"] = body["NewName"]
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("POST /Library/VirtualFolders/Paths", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		libs.mu.Lock()
		defer libs.mu.Unlock()
		added = body
		locations, ok := libs.folders[0]["Locations"].([]string)
		if !ok {
			t.Errorf("Locations = %v, want a list", libs.folders[0]["Locations"])
		}
		libs.folders[0]["Locations"] = append(locations, text(object(t, body["PathInfo"], "PathInfo")["Path"]))
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("POST /Library/VirtualFolders/Paths/Delete", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		libs.mu.Lock()
		defer libs.mu.Unlock()
		removed = body
		locations, ok := libs.folders[0]["Locations"].([]string)
		if !ok {
			t.Errorf("Locations = %v, want a list", libs.folders[0]["Locations"])
		}
		libs.folders[0]["Locations"] = slices.DeleteFunc(locations, func(p string) bool { return p == body["Path"] })
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("POST /Library/VirtualFolders/LibraryOptions", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		libs.mu.Lock()
		defer libs.mu.Unlock()
		options = body
		libs.folders[0]["LibraryOptions"] = body["LibraryOptions"]
		w.WriteHeader(http.StatusNoContent)
	})
	cs := session(t, f, Options{})

	if msg := mustRefuse(t, cs, "library_create", map[string]any{"name": "Zzyzx Docs", "type": "movies", "paths": []string{}}); !strings.Contains(msg, "at least one path") {
		t.Errorf("create without a path: %s", msg)
	}
	// a name another library has apart from case: Jellyfin makes it as
	// "ZZYZX FILMS2", and the read-back by name answered with the old one
	if msg := mustRefuse(t, cs, "library_create", map[string]any{"name": "ZZYZX FILMS", "type": "movies", "paths": []string{"/zz/more"}}); !strings.Contains(msg, `a library named "Zzyzx Films" already exists`) {
		t.Errorf("create beside a library of the same name = %s", msg)
	}
	libs.mu.Lock()
	if created != nil {
		t.Errorf("a library was made under a name one already has: %v", created)
	}
	libs.mu.Unlock()
	out := mustCall(t, cs, "library_create", map[string]any{"name": "Zzyzx Docs", "type": "movies", "paths": []string{"/zz/docs"}})
	if out["id"] != "lib10" || out["name"] != "Zzyzx Docs" || out["collection_type"] != "movies" || strings.Join(texts(out["locations"]), ",") != "/zz/docs" || boolean(t, out["saves_nfo"], "saves_nfo") {
		t.Errorf("create = %v", out)
	}
	libs.mu.Lock()
	body := created
	libs.mu.Unlock()
	if body["Name"] != "Zzyzx Docs" || body["CollectionType"] != "movies" || strings.Join(texts(body["Paths"]), ",") != "/zz/docs" || body["RefreshLibrary"] != any(false) {
		t.Errorf("posted library = %v", body)
	}
	typeOptions := objects(t, object(t, body["LibraryOptions"], "LibraryOptions")["TypeOptions"], "TypeOptions")
	if len(typeOptions) == 0 {
		t.Fatal("no type options: the fetchers were left to the server's defaults")
	}
	for _, o := range typeOptions {
		if fetchers, ok := o["MetadataFetchers"].([]any); ok && len(fetchers) != 0 {
			t.Errorf("fetchers on for %v", o)
		}
	}

	if msg := mustRefuse(t, cs, "library_edit", map[string]any{"library": "Zzyzx Films"}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("empty edit: %s", msg)
	}
	if msg := mustRefuse(t, cs, "library_edit", map[string]any{"library": "Zzyzx Films", "add_paths": []string{"/zz/films"}}); !strings.Contains(msg, "already holds /zz/films") {
		t.Errorf("adding a held folder: %s", msg)
	}
	if msg := mustRefuse(t, cs, "library_edit", map[string]any{"library": "Zzyzx Films", "remove_paths": []string{"/zz/nope"}}); !strings.Contains(msg, "has no folder /zz/nope") {
		t.Errorf("removing a stranger: %s", msg)
	}
	out = mustCall(t, cs, "library_edit", map[string]any{
		"library": "lib9", "name": "Zzyzx Cinema", "add_paths": []string{"/zz/more"}, "remove_paths": []string{"/zz/films"}, "save_nfo": true,
	})
	if got := strings.Join(texts(out["changed"]), ";"); got != "nfo saving on;added /zz/more;removed /zz/films;renamed Zzyzx Films to Zzyzx Cinema" {
		t.Errorf("changed = %s", got)
	}
	if out["id"] != "lib9" || out["name"] != "Zzyzx Cinema" || strings.Join(texts(out["locations"]), ",") != "/zz/more" || !boolean(t, out["saves_nfo"], "saves_nfo") {
		t.Errorf("edited library = %v", out)
	}
	libs.mu.Lock()
	defer libs.mu.Unlock()
	if renamed["Id"] != "lib9" || renamed["NewName"] != "Zzyzx Cinema" {
		t.Errorf("rename body = %v", renamed)
	}
	if added["Id"] != "lib9" || object(t, added["PathInfo"], "PathInfo")["Path"] != "/zz/more" || added["RefreshLibrary"] != any(false) {
		t.Errorf("add body = %v", added)
	}
	if removed["Id"] != "lib9" || removed["Path"] != "/zz/films" {
		t.Errorf("remove body = %v", removed)
	}
	if options["Id"] != "lib9" || strings.Join(texts(object(t, options["LibraryOptions"], "LibraryOptions")["MetadataSavers"]), ",") != "Nfo" {
		t.Errorf("options body = %v", options)
	}
}

// library_edit checks every folder it is given before it changes anything,
// so a folder the library does not have leaves nfo saving and the other
// folders as they were; and when the server refuses a step after others
// have landed, the refusal says which did.
func TestLibraryEditChecksFirstAndSaysWhatLanded(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	for _, route := range []string{"POST /Library/VirtualFolders/LibraryOptions", "POST /Library/VirtualFolders/Paths", "POST /Library/VirtualFolders/Paths/Delete"} {
		f.mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	}
	f.mux.HandleFunc("POST /Library/VirtualFolders/Name", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "a library by that name already exists", http.StatusBadRequest)
	})
	cs := session(t, f, Options{})
	changes := func() int {
		n := 0
		for _, path := range []string{"/Library/VirtualFolders/LibraryOptions", "/Library/VirtualFolders/Paths", "/Library/VirtualFolders/Paths/Delete", "/Library/VirtualFolders/Name"} {
			n += len(f.requests(path))
		}
		return n
	}

	for args, want := range map[*map[string]any]string{
		{"save_nfo": true, "add_paths": []string{"/zz/more"}, "remove_paths": []string{"/zz/nope"}}: "has no folder /zz/nope",
		{"save_nfo": true, "add_paths": []string{"/zz/more", "/zz/films"}}:                          "already holds /zz/films",
		{"add_paths": []string{"/zz/more", "/zz/more"}}:                                             "/zz/more is named more than once",
	} {
		(*args)["library"] = "Zzyzx Films"
		if msg := mustRefuse(t, cs, "library_edit", *args); !strings.Contains(msg, want) {
			t.Errorf("%v = %q, want %q", *args, msg, want)
		}
	}
	if n := changes(); n != 0 {
		t.Errorf("%d changes were made before the edit was refused, want none", n)
	}

	msg := mustRefuse(t, cs, "library_edit", map[string]any{"library": "Zzyzx Films", "save_nfo": true, "add_paths": []string{"/zz/more"}, "name": "Zzyzx Cinema"})
	if !strings.Contains(msg, "renaming to Zzyzx Cinema") || !strings.Contains(msg, "already done before it failed: nfo saving on, added /zz/more") {
		t.Errorf("a refused rename = %q, want what landed before it named", msg)
	}
}

// An older Jellyfin lists a new library without an id until its first scan
// (12.1 gives one when it is made), and a tool narrowing to
// it by that empty id would answer for, or change, the whole server: the
// tools that read or filter by a library refuse it and say to scan, and the
// tools that act on the library itself still work on it by name.
func TestALibraryWithNoIDIsNeverTheWholeServer(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.jellyfin = true
	f.mux.HandleFunc("GET /Library/VirtualFolders", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{
			{"Name": "Zzyzx Films", "CollectionType": "movies", "ItemId": "lib9", "Locations": []string{"/zz/films"}},
			{"Name": "Zzyzx New", "CollectionType": "movies", "Locations": []string{"/zz/new"}},
		})
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(film("9", "Zzyzx", 2001))) })
	f.mux.HandleFunc("GET /Users", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{{"Id": "u1", "Name": "Quux", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}}})
	})
	for _, route := range []string{"POST /Library/Refresh", "POST /Library/VirtualFolders/Paths", "DELETE /Library/VirtualFolders"} {
		f.mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	}
	cs := session(t, f, Options{EnableDelete: true})

	for name, args := range map[string]map[string]any{
		"library_recent":         {"library": "Zzyzx New"},
		"library_items":          {"library": "zzyzx new"},
		"audit_missing_overview": {"library": "Zzyzx New"},
		"metadata_rename":        {"library": "Zzyzx New", "field": "genres", "from": "Scifi", "to": "Science Fiction"},
		"user_stats":             {"library": "Zzyzx New"},
	} {
		if msg := mustRefuse(t, cs, name, args); !strings.Contains(msg, "the server lists the Zzyzx New library without an id, so there is nothing to narrow to; run library_scan") {
			t.Errorf("%s = %q, want the library refused until a scan gives it an id", name, msg)
		}
	}
	if reqs := f.requests("/Items"); len(reqs) != 0 {
		t.Errorf("the whole server was read for one library: %v", reqs)
	}

	// leaving a library out goes by its folders, which it has before its id
	out := mustCall(t, cs, "audit_missing_metadata_provider", map[string]any{"ignore": []string{"Zzyzx New"}})
	if number(t, out["items_scanned"], "items_scanned") != 1 {
		t.Errorf("audit_missing_metadata_provider ignoring the unscanned library = %v", out)
	}
	f.reset()

	out = mustCall(t, cs, "library_get", map[string]any{"library": "Zzyzx New"})
	if out["name"] != "Zzyzx New" || out["id"] != nil || number(t, out["item_count"], "item_count") != 0 || !strings.Contains(text(out["note"]), "listed without an id") {
		t.Errorf("library_get = %v, want the library with no counts and a note", out)
	}
	if reqs := f.requests("/Items"); len(reqs) != 0 {
		t.Errorf("library_get counted the whole server: %v", reqs)
	}

	out = mustCall(t, cs, "library_scan", map[string]any{"library": "Zzyzx New"})
	if !boolean(t, out["started"], "started") || out["library"] != "Zzyzx New" || !strings.Contains(text(out["note"]), "every library") || len(f.requests("/Library/Refresh")) != 1 {
		t.Errorf("library_scan = %v, want every library scanned to give it an id", out)
	}

	if msg := mustRefuse(t, cs, "library_edit", map[string]any{"library": "Zzyzx New", "save_nfo": true, "add_paths": []string{"/zz/more"}}); !strings.Contains(msg, "without an id, and nfo saving is switched by id") {
		t.Errorf("nfo saving on a library with no id = %q", msg)
	}
	if reqs := f.requests("/Library/VirtualFolders/Paths"); len(reqs) != 0 {
		t.Errorf("a folder was added to an edit that was refused: %v", reqs)
	}
	out = mustCall(t, cs, "library_edit", map[string]any{"library": "Zzyzx New", "add_paths": []string{"/zz/more"}})
	if strings.Join(texts(out["changed"]), ";") != "added /zz/more" {
		t.Errorf("library_edit = %v", out)
	}

	out = mustCall(t, cs, "library_delete", map[string]any{"library": "Zzyzx New", "confirm": true})
	if out["deleted"] != "Zzyzx New" {
		t.Errorf("library_delete = %v", out)
	}
	if q := lastQuery(t, f, "/Library/VirtualFolders"); q.Get("name") != "Zzyzx New" {
		t.Errorf("delete query = %v", q)
	}
}

// A library named apart from case is found when one library has the name,
// an exact name wins over one that differs in case, and a name several
// libraries share apart from case is refused naming them: Jellyfin on Linux
// can hold "Movies" and "movies", and a delete must not guess.
func TestLibraryNamesPreferTheExactOne(t *testing.T) {
	t.Parallel()

	f, libs := zzyzxServer(t)
	libs.folders = []map[string]any{
		{"Name": "Movies", "CollectionType": "movies", "ItemId": "m1", "Locations": []string{"/zz/a"}},
		{"Name": "movies", "CollectionType": "movies", "ItemId": "m2", "Locations": []string{"/zz/b"}},
		{"Name": "Zzyzx Shows", "CollectionType": "tvshows", "ItemId": "s1", "Locations": []string{"/zz/c"}},
	}
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page()) })
	f.mux.HandleFunc("POST /Library/VirtualFolders/Delete", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	cs := session(t, f, Options{EnableDelete: true})

	for asked, want := range map[string]string{"Movies": "m1", "movies": "m2", "zzyzx shows": "s1", "m2": "m2"} {
		if out := mustCall(t, cs, "library_get", map[string]any{"library": asked}); out["id"] != want {
			t.Errorf("library %q = %v, want %s", asked, out["id"], want)
		}
	}
	msg := mustRefuse(t, cs, "library_delete", map[string]any{"library": "MOVIES", "confirm": true})
	if !strings.Contains(msg, `2 libraries are named "MOVIES" apart from case`) || !strings.Contains(msg, `"Movies" (id m1)`) || !strings.Contains(msg, `"movies" (id m2)`) {
		t.Errorf("an ambiguous name = %q, want both named", msg)
	}
	if reqs := f.requests("/Library/VirtualFolders/Delete"); len(reqs) != 0 {
		t.Errorf("a library was deleted on a guess: %v", reqs)
	}
}

// Against Jellyfin, which spells its queries and lists differently:
// library_items takes the same shape on /Items, item_find_by_metadata_id
// has no server-side lookup and scans films and series for the id itself,
// and session_list reads the bare list.
func TestJellyfinVariants(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.jellyfin = true
	f.mux.HandleFunc("GET /Library/VirtualFolders", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{{"Name": "Zzyzx Films", "CollectionType": "movies", "ItemId": "lib9", "Locations": []string{"/zz/films"}}})
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		a, b := film("9", "Zzyzx", 2001), film("8", "Xyzzy", 1999)
		a["ProviderIds"] = map[string]string{"Tmdb": "77"}
		b["ProviderIds"] = map[string]string{"Imdb": "tt77"}
		writeJSON(t, w, page(a, b))
	})
	f.mux.HandleFunc("GET /Sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{{"Id": "s1", "UserName": "Quux", "Client": "Zzyzx Web", "DeviceName": "Laptop"}})
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "library_items", map[string]any{"query": "zzy", "library": "Zzyzx Films"})
	q := lastQuery(t, f, "/Items")
	if q.Get("searchTerm") != "zzy" || q.Get("includeItemTypes") != "Movie" || q.Get("parentId") != "lib9" || q.Get("limit") != "25" || q.Get("recursive") != "true" || q.Get("collapseBoxSetItems") != "false" || q.Has("sortBy") {
		t.Errorf("query = %v", q)
	}
	if number(t, out["total"], "total") != 2 || len(objects(t, out["items"], "items")) != 2 {
		t.Errorf("search = %v", out)
	}

	f.reset()
	out = mustCall(t, cs, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "77"})
	// Jellyfin's SDK repeats a list parameter rather than joining it
	if q = lastQuery(t, f, "/Items"); !slices.Equal(q["includeItemTypes"], []string{"Movie", "Series"}) || q.Has("anyProviderIdEquals") {
		t.Errorf("scan query = %v", q)
	}
	if !boolean(t, out["found"], "found") {
		t.Error("not found")
	}
	if items := objects(t, out["items"], "items"); len(items) != 1 || items[0]["id"] != "9" {
		t.Errorf("items = %v, want the tmdb match only", items)
	}
	if out = mustCall(t, cs, "item_find_by_metadata_id", map[string]any{"metadata_provider": "imdb", "id": "tt1"}); boolean(t, out["found"], "found") {
		t.Errorf("found = %v", out)
	}

	rows := objects(t, mustCall(t, cs, "session_list", map[string]any{})["sessions"], "sessions")
	if len(rows) != 1 || rows[0]["id"] != "s1" || rows[0]["device"] != "Laptop" || rows[0]["app"] != "Zzyzx Web" || rows[0]["user"] != "Quux" {
		t.Errorf("sessions = %v", rows)
	}
}
