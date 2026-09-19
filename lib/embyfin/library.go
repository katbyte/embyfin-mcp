package embyfin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/go-kt/pointer"

	apiclient "github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
)

type VirtualFolder struct {
	Name           string   `json:"Name"`
	CollectionType string   `json:"CollectionType,omitempty"` // movies, tvshows, music, ...; empty = mixed
	Locations      []string `json:"Locations,omitempty"`
	ItemID         string   `json:"ItemId,omitempty"`
	// GUID is Emby's other id for the library, the one a user's
	// EnabledFolders lists; Jellyfin's lists the ItemId
	GUID string `json:"Guid,omitempty"`
	// MetadataFetchers is the internet metadata fetchers the library's
	// options list per item type (Movie, Series, Episode...); a type not
	// listed takes the server's defaults
	MetadataFetchers map[string][]string `json:"MetadataFetchers,omitempty"`
	// SavesNfo is whether an edit to one of the library's items is written
	// back to an nfo beside its media file (see nfoSaver)
	SavesNfo bool `json:"SavesNfo,omitempty"`
}

// nfoSaver is the metadata saver that writes an item's metadata to an nfo
// beside its media file when the item changes: Emby names the file after the
// media (Interstellar (2014).nfo), Jellyfin movie.nfo or tvshow.nfo. Both
// default it off for a new library, but Jellyfin runs every saver for a
// library whose options list none at all (null), which a library created
// through the API without a list has; Emby stores such a library with an
// empty list.
const nfoSaver = "Nfo"

// savers is the metadata saver list that turns the nfo saver on or off.
func savers(nfo bool) []string {
	if nfo {
		return []string{nfoSaver}
	}

	return []string{}
}

// FetchersOff reports whether the library fetches no metadata from the
// internet for an item type: its options list the type with no fetchers, as
// a library created without providers does. Such a library knows only what
// its files and nfo sidecars say, so a refresh has nothing to fetch.
func (f *VirtualFolder) FetchersOff(itemType string) bool {
	fetchers, listed := f.MetadataFetchers[itemType]

	return listed && len(fetchers) == 0
}

// FolderOf returns the library whose folders hold path (the deepest, when
// libraries nest), or nil when none does: a collection or playlist is not in
// a library's folders.
func FolderOf(folders []VirtualFolder, path string) *VirtualFolder {
	var found *VirtualFolder
	deepest := -1
	for i := range folders {
		for _, loc := range folders[i].Locations {
			loc = strings.TrimRight(loc, "/")
			if loc != "" && strings.HasPrefix(path, loc+"/") && len(loc) > deepest {
				found, deepest = &folders[i], len(loc)
			}
		}
	}

	return found
}

// VirtualFolders lists the server's libraries. Emby 4.10 documents the
// paged /Library/VirtualFolders/Query; the bare /Library/VirtualFolders it
// also still answers wrapped the list in an envelope on some versions and
// not on others.
func (c *Client) VirtualFolders(ctx context.Context) ([]VirtualFolder, error) {
	if c.isEmby() {
		res, err := c.emby.GetLibraryVirtualFoldersQuery(ctx, emby.GetLibraryVirtualFoldersQueryOperationOptions{})
		if err != nil {
			return nil, err
		}
		folders := make([]VirtualFolder, 0, len(res.Model.Items))
		for i := range res.Model.Items {
			folders = append(folders, virtualFolderFromEmby(&res.Model.Items[i]))
		}

		return folders, nil
	}

	res, err := c.jf.GetVirtualFolders(ctx)
	if err != nil {
		return nil, err
	}
	folders := make([]VirtualFolder, 0, len(res.Model))
	for i := range res.Model {
		folders = append(folders, virtualFolderFromJF(&res.Model[i]))
	}

	return folders, nil
}

// Persons searches people (actors, directors, ...) known to the library.
func (c *Client) Persons(ctx context.Context, searchTerm string, limit int) ([]Item, error) {
	if c.isEmby() {
		res, err := c.emby.GetPersons(ctx, emby.GetPersonsOperationOptions{SearchTerm: searchTerm, Limit: limit})
		if err != nil {
			return nil, err
		}

		return itemsFromEmby(res.Model.Items), nil
	}

	res, err := c.jf.GetPersons(ctx, jf.GetPersonsOperationOptions{SearchTerm: searchTerm, Limit: limit})
	if err != nil {
		return nil, err
	}

	return itemsFromJF(res.Model.Items), nil
}

// LibrarySpec describes a library to create.
type LibrarySpec struct {
	Name           string
	CollectionType string   // movies, tvshows, music, mixed, ...
	Paths          []string // server-side folders
	// Providers leaves the server's internet metadata and image fetchers on.
	// Off, the library is built from local files and nfo sidecars only.
	Providers bool
	// Refresh scans the new library straight away.
	Refresh bool
	// SaveNfo, when set, turns the library's nfo saver on or off. Unset, the
	// servers differ: Emby saves no nfo, Jellyfin saves them.
	SaveNfo *bool
}

// libraryTypes lists the item types a collection can hold, which is what the
// per-type fetcher settings are keyed by.
func libraryTypes(collectionType string) []string {
	switch collectionType {
	case "movies":
		return []string{"Movie"}
	case "tvshows":
		return []string{"Series", "Season", "Episode"}
	case "music":
		return []string{"MusicArtist", "MusicAlbum", "Audio"}
	default:
		return []string{"Movie", "Series", "Season", "Episode", "MusicArtist", "MusicAlbum", "Audio", "Video"}
	}
}

// CreateLibrary adds a library. Emby takes everything in the JSON body;
// Jellyfin takes the name, type and paths as query parameters and only the
// options in the body. Both new libraries get realtime monitoring and
// chapter image extraction switched off (both servers default them on);
// every other option is left out, so the server's defaults apply.
//
// Without providers, each item type the collection holds gets an empty
// fetcher list, which switches the internet providers off for that type
// (Jellyfin does not honour EnableInternetProviders; both servers read the
// per-type lists). With providers, Emby needs its default fetchers listed:
// its web client posts them when it creates a library, and a library created
// with none configured never goes to the internet. Jellyfin applies its
// defaults itself.
func (c *Client) CreateLibrary(ctx context.Context, spec LibrarySpec) error {
	if c.isEmby() {
		options := &emby.LibraryOptions{
			EnableRealtimeMonitor:        new(false),
			EnableChapterImageExtraction: new(false),
		}
		for _, p := range spec.Paths {
			options.PathInfos = append(options.PathInfos, emby.MediaPathInfo{Path: p})
		}
		if spec.Providers {
			defaults, err := c.embyDefaultTypeOptions(ctx, spec.CollectionType)
			if err != nil {
				return err
			}
			options.TypeOptions = defaults
		} else {
			for _, t := range libraryTypes(spec.CollectionType) {
				options.TypeOptions = append(options.TypeOptions, emby.TypeOptions{Type: t, MetadataFetchers: []string{}, ImageFetchers: []string{}})
			}
		}
		if spec.SaveNfo != nil {
			options.MetadataSavers = savers(*spec.SaveNfo)
		}

		_, err := c.emby.PostLibraryVirtualFolders(ctx, emby.LibraryAddVirtualFolder{
			Name:           spec.Name,
			CollectionType: spec.CollectionType,
			RefreshLibrary: new(spec.Refresh),
			Paths:          spec.Paths,
			LibraryOptions: options,
		})

		return err
	}

	options := &jf.LibraryOptions{
		EnableRealtimeMonitor:          new(false),
		EnableChapterImageExtraction:   new(false),
		EnableTrickplayImageExtraction: new(false),
	}
	for _, p := range spec.Paths {
		options.PathInfos = append(options.PathInfos, jf.MediaPathInfo{Path: p})
	}
	if !spec.Providers {
		for _, t := range libraryTypes(spec.CollectionType) {
			options.TypeOptions = append(options.TypeOptions, jf.TypeOptions{Type: t, MetadataFetchers: []string{}, ImageFetchers: []string{}})
		}
	}
	if spec.SaveNfo != nil {
		options.MetadataSavers = savers(*spec.SaveNfo)
	}
	query := jf.AddVirtualFolderOperationOptions{
		Name:           spec.Name,
		Paths:          spec.Paths,
		RefreshLibrary: new(spec.Refresh),
	}
	if spec.CollectionType != "mixed" {
		query.CollectionType = jf.CollectionTypeOptions(spec.CollectionType)
	}
	_, err := c.jf.AddVirtualFolder(ctx, jf.AddVirtualFolderDto{LibraryOptions: options}, query)

	return err
}

// embyDefaultTypeOptions asks Emby which fetchers a new library of this kind
// enables by default, in the shape LibraryOptions.TypeOptions takes. Without
// LibraryContentType Emby answers for every item type at once.
func (c *Client) embyDefaultTypeOptions(ctx context.Context, collectionType string) ([]emby.TypeOptions, error) {
	res, err := c.emby.GetLibrariesAvailableOptions(ctx, emby.GetLibrariesAvailableOptionsOperationOptions{
		LibraryContentType: collectionType,
		IsNewLibrary:       new(true),
	})
	if err != nil {
		return nil, err
	}

	enabled := func(infos []emby.LibraryOptionInfo) []string {
		names := []string{}
		for _, f := range infos {
			if pointer.From(f.DefaultEnabled) {
				names = append(names, f.Name)
			}
		}
		return names
	}
	out := make([]emby.TypeOptions, 0, len(res.Model.TypeOptions))
	for _, t := range res.Model.TypeOptions {
		metadata, images := enabled(t.MetadataFetchers), enabled(t.ImageFetchers)
		out = append(out, emby.TypeOptions{
			Type: t.Type, MetadataFetchers: metadata, MetadataFetcherOrder: metadata,
			ImageFetchers: images, ImageFetcherOrder: images,
		})
	}

	return out, nil
}

// DeleteLibrary removes a library. The media files stay on disk. Jellyfin
// takes the library's name, Emby its id (and answers 500 to a name). Emby
// removes the library's items with it; Jellyfin keeps them, still in
// playlists, favourites and a user's counts and readable by id, until its
// library scan finds their folder gone, so there the removal asks for that
// scan.
func (c *Client) DeleteLibrary(ctx context.Context, folder *VirtualFolder) error {
	if c.isEmby() {
		_, err := c.emby.PostLibraryVirtualFoldersDelete(ctx, emby.LibraryRemoveVirtualFolder{Id: folder.ItemID})
		return err
	}

	_, err := c.jf.RemoveVirtualFolder(ctx, jf.RemoveVirtualFolderOperationOptions{Name: folder.Name, RefreshLibrary: new(true)})

	return err
}

// SetLibraryNfo turns a library's nfo saver on or off. The options are posted
// back whole, as the server listed them with only the saver list changed,
// because both servers replace a library's options with what is posted.
func (c *Client) SetLibraryNfo(ctx context.Context, folder *VirtualFolder, on bool) error {
	if folder.ItemID == "" {
		return errNoLibraryID
	}
	if c.isEmby() {
		res, err := c.emby.GetLibraryVirtualFoldersQuery(ctx, emby.GetLibraryVirtualFoldersQueryOperationOptions{})
		if err != nil {
			return err
		}
		for _, f := range res.Model.Items {
			if f.ItemId == folder.ItemID && f.LibraryOptions != nil {
				f.LibraryOptions.MetadataSavers = savers(on)
				_, err := c.emby.PostLibraryVirtualFoldersLibraryOptions(ctx, emby.LibraryUpdateLibraryOptions{Id: folder.ItemID, LibraryOptions: f.LibraryOptions})

				return err
			}
		}

		return fmt.Errorf("the server lists no options for the %s library", folder.Name)
	}

	res, err := c.jf.GetVirtualFolders(ctx)
	if err != nil {
		return err
	}
	for _, f := range res.Model {
		if f.ItemId == folder.ItemID && f.LibraryOptions != nil {
			f.LibraryOptions.MetadataSavers = savers(on)
			_, err := c.jf.UpdateLibraryOptions(ctx, jf.UpdateLibraryOptionsDto{Id: folder.ItemID, LibraryOptions: f.LibraryOptions})

			return err
		}
	}

	return fmt.Errorf("the server lists no options for the %s library", folder.Name)
}

// errNoLibraryID is a Jellyfin library that has not been scanned yet: it
// gets its id on its first scan, and every per-library call needs one.
var errNoLibraryID = errors.New("the library has no id until its first scan; scan every library first")

// RenameLibrary renames a library. Emby takes its id, Jellyfin its name.
func (c *Client) RenameLibrary(ctx context.Context, folder *VirtualFolder, newName string) error {
	if c.isEmby() {
		_, err := c.emby.PostLibraryVirtualFoldersName(ctx, emby.LibraryRenameVirtualFolder{Id: folder.ItemID, NewName: newName})
		return err
	}

	_, err := c.jf.RenameVirtualFolder(ctx, jf.RenameVirtualFolderOperationOptions{Name: folder.Name, NewName: newName, RefreshLibrary: new(false)})

	return err
}

// AddLibraryPath adds a folder on the server to a library. Emby picks the
// folder up on the library's next scan. Jellyfin's scan of one library does
// not see a changed folder (only its library scan revalidates the folders),
// so there the change asks for that scan.
func (c *Client) AddLibraryPath(ctx context.Context, folder *VirtualFolder, path string) error {
	if c.isEmby() {
		_, err := c.emby.PostLibraryVirtualFoldersPaths(ctx, emby.LibraryAddMediaPath{
			Id: folder.ItemID, PathInfo: &emby.MediaPathInfo{Path: path}, RefreshLibrary: new(false),
		})

		return err
	}

	_, err := c.jf.AddMediaPath(ctx, jf.MediaPathDto{Name: folder.Name, PathInfo: &jf.MediaPathInfo{Path: path}}, jf.AddMediaPathOperationOptions{RefreshLibrary: new(true)})

	return err
}

// RemoveLibraryPath takes a folder out of a library; the files stay on disk.
// As with AddLibraryPath, Jellyfin's change asks for its library scan.
func (c *Client) RemoveLibraryPath(ctx context.Context, folder *VirtualFolder, path string) error {
	if c.isEmby() {
		_, err := c.emby.PostLibraryVirtualFoldersPathsDelete(ctx, emby.LibraryRemoveMediaPath{Id: folder.ItemID, Path: path, RefreshLibrary: new(false)})
		return err
	}

	_, err := c.jf.RemoveMediaPath(ctx, jf.RemoveMediaPathOperationOptions{Name: folder.Name, Path: path, RefreshLibrary: new(true)})

	return err
}

// PathExists says whether the server can see a file or folder at path, as
// its own process sees it - which is what decides what its delete would
// touch. Both servers answer 404 for a path they cannot find, and each checks
// only the kind it is asked about, so a folder is asked after, then a file.
func (c *Client) PathExists(ctx context.Context, path string) (bool, error) {
	for _, isFile := range []bool{false, true} {
		var err error
		if c.isEmby() {
			_, err = c.emby.PostEnvironmentValidatePath(ctx, emby.ValidatePath{IsFile: new(isFile)}, emby.PostEnvironmentValidatePathOperationOptions{Path: path})
		} else {
			_, err = c.jf.ValidatePath(ctx, jf.ValidatePathDto{Path: path, IsFile: new(isFile)})
		}
		switch {
		case err == nil:
			return true, nil
		case !apiclient.IsNotFound(err):
			return false, err
		}
	}

	return false, nil
}

// ScanLibrary scans one library's folders for new, changed and removed
// files: a refresh of the library's own folder, recursive, that fills in
// metadata only where it is missing (what the web clients' "scan library
// files" sends).
func (c *Client) ScanLibrary(ctx context.Context, folder *VirtualFolder) error {
	if folder.ItemID == "" {
		return errNoLibraryID
	}
	if c.isEmby() {
		_, err := c.emby.PostItemsByIdRefresh(ctx, folder.ItemID, emby.BaseRefreshRequest{}, emby.PostItemsByIdRefreshOperationOptions{
			Recursive:           new(true),
			MetadataRefreshMode: emby.MetadataRefreshModeDefault,
			ImageRefreshMode:    emby.MetadataRefreshModeDefault,
		})

		return err
	}

	_, err := c.jf.RefreshItem(ctx, folder.ItemID, jf.RefreshItemOperationOptions{
		MetadataRefreshMode: jf.MetadataRefreshModeDefault,
		ImageRefreshMode:    jf.MetadataRefreshModeDefault,
	})

	return err
}

// Person looks a person up by name, exactly as the library spells it.
func (c *Client) Person(ctx context.Context, name, userID string) (*Item, error) {
	if c.isEmby() {
		res, err := c.emby.GetPersonsByName(ctx, name, emby.GetPersonsByNameOperationOptions{UserId: userID})
		if err != nil {
			return nil, err
		}
		it := itemFromEmby(res.Model)

		return &it, nil
	}

	res, err := c.jf.GetPerson(ctx, name, jf.GetPersonOperationOptions{UserId: userID})
	if err != nil {
		return nil, err
	}
	it := itemFromJF(res.Model)

	return &it, nil
}
