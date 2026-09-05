package embyfin

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

type SystemInfo struct {
	ServerName      string `json:"ServerName"`
	Version         string `json:"Version"`
	ID              string `json:"Id"`
	OperatingSystem string `json:"OperatingSystem"`
}

func (c *Client) SystemInfo(ctx context.Context) (*SystemInfo, error) {
	var info SystemInfo
	if err := c.get(ctx, "/System/Info", nil, &info); err != nil {
		return nil, err
	}

	return &info, nil
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
	var counts ItemCounts
	if err := c.get(ctx, "/Items/Counts", nil, &counts); err != nil {
		return nil, err
	}

	return &counts, nil
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

type activityResponse struct {
	Items            []ActivityEntry `json:"Items"`
	TotalRecordCount int             `json:"TotalRecordCount"`
}

// ActivityLog returns activity entries since minDate, newest first.
func (c *Client) ActivityLog(ctx context.Context, minDate time.Time, limit int) ([]ActivityEntry, int, error) {
	q := url.Values{}
	if !minDate.IsZero() {
		q.Set("MinDate", minDate.UTC().Format(time.RFC3339))
	}
	if limit > 0 {
		q.Set("Limit", strconv.Itoa(limit))
	}

	var resp activityResponse
	if err := c.get(ctx, "/System/ActivityLog/Entries", q, &resp); err != nil {
		return nil, 0, err
	}

	return resp.Items, resp.TotalRecordCount, nil
}

type Device struct {
	Name             string `json:"Name"`
	AppName          string `json:"AppName"`
	AppVersion       string `json:"AppVersion"`
	LastUserName     string `json:"LastUserName,omitempty"`
	DateLastActivity string `json:"DateLastActivity,omitempty"`
	ID               string `json:"Id"`
}

type devicesResponse struct {
	Items []Device `json:"Items"`
}

func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	var resp devicesResponse
	if err := c.get(ctx, "/Devices", nil, &resp); err != nil {
		return nil, err
	}

	return resp.Items, nil
}

type LogFile struct {
	Name         string `json:"Name"`
	Size         int64  `json:"Size"`
	DateModified string `json:"DateModified"`
}

func (c *Client) LogFiles(ctx context.Context) ([]LogFile, error) {
	var files []LogFile
	if err := c.get(ctx, "/System/Logs", nil, &files); err != nil {
		return nil, err
	}

	return files, nil
}

// LogText fetches the raw text of a named server log file.
func (c *Client) LogText(ctx context.Context, name string) (string, error) {
	q := url.Values{}
	q.Set("Name", name)

	return c.getText(ctx, "/System/Logs/Log", q)
}
