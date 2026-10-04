package tools

import (
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Jellyfin deletes the activity it logged more than its retention ago (30
// days out of the box), and the history tools read 60 days by default: they
// reached the end of what was kept and said complete, and a film played 40
// days ago was never played. Asked for nothing, the period is what the
// server keeps; asked for more, the answer is short of it and says so.
func TestTheHistoryToolsKnowWhatTheServerKeeps(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.jellyfin = true
	f.mux.HandleFunc("GET /Users", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{{"Id": "0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a01", "Name": "Quux", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}}})
	})
	f.mux.HandleFunc("GET /System/Configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"ActivityLogRetentionDays": 30})
	})
	var since []string
	f.mux.HandleFunc("GET /System/ActivityLog/Entries", func(w http.ResponseWriter, r *http.Request) {
		since = append(since, param(r.URL.Query(), "minDate"))
		writeJSON(t, w, page(map[string]any{"Name": "Quux has finished playing Zzyzx", "Type": "VideoPlaybackStopped", "Date": time.Now().UTC().Format(time.RFC3339), "ItemId": "9", "UserId": "0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a01"}))
	})
	f.mux.HandleFunc("GET /Items", itemsByID(t, map[string]string{"9": "Zzyzx"}))
	cs := session(t, f, Options{})

	readFrom := func() time.Time {
		t.Helper()
		at, err := time.Parse(time.RFC3339, since[len(since)-1])
		if err != nil {
			t.Fatalf("minDate %q: %v", since[len(since)-1], err)
		}

		return at
	}
	for _, tool := range []struct {
		name string
		args map[string]any
	}{{"user_history", map[string]any{}}, {"server_activity", map[string]any{"item": "9"}}, {"server_activity", map[string]any{}}} {
		// asked for nothing: the thirty days the server keeps, whole
		out := mustCall(t, cs, tool.name, tool.args)
		if number(t, out["days"], "days") != 30 || !boolean(t, out["complete"], "complete") || !strings.Contains(text(out["note"]), "the server keeps 30 days of activity") {
			t.Errorf("%s by default = %v", tool.name, out)
		}
		if at := readFrom(); time.Since(at) > 31*24*time.Hour || time.Since(at) < 29*24*time.Hour {
			t.Errorf("%s by default read from %v, want 30 days back", tool.name, at)
		}

		// asked for sixty: not complete, and why
		args := map[string]any{"days": 60}
		maps.Copy(args, tool.args)
		out = mustCall(t, cs, tool.name, args)
		if number(t, out["days"], "days") != 60 || boolean(t, out["complete"], "complete") || !strings.Contains(text(out["note"]), "of the 60 days asked for only the last 30 can be read") {
			t.Errorf("%s over 60 days = %v", tool.name, out)
		}

		// and within what is kept, whole
		args["days"] = 7
		if out = mustCall(t, cs, tool.name, args); number(t, out["days"], "days") != 7 || !boolean(t, out["complete"], "complete") || out["note"] != nil {
			t.Errorf("%s over 7 days = %v", tool.name, out)
		}
	}
}

// A play of an item since removed from the library was counted in total and
// left off the page with nothing to say why: a page of 25 held 24, and the
// one missing was the one a caller asking what was deleted wanted.
func TestUserHistorySaysWhatWasRemoved(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /System/ActivityLog/Entries", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(
			map[string]any{"Name": "Quux has finished playing Gone Film", "Type": "playback.stop", "Date": "2026-09-20T10:00:00Z", "ItemId": "7"},
			map[string]any{"Name": "Quux has finished playing Zzyzx", "Type": "playback.stop", "Date": "2026-09-19T10:00:00Z", "ItemId": "9"},
		))
	})
	f.mux.HandleFunc("GET /Items", itemsByID(t, map[string]string{"9": "Zzyzx"}))
	out := mustCall(t, session(t, f, Options{}), "user_history", map[string]any{})
	items := objects(t, out["items"], "items")
	if number(t, out["total"], "total") != 2 || len(items) != 2 {
		t.Fatalf("history = %v, want both plays", out)
	}
	if gone := items[0]; gone["id"] != "7" || !boolean(t, gone["removed"], "removed") || text(gone["log_line"]) != "Quux has finished playing Gone Film" {
		t.Errorf("the removed item's row = %v", gone)
	}
	if kept := items[1]; kept["id"] != "9" || kept["name"] != "Zzyzx" || kept["removed"] != nil || kept["log_line"] != nil {
		t.Errorf("the held item's row = %v", kept)
	}
}

// Plays are logged against the episode played, so a series' id is in no
// entry, and the history of a series answered no plays, complete. A series
// or a season is read as its episodes, and what holds items of another kind
// is refused rather than answered with nothing.
func TestServerActivityReadsWhatAnItemHolds(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = append(s.episodes, ep{season: 2, number: 1, name: "Hello, Ms. Cobel", path: "/media/shows/Severance/Season 02/S02E01.mkv"})
	f := tvServer(t, s)
	f.mux.HandleFunc("GET /System/ActivityLog/Entries", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(
			map[string]any{"Name": "Quux has finished playing Severance - S02E01", "Type": "playback.stop", "Date": "2026-09-21T10:00:00Z", "ItemId": "sev-2-1"},
			map[string]any{"Name": "Quux has finished playing Severance - S01E02", "Type": "playback.stop", "Date": "2026-09-20T10:00:00Z", "ItemId": "sev-1-2"},
			map[string]any{"Name": "Quux has finished playing Zzyzx", "Type": "playback.stop", "Date": "2026-09-19T10:00:00Z", "ItemId": "9"},
		))
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "server_activity", map[string]any{"item": "sev"})
	if got := objects(t, out["entries"], "entries"); len(got) != 2 || !strings.HasSuffix(text(got[0]["summary"]), "S02E01") || !strings.HasSuffix(text(got[1]["summary"]), "S01E02") || number(t, out["covers"], "covers") != 3 {
		t.Errorf("a series' history = %v, covering %v", got, out["covers"])
	}
	// an episode is itself
	if out := mustCall(t, cs, "server_activity", map[string]any{"item": "sev-1-2"}); len(objects(t, out["entries"], "entries")) != 1 || out["covers"] != nil {
		t.Errorf("an episode's history = %v", out)
	}
}

// An id the log names no play by, and that holds nothing whose plays it
// names, is refused: a collection answered no plays.
func TestServerActivityRefusesACollection(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(map[string]any{"Id": "c1", "Name": "Zzyzx Collection", "Type": "BoxSet"}))
	})
	msg := mustRefuse(t, session(t, f, Options{}), "server_activity", map[string]any{"item": "c1"})
	if !strings.Contains(msg, `c1 is a collection, "Zzyzx Collection"`) || !strings.Contains(msg, "a series, a season or an album") {
		t.Errorf("a collection = %q", msg)
	}
}
