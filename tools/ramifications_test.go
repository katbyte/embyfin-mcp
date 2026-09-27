package tools

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// Every list's members are read a few at a time, not one after another: a
// server with thousands of collections took one request each, in a row. The
// reads stay at readsAtOnce at once, every one is made, and the first that
// fails is the answer, with no more begun after it.
func TestEachAtOnce(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	now, most := 0, 0
	done := map[int]bool{}
	release := make(chan struct{})
	go func() {
		// let the first batch pile up before any finishes
		for range 200 {
			release <- struct{}{}
		}
	}()
	err := eachAtOnce(t.Context(), 200, func(_ context.Context, i int) error {
		mu.Lock()
		now++
		most = max(most, now)
		mu.Unlock()
		<-release
		mu.Lock()
		now--
		done[i] = true
		mu.Unlock()
		return nil
	})
	if err != nil || len(done) != 200 || most > readsAtOnce || most < 2 {
		t.Errorf("200 reads: err %v, %d made, at most %d at once; want all made, never more than %d at once", err, len(done), most, readsAtOnce)
	}

	failed := errors.New("the server is busy")
	var calls int
	err = eachAtOnce(t.Context(), 1000, func(ctx context.Context, i int) error {
		mu.Lock()
		calls++
		mu.Unlock()
		if i == 3 {
			return failed
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, failed) || calls >= 1000 {
		t.Errorf("a failing read: err %v after %d calls, want the failure, and the reads after it not begun", err, calls)
	}
}
