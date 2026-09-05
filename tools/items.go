package tools

import (
	"context"
	"errors"
	"fmt"
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
	RuntimeMin          int               `json:"runtime_minutes,omitempty"`
	Path                string            `json:"path,omitempty"`
	MetadataProviderIDs map[string]string `json:"metadata_provider_ids,omitempty"`
	Video               string            `json:"video,omitempty"                 jsonschema:"codec, resolution and bitrate of the primary video stream"`
	Audio               []string          `json:"audio,omitempty"`
	Subtitles           []string          `json:"subtitles,omitempty"`
	Container           string            `json:"container,omitempty"`
	SizeMB              int64             `json:"size_mb,omitempty"`
	Added               string            `json:"added,omitempty"                 jsonschema:"when the item was added to the library"`
}

func summarise(it *embyfin.Item) itemSummary {
	s := itemSummary{
		ID:                  it.ID,
		Name:                it.Name,
		Type:                it.Type,
		Year:                it.ProductionYear,
		Series:              it.SeriesName,
		Season:              it.ParentIndexNumber,
		Episode:             it.IndexNumber,
		RuntimeMin:          it.RuntimeMinutes(),
		Path:                it.Path,
		MetadataProviderIDs: it.ProviderIDs,
		Added:               it.DateCreated,
	}
	if len(it.MediaSources) == 0 {
		return s
	}

	src := it.MediaSources[0]
	s.Container = src.Container
	s.SizeMB = src.Size / (1 << 20)

	for _, st := range src.MediaStreams {
		switch st.Type {
		case "Video":
			if s.Video == "" {
				s.Video = fmt.Sprintf("%s %dx%d @ %d kbps", st.Codec, st.Width, st.Height, st.BitRate/1000)
			}
		case "Audio":
			desc := st.Codec
			if st.Language != "" {
				desc = st.Language + " " + desc
			}
			if st.Channels > 0 {
				desc = fmt.Sprintf("%s %dch", desc, st.Channels)
			}
			s.Audio = append(s.Audio, desc)
		case "Subtitle":
			lang := st.Language
			if lang == "" {
				lang = "und"
			}
			if st.IsExternal {
				lang += " (external)"
			}
			s.Subtitles = append(s.Subtitles, lang)
		}
	}

	return s
}

func summariseAll(items []embyfin.Item) []itemSummary {
	out := make([]itemSummary, 0, len(items))
	for i := range items {
		out = append(out, summarise(&items[i]))
	}

	return out
}

func registerItemTools(server *mcp.Server, client *embyfin.Client, opts Options) {
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
		Overview string      `json:"overview,omitempty"`
		People   []personOut `json:"people,omitempty"   jsonschema:"directors, writers, and top-billed cast"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "item_get",
		Description: "Fetch one library item by id with full quality facts: video/audio/subtitle streams, container, size, runtime, path, metadata provider ids, overview, and people.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getItemIn) (*mcp.CallToolResult, getItemOut, error) {
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, getItemOut{}, err
		}

		out := getItemOut{itemSummary: summarise(it), Overview: it.Overview}
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
	mcp.AddTool(server, &mcp.Tool{
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
	}
	type similarOut struct {
		Items []itemSummary `json:"items"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "item_similar",
		Description: "Items in the library the server considers similar to the given one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in similarIn) (*mcp.CallToolResult, similarOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 10
		}

		items, err := client.Similar(ctx, in.ID, limit)
		if err != nil {
			return nil, similarOut{}, err
		}

		return nil, similarOut{Items: summariseAll(items)}, nil
	})

	type refreshIn struct {
		ID         string `json:"id"                    jsonschema:"the library item id"`
		ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"replace all existing metadata and images instead of filling gaps"`
	}
	type refreshOut struct {
		Refreshed string `json:"refreshed"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "item_refresh",
		Description: "Ask the server to re-fetch metadata and images for one item. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in refreshIn) (*mcp.CallToolResult, refreshOut, error) {
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
	mcp.AddTool(server, &mcp.Tool{
		Name:        "item_edit",
		Description: "Update an item's metadata fields (title, sort title, overview, year, genres, tags). Only provided fields change. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		admin, err := client.ResolveUser(ctx, "")
		if err != nil {
			return nil, editOut{}, err
		}

		full, err := client.FullItem(ctx, admin.ID, in.ID)
		if err != nil {
			return nil, editOut{}, err
		}

		var updated []string
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
		setField("Genres", in.Genres, len(in.Genres) > 0)
		setField("Tags", in.Tags, len(in.Tags) > 0)

		if len(updated) == 0 {
			return nil, editOut{}, errors.New("no fields to update were provided")
		}

		if err := client.UpdateItem(ctx, in.ID, full); err != nil {
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
	mcp.AddTool(server, &mcp.Tool{
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
		User        string `json:"user"`
		Played      bool   `json:"played"`
		PlayCount   int    `json:"play_count,omitempty"`
		LastPlayed  string `json:"last_played,omitempty"`
		ResumePoint int    `json:"resume_minutes,omitempty" jsonschema:"minutes into the item if partially watched"`
	}
	type lastWatchedOut struct {
		Item  string     `json:"item"`
		Users []watchRow `json:"users"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "item_last_watched",
		Description: "Per-user watch state for one item: played, play count, last played date, resume point.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lastWatchedIn) (*mcp.CallToolResult, lastWatchedOut, error) {
		users, err := client.Users(ctx)
		if err != nil {
			return nil, lastWatchedOut{}, err
		}

		out := lastWatchedOut{}
		for _, u := range users {
			items, _, err := client.Search(ctx, embyfin.SearchOptions{
				IDs: in.ID, UserID: u.ID, EnableUserData: true, Fields: embyfin.FieldsLean,
			})
			if err != nil {
				return nil, lastWatchedOut{}, err
			}
			if len(items) == 0 {
				continue
			}

			out.Item = items[0].Name
			if ud := items[0].UserData; ud != nil {
				out.Users = append(out.Users, watchRow{
					User:        u.Name,
					Played:      ud.Played,
					PlayCount:   ud.PlayCount,
					LastPlayed:  ud.LastPlayedDate,
					ResumePoint: int(ud.PlaybackPositionTicks / 600_000_000),
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
	mcp.AddTool(server, &mcp.Tool{
		Name:        "item_watch_history",
		Description: "Playback events for one item from the server activity log (who played it, when), default last 60 days.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, historyOut, error) {
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, historyOut{}, err
		}

		entries, _, err := client.ActivityLog(ctx, daysCutoff(in.Days), 1000)
		if err != nil {
			return nil, historyOut{}, err
		}

		out := historyOut{Item: it.Name}
		for _, e := range entries {
			if e.ItemID == in.ID || strings.Contains(e.Name, it.Name) || strings.Contains(e.ShortOverview, it.Name) {
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
	mcp.AddTool(server, &mcp.Tool{
		Name:        "item_set_watched",
		Description: "Mark an item played or unplayed for a user. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setWatchedIn) (*mcp.CallToolResult, setWatchedOut, error) {
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
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
	mcp.AddTool(server, &mcp.Tool{
		Name:        "item_set_favourite",
		Description: "Favourite or unfavourite an item for a user. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setFavouriteIn) (*mcp.CallToolResult, setFavouriteOut, error) {
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, setFavouriteOut{}, err
		}

		if err := client.SetFavourite(ctx, user.ID, in.ID, in.Favourite); err != nil {
			return nil, setFavouriteOut{}, err
		}

		return nil, setFavouriteOut{Item: in.ID, User: user.Name, Favourite: in.Favourite}, nil
	})

	if opts.EnableDelete {
		type deleteIn struct {
			ID      string `json:"id"      jsonschema:"the library item id"`
			Confirm bool   `json:"confirm" jsonschema:"must be true; acknowledges the media FILE is permanently deleted from disk"`
		}
		type deleteOut struct {
			Deleted string `json:"deleted"`
		}
		mcp.AddTool(server, &mcp.Tool{
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
}
