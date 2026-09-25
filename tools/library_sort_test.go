package tools

import (
	"net/http"
	"slices"
	"testing"
)

// Both servers answer a search in their own order of how well each item
// matches and ignore a sort asked for with it (seen live on both: "Dune",
// newest first, came back oldest first). A query with a sort is sorted and
// paged by the tool, over every match, by the sort's keys and the ones that
// settle a tie.
func TestLibraryItemsSortsASearch(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		// the server's order: best match first
		zzyzx := film("1", "Zzyzx", 2021)
		zzyzx["SortName"], zzyzx["DateCreated"] = "zzyzx", "2026-01-02T00:00:00Z"
		two := film("2", "Zzyzx: Part Two", 2024)
		two["SortName"], two["DateCreated"] = "zzyzx part two", "2026-01-03T00:00:00Z"
		older := film("3", "Zzyzx", 1984)
		older["SortName"], older["DateCreated"] = "zzyzx", "2026-01-01T00:00:00Z"
		writeJSON(t, w, page(zzyzx, two, older))
	})
	cs := session(t, f, Options{})

	for _, c := range []struct {
		args map[string]any
		want []string
	}{
		{map[string]any{"query": "zzyzx", "sort": "year", "desc": true}, []string{"2", "1", "3"}},
		{map[string]any{"query": "zzyzx", "sort": "year"}, []string{"3", "1", "2"}},
		// by name, the two of one name by when they were added: the later
		// first, as the whole order is reversed
		{map[string]any{"query": "zzyzx", "sort": "name", "desc": true}, []string{"2", "1", "3"}},
		{map[string]any{"query": "zzyzx", "sort": "name"}, []string{"3", "1", "2"}},
		// a page of the sorted matches
		{map[string]any{"query": "zzyzx", "sort": "year", "desc": true, "limit": 1, "offset": 1}, []string{"1"}},
	} {
		out := mustCall(t, cs, "library_items", c.args)
		var got []string
		for _, it := range objects(t, out["items"], "items") {
			got = append(got, text(it["id"]))
		}
		if !slices.Equal(got, c.want) || number(t, out["total"], "total") != 3 {
			t.Errorf("%v = %v of %v, want %v of 3", c.args, got, out["total"], c.want)
		}
	}
}
