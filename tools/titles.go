package tools

import (
	"github.com/katbyte/embyfin-mcp/lib/naming"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
)

// newProviderTitles is what a title check reads a film's or a series' other
// titles and TMDB's search from: the TMDB facts, asked with the configured
// token behind a breaker of their own (tmdb.Guarded), or nil without one.
func newProviderTitles(opts Options) *tmdb.Facts {
	return tmdbFacts(opts, tmdb.Guarded(opts.ProviderTransport))
}

// tmdbKind is the list a library item's TMDB id is read in: movie for a
// film, tv for a series, "" for anything else.
func tmdbKind(itemType string) string {
	switch itemType {
	case typeMovie:
		return tmdb.KindMovie
	case "Series":
		return tmdb.KindTV
	}

	return ""
}

// numberedAs says whether a search hit's title, or its original title,
// carries the numbers a title does (see naming.SameNumbering).
func numberedAs(title string, h tmdb.Hit) bool {
	return naming.SameNumbering(title, h.Title) || h.Original != "" && naming.SameNumbering(title, h.Original)
}

// bestHit is the search hit whose title (or original title) is closest to
// the one asked after, numbered as it is and dated within a year of it when
// a year was asked, or false when none comes near enough to be the one.
func bestHit(hits []tmdb.Hit, title string, year int) (tmdb.Hit, bool) {
	best, bestScore := tmdb.Hit{}, -1.0
	for _, h := range hits {
		if year > 0 && h.Year > 0 && abs(h.Year-year) > 1 || !numberedAs(title, h) {
			continue
		}
		score, _ := naming.Score(title, h.Title)
		if s, _ := naming.Score(title, h.Original); s > score {
			score = s
		}
		if score > bestScore {
			best, bestScore = h, score
		}
	}

	return best, bestScore >= seriesConfident
}
