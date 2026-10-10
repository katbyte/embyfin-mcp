package embyfin

import "context"

// LibrarySettings is one library and every setting the server keeps for it.
type LibrarySettings struct {
	ID             string
	Name           string
	CollectionType string
	// Options is every setting, by the server's own name for it and as the
	// server sent it: a switch, a number that keeps its digits
	// (json.Number), a word or a list. What the server keeps for each kind
	// of item in the library - what fetches its metadata and images, and in
	// what order - is named "<Kind>.<Setting>" (Movie.MetadataFetchers). A
	// setting the server sent no value for is left out, and so are the
	// library's folders (PathInfos): they are its locations, and on Emby
	// carry the account a share is reached with.
	Options map[string]any
}

// libraryFolder is a library as either server lists it, of which only what
// names it and its settings is read.
type libraryFolder struct {
	Name           string         `json:"Name"`
	ItemID         string         `json:"ItemId"`
	CollectionType string         `json:"CollectionType"`
	LibraryOptions map[string]any `json:"LibraryOptions"`
}

// LibrarySettings reads every library's settings as the server's own JSON
// and not into the typed client's model, which knows only the settings of
// the version it was generated from: a newer server's other settings would
// not be listed.
func (c *Client) LibrarySettings(ctx context.Context) ([]LibrarySettings, error) {
	var folders []libraryFolder
	if c.isEmby() {
		var page struct {
			Items []libraryFolder `json:"Items"`
		}
		if err := c.document(ctx, "/Library/VirtualFolders/Query", "the server's libraries", &page); err != nil {
			return nil, err
		}
		folders = page.Items
	} else if err := c.document(ctx, "/Library/VirtualFolders", "the server's libraries", &folders); err != nil {
		return nil, err
	}

	libraries := make([]LibrarySettings, 0, len(folders))
	for _, f := range folders {
		libraries = append(libraries, LibrarySettings{ID: f.ItemID, Name: f.Name, CollectionType: f.CollectionType, Options: librarySettings(f.LibraryOptions)})
	}

	return libraries, nil
}

// librarySettings lays a library's options out flat: each as the server
// sent it, the ones kept for each kind of item under the kind's name, and
// the folders left out.
func librarySettings(options map[string]any) map[string]any {
	out := make(map[string]any, len(options))
	for name, value := range options {
		switch {
		case value == nil, name == "PathInfos":
		case name == "TypeOptions":
			kinds, ok := value.([]any)
			if !ok {
				continue
			}
			for _, k := range kinds {
				kind, ok := k.(map[string]any)
				if !ok {
					continue
				}
				of, ok := kind["Type"].(string)
				if !ok || of == "" {
					continue
				}
				for setting, v := range kind {
					if setting != "Type" && v != nil {
						out[of+"."+setting] = v
					}
				}
			}
		default:
			out[name] = value
		}
	}

	return out
}
