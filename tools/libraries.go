package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type librarySummary struct {
	ID             string   `json:"id,omitempty"`
	Name           string   `json:"name"`
	CollectionType string   `json:"collection_type,omitempty" jsonschema:"movies, tvshows, music, etc; empty means mixed content"`
	Locations      []string `json:"locations,omitempty"`
	SavesNfo       bool     `json:"saves_nfo"                 jsonschema:"an edit to an item is written back to an nfo beside its media file"`
}

// summariseLibrary is the listing view of a library.
func summariseLibrary(f *embyfin.VirtualFolder) librarySummary {
	return librarySummary{ID: f.ItemID, Name: f.Name, CollectionType: f.CollectionType, Locations: f.Locations, SavesNfo: f.SavesNfo}
}

// saveNfoSchema describes save_nfo for library_create and library_edit.
const saveNfoSchema = "write each edit to an item back to an nfo beside its media file (the library's Nfo metadata saver): Emby names it after the media file, Jellyfin movie.nfo or tvshow.nfo"

// resolveLibrary finds a library by name (case-insensitive) or id; empty input
// returns nil meaning "all libraries".
// containerTypes are the item types that hold other items rather than being
// media: left out of a library's item count.
const containerTypes = "Folder,CollectionFolder,UserRootFolder,AggregateFolder,BoxSet,Playlist"

// searchTypesAll is the search default when no single library kind applies.
const searchTypesAll = "Movie,Series"

// countedTypes are the item types library_get reports counts for: a music
// library is counted by its artists, albums and songs, everything else by its
// films, series and episodes.
func countedTypes(folder *embyfin.VirtualFolder) []string {
	if folder != nil && folder.CollectionType == "music" {
		return []string{"MusicArtist", "MusicAlbum", "Audio"}
	}

	return []string{typeMovie, "Series", "Episode"}
}

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
		return typeMovie
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

func registerLibraryTools(r *registry) {
	client := r.client
	type libraryListOut struct {
		Libraries []librarySummary `json:"libraries"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_list",
		Description: "List all libraries on the media server with their type and filesystem locations.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, libraryListOut, error) {
		folders, err := client.VirtualFolders(ctx)
		if err != nil {
			return nil, libraryListOut{}, err
		}

		out := libraryListOut{}
		for i := range folders {
			out.Libraries = append(out.Libraries, summariseLibrary(&folders[i]))
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
		SavesNfo       bool           `json:"saves_nfo"                 jsonschema:"an edit to an item is written back to an nfo beside its media file"`
		ItemCount      int            `json:"item_count"                jsonschema:"total items in the library, recursive, not counting folders and collections"`
		TypeCounts     map[string]int `json:"type_counts,omitempty"     jsonschema:"item counts by primary type: Movie, Series and Episode, or MusicArtist, MusicAlbum and Audio in a music library"`
	}
	add(r, readTool, &mcp.Tool{
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
			SavesNfo:       folder.SavesNfo,
			TypeCounts:     map[string]int{},
		}

		// the recursive listing includes the library's own folders and, on
		// Jellyfin, the collections its items belong to; neither is an item
		_, total, err := client.Search(ctx, embyfin.SearchOptions{ParentID: folder.ItemID, Limit: 1, ExcludeItemTypes: containerTypes})
		if err != nil {
			return nil, libraryGetOut{}, err
		}
		out.ItemCount = total

		for _, t := range countedTypes(folder) {
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

	type recentIn struct {
		Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
		Types   string `json:"types,omitempty"   jsonschema:"comma-separated item types; defaults to Movie,Series,Episode"`
		Days    int    `json:"days,omitempty"    jsonschema:"how many days back, default 60"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum results, default 25"`
	}
	type recentOut struct {
		Items []itemSummary `json:"items" jsonschema:"newest additions first"`
	}
	add(r, readTool, &mcp.Tool{
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
		Types   string `json:"types,omitempty"   jsonschema:"comma-separated item types; default Movie,Series"`
	}
	type genresOut struct {
		Genres []string `json:"genres" jsonschema:"by name"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_genres",
		Description: "The genres the library's films and series carry, by name. library_filters counts them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in genresIn) (*mcp.CallToolResult, genresOut, error) {
		// read off the items rather than the servers' genre lists: Jellyfin's
		// does not show a genre until a scan has run since it was first used,
		// and both keep one no item carries any more
		opts, err := sweepOptions(ctx, client, in.Library, in.Types, vocabularyTypes, "Genres")
		if err != nil {
			return nil, genresOut{}, err
		}
		seen := map[string]bool{}
		out := genresOut{Genres: []string{}}
		if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
			for i := range items {
				for _, g := range items[i].Genres {
					if !seen[g] {
						seen[g] = true
						out.Genres = append(out.Genres, g)
					}
				}
			}
			return true
		}); err != nil {
			return nil, genresOut{}, err
		}
		slices.Sort(out.Genres)

		return nil, out, nil
	})

	type scanIn struct {
		Library string `json:"library,omitempty" jsonschema:"scan only this library, by name or id; default every library"`
	}
	type scanOut struct {
		Started bool   `json:"started"`
		Library string `json:"library,omitempty" jsonschema:"the library scanned, when one was named"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "library_scan",
		Description: "Scan the libraries' folders so new, changed and removed files are picked up: every library, or one. Every library runs as the server's scan task (task_list shows when it has finished); one library is a refresh of its folder, which is not a task and fills in metadata only where it is missing. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in scanIn) (*mcp.CallToolResult, scanOut, error) {
		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, scanOut{}, err
		}
		if folder == nil {
			if err := client.RefreshLibrary(ctx); err != nil {
				return nil, scanOut{}, err
			}
			return nil, scanOut{Started: true}, nil
		}
		if err := client.ScanLibrary(ctx, folder); err != nil {
			return nil, scanOut{}, err
		}

		return nil, scanOut{Started: true, Library: folder.Name}, nil
	})

	registerLibraryBrowseTools(r)

	type createIn struct {
		Name      string   `json:"name"                jsonschema:"the library's display name"`
		Type      string   `json:"type"                jsonschema:"movies, tvshows, music, musicvideos, homevideos, books or mixed"`
		Paths     []string `json:"paths"               jsonschema:"folders on the server's own filesystem"`
		Providers bool     `json:"providers,omitempty" jsonschema:"leave the server's internet metadata and image fetchers on; off, the library is built from the files and their nfo sidecars only"`
		Scan      bool     `json:"scan,omitempty"      jsonschema:"scan the new library straight away"`
		SaveNfo   *bool    `json:"save_nfo,omitempty"  jsonschema:"true or false; left out, Emby saves no nfo and Jellyfin saves them"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "library_create",
		Description: "Create a library over folders on the server. Emby assigns its id at once, Jellyfin on the first scan (pass scan=true, or library_scan). save_nfo: " + saveNfoSchema + ". Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, librarySummary, error) {
		if in.Name == "" || len(in.Paths) == 0 {
			return nil, librarySummary{}, errors.New("name and at least one path are required")
		}
		if err := client.CreateLibrary(ctx, embyfin.LibrarySpec{
			Name: in.Name, CollectionType: in.Type, Paths: in.Paths, Providers: in.Providers, Refresh: in.Scan, SaveNfo: in.SaveNfo,
		}); err != nil {
			return nil, librarySummary{}, err
		}

		folder, err := resolveLibrary(ctx, client, in.Name)
		if err != nil {
			return nil, librarySummary{}, err
		}

		return nil, summariseLibrary(folder), nil
	})

	type editIn struct {
		Library     string   `json:"library"                jsonschema:"library name (case-insensitive) or library id"`
		Name        string   `json:"name,omitempty"         jsonschema:"rename the library"`
		AddPaths    []string `json:"add_paths,omitempty"    jsonschema:"folders on the server's own filesystem to add"`
		RemovePaths []string `json:"remove_paths,omitempty" jsonschema:"folders to take out of the library (the files stay on disk)"`
		SaveNfo     *bool    `json:"save_nfo,omitempty"     jsonschema:"true to start writing nfos, false to stop"`
	}
	type editOut struct {
		librarySummary
		Changed []string `json:"changed" jsonschema:"what was done, in order"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "library_edit",
		Description: "Rename a library, add and remove the folders it is built from, or switch save_nfo: " + saveNfoSchema + ". On Emby nothing is scanned: run library_scan on the library afterwards to pick up what a new folder holds or drop what a removed one held. On Jellyfin a folder change starts a scan of every library, because a scan of one library does not see a changed folder. Jellyfin gives a renamed library a new id on its next scan. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		if in.Library == "" {
			return nil, editOut{}, errors.New("library is required")
		}
		if in.Name == "" && len(in.AddPaths) == 0 && len(in.RemovePaths) == 0 && in.SaveNfo == nil {
			return nil, editOut{}, errors.New("nothing to change: pass name, add_paths, remove_paths or save_nfo")
		}
		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, editOut{}, err
		}

		out := editOut{Changed: []string{}}
		// before a rename, which gives a Jellyfin library a new id on its next scan
		if in.SaveNfo != nil {
			if err := client.SetLibraryNfo(ctx, folder, *in.SaveNfo); err != nil {
				return nil, editOut{}, fmt.Errorf("switching nfo saving: %w", err)
			}
			if *in.SaveNfo {
				out.Changed = append(out.Changed, "nfo saving on")
			} else {
				out.Changed = append(out.Changed, "nfo saving off")
			}
		}
		for _, p := range in.AddPaths {
			if slices.Contains(folder.Locations, p) {
				return nil, editOut{}, fmt.Errorf("%s already holds %s", folder.Name, p)
			}
			if err := client.AddLibraryPath(ctx, folder, p); err != nil {
				return nil, editOut{}, fmt.Errorf("adding %s: %w", p, err)
			}
			out.Changed = append(out.Changed, "added "+p)
		}
		for _, p := range in.RemovePaths {
			if !slices.Contains(folder.Locations, p) {
				return nil, editOut{}, fmt.Errorf("%s has no folder %s (have: %s)", folder.Name, p, strings.Join(folder.Locations, ", "))
			}
			if err := client.RemoveLibraryPath(ctx, folder, p); err != nil {
				return nil, editOut{}, fmt.Errorf("removing %s: %w", p, err)
			}
			out.Changed = append(out.Changed, "removed "+p)
		}
		name := folder.Name
		if in.Name != "" && in.Name != folder.Name {
			if err := client.RenameLibrary(ctx, folder, in.Name); err != nil {
				return nil, editOut{}, err
			}
			out.Changed = append(out.Changed, "renamed "+folder.Name+" to "+in.Name)
			name = in.Name
		}

		updated, err := resolveLibrary(ctx, client, name)
		if err != nil {
			return nil, editOut{}, err
		}
		out.librarySummary = summariseLibrary(updated)

		return nil, out, nil
	})

	type deleteLibraryIn struct {
		Library string `json:"library" jsonschema:"library name (case-insensitive) or library id"`
		Confirm bool   `json:"confirm" jsonschema:"must be true; the library and the server's metadata for it are removed (the media files stay on disk)"`
	}
	type deleteLibraryOut struct {
		Deleted string `json:"deleted"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "library_delete",
		Description: "Remove a library from the server. The media files stay on disk, but the server's metadata, images and watch state for its items are gone, and they leave the playlists and collections that held them (Jellyfin drops them with the library scan this starts). Requires confirm=true.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteLibraryIn) (*mcp.CallToolResult, deleteLibraryOut, error) {
		if !in.Confirm {
			return nil, deleteLibraryOut{}, errors.New("refusing to delete without confirm=true")
		}
		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, deleteLibraryOut{}, err
		}
		if folder == nil {
			return nil, deleteLibraryOut{}, errors.New("library is required")
		}
		if err := client.DeleteLibrary(ctx, folder); err != nil {
			return nil, deleteLibraryOut{}, err
		}

		return nil, deleteLibraryOut{Deleted: folder.Name}, nil
	})
}
