package embyfin

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// library is a canned server's items in the order it sorts them, which a
// test changes between requests the way a library in use changes under a
// long read: a download lands, an upgrade replaces a file, a scan drops what
// is gone.
type library struct {
	mu    sync.Mutex
	ids   []string
	asked int
	// before changes the library before the nth request (1 for the first)
	before func(n int, ids []string) []string
	// edited is the ids edited before the nth request, which the server
	// saves then as it saves an item added
	edited func(n int) []string
	// saved is when the server last saved each item, the zero time for one
	// saved before the test began
	saved map[string]time.Time
	// unsaved leaves an item put in unsaved, as a play or a change to an
	// account's access brings an item into a read filtered by it
	unsaved bool
	// zeroPastEnd counts 0 for a request that starts past the last item, as
	// Emby does
	zeroPastEnd bool
}

// numbered is n item ids, "a0" to "a<n-1>".
func numbered(prefix string, n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("%s%d", prefix, i))
	}

	return out
}

// startLimit is the page a request asks for, whichever server's spelling.
func startLimit(r *http.Request) (start, limit int) {
	q := r.URL.Query()

	return queryInt(q.Get("StartIndex") + q.Get("startIndex")), queryInt(q.Get("Limit") + q.Get("limit"))
}

// queryInt reads a number a request carries, 0 when it carries none. One it
// cannot read is the client's fault, and fails the request.
func queryInt(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		panic(fmt.Sprintf("a request's number %q: %v", s, err))
	}

	return n
}

// itemsAnswer is a server's answer of the ids, counting total.
func itemsAnswer(ids []string, total int) string {
	rows := make([]string, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, fmt.Sprintf(`{"Id":%q,"Name":%q,"Type":"Movie"}`, id, id))
	}

	return `{"Items":[` + strings.Join(rows, ",") + `],"TotalRecordCount":` + strconv.Itoa(total) + `}`
}

func (l *library) route() route {
	return func(r *http.Request, _ string) (int, string) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.asked++
		if l.saved == nil {
			l.saved = map[string]time.Time{}
		}
		if l.before != nil {
			had := map[string]bool{}
			for _, id := range l.ids {
				had[id] = true
			}
			l.ids = l.before(l.asked, l.ids)
			for _, id := range l.ids {
				if !had[id] && !l.unsaved {
					l.saved[id] = time.Now()
				}
			}
		}
		if l.edited != nil {
			for _, id := range l.edited(l.asked) {
				l.saved[id] = time.Now()
			}
		}
		ids := savedSince(r, l.ids, l.saved)
		start, limit := startLimit(r)
		total := len(ids)
		if l.zeroPastEnd && start >= total {
			total = 0
		}

		return http.StatusOK, itemsAnswer(ids[min(start, len(ids)):min(start+limit, len(ids))], total)
	}
}

// savedSince is the ids a request asks for by when they were last saved, in
// their order: every one when it names no time.
func savedSince(r *http.Request, ids []string, saved map[string]time.Time) []string {
	q := r.URL.Query()
	since := q.Get("MinDateLastSaved") + q.Get("minDateLastSaved")
	if since == "" {
		return ids
	}
	at, err := time.Parse(time.RFC3339, since)
	if err != nil {
		panic(fmt.Sprintf("a request's saved-since time %q: %v", since, err))
	}
	var out []string
	for _, id := range ids {
		if !saved[id].Before(at) {
			out = append(out, id)
		}
	}

	return out
}

// readingAgain says whether a request is the read again at a read's end: the
// one read asking for readAgainFields alone.
func readingAgain(r *http.Request) bool {
	q := r.URL.Query()

	return q.Get("Fields")+q.Get("fields") == readAgainFields
}

// readEvery reads the library in pages of 10 (and the overlap of 10 each
// re-reads), answering every id handed on in order, and what the read saw.
// On Emby the library counts 0 past its end, as Emby does.
func readEvery(t *testing.T, backend Backend, l *library) ([]string, ReadResult, error) {
	t.Helper()

	l.zeroPastEnd = backend == Emby
	c, _ := newFake(t, backend, map[string]route{"GET /Items": l.route()})
	var got []string
	result, err := c.ReadAll(t.Context(), SearchOptions{PageSize: 10}, ToAnswer, func(items []Item) bool {
		for i := range items {
			it := &items[i]
			got = append(got, it.ID)
		}
		return true
	})

	return got, result, err
}

// once says every id in want was read exactly once, and nothing was read
// twice.
func once(t *testing.T, name string, got, want []string) {
	t.Helper()

	count := map[string]int{}
	for _, id := range got {
		count[id]++
	}
	for id, n := range count {
		if n > 1 {
			t.Errorf("%s: %s read %d times", name, id, n)
		}
	}
	for _, id := range want {
		if count[id] != 1 {
			t.Errorf("%s: %s read %d times, want once", name, id, count[id])
		}
	}
}

const changedNote = "the library changed while it was read: items added or removed during it may be missing, or listed though gone"

// A read of a library that changes under it reads every item that was there
// throughout exactly once, never fails for the library growing, and says the
// library changed whenever a count differs from the first or the last items
// read turn up anywhere but where they were. Paged by a plain offset, an item
// added before the position was read twice and one removed before it made
// another never read; comparing only the count at the end missed an upgrade
// replacing a file - one out and one in - which leaves the count alone.
func TestReadAllOfALibraryThatChanges(t *testing.T) {
	t.Parallel()

	// an item sorts by the date on its file: one imported now with an old
	// date lands among the first, not at the end
	front := func(n int, ids ...string) func(int, []string) []string {
		return func(asked int, have []string) []string {
			if asked != n {
				return have
			}
			return append(slices.Clone(ids), have...)
		}
	}
	without := func(n int, gone ...string) func(int, []string) []string {
		return func(asked int, have []string) []string {
			if asked != n {
				return have
			}
			return slices.DeleteFunc(slices.Clone(have), func(id string) bool { return slices.Contains(gone, id) })
		}
	}
	// moved puts id after the item after, as an edit that changes how it
	// sorts does
	moved := func(n int, id, after string) func(int, []string) []string {
		return func(asked int, have []string) []string {
			if asked != n {
				return have
			}
			have = slices.DeleteFunc(slices.Clone(have), func(s string) bool { return s == id })
			return slices.Insert(have, slices.Index(have, after)+1, id)
		}
	}
	both := func(fs ...func(int, []string) []string) func(int, []string) []string {
		return func(asked int, have []string) []string {
			for _, f := range fs {
				have = f(asked, have)
			}
			return have
		}
	}
	but := func(ids []string, gone ...string) []string {
		return slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return slices.Contains(gone, id) })
	}

	for _, backend := range []Backend{Emby, Jellyfin} {
		start := numbered("a", 95)
		for _, tc := range []struct {
			name    string
			before  func(int, []string) []string
			want    []string // read once each
			changed string   // what Changed says, or "" for nothing
		}{
			{"a library that holds still", nil, start, ""},
			{"items added among the first, between pages", front(2, "n0", "n1", "n2"), start, changedNote + " (95 matched when the read began, 98 when it ended, and 95 were read)"},
			{"items added among the first before every page", func(asked int, have []string) []string {
				if asked == 1 {
					return have
				}
				return append([]string{fmt.Sprintf("n%d", asked)}, have...)
			}, start, changedNote},
			// the count moved, though nothing was missed: it cannot tell
			{"items at the end, added between pages", func(asked int, have []string) []string {
				if asked == 2 {
					return append(slices.Clone(have), "z0", "z1")
				}
				return have
			}, append(slices.Clone(start), "z0", "z1"), changedNote + " (95 matched when the read began, 97 when it ended, and 97 were read)"},
			{"items already read removed between pages", without(2, "a1", "a2", "a3"), but(start, "a1", "a2", "a3"), changedNote + " (95 matched when the read began, 92 when it ended, and 95 were read)"},
			// more removed than the overlap re-reads: the read looks further
			// back to find its place
			{"more removed than the overlap", without(2, numbered("a", 15)...), start[15:], changedNote + " (95 matched when the read began, 80 when it ended, and 95 were read)"},
			// an upgrade: a file replaced by one downloaded now, which sorts
			// at the end; the count is unchanged, but the last items read
			// moved back one
			{"an item replaced by another", both(without(2, "a3"), func(asked int, have []string) []string {
				if asked == 2 {
					return append(slices.Clone(have), "n0")
				}
				return have
			}), append(but(start, "a3"), "n0"), changedNote + " (95 matched when the read began, 95 when it ended, and 96 were read)"},
			// one out and one in, both among the items already read, between
			// the same two requests, leave the count and the last items read
			// as they were: reading every match again at the end finds n0,
			// which the read never reached, and not a3
			{"an item replaced by another among those already read", both(without(2, "a3"), front(2, "n0")), but(start, "a3"), changedNote + " (95 matched when the read began, 95 when it ended, and 95 were read, with 1 there at the end not among them, and 1 of them gone by the end)"},
			{"the last item read removed", without(2, "a9"), but(start, "a9"), changedNote},
			// one of the last items read, renamed to sort further on: the
			// rest still say where the read is
			{"one of the last items read moved on", moved(2, "a10", "a25"), start, changedNote},
			// more removed than the overlap, and one of the last items read
			// moved into the next page: that one alone is no sign of where
			// the read is, and taking it for one skipped a20 to a22
			{"the last items read pulled back, one moved on", both(without(2, numbered("a", 12)...), moved(2, "a19", "a30")), start, changedNote},
			// and two moved on, the later read now listed first: not in the
			// order they were read, so no sign of where the read is either
			{"the last items read pulled back, two moved on out of order", both(without(2, numbered("a", 12)...), moved(2, "a19", "a30"), moved(2, "a18", "a32")), start, changedNote},
		} {
			l := &library{ids: slices.Clone(start), before: tc.before}
			got, result, err := readEvery(t, backend, l)
			if err != nil {
				t.Errorf("%s, %s: %v", backend, tc.name, err)
				continue
			}
			once(t, fmt.Sprintf("%s, %s", backend, tc.name), got, tc.want)
			if changed := result.Changed(); tc.changed == "" && changed != "" || !strings.Contains(changed, tc.changed) {
				t.Errorf("%s, %s: changed = %q, want %q", backend, tc.name, changed, tc.changed)
			}
			if result.Read != len(got) {
				t.Errorf("%s, %s: read %d, handed on %d", backend, tc.name, result.Read, len(got))
			}
		}

		// two items edited between the same two requests so that one
		// unread sorts among those read and one read sorts among those
		// unread: the count and the last items read stay as they were, and
		// the read again finds the one never reached
		swap := func(asked int, have []string) []string {
			if asked != 2 {
				return have
			}
			have = slices.DeleteFunc(slices.Clone(have), func(id string) bool { return id == "a50" || id == "a4" })
			have = slices.Insert(have, slices.Index(have, "a2")+1, "a50")
			return slices.Insert(have, slices.Index(have, "a60")+1, "a4")
		}
		edits := func(asked int) []string {
			if asked == 2 {
				return []string{"a50", "a4"}
			}
			return nil
		}
		got, result, err := readEvery(t, backend, &library{ids: slices.Clone(start), before: swap, edited: edits})
		if err != nil {
			t.Errorf("%s, two items edited past each other: %v", backend, err)
		}
		once(t, fmt.Sprintf("%s, two items edited past each other", backend), got, but(start, "a50"))
		if want := changedNote + " (95 matched when the read began, 95 when it ended, and 94 were read, with 1 there at the end not among them)"; result.Changed() != want {
			t.Errorf("%s, two items edited past each other: changed = %q, want %q", backend, result.Changed(), want)
		}

		// and the same by a change the server keeps no saved date for - an
		// item coming into a read filtered by watch state, or by an
		// account's access, as another goes out of it - which a check of
		// what the server saved could not see
		got, result, err = readEvery(t, backend, &library{ids: slices.Clone(start), before: both(without(2, "a3"), front(2, "n0")), unsaved: true})
		if want := changedNote + " (95 matched when the read began, 95 when it ended, and 95 were read, with 1 there at the end not among them, and 1 of them gone by the end)"; err != nil || result.Changed() != want || slices.Contains(got, "n0") {
			t.Errorf("%s, one out and one in unsaved: %q, n0 read %v, %v; want %q", backend, result.Changed(), slices.Contains(got, "n0"), err, want)
		}

		// the read ends at a page that comes back short, not at the total an
		// earlier page gave: 40 items fill the third page exactly, and two
		// that land at the end before the fourth are read too
		l40 := &library{ids: numbered("a", 40), before: func(asked int, have []string) []string {
			if asked == 4 {
				return append(slices.Clone(have), "z0", "z1")
			}
			return have
		}}
		got, _, err = readEvery(t, backend, l40)
		if err != nil {
			t.Errorf("%s: %v", backend, err)
		}
		once(t, fmt.Sprintf("%s, items added past a full last page", backend), got, append(numbered("a", 40), "z0", "z1"))

		// removals that take the next request past the end: Emby counts 0
		// there, and the count comes from the wider read that finds the
		// read's place, not from that
		l25 := &library{ids: numbered("a", 25), before: without(2, numbered("a", 15)...)}
		got, result, err = readEvery(t, backend, l25)
		if err != nil {
			t.Errorf("%s, removals past the end: %v", backend, err)
		}
		once(t, fmt.Sprintf("%s, removals past the end", backend), got, numbered("a", 25))
		if want := changedNote + " (25 matched when the read began, 10 when it ended, and 25 were read)"; result.Changed() != want {
			t.Errorf("%s, removals past the end: changed = %q, want %q", backend, result.Changed(), want)
		}

		// a library that changed past finding the read's place again is
		// never answered as the whole: see TestReadAllToAnswerOrToAct
	}
}

// A server whose pages cannot be trusted fails the read rather than answer
// part of the list as all of it, or go round forever: one that sends a page
// short while counting more to come, and one that answers the same first
// page wherever it is asked to start.
func TestReadAllOfAServerThatPagesWrongly(t *testing.T) {
	t.Parallel()

	ids := numbered("a", 95)
	for _, backend := range []Backend{Emby, Jellyfin} {
		for _, tc := range []struct {
			name string
			page func(start, limit int) []string
			want string
		}{
			{"a short page that counts more to come", func(start, limit int) []string {
				return ids[start:min(start+limit, start+15, len(ids))]
			}, "sent 15 items from position 0 where 20 were asked for, yet counts 95"},
			{"the start ignored", func(_, limit int) []string {
				return ids[:min(limit, len(ids))]
			}, "only items already read, twice in a row"},
		} {
			asked := 0
			c, _ := newFake(t, backend, map[string]route{"GET /Items": func(r *http.Request, _ string) (int, string) {
				asked++
				return http.StatusOK, itemsAnswer(tc.page(startLimit(r)), len(ids))
			}})
			_, err := c.ReadAll(t.Context(), SearchOptions{PageSize: 10}, ToAnswer, func([]Item) bool { return true })
			if err == nil || !strings.Contains(err.Error(), tc.want) || asked > 5 {
				t.Errorf("%s, %s: %d requests, %v; want an error saying %q", backend, tc.name, asked, err, tc.want)
			}
		}

		// a server that lists an item twice, counting both, reads each
		// once and says the count and the read disagree
		twice := slices.Insert(slices.Clone(ids), 50, "a49")
		c, _ := newFake(t, backend, map[string]route{"GET /Items": func(r *http.Request, _ string) (int, string) {
			start, limit := startLimit(r)
			return http.StatusOK, itemsAnswer(twice[min(start, len(twice)):min(start+limit, len(twice))], len(twice))
		}})
		result, err := c.ReadAll(t.Context(), SearchOptions{PageSize: 10}, ToAnswer, func([]Item) bool { return true })
		if want := "the server says 96 match but 95 different items were read"; err != nil || !strings.HasPrefix(result.Changed(), want) {
			t.Errorf("%s, an item listed twice = %q, %v; want %q", backend, result.Changed(), err, want)
		}
	}
}

// changing is a library changed at random before some requests after the
// first, which records what a read of it has to answer.
type changing struct {
	mu    sync.Mutex
	rng   *rand.Rand
	ids   []string
	asked int
	added int
	// gone is every id taken out or moved at any point: a read owes the rest
	// of the first ids exactly once
	gone map[string]bool
	// changed is set once anything changed
	changed bool
	// saved is when the server saved each item it added, or moved by an
	// edit
	saved map[string]time.Time
}

func (l *changing) route() route {
	return func(r *http.Request, _ string) (int, string) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.asked++
		start, limit := startLimit(r)
		// nothing changes once the pages are read: the read again at the
		// end checks them, and a change during it is one after the pages
		if l.asked > 1 && !readingAgain(r) && l.rng.IntN(4) == 0 {
			l.changed = true
			// every item put in is saved then, as a server saves an item it
			// adds or one an edit moves
			put := func(id string) {
				l.saved[id] = time.Now()
				l.ids = slices.Insert(l.ids, l.rng.IntN(len(l.ids)+1), id)
			}
			for range 1 + l.rng.IntN(2) {
				kind := l.rng.IntN(4)
				if kind == 3 || len(l.ids) == 0 {
					l.added++
					put(fmt.Sprintf("n%d", l.added))
					continue
				}
				// taken out: and for 1 put back elsewhere, as an edit that
				// changes how it sorts does, and for 2 replaced by another
				i := l.rng.IntN(len(l.ids))
				id := l.ids[i]
				l.gone[id] = true
				l.ids = slices.Delete(l.ids, i, i+1)
				switch kind {
				case 1:
					put(id)
				case 2:
					l.added++
					put(fmt.Sprintf("n%d", l.added))
				}
			}
		}
		ids := savedSince(r, l.ids, l.saved)
		total := len(ids)
		if start >= total {
			total = 0
		}

		return http.StatusOK, itemsAnswer(ids[min(start, len(ids)):min(start+limit, len(ids))], total)
	}
}

// Read after read of a library changed at random between requests: each
// reads every item there throughout exactly once and nothing twice, and one
// that says no change was seen answers the library exactly as it is at the
// end. The note compared only the count at the end, so an item added and
// another taken out cancelled out, and a read that missed an item or listed
// one gone said nothing of it; and as many put in as taken out behind the
// read, between the same two requests, went unseen until the read asked what
// the server saved while it read.
func TestReadAllOfALibraryChangedAtRandom(t *testing.T) {
	t.Parallel()

	quiet, quietChanged, noted := 0, 0, 0
	for run := range 400 {
		backend := []Backend{Emby, Jellyfin}[run%2]
		l := &changing{rng: rand.New(rand.NewPCG(uint64(run), 1)), gone: map[string]bool{}, saved: map[string]time.Time{}} //nolint:gosec // changes a test can repeat, not a secret
		first := numbered("a", 40+l.rng.IntN(80))
		l.ids = slices.Clone(first)
		c, _ := newFake(t, backend, map[string]route{"GET /Items": l.route()})
		var got []string
		result, err := c.ReadAll(t.Context(), SearchOptions{PageSize: 10}, ToAnswer, func(items []Item) bool {
			for _, it := range items {
				got = append(got, it.ID)
			}
			return true
		})
		name := fmt.Sprintf("run %d (%s)", run, backend)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		once(t, name, got, slices.DeleteFunc(first, func(id string) bool { return l.gone[id] }))
		if result.Read != len(got) {
			t.Errorf("%s: read %d, handed on %d", name, result.Read, len(got))
		}
		if result.Changed() != "" {
			noted++
			continue
		}
		quiet++
		if l.changed {
			quietChanged++
		}
		if read, now := slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(l.ids)); !slices.Equal(read, now) {
			t.Errorf("%s: no change seen, yet read %v of %v", name, read, now)
		}
	}
	// every kind of read has to happen for the test to test anything
	t.Logf("%d reads saw no change (%d of them of a library that changed), %d saw one", quiet, quietChanged, noted)
	if quiet < 20 || quietChanged < 5 || noted < 100 {
		t.Errorf("%d reads saw no change (%d of a library that changed) and %d did: the changes need tuning", quiet, quietChanged, noted)
	}
}

// Each request asks for its page and the overlap it re-reads, from where the
// last one ended less the overlap, and the read stops at a page that comes
// back short rather than at the total the first page gave.
func TestReadAllPagesWithAnOverlap(t *testing.T) {
	t.Parallel()

	l := &library{ids: numbered("a", 25)}
	c, f := newFake(t, Emby, map[string]route{"GET /Items": l.route()})
	result, err := c.ReadAll(t.Context(), SearchOptions{PageSize: 10}, ToAnswer, func([]Item) bool { return true })
	if err != nil || result.Read != 25 || result.Total != 25 || result.Changed() != "" {
		t.Fatalf("read = %+v, %q, %v", result, result.Changed(), err)
	}
	requests := f.all("GET /Items")
	asked := make([]string, 0, len(requests))
	for _, r := range requests {
		asked = append(asked, r.query.Get("StartIndex")+"+"+r.query.Get("Limit"))
	}
	// and after the pages every match again, the ids alone, in the same
	// order and larger pages
	if !slices.Equal(asked, []string{"+20", "10+20", "+200"}) {
		t.Errorf("requests = %v, want 0 and 10, twenty each, then every match again", asked)
	}
	if len(requests) == 3 {
		if q := requests[2].query; q.Get("Fields") != readAgainFields || q.Get("SortBy") != "DateCreated,SortName" || q.Get("MinDateLastSaved") != "" {
			t.Errorf("the read again asked %v", q)
		}
	}

	// a read that took one request needs nothing more: one answer holds
	// no change between two
	l = &library{ids: numbered("a", 5)}
	c, f = newFake(t, Emby, map[string]route{"GET /Items": l.route()})
	if result, err = c.ReadAll(t.Context(), SearchOptions{PageSize: 10}, ToAnswer, func([]Item) bool { return true }); err != nil || result.Read != 5 || len(f.all("GET /Items")) != 1 {
		t.Errorf("a read of one page = %+v after %d requests, %v", result, len(f.all("GET /Items")), err)
	}

	// a caller that stops is not read on for, nor read again for, and a
	// read stopped short of the count says nothing of the library changing
	l = &library{ids: numbered("a", 95)}
	c, _ = newFake(t, Emby, map[string]route{"GET /Items": l.route()})
	pages := 0
	result, err = c.ReadAll(t.Context(), SearchOptions{PageSize: 10}, ToAnswer, func([]Item) bool { pages++; return pages < 2 })
	if err != nil || pages != 2 || l.asked != 2 || result.Changed() != "" {
		t.Errorf("a stop after two pages = %d pages and %d requests, %q, %v", pages, l.asked, result.Changed(), err)
	}
}

// A read of more than one request that saw no change reads every match again
// at its end, whatever the server saved: an item there then that the read
// never handed on, or one it handed on that is gone, says the library
// changed; one that holds still says nothing, however many pages the read
// again takes. It reads with the caller's own filters, a saved-since among
// them (what a failure to read again does is TestReadAllToAnswerOrToAct).
func TestReadAllReadsAgainAtItsEnd(t *testing.T) {
	t.Parallel()

	for _, backend := range []Backend{Emby, Jellyfin} {
		for _, change := range []bool{false, true} {
			// with change, one out and one in among those read before the
			// second request, so neither the count nor the last items read
			// move
			l := &library{ids: numbered("a", 250), unsaved: true, before: func(asked int, have []string) []string {
				if asked == 2 && change {
					have = slices.DeleteFunc(slices.Clone(have), func(id string) bool { return id == "a1" })
					return slices.Insert(have, 3, "n0")
				}
				return have
			}}
			c, f := newFake(t, backend, map[string]route{"GET /Items": l.route()})
			result, err := c.ReadAll(t.Context(), SearchOptions{PageSize: 10}, ToAnswer, func([]Item) bool { return true })
			again := 0
			for _, r := range f.all("GET /Items") {
				if r.query.Get("Fields")+r.query.Get("fields") == readAgainFields {
					again++
				}
			}
			want := ""
			if change {
				want = changedNote + " (250 matched when the read began, 250 when it ended, and 250 were read, with 1 there at the end not among them, and 1 of them gone by the end)"
			}
			if err != nil || result.Changed() != want || again != 2 {
				t.Errorf("%s, one out and one in %v = %q after %d requests reading again, %v; want %q after 2", backend, change, result.Changed(), again, err, want)
			}
		}

		// the library changing while it is read again, over more than one
		// request, is a change during the read: 250 items in pages of 10
		// take 25 requests, so the 27th is the second of the read again
		l := &library{ids: numbered("a", 250), before: func(asked int, have []string) []string {
			if asked == 27 {
				return append([]string{"n0"}, have...)
			}
			return have
		}}
		c, _ := newFake(t, backend, map[string]route{"GET /Items": l.route()})
		result, err := c.ReadAll(t.Context(), SearchOptions{PageSize: 10}, ToAnswer, func([]Item) bool { return true })
		if !strings.HasPrefix(result.Changed(), changedNote) || err != nil || l.asked < 27 {
			t.Errorf("%s, a change while read again = %q after %d requests, %v", backend, result.Changed(), l.asked, err)
		}

		// an item read and then taken out after the last page, which neither
		// the count nor the last items read can show: 25 items in pages of 10
		// take 2 requests, so the 3rd is the read again
		l = &library{ids: numbered("a", 25), before: func(asked int, have []string) []string {
			if asked == 3 {
				return slices.DeleteFunc(slices.Clone(have), func(id string) bool { return id == "a3" })
			}
			return have
		}}
		c, _ = newFake(t, backend, map[string]route{"GET /Items": l.route()})
		result, err = c.ReadAll(t.Context(), SearchOptions{PageSize: 10}, ToAnswer, func([]Item) bool { return true })
		if want := changedNote + " (25 matched when the read began, 25 when it ended, and 25 were read, and 1 of them gone by the end)"; err != nil || result.Changed() != want {
			t.Errorf("%s, an item gone after the last page = %q, %v; want %q", backend, result.Changed(), err, want)
		}

		// the read again keeps the caller's own filters: a saved-since
		now := time.Now()
		since := now.Add(-10 * time.Second).UTC().Format(time.RFC3339)
		fresh := &library{ids: numbered("a", 25), saved: map[string]time.Time{}}
		for _, id := range fresh.ids {
			fresh.saved[id] = now
		}
		reader, f := newFake(t, backend, map[string]route{"GET /Items": fresh.route()})
		if _, err = reader.ReadAll(t.Context(), SearchOptions{PageSize: 10, SavedSince: since}, ToAnswer, func([]Item) bool { return true }); err != nil {
			t.Fatalf("%s: %v", backend, err)
		}
		requests := f.all("GET /Items")
		last := requests[len(requests)-1].query
		if asked := last.Get("MinDateLastSaved") + last.Get("minDateLastSaved"); asked != since || len(requests) != 3 || last.Get("Fields")+last.Get("fields") != readAgainFields {
			t.Errorf("%s: a read saved since %s read again saved since %s, in %d requests in all", backend, since, asked, len(requests))
		}
	}
}

// A read the library changed too much to follow fails, whatever it is for,
// with a CutError saying how far it got, and hands back what it read: an
// answer that lists what it found can use that, and one that says what is
// not there, or how many there are, cannot (a read to answer that answered
// with a note gave found: false, and "never watched", from half a read).
// The check at the end failing is where a read to answer and one that
// decides a change to the server part: the one answers what it read, and
// says so; the other fails, and nothing is changed. Every read says which it
// is.
func TestReadAllToAnswerOrToAct(t *testing.T) {
	t.Parallel()

	for _, backend := range []Backend{Emby, Jellyfin} {
		// the whole library replaced before the second request
		replaced := func() *library {
			return &library{ids: numbered("a", 95), before: func(asked int, have []string) []string {
				if asked == 2 {
					return numbered("b", 95)
				}
				return have
			}}
		}
		for _, purpose := range []ReadPurpose{ToAnswer, ToAct} {
			c, _ := newFake(t, backend, map[string]route{"GET /Items": replaced().route()})
			handed := 0
			result, err := c.ReadAll(t.Context(), SearchOptions{PageSize: 10}, purpose, func(items []Item) bool { handed += len(items); return true })
			var cut *CutError
			want := "the library changed too much while it was read to follow it, so it stopped after 20 items and the rest are missing: ask again"
			if !errors.As(err, &cut) || cut.Read != 20 || err.Error() != want || result.Read != 20 || handed != 20 || !result.Cut {
				t.Errorf("%s, purpose %d: a library replaced whole = %+v after %d handed on, %v; want the CutError %q", backend, purpose, result, handed, err, want)
			}
		}
		var err error
		var result ReadResult

		// the read again at the end failing
		failing := func() route {
			items := (&library{ids: numbered("a", 25)}).route()
			return func(r *http.Request, body string) (int, string) {
				if readingAgain(r) {
					return http.StatusInternalServerError, "boom"
				}
				return items(r, body)
			}
		}
		c, _ := newFake(t, backend, map[string]route{"GET /Items": failing()})
		result, err = c.ReadAll(t.Context(), SearchOptions{PageSize: 10}, ToAnswer, func([]Item) bool { return true })
		if want := "whether the library changed during the read could not be checked: reading every match again"; err != nil || result.Read != 25 || !strings.HasPrefix(result.Changed(), want) || !strings.Contains(result.Changed(), "boom") {
			t.Errorf("%s: a read to answer whose check fails = %+v, %q, %v; want its 25 items and %q", backend, result, result.Changed(), err, want)
		}
		c, _ = newFake(t, backend, map[string]route{"GET /Items": failing()})
		if _, err = c.ReadAll(t.Context(), SearchOptions{PageSize: 10}, ToAct, func([]Item) bool { return true }); err == nil || !strings.Contains(err.Error(), "reading every match again") {
			t.Errorf("%s: a read to act whose check fails = %v", backend, err)
		}

		// a read that does not say what it is for is refused, before
		// anything is asked
		c, f := newFake(t, backend, map[string]route{})
		if _, err = c.ReadAll(t.Context(), SearchOptions{}, 0, func([]Item) bool { return true }); err == nil || len(f.all("GET /Items")) != 0 {
			t.Errorf("%s: a read for nothing = %v", backend, err)
		}
	}
}

// A search is read however each server counts it. Emby counts 0 beside a
// page of matches to a search asked for with a limit (seen on 4.10: 40
// matches, 0 at every limit), which read as a count of none: the read is
// taken as uncounted, and says nothing of the count it was not given, where
// it said "the server says 0 match but 95 different items were read".
// Jellyfin lists at most three times the limit of a search, counting that
// many (12.1: a limit of 10 counts 30 and lists nothing from 30 on), which
// read as the whole of it: a read that reaches that many fails rather than
// answer part of the matches as all of them.
func TestReadAllOfASearch(t *testing.T) {
	t.Parallel()

	for _, backend := range []Backend{Emby, Jellyfin} {
		// counting 0 beside every page of matches
		for _, change := range []bool{false, true} {
			l := &library{ids: numbered("a", 95), before: func(asked int, have []string) []string {
				if change && asked == 2 {
					return append([]string{"n0"}, have...)
				}
				return have
			}}
			items := l.route()
			c, _ := newFake(t, backend, map[string]route{"GET /Items": func(r *http.Request, body string) (int, string) {
				status, answer := items(r, body)
				return status, strings.Replace(answer, `"TotalRecordCount":`, `"TotalRecordCount":0,"Was":`, 1)
			}})
			result, err := c.ReadAll(t.Context(), SearchOptions{PageSize: 10, SearchTerm: "zzyzx"}, ToAnswer, func([]Item) bool { return true })
			want := ""
			if change {
				want = changedNote + " (95 were read; the server gave no count)"
			}
			if err != nil || result.Changed() != want || result.Read != 95 {
				t.Errorf("%s: a search counted as 0 (changed %v) = %+v, %q, %v; want %q", backend, change, result, result.Changed(), err, want)
			}
		}

		// listing at most three times the limit of a search
		for _, matches := range []int{50, 100} {
			ids := numbered("a", matches)
			c, _ := newFake(t, backend, map[string]route{"GET /Items": func(r *http.Request, _ string) (int, string) {
				start, limit := startLimit(r)
				listed := min(len(ids), 3*limit)
				return http.StatusOK, itemsAnswer(ids[min(start, listed):min(start+limit, listed)], listed)
			}})
			result, err := c.ReadAll(t.Context(), SearchOptions{PageSize: 10, SearchTerm: "zzyzx"}, ToAnswer, func([]Item) bool { return true })
			switch {
			case matches == 50 && (err != nil || result.Read != 50 || result.Changed() != ""):
				t.Errorf("%s: a search of 50 under the server's limit = %+v, %q, %v", backend, result, result.Changed(), err)
			case matches == 100 && (err == nil || !strings.Contains(err.Error(), "at most three times")):
				t.Errorf("%s: a search of 100 over the server's limit = %+v, %v; want it refused", backend, result, err)
			}
		}
	}
}

// A saved-since is checked once, where every item query goes: a value that is
// no date is refused before anything is asked, saying so, where Emby answered
// a bare 500 and Jellyfin a bare 400. A date alone, or a time with or without
// its zone, goes to the server as given; both read each.
func TestSearchChecksASavedSince(t *testing.T) {
	t.Parallel()

	for _, backend := range []Backend{Emby, Jellyfin} {
		for since, fine := range map[string]bool{
			"2026-01-02": true, "2026-01-02T15:04:05Z": true, "2026-01-02T15:04:05": true, "2026-01-02T15:04:05.1234567Z": true, "2026-01-02T15:04:05+10:00": true,
			"last tuesday": false, "02/01/2026": false, "2026-13-02": false,
		} {
			c, f := newFake(t, backend, map[string]route{"GET /Items": ok(`{"Items":[],"TotalRecordCount":0}`)})
			_, _, err := c.Search(t.Context(), SearchOptions{SavedSince: since})
			requests := f.all("GET /Items")
			switch {
			case fine && (err != nil || len(requests) != 1 || requests[0].query.Get("MinDateLastSaved")+requests[0].query.Get("minDateLastSaved") != since):
				t.Errorf("%s: saved since %q = %v after %d requests", backend, since, err, len(requests))
			case !fine && (err == nil || !strings.Contains(err.Error(), "is not a date") || len(requests) != 0):
				t.Errorf("%s: saved since %q = %v after %d requests; want it refused as no date, nothing asked", backend, since, err, len(requests))
			}
		}
	}
}

// A read of every match walks in an order that settles ties between items
// of one name, and a caller's own order, or a search's relevance, is left as
// it is: in the servers' default order two items of one name swapped places
// between pages.
func TestReadAllReadsInASettledOrder(t *testing.T) {
	t.Parallel()

	for _, backend := range []Backend{Emby, Jellyfin} {
		for _, tc := range []struct {
			opts SearchOptions
			sort string
		}{
			{SearchOptions{IncludeItemTypes: "Movie"}, "DateCreated,SortName"},
			{SearchOptions{IncludeItemTypes: "Movie", SortBy: "ProductionYear,SortName"}, "ProductionYear,SortName"},
			{SearchOptions{SearchTerm: "alien"}, ""},
		} {
			l := &library{ids: numbered("a", 3)}
			c, f := newFake(t, backend, map[string]route{"GET /Items": l.route()})
			if _, err := c.ReadAll(t.Context(), tc.opts, ToAnswer, func([]Item) bool { return true }); err != nil {
				t.Fatal(err)
			}
			q := f.only("GET /Items").query
			if got := strings.Join(queryList(q, "SortBy"), ","); got != tc.sort {
				t.Errorf("%s %+v: sorted by %q, want %q", backend, tc.opts, got, tc.sort)
			}
		}
	}
}

// An id is asked after as one only when it is shaped like the server's:
// Emby answers a name asked after as an id with a 500.
func TestLooksLikeID(t *testing.T) {
	t.Parallel()

	for backend, ids := range map[Backend]map[string]bool{
		Emby: {"117": true, "0": true, "": false, "Ridley Scott": false, "0badc0de0badc0de0badc0de0badc0de": false, "12a": false},
		Jellyfin: {
			"0badc0de0badc0de0badc0de0badc0de": true, "0BADC0DE0BADC0DE0BADC0DE0BADC0DE": true, "0badc0de-0bad-c0de-0bad-c0de0badc0de": true,
			"117": false, "": false, "Ridley Scott": false, "0badc0de0badc0de0badc0de0badc0dz": false, "0badc0de0badc0de0badc0de0badc0de0": false,
		},
	} {
		c, _ := newFake(t, backend, map[string]route{})
		for id, want := range ids {
			if got := c.LooksLikeID(id); got != want {
				t.Errorf("%s: LooksLikeID(%q) = %v, want %v", backend, id, got, want)
			}
		}
	}
}
