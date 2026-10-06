package embyfin

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// renumberingPlaylist is an Emby 4.11 playlist: a moment after an entry
// is removed the entries are numbered 1 to n again (seen on 4.11.0.4), so
// an entry id read before names whatever entry now has it. With lazy set,
// the numbering lands just after that many reads instead, as when the reads
// come in that moment.
type renumberingPlaylist struct {
	items, entries []string
	lazy           int
}

func (p *renumberingPlaylist) renumber() {
	for i := range p.entries {
		p.entries[i] = strconv.Itoa(i + 1)
	}
}

func (p *renumberingPlaylist) routes(names map[string]string) map[string]route {
	return map[string]route{
		"GET /Playlists/p1/Items": func(*http.Request, string) (int, string) {
			rows := make([]string, 0, len(p.items))
			for i, id := range p.items {
				rows = append(rows, `{"Id":"`+id+`","Name":"`+names[id]+`","PlaylistItemId":"`+p.entries[i]+`"}`)
			}
			if p.lazy > 0 {
				p.lazy--
				if p.lazy == 0 {
					defer p.renumber()
				}
			}
			return http.StatusOK, `{"Items":[` + strings.Join(rows, ",") + `]}`
		},
		"DELETE /Playlists/p1/Items": func(r *http.Request, _ string) (int, string) {
			gone := strings.Split(r.URL.Query().Get("EntryIds"), ",")
			var items, entries []string
			for i := range p.items {
				if !slices.Contains(gone, p.entries[i]) {
					items, entries = append(items, p.items[i]), append(entries, p.entries[i])
				}
			}
			p.items, p.entries = items, entries
			p.renumber()
			return http.StatusNoContent, ""
		},
		"POST /Playlists/p1/Items/1/Move/0": noContent,
		"POST /Playlists/p1/Items/2/Move/0": noContent,
	}
}

// An entry id read before a removal names another entry after it on Emby
// 4.11, and the server takes a removal of it as a removal of that entry: the
// same removal sent twice took out Dune: Part Two and then Arrival. Each
// entry is named with the item it holds, and one holding another item now is
// refused, with nothing sent, for a removal and a move alike.
func TestAnEntryNumberedAgainIsNotTakenForAnother(t *testing.T) {
	t.Parallel()

	names := map[string]string{"dune": "Dune", "dune2": "Dune: Part Two", "arrival": "Arrival"}
	p := &renumberingPlaylist{items: []string{"dune", "dune2", "arrival"}, entries: []string{"1", "2", "3"}}
	c, f := newFake(t, Emby, p.routes(names))
	c.settle = time.Millisecond

	gone, err := c.RemoveFromPlaylist(t.Context(), "p1", EntriesToRemove{EntryIDs: []string{"2"}, ItemIDs: []string{"dune2"}})
	if err != nil || len(gone.Removed) != 1 || gone.Removed[0].Item.ID != "dune2" || gone.Removed[0].Position != 2 {
		t.Fatalf("removing Dune: Part Two = %+v, %v", gone, err)
	}
	if !slices.Equal(p.items, []string{"dune", "arrival"}) || !slices.Equal(p.entries, []string{"1", "2"}) {
		t.Fatalf("the playlist holds %v as entries %v; the test's server should hold Dune and Arrival, numbered again", p.items, p.entries)
	}

	// the same removal again: entry 2 is Arrival's now
	_, err = c.RemoveFromPlaylist(t.Context(), "p1", EntriesToRemove{EntryIDs: []string{"2"}, ItemIDs: []string{"dune2"}})
	if err == nil || !strings.Contains(err.Error(), "entry 2 holds Arrival (arrival) now, not item dune2; the playlist does not hold item dune2, so nothing was changed") {
		t.Errorf("removing an entry already removed = %v", err)
	}
	// and a move of an entry read before: Dune is entry 1 now
	err = c.MovePlaylistEntry(t.Context(), "p1", "u1", "2", "dune", "", 0)
	if err == nil || !strings.Contains(err.Error(), "entry 2 holds Arrival (arrival) now, not item dune; Dune (dune) is entry 1 now, so nothing was changed") {
		t.Errorf("moving an entry numbered again = %v", err)
	}
	if n := len(f.all("DELETE /Playlists/p1/Items")); n != 1 {
		t.Errorf("%d removals were sent, want the first alone", n)
	}
	if n := len(f.all("POST /Playlists/p1/Items/2/Move/0")); n != 0 {
		t.Errorf("%d moves were sent, want none", n)
	}
	if !slices.Equal(p.items, []string{"dune", "arrival"}) {
		t.Errorf("after the refusals the playlist holds %v, want Dune and Arrival", p.items)
	}
}

// The entries are numbered again just after they are read and before the
// removal lands, so the id checked names another entry when it arrives: the
// removal takes that entry out instead. It is not sent again, and the error
// names what was lost, what is still there, and the playlist as it is.
func TestARemovalThatTookAnotherEntrySaysSo(t *testing.T) {
	t.Parallel()

	names := map[string]string{"a": "Alien", "b": "Blade Runner"}
	// entry 1 was removed a moment ago and the two left are not yet
	// numbered again
	// (after both of the reads a removal checks the playlist with)
	p := &renumberingPlaylist{items: []string{"a", "b"}, entries: []string{"2", "3"}, lazy: 2}
	c, f := newFake(t, Emby, p.routes(names))
	c.settle = time.Millisecond

	_, err := c.RemoveFromPlaylist(t.Context(), "p1", EntriesToRemove{EntryIDs: []string{"2"}, ItemIDs: []string{"a"}})
	if !slices.Equal(p.items, []string{"a"}) {
		t.Fatalf("the playlist holds %v; the test's server should have taken Blade Runner out", p.items)
	}
	want := "after the removal of Alien (a) the playlist lost Blade Runner (b) too, which it was not asked to take out, and it still holds Alien (a), which it was asked to take out"
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "the playlist holds now, in order: Alien (a)") {
		t.Errorf("a removal that took another entry = %v, want it to say %q and what the playlist holds", err, want)
	}
	if n := len(f.all("DELETE /Playlists/p1/Items")); n != 1 {
		t.Errorf("%d removals were sent, want one: nothing more is guessed", n)
	}
}

// Emby 4.11 numbers a playlist's entries again a fraction of a second after
// a change, and a check made in that moment passed ids about to move. The
// playlist is read twice, a moment apart, and two reads that differ stop the
// removal and the move before anything is sent.
func TestAPlaylistChangingAsItIsReadIsLeftAlone(t *testing.T) {
	t.Parallel()

	names := map[string]string{"a": "Alien", "b": "Blade Runner"}
	for _, tc := range []struct {
		name string
		call func(c *Client) error
	}{
		{"a removal", func(c *Client) error {
			_, err := c.RemoveFromPlaylist(t.Context(), "p1", EntriesToRemove{EntryIDs: []string{"2"}, ItemIDs: []string{"a"}})
			return err
		}},
		{"a move", func(c *Client) error {
			return c.MovePlaylistEntry(t.Context(), "p1", "u1", "3", "b", "", 0)
		}},
	} {
		// numbered again just after the first of the two reads
		p := &renumberingPlaylist{items: []string{"a", "b"}, entries: []string{"2", "3"}, lazy: 1}
		c, f := newFake(t, Emby, p.routes(names))
		c.settle = time.Millisecond

		if err := tc.call(c); err == nil || !strings.Contains(err.Error(), "the playlist changed while it was read, a moment apart") || !strings.Contains(err.Error(), "so nothing was changed") {
			t.Errorf("%s while the entries were numbered again = %v", tc.name, err)
		}
		for _, r := range f.requests {
			if r.method != http.MethodGet {
				t.Errorf("%s sent %s %s", tc.name, r.method, r.path)
			}
		}
	}
}
