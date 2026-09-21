package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Films whose ids disagree with each other, asked of TMDB.
//
// A film matched by hand, or by a server that guessed, can hold a TMDB id
// for one film and an IMDb id for another, or an IMDb id that is not a film
// at all but a series or an episode of one. Nothing on the server shows it:
// the item reads as matched. TMDB, asked about either id, says what it is.

// checkMovieIDs asks TMDB about a film's ids and says what is wrong with
// them, "" when nothing is. An IMDb id TMDB cannot place is not reported:
// that is as likely TMDB lacking the film as the id being wrong.
func checkMovieIDs(ctx context.Context, provider *tmdb.Client, tmdbID, imdbID string) (string, error) {
	if tmdbID != "" {
		m, err := provider.Movie(ctx, tmdbID)
		switch {
		case err != nil:
			return "", err
		case m.ID == 0:
			return fmt.Sprintf("TMDB has no film %s: the id is wrong, or the film was taken down", tmdbID), nil
		case imdbID != "" && m.IMDbID != "" && !strings.EqualFold(m.IMDbID, imdbID):
			return fmt.Sprintf("its TMDB id is %d, %s (%d), whose IMDb id is %s, not the %s it holds: one of the two is wrong", m.ID, m.Title, m.Year(), m.IMDbID, imdbID), nil
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
		return fmt.Sprintf("its IMDb id %s is a series, not a film: %s, TMDB tv %d", imdbID, s.Name, s.ID), nil
	case len(f.Episodes) > 0:
		e := f.Episodes[0]
		return fmt.Sprintf("its IMDb id %s is an episode, not a film: %q, S%02dE%02d of TMDB tv %d", imdbID, e.Name, e.Season, e.Episode, e.ShowID), nil
	}

	return "", nil
}

func registerMovieIDAudit(r *registry) {
	client := r.client
	var provider *tmdb.Client
	if r.opts.TMDBKey != "" {
		provider = tmdb.NewWithTransport(r.opts.TMDBKey, r.opts.ProviderTransport)
	}

	type movieIDsIn struct {
		Library    string `json:"library,omitempty"     jsonschema:"one library by name or id; default every library"`
		Limit      int    `json:"limit,omitempty"       jsonschema:"maximum findings, default 50"`
		MaxLookups int    `json:"max_lookups,omitempty" jsonschema:"TMDB lookups this call may make, one a film, default 250"`
		StartIndex int    `json:"start_index,omitempty" jsonschema:"where to go on from: a previous call's next_start_index"`
	}
	type movieIDFinding struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Year   int    `json:"year,omitempty"`
		Path   string `json:"path,omitempty"`
		Holds  string `json:"holds"          jsonschema:"the ids the film holds"`
		Detail string `json:"detail"`
	}
	type movieIDsOut struct {
		Scanned        int              `json:"items_scanned"`
		Found          int              `json:"total_findings"             jsonschema:"among the films this call looked up; the rest wait for next_start_index"`
		Findings       []movieIDFinding `json:"findings"                   jsonschema:"capped at limit"`
		NextStartIndex int              `json:"next_start_index,omitempty" jsonschema:"pass back as start_index to go on; absent when the sweep finished"`
	}

	desc := "Find films whose ids disagree, by asking TMDB: a TMDB id whose film carries a different IMDb id, a TMDB id TMDB no longer has, or an IMDb id that is a series or an episode rather than a film. " +
		"The item reads as matched on the server either way. One TMDB request a film, so the sweep is paged: pass next_start_index back as start_index to go on."
	if provider == nil {
		desc += " Disabled: set EMBYFIN_TMDB_TOKEN to enable."
	}
	add(r, readTool, &mcp.Tool{
		Name:        "audit_movie_ids",
		Description: desc,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in movieIDsIn) (*mcp.CallToolResult, movieIDsOut, error) {
		if provider == nil {
			return nil, movieIDsOut{}, errors.New("audit_movie_ids needs a TMDB token: set EMBYFIN_TMDB_TOKEN (or --tmdb-token) and restart")
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		maxLookups := in.MaxLookups
		if maxLookups <= 0 {
			maxLookups = defaultTMDBLookups
		}
		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, movieIDsOut{}, err
		}
		parent := ""
		if folder != nil {
			parent = folder.ItemID
		}

		out := movieIDsOut{Findings: []movieIDFinding{}}
		lookups := 0
		for start := max(in.StartIndex, 0); ; start += moviePage {
			items, total, err := client.Search(ctx, embyfin.SearchOptions{
				IncludeItemTypes: "Movie", ParentID: parent, Fields: "Path,ProviderIds,ProductionYear",
				SortBy: "SortName", SortOrder: "Ascending", StartIndex: start, Limit: moviePage,
			})
			if err != nil {
				return nil, movieIDsOut{}, err
			}
			for i := range items {
				it := &items[i]
				tmdbID, imdbID := providerID(it, "tmdb"), providerID(it, "imdb")
				if tmdbID != "" || imdbID != "" {
					if lookups >= maxLookups {
						out.NextStartIndex = start + i

						return nil, out, nil
					}
					lookups++
				}
				out.Scanned++
				if tmdbID == "" && imdbID == "" {
					continue
				}

				detail, err := checkMovieIDs(ctx, provider, tmdbID, imdbID)
				if err != nil {
					return nil, movieIDsOut{}, err
				}
				if detail == "" {
					continue
				}
				out.Found++
				if len(out.Findings) < limit {
					var holds []string
					if tmdbID != "" {
						holds = append(holds, "tmdb:"+tmdbID)
					}
					if imdbID != "" {
						holds = append(holds, "imdb:"+imdbID)
					}
					out.Findings = append(out.Findings, movieIDFinding{
						ID: it.ID, Name: it.Name, Year: it.ProductionYear, Path: it.Path,
						Holds: strings.Join(holds, " "), Detail: detail,
					})
				}
			}
			if len(items) < moviePage || start+len(items) >= total {
				return nil, out, nil
			}
		}
	})
}
