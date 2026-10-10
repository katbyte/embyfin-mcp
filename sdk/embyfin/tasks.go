package embyfin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/sdk/emby"
	"github.com/katbyte/embyfin-mcp/sdk/jf"
)

type TaskResult struct {
	Status       string `json:"Status,omitempty"`
	StartTimeUtc string `json:"StartTimeUtc,omitempty"`
	EndTimeUtc   string `json:"EndTimeUtc,omitempty"`
	ErrorMessage string `json:"ErrorMessage,omitempty"`
	// LongErrorMessage is the failure with its stack, which names the code
	// at fault where the short one only says that something failed
	LongErrorMessage string `json:"LongErrorMessage,omitempty"`
}

// Took is how long the run lasted, and whether both its ends are known.
func (r *TaskResult) Took() (time.Duration, bool) {
	start, err1 := time.Parse(time.RFC3339Nano, r.StartTimeUtc)
	end, err2 := time.Parse(time.RFC3339Nano, r.EndTimeUtc)
	if err1 != nil || err2 != nil || end.Before(start) {
		return 0, false
	}

	return end.Sub(start), true
}

// The kinds of trigger both servers have.
const (
	TriggerDaily    = "DailyTrigger"
	TriggerWeekly   = "WeeklyTrigger"
	TriggerInterval = "IntervalTrigger"
	TriggerStartup  = "StartupTrigger"
	// TriggerSystemEvent is Emby's alone: a task run when the server wakes
	TriggerSystemEvent = "SystemEventTrigger"
)

// TaskTrigger is one of the things that start a task without being asked.
type TaskTrigger struct {
	Type string `json:"Type"`
	// TimeOfDay is when a daily or weekly trigger fires, by the server's
	// own clock, as the time since its midnight
	TimeOfDay time.Duration `json:"TimeOfDay,omitempty"`
	// DayOfWeek is a weekly trigger's day, in English
	DayOfWeek string `json:"DayOfWeek,omitempty"`
	// Interval is how long an interval trigger waits between runs
	Interval time.Duration `json:"Interval,omitempty"`
	// MaxRuntime stops a run the trigger started once it has run this long;
	// zero is no limit
	MaxRuntime time.Duration `json:"MaxRuntime,omitempty"`
	// SystemEvent is what a system-event trigger waits for (Emby)
	SystemEvent string `json:"SystemEvent,omitempty"`
}

// tick is the unit both servers give a trigger's times in: a hundred
// nanoseconds.
const tick = 100 * time.Nanosecond

// clock is a time of day as hh:mm, with seconds when it has any.
func clock(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if s != 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}

	return fmt.Sprintf("%02d:%02d", h, m)
}

// span is a length of time in the largest units that hold it whole: 12h,
// 1h30m, 45s.
func span(d time.Duration) string {
	out := d.Round(time.Second).String()
	if strings.HasSuffix(out, "m0s") {
		out = strings.TrimSuffix(out, "0s")
	}
	if strings.HasSuffix(out, "h0m") {
		out = strings.TrimSuffix(out, "0m")
	}

	return out
}

// String is the trigger as a person would say it: "daily 06:00", "weekly
// Sunday 04:15", "every 12h", "at startup", with the most a run may take
// after it. A time of day is the server's own clock's.
func (t TaskTrigger) String() string {
	var what string
	switch t.Type {
	case TriggerDaily:
		what = "daily " + clock(t.TimeOfDay)
	case TriggerWeekly:
		what = "weekly " + t.DayOfWeek + " " + clock(t.TimeOfDay)
	case TriggerInterval:
		what = "every " + span(t.Interval)
	case TriggerStartup:
		what = "at startup"
	case TriggerSystemEvent:
		what = "on " + strings.ToLower(t.SystemEvent)
	default:
		what = t.Type
	}
	if t.MaxRuntime > 0 {
		what += ", for at most " + span(t.MaxRuntime)
	}

	return what
}

type Task struct {
	ID string `json:"Id"`
	// Key names what the task is, the same on both servers for the ones they
	// share (RefreshLibrary is the library scan), whatever it is called
	Key         string `json:"Key,omitempty"`
	Name        string `json:"Name"`
	Category    string `json:"Category,omitempty"`
	Description string `json:"Description,omitempty"`
	State       string `json:"State,omitempty"` // Idle, Running, Cancelling
	// Progress is how far a running task says it has got, in percent; a
	// task that is not running has none
	Progress float64 `json:"CurrentProgressPercentage,omitempty"`
	// Hidden is a task the server's own dashboard does not list
	Hidden              bool          `json:"IsHidden,omitempty"`
	Triggers            []TaskTrigger `json:"Triggers,omitempty"`
	LastExecutionResult *TaskResult   `json:"LastExecutionResult,omitempty"`
}

// LibraryScanKey is the key of the scheduled task both servers call "Scan
// media library": the scan of every library.
const LibraryScanKey = "RefreshLibrary"

// Running says whether the task is doing something now: running, or being
// cancelled, which it still is until it stops.
func (t *Task) Running() bool {
	return t.State != "" && !strings.EqualFold(t.State, "Idle")
}

func (c *Client) Tasks(ctx context.Context) ([]Task, error) {
	if c.isEmby() {
		res, err := c.emby.GetScheduledTasks(ctx, emby.GetScheduledTasksOperationOptions{})
		if err != nil {
			return nil, err
		}
		tasks := make([]Task, 0, len(res.Model))
		for i := range res.Model {
			tasks = append(tasks, taskFromEmby(&res.Model[i]))
		}

		return tasks, nil
	}

	res, err := c.jf.GetTasks(ctx, jf.GetTasksOperationOptions{})
	if err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(res.Model))
	for i := range res.Model {
		tasks = append(tasks, taskFromJF(&res.Model[i]))
	}

	return tasks, nil
}

// ScansRunning says which library scans are running now: the scan of every
// library, which is the scheduled task both servers call "Scan media
// library", and the scan of one library, which is not a task but a refresh of
// the library's own folder, and which the task list does not show. A library
// being refreshed is listed with how far it has got (seen on Emby 4.10: a
// progress while it runs, none once it ends, while the task stayed idle
// throughout); Jellyfin says so in its status as well. A scan that has just
// been asked for and not yet begun shows neither, so no scan seen is not
// proof that none is about to run.
func (c *Client) ScansRunning(ctx context.Context) ([]string, error) {
	tasks, err := c.Tasks(ctx)
	if err != nil {
		return nil, err
	}
	var running []string
	for i := range tasks {
		if tasks[i].IsLibraryScan() && tasks[i].Running() {
			running = append(running, "the scan of every library ("+tasks[i].Name+")")
		}
	}
	refreshing, err := c.librariesRefreshing(ctx)
	if err != nil {
		return nil, err
	}
	for _, name := range refreshing {
		running = append(running, "a scan of the "+name+" library")
	}

	return running, nil
}

// IsLibraryScan says whether a task is the scan of every library: by its key,
// or by the name both servers give it for a server that sends no key.
func (t *Task) IsLibraryScan() bool {
	if t.Key != "" {
		return t.Key == LibraryScanKey
	}

	return strings.EqualFold(t.Name, "scan media library")
}

// librariesRefreshing lists the libraries the server says it is refreshing
// now: those listed with a refresh progress, or an active refresh status.
func (c *Client) librariesRefreshing(ctx context.Context) ([]string, error) {
	var names []string
	busy := func(name, status string, progress float64) {
		if progress > 0 || strings.EqualFold(status, "Active") {
			names = append(names, name)
		}
	}
	if c.isEmby() {
		res, err := c.emby.GetLibraryVirtualFoldersQuery(ctx, emby.GetLibraryVirtualFoldersQueryOperationOptions{})
		if err != nil {
			return nil, err
		}
		for _, f := range orEmpty(res.Model).Items {
			busy(f.Name, f.RefreshStatus, f.RefreshProgress)
		}

		return names, nil
	}

	res, err := c.jf.GetVirtualFolders(ctx)
	if err != nil {
		return nil, err
	}
	for _, f := range res.Model {
		busy(f.Name, f.RefreshStatus, f.RefreshProgress)
	}

	return names, nil
}

// RunTask starts a scheduled task by name (case-insensitive) or id, and
// returns it as it was listed before the start.
func (c *Client) RunTask(ctx context.Context, nameOrID string) (*Task, error) {
	task, err := c.FindTask(ctx, nameOrID)
	if err != nil {
		return nil, err
	}
	if err := c.StartTask(ctx, task); err != nil {
		return nil, err
	}

	return task, nil
}

// FindTask finds a scheduled task by name (case-insensitive) or id, as the
// server lists it now.
func (c *Client) FindTask(ctx context.Context, nameOrID string) (*Task, error) {
	tasks, err := c.Tasks(ctx)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(tasks))
	for i := range tasks {
		if strings.EqualFold(tasks[i].Name, nameOrID) || tasks[i].ID == nameOrID {
			return &tasks[i], nil
		}
		names = append(names, tasks[i].Name)
	}

	return nil, fmt.Errorf("no task named %q (have: %s)", nameOrID, strings.Join(names, ", "))
}

// StartTask starts a scheduled task. The server answers once it has taken
// the start, and runs the task in the background.
func (c *Client) StartTask(ctx context.Context, task *Task) error {
	var err error
	if c.isEmby() {
		_, err = c.emby.PostScheduledTasksRunningById(ctx, task.ID)
	} else {
		_, err = c.jf.StartTask(ctx, task.ID)
	}

	return err
}

// StopTask asks the server to stop a running task. The server answers once
// it has taken the ask: the task goes on cancelling until it has stopped,
// which the task list shows.
func (c *Client) StopTask(ctx context.Context, task *Task) error {
	var err error
	if c.isEmby() {
		_, err = c.emby.DeleteScheduledTasksRunningById(ctx, task.ID)
	} else {
		_, err = c.jf.StopTask(ctx, task.ID)
	}

	return err
}

// SetTaskTriggers replaces everything that starts a task without being
// asked: the server keeps exactly the triggers given, and none when given
// none.
func (c *Client) SetTaskTriggers(ctx context.Context, task *Task, triggers []TaskTrigger) error {
	if c.isEmby() {
		in := make([]emby.TaskTriggerInfo, 0, len(triggers))
		for _, t := range triggers {
			in = append(in, emby.TaskTriggerInfo{
				Type: t.Type, DayOfWeek: emby.DayOfWeek(t.DayOfWeek), SystemEvent: emby.SystemEvent(t.SystemEvent),
				TimeOfDayTicks: int64(t.TimeOfDay / tick), IntervalTicks: int64(t.Interval / tick), MaxRuntimeTicks: int64(t.MaxRuntime / tick),
			})
		}
		_, err := c.emby.PostScheduledTasksByIdTriggers(ctx, task.ID, in)

		return err
	}
	in := make([]jf.TaskTriggerInfo, 0, len(triggers))
	for _, t := range triggers {
		if t.Type == TriggerSystemEvent {
			return fmt.Errorf("a trigger on a system event (%s) is Emby's alone: Jellyfin's tasks start daily, weekly, at an interval or at startup", t.SystemEvent)
		}
		in = append(in, jf.TaskTriggerInfo{
			Type: jf.TaskTriggerInfoType(t.Type), DayOfWeek: jf.DayOfWeek(t.DayOfWeek),
			TimeOfDayTicks: int64(t.TimeOfDay / tick), IntervalTicks: int64(t.Interval / tick), MaxRuntimeTicks: int64(t.MaxRuntime / tick),
		})
	}
	_, err := c.jf.UpdateTask(ctx, task.ID, in)

	return err
}

var weekdays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// timeOfDay reads "06:00", "6:00" or "23:59:30" as the time since midnight.
func timeOfDay(s string) (time.Duration, error) {
	for _, layout := range []string{"15:04", "15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second, nil
		}
	}

	return 0, fmt.Errorf("%q is not a time of day such as 06:00", s)
}

// ParseTaskTrigger reads a trigger written as String writes one: "daily
// 06:00", "weekly Sunday 04:15", "every 12h", "at startup", each with ", for
// at most 4h" after it for the most a run may take, and on Emby "on
// wakefromsleep". Case and spacing are free.
func ParseTaskTrigger(s string) (TaskTrigger, error) {
	const help = `write it as "daily 06:00", "weekly Sunday 04:15", "every 12h" or "at startup", with ", for at most 4h" after it to stop a run that takes longer`
	var t TaskTrigger
	what, limit, limited := strings.Cut(strings.ToLower(strings.Join(strings.Fields(s), " ")), ", for at most ")
	if limited {
		d, err := time.ParseDuration(strings.ReplaceAll(limit, " ", ""))
		if err != nil || d <= 0 {
			return t, fmt.Errorf("trigger %q: %q is not a length of time such as 4h or 90m; %s", s, limit, help)
		}
		t.MaxRuntime = d
	}

	words := strings.Fields(what)
	var err error
	switch {
	case len(words) == 2 && words[0] == "daily":
		t.Type = TriggerDaily
		t.TimeOfDay, err = timeOfDay(words[1])
	case len(words) == 3 && words[0] == "weekly":
		t.Type = TriggerWeekly
		for _, day := range weekdays {
			if strings.EqualFold(day, words[1]) {
				t.DayOfWeek = day
			}
		}
		if t.DayOfWeek == "" {
			return t, fmt.Errorf("trigger %q: %q is not a day of the week; %s", s, words[1], help)
		}
		t.TimeOfDay, err = timeOfDay(words[2])
	case len(words) == 2 && words[0] == "every":
		t.Type = TriggerInterval
		if t.Interval, err = time.ParseDuration(words[1]); err == nil && t.Interval < time.Minute {
			err = fmt.Errorf("%q is less than a minute", words[1])
		}
	case what == "at startup":
		t.Type = TriggerStartup
	case len(words) == 2 && words[0] == "on":
		t.Type = TriggerSystemEvent
		for _, event := range []string{"WakeFromSleep", "DisplayConfigurationChange", "NetworkChange"} {
			if strings.EqualFold(event, words[1]) {
				t.SystemEvent = event
			}
		}
		if t.SystemEvent == "" {
			return t, fmt.Errorf("trigger %q: %q is not a system event Emby has (wakefromsleep, displayconfigurationchange, networkchange)", s, words[1])
		}
	default:
		return t, fmt.Errorf("trigger %q is not one this reads: %s", s, help)
	}
	if err != nil {
		return t, fmt.Errorf("trigger %q: %w; %s", s, err, help)
	}

	return t, nil
}

// RefreshLibrary triggers a scan of all libraries.
func (c *Client) RefreshLibrary(ctx context.Context) error {
	if c.isEmby() {
		_, err := c.emby.PostLibraryRefresh(ctx)
		return err
	}

	_, err := c.jf.RefreshLibrary(ctx)

	return err
}
