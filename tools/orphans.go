package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/katbyte/embyfin-mcp/lib/mediapath"
	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
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

// findOrphans is the sweep audit_orphans counts: every item outside every
// library folder, the library folders it was read against, and what the
// sweep saw. audit_all reports its count without placing the items in
// folders, which asks the server about each folder.
func findOrphans(ctx context.Context, client *embyfin.Client) ([]embyfin.LibraryPath, []embyfin.Item, embyfin.ReadResult, error) {
	libs, err := client.LibraryPaths(ctx)
	if err != nil {
		return nil, nil, embyfin.ReadResult{}, err
	}
	orphans, swept, err := sweepOrphans(ctx, client, libs, "", embyfin.ToAnswer)
	if err != nil {
		return libs, orphans, swept, err
	}
	orphans, err = dropCaseTwins(ctx, client, orphans, libs)

	return libs, orphans, swept, err
}

// dropCaseTwins takes out of the orphans the items under a library's folder
// spelled with other case, when the server's disk ignores case (Windows, and
// macOS by default): there the two spellings are one folder, and a library
// added as D:\Movies whose items the server stores under D:\movies read as
// every film in it orphaned. The paths are compared as written everywhere
// else, so this asks the disk: whether it finds the library's folder spelled
// with every letter's case turned over. On a disk that tells case apart the
// two are different folders, and the items stay orphans.
func dropCaseTwins(ctx context.Context, client *embyfin.Client, orphans []embyfin.Item, libs []embyfin.LibraryPath) ([]embyfin.Item, error) {
	folds := map[string]bool{} // library folder -> whether the disk ignores case there
	kept := make([]embyfin.Item, 0, len(orphans))
	for j := range orphans {
		it := &orphans[j]
		i := slices.IndexFunc(libs, func(l embyfin.LibraryPath) bool {
			return mediapath.Within(strings.ToLower(it.Path), strings.ToLower(l.Path))
		})
		if i < 0 {
			kept = append(kept, *it)

			continue
		}
		folder := libs[i].Path
		ignores, asked := folds[folder]
		if !asked {
			turned := turnCase(folder)
			there, err := client.PathExists(ctx, turned)
			if err != nil {
				return nil, fmt.Errorf("asking the server whether its disk ignores case (whether it finds %s as %s): %w", folder, turned, err)
			}
			ignores = there && turned != folder
			folds[folder] = ignores
		}
		if !ignores {
			kept = append(kept, *it)
		}
	}

	return kept, nil
}

// turnCase is a path with every letter's case turned over.
func turnCase(p string) string {
	return strings.Map(func(r rune) rune {
		if u := unicode.ToUpper(r); u != r {
			return u
		}

		return unicode.ToLower(r)
	}, p)
}

func auditOrphans(ctx context.Context, client *embyfin.Client, in orphansIn) (orphansOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	libs, orphans, swept, err := findOrphans(ctx, client)
	if err != nil {
		return orphansOut{}, err
	}

	places := placeOrphans(ctx, client, orphans, libs)
	folders := slices.SortedFunc(maps.Keys(places), func(a, b string) int {
		return cmp.Or(cmp.Compare(len(places[b].items), len(places[a].items)), strings.Compare(a, b))
	})

	out := orphansOut{Scanned: swept.Read, Found: len(orphans), Folders: []orphanGroup{}, Note: swept.Changed()}
	for _, folder := range folders[:min(len(folders), limit)] {
		p := places[folder]
		group := orphanGroup{Folder: folder, OnServer: p.state, Note: p.note, Items: len(p.items), ByType: map[string]int{}, Examples: orphanRows(p.items, 5)}
		for i := range p.items {
			it := &p.items[i]
			group.ByType[it.Type]++
		}
		out.Folders = append(out.Folders, group)
	}

	return out, nil
}

// orphanPlace is a folder orphans are reported under: the items, and
// whether the server can see the folder.
type orphanPlace struct {
	items       []embyfin.Item
	state, note string
}

// placeOrphans decides the folder each orphan is reported under: for
// leftovers, the deepest folder holding them that the server can no longer
// see, which is the folder item_orphans_delete takes; for files still on
// disk, the deepest folder holding them.
//
// It starts from the orphans under each highest folder that holds no
// library (orphanFolder) and asks the server about the deepest folder
// holding them all. Gone, that folder is the answer: everything under it
// went with it. Still there, the orphans are split by the entry beneath it
// they sit under and each part is asked about the same way. That is what
// keeps two unrelated removed trees apart, and reports a disk that was
// mounted at /mnt/old at the folder that was on it rather than at /mnt,
// which the server can see: climbing to just below the top named /mnt,
// called it present, and item_orphans_delete refused the folder the audit
// had named. An orphan sitting directly in a folder the server can see is
// on disk, and those are reported together under the deepest folder holding
// them. A folder the server could not be asked about is reported as it
// stands, unknown, and nothing below it is guessed at. Each folder is asked
// about once.
func placeOrphans(ctx context.Context, client *embyfin.Client, orphans []embyfin.Item, libs []embyfin.LibraryPath) map[string]*orphanPlace {
	type answer struct{ state, note string }
	asked := map[string]answer{}
	ask := func(folder string) answer {
		a, ok := asked[folder]
		if !ok {
			a.state, a.note = folderState(ctx, client, folder)
			asked[folder] = a
		}

		return a
	}
	places := map[string]*orphanPlace{}
	put := func(folder string, items []embyfin.Item) {
		p := places[folder]
		if p == nil {
			a := ask(folder)
			p = &orphanPlace{state: a.state, note: a.note}
			places[folder] = p
		}
		p.items = append(p.items, items...)
	}

	var split func(folder string, items []embyfin.Item, onDisk *[]embyfin.Item)
	split = func(folder string, items []embyfin.Item, onDisk *[]embyfin.Item) {
		if ask(folder).state != folderPresent {
			put(folder, items)

			return
		}
		below := map[string][]embyfin.Item{}
		for i := range items {
			it := &items[i]
			child := mediapath.ChildOf(folder, it.Path)
			if child == "" {
				// the folder's own item: a Folder or a Series whose folder
				// the server can see
				*onDisk = append(*onDisk, *it)

				continue
			}
			below[child] = append(below[child], *it)
		}
		for _, child := range slices.Sorted(maps.Keys(below)) {
			part := below[child]
			// a file sitting in this folder, or copies of one: the folder
			// holding it is this one, which the server can see
			if !slices.ContainsFunc(part, func(it embyfin.Item) bool { return mediapath.Trim(it.Path) != child }) {
				*onDisk = append(*onDisk, part...)

				continue
			}
			split(holdingFolder(part, child), part, onDisk)
		}
	}

	byRegion := map[string][]embyfin.Item{}
	for i := range orphans {
		it := &orphans[i]
		region := orphanFolder(it.Path, libs)
		byRegion[region] = append(byRegion[region], *it)
	}
	for _, region := range slices.Sorted(maps.Keys(byRegion)) {
		items := byRegion[region]
		var onDisk []embyfin.Item
		split(holdingFolder(items, region), items, &onDisk)
		if len(onDisk) > 0 {
			put(holdingFolder(onDisk, region), onDisk)
		}
	}

	return places
}

// holdingFolder is the deepest folder every item is at or under: the path
// their paths share, or, when they are all one path (a single file, or
// copies of one), the folder holding it. It never climbs above bound, which
// holds them all and no library.
func holdingFolder(items []embyfin.Item, bound string) string {
	first := mediapath.Trim(items[0].Path)
	common, same := first, true
	for i := 1; i < len(items); i++ {
		p := mediapath.Trim(items[i].Path)
		same = same && p == first
		for common != "" && !mediapath.Within(p, common) {
			common = mediapath.Dir(common)
		}
	}
	if same {
		if up := mediapath.Dir(common); up != "" && mediapath.Within(up, bound) {
			common = up
		}
	}

	return common
}

// inLibrary is the library folder path is inside, if any.
func inLibrary(path string, libs []embyfin.LibraryPath) (embyfin.LibraryPath, bool) {
	for _, l := range libs {
		if mediapath.Within(path, l.Path) {
			return l, true
		}
	}

	return embyfin.LibraryPath{}, false
}

// holdsLibrary is a library folder inside folder, if any.
func holdsLibrary(folder string, libs []embyfin.LibraryPath) (embyfin.LibraryPath, bool) {
	for _, l := range libs {
		if mediapath.Within(l.Path, folder) {
			return l, true
		}
	}

	return embyfin.LibraryPath{}, false
}

// orphanFolder is the highest folder above an orphan that holds no library
// folder, which is as high as the removed library's folder can have been:
// the one above holds libraries still in use, and naming it would be naming
// them. audit_orphans reports each orphan at or below it (placeOrphans).
func orphanFolder(path string, libs []embyfin.LibraryPath) string {
	folder := mediapath.Trim(path)
	for {
		up := mediapath.Dir(folder)
		if up == "" || mediapath.IsTop(up) {
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
// it - and says what the sweep saw, read for purpose. It names no library and
// no user, because an orphan belongs to neither: only a sweep of everything
// the server holds reaches it.
func sweepOrphans(ctx context.Context, client *embyfin.Client, libs []embyfin.LibraryPath, root string, purpose embyfin.ReadPurpose) ([]embyfin.Item, embyfin.ReadResult, error) {
	var found []embyfin.Item
	swept, err := sweepAll(ctx, client, embyfin.SearchOptions{IncludeItemTypes: orphanTypes, Fields: "Path,ParentId"}, purpose, func(items []embyfin.Item) {
		for i := range items {
			it := items[i]
			if !mediapath.OnDisk(it.Path) {
				continue
			}
			if _, in := inLibrary(it.Path, libs); in {
				continue
			}
			if root != "" && !mediapath.Within(it.Path, root) {
				continue
			}
			found = append(found, it)
		}
	})

	return found, swept, err
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
	for i := range min(n, len(sorted)) {
		it := &sorted[i]
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
	for i := range items {
		it := &items[i]
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
func deleteOrphanBatch(ctx context.Context, client *embyfin.Client, batch []embyfin.Item) (int, []orphanFailure, error) {
	ids := make([]string, len(batch))
	for i := range batch {
		ids[i] = batch[i].ID
	}
	batchErr := client.DeleteItems(ctx, ids)
	if batchErr == nil {
		return len(batch), nil, nil
	}

	held, err := stillHeld(ctx, client, ids)
	if err != nil {
		failed := make([]orphanFailure, 0, len(batch))
		for i := range batch {
			it := &batch[i]
			failed = append(failed, orphanFailure{ID: it.ID, Path: it.Path, Error: "the batch failed (" + batchErr.Error() + ") and what it left could not be read: " + err.Error()})
		}

		return 0, failed, batchErr
	}

	gone := 0
	var failed []orphanFailure
	for i := range batch {
		it := &batch[i]
		if !held[it.ID] {
			gone++

			continue
		}
		if err := client.DeleteItem(ctx, it.ID); err != nil {
			still, serr := stillHeld(ctx, client, []string{it.ID})
			if serr == nil && !still[it.ID] {
				gone++

				continue
			}
			msg := err.Error()
			if serr != nil {
				msg += "; and reading whether it went anyway failed: " + serr.Error()
			}
			failed = append(failed, orphanFailure{ID: it.ID, Path: it.Path, Error: msg})

			continue
		}
		gone++
	}

	return gone, failed, batchErr
}

// settled reads back the items a delete would not take, and says how many
// are gone anyway and which are still there. A server that deletes a folder's
// items with it takes some of them after the failure. A batch whose read
// fails is left as it was reported, saying so.
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
			for _, f := range batch {
				f.Error += "; and reading whether it went since failed: " + err.Error()
				left = append(left, f)
			}

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

type orphanGroup struct {
	Folder   string         `json:"folder"         jsonschema:"the deepest folder holding these items: for leftovers the one the server can no longer see, which item_orphans_delete takes as named; for files still on disk the folder they are in"`
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
	Found   int           `json:"total_findings" jsonschema:"items outside every library, under every folder"`
	Folders []orphanGroup `json:"folders"        jsonschema:"one row per folder, most items first; capped at limit"`
	Note    string        `json:"note,omitempty" jsonschema:"set when the library was seen to change while it was read: items added or removed meanwhile may be missing, or listed though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read"`
}

func registerOrphanTools(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_orphans",
		Description: "Find items the server still holds under a folder no library covers: what a renamed or removed library folder leaves behind. " +
			"No library lists them and no scan revisits them, but every sweep of the server counts them. " +
			"A folder spelled like a library's folder but for its case is that library's when the server's disk ignores case (the disk is asked), and another folder when it does not. " +
			"Grouped by the folder they were under, each saying whether the server can still see it; item_orphans_delete removes the items under a folder it cannot.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in orphansIn) (*mcp.CallToolResult, orphansOut, error) {
		out, err := auditOrphans(ctx, client, in)

		return nil, out, err
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
		Note      string          `json:"note,omitempty"    jsonschema:"set when the server was seen to change while it was read for the items to delete: items added meanwhile may be missing, so remaining may be low, and items removed may be listed though gone, and fail to delete. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read. A read that cannot be sure fails instead, and nothing is changed"`
		// what went with them, and what the server said on the way
		ListsAffected []listRef `json:"lists_affected"         jsonschema:"the playlists and collections holding items under the folder, which lose them when they are deleted"`
		BatchErrors   []string  `json:"batch_errors,omitempty" jsonschema:"what the server answered a batch it would not delete whole, each item of which was then deleted on its own; the first 10"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name: "item_orphans_delete",
		Description: "PERMANENTLY delete the items the server still holds under a folder no library covers and the server can no longer see: what a renamed or removed library folder leaves behind (audit_orphans lists the folders). " +
			"Refuses a folder inside a library's folder or holding one, and a folder the server can still see, because deleting an item deletes its file; the folder is checked again before every batch. A disk not mounted or a network share offline reads as gone just the same: its items are deleted, and when it is back its files are new to the server. " +
			"What goes with the items: " + goneWithItems + ". lists_affected names the playlists and collections. " +
			"Without confirm=true it only reports what it would delete, and the lists that hold them. Deletes at most limit items a call, deepest first; remaining says how many are left.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		folder := mediapath.Trim(strings.TrimSpace(in.Folder))
		switch {
		case folder == "" || !mediapath.OnDisk(folder):
			return nil, deleteOut{}, errors.New("folder must be a full path on the server, as audit_orphans reports it")
		case mediapath.HasDots(folder):
			return nil, deleteOut{}, fmt.Errorf("folder %s steps through . or ..: name it as audit_orphans reports it", folder)
		case mediapath.IsTop(folder):
			return nil, deleteOut{}, fmt.Errorf("refusing %s: it is the top of a filesystem", folder)
		case in.Limit > orphanDeleteMax:
			return nil, deleteOut{}, fmt.Errorf("limit %d is more than the %d one call deletes: call again for the rest", in.Limit, orphanDeleteMax)
		}
		limit := in.Limit
		if limit <= 0 {
			limit = orphanDeleteDefault
		}

		libs, err := client.LibraryPaths(ctx)
		if err != nil {
			return nil, deleteOut{}, err
		}
		if l, inside := inLibrary(folder, libs); inside {
			return nil, deleteOut{}, fmt.Errorf("%s is inside the %s library's folder %s: only a folder outside every library holds orphans", folder, l.Library, l.Path)
		}
		if l, holds := holdsLibrary(folder, libs); holds {
			return nil, deleteOut{}, fmt.Errorf("%s holds the %s library's folder %s: name the folder the orphans are under, as audit_orphans reports it", folder, l.Library, l.Path)
		}
		if err := refuseVisible(ctx, client, folder); err != nil {
			return nil, deleteOut{}, err
		}

		// what to delete: a sweep that cannot be sure of it fails, and
		// nothing is deleted
		orphans, swept, err := sweepOrphans(ctx, client, libs, folder, embyfin.ToAct)
		if err != nil {
			return nil, deleteOut{}, err
		}
		out := deleteOut{Folder: folder, Scanned: swept.Read, Found: len(orphans), ByType: map[string]int{}, Examples: orphanRows(orphans, 10), Note: swept.Changed()}
		for i := range orphans {
			it := &orphans[i]
			out.ByType[it.Type]++
		}
		lists, err := readMemberships(ctx, client)
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("could not read the playlists and collections the items under %s would leave, so nothing was deleted: %w", folder, err)
		}
		out.ListsAffected = lists.holding(idsOf(orphans))
		if !in.Confirm {
			out.Remaining = out.Found

			return nil, out, nil
		}

		// deepest first: an item goes before the folder holding it, so no
		// delete depends on a server taking a folder's items with it
		var failures []orphanFailure
		targets := slices.SortedFunc(slices.Values(orphans), func(a, b embyfin.Item) int {
			return cmp.Or(cmp.Compare(mediapath.Depth(b.Path), mediapath.Depth(a.Path)), strings.Compare(a.Path, b.Path), strings.Compare(a.ID, b.ID))
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
			deleted, failed, batchErr := deleteOrphanBatch(ctx, client, targets[start:min(start+orphanBatch, len(targets))])
			out.Deleted += deleted
			failures = append(failures, failed...)
			if batchErr != nil && len(out.BatchErrors) < 10 {
				out.BatchErrors = append(out.BatchErrors, batchErr.Error())
			}
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
