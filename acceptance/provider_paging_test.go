//go:build integration

package acceptance

import (
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"

	"github.com/katbyte/embyfin-mcp/lib/testenv"
)

// audit_provider walked a page at a time at its smallest, one lookup a call,
// through next_offset to the end: every messy film is scanned exactly once,
// and every one TMDB has a runtime for is asked about exactly once. Films
// sharing a sort name are what broke it: a server's sort by name leaves them
// in either order from one read to the next, which asked about one twice and
// the other never. A film is seen asked about by its finding, so two copies
// of Aliens are staged, one title and so one sort name, each cut to five
// seconds of TMDB's 137 minutes, beside the messy Arrival cut short.
func TestAuditProviderWalksEveryFilmOnce(t *testing.T) {
	needsTMDBCassette(t)
	if testenv.DataDir() == "" {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	cut := fixture(t, "messy-shows/Severance/Season 01/Severance S01E03.mp4")
	nfo := movieNfo("Aliens", 1986, "679", "tt0090605")
	stage(t, plus(2, 0, 0), map[string][]byte{
		"messy-movies/Aliens (1986)/Aliens (1986).mp4":             cut,
		"messy-movies/Aliens (1986)/movie.nfo":                     nfo,
		"messy-movies/Aliens (1986) 1080p/Aliens (1986) 1080p.mp4": cut,
		"messy-movies/Aliens (1986) 1080p/movie.nfo":               nfo,
	}, "messy-movies/Aliens (1986)", "messy-movies/Aliens (1986) 1080p")

	whole := suite.Call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "runtime"})
	want := map[string]int{}
	aliens := 0
	for _, f := range acc.Rows(t, whole["findings"], "findings") {
		want[acc.Str(f["id"])] = 1
		if title(acc.Str(f["name"])) == "Aliens" {
			aliens++
		}
	}
	if aliens != 2 || len(want) < 3 {
		t.Fatalf("one call finds %d films off, %d of them the staged Aliens: want both and Arrival: %v", len(want), aliens, whole["findings"])
	}

	seen := map[string]int{}
	scanned, offset, calls := 0, 0, 0
	for {
		if calls++; calls > 3*(messyMovies()+2) {
			t.Fatal("the walk never ended")
		}
		out := suite.Call(t, "audit_provider", map[string]any{"library": "Messy Movies", "checks": "runtime", "max_lookups": 1, "offset": offset})
		scanned += acc.Num(t, out["items_scanned"], "items_scanned")
		for _, f := range acc.Rows(t, out["findings"], "findings") {
			seen[acc.Str(f["id"])]++
		}
		next, ok := out["next_offset"]
		if !ok {
			break
		}
		offset = acc.Num(t, next, "next_offset")
	}
	if scanned != messyMovies()+2 {
		t.Errorf("the walk scanned %d films, want every one of the %d once", scanned, messyMovies()+2)
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
	one := findItem(t, "Messy Movies", "Movie", "Arrival")
	out := suite.Call(t, "audit_provider", map[string]any{"ids": []any{one}, "checks": "runtime"})
	if acc.Num(t, out["items_scanned"], "items_scanned") != 1 || acc.Num(t, out["total_findings"], "total_findings") != 1 {
		t.Errorf("audit_provider by id = %v", out)
	}
}
