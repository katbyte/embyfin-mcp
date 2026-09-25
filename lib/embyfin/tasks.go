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
	ID                  string      `json:"Id"`
	Name                string      `json:"Name"`
	Category            string      `json:"Category,omitempty"`
	State               string      `json:"State,omitempty"` // Idle, Running, Cancelling
	LastExecutionResult *TaskResult `json:"LastExecutionResult,omitempty"`
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

// LibraryScanRunning says whether a library scan is running now: the task
// both servers call "Scan media library" is not idle.
func (c *Client) LibraryScanRunning(ctx context.Context) (bool, error) {
	tasks, err := c.Tasks(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		if strings.Contains(strings.ToLower(t.Name), "scan media library") && t.State != "" && !strings.EqualFold(t.State, "Idle") {
			return true, nil
		}
	}

	return false, nil
}

// RunTask starts a scheduled task by name (case-insensitive) or id.
func (c *Client) RunTask(ctx context.Context, nameOrID string) (*Task, error) {
	tasks, err := c.Tasks(ctx)
	if err != nil {
		return nil, err
	}

	var task *Task
	names := make([]string, 0, len(tasks))
	for i := range tasks {
		if strings.EqualFold(tasks[i].Name, nameOrID) || tasks[i].ID == nameOrID {
			task = &tasks[i]
			break
		}
		names = append(names, tasks[i].Name)
	}
	if task == nil {
		return nil, fmt.Errorf("no task named %q (have: %s)", nameOrID, strings.Join(names, ", "))
	}

	if c.isEmby() {
		_, err = c.emby.PostScheduledTasksRunningById(ctx, task.ID)
	} else {
		_, err = c.jf.StartTask(ctx, task.ID)
	}
	if err != nil {
		return nil, err
	}

	return task, nil
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
