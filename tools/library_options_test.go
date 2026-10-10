package tools

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// optionLibraries is three made-up libraries as Emby lists them: two of
// films that differ in two settings and in what fetches a film's metadata,
// the second with a language set where the others have none, and one of
// shows. The first holds what a real one can and no answer may: the account
// its folder's share is reached with.
func optionLibraries() []map[string]any {
	films := func(name, id string, saveLocal bool, interval int, fetchers []string) map[string]any {
		return map[string]any{"Name": name, "ItemId": id, "CollectionType": "movies", "LibraryOptions": map[string]any{
			"ContentType": "movies", "EnableRealtimeMonitor": true, "SaveLocalMetadata": saveLocal, "ThumbnailImagesIntervalSeconds": interval,
			"EnableChapterImageExtraction": false, "MetadataSavers": []string{"Nfo"}, "SettingFromANewerServer": 42, "NotSet": nil,
			"PathInfos":   []map[string]any{{"Path": "/zz/films", "NetworkPath": `\\zzyzx\films`, "Username": "quux", "Password": "sekrit"}},
			"TypeOptions": []map[string]any{{"Type": "Movie", "MetadataFetchers": fetchers, "ImageFetchers": []string{"Zzyzx Images"}, "ImageOptions": []map[string]any{{"Type": "Backdrop", "Limit": 2}}}},
		}}
	}

	shorts := films("Zzyzx Shorts", "lib2", false, -1, []string{"Plugh DB", "Quux DB"})
	if options, ok := shorts["LibraryOptions"].(map[string]any); ok {
		options["PreferredMetadataLanguage"] = "en"
	}

	return []map[string]any{
		films("Zzyzx Films", "lib1", true, 10, []string{"Quux DB", "Plugh DB"}),
		shorts,
		{"Name": "Zzyzx Shows", "ItemId": "lib3", "CollectionType": "tvshows", "LibraryOptions": map[string]any{
			"ContentType": "tvshows", "EnableRealtimeMonitor": true, "SaveLocalMetadata": true, "ThumbnailImagesIntervalSeconds": 10,
			"EnableChapterImageExtraction": false, "MetadataSavers": []string{"Nfo"}, "SettingFromANewerServer": 42,
			"TypeOptions": []map[string]any{{"Type": "Series", "MetadataFetchers": []string{"Quux DB"}}},
		}},
	}
}

func optionServer(t *testing.T) *fakeServer {
	t.Helper()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(optionLibraries()...))
	})

	return f
}

// optionRows is a library_options answer's rows by setting.
func optionRows(t *testing.T, out map[string]any) (names []string, by map[string]map[string]any) {
	t.Helper()

	by = map[string]map[string]any{}
	for _, row := range objects(t, out["options"], "options") {
		names = append(names, text(row["option"]))
		by[text(row["option"])] = row
	}

	return names, by
}

// Every setting the server sends is a row: one value where the libraries
// agree, each library's where they differ, the differing ones first. A
// library with no value for a setting another has differs from it. What is
// kept for a kind of item is named by the kind, and had only by the
// libraries of that kind. A share's account, a library's folders and its
// kind are not settings and are not given.
func TestLibraryOptionsSideBySide(t *testing.T) {
	t.Parallel()

	out := mustCall(t, session(t, optionServer(t), Options{}), "library_options", map[string]any{})
	names, rows := optionRows(t, out)
	libraries := objects(t, out["libraries"], "libraries")
	if out["backend"] != "emby" || len(libraries) != 3 || libraries[0]["name"] != "Zzyzx Films" || libraries[0]["id"] != "lib1" || libraries[2]["type"] != "tvshows" {
		t.Fatalf("libraries = %v", out["libraries"])
	}
	if number(t, out["differing"], "differing") != 4 || number(t, out["alike"], "alike") != 7 || len(names) != 11 {
		t.Errorf("%v differing, %v alike, rows %v", out["differing"], out["alike"], names)
	}
	// the ones that differ lead, by name, then the rest by name
	if want := []string{"Movie.MetadataFetchers", "PreferredMetadataLanguage", "SaveLocalMetadata", "ThumbnailImagesIntervalSeconds", "EnableChapterImageExtraction", "EnableRealtimeMonitor", "MetadataSavers", "Movie.ImageFetchers", "Movie.ImageOptions", "Series.MetadataFetchers", "SettingFromANewerServer"}; !slices.Equal(names, want) {
		t.Errorf("rows = %v\nwant   %v", names, want)
	}

	if values := object(t, rows["SaveLocalMetadata"]["values"], "values"); len(values) != 3 || !boolean(t, values["Zzyzx Films"], "Zzyzx Films") || boolean(t, values["Zzyzx Shorts"], "Zzyzx Shorts") || !boolean(t, values["Zzyzx Shows"], "Zzyzx Shows") || rows["SaveLocalMetadata"]["value"] != nil {
		t.Errorf("a setting they differ on = %v", rows["SaveLocalMetadata"])
	}
	// a list in another order is another setting: the order is the order asked
	if values := object(t, rows["Movie.MetadataFetchers"]["values"], "values"); len(values) != 2 || !slices.Equal(texts(values["Zzyzx Films"]), []string{"Quux DB", "Plugh DB"}) || !slices.Equal(texts(values["Zzyzx Shorts"]), []string{"Plugh DB", "Quux DB"}) {
		t.Errorf("fetchers in two orders = %v", rows["Movie.MetadataFetchers"])
	}
	// one library has a language set and the others none: that is a difference, and says who has none
	if row := rows["PreferredMetadataLanguage"]; object(t, row["values"], "values")["Zzyzx Shorts"] != "en" || len(object(t, row["values"], "values")) != 1 || !slices.Equal(texts(row["unset"]), []string{"Zzyzx Films", "Zzyzx Shows"}) {
		t.Errorf("a setting one library has and the others have no value for = %v", row)
	}
	if row := rows["EnableChapterImageExtraction"]; boolean(t, row["value"], "value") || row["values"] != nil || row["only"] != nil || row["unset"] != nil {
		t.Errorf("a switch every library has off = %v", row)
	}
	// a library of shows has no setting for films, and that is no difference
	if row := rows["Movie.ImageFetchers"]; !slices.Equal(texts(row["value"]), []string{"Zzyzx Images"}) || !slices.Equal(texts(row["only"]), []string{"Zzyzx Films", "Zzyzx Shorts"}) || row["unset"] != nil {
		t.Errorf("a setting for films = %v", row)
	}
	if row := rows["Series.MetadataFetchers"]; !slices.Equal(texts(row["only"]), []string{"Zzyzx Shows"}) {
		t.Errorf("a setting for series = %v", row)
	}
	// a setting this client has never heard of is listed like any other
	if row := rows["SettingFromANewerServer"]; row["value"] != 42.0 {
		t.Errorf("a newer server's setting = %v", row)
	}
	for _, never := range []string{"sekrit", "quux", "PathInfos", "NetworkPath", "ContentType", "NotSet"} {
		if strings.Contains(string(mustJSON(t, out)), never) {
			t.Errorf("the answer holds %q: %v", never, out)
		}
	}
}

// The table is narrowed to the libraries and the settings asked for, and to
// the ones that differ; differ_from says which settings each other library
// has differently from one, of those both have.
func TestLibraryOptionsNarrowed(t *testing.T) {
	t.Parallel()

	cs := session(t, optionServer(t), Options{})

	out := mustCall(t, cs, "library_options", map[string]any{"differing": true})
	if names, _ := optionRows(t, out); !slices.Equal(names, []string{"Movie.MetadataFetchers", "PreferredMetadataLanguage", "SaveLocalMetadata", "ThumbnailImagesIntervalSeconds"}) || number(t, out["alike"], "alike") != 7 {
		t.Errorf("only the differing = %v, alike %v", names, out["alike"])
	}

	// by id and by name in any case, in the server's order whatever order they were asked in
	out = mustCall(t, cs, "library_options", map[string]any{"libraries": []any{"zzyzx shows", "lib1"}, "options": []any{"savelocal", "FETCHERS"}})
	names, rows := optionRows(t, out)
	if libraries := objects(t, out["libraries"], "libraries"); len(libraries) != 2 || libraries[0]["name"] != "Zzyzx Films" || libraries[1]["name"] != "Zzyzx Shows" {
		t.Errorf("two libraries asked for = %v", out["libraries"])
	}
	if !slices.Equal(names, []string{"Movie.ImageFetchers", "Movie.MetadataFetchers", "SaveLocalMetadata", "Series.MetadataFetchers"}) || !boolean(t, rows["SaveLocalMetadata"]["value"], "value") || number(t, out["differing"], "differing") != 0 {
		t.Errorf("two libraries, two kinds of setting = %v", out["options"])
	}

	// held against one: the others by what they have differently, a value where it has none included, and a library of another kind by the settings both could have
	out = mustCall(t, cs, "library_options", map[string]any{"differ_from": "Zzyzx Films", "differing": true})
	differ := object(t, out["differ_from"], "differ_from")
	others := objects(t, differ["others"], "others")
	if differ["library"] != "Zzyzx Films" || len(others) != 2 || others[0]["library"] != "Zzyzx Shorts" || !slices.Equal(texts(others[0]["differs"]), []string{"Movie.MetadataFetchers", "PreferredMetadataLanguage", "SaveLocalMetadata", "ThumbnailImagesIntervalSeconds"}) || others[1]["library"] != "Zzyzx Shows" || len(texts(others[1]["differs"])) != 0 || others[1]["differs"] == nil {
		t.Errorf("differ_from = %v", differ)
	}
	// the one held against is compared even when it was not among those named
	out = mustCall(t, cs, "library_options", map[string]any{"libraries": []any{"Zzyzx Shorts"}, "differ_from": "lib1", "options": []any{"SaveLocal"}})
	if libraries := objects(t, out["libraries"], "libraries"); len(libraries) != 2 || !slices.Equal(texts(objects(t, object(t, out["differ_from"], "differ_from")["others"], "others")[0]["differs"]), []string{"SaveLocalMetadata"}) {
		t.Errorf("held against one not named = %v", out)
	}

	out = mustCall(t, cs, "library_options", map[string]any{"libraries": []any{"lib3"}, "differing": true})
	if names, _ := optionRows(t, out); len(names) != 0 || !strings.Contains(text(out["note"]), "one library compared") {
		t.Errorf("one library = %v, note %q", names, out["note"])
	}
	out = mustCall(t, cs, "library_options", map[string]any{"options": []any{"trickplay"}})
	if names, _ := optionRows(t, out); len(names) != 0 || !strings.Contains(text(out["note"]), "no setting's name holds any of trickplay") {
		t.Errorf("a setting this server has none of = %v, note %q", names, out["note"])
	}
	for want, args := range map[string]map[string]any{
		`no library named "nope" or with that id: the server has Zzyzx Films, Zzyzx Shorts, Zzyzx Shows`: {"libraries": []any{"nope"}},
		`differ_from: no library named "nope"`: {"differ_from": "nope"},
	} {
		if msg := mustRefuse(t, cs, "library_options", args); !strings.Contains(msg, want) {
			t.Errorf("%v refused with %q, want %q", args, msg, want)
		}
	}
}

// Jellyfin lists its libraries at another address and as a plain list, and
// its settings are read the same way. A library of films with no fetchers
// listed for a film uses the server's own, which is not what one with a list
// does: the two differ, and a library of shows is not held against either.
func TestLibraryOptionsOnJellyfin(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.jellyfin = true
	f.mux.HandleFunc("GET /Library/VirtualFolders", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{
			{"Name": "Zzyzx Films", "ItemId": "0f0f", "CollectionType": "movies", "LibraryOptions": map[string]any{"EnableTrickplayImageExtraction": true, "SaveTrickplayWithMedia": false, "TypeOptions": []map[string]any{{"Type": "Movie", "SimilarItemProviders": []string{}}}}},
			{"Name": "Zzyzx Shorts", "ItemId": "0d0d", "CollectionType": "movies", "LibraryOptions": map[string]any{"EnableTrickplayImageExtraction": false, "SaveTrickplayWithMedia": false, "TypeOptions": []map[string]any{}}},
			{"Name": "Zzyzx Shows", "ItemId": "0e0e", "CollectionType": "tvshows", "LibraryOptions": map[string]any{"EnableTrickplayImageExtraction": false, "SaveTrickplayWithMedia": false}},
		})
	})
	out := mustCall(t, session(t, f, Options{}), "library_options", map[string]any{"options": []any{"trickplay", "similar"}, "differ_from": "Zzyzx Shorts"})
	names, rows := optionRows(t, out)
	if out["backend"] != "jellyfin" || !slices.Equal(names, []string{"EnableTrickplayImageExtraction", "Movie.SimilarItemProviders", "SaveTrickplayWithMedia"}) || boolean(t, object(t, rows["EnableTrickplayImageExtraction"]["values"], "values")["Zzyzx Shows"], "Zzyzx Shows") || boolean(t, rows["SaveTrickplayWithMedia"]["value"], "value") {
		t.Errorf("on Jellyfin = %v", out)
	}
	if row := rows["Movie.SimilarItemProviders"]; len(object(t, row["values"], "values")) != 1 || len(texts(object(t, row["values"], "values")["Zzyzx Films"])) != 0 || !slices.Equal(texts(row["unset"]), []string{"Zzyzx Shorts"}) {
		t.Errorf("fetchers listed as none on one library of films and not listed on the other = %v", row)
	}
	others := objects(t, object(t, out["differ_from"], "differ_from")["others"], "others")
	if len(others) != 2 || others[0]["library"] != "Zzyzx Films" || !slices.Equal(texts(others[0]["differs"]), []string{"EnableTrickplayImageExtraction", "Movie.SimilarItemProviders"}) || others[1]["library"] != "Zzyzx Shows" || len(texts(others[1]["differs"])) != 0 {
		t.Errorf("held against the one with none listed = %v", others)
	}
}
