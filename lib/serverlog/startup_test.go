package serverlog

import (
	"maps"
	"strings"
	"testing"
)

// What each server writes of itself as it starts, in the shapes Emby 4.10
// and Jellyfin 12.2 write it; the values are made up.
const (
	embyStart = "2026-01-05 08:00:00.081 Info Main: Application path: /system/EmbyServer.dll\n" +
		"2026-01-05 08:00:00.201 Info Main: Emby Server 4.10.1.0\n" +
		"\tCommand line: /system/EmbyServer.dll -programdata /config\n" +
		"\tOperating system: Linux version 6.1 (...)\n" +
		"\tOS/Process: x64/arm64\n" +
		"\tFramework: .NET 8.0.1\n" +
		"\tRuntime: system/System.Private.CoreLib.dll\n" +
		"\tProcessor count: 16\n" +
		"\tData path: /config\n" +
		"\tApplication path: /system\n" +
		"2026-01-05 08:00:00.202 Info Main: Logs path: /config/logs\n" +
		"2026-01-05 08:00:00.203 Info Main: Cache path: /config/cache\n" +
		"2026-01-05 08:00:00.204 Info Main: Internal metadata path: /config/metadata\n" +
		"2026-01-05 08:00:00.300 Info App: Processor count: 99\n"
	jellyfinStart = `[2026-01-05 08:00:00.335 -07:00] [INF] [5] Main: Jellyfin version: "12.2.0"
[2026-01-05 08:00:00.342 -07:00] [INF] [5] Main: Operating system: "Example Linux 13"
[2026-01-05 08:00:00.343 -07:00] [INF] [5] Main: Architecture: Arm64
[2026-01-05 08:00:00.343 -07:00] [INF] [5] Main: Processor count: 8
[2026-01-05 08:00:00.343 -07:00] [INF] [5] Main: Program data path: "/config"
[2026-01-05 08:00:00.343 -07:00] [INF] [5] Main: Log directory path: "/config/log"
[2026-01-05 08:00:00.344 -07:00] [INF] [5] Main: Cache path: "/cache"
`
)

// The top of a log a server began at a start says what its API does not:
// how many processors it counted, what it runs on, where it keeps things,
// and when it started. A log begun at midnight says none of it.
func TestReadStartup(t *testing.T) {
	t.Parallel()

	got, err := ReadStartup(strings.NewReader(embyStart), Emby)
	if err != nil || got == nil {
		t.Fatalf("Emby's start = %v, %v", got, err)
	}
	if got.Processors != 16 || got.At != "2026-01-05 08:00:00.201" || got.Architecture != "arm64" || got.Framework != ".NET 8.0.1" || got.OperatingSystem != "Linux version 6.1 (...)" {
		t.Errorf("Emby's start = %+v", got)
	}
	if want := map[string]string{"data": "/config", "logs": "/config/logs", "cache": "/config/cache", "metadata": "/config/metadata"}; !maps.Equal(got.Paths, want) {
		t.Errorf("Emby's paths = %v, want %v", got.Paths, want)
	}

	got, err = ReadStartup(strings.NewReader(jellyfinStart), Jellyfin)
	if err != nil || got == nil {
		t.Fatalf("Jellyfin's start = %v, %v", got, err)
	}
	if got.Processors != 8 || got.At != "2026-01-05 08:00:00.343 -07:00" || got.Architecture != "Arm64" || got.OperatingSystem != "Example Linux 13" {
		t.Errorf("Jellyfin's start = %+v", got)
	}
	if want := map[string]string{"data": "/config", "logs": "/config/log", "cache": "/cache"}; !maps.Equal(got.Paths, want) {
		t.Errorf("Jellyfin's paths = %v, want %v", got.Paths, want)
	}

	// a log begun at midnight, and one whose start is too far in to be one
	for name, log := range map[string]string{
		"begun at midnight": "2026-01-05 00:00:00.000 Info TaskManager: Executing Scan media library\n",
		"empty":             "",
		"far in":            strings.Repeat("2026-01-05 00:00:00.000 Info App: line\n", startupEntries) + embyStart,
	} {
		if got, err := ReadStartup(strings.NewReader(log), Emby); got != nil || err != nil {
			t.Errorf("a log %s read as a start: %+v, %v", name, got, err)
		}
	}
}
