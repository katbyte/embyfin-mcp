package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolvePerson finds a person by exact name (case-insensitive) or id among
// the people whose name holds the search. When none matches exactly it
// returns the near ones instead, so a partial name answers with the people it
// could mean rather than an error; a name nobody has is an error.
func resolvePerson(ctx context.Context, client *embyfin.Client, nameOrID string) (found *embyfin.Item, near []embyfin.Item, err error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return nil, nil, errors.New("person is required")
	}
	people, err := client.Persons(ctx, nameOrID, 50)
	if err != nil {
		return nil, nil, err
	}
	for i := range people {
		if strings.EqualFold(people[i].Name, nameOrID) || people[i].ID == nameOrID {
			return &people[i], nil, nil
		}
	}
	if len(people) == 0 {
		// an id is no search term: look it up as one, when it is shaped like
		// one (Emby answers a name asked after as an id with a 500)
		if !client.LooksLikeID(nameOrID) {
			return nil, nil, fmt.Errorf("no person named %q in the library", nameOrID)
		}
		items, _, err := client.Search(ctx, embyfin.SearchOptions{IDs: nameOrID, IncludeItemTypes: "Person", Fields: "Path"})
		if err != nil {
			return nil, nil, err
		}
		if len(items) == 1 && items[0].ID == nameOrID {
			return &items[0], nil, nil
		}
		return nil, nil, fmt.Errorf("no person named %q in the library", nameOrID)
	}

	return nil, people, nil
}

func registerPersonTools(r *registry) {
	client := r.client

	type getIn struct {
		Person string `json:"person"          jsonschema:"the person's name (case-insensitive), a part of it, or their id"`
		Types  string `json:"types,omitempty" jsonschema:"comma-separated item types to list; default Movie,Series,Episode"`
	}
	type personRow struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	}
	type creditRow struct {
		itemSummary
		Credit string `json:"credit"         jsonschema:"Actor, Director, Writer, Producer, GuestStar..."`
		Role   string `json:"role,omitempty" jsonschema:"the character, for an actor"`
	}
	// a TV director, writer or guest star is credited on the episodes they
	// made or appeared in, not on the series: read for films and series
	// alone, they had no credits at all
	type episodeCredit struct {
		ID      string `json:"id"`
		Season  *int   `json:"season"          jsonschema:"0 for the specials; null when the server holds no season number for the episode"`
		Episode *int   `json:"episode"         jsonschema:"null when the server holds no episode number for it"`
		Title   string `json:"title,omitempty"`
		Credit  string `json:"credit"          jsonschema:"Director, Writer, GuestStar, Actor..."`
		Role    string `json:"role,omitempty"  jsonschema:"the character, for an actor or a guest star"`
	}
	type seriesCredits struct {
		Series   string          `json:"series"`
		SeriesID string          `json:"series_id,omitempty"`
		Episodes []episodeCredit `json:"episodes"            jsonschema:"by season and episode"`
	}
	type getOut struct {
		Name                string            `json:"name,omitempty"`
		ID                  string            `json:"id,omitempty"`
		MetadataProviderIDs map[string]string `json:"metadata_provider_ids,omitempty"`
		Overview            string            `json:"overview,omitempty"`
		Born                string            `json:"born,omitempty"`
		Credits             []creditRow       `json:"credits"                         jsonschema:"the films and series the library holds with them in it, oldest first; a person credited twice on one item is listed twice"`
		EpisodeCredits      []seriesCredits   `json:"episode_credits,omitempty"       jsonschema:"the episodes they are credited on, by series in the order the library holds them, when types includes Episode (the default): a TV director, writer or guest star is credited on episodes rather than on the series"`
		Candidates          []personRow       `json:"candidates,omitempty"            jsonschema:"when the name matched nobody exactly: the people it could mean, to ask again by name or id"`
		Note                string            `json:"note,omitempty"                  jsonschema:"set when the library was seen to change while it was read: an item added or removed then may be missing from the credits, or credited though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "person_get",
		Description: "One actor, director or writer and everything the library holds with them in it, with how each item credits them: 'what have I got with Denis Villeneuve', 'which of my films is Sigourney Weaver in'. Films and series are listed in credits, and the episodes they are credited on (a TV director, writer or guest star is credited on episodes, not on the series) in episode_credits by series. A part of a name that matches nobody exactly answers with the people it could mean.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		found, near, err := resolvePerson(ctx, client, in.Person)
		if err != nil {
			return nil, getOut{}, err
		}
		if found == nil {
			out := getOut{Candidates: make([]personRow, 0, len(near))}
			for i := range near {
				p := &near[i]
				out.Candidates = append(out.Candidates, personRow{Name: p.Name, ID: p.ID})
			}

			return nil, out, nil
		}
		person, err := client.Person(ctx, found.Name, "")
		if err != nil {
			return nil, getOut{}, err
		}
		out := getOut{
			Name: person.Name, ID: found.ID, MetadataProviderIDs: providerKeys(person.ProviderIDs),
			Overview: person.Overview, Born: person.PremiereDate, Credits: []creditRow{},
		}

		types := in.Types
		if types == "" {
			types = searchTypesAll + "," + typeEpisode
		}
		bySeries := map[string]int{}
		result, err := client.ReadAll(ctx, embyfin.SearchOptions{
			PersonIDs: found.ID, IncludeItemTypes: types, Fields: embyfin.FieldsDefault + ",People",
			SortBy: "ProductionYear,SortName,DateCreated", SortOrder: "Ascending",
		}, embyfin.ToAnswer, func(items []embyfin.Item) bool {
			for i := range items {
				it := &items[i]
				for _, p := range it.People {
					if p.ID != found.ID && !strings.EqualFold(p.Name, found.Name) {
						continue
					}
					role := p.Role
					if strings.EqualFold(role, p.Type) { // a provider sometimes fills the role with the job
						role = ""
					}
					if it.Type != typeEpisode {
						out.Credits = append(out.Credits, creditRow{itemSummary: summarise(it), Credit: p.Type, Role: role})

						continue
					}
					key := cmp.Or(it.SeriesID, it.SeriesName)
					at, ok := bySeries[key]
					if !ok {
						at = len(out.EpisodeCredits)
						bySeries[key] = at
						out.EpisodeCredits = append(out.EpisodeCredits, seriesCredits{Series: it.SeriesName, SeriesID: it.SeriesID})
					}
					out.EpisodeCredits[at].Episodes = append(out.EpisodeCredits[at].Episodes, episodeCredit{
						ID: it.ID, Season: it.ParentIndexNumber, Episode: it.IndexNumber, Title: it.Name, Credit: p.Type, Role: role,
					})
				}
			}
			return true
		})
		if err != nil {
			return nil, getOut{}, err
		}
		out.Note = result.Changed()
		for i := range out.EpisodeCredits {
			slices.SortStableFunc(out.EpisodeCredits[i].Episodes, func(a, b episodeCredit) int {
				return cmp.Or(compareNumbers(a.Season, b.Season), compareNumbers(a.Episode, b.Episode))
			})
		}

		return nil, out, nil
	})
}
