package tools

import (
	"cmp"
	"context"
	"fmt"
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
	// what makes the finding something other than it looks: two films on
	// one id shown as one film's versions
	Warning string `json:"warning,omitempty"`
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
	return runAuditOver(ctx, client, in, fields, false, skip, check, nil)
}

// runAuditOver is runAudit, over the items as the server shows them to
// people when shown is set (see shownItems): what an audit of versions has
// to read, because Emby merges them only in a user's view.
func runAuditOver(ctx context.Context, client *embyfin.Client, in auditIn, fields string, shown bool, skip func(*embyfin.Item) bool, check func(*embyfin.Item) (string, bool), warn func(*embyfin.Item) string) (auditOut, error) {
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
	score := func(items []embyfin.Item) bool {
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
				f := auditFinding{
					ID:     items[i].ID,
					Name:   items[i].Name,
					Year:   items[i].ProductionYear,
					Path:   items[i].Path,
					Detail: detail,
				}
				if warn != nil {
					f.Warning = warn(&items[i])
				}
				out.Findings = append(out.Findings, f)
			}
		}
		return true
	}
	if shown {
		items, err := shownItems(ctx, client, opts)
		if err != nil {
			return auditOut{}, err
		}
		score(items)

		return out, nil
	}
	if err := client.SearchAll(ctx, opts, score); err != nil {
		return auditOut{}, err
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
	// music is the item types the audit reads in a music library, "" for an
	// audit that has nothing to say about music: audit_all runs it there
	// with these, and over every library with them added to its own
	music string
	// shown sweeps the items as the server shows them to people rather than
	// as it stores them: Emby merges versions only in a user's view
	shown bool
	// warn, when set, says what a finding is other than it looks, on the
	// finding's warning
	warn func(*embyfin.Item) string
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
		music:       musicAlbum,
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
		name: "audit_multiple_versions",
		description: "Sweep the library for items the server shows as one title with several versions (more than one media file, e.g. a 4K and a 1080p copy). Jellyfin merges the files of one film in one folder when it scans; Emby merges those, and copies in other folders sharing a provider id, in what it shows people - so on Emby this reads the library as the first administrator is shown it. Separate entries for the same title show up in audit_duplicates instead. Defaults to Movie,Episode. " +
			"A finding with a warning is probably not one film at all: a version's file names another title than any the film goes by, or a year more than one off, so another film matched to its ids was merged in - identify the wrong one rather than keep the better copy.",
		fields: "Path,ProviderIds,ProductionYear,MediaSources,OriginalTitle,SortName",
		types:  "Movie,Episode",
		shown:  true,
		warn:   versionWarning,
		check: func(it *embyfin.Item) (string, bool) {
			if len(it.MediaSources) < 2 {
				return "", false
			}
			names := make([]string, 0, len(it.MediaSources))
			for _, s := range it.MediaSources {
				// the server's path, which on Windows is split by backslashes
				names = append(names, baseName(s.Path))
			}
			return strconv.Itoa(len(it.MediaSources)) + " versions: " + strings.Join(names, ", "), true
		},
		tool: registerVersionsAudit,
	},
}

// versionsIn is audit_multiple_versions' input: the shared one, saying the
// default this audit really sweeps. A series is never a file with versions,
// so it reads films and episodes, and a schema that said Movie,Series told a
// caller that episodes were left out when they were the half that counts.
type versionsIn struct {
	Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
	Types   string `json:"types,omitempty"   jsonschema:"comma-separated item types to audit; defaults to Movie,Episode"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum findings to return, default 100"`
}

// registerVersionsAudit adds audit_multiple_versions, with the input that
// states its own defaults; the sweep is the shared one.
func registerVersionsAudit(r *registry, c auditCheck) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name:        c.name,
		Description: c.description,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in versionsIn) (*mcp.CallToolResult, auditOut, error) {
		if in.Types == "" {
			in.Types = c.types
		}
		out, err := runAuditOver(ctx, client, auditIn(in), c.fields, c.shown, nil, c.check, c.warn)

		return nil, out, err
	})
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
		TotalGroups int             `json:"total_findings"`
		Groups      [][]itemSummary `json:"groups"         jsonschema:"each group shares one metadata provider id; capped at limit, total_findings is the real count"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_duplicates",
		Description: "Find separate entries sharing the same tmdb/imdb/tvdb id: multiple copies of one film, series or episode, in one library or across libraries (compare the paths). " +
			"A tmdb or tvdb id only joins entries of one kind, a film to a film and a series to a series, because both providers number films and TV apart; an imdb id joins any. " +
			"Episodes are grouped by provider id AND series name AND season and episode number, because a library can carry one shared id across unrelated episodes. " +
			"Items the server shows as one title's versions are not entries of their own (Emby merges them only in what it shows people, so on Emby this reads the library as the first administrator is shown it): audit_multiple_versions lists those. " +
			"A group whose members carry a warning is probably not copies at all: a film's file names another title than any the entry goes by, or a year more than one off, so it is another film matched to the same id. Default limit 50 groups.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dupIn) (*mcp.CallToolResult, dupOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = duplicateLimit
		}
		groups, scanned, err := duplicateGroups(ctx, client, in.Library, in.Types)
		if err != nil {
			return nil, dupOut{}, err
		}

		out := dupOut{Scanned: scanned, TotalGroups: len(groups)}
		if len(groups) > limit {
			groups = groups[:limit]
		}
		for _, g := range groups {
			members := summariseAll(g)
			if warning := duplicateWarning(g); warning != "" {
				for i := range members {
					members[i].Warning = warning
				}
			}
			out.Groups = append(out.Groups, members)
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
		// by its folders, not its id, so a library listed without one can be
		// left out too
		folder, err := findLibrary(ctx, client, name)
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

// audit_duplicates' defaults. Episodes too: a library holding one episode
// twice is the common shape, and audit_all reports this audit's count, so
// the two have to sweep the same things or the overview names a number the
// audit cannot reproduce.
const (
	duplicateTypes = "Movie,Series,Episode"
	duplicateLimit = 50
)

// dupIn is audit_duplicates' input. It is not the shared auditIn because its
// defaults are not: the shared schema said Movie,Series and 100, and a
// caller believing it asked for episodes it already had, or read a capped
// list as the whole.
type dupIn struct {
	Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
	Types   string `json:"types,omitempty"   jsonschema:"comma-separated item types to group; defaults to Movie,Series,Episode"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum groups to return, default 50"`
}

// duplicateGroups sweeps the library and groups items by shared tmdb/imdb id,
// in a deterministic order so a limit pages the same way each run.
func duplicateGroups(ctx context.Context, client *embyfin.Client, library, types string) ([][]embyfin.Item, int, error) {
	if types == "" {
		types = duplicateTypes
	}
	opts := embyfin.SearchOptions{IncludeItemTypes: types}

	folder, err := resolveLibrary(ctx, client, library)
	if err != nil {
		return nil, 0, err
	}
	if folder != nil {
		opts.ParentID = folder.ItemID
	}

	// as the server shows them: two files Emby shows as one film's versions
	// are versions (audit_multiple_versions), not two entries
	opts.Fields = embyfin.FieldsDefault + ",SortName"
	all, err := shownItems(ctx, client, opts)
	if err != nil {
		return nil, 0, err
	}

	return groupByProviderID(all), len(all), nil
}

// duplicateWarning is what to say about a group of entries sharing an id when
// a member's file names another film than the one it is matched to, "" when
// none does: the group is then probably different films on one id, not copies
// to choose between. Runtimes far apart are said beside it, never alone - a
// director's cut runs longer too.
func duplicateWarning(group []embyfin.Item) string {
	var odd []string
	for i := range group {
		for _, path := range filesOf(&group[i]) {
			if why, other := otherFilm(&group[i], path); other {
				odd = append(odd, why)
			}
		}
	}
	if len(odd) == 0 {
		return ""
	}
	apart := ""
	for i := range group {
		for j := i + 1; j < len(group) && apart == ""; j++ {
			if a, b := group[i].RunTimeTicks, group[j].RunTimeTicks; runtimesApart(a, b) {
				apart = fmt.Sprintf(", and the entries run %s and %s", runtimeText(a), runtimeText(b))
			}
		}
	}

	return fmt.Sprintf("probably not copies of one film: %s%s. These entries share an id, but a file names another film: keep neither over the other until the wrong one is identified (item_identify)", strings.Join(odd, "; "), apart)
}

// filesOf are the paths of every file an item is shown in, or its own path
// when the read carried no versions.
func filesOf(it *embyfin.Item) []string {
	if len(it.MediaSources) == 0 {
		return []string{it.Path}
	}
	out := make([]string, 0, len(it.MediaSources))
	for i := range it.MediaSources {
		out = append(out, it.MediaSources[i].Path)
	}

	return out
}

// idSpace is the numbering a tmdb or tvdb id is read in. Both providers
// number films and TV apart - and a series, its seasons and its episodes
// apart again - so tmdb 1399 is one film and an unrelated series. An IMDb
// id names one title whatever it is, and needs no space.
func idSpace(itemType string) string {
	switch itemType {
	case "Series", "Season", typeEpisode:
		return strings.ToLower(itemType)
	}

	return "film"
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
		// an episode's provider id is shared far more loosely than a film's:
		// a library can carry one imdb id on many unrelated episodes, even
		// across different shows. Its series, season and episode number go
		// into the key, so a group is at worst the same episode of the same
		// show.
		//
		// The series goes in by name, folded the way a folder name is (case,
		// spacing, accents, punctuation), and not by id: one show split
		// across two series entries after a folder rename is a case this
		// audit exists for, and the server names the two alike but numbers
		// them apart. Two different shows of one name sharing a loose id
		// would still meet, which is rare enough to read past in a group
		// whose paths are listed.
		episode := ""
		if it.Type == typeEpisode {
			episode = fmt.Sprintf(":%s:s%02de%02d", folderKey(it.SeriesName), it.ParentIndexNumber, it.IndexNumber)
		}
		for k, v := range it.ProviderIDs {
			lk := strings.ToLower(k)
			if (lk != "tmdb" && lk != "imdb" && lk != "tvdb") || v == "" {
				continue
			}
			key := lk + ":" + v + episode
			if lk != "imdb" {
				key = idSpace(it.Type) + ":" + key
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
	Types    string `json:"types,omitempty"   jsonschema:"the item types the row counted, when they are not the audit's own default: pass them to the audit as types for the worklist behind the count"`
	Skipped  bool   `json:"skipped,omitempty" jsonschema:"true when the audit was not run here; note says why"`
	Note     string `json:"note,omitempty"    jsonschema:"why an audit was skipped, or what its count means"`
}

// musicAlbum is what the audits that apply to music read in a music library:
// an album is what carries a cover and the genres a tagger wrote, where an
// artist rarely has an image of its own and a track repeats its album.
const musicAlbum = "MusicAlbum"

// auditAllTypes is the item types audit_all has an audit read over a library,
// given the audit's own default and the types it reads in a music library
// ("" when it has nothing to say about music), and whether it applies there
// at all. A music library holds nothing an audit of films and series reads,
// and every library at once, or a mixed one, holds both.
func auditAllTypes(folder *embyfin.VirtualFolder, def, music string) (types string, applies bool) {
	switch {
	case folder != nil && folder.CollectionType == "music":
		return music, music != ""
	case music != "" && (folder == nil || folder.CollectionType == "" || folder.CollectionType == "mixed"):
		return def + "," + music, true
	}

	return "", true
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
			"Every audit has a row, the orphans check included when no library is given (it is server-wide). The ones that need more than the server are listed as skipped with why: audit_language needs a language to ask about, audit_provider asks the provider one film at a time and is paged, and audit_anime_ids reads the Anime-Lists file. " +
			"In a music library the audits that apply to music (missing covers and spellings) count its albums, and the rest are skipped as having nothing there to read; over every library, or a mixed one, those two count albums beside films and series. A row counted over other types than the audit's default names them in types, to pass to the audit for its worklist.",
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
		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, auditAllOut{}, err
		}
		// a music library holds none of what the audits of films and series
		// read: each of those is a row saying so, rather than a 0 that reads
		// as a clean library, and the audits that apply to music read albums
		music := folder != nil && folder.CollectionType == "music"
		const notMusic = "reads films, series or episodes, and a music library has none"

		for i := range auditChecks {
			c := &auditChecks[i]
			types, applies := auditAllTypes(folder, cmp.Or(c.types, "Movie,Series"), c.music)
			if !applies {
				skip(c.name, notMusic)
				continue
			}
			res, rerr := runAuditOver(ctx, client, auditIn{Library: in.Library, Types: cmp.Or(types, c.types), Limit: 1}, c.fields, c.shown, nil, c.check, nil)
			if rerr != nil {
				return fail(c.name, rerr)
			}
			add(auditAllRow{Audit: c.name, Findings: res.Found, Scanned: res.Scanned, Types: types})
		}
		if music {
			// the rest read films, series and episodes alone, but for the
			// spellings of the genres, tags and studios an album carries
			for _, audit := range []string{"audit_file_path", "audit_duplicates", "audit_duplicate_episodes", "audit_duplicate_series", "audit_disc_folders", "audit_runtime", "audit_quality", "audit_missing_episodes"} {
				skip(audit, notMusic)
			}
			spelling, serr := spellingRow(ctx, client, in.Library, folder)
			if serr != nil {
				return fail("audit_spelling", serr)
			}
			add(spelling)
			skip("audit_unwatched", notMusic)
			skip("audit_orphans", "server-wide: run it without a library")
			skip("audit_language", "needs a language to ask about")
			skip("audit_provider", "asks the provider one film at a time and is paged: run it on its own")
			skip("audit_anime_ids", "reads the Anime-Lists file from the web: run it on its own")

			return nil, out, nil
		}

		paths, err := auditFilePath(ctx, client, nil, nil, pathIn{Library: in.Library, Limit: 1})
		if err != nil {
			return fail("audit_file_path", err)
		}
		add(auditAllRow{Audit: "audit_file_path", Findings: paths.Found, Scanned: paths.Scanned, Note: "items whose path disagrees with their metadata - title, year, series, season or episode - or whose name has a letter that only looks Latin; TMDB is not asked here, so a path named by a title only TMDB lists for the item is still counted"})

		// films, series AND episodes: this pass used to ask for the default
		// Movie,Series, so it reported the series count as items_scanned and
		// an episode held twice was invisible from the call that says "start
		// here"
		groups, scanned, err := duplicateGroups(ctx, client, in.Library, "")
		if err != nil {
			return fail("audit_duplicates", err)
		}
		add(auditAllRow{Audit: "audit_duplicates", Findings: len(groups), Scanned: scanned, Note: "groups of entries sharing a provider id, films series and episodes"})

		titles, err := auditDuplicateEpisodes(ctx, client, dupTitlesIn{Library: in.Library, Limit: 1})
		if err != nil {
			return fail("audit_duplicate_episodes", err)
		}
		add(auditAllRow{Audit: "audit_duplicate_episodes", Findings: titles.Found, Scanned: titles.Scanned, Note: "seasons holding one episode title twice"})

		folders, err := auditDuplicateSeries(ctx, client, folderIn{Library: in.Library, Limit: 1})
		if err != nil {
			return fail("audit_duplicate_series", err)
		}
		add(auditAllRow{Audit: "audit_duplicate_series", Findings: folders.Found, Scanned: folders.Scanned, Note: "series held twice from two folders of one name; items_scanned is series"})

		discs, err := auditDiscFolders(ctx, client, discIn{Library: in.Library, Limit: 1})
		if err != nil {
			return fail("audit_disc_folders", err)
		}
		add(auditAllRow{Audit: "audit_disc_folders", Findings: discs.Found, Scanned: discs.Scanned, Note: "folders holding a disc's streams as films"})

		parent := ""
		if folder != nil {
			parent = folder.ItemID
		}
		eps, err := auditEpisodeRuntimes(ctx, client, parent, runtimeIn{TolerancePct: defaultRuntimeTolerancePct, Limit: 1})
		if err != nil {
			return fail("audit_runtime", err)
		}
		add(auditAllRow{Audit: "audit_runtime", Findings: eps.Found, Scanned: eps.Scanned, Note: "episodes against their season median, and durations too long to be a runtime"})

		quality, err := auditQuality(ctx, client, qualityIn{Library: in.Library, Limit: 1})
		if err != nil {
			return fail("audit_quality", err)
		}
		add(auditAllRow{Audit: "audit_quality", Findings: quality.Found, Scanned: quality.Scanned, Note: fmt.Sprintf("below 720p or in a legacy codec; %d files never probed and so not judged, %d written over since the server saw them, neither counted here", quality.TotalUnprobed, quality.TotalReplaced)})

		missing, err := auditMissingEpisodes(ctx, client, nil, episodesIn{Library: in.Library, Limit: 1})
		if err != nil {
			return fail("audit_missing_episodes", err)
		}
		add(auditAllRow{Audit: "audit_missing_episodes", Findings: missing.Found, Scanned: missing.Scanned, Note: "series with episodes missing"})

		spelling, err := spellingRow(ctx, client, in.Library, folder)
		if err != nil {
			return fail("audit_spelling", err)
		}
		add(spelling)

		unwatched, err := auditUnwatched(ctx, client, unwatchedIn{Library: in.Library, Limit: 1})
		if err != nil {
			return fail("audit_unwatched", err)
		}
		// what nobody has watched is a row, not a defect: it is left out of
		// the total so a clean library totals zero
		out.Audits = append(out.Audits, auditAllRow{Audit: "audit_unwatched", Findings: unwatched.Found, Scanned: unwatched.Scanned, Note: "films no account has watched: not a defect, what to archive or recommend; not in total_findings"})

		if in.Library == "" {
			// the sweep audit_orphans counts, without the grouping into
			// folders, which asks the server about each folder and changes
			// no number here
			_, orphans, scanned, err := findOrphans(ctx, client)
			if err != nil {
				return fail("audit_orphans", err)
			}
			add(auditAllRow{Audit: "audit_orphans", Findings: len(orphans), Scanned: scanned, Note: "items under folders no library covers; audit_orphans lists them, in the admin toolset"})
		} else {
			skip("audit_orphans", "server-wide: run it without a library")
		}
		skip("audit_language", "needs a language to ask about")
		skip("audit_provider", "asks the provider one film at a time and is paged: run it on its own")
		skip("audit_anime_ids", "reads the Anime-Lists file from the web: run it on its own")

		return nil, out, nil
	})
}

// spellingRow is audit_all's audit_spelling row: the groups spelled more than
// one way among the genres, tags and studios of what the library holds,
// albums included where it holds music.
func spellingRow(ctx context.Context, client *embyfin.Client, library string, folder *embyfin.VirtualFolder) (auditAllRow, error) {
	types, _ := auditAllTypes(folder, vocabularyTypes, musicAlbum)
	spellings, scanned, err := spellingAudit(ctx, client, library, types, vocabFields)
	if err != nil {
		return auditAllRow{}, err
	}
	groups := 0
	for _, f := range vocabFields {
		groups += len(spellings.report(f))
	}

	return auditAllRow{Audit: "audit_spelling", Findings: groups, Scanned: scanned, Types: types, Note: "groups of genres, tags and studios spelled more than one way"}, nil
}

// runtime audit -----------------------------------------------------------

const (
	defaultRuntimeTolerancePct = 20
	minRuntimeDiffMinutes      = 2
	defaultTMDBLookups         = 250
)

type runtimeIn struct {
	Library      string `json:"library,omitempty"           jsonschema:"restrict to one library by name or id"`
	TolerancePct int    `json:"tolerance_percent,omitempty" jsonschema:"flag when the file runtime differs from the season's median by more than this percent, default 20"`
	Limit        int    `json:"limit,omitempty"             jsonschema:"maximum findings to return, default 100"`
}

func registerRuntimeAudit(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_runtime",
		Description: "Find episodes whose runtime disagrees with their season's: truncated downloads, wrong files, or wrong matches, each compared to the median of its season (needs 3+ episodes), with no external data. " +
			fmt.Sprintf("A duration too long to be any episode's (%d hours or more) is broken metadata and reported wherever it is, whatever the season holds. A show's extras, which Emby 4.10 holds as episodes when they sit in a season's Extras folder, are left out. A film's runtime against its provider's is audit_provider.", absurdRuntimeS/3600),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in runtimeIn) (*mcp.CallToolResult, auditOut, error) {
		if in.TolerancePct <= 0 {
			in.TolerancePct = defaultRuntimeTolerancePct
		}
		if in.Limit <= 0 {
			in.Limit = 100
		}

		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, auditOut{}, err
		}
		parent := ""
		if folder != nil {
			parent = folder.ItemID
		}
		out, err := auditEpisodeRuntimes(ctx, client, parent, in)

		return nil, out, err
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

	type scored struct {
		finding auditFinding
		pct     int
	}
	var findings []scored
	name := func(e ep) string { return fmt.Sprintf("%s S%02dE%02d %s", e.series, e.season, e.index, e.name) }

	seasons := map[seasonKey][]ep{}
	out := auditOut{Findings: []auditFinding{}}
	sweepErr := client.SearchAll(ctx, embyfin.SearchOptions{
		IncludeItemTypes: "Episode",
		ParentID:         parent,
		Fields:           "Path",
	}, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			// a featurette Emby took for an episode runs as long as it runs
			if extraEpisode(it) {
				continue
			}
			out.Scanned++
			if it.RunTimeTicks <= 0 {
				continue
			}
			e := ep{
				id: it.ID, name: it.Name, series: it.SeriesName, filePath: it.Path,
				season: it.ParentIndexNumber, index: it.IndexNumber, minutes: it.RuntimeMinutes(),
				// a file the server recorded as holding several episodes is
				// expected to run that many times the median, not once
				span: max(it.IndexNumberEnd-it.IndexNumber+1, 1),
			}
			// a duration this long is broken metadata rather than a long
			// episode, and needs nothing to compare it to: it is reported
			// wherever it is, a season of two, a season where every file is
			// broken, the specials. A percentage off a median would dress it
			// up as a measurement.
			if e.minutes >= absurdRuntimeMinutes {
				findings = append(findings, scored{pct: brokenRuntimeRank, finding: auditFinding{
					ID: e.id, Name: name(e), Path: e.filePath,
					Detail: fmt.Sprintf("%d min: not a runtime, the file's duration metadata is broken", e.minutes),
				}})
			}
			// specials (season 0) have no typical length, so there is nothing to compare to
			if it.SeriesID == "" || it.ParentIndexNumber == 0 {
				continue
			}
			k := seasonKey{it.SeriesID, it.ParentIndexNumber}
			seasons[k] = append(seasons[k], e)
		}
		return true
	})
	if sweepErr != nil {
		return auditOut{}, sweepErr
	}

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
			if e.minutes >= absurdRuntimeMinutes {
				continue // reported as broken already
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
				ID: e.id, Name: name(e), Path: e.filePath, Detail: detail,
			}})
		}
	}
	// worst deviations first so a capped worklist starts with the clearest
	// problems; the id settles two entries of one name, so a limit keeps the
	// same ones on every call
	slices.SortFunc(findings, func(a, b scored) int {
		return cmp.Or(cmp.Compare(b.pct, a.pct), strings.Compare(a.finding.Name, b.finding.Name), strings.Compare(a.finding.ID, b.finding.ID))
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
