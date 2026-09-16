package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fullItemNames reads a name list off a full item map, whichever way the
// server spells it: plain strings (Genres, Jellyfin's Tags) or named records
// (Studios, Emby's TagItems and GenreItems).
func fullItemNames(full map[string]any, key string) []string {
	raw, ok := full[key].([]any)
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		switch v := v.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			if name, ok := v["Name"].(string); ok {
				out = append(out, name)
			}
		}
	}

	return out
}

// vocabularyOf reads one of the vocabulary fields off a full item map. Emby
// keeps tags only as TagItems (Tags is null); Jellyfin only as Tags.
func vocabularyOf(full map[string]any, field string) []string {
	switch field {
	case fieldGenres:
		return fullItemNames(full, "Genres")
	case fieldTags:
		if tags := fullItemNames(full, "Tags"); len(tags) > 0 {
			return tags
		}
		return fullItemNames(full, "TagItems")
	case fieldStudios:
		return fullItemNames(full, "Studios")
	}

	return nil
}

// setVocabulary writes a vocabulary field onto a full item map in every
// spelling a server reads: Jellyfin the plain Genres and Tags lists, Emby
// the named GenreItems and TagItems; studios are named records on both.
func setVocabulary(full map[string]any, field string, values []string) {
	if values == nil {
		values = []string{}
	}
	switch field {
	case fieldGenres:
		full["Genres"], full["GenreItems"] = values, nameRefs(values)
	case fieldTags:
		full["Tags"], full["TagItems"] = values, nameRefs(values)
	case fieldStudios:
		full["Studios"] = nameRefs(values)
	}
}

// listEdit is a change to one name list: a replacement, or names added and
// removed, compared case-insensitively (both servers fold a second spelling
// that differs only in case into the first).
type listEdit struct {
	replace      []string
	add, remove  []string
	replaceGiven bool
	field        string
}

func (e listEdit) empty() bool { return !e.replaceGiven && len(e.add) == 0 && len(e.remove) == 0 }

func (e listEdit) validate() error {
	if e.replaceGiven && (len(e.add) > 0 || len(e.remove) > 0) {
		return fmt.Errorf("%s replaces the list; add_%s and remove_%s edit it: pass one or the other", e.field, e.field, e.field)
	}

	return nil
}

// apply returns the edited list, in its order with additions at the end.
func (e listEdit) apply(current []string) []string {
	if e.replaceGiven {
		return cleanNames(e.replace)
	}
	out := slices.Clone(current)
	for _, a := range cleanNames(e.add) {
		if !slices.ContainsFunc(out, func(x string) bool { return strings.EqualFold(x, a) }) {
			out = append(out, a)
		}
	}

	return slices.DeleteFunc(out, func(x string) bool {
		return slices.ContainsFunc(e.remove, func(d string) bool { return strings.EqualFold(strings.TrimSpace(d), x) })
	})
}

// cleanNames trims names and drops the empty ones.
func cleanNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}

	return out
}

func registerItemEditTools(r *registry) {
	client := r.client

	type batchIn struct {
		IDs            []string `json:"ids"                       jsonschema:"the library item ids to change"`
		Genres         []string `json:"genres,omitempty"          jsonschema:"replace each item's genres with these"`
		AddGenres      []string `json:"add_genres,omitempty"      jsonschema:"genres to add to each item's own, keeping the rest"`
		RemoveGenres   []string `json:"remove_genres,omitempty"   jsonschema:"genres to take off each item, keeping the rest"`
		Tags           []string `json:"tags,omitempty"            jsonschema:"replace each item's tags with these"`
		AddTags        []string `json:"add_tags,omitempty"        jsonschema:"tags to add to each item's own"`
		RemoveTags     []string `json:"remove_tags,omitempty"     jsonschema:"tags to take off each item"`
		Studios        []string `json:"studios,omitempty"         jsonschema:"replace each item's studios with these"`
		AddStudios     []string `json:"add_studios,omitempty"     jsonschema:"studios to add to each item's own"`
		RemoveStudios  []string `json:"remove_studios,omitempty"  jsonschema:"studios to take off each item"`
		OfficialRating string   `json:"official_rating,omitempty" jsonschema:"the parental rating to set on every item, e.g. PG-13"`
	}
	type batchOut struct {
		Updated int      `json:"items_updated"`
		Items   []string `json:"items"         jsonschema:"the titles changed, in the order given"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_batch_edit",
		Description: "Apply the same metadata change to many items in one call: a genre on forty films, a tag on a franchise, a studio spelled right everywhere it was typed. genres, tags and studios replace the list on every item; add_* and remove_* edit each item's own list, keeping the rest. Fields left out are untouched. Use item_edit for one item or for fields that differ per item, and metadata_rename to rename a value wherever it is used. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in batchIn) (*mcp.CallToolResult, batchOut, error) {
		if len(in.IDs) == 0 {
			return nil, batchOut{}, errNoItems
		}
		edits := []listEdit{
			{field: fieldGenres, replace: in.Genres, replaceGiven: in.Genres != nil, add: in.AddGenres, remove: in.RemoveGenres},
			{field: fieldTags, replace: in.Tags, replaceGiven: in.Tags != nil, add: in.AddTags, remove: in.RemoveTags},
			{field: fieldStudios, replace: in.Studios, replaceGiven: in.Studios != nil, add: in.AddStudios, remove: in.RemoveStudios},
		}
		changes := false
		for _, e := range edits {
			if err := e.validate(); err != nil {
				return nil, batchOut{}, err
			}
			changes = changes || !e.empty()
		}
		rating := strings.TrimSpace(in.OfficialRating)
		if !changes && rating == "" {
			return nil, batchOut{}, errors.New("nothing to change: pass genres, tags or studios (or their add_ and remove_ forms), or official_rating")
		}

		admin, err := client.ResolveUser(ctx, "")
		if err != nil {
			return nil, batchOut{}, err
		}
		out := batchOut{Items: make([]string, 0, len(in.IDs))}
		for _, id := range in.IDs {
			full, err := client.EditItem(ctx, admin.ID, id, func(full map[string]any) (bool, error) {
				for _, e := range edits {
					if !e.empty() {
						setVocabulary(full, e.field, e.apply(vocabularyOf(full, e.field)))
					}
				}
				if rating != "" {
					full["OfficialRating"] = rating
				}
				return true, nil
			})
			if err != nil {
				return nil, out, fmt.Errorf("%s: %w (%d items were updated before it)", id, err, out.Updated)
			}
			out.Updated++
			out.Items = append(out.Items, itemName(full))
		}

		return nil, out, nil
	})

	type progressIn struct {
		ID              string  `json:"id"               jsonschema:"the library item id"`
		User            string  `json:"user,omitempty"   jsonschema:"user name or id; defaults to the first administrator"`
		PositionMinutes float64 `json:"position_minutes" jsonschema:"where to resume from, in minutes from the start; above zero (item_set_watched watched=false clears a resume point)"`
	}
	type progressOut struct {
		Item            string  `json:"item"`
		User            string  `json:"user"`
		PositionMinutes float64 `json:"position_minutes"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "item_set_progress",
		Description: "Set where a user is in a film or episode, so it shows under continue watching from that point, and mark it not yet watched. user_in_progress lists what is in progress. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in progressIn) (*mcp.CallToolResult, progressOut, error) {
		if in.PositionMinutes <= 0 {
			return nil, progressOut{}, errors.New("position_minutes must be above zero; to clear a resume point, mark the item unwatched with item_set_watched")
		}
		user, err := client.ResolveUser(ctx, in.User)
		if err != nil {
			return nil, progressOut{}, err
		}
		it, err := visibleTo(ctx, client, user, in.ID)
		if err != nil {
			return nil, progressOut{}, err
		}
		if err := client.SetProgress(ctx, user.ID, in.ID, int64(in.PositionMinutes*ticksPerMinute)); err != nil {
			return nil, progressOut{}, err
		}

		return nil, progressOut{Item: it.Name, User: user.Name, PositionMinutes: in.PositionMinutes}, nil
	})
}

// itemName is a full item map's name.
func itemName(full map[string]any) string {
	if name, ok := full["Name"].(string); ok {
		return name
	}

	return ""
}

// ticksPerMinute is the servers' 100ns ticks in a minute.
const ticksPerMinute = 600_000_000

// progressOf describes a user's place in an item: the minutes in, and the
// percent through it (capped: a position past a file's probed runtime is
// the server's to keep, not a percent above a hundred).
func progressOf(it *embyfin.Item) (minutes float64, percent int) {
	if it.UserData == nil {
		return 0, 0
	}
	minutes = float64(it.UserData.PlaybackPositionTicks) / ticksPerMinute
	percent = min(int(it.UserData.PlayedPercentage), 100)

	return minutes, percent
}
