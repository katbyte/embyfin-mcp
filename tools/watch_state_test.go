package tools

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
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

// audit_unwatched read only what was played to the end, so a film stopped at
// four fifths - or begun and abandoned in its first minutes, which both
// servers count as a play with no resume point - was "never watched" beside
// the films nobody opened, on the list of what to archive or delete. Those
// are listed apart as started, with who and how far. And an account whose
// view hides part of a library is named, since what it played there is not
// read.
func TestUnwatchedListsWhatIsStartedApart(t *testing.T) {
	t.Parallel()

	for _, sorted := range []bool{true, false} {
		f := newFakeServer(t)
		f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, page(map[string]any{"Name": "Zzyzx Films", "CollectionType": "movies", "ItemId": "lib9", "Locations": []string{"/zz/films"}}))
		})
		users := []map[string]any{
			{"Id": "u1", "Name": "root", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}},
			{"Id": "u2", "Name": "alice", "Policy": map[string]any{"EnableAllFolders": true, "BlockedTags": []string{"gore"}}},
		}
		f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(users...)) })
		alien, arrival, dune := film("1", "Alien", 1979), film("2", "Arrival", 2016), film("3", "Dune", 2021)
		for i, it := range []map[string]any{alien, arrival, dune} {
			it["ProviderIds"] = map[string]any{"Tmdb": []string{"348", "329865", "438631"}[i]}
			it["DateCreated"] = []string{"2020-01-01T00:00:00Z", "2020-02-01T00:00:00Z", "2020-03-01T00:00:00Z"}[i]
		}
		withData := func(it map[string]any, data map[string]any) map[string]any {
			out := map[string]any{"UserData": data}
			maps.Copy(out, it)

			return out
		}
		// alice is four fifths through Arrival, and began Dune and stopped
		// in its first minutes: a play counted, no resume point
		resumable := withData(arrival, map[string]any{"PlaybackPositionTicks": 90 * 60 * ticksPerSecond, "PlayedPercentage": 80, "PlayCount": 1, "LastPlayedDate": "2026-09-01T20:00:00Z"})
		begun := withData(dune, map[string]any{"PlayCount": 1, "LastPlayedDate": "2026-09-02T20:00:00Z"})
		never := withData(alien, map[string]any{})
		f.mux.HandleFunc("GET /Users/{user}/Items", func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("user") != "u2" {
				writeJSON(t, w, page())

				return
			}
			switch param(r.URL.Query(), "Filters") {
			case "IsResumable":
				writeJSON(t, w, page(resumable))
			case "IsUnplayed":
				if !sorted {
					// a server that did not keep the order asked for
					writeJSON(t, w, page(never, begun, resumable))

					return
				}
				writeJSON(t, w, page(resumable, begun, never))
			default:
				writeJSON(t, w, page())
			}
		})
		f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(alien, arrival, dune)) })
		out := mustCall(t, session(t, f, Options{}), "audit_unwatched", nil)

		findings := objects(t, out["findings"], "findings")
		if len(findings) != 1 || text(findings[0]["name"]) != "Alien" || number(t, out["total_findings"], "total_findings") != 1 {
			t.Errorf("sorted %v: findings = %v, want Alien alone", sorted, findings)
		}
		started := objects(t, out["started"], "started")
		details := map[string]string{}
		for _, row := range started {
			details[text(row["name"])] = text(row["detail"])
		}
		if len(started) != 2 || number(t, out["total_started"], "total_started") != 2 ||
			details["Arrival"] != "started and never finished, by alice (80%); added 2020-02-01" || details["Dune"] != "started and never finished, by alice; added 2020-03-01" {
			t.Errorf("sorted %v: started = %v", sorted, details)
		}
		if limited := texts(out["views_limited"]); !slices.Equal(limited, []string{"alice: items tagged gore blocked"}) {
			t.Errorf("sorted %v: views_limited = %v", sorted, limited)
		}
	}
}

// What an account's view hides is said whatever hides it: a parental rating
// limit of 0 (Jellyfin's strictest) is a limit, and no limit is none; an
// allowed-tags list shows only those tags.
func TestViewLimitsNameWhatHides(t *testing.T) {
	t.Parallel()

	zero, twelve := 0, 12
	for _, tc := range []struct {
		policy embyfin.UserPolicy
		want   string
	}{
		{embyfin.UserPolicy{}, ""},
		{embyfin.UserPolicy{MaxParentalRating: &zero}, "a parental rating limit"},
		{embyfin.UserPolicy{MaxParentalRating: &twelve}, "a parental rating limit"},
		{embyfin.UserPolicy{AllowedTags: []string{"kids"}}, "only items tagged kids shown"},
		{embyfin.UserPolicy{BlockedTags: []string{"gore"}, BlockUnratedItems: []string{"Movie"}}, "unrated Movie blocked; items tagged gore blocked"},
		{embyfin.UserPolicy{BlockedFolders: []string{"a", "b"}}, "2 folders blocked by id"},
	} {
		if got := viewLimits(&embyfin.User{Policy: tc.policy}); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.policy, got, tc.want)
		}
	}
}
