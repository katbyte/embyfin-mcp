package embyfin

import (
	"sync"
	"testing"
	"time"
)

// Jellyfin reads an item's id with or without its dashes and in either case,
// so two edits of one item that spell its id differently are still one
// item's edits: the second waits for the first, from another client of the
// same server too. Another item never waits, and nor does the item with the
// same id on another server.
func TestLocksKeyAnItemHoweverItsIDIsSpelled(t *testing.T) {
	t.Parallel()

	server := "http://" + t.Name() + ".test"
	c, same, other := &Client{baseURL: server}, &Client{baseURL: server}, &Client{baseURL: server + ":8097"}
	var released sync.WaitGroup
	// take locks the item in the background and says when it has it
	take := func(c *Client, id string) <-chan struct{} {
		took := make(chan struct{})
		released.Go(func() {
			unlock := c.lockItem(id)
			close(took)
			unlock()
		})

		return took
	}

	unlock := c.lockItem("4AE5B1D6-0C1F-4D52-9C3B-7F5E2A1B8C90")
	respelled := take(same, "4ae5b1d60c1f4d529c3b7f5e2a1b8c90")
	select {
	case <-respelled:
		t.Fatal("another spelling of the id took the lock while the first held it")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-take(c, "0b1c2d3e-0000-4000-8000-000000000000"):
	case <-time.After(time.Second):
		t.Fatal("another item waited for this one")
	}
	select {
	case <-take(other, "4ae5b1d60c1f4d529c3b7f5e2a1b8c90"):
	case <-time.After(time.Second):
		t.Fatal("the same id on another server waited for this one")
	}

	unlock()
	select {
	case <-respelled:
	case <-time.After(time.Second):
		t.Fatal("the other spelling never took the lock once it was free")
	}
	released.Wait()
}
