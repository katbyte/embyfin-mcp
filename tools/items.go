package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// itemSummary is the trimmed view returned by search/lookup tools — enough to
// identify an item and judge its quality without the full MediaBrowser payload.
type itemSummary struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	Type                string            `json:"type"`
	Year                int               `json:"year,omitempty"`
	Series              string            `json:"series,omitempty"`
	Season              int               `json:"season,omitempty"`
	Episode             int               `json:"episode,omitempty"`
	RuntimeS            int               `json:"runtime_s,omitempty"             jsonschema:"runtime in seconds"`
	Path                string            `json:"path,omitempty"`
	MetadataProviderIDs map[string]string `json:"metadata_provider_ids,omitempty" jsonschema:"keyed tmdb, imdb, tvdb"`
	// the same facts, under the same names and in the same units, as an
	// episode row: a caller comparing an item_get against a library_episodes
	// row should not have to convert megabytes, minutes, or a sentence
	qualityFacts
	Added        string `json:"added,omitempty"         jsonschema:"when the item was added to the library"`
	FileModified string `json:"file_modified,omitempty" jsonschema:"when the file itself last changed (Emby only); this moves when a download overwrites a path in place, while added does not"`
}

func summarise(it *embyfin.Item) itemSummary {
	return itemSummary{
		ID:                  it.ID,
		Name:                it.Name,
		Type:                it.Type,
		Year:                it.ProductionYear,
		Series:              it.SeriesName,
		Season:              it.ParentIndexNumber,
		Episode:             it.IndexNumber,
		RuntimeS:            int(it.RunTimeTicks / ticksPerSecond),
		Path:                it.Path,
		MetadataProviderIDs: providerKeys(it.ProviderIDs),
		Added:               it.DateCreated,
		FileModified:        it.DateModified,

		// the best file speaks for the item, as it does for an episode row
		qualityFacts: qualityOf(it),
	}
}

// providerKeys spells provider ids with lowercase keys (tmdb, imdb, tvdb):
// the servers spell them three ways between them (Tmdb, IMDB, Imdb), and
// item_find_by_metadata_id takes the lowercase form.
func providerKeys(ids map[string]string) map[string]string {
	if ids == nil {
		return nil
	}
	out := make(map[string]string, len(ids))
	for k, v := range ids {
		out[strings.ToLower(k)] = v
	}

	return out
}

// nameRefs spells a list of names the way Emby's GenreItems and TagItems
// want them.
func nameRefs(names []string) []map[string]string {
	out := make([]map[string]string, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]string{"Name": n})
	}

	return out
}

func summariseAll(items []embyfin.Item) []itemSummary {
	out := make([]itemSummary, 0, len(items))
	for i := range items {
		out = append(out, summarise(&items[i]))
	}

	return out
}

// visibleTo reads an item and checks a user may see it, before a change is
// made in their name: Emby stores watch state for an item in a library the
// user cannot see, and Jellyfin answers the change with a bare 404.
func visibleTo(ctx context.Context, client *embyfin.Client, user *embyfin.User, itemID string) (*embyfin.Item, error) {
	it, err := client.ItemByID(ctx, itemID)
	if err != nil {
		return nil, err
	}
	_, seen, err := client.VisibleUserItem(ctx, user.ID, itemID)
	if err != nil {
		return nil, err
	}
	if !seen {
		return nil, fmt.Errorf("%s cannot see %s: it is in a library they have no access to, or rated above what they may watch", user.Name, it.Name)
	}

	return it, nil
}

// libraryOf is the library whose folders hold an item's file, nil for an item
// in none (a collection, a playlist).
func libraryOf(ctx context.Context, client *embyfin.Client, it *embyfin.Item) (*embyfin.VirtualFolder, error) {
	folders, err := client.VirtualFolders(ctx)
	if err != nil {
		return nil, err
	}

	return embyfin.FolderOf(folders, it.Path), nil
}

func registerItemTools(r *registry) {
	client := r.client
	type getItemIn struct {
		ID string `json:"id" jsonschema:"the library item id"`
	}
	type personOut struct {
		Name string `json:"name"`
		Type string `json:"type,omitempty"`
		Role string `json:"role,omitempty"`
	}
	type getItemOut struct {
		itemSummary
		Overview        string      `json:"overview,omitempty"`
		Genres          []string    `json:"genres"`
		Tags            []string    `json:"tags"`
		Studios         []string    `json:"studios"`
		OfficialRating  string      `json:"official_rating,omitempty"  jsonschema:"the parental rating, e.g. PG-13"`
		CommunityRating float64     `json:"community_rating,omitempty" jsonschema:"the provider's audience score, out of 10"`
		People          []personOut `json:"people,omitempty"           jsonschema:"directors, writers, and top-billed cast"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "item_get",
		Description: "Fetch one library item by id with full quality facts: video/audio/subtitle streams, container, size, runtime, path, metadata provider ids, overview, genres, tags, studios, ratings, and people.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getItemIn) (*mcp.CallToolResult, getItemOut, error) {
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, getItemOut{}, err
		}

		out := getItemOut{
			itemSummary: summarise(it), Overview: it.Overview, Genres: it.Genres, Tags: it.TagNames(), Studios: it.StudioNames(),
			OfficialRating: it.OfficialRating, CommunityRating: math.Round(it.CommunityRating*10) / 10, // the servers' float32, without its noise
		}
		for i, p := range it.People {
			if i >= 15 {
				break
			}
			out.People = append(out.People, personOut{Name: p.Name, Type: p.Type, Role: p.Role})
		}

		return nil, out, nil
	})

	type lookupIn struct {
		Provider string `json:"metadata_provider" jsonschema:"metadata provider: tmdb, imdb, or tvdb"`
		ID       string `json:"id"                jsonschema:"the metadata provider's id, e.g. 89998 or tt0045655"`
	}
	type lookupOut struct {
		Found bool          `json:"found"`
		Items []itemSummary `json:"items,omitempty" jsonschema:"can be multiple when the library has more than one copy"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "item_find_by_metadata_id",
		Description: "Find library items matching a metadata provider id (tmdb/imdb/tvdb). The definitive 'do I already have this movie?' check; returns every copy.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lookupIn) (*mcp.CallToolResult, lookupOut, error) {
		provider := strings.ToLower(strings.TrimSpace(in.Provider))
		items, err := client.ItemsByProviderID(ctx, provider, strings.TrimSpace(in.ID))
		if err != nil {
			return nil, lookupOut{}, err
		}

		return nil, lookupOut{Found: len(items) > 0, Items: summariseAll(items)}, nil
	})

	type similarIn struct {
		ID    string `json:"id"              jsonschema:"the library item id"`
		Limit int    `json:"limit,omitempty" jsonschema:"maximum results, default 10"`
		User  string `json:"user,omitempty"  jsonschema:"user name or id whose library view to use; defaults to the first administrator"`
	}
	type similarOut struct {
		Items []itemSummary `json:"items"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "item_similar",
		Description: "Items in the library the server considers similar to the given one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in similarIn) (*mcp.CallToolResult, similarOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 10
		}

		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, similarOut{}, err
		}

		items, err := client.Similar(ctx, in.ID, user.ID, limit)
		if err != nil {
			return nil, similarOut{}, err
		}

		return nil, similarOut{Items: summariseAll(items)}, nil
	})

	type refreshIn struct {
		ID         string `json:"id"                    jsonschema:"the library item id"`
		ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"replace all existing metadata and images instead of filling gaps; refused in a library whose metadata fetchers are off"`
	}
	type refreshOut struct {
		Refreshed string `json:"refreshed"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_refresh",
		Description: "Ask the server to re-fetch metadata and images for one item, filling what is missing (replace_all replaces everything). A library with its metadata fetchers off has nothing to fetch: there a refresh re-reads the files and nfo sidecars, and replace_all is refused. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in refreshIn) (*mcp.CallToolResult, refreshOut, error) {
		if in.ReplaceAll {
			it, err := client.ItemByID(ctx, in.ID)
			if err != nil {
				return nil, refreshOut{}, err
			}
			folder, err := libraryOf(ctx, client, it)
			if err != nil {
				return nil, refreshOut{}, err
			}
			// Jellyfin clears such an item rather than re-reading its files;
			// Emby re-reads them. Refused on both, so the tool does one thing
			if folder != nil && folder.FetchersOff(it.Type) {
				return nil, refreshOut{}, fmt.Errorf("the %s library has its metadata fetchers off, so replace_all has nothing to fetch and would clear %s's metadata: refresh without replace_all to re-read its files and nfo, or set fields with item_edit", folder.Name, it.Name)
			}
		}
		if err := client.RefreshItem(ctx, in.ID, in.ReplaceAll); err != nil {
			return nil, refreshOut{}, err
		}

		return nil, refreshOut{Refreshed: in.ID}, nil
	})

	type editIn struct {
		ID       string   `json:"id"                  jsonschema:"the library item id"`
		Name     string   `json:"name,omitempty"      jsonschema:"new display title"`
		SortName string   `json:"sort_name,omitempty" jsonschema:"new sort title"`
		Overview string   `json:"overview,omitempty"  jsonschema:"new overview/plot text"`
		Year     int      `json:"year,omitempty"      jsonschema:"new production year"`
		Genres   []string `json:"genres,omitempty"    jsonschema:"replacement genre list"`
		Tags     []string `json:"tags,omitempty"      jsonschema:"replacement tag list"`
	}
	type editOut struct {
		Updated []string `json:"updated_fields"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_edit",
		Description: "Update an item's metadata fields (title, sort title, overview, year, genres, tags). Only provided fields change. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		admin, err := client.ResolveUser(ctx, "")
		if err != nil {
			return nil, editOut{}, err
		}

		var updated []string
		if _, err := client.EditItem(ctx, admin.ID, in.ID, func(full map[string]any) (bool, error) {
			setField := func(key string, val any, changed bool) {
				if changed {
					full[key] = val
					updated = append(updated, key)
				}
			}
			setField("Name", in.Name, in.Name != "")
			setField("SortName", in.SortName, in.SortName != "")
			setField("ForcedSortName", in.SortName, in.SortName != "")
			setField("Overview", in.Overview, in.Overview != "")
			setField("ProductionYear", in.Year, in.Year > 0)
			// Jellyfin reads the plain lists; Emby reads the named records
			setField("Genres", in.Genres, len(in.Genres) > 0)
			if len(in.Genres) > 0 {
				full["GenreItems"] = nameRefs(in.Genres)
			}
			setField("Tags", in.Tags, len(in.Tags) > 0)
			if len(in.Tags) > 0 {
				full["TagItems"] = nameRefs(in.Tags)
			}
			if len(updated) == 0 {
				return false, errors.New("no fields to update were provided")
			}

			return true, nil
		}); err != nil {
			return nil, editOut{}, err
		}

		return nil, editOut{Updated: updated}, nil
	})

	type mixIn struct {
		ID    string `json:"id"              jsonschema:"a song, album, artist, playlist, or music genre item id to seed the mix"`
		Limit int    `json:"limit,omitempty" jsonschema:"maximum tracks, default 30"`
	}
	type mixOut struct {
		Items []itemSummary `json:"items"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "item_instant_mix",
		Description: "Generate a music mix seeded from a song, album, artist, or genre.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mixIn) (*mcp.CallToolResult, mixOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 30
		}

		items, err := client.InstantMix(ctx, in.ID, limit)
		if err != nil {
			return nil, mixOut{}, err
		}

		return nil, mixOut{Items: summariseAll(items)}, nil
	})

	type lastWatchedIn struct {
		ID string `json:"id" jsonschema:"the library item id"`
	}
	type watchRow struct {
		User       string `json:"user"`
		Played     bool   `json:"played"`
		PlayCount  int    `json:"play_count,omitempty"`
		LastPlayed string `json:"last_played,omitempty"`
		ResumeS    int    `json:"resume_s,omitempty"    jsonschema:"seconds into the item if partially watched"`
	}
	type lastWatchedOut struct {
		Item  string     `json:"item"`
		Users []watchRow `json:"users"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "item_last_watched",
		Description: "Per-user watch state for one item: played, play count, last played date, resume point, for each user who can see it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lastWatchedIn) (*mcp.CallToolResult, lastWatchedOut, error) {
		users, err := client.Users(ctx)
		if err != nil {
			return nil, lastWatchedOut{}, err
		}

		out := lastWatchedOut{}
		for _, u := range users {
			// the single-item read: Emby's lists leave the play count and the
			// last played date out
			it, seen, err := client.VisibleUserItem(ctx, u.ID, in.ID)
			if err != nil {
				return nil, lastWatchedOut{}, err
			}
			if !seen {
				continue // a user who cannot see the item
			}

			out.Item = it.Name
			if ud := it.UserData; ud != nil {
				out.Users = append(out.Users, watchRow{
					User:       u.Name,
					Played:     ud.Played,
					PlayCount:  ud.PlayCount,
					LastPlayed: ud.LastPlayedDate,
					ResumeS:    int(ud.PlaybackPositionTicks / ticksPerSecond),
				})
			}
		}

		return nil, out, nil
	})

	type historyIn struct {
		ID   string `json:"id"             jsonschema:"the library item id"`
		Days int    `json:"days,omitempty" jsonschema:"how many days back to search, default 60"`
	}
	type historyOut struct {
		Item    string   `json:"item"`
		Entries []string `json:"entries" jsonschema:"activity log lines mentioning this item, newest first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "item_watch_history",
		Description: "Playback events for one item from the server activity log (who played it, when), default last 60 days.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, historyOut, error) {
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, historyOut{}, err
		}

		entries, _, err := client.ActivityLog(ctx, daysCutoff(in.Days), activityScanLimit)
		if err != nil {
			return nil, historyOut{}, err
		}

		out := historyOut{Item: it.Name}
		for _, e := range entries {
			// by id when the entry names its item: a title is a substring of
			// others ("Dune" of "Dune: Part Two")
			match := e.ItemID == in.ID
			if e.ItemID == "" {
				match = strings.Contains(e.Name, it.Name) || strings.Contains(e.ShortOverview, it.Name)
			}
			if match {
				out.Entries = append(out.Entries, e.Date+" "+e.Name)
			}
		}

		return nil, out, nil
	})

	type setWatchedIn struct {
		ID      string `json:"id"             jsonschema:"the library item id"`
		User    string `json:"user,omitempty" jsonschema:"user name or id; defaults to the first administrator"`
		Watched bool   `json:"watched"        jsonschema:"true marks played, false marks unplayed"`
	}
	type setWatchedOut struct {
		Item    string `json:"item"`
		User    string `json:"user"`
		Watched bool   `json:"watched"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_set_watched",
		Description: "Mark an item played or unplayed for a user who can see it. Emby keeps watch state by metadata provider id, so there every copy of the film (or episode) is marked with it. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setWatchedIn) (*mcp.CallToolResult, setWatchedOut, error) {
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, setWatchedOut{}, err
		}
		if _, err := visibleTo(ctx, client, user, in.ID); err != nil {
			return nil, setWatchedOut{}, err
		}

		if err := client.SetPlayed(ctx, user.ID, in.ID, in.Watched); err != nil {
			return nil, setWatchedOut{}, err
		}

		return nil, setWatchedOut{Item: in.ID, User: user.Name, Watched: in.Watched}, nil
	})

	type setFavouriteIn struct {
		ID        string `json:"id"             jsonschema:"the library item id"`
		User      string `json:"user,omitempty" jsonschema:"user name or id; defaults to the first administrator"`
		Favourite bool   `json:"favourite"`
	}
	type setFavouriteOut struct {
		Item      string `json:"item"`
		User      string `json:"user"`
		Favourite bool   `json:"favourite"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_set_favourite",
		Description: "Favourite or unfavourite an item for a user who can see it. Emby keeps this by metadata provider id, so there every copy of the film is marked with it. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setFavouriteIn) (*mcp.CallToolResult, setFavouriteOut, error) {
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, setFavouriteOut{}, err
		}
		if _, err := visibleTo(ctx, client, user, in.ID); err != nil {
			return nil, setFavouriteOut{}, err
		}

		if err := client.SetFavourite(ctx, user.ID, in.ID, in.Favourite); err != nil {
			return nil, setFavouriteOut{}, err
		}

		return nil, setFavouriteOut{Item: in.ID, User: user.Name, Favourite: in.Favourite}, nil
	})

	type deleteIn struct {
		ID      string `json:"id"      jsonschema:"the library item id"`
		Confirm bool   `json:"confirm" jsonschema:"must be true; acknowledges the media FILE is permanently deleted from disk"`
	}
	type deleteOut struct {
		Deleted string `json:"deleted"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name:        "item_delete",
		Description: "PERMANENTLY delete an item AND its media file from disk. Irreversible. Requires confirm=true.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		if !in.Confirm {
			return nil, deleteOut{}, errors.New("refusing to delete without confirm=true")
		}

		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, deleteOut{}, err
		}

		if err := client.DeleteItem(ctx, in.ID); err != nil {
			return nil, deleteOut{}, err
		}

		return nil, deleteOut{Deleted: it.Name + " (" + it.Path + ")"}, nil
	})
}
