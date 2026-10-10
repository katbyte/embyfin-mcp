package embyfin

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/sdk/emby"
	"github.com/katbyte/pandorest/client"
)

// Preview thumbnails: the small frames a player shows over the seek bar as
// the viewer scrubs.
//
// Emby keeps a video's in one file, a BIF (Roku's format: a header, an index
// and the JPEGs one after another), named after the video with the frames'
// width and how many seconds apart they are - "Film (1979)-320-10.bif" -
// beside the video or in the server's metadata folder, as the library says.
// A library makes them when its options have extraction on and an interval
// above zero, both; with either off a refresh makes none (seen on 4.10.1).
//
// GET /Videos/{Id}/index.bif answers the file as it is on disk at the time
// of asking: a file deleted behind the server's back is gone from the
// answer at once, and a damaged one is answered damaged. With no file it
// answers an empty set, a header counting no thumbnails and nothing after
// it. It honours Range, so the header - and after it the one index entry
// that says where the file ends - is all that is read of a file: 72 bytes.
// It answers the same empty set for an id that is no video, so it is asked
// only about videos.
//
// GET /Items/{Id}/ThumbnailSet is no use here: whenever any file is there it
// lists a position every interval up to the video's runtime, whatever the
// file holds, an empty one too.
//
// Jellyfin keeps trickplay tiles instead, in another shape and by other
// routes, which this does not read.

// PreviewWidth is the width, in pixels, of the frames Emby makes: the one
// width its files are named for.
const PreviewWidth = 320

// ErrNoPreviewFiles is the answer for a server that keeps no preview
// thumbnail files of this kind.
var ErrNoPreviewFiles = errors.New("preview thumbnails are read on Emby alone so far: Jellyfin keeps trickplay tiles, in another shape and by other routes")

// The BIF layout: eight bytes that begin every file, a version, how many
// frames, how many milliseconds apart, and reserved space to byte 64; then
// an index of one entry a frame and one more to end it, eight bytes each (a
// frame's number and where its JPEG begins), the last entry's place being
// where the file ends.
const (
	bifHeader     = 64
	bifIndexEntry = 8
	// bifMostFrames is more frames than any file holds: a day of video at
	// a frame a second. A count above it is not a count
	bifMostFrames = 24 * 60 * 60
)

var bifMagic = [8]byte{0x89, 'B', 'I', 'F', 0x0d, 0x0a, 0x1a, 0x0a}

// Preview is what the server holds of one video's preview thumbnails.
type Preview struct {
	// Size is the file's size in bytes, and -1 when the server did not say
	Size int64
	// Thumbnails is how many frames the file's header counts
	Thumbnails int
	// Interval is how far apart the frames are
	Interval time.Duration
	// Damage says what is wrong with the file, when it is not one a player
	// can read: empty for a sound file, and for no file at all
	Damage string
}

// Missing reports that the server holds no preview thumbnails for the video.
func (p *Preview) Missing() bool { return p.Damage == "" && p.Thumbnails == 0 }

// Sound reports that the server holds a file a player can read.
func (p *Preview) Sound() bool { return p.Damage == "" && p.Thumbnails > 0 }

// Covers is how much of a video the frames reach across.
func (p *Preview) Covers() time.Duration { return time.Duration(p.Thumbnails) * p.Interval }

// bifRange asks for some bytes of a video's preview file.
type bifRange struct{ first, last int64 }

func (o bifRange) ToHeaders() *client.Headers {
	h := client.Headers{}
	h.Append("Range", fmt.Sprintf("bytes=%d-%d", o.first, o.last))

	return &h
}

func (bifRange) ToQuery() *client.QueryParams {
	q := client.QueryParams{}
	q.Append("Width", strconv.Itoa(PreviewWidth))

	return &q
}

// previewBytes reads bytes first to last of a video's preview file, and the
// file's size where the server says it. A server that answers the whole
// file to a request for part of it is read up to last and no further.
func (c *Client) previewBytes(ctx context.Context, itemID string, first, last int64) (got []byte, size int64, err error) {
	req, err := c.emby.Client.NewRequest(ctx, client.RequestOptions{
		// 416 is the answer to a range past the end of a file, which is the
		// answer for every range of an empty one
		ExpectedStatusCodes: []int{http.StatusOK, http.StatusPartialContent, http.StatusRequestedRangeNotSatisfiable},
		HTTPMethod:          http.MethodGet,
		OptionsObject:       bifRange{first, last},
		Path:                fmt.Sprintf("/Videos/%s/index.bif", url.PathEscape(itemID)),
		StreamResponse:      true,
	})
	if err != nil {
		return nil, 0, err
	}
	resp, err := req.Execute(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	want := last - first + 1
	size = -1
	switch resp.StatusCode {
	case http.StatusPartialContent, http.StatusRequestedRangeNotSatisfiable:
		// "bytes 0-63/412030", and "bytes */0" or "bytes 0-0/0" of an empty file
		if _, total, ok := strings.Cut(resp.Header.Get("Content-Range"), "/"); ok {
			if n, perr := strconv.ParseInt(strings.TrimSpace(total), 10, 64); perr == nil {
				size = n
			}
		}
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			return nil, size, nil
		}
	default:
		size = resp.ContentLength
		if _, err := io.CopyN(io.Discard, resp.Body, first); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, size, nil
			}

			return nil, size, fmt.Errorf("reading the preview thumbnails of %s: %w", itemID, err)
		}
	}
	got, err = io.ReadAll(io.LimitReader(resp.Body, want))
	if err != nil {
		return nil, size, fmt.Errorf("reading the preview thumbnails of %s: %w", itemID, err)
	}

	return got, size, nil
}

// PreviewOf reads what the server holds of a video's preview thumbnails:
// its file's header, and for a file with frames the end of its index, which
// together say whether the file is whole. The id has to be a video's.
func (c *Client) PreviewOf(ctx context.Context, itemID string) (Preview, error) {
	if !c.isEmby() {
		return Preview{}, ErrNoPreviewFiles
	}
	head, size, err := c.previewBytes(ctx, itemID, 0, bifHeader-1)
	if err != nil {
		return Preview{}, err
	}
	p := Preview{Size: size}
	switch {
	case size == 0 || len(head) == 0:
		p.Damage = "the file is empty"

		return p, nil
	case len(head) < bifHeader:
		p.Damage = fmt.Sprintf("the file is %d bytes, less than the header every one begins with", len(head))

		return p, nil
	case [8]byte(head[:8]) != bifMagic:
		p.Damage = "the file is not a set of thumbnails: it does not begin as one"

		return p, nil
	}
	frames := int64(binary.LittleEndian.Uint32(head[12:16]))
	// the interval is in milliseconds, and none written means a second
	p.Interval = time.Duration(binary.LittleEndian.Uint32(head[16:20])) * time.Millisecond
	if p.Interval == 0 {
		p.Interval = time.Second
	}
	if frames > bifMostFrames {
		p.Damage = fmt.Sprintf("the file's header counts %d thumbnails, which is no count", frames)

		return p, nil
	}
	p.Thumbnails = int(frames)
	index := bifHeader + bifIndexEntry*(frames+1)
	switch {
	case frames == 0 && size > index:
		p.Damage = fmt.Sprintf("the file's header counts no thumbnails, in a file of %d bytes", size)

		return p, nil
	case frames == 0:
		// the empty set the server answers with when it holds no file
		return p, nil
	case size >= 0 && size < index:
		p.Damage = fmt.Sprintf("the file is cut off: its header counts %d thumbnails, whose index alone ends at byte %d, and the file is %d bytes", frames, index, size)

		return p, nil
	}

	// the entry closing the index says where the last frame ends, which is
	// where the file does
	end, _, err := c.previewBytes(ctx, itemID, index-bifIndexEntry, index-1)
	if err != nil {
		return Preview{}, err
	}
	if len(end) < bifIndexEntry {
		p.Damage = fmt.Sprintf("the file is cut off: its header counts %d thumbnails, and the index ends before the entry that closes it", frames)

		return p, nil
	}
	if ends := int64(binary.LittleEndian.Uint32(end[4:8])); size >= 0 && ends != size {
		p.Damage = fmt.Sprintf("the file is cut off or added to: its index ends the last thumbnail at byte %d, and the file is %d bytes", ends, size)
	}

	return p, nil
}

// RegeneratePreview asks Emby to make a video's preview thumbnails again,
// replacing a file that is there, and waits for the refresh that makes them
// to run, for patience at most. It answers what the server holds after, and
// whether the refresh was seen to run: a video whose refresh ran and left
// no sound file is one Emby could make none for.
//
// Emby has no request for the thumbnails alone. On 4.10.1 its scheduled
// task passes over a video it made them for before, a deleted file or no,
// and a refresh makes them only when it is a full one of the item's
// metadata and says to replace the thumbnails: with the metadata mode at
// its default or at validation, or the flag unset, it makes none (4.11.0.6
// documents the same routes and no other). So this is the narrowest refresh
// that works: full for metadata, replacing none of it; validation only for
// images, fetching none. It still does what a search for missing metadata
// does - reads the file's streams again, fills a field the item has empty
// from its nfo or its providers, and in a library that saves nfo files
// writes the item's again, the same when nothing changed.
//
// Emby runs the refreshes it is asked for one at a time, in the order asked
// (seen on 4.10.1: eight asked at once ran end to end), and makes the file
// before it saves the item, so the save is the sign that it is done.
func (c *Client) RegeneratePreview(ctx context.Context, itemID string, patience time.Duration) (Preview, bool, error) {
	if !c.isEmby() {
		return Preview{}, false, ErrNoPreviewFiles
	}
	unlock := c.lockItem(itemID)
	defer unlock()

	_, before, err := c.unsavedFor(ctx, itemID)
	if err != nil {
		return Preview{}, false, err
	}
	if _, err := c.emby.PostItemsByIdRefresh(ctx, itemID, emby.BaseRefreshRequest{ReplaceThumbnailImages: new(true)}, emby.PostItemsByIdRefreshOperationOptions{
		MetadataRefreshMode: emby.MetadataRefreshModeFullRefresh,
		ImageRefreshMode:    emby.MetadataRefreshModeValidationOnly,
		ReplaceAllMetadata:  new(false),
		ReplaceAllImages:    new(false),
	}); err != nil {
		return Preview{}, false, err
	}
	// a read of the item's save waits a minute at most, and a long video
	// behind others takes longer. A server that sends no etag has no save
	// to see, and the file is watched for instead
	ran := false
	for until := time.Now().Add(patience); ; {
		if before.etag == "" {
			if err := c.pause(ctx); err != nil {
				return Preview{}, false, err
			}
		} else if ran, err = c.awaitSave(ctx, itemID, before); err != nil {
			return Preview{}, false, fmt.Errorf("the thumbnails were asked for and are made whatever happens next, but reading the item back for them failed: %w", err)
		}
		after, err := c.PreviewOf(ctx, itemID)
		if err != nil {
			return Preview{}, ran, fmt.Errorf("the thumbnails were asked for, but reading them back failed: %w", err)
		}
		if ran || before.etag == "" && after.Sound() || !time.Now().Before(until) {
			return after, ran, nil
		}
	}
}
