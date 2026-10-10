package tools

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/serverlog"
	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// healthLastMost is the longest stretch of log one health check reads
	healthLastMost = 24 * time.Hour
	// the last run of a task that ended on its own in failure, as both
	// servers word it; Cancelled is someone stopping it
	taskFailed  = "Failed"
	taskAborted = "Aborted"
	// how what is playing reaches a device when the server re-encodes it
	playTranscode = "Transcode"
)

// counted is a number of things in words: 1 task, 2 tasks.
func counted(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}

	return fmt.Sprintf("%d %ss", n, what)
}

func registerHealthTool(r *registry) {
	client := r.client

	type healthIn struct {
		Last        string  `json:"last,omitempty"         jsonschema:"how much of the log to read, counted back from its last entry: 15m (default), 1h, 2h30m, at most 24h"`
		GapSeconds  float64 `json:"gap_seconds,omitempty"  jsonschema:"the least silence in the log that counts as a gap, default 30"`
		SlowSeconds float64 `json:"slow_seconds,omitempty" jsonschema:"the least an answer took, or a request has waited, to count as slow, default 1"`
		Limit       int     `json:"limit,omitempty"        jsonschema:"how many rows each list gives at most, default 5, at most 50"`
	}
	type healthTask struct {
		ID         string   `json:"id"`
		Name       string   `json:"name"`
		State      string   `json:"state,omitempty"       jsonschema:"Running or Cancelling"`
		Progress   *float64 `json:"progress,omitempty"    jsonschema:"how far a running task says it has got, in percent"`
		LastStatus string   `json:"last_status,omitempty" jsonschema:"Failed or Aborted"`
		LastRun    string   `json:"last_run,omitempty"    jsonschema:"when that run ended"`
		Error      string   `json:"error,omitempty"       jsonschema:"what it failed with, in the server's words: task_get has it in full"`
	}
	type healthTasks struct {
		Running []healthTask `json:"running"`
		Failed  []healthTask `json:"failed"  jsonschema:"the tasks whose last run failed or was aborted"`
	}
	type healthSession struct {
		User        string   `json:"user,omitempty"`
		Device      string   `json:"device"`
		App         string   `json:"app,omitempty"`
		NowPlaying  string   `json:"now_playing"`
		Position    string   `json:"position,omitempty"`
		Progress    *float64 `json:"progress,omitempty"`
		Paused      bool     `json:"paused,omitempty"`
		PlayMethod  string   `json:"play_method,omitempty" jsonschema:"DirectPlay, DirectStream or Transcode"`
		Transcoding string   `json:"transcoding,omitempty" jsonschema:"what the server re-encodes for the device, into what, on what, and why"`
	}
	type healthSessions struct {
		Connected   int             `json:"connected"   jsonschema:"devices with a session open, playing or not"`
		Transcoding int             `json:"transcoding" jsonschema:"how many of those playing the server is re-encoding for"`
		Playing     []healthSession `json:"playing"`
	}
	type healthLog struct {
		Files []string `json:"files,omitempty"     jsonschema:"the log files read"`
		From  string   `json:"from,omitempty"      jsonschema:"the first entry of the stretch read, as written"`
		To    string   `json:"to,omitempty"        jsonschema:"the last entry in the log, as written: the stretch is counted back from here, not from now"`
		Quiet string   `json:"quiet_for,omitempty" jsonschema:"how long ago the log was last written to, by this machine's clock: from the last entry's own time where the lines carry a zone (Jellyfin), else from the date the server gives for the file (Emby). A server with nothing to do writes nothing, so a long quiet alone is not a fault; with requests waiting or a task running it is"`

		Entries    int             `json:"entries"`
		ByLevel    map[string]int  `json:"by_level,omitempty"`
		ErrorKinds int             `json:"error_kinds"        jsonschema:"how many different messages were logged as an error or worse"`
		Errors     []logMessageRow `json:"errors,omitempty"   jsonschema:"those messages, the most logged first"`

		GapsFound int         `json:"gaps_found"     jsonschema:"stretches in which nothing was logged: how a server that stopped answering shows"`
		Gaps      []logGapRow `json:"gaps,omitempty" jsonschema:"the longest"`

		AnswersTimed int             `json:"answers_timed"     jsonschema:"answers the log gives a time for, slow or not"`
		SlowFound    int             `json:"slow_found"`
		Slowest      []logAnswerRow  `json:"slowest,omitempty"`
		WaitingFound int             `json:"waiting_found"     jsonschema:"requests logged with no answer by the last entry, waiting at least slow_seconds"`
		Waiting      []logWaitingRow `json:"waiting,omitempty" jsonschema:"the ones waiting longest"`

		Error string `json:"error,omitempty" jsonschema:"set when the log could not be read: the rest of the answer stands without it"`
	}
	type healthOut struct {
		Backend         string `json:"backend"`
		ServerName      string `json:"server_name"`
		ServerVersion   string `json:"server_version"`
		PendingRestart  bool   `json:"pending_restart"         jsonschema:"a change is waiting for the server to be restarted"`
		UpdateAvailable bool   `json:"update_available"`
		ShuttingDown    bool   `json:"shutting_down,omitempty"`

		Summary  string         `json:"summary"        jsonschema:"the counts below in one line"`
		Tasks    healthTasks    `json:"tasks"`
		Sessions healthSessions `json:"sessions"`
		Log      healthLog      `json:"log"`
		Note     string         `json:"note,omitempty"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "server_health",
		Description: "How the server is doing right now, in one call: the tasks running and how far each has got, the tasks whose last run failed, who is playing what and whether the server is re-encoding it, and what the last stretch of the log shows - entries by level, the errors by message, the gaps in which nothing was logged, the slow answers and the requests still waiting for one. " +
			"Start here when a server seems slow or stuck; task_get and server_log_search then go into whichever part stands out. The log is read back from its own last entry, not from now, and to says when that was: a log whose last entry is old belongs to a server that has gone quiet, or stopped. The server serves no part of a log file, so its newest is read whole, and on a busy server the call takes as long as that does. " +
			"Emby times each answer it logs, and at its default logging it logs a request that changes something (a POST) and not one that only reads (a GET), which debug logging adds; Jellyfin logs an answer's time only when it was slow and debug logging is on, and never a request before its answer. So nothing slow or waiting does not mean nothing was.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in healthIn) (*mcp.CallToolResult, healthOut, error) {
		last := 15 * time.Minute
		if in.Last != "" {
			var err error
			if last, err = time.ParseDuration(strings.ReplaceAll(in.Last, " ", "")); err != nil || last <= 0 {
				return nil, healthOut{}, fmt.Errorf("last %q is not a length of time such as 15m, 1h or 2h30m", in.Last)
			}
			if last > healthLastMost {
				return nil, healthOut{}, fmt.Errorf("last %s is more than the %s one check reads: server_log_search reads a longer stretch, between two times", in.Last, healthLastMost)
			}
		}
		limit := in.Limit
		switch {
		case limit <= 0:
			limit = 5
		case limit > 50:
			limit = 50
		}
		seconds := func(given, byDefault float64) time.Duration {
			if given > 0 {
				return time.Duration(given * float64(time.Second))
			}

			return time.Duration(byDefault * float64(time.Second))
		}

		info, err := client.SystemInfo(ctx)
		if err != nil {
			return nil, healthOut{}, err
		}
		emby := client.Backend() == embyfin.Emby
		out := healthOut{
			Backend: string(client.Backend()), ServerName: info.ServerName, ServerVersion: info.Version,
			PendingRestart: info.HasPendingRestart, UpdateAvailable: info.HasUpdateAvailable, ShuttingDown: info.IsShuttingDown,
			Tasks:    healthTasks{Running: []healthTask{}, Failed: []healthTask{}},
			Sessions: healthSessions{Playing: []healthSession{}},
		}

		tasks, err := client.Tasks(ctx)
		if err != nil {
			return nil, healthOut{}, err
		}
		for i := range tasks {
			row := taskRowOf(&tasks[i])
			switch {
			case tasks[i].Running():
				out.Tasks.Running = append(out.Tasks.Running, healthTask{ID: row.ID, Name: row.Name, State: row.State, Progress: row.Progress})
			case row.LastStatus == taskFailed || row.LastStatus == taskAborted:
				out.Tasks.Failed = append(out.Tasks.Failed, healthTask{ID: row.ID, Name: row.Name, LastStatus: row.LastStatus, LastRun: row.LastRun, Error: row.Error})
			}
		}

		sessions, err := client.Sessions(ctx)
		if err != nil {
			return nil, healthOut{}, err
		}
		out.Sessions.Connected = len(sessions)
		for i := range sessions {
			s := &sessions[i]
			if s.NowPlayingItem == nil {
				continue
			}
			row := healthSession{User: s.UserName, Device: s.DeviceName, App: s.Client, Paused: s.PlayState.IsPaused, PlayMethod: s.PlayState.PlayMethod, Transcoding: transcodeSummary(s.Transcoding)}
			row.NowPlaying, row.Position, row.Progress = playingOf(s)
			if row.PlayMethod == playTranscode || s.Transcoding != nil {
				out.Sessions.Transcoding++
			}
			out.Sessions.Playing = append(out.Sessions.Playing, row)
		}

		// the log is the one part a server can make too big to read in one
		// go; what it would have said is then missing, and says so, and the
		// rest stands
		search := &logSearch{mode: logModeHealth, last: last, gaps: serverlog.Gaps{Least: seconds(in.GapSeconds, 30), Keep: limit}, slow: serverlog.Slow{Least: seconds(in.SlowSeconds, 1), Keep: limit}}
		var newest embyfin.LogFile
		if err := func() error {
			listed, err := client.LogFiles(ctx)
			if err != nil {
				return err
			}
			files := logsFor(listed, time.Time{}, time.Time{}, last)
			if len(files) == 0 {
				return fmt.Errorf("none of the server's %d log files is its own log", len(listed))
			}
			if len(files) > logFilesMost {
				return fmt.Errorf("the last %s of the log is spread over %d files, and one check reads at most %d: ask for a shorter stretch", last, len(files), logFilesMost)
			}
			for _, f := range files {
				if _, err := search.readLog(ctx, client, f, false); err != nil {
					return err
				}
				out.Log.Files = append(out.Log.Files, f.Name)
				newest = f
			}
			search.finish()

			return nil
		}(); err != nil {
			if ctx.Err() != nil {
				return nil, healthOut{}, err
			}
			out.Log = healthLog{Error: err.Error()}
		} else {
			out.Log.From, out.Log.To, out.Log.Entries, out.Log.ByLevel = search.firstAt, search.lastAt, search.matched, search.byLevel
			written := search.lastEntry
			if !search.lastZoned {
				written, _ = logInstant(newest.DateModified)
			}
			// the server's clock and this machine's are two clocks: a last entry a moment into this one's future was written just now
			if quiet := time.Since(written); !written.IsZero() && quiet > -time.Minute {
				out.Log.Quiet = max(quiet, 0).Round(time.Second).String()
			}
			out.Log.ErrorKinds = search.counts.Patterns()
			for _, p := range search.counts.Top(limit) {
				out.Log.Errors = append(out.Log.Errors, logMessageRow(p))
			}
			out.Log.GapsFound = search.gaps.Found
			for _, g := range search.gaps.Kept() {
				out.Log.Gaps = append(out.Log.Gaps, logGapRow(g))
			}
			slices.SortStableFunc(out.Log.Gaps, func(a, b logGapRow) int { return cmp.Compare(b.Seconds, a.Seconds) })
			out.Log.AnswersTimed, out.Log.SlowFound = search.slow.Seen, search.slow.Found
			for _, a := range search.slow.Slowest() {
				out.Log.Slowest = append(out.Log.Slowest, logAnswerRow{At: a.At, Millis: a.Millis, Status: a.Status, Method: a.Method, Path: a.Path, Client: a.Client})
			}
			waiting, found := search.slow.Waiting()
			out.Log.WaitingFound = found
			for _, w := range waiting {
				out.Log.Waiting = append(out.Log.Waiting, logWaitingRow(w))
			}
		}

		said := []string{
			counted(len(out.Tasks.Running), "task") + " running",
			counted(len(out.Tasks.Failed), "task") + " failed on the last run",
			fmt.Sprintf("%d playing of %s connected, %d re-encoded", len(out.Sessions.Playing), counted(out.Sessions.Connected, "device"), out.Sessions.Transcoding),
		}
		if out.Log.Error != "" {
			said = append(said, "the log could not be read")
		} else {
			errorsLogged := 0
			for level, n := range out.Log.ByLevel {
				if level == serverlog.Error.String() || level == serverlog.Fatal.String() {
					errorsLogged += n
				}
			}
			said = append(said, fmt.Sprintf("in the last %s of the log: %s of %s, %s of %s or more, %s and %s waiting", last, counted(errorsLogged, "error"), counted(out.Log.ErrorKinds, "kind"), counted(out.Log.GapsFound, "gap"), search.gaps.Least, counted(out.Log.SlowFound, "slow answer"), counted(out.Log.WaitingFound, "request")))
		}
		if out.PendingRestart {
			said = append(said, "a restart is pending")
		}
		out.Summary = strings.Join(said, "; ")

		var notes []string
		if emby {
			notes = append(notes, "the log's times are the server's own clock as written, with no zone, and its clients are placeholders (host2): server_log_search with raw names them", embyTimedNote)
		} else {
			notes = append(notes, "Jellyfin logs an answer's time only when it was slow and debug logging is on, and never a request before its answer: none slow or waiting here does not mean none was")
		}
		if out.Log.Error == "" && out.Log.Entries == 0 {
			notes = append(notes, "no entry was read from the log")
		}
		if search.counts.Other > 0 {
			notes = append(notes, fmt.Sprintf("%d errors were of messages past the first %d different ones, and are in by_level but in no row", search.counts.Other, search.counts.Patterns()))
		}
		out.Note = strings.Join(notes, "; ")

		return nil, out, nil
	})
}
