package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The free-text lists an item carries, by the names the tools take them by.
// Every one is typed by hand or filled by a provider, so the same thing
// arrives spelled several ways: library_filters lists them, audit_spelling
// finds the variants and metadata_rename merges them.
const (
	fieldGenres  = "genres"
	fieldTags    = "tags"
	fieldStudios = "studios"
)

var vocabFields = []string{fieldGenres, fieldTags, fieldStudios}

// vocabField maps what a caller wrote (genre, Tags, studio...) onto the name
// in vocabFields, or returns "" for anything else.
func vocabField(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s != "" && !strings.HasSuffix(s, "s") {
		s += "s"
	}
	if slices.Contains(vocabFields, s) {
		return s
	}

	return ""
}

// valuesOf pulls a field's values off one item.
func valuesOf(field string, it *embyfin.Item) []string {
	switch field {
	case fieldGenres:
		return it.Genres
	case fieldTags:
		return it.TagNames()
	case fieldStudios:
		return it.StudioNames()
	}

	return nil
}

// valueCount is one value and how many items carry it.
type valueCount struct {
	Value string `json:"value"`
	Items int    `json:"items"`
}

// sortedCounts lists a value -> count map most used first, then by name.
func sortedCounts(m map[string]int) []valueCount {
	out := make([]valueCount, 0, len(m))
	for v, n := range m {
		out = append(out, valueCount{Value: v, Items: n})
	}
	slices.SortFunc(out, func(a, b valueCount) int {
		if a.Items != b.Items {
			return b.Items - a.Items
		}
		return strings.Compare(a.Value, b.Value)
	})

	return out
}

// vocabularyTypes are the item types the vocabulary sweeps read by default:
// what a library is browsed by. Episodes and seasons inherit their series'.
const vocabularyTypes = "Movie,Series"

// sweepOptions is the item query for a sweep over one library or all of
// them, the types defaulting to def.
func sweepOptions(ctx context.Context, client *embyfin.Client, library, types, def, fields string) (embyfin.SearchOptions, error) {
	if types == "" {
		types = def
	}
	opts := embyfin.SearchOptions{IncludeItemTypes: types, Fields: fields}
	folder, err := client.ResolveLibrary(ctx, library)
	if err != nil {
		return opts, err
	}
	if folder != nil {
		opts.ParentID = folder.ItemID
	}

	return opts, nil
}

// nonEmpty is a list of ids or names as given, less the blank ones and the
// spaces around the rest.
func nonEmpty(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}

	return out
}

// sweepPage is how many items one sweep request reads. A server pays for a
// page mostly in walking past the ones before it, and about the same for ten
// thousand rows as for one thousand, so a sweep of a few hundred thousand
// items goes in a few large pages.
const sweepPage = 10000

// sweepSort is the order a sweep reads in: when items were added, then name.
// The default, name alone, costs Emby several times as much deep into a large
// library - half an hour against a minute on one of a few hundred thousand -
// and the name settles most ties between items added at one moment. An item
// added mid-sweep can still sort anywhere (both servers date it by its file),
// which the read allows for (embyfin.ReadAll).
const sweepSort = "DateCreated,SortName"

// sweepAll reads every item matching opts, each once, a large page at a
// time, and says what the read saw: how many it read, and whether the
// library changed under it. It is for the audits that sweep a whole server
// rather than a library: smaller pages cost a large library dearly. It reads
// the way embyfin.ReadAll does, for purpose, finding its place again when the
// library changes under it.
func sweepAll(ctx context.Context, client *embyfin.Client, opts embyfin.SearchOptions, purpose embyfin.ReadPurpose, cb func(items []embyfin.Item)) (embyfin.ReadResult, error) {
	opts.SortBy, opts.SortOrder, opts.PageSize = sweepSort, "Ascending", sweepPage

	return client.ReadAll(ctx, opts, purpose, func(items []embyfin.Item) bool {
		cb(items)

		return true
	})
}

// changedNote is the note for an answer built from several full reads: what
// each read that saw the library change under it said, each once; "" when
// none saw a change.
func changedNote(reads ...embyfin.ReadResult) string {
	var notes []string
	for _, r := range reads {
		if n := r.Changed(); n != "" && !slices.Contains(notes, n) {
			notes = append(notes, n)
		}
	}

	return strings.Join(notes, "; ")
}

// userInLibrary resolves a user and checks they may see the library (nil is
// every library). Jellyfin lists a library's items for a user who may not see
// it when the library is named as the parent, so the check is made here for
// both servers.
func userInLibrary(ctx context.Context, client *embyfin.Client, nameOrID string, folder *embyfin.VirtualFolder) (*embyfin.User, error) {
	user, err := client.ResolveUser(ctx, nameOrID)
	if err != nil {
		return nil, err
	}
	if folder != nil && !user.CanSee(folder) {
		return nil, fmt.Errorf("%s cannot see the %s library", user.Name, folder.Name)
	}

	return user, nil
}

// watchFilters maps library_items' watched values onto the servers' item
// filters, which are read in a user's view.
var watchFilters = map[string]string{
	"watched":     "IsPlayed",
	"unwatched":   "IsUnplayed",
	"in_progress": "IsResumable",
	"favourite":   "IsFavorite",
}

// itemSorts maps library_items' sort names onto the servers' sort keys. Each
// ends in the keys that settle a tie: two items of one name (a remake, a
// second copy) otherwise come back in either order, and paged by offset one
// is listed twice and the other never. Neither server sorts by anything that
// tells every item apart, so the order is as settled as its keys allow.
var itemSorts = map[string]string{
	"name":      "SortName,DateCreated",
	"added":     "DateCreated,SortName",
	"premiered": "PremiereDate,SortName,DateCreated",
	"year":      "ProductionYear,SortName,DateCreated",
	"runtime":   "Runtime,SortName,DateCreated",
	"rating":    "CommunityRating,SortName,DateCreated",
	"played":    "DatePlayed,SortName,DateCreated",
	"random":    "Random",
}

// searchSortMax is the most matches a title search reads to count, page and
// sort them itself: a title search names a handful, and one that matches more
// than this is too broad to count, or to sort.
const searchSortMax = 2000

// searchMatches reads every item a title search matches, in the server's
// order of how well each matches, and sorts them by one of library_items'
// sorts when one is given, with the same keys settling a tie as the servers'
// (itemSorts). Neither server counts a search, nor pages one past three
// times its limit (Jellyfin), so library_items counts and pages the matches
// itself. more says a search without a sort has no count: it matched more
// than searchSortMax, and the first that many are answered, or its read
// stopped short, the library changing too much to follow, and the best
// matches it reached are answered. One with a sort is refused either way,
// having nothing whole to sort. The note is what the read saw of the library
// changing, or of the search being too broad.
func searchMatches(ctx context.Context, client *embyfin.Client, opts embyfin.SearchOptions, sort string, desc bool) (items []embyfin.Item, more bool, note string, err error) {
	all := opts
	all.SortBy, all.SortOrder, all.StartIndex, all.Limit = "", "", 0, 0
	all.Fields = embyfin.FieldsDefault + ",SortName,CommunityRating"
	read, err := client.ReadAll(ctx, all, embyfin.ToAnswer, func(page []embyfin.Item) bool {
		items = append(items, page...)
		return len(items) <= searchSortMax
	})
	if _, stopped := errors.AsType[*embyfin.CutError](err); stopped && sort == "" {
		// the best matches it reached are matches, and are answered; how
		// many there are, and what lies past them, is not known
		return items[:min(len(items), searchSortMax)], true, err.Error(), nil
	}
	if err != nil {
		return nil, false, "", err
	}
	note = read.Changed()
	if len(items) > searchSortMax {
		if sort != "" {
			return nil, false, "", fmt.Errorf("%q matches more than %d items, too many to sort (neither server sorts a search, so it is sorted here): narrow the search, or leave out the sort", opts.SearchTerm, searchSortMax)
		}
		note = joinWarnings(fmt.Sprintf("more than %d items match %q, too many to count: these are among the %d that match best, and no page past them can be given; narrow the search", searchSortMax, opts.SearchTerm, searchSortMax), note)

		return items[:searchSortMax], true, note, nil
	}
	switch sort {
	case "":
		return items, false, note, nil
	case "random":
		rand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] }) //nolint:gosec // an order to browse in, not a secret
		return items, false, note, nil
	}

	name := func(it *embyfin.Item) string { return strings.ToLower(cmp.Or(it.SortName, it.Name)) }
	played := func(it *embyfin.Item) string {
		if it.UserData == nil {
			return ""
		}
		return it.UserData.LastPlayedDate
	}
	key := func(a, b *embyfin.Item) int {
		switch sort {
		case "added":
			return strings.Compare(a.DateCreated, b.DateCreated)
		case "premiered":
			return strings.Compare(a.PremiereDate, b.PremiereDate)
		case "year":
			return cmp.Compare(a.ProductionYear, b.ProductionYear)
		case "runtime":
			return cmp.Compare(a.RunTimeTicks, b.RunTimeTicks)
		case "rating":
			return cmp.Compare(a.CommunityRating, b.CommunityRating)
		case "played":
			return strings.Compare(played(a), played(b))
		}

		return 0
	}
	slices.SortStableFunc(items, func(a, b embyfin.Item) int {
		c := cmp.Or(key(&a, &b), strings.Compare(name(&a), name(&b)), strings.Compare(a.DateCreated, b.DateCreated))
		if desc {
			return -c
		}
		return c
	})

	return items, false, note, nil
}

// sinceTime reads a time a caller gives as a date (2006-01-02) or a time
// with or without its zone, the way saved_since is read.
func sinceTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("%q is not a date (2026-01-02) or a time (2026-01-02T15:04:05Z)", s)
}

// addedSince reads the items added at or after cutoff, newest first, a page
// at a time until one older comes - which the order puts after every newer
// one - or searchSortMax have been read, when more says the rest were not.
func addedSince(ctx context.Context, client *embyfin.Client, opts embyfin.SearchOptions, cutoff time.Time) (items []embyfin.Item, more bool, note string, err error) {
	opts.SortBy, opts.SortOrder, opts.StartIndex, opts.Limit = itemSorts["added"], sortDescending, 0, episodePageMax
	for len(items) <= searchSortMax {
		page, total, err := client.Search(ctx, opts)
		if err != nil {
			return nil, false, "", err
		}
		for i := range page {
			after, err := afterCutoff(&page[i], cutoff)
			if err != nil {
				return nil, false, "", err
			}
			if !after {
				return items, false, "", nil
			}
			items = append(items, page[i])
		}
		opts.StartIndex += len(page)
		if len(page) == 0 || opts.StartIndex >= total {
			return items, false, "", nil
		}
	}

	return items[:searchSortMax], true, fmt.Sprintf("more than %d items were added since %s, too many to count: these are the newest %d, and no page past them can be given; ask from a later time", searchSortMax, cutoff.Format(time.RFC3339), searchSortMax), nil
}

// addedAfter keeps the items added at or after cutoff.
func addedAfter(items []embyfin.Item, cutoff time.Time) ([]embyfin.Item, error) {
	kept := make([]embyfin.Item, 0, len(items))
	for i := range items {
		after, err := afterCutoff(&items[i], cutoff)
		if err != nil {
			return nil, err
		}
		if after {
			kept = append(kept, items[i])
		}
	}

	return kept, nil
}

func registerLibraryBrowseTools(r *registry) {
	client := r.client

	type itemsIn struct {
		Query           string   `json:"query,omitempty"            jsonschema:"title or partial title to search for; with no sort the best matches come first"`
		Person          string   `json:"person,omitempty"           jsonschema:"only items featuring this actor, director or writer, by name"`
		Library         string   `json:"library,omitempty"          jsonschema:"library name or id; default every library"`
		Types           string   `json:"types,omitempty"            jsonschema:"comma-separated item types; defaults to the library's kind: Series in a TV library, Movie in a movie library, Movie,Series otherwise"`
		Genres          []string `json:"genres,omitempty"           jsonschema:"items with any of these genres"`
		Tags            []string `json:"tags,omitempty"             jsonschema:"items with any of these tags"`
		Studios         []string `json:"studios,omitempty"          jsonschema:"items from any of these studios or networks"`
		OfficialRatings []string `json:"official_ratings,omitempty" jsonschema:"items with any of these parental ratings, e.g. PG-13, TV-MA"`
		Years           []int    `json:"years,omitempty"            jsonschema:"items from any of these production years"`
		Watched         string   `json:"watched,omitempty"          jsonschema:"watched, unwatched, in_progress or favourite, in user's view"`
		User            string   `json:"user,omitempty"             jsonschema:"whose watch state watched and sort=played read, by name or id; defaults to the first administrator"`
		SavedSince      string   `json:"saved_since,omitempty"      jsonschema:"only items the server last SAVED at or after this time: a date such as 2026-01-02, or a time such as 2026-01-02T15:04:05Z: the closest either server offers to 'what changed'. A file written over an existing path is re-read and saved, but so is an item somebody edited, and neither server can sort by it"`
		AddedSince      string   `json:"added_since,omitempty"      jsonschema:"only items added to the library at or after this time: a date such as 2026-01-02, or a time such as 2026-01-02T15:04:05Z. Read newest first, so sort is added or left out; total counts them all when at most 2000 were added since, and is null past that"`
		Sort            string   `json:"sort,omitempty"             jsonschema:"name (default; relevance when there is a query), added, premiered, year, runtime, rating, played (needs a user) or random"`
		Desc            bool     `json:"desc,omitempty"             jsonschema:"sort descending"`
		Limit           int      `json:"limit,omitempty"            jsonschema:"page size, default 25, max 1000"`
		Offset          int      `json:"offset,omitempty"           jsonschema:"skip this many items, to page"`
	}
	type itemsOut struct {
		Total  *int          `json:"total"          jsonschema:"matches across every page, as the server stores them: on Emby each file of a film held in several is an item of its own, on Jellyfin one item with the rest in its versions. Read in a user's view (user, watched or sort played given), Emby shows a film held in several files once, counts it once, and lists that item's own file alone. A title search is counted here, neither server counting one; null when more than 2000 items match it, too many to count, when its read stopped short, the library changing too much to follow, or when more than 2000 items were added since added_since"`
		Offset int           `json:"offset"`
		Items  []itemSummary `json:"items"`
		Note   string        `json:"note,omitempty" jsonschema:"a title search reads every match, to count, page and sort them: set when more than 2000 match, and when the library was seen to change while it was read, so matches added or removed meanwhile may be missing, or listed though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "library_items",
		Description: "Find and browse library items: a title search, a structured filter, or both, sorted and paged: 'alien', 'every unwatched horror film, newest first', 'what is rated TV-MA', 'what came from A24', 'what has Sigourney Weaver in it', 'what was added since Monday' (added_since). Filters combine (an item must pass each one given); within one, any value matches. Returns trimmed summaries with metadata provider ids, runtime and stream quality facts of the file at each item's path, and every file in versions when an item is held in several. " +
			"On Emby a read in a user's view (user, watched, or sort played) shows people's view: a film held in several files is one item listing its own file alone, so leave those out to see every file.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in itemsIn) (*mcp.CallToolResult, itemsOut, error) {
		// capped as library_episodes is: every summary carries its files'
		// facts, so an uncapped limit was one call reading the whole library
		// with its media sources, in one answer no client can hold
		limit := in.Limit
		if limit <= 0 {
			limit = 25
		}
		limit = min(limit, episodePageMax)
		offset := max(in.Offset, 0)

		sort := strings.ToLower(strings.TrimSpace(in.Sort))
		var added time.Time
		if in.AddedSince != "" {
			cutoff, err := sinceTime(in.AddedSince)
			if err != nil {
				return nil, itemsOut{}, fmt.Errorf("added_since: %w", err)
			}
			if sort != "" && sort != "added" {
				return nil, itemsOut{}, fmt.Errorf("sort %q with added_since: the items added since a time are read newest first, so sort is added or left out", in.Sort)
			}
			added, sort, in.Desc = cutoff, "added", true
		}
		var sortBy string
		switch {
		case sort == "" && strings.TrimSpace(in.Query) != "":
			// the server's own relevance order for a search, which it only
			// gives when asked for no order at all
		case sort == "":
			sortBy = itemSorts["name"]
		default:
			var ok bool
			if sortBy, ok = itemSorts[sort]; !ok {
				return nil, itemsOut{}, fmt.Errorf("unknown sort %q; choose one of: name, added, premiered, year, runtime, rating, played, random", in.Sort)
			}
		}
		opts := embyfin.SearchOptions{
			SearchTerm:       strings.TrimSpace(in.Query),
			IncludeItemTypes: in.Types,
			Genres:           in.Genres,
			Tags:             in.Tags,
			Studios:          in.Studios,
			OfficialRatings:  in.OfficialRatings,
			SortBy:           sortBy,
			SavedSince:       in.SavedSince,
			Limit:            limit,
			StartIndex:       offset,
		}
		if sortBy != "" {
			opts.SortOrder = "Ascending"
			if in.Desc {
				opts.SortOrder = sortDescending
			}
		}
		years := make([]string, 0, len(in.Years))
		for _, y := range in.Years {
			years = append(years, strconv.Itoa(y))
		}
		opts.Years = strings.Join(years, ",")

		watched := strings.ToLower(strings.TrimSpace(in.Watched))
		if watched != "" {
			filter, ok := watchFilters[watched]
			if !ok {
				return nil, itemsOut{}, fmt.Errorf("unknown watched %q; choose one of: watched, unwatched, in_progress, favourite", in.Watched)
			}
			opts.Filters = filter
		}
		folder, err := client.ResolveLibrary(ctx, in.Library)
		if err != nil {
			return nil, itemsOut{}, err
		}
		if folder != nil {
			opts.ParentID = folder.ItemID
		}
		if watched != "" || sort == "played" || in.User != "" {
			user, uerr := userInLibrary(ctx, client, in.User, folder)
			if uerr != nil {
				return nil, itemsOut{}, uerr
			}
			opts.UserID, opts.EnableUserData = user.ID, true
		}
		if opts.IncludeItemTypes == "" {
			opts.IncludeItemTypes = defaultSearchTypes(folder)
		}
		if strings.TrimSpace(in.Person) != "" {
			found, _, perr := resolvePerson(ctx, client, in.Person)
			if perr != nil {
				return nil, itemsOut{}, perr
			}
			if found == nil {
				return nil, itemsOut{}, fmt.Errorf("no person named %q in the library (person_get lists the near names)", in.Person)
			}
			opts.PersonIDs = found.ID
		}

		// both servers answer a search in their own order of how well each
		// item matches, and ignore a sort asked for with it (seen on Emby
		// 4.10 and Jellyfin 12.1: "Dune", newest first, came back oldest
		// first); neither counts one, Emby answering 0 and Jellyfin at most
		// three times the limit, and Jellyfin lists nothing past that. So a
		// search's matches are read whole, and counted, paged and sorted here
		if opts.SearchTerm != "" || !added.IsZero() {
			var matches []embyfin.Item
			var more bool
			var note string
			if opts.SearchTerm != "" {
				matches, more, note, err = searchMatches(ctx, client, opts, sort, in.Desc)
			} else {
				matches, more, note, err = addedSince(ctx, client, opts, added)
			}
			if err != nil {
				return nil, itemsOut{}, err
			}
			if !added.IsZero() && opts.SearchTerm != "" {
				// a search's matches, read whole and sorted newest first: the
				// ones added since the time
				if matches, err = addedAfter(matches, added); err != nil {
					return nil, itemsOut{}, err
				}
			}
			page := matches[min(offset, len(matches)):min(offset+limit, len(matches))]
			out := itemsOut{Offset: offset, Items: summariseAll(page), Note: note}
			if !more {
				total := len(matches)
				out.Total = &total
			}

			return nil, out, nil
		}

		items, total, err := client.Search(ctx, opts)
		if err != nil {
			return nil, itemsOut{}, err
		}

		return nil, itemsOut{Total: &total, Offset: offset, Items: summariseAll(items)}, nil
	})
	type filtersIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
		Types   string `json:"types,omitempty"   jsonschema:"comma-separated item types to read; defaults to Movie,Series"`
	}
	type yearCount struct {
		Year  int `json:"year"`
		Items int `json:"items"`
	}
	type filtersOut struct {
		Scanned         int          `json:"items_scanned"`
		Genres          []valueCount `json:"genres"`
		Tags            []valueCount `json:"tags"`
		Studios         []valueCount `json:"studios"`
		OfficialRatings []valueCount `json:"official_ratings"`
		Years           []yearCount  `json:"years"            jsonschema:"oldest first"`
		Note            string       `json:"note,omitempty"   jsonschema:"set when the library was seen to change while it was read: items added or removed meanwhile may be missing from the counts, or counted though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_filters",
		Description: "The genres, tags, studios, parental ratings and years a library's items carry, each with how many items carry it, most used first: the values library_items filters on, and the vocabulary to tidy (a genre on one item, two spellings of a studio - audit_spelling finds those).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in filtersIn) (*mcp.CallToolResult, filtersOut, error) {
		opts, err := sweepOptions(ctx, client, in.Library, in.Types, vocabularyTypes, embyfin.FieldsVocabulary)
		if err != nil {
			return nil, filtersOut{}, err
		}

		counts := map[string]map[string]int{fieldGenres: {}, fieldTags: {}, fieldStudios: {}, "ratings": {}}
		years := map[int]int{}
		out := filtersOut{}
		result, err := client.ReadAll(ctx, opts, embyfin.ToAnswer, func(items []embyfin.Item) bool {
			for i := range items {
				it := &items[i]
				out.Scanned++
				for _, f := range vocabFields {
					for _, v := range valuesOf(f, it) {
						counts[f][v]++
					}
				}
				if it.OfficialRating != "" {
					counts["ratings"][it.OfficialRating]++
				}
				if it.ProductionYear > 0 {
					years[it.ProductionYear]++
				}
			}
			return true
		})
		if err != nil {
			return nil, filtersOut{}, err
		}
		out.Note = result.Changed()

		out.Genres, out.Tags, out.Studios = sortedCounts(counts[fieldGenres]), sortedCounts(counts[fieldTags]), sortedCounts(counts[fieldStudios])
		out.OfficialRatings = sortedCounts(counts["ratings"])
		for y, n := range years {
			out.Years = append(out.Years, yearCount{Year: y, Items: n})
		}
		slices.SortFunc(out.Years, func(a, b yearCount) int { return cmp.Compare(a.Year, b.Year) })

		return nil, out, nil
	})
}

// errNoItems is an edit asked to change nothing.
var errNoItems = errors.New("at least one item id is required")
