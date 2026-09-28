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
const saveNfoSchema = "write each edit of an item's metadata to an nfo beside its media file, over any nfo of that name already there (the library's Nfo metadata saver): Emby names it after the media file, Jellyfin movie.nfo or tvshow.nfo. Off, an edit lives only in the server's database, and on Emby a scan that finds a file written over reads the nfo beside it over the edit"

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

// resolveLibrary finds the library a tool reads or filters by, by id or name
// (see findLibrary); empty input returns nil meaning "all libraries".
//
// It refuses a library the server lists without an id - which Jellyfin 12.1
// and Emby never do, giving a library its id when it is made, but an older
// Jellyfin did until a library's first scan - because every caller narrows
// to a library by its id, and an
// empty id narrows to nothing: the tool would quietly answer for, or change,
// every library on the server instead of the one named. The tools that act on
// the library itself (library_get, library_scan, library_edit,
// library_delete) use findLibrary, which takes it as it is.
func resolveLibrary(ctx context.Context, client *embyfin.Client, nameOrID string) (*embyfin.VirtualFolder, error) {
	folder, err := findLibrary(ctx, client, nameOrID)
	if err != nil || folder == nil {
		return folder, err
	}
	if folder.ItemID == "" {
		return nil, fmt.Errorf("the server lists the %s library without an id, so there is nothing to narrow to; run library_scan, which gives it one", folder.Name)
	}

	return folder, nil
}

// findLibrary finds a library by id or name, id or not; empty input returns
// nil meaning "all libraries". An exact name wins, then a name that differs
// only in case, but only when one library has it: Jellyfin on Linux can hold
// both "Movies" and "movies", and taking whichever is listed first would
// point a delete at the wrong one.
func findLibrary(ctx context.Context, client *embyfin.Client, nameOrID string) (*embyfin.VirtualFolder, error) {
	if nameOrID == "" {
		return nil, nil //nolint:nilnil // nil folder means all libraries by design
	}

	folders, err := client.VirtualFolders(ctx)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(folders))
	var folded []*embyfin.VirtualFolder
	for i := range folders {
		if folders[i].ItemID == nameOrID || folders[i].Name == nameOrID {
			return &folders[i], nil
		}
		if strings.EqualFold(folders[i].Name, nameOrID) {
			folded = append(folded, &folders[i])
		}
		names = append(names, folders[i].Name)
	}
	switch len(folded) {
	case 0:
		return nil, fmt.Errorf("no library named %q (have: %s)", nameOrID, strings.Join(names, ", "))
	case 1:
		return folded[0], nil
	}
	candidates := make([]string, 0, len(folded))
	for _, f := range folded {
		id := "no id yet"
		if f.ItemID != "" {
			id = "id " + f.ItemID
		}
		candidates = append(candidates, fmt.Sprintf("%q (%s)", f.Name, id))
	}

	return nil, fmt.Errorf("%d libraries are named %q apart from case: %s; pass the exact name or an id", len(folded), nameOrID, strings.Join(candidates, ", "))
}

// accessLost is the names of the accounts among given, by id, that do not see
// a library now: the ones a Jellyfin rename, which gives the library a new
// id, took it from.
func accessLost(ctx context.Context, client *embyfin.Client, given []string, folder *embyfin.VirtualFolder) ([]string, error) {
	users, err := client.Users(ctx)
	if err != nil {
		return nil, err
	}
	var lost []string
	for i := range users {
		if slices.Contains(given, users[i].ID) && !users[i].CanSee(folder) {
			lost = append(lost, users[i].Name)
		}
	}

	return lost, nil
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
		ItemCount      int            `json:"item_count"                jsonschema:"every item the server keeps under the library but its folders, collections and playlists: in a show library its series, seasons and episodes together, and Emby keeps more besides (type_counts has the kinds one by one)"`
		TypeCounts     map[string]int `json:"type_counts,omitempty"     jsonschema:"item counts by primary type: Movie, Series and Episode, or MusicArtist, MusicAlbum and Audio in a music library. As the server stores them: Emby counts each file of a film or episode held in several as one of its own, Jellyfin one with the rest as versions"`
		Note           string         `json:"note,omitempty"            jsonschema:"set when the server lists the library without an id, so it has no items to count until a scan gives it one"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_get",
		Description: "Get information about one library: type, filesystem locations, and item counts as the server stores them (Emby counts every file of a film held in several, Jellyfin the film once).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in libraryGetIn) (*mcp.CallToolResult, libraryGetOut, error) {
		folder, err := findLibrary(ctx, client, in.Library)
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
		// counted by its id, and a count under no id is the whole server's
		if folder.ItemID == "" {
			out.Note = "listed without an id: nothing to count until library_scan gives it one"
			return nil, out, nil
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
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum results, default 25, max 1000"`
	}
	type recentOut struct {
		Items []itemSummary `json:"items"          jsonschema:"newest additions first"`
		More  bool          `json:"more"           jsonschema:"true when more items were added inside the period than the limit let through: this is only the newest of them"`
		Note  string        `json:"note,omitempty" jsonschema:"what more means for this answer"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_recent",
		Description: "Recently added items, newest first, default last 60 days, up to limit: more says when the period held more than that.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in recentIn) (*mcp.CallToolResult, recentOut, error) {
		types := in.Types
		if types == "" {
			types = "Movie,Series,Episode"
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 25
		}
		// one answer a client can hold, as the bulk reads cap theirs
		limit = min(limit, episodePageMax)

		// one more than the limit, to know whether the limit cut the period
		opts := embyfin.SearchOptions{
			IncludeItemTypes: types,
			SortBy:           "DateCreated,SortName",
			SortOrder:        sortDescending,
			Limit:            limit + 1,
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
		out := recentOut{Items: []itemSummary{}}
		for i := range items {
			after, err := afterCutoff(&items[i], cutoff)
			if err != nil {
				return nil, recentOut{}, err
			}
			if !after {
				continue
			}
			if len(out.Items) == limit {
				out.More = true
				out.Note = fmt.Sprintf("more than %d items were added in the period: these are the newest %d, so raise limit (up to %d) or narrow the days or types to see the rest", limit, limit, episodePageMax)

				break
			}
			out.Items = append(out.Items, summarise(&items[i]))
		}

		return nil, out, nil
	})

	type genresIn struct {
		Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
		Types   string `json:"types,omitempty"   jsonschema:"comma-separated item types; default Movie,Series"`
	}
	type genresOut struct {
		Genres []string `json:"genres"         jsonschema:"by name"`
		Note   string   `json:"note,omitempty" jsonschema:"set when the library was seen to change while it was read: a genre only an item added then carries may be missing, and one only an item removed then carried may be listed. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read"`
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
		result, err := client.ReadAll(ctx, opts, embyfin.ToAnswer, func(items []embyfin.Item) bool {
			for i := range items {
				for _, g := range items[i].Genres {
					if !seen[g] {
						seen[g] = true
						out.Genres = append(out.Genres, g)
					}
				}
			}
			return true
		})
		if err != nil {
			return nil, genresOut{}, err
		}
		out.Note = result.Changed()
		slices.Sort(out.Genres)

		return nil, out, nil
	})

	type scanIn struct {
		Library string `json:"library,omitempty" jsonschema:"scan only this library, by name or id; default every library"`
	}
	type scanOut struct {
		Started        bool     `json:"started"`
		Library        string   `json:"library,omitempty"         jsonschema:"the library scanned, when one was named"`
		Note           string   `json:"note"                      jsonschema:"where the scan runs and how to see it end, and when every library was scanned in place of the one named"`
		AlreadyRunning []string `json:"already_running,omitempty" jsonschema:"the scans the server showed running when this one was asked for"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "library_scan",
		Description: "Scan the libraries' folders so new, changed and removed files are picked up: every library, or one. It answers once the scan is asked for; the scan runs in the background. " +
			"What a scan changes, and nothing here undoes: an item whose file is gone is dropped, with its metadata, edits, images and places in playlists and collections; a new file becomes an item; a file written over is read again whole, and on Emby the nfo beside it is read again over any edit made since. An item deleted while a scan runs can be listed again until the next one (seen on Jellyfin). On Emby a scan saves every playlist and collection back as it found them, renumbering playlist entries, so a change made to one while it runs can be lost. " +
			"Every library runs as the server's scan task, which task_list shows ending; asked for while one runs, Jellyfin cancels it and starts again. One library is a refresh of its folder, which is not a task and fills in metadata only where it is missing. A Jellyfin library not yet scanned has no folder to refresh, so naming one scans every library, which is what gives it its id. already_running says which scans were running when this one was asked for.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in scanIn) (*mcp.CallToolResult, scanOut, error) {
		folder, err := findLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, scanOut{}, err
		}
		running, err := client.ScansRunning(ctx)
		if err != nil {
			return nil, scanOut{}, fmt.Errorf("could not tell whether a scan was running, so none was started: %w", err)
		}
		const everyNote = "runs in the background as the server's scan task; task_list shows when it ends"
		if folder == nil {
			if err := client.RefreshLibrary(ctx); err != nil {
				return nil, scanOut{}, err
			}
			return nil, scanOut{Started: true, Note: everyNote, AlreadyRunning: running}, nil
		}
		// a library with no id has no folder of its own to refresh yet; the
		// scan of every library is what gives it one
		if folder.ItemID == "" {
			if err := client.RefreshLibrary(ctx); err != nil {
				return nil, scanOut{}, err
			}
			return nil, scanOut{Started: true, Library: folder.Name, AlreadyRunning: running, Note: folder.Name + " is listed without an id, so every library is being scanned, which gives it one: " + everyNote}, nil
		}
		if err := client.ScanLibrary(ctx, folder); err != nil {
			return nil, scanOut{}, err
		}

		return nil, scanOut{Started: true, Library: folder.Name, AlreadyRunning: running, Note: "runs in the background as a refresh of the library's folder, which is no task: task_list does not show it"}, nil
	})

	registerLibraryBrowseTools(r)

	type createIn struct {
		Name      string   `json:"name"                jsonschema:"the library's display name"`
		Type      string   `json:"type"                jsonschema:"movies, tvshows, music, musicvideos, homevideos, books or mixed"`
		Paths     []string `json:"paths"               jsonschema:"folders on the server's own filesystem"`
		Providers bool     `json:"providers,omitempty" jsonschema:"leave the server's internet metadata and image fetchers on; off, the library is built from the files and their nfo sidecars only"`
		Scan      bool     `json:"scan,omitempty"      jsonschema:"scan the new library straight away: on Jellyfin a scan of every library"`
		SaveNfo   *bool    `json:"save_nfo,omitempty"  jsonschema:"true or false; left out, the server's own default: Emby saves no nfo, Jellyfin saves them"`
	}
	type createOut struct {
		librarySummary
		Note string `json:"note,omitempty" jsonschema:"what the new library shares with the others, and what the answer could not read back"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "library_create",
		Description: "Create a library over folders on the server; it has its id at once. save_nfo: " + saveNfoSchema + ". Left out, save_nfo is the server's own default: Emby saves no nfo, and Jellyfin saves one for every item edited, beside the media, over any already there; saves_nfo in the answer says which. " +
			"providers left off builds the library from its files and their nfo sidecars alone. scan=true reads what the folders hold straight away (or run library_scan after): on Jellyfin that is a scan of every library, which drops items whose files are gone anywhere on the server and cancels a scan under way. " +
			"A folder another library already reads, or one inside or around it, is read twice: its items are listed once in each library, and on Emby, which keeps watch state by provider id, the copies share it; note names any such folder. library_delete removes the library again, and the items it made with it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, createOut, error) {
		if in.Name == "" || len(in.Paths) == 0 {
			return nil, createOut{}, errors.New("name and at least one path are required")
		}
		// a name another library has, apart from case, is refused before
		// anything is made: Jellyfin makes "SCRATCH" beside "Scratch" as
		// "SCRATCH2", and a read-back by the name asked for then answered
		// with the other library
		existing, err := client.VirtualFolders(ctx)
		if err != nil {
			return nil, createOut{}, err
		}
		for i := range existing {
			if strings.EqualFold(existing[i].Name, in.Name) {
				return nil, createOut{}, fmt.Errorf("a library named %q already exists: choose a name no library has, apart from case too", existing[i].Name)
			}
		}
		// a folder another library reads is read twice, and said so
		var shared []string
		for _, p := range in.Paths {
			for i := range existing {
				for _, loc := range existing[i].Locations {
					if within(p, loc) || within(loc, p) {
						shared = append(shared, fmt.Sprintf("%s (the %s library reads %s)", p, existing[i].Name, loc))
					}
				}
			}
		}
		if err := client.CreateLibrary(ctx, embyfin.LibrarySpec{
			Name: in.Name, CollectionType: in.Type, Paths: in.Paths, Providers: in.Providers, Refresh: in.Scan, SaveNfo: in.SaveNfo,
		}); err != nil {
			return nil, createOut{}, err
		}
		out := createOut{}
		if len(shared) > 0 {
			out.Note = "another library already reads " + listed(shared, 20) + ": every item under it is listed in both"
		}

		// read back by the exact name asked for: the library made, and no
		// other
		folders, err := client.VirtualFolders(ctx)
		if err != nil {
			return nil, createOut{}, fmt.Errorf("the library %s was created, but reading it back failed: %w", in.Name, err)
		}
		for i := range folders {
			if folders[i].Name == in.Name {
				out.librarySummary = summariseLibrary(&folders[i])
				return nil, out, nil
			}
		}

		return nil, createOut{}, fmt.Errorf("the server answered the creation of %q, but lists no library of that name", in.Name)
	})

	type editIn struct {
		Library     string   `json:"library"                jsonschema:"library name (case-insensitive) or library id"`
		Name        string   `json:"name,omitempty"         jsonschema:"rename the library"`
		AddPaths    []string `json:"add_paths,omitempty"    jsonschema:"folders on the server's own filesystem to add"`
		RemovePaths []string `json:"remove_paths,omitempty" jsonschema:"folders to take out of the library, dropping every item under them (the files stay on disk); needs --enable-delete"`
		SaveNfo     *bool    `json:"save_nfo,omitempty"     jsonschema:"true to start writing nfos, false to stop"`
	}
	type removedFolder struct {
		Path    string         `json:"path"`
		Items   int            `json:"items"           jsonschema:"items the library held under it, which the server drops"`
		ByType  map[string]int `json:"by_type"`
		InLists int            `json:"in_lists"        jsonschema:"of those, how many a playlist or collection held"`
		Lists   []listRef      `json:"lists,omitempty" jsonschema:"the playlists and collections they leave"`
	}
	type editOut struct {
		librarySummary
		Changed    []string        `json:"changed"                   jsonschema:"what was done, in order"`
		Removed    []removedFolder `json:"removed_folders,omitempty" jsonschema:"each folder taken out, and what the library held under it before"`
		WasID      string          `json:"was_id,omitempty"          jsonschema:"the library's id before a rename that gave it another"`
		AccessLost []string        `json:"access_lost,omitempty"     jsonschema:"the accounts given this library alone, by its old id, that no longer see it"`
		Note       string          `json:"note,omitempty"            jsonschema:"what the answer could not read back, and what may still happen"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "library_edit",
		Description: "Rename a library (to a name no other library has, apart from case too), add and take out the folders it is built from, or switch save_nfo: " + saveNfoSchema + ". " +
			"A folder taken out (remove_paths) drops every item the library holds under it - on Emby the moment the folder leaves, on Jellyfin with the scan of every library the change starts - and with them " + goneWithItems + ". The files stay on disk. It needs --enable-delete, and the answer says for each folder how many items it held and how many a playlist or collection held. " +
			"A folder added is read at the library's next scan on Emby (library_scan), and by the scan of every library the change starts on Jellyfin. " +
			"A rename keeps the library's id on Emby. On Jellyfin it gives the library a new id, with the scan of every library it starts (the answer waits for it), and an account given this library by its old id rather than every library no longer sees it until an administrator gives it the library again, which no tool here does: access_lost names them. " +
			"On Jellyfin every folder change and rename starts a scan of every library, because a scan of one library does not see a changed folder: that scan drops items whose files are gone anywhere on the server and cancels a scan under way.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		if in.Library == "" {
			return nil, editOut{}, errors.New("library is required")
		}
		if in.Name == "" && len(in.AddPaths) == 0 && len(in.RemovePaths) == 0 && in.SaveNfo == nil {
			return nil, editOut{}, errors.New("nothing to change: pass name, add_paths, remove_paths or save_nfo")
		}
		if len(in.RemovePaths) > 0 && !r.opts.EnableDelete {
			return nil, editOut{}, fmt.Errorf("refusing to take %s out of %s without --enable-delete: a folder taken out drops every item under it, with %s. Nothing was changed; embyfin-mcp started with --enable-delete takes it out", strings.Join(in.RemovePaths, ", "), in.Library, goneWithItems)
		}
		// the folders and the rename go by name on Jellyfin, so a library not
		// yet scanned can still be edited; only nfo saving needs its id
		folder, err := findLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, editOut{}, err
		}

		// everything that can be checked is checked before anything changes,
		// so a refusal leaves the library as it was rather than half edited
		if in.SaveNfo != nil && folder.ItemID == "" {
			return nil, editOut{}, fmt.Errorf("the server lists the %s library without an id, and nfo saving is switched by id; run library_scan, which gives it one", folder.Name)
		}
		named := map[string]bool{}
		for _, p := range slices.Concat(in.AddPaths, in.RemovePaths) {
			if named[p] {
				return nil, editOut{}, fmt.Errorf("%s is named more than once in add_paths and remove_paths", p)
			}
			named[p] = true
		}
		for _, p := range in.AddPaths {
			if slices.Contains(folder.Locations, p) {
				return nil, editOut{}, fmt.Errorf("%s already holds %s", folder.Name, p)
			}
		}
		for _, p := range in.RemovePaths {
			if !slices.Contains(folder.Locations, p) {
				return nil, editOut{}, fmt.Errorf("%s has no folder %s (have: %s)", folder.Name, p, strings.Join(folder.Locations, ", "))
			}
		}
		// a name another library has, apart from case, is refused as
		// library_create refuses it: two libraries a letter's case apart
		// are one name to whoever picks a library by it, and to this tool's
		// own lookups. The library's own name in another case is its own
		if in.Name != "" && in.Name != folder.Name {
			existing, lerr := client.VirtualFolders(ctx)
			if lerr != nil {
				return nil, editOut{}, lerr
			}
			for i := range existing {
				other := &existing[i]
				same := other.Name == folder.Name || (folder.ItemID != "" && other.ItemID == folder.ItemID)
				if !same && strings.EqualFold(other.Name, in.Name) {
					return nil, editOut{}, fmt.Errorf("a library named %q already exists: rename %s to a name no other library has, apart from case too", other.Name, folder.Name)
				}
			}
		}

		out := editOut{Changed: []string{}}
		// what each folder taken out holds, counted before it goes: once it
		// has, the items are gone and nothing can count them
		if len(in.RemovePaths) > 0 {
			lists, lerr := readMemberships(ctx, client)
			if lerr != nil {
				return nil, editOut{}, fmt.Errorf("could not read the playlists and collections, so nothing was changed: %w", lerr)
			}
			under, ierr := libraryItemsUnder(ctx, client, folder, in.RemovePaths)
			if ierr != nil {
				return nil, editOut{}, fmt.Errorf("could not count what %s holds under %s, so nothing was changed: %w", folder.Name, strings.Join(in.RemovePaths, ", "), ierr)
			}
			for _, p := range in.RemovePaths {
				items := under[p]
				ids := idsOf(items)
				out.Removed = append(out.Removed, removedFolder{Path: p, Items: len(items), ByType: countByType(items), InLists: lists.inAny(ids), Lists: lists.holding(ids)})
			}
		}
		// the accounts given this library by its id, which a rename that
		// gives it a new one takes it from
		var given []string
		if in.Name != "" && in.Name != folder.Name {
			users, uerr := client.Users(ctx)
			if uerr != nil {
				return nil, editOut{}, fmt.Errorf("could not read the accounts that see %s, so nothing was changed: %w", folder.Name, uerr)
			}
			for i := range users {
				if !users[i].Policy.EnableAllFolders && users[i].CanSee(folder) {
					given = append(given, users[i].ID)
				}
			}
		}
		// a step the server refuses after others have landed says what they
		// were, so the caller knows what state the library is in
		failed := func(step string, err error) error {
			if len(out.Changed) == 0 {
				return fmt.Errorf("%s: %w", step, err)
			}

			return fmt.Errorf("%s: %w; already done before it failed: %s", step, err, strings.Join(out.Changed, ", "))
		}
		// before a rename, which gives a Jellyfin library a new id on its next scan
		if in.SaveNfo != nil {
			if err := client.SetLibraryNfo(ctx, folder, *in.SaveNfo); err != nil {
				return nil, editOut{}, failed("switching nfo saving", err)
			}
			if *in.SaveNfo {
				out.Changed = append(out.Changed, "nfo saving on")
			} else {
				out.Changed = append(out.Changed, "nfo saving off")
			}
		}
		for _, p := range in.AddPaths {
			if err := client.AddLibraryPath(ctx, folder, p); err != nil {
				return nil, editOut{}, failed("adding "+p, err)
			}
			out.Changed = append(out.Changed, "added "+p)
		}
		for _, p := range in.RemovePaths {
			if err := client.RemoveLibraryPath(ctx, folder, p); err != nil {
				return nil, editOut{}, failed("removing "+p, err)
			}
			out.Changed = append(out.Changed, "removed "+p)
		}
		name := folder.Name
		if in.Name != "" && in.Name != folder.Name {
			if err := client.RenameLibrary(ctx, folder, in.Name); err != nil {
				return nil, editOut{}, failed("renaming to "+in.Name, err)
			}
			out.Changed = append(out.Changed, "renamed "+folder.Name+" to "+in.Name)
			name = in.Name
		}

		updated, err := findLibrary(ctx, client, name)
		if err != nil {
			return nil, editOut{}, failed("reading the library back", err)
		}
		out.librarySummary = summariseLibrary(updated)
		var notes []string
		if updated.ItemID == "" && folder.ItemID != "" {
			// a renamed Jellyfin library the scan has not reached yet is
			// listed with none of its options: the rename kept them, and
			// the answer says so rather than reading nfo saving off
			out.SavesNfo = folder.SavesNfo
			if in.SaveNfo != nil {
				out.SavesNfo = *in.SaveNfo
			}
			notes = append(notes, "the server lists the renamed library without an id until its library scan reaches it; saves_nfo is as it was set, which a rename keeps")
		}
		if updated.ItemID != "" && folder.ItemID != "" && updated.ItemID != folder.ItemID {
			out.WasID = folder.ItemID
		}
		if len(given) > 0 && updated.ItemID == "" {
			notes = append(notes, fmt.Sprintf("%d account(s) were given this library by its id, which on Jellyfin a rename replaces: whether they still see it cannot be told until the library is listed with its new id", len(given)))
		} else if len(given) > 0 {
			lost, err := accessLost(ctx, client, given, updated)
			if err != nil {
				notes = append(notes, fmt.Sprintf("%d account(s) were given this library by its id, and reading whether they still see it failed: %v", len(given), err))
			}
			out.AccessLost = lost
		}
		if len(in.RemovePaths) > 0 {
			when := "at once"
			if client.Backend() == embyfin.Jellyfin {
				when = "with the scan of every library the change started"
			}
			notes = append(notes, "the server drops the items under the folders taken out "+when+", with "+goneWithItems)
		}
		out.Note = strings.Join(notes, ". ")

		return nil, out, nil
	})

	type deleteLibraryIn struct {
		Library string `json:"library"           jsonschema:"library name (case-insensitive) or library id"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true to delete; without it the call is refused with what the library holds. The library and everything the server keeps for its items go (the media files stay on disk)"`
	}
	type deleteLibraryOut struct {
		Deleted    string         `json:"deleted"`
		ID         string         `json:"id,omitempty"`
		Locations  []string       `json:"locations,omitempty"`
		Items      int            `json:"items"                 jsonschema:"items the library held, not counting folders and collections"`
		ByType     map[string]int `json:"by_type"`
		InLists    int            `json:"in_lists"              jsonschema:"of those, how many a playlist or collection held"`
		Lists      []listRef      `json:"lists,omitempty"       jsonschema:"the playlists and collections they leave"`
		AccessLost []string       `json:"access_lost,omitempty" jsonschema:"the accounts given this library by its id, rather than every library, which no longer have it"`
		Note       string         `json:"note"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name: "library_delete",
		Description: "Remove a library from the server. The media files stay on disk; what the server keeps for the library's items goes with it: " + goneWithItems + ". " +
			"On Emby the items go with the library; on Jellyfin with the scan of every library the removal starts, which also drops items whose files are gone anywhere on the server and cancels a scan under way. An account given this library by its id, rather than every library, no longer has it. " +
			"Without confirm=true it refuses, saying how many items the library holds by type and how many of them a playlist or collection holds; with it, the answer says the same of what went.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteLibraryIn) (*mcp.CallToolResult, deleteLibraryOut, error) {
		// Jellyfin removes a library by name, so one not yet scanned can go too
		folder, err := findLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, deleteLibraryOut{}, err
		}
		if folder == nil {
			return nil, deleteLibraryOut{}, errors.New("library is required")
		}

		// what goes with it, read before: afterwards nothing can count it
		out := deleteLibraryOut{Deleted: folder.Name, ID: folder.ItemID, Locations: folder.Locations, ByType: map[string]int{}}
		counted := folder.ItemID != ""
		if counted {
			var items []embyfin.Item
			// what goes with it decides the delete: a read that cannot be
			// sure of it fails, and nothing is deleted
			read, rerr := client.ReadAll(ctx, embyfin.SearchOptions{ParentID: folder.ItemID, ExcludeItemTypes: containerTypes, Fields: "Path"}, embyfin.ToAct, func(page []embyfin.Item) bool {
				items = append(items, page...)
				return true
			})
			if rerr != nil {
				return nil, deleteLibraryOut{}, fmt.Errorf("could not read what the %s library holds, so nothing was deleted: %w", folder.Name, rerr)
			}
			if changed := read.Changed(); changed != "" {
				return nil, deleteLibraryOut{}, fmt.Errorf("can't be sure what the %s library holds, so nothing was deleted (%s): ask again", folder.Name, changed)
			}
			lists, lerr := readMemberships(ctx, client)
			if lerr != nil {
				return nil, deleteLibraryOut{}, fmt.Errorf("could not read the playlists and collections, so nothing was deleted: %w", lerr)
			}
			ids := idsOf(items)
			out.Items, out.ByType, out.InLists, out.Lists = len(items), countByType(items), lists.inAny(ids), lists.holding(ids)
		}
		users, err := client.Users(ctx)
		if err != nil {
			return nil, deleteLibraryOut{}, fmt.Errorf("could not read the accounts that see the %s library, so nothing was deleted: %w", folder.Name, err)
		}
		for i := range users {
			if !users[i].Policy.EnableAllFolders && users[i].CanSee(folder) {
				out.AccessLost = append(out.AccessLost, users[i].Name)
			}
		}
		holds := "it is listed without an id, so what it holds cannot be counted"
		if counted {
			holds = fmt.Sprintf("it holds %d items (%s), %d of them in a playlist or collection", out.Items, typeCounts(out.ByType), out.InLists)
			if len(out.Lists) > 0 {
				holds += ": " + listsSaid(out.Lists)
			}
		}
		if !in.Confirm {
			lose := ""
			if len(out.AccessLost) > 0 {
				lose = ". Accounts given it by its id lose it: " + strings.Join(out.AccessLost, ", ")
			}
			return nil, deleteLibraryOut{}, fmt.Errorf("refusing to delete the %s library without confirm=true: nothing was changed. %s; with the library go %s%s", folder.Name, holds, goneWithItems, lose)
		}
		if err := client.DeleteLibrary(ctx, folder); err != nil {
			return nil, deleteLibraryOut{}, libraryAfterFailedDelete(ctx, client, folder, err)
		}
		out.Note = "the library is gone, and its items with it"
		if client.Backend() == embyfin.Jellyfin {
			out.Note = "the library is gone; its items go with the scan of every library this started, which task_list shows ending"
		}

		return nil, out, nil
	})
}

// libraryAfterFailedDelete is the error for a library delete the server
// answered with an error, saying whether the library is still listed, read
// back: Jellyfin removes the library and then scans, and a scan that fails
// leaves it gone; Emby answered a database error with a bare 500 (seen on
// 4.11), and the library may be gone or not.
func libraryAfterFailedDelete(ctx context.Context, client *embyfin.Client, folder *embyfin.VirtualFolder, err error) error {
	folders, rerr := client.VirtualFolders(ctx)
	if rerr != nil {
		return fmt.Errorf("the delete of the %s library failed: %w; and reading the libraries back failed, so whether it is gone is not known: %w", folder.Name, err, rerr)
	}
	still := slices.ContainsFunc(folders, func(f embyfin.VirtualFolder) bool {
		return f.Name == folder.Name || folder.ItemID != "" && f.ItemID == folder.ItemID
	})
	if still {
		return fmt.Errorf("the delete of the %s library failed, and it is still listed, read back: %w. What the server took of its items before it failed is not known: library_get counts what is left", folder.Name, err)
	}

	return fmt.Errorf("the %s library is gone, read back, but the delete answered an error: %w", folder.Name, err)
}

// typeCounts says item counts by type, largest first: "Movie 12, Series 3".
func typeCounts(counts map[string]int) string {
	types := make([]string, 0, len(counts))
	for t := range counts {
		types = append(types, t)
	}
	slices.SortFunc(types, func(a, b string) int {
		if counts[a] != counts[b] {
			return counts[b] - counts[a]
		}
		return strings.Compare(a, b)
	})
	parts := make([]string, 0, len(types))
	for _, t := range types {
		parts = append(parts, fmt.Sprintf("%s %d", t, counts[t]))
	}
	if len(parts) == 0 {
		return "none"
	}

	return strings.Join(parts, ", ")
}
