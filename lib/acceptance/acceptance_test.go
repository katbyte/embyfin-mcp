package acceptance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeServer is an MCP server of three tools: one that answers, one that
// refuses, and a task list whose scan goes idle after a few asks.
func fakeServer(t *testing.T) (*Suite, *atomic.Int32) {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	type in struct {
		Name string `json:"name,omitempty"`
	}
	type out struct {
		Greeting string `json:"greeting"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "greet"}, func(_ context.Context, _ *mcp.CallToolRequest, in in) (*mcp.CallToolResult, out, error) {
		return nil, out{Greeting: "hello " + in.Name}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "refuse"}, func(_ context.Context, _ *mcp.CallToolRequest, _ in) (*mcp.CallToolResult, out, error) {
		return nil, out{}, errors.New("no: not like that")
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "never"}, func(_ context.Context, _ *mcp.CallToolRequest, _ in) (*mcp.CallToolResult, out, error) {
		return nil, out{}, nil
	})
	var scans atomic.Int32
	type tasks struct {
		Tasks []map[string]any `json:"tasks"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "task_list"}, func(_ context.Context, _ *mcp.CallToolRequest, _ in) (*mcp.CallToolResult, tasks, error) {
		state := "Running"
		if scans.Add(1) > 2 {
			state = "Idle"
		}

		return nil, tasks{Tasks: []map[string]any{{"name": "Scan media library", "state": state}, {"name": "Other", "state": "Running"}}}, nil
	})

	st, ct := mcp.NewInMemoryTransports()
	ctx := t.Context()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	return &Suite{Ctx: ctx, Session: cs, Ready: true, NotReady: "no server", CannotAnswer: map[string]string{"refuse": "it never does"}}, &scans
}

// Every call is counted as an answer or a failure, a tool never seen to
// answer is uncovered - unless it is one that cannot - and a refusal comes
// back as its text.
func TestCallsAreCounted(t *testing.T) {
	t.Parallel()

	s, _ := fakeServer(t)
	if out := s.Call(t, "greet", map[string]any{"name": "kt"}); out["greeting"] != "hello kt" {
		t.Errorf("greet = %v", out)
	}
	if out := s.Call(t, "greet", nil); out["greeting"] != "hello " {
		t.Errorf("greet with nothing = %v", out)
	}
	if msg := s.CallErr(t, "refuse", nil); !strings.HasPrefix(msg, "refuse: ") || !strings.Contains(msg, "not like that") {
		t.Errorf("refuse = %q", msg)
	}
	if _, err := s.Invoke("nope", nil); err == nil || !strings.HasPrefix(err.Error(), "nope: ") {
		t.Errorf("an unknown tool = %v", err)
	}
	if c := s.Calls("greet"); c != (Calls{Answered: 2}) {
		t.Errorf("greet's calls = %+v", c)
	}
	if c := s.Calls("refuse"); c != (Calls{Failed: 1}) {
		t.Errorf("refuse's calls = %+v", c)
	}
	never, onlyFailed, err := s.Uncovered()
	if err != nil || !slices.Equal(never, []string{"never", "task_list"}) || len(onlyFailed) != 0 {
		t.Errorf("uncovered = %v, %v, %v; want never and task_list, and refuse excused", never, onlyFailed, err)
	}
	s.CannotAnswer = nil
	if _, onlyFailed, _ := s.Uncovered(); !slices.Equal(onlyFailed, []string{"refuse"}) {
		t.Errorf("with no excuse, only failed = %v", onlyFailed)
	}
	if names := s.ToolNames(t); !slices.Equal(names, []string{"greet", "never", "refuse", "task_list"}) {
		t.Errorf("tool names = %v", names)
	}
}

// A suite whose server is not there skips the test rather than failing it.
func TestNotReadySkips(t *testing.T) {
	t.Parallel()

	s := &Suite{Ready: false, NotReady: "EMBYFIN_SERVER is not set"}
	t.Run("call", func(t *testing.T) {
		t.Parallel()
		s.Call(t, "greet", nil)
		t.Error("a call with no server did not skip")
	})
	t.Run("names", func(t *testing.T) {
		t.Parallel()
		s.ToolNames(t)
		t.Error("a listing with no server did not skip")
	})
}

// The scan waits read the task list until the scan is idle, and the undo
// family puts back through the tools, reporting what never succeeds.
func TestScansAndPutBacks(t *testing.T) {
	t.Parallel()

	s, scans := fakeServer(t)
	idle, err := s.ScanIdle()
	if err != nil || idle {
		t.Errorf("first look: idle %v, %v, want a scan running", idle, err)
	}
	if err := s.WaitForScan(); err != nil || scans.Load() < 3 {
		t.Errorf("WaitForScan = %v after %d looks", err, scans.Load())
	}
	// a server with no scan task at all is idle
	if idle, err := s.ScanIdle(); err != nil || !idle {
		t.Errorf("idle %v, %v", idle, err)
	}
	if err := s.WaitForExpectedScan(false); err != nil {
		t.Error(err)
	}
	if counts := s.TypeCounts("nope"); len(counts) != 0 {
		t.Errorf("counts of a library the server has no tool for = %v", counts)
	}
	if _, err := s.TypeCount("nope", "Movie"); err == nil {
		t.Error("a count the server cannot give did not fail")
	}
	// Retried gives up after its waits, with the last error
	if err := s.retryWith([]time.Duration{0, 0}, "refuse", nil); err == nil || !strings.Contains(err.Error(), "not like that") {
		t.Errorf("retried = %v", err)
	}
	if c := s.Calls("refuse"); c.Failed != 2 {
		t.Errorf("retried called refuse %d times, want 2", c.Failed)
	}
}

// The readers name the field a wrong shape was found in.
func TestReaders(t *testing.T) {
	t.Parallel()

	answer := map[string]any{
		"items": []any{map[string]any{"name": "Zzyzx", "n": 3.0}, map[string]any{"name": "Quux"}},
		"names": []any{"b", "a"},
		"n":     2.0,
		"ratio": 1.5,
		"flag":  true,
		"mixed": []any{"x", 1.0},
	}
	if rows := Rows(t, answer["items"], "items"); len(rows) != 2 || Str(rows[0]["name"]) != "Zzyzx" || Num(t, rows[0]["n"], "n") != 3 || NumOr0(rows[1]["n"]) != 0 {
		t.Errorf("rows = %v", rows)
	}
	if got := Strs(t, answer["names"], "names"); !slices.Equal(got, []string{"b", "a"}) || !slices.Equal(Sorted(got), []string{"a", "b"}) || !slices.Equal(Reversed(got), []string{"a", "b"}) {
		t.Errorf("strs = %v", got)
	}
	if Num(t, answer["n"], "n") != 2 || Decimal(t, answer["ratio"], "ratio") != 1.5 || !BoolOf(answer["flag"]) || !IsBool(answer["flag"], true) || IsBool(answer["gone"], false) {
		t.Error("the numbers and the flag were read wrong")
	}
	if got := Texts(answer["mixed"]); !slices.Equal(got, []string{"x", ""}) {
		t.Errorf("texts = %v", got)
	}
	if got := RowsOf(answer["mixed"]); len(got) != 0 {
		t.Errorf("rows of a list with no objects = %v", got)
	}
	if RowsOfAny(answer["n"]) != nil || len(RowsOfAny(answer["names"])) != 2 {
		t.Error("RowsOfAny")
	}
	if l, ok := OrEmptyList(nil).([]any); !ok || l == nil || OrEmptyList(answer["names"]) == nil {
		t.Error("OrEmptyList")
	}
	if got := WithoutName([]string{"a", "b", "a"}, "a"); !slices.Equal(got, []string{"b", "a"}) {
		t.Errorf("WithoutName = %v", got)
	}
	if got := SortedKeys(map[string]int{"b": 1, "a": 2}); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("SortedKeys = %v", got)
	}
	if m := Object(t, RowsOfAny(answer["items"])[0], "items[0]"); m["name"] != "Zzyzx" {
		t.Errorf("object = %v", m)
	}
}

// Waits: a check that comes true is seen, one that never does is given up on
// within the patience, and a check that stops holding is caught.
func TestWaits(t *testing.T) {
	t.Parallel()

	n := 0
	if !EventuallyWithin(5*time.Second, func() bool { n++; return n > 2 }) {
		t.Error("a check that comes true was not seen")
	}
	start := time.Now()
	if EventuallyWithin(time.Second, func() bool { return false }) || time.Since(start) > 3*time.Second {
		t.Error("a check that never comes true was not given up on in time")
	}
	m := 0
	if Holds(func() bool { m++; return m < 3 }) {
		t.Error("a check that stops holding was not caught")
	}
}

// Files laid out for the server are world-writable, copied whole, and read
// back as they are.
func TestFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "a", "b")
	MediaMkdir(t, root, dir)
	MediaWrite(t, filepath.Join(dir, "f.txt"), []byte("one"))
	for _, p := range []string{filepath.Join(root, "a"), dir} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o777 {
			t.Errorf("%s: %v %v, want a world-writable folder", p, info.Mode(), err)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "f.txt")); err != nil || info.Mode().Perm() != 0o666 {
		t.Errorf("the file: %v %v, want world-writable", info.Mode(), err)
	}

	CopyTree(t, root, filepath.Join(root, "a"), filepath.Join(root, "c"))
	if raw, err := os.ReadFile(filepath.Join(root, "c", "b", "f.txt")); err != nil || string(raw) != "one" { //nolint:gosec // under the test's own directory
		t.Errorf("the copy = %q, %v", raw, err)
	}

	before := TreeOf(t, root, filepath.Join(root, "c"))
	if _, ok := before[filepath.Join(root, "a")+"/"]; !ok || len(before) != 4 {
		t.Errorf("tree = %v, want the folders ending in / and the one file, c skipped", before)
	}
	files := FilesUnder(t, filepath.Join(root, "a"))
	if len(files) != 1 {
		t.Errorf("files under a = %v", files)
	}
	StillOnDisk(t, root, files, "nothing")
	SameTree(t, root, before, TreeOf(t, root, filepath.Join(root, "c")))
	MediaWrite(t, filepath.Join(dir, "f.txt"), []byte("two"))
	MediaWrite(t, filepath.Join(dir, "g.txt"), []byte("new"))
	after := TreeOf(t, root, filepath.Join(root, "c"))
	if len(after) != len(before)+1 || string(after[filepath.Join(dir, "f.txt")]) != "two" {
		t.Errorf("after the writes the tree = %v", after)
	}
}
