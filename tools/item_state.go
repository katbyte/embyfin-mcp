package tools

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// What item_set_state reaches, and what it was.
//
// A watched mark on a series, a season or a collection goes on every item
// under it (seen on Emby 4.10 and Jellyfin 12.1), and marking one unwatched
// clears its play count and last played date for good, so the tool reads
// each item it reaches before the change and says what it was. What is under
// a folder is read off the servers' lists in the user's view: straight after
// a series is marked, Jellyfin's single-item read still answers each
// episode's old state from what it keeps in memory, while its lists answer
// the new one (seen on 12.1). Emby's lists show the copies of a title as one
// row, so there an item's own state and its copies' are read one by one.

// stateCap is the most items a watched mark on a folder may reach: the
// answer lists what every one was before, and must stay an answer a caller
// can read.
const stateCap = 1000

// stateCapSaid is stateCap for a sentence.
var stateCapSaid = strconv.Itoa(stateCap)

// countUnder counts the items under a folder that are not folders: the rows
// a user's view shows, and the items the server stores, read with no user.
// Emby's view shows the copies of a film in one library as one row (seen on
// 4.11: Messy Movies stores 13 films and shows 11 rows, and a mark changed
// all 13), and a user limited by a rating or a tag does not see everything,
// while a mark in their name can still reach it (see unshownUnder).
func countUnder(ctx context.Context, client *embyfin.Client, userID, folderID string) (rows, stored int, err error) {
	if _, rows, err = client.Search(ctx, embyfin.SearchOptions{ParentID: folderID, Filters: "IsNotFolder", UserID: userID, Fields: embyfin.FieldsLean, Limit: 1}); err != nil {
		return 0, 0, err
	}
	_, stored, err = client.Search(ctx, embyfin.SearchOptions{ParentID: folderID, Filters: "IsNotFolder", Fields: embyfin.FieldsLean, Limit: 1})

	return rows, stored, err
}

// elsewhereShows is the most shows under a folder whose copies elsewhere
// copiesElsewhere looks for: a read or two for each.
const elsewhereShows = 20

// copiesRead is the most copies elsewhere whose state a folder's mark reads
// before and after: one read each.
const copiesRead = 200

// unshownUnder is the items stored under a folder that the rows a user's
// view shows leave out: on Emby a copy folded into another copy's row, or an
// item hidden from the user by a rating, a tag or a folder. A mark in the
// user's name reaches the folded copies with their row, and the hidden items
// under some folders and not others (seen on Emby 4.10 and 4.11: under a
// library, a series, a season and a playlist, not a collection); Emby
// answers a hidden item's state in the user's name, so each is read before
// and after. Jellyfin answers it with a 404, so there it is said instead.
func unshownUnder(ctx context.Context, client *embyfin.Client, folderID string, rows []stateRow) ([]string, error) {
	shown := map[string]bool{}
	for _, r := range rows {
		shown[r.ID] = true
	}
	var out []string
	// what the mark is checked against: a read that cannot be sure of it
	// fails
	read, err := client.ReadAll(ctx, embyfin.SearchOptions{ParentID: folderID, Filters: "IsNotFolder", Fields: embyfin.FieldsLean}, embyfin.ToAct, func(page []embyfin.Item) bool {
		for i := range page {
			if !shown[page[i].ID] {
				out = append(out, page[i].ID)
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if changed := read.Changed(); changed != "" {
		return nil, fmt.Errorf("can't be sure what is stored under %s: %s", folderID, changed)
	}

	return out, nil
}

// idsOfRows is rows' ids.
func idsOfRows(rows []memberRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}

	return out
}

// copiesElsewhere finds, on Emby, the items outside a folder that carry the
// ids of items under it, which Emby keeps one watch state for: films by
// their TMDB and IMDb ids, in one read for them all (ItemsByAnyProviderID),
// and episodes by their show's ids and their number, for up to
// elsewhereShows shows. complete is false when something under it was not
// looked for: songs, or the episodes of more shows than that.
func copiesElsewhere(ctx context.Context, client *embyfin.Client, folderID string) ([]memberRow, bool, error) {
	var stored []embyfin.Item
	read, err := client.ReadAll(ctx, embyfin.SearchOptions{ParentID: folderID, Filters: "IsNotFolder", Fields: "Path,ProviderIds"}, embyfin.ToAct, func(page []embyfin.Item) bool {
		stored = append(stored, page...)
		return true
	})
	if err != nil {
		return nil, false, err
	}
	if changed := read.Changed(); changed != "" {
		return nil, false, fmt.Errorf("can't be sure what is stored under %s, whose copies elsewhere the mark reaches: %s", folderID, changed)
	}
	under := map[string]bool{}
	for i := range stored {
		under[stored[i].ID] = true
	}
	complete := true
	var pairs []string
	shows := map[string][]*embyfin.Item{}
	for i := range stored {
		switch stored[i].Type {
		case typeMovie:
			for _, p := range []string{"tmdb", "imdb"} {
				if id := providerID(&stored[i], p); id != "" && !slices.Contains(pairs, p+"."+id) {
					pairs = append(pairs, p+"."+id)
				}
			}
		case typeEpisode:
			shows[stored[i].SeriesID] = append(shows[stored[i].SeriesID], &stored[i])
		default:
			complete = false
		}
	}
	var out []memberRow
	seen := map[string]bool{}
	keep := func(it *embyfin.Item) {
		if !under[it.ID] && !seen[it.ID] {
			seen[it.ID] = true
			out = append(out, memberRow{ID: it.ID, Name: it.Name})
		}
	}
	for start := 0; start < len(pairs); start += 40 {
		found, err := client.ItemsByAnyProviderID(ctx, pairs[start:min(start+40, len(pairs))])
		if err != nil {
			return nil, false, err
		}
		for i := range found {
			if found[i].Type == typeMovie {
				keep(&found[i])
			}
		}
	}
	if len(shows) > elsewhereShows {
		return out, false, nil
	}
	for seriesID, eps := range shows {
		copies, err := episodeCopiesOf(ctx, client, seriesID, eps)
		if err != nil {
			return nil, false, err
		}
		for i := range copies {
			keep(&copies[i])
		}
	}
	slices.SortFunc(out, func(a, b memberRow) int { return strings.Compare(a.Name+a.ID, b.Name+b.ID) })

	return out, complete, nil
}

// episodeCopiesOf is the episodes of the other shows carrying one of a
// show's ids that have the number of one of eps: the copies Emby keeps one
// state with.
func episodeCopiesOf(ctx context.Context, client *embyfin.Client, seriesID string, eps []*embyfin.Item) ([]embyfin.Item, error) {
	if seriesID == "" {
		return nil, nil
	}
	series, err := client.ItemByID(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	var pairs []string
	for _, p := range []string{"tvdb", "tmdb", "imdb"} {
		if id := providerID(series, p); id != "" {
			pairs = append(pairs, p+"."+id)
		}
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	others, err := client.ItemsByAnyProviderID(ctx, pairs)
	if err != nil {
		return nil, err
	}
	var out []embyfin.Item
	for i := range others {
		if others[i].Type != "Series" || others[i].ID == seriesID {
			continue
		}
		theirs, _, err := client.Search(ctx, embyfin.SearchOptions{ParentID: others[i].ID, IncludeItemTypes: typeEpisode, Fields: "Path"})
		if err != nil {
			return nil, err
		}
		for j := range theirs {
			if slices.ContainsFunc(eps, func(e *embyfin.Item) bool {
				return e.ParentIndexNumber == theirs[j].ParentIndexNumber && e.IndexNumber == theirs[j].IndexNumber
			}) {
				out = append(out, theirs[j])
			}
		}
	}

	return out, nil
}

// folderMarkRefused is the refusal of a watched mark on a folder holding more
// than stateCap items, nil for one that may be made. under is the count it
// names, and what a confirm for a mark that wide would name too.
func folderMarkRefused(it *embyfin.Item, under int, watched bool) error {
	if under <= stateCap {
		return nil
	}
	what := "marks every one of them watched, counting a play on each not yet watched"
	if !watched {
		what = "clears every one's play count, last played date and resume point for good"
	}

	return fmt.Errorf("%s holds %d items, more than the %d a watched mark on a folder may reach, so nothing was changed: the mark %s, and the answer lists what each was before, which past %d is more than one answer carries. Mark a series or a season at a time", it.Name, under, stateCap, what, stateCap)
}

// stateRow is a user's state on one item.
type stateRow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Played     bool   `json:"played"`
	PlayCount  int    `json:"play_count"`
	LastPlayed string `json:"last_played,omitempty"`
	PositionS  int    `json:"position_s,omitempty"`
	Favourite  bool   `json:"favourite"`
}

func stateRowOf(it *embyfin.Item) stateRow {
	row := stateRow{ID: it.ID, Name: it.Name}
	if ud := it.UserData; ud != nil {
		row.Played, row.PlayCount, row.LastPlayed, row.Favourite = ud.Played, ud.PlayCount, ud.LastPlayedDate, ud.IsFavourite
		row.PositionS = int(ud.PlaybackPositionTicks / ticksPerSecond)
	}

	return row
}

// stateReach is the items a state change reaches, as they were before it.
type stateReach struct {
	// items are the item itself and, for a watched mark on a folder, every
	// item under it
	items []stateRow
	// copies are, on Emby, a film's or an episode's other copies, which carry its
	// ids and so its state
	copies []stateRow
	// under says the items under a folder are among items: a watched mark
	// reaches them, a favourite does not
	under bool
}

// reachOf reads what a change to it reaches, in the user's view, with each
// item's state now: the items under a folder too when a watched mark is
// given.
func reachOf(ctx context.Context, client *embyfin.Client, userID string, it *embyfin.Item, watched bool) (stateReach, error) {
	reach := stateReach{under: watched && it.IsFolder}
	own, err := ownState(ctx, client, userID, it)
	if err != nil {
		return reach, err
	}
	reach.items = []stateRow{own}
	if reach.under {
		under, err := statesUnder(ctx, client, userID, it.ID)
		if err != nil {
			return reach, err
		}
		reach.items = append(reach.items, under...)
	}
	if client.Backend() == embyfin.Emby && (it.Type == typeMovie || it.Type == typeEpisode) {
		copies, err := filmCopies(ctx, client, it)
		if it.Type == typeEpisode {
			copies, err = episodeCopies(ctx, client, it)
		}
		if err != nil {
			return reach, err
		}
		if reach.copies, err = copyStates(ctx, client, userID, copies); err != nil {
			return reach, err
		}
	}

	return reach, nil
}

// stateAsked is what a state change asked for, each nil when not asked.
type stateAsked struct {
	watched, favourite *bool
	positionS          *int
}

// stateAfter is how a change read back: how many of the items it reached read
// differently, the copies that changed with them, as they read now, and what
// was asked for that does not read as asked.
type stateAfter struct {
	changed int
	copies  []stateRow
	unkept  []string
}

// readAgain reads the reached items again after a change, a few times until
// everything asked for reads as asked - the item's own favourite, resume
// point and watched mark, and for a watched mark on a folder every item under
// it (the server marks a folder's items one by one) - and compares. What
// still does not read as asked after that is unkept, each field said.
func (reach stateReach) readAgain(ctx context.Context, r *registry, client *embyfin.Client, userID string, it *embyfin.Item, asked stateAsked) (stateAfter, error) {
	var after stateAfter
	for range 12 {
		now, err := statesAgain(ctx, client, userID, it, reach.under)
		if err != nil {
			return after, err
		}
		after.changed = 0
		for _, row := range reach.items {
			if n, ok := now[row.ID]; !ok || n != row {
				after.changed++
			}
		}
		if after.unkept = unkept(reach, now, it, asked); len(after.unkept) == 0 {
			break
		}
		if err := r.pause(ctx); err != nil {
			return after, err
		}
	}
	if len(reach.copies) > 0 {
		copyIDs := make([]string, 0, len(reach.copies))
		for _, row := range reach.copies {
			copyIDs = append(copyIDs, row.ID)
		}
		now, err := copyStates(ctx, client, userID, copyIDs)
		if err != nil {
			return after, err
		}
		for _, n := range now {
			if i := slices.IndexFunc(reach.copies, func(c stateRow) bool { return c.ID == n.ID }); i >= 0 && reach.copies[i] != n {
				after.copies = append(after.copies, n)
			}
		}
	}

	return after, nil
}

// unkept says what a change asked for does not read as asked in now: the
// item's own favourite and resume point, its watched mark when it is not a
// folder (a folder's own is the server's sum of its items', and Emby leaves
// a collection's unmarked), and each item under a folder a watched mark
// reached.
func unkept(reach stateReach, now map[string]stateRow, it *embyfin.Item, asked stateAsked) []string {
	own, read := now[it.ID]
	if !read {
		return []string{"the item itself could not be read back"}
	}
	var out []string
	if f := asked.favourite; f != nil && own.Favourite != *f {
		out = append(out, fmt.Sprintf("favourite reads %v, not %v", own.Favourite, *f))
	}
	if p := asked.positionS; p != nil && (own.PositionS != *p || own.Played) {
		out = append(out, fmt.Sprintf("the resume point reads %d s (watched %v), not %d s", own.PositionS, own.Played, *p))
	}
	if w := asked.watched; w != nil && !it.IsFolder && asked.positionS == nil && own.Played != *w {
		out = append(out, fmt.Sprintf("watched reads %v, not %v", own.Played, *w))
	}
	if w := asked.watched; w != nil && reach.under {
		var off []stateRow
		for _, row := range reach.items[1:] {
			if n, ok := now[row.ID]; !ok || n.Played != *w {
				off = append(off, n)
			}
		}
		if len(off) > 0 {
			out = append(out, fmt.Sprintf("%d of the %d items under it do not read watched %v: %s", len(off), len(reach.items)-1, *w, statesSaid(off)))
		}
	}

	return out
}

// statesAgain reads the reached items again: the item itself, and with
// under the ones under a folder, all in the user's view.
func statesAgain(ctx context.Context, client *embyfin.Client, userID string, it *embyfin.Item, under bool) (map[string]stateRow, error) {
	out := map[string]stateRow{}
	own, err := ownState(ctx, client, userID, it)
	if err != nil {
		return nil, err
	}
	out[own.ID] = own
	if under {
		under, err := statesUnder(ctx, client, userID, it.ID)
		if err != nil {
			return nil, err
		}
		for _, row := range under {
			out[row.ID] = row
		}
	}

	return out, nil
}

// ownState reads a user's state on the item itself. On Emby that is the
// single-item read: its lists in a user's view show copies sharing a key as
// one row, with one copy's state (seen on 4.10). On Jellyfin it is the list,
// whose single-item read can answer from memory, and the single-item read
// when the list leaves the item out.
func ownState(ctx context.Context, client *embyfin.Client, userID string, it *embyfin.Item) (stateRow, error) {
	if client.Backend() == embyfin.Jellyfin || it.IsFolder {
		rows, err := statesOf(ctx, client, userID, it.Type, []string{it.ID})
		if err != nil {
			return stateRow{}, err
		}
		if len(rows) == 1 {
			return rows[0], nil
		}
	}
	one, err := client.UserItem(ctx, userID, it.ID)
	if err != nil {
		return stateRow{}, err
	}

	return stateRowOf(one), nil
}

// statesOf reads a user's state on items by id, in the order given, a batch
// at a time. An id the user's view does not list is left out.
func statesOf(ctx context.Context, client *embyfin.Client, userID, itemType string, ids []string) ([]stateRow, error) {
	byID := map[string]stateRow{}
	for start := 0; start < len(ids); start += orphanBatch {
		batch := ids[start:min(start+orphanBatch, len(ids))]
		// Emby's list in a user's view leaves out an album or an artist
		// unless its kind is asked for
		items, _, err := client.Search(ctx, embyfin.SearchOptions{IDs: strings.Join(batch, ","), IncludeItemTypes: itemType, UserID: userID, EnableUserData: true, Fields: "Path", Limit: len(batch)})
		if err != nil {
			return nil, err
		}
		for i := range items {
			// only the ids asked for: Emby answers an Ids filter it cannot
			// parse with the whole library
			if slices.Contains(batch, items[i].ID) {
				byID[items[i].ID] = stateRowOf(&items[i])
			}
		}
	}
	out := make([]stateRow, 0, len(ids))
	for _, id := range ids {
		if row, ok := byID[id]; ok {
			out = append(out, row)
		}
	}

	return out, nil
}

// copyStates reads a user's state on a film's copies one by one: Emby's lists
// in a user's view show the copies sharing a film's key as one row, which
// carried one copy's state and left the others out (seen on 4.10), and its
// single-item read answers for each.
func copyStates(ctx context.Context, client *embyfin.Client, userID string, ids []string) ([]stateRow, error) {
	out := make([]stateRow, 0, len(ids))
	for _, id := range ids {
		it, err := client.UserItem(ctx, userID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, stateRowOf(it))
	}

	return out, nil
}

// statesUnder reads a user's state on every item under a folder, in the
// user's view: a series' episodes, a collection's films.
func statesUnder(ctx context.Context, client *embyfin.Client, userID, folderID string) ([]stateRow, error) {
	var out []stateRow
	// the states a mark is checked by: a read that cannot be sure of them
	// fails
	read, err := client.ReadAll(ctx, embyfin.SearchOptions{ParentID: folderID, Filters: "IsNotFolder", UserID: userID, EnableUserData: true, Fields: "Path", SortBy: "SortName"}, embyfin.ToAct, func(items []embyfin.Item) bool {
		for i := range items {
			out = append(out, stateRowOf(&items[i]))
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if changed := read.Changed(); changed != "" {
		return nil, fmt.Errorf("can't be sure of the states under %s: %s", folderID, changed)
	}

	return out, nil
}

// filmCopies are the other films carrying one of a film's TMDB or IMDb ids,
// which Emby keeps one watch state for.
func filmCopies(ctx context.Context, client *embyfin.Client, it *embyfin.Item) ([]string, error) {
	var out []string
	for _, provider := range []string{"tmdb", "imdb"} {
		id := providerID(it, provider)
		if id == "" {
			continue
		}
		found, changed, err := client.ItemsByProviderID(ctx, provider, id, typeMovie)
		if err != nil {
			return nil, err
		}
		if changed != "" {
			return nil, fmt.Errorf("can't be sure of the copies of %s carrying its %s id: %s", it.Name, provider, changed)
		}
		for i := range found {
			if found[i].ID != it.ID && found[i].Type == typeMovie && !slices.Contains(out, found[i].ID) {
				out = append(out, found[i].ID)
			}
		}
	}

	return out, nil
}

// episodeCopies are the other episodes of the same number in a series carrying
// one of this episode's series' ids - the show held in two folders - which
// Emby keeps one watch state for, by the series' id and the episode's number
// (seen on 4.10: marking one copy of an episode held in two folders of a show
// marked the other).
func episodeCopies(ctx context.Context, client *embyfin.Client, it *embyfin.Item) ([]string, error) {
	// an episode the server holds no season or number for shares no state
	// by its number
	if it.SeriesID == "" || !numbered(it) {
		return nil, nil
	}
	series, err := client.ItemByID(ctx, it.SeriesID)
	if err != nil {
		return nil, err
	}
	shows := []string{series.ID}
	for _, provider := range []string{"tvdb", "tmdb", "imdb"} {
		id := providerID(series, provider)
		if id == "" {
			continue
		}
		found, changed, err := client.ItemsByProviderID(ctx, provider, id, "Series")
		if err != nil {
			return nil, err
		}
		if changed != "" {
			return nil, fmt.Errorf("can't be sure of the shows carrying %s's %s id: %s", series.Name, provider, changed)
		}
		for i := range found {
			if found[i].Type == "Series" && !slices.Contains(shows, found[i].ID) {
				shows = append(shows, found[i].ID)
			}
		}
	}
	var out []string
	for _, show := range shows {
		season := it.ParentIndexNumber
		eps, _, err := client.Search(ctx, embyfin.SearchOptions{ParentID: show, IncludeItemTypes: typeEpisode, ParentIndexNumber: season, Fields: "Path"})
		if err != nil {
			return nil, err
		}
		for i := range eps {
			if eps[i].ID != it.ID && numbered(&eps[i]) && *eps[i].IndexNumber == *it.IndexNumber && *eps[i].ParentIndexNumber == *season && !slices.Contains(out, eps[i].ID) {
				out = append(out, eps[i].ID)
			}
		}
	}

	return out, nil
}

// statesSaid names states for an error, the first few.
func statesSaid(rows []stateRow) string {
	parts := make([]string, 0, min(len(rows), 10))
	for _, row := range rows[:min(len(rows), 10)] {
		s := fmt.Sprintf("%s (%s): played %v, %d plays", row.Name, row.ID, row.Played, row.PlayCount)
		if row.LastPlayed != "" {
			s += ", last " + row.LastPlayed
		}
		if row.PositionS > 0 {
			s += fmt.Sprintf(", at %d s", row.PositionS)
		}
		s += fmt.Sprintf(", favourite %v", row.Favourite)
		parts = append(parts, s)
	}
	if len(rows) > len(parts) {
		parts = append(parts, fmt.Sprintf("and %d more", len(rows)-len(parts)))
	}

	return strings.Join(parts, "; ")
}
