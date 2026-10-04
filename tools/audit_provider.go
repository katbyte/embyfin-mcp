package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit_provider: films and episodes against their metadata provider.
//
// A film matched by hand, or by a server that guessed, can hold a TMDB id
// for one film and an IMDb id for another, or an IMDb id that is not a film
// at all but a series or an episode of one. Nothing on the server shows it:
// the item reads as matched. TMDB, asked about either id, says what it is,
// and the same record says how long the film runs, which a truncated
// download or a wrong file does not. An episode's length is TMDB's for that
// episode, from its series' season reads: the one length there is a fact to
// judge a file by, where its season's other files are only more files.

// checkMovieIDs asks TMDB about a film's ids and says what is wrong with
// them, "" when nothing is. An IMDb id TMDB cannot place is not reported:
// that is as likely TMDB lacking the film as the id being wrong.
func checkMovieIDs(ctx context.Context, provider *tmdb.Facts, tmdbID, imdbID string) (string, error) {
	if tmdbID != "" {
		m, err := provider.Movie(ctx, tmdbID)
		switch {
		case err != nil:
			return "", err
		case m.ID == 0:
			return fmt.Sprintf("TMDB has no film %s: the id is wrong, or the film was taken down", tmdbID), nil
		case imdbID != "" && m.IMDbID != "" && !strings.EqualFold(m.IMDbID, imdbID):
			return fmt.Sprintf("its TMDB id is %d, %s (%d), whose IMDb id is %s, not the %s it holds: one of the two is wrong", m.ID, m.Title, m.Year, m.IMDbID, imdbID), nil
		}

		return "", nil
	}

	f, err := provider.Find(ctx, "imdb_id", imdbID)
	switch {
	case err != nil:
		return "", err
	case len(f.Movies) > 0:
		return "", nil
	case len(f.Series) > 0:
		s := f.Series[0]
		return fmt.Sprintf("its IMDb id %s is a series, not a film: %s, TMDB tv %d", imdbID, s.Name, s.Id), nil
	case len(f.Episodes) > 0:
		e := f.Episodes[0]
		return fmt.Sprintf("its IMDb id %s is an episode, not a film: %q, S%02dE%02d of TMDB tv %d", imdbID, e.Name, e.SeasonNumber, e.EpisodeNumber, e.ShowId), nil
	}

	return "", nil
}

// providerAuditIn is audit_provider's input.
type providerAuditIn struct {
	Library      string   `json:"library,omitempty"           jsonschema:"one library by name or id; default every library"`
	IDs          []string `json:"ids,omitempty"               jsonschema:"only these films or episodes, by id: a handful checked without sweeping a library"`
	Types        string   `json:"types,omitempty"             jsonschema:"comma-separated: Movie, Episode; default both"`
	Provider     string   `json:"provider,omitempty"          jsonschema:"the provider to ask: tmdb is the only one yet, and the default"`
	Checks       string   `json:"checks,omitempty"            jsonschema:"comma-separated: ids (the ids a film holds agree with each other and exist; films only), runtime (the file's runtime against the provider's for that film or episode); default both"`
	TolerancePct int      `json:"tolerance_percent,omitempty" jsonschema:"runtime: flag when the file differs from the provider's runtime by more than this percent (and 2 minutes or more), default 20"`
	Limit        int      `json:"limit,omitempty"             jsonschema:"maximum findings, default 50"`
	MaxLookups   int      `json:"max_lookups,omitempty"       jsonschema:"films and series to ask the provider about in this call, default 250: a film is one lookup, a series one for all its episodes"`
	Offset       int      `json:"offset,omitempty"            jsonschema:"where to go on from: a previous call's next_offset"`
}

type providerFinding struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Year     int      `json:"year,omitempty"`
	Path     string   `json:"path,omitempty"`
	Holds    string   `json:"holds"          jsonschema:"the ids the film holds, or for an episode its series' TMDB id"`
	Problems []string `json:"problems"       jsonschema:"each begins with the check that failed: ids or runtime"`
}

type providerAuditOut struct {
	Scanned    int               `json:"items_scanned"`
	Found      int               `json:"total_findings"        jsonschema:"among the films and episodes this call asked about; the rest wait for next_offset"`
	ByCheck    map[string]int    `json:"by_check"              jsonschema:"findings by the check that failed; a film failing both counts under both"`
	Unjudged   int               `json:"runtime_not_judged"    jsonschema:"films and episodes scanned whose runtime was not judged, for want of a length on one side: no TMDB id (on the film, or on the episode's series), TMDB holds no length for it, or the server holds none for the file (never probed). None of them is known to be right"`
	Findings   []providerFinding `json:"findings"              jsonschema:"capped at limit"`
	NextOffset int               `json:"next_offset,omitempty" jsonschema:"pass back as offset to go on; absent when the sweep finished"`
	Note       string            `json:"note,omitempty"        jsonschema:"set when the library was seen to change while this call read its films: films added or removed meanwhile may be missing, or listed though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read"`
}

// providerChecks are the checks, in the order a row lists what it found.
var providerChecks = []string{"ids", "runtime"}

func registerProviderCheckAudit(r *registry) {
	client := r.client
	provider := r.tmdbFacts(r.opts.ProviderTransport)

	desc := "Check films and episodes against their metadata provider. A film, one request each: the ids it holds agree with each other and exist there (a TMDB id whose film carries a different IMDb id, a TMDB id TMDB no longer has, an IMDb id that is a series or an episode rather than a film), and the file's runtime is the provider's (a truncated download, a wrong file, a wrong match). " +
		"An episode: the file's runtime against TMDB's for that episode, read once for its whole series by the series' TMDB id; a file holding several episodes (S01E01E02) against their lengths together. TMDB numbers episodes in the order they aired, so a show numbered another way is compared with other episodes than its own: each finding names the TMDB episode it was compared with beside the file's own title. " +
		"The item reads as matched on the server either way. Paged, every film and then every episode once in the same order on every call: pass next_offset back as offset to go on; ids checks a handful without a sweep. What could not be judged, for want of a length on either side, is counted in runtime_not_judged. TMDB is the only provider yet."
	if provider == nil {
		desc += " Disabled: set EMBYFIN_TMDB_TOKEN to enable."
	}
	add(r, readTool, &mcp.Tool{
		Name:        "audit_provider",
		Description: desc,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in providerAuditIn) (*mcp.CallToolResult, providerAuditOut, error) {
		if provider == nil {
			return nil, providerAuditOut{}, errors.New("audit_provider needs a TMDB token: set EMBYFIN_TMDB_TOKEN (or --tmdb-token) and restart")
		}
		out, err := auditAgainstProvider(ctx, client, provider, in)

		return nil, out, err
	})
}

// auditAgainstProvider sweeps the films and then the episodes in order and
// asks the provider about each film that holds an id, and each series an
// episode is in, until the lookup budget runs out.
func auditAgainstProvider(ctx context.Context, client *embyfin.Client, provider *tmdb.Facts, in providerAuditIn) (providerAuditOut, error) {
	switch p := strings.ToLower(strings.TrimSpace(in.Provider)); p {
	case "", "tmdb":
	default:
		return providerAuditOut{}, fmt.Errorf("provider must be tmdb, the only one supported yet, not %q", p)
	}
	kinds, err := providerKinds(in.Types)
	if err != nil {
		return providerAuditOut{}, err
	}
	want := map[string]bool{}
	if strings.TrimSpace(in.Checks) == "" {
		for _, c := range providerChecks {
			want[c] = true
		}
	}
	for c := range strings.SplitSeq(in.Checks, ",") {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		if !slices.Contains(providerChecks, c) {
			return providerAuditOut{}, fmt.Errorf("checks must be among %s, not %q", strings.Join(providerChecks, ", "), c)
		}
		want[c] = true
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	maxLookups := in.MaxLookups
	if maxLookups <= 0 {
		maxLookups = defaultTMDBLookups
	}
	tolerance := in.TolerancePct
	if tolerance <= 0 {
		tolerance = defaultRuntimeTolerancePct
	}
	// episodes are judged by runtime alone: with only ids asked for they
	// have nothing to be judged by, and are not read
	if !want["runtime"] {
		kinds = slices.DeleteFunc(kinds, func(k string) bool { return k == typeEpisode })
		if len(kinds) == 0 {
			return providerAuditOut{}, errors.New("episodes are checked by runtime only: ask for checks runtime, or types Movie")
		}
	}
	opts := embyfin.SearchOptions{IncludeItemTypes: strings.Join(kinds, ","), Fields: "Path,ProviderIds,ProductionYear,SortName"}
	if ids := nonEmpty(in.IDs); len(ids) > 0 {
		if in.Library != "" {
			return providerAuditOut{}, errors.New("give library or ids, not both: a film or an episode is already in one library")
		}
		// read first: an id the server cannot use as a filter is not a
		// narrower sweep, it is the whole library or nothing (see checkIDs)
		if err := checkIDs(ctx, client, ids, kinds, "films or episodes"); err != nil {
			return providerAuditOut{}, err
		}
		opts.IDs = strings.Join(ids, ",")
	} else {
		folder, libErr := client.ResolveLibrary(ctx, in.Library)
		if libErr != nil {
			return providerAuditOut{}, libErr
		}
		if folder != nil {
			opts.ParentID = folder.ItemID
		}
	}

	// every film and then every episode, in an order this makes itself:
	// films by sort name, episodes by series, season and number, then by id.
	// The servers' own sort by name leaves two items of one name (a remake,
	// a second copy) in either order, and paged by offset one of them was
	// asked about twice and the other never
	var all []embyfin.Item
	read, err := client.ReadAll(ctx, opts, embyfin.ToAnswer, func(items []embyfin.Item) bool {
		for i := range items {
			// a featurette Emby took for an episode, and a record of an
			// episode with no file, have no file's runtime to judge
			if items[i].Type == typeEpisode && (extraEpisode(&items[i]) || !items[i].HasFile()) {
				continue
			}
			all = append(all, items[i])
		}

		return true
	})
	if err != nil {
		return providerAuditOut{}, err
	}
	slices.SortFunc(all, func(a, b embyfin.Item) int { return providerOrder(&a, &b) })

	// the TMDB id of each series an episode is in, read with the series
	var seriesTMDB map[string]string
	if slices.Contains(kinds, typeEpisode) {
		if seriesTMDB, err = seriesTMDBIDs(ctx, client, all); err != nil {
			return providerAuditOut{}, err
		}
	}

	out := providerAuditOut{Findings: []providerFinding{}, ByCheck: map[string]int{}, Note: read.Changed()}
	lookups := 0
	asked := map[string]bool{} // series whose run this call has read
	start := min(max(in.Offset, 0), len(all))
	items := all[start:]
	for i := range items {
		it := &items[i]
		if it.Type == typeEpisode {
			tv := seriesTMDB[it.SeriesID]
			if tv != "" && !asked[tv] {
				if lookups >= maxLookups {
					out.NextOffset = start + i

					return out, nil
				}
				lookups++
				asked[tv] = true
			}
			out.Scanned++
			problem, judged, err := checkEpisodeRuntime(ctx, provider, it, tv, tolerance)
			if err != nil {
				return providerAuditOut{}, err
			}
			if !judged {
				out.Unjudged++

				continue
			}
			if problem == "" {
				continue
			}
			out.Found++
			out.ByCheck["runtime"]++
			if len(out.Findings) < limit {
				out.Findings = append(out.Findings, providerFinding{
					ID: it.ID, Name: fmt.Sprintf("%s %s %s", it.SeriesName, episodeCode(it), it.Name), Path: it.Path,
					Holds: "tmdb tv " + tv, Problems: []string{"runtime: " + problem},
				})
			}

			continue
		}

		tmdbID, imdbID := providerID(it, "tmdb"), providerID(it, "imdb")
		if tmdbID != "" || imdbID != "" {
			if lookups >= maxLookups {
				out.NextOffset = start + i

				return out, nil
			}
			lookups++
		}
		out.Scanned++
		if want["runtime"] && (tmdbID == "" || it.RunTimeTicks <= 0) {
			out.Unjudged++
		}
		if tmdbID == "" && imdbID == "" {
			continue
		}

		var problems []string
		if want["ids"] {
			detail, err := checkMovieIDs(ctx, provider, tmdbID, imdbID)
			if err != nil {
				return providerAuditOut{}, err
			}
			if detail != "" {
				problems = append(problems, "ids: "+detail)
			}
		}
		// the same record the ids came from: one request a film
		if want["runtime"] && tmdbID != "" && it.RunTimeTicks > 0 {
			expected, err := provider.MovieRuntime(ctx, tmdbID)
			if err != nil {
				return providerAuditOut{}, err
			}
			if expected <= 0 {
				out.Unjudged++
			} else if pct, off := runtimeOff(it.RuntimeMinutes(), expected, tolerance); off {
				problems = append(problems, fmt.Sprintf("runtime: file %d min, TMDB says %d min (%d%% off)", it.RuntimeMinutes(), expected, pct))
			}
		}
		if len(problems) == 0 {
			continue
		}
		out.Found++
		for _, p := range problems {
			out.ByCheck[strings.SplitN(p, ":", 2)[0]]++
		}
		if len(out.Findings) < limit {
			var holds []string
			if tmdbID != "" {
				holds = append(holds, "tmdb:"+tmdbID)
			}
			if imdbID != "" {
				holds = append(holds, "imdb:"+imdbID)
			}
			out.Findings = append(out.Findings, providerFinding{
				ID: it.ID, Name: it.Name, Year: it.ProductionYear, Path: it.Path,
				Holds: strings.Join(holds, " "), Problems: problems,
			})
		}
	}

	return out, nil
}

// providerKinds reads audit_provider's types: Movie, Episode or both, the
// default.
func providerKinds(types string) ([]string, error) {
	var kinds []string
	for t := range strings.SplitSeq(types, ",") {
		switch k := strings.ToLower(strings.TrimSpace(t)); k {
		case "":
		case "movie", "movies":
			kinds = append(kinds, typeMovie)
		case "episode", "episodes":
			kinds = append(kinds, typeEpisode)
		default:
			return nil, fmt.Errorf("types must be among Movie, Episode, not %q", k)
		}
	}
	if len(kinds) == 0 {
		return []string{typeMovie, typeEpisode}, nil
	}

	return slices.Compact(slices.Sorted(slices.Values(kinds))), nil
}

// providerOrder is the order audit_provider sweeps in: films by sort name,
// then episodes by series, season and number, each settled by id.
func providerOrder(a, b *embyfin.Item) int {
	aEp, bEp := a.Type == typeEpisode, b.Type == typeEpisode
	if aEp != bEp {
		if aEp {
			return 1
		}

		return -1
	}
	if !aEp {
		return cmp.Or(strings.Compare(strings.ToLower(cmp.Or(a.SortName, a.Name)), strings.ToLower(cmp.Or(b.SortName, b.Name))), strings.Compare(a.ID, b.ID))
	}
	number := func(n *int) int {
		if n == nil {
			return -1
		}

		return *n
	}

	return cmp.Or(
		strings.Compare(strings.ToLower(a.SeriesName), strings.ToLower(b.SeriesName)), strings.Compare(a.SeriesID, b.SeriesID),
		cmp.Compare(number(a.ParentIndexNumber), number(b.ParentIndexNumber)), cmp.Compare(number(a.IndexNumber), number(b.IndexNumber)),
		strings.Compare(a.ID, b.ID),
	)
}

// seriesTMDBIDs reads the series the episodes are in, for the TMDB id each
// holds; a series with none is absent.
func seriesTMDBIDs(ctx context.Context, client *embyfin.Client, items []embyfin.Item) (map[string]string, error) {
	var ids []string
	for i := range items {
		if items[i].Type == typeEpisode && items[i].SeriesID != "" {
			ids = append(ids, items[i].SeriesID)
		}
	}
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	out := map[string]string{}
	for chunk := range slices.Chunk(ids, idsPerRequest) {
		if _, err := client.ReadAll(ctx, embyfin.SearchOptions{IDs: strings.Join(chunk, ","), IncludeItemTypes: "Series", Fields: "ProviderIds"}, embyfin.ToAnswer, func(series []embyfin.Item) bool {
			for i := range series {
				if tv := providerID(&series[i], "tmdb"); tv != "" {
					out[series[i].ID] = tv
				}
			}

			return true
		}); err != nil {
			return nil, fmt.Errorf("reading the series the episodes are in, for their TMDB ids: %w", err)
		}
	}

	return out, nil
}

// checkEpisodeRuntime compares an episode file's runtime with TMDB's for the
// episodes it holds, read from its series' run (tv, the series' TMDB id).
// judged is false when either side has no length: no TMDB id, no numbers,
// TMDB holding no such episode or no length for it, or the server none for
// the file. The problem names the TMDB episodes compared with, so a show
// TMDB numbers another way reads as that rather than as a wrong file.
func checkEpisodeRuntime(ctx context.Context, provider *tmdb.Facts, it *embyfin.Item, tv string, tolerancePct int) (problem string, judged bool, err error) {
	if tv == "" || it.RunTimeTicks <= 0 || it.ParentIndexNumber == nil || it.IndexNumber == nil {
		return "", false, nil
	}
	season := *it.ParentIndexNumber
	var run []tmdb.Episode
	if season == 0 {
		run, err = provider.SeriesSpecials(ctx, tv)
	} else {
		run, err = provider.SeriesEpisodes(ctx, tv)
	}
	if err != nil {
		return "", false, err
	}
	expected := 0
	var named []string
	for _, n := range episodeSpan(it) {
		i := slices.IndexFunc(run, func(e tmdb.Episode) bool { return e.Season == season && e.Episode == n })
		if i < 0 || run[i].Runtime <= 0 {
			return "", false, nil
		}
		expected += run[i].Runtime
		named = append(named, fmt.Sprintf("S%02dE%02d %q", season, n, run[i].Name))
	}
	pct, off := runtimeOff(it.RuntimeMinutes(), expected, tolerancePct)
	if !off {
		return "", true, nil
	}

	return fmt.Sprintf("file %d min, TMDB says %d min for %s (%d%% off)", it.RuntimeMinutes(), expected, strings.Join(named, " and "), pct), true, nil
}

// registerProviderCacheTool is provider_cache_clear: forgetting the TMDB
// answers the tools keep, for when TMDB was just put right and an hour is
// too long to wait. It changes nothing on the media server.
func registerProviderCacheTool(r *registry) {
	type clearOut struct {
		Cleared int    `json:"cleared" jsonschema:"answers forgotten, across every tool that keeps them"`
		Note    string `json:"note"    jsonschema:"what was cleared and what the next reads do"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "provider_cache_clear",
		Description: "Forget the answers this embyfin-mcp process keeps from TMDB - films, series and their episodes, ids looked up, alternative and translated titles, collections and title searches - so the next read of each asks TMDB again. Each is kept an hour at most anyway; clear them after putting something right at TMDB (a runtime, an episode added, a wrong id) to see it straight away. " +
			"It changes nothing on the media server, and nothing the media server itself fetched: a server's own metadata comes back from its providers only with item_refresh.",
	}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, clearOut, error) {
		r.cacheMu.Lock()
		caches := slices.Clone(r.providerCaches)
		r.cacheMu.Unlock()
		if len(caches) == 0 {
			return nil, clearOut{Note: "no TMDB token is set (EMBYFIN_TMDB_TOKEN), so no answer from TMDB is kept"}, nil
		}
		out := clearOut{}
		for _, c := range caches {
			out.Cleared += c.Clear()
		}
		out.Note = fmt.Sprintf("forgot %d answers from TMDB; the next read of each asks TMDB again", out.Cleared)

		return nil, out, nil
	})
}
