package tools

import (
	"context"
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
var pathChecks = []string{"series", "season", "episode", "title", "year"}

var pathYearRe = regexp.MustCompile(`\((19|20)\d\d\)`)

type pathIn struct {
	Library string `json:"library,omitempty" jsonschema:"one library by name or id"`
	Types   string `json:"types,omitempty"   jsonschema:"Movie, Series, Episode or several, comma-separated; default all three"`
	Checks  string `json:"checks,omitempty"  jsonschema:"comma-separated: title, year (films and series), series, season, episode (a file holding a run of episodes, S01E01E02, is an episode check); default every check"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum findings, default 100"`
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
	Problems []string `json:"problems"          jsonschema:"each begins with the check that failed: series, season, episode, title or year"`
	// the two titles when they differ, so a caller reads them without
	// parsing the problem text
	TitleInFile   string  `json:"title_in_file,omitempty"   jsonschema:"the title the file name claims"`
	TitleOnServer string  `json:"title_on_server,omitempty" jsonschema:"the title the server holds"`
	Score         float64 `json:"similarity,omitempty"      jsonschema:"0 to 1: how close the two titles are once case, punctuation and accents are folded"`
	// where the file's title sits in the provider's own list, which tells a
	// file numbered another way (a download numbered from TVDB in a library
	// matched to TMDB) from a file that is simply wrong
	TMDBEpisode string `json:"tmdb_episode,omitempty" jsonschema:"episodes: the episode TMDB gives the file's title to, as SxxEyy, when the series has a TMDB id and EMBYFIN_TMDB_TOKEN is set; a different number means the file is numbered in another order, not mislabelled"`
	Diagnosis   string `json:"diagnosis,omitempty"    jsonschema:"what the tmdb lookup made of it"`

	seriesID string
	rank     int // 0 a number or the series disagrees, 1 the title, 2 the year alone
}

type pathOut struct {
	Scanned  int            `json:"items_scanned"`
	Found    int            `json:"total_findings"`
	ByCheck  map[string]int `json:"by_check"       jsonschema:"findings by the check that failed; a row failing two counts under both"`
	Unnamed  int            `json:"unnamed"        jsonschema:"episode files whose name claims no title, so the title check had nothing to compare; not findings"`
	Findings []pathRow      `json:"findings"       jsonschema:"a number or the series disagreeing first, then titles least alike first, then years; capped at limit"`
}

func registerFilePathAudit(r *registry) {
	client := r.client
	provider := tmdbFacts(r.opts)

	add(r, readTool, &mcp.Tool{
		Name: "audit_file_path",
		Description: "Find items whose file path disagrees with the metadata the server holds for them: the title, the (year), the series, the season and the episode number or run of numbers in the path against the item. " +
			"A folder saying (2021) under a film matched to 1984 is the wrong edition or the wrong match; a file named after one episode where the server holds another is a file from another series written to this path, or one numbered in another provider's order (with EMBYFIN_TMDB_TOKEN set, each such row says which episode TMDB gives the file's title to); " +
			"a file holding two episodes (S01E01E02) where the server lists one has the second's content on disk while the server calls it missing, and the reverse means the metadata claims a run the file does not. " +
			"The path is what was placed on disk and the metadata what a provider or an edit set, so a row says which to fix by what it holds. item_identify or item_edit set the metadata right; a rename fixes the path.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pathIn) (*mcp.CallToolResult, pathOut, error) {
		out, err := auditFilePath(ctx, client, provider, in)

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

func auditFilePath(ctx context.Context, client *embyfin.Client, provider *tmdb.Facts, in pathIn) (pathOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	want, err := pathChecksWanted(in.Checks)
	if err != nil {
		return pathOut{}, err
	}
	opts, err := sweepOptions(ctx, client, in.Library, in.Types, "Movie,Series,Episode", "Path,ProductionYear")
	if err != nil {
		return pathOut{}, err
	}

	out := pathOut{Findings: []pathRow{}, ByCheck: map[string]int{}}
	var findings []pathRow
	if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			out.Scanned++
			if it.Path == "" {
				continue
			}
			row, unnamed := checkPath(it, want)
			if unnamed {
				out.Unnamed++
			}
			if len(row.Problems) == 0 {
				continue
			}
			for _, p := range row.Problems {
				out.ByCheck[strings.SplitN(p, ":", 2)[0]]++
			}
			findings = append(findings, row)
		}

		return true
	}); err != nil {
		return pathOut{}, err
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
		Series: it.SeriesName, Season: it.ParentIndexNumber, Episode: it.IndexNumber, seriesID: it.SeriesID, rank: 2,
	}
	problem := func(check, detail string) {
		row.Problems = append(row.Problems, check+": "+detail)
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
			} else if score, _ := titleScore(claimed, it.Name); score < seriesConfident {
				problem("title", fmt.Sprintf("the file is named %q, the server holds %q", claimed, it.Name))
				row.TitleInFile, row.TitleOnServer, row.Score = claimed, it.Name, score
				row.rank = min(row.rank, 1)
			}
		}

		return row, unnamed
	}

	// a film or a series: the path's own segment names it, with its year
	if want["title"] && it.Name != "" {
		// a server names a film it could not match after its folder, year
		// and all ("Cube (1997)"), and the path's title is read cut at its
		// year, so the year comes off the held name too: left on, a short
		// title scored under the bar against its own folder
		held := strings.TrimSpace(seriesNameYear.ReplaceAllString(it.Name, ""))
		if held == "" {
			held = it.Name
		}
		claimed, score := titleFromPath(it.Path, held)
		if claimed != "" && score < seriesConfident {
			problem("title", fmt.Sprintf("the path is named %q, the server holds %q: named by hand or in another language, or the wrong match", claimed, it.Name))
			row.TitleInFile, row.TitleOnServer, row.Score = claimed, it.Name, score
			row.rank = min(row.rank, 1)
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
