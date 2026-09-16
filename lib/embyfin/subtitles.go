package embyfin

import (
	"context"

	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
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
	if c.isEmby() {
		res, err := c.emby.GetItemsByIdRemoteSearchSubtitlesByLanguage(ctx, itemID, language, emby.GetItemsByIdRemoteSearchSubtitlesByLanguageOperationOptions{})
		if err != nil {
			return nil, err
		}
		subs := make([]RemoteSubtitle, 0, len(res.Model))
		for i := range res.Model {
			subs = append(subs, remoteSubtitleFromEmby(&res.Model[i]))
		}

		return subs, nil
	}

	res, err := c.jf.SearchRemoteSubtitles(ctx, itemID, language, jf.SearchRemoteSubtitlesOperationOptions{})
	if err != nil {
		return nil, err
	}
	subs := make([]RemoteSubtitle, 0, len(res.Model))
	for i := range res.Model {
		subs = append(subs, remoteSubtitleFromJF(&res.Model[i]))
	}

	return subs, nil
}

// DownloadSubtitle downloads a chosen remote subtitle to sit beside the item.
func (c *Client) DownloadSubtitle(ctx context.Context, itemID, subtitleID string) error {
	if c.isEmby() {
		_, err := c.emby.PostItemsByIdRemoteSearchSubtitlesBySubtitleId(ctx, itemID, subtitleID, emby.PostItemsByIdRemoteSearchSubtitlesBySubtitleIdOperationOptions{})
		return err
	}

	_, err := c.jf.DownloadRemoteSubtitles(ctx, itemID, subtitleID)

	return err
}
