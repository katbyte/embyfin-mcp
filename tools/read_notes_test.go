package tools

import (
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// growing is a canned server's items, more than one request reads, which
// gain one more at the front before every request for items after the first
// when grow is set: files imported mid-read with an old date sort among the
// first. With only set, they grow before those requests alone, so one of a
// tool's reads sees the change and the rest do not. With swap set instead,
// the first item goes as one is added at the front, once, before the second
// request: neither the count nor the last items read move.
type growing struct {
	mu   sync.Mutex
	n    int
	grow bool
	swap bool
	// drop takes the first item away before the first read in an
	// account's view, once, and arrive puts one in at the front then
	drop, arrive bool
	// replace puts a whole other library in place before the second
	// request, and failAgain answers the read again at a read's end with a
	// 500; replaceInView puts it in place before the second request in an
	// account's view alone, and hideInView lists nothing there
	replace, failAgain, replaceInView, hideInView bool
	viewAsked                                     int
	arrived, replaced                             bool
	only                                          func(r *http.Request) bool
	asked                                         int
	grown                                         bool
	added                                         int
	swapped                                       bool
}

// row is item id at position i, of the kind asked for.
func growingRow(kind, id string, i int) map[string]any {
	name := "Zzyzx " + id
	switch kind {
	case typeEpisode:
		return map[string]any{
			"Id": id, "Name": name, "Type": typeEpisode, "SeriesId": "s1", "SeriesName": "Zzyzx Show", "ParentIndexNumber": 1, "IndexNumber": i + 1,
			"Path": fmt.Sprintf("/zz/films/Zzyzx Show/Season 01/Zzyzx Show S01E%02d.mkv", i+1), "LocationType": "FileSystem",
		}
	case "Series":
		return map[string]any{"Id": id, "Name": "Zzyzx Show " + id, "Type": "Series", "Path": "/zz/films/Zzyzx Show " + id, "IsFolder": true}
	}

	return map[string]any{"Id": id, "Name": name, "Type": kind, "ProductionYear": 2001, "Path": "/zz/films/" + name + ".mkv", "LocationType": "FileSystem"}
}

func (g *growing) items(t *testing.T) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		kind := typeMovie
		if types := values(r, "IncludeItemTypes"); len(types) > 0 {
			kind = types[0]
		}
		// the server holds no collection or playlist, which a delete reads
		// for what it would leave
		if kind == "BoxSet" || kind == "Playlist" {
			writeJSON(t, w, page())

			return
		}
		if ids := values(r, "Ids"); len(ids) > 0 {
			g.mu.Lock()
			swapped := g.swapped
			g.mu.Unlock()
			rows := make([]map[string]any, 0, len(ids))
			for i, id := range ids {
				// the first item is gone once swapped
				if swapped && id == "a0" {
					continue
				}
				rows = append(rows, growingRow(kind, id, i))
			}
			writeJSON(t, w, page(rows...))

			return
		}
		// the read again at a read's end, which asks for one field alone:
		// nothing changes under it, which comes after the pages
		fields := values(r, "Fields")
		check := len(fields) == 1 && fields[0] == "DateCreated"
		if check && g.failAgain {
			http.Error(w, "boom", http.StatusInternalServerError)

			return
		}
		g.mu.Lock()
		g.asked++
		if g.drop && strings.HasPrefix(r.URL.Path, "/Users/") {
			g.swapped = true
		}
		if g.arrive && !g.arrived && strings.HasPrefix(r.URL.Path, "/Users/") {
			g.arrived = true
			g.added++
		}
		if g.replace && g.asked > 1 {
			g.replaced = true
		}
		inView := strings.HasPrefix(r.URL.Path, "/Users/")
		if inView {
			g.viewAsked++
			if g.replaceInView && g.viewAsked > 1 {
				g.replaced = true
			}
		}
		if inView && g.hideInView {
			g.mu.Unlock()
			writeJSON(t, w, page())

			return
		}
		replaced := g.replaced
		if (g.grow || g.swap && !g.swapped) && !check && (g.only == nil || g.only(r)) {
			if g.grown {
				g.added++
				g.swapped = g.swap
			}
			g.grown = true
		}
		added, gone := g.added, 0
		if g.swapped {
			gone = 1
		}
		g.mu.Unlock()
		total := g.n + added - gone
		start, limit := 0, total
		if v := values(r, "StartIndex"); len(v) > 0 {
			n, err := strconv.Atoi(v[0])
			if err != nil {
				t.Errorf("StartIndex %q: %v", v[0], err)
			}
			start = n
		}
		if v := values(r, "Limit"); len(v) > 0 {
			n, err := strconv.Atoi(v[0])
			if err != nil {
				t.Errorf("Limit %q: %v", v[0], err)
			}
			limit = n
		}
		var rows []map[string]any
		for i := start; i < min(start+limit, total); i++ {
			// the ones added first, newest at the very front, then the
			// first ones less any gone
			id := fmt.Sprintf("a%d", i-added+gone)
			if replaced {
				id = fmt.Sprintf("b%d", i-added+gone)
			}
			if i < added {
				id = fmt.Sprintf("n%d", added-1-i)
			}
			rows = append(rows, growingRow(kind, id, i))
		}
		writeJSON(t, w, map[string]any{"Items": rows, "TotalRecordCount": total})
	}
}

// growingServer is a canned Emby or Jellyfin with one library, two accounts,
// nothing on disk outside the library, and n items that grow as g says.
func growingServer(t *testing.T, jellyfin bool, g *growing) *fakeServer {
	t.Helper()

	f := newFakeServer(t)
	f.jellyfin = jellyfin
	libraries := []map[string]any{{
		"Name": "Zzyzx Films", "CollectionType": "movies", "ItemId": "lib9", "Locations": []string{"/zz/films"},
		"LibraryOptions": map[string]any{"MetadataSavers": []string{}},
	}}
	users := []map[string]any{
		{"Id": "u1", "Name": "Quux", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}},
		{"Id": "u2", "Name": "Plugh", "Policy": map[string]any{"IsAdministrator": false, "EnableAllFolders": true}},
	}
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": libraries, "TotalRecordCount": len(libraries)})
	})
	f.mux.HandleFunc("GET /Library/VirtualFolders", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, libraries) })
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": users, "TotalRecordCount": len(users)})
	})
	f.mux.HandleFunc("GET /Users", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, users) })
	f.mux.HandleFunc("GET /Items", g.items(t))
	f.mux.HandleFunc("GET /Users/{user}/Items", g.items(t))
	f.mux.HandleFunc("POST /Environment/ValidatePath", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	return f
}

// On Emby what people are shown is read twice, the items and then an
// administrator's view of them: an item removed between the two is left out
// and said to be, where its single read in the view failed the whole audit.
func TestAnItemRemovedBetweenEmbysTwoReads(t *testing.T) {
	t.Parallel()

	g := &growing{n: 1500, drop: true}
	cs := session(t, growingServer(t, false, g), Options{})
	out := mustCall(t, cs, "audit_multiple_versions", map[string]any{})
	if note := text(out["note"]); note != "the library changed while it was read: 1 item(s) it read were removed before it ended, and are left out" {
		t.Errorf("note = %q", note)
	}
	if got := number(t, out["items_scanned"], "items_scanned"); got != 1499 {
		t.Errorf("items_scanned = %d, want the 1499 still there", got)
	}
}

// And an item put in between Emby's two reads, which the administrator's
// view lists and the read of the items does not, is a change while it was
// read: said, where it went unmentioned.
func TestAnItemArrivingBetweenEmbysTwoReads(t *testing.T) {
	t.Parallel()

	g := &growing{n: 1500, arrive: true}
	cs := session(t, growingServer(t, false, g), Options{})
	out := mustCall(t, cs, "audit_multiple_versions", map[string]any{})
	if note := text(out["note"]); note != "the library changed while it was read: 1 item(s) came in between its two reads and are left out" {
		t.Errorf("note = %q", note)
	}
	if got := number(t, out["items_scanned"], "items_scanned"); got != 1500 {
		t.Errorf("items_scanned = %d, want the 1500 there when it began", got)
	}
}

// Emby's read of what people are shown places each item its administrator's
// view leaves out with a read of that item alone. A view read that stopped
// short left out everything past where it stopped, and set off a read for
// each - hundreds of thousands on a large library - so a stopped read fails
// the audit, and so does a view leaving out more items than it reads one at a
// time, before any is read.
func TestEmbysShownReadNeverReadsItemByItemWithoutBound(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		g    *growing
		want string
	}{
		{"a view read stopped short", &growing{n: 1500, replaceInView: true}, "changed too much"},
		{"a view leaving out 2500", &growing{n: 2500, hideInView: true}, "one at a time"},
	} {
		f := growingServer(t, false, tc.g)
		cs := session(t, f, Options{})
		msg := mustRefuse(t, cs, "audit_multiple_versions", map[string]any{})
		if !strings.Contains(msg, tc.want) {
			t.Errorf("%s: %s; want it refused saying %q", tc.name, msg, tc.want)
		}
		f.mu.Lock()
		single := 0
		for _, r := range f.seen {
			if strings.HasPrefix(r.Path, "/Users/u1/Items/") {
				single++
			}
		}
		f.mu.Unlock()
		if single != 0 {
			t.Errorf("%s: %d items read one at a time", tc.name, single)
		}
	}
}

// foldingServer is a canned Emby holding films in pairs, a file and a second
// version of it sharing Emby's presentation key, whose administrator's view
// lists the first of each pair alone, as Emby shows versions; beside them a
// cut in a folder of its own and a pair with no id, each sharing its film's
// key; a version with no key; a version whose key two listed films share; a
// film whose key no listed item has; and a season holding each episode in
// two files sharing a key. It counts the items read one at a time. From the
// pair numbered disagreeFrom on (-1 for none), its single reads name no
// version; gone is an item removed before it is read on its own.
type foldingServer struct {
	*fakeServer
	mu           sync.Mutex
	single       int
	disagreeFrom int
	gone         string
	// askedKey says a sweep asked for the presentation key, which Emby
	// answers only when asked
	askedKey bool
}

func newFoldingServer(t *testing.T, pairs, disagreeFrom int, gone string) *foldingServer {
	t.Helper()

	f := &foldingServer{fakeServer: newFakeServer(t), disagreeFrom: disagreeFrom, gone: gone}
	source := func(id, path string) []map[string]any {
		return []map[string]any{{"Id": "ms" + id, "ItemId": id, "Path": path, "Protocol": "File"}}
	}
	film := func(id, path, key string) map[string]any {
		return map[string]any{"Id": id, "Name": "Zzyzx " + id, "Type": typeMovie, "Path": path, "LocationType": "FileSystem", "PresentationUniqueKey": key, "MediaSources": source(id, path)}
	}
	episode := func(id, path string, n int) map[string]any {
		return map[string]any{
			"Id": id, "Name": "Zzyzx " + id, "Type": typeEpisode, "Path": path, "LocationType": "FileSystem", "SeriesId": "s1", "SeriesName": "Zzyzx Show", "ParentIndexNumber": 1, "IndexNumber": n,
			"PresentationUniqueKey": fmt.Sprintf("79126-lib-001 - %04d", n), "MediaSources": source(id, path),
		}
	}
	var stored, listed []map[string]any
	for i := range pairs {
		dir, key := fmt.Sprintf("/zz/films/Zzyzx %d", i), fmt.Sprintf("p-tmdb-Movie-%d-lib", 1000+i)
		first, second := film(fmt.Sprintf("f%d", i), dir+fmt.Sprintf("/Zzyzx %d.mkv", i), key), film(fmt.Sprintf("v%d", i), dir+fmt.Sprintf("/Zzyzx %d - 720p.mkv", i), key)
		stored = append(stored, first, second)
		listed = append(listed, first)
	}
	// a version the server gives no key, which only its own read places
	flatA, flatB, flatC := film("fa", "/zz/films/flat/Zzyzx A.mkv", "ka"), film("fb", "/zz/films/flat/Zzyzx B.mkv", "kb"), film("fc", "/zz/films/flat/Zzyzx A - 720p.mkv", "")
	// a cut in a folder of its own and a pair with no id, each sharing its
	// film's key; a version whose key two listed films share, which only its
	// own read can place; and a film whose key nothing listed has
	cutA, cutB := film("ca", "/zz/films/Zzyzx C/Zzyzx C.mkv", "p-tmdb-Movie-5000-lib"), film("cb", "/zz/films/Zzyzx C Cut/Zzyzx C Cut.mkv", "p-tmdb-Movie-5000-lib")
	noidA, noidB := film("na", "/zz/films/Zzyzx N/Zzyzx N.mkv", "0f0f0f"), film("nb", "/zz/films/Zzyzx N/Zzyzx N - 720p.mkv", "0f0f0f")
	twinA, twinB, twinC := film("ta", "/zz/films/Zzyzx T/Zzyzx T.mkv", "p-tmdb-Movie-7000-lib"), film("tb", "/zz/films/Zzyzx T Two/Zzyzx T Two.mkv", "p-tmdb-Movie-7000-lib"), film("tc", "/zz/films/Zzyzx T/Zzyzx T - 720p.mkv", "p-tmdb-Movie-7000-lib")
	stray := film("xf", "/zz/films/Zzyzx X/Zzyzx X.mkv", "p-tmdb-Movie-8000-lib")
	stored = append(stored, flatA, flatB, flatC, cutA, cutB, noidA, noidB, twinA, twinB, twinC, stray)
	listed = append(listed, flatA, flatB, cutA, noidA, twinA, twinB)
	for n := 1; n <= 3; n++ {
		season := "/zz/shows/Zzyzx Show/Season 01/"
		e, v := episode(fmt.Sprintf("e%d", n), season+fmt.Sprintf("Zzyzx Show S01E%02d.mkv", n), n), episode(fmt.Sprintf("ev%d", n), season+fmt.Sprintf("Zzyzx Show S01E%02d - 720p.mkv", n), n)
		stored = append(stored, e, v)
		listed = append(listed, e)
	}
	byID, keyed := map[string]map[string]any{}, map[string][]map[string]any{}
	for _, it := range stored {
		byID[fmt.Sprint(it["Id"])] = it
		if key := fmt.Sprint(it["PresentationUniqueKey"]); key != "" {
			keyed[key] = append(keyed[key], it)
		}
	}
	pageOf := func(t *testing.T, w http.ResponseWriter, r *http.Request, rows []map[string]any) {
		t.Helper()

		start, limit := 0, len(rows)
		if v := values(r, "StartIndex"); len(v) > 0 {
			n, err := strconv.Atoi(v[0])
			if err != nil {
				t.Errorf("StartIndex %q: %v", v[0], err)
			}
			start = n
		}
		if v := values(r, "Limit"); len(v) > 0 {
			n, err := strconv.Atoi(v[0])
			if err != nil {
				t.Errorf("Limit %q: %v", v[0], err)
			}
			limit = n
		}
		var kept []map[string]any
		types := values(r, "IncludeItemTypes")
		for _, it := range rows {
			if len(types) == 0 || slices.Contains(types, fmt.Sprint(it["Type"])) {
				kept = append(kept, it)
			}
		}
		writeJSON(t, w, map[string]any{"Items": kept[min(start, len(kept)):min(start+limit, len(kept))], "TotalRecordCount": len(kept)})
	}
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(map[string]any{"Id": "u1", "Name": "Quux", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}}))
	})
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(map[string]any{"Name": "Zzyzx", "CollectionType": "mixed", "ItemId": "lib1", "Locations": []string{"/zz"}}))
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		// an item asked for by id is there unless it has gone
		if ids := values(r, "Ids"); len(ids) > 0 {
			var rows []map[string]any
			for _, id := range ids {
				if it, ok := byID[id]; ok && id != f.gone {
					rows = append(rows, it)
				}
			}
			writeJSON(t, w, page(rows...))

			return
		}
		if slices.Contains(values(r, "Fields"), "PresentationUniqueKey") {
			f.mu.Lock()
			f.askedKey = true
			f.mu.Unlock()
		}
		pageOf(t, w, r, stored)
	})
	f.mux.HandleFunc("GET /Users/u1/Items", func(w http.ResponseWriter, r *http.Request) { pageOf(t, w, r, listed) })
	// the single read, which names every version: every item of its key,
	// and the flat folder's version is the first film's
	f.mux.HandleFunc("GET /Users/u1/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.single++
		f.mu.Unlock()
		if r.PathValue("id") == f.gone {
			http.NotFound(w, r)

			return
		}
		it := maps.Clone(byID[r.PathValue("id")])
		pair := -1
		if n, err := strconv.Atoi(strings.TrimPrefix(fmt.Sprint(it["Id"]), "v")); err == nil {
			pair = n
		}
		switch {
		case f.disagreeFrom == 0, f.disagreeFrom > 0 && pair >= f.disagreeFrom:
		case it["Id"] == "fc":
			it["MediaSources"] = append(source("fa", "/zz/films/flat/Zzyzx A.mkv"), source("fc", "/zz/films/flat/Zzyzx A - 720p.mkv")...)
		case it["Id"] == "tc":
			it["MediaSources"] = append(source("ta", "/zz/films/Zzyzx T/Zzyzx T.mkv"), source("tc", "/zz/films/Zzyzx T/Zzyzx T - 720p.mkv")...)
		case it["Id"] != "xf" && it["Id"] != "tb" && it["Id"] != "ta":
			var all []map[string]any
			for _, other := range keyed[fmt.Sprint(it["PresentationUniqueKey"])] {
				all = append(all, source(fmt.Sprint(other["Id"]), fmt.Sprint(other["Path"]))...)
			}
			it["MediaSources"] = all
		}
		writeJSON(t, w, it)
	})

	return f
}

// On Emby what people are shown is read as the items and an administrator's
// view of them, and each item the view leaves out is a version of one it
// lists. The view names no version (4.10: not even asked for its media
// sources), only an item's own read does, so each was read on its own - 901
// films of 20,244 held in two files were 901 reads. Emby merges the items
// that share its presentation key, so an item left out is placed with the one
// listed item sharing its key: matching every provider id placed a film
// sharing only a site, or an IMDb id beside another TMDB id, into another
// film. A sample of twenty placements, spread across them, is checked
// against each item's own read; an item no key places, or that two listed
// items share, is read on its own, and only those count toward the bound.
// The note says how each was placed.
func TestEmbysVersionsArePlacedByItsKey(t *testing.T) {
	t.Parallel()

	f := newFoldingServer(t, 2500, -1, "")
	cs := session(t, f.fakeServer, Options{})
	out := mustCall(t, cs, "audit_multiple_versions", map[string]any{"limit": 3000})
	// 2,500 pairs, the flat folder's first film, three episodes, the cut,
	// the pair with no id, and the twin its own read places
	if got := number(t, out["total_findings"], "total_findings"); got != 2507 {
		t.Errorf("total_findings = %d, want 2507", got)
	}
	versions := map[string]string{}
	for _, row := range objects(t, out["findings"], "findings") {
		versions[text(row["id"])] = text(row["detail"])
	}
	for id, want := range map[string]string{
		"f7": "2 versions: Zzyzx 7.mkv, Zzyzx 7 - 720p.mkv",
		"fa": "2 versions: Zzyzx A.mkv, Zzyzx A - 720p.mkv",
		"e2": "2 versions: Zzyzx Show S01E02.mkv, Zzyzx Show S01E02 - 720p.mkv",
		"ca": "2 versions: Zzyzx C.mkv, Zzyzx C Cut.mkv",
		"na": "2 versions: Zzyzx N.mkv, Zzyzx N - 720p.mkv",
		"ta": "2 versions: Zzyzx T.mkv, Zzyzx T - 720p.mkv",
	} {
		if versions[id] != want {
			t.Errorf("%s = %q, want %q", id, versions[id], want)
		}
	}
	for _, id := range []string{"fb", "tb", "xf"} {
		if _, found := versions[id]; found {
			t.Errorf("%s was given a version: %q", id, versions[id])
		}
	}
	f.mu.Lock()
	single := f.single
	f.mu.Unlock()
	if single != 23 {
		t.Errorf("%d items read one at a time, want the 20 checked and the 3 no key places", single)
	}
	if want := "on Emby, of the 2508 items shown only as versions of others, 2505 were placed by the key Emby merges versions by (20 of them checked against a read of each, all agreeing), and 3 were read one at a time"; text(out["note"]) != want {
		t.Errorf("note = %q, want %q", text(out["note"]), want)
	}
	f.mu.Lock()
	asked := f.askedKey
	f.mu.Unlock()
	if !asked {
		t.Error("the items were read without asking for the key Emby merges versions by, which it answers only when asked")
	}

	// audit_all counts it, and how the versions were placed is no sign of
	// the library changing
	f = newFoldingServer(t, 30, -1, "")
	cs = session(t, f.fakeServer, Options{})
	all := mustCall(t, cs, "audit_all", map[string]any{})
	if note := text(all["note"]); note != "" {
		t.Errorf("audit_all's note = %q of a library that held still", note)
	}
	for _, row := range objects(t, all["audits"], "audits") {
		if note := text(row["note"]); strings.Contains(note, "changed") || strings.Contains(note, "placed by the key") {
			t.Errorf("%s's row note = %q", text(row["audit"]), note)
		}
	}
}

// A sample that disagrees with the key - Emby merging by another rule than it
// did - drops every placement by key: each item left out is then read on its
// own, as far as the bound allows, and beyond it the audit is refused.
func TestEmbysVersionsWhenTheKeyDisagrees(t *testing.T) {
	t.Parallel()

	f := newFoldingServer(t, 30, 0, "")
	cs := session(t, f.fakeServer, Options{})
	out := mustCall(t, cs, "audit_multiple_versions", map[string]any{})
	f.mu.Lock()
	single := f.single
	f.mu.Unlock()
	// the single reads name no version, so nothing is shown in two files
	if got := number(t, out["total_findings"], "total_findings"); got != 0 || single != 38 {
		t.Errorf("total_findings = %d after %d single reads, want 0 after the 38 items left out, each once", got, single)
	}
	if want := "on Emby, the key Emby merges versions by disagreed with a read of"; !strings.HasPrefix(text(out["note"]), want) || !strings.Contains(text(out["note"]), "all 38 items shown only as versions of others were read one at a time") {
		t.Errorf("note = %q", text(out["note"]))
	}

	// and the sample reaches past the first placings: a key disagreeing from
	// the thousandth pair on is caught
	for _, from := range []int{0, 1000} {
		f = newFoldingServer(t, 2500, from, "")
		cs = session(t, f.fakeServer, Options{})
		if msg := mustRefuse(t, cs, "audit_multiple_versions", map[string]any{}); !strings.Contains(msg, "disagreed") || !strings.Contains(msg, "one at a time") {
			t.Errorf("a key that disagrees from pair %d on a view leaving out 2508 = %s", from, msg)
		}
	}
}

// A placing checked against a read of an item that has since gone is no
// disagreement: the item is left out, and said to be, and the rest stand.
func TestEmbysVersionsWhenASampledItemHasGone(t *testing.T) {
	t.Parallel()

	// the first placing is the first sampled
	f := newFoldingServer(t, 30, -1, "v0")
	cs := session(t, f.fakeServer, Options{})
	out := mustCall(t, cs, "audit_multiple_versions", map[string]any{})
	note := text(out["note"])
	if !strings.Contains(note, "1 item(s) it read were removed before it ended") || !strings.Contains(note, "34 were placed by the key Emby merges versions by") || strings.Contains(note, "disagreed") {
		t.Errorf("note = %q", note)
	}
	// 29 pairs, the flat folder's first film, three episodes, the cut, the
	// pair with no id, and the twin
	if got := number(t, out["total_findings"], "total_findings"); got != 36 {
		t.Errorf("total_findings = %d, want 36", got)
	}
}

// A read that cannot be sure answers only what it can stand behind. When
// the check at its end fails, the read was whole, and a read tool answers it
// with a note; a tool whose read decides a change refuses. When the library
// changed too much to follow, the read stopped short, and an answer saying
// what is not there, or how many there are, cannot be given from it: those
// tools refuse (found: false from half a read is a duplicate download, and
// "never watched" or "missing" are the same mistake), library_export takes
// its part-written file away, library_items answers the best matches it read
// with no total, and audit_all leaves that audit's row uncounted. An
// hours-long audit_all used to fail at its end, and library_export deleted a
// file it had written whole.
func TestReadsThatCannotBeSure(t *testing.T) {
	t.Parallel()

	const (
		answer = "answer" // a note saying so
		refuse = "refuse"
		noSum  = "no total" // the items read, no total, a note saying so
		rows   = "rows"     // audit_all: said on the audit's row
	)
	for _, jellyfin := range []bool{false, true} {
		for _, how := range []string{"the check failing", "the library replaced"} {
			for _, tc := range []struct {
				tool             string
				args             map[string]any
				n                int
				jellyfinOnly     bool
				onCheck, whenCut string
			}{
				{tool: "audit_missing_metadata", onCheck: answer, whenCut: refuse},
				{tool: "audit_unwatched", onCheck: answer, whenCut: refuse},
				{tool: "audit_missing_episodes", onCheck: answer, whenCut: refuse},
				{tool: "audit_orphans", n: 10500, onCheck: answer, whenCut: refuse},
				{tool: "library_export", onCheck: answer, whenCut: refuse},
				{tool: "item_find_by_metadata_id", args: map[string]any{"metadata_provider": "tmdb", "id": "1", "type": "movie"}, jellyfinOnly: true, onCheck: answer, whenCut: refuse},
				{tool: "library_items", args: map[string]any{"query": "zzy", "limit": 10}, onCheck: answer, whenCut: noSum},
				{tool: "library_items", args: map[string]any{"query": "zzy", "sort": "name"}, onCheck: answer, whenCut: refuse},
				{tool: "audit_all", onCheck: rows, whenCut: rows},
				{tool: "metadata_rename", args: map[string]any{"field": "genres", "from": "Zzyzxcore", "to": "Zzyzx-core"}, onCheck: refuse, whenCut: refuse},
				{tool: "item_orphans_delete", args: map[string]any{"folder": "/zz/gone"}, n: 10500, onCheck: refuse, whenCut: refuse},
			} {
				if tc.jellyfinOnly && !jellyfin {
					continue
				}
				name := fmt.Sprintf("%s %v (jellyfin %v, %s)", tc.tool, tc.args, jellyfin, how)
				g := &growing{n: 1500, failAgain: how == "the check failing", replace: how == "the library replaced"}
				if tc.n > 0 {
					g.n = tc.n
				}
				cs := session(t, growingServer(t, jellyfin, g), Options{EnableDelete: true})
				args := map[string]any{}
				maps.Copy(args, tc.args)
				path := filepath.Join(t.TempDir(), "export.jsonl")
				if tc.tool == "library_export" {
					args["path"] = path
				}
				out, refusal := callTool(t, cs, tc.tool, args)
				want, expect := "whether the library changed during the read could not be checked", tc.onCheck
				if how == "the library replaced" {
					want, expect = "the library changed too much while it was read to follow it", tc.whenCut
				}
				switch expect {
				case refuse:
					if refusal == "" || !strings.Contains(refusal, map[string]string{"the check failing": "reading every match again", "the library replaced": "changed too much"}[how]) {
						t.Errorf("%s: %v, refusal %q; want it refused", name, out, refusal)
					}
					if tc.tool == "library_export" {
						if _, err := os.Stat(path); !os.IsNotExist(err) {
							t.Errorf("%s: the part-written file is still there (%v)", name, err)
						}
					}
					continue
				case "":
					t.Fatalf("%s: no expectation", name)
				}
				if refusal != "" {
					t.Errorf("%s: refused %q; want an answer saying %q", name, refusal, want)
					continue
				}
				note := text(out["note"])
				switch expect {
				case answer:
					if !strings.Contains(note, want) {
						t.Errorf("%s: note = %q, want it to say %q", name, note, want)
					}
					if tc.tool == "library_export" {
						if _, err := os.Stat(path); err != nil {
							t.Errorf("%s: the file it wrote whole: %v", name, err)
						}
					}
				case noSum:
					if !strings.Contains(note, want) || out["total"] != nil || len(objects(t, out["items"], "items")) != 10 {
						t.Errorf("%s: total %v, note %q; want the page read, no total, and %q", name, out["total"], note, want)
					}
				case rows:
					// audit_all's own note names the audits, and each row
					// says what its read saw, or that it has no count
					var said []string
					uncounted := false
					for _, row := range objects(t, out["audits"], "audits") {
						said = append(said, text(row["note"]))
						if skipped, ok := row["skipped"].(bool); ok && skipped && strings.Contains(text(row["note"]), want) {
							uncounted = true
						}
					}
					all := strings.Join(said, "; ")
					if how == "the check failing" && (!strings.Contains(note, "may be off") || !strings.Contains(all, want)) {
						t.Errorf("%s: note = %q, rows %q; want it to say the counts may be off, and a row %q", name, note, all, want)
					}
					if how == "the library replaced" && (!strings.Contains(note, "could not be counted") || !uncounted) {
						t.Errorf("%s: note = %q, rows %q; want a row left uncounted saying %q, and the note naming it", name, note, all, want)
					}
				}
			}
		}
	}
}

// library_items counts a title search itself, and pages it itself: neither
// server counts one (Emby answers 0 to a search sent with a limit, Jellyfin at
// most three times the limit) nor lists past three times the limit
// (Jellyfin), so total read 0, or a number short of the matches, and a caller
// paging by it stopped early. A search too broad to read whole says it has no
// total rather than give a wrong one.
func TestLibraryItemsCountsASearchItself(t *testing.T) {
	t.Parallel()

	for _, jellyfin := range []bool{false, true} {
		for _, matches := range []int{40, 2500} {
			f := newFakeServer(t)
			f.jellyfin = jellyfin
			f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
				start, limit := 0, matches
				if v := values(r, "StartIndex"); len(v) > 0 {
					n, err := strconv.Atoi(v[0])
					if err != nil {
						t.Errorf("StartIndex %q: %v", v[0], err)
					}
					start = n
				}
				if v := values(r, "Limit"); len(v) > 0 {
					n, err := strconv.Atoi(v[0])
					if err != nil {
						t.Errorf("Limit %q: %v", v[0], err)
					}
					limit = n
				}
				// as each server answers a search
				listed, total := matches, 0
				if jellyfin {
					listed = min(matches, 3*limit)
					total = listed
				}
				var rows []map[string]any
				for i := start; i < min(start+limit, listed); i++ {
					rows = append(rows, growingRow(typeMovie, fmt.Sprintf("a%04d", i), i))
				}
				writeJSON(t, w, map[string]any{"Items": rows, "TotalRecordCount": total})
			})
			cs := session(t, f, Options{})
			name := fmt.Sprintf("jellyfin %v, %d matches", jellyfin, matches)

			out := mustCall(t, cs, "library_items", map[string]any{"query": "zzy", "limit": 10, "offset": 30})
			items := objects(t, out["items"], "items")
			if matches == 40 {
				if number(t, out["total"], "total") != 40 || len(items) != 10 || text(items[0]["id"]) != "a0030" || text(out["note"]) != "" {
					t.Errorf("%s: total %v, %d items from %v, note %q; want 40, the ten from a0030", name, out["total"], len(items), items, text(out["note"]))
				}
				continue
			}
			if out["total"] != nil || len(items) != 10 || !strings.Contains(text(out["note"]), "more than 2000") {
				t.Errorf("%s: total %v, %d items, note %q; want no total, the page, and why", name, out["total"], len(items), text(out["note"]))
			}
			if msg := mustRefuse(t, cs, "library_items", map[string]any{"query": "zzy", "sort": "name"}); !strings.Contains(msg, "too many to sort") {
				t.Errorf("%s: a sort of a search too broad = %s", name, msg)
			}
		}
	}
}

// Every answer read from a whole library says when the library was seen to
// change under the read, and says nothing when it held still: library_export
// and every audit read without the note, so a file or a worklist missing
// what arrived mid-read, or holding what left, read as the whole library.
func TestWholeLibraryReadsSayWhenTheLibraryChanged(t *testing.T) {
	t.Parallel()

	// a read of more items than one request takes; a sweep of the whole
	// server takes ten thousand a request
	const items, sweep = 1500, 10500
	played := func(r *http.Request) bool { return slices.Contains(values(r, "Filters"), "IsPlayed") }
	inView := func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/Users/") }
	for _, tc := range []struct {
		tool string
		args map[string]any
		n    int
		// jellyfinOnly is a tool that reads the library whole on Jellyfin
		// alone: Emby answers it in one request, and embyOnly one that reads
		// it twice on Emby alone
		jellyfinOnly, embyOnly bool
		// only, when set, grows the library under one of the tool's reads
		// alone, named by read
		only func(r *http.Request) bool
		read string
	}{
		{tool: "audit_missing_metadata"},
		{tool: "audit_missing_metadata", args: map[string]any{"problems": "provider_id", "missing": "tmdb"}},
		{tool: "audit_multiple_versions"},
		{tool: "audit_duplicates"},
		{tool: "audit_file_path"},
		{tool: "audit_duplicate_episodes"},
		{tool: "audit_disc_folders", n: sweep},
		{tool: "audit_runtime"},
		{tool: "audit_quality"},
		{tool: "audit_missing_episodes"},
		{tool: "audit_spelling"},
		{tool: "audit_unwatched"},
		{tool: "audit_unwatched", only: played, read: "what each account played"},
		{tool: "audit_unwatched", only: func(r *http.Request) bool { return !played(r) }, read: "what the library holds"},
		// Emby reads what it shows people in two steps: the items, and an
		// administrator's view of them
		{tool: "audit_multiple_versions", only: inView, read: "an administrator's view", embyOnly: true},
		{tool: "audit_multiple_versions", only: func(r *http.Request) bool { return !inView(r) }, read: "the items", embyOnly: true},
		{tool: "audit_orphans", n: sweep},
		{tool: "audit_language", args: map[string]any{"language": "eng"}},
		{tool: "audit_provider"},
		{tool: "audit_anime_ids"},
		{tool: "library_export"},
		{tool: "library_items", args: map[string]any{"query": "zzy", "sort": "added"}},
		{tool: "library_filters"},
		{tool: "user_stats", args: map[string]any{"user": "Quux"}},
		{tool: "metadata_rename", args: map[string]any{"field": "genres", "from": "Zzyzxcore", "to": "Zzyzx-core"}},
		{tool: "item_orphans_delete", args: map[string]any{"folder": "/zz/gone"}, n: sweep},
		{tool: "item_find_by_metadata_id", args: map[string]any{"metadata_provider": "tmdb", "id": "1", "type": "movie"}, jellyfinOnly: true},
		// every row's read spans two requests, the sweeps of the whole
		// server's too
		{tool: "audit_all", n: sweep},
	} {
		for _, jellyfin := range []bool{false, true} {
			if tc.jellyfinOnly && !jellyfin || tc.embyOnly && jellyfin {
				continue
			}
			for _, change := range []string{"none", "growing", "one out and one in"} {
				// the one out and one in go before the second request of the
				// first read alone
				if tc.read != "" && change == "one out and one in" {
					continue
				}
				grow := change != "none"
				name := fmt.Sprintf("%s (jellyfin %v, %s)", tc.tool, jellyfin, change)
				if tc.read != "" {
					name = fmt.Sprintf("%s (jellyfin %v, %s under %s)", tc.tool, jellyfin, change, tc.read)
				}
				g := &growing{n: items, grow: change == "growing", swap: change == "one out and one in", only: tc.only}
				if tc.n > 0 {
					g.n = tc.n
				}
				f := growingServer(t, jellyfin, g)
				cs := session(t, f, Options{EnableDelete: true, TMDBKey: "k", ProviderTransport: tmdbTransport(t, nil), AnimeList: animeListFile(t)})
				args := map[string]any{}
				maps.Copy(args, tc.args)
				if tc.tool == "library_export" {
					args["path"] = filepath.Join(t.TempDir(), "export.jsonl")
				}
				out, refusal := callTool(t, cs, tc.tool, args)
				if refusal != "" {
					t.Errorf("%s: %s", name, refusal)
					continue
				}
				note := text(out["note"])
				switch changed := strings.Contains(note, "the library changed while"); {
				case grow && !changed:
					t.Errorf("%s: note = %q, want it to say the library changed", name, note)
				case !grow && (changed || strings.Contains(note, "the server says")):
					t.Errorf("%s: note = %q of a library that held still", name, note)
				}
				if g.asked < 2 && tc.tool != "item_find_by_metadata_id" {
					t.Errorf("%s: %d requests for items, so the read never spanned two", name, g.asked)
				}
				if g.swap && !g.swapped {
					t.Errorf("%s: the read ended before its second request, so nothing changed under it", name)
				}
				if tc.tool != "audit_all" || change == "one out and one in" {
					continue
				}
				// and each audit's own row says what its read saw
				for _, row := range objects(t, out["audits"], "audits") {
					if skipped, ok := row["skipped"].(bool); ok && skipped {
						continue
					}
					note := text(row["note"])
					if changed := strings.Contains(note, "the library changed while it was read"); changed != grow {
						t.Errorf("%s: %s's note = %q", name, text(row["audit"]), note)
					}
				}
			}
		}
	}
}
