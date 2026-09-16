package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Turning a release name into a series the library holds.
//
// Files arrive named the way the scene names them - dots for spaces, the
// season and episode in the middle, and everything about the encode after
// it - and every client that has to match those against a library writes its
// own parser for them, badly. The library is the only side that knows both
// what the show is really called and what it is called elsewhere, so the
// matching belongs here.

// releaseMarkers are the shapes that end a title and begin the rest of a
// release name, most specific first: the episode, then a season on its own.
var releaseMarkers = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(^|[^a-z0-9])s(\d{1,2})[. _-]?e(\d{1,3})(?:[. _-]?e(\d{1,3}))?([^a-z0-9]|$)`),
	regexp.MustCompile(`(?i)(^|[^a-z0-9])(\d{1,2})x(\d{2})([^a-z0-9]|$)`),
	regexp.MustCompile(`(?i)(^|[^a-z0-9])s(\d{1,2})([^a-z0-9]|$)`),
	regexp.MustCompile(`(?i)(^|[^a-z0-9])season[. _-]+(\d{1,2})([^a-z0-9]|$)`),
}

// releaseJunk are the words that only ever appear after a title: what the
// file was made from and with. A title that ends at one of these had no
// season marker to end at.
var releaseJunk = map[string]bool{}

func init() {
	for _, w := range []string{
		// resolution and shape
		"480p", "540p", "576p", "720p", "1080p", "1080i", "2160p", "4k", "uhd", "hd", "sd",
		// where it came from
		"hdtv", "pdtv", "web", "webrip", "web-dl", "webdl", "bluray", "blu-ray", "brrip", "bdrip",
		"dvdrip", "dvd", "hdrip", "remux", "amzn", "nf", "dsnp", "atvp", "hmax", "max", "cr",
		"crunchyroll", "itunes", "pcok", "stan", "ip", "all4", "uktv",
		// what it was encoded with
		"x264", "x265", "h264", "h265", "h", "hevc", "avc", "xvid", "divx", "av1", "vp9",
		"10bit", "8bit", "hi10p", "hdr", "hdr10", "dv", "sdr",
		// sound
		"aac", "aac2", "ac3", "eac3", "ddp", "ddp5", "dd5", "dts", "dts-hd", "truehd", "atmos", "flac", "opus", "mp3",
		// the rest
		"proper", "repack", "internal", "limited", "extended", "uncut", "complete", "multi",
		"dual", "dual-audio", "subbed", "dubbed", "subs", "batch",
	} {
		releaseJunk[w] = true
	}
}

// release is what a release name says: the series it is for, and where in
// the run it sits when it says so.
type release struct {
	Title      string
	Year       int
	Season     int
	Episode    int
	EpisodeEnd int
}

var (
	fileExtension = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|m4v|ts|mov|wmv|mpg|mpeg|flv|webm|iso|nfo)$`)
	bracketedYear = regexp.MustCompile(`[(\[]((?:19|20)\d{2})[)\]]`)
	bareYear      = regexp.MustCompile(`(^|[^a-z0-9])((?:19|20)\d{2})([^a-z0-9]|$)`)
	spaceRun      = regexp.MustCompile(`\s+`)
)

// parseRelease reads a release name: the title, and the year, season and
// episode when the name carries them. A name that is already a plain title
// comes back as itself.
//
// A path is read segment by segment, because the parts of the answer are
// spread across them: "Severance (2022)/Season 01/Severance - S01E01 - ...".
// The title and year come from the last segment that names a show, and the
// season and episode from the last that numbers one, so a file named only
// "S01E01.mkv" still takes its title from the folder above it.
func parseRelease(name string) release {
	var out release
	for _, segment := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		seg := parseSegment(segment)
		if seg.Title != "" {
			out.Title = seg.Title
		}
		if seg.Season > 0 || seg.Episode > 0 {
			out.Season, out.Episode, out.EpisodeEnd = seg.Season, seg.Episode, seg.EpisodeEnd
		}
		if seg.Year > 0 {
			out.Year = seg.Year
		}
	}
	if out.Title == "" {
		// a name that is all marker and encode still has to answer with
		// something a caller can see, rather than with nothing
		out.Title = strings.TrimSpace(spaceRun.ReplaceAllString(strings.NewReplacer(".", " ", "_", " ").Replace(name), " "))
	}
	if out.Title == "" {
		out.Title = strings.TrimSpace(name)
	}

	return out
}

// parseSegment reads one path segment. Its title is empty when the segment
// holds no show name - a "Season 01" folder, or a bare "S01E01.mkv".
func parseSegment(name string) release {
	s := fileExtension.ReplaceAllString(strings.TrimSpace(name), "")

	var out release
	cut := len(s)
	for _, re := range releaseMarkers {
		m := re.FindStringSubmatchIndex(s)
		if m == nil {
			continue
		}
		// the marker starts after whatever separator matched before it
		at := m[2*1+1]
		if at >= cut {
			continue
		}
		cut = at
		groups := re.FindStringSubmatch(s)
		switch len(groups) {
		case 6: // SxxExx, maybe a second episode
			out.Season, out.Episode = atoi(groups[2]), atoi(groups[3])
			out.EpisodeEnd = atoi(groups[4])
		case 5: // 1x02
			out.Season, out.Episode = atoi(groups[2]), atoi(groups[3])
		case 4: // Sxx or Season xx
			out.Season = atoi(groups[2])
		}
	}
	head := s[:cut]

	// the year, when the name gives one: parenthesised anywhere, or a bare
	// 19xx/20xx that is not the whole title (1600 Penn, 2012)
	if m := bracketedYear.FindStringSubmatch(head); m != nil {
		out.Year = atoi(m[1])
		head = strings.Replace(head, m[0], " ", 1)
	} else if m := bareYear.FindStringSubmatchIndex(head); len(m) > 5 && m[4] > 0 {
		out.Year = atoi(head[m[4]:m[5]])
		head = head[:m[4]] + " " + head[m[5]:]
	}

	// dots and underscores stand in for spaces; hyphens do not, because a
	// title can be made of them (9-1-1)
	head = strings.NewReplacer(".", " ", "_", " ").Replace(head)
	head = spaceRun.ReplaceAllString(head, " ")

	// what is left may still run into the encode's words when the name had no
	// season marker at all
	words := strings.Fields(head)
	for i, w := range words {
		if releaseJunk[strings.ToLower(strings.Trim(w, "()[]-"))] {
			words = words[:i]
			break
		}
	}
	out.Title = strings.Trim(strings.Join(words, " "), " -._([{")

	return out
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)

	return n
}

var notAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// normaliseTitle folds a title to what two spellings of the same show have in
// common: case, the ampersand written out, apostrophes, and every other mark
// that a release name and a library differ on.
func normaliseTitle(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "&", " and ")
	s = strings.NewReplacer("'", "", "’", "", "`", "").Replace(s)
	s = notAlphanumeric.ReplaceAllString(s, " ")

	return strings.TrimSpace(spaceRun.ReplaceAllString(s, " "))
}

// withoutThe drops a leading article, which a library and a release name
// disagree about often enough to be worth a second look.
func withoutThe(s string) string {
	for _, article := range []string{"the ", "a ", "an "} {
		if rest, ok := strings.CutPrefix(s, article); ok {
			return rest
		}
	}

	return s
}

// dice is the overlap of two word lists: twice what they share over what
// they hold between them, so a long title and a short one that share
// everything the short one has still score below an exact match.
func dice(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	rest := slices.Clone(b)
	shared := 0
	for _, w := range a {
		if i := slices.Index(rest, w); i >= 0 {
			shared++
			rest = slices.Delete(rest, i, i+1)
		}
	}

	return 2 * float64(shared) / float64(len(a)+len(b))
}

// titleScore is how sure we are that two titles name the same show, and what
// made us think so. 1 is the same title once both are folded.
func titleScore(query, candidate string) (score float64, matchedOn string) {
	q, c := normaliseTitle(query), normaliseTitle(candidate)
	if q == "" || c == "" {
		return 0, ""
	}
	if q == c {
		return 1, "title"
	}
	if withoutThe(q) == withoutThe(c) {
		return 0.97, "title without the article"
	}

	short, long := q, c
	if len(short) > len(long) {
		short, long = long, short
	}
	if strings.HasPrefix(long, short+" ") {
		// "Doctor Who" against "Doctor Who Confidential": right as far as it
		// goes, and wrong often enough not to be certain
		return 0.80 + 0.15*float64(len(short))/float64(len(long)), "title prefix"
	}

	if d := dice(strings.Fields(q), strings.Fields(c)); d > 0 {
		return 0.75 * d, "words in common"
	}

	return 0, ""
}

// scoreSeries scores a library series against a parsed release, weighing the
// year when both sides know one.
func scoreSeries(rel release, it *embyfin.Item) (score float64, matchedOn string) {
	score, how := titleScore(rel.Title, it.Name)
	if it.OriginalTitle != "" {
		if s, h := titleScore(rel.Title, it.OriginalTitle); s > score {
			score, how = s, h+" (original title)"
		}
	}
	if score == 0 {
		return 0, ""
	}

	switch {
	case rel.Year == 0 || it.ProductionYear == 0:
	case rel.Year == it.ProductionYear:
		score, how = math.Min(1, score+0.05), how+" and year"
	case abs(rel.Year-it.ProductionYear) == 1:
		// a show that first ran either side of new year is dated both ways
	default:
		score, how = score*0.75, how+", but a different year"
	}

	return math.Round(score*100) / 100, how
}

func abs(n int) int {
	if n < 0 {
		return -n
	}

	return n
}

// resolveSearchTerms are what to ask the server for a parsed title: the title
// itself, then shorter heads of it, because a library spelling the show
// "24 Hours in A&E" matches no search for "24 Hours in A and E".
func resolveSearchTerms(title string) []string {
	terms := []string{title}
	words := strings.Fields(title)
	for _, n := range []int{3, 2, 1} {
		if len(words) > n {
			terms = append(terms, strings.Join(words[:n], " "))
		}
	}

	return terms
}

func registerResolveTools(r *registry) {
	client := r.client

	type resolveIn struct {
		Title   string `json:"title"             jsonschema:"a release name or a plain title, e.g. 24.Hours.in.A.and.E.S36E03.1080p.HDTV.H264-DARKFLiX or 1600 Penn"`
		Year    int    `json:"year,omitempty"    jsonschema:"the year to prefer, when the caller knows one the name does not carry"`
		Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum candidates to return, default 5"`
	}
	type resolveRow struct {
		SeriesID  string  `json:"series_id"`
		Name      string  `json:"name"`
		Year      int     `json:"year,omitempty"`
		Score     float64 `json:"score"          jsonschema:"0 to 1. 1 is the same title once case, punctuation and the ampersand are folded; below about 0.9 is a guess a caller should not act on unattended"`
		MatchedOn string  `json:"matched_on"     jsonschema:"what made the match: the title, the title without its article, a prefix of it, or words in common"`
		Path      string  `json:"path,omitempty"`
	}
	type resolveOut struct {
		Title      string       `json:"parsed_title"                 jsonschema:"the title read out of the name, with the season, encode and group taken off"`
		Year       int          `json:"parsed_year,omitempty"`
		Season     int          `json:"parsed_season,omitempty"      jsonschema:"the season the name carried, when it carried one"`
		Episode    int          `json:"parsed_episode,omitempty"`
		EpisodeEnd int          `json:"parsed_episode_end,omitempty" jsonschema:"set when the name covers several episodes (S01E01E02)"`
		Candidates []resolveRow `json:"candidates"                   jsonschema:"the library's series that could be it, best first"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "show_resolve",
		Description: "Which series in the library is this release name? Reads the title, year, season and episode out of a scene name (dots for spaces, S03E07, the encode and group after it) and returns the library's series that could be it, best first, each scored. " +
			"Scores below about 0.9 are guesses: a caller should ask rather than act on one. Pass the id it returns to show_episodes_exist or show_missing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in resolveIn) (*mcp.CallToolResult, resolveOut, error) {
		if strings.TrimSpace(in.Title) == "" {
			return nil, resolveOut{}, errors.New("a title is required")
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 5
		}

		rel := parseRelease(in.Title)
		if in.Year > 0 {
			rel.Year = in.Year
		}
		if normaliseTitle(rel.Title) == "" {
			return nil, resolveOut{}, fmt.Errorf("no title could be read out of %q: it is all season, encode and group", in.Title)
		}
		out := resolveOut{Title: rel.Title, Year: rel.Year, Season: rel.Season, Episode: rel.Episode, EpisodeEnd: rel.EpisodeEnd, Candidates: []resolveRow{}}

		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, resolveOut{}, err
		}
		parent := ""
		if folder != nil {
			parent = folder.ItemID
		}

		// ask for the title, then for shorter heads of it until something
		// scores well enough to stop looking
		best := 0.0
		scored := map[string]resolveRow{}
		for _, term := range resolveSearchTerms(rel.Title) {
			items, _, serr := client.Search(ctx, embyfin.SearchOptions{
				SearchTerm: term, IncludeItemTypes: "Series", ParentID: parent, Limit: 50,
				Fields: "Path,ProductionYear,OriginalTitle",
			})
			if serr != nil {
				return nil, resolveOut{}, serr
			}
			for i := range items {
				it := &items[i]
				if _, seen := scored[it.ID]; seen {
					continue
				}
				score, how := scoreSeries(rel, it)
				if score <= 0 {
					continue
				}
				scored[it.ID] = resolveRow{SeriesID: it.ID, Name: it.Name, Year: it.ProductionYear, Score: score, MatchedOn: how, Path: it.Path}
				best = math.Max(best, score)
			}
			if best >= 0.95 {
				break
			}
		}

		rows := make([]resolveRow, 0, len(scored))
		for _, row := range scored {
			rows = append(rows, row)
		}
		slices.SortFunc(rows, func(a, b resolveRow) int {
			if a.Score != b.Score {
				if a.Score > b.Score {
					return -1
				}
				return 1
			}
			return strings.Compare(a.Name, b.Name)
		})
		out.Candidates = append(out.Candidates, rows[:min(len(rows), limit)]...)

		return nil, out, nil
	})
}
