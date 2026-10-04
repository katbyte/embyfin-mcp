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
	"cmp"
	"context"
	"fmt"
	"net/http"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/go-kt/clog"
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
		"audit_quality", "audit_missing_episodes", "audit_spelling", "audit_whitespace", "audit_unwatched", "audit_language", "audit_duplicate_episodes", "audit_disc_folders", "audit_anime_ids", "audit_provider", "provider_cache_clear", "quality_compare", "plan_check",
		"item_identify", "item_identify_apply", "item_refresh", "item_edit", "metadata_rename",
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
		"server_stats", "server_activity", "server_devices", "server_log",
		"task_list", "task_run", "library_scan", "library_create", "library_edit", "library_delete", "item_delete", "audit_orphans", "item_orphans_delete",
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

type toolKind int

const (
	readTool toolKind = iota
	writeTool
	deleteTool
)

type pending struct {
	name        string
	kind        toolKind
	description string
	register    func()
}

// registry collects tool registrations so the allow/deny patterns can be
// validated against the full tool list before anything is added.
type registry struct {
	server  *mcp.Server
	client  *embyfin.Client
	opts    Options
	pending []pending

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

// hints are what a tool that changes something tells a client about the
// change, beyond its kind, in the MCP annotations' terms: destructive when it
// can change or take away what was there - a value written over, a file
// deleted or rewritten, a watch history cleared, a scan that drops items -
// rather than only add; idempotent when calling it again with the same
// arguments changes nothing more.
type hints struct{ destructive, idempotent bool }

// changeHints are every write tool's hints, each decided on its own rather
// than taken from the kind: marked all alike, library_edit (a folder removed
// drops its items), task_run (a task can delete files), item_artwork_set
// (Emby deletes the poster file it replaces) and item_set_state (unwatched
// clears play counts for good) read as additive to a client. A write tool
// missing from here is marked destructive, and fails
// TestEveryWriteToolIsHinted.
var changeHints = map[string]hints{
	// a task can delete files, rewrite lists or install an update; a scan
	// drops the items whose files are gone
	"task_run":     {destructive: true},
	"library_scan": {destructive: true},
	// Jellyfin saves nfos by default, over any beside the media
	"library_create": {destructive: true},
	// a folder taken out drops its items; a rename gives a Jellyfin library
	// a new id
	"library_edit": {destructive: true},
	// replaces metadata and images, and re-reads an nfo over edits
	"item_refresh": {destructive: true},
	// unwatched clears play counts and dates; a second watched mark on a
	// watched item adds no play (seen on both servers)
	"item_set_state": {destructive: true, idempotent: true},
	// values written over, the same values again the second time
	"item_edit":       {destructive: true, idempotent: true},
	"metadata_rename": {destructive: true, idempotent: true},
	// replaces every field and image, hand edits included
	"item_identify_apply": {destructive: true},
	// Emby deletes the poster file it replaces
	"item_artwork_set": {destructive: true},
	// a download of the same language and format writes over the file
	"item_subtitle_download": {destructive: true},
	// what the device was playing stops, and its progress is recorded
	"session_play":    {destructive: true},
	"session_command": {destructive: true},
	"session_message": {},
	// the first playlist made on an Emby server starts a library scan, which
	// drops the items whose files are gone
	"playlist_create": {destructive: true},
	// the first collection made on a server starts a scan of every library
	// (seen on Emby 4.11 and Jellyfin 12.1)
	"collection_create": {destructive: true},
	// entries taken out, a name written over, a Jellyfin move that takes
	// entries out and puts them back; an item appended again is another
	// entry, where a collection holds an item once
	"playlist_edit":   {destructive: true},
	"collection_edit": {destructive: true, idempotent: true},
}

// hostWriters are the read tools that write a file on the machine
// embyfin-mcp runs on. The server is only read, so they stay read tools and a
// read-only session keeps them, but they do not claim to change nothing: each
// only ever writes a new file, which is additive.
var hostWriters = map[string]bool{
	"library_export": true,
}

// add queues a typed tool for registration. It sets the MCP annotations from
// kind and the tool's hints so clients can tell read-only from destructive
// tools without parsing descriptions, and normalises the output so that empty
// collections serialise as [] rather than null: an AI client reading
// "people": null cannot tell "none" from "not fetched", and Go leaves
// un-appended slices nil.
func add[In, Out any](r *registry, kind toolKind, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	f := false
	switch kind {
	case readTool:
		t.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: !hostWriters[t.Name], DestructiveHint: &f, OpenWorldHint: &f}
	case writeTool:
		hint, known := changeHints[t.Name]
		if !known {
			// the answer that warns, until the tool is given its own
			hint = hints{destructive: true}
		}
		t.Annotations = &mcp.ToolAnnotations{DestructiveHint: new(hint.destructive), IdempotentHint: hint.idempotent, OpenWorldHint: &f}
	case deleteTool:
		t.Annotations = &mcp.ToolAnnotations{DestructiveHint: new(true), OpenWorldHint: &f}
	}

	wrapped := func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		// the account a list is read whole in is chosen again for each call:
		// one narrowed since the last would read short without a word
		r.client.ForgetFullViewer()
		res, out, err := recovered(ctx, r, t.Name, h, req, in)
		if err == nil {
			emptyNilSlices(reflect.ValueOf(&out).Elem())
		}
		// anything that changes the server may have added, renamed or removed
		// a series, so the index is read again rather than trusted. Done on a
		// failure too: a write that errored part way may still have landed.
		if kind != readTool {
			r.seriesCache().invalidate()
		}

		return res, out, err
	}

	r.pending = append(r.pending, pending{
		name:        t.Name,
		kind:        kind,
		description: t.Description,
		register:    func() { mcp.AddTool(r.server, t, wrapped) },
	})
}

// recovered calls a handler, turning a panic into an ordinary tool error.
// Nothing above the handler recovers one - not the MCP SDK, not the CLI - so
// one nil dereference in one tool would otherwise end the whole session. The
// caller is told which tool failed; the stack goes to the log, which writes
// to stderr, because in stdio mode stdout carries the protocol itself.
func recovered[In, Out any](ctx context.Context, r *registry, name string, h mcp.ToolHandlerFor[In, Out], req *mcp.CallToolRequest, in In) (res *mcp.CallToolResult, out Out, err error) {
	defer func() {
		if p := recover(); p != nil {
			r.logError("internal error in %s: %v\n%s", name, p, debug.Stack())
			var zero Out
			res, out, err = nil, zero, fmt.Errorf("internal error in %s: %v", name, p)
		}
	}()

	return h(ctx, req, in)
}

// logError writes to the registry's log: the process's own, which writes to
// stderr, unless a test gave it another.
func (r *registry) logError(format string, args ...any) {
	if r.errorLog != nil {
		r.errorLog(format, args...)
		return
	}
	clog.Log.Errorf(format, args...)
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

// RegisterAll adds every tool permitted by opts to the MCP server and returns
// the names registered. It fails when an allow/deny pattern matches no tool,
// so a typo cannot silently hide one.
func RegisterAll(server *mcp.Server, client *embyfin.Client, opts Options) ([]string, error) {
	r := &registry{server: server, client: client, opts: opts}
	queueTools(r)

	keep, err := selected(r, opts)
	if err != nil {
		return nil, err
	}

	var registered []string
	for _, p := range r.pending {
		if !keep[p.name] {
			continue
		}
		p.register()
		registered = append(registered, p.name)
	}
	slices.Sort(registered)

	return registered, nil
}

// queueTools queues every tool, before any filtering.
func queueTools(r *registry) {
	registerServerTools(r)
	registerLibraryTools(r)
	registerEpisodeTools(r)
	registerQualityTools(r)
	registerAuditTools(r)
	registerLanguageAudit(r)
	registerDuplicateEpisodesAudit(r)
	registerFilePathAudit(r)
	registerDiscAudit(r)
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

// names lists the queued tools in registration order.
func (r *registry) names() []string {
	out := make([]string, 0, len(r.pending))
	for _, p := range r.pending {
		out = append(out, p.name)
	}

	return out
}

// selected applies the kind gates and the toolset/allow/deny filters, and is
// shared by RegisterAll and Describe so `embyfin-mcp tools` cannot drift from
// what the server actually registers.
func selected(r *registry, opts Options) (map[string]bool, error) {
	names := r.names()
	sets, err := compileToolsets(opts.Toolsets, names)
	if err != nil {
		return nil, err
	}
	allow, err := compilePatterns(opts.Allow, names, "allow")
	if err != nil {
		return nil, err
	}
	deny, err := compilePatterns(opts.Deny, names, "deny")
	if err != nil {
		return nil, err
	}
	if len(sets) > 0 {
		if err := allowedWithin(opts.Allow, sets, opts.Toolsets, names); err != nil {
			return nil, err
		}
	}

	keep := make(map[string]bool, len(r.pending))
	for _, p := range r.pending {
		switch {
		case p.kind == deleteTool && !opts.EnableDelete:
		case p.kind != readTool && opts.ReadOnly:
		case len(sets) > 0 && !sets[p.name]:
		case len(allow) > 0 && !matchesAny(allow, p.name):
		case matchesAny(deny, p.name):
		default:
			keep[p.name] = true
		}
	}

	return keep, nil
}

// compileToolsets turns the requested set names into the tools they hold,
// always including core. An unknown name aborts startup naming the valid ones,
// the way an allow/deny pattern that matches nothing does.
func compileToolsets(raw, known []string) (map[string]bool, error) {
	var asked []string
	for _, entry := range raw {
		for name := range strings.SplitSeq(entry, ",") {
			if name = strings.TrimSpace(name); name != "" {
				asked = append(asked, name)
			}
		}
	}
	if len(asked) == 0 {
		return nil, nil
	}

	out := map[string]bool{}
	for _, name := range asked {
		if name == "all" {
			for _, t := range known {
				out[t] = true
			}
			continue
		}
		if tools, ok := Toolsets[name]; ok {
			for _, t := range tools {
				out[t] = true
			}
			continue
		}
		// not a named set: a resource family, every tool with that prefix
		found := false
		for _, t := range known {
			if strings.HasPrefix(t, name+"_") {
				out[t], found = true, true
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown toolset %q (sets: all, %s; or a resource family: %s)",
				name, strings.Join(setNames(), ", "), strings.Join(resourceFamilies(known), ", "))
		}
	}
	// core is what every other set assumes: without it there is no way to find
	// a library or open an item
	for _, t := range Toolsets["core"] {
		out[t] = true
	}

	return out, nil
}

// setNames lists the curated toolsets, sorted.
func setNames() []string {
	out := make([]string, 0, len(Toolsets))
	for k := range Toolsets {
		out = append(out, k)
	}
	slices.Sort(out)

	return out
}

// resourceFamilies lists the resource prefixes in use, sorted.
func resourceFamilies(known []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range known {
		if i := strings.Index(t, "_"); i > 0 && !seen[t[:i]] {
			seen[t[:i]] = true
			out = append(out, t[:i])
		}
	}
	slices.Sort(out)

	return out
}

// compilePatterns expands the essential preset, splits comma-separated
// entries, and checks that every pattern matches at least one known tool.
func compilePatterns(raw, known []string, which string) ([]string, error) {
	var out []string
	for _, entry := range raw {
		for pat := range strings.SplitSeq(entry, ",") {
			pat = strings.TrimSpace(pat)
			if pat == "" {
				continue
			}
			if pat == "essential" {
				out = append(out, EssentialTools...)
				continue
			}
			if !slices.ContainsFunc(known, func(n string) bool { return matchPattern(pat, n) }) {
				return nil, fmt.Errorf("%s-tools pattern %q matches no tool (have: %s)", which, pat, strings.Join(known, ", "))
			}
			out = append(out, pat)
		}
	}

	return out, nil
}

// allowedWithin checks an allow list against the toolsets asked for beside
// it, which it narrows: a tool it names that none of the sets holds would
// not be registered, and silently leaving it out read as the tool not
// existing. --allow-tools essential with the default core registered three
// of its five. So each name or pattern must reach a tool in the sets, and
// the error says which set to add.
func allowedWithin(raw []string, sets map[string]bool, asked, known []string) error {
	in := func(name string) bool { return sets[name] }
	for _, entry := range raw {
		for pat := range strings.SplitSeq(entry, ",") {
			pat = strings.TrimSpace(pat)
			if pat == "" {
				continue
			}
			var outside []string
			switch {
			case pat == "essential":
				outside = slices.DeleteFunc(slices.Clone(EssentialTools), in)
			case !slices.ContainsFunc(known, func(n string) bool { return sets[n] && matchPattern(pat, n) }):
				outside = slices.DeleteFunc(slices.Clone(known), func(n string) bool { return !matchPattern(pat, n) })
				slices.Sort(outside)
			}
			if len(outside) == 0 {
				continue
			}
			homes := []string{}
			for _, name := range outside {
				if set := toolsetOf(name); set != "" && !slices.Contains(homes, set) {
					homes = append(homes, set)
				}
			}
			slices.Sort(homes)
			hint := "leave --toolsets out, so the allow list chooses from every tool"
			if len(homes) > 0 {
				hint = "add " + strings.Join(homes, ",") + " to --toolsets, or " + hint
			}

			return fmt.Errorf("allow-tools %q names %s, which the toolsets asked for (%s) do not hold: %s", pat, strings.Join(outside, ", "), strings.Join(asked, ","), hint)
		}
	}

	return nil
}

// toolsetOf is the curated set a tool belongs to, or "" for a name or a
// pattern no set holds.
func toolsetOf(name string) string {
	for set, members := range Toolsets {
		if slices.Contains(members, name) {
			return set
		}
	}

	return ""
}

func matchesAny(patterns []string, name string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return matchPattern(p, name) })
}

// matchPattern supports exact names plus a single leading or trailing '*'.
func matchPattern(pattern, name string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	case strings.HasPrefix(pattern, "*"):
		return strings.HasSuffix(name, strings.TrimPrefix(pattern, "*"))
	default:
		return pattern == name
	}
}

// ToolInfo describes a registered tool without a server to register it on.
type ToolInfo struct {
	Name        string
	Kind        string // read, write or delete
	Toolset     string // the curated set it belongs to
	Description string
}

// Describe lists the tools opts would register, for `embyfin-mcp tools`. It
// needs no connectivity: registration never calls the client, only the
// handlers do.
func Describe(opts Options) ([]ToolInfo, error) {
	client, err := embyfin.New(embyfin.Emby, "https://describe.invalid", "describe")
	if err != nil {
		return nil, err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "embyfin-mcp", Version: "describe"}, nil)

	r := &registry{server: server, client: client, opts: opts}
	queueTools(r)

	keep, err := selected(r, opts)
	if err != nil {
		return nil, err
	}

	set := map[string]string{}
	for name, members := range Toolsets {
		for _, m := range members {
			// core wins: it is the set a tool is reached through most often
			if set[m] == "" || name == "core" {
				set[m] = name
			}
		}
	}

	kinds := map[toolKind]string{readTool: "read", writeTool: "write", deleteTool: "delete"}
	out := make([]ToolInfo, 0, len(r.pending))
	for _, p := range r.pending {
		if !keep[p.name] {
			continue
		}
		out = append(out, ToolInfo{Name: p.name, Kind: kinds[p.kind], Toolset: set[p.name], Description: p.description})
	}
	slices.SortFunc(out, func(a, b ToolInfo) int { return cmp.Compare(a.Name, b.Name) })

	return out, nil
}

// ToolsetNames lists the curated toolsets, for help output.
func ToolsetNames() []string { return setNames() }

// FamilyNames lists the resource prefixes accepted by --toolsets, for help
// output.
func FamilyNames() []string {
	r := &registry{}
	queueTools(r)

	return resourceFamilies(r.names())
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
