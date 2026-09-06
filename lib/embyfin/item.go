package embyfin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
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

type Person struct {
	Name string `json:"Name"`
	ID   string `json:"Id,omitempty"`
	Role string `json:"Role,omitempty"`
	Type string `json:"Type,omitempty"` // Actor, Director, Writer...
}

type UserData struct {
	Played                bool   `json:"Played"`
	PlayCount             int    `json:"PlayCount"`
	IsFavourite           bool   `json:"IsFavorite"`
	LastPlayedDate        string `json:"LastPlayedDate,omitempty"`
	PlaybackPositionTicks int64  `json:"PlaybackPositionTicks,omitempty"`
}

type Item struct {
	ID                string            `json:"Id"`
	Name              string            `json:"Name"`
	OriginalTitle     string            `json:"OriginalTitle,omitempty"`
	Type              string            `json:"Type"` // Movie, Series, Episode...
	ProductionYear    int               `json:"ProductionYear,omitempty"`
	PremiereDate      string            `json:"PremiereDate,omitempty"`
	DateCreated       string            `json:"DateCreated,omitempty"`
	Path              string            `json:"Path,omitempty"`
	Overview          string            `json:"Overview,omitempty"`
	RunTimeTicks      int64             `json:"RunTimeTicks,omitempty"` // 1 tick = 100ns
	ProviderIDs       map[string]string `json:"ProviderIds,omitempty"`
	ImageTags         map[string]string `json:"ImageTags,omitempty"`
	MediaSources      []MediaSource     `json:"MediaSources,omitempty"`
	People            []Person          `json:"People,omitempty"`
	UserData          *UserData         `json:"UserData,omitempty"`
	SeriesName        string            `json:"SeriesName,omitempty"`
	ParentIndexNumber int               `json:"ParentIndexNumber,omitempty"` // season number for episodes
	IndexNumber       int               `json:"IndexNumber,omitempty"`       // episode number
	PlaylistItemID    string            `json:"PlaylistItemId,omitempty"`    // entry id within a playlist
	IsMissing         bool              `json:"IsMissing,omitempty"`         // virtual episode the library lacks
}

// RuntimeMinutes converts RunTimeTicks (100ns units) to whole minutes.
func (i *Item) RuntimeMinutes() int {
	return int(i.RunTimeTicks / int64(time.Minute/100))
}

type itemsResponse struct {
	Items            []Item `json:"Items"`
	TotalRecordCount int    `json:"TotalRecordCount"`
}

// FieldsDefault is requested unless SearchOptions.Fields overrides it.
const FieldsDefault = "Path,ProviderIds,ProductionYear,PremiereDate,OriginalTitle,MediaSources,DateCreated"

// FieldsDetail adds the expensive fields used for single-item views.
const FieldsDetail = FieldsDefault + ",People,Overview"

// FieldsLean keeps audit sweeps cheap.
const FieldsLean = "Path,ProviderIds,ProductionYear,Overview,DateCreated"

type SearchOptions struct {
	SearchTerm       string
	IncludeItemTypes string // e.g. "Movie" or "Series,Episode"; empty = all
	ParentID         string // restrict to one library (a VirtualFolder ItemID)
	PersonIDs        string // restrict to items featuring these people
	GenreNames       string // restrict by genre name(s)
	Years            string // comma-separated production years
	IDs              string // comma-separated item ids
	Filters          string // e.g. IsPlayed, IsFavorite, IsResumable
	SortBy           string // e.g. DateCreated, DatePlayed, SortName
	SortOrder        string // Ascending or Descending
	UserID           string // user context: adds watch state to UserData
	EnableUserData   bool
	Fields           string // override FieldsDefault
	Limit            int
	StartIndex       int
}

// Search returns matching library items plus the total match count
// (which may exceed len(items) when Limit pages the results).
func (c *Client) Search(ctx context.Context, opts SearchOptions) ([]Item, int, error) {
	q := url.Values{}
	q.Set("Recursive", "true")

	fields := opts.Fields
	if fields == "" {
		fields = FieldsDefault
	}
	q.Set("Fields", fields)

	set := func(key, val string) {
		if val != "" {
			q.Set(key, val)
		}
	}
	set("SearchTerm", opts.SearchTerm)
	set("IncludeItemTypes", opts.IncludeItemTypes)
	set("ParentId", opts.ParentID)
	set("PersonIds", opts.PersonIDs)
	set("Genres", opts.GenreNames)
	set("Years", opts.Years)
	set("Ids", opts.IDs)
	set("Filters", opts.Filters)
	set("SortBy", opts.SortBy)
	set("SortOrder", opts.SortOrder)
	if opts.EnableUserData {
		q.Set("EnableUserData", "true")
	}
	if opts.Limit > 0 {
		q.Set("Limit", strconv.Itoa(opts.Limit))
	}
	if opts.StartIndex > 0 {
		q.Set("StartIndex", strconv.Itoa(opts.StartIndex))
	}

	// Emby scopes user context via the path; Jellyfin dropped those routes
	// in 10.9 and takes userId as a query parameter instead.
	path := "/Items"
	if opts.UserID != "" {
		if c.backend == Emby {
			path = "/Users/" + url.PathEscape(opts.UserID) + "/Items"
		} else {
			q.Set("userId", opts.UserID)
		}
	}

	var resp itemsResponse
	if err := c.get(ctx, path, q, &resp); err != nil {
		return nil, 0, err
	}

	return resp.Items, resp.TotalRecordCount, nil
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

	if len(items) == 0 {
		return nil, fmt.Errorf("no item with id %s", id)
	}

	return &items[0], nil
}

// ItemsByProviderID looks up items by a metadata provider id, e.g.
// ("tmdb", "89998"). Emby supports this server-side; Jellyfin lacks the query
// parameter, so we fall back to scanning by type and filtering client-side.
func (c *Client) ItemsByProviderID(ctx context.Context, provider, id string) ([]Item, error) {
	if c.backend == Emby {
		q := url.Values{}
		q.Set("Recursive", "true")
		q.Set("Fields", FieldsDefault)
		q.Set("AnyProviderIdEquals", provider+"."+id)

		var resp itemsResponse
		if err := c.get(ctx, "/Items", q, &resp); err != nil {
			return nil, err
		}

		return resp.Items, nil
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
	q := url.Values{}
	q.Set("UserId", userID)
	q.Set("Fields", FieldsDefault)
	if limit > 0 {
		q.Set("Limit", strconv.Itoa(limit))
	}

	var resp itemsResponse
	if err := c.get(ctx, "/Items/"+url.PathEscape(id)+"/Similar", q, &resp); err != nil {
		return nil, err
	}

	return resp.Items, nil
}

// InstantMix builds a music mix seeded from a song, album, artist, or genre.
func (c *Client) InstantMix(ctx context.Context, id string, limit int) ([]Item, error) {
	q := url.Values{}
	q.Set("Fields", FieldsDefault)
	if limit > 0 {
		q.Set("Limit", strconv.Itoa(limit))
	}

	var resp itemsResponse
	if err := c.get(ctx, "/Items/"+url.PathEscape(id)+"/InstantMix", q, &resp); err != nil {
		return nil, err
	}

	return resp.Items, nil
}

// RefreshItem asks the server to re-fetch metadata and images for one item.
func (c *Client) RefreshItem(ctx context.Context, id string, replaceAll bool) error {
	q := url.Values{}
	q.Set("MetadataRefreshMode", "FullRefresh")
	q.Set("ImageRefreshMode", "FullRefresh")
	if replaceAll {
		q.Set("ReplaceAllMetadata", "true")
		q.Set("ReplaceAllImages", "true")
	}

	return c.post(ctx, "/Items/"+url.PathEscape(id)+"/Refresh", q, nil, nil)
}

// DeleteItem permanently removes an item AND its media file from disk.
func (c *Client) DeleteItem(ctx context.Context, id string) error {
	return c.del(ctx, "/Items/"+url.PathEscape(id), nil)
}

// FullItem fetches the complete raw item DTO in a user's context — required
// for edits, which must POST the full object back.
func (c *Client) FullItem(ctx context.Context, userID, itemID string) (map[string]any, error) {
	path := "/Items/" + url.PathEscape(itemID)
	q := url.Values{}
	if c.backend == Emby {
		path = "/Users/" + url.PathEscape(userID) + "/Items/" + url.PathEscape(itemID)
	} else if userID != "" {
		q.Set("userId", userID)
	}

	var full map[string]any
	if err := c.get(ctx, path, q, &full); err != nil {
		return nil, err
	}

	return full, nil
}

// UpdateItem posts a full item DTO back to the server, replacing its metadata.
func (c *Client) UpdateItem(ctx context.Context, itemID string, full map[string]any) error {
	return c.do(ctx, http.MethodPost, "/Items/"+url.PathEscape(itemID), nil, full, nil)
}
