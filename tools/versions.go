package tools

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// Versions, as the servers show them to people.
//
// Jellyfin merges the files of one film in one folder into one item with
// several versions when it scans, and every read of the item answers with all
// of them. Emby 4.10 merges too - files named as versions of one film in one
// folder, the same for an episode, and copies in other folders of one
// library sharing a provider id - but only in a user's view: a sweep of
// /Items holds each file as an item of its own with one version, a list in a
// user's view hides all but one of the merged items without naming the
// others as its versions, and only the single item read in a user's view
// lists every version. So on Emby what people are shown is read in three
// steps: the sweep; the same sweep in an administrator's view, where the
// items not listed are versions of ones that are; and a single read of each
// item not listed, whose versions name the item it was merged into.

// shownItem is one item as the server shows it to people, with the items it
// is stored as.
type shownItem struct {
	// the item people are shown, carrying every version it holds
	embyfin.Item
	// what the server stores it as: on Jellyfin the item itself, on Emby
	// the listed item and each item merged into it, every one with its own
	// file, dates and facts
	stored []embyfin.Item
}

// shownItems sweeps opts and answers with the items as the server shows them
// to people, each with every version it holds: on Jellyfin the sweep itself,
// on Emby the items an administrator's view lists, each carrying the versions
// of the items merged into it.
func shownItems(ctx context.Context, client *embyfin.Client, opts embyfin.SearchOptions) ([]embyfin.Item, error) {
	groups, err := shownGroups(ctx, client, opts)
	if err != nil {
		return nil, err
	}
	out := make([]embyfin.Item, 0, len(groups))
	for i := range groups {
		out = append(out, groups[i].Item)
	}

	return out, nil
}

// shownGroups is shownItems keeping, beside each item shown, the items it is
// stored as: what an audit judging an item by all its files, but listing
// facts file by file, reads.
func shownGroups(ctx context.Context, client *embyfin.Client, opts embyfin.SearchOptions) ([]shownItem, error) {
	var all []embyfin.Item
	if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
		all = append(all, items...)

		return true
	}); err != nil {
		return nil, err
	}
	if client.Backend() != embyfin.Emby || len(all) == 0 {
		out := make([]shownItem, 0, len(all))
		for i := range all {
			out = append(out, shownItem{Item: all[i], stored: all[i : i+1]})
		}

		return out, nil
	}

	admin, err := client.ResolveUser(ctx, "")
	if err != nil {
		return nil, err
	}
	listed := map[string]bool{}
	view := opts
	view.UserID, view.Fields = admin.ID, "Path"
	if err := client.SearchAll(ctx, view, func(items []embyfin.Item) bool {
		for i := range items {
			listed[items[i].ID] = true
		}

		return true
	}); err != nil {
		return nil, err
	}

	// the listed items by id and by file, which are how a merged item's
	// versions name it
	byID, byPath := map[string]int{}, map[string]int{}
	var shown []shownItem
	var hidden []embyfin.Item
	for i := range all {
		if listed[all[i].ID] {
			byID[all[i].ID], byPath[all[i].Path] = len(shown), len(shown)
			shown = append(shown, shownItem{Item: all[i], stored: []embyfin.Item{all[i]}})

			continue
		}
		hidden = append(hidden, all[i])
	}
	for i := range hidden {
		it, err := client.UserItem(ctx, admin.ID, hidden[i].ID)
		if err != nil {
			return nil, err
		}
		owner := -1
		for _, src := range it.MediaSources {
			j, ok := byID[src.ItemID]
			if !ok {
				j, ok = byPath[src.Path]
			}
			if ok {
				owner = j

				break
			}
		}
		if owner < 0 {
			// merged into nothing this sweep reaches (or not merged at all,
			// only left out of the view): it stands on its own
			shown = append(shown, shownItem{Item: hidden[i], stored: []embyfin.Item{hidden[i]}})

			continue
		}
		// every version, as the single read lists them: the listed item's
		// own file among them
		shown[owner].MediaSources = it.MediaSources
		shown[owner].stored = append(shown[owner].stored, hidden[i])
	}

	return shown, nil
}

// versionRow is one file an item is shown in.
type versionRow struct {
	ID       string `json:"id,omitempty"        jsonschema:"the item this version is held as: on Emby every version is an item of its own, and this is the id to identify, edit or delete that file by"`
	Label    string `json:"label,omitempty"     jsonschema:"the server's name for the version"`
	Path     string `json:"path"`
	RuntimeS int    `json:"runtime_s,omitempty" jsonschema:"this file's own runtime in seconds"`
	qualityFacts
}

// versionsOf is every file the server shows an item in. Jellyfin answers
// them on every read of the item; Emby only on the single read in a user's
// view, so there the first administrator's is asked.
func versionsOf(ctx context.Context, client *embyfin.Client, it *embyfin.Item) ([]embyfin.MediaSource, error) {
	if client.Backend() != embyfin.Emby || !it.HasFile() || it.IsFolder {
		return it.MediaSources, nil
	}
	admin, err := client.ResolveUser(ctx, "")
	if err != nil {
		return nil, err
	}
	shown, err := client.UserItem(ctx, admin.ID, it.ID)
	if err != nil {
		return nil, err
	}
	if len(shown.MediaSources) == 0 {
		return it.MediaSources, nil
	}

	return shown.MediaSources, nil
}

// Versions that are not the same film.
//
// A server makes files one item's versions by what it holds for them, not by
// what they are: Emby joins copies in other folders that carry the same
// provider id, so a file matched to another film's ids is shown as a version
// of that film, and every read of the item answers with its name. A caller
// comparing the versions then keeps the better copy and deletes a film. The
// path is the one thing the server did not write, so a version whose file or
// folder names another title, or a year more than one off, is probably
// another film. Runtimes far apart say the same more weakly - a director's cut
// runs longer too - so they strengthen a name that disagrees and never stand
// alone.

// heldTitles are the titles an item goes by (knownTitles), bare.
func heldTitles(it *embyfin.Item) []string {
	known := knownTitles(it)
	out := make([]string, 0, len(known))
	for _, k := range known {
		out = append(out, k.title)
	}

	return out
}

// pathClaim is what one segment of a path says the film is.
type pathClaim struct {
	segment string  // the file's or folder's name
	title   string  // the title it names
	year    int     // the year it names
	score   float64 // how close the title is to the nearest of the item's names
}

// segmentYear is the year a file or folder name gives, 0 for none: a
// (bracketed) one, else the last bare one.
func segmentYear(name string) int {
	if m := bracketedYear.FindStringSubmatch(name); m != nil {
		y, _ := strconv.Atoi(m[1])

		return y
	}
	if years := bareYear.FindAllStringSubmatch(name, -1); len(years) > 0 {
		y, _ := strconv.Atoi(years[len(years)-1][2])

		return y
	}

	return 0
}

// claimOf reads what a film's path claims it is: the file's own name when it
// gives a year - "Title (Year)", or a scene name's bare year - else the folder
// holding it when that is named "Title (Year)". A disc's stream names nothing,
// so for one it is the folder named for the disc. A name with no year
// ("movie.mkv", a collection's folder of several) claims too little to hold
// against a film, and false comes back.
func claimOf(path string, held []string) (pathClaim, bool) {
	file := path
	for {
		base := fileExtension.ReplaceAllString(baseName(file), "")
		if !discFile.MatchString(base) && !isDiscFolder(base) || parentDir(file) == "" {
			break
		}
		file = parentDir(file)
	}
	p := file
	year := segmentYear(fileExtension.ReplaceAllString(baseName(p), ""))
	if year == 0 {
		p = parentDir(file)
		if m := bracketedYear.FindStringSubmatch(baseName(p)); p != "" && m != nil {
			year, _ = strconv.Atoi(m[1])
		}
	}
	if year == 0 {
		return pathClaim{}, false
	}
	claim := pathClaim{segment: baseName(p), year: year, score: -1}
	for _, name := range held {
		if title, score := titleFromPath(p, name); score > claim.score {
			claim.title, claim.score = title, score
		}
	}

	return claim, true
}

// otherFilm says whether the file at path names a different film than the
// item it is held under: a title unlike every name the item goes by, or a
// year more than one off its own (a film released either side of new year is
// dated both ways). why says what the path names, "" when nothing disagrees.
func otherFilm(it *embyfin.Item, path string) (why string, other bool) {
	if path == "" || (it.Type != "" && it.Type != typeMovie) {
		return "", false
	}
	c, ok := claimOf(path, heldTitles(it))
	if !ok {
		return "", false
	}
	titleOff := c.title != "" && c.score < seriesConfident
	yearOff := it.ProductionYear > 0 && abs(c.year-it.ProductionYear) > 1
	if !titleOff && !yearOff {
		return "", false
	}
	held := it.Name
	if it.ProductionYear > 0 {
		held = fmt.Sprintf("%s (%d)", it.Name, it.ProductionYear)
	}

	return fmt.Sprintf("%q is named for %q (%d), not %s", c.segment, cmp.Or(c.title, c.segment), c.year, held), true
}

// runtimesApart says whether two files run far enough apart to be two films
// rather than two encodes of one: more than a tenth of the longer and more
// than a minute. A director's cut does too, which is why it is only a tell.
func runtimesApart(a, b int64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	longer, gap := max(a, b), a-b
	if gap < 0 {
		gap = -gap
	}

	return gap*10 > longer && gap > 60*ticksPerSecond
}

// versionWarning is what to say about an item's versions when one of them is
// probably another film, "" when none is: which file names what, and how
// far the runtimes are apart when they are. A single version is judged the
// same way, because Jellyfin keeps a film matched to another's ids as a
// separate item rather than merging it.
func versionWarning(it *embyfin.Item) string {
	var odd []string
	for i := range it.MediaSources {
		if why, other := otherFilm(it, it.MediaSources[i].Path); other {
			odd = append(odd, why)
		}
	}
	if len(odd) == 0 && len(it.MediaSources) == 0 {
		if why, other := otherFilm(it, it.Path); other {
			odd = append(odd, why)
		}
	}
	if len(odd) == 0 {
		return ""
	}

	apart := ""
	for i := range it.MediaSources {
		for j := i + 1; j < len(it.MediaSources); j++ {
			a, b := it.MediaSources[i].RunTimeTicks, it.MediaSources[j].RunTimeTicks
			if apart == "" && runtimesApart(a, b) {
				apart = fmt.Sprintf(", and its versions run %s and %s", runtimeText(a), runtimeText(b))
			}
		}
	}
	if len(it.MediaSources) > 1 {
		return fmt.Sprintf("probably not one film: %s%s. A file matched to this film's ids is shown as a version of it; compare the files before keeping one over another, and identify the wrong one (item_identify on the version's own id) to split them", strings.Join(odd, "; "), apart)
	}

	return fmt.Sprintf("may be a different film matched to this one's ids: %s. Compare the file before treating it as a copy of this film", strings.Join(odd, "; "))
}

// runtimeText is a runtime in ticks as a person reads it.
func runtimeText(ticks int64) string {
	seconds := ticks / ticksPerSecond
	if seconds < 120 {
		return fmt.Sprintf("%ds", seconds)
	}

	return fmt.Sprintf("%d min", seconds/60)
}
