package embyfin

import (
	"sync"
	"testing"
	"time"
)

// Jellyfin reads an item's id with or without its dashes and in either case,
// so two edits of one item that spell its id differently are still one
// item's edits: the second waits for the first. Another item never waits.
func TestLocksKeyAnItemHoweverItsIDIsSpelled(t *testing.T) {
	t.Parallel()

	var k keyedLocks
	var released sync.WaitGroup
	// take locks key in the background and says when it has it
	take := func(key string) <-chan struct{} {
		took := make(chan struct{})
		released.Go(func() {
			release := k.lock(key)
			close(took)
			release()
		})
		return took
	}

	unlock := k.lock("4AE5B1D6-0C1F-4D52-9C3B-7F5E2A1B8C90")
	respelled := take("4ae5b1d60c1f4d529c3b7f5e2a1b8c90")
	select {
	case <-respelled:
		t.Fatal("another spelling of the id took the lock while the first held it")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-take("0b1c2d3e-0000-4000-8000-000000000000"):
	case <-time.After(time.Second):
		t.Fatal("another item waited for this one")
	}

	unlock()
	select {
	case <-respelled:
	case <-time.After(time.Second):
		t.Fatal("the other spelling never took the lock once it was free")
	}

	// and nothing is kept once nobody holds or waits
	released.Wait()
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.locks) != 0 {
		t.Errorf("%d locks kept after every holder let go", len(k.locks))
	}
}
