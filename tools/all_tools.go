// Package tools defines the MCP tools exposed by embyfin-mcp, split by the
// resource they act on (server.go, libraries.go, items.go, ...). Tools are
// named resource-first (library_*, item_*, audit_*) so they group by what
// they act on.
//
// Every tool is registered through add with a kind: read tools never change
// server state, write tools do (and are dropped under --read-only), and delete
// tools remove what cannot be put back - media files, libraries, the items a
// removed library left, playlists and collections - and are only registered
// with --enable-delete. --toolsets picks the groups a session needs, and
// --allow-tools / --deny-tools narrow the set further.
package tools

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	mcpregistry "github.com/katbyte/go-kt/mcp/registry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options controls which tools are registered.
type Options struct {
	// ReadOnly registers only tools that never change server state.
	ReadOnly bool
	// EnableDelete registers the tools that delete: item_delete (the media
	// file), item_orphans_delete, library_delete, playlist_delete and
	// collection_delete. Off unless the operator opts in.
	EnableDelete bool
	// Toolsets, when set, restricts registration to the named groups (see
	// Toolsets). "core" is always included, so a set can be asked for on its
	// own. Allow and Deny narrow whatever is left.
	Toolsets []string
	// Allow, when set, restricts registration to matching tools: exact names,
	// prefix/suffix globs (library_*, *_delete) or the "essential" preset.
	// With no Toolsets it chooses from every tool; beside Toolsets, a name
	// or pattern reaching no tool the sets hold is refused rather than
	// silently registering nothing.
	Allow []string
	// Deny removes matching tools from whatever Allow left.
	Deny []string
	// TMDBKey enables what needs the metadata provider's own facts:
	// audit_provider (films' ids and runtimes against TMDB's),
	// audit_missing_episodes with provider true (each series' whole run),
	// audit_file_path naming the episode TMDB gives a file's title to, and
	// show_missing's fallback for a server that keeps no record of a series'
	// run. Empty disables them.
	TMDBKey string
	// AnimeList is where audit_anime_ids reads the Anime-Lists mapping: a
	// URL, or a file on disk. Empty is the list its maintainers publish.
	AnimeList string
	// ProviderTransport, when set, carries the calls embyfin-mcp itself
	// makes to metadata providers (TMDB). The tests point it at a
	// record/replay proxy; nil is the default transport.
	ProviderTransport http.RoundTripper
}

// Toolsets group the tools by the job someone is doing, so a client can load a
// working subset instead of all of them. The whole surface is several
// thousand tokens of tool definitions (name, description, input schema)
// before anyone has asked a question; core alone is a fraction of that.
//
// "all" is every tool, which is what the library does when no toolset is asked
// for; the embyfin-mcp binary defaults to core instead (see
// cli.FlagData.ToolOptions).
//
// --toolsets also takes a resource family - library, item, audit, show, user,
// person, metadata, session, playlist, collection, server, task - which is every tool with that
// prefix. Those are derived from the registered names rather than listed
// here, so they cannot go stale.
//
// Every tool belongs to exactly one set (TestToolsetsPartition proves it), and
// core is added to whatever else is asked for, because none of the other sets
// can find a library or open an item on their own.
var Toolsets = map[string][]string{
	// enough to find things and read them: the base every other set assumes
	"core": {
		"server_info", "library_list", "library_get", "library_items", "item_get", "item_find_by_metadata_id",
	},
	// find what is wrong with a library and fix it: every audit, identify
	// and refresh, the metadata, artwork and subtitle editors, and the
	// listings a curation session reads
	"curation": {
		"audit_all", "audit_missing_metadata",
		"audit_file_path", "audit_duplicates", "audit_multiple_versions", "audit_runtime",
		"audit_quality", "audit_missing_episodes", "audit_spelling", "audit_whitespace", "audit_unwatched", "audit_language", "audit_duplicate_episodes", "audit_disc_folders", "audit_previews", "audit_anime_ids", "audit_provider", "provider_cache_clear", "quality_compare", "plan_check",
		"item_identify", "item_identify_apply", "item_refresh", "item_previews_regenerate", "item_edit", "metadata_rename",
		"item_artwork", "item_artwork_set", "item_subtitle_search", "item_subtitle_download",
		"item_similar", "show_seasons", "show_episodes_exist", "show_missing", "show_resolve",
		"library_episodes", "library_export", "library_filters", "person_get",
	},
	// who watched what, and keeping watch state right: the users, their
	// history, what is next and in progress, favourites, played flags
	"watching": {
		"user_list", "user_get", "user_history", "user_next_up", "user_stats",
		"item_last_watched", "item_set_state",
		"item_instant_mix",
	},
	// group things: shared collections and per-user playlists
	"organise": {
		"collection_list", "collection_get", "collection_create", "collection_edit", "collection_delete",
		"playlist_list", "playlist_get", "playlist_create", "playlist_edit", "playlist_delete",
	},
	// the devices playing right now, and driving them
	"remote": {
		"session_list", "session_play", "session_command", "session_message",
	},
	// running the server rather than using it: statistics, activity, logs,
	// scheduled tasks, scans, libraries, what a removed library leaves behind,
	// and the tools that remove things
	"admin": {
		"server_health", "server_stats", "server_activity", "server_devices", "server_plugins", "server_log", "server_log_search", "server_config", "server_config_edit",
		"task_list", "task_get", "task_run", "task_stop", "task_edit", "library_scan", "library_options", "library_create", "library_edit", "library_delete", "item_delete", "audit_orphans", "item_orphans_delete",
	},
}

// EssentialTools is the curated preset selected by --allow-tools essential:
// enough to find things, read them, and keep watch state in sync.
var EssentialTools = []string{
	"library_list",
	"library_items",
	"item_get",
	"user_next_up",
	"item_set_state",
}

// The kinds a tool is added with, in go-kt's registry's terms: a read tool
// never changes what the server holds, a write tool does, and a delete tool
// removes what cannot be put back.
const (
	readTool   = mcpregistry.Read
	writeTool  = mcpregistry.Write
	deleteTool = mcpregistry.Delete
)

// registry is this application's tools and what they share. Which of them a
// session gets, and what each tells a client about itself, is go-kt's
// registry's work (tools), so the allow and deny patterns are checked
// against every tool before any is registered.
type registry struct {
	client *embyfin.Client
	opts   Options
	// tools holds every tool queued, made when the first is (see queued)
	tools *mcpregistry.Registry

	// the library's series, read once and shared by every call; see
	// series_index.go
	seriesOnce sync.Once
	series     *seriesCache

	// providerCaches are the TMDB answers the tools keep, every one made at
	// registration, for provider_cache_clear to forget
	cacheMu        sync.Mutex
	providerCaches []providerCache

	// errorLog is where a handler's panic is logged; nil is the process's
	// log (see logError)
	errorLog func(format string, args ...any)
	// settle is how long a tool waits between checks that a change the
	// server makes in the background has landed; zero is settleInterval.
	// The tests shorten it.
	settle time.Duration
}

// settleInterval is the wait between checks that a change has landed, as the
// client's own checks wait.
const settleInterval = 250 * time.Millisecond

// pause waits one settle interval, or until the context ends.
func (r *registry) pause(ctx context.Context) error {
	wait := r.settle
	if wait == 0 {
		wait = settleInterval
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// toolHints are what a tool tells a client about itself beyond its kind, in
// the MCP annotations' terms. A write tool is destructive when it can change
// or take away what was there - a value written over, a file deleted or
// rewritten, a watch history cleared, a scan that drops items - and additive
// when it only ever adds; idempotent when calling it again with the same
// arguments changes nothing more.
//
// Every write tool has an entry, each decided on its own rather than taken
// from the kind: marked all alike, library_edit (a folder removed drops its
// items), task_run (a task can delete files), item_artwork_set (Emby deletes
// the poster file it replaces) and item_set_state (unwatched clears play
// counts for good) read as additive to a client. A write tool missing from
// here is marked destructive, and fails TestEveryWriteToolIsHinted.
var toolHints = map[string]mcpregistry.Hints{
	// a task can delete files, rewrite lists or install an update; a scan
	// drops the items whose files are gone
	"task_run": {},
	// settings written over, the same ones again the second time
	"server_config_edit": {Idempotent: true},
	// a task stops where it is, part of its work done; asked again of a
	// task that has stopped, nothing more happens
	"task_stop": {Idempotent: true},
	// triggers written over, the same ones again the second time
	"task_edit":    {Idempotent: true},
	"library_scan": {},
	// Jellyfin saves nfos by default, over any beside the media
	"library_create": {},
	// a folder taken out drops its items; a rename gives a Jellyfin library
	// a new id
	"library_edit": {},
	// replaces metadata and images, and re-reads an nfo over edits
	"item_refresh": {},
	// a damaged file is written over, an nfo written again, an empty field
	// filled; a library's next videos are others the second time
	"item_previews_regenerate": {},
	// unwatched clears play counts and dates; a second watched mark on a
	// watched item adds no play (seen on both servers)
	"item_set_state": {Idempotent: true},
	// values written over, the same values again the second time
	"item_edit":       {Idempotent: true},
	"metadata_rename": {Idempotent: true},
	// replaces every field and image, hand edits included
	"item_identify_apply": {},
	// Emby deletes the poster file it replaces
	"item_artwork_set": {},
	// a download of the same language and format writes over the file
	"item_subtitle_download": {},
	// what the device was playing stops, and its progress is recorded
	"session_play":    {},
	"session_command": {},
	// a message shown on a device takes nothing away
	"session_message": {Additive: true},
	// the first playlist made on an Emby server starts a library scan, which
	// drops the items whose files are gone
	"playlist_create": {},
	// the first collection made on a server starts a scan of every library
	// (seen on Emby 4.11 and Jellyfin 12.1)
	"collection_create": {},
	// entries taken out, a name written over, a Jellyfin move that takes
	// entries out and puts them back; an item appended again is another
	// entry, where a collection holds an item once
	"playlist_edit":   {},
	"collection_edit": {Idempotent: true},

	// a read tool that writes a file on the machine embyfin-mcp runs on. The
	// server is only read, so it stays a read tool and a read-only session
	// keeps it, but it does not claim to change nothing: it only ever writes
	// a new file, which is additive
	"library_export": {WritesHere: true},
}

// queued is every tool queued so far, in go-kt's registry, which is made
// when the first tool is.
func (r *registry) queued() *mcpregistry.Registry {
	if r.tools == nil {
		r.tools = mcpregistry.New(mcpregistry.Config{
			Toolsets:  Toolsets,
			Essential: EssentialTools,
			Hints:     toolHints,
			LogError:  r.errorLog,
		})
	}

	return r.tools
}

// add queues a typed tool for registration. go-kt's registry sets the MCP
// annotations from kind and the tool's hints so clients can tell read-only
// from destructive tools without parsing descriptions, turns a panic in the
// handler into an ordinary tool error, and sends empty collections as []
// rather than null: an AI client reading "people": null cannot tell "none"
// from "not fetched", and Go leaves un-appended slices nil. What is this
// application's own is done around the handler here.
func add[In, Out any](r *registry, kind mcpregistry.Kind, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	mcpregistry.Add(r.queued(), kind, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		// the account a list is read whole in is chosen again for each call:
		// one narrowed since the last would read short without a word
		r.client.ForgetFullViewer()
		// anything that changes the server may have added, renamed or removed
		// a series, so the index is read again rather than trusted. Done on a
		// failure too, and on a panic: a write that failed part way may still
		// have landed.
		if kind != readTool {
			defer r.seriesCache().invalidate()
		}

		return h(ctx, req, in)
	})
}

// RegisterAll adds every tool permitted by opts to the MCP server and returns
// the names registered. It fails when an allow/deny pattern matches no tool,
// so a typo cannot silently hide one.
func RegisterAll(server *mcp.Server, client *embyfin.Client, opts Options) ([]string, error) {
	r := &registry{client: client, opts: opts}
	queueTools(r)

	return r.queued().Register(server, opts.selection())
}

// selection is the part of the options that chooses tools, as go-kt's
// registry takes it.
func (o *Options) selection() mcpregistry.Selection {
	return mcpregistry.Selection{
		ReadOnly:     o.ReadOnly,
		EnableDelete: o.EnableDelete,
		Toolsets:     o.Toolsets,
		Allow:        o.Allow,
		Deny:         o.Deny,
	}
}

// queueTools queues every tool, before any filtering.
func queueTools(r *registry) {
	registerServerTools(r)
	registerLogSearchTool(r)
	registerTaskTools(r)
	registerConfigTools(r)
	registerPluginsTool(r)
	registerHealthTool(r)
	registerLibraryOptionsTool(r)
	registerLibraryTools(r)
	registerEpisodeTools(r)
	registerQualityTools(r)
	registerAuditTools(r)
	registerLanguageAudit(r)
	registerDuplicateEpisodesAudit(r)
	registerFilePathAudit(r)
	registerDiscAudit(r)
	registerPreviewTools(r)
	registerAnimeAudit(r)
	registerProviderCheckAudit(r)
	registerProviderCacheTool(r)
	registerExportTool(r)
	registerPlanTools(r)
	registerOrphanTools(r)
	registerSpellingTools(r)
	registerWhitespaceAudit(r)
	registerMediaAudits(r)
	registerItemTools(r)
	registerItemEditTools(r)
	registerIdentifyTools(r)
	registerArtworkTools(r)
	registerSubtitleTools(r)
	registerShowTools(r)
	registerResolveTools(r)
	registerUserTools(r)
	registerUserDetailTools(r)
	registerPersonTools(r)
	registerSessionTools(r)
	registerPlaylistTools(r)
	registerCollectionTools(r)
}

// ToolInfo describes a registered tool without a server to register it on.
type ToolInfo = mcpregistry.Info

// described is every tool queued with no server behind it, for the answers
// that need none: registration never calls the client, only the handlers
// do.
func described(opts Options) (*mcpregistry.Registry, error) {
	client, err := embyfin.New(embyfin.Emby, "https://describe.invalid", "describe")
	if err != nil {
		return nil, err
	}
	r := &registry{client: client, opts: opts}
	queueTools(r)

	return r.queued(), nil
}

// Describe lists the tools opts would register, for `embyfin-mcp tools`. It
// needs no connectivity, and makes the same choice RegisterAll does.
func Describe(opts Options) ([]ToolInfo, error) {
	reg, err := described(opts)
	if err != nil {
		return nil, err
	}

	return reg.Describe(opts.selection())
}

// ToolsetNames lists the curated toolsets, for help output.
func ToolsetNames() []string {
	return mcpregistry.New(mcpregistry.Config{Toolsets: Toolsets}).ToolsetNames()
}

// FamilyNames lists the resource prefixes accepted by --toolsets, for help
// output.
func FamilyNames() []string {
	reg, err := described(Options{})
	if err != nil {
		return nil
	}

	return reg.FamilyNames()
}

// typeMovie is the MediaBrowser item type for films.
const (
	typeMovie   = "Movie"
	typeEpisode = "Episode"
)

// sortDescending is the MediaBrowser SortOrder for newest/most-recent first.
const sortDescending = "Descending"

// daysCutoff converts a days-back input (default 60) to the cutoff time.
func daysCutoff(days int) time.Time {
	if days <= 0 {
		days = 60
	}

	return time.Now().AddDate(0, 0, -days)
}

// afterCutoff reports whether a server timestamp (RFC3339) is at or after
// the cutoff. An item with none is dated nothing, so before it; one that
// can't be read is an error, since either answer would be a guess.
func afterCutoff(it *embyfin.Item, cutoff time.Time) (bool, error) {
	if it.DateCreated == "" {
		return false, nil
	}
	t, err := time.Parse(time.RFC3339, it.DateCreated)
	if err != nil {
		return false, fmt.Errorf("the server says %s (id %s) was added %q, which can't be read as a time", it.Name, it.ID, it.DateCreated)
	}

	return !t.Before(cutoff), nil
}
