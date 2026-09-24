//go:build integration

package acceptance

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// fixtureVideo reads one of the videos scripts/testenv.sh made.
func fixtureVideo(t *testing.T, parts ...string) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(append([]string{dataDir()}, parts...)...)) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

// jsonLines reads a file of one JSON object per line.
func jsonLines(t *testing.T, path string) []map[string]any {
	t.Helper()

	raw, err := os.ReadFile(path) //nolint:gosec // a path the test chose
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("%s: a line that is not JSON: %v: %s", path, err, line)
		}
		out = append(out, row)
	}

	return out
}

// library_export writes what library_episodes and library_items answer, one
// row a line and field for field, into a file of the caller's choosing and
// nothing to the server. It will not write over a file, and saved_since
// narrows it to what the server saved since.
func TestLibraryExport(t *testing.T) {
	dir := t.TempDir()

	// the episodes of Shows, each line the row library_episodes answers
	path := filepath.Join(dir, "shows.jsonl")
	out := call(t, "library_export", map[string]any{"path": path, "library": "Shows"})
	paged := map[string]map[string]any{}
	for _, row := range rows(t, call(t, "library_episodes", map[string]any{"library": "Shows", "limit": 1000})["episodes"], "episodes") {
		paged[str(row["id"])] = row
	}
	lines := jsonLines(t, path)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := num(t, out["rows"], "rows"); n != len(paged) || n != len(lines) || n != 9 || str(out["shape"]) != "episode row" || num(t, out["bytes"], "bytes") != int(st.Size()) {
		t.Errorf("library_export = %v, for %d lines of %d bytes and %d library_episodes rows, want Shows' 9", out, len(lines), st.Size(), len(paged))
	}
	for _, line := range lines {
		if want := paged[str(line["id"])]; !reflect.DeepEqual(line, want) {
			t.Errorf("the line for %v differs from its library_episodes row:\nline %v\nrow  %v", line["id"], line, want)
		}
	}
	// no virtual episodes on either server, so every episode has its file
	if n := num(t, call(t, "library_export", map[string]any{"path": filepath.Join(dir, "all.jsonl"), "library": "Shows", "with_file": false})["rows"], "rows"); n != len(lines) {
		t.Errorf("with_file false wrote %d rows, want the same %d", n, len(lines))
	}

	// the films, each line the summary library_items answers
	films := filepath.Join(dir, "movies.jsonl")
	out = call(t, "library_export", map[string]any{"path": films, "library": "Movies", "types": "Movie"})
	listed := map[string]map[string]any{}
	for _, row := range rows(t, call(t, "library_items", map[string]any{"library": "Movies", "types": "Movie", "limit": 50})["items"], "items") {
		listed[str(row["id"])] = row
	}
	lines = jsonLines(t, films)
	if n := num(t, out["rows"], "rows"); n != 8 || len(lines) != 8 || len(listed) != 8 || str(out["shape"]) != "item summary" {
		t.Errorf("library_export types Movie = %v with %d lines, want the 8 films", out, len(lines))
	}
	for _, line := range lines {
		want := listed[str(line["id"])]
		for _, field := range []string{"name", "type", "year", "path", "metadata_provider_ids", "width", "height", "video_codec", "container", "size", "runtime_s", "audio"} {
			if !reflect.DeepEqual(line[field], want[field]) {
				t.Errorf("%v's %s is %v in the file and %v in library_items", line["name"], field, line[field], want[field])
			}
		}
	}

	// only the facts asked for, beside what names the episode
	narrow := filepath.Join(dir, "narrow.jsonl")
	call(t, "library_export", map[string]any{"path": narrow, "library": "Shows", "fields": []any{"height", "path"}})
	for _, line := range jsonLines(t, narrow) {
		if num(t, line["height"], "height") != 720 || str(line["path"]) == "" || str(line["id"]) == "" || str(line["series"]) == "" {
			t.Errorf("a narrowed line lacks what it asked for or what names it: %v", line)
		}
		for _, gone := range []string{"width", "size", "audio", "video_codec", "frame_rate"} {
			if line[gone] != nil {
				t.Errorf("a narrowed line carries %s: %v", gone, line)
			}
		}
	}

	// a path something is already at is refused, and left as it was
	before, err := os.ReadFile(path) //nolint:gosec // a path the test chose
	if err != nil {
		t.Fatal(err)
	}
	if msg := callErr(t, "library_export", map[string]any{"path": path, "library": "Shows"}); !strings.Contains(msg, "exists") {
		t.Errorf("writing over the file = %q", msg)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) { //nolint:gosec // same
		t.Error("the refused export changed the file")
	}

	// the one episode saved since an edit is the one line
	series := findItem(t, "Shows", "Series", "Breaking Bad")
	pilot := str(rows(t, call(t, "show_episodes", map[string]any{"series_id": series, "season": 1})["episodes"], "episodes")[0]["id"])
	start := time.Now().Add(-2 * time.Second).UTC().Format(time.RFC3339)
	call(t, "item_edit", map[string]any{"ids": []any{pilot}, "add_tags": []any{"zzyzx-export"}})
	t.Cleanup(func() {
		_, _ = invoke("item_edit", map[string]any{"ids": []any{pilot}, "remove_tags": []any{"zzyzx-export"}})
	})
	since := filepath.Join(dir, "since.jsonl")
	out = call(t, "library_export", map[string]any{"path": since, "library": "Shows", "saved_since": start})
	var ids []string
	for _, line := range jsonLines(t, since) {
		ids = append(ids, str(line["id"]))
	}
	if num(t, out["rows"], "rows") != 1 || !slices.Equal(ids, []string{pilot}) {
		t.Errorf("saved since the edit = %v (%v rows), want the edited pilot %s alone", ids, out["rows"], pilot)
	}
	// and library_episodes and library_items narrow the same way
	var eps []string
	for _, row := range rows(t, call(t, "library_episodes", map[string]any{"library": "Shows", "saved_since": start})["episodes"], "episodes") {
		eps = append(eps, str(row["id"]))
	}
	if !slices.Equal(eps, []string{pilot}) {
		t.Errorf("library_episodes saved since the edit = %v, want %s", eps, pilot)
	}
	var items []string
	for _, row := range rows(t, call(t, "library_items", map[string]any{"library": "Shows", "types": "Episode", "saved_since": start})["items"], "items") {
		items = append(items, str(row["id"]))
	}
	if !slices.Equal(items, []string{pilot}) {
		t.Errorf("library_items saved since the edit = %v, want %s", items, pilot)
	}
}

// The fixtures were probed at their scan and never rewritten, so the audit
// proves its sweep: every file counted, nothing reported.
func TestAuditQualityTrustsTheFixtures(t *testing.T) {
	out := call(t, "audit_quality", map[string]any{"library": "Shows"})
	if n := num(t, out["items_scanned"], "items_scanned"); n < 5 {
		t.Errorf("items_scanned = %d, want every episode", n)
	}
	if n := num(t, out["total_unprobed"], "total_unprobed"); n != 0 {
		t.Errorf("%d files reported unprobed: %v", n, out["unprobed"])
	}
	if n := num(t, out["total_replaced"], "total_replaced"); n != 0 {
		t.Errorf("%d files reported replaced: %v", n, out["replaced"])
	}
}
