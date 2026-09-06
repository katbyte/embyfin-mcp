// Package tools defines the MCP tools exposed by embyfin-mcp, split by the
// resource they act on (server.go, libraries.go, items.go, ...). Tools are
// named resource-first (library_*, item_*) so they group by what they act on.
package tools

import (
	"context"
	"reflect"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options controls which optional tools are registered.
type Options struct {
	// EnableDelete registers item_delete, which permanently removes media
	// files. Off unless the operator opts in.
	EnableDelete bool
}

// RegisterAll adds every tool to the MCP server.
func RegisterAll(server *mcp.Server, client *embyfin.Client, opts Options) {
	registerServerTools(server, client)
	registerLibraryTools(server, client)
	registerAuditTools(server, client)
	registerItemTools(server, client, opts)
	registerIdentifyTools(server, client)
	registerArtworkTools(server, client)
	registerSubtitleTools(server, client)
	registerShowTools(server, client)
	registerUserTools(server, client)
	registerSessionTools(server, client)
	registerPlaylistTools(server, client)
	registerCollectionTools(server, client)
}

// activityScanLimit is how many activity log entries history tools read before filtering.
const activityScanLimit = 1000

// sortDescending is the MediaBrowser SortOrder for newest/most-recent first.
const sortDescending = "Descending"

// daysCutoff converts a days-back input (default 60) to the cutoff time.
func daysCutoff(days int) time.Time {
	if days <= 0 {
		days = 60
	}

	return time.Now().AddDate(0, 0, -days)
}

// afterCutoff reports whether an RFC3339-ish server timestamp is at or after
// the cutoff. Unparseable or empty timestamps count as before it.
func afterCutoff(stamp string, cutoff time.Time) bool {
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return false
	}

	return !t.Before(cutoff)
}

// addTool registers a typed tool handler, normalising its output so that empty
// collections serialise as [] rather than null: an AI client reading "people":
// null cannot tell "none" from "not fetched", and Go leaves un-appended slices nil.
func addTool[In, Out any](server *mcp.Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	mcp.AddTool(server, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		res, out, err := h(ctx, req, in)
		if err == nil {
			emptyNilSlices(reflect.ValueOf(&out).Elem())
		}

		return res, out, err
	})
}

// emptyNilSlices walks v (structs, pointers, slices) and replaces every settable
// nil slice with an empty one.
func emptyNilSlices(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			emptyNilSlices(v.Elem())
		}
	case reflect.Struct:
		for _, f := range v.Fields() {
			emptyNilSlices(f)
		}
	case reflect.Slice:
		if v.IsNil() {
			if v.CanSet() {
				v.Set(reflect.MakeSlice(v.Type(), 0, 0))
			}

			return
		}
		for i := range v.Len() {
			emptyNilSlices(v.Index(i))
		}
	default:
	}
}
