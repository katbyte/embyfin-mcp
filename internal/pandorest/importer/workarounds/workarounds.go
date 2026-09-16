// Package workarounds holds the importer's fixes for bugs in the vendored
// OpenAPI documents, one named workaround per bug, after Pandora's
// dataworkarounds.
//
// Every workaround patches the loaded document before it is normalised, and
// every one first checks that the bug it fixes is still there: when the
// document no longer has the problem (a refreshed spec declares the missing
// parameter, the duplicate operationId is gone) the workaround fails the
// import instead of silently doing nothing, so dead workarounds get deleted
// rather than accumulating. The applied names are recorded in the service's
// definitions.
//
// Only the shape of the document belongs here: what an operation takes and
// answers. Behaviour the document cannot express (Emby keying watch state by
// provider id, a filter it silently drops) stays in lib/embyfin, next to the
// live tests that found it.
package workarounds

import (
	"fmt"
	"strings"

	"github.com/katbyte/embyfin-mcp/internal/pandorest/openapi"
)

// Workaround fixes one bug in one service's document.
type Workaround interface {
	// Name identifies the workaround in logs and in Service.json.
	Name() string
	// Service is the config service name it applies to.
	Service() string
	// Bug says what is wrong with the document and how the server behaves.
	Bug() string
	// Apply patches the document, or returns an error when the bug it fixes
	// is not there (fixed upstream, or the operation is gone).
	Apply(spec *openapi.Spec) error
}

// All is every workaround, in the order they are applied.
var All = []Workaround{
	embyUndeclaredResponses{},
	embyUndeclaredPathParameters{},
	embyNoContentStatus{},
	embyPlaylistCreateUserID{},
	embyLibraryAvailableOptionsQuery{},
	embyNextUpLegacy{},
	embyLibraryDeleteID{},
	embyCommaSeparatedArrays{},
	embyNullResultNoContent{},
	embyOpenAPIDocuments{},
	embyRecordingFoldersQueryResult{},
	embyParentPathText{},
	embyCodecDisplayText{},
	embyPropertyConditionValue{},
	jellyfinCreatePlaylistQuery{},
}

// Apply runs every workaround for a service, logging each, and returns the
// names applied.
func Apply(service string, spec *openapi.Spec, log func(string)) ([]string, error) {
	if log == nil {
		log = func(string) {}
	}
	applied := []string{}
	for _, w := range All {
		if w.Service() != service {
			continue
		}
		if err := w.Apply(spec); err != nil {
			return nil, fmt.Errorf("%s: workaround %s no longer applies, so remove it: %w\n  (it was for: %s)", service, w.Name(), err, w.Bug())
		}
		log(fmt.Sprintf("%s: applied workaround %s", service, w.Name()))
		applied = append(applied, w.Name())
	}

	return applied, nil
}

// operation finds an operation a workaround targets.
func operation(spec *openapi.Spec, method, path string) (*openapi.Operation, error) {
	op := spec.Operation(method, path)
	if op == nil {
		return nil, fmt.Errorf("%s %s is not in the document", method, path)
	}

	return op, nil
}

// param is a parameter a workaround adds.
type param struct {
	name, in, typ, description string
}

func (p param) parameter() *openapi.Parameter {
	return &openapi.Parameter{Name: p.name, In: p.in, Description: p.description, Schema: &openapi.Schema{Type: p.typ}}
}

// addParameters adds undeclared parameters to operations, failing when any
// target already declares one of them.
func addParameters(spec *openapi.Spec, targets []string, params ...param) error {
	for _, target := range targets {
		method, path, _ := strings.Cut(target, " ")
		op, err := operation(spec, method, path)
		if err != nil {
			return err
		}
		for _, p := range params {
			if op.Parameter(p.in, p.name) != nil {
				return fmt.Errorf("%s already declares %s parameter %s", target, p.in, p.name)
			}
			op.Parameters = append(op.Parameters, p.parameter())
		}
	}

	return nil
}

// property finds a component schema's property.
func property(spec *openapi.Spec, schema, name string) (*openapi.Schema, error) {
	s := spec.Components.Schemas[schema]
	if s == nil {
		return nil, fmt.Errorf("no schema %s", schema)
	}
	p := s.Properties[name]
	if p == nil {
		return nil, fmt.Errorf("schema %s has no property %s", schema, name)
	}

	return p, nil
}
