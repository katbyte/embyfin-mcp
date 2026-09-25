//go:build integration

package acceptance

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// unknownID is an item id in the server's own shape that names nothing:
// Emby numbers its items, Jellyfin gives them Guids. Not the empty Guid,
// which Jellyfin reads as its root folder.
func unknownID() string {
	if isJellyfin() {
		return "0badc0de0badc0de0badc0de0badc0de"
	}

	return "999999999"
}

func TestItemGet(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Dune")
	out := call(t, "item_get", map[string]any{"id": id})
	if str(out["name"]) != "Dune" || num(t, out["year"], "year") != 2021 || str(out["type"]) != "Movie" {
		t.Errorf("item_get = %v", out)
	}
	if !strings.HasSuffix(str(out["path"]), "Dune (2021).mp4") {
		t.Errorf("path = %v", out["path"])
	}
	ids, _ := out["metadata_provider_ids"].(map[string]any)
	if str(ids["tmdb"]) != "438631" || str(ids["imdb"]) != "tt1160419" {
		t.Errorf("provider ids = %v", ids)
	}
	if str(out["overview"]) == "" {
		t.Error("no overview")
	}
	// the probe: an h264 video and an aac audio stream in an mp4
	// the same fields as an episode row, not a sentence to parse back
	if str(out["video_codec"]) != "h264" || num(t, out["width"], "width") != 1280 || num(t, out["height"], "height") != 720 {
		t.Errorf("video = %v %vx%v", out["video_codec"], out["width"], out["height"])
	}
	if num(t, out["size"], "size") < 1024 {
		t.Errorf("size = %v, want bytes", out["size"])
	}
	// the fixtures are encoded at 5 frames a second, and a test pattern
	// claims no HDR
	if fps, _ := out["frame_rate"].(float64); fps < 4.9 || fps > 5.1 {
		t.Errorf("frame_rate = %v, want the fixture's 5", out["frame_rate"])
	}
	if hdr := str(out["hdr"]); hdr != "sdr" && hdr != "unknown" {
		t.Errorf("hdr = %q on an SDR test pattern", hdr)
	}
	// audio is fields, not a sentence: the codec reads as the codec whether
	// or not the track is tagged with a language, and the fixture's track is
	// not, which reads as und rather than as nothing
	if a := rows(t, out["audio"], "audio"); len(a) != 1 || str(a[0]["codec"]) != "aac" || str(a[0]["language"]) != "und" || num(t, a[0]["channels"], "channels") != 1 {
		t.Errorf("audio = %v, want one untagged mono aac track", a)
	}
	if c := str(out["container"]); c != "mp4" && c != "mov,mp4,m4a,3gp,3g2,mj2" {
		t.Errorf("container = %q", c)
	}
	// seconds, like every duration a tool answers with: in minutes this file
	// read as 0, which said nothing about the unit at all
	if n := numOr0(out["runtime_s"]); n < 1 || n > 2 {
		t.Errorf("a one-second file has runtime_s %v", out["runtime_s"])
	}
	// the nfo's director, as the director
	if !slices.ContainsFunc(rows(t, out["people"], "people"), func(p map[string]any) bool {
		return str(p["name"]) == "Denis Villeneuve" && str(p["type"]) == "Director"
	}) {
		t.Errorf("Denis Villeneuve is not among the people as the director: %v", out["people"])
	}
	// the provider's audience score, out of ten, which only a film the
	// providers matched has; nothing carries a subtitle but The Thirteenth Floor
	if r := decimal(t, out["community_rating"], "community_rating"); r <= 0 || r > 10 {
		t.Errorf("community_rating = %v, want TMDB's score out of 10", r)
	}
	if out["subtitles"] != nil {
		t.Errorf("Dune has subtitles %v, want none", out["subtitles"])
	}
	messy := call(t, "item_get", map[string]any{"id": findItem(t, "Messy Movies", "Movie", "Arrival")})
	if messy["community_rating"] != nil || str(messy["name"]) != "Arrival" {
		t.Errorf("the messy Arrival, which no provider matched, = rating %v", messy["community_rating"])
	}

	for _, bad := range []string{"00000000000000000000000000000000", unknownID()} {
		if msg := callErr(t, "item_get", map[string]any{"id": bad}); !strings.Contains(msg, "no item with id "+bad) {
			t.Errorf("an unknown id %s: %s", bad, msg)
		}
	}
}

// An external subtitle beside a film is a subtitle track of the film, by the
// language its name carries: The Thirteenth Floor (1999).eng.srt is English,
// which Emby spells en and Jellyfin eng.
func TestItemGetSubtitles(t *testing.T) {
	out := call(t, "item_get", map[string]any{"id": findItem(t, "Movies", "Movie", "The Thirteenth Floor")})
	want := "en (external)"
	if isJellyfin() {
		want = "eng (external)"
	}
	if got := strs(t, out["subtitles"], "subtitles"); !slices.Equal(got, []string{want}) {
		t.Errorf("subtitles = %v, want [%s]", got, want)
	}
}

// A film held as two files reads as its best: Jellyfin folds the messy Blade
// Runner's 1080p and 2160p files into one film, which reads 2160 high; Emby
// keeps them as two films, each read as its own file.
func TestItemGetOfTwoFiles(t *testing.T) {
	out := call(t, "library_items", map[string]any{"library": "Messy Movies", "query": "Blade Runner"})
	var heights []int
	for _, it := range rows(t, out["items"], "items") {
		got := call(t, "item_get", map[string]any{"id": str(it["id"])})
		if str(got["name"]) != "Blade Runner" {
			t.Errorf("a Blade Runner search found %v", got["name"])
		}
		heights = append(heights, num(t, got["height"], "height"))
		if w := num(t, got["width"], "width"); w*9 != num(t, got["height"], "height")*16 {
			t.Errorf("%dx%v is not the file's 16:9", w, got["height"])
		}
	}
	slices.Sort(heights)
	want := []int{1080, 2160}
	if versionsMerged() {
		want = []int{2160}
	}
	if !slices.Equal(heights, want) {
		t.Errorf("the messy Blade Runner reads %v high, want %v", heights, want)
	}
}

// An episode, a season and a series each read with what places them: an
// episode its series and numbers, a season its series and number, a series
// neither. The people are capped at fifteen, however many the server holds.
func TestItemGetOfAShow(t *testing.T) {
	bb := findItem(t, "Shows", "Series", "Breaking Bad")
	eps := rows(t, call(t, "library_episodes", map[string]any{"series_id": bb})["episodes"], "episodes")
	if len(eps) != 3 {
		t.Fatalf("Breaking Bad has %d episodes, want 3", len(eps))
	}
	pilot := str(eps[0]["id"])
	ep := call(t, "item_get", map[string]any{"id": pilot})
	if str(ep["type"]) != "Episode" || str(ep["name"]) != "Pilot" || str(ep["series"]) != "Breaking Bad" || num(t, ep["season"], "season") != 1 || num(t, ep["episode"], "episode") != 1 {
		t.Errorf("the pilot = %v", ep)
	}
	// the providers credit the pilot with more guest stars than fit
	if held, _ := fullItem(t, pilot)["People"].([]any); len(held) <= 15 {
		t.Fatalf("the server holds %d people for the pilot, want more than 15 for the cap to show", len(held))
	}
	if n := len(rows(t, ep["people"], "people")); n != 15 {
		t.Errorf("the pilot lists %d people, want the 15 the cap keeps", n)
	}

	sev := findItem(t, "Shows", "Series", "Severance")
	seasons := rows(t, call(t, "show_seasons", map[string]any{"series_id": sev})["seasons"], "seasons")
	if len(seasons) != 2 {
		t.Fatalf("Severance has %d seasons, want 2", len(seasons))
	}
	for _, s := range seasons {
		got := call(t, "item_get", map[string]any{"id": str(s["id"])})
		number := num(t, s["season"], "season")
		if str(got["type"]) != "Season" || str(got["series"]) != "Severance" || num(t, got["season"], "season") != number || got["episode"] != nil {
			t.Errorf("season %d = %v", number, got)
		}
		if !strings.HasSuffix(str(got["path"]), fmt.Sprintf("/Severance/Season %02d", number)) {
			t.Errorf("season %d is at %v", number, got["path"])
		}
	}

	series := call(t, "item_get", map[string]any{"id": bb})
	if str(series["type"]) != "Series" || series["season"] != nil || series["episode"] != nil || str(series["series"]) != "" {
		t.Errorf("a series reads with a series or numbers of its own: %v", series)
	}
}

func TestItemFindByMetadataID(t *testing.T) {
	// Alien is in the library three times: once clean, twice messy
	out := call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "348"})
	if found, _ := out["found"].(bool); !found {
		t.Fatalf("tmdb 348 not found: %v", out)
	}
	items := rows(t, out["items"], "items")
	if len(items) != 3 {
		t.Errorf("tmdb 348 matched %d items, want 3: %v", len(items), items)
	}
	for _, it := range items {
		if str(it["name"]) != "Alien" {
			t.Errorf("tmdb 348 matched %v", it["name"])
		}
	}
	// the provider and the id are trimmed as well as folded
	if spaced := rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": " TMDB ", "id": " 348 "})["items"], "items"); len(spaced) != 3 {
		t.Errorf("tmdb 348 with spaces round it matched %v", spaced)
	}

	// by imdb, case-insensitively named
	out = call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "IMDB", "id": "tt1160419"})
	if items = rows(t, out["items"], "items"); len(items) != 1 || str(items[0]["name"]) != "Dune" {
		t.Errorf("imdb tt1160419 = %v", items)
	}
	// Breaking Bad's IMDb id is on the series, and on the messy Memento by
	// mistake: a film and a series both answer
	got := map[string]string{}
	for _, it := range rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "imdb", "id": "tt0903747"})["items"], "items") {
		got[str(it["name"])] = str(it["type"])
	}
	if len(got) != 2 || got["Breaking Bad"] != "Series" || got["Memento"] != "Movie" {
		t.Errorf("imdb tt0903747 = %v, want the Breaking Bad series and the Memento film", got)
	}

	// a series by tvdb
	out = call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tvdb", "id": "371980"})
	items = rows(t, out["items"], "items")
	var series int
	for _, it := range items {
		if str(it["name"]) == "Severance" && str(it["type"]) == "Series" {
			series++
		}
	}
	if series != 2 || len(items) != 2 { // the clean one and the messy one
		t.Errorf("tvdb 371980 = %v", items)
	}
	// a provider outside the three the tool names: .hack//Liminality is
	// known by its AniDB id alone
	anidb := rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "anidb", "id": "222"})["items"], "items")
	if len(anidb) != 1 || str(anidb[0]["name"]) != ".hack//Liminality" || str(anidb[0]["type"]) != "Series" {
		t.Errorf("anidb 222 = %v", anidb)
	}

	out = call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "1"})
	if found, _ := out["found"].(bool); found || len(rowsOf(out["items"])) != 0 {
		t.Errorf("tmdb 1 = %v", out)
	}
}

func TestItemSimilar(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Alien")
	out := call(t, "item_similar", map[string]any{"id": id, "limit": 3})
	items := rows(t, out["items"], "items")
	if len(items) == 0 || len(items) > 3 {
		t.Fatalf("similar = %v", items)
	}
	for _, it := range items {
		if str(it["id"]) == id {
			t.Error("an item is similar to itself")
		}
		if str(it["id"]) == "" || str(it["name"]) == "" {
			t.Errorf("similar row = %v", it)
		}
	}
	// as another user
	out = call(t, "item_similar", map[string]any{"id": id, "user": "alice"})
	if len(rows(t, out["items"], "items")) == 0 {
		t.Error("nothing similar for alice")
	}
	if msg := callErr(t, "item_similar", map[string]any{"id": id, "user": "nobody"}); !strings.Contains(msg, "nobody") {
		t.Errorf("an unknown user: %s", msg)
	}
	// an id nothing has is not an item nothing is similar to
	if msg := callErr(t, "item_similar", map[string]any{"id": unknownID()}); !strings.Contains(msg, "no item with id "+unknownID()) {
		t.Errorf("an unknown id: %s", msg)
	}
}

// item_refresh re-reading an item and keeping an edit is a journey
// (TestEditsSurviveARefreshAndAScan); here, an id nothing has is refused
// by name rather than answered with the server's bare error.
func TestItemRefresh(t *testing.T) {
	for _, replace := range []bool{false, true} {
		if msg := callErr(t, "item_refresh", map[string]any{"id": unknownID(), "replace_all": replace}); !strings.Contains(msg, "no item with id "+unknownID()) {
			t.Errorf("refreshing an unknown id (replace_all %v): %s", replace, msg)
		}
	}
}

func TestItemInstantMix(t *testing.T) {
	// a film is not music: neither server mixes anything from one, and the
	// tool answers with the empty mix rather than failing. The mixes that
	// mean something are seeded from the music library (TestMusicInstantMix).
	id := findItem(t, "Movies", "Movie", "Alien")
	out := call(t, "item_instant_mix", map[string]any{"id": id, "limit": 5})
	if items := rows(t, out["items"], "items"); len(items) != 0 {
		t.Errorf("a mix seeded from a film = %v", items)
	}
	// and an id nothing has is not a seed that mixes nothing
	if msg := callErr(t, "item_instant_mix", map[string]any{"id": unknownID()}); !strings.Contains(msg, "no item with id "+unknownID()) {
		t.Errorf("an unknown id: %s", msg)
	}
}

func TestItemEdit(t *testing.T) {
	id := findItem(t, "Messy Movies", "Movie", "Interstellar")
	before := call(t, "item_get", map[string]any{"id": id})
	stored := fullItem(t, id)
	// everything the test changes goes back, so the audits and the tests
	// after it find the film as the fixtures laid it out: the sort name and
	// its lock as the server keeps them, which no tool clears
	t.Cleanup(func() {
		if _, err := invoke("item_edit", map[string]any{
			"ids": []any{id}, "name": "Interstellar", "year": 2014,
			"overview": before["overview"], "genres": before["genres"], "tags": before["tags"],
		}); err != nil {
			t.Errorf("putting Interstellar back: %v", err)
		}
		updateItem(t, id, map[string]any{"SortName": stored["SortName"], "ForcedSortName": stored["ForcedSortName"], "LockedFields": stored["LockedFields"]})
	})

	out := call(t, "item_edit", map[string]any{
		"ids": []any{id}, "overview": "Edited by the acceptance suite.", "genres": []any{"Science Fiction", "Adventure"}, "tags": []any{"acceptance"},
	})
	updated := strs(t, out["changed"], "changed")
	slices.Sort(updated)
	if !slices.Equal(updated, []string{"Genres", "Overview", "Tags"}) {
		t.Errorf("updated = %v", updated)
	}
	got := call(t, "item_get", map[string]any{"id": id})
	if str(got["overview"]) != "Edited by the acceptance suite." {
		t.Errorf("overview after edit = %q", got["overview"])
	}
	if g := strs(t, got["genres"], "genres"); !slices.Equal(g, []string{"Science Fiction", "Adventure"}) {
		t.Errorf("genres after edit = %v", g)
	}
	if tags := strs(t, got["tags"], "tags"); !slices.Equal(tags, []string{"acceptance"}) {
		t.Errorf("tags after edit = %v", tags)
	}

	// a title and year
	out = call(t, "item_edit", map[string]any{"ids": []any{id}, "name": "Interstellar (edited)", "year": 2015, "sort_name": "interstellar edited"})
	updated = strs(t, out["changed"], "changed")
	if !slices.Contains(updated, "Name") || !slices.Contains(updated, "ProductionYear") || !slices.Contains(updated, "SortName") {
		t.Errorf("updated = %v", updated)
	}
	got = call(t, "item_get", map[string]any{"id": id})
	if str(got["name"]) != "Interstellar (edited)" || num(t, got["year"], "year") != 2015 {
		t.Errorf("after the title edit = %v", got)
	}
	// the sort name is kept, which no read tool shows, so it is read off
	// the item as the server's own editor does: Emby works it out from the
	// name again on every save unless it is locked
	if full := fullItem(t, id); str(full["ForcedSortName"]) != "interstellar edited" {
		t.Errorf("the sort name = %v (forced %v, locked %v), want interstellar edited", full["SortName"], full["ForcedSortName"], full["LockedFields"])
	}

	if msg := callErr(t, "item_edit", map[string]any{"ids": []any{id}}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("an empty edit: %s", msg)
	}
	// an id nothing has fails naming it, and says nothing was changed
	if msg := callErr(t, "item_edit", map[string]any{"ids": []any{unknownID()}, "overview": "Zzyzx: nowhere."}); !strings.HasPrefix(msg, "item_edit: "+unknownID()+": ") || !strings.Contains(msg, "(0 items were updated before it)") {
		t.Errorf("an edit of an unknown id: %s", msg)
	}
}
