package differ

import (
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/internal/pandorest/definitions"
)

func str() definitions.TypeRef { return definitions.TypeRef{Type: definitions.String} }

func ref(name string) definitions.TypeRef {
	return definitions.TypeRef{Type: definitions.Reference, ReferenceName: name}
}

// base is a small service the tests change one thing at a time.
func base() *definitions.Service {
	return &definitions.Service{
		Name: "mini", Title: "Mini", APIVersion: "1", Workarounds: []string{"mini-old"},
		Groups: []definitions.Group{
			{
				Name: "Items",
				Operations: []definitions.Operation{
					{
						Name: "GetItems", Method: "GET", Path: "/Items", ExpectedStatusCodes: []int{200},
						Options: []definitions.Option{
							{Name: "Limit", Field: "Limit", In: definitions.InQuery, Type: definitions.TypeRef{Type: definitions.Integer}},
							{Name: "Fields", Field: "Fields", In: definitions.InQuery, Type: str()},
						},
						Response: &definitions.Body{ContentType: "application/json", Type: ref("Item")},
						Pageable: &definitions.Pageable{},
					},
					{
						Name: "DeleteItem", Method: "DELETE", Path: "/Items/{Id}", ExpectedStatusCodes: []int{204},
						PathParameters: []definitions.PathParameter{{Name: "Id", Argument: "id", Type: str()}},
					},
				},
				Models: []definitions.Model{{Name: "Item", Fields: []definitions.Field{
					{Name: "Id", JSONName: "Id", Type: str()},
					{Name: "Tags", JSONName: "Tags", Type: definitions.TypeRef{Type: definitions.List, NestedItem: &definitions.TypeRef{Type: definitions.String}}},
				}}},
				Constants: []definitions.Constant{{Name: "Kind", Values: []definitions.ConstantValue{{Name: "KindMovie", Value: "Movie"}, {Name: "KindSeries", Value: "Series"}}}},
			},
		},
	}
}

func TestNoChanges(t *testing.T) {
	t.Parallel()

	r := Diff(base(), base())
	if !r.Empty() || r.Breaking() || r.String() != "mini: no changes\n" {
		t.Errorf("Diff of the same service = %q", r.String())
	}
}

func TestDiff(t *testing.T) {
	t.Parallel()

	newer := base()
	newer.APIVersion = "2"
	newer.Workarounds = []string{"mini-new"}
	items := &newer.Groups[0]
	get := &items.Operations[0]
	get.Options[0].Type = definitions.TypeRef{Type: definitions.Integer64}                                                                   // changed: breaking
	get.Options = append(get.Options[:1], definitions.Option{Name: "SearchTerm", Field: "SearchTerm", In: definitions.InQuery, Type: str()}) // Fields removed, SearchTerm added
	get.ExpectedStatusCodes = []int{200, 204}                                                                                                // widened: not breaking
	items.Operations = items.Operations[:1]                                                                                                  // DeleteItem removed
	items.Operations = append(items.Operations, definitions.Operation{Name: "PostItem", Method: "POST", Path: "/Items", ExpectedStatusCodes: []int{200}})
	items.Models[0].Fields = append(items.Models[0].Fields[:1], definitions.Field{Name: "TagItems", JSONName: "TagItems", Type: str()})
	items.Constants[0].Values = append(items.Constants[0].Values[:1], definitions.ConstantValue{Name: "KindEpisode", Value: "Episode"})
	newer.Groups = append(newer.Groups, definitions.Group{Name: "Other", Models: []definitions.Model{{Name: "Extra"}}})

	r := Diff(base(), newer)
	if r.Empty() || !r.Breaking() {
		t.Fatalf("Diff = %q, want breaking changes", r.String())
	}
	got := r.String()
	for _, want := range []string{
		"mini: 2 added, 1 removed, 3 changed (4 breaking)",
		"  document: Mini 1 -> Mini 2",
		"  workaround added: mini-new",
		"  workaround removed: mini-old",
		"+ operation PostItem (POST /Items)",
		"- operation DeleteItem (DELETE /Items/{Id}) [breaking]",
		"~ operation GetItems (GET /Items)",
		"    - option query fields: String [breaking]",
		"    ~ option query limit: Integer -> Integer64 [breaking]",
		"    + option query searchterm: String",
		"    ~ expected status codes [200] -> [200 204]\n",
		"~ model Item",
		"    + field TagItems: String",
		"    - field Tags: List[String] [breaking]",
		"+ model Extra",
		"~ constant Kind",
		`    + value "Episode"`,
		`    - value "Series" [breaking]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
}

func TestDiffBodiesAndPaths(t *testing.T) {
	t.Parallel()

	newer := base()
	get := &newer.Groups[0].Operations[0]
	get.Name = "ListItems"
	get.Response = nil
	get.Pageable = nil
	del := &newer.Groups[0].Operations[1]
	del.PathParameters[0].Type = definitions.TypeRef{Type: definitions.Integer}
	del.Request = &definitions.Body{ContentType: "application/json", Type: ref("Item")}
	del.ExpectedStatusCodes = []int{200}
	newer.Groups = append(newer.Groups, definitions.Group{Name: "Moved"})
	newer.Groups[1].Operations = append(newer.Groups[1].Operations, *del)
	newer.Groups[0].Operations = newer.Groups[0].Operations[:1]

	got := Diff(base(), newer)
	for _, want := range []string{
		"~ operation ListItems (GET /Items)",
		"    ~ method name GetItems -> ListItems [breaking]",
		"    - response Reference(Item) (application/json) [breaking]",
		"    ~ pageable true -> false [breaking]",
		"~ operation DeleteItem (DELETE /Items/{Id})",
		"    ~ tag Items -> Moved\n",
		"    ~ path parameter 1: id String -> id Integer [breaking]",
		"    + request Reference(Item) (application/json) [breaking]",
		"    ~ expected status codes [204] -> [200] [breaking]",
	} {
		if !strings.Contains(got.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, got.String())
		}
	}
}

func list() definitions.TypeRef {
	return definitions.TypeRef{Type: definitions.List, NestedItem: &definitions.TypeRef{Type: definitions.String}}
}

func dict() definitions.TypeRef {
	return definitions.TypeRef{Type: definitions.Dictionary, NestedItem: &definitions.TypeRef{Type: definitions.String}}
}

// wired is base() with a list option, an object option and a union model,
// the shapes whose wire form and variants the differ compares.
func wired() *definitions.Service {
	svc := base()
	items := &svc.Groups[0]
	items.Operations[0].Options = append(items.Operations[0].Options,
		definitions.Option{Name: "Ids", Field: "Ids", In: definitions.InQuery, Type: list(), CommaSeparated: true},
		definitions.Option{Name: "Tags", Field: "Tags", In: definitions.InQuery, Type: list()},
		definitions.Option{Name: "StreamOptions", Field: "StreamOptions", In: definitions.InQuery, Type: dict()},
		definitions.Option{Name: "X-Ids", Field: "XIds", In: definitions.InHeader, Type: list(), CommaSeparated: true},
	)
	items.Models[0].Fields = append(items.Models[0].Fields, definitions.Field{Name: "Url", JSONName: "Url", Type: str()})
	items.Models = append(items.Models, definitions.Model{Name: "Union", Union: []string{"Item"}})

	return svc
}

// What the differ marks breaking: a caller of the generated SDK would fail
// to compile, or send something the server reads differently.
func TestDiffBreaking(t *testing.T) {
	t.Parallel()

	newer := wired()
	get := &newer.Groups[0].Operations[0]
	get.Options[2].CommaSeparated = false // Ids: comma-separated -> one key per value
	get.Options[3].CommaSeparated = true  // Tags: one key per value -> comma-separated
	get.Options[4].DeepObject = true      // StreamOptions
	get.Options[5].CommaSeparated = false // X-Ids header
	get.Options[0].Required = true        // Limit
	get.Options[1].Field = "FieldList"    // Fields
	newer.Groups[0].Models[0].Fields[2].Name = "URL"
	newer.Groups[0].Models[1].Union = []string{"Item", "Other"}

	r := Diff(wired(), newer)
	if !r.Breaking() {
		t.Fatalf("Diff = %q, want breaking", r.String())
	}
	got := r.String()
	for _, want := range []string{
		"mini: 0 added, 0 removed, 3 changed (3 breaking)",
		"~ operation GetItems (GET /Items)",
		"    ~ option query ids: sent comma-separated -> one key per value [breaking]",
		"    ~ option query tags: sent one key per value -> comma-separated [breaking]",
		"    ~ option query streamoptions: deep object false -> true [breaking]",
		"    ~ option header x-ids: sent comma-separated -> one key per value [breaking]",
		"    ~ option query limit: required false -> true [breaking]",
		"    ~ option query fields: field Fields -> FieldList [breaking]",
		"~ model Item",
		"    ~ field Url: Go name Url -> URL [breaking]",
		"~ model Union",
		"    ~ union [Item] -> [Item Other] [breaking]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
}

// What is reported and not breaking: the generated code is the same, or
// only accepts more.
func TestDiffNotBreaking(t *testing.T) {
	t.Parallel()

	newer := wired()
	get := &newer.Groups[0].Operations[0]
	get.Description = "Gets items."
	get.Deprecated = true
	get.Options[0].Description = "How many."
	get.Options[0].Deprecated = true
	get.Options[1].Required = false // already false: unchanged
	get.ExpectedStatusCodes = []int{200, 204}
	newer.Groups[0].Operations[1].Options = append(newer.Groups[0].Operations[1].Options,
		definitions.Option{Name: "Force", Field: "Force", In: definitions.InQuery, Type: definitions.TypeRef{Type: definitions.Boolean}, Required: true})
	item := &newer.Groups[0].Models[0]
	item.Fields[0].Nullable = true
	item.Fields[1].Description = "The tags."

	r := Diff(wired(), newer)
	got := r.String()
	if r.Empty() {
		t.Fatal("Diff reported no changes")
	}
	if r.Breaking() {
		t.Errorf("Diff marks a non-breaking change breaking:\n%s", got)
	}
	for _, want := range []string{
		"mini: 0 added, 0 removed, 3 changed (0 breaking)",
		"~ operation GetItems (GET /Items)\n",
		"    ~ description changed\n",
		"    ~ deprecated false -> true\n",
		"    ~ option query limit: deprecated false -> true\n",
		"    ~ option query limit: description changed\n",
		"    ~ expected status codes [200] -> [200 204]\n",
		"~ operation DeleteItem (DELETE /Items/{Id})\n",
		"    + option query force: Boolean\n",
		"~ model Item\n",
		"    ~ field Id: nullable false -> true\n",
		"    ~ field Tags: description changed\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[breaking]") || strings.Contains(got, "fields") {
		t.Errorf("report marks something breaking, or reports the unchanged option:\n%s", got)
	}

	// required true -> false only relaxes the caller
	relaxed := wired()
	relaxed.Groups[0].Operations[0].Options[0].Required = true
	newer = wired()
	r = Diff(relaxed, newer)
	if r.Breaking() || !strings.Contains(r.String(), "    ~ option query limit: required true -> false\n") {
		t.Errorf("required true -> false = %q", r.String())
	}
}
