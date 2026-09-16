package embyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apiclient "github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
)

type MediaStream struct {
	Type         string `json:"Type"` // Video, Audio, Subtitle
	Codec        string `json:"Codec"`
	Language     string `json:"Language,omitempty"`
	Width        int    `json:"Width,omitempty"`
	Height       int    `json:"Height,omitempty"`
	BitRate      int64  `json:"BitRate,omitempty"`
	Channels     int    `json:"Channels,omitempty"`
	DisplayTitle string `json:"DisplayTitle,omitempty"`
	IsExternal   bool   `json:"IsExternal,omitempty"`
}

type MediaSource struct {
	Container    string        `json:"Container"`
	Size         int64         `json:"Size"`
	Bitrate      int64         `json:"Bitrate"`
	Path         string        `json:"Path"`
	MediaStreams []MediaStream `json:"MediaStreams"`
}

// NameRef is a named record the server also knows by id (a tag, a genre, a
// studio).
type NameRef struct {
	Name string `json:"Name"`
	ID   any    `json:"Id,omitempty"` // a number on Emby, a string on Jellyfin
}

type Person struct {
	Name string `json:"Name"`
	ID   string `json:"Id,omitempty"`
	Role string `json:"Role,omitempty"`
	Type string `json:"Type,omitempty"` // Actor, Director, Writer...
}

type UserData struct {
	Played                bool    `json:"Played"`
	PlayCount             int     `json:"PlayCount"`
	IsFavourite           bool    `json:"IsFavorite"`
	LastPlayedDate        string  `json:"LastPlayedDate,omitempty"`
	PlaybackPositionTicks int64   `json:"PlaybackPositionTicks,omitempty"`
	PlayedPercentage      float64 `json:"PlayedPercentage,omitempty"`
	UnplayedItemCount     int     `json:"UnplayedItemCount,omitempty"` // a series or season's unwatched episodes
}

type Item struct {
	ID             string    `json:"Id"`
	Name           string    `json:"Name"`
	OriginalTitle  string    `json:"OriginalTitle,omitempty"`
	Type           string    `json:"Type"` // Movie, Series, Episode...
	ProductionYear int       `json:"ProductionYear,omitempty"`
	PremiereDate   string    `json:"PremiereDate,omitempty"`
	DateCreated    string    `json:"DateCreated,omitempty"`
	Path           string    `json:"Path,omitempty"`
	Overview       string    `json:"Overview,omitempty"`
	Genres         []string  `json:"Genres,omitempty"`
	Tags           []string  `json:"Tags,omitempty"`     // Jellyfin
	TagItems       []NameRef `json:"TagItems,omitempty"` // Emby
	Studios        []NameRef `json:"Studios,omitempty"`

	OfficialRating  string  `json:"OfficialRating,omitempty"` // the parental rating, e.g. PG-13
	CommunityRating float64 `json:"CommunityRating,omitempty"`

	RunTimeTicks      int64             `json:"RunTimeTicks,omitempty"` // 1 tick = 100ns
	ProviderIDs       map[string]string `json:"ProviderIds,omitempty"`
	ImageTags         map[string]string `json:"ImageTags,omitempty"`
	MediaSources      []MediaSource     `json:"MediaSources,omitempty"`
	People            []Person          `json:"People,omitempty"`
	UserData          *UserData         `json:"UserData,omitempty"`
	SeriesName        string            `json:"SeriesName,omitempty"`
	SeriesID          string            `json:"SeriesId,omitempty"`
	ParentIndexNumber int               `json:"ParentIndexNumber,omitempty"` // season number for episodes
	IndexNumber       int               `json:"IndexNumber,omitempty"`       // episode number
	IndexNumberEnd    int               `json:"IndexNumberEnd,omitempty"`    // last episode number of a file holding several (S01E01E02)
	PlaylistItemID    string            `json:"PlaylistItemId,omitempty"`    // entry id within a playlist
	IsMissing         bool              `json:"IsMissing,omitempty"`         // virtual episode the library lacks
}

// HasFile reports whether the library holds a file for the item, which is
// one question asked in several places and worth one answer: an episode is
// held when there is a file behind it, and not when the server is only
// keeping the record of one it lacks (IsMissing, which both mappers read off
// LocationType Virtual).
func (i *Item) HasFile() bool {
	return !i.IsMissing && i.Path != ""
}

// TagNames returns the item's tags whichever way the server spells them.
func (i *Item) TagNames() []string {
	if len(i.Tags) > 0 {
		return i.Tags
	}
	out := make([]string, 0, len(i.TagItems))
	for _, t := range i.TagItems {
		out = append(out, t.Name)
	}

	return out
}

// StudioNames returns the names of the item's studios.
func (i *Item) StudioNames() []string {
	out := make([]string, 0, len(i.Studios))
	for _, s := range i.Studios {
		out = append(out, s.Name)
	}

	return out
}

// RuntimeMinutes converts RunTimeTicks (100ns units) to whole minutes.
func (i *Item) RuntimeMinutes() int {
	return int(i.RunTimeTicks / int64(time.Minute/100))
}

// FieldsDefault is requested unless SearchOptions.Fields overrides it.
const FieldsDefault = "Path,ProviderIds,ProductionYear,PremiereDate,OriginalTitle,MediaSources,DateCreated"

// FieldsDetail adds the expensive fields used for single-item views.
const FieldsDetail = FieldsDefault + ",People,Overview,Genres,Tags,TagItems,Studios,OfficialRating,CommunityRating"

// FieldsVocabulary is what the vocabulary sweeps (filters, spellings,
// renames) read: the free-text lists and the parental rating.
const FieldsVocabulary = "Path,ProductionYear,Genres,Tags,TagItems,Studios,OfficialRating"

// FieldsLean keeps audit sweeps cheap.
const FieldsLean = "Path,ProviderIds,ProductionYear,Overview,DateCreated"

type SearchOptions struct {
	SearchTerm       string
	IncludeItemTypes string // e.g. "Movie" or "Series,Episode"; empty = all
	ExcludeItemTypes string // e.g. "Folder,BoxSet"
	ParentID         string // restrict to one library (a VirtualFolder ItemID)
	PersonIDs        string // restrict to items featuring these people
	// Genres, Tags, Studios and OfficialRatings restrict by name; an item
	// matches when it carries any of the names given. Names are lists rather
	// than comma-separated because a genre can hold a comma.
	Genres          []string
	Tags            []string
	Studios         []string
	OfficialRatings []string
	Years           string // comma-separated production years
	// ParentIndexNumber restricts to one season number (episodes only); 0 is
	// every season, so the specials cannot be asked for this way.
	ParentIndexNumber int
	IDs               string // comma-separated item ids
	Filters           string // e.g. IsPlayed, IsFavorite, IsResumable
	SortBy            string // e.g. DateCreated, DatePlayed, SortName
	SortOrder         string // Ascending or Descending
	UserID            string // user context: adds watch state to UserData
	EnableUserData    bool
	Fields            string // override FieldsDefault
	Limit             int
	StartIndex        int
}

// Search returns matching library items plus the total match count
// (which may exceed len(items) when Limit pages the results).
func (c *Client) Search(ctx context.Context, opts SearchOptions) ([]Item, int, error) {
	fields := opts.Fields
	if fields == "" {
		fields = FieldsDefault
	}
	if c.isEmby() {
		return c.searchEmby(ctx, opts, fields)
	}

	return c.searchJF(ctx, opts, fields)
}

func (c *Client) searchEmby(ctx context.Context, opts SearchOptions, fields string) ([]Item, int, error) {
	o := emby.GetItemsOperationOptions{
		Recursive:        new(true),
		Fields:           fields,
		SearchTerm:       opts.SearchTerm,
		IncludeItemTypes: opts.IncludeItemTypes,
		ExcludeItemTypes: opts.ExcludeItemTypes,
		ParentId:         opts.ParentID,
		PersonIds:        opts.PersonIDs,
		// Emby reads these as one pipe-delimited value
		Genres:            strings.Join(opts.Genres, "|"),
		Tags:              strings.Join(opts.Tags, "|"),
		Studios:           strings.Join(opts.Studios, "|"),
		OfficialRatings:   strings.Join(opts.OfficialRatings, "|"),
		Years:             opts.Years,
		Ids:               opts.IDs,
		Filters:           opts.Filters,
		SortBy:            opts.SortBy,
		SortOrder:         opts.SortOrder,
		ParentIndexNumber: opts.ParentIndexNumber,
		Limit:             opts.Limit,
		StartIndex:        opts.StartIndex,
	}
	if opts.EnableUserData {
		o.EnableUserData = new(true)
	}

	// Emby scopes user context via the path; Jellyfin dropped those routes
	// in 10.9 and takes userId as a query parameter instead.
	var res *emby.QueryResultBaseItemDto
	if opts.UserID != "" {
		resp, err := c.emby.GetUsersByUserIdItems(ctx, opts.UserID, embyUserItemsOptions(&o))
		if err != nil {
			return nil, 0, err
		}
		res = resp.Model
	} else {
		resp, err := c.emby.GetItems(ctx, o)
		if err != nil {
			return nil, 0, err
		}
		res = resp.Model
	}

	return itemsFromEmby(res.Items), res.TotalRecordCount, nil
}

// embyUserItemsOptions copies the item query onto the per-user route's
// options, which the document declares separately with the same fields
// (minus UserId, which is the path).
func embyUserItemsOptions(o *emby.GetItemsOperationOptions) emby.GetUsersByUserIdItemsOperationOptions {
	return emby.GetUsersByUserIdItemsOperationOptions{
		Recursive:         o.Recursive,
		Fields:            o.Fields,
		SearchTerm:        o.SearchTerm,
		IncludeItemTypes:  o.IncludeItemTypes,
		ExcludeItemTypes:  o.ExcludeItemTypes,
		ParentId:          o.ParentId,
		PersonIds:         o.PersonIds,
		Genres:            o.Genres,
		Tags:              o.Tags,
		Studios:           o.Studios,
		OfficialRatings:   o.OfficialRatings,
		Years:             o.Years,
		ParentIndexNumber: o.ParentIndexNumber,
		Ids:               o.Ids,
		Filters:           o.Filters,
		SortBy:            o.SortBy,
		SortOrder:         o.SortOrder,
		Limit:             o.Limit,
		StartIndex:        o.StartIndex,
		EnableUserData:    o.EnableUserData,
	}
}

func (c *Client) searchJF(ctx context.Context, opts SearchOptions, fields string) ([]Item, int, error) {
	years, err := intList(opts.Years)
	if err != nil {
		return nil, 0, fmt.Errorf("years: %w", err)
	}
	o := jf.GetItemsOperationOptions{
		Recursive: new(true),
		// a film that belongs to a collection is otherwise folded into it on
		// Jellyfin when no user context says how to display it, and vanishes
		// from the library's own listing (Emby ignores the parameter)
		CollapseBoxSetItems: new(false),
		Fields:              list[jf.ItemFields](fields),
		SearchTerm:          opts.SearchTerm,
		IncludeItemTypes:    list[jf.BaseItemKind](opts.IncludeItemTypes),
		ExcludeItemTypes:    list[jf.BaseItemKind](opts.ExcludeItemTypes),
		ParentId:            opts.ParentID,
		PersonIds:           list[string](opts.PersonIDs),
		Genres:              opts.Genres,
		Tags:                opts.Tags,
		Studios:             opts.Studios,
		OfficialRatings:     opts.OfficialRatings,
		Years:               years,
		ParentIndexNumber:   opts.ParentIndexNumber,
		Ids:                 list[string](opts.IDs),
		Filters:             list[jf.ItemFilter](opts.Filters),
		SortBy:              list[jf.ItemSortBy](opts.SortBy),
		SortOrder:           list[jf.SortOrder](opts.SortOrder),
		UserId:              opts.UserID,
		Limit:               opts.Limit,
		StartIndex:          opts.StartIndex,
	}
	if opts.EnableUserData {
		o.EnableUserData = new(true)
	}

	res, err := c.jf.GetItems(ctx, o)
	if err != nil {
		return nil, 0, err
	}

	return itemsFromJF(res.Model.Items), res.Model.TotalRecordCount, nil
}

// SearchAll pages through every matching item. cb is called per page; return
// false to stop early.
func (c *Client) SearchAll(ctx context.Context, opts SearchOptions, cb func(items []Item) bool) error {
	const page = 1000
	opts.Limit = page

	for start := 0; ; start += page {
		opts.StartIndex = start

		items, total, err := c.Search(ctx, opts)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}

		if !cb(items) {
			return nil
		}

		if start+page >= total {
			return nil
		}
	}
}

// ItemByID fetches a single item with full detail fields.
func (c *Client) ItemByID(ctx context.Context, id string) (*Item, error) {
	items, _, err := c.Search(ctx, SearchOptions{IDs: id, Fields: FieldsDetail})
	if err != nil {
		return nil, err
	}

	// Emby drops an Ids filter it cannot parse and answers with the whole
	// library, so the answer has to be the item that was asked for
	if len(items) == 0 || items[0].ID != id {
		return nil, fmt.Errorf("no item with id %s", id)
	}

	return &items[0], nil
}

// ItemsByProviderID looks up items by a metadata provider id, e.g.
// ("tmdb", "89998"). Emby supports this server-side; Jellyfin lacks the query
// parameter, so we fall back to scanning by type and filtering client-side.
func (c *Client) ItemsByProviderID(ctx context.Context, provider, id string) ([]Item, error) {
	if c.isEmby() {
		res, err := c.emby.GetItems(ctx, emby.GetItemsOperationOptions{
			Recursive:           new(true),
			Fields:              FieldsDefault,
			AnyProviderIdEquals: provider + "." + id,
		})
		if err != nil {
			return nil, err
		}

		return itemsFromEmby(res.Model.Items), nil
	}

	// Jellyfin fallback: page through movies and series and match locally.
	var matches []Item
	if err := c.SearchAll(ctx, SearchOptions{IncludeItemTypes: "Movie,Series"}, func(items []Item) bool {
		for _, it := range items {
			for k, v := range it.ProviderIDs {
				if strings.EqualFold(k, provider) && v == id {
					matches = append(matches, it)
				}
			}
		}
		return true
	}); err != nil {
		return nil, err
	}

	return matches, nil
}

// Similar returns items the server considers similar to the given one.
// userID is required: Emby returns HTTP 500 for /Similar without one.
func (c *Client) Similar(ctx context.Context, id, userID string, limit int) ([]Item, error) {
	if c.isEmby() {
		res, err := c.emby.GetItemsByIdSimilar(ctx, id, emby.GetItemsByIdSimilarOperationOptions{UserId: userID, Fields: FieldsDefault, Limit: limit})
		if err != nil {
			return nil, err
		}

		return itemsFromEmby(res.Model.Items), nil
	}

	res, err := c.jf.GetSimilarItems(ctx, id, jf.GetSimilarItemsOperationOptions{UserId: userID, Fields: list[jf.ItemFields](FieldsDefault), Limit: limit})
	if err != nil {
		return nil, err
	}

	return itemsFromJF(res.Model.Items), nil
}

// InstantMix builds a music mix seeded from a song, album, artist, or genre.
func (c *Client) InstantMix(ctx context.Context, id string, limit int) ([]Item, error) {
	if c.isEmby() {
		res, err := c.emby.GetItemsByIdInstantMix(ctx, id, emby.GetItemsByIdInstantMixOperationOptions{Fields: FieldsDefault, Limit: limit})
		if err != nil {
			return nil, err
		}

		return itemsFromEmby(res.Model.Items), nil
	}

	res, err := c.jf.GetInstantMixFromItem(ctx, id, jf.GetInstantMixFromItemOperationOptions{Fields: list[jf.ItemFields](FieldsDefault), Limit: limit})
	if err != nil {
		return nil, err
	}

	return itemsFromJF(res.Model.Items), nil
}

// RefreshItem asks the server to re-fetch metadata and images for one item.
func (c *Client) RefreshItem(ctx context.Context, id string, replaceAll bool) error {
	var replace *bool
	if replaceAll {
		replace = new(true)
	}
	if c.isEmby() {
		_, err := c.emby.PostItemsByIdRefresh(ctx, id, emby.BaseRefreshRequest{}, emby.PostItemsByIdRefreshOperationOptions{
			MetadataRefreshMode: emby.MetadataRefreshModeFullRefresh,
			ImageRefreshMode:    emby.MetadataRefreshModeFullRefresh,
			ReplaceAllMetadata:  replace,
			ReplaceAllImages:    replace,
		})

		return err
	}

	_, err := c.jf.RefreshItem(ctx, id, jf.RefreshItemOperationOptions{
		MetadataRefreshMode: jf.MetadataRefreshModeFullRefresh,
		ImageRefreshMode:    jf.MetadataRefreshModeFullRefresh,
		ReplaceAllMetadata:  replace,
		ReplaceAllImages:    replace,
	})

	return err
}

// DeleteItem permanently removes an item AND its media file from disk.
func (c *Client) DeleteItem(ctx context.Context, id string) error {
	if c.isEmby() {
		_, err := c.emby.DeleteItemsById(ctx, id)
		return err
	}

	_, err := c.jf.DeleteItem(ctx, id)

	return err
}

// UserItem fetches one item in a user's context, with that user's watch
// state complete: Emby's list endpoints leave LastPlayedDate and PlayCount
// out of UserData, and only the single-item read has them.
func (c *Client) UserItem(ctx context.Context, userID, itemID string) (*Item, error) {
	if c.isEmby() {
		res, err := c.emby.GetUsersByUserIdItemsById(ctx, userID, itemID)
		if err != nil {
			return nil, err
		}
		it := itemFromEmby(res.Model)

		return &it, nil
	}

	res, err := c.jf.GetItem(ctx, itemID, jf.GetItemOperationOptions{UserId: userID})
	if err != nil {
		return nil, err
	}
	it := itemFromJF(res.Model)

	return &it, nil
}

// FullItem fetches the complete item DTO in a user's context, as a map of
// its JSON, which is what edits take: they set fields on the map and post it
// back with UpdateItem. The map is the typed DTO's JSON, so it carries what
// the server's document declares (both servers keep the fields a partial
// post leaves out).
func (c *Client) FullItem(ctx context.Context, userID, itemID string) (map[string]any, error) {
	if c.isEmby() {
		res, err := c.emby.GetUsersByUserIdItemsById(ctx, userID, itemID)
		if err != nil {
			return nil, err
		}

		return toMap(res.Model)
	}

	res, err := c.jf.GetItem(ctx, itemID, jf.GetItemOperationOptions{UserId: userID})
	if err != nil {
		return nil, err
	}

	return toMap(res.Model)
}

// UpdateItem posts a full item DTO back to the server, replacing its
// metadata. Emby reads the named GenreItems and TagItems and ignores the
// plain Genres and Tags lists; Jellyfin reads the plain lists; so an edit
// sets both (tools/items.go does).
func (c *Client) UpdateItem(ctx context.Context, itemID string, full map[string]any) error {
	if c.isEmby() {
		var dto emby.BaseItemDto
		if err := fromMap(full, &dto); err != nil {
			return err
		}
		_, err := c.emby.PostItemsByItemId(ctx, itemID, dto)

		return err
	}

	var dto jf.BaseItemDto
	if err := fromMap(full, &dto); err != nil {
		return err
	}
	_, err := c.jf.UpdateItem(ctx, itemID, dto)

	return err
}

// EditItem changes an item the way FullItem and UpdateItem do, holding the
// item for the whole round so that edits made at the same time (an MCP client
// calls tools in parallel) apply one after the other rather than each posting
// back what it read (see keyedLocks). edit changes the map and reports
// whether it changed anything; nothing is posted when it did not. The map is
// returned as edited.
func (c *Client) EditItem(ctx context.Context, userID, itemID string, edit func(full map[string]any) (bool, error)) (map[string]any, error) {
	unlock := c.items.lock(itemID)
	defer unlock()

	full, err := c.FullItem(ctx, userID, itemID)
	if err != nil {
		return nil, err
	}
	changed, err := edit(full)
	if err != nil || !changed {
		return full, err
	}

	return full, c.UpdateItem(ctx, itemID, full)
}

// VisibleUserItem is UserItem for an item the user may see, and false for
// one they may not (in a library they have no access to, or rated above what
// they may watch). Jellyfin's single-item read answers 404 for such an item;
// Emby's answers with it, and only its list query in the user's view leaves
// it out, so Emby asks that first.
func (c *Client) VisibleUserItem(ctx context.Context, userID, itemID string) (*Item, bool, error) {
	if c.isEmby() {
		items, _, err := c.Search(ctx, SearchOptions{IDs: itemID, UserID: userID, Fields: "Path", Limit: 1})
		if err != nil {
			return nil, false, err
		}
		// an id Emby cannot parse drops the filter, so the answer must be the item
		if len(items) == 0 || items[0].ID != itemID {
			return nil, false, nil
		}
	}

	it, err := c.UserItem(ctx, userID, itemID)
	if apiclient.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	return it, true, nil
}

// toMap and fromMap move a typed DTO through its JSON, the shape the tools
// edit.
func toMap(dto any) (map[string]any, error) {
	b, err := json.Marshal(dto)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}

	return m, nil
}

func fromMap(m map[string]any, dto any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, dto); err != nil {
		return fmt.Errorf("item fields: %w", err)
	}

	return nil
}
