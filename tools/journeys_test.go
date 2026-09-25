package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// What the acceptance journeys found, pinned against the canned server:
// names that several share, adds of what is already held, a user's view of
// a library, and the activity log's playback entries on both servers.

func TestPlaybackEvent(t *testing.T) {
	t.Parallel()

	for typ, want := range map[string]string{
		"playback.start": "start", "playback.stop": "stop", // Emby
		"VideoPlayback": "start", "VideoPlaybackStopped": "stop", // Jellyfin
		"AudioPlayback": "start", "AudioPlaybackStopped": "stop",
		"user.authenticated": "", "SessionStarted": "",
	} {
		got, ok := playbackEvent(typ)
		if got != want || ok != (want != "") {
			t.Errorf("playbackEvent(%q) = %q, %v, want %q", typ, got, ok, want)
		}
	}
}

func TestSharedNamesAndHeldItems(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	var added []string
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("ParentId") == "c1": // the collection's members, with what was added
			rows := []string{`{"Id":"a","Name":"Alien"}`}
			for _, ids := range added {
				for id := range strings.SplitSeq(ids, ",") {
					rows = append(rows, `{"Id":"`+id+`"}`)
				}
			}
			_, _ = io.WriteString(w, `{"Items":[`+strings.Join(rows, ",")+`],"TotalRecordCount":1}`)
		case q.Get("IncludeItemTypes") == "Playlist":
			_, _ = io.WriteString(w, `{"Items":[{"Id":"p1","Name":"Mix"},{"Id":"p2","Name":"mix"}],"TotalRecordCount":2}`)
		default:
			_, _ = io.WriteString(w, `{"Items":[{"Id":"c1","Name":"Set"}],"TotalRecordCount":1}`)
		}
	})
	f.mux.HandleFunc("POST /Collections/c1/Items", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		added = append(added, r.URL.Query().Get("Ids"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	cs := session(t, f, Options{})

	// a name two playlists share is refused, naming both
	if _, msg := callTool(t, cs, "playlist_get", map[string]any{"playlist": "MIX"}); !strings.Contains(msg, "2 playlists are named") || !strings.Contains(msg, "p1, p2") {
		t.Errorf("playlist_get by a shared name: %s", msg)
	}

	// an add counts what is new and sends only that
	out, msg := callTool(t, cs, "collection_add", map[string]any{"collection": "set", "item_ids": []any{"a", "b", "b"}})
	if msg != "" || out["added"] != float64(1) || out["already_held"] != float64(2) {
		t.Errorf("collection_add = %v %s", out, msg)
	}
	if out, msg = callTool(t, cs, "collection_add", map[string]any{"collection": "Set", "item_ids": []any{"a"}}); msg != "" || out["added"] != float64(0) {
		t.Errorf("collection_add of a member = %v %s", out, msg)
	}
	if len(added) != 1 || added[0] != "b" {
		t.Errorf("adds sent = %v, want one of b", added)
	}

	// and a collection under a name that exists is refused
	if _, msg := callTool(t, cs, "collection_create", map[string]any{"name": "SET", "item_ids": []any{"b"}}); !strings.Contains(msg, "exists (id c1)") {
		t.Errorf("collection_create under an existing name: %s", msg)
	}
}

func TestUserLibraryAccessOnEmby(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	// Emby lists a user's libraries by folder Guid; the numeric id grants nothing
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[
			{"Id":"u1","Name":"root","Policy":{"IsAdministrator":true,"EnableAllFolders":true}},
			{"Id":"u2","Name":"alice","Policy":{"EnableAllFolders":false,"EnabledFolders":["guid-movies"]}}],"TotalRecordCount":2}`)
	})
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[
			{"Name":"Movies","ItemId":"3","Guid":"guid-movies","CollectionType":"movies"},
			{"Name":"Shows","ItemId":"5","Guid":"guid-shows","CollectionType":"tvshows"}],"TotalRecordCount":2}`)
	})
	f.mux.HandleFunc("GET /Users/u2/Items", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[],"TotalRecordCount":0}`)
	})
	cs := session(t, f, Options{})

	out, msg := callTool(t, cs, "user_get", map[string]any{"user": "alice"})
	if msg != "" {
		t.Fatal(msg)
	}
	if libs, isList := out["libraries"].([]any); !isList || len(libs) != 1 || libs[0] != "Movies" {
		t.Errorf("alice's libraries = %v, want [Movies]", out["libraries"])
	}
	if _, msg := callTool(t, cs, "library_items", map[string]any{"library": "Shows", "user": "alice"}); !strings.Contains(msg, "alice cannot see the Shows library") {
		t.Errorf("library_items in a library alice cannot see: %s", msg)
	}
	if _, msg := callTool(t, cs, "library_items", map[string]any{"library": "Movies", "user": "alice"}); msg != "" {
		t.Errorf("library_items in Movies for alice: %s", msg)
	}
}

// Edits of one item made at once each read the item and post it back; held
// for the round, none posts back a read that another's change has made stale.
func TestParallelEditsKeepEveryChange(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Id":"admin","Name":"root","Policy":{"IsAdministrator":true}}],"TotalRecordCount":1}`)
	})
	var mu sync.Mutex
	tags := `[]`
	f.mux.HandleFunc("GET /Users/admin/Items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		body := `{"Id":"1","Name":"Arrival","TagItems":` + tags + `}`
		mu.Unlock()
		time.Sleep(20 * time.Millisecond) // long enough for another read to overlap
		_, _ = io.WriteString(w, body)
	})
	f.mux.HandleFunc("POST /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		var item struct{ TagItems json.RawMessage }
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &item)
		mu.Lock()
		tags = string(item.TagItems)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	cs := session(t, f, Options{})

	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "item_edit", Arguments: map[string]any{"ids": []any{"1"}, "add_tags": []any{fmt.Sprintf("tag-%d", i)}}})
			if err != nil || res.IsError {
				t.Errorf("item_edit tag-%d: %v %v", i, err, res)
			}
		})
	}
	wg.Wait()
	for i := range 6 {
		if !strings.Contains(tags, fmt.Sprintf(`"tag-%d"`, i)) {
			t.Errorf("tag-%d was lost: the item's tags are %s", i, tags)
		}
	}
}

// library_genres reads the genres off the items, so an edit shows at once
// and a genre no item carries any more is gone, where the servers' own genre
// lists lag.
func TestLibraryGenresReadOffTheItems(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Id":"1","Name":"Alien","Genres":["Horror","Zzyzx Saga"]},{"Id":"2","Name":"Aliens","Genres":["Action","Zzyzx Saga"]}],"TotalRecordCount":2}`)
	})
	cs := session(t, f, Options{})

	out, msg := callTool(t, cs, "library_genres", nil)
	if got, _ := json.Marshal(out["genres"]); msg != "" || string(got) != `["Action","Horror","Zzyzx Saga"]` {
		t.Errorf("library_genres = %s %s", got, msg)
	}
	if q := f.requests("/Items")[0].Query; !strings.Contains(q, "IncludeItemTypes=Movie%2CSeries") {
		t.Errorf("the sweep = %s", q)
	}
	if len(f.requests("/Genres")) != 0 {
		t.Error("the server's genre list was read")
	}
}

// Emby lists the next episode of a series among a user's resume items once
// the one before is marked watched, at position zero: that is next up, not
// in progress, and user_next_up's in_progress leaves it out.
func TestInProgressLeavesOutNextUp(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Id":"admin","Name":"root","Policy":{"IsAdministrator":true}}],"TotalRecordCount":1}`)
	})
	f.mux.HandleFunc("GET /Users/admin/Items/Resume", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[
			{"Id":"e2","Name":"Cat's in the Bag...","Type":"Episode","UserData":{"PlaybackPositionTicks":0,"Played":false}},
			{"Id":"m1","Name":"Arrival","Type":"Movie","UserData":{"PlaybackPositionTicks":600000000,"PlayedPercentage":42}}],"TotalRecordCount":2}`)
	})
	f.mux.HandleFunc("GET /Shows/NextUp", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Id":"e2","Name":"Cat's in the Bag...","Type":"Episode"}],"TotalRecordCount":1}`)
	})
	cs := session(t, f, Options{})

	out, msg := callTool(t, cs, "user_next_up", nil)
	if got, _ := json.Marshal(out["in_progress"]); msg != "" || !strings.Contains(string(got), `"name":"Arrival"`) || strings.Contains(string(got), "Cat's in the Bag") {
		t.Errorf("user_next_up in_progress = %s %s", got, msg)
	}
	if got, _ := json.Marshal(out["next_up"]); !strings.Contains(string(got), "Cat's in the Bag") {
		t.Errorf("user_next_up next_up = %s", got)
	}
}

// replace_all has nothing to fetch in a library with its metadata fetchers
// off (and Jellyfin clears the item), so it is refused there, and only there.
func TestReplaceAllNeedsFetchers(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	// each refresh saves the item a moment after it is asked for, which
	// moves its etag
	var mu sync.Mutex
	saves := map[string]int{}
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Query().Get("Ids") == "1" {
			_, _ = fmt.Fprintf(w, `{"Items":[{"Id":"1","Name":"Interstellar","Type":"Movie","Etag":"e%d","Path":"/media/messy-movies/Interstellar (2014)/Interstellar (2014).mp4"}],"TotalRecordCount":1}`, saves["1"])
			return
		}
		_, _ = fmt.Fprintf(w, `{"Items":[{"Id":"2","Name":"Dune","Type":"Movie","Etag":"e%d","Path":"/media/messy/Dune (2021)/Dune (2021).mp4"}],"TotalRecordCount":1}`, saves["2"])
	})
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[
			{"Name":"Messy","ItemId":"8","Locations":["/media/messy"],"LibraryOptions":{"TypeOptions":[{"Type":"Movie","MetadataFetchers":[]}]}},
			{"Name":"Messy Movies","ItemId":"9","Locations":["/media/messy-movies/"],"LibraryOptions":{"TypeOptions":[{"Type":"Movie","MetadataFetchers":["TheMovieDb"]}]}}],"TotalRecordCount":2}`)
	})
	f.mux.HandleFunc("POST /Items/{id}/Refresh", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		saves[r.PathValue("id")]++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	cs := session(t, f, Options{})

	// /media/messy-movies is not inside /media/messy
	if out, msg := callTool(t, cs, "item_refresh", map[string]any{"id": "1", "replace_all": true}); msg != "" || !boolean(t, out["landed"], "landed") {
		t.Errorf("replace_all with the fetchers on: %v %s", out, msg)
	}
	if _, msg := callTool(t, cs, "item_refresh", map[string]any{"id": "2", "replace_all": true}); !strings.Contains(msg, "the Messy library has its metadata fetchers off") {
		t.Errorf("replace_all with the fetchers off: %s", msg)
	}
	if _, msg := callTool(t, cs, "item_refresh", map[string]any{"id": "2"}); msg != "" {
		t.Errorf("a refresh with the fetchers off: %s", msg)
	}
	if n := len(f.requests("/Items/2/Refresh")); n != 1 {
		t.Errorf("the film without fetchers was refreshed %d times, want the plain refresh alone", n)
	}
}

// Copies of a film share its provider ids, and Emby marks them all when one
// is watched or favourited: a user's counts are by film. The counts are
// user_stats' alone; user_get reads the account and never sweeps the library.
func TestCountsByTitle(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Id":"admin","Name":"root","Policy":{"IsAdministrator":true,"EnableAllFolders":true}},{"Id":"u2","Name":"alice","Policy":{"EnableAllFolders":true}}],"TotalRecordCount":2}`)
	})
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[],"TotalRecordCount":0}`)
	})
	alien := func(id string) string {
		return `{"Id":"` + id + `","Name":"Alien","Type":"Movie","ProviderIds":{"Tmdb":"348","Imdb":"tt0078748"},"UserData":{"Played":true,"IsFavorite":true}}`
	}
	f.mux.HandleFunc("GET /Users/u2/Items", func(w http.ResponseWriter, r *http.Request) {
		// the one sweep: every film and episode with its state
		if r.URL.Query().Get("IncludeItemTypes") != "Movie,Episode" {
			_, _ = io.WriteString(w, `{"Items":[],"TotalRecordCount":0}`)
			return
		}
		_, _ = io.WriteString(w, `{"Items":[`+alien("1")+`,`+alien("2")+`,{"Id":"3","Name":"Alien","Type":"Movie","ProviderIds":{"Imdb":"tt0078748"},"UserData":{"Played":true}},{"Id":"4","Name":"Aliens","Type":"Movie","ProviderIds":{"Tmdb":"679"},"UserData":{"Played":true}},{"Id":"5","Name":"Zzyzx Home Video","Type":"Movie","UserData":{"Played":true}}],"TotalRecordCount":5}`)
	})
	cs := session(t, f, Options{})

	out, msg := callTool(t, cs, "user_stats", map[string]any{"user": "alice"})
	if msg != "" || out["movies_watched"] != float64(3) || out["favourites"] != float64(1) {
		t.Errorf("user_stats alice = movies_watched %v favourites %v %s, want Alien, Aliens and the home video watched and Alien favourited", out["movies_watched"], out["favourites"], msg)
	}
	if n := len(f.requests("/Users/u2/Items")); n != 1 {
		t.Errorf("user_stats swept alice's view %d times, want once", n)
	}

	f.reset()
	out, msg = callTool(t, cs, "user_get", map[string]any{"user": "alice"})
	if msg != "" || out["name"] != "alice" {
		t.Errorf("user_get alice = %v %s", out, msg)
	}
	for _, k := range []string{"movies_watched", "episodes_watched", "in_progress", "favourites"} {
		if _, ok := out[k]; ok {
			t.Errorf("user_get carries %s; the counts are user_stats'", k)
		}
	}
	if n := len(f.requests("/Users/u2/Items")); n != 0 {
		t.Errorf("user_get swept alice's view %d times, want none", n)
	}
}

// A change made in a user's name to an item they cannot see is refused, the
// same on both servers: Emby would store it, Jellyfin answer a bare 404.
func TestNoChangesForAUserWhoCannotSee(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Id":"admin","Name":"root","Policy":{"IsAdministrator":true}},{"Id":"u2","Name":"alice","Policy":{"EnableAllFolders":false}}],"TotalRecordCount":2}`)
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Id":"e2","Name":"Cat's in the Bag...","Type":"Episode"}],"TotalRecordCount":1}`)
	})
	// alice's view leaves it out; Emby's single-item read would still answer
	f.mux.HandleFunc("GET /Users/u2/Items", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[],"TotalRecordCount":0}`)
	})
	f.mux.HandleFunc("GET /Users/{user}/Items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Id":"e2","Name":"Cat's in the Bag...","UserData":{"Played":false}}`)
	})
	f.mux.HandleFunc("GET /Users/admin/Items", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Id":"e2","Name":"Cat's in the Bag...","Type":"Episode"}],"TotalRecordCount":1}`)
	})
	f.mux.HandleFunc("POST /Users/{user}/PlayedItems/{id}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	})
	cs := session(t, f, Options{})

	for _, args := range []map[string]any{
		{"id": "e2", "user": "alice", "watched": true},
		{"id": "e2", "user": "alice", "favourite": true},
		{"id": "e2", "user": "alice", "position_s": 60},
	} {
		if _, msg := callTool(t, cs, "item_set_state", args); !strings.Contains(msg, "alice cannot see Cat's in the Bag...") {
			t.Errorf("item_set_state %v for alice: %s", args, msg)
		}
	}
	if n := len(f.requests("/Users/u2/PlayedItems/e2")) + len(f.requests("/Users/u2/FavoriteItems/e2")) + len(f.requests("/Users/u2/Items/e2/UserData")); n != 0 {
		t.Errorf("the refused change was sent %d times", n)
	}
	if _, msg := callTool(t, cs, "item_set_state", map[string]any{"id": "e2", "watched": true}); msg != "" {
		t.Errorf("item_set_state for root: %s", msg)
	}

	// and her watch state on it is not reported
	out, msg := callTool(t, cs, "item_last_watched", map[string]any{"id": "e2"})
	if got, _ := json.Marshal(out["users"]); msg != "" || string(got) != `[{"played":false,"user":"root"}]` {
		t.Errorf("item_last_watched users = %s %s", got, msg)
	}
}

// audit_unwatched counts a film watched when any copy of it is, wherever the
// copy is.
func TestUnwatchedByTitle(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Id":"admin","Name":"root","Policy":{"IsAdministrator":true,"EnableAllFolders":true}}],"TotalRecordCount":1}`)
	})
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Name":"Films","ItemId":"lib","CollectionType":"movies","Locations":["/m"]}],"TotalRecordCount":1}`)
	})
	library := `{"Items":[
			{"Id":"messy","Name":"Alien","Type":"Movie","ProviderIds":{"Tmdb":"348"}},
			{"Id":"arrival","Name":"Arrival","Type":"Movie","ProviderIds":{"Tmdb":"329865"}}],"TotalRecordCount":2}`
	// what the account played, and the library as it is shown the account
	f.mux.HandleFunc("GET /Users/admin/Items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("Filters") == "IsPlayed" {
			_, _ = io.WriteString(w, `{"Items":[{"Id":"clean","Name":"Alien","Type":"Movie","ProviderIds":{"Tmdb":"348"}}],"TotalRecordCount":1}`)
			return
		}
		_, _ = io.WriteString(w, library)
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, library)
	})
	cs := session(t, f, Options{})

	out, msg := callTool(t, cs, "audit_unwatched", nil)
	if got, _ := json.Marshal(out["findings"]); msg != "" || out["total_findings"] != float64(1) || !strings.Contains(string(got), `"name":"Arrival"`) {
		t.Errorf("audit_unwatched = %s %s, want Arrival alone", got, msg)
	}
	if q := f.requests("/Users/admin/Items")[0].Query; strings.Contains(q, "ParentId") {
		t.Errorf("the watched sweep was scoped: %s", q)
	}
}

// In a library with its metadata fetchers off there is nothing to fetch, and
// the server's apply refreshes the item as if there were: Jellyfin's replaced
// every episode number of a series with nothing (seen live). So there the
// candidate's ids are set with a plain edit of the item, which changes the
// ids and nothing else, and read back. With the fetchers on the server's own
// apply runs, re-fetching as it should.
func TestIdentifyApplyWithTheFetchersOff(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name, path, id string
		fetchersOff    bool
	}{
		{"fetchers off", "/media/messy-shows/Zzyzx Show", "2", true},
		{"fetchers on", "/media/shows/Zzyzx Show", "3", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			var mu sync.Mutex
			ids := map[string]any{}
			f := newFakeServer(t)
			f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"Items":[{"Id":"admin","Name":"root","Policy":{"IsAdministrator":true,"EnableAllFolders":true}}],"TotalRecordCount":1}`)
			})
			f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"Items":[
					{"Name":"Messy Shows","ItemId":"8","Locations":["/media/messy-shows"],"LibraryOptions":{"TypeOptions":[{"Type":"Series","MetadataFetchers":[]}]}},
					{"Name":"Shows","ItemId":"9","Locations":["/media/shows"],"LibraryOptions":{"TypeOptions":[{"Type":"Series","MetadataFetchers":["TheMovieDb"]}]}}],"TotalRecordCount":2}`)
			})
			item := func() map[string]any {
				mu.Lock()
				defer mu.Unlock()
				return map[string]any{"Id": c.id, "Name": "Zzyzx Show", "Type": "Series", "Path": c.path, "ProviderIds": ids}
			}
			f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, page(item()))
			})
			f.mux.HandleFunc("GET /Users/admin/Items/{id}", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, item())
			})
			f.mux.HandleFunc("POST /Items/RemoteSearch/Series", func(w http.ResponseWriter, r *http.Request) {
				// asked by the candidate's id, the provider's record of the
				// show carries its other ids too
				info := object(t, readBody(t, r)["SearchInfo"], "SearchInfo")
				if known, ok := info["ProviderIds"].(map[string]any); ok && known["Tmdb"] == "655" {
					_, _ = io.WriteString(w, `[{"Name":"Zzyzx Show","ProductionYear":1987,"ProviderIds":{"Tmdb":"655","Imdb":"tt0000655","Tvdb":"70655"}}]`)
					return
				}
				_, _ = io.WriteString(w, `[{"Name":"Zzyzx Show","ProductionYear":1987,"ProviderIds":{"Tmdb":"655"}}]`)
			})
			// the show's folder holds the nfo the server reads for it
			f.mux.HandleFunc("GET /Environment/DirectoryContents", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("Path") != c.path {
					http.NotFound(w, r)
					return
				}
				writeJSON(t, w, []map[string]any{{"Name": "tvshow.nfo", "Path": c.path + "/tvshow.nfo", "Type": "File"}, {"Name": "Season 01", "Path": c.path + "/Season 01", "Type": "Directory"}})
			})
			f.mux.HandleFunc("POST /Items/RemoteSearch/Apply/{id}", func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				ids = map[string]any{"Tmdb": "655"}
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			})
			f.mux.HandleFunc("POST /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
				body := readBody(t, r)
				mu.Lock()
				if sent, ok := body["ProviderIds"].(map[string]any); ok {
					ids = sent
				}
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			})
			cs := session(t, f, Options{})

			out, msg := callTool(t, cs, "item_identify_apply", map[string]any{"id": c.id, "kind": "series", "candidate": 0})
			if msg != "" {
				t.Fatal(msg)
			}
			if got, ok := out["metadata_provider_ids"].(map[string]any); !ok || got["tmdb"] != "655" {
				t.Errorf("item_identify_apply = %v", out)
			}
			applied, edited := len(f.requests("/Items/RemoteSearch/Apply/"+c.id)), len(f.requests("/Items/"+c.id))
			switch {
			case c.fetchersOff && (applied != 0 || edited != 1):
				t.Errorf("with the fetchers off: %d applies and %d edits, want the edit alone", applied, edited)
			case !c.fetchersOff && (applied != 1 || edited != 0):
				t.Errorf("with the fetchers on: %d applies and %d edits, want the apply alone", applied, edited)
			}
			note := text(out["note"])
			if c.fetchersOff != strings.Contains(note, "metadata fetchers off") {
				t.Errorf("note = %q", note)
			}
			// the item was no title before, and is one now: what the next
			// refresh may undo is said - the nfo it reads again, and on Emby
			// the watch state that follows the ids
			if !strings.Contains(note, "the nfo beside the file (tvshow.nfo)") || !strings.Contains(note, "watched mark and favourite for the title it was matched to") {
				t.Errorf("note = %q, want the nfo and the watch state warned of", note)
			}
			// with the fetchers off every id the provider knows the show by
			// is set, the candidate's TMDB id and the IMDb and TVDB ids its
			// record adds
			if c.fetchersOff {
				if got := object(t, out["metadata_provider_ids"], "metadata_provider_ids"); got["imdb"] != "tt0000655" || got["tvdb"] != "70655" {
					t.Errorf("with the fetchers off the ids set = %v, want the show's IMDb and TVDB ids beside TMDB's", got)
				}
			}
		})
	}
}
