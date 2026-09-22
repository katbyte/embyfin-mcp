package openapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const tinySpec = `{
  "openapi": "3.0.1",
  "info": {"title": "Tiny", "version": "7"},
  "paths": {
    "/Items": {
      "get": {"operationId": "GetItems", "tags": ["Items"], "parameters": [
          {"name": "Ids", "in": "query", "schema": {"type": "array", "items": {"type": "string"}}},
          {"name": "X-Emby-Token", "in": "header", "schema": {"type": "string"}}],
        "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Item"}}}}}},
      "post": {"operationId": "PostItems", "responses": {"204": {"description": "ok"}}},
      "delete": {"operationId": "DeleteItems", "responses": {"204": {"description": "ok"}}},
      "head": {"operationId": "HeadItems", "responses": {"200": {"description": "ignored"}}}
    }
  },
  "components": {"schemas": {
    "Item": {"type": "object", "properties": {"Id": {"type": "string"}}},
    "Kind": {"type": "string", "enum": ["Movie", "Series"]}
  }}
}`

func parseTiny(t *testing.T) *Spec {
	t.Helper()
	s, err := Parse([]byte(tinySpec))
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func TestParse(t *testing.T) {
	t.Parallel()

	s := parseTiny(t)
	if s.OpenAPI != "3.0.1" || s.Info.Title != "Tiny" || s.Info.Version != "7" {
		t.Errorf("header = %q %q %q", s.OpenAPI, s.Info.Title, s.Info.Version)
	}
	if len(s.Paths) != 1 || len(s.Components.Schemas) != 2 {
		t.Errorf("paths = %d, schemas = %d", len(s.Paths), len(s.Components.Schemas))
	}
	item := s.Components.Schemas["Item"]
	if item == nil || item.Type != TypeObject || item.Properties["Id"] == nil || item.Properties["Id"].Type != TypeString {
		t.Errorf("Item = %+v", item)
	}
	get := s.Paths["/Items"].Get
	if get == nil || get.OperationID != "GetItems" || !slices.Equal(get.Tags, []string{"Items"}) || len(get.Parameters) != 2 {
		t.Fatalf("GET /Items = %+v", get)
	}
	if ref := get.Responses["200"].Content["application/json"].Schema.RefName(); ref != "Item" {
		t.Errorf("response ref = %q", ref)
	}
	// HEAD and OPTIONS are not part of PathItem
	if got := s.Paths["/Items"].Methods(); len(got) != 3 || got[0].Method != http.MethodGet || got[1].Method != http.MethodPost || got[2].Method != http.MethodDelete {
		t.Errorf("Methods = %+v", got)
	}
}

func TestParseRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, raw, want string
	}{
		{"not json", `{"openapi": `, "decoding spec"},
		{"swagger 2", `{"swagger": "2.0", "openapi": "2.0"}`, `unsupported openapi version "2.0"`},
		{"no version", `{"info": {}}`, `unsupported openapi version ""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := Parse([]byte(tt.raw))
			if err == nil || s != nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Parse = %+v, %v; want error containing %q", s, err, tt.want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(path, []byte(tinySpec), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil || s.Info.Title != "Tiny" {
		t.Errorf("Load = %+v, %v", s, err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("Load of a missing file did not fail")
	}
}

func TestSpecOperation(t *testing.T) {
	t.Parallel()

	s := parseTiny(t)
	if op := s.Operation("GET", "/Items"); op == nil || op.OperationID != "GetItems" {
		t.Errorf("GET /Items = %+v", op)
	}
	if op := s.Operation("get", "/Items"); op == nil || op.OperationID != "GetItems" {
		t.Errorf("method lookup is not case-insensitive: %+v", op)
	}
	if op := s.Operation("DELETE", "/Items"); op == nil || op.OperationID != "DeleteItems" {
		t.Errorf("DELETE /Items = %+v", op)
	}
	if op := s.Operation("PUT", "/Items"); op != nil {
		t.Errorf("PUT /Items = %+v, want nil", op)
	}
	if op := s.Operation("GET", "/Nope"); op != nil {
		t.Errorf("GET /Nope = %+v, want nil", op)
	}
}

func TestOperationParameter(t *testing.T) {
	t.Parallel()

	op := parseTiny(t).Operation("GET", "/Items")
	if p := op.Parameter(InQuery, "ids"); p == nil || p.Name != "Ids" {
		t.Errorf("query ids = %+v (lookup is case-insensitive)", p)
	}
	if p := op.Parameter(InHeader, "x-emby-token"); p == nil || p.Name != "X-Emby-Token" {
		t.Errorf("header token = %+v", p)
	}
	if p := op.Parameter(InPath, "Ids"); p != nil {
		t.Errorf("path Ids = %+v, want nil (wrong location)", p)
	}
}

func TestExploded(t *testing.T) {
	t.Parallel()

	yes, no := true, false
	tests := []struct {
		name string
		prm  Parameter
		want bool
	}{
		{"query default is one key per value", Parameter{In: InQuery}, true},
		{"query style form", Parameter{In: InQuery, Style: "form"}, true},
		{"query style form explode false", Parameter{In: InQuery, Style: "form", Explode: &no}, false},
		{"query explode false", Parameter{In: InQuery, Explode: &no}, false},
		{"query explode true", Parameter{In: InQuery, Explode: &yes}, true},
		{"query spaceDelimited", Parameter{In: InQuery, Style: "spaceDelimited"}, false},
		{"header default is comma-separated", Parameter{In: InHeader}, false},
		{"header style simple", Parameter{In: InHeader, Style: "simple"}, false},
		{"header style form", Parameter{In: InHeader, Style: "form"}, true},
		{"header explode true", Parameter{In: InHeader, Explode: &yes}, true},
		{"header explode false", Parameter{In: InHeader, Style: "form", Explode: &no}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.prm.Exploded(); got != tt.want {
				t.Errorf("Exploded() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestDeepObject(t *testing.T) {
	t.Parallel()

	if !(&Parameter{In: InQuery, Style: "deepObject"}).DeepObject() {
		t.Error("style deepObject is not DeepObject")
	}
	for _, style := range []string{"", "form", "simple"} {
		if (&Parameter{In: InQuery, Style: style}).DeepObject() {
			t.Errorf("style %q is DeepObject", style)
		}
	}
}

func TestAdditional(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      string
		wantOK   bool
		wantType string
	}{
		{"absent", "", false, ""},
		{"false", "false", false, ""},
		{"true", "true", true, ""},
		{"empty object", "{}", true, ""},
		{"empty object with space", "{ }", true, ""},
		{"typed", `{"type": "string"}`, true, TypeString},
		{"ref", `{"$ref": "#/components/schemas/Item"}`, true, ""},
		{"malformed string", `"yes"`, true, ""},
		{"malformed type", `{"type": 5}`, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &Schema{}
			if tt.raw != "" {
				s.AdditionalProperties = json.RawMessage(tt.raw)
			}
			sch, ok := s.Additional()
			if ok != tt.wantOK {
				t.Fatalf("Additional() ok = %t, want %t", ok, tt.wantOK)
			}
			if !ok {
				if sch != nil {
					t.Errorf("Additional() = %+v with ok false", sch)
				}
				return
			}
			if sch == nil || sch.Type != tt.wantType {
				t.Errorf("Additional() = %+v, want type %q", sch, tt.wantType)
			}
			if tt.name == "ref" && sch.RefName() != "Item" {
				t.Errorf("ref lost: %+v", sch)
			}
		})
	}
}

func TestRefName(t *testing.T) {
	t.Parallel()

	var nilSchema *Schema
	tests := []struct {
		name string
		s    *Schema
		want string
	}{
		{"nil", nilSchema, ""},
		{"plain", &Schema{Type: TypeString}, ""},
		{"ref", &Schema{Ref: SchemaRefPrefix + "Item"}, "Item"},
		{"foreign ref kept whole", &Schema{Ref: "#/definitions/Item"}, "#/definitions/Item"},
		{"allOf single ref", &Schema{AllOf: []*Schema{{Ref: SchemaRefPrefix + "Kind"}}, Nullable: true}, "Kind"},
		{"allOf single inline", &Schema{AllOf: []*Schema{{Type: TypeString}}}, ""},
		{"allOf two refs", &Schema{AllOf: []*Schema{{Ref: SchemaRefPrefix + "A"}, {Ref: SchemaRefPrefix + "B"}}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.s.RefName(); got != tt.want {
				t.Errorf("RefName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnumValues(t *testing.T) {
	t.Parallel()

	s := &Schema{Type: TypeString, Enum: []json.RawMessage{json.RawMessage(`"Movie"`), json.RawMessage(`"Series"`), json.RawMessage(`1`), json.RawMessage(`true`)}}
	if got := s.EnumValues(); !slices.Equal(got, []string{"Movie", "Series", "1", "true"}) {
		t.Errorf("EnumValues() = %q", got)
	}
	if got := (&Schema{}).EnumValues(); len(got) != 0 {
		t.Errorf("EnumValues() of no enum = %q", got)
	}
}

func TestIsEnumIsUnion(t *testing.T) {
	t.Parallel()

	var nilSchema *Schema
	enum := []json.RawMessage{json.RawMessage(`"A"`)}
	tests := []struct {
		name      string
		s         *Schema
		enum, uni bool
	}{
		{"nil", nilSchema, false, false},
		{"plain string", &Schema{Type: TypeString}, false, false},
		{"string enum", &Schema{Type: TypeString, Enum: enum}, true, false},
		{"untyped enum", &Schema{Enum: enum}, true, false},
		{"integer enum", &Schema{Type: TypeInteger, Enum: enum}, false, false},
		{"empty enum", &Schema{Type: TypeString, Enum: []json.RawMessage{}}, false, false},
		{"oneOf", &Schema{Type: TypeObject, OneOf: []*Schema{{Ref: SchemaRefPrefix + "A"}}}, false, true},
		{"anyOf", &Schema{AnyOf: []*Schema{{Type: TypeString}}}, false, true},
		{"allOf is not a union", &Schema{AllOf: []*Schema{{Ref: SchemaRefPrefix + "A"}, {Ref: SchemaRefPrefix + "B"}}}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.s.IsEnum(); got != tt.enum {
				t.Errorf("IsEnum() = %t, want %t", got, tt.enum)
			}
			if got := tt.s.IsUnion(); got != tt.uni {
				t.Errorf("IsUnion() = %t, want %t", got, tt.uni)
			}
		})
	}
}

func TestMethods(t *testing.T) {
	t.Parallel()

	item := &PathItem{Patch: &Operation{OperationID: "p"}, Get: &Operation{OperationID: "g"}, Put: &Operation{OperationID: "u"}}
	got := item.Methods()
	names := make([]string, 0, len(got))
	for _, m := range got {
		names = append(names, m.Method+":"+m.Operation.OperationID)
	}
	if !slices.Equal(names, []string{"GET:g", "PUT:u", "PATCH:p"}) {
		t.Errorf("Methods() = %v", names)
	}
	if got := (&PathItem{}).Methods(); len(got) != 0 {
		t.Errorf("Methods() of an empty item = %+v", got)
	}
}

func TestSortedKeys(t *testing.T) {
	t.Parallel()

	got := SortedKeys(map[string]int{"b": 1, "Z": 2, "a": 3, "A": 4})
	if !slices.Equal(got, []string{"A", "Z", "a", "b"}) {
		t.Errorf("SortedKeys = %v", got)
	}
	if got := SortedKeys(map[string]*Schema(nil)); len(got) != 0 {
		t.Errorf("SortedKeys of nil = %v", got)
	}
}
