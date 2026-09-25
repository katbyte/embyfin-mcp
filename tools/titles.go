package tools

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
)

// Every title a film or a series goes by, and the letters that only look
// like a title's.
//
// A path names a film by whatever title whoever placed it used: the one on
// the poster where they live, the original one, a sort name. The server
// holds one name, and the provider knows the rest. So a path is only said to
// name another film when its title is none of them - the item's name, its
// original title, its sort name, and, with a TMDB token, every alternative
// title TMDB lists for its id - and when it is not, TMDB is asked what the
// path's title and year do name, which is the evidence a caller acts on.

// How a title is known to an item.
const (
	titleAsName     = "name"
	titleAsOriginal = "original title"
	titleAsSort     = "sort name"
)

// knownTitle is one title an item goes by, and which.
type knownTitle struct {
	title, as string
}

// knownTitles are the titles an item goes by: its name, spelled plainly when
// it carries a letter that only looks Latin; its original title, which a
// folder in the film's own language names; and its sort name. Each comes
// without the year a server leaves on a film it could not match ("Cube
// (1997)"), because a path's title is read cut at its year.
func knownTitles(it *embyfin.Item) []knownTitle {
	_, plain := lookalikeLetters(it.Name)
	var out []knownTitle
	for _, k := range []knownTitle{{plain, titleAsName}, {it.OriginalTitle, titleAsOriginal}, {it.SortName, titleAsSort}} {
		title := strings.TrimSpace(seriesNameYear.ReplaceAllString(k.title, ""))
		if title == "" {
			title = strings.TrimSpace(k.title)
		}
		if title != "" {
			out = append(out, knownTitle{title, k.as})
		}
	}

	return out
}

// lookalike is a letter of another script drawn like a Latin one.
type lookalike struct {
	latin  rune
	script string
}

// lookalikes are the Cyrillic and Greek letters drawn like a Latin letter,
// and the one they stand in for. The table is kept to the letters that are
// indistinguishable in print: a Greek delta is a letter no one mistakes for
// an A, and used as a symbol it is no stand-in for anything.
var lookalikes = map[rune]lookalike{
	'\u0430': {'a', "Cyrillic"}, '\u0435': {'e', "Cyrillic"}, '\u043E': {'o', "Cyrillic"}, '\u0440': {'p', "Cyrillic"},
	'\u0441': {'c', "Cyrillic"}, '\u0443': {'y', "Cyrillic"}, '\u0445': {'x', "Cyrillic"}, '\u0456': {'i', "Cyrillic"},
	'\u0458': {'j', "Cyrillic"}, '\u0455': {'s', "Cyrillic"},
	'\u0410': {'A', "Cyrillic"}, '\u0412': {'B', "Cyrillic"}, '\u0415': {'E', "Cyrillic"}, '\u041A': {'K', "Cyrillic"},
	'\u041C': {'M', "Cyrillic"}, '\u041D': {'H', "Cyrillic"}, '\u041E': {'O', "Cyrillic"}, '\u0420': {'P', "Cyrillic"},
	'\u0421': {'C', "Cyrillic"}, '\u0422': {'T', "Cyrillic"}, '\u0425': {'X', "Cyrillic"},
	'\u0391': {'A', "Greek"}, '\u0392': {'B', "Greek"}, '\u0395': {'E', "Greek"}, '\u0396': {'Z', "Greek"},
	'\u0397': {'H', "Greek"}, '\u0399': {'I', "Greek"}, '\u039A': {'K', "Greek"}, '\u039C': {'M', "Greek"},
	'\u039D': {'N', "Greek"}, '\u039F': {'O', "Greek"}, '\u03A1': {'P', "Greek"}, '\u03A4': {'T', "Greek"},
	'\u03A5': {'Y', "Greek"}, '\u03A7': {'X', "Greek"}, '\u03BF': {'o', "Greek"}, '\u03BD': {'v', "Greek"},
}

// scriptOf names a letter's script where it is one lookalikes draws from.
func scriptOf(r rune) string {
	switch {
	case unicode.Is(unicode.Cyrillic, r):
		return "Cyrillic"
	case unicode.Is(unicode.Greek, r):
		return "Greek"
	}

	return ""
}

// lookalikeLetters finds the letters in a Latin title that belong to another
// script and only look Latin: a Cyrillic A (U+0410) spelling Arrival, which
// no search for Arrival finds. plain is the title with each put right. A title written in
// the other script is left alone - one with any letter of it that looks like
// no Latin letter uses the script for itself, and a Greek delta in a Latin
// title is a symbol, not a stand-in - and so is a title with no Latin letter
// at all.
func lookalikeLetters(title string) (found []string, plain string) {
	latin, genuine := false, map[string]bool{}
	for _, r := range title {
		if _, ok := lookalikes[r]; ok {
			continue
		}
		switch {
		case unicode.Is(unicode.Latin, r):
			latin = true
		case scriptOf(r) != "" && unicode.IsLetter(r):
			genuine[scriptOf(r)] = true
		}
	}
	if !latin {
		return nil, title
	}

	var b strings.Builder
	for _, r := range title {
		l, ok := lookalikes[r]
		if !ok || genuine[l.script] {
			b.WriteRune(r)

			continue
		}
		found = append(found, fmt.Sprintf("the %s %c (U+%04X) in place of the Latin %c", l.script, r, r, l.latin))
		b.WriteRune(l.latin)
	}

	return found, b.String()
}

// providerTitles asks TMDB what else a film or a series is called, and what
// its search finds by a title and a year, each answer kept for the life of
// the process as tmdb.Facts keeps its own.
type providerTitles struct {
	api *tmdb.Client

	mu          sync.Mutex
	alternative map[string][]string   // "movie:ID" or "tv:ID" -> the titles TMDB lists for it
	searched    map[string][]titleHit // "movie:title:year" -> what the search found
}

// titleHit is one film or series TMDB's search found.
type titleHit struct {
	ID       int
	Title    string
	Original string
	Year     int
}

// newProviderTitles asks TMDB with the configured token, or is nil without
// one.
func newProviderTitles(opts Options) *providerTitles {
	if opts.TMDBKey == "" {
		return nil
	}
	api, err := tmdb.New(tmdb.DefaultBaseURL, opts.TMDBKey)
	if err != nil {
		return nil
	}
	api.Client.HTTPClient = &http.Client{Timeout: 15 * time.Second, Transport: opts.ProviderTransport}

	return &providerTitles{api: api, alternative: map[string][]string{}, searched: map[string][]titleHit{}}
}

// tmdbError names the setting to check when TMDB refuses the credential.
func tmdbError(what string, err error) error {
	if client.StatusCode(err) == http.StatusUnauthorized {
		return fmt.Errorf("tmdb %s: HTTP 401 (check EMBYFIN_TMDB_TOKEN)", what)
	}

	return fmt.Errorf("tmdb %s: %w", what, err)
}

// tmdbKind is the list a library item's TMDB id is read in: movie for a
// film, tv for a series, "" for anything else.
func tmdbKind(itemType string) string {
	switch itemType {
	case typeMovie:
		return "movie"
	case "Series":
		return "tv"
	}

	return ""
}

// alternatives are the other titles TMDB lists for a film or a series: the
// ones it goes by in other countries and languages. An id TMDB does not know
// has none.
func (p *providerTitles) alternatives(ctx context.Context, kind, id string) ([]string, error) {
	memo := kind + ":" + id
	p.mu.Lock()
	titles, ok := p.alternative[memo]
	p.mu.Unlock()
	if ok {
		return titles, nil
	}

	// an id that is not a number is none TMDB has
	titles = []string{}
	n, _ := strconv.Atoi(id)
	if n <= 0 {
		return titles, nil
	}
	switch kind {
	case "movie":
		res, err := p.api.MovieAlternativeTitles(ctx, n, tmdb.MovieAlternativeTitlesOperationOptions{})
		switch {
		case client.IsNotFound(err):
		case err != nil:
			return nil, tmdbError("alternative titles of movie "+id, err)
		case res.Model != nil:
			for _, t := range res.Model.Titles {
				titles = append(titles, t.Title)
			}
		}
	case "tv":
		res, err := p.api.TvSeriesAlternativeTitles(ctx, n)
		switch {
		case client.IsNotFound(err):
		case err != nil:
			return nil, tmdbError("alternative titles of tv "+id, err)
		case res.Model != nil:
			for _, t := range res.Model.Results {
				titles = append(titles, t.Title)
			}
		}
	}

	p.mu.Lock()
	p.alternative[memo] = titles
	p.mu.Unlock()

	return titles, nil
}

// search is what TMDB's search finds by a title, in the year given when
// there is one: films for kind movie, series for tv.
func (p *providerTitles) search(ctx context.Context, kind, title string, year int) ([]titleHit, error) {
	memo := fmt.Sprintf("%s:%s:%d", kind, strings.ToLower(title), year)
	p.mu.Lock()
	hits, ok := p.searched[memo]
	p.mu.Unlock()
	if ok {
		return hits, nil
	}

	hits = []titleHit{}
	yearOf := func(date string) int {
		y, _ := strconv.Atoi(date[:min(len(date), 4)])

		return y
	}
	switch kind {
	case "movie":
		opts := tmdb.SearchMovieOperationOptions{Query: title}
		if year > 0 {
			opts.Year = strconv.Itoa(year)
		}
		res, err := p.api.SearchMovie(ctx, opts)
		if err != nil {
			return nil, tmdbError("search for the film "+title, err)
		}
		if res.Model != nil {
			for _, r := range res.Model.Results {
				hits = append(hits, titleHit{ID: r.Id, Title: r.Title, Original: r.OriginalTitle, Year: yearOf(r.ReleaseDate)})
			}
		}
	case "tv":
		opts := tmdb.SearchTvOperationOptions{Query: title}
		if year > 0 {
			opts.FirstAirDateYear = &year
		}
		res, err := p.api.SearchTv(ctx, opts)
		if err != nil {
			return nil, tmdbError("search for the series "+title, err)
		}
		if res.Model != nil {
			for _, r := range res.Model.Results {
				hits = append(hits, titleHit{ID: r.Id, Title: r.Name, Original: r.OriginalName, Year: yearOf(r.FirstAirDate)})
			}
		}
	}

	p.mu.Lock()
	p.searched[memo] = hits
	p.mu.Unlock()

	return hits, nil
}

// bestHit is the search hit whose title (or original title) is closest to
// the one asked after, dated within a year of it when a year was asked, or
// false when none comes near enough to be the one.
func bestHit(hits []titleHit, title string, year int) (titleHit, bool) {
	best, bestScore := titleHit{}, -1.0
	for _, h := range hits {
		if year > 0 && h.Year > 0 && abs(h.Year-year) > 1 {
			continue
		}
		score, _ := titleScore(title, h.Title)
		if s, _ := titleScore(title, h.Original); s > score {
			score = s
		}
		if score > bestScore {
			best, bestScore = h, score
		}
	}

	return best, bestScore >= seriesConfident
}
