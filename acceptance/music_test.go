//go:build integration

package acceptance

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// The Music library is what the audio side of the tools has to work with:
// four artists, five albums, twenty tagged one-second tracks, the fetchers
// off so the id3 tags are all either server knows. It carries two defects of
// its own - an album with no cover art, and a genre spelled two ways - so the
// audits can be pointed at music and find something true, and the tags carry
// more (TestMusicTagDefects).

func TestMusicLibrary(t *testing.T) {
	out := call(t, "library_get", map[string]any{"library": "Music"})
	if str(out["collection_type"]) != "music" {
		t.Errorf("Music collection_type = %v", out["collection_type"])
	}
	counts, _ := out["type_counts"].(map[string]any)
	if got := num(t, counts["MusicAlbum"], "type_counts.MusicAlbum"); got != musicAlbums() {
		t.Errorf("albums = %d, want %d (%v)", got, musicAlbums(), counts)
	}
	if got := num(t, counts["Audio"], "type_counts.Audio"); got != songs() {
		t.Errorf("songs = %d, want %d (%v)", got, songs(), counts)
	}
	if got := num(t, counts["MusicArtist"], "type_counts.MusicArtist"); got != artists() {
		t.Errorf("artists = %d, want %d (%v)", got, artists(), counts)
	}
}

// A music library is browsed by its own kind: library_items returns albums
// where a movie library returns films, and the tags are what they are named
// and dated by.
func TestMusicItems(t *testing.T) {
	out := call(t, "library_items", map[string]any{"library": "Music", "sort": "name", "limit": 50})
	if got := num(t, out["total"], "total"); got != musicAlbums() {
		t.Errorf("total = %d, want %d", got, musicAlbums())
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
	// and on Emby the album made of the track whose album is misspelt
	if !isJellyfin() {
		want["Progressive Rock"]++
	}
	for genre, n := range want {
		if got[genre] != n {
			t.Errorf("genre %s on %d albums, want %d (%v)", genre, got[genre], n, got)
		}
	}
}

// item_instant_mix is the one tool that exists only for music: seeded from a
// song, an album, an artist, a genre or a playlist of songs, it must answer
// with tracks, no more of them than the limit.
func TestMusicInstantMix(t *testing.T) {
	var genre string
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Music", "types": "MusicGenre"})["items"], "items") {
		if str(it["name"]) == "Electronic" {
			genre = str(it["id"])
		}
	}
	if genre == "" {
		t.Fatal("the music library has no Electronic genre to seed a mix with")
	}
	seeds := []struct{ kind, id string }{
		{"song", findItem(t, "Music", "Audio", "Belgrade")},
		{"album", findItem(t, "Music", "MusicAlbum", "Polygon")},
		{"artist", findItem(t, "Music", "MusicArtist", "Battle Tapes")},
		{"genre", genre},
	}
	// Emby mixes nothing from a playlist asked the way the tool asks
	// (TestKnownBugPlaylistMixOnEmby)
	if isJellyfin() {
		seeds = append(seeds, struct{ kind, id string }{"playlist", songPlaylist(t)})
	}
	for _, seed := range seeds {
		t.Run(seed.kind, func(t *testing.T) {
			out := call(t, "item_instant_mix", map[string]any{"id": seed.id, "limit": 10})
			items := rows(t, out["items"], "items")
			if len(items) == 0 {
				t.Fatalf("a mix seeded from the %s is empty", seed.kind)
			}
			for _, row := range items {
				if got := str(row["type"]); got != "Audio" {
					t.Errorf("mix holds a %s (%v), want only Audio", got, row["name"])
				}
			}
			// a limit below what the mix would hold caps it
			if len(items) > 2 {
				if capped := rows(t, call(t, "item_instant_mix", map[string]any{"id": seed.id, "limit": 2})["items"], "items"); len(capped) != 2 {
					t.Errorf("a mix seeded from the %s with limit 2 = %d tracks", seed.kind, len(capped))
				}
			}
		})
	}
}

// songPlaylist makes a playlist of two songs for the length of a test, and
// returns its id.
func songPlaylist(t *testing.T) string {
	t.Helper()

	id := str(call(t, "playlist_create", map[string]any{
		"name": "Zzyzx Mix Seed", "media_type": "Audio",
		"item_ids": []any{findItem(t, "Music", "Audio", "Belgrade"), findItem(t, "Music", "Audio", "Time")},
	})["id"])
	deleteLater(t, "playlist_delete", "playlist", id)

	return id
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
	var want []string
	for _, a := range albums {
		if !a.Cover || !isJellyfin() {
			want = append(want, a.Album)
		}
	}
	// and on Emby the album made of the one track whose album is misspelt
	if !isJellyfin() {
		want = append(want, misspeltAlbum)
	}
	slices.Sort(want)
	if isJellyfin() && len(want) != 1 {
		t.Fatalf("the fixtures hold %v without art, want the one album", want)
	}
	if got := findings(t, out); !slices.Equal(got, want) {
		t.Errorf("albums with no art = %v, want %v", got, want)
	}
	if got := num(t, out["items_scanned"], "items_scanned"); got != musicAlbums() {
		t.Errorf("scanned %d albums, want %d", got, musicAlbums())
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
	if genres := strs(t, out["genres"], "genres"); !slices.Equal(genres, []string{"Electronic"}) {
		t.Errorf("genres = %v, want Electronic", genres)
	}
	// the MusicBrainz ids its tags carry, keyed as every provider id is
	polygon := albums[0]
	ids := object(t, out["metadata_provider_ids"], "metadata_provider_ids")
	if str(ids["musicbrainzartist"]) != polygon.MBArtist || str(ids["musicbrainzalbum"]) != polygon.MBAlbum {
		t.Errorf("provider ids = %v, want Battle Tapes' artist id %s and Polygon's release id %s", ids, polygon.MBArtist, polygon.MBAlbum)
	}
	// and on Emby the artist carries its own; Jellyfin gives an artist made
	// from the tags none
	artist := call(t, "item_get", map[string]any{"id": findItem(t, "Music", "MusicArtist", "Battle Tapes")})
	artistIDs, _ := artist["metadata_provider_ids"].(map[string]any)
	want := polygon.MBArtist
	if isJellyfin() {
		want = ""
	}
	if got := str(artistIDs["musicbrainzartist"]); got != want {
		t.Errorf("Battle Tapes' MusicBrainz id = %q, want %q", got, want)
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

// item_instant_mix seeded from a playlist: Emby answers a mix of a playlist
// asked through its items' route with nothing, and the tool asks its
// playlists' route instead.
func TestInstantMixFromAPlaylist(t *testing.T) {
	if items := rows(t, call(t, "item_instant_mix", map[string]any{"id": songPlaylist(t)})["items"], "items"); len(items) == 0 {
		t.Error("a mix seeded from a playlist of two songs is empty")
	}
}

// The tags carry the defects a tagger leaves, one album's worth each, and
// what each server makes of them is pinned here: Polygon's album artist is
// Various Artists over Battle Tapes' tracks; Thundercolor carries no
// MusicBrainz ids, its third track is numbered 2 and its fourth not at all;
// The Dark Side of the Moon is two discs in two folders, each numbered from
// one; and on Wish You Were Here one track's album is misspelt Wish You Where
// Here and another's artist is spelled The Pink Floyd.
//
// Emby builds albums from the tags alone, so the misspelt one is an album of
// its own; Jellyfin builds them from folders, so it is one track of the
// album it sits in. Both make artists of every name the tags carry. Nothing
// here tells the two Pink Floyds apart, or the two spellings of the album:
// audit_spelling reads genres, tags and studios, not the names of artists or
// albums.
func TestMusicTagDefects(t *testing.T) {
	namesOf := func(kind string) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Music", "types": kind, "limit": 100})["items"], "items") {
			out[str(it["name"])] = it
		}

		return out
	}
	albumNames := namesOf("MusicAlbum")
	if _, split := albumNames[misspeltAlbum]; split == isJellyfin() || len(albumNames) != musicAlbums() {
		t.Errorf("albums = %v, want %d, the misspelt one among them only on Emby", sorted(slices.Collect(maps.Keys(albumNames))), musicAlbums())
	}
	artistNames := namesOf("MusicArtist")
	for _, name := range []string{variousArtists, pinkFloydAgain, "Pink Floyd", "Battle Tapes"} {
		if artistNames[name] == nil {
			t.Errorf("no artist %q among %v", name, sorted(slices.Collect(maps.Keys(artistNames))))
		}
	}
	if len(artistNames) != artists() {
		t.Errorf("artists = %d, want %d", len(artistNames), artists())
	}
	// the second spelling carries Pink Floyd's MusicBrainz id: from the
	// track's tag on Emby, and on Jellyfin from MusicBrainz itself, which it
	// asks about an artist with no folder of its own whatever the library's
	// fetchers say; neither joins it to Pink Floyd by that id
	again := call(t, "item_get", map[string]any{"id": str(artistNames[pinkFloydAgain]["id"])})
	if ids, _ := again["metadata_provider_ids"].(map[string]any); str(ids["musicbrainzartist"]) != albums[2].MBArtist {
		t.Errorf("%s's ids = %v, want Pink Floyd's MusicBrainz id", pinkFloydAgain, again["metadata_provider_ids"])
	}

	track := func(title string) map[string]any {
		t.Helper()
		return call(t, "item_get", map[string]any{"id": findItem(t, "Music", "Audio", title)})
	}
	// the numbers as tagged: a track numbered twice and one not numbered,
	// and the second disc's tracks numbered from one again
	for title, want := range map[string]any{"Stay With You": 2.0, "This Is How You Know": 2.0, "Changing Guard": nil, "Speak to Me": 1.0, "Breathe": 2.0, "On the Run": 1.0, "Time": 2.0} {
		if got := track(title)["episode"]; got != want {
			t.Errorf("%s is track %v, want %v as tagged", title, got, want)
		}
	}
	// the rip nothing was looked up for has no ids, down to its artist
	if ids := track("Diving At Night")["metadata_provider_ids"]; ids != nil {
		t.Errorf("Thundercolor's track carries %v, want no ids", ids)
	}
	if ids := call(t, "item_get", map[string]any{"id": str(albumNames["Thundercolor"]["id"])})["metadata_provider_ids"]; ids != nil {
		t.Errorf("Thundercolor carries %v, want no ids", ids)
	}
	// and two discs are one album holding both
	var discs []string
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Music", "types": "Audio", "limit": 100})["items"], "items") {
		if path := str(it["path"]); strings.Contains(path, "/The Dark Side of the Moon (1973)/") {
			discs = append(discs, path[strings.Index(path, "(1973)/")+7:])
		}
	}
	if want := []string{"Disc 1/01 - Speak to Me.mp3", "Disc 1/02 - Breathe.mp3", "Disc 2/01 - On the Run.mp3", "Disc 2/02 - Time.mp3"}; !slices.Equal(sorted(discs), want) {
		t.Errorf("The Dark Side of the Moon's tracks = %v, want %v", sorted(discs), want)
	}

	// what audit_spelling reads in a music library: the genre spelled two
	// ways, and neither artist's nor album's name
	spelling := call(t, "audit_spelling", map[string]any{"library": "Music", "types": "MusicAlbum"})
	if groups := rows(t, spelling["groups"], "groups"); len(groups) != 1 || str(groups[0]["field"]) != "genres" {
		t.Errorf("audit_spelling over Music = %v, want the Electronica pair alone", groups)
	}
}
