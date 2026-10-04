package tools

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/mediapath"
	"github.com/katbyte/embyfin-mcp/lib/naming"
)

// The library's series, read once and matched against in process.
//
// Matching a name by asking the server's search depends on the search
// returning the right show, and it does not always: a release spelling an
// acronym with spaces where the library has points, a dotted acronym that
// ranks below four shows sharing a word with it, a search that folds
// punctuation on one server and not the other. Each of those was a real miss.
// The index holds every series, so the scoring decides rather than the
// search's relevance order - and a batch of names costs one read of the
// index instead of up to four searches a name.
//
// The same read answers which library entries hold the same show, by
// provider id, without a search per series.

// seriesIndexTTL is how long a read of the index is trusted. A write through
// this server drops it at once; the TTL is for changes made anywhere else - a
// scan, the server's own web client, a folder renamed on disk - and a name
// the index cannot place falls back to a live search regardless, so a show
// added a moment ago still resolves. What the index alone answers is the
// negative: no other entry holds this show, no series folder holds this path.
// Trusted for five minutes, a second entry made by a scan in that time was
// not there to be seen, so the TTL is a minute - a read of every series is a
// page or three, and a loop of calls pays for one a minute - and plan_check,
// which answers where a write lands, reads the index afresh every call.
const seriesIndexTTL = time.Minute

// A write drops the index, but several writes finish after they return:
// library_scan, item_refresh, task_run and item_identify_apply only queue the
// work. A lookup a few seconds later read the library as it was before the
// scan got to it and trusted that for the whole TTL, so plan_check could not
// see a folder just scanned and the duplicate check could not see an entry
// just made. So for a while after any write an index is trusted only
// briefly, and read again once that has passed, until the writes have had
// time to land.
const (
	// seriesIndexSettle is how long after a write the library is taken to
	// be still changing
	seriesIndexSettle = 5 * time.Minute
	// seriesIndexSettlingTTL is how long an index read in that time is
	// trusted: long enough that one batch of names costs one read, short
	// enough that the next call sees what the scan has done since
	seriesIndexSettlingTTL = 10 * time.Second
)

// commonWord is how many series a title word can name before it stops being
// worth gathering candidates by: "the" names thousands of shows and narrows
// nothing.
const commonWord = 250

type seriesIndex struct {
	items      []embyfin.Item
	byID       map[string]int
	byWord     map[string][]int
	byProvider map[string][]int
	// byFolder is the series by the folder rule audit_duplicates' folder_groups
	// groups by (twinFolderKey): two folders of one show beside each other
	byFolder map[string][]int
	read     time.Time
	// ttl is how long this read is trusted: the full TTL, or the short one
	// when it was read while a write may still have been landing
	ttl time.Duration
}

// seriesCache holds an index per library, and one across all of them for
// finding the same show held twice.
type seriesCache struct {
	mu      sync.Mutex
	indexes map[string]*seriesIndex
	// lastWrite is when a write through this server last dropped the index
	lastWrite time.Time
	// now is the clock, for a test to move; nil is the wall clock
	now func() time.Time
}

func (r *registry) seriesCache() *seriesCache {
	r.seriesOnce.Do(func() { r.series = &seriesCache{indexes: map[string]*seriesIndex{}} })

	return r.series
}

func (c *seriesCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}

	return time.Now()
}

// invalidate drops every index, so the next call reads the library again,
// and marks the library as changing for a while (see seriesIndexSettle). It
// waits for a read in progress, so an index read before the write cannot be
// stored after it.
func (c *seriesCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	clear(c.indexes)
	c.lastWrite = c.clock()
}

// get returns the index for a library ("" for every library), reading it
// when there is none or it has gone stale.
func (c *seriesCache) get(ctx context.Context, client *embyfin.Client, parent string) (*seriesIndex, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if idx, ok := c.indexes[parent]; ok && c.clock().Sub(idx.read) < idx.ttl {
		return idx, nil
	}

	return c.readLocked(ctx, client, parent)
}

// fresh reads the index for a library now, whatever is held, and keeps the
// read for the calls after it: for an answer that must not rest on a read
// from before a scan the caller cannot see.
func (c *seriesCache) fresh(ctx context.Context, client *embyfin.Client, parent string) (*seriesIndex, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.readLocked(ctx, client, parent)
}

// readLocked reads and keeps the index for a library; c.mu is held.
func (c *seriesCache) readLocked(ctx context.Context, client *embyfin.Client, parent string) (*seriesIndex, error) {
	now := c.clock()
	idx, err := readSeriesIndex(ctx, client, parent)
	if err != nil {
		return nil, err
	}
	idx.read, idx.ttl = now, seriesIndexTTL
	if !c.lastWrite.IsZero() && now.Sub(c.lastWrite) < seriesIndexSettle {
		idx.ttl = seriesIndexSettlingTTL
	}
	c.indexes[parent] = idx

	return idx, nil
}

func readSeriesIndex(ctx context.Context, client *embyfin.Client, parent string) (*seriesIndex, error) {
	idx := &seriesIndex{
		byID:       map[string]int{},
		byWord:     map[string][]int{},
		byProvider: map[string][]int{},
		byFolder:   map[string][]int{},
		read:       time.Now(),
	}

	for start := 0; ; {
		page, total, err := client.Search(ctx, embyfin.SearchOptions{
			IncludeItemTypes: "Series", ParentID: parent,
			SortBy: "SortName,DateCreated", SortOrder: "Ascending",
			StartIndex: start, Limit: episodePageMax,
			Fields: "Path,ProductionYear,OriginalTitle,ProviderIds",
		})
		if err != nil {
			return nil, err
		}
		// a series read twice across a page boundary would be a show held
		// twice (otherEntriesFor): it is kept once
		for i := range page {
			if _, seen := idx.byID[page[i].ID]; !seen {
				idx.byID[page[i].ID] = len(idx.items)
				idx.items = append(idx.items, page[i])
			}
		}
		start += len(page)
		if len(page) == 0 || start >= total {
			break
		}
	}

	for i := range idx.items {
		it := &idx.items[i]
		idx.byID[it.ID] = i
		words := strings.Fields(naming.Normalise(it.Name))
		words = append(words, strings.Fields(naming.Normalise(it.OriginalTitle))...)
		for _, w := range words {
			if list := idx.byWord[w]; len(list) == 0 || list[len(list)-1] != i {
				idx.byWord[w] = append(list, i)
			}
		}
		for _, provider := range []string{"tmdb", "tvdb", "imdb"} {
			if id := providerID(it, provider); id != "" {
				key := provider + ":" + id
				idx.byProvider[key] = append(idx.byProvider[key], i)
			}
		}
		if key := twinFolderKey(it.Path); key != "" {
			idx.byFolder[key] = append(idx.byFolder[key], i)
		}
	}

	return idx, nil
}

// twinFolderKey is what two folders of one show have in common when a rename
// changed only spacing, case, an accent or punctuation, by the rule
// audit_duplicates' folder_groups group by: the folder above, and the folder's own
// name, each folded (folderKey). "" for a path with no name to compare: one
// that folds to nothing ("???") would otherwise meet every other like it.
func twinFolderKey(path string) string {
	if path == "" {
		return ""
	}
	name := folderKey(mediapath.Base(path))
	if name == "" {
		return ""
	}

	return folderKey(mediapath.Dir(path)) + "/" + name
}

// sameAnime says whether two entries could be one show by their AniDB ids:
// not when each carries one and the two differ. An anime entry kept apart -
// an OVA, a film, a second season AniDB counts as a show of its own - carries
// its parent's TVDB or TMDB id beside its own AniDB id (audit_anime_ids
// reports it as ids_disagree), and read by the shared id alone it was joined
// to its parent, its episodes counted as the parent's.
func sameAnime(a, b *embyfin.Item) bool {
	x, y := providerID(a, "anidb"), providerID(b, "anidb")

	return x == "" || y == "" || x == y
}

// rank scores the series a parsed name could mean, best first. Only series
// sharing a word with the name are scored: across a library of thousands that
// is the difference between scoring a handful and scoring all of them, and a
// series sharing no word with a name does not score above nothing anyway.
func (idx *seriesIndex) rank(rel naming.Release) []seriesCandidate {
	words := strings.Fields(naming.Normalise(rel.Title))

	gather := func(capped bool) []int {
		var out []int
		for _, w := range words {
			if list := idx.byWord[w]; !capped || len(list) <= commonWord {
				out = append(out, list...)
			}
		}

		return out
	}
	candidates := gather(true)
	if len(candidates) == 0 {
		// a name made only of common words is still a name
		candidates = gather(false)
	}
	slices.Sort(candidates)
	candidates = slices.Compact(candidates)

	rows := make([]seriesCandidate, 0, len(candidates))
	for _, i := range candidates {
		it := &idx.items[i]
		if score, how := scoreSeries(rel, it); score > 0 {
			rows = append(rows, seriesCandidate{SeriesID: it.ID, Name: it.Name, Year: it.ProductionYear, Score: score, MatchedOn: how, Path: it.Path})
		}
	}
	sortCandidates(rows)

	return rows
}

// matchSeries ranks the series a name could mean against the index, falling
// back to the server's search when the index has nothing it is sure of: a
// show added since the index was read, or a fragment of a name ("Sev") that
// shares no whole word with anything and only a substring search finds. It
// also returns every series it looked at, for the caller that has to tell a
// search that found one thing from a search that found a hundred.
func (r *registry) matchSeries(ctx context.Context, rel naming.Release, parent string) ([]seriesCandidate, []embyfin.Item, error) {
	idx, err := r.seriesCache().get(ctx, r.client, parent)
	if err != nil {
		return nil, nil, err
	}

	rows := idx.rank(rel)
	seen := make([]embyfin.Item, 0, len(rows))
	for _, row := range rows {
		seen = append(seen, idx.items[idx.byID[row.SeriesID]])
	}
	if len(rows) > 0 && rows[0].Score >= seriesConfident {
		return rows, seen, nil
	}

	searched, found, err := rankSeries(ctx, r.client, rel, parent)
	if err != nil {
		return nil, nil, err
	}
	stale := false
	for i := range found {
		it := &found[i]
		if _, ok := idx.byID[it.ID]; !ok {
			stale = true
		}
		if !slices.ContainsFunc(seen, func(s embyfin.Item) bool { return s.ID == it.ID }) {
			seen = append(seen, *it)
		}
	}
	for _, row := range searched {
		if !slices.ContainsFunc(rows, func(s seriesCandidate) bool { return s.SeriesID == row.SeriesID }) {
			rows = append(rows, row)
		}
	}
	sortCandidates(rows)
	// the search saw a series the index does not hold, so the index is out of
	// date for everything else too
	if stale {
		r.seriesCache().invalidate()
	}

	return rows, seen, nil
}

// otherEntriesFor finds the other library entries for a series: the same show
// held twice, which a folder rename leaves behind and which nothing in the
// server's UI points at. Two ways: entries sharing a tmdb, tvdb or imdb id
// (unless their AniDB ids differ, see sameAnime), and entries built from a
// folder beside this one whose name differs only in spacing, case, accents
// or punctuation (twinFolderKey) - which is the usual case after a rename,
// because the second entry was matched to nothing and carries no ids at all.
//
// This is not tidiness. A show split across two entries is split by EPISODE -
// one entry holding season 1 and another holding the rest is a real shape in a
// real library - so an exists check against one of them answers "known:
// false" for an episode the library is holding in the other.
//
// An entry sharing the id whose AniDB id differs is returned apart, in
// anime: not the same show, but an episode the provider numbers into this
// one may be filed there.
//
// It fails when the library's series could not be read, and the caller has to
// say so: answering "no other entries" in its place is the one thing that
// makes an absence read as proof.
func (r *registry) otherEntriesFor(ctx context.Context, series *embyfin.Item) (same, anime []embyfin.Item, err error) {
	idx, err := r.seriesCache().get(ctx, r.client, "")
	if err != nil {
		return nil, nil, err
	}

	seen := map[string]bool{series.ID: true}
	take := func(list []int) {
		for _, i := range list {
			it := idx.items[i]
			if seen[it.ID] {
				continue
			}
			seen[it.ID] = true
			if sameAnime(series, &it) {
				same = append(same, it)
			} else {
				anime = append(anime, it)
			}
		}
	}
	for _, provider := range []string{"tmdb", "tvdb", "imdb"} {
		if id := providerID(series, provider); id != "" {
			take(idx.byProvider[provider+":"+id])
		}
	}
	if key := twinFolderKey(series.Path); key != "" {
		take(idx.byFolder[key])
	}

	return same, anime, nil
}

func sortCandidates(rows []seriesCandidate) {
	slices.SortFunc(rows, func(a, b seriesCandidate) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}

			return 1
		}

		return strings.Compare(a.Name, b.Name)
	})
}
