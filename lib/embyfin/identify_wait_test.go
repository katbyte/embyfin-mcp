package embyfin

import (
	"net/http"
	"slices"
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

// containsField reports whether a comma-separated Fields list names one.
func containsField(fields, want string) bool {
	return slices.Contains(list[string](fields), want)
}
