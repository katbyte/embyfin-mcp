package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/go-kt/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverLog picks the server's own log out of its log files: the newest of
// Emby's embyserver.txt (the live one) and embyserver-<n>.txt (the rotated
// ones), or of Jellyfin's log_<date>.log, one a day. The files beside them
// are the transcodes' and the hardware probes', and the most recently changed
// file is as often one of those, a playback running now. own is false when
// none is the server's, and the most recently changed file comes back
// instead.
func serverLog(files []embyfin.LogFile) (name string, own bool) {
	latest := func(keep func(string) bool) string {
		pick, when := "", ""
		for _, f := range files {
			if keep(f.Name) && (pick == "" || f.DateModified > when) {
				pick, when = f.Name, f.DateModified
			}
		}

		return pick
	}
	if pick := latest(func(n string) bool {
		n = strings.ToLower(n)
		return strings.HasPrefix(n, "embyserver") || strings.HasPrefix(n, "log_")
	}); pick != "" {
		return pick, true
	}

	return latest(func(string) bool { return true }), false
}

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
		Movies         int `json:"movies"                jsonschema:"films as the server counts them: Emby counts each file of a film held in several as a film of its own, Jellyfin a film once however many files it is held in"`
		Series         int `json:"series"`
		Episodes       int `json:"episodes"              jsonschema:"episodes as the server counts them: Emby counts each file of an episode held in several, and an extra it took for an episode; Jellyfin an episode once however many files"`
		Albums         int `json:"albums,omitempty"`
		Songs          int `json:"songs,omitempty"`
		Collections    int `json:"collections,omitempty"`
		ActiveSessions int `json:"active_sessions"`
		Users          int `json:"users"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_stats",
		Description: "Global library counts (movies, series, episodes, music) as the server keeps them, user count, and active playback session count. The two servers count differently: Emby counts every file of a film or episode held in several files, Jellyfin each film or episode once, so the same library reads higher on Emby.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, serverStatsOut, error) {
		counts, err := client.Counts(ctx)
		if err != nil {
			return nil, serverStatsOut{}, err
		}
		// Emby's counts leave collections at 0 however many there are, so
		// they are counted the way collection_list lists them
		_, collections, err := client.Search(ctx, embyfin.SearchOptions{IncludeItemTypes: "BoxSet", Fields: embyfin.FieldsLean, Limit: 1})
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
			Collections:    collections,
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
		Name  string `json:"name,omitempty"  jsonschema:"log file name (server_logs lists them); empty fetches the server's own log"`
		Lines int    `json:"lines,omitempty" jsonschema:"how many lines from the end to return, default 200"`
	}
	type logOut struct {
		Name string `json:"name"`
		Tail string `json:"tail"`
		Note string `json:"note,omitempty" jsonschema:"set when no name was given and the server lists no log of its own, so the most recently changed file was read instead"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_log",
		Description: "Fetch the tail of a server log file: by default the server's own log (Emby's embyserver.txt, Jellyfin's newest log_<date>.log), not the transcode or hardware logs beside it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in logIn) (*mcp.CallToolResult, logOut, error) {
		name, note := in.Name, ""
		if name == "" {
			files, err := client.LogFiles(ctx)
			if err != nil {
				return nil, logOut{}, err
			}
			if len(files) == 0 {
				return nil, logOut{}, errors.New("server reports no log files")
			}
			var own bool
			if name, own = serverLog(files); !own {
				note = fmt.Sprintf("the server lists no log of its own (embyserver.txt, log_<date>.log), so this is the most recently changed of its %d log files", len(files))
			}
		}

		lines := in.Lines
		if lines <= 0 {
			lines = 200
		}
		// the whole log is read through for its tail, however big it is
		tail, err := client.LogTail(ctx, name, lines)
		if err != nil {
			return nil, logOut{}, err
		}

		return nil, logOut{Name: name, Tail: tail, Note: note}, nil
	})

	type taskOut struct {
		ID         string `json:"id"                    jsonschema:"what task_run takes, as well as the name"`
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
		Description: "List the server's scheduled tasks (library scan, metadata refresh, backups...) with their ids, state and last result.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, tasksOut, error) {
		tasks, err := client.Tasks(ctx)
		if err != nil {
			return nil, tasksOut{}, err
		}

		out := tasksOut{}
		for _, t := range tasks {
			row := taskOut{ID: t.ID, Name: t.Name, Category: t.Category, State: t.State}
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
		Started      string   `json:"started"`
		ID           string   `json:"id"`
		Category     string   `json:"category,omitempty"`
		Description  string   `json:"description,omitempty"   jsonschema:"what the server says the task does"`
		WasRunning   bool     `json:"was_running,omitempty"   jsonschema:"the task was already running when it was asked for"`
		ScansRunning []string `json:"scans_running,omitempty" jsonschema:"the library scans the server showed running just before the task was asked for"`
		Note         string   `json:"note"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "task_run",
		Description: "Start one of the server's scheduled tasks, by name or id (task_list has them). The server runs it in the background, for seconds or for hours, and this answers once the task is started: task_list shows when it ends and how it went. " +
			"What a task does is the server's own, and nothing a task does can be undone with these tools: the library scan drops every item whose file is gone, reads new files and re-reads changed ones; the others delete cache, log and transcode files, delete old activity log entries or the watch state of items gone for months, write images into the library's folders, download subtitles and lyrics, look up people and upcoming episodes, or download and install updates to the server and its plugins. " +
			"Without --enable-delete only the library scan (the task keyed RefreshLibrary, called Scan media library) is started, and any other task is refused with what the server says it does; with it, any task is. The answer gives the task's category and description, and says whether it, or a library scan, was already running.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskRunIn) (*mcp.CallToolResult, taskRunOut, error) {
		task, err := client.FindTask(ctx, in.Task)
		if err != nil {
			return nil, taskRunOut{}, err
		}
		if !r.opts.EnableDelete && !task.IsLibraryScan() {
			what := task.Category
			if task.Description != "" {
				what += ": " + task.Description
			}
			return nil, taskRunOut{}, fmt.Errorf("refusing to start %q (%s) without --enable-delete: a task other than the library scan can delete or rewrite files, clear watch state or install updates, which no tool undoes. Nothing was started; embyfin-mcp started with --enable-delete runs it", task.Name, what)
		}
		// read before the start: afterwards, the task itself is the scan
		scans, err := client.ScansRunning(ctx)
		if err != nil {
			return nil, taskRunOut{}, fmt.Errorf("could not tell whether a library scan was running, so %s was not started: %w", task.Name, err)
		}
		if err := client.StartTask(ctx, task); err != nil {
			return nil, taskRunOut{}, err
		}

		out := taskRunOut{
			Started: task.Name, ID: task.ID, Category: task.Category, Description: task.Description,
			WasRunning: task.Running(), ScansRunning: scans,
			Note: "runs in the background; task_list shows when it ends and how it went",
		}
		if out.WasRunning {
			out.Note = "it was already running (" + task.State + ") when asked for: the run under way goes on in the background; task_list shows when it ends and how it went"
		}

		return nil, out, nil
	})
}
