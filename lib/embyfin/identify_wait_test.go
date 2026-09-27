package embyfin

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// Emby answers an apply before the refresh it starts has run. A film that
// already held the candidate's TMDB id reads as applied at once, with the
// IMDb id the refresh is about to correct still on it (seen live: Blade
// Runner holding Alien's IMDb id). The item's etag moves when the refresh
// saves it, so the read-back waits for that before answering.
func TestApplyWaitsForTheRefresh(t *testing.T) {
	t.Parallel()

	reads := 0
	c, f := newFake(t, Emby, map[string]route{
		"POST /Items/RemoteSearch/Apply/83": noContent,
		"GET /Items": func(*http.Request, string) (int, string) {
			reads++
			// the item as it was for the first few reads, then as the
			// refresh saved it
			if reads <= 4 {
				return http.StatusOK, `{"Items":[{"Id":"83","Name":"Blade Runner","Etag":"e1","ProviderIds":{"Tmdb":"78","Imdb":"tt0078748"}}],"TotalRecordCount":1}`
			}
			return http.StatusOK, `{"Items":[{"Id":"83","Name":"Blade Runner","Etag":"e2","ProviderIds":{"Tmdb":"78","Imdb":"tt0083658"}}],"TotalRecordCount":1}`
		},
	})
	c.settle, c.saveGrain = time.Millisecond, time.Millisecond
	it, err := c.ApplyRemoteSearchResult(t.Context(), "83", RemoteSearchResult{Name: "Blade Runner", ProviderIDs: map[string]string{"Tmdb": "78"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := it.ProviderIDs["Imdb"]; got != "tt0083658" {
		t.Errorf("the apply answered with IMDb id %s, want the refreshed tt0083658", got)
	}
	// the etag is asked for, or it could not be waited on
	for _, r := range f.all("GET /Items") {
		if !containsField(r.query.Get("Fields"), "Etag") {
			t.Errorf("a read-back asked for %q, without the etag", r.query.Get("Fields"))
			break
		}
	}
}

// A match the server has not been seen to apply when the wait runs out -
// the item still holding the ids it had, as it does while the refresh that
// applies it waits behind a scan - is not a refusal: it may still land. The
// error says it was sent and may still apply, not that it did not apply. One
// the server answered with other ids that held is a refusal.
func TestApplyNotSeenToLandMayStillApply(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		after   string // the ids every read after the first sees
		pending bool
	}{
		{"still as it was", `{"Tmdb":"1"}`, true},
		{"held other ids", `{"Tmdb":"2"}`, false},
	} {
		reads := 0
		c, _ := newFake(t, Emby, map[string]route{
			"POST /Items/RemoteSearch/Apply/83": noContent,
			"GET /Items": func(*http.Request, string) (int, string) {
				// the first read, before the apply, sees the ids it had
				reads++
				ids := `{"Tmdb":"1"}`
				if reads > 1 {
					ids = tc.after
				}
				return http.StatusOK, `{"Items":[{"Id":"83","Name":"Blade Runner","ProviderIds":` + ids + `}],"TotalRecordCount":1}`
			},
		})
		c.settle, c.saveGrain = time.Millisecond, time.Millisecond
		if tc.pending {
			_, err := c.ApplyRemoteSearchResult(t.Context(), "83", RemoteSearchResult{Name: "Blade Runner", ProviderIDs: map[string]string{"Tmdb": "78"}}, false)
			var pending *MatchPendingError
			if !errors.As(err, &pending) || !strings.Contains(err.Error(), "sent and not yet seen to land") || !strings.Contains(err.Error(), "may still apply") {
				t.Errorf("%s: err = %v, want the match said to be pending", tc.name, err)
			}
			continue
		}
		_, err := c.ApplyRemoteSearchResult(t.Context(), "83", RemoteSearchResult{Name: "Blade Runner", ProviderIDs: map[string]string{"Tmdb": "78"}}, false)
		var pending *MatchPendingError
		if err == nil || errors.As(err, &pending) {
			t.Errorf("%s: err = %v, want a refusal", tc.name, err)
		}
	}
}

// containsField reports whether a comma-separated Fields list names one.
func containsField(fields, want string) bool {
	return slices.Contains(list[string](fields), want)
}

// Only a match Emby has not yet reached is pending. The item saved by the
// refresh the apply started, and still holding the ids it had, is one the
// server ran and did not take; and Jellyfin runs that refresh before it
// answers the apply, so there a match not taken never lands later. Both are
// the refusal that says the server did not apply it, with Emby's advice
// about the nfo it reads.
func TestAMatchSavedOverOrOnJellyfinIsNotPending(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		backend Backend
		saves   bool
		nfo     bool
	}{
		{"Emby, saved over", Emby, true, true},
		{"Jellyfin, saved over", Jellyfin, true, false},
		{"Jellyfin, never saved", Jellyfin, false, false},
	} {
		applied := false
		c, _ := newFake(t, tc.backend, map[string]route{
			"POST /Items/RemoteSearch/Apply/83": func(*http.Request, string) (int, string) {
				applied = true
				return http.StatusNoContent, ""
			},
			"GET /Items": func(*http.Request, string) (int, string) {
				etag := "e1"
				if applied && tc.saves {
					etag = "e2"
				}
				return http.StatusOK, `{"Items":[{"Id":"83","Name":"Blade Runner","Etag":"` + etag + `","ProviderIds":{"Tmdb":"1","Imdb":"tt1"}}],"TotalRecordCount":1}`
			},
		})
		c.settle, c.saveGrain = time.Millisecond, time.Millisecond
		_, err := c.ApplyRemoteSearchResult(t.Context(), "83", RemoteSearchResult{Name: "Blade Runner", ProviderIDs: map[string]string{"Tmdb": "78", "Imdb": "tt0083658"}}, false)
		var pending *MatchPendingError
		if errors.As(err, &pending) || err == nil || !strings.Contains(err.Error(), "the server did not apply the candidate: the item's tmdb id is 1, not 78") {
			t.Errorf("%s: err = %v, want the refusal that the server did not apply it", tc.name, err)
			continue
		}
		if got := strings.Contains(err.Error(), "nfo"); got != tc.nfo {
			t.Errorf("%s: the refusal speaks of the nfo: %v, want %v: %v", tc.name, got, tc.nfo, err)
		}
	}
}
