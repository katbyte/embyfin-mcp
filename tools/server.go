package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/katbyte/go-kt/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerServerTools(r *registry) {
	client := r.client
	// serverInfoOut names both versions, because both change what a caller can
	// rely on: the media server's decides what the API answers, and this
	// binary's decides which tools and fixes are in play. The second is the one
	// that goes stale unnoticed - an MCP client keeps the binary it started
	// with, so a session can run for hours on a build that predates the fix it
	// is relying on, and nothing else it can call would say so.
	type serverInfoOut struct {
		Backend           string `json:"backend"`
		ServerName        string `json:"server_name"`
		ServerVersion     string `json:"server_version"`
		SDKAPIVersion     string `json:"sdk_api_version"     jsonschema:"the server API version this MCP server's client was generated from; a server ahead of it may answer with fields the client does not read"`
		OperatingSystem   string `json:"operating_system"`
		EmbyfinMCPVersion string `json:"embyfin_mcp_version" jsonschema:"the build of this MCP server answering, e.g. v0.1.1+4@g8909c7c: the tag, the commits since it, and the commit"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_info",
		Description: "Check connectivity to the media server and return its name and version, the API version this MCP server's client was built against, and the version of this MCP server.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, serverInfoOut, error) {
		info, err := client.SystemInfo(ctx)
		if err != nil {
			return nil, serverInfoOut{}, err
		}

		return nil, serverInfoOut{
			Backend:           string(client.Backend()),
			ServerName:        info.ServerName,
			ServerVersion:     info.Version,
			SDKAPIVersion:     client.APIVersion(),
			OperatingSystem:   info.OperatingSystem,
			EmbyfinMCPVersion: version.Version,
		}, nil
	})

	type serverStatsOut struct {
		Movies         int `json:"movies"`
		Series         int `json:"series"`
		Episodes       int `json:"episodes"`
		Albums         int `json:"albums,omitempty"`
		Songs          int `json:"songs,omitempty"`
		Collections    int `json:"collections,omitempty"`
		ActiveSessions int `json:"active_sessions"`
		Users          int `json:"users"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_stats",
		Description: "Global library counts (movies, series, episodes, music), user count, and active playback session count.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, serverStatsOut, error) {
		counts, err := client.Counts(ctx)
		if err != nil {
			return nil, serverStatsOut{}, err
		}

		sessions, err := client.Sessions(ctx)
		if err != nil {
			return nil, serverStatsOut{}, err
		}
		active := 0
		for _, s := range sessions {
			if s.NowPlayingItem != nil {
				active++
			}
		}

		users, err := client.Users(ctx)
		if err != nil {
			return nil, serverStatsOut{}, err
		}

		return nil, serverStatsOut{
			Movies:         counts.MovieCount,
			Series:         counts.SeriesCount,
			Episodes:       counts.EpisodeCount,
			Albums:         counts.AlbumCount,
			Songs:          counts.SongCount,
			Collections:    counts.BoxSetCount,
			ActiveSessions: active,
			Users:          len(users),
		}, nil
	})

	type activityIn struct {
		Days   int `json:"days,omitempty"   jsonschema:"how many days back to include, default 60"`
		Limit  int `json:"limit,omitempty"  jsonschema:"page size, default 50"`
		Offset int `json:"offset,omitempty" jsonschema:"skip this many entries, to page"`
	}
	type activityEntry struct {
		Date     string `json:"date"`
		Type     string `json:"type"`
		Severity string `json:"severity,omitempty"`
		Summary  string `json:"summary"`
	}
	type activityOut struct {
		Total   int             `json:"total"   jsonschema:"entries in the timeframe, across every page"`
		Offset  int             `json:"offset"`
		Entries []activityEntry `json:"entries"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_activity",
		Description: "Recent server activity log: logins, playback, library changes, errors. Newest first, default last 60 days.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in activityIn) (*mcp.CallToolResult, activityOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}

		offset := max(in.Offset, 0)
		entries, total, err := client.ActivityLog(ctx, daysCutoff(in.Days), limit, offset)
		if err != nil {
			return nil, activityOut{}, err
		}

		out := activityOut{Total: total, Offset: offset, Entries: []activityEntry{}}
		for _, e := range entries {
			summary := e.Name
			if e.ShortOverview != "" {
				summary += " — " + e.ShortOverview
			}
			out.Entries = append(out.Entries, activityEntry{
				Date:     e.Date,
				Type:     e.Type,
				Severity: e.Severity,
				Summary:  summary,
			})
		}

		return nil, out, nil
	})

	type deviceOut struct {
		Name         string `json:"name"`
		App          string `json:"app"`
		LastUser     string `json:"last_user,omitempty"`
		LastActivity string `json:"last_activity,omitempty"`
	}
	type devicesOut struct {
		Devices []deviceOut `json:"devices"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_devices",
		Description: "Devices and apps that have connected to the server, with last user and last-seen time.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, devicesOut, error) {
		devices, err := client.Devices(ctx)
		if err != nil {
			return nil, devicesOut{}, err
		}

		out := devicesOut{}
		for _, d := range devices {
			out.Devices = append(out.Devices, deviceOut{
				Name:         d.Name,
				App:          strings.TrimSpace(d.AppName + " " + d.AppVersion),
				LastUser:     d.LastUserName,
				LastActivity: d.DateLastActivity,
			})
		}

		return nil, out, nil
	})

	type logFileRow struct {
		Name     string `json:"name"`
		Size     int64  `json:"size"     jsonschema:"file size in bytes"`
		Modified string `json:"modified"`
	}
	type logsOut struct {
		Files []logFileRow `json:"files"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_logs",
		Description: "List the server's log files with size and modification time.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, logsOut, error) {
		files, err := client.LogFiles(ctx)
		if err != nil {
			return nil, logsOut{}, err
		}

		out := logsOut{}
		for _, f := range files {
			out.Files = append(out.Files, logFileRow{Name: f.Name, Size: f.Size, Modified: f.DateModified})
		}

		return nil, out, nil
	})

	type logIn struct {
		Name  string `json:"name,omitempty"  jsonschema:"log file name; empty fetches the most recently modified log"`
		Lines int    `json:"lines,omitempty" jsonschema:"how many lines from the end to return, default 200"`
	}
	type logOut struct {
		Name string `json:"name"`
		Tail string `json:"tail"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_log",
		Description: "Fetch the tail of a server log file — by default the most recent/active one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in logIn) (*mcp.CallToolResult, logOut, error) {
		name := in.Name
		if name == "" {
			files, err := client.LogFiles(ctx)
			if err != nil {
				return nil, logOut{}, err
			}

			latest := ""
			for _, f := range files {
				if latest == "" || f.DateModified > latest {
					latest = f.DateModified
					name = f.Name
				}
			}
			if name == "" {
				return nil, logOut{}, errors.New("server reports no log files")
			}
		}

		text, err := client.LogText(ctx, name)
		if err != nil {
			return nil, logOut{}, err
		}

		lines := in.Lines
		if lines <= 0 {
			lines = 200
		}
		split := strings.Split(strings.TrimRight(text, "\n"), "\n")
		if len(split) > lines {
			split = split[len(split)-lines:]
		}

		return nil, logOut{Name: name, Tail: strings.Join(split, "\n")}, nil
	})

	type taskOut struct {
		Name       string `json:"name"`
		Category   string `json:"category,omitempty"`
		State      string `json:"state"`
		LastStatus string `json:"last_status,omitempty"`
		LastRun    string `json:"last_run,omitempty"`
	}
	type tasksOut struct {
		Tasks []taskOut `json:"tasks"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "task_list",
		Description: "List the server's scheduled tasks (library scan, metadata refresh, backups...) with state and last result.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, tasksOut, error) {
		tasks, err := client.Tasks(ctx)
		if err != nil {
			return nil, tasksOut{}, err
		}

		out := tasksOut{}
		for _, t := range tasks {
			row := taskOut{Name: t.Name, Category: t.Category, State: t.State}
			if t.LastExecutionResult != nil {
				row.LastStatus = t.LastExecutionResult.Status
				row.LastRun = t.LastExecutionResult.EndTimeUtc
			}
			out.Tasks = append(out.Tasks, row)
		}

		return nil, out, nil
	})

	type taskRunIn struct {
		Task string `json:"task" jsonschema:"task name (case-insensitive) or id, from task_list"`
	}
	type taskRunOut struct {
		Started string `json:"started"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "task_run",
		Description: "Start a scheduled task by name. Changes server state: the task runs immediately.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskRunIn) (*mcp.CallToolResult, taskRunOut, error) {
		task, err := client.RunTask(ctx, in.Task)
		if err != nil {
			return nil, taskRunOut{}, err
		}

		return nil, taskRunOut{Started: task.Name}, nil
	})
}
