package embyfin

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/serverlog"
	"github.com/katbyte/embyfin-mcp/sdk/emby"
	"github.com/katbyte/embyfin-mcp/sdk/jf"
)

type SystemInfo struct {
	ServerName      string `json:"ServerName"`
	Version         string `json:"Version"`
	ID              string `json:"Id"`
	OperatingSystem string `json:"OperatingSystem"`
	// HasPendingRestart says a change is waiting for the server to be
	// restarted: a plugin installed or updated, a setting that only a
	// start reads
	HasPendingRestart  bool `json:"HasPendingRestart"`
	HasUpdateAvailable bool `json:"HasUpdateAvailable"`
	IsShuttingDown     bool `json:"IsShuttingDown"`
	CanSelfRestart     bool `json:"CanSelfRestart"`
	// Paths are where the server says it keeps things, by kind: data, logs,
	// cache, metadata, transcodes. Emby 4.10 sends none
	Paths map[string]string `json:"Paths,omitempty"`
}

// systemPaths gathers the paths a server names, leaving out the ones it
// does not.
func systemPaths(data, logs, cache, metadata, transcodes string) map[string]string {
	paths := map[string]string{}
	for name, path := range map[string]string{"data": data, "logs": logs, "cache": cache, "metadata": metadata, "transcodes": transcodes} {
		if path != "" {
			paths[name] = path
		}
	}

	return paths
}

// LogStartup reads what the server wrote of itself at the top of its log as
// it started (serverlog.Startup): the processors it counted, when it
// started, where it keeps things. The newest of its own logs that begins
// with a start is read, looking no further back than a few: a server begins
// a new log each midnight, and those begin with none. nil when none of them
// does.
func (c *Client) LogStartup(ctx context.Context) (*serverlog.Startup, error) {
	files, err := c.LogFiles(ctx)
	if err != nil {
		return nil, err
	}
	var own []LogFile
	for i := range files {
		if files[i].ServerOwn() {
			own = append(own, files[i])
		}
	}
	slices.SortStableFunc(own, func(a, b LogFile) int { return strings.Compare(b.DateModified, a.DateModified) })
	format := serverlog.Jellyfin
	if c.isEmby() {
		format = serverlog.Emby
	}
	for i, f := range own {
		if i >= startupLogsMost {
			break
		}
		start, err := c.readStartup(ctx, f.Name, format)
		if err != nil {
			return nil, fmt.Errorf("reading the start of log %s: %w", f.Name, err)
		}
		if start != nil {
			return start, nil
		}
	}

	return nil, nil //nolint:nilnil // no log that begins with a start is an answer, not a failure
}

// startupLogsMost is how many logs back a start is looked for: a server up
// for a week has seven logs since its last.
const startupLogsMost = 12

func (c *Client) readStartup(ctx context.Context, name string, format serverlog.Format) (*serverlog.Startup, error) {
	body, err := c.LogStream(ctx, name, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()

	return serverlog.ReadStartup(body, format)
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

// Plugin is something installed into the server that is not part of it.
type Plugin struct {
	ID          string `json:"Id"`
	Name        string `json:"Name"`
	Version     string `json:"Version"`
	Description string `json:"Description,omitempty"`
	// Status is Jellyfin's word for whether the plugin is running: Active,
	// Restart (waiting for one), Disabled, NotSupported, Malfunctioned,
	// Superseded, Deleted. Emby has none
	Status string `json:"Status,omitempty"`
	// Bundled says the plugin came with the server and cannot be taken
	// out, which Jellyfin alone says
	Bundled bool `json:"Bundled,omitempty"`
}

// Plugins lists what is installed into the server.
func (c *Client) Plugins(ctx context.Context) ([]Plugin, error) {
	if c.isEmby() {
		res, err := c.emby.GetPlugins(ctx)
		if err != nil {
			return nil, err
		}
		plugins := make([]Plugin, 0, len(res.Model))
		for i := range res.Model {
			d := &res.Model[i]
			plugins = append(plugins, Plugin{ID: d.Id, Name: d.Name, Version: d.Version, Description: d.Description})
		}

		return plugins, nil
	}
	res, err := c.jf.GetPlugins(ctx)
	if err != nil {
		return nil, err
	}
	plugins := make([]Plugin, 0, len(res.Model))
	for i := range res.Model {
		d := &res.Model[i]
		plugins = append(plugins, Plugin{ID: d.Id, Name: d.Name, Version: d.Version, Description: d.Description, Status: string(d.Status), Bundled: d.CanUninstall != nil && !*d.CanUninstall})
	}

	return plugins, nil
}

type LogFile struct {
	Name string `json:"Name"`
	Size int64  `json:"Size"`
	// DateCreated and DateModified are when the server says the file was
	// begun and last written to, as instants: the log's own lines may
	// carry no zone, and these do
	DateCreated  string `json:"DateCreated,omitempty"`
	DateModified string `json:"DateModified"`
}

// ServerOwn says whether the file is the server's own log - Emby's
// embyserver.txt and the embyserver-<n>.txt it leaves at each restart and
// midnight, Jellyfin's log_<date>.log - and not a transcode's or a hardware
// probe's beside it.
func (f *LogFile) ServerOwn() bool {
	n := strings.ToLower(f.Name)

	return strings.HasPrefix(n, "embyserver") || strings.HasPrefix(n, "log_")
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

// LogStream opens a named server log to be read from its start to its end.
// Neither server serves a part of one, so a reader that wants its last hour
// reads it all.
//
// Emby hands a log out with the hosts and addresses in it replaced by
// placeholders (host1, host2) and its tokens blanked, unless raw asks for it
// as it is on disk: then it names the clients, and wraps each address in
// marks no screen shows. Jellyfin has the one form, as on disk.
func (c *Client) LogStream(ctx context.Context, name string, raw bool) (io.ReadCloser, error) {
	if c.isEmby() {
		var opts emby.GetSystemLogsByNameOperationOptions
		if raw {
			opts.Sanitize = new(false)
		}
		res, err := c.emby.GetSystemLogsByName(ctx, name, opts)
		if err != nil {
			return nil, err
		}

		return res.HttpResponse.Body, nil
	}
	res, err := c.jf.GetLogFile(ctx, jf.GetLogFileOperationOptions{Name: name})
	if err != nil {
		return nil, err
	}

	return res.HttpResponse.Body, nil
}

// LogTail is the last lines of a named server log file, at most n of them.
// The log is read through to its end keeping only its tail (tailLines), so a
// log of any size answers its true last lines: a cap on how much of it is read
// would answer lines from the middle of a big one, and could cut the last of
// them in two.
func (c *Client) LogTail(ctx context.Context, name string, n int) (string, error) {
	body, err := c.LogStream(ctx, name, false)
	if err != nil {
		return "", err
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
