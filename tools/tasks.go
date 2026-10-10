package tools

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// taskRow is a scheduled task as the task tools answer with it.
type taskRow struct {
	ID       string   `json:"id"                 jsonschema:"what the task tools take, as well as the name"`
	Name     string   `json:"name"`
	Category string   `json:"category,omitempty"`
	State    string   `json:"state"              jsonschema:"Idle, Running or Cancelling"`
	Progress *float64 `json:"progress,omitempty" jsonschema:"how far a running task says it has got, in percent; absent when it is not running"`
	Hidden   bool     `json:"hidden,omitempty"   jsonschema:"the server's own dashboard does not list this task"`
	Triggers []string `json:"triggers"           jsonschema:"what starts the task without being asked: daily 06:00, weekly Sunday 04:15, every 12h, at startup, each with the most a run may take when one is set. A time of day is by the server's own clock. Empty for a task only ever started by hand"`

	LastStatus  string `json:"last_status,omitempty"  jsonschema:"how the last run ended: Completed, Failed, Cancelled or Aborted"`
	LastStarted string `json:"last_started,omitempty"`
	LastRun     string `json:"last_run,omitempty"     jsonschema:"when the last run ended"`
	LastTook    string `json:"last_took,omitempty"    jsonschema:"how long the last run lasted, e.g. 1h39m12s"`
	Error       string `json:"error,omitempty"        jsonschema:"what the last run failed with, in the server's words"`
}

func taskRowOf(t *embyfin.Task) taskRow {
	row := taskRow{ID: t.ID, Name: t.Name, Category: t.Category, State: t.State, Hidden: t.Hidden, Triggers: make([]string, 0, len(t.Triggers))}
	if t.Running() {
		row.Progress = new(math.Round(t.Progress*10) / 10)
	}
	for _, trigger := range t.Triggers {
		row.Triggers = append(row.Triggers, trigger.String())
	}
	if last := t.LastExecutionResult; last != nil {
		row.LastStatus, row.LastStarted, row.LastRun, row.Error = last.Status, last.StartTimeUtc, last.EndTimeUtc, last.ErrorMessage
		if took, ok := last.Took(); ok {
			// to the second, or to the millisecond for a run shorter than one
			if row.LastTook = took.Round(time.Second).String(); took < time.Second {
				row.LastTook = took.Round(time.Millisecond).String()
			}
		}
	}

	return row
}

// errorDetailMost is how much of a failure's full text task_get gives: a
// stack names the code at fault in its first frames.
const errorDetailMost = 8000

// stopChecks is how many times task_stop looks at a task it asked to stop,
// a settle interval apart: fifteen seconds as a server is polled.
const stopChecks = 60

func registerTaskTools(r *registry) {
	client := r.client

	type taskIn struct {
		Task string `json:"task" jsonschema:"task name (case-insensitive) or id, from task_list"`
	}
	type taskGetOut struct {
		taskRow

		Key         string `json:"key,omitempty"          jsonschema:"what the task is, the same on Emby and Jellyfin for the tasks they share: RefreshLibrary is the library scan"`
		Description string `json:"description,omitempty"  jsonschema:"what the server says the task does"`
		ErrorDetail string `json:"error_detail,omitempty" jsonschema:"the last run's failure in full, with its stack: the first frames name the code at fault, a plugin's included. Cut at 8000 characters, and says so"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "task_get",
		Description: "One scheduled task whole, by name or id: its state and progress, what starts it and when, how its last run went, and the last failure in full - the short error and the stack under it, which names the code that failed. task_list gives every task without the stack.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskIn) (*mcp.CallToolResult, taskGetOut, error) {
		task, err := client.FindTask(ctx, in.Task)
		if err != nil {
			return nil, taskGetOut{}, err
		}
		out := taskGetOut{taskRow: taskRowOf(task), Key: task.Key, Description: task.Description}
		if last := task.LastExecutionResult; last != nil {
			out.ErrorDetail = last.LongErrorMessage
			if len(out.ErrorDetail) > errorDetailMost {
				out.ErrorDetail = fmt.Sprintf("%s\n... (cut: %d more characters)", out.ErrorDetail[:errorDetailMost], len(last.LongErrorMessage)-errorDetailMost)
			}
		}

		return nil, out, nil
	})

	type taskStopOut struct {
		Stopped    string   `json:"stopped"`
		ID         string   `json:"id"`
		WasRunning bool     `json:"was_running"`
		State      string   `json:"state"              jsonschema:"the task's state when this answered"`
		Progress   *float64 `json:"progress,omitempty" jsonschema:"how far it had got when asked to stop"`
		Note       string   `json:"note"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "task_stop",
		Description: "Ask the server to stop a running scheduled task, by name or id, and say whether it stopped. A task stops where it is: what it had done stays done, what it had not is left for its next run, and a task that will not stop until a step of its own ends goes on cancelling. Nothing is asked of a task that is not running.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskIn) (*mcp.CallToolResult, taskStopOut, error) {
		task, err := client.FindTask(ctx, in.Task)
		if err != nil {
			return nil, taskStopOut{}, err
		}
		out := taskStopOut{Stopped: task.Name, ID: task.ID, WasRunning: task.Running(), State: task.State}
		if !out.WasRunning {
			out.Note = "it was not running, so nothing was asked of the server"

			return nil, out, nil
		}
		out.Progress = taskRowOf(task).Progress
		if err := client.StopTask(ctx, task); err != nil {
			return nil, taskStopOut{}, err
		}
		// the server takes the ask at once and the task stops when it can
		for checks := 0; ; checks++ {
			now, err := client.FindTask(ctx, task.ID)
			if err != nil {
				return nil, taskStopOut{}, fmt.Errorf("the server took the stop, and the task could not be read again to see whether it stopped: %w", err)
			}
			out.State = now.State
			if !now.Running() {
				out.Note = "it stopped: what it had done stays done"
				if last := now.LastExecutionResult; last != nil && last.Status != "" {
					out.Note += ", and its run is recorded as " + last.Status
				}

				return nil, out, nil
			}
			if checks >= stopChecks {
				out.Note = fmt.Sprintf("the server took the stop and the task is still %s after %s: it stops once the step it is in ends; task_get shows when", strings.ToLower(now.State), (stopChecks * cmp.Or(r.settle, settleInterval)).Round(time.Millisecond))

				return nil, out, nil
			}
			if err := r.pause(ctx); err != nil {
				return nil, taskStopOut{}, err
			}
		}
	})

	type taskEditIn struct {
		Task     string   `json:"task"     jsonschema:"task name (case-insensitive) or id, from task_list"`
		Triggers []string `json:"triggers" jsonschema:"everything that should start the task from now on, replacing what does: daily 06:00, weekly Sunday 04:15, every 12h, at startup, each with \", for at most 4h\" after it to stop a run that takes longer (Emby also: on wakefromsleep). A time of day is by the server's own clock. task_list and task_get write a task's triggers this way, so keep one by repeating it. An empty list leaves the task started by hand only"`
	}
	type taskEditOut struct {
		Edited string   `json:"edited"`
		ID     string   `json:"id"`
		Before []string `json:"before"         jsonschema:"what started the task until now"`
		After  []string `json:"after"          jsonschema:"what starts it now, as the server lists it after the change"`
		Note   string   `json:"note,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "task_edit",
		Description: "Change when one of the server's scheduled tasks starts by itself: triggers replaces every trigger the task has, and the answer gives them before and after, read back from the server. Times of day are the server's own clock. " +
			"The task is not run by this, and nothing else about it changes. Giving a first trigger to a task that has none makes the server run a task nothing runs now, which is what task_run does: for any task but the library scan that needs --enable-delete, as task_run does. Moving, adding to or taking away the triggers of a task that already starts by itself does not.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskEditIn) (*mcp.CallToolResult, taskEditOut, error) {
		task, err := client.FindTask(ctx, in.Task)
		if err != nil {
			return nil, taskEditOut{}, err
		}
		want := make([]embyfin.TaskTrigger, 0, len(in.Triggers))
		for _, text := range in.Triggers {
			trigger, perr := embyfin.ParseTaskTrigger(text)
			if perr != nil {
				return nil, taskEditOut{}, perr
			}
			want = append(want, trigger)
		}
		if len(task.Triggers) == 0 && len(want) > 0 && !r.opts.EnableDelete && !task.IsLibraryScan() {
			what := task.Category
			if task.Description != "" {
				what += ": " + task.Description
			}

			return nil, taskEditOut{}, fmt.Errorf("refusing to give %q (%s) a trigger without --enable-delete: nothing starts it now, and a task other than the library scan can delete or rewrite files, clear watch state or install updates, which no tool undoes. Nothing was changed; embyfin-mcp started with --enable-delete sets it", task.Name, what)
		}

		before := taskRowOf(task).Triggers
		if err := client.SetTaskTriggers(ctx, task, want); err != nil {
			return nil, taskEditOut{}, err
		}
		now, err := client.FindTask(ctx, task.ID)
		if err != nil {
			return nil, taskEditOut{}, fmt.Errorf("the server took the change, and the task could not be read again to check it: %w", err)
		}
		out := taskEditOut{Edited: task.Name, ID: task.ID, Before: before, After: taskRowOf(now).Triggers}
		asked := make([]string, 0, len(want))
		for _, t := range want {
			asked = append(asked, t.String())
		}
		if !slices.Equal(slices.Sorted(slices.Values(asked)), slices.Sorted(slices.Values(out.After))) {
			return nil, taskEditOut{}, fmt.Errorf("the server took the change and lists %s as starting %q with %v, not the %v asked for", task.Name, task.Name, out.After, asked)
		}
		if len(out.After) == 0 {
			out.Note = "nothing starts it by itself now: it runs only when task_run asks"
		}

		return nil, out, nil
	})
}
