package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerUserTools(r *registry) {
	client := r.client
	type userRow struct {
		Name         string `json:"name"`
		ID           string `json:"id"`
		Admin        bool   `json:"admin,omitempty"`
		LastActivity string `json:"last_activity,omitempty"`
	}
	type usersOut struct {
		Users []userRow `json:"users"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_list",
		Description: "List the server's user accounts.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, usersOut, error) {
		users, err := client.Users(ctx)
		if err != nil {
			return nil, usersOut{}, err
		}

		out := usersOut{}
		for _, u := range users {
			out.Users = append(out.Users, userRow{
				Name:         u.Name,
				ID:           u.ID,
				Admin:        u.Policy.IsAdministrator,
				LastActivity: u.LastActivityDate,
			})
		}

		return nil, out, nil
	})

	type historyIn struct {
		User   string `json:"user,omitempty"   jsonschema:"user name or id; defaults to the first administrator"`
		Days   int    `json:"days,omitempty"   jsonschema:"how many days back, default 60 or as many as the server keeps if fewer"`
		Limit  int    `json:"limit,omitempty"  jsonschema:"page size, default 25"`
		Offset int    `json:"offset,omitempty" jsonschema:"skip this many, to page"`
	}
	type historyRow struct {
		itemSummary
		LastPlayed string `json:"last_played"`
		Event      string `json:"event"              jsonschema:"stop = finished or stopped playing, start = began playing (may still be in progress)"`
		Removed    bool   `json:"removed,omitempty"  jsonschema:"the item is no longer in the library (deleted, or added again under a new id since): only its id and the log's line about the play are known"`
		LogLine    string `json:"log_line,omitempty" jsonschema:"the activity log's line about the play, for an item no longer in the library"`
	}
	type historyOut struct {
		User     string       `json:"user"`
		Days     int          `json:"days"           jsonschema:"the period read, in days back from now"`
		Total    int          `json:"total"          jsonschema:"items played in the period, across every page, as far as the activity log was read - those since removed from the library included (removed on their rows)"`
		Offset   int          `json:"offset"`
		Items    []historyRow `json:"items"          jsonschema:"most recent first"`
		Complete bool         `json:"complete"       jsonschema:"false when not all of the period could be read: it holds more activity than one call reads, or it reaches back past what the server keeps of its activity log. The answer then covers only the newest part of it, and note says how far back"`
		Note     string       `json:"note,omitempty" jsonschema:"what of the period could not be read, and why"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "user_history",
		Description: "What a user has played recently (from the activity log), most recent first: the last 60 days, or as many as the server keeps if fewer (Jellyfin deletes activity older than its retention, 30 days out of the box). " +
			"A period asked for past what the server keeps comes back complete false, saying how far back can be seen. A play of an item since removed from the library is a row marked removed, with the log's line about it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, historyOut, error) {
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, historyOut{}, err
		}
		users, err := client.Users(ctx)
		if err != nil {
			return nil, historyOut{}, err
		}

		limit := in.Limit
		if limit <= 0 {
			limit = 25
		}

		// Emby's list endpoints omit LastPlayedDate from UserData (only the single-item
		// endpoint has it), so recent history comes from the activity log's playback
		// events, each put down to the user who played it (see playedBy).
		window, err := client.ActivityWindow(ctx, in.Days)
		if err != nil {
			return nil, historyOut{}, err
		}
		activity, err := client.ReadActivity(ctx, window.Cutoff)
		if err != nil {
			return nil, historyOut{}, err
		}

		jellyfin := client.Backend() == embyfin.Jellyfin
		lastEvent := map[string]embyfin.ActivityEntry{}
		var ids []string
		for i := range activity.Entries { // newest first
			e := activity.Entries[i]
			if _, ok := playbackEvent(e.Type); !ok || e.ItemID == "" || playedBy(&e, users, jellyfin) != user.ID {
				continue
			}
			if _, seen := lastEvent[e.ItemID]; seen {
				continue
			}
			lastEvent[e.ItemID] = e
			ids = append(ids, e.ItemID)
		}

		offset := max(in.Offset, 0)
		out := historyOut{
			User: user.Name, Days: window.Days, Total: len(ids), Offset: offset, Items: []historyRow{},
			Complete: activity.Complete && !window.Short, Note: joinNotes(window.Note, activity.Note()),
		}
		if offset >= len(ids) {
			return nil, out, nil
		}
		ids = ids[offset:min(offset+limit, len(ids))]

		// a batch at a time: a large limit named every id in one request,
		// which a server refuses past a few hundred (see idsPerRequest)
		byID := make(map[string]*embyfin.Item, len(ids))
		for batch := range slices.Chunk(ids, idsPerRequest) {
			items, _, err := client.Search(ctx, embyfin.SearchOptions{IDs: strings.Join(batch, ","), Limit: len(batch)})
			if err != nil {
				return nil, historyOut{}, err
			}
			for i := range items {
				byID[items[i].ID] = &items[i]
			}
		}
		for _, id := range ids {
			e := lastEvent[id]
			event, _ := playbackEvent(e.Type)
			row := historyRow{LastPlayed: e.Date, Event: event}
			if it := byID[id]; it != nil {
				row.itemSummary = summarise(it)
			} else {
				// removed from the library since: left out, it was counted in
				// total and missing from the page, with nothing to say why
				row.itemSummary = itemSummary{ID: id}
				row.Removed, row.LogLine = true, e.Name
			}
			out.Items = append(out.Items, row)
		}

		return nil, out, nil
	})

	type nextUpIn struct {
		User  string `json:"user,omitempty"  jsonschema:"user name or id; defaults to the first administrator"`
		Limit int    `json:"limit,omitempty" jsonschema:"maximum rows in each list, default 25"`
	}
	type nextUpOut struct {
		User           string          `json:"user"`
		NextUp         []itemSummary   `json:"next_up"                    jsonschema:"the next episode of each series the user is watching, at most limit"`
		NextUpMore     bool            `json:"next_up_more,omitempty"     jsonschema:"true when there are more series to go on with than limit: raise limit to see them"`
		InProgress     []inProgressRow `json:"in_progress"                jsonschema:"everything part way through (the continue watching row), most recently played first, with where each resumes; at most limit"`
		InProgressMore bool            `json:"in_progress_more,omitempty" jsonschema:"true when more is part way through than limit: raise limit to see it"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_next_up",
		Description: "What a user should watch next - the next episode of each series they are watching - and everything they are part way through (the continue watching row), with where each resumes and how far through it is. Each list stops at limit, and says so (next_up_more, in_progress_more) when there was more. item_set_state moves a resume point, or finishes or clears one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nextUpIn) (*mcp.CallToolResult, nextUpOut, error) {
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, nextUpOut{}, err
		}

		limit := in.Limit
		if limit <= 0 {
			limit = 25
		}

		// one more than the limit of each: cut at the limit with nothing to
		// say so, a full list read as everything there was
		nextUp, err := client.NextUp(ctx, user.ID, limit+1)
		if err != nil {
			return nil, nextUpOut{}, err
		}

		resume, err := client.Resume(ctx, user.ID, limit+1)
		if err != nil {
			return nil, nextUpOut{}, err
		}

		return nil, nextUpOut{
			User:           user.Name,
			NextUp:         summariseAll(nextUp[:min(len(nextUp), limit)]),
			NextUpMore:     len(nextUp) > limit,
			InProgress:     inProgressRows(resume[:min(len(resume), limit)]),
			InProgressMore: len(resume) > limit,
		}, nil
	})
}

// inProgressRow is a film or an episode part way through, with where it
// resumes.
type inProgressRow struct {
	itemSummary
	PositionS  int    `json:"position_s"            jsonschema:"where playback resumes, in seconds"`
	Percent    int    `json:"percent"               jsonschema:"how far through, capped at 100"`
	LastPlayed string `json:"last_played,omitempty"`
}

// inProgressRows are the rows for a user's resume list, in its order.
func inProgressRows(items []embyfin.Item) []inProgressRow {
	out := make([]inProgressRow, 0, len(items))
	for i := range items {
		seconds, percent := progressOf(&items[i])
		row := inProgressRow{itemSummary: summarise(&items[i]), PositionS: seconds, Percent: percent}
		if items[i].UserData != nil {
			row.LastPlayed = items[i].UserData.LastPlayedDate
		}
		out = append(out, row)
	}

	return out
}

// playbackEvent reads an activity entry's type as a playback start or stop:
// Emby types them playback.start and playback.stop, Jellyfin VideoPlayback
// and VideoPlaybackStopped (Audio... for music).
func playbackEvent(entryType string) (string, bool) {
	switch entryType {
	case "playback.start", "VideoPlayback", "AudioPlayback":
		return "start", true
	case "playback.stop", "VideoPlaybackStopped", "AudioPlaybackStopped":
		return "stop", true
	}

	return "", false
}

// playedBy is the id of the user an activity entry is about, or "" when it
// is none of users. Jellyfin's entries carry the user's id, which settles it.
// Emby's carry an internal numeric id that /Users does not expose, so there
// the entry's text is read instead - "<user> has finished playing ..." - and
// the longest user name it starts with wins: "Bob Smith is playing ..." is Bob
// Smith's, not Bob's.
func playedBy(e *embyfin.ActivityEntry, users []embyfin.User, byID bool) string {
	if id := idKey(e.UserID); byID && id != "" {
		for i := range users {
			if idKey(users[i].ID) == id {
				return users[i].ID
			}
		}

		return ""
	}

	best := -1
	for i := range users {
		if strings.HasPrefix(e.Name, users[i].Name+" ") && (best < 0 || len(users[i].Name) > len(users[best].Name)) {
			best = i
		}
	}
	if best < 0 {
		return ""
	}

	return users[best].ID
}

// idKey is a Jellyfin id to compare by, whether it was written with dashes or
// without, in either case; an entry about no user carries the empty id, which
// is no id at all.
func idKey(id string) string {
	k := strings.ToLower(strings.ReplaceAll(id, "-", ""))
	if strings.Trim(k, "0") == "" {
		return ""
	}

	return k
}

// playedAs is the ids besides its own that the activity log names a play of
// an item by: for what holds what is played - a series, a season, an album -
// the episodes or tracks it holds now, a list that is empty but not nil when
// it holds none; nil for anything played itself. What holds items of any
// other kind (a collection, a playlist, an artist, a folder) is refused: read
// by its own id it answered no plays, which is not an answer.
func playedAs(ctx context.Context, client *embyfin.Client, it *embyfin.Item) ([]string, error) {
	var held []embyfin.Item
	var err error
	switch it.Type {
	case "Series":
		held, err = client.Episodes(ctx, it.ID, embyfin.EpisodeOptions{Fields: "Path"})
	case "Season":
		held, err = client.Episodes(ctx, it.SeriesID, embyfin.EpisodeOptions{SeasonID: it.ID, Fields: "Path"})
	case "MusicAlbum":
		// a track left out would read as never played: a read that cannot
		// be sure of them fails
		var read embyfin.ReadResult
		read, err = client.ReadAll(ctx, embyfin.SearchOptions{ParentID: it.ID, IncludeItemTypes: "Audio", Fields: "Path"}, embyfin.ToAnswer, func(items []embyfin.Item) bool {
			held = append(held, items...)

			return true
		})
		if changed := read.Changed(); err == nil && changed != "" {
			err = fmt.Errorf("can't be sure of the tracks of %s: %s", it.Name, changed)
		}
	case "BoxSet", "Playlist", "Folder", "CollectionFolder", "MusicArtist", "PhotoAlbum", "Person", "Genre", "MusicGenre", "Studio", "UserView", "Channel":
		return nil, fmt.Errorf("%s is %s: the activity log names a play by what was played - a film, an episode, a track - so ask about one of those, or about a series, a season or an album, whose episodes or tracks are read", it.ID, describeItem(it))
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(held))
	for i := range held {
		ids = append(ids, held[i].ID)
	}

	return ids, nil
}
