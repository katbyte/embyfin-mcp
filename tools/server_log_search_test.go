package tools

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/serverlog"
	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
)

// The logs of a made-up Emby server: yesterday's, closed at a restart, and
// today's, with a request answered slowly across a silence of two minutes
// and one never answered. Every name, id and path is invented.
const (
	embyOlderLog = "2026-01-04 23:10:00.000 Info TaskManager: Executing Scan media library\n" +
		"2026-01-04 23:59:59.000 Info App: Stopping\n"
	embyNewerLog = "2026-01-05 08:32:23.118 Info TaskManager: Executing Calculate statistics for all users\n" +
		"2026-01-05 08:32:24.000 Info HttpClient: GET https://api.example.org/3/tv/2940?api_key=sekrit&language=en\n" +
		"2026-01-05 08:32:25.000 Info HttpClient: GET https://api.example.org/3/tv/2941?api_key=sekrit&language=en\n" +
		"2026-01-05 08:32:31.000 Info PlaystateService-0HABC123DEF46:0000037A: http/1.1 POST http://host1/emby/Sessions/Playing/Progress?X-Emby-Token=sekrit. Source Ip: host2, Accept=*/*\n" +
		"2026-01-05 08:32:32.000 Info ImageService-0HABC123DEF48:00000001: http/1.1 GET http://host1/emby/Items/9/Images/Primary. Source Ip: host3, Accept=*/*\n" +
		"2026-01-05 08:32:40.404 Error ImageService-0HABC123DEF47:00000004: Error processing request\n" +
		"\t*** Error Report ***\n" +
		"\tVersion: 4.10.1.0\n" +
		"\tApplication path: /system\n" +
		"\tExample.NotFoundException: Thumbnail set not found.\n" +
		"\t   at Example.Api.ImageService.Get(ImageRequest request)\n" +
		"\t\n" +
		"2026-01-05 08:34:53.101 Info PlaystateService-0HABC123DEF46:0000037A: http/1.1 Response 204 to host2. Time: 141832ms. POST http://host1/emby/Sessions/Playing/Progress?X-Emby-Token=sekrit\n" +
		"2026-01-05 08:34:54.000 Warn App: Slow going\n"
)

func embyLogServer(t *testing.T) *fakeServer {
	t.Helper()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /System/Logs/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(
			map[string]any{"Name": "ffmpeg-transcode-0badc0de.txt", "Size": 5, "DateCreated": "2026-01-05T08:35:00Z", "DateModified": "2026-01-05T08:36:00Z"},
			map[string]any{"Name": "embyserver.txt", "Size": len(embyNewerLog), "DateCreated": "2026-01-05T00:00:01Z", "DateModified": "2026-01-05T08:34:54Z"},
			map[string]any{"Name": "embyserver-63900000000.txt", "Size": len(embyOlderLog), "DateCreated": "2026-01-04T00:00:01Z", "DateModified": "2026-01-04T23:59:59Z"},
			map[string]any{"Name": "embyserver-63800000000.txt", "Size": 1, "DateCreated": "2025-12-01T00:00:01Z", "DateModified": "2025-12-01T23:59:59Z"},
		))
	})
	f.mux.HandleFunc("GET /System/Logs/{name}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("name") {
		case "embyserver.txt":
			_, _ = io.WriteString(w, embyNewerLog)
		case "embyserver-63900000000.txt":
			_, _ = io.WriteString(w, embyOlderLog)
		default:
			_, _ = io.WriteString(w, "not a log line\n")
		}
	})

	return f
}

func logNames(t *testing.T, out map[string]any) []string {
	t.Helper()

	files := objects(t, out["files"], "files")
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, text(f["name"]))
	}

	return names
}

func logLines(t *testing.T, out map[string]any) []string {
	t.Helper()

	return texts(out["lines"])
}

// With nothing asked, the search reads the server's own newest log and
// answers with its entries: an error report on one line, no credential, and
// how far the file's clock runs from UTC. Emby is asked for the anonymised
// log unless raw is set.
func TestLogSearchReadsTheNewestLog(t *testing.T) {
	t.Parallel()

	f := embyLogServer(t)
	cs := session(t, f, Options{})
	out := mustCall(t, cs, "server_log_search", map[string]any{})
	if got := logNames(t, out); !slices.Equal(got, []string{"embyserver.txt"}) {
		t.Fatalf("files read = %v, want the newest of the server's own", got)
	}
	file := objects(t, out["files"], "files")[0]
	if number(t, file["entries"], "entries") != 8 || file["first"] != "2026-01-05 08:32:23.118" || file["last"] != "2026-01-05 08:34:54.000" || file["utc_offset"] != "+00:00" {
		t.Errorf("what was read of it = %v", file)
	}
	lines := logLines(t, out)
	if out["mode"] != "lines" || len(lines) != 8 || number(t, out["matched"], "matched") != 8 || number(t, out["entries_scanned"], "entries_scanned") != 8 {
		t.Fatalf("the search = %v", out)
	}
	if lines[5] != "2026-01-05 08:32:40.404 Error ImageService-0HABC123DEF47:00000004: Error processing request | Example.NotFoundException: Thumbnail set not found. (+5 lines)" {
		t.Errorf("the error report = %q", lines[5])
	}
	if all := strings.Join(lines, "\n"); strings.Contains(all, "sekrit") || !strings.Contains(all, "api_key=***&language=en") {
		t.Errorf("a credential came through, or more than one was blanked: %s", all)
	}
	if !strings.Contains(text(out["note"]), "the server's own clock") {
		t.Errorf("note = %q, want it to say whose clock the times are", out["note"])
	}
	if q := f.requests("/System/Logs/embyserver.txt"); len(q) != 1 || q[0].Query != "" {
		t.Errorf("the log was asked for as %v, want once and anonymised", q)
	}

	// raw asks Emby for the log as it is on disk, and expand gives entries whole
	f.reset()
	out = mustCall(t, cs, "server_log_search", map[string]any{"raw": true, "expand": true, "level": "error"})
	if q := f.requests("/System/Logs/embyserver.txt"); len(q) != 1 || q[0].Query != "Sanitize=false" {
		t.Errorf("the raw log was asked for as %v", q)
	}
	if lines := logLines(t, out); len(lines) != 1 || strings.Count(lines[0], "\n") != 5 || !strings.Contains(lines[0], "\n\t   at Example.Api.ImageService.Get") {
		t.Errorf("errors, whole = %q", lines)
	}
}

// A window is searched across every log that could hold it, oldest first,
// and no further into a file than the window's end.
func TestLogSearchReadsAWindowAcrossFiles(t *testing.T) {
	t.Parallel()

	cs := session(t, embyLogServer(t), Options{})
	out := mustCall(t, cs, "server_log_search", map[string]any{"since": "2026-01-04 23:30", "until": "2026-01-05 08:32:24.500"})
	if got := logNames(t, out); !slices.Equal(got, []string{"embyserver-63900000000.txt", "embyserver.txt"}) {
		t.Fatalf("files read = %v, want the two the window touches and not the one from a month before", got)
	}
	if lines := logLines(t, out); len(lines) != 3 || !strings.HasPrefix(lines[0], "2026-01-04 23:59:59.000 Info App: Stopping") || !strings.Contains(lines[2], "tv/2940") {
		t.Errorf("entries in the window = %q", lines)
	}
	// the second file was read past the window's end only by the lines
	// written within five minutes of it, which here is all of it
	if number(t, out["entries_scanned"], "entries_scanned") != 10 || number(t, out["matched"], "matched") != 3 {
		t.Errorf("scanned %v, matched %v", out["entries_scanned"], out["matched"])
	}

	// a window that ends early in a file stops the reading of it
	out = mustCall(t, cs, "server_log_search", map[string]any{"files": []any{"embyserver.txt"}, "until": "2026-01-05 08:20"})
	file := objects(t, out["files"], "files")[0]
	if number(t, file["entries"], "entries") != 0 || file["stopped_at"] != "2026-01-05 08:32:23.118" || file["utc_offset"] != nil || number(t, out["matched"], "matched") != 0 {
		t.Errorf("a window before the file's first line = %v", out)
	}

	// last is counted back from the log's own end
	out = mustCall(t, cs, "server_log_search", map[string]any{"last": "30s"})
	if lines := logLines(t, out); len(lines) != 2 || !strings.Contains(lines[0], "Time: 141832ms") || !strings.Contains(lines[1], "Slow going") {
		t.Errorf("the last 30 seconds = %q", lines)
	}
	// and an earlier log is read only when it was still being written inside the stretch: each is read whole, and a busy day's is large
	if names := logNames(t, out); !slices.Equal(names, []string{"embyserver.txt"}) {
		t.Errorf("the last 30 seconds read %v, want today's log alone", names)
	}
	out = mustCall(t, cs, "server_log_search", map[string]any{"last": "9h", "include": []any{"App: Stopping|TaskManager"}})
	if names, lines := logNames(t, out), logLines(t, out); !slices.Equal(names, []string{"embyserver-63900000000.txt", "embyserver.txt"}) || len(lines) != 2 || !strings.Contains(lines[0], "23:59:59.000 Info App: Stopping") || !strings.Contains(lines[1], "Calculate statistics") {
		t.Errorf("the last nine hours read %v and found %q, want yesterday's log too and the one line of it inside the stretch", names, lines)
	}
	out = mustCall(t, cs, "server_log_search", map[string]any{"last": "3m", "include": []any{"httpclient"}, "exclude": []any{"2941"}})
	if lines := logLines(t, out); len(lines) != 1 || !strings.Contains(lines[0], "tv/2940") {
		t.Errorf("the last three minutes, filtered = %q", lines)
	}
}

// The other answers a search gives: counts with the same message as one
// row, a histogram, the silence a freeze leaves, and the slow answer with
// the request still waiting.
func TestLogSearchModes(t *testing.T) {
	t.Parallel()

	cs := session(t, embyLogServer(t), Options{})

	out := mustCall(t, cs, "server_log_search", map[string]any{"mode": "count"})
	levels := object(t, out["by_level"], "by_level")
	if number(t, levels["info"], "info") != 6 || number(t, levels["error"], "error") != 1 || number(t, levels["warn"], "warn") != 1 || number(t, out["messages_total"], "messages_total") != 7 {
		t.Errorf("counts = %v", out)
	}
	if top := objects(t, out["messages"], "messages")[0]; top["pattern"] != "HttpClient: GET https://api.example.org/N/tv/N?*" || number(t, top["count"], "count") != 2 || top["first"] != "2026-01-05 08:32:24.000" || top["last"] != "2026-01-05 08:32:25.000" {
		t.Errorf("the message logged most = %v", top)
	}
	if _, ok := out["lines"]; ok {
		t.Error("a count answered with lines too")
	}

	out = mustCall(t, cs, "server_log_search", map[string]any{"mode": "histogram"})
	if b := objects(t, out["buckets"], "buckets"); out["bucket"] != "1m0s" || len(b) != 2 || b[0]["start"] != "2026-01-05 08:32" || number(t, b[0]["count"], "count") != 6 || number(t, b[1]["count"], "count") != 2 {
		t.Errorf("by the minute = %v", out)
	}
	out = mustCall(t, cs, "server_log_search", map[string]any{"mode": "histogram", "bucket": "hour"})
	if b := objects(t, out["buckets"], "buckets"); out["bucket"] != "1h0m0s" || len(b) != 1 || number(t, b[0]["count"], "count") != 8 {
		t.Errorf("by the hour = %v", out)
	}

	out = mustCall(t, cs, "server_log_search", map[string]any{"mode": "gaps"})
	gaps := objects(t, out["gaps"], "gaps")
	if number(t, out["gaps_found"], "gaps_found") != 1 || len(gaps) != 1 || gaps[0]["from"] != "2026-01-05 08:32:40.404" || gaps[0]["to"] != "2026-01-05 08:34:53.101" || !strings.Contains(text(gaps[0]["after"]), "Time: 141832ms") {
		t.Errorf("the silence = %v", out)
	}
	out = mustCall(t, cs, "server_log_search", map[string]any{"mode": "gaps", "seconds": 5})
	if number(t, out["gaps_found"], "gaps_found") != 3 || !strings.Contains(text(out["note"]), "5s or more") {
		t.Errorf("silences of five seconds = %v", out)
	}

	out = mustCall(t, cs, "server_log_search", map[string]any{"mode": "slow"})
	slowest := objects(t, out["slowest"], "slowest")
	if number(t, out["answers_timed"], "answers_timed") != 1 || number(t, out["slow_found"], "slow_found") != 1 || len(slowest) != 1 || number(t, slowest[0]["ms"], "ms") != 141832 || slowest[0]["path"] != "/emby/Sessions/Playing/Progress" || slowest[0]["method"] != "POST" || slowest[0]["client"] != "host2" {
		t.Errorf("the slow answer = %v", out)
	}
	waiting := objects(t, out["waiting"], "waiting")
	if number(t, out["waiting_found"], "waiting_found") != 1 || len(waiting) != 1 || waiting[0]["path"] != "/emby/Items/9/Images/Primary" || number(t, waiting[0]["seconds"], "seconds") != 142 || waiting[0]["client"] != "host3" {
		t.Errorf("the request still waiting = %v", out)
	}
	if m := objects(t, out["slow_minutes"], "slow_minutes"); len(m) != 1 || m[0]["minute"] != "2026-01-05 08:34" || number(t, m[0]["slowest_ms"], "slowest_ms") != 141832 {
		t.Errorf("slow answers by the minute = %v", m)
	}

	// more matched than are shown says how many, and why
	out = mustCall(t, cs, "server_log_search", map[string]any{"limit": 2})
	if lines := logLines(t, out); len(lines) != 2 || number(t, out["not_shown"], "not_shown") != 6 || !strings.Contains(text(out["note"]), "6 more matched and are not shown (limit)") {
		t.Errorf("two of eight = %v", out)
	}
	out = mustCall(t, cs, "server_log_search", map[string]any{"limit": 2, "newest": true})
	if lines := logLines(t, out); len(lines) != 2 || !strings.Contains(lines[1], "Slow going") {
		t.Errorf("the newest two of eight = %v", lines)
	}
}

// What a search refuses, each saying what to give instead.
func TestLogSearchRefusals(t *testing.T) {
	t.Parallel()

	cs := session(t, embyLogServer(t), Options{})
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"mode": "tail"}, `mode "tail" is not one of lines, count, histogram, gaps, slow`},
		{map[string]any{"include": []any{"("}}, `include "(" is not a regular expression`},
		{map[string]any{"level": "loud"}, "is not one of debug, info, warn, error, fatal"},
		{map[string]any{"since": "yesterday"}, "since: the time"},
		{map[string]any{"since": "2026-01-05T08:00:00Z"}, "cannot be held against Emby's log lines, which carry none"},
		{map[string]any{"since": "2026-01-05 09:00", "until": "2026-01-05 08:00"}, "is before since"},
		{map[string]any{"last": "30m", "since": "2026-01-05 08:00"}, "not both"},
		{map[string]any{"last": "soon"}, `last "soon" is not a length of time`},
		{map[string]any{"mode": "histogram", "bucket": "1ms"}, "is not minute, hour or a length of at least a second"},
		{map[string]any{"files": []any{"nope.txt"}}, `no log file named "nope.txt"`},
	} {
		if msg := mustRefuse(t, cs, "server_log_search", c.args); !strings.Contains(msg, c.want) {
			t.Errorf("%v refused with %q, want %q", c.args, msg, c.want)
		}
	}

	// a server that keeps many logs of its own is not read whole for a
	// window that touches them all
	f := newFakeServer(t)
	f.mux.HandleFunc("GET /System/Logs/Query", func(w http.ResponseWriter, _ *http.Request) {
		rows := make([]map[string]any, 0, logFilesMost+1)
		for i := range logFilesMost + 1 {
			rows = append(rows, map[string]any{"Name": "embyserver-" + string(rune('a'+i)) + ".txt", "DateCreated": "2026-01-05T00:00:00Z", "DateModified": "2026-01-05T01:00:00Z"})
		}
		writeJSON(t, w, page(rows...))
	})
	if msg := mustRefuse(t, session(t, f, Options{}), "server_log_search", map[string]any{"since": "2026-01-05 00:30"}); !strings.Contains(msg, "would read 9 log files") || !strings.Contains(msg, "embyserver-a.txt") {
		t.Errorf("nine files in the window refused with %q", msg)
	}
	// and one with none of its own says to name the files
	g := newFakeServer(t)
	g.mux.HandleFunc("GET /System/Logs/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(map[string]any{"Name": "ffmpeg-transcode-0badc0de.txt", "DateModified": "2026-01-05T01:00:00Z"}))
	})
	if msg := mustRefuse(t, session(t, g, Options{}), "server_log_search", map[string]any{}); !strings.Contains(msg, "name the files to read") {
		t.Errorf("no log of the server's own refused with %q", msg)
	}
}

// Jellyfin's lines carry a zone, so a time with one is held against them as
// an instant; it has no anonymised form, and logs no request before its
// answer, which the search says.
func TestLogSearchOnJellyfin(t *testing.T) {
	t.Parallel()

	const log = `[2026-01-05 03:50:34.641 -07:00] [ERR] [71] Example.Tasks.TaskManager: Error executing Scheduled Task
Example.Data.SqliteException (0x80004005): SQLite Error 5: 'database is locked'.
   at Example.Data.SqliteDataReader.NextResult()
[2026-01-05 03:50:57.186 -07:00] [INF] [74] Example.Tasks.TaskManager: "Scan Media Library" Completed after 500 minute(s) and 50 seconds
[2026-01-05 03:51:01.000 -07:00] [DBG] [12] Example.Api.ResponseTimeMiddleware: Slow HTTP Response from "http://media.example.net/Items?api_key=sekrit" to 10.9.9.12 in 0:00:01.2345678 with Status Code 200
`
	f := newFakeServer(t)
	f.jellyfin = true
	f.mux.HandleFunc("GET /System/Logs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{
			{"Name": "FFmpeg.Transcode-2026-01-05_0badc0de.log", "Size": 3, "DateCreated": "2026-01-05T11:00:00Z", "DateModified": "2026-01-05T11:05:00Z"},
			{"Name": "log_20260105.log", "Size": len(log), "DateCreated": "2026-01-05T07:00:00Z", "DateModified": "2026-01-05T10:51:01Z"},
		})
	})
	f.mux.HandleFunc("GET /System/Logs/Log", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "log_20260105.log" {
			http.NotFound(w, r)

			return
		}
		_, _ = io.WriteString(w, log)
	})
	cs := session(t, f, Options{})
	if got := f.client(t).Backend(); got != embyfin.Jellyfin {
		t.Fatalf("the fake is %s", got)
	}

	// 10:50:57 UTC is 03:50:57 by the log's clock
	out := mustCall(t, cs, "server_log_search", map[string]any{"since": "2026-01-05T10:50:57Z", "raw": true})
	lines := logLines(t, out)
	if len(lines) != 2 || !strings.HasPrefix(lines[0], `2026-01-05 03:50:57.186 -07:00 INF Example.Tasks.TaskManager: "Scan Media Library" Completed`) || strings.Contains(lines[1], "sekrit") {
		t.Errorf("entries from an instant on = %q", lines)
	}
	if note := text(out["note"]); !strings.Contains(note, "raw is Emby's") || strings.Contains(note, "own clock") {
		t.Errorf("note = %q", note)
	}
	if file := objects(t, out["files"], "files")[0]; file["utc_offset"] != nil || file["name"] != "log_20260105.log" {
		t.Errorf("the file read = %v", file)
	}
	if q := f.requests("/System/Logs/Log"); len(q) != 1 || q[0].Query != "name=log_20260105.log" {
		t.Errorf("the log was asked for as %v", q)
	}

	out = mustCall(t, cs, "server_log_search", map[string]any{"level": "error"})
	if lines := logLines(t, out); len(lines) != 1 || !strings.HasSuffix(lines[0], "Error executing Scheduled Task | Example.Data.SqliteException (0x80004005): SQLite Error 5: 'database is locked'. (+2 lines)") {
		t.Errorf("errors = %q", lines)
	}

	out = mustCall(t, cs, "server_log_search", map[string]any{"mode": "slow"})
	slowest := objects(t, out["slowest"], "slowest")
	if len(slowest) != 1 || number(t, slowest[0]["ms"], "ms") != 1234 || slowest[0]["path"] != "/Items" || slowest[0]["client"] != "10.9.9.12" || !strings.Contains(text(out["note"]), "only when it was slow and debug logging is on") {
		t.Errorf("Jellyfin's slow answers = %v", out)
	}
}

// How far an Emby log's clock runs from UTC is judged from its last line
// against when the server says the file was last written, and left unsaid
// when the two do not agree.
func TestLogOffset(t *testing.T) {
	t.Parallel()

	for stamp, want := range map[string]string{
		"2026-01-05 15:34:54.000": "+00:00",
		"2026-01-05 08:34:53.000": "-07:00",
		"2026-01-05 21:04:55.000": "+05:30",
		"2026-01-06 05:34:54.000": "+14:00",
		"2026-01-05 08:40:00.000": "", // five minutes out: not a zone's difference
		"2026-01-07 15:34:54.000": "", // two days: not this file's last line
	} {
		if got := logOffset(mustStamp(t, stamp), "2026-01-05T15:34:54.1234567Z"); got != want {
			t.Errorf("a last line of %s against a file written 15:34:54 UTC = %q, want %q", stamp, got, want)
		}
	}
	if got := logOffset(mustStamp(t, "2026-01-05 15:34:54.000"), "not a date"); got != "" {
		t.Errorf("a file with no date read = %q", got)
	}
}

func mustStamp(t *testing.T, s string) time.Time {
	t.Helper()

	at, _, err := serverlog.ParseTime(s)
	if err != nil {
		t.Fatal(err)
	}

	return at
}
