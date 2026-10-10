package embyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/sdk/emby"
	"github.com/katbyte/embyfin-mcp/sdk/jf"
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
	// IsForced marks a subtitle track that shows only the lines in another
	// language than the audio's (signs, a foreign-language scene): it is not
	// subtitles a viewer can follow the whole film by. Both servers read it
	// off the file's flag and off ".forced" in an external file's name.
	IsForced bool `json:"IsForced,omitempty"`

	// FrameRate is the video's frames per second. A film or a scripted drama
	// at 59.94 or 60 was most likely interpolated from a 23.976 master; sport,
	// much broadcast TV and some documentaries are shot at 50 or 60, so it is
	// a lead, not proof.
	FrameRate float32 `json:"AverageFrameRate,omitempty"`
	// ColourTransfer and ColourPrimaries say whether the file claims HDR
	// (smpte2084, arib-std-b67 / bt2020). Claimed on a source that cannot
	// have been HDR, they are a claim about the encode rather than the
	// picture.
	ColourTransfer  string `json:"ColorTransfer,omitempty"`
	ColourPrimaries string `json:"ColorPrimaries,omitempty"`
	// VideoRange and VideoRangeType are the servers' own reading: "SDR",
	// "HDR" (Emby 4.10 writes "HDR 10" for a file tagged with HDR10's
	// colours), and on Jellyfin the narrower "HDR10", "HDR10Plus", "HLG",
	// "DOVI", "DOVIWithHDR10" and the rest of its Dolby Vision kinds. Emby
	// answers only the first. Both are empty when the server has not probed
	// the file, which is not the same as SDR.
	VideoRange     string `json:"VideoRange,omitempty"`
	VideoRangeType string `json:"VideoRangeType,omitempty"`
	// ExtendedVideoType and ExtendedVideoSubType are Emby's narrower reading
	// (Jellyfin has neither): "None", "Hdr10", "Hdr10Plus", "HyperLogGamma"
	// or "DolbyVision", and for Dolby Vision its profile and the base layer a
	// player without it falls back to ("DoviProfile81" is profile 8.1, an
	// HDR10 base).
	ExtendedVideoType    string `json:"ExtendedVideoType,omitempty"`
	ExtendedVideoSubType string `json:"ExtendedVideoSubType,omitempty"`
	// AspectRatio is the shape the picture is meant to be shown at, as the
	// server read it from the file ("16:9", "4:3"), which the stored frame
	// does not always say: a DVD rip is 720x480 or 720x576 whether it is
	// 4:3 or an anamorphic 16:9. Empty when the file does not say.
	AspectRatio string `json:"AspectRatio,omitempty"`
}

// DisplayWidth is the width the picture is shown at, worked out from the
// height and the aspect ratio the file states, when that differs from the
// stored frame by more than rounding: an anamorphic 720x480 16:9 DVD is
// shown at 853x480. It is 0 when the file states no ratio, or the frame
// already has that shape.
func (s *MediaStream) DisplayWidth() int {
	w, h, ok := ParseAspect(s.AspectRatio)
	if !ok || s.Height <= 0 {
		return 0
	}
	display := int(math.Round(float64(s.Height) * w / h))
	if diff := display - s.Width; diff < 0 {
		diff = -diff
		if diff*50 <= s.Width { // within 2%
			return 0
		}
	} else if diff*50 <= s.Width {
		return 0
	}

	return display
}

// ParseAspect reads a stated ratio, "16:9" as 16 and 9, and one written as
// a decimal against 1, "1.5:1" or "2.35:1" - as both servers state a DVD's
// VOB they read the ratio of from its stream - as 1.5 and 1.
func ParseAspect(ratio string) (w, h float64, ok bool) {
	a, b, found := strings.Cut(ratio, ":")
	if !found {
		return 0, 0, false
	}
	w, err1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
	h, err2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if err1 != nil || err2 != nil || !(w > 0) || !(h > 0) || math.IsInf(w, 0) || math.IsInf(h, 0) {
		return 0, 0, false
	}

	return w, h, true
}

// MediaSource is one file an item is played from: one of its versions.
type MediaSource struct {
	// ItemID is the item the version is held as. Emby stores every version
	// as an item of its own and names it here; Jellyfin gives the version's
	// own id, which is the item's for its first version.
	ItemID string `json:"ItemId,omitempty"`
	// Name is the server's label for the version: "1080p" for a file named
	// as a version of the one beside it, else the file's or folder's name.
	Name         string        `json:"Name,omitempty"`
	Container    string        `json:"Container"`
	Size         int64         `json:"Size"`
	Bitrate      int64         `json:"Bitrate"`
	Path         string        `json:"Path"`
	RunTimeTicks int64         `json:"RunTimeTicks,omitempty"` // this file's own runtime, 1 tick = 100ns
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
	ID            string `json:"Id"`
	Name          string `json:"Name"`
	OriginalTitle string `json:"OriginalTitle,omitempty"`
	SortName      string `json:"SortName,omitempty"` // only answered when asked for (Fields=SortName)
	// PresentationKey is the key Emby shows items by: items it stores apart
	// that share it are one item's versions in a user's view. Emby answers
	// it only when asked (Fields=PresentationUniqueKey); "" otherwise, and
	// on Jellyfin, which stores its versions as one item instead.
	PresentationKey string `json:"PresentationUniqueKey,omitempty"`
	Type            string `json:"Type"` // Movie, Series, Episode...
	ProductionYear  int    `json:"ProductionYear,omitempty"`
	PremiereDate    string `json:"PremiereDate,omitempty"`
	DateCreated     string `json:"DateCreated,omitempty"`
	// DateModified is when the FILE last changed, which is the only thing
	// that moves when a download overwrites a path in place: the item keeps
	// its id and its DateCreated, so every "what was added" view is blind to
	// it. Emby answers it; Jellyfin's item has no such field and leaves it
	// empty.
	DateModified string    `json:"DateModified,omitempty"`
	Path         string    `json:"Path,omitempty"`
	Overview     string    `json:"Overview,omitempty"`
	Genres       []string  `json:"Genres,omitempty"`
	Tags         []string  `json:"Tags,omitempty"`     // Jellyfin
	TagItems     []NameRef `json:"TagItems,omitempty"` // Emby
	Studios      []NameRef `json:"Studios,omitempty"`
	// ParentID is the folder holding the item. An item a removed library
	// left behind can point at a parent the server no longer has.
	ParentID string `json:"ParentId,omitempty"`

	OfficialRating  string  `json:"OfficialRating,omitempty"` // the parental rating, e.g. PG-13
	CommunityRating float64 `json:"CommunityRating,omitempty"`

	RunTimeTicks int64             `json:"RunTimeTicks,omitempty"` // 1 tick = 100ns
	ProviderIDs  map[string]string `json:"ProviderIds,omitempty"`
	ImageTags    map[string]string `json:"ImageTags,omitempty"`
	MediaSources []MediaSource     `json:"MediaSources,omitempty"`
	// MediaSourceCount is how many versions Jellyfin holds the item in,
	// when that is more than one and asked for (Fields=MediaSourceCount):
	// the cheap way to know an item has files beyond its own path. Emby
	// has no such field, and holds each version as an item of its own.
	MediaSourceCount int       `json:"MediaSourceCount,omitempty"`
	People           []Person  `json:"People,omitempty"`
	UserData         *UserData `json:"UserData,omitempty"`
	SeriesName       string    `json:"SeriesName,omitempty"`
	SeriesID         string    `json:"SeriesId,omitempty"`
	// Album, AlbumArtist and Artists are a track's tags as the server read
	// them (an album entry carries its name and AlbumArtist)
	Album       string   `json:"Album,omitempty"`
	AlbumArtist string   `json:"AlbumArtist,omitempty"`
	Artists     []string `json:"Artists,omitempty"`
	// ParentIndexNumber is an episode's season number and IndexNumber its
	// episode number (a season's own number, on a season). Either is nil
	// when the server holds none, which is not 0: season 0 is the specials.
	// Jellyfin holds a file named without SxxEyy with neither, even in a
	// "Season 01" folder, and Emby one at the show's root; Emby holds a
	// season's extra as season 0 with no episode number.
	ParentIndexNumber *int   `json:"ParentIndexNumber,omitempty"`
	IndexNumber       *int   `json:"IndexNumber,omitempty"`
	IndexNumberEnd    int    `json:"IndexNumberEnd,omitempty"` // last episode number of a file holding several (S01E01E02)
	PlaylistItemID    string `json:"PlaylistItemId,omitempty"` // entry id within a playlist
	IsMissing         bool   `json:"IsMissing,omitempty"`      // virtual episode the library lacks
	// IsFolder is whether the server holds the item as a folder of others (a
	// series, a season, an album, a collection), which is what a playlist
	// add expands into the items beneath it
	IsFolder bool `json:"IsFolder,omitempty"`
	// Etag changes whenever the server saves the item, which is how a read
	// tells that a refresh it did not wait on has landed. Only answered when
	// asked for (Fields=Etag).
	Etag string `json:"Etag,omitempty"`
	// Settings is what Fields=Settings answers, nil when it was not asked
	// for or answered nothing
	Settings *ItemSettings `json:"-"`
}

// ItemSettings is an item's sort name as set and its locked fields.
// Jellyfin's ForcedSortName is the sort name as set, empty when none was,
// where its SortName is its own reading of it (lowercased, articles and
// dashes dropped); Emby's is its SortName, set or not, and a sort name set by
// an edit is among its LockedFields.
type ItemSettings struct {
	ForcedSortName string
	LockedFields   []string
}

// settingsOf is an item's settings, nil when there are none to say.
func settingsOf(forced string, locked []string) *ItemSettings {
	if forced == "" && len(locked) == 0 {
		return nil
	}

	return &ItemSettings{ForcedSortName: forced, LockedFields: locked}
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
const FieldsDetail = FieldsDefault + ",SortName,People,Overview,Genres,Tags,TagItems,Studios,OfficialRating,CommunityRating"

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
	// Direct lists only what ParentID holds itself, not what is under that
	// in turn (a collection's series, not its seasons and episodes)
	Direct    bool
	PersonIDs string // restrict to items featuring these people
	// Genres, Tags, Studios and OfficialRatings restrict by name; an item
	// matches when it carries any of the names given. Names are lists rather
	// than comma-separated because a genre can hold a comma.
	Genres          []string
	Tags            []string
	Studios         []string
	OfficialRatings []string
	Years           string // comma-separated production years
	// MediaTypes restricts to items of these media types, comma-separated
	// (Video, Audio, Photo, Book)
	MediaTypes string
	// ArtistIDs restricts to the songs and albums of these artists, by id,
	// comma-separated
	ArtistIDs string
	// ParentIndexNumber restricts to one season number (episodes only): nil
	// is every season, 0 the specials.
	ParentIndexNumber *int
	IDs               string // comma-separated item ids
	Filters           string // e.g. IsPlayed, IsFavorite, IsResumable
	SortBy            string // e.g. DateCreated, DateModified, DatePlayed, SortName
	SortOrder         string // Ascending or Descending
	// SavedSince restricts to items whose metadata the server last saved at
	// or after this time (MinDateLastSaved): a date, or a time with or
	// without its zone (isDate); Search refuses anything else. It is the
	// closest thing both servers offer to "what changed": a file written
	// over an existing path is re-read and saved again, but so is an item
	// someone edited, so it is a net rather than a measurement.
	SavedSince string
	// Path finds the item holding exactly this file. Emby answers it;
	// Jellyfin's item query has no such parameter, so a caller needing it on
	// both backends reads a folder and compares paths itself.
	Path string
	// IsMissing true lists only the server's records of episodes it has no
	// file for, which Jellyfin keeps with the TheTVDB plugin and leaves out
	// of an item query unless asked. Emby's item query has no such
	// parameter, and a search passing it there is refused rather than
	// answered with every episode.
	IsMissing      *bool
	UserID         string // user context: adds watch state to UserData
	EnableUserData bool
	Fields         string // override FieldsDefault
	Limit          int
	StartIndex     int
	// PageSize is how many items ReadAll asks for a request,
	// past the overlap each re-reads; 0 is readPage.
	PageSize int
}

// Search returns matching library items plus the total match count
// (which may exceed len(items) when Limit pages the results).
func (c *Client) Search(ctx context.Context, opts SearchOptions) ([]Item, int, error) {
	if opts.SavedSince != "" && !isDate(opts.SavedSince) {
		// both servers refuse it, Emby with a bare 500 and Jellyfin a 400
		return nil, 0, fmt.Errorf("saved since %q is not a date: give one such as 2026-01-02 or 2026-01-02T15:04:05Z", opts.SavedSince)
	}
	fields := opts.Fields
	if fields == "" {
		fields = FieldsDefault
	}
	if c.isEmby() {
		return c.searchEmby(ctx, opts, fields)
	}

	return c.searchJF(ctx, opts, fields)
}

// isDate says whether s is a date both servers read as a saved-since: a day
// alone, or a time of it with or without its zone, to any fraction of a
// second (seen on Emby 4.10 and Jellyfin 12.1).
func isDate(s string) bool {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", time.DateOnly} {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}

	return false
}

// embyPlayFields are the fields that make Emby's lists carry a user's play
// count and when they last played an item, which they otherwise leave out of
// UserData (seen on 4.10: 0 and nothing, where the single-item read has both).
const embyPlayFields = "UserDataPlayCount,UserDataLastPlayedDate"

func (c *Client) searchEmby(ctx context.Context, opts SearchOptions, fields string) ([]Item, int, error) {
	if opts.IsMissing != nil {
		return nil, 0, errors.New("emby's item query has no filter for missing episodes: read an episode's record by its having no file")
	}
	if opts.EnableUserData {
		fields += "," + embyPlayFields
	}
	o := emby.GetItemsOperationOptions{
		Recursive:        new(!opts.Direct),
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
		MediaTypes:        opts.MediaTypes,
		ArtistIds:         opts.ArtistIDs,
		Ids:               opts.IDs,
		Filters:           opts.Filters,
		SortBy:            opts.SortBy,
		SortOrder:         opts.SortOrder,
		MinDateLastSaved:  opts.SavedSince,
		Path:              opts.Path,
		ParentIndexNumber: opts.ParentIndexNumber,
		Limit:             nz(opts.Limit),
		StartIndex:        nz(opts.StartIndex),
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
		res = orEmpty(resp.Model)
	} else {
		resp, err := c.emby.GetItems(ctx, o)
		if err != nil {
			return nil, 0, err
		}
		res = orEmpty(resp.Model)
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
		MediaTypes:        o.MediaTypes,
		ArtistIds:         o.ArtistIds,
		MinDateLastSaved:  o.MinDateLastSaved,
		Path:              o.Path,
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
		Recursive: new(!opts.Direct),
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
		MediaTypes:          list[jf.MediaType](opts.MediaTypes),
		ArtistIds:           list[string](opts.ArtistIDs),
		ParentIndexNumber:   opts.ParentIndexNumber,
		Ids:                 list[string](opts.IDs),
		Filters:             list[jf.ItemFilter](opts.Filters),
		SortBy:              list[jf.ItemSortBy](opts.SortBy),
		SortOrder:           list[jf.SortOrder](opts.SortOrder),
		MinDateLastSaved:    opts.SavedSince,
		IsMissing:           opts.IsMissing,
		UserId:              opts.UserID,
		Limit:               nz(opts.Limit),
		StartIndex:          nz(opts.StartIndex),
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

// NoItemError is a lookup of an id the item query lists no item for, told
// apart from a lookup that failed: a caller may still find the id as a
// version folded into another item, which no item query lists.
type NoItemError struct{ ID string }

func (e *NoItemError) Error() string { return "no item with id " + e.ID }

// ItemByID fetches a single item with full detail fields.
func (c *Client) ItemByID(ctx context.Context, id string) (*Item, error) {
	// Emby drops an Ids filter it cannot parse and answers with the whole
	// library, so the answer has to be the item that was asked for, and the
	// query is capped so that whole library is never actually pulled
	items, _, err := c.Search(ctx, SearchOptions{IDs: id, Fields: FieldsDetail, Limit: 2})
	if err != nil {
		return nil, err
	}

	if len(items) == 0 || items[0].ID != id {
		return nil, &NoItemError{ID: id}
	}

	return &items[0], nil
}

// ProviderIDTypes are the item types a provider id is looked up among: the
// films and series a library holds. A provider numbers other things apart
// (TMDB's collection 10 is not its film 10), and a box set, season or episode
// carrying the same number is not the film or the series, so both servers
// are asked for these alone. TMDB and TheTVDB number films and series apart
// too - TMDB's film 1396 is not its series 1396 - so a caller looking one up
// by those names which it means.
const ProviderIDTypes = "Movie,Series"

// ItemsByProviderID looks up the items of the given types (Movie, Series, or
// both, comma-separated) carrying a metadata provider id, e.g. ("tmdb",
// "89998", "Movie"). Emby supports this server-side; Jellyfin lacks the query
// parameter, so we fall back to scanning by type and filtering client-side,
// and changed says when the library changed under that read (see ReadAll).
func (c *Client) ItemsByProviderID(ctx context.Context, provider, id, types string) ([]Item, string, error) {
	if c.isEmby() {
		res, err := c.emby.GetItems(ctx, emby.GetItemsOperationOptions{
			Recursive:           new(true),
			Fields:              FieldsDefault,
			IncludeItemTypes:    types,
			AnyProviderIdEquals: provider + "." + id,
		})
		if err != nil {
			return nil, "", err
		}

		return itemsFromEmby(orEmpty(res.Model).Items), "", nil
	}

	// Jellyfin fallback: page through the films or series and match locally.
	var matches []Item
	result, err := c.ReadAll(ctx, SearchOptions{IncludeItemTypes: types}, ToAnswer, func(items []Item) bool {
		for i := range items {
			it := &items[i]
			for k, v := range it.ProviderIDs {
				if strings.EqualFold(k, provider) && v == id {
					matches = append(matches, *it)
				}
			}
		}
		return true
	})
	if err != nil {
		return nil, "", err
	}

	return matches, result.Changed(), nil
}

// ItemsByAnyProviderID finds, on Emby, the films and series carrying any of
// the ids given, each as "provider.id" ("tmdb.348"), with one read for them
// all: Emby's filter takes several, comma-separated, of any providers (seen
// on 4.10). Only an item carrying one of them is kept, whatever the server
// answers. Jellyfin has no such filter: an error.
func (c *Client) ItemsByAnyProviderID(ctx context.Context, ids []string) ([]Item, error) {
	if !c.isEmby() {
		return nil, errors.New("jellyfin cannot find items by several provider ids at once")
	}
	res, err := c.emby.GetItems(ctx, emby.GetItemsOperationOptions{
		Recursive:           new(true),
		Fields:              FieldsDefault,
		IncludeItemTypes:    ProviderIDTypes,
		AnyProviderIdEquals: strings.Join(ids, ","),
	})
	if err != nil {
		return nil, err
	}
	var out []Item
	found := itemsFromEmby(orEmpty(res.Model).Items)
	for i := range found {
		it := &found[i]
		for k, v := range it.ProviderIDs {
			if slices.Contains(ids, strings.ToLower(k)+"."+v) {
				out = append(out, *it)
				break
			}
		}
	}

	return out, nil
}

// Similar returns items the server considers similar to the given one.
// userID is required: Emby returns HTTP 500 for /Similar without one.
func (c *Client) Similar(ctx context.Context, id, userID string, limit int) ([]Item, error) {
	if c.isEmby() {
		res, err := c.emby.GetItemsByIdSimilar(ctx, id, emby.GetItemsByIdSimilarOperationOptions{UserId: userID, Fields: FieldsDefault, Limit: nz(limit)})
		if err != nil {
			return nil, err
		}

		return itemsFromEmby(orEmpty(res.Model).Items), nil
	}

	res, err := c.jf.GetSimilarItems(ctx, id, jf.GetSimilarItemsOperationOptions{UserId: userID, Fields: list[jf.ItemFields](FieldsDefault), Limit: nz(limit)})
	if err != nil {
		return nil, err
	}

	return itemsFromJF(res.Model.Items), nil
}

// InstantMix builds a music mix seeded from a song, album, artist, or genre.
func (c *Client) InstantMix(ctx context.Context, id string, limit int) ([]Item, error) {
	if c.isEmby() {
		res, err := c.emby.GetItemsByIdInstantMix(ctx, id, emby.GetItemsByIdInstantMixOperationOptions{Fields: FieldsDefault, Limit: nz(limit)})
		if err != nil {
			return nil, err
		}
		items := itemsFromEmby(orEmpty(res.Model).Items)
		if len(items) > 0 {
			return items, nil
		}
		// Emby answers a mix asked of a playlist with nothing, through the
		// items' route and its playlists' own alike (seen on 4.10: it reads a
		// playlist's genres off children a playlist does not have), so a
		// playlist's mix is made of its songs' mixes, taken in turn
		// a seed no item is has no mix, as the route said; a read that failed
		// says nothing either way
		seed, err := c.ItemByID(ctx, id)
		var none *NoItemError
		switch {
		case errors.As(err, &none):
			return items, nil
		case err != nil:
			return nil, err
		case seed.Type != "Playlist":
			return items, nil
		}
		songs, _, err := c.Search(ctx, SearchOptions{ParentID: id, IncludeItemTypes: "Audio", Fields: FieldsLean, Limit: playlistMixSeeds})
		if err != nil {
			return nil, err
		}
		mixes := make([][]Item, 0, len(songs))
		for i := range songs {
			res, err := c.emby.GetItemsByIdInstantMix(ctx, songs[i].ID, emby.GetItemsByIdInstantMixOperationOptions{Fields: FieldsDefault, Limit: nz(limit)})
			if err != nil {
				return nil, err
			}
			mixes = append(mixes, itemsFromEmby(orEmpty(res.Model).Items))
		}

		return interleave(mixes, limit), nil
	}

	res, err := c.jf.GetInstantMixFromItem(ctx, id, jf.GetInstantMixFromItemOperationOptions{Fields: list[jf.ItemFields](FieldsDefault), Limit: nz(limit)})
	if err != nil {
		return nil, err
	}

	return itemsFromJF(res.Model.Items), nil
}

// playlistMixSeeds is how many of a playlist's songs its mix is made from on
// Emby.
const playlistMixSeeds = 5

// interleave takes lists in turn, the first of each, then the second, each
// item once, until limit (0 for all of them).
func interleave(lists [][]Item, limit int) []Item {
	out := []Item{}
	seen := map[string]bool{}
	for i := 0; ; i++ {
		more := false
		for _, l := range lists {
			if i >= len(l) {
				continue
			}
			more = true
			if seen[l[i].ID] {
				continue
			}
			seen[l[i].ID] = true
			out = append(out, l[i])
			if limit > 0 && len(out) == limit {
				return out
			}
		}
		if !more {
			return out
		}
	}
}

// RefreshItem asks the server to re-fetch metadata and images for one item,
// and waits for the refresh to land: both servers answer once it is queued,
// and a change made to the item before it has run is undone by it (a
// replace_all refresh put the providers' tags back over an edit made straight
// after it answered, in about half of tries on both servers). It answers
// whether the refresh was seen to save the item within the wait (see
// awaitSave); a refresh queued behind a scan can take longer. A series' or a
// season's own record is what is waited on: the refresh reaches the episodes
// under it after. An edit of the item from this process waits for it.
func (c *Client) RefreshItem(ctx context.Context, id string, replaceAll bool) (bool, error) {
	unlock := c.lockItem(id)
	defer unlock()

	_, before, err := c.unsavedFor(ctx, id)
	if err != nil {
		return false, err
	}
	var replace *bool
	if replaceAll {
		replace = new(true)
	}
	if c.isEmby() {
		_, err = c.emby.PostItemsByIdRefresh(ctx, id, emby.BaseRefreshRequest{}, emby.PostItemsByIdRefreshOperationOptions{
			MetadataRefreshMode: emby.MetadataRefreshModeFullRefresh,
			ImageRefreshMode:    emby.MetadataRefreshModeFullRefresh,
			ReplaceAllMetadata:  replace,
			ReplaceAllImages:    replace,
		})
	} else {
		_, err = c.jf.RefreshItem(ctx, id, jf.RefreshItemOperationOptions{
			MetadataRefreshMode: jf.MetadataRefreshModeFullRefresh,
			ImageRefreshMode:    jf.MetadataRefreshModeFullRefresh,
			ReplaceAllMetadata:  replace,
			ReplaceAllImages:    replace,
		})
	}
	if err != nil {
		return false, err
	}
	landed, err := c.awaitSave(ctx, id, before)
	if err != nil {
		return false, fmt.Errorf("the refresh was asked for and runs whatever happens next, but reading the item back for it failed: %w", err)
	}

	return landed, nil
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

// DeleteItems permanently removes items AND their media files from disk, in
// one request. The two servers differ on an id they cannot find: Emby skips
// it, Jellyfin works through the ids in order and answers 400 at the first
// one, with the ones before it already gone. Emby deletes a folder's items
// with it.
func (c *Client) DeleteItems(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if c.isEmby() {
		_, err := c.emby.DeleteItems(ctx, emby.DeleteItemsOperationOptions{Ids: strings.Join(ids, ",")})
		return err
	}

	_, err := c.jf.DeleteItems(ctx, jf.DeleteItemsOperationOptions{Ids: ids})

	return err
}

// UserItem fetches one item in a user's context, with that user's watch
// state complete (Emby's lists carry the play count and last played date
// only when asked for them: embyPlayFields).
func (c *Client) UserItem(ctx context.Context, userID, itemID string) (*Item, error) {
	if c.isEmby() {
		res, err := c.emby.GetUsersByUserIdItemsById(ctx, userID, itemID)
		if err != nil {
			return nil, err
		}
		if res.Model == nil {
			return nil, noResult("no item with id %s", itemID)
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
		// a nil model would make a nil map, which an edit then writes to
		if res.Model == nil {
			return nil, noResult("no item with id %s", itemID)
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
// back what it read (see lockItem). edit changes the map and reports
// whether it changed anything; nothing is posted when it did not. The map is
// returned as edited.
func (c *Client) EditItem(ctx context.Context, userID, itemID string, edit func(full map[string]any) (bool, error)) (map[string]any, error) {
	unlock := c.lockItem(itemID)
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
// it out, so Emby asks that too. That list leaves out music albums and
// artists unless their kind is asked for, even for an administrator (seen
// live on Emby 4.10), so it is asked for the item's own kind.
//
// A 404 is also both servers' answer for an id no item has, which is not an
// item the user cannot see: read as one, a mistyped id was "nobody has
// watched this". So a 404 is checked against the library, and an id nothing
// has is an error.
func (c *Client) VisibleUserItem(ctx context.Context, userID, itemID string) (*Item, bool, error) {
	it, err := c.UserItem(ctx, userID, itemID)
	if IsNotFound(err) {
		if _, err := c.ItemByID(ctx, itemID); err != nil {
			return nil, false, err
		}

		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !c.isEmby() {
		return it, true, nil
	}

	items, _, err := c.Search(ctx, SearchOptions{IDs: itemID, UserID: userID, IncludeItemTypes: it.Type, Fields: "Path", Limit: 1})
	if err != nil {
		return nil, false, err
	}
	// an id Emby cannot parse drops the filter, so the answer must be the item
	if len(items) == 0 || items[0].ID != itemID {
		return nil, false, nil
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

// ResolveByType finds an item of the given type by id or by name
// (case-insensitive) — used for playlists and collections. Names need not be
// unique, so a name several share is an error that lists their ids.
func (c *Client) ResolveByType(ctx context.Context, itemType, nameOrID string) (*Item, error) {
	items, _, err := c.Search(ctx, SearchOptions{IncludeItemTypes: itemType, Fields: FieldsLean})
	if err != nil {
		return nil, err
	}

	kind := strings.ToLower(itemType)
	if itemType == "BoxSet" {
		kind = "collection"
	}
	names := make([]string, 0, len(items))
	var named []*Item
	for i := range items {
		if items[i].ID == nameOrID {
			return &items[i], nil
		}
		if strings.EqualFold(items[i].Name, nameOrID) {
			named = append(named, &items[i])
		}
		names = append(names, items[i].Name)
	}
	switch len(named) {
	case 1:
		return named[0], nil
	case 0:
		return nil, fmt.Errorf("no %s named %q (have: %s)", kind, nameOrID, strings.Join(names, ", "))
	}
	ids := make([]string, 0, len(named))
	for _, it := range named {
		ids = append(ids, it.ID)
	}

	return nil, fmt.Errorf("%d %ss are named %q (ids %s): pass an id", len(named), kind, nameOrID, strings.Join(ids, ", "))
}
