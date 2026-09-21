// Package client is the hand-written base client the generated SDKs, lib/emby
// and lib/jf, send every request through, after go-azure-sdk's sdk/client.
// The two servers speak the same HTTP API family and differ here only in how
// the token is sent (EmbyToken, JellyfinToken).
//
// A generated method builds RequestOptions (method, path, the status codes the
// operation is documented to answer, its options object), makes a Request,
// marshals the body into it, executes it and unmarshals the Response into its
// model:
//
//	opts := client.RequestOptions{
//		ContentType:         "application/json",
//		ExpectedStatusCodes: []int{http.StatusOK},
//		HttpMethod:          http.MethodGet,
//		OptionsObject:       options,
//		Path:                "/Items",
//	}
//	req, err := c.Client.NewRequest(ctx, opts)
//	resp, err := req.Execute(ctx)
//	err = resp.Unmarshal(&model)
//
// A status the operation does not document is an error (*StatusError), even
// another 2xx: that is how a document that has drifted from its server shows
// up, and the fix belongs in the importer's workarounds. The response is
// returned alongside the error, its body still readable.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/go-kt/version"
)

const (
	// MaxResponseBytes caps a buffered (non-stream) response body.
	MaxResponseBytes = 64 << 20
	// DefaultPageSize is the page a Complete method asks for when the
	// options leave Limit unset.
	DefaultPageSize = 500
	// UserAgent is sent with every request, and names the client to the
	// server in the MediaBrowser authorization header.
	UserAgent = "embyfin-mcp"

	errBodyPreview = 300
)

// Authorizer adds credentials to a request.
type Authorizer interface {
	Authorize(req *http.Request)
}

// EmbyToken authenticates to Emby with an API key or a user access token.
// Emby reads the token from X-Emby-Token and identifies the client from
// X-Emby-Authorization, which carries the token as well.
type EmbyToken string

func (t EmbyToken) Authorize(req *http.Request) {
	req.Header.Set("X-Emby-Token", string(t))
	req.Header.Set("X-Emby-Authorization", mediaBrowserAuth("Emby", string(t)))
}

// JellyfinToken authenticates to Jellyfin with an API key or a user access
// token. Jellyfin takes the token and the client identity from one
// MediaBrowser Authorization header (Jellyfin 12 answers 401 to a key sent
// only as X-Emby-Token).
type JellyfinToken string

func (t JellyfinToken) Authorize(req *http.Request) {
	req.Header.Set("Authorization", mediaBrowserAuth("MediaBrowser", string(t)))
}

// TMDBToken authenticates to TMDB with an API Read Access Token, which is a
// JWT and goes in the Authorization header as a bearer, or with the older API
// Key, which goes in the api_key query parameter.
type TMDBToken string

func (t TMDBToken) Authorize(req *http.Request) {
	if strings.HasPrefix(string(t), "eyJ") {
		req.Header.Set("Authorization", "Bearer "+string(t))

		return
	}
	q := req.URL.Query()
	q.Set("api_key", string(t))
	req.URL.RawQuery = q.Encode()
}

func mediaBrowserAuth(scheme, token string) string {
	return fmt.Sprintf(`%s Client=%q, Device=%q, DeviceId=%q, Version=%q, Token=%q`, scheme, UserAgent, UserAgent, UserAgent, version.Version, token)
}

// Client sends requests to one server.
type Client struct {
	// BaseURL is the server address without a trailing slash.
	BaseURL    string
	HTTPClient *http.Client
	Authorizer Authorizer
}

// New returns a client for the server at baseURL (scheme and host,
// optionally a path prefix such as /emby).
func New(baseURL string, authorizer Authorizer) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("server URL is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("server URL %q must include a scheme and host, e.g. http://nas:8096", baseURL)
	}
	if u.User != nil {
		return nil, errors.New("server URL must not contain credentials; pass the API key separately")
	}
	if authorizer == nil {
		return nil, errors.New("an authorizer is required")
	}

	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: 120 * time.Second},
		Authorizer: authorizer,
	}, nil
}

// Options is what a generated options struct implements: the query and
// header parameters it holds.
type Options interface {
	ToHeaders() *Headers
	ToQuery() *QueryParams
}

// Headers are request headers from an options object.
type Headers struct {
	values http.Header
}

// Append adds a header.
func (h *Headers) Append(key, value string) {
	if h.values == nil {
		h.values = http.Header{}
	}
	h.values.Add(key, value)
}

// Values returns the headers.
func (h *Headers) Values() http.Header { return h.values }

// QueryParams are query parameters from an options object.
type QueryParams struct {
	values url.Values
}

// Append adds a query parameter.
func (q *QueryParams) Append(key, value string) {
	if q.values == nil {
		q.values = url.Values{}
	}
	q.values.Add(key, value)
}

// Values returns the parameters.
func (q *QueryParams) Values() url.Values { return q.values }

// RequestOptions describes one request.
type RequestOptions struct {
	// ContentType is the media type of the request body, when there is one.
	ContentType string
	// ExpectedStatusCodes are the statuses the operation documents; any
	// other is a *StatusError.
	ExpectedStatusCodes []int
	HttpMethod          string //nolint:revive,staticcheck // go-azure-sdk's spelling, which the generated code mirrors
	// OptionsObject supplies query parameters and headers; nil for none.
	OptionsObject Options
	// Path is the escaped path below BaseURL.
	Path string
	// StreamResponse leaves a successful response's body unread for the
	// caller, for operations that answer a file.
	StreamResponse bool
}

// Request is a request being built.
type Request struct {
	*http.Request

	ExpectedStatusCodes []int
	StreamResponse      bool

	client      *Client
	contentType string
}

// NewRequest builds a request with authentication, the fixed client headers
// and the options object applied.
func (c *Client) NewRequest(ctx context.Context, input RequestOptions) (*Request, error) {
	u := c.BaseURL + input.Path
	var headers http.Header
	if input.OptionsObject != nil {
		if q := input.OptionsObject.ToQuery(); q != nil && len(q.Values()) > 0 {
			u += "?" + q.Values().Encode()
		}
		if h := input.OptionsObject.ToHeaders(); h != nil {
			headers = h.Values()
		}
	}

	req, err := http.NewRequestWithContext(ctx, input.HttpMethod, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	c.Authorizer.Authorize(req)
	maps.Copy(req.Header, headers)

	return &Request{
		Request:             req,
		ExpectedStatusCodes: input.ExpectedStatusCodes,
		StreamResponse:      input.StreamResponse,
		client:              c,
		contentType:         input.ContentType,
	}, nil
}

// Marshal sets the request body to payload as JSON.
func (r *Request) Marshal(payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling request body: %w", err)
	}
	contentType := r.contentType
	if contentType == "" {
		contentType = "application/json"
	}

	return r.SetBody(bytes.NewReader(b), contentType)
}

// SetBody sets the request body to raw bytes of the given media type, or the
// operation's own when contentType is empty. A nil body sends none. An
// operation that takes a range of types (image/*) needs the concrete one.
func (r *Request) SetBody(body io.Reader, contentType string) error {
	if body == nil {
		return nil
	}
	if contentType == "" {
		contentType = r.contentType
	}
	if strings.Contains(contentType, "*") {
		return fmt.Errorf("%s %s: name the body's content type (the operation takes %s)", r.Method, r.URL.Path, contentType)
	}
	r.Body = io.NopCloser(body)
	switch b := body.(type) {
	case *bytes.Reader:
		r.ContentLength = int64(b.Len())
	case *bytes.Buffer:
		r.ContentLength = int64(b.Len())
	case *strings.Reader:
		r.ContentLength = int64(b.Len())
	default:
		r.ContentLength = -1
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}

	return nil
}

// Execute sends the request. The response is returned whenever the server
// answered, including with a *StatusError, so the caller can read its status
// and body. Its body is buffered (and readable again) unless the request
// streams a successful response.
func (r *Request) Execute(_ context.Context) (*Response, error) {
	httpResp, err := r.client.HTTPClient.Do(r.Request) //nolint:bodyclose // buffered and closed below, or a stream handed to the caller to close
	if err != nil {
		return nil, err
	}
	resp := &Response{Response: httpResp}

	if !slices.Contains(r.ExpectedStatusCodes, httpResp.StatusCode) {
		body, _ := resp.buffer()
		return resp, &StatusError{
			Method:              r.Method,
			Path:                r.URL.Path,
			StatusCode:          httpResp.StatusCode,
			ExpectedStatusCodes: r.ExpectedStatusCodes,
			Body:                truncate(strings.TrimSpace(string(body)), errBodyPreview),
		}
	}
	if r.StreamResponse {
		return resp, nil
	}
	if _, err := resp.buffer(); err != nil {
		return resp, fmt.Errorf("%s %s: reading response: %w", r.Method, r.URL.Path, err)
	}

	return resp, nil
}

// Response is a server's answer.
type Response struct {
	*http.Response
}

// buffer reads the body into memory, closes the connection's reader and
// replaces it with one over the bytes, so the body can be read again.
func (r *Response) buffer() ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxResponseBytes+1))
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return body, err
	}
	if len(body) > MaxResponseBytes {
		return body, fmt.Errorf("response is larger than %d bytes", MaxResponseBytes)
	}

	return body, nil
}

// Unmarshal decodes the JSON body into model, leaving the body readable
// again for a caller that wants the bytes. An empty body leaves model as it
// is.
func (r *Response) Unmarshal(model any) error {
	body, err := r.buffer()
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, model); err != nil {
		return fmt.Errorf("%s %s: decoding response: %w", r.Request.Method, r.Request.URL.Path, err)
	}

	return nil
}

// StatusError is an answer with a status the operation does not document.
type StatusError struct {
	Method              string
	Path                string
	StatusCode          int
	ExpectedStatusCodes []int
	// Body is the start of the response body.
	Body string
}

func (e *StatusError) Error() string {
	want := make([]string, 0, len(e.ExpectedStatusCodes))
	for _, c := range e.ExpectedStatusCodes {
		want = append(want, strconv.Itoa(c))
	}
	msg := fmt.Sprintf("%s %s: HTTP %d (expected %s)", e.Method, e.Path, e.StatusCode, strings.Join(want, " or "))
	if e.Body != "" {
		msg += ": " + e.Body
	}
	switch e.StatusCode {
	case http.StatusUnauthorized:
		msg += " (API key rejected; check the token)"
	case http.StatusForbidden:
		msg += " (the API key's user lacks permission for this; most write operations need an administrator)"
	}

	return msg
}

// StatusCode returns the status of a *StatusError in err's chain, or 0.
func StatusCode(err error) int {
	if se, ok := errors.AsType[*StatusError](err); ok {
		return se.StatusCode
	}

	return 0
}

// IsNotFound reports whether err is a 404 from the server.
func IsNotFound(err error) bool { return StatusCode(err) == http.StatusNotFound }

// WasNotFound reports whether a response is a 404.
func WasNotFound(resp *http.Response) bool {
	return resp != nil && resp.StatusCode == http.StatusNotFound
}

// CSV joins a list parameter the way both servers read arrays in a query
// string: comma-separated.
func CSV[T ~string | ~int | ~int64](values []T) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = fmt.Sprint(v)
	}

	return strings.Join(parts, ",")
}

// JSONObject renders an object query parameter as the JSON string the
// servers bind it from.
func JSONObject[T any](value map[string]T) string {
	b, err := json.Marshal(value)
	if err != nil {
		return ""
	}

	return string(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "..."
}
