package tools

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/animelist"
	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Anime held against Anime-Lists, the community mapping of every AniDB entry
// to where TVDB and TMDB hold it.
//
// AniDB gives an OVA, a film or a TV special an entry of its own. TVDB and
// TMDB often fold the same thing into the specials of another series, so a
// library matched on them holds it there. The list records both, which is
// what tells three things apart: a series whose ids disagree, because its
// TMDB id is the whole show while its AniDB id is one of that show's
// specials; specials that are an AniDB entry of their own, which a library
// keeping those apart would split out into a series; and series already
// kept apart, with the entry that says they should be.

// minSpecialS is how long a special has to run to be counted as part of an
// entry. An opening, an ending or a trailer filed among the specials runs a
// minute or two and sits at the same numbers.
const minSpecialS = 180

// heldSpecials are a series' specials that have a file. They are read from
// the specials season by its id: asking either server for season 0 by
// number asks for every season.
func heldSpecials(ctx context.Context, client *embyfin.Client, seriesID string) ([]embyfin.Item, error) {
	seasons, err := client.Seasons(ctx, seriesID, "")
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(seasons, func(s embyfin.Item) bool { return s.IndexNumber == 0 })
	if i < 0 {
		return nil, nil
	}
	eps, err := client.Episodes(ctx, seriesID, embyfin.EpisodeOptions{SeasonID: seasons[i].ID, Fields: "Path"})
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(eps, func(e embyfin.Item) bool { return !e.HasFile() }), nil
}

// placement is where the list puts an entry among a series' specials.
type placement struct {
	where string
	// numbers are the specials the list names, or nil where it gives only
	// the first; the entry then runs over the specials a library holds from
	// there, up to stop (where the next entry begins, 0 for none) or a
	// special the list gives to something else
	numbers     []int
	first, stop int
	claimed     func(int) bool
}

// in is which specials the entry is, among those a library holds.
func (p placement) in(held map[int]bool) []int {
	if p.numbers != nil {
		return p.numbers
	}
	var out []int
	for n := p.first; held[n] && (p.stop == 0 || n < p.stop) && (n == p.first || !p.claimed(n)); n++ {
		out = append(out, n)
	}

	return out
}

// placeIn is where an entry sits among a series' specials, by the numbering
// of the provider that folds it in there, TVDB's first: a library named by
// Sonarr numbers its specials TVDB's way. Two entries the list starts at
// the same special are not placed at all, because it does not say which is
// which.
func placeIn(list *animelist.List, e *animelist.Entry, tvdb, tmdb string) (placement, bool) {
	inTVDB, inTMDB := e.Specials()
	for _, p := range []struct{ provider, id, folded, label string }{
		{"tvdb", tvdb, inTVDB, "TVDB"},
		{"tmdb", tmdb, inTMDB, "TMDB"},
	} {
		if p.id == "" || p.folded != p.id {
			continue
		}
		first, numbers, known := e.Place(p.provider)
		if !known {
			continue
		}
		if numbers != nil {
			return placement{where: p.label + " specials " + spans(numbers), numbers: numbers}, true
		}

		claimed := func(n int) bool { return list.Claimed(p.provider, p.id, n) }
		if claimed(first) {
			return placement{}, false
		}
		stop := 0
		for _, next := range list.SpecialsIn(p.provider, p.id) {
			f, _, k := next.Place(p.provider)
			switch {
			case next == e || !k:
			case f == first:
				return placement{}, false
			case f > first && (stop == 0 || f < stop):
				stop = f
			}
		}

		return placement{where: p.label + " specials from " + strconv.Itoa(first), first: first, stop: stop, claimed: claimed}, true
	}

	return placement{}, false
}

// spans writes episode numbers the way a person would: 4-7, or 2, 5.
func spans(numbers []int) string {
	sorted := slices.Sorted(slices.Values(numbers))
	sorted = slices.Compact(sorted)
	var parts []string
	for i := 0; i < len(sorted); {
		j := i
		for j+1 < len(sorted) && sorted[j+1] == sorted[j]+1 {
			j++
		}
		part := strconv.Itoa(sorted[i])
		if j > i {
			part += "–" + strconv.Itoa(sorted[j])
		}
		parts = append(parts, part)
		i = j + 1
	}

	return strings.Join(parts, ", ")
}

// entryName is an AniDB entry as a finding names it.
func entryName(e *animelist.Entry) string {
	return "AniDB " + e.AniDB + " " + e.Name
}

func registerAnimeAudit(r *registry) {
	client := r.client
	lists := animelist.NewLoader(r.opts.AnimeList, r.opts.ProviderTransport)

	type animeIn struct {
		Library string `json:"library,omitempty" jsonschema:"one library by name or id; default every library"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum findings in each list, default 50"`
	}
	type animeSeries struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Year   int    `json:"year,omitempty"`
		AniDB  string `json:"anidb"          jsonschema:"the AniDB entry the series holds: id and title"`
		Detail string `json:"detail"`
	}
	type animeSpecial struct {
		ID       string `json:"id"`
		Episode  int    `json:"episode"`
		Name     string `json:"name"`
		RuntimeS int    `json:"runtime_s,omitempty"`
	}
	type animeSplit struct {
		SeriesID string         `json:"series_id"`
		Series   string         `json:"series"`
		AniDB    string         `json:"anidb"               jsonschema:"the AniDB entry these specials are: id and title"`
		Where    string         `json:"where"               jsonschema:"which of the series' specials the list says the entry is, by whose numbering"`
		Specials []animeSpecial `json:"specials"            jsonschema:"the specials the library holds there, with a file"`
		HeldAlso string         `json:"held_also,omitempty" jsonschema:"the library also holds the entry as a series of its own: that series' id"`
	}
	type animeOut struct {
		Source        string        `json:"list_source"`
		Entries       int           `json:"list_entries"        jsonschema:"AniDB entries the list maps, so an empty report is not a list that failed to load"`
		Scanned       int           `json:"series_scanned"`
		IDsDisagree   []animeSeries `json:"ids_disagree"        jsonschema:"series whose TMDB or TVDB id is a whole show while their AniDB id is one of that show's specials: one of the two ids is wrong"`
		SplitOut      []animeSplit  `json:"split_out"           jsonschema:"specials that are an AniDB entry of their own, which a library keeping those apart would split out into a series"`
		KeptSeparate  []animeSeries `json:"kept_separate"       jsonschema:"series held on their own that TMDB or TVDB fold into another series' specials; the AniDB entry is why they stay apart"`
		TotalDisagree int           `json:"total_ids_disagree"`
		TotalSplit    int           `json:"total_split_out"`
		TotalSeparate int           `json:"total_kept_separate"`
	}

	add(r, readTool, &mcp.Tool{
		Name: "audit_anime_ids",
		Description: "Check anime series against Anime-Lists, the community mapping of every AniDB entry to where TVDB and TMDB hold it. AniDB gives an OVA or a film an entry of its own where TVDB and TMDB often fold it into another series' specials. " +
			"Reports series whose ids disagree (a TMDB or TVDB id that is the whole show beside an AniDB id that is one of its specials), specials that are an AniDB entry of their own and could be split out into a series, and series already kept apart, with the entry that justifies it. " +
			"Reads the specials of each series the list says holds another entry's episodes; the list is fetched once a day.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in animeIn) (*mcp.CallToolResult, animeOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		list, err := lists.Load(ctx)
		if err != nil {
			return nil, animeOut{}, err
		}
		opts := embyfin.SearchOptions{IncludeItemTypes: "Series", Fields: "Path,ProviderIds,ProductionYear"}
		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, animeOut{}, err
		}
		if folder != nil {
			opts.ParentID = folder.ItemID
		}
		var series []embyfin.Item
		if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
			series = append(series, items...)

			return true
		}); err != nil {
			return nil, animeOut{}, err
		}

		out := animeOut{Source: lists.Source(), Entries: list.Len(), Scanned: len(series)}
		heldAs := map[string]string{} // AniDB id to the series holding it as its own
		for i := range series {
			if aid := providerID(&series[i], "anidb"); aid != "" {
				heldAs[aid] = series[i].ID
			}
		}

		disagree, separate, split := []animeSeries{}, []animeSeries{}, []animeSplit{}
		for i := range series {
			it := &series[i]
			aid, tvdb, tmdb := providerID(it, "anidb"), providerID(it, "tvdb"), providerID(it, "tmdb")

			// its own entry, where TVDB or TMDB fold that into specials
			if own := list.Entry(aid); own != nil {
				inTVDB, inTMDB := own.Specials()
				row := animeSeries{ID: it.ID, Name: it.Name, Year: it.ProductionYear, AniDB: entryName(own)}
				switch {
				case inTMDB != "" && inTMDB == tmdb:
					row.Detail = fmt.Sprintf("its TMDB id is the whole show (tv %s), but its AniDB id is one of that show's specials: one of the two is wrong", tmdb)
					disagree = append(disagree, row)
				case inTVDB != "" && inTVDB == tvdb:
					row.Detail = fmt.Sprintf("its TVDB id is the whole show (series %s), but its AniDB id is one of that show's specials: one of the two is wrong", tvdb)
					disagree = append(disagree, row)
				case inTVDB != "" || inTMDB != "":
					var where []string
					if inTMDB != "" {
						where = append(where, "TMDB folds it into the specials of tv "+inTMDB)
					}
					switch {
					case inTVDB != "" && len(where) > 0:
						where = append(where, "TVDB into those of series "+inTVDB)
					case inTVDB != "":
						where = append(where, "TVDB folds it into the specials of series "+inTVDB)
					}
					row.Detail = "an AniDB entry of its own; " + strings.Join(where, ", and ")
					separate = append(separate, row)
				}
			}

			// the entries the list says this series' specials hold
			var entries []*animelist.Entry
			for _, e := range slices.Concat(list.SpecialsIn("tvdb", tvdb), list.SpecialsIn("tmdb", tmdb)) {
				if e.AniDB != aid && !slices.Contains(entries, e) {
					entries = append(entries, e)
				}
			}
			if len(entries) == 0 {
				continue
			}
			specials, err := heldSpecials(ctx, client, it.ID)
			if err != nil {
				return nil, animeOut{}, fmt.Errorf("reading the specials of %s: %w", it.Name, err)
			}
			if len(specials) == 0 {
				continue
			}
			// the specials long enough to be part of an entry, by number
			held := map[int]bool{}
			specials = slices.DeleteFunc(specials, func(s embyfin.Item) bool {
				runtime := int(s.RunTimeTicks / ticksPerSecond)
				return runtime != 0 && runtime < minSpecialS
			})
			for _, s := range specials {
				for n := s.IndexNumber; n <= max(s.IndexNumber, s.IndexNumberEnd); n++ {
					held[n] = true
				}
			}
			for _, e := range entries {
				place, ok := placeIn(list, e, tvdb, tmdb)
				if !ok {
					continue
				}
				numbers := place.in(held)
				row := animeSplit{SeriesID: it.ID, Series: it.Name, AniDB: entryName(e), Where: place.where, HeldAlso: heldAs[e.AniDB], Specials: []animeSpecial{}}
				for _, s := range specials {
					last := max(s.IndexNumber, s.IndexNumberEnd)
					if slices.ContainsFunc(numbers, func(n int) bool { return n >= s.IndexNumber && n <= last }) {
						row.Specials = append(row.Specials, animeSpecial{ID: s.ID, Episode: s.IndexNumber, Name: s.Name, RuntimeS: int(s.RunTimeTicks / ticksPerSecond)})
					}
				}
				if len(row.Specials) > 0 {
					split = append(split, row)
				}
			}
		}

		byName := func(a, b animeSeries) int {
			return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
		}
		slices.SortFunc(disagree, byName)
		slices.SortFunc(separate, byName)
		slices.SortFunc(split, func(a, b animeSplit) int {
			return cmp.Or(strings.Compare(a.Series, b.Series), cmp.Compare(a.Specials[0].Episode, b.Specials[0].Episode))
		})
		out.TotalDisagree, out.TotalSplit, out.TotalSeparate = len(disagree), len(split), len(separate)
		out.IDsDisagree = disagree[:min(len(disagree), limit)]
		out.SplitOut = split[:min(len(split), limit)]
		out.KeptSeparate = separate[:min(len(separate), limit)]

		return nil, out, nil
	})
}
