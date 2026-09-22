package embyfin

import (
	"context"
	"io"
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
func (c *Client) ActivityLog(ctx context.Context, minDate time.Time, limit int) ([]ActivityEntry, int, error) {
	var since string
	if !minDate.IsZero() {
		since = minDate.UTC().Format(time.RFC3339)
	}
	if limit < 0 {
		limit = 0
	}

	if c.isEmby() {
		res, err := c.emby.GetSystemActivityLogEntries(ctx, emby.GetSystemActivityLogEntriesOperationOptions{MinDate: since, Limit: nz(limit)})
		if err != nil {
			return nil, 0, err
		}
		entries := make([]ActivityEntry, 0, len(res.Model.Items))
		for i := range res.Model.Items {
			entries = append(entries, activityFromEmby(&res.Model.Items[i]))
		}

		return entries, res.Model.TotalRecordCount, nil
	}

	res, err := c.jf.GetLogEntries(ctx, jf.GetLogEntriesOperationOptions{MinDate: since, Limit: nz(limit)})
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
		devices := make([]Device, 0, len(res.Model.Items))
		for i := range res.Model.Items {
			devices = append(devices, deviceFromEmby(&res.Model.Items[i]))
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
		files := make([]LogFile, 0, len(res.Model.Items))
		for i := range res.Model.Items {
			files = append(files, logFileFromEmby(&res.Model.Items[i]))
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

// LogText fetches the raw text of a named server log file.
func (c *Client) LogText(ctx context.Context, name string) (string, error) {
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

	text, err := io.ReadAll(io.LimitReader(body, maxLogBytes))
	if err != nil {
		return "", err
	}

	return string(text), nil
}

// maxLogBytes caps a log read; the tools show the tail of it.
const maxLogBytes = 32 << 20
