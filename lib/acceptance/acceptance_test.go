package acceptance

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/katbyte/go-kt/mcp/acctest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeServer is an MCP server of three tools: a task list whose scan goes
// idle after a few asks, an item read that knows one item, and a write that
// counts its calls.
func fakeServer(t *testing.T) (s *Suite, scans, puts *atomic.Int32) {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	type in struct {
		ID string `json:"id,omitempty"`
	}
	type out struct {
		ID string `json:"id"`
	}
	scans, puts = new(atomic.Int32), new(atomic.Int32)
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
	mcp.AddTool(srv, &mcp.Tool{Name: "item_get"}, func(_ context.Context, _ *mcp.CallToolRequest, in in) (*mcp.CallToolResult, out, error) {
		if in.ID != "1" {
			return nil, out{}, errors.New("no item with id " + in.ID)
		}

		return nil, out(in), nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "put"}, func(_ context.Context, _ *mcp.CallToolRequest, in in) (*mcp.CallToolResult, out, error) {
		puts.Add(1)

		return nil, out(in), nil
	})

	// the run's own context, not the test's: a test's is cancelled before
	// its clean-ups run, which is when a put-back calls
	connected, err := acctest.Connect(context.WithoutCancel(t.Context()), srv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connected.Session.Close() })

	return &Suite{Suite: connected}, scans, puts
}

// A suite not yet connected skips every call, saying why.
func TestNewSkipsUntilConnected(t *testing.T) {
	t.Parallel()

	s := New("no server")
	if s.Ready || s.NotReady != "no server" {
		t.Errorf("a new suite: ready %v, %q", s.Ready, s.NotReady)
	}
}

// The scan waits read the task list until the scan is idle.
func TestScans(t *testing.T) {
	t.Parallel()

	s, scans, _ := fakeServer(t)
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
}

// A change to an item is put back while the item is there, and left alone
// once the item is gone: there is nothing to put back onto.
func TestUndoIfThere(t *testing.T) {
	t.Parallel()

	s, _, puts := fakeServer(t)
	s.UndoIfThere(t, "1", "put", map[string]any{"id": "1"})
	if puts.Load() != 1 {
		t.Errorf("an item still there was put back %d times, want once", puts.Load())
	}
	s.UndoIfThere(t, "2", "put", map[string]any{"id": "2"})
	if puts.Load() != 1 {
		t.Errorf("an item that is gone was put back: %d puts", puts.Load())
	}
}
