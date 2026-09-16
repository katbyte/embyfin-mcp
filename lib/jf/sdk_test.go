package jf

// Hand-written tests of the generated client against a canned server: the
// requests the method shapes build and the responses they decode. The
// generator leaves _test.go files alone. The integration suite proves the
// same shapes against a real Jellyfin.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/client"
)

type canned struct {
	*httptest.Server
	requests []*http.Request
	bodies   []string
}

func serve(t *testing.T, handle func(r *http.Request) (int, string)) (*Client, *canned) {
	t.Helper()

	s := &canned{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.requests = append(s.requests, r.Clone(r.Context()))
		s.bodies = append(s.bodies, string(b))
		status, body := handle(r)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	c, err := New(s.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	// the server's own client, so closing another test's server cannot
	// close this one's connections
	c.Client.HTTPClient = s.Client()

	return c, s
}

func TestGetItems(t *testing.T) {
	t.Parallel()

	c, s := serve(t, func(*http.Request) (int, string) {
		return http.StatusOK, `{"Items":[{"Id":"a1","Name":"Alien","Type":"Movie","UserData":{"IsFavorite":true}}],"TotalRecordCount":1}`
	})
	res, err := c.GetItems(t.Context(), GetItemsOperationOptions{
		Fields:              []ItemFields{ItemFieldsPath, ItemFieldsGenres},
		IncludeItemTypes:    []BaseItemKind{BaseItemKindMovie},
		Years:               []int{1979, 1986},
		CollapseBoxSetItems: new(false),
		UserId:              "u",
	})
	if err != nil {
		t.Fatal(err)
	}

	r := s.requests[0]
	if !strings.HasPrefix(r.Header.Get("Authorization"), `MediaBrowser Client="embyfin-mcp"`) || r.Header.Get("X-Emby-Token") != "" {
		t.Errorf("auth headers = %v", r.Header)
	}
	// Jellyfin's lists go one key per value, the document's default
	q := r.URL.Query()
	if !slices.Equal(q["fields"], []string{"Path", "Genres"}) || !slices.Equal(q["years"], []string{"1979", "1986"}) ||
		q.Get("includeItemTypes") != "Movie" || q.Get("collapseBoxSetItems") != "false" || q.Get("userId") != "u" {
		t.Errorf("query = %s", r.URL.RawQuery)
	}

	it := res.Model.Items[0]
	if it.Id != "a1" || it.Type != BaseItemKindMovie || it.UserData == nil || it.UserData.IsFavorite == nil || !*it.UserData.IsFavorite {
		t.Errorf("item = %+v", it)
	}
}

func TestCreatePlaylistBody(t *testing.T) {
	t.Parallel()

	c, s := serve(t, func(*http.Request) (int, string) { return http.StatusOK, `{"Id":"p1"}` })
	res, err := c.CreatePlaylist(t.Context(), CreatePlaylistDto{Name: "P", Ids: []string{"a", "b"}, MediaType: MediaTypeVideo, UserId: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Model.Id != "p1" {
		t.Errorf("Id = %q", res.Model.Id)
	}

	// the deprecated query form is gone (jellyfin-create-playlist-query)
	r := s.requests[0]
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request = %s ? %s (%s)", r.URL.Path, r.URL.RawQuery, r.Header.Get("Content-Type"))
	}
	var body struct {
		Name, MediaType string
		IDs             []string `json:"Ids"`
	}
	if err := json.Unmarshal([]byte(s.bodies[0]), &body); err != nil || body.Name != "P" || body.MediaType != "Video" || len(body.IDs) != 2 {
		t.Errorf("body = %s", s.bodies[0])
	}
	if strings.Contains(s.bodies[0], "IsPublic") {
		t.Errorf("an unset flag was sent: %s", s.bodies[0])
	}
}

func TestExpectedStatusCodes(t *testing.T) {
	t.Parallel()

	status := http.StatusNoContent
	c, _ := serve(t, func(*http.Request) (int, string) { return status, "" })
	if _, err := c.RefreshItem(t.Context(), "a1", RefreshItemOperationOptions{}); err != nil {
		t.Fatalf("the documented 204 = %v", err)
	}
	// a 200 where the document says 204 is a drift to fix, so an error
	status = http.StatusOK
	res, err := c.RefreshItem(t.Context(), "a1", RefreshItemOperationOptions{})
	if client.StatusCode(err) != http.StatusOK || res.HttpResponse == nil {
		t.Errorf("an undocumented 200 = %v", err)
	}
}

func TestRawBodyAndStream(t *testing.T) {
	t.Parallel()

	c, s := serve(t, func(r *http.Request) (int, string) {
		if r.Method == http.MethodGet {
			return http.StatusOK, "log text"
		}
		return http.StatusNoContent, ""
	})

	if _, err := c.SetItemImage(t.Context(), "a1", ImageTypePrimary, strings.NewReader("png"), ""); err == nil || !strings.Contains(err.Error(), "image/*") {
		t.Errorf("an image with no content type = %v", err)
	}
	if _, err := c.SetItemImage(t.Context(), "a1", ImageTypePrimary, strings.NewReader("png"), "image/png"); err != nil {
		t.Fatal(err)
	}
	r := s.requests[0]
	if r.URL.Path != "/Items/a1/Images/Primary" || r.Header.Get("Content-Type") != "image/png" || s.bodies[0] != "png" {
		t.Errorf("upload = %s %q %q", r.URL.Path, r.Header.Get("Content-Type"), s.bodies[0])
	}

	log, err := c.GetLogFile(t.Context(), GetLogFileOperationOptions{Name: "log.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.HttpResponse.Body.Close() }()
	if b, _ := io.ReadAll(log.HttpResponse.Body); string(b) != "log text" || s.requests[1].URL.Query().Get("name") != "log.txt" {
		t.Errorf("log = %q", b)
	}
}
