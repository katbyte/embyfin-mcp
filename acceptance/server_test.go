//go:build integration

package acceptance

import (
	"slices"
	"strings"
	"testing"
)

func TestServerInfo(t *testing.T) {
	out := call(t, "server_info", nil)
	if str(out["backend"]) != string(backend) {
		t.Errorf("backend = %v, want %s", out["backend"], backend)
	}
	if str(out["version"]) == "" || str(out["server_name"]) == "" {
		t.Errorf("server_info = %v", out)
	}
}

func TestServerStats(t *testing.T) {
	out := call(t, "server_stats", nil)
	// the clean movies and the messy ones
	if got, want := num(t, out["movies"], "movies"), 8+messyMovies(); got != want {
		t.Errorf("movies = %d, want %d", got, want)
	}
	if got := num(t, out["series"], "series"); got != 3+2 {
		t.Errorf("series = %d, want 5", got)
	}
	if got := num(t, out["episodes"], "episodes"); got < 9+5 {
		t.Errorf("episodes = %d, want at least 14", got)
	}
	if got := num(t, out["albums"], "albums"); got != len(albums) {
		t.Errorf("albums = %d, want %d", got, len(albums))
	}
	if got := num(t, out["songs"], "songs"); got != songs() {
		t.Errorf("songs = %d, want %d", got, songs())
	}
	if got := num(t, out["users"], "users"); got != 2 {
		t.Errorf("users = %d, want root and alice", got)
	}
	if _, ok := out["active_sessions"]; !ok {
		t.Error("active_sessions missing")
	}
}

// The activity log is where the user history tools read from, so the scan
// and the logins the harness caused must show up in it.
func TestServerActivity(t *testing.T) {
	out := call(t, "server_activity", map[string]any{"days": 1, "limit": 100})
	entries := rows(t, out["entries"], "entries")
	if len(entries) == 0 {
		t.Fatal("no activity at all after a scan and two logins")
	}
	if num(t, out["total_in_timeframe"], "total_in_timeframe") < len(entries) {
		t.Errorf("total_in_timeframe %v < %d entries", out["total_in_timeframe"], len(entries))
	}
	for _, e := range entries {
		if str(e["date"]) == "" || str(e["type"]) == "" || str(e["summary"]) == "" {
			t.Errorf("entry lacks a field: %v", e)
		}
	}
	if len(entries) > 1 && str(entries[0]["date"]) < str(entries[len(entries)-1]["date"]) {
		t.Error("entries are not newest first")
	}
}

func TestServerDevices(t *testing.T) {
	out := call(t, "server_devices", nil)
	devices := rows(t, out["devices"], "devices")
	// the testenv script logged in as a device, and so did this suite
	var found bool
	for _, d := range devices {
		if strings.Contains(str(d["app"]), "embyfin-mcp") || strings.Contains(str(d["name"]), "embyfin-mcp") || strings.Contains(str(d["name"]), "testenv") {
			found = true
		}
	}
	if !found {
		t.Errorf("no embyfin-mcp device among %v", devices)
	}
}

func TestServerLogs(t *testing.T) {
	out := call(t, "server_logs", nil)
	files := rows(t, out["files"], "files")
	if len(files) == 0 {
		t.Fatal("no log files")
	}
	for _, f := range files {
		if str(f["name"]) == "" || str(f["modified"]) == "" {
			t.Errorf("log file row lacks a field: %v", f)
		}
	}

	// the default is the most recently modified log, and the tail is bounded
	tail := call(t, "server_log", map[string]any{"lines": 5})
	if str(tail["name"]) == "" {
		t.Error("server_log picked no file")
	}
	if n := len(strings.Split(str(tail["tail"]), "\n")); n > 5 {
		t.Errorf("tail has %d lines, want at most 5", n)
	}

	// a named file
	named := call(t, "server_log", map[string]any{"name": str(files[0]["name"]), "lines": 1})
	if str(named["name"]) != str(files[0]["name"]) {
		t.Errorf("server_log name = %v, want %v", named["name"], files[0]["name"])
	}
	// Jellyfin answers 404, Emby 500; either way the tool fails rather than
	// handing back an empty tail
	callErr(t, "server_log", map[string]any{"name": "no-such.log"})
}

func TestTasks(t *testing.T) {
	out := call(t, "task_list", nil)
	tasks := rows(t, out["tasks"], "tasks")
	var scan map[string]any
	for _, task := range tasks {
		if strings.EqualFold(str(task["name"]), "scan media library") {
			scan = task
		}
	}
	if scan == nil {
		t.Fatalf("no scan task among %d tasks", len(tasks))
	}
	if str(scan["state"]) == "" {
		t.Errorf("scan task has no state: %v", scan)
	}
	// the harness ran a scan, so it has a last result
	if str(scan["last_status"]) == "" {
		t.Errorf("scan task has no last result: %v", scan)
	}

	// run one by name, case-insensitively
	run := call(t, "task_run", map[string]any{"task": "SCAN MEDIA LIBRARY"})
	if !strings.EqualFold(str(run["started"]), str(scan["name"])) {
		t.Errorf("task_run started %v, want %v", run["started"], scan["name"])
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}

	if msg := callErr(t, "task_run", map[string]any{"task": "no such task"}); !strings.Contains(msg, "no such task") {
		t.Errorf("an unknown task: %s", msg)
	}
}

// The server tools are the whole server family: nothing else may have been
// added without a test here.
func TestServerFamilyIsComplete(t *testing.T) {
	var got []string
	for _, name := range toolNames(t) {
		if strings.HasPrefix(name, "server_") || strings.HasPrefix(name, "task_") {
			got = append(got, name)
		}
	}
	want := []string{"server_activity", "server_devices", "server_info", "server_log", "server_logs", "server_stats", "task_list", "task_run"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("server tools = %v, want %v", got, want)
	}
}
