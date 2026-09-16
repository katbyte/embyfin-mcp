package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
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

// itemSorts maps library_items' sort names onto the servers' sort keys.
var itemSorts = map[string]string{
	"name":      "SortName",
	"added":     "DateCreated,SortName",
	"premiered": "PremiereDate,SortName",
	"year":      "ProductionYear,SortName",
	"runtime":   "Runtime,SortName",
	"rating":    "CommunityRating,SortName",
	"played":    "DatePlayed,SortName",
	"random":    "Random",
}

func registerLibraryBrowseTools(r *registry) {
	client := r.client

	type itemsIn struct {
		Library         string   `json:"library,omitempty"          jsonschema:"library name or id; default every library"`
		Types           string   `json:"types,omitempty"            jsonschema:"comma-separated item types; defaults to the library's kind: Series in a TV library, Movie in a movie library, Movie,Series otherwise"`
		Genres          []string `json:"genres,omitempty"           jsonschema:"items with any of these genres"`
		Tags            []string `json:"tags,omitempty"             jsonschema:"items with any of these tags"`
		Studios         []string `json:"studios,omitempty"          jsonschema:"items from any of these studios or networks"`
		OfficialRatings []string `json:"official_ratings,omitempty" jsonschema:"items with any of these parental ratings, e.g. PG-13, TV-MA"`
		Years           []int    `json:"years,omitempty"            jsonschema:"items from any of these production years"`
		Watched         string   `json:"watched,omitempty"          jsonschema:"watched, unwatched, in_progress or favourite, in user's view"`
		User            string   `json:"user,omitempty"             jsonschema:"whose watch state watched and sort=played read, by name or id; defaults to the first administrator"`
		Sort            string   `json:"sort,omitempty"             jsonschema:"name (default), added, premiered, year, runtime, rating, played (needs a user) or random"`
		Desc            bool     `json:"desc,omitempty"             jsonschema:"sort descending"`
		Limit           int      `json:"limit,omitempty"            jsonschema:"page size, default 25"`
		Offset          int      `json:"offset,omitempty"           jsonschema:"skip this many items, to page"`
	}
	type itemsOut struct {
		Total  int           `json:"total"  jsonschema:"matches across every page"`
		Offset int           `json:"offset"`
		Items  []itemSummary `json:"items"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_items",
		Description: "Browse a library by structured filter, sorted and paged: 'every unwatched horror film, newest first', 'what is rated TV-MA', 'what came from A24'. Filters combine (an item must pass each one given); within one, any value matches. Takes no text query: library_search finds things by title.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in itemsIn) (*mcp.CallToolResult, itemsOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 25
		}
		offset := max(in.Offset, 0)

		sort := strings.ToLower(strings.TrimSpace(in.Sort))
		if sort == "" {
			sort = "name"
		}
		sortBy, ok := itemSorts[sort]
		if !ok {
			return nil, itemsOut{}, fmt.Errorf("unknown sort %q; choose one of: name, added, premiered, year, runtime, rating, played, random", in.Sort)
		}
		opts := embyfin.SearchOptions{
			IncludeItemTypes: in.Types,
			Genres:           in.Genres,
			Tags:             in.Tags,
			Studios:          in.Studios,
			OfficialRatings:  in.OfficialRatings,
			SortBy:           sortBy,
			SortOrder:        "Ascending",
			Limit:            limit,
			StartIndex:       offset,
		}
		if in.Desc {
			opts.SortOrder = sortDescending
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
