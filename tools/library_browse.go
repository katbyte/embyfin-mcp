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
	folder, err := resolveLibrary(ctx, client, library)
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
// and in this order an item added mid-sweep lands at the end rather than
// shifting a page not yet read.
const sweepSort = "DateCreated,SortName"

// sweepAll reads every item matching opts, a page at a time, and says how
// many it read. It is for the audits that sweep a whole server rather than a
// library: SearchAll's smaller pages and default order cost a large library
// dearly.
func sweepAll(ctx context.Context, client *embyfin.Client, opts embyfin.SearchOptions, cb func(items []embyfin.Item)) (int, error) {
	opts.SortBy, opts.SortOrder, opts.Limit = sweepSort, "Ascending", sweepPage
	scanned := 0
	for start := 0; ; start += sweepPage {
		opts.StartIndex = start
		items, total, err := client.Search(ctx, opts)
		if err != nil {
			return scanned, err
		}
		scanned += len(items)
		cb(items)
		if len(items) < sweepPage || start+len(items) >= total {
			return scanned, nil
		}
	}
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

// searchSortMax is the most matches a search with a sort reads to sort them
// itself: a title search names a handful, and one that matches more than
// this is too broad to be worth sorting.
const searchSortMax = 2000

// sortedMatches reads every item a search matches and sorts them by one of
// library_items' sorts, with the same keys settling a tie as the servers'
// (itemSorts).
func sortedMatches(ctx context.Context, client *embyfin.Client, opts embyfin.SearchOptions, sort string, desc bool) ([]embyfin.Item, error) {
	all := opts
	all.SortBy, all.SortOrder = "", ""
	all.Fields = embyfin.FieldsDefault + ",SortName,CommunityRating"
	var items []embyfin.Item
	if err := client.SearchAll(ctx, all, func(page []embyfin.Item) bool {
		items = append(items, page...)
		return len(items) <= searchSortMax
	}); err != nil {
		return nil, err
	}
	if len(items) > searchSortMax {
		return nil, fmt.Errorf("%q matches more than %d items, too many to sort (neither server sorts a search, so it is sorted here): narrow the search, or leave out the sort", opts.SearchTerm, searchSortMax)
	}
	if sort == "random" {
		rand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] }) //nolint:gosec // an order to browse in, not a secret
		return items, nil
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

	return items, nil
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
		SavedSince      string   `json:"saved_since,omitempty"      jsonschema:"only items the server last SAVED at or after this time (RFC3339): the closest either server offers to 'what changed'. A file written over an existing path is re-read and saved, but so is an item somebody edited, and neither server can sort by it"`
		Sort            string   `json:"sort,omitempty"             jsonschema:"name (default; relevance when there is a query), added, premiered, year, runtime, rating, played (needs a user) or random"`
		Desc            bool     `json:"desc,omitempty"             jsonschema:"sort descending"`
		Limit           int      `json:"limit,omitempty"            jsonschema:"page size, default 25, max 1000"`
		Offset          int      `json:"offset,omitempty"           jsonschema:"skip this many items, to page"`
	}
	type itemsOut struct {
		Total  int           `json:"total"  jsonschema:"matches across every page"`
		Offset int           `json:"offset"`
		Items  []itemSummary `json:"items"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_items",
		Description: "Find and browse library items: a title search, a structured filter, or both, sorted and paged: 'alien', 'every unwatched horror film, newest first', 'what is rated TV-MA', 'what came from A24', 'what has Sigourney Weaver in it'. Filters combine (an item must pass each one given); within one, any value matches. Returns trimmed summaries with metadata provider ids, runtime and stream quality facts.",
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
		folder, err := resolveLibrary(ctx, client, in.Library)
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
		// first), so the matches are read whole and sorted here
		if opts.SearchTerm != "" && sortBy != "" {
			matches, merr := sortedMatches(ctx, client, opts, sort, in.Desc)
			if merr != nil {
				return nil, itemsOut{}, merr
			}
			page := matches[min(offset, len(matches)):min(offset+limit, len(matches))]

			return nil, itemsOut{Total: len(matches), Offset: offset, Items: summariseAll(page)}, nil
		}

		items, total, err := client.Search(ctx, opts)
		if err != nil {
			return nil, itemsOut{}, err
		}

		return nil, itemsOut{Total: total, Offset: offset, Items: summariseAll(items)}, nil
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
		if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
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
		}); err != nil {
			return nil, filtersOut{}, err
		}

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
