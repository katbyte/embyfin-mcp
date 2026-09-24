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

// A genre spelled two ways across a music library, merged: audit_spelling
// pairs Electronica with Electronic, metadata_rename moves SirensCeol's album
// and tracks onto Electronic, and the audit comes back clean - and stays
// clean through a library scan, which re-reads a file's tags only when the
// file has changed, and these have not.
func TestAMusicRenameSurvivesAScan(t *testing.T) {
	carriers := func(genre string) []string {
		var ids []string
		for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Music", "types": "MusicArtist,MusicAlbum,Audio", "genres": []any{genre}, "limit": 50})["items"], "items") {
			ids = append(ids, str(it["id"]))
		}
		slices.Sort(ids)
		return ids
	}
	// Afterworld and its four tracks, and on Emby the artist too: Emby gives
	// an artist the genres of its albums, Jellyfin leaves an artist's empty
	want := 5
	if !isJellyfin() {
		want = 6
	}
	sirens := carriers("Electronica")
	if len(sirens) != want {
		t.Fatalf("Electronica is on %d items, want %d", len(sirens), want)
	}
	t.Cleanup(func() {
		for _, id := range sirens {
			_, _ = invoke("item_edit", map[string]any{"ids": []any{id}, "genres": []any{"Electronica"}})
		}
	})
	paired := func() bool {
		for _, g := range rows(t, call(t, "audit_spelling", map[string]any{"library": "Music", "types": "MusicAlbum", "field": "genres"})["groups"], "groups") {
			var spellings []string
			for _, s := range rows(t, g["spellings"], "spellings") {
				spellings = append(spellings, str(s["value"]))
			}
			if slices.Contains(spellings, "Electronica") {
				return true
			}
		}
		return false
	}
	if !paired() {
		t.Fatal("audit_spelling does not pair Electronica with Electronic to start with")
	}

	out := call(t, "metadata_rename", map[string]any{"field": "genre", "from": "Electronica", "to": "Electronic", "library": "Music"})
	if num(t, out["updated"], "updated") != len(sirens) || str(out["field"]) != "genres" {
		t.Errorf("metadata_rename = %v, want the %d items that carried it", out, len(sirens))
	}
	if got := carriers("Electronica"); len(got) != 0 {
		t.Errorf("after the rename Electronica is still on %v", got)
	}
	if paired() {
		t.Error("after the rename audit_spelling still pairs Electronica")
	}
	electronic := func() int {
		return valueCounts(t, call(t, "library_filters", map[string]any{"library": "Music", "types": "MusicAlbum"})["genres"], "genres")["Electronic"]
	}
	if n := electronic(); n != 3 {
		t.Errorf("library_filters counts Electronic on %d albums, want the three electronic acts'", n)
	}

	if scan := call(t, "library_scan", map[string]any{"library": "Music"}); !boolOf(scan["started"]) {
		t.Fatalf("library_scan = %v", scan)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
	if !holds(func() bool { return !paired() && electronic() == 3 }) {
		t.Errorf("a scan undid the rename: Electronic on %d albums, the pair back %v", electronic(), paired())
	}
}
