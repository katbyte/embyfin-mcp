package embyfin

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
)

type SystemInfo struct {
	ServerName      string `json:"ServerName"`
	Version         string `json:"Version"`
	ID              string `json:"Id"`
	OperatingSystem string `json:"OperatingSystem"`
}

func (c *Client) SystemInfo(ctx context.Context) (*SystemInfo, error) {
	if c.isEmby() {
		res, err := c.emby.GetSystemInfo(ctx)
		if err != nil {
			return nil, err
		}
		if res.Model == nil {
			return nil, errors.New("the server answered with no system information")
		}

		return systemInfoFromEmby(res.Model), nil
	}

	res, err := c.jf.GetSystemInfo(ctx)
	if err != nil {
		return nil, err
	}

	return systemInfoFromJF(res.Model), nil
}

type ItemCounts struct {
	MovieCount      int `json:"MovieCount"`
	SeriesCount     int `json:"SeriesCount"`
	EpisodeCount    int `json:"EpisodeCount"`
	AlbumCount      int `json:"AlbumCount"`
	SongCount       int `json:"SongCount"`
	MusicVideoCount int `json:"MusicVideoCount"`
	BoxSetCount     int `json:"BoxSetCount"`
	TrailerCount    int `json:"TrailerCount"`
}

func (c *Client) Counts(ctx context.Context) (*ItemCounts, error) {
	if c.isEmby() {
		res, err := c.emby.GetItemsCounts(ctx, emby.GetItemsCountsOperationOptions{})
		if err != nil {
			return nil, err
		}
		// no counts is not zero of everything
		if res.Model == nil {
			return nil, errors.New("the server answered with no item counts")
		}

		return countsFromEmby(res.Model), nil
	}

	res, err := c.jf.GetItemCounts(ctx, jf.GetItemCountsOperationOptions{})
	if err != nil {
		return nil, err
	}

	return countsFromJF(res.Model), nil
}

type ActivityEntry struct {
	Name          string `json:"Name"`
	Type          string `json:"Type"`
	Date          string `json:"Date"`
	Severity      string `json:"Severity"`
	ShortOverview string `json:"ShortOverview,omitempty"`
	UserID        string `json:"UserId,omitempty"`
	ItemID        string `json:"ItemId,omitempty"`
}

// ActivityLog returns activity entries since minDate, newest first.
func (c *Client) ActivityLog(ctx context.Context, minDate time.Time, limit, offset int) ([]ActivityEntry, int, error) {
	var since string
	if !minDate.IsZero() {
		since = minDate.UTC().Format(time.RFC3339)
	}
	if limit < 0 {
		limit = 0
	}

	if c.isEmby() {
		res, err := c.emby.GetSystemActivityLogEntries(ctx, emby.GetSystemActivityLogEntriesOperationOptions{MinDate: since, Limit: nz(limit), StartIndex: nz(offset)})
		if err != nil {
			return nil, 0, err
		}
		page := orEmpty(res.Model)
		entries := make([]ActivityEntry, 0, len(page.Items))
		for i := range page.Items {
			entries = append(entries, activityFromEmby(&page.Items[i]))
		}

		return entries, page.TotalRecordCount, nil
	}

	res, err := c.jf.GetLogEntries(ctx, jf.GetLogEntriesOperationOptions{MinDate: since, Limit: nz(limit), StartIndex: nz(offset)})
	if err != nil {
		return nil, 0, err
	}
	entries := make([]ActivityEntry, 0, len(res.Model.Items))
	for i := range res.Model.Items {
		entries = append(entries, activityFromJF(&res.Model.Items[i]))
	}

	return entries, res.Model.TotalRecordCount, nil
}

type Device struct {
	Name             string `json:"Name"`
	AppName          string `json:"AppName"`
	AppVersion       string `json:"AppVersion"`
	LastUserName     string `json:"LastUserName,omitempty"`
	DateLastActivity string `json:"DateLastActivity,omitempty"`
	ID               string `json:"Id"`
}

func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	if c.isEmby() {
		res, err := c.emby.GetDevices(ctx, emby.GetDevicesOperationOptions{})
		if err != nil {
			return nil, err
		}
		listed := orEmpty(res.Model).Items
		devices := make([]Device, 0, len(listed))
		for i := range listed {
			devices = append(devices, deviceFromEmby(&listed[i]))
		}

		return devices, nil
	}

	res, err := c.jf.GetDevices(ctx, jf.GetDevicesOperationOptions{})
	if err != nil {
		return nil, err
	}
	devices := make([]Device, 0, len(res.Model.Items))
	for i := range res.Model.Items {
		devices = append(devices, deviceFromJF(&res.Model.Items[i]))
	}

	return devices, nil
}

type LogFile struct {
	Name         string `json:"Name"`
	Size         int64  `json:"Size"`
	DateModified string `json:"DateModified"`
}

func (c *Client) LogFiles(ctx context.Context) ([]LogFile, error) {
	if c.isEmby() {
		res, err := c.emby.GetSystemLogsQuery(ctx, emby.GetSystemLogsQueryOperationOptions{})
		if err != nil {
			return nil, err
		}
		listed := orEmpty(res.Model).Items
		files := make([]LogFile, 0, len(listed))
		for i := range listed {
			files = append(files, logFileFromEmby(&listed[i]))
		}

		return files, nil
	}

	res, err := c.jf.GetServerLogs(ctx)
	if err != nil {
		return nil, err
	}
	files := make([]LogFile, 0, len(res.Model))
	for i := range res.Model {
		files = append(files, logFileFromJF(&res.Model[i]))
	}

	return files, nil
}

// LogTail is the last lines of a named server log file, at most n of them.
// The log is read through to its end keeping only its tail (tailLines), so a
// log of any size answers its true last lines: a cap on how much of it is read
// would answer lines from the middle of a big one, and could cut the last of
// them in two.
func (c *Client) LogTail(ctx context.Context, name string, n int) (string, error) {
	var body io.ReadCloser
	if c.isEmby() {
		res, err := c.emby.GetSystemLogsByName(ctx, name, emby.GetSystemLogsByNameOperationOptions{})
		if err != nil {
			return "", err
		}
		body = res.HttpResponse.Body
	} else {
		res, err := c.jf.GetLogFile(ctx, jf.GetLogFileOperationOptions{Name: name})
		if err != nil {
			return "", err
		}
		body = res.HttpResponse.Body
	}
	defer func() { _ = body.Close() }()

	tail, err := tailLines(body, n)
	if err != nil {
		return "", fmt.Errorf("reading log %s: %w", name, err)
	}

	return tail, nil
}

const (
	// maxLogLine is how much of one log line a tail keeps: the start of a
	// longer one (a stack or a request dumped on one line), marked "..."
	maxLogLine = 64 << 10
	// maxTailBytes bounds a tail however many lines are asked for: it holds
	// the last lines that fit
	maxTailBytes = 16 << 20
)

// tailLines reads r to its end and returns its last n lines joined by
// newlines, as the file ends them: a carriage return before the newline
// goes, blank lines at the very end do not count (one inside does), and a line
// longer than maxLogLine keeps its start. Only the tail is ever held, at most
// n lines and about maxTailBytes.
func tailLines(r io.Reader, n int) (string, error) {
	if n <= 0 {
		return "", nil
	}
	var (
		kept []string
		size int
		// blank lines seen and not kept yet: they count once a line follows
		blank int
	)
	keep := func(line string) {
		kept = append(kept, line)
		size += len(line) + 1
		for len(kept) > n || size > maxTailBytes && len(kept) > 1 {
			size -= len(kept[0]) + 1
			kept = kept[1:]
		}
	}

	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	cut := false
	for {
		chunk, err := br.ReadSlice('\n')
		ended := err == nil
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) && !errors.Is(err, io.EOF) {
			return "", err
		}
		chunk = bytes.TrimSuffix(chunk, []byte("\n"))
		if room := maxLogLine - len(line); len(chunk) > room {
			chunk, cut = chunk[:room], true
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue // the rest of a long line
		}

		// a whole line, or what the file ends on without a newline
		if ended || len(line) > 0 {
			s := strings.TrimSuffix(string(line), "\r")
			if cut {
				s += "..."
			}
			if s == "" {
				blank++
			} else {
				for range min(blank, n) {
					keep("")
				}
				blank = 0
				keep(s)
			}
		}
		line, cut = line[:0], false
		if !ended {
			return strings.Join(kept, "\n"), nil
		}
	}
}
