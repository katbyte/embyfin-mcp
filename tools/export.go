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
		Path       string   `json:"path"                  jsonschema:"where to write, on the machine embyfin-mcp runs on; one JSON object per line. Always a new file: a path anything is already at is refused, so choose one nothing is at"`
		Library    string   `json:"library,omitempty"     jsonschema:"name or id; default every library"`
		Types      string   `json:"types,omitempty"       jsonschema:"comma-separated item types; default Episode (the bulk case); Movie, or Movie,Episode"`
		Fields     []string `json:"fields,omitempty"      jsonschema:"only these facts on each episode row: path, date_created, file_modified, runtime_s, container, size, bitrate, width, height, aspect_ratio, display_width, video_codec, frame_rate, hdr, audio, subtitles; default all"`
		WithFile   *bool    `json:"with_file,omitempty"   jsonschema:"only items with a file; default true"`
		SavedSince string   `json:"saved_since,omitempty" jsonschema:"only items the server last saved at or after this time (RFC3339)"`
	}
	type exportFileOut struct {
		Path  string `json:"path"`
		Rows  int    `json:"rows"  jsonschema:"objects written, one per line"`
		Bytes int64  `json:"bytes"`
		Shape string `json:"shape" jsonschema:"what each line holds: an episode row as library_episodes answers it, or an item summary as library_items does"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_export",
		Description: "Write every episode (or film) in a library to a file on the machine embyfin-mcp runs on, one JSON object per line with the same fields as a library_episodes row (or a library_items summary for films): the whole library in one call and nothing through the conversation, for a caller that will compare it against a folder or a vault. Reads the server only, and writes only a new file: a path anything is already at is refused, never written over.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in exportFileIn) (*mcp.CallToolResult, exportFileOut, error) {
		if strings.TrimSpace(in.Path) == "" {
			return nil, exportFileOut{}, errors.New("path is required: where on this machine to write the file")
		}
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
			opts.Fields = "Path,DateCreated,DateModified"
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

		f, err := createExport(in.Path)
		if err != nil {
			return nil, exportFileOut{}, err
		}
		// what this call made, so a failure removes that file and never
		// whatever may have been put at the path since
		created, err := f.Stat()
		if err != nil {
			_ = f.Close()

			return nil, exportFileOut{}, err
		}
		w := bufio.NewWriterSize(f, 1<<20)
		enc := json.NewEncoder(w)
		out := exportFileOut{Path: in.Path, Shape: "item summary"}
		if episodes {
			out.Shape = "episode row"
		}
		var writeErr error
		sweepErr := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
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
			if now, serr := os.Lstat(in.Path); serr == nil && os.SameFile(created, now) {
				_ = os.Remove(in.Path)
			}

			return nil, exportFileOut{}, err
		}
		if st, err := os.Stat(in.Path); err == nil {
			out.Bytes = st.Size()
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
