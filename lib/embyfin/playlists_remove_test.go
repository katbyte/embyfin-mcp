package embyfin

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// slot is a playlist entry in a test's Emby: the item and its entry id.
type slot struct{ item, entry string }

// removalPlaylist is an Emby playlist whose removals a test steers: onDelete
// runs in place of applying one when it is set, and gets the entries as they
// are and the entry ids named.
type removalPlaylist struct {
	entries  []slot
	names    map[string]string
	onDelete func(p *removalPlaylist, gone []string)
	deletes  [][]string
}

func (p *removalPlaylist) apply(gone []string) {
	p.entries = slices.DeleteFunc(p.entries, func(e slot) bool { return slices.Contains(gone, e.entry) })
}

func (p *removalPlaylist) items() []string {
	out := make([]string, 0, len(p.entries))
	for _, e := range p.entries {
		out = append(out, e.item)
	}

	return out
}

func (p *removalPlaylist) listing() []Item {
	out := make([]Item, 0, len(p.entries))
	for _, e := range p.entries {
		out = append(out, Item{ID: e.item, Name: p.names[e.item], PlaylistItemID: e.entry})
	}

	return out
}

func (p *removalPlaylist) client(t *testing.T) *Client {
	t.Helper()

	c, _ := newFake(t, Emby, map[string]route{
		"GET /Playlists/p1/Items": func(*http.Request, string) (int, string) {
			rows := make([]string, 0, len(p.entries))
			for _, e := range p.entries {
				rows = append(rows, `{"Id":"`+e.item+`","Name":"`+p.names[e.item]+`","PlaylistItemId":"`+e.entry+`"}`)
			}
			return http.StatusOK, `{"Items":[` + strings.Join(rows, ",") + `]}`
		},
		"DELETE /Playlists/p1/Items": func(r *http.Request, _ string) (int, string) {
			gone := strings.Split(r.URL.Query().Get("EntryIds"), ",")
			p.deletes = append(p.deletes, gone)
			if p.onDelete != nil {
				p.onDelete(p, gone)
			} else {
				p.apply(gone)
			}
			return http.StatusNoContent, ""
		},
	})
	c.settle = time.Millisecond

	return c
}

var removalNames = map[string]string{"alien": "Alien", "blade": "Blade Runner", "zodiac": "Zodiac", "dune": "Dune", "arrival": "Arrival"}

// Someone adds Zodiac while Alien is being removed. The removal checked the
// playlist for more than it should hold, and sent itself again for whatever
// that was: Zodiac went too, and the answer named only Alien. What appeared
// and was not asked for is left in, and said.
func TestARemovalLeavesWhatAppearedMeanwhile(t *testing.T) {
	t.Parallel()

	p := &removalPlaylist{names: removalNames, entries: []slot{{"alien", "1"}, {"blade", "2"}}}
	p.onDelete = func(p *removalPlaylist, gone []string) {
		p.apply(gone)
		if len(p.deletes) == 1 {
			p.entries = append(p.entries, slot{"zodiac", "3"})
		}
	}
	c := p.client(t)

	got, err := c.RemoveFromPlaylist(t.Context(), "p1", EntriesToRemove{EntryIDs: []string{"1"}, ItemIDs: []string{"alien"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Removed) != 1 || got.Removed[0].Item.ID != "alien" || len(got.Appeared) != 1 || got.Appeared[0].ID != "zodiac" {
		t.Errorf("the removal answered %+v, want Alien removed and Zodiac said to have appeared", got)
	}
	if len(p.deletes) != 1 || !slices.Equal(p.items(), []string{"blade", "zodiac"}) {
		t.Errorf("removals sent %v and the playlist holds %v; want one removal, and Zodiac kept", p.deletes, p.items())
	}
}

// A scan saves the playlist back as it found it, the removal lost: the
// removal is sent again for the entry at the place asked for. It took the
// last copy of an item the playlist holds twice, when the first was asked
// for, which left the playlist with the wrong one. A scan also numbers the
// entries again, so the entry is taken by its place, under its id now.
func TestARemovalSentAgainTakesTheCopyAskedFor(t *testing.T) {
	t.Parallel()

	p := &removalPlaylist{names: removalNames, entries: []slot{{"dune", "1"}, {"arrival", "2"}, {"dune", "3"}}}
	fingerprint := PlaylistFingerprint(p.listing())
	p.onDelete = func(p *removalPlaylist, gone []string) {
		if len(p.deletes) == 1 {
			// lost, and the entries numbered again by the scan's save
			for i := range p.entries {
				p.entries[i].entry = strconv.Itoa(i + 4)
			}
			return
		}
		p.apply(gone)
	}
	c := p.client(t)

	got, err := c.RemoveFromPlaylist(t.Context(), "p1", EntriesToRemove{EntryIDs: []string{"1"}, ItemIDs: []string{"dune"}, Fingerprint: fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Removed) != 1 || got.Removed[0].Position != 1 {
		t.Errorf("the removal answered %+v, want the first Dune", got)
	}
	if len(p.deletes) != 2 || !slices.Equal(p.deletes[1], []string{"4"}) {
		t.Errorf("removals sent %v, want the second for entry 4, the first Dune's id now", p.deletes)
	}
	if want := []slot{{"arrival", "5"}, {"dune", "6"}}; !slices.Equal(p.entries, want) {
		t.Errorf("the playlist holds %v, want %v", p.entries, want)
	}
}

// Put back in some other way than as it was, which of an item's entries was
// the one asked for cannot be told: the removal is not sent again for any.
func TestARemovalPutBackOtherwiseIsNotGuessedAt(t *testing.T) {
	t.Parallel()

	p := &removalPlaylist{names: removalNames, entries: []slot{{"dune", "1"}, {"arrival", "2"}}}
	p.onDelete = func(p *removalPlaylist, gone []string) {
		// Dune back, at the end
		p.apply(gone)
		p.entries = append(p.entries, slot{"dune", "3"})
	}
	c := p.client(t)

	_, err := c.RemoveFromPlaylist(t.Context(), "p1", EntriesToRemove{EntryIDs: []string{"1"}, ItemIDs: []string{"dune"}})
	if err == nil || !strings.Contains(err.Error(), "held Dune (dune) again, and not as it was before the removal") || !strings.Contains(err.Error(), "it was not sent again") {
		t.Errorf("a removal put back otherwise = %v", err)
	}
	if len(p.deletes) != 1 {
		t.Errorf("removals sent %v, want the one", p.deletes)
	}
}

// An item held more than once, with an entry id each: an old id of one of
// its entries could name another after a change, so the fingerprint of the
// playlist as read is needed, and one given for a playlist that has changed
// since is refused. The same removal sent twice with its fingerprint - the
// second time after Emby 4.11 numbered the entries again - is refused, where
// the entry id and the item alone would have taken the other Dune.
func TestAnItemHeldTwiceNeedsTheFingerprint(t *testing.T) {
	t.Parallel()

	p := &removalPlaylist{names: removalNames, entries: []slot{{"dune", "1"}, {"dune", "2"}, {"arrival", "3"}}}
	p.onDelete = func(p *removalPlaylist, gone []string) {
		p.apply(gone)
		for i := range p.entries {
			p.entries[i].entry = strconv.Itoa(i + 1)
		}
	}
	c := p.client(t)
	fingerprint := PlaylistFingerprint(p.listing())

	if _, err := c.RemoveFromPlaylist(t.Context(), "p1", EntriesToRemove{EntryIDs: []string{"1"}, ItemIDs: []string{"dune"}}); err == nil || !strings.Contains(err.Error(), "the playlist holds Dune (dune) more than once (entries 1, 2)") || !strings.Contains(err.Error(), "fingerprint") {
		t.Errorf("removing one of two Dunes without the fingerprint = %v", err)
	}
	if err := c.MovePlaylistEntry(t.Context(), "p1", "u1", "2", "dune", "", 2); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Errorf("moving one of two Dunes without the fingerprint = %v", err)
	}
	if _, err := c.RemoveFromPlaylist(t.Context(), "p1", EntriesToRemove{EntryIDs: []string{"1"}, ItemIDs: []string{"dune"}, Fingerprint: "0123456789abcdef"}); err == nil || !strings.Contains(err.Error(), "the playlist changed since it was read (its fingerprint was 0123456789abcdef and is "+fingerprint+" now)") {
		t.Errorf("removing with a fingerprint the playlist does not have = %v", err)
	}
	if len(p.deletes) != 0 {
		t.Fatalf("removals sent %v by refused calls", p.deletes)
	}

	if _, err := c.RemoveFromPlaylist(t.Context(), "p1", EntriesToRemove{EntryIDs: []string{"1"}, ItemIDs: []string{"dune"}, Fingerprint: fingerprint}); err != nil {
		t.Fatal(err)
	}
	_, err := c.RemoveFromPlaylist(t.Context(), "p1", EntriesToRemove{EntryIDs: []string{"1"}, ItemIDs: []string{"dune"}, Fingerprint: fingerprint})
	if err == nil || !strings.Contains(err.Error(), "the playlist changed since it was read") {
		t.Errorf("the same removal sent again = %v", err)
	}
	if len(p.deletes) != 1 || !slices.Equal(p.items(), []string{"dune", "arrival"}) {
		t.Errorf("removals sent %v and the playlist holds %v; want one, and a Dune left", p.deletes, p.items())
	}
}
