package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolvePerson finds a person by exact name (case-insensitive) or id among
// the people whose name holds the search, and lists the near ones when none
// matches.
func resolvePerson(ctx context.Context, client *embyfin.Client, nameOrID string) (*embyfin.Item, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return nil, errors.New("person is required")
	}
	people, err := client.Persons(ctx, nameOrID, 50)
	if err != nil {
		return nil, err
	}
	for i := range people {
		if strings.EqualFold(people[i].Name, nameOrID) || people[i].ID == nameOrID {
			return &people[i], nil
		}
	}
	if len(people) == 0 {
		// an id is no search term: look it up as one
		if items, _, err := client.Search(ctx, embyfin.SearchOptions{IDs: nameOrID, IncludeItemTypes: "Person", Fields: "Path"}); err == nil && len(items) == 1 && items[0].ID == nameOrID {
			return &items[0], nil
		}
		return nil, fmt.Errorf("no person named %q in the library", nameOrID)
	}
	names := make([]string, 0, len(people))
	for _, p := range people {
		names = append(names, p.Name)
	}

	return nil, fmt.Errorf("no person named %q (did you mean: %s)", nameOrID, strings.Join(names, ", "))
}

func registerPersonTools(r *registry) {
	client := r.client

	type getIn struct {
		Person string `json:"person"          jsonschema:"the person's name (case-insensitive) or id, from library_people"`
		Types  string `json:"types,omitempty" jsonschema:"comma-separated item types to list; default Movie,Series"`
	}
	type creditRow struct {
		itemSummary
		Credit string `json:"credit"         jsonschema:"Actor, Director, Writer, Producer, GuestStar..."`
		Role   string `json:"role,omitempty" jsonschema:"the character, for an actor"`
	}
	type getOut struct {
		Name                string            `json:"name"`
		ID                  string            `json:"id"`
		MetadataProviderIDs map[string]string `json:"metadata_provider_ids,omitempty"`
		Overview            string            `json:"overview,omitempty"`
		Born                string            `json:"born,omitempty"`
		Credits             []creditRow       `json:"credits"                         jsonschema:"what the library holds with them in it, oldest first; a person credited twice on one item is listed twice"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "person_get",
		Description: "One actor, director or writer and everything the library holds with them in it, with how each item credits them: 'what have I got with Denis Villeneuve', 'which of my films is Sigourney Weaver in'.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		found, err := resolvePerson(ctx, client, in.Person)
		if err != nil {
			return nil, getOut{}, err
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
			types = searchTypesAll
		}
		if err := client.SearchAll(ctx, embyfin.SearchOptions{
			PersonIDs: found.ID, IncludeItemTypes: types, Fields: embyfin.FieldsDefault + ",People",
			SortBy: "ProductionYear,SortName", SortOrder: "Ascending",
		}, func(items []embyfin.Item) bool {
			for i := range items {
				it := &items[i]
				for _, p := range it.People {
					if p.ID == found.ID || strings.EqualFold(p.Name, found.Name) {
						row := creditRow{itemSummary: summarise(it), Credit: p.Type, Role: p.Role}
						if strings.EqualFold(row.Role, row.Credit) { // a provider sometimes fills the role with the job
							row.Role = ""
						}
						out.Credits = append(out.Credits, row)
					}
				}
			}
			return true
		}); err != nil {
			return nil, getOut{}, err
		}

		return nil, out, nil
	})
}
