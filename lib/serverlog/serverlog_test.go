package serverlog

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// Emby names an exception's type twice before its message.
const (
	notFoundType = "Example.Extensions.ResourceNotFoundException"
	notFound     = notFoundType + ": " + notFoundType + ": Thumbnail set not found."
	plainType    = "System.Exception"
	noThumbnails = plainType + ": " + plainType + ": No thumbnails created"
)

// embyLog is lines in the shapes Emby writes, every name, id and path made
// up: plain lines, a request and its answers, an error report whose later
// lines each begin with a tab and whose last holds only one, and a short
// report that goes straight to what went wrong.
const embyLog = "2026-01-05 08:32:23.118 Info TaskManager: Executing Calculate statistics for all users\n" +
	"2026-01-05 08:32:24.000 Info HttpClient: GET https://api.example.org/3/tv/2940/season/1?api_key=x_secret&language=en\n" +
	"2026-01-05 08:32:24.095 Info HttpClient: Http response 200 from https://api.example.org/3/tv/2940/season/1?api_key=x_secret&language=en after 95ms\n" +
	"2026-01-05 08:32:30.336 Info DynamicHlsService-0HABC123DEF45:00000379: http/1.1 GET http://host1/emby/videos/123456/hls1/main/1002.ts?PlaySessionId=0123456789abcdef0123456789abcdef. Source Ip: host2, Accept=*/*, Host=host1\n" +
	"2026-01-05 08:32:30.343 Info DynamicHlsService-0HABC123DEF45:00000379: http/1.1 Response 200 to host2. Time: 6ms. GET http://host1/emby/videos/123456/hls1/main/1002.ts?PlaySessionId=0123456789abcdef0123456789abcdef. \n" +
	"2026-01-05 08:32:31.000 Info PlaystateService-0HABC123DEF46:0000037A: http/1.1 POST http://host1/emby/Sessions/Playing/Progress?X-Emby-Client=Example Player&X-Emby-Device-Name=Living Room&X-Emby-Token=x_secret. Source Ip: host2, Accept=*/*\n" +
	"2026-01-05 08:32:32.000 Info ImageService-0HABC123DEF48:00000001: http/1.1 GET http://host1/emby/Items/9/Images/Primary. Source Ip: host3, Accept=*/*\n" +
	"2026-01-05 08:32:40.404 Error ImageService-0HABC123DEF47:00000004: Error processing request\n" +
	"\t*** Error Report ***\n" +
	"\tVersion: 4.10.1.0\n" +
	"\tCommand line: /system/EmbyServer.dll -programdata /config\n" +
	"\tOperating system: Linux version 6.1 (...)\n" +
	"\tOS/Process: x64/x64\n" +
	"\tFramework: .NET 8.0.1\n" +
	"\tRuntime: system/System.Private.CoreLib.dll\n" +
	"\tProcessor count: 16\n" +
	"\tData path: /config\n" +
	"\tApplication path: /system\n" +
	"\t" + notFound + "\n" +
	"\t   at Example.Api.Images.ImageService.GetThumbnailImageResult(ImageRequest request)\n" +
	"\t   at Example.Server.Services.ServiceController.GetTaskResult(Task task)\n" +
	"\tSource: Example.Api\n" +
	"\tTargetSite: Void MoveNext()\n" +
	"\t\n" +
	"2026-01-05 08:32:41.000 Error ChapterImagesTask: Error creating thumbnails for /media/x/Example (2012)/Example (2012).webm\n" +
	"\t*** Error Report ***\n" +
	"\t" + noThumbnails + " for /media/x/Example (2012)/Example (2012).webm 320\n" +
	"2026-01-05 08:34:53.101 Info PlaystateService-0HABC123DEF46:0000037A: http/1.1 Response 204 to host2. Time: 141832ms. POST http://host1/emby/Sessions/Playing/Progress?X-Emby-Client=Example Player&X-Emby-Device-Name=Living Room&X-Emby-Token=x_secret\n" +
	"2026-01-05 08:34:53.200 Warn App: Slow going\n" +
	"2026-01-05 08:34:54.000 Info TaskManager: Calculate statistics for all users Completed after 2 minute(s) and 31 seconds\n"

// jellyfinLog is the same for Jellyfin: every line carries the zone, an
// exception's lines have no prefix at all, and a database command is logged
// across several.
const jellyfinLog = `[2026-01-05 03:50:34.641 -07:00] [ERR] [71] Example.Server.ScheduledTasks.TaskManager: Error executing Scheduled Task
Example.Data.Sqlite.SqliteException (0x80004005): SQLite Error 5: 'database is locked'.
   at Example.Data.Sqlite.SqliteDataReader.NextResult()
   at Example.Server.ScheduledTasks.ScheduledTaskWorker.ExecuteInternal(TaskOptions options)
[2026-01-05 03:50:34.642 -07:00] [INF] [71] Example.Server.ScheduledTasks.TaskManager: "Extract Chapter Images" Failed after 110 minute(s) and 34 seconds
[2026-01-05 03:50:57.186 -07:00] [INF] [74] Example.Server.ScheduledTasks.TaskManager: "Scan Media Library" Completed after 500 minute(s) and 50 seconds
[2026-01-05 03:51:00.000 -07:00] [ERR] [51] Example.EntityFrameworkCore.Database.Command: Failed executing DbCommand ("9"ms) [Parameters=["@p0='?' (DbType = Guid)"], CommandType='Text', CommandTimeout='60']"
""INSERT INTO \"BaseItemImageInfos\" (\"Id\", \"Blurhash\")
VALUES (@p0, @p1);
SELECT changes();"
[2026-01-05 03:51:01.000 -07:00] [DBG] [12] Example.Api.Middleware.ResponseTimeMiddleware: Slow HTTP Response from "http://media.example.net/Items?userId=0123456789abcdef0123456789abcdef&api_key=abc123" to 10.9.9.12 in 0:00:01.2345678 with Status Code 200
[2026-01-05 03:51:02.000 -07:00] [WRN] [67] Example.XbmcMetadata.Providers.MovieNfoProvider: Trailer URL uses a deprecated format : "plugin://example"
[2026-01-05 03:51:03.000 -07:00] [INF] [10] Main: Jellyfin version: "12.2.0"
`

func entries(t *testing.T, f Format, log string) []*Entry {
	t.Helper()

	var out []*Entry
	if err := Scan(strings.NewReader(log), f, func(e *Entry) error {
		out = append(out, e)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return out
}

// An Emby log is read as entries: the time, level and source off the first
// line, the request's number off a line about one, and every line with a
// tab kept with the line before it. No credential a url carried comes out.
func TestScanEmby(t *testing.T) {
	t.Parallel()

	es := entries(t, Emby, embyLog)
	if len(es) != 12 {
		t.Fatalf("%d entries, want 12", len(es))
	}
	e := es[0]
	if e.Line != 1 || e.Zoned || e.Stamp() != "2026-01-05 08:32:23.118" || e.Level != Info || e.LevelName != "Info" || e.Source != "TaskManager" || e.Request != "" || e.Message != "Executing Calculate statistics for all users" {
		t.Errorf("the first entry = %+v", e)
	}
	if e := es[3]; e.Source != "DynamicHlsService" || e.Request != "0HABC123DEF45:00000379" || !strings.HasPrefix(e.Message, "http/1.1 GET http://host1/emby/videos/") {
		t.Errorf("a request's entry = %+v", e)
	}
	report := es[7]
	if report.Line != 8 || report.Level != Error || len(report.More) != 15 || report.More[0] != "*** Error Report ***" {
		t.Errorf("the error report: line %d, level %v, %d later lines, first %q", report.Line, report.Level, len(report.More), report.More)
	}
	if got := report.Cause(); got != notFound {
		t.Errorf("what the report says went wrong = %q", got)
	}
	if got := report.Render(false); got != "2026-01-05 08:32:40.404 Error ImageService-0HABC123DEF47:00000004: Error processing request | "+notFound+" (+15 lines)" {
		t.Errorf("the report on one line = %q", got)
	}
	if got := report.Render(true); strings.Count(got, "\n") != 15 || !strings.Contains(got, "\n\t   at Example.Api.Images.ImageService") {
		t.Errorf("the report whole = %q", got)
	}
	// a short report goes straight to what went wrong
	if got := es[8].Cause(); !strings.HasPrefix(got, noThumbnails) || es[9].Line != 28 {
		t.Errorf("the short report's cause = %q, and the entry after it starts at line %d", got, es[9].Line)
	}
	for _, e := range es {
		if text := e.Text(); strings.Contains(text, "x_secret") {
			t.Errorf("a credential came through: %s", text)
		}
	}
	if !strings.Contains(es[1].Message, "api_key=***&language=en") {
		t.Errorf("a blanked key took more than its value with it: %q", es[1].Message)
	}
}

// A Jellyfin log is read the same way: the zone off every line, the class
// that logged it, and the lines with no prefix kept with the line before.
func TestScanJellyfin(t *testing.T) {
	t.Parallel()

	es := entries(t, Jellyfin, jellyfinLog)
	if len(es) != 7 {
		t.Fatalf("%d entries, want 7", len(es))
	}
	e := es[0]
	if !e.Zoned || e.Stamp() != "2026-01-05 03:50:34.641 -07:00" || e.Level != Error || e.LevelName != "ERR" || e.Source != "Example.Server.ScheduledTasks.TaskManager" || e.Message != "Error executing Scheduled Task" || len(e.More) != 3 {
		t.Errorf("the first entry = %+v", e)
	}
	if got := e.Cause(); got != "Example.Data.Sqlite.SqliteException (0x80004005): SQLite Error 5: 'database is locked'." {
		t.Errorf("what went wrong = %q", got)
	}
	if e := es[3]; len(e.More) != 3 || e.More[2] != `SELECT changes();"` {
		t.Errorf("a command logged across lines = %q", e.More)
	}
	if e := es[4]; e.Level != Debug || strings.Contains(e.Message, "abc123") {
		t.Errorf("a slow answer's line = %+v", e)
	}
	if e := es[6]; e.Source != "Main" || e.Message != `Jellyfin version: "12.2.0"` {
		t.Errorf("the last entry = %+v", e)
	}
}

// What a reader can be handed that is no tidy log: lines before the first
// entry, a file with no newline at its end, Windows line ends, the marks
// Emby wraps an address in, a line far longer than any is kept, an entry of
// more lines than are kept, and nothing at all.
func TestScanOddInput(t *testing.T) {
	t.Parallel()

	es := entries(t, Emby, "\t   at the tail of an entry cut off\r\n2026-01-05 08:00:00.000 Info App: from \u200b10.9.9.12\u200c to \u200dhost\u2060\r\n\tmore\r\n2026-01-05 08:00:01.000 Info App: last")
	if len(es) != 2 || es[0].Line != 2 || es[0].Message != "from 10.9.9.12 to host" || !slices.Equal(es[0].More, []string{"more"}) || es[1].Message != "last" {
		t.Errorf("entries = %+v", es)
	}
	if empty := entries(t, Emby, ""); len(empty) != 0 {
		t.Errorf("an empty log gave %d entries", len(empty))
	}
	if other := entries(t, Jellyfin, embyLog); len(other) != 0 {
		t.Errorf("an Emby log read as Jellyfin's gave %d entries", len(other))
	}

	long := "2026-01-05 08:00:00.000 Info App: " + strings.Repeat("x", 3*maxLine) + "\n" + strings.Repeat("\tline\n", maxMore+25) + "2026-01-05 08:00:01.000 Info App: after\n"
	es = entries(t, Emby, long)
	if len(es) != 2 || len(es[0].Message) > maxLine+3 || !strings.HasSuffix(es[0].Message, "...") {
		t.Fatalf("a very long line: %d entries, the first of %d characters", len(es), len(es[0].Message))
	}
	if got := es[0].More; len(got) != maxMore+1 || got[maxMore] != "... and 25 more lines" {
		t.Errorf("an entry of %d lines kept %d, the last %q", maxMore+25, len(got), got[len(got)-1])
	}

	// a reader's error is the scan's, and so is the caller's own; ErrStop ends
	// a scan with neither
	failed := errors.New("the connection dropped")
	if err := Scan(strings.NewReader(embyLog), Emby, func(*Entry) error { return failed }); !errors.Is(err, failed) {
		t.Errorf("a failing fn = %v", err)
	}
	n := 0
	if err := Scan(strings.NewReader(embyLog), Emby, func(*Entry) error { n++; return ErrStop }); err != nil || n != 1 {
		t.Errorf("ErrStop after the first entry: %v after %d", err, n)
	}
}

func mustTime(t *testing.T, s string) (time.Time, bool) {
	t.Helper()

	at, zoned, err := ParseTime(s)
	if err != nil {
		t.Fatal(err)
	}

	return at, zoned
}

// A filter picks by time, level and text. A time with no zone is held
// against what the log's clock read; one with a zone is held against a
// zoned log's entry as an instant.
func TestFilter(t *testing.T) {
	t.Parallel()

	matched := func(f Format, log string, fl Filter) []int {
		var lines []int
		for _, e := range entries(t, f, log) {
			if fl.Match(e) {
				lines = append(lines, e.Line)
			}
		}

		return lines
	}

	var window Filter
	window.Since, window.SinceZoned = mustTime(t, "2026-01-05 08:32:40")
	window.Until, window.UntilZoned = mustTime(t, "2026-01-05 08:34:53.150")
	if got := matched(Emby, embyLog, window); !slices.Equal(got, []int{8, 25, 28}) {
		t.Errorf("entries in the window start at lines %v", got)
	}
	if got := matched(Emby, embyLog, Filter{MinLevel: Warn}); !slices.Equal(got, []int{8, 25, 29}) {
		t.Errorf("warnings and worse start at lines %v", got)
	}
	// text is matched against the whole entry, so what went wrong, on a
	// later line, finds its entry
	if got := matched(Emby, embyLog, Filter{Include: []*regexp.Regexp{regexp.MustCompile(`Thumbnail set not found`)}}); !slices.Equal(got, []int{8}) {
		t.Errorf("an entry found by a later line starts at %v", got)
	}
	if got := matched(Emby, embyLog, Filter{Include: []*regexp.Regexp{regexp.MustCompile(`HttpClient`), regexp.MustCompile(`GET`)}, Exclude: []*regexp.Regexp{regexp.MustCompile(`response`)}}); !slices.Equal(got, []int{2}) {
		t.Errorf("every include and no exclude: lines %v", got)
	}

	// 03:50:57 at -07:00 is 10:50:57 UTC: a zoned bound is an instant
	var zoned Filter
	zoned.Since, zoned.SinceZoned = mustTime(t, "2026-01-05T10:50:57Z")
	if got := matched(Jellyfin, jellyfinLog, zoned); !slices.Equal(got, []int{6, 7, 11, 12, 13}) {
		t.Errorf("entries from an instant on start at lines %v", got)
	}
	// and one with no zone is the log's own clock
	var local Filter
	local.Since, local.SinceZoned = mustTime(t, "2026-01-05 03:51")
	if got := matched(Jellyfin, jellyfinLog, local); !slices.Equal(got, []int{7, 11, 12, 13}) {
		t.Errorf("entries from 03:51 by the log's clock start at lines %v", got)
	}
	// in a log written in order, nothing past the upper bound can match
	var upTo Filter
	upTo.Until, upTo.UntilZoned = mustTime(t, "2026-01-05 03:50:40")
	if es := entries(t, Jellyfin, jellyfinLog); upTo.After(es[0]) || !upTo.After(es[2]) || (&Filter{}).After(es[2]) {
		t.Error("After does not tell an entry past the upper bound from one before it")
	}

	for _, bad := range []string{"", "yesterday", "11:50", "2026-13-01"} {
		if _, _, err := ParseTime(bad); err == nil {
			t.Errorf("ParseTime(%q) was read as a time", bad)
		}
	}
	for s, wantZoned := range map[string]bool{"2026-01-05": false, "2026-01-05 11:50": false, "2026-01-05T11:50:30": false, "2026-01-05 11:50:30.250": false, "2026-01-05 11:50 -07:00": true, "2026-01-05T11:50:30-07:00": true, "2026-01-05T11:50Z": true} {
		if _, zoned, err := ParseTime(s); err != nil || zoned != wantZoned {
			t.Errorf("ParseTime(%q): zoned %v, %v", s, zoned, err)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("ParseLevel took a level there is none of")
	}
	if l, err := ParseLevel("Warning"); err != nil || l != Warn {
		t.Errorf("ParseLevel(Warning) = %v, %v", l, err)
	}
}
