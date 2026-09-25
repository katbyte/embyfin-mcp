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
	// truncated says the tree was too big to list whole
	truncated bool
	// note is what could not be worked out
	note string
}

// treeMax is the most entries a delete lists under a folder it takes whole:
// enough for a series, and a bound on the reads a large one costs.
const treeMax = 2000

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
		// not a reason to refuse the delete: the answer says what is unknown
		return deletePlan{note: "could not read the folder holding the item, so what the delete takes with it is not known: " + lerr.Error()}, nil //nolint:nilerr // the failure is reported in the note
	case !found:
		return deletePlan{note: "the server cannot find the folder holding the item: the delete removes its record, and nothing on disk"}, nil
	}

	self := slices.IndexFunc(entries, func(e embyfin.FolderEntry) bool { return trimSep(e.Path) == trimSep(it.Path) })
	if self >= 0 && entries[self].IsDir {
		// a series, a season, a disc kept as its folder: the item is a folder
		return treePlan(ctx, client, it.Path)
	}
	if !sharedFolder(ctx, client, it, parent, entries, versions) {
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
// extra.
func sharedFolder(ctx context.Context, client *embyfin.Client, it *embyfin.Item, parent string, entries []embyfin.FolderEntry, versions []string) bool {
	if it.Type == typeEpisode {
		return true
	}
	if libs, err := libraryPaths(ctx, client); err != nil || slices.ContainsFunc(libs, func(l libraryPath) bool { return trimSep(l.path) == trimSep(parent) }) {
		// never a library's own folder, and when the libraries cannot be
		// read, the smaller answer
		return true
	}
	for _, e := range entries {
		if e.IsDir || !mediaExtensions.MatchString(e.Name) || isExtra(e.Name) {
			continue
		}
		if trimSep(e.Path) == trimSep(it.Path) || slices.ContainsFunc(versions, func(v string) bool { return trimSep(v) == trimSep(e.Path) }) {
			continue
		}

		return true
	}

	return false
}

// treePlan is a delete that takes a folder whole: the folder, and what is
// under it, listed.
func treePlan(ctx context.Context, client *embyfin.Client, folder string) (deletePlan, error) {
	tree, truncated, err := listTree(ctx, client, folder)
	if err != nil {
		return deletePlan{}, err
	}

	return deletePlan{folder: folder, watch: folder, before: tree, truncated: truncated}, nil
}

// listTree lists everything under a folder, at most treeMax entries. A
// folder the server cannot find is an empty tree.
func listTree(ctx context.Context, client *embyfin.Client, root string) (tree []embyfin.FolderEntry, truncated bool, err error) {
	queue := []string{root}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		entries, found, lerr := client.ListFolder(ctx, dir)
		if lerr != nil {
			return nil, false, lerr
		}
		if !found {
			continue
		}
		for _, e := range entries {
			if len(tree) >= treeMax {
				return tree, true, nil
			}
			tree = append(tree, e)
			if e.IsDir {
				queue = append(queue, e.Path)
			}
		}
	}

	return tree, false, nil
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
		more := ""
		if p.truncated {
			more = " and more"
		}
		return fmt.Sprintf("it would remove the folder %s with everything in it (%s%s)", p.folder, listed(names, 20), more)
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
		tree, _, err := listTree(ctx, r.client, plan.watch)
		if err != nil {
			return nil, "", err
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
	case plan.truncated:
		note = fmt.Sprintf("the folder held more than the %d paths listed before the delete; every one under it went with it", treeMax)
	case len(removed) == 0:
		note = "the server answered the delete, and nothing under " + plan.watch + " had gone yet"
	}

	return removed, note, nil
}
