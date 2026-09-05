package embyfin

import (
	"context"
	"fmt"
	"net/url"
	"strings"
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

type remoteSearchBody struct {
	SearchInfo map[string]any `json:"SearchInfo"`
	ItemID     string         `json:"ItemId"`
}

// RemoteSearch asks the metadata providers for candidate matches for an item.
// kind is "Movie" or "Series". name/year are optional overrides; when empty
// the server searches with the item's current metadata.
func (c *Client) RemoteSearch(ctx context.Context, kind, itemID, name string, year int) ([]RemoteSearchResult, error) {
	switch strings.ToLower(kind) {
	case "movie":
		kind = "Movie"
	case "series", "show", "tv":
		kind = "Series"
	default:
		return nil, fmt.Errorf("unsupported identify kind %q (want movie or series)", kind)
	}

	info := map[string]any{}
	if name != "" {
		info["Name"] = name
	}
	if year > 0 {
		info["Year"] = year
	}

	var results []RemoteSearchResult
	body := remoteSearchBody{SearchInfo: info, ItemID: itemID}
	if err := c.post(ctx, "/Items/RemoteSearch/"+kind, nil, body, &results); err != nil {
		return nil, err
	}

	return results, nil
}

// ApplyRemoteSearchResult rewrites an item's identity to the chosen candidate:
// the server re-fetches metadata and images for it.
func (c *Client) ApplyRemoteSearchResult(ctx context.Context, itemID string, result RemoteSearchResult, replaceAllImages bool) error {
	q := url.Values{}
	if replaceAllImages {
		q.Set("ReplaceAllImages", "true")
	}

	return c.post(ctx, "/Items/RemoteSearch/Apply/"+url.PathEscape(itemID), q, result, nil)
}
