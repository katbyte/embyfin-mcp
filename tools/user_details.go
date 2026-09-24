package tools

import (
	"context"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// idsPerRequest is how many items a tool reads back by id in one request.
// Jellyfin takes each id as a parameter of its own, about 37 bytes, and a
// request line much past 8 KB is refused by a typical server or proxy: a user
// with a couple of hundred series started failed the whole of user_stats
// when they went in one request.
const idsPerRequest = 100

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
		// how the account wants playback, which decides whether a file whose
		// first audio track is in another language plays right for it
		AudioLanguage         string `json:"audio_language,omitempty"    jsonschema:"preferred audio language (ISO 639-2, e.g. eng); absent means the server's default"`
		SubtitleLanguage      string `json:"subtitle_language,omitempty" jsonschema:"preferred subtitle language; absent means the server's default"`
		SubtitleMode          string `json:"subtitle_mode,omitempty"     jsonschema:"when subtitles show: Default, Always, OnlyForced, None, Smart (or HearingImpaired on Emby)"`
		PlayDefaultAudioTrack bool   `json:"play_default_audio_track"    jsonschema:"true plays the file's default track whatever its language, rather than the one in the preferred language"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_get",
		Description: "One account in depth: whether it is an administrator, disabled or hidden, what it may do, which libraries it sees, when it last logged in, and its playback preferences (audio and subtitle language, subtitle mode). What it has watched is user_stats.",
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
		MoviesWatched   int          `json:"movies_watched"   jsonschema:"films watched, each once however many copies the server holds"`
		EpisodesWatched int          `json:"episodes_watched"`
		InProgress      int          `json:"in_progress"      jsonschema:"films and episodes part way through"`
		Favourites      int          `json:"favourites"       jsonschema:"films and episodes favourited"`
		HoursWatched    float64      `json:"hours_watched"    jsonschema:"the runtime of everything watched, once each"`
		SeriesStarted   int          `json:"series_started"`
		SeriesFinished  int          `json:"series_finished"`
		TopGenres       []valueCount `json:"top_genres"       jsonschema:"across the films and series watched, a series once"`
		TopSeries       []seriesRow  `json:"top_series"       jsonschema:"most episodes watched, top 10"`
		MostPlayed      []playsRow   `json:"most_played"      jsonschema:"highest play counts, top 10 (Emby does not count a mark as watched as a play)"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "user_stats",
		Description: "A user's watching in numbers, from their watch state across the library in one pass: films and episodes watched, in progress and favourited, hours, series started and finished, their top genres and series, and what they have played most. A film with copies in several places counts once.",
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
		// one walk in the user's view, sorting each item by its watch state,
		// rather than a filtered walk per number
		opts.UserID, opts.EnableUserData = u.ID, true

		out := statsOut{User: u.Name}
		var ticks int64
		genres := map[string]int{}
		episodes := map[string]int{} // series id -> episodes watched
		var plays []playsRow
		// a title counts once per state, whichever of its copies carries it:
		// Jellyfin marks the one copy watched, and the others come first in
		// the sweep as often as not
		played, favourited, inProgress := titles{}, titles{}, titles{}
		if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
			for i := range items {
				it := &items[i]
				if it.UserData == nil {
					continue
				}
				if it.UserData.IsFavourite && favourited.add(it) {
					out.Favourites++
				}
				if it.UserData.PlaybackPositionTicks > 0 && inProgress.add(it) {
					out.InProgress++
				}
				if !it.UserData.Played || !played.add(it) {
					continue // not watched, or another copy of a watched title already counted
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
				if it.UserData.PlayCount > 0 {
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
		// a batch at a time: see idsPerRequest
		ids := slices.Sorted(maps.Keys(episodes))
		for batch := range slices.Chunk(ids, idsPerRequest) {
			if err := client.SearchAll(ctx, embyfin.SearchOptions{IDs: strings.Join(batch, ","), IncludeItemTypes: "Series", UserID: u.ID, EnableUserData: true, Fields: "Path,Genres"}, func(items []embyfin.Item) bool {
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
