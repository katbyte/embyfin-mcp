//go:build integration

package acceptance

import (
	"testing"
)

// audit_provider walked a page at a time at its smallest, one lookup a call,
// through next_offset to the end: every messy film is scanned exactly once,
// and every one TMDB has a runtime for is asked about exactly once. The two
// messy Aliens share a sort name, and a server's sort by name leaves them in
// either order from one read to the next, which asked about one twice and
// the other never.
func TestAuditProviderWalksEveryFilmOnce(t *testing.T) {
	needsTMDBCassette(t)
	whole := call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "runtime"})
	want := map[string]int{}
	for _, f := range rows(t, whole["findings"], "findings") {
		want[str(f["id"])] = 1
	}
	if len(want) < 6 {
		t.Fatalf("one call finds %d films off, want the one-second files: %v", len(want), whole["findings"])
	}

	seen := map[string]int{}
	scanned, offset, calls := 0, 0, 0
	for {
		if calls++; calls > 3*messyMovies() {
			t.Fatal("the walk never ended")
		}
		out := call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "runtime", "max_lookups": 1, "offset": offset})
		scanned += num(t, out["items_scanned"], "items_scanned")
		for _, f := range rows(t, out["findings"], "findings") {
			seen[str(f["id"])]++
		}
		next, ok := out["next_offset"]
		if !ok {
			break
		}
		offset = num(t, next, "next_offset")
	}
	if scanned != messyMovies() {
		t.Errorf("the walk scanned %d films, want every one of the %d once", scanned, messyMovies())
	}
	for id := range want {
		if seen[id] != 1 {
			t.Errorf("film %s was asked about %d times, want once", id, seen[id])
		}
	}
	if len(seen) != len(want) {
		t.Errorf("the walk found %v, one call %v", seen, want)
	}

	// a handful by id, without a sweep
	one := findItem(t, "Messy Movies", "Movie", "Interstellar")
	out := call(t, "audit_provider", map[string]any{"ids": []any{one}, "checks": "runtime"})
	if num(t, out["items_scanned"], "items_scanned") != 1 || num(t, out["total_findings"], "total_findings") != 1 {
		t.Errorf("audit_provider by id = %v", out)
	}
}
