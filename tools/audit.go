package tools

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// auditIn is shared by all audit sweeps.
type auditIn struct {
	Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
	Types   string `json:"types,omitempty"   jsonschema:"comma-separated item types to audit; defaults to Movie,Series"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum findings to return, default 100"`
}

type auditFinding struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Year   int    `json:"year,omitempty"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type auditOut struct {
	Scanned  int            `json:"items_scanned"`
	Found    int            `json:"total_findings"`
	Findings []auditFinding `json:"findings"       jsonschema:"capped at limit; total_findings is the real count"`
}

// runAudit sweeps matching items and collects findings from check. check
// returns (finding detail, true) when the item is suspect.
func runAudit(ctx context.Context, client *embyfin.Client, in auditIn, fields string, check func(*embyfin.Item) (string, bool)) (auditOut, error) {
	types := in.Types
	if types == "" {
		types = "Movie,Series"
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}

	opts := embyfin.SearchOptions{IncludeItemTypes: types, Fields: fields}

	folder, err := resolveLibrary(ctx, client, in.Library)
	if err != nil {
		return auditOut{}, err
	}
	if folder != nil {
		opts.ParentID = folder.ItemID
	}

	out := auditOut{}
	sweepErr := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
		for i := range items {
			out.Scanned++
			detail, suspect := check(&items[i])
			if !suspect {
				continue
			}

			out.Found++
			if len(out.Findings) < limit {
				out.Findings = append(out.Findings, auditFinding{
					ID:     items[i].ID,
					Name:   items[i].Name,
					Year:   items[i].ProductionYear,
					Path:   items[i].Path,
					Detail: detail,
				})
			}
		}
		return true
	})
	if sweepErr != nil {
		return auditOut{}, sweepErr
	}

	return out, nil
}

var pathYearRe = regexp.MustCompile(`\((19|20)\d\d\)`)

func registerAuditTools(server *mcp.Server, client *embyfin.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "library_audit_missing_metadata_provider",
		Description: "Sweep the library for items with no metadata provider ids (tmdb/imdb/tvdb) — unmatched items that need identification.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := runAudit(ctx, client, in, embyfin.FieldsLean, func(it *embyfin.Item) (string, bool) {
			for k, v := range it.ProviderIDs {
				switch strings.ToLower(k) {
				case "tmdb", "imdb", "tvdb":
					if v != "" {
						return "", false
					}
				}
			}
			return "no tmdb/imdb/tvdb id", true
		})

		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "library_audit_missing_poster",
		Description: "Sweep the library for items with no primary poster image.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := runAudit(ctx, client, in, embyfin.FieldsLean+",ImageTags", func(it *embyfin.Item) (string, bool) {
			if it.ImageTags["Primary"] == "" {
				return "no primary image", true
			}
			return "", false
		})

		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "library_audit_missing_overview",
		Description: "Sweep the library for items with no overview/plot text — usually a sign of a failed metadata match.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := runAudit(ctx, client, in, embyfin.FieldsLean, func(it *embyfin.Item) (string, bool) {
			if strings.TrimSpace(it.Overview) == "" {
				return "no overview", true
			}
			return "", false
		})

		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "library_audit_year_mismatch",
		Description: "Sweep the library for items whose folder/file name contains a (year) that disagrees with the matched metadata year by 2+ — a strong wrong-match signal.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := runAudit(ctx, client, in, embyfin.FieldsLean, func(it *embyfin.Item) (string, bool) {
			if it.Path == "" || it.ProductionYear == 0 {
				return "", false
			}

			m := pathYearRe.FindString(it.Path)
			if m == "" {
				return "", false
			}

			pathYear, _ := strconv.Atoi(strings.Trim(m, "()"))
			diff := pathYear - it.ProductionYear
			if diff < 0 {
				diff = -diff
			}
			if diff >= 2 {
				return "path says " + strconv.Itoa(pathYear) + ", metadata says " + strconv.Itoa(it.ProductionYear), true
			}

			return "", false
		})

		return nil, out, err
	})

	type dupOut struct {
		Scanned int             `json:"items_scanned"`
		Groups  [][]itemSummary `json:"duplicate_groups" jsonschema:"each group shares one metadata provider id"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "library_duplicates",
		Description: "Find items sharing the same tmdb/imdb id — multiple copies of the same movie or series.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, dupOut, error) {
		types := in.Types
		if types == "" {
			types = "Movie,Series"
		}

		opts := embyfin.SearchOptions{IncludeItemTypes: types}

		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, dupOut{}, err
		}
		if folder != nil {
			opts.ParentID = folder.ItemID
		}

		byProvider := map[string][]embyfin.Item{}
		out := dupOut{}
		sweepErr := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
			for _, it := range items {
				out.Scanned++
				for k, v := range it.ProviderIDs {
					lk := strings.ToLower(k)
					if (lk == "tmdb" || lk == "imdb") && v != "" {
						key := lk + ":" + v
						byProvider[key] = append(byProvider[key], it)
					}
				}
			}
			return true
		})
		if sweepErr != nil {
			return nil, dupOut{}, sweepErr
		}

		seen := map[string]bool{}
		for _, group := range byProvider {
			if len(group) < 2 || seen[group[0].ID] {
				continue
			}
			seen[group[0].ID] = true
			out.Groups = append(out.Groups, summariseAll(group))
		}

		return nil, out, nil
	})
}
