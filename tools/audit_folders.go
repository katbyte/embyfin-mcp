package tools

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Two folders for one show.
//
// A rename that differs only in spacing, case or an accent leaves the old
// folder behind and the server builds a SECOND series from it, splitting the
// episodes across two entries that answer separately. audit_duplicates cannot
// see it when the second entry carries no provider id - which is the usual
// case, because nothing matched it.
//
// It answers the opposite question too, and that matters as much: told that
// two folders collide, a caller can stop guessing from its own generated
// strings. One session concluded a show had split over a composed against a
// combining accent when it had not.

// folderKey is what two folder names have in common when they are the same
// name written differently: case, spacing, accents and the punctuation a
// rename tends to move.
func folderKey(name string) string {
	folded := foldAccents(strings.ToLower(name))

	var b strings.Builder
	space := false
	for _, r := range folded {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			// every run of anything else is one separator: "A  - B", "A - B"
			// and "A-B" are the same name
			space = true
		}
	}

	return b.String()
}

func registerFolderAudit(r *registry) {
	client := r.client

	type folderRow struct {
		SeriesID string `json:"series_id"`
		Name     string `json:"name"`
		Year     int    `json:"year,omitempty"`
		Folder   string `json:"folder"         jsonschema:"the folder name that collided"`
		Path     string `json:"path,omitempty"`
	}
	type folderGroup struct {
		Key    string      `json:"key"    jsonschema:"the parent directory and what the folder names collapse to once case, spacing, accents and punctuation are folded"`
		Series []folderRow `json:"series" jsonschema:"the entries built from those folders"`
	}
	type folderIn struct {
		Library string `json:"library,omitempty" jsonschema:"one library by name or id"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum groups, default 50"`
	}
	type folderOut struct {
		Scanned int           `json:"series_scanned"`
		Found   int           `json:"total_groups"`
		Groups  []folderGroup `json:"groups"         jsonschema:"capped at limit; total_groups is the real count"`
	}

	add(r, readTool, &mcp.Tool{
		Name: "audit_duplicate_series_folders",
		Description: "Find shows the server holds twice because two folders name the same series: a rename that changed only spacing, case, an accent or punctuation leaves the old folder behind and a second entry is built from it. " +
			"The episodes are then split across both entries, so each answers 'no' to half the questions asked of it. audit_duplicates cannot see these when the second entry carries no provider id, which is usual.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in folderIn) (*mcp.CallToolResult, folderOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		opts, err := sweepOptions(ctx, client, in.Library, "Series", "Series", "Path,ProductionYear")
		if err != nil {
			return nil, folderOut{}, err
		}

		byKey := map[string][]folderRow{}
		out := folderOut{Groups: []folderGroup{}}
		if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
			for i := range items {
				it := &items[i]
				out.Scanned++
				if it.Path == "" {
					continue
				}
				folder := filepath.Base(it.Path)
				// the parent is part of the key: a clean library and a messy
				// one holding the same show are two folders of the same name
				// and nothing is wrong with that. A rename leaves its twin
				// beside it.
				key := folderKey(filepath.Dir(it.Path)) + "/" + folderKey(folder)
				if key == "" {
					continue
				}
				byKey[key] = append(byKey[key], folderRow{
					SeriesID: it.ID, Name: it.Name, Year: it.ProductionYear,
					Folder: folder, Path: it.Path,
				})
			}

			return true
		}); err != nil {
			return nil, folderOut{}, err
		}

		var groups []folderGroup
		for key, rows := range byKey {
			if len(rows) < 2 {
				continue
			}
			slices.SortFunc(rows, func(a, b folderRow) int { return strings.Compare(a.Folder, b.Folder) })
			groups = append(groups, folderGroup{Key: key, Series: rows})
		}
		slices.SortFunc(groups, func(a, b folderGroup) int { return strings.Compare(a.Key, b.Key) })

		out.Found = len(groups)
		out.Groups = append(out.Groups, groups[:min(len(groups), limit)]...)

		return nil, out, nil
	})
}
