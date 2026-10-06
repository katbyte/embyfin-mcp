package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callTool calls a tool on a session and returns its structured result, or
// the error text when the tool refused.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (out map[string]any, refusal string) {
	t.Helper()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		var msgs []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msgs = append(msgs, tc.Text)
			}
		}
		return nil, strings.Join(msgs, "; ")
	}
	out, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("%s: structured content is %T", name, res.StructuredContent)
	}

	return out, refusal
}

func TestVocabField(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{"genre": "genres", "Tags": "tags", "studio": "studios", "narrators": "", "": ""} {
		if got := vocabField(in); got != want {
			t.Errorf("vocabField(%q) = %q, want %q", in, got, want)
		}
	}
	if got := sortedCounts(map[string]int{"b": 2, "a": 2, "c": 5}); got[0].Value != "c" || got[1].Value != "a" || got[2].Value != "b" {
		t.Errorf("sortedCounts = %v", got)
	}
}

func TestListEdit(t *testing.T) {
	t.Parallel()

	current := []string{"Drama", "Crime"}
	if got := (listEdit{add: []string{"crime", " Thriller ", ""}, remove: []string{"DRAMA"}}).apply(current); !slices.Equal(got, []string{"Crime", "Thriller"}) {
		t.Errorf("add and remove = %v", got)
	}
	if got := (listEdit{replace: []string{"Horror", " "}, replaceGiven: true}).apply(current); !slices.Equal(got, []string{"Horror"}) {
		t.Errorf("replace = %v", got)
	}
	if got := (listEdit{replace: []string{}, replaceGiven: true}).apply(current); len(got) != 0 {
		t.Errorf("an empty replacement clears the list: %v", got)
	}
	if !slices.Equal(current, []string{"Drama", "Crime"}) {
		t.Errorf("apply changed its input: %v", current)
	}
	if err := (listEdit{field: "tags", replaceGiven: true, add: []string{"x"}}).validate(); err == nil || !strings.Contains(err.Error(), "add_tags") {
		t.Errorf("replace with add = %v", err)
	}
	if !(listEdit{}).empty() || (listEdit{remove: []string{"x"}}).empty() {
		t.Error("empty is wrong")
	}
}

func TestVocabularyOnAFullItem(t *testing.T) {
	t.Parallel()

	// Emby: tags as named records only, studios with numeric ids
	var emby map[string]any
	if err := json.Unmarshal([]byte(`{"Genres":["Drama"],"Tags":null,"TagItems":[{"Name":"heist","Id":3}],"Studios":[{"Name":"Regency","Id":9}]}`), &emby); err != nil {
		t.Fatal(err)
	}
	if got := vocabularyOf(emby, fieldTags); !slices.Equal(got, []string{"heist"}) {
		t.Errorf("Emby tags = %v", got)
	}
	if got := vocabularyOf(emby, fieldStudios); !slices.Equal(got, []string{"Regency"}) {
		t.Errorf("studios = %v", got)
	}
	setVocabulary(emby, fieldGenres, []string{"Crime"})
	setVocabulary(emby, fieldTags, nil)
	setVocabulary(emby, fieldStudios, []string{"A24"})
	b, _ := json.Marshal(emby)
	for _, want := range []string{`"Genres":["Crime"]`, `"GenreItems":[{"Name":"Crime"}]`, `"Tags":[]`, `"TagItems":[]`, `"Studios":[{"Name":"A24"}]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s lacks %s", b, want)
		}
	}

	// Jellyfin: plain tags
	if got := vocabularyOf(map[string]any{"Tags": []any{"a", "b"}}, fieldTags); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("Jellyfin tags = %v", got)
	}
	if got := vocabularyOf(map[string]any{}, "nope"); got != nil {
		t.Errorf("an unknown field = %v", got)
	}
}

func TestRenameValue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		values   []string
		from, to string
		want     []string
		changed  bool
	}{
		{[]string{"Sci-Fi", "Drama"}, "Sci-Fi", "Science Fiction", []string{"Science Fiction", "Drama"}, true},
		{[]string{"Sci-Fi", "Science Fiction"}, "Sci-Fi", "Science Fiction", []string{"Science Fiction"}, true}, // a merge
		{[]string{"Sci-Fi", "Drama"}, "Sci-Fi", "", []string{"Drama"}, true},                                    // a removal
		{[]string{"sci-fi"}, "Sci-Fi", "Science Fiction", []string{"sci-fi"}, false},                            // matched exactly
		{[]string{"science fiction"}, "science fiction", "Science Fiction", []string{"Science Fiction"}, true},  // a case fix is not a merge
	} {
		got, changed := renameValue(tc.values, tc.from, tc.to)
		if !slices.Equal(got, tc.want) || changed != tc.changed {
			t.Errorf("renameValue(%v, %q, %q) = %v, %v; want %v, %v", tc.values, tc.from, tc.to, got, changed, tc.want, tc.changed)
		}
	}
}

func TestSpellingReport(t *testing.T) {
	t.Parallel()

	if got := norm("  Sci-Fi / Fantasy & Amélie.  "); got != "sci fi fantasy amelie" {
		t.Errorf("norm = %q", got)
	}
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"superhero", "superhreo", true},
		{"romance", "romances", true},
		{"horror", "humour", false}, // two edits on short words
		{"war", "was", false},       // too short to judge
		{"science fiction", "science fictoin", true},
		{"martial arts film", "martial arts flim", true},
	} {
		if got := typoApart(norm(tc.a), norm(tc.b)); got != tc.want {
			t.Errorf("typoApart(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
	if !truncationOf("warner bros", "warner bros pictures") || truncationOf("a24", "a24 films") || truncationOf("warner", "warnerbros") {
		t.Error("truncationOf is wrong")
	}

	counts := newSpellingCounts(vocabFields)
	add := func(genres, tags, studios []string) {
		it := &embyfin.Item{Genres: genres, Tags: tags}
		for _, s := range studios {
			it.Studios = append(it.Studios, embyfin.NameRef{Name: s})
		}
		counts.add(it)
	}
	add([]string{"Science Fiction", "Superhero"}, []string{"Sci-Fi"}, []string{"Warner Bros. Pictures"})
	add([]string{"Science Fiction"}, []string{"sci fi"}, []string{"Warner Bros."})
	add([]string{"Science-Fiction", "Superhreo"}, []string{"Sci-Fi"}, []string{"A24"})

	genres := counts.report(fieldGenres)
	if len(genres) != 2 || genres[0].Kind != "spelling" || genres[0].Keep != "Science Fiction" || len(genres[0].Spellings) != 2 ||
		genres[1].Kind != "near" || genres[1].Keep != "Superhero" {
		t.Errorf("genres = %+v", genres)
	}
	if tags := counts.report(fieldTags); len(tags) != 1 || tags[0].Keep != "Sci-Fi" || tags[0].Spellings[0].Items != 2 {
		t.Errorf("tags = %+v", tags)
	}
	if studios := counts.report(fieldStudios); len(studios) != 1 || studios[0].Kind != "contains" {
		t.Errorf("studios = %+v", studios)
	}
	// genres are not cut short: only studios are names
	c := newSpellingCounts([]string{fieldGenres})
	c.addValue(fieldGenres, "Action")
	c.addValue(fieldGenres, "Action Adventure")
	if g := c.report(fieldGenres); len(g) != 0 {
		t.Errorf("a genre inside another = %+v", g)
	}

	if fields, err := spellingFields(""); err != nil || !slices.Equal(fields, []string{fieldGenres, fieldTags, fieldStudios, fieldAlbums, fieldArtists}) {
		t.Errorf("spellingFields() = %v, %v", fields, err)
	}
	for in, want := range map[string]string{"album": fieldAlbums, "Artists": fieldArtists, "genre": fieldGenres} {
		if fields, err := spellingFields(in); err != nil || !slices.Equal(fields, []string{want}) {
			t.Errorf("spellingFields(%q) = %v, %v, want %s", in, fields, err, want)
		}
	}
	if _, err := spellingFields("narrators"); err == nil {
		t.Error("an unknown field was accepted")
	}
}

func TestCheckQuality(t *testing.T) {
	t.Parallel()

	video := func(codec string, w, h int, bitrate int64) embyfin.MediaSource {
		return embyfin.MediaSource{MediaStreams: []embyfin.MediaStream{{Type: "Audio", Codec: "aac"}, {Type: "Video", Codec: codec, Width: w, Height: h, BitRate: bitrate}}}
	}
	in := qualityDefaults(qualityIn{})
	if in.MinHeight != 720 || in.Limit != 100 {
		t.Errorf("defaults = %+v", in)
	}

	sd := &embyfin.Item{MediaSources: []embyfin.MediaSource{video("mpeg4", 640, 360, 900_000)}}
	detail, height, bad := checkQuality(sd, in)
	if !bad || height != 360 || !strings.Contains(detail, "mpeg4 640x360") || !strings.Contains(detail, "360p, below 720p") || !strings.Contains(detail, "legacy codec mpeg4") {
		t.Errorf("a DVD rip = %q %d %v", detail, height, bad)
	}
	// judged by its best version
	if _, _, flagged := checkQuality(&embyfin.Item{MediaSources: []embyfin.MediaSource{video("h264", 640, 360, 0), video("hevc", 3840, 2160, 0)}}, in); flagged {
		t.Error("an item with a 4K version was flagged")
	}
	// a widescreen encode is judged by its width: 1280x536 is a 720p picture
	// with the bars cropped, not a 536p one
	if d, h, flagged := checkQuality(&embyfin.Item{MediaSources: []embyfin.MediaSource{video("h264", 1280, 536, 0)}}, in); flagged || h != 720 {
		t.Errorf("a 2.39:1 720p encode = %q %d %v", d, h, flagged)
	}
	if d, h, flagged := checkQuality(&embyfin.Item{MediaSources: []embyfin.MediaSource{video("h264", 1920, 800, 0)}}, qualityDefaults(qualityIn{MinHeight: 1080})); flagged || h != 1080 {
		t.Errorf("a 2.40:1 1080p encode against 1080 = %q %d %v", d, h, flagged)
	}
	// the codec check can be turned off, and a bitrate floor on
	in.Codecs = new(false)
	if _, _, flagged := checkQuality(&embyfin.Item{MediaSources: []embyfin.MediaSource{video("mpeg2video", 1920, 1080, 0)}}, in); flagged {
		t.Error("the codec check ran when off")
	}
	// in bits per second, like every bitrate a tool answers with
	in.MinBitrate = 2_000_000
	detail, _, bad = checkQuality(&embyfin.Item{MediaSources: []embyfin.MediaSource{video("h264", 1920, 1080, 1_500_000)}}, in)
	if !bad || !strings.Contains(detail, "1500 kbps, below 2000 kbps") {
		t.Errorf("a low bitrate = %q %v", detail, bad)
	}
	// the source's bitrate stands in when the stream has none
	if _, _, bad := checkQuality(&embyfin.Item{MediaSources: []embyfin.MediaSource{{Bitrate: 1_000_000, MediaStreams: []embyfin.MediaStream{{Type: "Video", Codec: "h264", Height: 1080}}}}}, in); !bad {
		t.Error("the source bitrate was not used")
	}
	// nothing to judge
	if _, _, bad := checkQuality(&embyfin.Item{MediaSources: []embyfin.MediaSource{{MediaStreams: []embyfin.MediaStream{{Type: "Audio"}}}}}, in); bad {
		t.Error("an audio-only item was flagged")
	}
}

func TestSeasonGaps(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		eps  map[int][]int
		want []string
	}{
		{map[int][]int{1: {1, 2, 3}}, nil},
		{map[int][]int{1: {3, 1, 1}}, []string{"S01E02"}},
		{map[int][]int{1: {1, 4}, 3: {2}}, []string{"S01E02", "S01E03", "season 2"}},
		{map[int][]int{0: {1, 5}, 2: {1}}, nil}, // specials have no order, and nothing before the first season held
	} {
		if got := seasonGaps(tc.eps); !slices.Equal(got, tc.want) {
			t.Errorf("seasonGaps(%v) = %v, want %v", tc.eps, got, tc.want)
		}
	}
}

func TestProgressAndDates(t *testing.T) {
	t.Parallel()

	// positions are seconds, like runtimes, so the two divide without a
	// conversion: 45 minutes in is 2700
	if s, p := progressOf(&embyfin.Item{UserData: &embyfin.UserData{PlaybackPositionTicks: 45 * ticksPerMinute, PlayedPercentage: 180000}}); s != 2700 || p != 100 {
		t.Errorf("progressOf = %v, %v", s, p)
	}
	if s, p := progressOf(&embyfin.Item{}); s != 0 || p != 0 {
		t.Errorf("progressOf without user data = %v, %v", s, p)
	}
	for in, want := range map[string]string{"2026-09-15T07:09:22.0000000Z": "2026-09-15", "": "on an unknown date", "yesterday": "yesterday"} {
		if got := dateOf(in); got != want {
			t.Errorf("dateOf(%q) = %q", in, got)
		}
	}
}

// An edit of many items that fails part way loses its answer with the error,
// so the error itself names the items already changed and says the same call
// again finishes it: item_edit and metadata_rename alike.
func TestPartEditsSayWhatLanded(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(t, w, `{"Items":[{"Id":"admin","Name":"root","Policy":{"IsAdministrator":true}}],"TotalRecordCount":1}`)
	})
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(t, w, `{"Items":[],"TotalRecordCount":0}`)
	})
	names := map[string]string{"1": "Alien", "2": "Aliens", "3": "Alien 3"}
	f.mux.HandleFunc("GET /Users/admin/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		writeRaw(t, w, `{"Id":"`+id+`","Name":"`+names[id]+`","Genres":["Sci-Fi"]}`)
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(t, w, `{"Items":[{"Id":"1","Genres":["Sci-Fi"]},{"Id":"2","Genres":["Sci-Fi"]},{"Id":"3","Genres":["Sci-Fi"]}],"TotalRecordCount":3}`)
	})
	// the second item's save is refused
	f.mux.HandleFunc("POST /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "2" {
			http.Error(w, "the item is locked", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	cs := session(t, f, Options{})

	for name, args := range map[string]map[string]any{
		"item_edit":       {"ids": []any{"1", "2", "3"}, "add_tags": []any{"space"}},
		"metadata_rename": {"field": "genres", "from": "Sci-Fi", "to": "Science Fiction"},
	} {
		msg := mustRefuse(t, cs, name, args)
		if !strings.Contains(msg, "1 of the 3 items were already changed: Alien (1)") || !strings.Contains(msg, "run the same call again to finish") || !strings.HasPrefix(msg, "2: ") {
			t.Errorf("%s stopped part way: %s", name, msg)
		}
	}
}

// Emby shows an album with its tracks' old genre as well as the new one for
// a moment after they are renamed - to its genre filter, and on the album's
// own row, which item_get reads - while the album's own record has the new
// one alone (seen on 4.10), and a list or an item_get straight after the
// rename showed the old genre still on it. The rename reads the items again,
// both ways, until none shows the old value; one still shown is named in
// still_listed, and one whose own record has the old value again is an error
// naming it.
func TestARenameWaitsForTheServersListsToCatchUp(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		// filterLag and rowLag are how many reads after the rename still
		// show the album with the old genre, to the filter and on its row by
		// id; -1 for every read
		filterLag, rowLag int
		// kept: the album's edit does not hold
		kept      bool
		stillSaid string
		errSaid   string
	}{
		"the filter catches up":      {filterLag: 2},
		"the album's row catches up": {rowLag: 2},
		"never catches up":           {filterLag: -1, stillSaid: "2"},
		"puts it back":               {filterLag: -1, kept: true, errSaid: "renamed, but a few seconds after, the server has Electronica on Afterworld (2) again: it kept the old value or put it back; the 2 items edited were: Afterworld (1), Afterworld (2)"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFakeServer(t)
			f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
				writeRaw(t, w, `{"Items":[{"Id":"admin","Name":"root","Policy":{"IsAdministrator":true}}],"TotalRecordCount":1}`)
			})
			f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
				writeRaw(t, w, `{"Items":[],"TotalRecordCount":0}`)
			})
			// a track, 1, and its album, 2
			genres := map[string]string{"1": "Electronica", "2": "Electronica"}
			renamed, filterReads, rowReads := false, 0, 0
			f.mux.HandleFunc("GET /Users/admin/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
				f.mu.Lock()
				defer f.mu.Unlock()
				id := r.PathValue("id")
				writeRaw(t, w, `{"Id":"`+id+`","Name":"Afterworld","Genres":["`+genres[id]+`"]}`)
			})
			f.mux.HandleFunc("POST /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
				body := readBody(t, r)
				f.mu.Lock()
				defer f.mu.Unlock()
				id := r.PathValue("id")
				if g, ok := body["Genres"].([]any); ok && len(g) == 1 && (id != "2" || !tc.kept) {
					genres[id] = text(g[0])
				}
				renamed = genres["1"] == "Electronic"
				w.WriteHeader(http.StatusNoContent)
			})
			lagging := func(lag, reads int) bool { return lag < 0 || reads <= lag }
			f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
				f.mu.Lock()
				defer f.mu.Unlock()
				q := r.URL.Query()
				album := `{"Id":"2","Genres":["Electronic"]}`
				switch {
				case !renamed:
					writeRaw(t, w, `{"Items":[{"Id":"1","Genres":["Electronica"]},{"Id":"2","Genres":["Electronica"]}],"TotalRecordCount":2}`)
					return
				case q.Get("Genres") != "":
					filterReads++
					if lagging(tc.filterLag, filterReads) {
						writeRaw(t, w, `{"Items":[{"Id":"2","Genres":["Electronic","Electronica"]}],"TotalRecordCount":1}`)
						return
					}
					writeRaw(t, w, `{"Items":[],"TotalRecordCount":0}`)
					return
				}
				rowReads++
				if lagging(tc.rowLag, rowReads) {
					album = `{"Id":"2","Genres":["Electronic","Electronica"]}`
				}
				writeRaw(t, w, `{"Items":[{"Id":"1","Genres":["Electronic"]},`+album+`],"TotalRecordCount":2}`)
			})
			r := &registry{client: f.client(t), settle: time.Millisecond}
			registerSpellingTools(r)
			cs := hostRegistry(t, r)

			out, msg := callTool(t, cs, "metadata_rename", map[string]any{"field": "genres", "from": "Electronica", "to": "Electronic"})
			if tc.errSaid != "" {
				if !strings.Contains(msg, tc.errSaid) {
					t.Errorf("metadata_rename = %v %q, want %q", out, msg, tc.errSaid)
				}
				return
			}
			if msg != "" || number(t, out["updated"], "updated") != 2 || strings.Join(texts(out["still_listed"]), ",") != tc.stillSaid {
				t.Errorf("metadata_rename = %v %s, want both renamed and still_listed %q", out, msg, tc.stillSaid)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			// read until both ways stopped showing the old genre, and no more
			if want := max(tc.filterLag, tc.rowLag) + 1; tc.stillSaid == "" && (filterReads != want || rowReads != want) {
				t.Errorf("read the filter %d and the rows %d times after the rename, want %d each: until both caught up", filterReads, rowReads, want)
			}
		})
	}
}

// item_edit over several ids and metadata_rename against a canned Emby: the
// full item is read in the administrator's view and posted back with both
// spellings of each list, a field that is one item's own is refused for
// several ids, and a rename touches only the exact spelling.
func TestVocabularyEditsAgainstAFake(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Id":"admin","Name":"root","Policy":{"IsAdministrator":true}}],"TotalRecordCount":1}`)
	})
	full := map[string]string{
		"1": `{"Id":"1","Name":"Alien","Type":"Movie","Path":"/zz/saved/Alien/Alien.mkv","Genres":["Sci-Fi","Horror"],"TagItems":[{"Name":"space","Id":1}],"Studios":[]}`,
		"2": `{"Id":"2","Name":"Aliens","Type":"Movie","Path":"/zz/films/Aliens/Aliens.mkv","Genres":["sci-fi"],"TagItems":[],"Studios":[{"Name":"Brandywine","Id":4}]}`,
		// a song in the library that saves nfos, which gets none (seen on
		// Jellyfin 12.1)
		"3": `{"Id":"3","Name":"Afterworld","Type":"Audio","Path":"/zz/saved/Music/Afterworld.mp3","Genres":[],"TagItems":[],"Studios":[]}`,
	}
	// Alien's library writes an edit to its nfo, Aliens' does not
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(t, w, `{"Items":[
			{"Name":"Saved","ItemId":"7","Locations":["/zz/saved"],"LibraryOptions":{"MetadataSavers":["Nfo"]}},
			{"Name":"Films","ItemId":"8","Locations":["/zz/films"],"LibraryOptions":{"MetadataSavers":[]}}],"TotalRecordCount":2}`)
	})
	f.mux.HandleFunc("GET /Users/admin/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, full[r.PathValue("id")])
	})
	posted := map[string]string{}
	f.mux.HandleFunc("POST /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		posted[r.PathValue("id")] = string(b)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		// the server's genre filter is not exact: both spellings come back,
		// until Aliens is renamed
		f.mu.Lock()
		renamed := strings.Contains(posted["2"], `"Genres":["Science Fiction"]`)
		f.mu.Unlock()
		if renamed {
			_, _ = io.WriteString(w, `{"Items":[{"Id":"1","Name":"Alien","Genres":["Sci-Fi","Horror"]}],"TotalRecordCount":1}`)
			return
		}
		_, _ = io.WriteString(w, `{"Items":[{"Id":"1","Name":"Alien","Genres":["Sci-Fi","Horror"]},{"Id":"2","Name":"Aliens","Genres":["sci-fi"]}],"TotalRecordCount":2}`)
	})
	cs := session(t, f, Options{})

	out, msg := callTool(t, cs, "item_edit", map[string]any{"ids": []any{"1", "2"}, "add_tags": []any{"Classic", "SPACE"}, "remove_genres": []any{"horror"}, "official_rating": "R"})
	if msg != "" || number(t, out["updated"], "updated") != 2 {
		t.Fatalf("item_edit = %v %s", out, msg)
	}
	// the fields changed; case and order aside for now (the handler spells
	// the lists in lower case, which puts them after the capitalised fields)
	changed := texts(out["changed"])
	for i := range changed {
		changed[i] = strings.ToLower(changed[i])
	}
	slices.Sort(changed)
	if got := strings.Join(changed, ","); got != "genres,officialrating,tags" {
		t.Errorf("changed = %s, want genres, official rating and tags", got)
	}
	// each item in the order given, with what the fields changed were, to
	// set them back by; and the one whose library saves nfos named
	items := objects(t, out["items"], "items")
	if len(items) != 2 || items[0]["id"] != "1" || items[0]["name"] != "Alien" || items[1]["name"] != "Aliens" {
		t.Fatalf("items = %v, want each in the order given", items)
	}
	if got, err := json.Marshal(items[0]["was"]); err != nil || string(got) != `{"genres":["Sci-Fi","Horror"],"official_rating":null,"tags":["space"]}` {
		t.Errorf("Alien was = %s (%v), want the genres, tags and rating it had (none)", got, err)
	}
	if got := strings.Join(texts(out["nfo_expected"]), ","); got != "1" {
		t.Errorf("nfo_expected = %v, want Alien's alone", out["nfo_expected"])
	}
	if song, refusal := callTool(t, cs, "item_edit", map[string]any{"ids": []any{"3"}, "add_tags": []any{"Classic"}}); refusal != "" || song["nfo_expected"] != nil {
		t.Errorf("a song's edit = %v %s, want no nfo expected", song, refusal)
	}
	for _, want := range []string{`"TagItems":[{"Name":"space"},{"Name":"Classic"}]`, `"Tags":["space","Classic"]`, `"Genres":["Sci-Fi"]`, `"GenreItems":[{"Name":"Sci-Fi"}]`, `"OfficialRating":"R"`} {
		if !strings.Contains(posted["1"], want) {
			t.Errorf("Alien's update lacks %s: %s", want, posted["1"])
		}
	}
	if !strings.Contains(posted["2"], `"Studios":[{"Id":4,"Name":"Brandywine"}]`) {
		t.Errorf("a list the edit did not name was changed: %s", posted["2"])
	}

	// one item's own fields go with one id
	clear(posted)
	out, msg = callTool(t, cs, "item_edit", map[string]any{"ids": []any{"1"}, "name": "Alien (1979)", "sort_name": "Alien 1", "year": 1979})
	if msg != "" || number(t, out["updated"], "updated") != 1 {
		t.Fatalf("item_edit of one = %v %s", out, msg)
	}
	if got, _ := json.Marshal(out["changed"]); string(got) != `["Name","ProductionYear","SortName"]` {
		t.Errorf("changed = %s", got)
	}
	// the sort name locked, or Emby works it out from the name again
	for _, want := range []string{`"Name":"Alien (1979)"`, `"SortName":"Alien 1"`, `"ForcedSortName":"Alien 1"`, `"LockedFields":["SortName"]`, `"ProductionYear":1979`} {
		if !strings.Contains(posted["1"], want) {
			t.Errorf("Alien's update lacks %s: %s", want, posted["1"])
		}
	}
	if _, touched := posted["2"]; touched {
		t.Errorf("an item not named was posted: %s", posted["2"])
	}

	clear(posted)
	out, msg = callTool(t, cs, "metadata_rename", map[string]any{"field": "genre", "from": "sci-fi", "to": "Science Fiction"})
	if msg != "" || number(t, out["updated"], "updated") != 1 {
		t.Fatalf("metadata_rename = %v %s", out, msg)
	}
	if _, touched := posted["1"]; touched || !strings.Contains(posted["2"], `"Genres":["Science Fiction"]`) {
		t.Errorf("the rename posted %v", posted)
	}
	// every item changed by id, to put the value back on
	if got := strings.Join(texts(out["ids"]), ","); got != "2" || out["merged"] != nil {
		t.Errorf("the rename's ids = %v, merged %v", out["ids"], out["merged"])
	}
	if q := f.requests("/Items")[0].Query; !strings.Contains(q, "Genres=sci-fi") || !strings.Contains(q, "ExcludeItemTypes=") {
		t.Errorf("the rename's lookup = %s", q)
	}

	clear(posted)
	for args, want := range map[string]string{
		`{"ids":["1"]}`:                                  "nothing to change",
		`{"ids":[],"name":"x"}`:                          "item",
		`{"ids":["1"],"genres":["a"],"add_genres":[]}`:   "",
		`{"ids":["1"],"tags":["a"],"remove_tags":["b"]}`: "one or the other",
		`{"ids":["1","2"],"name":"x"}`:                   "name is one item's own: pass one id to set it",
		`{"ids":["1","2"],"sort_name":"x"}`:              "sort_name is one item's own",
		`{"ids":["1","2"],"overview":"x"}`:               "overview is one item's own",
		`{"ids":["1","2"],"year":1979,"add_tags":["a"]}`: "year is one item's own",
	} {
		var a map[string]any
		_ = json.Unmarshal([]byte(args), &a)
		_, msg := callTool(t, cs, "item_edit", a)
		if want == "" && msg != "" || want != "" && !strings.Contains(msg, want) {
			t.Errorf("item_edit %s = %q, want %q", args, msg, want)
		}
	}
	if _, touched := posted["2"]; touched {
		t.Errorf("a refused edit posted: %s", posted["2"])
	}
}

// C13: a row with video always states its dynamic range. It used to be
// missing for three different reasons - SDR, not probed, and not exposed by
// the server - and a caller reading "no HDR10 here" as "therefore Dolby
// Vision" put files in the wrong bucket.
func TestHDRFormatSaysWhenItDoesNotKnow(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		stream embyfin.MediaStream
		want   string
	}{
		{"nothing known", embyfin.MediaStream{Type: "Video"}, "unknown"},
		{"jellyfin's narrow reading wins", embyfin.MediaStream{VideoRangeType: "DOVIWithHDR10", VideoRange: "HDR"}, "dovi_hdr10"},
		{"jellyfin plain dolby vision", embyfin.MediaStream{VideoRangeType: "DOVI"}, "dovi"},
		{"emby says only HDR, the transfer says which", embyfin.MediaStream{VideoRange: "HDR", ColourTransfer: "arib-std-b67"}, "hlg"},
		// HDR of no named kind is not HDR10 for want of saying which
		{"a server says HDR and nothing else", embyfin.MediaStream{VideoRange: "HDR"}, "hdr"},
		// what Emby 4.10 answered for an HEVC file tagged with HDR10's colours
		{"emby names HDR10 itself, with a space", embyfin.MediaStream{VideoRange: "HDR 10"}, "hdr10"},
		{"emby names HDR10 and the transfer agrees", embyfin.MediaStream{VideoRange: "HDR 10", ColourTransfer: "smpte2084"}, "hdr10"},
		{"emby says SDR", embyfin.MediaStream{VideoRange: "SDR", ColourTransfer: "bt709"}, "sdr"},
		{"only a transfer, and it is an HDR one", embyfin.MediaStream{ColourTransfer: "smpte2084"}, "hdr10"},
		{"only a transfer, and it is not", embyfin.MediaStream{ColourTransfer: "bt709"}, "sdr"},
		// a transfer the file leaves unset is no SDR one
		{"only a transfer, and it says nothing", embyfin.MediaStream{ColourTransfer: "unknown"}, "unknown"},
		{"only a transfer, and it is unspecified", embyfin.MediaStream{ColourTransfer: "unspecified"}, "unknown"},
		// Jellyfin's Dolby Vision kinds each by name: DOVIWithEL and the rest
		// read as hdr10 when only the base layer's transfer was left to go on
		{"jellyfin dolby vision with an enhancement layer", embyfin.MediaStream{VideoRangeType: "DOVIWithEL", VideoRange: "HDR", ColourTransfer: "smpte2084"}, "dovi_el"},
		{"jellyfin dolby vision over HDR10+", embyfin.MediaStream{VideoRangeType: "DOVIWithHDR10Plus", VideoRange: "HDR", ColourTransfer: "smpte2084"}, "dovi_hdr10plus"},
		{"jellyfin dolby vision with both", embyfin.MediaStream{VideoRangeType: "DOVIWithELHDR10Plus", VideoRange: "HDR", ColourTransfer: "smpte2084"}, "dovi_el_hdr10plus"},
		{"jellyfin dolby vision it calls invalid", embyfin.MediaStream{VideoRangeType: "DOVIInvalid", VideoRange: "HDR", ColourTransfer: "smpte2084"}, "dovi_invalid"},
		{"jellyfin dolby vision over HLG", embyfin.MediaStream{VideoRangeType: "DOVIWithHLG", VideoRange: "HDR", ColourTransfer: "arib-std-b67"}, "dovi_hlg"},
		{"jellyfin dolby vision over SDR", embyfin.MediaStream{VideoRangeType: "DOVIWithSDR", VideoRange: "HDR", ColourTransfer: "bt709"}, "dovi_sdr"},
		{"jellyfin HDR10+", embyfin.MediaStream{VideoRangeType: "HDR10Plus", VideoRange: "HDR", ColourTransfer: "smpte2084"}, "hdr10plus"},
		{"jellyfin's own unknown, and a transfer", embyfin.MediaStream{VideoRangeType: "Unknown", ColourTransfer: "smpte2084"}, "hdr10"},
		// Emby's narrow reading, which its broad one ("HDR 10") hides
		{"emby dolby vision 8.1", embyfin.MediaStream{VideoRange: "HDR 10", ExtendedVideoType: "DolbyVision", ExtendedVideoSubType: "DoviProfile81", ColourTransfer: "smpte2084"}, "dovi_hdr10"},
		{"emby dolby vision 5", embyfin.MediaStream{VideoRange: "HDR 10", ExtendedVideoType: "DolbyVision", ExtendedVideoSubType: "DoviProfile50"}, "dovi"},
		{"emby dolby vision 8.4", embyfin.MediaStream{ExtendedVideoType: "DolbyVision", ExtendedVideoSubType: "DoviProfile84"}, "dovi_hlg"},
		{"emby dolby vision 7.6", embyfin.MediaStream{ExtendedVideoType: "DolbyVision", ExtendedVideoSubType: "DoviProfile76"}, "dovi_el"},
		{"emby dolby vision of no named profile", embyfin.MediaStream{ExtendedVideoType: "DolbyVision", ExtendedVideoSubType: "None"}, "dovi"},
		{"emby HDR10+", embyfin.MediaStream{VideoRange: "HDR 10", ExtendedVideoType: "Hdr10Plus", ExtendedVideoSubType: "Hdr10Plus0"}, "hdr10plus"},
		{"emby HLG", embyfin.MediaStream{ExtendedVideoType: "HyperLogGamma"}, "hlg"},
		// what Emby 4.10 answered for the fixtures' HDR10-tagged upscale
		{"emby HDR10", embyfin.MediaStream{VideoRange: "HDR 10", ExtendedVideoType: "Hdr10", ExtendedVideoSubType: "Hdr10", ColourTransfer: "smpte2084"}, "hdr10"},
		{"emby's none leaves it to the broad reading", embyfin.MediaStream{VideoRange: "SDR", ExtendedVideoType: "None", ExtendedVideoSubType: "None"}, "sdr"},
	} {
		if got := tc.stream.HDR(); got != tc.want {
			t.Errorf("%s: hdr = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// An episode's runtime is judged against TMDB's for that episode, read from
// its series' seasons, and against nothing else. A file holding two episodes
// under one number - named as one, running as two - is the shape that hides
// an episode: the servers parse one number from the name, so the second
// reads as missing, and its length against TMDB's is what gives it away. A
// file the server records as holding two is judged against the two together,
// a special against TMDB's specials, and an episode TMDB holds no length
// for, or whose series holds no TMDB id, is counted as not judged.
func TestAuditProviderJudgesEpisodesByTMDBsOwnLength(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = []ep{
		{season: 0, number: 1, name: "Inside Severance", path: "/m/s0e1.mkv", minutes: 10},
		{season: 1, number: 1, name: "Good News About Hell", path: "/m/1.mkv", minutes: 22},
		// a minute off TMDB's is no finding: within two minutes
		{season: 1, number: 2, name: "Half Loop", path: "/m/2.mkv", minutes: 23},
		// the double: named as one episode, runs as two
		{season: 1, number: 3, name: "In Perpetuity", path: "/m/3.mkv", minutes: 44},
		// recorded as covering two episodes, and running like it
		{season: 1, number: 4, number2: 5, name: "The You You Are", path: "/m/45.mkv", minutes: 44}, //nolint:dupword // the episode's title
		// TMDB holds no length for it, and no episode 7 at all
		{season: 1, number: 6, name: "Hide and Seek", path: "/m/6.mkv", minutes: 22},
		{season: 1, number: 7, name: "Defiant Jazz", path: "/m/7.mkv", minutes: 22},
	}
	// a series with no TMDB id is not judged, however short
	unmatched := &fakeSeries{id: "u", name: "Zzyzx Unmatched", episodes: []ep{{season: 1, number: 1, name: "Pilot", path: "/m/u1.mkv", minutes: 1}}}

	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		switch r.URL.Path {
		case "/3/tv/95396":
			_, _ = w.Write([]byte(`{"id":95396,"seasons":[{"season_number":0},{"season_number":1}]}`))
		case "/3/tv/95396/season/0":
			_, _ = w.Write([]byte(`{"season_number":0,"episodes":[{"season_number":0,"episode_number":1,"name":"Inside Severance","runtime":30}]}`))
		case "/3/tv/95396/season/1":
			_, _ = w.Write([]byte(`{"season_number":1,"episodes":[` +
				`{"season_number":1,"episode_number":1,"name":"Good News About Hell","runtime":22},` +
				`{"season_number":1,"episode_number":2,"name":"Half Loop","runtime":22},` +
				`{"season_number":1,"episode_number":3,"name":"In Perpetuity","runtime":22},` +
				`{"season_number":1,"episode_number":4,"name":"The You You Are","runtime":22},` + //nolint:dupword // the episode's title
				`{"season_number":1,"episode_number":5,"name":"The Grim Barbarity of Optics and Design","runtime":22},` +
				`{"season_number":1,"episode_number":6,"name":"Hide and Seek"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	cs := session(t, tvServer(t, s, unmatched), Options{TMDBKey: "k", ProviderTransport: rewrite{target}})

	out := mustCall(t, cs, "audit_provider", map[string]any{"types": "Episode"})
	findings := objects(t, out["findings"], "findings")
	got := make([]string, 0, len(findings))
	for _, row := range findings {
		got = append(got, text(row["name"])+" ["+text(row["holds"])+"]: "+strings.Join(texts(row["problems"]), " | "))
	}
	want := []string{
		`Severance S00E01 Inside Severance [tmdb tv 95396]: runtime: file 10 min, TMDB says 30 min for S00E01 "Inside Severance" (66% off)`,
		`Severance S01E03 In Perpetuity [tmdb tv 95396]: runtime: file 44 min, TMDB says 22 min for S01E03 "In Perpetuity" (100% off)`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("findings =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// every episode scanned; E06, E07 and the unmatched show's pilot unjudged
	if n := number(t, out["items_scanned"], "items_scanned"); n != 8 {
		t.Errorf("items_scanned = %d, want 8", n)
	}
	if n := number(t, out["runtime_not_judged"], "runtime_not_judged"); n != 3 {
		t.Errorf("runtime_not_judged = %d, want E06 (no length), E07 (no such episode) and the unmatched pilot", n)
	}
	// the series' run read once for all its episodes: the series, its season
	// 1 and its specials
	if n := asked.Load(); n != 3 {
		t.Errorf("TMDB was asked %d times, want 3", n)
	}

	// episodes have only a runtime to judge
	if msg := mustRefuse(t, cs, "audit_provider", map[string]any{"types": "Episode", "checks": "ids"}); !strings.Contains(msg, "episodes are checked by runtime only") {
		t.Errorf("episodes with ids alone = %q", msg)
	}
	// a capped call counts a series once: one lookup covers every episode
	if out := mustCall(t, cs, "audit_provider", map[string]any{"types": "Episode", "max_lookups": 1}); out["next_offset"] != nil || number(t, out["items_scanned"], "items_scanned") != 8 {
		t.Errorf("one lookup = %v, want the series' every episode and the unmatched show's", out)
	}
}

// C16: one episode's content under two episode numbers. Neither existing
// duplicate audit sees it - the provider ids differ because the server
// believes they are different episodes, and both files stand alone under
// their own item, so a library can carry the pair for years unreported.
func TestAuditDuplicateEpisodes(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = []ep{
		{season: 1, number: 1, name: "Good News About Hell", path: "/m/1.mkv", minutes: 45},
		{season: 1, number: 2, name: "Half Loop", path: "/m/2.mkv", minutes: 45},
		// the same episode again, under another number, near-identical length
		{season: 1, number: 4, name: "Half Loop", path: "/m/4.mkv", minutes: 46},
		// a shared title that is not the same content: half the length
		{season: 2, number: 1, name: "Part One", path: "/m/21.mkv", minutes: 60},
		{season: 2, number: 2, name: "Part One", path: "/m/22.mkv", minutes: 30},
	}
	f := tvServer(t, s)
	adminView(t, f) // the episodes as people are shown them
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_duplicate_episodes", map[string]any{})
	groups := objects(t, out["groups"], "groups")
	if len(groups) != 2 || number(t, out["total_findings"], "total_findings") != 2 {
		t.Fatalf("groups = %v", groups)
	}

	// the canned server gives every file one size, so two alike in size say
	// nothing when the season's other episode is that size too: a lead, and
	// the evidence says why
	first := groups[0]
	if text(first["confidence"]) != "lead" || text(first["title"]) != "Half Loop" ||
		!slices.Equal(texts(first["evidence"]), []string{"E02 and E04: the same size to the byte, which E01 of the season is too, so no sign alone"}) {
		t.Errorf("first group = %v", first)
	}
	if eps := objects(t, first["episodes"], "episodes"); len(eps) != 2 ||
		number(t, eps[0]["episode"], "episode") != 2 || number(t, eps[1]["episode"], "episode") != 4 {
		t.Errorf("the group is not E02 and E04: %v", first["episodes"])
	}
	if eps := objects(t, first["episodes"], "episodes"); eps[0]["path"] == nil || number(t, eps[0]["runtime_s"], "runtime_s") != 45*60 {
		t.Errorf("a row lacks what a caller decides on: %v", eps[0])
	}

	// the same title at half the length is said, as far apart: a copy cut
	// short, one file holding two, or two episodes
	second := groups[1]
	if text(second["confidence"]) != "far_apart" {
		t.Errorf("a shared title at half the runtime = %v", second)
	}
	if gap, ok := second["runtime_gap"].(float64); !ok || gap < 0.4 {
		t.Errorf("runtime_gap = %v, want the halves to show", second["runtime_gap"])
	}

	// nothing is claimed about which copy to keep
	for _, key := range []string{"keep", "winner", "better", "delete"} {
		if _, claimed := first[key]; claimed {
			t.Errorf("the audit picked a winner: %v", first)
		}
	}
}

// C17: a rename that changes only spacing, case or an accent leaves the old
// folder behind, and the server builds a second series from it. The episodes
// are then split across two entries, each answering "no" to half the
// questions. Sharing an id misses these because the second entry usually
// carries no provider id - nothing matched it - so audit_duplicates finds
// them by their folder names, as folder_groups, counted in total_findings;
// asked for films alone it does not read series for them.
func TestAuditDuplicateSeries(t *testing.T) {
	t.Parallel()

	shows := []*fakeSeries{
		{
			id: "svu", name: "Law & Order: Special Victims Unit", year: 1999, ids: map[string]string{"Tmdb": "2734"},
			episodes: []ep{{season: 1, number: 1, name: "One", path: "/m/a.mkv"}},
		},
		// the same folder with two spaces, and no provider id at all
		{
			id: "svu2", name: "Law & Order (1999)  - Special Victims Unit", year: 1999,
			episodes: []ep{{season: 1, number: 2, name: "Two", path: "/m/b.mkv"}},
		},
		{
			id: "sev", name: "Severance", year: 2022, ids: map[string]string{"Tmdb": "95396"},
			episodes: []ep{{season: 1, number: 1, name: "One", path: "/m/c.mkv"}},
		},
	}
	shows[0].path = "/media/shows/Law & Order (1999) - Special Victims Unit"
	shows[1].path = "/media/shows/Law & Order (1999)  - Special Victims Unit"
	f := tvServer(t, shows...)
	adminView(t, f)
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_duplicates", map[string]any{})
	groups := objects(t, out["folder_groups"], "folder_groups")
	if len(groups) != 1 || number(t, out["total_findings"], "total_findings") != 1 || len(objects(t, out["groups"], "groups")) != 0 {
		t.Fatalf("folder_groups = %v, total %v, groups %v; want the one pair counted", groups, out["total_findings"], out["groups"])
	}
	series := objects(t, groups[0]["series"], "series")
	if len(series) != 2 {
		t.Fatalf("group = %v", groups[0])
	}
	ids := []string{text(series[0]["series_id"]), text(series[1]["series_id"])}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"svu", "svu2"}) {
		t.Errorf("group holds %v, want the two Law & Order folders", ids)
	}
	// the answer says what they collapse to, so a caller can see why
	if text(groups[0]["key"]) == "" {
		t.Errorf("no key on the group: %v", groups[0])
	}
	// films alone: the folder rule is series', and reads nothing
	if out := mustCall(t, cs, "audit_duplicates", map[string]any{"types": "Movie"}); number(t, out["total_findings"], "total_findings") != 0 || len(objects(t, out["folder_groups"], "folder_groups")) != 0 {
		t.Errorf("audit_duplicates over films = %v, want no folder groups", out)
	}
	// audit_all counts the pair on its duplicates row and says so
	for _, row := range objects(t, mustCall(t, cs, "audit_all", map[string]any{})["audits"], "audits") {
		if text(row["audit"]) == "audit_duplicates" && (number(t, row["findings"], "findings") != 1 || !strings.Contains(text(row["note"]), "two folders of one name (1, its folder_groups)")) {
			t.Errorf("audit_all's duplicates row = %v, want the folder pair counted and named", row)
		}
	}
}

// The folding itself: what counts as the same folder name, and what does not.
func TestFolderKey(t *testing.T) {
	t.Parallel()

	for _, same := range [][]string{
		{"Law & Order (1999) - Special Victims Unit", "Law & Order (1999)  - Special Victims Unit", "law & order (1999) - special victims unit"},
		{"The Law According to Lidia Poët", "The Law According to Lidia Poet"},
		{"CSI (2000) - Crime Scene Investigation", "CSI (2000)   Crime Scene Investigation"},
	} {
		for _, other := range same[1:] {
			if folderKey(other) != folderKey(same[0]) {
				t.Errorf("%q and %q are the same folder: %q against %q", other, same[0], folderKey(other), folderKey(same[0]))
			}
		}
	}
	// a year that really differs is a different folder, not a collision
	if folderKey("The Simpsons (1987-)") == folderKey("The Simpsons (1987-2008)") {
		t.Error("two different year ranges collapsed together")
	}
	if folderKey("MASH (1972)") == folderKey("MASH (1972) [dvd]") {
		t.Error("a folder with an edition tag is not the same folder")
	}
}
