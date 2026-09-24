package tools

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// library_export is a read tool that writes a file, and that is only safe
// while the file it writes is always a new one. A path anything is already at
// is refused and left exactly as it was - a file, or a link to one.
func TestLibraryExportNeverWritesOverAnything(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})
	dir := t.TempDir()

	// a file the caller cares about
	kept := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(kept, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if msg := mustRefuse(t, cs, "library_export", map[string]any{"path": kept, "library": "Shows"}); !strings.Contains(msg, "exists") || !strings.Contains(msg, "choose a path") {
		t.Errorf("an existing file said: %s", msg)
	}
	if raw, _ := os.ReadFile(kept); string(raw) != "keep me" { //nolint:gosec // a path this test chose
		t.Errorf("an existing file was touched: %q", raw)
	}

	// a link whose target does not exist yet: looking before writing saw
	// nothing there, and the create then followed the link and wrote a file
	// wherever it pointed
	target := filepath.Join(dir, "elsewhere", "target.jsonl")
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if msg := mustRefuse(t, cs, "library_export", map[string]any{"path": link, "library": "Shows"}); !strings.Contains(msg, "exists") {
		t.Errorf("a link said: %s", msg)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("the export was written through a link to %s", target)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link itself was replaced: %v %v", st, err)
	}

	// and a new file is made readable by its owner and group, not the world
	fresh := filepath.Join(dir, "new", "shows.jsonl")
	mustCall(t, cs, "library_export", map[string]any{"path": fresh, "library": "Shows"})
	st, err := os.Stat(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm&0o007 != 0 || perm&0o600 != 0o600 {
		t.Errorf("the export was made %v, want 0640", perm)
	}
	if st, err := os.Stat(filepath.Dir(fresh)); err != nil || st.Mode().Perm()&0o007 != 0 {
		t.Errorf("the folder was made %v, want 0750", st.Mode().Perm())
	}
}

// A sweep that fails part way leaves no file behind: half a library read as
// the whole of one is worse than none.
func TestLibraryExportLeavesNothingWhenTheSweepFails(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": []map[string]any{{"Name": "Shows", "CollectionType": "tvshows", "ItemId": "lib"}}, "TotalRecordCount": 1})
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "the database is locked", http.StatusInternalServerError)
	})
	cs := session(t, f, Options{})

	path := filepath.Join(t.TempDir(), "shows.jsonl")
	mustRefuse(t, cs, "library_export", map[string]any{"path": path, "library": "Shows"})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a failed export left a file behind: %v", err)
	}
}

// Two exports aimed at one path cannot both write it. Looking for a file and
// then creating one left a gap between the two, and every export that looked
// inside it found nothing and went on to truncate the others' file.
func TestTwoExportsToOnePathCannotBothWrite(t *testing.T) {
	t.Parallel()

	// a race either shows itself or does not on one try, so it is run often
	// enough that a gap would
	const rounds, racers = 20, 32
	dir := t.TempDir()
	for round := range rounds {
		path := filepath.Join(dir, strconv.Itoa(round), "shows.jsonl")

		var (
			start, done sync.WaitGroup
			mu          sync.Mutex
			won         int
		)
		start.Add(1)
		for range racers {
			done.Go(func() {
				start.Wait()
				f, err := createExport(path)
				if err != nil {
					return
				}
				_ = f.Close()
				mu.Lock()
				won++
				mu.Unlock()
			})
		}
		start.Done()
		done.Wait()

		if won != 1 {
			t.Fatalf("round %d: %d of %d exports created the same file, want exactly 1", round, won, racers)
		}
	}
}
