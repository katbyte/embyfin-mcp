package tools

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// healthServer is the made-up Emby of the task and log tests at once: a scan
// a third done, a task that failed, three devices of which two are playing
// and one is being re-encoded for, and a log with an error, a silence of two
// minutes, an answer that took as long and a request never answered.
func healthServer(t *testing.T) *taskServer {
	t.Helper()

	s := newTaskServer(t)
	serveEmbyLogs(t, s.fakeServer)
	s.mux.HandleFunc("GET /System/Info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"ServerName": "Zzyzx", "Version": "4.10.1.0", "HasPendingRestart": true})
	})
	s.mux.HandleFunc("GET /Sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{
			{
				"Id": "s1", "UserName": "Quux", "Client": "Zzyzx TV", "DeviceName": "Lounge TV",
				"NowPlayingItem":  map[string]any{"Id": "e1", "Name": "Pilot", "Type": "Episode", "SeriesName": "Zzyzx Road", "ParentIndexNumber": 1, "IndexNumber": 1, "RunTimeTicks": 72_000_000_000},
				"PlayState":       map[string]any{"PositionTicks": 18_000_000_000, "PlayMethod": "Transcode"},
				"TranscodingInfo": map[string]any{"Container": "ts", "VideoCodec": "h264", "AudioCodec": "aac", "IsVideoDirect": false, "IsAudioDirect": true, "TranscodeReasons": []string{"ContainerBitrateExceedsLimit"}},
			},
			{
				"Id": "s2", "UserName": "Plugh", "Client": "Zzyzx Phone", "DeviceName": "Phone",
				"NowPlayingItem": map[string]any{"Id": "m2", "Name": "Zzyzx Falling", "Type": "Movie", "RunTimeTicks": 36_000_000_000},
				"PlayState":      map[string]any{"PositionTicks": 0, "IsPaused": true, "PlayMethod": "DirectPlay"},
			},
			{"Id": "s3", "Client": "Zzyzx Web", "DeviceName": "Browser"},
		})
	})

	return s
}

// One call says what is running, what failed, who is playing what and how,
// and what the end of the log shows, with the counts in one line.
func TestServerHealth(t *testing.T) {
	t.Parallel()

	out := mustCall(t, session(t, healthServer(t).fakeServer, Options{}), "server_health", map[string]any{})
	if out["backend"] != "emby" || out["server_name"] != "Zzyzx" || out["server_version"] != "4.10.1.0" || !boolean(t, out["pending_restart"], "pending_restart") || boolean(t, out["update_available"], "update_available") {
		t.Errorf("the server = %v", out)
	}
	if want := "1 task running; 1 task failed on the last run; 2 playing of 3 devices connected, 1 re-encoded; in the last 15m0s of the log: 1 error of 1 kind, 1 gap of 30s or more, 1 slow answer and 1 request waiting; a restart is pending"; out["summary"] != want {
		t.Errorf("summary = %q\nwant      %q", out["summary"], want)
	}

	tasks := object(t, out["tasks"], "tasks")
	running, failed := objects(t, tasks["running"], "running"), objects(t, tasks["failed"], "failed")
	if len(running) != 1 || running[0]["name"] != "Scan media library" || running[0]["id"] != "t1" || running[0]["state"] != "Running" || running[0]["progress"] != 33.3 {
		t.Errorf("running = %v", running)
	}
	if len(failed) != 1 || failed[0]["name"] != "Calculate statistics" || failed[0]["last_status"] != "Failed" || failed[0]["last_run"] != "2026-01-05T09:39:12.0000000Z" || !strings.Contains(text(failed[0]["error"]), "being used by another process") || failed[0]["progress"] != nil {
		t.Errorf("failed = %v", failed)
	}

	sessions := object(t, out["sessions"], "sessions")
	playing := objects(t, sessions["playing"], "playing")
	if number(t, sessions["connected"], "connected") != 3 || number(t, sessions["transcoding"], "transcoding") != 1 || len(playing) != 2 {
		t.Fatalf("sessions = %v", sessions)
	}
	if p := playing[0]; p["now_playing"] != "Zzyzx Road S01E01 Pilot" || p["user"] != "Quux" || p["device"] != "Lounge TV" || p["play_method"] != "Transcode" || p["progress"] != 25.0 || p["position"] != "30m0s / 2h0m0s" || !strings.Contains(text(p["transcoding"]), "ContainerBitrateExceedsLimit") {
		t.Errorf("the one re-encoded = %v", p)
	}
	if p := playing[1]; p["now_playing"] != "Zzyzx Falling" || !boolean(t, p["paused"], "paused") || p["play_method"] != "DirectPlay" || p["transcoding"] != nil {
		t.Errorf("the one played as it is = %v", p)
	}

	log := object(t, out["log"], "log")
	levels := object(t, log["by_level"], "by_level")
	if !slices.Equal(texts(log["files"]), []string{"embyserver.txt"}) || log["from"] != "2026-01-05 08:32:23.118" || log["to"] != "2026-01-05 08:34:54.000" || number(t, log["entries"], "entries") != 8 || levels["info"] != 6.0 || levels["error"] != 1.0 || levels["warn"] != 1.0 || log["error"] != nil {
		t.Errorf("the log read = %v", log)
	}
	// an Emby line names no instant, so how long the log has been quiet is from the file's date: a made-up one long past
	if quiet, err := time.ParseDuration(text(log["quiet_for"])); err != nil || quiet < 24*time.Hour {
		t.Errorf("quiet_for = %v, %v", log["quiet_for"], err)
	}
	errs := objects(t, log["errors"], "errors")
	if number(t, log["error_kinds"], "error_kinds") != 1 || len(errs) != 1 || errs[0]["pattern"] != "ImageService: Error processing request" || errs[0]["count"] != 1.0 {
		t.Errorf("errors = %v", errs)
	}
	gaps := objects(t, log["gaps"], "gaps")
	if number(t, log["gaps_found"], "gaps_found") != 1 || len(gaps) != 1 || gaps[0]["from"] != "2026-01-05 08:32:40.404" || gaps[0]["to"] != "2026-01-05 08:34:53.101" {
		t.Errorf("gaps = %v", gaps)
	}
	slowest, waiting := objects(t, log["slowest"], "slowest"), objects(t, log["waiting"], "waiting")
	if number(t, log["answers_timed"], "answers_timed") != 1 || number(t, log["slow_found"], "slow_found") != 1 || len(slowest) != 1 || slowest[0]["ms"] != 141832.0 || slowest[0]["path"] != "/emby/Sessions/Playing/Progress" {
		t.Errorf("slowest = %v", slowest)
	}
	if number(t, log["waiting_found"], "waiting_found") != 1 || len(waiting) != 1 || waiting[0]["path"] != "/emby/Items/9/Images/Primary" || waiting[0]["method"] != "GET" {
		t.Errorf("waiting = %v", waiting)
	}
	if strings.Contains(string(mustJSON(t, out)), "sekrit") {
		t.Errorf("a token from the log is in the answer: %v", out)
	}
}

// The stretch of log read and what counts as a gap or as slow are the
// caller's to set, and each list is cut at limit while its count stays whole.
func TestServerHealthAsAsked(t *testing.T) {
	t.Parallel()

	cs := session(t, healthServer(t).fakeServer, Options{})

	// the last ten seconds of the log: the slow answer and the line after it
	log := object(t, mustCall(t, cs, "server_health", map[string]any{"last": "10s"})["log"], "log")
	if number(t, log["entries"], "entries") != 2 || log["from"] != "2026-01-05 08:34:53.101" || number(t, log["gaps_found"], "gaps_found") != 0 || number(t, log["slow_found"], "slow_found") != 1 || number(t, log["error_kinds"], "error_kinds") != 0 {
		t.Errorf("the last 10s = %v", log)
	}
	// a second of silence is a gap, and nothing took three minutes
	out := mustCall(t, cs, "server_health", map[string]any{"gap_seconds": 1, "slow_seconds": 180, "limit": 2})
	log = object(t, out["log"], "log")
	if found := number(t, log["gaps_found"], "gaps_found"); found != 5 || len(objects(t, log["gaps"], "gaps")) != 2 || number(t, log["slow_found"], "slow_found") != 0 || number(t, log["waiting_found"], "waiting_found") != 0 {
		t.Errorf("gaps of a second, slow at three minutes = %v", log)
	}
	if !strings.Contains(text(out["summary"]), "5 gaps of 1s or more, 0 slow answers and 0 requests waiting") {
		t.Errorf("summary = %q", out["summary"])
	}

	for want, args := range map[string]map[string]any{
		`last "soon" is not a length of time`:      {"last": "soon"},
		"is more than the 24h0m0s one check reads": {"last": "48h"},
	} {
		if msg := mustRefuse(t, cs, "server_health", args); !strings.Contains(msg, want) {
			t.Errorf("%v refused with %q, want %q", args, msg, want)
		}
	}
}

// A log that cannot be read leaves the rest of the answer standing, and
// says so where the log's part would be.
func TestServerHealthWithoutTheLog(t *testing.T) {
	t.Parallel()

	s := newTaskServer(t)
	s.mux.HandleFunc("GET /System/Info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"ServerName": "Zzyzx", "Version": "4.10.1.0"})
	})
	s.mux.HandleFunc("GET /Sessions", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, []map[string]any{}) })
	s.mux.HandleFunc("GET /System/Logs/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(map[string]any{"Name": "embyserver.txt", "Size": 9, "DateCreated": "2026-01-05T00:00:01Z", "DateModified": "2026-01-05T08:34:54Z"}))
	})
	s.mux.HandleFunc("GET /System/Logs/{name}", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Access to the path is denied.", http.StatusForbidden)
	})

	out := mustCall(t, session(t, s.fakeServer, Options{}), "server_health", map[string]any{})
	log := object(t, out["log"], "log")
	if !strings.Contains(text(log["error"]), "403") || log["by_level"] != nil || log["quiet_for"] != nil {
		t.Errorf("the log's part = %v", log)
	}
	if running := objects(t, object(t, out["tasks"], "tasks")["running"], "running"); len(running) != 1 || out["summary"] != "1 task running; 1 task failed on the last run; 0 playing of 0 devices connected, 0 re-encoded; the log could not be read" {
		t.Errorf("the rest = %v", out)
	}
}

// On Jellyfin a log line names an instant, which is what quiet is counted
// from, and the answer says what its log cannot show.
func TestServerHealthOnJellyfin(t *testing.T) {
	t.Parallel()

	written := time.Now().Add(-90 * time.Second).UTC()
	log := "[" + written.Add(-2*time.Minute).Format("2006-01-02 15:04:05.000 -07:00") + "] [INF] [1] Example.Main: Startup complete\n" +
		"[" + written.Add(-time.Minute).Format("2006-01-02 15:04:05.000 -07:00") + "] [ERR] [7] Example.Tasks.TaskManager: Error executing Scheduled Task\nExample.Data.SqliteException (0x80004005): SQLite Error 5: 'database is locked'.\n" +
		"[" + written.Format("2006-01-02 15:04:05.000 -07:00") + "] [WRN] [7] Example.Main: Slow going\n"
	f := newFakeServer(t)
	f.jellyfin = true
	f.mux.HandleFunc("GET /System/Info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"ServerName": "Zzyzx", "Version": "12.2.0"})
	})
	f.mux.HandleFunc("GET /ScheduledTasks", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, []map[string]any{}) })
	f.mux.HandleFunc("GET /Sessions", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, []map[string]any{}) })
	f.mux.HandleFunc("GET /System/Logs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{{"Name": "log_" + written.Format("20060102") + ".log", "Size": len(log), "DateCreated": written.Add(-time.Hour).Format(time.RFC3339), "DateModified": "2001-01-01T00:00:00Z"}})
	})
	f.mux.HandleFunc("GET /System/Logs/Log", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, log) })

	out := mustCall(t, session(t, f, Options{}), "server_health", map[string]any{})
	got := object(t, out["log"], "log")
	// from the last line's own time, not the file's date, which here is years old
	if quiet, err := time.ParseDuration(text(got["quiet_for"])); err != nil || quiet < 85*time.Second || quiet > 10*time.Minute {
		t.Errorf("quiet_for = %v, %v: want about 1m30s", got["quiet_for"], err)
	}
	if number(t, got["entries"], "entries") != 3 || number(t, got["gaps_found"], "gaps_found") != 2 || number(t, got["error_kinds"], "error_kinds") != 1 || number(t, got["answers_timed"], "answers_timed") != 0 {
		t.Errorf("the log read = %v", got)
	}
	if out["backend"] != "jellyfin" || !strings.Contains(text(out["note"]), "Jellyfin logs an answer's time only when it was slow and debug logging is on") || !strings.Contains(text(out["summary"]), "0 tasks running; 0 tasks failed on the last run; 0 playing of 0 devices connected") {
		t.Errorf("on Jellyfin = %v", out)
	}
}
