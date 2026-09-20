package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Items the server holds under a folder no library covers.
//
// A library whose folder is renamed or removed can leave its items behind.
// The server keeps them - a sweep and an id still reach them - but no library
// lists them, no scan revisits them, and nothing the server runs on its own
// removes them, because a scan only walks library folders. Added again, the
// folder leaves another copy of everything each time, with no poster and no
// metadata, and every audit that sweeps the server counts them.
//
// audit_orphans finds them. item_orphans_delete removes them, and only under a
// folder the server cannot see: deleting an item deletes its file, so the one
// safe case is a folder that is gone.

// orphanTypes are the kinds of item that stand for a file or folder on disk,
// which is what a removed library leaves behind. People, genres, studios,
// collections and playlists live in the server's own data folders, outside
// every library by design, and Emby keeps music artists there too, so those
// are not swept. Nor are audiobooks: Emby does not know the type, and asked
// for it alone answers with every item it holds.
const orphanTypes = "Movie,Series,Season,Episode,Video,MusicVideo,Trailer,Audio,Book,Photo,PhotoAlbum,MusicAlbum,Folder"

// orphanPage is how many items one sweep request reads. A server pays for a
// page mostly in walking past the ones before it, and about the same for ten
// thousand rows as for one thousand, so a sweep of a few hundred thousand
// items goes in a few large pages.
const orphanPage = 10000

// orphanSweepSort is the order the sweep reads in: when items were added,
// then name. The default, name alone, costs Emby several times as much deep
// into a large library, and in this order an item added mid-sweep lands at
// the end rather than shifting a page not yet read.
const orphanSweepSort = "DateCreated,SortName"

// orphanBatch is how many items one delete request names: few enough for
// the ids to fit a URL on either server, and a request that fails is settled
// one id at a time, which is cheaper for a small batch.
const orphanBatch = 50

// orphanDeleteDefault and orphanDeleteMax bound one item_orphans_delete call:
// every call sweeps the server first, so a call should do real work, but one
// that runs for many minutes outlasts the client waiting on it.
const (
	orphanDeleteDefault = 2000
	orphanDeleteMax     = 10000
)

// Where a folder stands on the server, as the orphan tools report it.
const (
	folderMissing = "missing"
	folderPresent = "present"
	folderUnknown = "unknown"
)

// libraryPath is one folder a library reads from.
type libraryPath struct{ library, path string }

func libraryPaths(ctx context.Context, client *embyfin.Client) ([]libraryPath, error) {
	folders, err := client.VirtualFolders(ctx)
	if err != nil {
		return nil, err
	}

	var out []libraryPath
	for _, f := range folders {
		for _, loc := range f.Locations {
			if loc = trimSep(loc); loc != "" {
				out = append(out, libraryPath{library: f.Name, path: loc})
			}
		}
	}

	return out, nil
}

func isSep(b byte) bool { return b == '/' || b == '\\' }

// isTop is the top of a filesystem - /, C:\, or \\host\share - which is never
// reported or cleaned.
func isTop(p string) bool {
	switch {
	case p == "/":
		return true
	case len(p) == 3 && p[1] == ':' && isSep(p[2]):
		return true
	case strings.HasPrefix(p, `\\`):
		return strings.Count(strings.TrimSuffix(p[2:], `\`), `\`) <= 1
	}

	return false
}

// trimSep drops trailing separators, keeping the one that is a whole top.
func trimSep(p string) string {
	for len(p) > 1 && isSep(p[len(p)-1]) && !isTop(p) {
		p = p[:len(p)-1]
	}

	return p
}

// parentDir is the folder holding p, or "" when p is a filesystem's top.
func parentDir(p string) string {
	p = trimSep(p)
	if isTop(p) {
		return ""
	}

	i := strings.LastIndexAny(p, `/\`)
	switch {
	case i < 0:
		return ""
	case i == 0:
		return p[:1]
	case i == 2 && p[1] == ':':
		return p[:3]
	}

	return p[:i]
}

// within says whether path is root or inside it, a whole path segment at a
// time: /data/docs is not inside /data/doc.
func within(path, root string) bool {
	path, root = trimSep(path), trimSep(root)
	switch {
	case root == "" || !strings.HasPrefix(path, root):
		return false
	case len(path) == len(root):
		return true
	}

	return isSep(root[len(root)-1]) || isSep(path[len(root)])
}

// onDisk says whether a path names a place on a filesystem, rather than a URL
// or a name the server made up: nothing else can be checked, or left behind.
func onDisk(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || (len(p) > 2 && p[1] == ':' && isSep(p[2]))
}

// hasDots says whether a path steps through . or .., which only the server
// would resolve: every check here compares paths as they are written.
func hasDots(p string) bool {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == "." || seg == ".." {
			return true
		}
	}

	return false
}

// inLibrary is the library folder path is inside, if any.
func inLibrary(path string, libs []libraryPath) (libraryPath, bool) {
	for _, l := range libs {
		if within(path, l.path) {
			return l, true
		}
	}

	return libraryPath{}, false
}

// holdsLibrary is a library folder inside folder, if any.
func holdsLibrary(folder string, libs []libraryPath) (libraryPath, bool) {
	for _, l := range libs {
		if within(l.path, folder) {
			return l, true
		}
	}

	return libraryPath{}, false
}

// orphanFolder is the folder an orphan is reported under: the highest one
// above it that holds no library folder, which is where the removed library's
// folder was. The one above that holds libraries still in use, and naming it
// would be naming them.
func orphanFolder(path string, libs []libraryPath) string {
	folder := trimSep(path)
	for {
		up := parentDir(folder)
		if up == "" || isTop(up) {
			return folder
		}
		if _, holds := holdsLibrary(up, libs); holds {
			return folder
		}
		folder = up
	}
}

// sweepOrphans reads every item standing for a file or folder and keeps the
// ones outside every library folder - with root set, only those at or below
// it. It names no library and no user, because an orphan belongs to neither:
// only a sweep of everything the server holds reaches it.
func sweepOrphans(ctx context.Context, client *embyfin.Client, libs []libraryPath, root string) ([]embyfin.Item, int, error) {
	var found []embyfin.Item
	scanned := 0
	opts := embyfin.SearchOptions{
		IncludeItemTypes: orphanTypes, Fields: "Path,ParentId",
		SortBy: orphanSweepSort, SortOrder: "Ascending", Limit: orphanPage,
	}
	for start := 0; ; start += orphanPage {
		opts.StartIndex = start
		items, total, err := client.Search(ctx, opts)
		if err != nil {
			return nil, scanned, err
		}
		for i := range items {
			scanned++
			it := items[i]
			if !onDisk(it.Path) {
				continue
			}
			if _, in := inLibrary(it.Path, libs); in {
				continue
			}
			if root != "" && !within(it.Path, root) {
				continue
			}
			found = append(found, it)
		}
		if len(items) < orphanPage || start+len(items) >= total {
			return found, scanned, nil
		}
	}
}

// folderState asks the server whether it can see a folder.
func folderState(ctx context.Context, client *embyfin.Client, folder string) (state, note string) {
	exists, err := client.PathExists(ctx, folder)
	switch {
	case err != nil:
		return folderUnknown, "could not ask the server whether it can see this folder: " + err.Error()
	case exists:
		return folderPresent, ""
	}

	return folderMissing, ""
}

// refuseVisible is the check every delete waits on: the server must not be
// able to see the folder, because what it can see it would delete.
func refuseVisible(ctx context.Context, client *embyfin.Client, folder string) error {
	exists, err := client.PathExists(ctx, folder)
	switch {
	case err != nil:
		return fmt.Errorf("could not ask the server whether it can see %s: %w", folder, err)
	case exists:
		return fmt.Errorf("the server can still see %s: the files under it are real, and deleting their items would delete them too. Only the items under a folder the server cannot find are deleted", folder)
	}

	return nil
}

// orphanRow is one item left behind.
type orphanRow struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
	Path string `json:"path"`
}

// orphanRows is the first n of items by path.
func orphanRows(items []embyfin.Item, n int) []orphanRow {
	sorted := slices.SortedFunc(slices.Values(items), func(a, b embyfin.Item) int { return strings.Compare(a.Path, b.Path) })
	out := make([]orphanRow, 0, min(n, len(sorted)))
	for _, it := range sorted[:min(n, len(sorted))] {
		out = append(out, orphanRow{ID: it.ID, Type: it.Type, Name: it.Name, Path: it.Path})
	}

	return out
}

// orphanFailure is an item the server would not delete.
type orphanFailure struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Error string `json:"error"`
}

// stillHeld says which of ids the server still holds.
func stillHeld(ctx context.Context, client *embyfin.Client, ids []string) (map[string]bool, error) {
	items, _, err := client.Search(ctx, embyfin.SearchOptions{IDs: strings.Join(ids, ","), Fields: "Path", Limit: len(ids)})
	if err != nil {
		return nil, err
	}

	// only the ids asked after: Emby answers an Ids filter it cannot parse
	// with the whole library
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	held := map[string]bool{}
	for _, it := range items {
		if want[it.ID] {
			held[it.ID] = true
		}
	}

	return held, nil
}

// deleteOrphanBatch deletes one batch and says how many of it are gone. A
// request that fails part-way - Jellyfin stops at the first id it cannot
// find - is settled one id at a time: what is already gone counts, what is
// left is deleted on its own, and what will not go is reported.
func deleteOrphanBatch(ctx context.Context, client *embyfin.Client, batch []embyfin.Item) (int, []orphanFailure) {
	ids := make([]string, len(batch))
	for i, it := range batch {
		ids[i] = it.ID
	}
	if err := client.DeleteItems(ctx, ids); err == nil {
		return len(batch), nil
	}

	held, err := stillHeld(ctx, client, ids)
	if err != nil {
		failed := make([]orphanFailure, 0, len(batch))
		for _, it := range batch {
			failed = append(failed, orphanFailure{ID: it.ID, Path: it.Path, Error: "the batch failed and what it left could not be read: " + err.Error()})
		}

		return 0, failed
	}

	gone := 0
	var failed []orphanFailure
	for _, it := range batch {
		if !held[it.ID] {
			gone++

			continue
		}
		if err := client.DeleteItem(ctx, it.ID); err != nil {
			if still, serr := stillHeld(ctx, client, []string{it.ID}); serr == nil && !still[it.ID] {
				gone++

				continue
			}
			failed = append(failed, orphanFailure{ID: it.ID, Path: it.Path, Error: err.Error()})

			continue
		}
		gone++
	}

	return gone, failed
}

// settled reads back the items a delete would not take, and says how many
// are gone anyway and which are still there. A server that deletes a folder's
// items with it takes some of them after the failure.
func settled(ctx context.Context, client *embyfin.Client, failures []orphanFailure) (int, []orphanFailure) {
	gone := 0
	var left []orphanFailure
	for start := 0; start < len(failures); start += orphanBatch {
		batch := failures[start:min(start+orphanBatch, len(failures))]
		ids := make([]string, len(batch))
		for i, f := range batch {
			ids[i] = f.ID
		}
		held, err := stillHeld(ctx, client, ids)
		if err != nil {
			left = append(left, batch...)

			continue
		}
		for _, f := range batch {
			if held[f.ID] {
				left = append(left, f)

				continue
			}
			gone++
		}
	}

	return gone, left
}

// depth is how many folders deep a path is.
func depth(p string) int { return strings.Count(trimSep(p), "/") + strings.Count(trimSep(p), `\`) }

func registerOrphanTools(r *registry) {
	client := r.client

	type orphanGroup struct {
		Folder   string         `json:"folder"         jsonschema:"the highest folder above these items that holds no library folder: where a library's folder was"`
		OnServer string         `json:"on_server"      jsonschema:"missing: the server cannot find the folder, so these are leftovers item_orphans_delete removes; present: the files are still there, held outside every library, and item_orphans_delete refuses them; unknown: the check failed, see note"`
		Items    int            `json:"items"`
		ByType   map[string]int `json:"by_type"        jsonschema:"how many of each kind of item"`
		Examples []orphanRow    `json:"examples"       jsonschema:"the first few, by path"`
		Note     string         `json:"note,omitempty"`
	}
	type orphansIn struct {
		Limit int `json:"limit,omitempty" jsonschema:"maximum folders, default 50"`
	}
	type orphansOut struct {
		Scanned int           `json:"items_scanned"`
		Found   int           `json:"total_orphans" jsonschema:"items outside every library, under every folder"`
		Folders []orphanGroup `json:"folders"       jsonschema:"one row per folder, most items first; capped at limit"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_orphans",
		Description: "Find items the server still holds under a folder no library covers: what a renamed or removed library folder leaves behind. " +
			"No library lists them and no scan revisits them, but every sweep of the server counts them. " +
			"Grouped by the folder they were under, each saying whether the server can still see it; item_orphans_delete removes the items under a folder it cannot.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in orphansIn) (*mcp.CallToolResult, orphansOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		libs, err := libraryPaths(ctx, client)
		if err != nil {
			return nil, orphansOut{}, err
		}
		orphans, scanned, err := sweepOrphans(ctx, client, libs, "")
		if err != nil {
			return nil, orphansOut{}, err
		}

		byFolder := map[string][]embyfin.Item{}
		for _, it := range orphans {
			folder := orphanFolder(it.Path, libs)
			byFolder[folder] = append(byFolder[folder], it)
		}
		folders := slices.SortedFunc(maps.Keys(byFolder), func(a, b string) int {
			return cmp.Or(cmp.Compare(len(byFolder[b]), len(byFolder[a])), strings.Compare(a, b))
		})

		out := orphansOut{Scanned: scanned, Found: len(orphans), Folders: []orphanGroup{}}
		for _, folder := range folders[:min(len(folders), limit)] {
			items := byFolder[folder]
			group := orphanGroup{Folder: folder, Items: len(items), ByType: map[string]int{}, Examples: orphanRows(items, 5)}
			for _, it := range items {
				group.ByType[it.Type]++
			}
			group.OnServer, group.Note = folderState(ctx, client, folder)
			out.Folders = append(out.Folders, group)
		}

		return nil, out, nil
	})

	type deleteIn struct {
		Folder  string `json:"folder"            jsonschema:"the folder whose leftovers to delete, as audit_orphans reports it; the server must not be able to see it"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"true to delete; without it the call only reports what it would delete"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"most items to delete in this call, default 2000, at most 10000; remaining says how many are left"`
	}
	type deleteOut struct {
		Folder    string          `json:"folder"`
		Scanned   int             `json:"items_scanned"`
		Found     int             `json:"found"             jsonschema:"items under the folder and outside every library, before this call"`
		ByType    map[string]int  `json:"by_type"`
		Deleted   int             `json:"deleted"           jsonschema:"items this call deleted; 0 without confirm"`
		Remaining int             `json:"remaining"         jsonschema:"items still under the folder: call again to go on"`
		Examples  []orphanRow     `json:"examples"          jsonschema:"the first few, by path"`
		Failed    []orphanFailure `json:"failed,omitempty"  jsonschema:"items the server would not delete, the first 20"`
		Stopped   string          `json:"stopped,omitempty" jsonschema:"why deleting stopped before it reached limit"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name: "item_orphans_delete",
		Description: "PERMANENTLY delete the items the server still holds under a folder no library covers and the server can no longer see: what a renamed or removed library folder leaves behind (audit_orphans lists the folders). " +
			"Refuses a folder inside a library's folder or holding one, and a folder the server can still see, because deleting an item deletes its file; the folder is checked again before every batch. " +
			"Without confirm=true it only reports what it would delete. Deletes at most limit items a call, deepest first; remaining says how many are left.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		folder := trimSep(strings.TrimSpace(in.Folder))
		switch {
		case folder == "" || !onDisk(folder):
			return nil, deleteOut{}, errors.New("folder must be a full path on the server, as audit_orphans reports it")
		case hasDots(folder):
			return nil, deleteOut{}, fmt.Errorf("folder %s steps through . or ..: name it as audit_orphans reports it", folder)
		case isTop(folder):
			return nil, deleteOut{}, fmt.Errorf("refusing %s: it is the top of a filesystem", folder)
		case in.Limit > orphanDeleteMax:
			return nil, deleteOut{}, fmt.Errorf("limit %d is more than the %d one call deletes: call again for the rest", in.Limit, orphanDeleteMax)
		}
		limit := in.Limit
		if limit <= 0 {
			limit = orphanDeleteDefault
		}

		libs, err := libraryPaths(ctx, client)
		if err != nil {
			return nil, deleteOut{}, err
		}
		if l, inside := inLibrary(folder, libs); inside {
			return nil, deleteOut{}, fmt.Errorf("%s is inside the %s library's folder %s: only a folder outside every library holds orphans", folder, l.library, l.path)
		}
		if l, holds := holdsLibrary(folder, libs); holds {
			return nil, deleteOut{}, fmt.Errorf("%s holds the %s library's folder %s: name the folder the orphans are under, as audit_orphans reports it", folder, l.library, l.path)
		}
		if err := refuseVisible(ctx, client, folder); err != nil {
			return nil, deleteOut{}, err
		}

		orphans, scanned, err := sweepOrphans(ctx, client, libs, folder)
		if err != nil {
			return nil, deleteOut{}, err
		}
		out := deleteOut{Folder: folder, Scanned: scanned, Found: len(orphans), ByType: map[string]int{}, Examples: orphanRows(orphans, 10)}
		for _, it := range orphans {
			out.ByType[it.Type]++
		}
		if !in.Confirm {
			out.Remaining = out.Found

			return nil, out, nil
		}

		// deepest first: an item goes before the folder holding it, so no
		// delete depends on a server taking a folder's items with it
		var failures []orphanFailure
		targets := slices.SortedFunc(slices.Values(orphans), func(a, b embyfin.Item) int {
			return cmp.Or(cmp.Compare(depth(b.Path), depth(a.Path)), strings.Compare(a.Path, b.Path), strings.Compare(a.ID, b.ID))
		})
		targets = targets[:min(len(targets), limit)]
		for start := 0; start < len(targets); start += orphanBatch {
			if ctx.Err() != nil {
				out.Stopped = "the call was cancelled"

				break
			}
			// checked again before every batch: a folder that comes back, a
			// share remounted, holds real files again
			if err := refuseVisible(ctx, client, folder); err != nil {
				out.Stopped = err.Error()

				break
			}
			deleted, failed := deleteOrphanBatch(ctx, client, targets[start:min(start+orphanBatch, len(targets))])
			out.Deleted += deleted
			failures = append(failures, failed...)
		}
		// a failure can be undone later in the same run: an item the server
		// would not delete on its own goes when the folder holding it does,
		// so what failed is read back before any of it is reported
		gone, failures := settled(ctx, client, failures)
		out.Deleted += gone
		out.Failed = append(out.Failed, failures[:min(len(failures), 20)]...)
		out.Remaining = out.Found - out.Deleted

		return nil, out, nil
	})
}
