package embyfin

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// bifFile builds a preview thumbnail file of frames thumbnails every ms
// milliseconds, each a few bytes standing for its JPEG: the header, the
// index with the entry that closes it, and the frames.
func bifFile(frames, ms uint32) []byte {
	const each = 5
	b := make([]byte, bifHeader, bifHeader+bifIndexEntry*(frames+1)+each*frames)
	copy(b, bifMagic[:])
	binary.LittleEndian.PutUint32(b[12:], frames)
	binary.LittleEndian.PutUint32(b[16:], ms)
	at := bifHeader + bifIndexEntry*(frames+1)
	for i := range frames {
		b = binary.LittleEndian.AppendUint32(b, i)
		b = binary.LittleEndian.AppendUint32(b, at+each*i)
	}
	b = binary.LittleEndian.AppendUint32(b, 0xffffffff)
	b = binary.LittleEndian.AppendUint32(b, at+each*frames)

	return append(b, bytes.Repeat([]byte{0xff}, int(each*frames))...)
}

// previewServer answers one video's preview file the way Emby does: the
// bytes asked for, the file's size beside them. whole makes it answer the
// whole file to every request, as a server that does not read Range does.
type previewServer struct {
	mu     sync.Mutex
	file   []byte
	whole  bool
	ranges []string
	widths []string
}

func (p *previewServer) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	file, whole := p.file, p.whole
	p.ranges = append(p.ranges, r.Header.Get("Range"))
	p.widths = append(p.widths, r.URL.Query().Get("Width"))
	p.mu.Unlock()

	var first, last int
	if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &first, &last); err != nil || whole {
		w.Header().Set("Content-Length", strconv.Itoa(len(file)))
		_, _ = w.Write(file)

		return
	}
	if len(file) == 0 {
		// what Emby 4.10.1 answers for a file of no bytes
		w.Header().Set("Content-Range", "bytes 0-0/0")
		w.WriteHeader(http.StatusPartialContent)

		return
	}
	if first >= len(file) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(file)))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)

		return
	}
	last = min(last, len(file)-1)
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, len(file)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(file[first : last+1])
}

func previewClient(t *testing.T, mux *http.ServeMux) *Client {
	t.Helper()

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(Emby, srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	c.emby.Client.HTTPClient = srv.Client()
	c.settle, c.saveGrain = time.Millisecond, time.Millisecond

	return c
}

// A video's preview thumbnails are read off the file's first 64 bytes and,
// when it holds frames, the eight that close its index: sound, missing, or
// what is wrong with it. Every shape here is one Emby 4.10.1 was seen to
// answer for a file put in that state on disk.
func TestPreviewOfReadsTheFileItIsGiven(t *testing.T) {
	t.Parallel()

	sound := bifFile(703, 10000)
	cutInFrames := sound[:len(sound)-900]
	garbage := bytes.Repeat([]byte{0x5a}, 5000)
	for _, c := range []struct {
		name       string
		file       []byte
		whole      bool
		thumbnails int
		interval   time.Duration
		damage     string
		requests   int
	}{
		{name: "a sound file", file: sound, thumbnails: 703, interval: 10 * time.Second, requests: 2},
		{name: "a sound file from a server that answers the whole file", file: sound, whole: true, thumbnails: 703, interval: 10 * time.Second, requests: 2},
		// no file: the empty set, a header counting none and the entry closing an index of none
		{name: "no file", file: bifFile(0, 10000), interval: 10 * time.Second, requests: 1},
		{name: "a file with no interval written", file: bifFile(3, 0), thumbnails: 3, interval: time.Second, requests: 2},
		{name: "an empty file", file: nil, damage: "the file is empty", requests: 1},
		{name: "an empty file from a server that answers the whole file", file: nil, whole: true, damage: "the file is empty", requests: 1},
		{name: "a few bytes", file: sound[:20], damage: "the file is 20 bytes, less than the header every one begins with", requests: 1},
		{name: "not a thumbnail file", file: garbage, damage: "the file is not a set of thumbnails: it does not begin as one", requests: 1},
		{
			name: "cut off in its index", file: sound[:1000], thumbnails: 703, interval: 10 * time.Second, requests: 1,
			damage: "the file is cut off: its header counts 703 thumbnails, whose index alone ends at byte 5696, and the file is 1000 bytes",
		},
		{
			name: "cut off in its frames", file: cutInFrames, thumbnails: 703, interval: 10 * time.Second, requests: 2,
			damage: fmt.Sprintf("the file is cut off or added to: its index ends the last thumbnail at byte %d, and the file is %d bytes", len(sound), len(cutInFrames)),
		},
		{
			name: "a header counting none in a file that holds more", file: append(bifFile(0, 10000), garbage...), interval: 10 * time.Second, requests: 1,
			damage: "the file's header counts no thumbnails, in a file of 5072 bytes",
		},
	} {
		srv := &previewServer{file: c.file, whole: c.whole}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /Videos/42/index.bif", srv.serve)
		p, err := previewClient(t, mux).PreviewOf(t.Context(), "42")
		if err != nil {
			t.Errorf("%s: %v", c.name, err)

			continue
		}
		if p.Thumbnails != c.thumbnails || p.Damage != c.damage || p.Interval != c.interval {
			t.Errorf("%s: %d thumbnails %s apart, damage %q; want %d %s apart and %q", c.name, p.Thumbnails, p.Interval, p.Damage, c.thumbnails, c.interval, c.damage)
		}
		if p.Size != int64(len(c.file)) {
			t.Errorf("%s: size %d, want %d", c.name, p.Size, len(c.file))
		}
		if p.Sound() != (c.damage == "" && c.thumbnails > 0) || p.Missing() != (c.damage == "" && c.thumbnails == 0) {
			t.Errorf("%s: sound %v, missing %v", c.name, p.Sound(), p.Missing())
		}
		// the header, and for a file that holds frames the entry that
		// closes its index: never the frames, and always the width Emby
		// names its files for
		if len(srv.ranges) != c.requests || srv.ranges[0] != "bytes=0-63" {
			t.Errorf("%s: asked for %v, want %d requests beginning with the header", c.name, srv.ranges, c.requests)
		}
		if c.requests == 2 && srv.ranges[1] != fmt.Sprintf("bytes=%d-%d", bifHeader+bifIndexEntry*c.thumbnails, bifHeader+bifIndexEntry*(c.thumbnails+1)-1) {
			t.Errorf("%s: the second request is %q, want the entry that closes the index", c.name, srv.ranges[1])
		}
		if strings.Join(srv.widths, ",") != strings.TrimSuffix(strings.Repeat("320,", c.requests), ",") {
			t.Errorf("%s: widths asked for %v", c.name, srv.widths)
		}
	}

	if got := (&Preview{Thumbnails: 703, Interval: 10 * time.Second}).Covers(); got != 7030*time.Second {
		t.Errorf("703 thumbnails ten seconds apart cover %s", got)
	}
}

// Jellyfin keeps trickplay tiles, which nothing here reads: both calls say
// so and ask its server nothing.
func TestPreviewsAreEmbysAlone(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Jellyfin, map[string]route{})
	if _, err := c.PreviewOf(t.Context(), "42"); !errors.Is(err, ErrNoPreviewFiles) {
		t.Errorf("PreviewOf on Jellyfin: %v", err)
	}
	if _, _, err := c.RegeneratePreview(t.Context(), "42", time.Second); !errors.Is(err, ErrNoPreviewFiles) {
		t.Errorf("RegeneratePreview on Jellyfin: %v", err)
	}
	if n := len(f.requests); n != 0 {
		t.Errorf("Jellyfin was asked %d things", n)
	}
}

// Making a video's thumbnails again is the narrowest refresh that makes
// them - full for metadata and replacing none, validation only for images,
// the thumbnails to be replaced - and it is waited for: Emby makes the file
// and then saves the item, so the save is the sign it is done. A refresh
// that ran and left no file is told from one that never ran.
func TestRegeneratePreviewAsksNarrowlyAndWaits(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		// makes is whether the refresh leaves a file, and runs whether it
		// saves the item at all
		makes, runs bool
	}{
		{"a video Emby makes thumbnails for", true, true},
		{"a video Emby can make none for", false, true},
		{"a refresh that does not run in time", false, false},
	} {
		var mu sync.Mutex
		var query, body string
		asked, reads := false, 0
		file := &previewServer{file: bifFile(0, 10000)}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /Videos/42/index.bif", file.serve)
		mux.HandleFunc("POST /Items/42/Refresh", func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			asked, query, body = true, r.URL.RawQuery, string(b)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			if asked {
				reads++
			}
			etag := "e1"
			// the file is there a read before the item is saved
			if c.runs && reads >= 3 && c.makes {
				file.mu.Lock()
				file.file = bifFile(12, 10000)
				file.mu.Unlock()
			}
			if c.runs && reads >= 4 {
				etag = "e2"
			}
			mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"Items":[{"Id":"42","Name":"Zzyzx","Type":"Movie","Etag":%q}],"TotalRecordCount":1}`, etag)
		})

		after, ran, err := previewClient(t, mux).RegeneratePreview(t.Context(), "42", 20*time.Millisecond)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)

			continue
		}
		if ran != c.runs || after.Sound() != c.makes {
			t.Errorf("%s: ran %v, sound %v (%d thumbnails); want ran %v, sound %v", c.name, ran, after.Sound(), after.Thumbnails, c.runs, c.makes)
		}
		for _, want := range []string{"MetadataRefreshMode=FullRefresh", "ImageRefreshMode=ValidationOnly", "ReplaceAllMetadata=false", "ReplaceAllImages=false"} {
			if !strings.Contains(query, want) {
				t.Errorf("%s: the refresh asked %q, without %s", c.name, query, want)
			}
		}
		if strings.Contains(query, "Recursive") || body != `{"ReplaceThumbnailImages":true}` {
			t.Errorf("%s: the refresh asked %q with %s, want the thumbnails to be replaced and nothing below the item", c.name, query, body)
		}
	}
}
