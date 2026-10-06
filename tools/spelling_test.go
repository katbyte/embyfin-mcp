package tools

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
)

// A value in another script keeps its letters. Folding them away made
// "Zzyzx Studio α" and "Zzyzx Studio β" one spelling of one studio, with a
// merge advised, and left a value written wholly in another script out of
// the audit altogether.
func TestNormKeepsEveryScript(t *testing.T) {
	t.Parallel()

	for _, pair := range [][2]string{
		{"進撃の巨人", "鬼滅の刃"},
		{"Zzyzx Studio α", "Zzyzx Studio β"},
		{"Тихий дом", "Тихий сад"},
	} {
		a, b := norm(pair[0]), norm(pair[1])
		if a == "" || b == "" || a == b {
			t.Errorf("%q and %q are different values: %q against %q", pair[0], pair[1], a, b)
		}
	}
	for in, want := range map[string]string{
		"ТИХИЙ ДОМ":         "тихий дом",
		"Zzyzx Studio Α":    "zzyzx studio α",
		"星の森\u3000特集":       "星の森 特集", // an ideographic space is a space
		"Amélie":            "amelie",
		"Ame\u0301lie":      "amelie", // the accent written as a separate mark
		"  Sci-Fi / Drama ": "sci fi drama",
	} {
		if got := norm(in); got != want {
			t.Errorf("norm(%q) = %q, want %q", in, got, want)
		}
	}

	// a length is counted in letters: two ideographs are too short to call
	// a typo apart, however many bytes they take
	if typoApart(norm("星光"), norm("月光")) || truncationOf(norm("東星"), norm("東星 映画")) {
		t.Error("two-letter values were judged as long ones")
	}
}

// The report over values in other scripts: two spellings of one tag in
// Cyrillic are one group, and two studios a Greek letter apart are never
// one spelling to merge.
func TestSpellingReportAcrossScripts(t *testing.T) {
	t.Parallel()

	counts := newSpellingCounts(vocabFields)
	for _, v := range []struct{ tag, studio string }{
		{"Тихий дом", "Zzyzx Studio α"},
		{"ТИХИЙ ДОМ", "Zzyzx Studio β"},
		{"Тихий дом", "進撃の巨人"},
	} {
		counts.add(&embyfin.Item{Tags: []string{v.tag}, Studios: []embyfin.NameRef{{Name: v.studio}}})
	}

	tags := counts.report(fieldTags)
	if len(tags) != 1 || tags[0].Kind != "spelling" || tags[0].Keep != "Тихий дом" || len(tags[0].Spellings) != 2 {
		t.Errorf("tags = %+v, want the two spellings of one Cyrillic tag", tags)
	}
	for _, g := range counts.report(fieldStudios) {
		if g.Kind == "spelling" {
			t.Errorf("two studios a Greek letter apart were called one spelling: %+v", g)
		}
	}
	if n := len(counts[fieldStudios]); n != 3 {
		t.Errorf("studios counted = %d, want all three, the Japanese one included", n)
	}
}

// Values a word apart are two things, and so are names a mark apart. "teenage
// life" and "teenage love" were a near group, two edits in words too short
// to tell a slip from another word; "Discovery" and "discovery+" one value
// spelled two ways, and "Idea(L)" and "Ideal" too, because the fold drops
// every mark. A plus, a bang or a bracket makes another name - a streaming
// brand, a company - where case, spacing and a separator do not.
func TestSpellingKeepsWordsAndMarksApart(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"teenage life", "teenage love", false},
		{"teenage life", "teenage lift", false},
		{"coming of age", "coming of rage", false},
		{"martial arts film", "martial arts flim", true},
		{"science fiction", "science fictoin", true},
		{"superhero", "superheros", true},
		{"sciencefiction", "science fiction", true},
		{"time travel", "time travell", true},
		{"cyberpunk noir", "cyberpunk nr", true}, // letters dropped from a word's middle
		{"film nior", "film noir", true},         // two letters swapped in a short word
		{"kids flim", "kids film", true},
		{"road tirp", "road trip", true},
		{"teenage love", "teenage lve", true},
		{"coming of age", "coming of ace", false}, // a short word changed: another word
	} {
		if got := typoApart(norm(tc.a), norm(tc.b)); got != tc.want {
			t.Errorf("typoApart(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}

	counts := newSpellingCounts([]string{fieldStudios, fieldTags})
	for _, s := range []string{"Discovery", "DISCOVERY", "discovery+", "Discovery+ Originals", "Idea(L)", "Ideal", "Warner Bros.", "Warner Bros", "Warner Bros. (US)", "Warner Bros. (US", "Yahoo!", "Yahoo"} {
		counts.addValue(fieldStudios, s)
	}
	for _, s := range []string{"teenage life", "teenage love", "Action & Adventure", "Action Adventure"} {
		counts.addValue(fieldTags, s)
	}
	groups := counts.report(fieldStudios)
	studios := make([][]string, 0, len(groups))
	for _, g := range groups {
		var names []string
		for _, sp := range g.Spellings {
			names = append(names, sp.Value)
		}
		slices.Sort(names)
		studios = append(studios, append([]string{g.Kind}, names...))
	}
	// values only a mark apart are a near group to check, never one
	// spelling; the brand and its own longer name are a pair as any two
	// studios are, and the brand and the channel's name are not
	slices.SortFunc(studios, func(a, b []string) int { return strings.Compare(strings.Join(a, "|"), strings.Join(b, "|")) })
	want := [][]string{
		{"contains", "Discovery+ Originals", "discovery+"},
		{"near", "DISCOVERY", "Discovery", "discovery+"},
		{"near", "Idea(L)", "Ideal"},
		{"near", "Warner Bros. (US", "Warner Bros. (US)"},
		{"near", "Yahoo", "Yahoo!"},
		{"spelling", "DISCOVERY", "Discovery"},
		{"spelling", "Warner Bros", "Warner Bros."},
	}
	if !slices.EqualFunc(studios, want, slices.Equal) {
		t.Errorf("studio groups = %v, want %v", studios, want)
	}
	for _, g := range counts.report(fieldStudios) {
		if g.Kind == "near" && !strings.Contains(g.Note, "differ only by a mark") {
			t.Errorf("a group a mark apart says nothing of it: %+v", g)
		}
	}
	var tags []string
	for _, g := range counts.report(fieldTags) {
		for _, sp := range g.Spellings {
			tags = append(tags, g.Kind+" "+sp.Value)
		}
	}
	slices.Sort(tags)
	// an ampersand is no brand: the one genre with it and without
	if want := []string{"spelling Action & Adventure", "spelling Action Adventure"}; !slices.Equal(tags, want) {
		t.Errorf("tag groups = %v, want %v", tags, want)
	}
}

// A studio and a longer name it begins are a pair of their own. Joined into
// clusters through the shorter name, "Warner Bros." made one group of Warner
// Bros. Pictures and Warner Bros. Television, and metadata_rename would have
// merged two companies into the most used on every item. Each pair is still
// reported, as something to check, and says when the shorter name begins
// others too.
func TestSpellingContainsPairsOnly(t *testing.T) {
	t.Parallel()

	counts := newSpellingCounts([]string{fieldStudios})
	for studio, n := range map[string]int{
		"Warner Bros.": 3, "Warner Bros. Pictures": 2, "Warner Bros. Television": 1,
		"Paramount": 2, "Paramount Television": 1, "Paramount Animation": 1,
		"Sony Pictures": 2, "Sony Pictures Classics": 1, "Sony Pictures Television": 1,
		"Legendary Pictures": 1, "Legendary": 1,
	} {
		for range n {
			counts.addValue(fieldStudios, studio)
		}
	}

	pairs := map[[2]string]vocabGroup{}
	for _, g := range counts.report(fieldStudios) {
		if g.Kind != "contains" || len(g.Spellings) != 2 {
			t.Errorf("a group that is not a pair of a name and a longer one = %+v", g)

			continue
		}
		names := [2]string{g.Spellings[0].Value, g.Spellings[1].Value}
		if len(names[0]) > len(names[1]) {
			names[0], names[1] = names[1], names[0]
		}
		pairs[names] = g
	}
	for _, want := range [][2]string{
		{"Warner Bros.", "Warner Bros. Pictures"},
		{"Warner Bros.", "Warner Bros. Television"},
		{"Paramount", "Paramount Television"},
		{"Paramount", "Paramount Animation"},
		{"Sony Pictures", "Sony Pictures Classics"},
		{"Sony Pictures", "Sony Pictures Television"},
		{"Legendary", "Legendary Pictures"},
	} {
		g, ok := pairs[want]
		if !ok {
			t.Errorf("no pair of %q and %q in %v", want[0], want[1], pairs)

			continue
		}
		// a name beginning two others may be their parent company
		if many := want[0] != "Legendary"; many != (g.Note != "") {
			t.Errorf("%v note = %q", want, g.Note)
		}
	}
	if len(pairs) != 7 {
		t.Errorf("%d pairs, want 7: %v", len(pairs), pairs)
	}
	if g := pairs[[2]string{"Warner Bros.", "Warner Bros. Pictures"}]; g.Keep != "Warner Bros." ||
		g.Note != `"Warner Bros." also begins "Warner Bros. Television": it may name their parent company rather than "Warner Bros. Pictures" cut short` {
		t.Errorf("Warner Bros. and its pictures = %+v", g)
	}
}

// Album and artist names, as the tracks' tags carry them: an album spelled
// two ways by one album artist is a group naming that artist, and the same
// slip between two artists' albums is none, their albums never compared; an
// artist's name with and without a leading "The" is one spelling, and two
// short names a letter apart ("Blur", "Blue") are two bands.
func TestSpellingOfAlbumsAndArtists(t *testing.T) {
	t.Parallel()

	r := &spellingResult{counts: spellingCounts{fieldArtists: {}}, albumArtists: map[string]map[string]int{}}
	for range 3 {
		r.addAlbum("Wish You Were Here", "Pink Floyd")
	}
	r.addAlbum("Wish You Where Here", "Pink Floyd")
	r.addAlbum("Zzyzx Greatest Hits", "Zzyzx One")
	r.addAlbum("Zzyzx Greatest Hit", "Zzyzx Two")
	for range 7 {
		r.addArtist("Pink Floyd")
	}
	r.addArtist("The Pink Floyd")
	r.addArtist("Blur")
	r.addArtist("Blue")
	r.addArtist("The The") //nolint:dupword // a band's name

	got := r.groups([]string{fieldAlbums, fieldArtists})
	if len(got) != 2 {
		t.Fatalf("groups = %+v, want the album and the artist", got)
	}
	album, artist := got[0], got[1]
	if album.Field != fieldAlbums || album.Kind != "near" || album.AlbumArtist != "Pink Floyd" || album.Keep != "Wish You Were Here" ||
		!slices.Equal(album.Spellings, []spelling{{"Wish You Were Here", 3}, {"Wish You Where Here", 1}}) {
		t.Errorf("the album = %+v", album)
	}
	if artist.Field != fieldArtists || artist.Kind != "spelling" || artist.AlbumArtist != "" || artist.Keep != "Pink Floyd" ||
		!slices.Equal(artist.Spellings, []spelling{{"Pink Floyd", 7}, {"The Pink Floyd", 1}}) {
		t.Errorf("the artist = %+v", artist)
	}
	if k := artistKey("The The"); k != "the" { //nolint:dupword // a band's name
		t.Errorf("artistKey(The The) = %q, want the name kept", k)
	}
}

// The album and artist names come off the tracks' tags, each spelling
// counting the tracks that carry it; the album entries are never read. When
// no track comes back with its album, album names are not compared and the
// answer says so, rather than reading as nothing spelled two ways; artists
// are compared either way. And metadata_rename refuses them, as names in the
// files' tags.
func TestSpellingReadsTheTracks(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		tracks []map[string]any
		// albums is the album group's keep, "" for no album group, when
		// the albums note must say none was compared
		albums string
	}{
		{
			name: "albums on the tracks",
			tracks: []map[string]any{
				{"Id": "t1", "Type": "Audio", "Name": "One", "Album": "Zzyzx Album", "AlbumArtist": "Zzyzx Band", "Artists": []string{"Zzyzx Band"}},
				{"Id": "t2", "Type": "Audio", "Name": "Two", "Album": "Zzyzx Album", "AlbumArtist": "Zzyzx Band", "Artists": []string{"The Zzyzx Band"}},
				{"Id": "t3", "Type": "Audio", "Name": "Three", "Album": "Zzyzx Albun", "AlbumArtist": "Zzyzx Band", "Artists": []string{"Zzyzx Band"}},
			},
			albums: "Zzyzx Album",
		},
		{
			name: "no album on any track",
			tracks: []map[string]any{
				{"Id": "t1", "Type": "Audio", "Name": "One", "AlbumArtist": "Zzyzx Band", "Artists": []string{"Zzyzx Band"}},
				{"Id": "t2", "Type": "Audio", "Name": "Two", "AlbumArtist": "Zzyzx Band", "Artists": []string{"The Zzyzx Band"}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeServer(t)
			music := map[string]any{"Name": "Tunes", "CollectionType": "music", "ItemId": "lib-music", "Locations": []string{"/media/music"}}
			f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(music)) })
			albumsRead := false
			f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
				switch param(r.URL.Query(), "IncludeItemTypes") {
				case "Audio":
					writeJSON(t, w, page(tc.tracks...))
				case musicAlbum:
					albumsRead = true
					writeJSON(t, w, page())
				default:
					writeJSON(t, w, page())
				}
			})
			adminView(t, f)
			cs := session(t, f, Options{})

			out := mustCall(t, cs, "audit_spelling", map[string]any{})
			if albumsRead || number(t, out["tracks_scanned"], "tracks_scanned") != len(tc.tracks) {
				t.Errorf("album entries read %v, %v tracks scanned: want the %d tracks alone", albumsRead, out["tracks_scanned"], len(tc.tracks))
			}
			fields := map[string]map[string]any{}
			for _, g := range objects(t, out["groups"], "groups") {
				fields[text(g["field"])] = g
			}
			switch g, note := fields[fieldAlbums], text(out["albums_note"]); {
			case tc.albums != "" && (g == nil || text(g["keep"]) != tc.albums || text(g["album_artist"]) != "Zzyzx Band" || note != ""):
				t.Errorf("the album group = %v, albums_note %q", g, note)
			case tc.albums == "" && (g != nil || note != "none of the 2 tracks read came back with an album name, so album names were not compared: whether any is spelled two ways is not known"):
				t.Errorf("with no album on any track the album group = %v, albums_note %q", g, note)
			}
			if g := fields[fieldArtists]; g == nil || text(g["keep"]) != "Zzyzx Band" || text(g["kind"]) != "spelling" {
				t.Errorf("the artist group = %v", g)
			}

			if msg := mustRefuse(t, cs, "metadata_rename", map[string]any{"field": "albums", "from": "Zzyzx Albun", "to": "Zzyzx Album"}); !strings.Contains(msg, "albums are names in the tracks' own tags") {
				t.Errorf("metadata_rename of an album = %s", msg)
			}
		})
	}
}

// A server with no library that can hold music is not read for album and
// artist names: no track is asked for, and none is counted.
func TestSpellingLeavesAFilmServersTracksAlone(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	films := map[string]any{"Name": "Films", "CollectionType": "movies", "ItemId": "lib-films", "Locations": []string{"/media/films"}}
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(films)) })
	tracksAsked := false
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		if kinds := param(r.URL.Query(), "IncludeItemTypes"); kinds == "Audio" || kinds == musicAlbum {
			tracksAsked = true
		}
		writeJSON(t, w, page())
	})
	adminView(t, f)
	out := mustCall(t, session(t, f, Options{}), "audit_spelling", map[string]any{})
	if tracksAsked || number(t, out["tracks_scanned"], "tracks_scanned") != 0 || out["albums_note"] != nil {
		t.Errorf("a film server read for music names: asked %v, answer %v", tracksAsked, out)
	}
}
