// Package embyfin is the backend-neutral client the tools use: one Client
// that speaks to either Emby or Jellyfin and hides the differences between
// them, so everything above it stays backend-agnostic.
//
// The layer is thin. It holds one of the two typed clients generated from
// the servers' OpenAPI documents, lib/emby or lib/jf (chosen by Backend in
// New), and every method branches per backend: it calls the typed operation
// with typed parameters and bodies, then converts the typed DTOs into the
// neutral types (Item, VirtualFolder, User, Session, ...) with one explicit
// field-mapping function per DTO per backend (emby.go, jf.go). Every
// difference between the servers lives here, next to a comment saying which
// server it is for; api-defs/README.md lists them under "Conventions worth
// knowing".
//
// The typed clients are generated (make generate) and must not be edited by
// hand. Where a document is wrong about its server's shape (a parameter it
// leaves out, a field it lacks) the fix is a workaround in
// internal/pandorest's importer, so the generated clients carry it; what
// stays here is behaviour no document can express, next to a comment saying
// which server it is for and the live test that found it.
package embyfin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
)

type Backend string

const (
	Emby     Backend = "emby"
	Jellyfin Backend = "jellyfin"
)

// Client talks to one media server through the typed client for its backend;
// exactly one of emby and jf is set.
type Client struct {
	backend Backend
	baseURL string
	emby    *emby.Client
	jf      *jf.Client
	// settle is how long to wait between checks that a change the server
	// applies in the background has landed
	settle time.Duration
	// saveGrain is how finely Emby tells one save of an item from the next:
	// to the second (see unsavedFor)
	saveGrain time.Duration
	// items serialises this process's changes to one item (an item's
	// metadata, a playlist's entries): see keyedLocks
	items keyedLocks
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

	c := &Client{backend: backend, baseURL: strings.TrimRight(baseURL, "/"), settle: 250 * time.Millisecond, saveGrain: time.Second}
	var err error
	if backend == Emby {
		c.emby, err = emby.New(baseURL, token)
	} else {
		c.jf, err = jf.New(baseURL, token)
	}
	if err != nil {
		return nil, err
	}

	return c, nil
}

func (c *Client) Backend() Backend { return c.backend }

// APIVersion is the version of the server's API document the SDK in use was
// generated from, which is not always the server's own version.
func (c *Client) APIVersion() string {
	if c.isEmby() {
		return emby.APIVersion
	}

	return jf.APIVersion
}

// BaseURL is the server address the client was created with, without a
// trailing slash.
func (c *Client) BaseURL() string { return c.baseURL }

// isEmby reports whether the client talks to Emby; the other branch of every
// method is Jellyfin.
func (c *Client) isEmby() bool { return c.backend == Emby }

// list splits a comma-separated option into the typed slice a Jellyfin
// parameter takes (the typed client joins it back with commas on the wire).
func list[T ~string](s string) []T {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]T, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, T(p))
		}
	}

	return out
}

// intList is list for numeric options such as years.
func intList(s string) ([]int, error) {
	parts := list[string](s)
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number: %w", p, err)
		}
		out = append(out, n)
	}

	return out, nil
}

// embyID parses an Emby item id for the few Emby parameters the document
// types as integers (Emby ids are numeric strings everywhere else).
func embyID(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("item id %q is not an Emby id: %w", id, err)
	}

	return n, nil
}

// pause waits one settle interval, or until the context ends.
func (c *Client) pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(c.settle):
		return nil
	}
}

// nz is n as the pointer a generated option takes, nil for 0: the options
// here keep 0 for "not asked", and only a season or an image index means 0.
func nz[N int | int64](n N) *N {
	if n == 0 {
		return nil
	}

	return &n
}
