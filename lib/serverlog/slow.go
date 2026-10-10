package serverlog

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Response is what a server logged about answering one HTTP request.
type Response struct {
	At     string
	Millis int
	Status int
	Method string
	// Path is the request's path with its query left off, and Client who
	// asked, as the log names them
	Path, Client string
}

var (
	// Emby logs every request, and then its answer with how long it took:
	//   http/1.1 GET http://host/emby/Items. Source Ip: host2, UserAgent: ...
	//   http/1.1 Response 200 to host2. Time: 6ms. GET http://host/emby/Items.
	//   http/1.1 Response 200 to host2. Time: 6ms. GET http://host/emby/Items. Headers: Content-Type=...
	// A url can hold spaces (a device's name in its query) and points, so
	// each is read up to the words that follow it (see answeredURL)
	embyRequest  = regexp.MustCompile(`^http/\S+ ([A-Z]+) (.*?)\. Source Ip: ([^,]*)`)
	embyResponse = regexp.MustCompile(`^http/\S+ Response (\d+) to (.*?)\. Time: (\d+)ms\. ([A-Z]+) (.*)$`)
	// Jellyfin logs an answer only when it was slow (half a second by
	// default), and only at debug level:
	//   Slow HTTP Response from "http://..." to 10.0.0.9 in 0:00:01.2345678 with Status Code 200
	jellyfinSlow = regexp.MustCompile(`^Slow HTTP Response from "?(.*?)"? to "?([^" ]*)"? in ([0-9:.]+) with Status Code (\d+)`)
)

// answeredURL is the url off the end of an Emby answer's line: what comes
// before the headers the line may go on to list, without the point Emby
// closes it with.
func answeredURL(rest string) string {
	if before, _, ok := strings.Cut(rest, ". Headers: "); ok {
		return before
	}

	return strings.TrimSuffix(strings.TrimRight(rest, " "), ".")
}

// pathOf is a url's path: no scheme, no host, no query.
func pathOf(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		if j := strings.Index(u, "/"); j >= 0 {
			u = u[j:]
		} else {
			u = "/"
		}
	}
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}

	return u
}

// span reads a .NET time span as Jellyfin writes one, "0:00:01.2345678" or
// "1.02:03:04.5", in milliseconds.
func span(s string) (int, bool) {
	days := 0
	// days come before the hours, after a point: d.hh:mm:ss
	if head, rest, ok := strings.Cut(s, ":"); ok && strings.Contains(head, ".") {
		d, h, _ := strings.Cut(head, ".")
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, s = n, h+":"+rest
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}

	return int((time.Duration(days)*24*time.Hour + time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec*float64(time.Second))).Milliseconds()), true
}

// ResponseOf reads the entry as a server's line about an answer it gave,
// and says whether it is one.
func ResponseOf(e *Entry) (Response, bool) {
	if m := embyResponse.FindStringSubmatch(e.Message); m != nil {
		status, _ := strconv.Atoi(m[1])
		ms, _ := strconv.Atoi(m[3])

		return Response{At: e.Stamp(), Millis: ms, Status: status, Method: m[4], Path: pathOf(answeredURL(m[5])), Client: m[2]}, true
	}
	if m := jellyfinSlow.FindStringSubmatch(e.Message); m != nil {
		ms, ok := span(m[3])
		if !ok {
			return Response{}, false
		}
		status, _ := strconv.Atoi(m[4])

		return Response{At: e.Stamp(), Millis: ms, Status: status, Path: pathOf(m[1]), Client: m[2]}, true
	}

	return Response{}, false
}

// Waiting is a request a server logged and never logged an answer to.
type Waiting struct {
	Since        string
	Seconds      float64
	Method, Path string
	Client       string
}

// SlowMinute is one minute's slow answers.
type SlowMinute struct {
	Minute         string
	Count, Slowest int
}

// Slow finds the answers that took at least Least, and the requests still
// waiting for one when the log ends.
type Slow struct {
	Least time.Duration
	// Keep is how many of each are kept: the slowest answers, and the
	// requests waiting longest
	Keep int

	// Seen is every answer the log timed, slow or not, and Found the slow
	// ones, kept or not
	Seen, Found int
	slowest     []Response
	minutes     map[string]*SlowMinute
	// asked is the requests logged with no answer yet, by Emby's number for
	// each; an answer takes its request out
	asked map[string]*Entry
	last  *Entry
}

// maxAsked bounds the requests remembered as waiting: a log cut off at its
// start has answers with no request, never the other way about, so this is
// only against a log that is not what it looks like.
const maxAsked = 20000

// Add offers the next entry, in the order written.
func (s *Slow) Add(e *Entry) {
	s.last = e
	if s.asked == nil {
		s.asked, s.minutes = map[string]*Entry{}, map[string]*SlowMinute{}
	}
	if e.Request != "" && embyRequest.MatchString(e.Message) {
		if len(s.asked) < maxAsked {
			s.asked[e.Request] = e
		}

		return
	}
	r, ok := ResponseOf(e)
	if !ok {
		return
	}
	delete(s.asked, e.Request)
	s.Seen++
	if time.Duration(r.Millis)*time.Millisecond < s.Least {
		return
	}
	s.Found++
	minute := wall(e.Time).Truncate(time.Minute).Format("2006-01-02 15:04")
	m := s.minutes[minute]
	if m == nil {
		m = &SlowMinute{Minute: minute}
		s.minutes[minute] = m
	}
	m.Count++
	m.Slowest = max(m.Slowest, r.Millis)

	s.slowest = append(s.slowest, r)
	if len(s.slowest) > s.Keep {
		quickest := 0
		for i := range s.slowest {
			if s.slowest[i].Millis < s.slowest[quickest].Millis {
				quickest = i
			}
		}
		s.slowest = slices.Delete(s.slowest, quickest, quickest+1)
	}
}

// Slowest is the slow answers kept, slowest first.
func (s *Slow) Slowest() []Response {
	out := slices.Clone(s.slowest)
	slices.SortStableFunc(out, func(a, b Response) int { return cmp.Compare(b.Millis, a.Millis) })

	return out
}

// Minutes is each minute that held a slow answer, in order.
func (s *Slow) Minutes() []SlowMinute {
	out := make([]SlowMinute, 0, len(s.minutes))
	for _, m := range s.minutes {
		out = append(out, *m)
	}
	slices.SortFunc(out, func(a, b SlowMinute) int { return cmp.Compare(a.Minute, b.Minute) })

	return out
}

// Waiting is the requests with no answer logged by the log's last entry,
// that had by then waited at least Least: the longest waiting first, and
// how many there were, kept or not. Only Emby logs a request before it
// answers it, so only its log can show one.
func (s *Slow) Waiting() (kept []Waiting, found int) {
	if s.last == nil {
		return nil, 0
	}
	for _, e := range s.asked {
		waited := wall(s.last.Time).Sub(wall(e.Time))
		if waited < s.Least {
			continue
		}
		m := embyRequest.FindStringSubmatch(e.Message)
		kept = append(kept, Waiting{Since: e.Stamp(), Seconds: waited.Seconds(), Method: m[1], Path: pathOf(m[2]), Client: strings.TrimSpace(m[3])})
	}
	slices.SortFunc(kept, func(a, b Waiting) int {
		return cmp.Or(cmp.Compare(b.Seconds, a.Seconds), cmp.Compare(a.Path, b.Path))
	})
	found = len(kept)
	if len(kept) > s.Keep {
		kept = kept[:s.Keep]
	}

	return kept, found
}
