//go:build integration

package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// What the read tools could not know, said rather than answered as if known:
// a file on the disk that no scan has reached, a film started and not
// finished, the plays of a series, the activity a server no longer keeps,
// and ids an audit cannot use.

// A file a previous batch wrote, or any the server has not scanned yet, is
// on the disk and in no item. plan_check asked the library alone and read
// its path as free, and the next write replaced it: the disk is read too.
func TestPlanCheckSeesWhatNoScanHasReached(t *testing.T) {
	if dataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	episode := "shows/Severance/Season 01/Severance S01E05.mp4"
	stageFile(t, filepath.Join(dataDir(), episode), fixture(t, "shows/Severance/Season 01/Severance S01E01.mp4"))
	film := "movies/Arrival (2016)/Arrival (2016) - 1080p.mp4"
	stageFile(t, filepath.Join(dataDir(), film), fixture(t, "movies/Arrival (2016)/Arrival (2016).mp4"))

	out := call(t, "plan_check", map[string]any{"entries": []map[string]any{
		{"path": "/media/" + episode},
		{"path": "/media/" + film},
		{"path": "/media/shows/Severance/Season 01/Severance S01E06.mp4"},
		{"path": "/media/shows/Severance/Season 01/Severance S01E01.mp4"},
	}})
	entries := rows(t, out["entries"], "entries")
	if len(entries) != 4 {
		t.Fatalf("entries = %v", entries)
	}
	// written into a season and never scanned: replaced all the same
	if e := entries[0]; !isBool(e["exists"], true) || !isBool(e["in_library"], false) || !isBool(e["on_disk"], true) || e["current"] != nil || !strings.Contains(str(e["note"]), "no item holds") {
		t.Errorf("an episode no scan has reached = %v", e)
	}
	// a second file beside a film: Emby asks its library by the path and
	// holds nothing there; Jellyfin cannot ask, and the disk alone answers
	e := entries[1]
	if !isBool(e["exists"], true) || !isBool(e["on_disk"], true) {
		t.Errorf("a film's file no scan has reached = %v", e)
	}
	if isJellyfin() && (e["in_library"] != nil || !isBool(e["checked"], false)) || !isJellyfin() && !isBool(e["in_library"], false) {
		t.Errorf("a film's file no scan has reached: checked %v, in_library %v", e["checked"], e["in_library"])
	}
	if e := entries[2]; !isBool(e["exists"], false) || !isBool(e["on_disk"], false) || !isBool(e["in_library"], false) {
		t.Errorf("a free path = %v", e)
	}
	if e := entries[3]; !isBool(e["exists"], true) || !isBool(e["on_disk"], true) || !isBool(e["in_library"], true) || e["current"] == nil {
		t.Errorf("a file the library holds = %v", e)
	}
	notScanned := 2
	if isJellyfin() {
		notScanned = 1
	}
	if num(t, out["existing"], "existing") != 3 || num(t, out["not_scanned"], "not_scanned") != notScanned {
		t.Errorf("existing %v, not_scanned %v, want 3 and %d", out["existing"], out["not_scanned"], notScanned)
	}

	// a name differing only in case: the same file on a disk that ignores
	// case, which the host's own disk says (the server reads it through the
	// same mount), and another on one that does not
	cased := "shows/Severance/Season 01/severance s01e01.mp4"
	_, err := os.Stat(filepath.Join(dataDir(), cased))
	ignores := err == nil
	row := rows(t, call(t, "plan_check", map[string]any{"entries": []map[string]any{{"path": "/media/" + cased}}})["entries"], "entries")[0]
	switch {
	case ignores && (!isBool(row["exists"], true) || !isBool(row["in_library"], true) || !strings.Contains(str(row["note"]), "ignores case")):
		t.Errorf("on a disk that ignores case, the held file spelled otherwise = %v", row)
	case !ignores && (!isBool(row["exists"], false) || !strings.Contains(str(row["note"]), "differing only in case")):
		t.Errorf("on a disk that tells case apart, a path differing only in case = %v", row)
	}
}

// audit_unwatched read only what was played to the end, so a film stopped
// part way was "never watched" beside those nobody opened, on the list of
// what to archive or delete. Limitless runs its 106 minutes, long enough for
// both servers to keep a resume point.
func TestUnwatchedListsWhatIsStarted(t *testing.T) {
	film := findItem(t, "Movies", "Movie", "Limitless")
	putBack(t, "item_set_state", map[string]any{"id": film, "user": "alice", "watched": false})
	_, token := signInPlayer(t)
	where := func() (finding, started map[string]any) {
		out := call(t, "audit_unwatched", map[string]any{"library": "Movies"})
		for _, f := range rows(t, out["findings"], "findings") {
			if str(f["id"]) == film {
				finding = f
			}
		}
		for _, s := range rows(t, out["started"], "started") {
			if str(s["id"]) == film {
				started = s
			}
		}

		return finding, started
	}
	if f, s := where(); f == nil || s != nil {
		t.Fatalf("before any play, Limitless = %v, started %v: want it never watched", f, s)
	}

	playTo(t, token, film, 10*60*10_000_000) // ten minutes in
	var f, s map[string]any
	if !eventually(func() bool { f, s = where(); return s != nil }) {
		t.Fatalf("stopped ten minutes in, Limitless = %v: want it started", f)
	}
	if f != nil || !strings.HasPrefix(str(s["detail"]), "started and never finished, by alice (9%); added ") {
		t.Errorf("stopped ten minutes in, Limitless = %v, started %v", f, s)
	}

	// marked unwatched, it is never watched again
	call(t, "item_set_state", map[string]any{"id": film, "user": "alice", "watched": false})
	if !eventually(func() bool { f, s = where(); return f != nil && s == nil }) {
		t.Fatalf("marked unwatched, Limitless = %v, started %v", f, s)
	}

	// begun and stopped a minute in, below where either server keeps a
	// resume point: a play counted and dated, and no position
	playTo(t, token, film, 60*10_000_000)
	if !eventually(func() bool { f, s = where(); return s != nil }) {
		t.Fatalf("stopped a minute in, Limitless = %v: want it started", f)
	}
	if f != nil || !strings.HasPrefix(str(s["detail"]), "started and never finished, by alice; added ") {
		t.Errorf("stopped a minute in, Limitless = %v, started %v", f, s)
	}

	// the order that read stops early by: an account's unplayed films by when
	// it last played them, newest first, every one begun ahead of every one
	// never begun
	alice := os.Getenv("EMBYFIN_TEST_USER_ID")
	status, raw := api(t, http.MethodGet, "/Items?Recursive=true&IncludeItemTypes=Movie&Filters=IsUnplayed&SortBy=DatePlayed,SortName&SortOrder=Descending&EnableUserData=true&Fields=UserDataPlayCount,UserDataLastPlayedDate&Limit=100&UserId="+alice, "", nil)
	var page struct {
		Items []struct {
			ID       string `json:"Id"`
			UserData struct {
				PlayCount      int
				LastPlayedDate string
			}
		}
	}
	if status != http.StatusOK || json.Unmarshal(raw, &page) != nil {
		t.Fatalf("alice's unplayed films by last played: HTTP %d: %.200s", status, raw)
	}
	ended, seen := false, false
	for _, it := range page.Items {
		begun := it.UserData.PlayCount > 0 || it.UserData.LastPlayedDate != ""
		switch {
		case begun && ended:
			t.Errorf("a film begun (%s) comes after one never begun: the server did not sort by last played", it.ID)
		case !begun:
			ended = true
		case it.ID == film:
			seen = true
		}
	}
	if !seen {
		t.Errorf("Limitless is not among the begun films at the head of alice's unplayed ones: %.300s", raw)
	}
}

// An account whose view hides part of a library it can see - here, items
// with a tag blocked - is read as it sees the library, and neither server
// lists what the block hides: what it played there is not counted, and the
// answer names the account and the block rather than reading as complete.
func TestUnwatchedNamesAViewWithABlock(t *testing.T) {
	const tag = "zzyzx-blocked"
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	// every copy of the film, the messy library's too: Emby keeps watch
	// state by provider id, so a watch marks each copy, and a copy the block
	// does not hide would be read as her watch (seen on 4.10)
	var copies []any
	for _, it := range rows(t, call(t, "library_items", map[string]any{"query": "Arrival", "types": "Movie", "limit": 50})["items"], "items") {
		if str(it["name"]) == "Arrival" {
			copies = append(copies, str(it["id"]))
		}
	}
	if len(copies) < 2 {
		t.Fatalf("Arrival's copies = %v, want the clean library's and the messy one's", copies)
	}
	call(t, "item_edit", map[string]any{"ids": copies, "add_tags": []any{tag}})
	putBack(t, "item_edit", map[string]any{"ids": copies, "remove_tags": []any{tag}})
	call(t, "item_set_state", map[string]any{"id": arrival, "user": "alice", "watched": true})
	putBack(t, "item_set_state", map[string]any{"id": arrival, "user": "alice", "watched": false})

	unwatched := func() ([]string, []string) {
		out := call(t, "audit_unwatched", map[string]any{"library": "Movies"})

		return findings(t, out), strs(t, orEmptyList(out["views_limited"]), "views_limited")
	}
	if got, limited := unwatched(); slices.Contains(got, "Arrival") || len(limited) != 0 {
		t.Fatalf("before the block: unwatched %v, views_limited %v: want Arrival watched and no view limited", got, limited)
	}

	// block the tag for alice, straight through the API: no tool changes an
	// account's access
	alice := os.Getenv("EMBYFIN_TEST_USER_ID")
	status, raw := api(t, http.MethodGet, "/Users/"+alice, "", nil)
	var user struct{ Policy map[string]any }
	if status != http.StatusOK || json.Unmarshal(raw, &user) != nil || user.Policy == nil {
		t.Fatalf("reading alice: HTTP %d: %.200s", status, raw)
	}
	original, err := json.Marshal(user.Policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var policy map[string]any
		if err := json.Unmarshal(original, &policy); err != nil {
			t.Error(err)

			return
		}
		if status, raw := api(t, http.MethodPost, "/Users/"+alice+"/Policy", "", policy); status/100 != 2 {
			t.Errorf("restoring alice's policy: HTTP %d: %s", status, raw)
		}
	})
	user.Policy["BlockedTags"] = []string{tag}
	if status, raw := api(t, http.MethodPost, "/Users/"+alice+"/Policy", "", user.Policy); status/100 != 2 {
		t.Fatalf("blocking the tag for alice: HTTP %d: %s", status, raw)
	}

	got, limited := unwatched()
	if !slices.Equal(limited, []string{"alice: items tagged " + tag + " blocked"}) {
		t.Errorf("views_limited = %v, want alice's block named", limited)
	}
	// what the block hides is not read: her watch of Arrival is not seen
	if !slices.Contains(got, "Arrival") {
		t.Errorf("unwatched = %v: alice's watch of Arrival was read through her block, which the description says it is not", got)
	}

	// Emby's one tag list turns about with IsTagBlockingModeInclusive: the
	// tagged items are then the only ones she sees (4.10 shows her the two
	// copies of Arrival alone), and the limit is named for what it is
	if isJellyfin() {
		return
	}
	user.Policy["IsTagBlockingModeInclusive"] = true
	if status, raw := api(t, http.MethodPost, "/Users/"+alice+"/Policy", "", user.Policy); status/100 != 2 {
		t.Fatalf("showing alice only the tag: HTTP %d: %s", status, raw)
	}
	if _, limited := unwatched(); !slices.Equal(limited, []string{"alice: only items tagged " + tag + " shown"}) {
		t.Errorf("views_limited = %v, want alice's view named as only the tag", limited)
	}
}

// A season mostly of files a second long beside a few whole episodes: the
// median of them all was the short files', and the whole episodes were the
// ones reported. Split between two lengths like this, the season is one
// finding naming both and which episodes run each, and neither set is judged
// by the other.
func TestAuditRuntimeSeasonMostlyShort(t *testing.T) {
	src := "messy-shows/hack Liminality (2002)/Season 01/"
	long, short := fixture(t, src+"hack Liminality S01E01.mp4"), fixture(t, src+"hack Liminality S01E03.mp4")
	g := "messy-shows/hack Liminality (2002)/Season 03/"
	files := map[string][]byte{}
	for n, raw := range [][]byte{long, short, short, long, short, short, long} {
		files[g+fmt.Sprintf("hack Liminality S03E%02d.mp4", n+1)] = raw
	}
	stage(t, plus(0, 0, len(files)), files, g)

	var got []map[string]any
	for _, f := range rows(t, call(t, "audit_runtime", map[string]any{"library": "Messy Shows"})["findings"], "findings") {
		if strings.Contains(str(f["path"]), "/hack Liminality (2002)/Season 03") {
			got = append(got, f)
		}
	}
	want := "the season is split between two lengths: 4 files run about 0 min (E02, E03, E05, E06) and 3 about 3 min (E01, E04, E07). One set is not what the other is - cut files or previews, double episodes, or another show's - so neither is judged by the other: compare the files"
	if len(got) != 1 || str(got[0]["name"]) != ".hack//Liminality season 3" || str(got[0]["detail"]) != want || !strings.HasSuffix(str(got[0]["path"]), "/hack Liminality (2002)/Season 03") {
		t.Errorf("season three's findings = %v, want the season split, naming both sets", got)
	}
}

// A parental rating limit of 0 is a limit - on Jellyfin the strictest there
// is - and the generated models read it as none: user_get showed no limit
// and views_limited left the account out. What the tools say is what the
// server stored, however it stores it.
func TestARatingLimitOfZeroIsALimit(t *testing.T) {
	alice := os.Getenv("EMBYFIN_TEST_USER_ID")
	status, raw := api(t, http.MethodGet, "/Users/"+alice, "", nil)
	var user struct{ Policy map[string]any }
	if status != http.StatusOK || json.Unmarshal(raw, &user) != nil || user.Policy == nil {
		t.Fatalf("reading alice: HTTP %d: %.200s", status, raw)
	}
	original, err := json.Marshal(user.Policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var policy map[string]any
		if err := json.Unmarshal(original, &policy); err != nil {
			t.Error(err)

			return
		}
		if st, body := api(t, http.MethodPost, "/Users/"+alice+"/Policy", "", policy); st/100 != 2 {
			t.Errorf("restoring alice's policy: HTTP %d: %s", st, body)
		}
	})
	user.Policy["MaxParentalRating"] = 0
	if st, body := api(t, http.MethodPost, "/Users/"+alice+"/Policy", "", user.Policy); st/100 != 2 {
		t.Fatalf("limiting alice at 0: HTTP %d: %s", st, body)
	}

	// what the server stored, read back as it wrote it
	status, raw = api(t, http.MethodGet, "/Users/"+alice, "", nil)
	var stored struct {
		Policy struct{ MaxParentalRating *float64 }
	}
	if status != http.StatusOK || json.Unmarshal(raw, &stored) != nil {
		t.Fatalf("reading alice back: HTTP %d: %.200s", status, raw)
	}
	if isJellyfin() && (stored.Policy.MaxParentalRating == nil || *stored.Policy.MaxParentalRating != 0) {
		t.Fatalf("Jellyfin stored alice's limit as %v, want 0", stored.Policy.MaxParentalRating)
	}
	limit := call(t, "user_get", map[string]any{"user": "alice"})["max_parental_rating"]
	limited := strs(t, orEmptyList(call(t, "audit_unwatched", map[string]any{"library": "Movies"})["views_limited"]), "views_limited")
	switch stored.Policy.MaxParentalRating {
	case nil:
		// a server that keeps no limit of 0 says so, and so do the tools
		if limit != nil || len(limited) != 0 {
			t.Errorf("the server stored no limit, yet user_get says %v and views_limited %v", limit, limited)
		}
	default:
		if n, ok := limit.(float64); !ok || n != *stored.Policy.MaxParentalRating {
			t.Errorf("user_get max_parental_rating = %v, want the stored %v", limit, *stored.Policy.MaxParentalRating)
		}
		if !slices.Equal(limited, []string{"alice: a parental rating limit"}) {
			t.Errorf("views_limited = %v, want alice's limit named", limited)
		}
	}
}

// isBool says whether a decoded field holds exactly the boolean want: an
// absent field, or one of another type, is neither true nor false.
func isBool(v any, want bool) bool {
	b, ok := v.(bool)

	return ok && b == want
}

// orEmptyList is a JSON list, or an empty one when the field is absent.
func orEmptyList(v any) any {
	if v == nil {
		return []any{}
	}

	return v
}

// Plays are logged against the episode played, so item_watch_history on a
// series answered no plays, complete; and Jellyfin deletes the activity it
// logged more than its retention ago (30 days out of the box), which a read
// of 60 days reached the end of and called complete.
func TestHistoryOfASeriesAndWhatTheServerKeeps(t *testing.T) {
	sev := findItem(t, "Shows", "Series", "Severance")
	var first string
	episodes := rows(t, call(t, "library_episodes", map[string]any{"series_id": sev})["episodes"], "episodes")
	for _, e := range episodes {
		if num(t, e["season"], "season") == 1 && num(t, e["episode"], "episode") == 1 {
			first = str(e["id"])
		}
	}
	if first == "" {
		t.Fatal("Severance holds no S01E01")
	}
	putBack(t, "item_set_state", map[string]any{"id": first, "user": "alice", "watched": false})
	_, token := signInPlayer(t)
	playThrough(t, token, first)

	mentions := func(id string) (map[string]any, bool) {
		out := call(t, "item_watch_history", map[string]any{"id": id, "days": 7})

		return out, slices.ContainsFunc(strs(t, out["entries"], "entries"), func(e string) bool { return strings.Contains(e, "alice") })
	}
	var out map[string]any
	if !eventually(func() bool { var ok bool; out, ok = mentions(sev); return ok }) {
		t.Fatalf("the series' history = %v, want alice's play of its first episode", out)
	}
	if num(t, out["covers"], "covers") != len(episodes) {
		t.Errorf("the series' history covers %v, want its %d episodes", out["covers"], len(episodes))
	}
	var season string
	for _, s := range rows(t, call(t, "show_seasons", map[string]any{"series_id": sev})["seasons"], "seasons") {
		if num(t, s["season"], "season") == 1 {
			season = str(s["id"])
		}
	}
	if seasonOut, ok := mentions(season); !ok {
		t.Errorf("season one's history = %v, want alice's play of its first episode", seasonOut)
	}
	if episodeOut, ok := mentions(first); !ok || episodeOut["covers"] != nil {
		t.Errorf("the episode's own history = %v", episodeOut)
	}

	// the period read by default: what Jellyfin keeps, or Emby's 60 days
	out = call(t, "user_history", map[string]any{"user": "alice"})
	switch {
	case isJellyfin() && (num(t, out["days"], "days") != 30 || !isBool(out["complete"], true) || !strings.Contains(str(out["note"]), "the server keeps 30 days of activity")):
		t.Errorf("Jellyfin's default history = days %v, complete %v, note %q: want the 30 days it keeps", out["days"], out["complete"], out["note"])
	case !isJellyfin() && (num(t, out["days"], "days") != 60 || !isBool(out["complete"], true) || out["note"] != nil):
		t.Errorf("Emby's default history = days %v, complete %v, note %q: want 60 days, whole", out["days"], out["complete"], out["note"])
	}
	// and sixty asked for is more than Jellyfin keeps
	out = call(t, "item_watch_history", map[string]any{"id": first, "days": 60})
	if complete := isBool(out["complete"], true); complete == isJellyfin() {
		t.Errorf("sixty days of history = complete %v, note %q: want it short of the period on Jellyfin alone", out["complete"], out["note"])
	}

	// a library holds no plays of its own and is refused, not answered none
	var library string
	for _, l := range rows(t, call(t, "library_list", nil)["libraries"], "libraries") {
		if str(l["name"]) == "Movies" {
			library = str(l["id"])
		}
	}
	if msg := callErr(t, "item_watch_history", map[string]any{"id": library}); !strings.Contains(msg, "is a library") || !strings.Contains(msg, "a series, a season or an album") {
		t.Errorf("a library's history = %s", msg)
	}
}

// The ids an audit is narrowed to are read before it runs: Emby drops an Ids
// filter it cannot parse and answers with the whole library, and an id no
// item has, or a series' id where films are checked, came back as nothing
// scanned and no findings.
func TestAuditsReadTheirIDsFirst(t *testing.T) {
	arrival := findItem(t, "Movies", "Movie", "Arrival")
	sev := findItem(t, "Shows", "Series", "Severance")
	for _, tool := range []string{"audit_provider", "audit_file_path"} {
		if msg := callErr(t, tool, map[string]any{"ids": []any{unknownID()}}); !strings.Contains(msg, "no item has the id "+unknownID()) {
			t.Errorf("%s on an id no item has = %s", tool, msg)
		}
		// an id that is no id at all: Jellyfin drops the filter and answers
		// with the whole library, and Emby refuses it
		want := "reading the items the ids x" + arrival + " name"
		if isJellyfin() {
			want = "no item has the id x" + arrival + ": the server answered with other items instead"
		}
		if msg := callErr(t, tool, map[string]any{"ids": []any{"x" + arrival}}); !strings.Contains(msg, want) {
			t.Errorf("%s on an id the server cannot read = %s, want %q", tool, msg, want)
		}
		// and 0, which both take for no filter at all
		if msg := callErr(t, tool, map[string]any{"ids": []any{"0"}}); !strings.Contains(msg, "no item has the id 0: the server answered with other items instead") {
			t.Errorf("%s on the id 0 = %s", tool, msg)
		}
	}
	if msg := callErr(t, "audit_provider", map[string]any{"ids": []any{sev}}); !strings.Contains(msg, sev+` is a series, "Severance"`) {
		t.Errorf("audit_provider on a series = %s", msg)
	}
	if out := call(t, "audit_file_path", map[string]any{"ids": []any{arrival, sev}}); num(t, out["items_scanned"], "items_scanned") != 2 {
		t.Errorf("audit_file_path on a film and a series = %v, want both read", out)
	}
}
