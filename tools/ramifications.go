package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/mediapath"
)

// What a change reaches beyond the item it names. A tool that takes items
// away - a delete, a folder taken out of a library, a library removed - says
// what went with them before anyone asks: the playlists and collections they
// were in, which neither server keeps a place in for an item that is gone,
// and how many items a folder held. These are the reads it says that with.

// goneWithItems is what goes with items a server drops, said the same way by
// every tool that drops them. A film's watch state is kept by provider id on
// both servers (a film with ids moved to a new folder took its watched mark
// and favourite with it, one without lost them); an item's id is a number on
// Emby and made from its path on Jellyfin.
const goneWithItems = "their metadata, edits and images, and their places in playlists and collections, which no tool brings back; a film's watch state is kept by its provider ids, so a film with ids gets it back if the same film is added again, and one without ids, added again at another path, does not. Files added again are new items without the edits: Emby gives them new ids, and Jellyfin, which makes an item's id from its path, the old ids at the same paths"

// identity is who an item is, as a match or a refresh can change it: its
// name and year, and the ids that say which title it is.
type identity struct {
	Name string            `json:"name"`
	Year int               `json:"year,omitempty"`
	IDs  map[string]string `json:"metadata_provider_ids,omitempty" jsonschema:"keyed tmdb, imdb, tvdb"`
}

// identityProviders are the ids that say which title an item is; the others
// a server adds (a website, a wiki page, a collection's id) do not.
var identityProviders = []string{"tmdb", "imdb", "tvdb"}

func identityOf(it *embyfin.Item) identity {
	out := identity{Name: it.Name, Year: it.ProductionYear, IDs: map[string]string{}}
	for _, p := range identityProviders {
		if id := providerID(it, p); id != "" {
			out.IDs[p] = id
		}
	}

	return out
}

// naming.SameTitle says whether two identities name one title: no id held before is
// gone or different after. An id added where there was none fills a gap in
// the same title, as a refresh with the fetchers on does.
func (a identity) sameTitle(b identity) bool {
	for p, id := range a.IDs {
		if b.IDs[p] != id {
			return false
		}
	}

	return true
}

func (a identity) String() string {
	s := a.Name
	if a.Year > 0 {
		s += fmt.Sprintf(" (%d)", a.Year)
	}
	var ids []string
	for _, p := range identityProviders {
		if id := a.IDs[p]; id != "" {
			ids = append(ids, p+" "+id)
		}
	}
	if len(ids) == 0 {
		return s + ", no ids"
	}

	return s + ", " + strings.Join(ids, ", ")
}

// listRef is one playlist or collection, and how many of the items in
// question it held.
type listRef struct {
	Kind string `json:"kind" jsonschema:"playlist or collection"`
	ID   string `json:"id"`
	Name string `json:"name"`
	Held int    `json:"held" jsonschema:"how many of the items it held"`
}

// memberships is every playlist and collection on the server, by the items
// each holds.
type memberships struct {
	lists  []listRef
	byItem map[string][]int // item id -> indexes into lists
}

// readMemberships reads every collection's and every playlist's members,
// once for the call and readsAtOnce lists at a time: one read of each list is
// what finding the lists an item is in costs, since neither server says
// which lists hold an item, nor answers any one read with every list's
// members (tried on Emby 4.10 and Jellyfin 12.1: the collections' own folder
// read with its children, a library read grouping items into their
// collections). A collection holds what it lists itself (a series, not its
// episodes); a playlist of a series holds its episodes, so it is found by
// those. Each list is read whole, whoever can see what it holds (see
// CollectionItems and PlaylistHeld): on Jellyfin one that cannot be - no
// administrator sees every library - is an error, not a list short of what
// it holds.
func readMemberships(ctx context.Context, client *embyfin.Client) (memberships, error) {
	var lists []listRef
	for _, kind := range []struct{ itemType, label string }{{"BoxSet", "collection"}, {"Playlist", "playlist"}} {
		found, _, err := client.Search(ctx, embyfin.SearchOptions{IncludeItemTypes: kind.itemType, Fields: embyfin.FieldsLean})
		if err != nil {
			return memberships{}, fmt.Errorf("reading the %ss: %w", kind.label, err)
		}
		for i := range found {
			lists = append(lists, listRef{Kind: kind.label, ID: found[i].ID, Name: found[i].Name})
		}
	}
	members := make([][]string, len(lists))
	if err := eachAtOnce(ctx, len(lists), func(ctx context.Context, i int) error {
		var err error
		if lists[i].Kind == "playlist" {
			members[i], err = client.PlaylistMembers(ctx, lists[i].ID)
		} else {
			members[i], err = client.CollectionMembers(ctx, lists[i].ID)
		}
		if err != nil {
			return fmt.Errorf("reading what the %s %s holds: %w", lists[i].Kind, lists[i].Name, err)
		}
		return nil
	}); err != nil {
		return memberships{}, err
	}
	m := memberships{lists: lists, byItem: map[string][]int{}}
	for at := range lists {
		for _, id := range slices.Compact(slices.Sorted(slices.Values(members[at]))) {
			m.byItem[id] = append(m.byItem[id], at)
		}
	}

	return m, nil
}

// readsAtOnce is how many reads a sweep of every list makes at once.
const readsAtOnce = 8

// eachAtOnce calls fn for every index below n, readsAtOnce at a time, and
// returns the first error; once one has failed, the calls not yet begun are
// not made.
func eachAtOnce(ctx context.Context, n int, fn func(ctx context.Context, i int) error) error {
	inner, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	slots := make(chan struct{}, readsAtOnce)
	for i := range n {
		select {
		case slots <- struct{}{}:
		case <-inner.Done():
		}
		if inner.Err() != nil {
			break
		}
		wg.Go(func() {
			defer func() { <-slots }()
			if err := fn(inner, i); err != nil {
				mu.Lock()
				if first == nil {
					first = err
					cancel()
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if first != nil {
		return first
	}

	return ctx.Err()
}

// holding is the lists holding any of ids, each with how many of them,
// collections first, by name.
func (m memberships) holding(ids []string) []listRef {
	held := map[int]int{}
	for _, id := range slices.Compact(slices.Sorted(slices.Values(ids))) {
		for _, at := range m.byItem[id] {
			held[at]++
		}
	}
	out := make([]listRef, 0, len(held))
	for at, n := range held {
		l := m.lists[at]
		l.Held = n
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b listRef) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.Name, b.Name)
	})

	return out
}

// inAny counts the ids some list holds.
func (m memberships) inAny(ids []string) int {
	n := 0
	for _, id := range slices.Compact(slices.Sorted(slices.Values(ids))) {
		if len(m.byItem[id]) > 0 {
			n++
		}
	}

	return n
}

// listsSaid names lists for a sentence: "the collection Alien Saga (2 of
// them), the playlist Friday".
func listsSaid(lists []listRef) string {
	names := make([]string, 0, len(lists))
	for _, l := range lists {
		names = append(names, fmt.Sprintf("the %s %s (%d)", l.Kind, l.Name, l.Held))
	}

	return listed(names, 20)
}

// libraryItemsUnder is every item a library holds at or under each of
// folders on disk - films, series, seasons, episodes, albums, songs - by
// folder, read with one sweep of the library for them all, since neither
// server finds items by folder. Folders and collections are not items to
// count.
func libraryItemsUnder(ctx context.Context, client *embyfin.Client, library *embyfin.VirtualFolder, folders []string) (map[string][]embyfin.Item, error) {
	if library.ItemID == "" {
		return nil, fmt.Errorf("the server lists the %s library without an id, so what it holds under %s cannot be read", library.Name, strings.Join(folders, ", "))
	}
	out := make(map[string][]embyfin.Item, len(folders))
	// what a change takes with it: a read that cannot be sure of it fails
	read, err := client.ReadAll(ctx, embyfin.SearchOptions{ParentID: library.ItemID, ExcludeItemTypes: containerTypes, Fields: "Path"}, embyfin.ToAct, func(items []embyfin.Item) bool {
		for i := range items {
			for _, folder := range folders {
				if mediapath.Within(items[i].Path, folder) {
					out[folder] = append(out[folder], items[i])
				}
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if changed := read.Changed(); changed != "" {
		return nil, fmt.Errorf("can't be sure what the %s library holds under %s: %s", library.Name, strings.Join(folders, ", "), changed)
	}

	return out, nil
}

// besideMedia is what a change did to the folder beside an item's media, as
// far as listing it before and after shows: a file removed or newly added.
// A file written over in place keeps its name, and neither server's folder
// listing gives a size or a time, so that is not seen; nor is anything in a
// folder below it.
type besideMedia struct {
	FolderRead    string   `json:"folder_read,omitempty"          jsonschema:"the folder listed before and after: the item's own, or the one holding its file. A file written over in place (such as the nfo) and files in the folders below it are not seen"`
	RemovedBeside []string `json:"removed_beside_media,omitempty" jsonschema:"files removed from that folder: gone from disk, and no tool puts them back"`
	AddedBeside   []string `json:"added_beside_media,omitempty"   jsonschema:"files newly added to that folder"`
}

// besideMediaSaid is what a change's description says of besideMedia.
const besideMediaSaid = "The folder beside the media is listed before and after, and the answer names the files removed from it or newly added to it; a file written over in place (such as the nfo) and files in the folders below it are not seen"

// nfoUnseen is the note for a change in a library that saves nfos, which may
// have written the nfo beside the media over in place.
func nfoUnseen(folder *embyfin.VirtualFolder) string {
	if folder == nil || !folder.SavesNfo {
		return ""
	}

	return "the " + folder.Name + " library saves nfos, so the server may have written the nfo beside the media over, which a listing of the folder does not show"
}

// filesBeside reads what is beside an item's media: its own folder for a
// folder item, the folder holding its file for any other. An item with
// nothing on disk has no folder, "" and nothing; one whose folder the server
// cannot find has the folder and nothing.
func filesBeside(ctx context.Context, client *embyfin.Client, it *embyfin.Item) (string, []embyfin.FolderEntry, error) {
	if it.Path == "" || !mediapath.OnDisk(it.Path) {
		return "", nil, nil
	}
	dir := mediapath.Dir(it.Path)
	if it.IsFolder {
		dir = mediapath.Trim(it.Path)
	}
	entries, found, err := client.ListFolder(ctx, dir)
	if err != nil || !found {
		return dir, nil, err
	}

	return dir, entries, nil
}

// besideAfter reads a folder filesBeside read again, and says what went from
// it and what came.
func besideAfter(ctx context.Context, client *embyfin.Client, dir string, had []embyfin.FolderEntry) (besideMedia, error) {
	out := besideMedia{FolderRead: dir}
	if dir == "" {
		return out, nil
	}
	now, found, err := client.ListFolder(ctx, dir)
	if err != nil {
		return out, err
	}
	if !found {
		now = nil
	}
	in := func(list []embyfin.FolderEntry, path string) bool {
		return slices.ContainsFunc(list, func(e embyfin.FolderEntry) bool { return mediapath.Trim(e.Path) == mediapath.Trim(path) })
	}
	for _, e := range had {
		if !in(now, e.Path) {
			out.RemovedBeside = append(out.RemovedBeside, e.Path)
		}
	}
	for _, e := range now {
		if !in(had, e.Path) {
			out.AddedBeside = append(out.AddedBeside, e.Path)
		}
	}
	slices.Sort(out.RemovedBeside)
	slices.Sort(out.AddedBeside)

	return out, nil
}

// countByType counts items by their type.
func countByType(items []embyfin.Item) map[string]int {
	out := map[string]int{}
	for i := range items {
		out[items[i].Type]++
	}

	return out
}

// idsOf is items' ids.
func idsOf(items []embyfin.Item) []string {
	out := make([]string, 0, len(items))
	for i := range items {
		out = append(out, items[i].ID)
	}

	return out
}
