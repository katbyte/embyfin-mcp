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
		MetadataProviderIDs map[string]string `json:"metadata_provider_ids,omitempty" jsonschema:"keyed tmdb, imdb, tvdb"`
		Provider            string            `json:"search_provider,omitempty"`
		Overview            string            `json:"overview,omitempty"`
	}
	type identifyOut struct {
		Item       string      `json:"item"`
		Candidates []candidate `json:"candidates" jsonschema:"suggestions only; nothing is changed until item_identify_apply"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "item_identify",
		Description: "Ask the metadata providers for candidate matches for an item (suggestions only — verify year and runtime before applying with item_identify_apply).",
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
		ID               string `json:"id"                           jsonschema:"the library item id"`
		Kind             string `json:"kind"                         jsonschema:"movie or series"`
		Candidate        int    `json:"candidate"                    jsonschema:"index from item_identify's candidates"`
		Name             string `json:"name,omitempty"               jsonschema:"must match the name/year overrides used in item_identify so indexes line up"`
		Year             int    `json:"year,omitempty"`
		ReplaceAllImages bool   `json:"replace_all_images,omitempty"`
	}
	type applyOut struct {
		Applied             string            `json:"applied"                         jsonschema:"the candidate applied"`
		Name                string            `json:"name"                            jsonschema:"the item as it is now"`
		Year                int               `json:"year,omitempty"`
		Overview            string            `json:"overview,omitempty"`
		MetadataProviderIDs map[string]string `json:"metadata_provider_ids,omitempty" jsonschema:"the item's ids now, keyed tmdb, imdb, tvdb"`
		Note                string            `json:"note,omitempty"                  jsonschema:"what the change could not do"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "item_identify_apply",
		Description: "Apply a candidate from item_identify: rewrites the item's identity and re-fetches its metadata and images, then reads the item back, once the refresh has saved it, and reports it as it now is. An identity the server did not take is an error (Emby keeps the one an nfo beside the file names). " +
			"In a library with its metadata fetchers off the ids are set by a plain edit of the item, which changes them and nothing else (the server's apply would refresh the item with nothing to fetch): every id the title goes by, the ones the candidate carries and the others the server's providers give for it (a TMDB candidate's IMDb id), so an old wrong id is replaced rather than left; check the year and overview reported, and set them with item_edit. " +
			"note says what the next refresh may undo: an nfo beside the file that may still name the old title (a refresh reads it again; correct or remove it), and on Emby, which keeps watch state by provider id, that the item now shows each user's watched mark and favourite for the title it was matched to, the old ones coming back with the old ids. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in applyIn) (*mcp.CallToolResult, applyOut, error) {
		results, err := client.RemoteSearch(ctx, in.Kind, in.ID, in.Name, in.Year)
		if err != nil {
			return nil, applyOut{}, err
		}

		if in.Candidate < 0 || in.Candidate >= len(results) {
			return nil, applyOut{}, fmt.Errorf("candidate %d out of range (search returned %d results)", in.Candidate, len(results))
		}
		chosen := results[in.Candidate]

		// in a library with its metadata fetchers off there is nothing to
		// fetch, and the server's apply refreshes the item as if there were:
		// Jellyfin's replaced every episode number of a series with nothing.
		// There the ids are set with a plain edit, which changes them and
		// nothing else.
		current, err := client.ItemByID(ctx, in.ID)
		if err != nil {
			return nil, applyOut{}, err
		}
		folder, err := libraryOf(ctx, client, current)
		if err != nil {
			return nil, applyOut{}, err
		}
		fetchersOff := folder != nil && folder.FetchersOff(current.Type)

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
			// lookup that fails leaves the candidate's own
			ids := chosen.ProviderIDs
			if full, lerr := client.RemoteIDs(ctx, in.Kind, chosen.Name, chosen.ProductionYear, ids); lerr == nil {
				ids = full
			}
			it, err = client.SetProviderIDs(ctx, admin.ID, in.ID, ids)
		} else {
			it, err = client.ApplyRemoteSearchResult(ctx, in.ID, chosen, in.ReplaceAllImages)
		}
		if err != nil {
			return nil, applyOut{}, err
		}

		overview := it.Overview
		if len(overview) > 200 {
			overview = overview[:200] + "..."
		}
		out := applyOut{
			Applied: fmt.Sprintf("%s (%d)", chosen.Name, chosen.ProductionYear),
			Name:    it.Name, Year: it.ProductionYear, Overview: overview,
			MetadataProviderIDs: providerKeys(it.ProviderIDs),
		}
		var notes []string
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
	name, known := nfoBeside(ctx, client, it)
	if known && name == "" {
		return ""
	}
	// named when the folder could be read, and said to be there only if it is
	nfo, ifThere := "the nfo beside the file ("+name+")", ""
	if name == "" {
		nfo, ifThere = "an nfo beside the file", ", if there is one"
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
// episode - and whether the folder could be read to say.
func nfoBeside(ctx context.Context, client *embyfin.Client, it *embyfin.Item) (name string, known bool) {
	if it.Path == "" || !onDisk(it.Path) {
		return "", true
	}
	dir, want := parentDir(it.Path), []string{"movie.nfo", strings.TrimSuffix(baseName(it.Path), filepath.Ext(baseName(it.Path))) + ".nfo"}
	if it.Type == "Series" || it.Type == "Season" {
		dir, want = it.Path, []string{"tvshow.nfo", "season.nfo"}
	}
	entries, found, err := client.ListFolder(ctx, dir)
	if err != nil || !found {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir && slices.ContainsFunc(want, func(w string) bool { return strings.EqualFold(e.Name, w) }) {
			return e.Name, true
		}
	}

	return "", true
}
