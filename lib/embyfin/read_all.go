package embyfin

import (
	"context"
	"errors"
	"fmt"
)

// Reading every item a query matches, page by page, while the library keeps
// changing under the read.
//
// A library in use gains and loses items all the time: a download lands, an
// upgrade replaces a file, a scan drops what is gone. Paged by a plain offset,
// every item added before the read's position pushes an item it has read onto
// the next page, where it is read twice, and every item removed before it
// pulls an unread one back onto a page already read, where it is never read
// at all. An item added is not added at the end either: both servers date an
// item by its file (Jellyfin's UseFileCreationTimeForDateAdded is on by
// default), so a file imported today with an old timestamp, or moved or
// hard-linked in, sorts among the oldest and shifts everything after it.
//
// So each request starts a little before where the last one ended and asks
// for the overlap and a page: the ids already handed on are known and left
// out, and the read finds its place again by the last items it read, wherever
// the changes moved them. An item removed before the position pulls those
// back into the overlap; one added pushes them forward into the page. When
// they are not where they were, it reads wider, and fails only when even that
// cannot find them. It never fails for the library growing, and it stops at a
// page that comes back short rather than at the count the first page gave.
//
// What it cannot do is read an item added behind it once it has passed, or
// take back one it read that is gone. It says the library changed whenever it
// sees a sign of either: a count that differs from the first, or the last
// items read anywhere but at the start of the next request. As many items
// added as removed, all among those already read, between the same two
// requests, leave both where they were; so a read of more than one request
// that saw neither reads every match once more at the end, for the ids alone,
// and compares. An item there at the end that it never handed on was added
// behind it, or moved there by an edit, a play or a change to an account's
// access; one it handed on that is not there at the end has gone. So no item
// comes into the read or leaves it between its first page and its last
// unseen. What is not seen is an item it read being changed after it read
// it - its file probed again, its genres edited, its watch state in a read
// not filtered by it: each item is answered as it was when read.
//
// It reads again rather than asking what the server saved during the read:
// Jellyfin 12.1 put an item back at a path it had just held without saving it
// anew, and the saved date said nothing had come.
//
// A read the library changed too much to follow stops, whatever it is for,
// and fails with a CutError, handing back what it read: an answer that lists
// what it found can use that and say so, one that says what is not there or
// how many there are cannot. A read whose check at the end fails was whole,
// and does what its caller said it is for: a read to answer ends and says
// so; a read to act, which decides a change to the server, fails, so nothing
// is changed on it.
//
// Neither server counts every search. Emby counts 0 beside a page of
// matches to some searches sent with a limit (4.10: a search matching 40 counts
// 0 at every limit, one matching 4 counts 4), and such a read is uncounted:
// what a count would have told is left to the read again. Jellyfin lists at
// most three times the limit of a search and counts that many (12.1: a limit
// of 10 counts 30 and lists nothing from 30 on); a read that reaches that
// many fails, rather than answer part of the matches as all of them.

// readPage is how many items a read asks for beyond its overlap, unless
// SearchOptions.PageSize says otherwise.
const readPage = 1000

// readOverlap is how many items before its position each request re-reads to
// find its place: as many removals between two requests as it absorbs
// before it has to read wider.
const readOverlap = 100

// readAllSort is the order a read walks in when the caller gives none: when
// each item was added, then its name. The servers' default ties every item of
// one name (a remake, a second copy) and settles the tie differently from one
// request to the next; this order settles most ties, though not an item added
// mid-read, which can sort anywhere (see above).
const readAllSort = "DateCreated,SortName"

// readAgainPage is the most items a request of the read again at a read's end
// asks for beyond its overlap: they are read for their ids alone, so ten
// times a read's page, up to this.
const readAgainPage = 10000

// readAgainFields is the one field the read again asks for: the least a
// server answers with, beside the id.
const readAgainFields = "DateCreated"

// ReadPurpose is what a read of every match is for, which every caller of
// ReadAll says: it decides what a read that cannot be sure does (see above).
type ReadPurpose int

const (
	// ToAnswer is a read whose items are an answer: when the check at its
	// end fails, it answers what it read and says so.
	ToAnswer ReadPurpose = iota + 1
	// ToAct is a read that decides a change to the server - what to delete,
	// rename or add: when the check at its end fails it fails, and nothing
	// is changed on it.
	ToAct
)

// CutError is a read the library changed too much to follow: it stopped
// after Read items, whatever it was for, and the rest are missing. The
// result handed back with it holds what it read; an answer that says what is
// not there, or how many there are, cannot be given from that.
type CutError struct{ Read int }

func (e *CutError) Error() string {
	return fmt.Sprintf("the library changed too much while it was read to follow it, so it stopped after %d items and the rest are missing: ask again", e.Read)
}

// ReadResult is what a read of every match saw.
type ReadResult struct {
	// Read is how many items were handed on, each once.
	Read int
	// First is how many the server said match at the first request.
	First int
	// Total is how many it said match at the last request.
	Total int
	// ChangeSeen is set when a count differed from the first, the last items
	// one request read were anywhere but at the start of the next, or the
	// read again at the end found other items than the read handed on.
	ChangeSeen bool
	// Missed is how many items the read again at the end found that the
	// read did not hand on: added behind it, or moved there.
	Missed int
	// Gone is how many items the read handed on that the read again at the
	// end did not find.
	Gone int
	// Stopped is set when the caller stopped the read before its end.
	Stopped bool
	// Uncounted is set when the server gave no count: 0 beside a page of
	// items, as Emby does for some searches. First and Total are then not
	// counts, and nothing is judged by them.
	Uncounted bool
	// Cut is set when the read stopped before its end because the library
	// changed too much to follow (and ReadAll failed with a CutError): the
	// items after it are missing.
	Cut bool
	// Unchecked is why the read again at the end of a read to answer could
	// not be made, "" when it was made or not needed.
	Unchecked string
}

// Changed is what to say of how the library moved under the read, or of what
// the read could not tell; "" when no item was seen to come or go between its
// first page and its last (see above).
func (r ReadResult) Changed() string {
	switch {
	case r.Cut:
		return (&CutError{Read: r.Read}).Error()
	case r.ChangeSeen:
		counts := fmt.Sprintf("%d matched when the read began, %d when it ended, and %d were read", r.First, r.Total, r.Read)
		if r.Uncounted {
			counts = fmt.Sprintf("%d were read; the server gave no count", r.Read)
		}
		if r.Missed > 0 {
			counts += fmt.Sprintf(", with %d there at the end not among them", r.Missed)
		}
		if r.Gone > 0 {
			counts += fmt.Sprintf(", and %d of them gone by the end", r.Gone)
		}
		return fmt.Sprintf("the library changed while it was read: items added or removed during it may be missing, or listed though gone (%s)", counts)
	case r.Unchecked != "":
		return "whether the library changed during the read could not be checked: " + r.Unchecked
	case !r.Stopped && !r.Uncounted && r.Read != r.Total:
		return fmt.Sprintf("the server says %d match but %d different items were read, which the read cannot explain: items may be missing, or listed though gone", r.Total, r.Read)
	}

	return ""
}

// ReadAll reads every item matching opts, handing each on once to cb, a
// request's worth at a time: return false to stop. Without a sort it reads in
// readAllSort, and a search term keeps the server's own order of how well
// each item matches, which it only gives when asked for no order at all.
// The result says what the read saw; Changed says how the library moved
// under it. A server that sends a page short while counting more to come, or
// the same items again wherever it is asked to start, fails the read rather
// than have it answer part of the list as all of it, or go round forever.
//
// A read of more than one request that saw no change reads every match once
// more at its end, ids alone, ten times a page to a request (see above).
// purpose says what the read is for, and so what it does when that check
// fails. A read the library changes too much to follow fails with a
// CutError, whatever it is for.
func (c *Client) ReadAll(ctx context.Context, opts SearchOptions, purpose ReadPurpose, cb func(items []Item) bool) (ReadResult, error) {
	if purpose != ToAnswer && purpose != ToAct {
		return ReadResult{}, errors.New("a read of every match must say whether it answers a question or decides a change (embyfin.ToAnswer or embyfin.ToAct)")
	}
	page := opts.PageSize
	if page <= 0 {
		page = readPage
	}
	if opts.SortBy == "" && opts.SearchTerm == "" {
		opts.SortBy, opts.SortOrder = readAllSort, "Ascending"
	}

	read := map[string]bool{}
	result, requests, err := c.readPages(ctx, opts, page, read, cb)
	if err != nil || result.Stopped || result.Cut || result.ChangeSeen || requests < 2 {
		return result, err
	}
	missed, gone, changed, err := c.readAgain(ctx, opts, page, read)
	if err != nil {
		if purpose == ToAct {
			return result, err
		}
		result.Unchecked = err.Error()

		return result, nil
	}
	result.Missed, result.Gone = missed, gone
	result.ChangeSeen = missed > 0 || gone > 0 || changed

	return result, nil
}

// readAgain reads every item matching opts once more, for the ids alone:
// how many it finds that the read did not hand on, how many the read handed
// on that it does not find, and whether the library was seen to change while
// it read.
func (c *Client) readAgain(ctx context.Context, opts SearchOptions, page int, read map[string]bool) (missed, gone int, changed bool, err error) {
	again := opts
	again.Fields, again.EnableUserData = readAgainFields, false
	found := map[string]bool{}
	result, _, err := c.readPages(ctx, again, min(10*page, max(page, readAgainPage)), found, func(items []Item) bool {
		for i := range items {
			if !read[items[i].ID] {
				missed++
			}
		}

		return true
	})
	if err != nil {
		return 0, 0, false, fmt.Errorf("reading every match again, to see whether the library changed during the read: %w", err)
	}
	for id := range read {
		if !found[id] {
			gone++
		}
	}

	return missed, gone, result.ChangeSeen, nil
}

// readPages is ReadAll's pages, handing on each item not already in seen,
// and how many requests they took.
func (c *Client) readPages(ctx context.Context, opts SearchOptions, page int, seen map[string]bool, cb func(items []Item) bool) (ReadResult, int, error) {
	overlap := min(readOverlap, page)
	requests := 0

	var result ReadResult
	counted := false
	read := func(start, count int) ([]Item, error) {
		opts.StartIndex, opts.Limit = start, count
		requests++
		items, total, err := c.Search(ctx, opts)
		if err != nil {
			return nil, err
		}
		// a count of 0 beside a page of items is no count (see above)
		if total == 0 && len(items) > 0 {
			result.Uncounted = true
		}
		if result.Uncounted {
			return items, nil
		}
		if counted && total != result.First {
			result.ChangeSeen = true
		}
		if !counted {
			result.First, counted = total, true
		}
		result.Total = total

		return items, nil
	}

	// tail is the ids of the last items the previous request read, the ones
	// the next finds its place by
	var tail []string
	pos, stale := 0, 0
	for {
		start, count := max(pos-overlap, 0), overlap+page
		items, err := read(start, count)
		if err != nil {
			return result, requests, err
		}
		if len(tail) > 0 && !startsWith(items, tail) {
			result.ChangeSeen = true
			if !placed(items, tail) {
				// removals before the position have pulled the last items
				// read back further than the overlap, or additions pushed
				// them further than a page: read a page back and three wide.
				// The count comes from this read: removals can take the
				// first past the end, where Emby counts 0.
				//
				// It is one request, so one answer, and so asks for more than
				// a page. Jellyfin orders a search's best matches by the
				// limit asked for (12.1: limits of 4 and of 14 part at the
				// sixth), so a search there may not find its place in it and
				// stop: never answer wrongly, as a read that widens has
				// already seen the library change, and says so
				start, count = max(pos-overlap-page, 0), overlap+3*page
				if items, err = read(start, count); err != nil {
					return result, requests, err
				}
				if !placed(items, tail) {
					result.Cut, result.Read = true, len(seen)

					return result, requests, &CutError{Read: len(seen)}
				}
			}
		}
		var fresh []Item
		for i := range items {
			if !seen[items[i].ID] {
				seen[items[i].ID] = true
				fresh = append(fresh, items[i])
			}
		}
		result.Read = len(seen)
		if len(fresh) > 0 && !cb(fresh) {
			result.Stopped = true
			return result, requests, nil
		}
		if len(items) < count {
			// Jellyfin lists at most three times the limit of a search, and
			// counts that many (see above)
			if opts.SearchTerm != "" && !result.Uncounted && result.Total >= 3*count && start+len(items) >= result.Total {
				return result, requests, fmt.Errorf("the server lists at most three times the page asked for a search, and this one reached that (%d), so it may match more than it lists: narrow the search", result.Total)
			}
			if !result.Uncounted && start+len(items) < result.Total {
				return result, requests, fmt.Errorf("the server sent %d items from position %d where %d were asked for, yet counts %d matching, so the read cannot tell where the list ends: ask again", len(items), start, count, result.Total)
			}
			return result, requests, nil
		}
		// a full page of nothing new is a library that grew a page behind
		// the read; two in a row is a server that ignores where a page
		// starts, which the read would ask forever
		stale++
		if len(fresh) > 0 {
			stale = 0
		}
		if stale == 2 {
			return result, requests, errors.New("the server sent only items already read, twice in a row, from further and further on: it seems to ignore where a page starts, so the read cannot go on")
		}
		pos = start + len(items)
		tail = tail[:0]
		for _, it := range items[len(items)-overlap:] {
			tail = append(tail, it.ID)
		}
	}
}

// startsWith says whether items start with the ids, in order: where the last
// items read are when nothing changed.
func startsWith(items []Item, ids []string) bool {
	if len(items) < len(ids) {
		return false
	}
	for i, id := range ids {
		if items[i].ID != id {
			return false
		}
	}

	return true
}

// placed says whether items hold the read's place: two or more of the ids of
// the last items read, one read before the other and listed before it (or
// the one id, when there is only one). One alone may be an item edited to
// sort somewhere else, which says nothing of where the read left off.
func placed(items []Item, ids []string) bool {
	at := make(map[string]int, len(ids))
	for i, id := range ids {
		at[id] = i
	}
	last := -1
	for _, it := range items {
		i, ok := at[it.ID]
		if !ok {
			continue
		}
		if len(ids) == 1 || last >= 0 && i > last {
			return true
		}
		last = i
	}

	return false
}
