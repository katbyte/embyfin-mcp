package tmdb

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/client"
)

// What else a film or a series is called, and what TMDB's search finds by a
// title: the facts the file path audit and the version and duplicate
// warnings read a path against, kept with the rest for factsTTL.

// The lists a title is read in: TMDB numbers films and series apart.
const (
	KindMovie = "movie"
	KindTV    = "tv"
)

// Hit is one film or series TMDB's search found, or one film of a collection.
type Hit struct {
	ID       int
	Title    string
	Original string
	Year     int
}

// tmdbNumber is a TMDB id as a number, and whether it is one TMDB could hold:
// an id that is not a number, or is 0, is none TMDB has, and no error.
func tmdbNumber(id string) (n int, known bool, err error) {
	if id == "" || strings.Trim(id, "0123456789") != "" {
		return 0, false, nil
	}
	n, err = strconv.Atoi(id)
	if err != nil {
		return 0, false, fmt.Errorf("tmdb id %s: %w", id, err)
	}

	return n, n > 0, nil
}

// AlternativeTitles are the other titles TMDB lists for a film (kind movie)
// or a series (kind tv): the ones it goes by in other countries and
// languages. An id TMDB does not know has none.
func (f *Facts) AlternativeTitles(ctx context.Context, kind, id string) ([]string, error) {
	memo := kind + ":" + id
	if titles, ok := recall(f, f.alternatives, memo); ok {
		return titles, nil
	}
	titles := []string{}
	n, known, err := tmdbNumber(id)
	if err != nil {
		return nil, fmt.Errorf("tmdb %s %s: %w", kind, id, err)
	}
	if known {
		switch kind {
		case KindMovie:
			res, err := f.api.MovieAlternativeTitles(ctx, n, MovieAlternativeTitlesOperationOptions{})
			switch {
			case client.IsNotFound(err):
			case err != nil:
				return nil, explain("alternative titles of movie "+id, err)
			case res.Model != nil:
				for _, t := range res.Model.Titles {
					titles = append(titles, t.Title)
				}
			}
		case KindTV:
			res, err := f.api.TvSeriesAlternativeTitles(ctx, n)
			switch {
			case client.IsNotFound(err):
			case err != nil:
				return nil, explain("alternative titles of tv "+id, err)
			case res.Model != nil:
				for _, t := range res.Model.Results {
					titles = append(titles, t.Title)
				}
			}
		}
	}
	keep(f, f.alternatives, memo, titles)

	return titles, nil
}

// Translations are a film's or a series' titles in the translations TMDB
// holds for it - its title in each language it is translated into, which
// AlternativeTitles often leaves out ("La llegada" for Arrival). A
// translation giving no title of its own (the original's is used) adds none,
// and an id TMDB does not know has none.
func (f *Facts) Translations(ctx context.Context, kind, id string) ([]string, error) {
	memo := kind + ":" + id
	if titles, ok := recall(f, f.translations, memo); ok {
		return titles, nil
	}
	titles := []string{}
	n, known, err := tmdbNumber(id)
	if err != nil {
		return nil, fmt.Errorf("tmdb translations of %s %s: %w", kind, id, err)
	}
	if known {
		switch kind {
		case KindMovie:
			res, err := f.api.MovieTranslations(ctx, n)
			switch {
			case client.IsNotFound(err):
			case err != nil:
				return nil, explain("translations of movie "+id, err)
			case res.Model != nil:
				for _, t := range res.Model.Translations {
					if t.Data != nil && t.Data.Title != "" {
						titles = append(titles, t.Data.Title)
					}
				}
			}
		case KindTV:
			res, err := f.api.TvSeriesTranslations(ctx, n)
			switch {
			case client.IsNotFound(err):
			case err != nil:
				return nil, explain("translations of tv "+id, err)
			case res.Model != nil:
				for _, t := range res.Model.Translations {
					if t.Data != nil && t.Data.Name != "" {
						titles = append(titles, t.Data.Name)
					}
				}
			}
		}
	}
	keep(f, f.translations, memo, titles)

	return titles, nil
}

// CollectionParts are the films of the TMDB collection a film is in -
// Alien's holds Aliens and Alien³ - the film itself among them; none when
// TMDB puts it in no collection, or does not know the id.
func (f *Facts) CollectionParts(ctx context.Context, id string) ([]Hit, error) {
	if parts, ok := recall(f, f.collected, id); ok {
		return parts, nil
	}
	parts := []Hit{}
	n, known, err := tmdbNumber(id)
	if err != nil {
		return nil, fmt.Errorf("tmdb movie %s: %w", id, err)
	}
	if known {
		res, err := f.api.MovieDetails(ctx, n, MovieDetailsOperationOptions{})
		switch {
		case client.IsNotFound(err):
		case err != nil:
			return nil, explain("movie "+id, err)
		case res.Model != nil && res.Model.BelongsToCollection != nil && res.Model.BelongsToCollection.Id > 0:
			if parts, err = f.collection(ctx, res.Model.BelongsToCollection.Id); err != nil {
				return nil, err
			}
		}
	}
	keep(f, f.collected, id, parts)

	return parts, nil
}

// collection is the films of a TMDB collection, each with its title and
// year, asked once for every film of it.
func (f *Facts) collection(ctx context.Context, id int) ([]Hit, error) {
	memo := strconv.Itoa(id)
	if parts, ok := recall(f, f.collections, memo); ok {
		return parts, nil
	}
	parts := []Hit{}
	res, err := f.api.CollectionDetails(ctx, id, CollectionDetailsOperationOptions{})
	switch {
	case client.IsNotFound(err):
	case err != nil:
		return nil, explain(fmt.Sprintf("collection %d", id), err)
	case res.Model != nil:
		for _, part := range res.Model.Parts {
			year, err := DateYear(fmt.Sprintf("collection %d: film %d", id, part.Id), part.ReleaseDate)
			if err != nil {
				return nil, err
			}
			parts = append(parts, Hit{ID: part.Id, Title: part.Title, Original: part.OriginalTitle, Year: year})
		}
	}
	keep(f, f.collections, memo, parts)

	return parts, nil
}

// searchMemo is Search's key for an answer.
func searchMemo(kind, title string, year int) string {
	return fmt.Sprintf("%s:%s:%d", kind, strings.ToLower(title), year)
}

// Asked says whether Search has already answered for a title and year, so
// asking again costs TMDB nothing.
func (f *Facts) Asked(kind, title string, year int) bool {
	_, ok := recall(f, f.searched, searchMemo(kind, title, year))

	return ok
}

// FailedBefore says whether Search failed for a title and year, TMDB not
// answering (its caller's own giving up aside), and has not answered since.
func (f *Facts) FailedBefore(kind, title string, year int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.unanswered[searchMemo(kind, title, year)]
}

// unansweredSearch notes a search TMDB failed, unless its caller gave up.
func (f *Facts) unansweredSearch(ctx context.Context, memo string) {
	if ctx.Err() != nil {
		return
	}
	f.mu.Lock()
	f.unanswered[memo] = true
	f.mu.Unlock()
}

// Search is what TMDB's search finds by a title, in the year given when
// there is one: films for kind movie, series for tv.
func (f *Facts) Search(ctx context.Context, kind, title string, year int) ([]Hit, error) {
	memo := searchMemo(kind, title, year)
	if hits, ok := recall(f, f.searched, memo); ok {
		return hits, nil
	}

	hits := []Hit{}
	switch kind {
	case KindMovie:
		opts := SearchMovieOperationOptions{Query: title}
		if year > 0 {
			opts.Year = strconv.Itoa(year)
		}
		res, err := f.api.SearchMovie(ctx, opts)
		if err != nil {
			f.unansweredSearch(ctx, memo)

			return nil, explain("search for the film "+title, err)
		}
		if res.Model != nil {
			for _, r := range res.Model.Results {
				y, err := DateYear(fmt.Sprintf("search for the film %s: film %d", title, r.Id), r.ReleaseDate)
				if err != nil {
					return nil, err
				}
				hits = append(hits, Hit{ID: r.Id, Title: r.Title, Original: r.OriginalTitle, Year: y})
			}
		}
	case KindTV:
		opts := SearchTvOperationOptions{Query: title}
		if year > 0 {
			opts.FirstAirDateYear = &year
		}
		res, err := f.api.SearchTv(ctx, opts)
		if err != nil {
			f.unansweredSearch(ctx, memo)

			return nil, explain("search for the series "+title, err)
		}
		if res.Model != nil {
			for _, r := range res.Model.Results {
				y, err := DateYear(fmt.Sprintf("search for the series %s: series %d", title, r.Id), r.FirstAirDate)
				if err != nil {
					return nil, err
				}
				hits = append(hits, Hit{ID: r.Id, Title: r.Name, Original: r.OriginalName, Year: y})
			}
		}
	}

	keep(f, f.searched, memo, hits)
	f.mu.Lock()
	delete(f.unanswered, memo)
	f.mu.Unlock()

	return hits, nil
}
