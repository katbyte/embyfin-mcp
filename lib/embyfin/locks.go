package embyfin

import "sync"

// keyedLocks holds one mutex per key, made when first wanted and dropped when
// nobody holds or waits for it, so a long-running server does not collect one
// per item it ever touched.
//
// The tools change an item by reading it whole and posting it back (neither
// server has a conditional update), and an MCP client calls tools in
// parallel: two edits of one item at once each post back what they read, and
// the later post undoes the earlier one's change. Holding the item's key for
// the whole round keeps them apart within this process; another client of the
// same server is not covered.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

// lock blocks until key is free, takes it, and returns the release.
func (k *keyedLocks) lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyedLock{}
	}
	l := k.locks[key]
	if l == nil {
		l = &keyedLock{}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()

	l.mu.Lock()

	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
