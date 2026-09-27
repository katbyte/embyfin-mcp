package embyfin

import (
	"context"
	"fmt"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
)

type TaskResult struct {
	Status       string `json:"Status,omitempty"`
	StartTimeUtc string `json:"StartTimeUtc,omitempty"`
	EndTimeUtc   string `json:"EndTimeUtc,omitempty"`
	ErrorMessage string `json:"ErrorMessage,omitempty"`
}

type Task struct {
	ID string `json:"Id"`
	// Key names what the task is, the same on both servers for the ones they
	// share (RefreshLibrary is the library scan), whatever it is called
	Key                 string      `json:"Key,omitempty"`
	Name                string      `json:"Name"`
	Category            string      `json:"Category,omitempty"`
	Description         string      `json:"Description,omitempty"`
	State               string      `json:"State,omitempty"` // Idle, Running, Cancelling
	LastExecutionResult *TaskResult `json:"LastExecutionResult,omitempty"`
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

// RefreshLibrary triggers a scan of all libraries.
func (c *Client) RefreshLibrary(ctx context.Context) error {
	if c.isEmby() {
		_, err := c.emby.PostLibraryRefresh(ctx)
		return err
	}

	_, err := c.jf.RefreshLibrary(ctx)

	return err
}
