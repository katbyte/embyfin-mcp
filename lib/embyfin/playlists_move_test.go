package embyfin

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// A Jellyfin move takes the entries from the lower of the two positions to
// the end out and puts them back in the new order. Taken out and not put
// back, they are gone from the playlist: the error names each of them, in
// the order to add them again, rather than only that the add failed.
func TestAJellyfinMoveThatLosesEntriesNamesThem(t *testing.T) {
	t.Parallel()

	held := []string{"a", "b", "c"}
	names := map[string]string{"a": "Alien", "b": "Blade Runner", "c": "Cube"}
	c, _ := newFake(t, Jellyfin, map[string]route{
		"GET /Playlists/p1/Items": func(*http.Request, string) (int, string) {
			rows := make([]string, 0, len(held))
			for _, id := range held {
				rows = append(rows, `{"Id":"`+id+`","Name":"`+names[id]+`","PlaylistItemId":"`+id+`"}`)
			}
			return http.StatusOK, `{"Items":[` + strings.Join(rows, ",") + `],"TotalRecordCount":3}`
		},
		"DELETE /Playlists/p1/Items": func(r *http.Request, _ string) (int, string) {
			gone := r.URL.Query()["entryIds"]
			held = slices.DeleteFunc(held, func(id string) bool { return slices.Contains(gone, id) })
			return http.StatusNoContent, ""
		},
		"POST /Playlists/p1/Items": answer(http.StatusInternalServerError, "the playlist file is locked"),
	})
	c.settle, c.saveGrain = time.Millisecond, time.Millisecond

	err := c.MovePlaylistEntry(t.Context(), "p1", "u1", "c", "c", "", 1)
	if err == nil || !strings.Contains(err.Error(), "putting them back failed, so they are gone from the playlist until added again (playlist_edit add_items, in this order): Cube (c), Blade Runner (b)") {
		t.Errorf("a move whose put-back failed = %v", err)
	}
	if !slices.Equal(held, []string{"a"}) {
		t.Fatalf("the playlist holds %v; the test's server should have lost b and c", held)
	}
}
