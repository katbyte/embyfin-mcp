package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// auditIn is shared by all audit sweeps.
type auditIn struct {
	Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
	Types   string `json:"types,omitempty"   jsonschema:"comma-separated item types to audit; defaults to Movie,Series"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum findings to return, default 100"`
}

type auditFinding struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Year   int    `json:"year,omitempty"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type auditOut struct {
	Scanned  int            `json:"items_scanned"`
	Found    int            `json:"total_findings"`
	Findings []auditFinding `json:"findings"       jsonschema:"capped at limit; total_findings is the real count"`
}

// runAudit sweeps matching items and collects findings from check. check
// returns (finding detail, true) when the item is suspect. skip, when set,
// leaves an item out before it is counted as scanned.
func runAudit(ctx context.Context, client *embyfin.Client, in auditIn, fields string, skip func(*embyfin.Item) bool, check func(*embyfin.Item) (string, bool)) (auditOut, error) {
	types := in.Types
	if types == "" {
		types = "Movie,Series"
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}

	opts := embyfin.SearchOptions{IncludeItemTypes: types, Fields: fields}

	folder, err := resolveLibrary(ctx, client, in.Library)
	if err != nil {
		return auditOut{}, err
	}
	if folder != nil {
		opts.ParentID = folder.ItemID
	}

	out := auditOut{Findings: []auditFinding{}}
	sweepErr := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
		for i := range items {
			if skip != nil && skip(&items[i]) {
				continue
			}
			out.Scanned++
			detail, suspect := check(&items[i])
			if !suspect {
				continue
			}

			out.Found++
			if len(out.Findings) < limit {
				out.Findings = append(out.Findings, auditFinding{
					ID:     items[i].ID,
					Name:   items[i].Name,
					Year:   items[i].ProductionYear,
					Path:   items[i].Path,
					Detail: detail,
				})
			}
		}
		return true
	})
	if sweepErr != nil {
		return auditOut{}, sweepErr
	}

	return out, nil
}

// auditCheck is one per-item audit: a sweep over the library applying check
// to every item of the default types, and a finding for each that fails.
// The table drives both the individual audit_* tools and audit_all.
type auditCheck struct {
	name        string
	description string
	fields      string // the item fields the check needs, on top of the lean set
	types       string // default item types, "" for Movie,Series
	check       func(*embyfin.Item) (string, bool)
	// tool, when set, registers the audit's tool in place of the shared one,
	// for an audit with options of its own; audit_all still runs check
	tool func(r *registry, c auditCheck)
}

// auditChecks are the per-item audits, in the order audit_all reports them.
var auditChecks = []auditCheck{
	{
		name: "audit_missing_metadata_provider",
		description: "Sweep the library for items with no metadata provider id of any kind (a link to its website or social pages is not one): unmatched items that need identification (item_identify). " +
			"missing drills down to the providers named, so missing=tmdb also finds items matched elsewhere but not on TMDB; each finding lists the ids the item does have.",
		fields: embyfin.FieldsLean,
		check:  noProviderID,
		tool:   registerProviderAudit,
	},
	{
		name:        "audit_missing_poster",
		description: "Sweep the library for items with no primary poster image (item_artwork_set fixes them).",
		fields:      embyfin.FieldsLean + ",ImageTags",
		check: func(it *embyfin.Item) (string, bool) {
			if it.ImageTags["Primary"] == "" {
				return "no primary image", true
			}
			return "", false
		},
	},
	{
		name:        "audit_missing_overview",
		description: "Sweep the library for items with no overview/plot text, usually a sign of a failed metadata match (item_refresh or item_identify fixes them where the library's metadata fetchers are on; item_edit sets one by hand).",
		fields:      embyfin.FieldsLean,
		check: func(it *embyfin.Item) (string, bool) {
			if strings.TrimSpace(it.Overview) == "" {
				return "no overview", true
			}
			return "", false
		},
	},
	{
		name:        "audit_multiple_versions",
		description: "Sweep the library for entries the server has merged into several versions (more than one media file under one item, e.g. a 4K and a 1080p copy; Jellyfin merges same-folder versions at scan time, Emby only when merged in its web client). Separate entries for the same title show up in audit_duplicates instead. Defaults to Movie,Episode.",
		fields:      "Path,ProviderIds,ProductionYear,MediaSources",
		types:       "Movie,Episode",
		check: func(it *embyfin.Item) (string, bool) {
			if len(it.MediaSources) < 2 {
				return "", false
			}
			names := make([]string, 0, len(it.MediaSources))
			for _, s := range it.MediaSources {
				names = append(names, path.Base(s.Path))
			}
			return strconv.Itoa(len(it.MediaSources)) + " versions: " + strings.Join(names, ", "), true
		},
	},
}

// metadataProviders are the providers missing can name, in the order a
// finding lists the ids an item does have. AniDB and MyAnimeList are where an
// anime library matches, often with none of the other three.
var metadataProviders = []string{"tmdb", "imdb", "tvdb", "anidb", "myanimelist"}

// providerLinks are what a server keeps beside an item's provider ids that
// are not ids: links to its pages elsewhere, which a provider passes along
// with a match. An item holding only these matches nothing.
var providerLinks = []string{"official website", "fan site", "facebook", "instagram", "twitter", "x (twitter)", "reddit", "youtube", "wikipedia"}

// noProviderID flags an item with no provider id of any kind. Whichever
// provider matched an item, it was matched, so this is the default.
func noProviderID(it *embyfin.Item) (string, bool) {
	links := false
	for key, id := range it.ProviderIDs {
		switch {
		case id == "":
		case slices.Contains(providerLinks, strings.ToLower(key)):
			links = true
		default:
			return "", false
		}
	}
	if links {
		return "no provider id, only links to its pages", true
	}

	return "no provider id", true
}

// missingProviders flags an item holding none of the providers named: tmdb
// alone finds one matched elsewhere but not on TMDB. A finding names the ids
// the item does have, which are what find it on the provider it lacks.
func missingProviders(missing []string) func(*embyfin.Item) (string, bool) {
	return func(it *embyfin.Item) (string, bool) {
		for _, p := range missing {
			if providerID(it, p) != "" {
				return "", false
			}
		}

		detail := "no " + strings.Join(missing, "/") + " id"
		has := make([]string, 0, len(metadataProviders))
		for _, p := range metadataProviders {
			if id := providerID(it, p); id != "" {
				has = append(has, p+":"+id)
			}
		}
		if len(has) > 0 {
			detail += "; has " + strings.Join(has, " ")
		}

		return detail, true
	}
}

// parseProviders reads the providers a caller named, comma-separated and in
// any case, in metadataProviders order. None named is nil.
func parseProviders(s string) ([]string, error) {
	named := map[string]bool{}
	for p := range strings.SplitSeq(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if !slices.Contains(metadataProviders, p) {
			return nil, fmt.Errorf("unknown provider %q; choose from: %s", p, strings.Join(metadataProviders, ", "))
		}
		named[p] = true
	}
	if len(named) == 0 {
		return nil, nil
	}

	return slices.DeleteFunc(slices.Clone(metadataProviders), func(p string) bool { return !named[p] }), nil
}

// auditCheckByName finds a table entry, for tests and for audit_all.
func auditCheckByName(name string) *auditCheck {
	for i := range auditChecks {
		if auditChecks[i].name == name {
			return &auditChecks[i]
		}
	}

	return nil
}

func registerAuditTools(r *registry) {
	client := r.client
	for i := range auditChecks {
		c := auditChecks[i]
		if c.tool != nil {
			c.tool(r, c)

			continue
		}
		add(r, readTool, &mcp.Tool{
			Name:        c.name,
			Description: c.description,
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
			if in.Types == "" {
				in.Types = c.types
			}
			out, err := runAudit(ctx, client, in, c.fields, nil, c.check)

			return nil, out, err
		})
	}

	type dupOut struct {
		Scanned     int             `json:"items_scanned"`
		TotalGroups int             `json:"total_groups"`
		Groups      [][]itemSummary `json:"duplicate_groups" jsonschema:"each group shares one metadata provider id; capped at limit, total_groups is the real count"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_duplicates",
		Description: "Find separate entries sharing the same tmdb/imdb/tvdb id: multiple copies of one film, series or episode, in one library or across libraries (compare the paths). " +
			"Episodes are grouped by provider id AND season and episode number, because a library can carry one shared id across unrelated episodes. Default limit 50 groups.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, dupOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		groups, scanned, err := duplicateGroups(ctx, client, in)
		if err != nil {
			return nil, dupOut{}, err
		}

		out := dupOut{Scanned: scanned, TotalGroups: len(groups)}
		if len(groups) > limit {
			groups = groups[:limit]
		}
		for _, g := range groups {
			out.Groups = append(out.Groups, summariseAll(g))
		}

		return nil, out, nil
	})

	registerRuntimeAudit(r)
	registerAuditAll(r)
}

// registerProviderAudit adds audit_missing_metadata_provider, which takes the
// providers to look for, and the libraries to leave out, on top of the
// options every audit shares.
func registerProviderAudit(r *registry, c auditCheck) {
	client := r.client

	type providerIn struct {
		auditIn
		Missing string   `json:"missing,omitempty" jsonschema:"comma-separated providers the item has none of: tmdb, imdb, tvdb, anidb, myanimelist. Default: no provider id of any kind"`
		Ignore  []string `json:"ignore,omitempty"  jsonschema:"libraries to leave out, by name or id: ones whose items never carry an id, such as a YouTube library"`
	}

	add(r, readTool, &mcp.Tool{
		Name:        c.name,
		Description: c.description,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in providerIn) (*mcp.CallToolResult, auditOut, error) {
		missing, err := parseProviders(in.Missing)
		if err != nil {
			return nil, auditOut{}, err
		}
		ignored, err := libraryFolders(ctx, client, in.Ignore)
		if err != nil {
			return nil, auditOut{}, err
		}
		var skip func(*embyfin.Item) bool
		if len(ignored) > 0 {
			skip = func(it *embyfin.Item) bool {
				_, under := inLibrary(it.Path, ignored)

				return under
			}
		}
		if in.Types == "" {
			in.Types = c.types
		}
		check := c.check
		if len(missing) > 0 {
			check = missingProviders(missing)
		}
		out, err := runAudit(ctx, client, in.auditIn, c.fields, skip, check)

		return nil, out, err
	})
}

// libraryFolders are the folders of the libraries named, by name or id. A
// name that matches no library is refused: leaving out nothing, when the
// caller meant to leave something out, reads as a finding.
func libraryFolders(ctx context.Context, client *embyfin.Client, names []string) ([]libraryPath, error) {
	var folders []libraryPath
	for _, name := range names {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		folder, err := resolveLibrary(ctx, client, name)
		if err != nil {
			return nil, err
		}
		for _, loc := range folder.Locations {
			if loc = trimSep(loc); loc != "" {
				folders = append(folders, libraryPath{library: folder.Name, path: loc})
			}
		}
	}

	return folders, nil
}

// duplicateGroups sweeps the library and groups items by shared tmdb/imdb id,
// in a deterministic order so a limit pages the same way each run.
func duplicateGroups(ctx context.Context, client *embyfin.Client, in auditIn) ([][]embyfin.Item, int, error) {
	types := in.Types
	if types == "" {
		// episodes too: a library holding one episode twice is the common
		// shape, and audit_all reports this audit's count, so the two have to
		// sweep the same things or the overview names a number the audit
		// cannot reproduce
		types = "Movie,Series,Episode"
	}
	opts := embyfin.SearchOptions{IncludeItemTypes: types}

	folder, err := resolveLibrary(ctx, client, in.Library)
	if err != nil {
		return nil, 0, err
	}
	if folder != nil {
		opts.ParentID = folder.ItemID
	}

	var all []embyfin.Item
	sweepErr := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
		all = append(all, items...)
		return true
	})
	if sweepErr != nil {
		return nil, 0, sweepErr
	}

	return groupByProviderID(all), len(all), nil
}

// groupByProviderID groups items sharing a tmdb, imdb or tvdb id. An item
// carrying two ids joins the entries sharing either, so one group holds every
// copy of a film however each copy is matched, rather than the copies being
// split by which id they happen to share.
func groupByProviderID(items []embyfin.Item) [][]embyfin.Item {
	// union-find over item positions, joined by each provider key
	parent := make([]int, len(items))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	firstWith := map[string]int{}
	for i, it := range items {
		for k, v := range it.ProviderIDs {
			lk := strings.ToLower(k)
			if (lk != "tmdb" && lk != "imdb" && lk != "tvdb") || v == "" {
				continue
			}
			// an episode's provider id is shared far more loosely than a
			// film's: a library can carry one imdb id on many unrelated
			// episodes, even across different shows. Its season and episode
			// number go into the key, so a group is at worst the same
			// episode of the same show.
			key := lk + ":" + v
			if it.Type == typeEpisode {
				key = fmt.Sprintf("%s:s%02de%02d", key, it.ParentIndexNumber, it.IndexNumber)
			}
			if j, ok := firstWith[key]; ok {
				parent[find(i)] = find(j)
			} else {
				firstWith[key] = i
			}
		}
	}

	byRoot := map[int][]embyfin.Item{}
	var roots []int
	for i, it := range items {
		r := find(i)
		if byRoot[r] == nil {
			roots = append(roots, r)
		}
		byRoot[r] = append(byRoot[r], it)
	}
	var groups [][]embyfin.Item
	for _, r := range roots {
		if len(byRoot[r]) >= 2 {
			groups = append(groups, byRoot[r])
		}
	}
	slices.SortFunc(groups, func(a, b []embyfin.Item) int {
		if c := strings.Compare(a[0].Name, b[0].Name); c != 0 {
			return c
		}
		return strings.Compare(a[0].ID, b[0].ID)
	})

	return groups
}

// audit_all --------------------------------------------------------------

type auditAllIn struct {
	Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
}

type auditAllRow struct {
	Audit    string `json:"audit"`
	Findings int    `json:"findings"`
	Scanned  int    `json:"items_scanned"`
	Skipped  bool   `json:"skipped,omitempty" jsonschema:"true when the audit was not run here; note says why"`
	Note     string `json:"note,omitempty"    jsonschema:"why an audit was skipped, or what its count means"`
}

type auditAllOut struct {
	Audits []auditAllRow `json:"audits"`
	Total  int           `json:"total_findings" jsonschema:"the defects found, over every row but audit_unwatched, which is about viewing rather than a fault"`
}

// registerAuditAll adds the one-call overview: every audit, counts only, so a
// session starts with a picture of where a library needs work and then
// calls the audit that matters for its worklist.
func registerAuditAll(r *registry) {
	client := r.client
	add(r, readTool, &mcp.Tool{
		Name: "audit_all",
		Description: "Run every audit and report only the counts, so one call says where a library needs work; start here, then call the audit whose count is not zero for its worklist. " +
			"Every audit has a row, the orphans check included when no library is given (it is server-wide). The ones that need more than the server are listed as skipped with why: audit_language needs a language to ask about, audit_movie_ids asks TMDB one film at a time and is paged, and audit_anime_ids reads the Anime-Lists file; audit_runtime here is the episode pass, the movie pass being TMDB's.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditAllIn) (*mcp.CallToolResult, auditAllOut, error) {
		out := auditAllOut{Audits: []auditAllRow{}}
		add := func(row auditAllRow) {
			out.Audits = append(out.Audits, row)
			out.Total += row.Findings
		}
		skip := func(audit, why string) {
			out.Audits = append(out.Audits, auditAllRow{Audit: audit, Skipped: true, Note: why})
		}
		fail := func(audit string, err error) (*mcp.CallToolResult, auditAllOut, error) {
			return nil, auditAllOut{}, fmt.Errorf("%s: %w", audit, err)
		}

		for i := range auditChecks {
			c := &auditChecks[i]
			res, err := runAudit(ctx, client, auditIn{Library: in.Library, Types: c.types, Limit: 1}, c.fields, nil, c.check)
			if err != nil {
				return fail(c.name, err)
			}
			add(auditAllRow{Audit: c.name, Findings: res.Found, Scanned: res.Scanned})
		}

		paths, err := auditFilePath(ctx, client, nil, pathIn{Library: in.Library, Limit: 1})
		if err != nil {
			return fail("audit_file_path", err)
		}
		add(auditAllRow{Audit: "audit_file_path", Findings: paths.Found, Scanned: paths.Scanned, Note: "items whose path disagrees with their metadata: title, year, series, season or episode"})

		// films, series AND episodes: this pass used to ask for the default
		// Movie,Series, so it reported the series count as items_scanned and
		// an episode held twice was invisible from the call that says "start
		// here"
		groups, scanned, err := duplicateGroups(ctx, client, auditIn{Library: in.Library})
		if err != nil {
			return fail("audit_duplicates", err)
		}
		add(auditAllRow{Audit: "audit_duplicates", Findings: len(groups), Scanned: scanned, Note: "groups of entries sharing a provider id, films series and episodes"})

		titles, err := auditDuplicateTitles(ctx, client, dupTitlesIn{Library: in.Library, Limit: 1})
		if err != nil {
			return fail("audit_duplicate_titles", err)
		}
		add(auditAllRow{Audit: "audit_duplicate_titles", Findings: titles.Found, Scanned: titles.Scanned, Note: "seasons holding one episode title twice"})

		folders, err := auditDuplicateSeriesFolders(ctx, client, folderIn{Library: in.Library, Limit: 1})
		if err != nil {
			return fail("audit_duplicate_series_folders", err)
		}
		add(auditAllRow{Audit: "audit_duplicate_series_folders", Findings: folders.Found, Scanned: folders.Scanned, Note: "series held twice from two folders of one name; items_scanned is series"})

		discs, err := auditDiscFolders(ctx, client, discIn{Library: in.Library, Limit: 1})
		if err != nil {
			return fail("audit_disc_folders", err)
		}
		add(auditAllRow{Audit: "audit_disc_folders", Findings: discs.Found, Scanned: discs.Scanned, Note: "folders holding a disc's streams as films"})

		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, auditAllOut{}, err
		}
		parent := ""
		if folder != nil {
			parent = folder.ItemID
		}
		eps, err := auditEpisodeRuntimes(ctx, client, parent, runtimeIn{TolerancePct: defaultRuntimeTolerancePct, Limit: 1})
		if err != nil {
			return fail("audit_runtime", err)
		}
		add(auditAllRow{Audit: "audit_runtime", Findings: eps.Found, Scanned: eps.Scanned, Note: "episodes against their season median"})

		quality, err := auditQuality(ctx, client, qualityIn{Library: in.Library, Limit: 1})
		if err != nil {
			return fail("audit_quality", err)
		}
		add(auditAllRow{Audit: "audit_quality", Findings: quality.Found, Scanned: quality.Scanned, Note: fmt.Sprintf("below 720p or in a legacy codec; %d files never probed and so not judged, %d written over since the server saw them, neither counted here", quality.TotalUnprobed, quality.TotalReplaced)})

		missing, err := auditMissingEpisodes(ctx, client, episodesIn{Library: in.Library, Limit: 1})
		if err != nil {
			return fail("audit_missing_episodes", err)
		}
		add(auditAllRow{Audit: "audit_missing_episodes", Findings: missing.Found, Scanned: missing.Scanned, Note: "series with episodes missing"})

		spellings, scannedSpelling, err := spellingAudit(ctx, client, in.Library, "", vocabFields)
		if err != nil {
			return fail("audit_spelling", err)
		}
		spellingGroups := 0
		for _, f := range vocabFields {
			spellingGroups += len(spellings.report(f))
		}
		add(auditAllRow{Audit: "audit_spelling", Findings: spellingGroups, Scanned: scannedSpelling, Note: "groups of genres, tags and studios spelled more than one way"})

		unwatched, err := auditUnwatched(ctx, client, unwatchedIn{Library: in.Library, Limit: 1})
		if err != nil {
			return fail("audit_unwatched", err)
		}
		// what nobody has watched is a row, not a defect: it is left out of
		// the total so a clean library totals zero
		out.Audits = append(out.Audits, auditAllRow{Audit: "audit_unwatched", Findings: unwatched.Found, Scanned: unwatched.Scanned, Note: "films no account has watched: not a defect, what to archive or recommend; not in total_findings"})

		if in.Library == "" {
			orphans, err := auditOrphans(ctx, client, orphansIn{Limit: 1})
			if err != nil {
				return fail("audit_orphans", err)
			}
			add(auditAllRow{Audit: "audit_orphans", Findings: orphans.Found, Scanned: orphans.Scanned, Note: "items under folders no library covers; audit_orphans lists them, in the admin toolset"})
		} else {
			skip("audit_orphans", "server-wide: run it without a library")
		}
		skip("audit_language", "needs a language to ask about")
		skip("audit_movie_ids", "asks TMDB one film at a time and is paged: run it on its own")
		skip("audit_anime_ids", "reads the Anime-Lists file from the web: run it on its own")

		return nil, out, nil
	})
}

// runtime audit -----------------------------------------------------------

const (
	defaultRuntimeTolerancePct = 20
	minRuntimeDiffMinutes      = 2
	defaultTMDBLookups         = 250
	moviePage                  = 200
)

type runtimeIn struct {
	Library      string `json:"library,omitempty"           jsonschema:"restrict to one library by name or id"`
	Types        string `json:"types,omitempty"             jsonschema:"Episode compares each episode to its season's median (no external data); Movie compares to TMDB's runtime and needs EMBYFIN_TMDB_TOKEN. Default Episode"`
	TolerancePct int    `json:"tolerance_percent,omitempty" jsonschema:"flag when the file runtime differs from the expected one by more than this percent, default 20"`
	Limit        int    `json:"limit,omitempty"             jsonschema:"maximum findings to return, default 100"`
	Offset       int    `json:"offset,omitempty"            jsonschema:"Movie only: skip this many movies, to go on from a previous call's next_offset"`
	MaxLookups   int    `json:"max_lookups,omitempty"       jsonschema:"Movie only: TMDB lookups per call, default 250"`
}

type runtimeOut struct {
	auditOut
	NextOffset int `json:"next_offset,omitempty" jsonschema:"Movie only: pass back as offset to go on; absent when the sweep finished"`
}

func registerRuntimeAudit(r *registry) {
	client := r.client
	opts := r.opts
	provider := tmdbFacts(opts)

	desc := "Find media files whose runtime disagrees with what it should be: truncated downloads, wrong files, or wrong matches. Episodes are compared to the median of their season (needs 3+ episodes); movies to TMDB's runtime"
	if provider == nil {
		desc += " (movie mode disabled: set EMBYFIN_TMDB_TOKEN to enable)"
	}
	add(r, readTool, &mcp.Tool{
		Name:        "audit_runtime",
		Description: desc + ".",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in runtimeIn) (*mcp.CallToolResult, runtimeOut, error) {
		if in.TolerancePct <= 0 {
			in.TolerancePct = defaultRuntimeTolerancePct
		}
		if in.Limit <= 0 {
			in.Limit = 100
		}

		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, runtimeOut{}, err
		}
		parent := ""
		if folder != nil {
			parent = folder.ItemID
		}

		switch strings.ToLower(in.Types) {
		case "", "episode", "episodes":
			out, err := auditEpisodeRuntimes(ctx, client, parent, in)
			return nil, runtimeOut{auditOut: out}, err
		case "movie", "movies":
			if provider == nil {
				return nil, runtimeOut{}, errors.New("movie runtime audit needs a TMDB token: set EMBYFIN_TMDB_TOKEN (or --tmdb-token) and restart")
			}
			out, err := auditMovieRuntimes(ctx, client, provider, parent, in)
			return nil, out, err
		default:
			return nil, runtimeOut{}, fmt.Errorf("types must be Episode or Movie, got %q", in.Types)
		}
	})
}

// runtimeOff reports whether actual differs from expected by more than the
// tolerance (percent) and by at least a couple of minutes.
func runtimeOff(actual, expected, tolerancePct int) (int, bool) {
	diff := actual - expected
	if diff < 0 {
		diff = -diff
	}
	if diff < minRuntimeDiffMinutes || expected <= 0 {
		return 0, false
	}
	pct := diff * 100 / expected

	return pct, pct > tolerancePct
}

// absurdRuntimeMinutes is where a runtime stops being a long episode and
// becomes broken metadata: an "episode" that runs for weeks is a broken
// duration, and anything computed from it is arithmetic on nonsense.
const absurdRuntimeMinutes = absurdRuntimeS / 60

// brokenRuntimeRank sorts broken durations above every percentage, because
// they are the clearest problem in the list rather than the largest number.
const brokenRuntimeRank = 1 << 30

func auditEpisodeRuntimes(ctx context.Context, client *embyfin.Client, parent string, in runtimeIn) (auditOut, error) {
	type ep struct {
		id, name, series, filePath string
		season, index, minutes     int
		span                       int
	}
	type seasonKey struct {
		series string
		season int
	}

	seasons := map[seasonKey][]ep{}
	out := auditOut{Findings: []auditFinding{}}
	sweepErr := client.SearchAll(ctx, embyfin.SearchOptions{
		IncludeItemTypes: "Episode",
		ParentID:         parent,
		Fields:           "Path",
	}, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			out.Scanned++
			// specials (season 0) have no typical length, so there is nothing to compare to
			if it.SeriesID == "" || it.RunTimeTicks <= 0 || it.ParentIndexNumber == 0 {
				continue
			}
			k := seasonKey{it.SeriesID, it.ParentIndexNumber}
			seasons[k] = append(seasons[k], ep{
				id: it.ID, name: it.Name, series: it.SeriesName, filePath: it.Path,
				season: it.ParentIndexNumber, index: it.IndexNumber, minutes: it.RuntimeMinutes(),
				// a file the server recorded as holding several episodes is
				// expected to run that many times the median, not once
				span: max(it.IndexNumberEnd-it.IndexNumber+1, 1),
			})
		}
		return true
	})
	if sweepErr != nil {
		return auditOut{}, sweepErr
	}

	type scored struct {
		finding auditFinding
		pct     int
	}
	var findings []scored
	for _, eps := range seasons {
		if len(eps) < 3 {
			continue
		}
		// the median comes from the single-episode files: a season where
		// several files hold two episodes would otherwise take the double
		// length as normal and report the honest singles as short
		mins := make([]int, 0, len(eps))
		for _, e := range eps {
			if e.span == 1 && e.minutes < absurdRuntimeMinutes {
				mins = append(mins, e.minutes)
			}
		}
		if len(mins) == 0 {
			continue
		}
		slices.Sort(mins)
		median := mins[len(mins)/2]

		for _, e := range eps {
			name := fmt.Sprintf("%s S%02dE%02d %s", e.series, e.season, e.index, e.name)
			// a duration this long is broken metadata rather than a long
			// episode; a percentage off a median would dress it up as a
			// measurement
			if e.minutes >= absurdRuntimeMinutes {
				findings = append(findings, scored{pct: brokenRuntimeRank, finding: auditFinding{
					ID: e.id, Name: name, Path: e.filePath,
					Detail: fmt.Sprintf("%d min: not a runtime, the file's duration metadata is broken", e.minutes),
				}})

				continue
			}
			expected := median * e.span
			pct, off := runtimeOff(e.minutes, expected, in.TolerancePct)
			if !off {
				continue
			}
			detail := fmt.Sprintf("%d min, season median %d min (%d%% off)", e.minutes, median, pct)
			if e.span > 1 {
				detail = fmt.Sprintf("%d min for %d episodes, season median %d min each, %d expected (%d%% off)", e.minutes, e.span, median, expected, pct)
			}
			findings = append(findings, scored{pct: pct, finding: auditFinding{
				ID: e.id, Name: name, Path: e.filePath, Detail: detail,
			}})
		}
	}
	// worst deviations first so a capped worklist starts with the clearest problems
	slices.SortFunc(findings, func(a, b scored) int {
		if a.pct != b.pct {
			return b.pct - a.pct
		}
		return strings.Compare(a.finding.Name, b.finding.Name)
	})

	out.Found = len(findings)
	if len(findings) > in.Limit {
		findings = findings[:in.Limit]
	}
	for _, f := range findings {
		out.Findings = append(out.Findings, f.finding)
	}

	return out, nil
}

func auditMovieRuntimes(ctx context.Context, client *embyfin.Client, provider *tmdb.Facts, parent string, in runtimeIn) (runtimeOut, error) {
	maxLookups := in.MaxLookups
	if maxLookups <= 0 {
		maxLookups = defaultTMDBLookups
	}

	out := runtimeOut{Findings: []auditFinding{}}
	lookups := 0
	for start := max(in.Offset, 0); ; start += moviePage {
		items, total, err := client.Search(ctx, embyfin.SearchOptions{
			IncludeItemTypes: "Movie",
			ParentID:         parent,
			Fields:           "Path,ProviderIds,ProductionYear",
			SortBy:           "SortName",
			SortOrder:        "Ascending",
			StartIndex:       start,
			Limit:            moviePage,
		})
		if err != nil {
			return runtimeOut{}, err
		}

		for i := range items {
			it := &items[i]
			tmdbID := providerID(it, "tmdb")
			if tmdbID == "" || it.RunTimeTicks <= 0 {
				out.Scanned++
				continue
			}
			if lookups >= maxLookups {
				out.NextOffset = start + i
				return out, nil
			}
			lookups++
			out.Scanned++

			expected, err := provider.MovieRuntime(ctx, tmdbID)
			if err != nil {
				return runtimeOut{}, err
			}
			pct, off := runtimeOff(it.RuntimeMinutes(), expected, in.TolerancePct)
			if !off {
				continue
			}
			out.Found++
			if len(out.Findings) < in.Limit {
				out.Findings = append(out.Findings, auditFinding{
					ID:     it.ID,
					Name:   it.Name,
					Year:   it.ProductionYear,
					Path:   it.Path,
					Detail: fmt.Sprintf("file %d min, TMDB says %d min (%d%% off)", it.RuntimeMinutes(), expected, pct),
				})
			}
		}

		if len(items) < moviePage || start+len(items) >= total {
			return out, nil
		}
	}
}

// tmdbFacts is how the tools ask TMDB, or nil when no token is set.
func tmdbFacts(opts Options) *tmdb.Facts {
	if opts.TMDBKey == "" {
		return nil
	}
	facts, err := tmdb.NewFacts(opts.TMDBKey, opts.ProviderTransport)
	if err != nil {
		return nil
	}

	return facts
}

// providerID returns the item's id for a metadata provider, matched case-insensitively.
func providerID(it *embyfin.Item, provider string) string {
	for k, v := range it.ProviderIDs {
		if strings.EqualFold(k, provider) {
			return v
		}
	}

	return ""
}
