package serverlog

import (
	"cmp"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Filter picks the entries a question is about.
type Filter struct {
	// Since and Until bound the time, either left zero for no bound. An
	// entry is held against a bound as an instant when both carry a zone,
	// and otherwise by what the clock read: 11:50 is 11:50 as the log wrote
	// it, whatever zone that was
	Since, Until           time.Time
	SinceZoned, UntilZoned bool
	// Include keeps an entry only when every one matches its text, and
	// Exclude drops one that any matches
	Include, Exclude []*regexp.Regexp
	// MinLevel drops what is less serious; an entry whose level word is not
	// known is kept
	MinLevel Level
}

// wall is a time's clock reading with its zone taken off, for holding two
// readings against each other.
func wall(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

func before(a time.Time, aZoned bool, b time.Time, bZoned bool) bool {
	if aZoned && bZoned {
		return a.Before(b)
	}

	return wall(a).Before(wall(b))
}

// After reports whether the entry is past the filter's Until: in a log
// written in order, nothing after it can match.
func (f *Filter) After(e *Entry) bool {
	return !f.Until.IsZero() && before(f.Until, f.UntilZoned, e.Time, e.Zoned)
}

// Match reports whether the entry is one the filter asks for.
func (f *Filter) Match(e *Entry) bool {
	if !f.Since.IsZero() && before(e.Time, e.Zoned, f.Since, f.SinceZoned) {
		return false
	}
	if f.After(e) {
		return false
	}
	if e.Level != Unknown && e.Level < f.MinLevel {
		return false
	}
	if len(f.Include)+len(f.Exclude) == 0 {
		return true
	}
	text := e.Text()
	for _, re := range f.Include {
		if !re.MatchString(text) {
			return false
		}
	}
	for _, re := range f.Exclude {
		if re.MatchString(text) {
			return false
		}
	}

	return true
}

var timeLayouts = []struct {
	layout string
	zoned  bool
}{
	{"2006-01-02 15:04:05.000 -07:00", true},
	{"2006-01-02 15:04:05 -07:00", true},
	{"2006-01-02 15:04 -07:00", true},
	{time.RFC3339Nano, true},
	{"2006-01-02T15:04Z07:00", true},
	{"2006-01-02 15:04:05.000", false},
	{"2006-01-02 15:04:05", false},
	{"2006-01-02 15:04", false},
	{"2006-01-02T15:04:05.000", false},
	{"2006-01-02T15:04:05", false},
	{"2006-01-02T15:04", false},
	{"2006-01-02", false},
}

// ParseTime reads a time as a caller writes one: a date, a date and a time
// to the minute, second or millisecond, with a zone ("Z", "-07:00") or
// without. zoned says whether it gave one.
func ParseTime(s string) (t time.Time, zoned bool, err error) {
	s = strings.TrimSpace(s)
	for _, l := range timeLayouts {
		if t, err := time.Parse(l.layout, s); err == nil {
			return t, l.zoned, nil
		}
	}

	return time.Time{}, false, errors.New("the time " + strconv.Quote(s) + " is not a date and time such as 2026-10-09 11:50, 2026-10-09 11:50:30 or 2026-10-09T11:50:30-07:00")
}

// reportHeader are the lines an Emby error report opens with before it says
// what went wrong: the same on every report, and no part of any answer.
var reportHeader = []string{
	"*** Error Report ***", "Version:", "Command line:", "Operating system:", "OS/Process:", "Framework:",
	"Runtime:", "Processor count:", "Data path:", "Application path:",
}

// Cause is the line of an entry's later lines that says what went wrong:
// the exception and its message, which is the first line that is no part of
// an Emby report's header. "" when the entry is one line, or is all header.
func (e *Entry) Cause() string {
	for _, m := range e.More {
		line := strings.TrimSpace(m)
		if line == "" || slices.ContainsFunc(reportHeader, func(h string) bool { return strings.HasPrefix(line, h) }) {
			continue
		}

		return line
	}

	return ""
}

// Render is the entry as text: on one line, with what went wrong after it
// and how many lines were left out, or whole when expand is set.
func (e *Entry) Render(expand bool) string {
	var b strings.Builder
	b.WriteString(e.Stamp())
	b.WriteString(" ")
	b.WriteString(e.LevelName)
	b.WriteString(" ")
	b.WriteString(e.Source)
	if e.Request != "" {
		b.WriteString("-" + e.Request)
	}
	b.WriteString(": ")
	b.WriteString(e.Message)
	if len(e.More) == 0 {
		return b.String()
	}
	if expand {
		for _, m := range e.More {
			b.WriteString("\n\t")
			b.WriteString(m)
		}

		return b.String()
	}
	if cause := e.Cause(); cause != "" {
		b.WriteString(" | ")
		b.WriteString(cause)
	}
	b.WriteString(" (+" + plural(len(e.More), "line") + ")")

	return b.String()
}

var (
	quoted   = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'[^']*'`)
	query    = regexp.MustCompile(`\?[^\s"']*`)
	longID   = regexp.MustCompile(`\b[0-9A-Fa-f]{8}(?:-?[0-9A-Fa-f]{4}){3}-?[0-9A-Fa-f]{12}\b|\b[0-9A-Fa-f]{12,}\b|\b0H[0-9A-Z]{8,}(?::[0-9A-F]+)?\b`)
	number   = regexp.MustCompile(`\d+(?:[.,:]\d+)*`)
	spaceRun = regexp.MustCompile(`\s+`)
)

// maxPattern is how much of a message a pattern keeps.
const maxPattern = 160

// Pattern is the entry's first line with what differs from one occurrence
// to the next taken out - what is in quotes, a url's query, ids, numbers -
// so that the same message logged twenty thousand times is one row with a
// count, not twenty thousand rows.
func (e *Entry) Pattern() string {
	s := e.Message
	s = quoted.ReplaceAllString(s, `"*"`)
	s = query.ReplaceAllString(s, "?*")
	s = longID.ReplaceAllString(s, "ID")
	s = number.ReplaceAllString(s, "N")
	s = strings.TrimSpace(spaceRun.ReplaceAllString(s, " "))
	if len(s) > maxPattern {
		s = s[:maxPattern] + "..."
	}

	return e.Source + ": " + s
}

// Lines keeps the entries a search matched, as text: the first Limit of
// them, or the last when Newest is set, and never more than MaxChars of
// text.
type Lines struct {
	Limit, MaxChars int
	Newest, Expand  bool

	kept  []string
	chars int
	// Matched is every entry offered, kept or not
	Matched int
	// Cut says the text limit, not the count, is what stopped entries
	// being kept
	Cut bool
}

// Add offers an entry.
func (l *Lines) Add(e *Entry) {
	l.Matched++
	if !l.Newest && (len(l.kept) >= l.Limit || l.Cut) {
		return
	}
	text := e.Render(l.Expand)
	if !l.Newest && l.chars+len(text) > l.MaxChars && len(l.kept) > 0 {
		l.Cut = true

		return
	}
	l.kept = append(l.kept, text)
	l.chars += len(text) + 1
	if l.Newest {
		for len(l.kept) > l.Limit || l.chars > l.MaxChars && len(l.kept) > 1 {
			if len(l.kept) <= l.Limit {
				l.Cut = true
			}
			l.chars -= len(l.kept[0]) + 1
			l.kept = l.kept[1:]
		}
	}
}

// Kept is the entries kept, oldest first. One alone longer than MaxChars is
// kept cut short.
func (l *Lines) Kept() []string {
	if len(l.kept) == 1 && len(l.kept[0]) > l.MaxChars && l.MaxChars > 0 {
		l.Cut = true

		return []string{l.kept[0][:l.MaxChars] + "..."}
	}

	return l.kept
}

// LevelWord is the entry's level as counts name it: debug, info, warn, error
// or fatal, and a level this package does not know as the log wrote it, in
// lower case.
func (e *Entry) LevelWord() string {
	if e.Level == Unknown {
		return strings.ToLower(e.LevelName)
	}

	return e.Level.String()
}

// PatternCount is one message and how often it was logged.
type PatternCount struct {
	Pattern string
	Level   string
	Count   int
	// First and Last are when it was first and last logged, as written
	First, Last string
}

// maxPatterns is how many different messages a count tells apart; past it,
// new ones are counted together.
const maxPatterns = 5000

// Counts counts the entries a search matched: all of them, by level, and by
// message.
type Counts struct {
	Total   int
	ByLevel map[string]int
	// Other is the entries of messages past the maxPatterns told apart
	Other    int
	patterns map[string]*PatternCount
}

// Add counts an entry.
func (c *Counts) Add(e *Entry) {
	if c.ByLevel == nil {
		c.ByLevel, c.patterns = map[string]int{}, map[string]*PatternCount{}
	}
	c.Total++
	level := e.LevelWord()
	c.ByLevel[level]++

	key := e.Pattern()
	p := c.patterns[key]
	if p == nil {
		if len(c.patterns) >= maxPatterns {
			c.Other++

			return
		}
		p = &PatternCount{Pattern: key, Level: level, First: e.Stamp()}
		c.patterns[key] = p
	}
	p.Count++
	p.Last = e.Stamp()
}

// Top is the n messages logged most often, most first.
func (c *Counts) Top(n int) []PatternCount {
	out := make([]PatternCount, 0, len(c.patterns))
	for _, p := range c.patterns {
		out = append(out, *p)
	}
	slices.SortFunc(out, func(a, b PatternCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Pattern, b.Pattern))
	})
	if len(out) > n {
		out = out[:n]
	}

	return out
}

// Patterns is how many different messages were told apart.
func (c *Counts) Patterns() int { return len(c.patterns) }

// Bucket is a stretch of time and how many entries fell in it.
type Bucket struct {
	Start string
	Count int
}

// Histogram counts the entries a search matched by when they were logged,
// in stretches of Width.
type Histogram struct {
	Width  time.Duration
	counts map[time.Time]int
	zoned  bool
}

// Add counts an entry in its stretch.
func (h *Histogram) Add(e *Entry) {
	if h.counts == nil {
		h.counts = map[time.Time]int{}
	}
	h.zoned = h.zoned || e.Zoned
	h.counts[wall(e.Time).Truncate(h.Width)]++
}

// Buckets is every stretch that held an entry, in order: one with none is
// left out, and shows as the jump between its neighbours.
func (h *Histogram) Buckets() []Bucket {
	starts := make([]time.Time, 0, len(h.counts))
	for t := range h.counts {
		starts = append(starts, t)
	}
	slices.SortFunc(starts, func(a, b time.Time) int { return a.Compare(b) })
	layout := "2006-01-02 15:04"
	if h.Width < time.Minute {
		layout = "2006-01-02 15:04:05"
	}
	out := make([]Bucket, 0, len(starts))
	for _, t := range starts {
		out = append(out, Bucket{Start: t.Format(layout), Count: h.counts[t]})
	}

	return out
}

// Gap is a stretch in which nothing a search matched was logged.
type Gap struct {
	From, To string
	Seconds  float64
	// Before and After are the entries either side of it
	Before, After string
}

// Gaps finds the stretches of at least Least in which nothing was logged:
// how a server that stopped answering shows in its own log.
type Gaps struct {
	Least time.Duration
	// Keep is how many are kept, the longest
	Keep int

	last *Entry
	kept []Gap
	// Found is how many there were, kept or not
	Found int
}

// Add offers the next entry, in the order written.
func (g *Gaps) Add(e *Entry) {
	if g.last != nil {
		quiet := wall(e.Time).Sub(wall(g.last.Time))
		if g.last.Zoned && e.Zoned {
			quiet = e.Time.Sub(g.last.Time)
		}
		if quiet >= g.Least {
			g.Found++
			g.kept = append(g.kept, Gap{
				From: g.last.Stamp(), To: e.Stamp(), Seconds: quiet.Seconds(),
				Before: g.last.Render(false), After: e.Render(false),
			})
			if len(g.kept) > g.Keep {
				shortest := 0
				for i := range g.kept {
					if g.kept[i].Seconds < g.kept[shortest].Seconds {
						shortest = i
					}
				}
				g.kept = slices.Delete(g.kept, shortest, shortest+1)
			}
		}
	}
	// only what Render needs is held on to
	g.last = e
}

// Kept is the gaps kept, in the order they happened.
func (g *Gaps) Kept() []Gap { return g.kept }
