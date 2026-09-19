//go:build integration

package acceptance

import (
	"slices"
	"strings"
	"testing"
)

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
	// or not the track is tagged with a language
	if a := rows(t, out["audio"], "audio"); len(a) != 1 || str(a[0]["codec"]) != "aac" || str(a[0]["language"]) == "" || num(t, a[0]["channels"], "channels") <= 0 {
		t.Errorf("audio = %v", a)
	}
	if c := str(out["container"]); c != "mp4" && c != "mov,mp4,m4a,3gp,3g2,mj2" {
		t.Errorf("container = %q", c)
	}
	// seconds, like every duration a tool answers with: in minutes this file
	// read as 0, which said nothing about the unit at all
	if n := numOr0(out["runtime_s"]); n < 1 || n > 2 {
		t.Errorf("a one-second file has runtime_s %v", out["runtime_s"])
	}
	// people come from the nfo (and the provider): the director at least
	var director bool
	for _, p := range rows(t, out["people"], "people") {
		if str(p["name"]) == "Denis Villeneuve" {
			director = true
		}
	}
	if !director {
		t.Errorf("Denis Villeneuve missing from people: %v", out["people"])
	}

	if msg := callErr(t, "item_get", map[string]any{"id": "00000000000000000000000000000000"}); !strings.Contains(msg, "no item") {
		t.Errorf("an unknown id: %s", msg)
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

	// by imdb, case-insensitively named
	out = call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "IMDB", "id": "tt1160419"})
	if items = rows(t, out["items"], "items"); len(items) != 1 || str(items[0]["name"]) != "Dune" {
		t.Errorf("imdb tt1160419 = %v", items)
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
	if series != 2 { // the clean one and the messy one
		t.Errorf("tvdb 371980 = %v", items)
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
}

func TestItemEdit(t *testing.T) {
	id := findItem(t, "Messy Movies", "Movie", "Interstellar")
	out := call(t, "item_edit", map[string]any{
		"id": id, "overview": "Edited by the acceptance suite.", "genres": []any{"Science Fiction", "Adventure"}, "tags": []any{"acceptance"},
	})
	updated := strs(t, out["updated_fields"], "updated_fields")
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
	out = call(t, "item_edit", map[string]any{"id": id, "name": "Interstellar (edited)", "year": 2015, "sort_name": "interstellar edited"})
	updated = strs(t, out["updated_fields"], "updated_fields")
	if !slices.Contains(updated, "Name") || !slices.Contains(updated, "ProductionYear") || !slices.Contains(updated, "SortName") {
		t.Errorf("updated = %v", updated)
	}
	got = call(t, "item_get", map[string]any{"id": id})
	if str(got["name"]) != "Interstellar (edited)" || num(t, got["year"], "year") != 2015 {
		t.Errorf("after the title edit = %v", got)
	}
	// and back, so the audits still find what they expect
	call(t, "item_edit", map[string]any{"id": id, "name": "Interstellar", "year": 2014, "sort_name": "Interstellar"})

	if msg := callErr(t, "item_edit", map[string]any{"id": id}); !strings.Contains(msg, "no fields") {
		t.Errorf("an empty edit: %s", msg)
	}
}

func TestItemRefresh(t *testing.T) {
	// refreshing an item in a provider-on library re-asks TMDB through the
	// proxy; the assertion is that it is accepted and the item survives
	id := findItem(t, "Movies", "Movie", "Princess Mononoke")
	out := call(t, "item_refresh", map[string]any{"id": id})
	if str(out["refreshed"]) != id {
		t.Errorf("item_refresh = %v", out)
	}
	out = call(t, "item_refresh", map[string]any{"id": id, "replace_all": true})
	if str(out["refreshed"]) != id {
		t.Errorf("item_refresh replace_all = %v", out)
	}
	if err := waitForScan(); err != nil {
		t.Fatal(err)
	}
	got := call(t, "item_get", map[string]any{"id": id})
	if str(got["name"]) != "Princess Mononoke" || num(t, got["year"], "year") != 1997 {
		t.Errorf("after refresh = %v", got)
	}
}

func TestItemInstantMix(t *testing.T) {
	// a film is not music: the mix is empty on Jellyfin and a list on Emby,
	// and the tool must answer either way. The mixes that mean something are
	// seeded from the music library (TestMusicInstantMix).
	id := findItem(t, "Movies", "Movie", "Alien")
	out := call(t, "item_instant_mix", map[string]any{"id": id, "limit": 5})
	if _, ok := out["items"].([]any); !ok {
		t.Errorf("instant mix = %v", out)
	}
}

// The item family is complete: nothing may be added without a test here.
func TestItemFamilyIsComplete(t *testing.T) {
	var got []string
	for _, name := range toolNames(t) {
		if strings.HasPrefix(name, "item_") {
			got = append(got, name)
		}
	}
	want := []string{
		"item_artwork", "item_artwork_set", "item_batch_edit", "item_delete", "item_edit", "item_find_by_metadata_id", "item_get",
		"item_identify", "item_identify_apply", "item_instant_mix", "item_last_watched", "item_orphans_delete", "item_refresh",
		"item_set_favourite", "item_set_progress", "item_set_watched", "item_similar", "item_subtitle_download", "item_subtitle_search",
		"item_watch_history",
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("item tools = %v, want %v", got, want)
	}
}
