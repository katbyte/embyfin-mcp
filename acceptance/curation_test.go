//go:build integration

package acceptance

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// names lists the "name" of each row in a field, in order.
func names(t *testing.T, v any, field string) []string {
	t.Helper()

	out := []string{}
	for _, row := range rows(t, v, field) {
		out = append(out, title(str(row["name"])))
	}

	return out
}

// valueCounts reads a library_filters list into value -> items.
func valueCounts(t *testing.T, v any, field string) map[string]int {
	t.Helper()

	out := map[string]int{}
	for _, row := range rows(t, v, field) {
		out[str(row["value"])] = num(t, row["items"], field+".items")
	}

	return out
}

func TestLibraryItems(t *testing.T) {
	// a movie library lists its films, by name unless told otherwise
	out := call(t, "library_items", map[string]any{"library": "Movies", "limit": 50})
	got := names(t, out["items"], "items")
	if num(t, out["total"], "total") != 8 || len(got) != 8 {
		t.Fatalf("Movies = %d of %v: %v", len(got), out["total"], got)
	}
	sorted := slices.Clone(got)
	slices.SortFunc(sorted, func(a, b string) int { return strings.Compare(sortName(a), sortName(b)) })
	if !slices.Equal(got, sorted) {
		t.Errorf("not in name order: %v", got)
	}

	// paging: the second page of three is the fourth to sixth
	page := call(t, "library_items", map[string]any{"library": "Movies", "limit": 3, "offset": 3})
	if p := names(t, page["items"], "items"); !slices.Equal(p, got[3:6]) || num(t, page["offset"], "offset") != 3 || num(t, page["total"], "total") != 8 {
		t.Errorf("page two = %v (offset %v total %v), want %v", p, page["offset"], page["total"], got[3:6])
	}

	// filters: genres, years, and the two together
	out = call(t, "library_items", map[string]any{"library": "Movies", "genres": []any{"Science Fiction"}})
	if got := names(t, out["items"], "items"); !slices.Equal(got, []string{"Blade Runner", "Dune", "Dune: Part Two", "The Thirteenth Floor"}) {
		t.Errorf("Science Fiction = %v", got)
	}
	out = call(t, "library_items", map[string]any{"library": "Movies", "genres": []any{"Horror", "Animation"}})
	if got := names(t, out["items"], "items"); !slices.Equal(got, []string{"Alien", "Princess Mononoke"}) {
		t.Errorf("Horror or Animation = %v", got)
	}
	out = call(t, "library_items", map[string]any{"library": "Movies", "years": []any{1982, 1997}})
	if got := names(t, out["items"], "items"); !slices.Equal(got, []string{"Blade Runner", "Princess Mononoke"}) {
		t.Errorf("1982 and 1997 = %v", got)
	}
	out = call(t, "library_items", map[string]any{"library": "Movies", "years": []any{1982, 1997}, "genres": []any{"Science Fiction"}})
	if got := names(t, out["items"], "items"); !slices.Equal(got, []string{"Blade Runner"}) {
		t.Errorf("1982 and 1997 science fiction = %v", got)
	}

	// tags: none of the films carries one until one is put on two of them,
	// and either of two tags matches
	alien, arrival := findItem(t, "Movies", "Movie", "Alien"), findItem(t, "Movies", "Movie", "Arrival")
	call(t, "item_edit", map[string]any{"ids": []any{alien}, "add_tags": []any{"zzyzx-tagged"}})
	call(t, "item_edit", map[string]any{"ids": []any{arrival}, "add_tags": []any{"zzyzx-other"}})
	t.Cleanup(func() {
		_, _ = invoke("item_edit", map[string]any{"ids": []any{alien, arrival}, "remove_tags": []any{"zzyzx-tagged", "zzyzx-other"}})
	})
	out = call(t, "library_items", map[string]any{"library": "Movies", "tags": []any{"zzyzx-tagged"}})
	if got := names(t, out["items"], "items"); !slices.Equal(got, []string{"Alien"}) || num(t, out["total"], "total") != 1 {
		t.Errorf("tag zzyzx-tagged = %v", got)
	}
	out = call(t, "library_items", map[string]any{"library": "Movies", "tags": []any{"zzyzx-tagged", "zzyzx-other"}})
	if got := names(t, out["items"], "items"); !slices.Equal(got, []string{"Alien", "Arrival"}) {
		t.Errorf("either tag = %v, want [Alien Arrival]", got)
	}

	// a sort: newest first
	out = call(t, "library_items", map[string]any{"library": "Movies", "sort": "year", "desc": true, "limit": 2})
	if got := names(t, out["items"], "items"); !slices.Equal(got, []string{"Dune: Part Two", "Dune"}) {
		t.Errorf("newest = %v", got)
	}

	// every rating and studio library_filters reports lists that many items
	filters := call(t, "library_filters", map[string]any{"library": "Movies"})
	for field, arg := range map[string]string{"official_ratings": "official_ratings", "studios": "studios"} {
		counts := valueCounts(t, filters[field], field)
		if len(counts) == 0 {
			t.Errorf("Movies has no %s to filter on", field)
		}
		for value, n := range counts {
			out := call(t, "library_items", map[string]any{"library": "Movies", arg: []any{value}, "limit": 50})
			if total := num(t, out["total"], "total"); total != n {
				t.Errorf("%s %q lists %d items, library_filters counted %d", field, value, total, n)
			}
		}
	}

	// watch state, in a user's view
	mononoke := findItem(t, "Movies", "Movie", "Princess Mononoke")
	call(t, "item_set_state", map[string]any{"id": mononoke, "user": "alice", "watched": true})
	t.Cleanup(func() {
		_, _ = invoke("item_set_state", map[string]any{"id": mononoke, "user": "alice", "watched": false})
	})
	out = call(t, "library_items", map[string]any{"library": "Movies", "watched": "watched", "user": "alice"})
	if got := names(t, out["items"], "items"); !slices.Equal(got, []string{"Princess Mononoke"}) {
		t.Errorf("alice has watched %v, want [Princess Mononoke]", got)
	}
	out = call(t, "library_items", map[string]any{"library": "Movies", "watched": "unwatched", "user": "alice"})
	if n := num(t, out["total"], "total"); n != 7 {
		t.Errorf("alice has %d unwatched, want 7", n)
	}
	out = call(t, "library_items", map[string]any{"library": "Movies", "watched": "watched"})
	if n := num(t, out["total"], "total"); n != 0 {
		t.Errorf("root (the default user) has watched %d", n)
	}

	// a TV library lists series
	out = call(t, "library_items", map[string]any{"library": "Shows"})
	for _, it := range rows(t, out["items"], "items") {
		if str(it["type"]) != "Series" {
			t.Errorf("Shows listed a %v", it["type"])
		}
	}

	for _, bad := range []map[string]any{{"sort": "loudness"}, {"watched": "twice"}, {"library": "Nope"}} {
		if msg := callErr(t, "library_items", bad); !strings.Contains(msg, "loudness") && !strings.Contains(msg, "twice") && !strings.Contains(msg, "Nope") {
			t.Errorf("library_items %v: %s", bad, msg)
		}
	}
}

// sortName is how both servers order titles: without a leading article.
func sortName(name string) string {
	for _, article := range []string{"The ", "A ", "An "} {
		if rest, ok := strings.CutPrefix(name, article); ok {
			return strings.ToLower(rest)
		}
	}

	return strings.ToLower(name)
}

func TestLibraryFilters(t *testing.T) {
	out := call(t, "library_filters", map[string]any{"library": "Movies"})
	if n := num(t, out["items_scanned"], "items_scanned"); n != 8 {
		t.Errorf("scanned %d, want 8", n)
	}
	genres := valueCounts(t, out["genres"], "genres")
	for genre, want := range map[string]int{"Science Fiction": 4, "Horror": 1, "Animation": 1, "Drama": 1, "Action": 1} {
		if genres[genre] != want {
			t.Errorf("genre %s on %d films, want %d: %v", genre, genres[genre], want, genres)
		}
	}
	// most used first
	if first := rows(t, out["genres"], "genres")[0]; str(first["value"]) != "Science Fiction" {
		t.Errorf("the most used genre = %v", first)
	}
	years := map[int]int{}
	for _, row := range rows(t, out["years"], "years") {
		years[num(t, row["year"], "year")] = num(t, row["items"], "items")
	}
	if years[1982] != 1 || years[1999] != 1 || len(years) != 8 {
		t.Errorf("years = %v", years)
	}
	if len(rows(t, out["official_ratings"], "official_ratings")) == 0 || len(rows(t, out["studios"], "studios")) == 0 {
		t.Errorf("the provider filled no ratings or studios: %v %v", out["official_ratings"], out["studios"])
	}

	// the messy series carry only what their nfo says, and nothing else:
	// Severance's Drama, Zzyzx Paths' Science Fiction and Gaiden's
	// Science-Fiction
	out = call(t, "library_filters", map[string]any{"library": "Messy Shows", "types": "Series"})
	if g := valueCounts(t, out["genres"], "genres"); len(g) != 3 || g["Drama"] != 1 || g["Science Fiction"] != 1 || g["Science-Fiction"] != 1 {
		t.Errorf("Messy Shows genres = %v, want Drama, Science Fiction and Science-Fiction on one each", g)
	}
	if n := num(t, out["items_scanned"], "items_scanned"); n != messySeries {
		t.Errorf("Messy Shows filters read %d series, want %d", n, messySeries)
	}
	if len(rows(t, out["tags"], "tags")) != 0 || len(rows(t, out["studios"], "studios")) != 0 {
		t.Errorf("Messy Shows tags %v studios %v, want none", out["tags"], out["studios"])
	}
}

func TestLibraryEdit(t *testing.T) {
	created := call(t, "library_create", map[string]any{"name": "Edit Me", "type": "tvshows", "paths": []any{"/media/messy-shows"}})
	if str(created["name"]) != "Edit Me" {
		t.Fatalf("library_create = %v", created)
	}
	name := "Edit Me"
	t.Cleanup(func() {
		_, _ = invoke("library_delete", map[string]any{"library": name, "confirm": true})
		_ = waitForScan() // Jellyfin's removal starts a library scan
	})

	out := call(t, "library_edit", map[string]any{"library": "edit me", "add_paths": []any{"/media/shows"}, "remove_paths": []any{"/media/messy-shows"}})
	if locs := strs(t, out["locations"], "locations"); !slices.Equal(locs, []string{"/media/shows"}) {
		t.Errorf("locations = %v, want [/media/shows]", locs)
	}
	if changed := strs(t, out["changed"], "changed"); len(changed) != 2 {
		t.Errorf("changed = %v", changed)
	}

	out = call(t, "library_edit", map[string]any{"library": "Edit Me", "name": "Edited"})
	name = "Edited"
	if str(out["name"]) != "Edited" || !slices.Contains(strs(t, out["changed"], "changed"), "renamed Edit Me to Edited") {
		t.Errorf("rename = %v", out)
	}
	listed := map[string]bool{}
	for _, row := range rows(t, call(t, "library_list", nil)["libraries"], "libraries") {
		listed[str(row["name"])] = true
	}
	if !listed["Edited"] || listed["Edit Me"] {
		t.Errorf("libraries after the rename = %v", listed)
	}

	for want, args := range map[string]map[string]any{
		"already holds": {"library": "Edited", "add_paths": []any{"/media/shows"}},
		"has no folder": {"library": "Edited", "remove_paths": []any{"/media/nowhere"}},
		"nothing to":    {"library": "Edited"},
	} {
		if msg := callErr(t, "library_edit", args); !strings.Contains(msg, want) {
			t.Errorf("library_edit %v: %s", args, msg)
		}
	}
}

// item_edit makes the same change on many items in one call.
func TestItemEditMany(t *testing.T) {
	arrival := findItem(t, "Messy Movies", "Movie", "Arrival")
	dune := findItem(t, "Messy Movies", "Movie", "Dune")
	before := map[string]map[string]any{}
	for _, id := range []string{arrival, dune} {
		before[id] = call(t, "item_get", map[string]any{"id": id})
	}
	t.Cleanup(func() {
		for id, b := range before {
			_, _ = invoke("item_edit", map[string]any{"ids": []any{id}, "genres": b["genres"], "tags": b["tags"], "studios": b["studios"]})
		}
	})

	out := call(t, "item_edit", map[string]any{
		"ids":        []any{arrival, dune},
		"add_genres": []any{"Mystery"}, "add_tags": []any{"watchlist", "alien"}, "studios": []any{"Paramount Pictures"},
		"official_rating": "R",
	})
	if num(t, out["updated"], "updated") != 2 || !slices.Equal(strs(t, out["items"], "items"), []string{"Arrival", "Dune"}) {
		t.Errorf("item_edit = %v", out)
	}
	// the fields changed, sorted
	if !slices.Equal(strs(t, out["changed"], "changed"), []string{"Genres", "OfficialRating", "Studios", "Tags"}) {
		t.Errorf("item_edit changed = %v", out["changed"])
	}
	for id, b := range before {
		got := call(t, "item_get", map[string]any{"id": id})
		genres := strs(t, got["genres"], "genres")
		// the item's own genres stay, the new one is added
		for _, g := range append(strs(t, b["genres"], "genres"), "Mystery") {
			if !slices.Contains(genres, g) {
				t.Errorf("%s genres = %v, want %s among them", got["name"], genres, g)
			}
		}
		if tags := strs(t, got["tags"], "tags"); !slices.Contains(tags, "watchlist") || !slices.Contains(tags, "alien") {
			t.Errorf("%s tags = %v", got["name"], tags)
		}
		if studios := strs(t, got["studios"], "studios"); !slices.Equal(studios, []string{"Paramount Pictures"}) {
			t.Errorf("%s studios = %v", got["name"], studios)
		}
		if str(got["official_rating"]) != "R" {
			t.Errorf("%s rating = %v", got["name"], got["official_rating"])
		}
	}

	// remove what was added, case-insensitively, leaving the rest
	call(t, "item_edit", map[string]any{"ids": []any{arrival, dune}, "remove_genres": []any{"mystery"}, "remove_tags": []any{"WATCHLIST"}})
	got := call(t, "item_get", map[string]any{"id": dune})
	if slices.Contains(strs(t, got["genres"], "genres"), "Mystery") || slices.Contains(strs(t, got["tags"], "tags"), "watchlist") || !slices.Contains(strs(t, got["tags"], "tags"), "alien") {
		t.Errorf("after removing = genres %v tags %v", got["genres"], got["tags"])
	}

	for want, args := range map[string]map[string]any{
		"one or the other":            {"ids": []any{dune}, "tags": []any{"a"}, "add_tags": []any{"b"}},
		"nothing to change":           {"ids": []any{dune}},
		`missing properties: ["ids"]`: {"add_tags": []any{"b"}},
		// a title, sort title, overview or year is one item's own
		"name is one item's own":     {"ids": []any{arrival, dune}, "name": "Zzyzx"},
		"overview is one item's own": {"ids": []any{arrival, dune}, "overview": "Zzyzx"},
		"year is one item's own":     {"ids": []any{arrival, dune}, "year": 2000},
	} {
		if msg := callErr(t, "item_edit", args); !strings.Contains(msg, want) {
			t.Errorf("item_edit %v: %s", args, msg)
		}
	}
}

// audit_spelling finds variants the batch editor plants, metadata_rename
// merges them, and the audit comes back clean.
func TestAuditSpellingAndMetadataRename(t *testing.T) {
	dune := findItem(t, "Messy Movies", "Movie", "Dune")
	interstellar := findItem(t, "Messy Movies", "Movie", "Interstellar")
	taken := findItem(t, "Messy Movies", "Movie", "Zzyzx Taken Down")
	t.Cleanup(func() {
		for _, id := range []string{dune, interstellar} {
			_, _ = invoke("item_edit", map[string]any{
				"ids": []any{id}, "remove_genres": []any{"Science-Fiction"}, "remove_tags": []any{"Sci-Fi", "Sci Fi"}, "remove_studios": []any{"Syncopy", "Syncopy Films"},
			})
		}
		// the merge below takes Taken Down's spelling too: put it back
		_, _ = invoke("item_edit", map[string]any{"ids": []any{taken}, "genres": []any{"Science-Fiction"}})
	})

	// the one the fixtures carry to start with: Taken Down's nfo spells the
	// genre Science-Fiction where the other films say Science Fiction
	out := call(t, "audit_spelling", map[string]any{"library": "Messy Movies"})
	lasting := rows(t, out["groups"], "groups")
	if n := num(t, out["total_findings"], "total_findings"); n != 1 || len(lasting) != 1 || str(lasting[0]["field"]) != "genres" || str(lasting[0]["keep"]) != "Science Fiction" {
		t.Fatalf("Messy Movies starts with %d spelling groups: %v, want Taken Down's genre alone", n, out["groups"])
	}
	counts := map[string]int{}
	for _, s := range rows(t, lasting[0]["spellings"], "spellings") {
		counts[str(s["value"])] = num(t, s["items"], "items")
	}
	// Blade Runner counts once where it is one film, twice on Emby
	carriers := 3
	if !versionsMerged() {
		carriers = 4
	}
	if counts["Science Fiction"] != carriers || counts["Science-Fiction"] != 1 || len(counts) != 2 {
		t.Errorf("the genre's spellings = %v, want Dune's, Interstellar's and Blade Runner's against Taken Down's", counts)
	}

	// both nfos say Science Fiction, and Dune gets it hyphenated as well
	call(t, "item_edit", map[string]any{"ids": []any{dune}, "add_genres": []any{"Science-Fiction"}, "add_tags": []any{"Sci-Fi"}, "add_studios": []any{"Syncopy"}})
	call(t, "item_edit", map[string]any{"ids": []any{interstellar}, "add_tags": []any{"Sci Fi"}, "add_studios": []any{"Syncopy Films"}})

	out = call(t, "audit_spelling", map[string]any{"library": "Messy Movies"})
	groups := map[string]map[string]any{}
	for _, g := range rows(t, out["groups"], "groups") {
		groups[str(g["field"])] = g
	}
	if n := num(t, out["total_findings"], "total_findings"); n != 3 || len(groups) != 3 {
		t.Fatalf("groups = %v", out["groups"])
	}
	spellings := func(g map[string]any) []string {
		var out []string
		for _, s := range rows(t, g["spellings"], "spellings") {
			out = append(out, str(s["value"]))
		}
		slices.Sort(out)
		return out
	}
	if g := groups["genres"]; str(g["kind"]) != "spelling" || str(g["keep"]) != "Science Fiction" || !slices.Equal(spellings(g), []string{"Science Fiction", "Science-Fiction"}) {
		t.Errorf("genres group = %v", g)
	}
	if g := groups["tags"]; str(g["kind"]) != "spelling" || !slices.Equal(spellings(g), []string{"Sci Fi", "Sci-Fi"}) {
		t.Errorf("tags group = %v", g)
	}
	if g := groups["studios"]; str(g["kind"]) != "contains" || !slices.Equal(spellings(g), []string{"Syncopy", "Syncopy Films"}) {
		t.Errorf("studios group = %v", g)
	}
	// one field at a time
	if n := num(t, call(t, "audit_spelling", map[string]any{"library": "Messy Movies", "field": "tag"})["total_findings"], "total_findings"); n != 1 {
		t.Errorf("tags alone = %d groups", n)
	}
	// and a limit caps the groups, not the count
	if capped := call(t, "audit_spelling", map[string]any{"library": "Messy Movies", "limit": 1}); len(rows(t, capped["groups"], "groups")) != 1 || num(t, capped["total_findings"], "total_findings") != 3 {
		t.Errorf("limit 1 = %v", capped)
	}

	// merge each: a rename onto a spelling, a rename in place, a removal
	out = call(t, "metadata_rename", map[string]any{"field": "genres", "from": "Science-Fiction", "to": "Science Fiction", "library": "Messy Movies"})
	if num(t, out["updated"], "updated") != 2 || !slices.Equal(sorted(strs(t, out["items"], "items")), []string{"Dune", "Zzyzx Taken Down"}) {
		t.Errorf("genre rename = %v, want Dune and Taken Down", out)
	}
	out = call(t, "metadata_rename", map[string]any{"field": "tags", "from": "Sci Fi", "to": "Sci-Fi"})
	if num(t, out["updated"], "updated") != 1 {
		t.Errorf("tag rename = %v", out)
	}
	out = call(t, "metadata_rename", map[string]any{"field": "studios", "from": "Syncopy Films", "remove": true, "library": "Messy Movies"})
	if num(t, out["updated"], "updated") != 1 {
		t.Errorf("studio removal = %v", out)
	}
	got := call(t, "item_get", map[string]any{"id": dune})
	if genres := strs(t, got["genres"], "genres"); slices.Contains(genres, "Science-Fiction") || !slices.Contains(genres, "Science Fiction") {
		t.Errorf("Dune genres after the merge = %v", genres)
	}
	if tags := strs(t, call(t, "item_get", map[string]any{"id": interstellar})["tags"], "tags"); !slices.Equal(tags, []string{"Sci-Fi"}) {
		t.Errorf("Interstellar tags after the rename = %v", tags)
	}

	if n := num(t, call(t, "audit_spelling", map[string]any{"library": "Messy Movies"})["total_findings"], "total_findings"); n != 0 {
		t.Errorf("after merging, %d groups remain", n)
	}
	// renaming what nothing carries changes nothing
	if out := call(t, "metadata_rename", map[string]any{"field": "genres", "from": "Nonexistent", "to": "Drama"}); num(t, out["updated"], "updated") != 0 {
		t.Errorf("renaming an unused genre = %v", out)
	}

	for want, args := range map[string]map[string]any{
		"unknown field": {"field": "narrators", "from": "a", "to": "b"},
		"either to or":  {"field": "tags", "from": "a", "to": "b", "remove": true},
		"to is require": {"field": "tags", "from": "a"},
	} {
		if msg := callErr(t, "metadata_rename", args); !strings.Contains(msg, want) {
			t.Errorf("metadata_rename %v: %s", args, msg)
		}
	}
	if msg := callErr(t, "audit_spelling", map[string]any{"field": "narrators"}); !strings.Contains(msg, "unknown field") {
		t.Errorf("audit_spelling narrators: %s", msg)
	}
}

func TestAuditQuality(t *testing.T) {
	// the messy films are 360p rips, Princess Mononoke's in MPEG-4 part 2,
	// the loose DVD a 480p MPEG-2 VOB, and the Blade Runner files really are
	// 1080p and 2160p
	out := call(t, "audit_quality", map[string]any{"library": "Messy Movies"})
	got := findings(t, out)
	want := []string{"Alien", "Alien", "Arrival", "Dune", "Interstellar", "Princess Mononoke", "Zzyzx Crossed Wires", "Zzyzx Night Ferry", "Zzyzx Taken Down"}
	if !slices.Equal(got, want) {
		t.Errorf("low quality = %v, want %v", got, want)
	}
	for _, f := range rows(t, out["findings"], "findings") {
		detail := str(f["detail"])
		switch title(str(f["name"])) {
		case "Zzyzx Night Ferry":
			if detail != "mpeg2video 720x480: 480p, below 720p; legacy codec mpeg2video" {
				t.Errorf("the DVD = %q", detail)
			}
		case "Princess Mononoke":
			if detail != "mpeg4 640x360: 360p, below 720p; legacy codec mpeg4" {
				t.Errorf("Princess Mononoke = %q", detail)
			}
		default:
			if detail != "h264 640x360: 360p, below 720p" {
				t.Errorf("finding = %v", f)
			}
		}
	}
	if n := num(t, out["items_scanned"], "items_scanned"); n != messyMovies() {
		t.Errorf("scanned %d, want %d", n, messyMovies())
	}
	// the Blu-ray kept whole was never probed, so it has no picture to judge,
	// and it says so rather than passing for a good copy
	unprobed := rows(t, out["unprobed"], "unprobed")
	if len(unprobed) != 1 || num(t, out["total_unprobed"], "total_unprobed") != 1 || title(str(unprobed[0]["name"])) != "Zzyzx Keep Case" || !strings.Contains(str(unprobed[0]["detail"]), "never probed") {
		t.Errorf("unprobed = %v, want the kept Blu-ray alone", unprobed)
	}
	// nothing was written over since the scan read it
	if n := num(t, out["total_replaced"], "total_replaced"); n != 0 {
		t.Errorf("replaced = %v", out["replaced"])
	}

	// a lower bar leaves only the codecs
	out = call(t, "audit_quality", map[string]any{"library": "Messy Movies", "min_height": 360})
	if got := findings(t, out); !slices.Equal(got, []string{"Princess Mononoke", "Zzyzx Night Ferry"}) {
		t.Errorf("at 360 lines = %v, want [Princess Mononoke Zzyzx Night Ferry]", got)
	}
	out = call(t, "audit_quality", map[string]any{"library": "Messy Movies", "min_height": 360, "legacy_codecs": false})
	if n := num(t, out["total_findings"], "total_findings"); n != 0 {
		t.Errorf("at 360 lines with codecs off = %v", out["findings"])
	}
	// a bitrate floor nothing a one-second test pattern reaches
	out = call(t, "audit_quality", map[string]any{"library": "Movies", "min_bitrate": 1000000000})
	if n := num(t, out["total_findings"], "total_findings"); n != 8 {
		t.Errorf("a bitrate floor flagged %d films, want all 8", n)
	}

	// episodes too, named by series and number, the lowest first: Gaiden's
	// 160x90 before the 360p rest
	out = call(t, "audit_quality", map[string]any{"library": "Messy Shows"})
	if n := num(t, out["total_findings"], "total_findings"); n != messyEpisodes {
		t.Errorf("messy episodes = %d, want %d", n, messyEpisodes)
	}
	episode := regexp.MustCompile(`^.+ S\d\dE\d\d `)
	for i, f := range rows(t, out["findings"], "findings") {
		if name := str(f["name"]); !episode.MatchString(name) {
			t.Errorf("episode finding name = %q", name)
		}
		if i < 3 && !strings.HasPrefix(str(f["name"]), "Zzyzx Gaiden") {
			t.Errorf("finding %d is %v, want Gaiden's three 90p files first", i, f["name"])
		}
	}
	// and a limit caps each list, not the count
	out = call(t, "audit_quality", map[string]any{"library": "Messy Shows", "types": "Episode", "limit": 2})
	if len(rows(t, out["findings"], "findings")) != 2 || num(t, out["total_findings"], "total_findings") != messyEpisodes {
		t.Errorf("limit 2 = %v of %v", len(rows(t, out["findings"], "findings")), out["total_findings"])
	}
}

func TestAuditMissingEpisodes(t *testing.T) {
	out := call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows"})
	if got := findings(t, out); !slices.Equal(got, []string{"Star Trek The Next Generation", "Zzyzx Paths"}) {
		t.Fatalf("series with gaps = %v, want Star Trek The Next Generation and Zzyzx Paths", got)
	}
	// Zzyzx Paths holds seasons 1 and 3, and in season 1 has E04's file held
	// as E05; Jellyfin also takes its E02E03 file for E02 alone (its nfo ends
	// the run there), where Emby holds both
	paths := "missing between the episodes on disk: S01E04, season 2"
	if isJellyfin() {
		paths = "missing between the episodes on disk: S01E03, S01E04, season 2"
	}
	for _, f := range rows(t, out["findings"], "findings") {
		want := "missing between the episodes on disk: S01E02"
		if title(str(f["name"])) == "Zzyzx Paths" {
			want = paths
		}
		if str(f["detail"]) != want || str(f["id"]) == "" {
			t.Errorf("finding = %v, want %q", f, want)
		}
	}
	if n := num(t, out["items_scanned"], "items_scanned"); n != messyEpisodes {
		t.Errorf("scanned %d episodes, want %d", n, messyEpisodes)
	}
	if capped := call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows", "limit": 1}); len(rows(t, capped["findings"], "findings")) != 1 || num(t, capped["total_findings"], "total_findings") != 2 {
		t.Errorf("limit 1 = %v", capped)
	}
	// A2: the sweep says how much of the answer it could know. Neither server
	// records a series' full run out of the box, so a series it does not list
	// is not a series proved complete, and the answer has to say so.
	known, ok := out["runs_known"].(bool)
	if !ok {
		t.Fatalf("runs_known is %T, want a bool", out["runs_known"])
	}
	if known == (str(out["note"]) != "") {
		t.Errorf("runs_known = %v with note %q: the note belongs with the weaker answer", known, out["note"])
	}
	if !known && !strings.Contains(str(out["note"]), "not known to be complete") {
		t.Errorf("the note does not warn that absence is not completeness: %q", out["note"])
	}

	// the clean shows hold their episodes from the first without gaps
	out = call(t, "audit_missing_episodes", map[string]any{"library": "Shows"})
	for _, f := range rows(t, out["findings"], "findings") {
		if strings.Contains(str(f["detail"]), "between the episodes on disk") {
			t.Errorf("a clean show has a gap: %v", f)
		}
	}

	// with the provider, TMDB's run says what the messy Severance lacks
	// after its last file (season one has nine episodes, and we hold three),
	// and the unidentified Star Trek is a series nobody can be asked about
	needsTMDBRecording(t, "GET api.themoviedb.org/3/tv/95396")
	out = call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows", "provider": true})
	if !boolOf(out["runs_known"]) || str(out["note"]) != "" {
		t.Errorf("with the provider runs_known = %v, note %q", out["runs_known"], out["note"])
	}
	found := map[string]string{}
	for _, f := range rows(t, out["findings"], "findings") {
		found[title(str(f["name"]))] = str(f["detail"])
	}
	if d := found["Severance"]; !strings.Contains(d, "listed by TMDB without a file: S01E04, S01E05") {
		t.Errorf("Severance = %q, want TMDB's run past the three files", d)
	}
	if d := found["Star Trek The Next Generation"]; !strings.Contains(d, "between the episodes on disk: S01E02") || strings.Contains(d, "TMDB") {
		t.Errorf("Star Trek = %q, want its gap alone", d)
	}
	// every messy series but Severance carries no id a provider knows it by
	// (Gaiden's AniDB id is not one TMDB is asked by)
	var unknown []string
	for _, u := range rows(t, out["unknown"], "unknown") {
		unknown = append(unknown, title(str(u["name"])))
		if !strings.Contains(str(u["reason"]), "carries no tmdb, tvdb or imdb id") {
			t.Errorf("unknown = %v", u)
		}
	}
	slices.Sort(unknown)
	wantUnknown := sorted(append([]string{"Zzyzx Gaiden"}, unmatchedShows...))
	if !slices.Equal(unknown, wantUnknown) || num(t, out["total_unknown"], "total_unknown") != len(wantUnknown) {
		t.Errorf("unknown = %v, want %v", unknown, wantUnknown)
	}

	// paged a series at a time, the walk to the end asks after every series
	// once and finds what the one call found
	var paged, pagedUnknown []string
	offset, pages := 0, 0
	for {
		page := call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows", "provider": true, "max_lookups": 2, "offset": offset})
		pages++
		paged = append(paged, findings(t, page)...)
		for _, u := range rows(t, page["unknown"], "unknown") {
			pagedUnknown = append(pagedUnknown, title(str(u["name"])))
		}
		next, more := page["next_offset"]
		if !more {
			break
		}
		if n := num(t, next, "next_offset"); n != offset+2 {
			t.Fatalf("page at %d says go on from %d", offset, n)
		}
		offset += 2
		if pages > messySeries {
			t.Fatal("the pages never end")
		}
	}
	slices.Sort(paged)
	slices.Sort(pagedUnknown)
	if pages != (messySeries+1)/2 || !slices.Equal(paged, findings(t, out)) || !slices.Equal(pagedUnknown, wantUnknown) {
		t.Errorf("%d pages found %v and could not ask after %v, want %d pages, %v and %v", pages, paged, pagedUnknown, (messySeries+1)/2, findings(t, out), wantUnknown)
	}
}

func TestAuditUnwatched(t *testing.T) {
	out := call(t, "audit_unwatched", map[string]any{"library": "Movies"})
	if n := num(t, out["total_findings"], "total_findings"); n != 8 {
		t.Errorf("unwatched films = %d, want all 8", n)
	}
	if users := strs(t, out["users"], "users"); !slices.Contains(users, "root") || !slices.Contains(users, "alice") {
		t.Errorf("users = %v", users)
	}
	if capped := call(t, "audit_unwatched", map[string]any{"library": "Movies", "limit": 2}); len(rows(t, capped["findings"], "findings")) != 2 || num(t, capped["total_findings"], "total_findings") != 8 {
		t.Errorf("limit 2 = %v", capped)
	}

	dune := findItem(t, "Movies", "Movie", "Dune")
	call(t, "item_set_state", map[string]any{"id": dune, "user": "alice", "watched": true})
	t.Cleanup(func() {
		_, _ = invoke("item_set_state", map[string]any{"id": dune, "user": "alice", "watched": false})
	})
	out = call(t, "audit_unwatched", map[string]any{"library": "Movies"})
	if got := findings(t, out); len(got) != 7 || slices.Contains(got, "Dune") {
		t.Errorf("unwatched after alice watched Dune = %v", got)
	}
	for _, f := range rows(t, out["findings"], "findings") {
		if !strings.HasPrefix(str(f["detail"]), "never watched, added ") {
			t.Errorf("detail = %v", f["detail"])
		}
	}

	// a series is watched once anyone has watched an episode of it
	series := findItem(t, "Shows", "Series", "Severance")
	eps := call(t, "show_episodes", map[string]any{"series_id": series})
	first := str(rows(t, eps["episodes"], "episodes")[0]["id"])
	call(t, "item_set_state", map[string]any{"id": first, "watched": true})
	t.Cleanup(func() { _, _ = invoke("item_set_state", map[string]any{"id": first, "watched": false}) })
	out = call(t, "audit_unwatched", map[string]any{"library": "Shows", "types": "Series"})
	if got := findings(t, out); !slices.Equal(got, []string{"Breaking Bad", "The Expanse"}) {
		t.Errorf("unwatched series = %v, want Breaking Bad and The Expanse", got)
	}

	// everything was added today, so nothing is older than a day
	out = call(t, "audit_unwatched", map[string]any{"library": "Movies", "added_days": 1})
	if n := num(t, out["total_findings"], "total_findings"); n != 0 {
		t.Errorf("added over a day ago = %d", n)
	}
	if msg := callErr(t, "audit_unwatched", map[string]any{"types": "Episode"}); !strings.Contains(msg, "Movie, Series or both") {
		t.Errorf("types=Episode: %s", msg)
	}
}

func TestPersonGet(t *testing.T) {
	out := call(t, "person_get", map[string]any{"person": "denis villeneuve"})
	if str(out["name"]) != "Denis Villeneuve" || str(out["id"]) == "" {
		t.Fatalf("person_get = %v", out)
	}
	titles := map[string]bool{}
	var arrivalDirector bool
	for _, c := range rows(t, out["credits"], "credits") {
		titles[title(str(c["name"]))] = true
		if str(c["credit"]) == "" {
			t.Errorf("a credit without its kind: %v", c)
		}
		if title(str(c["name"])) == "Arrival" && str(c["credit"]) == "Director" {
			arrivalDirector = true
		}
	}
	if !titles["Arrival"] || !titles["Dune"] || !titles["Dune: Part Two"] || len(titles) != 3 {
		t.Errorf("Villeneuve's credits = %v", titles)
	}
	if !arrivalDirector {
		t.Errorf("Arrival does not credit him as director: %v", out["credits"])
	}
	// oldest first
	if first := rows(t, out["credits"], "credits")[0]; title(str(first["name"])) != "Arrival" {
		t.Errorf("first credit = %v", first["name"])
	}

	// by id, and no series
	byID := call(t, "person_get", map[string]any{"person": str(out["id"]), "types": "Series"})
	if str(byID["name"]) != "Denis Villeneuve" || len(rows(t, byID["credits"], "credits")) != 0 {
		t.Errorf("by id with types=Series = %v", byID)
	}

	// a partial name answers with the people it could mean, and nothing else
	near := call(t, "person_get", map[string]any{"person": "Scott"})
	var ridley bool
	for _, p := range rows(t, near["candidates"], "candidates") {
		ridley = ridley || (str(p["name"]) == "Ridley Scott" && str(p["id"]) != "")
	}
	if !ridley || near["name"] != nil || len(rows(t, near["credits"], "credits")) != 0 {
		t.Errorf("a partial name should offer the full one: %v", near)
	}
	if msg := callErr(t, "person_get", map[string]any{"person": "Nobody Atall"}); !strings.Contains(msg, "Nobody Atall") {
		t.Errorf("an unknown person: %s", msg)
	}
}

func TestCurationFamiliesAreComplete(t *testing.T) {
	var got []string
	for _, name := range toolNames(t) {
		if strings.HasPrefix(name, "person_") || strings.HasPrefix(name, "metadata_") {
			got = append(got, name)
		}
	}
	if want := []string{"metadata_rename", "person_get"}; !slices.Equal(slices.Sorted(slices.Values(got)), want) {
		t.Errorf("person and metadata tools = %v, want %v", got, want)
	}
}
