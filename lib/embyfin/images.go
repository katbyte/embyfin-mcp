package embyfin

import (
	"context"

	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
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
	if c.isEmby() {
		res, err := c.emby.GetItemsByIdImages(ctx, itemID)
		if err != nil {
			return nil, err
		}
		infos := res.Model
		images = make([]ImageInfo, 0, len(infos))
		for i := range infos {
			images = append(images, imageInfoFromEmby(&infos[i]))
		}

		return images, nil
	}

	res, err := c.jf.GetItemImageInfos(ctx, itemID)
	if err != nil {
		return nil, err
	}
	infos := res.Model
	images = make([]ImageInfo, 0, len(infos))
	for i := range infos {
		images = append(images, imageInfoFromJF(&infos[i]))
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

// RemoteImages lists provider image candidates for an item. imageType is e.g.
// Primary (poster), Backdrop, Logo, Thumb.
func (c *Client) RemoteImages(ctx context.Context, itemID, imageType string, limit int) ([]RemoteImage, int, error) {
	if c.isEmby() {
		res, err := c.emby.GetItemsByIdRemoteImages(ctx, itemID, emby.GetItemsByIdRemoteImagesOperationOptions{Type: emby.ImageType(imageType), Limit: nz(limit)})
		if err != nil {
			return nil, 0, err
		}
		images := make([]RemoteImage, 0, len(res.Model.Images))
		for i := range res.Model.Images {
			images = append(images, remoteImageFromEmby(&res.Model.Images[i]))
		}

		return images, res.Model.TotalRecordCount, nil
	}

	res, err := c.jf.GetRemoteImages(ctx, itemID, jf.GetRemoteImagesOperationOptions{Type: jf.ImageType(imageType), Limit: nz(limit)})
	if err != nil {
		return nil, 0, err
	}
	images := make([]RemoteImage, 0, len(res.Model.Images))
	for i := range res.Model.Images {
		images = append(images, remoteImageFromJF(&res.Model.Images[i]))
	}

	return images, res.Model.TotalRecordCount, nil
}

// DownloadRemoteImage applies a provider image (by its URL from RemoteImages)
// as the item's image of the given type.
func (c *Client) DownloadRemoteImage(ctx context.Context, itemID, imageType, imageURL string) error {
	if c.isEmby() {
		_, err := c.emby.PostItemsByIdRemoteImagesDownload(ctx, itemID, emby.ImagesBaseDownloadRemoteImage{}, emby.PostItemsByIdRemoteImagesDownloadOperationOptions{Type: emby.ImageType(imageType), ImageUrl: imageURL})
		return err
	}

	_, err := c.jf.DownloadRemoteImage(ctx, itemID, jf.DownloadRemoteImageOperationOptions{Type: jf.ImageType(imageType), ImageUrl: imageURL})

	return err
}
