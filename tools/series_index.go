package tools

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
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
// scan, the server's own web client - and a name the index cannot place
// falls back to a live search regardless, so a show added a minute ago still
// resolves.
const seriesIndexTTL = 5 * time.Minute

// commonWord is how many series a title word can name before it stops being
// worth gathering candidates by: "the" names thousands of shows and narrows
// nothing.
const commonWord = 250

type seriesIndex struct {
	items      []embyfin.Item
	byID       map[string]int
	byWord     map[string][]int
	byProvider map[string][]int
	read       time.Time
}

// seriesCache holds an index per library, and one across all of them for
// finding the same show held twice.
type seriesCache struct {
	mu      sync.Mutex
	indexes map[string]*seriesIndex
}

func (r *registry) seriesCache() *seriesCache {
	r.seriesOnce.Do(func() { r.series = &seriesCache{indexes: map[string]*seriesIndex{}} })

	return r.series
}

// invalidate drops every index, so the next call reads the library again.
func (c *seriesCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	clear(c.indexes)
}

// get returns the index for a library ("" for every library), reading it
// when there is none or it has gone stale.
func (c *seriesCache) get(ctx context.Context, client *embyfin.Client, parent string) (*seriesIndex, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if idx, ok := c.indexes[parent]; ok && time.Since(idx.read) < seriesIndexTTL {
		return idx, nil
	}
	idx, err := readSeriesIndex(ctx, client, parent)
	if err != nil {
		return nil, err
	}
	c.indexes[parent] = idx

	return idx, nil
}

func readSeriesIndex(ctx context.Context, client *embyfin.Client, parent string) (*seriesIndex, error) {
	idx := &seriesIndex{
		byID:       map[string]int{},
		byWord:     map[string][]int{},
		byProvider: map[string][]int{},
		read:       time.Now(),
	}

	for start := 0; ; {
		page, total, err := client.Search(ctx, embyfin.SearchOptions{
			IncludeItemTypes: "Series", ParentID: parent,
			SortBy: "SortName", SortOrder: "Ascending",
			StartIndex: start, Limit: episodePageMax,
			Fields: "Path,ProductionYear,OriginalTitle,ProviderIds",
		})
		if err != nil {
			return nil, err
		}
		idx.items = append(idx.items, page...)
		start += len(page)
		if len(page) == 0 || start >= total {
			break
		}
	}

	for i := range idx.items {
		it := &idx.items[i]
		idx.byID[it.ID] = i
		words := strings.Fields(normaliseTitle(it.Name))
		words = append(words, strings.Fields(normaliseTitle(it.OriginalTitle))...)
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
	}

	return idx, nil
}

// rank scores the series a parsed name could mean, best first. Only series
// sharing a word with the name are scored: across a library of thousands that
// is the difference between scoring a handful and scoring all of them, and a
// series sharing no word with a name does not score above nothing anyway.
func (idx *seriesIndex) rank(rel release) []seriesCandidate {
	words := strings.Fields(normaliseTitle(rel.Title))

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
func (r *registry) matchSeries(ctx context.Context, rel release, parent string) ([]seriesCandidate, []embyfin.Item, error) {
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
	for _, it := range found {
		if _, ok := idx.byID[it.ID]; !ok {
			stale = true
		}
		if !slices.ContainsFunc(seen, func(s embyfin.Item) bool { return s.ID == it.ID }) {
			seen = append(seen, it)
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
// server's UI points at.
//
// This is not tidiness. A show split across two entries is split by EPISODE -
// one entry holding season 1 and another holding the rest is a real shape in a
// real library - so an exists check against one of them answers "known:
// false" for an episode the library is holding in the other.
func (r *registry) otherEntriesFor(ctx context.Context, series *embyfin.Item) []embyfin.Item {
	idx, err := r.seriesCache().get(ctx, r.client, "")
	if err != nil {
		return nil // a warning we could not raise is not an error in the answer
	}

	seen := map[string]bool{series.ID: true}
	var out []embyfin.Item
	for _, provider := range []string{"tmdb", "tvdb", "imdb"} {
		id := providerID(series, provider)
		if id == "" {
			continue
		}
		for _, i := range idx.byProvider[provider+":"+id] {
			if it := idx.items[i]; !seen[it.ID] {
				seen[it.ID] = true
				out = append(out, it)
			}
		}
	}

	return out
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
