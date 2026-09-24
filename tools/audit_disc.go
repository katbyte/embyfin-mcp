package tools

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A disc copied into the library as its own files rather than as a film.
//
// A Blu-ray is a folder of numbered streams (00000.m2ts, 00001.m2ts) and a
// DVD a folder of VOBs, and a server holds either as one item when the disc
// keeps its BDMV or VIDEO_TS structure. Flattened - the structure dropped and
// the streams left in the film's folder - the server sees a folder of videos
// and makes an item of each: the menu, the trailers, the studio logo and the
// feature, all as separate films, each matched on its own.
//
// The matches are the damage. A one-minute stream named 00003 is matched by
// whatever the server can make of it, so a disc can put a four-minute clip in
// the library under the name of a film the disc has nothing to do with, and
// the real film is left pointing at a menu.

// discPatterns are the file shapes a disc leaves behind, by what they are.
var discPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	// a Blu-ray's streams, five digits and an extension, nothing else
	{"flattened blu-ray", regexp.MustCompile(`(?i)/\d{5}\.(m2ts|mts|ssif)$`)},
	// a DVD's VOBs, titleset and part
	{"flattened dvd", regexp.MustCompile(`(?i)/vts_\d+_\d+\.vob$`)},
}

// discStructures are the folders a server holds a whole disc as, which is the
// arrangement that works: an item inside one is the server reaching past the
// disc into its parts.
var discStructures = []string{"BDMV", "VIDEO_TS", "AUDIO_TS"}

// discRoot is the folder to report an item under: the disc's own folder,
// which is the one holding the streams, or the one holding BDMV or VIDEO_TS.
func discRoot(path string) (root, kind string, isDisc bool) {
	// a server on Windows answers with backslashes; the shapes below are
	// written with one separator, so the path is read with one
	slashed := strings.ReplaceAll(path, `\`, "/")
	parts := strings.Split(slashed, "/")
	for i, seg := range parts[:max(len(parts)-1, 0)] {
		if slices.ContainsFunc(discStructures, func(s string) bool { return strings.EqualFold(seg, s) }) {
			return trimSep(strings.Join(parts[:i], "/")), "inside a disc structure", true
		}
	}
	for _, p := range discPatterns {
		if p.re.MatchString(slashed) {
			return parentDir(path), p.kind, true
		}
	}

	return "", "", false
}

// discTypes are the kinds of item a disc's parts are read as: a stream is a
// film or a video, and on a shows library an episode.
const discTypes = "Movie,Episode,Video"

type discIn struct {
	Library string `json:"library,omitempty" jsonschema:"one library by name or id"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum folders, default 50"`
}

type discOut struct {
	Scanned int         `json:"items_scanned"`
	Found   int         `json:"total_folders"`
	Folders []discGroup `json:"folders"       jsonschema:"most items first; capped at limit"`
}

type discRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	File      string `json:"file"                 jsonschema:"the file inside the disc folder"`
	RuntimeS  int    `json:"runtime_s,omitempty"`
	Size      int64  `json:"size,omitempty"       jsonschema:"file size in bytes"`
	MatchedTo string `json:"matched_to,omitempty" jsonschema:"the metadata provider ids the server gave this piece of the disc"`
}

type discGroup struct {
	Folder  string    `json:"folder"`
	Kind    string    `json:"kind"           jsonschema:"flattened blu-ray or flattened dvd: the disc's streams with no BDMV or VIDEO_TS structure around them, so each is read as its own film. inside a disc structure: the server reached past a disc folder into its parts"`
	Items   int       `json:"items"          jsonschema:"how many items the server built from this one disc"`
	Entries []discRow `json:"entries"        jsonschema:"by file"`
	Note    string    `json:"note,omitempty"`
}

func registerDiscAudit(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_disc_folders",
		Description: "Find discs copied in as their own files rather than as a film: a Blu-ray's numbered streams (00000.m2ts) or a DVD's VOBs sitting in a film's folder with no BDMV or VIDEO_TS structure around them. " +
			"The server makes an item of each stream, so one disc becomes several films - the menu, the trailers and the feature - each matched on its own, which puts short clips in the library under the names of films the disc has nothing to do with. " +
			"Each folder lists what the server built from it, with the runtime and the ids it matched, so the wrong ones are plain. Remuxing the feature to one file and removing the rest is the cure; deleting the items is not, because they are the disc.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in discIn) (*mcp.CallToolResult, discOut, error) {
		out, err := auditDiscFolders(ctx, client, in)

		return nil, out, err
	})
}

func auditDiscFolders(ctx context.Context, client *embyfin.Client, in discIn) (discOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	opts, err := sweepOptions(ctx, client, in.Library, discTypes, discTypes, "Path,MediaSources,ProviderIds")
	if err != nil {
		return discOut{}, err
	}

	type group struct {
		kind    string
		entries []discRow
		// what each piece was matched to, as one string per piece: two
		// pieces of one disc matched to different titles is the defect
		matches map[string]bool
	}
	byFolder := map[string]*group{}
	out := discOut{Folders: []discGroup{}}
	scanned, err := sweepAll(ctx, client, opts, func(items []embyfin.Item) {
		for i := range items {
			it := &items[i]
			root, kind, isDisc := discRoot(it.Path)
			if !isDisc {
				continue
			}
			g := byFolder[root]
			if g == nil {
				g = &group{kind: kind, matches: map[string]bool{}}
				byFolder[root] = g
			}
			ids := make([]string, 0, 2)
			for provider, id := range providerKeys(it.ProviderIDs) {
				ids = append(ids, strings.ToLower(provider)+":"+id)
			}
			slices.Sort(ids)
			matched := strings.Join(ids, " ")
			if matched != "" {
				g.matches[matched] = true
			}
			g.entries = append(g.entries, discRow{
				ID: it.ID, Name: it.Name, File: baseName(it.Path),
				RuntimeS: int(it.RunTimeTicks / ticksPerSecond),
				Size:     qualityOf(it).Size, MatchedTo: matched,
			})
		}
	})
	out.Scanned = scanned
	if err != nil {
		return discOut{}, err
	}

	groups := make([]discGroup, 0, len(byFolder))
	for folder, g := range byFolder {
		slices.SortFunc(g.entries, func(a, b discRow) int { return strings.Compare(a.File, b.File) })
		row := discGroup{Folder: folder, Kind: g.kind, Items: len(g.entries), Entries: g.entries}
		// the tell that the pieces were matched on their own: one disc
		// cannot be several films
		if len(g.matches) > 1 {
			row.Note = fmt.Sprintf("matched to %d different titles, so at least %d of these are the wrong film", len(g.matches), len(g.matches)-1)
		}
		groups = append(groups, row)
	}
	slices.SortFunc(groups, func(a, b discGroup) int {
		if a.Items != b.Items {
			return b.Items - a.Items
		}

		return strings.Compare(a.Folder, b.Folder)
	})

	out.Found = len(groups)
	out.Folders = append(out.Folders, groups[:min(len(groups), limit)]...)

	return out, nil
}
