package tools

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// Both servers answer a single read in a user's view with 404 for an item the
// user may not see and for an id no item has. Read as the first, a mistyped
// id was {item:"", users:[]}: nobody has watched this. The library read with
// no user tells the two apart, and an id nothing has is an error.
func TestItemLastWatchedRefusesAnUnknownID(t *testing.T) {
	t.Parallel()

	for _, jellyfin := range []bool{false, true} {
		f := newFakeServer(t)
		f.jellyfin = jellyfin
		users := []map[string]any{
			{"Id": "u1", "Name": "root", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}},
			{"Id": "u2", "Name": "alice", "Policy": map[string]any{"EnableAllFolders": false}},
		}
		arrival := map[string]any{"Id": "32", "Name": "Arrival", "Type": "Movie", "UserData": map[string]any{"Played": true, "PlayCount": 1}}
		// the library holds Arrival; alice may not see it
		f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(users...)) })
		f.mux.HandleFunc("GET /Users", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, users) })
		f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
			if slices.Contains(strings.Split(param(r.URL.Query(), "Ids"), ","), "32") {
				writeJSON(t, w, page(arrival))
				return
			}
			writeJSON(t, w, page())
		})
		seen := func(user, id string) bool { return id == "32" && user == "u1" }
		// Emby's single read answers what the user may not see, and its list
		// in the user's view leaves it out; Jellyfin's single read is a 404
		f.mux.HandleFunc("GET /Users/{user}/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") != "32" {
				http.NotFound(w, r)
				return
			}
			writeJSON(t, w, arrival)
		})
		f.mux.HandleFunc("GET /Users/{user}/Items", func(w http.ResponseWriter, r *http.Request) {
			if seen(r.PathValue("user"), param(r.URL.Query(), "Ids")) {
				writeJSON(t, w, page(arrival))
				return
			}
			writeJSON(t, w, page())
		})
		f.mux.HandleFunc("GET /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
			if !seen(param(r.URL.Query(), "userId"), r.PathValue("id")) {
				http.NotFound(w, r)
				return
			}
			writeJSON(t, w, arrival)
		})
		cs := session(t, f, Options{})

		if msg := mustRefuse(t, cs, "item_last_watched", map[string]any{"id": "99999999"}); !strings.Contains(msg, "no item with id 99999999") {
			t.Errorf("jellyfin %v: an id nothing has = %q", jellyfin, msg)
		}
		out := mustCall(t, cs, "item_last_watched", map[string]any{"id": "32"})
		if rows := objects(t, out["users"], "users"); out["item"] != "Arrival" || len(rows) != 1 || rows[0]["user"] != "root" {
			t.Errorf("jellyfin %v: an item alice may not see = %v, want root's row alone", jellyfin, out)
		}
	}
}

// audit_unwatched promises what no account has watched, and an account that
// lost access to a library had watched there all the same. Its own view
// leaves that library out on both servers; Jellyfin lists it with the library
// named as the parent, and Emby, which leaves the library out even then, with
// a folder the library is built from named instead.
func TestUnwatchedCountsPlaysInALibraryLostAccessTo(t *testing.T) {
	t.Parallel()

	for _, jellyfin := range []bool{false, true} {
		f := newFakeServer(t)
		f.jellyfin = jellyfin
		shows := map[string]any{"Name": "Shows", "CollectionType": "tvshows", "ItemId": "lib-shows", "Guid": "guid-shows", "Locations": []string{"/media/shows"}}
		films := map[string]any{"Name": "Films", "CollectionType": "movies", "ItemId": "lib-films", "Guid": "guid-films", "Locations": []string{"/media/films"}}
		if jellyfin {
			shows["Guid"], films["Guid"] = "", ""
		}
		f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(shows, films)) })
		f.mux.HandleFunc("GET /Library/VirtualFolders", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, []map[string]any{shows, films}) })
		enabled := "guid-films"
		if jellyfin {
			enabled = "lib-films"
		}
		users := []map[string]any{
			{"Id": "u1", "Name": "root", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}},
			{"Id": "u2", "Name": "alice", "Policy": map[string]any{"EnableAllFolders": false, "EnabledFolders": []string{enabled}}},
		}
		f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(users...)) })
		f.mux.HandleFunc("GET /Users", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, users) })

		breakingBad := map[string]any{"Id": "bb", "Name": "Breaking Bad", "Type": "Series", "ProviderIds": map[string]any{"Tmdb": "1396"}, "Path": "/media/shows/Breaking Bad"}
		expanse := map[string]any{"Id": "ex", "Name": "The Expanse", "Type": "Series", "ProviderIds": map[string]any{"Tmdb": "63639"}, "Path": "/media/shows/The Expanse"}
		pilot := map[string]any{"Id": "e1", "Name": "Pilot", "Type": "Episode", "SeriesId": "bb", "SeriesName": "Breaking Bad"}
		folder := map[string]any{"Id": "f-shows", "Name": "shows", "Type": "Folder", "Path": "/media/shows"}
		// alice watched the pilot; her view has no Shows in it, and only a
		// read naming the library (Jellyfin) or its folder (Emby) finds it
		hidden := "f-shows"
		if jellyfin {
			hidden = "lib-shows"
		}
		played := func(w http.ResponseWriter, r *http.Request, user string) {
			q := r.URL.Query()
			switch {
			case param(q, "Filters") != "IsPlayed":
				writeJSON(t, w, page(breakingBad, expanse))
			case user == "u2" && param(q, "ParentId") == hidden:
				writeJSON(t, w, page(pilot))
			default:
				writeJSON(t, w, page())
			}
		}
		f.mux.HandleFunc("GET /Users/{user}/Items", func(w http.ResponseWriter, r *http.Request) { played(w, r, r.PathValue("user")) })
		f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			switch {
			case param(q, "userId") != "":
				played(w, r, param(q, "userId"))
			case param(q, "Path") == "/media/shows":
				writeJSON(t, w, page(folder))
			case param(q, "Ids") != "":
				writeJSON(t, w, page(breakingBad))
			default:
				writeJSON(t, w, page(breakingBad, expanse))
			}
		})
		cs := session(t, f, Options{})

		out := mustCall(t, cs, "audit_unwatched", map[string]any{"library": "Shows", "types": "Series"})
		found := objects(t, out["findings"], "findings")
		got := make([]string, 0, len(found))
		for _, row := range found {
			got = append(got, text(row["name"]))
		}
		if !slices.Equal(got, []string{"The Expanse"}) {
			t.Errorf("jellyfin %v: unwatched = %v, want The Expanse alone: alice watched Breaking Bad's pilot before she lost Shows", jellyfin, got)
		}
		if users := texts(out["users"]); !slices.Equal(users, []string{"root", "alice"}) {
			t.Errorf("jellyfin %v: users = %v", jellyfin, users)
		}
	}
}

// item_last_watched agrees with audit_unwatched about an account that lost
// access to a library: it watched there all the same. Its row says so; an
// account that cannot see the item and never watched it has none. Emby's
// single read answers for the item the account may not see, with the play
// count its lists leave out; Jellyfin's is a 404, and it lists the item with
// the library named as the parent.
func TestItemLastWatchedCountsAnAccountThatLostAccess(t *testing.T) {
	t.Parallel()

	for _, jellyfin := range []bool{false, true} {
		f := newFakeServer(t)
		f.jellyfin = jellyfin
		films := map[string]any{"Name": "Films", "CollectionType": "movies", "ItemId": "lib-films", "Guid": "guid-films", "Locations": []string{"/media/films"}}
		if jellyfin {
			films["Guid"] = ""
		}
		f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(films)) })
		f.mux.HandleFunc("GET /Library/VirtualFolders", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, []map[string]any{films}) })
		users := []map[string]any{
			{"Id": "u1", "Name": "root", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}},
			{"Id": "u2", "Name": "alice", "Policy": map[string]any{"EnableAllFolders": false}},
			{"Id": "u3", "Name": "bob", "Policy": map[string]any{"EnableAllFolders": false}},
		}
		f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(users...)) })
		f.mux.HandleFunc("GET /Users", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, users) })
		arrival := func(user string) map[string]any {
			played := map[string]any{"Played": false}
			if user == "u2" {
				played = map[string]any{"Played": true, "PlayCount": 2, "LastPlayedDate": "2026-09-01T20:00:00Z"}
			}
			return map[string]any{"Id": "32", "Name": "Arrival", "Type": "Movie", "Path": "/media/films/Arrival (2016)/Arrival (2016).mkv", "UserData": played}
		}
		f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			user := param(q, "userId")
			switch {
			case user == "":
				writeJSON(t, w, page(arrival("")))
			case user == "u1", param(q, "ParentId") == "lib-films":
				// root sees it; the others only with the library named
				writeJSON(t, w, page(arrival(user)))
			default:
				writeJSON(t, w, page())
			}
		})
		f.mux.HandleFunc("GET /Users/{user}/Items", func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("user") == "u1" {
				writeJSON(t, w, page(arrival("u1")))
				return
			}
			writeJSON(t, w, page())
		})
		f.mux.HandleFunc("GET /Users/{user}/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, arrival(r.PathValue("user")))
		})
		f.mux.HandleFunc("GET /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
			if param(r.URL.Query(), "userId") != "u1" {
				http.NotFound(w, r)
				return
			}
			writeJSON(t, w, arrival("u1"))
		})
		cs := session(t, f, Options{})

		out := mustCall(t, cs, "item_last_watched", map[string]any{"id": "32"})
		got, _ := json.Marshal(out["users"])
		want := `[{"played":false,"user":"root"},{"last_played":"2026-09-01T20:00:00Z","no_access":true,"play_count":2,"played":true,"user":"alice"}]`
		if string(got) != want {
			t.Errorf("jellyfin %v: users = %s, want root's row and alice's watch before she lost the library, and nothing of bob's", jellyfin, got)
		}
	}
}
