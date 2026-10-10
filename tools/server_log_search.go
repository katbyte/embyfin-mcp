package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/serverlog"
	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	logModeLines     = "lines"
	logModeCount     = "count"
	logModeHistogram = "histogram"
	logModeGaps      = "gaps"
	logModeSlow      = "slow"
)

const (
	// logFilesMost is how many log files one search reads: each is read
	// whole, and a busy day's is a hundred megabytes
	logFilesMost = 8
	// logWindowSlack is how far either side of a window a file may be dated
	// and still be read for it. A file's dates are instants and an Emby
	// log's lines carry no zone, so a window given by the log's clock can
	// be up to fourteen hours from the instants it names
	logWindowSlack = 15 * time.Hour
	// logLastMost is how many entries the stretch before a log's end may
	// hold when it is asked for by its length
	logLastMost = 400000
	// logPastUntil is how far past a window's end the reading of a file
	// goes before it stops: lines are written a little out of order
	logPastUntil = 5 * time.Minute
)

// embyTimedNote is what Emby's log can show of how long answers took, as
// seen on 4.10.1: a GET asked of a server at its default logging left no
// line at all, a POST its request and its timed answer, and with debug
// logging on the GET both as well, at debug level.
const embyTimedNote = "Emby logs a request that only reads (a GET) with debug logging on alone: with it off, the answers timed and the requests that can show as waiting are the ones it does log, those that change something (a POST)"

// logInstant reads a date the server gives for a log file.
func logInstant(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}

	return time.Time{}, false
}

// logsFor picks the server's own logs a search reads when it names none,
// oldest first: the newest alone, or with a window every one that could
// hold a part of it.
func logsFor(files []embyfin.LogFile, since, until time.Time, last time.Duration) []embyfin.LogFile {
	var own []embyfin.LogFile
	for i := range files {
		if files[i].ServerOwn() {
			own = append(own, files[i])
		}
	}
	slices.SortStableFunc(own, func(a, b embyfin.LogFile) int { return strings.Compare(a.DateModified, b.DateModified) })
	if len(own) == 0 {
		return nil
	}
	newest := own[len(own)-1]
	if since.IsZero() && until.IsZero() && last == 0 {
		return []embyfin.LogFile{newest}
	}
	slack := logWindowSlack
	if last > 0 {
		// the stretch before the newest log's end, which an earlier log
		// holds the start of when the server restarted inside it. It is
		// counted from an instant, the newest log's last writing, and a
		// file's dates are instants too: no zone comes into it, so an
		// earlier log is read only if it was still being written then
		if end, ok := logInstant(newest.DateModified); ok {
			since, slack = end.Add(-last), logPastUntil
		}
	}

	var picked []embyfin.LogFile
	for _, f := range own {
		written, ok := logInstant(f.DateModified)
		if ok && !since.IsZero() && written.Before(since.Add(-slack)) {
			continue // last written before the window opens
		}
		begun, ok := logInstant(f.DateCreated)
		if ok && !until.IsZero() && begun.After(until.Add(logWindowSlack)) {
			continue // begun after the window closes
		}
		picked = append(picked, f)
	}

	return picked
}

// logOffset is how far an Emby log's clock runs from UTC, judged from the
// file's last line against the instant the server says the file was last
// written: its lines carry no zone, and a server whose zone was changed has
// logs written both ways. "" when the two are not a whole quarter of an hour
// apart, to within two minutes, which is when it cannot be judged.
func logOffset(lastLine time.Time, modified string) string {
	written, ok := logInstant(modified)
	if !ok || lastLine.IsZero() {
		return ""
	}
	clock := time.Date(lastLine.Year(), lastLine.Month(), lastLine.Day(), lastLine.Hour(), lastLine.Minute(), lastLine.Second(), 0, time.UTC)
	apart := clock.Sub(written.UTC())
	offset := apart.Round(15 * time.Minute)
	if d := apart - offset; d > 2*time.Minute || d < -2*time.Minute || offset > 14*time.Hour || offset < -12*time.Hour {
		return ""
	}
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}

	return fmt.Sprintf("%s%02d:%02d", sign, int(offset.Hours()), int(offset.Minutes())%60)
}

type logFileRead struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"                 jsonschema:"file size in bytes, as the server lists it"`
	Entries   int    `json:"entries"              jsonschema:"entries read from it; an entry is a line and the lines after it that have no time of their own"`
	First     string `json:"first,omitempty"      jsonschema:"the time of the first entry read, as written"`
	Last      string `json:"last,omitempty"       jsonschema:"the time of the last entry read, as written"`
	UTCOffset string `json:"utc_offset,omitempty" jsonschema:"Emby only, whose lines carry no zone: how far this file's clock runs from UTC, judged from its last line against when the server says it was last written. Absent when the file was not read to its end or the two do not agree"`
	StoppedAt string `json:"stopped_at,omitempty" jsonschema:"set when the file was not read to its end, because its entries had passed until"`
}

type logMessageRow struct {
	Pattern string `json:"pattern" jsonschema:"what logged it and its message, with what differs from one time to the next taken out: N for a number, ID for an id, \"*\" for what was in quotes, ?* for a url's query"`
	Level   string `json:"level"`
	Count   int    `json:"count"`
	First   string `json:"first"`
	Last    string `json:"last"`
}

type logBucketRow struct {
	Start string `json:"start"`
	Count int    `json:"count"`
}

type logGapRow struct {
	From    string  `json:"from"`
	To      string  `json:"to"`
	Seconds float64 `json:"seconds"`
	Before  string  `json:"before"  jsonschema:"the last entry before the silence"`
	After   string  `json:"after"   jsonschema:"the first entry after it"`
}

type logAnswerRow struct {
	At     string `json:"at"`
	Millis int    `json:"ms"`
	Status int    `json:"status"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path"`
	Client string `json:"client,omitempty" jsonschema:"who asked, as the log names them: Emby's placeholder (host2) unless raw was asked for"`
}

type logSlowMinuteRow struct {
	Minute  string `json:"minute"`
	Count   int    `json:"count"`
	Slowest int    `json:"slowest_ms"`
}

type logWaitingRow struct {
	Since   string  `json:"since"`
	Seconds float64 `json:"seconds"          jsonschema:"how long it had waited by the last entry read"`
	Method  string  `json:"method"`
	Path    string  `json:"path"`
	Client  string  `json:"client,omitempty"`
}

type logSearchIn struct {
	Files    []string `json:"files,omitempty"     jsonschema:"log files to read, by name (server_log lists them). Empty reads the server's own log: the newest, and with since, until or last every earlier one that could hold a part of the window, since a server starts a new file at midnight and at each restart"`
	Since    string   `json:"since,omitempty"     jsonschema:"read entries from this time on: 2026-10-09 11:50, or to the second. With no zone it is the log's own clock, as its lines are written; Jellyfin's lines carry a zone, so a time with one (2026-10-09T11:50:00-07:00) is held against them as an instant. Emby's carry none, so give Emby the log's clock"`
	Until    string   `json:"until,omitempty"     jsonschema:"read entries up to this time, written as since is"`
	Last     string   `json:"last,omitempty"      jsonschema:"instead of since: the stretch before the log's last entry, as 30m, 2h or 1h30m. It is counted back from the log's own end, so no zone comes into it"`
	Include  []string `json:"include,omitempty"   jsonschema:"keep only entries matching every one of these, as regular expressions with case ignored, matched against what logged the entry, its message and the lines under it"`
	Exclude  []string `json:"exclude,omitempty"   jsonschema:"drop entries matching any of these, written as include is"`
	Level    string   `json:"level,omitempty"     jsonschema:"keep entries at this level or worse: debug, info, warn, error or fatal"`
	Mode     string   `json:"mode,omitempty"      jsonschema:"what to answer with. lines (default): the entries. count: how many, by level and by message, the same message logged many times being one row. histogram: how many in each minute or hour. gaps: the stretches in which nothing was logged, which is how a server that stopped answering shows. slow: the answers that took long, and the requests still waiting for one"`
	Bucket   string   `json:"bucket,omitempty"    jsonschema:"histogram: the width of each stretch, minute (default), hour, or a length such as 10s or 5m"`
	Seconds  float64  `json:"seconds,omitempty"   jsonschema:"gaps: the least silence that counts, default 30. slow: the least an answer took or a request has waited, default 1"`
	Limit    int      `json:"limit,omitempty"     jsonschema:"how many rows to answer with, default 100, at most 1000: entries, messages, gaps (the longest), slow answers (the slowest)"`
	MaxChars int      `json:"max_chars,omitempty" jsonschema:"lines: how much text to answer with at most, default 40000, at most 90000"`
	Newest   bool     `json:"newest,omitempty"    jsonschema:"lines: keep the last entries that matched rather than the first"`
	Expand   bool     `json:"expand,omitempty"    jsonschema:"lines: give each entry whole. By default an entry of several lines - an error report, a stack - is its first line, the line that says what went wrong, and how many lines were left out"`
	Raw      bool     `json:"raw,omitempty"       jsonschema:"Emby only: read the log as it is on disk, naming each client's address and the host it asked for. By default Emby replaces them with placeholders (host1, host2), so clients cannot be told apart. Tokens and API keys are blanked either way"`
}

type logSearchOut struct {
	Mode     string        `json:"mode"`
	Files    []logFileRead `json:"files"               jsonschema:"the files read, oldest first, with the first and last time read from each"`
	Scanned  int           `json:"entries_scanned"`
	Matched  int           `json:"matched"             jsonschema:"entries in the window that passed every filter"`
	Lines    []string      `json:"lines,omitempty"`
	NotShown int           `json:"not_shown,omitempty" jsonschema:"lines: entries that matched and are not in lines"`

	ByLevel       map[string]int  `json:"by_level,omitempty"`
	Messages      []logMessageRow `json:"messages,omitempty"       jsonschema:"count: the messages logged most, most first"`
	MessagesTotal *int            `json:"messages_total,omitempty" jsonschema:"count: how many different messages there were"`

	Bucket  string         `json:"bucket,omitempty"`
	Buckets []logBucketRow `json:"buckets,omitempty" jsonschema:"histogram: each stretch that held an entry, in order; one with none is left out"`

	Gaps      []logGapRow `json:"gaps,omitempty"       jsonschema:"gaps: the longest, in the order they happened"`
	GapsFound *int        `json:"gaps_found,omitempty" jsonschema:"gaps: how many there were"`

	AnswersTimed *int               `json:"answers_timed,omitempty" jsonschema:"slow: how many answers the log gave a time for, slow or not"`
	SlowFound    *int               `json:"slow_found,omitempty"    jsonschema:"slow: how many took at least seconds"`
	Slowest      []logAnswerRow     `json:"slowest,omitempty"       jsonschema:"slow: the slowest answers, slowest first"`
	SlowMinutes  []logSlowMinuteRow `json:"slow_minutes,omitempty"  jsonschema:"slow: each minute that held a slow answer, with how many and the slowest"`
	Waiting      []logWaitingRow    `json:"waiting,omitempty"       jsonschema:"slow, Emby only: requests the log shows arriving and no answer to by the last entry read, which had waited at least seconds: what is hanging"`
	WaitingFound *int               `json:"waiting_found,omitempty" jsonschema:"slow: how many requests were still waiting"`

	Note string `json:"note,omitempty"`
}

// logSearch is one search under way: what it was asked, and what it has
// gathered.
type logSearch struct {
	mode   string
	filter serverlog.Filter
	last   time.Duration
	lines  serverlog.Lines
	counts serverlog.Counts
	hist   serverlog.Histogram
	gaps   serverlog.Gaps
	slow   serverlog.Slow

	scanned, matched int
	// held is the entries of the stretch before the log's end, kept until
	// the end is known (last)
	held []*serverlog.Entry
}

// offer takes an entry that is in the window and passes the filters.
func (s *logSearch) offer(e *serverlog.Entry) {
	s.matched++
	switch s.mode {
	case logModeCount:
		s.counts.Add(e)
	case logModeHistogram:
		s.hist.Add(e)
	case logModeGaps:
		s.gaps.Add(e)
	case logModeSlow:
		s.slow.Add(e)
	default:
		s.lines.Add(e)
	}
}

// add takes the next entry read. With last asked for, the entries of the
// stretch before the newest read so far are held until the log's end is
// known, and the older ones let go.
func (s *logSearch) add(e *serverlog.Entry) error {
	s.scanned++
	if s.last == 0 {
		if s.filter.Match(e) {
			s.offer(e)
		}

		return nil
	}
	s.held = append(s.held, e)
	// an entry is let go once it is further than last behind the newest;
	// a quarter of the slice at a time, so letting go costs little
	if len(s.held)%4096 == 0 {
		s.trim(e.Time)
	}
	if len(s.held) > logLastMost {
		s.trim(e.Time)
		if len(s.held) > logLastMost {
			return fmt.Errorf("the last %s of the log holds more than %d entries: ask for a shorter stretch, or give since and until", s.last, logLastMost)
		}
	}

	return nil
}

func (s *logSearch) trim(newest time.Time) {
	from := 0
	for from < len(s.held) && newest.Sub(s.held[from].Time) > s.last+time.Minute {
		from++
	}
	s.held = slices.Delete(s.held, 0, from)
}

// finish offers the entries held for last, now that the log's end is known.
func (s *logSearch) finish() {
	if s.last == 0 || len(s.held) == 0 {
		return
	}
	end := s.held[len(s.held)-1]
	for _, e := range s.held {
		if end.Time.Sub(e.Time) <= s.last && s.filter.Match(e) {
			s.offer(e)
		}
	}
	s.held = nil
}

func compileLogPatterns(which string, raw []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(raw))
	for _, p := range raw {
		if strings.TrimSpace(p) == "" {
			continue
		}
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			return nil, fmt.Errorf("%s %q is not a regular expression: %w", which, p, err)
		}
		out = append(out, re)
	}

	return out, nil
}

// newLogSearch reads what a search was asked.
func newLogSearch(in *logSearchIn, emby bool) (*logSearch, error) {
	s := &logSearch{mode: strings.ToLower(strings.TrimSpace(in.Mode))}
	switch s.mode {
	case "":
		s.mode = logModeLines
	case logModeLines, logModeCount, logModeHistogram, logModeGaps, logModeSlow:
	default:
		return nil, fmt.Errorf("mode %q is not one of %s, %s, %s, %s, %s", in.Mode, logModeLines, logModeCount, logModeHistogram, logModeGaps, logModeSlow)
	}

	var err error
	if s.filter.Include, err = compileLogPatterns("include", in.Include); err != nil {
		return nil, err
	}
	if s.filter.Exclude, err = compileLogPatterns("exclude", in.Exclude); err != nil {
		return nil, err
	}
	if s.filter.MinLevel, err = serverlog.ParseLevel(in.Level); err != nil {
		return nil, err
	}
	if in.Since != "" {
		if s.filter.Since, s.filter.SinceZoned, err = serverlog.ParseTime(in.Since); err != nil {
			return nil, fmt.Errorf("since: %w", err)
		}
	}
	if in.Until != "" {
		if s.filter.Until, s.filter.UntilZoned, err = serverlog.ParseTime(in.Until); err != nil {
			return nil, fmt.Errorf("until: %w", err)
		}
	}
	if emby && (s.filter.SinceZoned || s.filter.UntilZoned) {
		return nil, errors.New("a time with a zone cannot be held against Emby's log lines, which carry none: give since and until as the log's own clock reads (2026-10-09 11:50), which files' first and last show")
	}
	if !s.filter.Since.IsZero() && !s.filter.Until.IsZero() && s.filter.SinceZoned == s.filter.UntilZoned && s.filter.Until.Before(s.filter.Since) {
		return nil, fmt.Errorf("until (%s) is before since (%s)", in.Until, in.Since)
	}
	if in.Last != "" {
		if in.Since != "" || in.Until != "" {
			return nil, errors.New("last is the stretch before the log's end: give it, or since and until, not both")
		}
		if s.last, err = time.ParseDuration(strings.ReplaceAll(in.Last, " ", "")); err != nil || s.last <= 0 {
			return nil, fmt.Errorf("last %q is not a length of time such as 30m, 2h or 1h30m", in.Last)
		}
	}

	limit := in.Limit
	switch {
	case limit <= 0:
		limit = 100
	case limit > 1000:
		limit = 1000
	}
	chars := in.MaxChars
	switch {
	case chars <= 0:
		chars = 40000
	case chars > 90000:
		chars = 90000
	}
	s.lines = serverlog.Lines{Limit: limit, MaxChars: chars, Newest: in.Newest, Expand: in.Expand}

	least := func(byDefault float64) time.Duration {
		if in.Seconds > 0 {
			return time.Duration(in.Seconds * float64(time.Second))
		}

		return time.Duration(byDefault * float64(time.Second))
	}
	s.gaps = serverlog.Gaps{Least: least(30), Keep: limit}
	s.slow = serverlog.Slow{Least: least(1), Keep: limit}

	s.hist.Width = time.Minute
	switch b := strings.ToLower(strings.TrimSpace(in.Bucket)); b {
	case "", "minute", "minutes", "min":
	case "hour", "hours":
		s.hist.Width = time.Hour
	default:
		if s.hist.Width, err = time.ParseDuration(b); err != nil || s.hist.Width < time.Second {
			return nil, fmt.Errorf("bucket %q is not minute, hour or a length of at least a second such as 10s or 5m", in.Bucket)
		}
	}

	return s, nil
}

// readLog reads one file through the search and says what was read of it.
func (s *logSearch) readLog(ctx context.Context, client *embyfin.Client, f embyfin.LogFile, raw bool) (logFileRead, error) {
	read := logFileRead{Name: f.Name, Size: f.Size}
	body, err := client.LogStream(ctx, f.Name, raw)
	if err != nil {
		return read, err
	}
	defer func() { _ = body.Close() }()

	format := serverlog.Jellyfin
	if client.Backend() == embyfin.Emby {
		format = serverlog.Emby
	}
	var lastLine time.Time
	if err = serverlog.Scan(body, format, func(e *serverlog.Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		if until := s.filter.Until; !until.IsZero() && s.filter.After(e) && s.filter.After(&serverlog.Entry{Time: e.Time.Add(-logPastUntil), Zoned: e.Zoned}) {
			read.StoppedAt = e.Stamp()

			return serverlog.ErrStop
		}
		read.Entries++
		if read.First == "" {
			read.First = e.Stamp()
		}
		read.Last, lastLine = e.Stamp(), e.Time

		return s.add(e)
	}); err != nil {
		return read, fmt.Errorf("reading log %s: %w", f.Name, err)
	}
	if format == serverlog.Emby && read.StoppedAt == "" {
		read.UTCOffset = logOffset(lastLine, f.DateModified)
	}

	return read, nil
}

func registerLogSearchTool(r *registry) {
	client := r.client
	add(r, readTool, &mcp.Tool{
		Name: "server_log_search",
		Description: "Search the server's log instead of reading its tail: entries between two times or in the last stretch before its end, filtered by text and level, and answered as the entries themselves, as counts (by level, and by message with the same message logged many times as one row), as a histogram by the minute or hour, as the gaps in which nothing was logged (how a freeze shows), or as the slow answers and the requests still waiting for one. " +
			"A window is searched across every log file that could hold it: a server starts a new file at midnight and at each restart. Each file is read whole through the server, which serves no part of one, so a wide window on a busy server takes a while; files says what was read, with each one's first and last time. " +
			"An entry is a line and the lines under it that have no time of their own, so an error report or a stack is one entry, given on one line unless expand is set. Tokens and API keys in a url are blanked. " +
			"Emby's lines carry no zone: its times are the server's own clock, and utc_offset on each file says how far that ran from UTC when it can be judged. Emby times each answer it logs, and at its default logging it logs a request that changes something (a POST) and not one that only reads (a GET), which debug logging adds; Jellyfin logs an answer's time only when it was slow and debug logging is on. So slow finding nothing does not mean nothing was slow.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in logSearchIn) (*mcp.CallToolResult, logSearchOut, error) {
		emby := client.Backend() == embyfin.Emby
		s, err := newLogSearch(&in, emby)
		if err != nil {
			return nil, logSearchOut{}, err
		}
		listed, err := client.LogFiles(ctx)
		if err != nil {
			return nil, logSearchOut{}, err
		}

		var files []embyfin.LogFile
		if len(in.Files) > 0 {
			for _, name := range in.Files {
				at := slices.IndexFunc(listed, func(f embyfin.LogFile) bool { return strings.EqualFold(f.Name, name) })
				if at < 0 {
					return nil, logSearchOut{}, fmt.Errorf("the server has no log file named %q: server_log lists the %d it has", name, len(listed))
				}
				files = append(files, listed[at])
			}
			slices.SortStableFunc(files, func(a, b embyfin.LogFile) int { return strings.Compare(a.DateModified, b.DateModified) })
		} else {
			files = logsFor(listed, s.filter.Since, s.filter.Until, s.last)
			if len(files) == 0 {
				return nil, logSearchOut{}, fmt.Errorf("none of the server's %d log files is its own log (embyserver.txt, log_<date>.log) or could hold that window: name the files to read (server_log lists them)", len(listed))
			}
		}
		if len(files) > logFilesMost {
			names := make([]string, 0, len(files))
			for _, f := range files {
				names = append(names, f.Name)
			}

			return nil, logSearchOut{}, fmt.Errorf("that would read %d log files, each whole, and one search reads at most %d: narrow the window, or name the files to read (%s)", len(files), logFilesMost, strings.Join(names, ", "))
		}

		out := logSearchOut{Mode: s.mode}
		for _, f := range files {
			read, err := s.readLog(ctx, client, f, in.Raw && emby)
			if err != nil {
				return nil, logSearchOut{}, err
			}
			out.Files = append(out.Files, read)
		}
		s.finish()
		out.Scanned, out.Matched = s.scanned, s.matched

		var notes []string
		switch s.mode {
		case logModeCount:
			out.ByLevel = s.counts.ByLevel
			out.MessagesTotal = new(s.counts.Patterns())
			for _, p := range s.counts.Top(s.lines.Limit) {
				out.Messages = append(out.Messages, logMessageRow(p))
			}
			if s.counts.Other > 0 {
				notes = append(notes, fmt.Sprintf("%d entries were of messages past the first %d different ones, and are counted in matched and by_level but in no row", s.counts.Other, s.counts.Patterns()))
			}
		case logModeHistogram:
			out.Bucket = s.hist.Width.String()
			buckets := s.hist.Buckets()
			if len(buckets) > s.lines.Limit {
				notes = append(notes, fmt.Sprintf("%d stretches held an entry and the first %d are given: widen bucket, narrow the window, or raise limit", len(buckets), s.lines.Limit))
				buckets = buckets[:s.lines.Limit]
			}
			for _, b := range buckets {
				out.Buckets = append(out.Buckets, logBucketRow(b))
			}
		case logModeGaps:
			out.GapsFound = new(s.gaps.Found)
			for _, g := range s.gaps.Kept() {
				out.Gaps = append(out.Gaps, logGapRow(g))
			}
			notes = append(notes, fmt.Sprintf("a gap is %s or more with no entry that passed the filters", s.gaps.Least))
		case logModeSlow:
			out.AnswersTimed, out.SlowFound = new(s.slow.Seen), new(s.slow.Found)
			for _, a := range s.slow.Slowest() {
				out.Slowest = append(out.Slowest, logAnswerRow{At: a.At, Millis: a.Millis, Status: a.Status, Method: a.Method, Path: a.Path, Client: a.Client})
			}
			for _, m := range s.slow.Minutes() {
				out.SlowMinutes = append(out.SlowMinutes, logSlowMinuteRow{Minute: m.Minute, Count: m.Count, Slowest: m.Slowest})
			}
			waiting, found := s.slow.Waiting()
			out.WaitingFound = new(found)
			for _, w := range waiting {
				out.Waiting = append(out.Waiting, logWaitingRow(w))
			}
			if emby {
				notes = append(notes, embyTimedNote)
			} else {
				notes = append(notes, "Jellyfin logs an answer's time only when it was slow and debug logging is on, and never a request before its answer: none found here does not mean none was slow, and nothing can show as waiting")
			}
		default:
			out.Lines = s.lines.Kept()
			if out.NotShown = s.matched - len(out.Lines); out.NotShown > 0 {
				why := "limit"
				if s.lines.Cut {
					why = "max_chars"
				}
				notes = append(notes, fmt.Sprintf("%d more matched and are not shown (%s): narrow the window, add include or exclude, or ask for mode count to see what they are", out.NotShown, why))
			}
		}
		if emby {
			notes = append(notes, "times are the server's own clock as its log writes them, with no zone")
		}
		if in.Raw && !emby {
			notes = append(notes, "raw is Emby's: Jellyfin hands its log out as it is on disk either way")
		}
		if s.scanned == 0 {
			notes = append(notes, "no entry was read: "+strconv.Itoa(len(files))+" file(s) held none in the form this server writes")
		}
		out.Note = strings.Join(notes, "; ")

		return nil, out, nil
	})
}
