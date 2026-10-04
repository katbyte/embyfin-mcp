package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/mediapath"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// memberRow is an item a collection or playlist held, by id and name: what
// putting it back takes.
type memberRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// membersOf reads what a collection holds itself, by id and name: a series
// as the series, not its seasons and episodes.
func membersOf(ctx context.Context, client *embyfin.Client, collectionID string) ([]memberRow, error) {
	items, err := client.CollectionItems(ctx, collectionID, embyfin.FieldsLean, nil)
	if err != nil {
		return nil, err
	}

	return memberRows(items), nil
}

// memberRows is items by id and name, in order.
func memberRows(items []embyfin.Item) []memberRow {
	out := make([]memberRow, 0, len(items))
	for i := range items {
		out = append(out, memberRow{ID: items[i].ID, Name: items[i].Name})
	}

	return out
}

// membersSaid names members for a sentence.
func membersSaid(rows []memberRow) string {
	names := make([]string, 0, len(rows))
	for _, m := range rows {
		names = append(names, m.Name+" ("+m.ID+")")
	}

	return listed(names, 50)
}

// collectionNow is a collection as it was before a create: its name, the name
// its folder keeps - the one it was first made with, "" where the server does
// not list the folder - and, where it does not, which of the items asked for
// it held.
type collectionNow struct {
	id, name, firstName string
	// held is which of the items asked for it held, read where the folder
	// is not listed (heldRead); nothing else of what it held is known
	held     []string
	heldRead bool
}

// boxsetFolder is how Jellyfin ends a collection's folder name, after the
// name it was first made with.
const boxsetFolder = " [boxset]"

// folderName is a collection's name as the servers spell it in the name of
// its folder, for telling whether a create by one name reaches the folder of
// another: a character a file name cannot hold - / \ : * ? " < > | - becomes a
// space (Jellyfin kept "Zzyzx AC/DC: Live? *" as "Zzyzx AC DC  Live    [boxset]",
// seen on 12.1, and Emby its metadata as "Zzyzx AC DC Live", seen on 4.10),
// and case, runs of spaces and the ends are set aside. Two names taken for one
// that are not only refuses a name that would have worked.
func folderName(name string) string {
	mapped := strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return ' '
		}
		return r
	}, name)

	return strings.ToLower(strings.Join(strings.Fields(mapped), " "))
}

// collectionsNow reads every collection before a create of the items asked
// for. A collection whose folder the server lists (Jellyfin) has its first
// name read off it. One whose folder it does not (every one on Emby, which
// adds the items of a create that reaches it) has read which of the items
// asked for it holds, readsAtOnce collections at a time, so that one the
// create reaches all the same can have what it gained taken out again.
func collectionsNow(ctx context.Context, client *embyfin.Client, asked []string) ([]collectionNow, error) {
	cols, _, err := client.Search(ctx, embyfin.SearchOptions{IncludeItemTypes: "BoxSet", Fields: "Path"})
	if err != nil {
		return nil, err
	}
	out := make([]collectionNow, len(cols))
	for i := range cols {
		out[i] = collectionNow{id: cols[i].ID, name: cols[i].Name}
		if base := mediapath.Base(cols[i].Path); cols[i].Path != "" && strings.HasSuffix(strings.ToLower(base), boxsetFolder) {
			out[i].firstName = base[:len(base)-len(boxsetFolder)]
		}
	}
	err = eachAtOnce(ctx, len(out), func(ctx context.Context, i int) error {
		if out[i].firstName != "" || len(asked) == 0 {
			return nil
		}
		items, rerr := client.CollectionItems(ctx, out[i].id, "Path", asked)
		if rerr != nil {
			return fmt.Errorf("reading what the collection %s holds: %w", out[i].name, rerr)
		}
		out[i].held, out[i].heldRead = idsOf(items), true
		return nil
	})

	return out, err
}

// putCollectionBack puts back what it can of a collection a create reached in
// place of making one, and is the error that says what happened. Of the items
// asked for, the ones it gained are taken out again where which it held
// before was read (Emby, which adds them); where it was not (Jellyfin, which
// replaces what the collection holds with them), nothing is taken out, since
// what it held before is not known, and the error says so and what it holds
// now. Its name is set back either way.
func putCollectionBack(ctx context.Context, client *embyfin.Client, was collectionNow, asked string, askedIDs []string) error {
	reached := fmt.Sprintf("no new collection was made: the server answered the create of %q with the existing collection %q (id %s), which was first made under a name its folder shares", asked, was.name, was.id)
	nowItems, err := client.CollectionItems(ctx, was.id, embyfin.FieldsLean, nil)
	if err != nil {
		return fmt.Errorf("%s; reading what it holds now failed, so nothing of it was put back: %w", reached, err)
	}
	now := idsOf(nowItems)
	var done, failed, unknown []string
	switch {
	case was.heldRead:
		gained := slices.DeleteFunc(slices.Clone(askedIDs), func(id string) bool {
			return !slices.Contains(now, id) || slices.Contains(was.held, id)
		})
		if len(gained) > 0 {
			gainedSaid := namedAmong(nowItems, gained)
			if err := client.RemoveFromCollection(ctx, was.id, gained); err != nil {
				failed = append(failed, fmt.Sprintf("taking out %s, which it gained: %v", gainedSaid, err))
			} else {
				done = append(done, "took out "+gainedSaid+", which it gained")
			}
		}
	default:
		unknown = append(unknown, "what it held before was not read, so nothing was taken out of it or added back: the server may have replaced what it held with the items asked for (Jellyfin does), and it holds now "+membersSaid(memberRows(nowItems)))
	}
	if col, err := client.ItemByID(ctx, was.id); err != nil {
		failed = append(failed, fmt.Sprintf("reading its name: %v", err))
	} else if col.Name != was.name {
		admin, err := client.ResolveUser(ctx, "")
		if err == nil {
			_, err = client.EditItem(ctx, admin.ID, was.id, func(full map[string]any) (bool, error) {
				full["Name"] = was.name
				return true, nil
			})
		}
		if err != nil {
			failed = append(failed, fmt.Sprintf("renaming it back from %q to %q: %v", col.Name, was.name, err))
		} else {
			done = append(done, fmt.Sprintf("renamed it back from %q", col.Name))
		}
	}
	switch {
	case len(failed) > 0:
		put := "nothing was put back"
		if len(done) > 0 {
			put = "it " + strings.Join(done, "; ")
		}
		return fmt.Errorf("%s. Putting it back, %s, and these failed: %s%s", reached, put, strings.Join(failed, "; "), prefixed(". ", unknown))
	case len(unknown) > 0:
		return fmt.Errorf("%s. %s%s: choose another name", reached, strings.Join(unknown, "; "), prefixed(". It ", done))
	case len(done) > 0:
		return fmt.Errorf("%s. It was put back as it was (%s): choose another name, or add to it with collection_edit add_items", reached, strings.Join(done, "; "))
	}

	return fmt.Errorf("%s, and holds what it held: choose another name, or add to it with collection_edit add_items", reached)
}

// namedAmong names ids by the items holding them, each once.
func namedAmong(items []embyfin.Item, ids []string) string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		name := id
		if i := slices.IndexFunc(items, func(it embyfin.Item) bool { return it.ID == id }); i >= 0 {
			name = items[i].Name + " (" + id + ")"
		}
		names = append(names, name)
	}

	return listed(names, 50)
}

// prefixed joins parts after a lead-in, or is "" for none.
func prefixed(lead string, parts []string) string {
	if len(parts) == 0 {
		return ""
	}

	return lead + strings.Join(parts, "; ")
}

func registerCollectionTools(r *registry) {
	client := r.client
	type collectionRow struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type listOut struct {
		Collections []collectionRow `json:"collections"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "collection_list",
		Description: "List all collections (boxsets).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, listOut, error) {
		items, _, err := client.Search(ctx, embyfin.SearchOptions{IncludeItemTypes: "BoxSet", Fields: embyfin.FieldsLean})
		if err != nil {
			return nil, listOut{}, err
		}

		out := listOut{}
		for i := range items {
			it := &items[i]
			out.Collections = append(out.Collections, collectionRow{ID: it.ID, Name: it.Name})
		}

		return nil, out, nil
	})

	type getIn struct {
		Collection string `json:"collection" jsonschema:"collection name (case-insensitive) or id"`
	}
	type getOut struct {
		Name  string        `json:"name"`
		Items []itemSummary `json:"items"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "collection_get",
		Description: "A collection's contents.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		col, err := client.ResolveByType(ctx, "BoxSet", in.Collection)
		if err != nil {
			return nil, getOut{}, err
		}

		items, err := client.CollectionItems(ctx, col.ID, "", nil)
		if err != nil {
			return nil, getOut{}, err
		}

		return nil, getOut{Name: col.Name, Items: summariseAll(items)}, nil
	})

	type createIn struct {
		Name    string   `json:"name"               jsonschema:"name for the new collection"`
		ItemIDs []string `json:"item_ids,omitempty" jsonschema:"initial items"`
	}
	type createOut struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Items int    `json:"items"          jsonschema:"items the new collection holds, read back"`
		Note  string `json:"note,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "collection_create",
		Description: "Create a new collection (boxset) holding the given items (Emby needs at least one), and check a moment later that it still holds them. " +
			"The first collection made on a server makes it add a Collections library and start a scan of every library, which drops items whose files are gone, re-reads changed files, and on Emby saves every playlist back as it found it. " +
			"Both servers keep a collection in a folder named after the name it was first made with, whatever it has been renamed since, with any character a file name cannot hold (/ \\ : * ? \" < > |) made a space, and a new collection whose name comes to the same folder name reaches the old one instead of making another: Emby adds the items to it, and Jellyfin replaces what it holds with them and takes its first name back. So a name another collection has, or was first made with, is refused, as is one that differs from those only in case, spaces or such characters, and collection_edit add_items adds to that one. " +
			"Emby does not show which name a collection was first made with: there, if the server answers with a collection that was already there, the items it gained are taken out of it again and the call is an error saying so; where what such a collection held before was not read, nothing is taken out and the error says what it holds now. " +
			membersScanSaid,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, createOut, error) {
		// the name is taken by one collection, or by several already, which
		// resolveByType reports as an ambiguity rather than a hit
		existing, err := client.ResolveByType(ctx, "BoxSet", in.Name)
		switch {
		case err == nil:
			return nil, createOut{}, fmt.Errorf("a collection named %q exists (id %s): add to it with collection_edit add_items", existing.Name, existing.ID)
		case strings.Contains(err.Error(), "are named"):
			return nil, createOut{}, fmt.Errorf("the name is taken: %w", err)
		}
		// the name a collection was first made with, which its folder keeps
		// (Jellyfin lists the folder; Emby does not), and which of the items
		// asked for each holds now, to put back one the server reaches all
		// the same
		before, err := collectionsNow(ctx, client, in.ItemIDs)
		if err != nil {
			return nil, createOut{}, fmt.Errorf("could not read the collections there are, so none was made: %w", err)
		}
		want := folderName(in.Name)
		for _, c := range before {
			switch {
			case c.firstName != "" && folderName(c.firstName) == want:
				return nil, createOut{}, fmt.Errorf("the collection %q (id %s) is kept in the folder %q, from the name it was first made with: a new collection named %q would come to the same folder and reach that one instead of making another. Nothing was made: choose another name, or add to it with collection_edit add_items", c.name, c.id, c.firstName+boxsetFolder, in.Name)
			case folderName(c.name) == want:
				return nil, createOut{}, fmt.Errorf("the collection %q (id %s) differs from %q only in case, spaces or characters a folder name cannot hold, which both servers set aside in naming its folder: a new collection by it could reach that one instead of making another. Nothing was made: choose another name, or add to it with collection_edit add_items", c.name, c.id, in.Name)
			}
		}

		scanning, err := client.ScansRunning(ctx)
		if err != nil {
			return nil, createOut{}, fmt.Errorf("could not tell whether a library scan was running, which can put back or drop a collection's members, so none was made: %w", err)
		}
		id, err := client.CreateCollection(ctx, in.Name, in.ItemIDs)
		if err != nil {
			return nil, createOut{}, err
		}
		if old := slices.IndexFunc(before, func(c collectionNow) bool { return c.id == id }); old >= 0 {
			return nil, createOut{}, putCollectionBack(ctx, client, before[old], in.Name, in.ItemIDs)
		}
		if err := client.KeepMembers(ctx, id, in.ItemIDs); err != nil {
			return nil, createOut{}, fmt.Errorf("the collection %s was made (id %s), but checking it kept its items failed: %w", in.Name, id, err)
		}
		members, err := client.CollectionMembers(ctx, id)
		if err != nil {
			return nil, createOut{}, fmt.Errorf("the collection %s was made (id %s) and holds its items, but reading it back failed: %w", in.Name, id, err)
		}
		note, err := membersRace(ctx, client, scanning)
		if err != nil {
			return nil, createOut{}, fmt.Errorf("the collection %s was made (id %s) holding %d items, but %w", in.Name, id, len(members), err)
		}

		return nil, createOut{ID: id, Name: in.Name, Items: len(members), Note: note}, nil
	})

	type editIn struct {
		Collection  string   `json:"collection"             jsonschema:"collection name or id"`
		Name        string   `json:"name,omitempty"         jsonschema:"rename the collection"`
		SortName    string   `json:"sort_name,omitempty"    jsonschema:"the name it sorts by, e.g. Alien 1 to keep a saga together"`
		Overview    string   `json:"overview,omitempty"     jsonschema:"the collection's description"`
		AddItems    []string `json:"add_items,omitempty"    jsonschema:"library item ids to add; one it already holds is left as it is and counted as already_held"`
		RemoveItems []string `json:"remove_items,omitempty" jsonschema:"library item ids to take out (the items stay in the library); one it does not hold is an error, and nothing is taken out"`
	}
	type editOut struct {
		Name         string            `json:"name"`
		Updated      []string          `json:"changed"                 jsonschema:"the fields changed: Name, SortName, Overview"`
		Was          map[string]string `json:"was"                     jsonschema:"each field changed, as it was before"`
		Added        int               `json:"added,omitempty"         jsonschema:"items the collection gained"`
		AlreadyHeld  int               `json:"already_held,omitempty"  jsonschema:"items asked to add that it already held, which a collection cannot hold twice"`
		Removed      int               `json:"removed,omitempty"       jsonschema:"items that left it"`
		RemovedItems []memberRow       `json:"removed_items,omitempty" jsonschema:"the items taken out, by id and name, to put back with add_items"`
		Note         string            `json:"note,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "collection_edit",
		Description: "Change a collection: rename it, set the name it sorts by or its description, and add items to it or take items out of it (the items stay in the library), any or all in one call. Only the fields given change. An item added that it already holds is left as it is and counted as already_held; an item taken out that it does not hold is an error, and nothing is taken out. " +
			"The fields are changed first: it reads the collection back a moment later and sends the edit once more if the refresh a new or changed collection gets saved it over the edit. Then the items taken out, and it answers once they have left and are still out a moment later; then the items added, which it checks a moment later that it still holds. The answer names the items taken out, by id and name, so add_items can put them back. " +
			"A rename leaves the collection's folder under the name it was first made with, which collection_create then refuses. On Emby a sort name set here is locked, so Emby no longer works it out from the name, and no tool unlocks it. The answer gives what each field was. " + membersScanSaid,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
		fields := in.Name != "" || in.SortName != "" || in.Overview != ""
		if !fields && len(in.AddItems) == 0 && len(in.RemoveItems) == 0 {
			return nil, editOut{}, errors.New("nothing to change: pass name, sort_name, overview, add_items or remove_items")
		}
		for _, id := range in.AddItems {
			if slices.Contains(in.RemoveItems, id) {
				return nil, editOut{}, fmt.Errorf("item %s is in add_items and in remove_items: nothing was changed", id)
			}
		}
		col, err := client.ResolveByType(ctx, "BoxSet", in.Collection)
		if err != nil {
			return nil, editOut{}, err
		}
		out := editOut{Name: col.Name, Updated: []string{}, Was: map[string]string{}}
		if fields {
			if err := r.editCollectionFields(ctx, col, in.Name, in.SortName, in.Overview, &out.Name, &out.Updated, out.Was); err != nil {
				return nil, editOut{}, err
			}
		}
		if len(in.AddItems) == 0 && len(in.RemoveItems) == 0 {
			return nil, out, nil
		}
		// what the edit of the fields did, for an error after it
		done := ""
		if len(out.Updated) > 0 {
			done = fmt.Sprintf("; %s already changed (%s)", col.Name, strings.Join(out.Updated, ", "))
		}
		// counted by what the collection holds before and after, not by what
		// was asked: both servers answer an add of a member with a success and
		// change nothing
		held, err := membersOf(ctx, client, col.ID)
		if err != nil {
			return nil, editOut{}, fmt.Errorf("%w%s", err, done)
		}
		scanning, err := client.ScansRunning(ctx)
		if err != nil {
			return nil, editOut{}, fmt.Errorf("could not tell whether a library scan was running, which can put back or drop a collection's members, so its members were not changed: %w%s", err, done)
		}
		changed := []string{}
		if len(in.RemoveItems) > 0 {
			if err := client.RemoveFromCollection(ctx, col.ID, in.RemoveItems); err != nil {
				return nil, editOut{}, fmt.Errorf("%w%s", err, done)
			}
			out.RemovedItems = []memberRow{}
			for _, m := range held {
				if slices.Contains(in.RemoveItems, m.ID) {
					out.RemovedItems = append(out.RemovedItems, m)
				}
			}
			after, rerr := membersOf(ctx, client, col.ID)
			if rerr != nil {
				return nil, editOut{}, fmt.Errorf("took %s out of %s, but reading the collection back failed: %w%s", membersSaid(out.RemovedItems), col.Name, rerr, done)
			}
			out.Removed = max(len(held)-len(after), 0)
			held = after
			changed = append(changed, "took out "+membersSaid(out.RemovedItems))
		}
		if len(in.AddItems) > 0 {
			var fresh []string
			for _, id := range in.AddItems {
				if !slices.ContainsFunc(held, func(m memberRow) bool { return m.ID == id }) && !slices.Contains(fresh, id) {
					fresh = append(fresh, id)
				}
			}
			out.Added, out.AlreadyHeld = len(fresh), len(in.AddItems)-len(fresh)
			if len(fresh) > 0 {
				if err := client.AddToCollection(ctx, col.ID, fresh); err != nil {
					return nil, editOut{}, fmt.Errorf("%w%s", err, prefixed("; already done: ", changed)+done)
				}
				changed = append(changed, fmt.Sprintf("added %d items", len(fresh)))
			}
		}
		if len(changed) > 0 {
			if out.Note, err = membersRace(ctx, client, scanning); err != nil {
				return nil, editOut{}, fmt.Errorf("%s in %s, but %w", strings.Join(changed, " and "), col.Name, err)
			}
		}

		return nil, out, nil
	})

	type deleteIn struct {
		Collection string `json:"collection" jsonschema:"collection name or id"`
	}
	type deleteOut struct {
		Deleted  string      `json:"deleted"`
		ID       string      `json:"id"`
		Held     []memberRow `json:"held"           jsonschema:"what the collection held, by id and name, to make it again with collection_create"`
		WatchedS int         `json:"watched_s"      jsonschema:"how many seconds the collection was watched after the delete, and stayed gone"`
		Note     string      `json:"note,omitempty" jsonschema:"why it was watched longer than a few seconds, and whether it came back"`
	}
	add(r, deleteTool, &mcp.Tool{
		Name: "collection_delete",
		Description: "Delete a collection, which cannot be undone: the items stay in the library and only the grouping goes, with its name, sort name, description and images. held in the answer lists what it held, by id and name, to make it again with collection_create, which gives it a new id. " +
			"On Emby, a collection deleted while a refresh of it was still queued (behind a library scan, say) leaves its name unusable for a new collection until Emby restarts. " +
			"It answers once the collection has stayed gone, deleting it again if it comes back: Jellyfin refreshes a collection it has made or whose members changed, and saves it back when that refresh ends - a second or so later, or a minute when the provider fails - so there a collection changed in the last 75 seconds is watched a few seconds when the provider that refresh asks answers at once, and until 75 seconds after the change when it is slow or failing; anything else for a few seconds.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, deleteOut, error) {
		col, err := client.ResolveByType(ctx, "BoxSet", in.Collection)
		if err != nil {
			return nil, deleteOut{}, err
		}
		held, err := membersOf(ctx, client, col.ID)
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("could not read what %s holds, so it was not deleted: %w", col.Name, err)
		}

		// read back until it stays gone: Jellyfin saves a collection it is
		// still refreshing back after the delete answered
		seen, err := client.DeleteCollection(ctx, col.ID)
		if err != nil {
			return nil, deleteOut{}, fmt.Errorf("%w (%s held: %s)", err, col.Name, membersSaid(held))
		}
		out := deleteOut{Deleted: col.Name, ID: col.ID, Held: held, WatchedS: int(seen.Watched.Round(time.Second) / time.Second)}
		switch {
		case seen.Recent && seen.ProviderSlow:
			out.Note = fmt.Sprintf("the collection was changed %d s before the delete, and Jellyfin saves back a collection whose refresh from such a change ends after it is deleted; the provider that refresh asks was slow or failing, so a refresh could run a minute, and it was watched until that could no longer happen", int(seen.SavedAgo/time.Second))
		case seen.Recent:
			out.Note = fmt.Sprintf("the collection was changed %d s before the delete, and Jellyfin saves back a collection whose refresh from such a change ends after it is deleted; the provider that refresh asks answered at once, so a refresh would end in a moment, and it was watched a few times that", int(seen.SavedAgo/time.Second))
		}
		if seen.Back > 0 {
			out.Note = strings.TrimPrefix(fmt.Sprintf("%s; it came back %d time(s), saved by that refresh, and was deleted again", out.Note, seen.Back), "; ")
		}

		return nil, out, nil
	})
}

// editCollectionFields renames a collection, or sets its sort name or
// overview, whichever are given, sent again once if a refresh saved the
// collection over it: seen on Emby, a rename straight after collection_create
// was answered and lost to the refresh the create queued. name, updated and
// was say what changed, and what each field was.
func (r *registry) editCollectionFields(ctx context.Context, col *embyfin.Item, newName, sortName, overview string, name *string, updated *[]string, was map[string]string) error {
	client := r.client
	admin, err := client.ResolveUser(ctx, "")
	if err != nil {
		return err
	}
	edit := func(full map[string]any) (bool, error) {
		*updated = (*updated)[:0]
		if newName != "" {
			wasOnce(was, "Name", fieldText(full, "Name"))
			full["Name"], *name = newName, newName
			*updated = append(*updated, "Name")
		}
		if sortName != "" {
			wasOnce(was, "SortName", sortNameOf(full))
			setSortName(full, sortName, client.Backend() == embyfin.Emby)
			*updated = append(*updated, "SortName")
		}
		if overview != "" {
			wasOnce(was, "Overview", fieldText(full, "Overview"))
			full["Overview"] = overview
			*updated = append(*updated, "Overview")
		}
		return true, nil
	}
	for try := range 2 {
		if _, err := client.EditItem(ctx, admin.ID, col.ID, edit); err != nil {
			if try > 0 {
				return fmt.Errorf("the edit of %s was saved over and sent again, and the second failed: %w", col.Name, err)
			}
			return err
		}
		held, err := r.editHeld(ctx, admin.ID, col.ID, func(full map[string]any) bool {
			return (newName == "" || fieldText(full, "Name") == newName) &&
				(sortName == "" || sortNameOf(full) == sortName) &&
				(overview == "" || fieldText(full, "Overview") == overview)
		})
		if err != nil {
			return fmt.Errorf("edited %s (%s), but reading it back failed: %w", col.Name, strings.Join(*updated, ", "), err)
		}
		if held {
			return nil
		}
	}

	return fmt.Errorf("the server saved %s over the edit twice (%s did not stay): a refresh of it is still running; try again in a minute", col.Name, strings.Join(*updated, ", "))
}

// membersScanSaid is what the tools that change a collection's members say
// of a library scan running meanwhile.
const membersScanSaid = "A library scan running at the time can save the collection as it read it once it finishes, putting back an item taken out or dropping one added (seen on Jellyfin 12.1): the answer says when one was running, and nothing is changed when that cannot be told."

// membersRace reads again whether a library scan is running, after a change
// to a collection's members, and says, with the ones running before the
// change, that one may undo it: a scan's refresh of a collection saves the
// members it read, and can put back an item taken out or drop one added
// after the change was seen to hold (seen on Jellyfin 12.1). "" when none
// was running; an error, to follow "but", when it cannot be told.
func membersRace(ctx context.Context, client *embyfin.Client, before []string) (string, error) {
	after, err := client.ScansRunning(ctx)
	if err != nil {
		return "", fmt.Errorf("could not tell whether a library scan was running, which can put back or drop members: %w", err)
	}
	running := slices.Compact(slices.Sorted(slices.Values(slices.Concat(before, after))))
	if len(running) == 0 {
		return "", nil
	}

	return strings.Join(running, " and ") + " was running: it may put back or drop members once it finishes; check the collection afterwards", nil
}
