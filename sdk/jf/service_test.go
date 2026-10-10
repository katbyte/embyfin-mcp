package jf

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/pandorest/client"
)

// Jellyfin is sent the key in one MediaBrowser Authorization header, beside
// who is asking and the version running, and in no header of Emby's: Jellyfin
// 12 answers 401 to a key sent only as X-Emby-Token.
func TestTheKeyIsSentAsJellyfinReadsIt(t *testing.T) {
	t.Parallel()

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, err := New(srv.URL, "tok", client.WithTransport(srv.Client().Transport))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetPublicSystemInfo(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := `MediaBrowser Client="embyfin-mcp", Device="embyfin-mcp", DeviceId="embyfin-mcp", Version="` + version.Version + `", Token="tok"`
	if got.Get("Authorization") != want || got.Get("X-Emby-Token") != "" || got.Get("User-Agent") != "embyfin-mcp" {
		t.Errorf("headers = %v\nwant Authorization %q", got, want)
	}
}

// A refusal says what it most often means on a server of one's own.
func TestARefusalSaysWhatToCheck(t *testing.T) {
	t.Parallel()

	for status, want := range map[int]string{
		http.StatusUnauthorized: "GET /System/Info/Public: HTTP 401 (expected 200): no (API key rejected; check the token)",
		http.StatusForbidden:    "GET /System/Info/Public: HTTP 403 (expected 200): no (the API key's user lacks permission for this; most write operations need an administrator)",
		http.StatusTeapot:       "GET /System/Info/Public: HTTP 418 (expected 200): no",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", status) }))
		t.Cleanup(srv.Close)
		c, err := New(srv.URL, "tok", client.WithTransport(srv.Client().Transport))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.GetPublicSystemInfo(t.Context()); err == nil || err.Error() != want || client.StatusCode(err) != status {
			t.Errorf("a %d = %v\nwant    %s", status, err, want)
		}
	}
}
