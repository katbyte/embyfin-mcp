package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type librarySummary struct {
	ID             string   `json:"id,omitempty"`
	Name           string   `json:"name"`
	CollectionType string   `json:"collection_type,omitempty" jsonschema:"movies, tvshows, music, etc; empty means mixed content"`
	Locations      []string `json:"locations,omitempty"`
}

// resolveLibrary finds a library by name (case-insensitive) or id; empty input
// returns nil meaning "all libraries".
// searchTypesAll is the search default when no single library kind applies.
const searchTypesAll = "Movie,Series"

// defaultSearchTypes picks the item types a search should return when the caller
// did not say: the library's own kind, or movies and series across everything.
func defaultSearchTypes(folder *embyfin.VirtualFolder) string {
	if folder == nil {
		return searchTypesAll
	}

	switch folder.CollectionType {
	case "tvshows":
		return "Series"
	case "movies":
		return "Movie"
	case "music":
		return "MusicAlbum"
	default:
		return searchTypesAll
	}
}

func resolveLibrary(ctx context.Context, client *embyfin.Client, nameOrID string) (*embyfin.VirtualFolder, error) {
	if nameOrID == "" {
		return nil, nil //nolint:nilnil // nil folder means all libraries by design
	}

	folders, err := client.VirtualFolders(ctx)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(folders))
	for i := range folders {
		if strings.EqualFold(folders[i].Name, nameOrID) || folders[i].ItemID == nameOrID {
			return &folders[i], nil
		}
		names = append(names, folders[i].Name)
	}

	return nil, fmt.Errorf("no library named %q (have: %s)", nameOrID, strings.Join(names, ", "))
}

func registerLibraryTools(server *mcp.Server, client *embyfin.Client) {
	type libraryListOut struct {
		Libraries []librarySummary `json:"libraries"`
	}
	addTool(server, &mcp.Tool{
		Name:        "library_list",
		Description: "List all libraries on the media server with their type and filesystem locations.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, libraryListOut, error) {
		folders, err := client.VirtualFolders(ctx)
		if err != nil {
			return nil, libraryListOut{}, err
		}

		out := libraryListOut{}
		for _, f := range folders {
			out.Libraries = append(out.Libraries, librarySummary{
				ID:             f.ItemID,
				Name:           f.Name,
				CollectionType: f.CollectionType,
				Locations:      f.Locations,
			})
		}

		return nil, out, nil
	})

	type libraryGetIn struct {
		Library string `json:"library" jsonschema:"library name (case-insensitive) or library id"`
	}
	type libraryGetOut struct {
		ID             string         `json:"id,omitempty"`
		Name           string         `json:"name"`
		CollectionType string         `json:"collection_type,omitempty"`
		Locations      []string       `json:"locations,omitempty"`
		ItemCount      int            `json:"item_count"                jsonschema:"total items in the library, recursive"`
		TypeCounts     map[string]int `json:"type_counts,omitempty"     jsonschema:"item counts by primary type, e.g. Movie, Series, Episode"`
	}
	addTool(server, &mcp.Tool{
		Name:        "library_get",
		Description: "Get information about one library: type, filesystem locations, and item counts.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in libraryGetIn) (*mcp.CallToolResult, libraryGetOut, error) {
		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, libraryGetOut{}, err
		}
		if folder == nil {
			return nil, libraryGetOut{}, errors.New("library name is required")
		}

		out := libraryGetOut{
			ID:             folder.ItemID,
			Name:           folder.Name,
			CollectionType: folder.CollectionType,
			Locations:      folder.Locations,
			TypeCounts:     map[string]int{},
		}

		_, total, err := client.Search(ctx, embyfin.SearchOptions{ParentID: folder.ItemID, Limit: 1})
		if err != nil {
			return nil, libraryGetOut{}, err
		}
		out.ItemCount = total

		for _, t := range []string{"Movie", "Series", "Episode"} {
			_, n, err := client.Search(ctx, embyfin.SearchOptions{ParentID: folder.ItemID, IncludeItemTypes: t, Limit: 1})
			if err != nil {
				return nil, libraryGetOut{}, err
			}
			if n > 0 {
				out.TypeCounts[t] = n
			}
		}

		return nil, out, nil
	})

	type searchIn struct {
		Query   string `json:"query,omitempty"   jsonschema:"title or partial title to search for"`
		Types   string `json:"types,omitempty"   jsonschema:"comma-separated item types, e.g. Movie or Series,Episode; defaults to the library's kind: Series in a TV library, Movie in a movie library, Movie,Series otherwise"`
		Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
		Genre   string `json:"genre,omitempty"   jsonschema:"restrict by genre name"`
		Year    string `json:"year,omitempty"    jsonschema:"restrict by production year(s), comma-separated"`
		Person  string `json:"person,omitempty"  jsonschema:"restrict to items featuring this actor/director (name)"`
		SortBy  string `json:"sort_by,omitempty" jsonschema:"e.g. SortName, DateCreated, ProductionYear, Random; default relevance"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum results to return, default 10"`
	}
	type searchOut struct {
		TotalMatches int           `json:"total_matches"`
		Items        []itemSummary `json:"items"`
	}
	addTool(server, &mcp.Tool{
		Name:        "library_search",
		Description: "Search the media library by title with optional filters: library, genre, year, person. Returns trimmed summaries with metadata provider ids, runtime, and stream quality facts.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 10
		}

		opts := embyfin.SearchOptions{
			SearchTerm:       in.Query,
			IncludeItemTypes: in.Types,
			GenreNames:       in.Genre,
			Years:            in.Year,
			SortBy:           in.SortBy,
			Limit:            limit,
		}
		if in.SortBy != "" {
			opts.SortOrder = "Ascending"
			if strings.EqualFold(in.SortBy, "DateCreated") {
				opts.SortOrder = sortDescending
			}
		}

		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, searchOut{}, err
		}
		if folder != nil {
			opts.ParentID = folder.ItemID
		}
		if opts.IncludeItemTypes == "" {
			opts.IncludeItemTypes = defaultSearchTypes(folder)
		}

		if in.Person != "" {
			people, perr := client.Persons(ctx, in.Person, 1)
			if perr != nil {
				return nil, searchOut{}, perr
			}
			if len(people) == 0 {
				return nil, searchOut{}, fmt.Errorf("no person matching %q found in the library", in.Person)
			}
			opts.PersonIDs = people[0].ID
		}

		items, total, err := client.Search(ctx, opts)
		if err != nil {
			return nil, searchOut{}, err
		}

		return nil, searchOut{TotalMatches: total, Items: summariseAll(items)}, nil
	})

	type recentIn struct {
		Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
		Types   string `json:"types,omitempty"   jsonschema:"comma-separated item types; defaults to Movie,Series,Episode"`
		Days    int    `json:"days,omitempty"    jsonschema:"how many days back, default 60"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum results, default 25"`
	}
	type recentOut struct {
		Items []itemSummary `json:"items" jsonschema:"newest additions first"`
	}
	addTool(server, &mcp.Tool{
		Name:        "library_recent",
		Description: "Recently added items, newest first, default last 60 days.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in recentIn) (*mcp.CallToolResult, recentOut, error) {
		types := in.Types
		if types == "" {
			types = "Movie,Series,Episode"
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 25
		}

		opts := embyfin.SearchOptions{
			IncludeItemTypes: types,
			SortBy:           "DateCreated",
			SortOrder:        sortDescending,
			Limit:            limit,
		}

		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, recentOut{}, err
		}
		if folder != nil {
			opts.ParentID = folder.ItemID
		}

		items, _, err := client.Search(ctx, opts)
		if err != nil {
			return nil, recentOut{}, err
		}

		cutoff := daysCutoff(in.Days)
		out := recentOut{}
		for i := range items {
			if afterCutoff(items[i].DateCreated, cutoff) {
				out.Items = append(out.Items, summarise(&items[i]))
			}
		}

		return nil, out, nil
	})

	type genresIn struct {
		Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
		Types   string `json:"types,omitempty"   jsonschema:"comma-separated item types, e.g. Movie"`
	}
	type genresOut struct {
		Genres []string `json:"genres"`
	}
	addTool(server, &mcp.Tool{
		Name:        "library_genres",
		Description: "Genres present in the library.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in genresIn) (*mcp.CallToolResult, genresOut, error) {
		parentID := ""
		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, genresOut{}, err
		}
		if folder != nil {
			parentID = folder.ItemID
		}

		genres, err := client.Genres(ctx, parentID, in.Types)
		if err != nil {
			return nil, genresOut{}, err
		}

		return nil, genresOut{Genres: genres}, nil
	})

	type peopleIn struct {
		Name  string `json:"name"            jsonschema:"person name or partial name to search for"`
		Limit int    `json:"limit,omitempty" jsonschema:"maximum people to return, default 10"`
	}
	type personRow struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	}
	type peopleOut struct {
		People []personRow `json:"people" jsonschema:"use library_search with person to list their items"`
	}
	addTool(server, &mcp.Tool{
		Name:        "library_people",
		Description: "Search actors, directors, and other people known to the library.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in peopleIn) (*mcp.CallToolResult, peopleOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 10
		}

		people, err := client.Persons(ctx, in.Name, limit)
		if err != nil {
			return nil, peopleOut{}, err
		}

		out := peopleOut{}
		for _, p := range people {
			out.People = append(out.People, personRow{Name: p.Name, ID: p.ID})
		}

		return nil, out, nil
	})

	type scanOut struct {
		Started bool `json:"started"`
	}
	addTool(server, &mcp.Tool{
		Name:        "library_scan",
		Description: "Trigger a scan of all libraries so new files are picked up. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, scanOut, error) {
		if err := client.RefreshLibrary(ctx); err != nil {
			return nil, scanOut{}, err
		}

		return nil, scanOut{Started: true}, nil
	})
}
