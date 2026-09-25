package workarounds

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/katbyte/embyfin-mcp/internal/pandorest/openapi"
)

// The TMDB document is the v3 API's own OpenAPI file, from
// developer.themoviedb.org/openapi. It declares no schemas of its own, only
// inline ones, and tags nothing.

const tmdb = "tmdb"

// tmdbGroups names the group an operation lives in after the first segment
// of its path, where that is not simply the segment title-cased.
var tmdbGroups = map[string]string{
	"tv":            "TV",
	"guest_session": "GuestSession",
}

type tmdbTags struct{}

func (tmdbTags) Name() string    { return "tmdb-tags" }
func (tmdbTags) Service() string { return tmdb }
func (tmdbTags) Bug() string {
	return "no operation is tagged, so nothing groups them; the first segment of the path does (/3/movie/{movie_id}/credits is Movie)"
}

func (tmdbTags) Apply(spec *openapi.Spec) error {
	n := 0
	for _, path := range openapi.SortedKeys(spec.Paths) {
		for _, m := range spec.Paths[path].Methods() {
			if len(m.Operation.Tags) > 0 {
				return errors.New(m.Method + " " + path + " is tagged")
			}
			segment, _, _ := strings.Cut(strings.TrimPrefix(path, "/3/"), "/")
			if segment == "" {
				return errors.New(m.Method + " " + path + " has no first segment to name a tag after")
			}
			group := tmdbGroups[segment]
			if group == "" {
				group = strings.ToUpper(segment[:1]) + segment[1:]
			}
			m.Operation.Tags = []string{group}
			n++
		}
	}
	if n == 0 {
		return errors.New("the document has no operations")
	}

	return nil
}

type tmdbRawBodies struct{}

func (tmdbRawBodies) Name() string    { return "tmdb-raw-bodies" }
func (tmdbRawBodies) Service() string { return tmdb }
func (tmdbRawBodies) Bug() string {
	return "every request body is declared as an object holding one RAW_BODY string, the docs site's placeholder; the body's real shape is only in its request example ({\"value\": 8.5} for a rating)"
}

func (tmdbRawBodies) Apply(spec *openapi.Spec) error {
	n := 0
	for _, path := range openapi.SortedKeys(spec.Paths) {
		for _, m := range spec.Paths[path].Methods() {
			rb := m.Operation.RequestBody
			if rb == nil || rb.Content["application/json"] == nil {
				continue
			}
			media := rb.Content["application/json"]
			if s := media.Schema; s == nil || len(s.Properties) != 1 || s.Properties["RAW_BODY"] == nil {
				continue
			}
			names := openapi.SortedKeys(media.Examples)
			if len(names) == 0 || media.Examples[names[0]] == nil {
				return errors.New(m.Method + " " + path + " declares a RAW_BODY with no example to take its shape from")
			}
			schema, err := schemaOf(media.Examples[names[0]].Value)
			if err != nil {
				return fmt.Errorf("%s %s: its example: %w", m.Method, path, err)
			}
			media.Schema = schema
			n++
		}
	}
	if n == 0 {
		return errors.New("no request body is declared as a RAW_BODY")
	}

	return nil
}

// schemaOf is the schema a JSON example has: an object's fields by their
// values, a whole number an integer and any other number a number.
func schemaOf(raw json.RawMessage) (*openapi.Schema, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}

	return schemaOfValue(v), nil
}

func schemaOfValue(v any) *openapi.Schema {
	switch x := v.(type) {
	case map[string]any:
		s := &openapi.Schema{Type: openapi.TypeObject, Properties: map[string]*openapi.Schema{}}
		for k, e := range x {
			s.Properties[k] = schemaOfValue(e)
		}
		return s
	case []any:
		items := &openapi.Schema{}
		if len(x) > 0 {
			items = schemaOfValue(x[0])
		}
		return &openapi.Schema{Type: openapi.TypeArray, Items: items}
	case json.Number:
		if _, err := x.Int64(); err == nil {
			return &openapi.Schema{Type: openapi.TypeInteger}
		}
		return &openapi.Schema{Type: openapi.TypeNumber}
	case string:
		return &openapi.Schema{Type: openapi.TypeString}
	case bool:
		return &openapi.Schema{Type: openapi.TypeBoolean}
	}

	return &openapi.Schema{}
}

type tmdbEmptyLists struct{}

func (tmdbEmptyLists) Name() string    { return "tmdb-empty-lists" }
func (tmdbEmptyLists) Service() string { return tmdb }
func (tmdbEmptyLists) Bug() string {
	return "the document's schemas are drawn from examples, and a list an example left empty declares no item type (genres on the latest film, every result list of find); the same list is typed elsewhere in the document"
}

// tmdbBorrowed says where a list with no same-named twin takes its items
// from: the operation and the list whose items are the same kind of thing.
var tmdbBorrowed = map[string]struct{ operation, list string }{
	"tv_results":         {"search-tv", "results"},
	"person_results":     {"search-person", "results"},
	"tv_episode_results": {"", "episodes"},
	"tv_season_results":  {"", "seasons"},
}

func (tmdbEmptyLists) Apply(spec *openapi.Spec) error {
	// every typed list in the answers, by name, the first one found
	typed := map[string]*openapi.Schema{}
	byOperation := map[string]map[string]*openapi.Schema{}
	eachList(spec, func(operationID, name string, list *openapi.Schema) {
		if !typedItems(list.Items) {
			return
		}
		if typed[name] == nil {
			typed[name] = list.Items
		}
		if byOperation[operationID] == nil {
			byOperation[operationID] = map[string]*openapi.Schema{}
		}
		byOperation[operationID][name] = list.Items
	})

	n := 0
	var missing []string
	eachList(spec, func(operationID, name string, list *openapi.Schema) {
		if typedItems(list.Items) {
			return
		}
		items := typed[name]
		if from, ok := tmdbBorrowed[name]; ok {
			items = typed[from.list]
			if from.operation != "" {
				items = byOperation[from.operation][from.list]
			}
		}
		if name == "descriptors" {
			items = &openapi.Schema{Type: openapi.TypeString}
		}
		if items == nil {
			missing = append(missing, operationID+" "+name)
			return
		}
		list.Items = clone(items)
		n++
	})
	switch {
	case len(missing) > 0:
		return errors.New("these lists have no item type and nothing to take one from: " + strings.Join(missing, ", "))
	case n == 0:
		return errors.New("every list declares its item type")
	}

	return nil
}

// eachList calls fn for every list property in every answer, in document
// order, with the operation it answers and the property's name.
func eachList(spec *openapi.Spec, fn func(operationID, name string, list *openapi.Schema)) {
	var walk func(operationID string, s *openapi.Schema)
	walk = func(operationID string, s *openapi.Schema) {
		if s == nil {
			return
		}
		for _, name := range openapi.SortedKeys(s.Properties) {
			p := s.Properties[name]
			if p == nil {
				continue
			}
			if p.Type == openapi.TypeArray {
				fn(operationID, name, p)
				walk(operationID, p.Items)
			}
			walk(operationID, p)
		}
	}
	for _, path := range openapi.SortedKeys(spec.Paths) {
		for _, m := range spec.Paths[path].Methods() {
			for _, code := range openapi.SortedKeys(m.Operation.Responses) {
				r := m.Operation.Responses[code]
				if r == nil {
					continue
				}
				for _, ct := range openapi.SortedKeys(r.Content) {
					if media := r.Content[ct]; media != nil {
						walk(m.Operation.OperationID, media.Schema)
					}
				}
			}
		}
	}
}

func typedItems(s *openapi.Schema) bool {
	return s != nil && (s.Type != "" || len(s.Properties) > 0 || s.Ref != "")
}

// clone is a deep copy, so a borrowed schema is not shared between the
// places it now describes.
func clone(s *openapi.Schema) *openapi.Schema {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err) // a decoded schema always marshals
	}
	var out openapi.Schema
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}

	return &out
}

type tmdbNullFields struct{}

func (tmdbNullFields) Name() string    { return "tmdb-null-fields" }
func (tmdbNullFields) Service() string { return tmdb }
func (tmdbNullFields) Bug() string {
	return "a field an example left null declares no type (poster_path, imdb_id, deathday on the person who is alive); the same field is typed elsewhere in the document"
}

// tmdbNullTwins names the field a null one takes its type from, where no
// field of its own name is ever typed; a [] suffix takes a list's items.
var tmdbNullTwins = map[string]string{
	"next_episode_to_air": "last_episode_to_air",
	"deathday":            "birthday",
	"youtube_id":          "twitter_id",
	"parent_company":      "production_companies[]",
}

func (tmdbNullFields) Apply(spec *openapi.Spec) error {
	typed := map[string]*openapi.Schema{}
	eachField(spec, func(name string, s *openapi.Schema) {
		if typedField(s) && typed[name] == nil {
			typed[name] = s
		}
	})

	n := 0
	eachField(spec, func(name string, s *openapi.Schema) {
		if typedField(s) {
			return
		}
		twin := typed[name]
		if other, ok := tmdbNullTwins[name]; ok {
			list, items := strings.CutSuffix(other, "[]")
			twin = typed[list]
			if items && twin != nil {
				twin = twin.Items
			}
		}
		if twin == nil {
			return // untyped everywhere: left for the caller to decode
		}
		description := s.Description
		*s = *clone(twin)
		s.Description = description
		n++
	})
	if n == 0 {
		return errors.New("no field left untyped by a null example has a typed twin")
	}

	return nil
}

// eachField calls fn for every property of every answer, in document order
// (request bodies are one placeholder string each until tmdb-raw-bodies
// replaces them, and carry nothing to type).
func eachField(spec *openapi.Spec, fn func(name string, s *openapi.Schema)) {
	var walk func(s *openapi.Schema)
	walk = func(s *openapi.Schema) {
		if s == nil {
			return
		}
		for _, name := range openapi.SortedKeys(s.Properties) {
			if p := s.Properties[name]; p != nil {
				fn(name, p)
				walk(p)
				walk(p.Items)
			}
		}
	}
	for _, path := range openapi.SortedKeys(spec.Paths) {
		for _, m := range spec.Paths[path].Methods() {
			for _, code := range openapi.SortedKeys(m.Operation.Responses) {
				if r := m.Operation.Responses[code]; r != nil {
					for _, ct := range openapi.SortedKeys(r.Content) {
						if media := r.Content[ct]; media != nil {
							walk(media.Schema)
						}
					}
				}
			}
		}
	}
}

// typedField says whether a field declares what it holds: a type, and for
// an object its fields.
func typedField(s *openapi.Schema) bool {
	switch {
	case s == nil:
		return false
	case s.Type == openapi.TypeObject:
		return len(s.Properties) > 0 || len(s.AdditionalProperties) > 0
	}

	return s.Type != "" || s.Ref != "" || len(s.Properties) > 0
}

type tmdbStringIDs struct{}

func (tmdbStringIDs) Name() string    { return "tmdb-string-ids" }
func (tmdbStringIDs) Service() string { return tmdb }
func (tmdbStringIDs) Bug() string {
	return "a handful of operations declare an id path parameter as a string where every other operation declares the same parameter an integer (movie_id on movie-keywords)"
}

func (tmdbStringIDs) Apply(spec *openapi.Spec) error {
	integer := map[string]bool{}
	var params []*openapi.Parameter
	for _, path := range openapi.SortedKeys(spec.Paths) {
		for _, m := range spec.Paths[path].Methods() {
			for _, p := range m.Operation.Parameters {
				if p.In != openapi.InPath || p.Schema == nil {
					continue
				}
				if p.Schema.Type == openapi.TypeInteger {
					integer[p.Name] = true
				}
				params = append(params, p)
			}
		}
	}
	n := 0
	for _, p := range params {
		if p.Schema.Type == openapi.TypeString && integer[p.Name] {
			p.Schema.Type, p.Schema.Format = openapi.TypeInteger, "int32"
			n++
		}
	}
	if n == 0 {
		return errors.New("every id path parameter is declared the same way everywhere")
	}

	return nil
}

type tmdbWholeNumbers struct{}

func (tmdbWholeNumbers) Name() string    { return "tmdb-whole-numbers" }
func (tmdbWholeNumbers) Service() string { return tmdb }
func (tmdbWholeNumbers) Bug() string {
	return "a field an example happened to give a whole number is declared an integer, while the same field elsewhere is a number; TMDB answers fractions in both (vote_average 7.5, a review's rating 9.0)"
}

func (tmdbWholeNumbers) Apply(spec *openapi.Spec) error {
	number := map[string]bool{}
	eachField(spec, func(name string, s *openapi.Schema) {
		if s.Type == openapi.TypeNumber {
			number[name] = true
		}
	})
	n := 0
	eachField(spec, func(name string, s *openapi.Schema) {
		if s.Type == openapi.TypeInteger && number[name] {
			s.Type, s.Format = openapi.TypeNumber, ""
			n++
		}
	})
	if n == 0 {
		return errors.New("no field is declared an integer in one place and a number in another")
	}

	return nil
}

type tmdbChangeValues struct{}

func (tmdbChangeValues) Name() string    { return "tmdb-change-values" }
func (tmdbChangeValues) Service() string { return tmdb }
func (tmdbChangeValues) Bug() string {
	return "a change's value and original_value are declared as whatever the example's change held (a poster object, a string), but TMDB answers whatever the changed field holds: a string for a biography, an object keyed by the field for an image (title_logo, poster), so a change of any other field is lost or does not decode"
}

func (tmdbChangeValues) Apply(spec *openapi.Spec) error {
	n := 0
	for _, path := range openapi.SortedKeys(spec.Paths) {
		// one film's, show's or person's changes (/3/movie/changes lists
		// the ids of the films that changed)
		if !strings.HasSuffix(path, "}/changes") {
			continue
		}
		op := spec.Operation(http.MethodGet, path)
		if op == nil {
			continue
		}
		media, err := jsonResponse(op, "GET "+path)
		if err != nil {
			return err
		}
		changes := media.Schema
		if changes == nil || changes.Properties["changes"] == nil || changes.Properties["changes"].Items == nil {
			return errors.New("GET " + path + " no longer answers a list of changes")
		}
		items := changes.Properties["changes"].Items.Properties["items"]
		if items == nil || items.Items == nil {
			return errors.New("GET " + path + " no longer answers the items of a change")
		}
		for _, name := range []string{"value", "original_value"} {
			if v := items.Items.Properties[name]; v != nil && (v.Type != "" || len(v.Properties) > 0) {
				*v = openapi.Schema{Description: v.Description}
				n++
			}
		}
	}
	if n == 0 {
		return errors.New("no change declares the type of its value")
	}

	return nil
}

type tmdbDisplayPriorities struct{}

func (tmdbDisplayPriorities) Name() string    { return "tmdb-display-priorities" }
func (tmdbDisplayPriorities) Service() string { return tmdb }
func (tmdbDisplayPriorities) Bug() string {
	return "a watch provider's display_priorities is declared as an object with a field for each country the example happened to list; TMDB answers a priority for any country (KR, HR), which the fields drop"
}

func (tmdbDisplayPriorities) Apply(spec *openapi.Spec) error {
	for _, path := range []string{"/3/watch/providers/movie", "/3/watch/providers/tv"} {
		op, err := operation(spec, http.MethodGet, path)
		if err != nil {
			return err
		}
		media, err := jsonResponse(op, "GET "+path)
		if err != nil {
			return err
		}
		results := media.Schema
		if results == nil || results.Properties["results"] == nil || results.Properties["results"].Items == nil {
			return errors.New("GET " + path + " no longer answers a list of results")
		}
		p := results.Properties["results"].Items.Properties["display_priorities"]
		if p == nil || p.Type != openapi.TypeObject || len(p.Properties) == 0 {
			return errors.New("GET " + path + " no longer declares display_priorities with a field per country")
		}
		*p = openapi.Schema{Type: openapi.TypeObject, Description: p.Description, AdditionalProperties: json.RawMessage(`{"type":"integer"}`)}
	}

	return nil
}

type tmdbListIDs struct{}

func (tmdbListIDs) Name() string    { return "tmdb-list-ids" }
func (tmdbListIDs) Service() string { return tmdb }
func (tmdbListIDs) Bug() string {
	return "GET /3/list/{list_id} declares the list's id a string and TMDB answers a number; GET /3/list/{list_id}/item_status declares it a number and TMDB answers a string"
}

func (tmdbListIDs) Apply(spec *openapi.Spec) error {
	for _, fix := range []struct{ path, from, to string }{
		{"/3/list/{list_id}", openapi.TypeString, openapi.TypeInteger},
		{"/3/list/{list_id}/item_status", openapi.TypeInteger, openapi.TypeString},
	} {
		op, err := operation(spec, http.MethodGet, fix.path)
		if err != nil {
			return err
		}
		media, err := jsonResponse(op, "GET "+fix.path)
		if err != nil {
			return err
		}
		if media.Schema == nil || media.Schema.Properties["id"] == nil {
			return errors.New("GET " + fix.path + " no longer answers an id")
		}
		id := media.Schema.Properties["id"]
		if id.Type != fix.from {
			return fmt.Errorf("GET %s declares its id %s now", fix.path, id.Type)
		}
		id.Type, id.Format = fix.to, ""
	}

	return nil
}
