package embyfin

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/client"
)

// The rest of the neutral methods, against the same canned server as
// requests_test.go: the route, query and body each backend gets, the answer
// as it comes back mapped, and what a 404 turns into.

// fieldsDefault is FieldsDefault the way Jellyfin's typed client puts it on
// the wire: one key per field.
var fieldsDefault = strings.Split(FieldsDefault, ",")

func TestAddToCollection(t *testing.T) {
	t.Parallel()

	// Emby joins the ids, Jellyfin repeats the key; both are read back until
	// the collection holds what was added
	members := func(ids ...string) route {
		return func(*http.Request, string) (int, string) {
			rows := make([]string, 0, len(ids))
			for _, id := range ids {
				rows = append(rows, `{"Id":"`+id+`"}`)
			}
			return http.StatusOK, `{"Items":[` + strings.Join(rows, ",") + `],"TotalRecordCount":` + strconv.Itoa(len(ids)) + `}`
		}
	}
	c, f := newFake(t, Emby, map[string]route{"POST /Collections/c1/Items": noContent, "GET /Items": members("1", "2")})
	c.settle = time.Millisecond
	if err := c.AddToCollection(t.Context(), "c1", []string{"1", "2"}); err != nil {
		t.Fatal(err)
	}
	if r := f.only("POST /Collections/c1/Items"); r.query.Get("Ids") != "1,2" || r.body != "" {
		t.Errorf("Emby add = %v %q", r.query, r.body)
	}

	// Jellyfin: the first add is written over by a refresh of the
	// collection (the members read back without it), so it is sent again
	adds := 0
	c, f = newFake(t, Jellyfin, map[string]route{
		"POST /Collections/c1/Items": func(*http.Request, string) (int, string) {
			adds++
			return http.StatusNoContent, ""
		},
		"GET /Items": func(r *http.Request, s string) (int, string) {
			if adds >= 2 {
				return members("a", "b")(r, s)
			}
			return members("a")(r, s)
		},
		"POST /Collections/c9/Items": answer(http.StatusNotFound, ""),
	})
	c.settle = time.Millisecond
	if err := c.AddToCollection(t.Context(), "c1", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if reqs := f.all("POST /Collections/c1/Items"); len(reqs) != 2 || !slices.Equal(reqs[1].query["ids"], []string{"b"}) {
		t.Errorf("Jellyfin add = %d sends, %v; want the lost item sent again, on its own", len(reqs), reqs)
	}
	// a collection the server does not have is the server's 404, not a success
	if err := c.AddToCollection(t.Context(), "c9", []string{"a"}); !client.IsNotFound(err) {
		t.Errorf("adding to an unknown collection = %v", err)
	}
}

func TestImages(t *testing.T) {
	t.Parallel()

	// both servers answer a bare list
	images := `[{"ImageType":"Primary","Width":600,"Height":900,"Size":1234},{"ImageType":"Backdrop","Width":1920,"Height":1080}]`
	for _, backend := range []Backend{Emby, Jellyfin} {
		c, f := newFake(t, backend, map[string]route{
			"GET /Items/9/Images":   ok(images),
			"GET /Items/404/Images": answer(http.StatusNotFound, ""),
		})
		got, err := c.Images(t.Context(), "9")
		if err != nil || len(got) != 2 || got[0].ImageType != "Primary" || got[0].Width != 600 || got[0].Height != 900 || got[0].Size != 1234 || got[1].ImageType != "Backdrop" {
			t.Errorf("%s Images = %+v, %v", backend, got, err)
		}
		if r := f.only("GET /Items/9/Images"); len(r.query) != 0 {
			t.Errorf("%s asked with %v", backend, r.query)
		}
		if _, err := c.Images(t.Context(), "404"); !client.IsNotFound(err) {
			t.Errorf("%s images of an unknown item = %v", backend, err)
		}
	}
}

func TestRemoteImages(t *testing.T) {
	t.Parallel()

	page := `{"Images":[{"ProviderName":"Zzyzx","Url":"http://img.test/1.jpg","Type":"Primary","Width":1000,"Height":1500,
		"Language":"en","CommunityRating":7.5,"VoteCount":3}],"Providers":["Zzyzx"],"TotalRecordCount":12}`
	check := func(t *testing.T, backend Backend, got []RemoteImage, total int, err error) {
		t.Helper()

		if err != nil || total != 12 || len(got) != 1 {
			t.Fatalf("%s RemoteImages = %+v, %d, %v", backend, got, total, err)
		}
		if im := got[0]; im.ProviderName != "Zzyzx" || im.URL != "http://img.test/1.jpg" || im.Type != "Primary" || im.Width != 1000 || im.Height != 1500 ||
			im.Language != "en" || im.CommunityRating != 7.5 || im.VoteCount != 3 {
			t.Errorf("%s image = %+v", backend, im)
		}
	}

	c, f := newFake(t, Emby, map[string]route{"GET /Items/9/RemoteImages": ok(page)})
	got, total, err := c.RemoteImages(t.Context(), "9", "Primary", 5)
	check(t, Emby, got, total, err)
	if q := f.only("GET /Items/9/RemoteImages").query; q.Get("Type") != "Primary" || q.Get("Limit") != "5" {
		t.Errorf("Emby query = %v", q)
	}

	c, f = newFake(t, Jellyfin, map[string]route{
		"GET /Items/9/RemoteImages":   ok(page),
		"GET /Items/404/RemoteImages": answer(http.StatusNotFound, ""),
	})
	// no limit asks for no limit
	got, total, err = c.RemoteImages(t.Context(), "9", "Backdrop", 0)
	check(t, Jellyfin, got, total, err)
	if q := f.only("GET /Items/9/RemoteImages").query; q.Get("type") != "Backdrop" || q.Has("limit") || q.Has("Type") {
		t.Errorf("Jellyfin query = %v", q)
	}
	if _, _, err := c.RemoteImages(t.Context(), "404", "Primary", 0); !client.IsNotFound(err) {
		t.Errorf("remote images of an unknown item = %v", err)
	}
}

func TestDownloadRemoteImage(t *testing.T) {
	t.Parallel()

	// the image's type and address go in the query on both; Emby's document
	// wants a body too, which is sent empty
	c, f := newFake(t, Emby, map[string]route{"POST /Items/9/RemoteImages/Download": noContent})
	if err := c.DownloadRemoteImage(t.Context(), "9", "Primary", "http://img.test/1.jpg"); err != nil {
		t.Fatal(err)
	}
	if r := f.only("POST /Items/9/RemoteImages/Download"); r.query.Get("Type") != "Primary" || r.query.Get("ImageUrl") != "http://img.test/1.jpg" || r.body != `{}` {
		t.Errorf("Emby download = %v %s", r.query, r.body)
	}

	c, f = newFake(t, Jellyfin, map[string]route{
		"POST /Items/9/RemoteImages/Download":   noContent,
		"POST /Items/404/RemoteImages/Download": answer(http.StatusNotFound, ""),
	})
	if err := c.DownloadRemoteImage(t.Context(), "9", "Primary", "http://img.test/1.jpg"); err != nil {
		t.Fatal(err)
	}
	if r := f.only("POST /Items/9/RemoteImages/Download"); r.query.Get("type") != "Primary" || r.query.Get("imageUrl") != "http://img.test/1.jpg" || r.body != "" {
		t.Errorf("Jellyfin download = %v %q", r.query, r.body)
	}
	if err := c.DownloadRemoteImage(t.Context(), "404", "Primary", "http://img.test/1.jpg"); !client.IsNotFound(err) {
		t.Errorf("a download for an unknown item = %v", err)
	}
}

func TestSimilarAndInstantMix(t *testing.T) {
	t.Parallel()

	page := `{"Items":[{"Id":"7","Name":"Zzyzx","Type":"Movie","ProductionYear":2001}],"TotalRecordCount":1}`
	c, f := newFake(t, Emby, map[string]route{
		"GET /Items/9/Similar":    ok(page),
		"GET /Items/9/InstantMix": ok(page),
	})
	items, err := c.Similar(t.Context(), "9", "u1", 3)
	if err != nil || len(items) != 1 || items[0].ID != "7" || items[0].Name != "Zzyzx" || items[0].ProductionYear != 2001 {
		t.Errorf("Similar = %+v, %v", items, err)
	}
	// Emby answers 500 to /Similar without a user, so the user always goes
	if q := f.only("GET /Items/9/Similar").query; q.Get("UserId") != "u1" || q.Get("Fields") != FieldsDefault || q.Get("Limit") != "3" {
		t.Errorf("Emby similar = %v", q)
	}
	if items, err := c.InstantMix(t.Context(), "9", 0); err != nil || len(items) != 1 || items[0].ID != "7" {
		t.Errorf("InstantMix = %+v, %v", items, err)
	}
	if q := f.only("GET /Items/9/InstantMix").query; q.Get("Fields") != FieldsDefault || q.Has("Limit") || q.Has("UserId") {
		t.Errorf("Emby instant mix = %v", q)
	}

	c, f = newFake(t, Jellyfin, map[string]route{
		"GET /Items/9/Similar":      ok(page),
		"GET /Items/9/InstantMix":   ok(page),
		"GET /Items/404/Similar":    answer(http.StatusNotFound, ""),
		"GET /Items/404/InstantMix": answer(http.StatusNotFound, ""),
	})
	if items, err := c.Similar(t.Context(), "9", "u1", 3); err != nil || len(items) != 1 || items[0].Name != "Zzyzx" {
		t.Errorf("Similar = %+v, %v", items, err)
	}
	if q := f.only("GET /Items/9/Similar").query; q.Get("userId") != "u1" || !slices.Equal(q["fields"], fieldsDefault) || q.Get("limit") != "3" {
		t.Errorf("Jellyfin similar = %v", q)
	}
	if items, err := c.InstantMix(t.Context(), "9", 10); err != nil || len(items) != 1 {
		t.Errorf("InstantMix = %+v, %v", items, err)
	}
	if q := f.only("GET /Items/9/InstantMix").query; !slices.Equal(q["fields"], fieldsDefault) || q.Get("limit") != "10" {
		t.Errorf("Jellyfin instant mix = %v", q)
	}
	if _, err := c.Similar(t.Context(), "404", "u1", 0); !client.IsNotFound(err) {
		t.Errorf("similar to an unknown item = %v", err)
	}
	if _, err := c.InstantMix(t.Context(), "404", 0); !client.IsNotFound(err) {
		t.Errorf("a mix from an unknown item = %v", err)
	}
}

func TestDeleteItems(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Emby, map[string]route{
		"DELETE /Items/9": noContent,
		"DELETE /Items":   noContent,
	})
	if err := c.DeleteItem(t.Context(), "9"); err != nil {
		t.Fatal(err)
	}
	f.only("DELETE /Items/9")
	if err := c.DeleteItems(t.Context(), []string{"1", "2"}); err != nil {
		t.Fatal(err)
	}
	// Emby reads one comma-separated value
	if q := f.only("DELETE /Items").query; q.Get("Ids") != "1,2" {
		t.Errorf("Emby delete = %v", q)
	}

	c, f = newFake(t, Jellyfin, map[string]route{
		"DELETE /Items/a":   noContent,
		"DELETE /Items":     noContent,
		"DELETE /Items/404": answer(http.StatusNotFound, ""),
	})
	if err := c.DeleteItem(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	f.only("DELETE /Items/a")
	if err := c.DeleteItems(t.Context(), []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if q := f.only("DELETE /Items").query; !slices.Equal(q["ids"], []string{"a", "b"}) {
		t.Errorf("Jellyfin delete = %v", q)
	}
	// nothing to delete sends nothing
	before := len(f.requests)
	if err := c.DeleteItems(t.Context(), nil); err != nil || len(f.requests) != before {
		t.Errorf("deleting nothing = %v (%d requests)", err, len(f.requests)-before)
	}
	if err := c.DeleteItem(t.Context(), "404"); !client.IsNotFound(err) {
		t.Errorf("deleting an unknown item = %v", err)
	}
}

func TestVisibleUserItem(t *testing.T) {
	t.Parallel()

	// Emby's single-item read answers with an item the user may not see, and
	// only its list query in the user's view leaves it out, so that is asked
	// too, for the item's own kind
	visible := true
	c, f := newFake(t, Emby, map[string]route{
		"GET /Users/u1/Items": func(*http.Request, string) (int, string) {
			if visible {
				return http.StatusOK, `{"Items":[{"Id":"42","Path":"/media/z.mkv"}],"TotalRecordCount":1}`
			}
			return http.StatusOK, `{"Items":[],"TotalRecordCount":0}`
		},
		"GET /Users/u1/Items/42": ok(`{"Id":"42","Name":"Zzyzx","Type":"Movie","UserData":{"Played":true,"PlayCount":2}}`),
	})
	it, seen, err := c.VisibleUserItem(t.Context(), "u1", "42")
	if err != nil || !seen || it == nil || it.Name != "Zzyzx" || it.UserData == nil || it.UserData.PlayCount != 2 {
		t.Fatalf("VisibleUserItem = %+v, %v, %v", it, seen, err)
	}
	if q := f.only("GET /Users/u1/Items").query; q.Get("Ids") != "42" || q.Get("IncludeItemTypes") != "Movie" || q.Get("Fields") != "Path" || q.Get("Limit") != "1" {
		t.Errorf("Emby list query = %v", q)
	}
	f.only("GET /Users/u1/Items/42")

	visible = false
	if it, seen, err := c.VisibleUserItem(t.Context(), "u1", "42"); err != nil || seen || it != nil {
		t.Errorf("an item the user may not see = %+v, %v, %v", it, seen, err)
	}

	// Jellyfin's single-item read answers 404 for such an item
	c, f = newFake(t, Jellyfin, map[string]route{
		"GET /Items/42": ok(`{"Id":"42","Name":"Zzyzx"}`),
		"GET /Items/43": answer(http.StatusNotFound, ""),
		"GET /Items/44": answer(http.StatusForbidden, "no"),
	})
	if it, seen, err := c.VisibleUserItem(t.Context(), "u1", "42"); err != nil || !seen || it.Name != "Zzyzx" || f.only("GET /Items/42").query.Get("userId") != "u1" {
		t.Errorf("VisibleUserItem = %+v, %v, %v", it, seen, err)
	}
	if it, seen, err := c.VisibleUserItem(t.Context(), "u1", "43"); err != nil || seen || it != nil {
		t.Errorf("a 404 = %+v, %v, %v, want not seen and no error", it, seen, err)
	}
	// anything but a 404 is still an error
	if _, _, err := c.VisibleUserItem(t.Context(), "u1", "44"); client.StatusCode(err) != http.StatusForbidden {
		t.Errorf("a 403 = %v", err)
	}
}

// Emby's list in a user's view leaves out a music album unless its kind is
// asked for, even for an administrator (seen live on Emby 4.10: an album the
// user could see read as hidden from her and from root), so the visibility
// check asks for the item's own kind.
func TestVisibleUserItemFindsAnEmbyAlbum(t *testing.T) {
	t.Parallel()

	album := `{"Id":"152","Name":"Zzyzx Tapes","Type":"MusicAlbum","IsFolder":true}`
	c, _ := newFake(t, Emby, map[string]route{
		"GET /Users/u2/Items": func(r *http.Request, _ string) (int, string) {
			if r.URL.Query().Get("IncludeItemTypes") != "MusicAlbum" {
				return http.StatusOK, `{"Items":[],"TotalRecordCount":0}`
			}
			return http.StatusOK, `{"Items":[` + album + `],"TotalRecordCount":1}`
		},
		"GET /Users/u2/Items/152": ok(album),
	})
	it, seen, err := c.VisibleUserItem(t.Context(), "u2", "152")
	if err != nil || !seen || it == nil || it.Name != "Zzyzx Tapes" {
		t.Errorf("VisibleUserItem = %+v, %v, %v; want the album, seen", it, seen, err)
	}
}

func TestItemHasFile(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		item Item
		want bool
	}{
		{Item{Path: "/tv/s01e01.mkv"}, true},
		{Item{Path: "/tv/s01e01.mkv", IsMissing: true}, false}, // a record the library lacks a file for
		{Item{}, false},
	} {
		if got := tt.item.HasFile(); got != tt.want {
			t.Errorf("%+v HasFile = %v, want %v", tt.item, got, tt.want)
		}
	}
}

func TestFolderOfAndFetchersOff(t *testing.T) {
	t.Parallel()

	folders := []VirtualFolder{
		{Name: "Films", Locations: []string{"/media/films/"}},
		{Name: "Nested", Locations: []string{"/media/films/imports"}},
		{Name: "Shows", Locations: []string{"/media/shows", ""}},
		// a server on Windows names its folders with backslashes
		{Name: "Windows Films", Locations: []string{`D:\Media\Films\`}},
		{Name: "Windows Nested", Locations: []string{`D:\Media\Films\Imports`}},
		{Name: "Windows Share", Locations: []string{`\\nas\video`}},
	}
	for path, want := range map[string]string{
		"/media/films/Zzyzx (2001)/Zzyzx.mkv":           "Films",
		"/media/films/imports/Zzyzx (2001)/Zzyzx.mkv":   "Nested", // the deepest library wins
		"/media/shows/Zzyzx/Season 01/s01e01.mkv":       "Shows",
		"/media/filmstrip/Zzyzx.mkv":                    "", // a prefix is not a folder
		"/media/films":                                  "", // the folder itself is not in it
		"/collections/Zzyzx":                            "",
		`D:\Media\Films\Zzyzx (2001)\Zzyzx.mkv`:         "Windows Films",
		`D:\Media\Films\Imports\Zzyzx (2001)\Zzyzx.mkv`: "Windows Nested",
		`\\nas\video\Zzyzx (2001)\Zzyzx.mkv`:            "Windows Share",
		`D:\Media\Filmstrip\Zzyzx.mkv`:                  "",
		`D:\Media\Films`:                                "",
	} {
		got := FolderOf(folders, path)
		switch {
		case got == nil && want != "":
			t.Errorf("FolderOf(%s) = nil, want %s", path, want)
		case got != nil && got.Name != want:
			t.Errorf("FolderOf(%s) = %s, want %q", path, got.Name, want)
		}
	}

	// off is the type listed with no fetchers; unlisted takes the defaults
	f := VirtualFolder{MetadataFetchers: map[string][]string{"Movie": {}, "Series": {"TheTVDB"}}}
	if !f.FetchersOff("Movie") || f.FetchersOff("Series") || f.FetchersOff("Episode") {
		t.Errorf("FetchersOff: Movie %v Series %v Episode %v", f.FetchersOff("Movie"), f.FetchersOff("Series"), f.FetchersOff("Episode"))
	}
}

func TestPersons(t *testing.T) {
	t.Parallel()

	page := `{"Items":[{"Id":"26","Name":"Zzyzx Zed","Type":"Person","ProviderIds":{"Tmdb":"9"}}],"TotalRecordCount":1}`
	c, f := newFake(t, Emby, map[string]route{"GET /Persons": ok(page)})
	people, err := c.Persons(t.Context(), "zzy", 5)
	if err != nil || len(people) != 1 || people[0].ID != "26" || people[0].Type != "Person" || people[0].ProviderIDs["Tmdb"] != "9" {
		t.Errorf("Persons = %+v, %v", people, err)
	}
	if q := f.only("GET /Persons").query; q.Get("SearchTerm") != "zzy" || q.Get("Limit") != "5" {
		t.Errorf("Emby persons = %v", q)
	}

	c, f = newFake(t, Jellyfin, map[string]route{"GET /Persons": answer(http.StatusUnauthorized, "")})
	if _, err := c.Persons(t.Context(), "zzy", 0); client.StatusCode(err) != http.StatusUnauthorized {
		t.Errorf("a 401 = %v", err)
	}
	if q := f.only("GET /Persons").query; q.Get("searchTerm") != "zzy" || q.Has("limit") {
		t.Errorf("Jellyfin persons = %v", q)
	}
}

func TestPathExists(t *testing.T) {
	t.Parallel()

	// the server is asked about a folder, then a file; each answer is the
	// status the server gives that kind
	validate := func(folder, file int) route {
		return func(_ *http.Request, body string) (int, string) {
			if strings.Contains(body, `"IsFile":true`) {
				return file, ""
			}
			return folder, ""
		}
	}
	for _, tc := range []struct {
		folder, file int
		want         bool
		asked        int
		fails        bool
	}{
		{http.StatusNoContent, http.StatusNotFound, true, 1, false},
		{http.StatusNotFound, http.StatusNoContent, true, 2, false},
		{http.StatusNotFound, http.StatusNotFound, false, 2, false},
		{http.StatusInternalServerError, http.StatusNoContent, false, 1, true},
	} {
		for _, backend := range []Backend{Emby, Jellyfin} {
			c, f := newFake(t, backend, map[string]route{"POST /Environment/ValidatePath": validate(tc.folder, tc.file)})
			got, err := c.PathExists(t.Context(), "/zzyzx")
			if got != tc.want || (err != nil) != tc.fails {
				t.Errorf("%s folder %d file %d: PathExists = %v, %v; want %v, error %v", backend, tc.folder, tc.file, got, err, tc.want, tc.fails)
			}
			asked := f.all("POST /Environment/ValidatePath")
			if len(asked) != tc.asked {
				t.Fatalf("%s folder %d file %d: asked %d times, want %d", backend, tc.folder, tc.file, len(asked), tc.asked)
			}
			// Emby takes the path in the query and the kind in the body;
			// Jellyfin takes both in the body
			first := asked[0]
			if backend == Emby && (first.query.Get("Path") != "/zzyzx" || first.body != `{"IsFile":false}`) ||
				backend == Jellyfin && (len(first.query) != 0 || first.body != `{"IsFile":false,"Path":"/zzyzx"}`) {
				t.Errorf("%s asked with %v %s", backend, first.query, first.body)
			}
			if len(asked) == 2 && !strings.Contains(asked[1].body, `"IsFile":true`) {
				t.Errorf("%s asked about the file with %s", backend, asked[1].body)
			}
		}
	}
}

func TestSeasonsAndEpisodes(t *testing.T) {
	t.Parallel()

	seasons := `{"Items":[{"Id":"s1","Name":"Season 1","Type":"Season","IndexNumber":1,"UserData":{"UnplayedItemCount":3}}],"TotalRecordCount":1}`
	episodes := `{"Items":[{"Id":"e1","Name":"One","Type":"Episode","SeriesId":"9","ParentIndexNumber":1,"IndexNumber":1,"IndexNumberEnd":2,"Path":"/tv/s01e01.mkv"},
		{"Id":"e2","Name":"Two","Type":"Episode","ParentIndexNumber":1,"IndexNumber":3,"LocationType":"Virtual"}],"TotalRecordCount":2}`
	check := func(t *testing.T, backend Backend, items []Item, err error) {
		t.Helper()

		if err != nil || len(items) != 2 {
			t.Fatalf("%s Episodes = %+v, %v", backend, items, err)
		}
		if e := items[0]; e.ID != "e1" || e.SeriesID != "9" || e.ParentIndexNumber != 1 || e.IndexNumber != 1 || e.IndexNumberEnd != 2 || !e.HasFile() {
			t.Errorf("%s held episode = %+v", backend, e)
		}
		if e := items[1]; !e.IsMissing || e.HasFile() {
			t.Errorf("%s missing episode = %+v", backend, e)
		}
	}

	c, f := newFake(t, Emby, map[string]route{
		"GET /Shows/9/Seasons":  ok(seasons),
		"GET /Shows/9/Episodes": ok(episodes),
	})
	got, err := c.Seasons(t.Context(), "9", "u1")
	if err != nil || len(got) != 1 || got[0].ID != "s1" || got[0].IndexNumber != 1 || got[0].UserData == nil || got[0].UserData.UnplayedItemCount != 3 {
		t.Errorf("Seasons = %+v, %v", got, err)
	}
	if q := f.only("GET /Shows/9/Seasons").query; q.Get("UserId") != "u1" || q.Get("Fields") != FieldsDefault {
		t.Errorf("Emby seasons = %v", q)
	}
	// the specials are season 0, which is sent; Emby 4.10 has no missing
	// filter, so Missing asks it for nothing more
	items, err := c.Episodes(t.Context(), "9", EpisodeOptions{SeasonID: "s0", Season: new(0), UserID: "u1", Missing: true, Fields: "Path", Limit: 50, StartIndex: 100})
	check(t, Emby, items, err)
	q := f.only("GET /Shows/9/Episodes").query
	if q.Get("SeasonId") != "s0" || q.Get("Season") != "0" || q.Get("UserId") != "u1" || q.Get("Fields") != "Path" || q.Get("Limit") != "50" || q.Get("StartIndex") != "100" || q.Has("IsMissing") {
		t.Errorf("Emby episodes = %v", q)
	}

	c, f = newFake(t, Jellyfin, map[string]route{
		"GET /Shows/9/Seasons":    ok(seasons),
		"GET /Shows/9/Episodes":   ok(episodes),
		"GET /Shows/404/Seasons":  answer(http.StatusNotFound, ""),
		"GET /Shows/404/Episodes": answer(http.StatusNotFound, ""),
	})
	if got, err := c.Seasons(t.Context(), "9", "u1"); err != nil || len(got) != 1 || got[0].Name != "Season 1" {
		t.Errorf("Seasons = %+v, %v", got, err)
	}
	if jq := f.only("GET /Shows/9/Seasons").query; jq.Get("userId") != "u1" || !slices.Equal(jq["fields"], fieldsDefault) {
		t.Errorf("Jellyfin seasons = %v", jq)
	}
	items, err = c.Episodes(t.Context(), "9", EpisodeOptions{Season: new(2), Missing: true, Limit: 50})
	check(t, Jellyfin, items, err)
	q = f.only("GET /Shows/9/Episodes").query
	if q.Get("season") != "2" || q.Get("isMissing") != "true" || q.Get("limit") != "50" || !slices.Equal(q["fields"], fieldsDefault) || q.Has("seasonId") || q.Has("userId") || q.Has("startIndex") {
		t.Errorf("Jellyfin episodes = %v", q)
	}
	// the zero options ask for every season, not the specials, and no filter
	if _, err := c.Episodes(t.Context(), "9", EpisodeOptions{}); err != nil {
		t.Fatal(err)
	}
	if q := f.all("GET /Shows/9/Episodes")[1].query; q.Has("season") || q.Has("isMissing") || q.Has("limit") {
		t.Errorf("Jellyfin episodes with the zero options = %v", q)
	}
	if _, err := c.Seasons(t.Context(), "404", ""); !client.IsNotFound(err) {
		t.Errorf("seasons of an unknown series = %v", err)
	}
	if _, err := c.Episodes(t.Context(), "404", EpisodeOptions{}); !client.IsNotFound(err) {
		t.Errorf("episodes of an unknown series = %v", err)
	}
}

func TestEpisodesAndRecords(t *testing.T) {
	t.Parallel()

	held := `{"Items":[{"Id":"e1","Path":"/tv/s01e01.mkv"},{"Id":"e2","LocationType":"Virtual"}],"TotalRecordCount":2}`
	missing := `{"Items":[{"Id":"e2","LocationType":"Virtual"},{"Id":"e3","LocationType":"Virtual"}],"TotalRecordCount":2}`
	ids := func(items []Item) []string {
		out := make([]string, 0, len(items))
		for i := range items {
			out = append(out, items[i].ID)
		}
		return out
	}

	// Emby answers everything it holds, records included, in one query
	c, f := newFake(t, Emby, map[string]route{"GET /Shows/9/Episodes": ok(held)})
	items, err := c.EpisodesAndRecords(t.Context(), "9")
	if err != nil || !slices.Equal(ids(items), []string{"e1", "e2"}) {
		t.Errorf("Emby EpisodesAndRecords = %v, %v", ids(items), err)
	}
	f.only("GET /Shows/9/Episodes")

	// Jellyfin is asked twice, with and without the missing filter, and the
	// answers merged with no record counted twice
	c, f = newFake(t, Jellyfin, map[string]route{
		"GET /Shows/9/Episodes": func(r *http.Request, _ string) (int, string) {
			if r.URL.Query().Get("isMissing") == "true" {
				return http.StatusOK, missing
			}
			return http.StatusOK, held
		},
		"GET /Shows/404/Episodes": answer(http.StatusNotFound, ""),
	})
	items, err = c.EpisodesAndRecords(t.Context(), "9")
	if err != nil || !slices.Equal(ids(items), []string{"e1", "e2", "e3"}) {
		t.Errorf("Jellyfin EpisodesAndRecords = %v, %v", ids(items), err)
	}
	if !items[0].HasFile() || items[1].HasFile() || items[2].HasFile() {
		t.Errorf("held = %v %v %v, want only the first", items[0].HasFile(), items[1].HasFile(), items[2].HasFile())
	}
	asked := f.all("GET /Shows/9/Episodes")
	if len(asked) != 2 || asked[0].query.Has("isMissing") || asked[1].query.Get("isMissing") != "true" {
		t.Errorf("Jellyfin was asked %v", asked)
	}
	if _, err := c.EpisodesAndRecords(t.Context(), "404"); !client.IsNotFound(err) {
		t.Errorf("records of an unknown series = %v", err)
	}
}

func TestSubtitles(t *testing.T) {
	t.Parallel()

	found := `[{"Id":"s1","Name":"Zzyzx.srt","ProviderName":"Zzyzx Subs","Format":"srt","ThreeLetterISOLanguageName":"eng","DownloadCount":40,"CommunityRating":4.5,"Comment":"synced"}]`
	for _, backend := range []Backend{Emby, Jellyfin} {
		routes := map[string]route{
			"GET /Items/9/RemoteSearch/Subtitles/eng":   ok(found),
			"GET /Items/404/RemoteSearch/Subtitles/eng": answer(http.StatusNotFound, ""),
			"POST /Items/9/RemoteSearch/Subtitles/s1":   noContent,
			"POST /Items/9/RemoteSearch/Subtitles/gone": answer(http.StatusNotFound, ""),
		}
		downloads := []string{"s1"}
		if backend == Emby {
			// Emby's document also answers the download with the new stream's
			// index; Jellyfin's only with a 204
			routes["POST /Items/9/RemoteSearch/Subtitles/index"] = ok(`{"NewIndex":2}`)
			downloads = append(downloads, "index")
		}
		c, f := newFake(t, backend, routes)
		subs, err := c.SearchSubtitles(t.Context(), "9", "eng")
		if err != nil || len(subs) != 1 {
			t.Fatalf("%s SearchSubtitles = %+v, %v", backend, subs, err)
		}
		if s := subs[0]; s.ID != "s1" || s.Name != "Zzyzx.srt" || s.ProviderName != "Zzyzx Subs" || s.Format != "srt" || s.ThreeLetterISOLanguageName != "eng" ||
			s.DownloadCount != 40 || s.CommunityRating != 4.5 || s.Comment != "synced" {
			t.Errorf("%s subtitle = %+v", backend, s)
		}
		if r := f.only("GET /Items/9/RemoteSearch/Subtitles/eng"); len(r.query) != 0 {
			t.Errorf("%s searched with %v", backend, r.query)
		}
		if _, err := c.SearchSubtitles(t.Context(), "404", "eng"); !client.IsNotFound(err) {
			t.Errorf("%s subtitles for an unknown item = %v", backend, err)
		}

		for _, id := range downloads {
			if err := c.DownloadSubtitle(t.Context(), "9", id); err != nil {
				t.Errorf("%s DownloadSubtitle(%s) = %v", backend, id, err)
			}
			if r := f.only("POST /Items/9/RemoteSearch/Subtitles/" + id); len(r.query) != 0 || r.body != "" {
				t.Errorf("%s downloaded with %v %q", backend, r.query, r.body)
			}
		}
		if err := c.DownloadSubtitle(t.Context(), "9", "gone"); !client.IsNotFound(err) {
			t.Errorf("%s downloading an unknown subtitle = %v", backend, err)
		}
	}
}

func TestCountsDevicesAndLogFiles(t *testing.T) {
	t.Parallel()

	counts := `{"MovieCount":10,"SeriesCount":2,"EpisodeCount":30,"AlbumCount":4,"SongCount":50,"MusicVideoCount":1,"BoxSetCount":3,"TrailerCount":7}`
	devices := `{"Items":[{"Name":"Zzyzx Phone","AppName":"Zzyzx App","AppVersion":"1.2","LastUserName":"zed","DateLastActivity":"2026-09-14T00:00:00Z","Id":"d9"}],"TotalRecordCount":1}`
	logs := `[{"Name":"server.txt","Size":2048,"DateModified":"2026-09-14T00:00:00Z","DateCreated":"2026-09-01T00:00:00Z"}]`
	checkCounts := func(t *testing.T, backend Backend, n *ItemCounts, err error) {
		t.Helper()

		if err != nil || n.MovieCount != 10 || n.SeriesCount != 2 || n.EpisodeCount != 30 || n.AlbumCount != 4 || n.SongCount != 50 || n.MusicVideoCount != 1 || n.BoxSetCount != 3 || n.TrailerCount != 7 {
			t.Errorf("%s Counts = %+v, %v", backend, n, err)
		}
	}
	checkDevices := func(t *testing.T, backend Backend, ds []Device, err error) {
		t.Helper()

		if err != nil || len(ds) != 1 || ds[0].Name != "Zzyzx Phone" || ds[0].AppName != "Zzyzx App" || ds[0].AppVersion != "1.2" || ds[0].LastUserName != "zed" || ds[0].DateLastActivity == "" || ds[0].ID != "d9" {
			t.Errorf("%s Devices = %+v, %v", backend, ds, err)
		}
	}
	checkLogs := func(t *testing.T, backend Backend, files []LogFile, err error) {
		t.Helper()

		if err != nil || len(files) != 1 || files[0].Name != "server.txt" || files[0].Size != 2048 || files[0].DateModified != "2026-09-14T00:00:00Z" {
			t.Errorf("%s LogFiles = %+v, %v", backend, files, err)
		}
	}

	// Emby wraps its log list in a query result under its own route
	c, f := newFake(t, Emby, map[string]route{
		"GET /Items/Counts":      ok(counts),
		"GET /Devices":           ok(devices),
		"GET /System/Logs/Query": ok(`{"Items":` + logs + `,"TotalRecordCount":1}`),
	})
	n, err := c.Counts(t.Context())
	checkCounts(t, Emby, n, err)
	ds, err := c.Devices(t.Context())
	checkDevices(t, Emby, ds, err)
	files, err := c.LogFiles(t.Context())
	checkLogs(t, Emby, files, err)
	for _, key := range []string{"GET /Items/Counts", "GET /Devices", "GET /System/Logs/Query"} {
		if r := f.only(key); len(r.query) != 0 {
			t.Errorf("%s asked with %v", key, r.query)
		}
	}

	// Jellyfin answers the logs as a bare list
	c, f = newFake(t, Jellyfin, map[string]route{
		"GET /Items/Counts": ok(counts),
		"GET /Devices":      ok(devices),
		"GET /System/Logs":  ok(logs),
	})
	n, err = c.Counts(t.Context())
	checkCounts(t, Jellyfin, n, err)
	ds, err = c.Devices(t.Context())
	checkDevices(t, Jellyfin, ds, err)
	files, err = c.LogFiles(t.Context())
	checkLogs(t, Jellyfin, files, err)
	for _, key := range []string{"GET /Items/Counts", "GET /Devices", "GET /System/Logs"} {
		f.only(key)
	}

	// a refused read is the server's status, on every one of them
	c, _ = newFake(t, Jellyfin, map[string]route{
		"GET /Items/Counts": answer(http.StatusForbidden, ""),
		"GET /Devices":      answer(http.StatusForbidden, ""),
		"GET /System/Logs":  answer(http.StatusForbidden, ""),
	})
	if _, err := c.Counts(t.Context()); client.StatusCode(err) != http.StatusForbidden {
		t.Errorf("Counts refused = %v", err)
	}
	if _, err := c.Devices(t.Context()); client.StatusCode(err) != http.StatusForbidden {
		t.Errorf("Devices refused = %v", err)
	}
	if _, err := c.LogFiles(t.Context()); client.StatusCode(err) != http.StatusForbidden {
		t.Errorf("LogFiles refused = %v", err)
	}
	c, _ = newFake(t, Emby, map[string]route{"GET /System/Logs/Query": answer(http.StatusForbidden, "")})
	if _, err := c.LogFiles(t.Context()); client.StatusCode(err) != http.StatusForbidden {
		t.Errorf("Emby LogFiles refused = %v", err)
	}
}

// A new Jellyfin library asked to scan is created without the scan the
// create could carry (dropped when a scan is already running) and then asks
// for the library scan on its own, once; without Refresh it asks for none.
func TestCreateLibraryJellyfinRefresh(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Jellyfin, map[string]route{
		"POST /Library/VirtualFolders": noContent,
		"POST /Library/Refresh":        noContent,
	})
	if err := c.CreateLibrary(t.Context(), LibrarySpec{Name: "Films", CollectionType: "movies", Paths: []string{"/media/films"}, Refresh: true}); err != nil {
		t.Fatal(err)
	}
	if q := f.only("POST /Library/VirtualFolders").query; q.Get("refreshLibrary") != "false" || q.Get("collectionType") != "movies" || q.Get("name") != "Films" {
		t.Errorf("create = %v", q)
	}
	f.only("POST /Library/Refresh")
	// the scan is asked for after the create, not before
	if f.requests[0].path != "/Library/VirtualFolders" || f.requests[1].path != "/Library/Refresh" {
		t.Errorf("order = %v", f.requests)
	}

	if err := c.CreateLibrary(t.Context(), LibrarySpec{Name: "More", CollectionType: "movies", Paths: []string{"/media/more"}}); err != nil {
		t.Fatal(err)
	}
	if n := len(f.all("POST /Library/Refresh")); n != 1 {
		t.Errorf("a create without Refresh asked for a scan (%d scans in all)", n)
	}

	// a create the server refuses asks for no scan
	c, f = newFake(t, Jellyfin, map[string]route{"POST /Library/VirtualFolders": answer(http.StatusBadRequest, "bad path")})
	if err := c.CreateLibrary(t.Context(), LibrarySpec{Name: "Bad", CollectionType: "movies", Paths: []string{"/nope"}, Refresh: true}); client.StatusCode(err) != http.StatusBadRequest {
		t.Errorf("a refused create = %v", err)
	}
	if n := len(f.all("POST /Library/Refresh")); n != 0 {
		t.Errorf("a refused create asked for %d scans", n)
	}
}
