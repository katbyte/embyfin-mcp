package emby

// Hand-written tests of the generated client against a canned server: the
// requests the method shapes build and the responses they decode. The
// generator leaves _test.go files alone. The integration suite proves the
// same shapes against a real Emby.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestNewNeedsAToken(t *testing.T) {
	t.Parallel()

	if _, err := New("http://nas:8096", ""); err == nil || !strings.Contains(err.Error(), "API key is required") {
		t.Errorf("New without a token = %v", err)
	}
	if _, err := New("nas", "tok"); err == nil {
		t.Error("New with a bad URL succeeded")
	}
}

func TestGetItems(t *testing.T) {
	t.Parallel()

	c, s := serve(t, func(*http.Request) (int, string) {
		return http.StatusOK, `{"Items":[{"Id":"1","Name":"Alien","IsFolder":false,"Genres":[],"TagItems":[{"Name":"sci-fi","Id":7}]}],"TotalRecordCount":1}`
	})
	res, err := c.GetItems(t.Context(), GetItemsOperationOptions{
		Recursive: new(false), IncludeItemTypes: "Movie,Series", SearchTerm: "ali", Limit: 5, MinCommunityRating: 7.5,
	})
	if err != nil {
		t.Fatal(err)
	}

	r := s.requests[0]
	if r.Method != http.MethodGet || r.URL.Path != "/Items" || r.Header.Get("X-Emby-Token") != "tok" {
		t.Errorf("request = %s %s (token %q)", r.Method, r.URL.Path, r.Header.Get("X-Emby-Token"))
	}
	q := r.URL.Query()
	for k, want := range map[string]string{"Recursive": "false", "IncludeItemTypes": "Movie,Series", "SearchTerm": "ali", "Limit": "5", "MinCommunityRating": "7.5"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if len(q) != 5 {
		t.Errorf("unset options were sent: %s", r.URL.RawQuery)
	}

	if res.HttpResponse.StatusCode != http.StatusOK || res.Model.TotalRecordCount != 1 {
		t.Fatalf("result = %+v", res)
	}
	it := res.Model.Items[0]
	if it.Name != "Alien" || it.IsFolder == nil || *it.IsFolder || it.Genres == nil || len(it.TagItems) != 1 || it.TagItems[0].Id != 7 {
		t.Errorf("item = %+v", it)
	}
}

func TestGetItemsComplete(t *testing.T) {
	t.Parallel()

	c, s := serve(t, func(r *http.Request) (int, string) {
		// Emby reports a total of 0 on some lists whatever they hold, so the
		// pager stops on a short page
		switch r.URL.Query().Get("StartIndex") {
		case "":
			return http.StatusOK, `{"Items":[{"Id":"1"},{"Id":"2"}],"TotalRecordCount":0}`
		case "2":
			return http.StatusOK, `{"Items":[{"Id":"3"}],"TotalRecordCount":0}`
		}
		return http.StatusInternalServerError, "asked for a page past the end"
	})
	res, err := c.GetItemsComplete(t.Context(), GetItemsOperationOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 3 || res.Items[2].Id != "3" || len(s.requests) != 2 || res.LatestHttpResponse == nil {
		t.Errorf("Complete = %d items over %d requests", len(res.Items), len(s.requests))
	}

	c, s = serve(t, func(r *http.Request) (int, string) {
		if r.URL.Query().Get("Limit") != "500" {
			t.Errorf("Limit = %q, want the default page", r.URL.Query().Get("Limit"))
		}
		return http.StatusOK, `{"Items":[{"Id":"1"}],"TotalRecordCount":1}`
	})
	if res, err := c.GetItemsComplete(t.Context(), GetItemsOperationOptions{}); err != nil || len(res.Items) != 1 || len(s.requests) != 1 {
		t.Errorf("Complete with a total = %+v, %v over %d requests", res, err, len(s.requests))
	}
}

func TestUpdateItemBody(t *testing.T) {
	t.Parallel()

	c, s := serve(t, func(*http.Request) (int, string) { return http.StatusNoContent, "" })
	// a nil list and an unset flag are left out; an empty list and an
	// explicit false are sent, which is how an edit clears or turns off
	if _, err := c.PostItemsByItemId(t.Context(), "42", BaseItemDto{Name: "Heat", TagItems: []NameLongIdPair{}, LockData: new(false)}); err != nil {
		t.Fatalf("a 204 for an empty response (emby-no-content-status) = %v", err)
	}

	r := s.requests[0]
	if r.Method != http.MethodPost || r.URL.Path != "/Items/42" || r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request = %s %s %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s.bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["TagItems"]) != "[]" || string(body["LockData"]) != "false" || string(body["Name"]) != `"Heat"` || len(body) != 3 {
		t.Errorf("body = %s", s.bodies[0])
	}
}

func TestWorkaroundOptions(t *testing.T) {
	t.Parallel()

	c, s := serve(t, func(*http.Request) (int, string) { return http.StatusNoContent, "" })
	ctx := t.Context()
	calls := []struct {
		name string
		call func() error
		want string
	}{
		{"delete library by id", func() error {
			_, err := c.DeleteLibraryVirtualFolders(ctx, DeleteLibraryVirtualFoldersOperationOptions{Id: "9", RefreshLibrary: new(false)})
			return err
		}, "DELETE /Library/VirtualFolders?Id=9&RefreshLibrary=false"},
		{"playlist owner", func() error {
			_, err := c.PostPlaylists(ctx, PostPlaylistsOperationOptions{Name: "P", Ids: "1,2", UserId: "u"})
			return err
		}, "POST /Playlists?Ids=1%2C2&Name=P&UserId=u"},
		{"comma-separated ids", func() error {
			_, err := c.PostSessionsByIdPlaying(ctx, "s", PlayRequest{}, PostSessionsByIdPlayingOperationOptions{ItemIds: []int{1, 2}, PlayCommand: "PlayNow"})
			return err
		}, "POST /Sessions/s/Playing?ItemIds=1%2C2&PlayCommand=PlayNow"},
		{"escaped path", func() error {
			_, err := c.DeleteItemsById(ctx, "a/b")
			return err
		}, "DELETE /Items/a%2Fb"},
	}
	for i, tt := range calls {
		// a 200 JSON answer where the document says so; the canned 204 is
		// only right for the empty ones
		if err := tt.call(); err != nil && client.StatusCode(err) != http.StatusNoContent {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		r := s.requests[i]
		got := r.Method + " " + r.URL.EscapedPath()
		if r.URL.RawQuery != "" {
			got += "?" + r.URL.RawQuery
		}
		if got != tt.want {
			t.Errorf("%s = %s, want %s", tt.name, got, tt.want)
		}
	}
}

func TestStreamAndStatus(t *testing.T) {
	t.Parallel()

	c, _ := serve(t, func(r *http.Request) (int, string) {
		if r.URL.Path == "/System/Ping" {
			return http.StatusOK, "Emby Server"
		}
		return http.StatusNotFound, "no such item"
	})

	ping, err := c.GetSystemPing(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ping.HttpResponse.Body.Close() }()
	if b, _ := io.ReadAll(ping.HttpResponse.Body); string(b) != "Emby Server" {
		t.Errorf("ping = %q", b)
	}

	res, err := c.GetUsersByUserIdItemsById(t.Context(), "u", "404")
	if !client.IsNotFound(err) || !client.WasNotFound(res.HttpResponse) || res.Model != nil {
		t.Errorf("a 404 = %v, response %+v", err, res)
	}
}
