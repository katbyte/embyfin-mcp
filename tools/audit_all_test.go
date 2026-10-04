package tools

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// One audit failing does not end audit_all. A proxy's 502 nearly three hours
// into a large library failed the whole call, and every other audit's count
// with it: now the failed audit is a row saying so, with its error, the rest
// count as they would have, failed names it, and total_findings leaves it
// out. Every row that ran says how long it took, and a skipped one does not.
func TestAuditAllKeepsTheRowsAroundAFailure(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library := map[string]any{"Name": "Zzyzx", "CollectionType": "movies", "ItemId": "lib", "Locations": []string{"/zz/films"}}
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page(library)) })
	films := []map[string]any{
		{"Id": "a", "Name": "Zzyzx One", "Type": "Movie", "Path": "/zz/films/Zzyzx One (2001)/Zzyzx One (2001).mkv"},
		{"Id": "b", "Name": "Zzyzx  Two", "Type": "Movie", "Path": "/zz/films/Zzyzx Two (2002)/Zzyzx Two (2002).mkv"},
	}
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// audit_quality's sweep, and only its, meets a proxy that has had
		// enough
		if strings.HasPrefix(param(q, "Fields"), "Path,ProductionYear,MediaSources,DateCreated,DateModified") {
			http.Error(w, "Bad Gateway", http.StatusBadGateway)

			return
		}
		var rows []map[string]any
		if types := param(q, "IncludeItemTypes"); types == "" || slices.Contains(strings.Split(types, ","), "Movie") {
			rows = films
		}
		start := startIndex(t, q)
		writeJSON(t, w, map[string]any{"Items": rows[min(start, len(rows)):], "TotalRecordCount": len(rows)})
	})
	adminView(t, f)

	out := mustCall(t, session(t, f, Options{}), "audit_all", map[string]any{})
	if failed := texts(out["failed"]); !slices.Equal(failed, []string{"audit_quality"}) {
		t.Fatalf("failed = %v, want audit_quality alone: %v", failed, out)
	}
	total, ran := 0, 0
	for _, row := range objects(t, out["audits"], "audits") {
		name := text(row["audit"])
		took, timed := row["took_s"].(float64)
		switch {
		case row["skipped"] != nil:
			if timed {
				t.Errorf("%s was skipped and says it took %v", name, took)
			}
		case name == "audit_quality":
			if row["failed"] == nil || !boolean(t, row["failed"], "failed") || !strings.Contains(text(row["error"]), "502") || !timed {
				t.Errorf("the failed audit's row = %v, want failed with the 502 and its time", row)
			}
		default:
			ran++
			if row["failed"] != nil || row["error"] != nil || !timed || took < 0 {
				t.Errorf("%s = %v, want its count and its time", name, row)
			}
			if name != "audit_unwatched" {
				total += number(t, row["findings"], "findings")
			}
		}
	}
	// every audit that counts over films still counted them, the name with
	// two spaces among what they found
	if ran < 10 || total == 0 || number(t, out["total_findings"], "total_findings") != total {
		t.Errorf("%d rows ran, totalling %d against total_findings %v", ran, total, out["total_findings"])
	}
}

// startIndex is where a page a fake answers begins: the StartIndex asked
// for, 0 when none is. One that is no number fails the test.
func startIndex(t *testing.T, q url.Values) int {
	t.Helper()

	raw := param(q, "StartIndex")
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		t.Errorf("StartIndex %q: %v", raw, err)
	}

	return n
}

// The rows audit_all skips over a library of a kind no audit reads are the
// rows it runs anywhere else, in their order: the list was written out by
// hand beside the steps, and an audit added to the steps (audit_whitespace)
// was missing from it.
func TestAuditAllNamesEveryRowItRuns(t *testing.T) {
	t.Parallel()

	for _, folder := range []*embyfin.VirtualFolder{nil, {Name: "Zzyzx Music", CollectionType: "music"}} {
		var steps []auditAllKey
		for _, s := range auditAllSteps(t.Context(), nil, "", folder) {
			steps = append(steps, auditAllKey{audit: s.audit, problems: s.problems})
		}
		if rows := auditAllRows(); !slices.Equal(steps, rows) {
			t.Errorf("library %v: audit_all runs %v, and names %v", folder, steps, rows)
		}
	}
}
