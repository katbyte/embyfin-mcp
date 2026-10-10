package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// taskServer is a server with four scheduled tasks: a scan running a third
// of the way through, a hidden clean-up with every kind of trigger, a
// plugin's task that failed with a long stack, and one nothing starts.
// Posted triggers replace a task's, and a stop leaves the scan cancelling
// for as many reads as stopsAfter says.
type taskServer struct {
	*fakeServer

	mu         sync.Mutex
	triggers   map[string][]map[string]any
	posted     [][]map[string]any
	stops      int
	state      string
	stopsAfter int
}

const taskStack = "Example.Plugin.CalculatorException: the calculator could not start\n   at Example.Plugin.Calculators.BaseCalculator..ctor()\n"

// taskLongError is a failure's full text as a server gives it: the stack,
// and far more of it than anyone reads.
var taskLongError = taskStack + strings.Repeat("   at Example.Frame()\n", 500)

func newTaskServer(t *testing.T) *taskServer {
	t.Helper()

	s := &taskServer{fakeServer: newFakeServer(t), state: "Running", triggers: map[string][]map[string]any{
		"t1": {{"Type": "IntervalTrigger", "IntervalTicks": 432000000000}},
		"t2": {
			{"Type": "DailyTrigger", "TimeOfDayTicks": 216000000000},
			{"Type": "WeeklyTrigger", "DayOfWeek": "Sunday", "TimeOfDayTicks": 153000000000, "MaxRuntimeTicks": 144000000000},
			{"Type": "StartupTrigger"},
			{"Type": "SystemEventTrigger", "SystemEvent": "WakeFromSleep"},
		},
		"t3": {{"Type": "DailyTrigger", "TimeOfDayTicks": 0}},
	}}
	s.mux.HandleFunc("GET /ScheduledTasks", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.state == "Cancelling" {
			s.stopsAfter--
			if s.stopsAfter < 0 {
				s.state = "Idle"
			}
		}
		scan := map[string]any{"Id": "t1", "Key": "RefreshLibrary", "Name": "Scan media library", "Category": "Library", "Description": "Scans the libraries", "State": s.state, "Triggers": s.triggers["t1"]}
		if s.state == "Idle" {
			scan["LastExecutionResult"] = map[string]any{"Status": "Cancelled", "StartTimeUtc": "2026-01-05T08:00:00.0000000Z", "EndTimeUtc": "2026-01-05T08:00:00.2500000Z"}
		} else {
			scan["CurrentProgressPercentage"] = 33.3333333
		}
		writeJSON(t, w, []map[string]any{
			scan,
			{"Id": "t2", "Key": "DeleteCacheFiles", "Name": "Clean cache", "Category": "Maintenance", "State": "Idle", "IsHidden": true, "Triggers": s.triggers["t2"]},
			{"Id": "t3", "Key": "Statistics", "Name": "Calculate statistics", "Category": "Plugins", "State": "Idle", "Triggers": s.triggers["t3"], "LastExecutionResult": map[string]any{
				"Status": "Failed", "StartTimeUtc": "2026-01-05T08:00:00.0000000Z", "EndTimeUtc": "2026-01-05T09:39:12.0000000Z",
				"ErrorMessage": "The process cannot access the file because it is being used by another process.", "LongErrorMessage": taskLongError,
			}},
			{"Id": "t4", "Key": "Backup", "Name": "Back up", "Category": "Maintenance", "Description": "Writes a backup", "State": "Idle", "Triggers": s.triggers["t4"]},
		})
	})
	s.mux.HandleFunc("POST /ScheduledTasks/{id}/Triggers", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var triggers []map[string]any
		if err := json.Unmarshal(raw, &triggers); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}
		s.mu.Lock()
		s.posted, s.triggers[r.PathValue("id")] = append(s.posted, triggers), triggers
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	s.mux.HandleFunc("DELETE /ScheduledTasks/Running/{id}", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		s.stops, s.state = s.stops+1, "Cancelling"
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	return s
}

// A task is listed with how far it has got, what starts it in words, and
// how its last run went; task_get adds the failure in full, cut where it
// runs long.
func TestTaskListAndGet(t *testing.T) {
	t.Parallel()

	cs := session(t, newTaskServer(t).fakeServer, Options{})
	tasks := objects(t, mustCall(t, cs, "task_list", map[string]any{})["tasks"], "tasks")
	if len(tasks) != 4 {
		t.Fatalf("%d tasks", len(tasks))
	}
	if scan := tasks[0]; scan["state"] != "Running" || scan["progress"] != 33.3 || !slices.Equal(texts(scan["triggers"]), []string{"every 12h"}) || scan["hidden"] != nil {
		t.Errorf("the running scan = %v", scan)
	}
	if clean := tasks[1]; !boolean(t, clean["hidden"], "hidden") || clean["progress"] != nil || !slices.Equal(texts(clean["triggers"]), []string{"daily 06:00", "weekly Sunday 04:15, for at most 4h", "at startup", "on wakefromsleep"}) {
		t.Errorf("the hidden clean-up = %v", clean)
	}
	failed := tasks[2]
	if failed["last_status"] != "Failed" || failed["last_took"] != "1h39m12s" || failed["last_started"] != "2026-01-05T08:00:00.0000000Z" || failed["last_run"] != "2026-01-05T09:39:12.0000000Z" || !strings.Contains(text(failed["error"]), "being used by another process") || !slices.Equal(texts(failed["triggers"]), []string{"daily 00:00"}) {
		t.Errorf("the failed task = %v", failed)
	}
	if _, ok := failed["error_detail"]; ok {
		t.Error("the list carries a failure's full text")
	}
	if manual := tasks[3]; len(texts(manual["triggers"])) != 0 || manual["triggers"] == nil {
		t.Errorf("a task nothing starts = %v, want an empty list of triggers", manual)
	}

	got := mustCall(t, cs, "task_get", map[string]any{"task": "calculate STATISTICS"})
	detail := text(got["error_detail"])
	if cut := fmt.Sprintf("\n... (cut: %d more characters)", len(taskLongError)-errorDetailMost); got["id"] != "t3" || got["key"] != "Statistics" || detail != taskLongError[:errorDetailMost]+cut {
		t.Errorf("task_get = %v characters of detail ending %q; %v", len(detail), detail[max(0, len(detail)-40):], got["key"])
	}
	if got := mustCall(t, cs, "task_get", map[string]any{"task": "t4"}); got["description"] != "Writes a backup" || got["error_detail"] != nil || got["last_status"] != nil {
		t.Errorf("a task never run = %v", got)
	}
	if msg := mustRefuse(t, cs, "task_get", map[string]any{"task": "nope"}); !strings.Contains(msg, `no task named "nope"`) {
		t.Errorf("an unknown task = %q", msg)
	}
}

// task_edit replaces what starts a task, sends the server its ticks, and
// answers with the triggers before and after as the server lists them. A
// first trigger for a task nothing starts is refused without the delete
// tools, as starting it by hand is.
func TestTaskEdit(t *testing.T) {
	t.Parallel()

	s := newTaskServer(t)
	cs := session(t, s.fakeServer, Options{})
	out := mustCall(t, cs, "task_edit", map[string]any{"task": "Calculate statistics", "triggers": []any{"Daily 6:00", "weekly sunday 04:15, for at most 2h", "every 90m", "at  startup"}})
	if !slices.Equal(texts(out["before"]), []string{"daily 00:00"}) || !slices.Equal(texts(out["after"]), []string{"daily 06:00", "weekly Sunday 04:15, for at most 2h", "every 1h30m", "at startup"}) || out["edited"] != "Calculate statistics" {
		t.Errorf("task_edit = %v", out)
	}
	want := []map[string]any{
		{"Type": "DailyTrigger", "TimeOfDayTicks": float64(216000000000)},
		{"Type": "WeeklyTrigger", "DayOfWeek": "Sunday", "TimeOfDayTicks": float64(153000000000), "MaxRuntimeTicks": float64(72000000000)},
		{"Type": "IntervalTrigger", "IntervalTicks": float64(54000000000)},
		{"Type": "StartupTrigger"},
	}
	if sent, wanted := s.sent(t, 0), string(mustJSON(t, want)); sent != wanted {
		t.Errorf("the server was sent %s\nwant %s", sent, wanted)
	}

	// none at all leaves the task started by hand only
	out = mustCall(t, cs, "task_edit", map[string]any{"task": "t3", "triggers": []any{}})
	if len(texts(out["after"])) != 0 || !strings.Contains(text(out["note"]), "runs only when task_run asks") {
		t.Errorf("no triggers = %v", out)
	}

	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"task": "t3"}, `missing properties: ["triggers"]`},
		{map[string]any{"task": "t3", "triggers": []any{"hourly"}}, `trigger "hourly" is not one this reads`},
		{map[string]any{"task": "t3", "triggers": []any{"daily 25:00"}}, "is not a time of day"},
		{map[string]any{"task": "t3", "triggers": []any{"weekly someday 04:00"}}, "is not a day of the week"},
		{map[string]any{"task": "t3", "triggers": []any{"every 10s"}}, "is less than a minute"},
		{map[string]any{"task": "t3", "triggers": []any{"daily 06:00, for at most ever"}}, "is not a length of time"},
		// nothing starts the backup now, and nothing is to without the delete tools
		{map[string]any{"task": "Back up", "triggers": []any{"daily 03:00"}}, `refusing to give "Back up" (Maintenance: Writes a backup) a trigger without --enable-delete`},
	} {
		if msg := mustRefuse(t, cs, "task_edit", c.args); !strings.Contains(msg, c.want) {
			t.Errorf("%v refused with %q, want %q", c.args, msg, c.want)
		}
	}
	s.mu.Lock()
	if len(s.posted) != 2 {
		t.Errorf("the server was sent triggers %d times, want only for the two edits that were taken", len(s.posted))
	}
	s.mu.Unlock()

	// with them it is, and the library scan needs none either way
	withDelete := session(t, s.fakeServer, Options{EnableDelete: true})
	if out := mustCall(t, withDelete, "task_edit", map[string]any{"task": "Back up", "triggers": []any{"daily 03:00"}}); !slices.Equal(texts(out["after"]), []string{"daily 03:00"}) {
		t.Errorf("a first trigger with the delete tools = %v", out)
	}
	mustCall(t, cs, "task_edit", map[string]any{"task": "Scan media library", "triggers": []any{}})
	if out := mustCall(t, cs, "task_edit", map[string]any{"task": "Scan media library", "triggers": []any{"every 12h"}}); !slices.Equal(texts(out["after"]), []string{"every 12h"}) {
		t.Errorf("the scan's first trigger without the delete tools = %v", out)
	}
}

// sent is the triggers of the nth post the server took, as JSON.
func (s *taskServer) sent(t *testing.T, n int) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()

	return string(mustJSON(t, s.posted[n]))
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

// task_stop asks nothing of a task that is not running, and of one that is
// says whether it stopped or is still cancelling.
func TestTaskStop(t *testing.T) {
	t.Parallel()

	s := newTaskServer(t)
	r := &registry{client: s.client(t), settle: time.Millisecond}
	registerTaskTools(r)
	cs := hostRegistry(t, r)

	out := mustCall(t, cs, "task_stop", map[string]any{"task": "Clean cache"})
	if boolean(t, out["was_running"], "was_running") || !strings.Contains(text(out["note"]), "nothing was asked of the server") || s.stops != 0 {
		t.Errorf("stopping an idle task = %v, with %d stops sent", out, s.stops)
	}

	// it cancels for two reads, then is idle with its run recorded
	s.mu.Lock()
	s.stopsAfter = 2
	s.mu.Unlock()
	out = mustCall(t, cs, "task_stop", map[string]any{"task": "Scan media library"})
	if !boolean(t, out["was_running"], "was_running") || out["state"] != "Idle" || out["progress"] != 33.3 || !strings.Contains(text(out["note"]), "its run is recorded as Cancelled") || s.stops != 1 {
		t.Errorf("stopping the scan = %v, with %d stops sent", out, s.stops)
	}

	// and one that will not stop in the time watched is said to be still at it
	s.mu.Lock()
	s.state, s.stopsAfter = "Running", 1_000_000
	s.mu.Unlock()
	out = mustCall(t, cs, "task_stop", map[string]any{"task": "t1"})
	if out["state"] != "Cancelling" || !strings.Contains(text(out["note"]), "still cancelling after 60ms") {
		t.Errorf("a task that goes on cancelling = %v", out)
	}
}
