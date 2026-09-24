package embyfin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/client"
)

// The request each neutral method builds per backend and the answer it
// decodes, against a canned server. The acceptance suite proves these
// against real servers; these keep every quirk the neutral layer hides from
// quietly regressing without one.

// recorded is one request the canned server saw.
type recorded struct {
	method, path string
	query        url.Values
	body         string
}

// route answers one "METHOD /path".
type route func(r *http.Request, body string) (int, string)

// fake is a canned server that answers by method and path and records every
// request.
type fake struct {
	t        *testing.T
	routes   map[string]route
	requests []recorded
}

func newFake(t *testing.T, backend Backend, routes map[string]route) (*Client, *fake) {
	t.Helper()

	f := &fake{t: t, routes: routes}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.requests = append(f.requests, recorded{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.Query(), body: string(b)})
		handle, ok := routes[r.Method+" "+r.URL.EscapedPath()]
		if !ok {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		status, resp := handle(r, string(b))
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)

	c, err := New(backend, srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	// the server's own client, so closing another test's server cannot
	// close this one's connections
	if c.emby != nil {
		c.emby.Client.HTTPClient = srv.Client()
	} else {
		c.jf.Client.HTTPClient = srv.Client()
	}

	return c, f
}

func answer(status int, body string) route {
	return func(*http.Request, string) (int, string) { return status, body }
}

func ok(body string) route { return answer(http.StatusOK, body) }

var noContent = answer(http.StatusNoContent, "")

// only returns the one request made to "METHOD /path".
func (f *fake) only(key string) recorded {
	f.t.Helper()

	var got []recorded
	for _, r := range f.requests {
		if r.method+" "+r.path == key {
			got = append(got, r)
		}
	}
	if len(got) != 1 {
		f.t.Fatalf("%d requests to %s, want 1 (saw %v)", len(got), key, f.requests)
	}

	return got[0]
}

// all returns every request made to "METHOD /path".
func (f *fake) all(key string) []recorded {
	var got []recorded
	for _, r := range f.requests {
		if r.method+" "+r.path == key {
			got = append(got, r)
		}
	}

	return got
}

// queryList reads a list parameter whichever server's spelling it takes:
// Emby's capitalised and comma-joined, Jellyfin's camel-cased and repeated.
func queryList(q url.Values, name string) []string {
	if v := q.Get(name); v != "" {
		return strings.Split(v, ",")
	}
	var out []string
	for _, v := range q[strings.ToLower(name[:1])+name[1:]] {
		out = append(out, strings.Split(v, ",")...)
	}

	return out
}

// jsonBody decodes a recorded body.
func jsonBody(t *testing.T, body string) map[string]any {
	t.Helper()

	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}

	return m
}

func TestNew(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		backend      Backend
		url, token   string
		wantContains string
	}{
		{"plex", "http://nas", "t", "unknown backend"},
		{Emby, "", "t", "server URL is required"},
		{Jellyfin, "http://nas", "", "API token is required"},
		{Emby, "nas", "t", "must include a scheme and host"},
	} {
		if _, err := New(tt.backend, tt.url, tt.token); err == nil || !strings.Contains(err.Error(), tt.wantContains) {
			t.Errorf("New(%s, %q, %q) = %v, want %q", tt.backend, tt.url, tt.token, err, tt.wantContains)
		}
	}
	c, err := New(Jellyfin, "http://nas:8096/", "t")
	if err != nil || c.Backend() != Jellyfin || c.BaseURL() != "http://nas:8096" {
		t.Errorf("New = %+v, %v", c, err)
	}
}

// Emby answers a lookup that finds nothing with a 204 and no body, which the
// typed client hands back as a nil model and no error. Every Emby read takes
// that as nothing found: a list comes back empty, a single item is an error
// IsNotFound recognises, and nothing reaches through the missing model.
func TestEmbyNullResults(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(srv.Close)
	c, err := New(Emby, srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	c.emby.Client.HTTPClient = srv.Client()
	ctx := t.Context()

	// call runs one read, turning a panic into a failure of that read alone
	call := func(name string, read func() error) (err error) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("%s panicked on Emby's null result: %v", name, r)
				err = nil
			}
		}()
		return read()
	}
	empty := func(n int, err error) error {
		if err == nil && n != 0 {
			return fmt.Errorf("%d results", n)
		}
		return err
	}

	for name, read := range map[string]func() error{
		"Search": func() error { items, total, err := c.Search(ctx, SearchOptions{}); return empty(len(items)+total, err) },
		"Search as a user": func() error {
			items, _, err := c.Search(ctx, SearchOptions{UserID: "u1"})
			return empty(len(items), err)
		},
		"ItemsByProviderID": func() error { items, err := c.ItemsByProviderID(ctx, "tmdb", "348"); return empty(len(items), err) },
		"Similar":           func() error { items, err := c.Similar(ctx, "1", "u1", 5); return empty(len(items), err) },
		"InstantMix":        func() error { items, err := c.InstantMix(ctx, "1", 5); return empty(len(items), err) },
		"PlaylistItems": func() error {
			items, total, err := c.PlaylistItems(ctx, "p1", "u1")
			return empty(len(items)+total, err)
		},
		"RemoteImages": func() error {
			images, total, err := c.RemoteImages(ctx, "1", "Primary", 5)
			return empty(len(images)+total, err)
		},
		"VirtualFolders": func() error { folders, err := c.VirtualFolders(ctx); return empty(len(folders), err) },
		"Persons":        func() error { people, err := c.Persons(ctx, "ridley", 5); return empty(len(people), err) },
		"Seasons":        func() error { items, err := c.Seasons(ctx, "s1", "u1"); return empty(len(items), err) },
		"Episodes":       func() error { items, err := c.Episodes(ctx, "s1", EpisodeOptions{}); return empty(len(items), err) },
		"Users":          func() error { users, err := c.Users(ctx); return empty(len(users), err) },
		"NextUp":         func() error { items, err := c.NextUp(ctx, "u1", 5); return empty(len(items), err) },
		"Resume":         func() error { items, err := c.Resume(ctx, "u1", 5); return empty(len(items), err) },
		"ActivityLog": func() error {
			entries, total, err := c.ActivityLog(ctx, time.Time{}, 5, 0)
			return empty(len(entries)+total, err)
		},
		"Devices":  func() error { devices, err := c.Devices(ctx); return empty(len(devices), err) },
		"LogFiles": func() error { files, err := c.LogFiles(ctx); return empty(len(files), err) },
		// no fetchers listed is a library created with none
		"CreateLibrary with providers": func() error {
			return c.CreateLibrary(ctx, LibrarySpec{Name: "Films", CollectionType: "movies", Paths: []string{"/m"}, Providers: true})
		},
	} {
		if err := call(name, read); err != nil {
			t.Errorf("%s = %v, want nothing found", name, err)
		}
	}

	edited := false
	for name, read := range map[string]func() error{
		"UserItem": func() error { _, err := c.UserItem(ctx, "u1", "42"); return err },
		"FullItem": func() error { _, err := c.FullItem(ctx, "u1", "42"); return err },
		"EditItem": func() error {
			_, err := c.EditItem(ctx, "u1", "42", func(full map[string]any) (bool, error) {
				edited = true
				full["Name"] = "Alien"
				return true, nil
			})
			return err
		},
		"Person": func() error { _, err := c.Person(ctx, "Ridley Scott", ""); return err },
	} {
		if err := call(name, read); !client.IsNotFound(err) {
			t.Errorf("%s = %v, want an error IsNotFound recognises", name, err)
		}
	}
	if edited {
		t.Error("EditItem handed its edit an item the server does not have")
	}
	if it, seen, err := c.VisibleUserItem(ctx, "u1", "42"); err != nil || seen || it != nil {
		t.Errorf("VisibleUserItem = %v, %v, %v, want not seen", it, seen, err)
	}

	// what has no answer to fall back on is an error rather than a zero
	for name, read := range map[string]func() error{
		"CreatePlaylist":   func() error { _, err := c.CreatePlaylist(ctx, "Mix", []string{"1"}, "Video", "u1"); return err },
		"CreateCollection": func() error { _, err := c.CreateCollection(ctx, "Set", []string{"1"}); return err },
		"SystemInfo":       func() error { _, err := c.SystemInfo(ctx); return err },
		"Counts":           func() error { _, err := c.Counts(ctx); return err },
		"SetLibraryNfo":    func() error { return c.SetLibraryNfo(ctx, &VirtualFolder{Name: "Films", ItemID: "7"}, true) },
	} {
		if err := call(name, read); err == nil {
			t.Errorf("%s succeeded on an answer of nothing", name)
		}
	}
}

func TestSearchEmby(t *testing.T) {
	t.Parallel()

	page := `{"Items":[{"Id":"1","Name":"Alien","Type":"Movie","ProductionYear":1979,"RunTimeTicks":69600000000,
		"TagItems":[{"Name":"space","Id":4}],"ProviderIds":{"Tmdb":"348"},"UserData":{"Played":true,"PlayCount":2,"IsFavorite":false},
		"MediaSources":[{"Container":"mkv","Size":10,"MediaStreams":[{"Type":"Subtitle","IsExternal":true}]}],
		"People":[{"Name":"Ridley Scott","Type":"Director"}],"LocationType":"Virtual"}],"TotalRecordCount":9}`
	c, f := newFake(t, Emby, map[string]route{"GET /Items": ok(page), "GET /Users/u1/Items": ok(page)})

	items, total, err := c.Search(t.Context(), SearchOptions{SearchTerm: "ali", IncludeItemTypes: "Movie", Limit: 1, EnableUserData: true})
	if err != nil {
		t.Fatal(err)
	}
	q := f.only("GET /Items").query
	// the item queries' SearchTerm comes from the emby-search-term workaround
	for k, want := range map[string]string{"Recursive": "true", "Fields": FieldsDefault, "SearchTerm": "ali", "IncludeItemTypes": "Movie", "Limit": "1", "EnableUserData": "true"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if total != 9 || len(items) != 1 {
		t.Fatalf("Search = %d items of %d", len(items), total)
	}
	it := items[0]
	if it.Name != "Alien" || it.RuntimeMinutes() != 116 || !slices.Equal(it.TagNames(), []string{"space"}) || !it.IsMissing ||
		it.UserData == nil || !it.UserData.Played || it.UserData.IsFavourite || it.ProviderIDs["Tmdb"] != "348" ||
		len(it.MediaSources) != 1 || !it.MediaSources[0].MediaStreams[0].IsExternal || it.People[0].Type != "Director" {
		t.Errorf("item = %+v", it)
	}

	// a user's view goes through the per-user route on Emby
	if _, _, err := c.Search(t.Context(), SearchOptions{UserID: "u1", ParentID: "lib"}); err != nil {
		t.Fatal(err)
	}
	if q := f.only("GET /Users/u1/Items").query; q.Get("ParentId") != "lib" || q.Has("UserId") {
		t.Errorf("per-user query = %v", q)
	}
}

// A user's view goes through Emby's per-user route, whose options are a
// hand copy of the plain route's: every filter a query sets must reach the
// server whichever route it takes, or a user's view silently drops one
// (saved_since did, once).
func TestEmbyPerUserRouteKeepsEveryFilter(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Emby, map[string]route{"GET /Items": ok(`{"Items":[]}`), "GET /Users/u1/Items": ok(`{"Items":[]}`)})

	// every field set to something the zero value is not
	full := SearchOptions{}
	v := reflect.ValueOf(&full).Elem()
	for i := range v.NumField() {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("x" + v.Type().Field(i).Name)
		case reflect.Slice:
			f.Set(reflect.ValueOf([]string{"a"}))
		case reflect.Pointer:
			f.Set(reflect.ValueOf(new(3)))
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Int:
			f.SetInt(5)
		default:
			t.Fatalf("SearchOptions.%s is a %s: teach this test to set one", v.Type().Field(i).Name, f.Kind())
		}
	}
	full.UserID = ""
	if _, _, err := c.Search(t.Context(), full); err != nil {
		t.Fatal(err)
	}
	full.UserID = "u1"
	if _, _, err := c.Search(t.Context(), full); err != nil {
		t.Fatal(err)
	}

	plain, user := f.only("GET /Items").query, f.only("GET /Users/u1/Items").query
	for key, want := range plain {
		if got := user[key]; !slices.Equal(got, want) {
			t.Errorf("%s=%v on the plain route is %v on the per-user route", key, want, got)
		}
	}
}

func TestSearchJellyfin(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Jellyfin, map[string]route{
		"GET /Items": ok(`{"Items":[{"Id":"a","Name":"Alien","Type":"Movie","Tags":["space"]}],"TotalRecordCount":1}`),
	})
	items, _, err := c.Search(t.Context(), SearchOptions{UserID: "u1", IDs: "a,b", Years: "1979, 1986", SortBy: "SortName", Fields: "Path,Tags"})
	if err != nil {
		t.Fatal(err)
	}
	q := f.only("GET /Items").query
	if q.Get("userId") != "u1" || q.Get("collapseBoxSetItems") != "false" || q.Get("recursive") != "true" ||
		!slices.Equal(q["ids"], []string{"a", "b"}) || !slices.Equal(q["years"], []string{"1979", "1986"}) || !slices.Equal(q["fields"], []string{"Path", "Tags"}) {
		t.Errorf("query = %v", q)
	}
	if !slices.Equal(items[0].TagNames(), []string{"space"}) {
		t.Errorf("tags = %v", items[0].TagNames())
	}

	if _, _, err := c.Search(t.Context(), SearchOptions{Years: "nineteen"}); err == nil || !strings.Contains(err.Error(), "years") {
		t.Errorf("a bad year = %v", err)
	}
}

func TestSearchAllAndLookups(t *testing.T) {
	t.Parallel()

	var pages int
	c, f := newFake(t, Jellyfin, map[string]route{
		"GET /Items": func(r *http.Request, _ string) (int, string) {
			pages++
			if r.URL.Query().Get("startIndex") == "" {
				return http.StatusOK, `{"Items":[{"Id":"a","ProviderIds":{"Tmdb":"348"}}],"TotalRecordCount":1001}`
			}
			return http.StatusOK, `{"Items":[{"Id":"b","ProviderIds":{"tmdb":"348"}}],"TotalRecordCount":1001}`
		},
	})
	// Jellyfin has no provider-id query: every movie and series is paged
	matches, err := c.ItemsByProviderID(t.Context(), "TMDB", "348")
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 || len(matches) != 2 || f.requests[1].query.Get("startIndex") != "1000" {
		t.Errorf("%d pages, %d matches", pages, len(matches))
	}

	c, f = newFake(t, Emby, map[string]route{
		"GET /Items": ok(`{"Items":[{"Id":"1","Name":"Alien"}],"TotalRecordCount":1}`),
	})
	if _, err := c.ItemsByProviderID(t.Context(), "tmdb", "348"); err != nil {
		t.Fatal(err)
	}
	if q := f.requests[0].query; q.Get("AnyProviderIdEquals") != "tmdb.348" {
		t.Errorf("Emby lookup = %v", q)
	}
	// Emby drops an Ids filter it cannot parse and answers the whole library
	if _, err := c.ItemByID(t.Context(), "bogus"); err == nil || !strings.Contains(err.Error(), "no item with id bogus") {
		t.Errorf("ItemByID of an id the server ignored = %v", err)
	}
	it, err := c.ItemByID(t.Context(), "1")
	if err != nil || it.Name != "Alien" || f.requests[len(f.requests)-1].query.Get("Fields") != FieldsDetail {
		t.Errorf("ItemByID = %+v, %v", it, err)
	}
	// and the query is capped, so the whole library Emby answers for an id it
	// ignored is never actually pulled
	if q := f.requests[len(f.requests)-1].query; q.Get("Limit") != "2" {
		t.Errorf("ItemByID query = %v, want Limit=2", q)
	}
}

// A provider id names a film or a series. TMDB numbers its collections apart
// from its films, so collection 10 and film 10 are different things, and a
// box set, season or episode carrying the number is not the film: both
// servers are asked for films and series only.
func TestItemsByProviderIDFindsFilmsAndSeries(t *testing.T) {
	t.Parallel()

	library := []struct{ id, typ string }{{"m", "Movie"}, {"bs", "BoxSet"}, {"se", "Series"}, {"sn", "Season"}, {"ep", "Episode"}}
	// every item carries tmdb 10; the server narrows by type when asked
	items := func(r *http.Request, _ string) (int, string) {
		types := queryList(r.URL.Query(), "IncludeItemTypes")
		var out []string
		for _, it := range library {
			if len(types) == 0 || slices.Contains(types, it.typ) {
				out = append(out, `{"Id":"`+it.id+`","Type":"`+it.typ+`","ProviderIds":{"Tmdb":"10"}}`)
			}
		}
		return http.StatusOK, `{"Items":[` + strings.Join(out, ",") + `],"TotalRecordCount":` + strconv.Itoa(len(out)) + `}`
	}
	for _, backend := range []Backend{Emby, Jellyfin} {
		c, _ := newFake(t, backend, map[string]route{"GET /Items": items})
		found, err := c.ItemsByProviderID(t.Context(), "tmdb", "10")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, it := range found {
			got = append(got, it.Type)
		}
		if !slices.Equal(got, []string{"Movie", "Series"}) {
			t.Errorf("%s: tmdb 10 found %v, want the film and the series only", backend, got)
		}
	}
}

func TestCreateLibraryEmby(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Emby, map[string]route{
		"POST /Library/VirtualFolders": noContent,
		"GET /Libraries/AvailableOptions": ok(`{"TypeOptions":[{"Type":"Movie",
			"MetadataFetchers":[{"Name":"TheMovieDb","DefaultEnabled":true},{"Name":"OMDb","DefaultEnabled":false}],
			"ImageFetchers":[{"Name":"TheMovieDb","DefaultEnabled":true}]}]}`),
	})

	if err := c.CreateLibrary(t.Context(), LibrarySpec{Name: "Films", CollectionType: "movies", Paths: []string{"/media/movies"}}); err != nil {
		t.Fatal(err)
	}
	body := libraryBody(t, f.requests[0].body)
	if body.Name != "Films" || body.CollectionType != "movies" || string(body.RefreshLibrary) != "false" {
		t.Errorf("body = %s", f.requests[0].body)
	}
	// only what the client sets: the server's defaults apply to the rest
	if keys := slices.Sorted(maps.Keys(body.LibraryOptions)); !slices.Equal(keys, []string{"EnableChapterImageExtraction", "EnableRealtimeMonitor", "PathInfos", "TypeOptions"}) ||
		string(body.LibraryOptions["EnableRealtimeMonitor"]) != "false" {
		t.Errorf("LibraryOptions = %s", f.requests[0].body)
	}
	// providers off: an empty fetcher list per type, sent as []
	if !strings.Contains(f.requests[0].body, `"TypeOptions":[{"ImageFetchers":[],"MetadataFetchers":[],"Type":"Movie"}]`) {
		t.Errorf("TypeOptions = %s", f.requests[0].body)
	}

	// providers on: Emby lists its default fetchers, which the create posts
	if err := c.CreateLibrary(t.Context(), LibrarySpec{Name: "Films", CollectionType: "movies", Paths: []string{"/media/movies"}, Providers: true, Refresh: true}); err != nil {
		t.Fatal(err)
	}
	if q := f.only("GET /Libraries/AvailableOptions").query; q.Get("LibraryContentType") != "movies" || q.Get("IsNewLibrary") != "true" {
		t.Errorf("AvailableOptions query = %v", q)
	}
	last := f.requests[len(f.requests)-1].body
	if !strings.Contains(last, `"MetadataFetchers":["TheMovieDb"]`) || !strings.Contains(last, `"ImageFetcherOrder":["TheMovieDb"]`) || !strings.Contains(last, `"RefreshLibrary":true`) {
		t.Errorf("create with providers = %s", last)
	}

	// nfo saving asked for is listed; left out, it is not (above)
	if err := c.CreateLibrary(t.Context(), LibrarySpec{Name: "Films", CollectionType: "movies", Paths: []string{"/media/movies"}, SaveNfo: new(true)}); err != nil {
		t.Fatal(err)
	}
	if last = f.requests[len(f.requests)-1].body; string(libraryBody(t, last).LibraryOptions["MetadataSavers"]) != `["Nfo"]` {
		t.Errorf("create with save_nfo = %s", last)
	}
}

func TestCreateLibraryJellyfin(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Jellyfin, map[string]route{"POST /Library/VirtualFolders": noContent})
	if err := c.CreateLibrary(t.Context(), LibrarySpec{Name: "Mixed", CollectionType: "mixed", Paths: []string{"/a", "/b"}, Providers: true}); err != nil {
		t.Fatal(err)
	}
	r := f.requests[0]
	// name, type and paths go in the query; a mixed library names no type
	if r.query.Get("name") != "Mixed" || r.query.Has("collectionType") || !slices.Equal(r.query["paths"], []string{"/a", "/b"}) || r.query.Get("refreshLibrary") != "false" {
		t.Errorf("query = %v", r.query)
	}
	opts := libraryBody(t, r.body).LibraryOptions
	if string(opts["EnableTrickplayImageExtraction"]) != "false" || opts["TypeOptions"] != nil || opts["Enabled"] != nil || opts["MetadataSavers"] != nil {
		t.Errorf("LibraryOptions = %s (Jellyfin applies its own fetcher defaults, and Enabled and the savers are left to the server)", r.body)
	}

	// no nfo saving is an empty list: no list at all is every saver on Jellyfin
	if err := c.CreateLibrary(t.Context(), LibrarySpec{Name: "Mixed", CollectionType: "mixed", Paths: []string{"/a"}, SaveNfo: new(false)}); err != nil {
		t.Fatal(err)
	}
	if body := f.requests[len(f.requests)-1].body; string(libraryBody(t, body).LibraryOptions["MetadataSavers"]) != `[]` {
		t.Errorf("create with save_nfo false = %s", body)
	}
}

// A library's nfo saving is read off its saver list, and switched by posting
// its options back whole with only that list changed.
func TestLibraryNfoSaving(t *testing.T) {
	t.Parallel()

	options := `{"EnableRealtimeMonitor":false,"PreferredMetadataLanguage":"de","PathInfos":[{"Path":"/media/movies"}],
		"TypeOptions":[{"Type":"Movie","MetadataFetchers":[],"ImageFetchers":[]}]%s}`
	for _, tc := range []struct {
		backend      Backend
		list, update string
		savers       string // the listed options' savers
		saves        bool
	}{
		{Emby, "GET /Library/VirtualFolders/Query", "POST /Library/VirtualFolders/LibraryOptions", `,"MetadataSavers":[]`, false},
		{Emby, "GET /Library/VirtualFolders/Query", "POST /Library/VirtualFolders/LibraryOptions", `,"MetadataSavers":["Nfo"]`, true},
		{Jellyfin, "GET /Library/VirtualFolders", "POST /Library/VirtualFolders/LibraryOptions", `,"MetadataSavers":null`, true},
		{Jellyfin, "GET /Library/VirtualFolders", "POST /Library/VirtualFolders/LibraryOptions", `,"MetadataSavers":[]`, false},
	} {
		folder := `{"Name":"Films","ItemId":"7","Locations":["/media/movies"],"LibraryOptions":` + fmt.Sprintf(options, tc.savers) + `}`
		listing := `[` + folder + `]`
		if tc.backend == Emby {
			listing = `{"Items":[` + folder + `],"TotalRecordCount":1}`
		}
		c, f := newFake(t, tc.backend, map[string]route{tc.list: ok(listing), tc.update: noContent})

		folders, err := c.VirtualFolders(t.Context())
		if err != nil || len(folders) != 1 || folders[0].SavesNfo != tc.saves {
			t.Fatalf("%s with savers %s: folders = %+v, %v; want SavesNfo %v", tc.backend, tc.savers, folders, err, tc.saves)
		}
		if err := c.SetLibraryNfo(t.Context(), &folders[0], !tc.saves); err != nil {
			t.Fatal(err)
		}
		var body struct {
			ID             string `json:"Id"`
			LibraryOptions map[string]json.RawMessage
		}
		if err := json.Unmarshal([]byte(f.only(tc.update).body), &body); err != nil {
			t.Fatal(err)
		}
		want := `[]`
		if !tc.saves {
			want = `["Nfo"]`
		}
		o := body.LibraryOptions
		if body.ID != "7" || string(o["MetadataSavers"]) != want || string(o["PreferredMetadataLanguage"]) != `"de"` || !strings.Contains(string(o["TypeOptions"]), `"MetadataFetchers":[]`) {
			t.Errorf("%s turning nfo saving to %v posted %s", tc.backend, !tc.saves, f.only(tc.update).body)
		}
	}
}

// libraryBody decodes a library create body, keeping each option's JSON.
func libraryBody(t *testing.T, body string) (out struct {
	Name, CollectionType string
	RefreshLibrary       json.RawMessage
	LibraryOptions       map[string]json.RawMessage
},
) {
	t.Helper()

	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}

	return out
}

func TestLibraries(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Emby, map[string]route{
		"GET /Library/VirtualFolders/Query":   ok(`{"Items":[{"Name":"Films","ItemId":"9","CollectionType":"movies","Locations":["/media/movies"]}],"TotalRecordCount":1}`),
		"POST /Library/VirtualFolders/Delete": noContent,
	})
	folders, err := c.VirtualFolders(t.Context())
	if err != nil || len(folders) != 1 || folders[0].ItemID != "9" || folders[0].Locations[0] != "/media/movies" {
		t.Errorf("VirtualFolders = %+v, %v", folders, err)
	}
	// Emby identifies the library by id (a name is a 500)
	if err := c.DeleteLibrary(t.Context(), &VirtualFolder{Name: "Films", ItemID: "9"}); err != nil {
		t.Fatal(err)
	}
	if body := f.only("POST /Library/VirtualFolders/Delete").body; body != `{"Id":"9"}` {
		t.Errorf("delete = %s", body)
	}

	// Jellyfin keeps a removed library's items until its library scan finds
	// the folder gone, so the removal asks for that scan the way that is not
	// dropped when one is already running: on its own, not with the removal
	c, f = newFake(t, Jellyfin, map[string]route{"DELETE /Library/VirtualFolders": noContent, "POST /Library/Refresh": noContent})
	if err := c.DeleteLibrary(t.Context(), &VirtualFolder{Name: "Films", ItemID: "9"}); err != nil {
		t.Fatal(err)
	}
	if q := f.only("DELETE /Library/VirtualFolders").query; q.Get("name") != "Films" || q.Has("Id") || q.Get("refreshLibrary") != "false" {
		t.Errorf("delete = %v", q)
	}
	f.only("POST /Library/Refresh")
}

func TestEditItem(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Emby, map[string]route{
		"GET /Users/u1/Items/42": ok(`{"Id":"42","Name":"Princess Mononoke","Genres":[],"TagItems":[{"Name":"old","Id":3}],"LockData":false}`),
		"POST /Items/42":         noContent,
	})
	full, err := c.FullItem(t.Context(), "u1", "42")
	if err != nil {
		t.Fatal(err)
	}
	// the map keeps what the DTO holds, empty lists and explicit false included
	if locked, isBool := full["LockData"].(bool); full["Name"] != "Princess Mononoke" || full["Genres"] == nil || !isBool || locked {
		t.Errorf("FullItem = %v", full)
	}
	full["TagItems"] = []map[string]any{{"Name": "new"}}
	full["Tags"] = []string{"new"}
	if err := c.UpdateItem(t.Context(), "42", full); err != nil {
		t.Fatal(err)
	}
	// Emby reads TagItems on an update (emby-item-tag-items puts it on the DTO)
	body := f.only("POST /Items/42").body
	if !strings.Contains(body, `"TagItems":[{"Name":"new"}]`) || !strings.Contains(body, `"Genres":[]`) {
		t.Errorf("update = %s", body)
	}
	if err := c.UpdateItem(t.Context(), "42", map[string]any{"Name": 7}); err == nil || !strings.Contains(err.Error(), "item fields") {
		t.Errorf("an update with a wrongly typed field = %v", err)
	}

	c, f = newFake(t, Jellyfin, map[string]route{
		"GET /Items/42":  ok(`{"Id":"42","Name":"Princess Mononoke"}`),
		"POST /Items/42": noContent,
	})
	full, err = c.FullItem(t.Context(), "u1", "42")
	if err != nil || f.requests[0].query.Get("userId") != "u1" {
		t.Fatalf("FullItem = %v, %v", full, err)
	}
	full["Tags"] = []string{"new"}
	if err := c.UpdateItem(t.Context(), "42", full); err != nil {
		t.Fatal(err)
	}
	if body := f.only("POST /Items/42").body; !strings.Contains(body, `"Tags":["new"]`) {
		t.Errorf("update = %s", body)
	}
}

func TestPlaylists(t *testing.T) {
	t.Parallel()

	added := false
	c, f := newFake(t, Emby, map[string]route{
		"POST /Playlists": ok(`{"Id":"p1"}`),
		// what is added is read first, to tell a folder from an item
		"GET /Items": ok(`{"Items":[{"Id":"3","Type":"Movie","IsFolder":false},{"Id":"4","Type":"Movie","IsFolder":false}]}`),
		"POST /Playlists/p1/Items": func(*http.Request, string) (int, string) {
			added = true
			return http.StatusNoContent, ""
		},
		// the entries before the add and after, so the add is seen to stay
		"GET /Playlists/p1/Items": func(*http.Request, string) (int, string) {
			if added {
				return http.StatusOK, `{"Items":[{"Id":"1"},{"Id":"2"},{"Id":"3"},{"Id":"4"}]}`
			}
			return http.StatusOK, `{"Items":[{"Id":"1"},{"Id":"2"}]}`
		},
	})
	c.settle = time.Millisecond
	id, err := c.CreatePlaylist(t.Context(), "Mix", []string{"1", "2"}, "Video", "u1")
	if err != nil || id != "p1" {
		t.Fatalf("CreatePlaylist = %q, %v", id, err)
	}
	// Emby's owner rides along as a query parameter (emby-playlist-create-user-id)
	if q := f.only("POST /Playlists").query; q.Get("UserId") != "u1" || q.Get("Ids") != "1,2" || q.Get("MediaType") != "Video" {
		t.Errorf("Emby create = %v", q)
	}
	if err := c.AddToPlaylist(t.Context(), "p1", []string{"3", "4"}, "u1"); err != nil {
		t.Fatal(err)
	}
	if q := f.only("POST /Playlists/p1/Items").query; q.Get("Ids") != "3,4" {
		t.Errorf("Emby add = %v", q)
	}

	var p2Removed bool
	c, f = newFake(t, Jellyfin, map[string]route{
		"POST /Playlists": ok(`{"Id":"p2"}`),
		"DELETE /Playlists/p2/Items": func(*http.Request, string) (int, string) {
			p2Removed = true
			return http.StatusNoContent, ""
		},
		"GET /Playlists/p2/Items": func(*http.Request, string) (int, string) {
			if p2Removed {
				return http.StatusOK, `{"Items":[],"TotalRecordCount":0}`
			}
			return http.StatusOK, `{"Items":[{"Id":"a","PlaylistItemId":"e1"},{"Id":"b","PlaylistItemId":"e2"}],"TotalRecordCount":2}`
		},
		"POST /Collections": ok(`{"Id":"c1"}`),
	})
	c.settle = time.Millisecond
	if id, err := c.CreatePlaylist(t.Context(), "Mix", []string{"a"}, "Audio", "u1"); err != nil || id != "p2" {
		t.Fatalf("CreatePlaylist = %q, %v", id, err)
	}
	// Jellyfin wants a JSON body and answers 400 to the query form
	r := f.only("POST /Playlists")
	if len(r.query) != 0 || !strings.Contains(r.body, `"UserId":"u1"`) || !strings.Contains(r.body, `"MediaType":"Audio"`) {
		t.Errorf("Jellyfin create = %v %s", r.query, r.body)
	}
	items, total, err := c.PlaylistItems(t.Context(), "p2", "u1")
	if err != nil || total != 2 || items[0].PlaylistItemID != "e1" {
		t.Errorf("PlaylistItems = %+v, %d, %v", items, total, err)
	}
	if n, err := c.RemoveFromPlaylist(t.Context(), "p2", "u1", []string{"e1", "e2"}); err != nil || n != 2 {
		t.Fatalf("RemoveFromPlaylist = %d, %v", n, err)
	}
	if q := f.only("DELETE /Playlists/p2/Items").query; !slices.Equal(q["entryIds"], []string{"e1", "e2"}) {
		t.Errorf("remove = %v", q)
	}
	if id, err := c.CreateCollection(t.Context(), "Set", []string{"a", "b"}); err != nil || id != "c1" {
		t.Errorf("CreateCollection = %q, %v", id, err)
	}
}

func TestAddToPlaylist(t *testing.T) {
	t.Parallel()

	// the library the playlist is filled from: a series whose season holds
	// two episodes and the record of a missing one, a season with nothing in
	// it, an album holding two songs and a music video, and the songs' artist
	type libraryItem struct {
		typ, parent, media, artist string
		folder, virtual            bool
	}
	library := map[string]libraryItem{
		"a": {typ: "Movie", media: "Video"}, "b": {typ: "Movie", media: "Video"}, "c": {typ: "Movie", media: "Video"},
		"s": {typ: "Series", folder: true}, "s1": {typ: "Season", parent: "s", folder: true}, "s2": {typ: "Season", parent: "s", folder: true},
		"e1": {typ: "Episode", parent: "s1", media: "Video"}, "e2": {typ: "Episode", parent: "s1", media: "Video"},
		"e3": {typ: "Episode", parent: "s1", media: "Video", virtual: true},
		"al": {typ: "MusicAlbum", folder: true}, "ar": {typ: "MusicArtist", folder: true},
		"t1": {typ: "Audio", parent: "al", media: "Audio", artist: "ar"}, "t2": {typ: "Audio", parent: "al", media: "Audio", artist: "ar"},
		"mv": {typ: "MusicVideo", parent: "al", media: "Video"},
	}
	under := func(id, ancestor string) bool {
		for p := library[id].parent; p != ""; p = library[p].parent {
			if p == ancestor {
				return true
			}
		}
		return false
	}
	dto := func(id string) string {
		it := library[id]
		location := "FileSystem"
		if it.virtual {
			location = "Virtual"
		}
		return fmt.Sprintf(`{"Id":%q,"Name":"Item %s","Type":%q,"IsFolder":%t,"MediaType":%q,"LocationType":%q}`, id, id, it.typ, it.folder, it.media, location)
	}
	// items answers the item query the way both servers read it
	items := func(r *http.Request, _ string) (int, string) {
		q := r.URL.Query()
		ids, filters, parent := queryList(q, "Ids"), queryList(q, "Filters"), queryList(q, "ParentId")
		media, artists, types := queryList(q, "MediaTypes"), queryList(q, "ArtistIds"), queryList(q, "IncludeItemTypes")
		var out []string
		for _, id := range slices.Sorted(maps.Keys(library)) {
			it := library[id]
			if len(ids) > 0 && !slices.Contains(ids, id) ||
				slices.Contains(filters, "IsFolder") && !it.folder || slices.Contains(filters, "IsNotFolder") && it.folder ||
				len(parent) > 0 && !under(id, parent[0]) || len(media) > 0 && !slices.Contains(media, it.media) ||
				len(artists) > 0 && !slices.Contains(artists, it.artist) || len(types) > 0 && !slices.Contains(types, it.typ) {
				continue
			}
			out = append(out, dto(id))
		}
		return http.StatusOK, `{"Items":[` + strings.Join(out, ",") + `],"TotalRecordCount":` + strconv.Itoa(len(out)) + `}`
	}

	// a playlist whose entries follow the adds the server keeps, expanding
	// what it is handed the way both servers do: an artist is its songs, any
	// other folder the items anywhere beneath it of the playlist's media type
	// (the missing episode's record included), anything else itself. lose
	// says how many adds are answered, applied and then put back by a refresh
	playlist := func(t *testing.T, backend Backend, mediaType string, lose int) (*Client, *fake) {
		t.Helper()

		held := []string{"a"}
		expand := func(id string) []string {
			var out []string
			for _, leaf := range slices.Sorted(maps.Keys(library)) {
				it := library[leaf]
				switch {
				case library[id].typ == "MusicArtist":
					if it.artist == id {
						out = append(out, leaf)
					}
				case !it.folder && under(leaf, id) && it.media == mediaType:
					out = append(out, leaf)
				}
			}
			if !library[id].folder {
				return []string{id}
			}
			return out
		}
		playlistItem := ok(`{"Id":"p1","Name":"Mix","Type":"Playlist","IsFolder":true,"MediaType":"` + mediaType + `"}`)
		c, f := newFake(t, backend, map[string]route{
			"GET /Items":             items, // Emby's without a user, and Jellyfin's
			"GET /Users/u1/Items":    items, // Emby's in a user's view
			"GET /Users/u1/Items/p1": playlistItem,
			"GET /Items/p1":          playlistItem,
			"GET /Playlists/p1/Items": func(*http.Request, string) (int, string) {
				entries := make([]string, 0, len(held))
				for _, id := range held {
					entries = append(entries, `{"Id":"`+id+`"}`)
				}
				return http.StatusOK, `{"Items":[` + strings.Join(entries, ",") + `]}`
			},
			"POST /Playlists/p1/Items": func(r *http.Request, _ string) (int, string) {
				if lose > 0 {
					lose--
					return http.StatusNoContent, ""
				}
				for _, id := range queryList(r.URL.Query(), "Ids") {
					held = append(held, expand(id)...)
				}
				return http.StatusNoContent, ""
			},
		})
		c.settle = time.Millisecond

		return c, f
	}
	adds := func(f *fake) []string {
		var got []string
		for _, r := range f.requests {
			if r.method == http.MethodPost {
				got = append(got, r.query.Get("Ids")+strings.Join(r.query["ids"], ","))
			}
		}
		return got
	}
	holds := func(c *Client) []string {
		entries, _, err := c.PlaylistItems(t.Context(), "p1", "u1")
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(entries))
		for _, e := range entries {
			ids = append(ids, e.ID)
		}
		return ids
	}

	// kept at once: one add; a copy of an item already there counts as new
	c, f := playlist(t, Emby, "Video", 0)
	if err := c.AddToPlaylist(t.Context(), "p1", []string{"a", "b"}, "u1"); err != nil {
		t.Fatal(err)
	}
	if got := adds(f); !slices.Equal(got, []string{"a,b"}) {
		t.Errorf("adds = %v", got)
	}

	// lost once: what is missing is sent again, and only that
	c, f = playlist(t, Jellyfin, "Video", 1)
	if err := c.AddToPlaylist(t.Context(), "p1", []string{"b", "c"}, "u1"); err != nil {
		t.Fatal(err)
	}
	if got := adds(f); !slices.Equal(got, []string{"b,c", "b,c"}) {
		t.Errorf("adds = %v", got)
	}

	// never kept: an error naming what is missing, not a success
	c, f = playlist(t, Emby, "Video", 2)
	if err := c.AddToPlaylist(t.Context(), "p1", []string{"b"}, "u1"); err == nil || !strings.Contains(err.Error(), "did not keep b") {
		t.Errorf("an add the server never keeps = %v", err)
	}
	if got := adds(f); len(got) != 2 {
		t.Errorf("adds = %v, want two", got)
	}

	// a series goes in as its episodes, which is what is checked: the series
	// itself never shows in the playlist, and is sent once
	for _, backend := range []Backend{Emby, Jellyfin} {
		c, f = playlist(t, backend, "Video", 0)
		if err := c.AddToPlaylist(t.Context(), "p1", []string{"s"}, "u1"); err != nil {
			t.Fatalf("%s: adding a series = %v", backend, err)
		}
		if got := adds(f); !slices.Equal(got, []string{"s"}) {
			t.Errorf("%s: adds = %v, want the series sent once", backend, got)
		}
		if got := holds(c); !slices.Equal(got, []string{"a", "e1", "e2", "e3"}) {
			t.Errorf("%s: the playlist holds %v", backend, got)
		}

		// lost once: the episodes missing go again, never the series, which
		// would put every episode in twice
		c, f = playlist(t, backend, "Video", 1)
		if err := c.AddToPlaylist(t.Context(), "p1", []string{"s"}, "u1"); err != nil {
			t.Fatalf("%s: adding a series the server lost once = %v", backend, err)
		}
		if got := adds(f); !slices.Equal(got, []string{"s", "e1,e2"}) {
			t.Errorf("%s: adds = %v, want the series, then its episodes", backend, got)
		}
		if got := holds(c); !slices.Equal(got, []string{"a", "e1", "e2"}) {
			t.Errorf("%s: the playlist holds %v", backend, got)
		}
	}

	// an album is its songs of the playlist's media type, an artist their
	// songs, both beside a plain item in one add
	c, f = playlist(t, Jellyfin, "Audio", 0)
	if err := c.AddToPlaylist(t.Context(), "p1", []string{"al", "ar", "b"}, "u1"); err != nil {
		t.Fatal(err)
	}
	if got := holds(c); !slices.Equal(got, []string{"a", "t1", "t2", "t1", "t2", "b"}) || len(adds(f)) != 1 {
		t.Errorf("the playlist holds %v after %v", got, adds(f))
	}

	// a folder holding nothing the playlist can take is an error, and
	// nothing is sent
	c, f = playlist(t, Emby, "Video", 0)
	if err := c.AddToPlaylist(t.Context(), "p1", []string{"b", "s2"}, "u1"); err == nil || !strings.Contains(err.Error(), "holds nothing") {
		t.Errorf("adding an empty season = %v", err)
	}
	if got := adds(f); len(got) != 0 {
		t.Errorf("adds = %v, want none", got)
	}
}

func TestRemoveFromCollection(t *testing.T) {
	t.Parallel()

	// a collection whose members change as the removals the server applies
	// land: ignore says how many removals it answers 204 to without applying
	collection := func(t *testing.T, backend Backend, ignore int) (*Client, *fake) {
		t.Helper()

		held := []string{"a", "b", "c"}
		c, f := newFake(t, backend, map[string]route{
			"GET /Items": func(*http.Request, string) (int, string) {
				items := make([]string, 0, len(held))
				for _, id := range held {
					items = append(items, `{"Id":"`+id+`"}`)
				}
				return http.StatusOK, `{"Items":[` + strings.Join(items, ",") + `]}`
			},
			"DELETE /Collections/c1/Items": func(r *http.Request, _ string) (int, string) {
				if ignore > 0 {
					ignore--
					return http.StatusNoContent, ""
				}
				gone := strings.Split(r.URL.Query().Get("Ids"), ",")
				gone = append(gone, r.URL.Query()["ids"]...)
				held = slices.DeleteFunc(held, func(id string) bool { return slices.Contains(gone, id) })
				return http.StatusNoContent, ""
			},
		})
		c.settle = time.Millisecond

		return c, f
	}
	removals := func(f *fake) int {
		n := 0
		for _, r := range f.requests {
			if r.method == http.MethodDelete {
				n++
			}
		}
		return n
	}

	// Emby joins the ids, Jellyfin repeats the key
	c, f := collection(t, Emby, 0)
	if err := c.RemoveFromCollection(t.Context(), "c1", []string{"a", "c"}); err != nil {
		t.Fatal(err)
	}
	if q := f.only("DELETE /Collections/c1/Items").query; q.Get("Ids") != "a,c" {
		t.Errorf("Emby remove = %v", q)
	}
	c, f = collection(t, Jellyfin, 0)
	if err := c.RemoveFromCollection(t.Context(), "c1", []string{"a", "c"}); err != nil {
		t.Fatal(err)
	}
	if q := f.only("DELETE /Collections/c1/Items").query; !slices.Equal(q["ids"], []string{"a", "c"}) {
		t.Errorf("Jellyfin remove = %v", q)
	}

	// an item the collection does not hold is an error, and nothing is sent
	c, f = collection(t, Jellyfin, 0)
	if err := c.RemoveFromCollection(t.Context(), "c1", []string{"a", "z"}); err == nil || !strings.Contains(err.Error(), "does not hold item z") {
		t.Errorf("removing a non-member = %v", err)
	}
	if n := removals(f); n != 0 {
		t.Errorf("%d removals sent for a non-member", n)
	}

	// a removal answered but not applied is sent once more
	c, f = collection(t, Jellyfin, 1)
	if err := c.RemoveFromCollection(t.Context(), "c1", []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if n := removals(f); n != 2 {
		t.Errorf("%d removals sent, want the retry", n)
	}

	// and one that never lands is an error, not a success
	c, f = collection(t, Emby, 2)
	if err := c.RemoveFromCollection(t.Context(), "c1", []string{"b"}); err == nil || !strings.Contains(err.Error(), "did not remove b") {
		t.Errorf("a removal the server never applies = %v", err)
	}
	if n := removals(f); n != 2 {
		t.Errorf("%d removals sent, want two", n)
	}
}

func TestUserState(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Emby, map[string]route{
		"POST /Users/u1/PlayedItems/42":     ok(`{}`),
		"DELETE /Users/u1/FavoriteItems/42": ok(`{}`),
		"GET /Shows/NextUp":                 ok(`{"Items":[{"Id":"e2"}]}`),
		"GET /Users/u1/Items/Resume":        ok(`{"Items":[]}`),
	})
	if err := c.SetPlayed(t.Context(), "u1", "42", true); err != nil {
		t.Fatal(err)
	}
	if err := c.SetFavourite(t.Context(), "u1", "42", false); err != nil {
		t.Fatal(err)
	}
	if next, err := c.NextUp(t.Context(), "u1", 3); err != nil || len(next) != 1 {
		t.Fatalf("NextUp = %v, %v", next, err)
	}
	// Emby 4.10's default next-up mode lists nothing for an API key
	if q := f.only("GET /Shows/NextUp").query; q.Get("LegacyNextUp") != "true" || q.Get("UserId") != "u1" || q.Get("Limit") != "3" {
		t.Errorf("NextUp = %v", q)
	}
	if _, err := c.Resume(t.Context(), "u1", 0); err != nil {
		t.Fatal(err)
	}
	if q := f.only("GET /Users/u1/Items/Resume").query; q.Get("Recursive") != "true" || q.Get("MediaTypes") != "Video" || q.Has("Limit") {
		t.Errorf("Resume = %v", q)
	}

	c, f = newFake(t, Jellyfin, map[string]route{
		"DELETE /UserPlayedItems/42": ok(`{}`),
		"POST /UserFavoriteItems/42": ok(`{}`),
		"GET /UserItems/Resume":      ok(`{"Items":[{"Id":"a"}]}`),
		"GET /Users":                 ok(`[{"Id":"u2","Name":"alice","Policy":{"IsAdministrator":false}},{"Id":"u1","Name":"root","Policy":{"IsAdministrator":true}}]`),
	})
	if err := c.SetPlayed(t.Context(), "u1", "42", false); err != nil {
		t.Fatal(err)
	}
	if err := c.SetFavourite(t.Context(), "u1", "42", true); err != nil {
		t.Fatal(err)
	}
	if q := f.only("POST /UserFavoriteItems/42").query; q.Get("userId") != "u1" {
		t.Errorf("favourite = %v", q)
	}
	if items, err := c.Resume(t.Context(), "u1", 5); err != nil || len(items) != 1 || f.only("GET /UserItems/Resume").query.Get("mediaTypes") != "Video" {
		t.Errorf("Resume = %v, %v", items, err)
	}
	if u, err := c.ResolveUser(t.Context(), ""); err != nil || u.Name != "root" {
		t.Errorf("the default user = %+v, %v (want the first administrator)", u, err)
	}
	if u, err := c.ResolveUser(t.Context(), "ALICE"); err != nil || u.ID != "u2" {
		t.Errorf("ResolveUser(ALICE) = %+v, %v", u, err)
	}
	if _, err := c.ResolveUser(t.Context(), "bob"); err == nil || !strings.Contains(err.Error(), "have: alice, root") {
		t.Errorf("an unknown user = %v", err)
	}
}

func TestSessions(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Emby, map[string]route{
		"GET /Sessions":                  ok(`[{"Id":"s1","PlayState":{"IsPaused":true,"PositionTicks":5},"NowPlayingItem":{"Id":"1","Name":"Princess Mononoke"}}]`),
		"POST /Sessions/s1/Playing":      noContent,
		"POST /Sessions/s1/Playing/Seek": noContent,
		"POST /Sessions/s1/Message":      noContent,
	})
	sessions, err := c.Sessions(t.Context())
	if err != nil || len(sessions) != 1 || !sessions[0].PlayState.IsPaused || sessions[0].NowPlayingItem.Name != "Princess Mononoke" {
		t.Fatalf("Sessions = %+v, %v", sessions, err)
	}
	if err := c.Play(t.Context(), "s1", []string{"1", "22"}, "PlayNow"); err != nil {
		t.Fatal(err)
	}
	// Emby reads one comma-separated value (emby-comma-separated-arrays)
	if q := f.only("POST /Sessions/s1/Playing").query; q.Get("ItemIds") != "1,22" || q.Get("PlayCommand") != "PlayNow" {
		t.Errorf("Play = %v", q)
	}
	if err := c.Play(t.Context(), "s1", []string{"abc"}, "PlayNow"); err == nil || !strings.Contains(err.Error(), "not an Emby id") {
		t.Errorf("Play with a non-numeric id = %v", err)
	}
	if err := c.PlayCommand(t.Context(), "s1", "Seek", 900); err != nil {
		t.Fatal(err)
	}
	if r := f.only("POST /Sessions/s1/Playing/Seek"); r.body != `{"SeekPositionTicks":900}` || r.query.Get("SeekPositionTicks") != "900" {
		t.Errorf("Emby seek = %v %s", r.query, r.body)
	}
	if err := c.Message(t.Context(), "s1", "Hi", "there", 5000); err != nil {
		t.Fatal(err)
	}
	if q := f.only("POST /Sessions/s1/Message").query; q.Get("Text") != "there" || q.Get("TimeoutMs") != "5000" {
		t.Errorf("Emby message = %v", q)
	}

	c, f = newFake(t, Jellyfin, map[string]route{
		"POST /Sessions/s1/Playing":      noContent,
		"POST /Sessions/s1/Playing/Seek": noContent,
		"POST /Sessions/s1/Message":      noContent,
	})
	if err := c.Play(t.Context(), "s1", []string{"a", "b"}, "PlayNext"); err != nil {
		t.Fatal(err)
	}
	if q := f.only("POST /Sessions/s1/Playing").query; !slices.Equal(q["itemIds"], []string{"a", "b"}) || q.Get("playCommand") != "PlayNext" {
		t.Errorf("Play = %v", q)
	}
	if err := c.PlayCommand(t.Context(), "s1", "Seek", 900); err != nil {
		t.Fatal(err)
	}
	if q := f.only("POST /Sessions/s1/Playing/Seek").query; q.Get("seekPositionTicks") != "900" {
		t.Errorf("Jellyfin seek = %v", q)
	}
	if err := c.Message(t.Context(), "s1", "Hi", "there", 0); err != nil {
		t.Fatal(err)
	}
	if body := jsonBody(t, f.only("POST /Sessions/s1/Message").body); body["Text"] != "there" || body["Header"] != "Hi" {
		t.Errorf("Jellyfin message = %v", body)
	}
}

// A seek to the start is a position of 0, which Emby's body model leaves out
// as unset: the position goes in the query too, where Emby's own web client
// sends it and where 0 is sent. Other commands send no position at all.
func TestSeekToTheStart(t *testing.T) {
	t.Parallel()

	for _, backend := range []Backend{Emby, Jellyfin} {
		c, f := newFake(t, backend, map[string]route{
			"POST /Sessions/s1/Playing/Seek":  noContent,
			"POST /Sessions/s1/Playing/Pause": noContent,
		})
		if err := c.PlayCommand(t.Context(), "s1", "Seek", 0); err != nil {
			t.Fatal(err)
		}
		if got := queryList(f.only("POST /Sessions/s1/Playing/Seek").query, "SeekPositionTicks"); !slices.Equal(got, []string{"0"}) {
			t.Errorf("%s: a seek to the start sent position %v, want 0", backend, got)
		}
		if err := c.PlayCommand(t.Context(), "s1", "Pause", 0); err != nil {
			t.Fatal(err)
		}
		if got := queryList(f.only("POST /Sessions/s1/Playing/Pause").query, "SeekPositionTicks"); len(got) != 0 {
			t.Errorf("%s: a pause sent position %v", backend, got)
		}
	}
}

func TestServer(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Emby, map[string]route{
		"GET /ScheduledTasks":             ok(`[{"Id":"t1","Name":"Scan media library","State":"Idle","LastExecutionResult":{"Status":"Completed"}}]`),
		"POST /ScheduledTasks/Running/t1": noContent,
		"GET /System/Logs/embyserver.txt": ok("line one\nline two\n"),
		"GET /System/ActivityLog/Entries": ok(`{"Items":[{"Name":"login","Date":"2026-09-14T00:00:00Z"}],"TotalRecordCount":1}`),
		"GET /System/Info":                ok(`{"ServerName":"nas","Version":"4.10"}`),
		"POST /Library/Refresh":           noContent,
	})
	task, err := c.RunTask(t.Context(), "scan MEDIA library")
	if err != nil || task.ID != "t1" {
		t.Fatalf("RunTask = %+v, %v", task, err)
	}
	f.only("POST /ScheduledTasks/Running/t1")
	if _, err := c.RunTask(t.Context(), "defrag"); err == nil || !strings.Contains(err.Error(), "have: Scan media library") {
		t.Errorf("an unknown task = %v", err)
	}
	if text, err := c.LogTail(t.Context(), "embyserver.txt", 5); err != nil || text != "line one\nline two" || f.only("GET /System/Logs/embyserver.txt").path == "" {
		t.Errorf("LogTail = %q, %v", text, err)
	}
	since := time.Date(2026, 9, 14, 12, 0, 0, 0, time.FixedZone("x", 3600))
	if entries, total, err := c.ActivityLog(t.Context(), since, 10, 0); err != nil || total != 1 || entries[0].Name != "login" {
		t.Errorf("ActivityLog = %v, %d, %v", entries, total, err)
	}
	if q := f.only("GET /System/ActivityLog/Entries").query; q.Get("MinDate") != "2026-09-14T11:00:00Z" || q.Get("Limit") != "10" {
		t.Errorf("activity query = %v", q)
	}
	if info, err := c.SystemInfo(t.Context()); err != nil || info.ServerName != "nas" {
		t.Errorf("SystemInfo = %+v, %v", info, err)
	}
	if err := c.RefreshLibrary(t.Context()); err != nil {
		t.Error(err)
	}
}

// A log runs to tens of megabytes on a busy server, and its tail is the part
// worth reading: the whole of it is read through, keeping only the last lines,
// so a log past any read cap still answers its true last lines, whole.
func TestLogTailReadsABigLogToItsEnd(t *testing.T) {
	t.Parallel()

	// just over 40 MiB of numbered lines, made as they are sent
	const size = 40 << 20
	var lines atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var buf strings.Builder
		for sent := 0; sent < size; {
			buf.Reset()
			for range 1000 {
				fmt.Fprintf(&buf, "2026-09-23 12:00:00.000 Info Server: request %09d answered in 12ms\n", lines.Add(1))
			}
			n, err := io.WriteString(w, buf.String())
			if err != nil {
				return
			}
			sent += n
		}
	}))
	t.Cleanup(srv.Close)

	for _, backend := range []Backend{Emby, Jellyfin} {
		lines.Store(0)
		c, err := New(backend, srv.URL, "tok")
		if err != nil {
			t.Fatal(err)
		}
		tail, err := c.LogTail(t.Context(), "embyserver.txt", 3)
		if err != nil {
			t.Fatal(err)
		}
		last := lines.Load()
		want := make([]string, 0, 3)
		for n := last - 2; n <= last; n++ {
			want = append(want, fmt.Sprintf("2026-09-23 12:00:00.000 Info Server: request %09d answered in 12ms", n))
		}
		if tail != strings.Join(want, "\n") {
			t.Errorf("%s: the tail of %d lines = %q, want the last three", backend, last, tail)
		}
	}
}

// The tail is the last lines as the file ends them: a Windows server's
// carriage returns go, blank lines at the very end do not count, one in the
// middle does, and a line too long to keep whole keeps its start.
func TestTailLines(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", maxLogLine+10)
	for _, tc := range []struct {
		name, log string
		n         int
		want      string
	}{
		{"fewer lines than asked", "one\ntwo\n", 5, "one\ntwo"},
		{"the last n", "one\ntwo\nthree\nfour\n", 2, "three\nfour"},
		{"no newline at the end", "one\ntwo\nthree", 2, "two\nthree"},
		{"carriage returns", "one\r\ntwo\r\nthree\r\n", 2, "two\nthree"},
		{"blank lines at the end", "one\ntwo\n\n\n\n", 2, "one\ntwo"},
		{"a blank line inside", "one\n\ntwo\n", 3, "one\n\ntwo"},
		{"a line too long", "one\n" + long + "\nthree\n", 2, long[:maxLogLine] + "...\nthree"},
		{"empty", "", 3, ""},
	} {
		got, err := tailLines(strings.NewReader(tc.log), tc.n)
		if err != nil || got != tc.want {
			t.Errorf("%s: tail = %q, %v, want %q", tc.name, got, err, tc.want)
		}
	}

	// and what is kept stays within its bound however many lines are asked
	var big strings.Builder
	line := strings.Repeat("y", maxLogLine-1) + "\n"
	for big.Len() <= maxTailBytes+4*maxLogLine {
		big.WriteString(line)
	}
	got, err := tailLines(strings.NewReader(big.String()), 1_000_000)
	if err != nil || len(got) > maxTailBytes || len(got) < maxTailBytes-2*maxLogLine {
		t.Errorf("a tail of every line = %d bytes, %v, want it held to about %d", len(got), err, maxTailBytes)
	}
}

func TestErrorsReachTheCaller(t *testing.T) {
	t.Parallel()

	c, _ := newFake(t, Jellyfin, map[string]route{
		"GET /Items/404":              answer(http.StatusNotFound, "gone"),
		"POST /Items/42/Refresh":      answer(http.StatusForbidden, "no"),
		"GET /Library/VirtualFolders": answer(http.StatusUnauthorized, ""),
	})
	if _, err := c.FullItem(t.Context(), "u", "404"); !client.IsNotFound(err) {
		t.Errorf("a 404 = %v", err)
	}
	if err := c.RefreshItem(t.Context(), "42", true); client.StatusCode(err) != http.StatusForbidden || !strings.Contains(err.Error(), "lacks permission") {
		t.Errorf("a 403 = %v", err)
	}
	if _, err := c.VirtualFolders(t.Context()); client.StatusCode(err) != http.StatusUnauthorized {
		t.Errorf("a 401 = %v", err)
	}
}

func TestRemoteSearch(t *testing.T) {
	t.Parallel()

	// the item read back answers with the ids the apply left it
	ids := `{}`
	c, f := newFake(t, Emby, map[string]route{
		"GET /Items": func(*http.Request, string) (int, string) {
			return http.StatusOK, `{"Items":[{"Id":"17","Name":"Princess Mononoke","ProductionYear":1997,"ProviderIds":` + ids + `}],"TotalRecordCount":1}`
		},
		"POST /Items/RemoteSearch/Movie": ok(`[{"Name":"Princess Mononoke","ProductionYear":1997,"ProviderIds":{"Tmdb":"128"}}]`),
		"POST /Items/RemoteSearch/Apply/17": func(*http.Request, string) (int, string) {
			ids = `{"Tmdb":"128"}`
			return http.StatusNoContent, ""
		},
	})
	c.settle = time.Millisecond
	if _, err := c.RemoteSearch(t.Context(), "book", "17", "", 0); err == nil || !strings.Contains(err.Error(), "unsupported identify kind") {
		t.Errorf("an unsupported kind = %v", err)
	}
	results, err := c.RemoteSearch(t.Context(), "movie", "17", "", 0)
	if err != nil || len(results) != 1 || results[0].ProviderIDs["Tmdb"] != "128" {
		t.Fatalf("RemoteSearch = %+v, %v", results, err)
	}
	// Emby does not fall back to the item for an empty search, so the name
	// and year come from it; its document types ItemId as a number
	if body := f.only("POST /Items/RemoteSearch/Movie").body; body != `{"ItemId":17,"SearchInfo":{"Name":"Princess Mononoke","Year":1997}}` {
		t.Errorf("search body = %s", body)
	}
	it, err := c.ApplyRemoteSearchResult(t.Context(), "17", results[0], true)
	if err != nil || it.ProviderIDs["Tmdb"] != "128" {
		t.Fatalf("ApplyRemoteSearchResult = %+v, %v", it, err)
	}
	if r := f.only("POST /Items/RemoteSearch/Apply/17"); r.query.Get("ReplaceAllImages") != "true" || !strings.Contains(r.body, `"Tmdb":"128"`) {
		t.Errorf("apply = %v %s", r.query, r.body)
	}

	// an nfo that names the item wins on Emby: its ids stay, the candidate's
	// others are added, and the apply is answered all the same
	ids = `{"Tmdb":"841"}`
	c, _ = newFake(t, Emby, map[string]route{
		"GET /Items": func(*http.Request, string) (int, string) {
			return http.StatusOK, `{"Items":[{"Id":"17","Name":"Dune","ProviderIds":` + ids + `}],"TotalRecordCount":1}`
		},
		"POST /Items/RemoteSearch/Apply/17": func(*http.Request, string) (int, string) {
			ids = `{"Tmdb":"841","Eidr":"10.5240/D15F"}`
			return http.StatusNoContent, ""
		},
	})
	c.settle = time.Millisecond
	_, err = c.ApplyRemoteSearchResult(t.Context(), "17", RemoteSearchResult{Name: "Dune", ProviderIDs: map[string]string{"Tmdb": "438631"}}, false)
	if err == nil || !strings.Contains(err.Error(), "tmdb id is 841, not 438631") || !strings.Contains(err.Error(), "nfo") {
		t.Errorf("an apply the server did not take = %v", err)
	}
}

func TestSearchFilters(t *testing.T) {
	t.Parallel()

	page := `{"Items":[{"Id":"1","Name":"Princess Mononoke","Studios":[{"Name":"Studio Ghibli","Id":7}],"OfficialRating":"PG-13","CommunityRating":8.3,
		"UserData":{"PlaybackPositionTicks":600000000,"PlayedPercentage":42.5,"UnplayedItemCount":3}}],"TotalRecordCount":1}`
	c, f := newFake(t, Emby, map[string]route{"GET /Items": ok(page)})
	items, _, err := c.Search(t.Context(), SearchOptions{
		Genres: []string{"Crime", "Drama, Thriller"}, Tags: []string{"heist"}, Studios: []string{"A24", "Regency"}, OfficialRatings: []string{"R"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Emby reads each as one pipe-delimited value, so a comma stays in its name
	q := f.only("GET /Items").query
	for k, want := range map[string]string{"Genres": "Crime|Drama, Thriller", "Tags": "heist", "Studios": "A24|Regency", "OfficialRatings": "R"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	it := items[0]
	if !slices.Equal(it.StudioNames(), []string{"Studio Ghibli"}) || it.OfficialRating != "PG-13" || it.CommunityRating < 8.2 ||
		it.UserData.PlayedPercentage != 42.5 || it.UserData.UnplayedItemCount != 3 {
		t.Errorf("item = %+v %+v", it, it.UserData)
	}

	c, f = newFake(t, Jellyfin, map[string]route{
		"GET /Items": ok(`{"Items":[{"Id":"a","Studios":[{"Name":"A24","Id":"s1"}]}],"TotalRecordCount":1}`),
	})
	items, _, err = c.Search(t.Context(), SearchOptions{Genres: []string{"Crime", "Drama"}, Studios: []string{"A24"}, OfficialRatings: []string{"R", "PG-13"}})
	if err != nil {
		t.Fatal(err)
	}
	// Jellyfin takes one key per value
	q = f.only("GET /Items").query
	if !slices.Equal(q["genres"], []string{"Crime", "Drama"}) || !slices.Equal(q["studios"], []string{"A24"}) || !slices.Equal(q["officialRatings"], []string{"R", "PG-13"}) || q.Has("tags") {
		t.Errorf("query = %v", q)
	}
	if !slices.Equal(items[0].StudioNames(), []string{"A24"}) {
		t.Errorf("studios = %+v", items[0].Studios)
	}
}

func TestSetProgress(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Emby, map[string]route{"POST /Users/u1/Items/42/UserData": noContent})
	if err := c.SetProgress(t.Context(), "u1", "42", 0); err == nil || !strings.Contains(err.Error(), "above zero") || len(f.requests) != 0 {
		t.Errorf("a zero position = %v (%d requests)", err, len(f.requests))
	}
	if err := c.SetProgress(t.Context(), "u1", "42", 900); err != nil {
		t.Fatal(err)
	}
	// the position and the played flag, which Emby takes from the post
	if body := f.only("POST /Users/u1/Items/42/UserData").body; body != `{"PlaybackPositionTicks":900,"Played":false}` {
		t.Errorf("Emby user data = %s", body)
	}

	c, f = newFake(t, Jellyfin, map[string]route{"POST /UserItems/42/UserData": ok(`{}`)})
	if err := c.SetProgress(t.Context(), "u1", "42", 900); err != nil {
		t.Fatal(err)
	}
	r := f.only("POST /UserItems/42/UserData")
	// Jellyfin applies only what is set
	if r.query.Get("userId") != "u1" || r.body != `{"PlaybackPositionTicks":900,"Played":false}` {
		t.Errorf("Jellyfin user data = %v %s", r.query, r.body)
	}
}

func TestLibraryEdits(t *testing.T) {
	t.Parallel()

	folder := &VirtualFolder{Name: "Films", ItemID: "9"}
	c, f := newFake(t, Emby, map[string]route{
		"POST /Library/VirtualFolders/Name":         noContent,
		"POST /Library/VirtualFolders/Paths":        noContent,
		"POST /Library/VirtualFolders/Paths/Delete": noContent,
		"POST /Items/9/Refresh":                     noContent,
	})
	if err := c.RenameLibrary(t.Context(), folder, "Movies"); err != nil {
		t.Fatal(err)
	}
	if err := c.AddLibraryPath(t.Context(), folder, "/media/more"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveLibraryPath(t.Context(), folder, "/media/old"); err != nil {
		t.Fatal(err)
	}
	if err := c.ScanLibrary(t.Context(), folder); err != nil {
		t.Fatal(err)
	}
	// Emby names the library by its id throughout, and never scans on an edit
	for key, want := range map[string]string{
		"POST /Library/VirtualFolders/Name":         `{"Id":"9","NewName":"Movies"}`,
		"POST /Library/VirtualFolders/Paths":        `{"Id":"9","PathInfo":{"Path":"/media/more"},"RefreshLibrary":false}`,
		"POST /Library/VirtualFolders/Paths/Delete": `{"Id":"9","Path":"/media/old","RefreshLibrary":false}`,
	} {
		if body := f.only(key).body; body != want {
			t.Errorf("%s = %s, want %s", key, body, want)
		}
	}
	if q := f.only("POST /Items/9/Refresh").query; q.Get("Recursive") != "true" || q.Get("MetadataRefreshMode") != "Default" || q.Get("ImageRefreshMode") != "Default" || q.Has("ReplaceAllMetadata") {
		t.Errorf("scan = %v", q)
	}

	c, f = newFake(t, Jellyfin, map[string]route{
		"POST /Library/VirtualFolders/Name":    noContent,
		"POST /Library/VirtualFolders/Paths":   noContent,
		"DELETE /Library/VirtualFolders/Paths": noContent,
		"POST /Items/9/Refresh":                noContent,
		"POST /Library/Refresh":                noContent,
	})
	if err := c.RenameLibrary(t.Context(), folder, "Movies"); err != nil {
		t.Fatal(err)
	}
	if err := c.AddLibraryPath(t.Context(), folder, "/media/more"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveLibraryPath(t.Context(), folder, "/media/old"); err != nil {
		t.Fatal(err)
	}
	if err := c.ScanLibrary(t.Context(), folder); err != nil {
		t.Fatal(err)
	}
	// Jellyfin names it by its name, in the query
	if q := f.only("POST /Library/VirtualFolders/Name").query; q.Get("name") != "Films" || q.Get("newName") != "Movies" || q.Get("refreshLibrary") != "false" {
		t.Errorf("rename = %v", q)
	}
	// a folder change asks for Jellyfin's library scan, which a scan of the
	// one library does not do; asked for with the change it is dropped when a
	// scan is already running, so it is asked for on its own, once per change
	if r := f.only("POST /Library/VirtualFolders/Paths"); r.body != `{"Name":"Films","PathInfo":{"Path":"/media/more"}}` || r.query.Get("refreshLibrary") != "false" {
		t.Errorf("add path = %v %s", r.query, r.body)
	}
	if q := f.only("DELETE /Library/VirtualFolders/Paths").query; q.Get("name") != "Films" || q.Get("path") != "/media/old" || q.Get("refreshLibrary") != "false" {
		t.Errorf("remove path = %v", q)
	}
	if n := len(f.all("POST /Library/Refresh")); n != 2 {
		t.Errorf("the two folder changes asked for %d library scans, want 2", n)
	}
	if q := f.only("POST /Items/9/Refresh").query; q.Get("metadataRefreshMode") != "Default" {
		t.Errorf("scan = %v", q)
	}
	// a library Jellyfin has not scanned has no id to refresh
	if err := c.ScanLibrary(t.Context(), &VirtualFolder{Name: "New"}); !errors.Is(err, errNoLibraryID) {
		t.Errorf("scanning a library without an id = %v", err)
	}
}

func TestPlaylistEdits(t *testing.T) {
	t.Parallel()

	// the entries each Emby playlist lists, as "item:entry" pairs, changed by
	// the moves the routes below answer
	playlists := map[string][]string{
		"p1": {"a:5", "b:7"},
		"p3": {"a:1", "b:3"}, // renumbered before the move lands, so it moves nothing
		"p4": {"a:1", "b:2"}, // changed by someone else during the move
		"p6": {"a:1", "b:2"}, // written back as it was by a scan's refresh after the first move
	}
	listing := func(id string) route {
		return func(*http.Request, string) (int, string) {
			items := make([]string, 0, len(playlists[id]))
			for _, e := range playlists[id] {
				item, entry, _ := strings.Cut(e, ":")
				items = append(items, `{"Id":"`+item+`","PlaylistItemId":"`+entry+`"}`)
			}
			return http.StatusOK, `{"Items":[` + strings.Join(items, ",") + `]}`
		}
	}
	moved := func(id string, after ...string) route {
		return func(*http.Request, string) (int, string) {
			playlists[id] = after
			return http.StatusNoContent, ""
		}
	}
	c, f := newFake(t, Emby, map[string]route{
		"GET /Playlists/p1/Items":           listing("p1"),
		"POST /Playlists/p1/Items/7/Move/0": moved("p1", "b:7", "a:5"),
		"DELETE /Playlists/p1/Items": func(r *http.Request, _ string) (int, string) {
			gone := strings.Split(r.URL.Query().Get("EntryIds"), ",")
			playlists["p1"] = slices.DeleteFunc(playlists["p1"], func(e string) bool {
				_, entry, _ := strings.Cut(e, ":")
				return slices.Contains(gone, entry)
			})
			return http.StatusNoContent, ""
		},
		"GET /Playlists/p3/Items":           listing("p3"),
		"POST /Playlists/p3/Items/3/Move/0": moved("p3", "a:1", "b:2"),
		"POST /Playlists/p3/Items/2/Move/0": moved("p3", "b:1", "a:2"),
		"GET /Playlists/p4/Items":           listing("p4"),
		"POST /Playlists/p4/Items/2/Move/0": moved("p4", "c:1"),
		"GET /Playlists/p6/Items":           listing("p6"),
		"POST /Playlists/p6/Items/2/Move/0": func() route {
			calls := 0
			return func(*http.Request, string) (int, string) {
				calls++
				if calls > 1 {
					playlists["p6"] = []string{"b:2", "a:1"}
				}
				return http.StatusNoContent, ""
			}
		}(),
		"GET /Users/u1/Items/p1": ok(`{"Id":"p1","Name":"Mix","Type":"Playlist"}`),
		"POST /Items/p1":         noContent,
	})
	if err := c.MovePlaylistEntry(t.Context(), "p1", "u1", "7", 0); err != nil {
		t.Fatal(err)
	}
	f.only("POST /Playlists/p1/Items/7/Move/0")
	// already there: nothing to send
	if err := c.MovePlaylistEntry(t.Context(), "p1", "u1", "7", 0); err != nil {
		t.Fatal(err)
	}
	f.only("POST /Playlists/p1/Items/7/Move/0")
	// Emby answers a move or removal of an entry it does not hold with a 204
	// and changes nothing, so an unknown entry is caught first
	for _, entry := range []string{"3", "abc"} {
		if err := c.MovePlaylistEntry(t.Context(), "p1", "u1", entry, 0); err == nil || !strings.Contains(err.Error(), "no entry "+entry+" (its entry ids are 7, 5)") {
			t.Errorf("moving unknown entry %s = %v", entry, err)
		}
		if _, err := c.RemoveFromPlaylist(t.Context(), "p1", "u1", []string{"5", entry}); err == nil || !strings.Contains(err.Error(), "no entry "+entry) {
			t.Errorf("removing unknown entry %s = %v", entry, err)
		}
	}
	if err := c.MovePlaylistEntry(t.Context(), "p1", "u1", "5", 2); err == nil || !strings.Contains(err.Error(), "outside the playlist's 2 entries") {
		t.Errorf("moving past the end = %v", err)
	}
	if n, err := c.RemoveFromPlaylist(t.Context(), "p1", "u1", []string{"5"}); err != nil || n != 1 {
		t.Fatalf("RemoveFromPlaylist = %d, %v", n, err)
	}
	if q := f.only("DELETE /Playlists/p1/Items").query; q.Get("EntryIds") != "5" {
		t.Errorf("remove = %v", q)
	}
	// a refresh renumbered the entries between the read and the move: the
	// entry is found again by its position and moved once more
	if err := c.MovePlaylistEntry(t.Context(), "p3", "u1", "3", 0); err != nil {
		t.Fatal(err)
	}
	f.only("POST /Playlists/p3/Items/3/Move/0")
	f.only("POST /Playlists/p3/Items/2/Move/0")
	// anything else changing the playlist is an error, not a guess
	if err := c.MovePlaylistEntry(t.Context(), "p4", "u1", "2", 0); err == nil || !strings.Contains(err.Error(), "changed while entry 2") {
		t.Errorf("a playlist changed during the move = %v", err)
	}
	// a move the server accepted and a scan's refresh then wrote back is
	// sent once more
	if err := c.MovePlaylistEntry(t.Context(), "p6", "u1", "2", 0); err != nil {
		t.Fatal(err)
	}
	if n := len(f.all("POST /Playlists/p6/Items/2/Move/0")); n != 2 {
		t.Errorf("the written-back move was sent %d times, want 2", n)
	}
	if err := c.RenamePlaylist(t.Context(), "p1", "u1", "Road Trip"); err != nil {
		t.Fatal(err)
	}
	if body := jsonBody(t, f.only("POST /Items/p1").body); body["Name"] != "Road Trip" || body["Type"] != "Playlist" {
		t.Errorf("rename = %v", body)
	}

	// a Jellyfin playlist the routes below change, holding item ids: an
	// entry's id is its item's, so a removal takes every copy of an item and
	// an add appends items; one listing never changes however it is edited,
	// like a scan re-reading the playlist file
	jf := map[string][]string{
		"p2": {"a", "b", "c"}, "p5": {"a", "b"}, "p6": {"a", "b", "c"},
		"p7": {"a", "b", "a", "c"}, "p8": {"x", "a", "y", "a", "z"},
	}
	jfListing := func(id string) route {
		return func(*http.Request, string) (int, string) {
			items := make([]string, 0, len(jf[id]))
			for _, item := range jf[id] {
				items = append(items, `{"Id":"`+item+`","PlaylistItemId":"`+item+`"}`)
			}
			return http.StatusOK, `{"Items":[` + strings.Join(items, ",") + `],"TotalRecordCount":` + strconv.Itoa(len(items)) + `}`
		}
	}
	jfRemove := func(id string) route {
		return func(r *http.Request, _ string) (int, string) {
			gone := r.URL.Query()["entryIds"]
			jf[id] = slices.DeleteFunc(jf[id], func(item string) bool { return slices.Contains(gone, item) })
			return http.StatusNoContent, ""
		}
	}
	jfAdd := func(id string) route {
		return func(r *http.Request, _ string) (int, string) {
			jf[id] = append(jf[id], r.URL.Query()["ids"]...)
			return http.StatusNoContent, ""
		}
	}
	c, f = newFake(t, Jellyfin, map[string]route{
		"GET /Playlists/p2/Items":    jfListing("p2"),
		"DELETE /Playlists/p2/Items": jfRemove("p2"),
		"POST /Playlists/p2/Items":   jfAdd("p2"),
		"GET /Items/p2":              ok(`{"Id":"p2","Name":"Mix"}`),
		"POST /Items/p2":             noContent,
		"GET /Playlists/p5/Items":    ok(`{"Items":[{"Id":"a","PlaylistItemId":"a"},{"Id":"b","PlaylistItemId":"b"}],"TotalRecordCount":2}`),
		"DELETE /Playlists/p5/Items": noContent,
		"POST /Playlists/p5/Items":   noContent,
		"GET /Playlists/p6/Items":    jfListing("p6"),
		"DELETE /Playlists/p6/Items": jfRemove("p6"),
		// the first put-back lands beside the old tail a refresh restored,
		// the way a scan re-reading the playlist file leaves it
		"POST /Playlists/p6/Items": func() route {
			add := jfAdd("p6")
			calls := 0
			return func(r *http.Request, body string) (int, string) {
				calls++
				if calls == 1 {
					jf["p6"] = append(jf["p6"], "b", "c")
				}
				return add(r, body)
			}
		}(),
		"GET /Playlists/p7/Items":    jfListing("p7"),
		"DELETE /Playlists/p7/Items": jfRemove("p7"),
		"POST /Playlists/p7/Items":   jfAdd("p7"),
		"GET /Playlists/p8/Items":    jfListing("p8"),
		"DELETE /Playlists/p8/Items": jfRemove("p8"),
		"POST /Playlists/p8/Items":   jfAdd("p8"),
	})
	c.settle = time.Millisecond
	// Jellyfin's move wants a user behind the request, so the entries from
	// the lower of the two positions on come out and go back in the new
	// order; the ones before stay where they are
	if err := c.MovePlaylistEntry(t.Context(), "p2", "u1", "c", 1); err != nil {
		t.Fatal(err)
	}
	if q := f.only("DELETE /Playlists/p2/Items").query; !slices.Equal(q["entryIds"], []string{"b", "c"}) {
		t.Errorf("remove = %v", q)
	}
	if q := f.only("POST /Playlists/p2/Items").query; !slices.Equal(q["ids"], []string{"c", "b"}) || q.Get("userId") != "u1" {
		t.Errorf("re-add = %v", q)
	}
	if got := jf["p2"]; !slices.Equal(got, []string{"a", "c", "b"}) {
		t.Errorf("after the move the playlist holds %v, want a, c, b", got)
	}
	// a put-back the server undoes (a scan re-reading the playlist file
	// between the removal and the add) is sent once more, then an error,
	// rather than the old order being reported as moved
	if err := c.MovePlaylistEntry(t.Context(), "p5", "u1", "b", 0); err == nil || !strings.Contains(err.Error(), "did not move entry b") {
		t.Errorf("a move whose put-back is lost = %v", err)
	}
	if n := len(f.all("POST /Playlists/p5/Items")); n != 2 {
		t.Errorf("the put-back was sent %d times, want 2", n)
	}
	// a refresh that restores the old tail beside the new one is not
	// someone else's edit: the tail goes out and back once more
	if err := c.MovePlaylistEntry(t.Context(), "p6", "u1", "c", 1); err != nil {
		t.Fatal(err)
	}
	if got := jf["p6"]; !slices.Equal(got, []string{"a", "c", "b"}) {
		t.Errorf("after the second put-back the playlist holds %v, want a, c, b", got)
	}
	if n := len(f.all("POST /Playlists/p6/Items")); n != 2 {
		t.Errorf("the put-back was sent %d times, want 2", n)
	}
	// an item the playlist holds twice: removing it from the moved stretch
	// takes its copy above the stretch too, so the stretch starts at that
	// copy and the playlist ends as asked, both copies kept
	if err := c.MovePlaylistEntry(t.Context(), "p7", "u1", "c", 2); err != nil {
		t.Fatal(err)
	}
	if got := jf["p7"]; !slices.Equal(got, []string{"a", "b", "c", "a"}) {
		t.Errorf("after moving c above the second a the playlist holds %v, want a, b, c, a", got)
	}
	// and it starts no higher than a copy needs: x, above every copy of what
	// moves, is never taken out
	if err := c.MovePlaylistEntry(t.Context(), "p8", "u1", "z", 3); err != nil {
		t.Fatal(err)
	}
	if got := jf["p8"]; !slices.Equal(got, []string{"x", "a", "y", "z", "a"}) {
		t.Errorf("after moving z the playlist holds %v, want x, a, y, z, a", got)
	}
	if q := f.only("DELETE /Playlists/p8/Items").query; !slices.Equal(q["entryIds"], []string{"a", "y", "z"}) {
		t.Errorf("remove = %v, want each item from the first a on, once", q)
	}
	for entry, index := range map[string]int{"nope": 0, "a": 3} {
		if err := c.MovePlaylistEntry(t.Context(), "p2", "u1", entry, index); err == nil {
			t.Errorf("moving %s to %d succeeded", entry, index)
		}
	}
	if err := c.RenamePlaylist(t.Context(), "p2", "u1", "Road Trip"); err != nil {
		t.Fatal(err)
	}
	if body := jsonBody(t, f.only("POST /Items/p2").body); body["Name"] != "Road Trip" {
		t.Errorf("rename = %v", body)
	}
}

func TestPersonAndUsers(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Emby, map[string]route{
		"GET /Persons/Ridley%20Scott": ok(`{"Id":"26","Name":"Ridley Scott","Type":"Person","ProviderIds":{"Tmdb":"578"}}`),
		"GET /Users/Query": ok(`{"Items":[{"Id":"u1","Name":"root","HasPassword":true,"LastLoginDate":"2026-09-15T00:00:00Z",
			"Policy":{"IsAdministrator":true,"IsHidden":true,"EnableAllFolders":false,"EnabledFolders":["9"],"EnableContentDeletion":true,"MaxParentalRating":10}}],"TotalRecordCount":1}`),
	})
	p, err := c.Person(t.Context(), "Ridley Scott", "")
	if err != nil || p.ID != "26" || p.ProviderIDs["Tmdb"] != "578" {
		t.Errorf("Person = %+v, %v", p, err)
	}
	users, err := c.Users(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	u := users[0]
	if !u.HasPassword || u.LastLoginDate == "" || !u.Policy.IsAdministrator || !u.Policy.IsHidden || u.Policy.EnableAllFolders ||
		!slices.Equal(u.Policy.EnabledFolders, []string{"9"}) || !u.Policy.EnableContentDeletion || u.Policy.MaxParentalRating != 10 {
		t.Errorf("user = %+v", u)
	}
	f.only("GET /Users/Query")

	c, f = newFake(t, Jellyfin, map[string]route{
		"GET /Persons/Denis%20Villeneuve": ok(`{"Id":"p","Name":"Denis Villeneuve"}`),
		"GET /Users":                      ok(`[{"Id":"u2","Name":"alice","Policy":{"IsDisabled":true,"EnableAllFolders":true}}]`),
	})
	if p, err := c.Person(t.Context(), "Denis Villeneuve", "u2"); err != nil || p.Name != "Denis Villeneuve" || f.only("GET /Persons/Denis%20Villeneuve").query.Get("userId") != "u2" {
		t.Errorf("Person = %+v, %v", p, err)
	}
	if users, err := c.Users(t.Context()); err != nil || !users[0].Policy.IsDisabled || !users[0].Policy.EnableAllFolders || users[0].Policy.IsAdministrator {
		t.Errorf("Users = %+v, %v", users, err)
	}
}

func TestCanSee(t *testing.T) {
	t.Parallel()

	movies := &VirtualFolder{Name: "Movies", ItemID: "3", GUID: "guid-movies"} // Emby lists both
	shows := &VirtualFolder{Name: "Shows", ItemID: "f137"}                     // Jellyfin, an ItemId only
	for _, tt := range []struct {
		policy        UserPolicy
		movies, shows bool
	}{
		{UserPolicy{EnableAllFolders: true}, true, true},
		{UserPolicy{EnabledFolders: []string{"GUID-MOVIES", "f137"}}, true, true},
		// Emby's numeric id grants nothing
		{UserPolicy{EnabledFolders: []string{"3"}}, false, false},
		{UserPolicy{}, false, false},
	} {
		u := &User{Policy: tt.policy}
		if u.CanSee(movies) != tt.movies || u.CanSee(shows) != tt.shows {
			t.Errorf("%+v: movies %v shows %v, want %v %v", tt.policy, u.CanSee(movies), u.CanSee(shows), tt.movies, tt.shows)
		}
	}
}

func TestJourneyFindings(t *testing.T) {
	t.Parallel()

	// a search with the item asks only its library's fetchers, which a
	// library built from files alone has off: the search runs again without it
	searches := 0
	c, f := newFake(t, Emby, map[string]route{
		"POST /Items/RemoteSearch/Movie": func(*http.Request, string) (int, string) {
			searches++
			if searches == 1 {
				return http.StatusOK, `[]`
			}
			return http.StatusOK, `[{"Name":"Princess Mononoke","ProductionYear":1997,"ProviderIds":{"Imdb":"tt0119698"}}]`
		},
		"GET /Users/u1/Items/42": ok(`{"Id":"42","Name":"Dune","UserData":{"Played":true,"PlayCount":2,"LastPlayedDate":"2026-09-15T00:00:00Z"}}`),
	})
	results, err := c.RemoteSearch(t.Context(), "movie", "17", "Princess Mononoke", 1997)
	if err != nil || len(results) != 1 {
		t.Fatalf("RemoteSearch = %+v, %v", results, err)
	}
	var bodies []string
	for _, r := range f.requests {
		if r.path == "/Items/RemoteSearch/Movie" {
			bodies = append(bodies, r.body)
		}
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], `"ItemId":17`) || strings.Contains(bodies[1], "ItemId") {
		t.Errorf("searches = %v", bodies)
	}

	// the single-item read carries the play count Emby's lists leave out
	it, err := c.UserItem(t.Context(), "u1", "42")
	if err != nil || it.UserData == nil || it.UserData.PlayCount != 2 || it.UserData.LastPlayedDate == "" {
		t.Errorf("UserItem = %+v, %v", it, err)
	}

	// Emby cannot create an empty collection
	if _, err := c.CreateCollection(t.Context(), "Empty", nil); err == nil || !strings.Contains(err.Error(), "empty collection") {
		t.Errorf("an empty collection on Emby = %v", err)
	}

	// Jellyfin lists an item's entries under its own id, so one removal takes
	// every copy, and says so
	// (the first removal is answered and lost, as a scan saving the playlist
	// as it found it does; the second lands, and only then is it done)
	removals := 0
	c, f = newFake(t, Jellyfin, map[string]route{
		"GET /Playlists/p1/Items": func(*http.Request, string) (int, string) {
			if removals >= 2 {
				return http.StatusOK, `{"Items":[{"Id":"b","PlaylistItemId":"b"}]}`
			}
			return http.StatusOK, `{"Items":[{"Id":"a","PlaylistItemId":"a"},{"Id":"b","PlaylistItemId":"b"},{"Id":"a","PlaylistItemId":"a"}]}`
		},
		"DELETE /Playlists/p1/Items": func(*http.Request, string) (int, string) {
			removals++
			return http.StatusNoContent, ""
		},
	})
	c.settle = time.Millisecond
	if n, err := c.RemoveFromPlaylist(t.Context(), "p1", "u1", []string{"a"}); err != nil || n != 2 {
		t.Errorf("removing a twice-held item's entry = %d, %v, want 2", n, err)
	}
	if len(f.all("DELETE /Playlists/p1/Items")) != 2 {
		t.Errorf("a removal the server lost was not sent again: %d removals", removals)
	}
}
