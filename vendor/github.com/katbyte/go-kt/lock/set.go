package lock

import (
	"slices"
	"sync"
)

// set is a lock for each key, made when first wanted and dropped when nobody
// holds or waits for it, so a long-running server does not keep one for
// every record it ever touched. The zero value is ready to use.
type set struct {
	all sync.RWMutex

	mu   sync.Mutex
	keys map[string]*keyLock
}

type keyLock struct {
	sync.Mutex

	holders int
}

// hold takes the named keys until unlock is called. They are taken in sorted
// order, each once however often it is named, so two calls holding
// overlapping sets cannot wait on each other for ever.
func (s *set) hold(keys ...string) (unlock func()) {
	keys = slices.Clone(keys)
	slices.Sort(keys)
	keys = slices.Compact(keys)

	s.all.RLock()
	held := make([]*keyLock, 0, len(keys))
	for _, key := range keys {
		s.mu.Lock()
		if s.keys == nil {
			s.keys = map[string]*keyLock{}
		}
		k := s.keys[key]
		if k == nil {
			k = &keyLock{}
			s.keys[key] = k
		}
		k.holders++
		s.mu.Unlock()

		k.Lock()
		held = append(held, k)
	}

	return func() {
		for i, k := range slices.Backward(held) {
			k.Unlock()
			s.mu.Lock()
			k.holders--
			if k.holders == 0 {
				delete(s.keys, keys[i])
			}
			s.mu.Unlock()
		}
		s.all.RUnlock()
	}
}

// holdAll takes every key there is or could be: it waits for every hold to
// be let go, and no hold is granted until it is.
func (s *set) holdAll() (unlock func()) {
	s.all.Lock()

	return s.all.Unlock
}
