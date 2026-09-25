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
	ID   string `json:"id"`
	Name string `json:"name"`
	// a path in the film's own language names this, and without it beside
	// the name the two read as another film
	OriginalTitle       string            `json:"original_title,omitempty"        jsonschema:"the title in the film's or series' own language, when it is not the name"`
	Type                string            `json:"type"`
	Year                int               `json:"year,omitempty"`
	Series              string            `json:"series,omitempty"`
	Season              *int              `json:"season,omitempty"                jsonschema:"on a season or an episode only: its season's number, 0 for the specials"`
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
	// set by the tools that can tell a film's file names another film: two
	// films on one id read as one film's copies or versions without it
	Warning string `json:"warning,omitempty" jsonschema:"set when a file of this entry names another film than the one the entry is matched to: it is probably a different film sharing the id, not a copy or a version of this one"`
}

// originalIfOther is an item's original title when it says something its
// name does not.
func originalIfOther(it *embyfin.Item) string {
	if strings.EqualFold(strings.TrimSpace(it.OriginalTitle), strings.TrimSpace(it.Name)) {
		return ""
	}

	return it.OriginalTitle
}

func summarise(it *embyfin.Item) itemSummary {
	return itemSummary{
		ID:                  it.ID,
		Name:                it.Name,
		OriginalTitle:       originalIfOther(it),
		Type:                it.Type,
		Year:                it.ProductionYear,
		Series:              it.SeriesName,
		Season:              seasonOf(it),
		Episode:             episodeOf(it),
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
		Versions        []versionRow `json:"versions,omitempty"         jsonschema:"every file the server shows the item in, when it shows it in more than one; read warning before keeping one over another"`
		Overview        string       `json:"overview,omitempty"`
		Genres          []string     `json:"genres"`
		Tags            []string     `json:"tags"`
		Studios         []string     `json:"studios"`
		OfficialRating  string       `json:"official_rating,omitempty"  jsonschema:"the parental rating, e.g. PG-13"`
		CommunityRating float64      `json:"community_rating,omitempty" jsonschema:"the provider's audience score, out of 10"`
		People          []personOut  `json:"people,omitempty"           jsonschema:"directors, writers, and top-billed cast"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "item_get",
		Description: "Fetch one library item by id with full quality facts: video/audio/subtitle streams, container, size, runtime, path, metadata provider ids, overview, genres, tags, studios, ratings, and people. " +
			"An item the server shows in several files lists every version with its own path, runtime and facts. warning says when a film's file names a title the item does not go by (none of its name, original title or sort name) or a year more than one off, with the versions' runtimes when they are far apart: probably a different film matched to this one's ids, not a copy of it. Runtimes far apart alone are no warning: a director's cut runs longer too.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getItemIn) (*mcp.CallToolResult, getItemOut, error) {
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, getItemOut{}, err
		}
		versions, err := versionsOf(ctx, client, it)
		if err != nil {
			return nil, getItemOut{}, err
		}

		out := getItemOut{
			itemSummary: summarise(it), Overview: it.Overview, Genres: it.Genres, Tags: it.TagNames(), Studios: it.StudioNames(),
			OfficialRating: it.OfficialRating, CommunityRating: math.Round(it.CommunityRating*10) / 10, // the servers' float32, without its noise
		}
		shown := *it
		shown.MediaSources = versions
		out.Warning = versionWarning(&shown)
		if len(versions) > 1 {
			for i := range versions {
				out.Versions = append(out.Versions, versionRow{
					ID: versions[i].ItemID, Label: versions[i].Name, Path: versions[i].Path,
					RuntimeS: int(versions[i].RunTimeTicks / ticksPerSecond), qualityFacts: sourceQuality(&versions[i]),
				})
			}
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
		Description: "Find the films and series in the library matching a metadata provider id (tmdb/imdb/tvdb). The definitive 'do I already have this movie?' check; returns every copy.",
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
		// both servers answer an id they do not hold with nothing similar
		if _, err := client.ItemByID(ctx, in.ID); err != nil {
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
		Landed    bool   `json:"landed"         jsonschema:"true once the refresh was seen to save the item; false when it had not within about a minute (queued behind a scan, say): it still runs, and undoes an edit made before it does"`
		Note      string `json:"note,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "item_refresh",
		Description: "Ask the server to re-fetch metadata and images for one item, filling what is missing (replace_all replaces everything), and wait for the refresh to land: the server runs it a moment after it is asked, and an edit made before it has run is undone by it. landed says whether it was seen to save the item within about a minute; a series' or season's episodes are refreshed after it. " +
			"A library with its metadata fetchers off has nothing to fetch: there a refresh re-reads the files and nfo sidecars, and replace_all is refused. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in refreshIn) (*mcp.CallToolResult, refreshOut, error) {
		// read first: an id neither server holds is answered with a bare 400 or 500
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, refreshOut{}, err
		}
		if in.ReplaceAll {
			folder, ferr := libraryOf(ctx, client, it)
			if ferr != nil {
				return nil, refreshOut{}, ferr
			}
			// Jellyfin clears such an item rather than re-reading its files;
			// Emby re-reads them. Refused on both, so the tool does one thing
			if folder != nil && folder.FetchersOff(it.Type) {
				return nil, refreshOut{}, fmt.Errorf("the %s library has its metadata fetchers off, so replace_all has nothing to fetch and would clear %s's metadata: refresh without replace_all to re-read its files and nfo, or set fields with item_edit", folder.Name, it.Name)
			}
		}
		landed, err := client.RefreshItem(ctx, in.ID, in.ReplaceAll)
		if err != nil {
			return nil, refreshOut{}, err
		}
		out := refreshOut{Refreshed: in.ID, Landed: landed}
		if !landed {
			out.Note = "the refresh was asked for and not seen to save the item within about a minute: it is still queued (a scan or other refreshes ahead of it) and runs later, undoing any edit made to the item before it does"
		}

		return nil, out, nil
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
		// Emby answers an id it does not hold with an empty mix
		if _, err := client.ItemByID(ctx, in.ID); err != nil {
			return nil, mixOut{}, err
		}

		items, err := client.InstantMix(ctx, in.ID, limit)
		if err != nil {
			return nil, mixOut{}, err
		}
		// Emby answers a song's mix with more tracks than the limit
		items = items[:min(len(items), limit)]

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
		NoAccess   bool   `json:"no_access,omitempty"   jsonschema:"true for an account that has since lost access to the item's library: what it watched there still counts, as it does in the audit of what nobody has watched"`
	}
	type lastWatchedOut struct {
		Item  string     `json:"item"`
		Users []watchRow `json:"users"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "item_last_watched",
		Description: "Per-user watch state for one item: played, play count, last played date, resume point, for each user who can see it, and for each account that watched or started it before losing access to its library (no_access), which still counts as watched. An account held back from it by a parental rating is left out.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lastWatchedIn) (*mcp.CallToolResult, lastWatchedOut, error) {
		item, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, lastWatchedOut{}, err
		}
		users, err := client.Users(ctx)
		if err != nil {
			return nil, lastWatchedOut{}, err
		}
		// the item's library, read the first time an account cannot see it
		var folder *embyfin.VirtualFolder
		folderRead := false

		out := lastWatchedOut{Item: item.Name, Users: []watchRow{}}
		for _, u := range users {
			// the single-item read: Emby's lists leave the play count and the
			// last played date out
			it, seen, err := client.VisibleUserItem(ctx, u.ID, in.ID)
			if err != nil {
				return nil, lastWatchedOut{}, err
			}
			lost := false
			if !seen {
				// an account that lost access to the library still watched
				// what it watched there, and audit_unwatched counts it; one
				// held back by a parental rating is left out, as there
				if !folderRead && item.Path != "" {
					if folder, err = libraryOf(ctx, client, item); err != nil {
						return nil, lastWatchedOut{}, err
					}
					folderRead = true
				}
				if folder == nil || u.CanSee(folder) {
					continue
				}
				if it, err = watchedOutOfView(ctx, client, &u, folder, in.ID); err != nil {
					return nil, lastWatchedOut{}, err
				}
				lost = true
			}
			ud := (*embyfin.UserData)(nil)
			if it != nil {
				ud = it.UserData
			}
			if ud == nil || lost && !ud.Played && ud.PlayCount == 0 && ud.PlaybackPositionTicks == 0 {
				continue // nothing watched, by an account that cannot see it
			}
			out.Users = append(out.Users, watchRow{
				User:       u.Name,
				Played:     ud.Played,
				PlayCount:  ud.PlayCount,
				LastPlayed: ud.LastPlayedDate,
				ResumeS:    int(ud.PlaybackPositionTicks / ticksPerSecond),
				NoAccess:   lost,
			})
		}

		return nil, out, nil
	})

	type historyIn struct {
		ID   string `json:"id"             jsonschema:"the library item id"`
		Days int    `json:"days,omitempty" jsonschema:"how many days back to search, default 60"`
	}
	type historyOut struct {
		Item     string   `json:"item"`
		Entries  []string `json:"entries"        jsonschema:"activity log lines mentioning this item, newest first"`
		Complete bool     `json:"complete"       jsonschema:"false when the period holds more activity than one call reads: the answer then covers only the newest part of it, and note says how far back"`
		Note     string   `json:"note,omitempty" jsonschema:"how far back the activity log was read, when that is short of the period"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "item_watch_history",
		Description: "Playback events for one item from the server activity log (who played it, when), default last 60 days.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, historyOut, error) {
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, historyOut{}, err
		}

		activity, err := readActivity(ctx, client, daysCutoff(in.Days))
		if err != nil {
			return nil, historyOut{}, err
		}

		out := historyOut{Item: it.Name, Complete: activity.complete, Note: activity.note()}
		for _, e := range activity.entries {
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

	type setStateIn struct {
		ID        string `json:"id"                   jsonschema:"the library item id"`
		User      string `json:"user,omitempty"       jsonschema:"user name or id; defaults to the first administrator"`
		Watched   *bool  `json:"watched,omitempty"    jsonschema:"true marks played, false marks unplayed (which also clears a resume point)"`
		Favourite *bool  `json:"favourite,omitempty"  jsonschema:"true favourites, false unfavourites"`
		PositionS *int   `json:"position_s,omitempty" jsonschema:"where playback resumes from, in seconds from the start, above zero: the item shows under continue watching from there and is marked not yet watched"`
	}
	type setStateOut struct {
		Item      string `json:"item"`
		User      string `json:"user"`
		Watched   *bool  `json:"watched,omitempty"`
		Favourite *bool  `json:"favourite,omitempty"`
		PositionS *int   `json:"position_s,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_set_state",
		Description: "Set a user's state on an item: watched or not, favourite or not, and where it resumes from, any or all in one call, for a user who can see it. Emby keeps watch state and favourites by metadata provider id, so there every copy of the film (or episode) is marked with it. user_next_up lists the resume points. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setStateIn) (*mcp.CallToolResult, setStateOut, error) {
		if in.Watched == nil && in.Favourite == nil && in.PositionS == nil {
			return nil, setStateOut{}, errors.New("nothing to set: pass watched, favourite or position_s")
		}
		if in.PositionS != nil && *in.PositionS <= 0 {
			return nil, setStateOut{}, errors.New("position_s must be above zero; to clear a resume point, pass watched false")
		}
		if in.PositionS != nil && in.Watched != nil && *in.Watched {
			return nil, setStateOut{}, errors.New("position_s and watched true contradict: a resume point is part way through, watched is the end")
		}
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, setStateOut{}, err
		}
		it, err := visibleTo(ctx, client, user, in.ID)
		if err != nil {
			return nil, setStateOut{}, err
		}
		if in.Favourite != nil {
			if err := client.SetFavourite(ctx, user.ID, in.ID, *in.Favourite); err != nil {
				return nil, setStateOut{}, err
			}
		}
		if in.PositionS != nil {
			if err := client.SetProgress(ctx, user.ID, in.ID, int64(*in.PositionS)*ticksPerSecond); err != nil {
				return nil, setStateOut{}, err
			}
		}
		// a resume point already marks the item not yet watched, and marking
		// it unplayed again would clear the point just set
		if in.Watched != nil && (in.PositionS == nil || *in.Watched) {
			if err := client.SetPlayed(ctx, user.ID, in.ID, *in.Watched); err != nil {
				return nil, setStateOut{}, err
			}
		}

		return nil, setStateOut{Item: it.Name, User: user.Name, Watched: in.Watched, Favourite: in.Favourite, PositionS: in.PositionS}, nil
	})

	type deleteIn struct {
		ID      string `json:"id"                jsonschema:"the library item id"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true to delete, and acknowledges the media FILES are permanently deleted from disk; without it the call is refused with what it would remove"`
	}
	type deleteOut struct {
		Deleted string        `json:"deleted"`
		Removed []removedPath `json:"removed"        jsonschema:"every file and folder the delete took off the server's disk, read before and after it: a film alone in its folder takes the whole folder (its nfo, artwork, subtitles, extras and every version), as a series or season does; an episode, or a film sharing its folder, takes its files and every nfo, subtitle and image whose name begins with its file's name, another item's included"`
		Note    string        `json:"note,omitempty"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name: "item_delete",
		Description: "PERMANENTLY delete an item AND its media from disk. Irreversible. The server takes more than the item's own file: a film alone in its folder goes with the whole folder (nfo, artwork, subtitles, extras, every version), and a series or season with its folder. " +
			"An episode, or a film sharing its folder, goes with every nfo, subtitle and image whose name begins with its file's name, ignoring case and whatever follows - another item's too: deleting Alien.mkv from a folder it shares with Aliens.mkv also deletes Aliens.nfo, Aliens' subtitles and its poster (never Aliens.mkv itself). " +
			"Without confirm=true it refuses, saying what it would remove and which of those belong to another item; with it, the answer lists every path removed.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, deleteOut{}, err
		}
		// every version the server holds for it, which on Emby only the
		// item read in a user's view lists
		versions := []string{}
		if admin, aerr := client.ResolveUser(ctx, ""); aerr == nil {
			if full, uerr := client.UserItem(ctx, admin.ID, in.ID); uerr == nil {
				for _, s := range full.MediaSources {
					versions = append(versions, s.Path)
				}
			}
		}
		plan, err := planDelete(ctx, client, it, versions)
		if err != nil {
			return nil, deleteOut{}, err
		}
		if !in.Confirm {
			return nil, deleteOut{}, fmt.Errorf("refusing to delete %s without confirm=true: nothing was deleted. %s", it.Name, plan.would())
		}

		// a scan that had read the item's folder before the delete landed
		// can list the item again once it finishes (seen on Jellyfin 12.1):
		// the files stay gone, and the next scan lets the record go
		scanning, _ := client.LibraryScanRunning(ctx)
		if err := client.DeleteItem(ctx, in.ID); err != nil {
			return nil, deleteOut{}, err
		}
		removed, note, err := r.removedBy(ctx, plan, it.Path)
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("deleted %s, but reading back what went failed: %w", it.Name, err)
		}
		if after, _ := client.LibraryScanRunning(ctx); scanning || after {
			if note != "" {
				note += "; "
			}
			note += "a library scan was running: it can list this item again once it finishes, pointing at files that are gone, until the next scan lets it go - if it is still listed after that, delete it again"
		}

		return nil, deleteOut{Deleted: it.Name + " (" + it.Path + ")", Removed: removed, Note: note}, nil
	})
}
