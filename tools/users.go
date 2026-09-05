package tools

import (
	"context"

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
	mcp.AddTool(server, &mcp.Tool{
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
		LastPlayed string `json:"last_played,omitempty"`
		PlayCount  int    `json:"play_count,omitempty"`
	}
	type historyOut struct {
		User    string       `json:"user"`
		Watched []historyRow `json:"watched" jsonschema:"most recently played first"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "user_history",
		Description: "What a user has watched recently, most recent first, default last 60 days.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, historyOut, error) {
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, historyOut{}, err
		}

		limit := in.Limit
		if limit <= 0 {
			limit = 25
		}

		items, _, err := client.Search(ctx, embyfin.SearchOptions{
			IncludeItemTypes: "Movie,Episode",
			Filters:          "IsPlayed",
			SortBy:           "DatePlayed",
			SortOrder:        sortDescending,
			UserID:           user.ID,
			EnableUserData:   true,
			Limit:            limit,
		})
		if err != nil {
			return nil, historyOut{}, err
		}

		cutoff := daysCutoff(in.Days)
		out := historyOut{User: user.Name}
		for i := range items {
			ud := items[i].UserData
			if ud == nil || !afterCutoff(ud.LastPlayedDate, cutoff) {
				continue
			}
			out.Watched = append(out.Watched, historyRow{
				itemSummary: summarise(&items[i]),
				LastPlayed:  ud.LastPlayedDate,
				PlayCount:   ud.PlayCount,
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
	mcp.AddTool(server, &mcp.Tool{
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
	mcp.AddTool(server, &mcp.Tool{
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
