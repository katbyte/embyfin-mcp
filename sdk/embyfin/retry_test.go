package embyfin

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	apiclient "github.com/katbyte/pandorest/client"
)

// Both servers' clients send a read again after a gateway or busy answer,
// four times in all, and never a write or a delete: a sweep of a large
// library behind a reverse proxy failed whole on one 502.
func TestReadsAreRetriedAndWritesAreNot(t *testing.T) {
	t.Parallel()

	if readRetry.Tries != 4 || readRetry.Wait(0) != 2*time.Second || readRetry.Wait(2) != 8*time.Second {
		t.Errorf("the read retry = %d tries, waiting %v then %v", readRetry.Tries, readRetry.Wait(0), readRetry.Wait(2))
	}
	// failing answers first, then the real one, per route
	flaky := func(fails int, status int, body string) route {
		var mu sync.Mutex
		seen := 0
		return func(*http.Request, string) (int, string) {
			mu.Lock()
			defer mu.Unlock()
			seen++
			if seen <= fails {
				return http.StatusBadGateway, "Bad Gateway"
			}
			return status, body
		}
	}
	const deletePath = "DELETE /Items/1"
	for _, backend := range []Backend{Emby, Jellyfin} {
		c, f := newFake(t, backend, map[string]route{
			"GET /Items": flaky(2, http.StatusOK, `{"Items":[{"Id":"1","Name":"Alien","Type":"Movie"}],"TotalRecordCount":1}`),
			deletePath:   flaky(1, http.StatusNoContent, ""),
		})
		items, _, err := c.Search(t.Context(), SearchOptions{IDs: "1"})
		if err != nil || len(items) != 1 || len(f.all("GET /Items")) != 3 {
			t.Errorf("%s: a read after two gateway answers = %v, %v, after %d requests", backend, items, err, len(f.all("GET /Items")))
		}
		err = c.DeleteItem(t.Context(), "1")
		if apiclient.StatusCode(err) != http.StatusBadGateway || len(f.all(deletePath)) != 1 || strings.Contains(err.Error(), "tried") {
			t.Errorf("%s: a delete answered 502 = %v after %d requests, want it sent once", backend, err, len(f.all(deletePath)))
		}
	}
}
