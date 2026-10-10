package serverlog

import (
	"io"
	"strconv"
	"strings"
)

// Startup is what a server writes about itself at the top of its log when
// it starts: the facts about the machine that its API does not give.
type Startup struct {
	// At is when the server started, as its log wrote it
	At string
	// Processors is how many processors the server counted
	Processors int
	// Architecture is the process's, as the server names it (arm64, Arm64,
	// x64)
	Architecture    string
	OperatingSystem string
	Framework       string
	// Paths are where the server keeps things, by what it calls each: data,
	// logs, cache, metadata
	Paths map[string]string
}

// startupEntries is how far into a log its start is looked for: both
// servers say all of this within their first few dozen entries.
const startupEntries = 150

// startupPaths names the paths each server lists at its start, as one set.
var startupPaths = map[string]string{
	"data path": "data", "program data path": "data",
	"logs path": "logs", "log directory path": "logs",
	"cache path":             "cache",
	"internal metadata path": "metadata",
}

// ReadStartup reads the top of a log for what the server said of itself as
// it started. It answers nil when the log does not begin with a start: a
// server begins a new file each midnight, and only the one it began at a
// start has this.
func ReadStartup(r io.Reader, f Format) (*Startup, error) {
	s := &Startup{Paths: map[string]string{}}
	found := false
	fact := func(at, line string) {
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			return
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.Trim(strings.TrimSpace(value), `"`)
		switch key {
		case "processor count":
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				s.Processors, s.At, found = n, at, true
			}
		case "architecture":
			s.Architecture = value
		case "os/process":
			// Emby gives the system's and the process's: arm64/arm64
			_, s.Architecture, _ = strings.Cut(value, "/")
		case "operating system":
			s.OperatingSystem = value
		case "framework":
			s.Framework = value
		default:
			if name, ok := startupPaths[key]; ok {
				s.Paths[name] = value
			}
		}
	}

	n := 0
	if err := Scan(r, f, func(e *Entry) error {
		n++
		if n > startupEntries {
			return ErrStop
		}
		if e.Source != "Main" {
			return nil
		}
		fact(e.Stamp(), e.Message)
		for _, more := range e.More {
			fact(e.Stamp(), more)
		}

		return nil
	}); err != nil {
		return nil, err
	}
	if !found {
		return nil, nil //nolint:nilnil // no start in this log is an answer, not a failure
	}

	return s, nil
}
