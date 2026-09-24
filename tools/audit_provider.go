package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit_provider: films against their metadata provider, one request a film.
//
// A film matched by hand, or by a server that guessed, can hold a TMDB id
// for one film and an IMDb id for another, or an IMDb id that is not a film
// at all but a series or an episode of one. Nothing on the server shows it:
// the item reads as matched. TMDB, asked about either id, says what it is,
// and the same record says how long the film runs, which a truncated
// download or a wrong file does not.

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
		return fmt.Sprintf("its IMDb id %s is a series, not a film: %s, TMDB tv %d", imdbID, s.Name, s.Id), nil
	case len(f.Episodes) > 0:
		e := f.Episodes[0]
		return fmt.Sprintf("its IMDb id %s is an episode, not a film: %q, S%02dE%02d of TMDB tv %d", imdbID, e.Name, e.SeasonNumber, e.EpisodeNumber, e.ShowId), nil
	}

	return "", nil
}

// providerAuditIn is audit_provider's input.
type providerAuditIn struct {
	Library      string `json:"library,omitempty"           jsonschema:"one library by name or id; default every library"`
	Types        string `json:"types,omitempty"             jsonschema:"what to check; Movie is the only kind yet, and the default"`
	Provider     string `json:"provider,omitempty"          jsonschema:"the provider to ask: tmdb is the only one yet, and the default"`
	Checks       string `json:"checks,omitempty"            jsonschema:"comma-separated: ids (the ids the film holds agree with each other and exist), runtime (the file's runtime against the provider's); default both"`
	TolerancePct int    `json:"tolerance_percent,omitempty" jsonschema:"runtime: flag when the file differs from the provider's runtime by more than this percent, default 20"`
	Limit        int    `json:"limit,omitempty"             jsonschema:"maximum findings, default 50"`
	MaxLookups   int    `json:"max_lookups,omitempty"       jsonschema:"films to ask the provider about in this call, default 250"`
	Offset       int    `json:"offset,omitempty"            jsonschema:"where to go on from: a previous call's next_offset"`
}

type providerFinding struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Year     int      `json:"year,omitempty"`
	Path     string   `json:"path,omitempty"`
	Holds    string   `json:"holds"          jsonschema:"the ids the film holds"`
	Problems []string `json:"problems"       jsonschema:"each begins with the check that failed: ids or runtime"`
}

type providerAuditOut struct {
	Scanned    int               `json:"items_scanned"`
	Found      int               `json:"total_findings"        jsonschema:"among the films this call asked about; the rest wait for next_offset"`
	ByCheck    map[string]int    `json:"by_check"              jsonschema:"findings by the check that failed; a film failing both counts under both"`
	Findings   []providerFinding `json:"findings"              jsonschema:"capped at limit"`
	NextOffset int               `json:"next_offset,omitempty" jsonschema:"pass back as offset to go on; absent when the sweep finished"`
}

// providerChecks are the checks, in the order a row lists what it found.
var providerChecks = []string{"ids", "runtime"}

func registerProviderCheckAudit(r *registry) {
	client := r.client
	provider := tmdbFacts(r.opts)

	desc := "Check films against their metadata provider, one request a film: the ids the film holds agree with each other and exist there (a TMDB id whose film carries a different IMDb id, a TMDB id TMDB no longer has, an IMDb id that is a series or an episode rather than a film), and the file's runtime is the provider's (a truncated download, a wrong file, a wrong match). " +
		"The item reads as matched on the server either way. Paged: pass next_offset back as offset to go on. TMDB is the only provider yet, and films the only kind."
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

// auditAgainstProvider sweeps the films in order and asks the provider about
// each that holds an id, until the lookup budget runs out.
func auditAgainstProvider(ctx context.Context, client *embyfin.Client, provider *tmdb.Facts, in providerAuditIn) (providerAuditOut, error) {
	switch p := strings.ToLower(strings.TrimSpace(in.Provider)); p {
	case "", "tmdb":
	default:
		return providerAuditOut{}, fmt.Errorf("provider must be tmdb, the only one supported yet, not %q", p)
	}
	switch t := strings.ToLower(strings.TrimSpace(in.Types)); t {
	case "", "movie", "movies":
	default:
		return providerAuditOut{}, fmt.Errorf("types must be Movie, the only kind checked against a provider yet, not %q", t)
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
	folder, err := resolveLibrary(ctx, client, in.Library)
	if err != nil {
		return providerAuditOut{}, err
	}
	parent := ""
	if folder != nil {
		parent = folder.ItemID
	}

	out := providerAuditOut{Findings: []providerFinding{}, ByCheck: map[string]int{}}
	lookups := 0
	for start := max(in.Offset, 0); ; start += moviePage {
		items, total, err := client.Search(ctx, embyfin.SearchOptions{
			IncludeItemTypes: "Movie", ParentID: parent, Fields: "Path,ProviderIds,ProductionYear",
			SortBy: "SortName", SortOrder: "Ascending", StartIndex: start, Limit: moviePage,
		})
		if err != nil {
			return providerAuditOut{}, err
		}
		for i := range items {
			it := &items[i]
			tmdbID, imdbID := providerID(it, "tmdb"), providerID(it, "imdb")
			if tmdbID != "" || imdbID != "" {
				if lookups >= maxLookups {
					out.NextOffset = start + i

					return out, nil
				}
				lookups++
			}
			out.Scanned++
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
				if pct, off := runtimeOff(it.RuntimeMinutes(), expected, tolerance); off {
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
		if len(items) < moviePage || start+len(items) >= total {
			return out, nil
		}
	}
}
