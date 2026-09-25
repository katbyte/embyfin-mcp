//go:build integration

package acceptance

import (
	"reflect"
	"strings"
	"testing"
)

// library_edit renames a library only to a name no other library has, apart
// from case too, as library_create makes one: a rename of Music onto Movies,
// or onto movies, is refused before anything changes.
func TestALibraryRenamedOntoAnother(t *testing.T) {
	list := func() []map[string]any {
		t.Helper()
		return rows(t, call(t, "library_list", nil)["libraries"], "libraries")
	}
	before := list()
	for _, name := range []string{"Movies", "movies", "MOVIES"} {
		msg := callErr(t, "library_edit", map[string]any{"library": "Music", "name": name, "save_nfo": false})
		if !strings.Contains(msg, `a library named "Movies" already exists: rename Music to a name no other library has, apart from case too`) {
			t.Errorf("renaming Music onto %q = %q", name, msg)
		}
	}
	if after := list(); !reflect.DeepEqual(after, before) {
		t.Errorf("the refused renames changed the libraries:\nbefore %v\nafter  %v", before, after)
	}
}
