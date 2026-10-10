package embyfin

import (
	"reflect"
	"testing"
)

// A library's settings are laid out flat: each as the server sent it, the
// ones kept for a kind of item under the kind's name, and neither the
// folders, which on Emby carry a share's account, nor a setting with no
// value.
func TestLibrarySettingsAreLaidOutFlat(t *testing.T) {
	t.Parallel()

	got := librarySettings(map[string]any{
		"SaveLocalMetadata": true,
		"MetadataSavers":    []any{"Nfo"},
		"NotSet":            nil,
		"PathInfos":         []any{map[string]any{"Path": "/zz", "Username": "quux", "Password": "sekrit"}},
		"TypeOptions": []any{
			map[string]any{"Type": "Movie", "MetadataFetchers": []any{"Quux DB"}, "ImageFetchers": nil},
			map[string]any{"MetadataFetchers": []any{"a kind with no name"}},
			"not a group of settings",
		},
	})
	want := map[string]any{"SaveLocalMetadata": true, "MetadataSavers": []any{"Nfo"}, "Movie.MetadataFetchers": []any{"Quux DB"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("librarySettings = %v\nwant %v", got, want)
	}
	if got := librarySettings(nil); len(got) != 0 {
		t.Errorf("a library with no settings = %v", got)
	}
}
