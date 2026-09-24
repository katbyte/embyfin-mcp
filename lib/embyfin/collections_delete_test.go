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
	// the reads since the last delete, deletes how many deletes there were
	server := func(t *testing.T, back func(deletes, reads int) bool) (*Client, *fake) {
		t.Helper()

		deletes, reads := 0, 0
		c, f := newFake(t, Jellyfin, map[string]route{
			"DELETE /Items/c1": func(*http.Request, string) (int, string) {
				deletes++
				reads = 0
				return http.StatusNoContent, ""
			},
			"GET /Items": func(*http.Request, string) (int, string) {
				reads++
				if back(deletes, reads) {
					return http.StatusOK, `{"Items":[{"Id":"c1","Name":"Zzyzx Set","Type":"BoxSet"}],"TotalRecordCount":1}`
				}
				return http.StatusOK, `{"Items":[],"TotalRecordCount":0}`
			},
		})
		c.settle = time.Millisecond

		return c, f
	}

	t.Run("gone at once", func(t *testing.T) {
		t.Parallel()
		c, f := server(t, func(int, int) bool { return false })
		if err := c.DeleteCollection(t.Context(), "c1"); err != nil {
			t.Fatal(err)
		}
		if n := len(f.all("DELETE /Items/c1")); n != 1 {
			t.Errorf("%d deletes, want 1", n)
		}
		// and it was looked for after, not taken on the 204's word
		if n := len(f.all("GET /Items")); n < 2 {
			t.Errorf("read back %d times, want it seen to stay gone", n)
		}
	})

	t.Run("back once", func(t *testing.T) {
		t.Parallel()
		// gone on the first read after the first delete, back on the second
		c, f := server(t, func(deletes, reads int) bool { return deletes == 1 && reads >= 2 })
		if err := c.DeleteCollection(t.Context(), "c1"); err != nil {
			t.Fatal(err)
		}
		if n := len(f.all("DELETE /Items/c1")); n != 2 {
			t.Errorf("%d deletes, want the second when it came back", n)
		}
	})

	t.Run("back every time", func(t *testing.T) {
		t.Parallel()
		c, f := server(t, func(_, reads int) bool { return reads >= 2 })
		err := c.DeleteCollection(t.Context(), "c1")
		if err == nil || !strings.Contains(err.Error(), "came back") {
			t.Fatalf("err = %v, want one saying it came back", err)
		}
		if n := len(f.all("DELETE /Items/c1")); n != 3 {
			t.Errorf("%d deletes, want 3 tries", n)
		}
	})
}
