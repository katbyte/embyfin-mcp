package embyfin

import (
	"context"
	"fmt"
	"net/url"
	"strings"
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
	var tasks []Task
	if err := c.get(ctx, "/ScheduledTasks", nil, &tasks); err != nil {
		return nil, err
	}

	return tasks, nil
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

	if err := c.post(ctx, "/ScheduledTasks/Running/"+url.PathEscape(task.ID), nil, nil, nil); err != nil {
		return nil, err
	}

	return task, nil
}

// RefreshLibrary triggers a scan of all libraries.
func (c *Client) RefreshLibrary(ctx context.Context) error {
	return c.post(ctx, "/Library/Refresh", nil, nil, nil)
}
