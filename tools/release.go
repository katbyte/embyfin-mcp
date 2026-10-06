package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/naming"
	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// scoreSeries scores a library series against a parsed release, weighing the
// year when both sides know one.
func scoreSeries(rel naming.Release, it *embyfin.Item) (score float64, matchedOn string) {
	score, how := naming.Score(rel.Title, it.Name)
	if it.OriginalTitle != "" {
		if s, h := naming.Score(rel.Title, it.OriginalTitle); s > score {
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

// runWarning says when a server does not read a name's run of episodes as a
// run, or when whether it does has not been checked: a file so named is
// listed as its first episode alone, and the rest of the run as missing. ""
// for a name with no run, or one the server reads.
func runWarning(rel naming.Release, backend embyfin.Backend) string {
	style := rel.RunStyle()
	if style == "" {
		return ""
	}
	server := backendName(backend)
	read, known := naming.RunStyleRead(style, string(backend))
	switch {
	case !known:
		return fmt.Sprintf("whether %s reads %q (%s) as a run of episodes has not been checked: a file so named may be listed as E%02d alone, with E%02d%s read as missing", server, rel.Run, style, rel.Episode, rel.Episode+1, runRest(rel))
	case !read:
		return fmt.Sprintf("%s does not read %q (%s) as a run of episodes: a file so named is listed as E%02d alone, and E%02d%s read as missing; name it S%02dE%02d-E%02d", server, rel.Run, style, rel.Episode, rel.Episode+1, runRest(rel), rel.Season, rel.Episode, rel.EpisodeEnd)
	}

	return ""
}

// runRest is what follows the second episode of a run in a message: "" for a
// run of two, " to E05" for a longer one.
func runRest(rel naming.Release) string {
	if rel.EpisodeEnd <= rel.Episode+1 {
		return ""
	}

	return fmt.Sprintf(" to E%02d", rel.EpisodeEnd)
}

// backendName is a server's name as a message writes it.
func backendName(backend embyfin.Backend) string {
	if backend == embyfin.Jellyfin {
		return "Jellyfin"
	}

	return "Emby"
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)

	return n
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
	// "S W A T" is how a release name spells the library's "S.W.A.T.", and
	// the servers' search finds neither from the other: it folds the points
	// but not the spaces. Asking for the closed-up spelling as well is what
	// makes the series findable at all.
	if closed := naming.Normalise(title); closed != "" && !strings.EqualFold(closed, title) {
		terms = append(terms, closed)
	}
	words := strings.Fields(title)
	for _, n := range []int{3, 2, 1} {
		if len(words) > n {
			terms = append(terms, strings.Join(words[:n], " "))
		}
	}

	return terms
}

// seriesCandidate is one series the library holds that a name could mean,
// and how sure we are of it.
type seriesCandidate struct {
	SeriesID  string  `json:"series_id"`
	Name      string  `json:"name"`
	Year      int     `json:"year,omitempty"`
	Score     float64 `json:"score"          jsonschema:"0 to 1. 1 is the same title once case, punctuation, accents and the ampersand are folded; below about 0.9 is a guess a caller should not act on unattended"`
	MatchedOn string  `json:"matched_on"     jsonschema:"what made the match: the title, the title without its article, an acronym spelled out, a prefix of it, or words in common"`
	Path      string  `json:"path,omitempty"`

	RunnerUp     float64 `json:"runner_up_score,omitempty" jsonschema:"what the next best candidate scored, when there was one. A high score with a high runner-up is a near-tie, not a certainty"`
	RunnerUpName string  `json:"runner_up,omitempty"`
}

// rankSeries asks the server's search for a parsed name and scores what comes
// back, for the names the index cannot place (see matchSeries),
// best first. It also hands back every series it saw, scored or not, because
// a caller that has to choose one wants to know whether the search found a
// single thing or a hundred.
func rankSeries(ctx context.Context, client *embyfin.Client, rel naming.Release, parent string) ([]seriesCandidate, []embyfin.Item, error) {
	// ask for the title, then for shorter heads of it until something scores
	// well enough to stop looking
	best := 0.0
	seen := []embyfin.Item{}
	scored := map[string]seriesCandidate{}
	for _, term := range resolveSearchTerms(rel.Title) {
		items, _, err := client.Search(ctx, embyfin.SearchOptions{
			SearchTerm: term, IncludeItemTypes: "Series", ParentID: parent, Limit: 50,
			// the ids too, as the index reads them: a series resolved through
			// this search is the one asked whether the library holds it under
			// another entry, and without its ids that question answers "no"
			Fields: "Path,ProductionYear,OriginalTitle,ProviderIds",
		})
		if err != nil {
			return nil, nil, err
		}
		for i := range items {
			it := &items[i]
			if slices.ContainsFunc(seen, func(s embyfin.Item) bool { return s.ID == it.ID }) {
				continue
			}
			seen = append(seen, *it)
			score, how := scoreSeries(rel, it)
			if score <= 0 {
				continue
			}
			scored[it.ID] = seriesCandidate{SeriesID: it.ID, Name: it.Name, Year: it.ProductionYear, Score: score, MatchedOn: how, Path: it.Path}
			best = math.Max(best, score)
		}
		if best >= 0.95 {
			break
		}
	}

	rows := make([]seriesCandidate, 0, len(scored))
	for _, row := range scored {
		rows = append(rows, row)
	}
	sortCandidates(rows)

	return rows, seen, nil
}

func registerResolveTools(r *registry) {
	client := r.client

	type resolveIn struct {
		Title   string `json:"title"             jsonschema:"a release name or a plain title, e.g. 24.Hours.in.A.and.E.S36E03.1080p.HDTV.H264-DARKFLiX or 1600 Penn"`
		Year    int    `json:"year,omitempty"    jsonschema:"the year to prefer, when the caller knows one the name does not carry"`
		Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum candidates to return, default 5"`
	}
	type resolveOut struct {
		Title      string            `json:"parsed_title"                 jsonschema:"the title read out of the name, with the season, encode and group taken off"`
		Year       int               `json:"parsed_year,omitempty"`
		Season     *int              `json:"parsed_season,omitempty"      jsonschema:"the season the name carried, when it carried one: 0 is the specials (S00), and absent is a name with no season in it"`
		Episode    *int              `json:"parsed_episode,omitempty"     jsonschema:"the episode the name carried, when it carried one: 0 included (E00)"`
		EpisodeEnd int               `json:"parsed_episode_end,omitempty" jsonschema:"set when the name covers several episodes (S01E01E02)"`
		RunStyle   string            `json:"run_style,omitempty"          jsonschema:"how the name spells its run of episodes, its numbers as NN: SNNENN-ENN for S01E01-E02, NNxNN+NN for 02x47+48"`
		RunWarning string            `json:"run_warning,omitempty"        jsonschema:"set when the server does not read the name's run style as a run of episodes - a file so named is listed as its first episode alone, and the rest read as missing - or when whether it does has not been checked"`
		Candidates []seriesCandidate `json:"candidates"                   jsonschema:"the library's series that could be it, best first"`
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

		rel := naming.ParseRelease(in.Title)
		if in.Year > 0 {
			rel.Year = in.Year
		}
		if rel.Unread || naming.Normalise(rel.Title) == "" {
			return nil, resolveOut{}, fmt.Errorf("no title could be read out of %q: it is all season, encode and group", in.Title)
		}
		out := resolveOut{Title: rel.Title, Year: rel.Year, EpisodeEnd: rel.EpisodeEnd, RunStyle: rel.RunStyle(), RunWarning: runWarning(rel, client.Backend()), Candidates: []seriesCandidate{}}
		if rel.HasSeason {
			out.Season = new(rel.Season)
		}
		if rel.HasEpisode {
			out.Episode = new(rel.Episode)
		}

		folder, err := client.ResolveLibrary(ctx, in.Library)
		if err != nil {
			return nil, resolveOut{}, err
		}
		parent := ""
		if folder != nil {
			parent = folder.ItemID
		}

		rows, _, err := r.matchSeries(ctx, rel, parent)
		if err != nil {
			return nil, resolveOut{}, err
		}
		out.Candidates = append(out.Candidates, rows[:min(len(rows), limit)]...)

		return nil, out, nil
	})
}
