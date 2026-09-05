package embyfin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type User struct {
	ID               string `json:"Id"`
	Name             string `json:"Name"`
	LastActivityDate string `json:"LastActivityDate,omitempty"`
	Policy           struct {
		IsAdministrator bool `json:"IsAdministrator"`
	} `json:"Policy"`
}

func (c *Client) Users(ctx context.Context) ([]User, error) {
	var users []User
	if err := c.get(ctx, "/Users", nil, &users); err != nil {
		return nil, err
	}

	return users, nil
}

// ResolveUser finds a user by name (case-insensitive) or id. An empty
// nameOrID resolves to the first administrator.
func (c *Client) ResolveUser(ctx context.Context, nameOrID string) (*User, error) {
	users, err := c.Users(ctx)
	if err != nil {
		return nil, err
	}

	if nameOrID == "" {
		for i := range users {
			if users[i].Policy.IsAdministrator {
				return &users[i], nil
			}
		}
		if len(users) > 0 {
			return &users[0], nil
		}
		return nil, errors.New("server has no users")
	}

	names := make([]string, 0, len(users))
	for i := range users {
		if strings.EqualFold(users[i].Name, nameOrID) || users[i].ID == nameOrID {
			return &users[i], nil
		}
		names = append(names, users[i].Name)
	}

	return nil, fmt.Errorf("no user named %q (have: %s)", nameOrID, strings.Join(names, ", "))
}

// SetPlayed marks an item played or unplayed for a user.
// Emby keeps the legacy per-user route; Jellyfin moved it in 10.9.
func (c *Client) SetPlayed(ctx context.Context, userID, itemID string, played bool) error {
	path := "/Users/" + url.PathEscape(userID) + "/PlayedItems/" + url.PathEscape(itemID)
	q := url.Values{}
	if c.backend == Jellyfin {
		path = "/UserPlayedItems/" + url.PathEscape(itemID)
		q.Set("userId", userID)
	}

	if played {
		return c.post(ctx, path, q, nil, nil)
	}

	return c.del(ctx, path, q)
}

// SetFavourite marks an item as a favourite (or not) for a user. (The API
// route keeps the upstream FavoriteItems spelling.)
func (c *Client) SetFavourite(ctx context.Context, userID, itemID string, favourite bool) error {
	path := "/Users/" + url.PathEscape(userID) + "/FavoriteItems/" + url.PathEscape(itemID)
	q := url.Values{}
	if c.backend == Jellyfin {
		path = "/UserFavoriteItems/" + url.PathEscape(itemID)
		q.Set("userId", userID)
	}

	if favourite {
		return c.post(ctx, path, q, nil, nil)
	}

	return c.del(ctx, path, q)
}

// NextUp returns the next episodes to watch per series for a user.
func (c *Client) NextUp(ctx context.Context, userID string, limit int) ([]Item, error) {
	q := url.Values{}
	q.Set("UserId", userID)
	q.Set("Fields", FieldsDefault)
	if limit > 0 {
		q.Set("Limit", strconv.Itoa(limit))
	}

	var resp itemsResponse
	if err := c.get(ctx, "/Shows/NextUp", q, &resp); err != nil {
		return nil, err
	}

	return resp.Items, nil
}

// Resume returns partially-watched items for a user.
func (c *Client) Resume(ctx context.Context, userID string, limit int) ([]Item, error) {
	path := "/Users/" + url.PathEscape(userID) + "/Items/Resume"
	q := url.Values{}
	if c.backend == Jellyfin {
		path = "/UserItems/Resume"
		q.Set("userId", userID)
	}
	q.Set("Fields", FieldsDefault)
	q.Set("EnableUserData", "true")
	if limit > 0 {
		q.Set("Limit", strconv.Itoa(limit))
	}

	var resp itemsResponse
	if err := c.get(ctx, path, q, &resp); err != nil {
		return nil, err
	}

	return resp.Items, nil
}
