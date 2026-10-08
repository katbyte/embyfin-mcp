package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/katbyte/embyfin-mcp/lib/mediapath"
	"github.com/katbyte/embyfin-mcp/lib/naming"
	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/katbyte/embyfin-mcp/sdk/tmdb"
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
	Name     string   `json:"name"               jsonschema:"what the server holds: an episode by its series and number"`
	Series   string   `json:"series,omitempty"`
	Season   *int     `json:"season,omitempty"`
	Episode  *int     `json:"episode,omitempty"`
	Path     string   `json:"path,omitempty"`
	Problems []string `json:"problems"           jsonschema:"each begins with the check that failed: series, season, episode, title, year or lookalike"`
	Episodes int      `json:"episodes,omitempty" jsonschema:"on a show's own row: how many of its episode files it stands for, which disagree with the server the one way (titles none of the server's, or files named for another series) and have no rows of their own"`
	Files    []string `json:"files,omitempty"    jsonschema:"on a show's own row: the files it stands for, under its path"`
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
	TitleMatched string `json:"title_matched,omitempty" jsonschema:"films and series: which of the item's other titles the path's title is, when it is not its name: its original title, its sort name, one TMDB lists for it, or one TMDB's search finds it by; or how it is one of them written another way"`
	PathTMDB     string `json:"path_tmdb,omitempty"     jsonschema:"films and series whose path names none of the item's titles, with EMBYFIN_TMDB_TOKEN set: the film or series TMDB's search finds by the path's title and year, as its TMDB id, title and year; or a film whose file, read whole, TMDB gives to another film"`
	ItemTMDB     string `json:"item_tmdb,omitempty"     jsonschema:"the TMDB id the item carries, beside path_tmdb"`
	Diagnosis    string `json:"diagnosis,omitempty"     jsonschema:"what the tmdb lookup made of it"`

	seriesID   string
	fileSeries string       // the series an episode's file is named for, when not the one the server holds it under
	item       embyfin.Item // what the row was made from: the TMDB lookups read its ids and runtime
	rank       int          // 0 a number or the series disagrees, 1 the title, 2 the year alone
	// named is set once TMDB has said the path names the item's own title
	// and its name is what is wrong, which the search by the path's title
	// then has nothing to add to
	named bool
	// partOneYearOff is set on a film whose folder names it but for its part
	// 1, dated a year off: TMDB is asked which part the folder's year names
	partOneYearOff bool
	// tmdbErr is the TMDB read the row's diagnosis says failed, which the
	// note counts; seriesAskErr the read of the titles TMDB lists for the
	// row's series, which left its series problem standing
	tmdbErr, seriesAskErr error
	// read is the title the path's title check read, which TitleInFile
	// quotes read whole when words follow the path's year (see naming.Claim)
	read string
}

type pathOut struct {
	Scanned  int            `json:"items_scanned"`
	Found    int            `json:"total_findings"`
	ByCheck  map[string]int `json:"by_check"       jsonschema:"findings by the check that failed; a row failing two counts under both"`
	Unnamed  int            `json:"unnamed"        jsonschema:"episode files whose name claims no title, so the title check had nothing to compare; not findings"`
	Findings []pathRow      `json:"findings"       jsonschema:"a number or the series disagreeing first, then titles least alike first, then years; capped at limit"`
	Note     string         `json:"note,omitempty" jsonschema:"set when TMDB could not be asked, and what that leaves unsaid, or when the library was seen to change while it was read: items added or removed meanwhile may be missing, or listed though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. It says nothing of a change when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read"`
	// changed is what the reads said of the library changing, apart from
	// the rest of the note: what audit_all reports
	changed string
}

func registerFilePathAudit(r *registry) {
	client := r.client
	// every TMDB read of the audit - titles, searches, runtimes, episode
	// lists - behind one breaker, so TMDB down is seen once and not paid for
	// row by row (see tmdb.Breaker)
	guarded := tmdb.Guarded(r.opts.ProviderTransport)
	provider := r.tmdbFacts(guarded)

	add(r, readTool, &mcp.Tool{
		Name: "audit_file_path",
		Description: "Find items whose file path disagrees with the metadata the server holds for them: the title, the (year), the series, the season and the episode number or run of numbers in the path against the item, every version of a film held in several files included. " +
			"A film's or a series' path is compared with every title the item goes by - its name, its original title, its sort name and, with EMBYFIN_TMDB_TOKEN set, every alternative title TMDB lists for its id - and a year one either side of the item's is the same film, so a folder in the film's own language, or dated the year before, is no finding. " +
			"Nor is a title written another way: spaced otherwise (initials, an apostrophe), an article swapped before a word, a number in digits or in words, or one vowel inside one word spelled otherwise in a title of three words or more ('Grey' for 'Gray'); nor a part 1 marked as one - after a part word, or bracketed - on one side alone ('Dune' and 'Dune: Part One', 'Pilot' and 'Pilot (1)'), when the folder is dated the item's year or not at all: a year off, the row says it can't tell whether the file is part 1 or another part, and with EMBYFIN_TMDB_TOKEN set names the other part the folder's year does and what the file's runtime backs. A different number, a Roman numeral or a word's first letter is never a spelling, and any other number on one side and not the other - a part ('Part 2' beside the title alone) or a number of the name ('Air Force One' beside 'Air Force') - is always another title, however alike the rest. A part is the same part written 'Part 2', 'Pt. 2', '(2)', 'Part Two' or a closing 'II'; Vol and Volume, Ep and Episode, Ch and Chapter are each one word, and a book is not a volume. A closing letter one side has and the other lacks ('Henry' and 'Henry V') is a numeral or a name, and the row says it can't tell. " +
			"A path naming another film says which film the file's runtime backs: the item's means the folder is likely what is wrong (likely: another cut of the other film can run as long), and with no runtime to go by a film of the item's own name says it can't tell which. A TMDB read is tried again when it fails in a way that passes; a row TMDB still cannot be asked about says so, and the note counts them. " +
			"A country's code or a year closing one title in brackets and not the other ('The Office (US)' beside 'The Office') is the same title, and title_matched says so; another country on each side may be the other country's version, and the row says it can't tell; a year one either side of the other is the same title. Other capitals in brackets - (OVA), (TV), (DC) - are words the title has, and a code that is the title's one word too ('It (IT)') may be part of the name, so the row says it can't tell. A 3D or a 4K is an edition's word, not a number. One title that is the other with words added - an edition, a subtitle ('Alien' and 'Alien Director's Cut') - is said as that, quoting the words as the path or the item writes them: the names alone can't tell an edition from another film. The year is the path's bracketed one, or the bare year a scene name gives ('Title.2011.1080p'), never one later than next year ('Zzyzx (2049)' is not dated), one that is the title's own or a number of any title the item goes by ('Blade.Runner.2049.1080p' holds no release year), or one after the encode's words. Of two bare years side by side the later dates the file only when the earlier is a number of a title the item goes by ('Blade.Runner.2049.2017'); otherwise the first does ('Zzyzx.1982.2021' is dated 1982). When that first year is off, the row gives both readings, the title dated by each, since the names alone can't tell which. A part 1 whose folder is dated a year off stays can't tell even when an original or alternative title is the plain title. " +
			"A path whose title is none of them, or whose year is two or more off, is the wrong match or another film or series: with EMBYFIN_TMDB_TOKEN set, the row says what TMDB's search finds by the path's title and year (path_tmdb, beside the item's own id), whether the file's runtime backs either, and when the server never probed the file so its runtime backs neither; the search finding the item itself under a title like the path's, or under another when the path's is its title in one of TMDB's translations, means the path's title is one it goes by, and a year still apart is the item's to check - finding it under another title settles nothing otherwise, as the search matches loosely, and a path named 'Franchise (Year) Subtitle' is searched by its whole title unless the words before the year are a title the item goes by - unless the item's own name is none TMDB gives that title, when the path is right and the name is what is wrong. The words before the year, asked when the whole title finds nothing, count only as the same title ('Alien' is not 'Alien 2'). A film's file whose words before its year are the film's own title and whose words after are more ('Dune (2021) Part Two' held as Dune) is, with EMBYFIN_TMDB_TOKEN, asked of TMDB by its whole title in no year, and its words after the year held against the films of the film's TMDB collection ('Alien (1979) - Aliens'): a title row when TMDB names a film of another id by them (path_tmdb) - another film matched to this one's ids, a film of its own whose title is the film's and a number alone ('Film: Part Two') or of its TMDB collection among them, or, when that film's title is the film's and an edition's words alone, an entry TMDB lists of its own that may be an edition of it. Words after the year that are an edition's alone ('Film (1982) - Final Cut') are asked nothing and are no row; without a token a label and a title after the year cannot be told apart, and neither is a row. A renamer's or a release's words after the year are no title: tags in brackets or braces, a trailing '-GROUP', quality, source, HDR, IMAX, 3D, language and dub words, a stacked file's part ('cd1', 'Disc 2') and an extra's word ('Sample', 'Trailer'), read across a hyphen or a plus ('Bluray-1080p', 'HDR10+', 'German-DL'); 'Part 2', the number set apart, is a title's. At most 500 films not asked before are asked about this way in one call, four at a time - an answer an earlier call got is read again for nothing - films TMDB failed on before go after those never asked, and the note says how many are left (call again to continue) and how many of them are ones TMDB failed on, which calling again only retries; TMDB failing three times in a row stops the asking for a minute, and the note says what was not asked. " +
			"A file named after one episode where the server holds another is a file from another series written to this path, or one numbered in another provider's order (with EMBYFIN_TMDB_TOKEN set, each such row says which episode TMDB gives the file's title to); a file's title is read every way its name allows - a serial's part after its story and part number, either title of a file holding two ('A & B'), a time with its colons dropped - and any of them will do. " +
			"A file named for its series by the series' original title, its sort name or a title TMDB lists for it is named for its series - that very name, or it written another way as above, never a name with words added, which is how a spin-off's short title reads. " +
			"A show where five or more files, and at least half, disagree the one way - titles none of the server's that TMDB (asked first) gives to no episode number, or the files naming a series all named for the same other one (the files may be another show's, or the show's match may be wrong) - is one row for the show, episodes saying how many files it stands for and files listing them, rather than a row a file; a file TMDB places at a number, or that gets anything else wrong, keeps its own row. " +
			"A file holding two episodes (S01E01E02) where the server lists one has the second's content on disk while the server calls it missing, and the reverse means the metadata claims a run the file does not. " +
			"A name spelled with a letter of another script that only looks Latin (a Cyrillic A, U+0410, spelling a Latin title) is a lookalike row naming the letter and the plain spelling, because it is not the title it reads as: Emby's search for the plain title does not find it, and Jellyfin's does only from 12.2. " +
			"The path is what was placed on disk and the metadata what a provider or an edit set, so a row says which to fix by what it holds. item_identify or item_edit set the metadata right; a rename fixes the path. ids checks a handful of items without a sweep.",
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
		// read first: an id the server cannot use as a filter is not a
		// narrower sweep, it is the whole library or nothing (see checkIDs)
		what := "films, series and episodes"
		if strings.TrimSpace(in.Types) != "" {
			what = "types " + opts.IncludeItemTypes
		}
		if err := checkIDs(ctx, client, ids, strings.Split(opts.IncludeItemTypes, ","), what); err != nil {
			return pathOut{}, err
		}
		opts.IDs = strings.Join(ids, ",")
	}

	out := pathOut{Findings: []pathRow{}, ByCheck: map[string]int{}}
	var findings []pathRow
	var versioned []string
	shows := map[string]*showTally{} // series id -> what its episode files claim
	// films whose file reads as the film by the words before its year alone,
	// with more after it: TMDB is asked whether the whole title names
	// another film (wholeFilm), as the version and duplicate warnings ask
	type whole struct {
		row   pathRow
		claim naming.Claim
	}
	var wholes []whole
	check := func(it *embyfin.Item) {
		row, unnamed := checkPath(it, want, client.Backend())
		if unnamed {
			out.Unnamed++
		}
		if it.Type == typeEpisode && it.SeriesID != "" {
			tally := shows[it.SeriesID]
			if tally == nil {
				tally = &showTally{name: it.SeriesName}
				shows[it.SeriesID] = tally
			}
			if want["title"] && it.Name != "" && !unnamed {
				tally.named++
			}
			if f := naming.ParseSegment(mediapath.Base(it.Path)); f.Title != "" && f.Episode > 0 {
				tally.marked++
			}
		}
		if len(row.Problems) > 0 {
			findings = append(findings, row)

			return
		}
		if claim, ok := wholeClaim(it, it.Path); ok && provider != nil && want["title"] {
			wholes = append(wholes, whole{row, claim})
		}
	}
	swept, err := client.ReadAll(ctx, opts, embyfin.ToAnswer, func(items []embyfin.Item) bool {
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
	})
	if err != nil {
		return pathOut{}, err
	}
	out.changed = swept.Changed()

	// every other version's file, read in batches: a film shown in two files
	// can have the wrong one in either
	for chunk := range slices.Chunk(versioned, idsPerRequest) {
		versions, readErr := client.ReadAll(ctx, embyfin.SearchOptions{IDs: strings.Join(chunk, ","), Fields: opts.Fields + ",MediaSources"}, embyfin.ToAnswer, func(items []embyfin.Item) bool {
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
		})
		if readErr != nil {
			return pathOut{}, readErr
		}
		out.changed = joinWarnings(out.changed, versions.Changed())
	}
	// a file named for the series by its original title, its sort name or a
	// title TMDB lists for it is named for this series
	var seriesRead string
	findings, seriesRead, err = clearSeriesTitles(ctx, client, provider, findings)
	if err != nil {
		return pathOut{}, err
	}
	out.changed = joinWarnings(out.changed, seriesRead)
	out.Note = out.changed
	// what TMDB says of each episode's title comes before a show's rows are
	// rolled up: a file TMDB places at another number is its own row, not
	// part of the show's
	if provider != nil {
		if err := diagnoseByTMDB(ctx, client, provider, rollupCandidates(findings, shows)); err != nil {
			return pathOut{}, err
		}
	}
	// a show whose files most of all disagree the one way is one row
	findings = rollUpShows(findings, shows)

	// a title TMDB lists for the item is one it goes by. TMDB failing to
	// answer leaves that row as the server alone makes it, and says so on
	// it: the rest are still asked (see tmdbNote)
	if provider != nil {
		kept := findings[:0]
		for i := range findings {
			trial := findings[i]
			if err := clearAlternativeTitle(ctx, provider, &trial); err != nil {
				couldNotAsk(&findings[i], "which provider TMDB lists for the item", err)
			} else {
				findings[i] = trial
			}
			if len(findings[i].Problems) > 0 {
				kept = append(kept, findings[i])
			}
		}
		findings = kept
	}
	// the films whose file names more than the film after its year, asked
	// of TMDB a few at a time, and no more than wholeLimit of them not asked
	// before in one call - an answer TMDB gave an earlier call is read again
	// for nothing - so each call goes on where the last stopped. Films never
	// asked go before films TMDB failed to answer before, so a few it keeps
	// failing on do not stand in front of the rest of the library; what was
	// not asked, and why, is said
	if provider != nil && len(wholes) > 0 {
		var asked []whole
		var retry []int
		// pending counts the films left for a later call, and pendingRetry
		// those of them TMDB failed on before
		fresh, pending, pendingRetry := 0, 0, 0
		take := func(i int, failedBefore bool) {
			if fresh < wholeLimit {
				fresh++
				asked = append(asked, wholes[i])

				return
			}
			pending++
			if failedBefore {
				pendingRetry++
			}
		}
		for i := range wholes {
			switch {
			case provider.Asked("movie", wholes[i].claim.Whole, 0):
				asked = append(asked, wholes[i])
			case provider.FailedBefore("movie", wholes[i].claim.Whole, 0):
				retry = append(retry, i)
			default:
				take(i, false)
			}
		}
		for _, i := range retry {
			take(i, true)
		}
		// failed counts the films TMDB could not be asked about this call, and
		// failedOn those of them it failed on - the rest the breaker kept from
		// asking, which the next call asks as never asked
		failed, failedOn := 0, 0
		var firstErr error
		for i, r := range askWholes(ctx, provider, len(asked), func(i int) (naming.Claim, *embyfin.Item) { return asked[i].claim, &asked[i].row.item }) {
			w := &asked[i]
			switch {
			case r.found != nil:
				wholeRow(ctx, provider, &w.row, w.claim, r.found)
				if r.err != nil {
					w.row.Diagnosis += fmt.Sprintf("; TMDB could not be asked whether it is a film of this one's series (%v)", r.err)
				}
				findings = append(findings, w.row)
			case r.err != nil:
				failed++
				firstErr = cmp.Or(firstErr, r.err)
				if provider.FailedBefore("movie", w.claim.Whole, 0) {
					failedOn++
				}
			}
		}
		var notes []string
		if out.Note != "" {
			notes = append(notes, out.Note)
		}
		if failed > 0 {
			notes = append(notes, fmt.Sprintf("TMDB could not be asked about %d of the %d films read this call (%v), so a title there is not told apart from a version's label", failed, len(asked), firstErr))
		}
		// what is left: films never asked, which calling again goes on to,
		// and films TMDB failed on, which calling again only retries
		retries := failedOn + pendingRetry
		switch still := pending + failed; {
		case still > retries && retries > 0:
			notes = append(notes, fmt.Sprintf("call again to continue: %d of the %d films whose file names more than the film after its year are still to ask (one call asks TMDB about at most %d not asked before), %d of them ones TMDB failed on, which calling again retries", still, len(wholes), wholeLimit, retries))
		case still > retries:
			notes = append(notes, fmt.Sprintf("call again to continue: %d of the %d films whose file names more than the film after its year are still to ask (one call asks TMDB about at most %d not asked before)", still, len(wholes), wholeLimit))
		case still > 0:
			notes = append(notes, fmt.Sprintf("%d of the %d films whose file names more than the film after its year are ones TMDB failed on, and every other one is asked; calling again retries them", still, len(wholes)))
		}
		out.Note = strings.Join(notes, "; ")
	}

	// the plainest disagreements first: a number the file and the server
	// read differently, then the provider least alike, then years
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
	if provider != nil {
		findings = diagnoseTitlesByTMDB(ctx, provider, findings, limit)
	}
	for i := range findings {
		for _, p := range findings[i].Problems {
			out.ByCheck[strings.SplitN(p, ":", 2)[0]]++
		}
	}
	out.Found = len(findings)
	out.Findings = append(out.Findings, findings[:min(len(findings), limit)]...)
	if provider != nil {
		rows := make([]*pathRow, 0, len(out.Findings))
		for i := range out.Findings {
			if out.Findings[i].Diagnosis == "" {
				rows = append(rows, &out.Findings[i])
			}
		}
		if err := diagnoseByTMDB(ctx, client, provider, rows); err != nil {
			return pathOut{}, err
		}
	}
	// what the whole-title asking left unasked, beside the rows TMDB could
	// not be asked about
	var notes []string
	for _, n := range []string{out.Note, tmdbNote(out.Findings, findings[min(len(findings), limit):])} {
		if n != "" {
			notes = append(notes, n)
		}
	}
	out.Note = strings.Join(notes, "; ")

	return out, nil
}

// editionEntry says whether TMDB's entry h, named by a path, is one the title
// check reads as maybe an edition of the item (entryOf): titled the item's
// and an edition's words alone.
func editionEntry(it *embyfin.Item, h tmdb.Hit) bool {
	rest, ok := titleRest(it, h)

	return ok && restNumber(rest) == "" && editionRest(rest)
}

// wholeLimit is how many films' files audit_file_path asks TMDB about by
// their title read whole in one call, and wholeAsks how many at once.
const (
	wholeLimit = 500
	wholeAsks  = 4
)

// wholeAnswer is what TMDB said of one file read whole (wholeFilm).
type wholeAnswer struct {
	found *wholeFinding
	err   error
}

// askWholes asks TMDB about n files read whole, wholeAsks at a time, each
// given by at, and answers in their order.
func askWholes(ctx context.Context, titles *tmdb.Facts, n int, at func(int) (naming.Claim, *embyfin.Item)) []wholeAnswer {
	answers := make([]wholeAnswer, n)
	slots := make(chan struct{}, wholeAsks)
	var wg sync.WaitGroup
	for i := range n {
		claim, it := at(i)
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			answers[i].found, answers[i].err = wholeFilm(ctx, titles, it, claim)
		})
	}
	wg.Wait()

	return answers
}

// wholeRow makes row the finding of a film whose file TMDB says names
// another film by its title read whole (wholeFilm): another film matched to
// this one's ids, or an entry TMDB lists of its own that may be an edition of
// it.
func wholeRow(ctx context.Context, facts *tmdb.Facts, row *pathRow, claim naming.Claim, found *wholeFinding) {
	v := found.verdict(&row.item, claim)
	h, own := found.hit, providerID(&row.item, "tmdb")
	row.TitleInFile, row.TitleOnServer, row.rank = claim.Whole, row.item.Name, 1
	row.Score, _ = naming.Score(claim.Whole, row.item.Name)
	row.PathTMDB, row.ItemTMDB = fmt.Sprintf("%d %s (%d)", h.ID, h.Title, h.Year), own
	if v.says == saysOwnEntry {
		row.Problems = append(row.Problems, "title: "+v.why)
		row.Diagnosis = fmt.Sprintf("TMDB lists %d %s (%d) as an entry of its own, and its title begins with the film's: an edition of this film TMDB keeps apart, or another film matched to its ids - check the file", h.ID, h.Title, h.Year)

		return
	}
	row.Problems = append(row.Problems, "title: "+v.why+": another film matched to this one's ids")
	evidence, _ := runtimeEvidence(ctx, facts, &row.item, strconv.Itoa(h.ID), own)
	row.Diagnosis = fmt.Sprintf("the path names TMDB's film %d, %s (%d); the item carries TMDB %s: it is matched to another film than the one on disk", h.ID, h.Title, h.Year, own) + evidence
}

// checkPath runs the wanted checks over one item. unnamed reports an episode
// file whose name claims no title, which the title check cannot judge.
func checkPath(it *embyfin.Item, want map[string]bool, backend embyfin.Backend) (row pathRow, unnamed bool) {
	row = pathRow{
		ID: it.ID, Type: it.Type, Name: episodeOrItemName(it), Path: it.Path,
		Series: it.SeriesName, Season: it.ParentIndexNumber, Episode: it.IndexNumber, seriesID: it.SeriesID, item: *it, rank: 2,
	}
	problem := func(check, detail string) {
		row.Problems = append(row.Problems, check+": "+detail)
	}

	// a letter of another script drawn like a Latin one: the name reads
	// right and is another name to whatever compares its letters. Emby's
	// search is one such; Jellyfin's reads the two letters as one from 12.2.
	// The title checks below compare the plain spelling, so the one fault is
	// reported once
	letters, plain := naming.LookalikeLetters(it.Name)
	if want["lookalike"] && len(letters) > 0 {
		missed := fmt.Sprintf("a search for %q does not find it", plain)
		if backend == embyfin.Jellyfin {
			missed = fmt.Sprintf("whatever compares the name letter by letter does not take it for %q (Jellyfin's own search does from 12.2)", plain)
		}
		problem("lookalike", fmt.Sprintf("%q is spelled with %s, so it only looks like %q and %s: item_edit name %q puts it right", it.Name, strings.Join(letters, " and "), plain, missed, plain))
		row.rank = min(row.rank, 1)
	}

	if it.Type == typeEpisode {
		file := naming.ParseSegment(mediapath.Base(it.Path))
		// the series and the numbers are only what the file itself says: a
		// bare "S01E01.mkv" under a series folder claims nothing about the
		// series, and the server took the folder's word too. Nor does a name
		// with no episode marker to end a title at: "01 - Pilot.mkv",
		// "Episode 1.mkv" or a fansub's "[Grp] Show - 01 [1080p].mkv" is all
		// title to the parser, and read as a series name every one of them
		// was a file from another show.
		if want["series"] && file.Title != "" && file.Episode > 0 && it.SeriesName != "" {
			if score, _ := naming.Score(file.Title, it.SeriesName); score < seriesConfident {
				problem("series", fmt.Sprintf("the file is named for %q, the server holds it under %q: a file from another series written to this path", file.Title, it.SeriesName))
				row.rank = 0
				row.fileSeries = file.Title
			}
		}
		if file.Episode > 0 {
			if want["season"] && (it.ParentIndexNumber == nil || file.Season != *it.ParentIndexNumber) {
				held := "no season number for it"
				if it.ParentIndexNumber != nil {
					held = fmt.Sprintf("season %d", *it.ParentIndexNumber)
				}
				problem("season", fmt.Sprintf("the file says season %d, the server holds %s", file.Season, held))
				row.rank = 0
			}
			if want["episode"] {
				if detail := episodeRunProblem(file, it, backend); detail != "" {
					problem("episode", detail)
					row.rank = 0
				}
			}
		}
		if want["title"] && it.Name != "" {
			// any reading of the file's name that is the server's title will
			// do: the part of a serial, one of two titles of a run joined by
			// "&". A serial's story alone only against a title that is the
			// story and nothing more: part 1 filed as part 2 is a finding
			claimed, story := naming.EpisodeTitlesFromFile(it.Path)
			matches := slices.ContainsFunc(claimed, func(c string) bool { return naming.SameTitle(c, plain) }) ||
				story != "" && naming.SameName(story, plain)
			switch {
			case len(claimed) == 0:
				unnamed = true
			case !matches:
				score, _ := naming.Score(claimed[0], plain)
				// one title with words added is an edition, a subtitle, or
				// another episode, and the names alone can't say which
				detail := ""
				if w, longer, ok := naming.WordsAdded(naming.FormOf(claimed[0]), naming.FormOf(plain)); ok && naming.Judge(naming.FormOf(claimed[0]), naming.FormOf(plain)) == naming.Different {
					detail = fmt.Sprintf(": the server's title is the file's with %q added: an edition, a subtitle, or another episode - can't tell from the names", w)
					if longer == 1 {
						detail = fmt.Sprintf(": the file's title is the server's with %q added: an edition, a subtitle, or another episode - can't tell from the names", w)
					}
				}
				problem("title", fmt.Sprintf("the file is named %q, the server holds %q%s", claimed[0], it.Name, detail))
				row.TitleInFile, row.TitleOnServer, row.Score = claimed[0], it.Name, score
				row.rank = min(row.rank, 1)
			}
		}

		return row, unnamed
	}

	// a film or a series: the path's own segment names it, with its year.
	// The year is the path's bracketed one, or the bare year a scene name
	// gives, as the title was read cut at it
	what := "film"
	if tmdbKind(it.Type) == "tv" {
		what = "series"
	}
	// the bare year, read against every title the item goes by: a name
	// holding a number in words ("Seven Zzyzx") reads a scene name in the
	// original title's words ("Shichinin.no.Zzyzx.1956") as numbered apart,
	// and its year as none. The year is what a title's reading dates the
	// file by (naming.TitleAndYearFromPath), and never a number of any title the
	// item goes by: a sort name "Zzyzx" cut "Zzyzx.2012" at the name's 2012
	var titles []string
	for _, k := range knownTitles(it) {
		titles = append(titles, k.Title)
	}
	titleNumbers := naming.NumbersOf(titles...)
	nameYear := 0
	for _, k := range knownTitles(it) {
		if _, _, y := naming.TitleAndYearFromPath(it.Path, k.Title, titleNumbers); y != 0 && !titleNumbers[y] {
			nameYear = y

			break
		}
	}
	if want["title"] && it.Name != "" {
		// every title the item goes by, and the path's against the nearest:
		// a folder in the film's own language names its original title,
		// and one the server holds by its English one is the same film
		claimed, score, as, variant := "", -1.0, "", ""
		matched := false
		apart := map[naming.Verdict]string{} // verdict -> the first title the path is that to
		partOneOff, folderYear := "", 0      // a title the path is but for a part 1, a year off
		added, addedLonger := "", 0          // words one title has beyond the other
		for _, k := range knownTitles(it) {
			c, s, cutYear := naming.TitleAndYearFromPath(it.Path, k.Title, titleNumbers)
			fc, fk := naming.FormOf(c), naming.FormOf(k.Title)
			if titleNumbers[cutYear] {
				cutYear = 0 // a number of a title the item goes by dates nothing
			}
			year := cmp.Or(naming.PathYear(it.Path), cutYear)
			// the verdict, not the score: a part or a number on one side
			// and not the other is another film however alike the rest, and
			// a closing letter on one side alone is a numeral or a name
			v := naming.Judge(fc, fk)
			// a part 1 on one side alone is the first part only when the
			// folder is dated the item's year, or not at all: a year on, it
			// may be the second part filed under the first
			if v == naming.Same && naming.PartOneApart(fc, fk) && year != 0 && it.ProductionYear != 0 && year != it.ProductionYear {
				if partOneOff == "" {
					partOneOff, folderYear = k.Title, year
				}
				v = naming.CantTell
			} else if v != naming.Same && apart[v] == "" {
				apart[v] = k.Title
			}
			if w, longer, ok := naming.WordsAdded(fc, fk); ok && v == naming.Different && added == "" {
				added, addedLonger = w, longer
			}
			same := v == naming.Same
			if same && !matched || !matched && s > score {
				claimed, score, as = c, s, k.As
			}
			matched = matched || same
			// the same title written another way - spaced, an article, a
			// number in words, one letter spelled otherwise, a part 1 named
			// on one side alone - says how
			if how, ok := naming.VariantOf(fc, fk); ok && same && (s < seriesConfident || fc.Qualifier != fk.Qualifier) && variant == "" {
				variant = fmt.Sprintf("%s: %s", k.As, how)
			}
		}
		// the words after a (year) are part of the path's title when the
		// words before it are none the item goes by: "Zzyzx (2016) Unlimited
		// - Mechs" names "Zzyzx Unlimited - Mechs", where "Zzyzx" alone is a
		// word a dozen films begin with. Read so, the whole title is judged
		// against every title the item goes by, as the reading was; the
		// reading stays what TMDB's search asks by after it (read)
		read := claimed
		if whole, before := naming.YearParts(mediapath.Base(naming.TitledPath(it.Path))); !matched && partOneOff == "" && claimed != "" && whole != "" &&
			!slices.ContainsFunc(heldTitles(it), func(h string) bool { return naming.Like(before, h) }) {
			claimed, score = whole, -1
			apart, added, addedLonger = map[naming.Verdict]string{}, "", 0
			for _, k := range knownTitles(it) {
				fc, fk := naming.FormOf(whole), naming.FormOf(k.Title)
				v := naming.Judge(fc, fk)
				if s, _ := naming.Score(whole, k.Title); s > score {
					score = s
				}
				switch {
				case v == naming.Same:
					matched, as = true, k.As
				case apart[v] == "":
					apart[v] = k.Title
				}
				if w, longer, ok := naming.WordsAdded(fc, fk); ok && v == naming.Different && added == "" {
					added, addedLonger = w, longer
				}
			}
		}
		// the row quotes the title named, and keeps the one the title check
		// read, which the TMDB search asks by
		titleProblem := func(detail string) {
			problem("title", fmt.Sprintf("the path is named %q, the server holds %q%s", claimed, it.Name, detail))
			row.TitleInFile, row.TitleOnServer, row.Score, row.read = claimed, it.Name, score, read
			row.rank = min(row.rank, 1)
		}
		numberedApart := "another film of the series, or the wrong match"
		if what == "series" {
			numberedApart = "another series of the name, or the wrong match"
		}
		switch {
		case claimed == "":
		case partOneOff != "":
			// before a match by another title: an original, sort or other
			// title that is the plain title names the film or its series
			// alike, and settles nothing of which part the folder holds
			titleProblem(fmt.Sprintf(": the same title but for a part 1 on one side, and the folder dated %d where the item is %d - part 1, or another part filed under it, and can't tell which", folderYear, it.ProductionYear))
			row.partOneYearOff = true
		case matched && variant != "":
			row.TitleMatched = variant
		case matched && as != naming.AsName:
			row.TitleMatched = as
		case matched:
		case apart[naming.CantTell] != "":
			titleProblem(": alike, but one closes on a letter or a country the other does not - a sequel's numeral, another country's version, or part of the name: can't tell which")
		case apart[naming.NumberedApart] != "":
			titleProblem(": alike but numbered apart, a part or a number on one and not the other - " + numberedApart)
		case added != "" && addedLonger == 1:
			titleProblem(fmt.Sprintf(": the path's title is the item's with %q added: an edition, a subtitle, or another %s - can't tell from the names", added, what))
		case added != "":
			titleProblem(fmt.Sprintf(": the item's title is the path's with %q added: an edition, a subtitle, or another %s - can't tell from the names", added, what))
		default:
			titleProblem(", and the path's title is none of the titles it goes by: the wrong match, or another " + what)
		}
	}
	if want["year"] {
		if detail, off := checkYearMismatch(it, nameYear); off {
			// two years side by side, the earlier no number of the item's
			// titles: either may date the file, and "path says" the first
			// alone would say what the names can't. When the other reading
			// is dated the item's year, its title is another: the title or
			// the year disagrees
			if a, b, ok := naming.TwoReadings(it.Path, titleNumbers); ok && a.Year == nameYear {
				detail = fmt.Sprintf("the path reads as %q dated %d, or %q dated %d, can't tell which; neither is %d", a.Title, a.Year, b.Title, b.Year, it.ProductionYear)
				if abs(b.Year-it.ProductionYear) <= 1 {
					detail = fmt.Sprintf("the path reads as %q dated %d, or %q dated %d: the title or the year disagrees, can't tell which", a.Title, a.Year, b.Title, b.Year)
				}
			}
			problem("year", detail)
		}
	}

	return row, false
}

// showTally is what a show's episode files claim, for rollUpShows: how many
// are named with a title, and how many name a series beside their marker.
type showTally struct {
	name          string
	named, marked int
}

// showRollupMin is how many episode rows of one show, disagreeing the one
// way, make a row for the show instead: fewer are each worth their own.
const showRollupMin = 5

// titleOnly says whether an episode row's one problem is its title and TMDB
// has not placed that title at a number: the rows a show's title row can
// stand for. A title TMDB gives to another number of the show is a file
// numbered another way, and one it gives to this very number a title the
// server reworded; each of those says something of its own.
func titleOnly(r *pathRow) bool {
	return r.Type == typeEpisode && r.seriesID != "" && r.TMDBEpisode == "" &&
		len(r.Problems) == 1 && strings.HasPrefix(r.Problems[0], "title:")
}

// rollupCandidates are the rows that would be rolled up into their show's
// row by title (see rollUpShows), for TMDB to be asked about first.
func rollupCandidates(rows []pathRow, shows map[string]*showTally) []*pathRow {
	bySeries := map[string][]*pathRow{}
	for i := range rows {
		if titleOnly(&rows[i]) {
			bySeries[rows[i].seriesID] = append(bySeries[rows[i].seriesID], &rows[i])
		}
	}
	var out []*pathRow
	for id, list := range bySeries {
		if tally := shows[id]; tally != nil && len(list) >= showRollupMin && len(list)*2 >= tally.named {
			out = append(out, list...)
		}
	}

	return out
}

// rollUpShows turns a show's episode rows into one row for the show when
// most of its files disagree with the server the same way: their titles none
// of the server's and none TMDB places at a number, or every file named for
// the same other series than any title the show goes by. Row by row, one
// long-running show was thousands of rows that said one thing and buried the
// few that said something of their own. A show where only some files differ
// keeps its rows, and so does every row TMDB placed at a number or that gets
// anything else wrong; the show's row lists the files it stands for.
func rollUpShows(rows []pathRow, shows map[string]*showTally) []pathRow {
	titled := map[string][]int{}                // series id -> rows whose one problem is a title TMDB placed nowhere
	namedOther := map[string]map[string][]int{} // series id -> the other series' folded name -> rows named for it
	for i := range rows {
		r := &rows[i]
		if r.Type != typeEpisode || r.seriesID == "" {
			continue
		}
		if titleOnly(r) {
			titled[r.seriesID] = append(titled[r.seriesID], i)
		}
		if r.fileSeries != "" && slices.ContainsFunc(r.Problems, func(p string) bool { return strings.HasPrefix(p, "series:") }) {
			if namedOther[r.seriesID] == nil {
				namedOther[r.seriesID] = map[string][]int{}
			}
			key := naming.Normalise(r.fileSeries)
			namedOther[r.seriesID][key] = append(namedOther[r.seriesID][key], i)
		}
	}

	drop := map[int]bool{}
	var shown []pathRow
	for _, id := range slices.Sorted(maps.Keys(shows)) {
		tally := shows[id]
		// the series most of the files are named for, when one is
		var other []int
		for _, key := range slices.Sorted(maps.Keys(namedOther[id])) {
			if list := namedOther[id][key]; len(list) > len(other) {
				other = list
			}
		}
		switch {
		case len(other) >= showRollupMin && len(other)*2 >= tally.marked:
			first := rows[other[0]]
			show := showRow(id, tally, rows, other, 0,
				fmt.Sprintf("series: %d of the %d files named for a series are named for %q, none of the titles %q goes by (the first, %s): the files may be another show's, or the show's match may be wrong - compare them before changing either",
					len(other), tally.marked, first.fileSeries, tally.name, mediapath.Base(first.Path)))
			for _, i := range other {
				if err := rows[i].seriesAskErr; err != nil && show.tmdbErr == nil {
					couldNotAsk(&show, "which titles TMDB lists for the series", err)
				}
				rows[i].seriesAskErr = nil
				// what else a file gets wrong is still its own row
				rows[i].Problems = slices.DeleteFunc(rows[i].Problems, func(p string) bool { return strings.HasPrefix(p, "series:") })
				rows[i].rank = rankOf(rows[i].Problems)
				if len(rows[i].Problems) == 0 {
					drop[i] = true
				}
			}
			shown = append(shown, show)
		case len(titled[id]) >= showRollupMin && len(titled[id])*2 >= tally.named:
			list := titled[id]
			first := rows[list[0]]
			show := showRow(id, tally, rows, list, 1,
				fmt.Sprintf("title: %d of the %d episode files named with a title are named otherwise than the server holds them (the first, %q where the server holds %q): compare the files, listed in files",
					len(list), tally.named, first.TitleInFile, first.TitleOnServer))
			// what TMDB made of them, when it is one thing: a read that
			// failed, or every title one TMDB gives to no episode
			for _, i := range list {
				if err := rows[i].tmdbErr; err != nil && show.tmdbErr == nil {
					couldNotAsk(&show, "for the show's episodes", err)
				}
			}
			switch {
			case show.tmdbErr != nil:
			case !slices.ContainsFunc(list, func(i int) bool { return !strings.HasPrefix(rows[i].Diagnosis, "no TMDB episode") }):
				show.Diagnosis = "TMDB's titles match none of these files' titles as written"
			case !slices.ContainsFunc(list, func(i int) bool { return rows[i].Diagnosis != first.Diagnosis }):
				show.Diagnosis = first.Diagnosis
			}
			for _, i := range list {
				drop[i] = true
			}
			shown = append(shown, show)
		}
	}

	kept := make([]pathRow, 0, len(rows)-len(drop)+len(shown))
	for i := range rows {
		if drop[i] {
			continue
		}
		// a series problem left standing because TMDB's titles for the
		// series could not be read says so
		if err := rows[i].seriesAskErr; err != nil && rows[i].Diagnosis == "" {
			couldNotAsk(&rows[i], "which titles TMDB lists for the series", err)
		}
		kept = append(kept, rows[i])
	}

	return append(kept, shown...)
}

// showRow is the one row a show's disagreeing episode rows become, listing
// their files under the folder they share.
func showRow(id string, tally *showTally, rows []pathRow, of []int, rank int, problem string) pathRow {
	folder := mediapath.Dir(rows[of[0]].Path)
	for _, i := range of[1:] {
		for folder != "" && !mediapath.Within(rows[i].Path, folder) {
			folder = mediapath.Dir(folder)
		}
	}
	files := make([]string, 0, len(of))
	for _, i := range of {
		files = append(files, strings.TrimLeft(strings.TrimPrefix(rows[i].Path, folder), `/\`))
	}
	slices.Sort(files)

	return pathRow{ID: id, Type: "Series", Name: tally.name, Series: tally.name, Path: folder, Problems: []string{problem}, Episodes: len(of), Files: files, seriesID: id, rank: rank}
}

// clearSeriesTitles takes the series problem off an episode row whose file
// is named for its series by another title the series goes by: its original
// title, its sort name, or, with TMDB, a title TMDB lists for it. Compared
// with the series' own name alone, every file of a show filed under its
// original title read as another show's, and the show as matched to the
// wrong one - which invites a re-identify, a write. A row with nothing left
// wrong goes.
//
// A series whose TMDB titles could not be read leaves its rows' series
// problem standing, and each row says so (seriesAskErr, then rollUpShows).
// changed is what the read of the series saw of the library changing.
func clearSeriesTitles(ctx context.Context, client *embyfin.Client, titles *tmdb.Facts, rows []pathRow) (kept []pathRow, changed string, err error) {
	ids := map[string]bool{}
	for i := range rows {
		if rows[i].fileSeries != "" {
			ids[rows[i].seriesID] = true
		}
	}
	if len(ids) == 0 {
		return rows, "", nil
	}
	known := map[string][]string{} // series id -> every title it goes by
	failed := map[string]error{}   // series id -> why TMDB's titles for it could not be read
	for chunk := range slices.Chunk(slices.Sorted(maps.Keys(ids)), idsPerRequest) {
		read, err := client.ReadAll(ctx, embyfin.SearchOptions{IDs: strings.Join(chunk, ","), IncludeItemTypes: "Series", Fields: "OriginalTitle,SortName,ProviderIds"}, embyfin.ToAnswer, func(items []embyfin.Item) bool {
			for i := range items {
				it := &items[i]
				for _, k := range knownTitles(it) {
					known[it.ID] = append(known[it.ID], k.Title)
				}
				if id := providerID(it, "tmdb"); id != "" && titles != nil {
					alts, aerr := titles.AlternativeTitles(ctx, "tv", id)
					if aerr != nil {
						// the series' own titles are still compared; its
						// rows say what TMDB could not be asked
						failed[it.ID] = aerr

						continue
					}
					known[it.ID] = append(known[it.ID], alts...)
				}
			}

			return true
		})
		if err != nil {
			return nil, "", fmt.Errorf("reading the series the files are named for: %w", err)
		}
		changed = joinWarnings(changed, read.Changed())
	}

	kept = rows[:0]
	for i := range rows {
		r := rows[i]
		if r.fileSeries != "" && slices.ContainsFunc(known[r.seriesID], func(t string) bool { return naming.SameName(r.fileSeries, t) }) {
			r.Problems = slices.DeleteFunc(r.Problems, func(p string) bool { return strings.HasPrefix(p, "series:") })
			r.fileSeries = ""
			r.rank = rankOf(r.Problems)
		}
		if r.fileSeries != "" {
			r.seriesAskErr = failed[r.seriesID]
		}
		if len(r.Problems) > 0 {
			kept = append(kept, r)
		}
	}

	return kept, changed, nil
}

// episodeRunProblem compares the episode number, or the run of them, a file
// claims with the one the server holds. A file holding two episodes that
// the server lists as one has the second's content on disk while the server
// calls it missing - and when the file spells its run in a style the server
// does not read (naming.RunStyleRead), that is why, and the fix is the name;
// a server holding a run the file does not claim has metadata that says more
// than the file.
func episodeRunProblem(file naming.Release, it *embyfin.Item, backend embyfin.Backend) string {
	name := func(first, last int) string {
		if last > first {
			return fmt.Sprintf("E%02d-E%02d", first, last)
		}

		return fmt.Sprintf("E%02d", first)
	}
	fileEnd := max(file.EpisodeEnd, file.Episode)
	if it.IndexNumber == nil {
		return fmt.Sprintf("the file says %s, the server holds no episode number for it", name(file.Episode, fileEnd))
	}
	held := *it.IndexNumber
	serverEnd := max(it.IndexNumberEnd, held)
	if file.Episode == held && fileEnd == serverEnd {
		return ""
	}
	switch {
	case file.Episode == held && fileEnd > serverEnd:
		// a run the server does not read is why it holds the first alone
		if w := runWarning(file, backend); w != "" {
			return fmt.Sprintf("the file holds %s, the server holds %s alone: %s", name(file.Episode, fileEnd), name(held, serverEnd), w)
		}

		return fmt.Sprintf("the file holds %s, the server holds %s alone: the rest of the run is on disk while the server lists it missing", name(file.Episode, fileEnd), name(held, serverEnd))
	case file.Episode == held:
		return fmt.Sprintf("the server holds %s, the file claims %s: the metadata says a run the file name does not", name(held, serverEnd), name(file.Episode, fileEnd))
	default:
		return fmt.Sprintf("the file says %s, the server holds %s", name(file.Episode, fileEnd), name(held, serverEnd))
	}
}

// checkYearMismatch compares the year in an item's path with its metadata
// year: two or more apart is a wrong edition or a wrong match. The last
// (year) in the path is the item's own - a collection folder above it can
// carry the first film's - and with none bracketed, the bare year a scene
// name gives ("Title.2013.1080p"), as the title was read cut at it: bare is
// the year the title check cut the name's title at, never a year that is
// the title's own ("Death Race 2000").
func checkYearMismatch(it *embyfin.Item, bare int) (string, bool) {
	if it.Path == "" || it.ProductionYear == 0 {
		return "", false
	}
	year := cmp.Or(naming.PathYear(it.Path), bare)
	if year == 0 {
		return "", false
	}
	if diff := year - it.ProductionYear; diff >= 2 || diff <= -2 {
		return "path says " + strconv.Itoa(year) + ", metadata says " + strconv.Itoa(it.ProductionYear), true
	}

	return "", false
}

// diagnoseByTMDB looks each reported episode's file title up in TMDB's list
// of the series' episodes, its specials included, one read per series, and
// says on the row where TMDB puts that title: the same number as the file is
// a title the server has reworded, another number is a file numbered in a
// different order, and no number is a title TMDB has never heard of. A row
// whose series TMDB could not be asked about says why instead, and the note
// counts it: left without a word, a row with no tmdb_episode read
// as one TMDB had nothing to say about.
func diagnoseByTMDB(ctx context.Context, client *embyfin.Client, provider *tmdb.Facts, rows []*pathRow) error {
	type read struct {
		titles *episodeTitles // TMDB's episodes by their titles
		why    string         // why TMDB was not asked, or could not answer
		err    error          // what TMDB failed with, nil when it answered or was not asked
	}
	guides := map[string]read{} // series item id -> TMDB's episodes
	for _, row := range rows {
		if row.Type != typeEpisode || row.TitleInFile == "" {
			continue
		}
		g, ok := guides[row.seriesID]
		if !ok {
			eps, why, tmdbErr, err := tmdbGuide(ctx, client, provider, row.seriesID)
			if err != nil {
				return err
			}
			g = read{titles: indexEpisodes(eps), why: why, err: tmdbErr}
			guides[row.seriesID] = g
		}
		if g.why != "" {
			// every row TMDB could not answer for is counted (tmdbNote),
			// not every series
			row.Diagnosis, row.tmdbErr = g.why, g.err

			continue
		}
		best, bestScore, parts := g.titles.best(row.TitleInFile)
		switch {
		case len(parts) > 0:
			// a story TMDB gives only with part numbers: one of them, and
			// which is not for a title to say
			row.Diagnosis = fmt.Sprintf("TMDB gives this title, with a part number, to %s: can't tell which the file is", joinCodes(parts))
		case bestScore < seriesConfident:
			row.Diagnosis = "no TMDB episode title of this series matches the file's title as written: the file may be another series', named by hand, or titled otherwise than TMDB titles it"
		case row.Season != nil && row.Episode != nil && best.Season == *row.Season && best.Episode == *row.Episode:
			row.TMDBEpisode = fmt.Sprintf("S%02dE%02d", best.Season, best.Episode)
			row.Diagnosis = "TMDB gives the file's title to this very episode: the server's title is a reworded one, not a different episode"
		default:
			row.TMDBEpisode = fmt.Sprintf("S%02dE%02d", best.Season, best.Episode)
			row.Diagnosis = fmt.Sprintf("the file's title is TMDB's %s, not S%sE%s: the file is numbered in another order (a download numbered from TVDB, most often), and by its title it holds that episode", row.TMDBEpisode, numberText(row.Season), numberText(row.Episode))
		}
	}

	return nil
}

// joinCodes spells episodes as SxxEyy, the last joined by "and".
func joinCodes(eps []tmdb.Episode) string {
	codes := make([]string, 0, len(eps))
	for _, e := range eps {
		codes = append(codes, fmt.Sprintf("S%02dE%02d", e.Season, e.Episode))
	}
	if len(codes) < 2 {
		return strings.Join(codes, "")
	}

	return strings.Join(codes[:len(codes)-1], ", ") + " and " + codes[len(codes)-1]
}

// episodeTitles is a series' TMDB episodes by their titles, each read once
// (naming.FormOf), for a sweep's file titles to be looked up in with the very rule
// naming.SameTitle judges titles by. Every file title scored against every episode
// title was minutes of work for one show of thousands of episodes named by
// date, and scored raw it missed titles TMDB spells otherwise ("Part I" for
// "(1)", "12" for "Twelve", "Gray" for "Grey"). A title alike to another
// (see alike) shares its words spelled alike, or its first or second word
// (a score near enough to act on shares the first; a spelling one word
// apart shares one of the two), or its words run together: only episodes
// sharing one are compared.
type episodeTitles struct {
	eps    []tmdb.Episode
	forms  []naming.Form
	exact  map[string]int   // words spelled alike -> the first episode of them
	joined map[string][]int // an unnumbered title's words run together -> episodes
	byWord map[string][]int // first and second word -> episodes, in order
	// byStory is the episodes whose title is a story and a closing part
	// number, by the story: "The Search (1)" and "(2)" under "search"
	byStory map[string][]int
}

// indexEpisodes reads a series' episodes into an episodeTitles.
func indexEpisodes(eps []tmdb.Episode) *episodeTitles {
	x := &episodeTitles{eps: eps, forms: make([]naming.Form, len(eps)), exact: map[string]int{}, joined: map[string][]int{}, byWord: map[string][]int{}, byStory: map[string][]int{}}
	for i := range eps {
		f := naming.FormOf(eps[i].Name)
		x.forms[i] = f
		if _, seen := x.exact[f.Canon]; !seen && f.Canon != "" {
			x.exact[f.Canon] = i
		}
		if !f.Numbered && len(f.Words) > 0 {
			key := strings.Join(f.Words, "")
			x.joined[key] = append(x.joined[key], i)
		}
		for _, w := range leadWords(f.Words) {
			x.byWord[w] = append(x.byWord[w], i)
		}
		if n := len(f.Words); n > 1 && naming.IsNumber(f.Words[n-1]) {
			story := strings.Join(f.Words[:n-1], " ")
			x.byStory[story] = append(x.byStory[story], i)
		}
	}

	return x
}

// leadWords are a title's first and second words, as naming.Words spells them.
func leadWords(words []string) []string {
	return words[:min(len(words), 2)]
}

// best is the episode whose title is the file's (see alike), and how close
// (naming.Score over the words spelled alike, and at least seriesConfident for
// a title written another way): of two alike, the first. A score below
// seriesConfident says only that none is alike. parts, when set, are the
// episodes TMDB gives a file's bare story to only with a part number: two
// or more, or one that is not part 1 - which of them the file is, a title
// cannot say.
func (x *episodeTitles) best(title string) (episode tmdb.Episode, score float64, parts []tmdb.Episode) {
	if x == nil {
		return tmdb.Episode{}, 0, nil
	}
	q := naming.FormOf(title)
	if i, ok := x.exact[q.Canon]; ok && naming.Alike(q, x.forms[i]) {
		return x.eps[i], 1, nil
	}
	if !q.Numbered {
		if stories := x.byStory[q.Canon]; len(stories) > 1 || len(stories) == 1 && x.forms[stories[0]].Numbers[len(x.forms[stories[0]].Numbers)-1] != "1" {
			for _, i := range stories {
				parts = append(parts, x.eps[i])
			}

			return tmdb.Episode{}, 0, parts
		}
	}
	var candidates []int
	for _, w := range leadWords(q.Words) {
		candidates = append(candidates, x.byWord[w]...)
	}
	if !q.Numbered {
		candidates = append(candidates, x.joined[strings.Join(q.Words, "")]...)
	}
	slices.Sort(candidates)
	for _, i := range slices.Compact(candidates) {
		if !naming.Alike(q, x.forms[i]) {
			continue
		}
		s, _ := naming.FoldedScore(q.Canon, x.forms[i].Canon)
		s = max(s, seriesConfident)
		if s > score {
			episode, score = x.eps[i], s
		}
	}

	return episode, score, nil
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
// say more about - and what the path claims, to search by. A show's own row
// (rollUpShows) is its episode files' finding, and its path a folder they
// share, not a title to search by.
func pathDisputed(row *pathRow) (naming.Claim, bool) {
	if row.Episodes > 0 || tmdbKind(row.Type) == "" || !slices.ContainsFunc(row.Problems, func(p string) bool {
		return strings.HasPrefix(p, "title:") || strings.HasPrefix(p, "year:")
	}) {
		return naming.Claim{}, false
	}
	claim, ok := naming.ClaimOf(row.Path, heldTitles(&row.item))
	if !ok || claim.Title == "" {
		return naming.Claim{}, false
	}
	if row.read != "" {
		claim.Title = row.read
	}

	return claim, true
}

// clearAlternativeTitle drops a film's or a series' title problem when the
// path's title is one TMDB lists for the item's id: the name it went by in
// another country, or in another language.
func clearAlternativeTitle(ctx context.Context, titles *tmdb.Facts, row *pathRow) error {
	// a part 1 whose folder is dated a year off: an alternative title that
	// is the plain title ("Dune" of "Dune: Part One") names the film or its
	// series alike, and settles nothing (diagnoseRow asks which part)
	if !namesAnotherTitle(row) || row.partOneYearOff {
		return nil
	}
	id := providerID(&row.item, "tmdb")
	if id == "" {
		return nil
	}
	alts, err := titles.AlternativeTitles(ctx, tmdbKind(row.Type), id)
	if err != nil {
		return err
	}
	// the path's title as TMDB is asked by it: read whole when words follow
	// the year and the words before it are none the item goes by, and the
	// words before it too, as a title TMDB lists ("Mononoke-hime (1997)
	// Remastered") is one the item goes by whatever edition follows
	terms := naming.SearchTerms(row.Path, heldTitles(&row.item), row.read)
	for _, alt := range alts {
		if !slices.ContainsFunc(terms, func(term string) bool { return termLike(slices.Index(terms, term), term, alt) }) {
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
func nameKnown(ctx context.Context, titles *tmdb.Facts, row *pathRow, id string, hit *tmdb.Hit) (bool, error) {
	kind := tmdbKind(row.Type)
	known, err := titles.AlternativeTitles(ctx, kind, id)
	if err != nil {
		return false, err
	}
	if hit != nil {
		known = append(slices.Clone(known), hit.Title, hit.Original)
	}
	held := heldTitles(&row.item)
	for _, h := range held {
		for _, k := range known {
			if k != "" && naming.SameTitle(h, k) {
				return true, nil
			}
		}
	}
	hits, err := titles.Search(ctx, kind, row.item.Name, row.item.ProductionYear)
	if err != nil {
		return false, err
	}

	return slices.ContainsFunc(hits, func(h tmdb.Hit) bool { return strconv.Itoa(h.ID) == id }), nil
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

// tmdbNote counts the rows whose diagnosis says TMDB could not be asked, ""
// when there are none: shown are the rows listed, past those kept past the
// limit. A failure is the row's, not the sweep's: one "connection reset by
// peer" on one film's titles used to switch TMDB off for every row after it,
// and on a library of twenty thousand films two hundred rows were false
// findings for it. Each read is tried again first, and TMDB is taken to be
// down only by the breaker every read of the sweep goes through
// (tmdb.Breaker), which counts reads TMDB failed and nothing else; the rows
// after that are counted apart, as not put to it.
func tmdbNote(shown, past []pathRow) string {
	var failed, skipped, hidden int
	var last error
	all := slices.Concat(shown, past)
	for i := range all {
		err := all[i].tmdbErr
		if err == nil {
			continue
		}
		if i >= len(shown) {
			hidden++
		}
		if _, down := errors.AsType[*tmdb.DownError](err); down {
			skipped++

			continue
		}
		failed++
		last = err
	}
	var parts []string
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("TMDB could not be asked about %d rows (the last: %v)", failed, last))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d rows were not put to TMDB, taken to be down after reads failing one after another", skipped))
	}
	if len(parts) == 0 {
		return ""
	}
	note := strings.Join(parts, "; ")
	if hidden > 0 {
		note += fmt.Sprintf(" (%d of them past the limit, not listed)", hidden)
	}

	return note + ": each says so in its diagnosis, and may be no finding at all, since the path's title may be one TMDB lists for the item"
}

// couldNotAsk marks a row whose TMDB reads failed: what could not be asked,
// and why, on the row itself. A read not made because TMDB was taken to be
// down does not take the place of a failure the row already says: that one
// is the row's own.
func couldNotAsk(row *pathRow, what string, err error) {
	if _, down := errors.AsType[*tmdb.DownError](err); down && row.tmdbErr != nil {
		return
	}
	row.tmdbErr = err
	row.Diagnosis = fmt.Sprintf("TMDB could not be asked %s (%v), so this is judged by the server's titles alone and may be no finding", what, err)
}

// diagnoseTitlesByTMDB asks TMDB's search what a film's or a series' path
// names, for the rows whose title is none the item goes by or whose year is
// two or more from its own, in the order they are reported until limit rows
// are kept. The search finding the item's own id means the path names it by
// a title and a year TMDB knows it by, and the row goes; finding another says
// which film the path names, beside the id the item carries, with the file's
// runtime against both; finding nothing says so. A row TMDB could not be
// asked about stays as it was, saying so.
func diagnoseTitlesByTMDB(ctx context.Context, facts *tmdb.Facts, rows []pathRow, limit int) []pathRow {
	kept := make([]pathRow, 0, len(rows))
	for i := range rows {
		row := rows[i]
		claim, disputed := pathDisputed(&row)
		// a row TMDB has said what its path names already (a title read
		// whole, wholeFilm) has nothing to add
		if len(kept) >= limit || !disputed || row.named || row.PathTMDB != "" {
			kept = append(kept, row)

			continue
		}
		// judged on a copy, so a read failing part way leaves the row as the
		// server alone made it
		trial := row
		drop, err := diagnoseRow(ctx, facts, &trial, claim)
		if err != nil {
			couldNotAsk(&row, "what the path's title and year name", err)
			kept = append(kept, row)

			continue
		}
		if drop {
			continue
		}
		// the search answered where the read of the facts TMDB lists for
		// the item had failed: a title it settled needs them no more, and
		// what it says of one it did not stands beside what it cannot rule
		// out
		if row.tmdbErr != nil {
			switch {
			case !slices.ContainsFunc(trial.Problems, func(p string) bool { return strings.HasPrefix(p, "title:") }):
				if trial.Diagnosis == row.Diagnosis {
					trial.Diagnosis = ""
				}
				trial.tmdbErr = nil
			case trial.Diagnosis != row.Diagnosis:
				trial.Diagnosis += fmt.Sprintf("; but TMDB could not be asked which facts it lists for the item (%v), and the path's may be one of them", row.tmdbErr)
			}
		}
		kept = append(kept, trial)
	}

	return kept
}

// diagnoseRow is diagnoseTitlesByTMDB's answer for one row, and whether the
// row goes: the path names the item after all. TMDB is asked as the version
// and duplicate warnings ask it (searchPath), so the two never tell one file
// two ways: by the path's title read whole first when words follow its year
// and the words before it are none the item goes by, then by the title read.
func diagnoseRow(ctx context.Context, facts *tmdb.Facts, row *pathRow, claim naming.Claim) (drop bool, err error) {
	kind := tmdbKind(row.Type)
	own := providerID(&row.item, "tmdb")
	found, err := searchPath(ctx, facts, kind, own, claim.Terms(heldTitles(&row.item)), claim.Year)
	if err != nil {
		return false, err
	}
	term := found.term
	what := "film"
	if kind == "tv" {
		what = "series"
	}
	hit := found.another
	switch {
	case row.partOneYearOff:
		// a folder naming the film but for its part 1, dated a year off: the
		// search finding the item is no answer, since the second part is
		// filed the same way. The other part the folder's year names is, and
		// the file's runtime says which of the two the file is
		sibling, ok := siblingPart(found.hits, claim, own)
		if !ok {
			row.Diagnosis = fmt.Sprintf("TMDB's search finds no other part of it dated %d: can't tell whether the file is part 1 or another part filed under it", claim.Year)

			return false, nil
		}
		hit = &sibling
	case found.named:
		// the search finding the item's own id by the path's title and year,
		// under a title like the path's - numbered as the path is: a search
		// by a first part's title finds the second part a year on too - or
		// one TMDB lists for it or translates it by, is TMDB saying the path
		// names this film: the title is one it goes by, and a year that
		// still disagrees is the item's to check, not the path's. Unless the
		// name the item holds is none TMDB gives that film: then the path is
		// right and the name is what is wrong (a film renamed by hand to
		// another film's title read as clean)
		if namesAnotherTitle(row) {
			known, err := nameKnown(ctx, facts, row, own, found.own)
			if err != nil {
				return false, err
			}
			if !known {
				nameWrong(row, own, fmt.Sprintf("TMDB's search finds this very %s, TMDB %s %s (%d), by the path's title and year", what, own, found.own.Title, found.own.Year))

				return false, nil
			}
		}
		dropTitle(row, fmt.Sprintf("a title TMDB's search finds it by: %q (%d) is its TMDB %s", term, claim.Year, own))
		if len(row.Problems) == 0 {
			return true, nil
		}
		if slices.ContainsFunc(row.Problems, func(p string) bool { return strings.HasPrefix(p, "year:") }) {
			row.ItemTMDB = own
			row.Diagnosis = fmt.Sprintf("TMDB's search finds this very %s, TMDB %s, by the path's title and year: the path names it, and the year the item holds is the one to check", what, own)
		}

		return false, nil
	}
	if hit == nil {
		// what the search did find - never "no film" when it found one: the
		// item's own film under another title, numbered otherwise than the
		// path or not, or others
		inYear := func(h tmdb.Hit) bool { return claim.Year == 0 || h.Year == 0 || abs(h.Year-claim.Year) <= 1 }
		switch {
		case found.own != nil && !numberedAs(term, *found.own):
			h := found.own
			row.ItemTMDB = own
			row.Diagnosis = fmt.Sprintf("TMDB's search finds this very %s, TMDB %s %s (%d), %s: another part of it, or the wrong match", what, own, h.Title, h.Year, numberedOtherwise(term, h.Title))
		case found.own != nil:
			row.ItemTMDB = own
			row.Diagnosis = fmt.Sprintf("TMDB's search by the path's title and year, %q (%d), answers with this very %s, TMDB %s %s (%d), but under no title like the path's: its search matches loosely, so the path may name another %s, or this one by a title TMDB does not list - check the file", term, claim.Year, what, own, found.own.Title, found.own.Year, what)
		default:
			if at := slices.IndexFunc(found.hits, func(h tmdb.Hit) bool { return inYear(h) && !numberedAs(term, h) }); at >= 0 {
				h := found.hits[at]
				row.Diagnosis = fmt.Sprintf("TMDB's search by the path's title and year finds only %ss numbered otherwise than the path, the nearest TMDB %d %s (%d), %s: another part of a series, or named by hand", what, h.ID, h.Title, h.Year, numberedOtherwise(term, h.Title))

				return false, nil
			}
			if at := slices.IndexFunc(found.hits, inYear); at >= 0 {
				h := found.hits[at]
				row.Diagnosis = fmt.Sprintf("TMDB's search by the path's title and year finds only %ss titled otherwise than the path, the first TMDB %d %s (%d): named by hand, or for a %s TMDB does not list", what, h.ID, h.Title, h.Year, what)

				return false, nil
			}
			row.Diagnosis = fmt.Sprintf("TMDB's search finds no %s by the path's title and year: named by hand, or for a %s TMDB does not list", what, what)
			if kind == "movie" && row.item.RunTimeTicks <= 0 {
				row.Diagnosis += "; the server holds no media facts for the file (never probed), so its runtime cannot say which film it is"
			}
		}

		return false, nil
	}

	row.PathTMDB, row.ItemTMDB = fmt.Sprintf("%d %s (%d)", hit.ID, hit.Title, hit.Year), own
	if kind == "movie" && !row.partOneYearOff && editionEntry(&row.item, *hit) {
		// an entry of its own titled the film's and an edition's words: an
		// edition TMDB lists apart, as the title check reads it
		row.Diagnosis = fmt.Sprintf("the path names TMDB's %d, %s (%d), which TMDB lists as an entry of its own titled the film's and an edition's words: an edition of this film TMDB keeps apart, or another film matched to its ids - check the file", hit.ID, hit.Title, hit.Year)
		// the title problem allows the edition too; one saying words were
		// added already names an edition
		for i, p := range row.Problems {
			if rest, ok := strings.CutSuffix(p, ": the wrong match, or another film"); ok && strings.HasPrefix(p, "title:") {
				row.Problems[i] = rest + ": an edition TMDB lists apart, the wrong match, or another film"
			}
		}

		return false, nil
	}
	carries := "the item carries no TMDB id"
	if own != "" {
		carries = "the item carries TMDB " + own
	}
	if kind != "movie" {
		row.Diagnosis = fmt.Sprintf("the path names TMDB's %s %d, %s (%d); %s: it is matched to another %s than the one on disk", what, hit.ID, hit.Title, hit.Year, carries, what)

		return false, nil
	}
	evidence, backs := runtimeEvidence(ctx, facts, &row.item, strconv.Itoa(hit.ID), own)
	// a file that runs as the item's film sits in a folder naming another,
	// whatever that other film is called: the folder is likely what to put
	// right. Likely, not surely: an extended or TV cut of the other film can
	// run within a tenth of the item's. The row stays, to say so
	if own != "" && backs == backsItem {
		another := ""
		if homonym(row, *hit) {
			another = ", another film of the item's name"
		}
		row.Diagnosis = fmt.Sprintf("the path names TMDB's film %d, %s (%d)%s; the file's runtime points to the item's TMDB %s: the folder is likely what is wrong", hit.ID, hit.Title, hit.Year, another, own) + evidence

		return false, nil
	}
	// a film of the item's own name - a remake, an older or newer film
	// titled alike - is what a search by the name and another year finds,
	// and a name alone cannot say which the file is: its runtime can, and
	// with none to go by the row says it can't tell
	if own != "" && backs != backsPath && homonym(row, *hit) {
		row.Diagnosis = fmt.Sprintf("the path names TMDB's film %d, %s (%d), a film of the same name as the item's TMDB %s: can't tell which the file is", hit.ID, hit.Title, hit.Year, own) + evidence

		return false, nil
	}
	row.Diagnosis = fmt.Sprintf("the path names TMDB's %s %d, %s (%d); %s: it is matched to another %s than the one on disk", what, hit.ID, hit.Title, hit.Year, carries, what) + evidence

	return false, nil
}

// siblingPart is the search hit that is another part of what the path names
// - its title the path's with a part number other than 1 after it - dated
// the path's own year, which is not the item.
func siblingPart(hits []tmdb.Hit, claim naming.Claim, own string) (tmdb.Hit, bool) {
	story := naming.FormOf(claim.Title).Words
	for _, h := range hits {
		if h.Year != claim.Year || strconv.Itoa(h.ID) == own {
			continue
		}
		for _, t := range []string{h.Title, h.Original} {
			words := naming.FormOf(t).Words
			if n := len(words); n > 1 && naming.IsNumber(words[n-1]) && words[n-1] != "1" && slices.Equal(naming.WithoutPartOne(words), story) {
				return h, true
			}
		}
	}

	return tmdb.Hit{}, false
}

// numberedOtherwise says how a title TMDB found is numbered against the
// path's: "numbered 2, which the path is not", or "numbered 2, where the
// path says 1".
func numberedOtherwise(path, found string) string {
	of := func(numbers []string) string { return strings.Join(numbers, " ") }
	p, f := naming.Numbering(path), naming.Numbering(found)
	switch {
	case len(f) == 0:
		return "not numbered, where the path says " + of(p)
	case len(p) == 0:
		return fmt.Sprintf("numbered %s, which the path is not", of(f))
	}

	return fmt.Sprintf("numbered %s, where the path says %s", of(f), of(p))
}

// homonym says whether the film a path's search found goes by exactly a
// title the item goes by, once case and punctuation are folded: two films of
// one name, which only the file can tell apart. Only exactly: a sequel's
// title is its first film's and a number ("Zzyzx" and "Zzyzx 2"), and scored
// alike it read as the same name.
func homonym(row *pathRow, hit tmdb.Hit) bool {
	return homonymOf(&row.item, hit)
}

// homonymOf is homonym for an item.
func homonymOf(it *embyfin.Item, hit tmdb.Hit) bool {
	for _, held := range heldTitles(it) {
		for _, t := range []string{hit.Title, hit.Original} {
			if t != "" && naming.Normalise(held) == naming.Normalise(t) {
				return true
			}
		}
	}

	return false
}

// Which film a file's runtime backs, of the path's and the item's.
const (
	backsPath    = "path"
	backsItem    = "item"
	backsNeither = "neither"
)

// runtimeEvidence is what the file's runtime says about which of two films it
// is, as a clause to end a diagnosis with - the runtime TMDB gives each, and
// which the file runs closer to, or that the server never probed the file -
// and which it backs: backsPath, backsItem, backsNeither, or "" when it
// cannot say (no runtime to compare, or TMDB could not be asked for one).
func runtimeEvidence(ctx context.Context, facts *tmdb.Facts, it *embyfin.Item, pathID, itemID string) (clause, backs string) {
	if it.RunTimeTicks <= 0 {
		return "; the server holds no media facts for the file (never probed), so its runtime backs neither film", ""
	}
	if facts == nil {
		return "", ""
	}
	file := it.RuntimeMinutes()
	path, err := facts.MovieRuntime(ctx, pathID)
	switch {
	case err != nil:
		return fmt.Sprintf("; the file runs %d min, and TMDB could not be asked how long its %s runs (%v), so the runtime backs neither film here", file, pathID, err), ""
	case path <= 0:
		return fmt.Sprintf("; the file runs %d min, and TMDB gives no runtime for its %s, so the runtime backs neither film here", file, pathID), ""
	}
	out := fmt.Sprintf("; the file runs %d min, and TMDB's %s runs %d", file, pathID, path)
	if itemID == "" {
		return out, ""
	}
	item, err := facts.MovieRuntime(ctx, itemID)
	switch {
	case err != nil:
		return fmt.Sprintf("%s; TMDB could not be asked how long its %s runs (%v)", out, itemID, err), ""
	case item <= 0:
		return fmt.Sprintf("%s; TMDB gives no runtime for its %s", out, itemID), ""
	}
	out += fmt.Sprintf(" and its %s %d", itemID, item)
	// a runtime backs a film when it is that film's, as audit_provider
	// judges one; a truncated file is neither's, and says nothing
	_, offPath := runtimeOff(file, path, runtimeBacksPct)
	_, offItem := runtimeOff(file, item, runtimeBacksPct)
	switch {
	case !offPath && offItem:
		return out + ": the file's runtime is the path's film's", backsPath
	case offPath && !offItem:
		return out + ": the file's runtime is the item's film's", backsItem
	case offPath && offItem:
		return out + ": the file's runtime is neither's", backsNeither
	}

	return out, ""
}

// runtimeBacksPct is how close, in percent, a file's runtime has to be to a
// film's for it to back that film: closer than a cut of the same film is
// usually apart.
const runtimeBacksPct = 10

// tmdbNotAsked begins the reason a row's series was never put to TMDB, as
// opposed to TMDB failing to answer.
const tmdbNotAsked = "TMDB was not asked"

// tmdbGuide is TMDB's episodes for a library series, specials included, or
// why there are none to compare with: the series carries no TMDB id, or TMDB
// could not be asked. The error is the media server's, reading the series.
func tmdbGuide(ctx context.Context, client *embyfin.Client, provider *tmdb.Facts, seriesID string) (eps []tmdb.Episode, why string, tmdbErr, err error) {
	if seriesID == "" {
		return nil, tmdbNotAsked + ": the server holds the episode under no series", nil, nil
	}
	series, err := client.ItemByID(ctx, seriesID)
	if err != nil {
		return nil, "", nil, err
	}
	id := providerID(series, "tmdb")
	if id == "" {
		return nil, tmdbNotAsked + ": the series carries no TMDB id", nil, nil
	}
	// TMDB failing is said on the row and counted in the note, not a failed
	// audit: the error returned as err is the media server's
	run, tmdbErr := provider.SeriesEpisodes(ctx, id)
	if tmdbErr != nil {
		return nil, "TMDB could not be asked for the series' episodes: " + tmdbErr.Error(), tmdbErr, nil
	}
	// a file named after a special is TMDB's season 0, which the run leaves
	// out: without it every such file read as a title TMDB had never heard of
	specials, tmdbErr := provider.SeriesSpecials(ctx, id)
	if tmdbErr != nil {
		return nil, "TMDB could not be asked for the series' specials: " + tmdbErr.Error(), tmdbErr, nil
	}

	return append(slices.Clone(run), specials...), "", nil, nil
}
