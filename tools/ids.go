package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// The ids an audit is pointed at, read before the audit runs.
//
// An audit narrowed to a handful of items passes their ids to the server's
// search as a filter, and a filter the server cannot use is worse than no
// filter at all. Jellyfin drops an ids filter holding anything that is not
// one of its ids, and Emby one of no id it can use - 0, or only commas - and
// both answer with the whole library (seen on Jellyfin 12.1 and Emby 4.10;
// Emby refuses one that is not a number or a Guid at all), so audit_provider
// ran over every film as if for the one asked about, a TMDB lookup each. An
// id no item has, or the id of a season or a collection, came back as
// nothing at all: items_scanned 0, no findings, no error, which reads as
// "checked, and clean".
//
// (The message for a filter dropped names Emby, which does so for 0; Jellyfin
// does so for anything that is not one of its ids, and the check catches
// both the same way: an answer holding items that were not asked for.)

// checkIDs reads the items ids name and refuses any id no item has, or whose
// item is not one of kinds, saying what it is: what the audit is for is in
// what, as a caller reads it ("films", "films, series and episodes").
func checkIDs(ctx context.Context, client *embyfin.Client, ids, kinds []string, what string) error {
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}

	found := map[string]embyfin.Item{}
	ignored := false
	for chunk := range slices.Chunk(ids, idsPerRequest) {
		// one more than asked for: a server that dropped the filter answers
		// with other items, and one is enough to see it did
		items, _, err := client.Search(ctx, embyfin.SearchOptions{IDs: strings.Join(chunk, ","), Fields: "Path,SeriesName", Limit: len(chunk) + 1})
		if err != nil {
			return fmt.Errorf("reading the items the ids %s name: %w", strings.Join(chunk, ", "), err)
		}
		for i := range items {
			if !want[items[i].ID] {
				ignored = true

				continue
			}
			found[items[i].ID] = items[i]
		}
	}

	var missing []string
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		why := ""
		if ignored {
			why = ": the server answered with other items instead, as it does for an id it cannot use"
		}

		return fmt.Errorf("no item has the id %s%s", strings.Join(missing, ", "), why)
	}

	for _, id := range ids {
		it := found[id]
		if !slices.ContainsFunc(kinds, func(k string) bool { return strings.EqualFold(strings.TrimSpace(k), it.Type) }) {
			return fmt.Errorf("%s is %s, not one of the %s this checks", id, describeItem(&it), what)
		}
	}

	return nil
}

// joinNotes is the notes given that say anything, one after another.
func joinNotes(notes ...string) string {
	return strings.Join(slices.DeleteFunc(slices.Clone(notes), func(n string) bool { return n == "" }), "; ")
}

// describeItem says what an item is, for a refusal: its kind and its name.
func describeItem(it *embyfin.Item) string {
	name := it.Name
	if it.SeriesName != "" && it.Type != "Series" {
		name = it.SeriesName + ": " + name
	}
	kind := strings.ToLower(it.Type)
	switch it.Type {
	case "":
		kind = "an item"
	case typeMovie:
		kind = "a film"
	case "BoxSet":
		kind = "a collection"
	case "CollectionFolder":
		kind = "a library"
	case "MusicArtist":
		kind = "an artist"
	case "MusicAlbum":
		kind = "an album"
	case "Audio":
		kind = "a track"
	case typeEpisode:
		kind = "an episode"
	default:
		kind = "a " + kind
	}

	return fmt.Sprintf("%s, %q", kind, name)
}
