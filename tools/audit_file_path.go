package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit_file_path: what a file's path claims against what the server holds
// for it.
//
// A path is the one piece of metadata the server did not write: the title,
// the year, the season and the episode numbers in it were put there by
// whoever placed the file, and the server read its first guess from them
// before a provider or a hand edit replaced it. When the two disagree, one of
// them is wrong, and which one is what the row lays side by side: a folder
// saying (2021) under a film matched to 1984, a file named after one episode
// where the server holds another, a file holding two episodes (S01E01E02)
// where the server lists one, a file from another series written into this
// one's folder.

// pathChecks are the checks, in the order a row lists what it found.
var pathChecks = []string{"series", "season", "episode", "title", "year", "lookalike"}

var pathYearRe = regexp.MustCompile(`\((19|20)\d\d\)`)

type pathIn struct {
	Library string   `json:"library,omitempty" jsonschema:"one library by name or id"`
	IDs     []string `json:"ids,omitempty"     jsonschema:"only these items, by id: a handful checked without sweeping a library"`
	Types   string   `json:"types,omitempty"   jsonschema:"Movie, Series, Episode or several, comma-separated; default all three"`
	Checks  string   `json:"checks,omitempty"  jsonschema:"comma-separated: title, year (films and series), series, season, episode (a file holding a run of episodes, S01E01E02, is an episode check), lookalike (a name spelled with a letter of another script that only looks Latin); default every check"`
	Limit   int      `json:"limit,omitempty"   jsonschema:"maximum findings, default 100"`
}

// pathRow is one item whose path disagrees with its metadata.
type pathRow struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Name     string   `json:"name"              jsonschema:"what the server holds: an episode by its series and number"`
	Series   string   `json:"series,omitempty"`
	Season   int      `json:"season,omitempty"`
	Episode  int      `json:"episode,omitempty"`
	Path     string   `json:"path,omitempty"`
	Problems []string `json:"problems"          jsonschema:"each begins with the check that failed: series, season, episode, title, year or lookalike"`
	// the two titles when they differ, so a caller reads them without
	// parsing the problem text
	TitleInFile   string  `json:"title_in_file,omitempty"   jsonschema:"the title the file name claims"`
	TitleOnServer string  `json:"title_on_server,omitempty" jsonschema:"the title the server holds"`
	Score         float64 `json:"similarity,omitempty"      jsonschema:"0 to 1: how close the two titles are once case, punctuation and accents are folded"`
	// where the file's title sits in the provider's own list, which tells a
	// file numbered another way (a download numbered from TVDB in a library
	// matched to TMDB) from a file that is simply wrong
	TMDBEpisode string `json:"tmdb_episode,omitempty" jsonschema:"episodes: the episode TMDB gives the file's title to, as SxxEyy, when the series has a TMDB id and EMBYFIN_TMDB_TOKEN is set; a different number means the file is numbered in another order, not mislabelled"`
	// films and series: the title the path matched when it is not the
	// item's name, and what TMDB's search makes of a path that matches none
	TitleMatched string `json:"title_matched,omitempty" jsonschema:"films and series: which of the item's other titles the path's title is, when it is not its name: its original title, its sort name, one TMDB lists for it, or one TMDB's search finds it by"`
	PathTMDB     string `json:"path_tmdb,omitempty"     jsonschema:"films and series whose path names none of the item's titles, with EMBYFIN_TMDB_TOKEN set: the film or series TMDB's search finds by the path's title and year, as its TMDB id, title and year"`
	ItemTMDB     string `json:"item_tmdb,omitempty"     jsonschema:"the TMDB id the item carries, beside path_tmdb"`
	Diagnosis    string `json:"diagnosis,omitempty"     jsonschema:"what the tmdb lookup made of it"`

	seriesID string
	item     embyfin.Item // what the row was made from: the TMDB lookups read its ids and runtime
	rank     int          // 0 a number or the series disagrees, 1 the title, 2 the year alone
	// named is set once TMDB has said the path names the item's own title
	// and its name is what is wrong, which the search by the path's title
	// then has nothing to add to
	named bool
}

type pathOut struct {
	Scanned  int            `json:"items_scanned"`
	Found    int            `json:"total_findings"`
	ByCheck  map[string]int `json:"by_check"       jsonschema:"findings by the check that failed; a row failing two counts under both"`
	Unnamed  int            `json:"unnamed"        jsonschema:"episode files whose name claims no title, so the title check had nothing to compare; not findings"`
	Findings []pathRow      `json:"findings"       jsonschema:"a number or the series disagreeing first, then titles least alike first, then years; capped at limit"`
	Note     string         `json:"note,omitempty" jsonschema:"set when TMDB could not be asked, and what that leaves unsaid"`
}

func registerFilePathAudit(r *registry) {
	client := r.client
	provider := tmdbFacts(r.opts)
	titles := newProviderTitles(r.opts)

	add(r, readTool, &mcp.Tool{
		Name: "audit_file_path",
		Description: "Find items whose file path disagrees with the metadata the server holds for them: the title, the (year), the series, the season and the episode number or run of numbers in the path against the item, every version of a film held in several files included. " +
			"A film's or a series' path is compared with every title the item goes by - its name, its original title, its sort name and, with EMBYFIN_TMDB_TOKEN set, every alternative title TMDB lists for its id - and a year one either side of the item's is the same film, so a folder in the film's own language, or dated the year before, is no finding. " +
			"A path whose title is none of them, or whose year is two or more off, is the wrong match or another film: with EMBYFIN_TMDB_TOKEN set, the row says what TMDB's search finds by the path's title and year (path_tmdb, beside the item's own id), whether the file's runtime backs either, and when the server never probed the file so its runtime backs neither; the search finding the item itself means the path's title is one it goes by, and a year still apart is the item's to check - unless the item's own name is none TMDB gives that title, when the path is right and the name is what is wrong. " +
			"A file named after one episode where the server holds another is a file from another series written to this path, or one numbered in another provider's order (with EMBYFIN_TMDB_TOKEN set, each such row says which episode TMDB gives the file's title to); " +
			"a file holding two episodes (S01E01E02) where the server lists one has the second's content on disk while the server calls it missing, and the reverse means the metadata claims a run the file does not. " +
			"A name spelled with a letter of another script that only looks Latin (a Cyrillic A, U+0410, spelling a Latin title) is a lookalike row naming the letter and the plain spelling, because no search for the plain title finds it. " +
			"The path is what was placed on disk and the metadata what a provider or an edit set, so a row says which to fix by what it holds. item_identify or item_edit set the metadata right; a rename fixes the path. ids checks a handful of items without a sweep.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pathIn) (*mcp.CallToolResult, pathOut, error) {
		out, err := auditFilePath(ctx, client, provider, titles, in)

		return nil, out, err
	})
}

// pathChecksWanted reads the checks input: every check when empty.
func pathChecksWanted(s string) (map[string]bool, error) {
	want := map[string]bool{}
	if strings.TrimSpace(s) == "" {
		for _, c := range pathChecks {
			want[c] = true
		}

		return want, nil
	}
	for c := range strings.SplitSeq(s, ",") {
		c = strings.ToLower(strings.TrimSpace(c))
		if !slices.Contains(pathChecks, c) {
			return nil, fmt.Errorf("checks must be among %s, not %q", strings.Join(pathChecks, ", "), c)
		}
		want[c] = true
	}

	return want, nil
}

func auditFilePath(ctx context.Context, client *embyfin.Client, provider *tmdb.Facts, titles *providerTitles, in pathIn) (pathOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	want, err := pathChecksWanted(in.Checks)
	if err != nil {
		return pathOut{}, err
	}
	// the ids and runtime are what TMDB is asked by and what backs an
	// answer; the version count is how Jellyfin says an item holds more
	// files than its own path (Emby answers each file as an item of its own)
	opts, err := sweepOptions(ctx, client, in.Library, in.Types, "Movie,Series,Episode", "Path,ProductionYear,OriginalTitle,SortName,ProviderIds,MediaSourceCount")
	if err != nil {
		return pathOut{}, err
	}
	if ids := nonEmpty(in.IDs); len(ids) > 0 {
		if in.Library != "" {
			return pathOut{}, errors.New("give library or ids, not both: an item is already in one library")
		}
		opts.IDs = strings.Join(ids, ",")
	}

	out := pathOut{Findings: []pathRow{}, ByCheck: map[string]int{}}
	var findings []pathRow
	var versioned []string
	check := func(it *embyfin.Item) {
		row, unnamed := checkPath(it, want)
		if unnamed {
			out.Unnamed++
		}
		if len(row.Problems) > 0 {
			findings = append(findings, row)
		}
	}
	if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			out.Scanned++
			if it.Path == "" {
				continue
			}
			check(it)
			if it.MediaSourceCount > 1 {
				versioned = append(versioned, it.ID)
			}
		}

		return true
	}); err != nil {
		return pathOut{}, err
	}

	// every other version's file, read in batches: a film shown in two files
	// can have the wrong one in either
	for chunk := range slices.Chunk(versioned, idsPerRequest) {
		if err := client.SearchAll(ctx, embyfin.SearchOptions{IDs: strings.Join(chunk, ","), Fields: opts.Fields + ",MediaSources"}, func(items []embyfin.Item) bool {
			for i := range items {
				for _, src := range items[i].MediaSources {
					if src.Path == "" || src.Path == items[i].Path {
						continue
					}
					version := items[i]
					version.Path, version.RunTimeTicks, version.MediaSources = src.Path, src.RunTimeTicks, []embyfin.MediaSource{src}
					check(&version)
				}
			}

			return true
		}); err != nil {
			return pathOut{}, err
		}
	}

	// a title TMDB lists for the item is one it goes by. TMDB failing to
	// answer leaves the rows as the server alone makes them, and says so,
	// rather than failing every other check with it
	unasked := func(err error) {
		out.Note = "TMDB could not be asked (" + err.Error() + "), so a path named by a title TMDB lists for the item, or for another film, is not told apart from a wrong one here"
		titles = nil
	}
	if titles != nil {
		kept := findings[:0]
		for i := range findings {
			if titles != nil {
				if err := clearAlternativeTitle(ctx, titles, &findings[i]); err != nil {
					unasked(err)
				}
			}
			if len(findings[i].Problems) > 0 {
				kept = append(kept, findings[i])
			}
		}
		findings = kept
	}

	// the plainest disagreements first: a number the file and the server
	// read differently, then the titles least alike, then years
	slices.SortFunc(findings, func(a, b pathRow) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		if a.Score != b.Score {
			if a.Score < b.Score {
				return -1
			}

			return 1
		}

		return strings.Compare(a.Path, b.Path)
	})
	if titles != nil {
		if diagnosed, err := diagnoseTitlesByTMDB(ctx, provider, titles, findings, limit); err != nil {
			unasked(err)
		} else {
			findings = diagnosed
		}
	}
	for i := range findings {
		for _, p := range findings[i].Problems {
			out.ByCheck[strings.SplitN(p, ":", 2)[0]]++
		}
	}
	out.Found = len(findings)
	out.Findings = append(out.Findings, findings[:min(len(findings), limit)]...)
	if provider != nil {
		diagnoseByTMDB(ctx, client, provider, out.Findings)
	}

	return out, nil
}

// checkPath runs the wanted checks over one item. unnamed reports an episode
// file whose name claims no title, which the title check cannot judge.
func checkPath(it *embyfin.Item, want map[string]bool) (row pathRow, unnamed bool) {
	row = pathRow{
		ID: it.ID, Type: it.Type, Name: episodeOrItemName(it), Path: it.Path,
		Series: it.SeriesName, Season: it.ParentIndexNumber, Episode: it.IndexNumber, seriesID: it.SeriesID, item: *it, rank: 2,
	}
	problem := func(check, detail string) {
		row.Problems = append(row.Problems, check+": "+detail)
	}

	// a letter of another script drawn like a Latin one: the name reads
	// right and no search finds it. The title checks below compare the
	// plain spelling, so the one fault is reported once
	letters, plain := lookalikeLetters(it.Name)
	if want["lookalike"] && len(letters) > 0 {
		problem("lookalike", fmt.Sprintf("%q is spelled with %s, so it only looks like %q and a search for %q does not find it: item_edit name %q puts it right", it.Name, strings.Join(letters, " and "), plain, plain, plain))
		row.rank = min(row.rank, 1)
	}

	if it.Type == typeEpisode {
		file := parseSegment(baseName(it.Path))
		// the series and the numbers are only what the file itself says: a
		// bare "S01E01.mkv" under a series folder claims nothing about the
		// series, and the server took the folder's word too. Nor does a name
		// with no episode marker to end a title at: "01 - Pilot.mkv",
		// "Episode 1.mkv" or a fansub's "[Grp] Show - 01 [1080p].mkv" is all
		// title to the parser, and read as a series name every one of them
		// was a file from another show.
		if want["series"] && file.Title != "" && file.Episode > 0 && it.SeriesName != "" {
			if score, _ := titleScore(file.Title, it.SeriesName); score < seriesConfident {
				problem("series", fmt.Sprintf("the file is named for %q, the server holds it under %q: a file from another series written to this path", file.Title, it.SeriesName))
				row.rank = 0
			}
		}
		if file.Episode > 0 {
			if want["season"] && file.Season != it.ParentIndexNumber {
				problem("season", fmt.Sprintf("the file says season %d, the server holds season %d", file.Season, it.ParentIndexNumber))
				row.rank = 0
			}
			if want["episode"] {
				if detail := episodeRunProblem(file, it); detail != "" {
					problem("episode", detail)
					row.rank = 0
				}
			}
		}
		if want["title"] && it.Name != "" {
			claimed := episodeTitleFromFile(it.Path)
			if claimed == "" {
				unnamed = true
			} else if score, _ := titleScore(claimed, plain); score < seriesConfident {
				problem("title", fmt.Sprintf("the file is named %q, the server holds %q", claimed, it.Name))
				row.TitleInFile, row.TitleOnServer, row.Score = claimed, it.Name, score
				row.rank = min(row.rank, 1)
			}
		}

		return row, unnamed
	}

	// a film or a series: the path's own segment names it, with its year
	if want["title"] && it.Name != "" {
		// every title the item goes by, and the path's against the nearest:
		// a folder in the film's own language names its original title,
		// and one the server holds by its English one is the same film
		claimed, score, as := "", -1.0, ""
		for _, k := range knownTitles(it) {
			if c, s := titleFromPath(it.Path, k.title); s > score {
				claimed, score, as = c, s, k.as
			}
		}
		switch {
		case claimed != "" && score < seriesConfident:
			problem("title", fmt.Sprintf("the path is named %q, the server holds %q, and the path's title is none of the titles it goes by: the wrong match, or another film", claimed, it.Name))
			row.TitleInFile, row.TitleOnServer, row.Score = claimed, it.Name, score
			row.rank = min(row.rank, 1)
		case claimed != "" && as != titleAsName:
			row.TitleMatched = as
		}
	}
	if want["year"] {
		if detail, off := checkYearMismatch(it); off {
			problem("year", detail)
		}
	}

	return row, false
}

// episodeRunProblem compares the episode number, or the run of them, a file
// claims with the one the server holds. A file holding two episodes that
// the server lists as one has the second's content on disk while the server
// calls it missing; a server holding a run the file does not claim has
// metadata that says more than the file.
func episodeRunProblem(file release, it *embyfin.Item) string {
	fileEnd, serverEnd := max(file.EpisodeEnd, file.Episode), max(it.IndexNumberEnd, it.IndexNumber)
	if file.Episode == it.IndexNumber && fileEnd == serverEnd {
		return ""
	}
	name := func(first, last int) string {
		if last > first {
			return fmt.Sprintf("E%02d-E%02d", first, last)
		}

		return fmt.Sprintf("E%02d", first)
	}
	switch {
	case file.Episode == it.IndexNumber && fileEnd > serverEnd:
		return fmt.Sprintf("the file holds %s, the server holds %s alone: the rest of the run is on disk while the server lists it missing", name(file.Episode, fileEnd), name(it.IndexNumber, serverEnd))
	case file.Episode == it.IndexNumber:
		return fmt.Sprintf("the server holds %s, the file claims %s: the metadata says a run the file name does not", name(it.IndexNumber, serverEnd), name(file.Episode, fileEnd))
	default:
		return fmt.Sprintf("the file says %s, the server holds %s", name(file.Episode, fileEnd), name(it.IndexNumber, serverEnd))
	}
}

// titleFromPath reads the title a film's or a series' path claims and how
// close it is to the name held: the last segment, cut at its (year) when it
// has one, else at the last bare year, else at the encode's words. A title
// that is itself a year, or ends in one (2012, Blade Runner 2049), is tried
// uncut too, and the closer reading wins.
func titleFromPath(path, name string) (claimed string, score float64) {
	// the path is the server's, so split on either separator: one on Windows
	// answers with backslashes, whatever this runs on
	base := fileExtension.ReplaceAllString(baseName(path), "")
	// a disc's own files name nothing, nor do the folders a disc keeps them
	// in: the folder above those is the one named for the film. Read as a
	// title, VTS_01_1.VOB was "VTS 01 1", and every loose DVD a mismatch.
	for (discFile.MatchString(base) || isDiscFolder(base)) && parentDir(path) != "" {
		path = parentDir(path)
		base = baseName(path)
	}
	best, bestScore := "", -1.0
	try := func(candidate string) {
		candidate = strings.Trim(strings.TrimSpace(spaceRun.ReplaceAllString(strings.NewReplacer(".", " ", "_", " ").Replace(candidate), " ")), " -_([{")
		if candidate == "" {
			return
		}
		if score, _ := titleScore(candidate, name); score > bestScore {
			best, bestScore = candidate, score
		}
	}
	if m := bracketedYear.FindStringIndex(base); m != nil {
		try(base[:m[0]])
	} else {
		years := bareYear.FindAllStringSubmatchIndex(base, -1)
		if len(years) > 0 {
			last := years[len(years)-1]
			if last[4] > 0 {
				try(base[:last[4]])
			}
		}
		try(parseSegment(base).Title)
	}
	if best == "" {
		try(base)
	}

	return best, bestScore
}

// discFile is a disc's own stream or title-set file, which names nothing:
// VTS_01_1.VOB, VIDEO_TS.IFO, 00000.m2ts.
var discFile = regexp.MustCompile(`(?i)^(vts_\d+_\d+|video_ts|\d{5})(\.(ifo|bup))?$`)

// isDiscFolder is a folder a disc keeps its files in (see discStructures),
// or a Blu-ray's STREAM folder inside BDMV.
func isDiscFolder(name string) bool {
	return strings.EqualFold(name, "STREAM") || slices.ContainsFunc(discStructures, func(s string) bool { return strings.EqualFold(name, s) })
}

// checkYearMismatch compares the (year) in an item's path with its metadata
// year: two or more apart is a wrong edition or a wrong match. The last
// year in the path is the item's own: a collection folder above it can carry
// the first film's.
func checkYearMismatch(it *embyfin.Item) (string, bool) {
	if it.Path == "" || it.ProductionYear == 0 {
		return "", false
	}

	years := pathYearRe.FindAllString(it.Path, -1)
	if len(years) == 0 {
		return "", false
	}

	pathYear, _ := strconv.Atoi(strings.Trim(years[len(years)-1], "()"))
	diff := pathYear - it.ProductionYear
	if diff < 0 {
		diff = -diff
	}
	if diff >= 2 {
		return "path says " + strconv.Itoa(pathYear) + ", metadata says " + strconv.Itoa(it.ProductionYear), true
	}

	return "", false
}

// episodeTitleFromFile reads the title a file name claims, which is whatever
// follows the season and episode marker once the extension and the encode's
// words are off: "Show - 01x01 - The DVD.mkv" claims "The DVD". It returns ""
// for a name that claims nothing, which is most of a tidy library.
func episodeTitleFromFile(path string) string {
	name := fileExtension.ReplaceAllString(baseName(path), "")

	var after string
	for _, re := range releaseMarkers {
		if m := re.FindStringIndex(name); m != nil {
			after = name[m[1]:]

			break
		}
	}
	if after == "" {
		return ""
	}

	// the same cleanup a release name gets: dots and underscores are spaces,
	// and the encode's words end a title
	after = strings.TrimLeft(after, " -_.")
	after = strings.NewReplacer(".", " ", "_", " ").Replace(after)
	words := strings.Fields(after)
	for i, w := range words {
		if releaseJunk[strings.ToLower(strings.Trim(w, "()[]-"))] {
			words = words[:i]

			break
		}
	}

	return strings.Trim(strings.Join(words, " "), " -_([{")
}

// diagnoseByTMDB looks each reported episode's file title up in TMDB's list
// of the series' episodes, one read per series, and says on the row where
// TMDB puts that title: the same number as the file is a title the server
// has reworded, another number is a file numbered in a different order, and
// no number is a title TMDB has never heard of.
func diagnoseByTMDB(ctx context.Context, client *embyfin.Client, provider *tmdb.Facts, rows []pathRow) {
	guides := map[string][]tmdb.Episode{} // series item id -> TMDB's episodes, nil when it cannot be read
	for i := range rows {
		row := &rows[i]
		if row.Type != typeEpisode || row.TitleInFile == "" {
			continue
		}
		eps, ok := guides[row.seriesID]
		if !ok {
			eps = tmdbGuide(ctx, client, provider, row.seriesID)
			guides[row.seriesID] = eps
		}
		if eps == nil {
			continue
		}
		best, bestScore := tmdb.Episode{}, 0.0
		for _, e := range eps {
			if score, _ := titleScore(row.TitleInFile, e.Name); score > bestScore {
				best, bestScore = e, score
			}
		}
		switch {
		case bestScore < seriesConfident:
			row.Diagnosis = "no TMDB episode of this series has the file's title: the file is from another series, or named by hand"
		case best.Season == row.Season && best.Episode == row.Episode:
			row.TMDBEpisode = fmt.Sprintf("S%02dE%02d", best.Season, best.Episode)
			row.Diagnosis = "TMDB gives the file's title to this very episode: the server's title is a reworded one, not a different episode"
		default:
			row.TMDBEpisode = fmt.Sprintf("S%02dE%02d", best.Season, best.Episode)
			row.Diagnosis = fmt.Sprintf("the file's title is TMDB's %s, not S%02dE%02d: the file is numbered in another order (a download numbered from TVDB, most often), and its content is that episode", row.TMDBEpisode, row.Season, row.Episode)
		}
	}
}

// rankOf is where a row sorts by the checks it failed: a number or the
// series first, a title next, a year alone last.
func rankOf(problems []string) int {
	rank := 2
	for _, p := range problems {
		switch strings.SplitN(p, ":", 2)[0] {
		case "series", "season", "episode":
			return 0
		case "title", "lookalike":
			rank = 1
		}
	}

	return rank
}

// dropTitle takes a row's title problem away: the path's title is one the
// item goes by after all, and matched says which.
func dropTitle(row *pathRow, matched string) {
	row.Problems = slices.DeleteFunc(row.Problems, func(p string) bool { return strings.HasPrefix(p, "title:") })
	row.TitleMatched, row.TitleInFile, row.TitleOnServer, row.Score = matched, "", "", 0
	row.rank = rankOf(row.Problems)
}

// namesAnotherTitle says whether a row is a film's or a series' path naming a
// title the item does not go by.
func namesAnotherTitle(row *pathRow) bool {
	return row.TitleInFile != "" && tmdbKind(row.Type) != "" && slices.ContainsFunc(row.Problems, func(p string) bool { return strings.HasPrefix(p, "title:") })
}

// pathDisputed says whether a row is a film's or a series' path naming
// another title or another year than the item's - the rows TMDB's search can
// say more about - and what the path claims, to search by.
func pathDisputed(row *pathRow) (pathClaim, bool) {
	if tmdbKind(row.Type) == "" || !slices.ContainsFunc(row.Problems, func(p string) bool {
		return strings.HasPrefix(p, "title:") || strings.HasPrefix(p, "year:")
	}) {
		return pathClaim{}, false
	}
	claim, ok := claimOf(row.Path, heldTitles(&row.item))
	if !ok || claim.title == "" {
		return pathClaim{}, false
	}
	if row.TitleInFile != "" {
		claim.title = row.TitleInFile
	}

	return claim, true
}

// clearAlternativeTitle drops a film's or a series' title problem when the
// path's title is one TMDB lists for the item's id: the name it went by in
// another country, or in another language.
func clearAlternativeTitle(ctx context.Context, titles *providerTitles, row *pathRow) error {
	if !namesAnotherTitle(row) {
		return nil
	}
	id := providerID(&row.item, "tmdb")
	if id == "" {
		return nil
	}
	alts, err := titles.alternatives(ctx, tmdbKind(row.Type), id)
	if err != nil {
		return err
	}
	for _, alt := range alts {
		if score, _ := titleScore(row.TitleInFile, alt); score < seriesConfident {
			continue
		}
		// the path is right; the name is too, unless it is none TMDB gives
		// the title (a film renamed by hand to another film's)
		known, err := nameKnown(ctx, titles, row, id, nil)
		if err != nil {
			return err
		}
		if !known {
			nameWrong(row, id, "the path's title is one TMDB lists for the item's TMDB "+id)

			return nil
		}
		dropTitle(row, fmt.Sprintf("a title TMDB lists for it: %q", alt))

		return nil
	}

	return nil
}

// nameKnown says whether a film's or a series' name - or its original title
// or sort name - is one TMDB gives the title of its TMDB id: one TMDB lists
// for it, the title or original title of a search hit for that id when there
// is one to hand, or found as that id by TMDB's search for the name and year.
func nameKnown(ctx context.Context, titles *providerTitles, row *pathRow, id string, hit *titleHit) (bool, error) {
	kind := tmdbKind(row.Type)
	known, err := titles.alternatives(ctx, kind, id)
	if err != nil {
		return false, err
	}
	if hit != nil {
		known = append(slices.Clone(known), hit.Title, hit.Original)
	}
	held := heldTitles(&row.item)
	for _, h := range held {
		for _, k := range known {
			if score, _ := titleScore(h, k); k != "" && score >= seriesConfident {
				return true, nil
			}
		}
	}
	hits, err := titles.search(ctx, kind, row.item.Name, row.item.ProductionYear)
	if err != nil {
		return false, err
	}

	return slices.ContainsFunc(hits, func(h titleHit) bool { return strconv.Itoa(h.ID) == id }), nil
}

// nameWrong keeps a row whose path names the item's own title by TMDB's
// reckoning while its name is none TMDB gives it: the name is what to put
// right, and the row says so, in its title problem as well - which
// otherwise reads as the wrong match, or another film.
func nameWrong(row *pathRow, id, why string) {
	row.named, row.ItemTMDB = true, id
	for i, p := range row.Problems {
		if strings.HasPrefix(p, "title:") {
			row.Problems[i] = fmt.Sprintf("title: the path is named %q, the server holds %q: the path names the item's own title, and the name is none TMDB gives it", row.TitleInFile, row.item.Name)
		}
	}
	row.Diagnosis = fmt.Sprintf("%s, and the name the server holds, %q, is none TMDB gives it: the path is right and the name is what is wrong - put it right with item_edit", why, row.item.Name)
}

// diagnoseTitlesByTMDB asks TMDB's search what a film's or a series' path
// names, for the rows whose title is none the item goes by or whose year is
// two or more from its own, in the order they are reported until limit rows
// are kept. The search finding the item's own id means the path names it by
// a title and a year TMDB knows it by, and the row goes; finding another says
// which film the path names, beside the id the item carries, with the file's
// runtime against both; finding nothing says so.
func diagnoseTitlesByTMDB(ctx context.Context, facts *tmdb.Facts, titles *providerTitles, rows []pathRow, limit int) ([]pathRow, error) {
	kept := make([]pathRow, 0, len(rows))
	for i := range rows {
		row := rows[i]
		claim, disputed := pathDisputed(&row)
		if len(kept) >= limit || !disputed || row.named {
			kept = append(kept, row)

			continue
		}
		kind := tmdbKind(row.Type)
		hits, err := titles.search(ctx, kind, claim.title, claim.year)
		if err != nil {
			return nil, err
		}
		own := providerID(&row.item, "tmdb")
		hit, found := bestHit(hits, claim.title, claim.year)
		what := "film"
		if kind == "tv" {
			what = "series"
		}
		// the search finding the item's own id by the path's title and year,
		// however it spells them, is TMDB saying the path names this film:
		// the title is one it goes by, and a year that still disagrees is
		// the item's to check, not the path's
		if ownAt := slices.IndexFunc(hits, func(h titleHit) bool {
			return own != "" && strconv.Itoa(h.ID) == own && (claim.year == 0 || h.Year == 0 || abs(h.Year-claim.year) <= 1)
		}); ownAt >= 0 {
			// unless the name the item holds is none TMDB gives that film:
			// then the path is right and the name is what is wrong (a film
			// renamed by hand to another film's title read as clean)
			if namesAnotherTitle(&row) {
				known, err := nameKnown(ctx, titles, &row, own, &hits[ownAt])
				if err != nil {
					return nil, err
				}
				if !known {
					nameWrong(&row, own, fmt.Sprintf("TMDB's search finds this very %s, TMDB %s %s (%d), by the path's title and year", what, own, hits[ownAt].Title, hits[ownAt].Year))
					kept = append(kept, row)

					continue
				}
			}
			dropTitle(&row, fmt.Sprintf("a title TMDB's search finds it by: %q (%d) is its TMDB %s", claim.title, claim.year, own))
			if len(row.Problems) == 0 {
				continue
			}
			if slices.ContainsFunc(row.Problems, func(p string) bool { return strings.HasPrefix(p, "year:") }) {
				row.ItemTMDB = own
				row.Diagnosis = fmt.Sprintf("TMDB's search finds this very %s, TMDB %s, by the path's title and year: the path names it, and the year the item holds is the one to check", what, own)
			}
			kept = append(kept, row)

			continue
		}
		switch {
		case found:
			row.PathTMDB, row.ItemTMDB = fmt.Sprintf("%d %s (%d)", hit.ID, hit.Title, hit.Year), own
			carries := "the item carries no TMDB id"
			if own != "" {
				carries = "the item carries TMDB " + own
			}
			row.Diagnosis = fmt.Sprintf("the path names TMDB's %s %d, %s (%d); %s: it is matched to another %s than the one on disk", what, hit.ID, hit.Title, hit.Year, carries, what)
			if kind == "movie" {
				row.Diagnosis += runtimeEvidence(ctx, facts, &row.item, strconv.Itoa(hit.ID), own)
			}
		default:
			row.Diagnosis = fmt.Sprintf("TMDB's search finds no %s by the path's title and year: named by hand, or for a %s TMDB does not list", what, what)
			if kind == "movie" && row.item.RunTimeTicks <= 0 {
				row.Diagnosis += "; the server holds no media facts for the file (never probed), so its runtime cannot say which film it is"
			}
		}
		kept = append(kept, row)
	}

	return kept, nil
}

// runtimeEvidence is what the file's runtime says about which of two films it
// is, as a clause to end a diagnosis with: the runtime TMDB gives each, and
// which the file runs closer to, or that the server never probed the file.
func runtimeEvidence(ctx context.Context, facts *tmdb.Facts, it *embyfin.Item, pathID, itemID string) string {
	if it.RunTimeTicks <= 0 {
		return "; the server holds no media facts for the file (never probed), so its runtime backs neither film"
	}
	if facts == nil {
		return ""
	}
	file := it.RuntimeMinutes()
	path, err := facts.MovieRuntime(ctx, pathID)
	if err != nil || path <= 0 {
		return ""
	}
	out := fmt.Sprintf("; the file runs %d min, and TMDB's %s runs %d", file, pathID, path)
	if itemID == "" {
		return out
	}
	item, err := facts.MovieRuntime(ctx, itemID)
	if err != nil || item <= 0 {
		return out
	}
	out += fmt.Sprintf(" and its %s %d", itemID, item)
	// a runtime backs a film when it is that film's, as audit_provider
	// judges one; a truncated file is neither's, and says nothing
	_, offPath := runtimeOff(file, path, runtimeBacksPct)
	_, offItem := runtimeOff(file, item, runtimeBacksPct)
	switch {
	case !offPath && offItem:
		out += ": the file's runtime is the path's film's"
	case offPath && !offItem:
		out += ": the file's runtime is the item's film's"
	case offPath && offItem:
		out += ": the file's runtime is neither's"
	}

	return out
}

// runtimeBacksPct is how close, in percent, a file's runtime has to be to a
// film's for it to back that film: closer than a cut of the same film is
// usually apart.
const runtimeBacksPct = 10

// tmdbGuide is TMDB's episodes for a library series, or nil when the series
// has no TMDB id or TMDB cannot be asked.
func tmdbGuide(ctx context.Context, client *embyfin.Client, provider *tmdb.Facts, seriesID string) []tmdb.Episode {
	if seriesID == "" {
		return nil
	}
	series, err := client.ItemByID(ctx, seriesID)
	if err != nil {
		return nil
	}
	id := providerID(series, "tmdb")
	if id == "" {
		return nil
	}
	eps, err := provider.SeriesEpisodes(ctx, id)
	if err != nil {
		return nil
	}

	return eps
}
