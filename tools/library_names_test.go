package tools

import (
	"net/http"
	"strings"
	"testing"
)

// library_edit renames a library only to a name no other library has, apart
// from case too, as library_create makes one: two libraries a letter's case
// apart are one name to whoever picks a library by it. The library's own
// name in another case is still its own to take.
func TestLibraryEditRefusesAnotherLibrarysName(t *testing.T) {
	t.Parallel()

	f, libs := zzyzxServer(t)
	libs.mu.Lock()
	libs.folders = append(libs.folders, map[string]any{"Name": "Zzyzx Docs", "CollectionType": "movies", "ItemId": "lib10", "Locations": []string{"/zz/docs"}})
	libs.mu.Unlock()
	var renamed []string
	f.mux.HandleFunc("POST /Library/VirtualFolders/Name", func(w http.ResponseWriter, r *http.Request) {
		renamed = append(renamed, r.URL.Query().Get("NewName"))
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("POST /Library/VirtualFolders/Paths", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	cs := session(t, f, Options{})

	for _, name := range []string{"Zzyzx Docs", "zzyzx docs", "ZZYZX DOCS"} {
		msg := mustRefuse(t, cs, "library_edit", map[string]any{"library": "Zzyzx Films", "name": name, "add_paths": []string{"/zz/more"}})
		if !strings.Contains(msg, `a library named "Zzyzx Docs" already exists: rename Zzyzx Films to a name no other library has, apart from case too`) {
			t.Errorf("renaming onto %q = %q", name, msg)
		}
	}
	if n := len(f.requests("/Library/VirtualFolders/Name")) + len(f.requests("/Library/VirtualFolders/Paths")); n != 0 {
		t.Errorf("%d changes were made before the rename was refused, want none", n)
	}

	// its own name in another case is its own
	mustCall(t, cs, "library_edit", map[string]any{"library": "Zzyzx Films", "name": "ZZYZX FILMS"})
	if len(renamed) != 1 {
		t.Errorf("the rename to its own name in capitals was not sent: %v", f.requests("/Library/VirtualFolders/Name"))
	}
}
