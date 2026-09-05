package embyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

type VirtualFolder struct {
	Name           string   `json:"Name"`
	CollectionType string   `json:"CollectionType,omitempty"` // movies, tvshows, music, ...; empty = mixed
	Locations      []string `json:"Locations,omitempty"`
	ItemID         string   `json:"ItemId,omitempty"`
}

// VirtualFolders lists the server's libraries. Emby wraps the list in an
// Items envelope while Jellyfin returns a bare array, so decode both.
func (c *Client) VirtualFolders(ctx context.Context) ([]VirtualFolder, error) {
	var raw json.RawMessage
	if err := c.get(ctx, "/Library/VirtualFolders", nil, &raw); err != nil {
		return nil, err
	}

	var folders []VirtualFolder
	if err := json.Unmarshal(raw, &folders); err == nil {
		return folders, nil
	}

	var wrapped struct {
		Items []VirtualFolder `json:"Items"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("unexpected VirtualFolders response shape: %w", err)
	}

	return wrapped.Items, nil
}

// Genres lists genre names present in the library (optionally one library or
// item type).
func (c *Client) Genres(ctx context.Context, parentID, includeItemTypes string) ([]string, error) {
	q := url.Values{}
	q.Set("Recursive", "true")
	if parentID != "" {
		q.Set("ParentId", parentID)
	}
	if includeItemTypes != "" {
		q.Set("IncludeItemTypes", includeItemTypes)
	}

	var resp itemsResponse
	if err := c.get(ctx, "/Genres", q, &resp); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(resp.Items))
	for _, g := range resp.Items {
		names = append(names, g.Name)
	}

	return names, nil
}

// Persons searches people (actors, directors, ...) known to the library.
func (c *Client) Persons(ctx context.Context, searchTerm string, limit int) ([]Item, error) {
	q := url.Values{}
	if searchTerm != "" {
		q.Set("SearchTerm", searchTerm)
	}
	if limit > 0 {
		q.Set("Limit", strconv.Itoa(limit))
	}

	var resp itemsResponse
	if err := c.get(ctx, "/Persons", q, &resp); err != nil {
		return nil, err
	}

	return resp.Items, nil
}
