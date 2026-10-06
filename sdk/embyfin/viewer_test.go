package embyfin

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// viewerServer is a canned Jellyfin with the accounts given, and a
// collection c1 holding a film and a series, which it lists one level deep in
// an account's view - leaving out the series for any account but one who
// sees everything (sees) - and recursively, with the series' season beside
// it, to a read with no user.
func viewerServer(t *testing.T, users, sees string) (*Client, *fake) {
	t.Helper()

	c, f := newFake(t, Jellyfin, map[string]route{
		"GET /Users": ok(users),
		"GET /Items": func(r *http.Request, _ string) (int, string) {
			q := r.URL.Query()
			switch {
			case q.Get("recursive") == "true":
				return http.StatusOK, `{"Items":[{"Id":"f1","Name":"Alien","Type":"Movie"},{"Id":"s1","Name":"Breaking Bad","Type":"Series","IsFolder":true},{"Id":"s2","Name":"Season 1","Type":"Season","IsFolder":true}],"TotalRecordCount":3}`
			case q.Get("userId") == sees:
				return http.StatusOK, `{"Items":[{"Id":"f1","Name":"Alien","Type":"Movie"},{"Id":"s1","Name":"Breaking Bad","Type":"Series","IsFolder":true}],"TotalRecordCount":2}`
			}
			return http.StatusOK, `{"Items":[{"Id":"f1","Name":"Alien","Type":"Movie"}],"TotalRecordCount":1}`
		},
		"GET /Playlists/p1/Items": func(r *http.Request, _ string) (int, string) {
			if r.URL.Query().Get("userId") == "" {
				return http.StatusBadRequest, "Error processing request."
			}
			return http.StatusOK, `{"Items":[{"Id":"f1","Name":"Alien","PlaylistItemId":"f1"}],"TotalRecordCount":1}`
		},
	})

	return c, f
}

// A collection read in the first administrator's view left out what that
// account was not given: a collection of Breaking Bad and Alien read as
// Alien alone, and its delete said it held Alien, the only record of what
// went. The read is made in the view of an administrator who sees every
// library, passing over one given fewer libraries, one with a tag blocked,
// and one with a parental limit of 0 - which the documents' plain number
// reads as none, and the server's own answer shows.
func TestACollectionIsReadInAViewOfEverything(t *testing.T) {
	t.Parallel()

	users := `[{"Id":"u1","Name":"root","Policy":{"IsAdministrator":true,"EnableAllFolders":false,"EnabledFolders":["m"]}},
		{"Id":"u2","Name":"bob","Policy":{"IsAdministrator":true,"EnableAllFolders":true,"EnableAllChannels":true,"MaxParentalRating":0}},
		{"Id":"u5","Name":"dave","Policy":{"IsAdministrator":true,"EnableAllFolders":true,"EnableAllChannels":true,"BlockedTags":["x"]}},
		{"Id":"u3","Name":"alice","Policy":{"IsAdministrator":false,"EnableAllFolders":true}},
		{"Id":"u4","Name":"carol","Policy":{"IsAdministrator":true,"EnableAllFolders":true,"EnableAllChannels":true}}]`
	c, f := viewerServer(t, users, "u4")

	members, err := c.CollectionMembers(t.Context(), "c1")
	if err != nil || strings.Join(members, ",") != "f1,s1" {
		t.Fatalf("the collection's members = %v, %v; want the film and the series", members, err)
	}
	for _, r := range f.all("GET /Items") {
		if r.query.Get("parentId") == "c1" && r.query.Get("userId") != "u4" {
			t.Errorf("the collection was read in %q's view, want carol's, the one who sees everything", r.query.Get("userId"))
		}
	}
	held, err := c.PlaylistHeld(t.Context(), "p1")
	if err != nil || len(held) != 1 {
		t.Errorf("the playlist = %v, %v", held, err)
	}
	if q := f.all("GET /Playlists/p1/Items")[0].query; q.Get("userId") != "u4" {
		t.Errorf("the playlist was read in %q's view, want carol's", q.Get("userId"))
	}
}

// With no account that sees everything, a collection is read recursively
// with no user, which is what it holds only when nothing in it is a folder:
// a season listed beside its series could be a member or only something
// under one. So one holding a series is refused, saying why, and a playlist,
// which Jellyfin will not read without a user, is refused too.
func TestNoViewOfEverythingIsSaid(t *testing.T) {
	t.Parallel()

	users := `[{"Id":"u1","Name":"root","Policy":{"IsAdministrator":true,"EnableAllFolders":false,"EnabledFolders":["m"]}}]`
	c, _ := viewerServer(t, users, "")

	_, err := c.CollectionMembers(t.Context(), "c1")
	if !errors.Is(err, ErrNoFullView) || !strings.Contains(err.Error(), "can't read every member of the collection: no administrator sees every library: root's view is narrowed") || !strings.Contains(err.Error(), "Breaking Bad (Series)") {
		t.Errorf("a collection of a series with no view of everything = %v", err)
	}
	_, err = c.PlaylistHeld(t.Context(), "p1")
	if !errors.Is(err, ErrNoFullView) || !strings.Contains(err.Error(), "can't read every entry of the playlist") {
		t.Errorf("a playlist with no view of everything = %v", err)
	}

	// films alone: the recursive read is what it holds
	flat, _ := newFake(t, Jellyfin, map[string]route{
		"GET /Users": ok(users),
		"GET /Items": ok(`{"Items":[{"Id":"f1","Name":"Alien","Type":"Movie"},{"Id":"f2","Name":"Aliens","Type":"Movie"}],"TotalRecordCount":2}`),
	})
	if members, err := flat.CollectionMembers(t.Context(), "c1"); err != nil || strings.Join(members, ",") != "f1,f2" {
		t.Errorf("a collection of films with no view of everything = %v, %v", members, err)
	}
}

// Emby lists every entry of a playlist, and every member of a collection, to
// a read with no user: that is how they are read, whatever any account sees.
func TestEmbyReadsListsWithNoUser(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Emby, map[string]route{
		"GET /Playlists/p1/Items": ok(`{"Items":[{"Id":"9","PlaylistItemId":"1"}],"TotalRecordCount":1}`),
		"GET /Items":              ok(`{"Items":[{"Id":"9"}],"TotalRecordCount":1}`),
	})
	if held, err := c.PlaylistHeld(t.Context(), "p1"); err != nil || len(held) != 1 {
		t.Fatalf("PlaylistHeld = %v, %v", held, err)
	}
	if _, err := c.CollectionMembers(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.requests {
		if r.query.Get("UserId") != "" || strings.HasPrefix(r.path, "/Users/") {
			t.Errorf("a list was read in a user's view: %s %v", r.path, r.query)
		}
	}
}

// An administrator given the libraries one by one, every library there is
// ticked, sees all of them, and lists are read in its view: counting only
// "every library" set as such refused the collection and playlist tools on
// such a server. One not given every channel is passed over - a playlist
// can hold a channel's item - and one missing a library still is.
func TestEveryLibraryTickedSeesAll(t *testing.T) {
	t.Parallel()

	users := `[{"Id":"u1","Name":"root","Policy":{"IsAdministrator":true,"EnableAllFolders":false,"EnabledFolders":["m","s"],"EnableAllChannels":false}},
		{"Id":"u2","Name":"bob","Policy":{"IsAdministrator":true,"EnableAllFolders":false,"EnabledFolders":["m"],"EnableAllChannels":true}},
		{"Id":"u4","Name":"carol","Policy":{"IsAdministrator":true,"EnableAllFolders":false,"EnabledFolders":["s","m"],"EnableAllChannels":true}}]`
	c, f := newFake(t, Jellyfin, map[string]route{
		"GET /Users":                  ok(users),
		"GET /Library/VirtualFolders": ok(`[{"Name":"Movies","ItemId":"m"},{"Name":"Shows","ItemId":"s"}]`),
		"GET /Items":                  ok(`{"Items":[{"Id":"f1","Name":"Alien","Type":"Movie"}],"TotalRecordCount":1}`),
	})
	if _, err := c.CollectionMembers(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.all("GET /Items") {
		if r.query.Get("userId") != "u4" {
			t.Errorf("the collection was read in %q's view, want carol's, who has every library and channel", r.query.Get("userId"))
		}
	}
}
