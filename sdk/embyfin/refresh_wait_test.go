package embyfin

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Both servers answer a refresh once it is queued and run it a moment later,
// and an edit made in between was undone by it (seen live on both). The item
// is read back until the refresh has saved it - its etag moves - and it has
// held still, and the answer says whether that was seen.
func TestRefreshItemWaitsForTheSave(t *testing.T) {
	t.Parallel()

	for _, backend := range []Backend{Emby, Jellyfin} {
		// the refresh saves the item on the fourth read after it is asked
		// for, and fills its people a read later
		asked, reads := false, 0
		c, f := newFake(t, backend, map[string]route{
			"POST /Items/42/Refresh": func(*http.Request, string) (int, string) {
				asked = true
				return http.StatusNoContent, ""
			},
			"GET /Items": func(*http.Request, string) (int, string) {
				etag, people := "e1", ""
				if asked {
					reads++
				}
				if reads >= 4 {
					etag = "e2"
				}
				if reads >= 5 {
					people = `{"Name":"Zzyzx Actor","Type":"Actor"}`
				}
				return http.StatusOK, fmt.Sprintf(`{"Items":[{"Id":"42","Name":"Zzyzx","Etag":%q,"People":[%s]}],"TotalRecordCount":1}`, etag, people)
			},
		})
		c.settle, c.saveGrain = time.Millisecond, time.Millisecond
		landed, err := c.RefreshItem(t.Context(), "42", true)
		if err != nil || !landed {
			t.Fatalf("%s: landed %v, %v", backend, landed, err)
		}
		// answered once the people were in and had held, not at the first
		// read after the ask, nor at the save
		if reads < 5+4 {
			t.Errorf("%s: answered after %d reads, want the save and the people to have held", backend, reads)
		}
		if n := len(f.all("POST /Items/42/Refresh")); n != 1 {
			t.Errorf("%s: %d refreshes asked for", backend, n)
		}
	}

	// a refresh never seen to save the item is said to be so
	c, _ := newFake(t, Jellyfin, map[string]route{
		"POST /Items/42/Refresh": noContent,
		"GET /Items":             ok(`{"Items":[{"Id":"42","Name":"Zzyzx","Etag":"e1"}],"TotalRecordCount":1}`),
	})
	c.settle, c.saveGrain = time.Millisecond, time.Millisecond
	if landed, err := c.RefreshItem(t.Context(), "42", false); err != nil || landed {
		t.Errorf("a refresh that never saved: landed %v, %v", landed, err)
	}
}

// Emby keeps the time an item was saved to the second, and its etag follows
// it, so a refresh that saves in the same second as the change before it
// leaves the etag as it was (seen live: a refresh straight after an edit
// refilled the people and the etag held). On Emby the refresh is asked for
// only once the item has gone a grain unsaved; Jellyfin's etag moves on every
// save, and there it is asked for at once.
func TestRefreshItemWaitsOutEmbysSecond(t *testing.T) {
	t.Parallel()

	for _, backend := range []Backend{Emby, Jellyfin} {
		var first, posted time.Time
		c, _ := newFake(t, backend, map[string]route{
			"POST /Items/42/Refresh": func(*http.Request, string) (int, string) {
				posted = time.Now()
				return http.StatusNoContent, ""
			},
			"GET /Items": func(*http.Request, string) (int, string) {
				if first.IsZero() {
					first = time.Now()
				}
				etag := "e1"
				if !posted.IsZero() {
					etag = "e2"
				}
				return http.StatusOK, `{"Items":[{"Id":"42","Name":"Zzyzx","Etag":"` + etag + `"}],"TotalRecordCount":1}`
			},
		})
		c.settle, c.saveGrain = time.Millisecond, 50*time.Millisecond
		if landed, err := c.RefreshItem(t.Context(), "42", false); err != nil || !landed {
			t.Fatalf("%s: landed %v, %v", backend, landed, err)
		}
		waited := posted.Sub(first)
		if backend == Emby && waited < c.saveGrain {
			t.Errorf("Emby: the refresh was asked for %v after the first read, want a grain (%v) unsaved first", waited, c.saveGrain)
		}
		if backend == Jellyfin && waited >= c.saveGrain {
			t.Errorf("Jellyfin: the refresh was asked for %v after the first read, want at once", waited)
		}
	}
}
