// Package serverlog reads a media server's log as entries rather than lines,
// so a question about it - what happened between two times, how often, where
// it went quiet, what was slow - is answered from the whole file and not from
// whatever its last lines happen to be.
//
// Emby and Jellyfin write different lines, and both write entries of several:
// an Emby error report is a first line and then lines that each begin with a
// tab, a Jellyfin exception is a first line and then a stack with no prefix
// at all. A line that does not begin with a timestamp belongs to the entry
// before it, on both.
package serverlog

import (
	"bufio"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Format is whose log is being read.
type Format int

const (
	// Emby writes "2026-10-09 11:50:30.336 Info Source: message", with no
	// zone: the time is the server's own clock's.
	Emby Format = iota
	// Jellyfin writes "[2026-10-09 11:48:07.235 -07:00] [INF] [85] Source:
	// message", with the zone's offset on every line.
	Jellyfin
)

// Level is how serious an entry says it is.
type Level int

const (
	// Unknown is a level word this package does not know; it is kept as
	// written in Entry.LevelName and passes any level filter.
	Unknown Level = iota
	Debug
	Info
	Warn
	Error
	Fatal
)

func (l Level) String() string {
	return [...]string{"unknown", "debug", "info", "warn", "error", "fatal"}[l]
}

// ParseLevel reads a level as a caller names it: debug, info, warn (or
// warning), error, fatal. "" is no level asked for, which is Unknown.
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return Unknown, nil
	case "debug", "dbg", "verbose", "vrb":
		return Debug, nil
	case "info", "inf", "information":
		return Info, nil
	case "warn", "wrn", "warning":
		return Warn, nil
	case "error", "err":
		return Error, nil
	case "fatal", "ftl", "critical":
		return Fatal, nil
	default:
		return Unknown, errors.New("level " + s + " is not one of debug, info, warn, error, fatal")
	}
}

// Entry is one thing the server logged: its first line taken apart, and the
// lines that followed it without a timestamp of their own.
type Entry struct {
	// Line is where the entry starts in its file, counting from 1
	Line int
	// Time is the time as written. Zoned says whether the line gave the
	// zone (Jellyfin does); when it did not, Time holds the server's own
	// clock's reading and its zone is not known
	Time  time.Time
	Zoned bool
	// Level is the level read, and LevelName the word the server wrote
	Level     Level
	LevelName string
	// Source is what logged it: Emby's "TaskManager", Jellyfin's class name
	Source string
	// Request is Emby's connection and request number on a line about an
	// HTTP request ("0HABC123DEF45:00000379"), which ties a request's line
	// to its response's
	Request string
	Message string
	// More is every line after the first, as written but for a leading tab
	More []string
}

// Text is the entry on one line: its source and message, which is what a
// text filter is matched against, with the lines after the first.
func (e *Entry) Text() string {
	var b strings.Builder
	b.WriteString(e.Source)
	if e.Request != "" {
		b.WriteString("-" + e.Request)
	}
	b.WriteString(": ")
	b.WriteString(e.Message)
	for _, m := range e.More {
		b.WriteString("\n")
		b.WriteString(m)
	}

	return b.String()
}

// Stamp is the entry's time as the server wrote it, with the zone where the
// line gave one.
func (e *Entry) Stamp() string {
	if e.Zoned {
		return e.Time.Format("2006-01-02 15:04:05.000 -07:00")
	}

	return e.Time.Format("2006-01-02 15:04:05.000")
}

var (
	embyLine = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}) (\S+) (.*)$`)
	// an Emby source is one word, with the connection and request number
	// after a dash on a line about an HTTP request
	embySource   = regexp.MustCompile(`^([^\s:]+?)(?:-([0-9A-Z]+:[0-9A-F]+))?: ?(.*)$`)
	jellyfinLine = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3} [+-]\d{2}:\d{2})\] \[(\w+)\] \[\d+\] (.*)$`)
	jellyfinFrom = regexp.MustCompile(`^([^\s:]+): ?(.*)$`)
)

const (
	embyStamp     = "2006-01-02 15:04:05.000"
	jellyfinStamp = "2006-01-02 15:04:05.000 -07:00"
)

// maxLine is how much of one line is kept: the start of a longer one, a
// request or a stack dumped on a single line, marked "..."
const maxLine = 16 << 10

// maxMore is how many lines after the first an entry keeps; a longer one
// says how many it left out as its last line.
const maxMore = 400

// first reads a line that starts an entry, or says it starts none.
func first(f Format, line string) (*Entry, bool) {
	if f == Jellyfin {
		m := jellyfinLine.FindStringSubmatch(line)
		if m == nil {
			return nil, false
		}
		at, err := time.Parse(jellyfinStamp, m[1])
		if err != nil {
			return nil, false
		}
		e := &Entry{Time: at, Zoned: true, LevelName: m[2], Message: m[3]}
		e.Level, _ = ParseLevel(m[2])
		if s := jellyfinFrom.FindStringSubmatch(m[3]); s != nil {
			e.Source, e.Message = s[1], s[2]
		}

		return e, true
	}

	m := embyLine.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	at, err := time.Parse(embyStamp, m[1])
	if err != nil {
		return nil, false
	}
	e := &Entry{Time: at, LevelName: m[2], Message: m[3]}
	e.Level, _ = ParseLevel(m[2])
	if s := embySource.FindStringSubmatch(m[3]); s != nil {
		e.Source, e.Request, e.Message = s[1], s[2], s[3]
	}

	return e, true
}

// Scan reads a log to its end and hands each entry to fn, in the order
// written. Lines before the first entry - a file that starts mid-entry -
// are dropped. A fn that returns an error ends the read with that error;
// ErrStop ends it with none.
func Scan(r io.Reader, f Format, fn func(*Entry) error) error {
	var (
		cur  *Entry
		left int
		n    int
	)
	flush := func() error {
		if cur == nil {
			return nil
		}
		if left > 0 {
			cur.More = append(cur.More, "... and "+plural(left, "more line"))
		}
		e := cur
		cur, left = nil, 0

		return fn(e)
	}

	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := readLine(br)
		if err != nil && line == "" {
			if errors.Is(err, io.EOF) {
				break
			}

			return err
		}
		n++
		if e, ok := first(f, line); ok {
			if ferr := flush(); ferr != nil {
				if errors.Is(ferr, ErrStop) {
					return nil
				}

				return ferr
			}
			e.Line = n
			cur = e
		} else if cur != nil {
			// Emby ends an error report with a line holding only a tab
			if more := strings.TrimPrefix(line, "\t"); strings.TrimSpace(more) != "" {
				if len(cur.More) < maxMore {
					cur.More = append(cur.More, more)
				} else {
					left++
				}
			}
		}
		if err != nil {
			break
		}
	}
	if ferr := flush(); ferr != nil && !errors.Is(ferr, ErrStop) {
		return ferr
	}

	return nil
}

// ErrStop is what a Scan's fn returns to end the read early, with no error.
var ErrStop = errors.New("stop reading the log")

// readLine reads one line without its ending, keeping only the start of one
// longer than maxLine. It returns io.EOF with the last line when the file
// does not end in a newline, and with "" after it.
func readLine(br *bufio.Reader) (string, error) {
	var (
		b   []byte
		cut bool
	)
	for {
		chunk, err := br.ReadSlice('\n')
		if !cut {
			b = append(b, chunk...)
			if len(b) > maxLine {
				b, cut = b[:maxLine], true
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		line := strings.TrimRight(string(b), "\r\n")
		if cut {
			line += "..."
		}

		return clean(line), err
	}
}

// zeroWidth are the invisible marks Emby wraps a host or an address in when
// it writes its log, so that it can blank them out on the way to a reader.
// A file read as it is on disk carries them, and with them an address does
// not match the pattern for one.
var zeroWidth = strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\u2060", "", "\ufeff", "")

// secret is a credential in a url's query, which a log holds for every
// request a client made with one.
var secret = regexp.MustCompile(`(?i)\b(api_key|apikey|x-emby-token|x-mediabrowser-token|token|access_token|password|pw)=([^&\s"'<>]+)`)

// clean takes the invisible marks out of a line and blanks the credentials
// in it, so that nothing this package hands on can carry one.
func clean(line string) string {
	line = zeroWidth.Replace(line)
	if strings.Contains(line, "=") {
		line = secret.ReplaceAllString(line, "$1=***")
	}

	return line
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}

	return strconv.Itoa(n) + " " + what + "s"
}
