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
	kind, err := searchKind(kind)
	if err != nil {
		return nil, err
	}

	if name == "" || year <= 0 {
		it, ierr := c.ItemByID(ctx, itemID)
		if ierr != nil {
			return nil, ierr
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
	results, err := search(ctx, kind, itemID, name, year, nil)
	if err != nil || len(results) > 0 || itemID == "" {
		return results, err
	}

	// with an item, the server asks only the fetchers its library has on, so
	// one in a library built from files and nfos alone answers nothing; the
	// same search without the item asks every provider, and applying a
	// result to the item still sets its ids
	return search(ctx, kind, "", name, year, nil)
}

// RemoteIDs asks the server's providers for everything they know a title by,
// given a candidate's name, year and ids: a TMDB search result carries TMDB's
// id alone, and the provider's own record of the title adds its IMDb id (and
// TVDB's, for a series). The name and year go too, for the providers that
// search by name rather than id. The answer is the ids given with the others
// added, or the ids given when no provider knows more.
func (c *Client) RemoteIDs(ctx context.Context, kind, name string, year int, ids map[string]string) (map[string]string, error) {
	out := maps.Clone(ids)
	provider, want := primaryProviderID(ids)
	if provider == "" {
		return out, nil
	}
	kind, err := searchKind(kind)
	if err != nil {
		return nil, err
	}
	search := c.remoteSearchJF
	if c.isEmby() {
		search = c.remoteSearchEmby
	}
	results, err := search(ctx, kind, "", name, year, ids)
	if err != nil {
		return nil, err
	}
	for i := range results {
		if providerIDOf(results[i].ProviderIDs, provider) != want {
			continue
		}
		for _, p := range []string{"tmdb", "imdb", "tvdb"} {
			if v := providerIDOf(results[i].ProviderIDs, p); v != "" && providerIDOf(out, p) == "" {
				out[providerKey(results[i].ProviderIDs, p)] = v
			}
		}

		break
	}

	return out, nil
}

// searchKind is the item type a remote search asks for, from the kind a tool
// takes.
func searchKind(kind string) (string, error) {
	switch strings.ToLower(kind) {
	case "movie":
		return "Movie", nil
	case "series", "show", "tv":
		return "Series", nil
	}

	return "", fmt.Errorf("unsupported identify kind %q (want movie or series)", kind)
}

// providerKey is the key a provider's id is held under, spelled as the server
// spelled it.
func providerKey(ids map[string]string, provider string) string {
	for k := range ids {
		if strings.EqualFold(k, provider) {
			return k
		}
	}

	return provider
}

// remoteSearchEmby runs the search; Emby's document types the query's
// ItemId as an integer.
func (c *Client) remoteSearchEmby(ctx context.Context, kind, itemID, name string, year int, ids map[string]string) ([]RemoteSearchResult, error) {
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
			ItemId: id, SearchInfo: &emby.MovieInfo{Name: name, Year: year, ProviderIds: ids},
		})
		results = res.Model
	} else {
		var res emby.PostItemsRemoteSearchSeriesOperationResponse
		res, err = c.emby.PostItemsRemoteSearchSeries(ctx, emby.RemoteSearchQuerySeriesInfo{
			ItemId: id, SearchInfo: &emby.SeriesInfo{Name: name, Year: year, ProviderIds: ids},
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

func (c *Client) remoteSearchJF(ctx context.Context, kind, itemID, name string, year int, ids map[string]string) ([]RemoteSearchResult, error) {
	var (
		results []jf.RemoteSearchResult
		err     error
	)
	if kind == "Movie" {
		var res jf.GetMovieRemoteSearchResultsOperationResponse
		res, err = c.jf.GetMovieRemoteSearchResults(ctx, jf.MovieInfoRemoteSearchQuery{
			ItemId: itemID, SearchInfo: &jf.MovieInfo{Name: name, Year: year, ProviderIds: ids},
		})
		results = res.Model
	} else {
		var res jf.GetSeriesRemoteSearchResultsOperationResponse
		res, err = c.jf.GetSeriesRemoteSearchResults(ctx, jf.SeriesInfoRemoteSearchQuery{
			ItemId: itemID, SearchInfo: &jf.SeriesInfo{Name: name, Year: year, ProviderIds: ids},
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
// candidate's id (tmdb, else imdb, else tvdb) and the refresh has saved it,
// and ids that change to something else and stay so are an error naming what
// the server kept. An edit of the item from this process waits for it.
func (c *Client) ApplyRemoteSearchResult(ctx context.Context, itemID string, result RemoteSearchResult, replaceAllImages bool) (*Item, error) {
	unlock := c.items.lock(itemID)
	defer unlock()

	// on Emby, once the item has gone a second unsaved, so the refresh's
	// save moves the etag (see unsavedFor): a match straight after another
	// change sat out the whole wait for a save it could not see
	before, state, err := c.unsavedFor(ctx, itemID)
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
	// correct, say. So a read counts once the refresh has saved the item and
	// it has held still (see awaitSave). A server that sends no etag cannot
	// be waited on this way, and is taken at its first matching read.
	const settled, steady = 12, 4
	other, held := 0, 0
	moved := state.etag == ""
	last := state
	var it *Item
	for range savePolls {
		var now saveState
		var readErr error
		if it, now, readErr = c.savedItem(ctx, itemID); readErr != nil {
			return nil, readErr
		}
		if now.etag != state.etag {
			moved = true
		}
		have := providerIDOf(it.ProviderIDs, provider)
		switch {
		case have == want && moved:
			if now == last {
				held++
			} else {
				held = 0
			}
			if held >= steady || now.etag == "" {
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
		last = now
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
