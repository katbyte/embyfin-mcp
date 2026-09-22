package tools

import (
	"context"
	"math"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// userRef names a user; empty is the first administrator.
type userRef struct {
	User string `json:"user,omitempty" jsonschema:"user name or id; defaults to the first administrator"`
}

// titles counts items once per title. Copies of one film (or series, or
// episode) in several places share their metadata provider ids, and Emby
// keeps watch state and favourites by those ids, marking every copy at once,
// where Jellyfin marks the one copy; counted by title, a film watched is one
// film on both servers however many copies there are. An item with no
// provider id is its own title.
type titles map[string]bool

// titleKeys are the keys an item is known by: its type with each provider id.
func titleKeys(it *embyfin.Item) []string {
	var keys []string
	for k, v := range it.ProviderIDs {
		if p := strings.ToLower(k); v != "" && (p == "tmdb" || p == "imdb" || p == "tvdb") {
			keys = append(keys, it.Type+"/"+p+"/"+v)
		}
	}
	if len(keys) == 0 {
		keys = []string{it.Type + "/id/" + it.ID}
	}

	return keys
}

// has reports whether the item is a title already counted.
func (t titles) has(it *embyfin.Item) bool {
	return slices.ContainsFunc(titleKeys(it), func(k string) bool { return t[k] })
}

// add counts the item's title and reports whether it was not counted before.
func (t titles) add(it *embyfin.Item) bool {
	counted := t.has(it)
	for _, k := range titleKeys(it) {
		t[k] = true
	}

	return !counted
}

// titleCount is how many titles of a type a user's view holds under a filter
// (IsPlayed, IsFavorite...).
func titleCount(ctx context.Context, client *embyfin.Client, userID, types, filter string) (int, error) {
	seen, n := titles{}, 0
	err := client.SearchAll(ctx, embyfin.SearchOptions{
		IncludeItemTypes: types, Filters: filter, UserID: userID, EnableUserData: true, Fields: "Path,ProviderIds",
	}, func(items []embyfin.Item) bool {
		for i := range items {
			if seen.add(&items[i]) {
				n++
			}
		}
		return true
	})

	return n, err
}

func registerUserDetailTools(r *registry) {
	client := r.client

	type getOut struct {
		Name              string   `json:"name"`
		ID                string   `json:"id"`
		Admin             bool     `json:"admin"`
		Disabled          bool     `json:"disabled"`
		Hidden            bool     `json:"hidden"                        jsonschema:"left off the login screen"`
		HasPassword       bool     `json:"has_password"`
		CanDelete         bool     `json:"can_delete"                    jsonschema:"may delete media from the server"`
		RemoteAccess      bool     `json:"remote_access"                 jsonschema:"may connect from outside the local network"`
		Libraries         []string `json:"libraries"                     jsonschema:"the libraries the account can see; every library when all_libraries"`
		AllLibraries      bool     `json:"all_libraries"`
		MaxParentalRating int      `json:"max_parental_rating,omitempty" jsonschema:"the server's rating value above which items are hidden; absent when nothing is"`
		LastLogin         string   `json:"last_login,omitempty"`
		LastActivity      string   `json:"last_activity,omitempty"`
		MoviesWatched     int      `json:"movies_watched"                jsonschema:"films watched, each once however many copies the server holds"`
		EpisodesWatched   int      `json:"episodes_watched"`
		InProgress        int      `json:"in_progress"`
		Favourites        int      `json:"favourites"`
		// how the account wants playback, which decides whether a file whose
		// first audio track is in another language plays right for it
		AudioLanguage         string `json:"audio_language,omitempty"    jsonschema:"preferred audio language (ISO 639-2, e.g. eng); absent means the server's default"`
		SubtitleLanguage      string `json:"subtitle_language,omitempty" jsonschema:"preferred subtitle language; absent means the server's default"`
		SubtitleMode          string `json:"subtitle_mode,omitempty"     jsonschema:"when subtitles show: Default, Always, OnlyForced, None, Smart (or HearingImpaired on Emby)"`
		PlayDefaultAudioTrack bool   `json:"play_default_audio_track"    jsonschema:"true plays the file's default track whatever its language, rather than the one in the preferred language"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_get",
		Description: "One account in depth: whether it is an administrator, disabled or hidden, what it may do, which libraries it sees, when it last logged in, its playback preferences (audio and subtitle language, subtitle mode), and how much it has watched, has in progress and has favourited (a film with copies in several places counted once).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in userRef) (*mcp.CallToolResult, getOut, error) {
		u, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, getOut{}, err
		}
		p := u.Policy
		out := getOut{
			Name: u.Name, ID: u.ID, Admin: p.IsAdministrator, Disabled: p.IsDisabled, Hidden: p.IsHidden,
			HasPassword: u.HasPassword, CanDelete: p.EnableContentDeletion, RemoteAccess: p.EnableRemoteAccess,
			AllLibraries: p.EnableAllFolders, MaxParentalRating: p.MaxParentalRating,
			LastLogin: u.LastLoginDate, LastActivity: u.LastActivityDate, Libraries: []string{},
			AudioLanguage: u.Preferences.AudioLanguage, SubtitleLanguage: u.Preferences.SubtitleLanguage,
			SubtitleMode: u.Preferences.SubtitleMode, PlayDefaultAudioTrack: u.Preferences.PlayDefaultAudioTrack,
		}

		folders, err := client.VirtualFolders(ctx)
		if err != nil {
			return nil, getOut{}, err
		}
		for i := range folders {
			if u.CanSee(&folders[i]) {
				out.Libraries = append(out.Libraries, folders[i].Name)
			}
		}

		for _, c := range []struct {
			dst           *int
			types, filter string
		}{
			{&out.MoviesWatched, typeMovie, "IsPlayed"},
			{&out.EpisodesWatched, "Episode", "IsPlayed"},
			{&out.InProgress, "Movie,Episode", "IsResumable"},
			{&out.Favourites, "", "IsFavorite"},
		} {
			if *c.dst, err = titleCount(ctx, client, u.ID, c.types, c.filter); err != nil {
				return nil, getOut{}, err
			}
		}

		return nil, out, nil
	})

	type inProgressIn struct {
		userRef
		Limit int `json:"limit,omitempty" jsonschema:"maximum items, default 25"`
	}
	type inProgressRow struct {
		itemSummary
		PositionS  int    `json:"position_s"            jsonschema:"where playback resumes, in seconds"`
		Percent    int    `json:"percent"               jsonschema:"how far through, capped at 100"`
		LastPlayed string `json:"last_played,omitempty"`
	}
	type inProgressOut struct {
		User  string          `json:"user"`
		Items []inProgressRow `json:"items" jsonschema:"most recently played first"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_in_progress",
		Description: "What a user is part way through (the continue watching row): each film and episode with where it resumes and how far through it is. item_set_progress moves a resume point; item_set_watched finishes or clears one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in inProgressIn) (*mcp.CallToolResult, inProgressOut, error) {
		u, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, inProgressOut{}, err
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 25
		}
		items, err := client.Resume(ctx, u.ID, limit)
		if err != nil {
			return nil, inProgressOut{}, err
		}

		out := inProgressOut{User: u.Name, Items: []inProgressRow{}}
		for i := range items {
			seconds, percent := progressOf(&items[i])
			row := inProgressRow{itemSummary: summarise(&items[i]), PositionS: seconds, Percent: percent}
			if items[i].UserData != nil {
				row.LastPlayed = items[i].UserData.LastPlayedDate
			}
			out.Items = append(out.Items, row)
		}

		return nil, out, nil
	})

	type statsIn struct {
		userRef
		Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
	}
	type seriesRow struct {
		Name     string `json:"name"`
		Watched  int    `json:"episodes_watched"`
		Finished bool   `json:"finished"         jsonschema:"every episode the library holds is watched"`
	}
	type playsRow struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Plays int    `json:"plays"`
	}
	type statsOut struct {
		User            string       `json:"user"`
		MoviesWatched   int          `json:"movies_watched"`
		EpisodesWatched int          `json:"episodes_watched"`
		HoursWatched    float64      `json:"hours_watched"    jsonschema:"the runtime of everything watched, once each"`
		SeriesStarted   int          `json:"series_started"`
		SeriesFinished  int          `json:"series_finished"`
		TopGenres       []valueCount `json:"top_genres"       jsonschema:"across the films and series watched, a series once"`
		TopSeries       []seriesRow  `json:"top_series"       jsonschema:"most episodes watched, top 10"`
		MostPlayed      []playsRow   `json:"most_played"      jsonschema:"highest play counts, top 10 (Emby does not count a mark as watched as a play)"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_stats",
		Description: "A user's watching in numbers, from their watch state across the library: films and episodes watched, hours, series started and finished, their top genres and series, and what they have played most. A film with copies in several places counts once.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in statsIn) (*mcp.CallToolResult, statsOut, error) {
		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, statsOut{}, err
		}
		u, err := userInLibrary(ctx, client, in.User, folder)
		if err != nil {
			return nil, statsOut{}, err
		}
		opts, err := sweepOptions(ctx, client, in.Library, "", "Movie,Episode", "Path,Genres,ProviderIds")
		if err != nil {
			return nil, statsOut{}, err
		}
		opts.Filters, opts.UserID, opts.EnableUserData = "IsPlayed", u.ID, true

		out := statsOut{User: u.Name}
		var ticks int64
		genres := map[string]int{}
		episodes := map[string]int{} // series id -> episodes watched
		var plays []playsRow
		seen := titles{}
		if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
			for i := range items {
				it := &items[i]
				if !seen.add(it) {
					continue // another copy of a title already counted
				}
				ticks += it.RunTimeTicks
				if it.Type == typeMovie {
					out.MoviesWatched++
					for _, g := range it.Genres {
						genres[g]++
					}
				} else {
					out.EpisodesWatched++
					if it.SeriesID != "" {
						episodes[it.SeriesID]++
					}
				}
				if it.UserData != nil && it.UserData.PlayCount > 0 {
					name := it.Name
					if it.SeriesName != "" {
						name = it.SeriesName + ": " + it.Name
					}
					plays = append(plays, playsRow{Name: name, Type: it.Type, Plays: it.UserData.PlayCount})
				}
			}
			return true
		}); err != nil {
			return nil, statsOut{}, err
		}
		out.HoursWatched = math.Round(float64(ticks)/ticksPerMinute/60*10) / 10

		// the series watched, for their names, genres and whether any
		// episode is left
		out.SeriesStarted = len(episodes)
		var series []seriesRow
		if len(episodes) > 0 {
			ids := make([]string, 0, len(episodes))
			for id := range episodes {
				ids = append(ids, id)
			}
			if err := client.SearchAll(ctx, embyfin.SearchOptions{IDs: strings.Join(ids, ","), IncludeItemTypes: "Series", UserID: u.ID, EnableUserData: true, Fields: "Path,Genres"}, func(items []embyfin.Item) bool {
				for i := range items {
					s := &items[i]
					finished := s.UserData != nil && s.UserData.UnplayedItemCount == 0
					if finished {
						out.SeriesFinished++
					}
					for _, g := range s.Genres {
						genres[g]++
					}
					series = append(series, seriesRow{Name: s.Name, Watched: episodes[s.ID], Finished: finished})
				}
				return true
			}); err != nil {
				return nil, statsOut{}, err
			}
		}

		out.TopGenres = sortedCounts(genres)
		slices.SortFunc(series, func(a, b seriesRow) int {
			if a.Watched != b.Watched {
				return b.Watched - a.Watched
			}
			return strings.Compare(a.Name, b.Name)
		})
		out.TopSeries = series[:min(len(series), 10)]
		slices.SortFunc(plays, func(a, b playsRow) int {
			if a.Plays != b.Plays {
				return b.Plays - a.Plays
			}
			return strings.Compare(a.Name, b.Name)
		})
		out.MostPlayed = plays[:min(len(plays), 10)]

		return nil, out, nil
	})
}
