package embyfin

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
)

// RemoteSearchResult is a metadata provider's candidate match for an item.
type RemoteSearchResult struct {
	Name               string            `json:"Name"`
	ProductionYear     int               `json:"ProductionYear,omitempty"`
	PremiereDate       string            `json:"PremiereDate,omitempty"`
	Overview           string            `json:"Overview,omitempty"`
	ProviderIDs        map[string]string `json:"ProviderIds,omitempty"`
	SearchProviderName string            `json:"SearchProviderName,omitempty"`
	ImageURL           string            `json:"ImageUrl,omitempty"`
}

// RemoteSearch asks the metadata providers for candidate matches for an item.
// kind is "Movie" or "Series". name/year are optional overrides; when empty
// the item's current name and year are used (Emby returns nothing for an
// empty SearchInfo, it does not fall back to the item itself).
func (c *Client) RemoteSearch(ctx context.Context, kind, itemID, name string, year int) ([]RemoteSearchResult, error) {
	switch strings.ToLower(kind) {
	case "movie":
		kind = "Movie"
	case "series", "show", "tv":
		kind = "Series"
	default:
		return nil, fmt.Errorf("unsupported identify kind %q (want movie or series)", kind)
	}

	if name == "" || year <= 0 {
		it, err := c.ItemByID(ctx, itemID)
		if err != nil {
			return nil, err
		}
		if name == "" {
			name = it.Name
		}
		if year <= 0 {
			year = it.ProductionYear
		}
	}

	search := c.remoteSearchJF
	if c.isEmby() {
		search = c.remoteSearchEmby
	}
	results, err := search(ctx, kind, itemID, name, year)
	if err != nil || len(results) > 0 || itemID == "" {
		return results, err
	}

	// with an item, the server asks only the fetchers its library has on, so
	// one in a library built from files and nfos alone answers nothing; the
	// same search without the item asks every provider, and applying a
	// result to the item still sets its ids
	return search(ctx, kind, "", name, year)
}

// remoteSearchEmby runs the search; Emby's document types the query's
// ItemId as an integer.
func (c *Client) remoteSearchEmby(ctx context.Context, kind, itemID, name string, year int) ([]RemoteSearchResult, error) {
	var id int64
	if itemID != "" {
		var err error
		if id, err = embyID(itemID); err != nil {
			return nil, err
		}
	}
	var err error

	var results []emby.RemoteSearchResult
	if kind == "Movie" {
		var res emby.PostItemsRemoteSearchMovieOperationResponse
		res, err = c.emby.PostItemsRemoteSearchMovie(ctx, emby.RemoteSearchQueryMovieInfo{
			ItemId: id, SearchInfo: &emby.MovieInfo{Name: name, Year: year},
		})
		results = res.Model
	} else {
		var res emby.PostItemsRemoteSearchSeriesOperationResponse
		res, err = c.emby.PostItemsRemoteSearchSeries(ctx, emby.RemoteSearchQuerySeriesInfo{
			ItemId: id, SearchInfo: &emby.SeriesInfo{Name: name, Year: year},
		})
		results = res.Model
	}
	if err != nil {
		return nil, err
	}

	out := make([]RemoteSearchResult, 0, len(results))
	for i := range results {
		out = append(out, remoteSearchResultFromEmby(&results[i]))
	}

	return out, nil
}

func (c *Client) remoteSearchJF(ctx context.Context, kind, itemID, name string, year int) ([]RemoteSearchResult, error) {
	var (
		results []jf.RemoteSearchResult
		err     error
	)
	if kind == "Movie" {
		var res jf.GetMovieRemoteSearchResultsOperationResponse
		res, err = c.jf.GetMovieRemoteSearchResults(ctx, jf.MovieInfoRemoteSearchQuery{
			ItemId: itemID, SearchInfo: &jf.MovieInfo{Name: name, Year: year},
		})
		results = res.Model
	} else {
		var res jf.GetSeriesRemoteSearchResultsOperationResponse
		res, err = c.jf.GetSeriesRemoteSearchResults(ctx, jf.SeriesInfoRemoteSearchQuery{
			ItemId: itemID, SearchInfo: &jf.SeriesInfo{Name: name, Year: year},
		})
		results = res.Model
	}
	if err != nil {
		return nil, err
	}

	out := make([]RemoteSearchResult, 0, len(results))
	for i := range results {
		out = append(out, remoteSearchResultFromJF(&results[i]))
	}

	return out, nil
}

// ApplyRemoteSearchResult rewrites an item's identity to the chosen candidate,
// waits for it to take, and returns the item as it then is. The servers answer
// the apply before the refresh it starts has run (Emby) or after (Jellyfin),
// and neither says when the identity did not take: Emby re-reads an nfo
// beside the file that names the item and keeps that nfo's ids, adding the
// candidate's others. So the item is read back until it carries the
// candidate's id (tmdb, else imdb, else tvdb), and ids that change to
// something else and stay so are an error naming what the server kept.
func (c *Client) ApplyRemoteSearchResult(ctx context.Context, itemID string, result RemoteSearchResult, replaceAllImages bool) (*Item, error) {
	before, err := c.appliedItem(ctx, itemID)
	if err != nil {
		return nil, err
	}

	var replace *bool
	if replaceAllImages {
		replace = new(true)
	}
	if c.isEmby() {
		_, err = c.emby.PostItemsRemoteSearchApplyById(ctx, itemID, remoteSearchResultToEmby(&result), emby.PostItemsRemoteSearchApplyByIdOperationOptions{ReplaceAllImages: replace})
	} else {
		_, err = c.jf.ApplySearchCriteria(ctx, itemID, remoteSearchResultToJF(&result), jf.ApplySearchCriteriaOperationOptions{ReplaceAllImages: replace})
	}
	if err != nil {
		return nil, err
	}

	provider, want := primaryProviderID(result.ProviderIDs)
	if provider == "" {
		return c.ItemByID(ctx, itemID) // nothing to know it by
	}
	// at the settle interval, a minute for the refresh to run and a few
	// seconds for other ids to show they are what the server settled on.
	//
	// Emby answers the apply before the refresh it starts has run, so an
	// item that already held the candidate's id reads as applied at once
	// with everything else as it was - the IMDb id the refresh is about to
	// correct, say. The item's etag moves when the server saves it, so a
	// read counts once the etag has moved and then held for a few reads
	// (Jellyfin has run the refresh by the time it answers, so its etag has
	// moved by the first read). A server that sends no etag cannot be waited
	// on this way, and is taken at its first matching read.
	const polls, settled, steady = 240, 12, 4
	other, held := 0, 0
	moved := before.Etag == ""
	last := before.Etag
	var it *Item
	for range polls {
		var readErr error
		if it, readErr = c.appliedItem(ctx, itemID); readErr != nil {
			return nil, readErr
		}
		if it.Etag != before.Etag {
			moved = true
		}
		have := providerIDOf(it.ProviderIDs, provider)
		switch {
		case have == want && moved:
			if it.Etag == last {
				held++
			} else {
				held = 0
			}
			if held >= steady || it.Etag == "" {
				return it, nil
			}
		case have == want:
			// the id is the candidate's, but the refresh has not saved yet
		case maps.Equal(it.ProviderIDs, before.ProviderIDs):
			other = 0 // not applied yet
		default:
			other++
			if other >= settled {
				return it, c.notApplied(provider, want, have)
			}
		}
		last = it.Etag
		if err := c.pause(ctx); err != nil {
			return nil, err
		}
	}

	// the wait ran out: an item holding the candidate's id took it, whether
	// or not a save of the refresh was seen
	if have := providerIDOf(it.ProviderIDs, provider); have != want {
		return it, c.notApplied(provider, want, have)
	}

	return it, nil
}

// appliedItem reads an item with its etag, which is what tells a read taken
// after the server saved it from one taken before.
func (c *Client) appliedItem(ctx context.Context, id string) (*Item, error) {
	items, _, err := c.Search(ctx, SearchOptions{IDs: id, Fields: FieldsDetail + ",Etag", Limit: 2})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 || items[0].ID != id {
		return nil, fmt.Errorf("no item with id %s", id)
	}

	return &items[0], nil
}

// SetProviderIDs gives an item a candidate's identity by a plain edit of the
// item: its metadata provider ids become ids, and nothing else changes or is
// fetched. It is the apply for a library with its metadata fetchers off,
// where the server's own apply has nothing to fetch and refreshes the item
// all the same - which on Jellyfin wiped every episode number of a series.
// The item is read back, and an id that did not take is an error.
func (c *Client) SetProviderIDs(ctx context.Context, userID, itemID string, ids map[string]string) (*Item, error) {
	if _, err := c.EditItem(ctx, userID, itemID, func(full map[string]any) (bool, error) {
		set := make(map[string]any, len(ids))
		for k, v := range ids {
			set[k] = v
		}
		full["ProviderIds"] = set

		return true, nil
	}); err != nil {
		return nil, err
	}

	it, err := c.ItemByID(ctx, itemID)
	if err != nil {
		return nil, err
	}
	if provider, want := primaryProviderID(ids); provider != "" {
		if have := providerIDOf(it.ProviderIDs, provider); have != want {
			return it, c.notApplied(provider, want, have)
		}
	}

	return it, nil
}

// notApplied is the error for an identity the server did not take.
func (c *Client) notApplied(provider, want, have string) error {
	if have == "" {
		have = "none"
	}
	err := fmt.Errorf("the server did not apply the candidate: the item's %s id is %s, not %s", provider, have, want)
	if c.isEmby() {
		err = fmt.Errorf("%w (Emby takes the identity from an nfo file beside the media that names one: correct or remove the nfo, then identify again)", err)
	}

	return err
}

// primaryProviderID is the id an item is best known by: tmdb, else imdb, else
// tvdb, with the provider named the way the tools name it.
func primaryProviderID(ids map[string]string) (provider, id string) {
	for _, p := range []string{"tmdb", "imdb", "tvdb"} {
		if v := providerIDOf(ids, p); v != "" {
			return p, v
		}
	}

	return "", ""
}

// providerIDOf reads a provider id whichever way the server spells the key.
func providerIDOf(ids map[string]string, provider string) string {
	for k, v := range ids {
		if strings.EqualFold(k, provider) {
			return v
		}
	}

	return ""
}
