package tools

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"unicode/utf8"

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
// rename tends to move. Letters and digits of every script are kept (see
// wordRune): folded away, 進撃の巨人 and 鬼滅の刃 were one name, and so was
// every other folder named in Japanese beside them.
func folderKey(name string) string {
	var b strings.Builder
	space, latin := false, false
	for _, r := range strings.ToLower(name) {
		spelling, word := string(r), r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if r >= utf8.RuneSelf {
			spelling, word = wordRune(r, latin)
		}
		switch {
		case !word:
			// every run of anything else is one separator: "A  - B", "A - B"
			// and "A-B" are the same name
			space = true
		case spelling != "":
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteString(spelling)
			latin = spelling[0] < utf8.RuneSelf
		}
	}

	return b.String()
}

type folderIn struct {
	Library string `json:"library,omitempty" jsonschema:"one library by name or id"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum groups, default 50"`
}

type folderOut struct {
	Scanned int           `json:"items_scanned"`
	Found   int           `json:"total_findings"`
	Groups  []folderGroup `json:"groups"         jsonschema:"capped at limit; total_findings is the real count"`
}

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

func registerDuplicateSeriesAudit(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_duplicate_series",
		Description: "Find shows the server holds twice because two folders name the same series: a rename that changed only spacing, case, an accent or punctuation leaves the old folder behind and a second entry is built from it. " +
			"The episodes are then split across both entries, so each answers 'no' to half the questions asked of it. audit_duplicates cannot see these when the second entry carries no provider id, which is usual.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in folderIn) (*mcp.CallToolResult, folderOut, error) {
		out, err := auditDuplicateSeries(ctx, client, in)

		return nil, out, err
	})
}

func auditDuplicateSeries(ctx context.Context, client *embyfin.Client, in folderIn) (folderOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	opts, err := sweepOptions(ctx, client, in.Library, "Series", "Series", "Path,ProductionYear")
	if err != nil {
		return folderOut{}, err
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
			// split on either separator: a server on Windows answers with
			// backslashes, whatever this runs on
			folder := baseName(it.Path)
			name := folderKey(folder)
			// a name that folds to nothing ("???") says nothing to compare,
			// and would otherwise meet every other one like it
			if name == "" {
				continue
			}
			// the parent is part of the key: a clean library and a messy
			// one holding the same show are two folders of the same name
			// and nothing is wrong with that. A rename leaves its twin
			// beside it.
			key := folderKey(parentDir(it.Path)) + "/" + name
			byKey[key] = append(byKey[key], folderRow{
				SeriesID: it.ID, Name: it.Name, Year: it.ProductionYear,
				Folder: folder, Path: it.Path,
			})
		}

		return true
	}); err != nil {
		return folderOut{}, err
	}

	var groups []folderGroup
	for key, rows := range byKey {
		if len(rows) < 2 {
			continue
		}
		slices.SortFunc(rows, func(a, b folderRow) int {
			return cmp.Or(strings.Compare(a.Folder, b.Folder), strings.Compare(a.SeriesID, b.SeriesID))
		})
		groups = append(groups, folderGroup{Key: key, Series: rows})
	}
	slices.SortFunc(groups, func(a, b folderGroup) int { return strings.Compare(a.Key, b.Key) })

	out.Found = len(groups)
	out.Groups = append(out.Groups, groups[:min(len(groups), limit)]...)

	return out, nil
}
