package workarounds

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/internal/pandorest/openapi"
)

// The Jellyfin document is the one published with the release; it matches
// its server closely, so there is little here.

const jellyfin = "jellyfin"

type jellyfinCreatePlaylistQuery struct{}

func (jellyfinCreatePlaylistQuery) Name() string    { return "jellyfin-create-playlist-query" }
func (jellyfinCreatePlaylistQuery) Service() string { return jellyfin }
func (jellyfinCreatePlaylistQuery) Bug() string {
	return "POST /Playlists still declares the deprecated name, ids, userId and mediaType query parameters; the server answers 400 to that form and reads the CreatePlaylistDto body"
}

var jellyfinPlaylistQuery = []string{"name", "ids", "userId", "mediaType"}

func (jellyfinCreatePlaylistQuery) Apply(spec *openapi.Spec) error {
	op, err := operation(spec, "POST", "/Playlists")
	if err != nil {
		return err
	}
	for _, name := range jellyfinPlaylistQuery {
		if p := op.Parameter(openapi.InQuery, name); p == nil || !p.Deprecated {
			return fmt.Errorf("the deprecated %s query parameter is gone", name)
		}
	}
	if op.RequestBody == nil {
		return errors.New("it no longer takes a body")
	}
	op.Parameters = slices.DeleteFunc(op.Parameters, func(p *openapi.Parameter) bool {
		return p.In == openapi.InQuery && slices.ContainsFunc(jellyfinPlaylistQuery, func(n string) bool { return strings.EqualFold(n, p.Name) })
	})

	return nil
}
