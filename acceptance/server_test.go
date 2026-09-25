//go:build integration

package acceptance

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
)

func TestServerInfo(t *testing.T) {
	out := call(t, "server_info", nil)
	if str(out["backend"]) != string(backend) {
		t.Errorf("backend = %v, want %s", out["backend"], backend)
	}
	if str(out["server_version"]) == "" || !strings.HasPrefix(str(out["server_name"]), "embyfin-mcp-") {
		t.Errorf("server_info = %v", out)
	}
	// the API this binary's client was generated from, which is not the
	// server's own version when the server has moved on since
	want := emby.APIVersion
	if isJellyfin() {
		want = jf.APIVersion
	}
	if got := str(out["sdk_api_version"]); got != want {
		t.Errorf("sdk_api_version = %q, want %q", got, want)
	}
	// Emby says what it runs on; Jellyfin 12.1 answers with an empty
	// operating system, which the tool passes on rather than guessing
	wantOS := "Linux"
	if isJellyfin() {
		wantOS = ""
	}
	if got := str(out["operating_system"]); got != wantOS {
		t.Errorf("operating_system = %q, want %q", got, wantOS)
	}
	// this binary's own build, so a session can tell it is not running the
	// fix it thinks it is; a test build stamps nothing and reports dev or the
	// module version, but never nothing
	if str(out["embyfin_mcp_version"]) == "" {
		t.Errorf("server_info does not say which embyfin-mcp build answered: %v", out)
	}
}

func TestServerStats(t *testing.T) {
	out := call(t, "server_stats", nil)
	// the clean movies and the messy ones
	if got, want := num(t, out["movies"], "movies"), 8+messyMovies(); got != want {
		t.Errorf("movies = %d, want %d", got, want)
	}
	if got, want := num(t, out["series"], "series"), 3+messySeries; got != want {
		t.Errorf("series = %d, want %d", got, want)
	}
	if got, want := num(t, out["episodes"], "episodes"), 9+messyEpisodes; got != want {
		t.Errorf("episodes = %d, want %d", got, want)
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
	// what is playing is TestSessionWhilePlaying's to show

	// a collection is counted, and one fewer once it is gone
	before := numOr0(out["collections"])
	id := str(call(t, "collection_create", map[string]any{"name": "Zzyzx Counted", "item_ids": []any{findItem(t, "Movies", "Movie", "Alien")}})["id"])
	deleteLater(t, "collection_delete", "collection", id)
	if !eventually(func() bool { return numOr0(call(t, "server_stats", nil)["collections"]) == before+1 }) {
		t.Errorf("collections = %v after one was made, want %d", call(t, "server_stats", nil)["collections"], before+1)
	}
}

// The activity log is where the user history tools read from, so the scan
// and the logins the harness caused must show up in it.
func TestServerActivity(t *testing.T) {
	out := call(t, "server_activity", map[string]any{"days": 1, "limit": 100})
	entries := rows(t, out["entries"], "entries")
	if len(entries) < 2 {
		t.Fatalf("activity = %v, want the logins and more", entries)
	}
	if num(t, out["total"], "total") < len(entries) || num(t, out["offset"], "offset") != 0 {
		t.Errorf("total %v offset %v for %d entries", out["total"], out["offset"], len(entries))
	}
	// paged by offset: the second entry heads a page that skips the first.
	// Matched by what it says rather than its stamp: Emby orders two entries
	// written milliseconds apart either way round from one read to the next
	page := call(t, "server_activity", map[string]any{"days": 1, "limit": 1, "offset": 1})
	got := rows(t, page["entries"], "entries")
	if num(t, page["offset"], "offset") != 1 || len(got) != 1 || str(got[0]["type"]) != str(entries[1]["type"]) || str(got[0]["summary"]) != str(entries[1]["summary"]) {
		t.Errorf("page at offset 1 = %v, want %v", page, entries[1])
	}
	// every entry is dated, typed, said, and graded the way its server
	// grades: Emby Info, Warn or Error, Jellyfin in full words
	severities := []string{"Info", "Warn", "Error"}
	if isJellyfin() {
		severities = []string{"Information", "Warning", "Error"}
	}
	var stamps []time.Time
	for _, e := range entries {
		if str(e["type"]) == "" || str(e["summary"]) == "" || !slices.Contains(severities, str(e["severity"])) {
			t.Errorf("entry = %v, want a type, a summary and one of %v", e, severities)
		}
		stamps = append(stamps, stamp(t, e["date"]))
	}
	// newest first, the stamps read as times: as strings, a fraction of a
	// second written shorter sorts after a longer one
	if !slices.IsSortedFunc(stamps, func(a, b time.Time) int { return b.Compare(a) }) {
		t.Errorf("entries are not newest first: %v", stamps)
	}
	// the setup's login is among the day's, as the information it is
	day := rows(t, call(t, "server_activity", map[string]any{"days": 1, "limit": 5000})["entries"], "entries")
	login := slices.IndexFunc(day, func(e map[string]any) bool {
		return strings.HasPrefix(str(e["summary"]), "root ") && strings.Contains(strings.ToLower(str(e["type"])), "auth")
	})
	if login < 0 || str(day[login]["severity"]) != severities[0] {
		t.Errorf("no login by root among the day's %d entries", len(day))
	}
}

func TestServerDevices(t *testing.T) {
	out := call(t, "server_devices", nil)
	// the testenv script logged in as root from a device of its own
	var setup map[string]any
	for _, d := range rows(t, out["devices"], "devices") {
		if str(d["name"]) == "testenv" {
			setup = d
		}
	}
	if setup == nil || str(setup["app"]) != "embyfin-mcp-testenv 0" || str(setup["last_user"]) != "root" {
		t.Errorf("the setup's device = %v, among %v", setup, out["devices"])
	}
	if setup != nil && stamp(t, setup["last_activity"]).After(time.Now().Add(time.Minute)) {
		t.Errorf("the setup's device was last active at %v", setup["last_activity"])
	}
}

func TestServerLogs(t *testing.T) {
	out := call(t, "server_logs", nil)
	files := rows(t, out["files"], "files")
	if len(files) == 0 {
		t.Fatal("no log files")
	}
	// the newest by when it was written, read as times rather than strings
	newest := files[0]
	for _, f := range files {
		if str(f["name"]) == "" {
			t.Errorf("log file row lacks a name: %v", f)
		}
		if stamp(t, f["modified"]).After(stamp(t, newest["modified"])) {
			newest = f
		}
	}
	// in bytes, like every size a tool answers with: the logs a fresh server
	// writes are a few kilobytes, which in megabytes read as 0
	if numOr0(newest["size"]) < 1024 {
		t.Errorf("the newest log's size = %v, want bytes", newest["size"])
	}

	// the default is the most recently written log, and the tail is the
	// lines asked for
	tail := call(t, "server_log", map[string]any{"lines": 5})
	if str(tail["name"]) != str(newest["name"]) {
		t.Errorf("server_log read %v, want the newest log %v", tail["name"], newest["name"])
	}
	if n := len(strings.Split(str(tail["tail"]), "\n")); n != 5 {
		t.Errorf("tail has %d lines, want 5", n)
	}

	// a named file
	named := call(t, "server_log", map[string]any{"name": str(files[len(files)-1]["name"]), "lines": 1})
	if str(named["name"]) != str(files[len(files)-1]["name"]) || strings.Contains(str(named["tail"]), "\n") {
		t.Errorf("server_log of %v = %v", files[len(files)-1]["name"], named)
	}
	// Jellyfin answers 404, Emby 500; either way the tool fails rather than
	// handing back an empty tail
	status := "HTTP 500"
	if isJellyfin() {
		status = "HTTP 404"
	}
	if msg := callErr(t, "server_log", map[string]any{"name": "no-such.log"}); !strings.Contains(msg, status) {
		t.Errorf("a log that is not there: %s", msg)
	}
}

// taskRun is when task_list says a task last finished, the zero time when it
// never has.
func taskRun(t *testing.T, name string) (time.Time, map[string]any) {
	t.Helper()

	for _, task := range rows(t, call(t, "task_list", nil)["tasks"], "tasks") {
		if strings.EqualFold(str(task["name"]), name) {
			if task["last_run"] == nil {
				return time.Time{}, task
			}
			return stamp(t, task["last_run"]), task
		}
	}
	t.Fatalf("no task %s", name)

	return time.Time{}, nil
}

func TestTasks(t *testing.T) {
	out := call(t, "task_list", nil)
	tasks := rows(t, out["tasks"], "tasks")
	var scan map[string]any
	for _, task := range tasks {
		if strings.EqualFold(str(task["name"]), "scan media library") {
			scan = task
		}
		if str(task["name"]) == "" || str(task["state"]) == "" {
			t.Errorf("task row = %v", task)
		}
	}
	if scan == nil {
		t.Fatalf("no scan task among %d tasks", len(tasks))
	}
	// the harness ran a scan, so it has a last result
	if str(scan["last_status"]) != "Completed" || str(scan["category"]) != "Library" {
		t.Errorf("scan task = %v", scan)
	}

	// run one by name, case-insensitively, and it runs: its last run moves
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
	was, _ := taskRun(t, str(scan["name"]))
	run := call(t, "task_run", map[string]any{"task": "SCAN MEDIA LIBRARY"})
	if str(run["started"]) != str(scan["name"]) {
		t.Errorf("task_run started %v, want %v", run["started"], scan["name"])
	}
	if !eventuallyWithin(scanPatience, func() bool { at, _ := taskRun(t, str(scan["name"])); return at.After(was) }) {
		t.Errorf("the scan task's last run is still %v after task_run", was)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}

	// and by id: task_list does not give them, so the id comes from the
	// server's own list. The cache cleanup is quick and touches nothing a
	// test reads
	status, raw := api(t, http.MethodGet, "/ScheduledTasks", "", nil)
	type scheduledTask struct {
		ID   string `json:"Id"`
		Name string
		Key  string
	}
	var listed []scheduledTask
	if status != http.StatusOK || json.Unmarshal(raw, &listed) != nil {
		t.Fatalf("listing the tasks: HTTP %d: %.200s", status, raw)
	}
	i := slices.IndexFunc(listed, func(task scheduledTask) bool { return task.Key == "DeleteCacheFiles" })
	if i < 0 {
		t.Fatalf("no cache cleanup task among %v", listed)
	}
	cleanup := listed[i]
	was, _ = taskRun(t, cleanup.Name)
	if run := call(t, "task_run", map[string]any{"task": cleanup.ID}); str(run["started"]) != cleanup.Name {
		t.Errorf("task_run %s = %v, want %s", cleanup.ID, run, cleanup.Name)
	}
	var task map[string]any
	if !eventually(func() bool {
		var at time.Time
		at, task = taskRun(t, cleanup.Name)
		return at.After(was) && str(task["state"]) == "Idle"
	}) {
		t.Errorf("%s never ran: %v", cleanup.Name, task)
	}
	if str(task["last_status"]) != "Completed" {
		t.Errorf("%s = %v, want it completed", cleanup.Name, task)
	}

	if msg := callErr(t, "task_run", map[string]any{"task": "no such task"}); !strings.Contains(msg, `no task named "no such task" (have: `) {
		t.Errorf("an unknown task: %s", msg)
	}
}

// task_run takes a task's name or id from task_list, which gives both, and
// every task has an id.
func TestTaskListGivesIDs(t *testing.T) {
	for _, task := range rows(t, call(t, "task_list", nil)["tasks"], "tasks") {
		if str(task["id"]) == "" {
			t.Errorf("task %v has no id", task["name"])
		}
	}
}
