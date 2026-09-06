package tools

import (
	"context"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerUserTools(server *mcp.Server, client *embyfin.Client) {
	type userRow struct {
		Name         string `json:"name"`
		ID           string `json:"id"`
		Admin        bool   `json:"admin,omitempty"`
		LastActivity string `json:"last_activity,omitempty"`
	}
	type usersOut struct {
		Users []userRow `json:"users"`
	}
	addTool(server, &mcp.Tool{
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
		User  string `json:"user,omitempty"  jsonschema:"user name or id; defaults to the first administrator"`
		Days  int    `json:"days,omitempty"  jsonschema:"how many days back, default 60"`
		Limit int    `json:"limit,omitempty" jsonschema:"maximum items, default 25"`
	}
	type historyRow struct {
		itemSummary
		LastPlayed string `json:"last_played"`
		Event      string `json:"event"       jsonschema:"stop = finished or stopped playing, start = began playing (may still be in progress)"`
	}
	type historyOut struct {
		User    string       `json:"user"`
		Watched []historyRow `json:"watched" jsonschema:"most recently played first"`
	}
	addTool(server, &mcp.Tool{
		Name:        "user_history",
		Description: "What a user has played recently (from the activity log), most recent first, default last 60 days.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, historyOut, error) {
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, historyOut{}, err
		}

		limit := in.Limit
		if limit <= 0 {
			limit = 25
		}

		// Emby's list endpoints omit LastPlayedDate from UserData (only the single-item
		// endpoint has it), so recent history comes from the activity log's playback
		// events. Entries carry Emby's internal numeric user id, which /Users does not
		// expose, so match on the "<user> has finished playing ..." text instead.
		entries, _, err := client.ActivityLog(ctx, daysCutoff(in.Days), activityScanLimit)
		if err != nil {
			return nil, historyOut{}, err
		}

		prefix := user.Name + " "
		lastEvent := map[string]embyfin.ActivityEntry{}
		var ids []string
		for _, e := range entries { // newest first
			if !strings.HasPrefix(e.Type, "playback.") || e.ItemID == "" || !strings.HasPrefix(e.Name, prefix) {
				continue
			}
			if _, seen := lastEvent[e.ItemID]; seen {
				continue
			}
			lastEvent[e.ItemID] = e
			ids = append(ids, e.ItemID)
			if len(ids) >= limit {
				break
			}
		}

		out := historyOut{User: user.Name, Watched: []historyRow{}}
		if len(ids) == 0 {
			return nil, out, nil
		}

		items, _, err := client.Search(ctx, embyfin.SearchOptions{IDs: strings.Join(ids, ","), Limit: len(ids)})
		if err != nil {
			return nil, historyOut{}, err
		}
		byID := make(map[string]*embyfin.Item, len(items))
		for i := range items {
			byID[items[i].ID] = &items[i]
		}
		for _, id := range ids {
			it := byID[id]
			if it == nil { // removed from the library since
				continue
			}
			e := lastEvent[id]
			out.Watched = append(out.Watched, historyRow{
				itemSummary: summarise(it),
				LastPlayed:  e.Date,
				Event:       strings.TrimPrefix(e.Type, "playback."),
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
	addTool(server, &mcp.Tool{
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

	type favouritesIn struct {
		User  string `json:"user,omitempty"  jsonschema:"user name or id; defaults to the first administrator"`
		Limit int    `json:"limit,omitempty" jsonschema:"maximum items, default 50"`
	}
	type favouritesOut struct {
		User       string        `json:"user"`
		Favourites []itemSummary `json:"favourites"`
	}
	addTool(server, &mcp.Tool{
		Name:        "user_favourites",
		Description: "A user's favourite items.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in favouritesIn) (*mcp.CallToolResult, favouritesOut, error) {
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, favouritesOut{}, err
		}

		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}

		items, _, err := client.Search(ctx, embyfin.SearchOptions{
			Filters:        "IsFavorite",
			UserID:         user.ID,
			EnableUserData: true,
			Limit:          limit,
		})
		if err != nil {
			return nil, favouritesOut{}, err
		}

		return nil, favouritesOut{User: user.Name, Favourites: summariseAll(items)}, nil
	})
}
