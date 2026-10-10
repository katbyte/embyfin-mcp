//go:build integration

package acceptance

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"
)

// logStamp is how an entry begins on each server: Emby's own clock with no
// zone, Jellyfin's with one.
func logStamp() *regexp.Regexp {
	if isJellyfin() {
		return regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3} [+-]\d{2}:\d{2} [A-Z]{3} `)
	}

	return regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3} [A-Z][a-z]+ `)
}

// The server's log is searched, not tailed: its own newest file is read
// whole, and answered as entries, as counts that add up, as a histogram
// that adds up, as gaps, and as the answers it timed.
func TestLogSearch(t *testing.T) {
	out := suite.Call(t, "server_log_search", map[string]any{"limit": 20})
	files := acc.Rows(t, out["files"], "files")
	lines := acc.Strs(t, out["lines"], "lines")
	scanned := acc.Num(t, out["entries_scanned"], "entries_scanned")
	if len(files) != 1 || scanned == 0 || acc.Num(t, out["matched"], "matched") != scanned || len(lines) != 20 || acc.Num(t, out["not_shown"], "not_shown") != scanned-20 {
		t.Fatalf("server_log_search = %d files, %d scanned, %v matched, %d lines, %v not shown", len(files), scanned, out["matched"], len(lines), out["not_shown"])
	}
	name := acc.Str(files[0]["name"])
	if own := strings.HasPrefix(name, "embyserver") || strings.HasPrefix(name, "log_"); !own || acc.Num(t, files[0]["entries"], "entries") != scanned || acc.Str(files[0]["first"]) == "" || acc.Str(files[0]["last"]) == "" {
		t.Errorf("the file read = %v, want the server's own log, read whole", files[0])
	}
	for _, line := range lines {
		if !logStamp().MatchString(line) || strings.Contains(line, "\n") {
			t.Errorf("an entry does not begin with its time and level, or is not on one line: %q", line)
		}
	}
	// the container's clock is UTC, which an Emby file's last line shows
	// against when the server says it was last written; Jellyfin's lines
	// carry the zone, so nothing is judged
	if isJellyfin() {
		if files[0]["utc_offset"] != nil || !strings.Contains(lines[0], " +00:00 ") {
			t.Errorf("Jellyfin's file = %v, its first entry %q", files[0], lines[0])
		}
	} else if acc.Str(files[0]["utc_offset"]) != "+00:00" || !strings.Contains(acc.Str(out["note"]), "the server's own clock") {
		t.Errorf("Emby's file = %v, note %q", files[0], out["note"])
	}

	// counts add up, and the message logged most is first
	counts := suite.Call(t, "server_log_search", map[string]any{"mode": "count", "limit": 5})
	total := 0
	levels, _ := counts["by_level"].(map[string]any)
	for _, n := range levels {
		total += acc.NumOr0(n)
	}
	messages := acc.Rows(t, counts["messages"], "messages")
	if matched := acc.Num(t, counts["matched"], "matched"); total != matched || matched < scanned || len(messages) != 5 || acc.Num(t, messages[0]["count"], "count") < acc.Num(t, messages[4]["count"], "count") || acc.Num(t, counts["messages_total"], "messages_total") < 5 {
		t.Errorf("count: levels add to %d of %d matched, messages %v", total, matched, messages)
	}

	// and so does a histogram, here by the hour
	hist := suite.Call(t, "server_log_search", map[string]any{"mode": "histogram", "bucket": "hour"})
	sum := 0
	for _, b := range acc.Rows(t, hist["buckets"], "buckets") {
		sum += acc.Num(t, b["count"], "count")
	}
	if sum != acc.Num(t, hist["matched"], "matched") || acc.Str(hist["bucket"]) != "1h0m0s" {
		t.Errorf("histogram: buckets add to %d of %v matched, bucket %v", sum, hist["matched"], hist["bucket"])
	}

	// a server that has been set up and scanned was never silent for a
	// year, and was for a hundredth of a second
	if gaps := suite.Call(t, "server_log_search", map[string]any{"mode": "gaps", "seconds": 31_536_000}); acc.Num(t, gaps["gaps_found"], "gaps_found") != 0 {
		t.Errorf("gaps of a year = %v", gaps)
	}
	gaps := suite.Call(t, "server_log_search", map[string]any{"mode": "gaps", "seconds": 0.01, "limit": 3})
	kept := acc.Rows(t, gaps["gaps"], "gaps")
	if acc.Num(t, gaps["gaps_found"], "gaps_found") < 3 || len(kept) != 3 || acc.Decimal(t, kept[0]["seconds"], "seconds") < 0.01 || acc.Str(kept[0]["before"]) == "" || acc.Str(kept[0]["after"]) == "" {
		t.Errorf("gaps of a hundredth of a second = %v", gaps)
	}

	// Emby times every answer it gives, and this suite has asked it a great
	// deal; Jellyfin logs none unless debug logging is on, and says so
	slow := suite.Call(t, "server_log_search", map[string]any{"mode": "slow", "seconds": 0.001, "limit": 5})
	if isJellyfin() {
		if acc.Num(t, slow["answers_timed"], "answers_timed") != 0 || !strings.Contains(acc.Str(slow["note"]), "only when it was slow and debug logging is on") {
			t.Errorf("slow on Jellyfin = %v", slow)
		}
	} else {
		slowest := acc.Rows(t, slow["slowest"], "slowest")
		if acc.Num(t, slow["answers_timed"], "answers_timed") == 0 || len(slowest) != 5 || acc.Num(t, slowest[0]["ms"], "ms") < acc.Num(t, slowest[4]["ms"], "ms") || !strings.HasPrefix(acc.Str(slowest[0]["path"]), "/") || strings.ContainsAny(acc.Str(slowest[0]["path"]), "? ") || acc.Num(t, slowest[0]["status"], "status") < 100 {
			t.Errorf("slow on Emby = %v", slow)
		}
	}
}

// A search is narrowed by time, text and level, and finds what the server
// logged about a thing just done: here, a library scan it was asked for.
func TestLogSearchFindsWhatJustHappened(t *testing.T) {
	suite.Call(t, "library_scan", nil)
	if err := suite.WaitForExpectedScan(isJellyfin()); err != nil {
		t.Fatal(err)
	}
	// both servers log the scan task as it ends, by its name
	var found []string
	if !acc.Eventually(func() bool {
		out := suite.Call(t, "server_log_search", map[string]any{"last": "10m", "include": []any{"scan media library"}, "newest": true, "limit": 5})
		found = acc.Texts(out["lines"])

		return slices.ContainsFunc(found, func(l string) bool { return strings.Contains(l, "Completed after") })
	}) {
		t.Fatalf("the scan just run is not in the last ten minutes of the log: %q", found)
	}
	// narrowed to what it is not, nothing matches
	none := suite.Call(t, "server_log_search", map[string]any{"last": "10m", "include": []any{"scan media library"}, "exclude": []any{"scan"}})
	if acc.Num(t, none["matched"], "matched") != 0 || none["lines"] != nil {
		t.Errorf("included and excluded at once = %v", none)
	}
	// a window is given by the log's own clock, as its entries are stamped:
	// from the last entry's own time on, there is at least that entry
	last := acc.Str(acc.Rows(t, suite.Call(t, "server_log_search", map[string]any{"limit": 1, "newest": true})["files"], "files")[0]["last"])
	clock := last[:len("2026-01-05 08:00:00")]
	from := suite.Call(t, "server_log_search", map[string]any{"since": clock, "mode": "count"})
	if acc.Num(t, from["matched"], "matched") < 1 || acc.Num(t, from["matched"], "matched") >= acc.Num(t, from["entries_scanned"], "entries_scanned") {
		t.Errorf("from %s on: %v matched of %v scanned", clock, from["matched"], from["entries_scanned"])
	}
	if errs := suite.Call(t, "server_log_search", map[string]any{"level": "fatal", "mode": "count"}); acc.Num(t, errs["matched"], "matched") != 0 {
		t.Errorf("a test server logged something fatal: %v", errs)
	}

	// Emby's lines carry no zone, so a time with one is refused there;
	// Jellyfin's do, and it is held against them as an instant
	zoned := map[string]any{"since": strings.Replace(clock, " ", "T", 1) + "Z", "mode": "count"}
	if isJellyfin() {
		if out := suite.Call(t, "server_log_search", zoned); acc.Num(t, out["matched"], "matched") != acc.Num(t, from["matched"], "matched") {
			t.Errorf("the same instant with its zone matched %v, without %v", out["matched"], from["matched"])
		}
	} else if msg := suite.CallErr(t, "server_log_search", zoned); !strings.Contains(msg, "cannot be held against Emby's log lines, which carry none") {
		t.Errorf("a zoned time on Emby = %q", msg)
	}
	if msg := suite.CallErr(t, "server_log_search", map[string]any{"mode": "tail"}); !strings.Contains(msg, "is not one of lines, count, histogram, gaps, slow") {
		t.Errorf("an unknown mode = %q", msg)
	}
	if msg := suite.CallErr(t, "server_log_search", map[string]any{"files": []any{"no-such.log"}}); !strings.Contains(msg, `no log file named "no-such.log"`) {
		t.Errorf("an unknown file = %q", msg)
	}

	// Emby hands its log out with each client replaced by a placeholder,
	// and as it is on disk when asked: the suite's own address is then named
	if !isJellyfin() {
		answered := []any{`Response \d+ to `}
		blank := acc.Texts(suite.Call(t, "server_log_search", map[string]any{"include": answered, "limit": 3})["lines"])
		raw := acc.Texts(suite.Call(t, "server_log_search", map[string]any{"include": answered, "limit": 3, "raw": true})["lines"])
		address := regexp.MustCompile(`Response \d+ to \d+\.\d+\.\d+\.\d+\. Time: `)
		if len(blank) != 3 || len(raw) != 3 || !strings.Contains(blank[0], " to host") || address.MatchString(blank[0]) || !address.MatchString(raw[0]) {
			t.Errorf("anonymised %q\nas on disk %q", blank, raw)
		}
	}
}

// A task is read whole, and what starts it is changed and read back from
// the server, in words both ways.
func TestTaskGetAndEdit(t *testing.T) {
	var scan map[string]any
	for _, task := range acc.Rows(t, suite.Call(t, "task_list", nil)["tasks"], "tasks") {
		if strings.EqualFold(acc.Str(task["name"]), "scan media library") {
			scan = task
		}
	}
	if scan == nil {
		t.Fatal("no scan task")
	}
	before := acc.Texts(scan["triggers"])
	// both servers scan every twelve hours unless told otherwise
	if !slices.Equal(before, []string{"every 12h"}) {
		t.Errorf("the scan's triggers = %q, want every 12h", before)
	}

	got := suite.Call(t, "task_get", map[string]any{"task": acc.Str(scan["id"])})
	if acc.Str(got["key"]) != "RefreshLibrary" || acc.Str(got["name"]) != acc.Str(scan["name"]) || acc.Str(got["description"]) == "" || !slices.Equal(acc.Texts(got["triggers"]), before) || acc.Str(got["last_status"]) == "" || acc.Str(got["last_took"]) == "" || got["error_detail"] != nil {
		t.Errorf("task_get = %v", got)
	}

	back := make([]any, 0, len(before))
	for _, trigger := range before {
		back = append(back, trigger)
	}
	suite.PutBack(t, "task_edit", map[string]any{"task": acc.Str(scan["id"]), "triggers": back})
	want := []string{"daily 06:00", "weekly Sunday 04:15, for at most 2h", "every 12h", "at startup"}
	edited := suite.Call(t, "task_edit", map[string]any{"task": "Scan Media Library", "triggers": []any{"daily 6:00", "weekly sunday 04:15, for at most 2h", "every 12h", "at startup"}})
	if !slices.Equal(acc.Texts(edited["before"]), before) || !slices.Equal(acc.Texts(edited["after"]), want) {
		t.Errorf("task_edit = %v", edited)
	}
	if now := acc.Texts(suite.Call(t, "task_get", map[string]any{"task": acc.Str(scan["id"])})["triggers"]); !slices.Equal(now, want) {
		t.Errorf("the scan's triggers read back = %q, want %q", now, want)
	}
	// none at all, and the task still runs when asked
	if none := suite.Call(t, "task_edit", map[string]any{"task": acc.Str(scan["id"]), "triggers": []any{}}); len(acc.Texts(none["after"])) != 0 || !strings.Contains(acc.Str(none["note"]), "runs only when task_run asks") {
		t.Errorf("no triggers = %v", none)
	}
	if msg := suite.CallErr(t, "task_edit", map[string]any{"task": acc.Str(scan["id"]), "triggers": []any{"hourly"}}); !strings.Contains(msg, `trigger "hourly" is not one this reads`) {
		t.Errorf("a trigger that is no trigger = %q", msg)
	}
	// Jellyfin has no trigger on a system event, and says so before asking
	if isJellyfin() {
		if msg := suite.CallErr(t, "task_edit", map[string]any{"task": acc.Str(scan["id"]), "triggers": []any{"on wakefromsleep"}}); !strings.Contains(msg, "is Emby's alone") {
			t.Errorf("a system event on Jellyfin = %q", msg)
		}
	}
}

// A task that is not running is asked nothing; one that is, is stopped.
func TestTaskStop(t *testing.T) {
	idle := suite.Call(t, "task_stop", map[string]any{"task": "Scan Media Library"})
	if idle["was_running"] != false || !strings.Contains(acc.Str(idle["note"]), "nothing was asked of the server") {
		// a scan another test left running is not this test's to judge
		t.Logf("the scan was running before the test asked: %v", idle)
	}

	// a scan slowed by files that take the server a while to read
	slowScan(t)
	suite.Call(t, "task_run", map[string]any{"task": "Scan Media Library"})
	var stopped map[string]any
	if !acc.Eventually(func() bool {
		stopped = suite.Call(t, "task_stop", map[string]any{"task": "Scan Media Library"})

		return acc.BoolOf(stopped["was_running"])
	}) {
		t.Fatalf("the scan was never seen running to be stopped: %v", stopped)
	}
	// it stops, at once or once the step it is in ends, and its run is
	// then recorded as cut short
	if !acc.Eventually(func() bool {
		return acc.Str(suite.Call(t, "task_get", map[string]any{"task": "Scan Media Library"})["state"]) == "Idle"
	}) {
		t.Fatalf("the scan asked to stop is still %v", suite.Call(t, "task_get", map[string]any{"task": "Scan Media Library"})["state"])
	}
	// both servers record a scan stopped this way as cancelled
	after := suite.Call(t, "task_get", map[string]any{"task": "Scan Media Library"})
	if state := acc.Str(stopped["state"]); (state != "Idle" && state != "Cancelling") || acc.Str(after["last_status"]) != "Cancelled" {
		t.Errorf("task_stop left the scan %s, and its run recorded as %v", state, after["last_status"])
	}
	// and the library is scanned whole again, for the tests after
	if err := suite.WaitForScan(); err != nil {
		t.Fatal(err)
	}
	suite.Call(t, "library_scan", nil)
	if err := suite.WaitForExpectedScan(isJellyfin()); err != nil {
		t.Fatal(err)
	}
}

// While something plays, the session says how it reaches the device, and
// with details what the file is.
func TestSessionDetails(t *testing.T) {
	device, token := signInPlayer(t)
	dune := findItem(t, "Movies", "Movie", "Dune: Part Two")
	p := startPlaying(t, token, dune)
	half := int64(5010) * 10_000_000
	p.report("/Sessions/Playing/Progress", half, false)

	var row map[string]any
	if !acc.Eventually(func() bool {
		for _, s := range acc.Rows(t, suite.Call(t, "session_list", map[string]any{"details": true})["sessions"], "sessions") {
			if acc.Str(s["device"]) == device {
				row = s
			}
		}

		return row != nil && acc.Str(row["now_playing_id"]) == dune && row["progress"] != nil
	}) {
		t.Fatalf("the player is not shown playing: %v", row)
	}
	details, _ := row["details"].(map[string]any)
	source, _ := details["source"].(map[string]any)
	// the client said it plays the file as it is, half way through its 167
	// minutes; the server made no stream for it
	if acc.Str(row["play_method"]) != "DirectPlay" || acc.Decimal(t, row["progress"], "progress") != 50 || row["transcoding"] != nil || details["transcode"] != nil || acc.Str(row["last_activity"]) == "" {
		t.Errorf("the session = %v", row)
	}
	if acc.Str(details["app_version"]) == "" || acc.Str(details["remote_address"]) == "" {
		t.Errorf("the device = %v", details)
	}
	if acc.Str(source["container"]) == "" || acc.Str(source["video_codec"]) == "" || acc.Num(t, source["width"], "width") == 0 || acc.Num(t, source["height"], "height") == 0 || len(acc.RowsOf(source["audio"])) == 0 {
		t.Errorf("the file being played = %v", source)
	}
	// without details the row says how it plays and nothing of the file
	short := sessionOn(t, device)
	if acc.Str(short["play_method"]) != "DirectPlay" || short["details"] != nil {
		t.Errorf("the short row = %v", short)
	}
	p.stop(half)
}

// The server's own settings are read in groups on both servers, and on
// Jellyfin the ones that tune its work are changed and read back, with
// every other setting left as it was.
func TestServerConfig(t *testing.T) {
	out := suite.Call(t, "server_config", nil)
	groups := acc.Object(t, out["groups"], "groups")
	flat := func(groups map[string]any) map[string]any {
		all := map[string]any{}
		for name, group := range groups {
			for setting, value := range acc.Object(t, group, name) {
				all[setting] = value
			}
		}

		return all
	}
	before := flat(groups)
	// both servers say how long they keep logs. Jellyfin has a name for
	// itself among its settings, empty until one is given; Emby has none
	// there until one is given, and says where its cache is or what port it
	// listens on
	known := before["ServerName"] != nil
	if !isJellyfin() {
		known = before["CachePath"] != nil || before["HttpServerPortNumber"] != nil
	}

	if acc.Str(out["backend"]) != string(backend) || len(before) < 40 || acc.Object(t, groups["logs"], "logs")["LogFileRetentionDays"] == nil || !known {
		t.Fatalf("server_config = %d settings in %d groups: %v", len(before), len(groups), groups)
	}
	if only := acc.Object(t, suite.Call(t, "server_config", map[string]any{"groups": []any{"logs"}})["groups"], "groups"); len(only) != 1 || only["logs"] == nil {
		t.Errorf("one group asked for = %v", only)
	}
	if msg := suite.CallErr(t, "server_config", map[string]any{"groups": []any{"nope"}}); !strings.Contains(msg, `no group "nope"`) {
		t.Errorf("an unknown group = %q", msg)
	}

	threads := map[string]any{"set": map[string]any{"TrickplayOptions.ProcessThreads": 3, "LibraryScanFanoutConcurrency": 2}}
	if !isJellyfin() {
		// Emby's are read, and none is on the list to change
		if len(acc.Texts(out["editable"])) != 0 || !strings.Contains(acc.Str(out["note"]), "none of Emby's server settings is changed") {
			t.Errorf("what can be changed on Emby = %v, note %q", out["editable"], out["note"])
		}
		if msg := suite.CallErr(t, "server_config_edit", threads); !strings.Contains(msg, "none of Emby's server settings is changed by this tool") {
			t.Errorf("a change on Emby = %q", msg)
		}
		if after := flat(acc.Object(t, suite.Call(t, "server_config", nil)["groups"], "groups")); !reflect.DeepEqual(before, after) {
			t.Errorf("a refused change left Emby's settings different:\nbefore %v\nafter  %v", before, after)
		}

		return
	}

	if editable := acc.Texts(out["editable"]); !slices.Contains(editable, "TrickplayOptions.ProcessThreads") || !slices.Contains(editable, "LibraryScanFanoutConcurrency") || slices.Contains(editable, "CachePath") {
		t.Errorf("what can be changed on Jellyfin = %v", editable)
	}
	// put back to what a server just set up has, whatever happens below
	suite.PutBack(t, "server_config_edit", map[string]any{"set": map[string]any{
		"TrickplayOptions.ProcessThreads": before["TrickplayOptions.ProcessThreads"], "LibraryScanFanoutConcurrency": before["LibraryScanFanoutConcurrency"],
	}})
	edited := suite.Call(t, "server_config_edit", threads)
	changed := acc.Rows(t, edited["changed"], "changed")
	if len(changed) != 2 || acc.Str(changed[0]["setting"]) != "LibraryScanFanoutConcurrency" || acc.Num(t, changed[0]["after"], "after") != 2 || acc.Str(changed[1]["setting"]) != "TrickplayOptions.ProcessThreads" || acc.Num(t, changed[1]["after"], "after") != 3 || len(acc.Texts(edited["unchanged"])) != 0 {
		t.Errorf("server_config_edit = %v", edited)
	}
	// the whole document went back to the server, and only the two changed
	after := flat(acc.Object(t, suite.Call(t, "server_config", nil)["groups"], "groups"))
	var differ []string
	for setting := range before {
		if !reflect.DeepEqual(before[setting], after[setting]) {
			differ = append(differ, setting)
		}
	}
	if slices.Sort(differ); len(after) != len(before) || !slices.Equal(differ, []string{"LibraryScanFanoutConcurrency", "TrickplayOptions.ProcessThreads"}) {
		t.Errorf("settings that differ after the change = %v, of %d before and %d after", differ, len(before), len(after))
	}
	// asked again, nothing is changed and nothing is said to have been
	if again := suite.Call(t, "server_config_edit", threads); len(acc.RowsOf(again["changed"])) != 0 || len(acc.Texts(again["unchanged"])) != 2 {
		t.Errorf("the same change again = %v", again)
	}
	for want, set := range map[string]map[string]any{
		"CachePath is not a setting this tool changes": {"CachePath": "/elsewhere"},
		"takes 2 to 31, not 40":                        {"TrickplayOptions.Qscale": 40},
		"is on or off: give true or false":             {"TrickplayOptions.EnableHwEncoding": "yes"},
	} {
		if msg := suite.CallErr(t, "server_config_edit", map[string]any{"set": set}); !strings.Contains(msg, want) || !strings.Contains(msg, "Nothing was changed") {
			t.Errorf("%v refused with %q, want %q", set, msg, want)
		}
	}
	if now := flat(acc.Object(t, suite.Call(t, "server_config", nil)["groups"], "groups")); !reflect.DeepEqual(now, after) {
		t.Error("a refused change left the settings different")
	}
}
