package tools

import (
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// diskState is a canned server's disk: the paths it holds, folders ending
// in "/", and what a delete of each item takes off it.
type diskState struct {
	mu      sync.Mutex
	paths   map[string]bool
	deletes map[string][]string // item id -> paths the server removes (a folder takes what is under it)
	deleted []string
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

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		if it, ok := items[r.URL.Query().Get("Ids")]; ok {
			writeJSON(t, w, page(it))
			return
		}
		writeJSON(t, w, page())
	})
	f.mux.HandleFunc("GET /Users/u1/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, items[r.PathValue("id")])
	})
	f.mux.HandleFunc("GET /Environment/DirectoryContents", func(w http.ResponseWriter, r *http.Request) {
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
		w.WriteHeader(http.StatusNoContent)
	})

	return f
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

	for _, state := range []string{"Running", "Idle"} {
		const folder = "/zz/films/Zzyzx (2001)"
		items := map[string]map[string]any{
			"5": {"Id": "5", "Name": "Zzyzx", "Type": "Movie", "Path": folder + "/Zzyzx (2001).mkv"},
		}
		disk := &diskState{paths: map[string]bool{"/zz/": true, "/zz/films/": true, folder + "/": true, folder + "/Zzyzx (2001).mkv": true}, deletes: map[string][]string{"5": {folder + "/"}}}
		f := deleteServer(t, items, disk)
		f.mux.HandleFunc("GET /ScheduledTasks", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, []map[string]any{{"Id": "t1", "Name": "Scan media library", "Category": "Library", "State": state}})
		})
		r := &registry{client: f.client(t), settle: time.Millisecond, opts: Options{EnableDelete: true}}
		registerItemTools(r)
		cs := hostRegistry(t, r)

		out := mustCall(t, cs, "item_delete", map[string]any{"id": "5", "confirm": true})
		if said := strings.Contains(text(out["note"]), "a library scan was running"); said != (state == "Running") {
			t.Errorf("scan %s: note = %q", state, text(out["note"]))
		}
	}
}
