package embyfin

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Jellyfin refreshes a collection it has just made, and a delete that lands
// while the refresh runs is undone when the refresh saves the collection
// again: the delete answers 204 and the collection is back half a second
// later (seen live). DeleteCollection reads back until it has stayed gone,
// deletes again when it comes back, and gives up saying so.
func TestDeleteCollectionStaysGone(t *testing.T) {
	t.Parallel()

	// back returns whether the collection is listed on a read: reads counts
	// the reads since the last delete, deletes how many deletes there were.
	// The collection was last saved long ago, so no refresh of it can still
	// be running
	server := func(t *testing.T, back func(deletes, reads int) bool) (*Client, *fake) {
		t.Helper()

		deletes, reads := 0, 0
		c, f := newFake(t, Jellyfin, map[string]route{
			"GET /System/Info/Public": ok(`{}`),
			"DELETE /Items/c1": func(*http.Request, string) (int, string) {
				deletes++
				reads = 0
				return http.StatusNoContent, ""
			},
			"GET /Items": func(r *http.Request, _ string) (int, string) {
				if r.URL.Query().Get("minDateLastSaved") != "" {
					return http.StatusOK, `{"Items":[],"TotalRecordCount":0}`
				}
				reads++
				if back(deletes, reads) {
					return http.StatusOK, `{"Items":[{"Id":"c1","Name":"Zzyzx Set","Type":"BoxSet"}],"TotalRecordCount":1}`
				}
				return http.StatusOK, `{"Items":[],"TotalRecordCount":0}`
			},
		})
		c.settle, c.saveGrain = time.Millisecond, time.Millisecond

		return c, f
	}

	t.Run("gone at once", func(t *testing.T) {
		t.Parallel()
		c, f := server(t, func(int, int) bool { return false })
		seen, err := c.DeleteCollection(t.Context(), "c1")
		if err != nil {
			t.Fatal(err)
		}
		if n := len(f.all("DELETE /Items/c1")); n != 1 {
			t.Errorf("%d deletes, want 1", n)
		}
		// and it was looked for after, not taken on the 204's word
		if n := len(f.all("GET /Items")); n < 2 {
			t.Errorf("read back %d times, want it seen to stay gone", n)
		}
		if seen.Back != 0 || seen.Recent {
			t.Errorf("seen = %+v, want nothing back and no recent save", seen)
		}
	})

	t.Run("back once", func(t *testing.T) {
		t.Parallel()
		// gone on the first read after the first delete, back on the second
		c, f := server(t, func(deletes, reads int) bool { return deletes == 1 && reads >= 2 })
		seen, err := c.DeleteCollection(t.Context(), "c1")
		if err != nil {
			t.Fatal(err)
		}
		if n := len(f.all("DELETE /Items/c1")); n != 2 || seen.Back != 1 {
			t.Errorf("%d deletes, %d back, want the second delete when it came back", n, seen.Back)
		}
	})

	t.Run("back every time", func(t *testing.T) {
		t.Parallel()
		c, f := server(t, func(_, reads int) bool { return reads >= 2 })
		_, err := c.DeleteCollection(t.Context(), "c1")
		if err == nil || !strings.Contains(err.Error(), "came back") {
			t.Fatalf("err = %v, want one saying it came back", err)
		}
		if n := len(f.all("DELETE /Items/c1")); n != 3 {
			t.Errorf("%d deletes, want 3 tries", n)
		}
	})
}

// A refresh of a collection can run for a minute when the provider fails,
// and saves the collection back when it ends (seen on Jellyfin 12.1: sixty
// seconds after the collection was made, the provider asked about a name it
// could not answer for). A collection saved within the window a refresh may
// run for is asked about the way the refresh asks: the provider slow, it is
// watched until the window has passed since that save, not for a few
// seconds - here the refresh ends a second after the delete, long after the
// few seconds, and the collection comes back then and is deleted again; the
// provider answering at once, a refresh would end at once, and a few
// seconds' watch does.
func TestDeleteCollectionWatchesOutARecentRefresh(t *testing.T) {
	t.Parallel()

	for _, slow := range []bool{true, false} {
		seen, deletes, searches := recentCollectionDelete(t, slow)
		if searches != 1 {
			t.Errorf("slow %v: the provider was asked %d times, want once", slow, searches)
		}
		if !seen.Recent || seen.SavedAgo > 2*time.Second || seen.ProviderSlow != slow {
			t.Errorf("slow %v: seen = %+v, want the collection known to be saved a moment ago and the provider's pace read", slow, seen)
		}
		if slow && (deletes != 2 || seen.Back != 1 || seen.Watched < time.Second) {
			t.Errorf("slow provider: %d deletes, %d back, watched %v: want the refresh's save seen and deleted again", deletes, seen.Back, seen.Watched)
		}
		if !slow && (deletes != 1 || seen.Watched > time.Second) {
			t.Errorf("provider at once: %d deletes, watched %v: want a few intervals' watch", deletes, seen.Watched)
		}
	}
}

// recentCollectionDelete deletes a collection saved a moment ago whose
// refresh saves it back a second after the delete, the provider the refresh
// asks slow or not, and says what DeleteCollection saw.
func recentCollectionDelete(t *testing.T, slow bool) (seen CollectionDelete, deletes, searches int) {
	t.Helper()

	saved := time.Now().Truncate(time.Second)
	var deleted time.Time
	c, _ := newFake(t, Jellyfin, map[string]route{
		"GET /System/Info/Public": ok(`{}`),
		"POST /Items/RemoteSearch/BoxSet": func(*http.Request, string) (int, string) {
			searches++
			if slow {
				time.Sleep(time.Second) // past the ten intervals of 10ms
			}
			return http.StatusOK, `[]`
		},
		"DELETE /Items/c1": func(*http.Request, string) (int, string) {
			deletes++
			if deletes == 1 {
				deleted = time.Now()
			}
			return http.StatusNoContent, ""
		},
		"GET /Items": func(r *http.Request, _ string) (int, string) {
			// saved when made, and not since
			if since := r.URL.Query().Get("minDateLastSaved"); since != "" {
				at, err := time.Parse(time.RFC3339, since)
				if err != nil || at.After(saved) {
					return http.StatusOK, `{"Items":[],"TotalRecordCount":0}`
				}
				return http.StatusOK, `{"Items":[{"Id":"c1","Name":"Zzyzx Set","Type":"BoxSet"}],"TotalRecordCount":1}`
			}
			// read before the delete, and back once the refresh saves it
			// a second after the first delete
			if deletes == 0 || deletes == 1 && time.Since(deleted) > time.Second {
				return http.StatusOK, `{"Items":[{"Id":"c1","Name":"Zzyzx Set","Type":"BoxSet"}],"TotalRecordCount":1}`
			}
			return http.StatusOK, `{"Items":[],"TotalRecordCount":0}`
		},
	})
	// a window of three seconds, a short watch of a tenth of one, and a
	// provider slow past a tenth of one
	c.settle, c.saveGrain = 10*time.Millisecond, time.Millisecond
	seen, err := c.DeleteCollection(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}

	return seen, deletes, searches
}
