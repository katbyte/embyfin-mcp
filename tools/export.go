package tools

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// It reads the server and writes the local disk, which is why the path is
// the caller's to give and an existing file is refused unless it says to
// write over it: nothing on the server changes, so it is a read tool, and a
// read-only session can use it.
func registerExportTool(r *registry) {
	client := r.client

	type exportFileIn struct {
		Path       string   `json:"path"                  jsonschema:"where to write, on the machine embyfin-mcp runs on; one JSON object per line"`
		Overwrite  bool     `json:"overwrite,omitempty"   jsonschema:"write over a file that exists; default refuses"`
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
		Description: "Write every episode (or film) in a library to a file on the machine embyfin-mcp runs on, one JSON object per line with the same fields as a library_episodes row (or a library_items summary for films): the whole library in one call and nothing through the conversation, for a caller that will compare it against a folder or a vault. Reads the server only; writes the file, refusing one that exists unless overwrite is set.",
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
			opts.SortBy = "SortName"
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

		f, err := createExport(in.Path, in.Overwrite)
		if err != nil {
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
			_ = os.Remove(in.Path) // half a file is worse than none: the caller would read it as whole
			return nil, exportFileOut{}, err
		}
		if st, err := os.Stat(in.Path); err == nil {
			out.Bytes = st.Size()
		}

		return nil, out, nil
	})
}

// createExport opens the file to write, refusing to write over one that
// exists unless asked, and making the folder it goes in.
func createExport(path string, overwrite bool) (*os.File, error) {
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return nil, fmt.Errorf("%s exists: pass overwrite to write over it", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}

	return os.Create(path) //nolint:gosec // the path is the caller's own choice of where to write
}
