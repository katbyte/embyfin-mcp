package embyfin

import (
	"context"
	"net/url"
)

type RemoteSubtitle struct {
	ID                         string  `json:"Id"`
	Name                       string  `json:"Name,omitempty"`
	ProviderName               string  `json:"ProviderName,omitempty"`
	Format                     string  `json:"Format,omitempty"`
	ThreeLetterISOLanguageName string  `json:"ThreeLetterISOLanguageName,omitempty"`
	DownloadCount              int     `json:"DownloadCount,omitempty"`
	CommunityRating            float64 `json:"CommunityRating,omitempty"`
	Comment                    string  `json:"Comment,omitempty"`
}

// SearchSubtitles lists remote subtitle candidates for an item in the given
// language (three-letter code, e.g. eng).
func (c *Client) SearchSubtitles(ctx context.Context, itemID, language string) ([]RemoteSubtitle, error) {
	var subs []RemoteSubtitle
	path := "/Items/" + url.PathEscape(itemID) + "/RemoteSearch/Subtitles/" + url.PathEscape(language)
	if err := c.get(ctx, path, nil, &subs); err != nil {
		return nil, err
	}

	return subs, nil
}

// DownloadSubtitle downloads a chosen remote subtitle to sit beside the item.
func (c *Client) DownloadSubtitle(ctx context.Context, itemID, subtitleID string) error {
	path := "/Items/" + url.PathEscape(itemID) + "/RemoteSearch/Subtitles/" + url.PathEscape(subtitleID)
	return c.post(ctx, path, nil, nil, nil)
}
