package embyfin

import (
	"context"
	"net/http"

	"github.com/katbyte/embyfin-mcp/lib/client"
)

// ActivityRetention is how many days of its activity log the server keeps,
// and whether it keeps a limit at all.
//
// Jellyfin deletes the entries older than its ActivityLogRetentionDays every
// day (a scheduled task; 30 days out of the box), so a history read over a
// longer period reaches the end of what is kept and reads as complete when
// it is not. Emby 4.10 has no such setting and no task that prunes the log
// (seen on 4.10: its configuration and its scheduled tasks name none), so it
// keeps what it has.
//
// Jellyfin's setting can be unset, which its task refuses to run with, and 0,
// which deletes everything before now. The document types it as a number the
// generated model reads as 0 either way, so it is read here as the server
// wrote it.
func (c *Client) ActivityRetention(ctx context.Context) (days int, limited bool, err error) {
	if c.isEmby() {
		return 0, false, nil
	}
	req, err := c.jf.Client.NewRequest(ctx, client.RequestOptions{
		ExpectedStatusCodes: []int{http.StatusOK},
		HTTPMethod:          http.MethodGet,
		Path:                "/System/Configuration",
	})
	if err != nil {
		return 0, false, err
	}
	resp, err := req.Execute(ctx)
	if err != nil {
		return 0, false, err
	}
	var config struct {
		ActivityLogRetentionDays *int `json:"ActivityLogRetentionDays"`
	}
	if err := resp.Unmarshal(&config); err != nil {
		return 0, false, err
	}
	if config.ActivityLogRetentionDays == nil || *config.ActivityLogRetentionDays < 0 {
		return 0, false, nil
	}

	return *config.ActivityLogRetentionDays, true, nil
}
