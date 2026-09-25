//go:build integration

package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
)

// Two films part way and two series started, their first episodes marked
// watched last. Emby's resume list puts the next episode of each series
// first, at position zero, never started; user_next_up drops those, and used
// to after asking Emby for the limit, so the films part way through were
// never reached: in_progress came back empty with two films in progress. The
// limit is now applied to what is left.
func TestInProgressPastTheNextEpisodes(t *testing.T) {
	alien := findItem(t, "Movies", "Movie", "Alien")
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	var pilots []string
	for _, show := range []string{"Breaking Bad", "Severance"} {
		series := findItem(t, "Shows", "Series", show)
		pilots = append(pilots, str(rows(t, call(t, "library_episodes", map[string]any{"series_id": series, "season": 1})["episodes"], "episodes")[0]["id"]))
	}
	t.Cleanup(func() {
		for _, id := range append([]string{alien, arrival}, pilots...) {
			_, _ = invoke("item_set_state", map[string]any{"id": id, "user": "alice", "watched": false})
		}
	})
	for _, film := range []string{alien, arrival} {
		call(t, "item_set_state", map[string]any{"id": film, "user": "alice", "position_s": 2550})
	}
	for _, pilot := range pilots {
		call(t, "item_set_state", map[string]any{"id": pilot, "user": "alice", "watched": true})
	}

	if !isJellyfin() {
		// what Emby answers when asked for two: the next episodes, which a
		// limit asked of the server fills
		alice := os.Getenv("EMBYFIN_TEST_USER_ID")
		status, raw := api(t, http.MethodGet, "/Users/"+alice+"/Items/Resume?Recursive=true&MediaTypes=Video&EnableUserData=true&Limit=2", "", nil)
		var page struct {
			Items []struct {
				Type     string
				UserData struct{ PlaybackPositionTicks int64 }
			}
		}
		if status != http.StatusOK || json.Unmarshal(raw, &page) != nil {
			t.Fatalf("Emby's resume list: HTTP %d: %.200s", status, raw)
		}
		started := 0
		for _, it := range page.Items {
			if it.UserData.PlaybackPositionTicks > 0 {
				started++
			}
		}
		if len(page.Items) != 2 || started != 0 {
			t.Fatalf("Emby's first two resume rows = %s, want the two next episodes, never started: the case this test is for", raw)
		}
	}

	// Emby keeps watch state by title, so the messy copies of the two films
	// are part way through as well: any of them fills a row, and nothing else
	films := []string{"Alien", "Arrival"}
	for _, limit := range []int{1, 2, 10} {
		got := names(t, call(t, "user_next_up", map[string]any{"user": "alice", "limit": limit})["in_progress"], "in_progress")
		if limit <= 2 && len(got) != limit {
			t.Errorf("limit %d in_progress = %v, want %d rows", limit, got, limit)
		}
		if slices.ContainsFunc(got, func(n string) bool { return !slices.Contains(films, n) }) {
			t.Errorf("limit %d in_progress = %v, want only %v", limit, got, films)
		}
		if limit == 10 && (!slices.Contains(got, "Alien") || !slices.Contains(got, "Arrival")) {
			t.Errorf("limit 10 in_progress = %v, want both films", got)
		}
	}
	next := names(t, call(t, "user_next_up", map[string]any{"user": "alice", "limit": 2})["next_up"], "next_up")
	if len(next) != 2 {
		t.Errorf("next_up = %v, want the second episode of each series", next)
	}
}

// audit_unwatched promises what no account has watched. alice watched
// Arrival, then lost access to the film library: her own view no longer
// holds it on either server, and her watch still counts.
func TestUnwatchedAfterLosingAccess(t *testing.T) {
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	t.Cleanup(func() {
		_, _ = invoke("item_set_state", map[string]any{"id": arrival, "user": "alice", "watched": false})
	})
	unwatched := func() []string {
		t.Helper()
		return findings(t, call(t, "audit_unwatched", map[string]any{"library": "Movies"}))
	}
	if !slices.Contains(unwatched(), "Arrival") {
		t.Fatalf("Arrival is watched before alice watches it: %v", unwatched())
	}
	call(t, "item_set_state", map[string]any{"id": arrival, "user": "alice", "watched": true})
	if slices.Contains(unwatched(), "Arrival") {
		t.Fatalf("alice's watch of Arrival does not count while she can see it: %v", unwatched())
	}

	restrictAlice(t, "Shows")
	if got := unwatched(); slices.Contains(got, "Arrival") {
		t.Errorf("audit_unwatched = %v: alice's watch of Arrival stopped counting when she lost the library", got)
	}
	// and item_last_watched says the same: her watch, marked as made by an
	// account that has lost the library since
	var row map[string]any
	for _, u := range rows(t, call(t, "item_last_watched", map[string]any{"id": arrival})["users"], "users") {
		if str(u["user"]) == "alice" {
			row = u
		}
	}
	if row == nil || !boolOf(row["played"]) || !boolOf(row["no_access"]) || num(t, row["play_count"], "play_count") < 1 || str(row["last_played"]) == "" {
		t.Errorf("item_last_watched for alice after she lost the library = %v, want her watch, marked no_access", row)
	}
	if got := call(t, "user_stats", map[string]any{"user": "alice"}); num(t, got["movies_watched"], "movies_watched") != 0 {
		t.Errorf("user_stats alice = %v: in her own view she has watched no film she can see", got)
	}
}

// An id no item has is an error on both servers. Both answer a read in a
// user's view with a 404 for it, as they do for an item the user may not
// see, and item_last_watched read it as that: {item:"", users:[]}, nobody
// has watched this.
func TestAnIDNoItemHas(t *testing.T) {
	unknown := "99999999"
	if isJellyfin() {
		unknown = "0123456789abcdef0123456789abcdef"
	}
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"item_last_watched", map[string]any{"id": unknown}},
		{"item_get", map[string]any{"id": unknown}},
		{"item_set_state", map[string]any{"id": unknown, "user": "alice", "watched": true}},
	} {
		if msg := callErr(t, tc.tool, tc.args); !strings.Contains(msg, fmt.Sprintf("no item with id %s", unknown)) {
			t.Errorf("%s of an id nothing has = %q", tc.tool, msg)
		}
	}
	// and an item a user may not see is still one nobody is said to have
	// watched in her name
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	restrictAlice(t, "Shows")
	out := call(t, "item_last_watched", map[string]any{"id": arrival})
	if title(str(out["item"])) != "Arrival" {
		t.Errorf("item_last_watched = %v", out)
	}
	for _, u := range rows(t, out["users"], "users") {
		if str(u["user"]) == "alice" {
			t.Errorf("alice is reported on a film she may not see: %v", u)
		}
	}
}
