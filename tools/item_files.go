package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	apiclient "github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// The files behind the items a read answers with.
//
// Jellyfin folds a second file of one film or episode in one folder into the
// item as a version, and no item query lists that file on its own: its path
// is in the item's media sources and nowhere else. A read that leaves the
// media sources out, to be cheap, names the item's first file alone, and a
// caller comparing a folder against it reads the second file as not in the
// library. Emby holds every file as an item of its own, one file each, so
// there is nothing folded to read back.

// itemOrVersion reads the item an id names, or the version folded into
// another item it names: Jellyfin lists a second file of a film or an episode
// in one folder by an id of its own (versions[].id, other_copies), which no
// item query finds and the single read in a user's view answers, as the
// server's image, subtitle and similar reads do. An id neither finds is an
// embyfin.NoItemError. Emby holds every file as an item of its own, which
// the item query finds.
func itemOrVersion(ctx context.Context, client *embyfin.Client, id string) (*embyfin.Item, error) {
	item, err := client.ItemByID(ctx, id)
	var none *embyfin.NoItemError
	if err == nil || !errors.As(err, &none) {
		return item, err
	}

	return versionByID(ctx, client, id, none)
}

// versionByID reads the version an id names through the single read in the
// first administrator's view, answering none when that finds no such item:
// the fallback for an id the item query found nothing for (see
// itemOrVersion).
func versionByID(ctx context.Context, client *embyfin.Client, id string, none *embyfin.NoItemError) (*embyfin.Item, error) {
	if client.Backend() != embyfin.Jellyfin {
		return nil, none
	}
	admin, err := client.ResolveUser(ctx, "")
	if err != nil {
		return nil, err
	}
	version, err := client.UserItem(ctx, admin.ID, id)
	switch {
	case apiclient.IsNotFound(err):
		return nil, none
	case err != nil:
		return nil, err
	case version == nil || version.ID != id:
		return nil, none
	}

	return version, nil
}

// versionCountField is what a read asks for to know which items Jellyfin
// holds in more than one file, without paying for every item's streams.
const versionCountField = "MediaSourceCount"

// withVersionFiles reads back the media sources of the items a read took
// without them and that Jellyfin says it holds in more than one file (the
// read must ask for versionCountField), a batch of ids at a time.
func withVersionFiles(ctx context.Context, client *embyfin.Client, items []embyfin.Item) error {
	var folded []string
	at := map[string][]int{}
	for i := range items {
		if items[i].MediaSourceCount > 1 && len(items[i].MediaSources) < items[i].MediaSourceCount {
			if _, seen := at[items[i].ID]; !seen {
				folded = append(folded, items[i].ID)
			}
			at[items[i].ID] = append(at[items[i].ID], i)
		}
	}
	for chunk := range slices.Chunk(folded, idsPerRequest) {
		read, _, err := client.Search(ctx, embyfin.SearchOptions{IDs: strings.Join(chunk, ","), Fields: "Path,MediaSources", Limit: len(chunk)})
		if err != nil {
			return err
		}
		for i := range read {
			for _, j := range at[read[i].ID] {
				items[j].MediaSources = read[i].MediaSources
			}
		}
	}
	// fewer files read back than the server counted leaves the rest unknown,
	// which a row naming the ones read would not say
	for _, id := range folded {
		if it := &items[at[id][0]]; len(it.MediaSources) < it.MediaSourceCount {
			return fmt.Errorf("the server counts %d files for %s (id %s), and reading them back found %d: ask again", it.MediaSourceCount, it.Name, it.ID, len(it.MediaSources))
		}
	}

	return nil
}
