package tools

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
)

// Answers the read tools used to give with confidence and get wrong, against
// the canned servers: an episode the server holds no numbers for read as a
// special, a file folded into another read as not in the library, and a row
// whose facts were another file's than the one its path named.

// unnumberedSeverance is Severance with a file named without SxxEyy, which
// Jellyfin holds with no season or episode number even in a season folder,
// and an extra Emby took for an episode: season 0, no number of its own.
func unnumberedSeverance() *fakeSeries {
	s := severance()
	s.episodes = append(s.episodes,
		ep{season: 1, number: 90, name: "Severance - Unnumbered", path: "/media/shows/Severance/Season 01/Severance - Unnumbered.mkv", noSeason: true, noNumber: true},
		ep{season: 0, number: 91, name: "Featurette", path: "/media/shows/Severance/Season 01/Extras/Featurette.mkv", noNumber: true},
	)

	return s
}

func TestAnEpisodeWithNoNumbersIsNotASpecial(t *testing.T) {
	t.Parallel()

	for _, jellyfin := range []bool{false, true} {
		cs := session(t, tvServerFor(t, jellyfin, unnumberedSeverance()), Options{})

		// a number the server does not hold reads as none, not as 0
		rows := objects(t, mustCall(t, cs, "library_episodes", map[string]any{"series_id": "sev"})["episodes"], "episodes")
		byPath := map[string]map[string]any{}
		for _, row := range rows {
			byPath[text(row["path"])] = row
		}
		if row := byPath["/media/shows/Severance/Season 01/Severance - Unnumbered.mkv"]; row == nil || row["season"] != nil || row["episode"] != nil {
			t.Errorf("jellyfin %v: the unnumbered file = %v, want no season and no episode", jellyfin, row)
		}
		if row := byPath["/media/shows/Severance/Season 01/Extras/Featurette.mkv"]; row == nil || number(t, row["season"], "season") != 0 || row["episode"] != nil {
			t.Errorf("jellyfin %v: the featurette = %v, want season 0 and no episode", jellyfin, row)
		}

		// one season holds none of them, and says the show holds files no
		// season's list includes
		season := mustCall(t, cs, "library_episodes", map[string]any{"series_id": "sev", "season": 1})
		for _, row := range objects(t, season["episodes"], "episodes") {
			if row["season"] == nil || number(t, row["season"], "season") != 1 {
				t.Errorf("jellyfin %v: season 1 lists %v", jellyfin, row)
			}
		}
		if w := text(season["warning"]); !strings.Contains(w, "Severance - Unnumbered.mkv") || !strings.Contains(w, "no season's list includes") {
			t.Errorf("jellyfin %v: season 1's warning = %q, want it naming the unnumbered file", jellyfin, w)
		}

		// an absence beside files with no numbers is not proof: any of them
		// may be the episode
		exists := mustCall(t, cs, "show_episodes_exist", map[string]any{"series_id": "sev", "episodes": []map[string]any{{"season": 1, "episode": 5}}})
		if row := objects(t, exists["episodes"], "episodes")[0]; boolean(t, row["exists"], "exists") {
			t.Errorf("jellyfin %v: S01E05 = %v, want absent", jellyfin, row)
		}
		if w := text(exists["warning"]); !strings.Contains(w, "Severance - Unnumbered.mkv") || !strings.Contains(w, "not proof") {
			t.Errorf("jellyfin %v: the warning = %q, want it naming the unnumbered file", jellyfin, w)
		}
		// and nothing is claimed of an episode held by number
		held := mustCall(t, cs, "show_episodes_exist", map[string]any{"series_id": "sev", "episodes": []map[string]any{{"season": 1, "episode": 1}}})
		if held["warning"] != nil || !boolean(t, objects(t, held["episodes"], "episodes")[0]["exists"], "exists") {
			t.Errorf("jellyfin %v: S01E01 = %v, want held with no warning", jellyfin, held)
		}

		// the unnumbered files are not special 0 to show_missing, and are named
		missing := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"})
		if got := texts(missing["unnumbered_files"]); len(got) != 2 {
			t.Errorf("jellyfin %v: unnumbered_files = %v, want both", jellyfin, got)
		}
	}
}

// Jellyfin folds a second file of one episode in one folder into the
// episode. Its path is in the episode's media sources and nowhere else, so a
// row naming the first file alone read the second as not in the library -
// and its facts were the taller file's, beside the other's path.
func TestAFileFoldedIntoAnEpisodeIsListed(t *testing.T) {
	t.Parallel()

	s := severance()
	// its own file a 480p one, and the version folded into it 720p
	s.episodes[0].width, s.episodes[0].height = 854, 480
	s.episodes[0].alt = "/media/shows/Severance/Season 01/S01E01 - 720p.mkv"
	cs := session(t, tvServerJellyfin(t, s), Options{})

	for _, fields := range [][]string{nil, {"path", "height"}, {"path"}} {
		args := map[string]any{"series_id": "sev"}
		if fields != nil {
			args["fields"] = fields
		}
		row := objects(t, mustCall(t, cs, "library_episodes", args)["episodes"], "episodes")[0]
		if text(row["path"]) != s.episodes[0].path {
			t.Fatalf("fields %v: the row = %v", fields, row)
		}
		// the facts are the file the path names
		if fields == nil || slices.Contains(fields, "height") {
			if number(t, row["height"], "height") != 480 {
				t.Errorf("fields %v: the row's height = %v beside its 480p path", fields, row["height"])
			}
		}
		versions := objects(t, row["versions"], "versions")
		paths := make([]string, 0, len(versions))
		for _, v := range versions {
			paths = append(paths, text(v["path"]))
		}
		if !slices.Equal(paths, []string{s.episodes[0].path, s.episodes[0].alt}) || text(versions[1]["id"]) != "sev-1-1-alt" {
			t.Errorf("fields %v: versions = %v, want both files", fields, versions)
		}
		if slices.Contains(fields, "height") && number(t, versions[1]["height"], "height") != 720 {
			t.Errorf("fields %v: the folded file's height = %v", fields, versions[1]["height"])
		}
	}

	// asked whether the episode is held, the folded file is named beside it
	// as a version of it, with or without its facts
	for _, quality := range []bool{false, true} {
		row := objects(t, mustCall(t, cs, "show_episodes_exist", map[string]any{"series_id": "sev", "episodes": []map[string]any{{"season": 1, "episode": 1}}, "quality": quality})["episodes"], "episodes")[0]
		copies := objects(t, row["other_copies"], "other_copies")
		if len(copies) != 1 || text(copies[0]["path"]) != s.episodes[0].alt || text(copies[0]["version_of"]) != "sev-1-1" || text(copies[0]["id"]) != "sev-1-1-alt" {
			t.Errorf("quality %v: other copies = %v, want the 720p version of sev-1-1", quality, copies)
		}
		if quality && (number(t, row["height"], "height") != 480 || number(t, copies[0]["height"], "height") != 720) {
			t.Errorf("the held copy = %vp beside a %vp version, want 480 and 720", row["height"], copies[0]["height"])
		}
	}
}

// item_get lists every director, writer and creator however long the cast:
// Jellyfin lists the cast first, so the first fifteen people were all cast.
func TestCreditedPeopleKeepTheCrew(t *testing.T) {
	t.Parallel()

	people := make([]embyfin.Person, 0, 24)
	for i := range 20 {
		people = append(people, embyfin.Person{Name: "Actor " + string(rune('A'+i)), Type: "Actor"})
	}
	people = append(people,
		embyfin.Person{Name: "A Producer", Type: "Producer"},
		embyfin.Person{Name: "The Director", Type: "Director"},
		embyfin.Person{Name: "The Writer", Type: "Writer"},
		embyfin.Person{Name: "A Guest", Type: "GuestStar"},
	)
	got := creditedPeople(people)
	if len(got) != peopleShown {
		t.Fatalf("%d people, want %d", len(got), peopleShown)
	}
	if got[0].Name != "The Director" || got[1].Name != "The Writer" {
		t.Errorf("the crew = %v, want the director and the writer first", got[:2])
	}
	for _, p := range got[2:] {
		if p.Type != "Actor" {
			t.Errorf("%v is among the cast", p)
		}
	}
	if got[2].Name != "Actor A" || got[len(got)-1].Name != "Actor M" {
		t.Errorf("the cast = %v, want the top-billed in order", got[2:])
	}
	// a short list is kept whole, less the producer
	short := creditedPeople(people[len(people)-4:])
	if len(short) != 3 || slices.ContainsFunc(short, func(p embyfin.Person) bool { return p.Type == "Producer" }) {
		t.Errorf("a short list = %v, want the director, the writer and the guest", short)
	}
	// a song's composer and lyricist are its crew
	song := creditedPeople([]embyfin.Person{{Name: "A Composer", Type: "Composer"}, {Name: "A Lyricist", Type: "Lyricist"}, {Name: "An Engineer", Type: "Engineer"}})
	if len(song) != 2 || song[0].Type != "Composer" || song[1].Type != "Lyricist" {
		t.Errorf("a song's people = %v, want its composer and lyricist", song)
	}
	// and every crew credit is kept however many, with no cast past the cut
	crew := make([]embyfin.Person, 0, 21)
	for i := range 20 {
		crew = append(crew, embyfin.Person{Name: fmt.Sprintf("Writer %d", i), Type: "Writer"})
	}
	if got := creditedPeople(append(crew, embyfin.Person{Name: "A Star", Type: "Actor"})); len(got) != 20 || slices.ContainsFunc(got, func(p embyfin.Person) bool { return p.Type == "Actor" }) {
		t.Errorf("twenty writers and a star = %d people, want the twenty writers", len(got))
	}
}

// server_log with no name reads the server's own log, not whichever file
// changed last: that is as often a playback's transcode log.
func TestServerLogReadsTheServersOwn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		files []embyfin.LogFile
		want  string
		own   bool
	}{
		{"emby", []embyfin.LogFile{
			{Name: "embyserver-63925916347.txt", DateModified: "2026-09-24T01:00:00Z"},
			{Name: "embyserver.txt", DateModified: "2026-09-25T06:00:00Z"},
			{Name: "ffmpeg-transcode-0badc0de.txt", DateModified: "2026-09-25T07:00:00Z"},
		}, "embyserver.txt", true},
		{"jellyfin", []embyfin.LogFile{
			{Name: "log_20260924.log", DateModified: "2026-09-24T23:59:00Z"},
			{Name: "FFmpeg.Transcode-2026-09-25_0badc0de.log", DateModified: "2026-09-25T07:00:00Z"},
			{Name: "log_20260925.log", DateModified: "2026-09-25T06:00:00Z"},
		}, "log_20260925.log", true},
		{"neither", []embyfin.LogFile{
			{Name: "a.txt", DateModified: "2026-09-24T01:00:00Z"},
			{Name: "b.txt", DateModified: "2026-09-25T01:00:00Z"},
		}, "b.txt", false},
	} {
		if got, own := serverLog(tc.files); got != tc.want || own != tc.own {
			t.Errorf("%s: %s (own %v), want %s (own %v)", tc.name, got, own, tc.want, tc.own)
		}
	}
}

// An episode playing is named with its show and number: its own title alone
// is one of a dozen Pilots.
func TestSessionListNamesTheShow(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{{
			"Id": "s1", "Client": "Zzyzx Player", "DeviceName": "Lounge TV",
			"NowPlayingItem": map[string]any{"Id": "e1", "Name": "Good News About Hell", "Type": "Episode", "SeriesName": "Severance", "ParentIndexNumber": 1, "IndexNumber": 1, "RunTimeTicks": 600_000_000},
		}})
	})
	cs := session(t, f, Options{})

	row := objects(t, mustCall(t, cs, "session_list", map[string]any{})["sessions"], "sessions")[0]
	if row["now_playing"] != "Severance S01E01 Good News About Hell" || row["now_playing_id"] != "e1" {
		t.Errorf("playing = %v", row)
	}
}

// Audio languages are compared by one spelling: Emby writes English "en",
// Jellyfin and most files "eng", and compared as written two English copies
// each read as carrying a language the other lacks.
func TestQualityCompareReadsOneLanguageOneWay(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})
	out := mustCall(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"width": 1920, "height": 1080, "bitrate": 6000000, "audio": []map[string]any{{"language": "en", "codec": "aac", "channels": 2}}},
		"b": map[string]any{"width": 1920, "height": 1080, "bitrate": 6000000, "audio": []map[string]any{{"language": "eng", "codec": "aac", "channels": 2}, {"language": "ger", "codec": "aac", "channels": 2}}},
	})
	audio, ok := out["audio"].(map[string]any)
	if !ok {
		t.Fatalf("no audio dimension: %v", out)
	}
	if audio["only_a"] != nil || !slices.Equal(texts(audio["only_b"]), []string{"deu"}) {
		t.Errorf("audio = %v, want English on both and German on b alone", audio)
	}
}

// Jellyfin lists a version folded into a film by an id of its own, in
// versions and other_copies, which no item query finds and its single read,
// image list, subtitle search and similar items all answer for. The tools
// that read one item take that id as the item it is, and still refuse an id
// nothing has.
func TestAVersionsOwnIDIsAnItem(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.jellyfin = true
	version := map[string]any{
		"Id": "v1", "Name": "Zzyzx", "Type": "Movie", "Path": "/zz/films/Zzyzx - 1080p.mkv",
		"MediaSources": []map[string]any{
			{"Id": "v1", "Path": "/zz/films/Zzyzx - 1080p.mkv", "MediaStreams": []map[string]any{{"Type": "Video", "Codec": "h264", "Height": 1080, "Width": 1920}}},
			{"Id": "f1", "Path": "/zz/films/Zzyzx - 2160p.mkv", "MediaStreams": []map[string]any{{"Type": "Video", "Codec": "hevc", "Height": 2160, "Width": 3840}}},
		},
	}
	f.mux.HandleFunc("GET /Users", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{{"Id": "u1", "Name": "Quux", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}}})
	})
	// the item query knows the film by its own id alone
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page()) })
	f.mux.HandleFunc("GET /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "v1" {
			http.NotFound(w, r)
			return
		}
		writeJSON(t, w, version)
	})
	f.mux.HandleFunc("GET /Items/{id}/Images", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, []any{}) })
	f.mux.HandleFunc("GET /Items/{id}/RemoteImages", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Images": []any{}, "TotalRecordCount": 0})
	})
	f.mux.HandleFunc("GET /Items/{id}/RemoteSearch/Subtitles/{lang}", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, []any{}) })
	f.mux.HandleFunc("GET /Items/{id}/Similar", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page()) })
	cs := session(t, f, Options{})

	got := mustCall(t, cs, "item_get", map[string]any{"id": "v1"})
	if text(got["path"]) != "/zz/films/Zzyzx - 1080p.mkv" || number(t, got["height"], "height") != 1080 || len(objects(t, got["versions"], "versions")) != 2 {
		t.Errorf("item_get of a version's id = %v", got)
	}
	for _, tool := range []string{"item_artwork", "item_subtitle_search", "item_similar"} {
		mustCall(t, cs, tool, map[string]any{"id": "v1"})
	}
	for _, tool := range []string{"item_get", "item_artwork", "item_subtitle_search", "item_similar"} {
		if msg := mustRefuse(t, cs, tool, map[string]any{"id": "0badc0de0badc0de0badc0de0badc0de"}); !strings.Contains(msg, "no item with id 0badc0de0badc0de0badc0de0badc0de") {
			t.Errorf("%s of an id nothing has = %s", tool, msg)
		}
	}
}

// The files an item is read back for are held to the count the server gave:
// three counted and two read back is said, not listed as the two.
func TestVersionFilesAreHeldToTheirCount(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.jellyfin = true
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(map[string]any{"Id": "f1", "Name": "Zzyzx", "Type": "Movie", "MediaSources": []map[string]any{{"Path": "/zz/a.mkv"}, {"Path": "/zz/b.mkv"}}}))
	})
	client := f.client(t)
	for count, want := range map[int]string{2: "", 3: "the server counts 3 files for Zzyzx (id f1), and reading them back found 2"} {
		items := []embyfin.Item{{ID: "f1", Name: "Zzyzx", MediaSourceCount: count}}
		err := client.WithVersionFiles(t.Context(), items)
		switch {
		case want == "" && (err != nil || len(items[0].MediaSources) != 2):
			t.Errorf("%d counted: %v, %d files", count, err, len(items[0].MediaSources))
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%d counted, 2 read back: %v, want %q", count, err, want)
		}
	}
}

// A show held without numbers - a daily show's thousands of files - is
// counted in full and named in part: the first ten, and how many more.
func TestUnnumberedFilesAreCountedAndNamedInPart(t *testing.T) {
	t.Parallel()

	s := severance()
	for i := range 15 {
		s.episodes = append(s.episodes, ep{season: 9, number: 100 + i, name: fmt.Sprintf("Day %d", i), path: fmt.Sprintf("/media/shows/Severance/Day %02d.mkv", i), noSeason: true, noNumber: true})
	}
	cs := session(t, tvServer(t, s), Options{})

	missing := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"})
	if number(t, missing["unnumbered_count"], "unnumbered_count") != 15 || len(texts(missing["unnumbered_files"])) != 10 {
		t.Errorf("show_missing = %v unnumbered of %v named, want 15 and the first 10", missing["unnumbered_count"], texts(missing["unnumbered_files"]))
	}
	exists := mustCall(t, cs, "show_episodes_exist", map[string]any{"series_id": "sev", "episodes": []map[string]any{{"season": 1, "episode": 5}}})
	if w := text(exists["warning"]); !strings.Contains(w, "15 file(s)") || !strings.Contains(w, "Day 09.mkv and 5 more") || strings.Contains(w, "Day 10.mkv") {
		t.Errorf("the warning = %q, want 15 counted and 10 named", w)
	}
}
