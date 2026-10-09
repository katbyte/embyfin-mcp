//go:build integration

package acceptance

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"
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
	out := suite.Call(t, "item_get", map[string]any{"id": id})
	if acc.Str(out["name"]) != "Dune" || acc.Num(t, out["year"], "year") != 2021 || acc.Str(out["type"]) != "Movie" {
		t.Errorf("item_get = %v", out)
	}
	if !strings.HasSuffix(acc.Str(out["path"]), "Dune (2021).mp4") {
		t.Errorf("path = %v", out["path"])
	}
	ids, _ := out["metadata_provider_ids"].(map[string]any)
	if acc.Str(ids["tmdb"]) != "438631" || acc.Str(ids["imdb"]) != "tt1160419" {
		t.Errorf("provider ids = %v", ids)
	}
	if acc.Str(out["overview"]) == "" {
		t.Error("no overview")
	}
	// the probe: an h264 video and an aac audio stream in an mp4
	// the same fields as an episode row, not a sentence to parse back
	if acc.Str(out["video_codec"]) != "h264" || acc.Num(t, out["width"], "width") != 1280 || acc.Num(t, out["height"], "height") != 720 {
		t.Errorf("video = %v %vx%v", out["video_codec"], out["width"], out["height"])
	}
	if acc.Num(t, out["size"], "size") < 1024 {
		t.Errorf("size = %v, want bytes", out["size"])
	}
	// the fixtures are encoded at 5 frames a second, and a test pattern
	// claims no HDR
	if fps, _ := out["frame_rate"].(float64); fps < 4.9 || fps > 5.1 {
		t.Errorf("frame_rate = %v, want the fixture's 5", out["frame_rate"])
	}
	if hdr := acc.Str(out["hdr"]); hdr != "sdr" && hdr != "unknown" {
		t.Errorf("hdr = %q on an SDR test pattern", hdr)
	}
	// audio is fields, not a sentence: the codec reads as the codec whether
	// or not the track is tagged with a language, and the fixture's track is
	// not, which reads as und rather than as nothing
	if a := acc.Rows(t, out["audio"], "audio"); len(a) != 1 || acc.Str(a[0]["codec"]) != "aac" || acc.Str(a[0]["language"]) != "und" || acc.Num(t, a[0]["channels"], "channels") != 1 {
		t.Errorf("audio = %v, want one untagged mono aac track", a)
	}
	if c := acc.Str(out["container"]); c != "mp4" && c != "mov,mp4,m4a,3gp,3g2,mj2" {
		t.Errorf("container = %q", c)
	}
	// seconds, like every duration a tool answers with: the file runs TMDB's
	// 155 minutes for the film, which in minutes would say nothing of the
	// unit
	if n := acc.NumOr0(out["runtime_s"]); n != 155*60 {
		t.Errorf("a file of 155 minutes has runtime_s %v, want %d", out["runtime_s"], 155*60)
	}
	// the nfo's director, as the director
	if !slices.ContainsFunc(acc.Rows(t, out["people"], "people"), func(p map[string]any) bool {
		return acc.Str(p["name"]) == "Denis Villeneuve" && acc.Str(p["type"]) == "Director"
	}) {
		t.Errorf("Denis Villeneuve is not among the people as the director: %v", out["people"])
	}
	// the provider's audience score, out of ten, which only a film the
	// providers matched has; nothing carries a subtitle but The Thirteenth Floor
	if r := acc.Decimal(t, out["community_rating"], "community_rating"); r <= 0 || r > 10 {
		t.Errorf("community_rating = %v, want TMDB's score out of 10", r)
	}
	if out["subtitles"] != nil {
		t.Errorf("Dune has subtitles %v, want none", out["subtitles"])
	}
	messy := suite.Call(t, "item_get", map[string]any{"id": findItem(t, "Messy Movies", "Movie", "Arrival")})
	if messy["community_rating"] != nil || acc.Str(messy["name"]) != "Arrival" {
		t.Errorf("the messy Arrival, which no provider matched, = rating %v", messy["community_rating"])
	}

	for _, bad := range []string{"00000000000000000000000000000000", unknownID()} {
		if msg := suite.CallErr(t, "item_get", map[string]any{"id": bad}); !strings.Contains(msg, "no item with id "+bad) {
			t.Errorf("an unknown id %s: %s", bad, msg)
		}
	}
}

// An external subtitle beside a film is a subtitle track of the film, by the
// language its name carries: The Thirteenth Floor (1999).eng.srt is English,
// which Emby spells en and Jellyfin eng.
func TestItemGetSubtitles(t *testing.T) {
	out := suite.Call(t, "item_get", map[string]any{"id": findItem(t, "Movies", "Movie", "The Thirteenth Floor")})
	want := "en (external)"
	if isJellyfin() {
		want = "eng (external)"
	}
	if got := acc.Strs(t, out["subtitles"], "subtitles"); !slices.Equal(got, []string{want}) {
		t.Errorf("subtitles = %v, want [%s]", got, want)
	}
}

// A film held as two files reads as the file at its path: Jellyfin folds the
// messy Blade Runner's 1080p and 2160p files into one film at its 2160p file,
// which reads 2160 high; Emby keeps them as two films, each read as its own
// file.
func TestItemGetOfTwoFiles(t *testing.T) {
	out := suite.Call(t, "library_items", map[string]any{"library": "Messy Movies", "query": "Blade Runner"})
	var heights []int
	for _, it := range acc.Rows(t, out["items"], "items") {
		got := suite.Call(t, "item_get", map[string]any{"id": acc.Str(it["id"])})
		if acc.Str(got["name"]) != "Blade Runner" {
			t.Errorf("a Blade Runner search found %v", got["name"])
		}
		heights = append(heights, acc.Num(t, got["height"], "height"))
		if w := acc.Num(t, got["width"], "width"); w*9 != acc.Num(t, got["height"], "height")*16 {
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
// neither. The people are cut at fifteen, however many the server holds, and
// the director and the writer are never the ones cut (TestItemGetKeepsTheCrew).
func TestItemGetOfAShow(t *testing.T) {
	bb := findItem(t, "Shows", "Series", "Breaking Bad")
	eps := acc.Rows(t, suite.Call(t, "library_episodes", map[string]any{"series_id": bb})["episodes"], "episodes")
	if len(eps) != 3 {
		t.Fatalf("Breaking Bad has %d episodes, want 3", len(eps))
	}
	pilot := acc.Str(eps[0]["id"])
	ep := suite.Call(t, "item_get", map[string]any{"id": pilot})
	if acc.Str(ep["type"]) != "Episode" || acc.Str(ep["name"]) != "Pilot" || acc.Str(ep["series"]) != "Breaking Bad" || acc.Num(t, ep["season"], "season") != 1 || acc.Num(t, ep["episode"], "episode") != 1 {
		t.Errorf("the pilot = %v", ep)
	}
	// the providers credit the pilot with more guest stars than fit
	if held, _ := fullItem(t, pilot)["People"].([]any); len(held) <= 15 {
		t.Fatalf("the server holds %d people for the pilot, want more than 15 for the cap to show", len(held))
	}
	if n := len(acc.Rows(t, ep["people"], "people")); n != 15 {
		t.Errorf("the pilot lists %d people, want the 15 the cap keeps", n)
	}

	sev := findItem(t, "Shows", "Series", "Severance")
	seasons := acc.Rows(t, suite.Call(t, "show_seasons", map[string]any{"series_id": sev})["seasons"], "seasons")
	if len(seasons) != 2 {
		t.Fatalf("Severance has %d seasons, want 2", len(seasons))
	}
	for _, s := range seasons {
		got := suite.Call(t, "item_get", map[string]any{"id": acc.Str(s["id"])})
		number := acc.Num(t, s["season"], "season")
		if acc.Str(got["type"]) != "Season" || acc.Str(got["series"]) != "Severance" || acc.Num(t, got["season"], "season") != number || got["episode"] != nil {
			t.Errorf("season %d = %v", number, got)
		}
		if !strings.HasSuffix(acc.Str(got["path"]), fmt.Sprintf("/Severance/Season %02d", number)) {
			t.Errorf("season %d is at %v", number, got["path"])
		}
	}

	series := suite.Call(t, "item_get", map[string]any{"id": bb})
	if acc.Str(series["type"]) != "Series" || series["season"] != nil || series["episode"] != nil || acc.Str(series["series"]) != "" {
		t.Errorf("a series reads with a series or numbers of its own: %v", series)
	}
}

func TestItemFindByMetadataID(t *testing.T) {
	// Alien is in the library three times: once clean, twice messy
	out := suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "348", "type": "movie"})
	if found, _ := out["found"].(bool); !found {
		t.Fatalf("tmdb 348 not found: %v", out)
	}
	items := acc.Rows(t, out["items"], "items")
	if len(items) != 3 {
		t.Errorf("tmdb 348 matched %d items, want 3: %v", len(items), items)
	}
	for _, it := range items {
		if acc.Str(it["name"]) != "Alien" {
			t.Errorf("tmdb 348 matched %v", it["name"])
		}
	}
	// the provider and the id are trimmed as well as folded
	if spaced := acc.Rows(t, suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": " TMDB ", "id": " 348 ", "type": " Movie "})["items"], "items"); len(spaced) != 3 {
		t.Errorf("tmdb 348 with spaces round it matched %v", spaced)
	}

	// by imdb, case-insensitively named
	out = suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "IMDB", "id": "tt1160419"})
	if items = acc.Rows(t, out["items"], "items"); len(items) != 1 || acc.Str(items[0]["name"]) != "Dune" {
		t.Errorf("imdb tt1160419 = %v", items)
	}
	// Breaking Bad's IMDb id is on the series, and on the messy Memento by
	// mistake: a film and a series both answer
	got := map[string]string{}
	for _, it := range acc.Rows(t, suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "imdb", "id": "tt0903747"})["items"], "items") {
		got[acc.Str(it["name"])] = acc.Str(it["type"])
	}
	if len(got) != 2 || got["Breaking Bad"] != "Series" || got["Memento"] != "Movie" {
		t.Errorf("imdb tt0903747 = %v, want the Breaking Bad series and the Memento film", got)
	}

	// TMDB numbers films and series apart: its series 1396 is Breaking Bad,
	// and no film the library holds is its film 1396. Looked up without
	// saying which, the series answered a caller asking after a film
	if msg := suite.CallErr(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "1396"}); !strings.Contains(msg, "give type movie or series") {
		t.Errorf("tmdb 1396 with no type = %s, want it refused", msg)
	}
	if film := suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "1396", "type": "movie"}); acc.BoolOf(film["found"]) || len(acc.RowsOf(film["items"])) != 0 {
		t.Errorf("the film tmdb 1396 = %v, want nothing: Breaking Bad is the series 1396", film)
	}
	bb := acc.Rows(t, suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "1396", "type": "series"})["items"], "items")
	if len(bb) != 1 || acc.Str(bb[0]["name"]) != "Breaking Bad" || acc.Str(bb[0]["type"]) != "Series" {
		t.Errorf("the series tmdb 1396 = %v, want Breaking Bad", bb)
	}

	// a series by tvdb
	out = suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tvdb", "id": "371980", "type": "series"})
	items = acc.Rows(t, out["items"], "items")
	var series int
	for _, it := range items {
		if acc.Str(it["name"]) == "Severance" && acc.Str(it["type"]) == "Series" {
			series++
		}
	}
	if series != 2 || len(items) != 2 { // the clean one and the messy one
		t.Errorf("tvdb 371980 = %v", items)
	}
	// a provider outside the three the tool names: .hack//Liminality is
	// known by its AniDB id alone
	anidb := acc.Rows(t, suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "anidb", "id": "222"})["items"], "items")
	if len(anidb) != 1 || acc.Str(anidb[0]["name"]) != ".hack//Liminality" || acc.Str(anidb[0]["type"]) != "Series" {
		t.Errorf("anidb 222 = %v", anidb)
	}

	out = suite.Call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "1", "type": "movie"})
	if found, _ := out["found"].(bool); found || len(acc.RowsOf(out["items"])) != 0 {
		t.Errorf("tmdb 1 = %v", out)
	}
}

func TestItemSimilar(t *testing.T) {
	id := findItem(t, "Movies", "Movie", "Alien")
	out := suite.Call(t, "item_similar", map[string]any{"id": id, "limit": 3})
	items := acc.Rows(t, out["items"], "items")
	if len(items) == 0 || len(items) > 3 {
		t.Fatalf("similar = %v", items)
	}
	for _, it := range items {
		if acc.Str(it["id"]) == id {
			t.Error("an item is similar to itself")
		}
		if acc.Str(it["id"]) == "" || acc.Str(it["name"]) == "" {
			t.Errorf("similar row = %v", it)
		}
	}
	// as another user
	out = suite.Call(t, "item_similar", map[string]any{"id": id, "user": "alice"})
	if len(acc.Rows(t, out["items"], "items")) == 0 {
		t.Error("nothing similar for alice")
	}
	if msg := suite.CallErr(t, "item_similar", map[string]any{"id": id, "user": "nobody"}); !strings.Contains(msg, "nobody") {
		t.Errorf("an unknown user: %s", msg)
	}
	// an id nothing has is not an item nothing is similar to
	if msg := suite.CallErr(t, "item_similar", map[string]any{"id": unknownID()}); !strings.Contains(msg, "no item with id "+unknownID()) {
		t.Errorf("an unknown id: %s", msg)
	}
}

// item_refresh re-reading an item and keeping an edit is a journey
// (TestEditsSurviveARefreshAndAScan); here, an id nothing has is refused
// by name rather than answered with the server's bare error.
func TestItemRefresh(t *testing.T) {
	for _, replace := range []bool{false, true} {
		if msg := suite.CallErr(t, "item_refresh", map[string]any{"id": unknownID(), "replace_all": replace}); !strings.Contains(msg, "no item with id "+unknownID()) {
			t.Errorf("refreshing an unknown id (replace_all %v): %s", replace, msg)
		}
	}
}

func TestItemInstantMix(t *testing.T) {
	// a film is not music: neither server mixes anything from one, and the
	// tool answers with the empty mix rather than failing. The mixes that
	// mean something are seeded from the music library (TestMusicInstantMix).
	id := findItem(t, "Movies", "Movie", "Alien")
	out := suite.Call(t, "item_instant_mix", map[string]any{"id": id, "limit": 5})
	if items := acc.Rows(t, out["items"], "items"); len(items) != 0 {
		t.Errorf("a mix seeded from a film = %v", items)
	}
	// and an id nothing has is not a seed that mixes nothing
	if msg := suite.CallErr(t, "item_instant_mix", map[string]any{"id": unknownID()}); !strings.Contains(msg, "no item with id "+unknownID()) {
		t.Errorf("an unknown id: %s", msg)
	}
}

func TestItemEdit(t *testing.T) {
	id := findItem(t, "Messy Movies", "Movie", "Interstellar")
	before := suite.Call(t, "item_get", map[string]any{"id": id})
	stored := fullItem(t, id)
	// everything the test changes goes back, so the audits and the tests
	// after it find the film as the fixtures laid it out: the sort name and
	// its lock as the server keeps them, which no tool clears
	t.Cleanup(func() {
		if _, err := suite.Invoke("item_edit", map[string]any{
			"ids": []any{id}, "name": "Interstellar", "year": 2014,
			"overview": before["overview"], "genres": before["genres"], "tags": before["tags"],
		}); err != nil {
			t.Errorf("putting Interstellar back: %v", err)
		}
		updateItem(t, id, map[string]any{"SortName": stored["SortName"], "ForcedSortName": stored["ForcedSortName"], "LockedFields": stored["LockedFields"]})
	})

	out := suite.Call(t, "item_edit", map[string]any{
		"ids": []any{id}, "overview": "Edited by the acceptance suite.", "genres": []any{"Science Fiction", "Adventure"}, "tags": []any{"acceptance"},
	})
	updated := acc.Strs(t, out["changed"], "changed")
	slices.Sort(updated)
	if !slices.Equal(updated, []string{"Genres", "Overview", "Tags"}) {
		t.Errorf("updated = %v", updated)
	}
	got := suite.Call(t, "item_get", map[string]any{"id": id})
	if acc.Str(got["overview"]) != "Edited by the acceptance suite." {
		t.Errorf("overview after edit = %q", got["overview"])
	}
	if g := acc.Strs(t, got["genres"], "genres"); !slices.Equal(g, []string{"Science Fiction", "Adventure"}) {
		t.Errorf("genres after edit = %v", g)
	}
	if tags := acc.Strs(t, got["tags"], "tags"); !slices.Equal(tags, []string{"acceptance"}) {
		t.Errorf("tags after edit = %v", tags)
	}

	// a title and year
	out = suite.Call(t, "item_edit", map[string]any{"ids": []any{id}, "name": "Interstellar (edited)", "year": 2015, "sort_name": "interstellar edited"})
	updated = acc.Strs(t, out["changed"], "changed")
	if !slices.Contains(updated, "Name") || !slices.Contains(updated, "ProductionYear") || !slices.Contains(updated, "SortName") {
		t.Errorf("updated = %v", updated)
	}
	got = suite.Call(t, "item_get", map[string]any{"id": id})
	if acc.Str(got["name"]) != "Interstellar (edited)" || acc.Num(t, got["year"], "year") != 2015 {
		t.Errorf("after the title edit = %v", got)
	}
	// the sort name is kept, which no read tool shows, so it is read off
	// the item as the server's own editor does: Emby works it out from the
	// name again on every save unless it is locked
	if full := fullItem(t, id); acc.Str(full["ForcedSortName"]) != "interstellar edited" {
		t.Errorf("the sort name = %v (forced %v, locked %v), want interstellar edited", full["SortName"], full["ForcedSortName"], full["LockedFields"])
	}

	if msg := suite.CallErr(t, "item_edit", map[string]any{"ids": []any{id}}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("an empty edit: %s", msg)
	}
	// an id nothing has fails naming it, and says nothing was changed
	if msg := suite.CallErr(t, "item_edit", map[string]any{"ids": []any{unknownID()}, "overview": "Zzyzx: nowhere."}); !strings.HasPrefix(msg, "item_edit: "+unknownID()+": ") || !strings.Contains(msg, "nothing was changed") {
		t.Errorf("an edit of an unknown id: %s", msg)
	}
}
