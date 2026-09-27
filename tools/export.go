package tools

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// library_export writes a whole library to a file on the machine embyfin-mcp
// runs on, one JSON object per line, instead of answering it page by page.
// A large library read through library_episodes is hundreds of calls of
// half a megabyte each, every one of them passing through the model's
// context; a file passes through nothing, and a caller that wants the
// numbers rather than the prose reads it with whatever it likes.
//
// It reads the server and writes the local disk: nothing on the server
// changes, so it is a read tool, and a read-only session can use it. That is
// only safe because the one thing it writes is a file that did not exist.
// It used to take an overwrite flag, which made a read tool able to replace
// any file the process could write - so there is no such flag, and a path
// something is already at (a file, a folder, a link) is refused.
func registerExportTool(r *registry) {
	client := r.client

	type exportFileIn struct {
		Path       string   `json:"path"                  jsonschema:"where to write, on the machine embyfin-mcp runs on, as a full path from the top (a relative one is refused); one JSON object per line. Always a new file: a path anything is already at is refused, so choose one nothing is at"`
		Library    string   `json:"library,omitempty"     jsonschema:"name or id; default every library"`
		Types      string   `json:"types,omitempty"       jsonschema:"comma-separated item types; default Episode (the bulk case); Movie, or Movie,Episode"`
		Fields     []string `json:"fields,omitempty"      jsonschema:"only these facts on each episode row: path, date_created, file_modified, runtime_s, container, size, bitrate, width, height, aspect_ratio, display_width, video_codec, frame_rate, hdr, audio, subtitles; default all"`
		WithFile   *bool    `json:"with_file,omitempty"   jsonschema:"only items with a file; default true"`
		SavedSince string   `json:"saved_since,omitempty" jsonschema:"only items the server last saved at or after this time: a date such as 2026-01-02, or a time such as 2026-01-02T15:04:05Z"`
	}
	type exportFileOut struct {
		Path  string `json:"path"            jsonschema:"the file written, as a full path"`
		Rows  int    `json:"rows"            jsonschema:"objects written, one per line"`
		Bytes *int64 `json:"bytes,omitempty" jsonschema:"the file's size; left out when it could not be read, and note says why"`
		Shape string `json:"shape"           jsonschema:"what each line holds: an episode row as library_episodes answers it, or an item summary as library_items does"`
		Note  string `json:"note,omitempty"  jsonschema:"set when the file's size could not be read, and when the library was seen to change while it was read: items added or removed meanwhile may be missing from the file, or in it though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. It says nothing of a change when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_export",
		Description: "Write every episode (or film) in a library to a file on the machine embyfin-mcp runs on, one JSON object per line with the same fields as a library_episodes row (or a library_items summary for films): the whole library in one call and nothing through the conversation, for a caller that will compare it against a folder or a vault. A line whose item is held in more than one file lists every file in versions (Jellyfin folds a second file of one film or episode in one folder into the item), so compare a folder against versions as well as path. Reads the server only, and writes only a new file, at a full path (a relative one is refused: it would land wherever the process started), making the folders it needs: a path anything is already at is refused, never written over, and a file an export that fails part way wrote is removed. The file holds the library's paths and titles, so keep it out of anything shared.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in exportFileIn) (*mcp.CallToolResult, exportFileOut, error) {
		if strings.TrimSpace(in.Path) == "" {
			return nil, exportFileOut{}, errors.New("path is required: where on this machine to write the file")
		}
		if !filepath.IsAbs(in.Path) {
			return nil, exportFileOut{}, fmt.Errorf("path %q is relative, and would be written wherever embyfin-mcp was started: give a full path", in.Path)
		}
		path := filepath.Clean(in.Path)
		types := cmp.Or(in.Types, typeEpisode)
		keep, err := keptFacts(in.Fields)
		if err != nil {
			return nil, exportFileOut{}, err
		}
		withFile := in.WithFile == nil || *in.WithFile
		episodes := !strings.Contains(types, ",") && strings.EqualFold(types, typeEpisode)

		opts := embyfin.SearchOptions{IncludeItemTypes: types, SortBy: episodeSweepSort, SortOrder: "Ascending", SavedSince: in.SavedSince}
		if !episodes {
			opts.SortBy = "SortName,ProductionYear,DateCreated"
		}
		if episodes && !needsMediaSources(keep) {
			// how many files an item is held in, so the paths of the ones
			// Jellyfin folds into it are read back (withVersionFiles)
			opts.Fields = "Path,DateCreated,DateModified," + versionCountField
		} else {
			opts.Fields = embyfin.FieldsDefault + ",DateModified"
		}
		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, exportFileOut{}, err
		}
		if folder != nil {
			opts.ParentID = folder.ItemID
		}

		f, err := createExport(path)
		if err != nil {
			return nil, exportFileOut{}, err
		}
		// what this call made, so a failure removes that file and never
		// whatever may have been put at the path since. Not read, the file
		// made an instant ago, empty, goes again
		created, err := f.Stat()
		if err != nil {
			err = fmt.Errorf("reading the file just made at %s: %w", path, err)
			if cerr := f.Close(); cerr != nil {
				err = errors.Join(err, fmt.Errorf("closing it: %w", cerr))
			}
			if rerr := os.Remove(path); rerr != nil {
				err = errors.Join(err, fmt.Errorf("removing the empty file it left failed, so remove %s by hand: %w", path, rerr))
			}

			return nil, exportFileOut{}, err
		}
		w := bufio.NewWriterSize(f, 1<<20)
		enc := json.NewEncoder(w)
		out := exportFileOut{Path: path, Shape: "item summary"}
		if episodes {
			out.Shape = "episode row"
		}
		var writeErr error
		swept, sweepErr := client.ReadAll(ctx, opts, embyfin.ToAnswer, func(items []embyfin.Item) bool {
			if writeErr = withVersionFiles(ctx, client, items); writeErr != nil {
				return false
			}
			for i := range items {
				it := &items[i]
				if withFile && !it.HasFile() {
					continue
				}
				var row any = summarise(it)
				if episodes {
					row = episodeFacts(it, true, keep)
				}
				if writeErr = enc.Encode(row); writeErr != nil {
					return false
				}
				out.Rows++
			}

			return true
		})
		if err := errors.Join(sweepErr, writeErr, w.Flush(), f.Close()); err != nil {
			// half a file is worse than none: the caller would read it as whole
			now, serr := os.Lstat(path)
			switch {
			case serr != nil:
				err = errors.Join(err, fmt.Errorf("and reading the part written at %s to remove it failed, so it may still be there, cut short: %w", path, serr))
			case !os.SameFile(created, now):
				err = errors.Join(err, fmt.Errorf("and %s is no longer the file this export made, so it was left as it is", path))
			default:
				if rerr := os.Remove(path); rerr != nil {
					err = errors.Join(err, fmt.Errorf("and removing the part written failed, so %s is there cut short: remove it by hand: %w", path, rerr))
				}
			}

			return nil, exportFileOut{}, err
		}
		out.Note = swept.Changed()
		st, err := os.Stat(path)
		if err != nil {
			out.Note = joinWarnings("the file was written whole, but reading its size failed: "+err.Error(), out.Note)
		} else {
			out.Bytes = new(st.Size())
		}

		return nil, out, nil
	})
}

// createExport makes the file to write, and the folder it goes in. The file
// is created exclusively: the create itself fails when anything is at the
// path, a link included, so there is no gap between looking and writing for
// another export - or anything else - to land in. It was a look, then a
// create that truncated whatever had arrived in between.
func createExport(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}

	//nolint:gosec // the path is the caller's own choice of where to write, and only ever a new file
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("%s exists: library_export only writes a new file and never replaces one, so choose a path nothing is at", path)
	}

	return f, err
}
