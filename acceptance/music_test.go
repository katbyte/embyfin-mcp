//go:build integration

package acceptance

import (
	"slices"
	"strings"
	"testing"
)

// The Music library is what the audio side of the tools has to work with:
// four artists, five albums, twenty tagged one-second tracks, the fetchers
// off so the id3 tags are all either server knows. It carries two defects of
// its own - an album with no cover art, and a genre spelled two ways - so the
// audits can be pointed at music and find something true.

func TestMusicLibrary(t *testing.T) {
	out := call(t, "library_get", map[string]any{"library": "Music"})
	if str(out["collection_type"]) != "music" {
		t.Errorf("Music collection_type = %v", out["collection_type"])
	}
	counts, _ := out["type_counts"].(map[string]any)
	if got := num(t, counts["MusicAlbum"], "type_counts.MusicAlbum"); got != len(albums) {
		t.Errorf("albums = %d, want %d (%v)", got, len(albums), counts)
	}
	if got := num(t, counts["Audio"], "type_counts.Audio"); got != songs() {
		t.Errorf("songs = %d, want %d (%v)", got, songs(), counts)
	}
}

// A music library is browsed by its own kind: library_items returns albums
// where a movie library returns films, and the tags are what they are named
// and dated by.
func TestMusicItems(t *testing.T) {
	out := call(t, "library_items", map[string]any{"library": "Music", "sort": "name", "limit": 50})
	if got := num(t, out["total"], "total"); got != len(albums) {
		t.Errorf("total = %d, want %d", got, len(albums))
	}
	years := map[string]int{}
	for _, row := range rows(t, out["items"], "items") {
		if got := str(row["type"]); got != "MusicAlbum" {
			t.Errorf("%v is a %s, want MusicAlbum", row["name"], got)
		}
		years[str(row["name"])] = num(t, row["year"], "year")
	}
	for _, a := range albums {
		year, ok := years[a.Album]
		if !ok {
			t.Errorf("album %s missing from %v", a.Album, years)
			continue
		}
		if year != a.Year {
			t.Errorf("%s year = %d, want %d", a.Album, year, a.Year)
		}
	}

	// the tracks, by the titles their tags carry
	out = call(t, "library_items", map[string]any{"library": "Music", "types": "Audio", "sort": "name", "limit": 50})
	if got := num(t, out["total"], "total"); got != songs() {
		t.Errorf("songs = %d, want %d", got, songs())
	}
	var got []string
	for _, row := range rows(t, out["items"], "items") {
		got = append(got, str(row["name"]))
	}
	var want []string
	for _, a := range albums {
		want = append(want, a.Tracks...)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("tracks = %v, want %v", got, want)
	}
}

// The vocabulary of a music library is its genres, which come off the tags.
func TestMusicFilters(t *testing.T) {
	out := call(t, "library_filters", map[string]any{"library": "Music", "types": "MusicAlbum"})
	got := map[string]int{}
	for _, row := range rows(t, out["genres"], "genres") {
		got[str(row["value"])] = num(t, row["items"], "items")
	}
	want := map[string]int{}
	for _, a := range albums {
		want[a.Genre]++
	}
	for genre, n := range want {
		if got[genre] != n {
			t.Errorf("genre %s on %d albums, want %d (%v)", genre, got[genre], n, got)
		}
	}
}

// item_instant_mix is the one tool that exists only for music: seeded from a
// song, an album or an artist, it must answer with tracks.
func TestMusicInstantMix(t *testing.T) {
	for _, seed := range []struct{ kind, types, title string }{
		{"song", "Audio", "Belgrade"},
		{"album", "MusicAlbum", "Polygon"},
		{"artist", "MusicArtist", "Battle Tapes"},
	} {
		t.Run(seed.kind, func(t *testing.T) {
			id := findItem(t, "Music", seed.types, seed.title)
			out := call(t, "item_instant_mix", map[string]any{"id": id, "limit": 10})
			items := rows(t, out["items"], "items")
			if len(items) == 0 {
				t.Fatalf("a mix seeded from the %s %s is empty", seed.kind, seed.title)
			}
			for _, row := range items {
				if got := str(row["type"]); got != "Audio" {
					t.Errorf("mix holds a %s (%v), want only Audio", got, row["name"])
				}
			}
		})
	}
}

// Every audit takes types, so a music library can be swept for the same
// defects as a film one - but album art is where the two servers part
// company. Jellyfin gives an album the folder its tracks sit in, so the
// cover.jpg beside them becomes its primary image and the audit finds only
// Thundercolor, the rip that has none. Emby 4.10 builds albums from the tags
// alone and gives them no path at all, so no local image can reach one and
// every album is missing art; the picture embedded in each track only ever
// reaches the songs, and only where the library's "Embedded Images" fetcher
// is on, which a library created with the providers off has not got.
func TestAuditMusicMissingPoster(t *testing.T) {
	out := call(t, "audit_missing_poster", map[string]any{"library": "Music", "types": "MusicAlbum"})
	want := []string{"Thundercolor"}
	if !isJellyfin() {
		want = nil
		for _, a := range albums {
			want = append(want, a.Album)
		}
		slices.Sort(want)
	}
	if got := findings(t, out); !slices.Equal(got, want) {
		t.Errorf("albums with no art = %v, want %v", got, want)
	}
	if got := num(t, out["items_scanned"], "items_scanned"); got != len(albums) {
		t.Errorf("scanned %d albums, want %d", got, len(albums))
	}
}

// The electronic acts are tagged "Electronic" but SirensCeol says
// "Electronica", the kind of drift a tagger leaves behind.
func TestAuditMusicSpelling(t *testing.T) {
	out := call(t, "audit_spelling", map[string]any{"library": "Music", "types": "MusicAlbum", "field": "genres"})
	var found bool
	for _, g := range rows(t, out["groups"], "groups") {
		var spellings []string
		for _, s := range rows(t, g["spellings"], "spellings") {
			spellings = append(spellings, str(s["value"]))
		}
		if slices.Contains(spellings, "Electronic") && slices.Contains(spellings, "Electronica") {
			found = true
			if str(g["field"]) != "genres" {
				t.Errorf("group field = %v, want genres", g["field"])
			}
			if str(g["keep"]) != "Electronic" {
				t.Errorf("keep = %v, want Electronic, the one on two albums", g["keep"])
			}
		}
	}
	if !found {
		t.Errorf("Electronic/Electronica not paired: %v", out["groups"])
	}
}

// A song is an item like any other: item_get reads it back with the facts its
// tags carry, and the MusicBrainz ids come through as provider ids.
func TestMusicItemGet(t *testing.T) {
	id := findItem(t, "Music", "Audio", "Valkyrie")
	out := call(t, "item_get", map[string]any{"id": id})
	if str(out["name"]) != "Valkyrie" || str(out["type"]) != "Audio" {
		t.Errorf("item_get = %v", out)
	}
	if got := strings.ToLower(str(out["container"])); got != "mp3" {
		t.Errorf("container = %v, want mp3", out["container"])
	}
	if genres := strs(t, out["genres"], "genres"); !slices.Contains(genres, "Electronic") {
		t.Errorf("genres = %v, want Electronic", genres)
	}
}
