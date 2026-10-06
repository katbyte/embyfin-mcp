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

	"github.com/katbyte/embyfin-mcp/lib/mediapath"
	"github.com/katbyte/embyfin-mcp/lib/naming"
	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/katbyte/embyfin-mcp/sdk/tmdb"
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
	// Problems are the problems an item has, for an audit that checks
	// several (audit_missing_metadata)
	Problems []string `json:"problems,omitempty" jsonschema:"audit_missing_metadata: which of the problems asked about the item has, of provider_id, poster and overview"`
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

	folder, err := client.ResolveLibrary(ctx, in.Library)
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
		items, note, placing, serr := client.Shown(ctx, opts)
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
				names = append(names, mediapath.Base(s.Path))
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
	check := r.newTitleCheck()

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
	registerMissingAudit(r)
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

	check := r.newTitleCheck()
	type dupOut struct {
		Scanned      int             `json:"items_scanned"`
		TotalGroups  int             `json:"total_findings"      jsonschema:"groups and folder_groups together, before the limit"`
		Groups       [][]itemSummary `json:"groups"              jsonschema:"each group shares one metadata provider id; capped at limit, total_findings is the real count"`
		TotalFolders int             `json:"total_folder_groups" jsonschema:"folder_groups before the limit; counted in total_findings too"`
		FolderGroups []folderGroup   `json:"folder_groups"       jsonschema:"series the server holds twice because two folders beside each other name the same show, their names apart only in spacing, case, an accent or punctuation: a rename that left the old folder behind. Found by the folder names alone, so a second entry with no provider id, which is usual, is found here when groups cannot see it. Capped at limit"`
		Note         string          `json:"note,omitempty"      jsonschema:"set when the library was seen to change while it was read: items added or removed meanwhile may be missing, or listed though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read. On Emby, an audit of what people are shown also says how the items shown only as versions of others were placed: by the key Emby merges them by, with a sample checked against a read of each, or by a read of each"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_duplicates",
		Description: "Find separate entries sharing the same tmdb/imdb/tvdb id: multiple copies of one film, series or episode, in one library or across libraries (compare the paths). " +
			"A tmdb or tvdb id only joins entries of one kind, a film to a film and a series to a series, because both providers number films and TV apart; an imdb id joins any. " +
			"Episodes are grouped by provider id AND series name AND season and episode number, because a library can carry one shared id across unrelated episodes. " +
			"Items the server shows as one title's versions are not entries of their own (Emby merges them only in what it shows people, so on Emby this reads the library as the first administrator is shown it): audit_multiple_versions lists those. " +
			"folder_groups are the shows held twice because two folders name the same series: a rename that changed only spacing, case, an accent or punctuation leaves the old folder behind and a second entry is built from it, and the episodes are then split across both entries, so each answers 'no' to half the questions asked of it. Sharing an id cannot see these when the second entry carries no provider id, which is usual, so they are found by their folder names; left out when types leaves Series out. " +
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
		folders, err := duplicateFolders(ctx, client, in.Library, in.Types, limit)
		if err != nil {
			return nil, dupOut{}, err
		}

		out := dupOut{Scanned: scanned, TotalGroups: len(groups) + folders.Found, TotalFolders: folders.Found, FolderGroups: folders.Groups, Note: joinWarnings(joinWarnings(note, placing), folders.Note)}
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

// The problems audit_missing_metadata looks for, in the order a finding
// lists them.
const (
	missingProviderID = "provider_id"
	missingPoster     = "poster"
	missingOverview   = "overview"
)

var missingProblems = []string{missingProviderID, missingPoster, missingOverview}

// parseProblems reads the problems a caller named, comma-separated and in
// any case, in missingProblems' order; none named is all of them.
func parseProblems(s string) ([]string, error) {
	named := map[string]bool{}
	for p := range strings.SplitSeq(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if !slices.Contains(missingProblems, p) {
			return nil, fmt.Errorf("unknown problem %q; choose from: %s", p, strings.Join(missingProblems, ", "))
		}
		named[p] = true
	}
	if len(named) == 0 {
		return missingProblems, nil
	}

	return slices.DeleteFunc(slices.Clone(missingProblems), func(p string) bool { return !named[p] }), nil
}

// noPoster flags an item with no primary image.
func noPoster(it *embyfin.Item) (string, bool) {
	if it.ImageTags["Primary"] == "" {
		return "no primary image", true
	}

	return "", false
}

// noOverview flags an item with no overview, or one of nothing but spaces:
// Jellyfin keeps one as it was sent, Emby keeps none.
func noOverview(it *embyfin.Item) (string, bool) {
	if strings.TrimSpace(it.Overview) == "" {
		return "no overview", true
	}

	return "", false
}

// metadataIn is audit_missing_metadata's input: the shared options, the
// problems to look for, the providers provider_id asks after, and the
// libraries to leave out.
type metadataIn struct {
	Library  string   `json:"library,omitempty"  jsonschema:"restrict to one library by name or id"`
	Types    string   `json:"types,omitempty"    jsonschema:"comma-separated item types to audit; defaults to Movie,Series"`
	Problems string   `json:"problems,omitempty" jsonschema:"comma-separated problems to look for: provider_id (no metadata provider id of any kind - a link to its website or social pages is not one - so unmatched, and item_identify finds it), poster (no primary image: item_artwork_set fixes it), overview (no overview or plot text: item_refresh or item_identify where the library's metadata fetchers are on, item_edit sets one by hand). Default all three"`
	Missing  string   `json:"missing,omitempty"  jsonschema:"provider_id: comma-separated providers the item has none of: tmdb, imdb, tvdb, anidb, myanimelist, so missing=tmdb also finds items matched elsewhere but not on TMDB. Default: no provider id of any kind"`
	Ignore   []string `json:"ignore,omitempty"   jsonschema:"libraries to leave out, by name or id: ones whose items never carry an id, such as a YouTube library"`
	Limit    int      `json:"limit,omitempty"    jsonschema:"maximum findings to return, default 100"`
}

// metadataOut is audit_missing_metadata's worklist: one finding an item,
// naming each of its problems, and how many items have each.
type metadataOut struct {
	Scanned   int            `json:"items_scanned"`
	Found     int            `json:"total_findings" jsonschema:"items with any of the problems asked about"`
	ByProblem map[string]int `json:"by_problem"     jsonschema:"items with each problem, provider_id, poster and overview; an item with two counts under both"`
	Findings  []auditFinding `json:"findings"       jsonschema:"one an item, its problems listed; capped at limit, total_findings is the real count"`
	Note      string         `json:"note,omitempty" jsonschema:"set when the library was seen to change while it was read: items added or removed meanwhile may be missing, or listed though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read"`
	changed   string
}

// auditMissing sweeps the library for the items missing what a matched,
// finished item has: a provider id, a poster, an overview.
func auditMissing(ctx context.Context, client *embyfin.Client, in metadataIn) (metadataOut, error) {
	problems, err := parseProblems(in.Problems)
	if err != nil {
		return metadataOut{}, err
	}
	missing, err := parseProviders(in.Missing)
	if err != nil {
		return metadataOut{}, err
	}
	ignored, err := libraryFolders(ctx, client, in.Ignore)
	if err != nil {
		return metadataOut{}, err
	}
	checks := map[string]func(*embyfin.Item) (string, bool){missingProviderID: noProviderID, missingPoster: noPoster, missingOverview: noOverview}
	if len(missing) > 0 {
		checks[missingProviderID] = missingProviders(missing)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	opts := embyfin.SearchOptions{IncludeItemTypes: cmp.Or(in.Types, "Movie,Series"), Fields: embyfin.FieldsLean + ",ImageTags"}
	folder, err := client.ResolveLibrary(ctx, in.Library)
	if err != nil {
		return metadataOut{}, err
	}
	if folder != nil {
		opts.ParentID = folder.ItemID
	}

	out := metadataOut{ByProblem: map[string]int{}, Findings: []auditFinding{}}
	for _, p := range problems {
		out.ByProblem[p] = 0
	}
	read, err := client.ReadAll(ctx, opts, embyfin.ToAnswer, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			if _, under := inLibrary(it.Path, ignored); len(ignored) > 0 && under {
				continue
			}
			out.Scanned++
			var has, details []string
			for _, p := range problems {
				if detail, bad := checks[p](it); bad {
					has, details = append(has, p), append(details, detail)
					out.ByProblem[p]++
				}
			}
			if len(has) == 0 {
				continue
			}
			out.Found++
			if len(out.Findings) < limit {
				out.Findings = append(out.Findings, auditFinding{ID: it.ID, Name: it.Name, Year: it.ProductionYear, Path: it.Path, Detail: strings.Join(details, "; "), Problems: has})
			}
		}

		return true
	})
	if err != nil {
		return metadataOut{}, err
	}
	out.changed = read.Changed()
	out.Note = out.changed

	return out, nil
}

// registerMissingAudit adds audit_missing_metadata.
func registerMissingAudit(r *registry) {
	client := r.client
	add(r, readTool, &mcp.Tool{
		Name: "audit_missing_metadata",
		Description: "Sweep the library for items missing what a matched, finished item has: a metadata provider id of any kind (a link to its website or social pages is not one), so unmatched items that need identification (item_identify); a primary poster image (item_artwork_set fixes them); an overview or plot text, usually a sign of a failed metadata match (item_refresh or item_identify fixes them where the library's metadata fetchers are on; item_edit sets one by hand). " +
			"problems picks which to look for, and each finding lists the problems its item has; by_problem counts the items with each. missing drills provider_id down to the providers named, so missing=tmdb also finds items matched elsewhere but not on TMDB; each such finding lists the ids the item does have. ignore leaves whole libraries out by their folders.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in metadataIn) (*mcp.CallToolResult, metadataOut, error) {
		out, err := auditMissing(ctx, client, in)

		return nil, out, err
	})
}

// libraryFolders are the folders of the libraries named, by name or id. A
// name that matches no library is refused: leaving out nothing, when the
// caller meant to leave something out, reads as a finding.
func libraryFolders(ctx context.Context, client *embyfin.Client, names []string) ([]embyfin.LibraryPath, error) {
	var folders []embyfin.LibraryPath
	for _, name := range names {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		// by its folders, not its id, so a library listed without one can be
		// left out too
		folder, err := client.FindLibrary(ctx, name)
		if err != nil {
			return nil, err
		}
		for _, loc := range folder.Locations {
			if loc = mediapath.Trim(loc); loc != "" {
				folders = append(folders, embyfin.LibraryPath{Library: folder.Name, Path: loc})
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

	folder, err := client.ResolveLibrary(ctx, library)
	if err != nil {
		return nil, 0, "", "", err
	}
	if folder != nil {
		opts.ParentID = folder.ItemID
	}

	// as the server shows them: two files Emby shows as one film's versions
	// are versions (audit_multiple_versions), not two entries
	opts.Fields = embyfin.FieldsDefault + ",SortName"
	all, note, placing, err := client.Shown(ctx, opts)
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
			run = fmt.Sprintf("; %s is held as episodes %d to %d, and may hold them all", mediapath.Base(it.Path), *it.IndexNumber, it.IndexNumberEnd)

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
func seriesClaim(it *embyfin.Item) (why string, c naming.Claim, other bool) {
	if it.Path == "" {
		return "", naming.Claim{}, false
	}
	c, ok := naming.ClaimOf(it.Path, heldTitles(it))
	if !ok || mediapath.Base(it.Path) != c.Segment {
		return "", c, false
	}
	titleOff := c.Title != "" && c.Score < seriesConfident
	yearOff := it.ProductionYear > 0 && abs(c.Year-it.ProductionYear) > 1
	if !titleOff && !yearOff {
		return "", c, false
	}
	held := it.Name
	if it.ProductionYear > 0 {
		held = fmt.Sprintf("%s (%d)", it.Name, it.ProductionYear)
	}
	named := cmp.Or(c.Title, c.Segment)
	if titleOff {
		named = c.Named(heldTitles(it))
	}

	return fmt.Sprintf("%q is named for %q (%d), not %s", c.Segment, named, c.Year, held), c, true
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
	Types    string `json:"types,omitempty"    jsonschema:"the item types the row counted, when they are not the audit's own default: pass them to the audit as types for the worklist behind the count"`
	Where    string `json:"where,omitempty"    jsonschema:"audit_whitespace: the places the row counted, every one but the file names: pass them to it as where for the worklist behind the count"`
	Problems string `json:"problems,omitempty" jsonschema:"audit_missing_metadata: the one problem the row counted, provider_id, poster or overview, one row each: pass it to the audit as problems for the worklist behind the count"`
	Skipped  bool   `json:"skipped,omitempty"  jsonschema:"true when the audit was not run here, or its read stopped short and it has no count; note says why"`
	Partial  bool   `json:"partial,omitempty"  jsonschema:"true when the count covers only part of what the audit checks - episodes with no runtime to judge, shows whose run is not known - so a small count is not a clean result; note says what was left out"`
	Note     string `json:"note,omitempty"     jsonschema:"why an audit was skipped, or what its count means, and what its read saw when the library was seen to change under it"`
	// an audit that failed is a row of its own, not the end of the call
	Failed bool     `json:"failed,omitempty" jsonschema:"true when the audit ran and failed: findings and items_scanned are then not counts, error says what failed, and the audit can be run on its own"`
	Error  string   `json:"error,omitempty"  jsonschema:"what failed, when failed is set"`
	Took   *float64 `json:"took_s,omitempty" jsonschema:"how long the audit took, in seconds, whether it counted or failed; not set on a row skipped"`
	// changed is what the audit's read saw of the library changing under
	// it, or could not tell; "" when nothing was seen to come or go
	changed string
}

// auditAllKey names one of audit_all's rows: an audit, and for
// audit_missing_metadata the one problem the row counts.
type auditAllKey struct {
	audit, problems string
}

// auditAllRows is every row audit_all makes, in its order.
func auditAllRows() []auditAllKey {
	rows := make([]auditAllKey, 0, len(auditChecks)+17)
	for _, problem := range missingProblems {
		rows = append(rows, auditAllKey{audit: "audit_missing_metadata", problems: problem})
	}
	for i := range auditChecks {
		rows = append(rows, auditAllKey{audit: auditChecks[i].name})
	}
	for _, audit := range []string{
		"audit_file_path", "audit_duplicates", "audit_duplicate_episodes", "audit_disc_folders", "audit_runtime", "audit_quality", "audit_missing_episodes",
		"audit_spelling", "audit_whitespace", "audit_unwatched", "audit_orphans", "audit_language", "audit_provider", "audit_anime_ids",
	} {
		rows = append(rows, auditAllKey{audit: audit})
	}

	return rows
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
	// problems is the one problem an audit_missing_metadata row counts
	problems string
	skip     string // why the audit is not run here; "" runs it
	row      func() (auditAllRow, error)
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
			out.Audits = append(out.Audits, auditAllRow{Audit: s.audit, Problems: s.problems, Skipped: true, Note: s.skip})

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
		row.Audit, row.Problems = s.audit, s.problems
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
			"Every audit has a row, the orphans check included when no library is given (it is server-wide). The ones that need more than the server are listed as skipped with why: audit_language needs a language to ask about, audit_provider asks the provider about each film and each series and is paged, and audit_anime_ids reads the Anime-Lists file. " +
			"In a music library the audits that apply to music (missing covers and spellings) count its albums, and the rest are skipped as having nothing there to read; over every library, or a mixed one, those two count albums beside films and series. audit_whitespace reads music as it reads the rest, and its row leaves out the file names, which are one row an item, and says how many there are. A library of a kind no audit reads (home videos, music videos, books, photos) has every row skipped as not checked, and over every library each such library is named in libraries_not_checked. A row counted over other types than the audit's default names them in types, and one counted over some of the places an audit reads names them in where, to pass to the audit for its worklist. " +
			"A row marked partial counted only part of what its audit checks - files with no media facts, shows whose run is not known (the missing-episodes row does not ask TMDB, so it sees only gaps between files unless the server keeps records) - and its note says what was left out: a small count there is not a clean result. " +
			"Every row that ran says how long it took (took_s). An audit that fails does not end the call: its row says failed with the error, the others still count, and failed lists them - total_findings is then short by whatever they would have found, so run a failed one on its own.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditAllIn) (*mcp.CallToolResult, auditAllOut, error) {
		folder, err := client.ResolveLibrary(ctx, in.Library)
		if err != nil {
			return nil, auditAllOut{}, err
		}
		// in a library of a kind no audit reads, every row says so, and over
		// every library each such library is named: a 0 read over home videos
		// or books is not a clean library, it is one nothing looked at
		if folder != nil && !auditedKind(folder.CollectionType) {
			notKind := fmt.Sprintf("not checked for this library type: a %s library holds none of the films, series, episodes or albums the audits read", folder.CollectionType)
			out := auditAllOut{Audits: []auditAllRow{}}
			for _, row := range auditAllRows() {
				note := notKind
				if row.audit == "audit_orphans" {
					note = "server-wide: run it without a library"
				}
				out.Audits = append(out.Audits, auditAllRow{Audit: row.audit, Problems: row.problems, Skipped: true, Note: note})
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

	// a row a problem of the missing-metadata audit: the poster one reads
	// albums too, where there are any, and the others have nothing to say
	// about music
	for _, problem := range missingProblems {
		music := ""
		if problem == missingPoster {
			music = musicAlbum
		}
		types, applies := auditAllTypes(folder, "Movie,Series", music)
		if !applies {
			steps = append(steps, auditAllStep{audit: "audit_missing_metadata", problems: problem, skip: notMusic})

			continue
		}
		steps = append(steps, auditAllStep{audit: "audit_missing_metadata", problems: problem, row: func() (auditAllRow, error) {
			res, err := auditMissing(ctx, client, metadataIn{Library: library, Types: types, Problems: problem, Limit: 1})

			return auditAllRow{Findings: res.ByProblem[problem], Scanned: res.Scanned, Types: types, changed: res.changed}, err
		}})
	}
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
		skip("audit_provider", "asks the provider about each film and each series and is paged: run it on its own")
		skip("audit_anime_ids", "reads the Anime-Lists file from the web: run it on its own")
	}
	if music {
		// the rest read films, series and episodes alone, but for the
		// spellings of the genres, tags and studios an album carries
		for _, audit := range []string{"audit_file_path", "audit_duplicates", "audit_duplicate_episodes", "audit_disc_folders", "audit_runtime", "audit_quality", "audit_missing_episodes"} {
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
		paths, err := auditFilePath(ctx, client, nil, pathIn{Library: library, Limit: 1})

		return auditAllRow{Findings: paths.Found, Scanned: paths.Scanned, Note: "items whose path disagrees with their metadata - title, year, series, season or episode - or whose name has a letter that only looks Latin; TMDB is not asked here, so a path named by a title only TMDB lists for the item is still counted", changed: paths.changed}, err
	})
	// films, series AND episodes: this pass used to ask for the default
	// Movie,Series, so it reported the series count as items_scanned and an
	// episode held twice was invisible from the call that says "start here"
	run("audit_duplicates", func() (auditAllRow, error) {
		// how Emby's versions were placed is said by audit_duplicates itself,
		// and is not the library changing
		groups, scanned, note, _, err := duplicateGroups(ctx, client, library, "")
		if err != nil {
			return auditAllRow{}, err
		}
		folders, err := duplicateFolders(ctx, client, library, "", 1)
		if err != nil {
			return auditAllRow{}, err
		}

		return auditAllRow{Findings: len(groups) + folders.Found, Scanned: scanned, Note: fmt.Sprintf("groups of entries sharing a provider id, films series and episodes (%d), and series held twice from two folders of one name (%d, its folder_groups)", len(groups), folders.Found), changed: joinWarnings(note, folders.Note)}, nil
	})
	run("audit_duplicate_episodes", func() (auditAllRow, error) {
		titles, err := auditDuplicateEpisodes(ctx, client, dupTitlesIn{Library: library, Limit: 1})

		return auditAllRow{Findings: titles.Found, Scanned: titles.Scanned, Note: "groups of one season's entries sharing a title: near certain, leads, and titles whose entries run more than 15% apart (far_apart)", changed: titles.changed}, err
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
		runtimes, err := auditRuntimes(ctx, client, parent, 1)

		return auditAllRow{
			Findings: runtimes.Found, Scanned: runtimes.Scanned, Partial: runtimes.Unprobed > 0, changed: runtimes.Note,
			Note: fmt.Sprintf("films and episodes too short (under %d minutes) or too long (%d hours or more) to be one; files the server holds no runtime for (never probed), counted in items_scanned and not judged: %d", shortestRuntimeS/60, absurdRuntimeS/3600, runtimes.Unprobed),
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
// albums included where it holds music, and there the album and artist
// names in its tracks' tags too, and what its reads saw of the library
// changing under them.
func spellingRow(ctx context.Context, client *embyfin.Client, library string, folder *embyfin.VirtualFolder) (auditAllRow, error) {
	// the spellings apply to every kind of library, music with its albums;
	// were they ever not to, the row would say so rather than count nothing
	types, applies := auditAllTypes(folder, vocabularyTypes, musicAlbum)
	if !applies {
		return auditAllRow{Audit: "audit_spelling", Skipped: true, Note: "nothing in this library carries genres, tags or studios the audit reads"}, nil
	}
	fields, what := vocabFields, "groups of genres, tags and studios spelled more than one way"
	if slices.Contains(strings.Split(types, ","), musicAlbum) {
		fields, what = spellingFieldsAll, "groups of genres, tags, studios, album names and artist names spelled more than one way"
	}
	sweep, err := spellingAudit(ctx, client, library, types, fields)
	if err != nil {
		return auditAllRow{}, err
	}

	return auditAllRow{Audit: "audit_spelling", Findings: len(sweep.groups(fields)), Scanned: sweep.items, Types: types, Note: what, changed: sweep.note}, nil
}

// runtime audit -----------------------------------------------------------

// The runtime audit reports only a length no film or episode can have: one
// so short it is a broken or cut-off file, or one so long it is broken
// duration metadata. It judges nothing against the season or the library
// around it, which runs as its files run and says nothing true about any one
// of them: a length is judged against TMDB's for that film or episode, in
// audit_provider, or not at all.

const (
	// shortestRuntimeS is the length under which a file is no film and no
	// episode: a download cut off, a sample, a broken file
	shortestRuntimeS = 2 * 60
	// absurdRuntimeS is where a runtime stops being long and becomes broken
	// metadata: an "episode" or a film that runs for half a day or weeks
	absurdRuntimeS = 12 * 60 * 60

	defaultRuntimeTolerancePct = 20
	minRuntimeDiffMinutes      = 2
	defaultTMDBLookups         = 250
)

type runtimeIn struct {
	Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum findings to return, default 100"`
}

// runtimeOut is the runtime audit's worklist, and how many of the files it
// read it could not judge.
type runtimeOut struct {
	auditOut
	// counted in items_scanned and judged by nothing: left unsaid, a library
	// of never-probed files read as one with nothing wrong in it
	Unprobed int `json:"unprobed" jsonschema:"files the server holds no runtime for (never probed, an import cut short or a scan that stopped): counted in items_scanned, but with no runtime they are not judged, so none of them is known to be right"`
}

func registerRuntimeAudit(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_runtime",
		Description: fmt.Sprintf("Find films and episodes whose runtime no film or episode can have: under %d minutes, a download cut off, a sample or a broken file; or %d hours or more, broken duration metadata. ", shortestRuntimeS/60, absurdRuntimeS/3600) +
			"Nothing is judged against its season or its library, whose other files say nothing true about one file's length: a length against TMDB's for that film or episode is audit_provider. " +
			"A show's extras, which Emby 4.10 holds as episodes when they sit in a season's Extras folder, are left out, and so are the server's records of episodes it has no file for. Files the server holds no runtime for are counted in unprobed and judged by nothing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in runtimeIn) (*mcp.CallToolResult, runtimeOut, error) {
		if in.Limit <= 0 {
			in.Limit = 100
		}

		folder, err := client.ResolveLibrary(ctx, in.Library)
		if err != nil {
			return nil, runtimeOut{}, err
		}
		parent := ""
		if folder != nil {
			parent = folder.ItemID
		}
		out, err := auditRuntimes(ctx, client, parent, in.Limit)

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

// runtimeSaid is a length as a finding says it: seconds under two minutes,
// where a minute rounded off is half the length, minutes under an hour, and
// hours and minutes past that.
func runtimeSaid(seconds int) string {
	switch {
	case seconds < shortestRuntimeS:
		return strconv.Itoa(seconds) + " s"
	case seconds < 60*60:
		return fmt.Sprintf("%d min", seconds/60)
	}

	return fmt.Sprintf("%d h %d min", seconds/3600, seconds%3600/60)
}

// auditRuntimes sweeps the films and episodes with a file and reports those
// whose runtime is too short or too long to be one: the broken long first,
// then the shortest.
func auditRuntimes(ctx context.Context, client *embyfin.Client, parent string, limit int) (runtimeOut, error) {
	type scored struct {
		finding auditFinding
		seconds int
	}
	var long, short []scored
	out := runtimeOut{Findings: []auditFinding{}}
	swept, err := client.ReadAll(ctx, embyfin.SearchOptions{
		IncludeItemTypes: "Movie,Episode",
		ParentID:         parent,
		Fields:           "Path,ProductionYear",
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
			seconds := int(it.RunTimeTicks / ticksPerSecond)
			f := auditFinding{ID: it.ID, Name: it.Name, Year: it.ProductionYear, Path: it.Path}
			kind := "film"
			if it.Type == typeEpisode {
				f.Name, f.Year, kind = fmt.Sprintf("%s %s %s", it.SeriesName, episodeCode(it), it.Name), 0, "episode"
			}
			switch {
			case seconds >= absurdRuntimeS:
				f.Detail = runtimeSaid(seconds) + ": not a runtime, the file's duration metadata is broken"
				long = append(long, scored{f, seconds})
			case seconds < shortestRuntimeS:
				f.Detail = fmt.Sprintf("%s: too short to be the %s, an incomplete, sample or broken file", runtimeSaid(seconds), kind)
				short = append(short, scored{f, seconds})
			}
		}

		return true
	})
	if err != nil {
		return runtimeOut{}, err
	}
	out.Note = swept.Changed()

	// the broken durations first, then the shortest files; the name and id
	// settle ties, so a limit keeps the same ones on every call
	byName := func(a, b scored) int {
		return cmp.Or(strings.Compare(a.finding.Name, b.finding.Name), strings.Compare(a.finding.ID, b.finding.ID))
	}
	slices.SortFunc(long, func(a, b scored) int { return cmp.Or(cmp.Compare(b.seconds, a.seconds), byName(a, b)) })
	slices.SortFunc(short, func(a, b scored) int { return cmp.Or(cmp.Compare(a.seconds, b.seconds), byName(a, b)) })
	all := slices.Concat(long, short)
	out.Found = len(all)
	for _, f := range all[:min(len(all), limit)] {
		out.Findings = append(out.Findings, f.finding)
	}

	return out, nil
}

// tmdbFacts is how the tools ask TMDB, or nil when no token is set.
// providerCache is TMDB's answers as a tool keeps them: forgotten on
// Clear, which says how many there were.
type providerCache interface{ Clear() int }

// remember keeps a cache the tools made, for provider_cache_clear.
func (r *registry) remember(c providerCache) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()

	r.providerCaches = append(r.providerCaches, c)
}

// tmdbFacts is tmdbFacts, kept for provider_cache_clear.
func (r *registry) tmdbFacts(rt http.RoundTripper) *tmdb.Facts {
	facts := tmdbFacts(r.opts, rt)
	if facts != nil {
		r.remember(facts)
	}

	return facts
}

// newTitleCheck is newTitleCheck, its titles kept for provider_cache_clear.
func (r *registry) newTitleCheck() *titleCheck {
	check := newTitleCheck(r.opts)
	if check.titles != nil {
		r.remember(check.titles)
	}

	return check
}

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
