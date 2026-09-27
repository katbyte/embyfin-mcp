package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// What a delete takes off the disk.
//
// Deleting an item deletes more than its own file, the same way on both
// servers (seen live on Emby 4.10 and Jellyfin 12.1): a film alone in its
// folder takes the whole folder - its nfo, its artwork, its subtitles, its
// extras and every version of it - and so does a series or a season; a film
// sharing its folder with other films, and an episode, take their own files
// and every nfo, subtitle and piece of artwork whose name begins with theirs -
// a neighbour's too, when its name begins the same way ("Blade II.nfo" goes
// with "Blade.mkv"). What a server does is worked out before the delete, for
// a caller who has not yet said confirm, and read off the disk before and
// after it, for the answer.

// removedPath is one file or folder a delete took, or would take.
type removedPath struct {
	Path   string `json:"path"`
	Folder bool   `json:"folder,omitempty" jsonschema:"a folder, gone with everything in it"`
}

// deletePlan is what a delete of one item takes off the disk.
type deletePlan struct {
	// folder is the folder the server deletes whole, "" when it deletes
	// files and keeps the folder they are in
	folder string
	// files are the files it deletes, when it keeps their folder
	files []string
	// others are the files among them that are not the item's own, which
	// the server takes because their names begin with its file's name:
	// another item's ("Blade II.nfo" beside "Blade.mkv"), or no item's
	others []string
	// watch is the folder read before and after: the one deleted whole, or
	// the one holding the files
	watch string
	// before is everything under watch before the delete: the whole tree
	// when the folder goes, the folder's own entries when it stays
	before []embyfin.FolderEntry
	// truncated says the tree held more than before lists, and total how
	// many files and folders it held
	truncated bool
	total     int
	// note is what could not be worked out, and unknown says the folder
	// could not be read, so what the delete takes with the item is not known
	note    string
	unknown bool
}

// treeMax is the most entries a delete lists under a folder it takes whole:
// enough for a series, and a bound on what one answer carries. Past it the
// tree is still read to the end, to count, up to treeDirsMax folders.
const (
	treeMax     = 2000
	treeDirsMax = 20000
)

// mediaExtensions are the files a server makes items of: another one of
// these in a film's folder makes the folder a shared one, which the server
// does not delete for one of its films.
var mediaExtensions = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|m4v|ts|m2ts|mts|vob|mov|wmv|mpg|mpeg|m2v|flv|webm|ogm|divx|iso|mp3|flac|m4a|aac|ogg|opus|wav|wma|ape)$`)

// sidecarFiles are the files a server deletes with an item it takes out of a
// shared folder, when their names begin with the item's file name whatever
// the case, each server by its own list (seen live on Emby 4.10 and Jellyfin
// 12.1): the nfo, the subtitles and the artwork, and never a media file.
var sidecarFiles = map[embyfin.Backend]*regexp.Regexp{
	embyfin.Emby:     regexp.MustCompile(`(?i)\.(nfo|xml|srt|ass|ssa|vtt|sub|sup|smi|edl|bif|jpe?g|png|webp|gif|tbn)$`),
	embyfin.Jellyfin: regexp.MustCompile(`(?i)\.(nfo|xml|srt|vtt|sub|sup|idx|txt|edl|bif|smi|ttml|lrc|elrc|jpe?g|png|webp|gif|tbn|svg)$`),
}

// extraSuffix names a film's extras, which live in its folder without making
// it a shared one: "Film (2001)-trailer.mkv", "trailer.mp4", "sample.mkv".
var extraSuffix = regexp.MustCompile(`(?i)(^|[-._ ])(trailer|sample|scene|clip|interview|behindthescenes|deleted|deletedscene|featurette|short|other|extra|teaser)$`)

// isExtra says whether a file in a film's folder is one of its extras.
func isExtra(name string) bool {
	return extraSuffix.MatchString(strings.TrimSuffix(name, filepath.Ext(name)))
}

// planDelete works out what deleting an item takes off the disk. versions
// are the paths of every version of it the server holds.
func planDelete(ctx context.Context, client *embyfin.Client, it *embyfin.Item, versions []string) (deletePlan, error) {
	if it.Path == "" || !onDisk(it.Path) {
		return deletePlan{note: "the item has no file or folder on disk: the delete removes the server's record of it"}, nil
	}
	parent := parentDir(it.Path)
	entries, found, lerr := client.ListFolder(ctx, parent)
	switch {
	case lerr != nil:
		// not a reason to refuse a confirmed delete: the refusal and the
		// answer both say what is not known, and why
		return deletePlan{unknown: true, note: "could not read the folder holding the item, so what the delete takes with it is not known: " + lerr.Error()}, nil //nolint:nilerr // the failure is reported in the note
	case !found:
		return deletePlan{note: "the server cannot find the folder holding the item: the delete removes its record, and nothing on disk"}, nil
	}

	self := slices.IndexFunc(entries, func(e embyfin.FolderEntry) bool { return trimSep(e.Path) == trimSep(it.Path) })
	if self >= 0 && entries[self].IsDir {
		// a series, a season, an album, an artist, a disc kept as its
		// folder, a folder of a mixed library: the item is a folder
		return treePlan(ctx, client, it.Path)
	}
	shared, err := sharedFolder(ctx, client, it, parent, entries, versions)
	if err != nil {
		return deletePlan{}, err
	}
	if !shared {
		// a film alone in its folder takes the folder
		return treePlan(ctx, client, parent)
	}

	// the item's own files, and every sidecar whose name begins with one of
	// theirs: both servers go by the name alone, so "Blade.mkv" takes "Blade
	// II.nfo" and "Blade II.srt" with it, though never "Blade II.mkv"
	plan := deletePlan{watch: parent, before: entries}
	own := map[string]bool{it.Path: true}
	for _, v := range versions {
		if parentDir(v) == parent {
			own[v] = true
		}
	}
	sidecar := sidecarFiles[client.Backend()]
	stem := func(path string) string {
		return strings.ToLower(strings.TrimSuffix(baseName(path), filepath.Ext(path)))
	}
	// the other media files' names, which say whose a sidecar taken by the
	// name alone really is: "Blade II.nfo" is Blade II's, and goes with Blade
	var neighbours []string
	for _, e := range entries {
		if !e.IsDir && !own[e.Path] && mediaExtensions.MatchString(e.Name) {
			neighbours = append(neighbours, stem(e.Path))
		}
	}
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		if own[e.Path] {
			plan.files = append(plan.files, e.Path)

			continue
		}
		if sidecar == nil || !sidecar.MatchString(e.Name) {
			continue
		}
		// the longest of the item's file names the sidecar's begins with:
		// "Blade - 1080p.nfo" is the 1080p version's, not Blade's
		name, base := strings.ToLower(e.Name), ""
		for v := range own {
			if b := stem(v); strings.HasPrefix(name, b) && len(b) > len(base) {
				base = b
			}
		}
		if base == "" {
			continue
		}
		plan.files = append(plan.files, e.Path)
		// the item's own sidecar carries its file's name and then a separator
		// ("Blade.nfo", "Blade-poster.jpg", "Blade.en.srt"); one the name
		// runs on into ("Blade II.nfo"), or a longer neighbour's name begins,
		// is another's
		rest := strings.TrimPrefix(name, base)
		if rest == "" || !strings.ContainsRune(".-_", rune(rest[0])) ||
			slices.ContainsFunc(neighbours, func(n string) bool { return len(n) > len(base) && strings.HasPrefix(name, n) }) {
			plan.others = append(plan.others, e.Path)
		}
	}
	slices.Sort(plan.files)
	slices.Sort(plan.others)

	return plan, nil
}

// sharedFolder says whether an item's folder holds more than it: an episode
// always shares (a server never takes a season's folder for one of its
// episodes), a file straight in a library's own folder always does, and a
// film does when another media file there is neither a version of it nor an
// extra. The libraries not read is an error: the answer is the difference
// between a file and a whole folder going.
func sharedFolder(ctx context.Context, client *embyfin.Client, it *embyfin.Item, parent string, entries []embyfin.FolderEntry, versions []string) (bool, error) {
	if it.Type == typeEpisode {
		return true, nil
	}
	libs, err := libraryPaths(ctx, client)
	if err != nil {
		return false, fmt.Errorf("could not read the libraries' folders, so whether the delete takes %s's whole folder is not known: %w", it.Name, err)
	}
	if slices.ContainsFunc(libs, func(l libraryPath) bool { return trimSep(l.path) == trimSep(parent) }) {
		// never a library's own folder
		return true, nil
	}
	for _, e := range entries {
		if e.IsDir || !mediaExtensions.MatchString(e.Name) || isExtra(e.Name) {
			continue
		}
		if trimSep(e.Path) == trimSep(it.Path) || slices.ContainsFunc(versions, func(v string) bool { return trimSep(v) == trimSep(e.Path) }) {
			continue
		}

		return true, nil
	}

	return false, nil
}

// treePlan is a delete that takes a folder whole: the folder, and what is
// under it, listed.
func treePlan(ctx context.Context, client *embyfin.Client, folder string) (deletePlan, error) {
	tree, total, err := listTree(ctx, client, folder)
	if err != nil {
		return deletePlan{}, err
	}

	return deletePlan{folder: folder, watch: folder, before: tree, truncated: total > len(tree), total: total}, nil
}

// listTree lists everything under a folder, the first treeMax entries, and
// counts them all: a folder past treeDirsMax folders deep in folders is an
// error rather than a count short of the truth. A folder the server cannot
// find is an empty tree.
func listTree(ctx context.Context, client *embyfin.Client, root string) (tree []embyfin.FolderEntry, total int, err error) {
	queue := []string{root}
	for dirs := 0; len(queue) > 0; dirs++ {
		if dirs >= treeDirsMax {
			return nil, 0, fmt.Errorf("%s holds more than %d folders, too many to count what a delete of it would take", root, treeDirsMax)
		}
		dir := queue[0]
		queue = queue[1:]
		entries, found, lerr := client.ListFolder(ctx, dir)
		if lerr != nil {
			return nil, 0, lerr
		}
		if !found {
			continue
		}
		for _, e := range entries {
			total++
			if len(tree) < treeMax {
				tree = append(tree, e)
			}
			if e.IsDir {
				queue = append(queue, e.Path)
			}
		}
	}

	return tree, total, nil
}

// takes says whether the plan's delete takes a path: one under the folder it
// deletes whole, or one of the files it deletes. A plan that could not be
// worked out takes nothing it can name.
func (p deletePlan) takes(path string) bool {
	if p.folder != "" {
		return within(path, p.folder)
	}

	return slices.ContainsFunc(p.files, func(f string) bool { return trimSep(f) == trimSep(path) })
}

// notForItemDelete are the kinds item_delete refuses, each with what does
// delete it: the server's delete of a list or a library as an item is not
// what item_delete says it is (a delete of media from disk), and the
// answer's "items under it" would count what the list holds, which stays.
var notForItemDelete = map[string]string{
	"BoxSet":                "collection_delete deletes a collection, and leaves the items it holds",
	"Playlist":              "playlist_delete deletes a playlist, and leaves the items it holds",
	"CollectionFolder":      "library_delete removes a library, and leaves its files on disk",
	"UserView":              "library_delete removes a library, and leaves its files on disk",
	"AggregateFolder":       "it is the server's own root, which nothing deletes",
	"UserRootFolder":        "it is the server's own root, which nothing deletes",
	"PlaylistsFolder":       "it is the server's folder of playlists: playlist_delete deletes one playlist",
	"ManualPlaylistsFolder": "it is the server's folder of playlists: playlist_delete deletes one playlist",
	"Genre":                 "a genre is a name items carry, not something on disk: metadata_rename renames it, or with remove takes it off every item",
	"MusicGenre":            "a genre is a name items carry, not something on disk: metadata_rename renames it, or with remove takes it off every item",
	"Studio":                "a studio is a name items carry, not something on disk: metadata_rename (field studios) renames it, or with remove takes it off every item",
	"Person":                "a person is a name items credit, not something on disk, and no tool deletes one",
	"MusicArtist":           "an artist is a name the server gathers albums and songs under, and a delete of it takes whatever folder the server counts as the artist's: delete its albums with item_delete, one at a time",
}

// would is what the plan says the delete would remove, for a caller who has
// not confirmed.
func (p deletePlan) would() string {
	switch {
	case p.note != "":
		return p.note
	case p.folder != "":
		names := make([]string, 0, len(p.before))
		for _, e := range p.before {
			names = append(names, strings.TrimPrefix(e.Path, p.folder+"/"))
		}
		// the count is of the whole tree; the names only of what was listed
		shown := listed(names, 20)
		if p.total > len(names) {
			shown = fmt.Sprintf("%s, of %d in all", listed(names[:min(len(names), 20)], 20), p.total)
		}
		return fmt.Sprintf("it would remove the folder %s with everything in it, %d files and folders (%s)", p.folder, p.total, shown)
	}

	out := "it would remove " + listed(p.files, 20)
	if len(p.others) > 0 {
		// the server goes by the name alone, whatever follows it and
		// whatever its case, so a neighbour whose name begins with this
		// file's loses its sidecars: said plainly, before anyone confirms
		out += ". Of those, " + listed(p.others, 20) + " are not this item's own - another item's, or none's - and go because the server deletes every nfo, subtitle and image whose name begins with this file's name, whoever's it is"
	}

	return out
}

// listed joins names, at most n of them and a count of the rest.
func listed(names []string, n int) string {
	if len(names) == 0 {
		return "nothing else"
	}
	if len(names) <= n {
		return strings.Join(names, ", ")
	}

	return fmt.Sprintf("%s and %d more", strings.Join(names[:n], ", "), len(names)-n)
}

// removedSaid names what a delete removed, for a sentence.
func removedSaid(removed []removedPath) string {
	paths := make([]string, 0, len(removed))
	for _, p := range removed {
		paths = append(paths, p.Path)
	}

	return listed(paths, 20)
}

// afterFailedDelete is the error for an item delete the server answered with
// an error, saying what it read back: whether the item is still listed, and
// what went from the disk. A server can take the files and fail after, and
// a bare error said nothing of either.
func (r *registry) afterFailedDelete(ctx context.Context, plan deletePlan, it *embyfin.Item, err error) error {
	removed, _, rerr := r.removedBy(ctx, plan, it.Path)
	disk := "from the disk: nothing it was to take went"
	switch {
	case rerr != nil:
		disk = "and reading the disk back failed, so what went from it is not known: " + rerr.Error()
	case plan.unknown:
		// the folder was not read before, so there is nothing to compare
		disk = "what it took from the disk is not known, since the folder holding it could not be read before; " + r.ownFileNow(ctx, it.Path)
	case plan.watch == "" && onDisk(it.Path):
		disk = "the server could not find the folder holding it before, so nothing on disk was to go; " + r.ownFileNow(ctx, it.Path)
	case plan.watch == "":
		disk = "it had nothing on disk to take"
	case len(removed) > 0:
		disk = "from the disk, these went: " + removedSaid(removed)
	}
	listed := "the server still lists it"
	if _, lerr := r.client.ItemByID(ctx, it.ID); lerr != nil {
		listed = "the server no longer lists it"
		if !strings.Contains(lerr.Error(), "no item with id") {
			listed = "reading whether the server still lists it failed: " + lerr.Error()
		}
	}

	return fmt.Errorf("the delete of %s answered an error: %w. Read back, %s; %s", it.Name, err, listed, disk)
}

// ownFileNow says whether an item's own file is still on the server's disk,
// for a delete whose plan could not say what it would take.
func (r *registry) ownFileNow(ctx context.Context, path string) string {
	there, err := r.client.PathExists(ctx, path)
	switch {
	case err != nil:
		return "whether its own file " + path + " is still there could not be read: " + err.Error()
	case there:
		return "its own file " + path + " is still there"
	}

	return "its own file " + path + " is gone"
}

// removedBy reads the disk after a delete and says what the plan's watched
// folder lost, the folder itself included when it went.
func (r *registry) removedBy(ctx context.Context, plan deletePlan, itemPath string) ([]removedPath, string, error) {
	if plan.watch == "" {
		return []removedPath{}, plan.note, nil
	}
	// a server answers the delete as it finishes it, or a moment before:
	// the item's own file going is the sign it has
	var after []embyfin.FolderEntry
	gone := false
	for range 20 {
		entries, found, err := r.client.ListFolder(ctx, plan.watch)
		if err != nil {
			return nil, "", err
		}
		if !found {
			gone, after = true, nil

			break
		}
		after = entries
		ownGone := !slices.ContainsFunc(entries, func(e embyfin.FolderEntry) bool { return trimSep(e.Path) == trimSep(itemPath) })
		if plan.folder == "" && ownGone {
			break
		}
		if err := r.pause(ctx); err != nil {
			return nil, "", err
		}
	}
	if !gone && plan.folder != "" {
		// the folder stayed, against the plan: what went from under it
		tree, total, err := listTree(ctx, r.client, plan.watch)
		if err != nil {
			return nil, "", err
		}
		if total > len(tree) {
			return nil, "", fmt.Errorf("%s stayed, holding %d files and folders, more than the %d this can compare against what it held", plan.watch, total, len(tree))
		}
		after = tree
	}

	removed := []removedPath{}
	if gone {
		removed = append(removed, removedPath{Path: plan.watch, Folder: true})
	}
	left := map[string]bool{}
	for _, e := range after {
		left[trimSep(e.Path)] = true
	}
	for _, e := range plan.before {
		if !left[trimSep(e.Path)] {
			removed = append(removed, removedPath{Path: e.Path, Folder: e.IsDir})
		}
	}
	slices.SortFunc(removed, func(a, b removedPath) int { return strings.Compare(a.Path, b.Path) })
	note := ""
	switch {
	case plan.truncated && gone:
		note = fmt.Sprintf("the folder held %d files and folders, and every one went with it; removed lists the folder and the first %d", plan.total, treeMax)
	case plan.truncated:
		note = fmt.Sprintf("the folder held %d files and folders; removed lists what went of the first %d", plan.total, treeMax)
	case len(removed) == 0:
		note = "the server answered the delete, and nothing under " + plan.watch + " had gone yet"
	}

	return removed, note, nil
}
