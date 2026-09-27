package tools

import (
	"cmp"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// diskState is a canned server's disk: the paths it holds, folders ending
// in "/", and what a delete of each item takes off it.
type diskState struct {
	mu      sync.Mutex
	paths   map[string]bool
	deletes map[string][]string // item id -> paths the server removes (a folder takes what is under it)
	deleted []string

	// what the server says beside the disk: the scan task's state ("" for
	// Idle), whether it answers the task list at all, whether a library is
	// being scanned on its own, the collections and what each holds, the
	// items under a folder item, and whether a user's view of an item fails
	scan        string
	tasksFail   bool
	refreshing  bool
	collections map[string][]string
	under       map[string][]map[string]any
	viewFails   bool
	// deleteFails answers a delete with a 500 after it has taken the files,
	// as a server failing part way does; unreadable is a folder whose
	// listing the server answers with a 500
	deleteFails bool
	unreadable  string
}

func (d *diskState) list(dir string) ([]map[string]any, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if dir != "/" && !d.paths[dir+"/"] {
		return nil, false
	}
	var out []map[string]any
	for p := range d.paths {
		name := strings.TrimSuffix(p, "/")
		if path.Dir(name) != dir {
			continue
		}
		kind := "File"
		if strings.HasSuffix(p, "/") {
			kind = "Directory"
		}
		out = append(out, map[string]any{"Name": path.Base(name), "Path": name, "Type": kind})
	}

	return out, true
}

func (d *diskState) remove(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.deleted = append(d.deleted, id)
	for _, gone := range d.deletes[id] {
		for p := range d.paths {
			if p == gone || strings.HasPrefix(p, strings.TrimSuffix(gone, "/")+"/") {
				delete(d.paths, p)
			}
		}
	}
}

// deleteServer is a canned Emby whose disk item_delete reads before and
// after a delete, with the items given.
func deleteServer(t *testing.T, items map[string]map[string]any, disk *diskState) *fakeServer {
	t.Helper()

	f, libs := zzyzxServer(t)
	if disk.refreshing {
		libs.folders[0]["RefreshProgress"] = 50.0
	}
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if it, ok := items[q.Get("Ids")]; ok {
			writeJSON(t, w, page(it))
			return
		}
		disk.mu.Lock()
		defer disk.mu.Unlock()
		switch {
		case q.Get("IncludeItemTypes") == "BoxSet":
			var cols []map[string]any
			for id := range disk.collections {
				cols = append(cols, map[string]any{"Id": id, "Name": "Zzyzx " + id, "Type": "BoxSet"})
			}
			writeJSON(t, w, page(cols...))
		case disk.collections[q.Get("ParentId")] != nil:
			var members []map[string]any
			for _, id := range disk.collections[q.Get("ParentId")] {
				members = append(members, map[string]any{"Id": id, "Name": "Member " + id, "Type": "Movie"})
			}
			writeJSON(t, w, page(members...))
		case disk.under[q.Get("ParentId")] != nil:
			writeJSON(t, w, page(disk.under[q.Get("ParentId")]...))
		default:
			writeJSON(t, w, page())
		}
	})
	f.mux.HandleFunc("GET /Users/u1/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		if disk.viewFails {
			http.Error(w, "the server is busy", http.StatusInternalServerError)
			return
		}
		writeJSON(t, w, items[r.PathValue("id")])
	})
	f.mux.HandleFunc("GET /ScheduledTasks", func(w http.ResponseWriter, _ *http.Request) {
		if disk.tasksFail {
			http.Error(w, "the server is busy", http.StatusInternalServerError)
			return
		}
		writeJSON(t, w, []map[string]any{{"Id": "t1", "Key": "RefreshLibrary", "Name": "Scan media library", "Category": "Library", "State": cmp.Or(disk.scan, "Idle")}})
	})
	f.mux.HandleFunc("GET /Environment/DirectoryContents", func(w http.ResponseWriter, r *http.Request) {
		if disk.unreadable != "" && r.URL.Query().Get("Path") == disk.unreadable {
			http.Error(w, "access denied", http.StatusInternalServerError)
			return
		}
		entries, ok := disk.list(r.URL.Query().Get("Path"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(t, w, entries)
	})
	f.mux.HandleFunc("POST /Environment/ValidatePath", func(w http.ResponseWriter, r *http.Request) {
		disk.mu.Lock()
		p := r.URL.Query().Get("Path")
		held := disk.paths[p] || disk.paths[p+"/"]
		disk.mu.Unlock()
		if !held {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	f.mux.HandleFunc("DELETE /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		disk.remove(r.PathValue("id"))
		if disk.deleteFails {
			http.Error(w, "database is locked", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	return f
}

// A delete the server answers with an error can have taken the files all
// the same: the answer was the server's error alone. The disk and the
// server's list are read back, and the error says what went.
func TestAFailedDeleteSaysWhatWent(t *testing.T) {
	t.Parallel()

	const folder = "/zz/films/Zzyzx (2001)"
	items := map[string]map[string]any{"5": {"Id": "5", "Name": "Zzyzx", "Type": "Movie", "Path": folder + "/Zzyzx (2001).mkv"}}
	disk := &diskState{
		paths:       map[string]bool{"/zz/": true, "/zz/films/": true, folder + "/": true, folder + "/Zzyzx (2001).mkv": true, folder + "/movie.nfo": true},
		deletes:     map[string][]string{"5": {folder + "/"}},
		deleteFails: true,
	}
	f := deleteServer(t, items, disk)
	r := &registry{client: f.client(t), settle: time.Millisecond, opts: Options{EnableDelete: true}}
	registerItemTools(r)
	msg := mustRefuse(t, hostRegistry(t, r), "item_delete", map[string]any{"id": "5", "confirm": true})
	for _, want := range []string{"the delete of Zzyzx answered an error", "database is locked", "Read back, the server still lists it", "from the disk, these went: " + folder} {
		if !strings.Contains(msg, want) {
			t.Errorf("a delete that failed after taking the files: %s, want %q", msg, want)
		}
	}

	// a folder that could not be read before: what went is not known, and
	// said so, with whether the item's own file is still there - not that
	// it had nothing on disk to take
	const other = "/zz/films/Plugh (2002)"
	items["6"] = map[string]any{"Id": "6", "Name": "Plugh", "Type": "Movie", "Path": other + "/Plugh (2002).mkv"}
	disk.mu.Lock()
	disk.paths[other+"/"], disk.paths[other+"/Plugh (2002).mkv"] = true, true
	disk.deletes["6"] = []string{other + "/"}
	disk.unreadable = other
	disk.mu.Unlock()
	msg = mustRefuse(t, hostRegistry(t, r), "item_delete", map[string]any{"id": "6", "confirm": true})
	if !strings.Contains(msg, "what it took from the disk is not known, since the folder holding it could not be read before; its own file "+other+"/Plugh (2002).mkv is gone") || strings.Contains(msg, "nothing on disk") {
		t.Errorf("a failed delete of an item whose folder was not read: %s", msg)
	}

	// a library the server fails to remove, still listed
	lf, _ := zzyzxServer(t)
	lf.mux.HandleFunc("POST /Library/VirtualFolders/Delete", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "database table is locked", http.StatusInternalServerError)
	})
	lf.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page()) })
	idleScans(t, lf)
	lr := &registry{client: lf.client(t), settle: time.Millisecond, opts: Options{EnableDelete: true}}
	registerLibraryTools(lr)
	msg = mustRefuse(t, hostRegistry(t, lr), "library_delete", map[string]any{"library": "Zzyzx Films", "confirm": true})
	if !strings.Contains(msg, "the delete of the Zzyzx Films library failed, and it is still listed, read back") || !strings.Contains(msg, "database table is locked") {
		t.Errorf("a library delete that failed: %s", msg)
	}
}

// item_delete says what the delete takes off the disk, which is more than
// the item's own file: both servers delete a film's whole folder when the
// film is alone in it (its nfo, its artwork, its extras and every version),
// and an episode takes the files named after it (seen live on both). Without
// confirm it refuses, saying what it would remove; with it, the answer lists
// what was removed, read before and after.
func TestItemDeleteSaysWhatItRemoves(t *testing.T) {
	t.Parallel()

	const folder = "/zz/films/Zzyzx (2001)"
	source := func(p string) map[string]any { return map[string]any{"Path": p} }
	items := map[string]map[string]any{
		"5": {
			"Id": "5", "Name": "Zzyzx", "Type": "Movie", "Path": folder + "/Zzyzx (2001) - 1080p.mkv",
			"MediaSources": []map[string]any{source(folder + "/Zzyzx (2001) - 1080p.mkv"), source(folder + "/Zzyzx (2001) - 720p.mkv")},
		},
		"7": {
			"Id": "7", "Name": "Pilot", "Type": "Episode", "Path": "/zz/films/Plugh/Season 01/Plugh S01E01.mkv",
			"MediaSources": []map[string]any{source("/zz/films/Plugh/Season 01/Plugh S01E01.mkv")},
		},
	}
	disk := &diskState{paths: map[string]bool{}, deletes: map[string][]string{
		"5": {folder + "/"},
		"7": {"/zz/films/Plugh/Season 01/Plugh S01E01.mkv", "/zz/films/Plugh/Season 01/Plugh S01E01.nfo"},
	}}
	for _, p := range []string{
		"/zz/", "/zz/films/", folder + "/", folder + "/Zzyzx (2001) - 1080p.mkv", folder + "/Zzyzx (2001) - 720p.mkv", folder + "/movie.nfo", folder + "/poster.jpg",
		"/zz/films/Plugh/", "/zz/films/Plugh/Season 01/", "/zz/films/Plugh/Season 01/Plugh S01E01.mkv", "/zz/films/Plugh/Season 01/Plugh S01E01.nfo", "/zz/films/Plugh/Season 01/Plugh S01E02.mkv",
	} {
		disk.paths[p] = true
	}
	f := deleteServer(t, items, disk)
	r := &registry{client: f.client(t), settle: time.Millisecond, opts: Options{EnableDelete: true}}
	registerItemTools(r)
	cs := hostRegistry(t, r)

	// without confirm: what it would remove, and nothing gone
	msg := mustRefuse(t, cs, "item_delete", map[string]any{"id": "5"})
	for _, want := range []string{"confirm=true", "would remove", folder, "Zzyzx (2001) - 720p.mkv", "movie.nfo", "poster.jpg"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not say %q: %s", want, msg)
		}
	}
	if len(disk.deleted) != 0 {
		t.Fatalf("a delete without confirm was sent: %v", disk.deleted)
	}

	removed := func(out map[string]any) []string {
		rows := objects(t, out["removed"], "removed")
		got := make([]string, 0, len(rows))
		for _, row := range rows {
			p := text(row["path"])
			if isFolder, ok := row["folder"].(bool); ok && isFolder {
				p += "/"
			}
			got = append(got, p)
		}
		slices.Sort(got)
		return got
	}
	out := mustCall(t, cs, "item_delete", map[string]any{"id": "5", "confirm": true})
	want := []string{folder + "/", folder + "/Zzyzx (2001) - 1080p.mkv", folder + "/Zzyzx (2001) - 720p.mkv", folder + "/movie.nfo", folder + "/poster.jpg"}
	if got := removed(out); !slices.Equal(got, want) || !strings.HasPrefix(text(out["deleted"]), "Zzyzx") {
		t.Errorf("item_delete of a film alone in its folder = %v (removed %v), want %v", out, got, want)
	}

	// an episode among others: its file and the nfo named after it
	msg = mustRefuse(t, cs, "item_delete", map[string]any{"id": "7"})
	if !strings.Contains(msg, "Plugh S01E01.mkv") || !strings.Contains(msg, "Plugh S01E01.nfo") || strings.Contains(msg, "S01E02") {
		t.Errorf("the refusal for an episode: %s", msg)
	}
	out = mustCall(t, cs, "item_delete", map[string]any{"id": "7", "confirm": true})
	if got := removed(out); !slices.Equal(got, []string{"/zz/films/Plugh/Season 01/Plugh S01E01.mkv", "/zz/films/Plugh/Season 01/Plugh S01E01.nfo"}) {
		t.Errorf("item_delete of an episode removed %v", got)
	}
}

// A delete while a library scan runs can be undone for a while: a scan that
// read the item's folder before the delete lists the item again when it
// finishes, pointing at files that are gone, until the next scan (seen on
// Jellyfin 12.1). item_delete says so when a scan was running, and says
// nothing of it when none was.
func TestItemDeleteSaysWhenAScanWasRunning(t *testing.T) {
	t.Parallel()

	// the scan of every library is a task; the scan of one library is not,
	// and shows only as the library's progress (seen on Emby 4.10, where the
	// task stayed idle throughout)
	for _, tc := range []struct {
		scan       string
		refreshing bool
		want       string
	}{
		{scan: "Running", want: "the scan of every library (Scan media library) was running"},
		{refreshing: true, want: "a scan of the Zzyzx Films library was running"},
		{},
	} {
		const folder = "/zz/films/Zzyzx (2001)"
		items := map[string]map[string]any{
			"5": {"Id": "5", "Name": "Zzyzx", "Type": "Movie", "Path": folder + "/Zzyzx (2001).mkv"},
		}
		disk := &diskState{paths: map[string]bool{"/zz/": true, "/zz/films/": true, folder + "/": true, folder + "/Zzyzx (2001).mkv": true}, deletes: map[string][]string{"5": {folder + "/"}}, scan: tc.scan, refreshing: tc.refreshing}
		f := deleteServer(t, items, disk)
		r := &registry{client: f.client(t), settle: time.Millisecond, opts: Options{EnableDelete: true}}
		registerItemTools(r)
		cs := hostRegistry(t, r)

		out := mustCall(t, cs, "item_delete", map[string]any{"id": "5", "confirm": true})
		note := text(out["note"])
		if tc.want == "" && strings.Contains(note, "was running") || tc.want != "" && !strings.Contains(note, tc.want+": it can list this item again") {
			t.Errorf("scan %q, one library refreshing %v: note = %q", tc.scan, tc.refreshing, note)
		}
	}

	// a task list the server will not give: whether the delete could come
	// back is not known, so nothing is deleted
	items := map[string]map[string]any{"5": {"Id": "5", "Name": "Zzyzx", "Type": "Movie", "Path": "/zz/films/Zzyzx (2001)/Zzyzx (2001).mkv"}}
	disk := &diskState{paths: map[string]bool{"/zz/": true, "/zz/films/": true, "/zz/films/Zzyzx (2001)/": true, "/zz/films/Zzyzx (2001)/Zzyzx (2001).mkv": true}, tasksFail: true}
	f := deleteServer(t, items, disk)
	r := &registry{client: f.client(t), settle: time.Millisecond, opts: Options{EnableDelete: true}}
	registerItemTools(r)
	if msg := mustRefuse(t, hostRegistry(t, r), "item_delete", map[string]any{"id": "5", "confirm": true}); !strings.Contains(msg, "could not tell whether a scan was running, so Zzyzx was not deleted") {
		t.Errorf("a task list that failed: %s", msg)
	}
	if len(disk.deleted) != 0 {
		t.Errorf("deleted %v without knowing whether a scan ran", disk.deleted)
	}
}

// What a delete takes that its file does not say: a folder item - a series -
// goes whole with every item the server holds under it, counted; the items
// leave the collections that held them, named before and after; a view of
// the item that fails, which hides its other versions, stops the delete
// rather than understate it; and a folder that cannot be read is said to be
// unknown, which confirm deletes all the same.
func TestItemDeleteSaysWhatGoesWithIt(t *testing.T) {
	t.Parallel()

	const show = "/zz/films/Plugh"
	items := map[string]map[string]any{
		"20": {"Id": "20", "Name": "Plugh", "Type": "Series", "IsFolder": true, "Path": show},
		"5":  {"Id": "5", "Name": "Zzyzx", "Type": "Movie", "Path": "/zz/films/Zzyzx (2001)/Zzyzx (2001).mkv"},
	}
	disk := &diskState{
		paths: map[string]bool{
			"/zz/": true, "/zz/films/": true, show + "/": true, show + "/tvshow.nfo": true, show + "/Season 01/": true,
			show + "/Season 01/Plugh S01E01.mkv": true, show + "/Season 01/Plugh S01E02.mkv": true,
			"/zz/films/Zzyzx (2001)/": true, "/zz/films/Zzyzx (2001)/Zzyzx (2001).mkv": true,
		},
		deletes:     map[string][]string{"20": {show + "/"}},
		collections: map[string][]string{"c1": {"21", "5"}},
		under: map[string][]map[string]any{"20": {
			{"Id": "30", "Name": "Season 1", "Type": "Season"},
			{"Id": "21", "Name": "Pilot", "Type": "Episode"},
			{"Id": "22", "Name": "Second", "Type": "Episode"},
		}},
	}
	f := deleteServer(t, items, disk)
	r := &registry{client: f.client(t), settle: time.Millisecond, opts: Options{EnableDelete: true}}
	registerItemTools(r)
	cs := hostRegistry(t, r)

	msg := mustRefuse(t, cs, "item_delete", map[string]any{"id": "20"})
	for _, want := range []string{
		"it would remove the folder " + show + " with everything in it, 4 files and folders",
		"Plugh is a Series, and goes whole: with it go the 3 items the server holds under it (Episode 2, Season 1)",
		"It would leave the collection Zzyzx c1 (1)",
		"deletes what is there when it runs",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not say %q: %s", want, msg)
		}
	}
	out := mustCall(t, cs, "item_delete", map[string]any{"id": "20", "confirm": true})
	if number(t, out["items_under"], "items_under") != 3 || number(t, object(t, out["items_under_by_type"], "items_under_by_type")["Episode"], "Episode") != 2 {
		t.Errorf("the series' items = %v", out)
	}
	if lists := objects(t, out["lists_left"], "lists_left"); len(lists) != 1 || lists[0]["id"] != "c1" || number(t, lists[0]["held"], "held") != 1 {
		t.Errorf("lists_left = %v", out["lists_left"])
	}

	// the versions not read: refused, nothing deleted
	disk.viewFails = true
	if msg := mustRefuse(t, cs, "item_delete", map[string]any{"id": "5", "confirm": true}); !strings.Contains(msg, "could not read the versions the server holds for Zzyzx, so what the delete takes is not known and nothing was deleted") {
		t.Errorf("a view that failed: %s", msg)
	}
	if slices.Contains(disk.deleted, "5") {
		t.Error("the film was deleted with its versions unread")
	}
}

// A folder with more in it than a refusal lists is still counted whole: the
// count is the tree's, not the listing's.
func TestItemDeleteCountsABigFolder(t *testing.T) {
	t.Parallel()

	const folder = "/zz/films/Zzyzx (2001)"
	items := map[string]map[string]any{"5": {"Id": "5", "Name": "Zzyzx", "Type": "Movie", "Path": folder + "/Zzyzx (2001).mkv"}}
	disk := &diskState{paths: map[string]bool{"/zz/": true, "/zz/films/": true, folder + "/": true, folder + "/Zzyzx (2001).mkv": true, folder + "/extras/": true}}
	for i := range treeMax + 10 {
		disk.paths[fmt.Sprintf("%s/extras/still%04d.jpg", folder, i)] = true
	}
	f := deleteServer(t, items, disk)
	r := &registry{client: f.client(t), settle: time.Millisecond, opts: Options{EnableDelete: true}}
	registerItemTools(r)
	msg := mustRefuse(t, hostRegistry(t, r), "item_delete", map[string]any{"id": "5"})
	if want := fmt.Sprintf("with everything in it, %d files and folders", treeMax+12); !strings.Contains(msg, want) || !strings.Contains(msg, fmt.Sprintf("of %d in all", treeMax+12)) {
		t.Errorf("the refusal for a folder past %d entries = %.300s, want %q", treeMax, msg, want)
	}
}

// The libraries not read leave it unknown whether a film shares its folder,
// which is the difference between its file going and the whole folder going:
// the plan is an error, not the smaller answer.
func TestADeletePlanNeedsTheLibraries(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Environment/DirectoryContents", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{{"Name": "Zzyzx (2001).mkv", "Path": "/zz/films/Zzyzx (2001)/Zzyzx (2001).mkv", "Type": "File"}})
	})
	it := &embyfin.Item{ID: "5", Name: "Zzyzx", Type: "Movie", Path: "/zz/films/Zzyzx (2001)/Zzyzx (2001).mkv"}
	if _, err := planDelete(t.Context(), f.client(t), it, nil); err == nil || !strings.Contains(err.Error(), "could not read the libraries' folders, so whether the delete takes Zzyzx's whole folder is not known") {
		t.Errorf("planDelete with the libraries unread = %v", err)
	}
}

// Emby lists a copy of the same film in another folder as one of its
// versions. Deleting the plain copy said it would leave the collection
// holding the Director's Cut, which stayed, with its place in the list: a
// version goes, and its lists with it, only where its file is one the delete
// takes. And a collection or a playlist is not deleted here: the server's
// delete of one as an item leaves what it holds, which the answer counted as
// going.
func TestItemDeleteNamesOnlyWhatGoes(t *testing.T) {
	t.Parallel()

	const plain, cut = "/zz/films/Alien (1979)", "/zz/films/Alien (1979) Directors Cut"
	sources := []map[string]any{
		{"Id": "m32", "ItemId": "32", "Path": plain + "/Alien (1979).mkv"},
		{"Id": "m26", "ItemId": "26", "Path": cut + "/Alien (1979) Directors Cut.mkv"},
	}
	items := map[string]map[string]any{
		"32": {"Id": "32", "Name": "Alien", "Type": "Movie", "Path": plain + "/Alien (1979).mkv", "MediaSources": sources},
		"c9": {"Id": "c9", "Name": "Zzyzx Saga", "Type": "BoxSet", "IsFolder": true},
		"p9": {"Id": "p9", "Name": "Zzyzx Night", "Type": "Playlist", "IsFolder": true, "Path": "/config/data/userplaylists/Zzyzx Night [playlist]/Zzyzx Night.m3u"},
		"g9": {"Id": "g9", "Name": "Zzyzx Noir", "Type": "Genre"},
		"s9": {"Id": "s9", "Name": "Zzyzx Pictures", "Type": "Studio"},
		"e9": {"Id": "e9", "Name": "Zzyzx Person", "Type": "Person"},
		"a9": {"Id": "a9", "Name": "Zzyzx Band", "Type": "MusicArtist", "IsFolder": true, "Path": "/zz/music/Zzyzx Band"},
	}
	disk := &diskState{
		paths: map[string]bool{
			"/zz/": true, "/zz/films/": true,
			plain + "/": true, plain + "/Alien (1979).mkv": true, plain + "/poster.jpg": true,
			cut + "/": true, cut + "/Alien (1979) Directors Cut.mkv": true,
		},
		deletes:     map[string][]string{"32": {plain + "/"}},
		collections: map[string][]string{"c1": {"26"}},
	}
	f := deleteServer(t, items, disk)
	r := &registry{client: f.client(t), settle: time.Millisecond, opts: Options{EnableDelete: true}}
	registerItemTools(r)
	cs := hostRegistry(t, r)

	msg := mustRefuse(t, cs, "item_delete", map[string]any{"id": "32"})
	if !strings.Contains(msg, "No playlist or collection holds it") || strings.Contains(msg, "Zzyzx c1") || !strings.Contains(msg, "it would remove the folder "+plain) {
		t.Errorf("the refusal for the plain copy: %s", msg)
	}
	out := mustCall(t, cs, "item_delete", map[string]any{"id": "32", "confirm": true})
	if lists := objects(t, out["lists_left"], "lists_left"); len(lists) != 0 {
		t.Errorf("lists_left = %v, want none: the cut and its collection stay", lists)
	}

	for id, want := range map[string]string{
		"c9": "Zzyzx Saga is a BoxSet, which item_delete does not take: collection_delete deletes a collection, and leaves the items it holds. Nothing was deleted",
		"p9": "Zzyzx Night is a Playlist, which item_delete does not take: playlist_delete deletes a playlist, and leaves the items it holds. Nothing was deleted",
		"g9": "Zzyzx Noir is a Genre, which item_delete does not take: a genre is a name items carry, not something on disk: metadata_rename renames it",
		"s9": "Zzyzx Pictures is a Studio, which item_delete does not take: a studio is a name items carry",
		"e9": "Zzyzx Person is a Person, which item_delete does not take: a person is a name items credit, not something on disk, and no tool deletes one",
		"a9": "Zzyzx Band is a MusicArtist, which item_delete does not take: an artist is a name the server gathers albums and songs under",
	} {
		if msg := mustRefuse(t, cs, "item_delete", map[string]any{"id": id, "confirm": true}); !strings.Contains(msg, want) {
			t.Errorf("item_delete of %s: %s", id, msg)
		}
	}
	if len(disk.deleted) != 1 {
		t.Errorf("deleted %v: something other than the plain copy was deleted", disk.deleted)
	}
}
