package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

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
	// changed is what the reads said of the library changing, apart from
	// the rest of the note: what audit_all reports
	changed string
	Note    string `json:"note,omitempty" jsonschema:"set when the library was seen to change while it was read: items added or removed meanwhile may be missing, or listed though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read. On Emby, an audit of what people are shown also says how the items shown only as versions of others were placed: by the key Emby merges them by, with a sample checked against a read of each, or by a read of each"`
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
		items, note, placing, serr := shownItems(ctx, client, opts)
		if serr != nil {
			return auditOut{}, serr
		}
		score(items)
		out.changed, out.Note = note, joinWarnings(note, placing)

		return out, nil
	}
	result, err := client.ReadAll(ctx, opts, embyfin.ToAnswer, score)
	if err != nil {
		return auditOut{}, err
	}
	out.changed = result.Changed()
	out.Note = out.changed

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
			"A finding with a warning may not be one film at all: a version's file names another title than any the film goes by, or a year more than one off, so another film matched to its ids may have been merged in - identify the wrong one rather than keep the better copy. A file is read as audit_file_path reads a path: against the film's name, original title and sort name, and with EMBYFIN_TMDB_TOKEN every title TMDB lists for it, or one TMDB's search finds it by under a title like the file's; a file named 'Franchise (Year) Subtitle' is asked by its whole title, not the word before the year, and a title in one of TMDB's translations of the film counts when the search finds the film by it. 'probably not one film' when a year or TMDB says another film; 'may not be one film' when the file only names a title the film does not go by - a title no list holds, or with no token one TMDB was not asked about - or one TMDB lists as an entry of its own titled the film's and an edition's words (an edition, or another film), or its year alone disagrees and nothing says which is right, or TMDB could not be asked, and the warning says which; a file TMDB finds as this very film by its title and year says the year held is the one to check instead. A file whose title is the film's and whose year is two or more off is asked of TMDB by its title and year, as audit_file_path asks it: this very film says the year the item holds is the one to check (no other film), another film says 'probably', and nothing either way - or no token - that it may be another film, or the item's year is wrong. A renamer's or a release's words after the year are no title: tags in brackets or braces, a trailing '-GROUP', quality, source, HDR, IMAX, 3D, language and dub words, a stacked file's part ('cd1', 'Disc 2') and an extra's word ('Sample', 'Trailer'), read across a hyphen or a plus ('Bluray-1080p', 'HDR10+', 'German-DL'); 'Part 2', the number set apart, is a title's. A file whose words before its year are the film's own title and whose words after are more ('Dune (2021) Part Two' held as Dune) is, with EMBYFIN_TMDB_TOKEN, asked of TMDB by its whole title in no year, and its words after the year held against the films of the film's TMDB collection ('Alien (1979) - Aliens'): another film when TMDB names one of another id by them - a film of its own whose title is the film's and a number alone ('Film: Part Two', 'Film 2', 'Film II') or another film of its TMDB collection among them - 'may' only when that film's title is the film's and an edition's words alone ('Film: Ultimate Edition', 'Film: The Director's Cut'), which may be an edition TMDB lists apart, and said to be unchecked when TMDB cannot be asked. Words after the year that are an edition's alone ('Film (1982) - Final Cut') are asked nothing and stay quiet: the film's own title and year beside them can only be the film, or an edition TMDB lists apart; without a token a label and a title after the year cannot be told apart, and neither is warned. The words before the year, asked when the whole title finds nothing, count only as the same title ('Alien' is not 'Alien 2').",
		fields: "Path,ProviderIds,ProductionYear,MediaSources,OriginalTitle,SortName",
		types:  "Movie,Episode",
		shown:  true,
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
	check := newTitleCheck(r.opts)

	add(r, readTool, &mcp.Tool{
		Name:        c.name,
		Description: c.description,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in versionsIn) (*mcp.CallToolResult, auditOut, error) {
		if in.Types == "" {
			in.Types = c.types
		}
		warn := func(it *embyfin.Item) string { return versionWarning(ctx, check, it) }
		out, err := runAuditOver(ctx, client, auditIn(in), c.fields, c.shown, nil, c.check, warn)

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

	check := newTitleCheck(r.opts)
	type dupOut struct {
		Scanned     int             `json:"items_scanned"`
		TotalGroups int             `json:"total_findings"`
		Groups      [][]itemSummary `json:"groups"         jsonschema:"each group shares one metadata provider id; capped at limit, total_findings is the real count"`
		Note        string          `json:"note,omitempty" jsonschema:"set when the library was seen to change while it was read: items added or removed meanwhile may be missing, or listed though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read. On Emby, an audit of what people are shown also says how the items shown only as versions of others were placed: by the key Emby merges them by, with a sample checked against a read of each, or by a read of each"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_duplicates",
		Description: "Find separate entries sharing the same tmdb/imdb/tvdb id: multiple copies of one film, series or episode, in one library or across libraries (compare the paths). " +
			"A tmdb or tvdb id only joins entries of one kind, a film to a film and a series to a series, because both providers number films and TV apart; an imdb id joins any. " +
			"Episodes are grouped by provider id AND series name AND season and episode number, because a library can carry one shared id across unrelated episodes. " +
			"Items the server shows as one title's versions are not entries of their own (Emby merges them only in what it shows people, so on Emby this reads the library as the first administrator is shown it): audit_multiple_versions lists those. " +
			"Entries whose AniDB ids differ are grouped and each is warned: an anime's special or sequel kept as its own entry often carries its parent's TVDB or TMDB id, and is a different work, not a copy; only entries of one AniDB id can be copies, and one with none could copy any. Whatever else is wrong among the entries of one AniDB id is said as well, and an entry with none is held against the entries of each AniDB id in turn. A placeholder id (0, tt0000000) groups nothing. " +
			"A group whose members carry a warning may not be copies at all. 'probably not copies' when a year says so - a file's or folder's year more than one off the entry's, or the entries' own years more than one apart - or TMDB gives the file's title to another film; 'may not be copies' when a file or folder only names a title the entry does not go by - a title no list holds, or with no token one TMDB was not asked about - or one TMDB lists as an entry of its own titled the film's and an edition's words (an edition, or another film), or its year alone disagrees and nothing says which is right, or TMDB could not be asked, and the warning says which; a file TMDB finds as this very film by its title and year says the year held is the one to check instead. A file whose title is the film's and whose year is two or more off is asked of TMDB by its title and year, as audit_file_path asks it: this very film says the year the item holds is the one to check (no other film), another film says 'probably', and nothing either way - or no token - that it may be another film, or the item's year is wrong. A renamer's or a release's words after the year are no title: tags in brackets or braces, a trailing '-GROUP', quality, source, HDR, IMAX, 3D, language and dub words, a stacked file's part ('cd1', 'Disc 2') and an extra's word ('Sample', 'Trailer'), read across a hyphen or a plus ('Bluray-1080p', 'HDR10+', 'German-DL'); 'Part 2', the number set apart, is a title's. Paths are read as audit_file_path reads them: with EMBYFIN_TMDB_TOKEN a title TMDB lists for the film or series, or finds it by under that title, is one it goes by, as is its title in one of TMDB's translations when the search finds it by that. A file whose words before its year are the film's own title and whose words after are more ('Dune (2021) Part Two' held as Dune) is, with EMBYFIN_TMDB_TOKEN, asked of TMDB by its whole title in no year, and its words after the year held against the films of the film's TMDB collection ('Alien (1979) - Aliens'): another film when TMDB names one of another id by them - a film of its own whose title is the film's and a number alone ('Film: Part Two', 'Film 2', 'Film II') or another film of its TMDB collection among them - 'may' only when that film's title is the film's and an edition's words alone ('Film: Ultimate Edition', 'Film: The Director's Cut'), which may be an edition TMDB lists apart, and said to be unchecked when TMDB cannot be asked. Words after the year that are an edition's alone ('Film (1982) - Final Cut') are asked nothing and stay quiet: the film's own title and year beside them can only be the film, or an edition TMDB lists apart; without a token a label and a title after the year cannot be told apart, and neither is warned. The words before the year, asked when the whole title finds nothing, count only as the same title ('Alien' is not 'Alien 2'). " +
			"Runtimes are held against each other too, whatever the paths name: films or episodes more than twice as long as each other are 'probably not copies' of one cut - one file is cut short, a sample or holds more than one episode, or an id is wrong - and more than 15% apart (a PAL copy's 4% allowed for) are two cuts, a file cut short, or a wrong id: compare the files. Default limit 50 groups.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dupIn) (*mcp.CallToolResult, dupOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = duplicateLimit
		}
		groups, scanned, note, placing, err := duplicateGroups(ctx, client, in.Library, in.Types)
		if err != nil {
			return nil, dupOut{}, err
		}

		out := dupOut{Scanned: scanned, TotalGroups: len(groups), Note: joinWarnings(note, placing)}
		if len(groups) > limit {
			groups = groups[:limit]
		}
		for _, g := range groups {
			members := summariseAll(g)
			for i, warning := range duplicateWarnings(ctx, check, g) {
				members[i].Warning = warning
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
// in a deterministic order so a limit pages the same way each run, with the
// note on the library changing under the sweep, and on Emby how the items
// shown only as versions of others were placed.
func duplicateGroups(ctx context.Context, client *embyfin.Client, library, types string) (groups [][]embyfin.Item, scanned int, note, placing string, err error) {
	if types == "" {
		types = duplicateTypes
	}
	opts := embyfin.SearchOptions{IncludeItemTypes: types}

	folder, err := resolveLibrary(ctx, client, library)
	if err != nil {
		return nil, 0, "", "", err
	}
	if folder != nil {
		opts.ParentID = folder.ItemID
	}

	// as the server shows them: two files Emby shows as one film's versions
	// are versions (audit_multiple_versions), not two entries
	opts.Fields = embyfin.FieldsDefault + ",SortName"
	all, note, placing, err := shownItems(ctx, client, opts)
	if err != nil {
		return nil, 0, "", "", err
	}

	return groupByProviderID(all), len(all), note, placing, nil
}

// duplicateWarnings are what to say of each member of a group of entries
// sharing an id, "" for a member with nothing to say. A group whose members
// carry more than one AniDB id is different works on one id (anidbWarnings);
// any other is one warning for every member, or none (duplicateWarning).
func duplicateWarnings(ctx context.Context, check *titleCheck, group []embyfin.Item) []string {
	if split := anidbWarnings(group); split != nil {
		// the entries of one AniDB id can still be one matched wrong: what
		// the files, the years and the runtimes say of them is said too
		byID := map[string][]int{}
		var ids, unmarked []int
		for i := range group {
			id := realID(providerID(&group[i], "anidb"))
			if id == "" {
				unmarked = append(unmarked, i)

				continue
			}
			if byID[id] == nil {
				ids = append(ids, i)
			}
			byID[id] = append(byID[id], i)
		}
		of := func(members ...int) []embyfin.Item {
			out := make([]embyfin.Item, 0, len(members))
			for _, i := range members {
				out = append(out, group[i])
			}

			return out
		}
		for id, members := range byID {
			if len(members) < 2 {
				continue
			}
			if warning := duplicateWarning(ctx, check, of(members...)); warning != "" {
				for _, i := range members {
					split[i] += fmt.Sprintf(". Among the entries of AniDB %s: %s", id, warning)
				}
			}
		}
		// an entry with no AniDB id could copy the entries of any: what its
		// folder, its year and its length say against each is said too
		for _, i := range unmarked {
			for _, first := range ids {
				id := realID(providerID(&group[first], "anidb"))
				if warning := duplicateWarning(ctx, check, of(append([]int{i}, byID[id]...)...)); warning != "" {
					split[i] += fmt.Sprintf(". Against the entries of AniDB %s: %s", id, warning)
				}
			}
		}

		return split
	}
	warning := duplicateWarning(ctx, check, group)
	out := make([]string, len(group))
	for i := range out {
		out[i] = warning
	}

	return out
}

// anidbWarnings is what to say of each member of a group whose members carry
// more than one AniDB id, nil when they carry one or none. An anime's special
// or sequel kept as its own entry commonly carries its parent's TVDB or TMDB
// id - the providers fold it into the parent's seasons - and grouped with the
// parent unmarked it read as a second copy of it, inviting a delete of an
// entry holding other episodes. Only the entries of one AniDB id can be
// copies of each other, and one carrying none could copy any of them.
func anidbWarnings(group []embyfin.Item) []string {
	ids := make([]string, len(group))
	var distinct []string
	for i := range group {
		ids[i] = realID(providerID(&group[i], "anidb"))
		if ids[i] != "" && !slices.Contains(distinct, ids[i]) {
			distinct = append(distinct, ids[i])
		}
	}
	if len(distinct) < 2 {
		return nil
	}
	slices.Sort(distinct)
	out := make([]string, len(group))
	for i, id := range ids {
		if id == "" {
			out[i] = fmt.Sprintf("carries no AniDB id, in a group holding AniDB %s: different works sharing an id, and which of them this entry copies, if any, the ids do not say", quotedAnd(distinct))
			continue
		}
		others := slices.DeleteFunc(slices.Clone(distinct), func(o string) bool { return o == id })
		out[i] = fmt.Sprintf("AniDB ids differ: this entry is AniDB %s, and the group holds AniDB %s too - different works sharing an id (a special or a sequel held apart carries its parent's TVDB or TMDB id), not copies of each other; only entries of one AniDB id can be copies", id, quotedAnd(others))
	}

	return out
}

// quotedAnd is ids as a sentence lists them: "1", "1 and 2", "1, 2 and 3".
func quotedAnd(ids []string) string {
	if len(ids) < 2 {
		return strings.Join(ids, "")
	}

	return strings.Join(ids[:len(ids)-1], ", ") + " and " + ids[len(ids)-1]
}

// duplicateWarning is what to say about a group of entries sharing an id when
// they are probably not one title, "" when nothing says so: a member's file
// or folder names another title than any it goes by, or a year more than one
// off its own, or the entries' own years are more than one apart - a remake
// or a reboot matched to the original's id - or their runtimes are too far
// apart for copies (runtimeWarning). The group is then probably different
// titles on one id, not copies to choose between. Files and folders are read
// as check reads them.
func duplicateWarning(ctx context.Context, check *titleCheck, group []embyfin.Item) string {
	// an IMDb id names one title, so a film and a series sharing one - which
	// only an IMDb id joins - are no copies of anything
	var kinds []string
	for _, k := range []struct{ kind, what string }{{typeMovie, "a film"}, {"Series", "a series"}, {typeEpisode, "an episode"}} {
		if slices.ContainsFunc(group, func(it embyfin.Item) bool { return it.Type == k.kind }) {
			kinds = append(kinds, k.what)
		}
	}
	if len(kinds) > 1 {
		return fmt.Sprintf("probably not copies: %s share one IMDb id, which names one title, so one of the ids is wrong: identify the wrong one (item_identify) rather than keep either", strings.Join(kinds, " and "))
	}

	var verdicts []fileVerdict
	kind := "film"
	for i := range group {
		switch group[i].Type {
		case "Series":
			kind = "series"
			verdicts = append(verdicts, check.otherSeries(ctx, &group[i]))

			continue
		case typeEpisode:
			kind = "episode"
		}
		for _, path := range filesOf(&group[i]) {
			verdicts = append(verdicts, check.otherFilm(ctx, &group[i], path))
		}
	}
	// sure is whether anything says another title rather than a title an
	// entry does not go by, which may be one of its titles no list holds
	odd, sure, doubt, tmdbErr := sumVerdicts(verdicts)
	if len(odd) == 0 {
		if first, last := yearsOf(group); first > 0 && last-first > 1 {
			odd, sure = append(odd, fmt.Sprintf("the entries are dated %d and %d", first, last)), true
		}
	}
	// the lengths are read whatever the paths say: a file more than twice
	// as long as another is no copy of it, whatever it is named
	lengths := runtimeWarning(kind, group)
	if len(odd) == 0 {
		return lengths
	}
	if !sure && strings.HasPrefix(lengths, "probably not") {
		return fmt.Sprintf("%s. The paths say more: %s%s", lengths, strings.Join(odd, "; "), unasked(tmdbErr))
	}
	// a file TMDB finds as this very film by its title and year says the
	// entry's year is wrong, not that the file is another film
	if onlyItemYear(verdicts) {
		return fmt.Sprintf("an entry's year is the one to check, not its file: %s%s", strings.Join(odd, "; "), unasked(tmdbErr))
	}
	apart := ""
	for i := range group {
		for j := i + 1; j < len(group) && apart == ""; j++ {
			if a, b := group[i].RunTimeTicks, group[j].RunTimeTicks; runtimesApart(a, b) {
				apart = fmt.Sprintf(", and the entries run %s and %s", runtimeText(a), runtimeText(b))
			}
		}
	}

	if !sure {
		rest := "Compare them before keeping one over the other"
		if doubt {
			rest = fmt.Sprintf("%s: a translation or other title of it, or another %s matched to the same id - compare them before keeping one over the other", titleDoubt("A file or folder", kind, check.asks(group), tmdbErr), kind)
		}

		return fmt.Sprintf("may not be copies of one %s: %s%s. %s%s", kind, strings.Join(odd, "; "), apart, rest, unasked(tmdbErr))
	}

	return fmt.Sprintf("probably not copies of one %s: %s%s. These entries share an id, but that says one of them is matched wrong: keep neither over the other until the wrong one is identified (item_identify)%s", kind, strings.Join(odd, "; "), apart, unasked(tmdbErr))
}

// runtimeWarning is what to say of a group of films or episodes sharing an id
// whose runtimes are too far apart to be copies of one cut, "" when they are
// not. More than twice as long is no two cuts of one film: an id is wrong, or
// a file is cut short, a sample, or holds more - two episodes in one file,
// which shares the first one's id. More than 15% apart, once a PAL copy's 4%
// is allowed for, is two cuts, a file cut short, or a wrong id. Two films
// sharing an IMDb id ran 4 minutes and 64 and were listed as copies. A
// series' runtime is its episodes', which says nothing of which show it is.
// The runtimes are the files' where the server read them.
func runtimeWarning(kind string, group []embyfin.Item) string {
	if kind == "series" {
		return ""
	}
	var shortest, longest int64
	for i := range group {
		r := knownRuntime(&group[i])
		if r <= 0 {
			continue
		}
		if shortest == 0 || r < shortest {
			shortest = r
		}
		longest = max(longest, r)
	}
	if shortest <= 0 {
		return ""
	}
	runs := fmt.Sprintf("the entries run %s and %s", runtimeText(shortest), runtimeText(longest))
	// a file recorded as holding a run of episodes shares its first one's id
	run := ""
	for i := range group {
		if it := &group[i]; it.Type == typeEpisode && it.IndexNumber != nil && it.IndexNumberEnd > *it.IndexNumber {
			run = fmt.Sprintf("; %s is held as episodes %d to %d, and may hold them all", baseName(it.Path), *it.IndexNumber, it.IndexNumberEnd)

			break
		}
	}
	switch {
	case longest > 2*shortest && kind == "episode":
		return fmt.Sprintf("probably not copies of one episode: %s, more than twice as long: one file is cut short or a sample, or holds more than one episode, or one of the ids is wrong%s - compare the files before keeping either", runs, run)
	case longest > 2*shortest:
		return fmt.Sprintf("probably not copies of one film: %s, more than twice as long, which no two cuts of one film are: one file is cut short or a sample, or one of the ids is wrong - compare the files before keeping either", runs)
	case float64(longest) > 1.15*palSpeedup*float64(shortest) && kind == "episode":
		return fmt.Sprintf("not copies of one cut: %s, more than 15%% apart - an extended cut, a file cut short or holding more, or a wrong id on one%s: compare them before keeping one over the other", runs, run)
	case float64(longest) > 1.15*palSpeedup*float64(shortest):
		return fmt.Sprintf("not copies of one cut: %s, more than 15%% apart - two cuts of one film (a theatrical and an extended one), a file cut short, or a wrong id on one: compare them before keeping one over the other", runs)
	}

	return ""
}

// seriesClaim says whether a series' folder names another show than the
// entry it is held as, the way filmClaim reads a film's file: a (year) more
// than one off the entry's, or a title unlike every name it goes by, with
// what the folder claims. A folder with no year claims too little to hold
// against it.
func seriesClaim(it *embyfin.Item) (why string, c pathClaim, other bool) {
	if it.Path == "" {
		return "", pathClaim{}, false
	}
	c, ok := claimOf(it.Path, heldTitles(it))
	if !ok || baseName(it.Path) != c.segment {
		return "", c, false
	}
	titleOff := c.title != "" && c.score < seriesConfident
	yearOff := it.ProductionYear > 0 && abs(c.year-it.ProductionYear) > 1
	if !titleOff && !yearOff {
		return "", c, false
	}
	held := it.Name
	if it.ProductionYear > 0 {
		held = fmt.Sprintf("%s (%d)", it.Name, it.ProductionYear)
	}
	named := cmp.Or(c.title, c.segment)
	if titleOff {
		named = c.named(heldTitles(it))
	}

	return fmt.Sprintf("%q is named for %q (%d), not %s", c.segment, named, c.year, held), c, true
}

// yearsOf is the earliest and the latest year a group's entries are dated,
// 0 and 0 when none is dated. An episode's year is its air date's, which
// says nothing about which show it is, so a group of episodes has none.
func yearsOf(group []embyfin.Item) (first, last int) {
	for i := range group {
		y := group[i].ProductionYear
		if y <= 0 || group[i].Type == typeEpisode {
			continue
		}
		if first == 0 || y < first {
			first = y
		}
		last = max(last, y)
	}

	return first, last
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
// split by which id they happen to share. Entries whose AniDB ids differ are
// grouped too, and said to be different works (anidbWarnings): kept apart
// silently, a copy of a show beside a special carrying the show's id was
// never listed, and an entry with no AniDB id joined one or the other by the
// order the ids came in.
func groupByProviderID(items []embyfin.Item) [][]embyfin.Item {
	j := newJoins(len(items))
	holders := map[string][]int{} // provider key -> the items carrying it, in sweep order
	for i := range items {
		it := &items[i]
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
			episode = fmt.Sprintf(":%s:%s", folderKey(it.SeriesName), strings.ToLower(episodeCode(it)))
			// an episode the server holds no season or number for is no
			// particular episode, so sharing a loose id with another says
			// nothing: it keys on itself and joins no group
			if !numbered(it) {
				episode += ":" + it.ID
			}
		}
		for k, v := range it.ProviderIDs {
			lk := strings.ToLower(k)
			// a placeholder a scraper or a template left (0, tt0000000) names
			// nothing, and would join every entry carrying it
			if (lk != "tmdb" && lk != "imdb" && lk != "tvdb") || realID(v) == "" {
				continue
			}
			key := lk + ":" + v + episode
			if lk != "imdb" {
				key = idSpace(it.Type) + ":" + key
			}
			holders[key] = append(holders[key], i)
		}
	}
	for _, members := range holders {
		for _, m := range members[1:] {
			j.join(members[0], m)
		}
	}

	joined := j.groups()
	if len(joined) == 0 {
		return nil
	}
	groups := make([][]embyfin.Item, 0, len(joined))
	for _, members := range joined {
		group := make([]embyfin.Item, 0, len(members))
		for _, i := range members {
			group = append(group, items[i])
		}
		groups = append(groups, group)
	}
	slices.SortFunc(groups, func(a, b []embyfin.Item) int {
		if c := strings.Compare(a[0].Name, b[0].Name); c != 0 {
			return c
		}
		return strings.Compare(a[0].ID, b[0].ID)
	})

	return groups
}

// realID is a provider id as given, or "" for none: blank, or a placeholder
// of zeros (0, tt0000000) that names no title.
func realID(id string) string {
	id = strings.TrimSpace(id)
	if strings.Trim(strings.TrimPrefix(strings.ToLower(id), "tt"), "0") == "" {
		return ""
	}

	return id
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
	Where    string `json:"where,omitempty"   jsonschema:"audit_whitespace: the places the row counted, every one but the file names: pass them to it as where for the worklist behind the count"`
	Skipped  bool   `json:"skipped,omitempty" jsonschema:"true when the audit was not run here, or its read stopped short and it has no count; note says why"`
	Partial  bool   `json:"partial,omitempty" jsonschema:"true when the count covers only part of what the audit checks - episodes with no runtime to judge, shows whose run is not known - so a small count is not a clean result; note says what was left out"`
	Note     string `json:"note,omitempty"    jsonschema:"why an audit was skipped, or what its count means, and what its read saw when the library was seen to change under it"`
	// an audit that failed is a row of its own, not the end of the call
	Failed bool     `json:"failed,omitempty" jsonschema:"true when the audit ran and failed: findings and items_scanned are then not counts, error says what failed, and the audit can be run on its own"`
	Error  string   `json:"error,omitempty"  jsonschema:"what failed, when failed is set"`
	Took   *float64 `json:"took_s,omitempty" jsonschema:"how long the audit took, in seconds, whether it counted or failed; not set on a row skipped"`
	// changed is what the audit's read saw of the library changing under
	// it, or could not tell; "" when nothing was seen to come or go
	changed string
}

// auditAllNames is every audit audit_all has a row for, in its order.
func auditAllNames() []string {
	names := make([]string, 0, len(auditChecks)+15)
	for i := range auditChecks {
		names = append(names, auditChecks[i].name)
	}

	return append(names, "audit_file_path", "audit_duplicates", "audit_duplicate_episodes", "audit_duplicate_series", "audit_disc_folders", "audit_runtime", "audit_quality", "audit_missing_episodes",
		"audit_spelling", "audit_whitespace", "audit_unwatched", "audit_orphans", "audit_language", "audit_provider", "audit_anime_ids")
}

// auditedKind says whether the audits read anything in a library of a kind:
// films, series and episodes, and albums in a music library. A library of
// home videos, music videos, books or photos holds none of them, and every
// row over it counted 0 - which reads as a clean library.
func auditedKind(collectionType string) bool {
	switch collectionType {
	case "", "movies", "tvshows", "music", "mixed":
		return true
	}

	return false
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
	Audits     []auditAllRow `json:"audits"`
	Total      int           `json:"total_findings"                  jsonschema:"the defects found, over every row but audit_unwatched, which is about viewing rather than a fault, and those that failed: with failed set it is short by whatever they would have found"`
	NotChecked []string      `json:"libraries_not_checked,omitempty" jsonschema:"libraries of a kind no audit reads (home videos, music videos, books, photos), with their kind: nothing in them was checked, so the counts say nothing about them"`
	Failed     []string      `json:"failed,omitempty"                jsonschema:"the audits that failed; each one's row says why"`
	Note       string        `json:"note,omitempty"                  jsonschema:"set when an audit's read saw the library change, or could not check whether it did, naming those audits: their counts may be off, and each one's row says what its read saw; and naming the audits whose reads stopped short, the library changing too much to follow, which have no count. Empty when no audit's read saw an item come or go from its first page to its last"`
}

// auditAllStep is one of audit_all's rows: an audit to run for its count,
// or one skipped here and why.
type auditAllStep struct {
	audit string
	skip  string // why the audit is not run here; "" runs it
	row   func() (auditAllRow, error)
	// uncounted leaves a row out of total_findings: audit_unwatched's, which
	// is about viewing rather than a fault
	uncounted bool
}

// runAuditAll runs audit_all's steps in order, each row timed. An audit that
// fails is a row saying so, with its error, and the rest still run: failing
// the call with it lost every other audit's count, as one proxy's 502 nearly
// three hours into a large library did. Only the call itself ending - the
// caller gone - stops it, since nothing would read the rows.
func runAuditAll(ctx context.Context, steps []auditAllStep) (auditAllOut, error) {
	out := auditAllOut{Audits: []auditAllRow{}}
	// changed is the audits whose reads saw the library change, or could not
	// tell; cut those whose reads the library changed too much to follow: a
	// count from half a read is no count, so their rows have none
	var changed, cut []string
	for _, s := range steps {
		if s.skip != "" {
			out.Audits = append(out.Audits, auditAllRow{Audit: s.audit, Skipped: true, Note: s.skip})

			continue
		}
		start := time.Now()
		row, err := s.row()
		took := math.Round(time.Since(start).Seconds()*10) / 10
		if _, stopped := errors.AsType[*embyfin.CutError](err); stopped && ctx.Err() == nil {
			out.Audits = append(out.Audits, auditAllRow{Audit: s.audit, Skipped: true, Note: "not counted: " + err.Error()})
			cut = append(cut, s.audit)

			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return auditAllOut{}, fmt.Errorf("%s: %w", s.audit, err)
			}
			out.Audits = append(out.Audits, auditAllRow{Audit: s.audit, Failed: true, Error: err.Error(), Took: &took})
			out.Failed = append(out.Failed, s.audit)

			continue
		}
		row.Audit = s.audit
		if row.changed != "" {
			row.Note = joinWarnings(row.Note, row.changed)
			changed = append(changed, s.audit)
		}
		if !row.Skipped { // a row that turned out to have nothing to read was not timed as an audit
			row.Took = &took
		}
		out.Audits = append(out.Audits, row)
		if !s.uncounted {
			out.Total += row.Findings
		}
	}
	if len(changed) > 0 {
		out.Note = fmt.Sprintf("the counts of %s may be off: the library changed while they read it, or they could not tell whether it did; each row's note says which", strings.Join(changed, ", "))
	}
	if len(cut) > 0 {
		out.Note = joinWarnings(out.Note, strings.Join(cut, ", ")+" could not be counted: the library changed too much while they read it to follow it; run them again")
	}

	return out, nil
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
			"In a music library the audits that apply to music (missing covers and spellings) count its albums, and the rest are skipped as having nothing there to read; over every library, or a mixed one, those two count albums beside films and series. audit_whitespace reads music as it reads the rest, and its row leaves out the file names, which are one row an item, and says how many there are. A library of a kind no audit reads (home videos, music videos, books, photos) has every row skipped as not checked, and over every library each such library is named in libraries_not_checked. A row counted over other types than the audit's default names them in types, and one counted over some of the places an audit reads names them in where, to pass to the audit for its worklist. " +
			"A row marked partial counted only part of what its audit checks - files with no media facts, shows whose run is not known (the missing-episodes row does not ask TMDB, so it sees only gaps between files unless the server keeps records) - and its note says what was left out: a small count there is not a clean result. " +
			"Every row that ran says how long it took (took_s). An audit that fails does not end the call: its row says failed with the error, the others still count, and failed lists them - total_findings is then short by whatever they would have found, so run a failed one on its own.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditAllIn) (*mcp.CallToolResult, auditAllOut, error) {
		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, auditAllOut{}, err
		}
		// in a library of a kind no audit reads, every row says so, and over
		// every library each such library is named: a 0 read over home videos
		// or books is not a clean library, it is one nothing looked at
		if folder != nil && !auditedKind(folder.CollectionType) {
			notKind := fmt.Sprintf("not checked for this library type: a %s library holds none of the films, series, episodes or albums the audits read", folder.CollectionType)
			out := auditAllOut{Audits: []auditAllRow{}}
			for _, audit := range auditAllNames() {
				note := notKind
				if audit == "audit_orphans" {
					note = "server-wide: run it without a library"
				}
				out.Audits = append(out.Audits, auditAllRow{Audit: audit, Skipped: true, Note: note})
			}

			return nil, out, nil
		}
		var notChecked []string
		if folder == nil {
			libraries, lerr := client.VirtualFolders(ctx)
			if lerr != nil {
				return nil, auditAllOut{}, lerr
			}
			for _, l := range libraries {
				if !auditedKind(l.CollectionType) {
					notChecked = append(notChecked, fmt.Sprintf("%s (%s)", l.Name, l.CollectionType))
				}
			}
		}
		out, err := runAuditAll(ctx, auditAllSteps(ctx, client, in.Library, folder))
		if err != nil {
			return nil, auditAllOut{}, err
		}
		out.NotChecked = notChecked

		return nil, out, nil
	})
}

// auditAllSteps are audit_all's rows over one library, or every library when
// folder is nil, in the order they are reported.
func auditAllSteps(ctx context.Context, client *embyfin.Client, library string, folder *embyfin.VirtualFolder) []auditAllStep {
	var steps []auditAllStep
	run := func(audit string, row func() (auditAllRow, error)) {
		steps = append(steps, auditAllStep{audit: audit, row: row})
	}
	skip := func(audit, why string) {
		steps = append(steps, auditAllStep{audit: audit, skip: why})
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
		run(c.name, func() (auditAllRow, error) {
			res, err := runAuditOver(ctx, client, auditIn{Library: library, Types: cmp.Or(types, c.types), Limit: 1}, c.fields, c.shown, nil, c.check, nil)

			return auditAllRow{Findings: res.Found, Scanned: res.Scanned, Types: types, changed: res.changed}, err
		})
	}
	spelling := func() (auditAllRow, error) { return spellingRow(ctx, client, library, folder) }
	spaces := func() (auditAllRow, error) { return whitespaceAllRow(ctx, client, library) }
	later := func() {
		skip("audit_language", "needs a language to ask about")
		skip("audit_provider", "asks the provider one film at a time and is paged: run it on its own")
		skip("audit_anime_ids", "reads the Anime-Lists file from the web: run it on its own")
	}
	if music {
		// the rest read films, series and episodes alone, but for the
		// spellings of the genres, tags and studios an album carries
		for _, audit := range []string{"audit_file_path", "audit_duplicates", "audit_duplicate_episodes", "audit_duplicate_series", "audit_disc_folders", "audit_runtime", "audit_quality", "audit_missing_episodes"} {
			skip(audit, notMusic)
		}
		run("audit_spelling", spelling)
		run("audit_whitespace", spaces)
		skip("audit_unwatched", notMusic)
		skip("audit_orphans", "server-wide: run it without a library")
		later()

		return steps
	}

	run("audit_file_path", func() (auditAllRow, error) {
		paths, err := auditFilePath(ctx, client, nil, nil, pathIn{Library: library, Limit: 1})

		return auditAllRow{Findings: paths.Found, Scanned: paths.Scanned, Note: "items whose path disagrees with their metadata - title, year, series, season or episode - or whose name has a letter that only looks Latin; TMDB is not asked here, so a path named by a title only TMDB lists for the item is still counted", changed: paths.changed}, err
	})
	// films, series AND episodes: this pass used to ask for the default
	// Movie,Series, so it reported the series count as items_scanned and an
	// episode held twice was invisible from the call that says "start here"
	run("audit_duplicates", func() (auditAllRow, error) {
		// how Emby's versions were placed is said by audit_duplicates itself,
		// and is not the library changing
		groups, scanned, note, _, err := duplicateGroups(ctx, client, library, "")

		return auditAllRow{Findings: len(groups), Scanned: scanned, Note: "groups of entries sharing a provider id, films series and episodes", changed: note}, err
	})
	run("audit_duplicate_episodes", func() (auditAllRow, error) {
		titles, err := auditDuplicateEpisodes(ctx, client, dupTitlesIn{Library: library, Limit: 1})

		return auditAllRow{Findings: titles.Found, Scanned: titles.Scanned, Note: "groups of one season's entries sharing a title: near certain, leads, and titles whose entries run more than 15% apart (far_apart)", changed: titles.changed}, err
	})
	run("audit_duplicate_series", func() (auditAllRow, error) {
		folders, err := auditDuplicateSeries(ctx, client, folderIn{Library: library, Limit: 1})

		return auditAllRow{Findings: folders.Found, Scanned: folders.Scanned, Note: "series held twice from two folders of one name; items_scanned is series", changed: folders.Note}, err
	})
	run("audit_disc_folders", func() (auditAllRow, error) {
		discs, err := auditDiscFolders(ctx, client, discIn{Library: library, Limit: 1})

		return auditAllRow{Findings: discs.Found, Scanned: discs.Scanned, Note: "folders holding a disc's streams as films", changed: discs.Note}, err
	})
	run("audit_runtime", func() (auditAllRow, error) {
		parent := ""
		if folder != nil {
			parent = folder.ItemID
		}
		eps, err := auditEpisodeRuntimes(ctx, client, parent, runtimeIn{TolerancePct: defaultRuntimeTolerancePct, Limit: 1})

		return auditAllRow{
			Findings: eps.Found, Scanned: eps.Scanned, Partial: eps.Unprobed > 0, changed: eps.Note,
			Note: fmt.Sprintf("episodes against what their season typically runs, and durations too long to be a runtime; %d episode files the server holds no runtime for (never probed) are counted in items_scanned and not judged", eps.Unprobed),
		}, err
	})
	run("audit_quality", func() (auditAllRow, error) {
		quality, err := auditQuality(ctx, client, qualityIn{Library: library, Limit: 1})

		return auditAllRow{Findings: quality.Found, Scanned: quality.Scanned, Note: fmt.Sprintf("below 720p or in a legacy codec; %d files never probed and so not judged, %d written over since the server saw them, neither counted here", quality.TotalUnprobed, quality.TotalReplaced), changed: quality.changed}, err
	})
	run("audit_missing_episodes", func() (auditAllRow, error) {
		missing, err := auditMissingEpisodes(ctx, client, nil, episodesIn{Library: library, Limit: 1})

		// the provider is not asked here, so a run is known only where the
		// server keeps records of it: the count is the gaps between files
		// and what those records list, and the shows whose run is unknown
		// may lack more than any count here can see
		missingNote := "series with episode numbers skipped between their files, or listed missing by the server's own records"
		if missing.TotalUnknown > 0 {
			missingNote += fmt.Sprintf("; runs not known: %d of the %d shows have no record of their run on the server, so only the gaps between their files were seen and they may lack more (TMDB is not asked here: audit_missing_episodes with provider true reads every show's run)", missing.TotalUnknown, missing.Series)
		}

		return auditAllRow{Findings: missing.Found, Scanned: missing.Scanned, Partial: !missing.RunsKnown, Note: missingNote, changed: missing.changed}, err
	})
	run("audit_spelling", spelling)
	run("audit_whitespace", spaces)
	// what nobody has watched is a row, not a defect: it is left out of the
	// total so a clean library totals zero
	steps = append(steps, auditAllStep{audit: "audit_unwatched", uncounted: true, row: func() (auditAllRow, error) {
		unwatched, err := auditUnwatched(ctx, client, unwatchedIn{Library: library, Limit: 1})
		note := fmt.Sprintf("films no account has watched or started: not a defect, what to archive or recommend; not in total_findings. %d more started and never finished are not counted here", unwatched.TotalStarted)
		if len(unwatched.Limited) > 0 {
			note += fmt.Sprintf("; %d accounts' views hide part of a library, and what they played there is not read", len(unwatched.Limited))
		}

		return auditAllRow{Findings: unwatched.Found, Scanned: unwatched.Scanned, Partial: len(unwatched.Limited) > 0, Note: note, changed: unwatched.Note}, err
	}})
	if library == "" {
		// the sweep audit_orphans counts, without the grouping into folders,
		// which asks the server about each folder and changes no number here
		run("audit_orphans", func() (auditAllRow, error) {
			_, orphans, swept, err := findOrphans(ctx, client)

			return auditAllRow{Findings: len(orphans), Scanned: swept.Read, Note: "items under folders no library covers; audit_orphans lists them, in the admin toolset", changed: swept.Changed()}, err
		})
	} else {
		skip("audit_orphans", "server-wide: run it without a library")
	}
	later()

	return steps
}

// spellingRow is audit_all's audit_spelling row: the groups spelled more than
// one way among the genres, tags and studios of what the library holds,
// albums included where it holds music, and what its read saw of the
// library changing under it.
func spellingRow(ctx context.Context, client *embyfin.Client, library string, folder *embyfin.VirtualFolder) (auditAllRow, error) {
	// the spellings apply to every kind of library, music with its albums;
	// were they ever not to, the row would say so rather than count nothing
	types, applies := auditAllTypes(folder, vocabularyTypes, musicAlbum)
	if !applies {
		return auditAllRow{Audit: "audit_spelling", Skipped: true, Note: "nothing in this library carries genres, tags or studios the audit reads"}, nil
	}
	spellings, scanned, note, err := spellingAudit(ctx, client, library, types, vocabFields)
	if err != nil {
		return auditAllRow{}, err
	}
	groups := 0
	for _, f := range vocabFields {
		groups += len(spellings.report(f))
	}

	return auditAllRow{Audit: "audit_spelling", Findings: groups, Scanned: scanned, Types: types, Note: "groups of genres, tags and studios spelled more than one way", changed: note}, nil
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

// runtimeOut is the runtime audit's worklist, and how many of the episodes it
// read it could not judge.
type runtimeOut struct {
	auditOut
	// counted in items_scanned and judged by nothing: left unsaid, a season
	// of never-probed files read as a season with nothing wrong in it
	Unprobed int `json:"unprobed" jsonschema:"episode files the server holds no runtime for (never probed, an import cut short or a scan that stopped): counted in items_scanned, but with no runtime to compare they are not judged, so none of them is known to be right"`
}

func registerRuntimeAudit(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_runtime",
		Description: "Find episodes whose runtime disagrees with their season's: truncated downloads, wrong files, or wrong matches, each compared to what its season typically runs (needs 3+ episodes), with no external data. " +
			"What a season typically runs is its largest group of like runtimes, not the median of them all, and a file under half of that is named as far shorter than the rest: an incomplete or wrong file. A season split between two lengths - a second group of two files or more holding over a quarter of it (previews beside whole episodes, double episodes) - is one finding naming both lengths and which episodes run each, and neither is judged by the other; a file running neither, or holding several episodes, is judged by the nearer on a row of its own. " +
			fmt.Sprintf("A duration too long to be any episode's (%d hours or more) is broken metadata and reported wherever it is, whatever the season holds. A show's extras, which Emby 4.10 holds as episodes when they sit in a season's Extras folder, are left out, and so are the server's records of episodes it has no file for. Files the server holds no runtime for are counted in unprobed and judged by nothing. A film's runtime against its provider's is audit_provider.", absurdRuntimeS/3600),
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
		out, err := auditEpisodeRuntimes(ctx, client, parent, in)

		return nil, out, err
	})
}

// runtimeGroup is a run of like runtimes in a season: the median of them,
// how many files run so, and the shortest and longest of them.
type runtimeGroup struct {
	median, files int
	lo, hi        int
}

// holds says whether a runtime is one of the group's.
func (g runtimeGroup) holds(minutes int) bool {
	return minutes >= g.lo && minutes <= g.hi
}

// typicalRuntime is what a season's single-episode files run: the median of
// its largest group of like runtimes, not the median of them all. A season
// whose files were mostly minute-long previews beside a few whole episodes
// had a median of a minute, and the whole episodes were reported as "3200%
// off". Runtimes group when each is within a quarter of the one before.
//
// other is set when a second group of two files or more holds more than a
// quarter of the season too: then the season is split between two lengths,
// and which of them is right is not for a runtime to say - half the files
// are previews, or double episodes, or another show's - so neither is judged
// by the other.
func typicalRuntime(mins []int) (typical runtimeGroup, other *runtimeGroup) {
	sorted := slices.Sorted(slices.Values(mins))
	var groups []runtimeGroup
	start := 0
	for i := 1; i <= len(sorted); i++ {
		if i < len(sorted) && (sorted[i]*4 <= sorted[i-1]*5 || sorted[i]-sorted[i-1] < minRuntimeDiffMinutes) {
			continue
		}
		groups = append(groups, runtimeGroup{median: sorted[start+(i-1-start)/2], files: i - start, lo: sorted[start], hi: sorted[i-1]})
		start = i
	}
	// the largest group; of two the same size, the longer, which a group of
	// cut files is not
	best := 0
	for i := range groups {
		if groups[i].files >= groups[best].files {
			best = i
		}
	}
	for i := range groups {
		if i != best && groups[i].files >= 2 && groups[i].files*4 > len(sorted) {
			return groups[best], &groups[i]
		}
	}

	return groups[best], nil
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

// runtimeEp is an episode file as the runtime audit judges it.
type runtimeEp struct {
	id, name, series, filePath string
	// code is the episode's numbers, S01E02, and episode its number alone,
	// E02, with ?? for a number the server does not hold
	code, episode string
	minutes, span int
}

// splitSeason is the finding for a season split between two lengths (see
// typicalRuntime): both lengths, and which episodes run each, judged by
// neither. Only files inside a length are listed under it; the rest - a
// file far from both, a file holding several episodes - are judged by the
// nearer length on rows of their own (see judgeRuntime).
func splitSeason(seriesID string, season int, eps []runtimeEp, group, other runtimeGroup) auditFinding {
	var ofGroup, ofOther []string
	folder := ""
	for _, e := range eps {
		if folder == "" {
			folder = parentDir(e.filePath)
		}
		for folder != "" && !within(e.filePath, folder) {
			folder = parentDir(folder)
		}
		if e.span != 1 {
			continue
		}
		switch code := e.episode; {
		case group.holds(e.minutes):
			ofGroup = append(ofGroup, code)
		case other.holds(e.minutes):
			ofOther = append(ofOther, code)
		}
	}
	slices.Sort(ofGroup)
	slices.Sort(ofOther)

	return auditFinding{
		ID: seriesID, Name: fmt.Sprintf("%s season %d", eps[0].series, season), Path: folder,
		Detail: fmt.Sprintf("the season is split between two lengths: %d files run about %d min (%s) and %d about %d min (%s). One set is not what the other is - cut files or previews, double episodes, or another show's - so neither is judged by the other: compare the files",
			group.files, group.median, firstFew(ofGroup), other.files, other.median, firstFew(ofOther)),
	}
}

// nearer is the one of a split season's two lengths an episode file is
// judged by: the one its runtime an episode is closest to.
func nearer(e runtimeEp, group, other runtimeGroup) (by, besides runtimeGroup) {
	each := e.minutes / e.span
	if max(each-other.median, other.median-each) < max(each-group.median, group.median-each) {
		return other, group
	}

	return group, other
}

// judgeRuntime says whether an episode file runs far from what its season
// runs, typical minutes an episode, and how: a file holding several episodes
// is expected to run that many times as long. besides is the other length of
// a season split between two (see typicalRuntime), 0 when it is not.
func judgeRuntime(e runtimeEp, typical, besides, tolerancePct int) (detail string, pct int, off bool) {
	expected := typical * e.span
	pct, off = runtimeOff(e.minutes, expected, tolerancePct)
	if !off {
		return "", 0, false
	}
	// under half of what the rest run is no cut of the episode: a download
	// stopped short, a preview, another file
	short := e.minutes*2 < expected
	if besides > 0 {
		switch {
		case e.span > 1 && short:
			return fmt.Sprintf("%d min for %d episodes, far shorter than the nearer of the two lengths its season runs, %d min each (%d expected; the other %d): an incomplete or wrong file", e.minutes, e.span, typical, expected, besides), pct, true
		case e.span > 1:
			return fmt.Sprintf("%d min for %d episodes, where the nearer of the two lengths its season runs is %d min each, %d expected (the other %d; %d%% off)", e.minutes, e.span, typical, expected, besides, pct), pct, true
		case short:
			return fmt.Sprintf("%d min, far shorter than the nearer of the two lengths its season runs (%d min; the other %d): an incomplete or wrong file", e.minutes, typical, besides), pct, true
		}

		return fmt.Sprintf("%d min, where the nearer of the two lengths its season runs is %d min (the other %d; %d%% off)", e.minutes, typical, besides, pct), pct, true
	}
	switch {
	case e.span > 1 && short:
		return fmt.Sprintf("%d min for %d episodes, far shorter than the rest of its season, which run %d min each (%d expected): an incomplete or wrong file", e.minutes, e.span, typical, expected), pct, true
	case e.span > 1:
		return fmt.Sprintf("%d min for %d episodes, where its season runs %d min each, %d expected (%d%% off)", e.minutes, e.span, typical, expected, pct), pct, true
	case short:
		return fmt.Sprintf("%d min, far shorter than the rest of its season, which run %d min: an incomplete or wrong file", e.minutes, typical), pct, true
	}

	return fmt.Sprintf("%d min, where its season runs %d min (%d%% off)", e.minutes, typical, pct), pct, true
}

func auditEpisodeRuntimes(ctx context.Context, client *embyfin.Client, parent string, in runtimeIn) (runtimeOut, error) {
	type ep = runtimeEp
	type seasonKey struct {
		series string
		season int
	}

	type scored struct {
		finding auditFinding
		pct     int
	}
	var findings []scored
	name := func(e ep) string { return fmt.Sprintf("%s %s %s", e.series, e.code, e.name) }

	seasons := map[seasonKey][]ep{}
	out := runtimeOut{Findings: []auditFinding{}}
	swept, sweepErr := client.ReadAll(ctx, embyfin.SearchOptions{
		IncludeItemTypes: "Episode",
		ParentID:         parent,
		Fields:           "Path",
	}, embyfin.ToAnswer, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			// a featurette Emby took for an episode runs as long as it runs,
			// and a record of an episode with no file runs as long as its
			// provider says, which is nothing on disk
			if extraEpisode(it) || !it.HasFile() {
				continue
			}
			out.Scanned++
			if it.RunTimeTicks <= 0 {
				out.Unprobed++

				continue
			}
			e := ep{
				id: it.ID, name: it.Name, series: it.SeriesName, filePath: it.Path,
				code: episodeCode(it), episode: "E" + numberText(it.IndexNumber), minutes: it.RuntimeMinutes(),
				// a file the server recorded as holding several episodes is
				// expected to run that many times the median, not once
				span: max(len(episodeSpan(it)), 1),
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
			// specials (season 0) have no typical length, so there is nothing
			// to compare to, and an episode with no season number has no
			// season to compare with
			if it.SeriesID == "" || it.ParentIndexNumber == nil || *it.ParentIndexNumber == 0 {
				continue
			}
			k := seasonKey{it.SeriesID, *it.ParentIndexNumber}
			seasons[k] = append(seasons[k], e)
		}
		return true
	})
	if sweepErr != nil {
		return runtimeOut{}, sweepErr
	}
	out.Note = swept.Changed()

	for key, eps := range seasons {
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
		group, other := typicalRuntime(mins)
		if other != nil {
			findings = append(findings, scored{pct: brokenRuntimeRank - 1, finding: splitSeason(key.series, key.season, eps, group, *other)})
		}

		for _, e := range eps {
			if e.minutes >= absurdRuntimeMinutes {
				continue // reported as broken already
			}
			typical, besides := group.median, 0
			if other != nil {
				// a file inside one of a split season's lengths is that
				// length's, judged by neither; the rest by the nearer
				if e.span == 1 && (group.holds(e.minutes) || other.holds(e.minutes)) {
					continue
				}
				by, rest := nearer(e, group, *other)
				typical, besides = by.median, rest.median
			}
			detail, pct, off := judgeRuntime(e, typical, besides, in.TolerancePct)
			if !off {
				continue
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
func tmdbFacts(opts Options, rt http.RoundTripper) *tmdb.Facts {
	if opts.TMDBKey == "" {
		return nil
	}
	facts, err := tmdb.NewFacts(opts.TMDBKey, rt)
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
