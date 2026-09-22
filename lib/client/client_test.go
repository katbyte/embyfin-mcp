package client

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/katbyte/go-kt/version"
)

const testToken = "tok-123"

// options is a hand-written options object, the shape generated ones take.
type options struct {
	query  map[string][]string
	header map[string]string
}

func (o options) ToHeaders() *Headers {
	out := Headers{}
	for k, v := range o.header {
		out.Append(k, v)
	}
	return &out
}

func (o options) ToQuery() *QueryParams {
	out := QueryParams{}
	for k, vs := range o.query {
		for _, v := range vs {
			out.Append(k, v)
		}
	}
	return &out
}

// serve starts a canned server and a client for it.
func serve(t *testing.T, auth Authorizer, h http.HandlerFunc) *Client {
	t.Helper()

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, auth)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// the server's own client, so closing another test's server cannot
	// close this one's connections
	c.HTTPClient = srv.Client()

	return c
}

// execute builds and sends one request the way a generated method does.
func execute(t *testing.T, c *Client, opts RequestOptions, body any) (*Response, error) {
	t.Helper()

	req, err := c.NewRequest(t.Context(), opts)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		if err := req.Marshal(body); err != nil {
			t.Fatalf("Marshal: %v", err)
		}
	}

	return req.Execute(t.Context())
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, url, wantErr, wantBase string
		auth                         Authorizer
	}{
		{name: "ok", url: "http://nas:8096", auth: EmbyToken("t"), wantBase: "http://nas:8096"},
		{name: "trailing slash trimmed", url: "http://nas:8096/emby/", auth: EmbyToken("t"), wantBase: "http://nas:8096/emby"},
		{name: "missing url", url: "", auth: EmbyToken("t"), wantErr: "server URL is required"},
		{name: "no scheme", url: "nas:8096", auth: EmbyToken("t"), wantErr: "must include a scheme and host"},
		{name: "no host", url: "http://", auth: EmbyToken("t"), wantErr: "must include a scheme and host"},
		{name: "credentials in url", url: "http://kt:pw@nas:8096", auth: EmbyToken("t"), wantErr: "must not contain credentials"}, //nolint:gosec // the point of the case
		{name: "no authorizer", url: "http://nas:8096", wantErr: "authorizer is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := New(tt.url, tt.auth)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("New(%q) err = %v, want containing %q", tt.url, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.BaseURL != tt.wantBase {
				t.Errorf("BaseURL = %q, want %q", c.BaseURL, tt.wantBase)
			}
		})
	}
}

func TestAuthorizers(t *testing.T) {
	t.Parallel()

	identity := []string{`Client="embyfin-mcp"`, `Device="embyfin-mcp"`, `DeviceId="embyfin-mcp"`, `Version="` + version.Version + `"`, `Token="` + testToken + `"`}
	tests := []struct {
		name   string
		auth   Authorizer
		header string
		scheme string
		token  string // X-Emby-Token
	}{
		{name: "emby", auth: EmbyToken(testToken), header: "X-Emby-Authorization", scheme: "Emby ", token: testToken},
		// Jellyfin 12 answers 401 to a key sent only as X-Emby-Token
		{name: "jellyfin", auth: JellyfinToken(testToken), header: "Authorization", scheme: "MediaBrowser "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got http.Header
			c := serve(t, tt.auth, func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			})
			if _, err := execute(t, c, RequestOptions{HTTPMethod: http.MethodGet, Path: "/System/Info", ExpectedStatusCodes: []int{http.StatusOK}}, nil); err != nil {
				t.Fatal(err)
			}

			if v := got.Get("X-Emby-Token"); v != tt.token {
				t.Errorf("X-Emby-Token = %q, want %q", v, tt.token)
			}
			auth := got.Get(tt.header)
			if !strings.HasPrefix(auth, tt.scheme) {
				t.Errorf("%s = %q, want the %q scheme", tt.header, auth, tt.scheme)
			}
			for _, want := range identity {
				if !strings.Contains(auth, want) {
					t.Errorf("%s = %q, want containing %s", tt.header, auth, want)
				}
			}
			if got.Get("Accept") != "application/json" || got.Get("User-Agent") != UserAgent {
				t.Errorf("Accept %q, User-Agent %q", got.Get("Accept"), got.Get("User-Agent"))
			}
		})
	}
}

func TestOptionsObject(t *testing.T) {
	t.Parallel()

	var got *http.Request
	c := serve(t, EmbyToken(testToken), func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	opts := options{
		query:  map[string][]string{"fields": {"Path", "Genres"}, "Limit": {"5"}},
		header: map[string]string{"X-Emby-Authorization": "override"},
	}
	if _, err := execute(t, c, RequestOptions{HTTPMethod: http.MethodGet, Path: "/Items", ExpectedStatusCodes: []int{http.StatusOK}, OptionsObject: opts}, nil); err != nil {
		t.Fatal(err)
	}

	if got.URL.Path != "/Items" || got.URL.Query().Get("Limit") != "5" {
		t.Errorf("request = %s", got.URL)
	}
	if fields := got.URL.Query()["fields"]; len(fields) != 2 || fields[0] != "Path" || fields[1] != "Genres" {
		t.Errorf("fields = %v, want one key per value", fields)
	}
	// a header option wins over the authorizer's
	if v := got.Header.Get("X-Emby-Authorization"); v != "override" {
		t.Errorf("X-Emby-Authorization = %q, want the option's", v)
	}
}

func TestStatusError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   int
		body     string
		wantMsg  []string
		notFound bool
	}{
		{name: "401 hint", status: http.StatusUnauthorized, body: "bad token", wantMsg: []string{"GET /System/Info: HTTP 401 (expected 200): bad token", "API key rejected"}},
		{name: "403 hint", status: http.StatusForbidden, wantMsg: []string{"HTTP 403", "lacks permission"}},
		{name: "404 is not found", status: http.StatusNotFound, body: "nope", wantMsg: []string{"HTTP 404 (expected 200): nope"}, notFound: true},
		// a 2xx the operation does not document is an error too
		{name: "undocumented 204", status: http.StatusNoContent, wantMsg: []string{"HTTP 204 (expected 200)"}},
		{name: "long body truncated", status: http.StatusBadRequest, body: strings.Repeat("x", 1000), wantMsg: []string{"HTTP 400", strings.Repeat("x", errBodyPreview) + "..."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := serve(t, EmbyToken(testToken), func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			resp, err := execute(t, c, RequestOptions{HTTPMethod: http.MethodGet, Path: "/System/Info", ExpectedStatusCodes: []int{http.StatusOK}}, nil)

			se, ok := errors.AsType[*StatusError](err)
			if !ok {
				t.Fatalf("err = %v, want *StatusError", err)
			}
			if se.StatusCode != tt.status || StatusCode(err) != tt.status || se.Method != http.MethodGet || se.Path != "/System/Info" {
				t.Errorf("StatusError = %+v", se)
			}
			for _, want := range tt.wantMsg {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Error() = %q, want containing %q", err.Error(), want)
				}
			}
			if IsNotFound(err) != tt.notFound || WasNotFound(resp.Response) != tt.notFound {
				t.Errorf("IsNotFound = %v, WasNotFound = %v, want %v", IsNotFound(err), WasNotFound(resp.Response), tt.notFound)
			}
			// the response comes back with the error, its whole body readable
			if b, _ := io.ReadAll(resp.Body); string(b) != tt.body {
				t.Errorf("body after the error = %d bytes, want %d", len(b), len(tt.body))
			}
		})
	}
	if IsNotFound(errors.New("other")) || StatusCode(nil) != 0 || WasNotFound(nil) {
		t.Error("a non-status error reads as a status")
	}
}

func TestBodies(t *testing.T) {
	t.Parallel()

	t.Run("json both ways, body readable again", func(t *testing.T) {
		t.Parallel()

		c := serve(t, EmbyToken(testToken), func(w http.ResponseWriter, r *http.Request) {
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
			b, _ := io.ReadAll(r.Body)
			if string(b) != `{"Username":"kt"}` || r.ContentLength != int64(len(b)) {
				t.Errorf("body = %s (ContentLength %d)", b, r.ContentLength)
			}
			_, _ = io.WriteString(w, `{"AccessToken":"abc"}`)
		})
		resp, err := execute(t, c, RequestOptions{ContentType: "application/json", HTTPMethod: http.MethodPost, Path: "/Users/AuthenticateByName", ExpectedStatusCodes: []int{http.StatusOK}},
			struct{ Username string }{"kt"})
		if err != nil {
			t.Fatal(err)
		}
		var model struct{ AccessToken string }
		if err := resp.Unmarshal(&model); err != nil || model.AccessToken != "abc" {
			t.Fatalf("Unmarshal = %+v, %v", model, err)
		}
		if b, _ := io.ReadAll(resp.Body); string(b) != `{"AccessToken":"abc"}` {
			t.Errorf("body after Unmarshal = %q", b)
		}
	})

	t.Run("empty answer leaves the model alone", func(t *testing.T) {
		t.Parallel()

		c := serve(t, EmbyToken(testToken), func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		resp, err := execute(t, c, RequestOptions{HTTPMethod: http.MethodPost, Path: "/x", ExpectedStatusCodes: []int{http.StatusOK, http.StatusNoContent}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		model := map[string]string{"kept": "yes"}
		if err := resp.Unmarshal(&model); err != nil || model["kept"] != "yes" {
			t.Errorf("Unmarshal of nothing = %v, %v", model, err)
		}
	})

	t.Run("bad json is an error", func(t *testing.T) {
		t.Parallel()

		c := serve(t, EmbyToken(testToken), func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>") })
		resp, err := execute(t, c, RequestOptions{HTTPMethod: http.MethodGet, Path: "/System/Info", ExpectedStatusCodes: []int{http.StatusOK}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var model struct{}
		if err := resp.Unmarshal(&model); err == nil || !strings.Contains(err.Error(), "GET /System/Info: decoding response") {
			t.Errorf("Unmarshal = %v, want a decoding error", err)
		}
	})

	t.Run("raw body needs a concrete type for a range", func(t *testing.T) {
		t.Parallel()

		var gotType string
		c := serve(t, JellyfinToken(testToken), func(w http.ResponseWriter, r *http.Request) {
			gotType = r.Header.Get("Content-Type")
			w.WriteHeader(http.StatusNoContent)
		})
		req, err := c.NewRequest(t.Context(), RequestOptions{ContentType: "image/*", HTTPMethod: http.MethodPost, Path: "/Items/1/Images/Primary", ExpectedStatusCodes: []int{http.StatusNoContent}})
		if err != nil {
			t.Fatal(err)
		}
		if err := req.SetBody(strings.NewReader("png"), ""); err == nil || !strings.Contains(err.Error(), "image/*") {
			t.Errorf("SetBody with no type for image/* = %v, want an error", err)
		}
		if err := req.SetBody(bytes.NewReader([]byte("png")), "image/png"); err != nil {
			t.Fatal(err)
		}
		if _, err := req.Execute(t.Context()); err != nil {
			t.Fatal(err)
		}
		if gotType != "image/png" {
			t.Errorf("Content-Type = %q", gotType)
		}
	})

	t.Run("stream left unread", func(t *testing.T) {
		t.Parallel()

		c := serve(t, EmbyToken(testToken), func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "log line") })
		resp, err := execute(t, c, RequestOptions{HTTPMethod: http.MethodGet, Path: "/System/Logs/Log", ExpectedStatusCodes: []int{http.StatusOK}, StreamResponse: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if b, _ := io.ReadAll(resp.Body); string(b) != "log line" {
			t.Errorf("stream = %q", b)
		}
	})
}

func TestListHelpers(t *testing.T) {
	t.Parallel()

	type kind string
	if got := CSV([]kind{"Movie", "Series"}); got != "Movie,Series" {
		t.Errorf("CSV(kinds) = %q", got)
	}
	if got := CSV([]int{1979, 1986}); got != "1979,1986" {
		t.Errorf("CSV(ints) = %q", got)
	}
	if got := JSONObject(map[string]string{"a": "b"}); got != `{"a":"b"}` {
		t.Errorf("JSONObject = %q", got)
	}
}

// TMDB takes its read access token, a JWT, as a bearer; the older API key
// goes in the query, beside whatever the call already asks for.
func TestTMDBToken(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ token, bearer, key string }{
		{"eyJhbGciOiJIUzI1NiJ9.e30.sig", "Bearer eyJhbGciOiJIUzI1NiJ9.e30.sig", ""},
		{"0123456789abcdef0123456789abcdef", "", "0123456789abcdef0123456789abcdef"},
	} {
		var bearer, key, query string
		c := serve(t, TMDBToken(tc.token), func(w http.ResponseWriter, r *http.Request) {
			bearer, key, query = r.Header.Get("Authorization"), r.URL.Query().Get("api_key"), r.URL.Query().Get("query")
			w.WriteHeader(http.StatusOK)
		})
		opts := RequestOptions{
			HTTPMethod: http.MethodGet, Path: "/3/search/movie", ExpectedStatusCodes: []int{http.StatusOK},
			OptionsObject: options{query: map[string][]string{"query": {"alien"}}},
		}
		if _, err := execute(t, c, opts, nil); err != nil {
			t.Fatal(err)
		}
		if bearer != tc.bearer || key != tc.key || query != "alien" {
			t.Errorf("%s: bearer %q, api_key %q, query %q", tc.token[:6], bearer, key, query)
		}
	}
}
