package embyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	apiclient "github.com/katbyte/embyfin-mcp/sdk/client"
	"github.com/katbyte/go-kt/lock"
)

// configPath is where both servers keep their settings document.
const configPath = "/System/Configuration"

// ServerConfig reads the server's settings as one document, every setting
// by the server's own name and as the server sent it: a number keeps its
// digits (json.Number), a group of settings is a document inside it.
//
// It is read as the server's JSON and not into the typed client's model,
// which knows only the settings of the version it was generated from: a
// newer server's other settings would be dropped on the way in, and reset on
// the way back.
func (c *Client) ServerConfig(ctx context.Context) (map[string]any, error) {
	var doc map[string]any
	if err := c.document(ctx, configPath, "the server's settings", &doc); err != nil {
		return nil, err
	}

	return doc, nil
}

// document reads what the server answers a GET of path with into out as the
// server's own JSON, a number keeping its digits (json.Number), and not
// through the typed client's model of it. what names it for the error said
// when it is not the JSON out takes.
func (c *Client) document(ctx context.Context, path, what string, out any) error {
	req, err := c.base().NewRequest(ctx, apiclient.RequestOptions{
		ExpectedStatusCodes: []int{http.StatusOK},
		HTTPMethod:          http.MethodGet,
		Path:                path,
	})
	if err != nil {
		return err
	}
	resp, err := req.Execute(ctx)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := resp.Unmarshal(&raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%s are not the JSON document expected: %w", what, err)
	}

	return nil
}

// ErrConfigNotEditable is what EditServerConfig answers on a server whose
// settings this does not change.
var ErrConfigNotEditable = errors.New("the server's settings are changed on Jellyfin alone so far")

// ErrConfigUnchanged is what a change handed to EditServerConfig returns
// when it found nothing to change: nothing is then sent to the server.
var ErrConfigUnchanged = errors.New("the settings already are as asked")

// EditServerConfig changes Jellyfin's settings: the document is read whole,
// handed to change, and sent back whole, since Jellyfin has no way to send
// one setting. Every setting change leaves alone goes back exactly as it
// came, the ones this client's version has never heard of included. It
// answers with the document as it was and as the server gives it afterwards.
// A change that returns ErrConfigUnchanged sends nothing.
//
// Two changes at once would each send back what it read and the later undo
// the earlier, so changes from this process take turns.
func (c *Client) EditServerConfig(ctx context.Context, change func(doc map[string]any) error) (before, after map[string]any, err error) {
	if c.isEmby() {
		return nil, nil, ErrConfigNotEditable
	}
	unlock := lock.ByString(c.baseURL + " server settings")
	defer unlock()

	if before, err = c.ServerConfig(ctx); err != nil {
		return nil, nil, err
	}
	// a second copy to change, so that before stays as it was read
	doc, err := c.ServerConfig(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := change(doc); err != nil {
		if errors.Is(err, ErrConfigUnchanged) {
			return before, before, nil
		}

		return nil, nil, err
	}

	req, err := c.base().NewRequest(ctx, apiclient.RequestOptions{
		ContentType:         "application/json",
		ExpectedStatusCodes: []int{http.StatusNoContent, http.StatusOK},
		HTTPMethod:          http.MethodPost,
		Path:                configPath,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := req.Marshal(doc); err != nil {
		return nil, nil, err
	}
	if _, err := req.Execute(ctx); err != nil {
		return nil, nil, err
	}
	if after, err = c.ServerConfig(ctx); err != nil {
		return before, nil, fmt.Errorf("the server took the change, and its settings could not be read again to check it: %w", err)
	}

	return before, after, nil
}
