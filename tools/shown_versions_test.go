package tools

import (
	"maps"
	"net/http"
	"strings"
	"testing"
)

// fileItem is one file an Emby stores as an item of its own: a film, whose
// one version is the file. A nil video is a file never probed.
func fileItem(id, name string, year int, path string, video map[string]any, extra ...map[string]any) map[string]any {
	streams := []map[string]any{}
	runtime := int64(60)
	if video != nil {
		video = maps.Clone(video)
		if s, ok := video["seconds"].(int); ok {
			runtime = int64(s)
			delete(video, "seconds")
		}
		streams = append(streams, video)
	}
	streams = append(streams, extra...)
	size := 5
	if len(streams) == 0 {
		size, runtime = 0, 0
	}

	return map[string]any{
		"Id": id, "Name": name, "Type": "Movie", "ProductionYear": year, "Path": path, "LocationType": "FileSystem",
		"ProviderIds": map[string]any{"Tmdb": "t-" + strings.ToLower(strings.ReplaceAll(name, " ", "-"))}, "RunTimeTicks": runtime * ticksPerSecond,
		"MediaSources": []map[string]any{{"Path": path, "Size": size, "RunTimeTicks": runtime * ticksPerSecond, "MediaStreams": streams}},
	}
}

// picture is a video stream of a size and codec.
func picture(width, height int, codec string) map[string]any {
	return map[string]any{"Type": "Video", "Codec": codec, "Width": width, "Height": height}
}

// running is a picture that runs a number of seconds.
func running(width, height, seconds int) map[string]any {
	p := picture(width, height, "h264")
	p["seconds"] = seconds

	return p
}

// embyShowing is a canned Emby holding each group's films as the items it
// stores, one a file, and showing each group as one item: the first file is
// the one listed in a user's view, and the single read of any of them names
// every file as a version, as Emby 4.10 does.
func embyShowing(t *testing.T, groups ...[]map[string]any) *fakeServer {
	t.Helper()

	f, _ := zzyzxServer(t)
	var stored []map[string]any
	listed := make([]map[string]any, 0, len(groups))
	group := map[string][]map[string]any{}
	for _, g := range groups {
		listed = append(listed, g[0])
		for _, it := range g {
			stored = append(stored, it)
			group[text(it["Id"])] = g
		}
	}
	answer := func(all []map[string]any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			rows := all
			if ids := param(r.URL.Query(), "Ids"); ids != "" {
				rows = nil
				for _, it := range all {
					if strings.Contains(","+ids+",", ","+text(it["Id"])+",") {
						rows = append(rows, it)
					}
				}
			}
			writeJSON(t, w, page(rows...))
		}
	}
	f.mux.HandleFunc("GET /Items", answer(stored))
	f.mux.HandleFunc("GET /Users/u1/Items", answer(listed))
	f.mux.HandleFunc("GET /Users/u1/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		g := group[r.PathValue("id")]
		if g == nil {
			http.NotFound(w, r)
			return
		}
		var read map[string]any
		sources := []map[string]any{}
		for _, it := range g {
			if it["Id"] == r.PathValue("id") {
				read = maps.Clone(it)
			}
			own, ok := it["MediaSources"].([]map[string]any)
			if !ok || len(own) != 1 {
				t.Errorf("%v holds no file of its own", it["Id"])
				return
			}
			src := maps.Clone(own[0])
			src["ItemId"] = it["Id"]
			sources = append(sources, src)
		}
		read["MediaSources"] = sources
		writeJSON(t, w, read)
	})

	return f
}

// Emby stores each version of a film as an item of its own and shows them as
// one film only in a user's view: audit_quality judged each stored file on
// its own, and a 360p version beside a 2160p one was a worklist entry the
// description promised it would not be. Judged as shown, the film is its best
// file; the facts that cannot be trusted are still listed file by file.
func TestEmbyQualityJudgesEveryVersion(t *testing.T) {
	t.Parallel()

	blade := []map[string]any{
		fileItem("258", "Blade Runner", 1982, "/zz/films/Blade Runner (1982)/Blade Runner (1982) - 360p.mp4", picture(640, 360, "h264")),
		fileItem("30", "Blade Runner", 1982, "/zz/films/Blade Runner (1982)/Blade Runner (1982) - 2160p.mp4", picture(3840, 2160, "hevc")),
		fileItem("31", "Blade Runner", 1982, "/zz/films/Blade Runner (1982)/Blade Runner (1982) - 1080p.mp4", nil),
	}
	arrival := []map[string]any{fileItem("32", "Arrival", 2016, "/zz/films/Arrival (2016)/Arrival (2016).mp4", picture(640, 360, "h264"))}
	cs := session(t, embyShowing(t, blade, arrival), Options{})

	out := mustCall(t, cs, "audit_quality", map[string]any{"library": "Zzyzx Films"})
	if got := objects(t, out["findings"], "findings"); len(got) != 1 || got[0]["name"] != "Arrival" {
		t.Errorf("findings = %v, want Arrival alone: Blade Runner is shown with a 2160p version", got)
	}
	if n := number(t, out["items_scanned"], "items_scanned"); n != 2 {
		t.Errorf("items_scanned = %d, want the 2 films Emby shows", n)
	}
	// the version never probed is a file whose facts are unknown, whatever
	// the film it is shown as
	unprobed := objects(t, out["unprobed"], "unprobed")
	if len(unprobed) != 1 || unprobed[0]["id"] != "31" || !strings.HasSuffix(text(unprobed[0]["path"]), "1080p.mp4") {
		t.Errorf("unprobed = %v, want the 1080p file", unprobed)
	}
}

// audit_language: any version of an item counts, and on Emby the versions
// are stored apart, so one read alone lacked what another version has.
func TestEmbyLanguageReadsEveryVersion(t *testing.T) {
	t.Parallel()

	japanese := map[string]any{"Type": "Audio", "Language": "jpn"}
	subtitled := map[string]any{"Type": "Subtitle", "Language": "eng"}
	mononoke := []map[string]any{
		fileItem("28", "Princess Mononoke", 1997, "/zz/films/Princess Mononoke (1997)/Princess Mononoke (1997).mp4", picture(640, 360, "mpeg4"), japanese),
		fileItem("40", "Princess Mononoke", 1997, "/zz/films/Princess Mononoke (1997)/Princess Mononoke (1997) - Subtitled.mp4", picture(640, 360, "mpeg4"), japanese, subtitled),
	}
	cs := session(t, embyShowing(t, mononoke), Options{})

	out := mustCall(t, cs, "audit_language", map[string]any{"language": "eng", "find": findUnwatchable})
	if n := number(t, out["total_findings"], "total_findings"); n != 0 {
		t.Errorf("unwatchable in English = %v, want none: a version has English subtitles", out["findings"])
	}
	out = mustCall(t, cs, "audit_language", map[string]any{"language": "eng", "find": findSubtitles})
	if got := objects(t, out["findings"], "findings"); len(got) != 1 || got[0]["id"] != "28" {
		t.Errorf("English subtitles = %v, want the one film, as it is listed", got)
	}
	if n := number(t, out["items_scanned"], "items_scanned"); n != 1 {
		t.Errorf("items_scanned = %d, want the 1 film Emby shows", n)
	}
}

// Two files of one episode Emby shows as its versions are one episode, not
// the same title filed twice in a season.
func TestEmbyEpisodeVersionsAreNotDuplicates(t *testing.T) {
	t.Parallel()

	episode := func(id, name string, number int, path string) map[string]any {
		it := fileItem(id, name, 2022, path, picture(640, 360, "h264"))
		it["Type"], it["SeriesId"], it["SeriesName"], it["ParentIndexNumber"], it["IndexNumber"] = "Episode", "sev", "Severance", 1, number

		return it
	}
	first := []map[string]any{
		episode("65", "Good News About Hell", 1, "/zz/shows/Severance/Season 01/Severance S01E01.mp4"),
		episode("260", "Good News About Hell", 1, "/zz/shows/Severance/Season 01/Severance S01E01 - 720p.mp4"),
	}
	second := []map[string]any{episode("66", "Half Loop", 2, "/zz/shows/Severance/Season 01/Severance S01E02.mp4")}
	cs := session(t, embyShowing(t, first, second), Options{})

	out := mustCall(t, cs, "audit_duplicate_episodes", map[string]any{})
	if n := number(t, out["total_findings"], "total_findings"); n != 0 {
		t.Errorf("duplicate episodes = %v, want none: the two files are one episode's versions", out["groups"])
	}
	if n := number(t, out["items_scanned"], "items_scanned"); n != 2 {
		t.Errorf("items_scanned = %d, want the 2 episodes Emby shows", n)
	}
}

// item_get lists every file the server shows an item in: on Emby only the
// single read in a user's view names them, and the item read by id alone
// carried its own file and nothing of the other - a film matched to another
// film's ids read as that film, one file, and no word of the real one.
func TestItemGetListsEveryVersion(t *testing.T) {
	t.Parallel()

	interstellar := []map[string]any{
		fileItem("33", "Interstellar", 2014, "/zz/films/Interstellar (2014)/Interstellar (2014).mp4", running(1280, 720, 169*60)),
		fileItem("259", "Interstellar", 2014, "/zz/films/The Thirteenth Floor (1999)/The Thirteenth Floor (1999).mp4", running(1920, 1080, 100*60)),
	}
	blade := []map[string]any{
		fileItem("29", "Blade Runner", 1982, "/zz/films/Blade Runner (1982)/Blade Runner (1982) - 1080p.mp4", picture(1920, 1080, "h264")),
		fileItem("30", "Blade Runner", 1982, "/zz/films/Blade Runner (1982)/Blade Runner (1982) - 2160p.mp4", picture(3840, 2160, "hevc")),
	}
	mononoke := fileItem("28", "Princess Mononoke", 1997, "/zz/films/もののけ姫 (1997)/もののけ姫 (1997).mp4", picture(640, 360, "mpeg4"))
	mononoke["OriginalTitle"] = "もののけ姫"
	cs := session(t, embyShowing(t, interstellar, blade, []map[string]any{mononoke}), Options{})

	for _, id := range []string{"33", "259"} {
		out := mustCall(t, cs, "item_get", map[string]any{"id": id})
		versions := objects(t, out["versions"], "versions")
		if len(versions) != 2 || versions[0]["id"] != "33" || versions[1]["id"] != "259" || number(t, versions[1]["runtime_s"], "runtime_s") != 6000 || number(t, versions[1]["height"], "height") != 1080 {
			t.Errorf("item_get %s versions = %v, want both files with their own facts", id, versions)
		}
		if w := text(out["warning"]); !strings.HasPrefix(w, "probably not one film") || !strings.Contains(w, `"The Thirteenth Floor (1999).mp4" is named for "The Thirteenth Floor" (1999), not Interstellar (2014)`) || !strings.Contains(w, "run 169 min and 100 min") {
			t.Errorf("item_get %s warning = %q", id, w)
		}
	}
	out := mustCall(t, cs, "item_get", map[string]any{"id": "29"})
	if versions := objects(t, out["versions"], "versions"); len(versions) != 2 || out["warning"] != nil {
		t.Errorf("two versions of one film = %v, warning %v", versions, out["warning"])
	}
	// a folder in the film's own language is the film, and its title is said
	out = mustCall(t, cs, "item_get", map[string]any{"id": "28"})
	if out["warning"] != nil || out["versions"] != nil || out["original_title"] != "もののけ姫" {
		t.Errorf("a film in its own language's folder = %v", out)
	}
	for _, it := range objects(t, mustCall(t, cs, "library_items", map[string]any{"library": "Zzyzx Films"})["items"], "items") {
		want := any(nil)
		if it["id"] == "28" {
			want = "もののけ姫"
		}
		if it["original_title"] != want {
			t.Errorf("library_items %v original_title = %v, want %v", it["name"], it["original_title"], want)
		}
	}
}

// The versions and duplicates audits say when what they group is probably
// two films on one id rather than one film's copies: a list that reads as
// "keep the best" deletes a film.
func TestVersionsAndDuplicatesWarnOfAnotherFilm(t *testing.T) {
	t.Parallel()

	merged := []map[string]any{
		fileItem("33", "Interstellar", 2014, "/zz/films/Interstellar (2014)/Interstellar (2014).mp4", picture(1280, 720, "h264")),
		fileItem("259", "Interstellar", 2014, "/zz/films/The Thirteenth Floor (1999)/The Thirteenth Floor (1999).mp4", picture(1920, 1080, "h264")),
	}
	blade := []map[string]any{
		fileItem("29", "Blade Runner", 1982, "/zz/films/Blade Runner (1982)/Blade Runner (1982) - 1080p.mp4", picture(1920, 1080, "h264")),
		fileItem("30", "Blade Runner", 1982, "/zz/films/Blade Runner (1982)/Blade Runner (1982) - 2160p.mp4", picture(3840, 2160, "hevc")),
	}
	cs := session(t, embyShowing(t, merged, blade), Options{})
	out := mustCall(t, cs, "audit_multiple_versions", map[string]any{"library": "Zzyzx Films"})
	warned := map[string]string{}
	for _, f := range objects(t, out["findings"], "findings") {
		warned[text(f["name"])] = text(f["warning"])
	}
	if !strings.Contains(warned["Interstellar"], "probably not one film") || warned["Blade Runner"] != "" || len(warned) != 2 {
		t.Errorf("versions warnings = %v, want Interstellar's alone", warned)
	}

	// the same held apart, as Jellyfin holds files in two folders
	copyOf := fileItem("80", "Blade Runner", 1982, "/zz/other/Blade Runner (1982)/Blade Runner (1982).mp4", picture(640, 360, "h264"))
	cs = session(t, embyShowing(t, merged[:1], merged[1:], blade[:1], []map[string]any{copyOf}), Options{})
	groups, ok := mustCall(t, cs, "audit_duplicates", map[string]any{"types": "Movie"})["groups"].([]any)
	if !ok || len(groups) != 2 {
		t.Fatalf("groups = %v", groups)
	}
	for _, g := range groups {
		for _, m := range objects(t, g, "group") {
			w := text(m["warning"])
			if m["name"] == "Interstellar" {
				if !strings.HasPrefix(w, "probably not copies of one film") || !strings.Contains(w, "The Thirteenth Floor") {
					t.Errorf("%v warning = %q", m["path"], w)
				}
			} else if w != "" {
				t.Errorf("two copies of one film warned: %v", m)
			}
		}
	}
}

// quality_compare between two items that may be two films says so as a
// caveat, and still compares.
func TestQualityCompareCaveatsTwoFilms(t *testing.T) {
	t.Parallel()

	matched := fileItem("33", "Interstellar", 2014, "/zz/films/Interstellar (2014)/Interstellar (2014).mp4", running(1280, 720, 169*60))
	other := fileItem("259", "Interstellar", 2014, "/zz/films/The Thirteenth Floor (1999)/The Thirteenth Floor (1999).mp4", running(1920, 1080, 100*60))
	copyOf := fileItem("34", "Interstellar", 2014, "/zz/other/Interstellar (2014)/Interstellar (2014).mp4", running(1920, 1080, 169*60))
	cs := session(t, embyShowing(t, []map[string]any{matched}, []map[string]any{other}, []map[string]any{copyOf}), Options{})

	out := mustCall(t, cs, "quality_compare", map[string]any{"a": map[string]any{"item_id": "33"}, "b": map[string]any{"item_id": "259"}})
	caveats := strings.Join(texts(out["caveats"]), " | ")
	if !strings.Contains(caveats, "these may not be the same film") || !strings.Contains(caveats, "The Thirteenth Floor") || !strings.Contains(caveats, "a different cut, or a different film") {
		t.Errorf("caveats = %q", caveats)
	}
	if out["verdict"] != "b_better" {
		t.Errorf("verdict = %v: a caveat is never the verdict", out["verdict"])
	}
	out = mustCall(t, cs, "quality_compare", map[string]any{"a": map[string]any{"item_id": "33"}, "b": map[string]any{"item_id": "34"}})
	if caveats := strings.Join(texts(out["caveats"]), " | "); strings.Contains(caveats, "same film") {
		t.Errorf("two copies of one film = %q", caveats)
	}
}
