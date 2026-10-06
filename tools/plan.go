package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/mediapath"
	"github.com/katbyte/embyfin-mcp/lib/naming"
	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Reading the destinations before anything is written to them.
//
// A bulk import that does not ask what is already at its paths can overwrite
// one series' episodes in place with files of another series of the same
// name, and a rename pass can write one episode over another. None of it is
// visible afterwards: an overwritten path keeps the item's id and its
// date_created, so every "what was added" view is blind to it, and the
// originals are gone.
//
// This asks first. It writes nothing; it says what is at each path now,
// which series each path would join, and which entries collide with each
// other.
//
// "What is at a path" is two questions, and the library is only one of them.
// A file the previous batch wrote, or any file the server has not scanned
// yet, is on the disk and in no item: asked of the library alone, its path
// read as free, and the next write replaced it. So the server's disk is
// asked too, as the server's own process sees it.

// planBatchMax is how many destinations one call reads. The work is bounded
// by the number of series they fall under rather than by the count, but an
// unbounded batch is still a request that never returns.
const planBatchMax = 500

// planEntry is one file a caller intends to write.
type planEntry struct {
	Path    string `json:"path"              jsonschema:"the full destination path the file would be written to"`
	Size    *int64 `json:"size,omitempty"    jsonschema:"the incoming file's size in bytes, to compare against what is there; 0 is an empty file, and is compared too"`
	Series  string `json:"series,omitempty"  jsonschema:"the series the caller believes this is, checked against the series whose folder the path falls under"`
	Season  int    `json:"season,omitempty"`
	Episode int    `json:"episode,omitempty"`
}

// planCurrent is what the library holds at that path now.
type planCurrent struct {
	ItemID       string   `json:"item_id"`
	Name         string   `json:"name,omitempty"`
	Series       string   `json:"series,omitempty"`
	Season       *int     `json:"season,omitempty"        jsonschema:"on an episode only, 0 for the specials"`
	Episode      *int     `json:"episode,omitempty"       jsonschema:"on an episode the server holds a number for"`
	Size         int64    `json:"size,omitempty"          jsonschema:"the size of the file at this path in bytes: when the item holds several versions, the one here rather than the best of them"`
	SizeRatio    *float64 `json:"size_ratio,omitempty"    jsonschema:"the incoming size over this one, whenever a size was given and the server knows this one's: below 1 means the write would replace a bigger file with a smaller, and 0 an empty or all but empty incoming file"`
	RuntimeS     int      `json:"runtime_s,omitempty"`
	Height       int      `json:"height,omitempty"`
	VideoCodec   string   `json:"video_codec,omitempty"`
	Bitrate      int64    `json:"bitrate,omitempty"`
	DateCreated  string   `json:"date_created,omitempty"`
	FileModified string   `json:"file_modified,omitempty" jsonschema:"Emby only"`
}

// planJoin is the series a path would become an episode of.
type planJoin struct {
	SeriesID   string   `json:"series_id"`
	SeriesName string   `json:"series_name"`
	SeriesYear int      `json:"series_year,omitempty"`
	SeriesPath string   `json:"series_path"`
	Claimed    string   `json:"claimed_series,omitempty"   jsonschema:"the series the caller said this was"`
	ClaimScore *float64 `json:"claim_similarity,omitempty" jsonschema:"given whenever a series was claimed, 0 to 1 between the claimed series and the one whose folder this path falls under, scored as show_resolve scores a name: its title and, when it gives one, its year. Low means the path is under a different show's folder than the caller thinks - which is how one series' episodes get written over another's of the same name; 0 is a different show altogether"`
}

// planRow is the answer for one destination.
//
// Checked comes first because it says what exists rests on. A path the
// server could not be asked about used to read exists: false - on Jellyfin,
// every path outside a series folder, films included - and false is the one
// answer that says "write here". The disk answers for every path now, and
// checked says whether the library could be asked as well.
type planRow struct {
	Path      string       `json:"path"`
	Checked   bool         `json:"checked"                jsonschema:"whether the library could be asked which item is at this path. When false (Jellyfin cannot look a path up outside a series folder) in_library is null, exists is true only when the server's disk has a file here, and the note says why"`
	Exists    *bool        `json:"exists"                 jsonschema:"true when a file is at exactly this path, in the library (an item's own path or one of its versions) or on the server's disk though the library has not scanned it: writing there REPLACES it, and an item there keeps its id and date_created, so nothing afterwards will show what happened. false when the library holds nothing at this path and the server's disk has nothing there. null when either could not say (checked false, or on_disk null): not known is not free"`
	InLibrary *bool        `json:"in_library"             jsonschema:"whether the library holds a file at this path. false while exists is true is a file on the disk that no item holds - one a previous batch wrote, or any written since the last scan - and it is replaced all the same. null when the library could not be asked (checked false)"`
	OnDisk    *bool        `json:"on_disk"                jsonschema:"whether the server's disk has a file or folder at this path, as the server's own process sees it. A folder the server cannot list is settled by the nearest folder above it that it can: not listed there, it is not there. null when the disk could not say - a folder the server lists but cannot read, or lists as empty (as one it cannot read can) - and the note says which"`
	Current   *planCurrent `json:"current"                jsonschema:"the file at this path now, as the library holds it, or null when no item holds one here or which does is not known"`
	WouldJoin *planJoin    `json:"would_join"             jsonschema:"the series whose folder this path falls under, or null when no series folder holds it"`
	Duplicate []string     `json:"duplicate_of,omitempty" jsonschema:"other paths in this same batch that would land on the same file, or claim the same season and episode. Two entries written to one path leave only the last of them"`
	Note      string       `json:"note,omitempty"         jsonschema:"what could not be established, and why"`
}

func registerPlanTools(r *registry) {
	client := r.client

	type planIn struct {
		Entries []planEntry `json:"entries"           jsonschema:"the destinations to check, at most 500 per call"`
		Library string      `json:"library,omitempty" jsonschema:"restrict the series lookup to one library by name or id"`
	}
	type planOut struct {
		Entries     []planRow `json:"entries"      jsonschema:"one row per entry, in the order given"`
		Existing    int       `json:"existing"     jsonschema:"how many destinations already hold a file, in the library or on the server's disk"`
		NotScanned  int       `json:"not_scanned"  jsonschema:"how many of those are on the server's disk and in no item: files the library has not scanned yet"`
		Unchecked   int       `json:"unchecked"    jsonschema:"how many destinations the library could not be asked about: exists is true on those when the server's disk has a file there, and null otherwise"`
		DiskUnknown int       `json:"disk_unknown" jsonschema:"how many destinations the server's disk could not say about - a folder it lists but cannot read, or lists as empty: on_disk is null on those, and exists null unless the library holds a file there"`
		Duplicates  int       `json:"duplicates"   jsonschema:"how many entries collide with another entry in the same batch"`
		Unplaced    int       `json:"unplaced"     jsonschema:"how many paths fall under no series folder this server knows"`
	}

	add(r, readTool, &mcp.Tool{
		Name: "plan_check",
		Description: "Before writing files into the library: what is at each destination path now, which series each path would join, and which entries collide with each other. Reads only, writes nothing. " +
			"exists true means writing there replaces a file: one the library holds (the item keeps its id and date_created, so no later 'what was added' read can show it happened) or one on the server's disk that the library has not scanned yet - a previous batch's, say - which in_library false and on_disk true say. " +
			"The disk is read as the server's own process sees it: a folder it lists but cannot read, or lists as empty, leaves on_disk null. checked false means the library could not be asked which item is there (Jellyfin cannot look a path up outside a series folder). exists is null whenever either could not say - not known, which is not free. " +
			"The library's series folders are read afresh on every call. claim_similarity low means the path falls under a different show's folder than the caller thinks. At most 500 paths a call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in planIn) (*mcp.CallToolResult, planOut, error) {
		if len(in.Entries) == 0 {
			return nil, planOut{}, errors.New("at least one entry is required")
		}
		if len(in.Entries) > planBatchMax {
			return nil, planOut{}, fmt.Errorf("%d entries is more than the %d this reads in one call: ask in pages", len(in.Entries), planBatchMax)
		}
		// a path the server cannot place is one nothing can be said about,
		// and "nothing there" would be the wrong thing to say
		for i, entry := range in.Entries {
			if !mediapath.OnDisk(strings.TrimSpace(entry.Path)) {
				return nil, planOut{}, fmt.Errorf("entry %d: %q is not a full path: give each destination as the server sees it, from the top of its disk", i+1, entry.Path)
			}
		}

		folder, err := client.ResolveLibrary(ctx, in.Library)
		if err != nil {
			return nil, planOut{}, err
		}
		parent := ""
		if folder != nil {
			parent = folder.ItemID
		}
		// afresh: "no series folder holds this path" from a read made before
		// a scan or a rename in the web client is a wrong answer about where
		// a write lands
		index, err := r.seriesCache().fresh(ctx, r.client, parent)
		if err != nil {
			return nil, planOut{}, err
		}

		// which series' folder each path falls under: the longest folder that
		// is a prefix of it, so a show inside another show's folder still
		// lands on the right one
		joins := make([]*embyfin.Item, len(in.Entries))
		bySeries := map[string][]int{}
		for i, entry := range in.Entries {
			path := mediapath.Clean(entry.Path)
			best := -1
			for j := range index.items {
				folder := index.items[j].Path
				if !mediapath.Inside(path, folder) {
					continue
				}
				if best < 0 || len(folder) > len(index.items[best].Path) {
					best = j
				}
			}
			if best >= 0 {
				joins[i] = &index.items[best]
				bySeries[index.items[best].ID] = append(bySeries[index.items[best].ID], i)
			}
		}

		// one read per series the batch touches, rather than one per file.
		// Every version of an episode is indexed, not only the item's own
		// path: a server that finds two files of one episode in a folder
		// merges them into one item, and a path taken by the second of them
		// read as free and would have been written over
		held := map[string]*embyfin.Item{}
		for seriesID := range bySeries {
			episodes, eerr := client.Episodes(ctx, seriesID, embyfin.EpisodeOptions{Fields: planFields})
			if eerr != nil {
				return nil, planOut{}, eerr
			}
			for i := range episodes {
				for _, path := range itemPaths(&episodes[i]) {
					held[path] = &episodes[i]
				}
			}
		}

		// a path under no series folder is still worth answering for: a film,
		// or a show the server has not been told about
		unknown, err := lookUpOutsideSeries(ctx, client, in.Entries, joins, held)
		if err != nil {
			return nil, planOut{}, err
		}

		// and the disk, which holds what no scan has reached yet
		paths := make([]string, 0, len(in.Entries))
		for _, entry := range in.Entries {
			paths = append(paths, mediapath.Clean(entry.Path))
		}
		// what the library holds: its files, and its series folders
		library := slices.Collect(maps.Keys(held))
		for _, series := range joins {
			if series != nil && series.Path != "" {
				library = append(library, series.Path)
			}
		}
		disk, err := onServerDisk(ctx, client, paths, library)
		if err != nil {
			return nil, planOut{}, err
		}
		// a disk that ignores case holds some files under another spelling:
		// outside a series folder, whose episodes were all read, the library
		// is asked after that spelling too, or a file an item holds read as
		// one no item does
		var respelled []planEntry
		var respelledAt []int
		for i, p := range paths {
			if d := disk[p]; d.there && d.path != p && held[d.path] == nil && joins[i] == nil {
				respelled = append(respelled, planEntry{Path: d.path})
				respelledAt = append(respelledAt, i)
			}
		}
		if len(respelled) > 0 {
			stillUnknown, err := lookUpOutsideSeries(ctx, client, respelled, make([]*embyfin.Item, len(respelled)), held)
			if err != nil {
				return nil, planOut{}, err
			}
			for j, i := range respelledAt {
				if held[respelled[j].Path] != nil {
					// found under the disk's spelling: the library answered
					delete(unknown, i)
				} else if note, ok := stillUnknown[j]; ok {
					if _, already := unknown[i]; !already {
						unknown[i] = note
					}
				}
			}
		}

		// entries that would land on each other: the same path twice, or the
		// same episode claimed twice
		samePath := map[string][]int{}
		sameEpisode := map[string][]int{}
		for i, entry := range in.Entries {
			samePath[mediapath.Clean(entry.Path)] = append(samePath[mediapath.Clean(entry.Path)], i)
			if entry.Series != "" && entry.Episode > 0 {
				key := fmt.Sprintf("%s|s%02de%02d", strings.ToLower(entry.Series), entry.Season, entry.Episode)
				sameEpisode[key] = append(sameEpisode[key], i)
			}
		}

		out := planOut{Entries: make([]planRow, 0, len(in.Entries))}
		for i, entry := range in.Entries {
			path := mediapath.Clean(entry.Path)
			d := disk[path]
			row := planRow{Path: entry.Path, Checked: true, InLibrary: new(false)}
			if !d.unknown {
				row.OnDisk = new(d.there)
			}
			if note, ok := unknown[i]; ok {
				row.Checked, row.InLibrary, row.Note = false, nil, note
				out.Unchecked++
			}

			item, at := held[path], path
			if item == nil && d.there && d.path != path {
				// a disk that ignores case holds the file under another
				// spelling: the item holding that is the one replaced
				item, at = held[d.path], d.path
			}
			if item != nil {
				// the file at this path, not the best of the item's versions:
				// it is this one the write would replace
				q := qualityAt(item, at)
				row.InLibrary = new(true)
				current := planCurrent{
					ItemID: item.ID, Name: item.Name, Series: item.SeriesName,
					Season: seasonOf(item), Episode: item.IndexNumber,
					Size: q.Size, RuntimeS: int(item.RunTimeTicks / ticksPerSecond), Height: q.Height,
					VideoCodec: q.VideoCodec, Bitrate: q.Bitrate,
					DateCreated: item.DateCreated, FileModified: item.DateModified,
				}
				// every ratio worked out is given, 0 above all: an empty
				// incoming file, or one a hundredth the size, is the write
				// most worth stopping, and a 0 left out read as no size given
				if entry.Size != nil && *entry.Size >= 0 && q.Size > 0 {
					ratio := math.Round(float64(*entry.Size)/float64(q.Size)*100) / 100
					current.SizeRatio = &ratio
				}
				row.Current = &current
			}

			// a file is there when either holds one: the library, or the
			// disk it has not scanned. Nothing is there only when both say
			// so: a disk that could not say, or a library that could not be
			// asked (an item whose file is gone is still at its path), leaves
			// it not known
			inLibrary := row.InLibrary != nil && *row.InLibrary
			switch {
			case inLibrary || d.there:
				row.Exists = new(true)
				out.Existing++
			case !d.unknown && row.InLibrary != nil:
				row.Exists = new(false)
			}
			if d.unknown {
				out.DiskUnknown++
			}
			switch {
			case d.there && row.InLibrary != nil && !inLibrary:
				out.NotScanned++
				row.Note = joinNotes(row.Note, "the server's disk has a file here that no item holds - one written since the last scan, a previous batch's, say: writing here replaces it")
			case inLibrary && !d.there && !d.unknown:
				row.Note = joinNotes(row.Note, "the library holds an item at this path, but the server's disk has no file here: the item's file is gone, and a file written here becomes that item, keeping its id and date_created")
			}
			if d.folder {
				row.Note = joinNotes(row.Note, "a folder, not a file, is at this path on the server's disk")
			}
			row.Note = joinNotes(row.Note, d.note)

			if series := joins[i]; series != nil {
				join := planJoin{
					SeriesID: series.ID, SeriesName: series.Name,
					SeriesYear: series.ProductionYear, SeriesPath: series.Path,
				}
				if entry.Series != "" {
					// a score of 0 - another show altogether - is the one
					// most worth reading, so it is given like any other
					join.Claimed = entry.Series
					score := claimScore(entry.Series, series)
					join.ClaimScore = &score
				}
				row.WouldJoin = &join
			} else {
				out.Unplaced++
				row.Note = joinNotes(row.Note, "no series folder on this server holds this path: the file would land outside the library, or in a folder the server has not scanned")
			}

			for _, other := range append(slices.Clone(samePath[path]), sameEpisode[fmt.Sprintf("%s|s%02de%02d", strings.ToLower(entry.Series), entry.Season, entry.Episode)]...) {
				if other != i && !slices.Contains(row.Duplicate, in.Entries[other].Path) {
					row.Duplicate = append(row.Duplicate, in.Entries[other].Path)
				}
			}
			if len(row.Duplicate) > 0 {
				out.Duplicates++
			}

			out.Entries = append(out.Entries, row)
		}

		return nil, out, nil
	})
}

// planFields is what plan_check reads off an item: its paths, and the facts
// about the file at each.
const planFields = "Path,MediaSources,DateCreated,DateModified"

// planSearchMax is how many answers a title search on Jellyfin reads for the
// one at a path: a common title matches its sequels and namesakes too.
const planSearchMax = 200

// itemPaths is every path an item holds a file at: its own, and each of its
// versions'.
func itemPaths(it *embyfin.Item) []string {
	var out []string
	if it.Path != "" {
		out = append(out, mediapath.Clean(it.Path))
	}
	for i := range it.MediaSources {
		if p := it.MediaSources[i].Path; p != "" && !slices.Contains(out, mediapath.Clean(p)) {
			out = append(out, mediapath.Clean(p))
		}
	}

	return out
}

// lookUpOutsideSeries answers for the entries under no series folder, adding
// what it finds at them to held. It returns, by entry, the ones it could not
// answer for and why.
//
// Emby is asked by the path itself, which settles it. Jellyfin has no path
// filter at all, so it is asked for the title the path names and every
// answer's paths are compared: that finds a film or an episode filed under
// its own title, and nothing else, so finding nothing is not the same as
// nothing being there. What Jellyfin can settle is a path outside every
// library folder, because the server holds nothing there.
func lookUpOutsideSeries(ctx context.Context, client *embyfin.Client, entries []planEntry, joins []*embyfin.Item, held map[string]*embyfin.Item) (map[int]string, error) {
	unknown := map[int]string{}
	var locations []string
	for i, entry := range entries {
		path := mediapath.Clean(entry.Path)
		if joins[i] != nil || held[path] != nil {
			continue
		}

		opts := embyfin.SearchOptions{Path: entry.Path, Fields: planFields, Limit: 1}
		title := naming.ParseRelease(path).Title
		if client.Backend() != embyfin.Emby {
			opts = embyfin.SearchOptions{SearchTerm: title, Fields: planFields, Limit: planSearchMax}
		}
		items, _, err := client.Search(ctx, opts)
		if err != nil {
			return nil, err
		}
		// the returned item has to BE at that path: a server that ignores
		// the filter, or a search that matched a title, would otherwise have
		// us report the wrong file as the destination's contents
		if j := slices.IndexFunc(items, func(it embyfin.Item) bool { return slices.Contains(itemPaths(&it), path) }); j >= 0 {
			held[path] = &items[j]

			continue
		}
		if client.Backend() == embyfin.Emby {
			continue // asked by the path itself: nothing is there
		}

		if locations == nil {
			folders, ferr := client.VirtualFolders(ctx)
			if ferr != nil {
				return nil, ferr
			}
			locations = []string{}
			for f := range folders {
				locations = append(locations, folders[f].Locations...)
			}
		}
		inside := slices.ContainsFunc(locations, func(folder string) bool {
			return mediapath.Inside(path, folder)
		})
		if inside {
			unknown[i] = fmt.Sprintf("which item holds a file here is not known: Jellyfin cannot look an item up by its path, and a search for the title the path names (%q) found none at it - an item filed under another title would not be found - so exists is true only when the server's disk has a file here, and null otherwise", title)
		}
	}

	return unknown, nil
}

// diskEntry is what the server's disk holds at a path.
type diskEntry struct {
	there   bool   // a file or a folder is at the path
	folder  bool   // and it is a folder
	unknown bool   // the disk could not say: a folder the server lists but cannot read, or lists as empty
	path    string // the path as the disk spells it, when it is there
	note    string // what the disk said beside it, and what that means here
}

// onServerDisk reads what the server's disk holds at each path, as the
// server's own process sees it: one listing of each folder the paths are in,
// rather than a question a path, so a batch into one season costs one read.
//
// A name that differs only in case is settled by asking for the path itself:
// a disk that ignores case (Windows, and macOS by default) finds the file by
// the new spelling, and a write there replaces it; one that tells case apart
// does not, and the two are different files.
//
// A folder the server cannot list is not taken to be missing: the server
// answers the same for a folder its process may not read. The nearest folder
// above it that it can list settles it - one that does not list it means it
// is not there - up to the library's own folder, or the disk's root for a
// path outside every library. A folder listed as empty is not taken to be
// empty either, anywhere on that walk, since one the server cannot read -
// a share gone offline - lists so: a path in it is not known, unless the
// path check finds it. Nor is a folder the library holds items under
// (library, the paths the library is known to hold) taken to be gone when
// the disk does not list it: that is the server not seeing its own folder.
func onServerDisk(ctx context.Context, client *embyfin.Client, paths, library []string) (map[string]diskEntry, error) {
	type listing struct {
		entries []embyfin.FolderEntry
		found   bool
	}
	listed := map[string]listing{}
	list := func(folder string) (listing, error) {
		if l, ok := listed[folder]; ok {
			return l, nil
		}
		entries, found, err := client.ListFolder(ctx, folder)
		if err != nil {
			return listing{}, fmt.Errorf("reading the server's folder %s: %w", folder, err)
		}
		listed[folder] = listing{entries: entries, found: found}

		return listed[folder], nil
	}
	var locations []string // the libraries' folders, read when a walk needs them
	// absent says what a folder the server could not list is: not there,
	// when the nearest folder above that it can list does not list it, and
	// not known otherwise
	absent := func(folder string) (diskEntry, error) {
		if locations == nil {
			folders, err := client.VirtualFolders(ctx)
			if err != nil {
				return diskEntry{}, err
			}
			locations = []string{}
			for i := range folders {
				locations = append(locations, folders[i].Locations...)
			}
		}
		// the walk stops at the library's folder the path is in, or the root
		bound := ""
		for _, l := range locations {
			if mediapath.Within(folder, l) && len(mediapath.Trim(l)) > len(bound) {
				bound = mediapath.Trim(l)
			}
		}
		for child, parent := folder, mediapath.Dir(folder); ; child, parent = parent, mediapath.Dir(parent) {
			if child == bound || parent == "" {
				note := fmt.Sprintf("the server could not read %s, nor any folder above it up to %s: whether a file is at this path is not known", folder, child)
				if child == folder {
					note = fmt.Sprintf("the server could not read %s: whether a file is at this path is not known", folder)
				}

				return diskEntry{unknown: true, note: note}, nil
			}
			l, err := list(parent)
			if err != nil {
				return diskEntry{}, err
			}
			if !l.found {
				continue
			}
			switch {
			case len(l.entries) == 0:
				// empty is how a share gone offline, or a folder the
				// server may not read, lists: no proof of anything
				return diskEntry{unknown: true, note: fmt.Sprintf("the server lists nothing in %s: an empty folder, or one its process cannot read (a share gone offline), so whether a file is at this path is not known", parent)}, nil
			case slices.ContainsFunc(l.entries, func(e embyfin.FolderEntry) bool {
				return cmp.Or(e.Name, mediapath.Base(e.Path)) == mediapath.Base(child)
			}):
				return diskEntry{unknown: true, note: fmt.Sprintf("the server's disk lists the folder %s, but the server could not read it: whether a file is at this path is not known", child)}, nil
			case slices.ContainsFunc(library, func(p string) bool { return mediapath.Within(p, child) }):
				return diskEntry{unknown: true, note: fmt.Sprintf("the server's disk does not list %s, yet the library holds items there: the server cannot see its own library folder, so whether a file is at this path is not known", child)}, nil
			}

			return diskEntry{note: fmt.Sprintf("the folder %s is not on the server's disk (the nearest folder there is %s), so the disk has nothing at this path", child, parent)}, nil
		}
	}

	byFolder := map[string][]string{}
	for _, p := range paths {
		byFolder[mediapath.Dir(p)] = append(byFolder[mediapath.Dir(p)], p)
	}
	out := make(map[string]diskEntry, len(paths))
	for _, folder := range slices.Sorted(maps.Keys(byFolder)) {
		l, err := list(folder)
		if err != nil {
			return nil, err
		}
		if !l.found {
			d, err := absent(folder)
			if err != nil {
				return nil, err
			}
			for _, p := range byFolder[folder] {
				out[p] = d
			}

			continue
		}
		for _, p := range byFolder[folder] {
			if len(l.entries) == 0 {
				// an empty folder, or one the server cannot read: the path
				// check finds a file it cannot list, and nothing settles
				// the rest
				there, err := client.PathExists(ctx, p)
				if err != nil {
					return nil, fmt.Errorf("asking the server whether it can see %s: %w", p, err)
				}
				if there {
					out[p] = diskEntry{there: true, path: p, note: fmt.Sprintf("the server lists nothing in %s, yet finds this path: it cannot read the folder", folder)}
				} else {
					out[p] = diskEntry{unknown: true, note: fmt.Sprintf("the server lists nothing in %s: an empty folder, or one its process cannot read, so whether a file is at this path is not known", folder)}
				}

				continue
			}
			name := mediapath.Base(p)
			var same, folded *embyfin.FolderEntry
			for i := range l.entries {
				switch n := cmp.Or(l.entries[i].Name, mediapath.Base(l.entries[i].Path)); {
				case n == name:
					same = &l.entries[i]
				case strings.EqualFold(n, name):
					folded = &l.entries[i]
				}
			}
			switch {
			case same != nil:
				out[p] = diskEntry{there: true, folder: same.IsDir, path: p}
			case folded != nil:
				there, err := client.PathExists(ctx, p)
				if err != nil {
					return nil, fmt.Errorf("asking the server whether it can see %s: %w", p, err)
				}
				if there {
					spelled := cmp.Or(folded.Path, folder+"/"+folded.Name)
					out[p] = diskEntry{there: true, folder: folded.IsDir, path: spelled, note: fmt.Sprintf("the server's disk ignores case, and %q is at this path under its own spelling", folded.Name)}
				} else {
					out[p] = diskEntry{note: fmt.Sprintf("%q is beside this path, its name differing only in case: another file, on a disk that tells case apart", folded.Name)}
				}
			default:
				out[p] = diskEntry{}
			}
		}
	}

	return out, nil
}

// claimScore is how well the series a caller claims matches the series a
// path falls under, scored the way a name is resolved to a series: the
// claim's title and year against the series' title and year. Scoring the bare
// titles read "Severance (2022)" under Severance's own folder as a different
// show, and "Doctor Who" under a series the library names "Doctor Who (1963)"
// as nearly the same one.
func claimScore(claim string, series *embyfin.Item) float64 {
	named := *series
	if m := naming.SeriesNameYear.FindStringSubmatch(named.Name); m != nil {
		named.Name = strings.TrimSpace(strings.TrimSuffix(named.Name, m[0]))
		named.ProductionYear = cmp.Or(named.ProductionYear, atoi(m[1]))
	}
	score, _ := scoreSeries(naming.ParseRelease(claim), &named)

	return score
}
