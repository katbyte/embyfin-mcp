// Package tools defines the MCP tools exposed by embyfin-mcp, split by the
// resource they act on (server.go, libraries.go, items.go, ...). Tools are
// named resource-first (library_*, item_*) so they group by what they act on.
package tools

import (
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
