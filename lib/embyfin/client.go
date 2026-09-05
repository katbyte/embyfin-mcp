// Package embyfin is a minimal client for the MediaBrowser HTTP API family
// spoken by both Emby and Jellyfin. Backend differences are kept inside this
// package so everything above it stays backend-agnostic.
package embyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Backend string

const (
	Emby     Backend = "emby"
	Jellyfin Backend = "jellyfin"
)

type Client struct {
	backend Backend
	baseURL string
	token   string
	http    *http.Client
}

func New(backend Backend, baseURL, token string) (*Client, error) {
	switch backend {
	case Emby, Jellyfin:
	default:
		return nil, fmt.Errorf("unknown backend %q (want %q or %q)", backend, Emby, Jellyfin)
	}
	if baseURL == "" {
		return nil, errors.New("server URL is required (--server / EMBYFIN_SERVER)")
	}
	if token == "" {
		return nil, errors.New("API token is required (--token / EMBYFIN_TOKEN)")
	}

	return &Client{
		backend: backend,
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 60 * time.Second},
	}, nil
}

func (c *Client) Backend() Backend { return c.backend }

// do performs a request against the media server. body (when non-nil) is sent
// as JSON; out (when non-nil) receives the decoded JSON response.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	raw, err := c.doRaw(ctx, method, path, query, body)
	if err != nil {
		return err
	}

	if out == nil || len(raw) == 0 {
		return nil
	}

	return json.Unmarshal(raw, out)
}

func (c *Client) doRaw(ctx context.Context, method, path string, query url.Values, body any) ([]byte, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reqBody io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, err
	}

	// Emby's canonical header; Jellyfin accepts it too, but prefers the
	// MediaBrowser Authorization scheme, so send both.
	req.Header.Set("X-Emby-Token", c.token)
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Token=%q, Client="embyfin-mcp"`, c.token))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(string(raw), 300))
	}

	return raw, nil
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

func (c *Client) post(ctx context.Context, path string, query url.Values, body, out any) error {
	return c.do(ctx, http.MethodPost, path, query, body, out)
}

func (c *Client) del(ctx context.Context, path string, query url.Values) error {
	return c.do(ctx, http.MethodDelete, path, query, nil, nil)
}

// getText fetches a plain-text resource (e.g. a log file).
func (c *Client) getText(ctx context.Context, path string, query url.Values) (string, error) {
	raw, err := c.doRaw(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return "", err
	}

	return string(raw), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
