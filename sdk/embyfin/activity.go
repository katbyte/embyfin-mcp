package embyfin

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/katbyte/pandorest/client"
)

// ActivityRetention is how many days of its activity log the server keeps,
// and whether it keeps a limit at all.
//
// Jellyfin deletes the entries older than its ActivityLogRetentionDays every
// day (a scheduled task; 30 days out of the box), so a history read over a
// longer period reaches the end of what is kept and reads as complete when
// it is not. Emby 4.10 has no such setting and no task that prunes the log
// (seen on 4.10: its configuration and its scheduled tasks name none), so it
// keeps what it has.
//
// Jellyfin's setting can be unset, which its task refuses to run with, and 0,
// which deletes everything before now. The document types it as a number the
// generated model reads as 0 either way, so it is read here as the server
// wrote it.
func (c *Client) ActivityRetention(ctx context.Context) (days int, limited bool, err error) {
	if c.isEmby() {
		return 0, false, nil
	}
	req, err := c.jf.Client.NewRequest(ctx, client.RequestOptions{
		ExpectedStatusCodes: []int{http.StatusOK},
		HTTPMethod:          http.MethodGet,
		Path:                "/System/Configuration",
	})
	if err != nil {
		return 0, false, err
	}
	resp, err := req.Execute(ctx)
	if err != nil {
		return 0, false, err
	}
	var config struct {
		ActivityLogRetentionDays *int `json:"ActivityLogRetentionDays"`
	}
	if err := resp.Unmarshal(&config); err != nil {
		return 0, false, err
	}
	if config.ActivityLogRetentionDays == nil || *config.ActivityLogRetentionDays < 0 {
		return 0, false, nil
	}

	return *config.ActivityLogRetentionDays, true, nil
}

// ActivityPage is how many activity log entries one request reads, and
// ActivityScanMax the most a history tool reads in all. The server filters
// the log to the period, so reading it to the end covers the period; the
// ceiling stops a long period on a busy server from reading without end, and
// an answer it cuts short says how far back it got (see ReadActivity).
const (
	ActivityPage    = 1000
	ActivityScanMax = 20000
)

// DefaultActivityDays is the period an activity read covers when none is
// asked for.
const DefaultActivityDays = 60

// ActivityWindow is the period an activity read covers.
type ActivityWindow struct {
	Days   int
	Cutoff time.Time
	// Short is set when the days asked for reach back past what the server
	// keeps: reading to the end of the log is then not reading the period
	Short bool
	Note  string
}

// ActivityWindow settles the period an activity read covers: the days asked for, or
// DefaultActivityDays, held against what the server keeps of its activity log.
// Jellyfin deletes what is older than its retention (30 days out of the box),
// so a read of 60 days reached the end of what was kept and said complete: a
// film played 40 days ago was "never played". Asked for nothing, the period
// is what the server keeps, whole; asked for more than it keeps, the answer
// is short of the period and says so.
func (c *Client) ActivityWindow(ctx context.Context, asked int) (ActivityWindow, error) {
	w := ActivityWindow{Days: asked}
	if w.Days <= 0 {
		w.Days = DefaultActivityDays
	}
	keeps, limited, err := c.ActivityRetention(ctx)
	if err != nil {
		return ActivityWindow{}, fmt.Errorf("reading how long the server keeps its activity log: %w", err)
	}
	// a retention of 0 deletes everything before the task's daily run, so
	// the log holds a day at most
	keeps = max(keeps, 1)
	if limited && w.Days > keeps {
		if asked <= 0 {
			w.Days = keeps
			w.Note = fmt.Sprintf("the server keeps %d days of activity (its activity log retention) and deletes what is older, so the last %d days were read rather than %d", keeps, keeps, DefaultActivityDays)
		} else {
			w.Short = true
			w.Note = fmt.Sprintf("the server keeps %d days of activity (its activity log retention) and deletes what is older, so of the %d days asked for only the last %d can be read: nothing before %s is seen", keeps, w.Days, keeps, time.Now().AddDate(0, 0, -keeps).Format(time.DateOnly))
		}
	}
	w.Cutoff = time.Now().AddDate(0, 0, -w.Days)

	return w, nil
}

// ActivityRead is the activity log over a period, newest first.
type ActivityRead struct {
	Entries []ActivityEntry
	// Total is how many entries the log holds in the period
	Total int
	// Complete is false when the read stopped short of the period's start:
	// at ActivityScanMax, or where the server's pages ran out before its
	// count did
	Complete bool
}

// ReadActivity reads the activity log since cutoff, a page at a time, until
// it has every entry the server holds in the period or ActivityScanMax of
// them. One page used to be all a history tool read: a thousand entries is a
// few days of a busy server, so a film played a month ago was reported as
// never played inside the default 60 days.
func (c *Client) ReadActivity(ctx context.Context, cutoff time.Time) (ActivityRead, error) {
	var out ActivityRead
	for len(out.Entries) < ActivityScanMax {
		entries, total, err := c.ActivityLog(ctx, cutoff, min(ActivityPage, ActivityScanMax-len(out.Entries)), len(out.Entries))
		if err != nil {
			return ActivityRead{}, err
		}
		out.Entries = append(out.Entries, entries...)
		out.Total = total
		if len(entries) == 0 || len(out.Entries) >= total {
			out.Complete = len(out.Entries) >= total

			return out, nil
		}
	}

	return out, nil
}

// Note says how far back a read cut short got, and nothing for a whole one.
func (a ActivityRead) Note() string {
	if a.Complete || len(a.Entries) == 0 {
		return ""
	}

	return fmt.Sprintf("the activity log holds %d entries in the period and the newest %d were read, back to %s: nothing older was seen, so ask for fewer days to see all of a shorter period",
		a.Total, len(a.Entries), a.Entries[len(a.Entries)-1].Date)
}
