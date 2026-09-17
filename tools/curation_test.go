package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
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

	if fields, err := spellingFields(""); err != nil || len(fields) != 3 {
		t.Errorf("spellingFields() = %v, %v", fields, err)
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

// item_batch_edit and metadata_rename against a canned Emby: the full item is
// read in the administrator's view and posted back with both spellings of
// each list, and a rename touches only the exact spelling.
func TestVocabularyEditsAgainstAFake(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Id":"admin","Name":"root","Policy":{"IsAdministrator":true}}],"TotalRecordCount":1}`)
	})
	full := map[string]string{
		"1": `{"Id":"1","Name":"Alien","Genres":["Sci-Fi","Horror"],"TagItems":[{"Name":"space","Id":1}],"Studios":[]}`,
		"2": `{"Id":"2","Name":"Aliens","Genres":["sci-fi"],"TagItems":[],"Studios":[{"Name":"Brandywine","Id":4}]}`,
	}
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
		// the server's genre filter is not exact: both spellings come back
		_, _ = io.WriteString(w, `{"Items":[{"Id":"1","Name":"Alien","Genres":["Sci-Fi","Horror"]},{"Id":"2","Name":"Aliens","Genres":["sci-fi"]}],"TotalRecordCount":2}`)
	})
	cs := session(t, f, Options{})

	out, msg := callTool(t, cs, "item_batch_edit", map[string]any{"ids": []any{"1", "2"}, "add_tags": []any{"Classic", "SPACE"}, "remove_genres": []any{"horror"}, "official_rating": "R"})
	if msg != "" || number(t, out["items_updated"], "items_updated") != 2 {
		t.Fatalf("item_batch_edit = %v %s", out, msg)
	}
	for _, want := range []string{`"TagItems":[{"Name":"space"},{"Name":"Classic"}]`, `"Tags":["space","Classic"]`, `"Genres":["Sci-Fi"]`, `"GenreItems":[{"Name":"Sci-Fi"}]`, `"OfficialRating":"R"`} {
		if !strings.Contains(posted["1"], want) {
			t.Errorf("Alien's update lacks %s: %s", want, posted["1"])
		}
	}
	if !strings.Contains(posted["2"], `"Studios":[{"Id":4,"Name":"Brandywine"}]`) {
		t.Errorf("a list the edit did not name was changed: %s", posted["2"])
	}

	clear(posted)
	out, msg = callTool(t, cs, "metadata_rename", map[string]any{"field": "genre", "from": "sci-fi", "to": "Science Fiction"})
	if msg != "" || number(t, out["items_updated"], "items_updated") != 1 {
		t.Fatalf("metadata_rename = %v %s", out, msg)
	}
	if _, touched := posted["1"]; touched || !strings.Contains(posted["2"], `"Genres":["Science Fiction"]`) {
		t.Errorf("the rename posted %v", posted)
	}
	if q := f.requests("/Items")[0].Query; !strings.Contains(q, "Genres=sci-fi") || !strings.Contains(q, "ExcludeItemTypes=") {
		t.Errorf("the rename's lookup = %s", q)
	}

	for args, want := range map[string]string{
		`{"ids":["1"]}`: "nothing to change",
		`{"ids":["1"],"genres":["a"],"add_genres":[]}`:   "",
		`{"ids":["1"],"tags":["a"],"remove_tags":["b"]}`: "one or the other",
	} {
		var a map[string]any
		_ = json.Unmarshal([]byte(args), &a)
		_, msg := callTool(t, cs, "item_batch_edit", a)
		if want == "" && msg != "" || want != "" && !strings.Contains(msg, want) {
			t.Errorf("item_batch_edit %s = %q, want %q", args, msg, want)
		}
	}
}
