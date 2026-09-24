package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The path arithmetic the orphan tools stand on. The near miss is the shape a
// rename leaves: a folder renamed to a name that starts with the old one, where
// a string prefix check would call everything in the new library an orphan of
// the old one.
func TestOrphanPaths(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		path, root string
		want       bool
	}{
		{"/data/doc/A (2020)/A (2020).mkv", "/data/doc", true},
		{"/data/doc", "/data/doc", true},
		{"/data/doc", "/data/doc/", true},
		{"/data/docs/A (2020)/A (2020).mkv", "/data/doc", false},
		{"/data/doc2/A.mkv", "/data/doc", false},
		{"/data/do", "/data/doc", false},
		{"/anything", "/", true},
		{`D:\Video\Docs\A.mkv`, `D:\Video\Docs`, true},
		{`D:\Video\Docs2\A.mkv`, `D:\Video\Docs`, false},
		{`\\nas\video\docs\A.mkv`, `\\nas\video`, true},
		{"/data/films", "", false},
	} {
		if got := within(tc.path, tc.root); got != tc.want {
			t.Errorf("within(%q, %q) = %v, want %v", tc.path, tc.root, got, tc.want)
		}
	}

	for _, tc := range []struct{ path, want string }{
		{"/data/doc/", "/data"},
		{"/mnt", "/"},
		{"/", ""},
		{`C:\Video`, `C:\`},
		{`C:\`, ""},
		{`\\nas\video\docs`, `\\nas\video`},
		{`\\nas\video`, ""},
	} {
		if got := parentDir(tc.path); got != tc.want {
			t.Errorf("parentDir(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}

	for p, want := range map[string]bool{"/data/a.mkv": true, `C:\a.mkv`: true, `\\nas\a.mkv`: true, "https://example.org/t.mp4": false, "": false, "a.mkv": false} {
		if onDisk(p) != want {
			t.Errorf("onDisk(%q) = %v", p, !want)
		}
	}

	libs := []libraryPath{{"Movies", "/data/films"}, {"Documentaries", "/data/docs"}}
	for _, tc := range []struct{ path, want string }{
		// up to the folder beside the libraries, which is where the renamed one was
		{"/data/doc/A (2020)/A (2020).mkv", "/data/doc"},
		// nothing above holds a library, so it climbs to just below the top
		{"/srv/recordings/news.ts", "/srv"},
		// a file at the top of a filesystem is its own folder
		{"/a.mkv", "/a.mkv"},
	} {
		if got := orphanFolder(tc.path, libs); got != tc.want {
			t.Errorf("orphanFolder(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// orphanItem is an item as the MediaBrowser API lists one, as much as the
// orphan tools read.
type orphanItem struct {
	ID       string `json:"Id"`
	Name     string `json:"Name"`
	Type     string `json:"Type"`
	Path     string `json:"Path,omitempty"`
	ParentID string `json:"ParentId,omitempty"`
}

// orphanServer is a canned server with two libraries and the leftovers of a
// renamed one, the way a rename leaves them: two copies of a film and a folder
// for each, under the old folder name, which the server can no longer find and
// which is a string prefix of the new one.
type orphanServer struct {
	*fakeServer

	mu      sync.Mutex
	items   []orphanItem
	dirs    map[string]bool // folders the server can see
	files   map[string]bool // files it can see
	deleted []string        // ids, in the order deletes named them
	// onDelete runs as a batch delete arrives, before it is applied
	onDelete func(n int)
	// refuse is ids the server will not delete
	refuse map[string]bool
	// unanswered makes every path check fail, as a server that errors does
	unanswered bool
}

func newOrphanServer(t *testing.T, jellyfin bool) *orphanServer {
	t.Helper()

	o := newOrphanServerOf(t, jellyfin, []map[string]any{
		{"Name": "Movies", "CollectionType": "movies", "ItemId": "lib1", "Locations": []string{"/data/films"}},
		{"Name": "Documentaries", "CollectionType": "movies", "ItemId": "lib2", "Locations": []string{"/data/docs/"}},
	})
	o.items = []orphanItem{
		{ID: "lm", Name: "movies", Type: "Folder", Path: "/data/films"},
		{ID: "m1", Name: "Alien", Type: "Movie", Path: "/data/films/Alien (1979)/Alien (1979).mkv", ParentID: "lm"},
		{ID: "d1", Name: "Some Doc", Type: "Movie", Path: "/data/docs/Some Doc (2020)/Some Doc (2020).mkv"},
		// the leftovers: parents the server no longer holds
		{ID: "f1", Name: "Some Doc (2020)", Type: "Folder", Path: "/data/doc/Some Doc (2020)", ParentID: "gone1"},
		{ID: "o1", Name: "Some Doc", Type: "Movie", Path: "/data/doc/Some Doc (2020)/Some Doc (2020).mkv", ParentID: "f1"},
		{ID: "o2", Name: "Some Doc", Type: "Movie", Path: "/data/doc/Some Doc (2020)/Some Doc (2020).mkv", ParentID: "gone2"},
		{ID: "f2", Name: "Other Doc (2019)", Type: "Folder", Path: "/data/doc/Other Doc (2019)", ParentID: "gone1"},
		{ID: "o3", Name: "Other Doc", Type: "Movie", Path: "/data/doc/Other Doc (2019)/Other Doc (2019).mkv", ParentID: "f2"},
		// outside every library but still on disk: not leftovers, and never deleted
		{ID: "r1", Name: "News", Type: "Video", Path: "/srv/recordings/news.ts"},
		// a trailer the server streams from elsewhere has no folder at all
		{ID: "t1", Name: "Trailer", Type: "Trailer", Path: "https://example.org/trailer.mp4"},
	}
	o.dirs = map[string]bool{"/data/films": true, "/data/docs": true, "/srv": true, "/srv/recordings": true}
	o.files = map[string]bool{"/srv/recordings/news.ts": true}

	return o
}

// newOrphanServerOf is a canned server with the libraries given and nothing
// else: the caller fills in the items and what is on disk.
func newOrphanServerOf(t *testing.T, jellyfin bool, libraries []map[string]any) *orphanServer {
	t.Helper()

	o := &orphanServer{fakeServer: newFakeServer(t), refuse: map[string]bool{}, dirs: map[string]bool{}, files: map[string]bool{}}
	o.jellyfin = jellyfin
	o.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": libraries, "TotalRecordCount": len(libraries)})
	})
	o.mux.HandleFunc("GET /Library/VirtualFolders", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(libraries)
	})
	o.mux.HandleFunc("GET /Items", o.list)
	o.mux.HandleFunc("POST /Environment/ValidatePath", o.validate)
	o.mux.HandleFunc("DELETE /Items", o.deleteBatch)
	o.mux.HandleFunc("DELETE /Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		defer o.mu.Unlock()
		if !o.remove(r.PathValue("id"), true) {
			http.Error(w, "not found", http.StatusNotFound)

			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	return o
}

// values reads a query parameter the way either server spells it - Emby's
// Ids=a,b or Jellyfin's ids=a&ids=b - as one list.
func values(r *http.Request, name string) []string {
	var out []string
	for k, vs := range r.URL.Query() {
		if strings.EqualFold(k, name) {
			for _, v := range vs {
				out = append(out, strings.Split(v, ",")...)
			}
		}
	}

	return out
}

func (o *orphanServer) list(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	types, ids := values(r, "IncludeItemTypes"), values(r, "Ids")
	var rows []orphanItem
	for _, it := range o.items {
		if (len(types) == 0 || slices.Contains(types, it.Type)) && (len(ids) == 0 || slices.Contains(ids, it.ID)) {
			rows = append(rows, it)
		}
	}
	total := len(rows)
	start, limit := 0, len(rows)
	if v := values(r, "StartIndex"); len(v) > 0 {
		start, _ = strconv.Atoi(v[0])
	}
	if v := values(r, "Limit"); len(v) > 0 {
		limit, _ = strconv.Atoi(v[0])
	}
	rows = rows[min(start, len(rows)):min(start+limit, len(rows))]
	_ = json.NewEncoder(w).Encode(map[string]any{"Items": rows, "TotalRecordCount": total})
}

func (o *orphanServer) validate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path   string
		IsFile bool
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	path := body.Path
	if !o.jellyfin {
		// Emby takes the path in the query, the rest in the body
		path = r.URL.Query().Get("Path")
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if o.unanswered {
		http.Error(w, "boom", http.StatusInternalServerError)

		return
	}
	if (body.IsFile && o.files[path]) || (!body.IsFile && o.dirs[path]) {
		w.WriteHeader(http.StatusNoContent)

		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

// deleteBatch answers a batch delete the way each server does: Emby skips an
// id it cannot find and takes a folder's items with it, Jellyfin stops at the
// first id it cannot find with the ones before it already gone.
func (o *orphanServer) deleteBatch(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.onDelete != nil {
		o.onDelete(len(o.deleted))
	}
	for _, id := range values(r, "Ids") {
		if o.refuse[id] {
			http.Error(w, "access denied", http.StatusInternalServerError)

			return
		}
		if !o.remove(id, true) && o.jellyfin {
			http.Error(w, "Error processing request.", http.StatusBadRequest)

			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// remove deletes an item, and on Emby what a folder holds; the caller holds
// mu. A refused id is refused when it is named, not when its folder takes it.
func (o *orphanServer) remove(id string, direct bool) bool {
	if direct && o.refuse[id] {
		return false
	}
	i := slices.IndexFunc(o.items, func(it orphanItem) bool { return it.ID == id })
	if i < 0 {
		return false
	}
	o.items = slices.Delete(o.items, i, i+1)
	o.deleted = append(o.deleted, id)
	if !o.jellyfin {
		for _, child := range slices.Clone(o.items) {
			if child.ParentID == id {
				o.remove(child.ID, false)
			}
		}
	}

	return true
}

// deletedIDs is what the deletes named so far.
func (o *orphanServer) deletedIDs() []string {
	o.mu.Lock()
	defer o.mu.Unlock()

	return slices.Clone(o.deleted)
}

// deletes counts the delete requests the tools sent.
func (o *orphanServer) deletes() int {
	o.fakeServer.mu.Lock()
	defer o.fakeServer.mu.Unlock()

	n := 0
	for _, r := range o.seen {
		if r.Method == http.MethodDelete {
			n++
		}
	}

	return n
}

func (o *orphanServer) holds(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	return slices.ContainsFunc(o.items, func(it orphanItem) bool { return it.ID == id })
}

func TestAuditOrphans(t *testing.T) {
	t.Parallel()

	o := newOrphanServer(t, false)
	out := mustCall(t, session(t, o.fakeServer, Options{}), "audit_orphans", nil)

	// the five leftovers and the recording; not the library items, and not a
	// trailer with no folder
	if number(t, out["total_findings"], "total_findings") != 6 {
		t.Errorf("total_findings = %v", out["total_findings"])
	}
	folders := objects(t, out["folders"], "folders")
	if len(folders) != 2 {
		t.Fatalf("folders = %v", folders)
	}
	docs, srv := folders[0], folders[1]
	if text(docs["folder"]) != "/data/doc" || number(t, docs["items"], "items") != 5 || text(docs["on_server"]) != "missing" {
		t.Errorf("the renamed folder = %v", docs)
	}
	if byType := object(t, docs["by_type"], "by_type"); number(t, byType["Movie"], "Movie") != 3 || number(t, byType["Folder"], "Folder") != 2 {
		t.Errorf("by_type = %v", byType)
	}
	if ex := objects(t, docs["examples"], "examples"); len(ex) != 5 || text(ex[0]["path"]) != "/data/doc/Other Doc (2019)" {
		t.Errorf("examples = %v", ex)
	}
	// the folder holding what is still on disk, not the top of its tree
	if text(srv["folder"]) != "/srv/recordings" || text(srv["on_server"]) != "present" {
		t.Errorf("a folder the server can see = %v", srv)
	}

	// an orphan belongs to no library and no user, so the sweep names neither
	for _, req := range o.requests("/Items") {
		q, err := url.ParseQuery(req.Query)
		if err != nil {
			t.Fatal(err)
		}
		if q.Has("ParentId") || q.Has("UserId") {
			t.Errorf("the sweep was scoped: %s", req.Query)
		}
		if types := strings.Split(q.Get("IncludeItemTypes"), ","); !slices.Contains(types, "Episode") || !slices.Contains(types, "Folder") || slices.Contains(types, "AudioBook") {
			t.Errorf("the sweep asks for the wrong kinds of item: %v", types)
		}
		// the cheap order and big pages: on a large library the default order
		// made a sweep take half an hour
		if q.Get("SortBy") != "DateCreated,SortName" || q.Get("Limit") != "10000" {
			t.Errorf("the sweep reads in the slow order or small pages: %s", req.Query)
		}
	}
}

func TestItemOrphansDelete(t *testing.T) {
	t.Parallel()

	o := newOrphanServer(t, false)
	cs := session(t, o.fakeServer, Options{EnableDelete: true})

	for folder, want := range map[string]string{
		"":                         "full path",
		"data/doc":                 "full path",
		"/":                        "top of a filesystem",
		"/data/films/Alien (1979)": "inside the Movies library",
		"/data/docs/":              "inside the Documentaries library",
		"/data":                    "holds the",
		"/srv":                     "can still see",
		"/srv/recordings":          "can still see",
		"/data/doc/../movies/":     "steps through",
	} {
		if msg := mustRefuse(t, cs, "item_orphans_delete", map[string]any{"folder": folder, "confirm": true}); !strings.Contains(msg, want) {
			t.Errorf("folder %q: %s", folder, msg)
		}
	}
	if msg := mustRefuse(t, cs, "item_orphans_delete", map[string]any{"folder": "/data/doc", "limit": 20000}); !strings.Contains(msg, "10000") {
		t.Errorf("an oversized limit: %s", msg)
	}

	// a folder whose name is a prefix of the orphans' is a different folder
	near := mustCall(t, cs, "item_orphans_delete", map[string]any{"folder": "/data/do", "confirm": true})
	if number(t, near["found"], "found") != 0 || number(t, near["deleted"], "deleted") != 0 {
		t.Errorf("a name prefix matched: %v", near)
	}

	// without confirm it says what it would do, and does nothing
	o.reset()
	preview := mustCall(t, cs, "item_orphans_delete", map[string]any{"folder": "/data/doc"})
	if number(t, preview["found"], "found") != 5 || number(t, preview["deleted"], "deleted") != 0 || number(t, preview["remaining"], "remaining") != 5 {
		t.Errorf("preview = %v", preview)
	}
	if n := o.deletes(); n != 0 {
		t.Fatalf("a preview sent %d deletes", n)
	}

	o.reset()
	out := mustCall(t, cs, "item_orphans_delete", map[string]any{"folder": "/data/doc", "confirm": true})
	if number(t, out["deleted"], "deleted") != 5 || number(t, out["remaining"], "remaining") != 0 || out["stopped"] != nil {
		t.Errorf("delete = %v", out)
	}
	// deepest first, so no delete leans on a folder taking its items with it
	if got, want := o.deletedIDs(), []string{"o3", "o1", "o2", "f2", "f1"}; !slices.Equal(got, want) {
		t.Errorf("deleted %v, want %v", got, want)
	}
	for _, id := range []string{"m1", "d1", "lm", "r1", "t1"} {
		if !o.holds(id) {
			t.Errorf("%s is gone, and was never an orphan under the folder", id)
		}
	}
	// the folder is asked after before the sweep and again before the batch
	if n := len(o.requests("/Environment/ValidatePath")); n < 2 {
		t.Errorf("the folder was checked %d times", n)
	}
}

// A folder that comes back mid-run - a share remounted - holds real files
// again, so the next batch is never sent.
func TestItemOrphansDeleteStopsWhenTheFolderReturns(t *testing.T) {
	t.Parallel()

	o := newOrphanServer(t, false)
	for i := range 60 {
		o.items = append(o.items, orphanItem{ID: fmt.Sprintf("x%02d", i), Name: "X", Type: "Movie", Path: fmt.Sprintf("/data/doc/X %02d/X.mkv", i), ParentID: "gone"})
	}
	o.onDelete = func(n int) {
		if n == 0 {
			o.dirs["/data/doc"] = true
		}
	}
	cs := session(t, o.fakeServer, Options{EnableDelete: true})

	out := mustCall(t, cs, "item_orphans_delete", map[string]any{"folder": "/data/doc", "confirm": true})
	if number(t, out["deleted"], "deleted") != 50 || number(t, out["remaining"], "remaining") != 15 || !strings.Contains(text(out["stopped"]), "can still see") {
		t.Errorf("out = %v", out)
	}
}

// Jellyfin answers 400 at the first id it cannot find, having deleted the ones
// before it: the batch is settled one id at a time, and an item that will not
// go is reported rather than lost in the count.
func TestItemOrphansDeleteOnJellyfin(t *testing.T) {
	t.Parallel()

	o := newOrphanServer(t, true)
	o.refuse["f2"] = true
	// something else removes o1 after the sweep and before the delete
	o.onDelete = func(n int) {
		if n == 0 {
			o.items = slices.DeleteFunc(o.items, func(it orphanItem) bool { return it.ID == "o1" })
		}
	}
	cs := session(t, o.fakeServer, Options{EnableDelete: true})

	out := mustCall(t, cs, "item_orphans_delete", map[string]any{"folder": "/data/doc", "confirm": true, "limit": 5})
	if number(t, out["deleted"], "deleted") != 4 || number(t, out["remaining"], "remaining") != 1 {
		t.Errorf("out = %v", out)
	}
	failed := objects(t, out["failed"], "failed")
	if len(failed) != 1 || text(failed[0]["id"]) != "f2" || text(failed[0]["path"]) != "/data/doc/Other Doc (2019)" {
		t.Errorf("failed = %v", failed)
	}
	for _, id := range []string{"o1", "o2", "o3", "f1"} {
		if o.holds(id) {
			t.Errorf("%s survived", id)
		}
	}
	// the path goes in Jellyfin's body, not the query
	for _, req := range o.requests("/Environment/ValidatePath") {
		if strings.Contains(req.Query, "Path=") {
			t.Errorf("Jellyfin was sent Emby's query: %s", req.Query)
		}
	}
}

// A server that takes a folder's items with it can carry off an item it would
// not delete on its own. Against a real server that left a run reporting
// seven failures where two were real, so what failed is read back before any
// of it is reported.
func TestItemOrphansDeleteReadsBackWhatFailed(t *testing.T) {
	t.Parallel()

	o := newOrphanServer(t, false)
	o.refuse["o3"] = true // the server will not delete it when it is named
	cs := session(t, o.fakeServer, Options{EnableDelete: true})

	out := mustCall(t, cs, "item_orphans_delete", map[string]any{"folder": "/data/doc", "confirm": true})
	if number(t, out["deleted"], "deleted") != 5 || number(t, out["remaining"], "remaining") != 0 || out["failed"] != nil {
		t.Errorf("an item that went with its folder was reported as a failure: %v", out)
	}
	if o.holds("o3") {
		t.Error("o3 is still there")
	}
}

// The folder an orphan is reported under is the deepest one holding it that
// the server can no longer see. Climbing to just below the top named /mnt
// for a disk that was mounted at /mnt/old, said the server could see it (it
// can: /mnt is there), lumped an unrelated removed tree in with it, and
// item_orphans_delete then refused the very folder the audit named.
func TestAuditOrphansNamesTheFolderThatIsGone(t *testing.T) {
	t.Parallel()

	o := newOrphanServerOf(t, false, []map[string]any{
		{"Name": "Films", "CollectionType": "movies", "ItemId": "lib1", "Locations": []string{"/media/films"}},
		{"Name": "TV", "CollectionType": "tvshows", "ItemId": "lib2", "Locations": []string{"/media/tv"}},
	})
	o.items = []orphanItem{
		{ID: "m1", Name: "Zzyzx Kept", Type: "Movie", Path: "/media/films/Zzyzx Kept (2001)/Zzyzx Kept (2001).mkv"},
		// a disk that was mounted at /mnt/old: the mount point is still
		// there, the library's folder on it is not
		{ID: "tv", Name: "tv", Type: "Folder", Path: "/mnt/old/tv", ParentID: "gone"},
		{ID: "s1", Name: "Zzyzx Show", Type: "Series", Path: "/mnt/old/tv/Zzyzx Show", ParentID: "tv"},
		{ID: "e1", Name: "Pilot", Type: "Episode", Path: "/mnt/old/tv/Zzyzx Show/Season 01/Zzyzx Show S01E01.mkv", ParentID: "s1"},
		// an unrelated removed tree beside it
		{ID: "f1", Name: "Zzyzx Film", Type: "Movie", Path: "/mnt/other/films/Zzyzx Film (2001)/Zzyzx Film (2001).mkv", ParentID: "gone"},
		{ID: "f2", Name: "Zzyzx Other", Type: "Movie", Path: "/mnt/other/films/Zzyzx Other (2002)/Zzyzx Other (2002).mkv", ParentID: "gone"},
		// and a recording still on disk, outside every library
		{ID: "r1", Name: "News", Type: "Video", Path: "/mnt/rec/news.ts"},
	}
	o.dirs = map[string]bool{"/media": true, "/media/films": true, "/media/tv": true, "/mnt": true, "/mnt/old": true, "/mnt/other": true, "/mnt/rec": true}
	o.files = map[string]bool{"/mnt/rec/news.ts": true}
	cs := session(t, o.fakeServer, Options{EnableDelete: true})

	out := mustCall(t, cs, "audit_orphans", nil)
	got := map[string]string{}
	items := map[string]int{}
	for _, g := range objects(t, out["folders"], "folders") {
		got[text(g["folder"])] = text(g["on_server"])
		items[text(g["folder"])] = number(t, g["items"], "items")
	}
	want := map[string]string{"/mnt/old/tv": "missing", "/mnt/other/films": "missing", "/mnt/rec": "present"}
	if len(got) != len(want) {
		t.Fatalf("folders = %v, want %v", got, want)
	}
	for folder, state := range want {
		if got[folder] != state {
			t.Errorf("%s: on_server = %q, want %q (folders %v)", folder, got[folder], state, got)
		}
	}
	if items["/mnt/old/tv"] != 3 || items["/mnt/other/films"] != 2 || items["/mnt/rec"] != 1 || number(t, out["total_findings"], "total_findings") != 6 {
		t.Errorf("items = %v", items)
	}

	// every folder the audit says is gone, item_orphans_delete takes as named
	for folder, state := range got {
		if state != folderMissing {
			continue
		}
		preview := mustCall(t, cs, "item_orphans_delete", map[string]any{"folder": folder})
		if number(t, preview["found"], "found") != items[folder] {
			t.Errorf("%s: the delete found %v, the audit %d", folder, preview["found"], items[folder])
		}
	}

	// and it still refuses what it always did: a folder the server can see,
	// one holding a library, one inside a library
	for folder, want := range map[string]string{
		"/mnt":                           "can still see",
		"/mnt/old":                       "can still see",
		"/mnt/rec":                       "can still see",
		"/media":                         "holds the",
		"/media/tv/Zzyzx Show":           "inside the TV library",
		"/media/films/Zzyzx Kept (2001)": "inside the Films library",
	} {
		if msg := mustRefuse(t, cs, "item_orphans_delete", map[string]any{"folder": folder, "confirm": true}); !strings.Contains(msg, want) {
			t.Errorf("folder %q: %s", folder, msg)
		}
	}

	gone := mustCall(t, cs, "item_orphans_delete", map[string]any{"folder": "/mnt/old/tv", "confirm": true})
	if number(t, gone["deleted"], "deleted") != 3 || number(t, gone["remaining"], "remaining") != 0 {
		t.Errorf("delete = %v", gone)
	}
	for _, id := range []string{"m1", "f1", "f2", "r1"} {
		if !o.holds(id) {
			t.Errorf("%s is gone, and was never under /mnt/old/tv", id)
		}
	}
}

// A check the server cannot answer is taken for neither answer: the folder
// is reported as unknown, with why, and nothing below it is guessed at.
func TestAuditOrphansSaysWhenTheServerCannotBeAsked(t *testing.T) {
	t.Parallel()

	o := newOrphanServerOf(t, false, []map[string]any{
		{"Name": "Films", "CollectionType": "movies", "ItemId": "lib1", "Locations": []string{"/media/films"}},
	})
	o.items = []orphanItem{
		{ID: "f1", Name: "Zzyzx Film", Type: "Movie", Path: "/mnt/old/films/Zzyzx Film (2001)/Zzyzx Film (2001).mkv", ParentID: "gone"},
		{ID: "f2", Name: "Zzyzx Other", Type: "Movie", Path: "/mnt/old/films/Zzyzx Other (2002)/Zzyzx Other (2002).mkv", ParentID: "gone"},
	}
	o.unanswered = true

	out := mustCall(t, session(t, o.fakeServer, Options{}), "audit_orphans", nil)
	folders := objects(t, out["folders"], "folders")
	if len(folders) != 1 || text(folders[0]["folder"]) != "/mnt/old/films" || text(folders[0]["on_server"]) != folderUnknown || !strings.Contains(text(folders[0]["note"]), "could not ask") {
		t.Errorf("folders = %v", folders)
	}
}
