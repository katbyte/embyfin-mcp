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

// setSortName sets the name an item sorts by. Emby works the sort name out
// from the name again whenever the item is saved unless the field is locked,
// so without the lock the edit was answered and never kept (seen live on Emby
// 4.10). Jellyfin keeps a forced sort name as it is, and has no such lock:
// it refuses an update naming one.
func setSortName(full map[string]any, name string, emby bool) {
	full["SortName"], full["ForcedSortName"] = name, name
	var locked []any
	if l, ok := full["LockedFields"].([]any); ok {
		locked = l
	}
	if emby && !slices.Contains(locked, any("SortName")) {
		full["LockedFields"] = append(locked, "SortName")
	}
}

// fieldText reads a text field off a full item map, "" when it has none.
func fieldText(full map[string]any, key string) string {
	if s, ok := full[key].(string); ok {
		return s
	}

	return ""
}

// sortNameOf is the name an item sorts by as set: the forced one when there
// is one (Jellyfin lowercases the sort name it works out), else the sort name.
func sortNameOf(full map[string]any) string {
	if forced := fieldText(full, "ForcedSortName"); forced != "" {
		return forced
	}

	return fieldText(full, "SortName")
}

// wasOnce records what a field was before an edit, the first time it is
// read: an edit sent again reads the item again.
func wasOnce(was map[string]string, field, value string) {
	if _, seen := was[field]; !seen {
		was[field] = value
	}
}

// editHeld reads an item back after an edit until four reads in a row, a
// settle interval apart, show it, and says whether they did: a refresh the
// server queued before the edit - the one a new collection gets, a scan's -
// saves the item over it a moment after the edit is answered.
func (r *registry) editHeld(ctx context.Context, userID, id string, holds func(full map[string]any) bool) (bool, error) {
	for range 4 {
		full, err := r.client.FullItem(ctx, userID, id)
		if err != nil {
			return false, err
		}
		if !holds(full) {
			return false, nil
		}
		if err := r.pause(ctx); err != nil {
			return false, err
		}
	}

	return true, nil
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

	type editIn struct {
		IDs      []string `json:"ids"                 jsonschema:"the library item ids to change: one, or many for the same change"`
		Name     string   `json:"name,omitempty"      jsonschema:"new display title (one item only)"`
		SortName string   `json:"sort_name,omitempty" jsonschema:"new sort title (one item only)"`
		Overview string   `json:"overview,omitempty"  jsonschema:"new overview/plot text (one item only)"`
		Year     int      `json:"year,omitempty"      jsonschema:"new production year (one item only)"`
		// the name lists: replaced whole, or edited by adding and removing
		Genres         []string `json:"genres,omitempty"          jsonschema:"replace each item's genres with these"`
		AddGenres      []string `json:"add_genres,omitempty"      jsonschema:"genres to add to each item's own, keeping the rest"`
		RemoveGenres   []string `json:"remove_genres,omitempty"   jsonschema:"genres to take off each item, keeping the rest"`
		Tags           []string `json:"tags,omitempty"            jsonschema:"replace each item's tags with these"`
		AddTags        []string `json:"add_tags,omitempty"        jsonschema:"tags to add to each item's own"`
		RemoveTags     []string `json:"remove_tags,omitempty"     jsonschema:"tags to take off each item"`
		Studios        []string `json:"studios,omitempty"         jsonschema:"replace each item's studios with these"`
		AddStudios     []string `json:"add_studios,omitempty"     jsonschema:"studios to add to each item's own"`
		RemoveStudios  []string `json:"remove_studios,omitempty"  jsonschema:"studios to take off each item"`
		OfficialRating string   `json:"official_rating,omitempty" jsonschema:"the parental rating to set, e.g. PG-13"`
	}
	type itemWas struct {
		ID   string         `json:"id"`
		Name string         `json:"name"`
		Was  map[string]any `json:"was"  jsonschema:"each field changed, as it was before, to set it back by"`
	}
	type editOut struct {
		Changed     []string  `json:"changed"                jsonschema:"the fields changed, on every item"`
		Updated     int       `json:"updated"                jsonschema:"items changed"`
		Items       []itemWas `json:"items"                  jsonschema:"each item changed, in the order given, with what the fields were"`
		NfoExpected []string  `json:"nfo_expected,omitempty" jsonschema:"the films, series, seasons and episodes changed in a library that saves nfos, whose nfo beside the media the server writes the edit into, over the one there, a moment after: expected from the library's setting, not read back. Other kinds (a song, a collection) get no nfo"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "item_edit",
		Description: "Change an item's metadata, or make the same change on many items in one call: title, sort title, overview and year on one item; genres, tags, studios and the parental rating on any number - every id given is changed. genres, tags and studios replace the list on every item; add_* and remove_* edit each item's own list, keeping the rest. Fields left out are untouched. metadata_rename renames a value wherever it is used. " +
			"What an edit does beyond the field: in a library that saves nfos, the server writes a film's, series', season's or episode's nfo beside its media with the edit, over the nfo there. On Emby a sort title set here is locked, so Emby no longer works it out from the title, and no tool unlocks it. official_rating hides the item from every account limited to ratings below it, and no tool can clear a rating once set. " +
			"An edit survives a normal refresh, and is undone by a refresh with replace_all, by item_identify_apply, and on Emby by a scan that finds the file written over, which reads the nfo beside it again (unless the library saves nfos, which hold the edit). " +
			"The item is read, changed and posted back whole: edits from this server wait for each other, but a save by the server itself between the read and the post (a scan, a refresh, the web app) can be posted over. " +
			"The answer gives each item's fields as they were, to set them back by, and the items whose nfo the server is expected to write; an edit that fails part way names the items already changed, and running the same call again finishes it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		if len(in.IDs) == 0 {
			return nil, editOut{}, errNoItems
		}
		single := map[string]bool{"name": in.Name != "", "sort_name": in.SortName != "", "overview": in.Overview != "", "year": in.Year > 0}
		if len(in.IDs) > 1 {
			for _, f := range []string{"name", "sort_name", "overview", "year"} {
				if single[f] {
					return nil, editOut{}, fmt.Errorf("%s is one item's own: pass one id to set it", f)
				}
			}
		}
		edits := []listEdit{
			{field: fieldGenres, replace: in.Genres, replaceGiven: in.Genres != nil, add: in.AddGenres, remove: in.RemoveGenres},
			{field: fieldTags, replace: in.Tags, replaceGiven: in.Tags != nil, add: in.AddTags, remove: in.RemoveTags},
			{field: fieldStudios, replace: in.Studios, replaceGiven: in.Studios != nil, add: in.AddStudios, remove: in.RemoveStudios},
		}
		var changed []string
		for f, set := range map[string]bool{"Name": single["name"], "SortName": single["sort_name"], "Overview": single["overview"], "ProductionYear": single["year"]} {
			if set {
				changed = append(changed, f)
			}
		}
		for _, e := range edits {
			if err := e.validate(); err != nil {
				return nil, editOut{}, err
			}
			if !e.empty() {
				changed = append(changed, strings.ToUpper(e.field[:1])+e.field[1:])
			}
		}
		rating := strings.TrimSpace(in.OfficialRating)
		if rating != "" {
			changed = append(changed, "OfficialRating")
		}
		if len(changed) == 0 {
			return nil, editOut{}, errors.New("nothing to change: pass name, sort_name, overview, year, genres, tags or studios (or their add_ and remove_ forms), or official_rating")
		}
		slices.Sort(changed)

		admin, err := client.ResolveUser(ctx, "")
		if err != nil {
			return nil, editOut{}, err
		}
		// which libraries write an edit to an nfo, read once
		folders, err := client.VirtualFolders(ctx)
		if err != nil {
			return nil, editOut{}, fmt.Errorf("could not read the libraries, to say which items' nfo an edit writes, so nothing was changed: %w", err)
		}
		out := editOut{Changed: changed, Items: make([]itemWas, 0, len(in.IDs))}
		for _, id := range in.IDs {
			var was map[string]any
			full, err := client.EditItem(ctx, admin.ID, id, func(full map[string]any) (bool, error) {
				was = map[string]any{}
				if single["name"] {
					was["name"], full["Name"] = full["Name"], in.Name
				}
				if single["sort_name"] {
					was["sort_name"] = sortNameOf(full)
					setSortName(full, in.SortName, client.Backend() == embyfin.Emby)
				}
				if single["overview"] {
					was["overview"], full["Overview"] = full["Overview"], in.Overview
				}
				if single["year"] {
					was["year"], full["ProductionYear"] = full["ProductionYear"], in.Year
				}
				for _, e := range edits {
					if !e.empty() {
						current := vocabularyOf(full, e.field)
						was[e.field] = current
						setVocabulary(full, e.field, e.apply(current))
					}
				}
				if rating != "" {
					was["official_rating"], full["OfficialRating"] = full["OfficialRating"], rating
				}
				return true, nil
			})
			if err != nil {
				done := make([]memberRow, 0, len(out.Items))
				for _, w := range out.Items {
					done = append(done, memberRow{ID: w.ID, Name: w.Name})
				}
				return nil, editOut{}, partlyDone(id, err, done, len(in.IDs))
			}
			out.Updated++
			out.Items = append(out.Items, itemWas{ID: id, Name: itemName(full), Was: was})
			if lib := embyfin.FolderOf(folders, fieldText(full, "Path")); lib != nil && lib.SavesNfo && nfoKinds[fieldText(full, "Type")] {
				out.NfoExpected = append(out.NfoExpected, id)
			}
		}

		return nil, out, nil
	})
}

// nfoKinds are the items a library that saves nfos writes one for when an
// edit saves them: a song's edit wrote none on Jellyfin (seen on 12.1), and
// a collection is kept in the server's own folder, not beside any media.
var nfoKinds = map[string]bool{typeMovie: true, "Series": true, "Season": true, typeEpisode: true}

// partlyDone is the error for a change made item by item that failed part
// way: the ones already changed, by id and name, so the caller knows the
// state the items are in (the answer that would have listed them is lost with
// the error), and that the same call again finishes it.
func partlyDone(failedID string, err error, done []memberRow, asked int) error {
	if len(done) == 0 {
		return fmt.Errorf("%s: %w; nothing was changed", failedID, err)
	}
	names := make([]string, 0, len(done))
	for _, d := range done {
		names = append(names, d.Name+" ("+d.ID+")")
	}

	return fmt.Errorf("%s: %w. %d of the %d items were already changed: %s. The rest were not: run the same call again to finish, which leaves the ones done as they are", failedID, err, len(done), asked, strings.Join(names, ", "))
}

// itemName is a full item map's name.
func itemName(full map[string]any) string {
	if name, ok := full["Name"].(string); ok {
		return name
	}

	return ""
}

// The servers count time in 100ns ticks. Every duration and position a tool
// takes or answers with is in seconds, so a runtime and a resume point on the
// same row can be divided without a conversion.
const (
	ticksPerSecond = 10_000_000
	ticksPerMinute = 60 * ticksPerSecond
)

// progressOf describes a user's place in an item: the minutes in, and the
// percent through it (capped: a position past a file's probed runtime is
// the server's to keep, not a percent above a hundred).
func progressOf(it *embyfin.Item) (seconds, percent int) {
	if it.UserData == nil {
		return 0, 0
	}
	seconds = int(it.UserData.PlaybackPositionTicks / ticksPerSecond)
	percent = min(int(it.UserData.PlayedPercentage), 100)

	return seconds, percent
}
