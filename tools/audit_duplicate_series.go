package tools

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/mediapath"
)

// Two folders for one show: audit_duplicates' folder_groups.
//
// A rename that differs only in spacing, case or an accent leaves the old
// folder behind and the server builds a SECOND series from it, splitting the
// episodes across two entries that answer separately. Sharing an id cannot
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

// folderOut is what the folder rule found: how many series were read, how
// many pairs of folders name one show, and the first of them up to a limit.
type folderOut struct {
	Scanned int
	Found   int
	Groups  []folderGroup
	Note    string
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

// duplicateFolders is audit_duplicates' folder rule over one library, or
// every library: the series whose folders name one show, up to limit of them.
// It reads nothing when the types asked for leave series out, since it finds
// series alone, and types left empty asks for them.
func duplicateFolders(ctx context.Context, client *embyfin.Client, library, types string, limit int) (folderOut, error) {
	if types != "" && !slices.ContainsFunc(strings.Split(types, ","), func(t string) bool { return strings.EqualFold(strings.TrimSpace(t), "Series") }) {
		return folderOut{Groups: []folderGroup{}}, nil
	}
	opts, err := sweepOptions(ctx, client, library, "Series", "Series", "Path,ProductionYear")
	if err != nil {
		return folderOut{}, err
	}

	byKey := map[string][]folderRow{}
	out := folderOut{Groups: []folderGroup{}}
	swept, err := client.ReadAll(ctx, opts, embyfin.ToAnswer, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			out.Scanned++
			if it.Path == "" {
				continue
			}
			// split on either separator: a server on Windows answers with
			// backslashes, whatever this runs on
			folder := mediapath.Base(it.Path)
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
			key := folderKey(mediapath.Dir(it.Path)) + "/" + name
			byKey[key] = append(byKey[key], folderRow{
				SeriesID: it.ID, Name: it.Name, Year: it.ProductionYear,
				Folder: folder, Path: it.Path,
			})
		}

		return true
	})
	if err != nil {
		return folderOut{}, err
	}
	out.Note = swept.Changed()

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
