package tools

import (
	"context"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// Versions, as the servers show them to people.
//
// Jellyfin merges the files of one film in one folder into one item with
// several versions when it scans, and every read of the item answers with all
// of them. Emby 4.10 merges too - files named as versions of one film in one
// folder, and copies in other folders sharing a provider id - but only in a
// user's view: a sweep of /Items holds each file as an item of its own with
// one version, a list in a user's view hides all but one of the merged items
// without naming the others as its versions, and only the single item read in
// a user's view lists every version. So on Emby what people are shown is
// read in three steps: the sweep; the same sweep in an administrator's view,
// where the items not listed are versions of ones that are; and a single read
// of each item not listed, whose versions name the item it was merged into.

// shownItems sweeps opts and answers with the items as the server shows them
// to people, each with every version it holds: on Jellyfin the sweep itself,
// on Emby the items an administrator's view lists, each carrying the versions
// of the items merged into it.
func shownItems(ctx context.Context, client *embyfin.Client, opts embyfin.SearchOptions) ([]embyfin.Item, error) {
	var all []embyfin.Item
	if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
		all = append(all, items...)

		return true
	}); err != nil {
		return nil, err
	}
	if client.Backend() != embyfin.Emby || len(all) == 0 {
		return all, nil
	}

	admin, err := client.ResolveUser(ctx, "")
	if err != nil {
		return nil, err
	}
	listed := map[string]bool{}
	view := opts
	view.UserID, view.Fields = admin.ID, "Path"
	if err := client.SearchAll(ctx, view, func(items []embyfin.Item) bool {
		for i := range items {
			listed[items[i].ID] = true
		}

		return true
	}); err != nil {
		return nil, err
	}

	// the listed items by their file, which is how a merged item's versions
	// name it
	byPath := map[string]int{}
	var shown []embyfin.Item
	var hidden []embyfin.Item
	for i := range all {
		if listed[all[i].ID] {
			byPath[all[i].Path] = len(shown)
			shown = append(shown, all[i])

			continue
		}
		hidden = append(hidden, all[i])
	}
	for i := range hidden {
		it, err := client.UserItem(ctx, admin.ID, hidden[i].ID)
		if err != nil {
			return nil, err
		}
		owner := -1
		for _, src := range it.MediaSources {
			if j, ok := byPath[src.Path]; ok {
				owner = j

				break
			}
		}
		if owner < 0 {
			// merged into nothing this sweep reaches (or not merged at all,
			// only left out of the view): it stands on its own
			shown = append(shown, hidden[i])

			continue
		}
		// every version, as the single read lists them: the listed item's
		// own file among them
		shown[owner].MediaSources = it.MediaSources
	}

	return shown, nil
}
