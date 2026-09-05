package embyfin

import (
	"context"
	"net/url"
	"strconv"
)

type ImageInfo struct {
	ImageType string `json:"ImageType"`
	Width     int    `json:"Width,omitempty"`
	Height    int    `json:"Height,omitempty"`
	Size      int64  `json:"Size,omitempty"`
}

// Images lists the images an item currently has.
func (c *Client) Images(ctx context.Context, itemID string) ([]ImageInfo, error) {
	var images []ImageInfo
	if err := c.get(ctx, "/Items/"+url.PathEscape(itemID)+"/Images", nil, &images); err != nil {
		return nil, err
	}

	return images, nil
}

type RemoteImage struct {
	ProviderName    string  `json:"ProviderName,omitempty"`
	URL             string  `json:"Url"`
	Type            string  `json:"Type,omitempty"`
	Width           int     `json:"Width,omitempty"`
	Height          int     `json:"Height,omitempty"`
	Language        string  `json:"Language,omitempty"`
	CommunityRating float64 `json:"CommunityRating,omitempty"`
	VoteCount       int     `json:"VoteCount,omitempty"`
}

type remoteImagesResponse struct {
	Images           []RemoteImage `json:"Images"`
	TotalRecordCount int           `json:"TotalRecordCount"`
}

// RemoteImages lists provider image candidates for an item. imageType is e.g.
// Primary (poster), Backdrop, Logo, Thumb.
func (c *Client) RemoteImages(ctx context.Context, itemID, imageType string, limit int) ([]RemoteImage, int, error) {
	q := url.Values{}
	if imageType != "" {
		q.Set("Type", imageType)
	}
	if limit > 0 {
		q.Set("Limit", strconv.Itoa(limit))
	}

	var resp remoteImagesResponse
	if err := c.get(ctx, "/Items/"+url.PathEscape(itemID)+"/RemoteImages", q, &resp); err != nil {
		return nil, 0, err
	}

	return resp.Images, resp.TotalRecordCount, nil
}

// DownloadRemoteImage applies a provider image (by its URL from RemoteImages)
// as the item's image of the given type.
func (c *Client) DownloadRemoteImage(ctx context.Context, itemID, imageType, imageURL string) error {
	q := url.Values{}
	q.Set("Type", imageType)
	q.Set("ImageUrl", imageURL)

	return c.post(ctx, "/Items/"+url.PathEscape(itemID)+"/RemoteImages/Download", q, nil, nil)
}
