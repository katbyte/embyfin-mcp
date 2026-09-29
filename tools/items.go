package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
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
	Season              *int              `json:"season,omitempty"                jsonschema:"on a season or an episode only: its season's number, 0 for the specials. Absent on an episode or season the server holds no season number for, which is not the specials"`
	Episode             *int              `json:"episode,omitempty"               jsonschema:"on an episode the server holds a number for"`
	RuntimeS            int               `json:"runtime_s,omitempty"             jsonschema:"runtime in seconds"`
	Path                string            `json:"path,omitempty"                  jsonschema:"the file the facts beside it are read from; an item held in more than one file lists every one in versions"`
	MetadataProviderIDs map[string]string `json:"metadata_provider_ids,omitempty" jsonschema:"keyed tmdb, imdb, tvdb"`
	// the same facts, under the same names and in the same units, as an
	// episode row: a caller comparing an item_get against a library_episodes
	// row should not have to convert megabytes, minutes, or a sentence
	qualityFacts
	// Jellyfin folds a second file of one film in one folder into the film,
	// and no item query lists that file on its own
	Versions     []versionRow `json:"versions,omitempty"      jsonschema:"every file the item is held in, when more than one, each with its own id, path, runtime and facts: Jellyfin folds a second file of one film or episode in one folder into the item and lists it nowhere else, so a folder compared against path alone misses it. Deleting the item deletes every one"`
	Added        string       `json:"added,omitempty"         jsonschema:"when the item was added to the library"`
	FileModified string       `json:"file_modified,omitempty" jsonschema:"when the file itself last changed (Emby only); this moves when a download overwrites a path in place, while added does not"`
	// set by the tools that can tell a film's file names another film: two
	// films on one id read as one film's copies or versions without it
	Warning string `json:"warning,omitempty" jsonschema:"set when a file of this entry may name another film than the one the entry is matched to: 'probably' when a year or TMDB says so - a different film sharing the id, not a copy or a version of this one - and 'may' when only its title is none the film goes by, TMDB lists it as an entry of its own that may be an edition, or TMDB could not be asked"`
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

		// the file the path names speaks for the item, as it does for an
		// episode row, and every file is listed when there are several
		qualityFacts: pathQuality(it),
		Versions:     versionRows(it, nil),
	}
}

// peopleShown is how many people item_get lists before the cast is cut: an
// episode can credit dozens of guest stars.
const peopleShown = 15

// crewCredits are the credits item_get keeps whatever the cast: who made the
// thing, a film, a show, a song or a book. Jellyfin lists the cast first
// (twenty-one actors and guest stars before Breaking Bad's pilot's director
// and writer), so a cut of the first fifteen dropped them.
var crewCredits = []string{"Director", "Writer", "Creator", "Composer", "Lyricist", "Conductor", "Author"}

// castCredits are the credits of the people on screen.
var castCredits = []string{"Actor", "GuestStar"}

// creditedPeople keeps every crew credit (crewCredits), however many, then
// the cast in the server's order while there are fewer than peopleShown.
func creditedPeople(people []embyfin.Person) []embyfin.Person {
	var out []embyfin.Person
	for _, p := range people {
		if slices.Contains(crewCredits, p.Type) {
			out = append(out, p)
		}
	}
	for _, p := range people {
		if len(out) >= peopleShown {
			break
		}
		if slices.Contains(castCredits, p.Type) {
			out = append(out, p)
		}
	}

	return out
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
	check := r.newTitleCheck()
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
		People          []personOut `json:"people,omitempty"           jsonschema:"every director, writer, creator, composer, lyricist, conductor and author the server credits, however many, then the top-billed cast (actors and guest stars, in the server's order) while there are fewer than 15 people; producers and the rest of the crew are left out"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "item_get",
		Description: "Fetch one library item by id with full quality facts: video/audio/subtitle streams, container, size, runtime, path, metadata provider ids, overview, genres, tags, studios, ratings, and people. " +
			"The top-level facts are the file at path, on both servers; an item the server shows in several files lists every version in versions with its own id, path, runtime and facts (read warning before keeping one over another). warning says when a film's file names a title the item does not go by (none of its name, original title or sort name, nor with EMBYFIN_TMDB_TOKEN one TMDB lists for it, or its search finds it by under that title or one of TMDB's translations of it) or a year more than one off, with the versions' runtimes when they are far apart: 'probably not one film' (or, for a film in one file, 'probably a different film') when a year or TMDB says another film, 'may not be one film' when only the title is none the film goes by, which can be a title of it no list holds, or TMDB lists it as an entry of its own titled the film's and an edition's words (an edition, or another film), or TMDB could not be asked. A file whose words before its year are the film's, with more after, is asked of TMDB by its whole title in no year and against the films of the film's TMDB collection: another film when TMDB names one of another id by them - a film of its own whose title is the film's and a number alone ('Film: Part Two') or another film of its collection among them - and 'may' only when that film's title is the film's and an edition's words alone (an edition TMDB lists apart, or another film). A file whose title is the film's and whose year is two or more off is asked of TMDB by its title and year, as the file path audit asks it: this very film says the year the item holds is the one to check (no other film), another film says 'probably' - but another film of exactly the film's name 'may', as only the file can tell the two apart - and nothing either way - or no token - that it may be another film, or the item's year is wrong. A renamer's or a release's words after the year are no title: tags in brackets or braces, a trailing '-GROUP', quality, source, HDR, IMAX, 3D, language and dub words, a stacked file's part ('cd1', 'Disc 2') and an extra's word ('Sample', 'Trailer'), read across a hyphen or a plus ('Bluray-1080p', 'HDR10+', 'German-DL'); 'Part 2', the number set apart, is a title's. Runtimes far apart alone are no warning: a director's cut runs longer too.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getItemIn) (*mcp.CallToolResult, getItemOut, error) {
		// a version's own id too, which item_get lists in versions
		it, err := itemOrVersion(ctx, client, in.ID)
		if err != nil {
			return nil, getItemOut{}, err
		}
		versions, err := versionsOf(ctx, client, it)
		if err != nil {
			return nil, getItemOut{}, err
		}

		// every file the server shows the item in, which on Emby only the
		// read in a user's view names: the facts are the one at the item's
		// own path, on both servers
		shown := *it
		shown.MediaSources = versions
		summary := summarise(&shown)
		summary.Warning = versionWarning(ctx, check, &shown)
		out := getItemOut{
			itemSummary: summary, Overview: it.Overview, Genres: it.Genres, Tags: it.TagNames(), Studios: it.StudioNames(),
			OfficialRating: it.OfficialRating, CommunityRating: math.Round(it.CommunityRating*10) / 10, // the servers' float32, without its noise
		}
		for _, p := range creditedPeople(it.People) {
			out.People = append(out.People, personOut{Name: p.Name, Type: p.Type, Role: p.Role})
		}

		return nil, out, nil
	})

	type lookupIn struct {
		Provider string `json:"metadata_provider" jsonschema:"metadata provider: tmdb, imdb, or tvdb"`
		ID       string `json:"id"                jsonschema:"the metadata provider's id, e.g. 89998 or tt0045655"`
		Type     string `json:"type,omitempty"    jsonschema:"movie or series: needed for tmdb and tvdb, which number films and series apart (TMDB's film 1396 and its series 1396 are two different things); for imdb, whose ids each name one thing, leave it out to look among both"`
	}
	type lookupOut struct {
		Found bool          `json:"found"`
		Note  string        `json:"note,omitempty"  jsonschema:"set when the library was seen to change while Jellyfin's films or series were read for the id: an item added or removed then may be missing from items, or listed though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read"`
		Items []itemSummary `json:"items,omitempty" jsonschema:"every entry the library holds under the id: more than one when it holds the title more than once. On Emby each file is an entry of its own; on Jellyfin a second file of one film in one folder is folded into the entry and listed in its versions"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "item_find_by_metadata_id",
		Description: "Find the films or series in the library matching a metadata provider id (tmdb/imdb/tvdb). The definitive 'do I already have this movie?' check; returns every entry, each with every file it is held in. " +
			"tmdb and tvdb number films and series apart, so type (movie or series) is needed with them: TMDB's film 1396 is not its series 1396.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lookupIn) (*mcp.CallToolResult, lookupOut, error) {
		provider := strings.ToLower(strings.TrimSpace(in.Provider))
		types := embyfin.ProviderIDTypes
		switch strings.ToLower(strings.TrimSpace(in.Type)) {
		case "movie", "film":
			types = typeMovie
		case "series", "show", "tv":
			types = "Series"
		case "":
			if provider == "tmdb" || provider == "tvdb" {
				return nil, lookupOut{}, fmt.Errorf("%s numbers films and series apart (its film %s and its series %s are two different things): give type movie or series", provider, strings.TrimSpace(in.ID), strings.TrimSpace(in.ID))
			}
		default:
			return nil, lookupOut{}, fmt.Errorf("type must be movie or series, not %q", in.Type)
		}
		items, changed, err := client.ItemsByProviderID(ctx, provider, strings.TrimSpace(in.ID), types)
		if err != nil {
			return nil, lookupOut{}, err
		}

		return nil, lookupOut{Found: len(items) > 0, Items: summariseAll(items), Note: changed}, nil
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
		// both servers answer an id they do not hold with nothing similar; a
		// version's own id is one they hold
		if _, err := itemOrVersion(ctx, client, in.ID); err != nil {
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
		Refreshed       string   `json:"refreshed"`
		Landed          bool     `json:"landed"           jsonschema:"true once the refresh was seen to save the item; false when it had not within about a minute (queued behind a scan, say): it still runs, and undoes an edit made before it does"`
		Before          identity `json:"before"           jsonschema:"the item's name, year and ids before the refresh"`
		After           identity `json:"after"            jsonschema:"the same, read once the refresh landed or the wait ended"`
		IdentityChanged bool     `json:"identity_changed" jsonschema:"a tmdb, imdb or tvdb id the item held is gone or different after: an nfo read again can name another title"`
		besideMedia
		Note string `json:"note,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "item_refresh",
		Description: "Ask the server to re-fetch metadata and images for one item, filling what is missing (replace_all replaces everything), and wait for the refresh to land: the server runs it a moment after it is asked, and an edit made before it has run is undone by it. landed says whether it was seen to save the item within about a minute; a series' or season's episodes are refreshed after it. " +
			"A refresh reads the nfo beside the file again: on Emby that puts back the ids the nfo names over a match made since, and the watch state moves with the ids, since Emby keeps it by provider id. replace_all replaces every field and every image, hand edits included, and on Emby deletes the poster.jpg beside the media in favour of the provider's image; no tool puts back either. " + besideMediaSaid + ". " +
			"A library with its metadata fetchers off has nothing to fetch: there a refresh re-reads the files and nfo sidecars, and replace_all is refused. The answer gives the name, year and ids before and after, and says when they changed.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in refreshIn) (*mcp.CallToolResult, refreshOut, error) {
		// read first: an id neither server holds is answered with a bare 400 or 500
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
		if in.ReplaceAll && folder != nil && folder.FetchersOff(it.Type) {
			return nil, refreshOut{}, fmt.Errorf("the %s library has its metadata fetchers off, so replace_all has nothing to fetch and would clear %s's metadata: refresh without replace_all to re-read its files and nfo, or set fields with item_edit", folder.Name, it.Name)
		}
		dir, had, err := filesBeside(ctx, client, it)
		if err != nil {
			return nil, refreshOut{}, fmt.Errorf("could not read the folder beside %s's media, to say what the refresh removes from it, so nothing was changed: %w", it.Name, err)
		}
		landed, err := client.RefreshItem(ctx, in.ID, in.ReplaceAll)
		if err != nil {
			return nil, refreshOut{}, err
		}
		out := refreshOut{Refreshed: in.ID, Landed: landed, Before: identityOf(it)}
		now, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, refreshOut{}, fmt.Errorf("the refresh of %s was asked for (landed: %v), but reading the item back failed: %w; before it, it was %s", it.Name, landed, err, out.Before)
		}
		if out.besideMedia, err = besideAfter(ctx, client, dir, had); err != nil {
			return nil, refreshOut{}, fmt.Errorf("the refresh of %s was asked for (landed: %v), but reading the folder beside its media back failed, so what it removed there is not known: %w", it.Name, landed, err)
		}
		out.After = identityOf(now)
		out.IdentityChanged = !out.Before.sameTitle(out.After)
		var notes []string
		if !landed {
			notes = append(notes, "the refresh was asked for and not seen to save the item within about a minute: it is still queued (a scan or other refreshes ahead of it) and runs later, undoing any edit made to the item before it does, and the folder beside its media was read back before it ran, so what it removes there then is not in removed_beside_media")
		}
		if len(out.RemovedBeside) > 0 {
			notes = append(notes, "the refresh deleted "+strings.Join(out.RemovedBeside, ", ")+" from beside the media")
		}
		if nfo := nfoUnseen(folder); nfo != "" {
			notes = append(notes, nfo)
		}
		switch {
		case out.IdentityChanged:
			notes = append(notes, fmt.Sprintf("the refresh changed which title the item is: it was %s and is now %s", out.Before, out.After))
			if client.Backend() == embyfin.Emby {
				notes = append(notes, "Emby keeps watch state by provider id, so the item now shows each user's state for the ids it holds")
			}
		case out.Before.Name != out.After.Name || out.Before.Year != out.After.Year:
			notes = append(notes, fmt.Sprintf("the refresh changed the item's name or year: it was %s and is now %s", out.Before, out.After))
		}
		out.Note = strings.Join(notes, "; ")

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
		ID   string `json:"id"             jsonschema:"the library item id: a film, an episode or a track, or a series, a season or an album, whose episodes or tracks are read"`
		Days int    `json:"days,omitempty" jsonschema:"how many days back to search, default 60 or as many as the server keeps if fewer"`
	}
	type historyOut struct {
		Item     string   `json:"item"`
		Days     int      `json:"days"             jsonschema:"the period read, in days back from now"`
		Covers   *int     `json:"covers,omitempty" jsonschema:"for a series, a season or an album: how many of its episodes or tracks the log was read for, as the library holds them now"`
		Entries  []string `json:"entries"          jsonschema:"activity log lines about this item, or about its episodes or tracks, newest first"`
		Complete bool     `json:"complete"         jsonschema:"false when not all of the period could be read: it holds more activity than one call reads, or it reaches back past what the server keeps of its activity log. The answer then covers only the newest part of it, and note says how far back"`
		Note     string   `json:"note,omitempty"   jsonschema:"what of the period could not be read, and why"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "item_watch_history",
		Description: "Playback events for one item from the server activity log (who played it, when): the last 60 days, or as many as the server keeps if fewer (Jellyfin deletes activity older than its retention, 30 days out of the box). " +
			"Plays are logged against what was played, so a series, a season or an album is read as its episodes or tracks, as the library holds them now: a play of one since deleted is not found. A collection, a playlist, an artist or a folder is refused rather than answered with no plays.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, historyOut, error) {
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, historyOut{}, err
		}
		// what the log names a play of this by: the item itself, or what
		// it holds. A series' id is in no entry, and it used to answer no
		// plays at all, complete
		held, err := playedAs(ctx, client, it)
		if err != nil {
			return nil, historyOut{}, err
		}
		played := map[string]bool{in.ID: true}
		for _, id := range held {
			played[id] = true
		}

		window, err := readWindow(ctx, client, in.Days)
		if err != nil {
			return nil, historyOut{}, err
		}
		activity, err := readActivity(ctx, client, window.cutoff)
		if err != nil {
			return nil, historyOut{}, err
		}

		out := historyOut{Item: it.Name, Days: window.days, Entries: []string{}, Complete: activity.complete && !window.short, Note: joinNotes(window.note, activity.note())}
		if held != nil {
			out.Covers = new(len(held))
		}
		for _, e := range activity.entries {
			// by id when the entry names its item: a title is a substring of
			// others ("Dune" of "Dune: Part Two")
			match := played[e.ItemID]
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
		ID        string `json:"id"                   jsonschema:"the library item id: one film, episode or song, or a series, season, collection or other folder, whose watched mark then goes on every item under it"`
		User      string `json:"user,omitempty"       jsonschema:"user name or id; defaults to the first administrator"`
		Watched   *bool  `json:"watched,omitempty"    jsonschema:"true marks played; false marks unplayed, which clears the play count, last played date and resume point for good"`
		Favourite *bool  `json:"favourite,omitempty"  jsonschema:"true favourites, false unfavourites: the item's own, not what is under it"`
		PositionS *int   `json:"position_s,omitempty" jsonschema:"where playback resumes from, in seconds from the start, above zero: the item shows under continue watching from there and is marked not yet watched. One item's own; refused on a folder"`
	}
	type setStateOut struct {
		Item        string `json:"item"`
		ID          string `json:"id"`
		Type        string `json:"type"`
		User        string `json:"user"`
		Watched     *bool  `json:"watched,omitempty"`
		Favourite   *bool  `json:"favourite,omitempty"`
		PositionS   *int   `json:"position_s,omitempty"`
		Reaches     int    `json:"reaches"                jsonschema:"items the change reached: the item, and for a watched mark on a folder every item under it as the user sees it"`
		StoredUnder int    `json:"stored_under,omitempty" jsonschema:"for a watched mark on a folder, how many items the server stores under it, which the mark reaches, when that is not the rows the user's view shows: Emby shows a film's copies in one library as one row, which shares their state"`

		Was          []stateRow `json:"was"                      jsonschema:"every reached item's state before the change, which no tool puts back"`
		ItemsChanged int        `json:"items_changed"            jsonschema:"of the items reached, how many read differently after the change"`
		Copies       []stateRow `json:"copies_changed,omitempty" jsonschema:"on Emby, the item's other copies - another version of a film, the same film in another library, the same episode in another folder of the show - and for a watched mark on a folder the items elsewhere carrying the ids of items under it, and the items under it the user's view leaves out (a copy folded into another's row, or one hidden from the user), each read before and after: the ones whose state changed, as they read after"`
		Note         string     `json:"note,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "item_set_state",
		Description: "Set a user's state on an item: watched or not, favourite or not, and where it resumes from, any or all in one call, for a user who can see it. " +
			"A series, season, collection or other folder given as the item has watched set on every item under it - every episode of a series, every film in a collection - while favourite is the item's own, and position_s is refused on one. " +
			"watched false clears each item's play count, last played date and resume point for good - on a series or a library, every episode's or film's under it, in the one call - and no tool can restore them, so was in the answer lists what every one was. watched true on an unwatched item counts one play, sets last played to now and clears its resume point; on one already watched it changes nothing. " +
			"A watched mark on a folder holding more than " + stateCapSaid + " items is refused with the count, before anything is changed: mark a series or a season at a time. " +
			"Emby keeps watch state and favourites by provider id - a film's by its own ids, an episode's by its series' ids and its number - so there every other copy of the title is marked with it: another version of a film, the same film in another library, the same episode in another folder of the show; a mark on a folder marks the copies of what is under it too, which are read before and after where they can be found cheaply (films, and the episodes of up to 20 shows). Jellyfin keeps each copy's own. user_next_up lists the resume points. " +
			"A folder's stored items can be more than the rows a user's view shows: Emby shows a film's copies in one library as one row, and a user limited by rating, tag or folder does not see everything, while a mark in their name can reach what they cannot see (seen on Emby under a library, a series, a season or a playlist, not a collection; on Jellyfin under a library, a series, a collection or a playlist, not a season). On Emby the items a view leaves out are read before and after, and the ones that changed answered; Jellyfin does not answer them in the user's name, and the answer says the mark can reach them. " +
			"A change made while a library scan runs can be lost when the scan saves the item (seen on Jellyfin 12.1: a favourite set during a scan came back unset), and the answer says when one was running. " +
			"It reads back what was asked for - favourite, resume point, watched mark, and each item a folder's mark reached - and is an error, naming each field that does not hold, when the server did not keep it. The answer says how many items the change reached, what every one was before, how many read differently after, and on Emby the copies that changed with it.",
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
		if in.PositionS != nil && it.IsFolder {
			return nil, setStateOut{}, fmt.Errorf("position_s is a place in one item's media, and %s is a %s, which holds items rather than being one: set it on an item under it", it.Name, it.Type)
		}
		var rows, stored int
		if in.Watched != nil && it.IsFolder {
			var cerr error
			if rows, stored, cerr = countUnder(ctx, client, user.ID, it.ID); cerr != nil {
				return nil, setStateOut{}, fmt.Errorf("could not count the items under %s, which a watched mark on it reaches, so nothing was changed: %w", it.Name, cerr)
			}
			if err := folderMarkRefused(it, max(rows, stored), *in.Watched); err != nil {
				return nil, setStateOut{}, err
			}
		}
		reach, err := reachOf(ctx, client, user.ID, it, in.Watched != nil)
		if err != nil {
			return nil, setStateOut{}, fmt.Errorf("could not read what %s's state on %s is now, so nothing was changed: %w", user.Name, it.Name, err)
		}
		out := setStateOut{Item: it.Name, ID: it.ID, Type: it.Type, User: user.Name, Watched: in.Watched, Favourite: in.Favourite, PositionS: in.PositionS, Reaches: len(reach.items), Was: reach.items}
		var notes []string
		// what the view leaves out of a folder: a copy folded into another's
		// row on Emby, or an item hidden from the user, which a mark in their
		// name can reach too (seen on Emby 4.10 and 4.11 under a library, a
		// series, a season and a playlist, not a collection; on Jellyfin 12.1
		// under a library, a series, a collection and a playlist, not a
		// season)
		unlimited, unshown := false, map[string]bool{}
		if stored != rows {
			folders, ferr := client.VirtualFolders(ctx)
			if ferr != nil {
				return nil, setStateOut{}, fmt.Errorf("could not read the libraries, to tell what of %s %s sees, so nothing was changed: %w", it.Name, user.Name, ferr)
			}
			unlimited = user.Unlimited(folders)
			out.StoredUnder = stored
			switch {
			case client.Backend() == embyfin.Jellyfin:
				// Jellyfin answers a hidden item in the user's name with a 404
				notes = append(notes, fmt.Sprintf("the server stores %d items under %s, and %s's view shows %d: Jellyfin's mark in their name can reach items their view leaves out - a film or an episode hidden by a rating or a tag was marked under a library, a series, a collection and a playlist, not under a season - and those cannot be read back in their name", stored, it.Name, user.Name, rows))
			case unlimited:
				notes = append(notes, fmt.Sprintf("the server stores %d items under %s, which %s's view shows as %d rows: the mark reaches all %d, a row standing for several copies changing them with it", stored, it.Name, user.Name, rows, stored))
			default:
				// Emby answers a hidden item in the user's name, so what the
				// mark did to each is read rather than said
				left, uerr := unshownUnder(ctx, client, it.ID, reach.items[1:])
				if uerr != nil {
					return nil, setStateOut{}, fmt.Errorf("could not read the items under %s that %s's view leaves out, which a mark in their name can reach, so nothing was changed: %w", it.Name, user.Name, uerr)
				}
				read := left[:min(len(left), copiesRead)]
				before, cerr := copyStates(ctx, client, user.ID, read)
				if cerr != nil {
					return nil, setStateOut{}, fmt.Errorf("could not read %s's state on the items under %s that their view leaves out, which a mark in their name can reach, so nothing was changed: %w", user.Name, it.Name, cerr)
				}
				reach.copies = append(reach.copies, before...)
				for _, id := range read {
					unshown[id] = true
				}
				notes = append(notes, fmt.Sprintf("the server stores %d items under %s, and %s's view shows %d rows: the %d items it leaves out - a copy folded into another's row, or an item %s cannot see, which a mark in their name reaches under some folders and not others - were read before and after, and the ones that changed are in copies_changed, and what they were in was", stored, it.Name, user.Name, rows, len(left), user.Name))
				if len(left) > len(read) {
					notes = append(notes, fmt.Sprintf("%d more items under it that the view leaves out were not read, and may have changed too", len(left)-len(read)))
				}
			}
		}
		if reach.under && client.Backend() == embyfin.Emby {
			elsewhere, complete, eerr := copiesElsewhere(ctx, client, it.ID)
			if eerr != nil {
				return nil, setStateOut{}, fmt.Errorf("could not read which items elsewhere share the ids of what is under %s (Emby keeps one state for them), so nothing was changed: %w", it.Name, eerr)
			}
			// read before and after, like the copies of a single item, and
			// the ones that changed answered
			read := elsewhere[:min(len(elsewhere), copiesRead)]
			before, cerr := copyStates(ctx, client, user.ID, idsOfRows(read))
			if cerr != nil {
				return nil, setStateOut{}, fmt.Errorf("could not read %s's state on the items elsewhere sharing the ids of what is under %s, so nothing was changed: %w", user.Name, it.Name, cerr)
			}
			reach.copies = append(reach.copies, before...)
			if len(elsewhere) > len(read) {
				notes = append(notes, fmt.Sprintf("%d more items elsewhere carry the ids of items under it, and change with it too; the first %d were read before and after", len(elsewhere)-len(read), copiesRead))
			}
			if !complete {
				notes = append(notes, "items elsewhere sharing the ids of what is under it - songs, or the episodes of more than 20 shows - change with it too, and were not looked for")
			}
		}
		// a scan saving the item as it read it can lose a change made meanwhile
		scanning, err := client.ScansRunning(ctx)
		if err != nil {
			return nil, setStateOut{}, fmt.Errorf("could not tell whether a library scan was running, which can undo the change, so nothing was changed: %w", err)
		}
		// what landed, for an error part way: each change is its own request
		var done []string
		landed := func(err error) error {
			if len(done) == 0 {
				return err
			}
			return fmt.Errorf("%w; already set before it failed: %s", err, strings.Join(done, ", "))
		}
		if in.Favourite != nil {
			if err := client.SetFavourite(ctx, user.ID, in.ID, *in.Favourite); err != nil {
				return nil, setStateOut{}, err
			}
			done = append(done, fmt.Sprintf("favourite %v", *in.Favourite))
		}
		if in.PositionS != nil {
			if err := client.SetProgress(ctx, user.ID, in.ID, int64(*in.PositionS)*ticksPerSecond); err != nil {
				return nil, setStateOut{}, landed(err)
			}
			done = append(done, fmt.Sprintf("position %d s", *in.PositionS))
		}
		// a resume point already marks the item not yet watched, and marking
		// it unplayed again would clear the point just set
		if in.Watched != nil && (in.PositionS == nil || *in.Watched) {
			if err := client.SetPlayed(ctx, user.ID, in.ID, *in.Watched); err != nil {
				return nil, setStateOut{}, landed(err)
			}
			done = append(done, fmt.Sprintf("watched %v", *in.Watched))
		}

		after, err := reach.readAgain(ctx, r, client, user.ID, it, stateAsked{watched: in.Watched, favourite: in.Favourite, positionS: in.PositionS})
		if err != nil {
			return nil, setStateOut{}, fmt.Errorf("set %s for %s on %s, but reading the state back failed: %w (it was, before: %s)", strings.Join(done, ", "), user.Name, it.Name, err, statesSaid(out.Was))
		}
		out.ItemsChanged = after.changed
		out.Copies = after.copies
		// the items the view leaves out that changed are reached items too,
		// and was says what every reached item was
		for _, c := range reach.copies {
			if unshown[c.ID] && slices.ContainsFunc(after.copies, func(n stateRow) bool { return n.ID == c.ID }) {
				out.Was = append(out.Was, c)
			}
		}
		later, err := client.ScansRunning(ctx)
		if err != nil {
			return nil, setStateOut{}, fmt.Errorf("set %s for %s on %s, but could not tell whether a library scan was running, which can undo it: %w", strings.Join(done, ", "), user.Name, it.Name, err)
		}
		running := slices.Compact(slices.Sorted(slices.Values(slices.Concat(scanning, later))))
		if len(after.unkept) > 0 {
			scan := "a library scan saving the item can undo a change"
			if len(running) > 0 {
				scan = strings.Join(running, " and ") + " was running, and saving the item can undo a change"
			}
			return nil, setStateOut{}, fmt.Errorf("sent %s for %s on %s, but read back for a few seconds, %s: the server did not keep it (%s). Before, it was: %s", strings.Join(done, ", "), user.Name, it.Name, strings.Join(after.unkept, "; "), scan, statesSaid(out.Was))
		}
		if in.Watched != nil && !*in.Watched && reach.under {
			cleared := fmt.Sprintf("the %d items under %s", len(reach.items)-1, it.Name)
			listed := "was lists what every one was"
			switch {
			case stored != rows && client.Backend() == embyfin.Jellyfin:
				cleared = fmt.Sprintf("the %d items %s sees under %s, and of any it reaches that their view leaves out,", len(reach.items)-1, user.Name, it.Name)
				listed = "was lists what every one they see was"
			case stored != rows && unlimited:
				cleared = fmt.Sprintf("the %d items stored under %s", stored, it.Name)
			case stored != rows:
				cleared = fmt.Sprintf("the %d rows %s sees under %s, and of the items in copies_changed,", len(reach.items)-1, user.Name, it.Name)
			}
			notes = append(notes, "the play counts, last played dates and resume points of "+cleared+" are cleared for good: no tool restores them, and "+listed)
		}
		if len(running) > 0 {
			notes = append(notes, strings.Join(running, " and ")+" was running: it may overwrite this change once it finishes; check it afterwards")
		}
		out.Note = strings.Join(notes, "; ")

		return nil, out, nil
	})

	type deleteIn struct {
		ID      string `json:"id"                jsonschema:"the library item id"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true to delete, and acknowledges the media FILES are permanently deleted from disk; without it the call is refused with what it would remove"`
	}
	type deleteOut struct {
		Deleted     string         `json:"deleted"`
		Removed     []removedPath  `json:"removed"                       jsonschema:"every file and folder the delete took off the server's disk, read before and after it: a film alone in its folder takes the whole folder (its nfo, artwork, subtitles, extras and every version), as a series, season, album or other folder item does; an episode, or a film sharing its folder, takes its files and every nfo, subtitle and image whose name begins with its file's name, another item's included"`
		ItemsUnder  int            `json:"items_under,omitempty"         jsonschema:"for a folder item - a series, season, album, artist or folder - how many items the server held under it, which went with it"`
		UnderByType map[string]int `json:"items_under_by_type,omitempty"`
		ListsLeft   []listRef      `json:"lists_left"                    jsonschema:"the playlists and collections that held the item, one of its versions or an item under it, which no longer do"`
		Note        string         `json:"note,omitempty"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name: "item_delete",
		Description: "PERMANENTLY delete an item AND its media from disk. Irreversible. The server takes more than the item's own file: a film alone in its folder goes with the whole folder (nfo, artwork, subtitles, extras, every version), and a series, season, album or other folder item with its folder and every item the server holds under it. " +
			"An episode, or a film sharing its folder, goes with every nfo, subtitle and image whose name begins with its file's name, ignoring case and whatever follows - another item's too: deleting Alien.mkv from a folder it shares with Aliens.mkv also deletes Aliens.nfo, Aliens' subtitles and its poster (never Aliens.mkv itself). Everything deleted leaves every playlist and collection that held it; a copy Emby shows as another version of the film, in a folder the delete does not take, stays, and so do its places in lists. " +
			"A collection, a playlist or a library is refused here: each has a delete of its own, which leaves the items it holds. So are a genre, a studio or a person, which are names items carry, and an artist, whose delete would take whatever folder the server counts as its own. " +
			"Without confirm=true it refuses, saying what it would remove - every file and folder, counted - which of those belong to another item, how many items a folder item holds, and the playlists and collections they would leave; when the item's folder cannot be read it says what is not known, and confirm deletes it all the same. " +
			"With confirm=true it deletes what is there when it runs, not what an earlier refusal listed, and the answer lists every path removed and the lists left.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, deleteOut{}, err
		}
		if other, ok := notForItemDelete[it.Type]; ok {
			return nil, deleteOut{}, fmt.Errorf("%s is a %s, which item_delete does not take: %s. Nothing was deleted", it.Name, it.Type, other)
		}
		// every version the server holds for it, which on Emby only the
		// item read in a user's view lists: not read, what the delete takes
		// would be understated (it takes the folder and every version)
		admin, err := client.ResolveUser(ctx, "")
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("could not read the account whose view lists %s's versions, so what the delete takes is not known and nothing was deleted: %w", it.Name, err)
		}
		full, err := client.UserItem(ctx, admin.ID, in.ID)
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("could not read the versions the server holds for %s, so what the delete takes is not known and nothing was deleted: %w", it.Name, err)
		}
		versions := []string{}
		for _, s := range full.MediaSources {
			versions = append(versions, s.Path)
		}
		plan, err := planDelete(ctx, client, it, versions)
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("nothing was deleted: %w", err)
		}
		// a version the server lists is another item only where its file is
		// one the delete takes: Emby lists a copy of the same film in another
		// folder as one of its versions, and deleting one left the other and
		// its places in lists as they were (seen on 4.11)
		reached := []string{in.ID}
		for _, s := range full.MediaSources {
			if s.ItemID != "" && s.ItemID != in.ID && plan.takes(s.Path) {
				reached = append(reached, s.ItemID)
			}
		}
		out := deleteOut{Deleted: it.Name + " (" + it.Path + ")"}
		var under []embyfin.Item
		if it.IsFolder {
			// what goes with it decides the delete: a read that cannot be
			// sure of it fails, and nothing is deleted
			read, readErr := client.ReadAll(ctx, embyfin.SearchOptions{ParentID: in.ID, ExcludeItemTypes: containerTypes, Fields: "Path"}, embyfin.ToAct, func(page []embyfin.Item) bool {
				under = append(under, page...)
				return true
			})
			if readErr != nil {
				return nil, deleteOut{}, fmt.Errorf("could not read the items under %s, which go with it, so nothing was deleted: %w", it.Name, readErr)
			}
			if changed := read.Changed(); changed != "" {
				return nil, deleteOut{}, fmt.Errorf("can't be sure of the items under %s, which go with it, so nothing was deleted (%s): ask again", it.Name, changed)
			}
			out.ItemsUnder, out.UnderByType = len(under), countByType(under)
			reached = append(reached, idsOf(under)...)
		}
		lists, err := readMemberships(ctx, client)
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("could not read the playlists and collections %s would leave, so nothing was deleted: %w", it.Name, err)
		}
		out.ListsLeft = lists.holding(reached)
		if !in.Confirm {
			// what it would remove last, since that runs longest
			var parts []string
			if it.IsFolder {
				parts = append(parts, fmt.Sprintf("%s is a %s, and goes whole: with it go the %d items the server holds under it (%s)", it.Name, it.Type, len(under), typeCounts(out.UnderByType)))
			}
			if len(out.ListsLeft) > 0 {
				parts = append(parts, "It would leave "+listsSaid(out.ListsLeft))
			} else {
				parts = append(parts, "No playlist or collection holds it")
			}
			if plan.unknown {
				parts = append(parts, "confirm=true deletes it all the same, taking whatever the server takes with it")
			}
			parts = append(parts, "confirm=true deletes what is there when it runs, which the answer lists", plan.would())
			return nil, deleteOut{}, fmt.Errorf("refusing to delete %s without confirm=true: nothing was deleted. %s", it.Name, strings.Join(parts, ". "))
		}

		// a scan that had read the item's folder before the delete landed
		// can list the item again once it finishes (seen on Jellyfin 12.1):
		// the files stay gone, and the next scan lets the record go
		scanning, err := client.ScansRunning(ctx)
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("could not tell whether a scan was running, so %s was not deleted: %w", it.Name, err)
		}
		if err := client.DeleteItem(ctx, in.ID); err != nil {
			return nil, deleteOut{}, r.afterFailedDelete(ctx, plan, it, err)
		}
		removed, note, err := r.removedBy(ctx, plan, it.Path)
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("deleted %s, but reading back what went failed: %w", it.Name, err)
		}
		if plan.unknown {
			note += "; " + r.ownFileNow(ctx, it.Path)
		}
		after, err := client.ScansRunning(ctx)
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("deleted %s (removed: %s), but could not tell whether a scan was running, which can list it again until the next scan: %w", it.Name, removedSaid(removed), err)
		}
		// a folder that could not be read before says so here: its note
		notes := []string{}
		if note != "" {
			notes = append(notes, note)
		}
		if running := slices.Compact(slices.Sorted(slices.Values(slices.Concat(scanning, after)))); len(running) > 0 {
			notes = append(notes, strings.Join(running, " and ")+" was running: it can list this item again once it finishes, pointing at files that are gone, until a later scan lets it go - the next one as a rule, though Jellyfin has kept such an item through one more; if it is still listed after two, delete it again")
		}
		out.Removed, out.Note = removed, strings.Join(notes, "; ")

		return nil, out, nil
	})
}
