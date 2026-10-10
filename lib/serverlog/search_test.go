package serverlog

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// busy is n lines of one message, a second apart, each with its own number
// and id, the way a task logs every lookup it makes.
func busy(n int) string {
	var b strings.Builder
	at := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	for i := range n {
		fmt.Fprintf(&b, "%s Info HttpClient: GET https://api.example.org/3/tv/%d?api_key=k&language=en\n", at.Add(time.Duration(i)*time.Second).Format(embyStamp), 1000+i)
	}

	return b.String()
}

// What a search keeps is the first entries it matched, or the last, never
// more than asked for in number or in text, and it says which of the two
// stopped it.
func TestLines(t *testing.T) {
	t.Parallel()

	collect := func(l *Lines, log string) []string {
		for _, e := range entries(t, Emby, log) {
			l.Add(e)
		}

		return l.Kept()
	}

	first := &Lines{Limit: 3, MaxChars: 10000}
	if got := collect(first, busy(10)); len(got) != 3 || !strings.Contains(got[0], "09:00:00.000") || !strings.Contains(got[2], "09:00:02.000") || first.Matched != 10 || first.Cut {
		t.Errorf("the first three of ten: %q, matched %d, cut %v", got, first.Matched, first.Cut)
	}
	last := &Lines{Limit: 3, MaxChars: 10000, Newest: true}
	if got := collect(last, busy(10)); len(got) != 3 || !strings.Contains(got[0], "09:00:07.000") || !strings.Contains(got[2], "09:00:09.000") || last.Matched != 10 || last.Cut {
		t.Errorf("the last three of ten: %q, matched %d, cut %v", got, last.Matched, last.Cut)
	}

	// each line is 102 characters: 250 hold two of them
	short := &Lines{Limit: 100, MaxChars: 250}
	if got := collect(short, busy(10)); len(got) != 2 || !short.Cut || !strings.Contains(got[1], "09:00:01.000") {
		t.Errorf("ten lines in 250 characters, oldest kept: %d kept, cut %v", len(got), short.Cut)
	}
	shortLast := &Lines{Limit: 100, MaxChars: 250, Newest: true}
	if got := collect(shortLast, busy(10)); len(got) != 2 || !shortLast.Cut || !strings.Contains(got[1], "09:00:09.000") {
		t.Errorf("ten lines in 250 characters, newest kept: %q, cut %v", got, shortLast.Cut)
	}
	// one entry longer than everything allowed is kept, cut short
	one := &Lines{Limit: 5, MaxChars: 40}
	if got := collect(one, busy(1)); len(got) != 1 || len(got[0]) != 43 || !one.Cut {
		t.Errorf("one long entry in 40 characters = %q, cut %v", got, one.Cut)
	}
	none := &Lines{Limit: 5, MaxChars: 100}
	if got := collect(none, ""); len(got) != 0 || none.Matched != 0 || none.Cut {
		t.Errorf("nothing matched: %q", got)
	}
}

// The same message logged many times is one row with a count, however its
// numbers, ids, quoted names and queries differ from one time to the next.
func TestCountsAndPatterns(t *testing.T) {
	t.Parallel()

	var c Counts
	for _, e := range entries(t, Emby, busy(500)+embyLog) {
		c.Add(e)
	}
	if c.Total != 512 || c.ByLevel["info"] != 509 || c.ByLevel["error"] != 2 || c.ByLevel["warn"] != 1 {
		t.Errorf("total %d, by level %v", c.Total, c.ByLevel)
	}
	top := c.Top(3)
	if len(top) != 3 || top[0].Count != 500 || top[0].Pattern != "HttpClient: GET https://api.example.org/N/tv/N?*" || top[0].First != "2026-01-05 09:00:00.000" || top[0].Last != "2026-01-05 09:08:19.000" || top[0].Level != "info" {
		t.Errorf("the message logged most = %+v", top)
	}
	// the twelve entries of the other log are twelve messages: a lookup of
	// a season is not a lookup of a series
	if c.Patterns() != 13 || c.Other != 0 {
		t.Errorf("%d messages told apart, %d past the limit", c.Patterns(), c.Other)
	}

	for message, want := range map[string]string{
		`"Extract Chapter Images" Failed after 110 minute(s) and 34 seconds`:                              `App: "*" Failed after N minute(s) and N seconds`,
		`Finished creation of trickplay files for "/media/shows/Example Show (2016)/Example - 03x10.mkv"`: `App: Finished creation of trickplay files for "*"`,
		"Removing item 0123456789abcdef0123456789abcdef from 4AE5B1D6-0C1F-4D52-9C3B-7F5E2A1B8C90":        "App: Removing item ID from ID",
		"http/1.1 Response 200 to host2. Time: 6ms. GET http://host1/emby/videos/123456/main.ts?a=b&c=d":  "App: http/N Response N to hostN. Time: Nms. GET http://hostN/emby/videos/N/main.ts?*",
		"took    00:01:02.5   in all": "App: took N in all",
		strings.Repeat("word ", 60):   "App: " + strings.Repeat("word ", 32) + "...",
	} {
		if got := (&Entry{Source: "App", Message: message}).Pattern(); got != want {
			t.Errorf("Pattern(%q)\n got %q\nwant %q", message, got, want)
		}
	}

	// past the limit, new messages are counted together and not kept
	var many Counts
	const letters = "ghijklmnopqrstuvwxyz" // none a hex digit, so no word reads as an id
	for i := range maxPatterns + 7 {
		word := string([]byte{letters[i%20], letters[i/20%20], letters[i/400%20]})
		many.Add(&Entry{Source: "App", Level: Info, LevelName: "Info", Message: "kind " + word})
	}
	if many.Patterns() != maxPatterns || many.Other != 7 || many.Total != maxPatterns+7 {
		t.Errorf("%d told apart, %d counted together, of %d", many.Patterns(), many.Other, many.Total)
	}
}

// Entries are counted by the minute or the hour they were logged in, and a
// stretch with none shows as the jump between its neighbours.
func TestHistogram(t *testing.T) {
	t.Parallel()

	minutes := Histogram{Width: time.Minute}
	hours := Histogram{Width: time.Hour}
	seconds := Histogram{Width: 10 * time.Second}
	for _, e := range entries(t, Emby, embyLog) {
		minutes.Add(e)
		hours.Add(e)
		seconds.Add(e)
	}
	if got := minutes.Buckets(); !slices.Equal(got, []Bucket{{"2026-01-05 08:32", 9}, {"2026-01-05 08:34", 3}}) {
		t.Errorf("by the minute = %v", got)
	}
	if got := hours.Buckets(); !slices.Equal(got, []Bucket{{"2026-01-05 08:00", 12}}) {
		t.Errorf("by the hour = %v", got)
	}
	if got := seconds.Buckets(); len(got) != 4 || got[0] != (Bucket{"2026-01-05 08:32:20", 3}) {
		t.Errorf("by ten seconds = %v", got)
	}
	// a zoned log is counted by its own clock
	var jf Histogram
	jf.Width = time.Minute
	for _, e := range entries(t, Jellyfin, jellyfinLog) {
		jf.Add(e)
	}
	if got := jf.Buckets(); !slices.Equal(got, []Bucket{{"2026-01-05 03:50", 3}, {"2026-01-05 03:51", 4}}) {
		t.Errorf("Jellyfin's by the minute = %v", got)
	}
}

// A stretch with nothing logged is found, with the entries either side of
// it, and only the longest are kept.
func TestGaps(t *testing.T) {
	t.Parallel()

	g := Gaps{Least: 5 * time.Second, Keep: 10}
	for _, e := range entries(t, Emby, embyLog) {
		g.Add(e)
	}
	got := g.Kept()
	if g.Found != 3 || len(got) != 3 {
		t.Fatalf("%d gaps of five seconds or more, %d kept: %+v", g.Found, len(got), got)
	}
	freeze := got[2]
	if freeze.From != "2026-01-05 08:32:41.000" || freeze.To != "2026-01-05 08:34:53.101" || freeze.Seconds != 132.101 || !strings.Contains(freeze.Before, "ChapterImagesTask") || !strings.Contains(freeze.After, "Time: 141832ms") {
		t.Errorf("the long gap = %+v", freeze)
	}

	longest := Gaps{Least: 5 * time.Second, Keep: 1}
	for _, e := range entries(t, Emby, embyLog) {
		longest.Add(e)
	}
	if got := longest.Kept(); longest.Found != 3 || len(got) != 1 || got[0].Seconds != 132.101 {
		t.Errorf("the one longest of three = %+v", got)
	}

	// across a change of clocks a zoned log's gap is the time that passed
	jump := "[2026-11-01 01:59:59.000 -07:00] [INF] [1] App: before\n[2026-11-01 01:00:01.000 -08:00] [INF] [1] App: after\n"
	z := Gaps{Least: time.Second, Keep: 5}
	for _, e := range entries(t, Jellyfin, jump) {
		z.Add(e)
	}
	if got := z.Kept(); len(got) != 1 || got[0].Seconds != 2 {
		t.Errorf("a gap across the clocks going back = %+v", got)
	}
}

// Emby times every answer it gives, so the slow ones are read off its log
// with what was asked and by whom; a request it logged and never answered
// is one still waiting. Jellyfin logs only its slow answers.
func TestSlow(t *testing.T) {
	t.Parallel()

	s := Slow{Least: time.Second, Keep: 5}
	for _, e := range entries(t, Emby, embyLog) {
		s.Add(e)
	}
	if s.Seen != 2 || s.Found != 1 {
		t.Errorf("%d answers timed, %d slow", s.Seen, s.Found)
	}
	if got := s.Slowest(); len(got) != 1 || got[0] != (Response{At: "2026-01-05 08:34:53.101", Millis: 141832, Status: 204, Method: http.MethodPost, Path: "/emby/Sessions/Playing/Progress", Client: "host2"}) {
		t.Errorf("the slow answer = %+v", got)
	}
	if got := s.Minutes(); !slices.Equal(got, []SlowMinute{{"2026-01-05 08:34", 1, 141832}}) {
		t.Errorf("slow answers by the minute = %v", got)
	}
	// the image request at 08:32:32 was never answered: by the log's last
	// line, at 08:34:54, it had waited 142 seconds
	waiting, found := s.Waiting()
	if found != 1 || len(waiting) != 1 || waiting[0] != (Waiting{Since: "2026-01-05 08:32:32.000", Seconds: 142, Method: http.MethodGet, Path: "/emby/Items/9/Images/Primary", Client: "host3"}) {
		t.Errorf("requests still waiting = %+v (%d)", waiting, found)
	}

	// every answer is slow at a threshold of nothing; only the slowest kept
	all := Slow{Keep: 1}
	for _, e := range entries(t, Emby, embyLog) {
		all.Add(e)
	}
	if got := all.Slowest(); all.Found != 2 || len(got) != 1 || got[0].Millis != 141832 {
		t.Errorf("the slowest of two = %+v", got)
	}

	var jf Slow
	jf.Least, jf.Keep = time.Second, 5
	for _, e := range entries(t, Jellyfin, jellyfinLog) {
		jf.Add(e)
	}
	if got := jf.Slowest(); len(got) != 1 || got[0] != (Response{At: "2026-01-05 03:51:01.000 -07:00", Millis: 1234, Status: 200, Path: "/Items", Client: "10.9.9.12"}) {
		t.Errorf("Jellyfin's slow answer = %+v", got)
	}
	if w, n := jf.Waiting(); n != 0 || len(w) != 0 {
		t.Errorf("Jellyfin logs no request before its answer, and %d read as waiting", n)
	}
	if w, n := (&Slow{}).Waiting(); n != 0 || w != nil {
		t.Error("an empty log has a request waiting")
	}

	// the shapes Emby 4.10 writes an answer in: closed with a point, and
	// with the answer's headers after it
	for message, want := range map[string]Response{
		"http/1.1 Response 204 to host1. Time: 5ms. POST http://127.0.0.1:8096/Startup/Configuration. ":                                                                      {Millis: 5, Status: 204, Method: http.MethodPost, Path: "/Startup/Configuration", Client: "host1"},
		"http/1.1 Response 200 to host1. Time: 47ms. POST http://127.0.0.1:8096/Users/AuthenticateByName. Headers: Content-Type=application/json; charset=utf-8, Expires=-1": {Millis: 47, Status: 200, Method: http.MethodPost, Path: "/Users/AuthenticateByName", Client: "host1"},
		"http/1.1 Response 200 to host2. Time: 6ms. GET http://host1/emby/videos/123456/main/1002.ts?PlaySessionId=0123. ":                                                   {Millis: 6, Status: 200, Method: http.MethodGet, Path: "/emby/videos/123456/main/1002.ts", Client: "host2"},
		"http/1.1 Response 204 to host2. Time: 141832ms. POST http://host1/emby/Sessions/Playing/Progress?X-Emby-Device-Name=Living Room":                                    {Millis: 141832, Status: 204, Method: http.MethodPost, Path: "/emby/Sessions/Playing/Progress", Client: "host2"},
	} {
		if got, ok := ResponseOf(&Entry{Message: message}); !ok || got.Millis != want.Millis || got.Status != want.Status || got.Method != want.Method || got.Path != want.Path || got.Client != want.Client {
			t.Errorf("ResponseOf(%q)\n got %+v (%v)\nwant %+v", message, got, ok, want)
		}
	}
	if _, ok := ResponseOf(&Entry{Message: "http/1.1 POST http://127.0.0.1:8096/Startup/User. Source Ip: host1, UserAgent: curl/8.7.1"}); ok {
		t.Error("a request's line was read as an answer")
	}

	for in, want := range map[string]int{"0:00:01.2345678": 1234, "00:00:00.5": 500, "1:02:03": 3723000, "1.00:00:01": 86401000} {
		if got, ok := span(in); !ok || got != want {
			t.Errorf("span(%q) = %d, %v, want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "5", "1:2", "a:b:c", "x.1:2:3"} {
		if _, ok := span(bad); ok {
			t.Errorf("span(%q) was read as a time span", bad)
		}
	}
	for in, want := range map[string]string{"http://host1/emby/Items?x=1": "/emby/Items", "https://host1": "/", "/Items/9#top": "/Items/9", "http://host1:8096/a/b": "/a/b"} {
		if got := pathOf(in); got != want {
			t.Errorf("pathOf(%q) = %q, want %q", in, got, want)
		}
	}
}
