package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
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
	if pick := latest(func(n string) bool { return (&embyfin.LogFile{Name: n}).ServerOwn() }); pick != "" {
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

		PendingRestart  bool `json:"pending_restart"            jsonschema:"a change is waiting for the server to be restarted: a plugin installed or updated, a setting only a start reads"`
		UpdateAvailable bool `json:"update_available"           jsonschema:"the server says a newer version of itself is out"`
		ShuttingDown    bool `json:"shutting_down,omitempty"`
		CanSelfRestart  bool `json:"can_self_restart,omitempty" jsonschema:"the server can restart itself when asked, rather than only stop"`

		Architecture string            `json:"architecture,omitempty" jsonschema:"the server process's, from its log's startup lines. Jellyfin's API has a field for it that says X64 on an arm64 machine, so that is not used"`
		Processors   int               `json:"processors,omitempty"   jsonschema:"how many processors the server counted when it started. Neither server's API gives this: it is read from the startup lines of the server's log, and absent when no recent log begins with a start"`
		Started      string            `json:"started,omitempty"      jsonschema:"when the server last started, as its log wrote it: Jellyfin's lines carry the zone, Emby's are its own clock with none"`
		Paths        map[string]string `json:"paths,omitempty"        jsonschema:"where the server keeps things on its own disk, by kind (data, logs, cache, metadata, transcodes): from the server where it says, else from its log's startup lines"`
		Note         string            `json:"note,omitempty"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_info",
		Description: "Check connectivity to the media server and return its name and version, the API version this MCP server's client was built against, and the version of this MCP server; whether the server is waiting for a restart or has an update out; and what it says of the machine it runs on - processors, architecture, when it started, where it keeps its data, logs, cache and metadata - which it writes at the top of its log as it starts and gives no other way.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, serverInfoOut, error) {
		info, err := client.SystemInfo(ctx)
		if err != nil {
			return nil, serverInfoOut{}, err
		}
		out := serverInfoOut{
			Backend:           string(client.Backend()),
			ServerName:        info.ServerName,
			ServerVersion:     info.Version,
			SDKAPIVersion:     client.APIVersion(),
			OperatingSystem:   info.OperatingSystem,
			EmbyfinMCPVersion: version.Version,
			PendingRestart:    info.HasPendingRestart,
			UpdateAvailable:   info.HasUpdateAvailable,
			ShuttingDown:      info.IsShuttingDown,
			CanSelfRestart:    info.CanSelfRestart,
			Paths:             info.Paths,
		}
		// the rest is in the log alone, and the server is reachable and
		// described without it: a log that cannot be read is said, not fatal
		start, err := client.LogStartup(ctx)
		switch {
		case err != nil:
			out.Note = "the server's log could not be read for what it says of itself at startup (processors, when it started): " + err.Error()
		case start == nil:
			out.Note = "none of the server's recent logs begins with a start, so the processors it counted and when it started are not known: it begins a new log each midnight, and has been up longer than the logs looked at"
		default:
			out.Processors, out.Started, out.Architecture = start.Processors, start.At, start.Architecture
			// Jellyfin 12.2's API names no operating system; its log does
			if out.OperatingSystem == "" {
				out.OperatingSystem = start.OperatingSystem
			}
			for kind, path := range start.Paths {
				if out.Paths == nil {
					out.Paths = map[string]string{}
				}
				if out.Paths[kind] == "" {
					out.Paths[kind] = path
				}
			}
		}

		return nil, out, nil
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
		Days   int    `json:"days,omitempty"   jsonschema:"how many days back to include, default 60 or as many as the server keeps if fewer"`
		Item   string `json:"item,omitempty"   jsonschema:"only the entries about one library item, by id: a film, an episode or a track, or a series, a season or an album, whose episodes or tracks are read as the library holds them now (a play of one since deleted is not found). A collection, a playlist, an artist or a folder is refused rather than answered with nothing"`
		User   string `json:"user,omitempty"   jsonschema:"only the entries about one user, by name or id: their logins, plays and the rest"`
		Limit  int    `json:"limit,omitempty"  jsonschema:"page size, default 50"`
		Offset int    `json:"offset,omitempty" jsonschema:"skip this many entries, to page"`
	}
	type activityEntry struct {
		Date     string `json:"date"`
		Type     string `json:"type"`
		Severity string `json:"severity,omitempty"`
		Summary  string `json:"summary"`
		ItemID   string `json:"item_id,omitempty"  jsonschema:"the library item the entry is about, where the server records it"`
	}
	type activityOut struct {
		Days     int             `json:"days"             jsonschema:"the period read, in days back from now"`
		Item     string          `json:"item,omitempty"   jsonschema:"the item the entries were kept to"`
		User     string          `json:"user,omitempty"   jsonschema:"the user the entries were kept to"`
		Covers   *int            `json:"covers,omitempty" jsonschema:"item, for a series, a season or an album: how many of its episodes or tracks the log was read for, as the library holds them now"`
		Total    int             `json:"total"            jsonschema:"entries in the period, across every page, as far as the log was read"`
		Offset   int             `json:"offset"`
		Entries  []activityEntry `json:"entries"          jsonschema:"newest first"`
		Complete bool            `json:"complete"         jsonschema:"false when not all of the period could be read: it reaches back past what the server keeps of its activity log, or, kept to an item or a user, it holds more activity than one call reads. The answer then covers only the newest part of it, and note says how far back"`
		Note     string          `json:"note,omitempty"   jsonschema:"what of the period could not be read, and why"`
	}
	summarise := func(e *embyfin.ActivityEntry) activityEntry {
		summary := e.Name
		if e.ShortOverview != "" {
			summary += " — " + e.ShortOverview
		}

		return activityEntry{Date: e.Date, Type: e.Type, Severity: e.Severity, Summary: summary, ItemID: e.ItemID}
	}
	add(r, readTool, &mcp.Tool{
		Name: "server_activity",
		Description: "The server's activity log, newest first: logins, playback, library changes, errors. The last 60 days, or as many as the server keeps if fewer (Jellyfin deletes activity older than its retention, 30 days out of the box); a period asked for past what the server keeps comes back complete false, saying how far back can be seen. " +
			"item keeps the entries about one item (who played it, when): plays are logged against what was played, so a series, a season or an album is read as its episodes or tracks, as the library holds them now. user keeps the entries about one user, by the user's id where the server records it (Jellyfin) and by the user's name leading the entry's text on Emby, whose entries carry no id a client can match. Either reads the whole period, and says so when it holds more than one call reads.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in activityIn) (*mcp.CallToolResult, activityOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		offset := max(in.Offset, 0)
		window, err := client.ActivityWindow(ctx, in.Days)
		if err != nil {
			return nil, activityOut{}, err
		}
		out := activityOut{Days: window.Days, Offset: offset, Entries: []activityEntry{}, Complete: !window.Short, Note: window.Note}

		// about one item: by id when the entry names its item, and by title
		// when it carries none; a title is a substring of others ("Dune" of
		// "Dune: Part Two"), so an entry with an id is never matched by title
		var about func(e *embyfin.ActivityEntry) bool
		if in.Item != "" {
			it, ierr := client.ItemByID(ctx, in.Item)
			if ierr != nil {
				return nil, activityOut{}, ierr
			}
			// what the log names a play of this by: the item itself, or what
			// it holds. A series' id is in no entry, and read by it alone
			// the answer was no plays at all, complete
			held, herr := playedAs(ctx, client, it)
			if herr != nil {
				return nil, activityOut{}, herr
			}
			played := map[string]bool{in.Item: true}
			for _, id := range held {
				played[id] = true
			}
			out.Item = it.Name
			if held != nil {
				out.Covers = new(len(held))
			}
			about = func(e *embyfin.ActivityEntry) bool {
				if e.ItemID == "" {
					return strings.Contains(e.Name, it.Name) || strings.Contains(e.ShortOverview, it.Name)
				}

				return played[e.ItemID]
			}
		}
		if in.User != "" {
			user, uerr := client.ResolveUser(ctx, in.User)
			if uerr != nil {
				return nil, activityOut{}, uerr
			}
			users, uerr := client.Users(ctx)
			if uerr != nil {
				return nil, activityOut{}, uerr
			}
			jellyfin := client.Backend() == embyfin.Jellyfin
			out.User = user.Name
			aboutItem := about
			about = func(e *embyfin.ActivityEntry) bool {
				return playedBy(e, users, jellyfin) == user.ID && (aboutItem == nil || aboutItem(e))
			}
		}

		if about == nil {
			// the server pages the whole log itself
			entries, total, lerr := client.ActivityLog(ctx, window.Cutoff, limit, offset)
			if lerr != nil {
				return nil, activityOut{}, lerr
			}
			out.Total = total
			for i := range entries {
				out.Entries = append(out.Entries, summarise(&entries[i]))
			}

			return nil, out, nil
		}

		// kept to an item or a user, the whole period is read and sifted
		// here: the server filters its log by user on Jellyfin alone, and by
		// item on neither
		activity, err := client.ReadActivity(ctx, window.Cutoff)
		if err != nil {
			return nil, activityOut{}, err
		}
		out.Complete = activity.Complete && !window.Short
		out.Note = joinNotes(window.Note, activity.Note())
		var kept []activityEntry
		for i := range activity.Entries {
			if about(&activity.Entries[i]) {
				kept = append(kept, summarise(&activity.Entries[i]))
			}
		}
		out.Total = len(kept)
		if offset < len(kept) {
			out.Entries = kept[offset:min(offset+limit, len(kept))]
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
	type logIn struct {
		Name  string `json:"name,omitempty"  jsonschema:"log file name (files in the answer lists them); empty fetches the server's own log"`
		Lines int    `json:"lines,omitempty" jsonschema:"how many lines from the end to return, default 200"`
	}
	type logOut struct {
		Name  string       `json:"name"`
		Tail  string       `json:"tail"`
		Files []logFileRow `json:"files"          jsonschema:"every log file the server has, with size and modification time: the names this tool takes"`
		Note  string       `json:"note,omitempty" jsonschema:"set when no name was given and the server lists no log of its own, so the most recently changed file was read instead"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_log",
		Description: "Fetch the tail of a server log file, and list every log file the server has: by default the tail of the server's own log (Emby's embyserver.txt, Jellyfin's newest log_<date>.log), not the transcode or hardware logs beside it, which files names for a second call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in logIn) (*mcp.CallToolResult, logOut, error) {
		files, err := client.LogFiles(ctx)
		if err != nil {
			return nil, logOut{}, err
		}
		rows := make([]logFileRow, 0, len(files))
		for _, f := range files {
			rows = append(rows, logFileRow{Name: f.Name, Size: f.Size, Modified: f.DateModified})
		}
		name, note := in.Name, ""
		if name == "" {
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

		return nil, logOut{Name: name, Tail: tail, Files: rows, Note: note}, nil
	})

	type tasksOut struct {
		Tasks []taskRow `json:"tasks"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "task_list",
		Description: "List the server's scheduled tasks (library scan, metadata refresh, backups...) with their ids and state, how far a running one has got, what starts each without being asked (daily 06:00, every 12h, at startup: times of day are the server's own clock), and how its last run went: when, how long it took, and the error it failed with. task_get gives one task whole, with the failure's full text.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, tasksOut, error) {
		tasks, err := client.Tasks(ctx)
		if err != nil {
			return nil, tasksOut{}, err
		}

		out := tasksOut{}
		for i := range tasks {
			out.Tasks = append(out.Tasks, taskRowOf(&tasks[i]))
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
