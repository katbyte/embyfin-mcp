package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
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
		Days   int    `json:"days,omitempty"   jsonschema:"how many days back, default 60"`
		Limit  int    `json:"limit,omitempty"  jsonschema:"page size, default 25"`
		Offset int    `json:"offset,omitempty" jsonschema:"skip this many, to page"`
	}
	type historyRow struct {
		itemSummary
		LastPlayed string `json:"last_played"`
		Event      string `json:"event"       jsonschema:"stop = finished or stopped playing, start = began playing (may still be in progress)"`
	}
	type historyOut struct {
		User     string       `json:"user"`
		Total    int          `json:"total"          jsonschema:"items played in the period, across every page, as far as the activity log was read"`
		Offset   int          `json:"offset"`
		Items    []historyRow `json:"items"          jsonschema:"most recent first"`
		Complete bool         `json:"complete"       jsonschema:"false when the period holds more activity than one call reads: the answer then covers only the newest part of it, and note says how far back"`
		Note     string       `json:"note,omitempty" jsonschema:"how far back the activity log was read, when that is short of the period"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_history",
		Description: "What a user has played recently (from the activity log), most recent first, default last 60 days.",
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
		activity, err := readActivity(ctx, client, daysCutoff(in.Days))
		if err != nil {
			return nil, historyOut{}, err
		}

		jellyfin := client.Backend() == embyfin.Jellyfin
		lastEvent := map[string]embyfin.ActivityEntry{}
		var ids []string
		for i := range activity.entries { // newest first
			e := activity.entries[i]
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
		out := historyOut{User: user.Name, Total: len(ids), Offset: offset, Items: []historyRow{}, Complete: activity.complete, Note: activity.note()}
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
			it := byID[id]
			if it == nil { // removed from the library since
				continue
			}
			e := lastEvent[id]
			event, _ := playbackEvent(e.Type)
			out.Items = append(out.Items, historyRow{
				itemSummary: summarise(it),
				LastPlayed:  e.Date,
				Event:       event,
			})
		}

		return nil, out, nil
	})

	type nextUpIn struct {
		User  string `json:"user,omitempty"  jsonschema:"user name or id; defaults to the first administrator"`
		Limit int    `json:"limit,omitempty" jsonschema:"maximum items, default 15"`
	}
	type nextUpOut struct {
		User   string        `json:"user"`
		NextUp []itemSummary `json:"next_up" jsonschema:"next unwatched episode per series"`
		Resume []itemSummary `json:"resume"  jsonschema:"partially watched items"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_next_up",
		Description: "What a user should continue watching: next episodes per series, plus partially-watched items.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nextUpIn) (*mcp.CallToolResult, nextUpOut, error) {
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, nextUpOut{}, err
		}

		limit := in.Limit
		if limit <= 0 {
			limit = 15
		}

		nextUp, err := client.NextUp(ctx, user.ID, limit)
		if err != nil {
			return nil, nextUpOut{}, err
		}

		resume, err := client.Resume(ctx, user.ID, limit)
		if err != nil {
			return nil, nextUpOut{}, err
		}

		return nil, nextUpOut{
			User:   user.Name,
			NextUp: summariseAll(nextUp),
			Resume: summariseAll(resume),
		}, nil
	})
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

// activityRead is the activity log over a period, newest first.
type activityRead struct {
	entries []embyfin.ActivityEntry
	// total is how many entries the log holds in the period
	total int
	// complete is false when the read stopped short of the period's start:
	// at activityScanMax, or where the server's pages ran out before its
	// count did
	complete bool
}

// readActivity reads the activity log since cutoff, a page at a time, until
// it has every entry the server holds in the period or activityScanMax of
// them. One page used to be all a history tool read: a thousand entries is a
// few days of a busy server, so a film played a month ago was reported as
// never played inside the default 60 days.
func readActivity(ctx context.Context, client *embyfin.Client, cutoff time.Time) (activityRead, error) {
	var out activityRead
	for len(out.entries) < activityScanMax {
		entries, total, err := client.ActivityLog(ctx, cutoff, min(activityPage, activityScanMax-len(out.entries)), len(out.entries))
		if err != nil {
			return activityRead{}, err
		}
		out.entries = append(out.entries, entries...)
		out.total = total
		if len(entries) == 0 || len(out.entries) >= total {
			out.complete = len(out.entries) >= total

			return out, nil
		}
	}

	return out, nil
}

// note says how far back a read cut short got, and nothing for a whole one.
func (a activityRead) note() string {
	if a.complete || len(a.entries) == 0 {
		return ""
	}

	return fmt.Sprintf("the activity log holds %d entries in the period and the newest %d were read, back to %s: nothing older was seen, so ask for fewer days to see all of a shorter period",
		a.total, len(a.entries), a.entries[len(a.entries)-1].Date)
}
