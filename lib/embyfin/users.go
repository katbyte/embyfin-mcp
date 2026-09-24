package embyfin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
)

type User struct {
	ID               string     `json:"Id"`
	Name             string     `json:"Name"`
	LastActivityDate string     `json:"LastActivityDate,omitempty"`
	LastLoginDate    string     `json:"LastLoginDate,omitempty"`
	HasPassword      bool       `json:"HasPassword"`
	Policy           UserPolicy `json:"Policy"`
	// Preferences are how the account wants playback: which audio and
	// subtitle languages, and when subtitles show. Both servers keep them
	// on the user's configuration under the same names.
	Preferences Preferences `json:"Preferences"`
}

// Preferences are an account's playback preferences.
type Preferences struct {
	AudioLanguage    string `json:"AudioLanguage,omitempty"`    // ISO 639-2, e.g. "eng"; empty for the server's default
	SubtitleLanguage string `json:"SubtitleLanguage,omitempty"` // likewise
	// SubtitleMode is Default, Always, OnlyForced, None, Smart, or on Emby
	// HearingImpaired.
	SubtitleMode string `json:"SubtitleMode,omitempty"`
	// PlayDefaultAudioTrack plays the file's default track rather than the
	// one in the preferred language.
	PlayDefaultAudioTrack bool `json:"PlayDefaultAudioTrack"`
}

// UserPolicy is what an account may do and see.
type UserPolicy struct {
	IsAdministrator       bool     `json:"IsAdministrator"`
	IsDisabled            bool     `json:"IsDisabled"`
	IsHidden              bool     `json:"IsHidden"`
	EnableAllFolders      bool     `json:"EnableAllFolders"`
	EnabledFolders        []string `json:"EnabledFolders,omitempty"` // the libraries it sees, when not every one: see CanSee
	EnableContentDeletion bool     `json:"EnableContentDeletion"`
	EnableRemoteAccess    bool     `json:"EnableRemoteAccess"`
	MaxParentalRating     int      `json:"MaxParentalRating,omitempty"`
}

// CanSee reports whether the account may see a library. EnabledFolders lists
// a library by its Guid on Emby (the numeric id there grants nothing) and by
// its ItemId on Jellyfin.
func (u *User) CanSee(folder *VirtualFolder) bool {
	p := u.Policy
	if p.EnableAllFolders {
		return true
	}
	id := folder.ItemID
	if folder.GUID != "" {
		id = folder.GUID
	}

	return id != "" && slices.ContainsFunc(p.EnabledFolders, func(f string) bool { return strings.EqualFold(f, id) })
}

func (c *Client) Users(ctx context.Context) ([]User, error) {
	if c.isEmby() {
		res, err := c.emby.GetUsersQuery(ctx, emby.GetUsersQueryOperationOptions{})
		if err != nil {
			return nil, err
		}
		listed := orEmpty(res.Model).Items
		users := make([]User, 0, len(listed))
		for i := range listed {
			users = append(users, userFromEmby(&listed[i]))
		}

		return users, nil
	}

	res, err := c.jf.GetUsers(ctx, jf.GetUsersOperationOptions{})
	if err != nil {
		return nil, err
	}
	users := make([]User, 0, len(res.Model))
	for i := range res.Model {
		users = append(users, userFromJF(&res.Model[i]))
	}

	return users, nil
}

// ResolveUser finds a user by name (case-insensitive) or id. An empty
// nameOrID resolves to the first administrator.
func (c *Client) ResolveUser(ctx context.Context, nameOrID string) (*User, error) {
	users, err := c.Users(ctx)
	if err != nil {
		return nil, err
	}

	if nameOrID == "" {
		for i := range users {
			if users[i].Policy.IsAdministrator {
				return &users[i], nil
			}
		}
		if len(users) > 0 {
			return &users[0], nil
		}
		return nil, errors.New("server has no users")
	}

	names := make([]string, 0, len(users))
	for i := range users {
		if strings.EqualFold(users[i].Name, nameOrID) || users[i].ID == nameOrID {
			return &users[i], nil
		}
		names = append(names, users[i].Name)
	}

	return nil, fmt.Errorf("no user named %q (have: %s)", nameOrID, strings.Join(names, ", "))
}

// SetPlayed marks an item played or unplayed for a user.
// Emby keeps the legacy per-user route; Jellyfin moved it in 10.9.
func (c *Client) SetPlayed(ctx context.Context, userID, itemID string, played bool) error {
	var err error
	switch {
	case c.isEmby() && played:
		_, err = c.emby.PostUsersByUserIdPlayedItemsById(ctx, userID, itemID, emby.PostUsersByUserIdPlayedItemsByIdOperationOptions{})
	case c.isEmby():
		_, err = c.emby.DeleteUsersByUserIdPlayedItemsById(ctx, userID, itemID)
	case played:
		_, err = c.jf.MarkPlayedItem(ctx, itemID, jf.MarkPlayedItemOperationOptions{UserId: userID})
	default:
		_, err = c.jf.MarkUnplayedItem(ctx, itemID, jf.MarkUnplayedItemOperationOptions{UserId: userID})
	}

	return err
}

// SetFavourite marks an item as a favourite (or not) for a user. (The API
// routes keep the upstream FavoriteItems spelling.)
func (c *Client) SetFavourite(ctx context.Context, userID, itemID string, favourite bool) error {
	var err error
	switch {
	case c.isEmby() && favourite:
		_, err = c.emby.PostUsersByUserIdFavoriteItemsById(ctx, userID, itemID)
	case c.isEmby():
		_, err = c.emby.DeleteUsersByUserIdFavoriteItemsById(ctx, userID, itemID)
	case favourite:
		_, err = c.jf.MarkFavoriteItem(ctx, itemID, jf.MarkFavoriteItemOperationOptions{UserId: userID})
	default:
		_, err = c.jf.UnmarkFavoriteItem(ctx, itemID, jf.UnmarkFavoriteItemOperationOptions{UserId: userID})
	}

	return err
}

// SetProgress sets where a user is in an item, in ticks, and marks it
// unplayed: an item with a resume point is in progress, not watched. The
// position must be above zero, because a zero one cannot be sent (the
// document types it as a number the generated model omits when zero, and
// Jellyfin keeps what is omitted); marking an item unplayed clears it.
//
// Emby sets the position and the played flag from what is posted, a missing
// one as zero and false, and keeps the favourite and play count whatever is
// posted; Jellyfin applies only the fields set. Both are told the same two.
func (c *Client) SetProgress(ctx context.Context, userID, itemID string, positionTicks int64) error {
	if positionTicks <= 0 {
		return errors.New("the position must be above zero")
	}
	if c.isEmby() {
		_, err := c.emby.PostUsersByUserIdItemsByItemIdUserData(ctx, userID, itemID, emby.UserItemDataDto{PlaybackPositionTicks: positionTicks, Played: new(false)})
		return err
	}

	_, err := c.jf.UpdateItemUserData(ctx, itemID, jf.UpdateUserItemDataDto{PlaybackPositionTicks: positionTicks, Played: new(false)}, jf.UpdateItemUserDataOperationOptions{UserId: userID})

	return err
}

// NextUp returns the next episodes to watch per series for a user.
func (c *Client) NextUp(ctx context.Context, userID string, limit int) ([]Item, error) {
	if c.isEmby() {
		// Emby 4.10's default NextUp mode returns nothing for API-key callers; the legacy
		// per-series "next unwatched episode" mode is what we want anyway.
		res, err := c.emby.GetShowsNextUp(ctx, emby.GetShowsNextUpOperationOptions{
			UserId: userID, Fields: FieldsDefault, LegacyNextUp: new(true), Limit: nz(limit),
		})
		if err != nil {
			return nil, err
		}

		return itemsFromEmby(orEmpty(res.Model).Items), nil
	}

	res, err := c.jf.GetNextUp(ctx, jf.GetNextUpOperationOptions{UserId: userID, Fields: list[jf.ItemFields](FieldsDefault), Limit: nz(limit)})
	if err != nil {
		return nil, err
	}

	return itemsFromJF(res.Model.Items), nil
}

// Resume returns partially-watched items for a user: those with a resume
// point. Emby also lists the next episode of a series once the one before it
// is marked watched, at position zero and never started, which is next up
// rather than in progress, so it is left out.
func (c *Client) Resume(ctx context.Context, userID string, limit int) ([]Item, error) {
	if c.isEmby() {
		// without Recursive and MediaTypes Emby answers with an empty list even
		// when items are in progress
		res, err := c.emby.GetUsersByUserIdItemsResume(ctx, userID, emby.GetUsersByUserIdItemsResumeOperationOptions{
			Fields: FieldsDefault, EnableUserData: new(true), Recursive: new(true), MediaTypes: "Video", Limit: nz(limit),
		})
		if err != nil {
			return nil, err
		}

		return slices.DeleteFunc(itemsFromEmby(orEmpty(res.Model).Items), func(it Item) bool {
			return it.UserData == nil || it.UserData.PlaybackPositionTicks <= 0
		}), nil
	}

	res, err := c.jf.GetResumeItems(ctx, jf.GetResumeItemsOperationOptions{
		UserId: userID, Fields: list[jf.ItemFields](FieldsDefault), EnableUserData: new(true),
		MediaTypes: []jf.MediaType{jf.MediaTypeVideo}, Limit: nz(limit),
	})
	if err != nil {
		return nil, err
	}

	return itemsFromJF(res.Model.Items), nil
}
