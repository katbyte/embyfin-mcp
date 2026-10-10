package embyfin

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// idsPerRead is how many ids one request asks for at once.
const idsPerRead = 100

// joinNotes joins two notes with "; ", either of which may be empty.
func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}

	return a + "; " + b
}

// The files behind the items a read answers with.
//
// Jellyfin folds a second file of one film or episode in one folder into the
// item as a version, and no item query lists that file on its own: its path
// is in the item's media sources and nowhere else. A read that leaves the
// media sources out, to be cheap, names the item's first file alone, and a
// caller comparing a folder against it reads the second file as not in the
// library. Emby holds every file as an item of its own, one file each, so
// there is nothing folded to read back.

// ItemByIDOrVersion reads the item an id names, or the version folded into
// another item it names: Jellyfin lists a second file of a film or an episode
// in one folder by an id of its own (versions[].id, other_copies), which no
// item query finds and the single read in a user's view answers, as the
// server's image, subtitle and similar reads do; version says it was that.
// An id neither finds is a NoItemError. Emby holds every file as an item of its own, which
// the item query finds.
func (c *Client) ItemByIDOrVersion(ctx context.Context, id string) (item *Item, version bool, err error) {
	item, err = c.ItemByID(ctx, id)
	var none *NoItemError
	if err == nil || !errors.As(err, &none) {
		return item, false, err
	}
	item, err = c.versionByID(ctx, id, none)

	return item, err == nil, err
}

// versionByID reads the version an id names through the single read in the
// first administrator's view, answering none when that finds no such item:
// the fallback for an id the item query found nothing for (see
// itemOrVersion).
func (c *Client) versionByID(ctx context.Context, id string, none *NoItemError) (*Item, error) {
	if c.isEmby() {
		return nil, none
	}
	admin, err := c.ResolveUser(ctx, "")
	if err != nil {
		return nil, err
	}
	version, err := c.UserItem(ctx, admin.ID, id)
	switch {
	case IsNotFound(err):
		return nil, none
	case err != nil:
		return nil, err
	case version == nil || version.ID != id:
		return nil, none
	}

	return version, nil
}

// FieldVersionCount is what a read asks for to know which items Jellyfin
// holds in more than one file, without paying for every item's streams.
const FieldVersionCount = "MediaSourceCount"

// WithVersionFiles reads back the media sources of the items a read took
// without them and that Jellyfin says it holds in more than one file (the
// read must ask for FieldVersionCount), a batch of ids at a time.
func (c *Client) WithVersionFiles(ctx context.Context, items []Item) error {
	var folded []string
	at := map[string][]int{}
	for i := range items {
		if items[i].MediaSourceCount > 1 && len(items[i].MediaSources) < items[i].MediaSourceCount {
			if _, seen := at[items[i].ID]; !seen {
				folded = append(folded, items[i].ID)
			}
			at[items[i].ID] = append(at[items[i].ID], i)
		}
	}
	for chunk := range slices.Chunk(folded, idsPerRead) {
		read, _, err := c.Search(ctx, SearchOptions{IDs: strings.Join(chunk, ","), Fields: "Path,MediaSources", Limit: len(chunk)})
		if err != nil {
			return err
		}
		for i := range read {
			for _, j := range at[read[i].ID] {
				items[j].MediaSources = read[i].MediaSources
			}
		}
	}
	// fewer files read back than the server counted leaves the rest unknown,
	// which a row naming the ones read would not say
	for _, id := range folded {
		if it := &items[at[id][0]]; len(it.MediaSources) < it.MediaSourceCount {
			return fmt.Errorf("the server counts %d files for %s (id %s), and reading them back found %d: ask again", it.MediaSourceCount, it.Name, it.ID, len(it.MediaSources))
		}
	}

	return nil
}

// VersionsOf is every file the server shows an item in. Jellyfin answers
// them on every read of the item; Emby only on the single read in a user's
// view, so there the first administrator's is asked.
func (c *Client) VersionsOf(ctx context.Context, it *Item) ([]MediaSource, error) {
	if !c.isEmby() || !it.HasFile() || it.IsFolder {
		return it.MediaSources, nil
	}
	admin, err := c.ResolveUser(ctx, "")
	if err != nil {
		return nil, err
	}
	shown, err := c.UserItem(ctx, admin.ID, it.ID)
	if err != nil {
		return nil, err
	}
	if len(shown.MediaSources) == 0 {
		return it.MediaSources, nil
	}

	return shown.MediaSources, nil
}

// Versions, as the servers show them to people.
//
// Jellyfin merges the files of one film in one folder into one item with
// several versions when it scans, and every read of the item answers with all
// of them. Emby 4.10 merges too, but only in a user's view: a sweep of /Items
// holds each file as an item of its own with one version, a list in a user's
// view hides all but one of the merged items without naming the others as
// its versions, and only the single item read in a user's view lists every
// version. So on Emby what people are shown is read in three steps: the
// sweep; the same sweep in an administrator's view, where the items not
// listed are versions of ones that are; and placing each item not listed with
// the item it was merged into.
//
// The view names no version of what it lists (4.10: not with its media
// sources asked for, nor read by id, in the view or out of it); only the
// single read of an item does, and reading each - 901 films held in two
// files, of 20,244 - was 901 reads. Emby merges the items that share its
// presentation key (PresentationUniqueKey), which it answers when asked. Seen
// on 4.10, beside the single reads, for every case below:
//
//   - a film with a TMDB id: "p-tmdb-Movie-<tmdb>-<library>", so a TMDB id
//     shared merges, across folders and whatever else differs;
//   - a film with an IMDb id and no TMDB id: "p-imdb-Movie-<imdb>-<library>",
//     so an IMDb id merges only where neither film has a TMDB id - one beside
//     a film holding that IMDb id and a TMDB id stays apart, as does one
//     sharing an IMDb id under a TMDB id of its own;
//   - a film with neither: a key of its own, which a second file named as its
//     version in its folder shares;
//   - no other id counts: a website, or a Facebook or X account, merges
//     nothing;
//   - an episode: "<series key>-<season> - <episode>", the series key from its
//     TVDB id, so the same number under two folders of one show merges, and
//     two episodes of different numbers sharing a TVDB id stay apart.
//
// So an item not listed is placed with the one listed item sharing its key
// (placedByKey); one no key places, or that two listed items share, is read
// on its own. A sample of the placings is read too, and one that disagrees
// drops them all: every item not listed is then read on its own, as far as a
// bound allows.

// shownHiddenMax is the most items left out of an administrator's view that
// shownGroups reads one at a time: each is a request of its own, and a view
// leaving out more than this that no key places is refused rather than read
// item by item.
const shownHiddenMax = 1000

// shownSample is how many placings by key shownGroups checks against a read
// of each item, spread across them.
const shownSample = 20

// placedByKey places each item an administrator's view leaves out with the
// one listed item sharing its presentation key, as Emby merges them (see
// above): the index into listed for each, or -1 for one with no key, or whose
// key no listed item has, or two do.
func placedByKey(listed, left []Item) []int {
	byKey := map[string][]int{}
	for i := range listed {
		if key := listed[i].PresentationKey; key != "" {
			byKey[key] = append(byKey[key], i)
		}
	}
	owners := make([]int, len(left))
	for h := range left {
		owners[h] = -1
		if fits := byKey[left[h].PresentationKey]; len(fits) == 1 {
			owners[h] = fits[0]
		}
	}

	return owners
}

// ShownItem is one item as the server shows it to people, with the items it
// is stored as.
type ShownItem struct {
	// the item people are shown, carrying every version it holds
	Item
	// Stored is what the server stores it as: on Jellyfin the item itself, on Emby
	// the listed item and each item merged into it, every one with its own
	// file, dates and facts
	Stored []Item
}

// Shown sweeps opts and answers with the items as the server shows them
// to people, each with every version it holds: on Jellyfin the sweep itself,
// on Emby the items an administrator's view lists, each carrying the versions
// of the items merged into it. The note says whether the library was seen to
// change under the reads, and placing, on Emby, how the items shown only as
// versions of others were placed (see shownGroups).
func (c *Client) Shown(ctx context.Context, opts SearchOptions) (items []Item, note, placing string, err error) {
	groups, note, placing, err := c.ShownGroups(ctx, opts)
	if err != nil {
		return nil, "", "", err
	}
	items = make([]Item, 0, len(groups))
	for i := range groups {
		items = append(items, groups[i].Item)
	}

	return items, note, placing, nil
}

// ShownGroups is Shown keeping, beside each item shown, the items it is
// stored as: what an audit judging an item by all its files, but listing
// facts file by file, reads.
func (c *Client) ShownGroups(ctx context.Context, opts SearchOptions) (groups []ShownItem, note, placing string, err error) {
	if c.isEmby() {
		// the key Emby merges versions by, answered only when asked for
		opts.Fields = cmp.Or(opts.Fields, FieldsDefault) + ",PresentationUniqueKey"
	}
	var all []Item
	swept, err := c.ReadAll(ctx, opts, ToAnswer, func(items []Item) bool {
		all = append(all, items...)

		return true
	})
	if err != nil {
		return nil, "", "", err
	}
	if !c.isEmby() || len(all) == 0 {
		out := make([]ShownItem, 0, len(all))
		for i := range all {
			out = append(out, ShownItem{Item: all[i], Stored: all[i : i+1]})
		}

		return out, swept.Changed(), "", nil
	}

	admin, err := c.ResolveUser(ctx, "")
	if err != nil {
		return nil, "", "", err
	}
	listed := map[string]bool{}
	view := opts
	view.UserID, view.Fields = admin.ID, "Path"
	viewed, err := c.ReadAll(ctx, view, ToAnswer, func(items []Item) bool {
		for i := range items {
			listed[items[i].ID] = true
		}

		return true
	})
	if err != nil {
		return nil, "", "", err
	}
	note = joinNotes(swept.Changed(), viewed.Changed())
	// an item the view lists that the read of the items does not came in
	// between the two reads: it is left out, and said to be
	stored := make(map[string]bool, len(all))
	for i := range all {
		stored[all[i].ID] = true
	}
	came := 0
	for id := range listed {
		if !stored[id] {
			came++
		}
	}
	if came > 0 {
		note = joinNotes(note, fmt.Sprintf("the library changed while it was read: %d item(s) came in between its two reads and are left out", came))
	}

	// the listed items by id and by file, which are how a merged item's
	// versions name it
	byID, byPath := map[string]int{}, map[string]int{}
	var shown []ShownItem
	var left []Item
	for i := range all {
		if listed[all[i].ID] {
			byID[all[i].ID], byPath[all[i].Path] = len(shown), len(shown)
			shown = append(shown, ShownItem{Item: all[i], Stored: []Item{all[i]}})

			continue
		}
		left = append(left, all[i])
	}

	// the read of an item on its own: which listed item's versions it is
	// among (-1 for none), every version it names, and whether it has gone
	type single struct {
		owner   int
		sources []MediaSource
		gone    bool
	}
	readOne := func(it *Item) (single, error) {
		one, uerr := c.UserItem(ctx, admin.ID, it.ID)
		if IsNotFound(uerr) {
			// removed since the sweep read it, or only kept from this
			// account's view: the item query, which every item is in,
			// tells the two apart
			_, lerr := c.ItemByID(ctx, it.ID)
			var none *NoItemError
			switch {
			case errors.As(lerr, &none):
				return single{owner: -1, gone: true}, nil
			case lerr != nil:
				return single{}, lerr
			}
		}
		if uerr != nil {
			return single{}, uerr
		}
		for _, src := range one.MediaSources {
			j, ok := byID[src.ItemID]
			if !ok {
				j, ok = byPath[src.Path]
			}
			if ok {
				return single{owner: j, sources: one.MediaSources}, nil
			}
		}

		return single{owner: -1, sources: one.MediaSources}, nil
	}

	// each item left out placed by the key Emby merges by, and a sample of
	// those placings, spread across them, checked against a read of each
	listedItems := make([]Item, len(shown))
	for i := range shown {
		listedItems[i] = shown[i].Item
	}
	owners := placedByKey(listedItems, left)
	var placed []int
	for h, owner := range owners {
		if owner >= 0 {
			placed = append(placed, h)
		}
	}
	reads := map[int]single{}
	sampled, disagreed := 0, ""
	if n := min(shownSample, len(placed)); n > 0 {
		for i := range n {
			h := placed[i*len(placed)/n]
			one, rerr := readOne(&left[h])
			if rerr != nil {
				return nil, "", "", rerr
			}
			reads[h] = one
			sampled++
			if !one.gone && one.owner != owners[h] {
				disagreed = left[h].ID

				break
			}
		}
	}
	if disagreed != "" {
		for h := range owners {
			owners[h] = -1
		}
		placed = nil
	}

	// the rest read one at a time: bounded, as a view leaving out more than a
	// few that no key places is not a view of merged versions (and was a read
	// of every item, one request each, on a library of hundreds of
	// thousands)
	unplaced := len(left) - len(placed)
	if unplaced > shownHiddenMax {
		why := "that share no key with an item it lists"
		if disagreed != "" {
			why = fmt.Sprintf("(the key Emby merges versions by disagreed with a read of %s)", disagreed)
		}
		return nil, "", "", fmt.Errorf("the administrator's view on Emby leaves out %d of the %d items read %s, more than the %d it reads one at a time to find what each is a version of: read one library at a time, or fewer kinds", unplaced, len(all), why, shownHiddenMax)
	}
	gone, goneByKey := 0, 0
	for h := range left {
		if owner := owners[h]; owner >= 0 {
			if reads[h].gone {
				gone++
				goneByKey++

				continue
			}
			shown[owner].MediaSources = append(shown[owner].MediaSources, left[h].MediaSources...)
			shown[owner].Stored = append(shown[owner].Stored, left[h])

			continue
		}
		one, ok := reads[h]
		if !ok {
			if one, err = readOne(&left[h]); err != nil {
				return nil, "", "", err
			}
		}
		switch {
		case one.gone:
			gone++
		case one.owner < 0:
			// merged into nothing this sweep reaches (or not merged at all,
			// only left out of the view): it stands on its own
			shown = append(shown, ShownItem{Item: left[h], Stored: []Item{left[h]}})
		default:
			// every version, as the single read lists them: the listed
			// item's own file among them
			shown[one.owner].MediaSources = one.sources
			shown[one.owner].Stored = append(shown[one.owner].Stored, left[h])
		}
	}
	if gone > 0 {
		note = joinNotes(note, fmt.Sprintf("the library changed while it was read: %d item(s) it read were removed before it ended, and are left out", gone))
	}
	switch {
	case disagreed != "":
		placing = fmt.Sprintf("on Emby, the key Emby merges versions by disagreed with a read of %s, so all %d items shown only as versions of others were read one at a time", disagreed, len(left))
	case len(placed) > 0:
		placing = fmt.Sprintf("on Emby, of the %d items shown only as versions of others, %d were placed by the key Emby merges versions by (%d of them checked against a read of each, all agreeing)", len(left), len(placed)-goneByKey, sampled)
		if unplaced > 0 {
			placing += fmt.Sprintf(", and %d were read one at a time", unplaced)
		}
	}

	return shown, note, placing, nil
}
