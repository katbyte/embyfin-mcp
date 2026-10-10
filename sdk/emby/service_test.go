package emby

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/pandorest/client"
)

// Emby is sent the key twice over, as it reads it: alone in X-Emby-Token, and
// in X-Emby-Authorization beside who is asking and the version running.
func TestTheKeyIsSentAsEmbyReadsIt(t *testing.T) {
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
	if _, err := c.GetSystemInfoPublic(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := `Emby Client="embyfin-mcp", Device="embyfin-mcp", DeviceId="embyfin-mcp", Version="` + version.Version + `", Token="tok"`
	if got.Get("X-Emby-Token") != "tok" || got.Get("X-Emby-Authorization") != want || got.Get("User-Agent") != "embyfin-mcp" || got.Get("Authorization") != "" {
		t.Errorf("headers = %v\nwant X-Emby-Authorization %q", got, want)
	}
}

// A refusal says what it most often means on a server of one's own, and an
// answer that is neither says nothing more than the server did.
func TestARefusalSaysWhatToCheck(t *testing.T) {
	t.Parallel()

	for status, want := range map[int]string{
		http.StatusUnauthorized: "GET /System/Info/Public: HTTP 401 (expected 200 or 204): no (API key rejected; check the token)",
		http.StatusForbidden:    "GET /System/Info/Public: HTTP 403 (expected 200 or 204): no (the API key's user lacks permission for this; most write operations need an administrator)",
		http.StatusTeapot:       "GET /System/Info/Public: HTTP 418 (expected 200 or 204): no",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", status) }))
		t.Cleanup(srv.Close)
		c, err := New(srv.URL, "tok", client.WithTransport(srv.Client().Transport))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.GetSystemInfoPublic(t.Context()); err == nil || err.Error() != want || client.StatusCode(err) != status {
			t.Errorf("a %d = %v\nwant    %s", status, err, want)
		}
	}
}

// A redirect is followed, and the key stays with the server it was given
// for: sent on to another host, it would be handed to whoever answered there.
func TestTheKeyDoesNotFollowARedirectToAnotherHost(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var elsewhere http.Header
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		elsewhere = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ServerName":"Zzyzx"}`))
	}))
	t.Cleanup(other.Close)
	// the same machine under another name is another host
	away := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, away+"/System/Info/Public", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	c, err := New(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.GetSystemInfoPublic(t.Context())
	if err != nil || res.Model == nil || res.Model.ServerName != "Zzyzx" {
		t.Fatalf("a read through a redirect = %+v, %v", res.Model, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if elsewhere.Get("X-Emby-Token") != "" || elsewhere.Get("X-Emby-Authorization") != "" {
		t.Errorf("the key went to another host: %v", elsewhere)
	}
}
