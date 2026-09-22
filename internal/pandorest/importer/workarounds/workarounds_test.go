package workarounds

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/internal/pandorest/config"
	"github.com/katbyte/embyfin-mcp/internal/pandorest/openapi"
)

// loadSpec reads a service's vendored document.
func loadSpec(t *testing.T, service string) *openapi.Spec {
	t.Helper()

	cfg, ok := config.Find(service)
	if !ok {
		t.Fatalf("no service %q", service)
	}
	// tests run in the package directory, four below the repository root
	cfg, err := cfg.In(filepath.Join("..", "..", "..", "..")).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := openapi.Load(cfg.Path(cfg.Spec))
	if err != nil {
		t.Fatal(err)
	}

	return spec
}

// Every workaround fixes a bug that is in its vendored document, and fails
// once the bug is gone. Applying a workaround to the document it already
// fixed is the cheapest stand-in for a fixed upstream spec.
func TestWorkaroundsApplyOnceThenFail(t *testing.T) {
	t.Parallel()

	for _, w := range All {
		t.Run(w.Name(), func(t *testing.T) {
			t.Parallel()

			if _, ok := config.Find(w.Service()); !ok {
				t.Fatalf("service %q is not configured", w.Service())
			}
			if w.Bug() == "" || !strings.HasPrefix(w.Name(), w.Service()+"-") {
				t.Errorf("name %q or bug %q does not describe the workaround", w.Name(), w.Bug())
			}
			spec := loadSpec(t, w.Service())
			if err := w.Apply(spec); err != nil {
				t.Fatalf("the bug is not in the vendored document: %v", err)
			}
			if err := w.Apply(spec); err == nil {
				t.Error("applying it again succeeded, so it would not notice the bug being fixed")
			}
		})
	}
}

// A refreshed document that drops the 200 a workaround reads must get an
// error naming the workaround, as the README promises, not a nil-pointer
// panic from the middle of the import.
func TestWorkaroundsReportAMissing200(t *testing.T) {
	t.Parallel()

	for _, w := range All {
		t.Run(w.Name(), func(t *testing.T) {
			t.Parallel()

			spec := loadSpec(t, w.Service())
			for _, item := range spec.Paths {
				for _, m := range item.Methods() {
					delete(m.Operation.Responses, "200")
				}
			}
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panicked on a document with no 200 responses: %v", r)
				}
			}()
			_ = w.Apply(spec) // an error or a no-op: the point is that it returns
		})
	}
}

func TestApply(t *testing.T) {
	t.Parallel()

	spec := loadSpec(t, "jellyfin")
	var logged []string
	applied, err := Apply("jellyfin", spec, func(s string) { logged = append(logged, s) })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"jellyfin-create-playlist-query"}
	if !slices.Equal(applied, want) || len(logged) != len(want) {
		t.Errorf("applied %v (logged %v), want %v", applied, logged, want)
	}

	// a second pass over the patched document fails and names the workaround
	_, err = Apply("jellyfin", spec, nil)
	if err == nil || !strings.Contains(err.Error(), "workaround jellyfin-create-playlist-query no longer applies, so remove it") {
		t.Errorf("second Apply = %v", err)
	}

	if applied, err := Apply("nothing", spec, nil); err != nil || len(applied) != 0 {
		t.Errorf("Apply for a service without workarounds = %v, %v", applied, err)
	}
}

func TestNames(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, w := range All {
		if seen[w.Name()] {
			t.Errorf("two workarounds are named %s", w.Name())
		}
		seen[w.Name()] = true
	}
}

// A TMDB request body's shape is read off its example: whole numbers are
// integers, anything else numeric a number.
func TestSchemaOf(t *testing.T) {
	t.Parallel()

	s, err := schemaOf([]byte(`{"media_type": "movie", "media_id": 550, "favorite": true, "value": 8.5, "ids": [1, 2], "none": null}`))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for name, p := range s.Properties {
		got[name] = p.Type
		if p.Items != nil {
			got[name] += " of " + p.Items.Type
		}
	}
	want := map[string]string{"media_type": "string", "media_id": "integer", "favorite": "boolean", "value": "number", "ids": "array of integer", "none": ""}
	if s.Type != openapi.TypeObject || len(got) != len(want) {
		t.Fatalf("schema = %+v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}
