package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerIdentifyTools(r *registry) {
	client := r.client
	type identifyIn struct {
		ID   string `json:"id"             jsonschema:"the library item id to identify"`
		Kind string `json:"kind"           jsonschema:"movie or series"`
		Name string `json:"name,omitempty" jsonschema:"override the search title; defaults to the item's current name"`
		Year int    `json:"year,omitempty" jsonschema:"override the search year"`
	}
	type candidate struct {
		Index               int               `json:"index"                           jsonschema:"pass to item_identify_apply as candidate"`
		Name                string            `json:"name"`
		Year                int               `json:"year,omitempty"`
		MetadataProviderIDs map[string]string `json:"metadata_provider_ids,omitempty" jsonschema:"keyed tmdb, imdb, tvdb; pass to item_identify_apply as candidate_ids"`
		Provider            string            `json:"search_provider,omitempty"`
		Overview            string            `json:"overview,omitempty"`
	}
	type identifyOut struct {
		Item       string      `json:"item"`
		Candidates []candidate `json:"candidates" jsonschema:"suggestions only; nothing is changed until item_identify_apply"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "item_identify",
		Description: "Ask the metadata providers for candidate matches for an item (suggestions only — verify year and runtime before applying). item_identify_apply applies one, given its index and its metadata_provider_ids as candidate_ids; a candidate the providers give no ids for cannot be applied.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in identifyIn) (*mcp.CallToolResult, identifyOut, error) {
		it, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, identifyOut{}, err
		}

		results, err := client.RemoteSearch(ctx, in.Kind, in.ID, in.Name, in.Year)
		if err != nil {
			return nil, identifyOut{}, err
		}

		out := identifyOut{Item: it.Name, Candidates: []candidate{}}
		for i, r := range results {
			overview := r.Overview
			if len(overview) > 200 {
				overview = overview[:200] + "..."
			}
			out.Candidates = append(out.Candidates, candidate{
				Index:               i,
				Name:                r.Name,
				Year:                r.ProductionYear,
				MetadataProviderIDs: providerKeys(r.ProviderIDs),
				Provider:            r.SearchProviderName,
				Overview:            overview,
			})
		}

		return nil, out, nil
	})

	type applyIn struct {
		ID               string            `json:"id"                           jsonschema:"the library item id"`
		Kind             string            `json:"kind"                         jsonschema:"movie for a film, series for a show: the item's own kind"`
		Candidate        int               `json:"candidate"                    jsonschema:"index from item_identify's candidates"`
		CandidateIDs     map[string]string `json:"candidate_ids,omitempty"      jsonschema:"the candidate's metadata_provider_ids exactly as item_identify gave them: the search is asked again, and the candidate carrying these ids is the one applied. Required: a candidate without ids cannot be applied"`
		Name             string            `json:"name,omitempty"               jsonschema:"the name override used in item_identify, so the search asked again is the same"`
		Year             int               `json:"year,omitempty"               jsonschema:"the year override used in item_identify"`
		ReplaceAllImages bool              `json:"replace_all_images,omitempty"`
	}
	type applyOut struct {
		Applied             string            `json:"applied"                         jsonschema:"the candidate applied"`
		Name                string            `json:"name"                            jsonschema:"the item as it is now"`
		Year                int               `json:"year,omitempty"`
		Overview            string            `json:"overview,omitempty"`
		MetadataProviderIDs map[string]string `json:"metadata_provider_ids,omitempty" jsonschema:"the item's ids now, keyed tmdb, imdb, tvdb"`
		Was                 identity          `json:"was"                             jsonschema:"the item's name, year and ids before, to identify it back by"`
		WasIDs              map[string]string `json:"was_all_ids,omitempty"           jsonschema:"every provider id the item held before, as the server keeps them, which a plain edit of the ids can put back"`
		EpisodesRefreshed   int               `json:"episodes_refreshed,omitempty"    jsonschema:"for a series, how many episodes the server holds under it, each refreshed after the series"`
		besideMedia
		Note string `json:"note,omitempty" jsonschema:"what the change could not do, and what the next refresh may undo"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "item_identify_apply",
		Description: "Apply a candidate from item_identify: pass its index and its metadata_provider_ids as candidate_ids. The search is asked again and the candidate carrying those ids is applied, wherever the provider now lists it; none carrying them is a refusal, as is a kind that is not the item's (movie for a film, series for a show). A candidate the providers give no ids for cannot be applied: the server applies a match by its ids, and nothing could say it landed. " +
			"It rewrites the item's identity and re-fetches all its metadata and images: every field is replaced, hand edits included, and on Emby the poster.jpg beside the media is deleted in favour of the provider's image, which no tool puts back; a series has every season and episode refreshed after it. " + besideMediaSaid + ". On Emby a film given an id another film holds becomes that film's version. " +
			"It then reads the item back, once the refresh has saved it, and reports it as it now is, with was - its name, year and ids before - to identify it back by. An identity the server did not take is an error (Emby keeps the one an nfo beside the file names); a match not seen to land within the wait is an error saying it was sent and may still apply. " +
			"In a library with its metadata fetchers off the ids are set by a plain edit of the item, which changes them and nothing else (the server's apply would refresh the item with nothing to fetch): the item's whole set of ids is replaced by every id the title goes by, the ones the candidate carries and the others the server's providers give for it (a TMDB candidate's IMDb id), which drops any other id it held (a collection's, AniDB's) - was_all_ids keeps them; check the year and overview reported, and set them with item_edit. " +
			"note says what the next refresh may undo: an nfo beside the file that may still name the old title (a refresh reads it again; correct or remove it), and on Emby, which keeps watch state by provider id, that the item now shows each user's watched mark and favourite for the title it was matched to, the old ones coming back with the old ids. Jellyfin keeps watch state under an item's ids as well, and may show it moving too.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in applyIn) (*mcp.CallToolResult, applyOut, error) {
		current, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, applyOut{}, err
		}
		if want := identifyKinds[strings.ToLower(in.Kind)]; want != "" && want != current.Type {
			return nil, applyOut{}, fmt.Errorf("%s is a %s, and kind %s identifies a %s: nothing was changed", current.Name, current.Type, in.Kind, want)
		}
		results, err := client.RemoteSearch(ctx, in.Kind, in.ID, in.Name, in.Year)
		if err != nil {
			return nil, applyOut{}, err
		}
		if len(in.CandidateIDs) == 0 {
			return nil, applyOut{}, candidateIDsMissing(results, in.Candidate)
		}
		chosen, at, err := candidateCarrying(results, in.Candidate, in.CandidateIDs)
		if err != nil {
			return nil, applyOut{}, err
		}

		// in a library with its metadata fetchers off there is nothing to
		// fetch, and the server's apply refreshes the item as if there were:
		// Jellyfin's replaced every episode number of a series with nothing.
		// There the ids are set with a plain edit, which changes them and
		// nothing else.
		folder, err := libraryOf(ctx, client, current)
		if err != nil {
			return nil, applyOut{}, err
		}
		fetchersOff := folder != nil && folder.FetchersOff(current.Type)
		was := identityOf(current)
		// said on any failure after the change was sent, to put it back by
		wasSaid := fmt.Sprintf("before it, the item was %s", was)

		var notes []string
		if at != in.Candidate {
			notes = append(notes, fmt.Sprintf("the provider now lists the candidate at index %d, not %d: the one carrying the ids given was applied", at, in.Candidate))
		}
		// what is beside the media now: the refresh an apply starts deletes
		// the poster there on Emby
		dir, had, err := filesBeside(ctx, client, current)
		if err != nil {
			return nil, applyOut{}, fmt.Errorf("could not read the folder beside %s's media, to say what the match removes from it, so nothing was changed: %w", current.Name, err)
		}
		var it *embyfin.Item
		if fetchersOff {
			admin, aerr := client.ResolveUser(ctx, "")
			if aerr != nil {
				return nil, applyOut{}, aerr
			}
			// a TMDB candidate carries TMDB's id alone, and set alone it
			// left the item's old IMDb id in the nfo Jellyfin rewrites, for
			// a refresh to read back (seen: Memento holding a series' id).
			// Every id the providers know the title by replaces them; a
			// lookup that fails leaves the candidate's own, and says so
			ids := chosen.ProviderIDs
			full, lerr := client.RemoteIDs(ctx, in.Kind, chosen.Name, chosen.ProductionYear, ids)
			if lerr != nil {
				notes = append(notes, "the providers' other ids for the title could not be read ("+lerr.Error()+"), so only the candidate's own were set: an old id of another provider is gone rather than replaced")
			} else {
				ids = full
			}
			it, err = client.SetProviderIDs(ctx, admin.ID, in.ID, ids)
		} else {
			it, err = client.ApplyRemoteSearchResult(ctx, in.ID, chosen, in.ReplaceAllImages)
		}
		if err != nil {
			return nil, applyOut{}, fmt.Errorf("%w; %s", err, wasSaid)
		}

		overview := it.Overview
		if len(overview) > 200 {
			overview = overview[:200] + "..."
		}
		out := applyOut{
			Applied: fmt.Sprintf("%s (%d)", chosen.Name, chosen.ProductionYear),
			Name:    it.Name, Year: it.ProductionYear, Overview: overview,
			MetadataProviderIDs: providerKeys(it.ProviderIDs),
			Was:                 was, WasIDs: providerKeys(current.ProviderIDs),
		}
		if out.besideMedia, err = besideAfter(ctx, client, dir, had); err != nil {
			return nil, applyOut{}, fmt.Errorf("applied %s to %s, but reading the folder beside its media back failed, so what the match removed there is not known: %w; %s", out.Applied, it.Name, err, wasSaid)
		}
		if len(out.RemovedBeside) > 0 {
			notes = append(notes, "the server deleted "+strings.Join(out.RemovedBeside, ", ")+" from beside the media")
		}
		if nfo := nfoUnseen(folder); nfo != "" {
			notes = append(notes, nfo)
		}
		if current.Type == "Series" && !fetchersOff {
			_, n, err := client.Search(ctx, embyfin.SearchOptions{ParentID: in.ID, IncludeItemTypes: typeEpisode, Fields: embyfin.FieldsLean, Limit: 1})
			if err != nil {
				return nil, applyOut{}, fmt.Errorf("applied %s to %s, but counting the episodes it refreshes failed: %w; %s", out.Applied, it.Name, err, wasSaid)
			}
			out.EpisodesRefreshed = n
		}
		if fetchersOff {
			notes = append(notes, fmt.Sprintf("the %s library has its metadata fetchers off, so only the ids changed and nothing was fetched: set what is missing or wrong with item_edit", folder.Name))
		}
		// what the next refresh may undo, when the item is now another title
		if otherTitle(current, it) {
			if warning := nfoWarning(ctx, client, current, folder, fetchersOff); warning != "" {
				notes = append(notes, warning)
			}
			if client.Backend() == embyfin.Emby {
				notes = append(notes, "Emby keeps watch state and favourites by provider id: the item now shows each user's watched mark and favourite for the title it was matched to, not the ones it had, and those come back if its old ids do")
			}
		}
		out.Note = strings.Join(notes, ". ")

		return nil, out, nil
	})
}

// identifyKinds is the item type each identify kind is for.
var identifyKinds = map[string]string{"movie": typeMovie, "series": "Series", "show": "Series", "tv": "Series"}

// candidateIDsMissing is the refusal of an apply given no candidate_ids,
// saying what the candidate at the index given carries now: a candidate the
// providers give no ids for cannot be applied, since the server applies a
// match by its ids and nothing could say it landed; one with ids is named
// with them, to pass.
func candidateIDsMissing(results []embyfin.RemoteSearchResult, index int) error {
	if index < 0 || index >= len(results) {
		return fmt.Errorf("candidate_ids is required, and the search now returns %d results, none at index %d: nothing was changed. Run item_identify again and pass a candidate's index and its metadata_provider_ids", len(results), index)
	}
	r := &results[index]
	if ids := providerKeys(r.ProviderIDs); len(ids) > 0 {
		return fmt.Errorf("candidate_ids is required: at index %d the search now offers %s (%d) with %v - pass the metadata_provider_ids item_identify gave for the candidate chosen, so the one applied is that one even if the provider lists its candidates in another order now. Nothing was changed", index, r.Name, r.ProductionYear, ids)
	}

	return fmt.Errorf("the candidate at index %d, %s (%d), carries no metadata provider ids, so it cannot be applied: the server applies a match by its ids, and without them nothing could say it landed. Nothing was changed: search again with item_identify under another name or year, or set the item's fields with item_edit", index, r.Name, r.ProductionYear)
}

// candidateCarrying picks the candidate to apply from a search asked again:
// the one at the index given when it still carries the ids given, or else
// the one that does wherever the provider now lists it. A provider can list
// its candidates in another order from one search to the next, and the index
// alone applied another title. None carrying them is an error, and nothing
// is applied.
func candidateCarrying(results []embyfin.RemoteSearchResult, index int, ids map[string]string) (embyfin.RemoteSearchResult, int, error) {
	carries := func(r *embyfin.RemoteSearchResult) bool {
		have := providerKeys(r.ProviderIDs)
		for k, v := range ids {
			if have[strings.ToLower(k)] != v {
				return false
			}
		}
		return true
	}
	if index >= 0 && index < len(results) && carries(&results[index]) {
		return results[index], index, nil
	}
	for i := range results {
		if carries(&results[i]) {
			return results[i], i, nil
		}
	}
	there := fmt.Sprintf("the search now returns %d results", len(results))
	if index >= 0 && index < len(results) {
		there = fmt.Sprintf("at index %d it now offers %s (%d) with %v", index, results[index].Name, results[index].ProductionYear, providerKeys(results[index].ProviderIDs))
	}

	return embyfin.RemoteSearchResult{}, 0, fmt.Errorf("no candidate the search offers now carries %v (%s): nothing was changed. Run item_identify again and choose from what it offers now", ids, there)
}

// otherTitle says whether an item's ids now name another title than they
// did, by the first of tmdb, imdb and tvdb it held before and holds now: that
// one changed. The ids a match adds or corrects beside it are the same
// title's (Princess Mononoke's nfo gave it a TVDB id TMDB's record does not,
// and its match put TMDB's in its place). With no id held both before and
// after there is nothing to tell by, and it is taken as another title, which
// is the answer that warns.
func otherTitle(before, after *embyfin.Item) bool {
	for _, p := range []string{"tmdb", "imdb", "tvdb"} {
		if was, now := providerID(before, p), providerID(after, p); was != "" && now != "" {
			return was != now
		}
	}

	return true
}

// nfoWarning is what an nfo beside an item's file means for the identity just
// set, "" when there is none there: each server reads it again at a refresh.
// A library that saves no nfo leaves it naming the old title, and the refresh
// puts that back (seen on Emby: the messy Dune went back to Lynch's film,
// and the clean one to the 2021 film with its fetchers on). One that saves
// them writes the new ids in but keeps the ones it has no new value for, and
// so Jellyfin kept the old IMDb id there beside the new one. Jellyfin's own
// apply, with the fetchers on, keeps the match through a refresh.
func nfoWarning(ctx context.Context, client *embyfin.Client, it *embyfin.Item, folder *embyfin.VirtualFolder, fetchersOff bool) string {
	if !fetchersOff && client.Backend() != embyfin.Emby {
		return ""
	}
	name, unread := nfoBeside(ctx, client, it)
	if unread == "" && name == "" {
		return ""
	}
	// named when the folder could be read, and said to be there only if it is
	nfo, ifThere := "the nfo beside the file ("+name+")", ""
	if name == "" {
		nfo, ifThere = "an nfo beside the file", ", if there is one (the folder could not be read to see: "+unread+")"
	}
	switch {
	case fetchersOff && folder != nil && folder.SavesNfo:
		return "the server wrote the new ids into " + nfo + ifThere + ", but keeps any id there it had no new value for, so it may still name the old title: check it, and correct or remove it"
	case fetchersOff:
		return "the library does not save nfo files, so a refresh reads " + nfo + " again and puts back the ids it names" + ifThere + ": correct that nfo too, or remove it"
	}

	return "Emby reads " + nfo + " again at the next refresh" + ifThere + ", and puts back the title it names if that is another: correct or remove it"
}

// nfoBeside is the name of the nfo a server reads for an item - tvshow.nfo in
// a series' folder, movie.nfo or one named after the file beside a film or an
// episode - and, when the folder could not be read to say, why not. The match
// has already landed when this is asked, so a failed read is said in the
// answer rather than failing it.
func nfoBeside(ctx context.Context, client *embyfin.Client, it *embyfin.Item) (name, unread string) {
	if it.Path == "" || !onDisk(it.Path) {
		return "", ""
	}
	dir, want := parentDir(it.Path), []string{"movie.nfo", strings.TrimSuffix(baseName(it.Path), filepath.Ext(baseName(it.Path))) + ".nfo"}
	if it.Type == "Series" || it.Type == "Season" {
		dir, want = it.Path, []string{"tvshow.nfo", "season.nfo"}
	}
	entries, found, err := client.ListFolder(ctx, dir)
	switch {
	case err != nil:
		return "", err.Error()
	case !found:
		return "", "the server cannot find " + dir
	}
	for _, e := range entries {
		if !e.IsDir && slices.ContainsFunc(want, func(w string) bool { return strings.EqualFold(e.Name, w) }) {
			return e.Name, ""
		}
	}

	return "", ""
}
