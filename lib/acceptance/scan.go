package acceptance

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/katbyte/go-kt/mcp/acctest"
)

// ScanPatience is how long a library scan is given: quick on a quiet
// machine, and not always quick on a runner sharing itself with three other
// suites. Every wait on a scan uses it, so none gives up before the scan can
// have finished and leaks the scan into the next test.
const ScanPatience = 6 * time.Minute

// scanPoll is how often a wait on a scan looks again.
const scanPoll = 2 * time.Second

// TypeCount is how many items of one type a library holds right now, as
// library_get counts them.
func (s *Suite) TypeCount(library, kind string) (int, error) {
	out, err := s.Invoke("library_get", map[string]any{"library": library})
	if err != nil {
		return 0, err
	}
	counts, ok := out["type_counts"].(map[string]any)
	if !ok {
		return 0, nil
	}

	return acctest.NumOr0(counts[kind]), nil
}

// TypeCounts is a library's type_counts right now, empty when it cannot be
// read (a library not listed yet).
func (s *Suite) TypeCounts(library string) map[string]int {
	counts := map[string]int{}
	out, err := s.Invoke("library_get", map[string]any{"library": library})
	if err != nil {
		return counts
	}
	if raw, ok := out["type_counts"].(map[string]any); ok {
		for k, v := range raw {
			counts[k] = acctest.NumOr0(v)
		}
	}

	return counts
}

// WaitForItems polls library_get until a scan has settled on want items of
// one type in the library, for as long as patience allows.
func (s *Suite) WaitForItems(library, kind string, want int, patience time.Duration) error {
	var last string
	for range int(patience / scanPoll) {
		n, err := s.TypeCount(library, kind)
		switch {
		case err != nil:
			last = err.Error()
		case n == want:
			return nil
		default:
			last = fmt.Sprintf("at %d %s items", n, kind)
		}
		time.Sleep(scanPoll)
	}

	return fmt.Errorf("library %s never reached %d %s items (%s)", library, want, kind, last)
}

// ScanUntil asks for a scan of every library and waits for one library to
// hold want items of a kind, asking again whenever the scan goes idle short
// of the count. A scan already running when the ask comes passes folders
// written since it started, on both servers, and the ask itself is dropped
// by Jellyfin, so one ask is not enough on a busy server (CI's runners are).
func (s *Suite) ScanUntil(library, kind string, want int) error {
	deadline := time.Now().Add(ScanPatience)
	for {
		// idle before asking, so the ask starts a scan rather than joining one
		if err := s.WaitForScan(); err != nil {
			return err
		}
		if _, err := s.Invoke("library_scan", nil); err != nil {
			return err
		}
		if err := s.WaitForItems(library, kind, want, 45*time.Second); err == nil {
			return s.WaitForScan()
		} else if time.Now().After(deadline) {
			return err
		}
	}
}

// ScanUntilTrue asks for a scan of one library, or of every library when
// none is named, until check holds, asking again whenever the scan goes idle
// short of it: a scan already running when the ask comes passes over what
// was written since it started. It has ScanUntil's patience, for a change a
// count cannot see, and fails when no scan brings it about.
func (s *Suite) ScanUntilTrue(library string, check func() bool) error {
	var args map[string]any
	if library != "" {
		args = map[string]any{"library": library}
	}
	deadline := time.Now().Add(ScanPatience)
	for {
		if err := s.WaitForScan(); err != nil {
			return err
		}
		if _, err := s.Invoke("library_scan", args); err != nil {
			return err
		}
		for range int(45 * time.Second / scanPoll) {
			if check() {
				return s.WaitForScan()
			}
			time.Sleep(scanPoll)
		}
		if time.Now().After(deadline) {
			return errors.New("no scan brought about what the test waits for")
		}
	}
}

// WaitForScan waits for the library scan task to go idle, so the provider
// lookups the scan triggers have finished before a test looks at their
// results. A task_list that fails once (a server busy with the scan) is
// asked again rather than ending the wait.
func (s *Suite) WaitForScan() error {
	failures := 0
	for range int(ScanPatience / scanPoll) {
		idle, err := s.ScanIdle()
		switch {
		case err != nil:
			failures++
			if failures > 5 {
				return err
			}
		case idle:
			return nil
		}
		time.Sleep(scanPoll)
	}

	return errors.New("the library scan never went idle")
}

// ScanIdle says whether the library scan task is idle.
func (s *Suite) ScanIdle() (bool, error) {
	out, err := s.Invoke("task_list", nil)
	if err != nil {
		return false, err
	}
	for _, row := range acctest.RowsOf(out["tasks"]) {
		if strings.Contains(strings.ToLower(acctest.Str(row["name"])), "scan media library") && acctest.Str(row["state"]) != "Idle" {
			return false, nil
		}
	}

	return true, nil
}

// WaitForExpectedScan is WaitForScan for a scan a change should have
// started: a library made, deleted or given a folder. A server that starts
// the task off the request thread (Jellyfin: offThread) is given a moment to
// show it in the task list and, if it never does, a scan is started here, so
// a dropped scan costs a wait rather than a test. Emby's scans start on the
// request, so there it is a plain WaitForScan.
func (s *Suite) WaitForExpectedScan(offThread bool) error {
	if !offThread {
		return s.WaitForScan()
	}
	started := false
	for range 10 {
		idle, err := s.ScanIdle()
		if err == nil && !idle {
			started = true

			break
		}
		time.Sleep(scanPoll)
	}
	if !started {
		if _, err := s.Invoke("task_run", map[string]any{"task": "scan media library"}); err != nil {
			return fmt.Errorf("the change started no scan, and starting one failed: %w", err)
		}
	}

	return s.WaitForScan()
}
